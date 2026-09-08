// Package fgctldist describes the fgctl binaries a deployment hosts, so the CLI can be downloaded
// from the same server it talks to and can later notice that it is out of date.
//
// The catalog is built once at startup from a directory of files, and the checksums in it are
// computed from those bytes -- never configured, never taken from a build manifest that could
// describe a different artefact than the one on disk.
package fgctldist

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Artifact is one built binary.
type Artifact struct {
	// Name is the file name, which is also the last path segment of its download URL.
	Name string `json:"name"`
	OS   string `json:"os"`
	Arch string `json:"arch"`
	Size int64  `json:"size"`
	// SHA256 is hex-encoded, of the bytes on disk at startup.
	//
	// It is worth being precise about what this does and does not establish. It is transfer
	// integrity: it lets a download be checked against what the server holds. It is NOT
	// authenticity -- the manifest and the bytes come from the same server, so anyone able to
	// substitute one can substitute the other. Authenticity here rests on TLS to a host the parent
	// already trusts with the family's data.
	SHA256 string `json:"sha256"`
}

// Catalog is every artefact a deployment hosts, plus the version they were all built at.
type Catalog struct {
	// Version is the build these binaries came from, which is the SERVER's version: they are
	// compiled in the same image build from the same source. That identity is what makes
	// "is my fgctl current?" answerable at all -- the alternative would be a version string
	// maintained by hand in a second place, which drifts.
	Version   string     `json:"version"`
	Artifacts []Artifact `json:"artifacts"`

	dir string
}

// Scan reads dir and returns the catalog it describes.
//
// A file that does not parse as fgctl-<os>-<arch>[.exe] is ignored rather than rejected: the
// directory is inside a container image and a stray SHA256SUMS or README alongside the binaries is
// not a reason to refuse to serve any of them.
func Scan(dir, version string) (*Catalog, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	cat := &Catalog{Version: version, dir: dir}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		goos, goarch, ok := parseName(e.Name())
		if !ok {
			continue
		}
		full := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", full, err)
		}
		sum, err := sha256File(full)
		if err != nil {
			return nil, err
		}
		cat.Artifacts = append(cat.Artifacts, Artifact{
			Name: e.Name(), OS: goos, Arch: goarch, Size: info.Size(), SHA256: sum,
		})
	}
	// Sorted so the manifest, the console list and the download page are all stable across
	// restarts. An unstable order makes a diff of two manifests unreadable.
	sort.Slice(cat.Artifacts, func(i, j int) bool {
		if cat.Artifacts[i].OS != cat.Artifacts[j].OS {
			return cat.Artifacts[i].OS < cat.Artifacts[j].OS
		}
		return cat.Artifacts[i].Arch < cat.Artifacts[j].Arch
	})
	return cat, nil
}

// parseName accepts exactly fgctl-<os>-<arch> and fgctl-<os>-<arch>.exe.
//
// Being strict here is what makes Path safe: a request can only ever name an artefact that came
// out of this function, so no request can describe a path at all.
func parseName(name string) (goos, goarch string, ok bool) {
	base := strings.TrimSuffix(name, ".exe")
	rest, found := strings.CutPrefix(base, "fgctl-")
	if !found {
		return "", "", false
	}
	goos, goarch, found = strings.Cut(rest, "-")
	if !found || goos == "" || goarch == "" {
		return "", "", false
	}
	// A second dash means something like fgctl-linux-amd64-v2, which this scheme does not describe.
	if strings.Contains(goarch, "-") {
		return "", "", false
	}
	if (goos == "windows") != strings.HasSuffix(name, ".exe") {
		// A windows build without .exe, or a unix one with it, is a build-script bug. Skipping it
		// is better than serving a file whose name tells the downloader the wrong thing.
		return "", "", false
	}
	return goos, goarch, true
}

// Find returns the artefact with this exact name.
func (c *Catalog) Find(name string) (Artifact, bool) {
	for _, a := range c.Artifacts {
		if a.Name == name {
			return a, true
		}
	}
	return Artifact{}, false
}

// For returns the artefact for a platform, as runtime.GOOS/GOARCH name it.
func (c *Catalog) For(goos, goarch string) (Artifact, bool) {
	for _, a := range c.Artifacts {
		if a.OS == goos && a.Arch == goarch {
			return a, true
		}
	}
	return Artifact{}, false
}

// Path is the file backing an artefact.
//
// It joins with the base name only, so even a caller that passes an attacker-controlled string
// cannot escape the directory -- and Find has already restricted it to a name this catalog
// produced.
func (c *Catalog) Path(a Artifact) string {
	return filepath.Join(c.dir, filepath.Base(a.Name))
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
