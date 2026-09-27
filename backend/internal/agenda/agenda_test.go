package agenda

import (
	"testing"
	"time"
)

func titles(d Day) []string {
	out := []string{}
	for _, i := range d.Items {
		out = append(out, i.Title)
	}
	return out
}

func TestExpandPutsEachEntryOnItsDaysInTimeOrder(t *testing.T) {
	monday, _ := time.Parse(time.DateOnly, "2026-10-05")
	entries := []Entry{
		{ID: "t", Kind: Recurring, Title: "Training", Weekdays: 4, StartsAt: "17:00", EndsAt: "18:00"},
		{ID: "s", Kind: Recurring, Title: "Schule", Weekdays: 31, StartsAt: "08:00", EndsAt: "12:00"},
		{ID: "z", Kind: Single, Title: "Zahnarzt", Day: "2026-10-07", StartsAt: "14:00", EndsAt: "14:30"},
	}
	days := Expand(entries, nil, monday, 7)
	if got := titles(days[2]); len(got) != 3 || got[0] != "Schule" || got[1] != "Zahnarzt" || got[2] != "Training" {
		t.Errorf("Wednesday is %v", got)
	}
	if len(days[0].Items) != 1 || len(days[5].Items) != 0 || days[5].Items == nil {
		t.Errorf("Monday %v, Saturday %v (Saturday must be an empty list, not null)", titles(days[0]), days[5].Items)
	}
}

func TestAHolidaySuspendsTheRepeatingEntriesOnly(t *testing.T) {
	monday, _ := time.Parse(time.DateOnly, "2026-10-05")
	entries := []Entry{
		{Kind: Recurring, Title: "Schule", Weekdays: 31, StartsAt: "08:00", EndsAt: "12:00"},
		{Kind: Single, Title: "Zahnarzt", Day: "2026-10-07", StartsAt: "14:00", EndsAt: "14:30"},
	}
	days := Expand(entries, []Holiday{{Title: "Herbstferien", StartsOn: "2026-10-06", EndsOn: "2026-10-07"}}, monday, 4)
	if days[0].Holiday != "" || len(days[0].Items) != 1 {
		t.Errorf("the day before the holiday: %+v", days[0])
	}
	if days[1].Holiday != "Herbstferien" || len(days[1].Items) != 0 {
		t.Errorf("the holiday's first day: %+v", days[1])
	}
	if got := titles(days[2]); len(got) != 1 || got[0] != "Zahnarzt" {
		t.Errorf("the holiday's last day holds %v; the single entry stays", got)
	}
	if days[3].Holiday != "" || len(days[3].Items) != 1 {
		t.Errorf("the day after the holiday: %+v", days[3])
	}
}
