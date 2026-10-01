package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Time request states (FR-28).
const (
	TimeRequestOpen     = "OPEN"
	TimeRequestGranted  = "GRANTED"
	TimeRequestDeclined = "DECLINED"
)

// MaxTimeRequestsPerDay bounds how often one profile can ask in a day. Every request wakes every
// parent's phone; a child who learns that a tap buzzes a parent's pocket will tap.
const MaxTimeRequestsPerDay = 3

var (
	// ErrTimeRequestOpen is a request while another one of the same day is still unanswered.
	ErrTimeRequestOpen = errors.New("a request for today is still waiting for an answer")
	// ErrTimeRequestsExhausted is a request past MaxTimeRequestsPerDay.
	ErrTimeRequestsExhausted = errors.New("no more requests today")
	// ErrTimeRequestNotFound is an id that is not a request of this profile.
	ErrTimeRequestNotFound = errors.New("no such time request")
	// ErrTimeRequestDecided is an answer to a request that already has one, or that belongs to a
	// day that is over.
	ErrTimeRequestDecided = errors.New("that request was already answered or is from another day")
)

// TimeRequest is a child's "Mehr Zeit erbitten" (FR-28).
type TimeRequest struct {
	ID             uuid.UUID  `json:"id"`
	ChildID        uuid.UUID  `json:"child_id"`
	Day            string     `json:"day"`
	Minutes        int        `json:"minutes"`
	Note           string     `json:"note"`
	State          string     `json:"state"`
	GrantedMinutes int        `json:"granted_minutes"`
	RequestedAt    time.Time  `json:"requested_at"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
}

// CreateTimeRequest records a request for a local day. The partial unique index is the arbiter of
// "one open request": two taps racing each other cannot both get in.
func (s *Store) CreateTimeRequest(ctx context.Context, childID, deviceID uuid.UUID, day string, minutes int, note string) (*TimeRequest, error) {
	var out *TimeRequest
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM time_requests WHERE child_id = $1 AND day = $2::date`,
			childID, day).Scan(&count); err != nil {
			return err
		}
		var open bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM time_requests WHERE child_id = $1 AND day = $2::date AND state = 'OPEN')`,
			childID, day).Scan(&open); err != nil {
			return err
		}
		if open {
			return ErrTimeRequestOpen
		}
		if count >= MaxTimeRequestsPerDay {
			return ErrTimeRequestsExhausted
		}
		r := &TimeRequest{ID: uuid.New(), ChildID: childID, Day: day, Minutes: minutes, Note: note, State: TimeRequestOpen}
		if err := tx.QueryRow(ctx,
			`INSERT INTO time_requests (id, child_id, device_id, day, minutes, note)
			 VALUES ($1, $2, $3, $4::date, $5, $6) RETURNING requested_at`,
			r.ID, childID, deviceID, day, minutes, note).Scan(&r.RequestedAt); err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return ErrTimeRequestOpen
			}
			return err
		}
		out = r
		return nil
	})
	return out, err
}

// TimeRequestsForDay is a profile's requests of one local day, newest first.
func (s *Store) TimeRequestsForDay(ctx context.Context, childID uuid.UUID, day string) ([]TimeRequest, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, child_id, to_char(day, 'YYYY-MM-DD'), minutes, note, state, COALESCE(granted_minutes, 0),
		        requested_at, decided_at
		   FROM time_requests WHERE child_id = $1 AND day = $2::date
		  ORDER BY requested_at DESC`, childID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimeRequest{}
	for rows.Next() {
		var r TimeRequest
		if err := rows.Scan(&r.ID, &r.ChildID, &r.Day, &r.Minutes, &r.Note, &r.State, &r.GrantedMinutes,
			&r.RequestedAt, &r.DecidedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DecideTimeRequest answers an open request of the given day. A grant gives minutes, or what was
// asked when minutes is 0, and adds them to that day's extra time in the same transaction — the
// answer and the time it gives can never disagree. Returns the day's new extra-time total (0 for a
// decline).
func (s *Store) DecideTimeRequest(ctx context.Context, childID, requestID uuid.UUID, day string, grant bool, minutes int, decidedBy uuid.UUID) (*TimeRequest, int, error) {
	state := TimeRequestDeclined
	if grant {
		state = TimeRequestGranted
	}
	var out TimeRequest
	total := 0
	err := s.tx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`UPDATE time_requests
			    SET state = $4, decided_at = NOW(), decided_by = $6,
			        granted_minutes = CASE WHEN $4 = 'GRANTED' THEN COALESCE(NULLIF($5::int, 0), minutes) END
			  WHERE id = $1 AND child_id = $2 AND day = $3::date AND state = 'OPEN'
			  RETURNING id, child_id, to_char(day, 'YYYY-MM-DD'), minutes, note, state,
			            COALESCE(granted_minutes, 0), requested_at, decided_at`,
			requestID, childID, day, state, minutes, decidedBy).
			Scan(&out.ID, &out.ChildID, &out.Day, &out.Minutes, &out.Note, &out.State, &out.GrantedMinutes,
				&out.RequestedAt, &out.DecidedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM time_requests WHERE id = $1 AND child_id = $2)`,
				requestID, childID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return ErrTimeRequestDecided
			}
			return ErrTimeRequestNotFound
		}
		if err != nil || !grant {
			return err
		}
		err = tx.QueryRow(ctx,
			`INSERT INTO bonus_minutes (child_id, day, minutes) VALUES ($1, $2::date, $3::int)
			 ON CONFLICT (child_id, day) DO UPDATE
			   SET minutes = bonus_minutes.minutes + $3::int, updated_at = NOW()
			   WHERE bonus_minutes.minutes + $3::int <= $4
			 RETURNING minutes`,
			childID, day, out.GrantedMinutes, MaxBonusMinutesPerDay).Scan(&total)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBonusTooLarge
		}
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return &out, total, nil
}
