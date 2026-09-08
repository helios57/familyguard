package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/helios57/familyguard/backend/internal/fgctldist"
)

// fgctlRouter mounts only the two fgctl routes. Neither handler touches the database, which is what
// lets these tests cause the failure paths rather than fake them.
func fgctlRouter(t *testing.T, dir string) *gin.Engine {
	t.Helper()
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if dir != "" {
		cat, err := fgctldist.Scan(dir, "v9.9.9")
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		s.fgctl = cat
	}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(RequestID())
	r.GET(FgctlManifestPath, s.fgctlManifest)
	r.GET(FgctlDownloadPath, s.serveFgctl)
	return r
}

func fgctlDir(t *testing.T) (dir string, body []byte) {
	t.Helper()
	dir = t.TempDir()
	body = []byte("a binary, for the purposes of this test")
	if err := os.WriteFile(filepath.Join(dir, "fgctl-linux-amd64"), body, 0o755); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return dir, body
}

func fgctlGet(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestFgctlManifestPublishesTheChecksumOfTheBytesServed(t *testing.T) {
	dir, body := fgctlDir(t)
	r := fgctlRouter(t, dir)

	w := fgctlGet(t, r, "/fgctl")
	if w.Code != http.StatusOK {
		t.Fatalf("manifest: HTTP %d", w.Code)
	}
	var m struct {
		Hosted    bool   `json:"hosted"`
		Version   string `json:"version"`
		Artifacts []struct {
			Name, OS, Arch, SHA256, URL string
			Size                        int64
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decoding the manifest: %v", err)
	}
	if !m.Hosted || m.Version != "v9.9.9" || len(m.Artifacts) != 1 {
		t.Fatalf("manifest = %+v", m)
	}
	a := m.Artifacts[0]
	sum := sha256.Sum256(body)
	if a.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("the manifest's checksum is not the hash of the file")
	}
	if a.URL != "/fgctl/fgctl-linux-amd64" {
		t.Errorf("URL = %q", a.URL)
	}

	// The download has to be the bytes the manifest just described, or the checksum is a claim
	// about a different file and every client's verification fails for no reason.
	d := fgctlGet(t, r, a.URL)
	if d.Code != http.StatusOK {
		t.Fatalf("download: HTTP %d", d.Code)
	}
	if got := d.Body.Bytes(); string(got) != string(body) {
		t.Errorf("the download is not the file")
	}
	if got := d.Header().Get("X-Fgctl-SHA256"); got != a.SHA256 {
		t.Errorf("X-Fgctl-SHA256 = %q, manifest says %q", got, a.SHA256)
	}
}

// A deployment that ships no CLI is a configuration, not a fault: "hosted": false and 200, so the
// console draws an absence rather than having to tell one error apart from another.
func TestFgctlManifestSaysNotHostedRatherThanFailing(t *testing.T) {
	w := fgctlGet(t, fgctlRouter(t, ""), "/fgctl")
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200", w.Code)
	}
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	if m["hosted"] != false {
		t.Errorf("hosted = %v", m["hosted"])
	}
	// And with no catalog the download route must refuse rather than panic.
	if d := fgctlGet(t, fgctlRouter(t, ""), "/fgctl/fgctl-linux-amd64"); d.Code != http.StatusNotFound {
		t.Errorf("download with no catalog: HTTP %d, want 404", d.Code)
	}
}

func TestFgctlDownloadServesOnlyCataloguedNames(t *testing.T) {
	dir, _ := fgctlDir(t)
	// A file in the same directory that the catalog does not describe. If the handler joined the
	// request onto the directory instead of looking it up, this would be served.
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte("secret-ish"), 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	r := fgctlRouter(t, dir)

	if w := fgctlGet(t, r, "/fgctl/fgctl-linux-amd64"); w.Code != http.StatusOK {
		t.Fatalf("the catalogued name was refused (HTTP %d), so the refusals below prove nothing", w.Code)
	}
	for _, bad := range []string{"/fgctl/SHA256SUMS", "/fgctl/fgctl-linux-arm64", "/fgctl/..%2f..%2fetc%2fpasswd"} {
		if w := fgctlGet(t, r, bad); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = HTTP %d, want 404", bad, w.Code)
		}
	}
}

// The manifest is a promise about specific bytes. If the file changes under a running server, the
// honest answer is to refuse -- serving different bytes under a published checksum turns every
// client's verification into an unexplainable failure.
func TestFgctlDownloadRefusesAFileThatChangedUnderIt(t *testing.T) {
	dir, _ := fgctlDir(t)
	r := fgctlRouter(t, dir)
	if w := fgctlGet(t, r, "/fgctl/fgctl-linux-amd64"); w.Code != http.StatusOK {
		t.Fatalf("HTTP %d before the file was touched", w.Code)
	}
	if err := os.WriteFile(filepath.Join(dir, "fgctl-linux-amd64"), []byte("different bytes now"), 0o755); err != nil {
		t.Fatalf("rewriting: %v", err)
	}
	w := fgctlGet(t, r, "/fgctl/fgctl-linux-amd64")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("HTTP %d after the file changed, want 503", w.Code)
	}
	if body := w.Body.String(); !json.Valid([]byte(body)) {
		t.Errorf("the refusal is not the error envelope: %s", body)
	}
}
