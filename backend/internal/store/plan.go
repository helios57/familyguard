package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/helios57/familyguard/backend/internal/earned"
)

// PlanGroup is one group of a profile's daily plan (FR-22): the days and the window it runs in, and
// the earned time it is worth once every task in it is confirmed.
type PlanGroup struct {
	ID            uuid.UUID  `json:"id"`
	Title         string     `json:"title"`
	Weekdays      int        `json:"weekdays"` // bit 0 Monday … bit 6 Sunday
	StartsAt      string     `json:"starts_at"`
	EndsAt        string     `json:"ends_at"`
	EarnedMinutes int        `json:"earned_minutes"`
	Tasks         []PlanTask `json:"tasks"`
}

// PlanTask is one task in a group. Note is free text such as "10 min".
type PlanTask struct {
	ID    uuid.UUID `json:"id"`
	Title string    `json:"title"`
	Note  string    `json:"note"`
}

// Task states for one day. No row is "open".
const (
	TaskOpen      = "OPEN"
	TaskReported  = "REPORTED"
	TaskConfirmed = "CONFIRMED"
	TaskRejected  = "REJECTED"
)

// TaskDay is what happened to one task on one day.
type TaskDay struct {
	State      string     `json:"state"`
	ReportedAt *time.Time `json:"reported_at,omitempty"`
	DecidedAt  *time.Time `json:"decided_at,omitempty"`
}

// ErrTaskNotInPlan is a task id that is not an active task of this profile.
var ErrTaskNotInPlan = errors.New("that task is not in this profile's plan")

// GetPlan is a profile's active plan, groups and tasks in their order, never nil.
func (s *Store) GetPlan(ctx context.Context, childID uuid.UUID) ([]PlanGroup, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT g.id, g.title, g.weekdays, g.starts_at, g.ends_at, g.earned_minutes, t.id, t.title, t.note
		   FROM plan_groups g
		   LEFT JOIN plan_tasks t ON t.group_id = g.id AND t.retired_at IS NULL
		  WHERE g.child_id = $1 AND g.retired_at IS NULL
		  ORDER BY g.position, g.created_at, t.position, t.created_at`, childID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlanGroup{}
	for rows.Next() {
		var g PlanGroup
		var tid *uuid.UUID
		var ttitle, tnote *string
		if err := rows.Scan(&g.ID, &g.Title, &g.Weekdays, &g.StartsAt, &g.EndsAt, &g.EarnedMinutes,
			&tid, &ttitle, &tnote); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].ID != g.ID {
			g.Tasks = []PlanTask{}
			out = append(out, g)
		}
		if tid != nil {
			last := &out[len(out)-1]
			last.Tasks = append(last.Tasks, PlanTask{ID: *tid, Title: *ttitle, Note: *tnote})
		}
	}
	return out, rows.Err()
}

// ReplacePlan makes the profile's active plan exactly groups, in their order. A group or task that
// carries the id of an active one of this profile is updated in place — so its history (task days,
// credits) stays attached — one without an id is created, and every active one not named is retired.
// An id that belongs to another profile, or to nothing, is refused rather than silently created.
func (s *Store) ReplacePlan(ctx context.Context, childID uuid.UUID, groups []PlanGroup) ([]PlanGroup, error) {
	err := s.tx(ctx, func(tx pgx.Tx) error {
		existingGroups := map[uuid.UUID]bool{}
		rows, err := tx.Query(ctx,
			`SELECT id FROM plan_groups WHERE child_id = $1 AND retired_at IS NULL FOR UPDATE`, childID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			existingGroups[id] = true
		}
		rows.Close()

		keepGroups := []uuid.UUID{}
		for gi, g := range groups {
			gid := g.ID
			if gid == uuid.Nil {
				gid = uuid.New()
				if _, err := tx.Exec(ctx,
					`INSERT INTO plan_groups (id, child_id, title, weekdays, starts_at, ends_at, earned_minutes, position)
					 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
					gid, childID, g.Title, g.Weekdays, g.StartsAt, g.EndsAt, g.EarnedMinutes, gi); err != nil {
					return err
				}
			} else {
				if !existingGroups[gid] {
					return fmt.Errorf("%w: group %s is not in this profile's plan", ErrNotFound, gid)
				}
				if _, err := tx.Exec(ctx,
					`UPDATE plan_groups SET title = $2, weekdays = $3, starts_at = $4, ends_at = $5,
					        earned_minutes = $6, position = $7
					  WHERE id = $1`,
					gid, g.Title, g.Weekdays, g.StartsAt, g.EndsAt, g.EarnedMinutes, gi); err != nil {
					return err
				}
			}
			keepGroups = append(keepGroups, gid)

			existingTasks := map[uuid.UUID]bool{}
			trows, err := tx.Query(ctx,
				`SELECT id FROM plan_tasks WHERE group_id = $1 AND retired_at IS NULL`, gid)
			if err != nil {
				return err
			}
			for trows.Next() {
				var id uuid.UUID
				if err := trows.Scan(&id); err != nil {
					trows.Close()
					return err
				}
				existingTasks[id] = true
			}
			trows.Close()
			keepTasks := []uuid.UUID{}
			for ti, t := range g.Tasks {
				tid := t.ID
				if tid == uuid.Nil {
					tid = uuid.New()
					if _, err := tx.Exec(ctx,
						`INSERT INTO plan_tasks (id, group_id, title, note, position) VALUES ($1, $2, $3, $4, $5)`,
						tid, gid, t.Title, t.Note, ti); err != nil {
						return err
					}
				} else {
					if !existingTasks[tid] {
						return fmt.Errorf("%w: task %s is not in that group", ErrNotFound, tid)
					}
					if _, err := tx.Exec(ctx,
						`UPDATE plan_tasks SET title = $2, note = $3, position = $4 WHERE id = $1`,
						tid, t.Title, t.Note, ti); err != nil {
						return err
					}
				}
				keepTasks = append(keepTasks, tid)
			}
			if _, err := tx.Exec(ctx,
				`UPDATE plan_tasks SET retired_at = NOW()
				  WHERE group_id = $1 AND retired_at IS NULL AND NOT (id = ANY($2))`, gid, keepTasks); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx,
			`UPDATE plan_groups SET retired_at = NOW()
			  WHERE child_id = $1 AND retired_at IS NULL AND NOT (id = ANY($2))`, childID, keepGroups)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetPlan(ctx, childID)
}

