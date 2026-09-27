package httpapi

// FR-23: a profile's alarm clock — a time per weekday, and a change for one date. The phone holds
// the rule and computes the next ring itself; the server never books an instant.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/enforce"
	"github.com/helios57/familyguard/backend/internal/store"
)

// maxAlarmDaysAhead is how far ahead a date change may be set: far enough for a school trip,
// near enough that a stale change is not waiting for a family that has forgotten it.
const maxAlarmDaysAhead = 60

type alarmWeekRequest struct {
	Weekdays []string `json:"weekdays"`
}

type alarmDayRequest struct {
	// Time is HH:MM, or null for no alarm that day.
	Time *string `json:"time"`
}

// alarmToday is the profile's current day, the first a change may be set for and the first the
// phone is sent.
func (s *Server) alarmToday(c *gin.Context, pol *store.Policy) (string, bool) {
	day, err := enforce.DayKey(pol, s.now())
	if err != nil {
		s.fail(c, err)
		return "", false
	}
	return day, true
}

func (s *Server) getAlarm(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	pol, err := s.store.GetPolicy(c.Request.Context(), childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	today, ok := s.alarmToday(c, pol)
	if !ok {
		return
	}
	alarm, err := s.store.GetAlarm(c.Request.Context(), childID, today)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, alarm)
}

// putAlarm replaces the weekly schedule (FR-23.1): seven entries, Monday first, "" for a day that
// does not ring.
func (s *Server) putAlarm(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req alarmWeekRequest
	if !bindJSON(c, &req) {
		return
	}
	if len(req.Weekdays) != 7 {
		failWith(c, http.StatusBadRequest, "invalid_input", "weekdays has seven entries, Monday first; \"\" for a day with no alarm")
		return
	}
	on := 0
	for i, at := range req.Weekdays {
		req.Weekdays[i] = strings.TrimSpace(at)
		if req.Weekdays[i] == "" {
			continue
		}
		if !clockPattern.MatchString(req.Weekdays[i]) {
			failWith(c, http.StatusBadRequest, "invalid_input", "an alarm time is HH:MM, not \""+at+"\"")
			return
		}
		on++
	}
	ctx := c.Request.Context()
	pol, err := s.store.GetPolicy(ctx, childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	if err := s.store.SetAlarmWeek(ctx, childID, req.Weekdays); err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "ALARM_UPDATED", "child", childID.String(), map[string]any{"days_on": on})
	s.notifyChild(c, childID, "policy")
	today, ok := s.alarmToday(c, pol)
	if !ok {
		return
	}
	alarm, err := s.store.GetAlarm(ctx, childID, today)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, alarm)
}

// alarmDay validates the :day of a date change: a real date, today up to maxAlarmDaysAhead days on,
// in the profile's calendar. It writes the refusal itself.
func (s *Server) alarmDay(c *gin.Context, pol *store.Policy) (string, bool) {
	day := c.Param("day")
	at, err := time.Parse(time.DateOnly, day)
	if err != nil || at.Format(time.DateOnly) != day {
		failWith(c, http.StatusBadRequest, "invalid_input", "the day is YYYY-MM-DD, and a real date")
		return "", false
	}
	today, ok := s.alarmToday(c, pol)
	if !ok {
		return "", false
	}
	first, _ := time.Parse(time.DateOnly, today)
	if at.Before(first) || at.After(first.AddDate(0, 0, maxAlarmDaysAhead)) {
		failWith(c, http.StatusBadRequest, "invalid_input", "an alarm can be changed from today up to 60 days ahead")
		return "", false
	}
	return day, true
}

// putAlarmDay sets the alarm for one day (FR-23.2): a time, or null for none that day.
func (s *Server) putAlarmDay(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req alarmDayRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Time != nil {
		t := strings.TrimSpace(*req.Time)
		if !clockPattern.MatchString(t) {
			failWith(c, http.StatusBadRequest, "invalid_input", "time is HH:MM, or null for no alarm that day")
			return
		}
		req.Time = &t
	}
	ctx := c.Request.Context()
	pol, err := s.store.GetPolicy(ctx, childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	day, ok := s.alarmDay(c, pol)
	if !ok {
		return
	}
	if err := s.store.SetAlarmDay(ctx, childID, day, req.Time, parentOf(c).ID); err != nil {
		s.fail(c, err)
		return
	}
	detail := map[string]any{"day": day, "time": nil}
	if req.Time != nil {
		detail["time"] = *req.Time
	}
	s.auditParent(c, "ALARM_DAY_SET", "child", childID.String(), detail)
	s.notifyChild(c, childID, "policy")
	c.JSON(http.StatusOK, store.AlarmDay{Day: day, Time: req.Time})
}

// deleteAlarmDay returns a day to the weekly schedule.
func (s *Server) deleteAlarmDay(c *gin.Context) {
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
	day, ok := s.alarmDay(c, pol)
	if !ok {
		return
	}
	if err := s.store.ClearAlarmDay(ctx, childID, day); err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "ALARM_DAY_CLEARED", "child", childID.String(), map[string]any{"day": day})
	s.notifyChild(c, childID, "policy")
	c.Status(http.StatusNoContent)
}

// deviceAlarm is the alarm block the phone is sent beside its policy: the rule, the zone it is
// read in, and the changes from today on.
type deviceAlarm struct {
	Timezone string `json:"timezone"`
	store.Alarm
}

func (s *Server) deviceAlarm(ctx context.Context, childID uuid.UUID) (*deviceAlarm, error) {
	pol, err := s.store.GetPolicy(ctx, childID)
	if err != nil {
		return nil, err
	}
	today, err := enforce.DayKey(pol, s.now())
	if err != nil {
		return nil, err
	}
	alarm, err := s.store.GetAlarm(ctx, childID, today)
	if err != nil {
		return nil, err
	}
	return &deviceAlarm{Timezone: pol.Timezone, Alarm: *alarm}, nil
}
