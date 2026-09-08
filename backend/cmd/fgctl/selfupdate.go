package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/helios57/familyguard/backend/internal/fgclient"
)

// manifest is what GET /fgctl returns.
type manifest struct {
	Hosted    bool   `json:"hosted"`
	Version   string `json:"version"`
	Artifacts []struct {
		Name   string `json:"name"`
		OS     string `json:"os"`
		Arch   string `json:"arch"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
		URL    string `json:"url"`
	} `json:"artifacts"`
}

// cmdSelfUpdate replaces this binary with the one the configured server hosts.
//
// It is `self-update` rather than `update` because `update` already queues an UPDATE_APP command to
// a phone. Two things called update, one acting on the operator's machine and one on a child's, is
// the kind of collision that gets discovered by someone who meant the other one.
func cmdSelfUpdate(ctx context.Context, env *environment, args []string) error {
	checkOnly := false
	for _, a := range args {
		switch a {
		case "--check":
			checkOnly = true
		default:
			return fmt.Errorf("unexpected argument %q", a)
		}
	}

	var m manifest
	if err := env.client.GetAnonymous(ctx, "/fgctl", &m); err != nil {
		return fmt.Errorf("asking %s what it hosts: %w", env.cfg.BaseURL, err)
	}
	if !m.Hosted {
		return fmt.Errorf("%s does not host fgctl binaries, so there is nothing to update from",
			env.cfg.BaseURL)
	}

	current := version
	type report struct {
		Current   string `json:"current_version"`
		Available string `json:"available_version"`
		UpToDate  bool   `json:"up_to_date"`
		Platform  string `json:"platform"`
		Artifact  string `json:"artifact,omitempty"`
		Replaced  string `json:"replaced,omitempty"`
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH

	var wanted struct {
		Name, SHA256, URL string
		Size              int64
	}
	for _, a := range m.Artifacts {
		if a.OS == runtime.GOOS && a.Arch == runtime.GOARCH {
			wanted.Name, wanted.SHA256, wanted.URL, wanted.Size = a.Name, a.SHA256, a.URL, a.Size
		}
	}
	if wanted.Name == "" {
		return fmt.Errorf("%s hosts fgctl %s but not for %s; available: %s",
			env.cfg.BaseURL, m.Version, platform, platformList(m))
	}

	// Compared as strings, not parsed as semver. These two numbers come from the same build of the
	// same source -- the server stamps itself and the CLI with one value -- so "different" is the
	// whole question and an ordering would add a way to be wrong. It also means a downgrade works,
	// which is what you want when a release is being rolled back.
	upToDate := current == m.Version
	rep := report{Current: current, Available: m.Version, UpToDate: upToDate, Platform: platform}
	if upToDate || checkOnly {
		if !upToDate {
			rep.Artifact = wanted.Name
		}
		return env.emit(rep, func(w *tabwriter.Writer) {
			fmt.Fprintf(w, "installed\t%s\n", current)
			fmt.Fprintf(w, "available\t%s\n", m.Version)
			fmt.Fprintf(w, "platform\t%s\n", platform)
			if upToDate {
				fmt.Fprintf(w, "\t\nAlready current.\n")
			} else {
				fmt.Fprintf(w, "\t\nRun `fgctl self-update` to replace this binary with %s.\n", wanted.Name)
			}
		})
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating this binary: %w", err)
	}
	// Resolved so that updating through a symlink replaces the real file rather than the link.
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}

	// Downloaded into the SAME directory as the binary being replaced, because the swap below is a
	// rename and a rename cannot cross filesystems. A temp dir would work on a developer machine
	// and fail wherever /tmp is its own mount.
	dir := filepath.Dir(self)
	// The staged file is EXECUTED below, before it is installed. On Windows it is named .exe by
	// CONVENTION, not necessity: measured on a Windows 11 guest, Go runs the same PE from this
	// directory under `.exe`, under `.new`, and under a leading-dot extensionless name, 8/8 each.
	// An earlier version of this comment claimed the suffix was required and cited an A/B; that
	// A/B was confounded and its conclusion was wrong. See IMPLEMENTATION_PLAN.md 20.5.
	pattern := ".fgctl-update-*"
	if runtime.GOOS == "windows" {
		pattern = ".fgctl-update-*.exe"
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("this binary's directory (%s) is not writable, so it cannot replace "+
			"itself: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Best-effort on every path out: a failed update must not leave a partial binary next to a
	// working one.
	defer os.Remove(tmpName)

	sum, written, err := download(ctx, env.client, wanted.URL, tmp)
	tmp.Close()
	if err != nil {
		return err
	}
	if sum != wanted.SHA256 {
		return fmt.Errorf("the download does not match the checksum %s publishes "+
			"(got %s, expected %s) — nothing was replaced", env.cfg.BaseURL, sum, wanted.SHA256)
	}
	if wanted.Size != 0 && written != wanted.Size {
		return fmt.Errorf("the download is %d bytes but the manifest says %d — nothing was replaced",
			written, wanted.Size)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("making the downloaded binary executable: %w", err)
	}

	// The checksum proves the transfer was intact; it does not prove the file runs. Both have
	// failed here before in other projects for different reasons -- a truncated write passes no
	// check at all, but a correct download of a binary for the wrong libc passes the checksum and
	// then cannot start. Running it is the only check that covers that, and it is one exec.
	if err := verifyRuns(ctx, tmpName, m.Version); err != nil {
		return fmt.Errorf("the downloaded binary did not run, so it was NOT installed: %w", err)
	}

	replaced, err := swap(self, tmpName)
	if err != nil {
		return err
	}
	rep.Artifact = wanted.Name
	rep.Replaced = self
	return env.emit(rep, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "Updated %s\n  %s → %s\n", self, current, m.Version)
		if replaced != "" {
			fmt.Fprintf(w, "The previous binary is at %s; Windows cannot delete a running\n"+
				"executable, so remove it when convenient.\n", replaced)
		}
	})
}

// download copies the artefact into dst and returns its hex SHA-256 and length.
func download(ctx context.Context, client *fgclient.Client, path string, dst io.Writer) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.BaseURL+path, nil)
	if err != nil {
		return "", 0, fmt.Errorf("building the download request: %w", err)
	}
	// No Authorization header: this route has none, for the reasons on fgclient.DoAnonymous.
	resp, err := client.HTTP.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("downloading %s: %w", client.BaseURL+path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("downloading %s: HTTP %d", client.BaseURL+path, resp.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("reading the download: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// verifyRuns executes the downloaded binary and checks it reports the version it should.
func verifyRuns(ctx context.Context, path, want string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		// Deliberately verbose. This check has failed exactly once, on Windows, and could not be
		// reproduced afterwards under any condition tried -- so the next occurrence has to arrive
		// carrying its own evidence instead of sending someone back to a VM. The error text already
		// separates a process that ran and exited from one that never started; what was missing the
		// first time was whether the staged file was even there and whole.
		return fmt.Errorf("%w%s (it printed: %s)", err, stagedState(path), summarise(out))
	}
	// Contains, not equals: the line is "fgctl <version>".
	if !strings.Contains(string(out), want) {
		return fmt.Errorf("it reports %q, but the manifest promised %s", summarise(out), want)
	}
	return nil
}

// stagedState describes the downloaded file at the moment the exec failed, so a truncated download
// is distinguishable from a complete one that will not run.
func stagedState(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Sprintf(" [the staged file could not be stat'd: %v]", err)
	}
	return fmt.Sprintf(" [staged %s: %d bytes, mode %v]", filepath.Base(path), fi.Size(), fi.Mode())
}

func summarise(out []byte) string {
	text := strings.Join(strings.Fields(string(out)), " ")
	if len(text) > 200 {
		return text[:200] + "…"
	}
	if text == "" {
		return "nothing"
	}
	return text
}

func platformList(m manifest) string {
	seen := make([]string, 0, len(m.Artifacts))
	for _, a := range m.Artifacts {
		seen = append(seen, a.OS+"/"+a.Arch)
	}
	return strings.Join(seen, ", ")
}
