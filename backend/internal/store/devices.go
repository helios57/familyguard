package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// scanDevice reads one device row selected with deviceCols.
//
// Every device query goes through it. The column list and the scan list are then a single fact
// stated once: adding a column to deviceCols without adding it here does not compile, whereas the
// previous shape — the same Scan written out at four call sites — accepted a two-column addition
// at four places minus one and failed only when Postgres saw the mismatch at runtime.
func scanDevice(row pgx.Row) (*Device, error) {
	var d Device
	if err := row.Scan(&d.ID, &d.ChildID, &d.Name, &d.Model, &d.OSVersion, &d.Locked,
		&d.CriticalPackages, &d.EnrolledAt, &d.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &d, nil
}

const deviceCols = `id, child_id, name, model, os_version, locked, critical_packages, enrolled_at, created_at`

// CreateDevice registers a device slot with no enrollment credential yet.
//
// Minting the token is ResetEnrollment's job, and there is exactly one path that does it, because
// the plaintext token is never stored: only its hash is. That means "show me the QR for this
// device" cannot be a read — the value it would have to display does not exist anywhere — so the
// console asks for a fresh one, and the QR a parent is looking at is always one that will work.
// A device created here and never provisioned simply has nothing to enroll with.
func (s *Store) CreateDevice(ctx context.Context, childID uuid.UUID, name string) (*Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`INSERT INTO devices (id, child_id, name) VALUES ($1, $2, $3) RETURNING `+deviceCols,
		uuid.New(), childID, name))
}

// ResetEnrollment issues a fresh enrollment token for a device that has not enrolled, or that is
// being re-provisioned. It also clears the device token, so the old credential stops working the
// moment a new QR is generated.
func (s *Store) ResetEnrollment(ctx context.Context, deviceID uuid.UUID, tokenHash []byte, expiresAt time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE devices
		    SET enrollment_token_hash = $2, enrollment_expires_at = $3,
		        device_token_hash = NULL, enrolled_at = NULL
		  WHERE id = $1`, deviceID, tokenHash, expiresAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// EnrollmentResult is what a successful token exchange yields.
type EnrollmentResult struct {
	Device   Device
	ChildID  uuid.UUID
	Recovery RecoverySecret
}

// ConsumeEnrollment exchanges an enrollment token for a device token. The UPDATE both matches and
// clears the enrollment hash in a single statement, so a replayed token affects zero rows and gets
// ErrNotFound — the single-use property is enforced by the database, not by a read-then-write in
// application code that two concurrent requests could both pass.
//
// The expiry is compared in the same statement for the same reason.
//
// The two NULLs in the SET clause are redundant on purpose, and the redundancy is measured: with
// enrollment_token_hash left intact a replay is still refused, because enrollment_expires_at is
// NULL and `NULL > NOW()` is not true; with enrollment_expires_at left intact it is still refused,
// because the hash no longer matches. TestEnrollmentCredentialsAreSingleUse only goes red when
// BOTH are removed. So deleting either line as "the other one already covers it" is a change no
// test will object to, and the one that survives becomes a single point of failure.
func (s *Store) ConsumeEnrollment(ctx context.Context, enrollHash, deviceTokenHash []byte, rec RecoverySecret, model, osVersion string, criticalPackages []string) (*EnrollmentResult, error) {
	if criticalPackages == nil {
		criticalPackages = []string{}
	}
	dev, err := scanDevice(s.pool.QueryRow(ctx,
		`UPDATE devices
		    SET enrollment_token_hash = NULL,
		        enrollment_expires_at = NULL,
		        enrolled_at           = NOW(),
		        device_token_hash     = $2,
		        recovery_code         = $3,
		        recovery_salt         = $4,
		        recovery_iterations   = $5,
		        recovery_hash         = $6,
		        model                 = $7,
		        os_version            = $8,
		        critical_packages     = $9
		  WHERE enrollment_token_hash = $1
		    AND enrollment_expires_at > NOW()
		 RETURNING `+deviceCols,
		enrollHash, deviceTokenHash, rec.Code, rec.Salt, rec.Iterations, rec.Hash, model, osVersion,
		criticalPackages))
	if err != nil {
		return nil, err
	}
	out := EnrollmentResult{Device: *dev, ChildID: dev.ChildID, Recovery: rec}

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO device_state (device_id, last_seen_at) VALUES ($1, NOW())
		 ON CONFLICT (device_id) DO UPDATE SET last_seen_at = NOW()`, out.Device.ID); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeviceByTokenHash authenticates a device. A device that has been un-enrolled has a NULL hash and
