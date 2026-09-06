package e2e

import (
	"crypto/sha256"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// download fetches an unauthenticated path off the server and returns the status and the body.
//
// Unauthenticated deliberately, and with its own client: /dpc.apk is reached by a factory-reset
// phone in the setup wizard that holds no credential of any kind, so a route that quietly acquired
// an auth requirement has to fail here.
func (h *harness) download(path string) (int, string) {
	h.t.Helper()
	resp, err := (&http.Client{Timeout: callTimeout}).Get(h.base + path)
	if err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("read %s: %v", path, err)
	}
	return resp.StatusCode, string(body)
}

// ---- FR-15: the server can replace the app on the phone ----------------------

// The metadata half of the update path, against a real server holding a real file.
//
// Three values have to agree here, and they come from three different places: the URL the phone is
// told to download from, the checksum the same server publishes in a provisioning QR, and the bytes
// that actually come back over a socket. Checking any two of them is the check that passes while
// the deployment is broken — an `apk-info` that repeated its own configuration back would satisfy
// the first two and hand a phone a checksum for a file nobody has read.
func TestAPKInfoDescribesTheFileThisServerWillHandOver(t *testing.T) {
	apkPath, apkSum := writeAPKFixture(t)
	h := newHarness(t, withSelfHostedAPK(apkPath))

	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Nils")
	device := h.newDevice(parent.Token, child.ID, "Nils' phone")
	prov, enrollToken := h.provision(parent.Token, device.ID)
	enrolled := h.enrollDevice(enrollToken, "Pixel 7a", "Android 14", nil)

	var info struct {
		URL      string `json:"url"`
		Checksum string `json:"package_checksum"`
		Size     int64  `json:"size"`
	}
	h.call(http.MethodGet, "/device/apk-info", enrolled.DeviceToken, nil).
		expect(http.StatusOK).decode(&info)

	if want := h.base + apkDownloadPath; info.URL != want {
		t.Fatalf("apk-info sends the phone to %q; this server serves the DPC at %q", info.URL, want)
	}
	if info.Checksum != apkSum {
		t.Fatalf("apk-info publishes %s for a file whose bytes hash to %s", info.Checksum, apkSum)
	}
	stat, err := os.Stat(apkPath)
	if err != nil {
		t.Fatalf("stat the fixture: %v", err)
	}
	if info.Size != stat.Size() {
		t.Fatalf("apk-info declares %d bytes for a %d byte file; a phone would refuse the download "+
			"as truncated", info.Size, stat.Size())
	}

	// The same number the provisioning QR carries. If these two ever diverge, a phone that updates
	// and a phone that enrols are verifying against different claims about the same file.
	published, _ := prov.Payload[extraPackageChecksum].(string)
	if published != info.Checksum {
		t.Fatalf("the QR publishes %s and apk-info publishes %s for the same file", published, info.Checksum)
	}

	// And the bytes themselves, fetched from the URL apk-info gave, hashed here.
	resp, err := (&http.Client{Timeout: callTimeout}).Get(info.URL)
	if err != nil {
		t.Fatalf("download from the URL apk-info published: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the download: %v", err)
	}
	sum := sha256.Sum256(body)
	if got := b64(sum[:]); got != info.Checksum {
		t.Fatalf("apk-info publishes %s, the download hashes to %s — the phone would refuse it", info.Checksum, got)
	}
}

// A control plane that hosts no DPC says so, rather than describing one it does not have.
//
// 404 and not 500: this is a configuration a deployment may legitimately have, and a device told
// "there is nothing here" stops asking. A 5xx would put the phone into a retry loop against a
// server that is working exactly as configured.
func TestAPKInfoIsNotFoundWhenTheServerHostsNoDPC(t *testing.T) {
	h := newHarness(t) // the default harness points APK_URL at an external host
	f := enrolledFixture(t, h)

	h.call(http.MethodGet, "/device/apk-info", f.deviceToken(), nil).
		expectError(http.StatusNotFound, "not_found")
}

