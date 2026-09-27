# Phase 2 — Pause and today's time: Implementation Plan

> **For agentic workers:** executed inline by its author (superpowers:executing-plans), as the owner
> asked for phase 1. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A parent or guardian can pause a profile's phones — everything paused except the phone's
critical packages and the family's always-usable communication apps — and can add or take away
today's time, from the guardian window, the API, `fgctl` and MCP.

**Architecture:** Pause is a policy field (`policies.paused`) that both engines read, like bedtime:
a state, never a command, so it survives reboots and an offline phone and ends only when someone
unpauses. Today's time becomes a signed adjustment on the existing per-day row (`bonus_minutes`),
clamped so the day's budget stays within 0 … 1440 minutes; the engine gains `daily_limit_minutes` in
its output so "0 minutes left" is never mistaken for "no limit".

**Tech Stack:** Go (Gin, pgx), Kotlin DPC, vanilla-JS console, e2e against real PostgreSQL + Chrome,
and the `familyguard37` emulator for the device half.

**Spec:** `docs/superpowers/specs/2026-09-27-daily-plan-design.md` §4 and §6; FR-21.

## Global Constraints

Same as phase 1 (`2026-09-27-phase1-roles-and-rights.md`): push to `main`, the internals check on
every staged diff, latest versions, calibrate every test red on a value change with `cp`/`cmp`,
GitOps-only deploy verified by digest, never print a secret, console English / guardian view and
phone German.

## Rulings made while planning

- **The communication apps a pause leaves running are the existing FR-5.9 always-usable list**
  (WhatsApp, WhatsApp Business, Threema ×3, Signal, Audible) plus the critical packages (dialer, SMS,
  contacts, settings, the phone's own launcher and keyboards). The spec proposed a separate
  admin-kept list; the family already decided this exact list on 2026-09-20 and it already travels to
  the phone in every policy. A second list would be a second place to forget an app. Cost if wrong:
  one list added later.
- **Pause is honoured in tracking-only mode**, like LOCK_NOW: it is an explicit parent act, not a
  schedule.
- **Pause suspends "always free" and preinstalled-free apps too** (FR-5.8 ALLOW, FR-5.10): the spec
  says everything except critical and communication.
- **A reduction never takes the day below 0**, and an addition never above 1440: the stored
  adjustment is clamped to `[-limit, 1440 - limit]`, so `+15` after a large reduction visibly gives
  15 minutes back instead of vanishing into a negative sum.

## Review Focus

1. A pause while the phone is offline — the phone keeps it through a reboot (it is in the cached Input).
2. "0 minutes left today" after a reduction must read as a limit reached, never as "no daily limit" —
   on the phone, in the console and in the guardian window.
3. A guardian who pauses her own profile's phone can still call and message.
4. Unpausing restores exactly the previous state (bedtime still applies if it is bedtime).
5. A reduction on a profile with no daily limit is refused with a sentence, not a 500.

---

### Task 1: Engine — pause and the signed day adjustment (Go + Kotlin + vectors)

**Files:** `backend/internal/policy/engine.go`, `engine_test.go`, `vectors.json`;
`android-dpc/.../enforce/EnforcementEngine.kt`, `EnforcementEngineVectorsTest.kt` (count 51 → 58).

- `Settings.Paused bool json:"paused"`; `ReasonPaused = "PAUSED"`.
- Precedence of the reason: `PAUSED` > `BEDTIME` > `QUOTA`.
- Paused: every installed app that can be opened is suspended, ALLOW and free-by-default included,
  critical/always-usable removed last as always. Blocked apps stay hidden. Honoured in tracking-only.
- Adjustment: `quota = max(0, limit + bonus)` when `limit > 0` and `bonus != 0` on `bonus_day`;
  `quotaReached = limit > 0 && used >= quota`; `DesiredState.DailyLimitMinutes` (new, the plain limit).
- Vectors: pause with a game, an ALLOW app, a free camera, the dialer, WhatsApp; pause beats bedtime;
  pause in tracking-only; unpause restores; negative bonus lowers the quota; floor at 0 means limit
  reached with 0 used; negative bonus on another day ignored.
- Must-cover list gains `FR-21`.
- Probes: pause ignored; ALLOW exempt under pause; floor removed (quota 0 → unlimited); critical not
  re-added under pause.

### Task 2: Server — state, endpoints, audit

**Files:** migration `0018_pause_and_day_adjustment.sql`; `store/policy.go` (Paused fields),
`store/screentime.go` (signed `AdjustDay`), `enforce/resolve.go`, `httpapi/screentime.go`
(signed grant, `blockedReason` PAUSED), new `httpapi/pause.go`, `server.go` (route, guardian
allowlist), `roles_test.go` allowlist, e2e `tests/e2e/pause_test.go`, `audit_test.go`.

- `POST /children/:id/pause {"paused": bool}` → 200 `{paused, paused_at}`; everyone; audited
  `PROFILE_PAUSED` / `PROFILE_UNPAUSED`; bumps the policy version and notifies the phones.
- `POST /children/:id/bonus {"minutes": ±n}`: `n` in `[-1440, 1440] \ {0}`; no limit → 409
  `no_daily_limit`; stored sum clamped to `[-limit, 1440 - limit]`; audited `BONUS_GRANTED` (n > 0)
  or `TIME_REDUCED` (n < 0) with the resulting sum.
- `day_limits.bonus_minutes` and `bonus_minutes.minutes` CHECKs widened to the signed range.
- e2e: pause suspends a game and an ALLOW app but not the dialer or WhatsApp; unpause restores;
  pause survives a server restart; a guardian may pause; reduction lowers the quota and floors at 0
  with `daily_limit_minutes` still set; `+15` after an over-reduction gives 15 back; reduction with no
  limit is a 409; audit rows with detail.

### Task 3: Phone — say it

**Files:** `TodayReport.kt` (`Block.PAUSED`, limit presence from `dailyLimitMinutes`),
`RecoveryActivity.kt`, `ConnectionService.kt` (`whyPaused`, `pausedNotice`), strings EN/DE,
unit tests.

- German: blocked-screen line *"Deine Eltern haben dein Handy gesperrt. Anrufen und Nachrichten gehen
  weiter."*; notice title *"Handy gesperrt"*; Today screen *"Gesperrt von deinen Eltern"*.
- A reduction shows as *"45 von 60 Minuten (−15 heute)"*.

### Task 4: Real phone — the emulator

**Files:** `tests/e2e/emulator_pause_test.go` + `tests/android/pause.sh` (modelled on
`remote-adb.sh`: allow_debugging on, so adb survives the first policy).

- Enrol `familyguard37`, install `fixture-app`, pause from the API, wait for the phone's next
  applied policy, read `dumpsys package` — the fixture app suspended, the dialer not; unpause — not
  suspended. Exit 2 when no emulator is available (NOT MEASURED, never a pass).

### Task 5: Console — the guardian window acts

**Files:** `app.js` (guardian cards: *Sperren/Entsperren* with a second tap, `−15 · +15 · +30`,
state line *"Gesperrt"*; admins get the guardian view as a first tab *Today*), `app.css`,
`tests/e2e/roles_console_test.go` (extend).

### Task 6: CLI and MCP

**Files:** `backend/cmd/fgctl/commands.go`, `mcp.go`, tests; `pause <child>`, `unpause <child>`,
`time <child> <±minutes>`; MCP tools `pause_profile`, `adjust_time_today`.

### Task 7: Docs, release 0.6.19, deploy, verify

FR-21 as built; Phase 35 with its calibration table; version 0.6.19 / 28; the phase-1 recipe;
live read-back; `UPDATE_APP` to the family phone.
