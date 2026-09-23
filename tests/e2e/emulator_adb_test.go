package e2e

// FR-19 on a real Android: the parent's adb reaches the phone's own adbd through the control plane.
//
// debug_tunnel_test.go proves the relay with the test standing in for the phone. This proves the
// phone's half — the command reaching the DPC with its parameters, the DPC connecting to adbd on
// its own loopback, dialling the server's upgrade over the emulator's NAT, and splicing — by doing
// what a parent would do: `adb connect` to the port `fgctl adb` opened, and read a property back.
// The property is the phone's serial number, and it is compared with the one read over the
// emulator's ordinary adb connection, so the answer cannot have come from anywhere else.
//
// What the emulator cannot show: Wireless debugging. An emulator's adbd has no mDNS announcement
// and no TLS pairing, so this test gives the port explicitly (`--port`) and uses adbd's plain TCP
// mode. Port discovery and the device-owner switch for Wireless debugging are exercised only on the
// real phone, and IMPLEMENTATION_PLAN records them as such.
//
// Driven by tests/android/remote-adb.sh. Run bare, it skips: there is no device.

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// emulatorADBDPort is the guest port adbd listens on once it is switched to TCP. Inside the guest,
// so it does not collide with the emulator's own host-side 5554/5555 pair.
const emulatorADBDPort = 5555

func androidDeviceFromEnv(t *testing.T) *androidDevice {
	t.Helper()
	if os.Getenv("E2E_ANDROID") != "1" {
		t.Skip("no device: this test is driven by tests/android/remote-adb.sh, which installs the " +
			"DPC and makes it the device owner. E2E_ANDROID=1 is how that script says it has done so.")
	}
	d := &androidDevice{t: t, adb: os.Getenv("E2E_ANDROID_ADB"), serial: os.Getenv("E2E_ANDROID_SERIAL")}
	if d.adb == "" {
		t.Fatal("E2E_ANDROID=1 without E2E_ANDROID_ADB; nothing here can reach the device")
	}
	// Pinned now, while the emulator is the only device. Once the relay is connected adb lists TWO
	// (the emulator and 127.0.0.1:<port>), and every unqualified adb call — including the log
	// capture a failure depends on — dies on "more than one device/emulator". Measured: the first
	// failing run of this test lost its device log exactly that way.
	if d.serial == "" {
		out, err := exec.Command(d.adb, "get-serialno").Output()
		serial := strings.TrimSpace(string(out))
		if err != nil || serial == "" || serial == "unknown" {
			t.Fatalf("could not pin the emulator's serial before the relay adds a second device: %v %q", err, serial)
		}
		d.serial = serial
	}
	return d
}

// hostADB runs adb against an arbitrary serial — here, the relay's local address — rather than the
// emulator the device helper is bound to.
func hostADB(t *testing.T, adb string, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(adb, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return string(out), fmt.Errorf("adb %s did not return within %s", strings.Join(args, " "), timeout)
	}
	return string(out), err
}

func (d *androidDevice) debugNoticeShown() bool {
	out, _ := d.run(30*time.Second, "shell", "dumpsys", "notification", "--noredact")
	return strings.Contains(out, "A parent is connected for debugging")
}

