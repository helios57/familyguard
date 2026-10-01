package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// stageFgctl copies the built CLI into a directory shaped like the one the image carries, under the
// name the catalog expects for THIS platform. Using the real binary rather than a fixture is what
// makes the self-update test below able to run what it downloaded.
func stageFgctl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "fgctl-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	src, err := os.ReadFile(fgctlBin)
	if err != nil {
		t.Fatalf("reading the built fgctl (%s): %v", fgctlBin, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), src, 0o755); err != nil {
		t.Fatalf("staging fgctl: %v", err)
	}
	return dir
}

// The download route carries no credential, on purpose: it has to work before a first login and
// after a key is revoked. That is a security-relevant choice, so it is asserted rather than left to
// be true by accident.
func TestFgctlDownloadNeedsNoCredential(t *testing.T) {
	h := newHarness(t, withFgctlDir(stageFgctl(t)))

	res, err := http.Get(h.base + "/fgctl")
	if err != nil {
		t.Fatalf("GET /fgctl: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /fgctl with no Authorization header: HTTP %d", res.StatusCode)
	}
	var m struct {
		Hosted    bool   `json:"hosted"`
		Version   string `json:"version"`
		Artifacts []struct {
			Name, OS, Arch, SHA256, URL string
			Size                        int64
		} `json:"artifacts"`
	}
	if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
		t.Fatalf("decoding the manifest: %v", err)
	}
	if !m.Hosted || len(m.Artifacts) != 1 {
		t.Fatalf("manifest = %+v", m)
	}
	a := m.Artifacts[0]
	if a.OS != runtime.GOOS || a.Arch != runtime.GOARCH {
		t.Fatalf("staged %s/%s but the manifest says %s/%s", runtime.GOOS, runtime.GOARCH, a.OS, a.Arch)
	}

	// And the bytes, also with no credential, and they must hash to what the manifest promised —
	// otherwise the checksum clients are told to verify against is a claim about a different file.
	dl, err := http.Get(h.base + a.URL)
	if err != nil {
		t.Fatalf("GET %s: %v", a.URL, err)
	}
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d", a.URL, dl.StatusCode)
	}
	sum := sha256.New()
	n, err := io.Copy(sum, dl.Body)
	if err != nil {
		t.Fatalf("reading the download: %v", err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != a.SHA256 {
		t.Errorf("the download hashes to %s, the manifest says %s", got, a.SHA256)
	}
	if n != a.Size {
		t.Errorf("the download is %d bytes, the manifest says %d", n, a.Size)
	}
}

// A deployment that ships no CLI must say so plainly, not fail. The negative control for the test
// above: without it, "hosted" being true would prove nothing about the directory being read.
func TestFgctlManifestReportsAnAbsentCatalog(t *testing.T) {
	h := newHarness(t, withFgctlDir(t.TempDir()))
	res, err := http.Get(h.base + "/fgctl")
	if err != nil {
		t.Fatalf("GET /fgctl: %v", err)
	}
	defer res.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if res.StatusCode != http.StatusOK || m["hosted"] != false {
		t.Errorf("empty directory: HTTP %d, hosted=%v; want 200 and false", res.StatusCode, m["hosted"])
	}
}

// The whole loop: a binary asks the server what it hosts, downloads it, checks it, and replaces
// itself with it — then the REPLACED file is executed to prove the swap produced something that
// runs. Checking only the exit status of self-update would pass on a truncated write.
func TestFgctlSelfUpdateReplacesTheRunningBinary(t *testing.T) {
	h := newHarness(t, withFgctlDir(stageFgctl(t)))

	// A private copy, because self-update overwrites the file it is running from and the suite's
	// own fgctl is shared with every other test.
	home := t.TempDir()
	mine := filepath.Join(home, "fgctl")
	if runtime.GOOS == "windows" {
		mine += ".exe"
	}
	src, err := os.ReadFile(fgctlBin)
	if err != nil {
		t.Fatalf("reading fgctl: %v", err)
	}
	if err := os.WriteFile(mine, src, 0o755); err != nil {
		t.Fatalf("copying fgctl: %v", err)
	}

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"AppData=" + filepath.Join(home, "AppData"),
		"FAMILYGUARD_URL=" + h.base,
	}
	env = append(env, coverEnv()...)

	// --check first. It must not modify anything, which is asserted by comparing the file before
	// and after: a --check that quietly updated would otherwise look identical to a correct one.
	before, err := os.ReadFile(mine)
	if err != nil {
		t.Fatalf("reading before: %v", err)
	}
	check := exec.Command(mine, "self-update", "--check", "--json")
	check.Env = env
	out, err := check.CombinedOutput()
	if err != nil {
		t.Fatalf("self-update --check: %v\n%s", err, out)
	}
	var report struct {
		Current   string `json:"current_version"`
		Available string `json:"available_version"`
		UpToDate  bool   `json:"up_to_date"`
		Replaced  string `json:"replaced"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("decoding --check output: %v\n%s", err, out)
	}
	if report.Available == "" {
		t.Errorf("--check did not report the available version: %s", out)
	}
	if report.Replaced != "" {
		t.Errorf("--check reported replacing something: %s", out)
	}
	if after, _ := os.ReadFile(mine); string(after) != string(before) {
		t.Fatal("--check modified the binary")
	}

	// The staged build and this binary come from the same compile, so the server reports the same
	// version and self-update has nothing to do. That is the honest state here, and it is worth
	// asserting rather than engineering around: it proves the comparison is being made at all.
	if !report.UpToDate {
		t.Fatalf("the staged binary is the same build as this one but --check says otherwise: %s", out)
	}

	// Now force the other branch. The server's version comes from the binary it was built with, so
	// the way to make them differ is to make THIS one claim something else — which is exactly the
	// situation self-update exists for.
	stale := filepath.Join(home, "stale-fgctl")
	if runtime.GOOS == "windows" {
		stale += ".exe"
	}
	build := exec.Command("go", "build", "-ldflags", "-X main.version=v0.0.0-stale", "-o", stale, "./cmd/fgctl")
	build.Dir = filepath.Join("..", "..", "backend")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building a stale fgctl: %v\n%s", err, out)
	}

	update := exec.Command(stale, "self-update", "--json")
	update.Env = env
	out, err = update.CombinedOutput()
	if err != nil {
		t.Fatalf("self-update: %v\n%s", err, out)
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("decoding self-update output: %v\n%s", err, out)
	}
	if report.Current != "v0.0.0-stale" || report.UpToDate {
		t.Errorf("self-update misread its own state: %s", out)
	}

	// The point of the whole test: the file on disk is now a DIFFERENT, WORKING binary. Running it
	// is the only check that covers a truncated or corrupted write, which every status code above
	// would happily report as success.
	ran := exec.Command(stale, "version")
	ran.Env = env
	versionOut, err := ran.CombinedOutput()
	if err != nil {
		t.Fatalf("the replaced binary does not run: %v\n%s", err, versionOut)
	}
	if strings.Contains(string(versionOut), "v0.0.0-stale") {
		t.Errorf("the binary still reports the old version, so nothing was replaced: %s", versionOut)
	}
	if !strings.Contains(string(versionOut), report.Available) {
		t.Errorf("the replaced binary reports %q, want the server's %s", versionOut, report.Available)
	}
}

// TestFgctlSelfUpdateRefusesWhatItCannotTrust is the known-bad half of the test above, which only
// ever saw a good download: the three checks that are the point of self-update — the published
// checksum, the published size, and running the staged binary before swapping it in — had never
// been shown to refuse anything. Each case must leave the running binary byte-for-byte as it was,
// and leave no staged file behind beside it.
func TestFgctlSelfUpdateRefusesWhatItCannotTrust(t *testing.T) {
	home := t.TempDir()
	stale := filepath.Join(home, "stale-fgctl")
	if runtime.GOOS == "windows" {
		stale += ".exe"
	}
	build := exec.Command("go", "build", "-ldflags", "-X main.version=v0.0.0-stale", "-o", stale, "./cmd/fgctl")
	build.Dir = filepath.Join("..", "..", "backend")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building a stale fgctl: %v\n%s", err, out)
	}
	original, err := os.ReadFile(stale)
	if err != nil {
		t.Fatal(err)
	}
	artifact := "fgctl-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		artifact += ".exe"
	}

	for _, tc := range []struct {
		name string
		// before runs on the staged directory before the server starts; after, once it has
		// published its manifest.
		before, after func(t *testing.T, dir string)
		// proxy, when set, sits between fgctl and the server and may rewrite any answer.
		proxy func(path string, body []byte) []byte
		want  string
	}{
		{
			name: "the file changed after the server published its checksum",
			after: func(t *testing.T, dir string) {
				p := filepath.Join(dir, artifact)
				b, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				b[len(b)/2] ^= 0xff
				if err := os.WriteFile(p, b, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			// The server re-checks the file it serves and refuses rather than hand out bytes its own
			// manifest does not describe.
			want: "HTTP 503",
		},
		{
			// Bytes altered on the way, which the server cannot see: only the client's checksum
			// stands between them and the swap.
			name: "the download was altered in transit",
			proxy: func(path string, body []byte) []byte {
				if strings.HasSuffix(path, "/"+artifact) {
					body[len(body)/2] ^= 0xff
				}
				return body
			},
			want: "does not match the checksum",
		},
		{
			// A manifest whose size disagrees with the bytes it hashes.
			name: "the manifest's size disagrees with the download",
			proxy: func(path string, body []byte) []byte {
				if path == "/fgctl" {
					return regexp.MustCompile(`"size":\s*\d+`).ReplaceAll(body, []byte(`"size": 12345`))
				}
				return body
			},
			want: "but the manifest says 12345",
		},
		{
			name: "the published binary does not run",
			before: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, artifact), []byte("#!/nonexistent/interpreter\nnot a program\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "was NOT installed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each case its own copy: a case whose check failed open replaces the binary, and the
			// next case must not inherit that and fail for a reason of its own.
			home := t.TempDir()
			stale := filepath.Join(home, filepath.Base(stale))
			if err := os.WriteFile(stale, original, 0o755); err != nil {
				t.Fatal(err)
			}
			dir := stageFgctl(t)
			if tc.before != nil {
				tc.before(t, dir)
			}
			h := newHarness(t, withFgctlDir(dir))
			if tc.after != nil {
				tc.after(t, dir)
			}
			base := h.base
			if tc.proxy != nil {
				target, _ := url.Parse(h.base)
				rp := httputil.NewSingleHostReverseProxy(target)
				rp.ModifyResponse = func(r *http.Response) error {
					body, err := io.ReadAll(r.Body)
					r.Body.Close()
					if err != nil {
						return err
					}
					body = tc.proxy(r.Request.URL.Path, body)
					r.Body = io.NopCloser(bytes.NewReader(body))
					r.ContentLength = int64(len(body))
					r.Header.Set("Content-Length", strconv.Itoa(len(body)))
					return nil
				}
				srv := httptest.NewServer(rp)
				t.Cleanup(srv.Close)
				base = srv.URL
			}
			update := exec.Command(stale, "self-update", "--json")
			update.Env = append([]string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + home,
				"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
				"FAMILYGUARD_URL=" + base,
			}, coverEnv()...)
			out, err := update.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("self-update exited %v and said %q; want a refusal saying %q", err, out, tc.want)
			}
			if now, _ := os.ReadFile(stale); !bytes.Equal(now, original) {
				t.Fatal("a refused self-update still changed the running binary")
			}
			left, _ := filepath.Glob(filepath.Join(home, ".fgctl-update-*"))
			if len(left) != 0 {
				t.Errorf("a refused self-update left its staged download behind: %v", left)
			}
		})
	}
}
