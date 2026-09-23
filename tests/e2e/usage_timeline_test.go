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
	"strconv"
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
	// The two measurements the console draws, and they are deliberately different ones. `hours` is
	// the sittings intersected with each hour of the child's day; `apps` is the cumulative day
	// total the phone has reported since 0.6.0, which is also what the daily quota is enforced
	// against. A day that predates the sittings has an empty chart and a full table.
	Hours []struct {
		Start   time.Time `json:"start"`
		Seconds int       `json:"seconds"`
	} `json:"hours"`
	Apps []struct {
		PackageName  string `json:"package_name"`
		ForegroundMs int64  `json:"foreground_ms"`
		Label        string `json:"label"`
		SystemApp    bool   `json:"system_app"`
	} `json:"apps"`
	EverReported bool `json:"ever_reported"`
}

// The day card for one named device, read out of the rendered DOM.
//
// Matched on an h2 that is EXACTLY the device name. The locations card rendered directly under it
// is titled "Where <name> was" and therefore also contains the name, so a substring match would
// bind to whichever of the two came first and every assertion below would be about the wrong card.
const timelineCardJS = `((name) => {
  const cards = Array.from(document.querySelectorAll('#view .card'));
  const card = cards.find((c) => {
    const h = c.querySelector('h2');
    return h && h.textContent === name && c.querySelector('.tl-nav');
  });
  if (!card) throw new Error('no day card for ' + name + '; the activity view holds: '
    + cards.map((c) => (c.querySelector('h2') || {}).textContent).join(' | '));
  const chart = card.querySelector('.hr-chart');
  const nav = Array.from(card.querySelectorAll('.tl-nav button'));
  return {
    text: card.textContent,
    badges: Array.from(card.querySelectorAll('.badge')).map((b) => b.textContent),
    warnings: Array.from(card.querySelectorAll('p.warn')).map((p) => p.textContent),
    aria: chart ? (chart.getAttribute('aria-label') || '') : '',
    columns: Array.from(card.querySelectorAll('.hr-col')).map((c) => {
      const fill = c.querySelector('.hr-fill');
      return {
        title: c.getAttribute('title'),
        // The inline height, not the rendered box: the column is a flex child of a fixed-height
        // row, so measuring the pixels back would report the stylesheet rather than the share of
        // the hour the server said the screen was on for.
        height: fill ? fill.style.height : '',
        empty: !!fill && fill.classList.contains('empty'),
      };
    }),
    ticks: Array.from(card.querySelectorAll('.hr-tick')).map((s) => ({
      text: s.textContent, blank: s.classList.contains('blank'),
    })),
    rows: Array.from(card.querySelectorAll('.app-bars > li')).map(
      (li) => Array.from(li.querySelectorAll('b, .num, small')).map((n) => n.textContent).join(' | ')),
    prevDisabled: nav.length > 0 ? nav[0].disabled : null,
    nextDisabled: nav.length > 1 ? nav[1].disabled : null,
  };
})(%q)`

// clickDayJS steps the card for one named device a day backwards or forwards, through the button a
// thumb would press rather than through `state`.
const clickDayJS = `((name, label) => {
  const cards = Array.from(document.querySelectorAll('#view .card'));
  const card = cards.find((c) => {
    const h = c.querySelector('h2');
    return h && h.textContent === name && c.querySelector('.tl-nav');
  });
  if (!card) throw new Error('no day card for ' + name);
  const btn = card.querySelector('.tl-nav button[aria-label="' + label + '"]');
  if (!btn) throw new Error('no ' + label + ' button on the card for ' + name);
  if (btn.disabled) throw new Error(label + ' is disabled on the card for ' + name);
  btn.click();
})(%q, %q)`

type hourColumn struct {
	Title  string `json:"title"`
	Height string `json:"height"`
	Empty  bool   `json:"empty"`
}

type axisTick struct {
	Text  string `json:"text"`
	Blank bool   `json:"blank"`
}

type timelineCard struct {
	Text         string       `json:"text"`
	Badges       []string     `json:"badges"`
	Warnings     []string     `json:"warnings"`
	Aria         string       `json:"aria"`
	Columns      []hourColumn `json:"columns"`
	Ticks        []axisTick   `json:"ticks"`
	Rows         []string     `json:"rows"`
	PrevDisabled *bool        `json:"prevDisabled"`
	NextDisabled *bool        `json:"nextDisabled"`
}

