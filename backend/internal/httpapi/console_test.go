package httpapi

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// consoleRouter mounts only the console. mountConsole reads nothing off the Server, so a zero value
// is enough — and using one keeps this test from needing a database to assert what a static file
// handler does.
func consoleRouter(t *testing.T) *gin.Engine {
	t.Helper()
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.NoRoute(func(c *gin.Context) {
		failWith(c, http.StatusNotFound, "not_found", "no such endpoint")
	})
	if err := (&Server{}).mountConsole(r); err != nil {
		t.Fatalf("mountConsole: %v", err)
	}
	return r
}

func get(t *testing.T, r *gin.Engine, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// wantContentType is written out here rather than read from the handler's own contentTypes map.
// Comparing the server against its own table is the shape of check that passes whatever the table
// says — measured: flipping the manifest to text/plain left this test green until it was rewritten.
var wantContentType = map[string]string{
	"/":                     "text/html",
	"/index.html":           "text/html",
	"/app.css":              "text/css",
	"/app.js":               "text/javascript",
	"/manifest.webmanifest": "application/manifest+json",
	"/icon.svg":             "image/svg+xml",
}

func TestConsoleServesEveryRoute(t *testing.T) {
	r := consoleRouter(t)

	// A route the handler serves but this test does not know about would otherwise go unchecked.
	if len(wantContentType) != len(consoleRoutes) {
		t.Fatalf("this test knows %d routes, the server mounts %d", len(wantContentType), len(consoleRoutes))
	}

	for route, want := range wantContentType {
		t.Run(route, func(t *testing.T) {
			if _, mounted := consoleRoutes[route]; !mounted {
				t.Fatalf("%s is not mounted at all", route)
			}
			w := get(t, r, route, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("%s: status %d", route, w.Code)
			}
			if w.Body.Len() == 0 {
				t.Fatalf("%s: served an empty body", route)
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, want) {
				t.Fatalf("%s: content type %q, want %q", route, ct, want)
			}
			if w.Header().Get("ETag") == "" {
				t.Fatalf("%s: no ETag, so every load re-downloads the file", route)
			}
			if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
				t.Fatalf("%s: Cache-Control %q, want no-cache", route, cc)
			}
		})
	}
}

func TestConsoleRevalidatesWithETag(t *testing.T) {
	r := consoleRouter(t)

	first := get(t, r, "/app.js", nil)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the first response")
	}

	same := get(t, r, "/app.js", map[string]string{"If-None-Match": etag})
	if same.Code != http.StatusNotModified {
		t.Fatalf("matching ETag got %d, want 304", same.Code)
	}
	if same.Body.Len() != 0 {
		t.Fatalf("304 carried a %d byte body", same.Body.Len())
	}

	// The negative half. Without it, a handler that answered 304 unconditionally would pass the
	// check above and serve a stale console forever.
	stale := get(t, r, "/app.js", map[string]string{"If-None-Match": `"0000000000000000"`})
	if stale.Code != http.StatusOK || stale.Body.Len() == 0 {
		t.Fatalf("a non-matching ETag got %d with %d bytes, want 200 with the file",
			stale.Code, stale.Body.Len())
	}
}

// TestConsoleDoesNotSwallowUnknownPaths: a catch-all static handler would answer HTML with 200 here,
// and a caller hitting a mistyped API route would parse a web page as its error body.
func TestConsoleDoesNotSwallowUnknownPaths(t *testing.T) {
	r := consoleRouter(t)

	w := get(t, r, "/api/v1/typo", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown path got %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "<!DOCTYPE html>") {
		t.Fatal("an unknown path was answered with the console's HTML")
	}
}

