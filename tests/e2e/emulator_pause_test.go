package e2e

// FR-21 on a real Android device: a pause from the control plane suspends what a child can open,
// and leaves the phone able to call, and lifting it gives everything back.
//
// Everything else about the pause is proven on the server and in the two engines. This is the only
// place the platform answers: DevicePolicyManager.setPackagesSuspended applied by the DPC as device
// owner, read back from the package manager that enforces it. Driven by tests/android/pause.sh,
// which installs the DPC, the fixture app, and makes the DPC the device owner; run on its own it
// SKIPS, and the script reports NOT MEASURED if it did not run.

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fixturePackageOnDevice has no components at all, so it has no launcher entry: a pause must leave it
// alone (FR-3.12), and it is this test's negative control.
const fixturePackageOnDevice = "io.github.helios57.familyguard.fixture"

// pausableCandidates are preinstalled apps with a launcher entry on the emulator images this runs on.
// The first one the device has is the app the pause must take — a preinstalled app that is otherwise
// free by default (FR-5.10), which is exactly what a pause takes and a daily limit does not.
var pausableCandidates = []string{
	"com.google.android.deskclock", "com.android.deskclock", "com.google.android.calendar",
	"com.android.camera2",
}

// suspendedState reads the package manager's own flag for pkg: "true", "false", or "" when the
// package is not installed. dumpsys prints one `suspended=` per user; the emulator has one user.
func (d *androidDevice) suspendedState(pkg string) string {
	out, _ := d.run(30*time.Second, "shell", "dumpsys", "package", pkg)
	m := regexp.MustCompile(`suspended=(true|false)`).FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return m[1]
}

func (d *androidDevice) awaitSuspended(t *testing.T, pkg, want string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := d.suspendedState(pkg)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the package manager says suspended=%q after %s, want %q", pkg, got, within, want)
		}
		time.Sleep(2 * time.Second)
	}
}

func TestAPauseSuspendsAppsOnARealPhone(t *testing.T) {
	d := androidDeviceFromEnv(t)
	d.dumpDeviceLogOnFailure()

	h := newHarness(t, withPublicHost(emulatorHostAlias))
	deviceBase := fmt.Sprintf("http://%s:%d", emulatorHostAlias, h.port)

	parent := h.signIn(primaryParent)
	child := h.newChild(parent.Token, "Ada")
	// adb has to survive the first policy, or nothing here could read the device afterwards.
	h.patchPolicy(parent.Token, child.ID, map[string]any{"allow_debugging": true, "timezone": "Europe/Zurich"})
	device := h.newDevice(parent.Token, child.ID, "Ada's phone")
	_, enrollToken := h.provision(parent.Token, device.ID)
	d.enroll(deviceBase, enrollToken)
	// The enrolment instrumentation runs in its own process and leaves no service behind; a reboot
	// starts the DPC the way a phone in a pocket starts it, which is the state this test is about.
	// Same step as TestRemoteADBReachesARealPhonesAdbd.
	enrolled := time.Now()
	d.reboot()

	target := ""
	for _, p := range pausableCandidates {
		if d.suspendedState(p) != "" {
			target = p
			break
		}
	}
	if target == "" {
		t.Fatalf("this image has none of %v, so there is no launchable app to pause", pausableCandidates)
	}

	// The phone is managed once it has reported in and filed an inventory with both apps in it.
	deadline := time.Now().Add(3 * time.Minute)
	for {
		view := h.deviceView(parent.Token, device.ID)
		seen := view.State != nil && view.State.LastSeenAt != nil && view.State.LastSeenAt.After(enrolled)
		hasFixture := h.deviceHasApp(parent.Token, device.ID, fixturePackageOnDevice)
		hasTarget := h.deviceHasApp(parent.Token, device.ID, target)
		if seen && hasFixture && hasTarget {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 3 min: reported since enrolment=%v, fixture in inventory=%v, %s in inventory=%v",
				seen, hasFixture, target, hasTarget)
		}
		time.Sleep(2 * time.Second)
	}
	// Positive control: before the pause the app is usable. Without this, a device that suspends it
	// for another reason — a pause left over from an earlier run — would make the pause look applied.
	d.awaitSuspended(t, target, "false", 2*time.Minute)

	h.call(http.MethodPost, "/children/"+child.ID+"/pause", parent.Token, map[string]any{"paused": true}).
		expect(http.StatusOK)
	d.awaitSuspended(t, target, "true", 2*time.Minute)
	if got := d.suspendedState(fixturePackageOnDevice); got != "false" {
		t.Errorf("the fixture has no launcher entry and is suspended=%q: a pause must take only what a child can open", got)
	}

	// The phone can still call. The dialer's package differs by image, so every one this image has
	// is checked, and at least one must be present or the check proves nothing.
	dialers := 0
	for _, p := range []string{"com.android.dialer", "com.google.android.dialer"} {
		switch d.suspendedState(p) {
		case "true":
			t.Errorf("%s is suspended by the pause: the phone could not call", p)
			dialers++
		case "false":
			dialers++
		}
	}
	if dialers == 0 {
		t.Error("this image has neither dialer, so 'calls still work' was not measured")
	}

	h.call(http.MethodPost, "/children/"+child.ID+"/pause", parent.Token, map[string]any{"paused": false}).
		expect(http.StatusOK)
	d.awaitSuspended(t, target, "false", 2*time.Minute)
}

// deviceHasApp is whether the phone's own inventory lists pkg.
func (h *harness) deviceHasApp(token, deviceID, pkg string) bool {
	h.t.Helper()
	var out struct {
		Apps []struct {
			PackageName string `json:"package_name"`
		} `json:"apps"`
	}
	h.call(http.MethodGet, "/devices/"+deviceID+"/apps?include_system=1", token, nil).expect(http.StatusOK).decode(&out)
	for _, a := range out.Apps {
		if strings.EqualFold(a.PackageName, pkg) {
			return true
		}
	}
	return false
}
