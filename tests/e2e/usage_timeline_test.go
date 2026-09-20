package e2e

// FR-3.7: what ran WHEN, not only how long — through the API and then through a real browser.
//
// The owner's ask was two sentences apart and is two different questions: "i dont see which app was
// used how long today" was answered by the totals list, and "I want the recording and precise
// tracking what app was running when for how long" is not. "Ninety minutes of YouTube" is the same
// number whether it was one afternoon or a phone picked up thirty times, and a parent who wants to
// know what their child was doing at nine o'clock cannot read it off a total at all.
//
// Three properties are asserted here that no unit test on either side can reach:
//
//   - the interval survives the whole path — phone JSON, `timestamptz`, the overlap query, JSON
//     again, and the browser's own date parsing — without being moved by an hour;
//   - the day and the clock are the CHILD's, so the card is right for a parent in another
//     timezone. This test deliberately picks a zone in which "now" is mid-afternoon and which is
//     almost never the machine's own, so a console that quietly used the browser's zone would draw
//     different hours;
//   - a re-delivered sitting can lengthen the stored one and can never shorten it, which is what
//     makes the phone's retry safe.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// afternoonZone returns an IANA zone in which the current instant is comfortably mid-afternoon.
//
// Anchoring the fixture to a fixed zone would make it fail for part of every day: sittings placed
// "three hours ago" fall on yesterday when the suite runs just after local midnight, and sittings
// placed at a fixed local hour are in the future when it runs before it. Neither is a defect in the
// product, and a suite that is red for two hours a night is one whose reds stop being read.
//
// Choosing the zone from the clock removes the whole class, and it strengthens the test rather than
// weakening it: the zone is almost never the one the browser and the machine are in, so every hour
// this test reads back had to come from the child's policy.
func afternoonZone(t *testing.T) *time.Location {
	t.Helper()
	// One per UTC offset, so some member of the list is always in the afternoon.
	candidates := []string{
		"Pacific/Midway", "Pacific/Honolulu", "America/Anchorage", "America/Los_Angeles",
		"America/Denver", "America/Chicago", "America/New_York", "America/Halifax",
		"America/Sao_Paulo", "Atlantic/Azores", "UTC", "Europe/Zurich", "Europe/Athens",
		"Europe/Moscow", "Asia/Dubai", "Asia/Karachi", "Asia/Dhaka", "Asia/Bangkok",
		"Asia/Shanghai", "Asia/Tokyo", "Australia/Brisbane", "Pacific/Guadalcanal",
		"Pacific/Auckland",
	}
	now := time.Now()
	for _, name := range candidates {
		loc, err := time.LoadLocation(name)
		if err != nil {
			continue
		}
		// 16:00–19:59 local. Everything the fixture writes sits between 09:00 and 15:00 local, so
		// the whole day is in the past and none of it is near either midnight.
		if h := now.In(loc).Hour(); h >= 16 && h <= 19 {
			return loc
		}
	}
	t.Fatalf("no candidate zone is mid-afternoon at %s; the list has a gap", now.UTC())
	return nil
}

type timelineDTO struct {
	Day      string `json:"day"`
	Timezone string `json:"timezone"`
	From     string `json:"from"`
	To       string `json:"to"`
	Sessions []struct {
		PackageName string    `json:"package_name"`
		StartedAt   time.Time `json:"started_at"`
		EndedAt     time.Time `json:"ended_at"`
		Seconds     int       `json:"seconds"`
		Label       string    `json:"label"`
		SystemApp   bool      `json:"system_app"`
	} `json:"sessions"`
	EverReported bool `json:"ever_reported"`
}

// The timeline card for one named device, read out of the rendered DOM.
const timelineCardJS = `((name) => {
  const cards = Array.from(document.querySelectorAll('#view .card'));
  const card = cards.find((c) => {
    const h = c.querySelector('h2');
    return h && h.textContent.indexOf('What ran when') >= 0 && c.textContent.indexOf(name) >= 0;
  });
  if (!card) throw new Error('no timeline card for ' + name + '; the activity view holds: '
    + cards.map((c) => (c.querySelector('h2') || {}).textContent).join(' | '));
  return {
    text: card.textContent,
    badges: Array.from(card.querySelectorAll('.badge')).map((b) => b.textContent),
    warnings: Array.from(card.querySelectorAll('p.warn')).map((p) => p.textContent),
    blocks: Array.from(card.querySelectorAll('.tl-block')).map((b) => b.getAttribute('title')),
    rows: Array.from(card.querySelectorAll('.list li')).map((li) => li.textContent),
    ticks: Array.from(card.querySelectorAll('.tl-tick')).map((s) => s.textContent),
  };
})(%q)`

type timelineCard struct {
	Text     string   `json:"text"`
	Badges   []string `json:"badges"`
	Warnings []string `json:"warnings"`
	Blocks   []string `json:"blocks"`
	Rows     []string `json:"rows"`
	Ticks    []string `json:"ticks"`
}

