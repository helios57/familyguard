// Package catalog owns the APK files on disk and keeps them in step with the rows that describe
// them.
//
// It exists because "the catalog" is two things that can disagree — a directory on the node and a
// table in Postgres — and every interesting failure of FR-16 is a disagreement between them: a row
// whose file was deleted, a file nothing points at, two rows claiming one filename. Putting both
// sides behind one type is what makes the ordering a decision taken once rather than at each of the
// three ingestion routes.
//
// The ordering, everywhere: **parse, then claim the row, then move the file into place.** The
// database is the authority on what exists, so it is what a race is resolved against; the file is
// put where the row says only once the row is there to say it. The reverse order — file first —
// produces an orphan on every failed insert, and orphans in a directory an operator also writes to
// cannot be distinguished from files they put there on purpose.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/helios57/familyguard/backend/internal/apk"
	"github.com/helios57/familyguard/backend/internal/store"
)

// ErrNotConfigured means this deployment has no APK_DIR, so there is nowhere to keep an APK.
//
// Not an error state. A control plane may legitimately manage no applications, and a parent who is
// told so can act on it; a 500 sends them to the logs of a server that is working correctly.
var ErrNotConfigured = errors.New("this server is not configured to host applications (APK_DIR is unset)")

// ErrReservedPackage means the APK is the DPC itself (FR-16.6). See New.
var ErrReservedPackage = errors.New("that package cannot be held in the app catalog")