func TestRemoteADBReachesARealPhonesAdbd(t *testing.T) {
	d := androidDeviceFromEnv(t)
	d.dumpDeviceLogOnFailure()

	h := newHarness(t, withPublicHost(emulatorHostAlias))
	deviceBase := fmt.Sprintf("http://%s:%d", emulatorHostAlias, h.port)

	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Ada")
	// BEFORE the phone enrols: the first policy it applies then leaves adb on, which is the state a
	// parent has to put a phone in for this feature to mean anything — and the state that keeps
	// this test's own adb alive.
	h.patchPolicy(parent.Token, child.ID, map[string]any{"allow_debugging": true})
	device := h.newDevice(parent.Token, child.ID, "Ada's phone")
	_, enrollToken := h.provision(parent.Token, device.ID)
	d.enroll(deviceBase, enrollToken)
	// The instrumentation takes the app's process with it; a reboot is what brings the service
	// back the way a provisioned phone's comes back. See TestTheServerReplacesTheDPCOnARealDevice.
	rebooted := time.Now()
	d.reboot()

	// A heartbeat from AFTER the reboot. Any last_seen at all is not enough: the one written before
	// the reboot satisfied the first version of this loop, and the test then raced a phone that had
	// not come back yet — green once by luck, red once for the same reason.
	deadline := time.Now().Add(3 * time.Minute)
	for {
		view := h.deviceView(parent.Token, device.ID)
		if view.State != nil && view.State.LastSeenAt != nil && view.State.LastSeenAt.After(rebooted) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the phone never reported in after the reboot; its service is not running")
		}
		time.Sleep(2 * time.Second)
	}

	serial := strings.TrimSpace(d.mustRun(30*time.Second, "shell", "getprop", "ro.serialno"))
	if serial == "" {
		t.Fatal("the emulator reports no ro.serialno, so nothing read through the relay could be told apart")
	}

	// adbd onto TCP. It restarts, and the emulator's own transport reconnects a moment later.
	d.mustRun(30*time.Second, "tcpip", fmt.Sprint(emulatorADBDPort))
	if out, err := d.run(2*time.Minute, "wait-for-device"); err != nil {
		t.Fatalf("the emulator did not come back after adbd moved to TCP: %v\n%s", err, out)
	}

	env := map[string]string{"FAMILYGUARD_URL": h.base, "FAMILYGUARD_TOKEN": mintKey(t, h, "e2e-adb-phone")}
	relay := startADBRelay(t, env, device.ID, "--port", fmt.Sprint(emulatorADBDPort))
	relay.awaitPhone(t, 2*time.Minute)

	out, err := hostADB(t, d.adb, 90*time.Second, "connect", relay.listen)
	if err != nil || !strings.Contains(out, "connected to") {
		t.Fatalf("adb connect %s through the relay: %v\n%s\nfgctl said: %s", relay.listen, err, out, relay.stderr.String())
	}
	t.Cleanup(func() { _, _ = hostADB(t, d.adb, 20*time.Second, "disconnect", relay.listen) })

	var through string
	for i := 0; i < 20; i++ {
		// `adb connect` returns before the handshake completes, so the first shell may meet an
		// "unauthorized" or "offline" device for a second or two.
		through, err = hostADB(t, d.adb, 60*time.Second, "-s", relay.listen, "shell", "getprop", "ro.serialno")
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("adb shell through the relay: %v\n%s\nfgctl said: %s", err, through, relay.stderr.String())
	}
	if got := strings.TrimSpace(through); got != serial {
		t.Fatalf("through the relay the phone says it is %q; over its own adb it is %q", got, serial)
	}

	// Visible on the phone while it lasts.
	if !d.debugNoticeShown() {
		t.Error("a parent is connected over adb and the phone shows no notice of it")
	}

	// The phone's acknowledgement names the session it opened.
	var cmds struct {
		Commands []commandDTO `json:"commands"`
	}
	h.call(http.MethodGet, "/devices/"+device.ID+"/commands", parent.Token, nil).expect(http.StatusOK).decode(&cmds)
	acked := false
	for _, c := range cmds.Commands {
		if c.Type == "OPEN_DEBUG_STREAM" && c.State == "ACKED" && c.Result["state"] == "connected" {
			acked = true
		}
	}
	if !acked {
		t.Errorf("no OPEN_DEBUG_STREAM acknowledged as connected: %+v", cmds.Commands)
	}

	if out, err := hostADB(t, d.adb, 20*time.Second, "disconnect", relay.listen); err != nil {
		t.Fatalf("adb disconnect: %v\n%s", err, out)
	}
	if code := relay.exitCode(t, 30*time.Second); code != 0 {
		t.Fatalf("fgctl adb --once exited %d after adb disconnected: %q", code, relay.stderr.String())
	}
	gone := time.Now().Add(20 * time.Second)
	for d.debugNoticeShown() {
		if time.Now().After(gone) {
			t.Fatal("the session is over and the phone still says a parent is connected")
		}
		time.Sleep(time.Second)
	}
}
