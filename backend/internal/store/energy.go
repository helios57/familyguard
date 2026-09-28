package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/energy"
)

// RecordEnergySample stores one heartbeat's energy report (FR-26.5), stamped with the server's time.
func (s *Store) RecordEnergySample(ctx context.Context, deviceID uuid.UUID, e energy.Sample) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO energy_samples (device_id, since, battery_level, charging, cpu_ms, rx_bytes, tx_bytes,
		    stream_opens, events, polls, pushes, other_syncs, active_ms, passive_ms, route_full_ms, route_dns_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		deviceID, e.Since, e.BatteryLevel, e.Charging, e.CPUMs, e.RxBytes, e.TxBytes,
		e.StreamOpens, e.Events, e.Polls, e.Pushes, e.OtherSyncs,
		e.ActiveMs, e.PassiveMs, e.RouteFullMs, e.RouteDNSMs)
	return err
}

// EnergySamples is the device's reports taken at or after from, oldest first.
func (s *Store) EnergySamples(ctx context.Context, deviceID uuid.UUID, from time.Time) ([]energy.Sample, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT at, since, battery_level, charging, cpu_ms, rx_bytes, tx_bytes, stream_opens, events,
		       polls, pushes, other_syncs, active_ms, passive_ms, route_full_ms, route_dns_ms
		  FROM energy_samples WHERE device_id = $1 AND at >= $2 ORDER BY at`, deviceID, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []energy.Sample{}
	for rows.Next() {
		var e energy.Sample
		var battery *int16
		if err := rows.Scan(&e.At, &e.Since, &battery, &e.Charging, &e.CPUMs, &e.RxBytes, &e.TxBytes,
			&e.StreamOpens, &e.Events, &e.Polls, &e.Pushes, &e.OtherSyncs,
			&e.ActiveMs, &e.PassiveMs, &e.RouteFullMs, &e.RouteDNSMs); err != nil {
			return nil, err
		}
		if battery != nil {
			v := int(*battery)
			e.BatteryLevel = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
