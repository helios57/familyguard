package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/enforce"
	"github.com/helios57/familyguard/backend/internal/policy"
	"github.com/helios57/familyguard/backend/internal/store"
)

// Why an app cannot be used right now (FR-3.10), as the console and the phone both say it. The
// three the engine already names keep their names; the other three are the per-app causes the
// engine has no single field for.
const (
	BlockedByRule   = "BLOCKED"   // a parent's BLOCK rule, the family blocklist or the YouTube switch
	BlockedPending  = "PENDING"   // installed while free installation is off, waiting for a parent
	BlockedAppLimit = "APP_LIMIT" // this app's own daily allowance is spent
	BlockedQuota    = policy.ReasonQuota
	BlockedBedtime  = policy.ReasonBedtime
	notBlocked      = ""
)

// recordDayLimits writes what limits apply to a child on day (FR-3.9). Called whenever something
// that decides them changes while the day is current: a usage report, a bonus, a policy change.
func (s *Server) recordDayLimits(ctx context.Context, childID uuid.UUID, pol *store.Policy, day string) error {
	rules, err := s.store.ListAppRules(ctx, childID)
	if err != nil {
		return err
	}
	bonus, err := s.store.BonusMinutes(ctx, childID, day)
	if err != nil {
		return err
	}
	return s.store.RecordDayLimits(ctx, childID, day, pol.DailyLimitMinutes, bonus, ownLimits(rules))
}

// ownLimits is each app's own daily allowance: LIMIT rules with minutes set.
func ownLimits(rules []store.AppRule) map[string]int {
	out := map[string]int{}
	for _, r := range rules {
		if r.Action == store.ActionLimit && r.LimitMinutes > 0 {
			out[r.PackageName] = r.LimitMinutes
		}
	}
	return out
}

type grantBonusRequest struct {
	Minutes int `json:"minutes"`
}

// grantBonus adds screen time to a child for today only (FR-3.11) — the console's "+ time today".
//
// "Today" is the child's calendar day in the policy's zone, the same day key the quota counts
// minutes under, so the extra time and the minutes it offsets can never land on different days. It
// is carried to the phone as a bonus FOR that day, which the phone's own engine drops at midnight
// even when it is offline and cannot be told.
func (s *Server) grantBonus(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req grantBonusRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Minutes < 1 || req.Minutes > store.MaxBonusMinutesPerDay {
		failWith(c, http.StatusBadRequest, "invalid_input", "minutes must be between 1 and 1440")
		return
	}
	ctx := c.Request.Context()
	pol, err := s.store.GetPolicy(ctx, childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	if pol.DailyLimitMinutes <= 0 {
		failWith(c, http.StatusConflict, "no_daily_limit",
			"this child has no daily limit, so there is nothing to add extra time to")
		return
	}
	day, err := enforce.DayKey(pol, s.now())
	if err != nil {
		s.fail(c, err)
		return
	}
	total, err := s.store.GrantBonus(ctx, childID, day, req.Minutes)
	if errors.Is(err, store.ErrBonusTooLarge) {
		failWith(c, http.StatusConflict, "bonus_too_large", "a day cannot have more than 1440 minutes of extra time")
		return
	}
	if err != nil {
		s.fail(c, err)
		return
	}
	if err := s.recordDayLimits(ctx, childID, pol, day); err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "BONUS_GRANTED", "child", childID.String(), map[string]any{
		"minutes": req.Minutes, "day": day, "bonus_minutes": total,
	})
	// Every phone of this child re-syncs now: the extra time is only real once the phone lifts
	// the suspension, and a parent pressing the button is standing next to a child waiting for it.
	s.notifyChild(c, childID, "policy")
	c.JSON(http.StatusOK, gin.H{
		"day": day, "bonus_minutes": total, "daily_limit_minutes": pol.DailyLimitMinutes,
		"limit_today_minutes": pol.DailyLimitMinutes + total,
	})
}

// appRow is one line of the Activity table: the day's measurement, and what governs the app.
type appRow struct {
	store.UsageSample
	// Counted is false for the home screen, System UI and this system's own app (FR-3.8).
	Counted bool `json:"counted"`
	// LimitMinutes is this app's own allowance on that day, 0 when it has none.
	LimitMinutes int `json:"limit_minutes"`
	// Rule is the parent's rule for the app now: ALLOW, LIMIT, BLOCK, or "" for none.
	Rule string `json:"rule"`
	// Blocked is why the app cannot be used right now (today only; "" on another day, and "" for
	// an app that is usable).
	Blocked string `json:"blocked"`
	// FreeByDefault is true for a preinstalled app with no rule that stays usable whatever the
	// time (FR-5.10). Today only, from the same computation as Blocked.
	FreeByDefault bool `json:"free_by_default"`
}

