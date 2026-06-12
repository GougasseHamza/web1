package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	backendAddr = "127.0.0.1:9001"
	storageRoot = "/tmp/teamshelf/storage"
	uploadRoot  = "/tmp/teamshelf/uploads"
	adminUser   = "svc-audit"
	adminPass   = "ledger-drift-2026"

	maxUploadRequestBytes = 768 * 1024
	maxUploadFileBytes    = 512 * 1024
)

//go:embed web
var embeddedWeb embed.FS

type frontRequest struct {
	raw        []byte
	method     string
	target     string
	path       string
	headers    map[string]string
	closeAfter bool
}

type upstreamPool struct {
	mu   sync.Mutex
	conn net.Conn
	br   *bufio.Reader
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if err := seedStorage(); err != nil {
		log.Fatalf("seed storage: %v", err)
	}

	go startBackend()

	listenAddr := getenv("LISTEN_ADDR", ":8080")
	if err := startFrontend(listenAddr); err != nil {
		log.Fatalf("frontend: %v", err)
	}
}

func startBackend() {
	server := &http.Server{
		Addr:              backendAddr,
		Handler:           http.HandlerFunc(backendHandler),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("backend object service listening on %s", backendAddr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("backend: %v", err)
	}
}

func startFrontend(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("edge gateway listening on %s", addr)

	pool := &upstreamPool{}
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handleFrontendConn(conn, pool)
	}
}

func handleFrontendConn(conn net.Conn, pool *upstreamPool) {
	defer conn.Close()
	br := bufio.NewReader(conn)

	for {
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		req, err := readFrontRequest(br)
		if err != nil {
			if !errors.Is(err, io.EOF) && !isTimeout(err) {
				log.Printf("read client request: %v", err)
			}
			return
		}

		if strings.HasPrefix(req.path, "/admin") {
			writeEdgeResponse(conn, 404, "Not Found", "text/html; charset=utf-8", []byte(edgeNotFoundPage()))
			if req.closeAfter {
				return
			}
			continue
		}

		resp, err := pool.roundTrip(req.raw, req.closeAfter)
		if err != nil {
			log.Printf("upstream round trip: %v", err)
			writeEdgeResponse(conn, 502, "Bad Gateway", "text/plain; charset=utf-8", []byte("upstream unavailable\n"))
			return
		}

		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write(resp); err != nil {
			return
		}
		if req.closeAfter {
			return
		}
	}
}

func readFrontRequest(br *bufio.Reader) (*frontRequest, error) {
	header, err := readHeaderBlock(br, 64*1024)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(header), "\r\n")
	if len(lines) == 0 {
		return nil, errors.New("empty request")
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 3 {
		return nil, fmt.Errorf("malformed request line %q", lines[0])
	}

	headers := make(map[string]string)
	contentLength := 0
	for _, line := range lines[1:] {
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(name))
		val := strings.TrimSpace(value)
		headers[key] = val
		if key == "content-length" {
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 || n > 1024*1024 {
				return nil, fmt.Errorf("invalid content-length %q", val)
			}
			contentLength = n
		}
	}

	body := make([]byte, contentLength)
	if contentLength > 0 {
		if _, err := io.ReadFull(br, body); err != nil {
			return nil, err
		}
	}

	raw := append([]byte{}, header...)
	raw = append(raw, body...)
	return &frontRequest{
		raw:        raw,
		method:     fields[0],
		target:     fields[1],
		path:       requestPath(fields[1]),
		headers:    headers,
		closeAfter: strings.EqualFold(headers["connection"], "close"),
	}, nil
}

func requestPath(target string) string {
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		u, err := url.Parse(target)
		if err == nil {
			if u.RawQuery != "" {
				return u.EscapedPath() + "?" + u.RawQuery
			}
			if u.EscapedPath() != "" {
				return u.EscapedPath()
			}
			return "/"
		}
	}
	if target == "" || target[0] != '/' {
		return "/"
	}
	return target
}

func (u *upstreamPool) roundTrip(raw []byte, closeAfter bool) ([]byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := u.ensureConn(); err != nil {
			return nil, err
		}

		_ = u.conn.SetDeadline(time.Now().Add(8 * time.Second))
		if _, err := u.conn.Write(raw); err != nil {
			lastErr = err
			u.reset()
			continue
		}

		resp, err := readHTTPResponse(u.br)
		if err != nil {
			lastErr = err
			u.reset()
			continue
		}
		if closeAfter {
			u.reset()
		}
		return resp, nil
	}
	return nil, lastErr
}

func (u *upstreamPool) ensureConn() error {
	if u.conn != nil {
		return nil
	}
	conn, err := net.DialTimeout("tcp", backendAddr, 3*time.Second)
	if err != nil {
		return err
	}
	u.conn = conn
	u.br = bufio.NewReader(conn)
	return nil
}