// The parent's half: UPDATE_APP is a command the API accepts and the phone is handed.
//
// The type is checked against a closed set, so this also proves the server and the DPC agree on the
// spelling — a server deployed ahead of the fleet queues a row every phone answers "this device
// does not implement command type 'UPDATE_APP'", which is visible in the console and nowhere else.
func TestAParentCanTellThePhoneToUpdateItself(t *testing.T) {
	apkPath, _ := writeAPKFixture(t)
	h := newHarness(t, withSelfHostedAPK(apkPath))
	f := enrolledFixture(t, h)

	cmd := h.issueCommand(f.parent.Token, f.device.ID, "UPDATE_APP", nil)
	if cmd.Type != "UPDATE_APP" {
		t.Fatalf("the queued command came back as %q", cmd.Type)
	}

	var queue struct {
		Commands []commandDTO `json:"commands"`
	}
	h.call(http.MethodGet, "/device/commands", f.deviceToken(), nil).
		expect(http.StatusOK).decode(&queue)
	found := false
	for _, c := range queue.Commands {
		if c.ID == cmd.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the phone's queue does not hold the update command: %+v", queue.Commands)
	}
}

// The confirmation loop. UPDATE_APP is acknowledged *before* the install, because the install kills
// the process that would acknowledge it — so the heartbeat's version is the only thing that ever
// reports the update took effect. A console that could not show it would leave a parent with a
// command marked done and no way to tell whether the phone is running the new build.
func TestTheHeartbeatReportsWhichDPCThePhoneIsRunning(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
		"connectivity": "wifi", "app_version_name": "0.1.1", "app_version_code": 2,
	}).expect(http.StatusOK)

	view := h.deviceView(f.parent.Token, f.device.ID)
	if view.State == nil {
		t.Fatal("the device reported a heartbeat and the console has no state for it")
	}
	if view.State.AppVersionName != "0.1.1" || view.State.AppVersionCode != 2 {
		t.Fatalf("the console shows app %q build %d", view.State.AppVersionName, view.State.AppVersionCode)
	}

	// The update lands: a higher build, and the console follows it.
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
		"connectivity": "wifi", "app_version_name": "0.1.2", "app_version_code": 3,
	}).expect(http.StatusOK)
	view = h.deviceView(f.parent.Token, f.device.ID)
	if view.State.AppVersionCode != 3 {
		t.Fatalf("after an update the console still shows build %d", view.State.AppVersionCode)
	}

	// A heartbeat from an older DPC carries neither field. It must not erase what a newer one
	// reported — otherwise the version would blink out every time a phone that has not been
	// updated checks in, and "not reported" would be indistinguishable from "reported empty".
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
		"connectivity": "wifi",
	}).expect(http.StatusOK)
	view = h.deviceView(f.parent.Token, f.device.ID)
	if view.State.AppVersionName != "0.1.2" || view.State.AppVersionCode != 3 {
		t.Fatalf("a heartbeat that omitted the version erased it: %q build %d",
			view.State.AppVersionName, view.State.AppVersionCode)
	}
}

// The failure this whole feature makes routine, caught by the server rather than by the phone.
//
// Installing a new DPC is copying a file onto the node. The running process is not told, and it
// goes on publishing the checksum it computed at startup — so every QR issued since then, and every
// apk-info answer, describes bytes that are no longer there. Before this guard the only symptom was
// on the phone, mid-provisioning, as "Can't set up device".
//
// The replacement is the same length as the original on purpose. That is not a contrived case: two
// consecutive builds of this DPC were byte-for-byte the same size, so a guard that compared sizes
// would have passed the one swap this project has actually performed.
func TestAnAPKReplacedUnderTheRunningServerIsRefused(t *testing.T) {
	apkPath, _ := writeAPKFixture(t)
	h := newHarness(t, withSelfHostedAPK(apkPath))

	before, err := os.ReadFile(apkPath)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	// Served correctly first, so the refusal below is a change in behaviour and not the state this
	// server was in all along.
	if code, _ := h.download(apkDownloadPath); code != http.StatusOK {
		t.Fatalf("the server would not serve its own APK before anything was touched: %d", code)
	}

	after := []byte(strings.Repeat("x", len(before)))
	if err := os.WriteFile(apkPath, after, 0o600); err != nil {
		t.Fatalf("replace the fixture: %v", err)
	}
	code, body := h.download(apkDownloadPath)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("the server answered %d for an APK it does not publish a checksum for; a phone "+
			"would download it and refuse it mid-provisioning", code)
	}
	if !strings.Contains(body, "apk_changed") {
		t.Fatalf("the refusal reads %q; apk_unavailable would send the operator looking for a "+
			"missing file rather than a missing restart", body)
	}

	// The negative control, and the reason the check hashes rather than stats: putting the original
	// bytes back moves the modification time and changes nothing the QR describes.
	if err := os.WriteFile(apkPath, before, 0o600); err != nil {
		t.Fatalf("restore the fixture: %v", err)
	}
	if code, _ := h.download(apkDownloadPath); code != http.StatusOK {
		t.Fatalf("identical bytes are still refused (%d); the guard is measuring the file's "+
			"timestamp rather than its contents", code)
	}
}

