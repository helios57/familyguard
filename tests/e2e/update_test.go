package e2e

import (
	"crypto/sha256"
	"io"
	"net/http"
	"os"
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