// hour returns the column whose title begins with the given local hour, and says so when there is
// none. Looked up by its label rather than by index because a day that has lost or gained an hour
// puts every hour after the transition at a different position.
func (c timelineCard) hour(t *testing.T, at func(hour, min int) time.Time, h int) hourColumn {
	t.Helper()
	prefix := at(h, 0).Format("15") + " · "
	for _, col := range c.Columns {
		if strings.HasPrefix(col.Title, prefix) {
			return col
		}
	}
	titles := make([]string, 0, len(c.Columns))
	for _, col := range c.Columns {
		titles = append(titles, col.Title)
	}
	t.Fatalf("no column for the child's %s; the chart holds %q", prefix, titles)
	return hourColumn{}
}

// share is the inline height the console wrote on one column, as a percentage of a full hour.
func (c hourColumn) share(t *testing.T) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(strings.TrimSuffix(c.Height, "%"), 64)
	if err != nil {
		t.Fatalf("column %q carries an unreadable height %q: %v", c.Title, c.Height, err)
	}
	return v
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

	// ---- the two measurements the console draws ------------------------------------------------
	//
	// The hours are not a second copy of the sittings: a sitting is stored whole, so "how long was
	// the screen on between nine and ten" is an intersection, not a grouping. 09:00–09:55 lands
	// entirely in one hour; the 20-second sliver at 14:55 joins the 50-minute sitting that began at
	// 14:00 in the same one; and every hour of the day is answered, including the empty ones,
	// because a chart that omits its quiet hours looks like one that failed to load.
	midnight := at(0, 0)
	wantHours := int(midnight.AddDate(0, 0, 1).Sub(midnight) / time.Hour)
	if len(tl.Hours) != wantHours {
		t.Fatalf("the timeline answered %d hours; %s had %d on %s", len(tl.Hours), loc, wantHours, day)
	}
	if !tl.Hours[0].Start.Equal(at(0, 0)) {
		t.Errorf("the first hour begins at %s; the child's day begins at %s", tl.Hours[0].Start, at(0, 0))
	}
	secondsAt := func(hour int) int {
		t.Helper()
		for _, h := range tl.Hours {
			if h.Start.Equal(at(hour, 0)) {
				return h.Seconds
			}
		}
		t.Fatalf("no hour beginning at %s in the %d the timeline answered", at(hour, 0), len(tl.Hours))
		return 0
	}
	for _, want := range []struct{ hour, seconds int }{
		{9, 55 * 60},     // extended by the retry
		{12, 15 * 60},    // NOT shortened by the retry
		{14, 50*60 + 20}, // the sitting plus the sliver that followed it
		{10, 0}, {13, 0}, // and the hours between them are answered as themselves
	} {
		if got := secondsAt(want.hour); got != want.seconds {
			t.Errorf("the child's %02d:00 holds %d seconds, expected %d", want.hour, got, want.seconds)
		}
	}
	var charted int
	for _, h := range tl.Hours {
		charted += h.Seconds
	}
	if sum := 55*60 + 15*60 + 50*60 + 20; charted != sum {
		t.Errorf("the hours add up to %d seconds; the sittings are %d — an intersection that "+
			"double-counts or drops a boundary is exactly what this catches", charted, sum)
	}

	// The table's rows, which come from the day totals rather than from any of the above.
	if len(tl.Apps) != 2 {
		t.Fatalf("the timeline answered %d app rows for the 2 the phone reported: %+v", len(tl.Apps), tl.Apps)
	}
	if tl.Apps[0].PackageName != pkgGame || tl.Apps[0].ForegroundMs != 95*60*1000 || tl.Apps[0].Label != "Brawl Stars" {
		t.Errorf("the longest-used app came back as %+v", tl.Apps[0])
	}
	if tl.Apps[1].PackageName != pkgVideo || tl.Apps[1].ForegroundMs != 15*60*1000 {
		t.Errorf("the second app came back as %+v", tl.Apps[1])
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
	// An empty day still has hours, and they are the day's own. Answering `[]` would make the
	// console draw nothing and read as a day that could not be loaded.
	if len(yesterday.Hours) == 0 {
		t.Error("a day with nothing in it answered no hours at all")
	}
	for _, h := range yesterday.Hours {
		if h.Seconds != 0 {
			t.Errorf("yesterday's %s holds %d seconds; everything the fixture wrote is filed today",
				h.Start, h.Seconds)
		}
	}
	if len(yesterday.Apps) != 0 {
		t.Errorf("yesterday holds %d app rows that were all reported for today: %+v",
			len(yesterday.Apps), yesterday.Apps)
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
	b.switchTab(t, "activity", "#view .hr-chart")

	// The populated card at the design width, before anything is read out of it. Three of its
	// shapes are the kind that push a page sideways — a flex row of one column per hour, an axis of
	// one span per hour under it, and a fixed-layout table — and none of them is drawn by the
	// mobile suite's fixture, whose phone has reported nothing. The day stepper's two buttons are
	// the smallest controls in the console: CI caught them at 42x44 against a 44 px floor, on a
	// page that was green on this machine.
	b.measure(t, "activity/timeline").check(t, "activity/timeline")

	var drawn timelineCard
	b.eval(fmt.Sprintf(timelineCardJS, f.device.Name), &drawn)

	// One column per hour the CHILD's day actually had — the same count the API answered above,
	// which on the two mornings the clocks move is 23 or 25 rather than 24.
	if len(drawn.Columns) != wantHours {
		t.Errorf("the chart drew %d columns; %s had %d hours on %s", len(drawn.Columns), loc, wantHours, day)
	}
	if len(drawn.Ticks) != len(drawn.Columns) {
		t.Fatalf("%d ticks under %d columns — the axis cannot be lining up with the chart",
			len(drawn.Ticks), len(drawn.Columns))
	}
	// The first column is the child's midnight. A chart drawn from the BROWSER's day would still
	// look like a plausible day, with every hour of it in the wrong place.
	if len(drawn.Columns) > 0 && !strings.HasPrefix(drawn.Columns[0].Title, at(0, 0).Format("15")+" ") {
		t.Errorf("the chart starts at %q; the child's day starts at %q",
			drawn.Columns[0].Title, at(0, 0).Format("15"))
	}

	// What each hour holds, and how tall it is drawn.
	//
	// The numbers are the sittings INTERSECTED with each hour, not filed under the hour they began
	// in: 09:00 carries 55 minutes because the retry above extended that sitting, and 14:00 carries
	// the 50-minute sitting plus the 20-second sliver that followed it.
	//
	// The height is a share of a FULL HOUR, never of the busiest hour of the day. That is the
	// difference between a chart a parent can compare across days and one on which twenty minutes
	// and four hours are both drawn full height on their own day — and it is invisible to any
	// assertion that only looks at which columns are filled.
	for _, want := range []struct {
		hour  int
		title string
		share float64
	}{
		{9, "55 min", 3300.0 / 36.0},  // 09:00–09:55
		{12, "15 min", 900.0 / 36.0},  // 12:10–12:25, the shorter retry having been refused
		{14, "50 min", 3020.0 / 36.0}, // 14:00–14:50 plus a 20-second sliver at 14:55
	} {
		col := drawn.hour(t, at, want.hour)
		if !strings.HasSuffix(col.Title, " · "+want.title) {
			t.Errorf("the %02d:00 column reads %q; the child was on the phone for %s in it",
				want.hour, col.Title, want.title)
		}
		if col.Empty {
			t.Errorf("the %02d:00 column is drawn as an empty hour: %q", want.hour, col.Title)
		}
		if got := col.share(t); got < want.share-1 || got > want.share+1 {
			t.Errorf("the %02d:00 column is %.1f%% tall; %s of an hour is %.1f%%. A column scaled "+
				"to the day's busiest hour instead of to a full one makes every day look the same.",
				want.hour, got, want.title, want.share)
		}
	}
	filled := 0
	for _, col := range drawn.Columns {
		if !col.Empty {
			filled++
		}
	}
	if filled != 3 {
		t.Errorf("%d of the day's columns are drawn as used; the child was on the phone in 3 of "+
			"them: %+v", filled, drawn.Columns)
	}

	// The axis keeps a span for every hour and labels every third one. An axis built only from the
	// hours it labels would space them evenly and put each label under the wrong column.
	labelled := 0
	for i, tick := range drawn.Ticks {
		if tick.Blank {
			if tick.Text != "" {
				t.Errorf("tick %d is hidden and still carries the label %q", i, tick.Text)
			}
			continue
		}
		labelled++
		if hour, _, ok := strings.Cut(drawn.Columns[i].Title, " · "); ok && tick.Text != hour {
			t.Errorf("tick %d reads %q over the %q column: the axis does not line up with the chart",
				i, tick.Text, hour)
		}
	}
	if want := (len(drawn.Ticks) + 2) / 3; labelled != want {
		t.Errorf("the axis carries %d labels over %d hours; every third hour is %d",
			labelled, len(drawn.Ticks), want)
	}

	// The chart is one image to a screen reader, and its label is the sentence a parent would be
	// told: how long, which day, whose hours. 7220 seconds of sittings round to 2 h 0 min.
	for _, must := range []string{"2 h 0 min", day, loc.String()} {
		if !strings.Contains(drawn.Aria, must) {
			t.Errorf("the chart's label does not carry %q: %q", must, drawn.Aria)
		}
	}

	// The table under it, which is the OTHER measurement: cumulative day totals, the same rows the
	// daily quota is enforced against, not the sittings above.
	if len(drawn.Rows) != 2 {
		t.Fatalf("the app table holds %d rows; two apps were used for a full minute: %q",
			len(drawn.Rows), drawn.Rows)
	}
	for i, want := range []struct{ label, pkg, used string }{
		{"Brawl Stars", pkgGame, "1 h 35 min"},
		{"YouTube", pkgVideo, "15 min"},
	} {
		for _, must := range []string{want.label, want.pkg, want.used} {
			if !strings.Contains(drawn.Rows[i], must) {
				t.Errorf("row %d does not carry %q: %q", i, must, drawn.Rows[i])
			}
		}
	}
	// Longest first, because the row a parent acts on is the one at the top.
	if !strings.Contains(drawn.Rows[0], "Brawl Stars") {
		t.Errorf("the table does not put the day's longest app first: %q", drawn.Rows)
	}
	// The badge is the whole day, from the same rows as the table — 95 + 15 minutes.
	if !strings.Contains(strings.Join(drawn.Badges, " "), "1 h 50 min") {
		t.Errorf("the card's badge does not carry the day's total: %q", drawn.Badges)
	}
	// The zone is on screen, because a day drawn in somebody else's hours and not saying so is the
	// one failure a parent cannot see.
	if !strings.Contains(drawn.Text, loc.String()) {
		t.Errorf("the card does not name the zone %q it was drawn in: %q", loc, drawn.Text)
	}
	if len(drawn.Warnings) != 0 {
		t.Errorf("a phone that reported four sittings and two apps drew a warning: %q", drawn.Warnings)
	}
	// Today is the last day there can be anything to see; a stepper that walks into tomorrow offers
	// a parent an empty card and no way to tell it from a phone that stopped reporting.
	if drawn.NextDisabled == nil || !*drawn.NextDisabled {
		t.Errorf("the card is showing today and its ▶ is not disabled: %v", drawn.NextDisabled)
	}

	// ---- the day the parent steps back to ------------------------------------------------------
	//
	// Yesterday, through the button rather than through `state`. This is the other half of the
	// owner's ask — the chart and the table are "per day" only if the day can be changed — and it
	// is also the third state the chart has: hours that exist and are all empty, from a phone that
	// HAS reported. That must read as a quiet day, never as the phone that has never said.
	b.eval(fmt.Sprintf(clickDayJS, f.device.Name, "Previous day"), nil)
	b.waitFor("document.body.textContent.indexOf('The screen was not on at any point on this day.') >= 0",
		20*time.Second, "the card to step back a day")

	var back timelineCard
	b.eval(fmt.Sprintf(timelineCardJS, f.device.Name), &back)
	if len(back.Columns) == 0 {
		t.Error("stepping back a day drew no hours at all; an empty day still has hours in it")
	}
	for _, col := range back.Columns {
		if !col.Empty {
			t.Errorf("yesterday holds a used hour %q; everything the fixture wrote is filed today", col.Title)
		}
	}
	if !strings.Contains(back.Text, "No app was open on this day.") {
		t.Errorf("yesterday's table does not say the day was empty: %q", back.Text)
	}
	if len(back.Warnings) != 0 {
		t.Errorf("a day with nothing in it drew a warning, which is what a phone that has never "+
			"reported is supposed to look like: %q", back.Warnings)
	}
	if back.NextDisabled == nil || *back.NextDisabled {
		t.Errorf("the ▶ is disabled on a day that is not today, so there is no way back: %v",
			back.NextDisabled)
	}

	// ---- the phone that has never said anything ------------------------------------------------

	var silent timelineCard
	b.eval(fmt.Sprintf(timelineCardJS, "The spare phone"), &silent)
	if !strings.Contains(strings.Join(silent.Warnings, "\n"), "never reported when its screen was on") {
		t.Errorf("the silent phone's card does not say it has never reported: %q\n%s",
			silent.Warnings, silent.Text)
	}
	if strings.Contains(silent.Text, "The screen was not on at any point on this day.") {
		t.Errorf("the silent phone is drawn as a child who did not switch their phone on: %q", silent.Text)
	}
	if len(silent.Columns) != 0 {
		t.Errorf("the silent phone drew %d hour columns; it has never reported one", len(silent.Columns))
	}
}
