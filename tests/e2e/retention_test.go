package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestTelemetryIsKeptForAYearAndThenPruned holds the server's maintenance pass to the owner's ruling
// on what this product keeps: "No need to delete the data, keep it for at least 1 year". A child's
// location history was pruned after 30 days until 0.6.37, contradicting it, and nothing measured the
// window — the maintenance pass logged its counts and no test read them.
//
// Both sides of the line are seeded, because a prune that deletes nothing and a prune that deletes
// everything each pass half of this test.
func TestTelemetryIsKeptForAYearAndThenPruned(t *testing.T) {
	h := newHarness(t)
	f := enrolledFixture(t, h)

	for _, age := range []string{"300 days", "400 days"} {
		h.fixture(`INSERT INTO locations (id, device_id, latitude, longitude, captured_at)
			VALUES (gen_random_uuid(), '` + f.device.ID + `', 47.37, 8.54, NOW() - interval '` + age + `')`)
		h.fixture(`INSERT INTO audit_log (actor_type, action, occurred_at)
			VALUES ('SYSTEM', 'RETENTION_PROBE', NOW() - interval '` + age + `')`)
	}

	// The pass runs once at startup, so a restart is the way to run it now rather than in an hour.
	h.restart()
	for deadline := time.Now().Add(15 * time.Second); !h.logs.contains(`"msg":"maintenance"`); {
		if time.Now().After(deadline) {
			t.Fatalf("the restarted server never logged its maintenance pass:\n%s", h.logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}

	out, err := runPsqlOn(3*psqlBudget, h.dbName, `SELECT
		'locations=' || count(*) FILTER (WHERE captured_at > NOW() - interval '1 year') || '/'
		             || count(*) FILTER (WHERE captured_at < NOW() - interval '1 year')
		FROM locations WHERE device_id = '`+f.device.ID+`'
		UNION ALL SELECT
		'audit=' || count(*) FILTER (WHERE occurred_at > NOW() - interval '1 year') || '/'
		         || count(*) FILTER (WHERE occurred_at < NOW() - interval '1 year')
		FROM audit_log WHERE action = 'RETENTION_PROBE'`)
	if err != nil {
		t.Fatalf("reading back: %v\n%s", err, out)
	}
	got := string(out)
	// "young/old": the 300-day row survives, the 400-day row is gone.
	for _, want := range []string{"locations=1/0", "audit=1/0"} {
		if !strings.Contains(got, want) {
			t.Errorf("after the maintenance pass want %s (kept within a year / older than a year), got:\n%s", want, got)
		}
	}
	if !h.logs.contains(`"locations_pruned":1`) || !h.logs.contains(`"audit_pruned":1`) {
		t.Errorf("the maintenance log line does not count the one row it pruned from each table:\n%s", h.logs.String())
	}
}
