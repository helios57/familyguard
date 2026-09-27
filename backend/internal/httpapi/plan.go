package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/earned"
	"github.com/helios57/familyguard/backend/internal/enforce"
	"github.com/helios57/familyguard/backend/internal/store"
)

// FR-22: a profile's daily plan, today's tasks, and the earned time they are worth.

const (
	maxPlanGroups     = 12
	maxTasksPerGroup  = 20
	maxGroupTitle     = 80
	maxTaskTitle      = 120
	maxTaskNote       = 200
	maxEarnedPerGroup = 1440
)

var clockPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

type planRequest struct {
	Groups []store.PlanGroup `json:"groups"`
}

func (s *Server) getPlan(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if _, err := s.store.GetPolicy(c.Request.Context(), childID); err != nil {
		s.fail(c, err)
		return
	}
	plan, err := s.store.GetPlan(c.Request.Context(), childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"groups": plan})
}

// putPlan replaces the plan as one document (FR-22). A group or task that carries its id is
// updated in place, so its history stays attached; one without is new; one left out is retired.
func (s *Server) putPlan(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req planRequest
	if !bindJSON(c, &req) {
		return
	}
	if msg := validatePlan(req.Groups); msg != "" {
		failWith(c, http.StatusBadRequest, "invalid_input", msg)
		return
	}
	ctx := c.Request.Context()
	if _, err := s.store.GetPolicy(ctx, childID); err != nil {
		s.fail(c, err)
		return
	}
	plan, err := s.store.ReplacePlan(ctx, childID, req.Groups)
	if err != nil {
		s.fail(c, err)
		return
	}
	tasks := 0
	for _, g := range plan {
		tasks += len(g.Tasks)
	}
	s.auditParent(c, "PLAN_UPDATED", "child", childID.String(), map[string]any{"groups": len(plan), "tasks": tasks})
	s.notifyChild(c, childID, "policy")
	c.JSON(http.StatusOK, gin.H{"groups": plan})
}

// validatePlan names the first thing wrong with a plan, or "".
func validatePlan(groups []store.PlanGroup) string {
	if len(groups) > maxPlanGroups {
		return "a plan has at most 12 groups"
	}
	for i := range groups {
		g := &groups[i]
		g.Title = strings.TrimSpace(g.Title)
		g.StartsAt = strings.TrimSpace(g.StartsAt)
		g.EndsAt = strings.TrimSpace(g.EndsAt)
		switch {
		case g.Title == "" || len([]rune(g.Title)) > maxGroupTitle:
			return "every group needs a title of at most 80 characters"
		case g.Weekdays < 1 || g.Weekdays > 127:
			return "weekdays is a bit set, Monday = 1 … Sunday = 64, and needs at least one day"
		case !clockPattern.MatchString(g.StartsAt) || !clockPattern.MatchString(g.EndsAt):
			return "starts_at and ends_at must be HH:MM"
		case g.StartsAt >= g.EndsAt:
			return "a group ends after it starts, on the same day (\"" + g.Title + "\")"
		case g.EarnedMinutes < 0 || g.EarnedMinutes > maxEarnedPerGroup:
			return "earned_minutes must be between 0 and 1440"
		case len(g.Tasks) == 0:
			return "every group needs at least one task (\"" + g.Title + "\")"
		case len(g.Tasks) > maxTasksPerGroup:
			return "a group has at most 20 tasks"
		}
		for j := range g.Tasks {
			t := &g.Tasks[j]
			t.Title = strings.TrimSpace(t.Title)
			t.Note = strings.TrimSpace(t.Note)
			if t.Title == "" || len([]rune(t.Title)) > maxTaskTitle {
				return "every task needs a title of at most 120 characters"
			}
			if len([]rune(t.Note)) > maxTaskNote {
				return "a task's note is at most 200 characters"
			}
		}
	}
	return ""
}

// ---- today ------------------------------------------------------------------

