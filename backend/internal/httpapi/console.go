package httpapi

// The console is served from memory, from files compiled into the binary.
//
// It is mounted file by file rather than with a catch-all static handler. A catch-all is one line
// shorter and it is how a directory-traversal bug gets in; more practically here, it would swallow
// every unknown path and answer HTML, so a typo in an API route would return the console with 200
// instead of the JSON 404 that tells a caller what actually happened.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/helios57/familyguard/backend/internal/console"
)

// consoleFile is one asset, read once at startup.
type consoleFile struct {
	body        []byte
	contentType string
	etag        string
}

// consoleRoutes maps URL path to the embedded file behind it. index.html is served at "/" as well,
// which is the only place a single file answers two paths.
var consoleRoutes = map[string]string{
	"/":                     "index.html",
	"/index.html":           "index.html",
	"/app.css":              "app.css",
	"/app.js":               "app.js",
	"/manifest.webmanifest": "manifest.webmanifest",
	"/icon.svg":             "icon.svg",
}

// contentTypes is explicit rather than derived from mime.TypeByExtension, which consults
// /etc/mime.types and therefore answers differently on a developer's laptop and in a scratch
// container. A .webmanifest served as text/plain makes the install prompt silently never appear,
// and nothing anywhere reports it.
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".svg":         "image/svg+xml",
	".webmanifest": "application/manifest+json",
}

// mountConsole registers the console's routes on the engine.
//
// It fails rather than starting without a console: a server that answers 404 at "/" looks like a
// broken deployment from the outside and like a working one from the logs.
func (s *Server) mountConsole(r *gin.Engine) error {
	assets, err := console.FS()
	if err != nil {
		return fmt.Errorf("console assets: %w", err)
	}

	// Keyed by file name, so index.html is read and hashed once even though two routes serve it.
	loaded := make(map[string]consoleFile, len(consoleRoutes))
	for _, name := range consoleRoutes {
		if _, done := loaded[name]; done {
			continue
		}
		body, err := fs.ReadFile(assets, name)
		if err != nil {
			return fmt.Errorf("console asset %q: %w", name, err)
		}
		ct, ok := contentTypes[path.Ext(name)]
		if !ok {
			return fmt.Errorf("console asset %q: no content type registered for its extension", name)
		}
		sum := sha256.Sum256(body)
		loaded[name] = consoleFile{
			body:        body,
			contentType: ct,
			// A content hash, so a byte that changed always changes the ETag and a byte that did not
			// never does. A build timestamp would invalidate every asset on every deploy, and a
			// version string would keep serving stale JavaScript after a hotfix that forgot to bump it.
			etag: `"` + hex.EncodeToString(sum[:16]) + `"`,
		}
	}

	for route, name := range consoleRoutes {
		f := loaded[name]
		r.GET(route, func(c *gin.Context) { serveConsoleFile(c, f) })
		// HEAD costs nothing here and is what an uptime probe reaches for; without it the probe
		// records a 404 for a page that serves fine.
		r.HEAD(route, func(c *gin.Context) { serveConsoleFile(c, f) })
	}
	return nil
}

func serveConsoleFile(c *gin.Context, f consoleFile) {
	h := c.Writer.Header()
	h.Set("ETag", f.etag)
	// no-cache means "revalidate every time", not "do not store": the browser keeps the bytes and
	// spends one conditional request per load. Immutable caching would need hashed filenames, and
	// there is no build step here to generate them — while a console running against a newer API is
	// exactly the failure this project is written against.
	h.Set("Cache-Control", "no-cache")

	if match := c.GetHeader("If-None-Match"); match != "" {
		for _, tag := range strings.Split(match, ",") {
			if strings.TrimSpace(tag) == f.etag {
				c.Status(http.StatusNotModified)
				return
			}
		}
	}
	c.Data(http.StatusOK, f.contentType, f.body)
}
