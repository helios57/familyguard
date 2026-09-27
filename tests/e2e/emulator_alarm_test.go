package e2e

// FR-23 on a real Android device: the alarm rings at its minute with the phone offline and dozing,
// and Stop silences it and books the next one. Driven by tests/android/alarm.sh; run on its own it
// SKIPS.
//
// Everything that can be decided without a platform is unit-tested on the phone (NextAlarm,
// AlarmBooking) and on the server. What only a device can show is the platform's part: that
// setAlarmClock is delivered in Doze with no network, that the ring service starts from it, and that
// the tone plays on the alarm stream — read from dumpsys, never from the app's own claim.

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestTheAlarmRingsOnARealPhoneOfflineAndInDoze(t *testing.T) {
	d := androidDeviceFromEnv(t)
	d.dumpDeviceLogOnFailure()
	h := newHarness(t, withPublicHost(emulatorHostAlias))
	parent, child, device, _ := managedOnEmulator(t, h, d)

	// The whole week at 06:30 so a next ring exists after this one, and today changed to a minute
	// that is 90 to 150 seconds away, in the profile's own zone.
	zurich, _ := time.LoadLocation("Europe/Zurich")
	ring := time.Now().In(zurich).Add(90 * time.Second).Truncate(time.Minute).Add(time.Minute)
	if ring.Format("2006-01-02") != time.Now().In(zurich).Format("2006-01-02") {
		t.Skip("too close to midnight in Europe/Zurich for a same-day ring; run again after 00:03")
	}
	h.call(http.MethodPut, "/children/"+child.ID+"/alarm", parent.Token,
		map[string]any{"weekdays": []string{"06:30", "06:30", "06:30", "06:30", "06:30", "06:30", "06:30"}}).expect(http.StatusOK)
	h.call(http.MethodPut, "/children/"+child.ID+"/alarm/days/"+ring.Format("2006-01-02"), parent.Token,
		map[string]any{"time": ring.Format("15:04")}).expect(http.StatusOK)

	// Booked as the platform's alarm clock — the kind Doze delivers on time.
	booked := func() bool {
		out, _ := d.run(30*time.Second, "shell", "dumpsys", "alarm")
		return strings.Contains(out, "io.github.helios57.familyguard.ALARM_FIRE")
	}
	deadline := time.Now().Add(60 * time.Second)
	for !booked() {
		if time.Now().After(deadline) {
			t.Fatal("after 60 s the phone has booked no ALARM_FIRE with the platform")
		}
		time.Sleep(2 * time.Second)
	}

	// FR-23.4: the phone reports whether the alarm may take over the lock screen — measured on the
	// phone, so null here is a phone that never sent it (0.6.22 shipped without it).
	deadline = time.Now().Add(60 * time.Second)
	for {
		if st := h.deviceView(parent.Token, device.ID).State; st != nil && st.AlarmFullScreen != nil {
			t.Logf("the phone reports alarm_full_screen=%v", *st.AlarmFullScreen)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("after 60 s the phone has not reported alarm_full_screen")
		}
		time.Sleep(2 * time.Second)
	}

	// The Heute screen says it: "Alarm: today HH:MM" (the emulator runs in English).
	// Home first, each time: the screen re-reads on resume, and an `am start` for an activity that
	// is already on top only delivers an intent to it — it would show whatever it read before the
	// phone had the new alarm.
	wantLine := "Alarm: today " + ring.Format("15:04")
	deadline = time.Now().Add(20 * time.Second)
	for {
		d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_HOME")
		d.run(30*time.Second, "shell", "am", "start", "-n", "io.github.helios57.familyguard/.recovery.RecoveryActivity")
		time.Sleep(2 * time.Second)
		d.run(30*time.Second, "shell", "uiautomator", "dump", "/sdcard/fg-ui.xml")
		ui, _ := d.run(30*time.Second, "exec-out", "cat", "/sdcard/fg-ui.xml")
		if strings.Contains(ui, wantLine) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Heute screen does not say %q", wantLine)
		}
		time.Sleep(2 * time.Second)
	}

	// Offline, screen off, unplugged, forced into Doze.
	t.Cleanup(func() {
		d.run(30*time.Second, "shell", "svc", "wifi", "enable")
		d.run(30*time.Second, "shell", "svc", "data", "enable")
		d.run(30*time.Second, "shell", "dumpsys", "deviceidle", "unforce")
		d.run(30*time.Second, "shell", "dumpsys", "battery", "reset")
	})
	d.run(30*time.Second, "shell", "svc", "wifi", "disable")
	d.run(30*time.Second, "shell", "svc", "data", "disable")
	d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_SLEEP")
	d.run(30*time.Second, "shell", "dumpsys", "battery", "unplug")
	if out, _ := d.run(30*time.Second, "shell", "dumpsys", "deviceidle", "force-idle"); !strings.Contains(strings.ToLower(out), "idle") {
		t.Fatalf("the device could not be forced into Doze: %q", out)
	}
	if time.Until(ring) < 15*time.Second {
		t.Fatalf("setting the phone up took until %s, too close to the ring at %s to prove it waited for it", time.Now().Format("15:04:05"), ring.Format("15:04:05"))
	}

	ringing := func() (service, alarmAudio bool) {
		svc, _ := d.run(30*time.Second, "shell", "dumpsys", "activity", "services", "io.github.helios57.familyguard")
		audio, _ := d.run(30*time.Second, "shell", "dumpsys", "audio")
		playing := false
		for _, line := range strings.Split(audio, "\n") {
			if strings.Contains(line, "USAGE_ALARM") && strings.Contains(line, "state:started") {
				playing = true
			}
		}
		return strings.Contains(svc, "AlarmRingService"), playing
	}
	if s, a := ringing(); s || a {
		t.Fatalf("ringing before its minute: service=%v audio=%v", s, a)
	}
	deadline = ring.Add(45 * time.Second)
	var service, audio bool
	for {
		service, audio = ringing()
		if service && audio {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("45 s after %s: ring service running=%v, alarm-usage player started=%v", ring.Format("15:04"), service, audio)
		}
		time.Sleep(3 * time.Second)
	}
	late := time.Since(ring).Round(time.Second)
	t.Logf("rang %s after its minute, offline and in forced Doze", late)
	fullScreen, _ := d.run(30*time.Second, "shell", "dumpsys", "activity", "activities")
	t.Logf("the alarm screen is %s", map[bool]string{true: "on top", false: "not on top (notification only)"}[strings.Contains(fullScreen, "ResumedActivity") && strings.Contains(fullScreen, ".alarm.AlarmActivity")])

	// Stop, as the child would: the button on the alarm screen, or the notification's action.
	if !d.tapText(t, "Stop") {
		d.run(30*time.Second, "shell", "cmd", "statusbar", "expand-notifications")
		if !d.tapText(t, "Stop") {
			t.Fatal("neither the alarm screen nor the notification offers Stop")
		}
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		service, audio = ringing()
		if !service && !audio {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("20 s after Stop: service running=%v, alarm audio=%v", service, audio)
		}
		time.Sleep(2 * time.Second)
	}
	if !booked() {
		t.Error("after Stop no next ring is booked, though the week rings every day at 06:30")
	}
}

// tapText taps the first on-screen node whose text is text (case-insensitively, since Material
// buttons draw their label in capitals), and says whether there was one.
func (d *androidDevice) tapText(t *testing.T, text string) bool {
	t.Helper()
	d.run(30*time.Second, "shell", "uiautomator", "dump", "/sdcard/fg-ui.xml")
	ui, _ := d.run(30*time.Second, "exec-out", "cat", "/sdcard/fg-ui.xml")
	re := regexp.MustCompile(`(?i)text="` + regexp.QuoteMeta(text) + `"[^>]*bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"`)
	m := re.FindStringSubmatch(ui)
	if m == nil {
		return false
	}
	var x1, y1, x2, y2 int
	fmt.Sscan(m[1], &x1)
	fmt.Sscan(m[2], &y1)
	fmt.Sscan(m[3], &x2)
	fmt.Sscan(m[4], &y2)
	d.run(30*time.Second, "shell", "input", "tap", fmt.Sprint((x1+x2)/2), fmt.Sprint((y1+y2)/2))
	return true
}
