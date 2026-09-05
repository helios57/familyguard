// Package apk reads what an Android application package says about itself.
//
// It answers four questions and nothing else: which package is this, which version, what is the
// lowest Android it will install on, and who signed it. That is exactly what the app catalog needs
// to store a file and what a phone needs to decide whether to converge on it.
//
// It exists because the server had no way to answer any of them. Before FR-16 the only APK this
// deployment hosted was the DPC itself, described by two environment variables — APK_PATH for the
// bytes and APK_CERT_PATH for a separately supplied DER certificate — and an operator who put the
// wrong pair together would have published a checksum for one artifact and a signer for another
// with nothing to notice. A catalog of arbitrary applications cannot be operated that way: a
// package name typed by hand is a package name that will eventually be wrong, and a wrong one here
// means a phone installing something nobody chose.
//
// **Nothing here verifies a signature.** See signing.go for why that is the right division of
// labour and where the real check lives.
package apk

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// MaxSize bounds what this package will read.
//
// A cap is required rather than prudent: parseManifest reads the whole manifest into memory and the
// zip reader will happily inflate whatever the archive claims. 512 MB is far above any real
// application (the DPC is 16 MB, the largest thing on Play is a fraction of this) and far below
// anything that could exhaust the node.
const MaxSize = 512 << 20

// ErrTooLarge is returned for a file above MaxSize. It deliberately does NOT wrap ErrNotAnAPK: a
// file that is too big to read may be a perfectly good APK, and the two need different answers —
// "make it smaller" against "that is not an application".
var ErrTooLarge = errors.New("apk is larger than this server will read")

// ErrNotAnAPK is wrapped by every failure that means "these bytes are not a readable Android
// application", whatever specifically went wrong inside them.
//
// One sentinel exists because the alternative was measured: the caller matched on the three named
// errors below, and everything else — an empty body, a file that is not a zip at all, a truncated
// archive — fell through to a 500 saying "something went wrong". Those are the ORDINARY mistakes,
// the ones an operator actually makes, and they are exactly the ones the specific errors did not
// cover. A parse failure has one shape to the caller and the detail is in the message.
var ErrNotAnAPK = errors.New("not a readable Android application")

// ErrNoManifest means the archive has no AndroidManifest.xml, which makes it a zip and not an APK.
var ErrNoManifest = fmt.Errorf("%w: archive has no AndroidManifest.xml", ErrNotAnAPK)

// Info is everything read out of one APK.
//
// SHA256 is over the file as delivered, not over any part of it: it is what the phone re-computes
// after downloading, so it has to describe the same bytes end to end.
type Info struct {
	PackageName  string `json:"package_name"`
	VersionCode  int64  `json:"version_code"`
	VersionName  string `json:"version_name"`
	MinSDK       int    `json:"min_sdk"`
	Label        string `json:"label,omitempty"`
	SignerSHA256 string `json:"signer_sha256"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
}

// ParseFile reads an APK from disk.
func ParseFile(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	return Parse(f, st.Size())
}

// Parse reads an APK from anything addressable.
//
// The reader is used three times over the same bytes — the zip directory, the manifest entry, and
// a whole-file hash — which is why it takes an io.ReaderAt rather than a stream. An uploaded APK is
// spooled to a file before it gets here for the same reason: a single pass cannot both find the
// signing block (at the end) and read the manifest (near the start) without buffering everything
// anyway, and buffering it in memory is what MaxSize would then have to defend.
func Parse(r io.ReaderAt, size int64) (*Info, error) {
	if size <= 0 {
		return nil, fmt.Errorf("%w: it is %d bytes", ErrNotAnAPK, size)
	}
	if size > MaxSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, size)
	}

	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: it is not a zip archive (%v)", ErrNotAnAPK, err)
	}
	raw, err := readEntry(zr, "AndroidManifest.xml")
	if err != nil {
		return nil, err
	}
	manifest, err := parseManifest(raw)
	if err != nil {
		return nil, err
	}
	if manifest.pkg == "" {
		return nil, fmt.Errorf("%w: <manifest> declares no package", ErrNotBinaryXML)
	}
	if manifest.versionCode == nil {
		return nil, fmt.Errorf("%w: <manifest> declares no android:versionCode", ErrNotBinaryXML)
	}

	signer, err := signerDigest(r, size)
	if err != nil {
		return nil, err
	}

	sum := sha256.New()
	if _, err := io.Copy(sum, io.NewSectionReader(r, 0, size)); err != nil {
		return nil, fmt.Errorf("hash the file: %w", err)
	}

	info := &Info{
		PackageName: manifest.pkg,
		// The platform's own longVersionCode: the major half, when present, is the high 32 bits.
		// Dropping it would make two genuinely different builds compare equal, and the comparison
		// is what decides whether a phone installs an upgrade.
		VersionCode:  int64(deref(manifest.versionCodeMajor))<<32 | int64(*manifest.versionCode),
		VersionName:  manifest.versionName,
		MinSDK:       int(deref(manifest.minSDK)),
		Label:        manifest.label,
		SignerSHA256: signer,
		SHA256:       hex.EncodeToString(sum.Sum(nil)),
		Size:         size,
	}
	return info, nil
}

func readEntry(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		// An entry whose declared uncompressed size is absurd is refused before it is opened, so a
		// zip bomb costs a comparison rather than the memory it asked for.
		if f.UncompressedSize64 > MaxSize {
			return nil, fmt.Errorf("%w: %s declares %d bytes", ErrTooLarge, name, f.UncompressedSize64)
		}
		rc, err := f.Open()
		if err != nil {
			// A directory entry that will not open is a corrupt archive, not a server fault.
			return nil, fmt.Errorf("%w: %s could not be opened (%v)", ErrNotAnAPK, name, err)
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, MaxSize))
	}
	return nil, ErrNoManifest
}

func deref(p *uint32) uint32 {
	if p == nil {
		return 0
	}
	return *p
}
