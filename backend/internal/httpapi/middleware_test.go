package httpapi

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

func newTestRouter(mw ...gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(mw...)
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	r.POST("/echo", func(c *gin.Context) {
		body, err := c.GetRawData()
		if err != nil {
			c.String(http.StatusRequestEntityTooLarge, "too large")
			return
		}
		c.String(http.StatusOK, "%d", len(body))
	})
	return r
}

func TestRequestIDIsAssignedAndEchoed(t *testing.T) {
	r := newTestRouter(RequestID())

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))
	first := w.Header().Get("X-Request-Id")
	if first == "" {
		t.Fatal("no request id assigned")
	}

	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if w2.Header().Get("X-Request-Id") == first {
		t.Fatal("two requests were given the same id")
	}

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Request-Id", "caller-supplied-id")
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req)
	if got := w3.Header().Get("X-Request-Id"); got != "caller-supplied-id" {
		t.Fatalf("caller-supplied id not preserved: %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Request-Id", strings.Repeat("x", 500))
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, req)
	if got := w4.Header().Get("X-Request-Id"); len(got) > 64 {
		t.Fatalf("an oversized caller id was echoed back: %d chars", len(got))
	}
}

func TestSecurityHeaders(t *testing.T) {
	r := newTestRouter(SecurityHeaders())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Fatalf("%s = %q, want %q", k, got, v)
		}
	}
	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("no Content-Security-Policy set")
	}
	for _, forbidden := range []string{"unsafe-inline", "unsafe-eval", "*"} {
		if strings.Contains(csp, forbidden) {
			t.Fatalf("CSP contains %q, which defeats the point: %s", forbidden, csp)
		}
	}
	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("HSTS asserted over a plaintext request: %q", got)
	}
}

func TestHSTSOnlyOverTLS(t *testing.T) {
	r := newTestRouter(SecurityHeaders())

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("no HSTS on a TLS request")
	}

	req = httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("no HSTS behind a TLS-terminating proxy")
	}
}

func TestBodyLimit(t *testing.T) {
	r := newTestRouter(BodyLimit(1024))

	small := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(strings.Repeat("a", 512)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, small)
	if w.Code != http.StatusOK || w.Body.String() != "512" {
		t.Fatalf("a body inside the limit was rejected: %d %s", w.Code, w.Body.String())
	}

	big := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(strings.Repeat("a", 4096)))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, big)
	if w.Code == http.StatusOK {
		t.Fatalf("a body past the limit was accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestCORSAllowsOnlyConfiguredOrigins(t *testing.T) {
	r := newTestRouter(CORS([]string{"https://guard.example.ch"}))

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "https://guard.example.ch")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://guard.example.ch" {
		t.Fatalf("configured origin not allowed: %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("an unconfigured origin was allowed: %q", got)
	}

	// A near-miss must not match: prefix or suffix comparison is the classic CORS bug.
	for _, origin := range []string{
		"https://guard.example.ch.evil.example",
		"https://evil.example/guard.example.ch",
		"http://guard.example.ch",
	} {
		req = httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.Header.Set("Origin", origin)
		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("origin %q matched the allow-list: %q", origin, got)
		}
	}
}

func TestCORSPreflight(t *testing.T) {
	r := newTestRouter(CORS([]string{"https://guard.example.ch"}))
	r.OPTIONS("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodOptions, "/ping", nil)
	req.Header.Set("Origin", "https://guard.example.ch")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("allowed preflight returned %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodOptions, "/ping", nil)
	req.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("preflight from an unconfigured origin returned %d, want 403", w.Code)
	}
}

func TestCORSEmptyListIsSameOriginOnly(t *testing.T) {
	r := newTestRouter(CORS(nil))
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "https://guard.example.ch")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("an empty allow-list emitted a CORS header: %q", got)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("a same-origin request was blocked: %d", w.Code)
	}
}

func TestRateLimitMiddlewareReturns429(t *testing.T) {
	r := newTestRouter(RequestID(), RateLimit(NewRateLimiter(2, 100)))
	codes := []int{}
	for i := 0; i < 4; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))
		codes = append(codes, w.Code)
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("requests inside the budget were rejected: %v", codes)
	}
	if codes[2] != http.StatusTooManyRequests || codes[3] != http.StatusTooManyRequests {
		t.Fatalf("requests past the budget were not rate limited: %v", codes)
	}
}

// TestRateLimitCannotBeEscapedByAForgedHeader: with no trusted proxies the client address is the
// socket peer, so setting X-Forwarded-For must not hand the caller a fresh bucket.
func TestRateLimitCannotBeEscapedByAForgedHeader(t *testing.T) {
	r := newTestRouter(RateLimit(NewRateLimiter(2, 100)))
	blocked := false
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.Header.Set("X-Forwarded-For", "10.0.0."+string(rune('1'+i)))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("a forged X-Forwarded-For gave the caller unlimited buckets")
	}
}
