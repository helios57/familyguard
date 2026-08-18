package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/helios57/familyguard/backend/internal/config"
	"github.com/helios57/familyguard/backend/internal/provisioning"
)

// apkRouter mounts only the download route. serveAPK reads the config and the logger and nothing
// else, so this needs no database — which is what keeps it able to assert the failure paths, where
// a test that needed a live server would have to fake the failure instead of causing it.
func apkRouter(t *testing.T, apkPath string) *gin.Engine {
	t.Helper()
	s := &Server{
		cfg: &config.Config{APKPath: apkPath},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(RequestID())
	r.GET(APKDownloadPath, s.serveAPK)
	return r
}

// A file that is not an APK. Nothing here parses one, and using real bytes would mean either a
// 15 MB fixture in the repo or a test that only runs where a build output happens to be.
//
// The extension is deliberately not `.apk`. http.ServeFile types a response from the file
// extension first, and Go's mime package reads /etc/mime.types — which on this host does map
// `.apk` to the Android type. The container the server actually runs in is distroless: no
// /etc/mime.types, nothing for `.apk` in Go's builtin table, so ServeFile falls back to sniffing
// and reports a real APK as a zip. A fixture named `.apk` therefore measures this host's mime
// database instead of the handler — calibrated, deleting the explicit Content-Type header left the
// suite green. The t.Fatalf below keeps it that way: if some future host maps this extension, the
// test says so rather than quietly going back to measuring nothing.
func writeAPK(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "familyguard.dpcfixture")
	if got := mime.TypeByExtension(filepath.Ext(path)); got != "" {
		t.Fatalf("this host maps %s to %q, so the Content-Type assertion would measure the mime table, not the server",
			filepath.Ext(path), got)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestAPKIsServedWithTheChecksumTheQRWouldCarry(t *testing.T) {
	body := []byte("not really an apk, but a stable sequence of bytes")
	path := writeAPK(t, body)

	w := get(t, apkRouter(t, path), APKDownloadPath, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/vnd.android.package-archive" {
		t.Errorf("Content-Type = %q; a sniffed APK is reported as a zip", got)
	}
	if got := w.Body.String(); got != string(body) {
		t.Errorf("body = %q, want the file's bytes", got)
	}

	// The assertion that earns this handler its existence: what the device downloads hashes to the
	// value the QR payload publishes. The right side is the function the QR builder itself calls,
	// against the file on disk; the left is the same hash, in the same encoding, over the bytes that
	// actually came back. A change to either side that broke the correspondence would otherwise show
	// up as a refused download on a wiped phone, mid-provisioning.
	fromFile, err := provisioning.ChecksumFile(path)
	if err != nil {
		t.Fatalf("ChecksumFile: %v", err)
	}
	sum := sha256.Sum256(w.Body.Bytes())
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != fromFile {
		t.Errorf("the served bytes hash to %s, the QR would publish %s", got, fromFile)
	}
}

// A partial download resumes rather than restarting — the phone that walked out of Wi-Fi halfway
// through a 15 MB APK is the ordinary case, not the exotic one.
func TestAPKAnswersARangeRequest(t *testing.T) {
	body := []byte("0123456789abcdef")
	r := apkRouter(t, writeAPK(t, body))

	req := httptest.NewRequest(http.MethodGet, APKDownloadPath, nil)
	req.Header.Set("Range", "bytes=4-7")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 — the downloader would restart from zero", w.Code)
	}
	if got := w.Body.String(); got != "4567" {
		t.Errorf("body = %q, want %q", got, "4567")
	}
}

func TestAPKIsNotFoundWhenThisServerDoesNotHostIt(t *testing.T) {
	w := get(t, apkRouter(t, ""), APKDownloadPath, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// The file was there at startup — config.Load stats it — so a file missing now was removed or
// replaced under a running server, which also invalidates the checksum in every QR already issued.
// 503, not 404: the difference between "this deployment has no DPC" and "this deployment's DPC has
// gone missing" is the difference between a configuration and an incident.
func TestAPKThatVanishedIsReportedAsUnavailableRatherThanAbsent(t *testing.T) {
	path := writeAPK(t, []byte("bytes"))
	r := apkRouter(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove fixture: %v", err)
	}

	w := get(t, r, APKDownloadPath, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}
