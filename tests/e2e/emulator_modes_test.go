package e2e

// FR-26.1, FR-26.2 and FR-27 on a real Android device, each read from an authority rather than from
// the app's own log:
//
//   - the server's count of the phone's open streams says whether it is ACTIVE;
//   - the command queue says when a command queued for a sleeping phone was picked up;
//   - the server's Live endpoint says which positions arrived, and when.
//
// Driven by tests/android/modes.sh; run on its own it SKIPS. It forces Doze, because a phone in a
// pocket is the case the modes exist for.

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPhoneWithItsScreenOffLetsGoOfTheServerAndStillHearsOfChanges(t *testing.T) {
	d := androidDeviceFromEnv(t)
	d.dumpDeviceLogOnFailure()
	h := newHarness(t, withPublicHost(emulatorHostAlias))
	parent, _, device, _ := managedOnEmulator(t, h, d)
	// The server's own count of the phone's open streams is the authority here. The socket table is
	// not: it also holds the HTTP client's idle keep-alive connection from the last sync, which is
	// harmless (it carries nothing) and outlives the stream by minutes — measured, 2m11s.
	streamOpen := func() bool { return h.deviceView(parent.Token, device.ID).StreamOpen }
	await := func(want bool, within time.Duration, what string) time.Duration {
		t.Helper()
		start := time.Now()
		for streamOpen() != want {
			if time.Since(start) > within {
				t.Fatalf("after %v: %s never happened", within, what)
			}
			time.Sleep(2 * time.Second)
		}
		return time.Since(start)
	}
	defer d.run(30*time.Second, "shell", "dumpsys", "deviceidle", "unforce")
	defer d.run(30*time.Second, "shell", "dumpsys", "battery", "reset")
	defer d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_WAKEUP")

	// ---- ACTIVE: the screen is on and the stream is open.
	d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_WAKEUP")
	await(true, 90*time.Second, "an open stream with the screen on")

	// ---- the screen goes off: after the minute's grace, and not before it, the stream is let go.
	off := time.Now()
	d.run(30*time.Second, "shell", "input", "keyevent", "KEYCODE_SLEEP")
	time.Sleep(40 * time.Second)
	if !streamOpen() {
		t.Errorf("40 s after the screen went off the stream was already closed; the grace is a minute")
	}
	await(false, 60*time.Second, "the stream closing after the screen went off")
	t.Logf("the stream closed %v after the screen went off", time.Since(off).Round(time.Second))

	// ---- PASSIVE, in Doze: a command and Live, both queued now, reach the phone by its next poll.
	d.run(30*time.Second, "shell", "dumpsys", "battery", "unplug")
	d.run(30*time.Second, "shell", "dumpsys", "deviceidle", "force-idle")
	var feeding atomic.Bool
	feeding.Store(true)
	go func() {
		for i := 0; feeding.Load(); i++ {
			lon := 8.5400 + float64(i)*0.0001
			d.run(10*time.Second, "emu", "geo", "fix", strconv.FormatFloat(lon, 'f', 5, 64), "47.37000")
			time.Sleep(2 * time.Second)
		}
	}()
	defer feeding.Store(false)

	queued := time.Now()
	cmd := h.issueCommand(parent.Token, device.ID, "SYNC_POLICY", nil)
	h.call(http.MethodPost, "/devices/"+device.ID+"/live", parent.Token, map[string]any{"minutes": 15}).expect(http.StatusOK)
	if streamOpen() {
		t.Errorf("the phone is PASSIVE and its stream is open")
	}
	settled := awaitCommandSettled(t, h, parent.Token, device.ID, cmd.ID, 7*time.Minute)
	if settled.State != "ACKED" {
		t.Fatalf("a command queued for a sleeping phone ended %s after %v", settled.State, time.Since(queued))
	}
	took := settled.AckedAt.Sub(queued)
	t.Logf("a command queued for a sleeping phone was acknowledged after %v", took.Round(time.Second))
	if took > 6*time.Minute {
		t.Errorf("the poll is every 5 minutes and the command took %v", took)
	}

	// ---- Live: the same poll brought it; the phone holds its stream and reports every ten seconds.
	await(true, 60*time.Second, "the stream opening for Live")
	var live liveDTO
	deadline := time.Now().Add(2 * time.Minute)
	for {
		h.call(http.MethodGet, "/devices/"+device.ID+"/live", parent.Token, nil).expect(http.StatusOK).decode(&live)
		if len(live.Locations) >= 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("2 min after the poll brought Live the server holds %d positions", len(live.Locations))
		}
		time.Sleep(5 * time.Second)
	}
	t.Logf("Live: four positions %v after the session started", time.Since(queued).Round(time.Second))
	var gaps []time.Duration
	for i := 0; i+1 < len(live.Locations) && i < 3; i++ {
		gaps = append(gaps, live.Locations[i].CapturedAt.Sub(live.Locations[i+1].CapturedAt))
	}
	for _, g := range gaps {
		if g < 7*time.Second || g > 40*time.Second {
			t.Errorf("positions %v apart; Live reports about every 10 s (gaps %v)", g, gaps)
		}
	}

	var e energyDTO
	h.call(http.MethodGet, "/devices/"+device.ID+"/energy", parent.Token, nil).expect(http.StatusOK).decode(&e)
	if e.Total.Polls < 1 || e.Total.PassiveMs == nil || *e.Total.PassiveMs <= 0 || e.Total.ActiveMs == nil {
		t.Errorf("the phone woke by a poll and its energy report says polls=%d passive_ms=%v active_ms=%v",
			e.Total.Polls, e.Total.PassiveMs, e.Total.ActiveMs)
	}

	// ---- Stop: the stream carries it at once, and the phone, its screen long off, lets go again.
	h.call(http.MethodDelete, "/devices/"+device.ID+"/live", parent.Token, nil).expect(http.StatusOK)
	t.Logf("after Stop the stream closed within %v", await(false, 60*time.Second, "the stream closing after Live stopped").Round(time.Second))
}