func TestTheConsoleDrawsWhatRanWhen(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)
	loc := afternoonZone(t)

	// The child lives here; the browser and this process almost certainly do not.
	h.patchPolicy(f.parent.Token, f.child.ID, map[string]any{"timezone": loc.String()})

	now := time.Now().In(loc)
	day := now.Format("2006-01-02")
	at := func(hour, min int) time.Time {
		return time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, loc)
	}
	const pkgVideo = "com.example.video"

	h.call(http.MethodPost, "/device/inventory", f.deviceToken(), map[string]any{
		"apps": []map[string]any{
			{"package_name": pkgGame, "label": "Brawl Stars"},
			{"package_name": pkgVideo, "label": "YouTube"},
		},
	}).expect(http.StatusOK)

	session := func(pkg string, from, to time.Time) map[string]any {
		return map[string]any{
			"package_name": pkg,
			"started_at":   from.Format(time.RFC3339),
			"ended_at":     to.Format(time.RFC3339),
		}
	}
	report := func(sessions []map[string]any) map[string]any {
		var out struct {
			Day      string `json:"day"`
			Minutes  int    `json:"minutes"`
			Sessions int    `json:"sessions"`
		}
		h.call(http.MethodPost, "/device/usage", f.deviceToken(), map[string]any{
			"day":      day,
			"samples":  map[string]int64{pkgGame: 95 * 60 * 1000, pkgVideo: 15 * 60 * 1000},
			"sessions": sessions,
		}).expect(http.StatusOK).decode(&out)
		return map[string]any{"day": out.Day, "minutes": out.Minutes, "sessions": out.Sessions}
	}

	first := report([]map[string]any{
		session(pkgGame, at(9, 0), at(9, 45)),
		session(pkgVideo, at(12, 10), at(12, 25)),
		session(pkgGame, at(14, 0), at(14, 50)),
		// A sliver: the phone keeps anything over a second, and the console draws it in the strip
		// but must not put a "0 min" row in the list.
		session(pkgVideo, at(14, 55), at(14, 55).Add(20*time.Second)),
	})
	if first["sessions"] != 4 {
		t.Fatalf("the server stored %v of the 4 sittings reported", first["sessions"])
	}

	timeline := func(query string) timelineDTO {
		t.Helper()
		var out timelineDTO
		h.call(http.MethodGet, "/devices/"+f.device.ID+"/usage/timeline"+query, f.parent.Token, nil).
			expect(http.StatusOK).decode(&out)
		return out
	}

	tl := timeline("")
	if tl.Day != day || tl.Timezone != loc.String() {
		t.Fatalf("the timeline answered day=%q zone=%q; the child's day is %q in %q",
			tl.Day, tl.Timezone, day, loc)
	}
	if len(tl.Sessions) != 4 {
		t.Fatalf("the timeline holds %d sittings, expected 4: %+v", len(tl.Sessions), tl.Sessions)
	}
	// Ordered by start, and each one carrying the instants the phone sent rather than a day plus an
	// offset somebody recomputed.
	if !tl.Sessions[0].StartedAt.Equal(at(9, 0)) || !tl.Sessions[0].EndedAt.Equal(at(9, 45)) {
		t.Errorf("the first sitting came back as %s–%s, sent as %s–%s",
			tl.Sessions[0].StartedAt, tl.Sessions[0].EndedAt, at(9, 0), at(9, 45))
	}
	if tl.Sessions[0].Label != "Brawl Stars" || tl.Sessions[1].Label != "YouTube" {
		t.Errorf("the sittings are not named from the phone's inventory: %q, %q",
			tl.Sessions[0].Label, tl.Sessions[1].Label)
	}
	if tl.Sessions[0].Seconds != 45*60 {
		t.Errorf("a 45-minute sitting came back as %d seconds", tl.Sessions[0].Seconds)
	}
	if !tl.EverReported {
		t.Error("the device has reported four sittings and the server says it never has")
	}

	// The retry. The phone re-sends what it has not had acknowledged, so the same sitting arrives
	// twice — and the second copy may be LONGER, because the app was still open when the first went
	// out. It must merge, never duplicate, and never shorten.
	again := report([]map[string]any{
		session(pkgGame, at(9, 0), at(9, 55)),     // five minutes longer
		session(pkgVideo, at(12, 10), at(12, 20)), // five minutes shorter
	})
	if again["sessions"] != 2 {
		t.Fatalf("the retry stored %v rows for 2 sittings", again["sessions"])
	}
	tl = timeline("")
	if len(tl.Sessions) != 4 {
		t.Fatalf("a retry of two sittings turned 4 rows into %d", len(tl.Sessions))
	}
	if tl.Sessions[0].Seconds != 55*60 {
		t.Errorf("the longer report did not extend the stored sitting: %d seconds", tl.Sessions[0].Seconds)
	}
	if tl.Sessions[1].Seconds != 15*60 {
		t.Errorf("a shorter retry SHORTENED a stored sitting to %d seconds; a sitting that has "+
			"already been observed cannot un-happen", tl.Sessions[1].Seconds)
	}

	// A day with nothing in it is not the same statement as a device that never reports, and the
	// console has to be able to tell them apart — so the endpoint answers both on every request.
	yesterday := timeline("?day=" + now.AddDate(0, 0, -1).Format("2006-01-02"))
	if len(yesterday.Sessions) != 0 {
		t.Errorf("yesterday holds %d sittings that were all filed for today", len(yesterday.Sessions))
	}
	if !yesterday.EverReported {
		t.Error("an empty day reported the device as never having sent a sitting")
	}

	// A second phone for the same child, enrolled and silent. Its card is the one that must say
	// "never reported" rather than "nothing was opened".
	quiet := h.newDevice(f.parent.Token, f.child.ID, "The spare phone")
	_, quietToken := h.provision(f.parent.Token, quiet.ID)
	h.enrollDevice(quietToken, "Galaxy A14", "Android 14", nil)

	// ---- and now the part no API assertion reaches ----------------------------------------------

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")
	h.issuer.setNextLogin(primaryParent)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#view .card').length > 0", 15*time.Second, "the home view")
	b.switchTab(t, "activity", "#view .tl-track")

	// The populated card at the design width, before anything is read out of it. The strip is the
	// console's only absolutely-positioned content, and a block placed at 99.8% with a minimum
	// width is exactly the shape that pushes a page sideways — which the mobile suite cannot see,
	// because the fixture it renders has no sittings and therefore no strip.
	b.measure(t, "activity/timeline").check(t, "activity/timeline")

	var drawn timelineCard
	b.eval(fmt.Sprintf(timelineCardJS, f.device.Name), &drawn)

	if len(drawn.Blocks) != 4 {
		t.Errorf("the strip drew %d blocks for 4 sittings: %q", len(drawn.Blocks), drawn.Blocks)
	}
	// The hours are the CHILD's. This is the assertion that a console using the browser's own
	// timezone cannot satisfy, because the browser is not in `loc`.
	// 09:55, not 09:45: the retry above extended this sitting, and the card must be drawn from
	// what the server holds rather than from the first report it received.
	want := at(9, 0).Format("15:04") + "\u2013" + at(9, 55).Format("15:04")
	if !strings.Contains(strings.Join(drawn.Blocks, "\n"), want) {
		t.Errorf("no block is titled %q; the strip holds %q", want, drawn.Blocks)
	}
	rows := strings.Join(drawn.Rows, "\n")
	for _, must := range []string{"Brawl Stars", "YouTube", want} {
		if !strings.Contains(rows, must) {
			t.Errorf("the list does not carry %q; it holds %q", must, drawn.Rows)
		}
	}
	// The sliver is drawn and not listed: three rows, four blocks, and a line saying so.
	if len(drawn.Rows) != 3 {
		t.Errorf("the list holds %d rows; 3 sittings ran for at least a minute: %q", len(drawn.Rows), drawn.Rows)
	}
	if !strings.Contains(drawn.Text, "1 sitting(s) under a minute") {
		t.Errorf("the card does not account for the sub-minute sitting it drew: %q", drawn.Text)
	}
	if !strings.Contains(strings.Join(drawn.Badges, " "), "4 sitting(s)") {
		t.Errorf("the card's badge does not count the sittings: %q", drawn.Badges)
	}
	// The day and the zone are on screen, because a timeline with no date on it is a picture of an
	// unknown day.
	if !strings.Contains(drawn.Text, day) || !strings.Contains(drawn.Text, loc.String()) {
		t.Errorf("the card names neither the day %q nor the zone %q: %q", day, loc, drawn.Text)
	}
	// The axis spans the CHILD's day. A strip labelled from the browser's midnight would place
	// every hour of it somewhere else, and the blocks would still look plausible.
	if len(drawn.Ticks) == 0 || drawn.Ticks[0] != at(0, 0).Format("15:04") {
		t.Errorf("the axis starts at %q; the child's day starts at %q",
			drawn.Ticks, at(0, 0).Format("15:04"))
	}
	if len(drawn.Warnings) != 0 {
		t.Errorf("a phone that reported four sittings drew a warning: %q", drawn.Warnings)
	}

	var silent timelineCard
	b.eval(fmt.Sprintf(timelineCardJS, "The spare phone"), &silent)
	if !strings.Contains(strings.Join(silent.Warnings, "\n"), "never reported a sitting") {
		t.Errorf("the silent phone's card does not say it has never reported one: %q\n%s",
			silent.Warnings, silent.Text)
	}
	if strings.Contains(silent.Text, "Nothing was opened") {
		t.Errorf("the silent phone is drawn as a child who did not use it: %q", silent.Text)
	}
}