func (u *upstreamPool) reset() {
	if u.conn != nil {
		_ = u.conn.Close()
	}
	u.conn = nil
	u.br = nil
}

func readHTTPResponse(br *bufio.Reader) ([]byte, error) {
	header, err := readHeaderBlock(br, 64*1024)
	if err != nil {
		return nil, err
	}
	bodyLen := responseBodyLength(header)
	body := make([]byte, bodyLen)
	if bodyLen > 0 {
		if _, err := io.ReadFull(br, body); err != nil {
			return nil, err
		}
	}
	resp := append([]byte{}, header...)
	resp = append(resp, body...)
	return resp, nil
}

func responseBodyLength(header []byte) int {
	lines := strings.Split(string(header), "\r\n")
	for _, line := range lines[1:] {
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err == nil && n > 0 {
				return n
			}
			return 0
		}
	}
	return 0
}

func readHeaderBlock(br *bufio.Reader, max int) ([]byte, error) {
	var buf bytes.Buffer
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		buf.Write(line)
		if buf.Len() > max {
			return nil, errors.New("header too large")
		}
		if bytes.Equal(line, []byte("\r\n")) || bytes.Equal(line, []byte("\n")) {
			return buf.Bytes(), nil
		}
	}
}

func writeEdgeResponse(w io.Writer, code int, reason, contentType string, body []byte) {
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\n", code, reason)
	fmt.Fprintf(w, "Server: TeamShelf-Edge/4.18\r\n")
	fmt.Fprintf(w, "Content-Type: %s\r\n", contentType)
	fmt.Fprintf(w, "Content-Length: %d\r\n", len(body))
	fmt.Fprintf(w, "Cache-Control: no-store\r\n")
	fmt.Fprintf(w, "Connection: keep-alive\r\n")
	fmt.Fprintf(w, "\r\n")
	_, _ = w.Write(body)
}

func backendHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "TeamShelf-ObjectStore/2.7")
	w.Header().Set("X-Backend-Node", "obj-eu-archive-03")
	w.Header().Set("Cache-Control", "no-store")

	switch r.URL.Path {
	case "/":
		writeAsset(w, "web/index.html", "text/html; charset=utf-8")
	case "/assets/style.css":
		writeAsset(w, "web/static/style.css", "text/css; charset=utf-8")
	case "/assets/app.js":
		writeAsset(w, "web/static/app.js", "application/javascript; charset=utf-8")
	case "/assets/mark.svg":
		writeAsset(w, "web/static/mark.svg", "image/svg+xml")
	case "/api/health":
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"edge":    "cache-warm",
			"backend": "obj-eu-archive-03",
		})
	case "/api/files":
		writeJSON(w, http.StatusOK, []map[string]string{
			{"name": "Q2 renewal matrix", "owner": "Finance", "path": "contracts/q2-renewals.txt", "updated": "2026-06-07"},
			{"name": "Workspace onboarding", "owner": "Engineering", "path": "teams/engineering/onboarding.txt", "updated": "2026-06-03"},
			{"name": "Sync audit log", "owner": "Platform", "path": "audit/2026-06-sync.log", "updated": "2026-06-08"},
		})
	case "/api/sync":
		writeText(w, http.StatusOK, "queued\n")
	case "/download":
		handleDownload(w, r)
	case "/upload":
		handleUpload(w, r)
	case "/admin":
		handleAdminIndex(w, r)
	case "/admin/archive":
		handleArchiveEndpoint(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "object not found",
			"path":  r.URL.Path,
		})
	}
}

func writeAsset(w http.ResponseWriter, name, contentType string) {
	b, err := embeddedWeb.ReadFile(name)
	if err != nil {
		writeText(w, http.StatusNotFound, "missing asset\n")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	body, err := safeReadStorage(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not available"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(id)))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		writeUploadReviewNotice(w, "Upload intake accepts multipart PDF submissions only.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadRequestBytes)
	if err := r.ParseMultipartForm(64 * 1024); err != nil {
		writeUploadReviewNotice(w, "The document could not be parsed by the PDF intake scanner.")
		return
	}

	file, header, err := r.FormFile("document")
	if err != nil {
		writeUploadReviewNotice(w, "No document field was found in the upload request.")
		return
	}
	defer file.Close()

	name := sanitizedUploadName(header.Filename)
	if name == "" || !strings.EqualFold(filepath.Ext(name), ".pdf") {
		writeUploadReviewNotice(w, "The document failed extension validation. TeamShelf intake accepts PDF files only.")
		return
	}

	if header.Size <= 0 || header.Size > maxUploadFileBytes {
		writeUploadReviewNotice(w, "The document failed size validation for the PDF intake queue.")
		return
	}

	body, err := io.ReadAll(io.LimitReader(file, maxUploadFileBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxUploadFileBytes {
		writeUploadReviewNotice(w, "The document could not be safely scanned by the PDF intake queue.")
		return
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		writeUploadReviewNotice(w, "The document failed PDF header verification.")
		return
	}

	objectID, err := storeAcceptedUpload(body)
	if err != nil {
		log.Printf("store upload: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload queue unavailable"})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":   "queued",
		"filename": name,
		"object":   "incoming/" + objectID + ".pdf",
		"message":  "PDF accepted for retention review.",
	})
}

func writeUploadReviewNotice(w http.ResponseWriter, reason string) {
	writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{
		"error":  "upload rejected",
		"reason": reason,
		"notice": "Suspicious files are routed to the Admin Review queue: /admin?queue=upload-review",
	})
}

