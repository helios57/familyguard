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
	"strings"
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
	// The same value main.go computes at startup, by the same function, from the same file. Without
	// it every test here would exercise the handler with its correspondence check switched off —
	// which is how a guard ends up defined, unit-tested and never reached.
	if apkPath != "" {
		sum, err := provisioning.ChecksumFile(apkPath)
		if err != nil {
			t.Fatalf("ChecksumFile: %v", err)
		}
		s.packageChecksum = sum
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

// The failure this endpoint exists to make visible. A new APK is installed on the node by copying
// a file; the running process is not told, and it goes on publishing the checksum it computed at
// startup. Every QR issued since then describes bytes that are no longer on disk, and the phone
// finds out mid-provisioning with an error only the system log carries.
//
// The replacement here is deliberately the SAME SIZE as the original, because that is not a
// contrived case: 0.1.0 and 0.1.1 of this DPC are both 13297337 bytes — the version strings are
// the same length — so a check that compared sizes, or trusted an mtime, would have passed the
// one swap this repository has actually performed.
func TestAPKReplacedUnderARunningServerIsRefusedRatherThanServed(t *testing.T) {
	before := []byte("the bytes the startup checksum was computed from")
	after := []byte("a different build, byte for byte the same size!!")
	if len(before) != len(after) {
		t.Fatalf("the fixtures are %d and %d bytes; equal length is the point of this test, and a "+
			"pair that differs in size would pass it for the wrong reason", len(before), len(after))
	}

	path := writeAPK(t, before)
	r := apkRouter(t, path)

	if err := os.WriteFile(path, after, 0o600); err != nil {
		t.Fatalf("replace fixture: %v", err)
	}

	w := get(t, r, APKDownloadPath, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — the server served bytes it does not publish a checksum for", w.Code)
	}
	if !strings.Contains(w.Body.String(), "apk_changed") {
		t.Errorf("body = %q, want the apk_changed code; apk_unavailable would send the operator "+
			"looking for a missing file rather than a missing restart", w.Body.String())
	}
}

// The negative control, and the reason the check hashes rather than stats: reinstalling the same
// build moves the modification time and rewrites the inode without changing what the QR describes.
// A guard that refused on that would take the download offline for an operation that changed
// nothing, and the operator would learn to restart on every deploy to clear a false alarm.
func TestAPKRewrittenWithIdenticalBytesIsStillServed(t *testing.T) {
	body := []byte("the bytes the startup checksum was computed from")
	path := writeAPK(t, body)
	r := apkRouter(t, path)

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove fixture: %v", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("rewrite fixture: %v", err)
	}

	w := get(t, r, APKDownloadPath, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — identical bytes are not a changed APK", w.Code)
	}
	if got := w.Body.String(); got != string(body) {
		t.Errorf("body = %q, want the file's bytes", got)
	}
}
