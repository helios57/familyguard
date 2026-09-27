package httpapi

// FR-24: a profile's agenda, the family's holidays, and the week the server expands from them.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/agenda"
	"github.com/helios57/familyguard/backend/internal/enforce"
	"github.com/helios57/familyguard/backend/internal/store"
)

const (
	maxAgendaEntries = 50
	maxAgendaText    = 80
	maxHolidays      = 50
	maxHolidayDays   = 120
	maxAgendaDays    = 31
)

type agendaRequest struct {
	Entries []store.AgendaEntry `json:"entries"`
}

type holidaysRequest struct {
	Holidays []store.Holiday `json:"holidays"`
}

func (s *Server) getAgenda(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if _, err := s.store.GetPolicy(c.Request.Context(), childID); err != nil {
		s.fail(c, err)
		return
	}
	entries, err := s.store.GetAgenda(c.Request.Context(), childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}

// putAgenda replaces the agenda as one document (FR-24.1).
func (s *Server) putAgenda(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req agendaRequest
	if !bindJSON(c, &req) {
		return
	}
	if msg := validateAgenda(req.Entries); msg != "" {
		failWith(c, http.StatusBadRequest, "invalid_input", msg)
		return
	}
	ctx := c.Request.Context()
	if _, err := s.store.GetPolicy(ctx, childID); err != nil {
		s.fail(c, err)
		return
	}
	entries, err := s.store.ReplaceAgenda(ctx, childID, req.Entries)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "AGENDA_UPDATED", "child", childID.String(), map[string]any{"entries": len(entries)})
	s.notifyChild(c, childID, "policy")
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}

// validateAgenda names the first thing wrong with an agenda, or "".
func validateAgenda(entries []store.AgendaEntry) string {
	if len(entries) > maxAgendaEntries {
		return "an agenda has at most 50 entries"
	}
	for i := range entries {
		e := &entries[i]
		e.Title = strings.TrimSpace(e.Title)
		e.Place = strings.TrimSpace(e.Place)
		switch {
		case e.Title == "" || len([]rune(e.Title)) > maxAgendaText:
			return "every entry needs a title of at most 80 characters"
		case len([]rune(e.Place)) > maxAgendaText:
			return "a place is at most 80 characters"
		case !clockPattern.MatchString(e.StartsAt) || !clockPattern.MatchString(e.EndsAt):
			return "starts_at and ends_at must be HH:MM"
		case e.StartsAt >= e.EndsAt:
			return "an entry ends after it starts, on the same day (\"" + e.Title + "\")"
		}
		switch e.Kind {
		case agenda.Recurring:
			if e.Weekdays < 1 || e.Weekdays > 127 {
				return "a repeating entry needs weekdays, a bit set Monday = 1 … Sunday = 64 (\"" + e.Title + "\")"
			}
			e.Day = ""
		case agenda.Single:
			if !realDate(e.Day) {
				return "an entry on one date needs day as YYYY-MM-DD, a real date (\"" + e.Title + "\")"
			}
			e.Weekdays = 0
		default:
			return "kind is RECURRING or SINGLE"
		}
	}
	return ""
}

func realDate(day string) bool {
	t, err := time.Parse(time.DateOnly, day)
	return err == nil && t.Format(time.DateOnly) == day
}

// getAgendaDays is the agenda laid out over days (FR-24.3): ?from=YYYY-MM-DD (default today in the
// profile's calendar) and ?days=1…31 (default 7).
func (s *Server) getAgendaDays(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	pol, err := s.store.GetPolicy(ctx, childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	from := c.Query("from")
	if from == "" {
		if from, err = enforce.DayKey(pol, s.now()); err != nil {
			s.fail(c, err)
			return
		}
	}
	first, err := time.Parse(time.DateOnly, from)
	if err != nil || first.Format(time.DateOnly) != from {
		failWith(c, http.StatusBadRequest, "invalid_input", "from is YYYY-MM-DD")
		return
	}
	n := 7
	if raw := c.Query("days"); raw != "" {
		if n, err = strconv.Atoi(raw); err != nil || n < 1 || n > maxAgendaDays {
			failWith(c, http.StatusBadRequest, "invalid_input", "days is 1 to 31")
			return
		}
	}
	days, err := s.agendaDays(ctx, childID, first, n)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"days": days})
}