type todayTask struct {
	ID    uuid.UUID `json:"id"`
	Title string    `json:"title"`
	Note  string    `json:"note"`
	// State is OPEN, REPORTED, CONFIRMED or REJECTED.
	State      string     `json:"state"`
	ReportedAt *time.Time `json:"reported_at,omitempty"`
}

type todayGroup struct {
	ID            uuid.UUID `json:"id"`
	Title         string    `json:"title"`
	StartsAt      string    `json:"starts_at"`
	EndsAt        string    `json:"ends_at"`
	EarnedMinutes int       `json:"earned_minutes"`
	// Open is whether the group's window contains now: the child can report only then.
	Open bool `json:"open"`
	// Credited is the earned time the group earned today, 0 until every task is confirmed.
	Credited int         `json:"credited_minutes"`
	Tasks    []todayTask `json:"tasks"`
}

type todayEarned struct {
	// AvailableMinutes is the balance at the start of today plus today's credits; SpentMinutes is
	// what the profile's phones reported spending today; LeftMinutes the difference (FR-22).
	AvailableMinutes int                `json:"available_minutes"`
	SpentMinutes     int                `json:"spent_minutes"`
	LeftMinutes      int                `json:"left_minutes"`
	Credits          []earned.Remaining `json:"credits"`
}

type todayView struct {
	Day    string       `json:"day"`
	Groups []todayGroup `json:"groups"`
	Earned todayEarned  `json:"earned"`
}

// weekdayBit is a day's bit in plan_groups.weekdays: Monday 1 … Sunday 64.
func weekdayBit(d time.Weekday) int { return 1 << ((int(d) + 6) % 7) }

// today is a profile's day as the plan sees it: the groups that run today, each task's state, the
// credits, and the balance.
func (s *Server) today(ctx context.Context, childID uuid.UUID) (*todayView, error) {
	pol, err := s.store.GetPolicy(ctx, childID)
	if err != nil {
		return nil, err
	}
	day, err := enforce.DayKey(pol, s.now())
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(pol.Timezone)
	if err != nil {
		return nil, err
	}
	local := s.now().In(loc)
	clock := local.Format("15:04")

	plan, err := s.store.GetPlan(ctx, childID)
	if err != nil {
		return nil, err
	}
	states, err := s.store.TaskDays(ctx, childID, day)
	if err != nil {
		return nil, err
	}
	credited, err := s.store.CreditedGroups(ctx, childID, day)
	if err != nil {
		return nil, err
	}
	view := &todayView{Day: day, Groups: []todayGroup{}}
	for _, g := range plan {
		if g.Weekdays&weekdayBit(local.Weekday()) == 0 {
			continue
		}
		tg := todayGroup{
			ID: g.ID, Title: g.Title, StartsAt: g.StartsAt, EndsAt: g.EndsAt, EarnedMinutes: g.EarnedMinutes,
			Open: clock >= g.StartsAt && clock < g.EndsAt, Credited: credited[g.ID], Tasks: []todayTask{},
		}
		for _, t := range g.Tasks {
			tt := todayTask{ID: t.ID, Title: t.Title, Note: t.Note, State: store.TaskOpen}
			if d, ok := states[t.ID]; ok {
				tt.State, tt.ReportedAt = d.State, d.ReportedAt
			}
			tg.Tasks = append(tg.Tasks, tt)
		}
		view.Groups = append(view.Groups, tg)
	}

	balance, err := s.resolver.Balance(ctx, childID, day)
	if err != nil {
		return nil, err
	}
	spent, err := s.store.EarnedSpentByDay(ctx, childID, day)
	if err != nil {
		return nil, err
	}
	view.Earned = todayEarned{
		AvailableMinutes: balance.AvailableToday, SpentMinutes: spent[day],
		LeftMinutes: balance.AvailableToday - spent[day], Credits: balance.Credits,
	}
	return view, nil
}

