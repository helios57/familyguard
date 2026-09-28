package e2e

// FR-26.3 on a real Android device against the real FCM: the phone registers with the Firebase
// project the server names, the server holds its address, and a command queued for the phone while it
// rests in Doze — no stream open, its next poll minutes away — is acknowledged within about a minute,
// which only a push can do. Each claim is read from an authority: the server's device view, its count
// of open streams, and the command queue.
//
// Driven by tests/android/push.sh, which needs a Firebase project's service-account key and its
// Android app's three values in the environment; run on its own, or without them, it SKIPS.

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestARestingPhoneIsWokenByARealPush(t *testing.T) {
	d := androidDeviceFromEnv(t)
	vars := []string{"FCM_CREDENTIALS", "FCM_APPLICATION_ID", "FCM_API_KEY", "FCM_SENDER_ID"}
	var opts []harnessOption
	for _, v := range vars {
		if os.Getenv(v) == "" {
			t.Skipf("NOT MEASURED: %s is not set; a real push needs a Firebase project", v)
		}
		opts = append(opts, withEnv(v, os.Getenv(v)))
	}
	d.dumpDeviceLogOnFailure()
	h := newHarness(t, append(opts, withPublicHost(emulatorHostAlias))...)
	parent, _, device, _ := managedOnEmulator(t, h, d)
	view := func() deviceViewDTO { return h.deviceView(parent.Token, device.ID) }
	defer d.run(30*time.Second, "shell", "dumpsys", "deviceidle", "unforce")
	defer d.run(30*time.Second, "shell", "dumpsys", "battery", "reset")
	defer d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_WAKEUP")

	// ---- the phone registers and the server holds its address.
	start := time.Now()
	for view().State == nil || !view().State.PushRegistered {
		if time.Since(start) > 3*time.Minute {
			t.Fatalf("3 min after enrolling the server holds no push address for the phone")
		}
		time.Sleep(3 * time.Second)
	}
	t.Logf("the phone registered for push %v after enrolling", time.Since(start).Round(time.Second))

	// ---- it rests: the screen off, the stream let go, the phone in Doze.
	d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_SLEEP")
	start = time.Now()
	for view().StreamOpen {
		if time.Since(start) > 2*time.Minute {
			t.Fatalf("2 min after the screen went off the phone still holds its stream")
		}
		time.Sleep(2 * time.Second)
	}
	d.run(30*time.Second, "shell", "dumpsys", "battery", "unplug")
	d.run(30*time.Second, "shell", "dumpsys", "deviceidle", "force-idle")
	time.Sleep(10 * time.Second) // settled in Doze, and past the server's 10 s between two pushes

	// ---- a command for the resting phone arrives by the push, well before any poll could bring it.
	queued := time.Now()
	cmd := h.issueCommand(parent.Token, device.ID, "SYNC_POLICY", nil)
	settled := awaitCommandSettled(t, h, parent.Token, device.ID, cmd.ID, 4*time.Minute)
	if settled.State != "ACKED" {
		t.Fatalf("a command queued for a resting phone ended %s after %v", settled.State, time.Since(queued))
	}
	took := settled.AckedAt.Sub(queued)
	t.Logf("a command queued for a phone in Doze was acknowledged after %v", took.Round(time.Second))
	if took > 90*time.Second {
		t.Errorf("the command took %v; a push arrives in seconds, and only a poll takes minutes", took)
	}

	// ---- and the phone, having seen a push arrive, stretches its safety poll to 30 minutes. The
	// phone's own log is the only place that decision is visible.
	out, _ := d.run(30*time.Second, "logcat", "-d", "-s", "FamilyGuard/Connection:I")
	if !strings.Contains(out, "next poll in 30 min") {
		t.Errorf("the phone never booked a 30-minute poll after its push arrived")
	}

	var e energyDTO
	h.call("GET", "/devices/"+device.ID+"/energy", parent.Token, nil).expect(200).decode(&e)
	if e.Total.Pushes < 1 {
		t.Errorf("the phone was woken by a push and its energy report counts %d pushes", e.Total.Pushes)
	}
}