func sanitizedUploadName(name string) string {
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if base == "." || base == "/" {
		return ""
	}
	base = strings.TrimSpace(base)
	if len(base) > 120 {
		return ""
	}
	for _, r := range base {
		if r < 32 || r == 127 {
			return ""
		}
	}
	return base
}

func storeAcceptedUpload(body []byte) (string, error) {
	if err := os.MkdirAll(uploadRoot, 0o750); err != nil {
		return "", err
	}
	for attempt := 0; attempt < 4; attempt++ {
		id, err := randomHex(12)
		if err != nil {
			return "", err
		}
		target := filepath.Join(uploadRoot, id+".pdf")
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.Write(body); err != nil {
			_ = f.Close()
			_ = os.Remove(target)
			return "", err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(target)
			return "", err
		}
		return id, nil
	}
	return "", errors.New("upload id collision")
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func handleAdminIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("queue") != "upload-review" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "admin queue not found"})
		return
	}

	body := `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>TeamShelf Admin Operations</title>
  <link rel="stylesheet" href="/assets/style.css">
</head>
<body class="admin">
  <main class="admin-panel">
    <p class="eyebrow">internal operations</p>
    <h1>Upload Review Queue</h1>
    <p>Suspicious upload attempts are reviewed here before document-retention restores are approved.</p>
    <dl class="secrets">
      <dt>Audit service credentials</dt>
      <dd><code>svc-audit:ledger-drift-2026</code></dd>
      <dt>Archive restore console</dt>
      <dd><code>GET /admin/archive</code></dd>
      <dt>Operator note</dt>
      <dd>The archive restore reader is pinned to the legacy audit service. Restore clients still pass the archive object selector in the URL query string.</dd>
    </dl>
    <p class="muted">This console is reachable only from the edge-to-object service path.</p>
  </main>
</body>
</html>`
	writeHTML(w, http.StatusOK, body)
}

func handleArchiveEndpoint(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || user != adminUser || pass != adminPass {
		w.Header().Set("WWW-Authenticate", `Basic realm="TeamShelf audit archive"`)
		writeTextStatus(w, http.StatusUnauthorized, "authentication required\n", "text/plain; charset=utf-8")
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}

	// Deliberately vulnerable legacy behavior: the admin reader cleans the path
	// after concatenation but never verifies that the result stays under storageRoot.
	target := filepath.Clean(storageRoot + "/" + id)
	body, err := os.ReadFile(target)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "archive object not found",
			"id":    id,
		})
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(target)))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func safeReadStorage(id string) ([]byte, error) {
	cleanID := filepath.Clean("/" + id)
	target := filepath.Join(storageRoot, strings.TrimPrefix(cleanID, "/"))
	rel, err := filepath.Rel(storageRoot, target)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, errors.New("path escapes storage root")
	}
	return os.ReadFile(target)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	body, _ := json.MarshalIndent(v, "", "  ")
	body = append(body, '\n')
	writeTextStatus(w, code, string(body), "application/json; charset=utf-8")
}

func writeText(w http.ResponseWriter, code int, body string) {
	writeTextStatus(w, code, body, "text/plain; charset=utf-8")
}

func writeHTML(w http.ResponseWriter, code int, body string) {
	writeTextStatus(w, code, body, "text/html; charset=utf-8")
}

func writeTextStatus(w http.ResponseWriter, code int, body, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

func seedStorage() error {
	files := map[string]string{
		"contracts/q2-renewals.txt":        "TeamShelf renewal matrix\nOwner: Finance\nStatus: legal review\n",
		"teams/engineering/onboarding.txt": "Engineering onboarding\n- Request workspace access\n- Enroll hardware key\n- Review storage retention policy\n",
		"audit/2026-06-sync.log":           "2026-06-08T22:14:09Z sync job queued by svc-audit\n2026-06-08T22:15:33Z archive reader migration deferred\n",
	}
	for name, body := range files {
		target := filepath.Join(storageRoot, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func edgeNotFoundPage() string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Not Found</title><link rel="stylesheet" href="/assets/style.css"></head><body class="edge-error"><main><h1>Not Found</h1><p>The requested object is not published on this workspace edge.</p></main></body></html>`
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