// TaskDays is what happened to each task of a profile on one day; a task with no entry is open.
func (s *Store) TaskDays(ctx context.Context, childID uuid.UUID, day string) (map[uuid.UUID]TaskDay, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT task_id, state, reported_at, decided_at FROM task_days WHERE child_id = $1 AND day = $2::date`,
		childID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]TaskDay{}
	for rows.Next() {
		var id uuid.UUID
		var d TaskDay
		if err := rows.Scan(&id, &d.State, &d.ReportedAt, &d.DecidedAt); err != nil {
			return nil, err
		}
		out[id] = d
	}
	return out, rows.Err()
}

// TaskGroup is the active group an active task of this profile belongs to, or ErrTaskNotInPlan.
func (s *Store) TaskGroup(ctx context.Context, childID, taskID uuid.UUID) (*PlanGroup, error) {
	var g PlanGroup
	err := s.pool.QueryRow(ctx,
		`SELECT g.id, g.title, g.weekdays, g.starts_at, g.ends_at, g.earned_minutes
		   FROM plan_tasks t JOIN plan_groups g ON g.id = t.group_id
		  WHERE t.id = $1 AND g.child_id = $2 AND t.retired_at IS NULL AND g.retired_at IS NULL`,
		taskID, childID).Scan(&g.ID, &g.Title, &g.Weekdays, &g.StartsAt, &g.EndsAt, &g.EarnedMinutes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTaskNotInPlan
	}
	return &g, err
}

// ReportTask records the child's "Fertig" for a task on a day. A task already confirmed stays
// confirmed; a rejected one is reported again. Returns the resulting state.
func (s *Store) ReportTask(ctx context.Context, childID uuid.UUID, day string, taskID, deviceID uuid.UUID) (string, error) {
	var state string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO task_days (child_id, day, task_id, state, reported_at, reported_by)
		 VALUES ($1, $2::date, $3, 'REPORTED', NOW(), $4)
		 ON CONFLICT (child_id, day, task_id) DO UPDATE
		   SET state       = CASE WHEN task_days.state = 'CONFIRMED' THEN 'CONFIRMED' ELSE 'REPORTED' END,
		       reported_at = CASE WHEN task_days.state = 'CONFIRMED' THEN task_days.reported_at ELSE NOW() END,
		       reported_by = CASE WHEN task_days.state = 'CONFIRMED' THEN task_days.reported_by ELSE $4 END
		 RETURNING state`,
		childID, day, taskID, deviceID).Scan(&state)
	return state, err
}

// CreditChange is what a decision did to the day's credit for the task's group.
type CreditChange struct {
	Credited  int  `json:"credited_minutes,omitempty"`
	Withdrawn bool `json:"withdrawn,omitempty"`
}