// packageNamePattern is what may become part of a filename.
//
// Android's own rule is looser than this, but every character it allows beyond these is one this
// code would have to reason about inside a path. A package name arrives from a file that a stranger
// may have built, and it is used to NAME something on disk: `filepath.Base` alone would already
// stop traversal, and this stops the whole question being asked.
var packageNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)*$`)

// Catalog is the directory plus the rows.
type Catalog struct {
	dir   string
	store *store.Store
	log   *slog.Logger
	// reserved is the one package this catalog will not hold: the DPC's own.
	reserved string
}

// New returns a catalog over dir. An empty dir yields a catalog whose every operation reports
// ErrNotConfigured, which is what lets the handlers exist unconditionally.
//
// reserved is the DPC's package name. It is refused rather than accepted-and-ignored because the
// DPC already has its own update path (FR-15), which reads /device/apk-info and compares against
// the running app. A catalog entry for the same package would give the phone two descriptions of
// what version of itself it should be running, converging on each other's output every sync — and
// the managed-app applier's uninstall branch would try to remove the app doing the uninstalling.
func New(dir string, st *store.Store, log *slog.Logger, reserved string) *Catalog {
	return &Catalog{dir: dir, store: st, log: log, reserved: reserved}
}

// Configured reports whether this deployment hosts applications at all.
func (c *Catalog) Configured() bool { return c.dir != "" }

// Register spools an APK, reads it, and adds it to the catalog.
//
// The reader is spooled to a temporary file in the destination directory rather than to /tmp for
// two reasons: the parser needs random access over the whole file (the manifest is near the start
// and the signing block at the very end), and a rename within one filesystem is atomic, so the file
// appears under its final name complete or not at all. A spool elsewhere would make the last step a
// copy, and a copy can be interrupted halfway.
//
// limit caps what will be read. It is passed in rather than taken from apk.MaxSize so that the HTTP
// layer's own body limit and this one are the same number stated once at the caller.
func (c *Catalog) Register(ctx context.Context, r io.Reader, limit int64, source, label string) (*store.App, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	spool, err := os.CreateTemp(c.dir, ".incoming-*.apk")
	if err != nil {
		return nil, fmt.Errorf("stage the upload: %w", err)
	}
	staged := spool.Name()
	// Removed on every path that does not rename it away. A rename makes this a no-op on a name
	// that no longer exists, which is exactly the behaviour wanted — there is no branch here that
	// has to remember whether the file was moved.
	defer func() {
		_ = spool.Close()
		_ = os.Remove(staged)
	}()

	// +1 so that a file exactly at the limit is accepted and one byte over is detected, rather than
	// being silently truncated to the limit and then parsed as a corrupt archive.
	written, err := io.Copy(spool, io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read the upload: %w", err)
	}
	if written > limit {
		return nil, fmt.Errorf("%w: more than %d bytes", apk.ErrTooLarge, limit)
	}
	if err := spool.Sync(); err != nil {
		return nil, fmt.Errorf("flush the upload: %w", err)
	}

	info, err := apk.Parse(spool, written)
	if err != nil {
		return nil, err
	}
	if !packageNamePattern.MatchString(info.PackageName) {
		return nil, fmt.Errorf("%q is not a package name this server will store", info.PackageName)
	}
	if c.reserved != "" && info.PackageName == c.reserved {
		return nil, fmt.Errorf("%w: %s is the FamilyGuard app itself, which updates through "+
			"/device/apk-info rather than the app catalog", ErrReservedPackage, info.PackageName)
	}

	fileName := fmt.Sprintf("%s-%d.apk", info.PackageName, info.VersionCode)
	app, err := c.store.RegisterApp(ctx, store.App{
		PackageName:  info.PackageName,
		VersionCode:  info.VersionCode,
		VersionName:  info.VersionName,
		Label:        displayLabel(label, info),
		SHA256:       info.SHA256,
		SignerSHA256: info.SignerSHA256,
		SizeBytes:    info.Size,
		MinSDK:       info.MinSDK,
		FileName:     fileName,
		Source:       source,
	})
	if err != nil {
		return nil, err
	}

	// The row may be one that already existed — RegisterApp returns it when the bytes match — and
	// in that case the file it names is normally already here. "Normally" is the reason this is a
	// check and not an assumption: a row whose file was removed from the node is a real state, it
	// has no symptom until a phone tries to download, and re-uploading the same APK is exactly what
	// an operator would do to fix it. So the rename happens unless the correct file is already in
	// place.
	final := filepath.Join(c.dir, filepath.Base(app.FileName))
	if existing, statErr := os.Stat(final); statErr == nil && existing.Size() == app.SizeBytes {
		return app, nil
	}
	if err := os.Rename(staged, final); err != nil {
		// The row is now there and the file is not. Reported rather than repaired: deleting the row
		// would also delete a row that a concurrent, successful upload had just created.
		c.log.Error("an APK was registered but its file could not be put in place",
			"package", app.PackageName, "version_code", app.VersionCode, "path", final, "error", err)
		return nil, fmt.Errorf("store the upload: %w", err)
	}
	return app, nil
}

// RegisterFile adds one APK that is already in the directory, under the name it already has.
//
// Deliberately different from Register: it does NOT rename. The directory is somewhere an operator
// puts files by hand, and a scan that renamed their files would be a scan that fights them — the
// second copy landing under the canonical name while the original sits beside it, both registered,
// one of them unreachable.
func (c *Catalog) RegisterFile(ctx context.Context, name string) (*store.App, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	base := filepath.Base(name)
	info, err := apk.ParseFile(filepath.Join(c.dir, base))
	if err != nil {
		return nil, err
	}
	if !packageNamePattern.MatchString(info.PackageName) {
		return nil, fmt.Errorf("%q is not a package name this server will store", info.PackageName)
	}
	if c.reserved != "" && info.PackageName == c.reserved {
		return nil, fmt.Errorf("%w: %s is the FamilyGuard app itself, which updates through "+
			"/device/apk-info rather than the app catalog", ErrReservedPackage, info.PackageName)
	}
	return c.store.RegisterApp(ctx, store.App{
		PackageName:  info.PackageName,
		VersionCode:  info.VersionCode,
		VersionName:  info.VersionName,
		Label:        displayLabel("", info),
		SHA256:       info.SHA256,
		SignerSHA256: info.SignerSHA256,
		SizeBytes:    info.Size,
		MinSDK:       info.MinSDK,
		FileName:     base,
		Source:       store.AppSourceNode,
	})
}

// ScanResult is what one pass over the directory found.
//
// Failed is counted and named rather than only logged: "the scan ran" and "the scan ran and read
// everything" are different facts, and a startup that reports the first while meaning the second is
// how a directory ends up half-registered with nobody aware of it.
type ScanResult struct {
	Registered []store.App
	Failed     map[string]string
}

// Scan registers every APK in the directory that is not registered already.
//
// One bad file does not stop the pass. An operator's directory will eventually contain a partial
// copy, a zip that is not an APK, or a build signed with the wrong key, and a scan that aborted on
// the first would leave every file after it unregistered — sorted order, so which ones is a matter
// of alphabet.
func (c *Catalog) Scan(ctx context.Context) (*ScanResult, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", c.dir, err)
	}
	out := &ScanResult{Registered: []store.App{}, Failed: map[string]string{}}
	for _, e := range entries {
		name := e.Name()
		// `.incoming-*.apk` is this package's own spool. A scan racing an upload would otherwise
		// read a half-written file and record its failure as though the operator had put something
		// broken there.
		if e.IsDir() || !strings.HasSuffix(name, ".apk") || strings.HasPrefix(name, ".") {
			continue
		}
		app, err := c.RegisterFile(ctx, name)
		if err != nil {
			out.Failed[name] = err.Error()
			c.log.Warn("an APK in the catalog directory could not be registered",
				"file", name, "error", err)
			continue
		}
		out.Registered = append(out.Registered, *app)
	}
	return out, nil
}

// Open returns the file behind a catalog row, and refuses to return one whose size disagrees.
//
// The size check is cheap and catches the case the whole feature is most exposed to: a file
// replaced in place on the node, under a name a row already claims. The row's SHA-256 then
// describes bytes that are gone, and every phone that downloads will refuse the result — correctly,
// and with no way to tell from the device end that the server is the one that changed. Failing here
// puts the diagnosis where the fault is. It is not a re-hash: hashing 30 MB on every download would
// be paid on every sync of every phone, and the size catches a replacement by anything that is not
// the same length.
func (c *Catalog) Open(app *store.App) (*os.File, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	path := filepath.Join(c.dir, filepath.Base(app.FileName))
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Size() != app.SizeBytes {
		_ = f.Close()
		return nil, fmt.Errorf("%s is %d bytes and the catalog says %d; the file was replaced without being re-registered",
			app.FileName, info.Size(), app.SizeBytes)
	}
	return f, nil
}

// Remove deletes the file behind a row that has already been deleted.
//
// A missing file is not an error: the row is gone either way, and the operator may well have
// deleted the file first — which is the most likely reason they are now removing the row.
func (c *Catalog) Remove(app *store.App) error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	err := os.Remove(filepath.Join(c.dir, filepath.Base(app.FileName)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// displayLabel picks what the console shows, preferring what a person supplied.
//
// The manifest's own label is usually a resource reference this server does not resolve, so it is
// often empty; the package name is the honest last resort. What is never done is inventing one from
// the filename — a file called `muplay-final-2.apk` would become an app called "muplay final 2".
func displayLabel(supplied string, info *apk.Info) string {
	if l := strings.TrimSpace(supplied); l != "" {
		return l
	}
	if info.Label != "" {
		return info.Label
	}
	return info.PackageName
}
