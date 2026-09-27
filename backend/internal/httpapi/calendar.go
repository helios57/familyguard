package httpapi

// FR-25: a profile's calendar, read-only, merged into the agenda's days.

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/agenda"
)

type calendarRequest struct {
	URL string `json:"url"`
}

type calendarView struct {
	URL       string     `json:"url"`
	FetchedAt *time.Time `json:"fetched_at"`
	Error     string     `json:"error"`
	Events    int        `json:"events"`
}

// countEvents is how many occurrences the calendar holds in the coming 60 days — what the console
// shows as "what the last read found".
func countEvents(body []byte, loc *time.Location, now time.Time) int {
	occ, err := agenda.CalendarOccurrences(body, now.Add(-24*time.Hour), now.AddDate(0, 0, 60), loc)
	if err != nil {
		return 0
	}
	return len(occ)
}

func (s *Server) calendarView(ctx context.Context, childID uuid.UUID, loc *time.Location) (calendarView, error) {
	src, err := s.store.GetCalendarSource(ctx, childID)
	if err != nil || src == nil {
		return calendarView{}, err
	}
	return calendarView{URL: src.URL, FetchedAt: src.FetchedAt, Error: src.LastError, Events: countEvents(src.Body, loc, s.now())}, nil
}

func (s *Server) getCalendar(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	pol, err := s.store.GetPolicy(c.Request.Context(), childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	loc, _ := time.LoadLocation(pol.Timezone)
	view, err := s.calendarView(c.Request.Context(), childID, loc)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// putCalendar sets the profile's calendar address (FR-25.1). It is read at once: an address that
// cannot be read, or that does not answer a calendar, is refused rather than stored to fail later.
func (s *Server) putCalendar(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req calendarRequest
	if !bindJSON(c, &req) {
		return
	}
	address, err := agenda.NormalizeCalendarURL(req.URL, s.cfg.CalendarAllowLocal)
	if err != nil {
		failWith(c, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	ctx := c.Request.Context()
	pol, err := s.store.GetPolicy(ctx, childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	loc, _ := time.LoadLocation(pol.Timezone)
	body, err := s.calendars.Fetch(ctx, address)
	if err != nil {
		failWith(c, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	if _, err := agenda.CalendarOccurrences(body, s.now(), s.now().AddDate(0, 0, 1), loc); err != nil {
		failWith(c, http.StatusBadRequest, "invalid_input", "the address answered, but not with an iCalendar file (.ics)")
		return
	}
	if err := s.store.SetCalendarSource(ctx, childID, address, body); err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "CALENDAR_SET", "child", childID.String(), map[string]any{"host": hostOf(address)})
	s.notifyChild(c, childID, "policy")
	view, err := s.calendarView(ctx, childID, loc)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) deleteCalendar(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := s.store.GetPolicy(ctx, childID); err != nil {
		s.fail(c, err)
		return
	}
	if err := s.store.DeleteCalendarSource(ctx, childID); err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "CALENDAR_REMOVED", "child", childID.String(), map[string]any{})
	s.notifyChild(c, childID, "policy")
	c.Status(http.StatusNoContent)
}

// hostOf is what the audit may say about a calendar address: its host, never its path or query,
// which is where a secret address keeps its secret.
func hostOf(address string) string {
	if u, err := url.Parse(address); err == nil {
		return u.Host
	}
	return ""
}

// calendarOccurrences is the profile's calendar over [from, to) for the agenda's days (FR-25.3). A copy older
// than CALENDAR_MAX_AGE is answered as it is and read again in the background, one read per profile
// at a time; the phone and the console poll, so freshness follows use without a scheduler.
func (s *Server) calendarOccurrences(ctx context.Context, childID uuid.UUID, from, to time.Time) ([]agenda.Occurrence, error) {
	src, err := s.store.GetCalendarSource(ctx, childID)
	if err != nil || src == nil {
		return nil, err
	}
	last := src.LastAttemptAt
	if last == nil || s.now().Sub(*last) > s.cfg.CalendarMaxAge {
		s.refreshCalendar(childID, src.URL)
	}
	if len(src.Body) == 0 {
		return nil, nil
	}
	occ, err := agenda.CalendarOccurrences(src.Body, from, to, from.Location())
	if err != nil {
		// The kept copy parsed when it was stored; a copy that stopped parsing is not worth a 500
		// on the whole agenda. The next read replaces it.
		s.log.Warn("the kept calendar no longer parses", "child", childID, "error", err)
		return nil, nil
	}
	return occ, nil
}

func (s *Server) refreshCalendar(childID uuid.UUID, address string) {
	if _, busy := s.calendarBusy.LoadOrStore(childID, true); busy {
		return
	}
	go func() {
		defer s.calendarBusy.Delete(childID)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		body, err := s.calendars.Fetch(ctx, address)
		readErr := ""
		if err != nil {
			readErr = err.Error()
		} else if _, perr := agenda.CalendarOccurrences(body, s.now(), s.now().AddDate(0, 0, 1), time.UTC); perr != nil {
			readErr = "the address answered, but not with an iCalendar file (.ics)"
		}
		if err := s.store.RecordCalendarRead(ctx, childID, address, body, readErr); err != nil {
			s.log.Error("could not record a calendar read", "child", childID, "error", err)
		}
	}()
}
