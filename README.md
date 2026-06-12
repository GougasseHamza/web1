# Lab Description

TeamShelf Edge is a web application security lab about HTTP/1.1 request smuggling in a realistic cloud storage platform. Learners interact with a public file workspace and PDF-only upload intake, discover the admin review queue from a failed upload notice, then use a CL.TE desync to make the backend process requests the gateway never authorized.

# Overview of the TeamShelf storage solution

TeamShelf is a fictional managed storage product for internal teams. The lab models a common production pattern: an edge gateway keeps a warm upstream connection to an object service, while the object service exposes a backend-only upload review and archive maintenance console. The gateway and backend disagree on how to frame a request containing both `Content-Length` and `Transfer-Encoding: chunked`, allowing a smuggled request to reach the admin review queue after the upload workflow reveals it.

# MITRE ATT&CK Techniques Used

| Tactic | ID | Technique | Lab usage |
|---|---|---|---|
| Initial Access | T1190 | Exploit Public-Facing Application | Exploit the public TeamShelf edge gateway's CL.TE request parsing flaw. |
| Collection | T1005 | Data from Local System | Read `/home/local.txt` through the vulnerable backend archive reader. |

# Sources

- PortSwigger Web Security Academy, HTTP request smuggling, https://portswigger.net/web-security/request-smuggling
- PortSwigger Research, HTTP Desync Attacks: Request Smuggling Reborn, https://portswigger.net/research/http-desync-attacks-request-smuggling-reborn
- PayloadsAllTheThings, Request Smuggling, https://swisskyrepo.github.io/PayloadsAllTheThings/Request%20Smuggling/
- OWASP, Path Traversal, https://owasp.org/www-community/attacks/Path_Traversal
- MITRE ATT&CK T1190, Exploit Public-Facing Application, https://attack.mitre.org/techniques/T1190/
- MITRE ATT&CK T1005, Data from Local System, https://attack.mitre.org/techniques/T1005/