// ---- FR-15.6: the phone updates itself, so it has to be able to ask cheaply ----

// The claim that makes an automatic update loop affordable, read off a real archive.
//
// A phone checking every fifteen minutes cannot download 13 MB to find out it is already current,
// so `apk-info` states the version it hosts and the phone compares before downloading. The number
// has to come from the *archive*, and this is the test that says so: the version is asserted
// against what the fixture's own manifest declares, and the negative control below hosts bytes that
// are not an APK and must produce no version at all rather than a zero that reads as a real answer.
func TestAPKInfoPublishesTheVersionOfTheBuildItHosts(t *testing.T) {
	apkPath := filepath.Join(t.TempDir(), "familyguard.apk")
	if err := os.WriteFile(apkPath, fixtureAPK(t, "fixture-v1.apk"), 0o600); err != nil {
		t.Fatalf("write the hosted DPC: %v", err)
	}
	h := newHarness(t, withSelfHostedAPK(apkPath))
	f := enrolledFixture(t, h)

	var info struct {
		PackageName string `json:"package_name"`
		VersionName string `json:"version_name"`
		VersionCode int64  `json:"version_code"`
		Checksum    string `json:"package_checksum"`
	}
	h.call(http.MethodGet, "/device/apk-info", f.deviceToken(), nil).
		expect(http.StatusOK).decode(&info)

	// fixture-v1.apk declares these in its own manifest; backend/internal/apk/apk_test.go reads the
	// same file and asserts the same three values against a parser that is not this server.
	if info.PackageName != fixturePackage {
		t.Fatalf("apk-info names package %q for an archive that declares %q", info.PackageName, fixturePackage)
	}
	if info.VersionCode != 1 || info.VersionName != "0.0.1" {
		t.Fatalf("apk-info publishes %q build %d; the hosted archive declares 0.0.1 build 1. A phone "+
			"comparing against this either never updates or downloads on every check",
			info.VersionName, info.VersionCode)
	}
	if info.Checksum == "" {
		t.Fatal("apk-info published a version and no checksum, so the version is not describing a file")
	}
}

// The negative control, and the reason the version is parsed rather than configured.
//
// The server hosts something that is not a readable APK. It must still serve the download path —
// the checksum and the size are computed from bytes and are true of any file — and it must publish
// **no version at all**. A zero would be a number the phone compares against, and `0 <= anything`
// is a device that decides it is current forever; a made-up version would be worse. "The server did
// not say" is a state the phone already handles by downloading and reading the archive itself.
func TestAPKInfoPublishesNoVersionForAFileItCannotParse(t *testing.T) {
	apkPath, sum := writeAPKFixture(t) // deliberately not an APK: 51 bytes of text
	h := newHarness(t, withSelfHostedAPK(apkPath))
	f := enrolledFixture(t, h)

	var info map[string]any
	h.call(http.MethodGet, "/device/apk-info", f.deviceToken(), nil).
		expect(http.StatusOK).decode(&info)

	if info["package_checksum"] != sum {
		t.Fatalf("the unparseable file still has bytes, and apk-info must describe them: %v", info["package_checksum"])
	}
	for _, key := range []string{"version_code", "version_name", "package_name"} {
		if v, ok := info[key]; ok {
			t.Fatalf("apk-info published %s=%v for a file the server could not parse; the phone "+
				"would compare against a version nobody read", key, v)
		}
	}
}

// ---- FR-15.7: a self-update that did nothing has to reach the parent ----

