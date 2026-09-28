# Energy modes — Implementation Plan

> **For agentic workers:** executed inline by its author (superpowers:executing-plans); the owner
> approved the design on 2026-09-28 ("Looks good, go"). Steps use checkbox (`- [ ]`) syntax.

**Goal:** A phone that spends energy on FamilyGuard only while someone benefits from it: a
permanent connection only while the screen is on or Live runs, a push to wake it otherwise, a
lighter ad filter with the screen off — and a self-report that shows whether it worked.

**Spec:** `docs/superpowers/specs/2026-09-28-energy-modes-design.md` (FR-26, FR-27).

## Global constraints

- Latest versions of every library, resolved against the live registry; floor API 29.
- The public repo carries no server internals and no family names; nothing of anyone's Firebase
  project is committed.
- Nothing waits on a coroutine `delay` while the phone may sleep; wall-clock waits are alarms.
- Every probe is a value change, restored with `cp` and verified with `cmp`.
- Server first, then phones; watch CI and Release after every push.

## Review focus

1. A process restart between two samples (counters reset): no negative delta may reach a total.
2. A charging interval: not counted as battery used.
3. A bypass package that is not installed: `addDisallowedApplication` throws — the tunnel must
   still come up.
4. A default dialer or SMS app that is not a system app: it may carry ads, so it is not bypassed.
5. An older DPC's heartbeat without `energy`: records no sample and clears nothing.

---

## Phase 1 — energy self-report and the bypass list (0.6.26)

### Task 1: server — samples and hourly totals (FR-26.5)

**Files:** `backend/internal/store/migrations/0023_energy_samples.sql`, `backend/internal/store/energy.go`,
`backend/internal/energy/energy.go` (+ `_test.go`), `backend/internal/httpapi/deviceapi.go`
(heartbeat), `backend/internal/httpapi/energy.go`, `server.go` route, `tests/e2e/energy_test.go`.

- Heartbeat gains `energy`: `since` (RFC 3339, when the process started), `cpu_ms`, `rx_bytes`,
  `tx_bytes`, `stream_opens`, `events`, `polls`, `pushes`, `other_syncs`, and — nullable, reported
  from later phases — `active_ms`, `passive_ms`, `route_full_ms`, `route_dns_ms`. Counters are
  cumulative since `since`.
- Present → one row in `energy_samples` stamped with the server's time, with the heartbeat's
  `battery_level` and `charging` beside it. Absent → no row.
- `GET /devices/:id/energy?hours=24` (admins): `energy.Hourly(samples)` pairs consecutive samples of
  the same `since` (a new `since` starts a new run; never a negative delta), attributes each interval
  to the hour its end falls in, and sums per hour: `minutes`, `unplugged_minutes`, `battery_used`
  (percentage points dropped over intervals unplugged at both ends), `cpu_ms`, bytes, the counters.
  Plus a `total` over the window.
