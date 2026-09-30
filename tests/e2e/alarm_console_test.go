package e2e

// FR-23 in a real browser and in the device's own report: an admin sets the alarm clock on the Rules
// tab, changes one date, and a phone that may not take over the screen for it says so on its card.

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTheAdminSetsTheAlarmInTheConsole(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	h.patchPolicy(primary.Token, child.ID, map[string]any{"timezone": "Europe/Zurich"})
	b := signInBrowser(t, h, primaryParent)
	b.eval(`document.querySelector('.tab[data-tab="rules"]').click()`, nil)
	b.waitFor(`!!document.querySelector('#view .alarm-card button[data-alarm="day-toggle"]')`, 15*time.Second, "the alarm card")
	b.measure(t, "rules/alarm").check(t, "rules/alarm")

	// Monday to Friday at 06:30 — five day chips and one time, the common case — then Friday alone
	// moved to 07:00, which is what "pro Tag verschieden" is for. The weekend stays off.
	b.eval(`(() => {
	  const q = (sel) => document.querySelector('#view .alarm-card ' + sel);
	  [0, 1, 2, 3, 4].forEach((i) => q('button[data-alarm="day-toggle"][data-day="' + i + '"]').click());
	  const all = q('input[data-alarm="time-all"]'); all.value = '06:30'; all.dispatchEvent(new Event('input', { bubbles: true }));
	  q('button[data-alarm="per-day"]').click();
	  const fri = q('[data-day="4"] input[data-alarm="time"]'); fri.value = '07:00'; fri.dispatchEvent(new Event('input', { bubbles: true }));
	})()`, nil)
	// One bar saves every unsaved editor on the page; it appears only once something changed.
	b.waitFor(`!document.getElementById('savebar').hidden`, 5*time.Second, "the save bar")
	b.measure(t, "rules/alarm per day").check(t, "rules/alarm per day")
	b.eval(`document.querySelector('#savebar button[data-save="all"]').click()`, nil)
	want := []string{"06:30", "06:30", "06:30", "06:30", "07:00", "", ""}
	deadline := time.Now().Add(10 * time.Second)
	for !slices.Equal(h.getAlarm(primary.Token, child.ID).Weekdays, want) {
		if time.Now().After(deadline) {
			t.Fatalf("Save alarm stored %v, want %v\n%s", h.getAlarm(primary.Token, child.ID).Weekdays, want, b.pageErrorReport())
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Tomorrow: no alarm.
	b.waitFor(`!!document.querySelector('#view .alarm-card button[data-alarm="day-off"]')`, 10*time.Second, "the date change controls")
	b.eval(`document.querySelector('#view .alarm-card button[data-alarm="day-off"]').click()`, nil)
	tomorrow := zurichDay(1)
	deadline = time.Now().Add(10 * time.Second)
	for {
		o := h.getAlarm(primary.Token, child.ID).Overrides
		if len(o) == 1 && o[0].Day == tomorrow && o[0].Time == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("'no alarm tomorrow' stored %+v; want %s off", o, tomorrow)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The change is listed, and removing it returns the day to the week.
	b.waitFor(`!!document.querySelector('#view .alarm-card button[data-alarm="day-clear"][data-date="`+tomorrow+`"]')`, 10*time.Second, "the listed change")
	var listed string
	b.eval(`document.querySelector('#view .alarm-card .alarm-changes').textContent`, &listed)
	if !strings.Contains(listed, "kein Wecker") {
		t.Errorf("the listed change does not say there is no alarm that day: %q", listed)
	}
	b.eval(`document.querySelector('#view .alarm-card button[data-alarm="day-clear"][data-date="`+tomorrow+`"]').click()`, nil)
	deadline = time.Now().Add(10 * time.Second)
	for len(h.getAlarm(primary.Token, child.ID).Overrides) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("Remove left %+v", h.getAlarm(primary.Token, child.ID).Overrides)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(b.pageErrors) != 0 {
		t.Errorf("the alarm card made the page complain: %s", b.pageErrorReport())
	}
}

func TestAPhoneThatCannotTakeOverTheScreenForTheAlarmSaysSo(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	state := func() deviceStateDTO {
		t.Helper()
		v := h.deviceView(f.parent.Token, f.device.ID)
		if v.State == nil {
			t.Fatal("the device has no state row")
		}
		return *v.State
	}
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{"connectivity": "wifi"}).expect(http.StatusOK)
	if st := state(); st.AlarmFullScreen != nil {
		t.Fatalf("a heartbeat that said nothing was recorded as alarm_full_screen=%v", *st.AlarmFullScreen)
	}
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
		"connectivity": "wifi", "alarm_full_screen": false,
	}).expect(http.StatusOK)
	if st := state(); st.AlarmFullScreen == nil || *st.AlarmFullScreen {
		t.Fatalf("the phone reported no full-screen alarm and the server holds %v", st.AlarmFullScreen)
	}
	// An older build heartbeating in between does not erase it.
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{"connectivity": "wifi"}).expect(http.StatusOK)
	if st := state(); st.AlarmFullScreen == nil || *st.AlarmFullScreen {
		t.Fatalf("a heartbeat without the field cleared it: %v", st.AlarmFullScreen)
	}

	b := signInBrowser(t, h, primaryParent)
	openPhoneSheet(t, b, f.device.Name)
	b.waitFor(`Array.from(document.querySelectorAll('#sheet .badge')).some((n) => n.textContent === 'Wecker: nur Mitteilung')`,
		15*time.Second, "the phone sheet to say the alarm cannot take over the screen")

	// And it goes away when the phone reports it may.
	h.call(http.MethodPost, "/device/heartbeat", f.deviceToken(), map[string]any{
		"connectivity": "wifi", "alarm_full_screen": true,
	}).expect(http.StatusOK)
	// The open sheet is redrawn from the server's next answer, without being closed and reopened.
	b.waitFor(`document.getElementById('sheet').open && !Array.from(document.querySelectorAll('#sheet .badge')).some((n) => n.textContent === 'Wecker: nur Mitteilung')`,
		15*time.Second, "the badge to go once the phone may take over the screen")
}