// The channel that did not exist when this failed for real.
//
// On 2026-09-06 an UPDATE_APP was acknowledged "downloaded and verified; installing now" and
// nothing installed: the platform had answered STATUS_PENDING_USER_ACTION and the DPC logged it to
// a log on the phone. Everything a parent could see stayed green. So the reason now rides the
// heartbeat, and this asserts the three transitions that make it trustworthy — it arrives, an older
// DPC that cannot report does not erase it, and the phone clears it by sending empty.
func TestASilentUpdateFailureReachesTheConsole(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	const reason = "Android asked for someone to confirm this install; a device owner should never be asked"
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
		"connectivity": "wifi", "app_version_code": 7, "update_error": reason,
	}).expect(http.StatusOK)

	view := h.deviceView(f.parent.Token, f.device.ID)
	if view.State == nil || view.State.UpdateError != reason {
		t.Fatalf("the console does not carry the reason the update failed: %+v", view.State)
	}
	if view.State.UpdateErrorAt == nil {
		t.Fatal("the reason arrived without a time, so a parent cannot tell a failure from an old one")
	}
	first := *view.State.UpdateErrorAt

	// A heartbeat from a DPC too old to report — the fleet this feature exists to update — carries
	// no field at all. Erasing on absent would make the console flicker clean every 60 seconds.
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
		"connectivity": "wifi",
	}).expect(http.StatusOK)
	view = h.deviceView(f.parent.Token, f.device.ID)
	if view.State.UpdateError != reason {
		t.Fatalf("a heartbeat that said nothing about updates erased the failure: %q", view.State.UpdateError)
	}

	// Re-reporting the same failure must not restamp it: the parent needs to know how long this
	// phone has been stuck, and a timestamp that follows the heartbeat always reads "just now".
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
		"connectivity": "wifi", "update_error": reason,
	}).expect(http.StatusOK)
	view = h.deviceView(f.parent.Token, f.device.ID)
	if view.State.UpdateErrorAt == nil || !view.State.UpdateErrorAt.Equal(first) {
		t.Fatalf("repeating the same reason moved its timestamp from %v to %v", first, view.State.UpdateErrorAt)
	}

	// A different reason is a different failure, and it does move the clock.
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
		"connectivity": "wifi", "update_error": "the download failed (network is unreachable)",
	}).expect(http.StatusOK)
	view = h.deviceView(f.parent.Token, f.device.ID)
	if view.State.UpdateError != "the download failed (network is unreachable)" {
		t.Fatalf("the latest failure is not the one shown: %q", view.State.UpdateError)
	}
	if view.State.UpdateErrorAt == nil || !view.State.UpdateErrorAt.After(first) {
		t.Fatalf("a new failure kept the old timestamp: %v", view.State.UpdateErrorAt)
	}

	// The phone clears it by reporting empty, which is what it does once a newer build is running.
	// This is the transition that keeps a fixed phone from showing a failure forever.
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
		"connectivity": "wifi", "app_version_code": 8, "update_error": "",
	}).expect(http.StatusOK)
	view = h.deviceView(f.parent.Token, f.device.ID)
	if view.State.UpdateError != "" {
		t.Fatalf("a phone that updated still shows %q", view.State.UpdateError)
	}
	if view.State.UpdateErrorAt != nil {
		t.Fatalf("the failure was cleared and its timestamp was not: %v", view.State.UpdateErrorAt)
	}
}

// A hostile or broken DPC does not get to put an essay in the console.
//
// The field is written by the device, rendered in the parent's browser, and stored per device. It
// is clamped at the server rather than trusted, because the DPC that reports a failure is by
// definition the one that is not behaving as designed.
func TestAnOverlongUpdateFailureIsClampedRatherThanRefused(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
		"connectivity": "wifi", "update_error": strings.Repeat("é", 5000),
	}).expect(http.StatusOK)

	view := h.deviceView(f.parent.Token, f.device.ID)
	if n := len([]rune(view.State.UpdateError)); n == 0 || n > 400 {
		t.Fatalf("stored %d runes of a 5000-rune reason; a refusal would lose the heartbeat and an "+
			"unbounded store is a device writing into the console", n)
	}
}
