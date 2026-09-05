package apk

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The signer of every fixture, taken from a tool that is not this one.
//
// `apksigner verify --print-certs backend/internal/apk/testdata/fixture-v1.apk` prints
// "certificate SHA-256 digest: 32d7…", and this constant is that value. That is the whole point of
// hard-coding it: a test that compared Parse's output against Parse's output would agree with a
// digest taken over the public key, over the signature record, or over the wrong certificate — all
// of which are stable, all of which look like signer identities, and none of which is the number
// the phone computes. Android's Signature.toByteArray() is the DER certificate, and this is its
// SHA-256.
const debugSigner = "32d7a8778a00785f25f5e7987791067ce809b43dc451ec53301f0ff3af0d7feb"

const fixturePackage = "io.github.helios57.familyguard.fixture"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		// NOT a skip. A missing fixture makes every assertion below vacuous, and a suite that
		// skips its only real input reports green having measured nothing.
		t.Fatalf("read %s: %v (regenerate with tests/apk/regenerate-fixtures.sh)", name, err)
	}
	return b
}

func parseBytes(t *testing.T, b []byte) (*Info, error) {
	t.Helper()
	return Parse(bytes.NewReader(b), int64(len(b)))
}

func TestParseReadsARealV2SignedAPK(t *testing.T) {
	info, err := parseBytes(t, fixture(t, "fixture-v1.apk"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if info.PackageName != fixturePackage {
		t.Errorf("package name = %q, want %q", info.PackageName, fixturePackage)
	}
	if info.VersionCode != 1 {
		t.Errorf("version code = %d, want 1", info.VersionCode)
	}
	if info.VersionName != "0.0.1" {
		t.Errorf("version name = %q, want %q", info.VersionName, "0.0.1")
	}
	if info.MinSDK != 29 {
		t.Errorf("min sdk = %d, want 29 (the Galaxy S20 floor, NFR-13)", info.MinSDK)
	}
	if info.SignerSHA256 != debugSigner {
		t.Errorf("signer = %q, want %q (apksigner's own value)", info.SignerSHA256, debugSigner)
	}
	if info.Label != "FamilyGuard test fixture" {
		t.Errorf("label = %q, want the literal from the fixture manifest", info.Label)
	}
}

// The upgrade relation the applier depends on, read off two real files rather than asserted.
func TestTheTwoRevisionsDifferOnlyInVersion(t *testing.T) {
	v1, err := parseBytes(t, fixture(t, "fixture-v1.apk"))
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	v2, err := parseBytes(t, fixture(t, "fixture-v2.apk"))
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	if v1.PackageName != v2.PackageName {
		t.Fatalf("the two revisions are different packages: %q and %q", v1.PackageName, v2.PackageName)
	}
	if v1.SignerSHA256 != v2.SignerSHA256 {
		t.Fatalf("the two revisions have different signers; an upgrade test built on them would be "+
			"measuring a signer change: %q and %q", v1.SignerSHA256, v2.SignerSHA256)
	}
	if !(v2.VersionCode > v1.VersionCode) {
		t.Fatalf("v2 (%d) is not newer than v1 (%d)", v2.VersionCode, v1.VersionCode)
	}
	if v1.SHA256 == v2.SHA256 {
		t.Fatal("the two revisions hash the same; they are the same file")
	}
}

// The release build signs v3-only, so a parser exercised only on v2 is a parser exercised only on
// what production does not ship.
func TestParseReadsAV3OnlySignedAPK(t *testing.T) {
	info, err := parseBytes(t, fixture(t, "fixture-v3.apk"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if info.SignerSHA256 != debugSigner {
		t.Errorf("signer = %q, want %q", info.SignerSHA256, debugSigner)
	}
	if info.PackageName != fixturePackage {
		t.Errorf("package name = %q, want %q", info.PackageName, fixturePackage)
	}
}

// SHA256 must describe the delivered bytes, because that is what the phone re-computes.
func TestTheDigestIsOverTheWholeFile(t *testing.T) {
	raw := fixture(t, "fixture-v1.apk")
	info, err := parseBytes(t, raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// The value apksigner and sha256sum agree on for this file.
	const want = "4843b0847080c07f54c42c03ebecebd318b615245b914d4f682af76c01d8c9d4"
	if info.SHA256 != want {
		t.Errorf("sha256 = %q, want %q (sha256sum's value for the same file)", info.SHA256, want)
	}
	if info.Size != int64(len(raw)) {
		t.Errorf("size = %d, want %d", info.Size, len(raw))
	}
}

// ---- the refusals ------------------------------------------------------------------------------

// An APK rewritten by a zip writer that knows nothing about the signing block: valid zip, valid
// manifest, no signer. Derived from a checked-in fixture rather than stored, so it cannot drift
// away from the file it is supposed to be a variant of.
func stripSigningBlock(t *testing.T, raw []byte) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("read the fixture as a zip: %v", err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatalf("create %s: %v", f.Name, err)
		}
		if _, err := io.Copy(w, rc); err != nil {
			t.Fatalf("copy %s: %v", f.Name, err)
		}
		rc.Close()
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close the rewritten archive: %v", err)
	}
	return out.Bytes()
}

func TestAnUnsignedAPKIsRefused(t *testing.T) {
	stripped := stripSigningBlock(t, fixture(t, "fixture-v1.apk"))

	// The positive control. If the rewrite broke the manifest too, the refusal below would be the
	// right answer for the wrong reason, and the test would keep passing after signerDigest stopped
	// being called at all.
	raw, err := zipEntry(stripped, "AndroidManifest.xml")
	if err != nil {
		t.Fatalf("the rewritten archive has no readable manifest, so this case proves nothing: %v", err)
	}
	if m, err := parseManifest(raw); err != nil || m.pkg != fixturePackage {
		t.Fatalf("the rewritten archive's manifest no longer parses (%v / %+v); this case would be "+
			"refusing a broken zip rather than an unsigned APK", err, m)
	}

	_, err = parseBytes(t, stripped)
	if !errors.Is(err, ErrNoSigningBlock) {
		t.Fatalf("Parse accepted an APK with no signing block (err = %v)", err)
	}
}

func TestATruncatedFileIsRefused(t *testing.T) {
	raw := fixture(t, "fixture-v1.apk")
	if _, err := parseBytes(t, raw[:len(raw)/2]); err == nil {
		t.Fatal("Parse accepted half an APK")
	}
}

func TestSomethingThatIsNotAZipIsRefused(t *testing.T) {
	if _, err := parseBytes(t, []byte("this is not an apk, it is a sentence")); err == nil {
		t.Fatal("Parse accepted a text file")
	}
}

func TestAZipWithNoManifestIsRefused(t *testing.T) {
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	w, _ := zw.Create("classes.dex")
	_, _ = w.Write([]byte("dex\n035\x00"))
	_ = zw.Close()

	_, err := parseBytes(t, out.Bytes())
	if !errors.Is(err, ErrNoManifest) {
		t.Fatalf("err = %v, want ErrNoManifest", err)
	}
}

func TestAnEmptyFileIsRefused(t *testing.T) {
	if _, err := Parse(bytes.NewReader(nil), 0); err == nil {
		t.Fatal("Parse accepted a zero-byte file")
	}
}

func TestSomethingLargerThanTheCapIsRefusedWithoutReadingIt(t *testing.T) {
	// A reader that would panic if touched: the size check has to happen before any read, or the
	// cap is documentation rather than a limit.
	_, err := Parse(explodingReaderAt{t}, MaxSize+1)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

type explodingReaderAt struct{ t *testing.T }

func (e explodingReaderAt) ReadAt([]byte, int64) (int, error) {
	e.t.Fatal("Parse read the file before checking its size against MaxSize")
	return 0, nil
}

// ---- the text-manifest case, which is what a hand-built APK carries --------------------------

func TestATextManifestIsRefusedAsNotBinaryXML(t *testing.T) {
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	w, _ := zw.Create("AndroidManifest.xml")
	_, _ = w.Write([]byte(`<?xml version="1.0"?><manifest package="com.example"/>`))
	_ = zw.Close()

	_, err := parseBytes(t, out.Bytes())
	if !errors.Is(err, ErrNotBinaryXML) {
		t.Fatalf("err = %v, want ErrNotBinaryXML", err)
	}
}

// ---- helpers -----------------------------------------------------------------------------------

func zipEntry(raw []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, errors.New("no such entry: " + name)
}

func TestParseFileReportsAMissingFileRatherThanADirectory(t *testing.T) {
	if _, err := ParseFile(t.TempDir()); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("err = %v, want it to name the directory", err)
	}
}

// TestEveryParseFailureIsRecognisableAsOne is a guard on the caller's contract, not on the parser.
//
// It exists because the shape it forbids was shipped and measured: the HTTP layer matched on
// ErrNoManifest, ErrNotBinaryXML and ErrNoSigningBlock, and every OTHER way of not being an APK —
// an empty body, a file that is not a zip, an archive cut in half — fell through to "500, something
// went wrong". Those are the ordinary operator mistakes. A named-error list cannot be complete by
// inspection, so the assertion is that no input reaches a caller as an unclassified error.
func TestEveryParseFailureIsRecognisableAsOne(t *testing.T) {
	good := fixture(t, "fixture-v1.apk")

	inputs := map[string][]byte{
		"empty":                    {},
		"one byte":                 {0},
		"plain text":               []byte("this is not an apk, it is a sentence"),
		"a zip that is not an apk": zipWithout(t, good),
		"truncated at the front":   good[:len(good)/2],
		"truncated at the back":    good[len(good)/4:],
		"the manifest replaced":    withManifest(t, good, []byte("<manifest package=\"a.b\"/>")),
		"no signing block":         stripSigningBlock(t, good),
	}
	for name, body := range inputs {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(bytes.NewReader(body), int64(len(body)))
			if err == nil {
				t.Fatal("parsed successfully")
			}
			if !errors.Is(err, ErrNotAnAPK) {
				t.Fatalf("error %q does not wrap ErrNotAnAPK, so a caller sees it as a server fault", err)
			}
			// And it says something. An operator who uploaded the wrong file has to learn that from
			// the message, since the status code alone cannot distinguish which wrong file it was.
			if len(err.Error()) < len(ErrNotAnAPK.Error())+4 {
				t.Errorf("error %q adds nothing to the sentinel", err)
			}
		})
	}

	// The positive control: the same call on the real fixture succeeds, so the cases above are
	// about the inputs and not about a parser that refuses everything.
	if _, err := Parse(bytes.NewReader(good), int64(len(good))); err != nil {
		t.Fatalf("the fixture itself no longer parses: %v", err)
	}

	// And the negative control on the sentinel: ErrTooLarge must NOT wrap it. A file that is too
	// big to read may be a perfectly good application, and folding the two together would answer
	// "that is not an application" to someone whose only problem is a size limit.
	_, err := Parse(bytes.NewReader(good), MaxSize+1)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an oversized file reported %v", err)
	}
	if errors.Is(err, ErrNotAnAPK) {
		t.Fatal("ErrTooLarge wraps ErrNotAnAPK; the two conditions need different answers")
	}
}