func (s *Server) agendaDays(ctx context.Context, childID uuid.UUID, first time.Time, n int) ([]agenda.Day, error) {
	entries, err := s.store.GetAgenda(ctx, childID)
	if err != nil {
		return nil, err
	}
	holidays, err := s.store.ListHolidays(ctx)
	if err != nil {
		return nil, err
	}
	in := make([]agenda.Entry, 0, len(entries))
	for _, e := range entries {
		in = append(in, agenda.Entry{
			ID: e.ID.String(), Kind: e.Kind, Title: e.Title, Place: e.Place, Optional: e.Optional,
			Weekdays: e.Weekdays, Day: e.Day, StartsAt: e.StartsAt, EndsAt: e.EndsAt,
		})
	}
	// The days in the profile's zone, so a calendar's times are read in it (FR-25).
	pol, err := s.store.GetPolicy(ctx, childID)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(pol.Timezone)
	if err != nil {
		loc = time.UTC
	}
	start := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, loc)
	occ, err := s.calendarOccurrences(ctx, childID, start, start.AddDate(0, 0, n))
	if err != nil {
		return nil, err
	}
	return agenda.ExpandWithCalendar(in, agendaHolidays(holidays), occ, start, n), nil
}

func agendaHolidays(holidays []store.Holiday) []agenda.Holiday {
	out := make([]agenda.Holiday, 0, len(holidays))
	for _, h := range holidays {
		out = append(out, agenda.Holiday{Title: h.Title, StartsOn: h.StartsOn, EndsOn: h.EndsOn})
	}
	return out
}

func (s *Server) getHolidays(c *gin.Context) {
	holidays, err := s.store.ListHolidays(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"holidays": holidays})
}

// putHolidays replaces the family's holidays as one document (FR-24.2), and every phone re-reads:
// holidays change the agenda and the alarm of every profile.
func (s *Server) putHolidays(c *gin.Context) {
	var req holidaysRequest
	if !bindJSON(c, &req) {
		return
	}
	if len(req.Holidays) > maxHolidays {
		failWith(c, http.StatusBadRequest, "invalid_input", "at most 50 holidays")
		return
	}
	for i := range req.Holidays {
		h := &req.Holidays[i]
		h.Title = strings.TrimSpace(h.Title)
		if h.Title == "" || len([]rune(h.Title)) > maxAgendaText {
			failWith(c, http.StatusBadRequest, "invalid_input", "every holiday needs a title of at most 80 characters")
			return
		}
		if !realDate(h.StartsOn) || !realDate(h.EndsOn) {
			failWith(c, http.StatusBadRequest, "invalid_input", "starts_on and ends_on are YYYY-MM-DD, real dates")
			return
		}
		a, _ := time.Parse(time.DateOnly, h.StartsOn)
		b, _ := time.Parse(time.DateOnly, h.EndsOn)
		if b.Before(a) || b.Sub(a) > maxHolidayDays*24*time.Hour {
			failWith(c, http.StatusBadRequest, "invalid_input", "a holiday ends on or after its first day, within 120 days (\""+h.Title+"\")")
			return
		}
	}
	ctx := c.Request.Context()
	fam, err := s.store.GetFamily(ctx)
	if err != nil {
		s.fail(c, err)
		return
	}
	holidays, err := s.store.ReplaceHolidays(ctx, fam.ID, req.Holidays)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "HOLIDAYS_UPDATED", "family", "", map[string]any{"holidays": len(holidays)})
	children, err := s.store.ListChildren(ctx)
	if err != nil {
		s.log.Error("could not fan out a holiday change", "error", err, "request_id", RequestIDOf(c))
	}
	for _, ch := range children {
		s.notifyChild(c, ch.ID, "policy")
	}
	c.JSON(http.StatusOK, gin.H{"holidays": holidays})
}

// deviceAgenda is the phone's agenda block: today and tomorrow in the profile's calendar.
func (s *Server) deviceAgenda(ctx context.Context, childID uuid.UUID, pol *store.Policy) (gin.H, error) {
	today, err := enforce.DayKey(pol, s.now())
	if err != nil {
		return nil, err
	}
	first, _ := time.Parse(time.DateOnly, today)
	days, err := s.agendaDays(ctx, childID, first, 2)
	if err != nil {
		return nil, err
	}
	return gin.H{"days": days}, nil
}