// DecideTask confirms, rejects or undoes a task for a day, and keeps the group's credit in step in
// the same transaction: the group's last open task confirmed creates the day's credit (if the group
// is worth anything), and any decision that leaves the group incomplete withdraws it.
func (s *Store) DecideTask(ctx context.Context, childID uuid.UUID, day string, taskID uuid.UUID,
	decision string, by uuid.UUID) (CreditChange, error) {
	var change CreditChange
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var groupID uuid.UUID
		var worth int
		if err := tx.QueryRow(ctx,
			`SELECT g.id, g.earned_minutes FROM plan_tasks t JOIN plan_groups g ON g.id = t.group_id
			  WHERE t.id = $1 AND g.child_id = $2 AND t.retired_at IS NULL AND g.retired_at IS NULL
			  FOR UPDATE OF g`, taskID, childID).Scan(&groupID, &worth); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTaskNotInPlan
			}
			return err
		}
		switch decision {
		case TaskConfirmed, TaskRejected:
			if _, err := tx.Exec(ctx,
				`INSERT INTO task_days (child_id, day, task_id, state, decided_at, decided_by)
				 VALUES ($1, $2::date, $3, $4, NOW(), $5)
				 ON CONFLICT (child_id, day, task_id) DO UPDATE
				   SET state = EXCLUDED.state, decided_at = NOW(), decided_by = EXCLUDED.decided_by`,
				childID, day, taskID, decision, by); err != nil {
				return err
			}
		case TaskOpen:
			if _, err := tx.Exec(ctx,
				`DELETE FROM task_days WHERE child_id = $1 AND day = $2::date AND task_id = $3`,
				childID, day, taskID); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown decision %q", decision)
		}

		var tasks, confirmed int
		if err := tx.QueryRow(ctx,
			`SELECT count(*), count(d.task_id) FILTER (WHERE d.state = 'CONFIRMED')
			   FROM plan_tasks t
			   LEFT JOIN task_days d ON d.task_id = t.id AND d.child_id = $2 AND d.day = $3::date
			  WHERE t.group_id = $1 AND t.retired_at IS NULL`, groupID, childID, day).Scan(&tasks, &confirmed); err != nil {
			return err
		}
		complete := tasks > 0 && confirmed == tasks
		var active bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM earned_credits
			                 WHERE child_id = $1 AND group_id = $2 AND day = $3::date AND withdrawn_at IS NULL)`,
			childID, groupID, day).Scan(&active); err != nil {
			return err
		}
		switch {
		case complete && !active && worth > 0:
			if _, err := tx.Exec(ctx,
				`INSERT INTO earned_credits (id, child_id, group_id, day, minutes, confirmed_by)
				 VALUES ($1, $2, $3, $4::date, $5, $6)`, uuid.New(), childID, groupID, day, worth, by); err != nil {
				return err
			}
			change.Credited = worth
		case !complete && active:
			if _, err := tx.Exec(ctx,
				`UPDATE earned_credits SET withdrawn_at = NOW()
				  WHERE child_id = $1 AND group_id = $2 AND day = $3::date AND withdrawn_at IS NULL`,
				childID, groupID, day); err != nil {
				return err
			}
			change.Withdrawn = true
		}
		return nil
	})
	return change, err
}

// CreditedGroups is the groups of a profile that hold an active credit on a day, with its minutes.
func (s *Store) CreditedGroups(ctx context.Context, childID uuid.UUID, day string) (map[uuid.UUID]int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT group_id, minutes FROM earned_credits
		  WHERE child_id = $1 AND day = $2::date AND withdrawn_at IS NULL`, childID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]int{}
	for rows.Next() {
		var id uuid.UUID
		var m int
		if err := rows.Scan(&id, &m); err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
}

// EarnedCredits is a profile's active credits earned on or after since, for the balance.
func (s *Store) EarnedCredits(ctx context.Context, childID uuid.UUID, since string) ([]earned.Credit, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT to_char(day, 'YYYY-MM-DD'), minutes FROM earned_credits
		  WHERE child_id = $1 AND day >= $2::date AND withdrawn_at IS NULL
		  ORDER BY day, earned_at`, childID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []earned.Credit{}
	for rows.Next() {
		var c earned.Credit
		if err := rows.Scan(&c.Day, &c.Minutes); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EarnedSpentByDay is the earned time a profile's phones spent, per day since since, in whole minutes
// floored per day — the phones report it with their day totals.
func (s *Store) EarnedSpentByDay(ctx context.Context, childID uuid.UUID, since string) (map[string]int, error) {
	rows, err := s.pool.Query(ctx,
		// SUM over a BIGINT is NUMERIC, and NUMERIC / 60000 keeps the fraction — which Scan refuses
		// into an int. Cast back to BIGINT first, so the division is integer and floors like the
		// phone's own split.
		`SELECT to_char(u.day, 'YYYY-MM-DD'), (SUM(u.earned_ms)::bigint / 60000)::int
		   FROM usage_samples u JOIN devices d ON d.id = u.device_id
		  WHERE d.child_id = $1 AND u.day >= $2::date
		  GROUP BY u.day`, childID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var day string
		var m int
		if err := rows.Scan(&day, &m); err != nil {
			return nil, err
		}
		out[day] = m
	}
	return out, rows.Err()
}
