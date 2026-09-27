package agenda

import (
	"strings"
	"testing"
	"time"
)

var zurich, _ = time.LoadLocation("Europe/Zurich")

func ics(events ...string) []byte {
	return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" + strings.Join(events, "") + "END:VCALENDAR\r\n")
}

func event(lines ...string) string {
	return "BEGIN:VEVENT\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VEVENT\r\n"
}

func week(t *testing.T, body []byte, from string, n int, holidays ...Holiday) []Day {
	t.Helper()
	first, _ := time.ParseInLocation(time.DateOnly, from, zurich)
	occ, err := CalendarOccurrences(body, first, first.AddDate(0, 0, n), zurich)
	if err != nil {
		t.Fatalf("the calendar did not parse: %v", err)
	}
	return ExpandWithCalendar(nil, holidays, occ, first, n)
}

func TestATimedEventIsShownInTheProfilesZone(t *testing.T) {
	body := ics(event("UID:a", "DTSTAMP:20260901T000000Z", "SUMMARY:Elterngespräch", "LOCATION:Schulhaus",
		"DTSTART;TZID=America/New_York:20261007T080000", "DTEND;TZID=America/New_York:20261007T083000"))
	d := week(t, body, "2026-10-07", 1)
	if len(d[0].Items) != 1 {
		t.Fatalf("items %+v", d[0].Items)
	}
	it := d[0].Items[0]
	if it.Title != "Elterngespräch" || it.Place != "Schulhaus" || it.StartsAt != "14:00" || it.EndsAt != "14:30" || it.Source != "calendar" || it.AllDay {
		t.Errorf("the event reads %+v; want 14:00–14:30 Zurich time from 08:00 New York", it)
	}
}

func TestAnAllDayEventIsOnItsDateOnly(t *testing.T) {
	body := ics(event("UID:b", "DTSTAMP:20260901T000000Z", "SUMMARY:Schulreise", "DTSTART;VALUE=DATE:20261008", "DTEND;VALUE=DATE:20261009"))
	d := week(t, body, "2026-10-07", 3)
	if len(d[0].Items) != 0 || len(d[1].Items) != 1 || !d[1].Items[0].AllDay || len(d[2].Items) != 0 {
		t.Errorf("the all-day event lands %v / %+v / %v", d[0].Items, d[1].Items, d[2].Items)
	}
}

func TestAWeeklyEventRepeatsExceptOnItsExcludedDates(t *testing.T) {
	body := ics(event("UID:c", "DTSTAMP:20260901T000000Z", "SUMMARY:Geige",
		"DTSTART;TZID=Europe/Zurich:20261005T170000", "DTEND;TZID=Europe/Zurich:20261005T174500",
		"RRULE:FREQ=WEEKLY;BYDAY=MO", "EXDATE;TZID=Europe/Zurich:20261012T170000,20261026T170000"))
	d := week(t, body, "2026-10-05", 28)
	var on []string
	for _, day := range d {
		if len(day.Items) > 0 {
			on = append(on, day.Day)
		}
	}
	if strings.Join(on, " ") != "2026-10-05 2026-10-19" {
		t.Errorf("the weekly event is on %v; want the 5th and the 19th (the 12th and 26th excluded)", on)
	}
}

func TestAMovedOccurrenceReplacesTheOneItMoved(t *testing.T) {
	body := ics(
		event("UID:d", "DTSTAMP:20260901T000000Z", "SUMMARY:Training",
			"DTSTART;TZID=Europe/Zurich:20261006T170000", "DTEND;TZID=Europe/Zurich:20261006T180000", "RRULE:FREQ=WEEKLY;COUNT=3"),
		event("UID:d", "DTSTAMP:20260901T000000Z", "SUMMARY:Training (verschoben)", "RECURRENCE-ID;TZID=Europe/Zurich:20261013T170000",
			"DTSTART;TZID=Europe/Zurich:20261014T180000", "DTEND;TZID=Europe/Zurich:20261014T190000"),
		event("UID:e", "DTSTAMP:20260901T000000Z", "SUMMARY:Abgesagt", "STATUS:CANCELLED",
			"DTSTART;TZID=Europe/Zurich:20261015T100000", "DTEND;TZID=Europe/Zurich:20261015T110000"),
	)
	d := week(t, body, "2026-10-05", 21)
	byDay := map[string][]string{}
	for _, day := range d {
		for _, it := range day.Items {
			byDay[day.Day] = append(byDay[day.Day], it.StartsAt+" "+it.Title)
		}
	}
	if len(byDay["2026-10-13"]) != 0 || strings.Join(byDay["2026-10-14"], "") != "18:00 Training (verschoben)" ||
		len(byDay["2026-10-06"]) != 1 || len(byDay["2026-10-20"]) != 1 || len(byDay["2026-10-15"]) != 0 {
		t.Errorf("the days read %v; want the 13th moved to the 14th at 18:00, the others kept, the cancelled one gone", byDay)
	}
}

func TestAnEventAcrossMidnightIsOnBothDays(t *testing.T) {
	body := ics(event("UID:f", "DTSTAMP:20260901T000000Z", "SUMMARY:Übernachtung",
		"DTSTART;TZID=Europe/Zurich:20261009T220000", "DTEND;TZID=Europe/Zurich:20261010T020000"))
	d := week(t, body, "2026-10-09", 2)
	if len(d[0].Items) != 1 || d[0].Items[0].StartsAt != "22:00" || d[0].Items[0].EndsAt != "23:59" ||
		len(d[1].Items) != 1 || d[1].Items[0].StartsAt != "00:00" || d[1].Items[0].EndsAt != "02:00" {
		t.Errorf("across midnight: %+v / %+v", d[0].Items, d[1].Items)
	}
}

func TestAHolidayDoesNotHideACalendarEvent(t *testing.T) {
	body := ics(event("UID:g", "DTSTAMP:20260901T000000Z", "SUMMARY:Zahnarzt",
		"DTSTART;TZID=Europe/Zurich:20261007T140000", "DTEND;TZID=Europe/Zurich:20261007T143000"))
	d := week(t, body, "2026-10-07", 1, Holiday{Title: "Herbstferien", StartsOn: "2026-10-05", EndsOn: "2026-10-16"})
	if d[0].Holiday != "Herbstferien" || len(d[0].Items) != 1 {
		t.Errorf("in the holiday: %+v", d[0])
	}
}

func TestSomethingThatIsNotACalendarIsRefused(t *testing.T) {
	first, _ := time.ParseInLocation(time.DateOnly, "2026-10-07", zurich)
	if _, err := CalendarOccurrences([]byte("<html>not a calendar</html>"), first, first.AddDate(0, 0, 1), zurich); err == nil {
		t.Error("an HTML page was accepted as a calendar")
	}
}
