package agenda

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// Occurrence is one occurrence of a calendar event (FR-25), in absolute time.
type Occurrence struct {
	Title, Place string
	Start, End   time.Time
	AllDay       bool
}

// maxOccurrences bounds what one calendar can put into a window, so a rule that repeats every
// minute cannot turn a week's read into a million rows.
const maxOccurrences = 2000

// CalendarOccurrences reads an iCalendar body and lists the occurrences that overlap [from, to),
// with times read in loc where the calendar gives none. It handles RRULE with EXDATE and RDATE
// (including several dates in one property, which go-ical does not split), occurrences moved or
// changed with RECURRENCE-ID, cancelled events, and all-day events. A body that is not a calendar
// is an error; one event that cannot be read is skipped, so one bad event does not blank the rest.
func CalendarOccurrences(body []byte, from, to time.Time, loc *time.Location) ([]Occurrence, error) {
	cal, err := ical.NewDecoder(bytes.NewReader(body)).Decode()
	if err != nil {
		return nil, fmt.Errorf("not an iCalendar file: %w", err)
	}
	events := cal.Events()

	// Occurrences replaced by a RECURRENCE-ID event of the same UID are left out of the series.
	moved := map[string]map[int64]bool{}
	for i := range events {
		rid := events[i].Props.Get(ical.PropRecurrenceID)
		if rid == nil {
			continue
		}
		at, err := rid.DateTime(loc)
		if err != nil {
			continue
		}
		uid, _ := events[i].Props.Text(ical.PropUID)
		if moved[uid] == nil {
			moved[uid] = map[int64]bool{}
		}
		moved[uid][at.Unix()] = true
	}

	var out []Occurrence
	for i := range events {
		e := &events[i]
		if st := e.Props.Get(ical.PropStatus); st != nil && strings.EqualFold(st.Value, "CANCELLED") {
			continue
		}
		startProp := e.Props.Get(ical.PropDateTimeStart)
		if startProp == nil {
			continue
		}
		start, err := startProp.DateTime(loc)
		if err != nil {
			continue
		}
		allDay := startProp.ValueType() == ical.ValueDate || len(startProp.Value) == len("20060102")
		end, err := e.DateTimeEnd(loc)
		if err != nil || end.Before(start) {
			end = start
		}
		if allDay && !end.After(start) {
			end = start.AddDate(0, 0, 1)
		}
		title, _ := e.Props.Text(ical.PropSummary)
		place, _ := e.Props.Text(ical.PropLocation)
		occ := func(s time.Time) Occurrence {
			return Occurrence{Title: strings.TrimSpace(title), Place: strings.TrimSpace(place), Start: s, End: s.Add(end.Sub(start)), AllDay: allDay}
		}

		if e.Props.Get(ical.PropRecurrenceRule) == nil || e.Props.Get(ical.PropRecurrenceID) != nil {
			if o := occ(start); overlaps(o, from, to) {
				out = append(out, o)
			}
			continue
		}
		set, err := recurrence(e, start, loc)
		if err != nil {
			continue
		}
		uid, _ := e.Props.Text(ical.PropUID)
		for _, s := range set.Between(from.Add(-end.Sub(start)), to, true) {
			if moved[uid][s.Unix()] {
				continue
			}
			if o := occ(s); overlaps(o, from, to) {
				out = append(out, o)
			}
			if len(out) >= maxOccurrences {
				break
			}
		}
		if len(out) >= maxOccurrences {
			break
		}
	}
	return out, nil
}

func overlaps(o Occurrence, from, to time.Time) bool {
	if o.End.Equal(o.Start) {
		return !o.Start.Before(from) && o.Start.Before(to)
	}
	return o.Start.Before(to) && o.End.After(from)
}

// recurrence builds the event's recurrence set, splitting EXDATE and RDATE lists itself.
func recurrence(e *ical.Event, start time.Time, loc *time.Location) (*rrule.Set, error) {
	opt, err := e.Props.RecurrenceRule()
	if err != nil || opt == nil {
		return nil, fmt.Errorf("unreadable RRULE: %v", err)
	}
	opt.Dtstart = start
	rule, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, err
	}
	set := &rrule.Set{}
	set.RRule(rule)
	each := func(name string, add func(time.Time)) {
		for _, p := range e.Props[name] {
			for _, v := range strings.Split(p.Value, ",") {
				q := p
				q.Value = strings.TrimSpace(v)
				if at, err := q.DateTime(loc); err == nil {
					add(at)
				}
			}
		}
	}
	each(ical.PropExceptionDates, set.ExDate)
	each(ical.PropRecurrenceDates, set.RDate)
	return set, nil
}

// ExpandWithCalendar is Expand with a calendar's occurrences merged in (FR-25.3): each on every day it
// touches, clipped to that day, all-day ones first. A holiday does not hide them — a calendar event
// is on its date on purpose, like a single entry.
func ExpandWithCalendar(entries []Entry, holidays []Holiday, occ []Occurrence, first time.Time, n int) []Day {
	days := Expand(entries, holidays, first, n)
	loc := first.Location()
	for i := range days {
		date, _ := time.ParseInLocation(time.DateOnly, days[i].Day, loc)
		dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
		dayEnd := dayStart.AddDate(0, 0, 1)
		for _, o := range occ {
			if !overlaps(o, dayStart, dayEnd) {
				continue
			}
			it := Item{Title: o.Title, Place: o.Place, AllDay: o.AllDay, Source: SourceCalendar}
			if o.AllDay {
				it.StartsAt, it.EndsAt = "00:00", "23:59"
			} else {
				s, e := o.Start, o.End
				if s.Before(dayStart) {
					s = dayStart
				}
				it.StartsAt = s.In(loc).Format("15:04")
				if !e.Before(dayEnd) {
					it.EndsAt = "23:59"
				} else {
					it.EndsAt = e.In(loc).Format("15:04")
				}
			}
			days[i].Items = append(days[i].Items, it)
		}
		sort.SliceStable(days[i].Items, func(a, b int) bool {
			x, y := days[i].Items[a], days[i].Items[b]
			if x.AllDay != y.AllDay {
				return x.AllDay
			}
			return x.StartsAt < y.StartsAt
		})
	}
	return days
}
