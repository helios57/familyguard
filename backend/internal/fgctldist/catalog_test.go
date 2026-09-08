package fgctldist

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func TestScanReadsOnlyWellFormedNames(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "fgctl-linux-amd64", "linux amd64 bytes")
	write(t, dir, "fgctl-windows-amd64.exe", "windows bytes")
	write(t, dir, "fgctl-darwin-arm64", "darwin bytes")
	// None of these describe an artefact, and none of them may become one.
	write(t, dir, "SHA256SUMS", "irrelevant")
	write(t, dir, "README.md", "irrelevant")
	write(t, dir, "fgctl", "no platform in the name")
	write(t, dir, "fgctl-linux", "no arch")
	write(t, dir, "fgctl-linux-amd64-v2", "a variant this scheme does not describe")
	// The build script producing either of these would be a bug, and serving them would tell the
	// downloader the wrong thing about the file it is getting.
	write(t, dir, "fgctl-windows-arm64", "a windows build with no .exe")
	write(t, dir, "fgctl-linux-386.exe", "a unix build with .exe")

	cat, err := Scan(dir, "v1.2.3")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := map[string]bool{}
	for _, a := range cat.Artifacts {
		got[a.Name] = true
	}
	for _, want := range []string{"fgctl-linux-amd64", "fgctl-windows-amd64.exe", "fgctl-darwin-arm64"} {
		if !got[want] {
			t.Errorf("Scan dropped %q", want)
		}
	}
	if len(cat.Artifacts) != 3 {
		t.Errorf("Scan accepted something it should not have: %v", cat.Artifacts)
	}
	if cat.Version != "v1.2.3" {
		t.Errorf("version = %q", cat.Version)
	}
}

func TestScanChecksumsTheBytesOnDisk(t *testing.T) {
	dir := t.TempDir()
	body := "the bytes that will be served"
	write(t, dir, "fgctl-linux-amd64", body)

	cat, err := Scan(dir, "v1")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	a, ok := cat.For("linux", "amd64")
	if !ok {
		t.Fatal("linux/amd64 is missing")
	}
	sum := sha256.Sum256([]byte(body))
	if a.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("SHA256 = %s, which is not the hash of the file", a.SHA256)
	}
	if a.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", a.Size, len(body))
	}
	// The negative control for the assertion above: a different body must produce a different
	// digest, or the comparison is satisfied by any constant.
	write(t, dir, "fgctl-linux-arm64", body+" but different")
	cat2, err := Scan(dir, "v1")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	other, _ := cat2.For("linux", "arm64")
	if other.SHA256 == a.SHA256 {
		t.Error("two different files hashed the same, so the digest is not being computed")
	}
}

// Find is what stops a request naming a path. If it ever accepted something it did not produce,
// Path would join it against the catalog directory.
func TestFindRefusesAnythingItDidNotProduce(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "fgctl-linux-amd64", "bytes")
	cat, err := Scan(dir, "v1")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if _, ok := cat.Find("fgctl-linux-amd64"); !ok {
		t.Fatal("Find rejected the artefact it catalogued, so the refusals below prove nothing")
	}
	for _, bad := range []string{
		"../../../etc/passwd",
		"fgctl-linux-amd64/../../etc/passwd",
		"/etc/passwd",
		"",
		"SHA256SUMS",
	} {
		if _, ok := cat.Find(bad); ok {
			t.Errorf("Find accepted %q", bad)
		}
	}
}
