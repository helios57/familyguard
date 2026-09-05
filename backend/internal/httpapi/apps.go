package httpapi

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/helios57/familyguard/backend/internal/apk"
	"github.com/helios57/familyguard/backend/internal/catalog"
	"github.com/helios57/familyguard/backend/internal/store"
)

// uploadAppRoute is the one route allowed a larger request body. Named as a constant because the
// exemption in Router() and the route registration must be the same string — a mismatch would leave
// uploads silently capped at the JSON limit, which surfaces as "your APK is corrupt".
const uploadAppRoute = "/api/v1/apps"

// listApps returns the catalog.
//
// `configured` is reported rather than left to be inferred from an empty list. A deployment with no
// APK_DIR and a deployment whose directory is empty look identical in `apps`, and the two need
// different actions from a parent: one is "ask the operator to configure it", the other is "upload
// something".
func (s *Server) listApps(c *gin.Context) {
	apps, err := s.store.ListApps(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"apps": apps, "configured": s.catalog.Configured()})
}

// uploadApp registers an APK sent in the request body (FR-16.2).
//
// Two encodings are accepted, and both exist for a caller that actually has to use them. A browser
// sends multipart/form-data because that is what <input type="file"> produces. Everything else —
// curl, a deployment script, an MCP server — sends the file as the raw body, because building a
// multipart envelope by hand to upload one file is a needless obstacle:
//
//	curl -H "Authorization: Bearer fgk_…" --data-binary @muplay.apk \
//	     "https://…/api/v1/apps?label=MuPlay"
//
// What is NOT accepted is a filename or a package name from the caller. Everything stored is read
// out of the file — see internal/apk — because a package name supplied alongside an upload is a
// package name that will eventually not match the bytes, and the mismatch would only be discovered
// by a phone installing the wrong thing.
func (s *Server) uploadApp(c *gin.Context) {
	if !s.catalog.Configured() {
		failWith(c, http.StatusNotFound, "not_configured", catalog.ErrNotConfigured.Error())
		return
	}

	body, label, ok := s.uploadBody(c)
	if !ok {
		return
	}
	defer body.Close()

	app, err := s.catalog.Register(c.Request.Context(), body, s.cfg.MaxUploadBytes, store.AppSourceUpload, label)
	if err != nil {
		s.failCatalog(c, err)
		return
	}
	s.auditParent(c, "APP_REGISTERED", "app", app.ID.String(), map[string]any{
		"package_name": app.PackageName,
		"version_code": app.VersionCode,
		"sha256":       app.SHA256,
		// The signer is recorded on the row that created the pin, so that "who is this package
		// trusted to be signed by, and who decided that" is answerable from the audit log alone.
		"signer_sha256": app.SignerSHA256,
	})
	c.JSON(http.StatusCreated, app)
}

// uploadBody picks the APK bytes out of whichever encoding was used.
func (s *Server) uploadBody(c *gin.Context) (io.ReadCloser, string, bool) {
	contentType := c.GetHeader("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil && contentType != "" {
		failWith(c, http.StatusBadRequest, "invalid_body", "Content-Type is not a media type: "+err.Error())
		return nil, "", false
	}
	if mediaType != "multipart/form-data" {
		// The raw-body form. The label rides in the query string, which is the only place it can go
		// when the body is the file.
		return c.Request.Body, c.Query("label"), true
	}

	// MaxBodyBytes here bounds only what is buffered in MEMORY while parsing the envelope; the file
	// part itself spools to disk beyond that and is then read through the catalog's own limit. It
	// is not a second cap on the upload.
	form, err := c.MultipartForm()
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			failWith(c, http.StatusRequestEntityTooLarge, "body_too_large", "that file is larger than this server accepts")
			return nil, "", false
		}
		failWith(c, http.StatusBadRequest, "invalid_body", "could not read the upload: "+err.Error())
		return nil, "", false
	}
	files := form.File["apk"]
	if len(files) != 1 {
		failWith(c, http.StatusBadRequest, "invalid_body",
			"send exactly one file in a part named \"apk\", or the APK as the raw request body")
		return nil, "", false
	}
	f, err := files[0].Open()
	if err != nil {
		s.fail(c, err)
		return nil, "", false
	}
	label := ""
	if v := form.Value["label"]; len(v) > 0 {
		label = v[0]
	}
	return f, label, true
}