// screenTime is the day's counted use against the limit that applied (FR-3.9).
type screenTime struct {
	CountedMinutes   int `json:"counted_minutes"`
	UncountedMinutes int `json:"uncounted_minutes"`
	// DailyLimitMinutes and BonusMinutes are what applied on the day; LimitRecorded is false for a
	// day before anything recorded them, which the console says rather than guessing.
	DailyLimitMinutes int  `json:"daily_limit_minutes"`
	BonusMinutes      int  `json:"bonus_minutes"`
	LimitRecorded     bool `json:"limit_recorded"`
	// SuspendReason is QUOTA or BEDTIME while that is in force now (today only).
	SuspendReason string `json:"suspend_reason"`
	IsToday       bool   `json:"is_today"`
}

// describeDay joins a day's usage rows with what governs each app, for the Activity card.
//
// Today is read from the resolver — the same computation the phone obeys — so the console cannot
// say an app is usable while the phone suspends it. Another day is read from day_limits.
func (s *Server) describeDay(ctx context.Context, dev *store.Device, pol *store.Policy, day string, samples []store.UsageSample) ([]appRow, screenTime, error) {
	st := screenTime{}
	rules, err := s.store.ListAppRules(ctx, dev.ChildID)
	if err != nil {
		return nil, st, err
	}
	ruleOf := map[string]string{}
	for _, r := range rules {
		ruleOf[r.PackageName] = r.Action
	}

	today, err := enforce.DayKey(pol, s.now())
	if err != nil {
		return nil, st, err
	}
	st.IsToday = day == today

	var uncounted []string
	limits := map[string]int{}
	var desired *policy.DesiredState
	if st.IsToday {
		ds, in, err := s.resolver.Resolve(ctx, dev.ID, s.now())
		if err != nil {
			return nil, st, err
		}
		desired = ds
		uncounted = in.UncountedPackages
		limits = ownLimits(rules)
		st.DailyLimitMinutes = pol.DailyLimitMinutes
		st.BonusMinutes = ds.BonusMinutes
		st.LimitRecorded = true
		st.SuspendReason = ds.SuspendReason
	} else {
		home, err := s.store.HomePackages(ctx, dev.ID)
		if err != nil {
			return nil, st, err
		}
		uncounted = store.SortedUnique(home, policy.PlatformUncountedPackages, []string{s.cfg.DPCPackage()})
		recorded, err := s.store.GetDayLimits(ctx, dev.ChildID, day)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return nil, st, err
		default:
			st.DailyLimitMinutes = recorded.DailyLimitMinutes
			st.BonusMinutes = recorded.BonusMinutes
			st.LimitRecorded = true
			limits = recorded.AppLimits
		}
	}

	rows := make([]appRow, 0, len(samples))
	var countedMs, uncountedMs int64
	for _, sample := range samples {
		row := appRow{
			UsageSample:  sample,
			Counted:      !slices.Contains(uncounted, sample.PackageName),
			LimitMinutes: limits[sample.PackageName],
			Rule:         ruleOf[sample.PackageName],
		}
		if row.Counted {
			countedMs += sample.ForegroundMs
		} else {
			uncountedMs += sample.ForegroundMs
		}
		if desired != nil {
			row.Blocked = blockedReason(desired, sample.PackageName, row.LimitMinutes, sample.ForegroundMs)
			row.FreeByDefault = slices.Contains(desired.FreeByDefault, sample.PackageName)
		}
		rows = append(rows, row)
	}
	st.CountedMinutes = int(countedMs / 60000)
	st.UncountedMinutes = int(uncountedMs / 60000)
	return rows, st, nil
}

// blockedReason is why pkg cannot be used under ds, or "" when it can.
func blockedReason(ds *policy.DesiredState, pkg string, ownLimit int, usedMs int64) string {
	switch {
	case slices.Contains(ds.HiddenPackages, pkg):
		return BlockedByRule
	case slices.Contains(ds.PendingApproval, pkg):
		return BlockedPending
	case !slices.Contains(ds.SuspendedPackages, pkg):
		return notBlocked
	case ownLimit > 0 && int(usedMs/60000) >= ownLimit:
		return BlockedAppLimit
	case ds.SuspendReason != policy.ReasonNone:
		return ds.SuspendReason
	default:
		return BlockedByRule
	}
}
