// Package agenda expands a profile's agenda entries and the family's holidays into days (FR-24.3).
// Pure: no clock, no store. The server hands it the entries, the holidays and the dates.
package agenda

import (
	"sort"
	"time"
)

// Entry kinds.
const (
	Recurring = "RECURRING"
	Single    = "SINGLE"
)

// Entry is one agenda entry: repeating on weekdays (bit 0 Monday … bit 6 Sunday) or on one date.
type Entry struct {
	ID       string
	Kind     string
	Title    string
	Place    string
	Optional bool
	Weekdays int
	Day      string // YYYY-MM-DD, SINGLE only
	StartsAt string // HH:MM
	EndsAt   string
}

// Holiday is a family-wide range of dates, first and last day included.
type Holiday struct {
	Title    string
	StartsOn string
	EndsOn   string
}

// Item is one entry on one day.
type Item struct {
	EntryID  string `json:"entry_id"`
	Title    string `json:"title"`
	Place    string `json:"place"`
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
	Optional bool   `json:"optional"`
}

// Day is a date with its holiday ("" for none) and its items in time order.
type Day struct {
	Day     string `json:"day"`
	Holiday string `json:"holiday"`
	Items   []Item `json:"items"`
}

// WeekdayBit is a date's bit in Entry.Weekdays: Monday 1 … Sunday 64.
func WeekdayBit(d time.Weekday) int { return 1 << ((int(d) + 6) % 7) }

// HolidayOn is the title of the holiday that includes day, or "".
func HolidayOn(holidays []Holiday, day string) string {
	for _, h := range holidays {
		if h.StartsOn <= day && day <= h.EndsOn {
			return h.Title
		}
	}
	return ""
}

// Expand lays the entries out over n days from first. A holiday suppresses the repeating entries;
// a single entry on a holiday still happens — someone put it on that date on purpose.
func Expand(entries []Entry, holidays []Holiday, first time.Time, n int) []Day {
	out := make([]Day, 0, n)
	for i := 0; i < n; i++ {
		date := first.AddDate(0, 0, i)
		key := date.Format(time.DateOnly)
		day := Day{Day: key, Holiday: HolidayOn(holidays, key), Items: []Item{}}
		for _, e := range entries {
			on := false
			switch e.Kind {
			case Recurring:
				on = day.Holiday == "" && e.Weekdays&WeekdayBit(date.Weekday()) != 0
			case Single:
				on = e.Day == key
			}
			if on {
				day.Items = append(day.Items, Item{
					EntryID: e.ID, Title: e.Title, Place: e.Place,
					StartsAt: e.StartsAt, EndsAt: e.EndsAt, Optional: e.Optional,
				})
			}
		}
		sort.SliceStable(day.Items, func(a, b int) bool { return day.Items[a].StartsAt < day.Items[b].StartsAt })
		out = append(out, day)
	}
	return out
}