// scanApps registers whatever is in the node's APK directory (FR-16.1).
//
// Also run once at startup. Exposed as an endpoint because the other way an APK gets onto the node
// is an operator copying it there over ssh, and requiring a restart of the control plane to notice
// would make that route the awkward one — restarting is a bigger action than the one it serves.
//
// The response names what failed, per file. A scan that reported only a count would make "9 of 10"
// and "10 of 10" the same sentence to anyone not counting the directory themselves.
func (s *Server) scanApps(c *gin.Context) {
	if !s.catalog.Configured() {
		failWith(c, http.StatusNotFound, "not_configured", catalog.ErrNotConfigured.Error())
		return
	}
	result, err := s.catalog.Scan(c.Request.Context())
	if err != nil {
		s.failCatalog(c, err)
		return
	}
	s.auditParent(c, "APP_DIRECTORY_SCANNED", "app", "", map[string]any{
		"registered": len(result.Registered), "failed": len(result.Failed),
	})
	c.JSON(http.StatusOK, gin.H{"registered": result.Registered, "failed": result.Failed})
}

// deleteApp removes one version from the catalog and its file from disk.
//
// The row goes first. If the file removal then fails the catalog is consistent — nothing points at
// the file — and the leftover is a stale file rather than a row promising bytes that are not there;
// the scan will re-register it and say so, which is a visible state. The other order can leave a
// row whose file is gone, which has no symptom until a phone downloads.
//
// A declaration naming this package is deliberately NOT touched. Deleting one version of an app a
// child is declared to have should fall back to another version, not silently unassign the app —
// and if it was the only version, the declaration standing unfulfilled is the honest state, which
// the child's device view reports.
func (s *Server) deleteApp(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	app, err := s.store.DeleteApp(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	if err := s.catalog.Remove(app); err != nil {
		s.log.Error("an app row was deleted and its file could not be removed",
			"package", app.PackageName, "file", app.FileName, "error", err, "request_id", RequestIDOf(c))
	}
	s.auditParent(c, "APP_DELETED", "app", app.ID.String(), map[string]any{
		"package_name": app.PackageName, "version_code": app.VersionCode,
	})
	c.Status(http.StatusNoContent)
}

// ---- what a child is declared to have ----------------------------------------------------------

// managedAppView is one entry of a child's declared set, joined to what it currently resolves to.
//
// Available is false when nothing in the catalog carries that package any more. That is the state
// that must be visible: the phone will do nothing about a declaration it cannot fulfil, and without
// this the console would show the app as assigned and the parent would wait for an install that has
// no bytes behind it.
type managedAppView struct {
	PackageName string     `json:"package_name"`
	Available   bool       `json:"available"`
	App         *store.App `json:"app,omitempty"`
}

func (s *Server) managedAppViews(c *gin.Context, packages []string) ([]managedAppView, bool) {
	out := make([]managedAppView, 0, len(packages))
	for _, pkg := range packages {
		view := managedAppView{PackageName: pkg}
		app, err := s.store.LatestApp(c.Request.Context(), pkg)
		switch {
		case err == nil:
			view.Available = true
			view.App = app
		case errors.Is(err, store.ErrNotFound):
		default:
			s.fail(c, err)
			return nil, false
		}
		out = append(out, view)
	}
	return out, true
}

func (s *Server) listManagedApps(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	packages, err := s.store.ManagedPackages(c.Request.Context(), childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	views, ok := s.managedAppViews(c, packages)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"managed_apps": views})
}

// declareManagedApp adds a package to a child's set (FR-16.3).
//
// It refuses a package the catalog does not carry. The alternative — accepting any string — would
// let a typo sit in a child's set forever looking like an app that is about to install, and the
// only place the mistake would surface is the phone doing nothing.
func (s *Server) declareManagedApp(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	pkg, ok := packageParam(c)
	if !ok {
		return
	}
	if _, err := s.store.LatestApp(c.Request.Context(), pkg); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			failWith(c, http.StatusBadRequest, "unknown_package",
				"no version of "+pkg+" is registered; upload it first")
			return
		}
		s.fail(c, err)
		return
	}
	if err := s.store.DeclareManagedApp(c.Request.Context(), childID, pkg); err != nil {
		s.fail(c, err)
		return
	}
	s.bumpAndNotify(c, childID, "APP_DECLARED", map[string]any{"package_name": pkg})
	c.Status(http.StatusNoContent)
}

// withdrawManagedApp removes a package from a child's set, which is what makes the phone uninstall
// it (FR-16.5).
func (s *Server) withdrawManagedApp(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	pkg, ok := packageParam(c)
	if !ok {
		return
	}
	removed, err := s.store.WithdrawManagedApp(c.Request.Context(), childID, pkg)
	if err != nil {
		s.fail(c, err)
		return
	}
	if !removed {
		// 404 rather than 204. Withdrawing causes an UNINSTALL on a child's phone, and a parent
		// watching for an app to disappear must not be told "done" by a request that matched
		// nothing — most likely because they are looking at the wrong child.
		failWith(c, http.StatusNotFound, "not_found", pkg+" is not in this child's set")
		return
	}
	s.bumpAndNotify(c, childID, "APP_WITHDRAWN", map[string]any{"package_name": pkg})
	c.Status(http.StatusNoContent)
}