// TestConsoleReferencesOnlyMountedAssets catches the failure that only shows up on a real phone:
// index.html gains a stylesheet or a script, nobody registers the route, and the page loads with a
// silent 404 for the file that made it usable.
// FR-13.3's second half: no dependency on an input a phone does not have.
//
// True today by construction — the console has no `:hover` rule, no context menu, no double-click
// and no access key anywhere — which is precisely why it is worth a test. A requirement that holds
// because nobody happened to write the offending line is a requirement that stops holding the first
// time somebody does, and a hover-only affordance is invisible to every other guard here: it renders,
// it lays out, it passes the mobile suite, and it is simply unreachable with a thumb.
//
// The list is deliberately the *desktop-only* inputs, not "everything a mouse can do". A phone has a
// keyboard, so key handling is not banned; a phone has no pointer that can rest somewhere, so hover
// is. If a hover enhancement is ever wanted on top of a working tap path, this test is where that
// decision gets recorded rather than assumed.
func TestConsoleNeedsNoDesktopOnlyInput(t *testing.T) {
	r := consoleRouter(t)

	// Scanned from the routes rather than the embedded FS, so an asset that stopped being served
	// cannot quietly stop being checked, and one that is served without appearing in index.html is
	// still covered.
	assets := []string{"/", "/app.css", "/app.js"}
	forbidden := []struct {
		what string
		pat  *regexp.Regexp
	}{
		{"a :hover rule, which a touch screen cannot produce", regexp.MustCompile(`:hover\b`)},
		{"a contextmenu handler, which needs a right button", regexp.MustCompile(`\bcontextmenu\b`)},
		{"a double-click handler", regexp.MustCompile(`\bdblclick\b`)},
		{"an accesskey, which needs a hardware modifier", regexp.MustCompile(`(?i)\baccesskey\s*=`)},
		{"a mouse-only pointer event", regexp.MustCompile(`\b(?:mouseover|mouseenter|mouseleave)\b`)},
	}

	scanned := 0
	for _, path := range assets {
		w := get(t, r, path, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s is answered with %d, so this scan is not reading the console at all", path, w.Code)
		}
		body := w.Body.String()
		if len(body) == 0 {
			t.Fatalf("%s is empty; a scan over no bytes finds nothing and reports success", path)
		}
		scanned += len(body)
		for _, f := range forbidden {
			if f.pat.MatchString(body) {
				t.Errorf("%s declares %s", path, f.what)
			}
		}
	}
	// The positive control for the scan itself: the three assets together are tens of kilobytes, and
	// a router change that answered every path with an empty 200 would otherwise read as clean.
	if scanned < 4096 {
		t.Fatalf("the scan read only %d bytes across %d assets, which is too little to be the console",
			scanned, len(assets))
	}
}

func TestConsoleReferencesOnlyMountedAssets(t *testing.T) {
	r := consoleRouter(t)
	html := get(t, r, "/", nil).Body.String()

	refs := regexp.MustCompile(`(?:src|href)="(/[^"]*)"`).FindAllStringSubmatch(html, -1)
	if len(refs) == 0 {
		t.Fatal("no asset references found in index.html; the extraction is not measuring anything")
	}
	for _, m := range refs {
		ref := m[1]
		if strings.HasPrefix(ref, "/api/") {
			continue // an API endpoint, not a static asset
		}
		if w := get(t, r, ref, nil); w.Code != http.StatusOK {
			t.Fatalf("index.html references %s, which the server answers with %d", ref, w.Code)
		}
	}
}

// TestConsoleHasNoInlineScriptOrStyle guards the CSP contract from the other side.
//
// The policy is `script-src 'self'; style-src 'self'` with no 'unsafe-inline', so an inline handler
// or a style attribute does not error — the browser silently drops it. A button whose onclick never
// runs looks exactly like a button whose handler has a bug.
func TestConsoleHasNoInlineScriptOrStyle(t *testing.T) {
	r := consoleRouter(t)
	html := get(t, r, "/", nil).Body.String()

	if regexp.MustCompile(`<script[^>]*>\s*[^<\s]`).MatchString(html) {
		t.Fatal("index.html contains an inline <script> body, which CSP will refuse to execute")
	}
	if regexp.MustCompile(`\son[a-z]+\s*=`).MatchString(html) {
		t.Fatal("index.html contains an inline event handler attribute, which CSP will not run")
	}
	if regexp.MustCompile(`\sstyle\s*=\s*"`).MatchString(html) {
		t.Fatal("index.html contains a style attribute, which CSP will not apply")
	}
}

// TestConsoleDeclaresTheMobileViewport is the single line that decides whether the console is usable
// on a phone: without it the browser lays out at 980px and scales down, so every 44px tap target
// becomes about 16px and the mobile stylesheet never applies. Nothing about the page looks broken.
func TestConsoleDeclaresTheMobileViewport(t *testing.T) {
	html := get(t, consoleRouter(t), "/", nil).Body.String()

	meta := regexp.MustCompile(`<meta name="viewport" content="([^"]*)"`).FindStringSubmatch(html)
	if meta == nil {
		t.Fatal("no viewport meta tag: the mobile layout can never apply")
	}
	for _, want := range []string{"width=device-width", "initial-scale=1"} {
		if !strings.Contains(meta[1], want) {
			t.Fatalf("viewport %q is missing %q", meta[1], want)
		}
	}
	// user-scalable=no and a maximum-scale below 2 stop a parent from zooming in on a small label.
	// They are also what an accessibility audit fails on, and neither is needed here.
	if strings.Contains(meta[1], "user-scalable=no") || strings.Contains(meta[1], "maximum-scale") {
		t.Fatalf("viewport %q disables zoom", meta[1])
	}
}
