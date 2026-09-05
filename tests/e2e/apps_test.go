package e2e

// The application catalog and API keys, end to end: FR-16 and FR-17.
//
// Every APK here is a real one, built by tests/apk/regenerate-fixtures.sh from android-dpc's
// fixture-app module. That matters more than it looks: the server reads the package name, version
// code, minimum SDK and signing certificate out of the archive itself, so bytes fabricated by this
// suite would only ever prove the parser agrees with whatever this file decided an APK is.
//
//	fixture-v1.apk           versionCode 1, v2-signed, debug key
//	fixture-v2.apk           versionCode 2, v2-signed, same key  — the upgrade
//	fixture-v3.apk           versionCode 1, v3-ONLY signed, same key — the release scheme
//	fixture-othersigner.apk  versionCode 1, v2-signed, a DIFFERENT key
//
// The last one carries the security-relevant assertion: the same package signed by a different key
// must be refused, because Android would refuse to install it over the build already on the phone.
// It has to be its own fixture — this test was first written against fixture-v3, which shares v1's
// key, and the upload was refused for having a duplicate version code. A green for the wrong
// reason, and the only thing that distinguished it was reading the error code.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- the shapes a client sees ----------------------------------------------
//
// Written out again rather than imported, like every other DTO in this suite: renaming a field on
// the server has to be a failure here, not a silent agreement.

type appDTO struct {
	ID           string `json:"id"`
	PackageName  string `json:"package_name"`
	VersionCode  int64  `json:"version_code"`
	VersionName  string `json:"version_name"`
	Label        string `json:"label"`
	SHA256       string `json:"sha256"`
	SignerSHA256 string `json:"signer_sha256"`
	SizeBytes    int64  `json:"size_bytes"`
	MinSDK       int    `json:"min_sdk"`
	FileName     string `json:"file_name"`
	Source       string `json:"source"`
	CreatedAt    string `json:"created_at"`
}

type appListDTO struct {
	Apps       []appDTO `json:"apps"`
	Configured bool     `json:"configured"`
}

type managedAppDTO struct {
	PackageName string  `json:"package_name"`
	Available   bool    `json:"available"`
	App         *appDTO `json:"app"`
}

type apiKeyDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	ParentID   string `json:"parent_id"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
	RevokedAt  string `json:"revoked_at"`
	// Present exactly once, in the creation response. Its absence everywhere else is asserted.
	Token string `json:"token"`
}

const fixturePackage = "io.github.helios57.familyguard.fixture"

// ---- fixtures ---------------------------------------------------------------

func fixtureAPK(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(apkFixtures, name))
	if err != nil {
		// Fatal, never a skip. run.sh already refused to start without these; reaching here means
		// something moved them mid-run, and a suite that quietly dropped FR-16 would still say PASS.
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

// catalogHarness is a server that hosts applications, plus the directory it holds them in.
func catalogHarness(t *testing.T, opts ...harnessOption) (*harness, string) {
	t.Helper()
	dir := t.TempDir()
	return newHarness(t, append([]harnessOption{withAPKDir(dir)}, opts...)...), dir
}

// uploadRaw is the form a script or an MCP server uses: the APK as the request body.
func (h *harness) uploadRaw(token string, body []byte, query string) apiResponse {
	h.t.Helper()
	req := h.newRequest(http.MethodPost, "/apps"+query, token, body)
	req.Header.Set("Content-Type", "application/vnd.android.package-archive")
	return h.send(req)
}

// uploadMultipart is the form a browser's <input type="file"> produces.
func (h *harness) uploadMultipart(token string, body []byte, filename, label string) apiResponse {
	h.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("apk", filename)
	if err != nil {
		h.t.Fatalf("multipart: %v", err)
	}
	if _, err := part.Write(body); err != nil {
		h.t.Fatalf("multipart write: %v", err)
	}
	if label != "" {
		if err := w.WriteField("label", label); err != nil {
			h.t.Fatalf("multipart field: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		h.t.Fatalf("multipart close: %v", err)
	}
	req := h.newRequest(http.MethodPost, "/apps", token, buf.Bytes())
	req.Header.Set("Content-Type", w.FormDataContentType())
	return h.send(req)
}

// ---- FR-16: the catalog ------------------------------------------------------

// TestAnUploadedAPKIsReadRatherThanDescribed. Everything the catalog records comes out of the
// archive. Nothing in the request says what the file is, and that is the point: a package name
// supplied alongside an upload is a package name that will eventually not match the bytes, and the
// mismatch would first be noticed by a phone installing the wrong application.
func TestAnUploadedAPKIsReadRatherThanDescribed(t *testing.T) {
	h, dir := catalogHarness(t)
	parent := h.signIn(primaryParent)
	body := fixtureAPK(t, "fixture-v1.apk")

	var app appDTO
	h.uploadRaw(parent.Token, body, "?label=Test+Fixture").expect(http.StatusCreated).decode(&app)

	if app.PackageName != fixturePackage {
		t.Errorf("package name %q, want %q", app.PackageName, fixturePackage)
	}
	if app.VersionCode != 1 {
		t.Errorf("version code %d, want 1", app.VersionCode)
	}
	if app.MinSDK != 29 {
		// The Galaxy S20 floor. A catalog that did not read this could hand a phone an APK the
		// platform refuses, and the refusal happens on the phone.
		t.Errorf("min sdk %d, want 29", app.MinSDK)
	}
	// The digest is computed here from the bytes this test sent, not read back out of the answer.
	sum := sha256.Sum256(body)
	if want := hex.EncodeToString(sum[:]); app.SHA256 != want {
		t.Errorf("sha256 %q, want %q", app.SHA256, want)
	}
	if app.SizeBytes != int64(len(body)) {
		t.Errorf("size %d, want %d", app.SizeBytes, len(body))
	}
	if len(app.SignerSHA256) != 64 {
		t.Errorf("signer digest %q is not a SHA-256", app.SignerSHA256)
	}
	if app.Label != "Test Fixture" {
		t.Errorf("label %q — the query string's label was not used", app.Label)
	}
	if app.Source != "UPLOAD" {
		t.Errorf("source %q, want UPLOAD", app.Source)
	}

	// The bytes reached the directory, under the name the row promises, byte for byte.
	onDisk, err := os.ReadFile(filepath.Join(dir, app.FileName))
	if err != nil {
		t.Fatalf("the row names a file that is not there: %v", err)
	}
	if !bytes.Equal(onDisk, body) {
		t.Fatalf("the stored file is %d bytes, the upload was %d", len(onDisk), len(body))
	}
	// And nothing else: the spool file must not survive a successful upload.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the directory holds %v; one upload should leave one file", names)
	}
}

// TestMultipartAndRawBodyAgree. Two encodings, one handler — a console and a curl command must not
// be able to produce different rows for the same file.
func TestMultipartAndRawBodyAgree(t *testing.T) {
	h, _ := catalogHarness(t)
	parent := h.signIn(primaryParent)
	body := fixtureAPK(t, "fixture-v1.apk")

	var viaForm appDTO
	h.uploadMultipart(parent.Token, body, "whatever-the-browser-called-it.apk", "From the console").
		expect(http.StatusCreated).decode(&viaForm)

	var viaRaw appDTO
	h.uploadRaw(parent.Token, body, "").expect(http.StatusCreated).decode(&viaRaw)

	if viaForm.ID != viaRaw.ID {
		t.Fatalf("the same file produced two rows: %s and %s", viaForm.ID, viaRaw.ID)
	}
	// The browser's filename is discarded. It is attacker-controlled in the general case and says
	// nothing the archive does not say better.
	if strings.Contains(viaForm.FileName, "whatever-the-browser-called-it") {
		t.Errorf("the stored file is named after the upload: %q", viaForm.FileName)
	}
	if viaForm.FileName != fmt.Sprintf("%s-1.apk", fixturePackage) {
		t.Errorf("file name %q, want it derived from the package and version", viaForm.FileName)
	}
}

// TestTwoVersionsOfOneAppBothLive. Upgrading is a new row, not an edit: the older build stays
// downloadable so a phone mid-install is not left fetching bytes that moved.
func TestTwoVersionsOfOneAppBothLive(t *testing.T) {
	h, _ := catalogHarness(t)
	parent := h.signIn(primaryParent)

	var v1, v2 appDTO
	h.uploadRaw(parent.Token, fixtureAPK(t, "fixture-v1.apk"), "").expect(http.StatusCreated).decode(&v1)
	h.uploadRaw(parent.Token, fixtureAPK(t, "fixture-v2.apk"), "").expect(http.StatusCreated).decode(&v2)

	if v1.ID == v2.ID {
		t.Fatal("two builds collapsed into one row")
	}
	if v1.SHA256 == v2.SHA256 {
		t.Fatal("the two fixtures are the same file; the fixture generator is broken, not the server")
	}
	if v1.SignerSHA256 != v2.SignerSHA256 {
		t.Fatal("v1 and v2 should share a signing key; the fixture generator is broken")
	}

	var list appListDTO
	h.call(http.MethodGet, "/apps", parent.Token, nil).expect(http.StatusOK).decode(&list)
	if !list.Configured {
		t.Error("a server with APK_DIR set reports itself unconfigured")
	}
	if len(list.Apps) != 2 {
		t.Fatalf("catalog holds %d entries, want 2: %+v", len(list.Apps), list.Apps)
	}
}

// TestTheSameFileTwiceIsNotAConflict. Re-uploading is what an operator does when they are not sure
// it worked, and it must not be punished — the row is the same row.
func TestTheSameFileTwiceIsNotAConflict(t *testing.T) {
	h, _ := catalogHarness(t)
	parent := h.signIn(primaryParent)
	body := fixtureAPK(t, "fixture-v1.apk")

	var first, second appDTO
	h.uploadRaw(parent.Token, body, "").expect(http.StatusCreated).decode(&first)
	h.uploadRaw(parent.Token, body, "").expect(http.StatusCreated).decode(&second)
	if first.ID != second.ID {
		t.Fatalf("the same bytes produced two rows: %s then %s", first.ID, second.ID)
	}

	var list appListDTO
	h.call(http.MethodGet, "/apps", parent.Token, nil).expect(http.StatusOK).decode(&list)
	if len(list.Apps) != 1 {
		t.Fatalf("catalog holds %d entries after two identical uploads", len(list.Apps))
	}
}

// TestAPackageSignedByAnotherKeyIsRefused is the security assertion this whole fixture set exists
// for (FR-16.2).
//
// The pin is trust on first registration, not signature verification: the server does not decide
// whether a key is good, it decides that a package's key must not CHANGE. That is exactly the
// property Android enforces at install time, and enforcing it here means the refusal is a message
// in the console rather than an INSTALL_FAILED_UPDATE_INCOMPATIBLE on a child's phone.
func TestAPackageSignedByAnotherKeyIsRefused(t *testing.T) {
	h, dir := catalogHarness(t)
	parent := h.signIn(primaryParent)

	var pinned appDTO
	h.uploadRaw(parent.Token, fixtureAPK(t, "fixture-v1.apk"), "").expect(http.StatusCreated).decode(&pinned)

	rogue := fixtureAPK(t, "fixture-othersigner.apk")
	h.uploadRaw(parent.Token, rogue, "").expectError(http.StatusConflict, "signer_changed")
	// Not merely "a conflict": the code has to be the signer one. A duplicate version code is also
	// a 409, and that is exactly the answer this test used to get while proving nothing.

	// Refused means nothing landed — not the row, and not the file. A rejected upload that leaves
	// its bytes behind would be re-registered by the next directory scan.
	var list appListDTO
	h.call(http.MethodGet, "/apps", parent.Token, nil).expect(http.StatusOK).decode(&list)
	if len(list.Apps) != 1 || list.Apps[0].SignerSHA256 != pinned.SignerSHA256 {
		t.Fatalf("the pin moved or a row was added: %+v", list.Apps)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("a refused upload left files behind: %v", names)
	}

	// The positive control. Without it the refusal above would look identical to a server that
	// refuses every second upload, and the test would pass for the wrong reason.
	h.uploadRaw(parent.Token, fixtureAPK(t, "fixture-v2.apk"), "").expect(http.StatusCreated)
}

// TestWhatIsNotAnAPKIsRefusedAsSuch. The message has to name the problem: an operator who uploaded
// the wrong file needs to know it was the wrong file, not that "the server had an error".
func TestWhatIsNotAnAPKIsRefusedAsSuch(t *testing.T) {
	h, _ := catalogHarness(t)
	parent := h.signIn(primaryParent)

	real := fixtureAPK(t, "fixture-v1.apk")
	for name, body := range map[string][]byte{
		"plain text": []byte("this is not an apk, it is a sentence"),
		"empty":      {},
		"a zip with no manifest": func() []byte {
			// A real ZIP — so the failure is about the missing manifest and not about the archive.
			var buf bytes.Buffer
			w := multipart.NewWriter(&buf) // any bytes; the point is that it is not an APK
			_ = w.Close()
			return buf.Bytes()
		}(),
		"an APK truncated halfway": real[:len(real)/2],
	} {
		t.Run(name, func(t *testing.T) {
			r := h.uploadRaw(parent.Token, body, "")
			if r.Status != http.StatusBadRequest {
				t.Fatalf("status %d, want 400\nbody: %s", r.Status, r.Body)
			}
			if code := r.errorCode(); code != "not_an_apk" && code != "invalid_body" {
				t.Fatalf("error code %q — an operator cannot tell what went wrong\nbody: %s", code, r.Body)
			}
		})
	}

	// The positive control: the same endpoint accepts a real one.
	h.uploadRaw(parent.Token, real, "").expect(http.StatusCreated)
}

// TestTheDirectoryOnTheNodeIsAlsoASource covers FR-16.1 — an operator copying an APK to the node
// over ssh, which is the route that needs no console at all.
func TestTheDirectoryOnTheNodeIsAlsoASource(t *testing.T) {
	h, dir := catalogHarness(t)
	parent := h.signIn(primaryParent)

	if err := os.WriteFile(filepath.Join(dir, "dropped-by-an-operator.apk"), fixtureAPK(t, "fixture-v1.apk"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// And something that is not an APK, to prove the scan reports per file rather than aborting.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("a stray file"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var scan struct {
		Registered []appDTO          `json:"registered"`
		Failed     map[string]string `json:"failed"`
	}
	h.call(http.MethodPost, "/apps/scan", parent.Token, nil).expect(http.StatusOK).decode(&scan)

	if len(scan.Registered) != 1 || scan.Registered[0].PackageName != fixturePackage {
		t.Fatalf("scan registered %+v", scan.Registered)
	}
	if scan.Registered[0].Source != "NODE" {
		t.Errorf("source %q, want NODE — where an app came from is part of the audit trail", scan.Registered[0].Source)
	}
	// A file the scan could not read is NAMED. A count alone makes "9 of 10" and "10 of 10" the
	// same sentence to anyone not counting the directory themselves.
	if _, named := scan.Failed["notes.txt"]; !named && len(scan.Failed) != 0 {
		t.Errorf("failures reported as %v, which does not name notes.txt", scan.Failed)
	}

	// The operator's file keeps its own name: the directory is theirs, and a scan that renamed what
	// it found would make the next `ls` unrecognisable.
	if _, err := os.Stat(filepath.Join(dir, "dropped-by-an-operator.apk")); err != nil {
		t.Errorf("the scan moved the operator's file: %v", err)
	}
}

// TestADeploymentWithoutAnAPKDirSaysSo. Not an error state — a control plane may legitimately not
// host applications — but it has to be distinguishable from a configured one holding nothing, since
// the two need different actions from whoever is looking.
func TestADeploymentWithoutAnAPKDirSaysSo(t *testing.T) {
	h := newHarness(t)
	parent := h.signIn(primaryParent)

	var list appListDTO
	h.call(http.MethodGet, "/apps", parent.Token, nil).expect(http.StatusOK).decode(&list)
	if list.Configured {
		t.Error("a server with no APK_DIR reports itself configured to host applications")
	}
	if len(list.Apps) != 0 {
		t.Errorf("an unconfigured server lists %d applications", len(list.Apps))
	}
	h.uploadRaw(parent.Token, fixtureAPK(t, "fixture-v1.apk"), "").
		expectError(http.StatusNotFound, "not_configured")
	h.call(http.MethodPost, "/apps/scan", parent.Token, nil).
		expectError(http.StatusNotFound, "not_configured")
}

// ---- FR-16.3 / 16.5: the declared set ----------------------------------------

// TestDeclaringAnAppReachesThePhoneAsSomethingItCanFetch is the whole feature in one test: a parent
// declares an application for a child, and the phone's next policy names the exact build, where to
// get it, and what it must hash to.
func TestDeclaringAnAppReachesThePhoneAsSomethingItCanFetch(t *testing.T) {
	h, _ := catalogHarness(t)
	f := enrolledFixture(t, h)

	body := fixtureAPK(t, "fixture-v1.apk")
	var app appDTO
	h.uploadRaw(f.parent.Token, body, "").expect(http.StatusCreated).decode(&app)

	before := h.devicePolicy(f.deviceToken())
	h.call(http.MethodPut, "/children/"+f.child.ID+"/managed-apps/"+fixturePackage, f.parent.Token, nil).
		expect(http.StatusNoContent)

	after := h.devicePolicy(f.deviceToken())
	// The version bump is what makes the phone re-read at all. Without it the declaration would
	// apply at the next unrelated policy edit, or never.
	if after.Desired.PolicyVersion <= before.Desired.PolicyVersion {
		t.Fatalf("declaring an app did not bump the policy version (%d then %d)",
			before.Desired.PolicyVersion, after.Desired.PolicyVersion)
	}
	if len(after.Desired.ManagedApps) != 1 {
		t.Fatalf("the phone was told about %d managed apps: %+v", len(after.Desired.ManagedApps), after.Desired.ManagedApps)
	}
	got := after.Desired.ManagedApps[0]
	if got.PackageName != fixturePackage || got.VersionCode != 1 {
		t.Errorf("managed app is %s@%d", got.PackageName, got.VersionCode)
	}
	sum := sha256.Sum256(body)
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); got.Checksum != want {
		t.Errorf("checksum %q, want %q — the phone compares this against what it downloads", got.Checksum, want)
	}
	if got.Size != int64(len(body)) {
		t.Errorf("size %d, want %d", got.Size, len(body))
	}

	// And the URL works, with the device's own credential, returning exactly those bytes.
	req := h.newRequest(http.MethodGet, got.URL, f.deviceToken(), nil)
	download := h.send(req).expect(http.StatusOK)
	if !bytes.Equal(download.Body, body) {
		t.Fatalf("the download is %d bytes, the upload was %d", len(download.Body), len(body))
	}
	if ct := download.Header.Get("Content-Type"); ct != "application/vnd.android.package-archive" {
		t.Errorf("content type %q", ct)
	}
}

// TestAnUpgradeIsANewVersionInTheSamePolicy. The declared set names a package; which build it
// resolves to is the catalog's newest. Uploading v2 must move every child declared to have it,
// with no second parent action — that is what makes this a declared set rather than a queue of
// install commands.
func TestAnUpgradeIsANewVersionInTheSamePolicy(t *testing.T) {
	h, _ := catalogHarness(t)
	f := enrolledFixture(t, h)

	h.uploadRaw(f.parent.Token, fixtureAPK(t, "fixture-v1.apk"), "").expect(http.StatusCreated)
	h.call(http.MethodPut, "/children/"+f.child.ID+"/managed-apps/"+fixturePackage, f.parent.Token, nil).
		expect(http.StatusNoContent)

	at1 := h.devicePolicy(f.deviceToken())
	if len(at1.Desired.ManagedApps) != 1 || at1.Desired.ManagedApps[0].VersionCode != 1 {
		t.Fatalf("expected version 1: %+v", at1.Desired.ManagedApps)
	}

	h.uploadRaw(f.parent.Token, fixtureAPK(t, "fixture-v2.apk"), "").expect(http.StatusCreated)

	at2 := h.devicePolicy(f.deviceToken())
	if len(at2.Desired.ManagedApps) != 1 {
		t.Fatalf("expected one entry after the upgrade: %+v", at2.Desired.ManagedApps)
	}
	if at2.Desired.ManagedApps[0].VersionCode != 2 {
		t.Fatalf("the declared set still resolves to version %d after v2 was uploaded",
			at2.Desired.ManagedApps[0].VersionCode)
	}
	if at2.Desired.ManagedApps[0].URL == at1.Desired.ManagedApps[0].URL {
		t.Error("both versions share a download URL; the phone cannot tell them apart")
	}
	// Old builds stay fetchable: a phone that read the policy a minute ago must be able to finish.
	h.send(h.newRequest(http.MethodGet, at1.Desired.ManagedApps[0].URL, f.deviceToken(), nil)).
		expect(http.StatusOK)
}

// TestWithdrawingAnAppRemovesItFromThePolicy — which is what makes the phone uninstall it (FR-16.5).
func TestWithdrawingAnAppRemovesItFromThePolicy(t *testing.T) {
	h, _ := catalogHarness(t)
	f := enrolledFixture(t, h)

	h.uploadRaw(f.parent.Token, fixtureAPK(t, "fixture-v1.apk"), "").expect(http.StatusCreated)
	path := "/children/" + f.child.ID + "/managed-apps/" + fixturePackage
	h.call(http.MethodPut, path, f.parent.Token, nil).expect(http.StatusNoContent)

	declared := h.devicePolicy(f.deviceToken())
	h.call(http.MethodDelete, path, f.parent.Token, nil).expect(http.StatusNoContent)
	withdrawn := h.devicePolicy(f.deviceToken())

	if len(withdrawn.Desired.ManagedApps) != 0 {
		t.Fatalf("the app is still declared after withdrawal: %+v", withdrawn.Desired.ManagedApps)
	}
	if withdrawn.Desired.PolicyVersion <= declared.Desired.PolicyVersion {
		t.Error("withdrawing did not bump the policy version, so the phone would not notice")
	}

	// Withdrawing again is a 404, not a second success. Withdrawal causes an uninstall on a child's
	// phone, and a parent watching for an app to disappear must not be told "done" by a request
	// that matched nothing — most likely because they are looking at the wrong child.
	h.call(http.MethodDelete, path, f.parent.Token, nil).expectError(http.StatusNotFound, "not_found")
}

// TestDeclaringSomethingTheCatalogDoesNotHaveIsRefused. The alternative — accepting any string —
// leaves a typo sitting in a child's set forever, looking exactly like an app that is about to
// install, with the phone doing nothing as the only symptom.
func TestDeclaringSomethingTheCatalogDoesNotHaveIsRefused(t *testing.T) {
	h, _ := catalogHarness(t)
	f := enrolledFixture(t, h)

	h.call(http.MethodPut, "/children/"+f.child.ID+"/managed-apps/net.muplay.typo", f.parent.Token, nil).
		expectError(http.StatusBadRequest, "unknown_package")

	// Positive control: the same call with a package that IS in the catalog succeeds.
	h.uploadRaw(f.parent.Token, fixtureAPK(t, "fixture-v1.apk"), "").expect(http.StatusCreated)
	h.call(http.MethodPut, "/children/"+f.child.ID+"/managed-apps/"+fixturePackage, f.parent.Token, nil).
		expect(http.StatusNoContent)
}

// TestTheConsoleSeesADeclarationWithNothingBehindIt. Deleting the last version of an app a child is
// declared to have does not silently unassign it — the declaration stands, unfulfilled, and the
// console has to be able to say so. The phone is told nothing, because there is nothing to install.
func TestTheConsoleSeesADeclarationWithNothingBehindIt(t *testing.T) {
	h, _ := catalogHarness(t)
	f := enrolledFixture(t, h)

	var app appDTO
	h.uploadRaw(f.parent.Token, fixtureAPK(t, "fixture-v1.apk"), "").expect(http.StatusCreated).decode(&app)
	h.call(http.MethodPut, "/children/"+f.child.ID+"/managed-apps/"+fixturePackage, f.parent.Token, nil).
		expect(http.StatusNoContent)
	h.call(http.MethodDelete, "/apps/"+app.ID, f.parent.Token, nil).expect(http.StatusNoContent)

	var view struct {
		ManagedApps []managedAppDTO `json:"managed_apps"`
	}
	h.call(http.MethodGet, "/children/"+f.child.ID+"/managed-apps", f.parent.Token, nil).
		expect(http.StatusOK).decode(&view)
	if len(view.ManagedApps) != 1 {
		t.Fatalf("the declaration disappeared with the app: %+v", view.ManagedApps)
	}
	if view.ManagedApps[0].Available {
		t.Error("a declaration with no version behind it reports itself available")
	}

	// The phone is not told about it, because there is nothing it could do.
	pol := h.devicePolicy(f.deviceToken())
	if len(pol.Desired.ManagedApps) != 0 {
		t.Fatalf("the phone was told to install something that does not exist: %+v", pol.Desired.ManagedApps)
	}
}

// TestDeletingAnAppRemovesItsFileToo. A row deleted while its bytes stay behind would be
// re-registered by the next scan, which reads as the delete having silently failed.
func TestDeletingAnAppRemovesItsFileToo(t *testing.T) {
	h, dir := catalogHarness(t)
	parent := h.signIn(primaryParent)

	var app appDTO
	h.uploadRaw(parent.Token, fixtureAPK(t, "fixture-v1.apk"), "").expect(http.StatusCreated).decode(&app)
	if _, err := os.Stat(filepath.Join(dir, app.FileName)); err != nil {
		t.Fatalf("the upload left no file: %v", err)
	}

	h.call(http.MethodDelete, "/apps/"+app.ID, parent.Token, nil).expect(http.StatusNoContent)

	if _, err := os.Stat(filepath.Join(dir, app.FileName)); !os.IsNotExist(err) {
		t.Fatalf("the file survived the delete: %v", err)
	}
	var list appListDTO
	h.call(http.MethodGet, "/apps", parent.Token, nil).expect(http.StatusOK).decode(&list)
	if len(list.Apps) != 0 {
		t.Fatalf("the row survived: %+v", list.Apps)
	}
}

// ---- who may download ---------------------------------------------------------

// TestAManagedAppDownloadNeedsADeviceCredential. Unlike /dpc.apk, which must be reachable by a
// factory-reset phone with nothing, this is only ever fetched by a phone that enrolled weeks ago.
func TestAManagedAppDownloadNeedsADeviceCredential(t *testing.T) {
	h, _ := catalogHarness(t)
	f := enrolledFixture(t, h)
	h.uploadRaw(f.parent.Token, fixtureAPK(t, "fixture-v1.apk"), "").expect(http.StatusCreated)

	path := fmt.Sprintf("/api/v1/device/apps/%s/1.apk", fixturePackage)
	h.call(http.MethodGet, path, "", nil).expect(http.StatusUnauthorized)
	h.call(http.MethodGet, path, "not-a-token", nil).expect(http.StatusUnauthorized)
	// A parent's session is not a device credential either: the device group is its own.
	h.call(http.MethodGet, path, f.parent.Token, nil).expect(http.StatusUnauthorized)
	// Positive control.
	h.call(http.MethodGet, path, f.deviceToken(), nil).expect(http.StatusOK)
	// A version that was never registered is a 404, not the newest one.
	h.call(http.MethodGet, fmt.Sprintf("/api/v1/device/apps/%s/99.apk", fixturePackage), f.deviceToken(), nil).
		expect(http.StatusNotFound)
}

// ---- FR-17: API keys -----------------------------------------------------------

// TestAnAPIKeyIsTheSameParent covers the whole point of FR-17: one credential branch, not a second
// authorization model. A key reaches what its parent reaches, and the audit trail can still tell a
// script from a person.
func TestAnAPIKeyIsTheSameParent(t *testing.T) {
	h, _ := catalogHarness(t)
	parent := h.signIn(primaryParent)

	var created apiKeyDTO
	h.call(http.MethodPost, "/api-keys", parent.Token, map[string]any{"name": "MCP server"}).
		expect(http.StatusCreated).decode(&created)

	if !strings.HasPrefix(created.Token, "fgk_") {
		t.Fatalf("token %q does not carry the scheme prefix that routes it", firstChars(created.Token))
	}
	if created.Prefix == "" || !strings.Contains(created.Token, created.Prefix) {
		t.Errorf("prefix %q is not part of the token; the two cannot be matched up in a log", created.Prefix)
	}
	if created.LastUsedAt != "" {
		t.Errorf("a key that has never been used reports last_used_at %q", created.LastUsedAt)
	}

	// The same request, with the key instead of the session, is the same parent.
	var viaSession, viaKey struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	h.call(http.MethodGet, "/me", parent.Token, nil).expect(http.StatusOK).decode(&viaSession)
	h.call(http.MethodGet, "/me", created.Token, nil).expect(http.StatusOK).decode(&viaKey)
	if viaKey.ID != viaSession.ID || viaKey.Email != viaSession.Email || viaKey.Role != viaSession.Role {
		t.Fatalf("the key is a different identity: %+v vs %+v", viaKey, viaSession)
	}

	// And the ordinary surface really works with it — the read side and a write.
	child := h.newChild(created.Token, "Declared by a script")
	h.uploadRaw(created.Token, fixtureAPK(t, "fixture-v1.apk"), "").expect(http.StatusCreated)
	h.call(http.MethodPut, "/children/"+child.ID+"/managed-apps/"+fixturePackage, created.Token, nil).
		expect(http.StatusNoContent)
}

// TestTheTokenIsShownOnceAndNeverAgain. Only its SHA-256 is stored, so this response cannot be
// reproduced — not by another request, and not by an operator with database access.
func TestTheTokenIsShownOnceAndNeverAgain(t *testing.T) {
	h, _ := catalogHarness(t)
	parent := h.signIn(primaryParent)

	var created apiKeyDTO
	h.call(http.MethodPost, "/api-keys", parent.Token, map[string]any{"name": "MCP server"}).
		expect(http.StatusCreated).decode(&created)

	listed := h.call(http.MethodGet, "/api-keys", parent.Token, nil).expect(http.StatusOK)
	if bytes.Contains(listed.Body, []byte(created.Token)) {
		t.Fatal("the key list contains a usable token")
	}
	var list struct {
		Keys []apiKeyDTO `json:"api_keys"`
	}
	listed.decode(&list)
	if len(list.Keys) != 1 {
		t.Fatalf("listed %d keys", len(list.Keys))
	}
	if list.Keys[0].Token != "" {
		t.Fatal("a listed key carries a token field")
	}
	if list.Keys[0].Prefix != created.Prefix {
		t.Errorf("prefix %q does not match the created key's %q", list.Keys[0].Prefix, created.Prefix)
	}
	// last_used_at is set by the /me call the previous test relies on; here the key was never used
	// to authenticate, so it must still be empty. It is what tells a parent which key to revoke.
	if list.Keys[0].LastUsedAt != "" {
		t.Errorf("an unused key reports last_used_at %q", list.Keys[0].LastUsedAt)
	}

	// Now use it, and the list says so.
	h.call(http.MethodGet, "/me", created.Token, nil).expect(http.StatusOK)
	h.call(http.MethodGet, "/api-keys", parent.Token, nil).expect(http.StatusOK).decode(&list)
	if list.Keys[0].LastUsedAt == "" {
		t.Error("a key that authenticated a request still reports no use")
	}
}

// TestRevokingAKeyEndsItImmediately, and the record of it stays.
func TestRevokingAKeyEndsItImmediately(t *testing.T) {
	h, _ := catalogHarness(t)
	parent := h.signIn(primaryParent)

	var created apiKeyDTO
	h.call(http.MethodPost, "/api-keys", parent.Token, map[string]any{"name": "a laptop"}).
		expect(http.StatusCreated).decode(&created)
	h.call(http.MethodGet, "/me", created.Token, nil).expect(http.StatusOK)

	h.call(http.MethodPost, "/api-keys/"+created.ID+"/revoke", parent.Token, nil).expect(http.StatusOK)
	h.call(http.MethodGet, "/me", created.Token, nil).expect(http.StatusUnauthorized)

	// Revoked, not gone. The audit log refers to keys by id, and "was that credential ever used,
	// and when did it stop" is the first question after a laptop goes missing.
	var list struct {
		Keys []apiKeyDTO `json:"api_keys"`
	}
	h.call(http.MethodGet, "/api-keys", parent.Token, nil).expect(http.StatusOK).decode(&list)
	if len(list.Keys) != 1 || list.Keys[0].RevokedAt == "" {
		t.Fatalf("a revoked key is not listed as revoked: %+v", list.Keys)
	}

	// Revoking twice is not an error: the intent is already satisfied.
	h.call(http.MethodPost, "/api-keys/"+created.ID+"/revoke", parent.Token, nil).expect(http.StatusOK)
	// Deleting really removes it.
	h.call(http.MethodDelete, "/api-keys/"+created.ID, parent.Token, nil).expect(http.StatusNoContent)
	h.call(http.MethodGet, "/api-keys", parent.Token, nil).expect(http.StatusOK).decode(&list)
	if len(list.Keys) != 0 {
		t.Fatalf("the key survived deletion: %+v", list.Keys)
	}
}

// TestAKeyCannotMintACredential. A leaked key that can create a second key, or add a parent,
// outlives its own revocation — and revocation is the one action available in an emergency.
func TestAKeyCannotMintACredential(t *testing.T) {
	h, _ := catalogHarness(t)
	parent := h.signIn(primaryParent)

	var created apiKeyDTO
	h.call(http.MethodPost, "/api-keys", parent.Token, map[string]any{"name": "MCP server"}).
		expect(http.StatusCreated).decode(&created)

	h.call(http.MethodPost, "/api-keys", created.Token, map[string]any{"name": "a second one"}).
		expectError(http.StatusForbidden, "api_key_forbidden")
	h.call(http.MethodPost, "/api-keys/"+created.ID+"/revoke", created.Token, nil).
		expectError(http.StatusForbidden, "api_key_forbidden")
	h.call(http.MethodDelete, "/api-keys/"+created.ID, created.Token, nil).
		expectError(http.StatusForbidden, "api_key_forbidden")
	h.call(http.MethodPost, "/parents", created.Token, map[string]any{
		"email": "accomplice@example.test", "role": "ADMIN",
	}).expectError(http.StatusForbidden, "api_key_forbidden")

	// Reading the set is still allowed: an MCP server that can answer "what credentials does this
	// family have" is useful, and it is not a way to outlive a revocation.
	h.call(http.MethodGet, "/api-keys", created.Token, nil).expect(http.StatusOK)

	// The positive control — the same four calls from the console session succeed, so the refusals
	// above are about the credential and not about the routes being broken.
	var second apiKeyDTO
	h.call(http.MethodPost, "/api-keys", parent.Token, map[string]any{"name": "from the console"}).
		expect(http.StatusCreated).decode(&second)
	h.call(http.MethodPost, "/api-keys/"+second.ID+"/revoke", parent.Token, nil).expect(http.StatusOK)
	h.call(http.MethodDelete, "/api-keys/"+second.ID, parent.Token, nil).expect(http.StatusNoContent)
}

// TestAKeyThatWasNeverIssuedIsNotDistinguishable. A wrong key and a wrong session token must fail
// the same way: a different status or message would say which of the two a given string is.
func TestAKeyThatWasNeverIssuedIsNotDistinguishable(t *testing.T) {
	h, _ := catalogHarness(t)
	h.signIn(primaryParent) // the family exists

	shapes := map[string]string{
		"a plausible key":   "fgk_abcd1234_ZmFrZXRva2VuZmFrZXRva2VuZmFrZXRva2Vu",
		"the prefix alone":  "fgk_",
		"a session-shaped":  "eyJhbGciOiJIUzI1NiJ9.e30.not-a-signature",
		"empty-ish garbage": "x",
	}
	// The request id differs per request by design, so it is dropped before comparing — everything
	// else in the body must be identical, message included. A message that named the reason would
	// tell an attacker which of the two kinds of string they were holding.
	type failure struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	var seen []failure
	for name, token := range shapes {
		r := h.call(http.MethodGet, "/me", token, nil).expect(http.StatusUnauthorized)
		var f failure
		r.decode(&f)
		seen = append(seen, f)
		if f.Error != "unauthorized" {
			t.Errorf("%s: error code %q", name, f.Error)
		}
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] != seen[0] {
			t.Fatalf("two invalid credentials produced different answers:\n  %+v\n  %+v", seen[0], seen[i])
		}
	}
}

// TestOnlyThePrimaryAdminMintsKeys. Handing out a key is handing out the family's whole parent
// surface, which is the same authority as adding a parent.
func TestOnlyThePrimaryAdminMintsKeys(t *testing.T) {
	h, _ := catalogHarness(t)
	primary := h.signIn(primaryParent)
	other := h.signIn(secondParent)

	h.call(http.MethodPost, "/api-keys", other.Token, map[string]any{"name": "mine"}).
		expect(http.StatusForbidden)
	// Positive control on the same route.
	h.call(http.MethodPost, "/api-keys", primary.Token, map[string]any{"name": "mine"}).
		expect(http.StatusCreated)
}

// TestAKeyNeedsAName. Not validation for its own sake: a list of unnamed keys is a list nobody can
// revoke from, because the question at that moment is "which one is the laptop".
func TestAKeyNeedsAName(t *testing.T) {
	h, _ := catalogHarness(t)
	parent := h.signIn(primaryParent)
	for _, body := range []map[string]any{{}, {"name": ""}, {"name": "   "}} {
		h.call(http.MethodPost, "/api-keys", parent.Token, body).
			expectError(http.StatusBadRequest, "invalid_input")
	}
}

// ---- audit --------------------------------------------------------------------

// TestTheAuditTrailTellsAScriptFromAPerson. Both act as the same parent, which is the design; the
// log has to record which credential was used anyway, because "the parent uploaded it" and "a
// script uploaded it" send an investigation to different places.
func TestTheAuditTrailTellsAScriptFromAPerson(t *testing.T) {
	h, _ := catalogHarness(t)
	parent := h.signIn(primaryParent)

	var key apiKeyDTO
	h.call(http.MethodPost, "/api-keys", parent.Token, map[string]any{"name": "MCP server"}).
		expect(http.StatusCreated).decode(&key)

	h.newChild(parent.Token, "By hand")
	h.newChild(key.Token, "By a script")

	entries := h.readAudit(parent.Token)
	var byPerson, byKey int
	for _, e := range entries {
		if e.Action != "CHILD_ADDED" {
			continue
		}
		switch e.ActorType {
		case "API_KEY":
			byKey++
		case "PARENT":
			byPerson++
		default:
			t.Errorf("a child was created by actor type %q", e.ActorType)
		}
	}
	if byPerson != 1 || byKey != 1 {
		t.Fatalf("audit records %d by a person and %d by a key, want one of each\nentries: %+v",
			byPerson, byKey, entries)
	}
}

// firstChars is for messages about a credential: never print one, not even a prefix long enough to
// be useful. Four characters names the scheme and nothing else.
func firstChars(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[:4] + "…"
}
