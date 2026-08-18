package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// accessLogRouter mounts one parameterised route behind the access log and captures what the
// logger emitted. Nothing here touches the database: the middleware reads the request and the
// route table and nothing else.
func accessLogRouter(t *testing.T) (*gin.Engine, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	s := &Server{
		log: slog.New(slog.NewJSONHandler(&buf, nil)),
		now: time.Now,
	}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(RequestID(), s.accessLog())
	r.GET("/api/v1/devices/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r, &buf
}

// loggedPathOf drives one request and returns the "path" field of the line the access log wrote.
// It fails rather than returns a zero value when there is no line: a helper that reported "" for
// "the logger never ran" would make every assertion below pass for the wrong reason.
func loggedPathOf(t *testing.T, target string) string {
	t.Helper()
	r, buf := accessLogRouter(t)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatalf("the access log wrote nothing for %q; there is no field to assert about", target)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("access log line is not JSON: %v (line: %s)", err, line)
	}
	p, ok := rec["path"].(string)
	if !ok {
		t.Fatalf("access log line has no string \"path\" field: %s", line)
	}
	return p
}

// A matched route logs its TEMPLATE. This is the half that must not regress while fixing the
// other: the id in the URL is a device identifier, and the log is not where it belongs.
func TestAccessLogRecordsTheRouteTemplateNotTheIdentifier(t *testing.T) {
	got := loggedPathOf(t, "/api/v1/devices/9f1c3b0e-0000-4000-8000-000000000000")
	if got != "/api/v1/devices/:id" {
		t.Fatalf("path = %q, want the route template %q", got, "/api/v1/devices/:id")
	}
	if strings.Contains(got, "9f1c3b0e") {
		t.Fatalf("path = %q — the device id reached the log", got)
	}
}

// The regression this file exists for. `c.FullPath()` is empty for a request that matched no
// route, so every 404 used to log `"path":""` — a line that cannot distinguish a scanner probing
// /wp-login.php from a broken link in our own console.
func TestAccessLogSaysWhatFourOhFoured(t *testing.T) {
	got := loggedPathOf(t, "/wp-login.php")
	if got == "" {
		t.Fatal(`path = "" for an unmatched request: the 404 line reports nothing at all`)
	}
	if got != "/wp-login.php" {
		t.Fatalf("path = %q, want %q", got, "/wp-login.php")
	}
}

// The query string is where a code, a token or a state parameter travels. It is dropped, and this
// asserts the dropping rather than assuming url.URL.Path does it.
func TestAccessLogDropsTheQueryString(t *testing.T) {
	got := loggedPathOf(t, "/api/v1/auth/google/callback?code=super-secret-authorization-code&state=x")
	if strings.Contains(got, "super-secret-authorization-code") || strings.Contains(got, "code=") {
		t.Fatalf("path = %q — the query string reached the log", got)
	}
	if got != "/api/v1/auth/google/callback" {
		t.Fatalf("path = %q, want %q", got, "/api/v1/auth/google/callback")
	}
}

// An unmatched path is attacker-controlled and unbounded, so it is truncated. Without this a
// request line can decide how big a log record is.
func TestAccessLogTruncatesALongUnmatchedPath(t *testing.T) {
	got := loggedPathOf(t, "/"+strings.Repeat("a", 500))
	if len(got) > 120 {
		t.Fatalf("path is %d bytes; a 500-byte request path was not truncated", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("path = %q — truncation is not marked, so a truncated path reads as the whole path", got)
	}
}

// Control characters are replaced. slog's JSON handler escapes them correctly, so this is not the
// only thing standing between a crafted path and a corrupted log — it is what keeps that true for
// a reader that is less careful.
func TestAccessLogStripsControlCharactersFromAnUnmatchedPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.URL.Path = "/ev\nil\r\x00"
	r, buf := accessLogRouter(t)
	r.ServeHTTP(httptest.NewRecorder(), req)

	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &rec); err != nil {
		t.Fatalf("access log line is not JSON: %v", err)
	}
	got, _ := rec["path"].(string)
	if strings.ContainsAny(got, "\n\r\x00") {
		t.Fatalf("path = %q still carries control characters", got)
	}
	if got != "/ev?il??" {
		t.Fatalf("path = %q, want %q", got, "/ev?il??")
	}
}