- Tests: Go unit (pairing, restart, charging, attribution); e2e (two heartbeats → deltas; an older
  heartbeat → no row; a restart → no negative; a guardian refused; another family's device 404).

### Task 2: fgctl and MCP

- `fgctl energy <device> [--hours N]`: a table per hour and the total line
  (*battery −x % per unplugged hour · FamilyGuard CPU y s/h · z wake-ups/h*). MCP `get_energy`.
- Tests beside the existing fgctl e2e ones.

### Task 3: console

- The device detail's state list gains *Energy (24 h)*: the total line, or *not reported* for a phone
  without samples. Chrome at 360 px in the existing console test style.

### Task 4: phone — the meter (FR-26.5)

**Files:** `android-dpc/.../energy/EnergyMeter.kt` (+ test), `net/ApiClient.kt`,
`sync/Synchronizer.kt`, `sync/ConnectionService.kt`.

- `EnergyMeter.countSync(why)`: `wake:connected` → stream_opens; other `wake:*` → events;
  `poll*` → polls; `push*` → pushes; anything else → other_syncs.
- `snapshot()` reads `Process.getElapsedCpuTime()`, `TrafficStats.getUidRx/TxBytes(myUid)` minus the
  values at process start, and `since` from `Process.getStartElapsedRealtime()`.
- The heartbeat carries it. Kotlin unit: classification, serialization with nulls omitted.

### Task 5: phone — the bypass list (FR-26.4)

**Files:** `filter/FilterBypass.kt` (+ test), `filter/AdFilterVpnService.kt`.

- `FilterBypass.packages(installed, systemPackages, defaultDialer, defaultSms)`: the fixed list
  (IMS services, Signal, Threema's four packages, Google Play services) ∩ installed, plus the default
  dialer and SMS app only when they are system apps. Each is excluded with
  `addDisallowedApplication`, and a package that disappears between the two calls is skipped.
- Emulator: the tunnel's uid ranges (`dumpsys connectivity`) exclude the Play services uid, and a
  browser's DNS is still filtered.

### Task 6: docs and release

IMPLEMENTATION_PLAN Phase 41, REQUIREMENTS status, 0.6.26, then a day of self-reports from both
phones as the baseline.

## Phase 2 — the modes and Live (outline)

`PowerMode.decide` (pure) + the stream closed in PASSIVE (the blocking read is released by
disconnecting from outside) + a 5-minute `setExactAndAllowWhileIdle` poll + the update check at most
every 6 h in PASSIVE; server `live_until`, `POST|DELETE /devices/:id/live` for admins and guardians,
audit, desired state; the console button and map follow; fgctl `live`, MCP `start_live`/`stop_live`;
phone location every 10 s during Live in a `location` foreground service. Emulator: forced Doze —
the poll wakes it; Live started from the console reaches a dozing phone within the poll.

## Phase 3 — push (outline)

Firebase project (owner approved), key in the secret store and mounted; server FCM HTTP v1 sender
behind `FCM_CREDENTIALS_FILE`, coalesced 10 s per phone, sent where the stream would have told an
open connection; phone `firebase-messaging` initialised at runtime from options in the desired state;
token in the heartbeat; `UNREGISTERED` drops it; safety poll 30 min when push works. Emulator with
Google APIs: push wakes a dozing phone.

## Phase 4 — DNS only with the screen off (outline)

After 5 min screen off and no media audio, re-establish with `RouteMode.DNS_ONLY`; screen on →
`FULL`; route time in the self-report.

## Rulings

- Phase 1: *the filter threads sleeping when idle* dropped — timers without a wake lock never wake a
  sleeping phone, and the emulator measured those threads at 0 s CPU after 0.6.25. Cost if wrong: a
  few CPU wake-ups per second while the screen is on.
- Phase 1: the energy card sits in the Activity tab (admins), not in the device card — the device card
  is read by guardians too, and the energy report is a diagnostic. Cost if wrong: one more tap.
- Phase 1: no pruning of `energy_samples` — one row per heartbeat is ~100 per phone per day, and the
  owner keeps data for at least a year. Cost if wrong: a few MB a year.
- Phase 3 must also amend CONCEPT.md §7, which lists FCM as deliberately not built.
- Phase 2: the update check is every 6 hours in both modes, not only in PASSIVE — its own wake-up was
  the cost, a phone in use rides it along on any sync, and *Update app* is the shortcut. Cost if
  wrong: a release reaches an idle phone up to 6 h later unless a parent presses *Update app*.
- Phase 2: the device view gained `stream_open` (the hub's count) — the socket table could not be the
  test's authority, because the HTTP client's idle keep-alive connection outlived the stream by 2 min.
  "Online" became stream-open OR a heartbeat within 11 minutes (was 3); the console says *resting*
  for online-without-stream. Cost if wrong: a phone that died shows online for up to 11 minutes.
- Phase 2: the command and Live of the device test ride one poll, and device tests get a 30-minute Go
  timeout — the first run spent 15 minutes and was killed on its last step.
- Phase 2: the stream is not closed from a broadcast receiver's thread — closing TLS writes to the
  socket, which Android refuses on the main thread.