func (s *Server) getToday(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	view, err := s.today(c.Request.Context(), childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

type decisionRequest struct {
	// Decision is confirm, reject or undo.
	Decision string `json:"decision"`
}

// decideTask confirms, rejects or undoes a task for today (FR-22) — the guardian window's
// "Bestätigen" and "Nicht erledigt". Confirming a group's last open task earns its credit; any
// decision that leaves the group incomplete withdraws it.
func (s *Server) decideTask(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	taskID, ok := uuidParam(c, "task")
	if !ok {
		return
	}
	var req decisionRequest
	if !bindJSON(c, &req) {
		return
	}
	var state string
	switch strings.ToLower(strings.TrimSpace(req.Decision)) {
	case "confirm":
		state = store.TaskConfirmed
	case "reject":
		state = store.TaskRejected
	case "undo":
		state = store.TaskOpen
	default:
		failWith(c, http.StatusBadRequest, "invalid_input", "decision must be confirm, reject or undo")
		return
	}
	ctx := c.Request.Context()
	pol, err := s.store.GetPolicy(ctx, childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	day, err := enforce.DayKey(pol, s.now())
	if err != nil {
		s.fail(c, err)
		return
	}
	change, err := s.store.DecideTask(ctx, childID, day, taskID, state, parentOf(c).ID)
	if errors.Is(err, store.ErrTaskNotInPlan) {
		failWith(c, http.StatusNotFound, "not_found", "that task is not in this profile's plan")
		return
	}
	if err != nil {
		s.fail(c, err)
		return
	}
	detail := map[string]any{"task": taskID.String(), "day": day}
	// Each action written out: the audit test reads the actions from these call sites.
	switch state {
	case store.TaskConfirmed:
		s.auditParent(c, "TASK_CONFIRMED", "child", childID.String(), detail)
	case store.TaskRejected:
		s.auditParent(c, "TASK_REJECTED", "child", childID.String(), detail)
	default:
		s.auditParent(c, "TASK_UNDONE", "child", childID.String(), detail)
	}
	if change.Credited > 0 {
		s.auditParent(c, "EARNED_TIME_CREDITED", "child", childID.String(),
			map[string]any{"minutes": change.Credited, "day": day})
	}
	if change.Withdrawn {
		s.auditParent(c, "EARNED_TIME_WITHDRAWN", "child", childID.String(), map[string]any{"day": day})
	}
	// The phones fetch the new balance; the other parents' windows redraw.
	s.notifyChild(c, childID, "policy")
	view, err := s.today(ctx, childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"today": view, "credit": change})
}

// deviceReportTask is the child's "Fertig" (FR-22): only a task of this phone's profile, of a group
// that runs today, inside its window. It asks a parent to confirm; it earns nothing by itself.
func (s *Server) deviceReportTask(c *gin.Context) {
	dev := deviceOf(c)
	taskID, ok := uuidParam(c, "task")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	group, err := s.store.TaskGroup(ctx, dev.ChildID, taskID)
	if errors.Is(err, store.ErrTaskNotInPlan) {
		failWith(c, http.StatusNotFound, "not_found", "that task is not in this phone's plan")
		return
	}
	if err != nil {
		s.fail(c, err)
		return
	}
	pol, err := s.store.GetPolicy(ctx, dev.ChildID)
	if err != nil {
		s.fail(c, err)
		return
	}
	loc, err := time.LoadLocation(pol.Timezone)
	if err != nil {
		s.fail(c, err)
		return
	}
	local := s.now().In(loc)
	clock := local.Format("15:04")
	if group.Weekdays&weekdayBit(local.Weekday()) == 0 || clock < group.StartsAt || clock >= group.EndsAt {
		failWith(c, http.StatusConflict, "not_now",
			"this task can be reported between "+group.StartsAt+" and "+group.EndsAt+" on its days")
		return
	}
	day := local.Format(time.DateOnly)
	state, err := s.store.ReportTask(ctx, dev.ChildID, day, taskID, dev.ID)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.audit(c, store.ActorDevice, dev.ID.String(), "TASK_REPORTED", "child", dev.ChildID.String(),
		map[string]any{"task": taskID.String(), "day": day, "state": state})
	s.hub.PublishParents(Event{Type: "task", ChildID: dev.ChildID.String(), DeviceID: dev.ID.String()})
	view, err := s.today(ctx, dev.ChildID)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}
