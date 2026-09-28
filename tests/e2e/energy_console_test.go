package e2e

// FR-26.5 in the console: the Activity tab says what FamilyGuard spends on each phone, and says "not
// reported yet" for a phone that has not reported — never a row of zeros, which would be a claim.

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTheConsoleShowsWhatFamilyGuardSpends(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	for i, cpu := range []int64{1000, 4600} {
		if i > 0 {
			time.Sleep(1100 * time.Millisecond)
		}
		h.call(http.MethodPost, "/device/heartbeat", f.enroll.DeviceToken, map[string]any{
			"connectivity": "wifi", "battery_level": 80 - i, "charging": false,
			"energy": map[string]any{"since": since, "cpu_ms": cpu, "rx_bytes": 0, "tx_bytes": 0,
				"stream_opens": 1 + 2*i, "events": 0, "polls": 0, "pushes": 0, "other_syncs": 0,
				"active_ms": 1000 * i, "passive_ms": 3000 * i, "route_full_ms": 2000 * i, "route_dns_ms": 2000 * i},
		}).expect(http.StatusOK)
	}
	quiet := h.newDevice(f.parent.Token, f.child.ID, "The spare phone")
	_, quietToken := h.provision(f.parent.Token, quiet.ID)
	h.enrollDevice(quietToken, "Galaxy A14", "Android 14", nil)

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")
	h.issuer.setNextLogin(primaryParent)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#view .card').length > 0", 15*time.Second, "the home view")
	b.switchTab(t, "activity", "#view [data-energy]")
	b.measure(t, "activity/energy").check(t, "activity/energy")

	var cards []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	b.eval(`[...document.querySelectorAll('#view [data-energy]')].map((c) => ({kind: c.dataset.energy, text: c.textContent}))`, &cards)
	var reported, none string
	for _, c := range cards {
		switch c.Kind {
		case "reported":
			reported = c.Text
		case "none":
			none = c.Text
		}
	}
	for _, want := range []string{f.device.Name, "per unplugged hour", "FamilyGuard CPU", "s per hour", "wake-ups per hour",
		"Resting (screen off) 75 % of the time", "Ad filter DNS only 50 % of its time"} {
		if !strings.Contains(reported, want) {
			t.Errorf("the reporting phone's energy card lacks %q: %q", want, reported)
		}
	}
	if !strings.Contains(none, "The spare phone") || !strings.Contains(none, "Not reported yet") {
		t.Errorf("the silent phone's card should say it has not reported: %q", none)
	}
}