// therefore matches nothing.
func (s *Store) DeviceByTokenHash(ctx context.Context, tokenHash []byte) (*Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE device_token_hash = $1`, tokenHash))
}

// GetDevice returns one device.
func (s *Store) GetDevice(ctx context.Context, id uuid.UUID) (*Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE id = $1`, id))
}

// DeviceWithState pairs a device with its last reported state.
type DeviceWithState struct {
	Device
	State DeviceState `json:"state"`
	// Enrolled reports whether the device has completed provisioning. A device slot that only has
	// a pending QR is listed, because hiding it would make a failed provisioning invisible.
	Enrolled bool `json:"enrolled"`
}

// ListDevices returns every device, optionally filtered to one child, with the derived online flag.
func (s *Store) ListDevices(ctx context.Context, childID *uuid.UUID, offlineAfter time.Duration) ([]DeviceWithState, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT d.id, d.child_id, d.name, d.model, d.os_version, d.locked, d.critical_packages,
		        d.enrolled_at, d.created_at,
		        COALESCE(s.battery_level, NULL), COALESCE(s.charging, NULL), COALESCE(s.screen_on, NULL),
		        COALESCE(s.connectivity, ''), COALESCE(s.policy_version, 0), s.last_seen_at
		   FROM devices d
		   LEFT JOIN device_state s ON s.device_id = d.id
		  WHERE ($1::uuid IS NULL OR d.child_id = $1)
		  ORDER BY d.created_at`, childID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cutoff := time.Now().UTC().Add(-offlineAfter)
	out := []DeviceWithState{}
	for rows.Next() {
		var d DeviceWithState
		if err := rows.Scan(&d.ID, &d.ChildID, &d.Name, &d.Model, &d.OSVersion, &d.Locked,
			&d.CriticalPackages, &d.EnrolledAt, &d.CreatedAt,
			&d.State.BatteryLevel, &d.State.Charging, &d.State.ScreenOn,
			&d.State.Connectivity, &d.State.PolicyVersion, &d.State.LastSeenAt); err != nil {
			return nil, err
		}
		d.State.DeviceID = d.ID
		d.State.Online = d.State.LastSeenAt != nil && d.State.LastSeenAt.After(cutoff)
		d.Enrolled = d.EnrolledAt != nil
		out = append(out, d)
	}
	return out, rows.Err()
}

// RenameDevice changes the display name.
func (s *Store) RenameDevice(ctx context.Context, id uuid.UUID, name string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE devices SET name = $2 WHERE id = $1`, id, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteDevice removes a device and everything hanging off it.
func (s *Store) DeleteDevice(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM devices WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecoveryCode returns the plaintext offline recovery code for one device, for display to an
// authenticated parent. It is per-device by construction: there is no query that returns a code
// shared across devices, because no such value exists.
func (s *Store) RecoveryCode(ctx context.Context, deviceID uuid.UUID) (string, error) {
	var code *string
	err := s.pool.QueryRow(ctx, `SELECT recovery_code FROM devices WHERE id = $1`, deviceID).Scan(&code)
	if err != nil {
		return "", mapErr(err)
	}
	if code == nil {
		return "", ErrNotFound
	}
	return *code, nil
}

// TouchDevice records that the device was heard from, and stores the telemetry it carried.
func (s *Store) TouchDevice(ctx context.Context, deviceID uuid.UUID, st DeviceState) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO device_state (device_id, battery_level, charging, screen_on, connectivity, policy_version, last_seen_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
		 ON CONFLICT (device_id) DO UPDATE SET
		     battery_level  = COALESCE(EXCLUDED.battery_level, device_state.battery_level),
		     charging       = COALESCE(EXCLUDED.charging, device_state.charging),
		     screen_on      = COALESCE(EXCLUDED.screen_on, device_state.screen_on),
		     connectivity   = COALESCE(NULLIF(EXCLUDED.connectivity, ''), device_state.connectivity),
		     policy_version = GREATEST(EXCLUDED.policy_version, device_state.policy_version),
		     last_seen_at   = NOW(),
		     updated_at     = NOW()`,
		deviceID, st.BatteryLevel, st.Charging, st.ScreenOn, st.Connectivity, st.PolicyVersion)
	return err
}

// SetLocked records or clears the parent's explicit lock. It is symmetric by construction — the
// same call that sets it clears it — so there is no lock the console cannot undo (FR-9.2).
func (s *Store) SetLocked(ctx context.Context, deviceID uuid.UUID, locked bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE devices SET locked = $2 WHERE id = $1`, deviceID, locked)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCriticalPackages stores what the device reported as unsuspendable on its own hardware. An
// empty report is stored as empty rather than rejected: the engine unions this with its built-in
// list, so the worst case of a device that reports nothing is the built-in floor, never less.
func (s *Store) SetCriticalPackages(ctx context.Context, deviceID uuid.UUID, pkgs []string) error {
	if pkgs == nil {
		pkgs = []string{}
	}
	_, err := s.pool.Exec(ctx, `UPDATE devices SET critical_packages = $2 WHERE id = $1`, deviceID, pkgs)
	return err
}

// GetDeviceState reads the last known state with the derived online flag.
func (s *Store) GetDeviceState(ctx context.Context, deviceID uuid.UUID, offlineAfter time.Duration) (*DeviceState, error) {
	var st DeviceState
	err := s.pool.QueryRow(ctx,
		`SELECT device_id, battery_level, charging, screen_on, connectivity, policy_version, last_seen_at
		   FROM device_state WHERE device_id = $1`, deviceID).
		Scan(&st.DeviceID, &st.BatteryLevel, &st.Charging, &st.ScreenOn, &st.Connectivity,
			&st.PolicyVersion, &st.LastSeenAt)
	if err != nil {
		return nil, mapErr(err)
	}
	st.Online = st.LastSeenAt != nil && st.LastSeenAt.After(time.Now().UTC().Add(-offlineAfter))
	return &st, nil
}

// DeviceIDsForChild lists the enrolled devices of one child, used to fan a policy change out.
// Several devices per child is the normal case, not an edge one (FR-11.2): a policy change has to
// reach every one of them, and a caller that assumed a single device would silently leave the
// second phone running the previous policy.
func (s *Store) DeviceIDsForChild(ctx context.Context, childID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM devices WHERE child_id = $1 AND device_token_hash IS NOT NULL`, childID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RecoveryMaterial returns the salted hash material for a device, for the DPC to embed.
func (s *Store) RecoveryMaterial(ctx context.Context, deviceID uuid.UUID) (*RecoverySecret, error) {
	var rec RecoverySecret
	var salt, hash []byte
	var iter *int
	err := s.pool.QueryRow(ctx,
		`SELECT recovery_salt, recovery_iterations, recovery_hash FROM devices WHERE id = $1`, deviceID).
		Scan(&salt, &iter, &hash)
	if err != nil {
		return nil, mapErr(err)
	}
	if salt == nil || hash == nil || iter == nil {
		return nil, ErrNotFound
	}
	rec.Salt, rec.Hash, rec.Iterations = salt, hash, *iter
	return &rec, nil
}

// RecordRecoveryEvent stores a reported unlock attempt (FR-12.5). Both outcomes are recorded: a
// store that only kept successes would make a brute-force attempt invisible.
func (s *Store) RecordRecoveryEvent(ctx context.Context, deviceID uuid.UUID, succeeded bool, occurredAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO recovery_events (id, device_id, succeeded, occurred_at) VALUES ($1, $2, $3, $4)`,
		uuid.New(), deviceID, succeeded, occurredAt)
	return err
}

// ListRecoveryEvents returns the most recent recovery attempts for a device.
func (s *Store) ListRecoveryEvents(ctx context.Context, deviceID uuid.UUID, limit int) ([]RecoveryEvent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, device_id, succeeded, occurred_at, reported_at
		   FROM recovery_events WHERE device_id = $1 ORDER BY occurred_at DESC LIMIT $2`,
		deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecoveryEvent{}
	for rows.Next() {
		var e RecoveryEvent
		if err := rows.Scan(&e.ID, &e.DeviceID, &e.Succeeded, &e.OccurredAt, &e.ReportedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