// zipWithout returns a valid zip archive that is not an APK: the fixture with its manifest removed.
func zipWithout(t *testing.T, apk []byte) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(apk), int64(len(apk)))
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	kept := 0
	for _, f := range zr.File {
		if f.Name == "AndroidManifest.xml" {
			continue
		}
		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatalf("create %s: %v", f.Name, err)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		if _, err := io.Copy(w, rc); err != nil {
			t.Fatalf("copy %s: %v", f.Name, err)
		}
		rc.Close()
		kept++
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if kept == 0 {
		t.Fatal("the rebuilt archive is empty; this input would measure the wrong thing")
	}
	return buf.Bytes()
}

// withManifest rebuilds the archive with a different AndroidManifest.xml — here, plain text where
// binary XML belongs, which is what a build tool misconfiguration produces.
func withManifest(t *testing.T, apk, manifest []byte) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(apk), int64(len(apk)))
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	replaced := false
	for _, f := range zr.File {
		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatalf("create %s: %v", f.Name, err)
		}
		if f.Name == "AndroidManifest.xml" {
			if _, err := w.Write(manifest); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			replaced = true
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		if _, err := io.Copy(w, rc); err != nil {
			t.Fatalf("copy %s: %v", f.Name, err)
		}
		rc.Close()
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !replaced {
		t.Fatal("the fixture has no AndroidManifest.xml to replace")
	}
	return buf.Bytes()
}