// packageParam reads and validates a package name from the path.
func packageParam(c *gin.Context) (string, bool) {
	// gin does not decode path parameters, and a package name is dotted rather than escaped, so
	// this is normally a no-op — it is here for the caller who escaped it anyway rather than to
	// make anything possible.
	raw, err := url.PathUnescape(c.Param("package"))
	if err != nil {
		failWith(c, http.StatusBadRequest, "invalid_input", "that is not a package name")
		return "", false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 255 || strings.ContainsAny(raw, "/\\ \t\n") {
		failWith(c, http.StatusBadRequest, "invalid_input", "that is not a package name")
		return "", false
	}
	return raw, true
}

// ---- the device side ---------------------------------------------------------------------------

// deviceDownloadApp serves one managed application's bytes to the phone that is converging on it.
//
// Addressed by package AND exact version, because that is what the policy named. A "latest" URL
// would mean the phone verifies a checksum it was given at sync time against bytes chosen at
// download time, and the two would differ every time a new build was registered between the two
// requests — a failure that looks exactly like a corrupted download.
//
// Device authenticated, unlike /dpc.apk. The DPC's download has to be reachable by a factory-reset
// phone with no credential; a phone fetching a managed app enrolled weeks ago and has one.
//
// It does NOT check that this device's child is declared to have the package. That check would be
// worth something only if the file were a secret, and it is not — it is an application, and the
// device already had to be enrolled to get here. What it would cost is a phone that cannot finish
// an install because the declaration was withdrawn between the sync and the download, leaving a
// half-converged device and a 403 in a log nobody reads.
func (s *Server) deviceDownloadApp(c *gin.Context) {
	dev := deviceOf(c)
	if dev == nil {
		abortWith(c, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	pkg, ok := packageParam(c)
	if !ok {
		return
	}
	version, err := strconv.ParseInt(strings.TrimSuffix(c.Param("versionCode"), ".apk"), 10, 64)
	if err != nil {
		failWith(c, http.StatusBadRequest, "invalid_input", "that is not a version code")
		return
	}
	app, err := s.store.AppVersion(c.Request.Context(), pkg, version)
	if err != nil {
		s.fail(c, err)
		return
	}
	f, err := s.catalog.Open(app)
	if err != nil {
		// The row and the directory disagree. Logged at error with both sides, because this is the
		// one failure here that is the server's and the device cannot report it usefully.
		s.log.Error("a managed app could not be served",
			"package", app.PackageName, "version_code", app.VersionCode, "file", app.FileName,
			"device", dev.ID, "error", err, "request_id", RequestIDOf(c))
		failWith(c, http.StatusServiceUnavailable, "apk_unavailable", "that application is temporarily unavailable")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.fail(c, err)
		return
	}

	c.Header("Content-Type", "application/vnd.android.package-archive")
	// ServeContent, not io.Copy: it answers Range requests, so a phone that walked out of Wi-Fi
	// resumes rather than starting a 30 MB download again.
	http.ServeContent(c.Writer, c.Request, app.FileName, info.ModTime(), f)
}

// failCatalog maps the catalog's own errors, which are neither store errors nor validation errors.
func (s *Server) failCatalog(c *gin.Context, err error) {
	switch {
	case errors.Is(err, catalog.ErrNotConfigured):
		failWith(c, http.StatusNotFound, "not_configured", err.Error())
	case errors.Is(err, store.ErrSignerChanged):
		// 409, and the message says what to do. This is the refusal most likely to be met by
		// somebody who has done nothing wrong — rebuilding an app with a new key is a normal thing
		// to do — and "conflict" alone would send them looking for a duplicate.
		failWith(c, http.StatusConflict, "signer_changed",
			"this package was first registered with a different signing key. Android will refuse to "+
				"install it over the one on the phone, so it is refused here too. Remove every "+
				"version of the package first if the key really did change.")
	case errors.Is(err, store.ErrConflict):
		failWith(c, http.StatusConflict, "version_exists",
			"a different file is already registered under that package and version code. Bump the "+
				"version code, or delete the existing entry first.")
	case errors.Is(err, apk.ErrTooLarge):
		failWith(c, http.StatusRequestEntityTooLarge, "body_too_large", err.Error())
	case errors.Is(err, catalog.ErrReservedPackage):
		failWith(c, http.StatusConflict, "reserved_package", err.Error())
	case errors.Is(err, apk.ErrNotAnAPK):
		// One case for the whole parser. The three named errors it wraps are the interesting
		// failures; the ordinary ones — an empty body, a file that is not a zip, a download that
		// was cut short — are the ones a per-error list kept missing, and each miss was a 500
		// telling an operator with the wrong file that the server had a problem.
		failWith(c, http.StatusBadRequest, "not_an_apk", err.Error())
	case errors.Is(err, os.ErrNotExist):
		failWith(c, http.StatusNotFound, "not_found", err.Error())
	default:
		s.fail(c, err)
	}
}
