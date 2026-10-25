# Contacts only: strangers cannot call a child — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** With a per-child switch on, the phone rejects every incoming call that is not from one of
its contacts or a family number (no ring, no call-log entry, no missed-call notification), and the
parent sees each rejected call and whether the protection is really active.

**Architecture:** The switch is a policy column and the family numbers a family column; both reach
the phone in the desired state (passed through both engines, held together by `vectors.json`). The
DPC answers Telecom from a `CallScreeningService`; the decision is a pure function. The phone reports
whether it holds the call-screening role in the heartbeat (the `alarm_full_screen` pattern) and the
calls it rejected through a persistent outbox, which the server stores per event for one year.

**Tech Stack:** Go 1.27 + pgx (server), Kotlin/Android (DPC, minSdk 29, `android.telecom`,
`android.app.role.RoleManager`), vanilla JS console, Go e2e harness with real Postgres, real Chrome
and a real emulator with a simulated modem.

**Spec:** `docs/superpowers/specs/2026-10-25-contacts-only-blocker-design.md` (approved 2026-10-26).

## Global Constraints

- Public repo: no family data, no server internals. Test numbers are `+1 555 01xx` (`+1555010NN`).
  The house hygiene grep must be clean on every staged diff.
- Release **0.6.47, versionCode 56**, image tag 0.6.47; FR-31; IMPLEMENTATION_PLAN Phase 62.
- Switch off by default. Name in the console: **"Nur Kontakte dürfen anrufen"**. JSON:
  `calls_contacts_only`.
- Family numbers: one list per family, admins only, at most **10**, stored E.164 (`+` and 8–15
  digits). Input `079…` / `0041…` / `+41…` / spaces, dashes, dots, parentheses accepted; national
  `0` is read as Switzerland (`+41`). JSON: `family_numbers`.
- READ_CONTACTS is **not** requested. Outgoing calls are never touched.
- Rejected calls: number (as the phone saw it, E.164 when it could be formed), time, device; kept
  **365 days**.
- Console states: amber **"Anrufschutz: wartet auf Freigabe am Handy"** (switch on, role never held),
  red **"Anrufschutz aus"** (switch on, role was held and is not now). Aktivität: **"Abgewiesene
  Anrufe"**. Setup checklist: hidden numbers *"nicht prüfbar"*.
- The server refuses the switch on (409 `devices_outdated`) while a phone of the child reports a
  build below **56** (a phone with no reported build does not block) — the on-at-0 precedent.
- German UI strings, Swiss spelling (no ß). Latest versions of anything touched. Push straight to
  `main`; server before phones; watch CI and Release runs; argocd commit is the deploy.
- Calibrate every new test by breaking a value it guards, never by deleting a symbol.

## Review Focus

1. An **outgoing** call (API 30+ passes them to the screening service) must never be rejected — Task 5 test `CallDecisionTest.outgoingIsAlwaysAllowed`.
2. A family number arriving in national form (`0791…`) while the list holds `+41791…` must ring — Task 5 test `CallDecisionTest.familyNumberInNationalFormPasses` (the normaliser is injected; the Android one is `PhoneNumberUtils.formatNumberToE164` with the SIM country).
3. Switch on, but the phone has no state yet (null cache, first boot) must let calls through, not reject them — Task 5 test `CallDecisionTest.noStateAllowsEverything`.
4. A rejected-call report re-sent after a lost response must not be stored twice — Task 2 test `TestRejectedCallReportIsIdempotent` (the phone sends a client id per call; unique `(device_id, client_id)`).
5. Turning the switch off must clear `disallow_config_default_apps` only if FamilyGuard set it, and never touch it on API < 34 — Task 6 test `CallGuardRestrictionTest`.

---

### Task 1: Requirement and spec

**Files:** Modify `REQUIREMENTS.md` (new `### FR-31` after FR-30), add the approved spec.

FR-31.1 switch per child (+ 409 for old phones) · FR-31.2 non-contacts rejected silently, outgoing
and contacts untouched, family numbers always pass · FR-31.3 family numbers (admins, ≤10, E.164) ·
FR-31.4 role: granted once on the phone, kept (API 34+ restriction), reported; amber/red states ·
FR-31.5 rejected calls reported, shown in Aktivität, kept one year · FR-31.6 not enforced and said so:
hidden numbers (phone app switch, *nicht prüfbar*), SMS and WhatsApp (owner, 2026-10-26).

- [ ] `RequirementCitationsTest` (android unit) and the Go citation test stay green with the spec citing FR-31.

### Task 2: Server — schema, store, routes

**Files:** Create `backend/internal/store/migrations/0031_contacts_only_calls.sql`,
`backend/internal/httpapi/calls.go`, `tests/e2e/calls_test.go`. Modify `store/models.go`,
`store/policy.go`, `store/family.go`, `store/devices.go`, `store/telemetry.go` (or a new
`store/calls.go`), `httpapi/parents.go`, `httpapi/deviceapi.go`, `httpapi/server.go`,
`cmd/server/main.go` (prune).

**Interfaces — Produces:**
- `policies.calls_contacts_only BOOLEAN NOT NULL DEFAULT FALSE`; `Policy.CallsContactsOnly` `json:"calls_contacts_only"`; `PolicyUpdate.CallsContactsOnly *bool`; PATCH field `calls_contacts_only`.
- `families.family_numbers TEXT[] NOT NULL DEFAULT '{}'`; `Store.FamilyNumbers(ctx) ([]string, error)` (never nil), `Store.SetFamilyNumbers(ctx, []string) error` (bumps every policy); `GET/PUT /family/family-numbers` (admins) body `{"numbers":[…]}`; `func normalizeFamilyNumber(raw string) (string, bool)`.
- `device_state.call_screening BOOLEAN` (nil = not said), `device_state.call_screening_held_at TIMESTAMPTZ` (first time it was reported true; never cleared); `DeviceState.CallScreening *bool` `json:"call_screening,omitempty"`, `DeviceState.CallScreeningHeldAt *time.Time` `json:"call_screening_held_at,omitempty"`; heartbeat field `call_screening`.
- `rejected_calls(id, device_id → devices ON DELETE CASCADE, client_id TEXT, number TEXT, rejected_at TIMESTAMPTZ, UNIQUE(device_id, client_id))`; `POST /device/rejected-calls` body `{"calls":[{"id","number","at"}]}` (≤100 per request) → `{"accepted":n}`; `GET /devices/:id/rejected-calls?days=N` (everyone who sees Aktivität) → `{"calls":[{"number","at"}]}`; `Store.PruneRejectedCalls(ctx, olderThan)`, run with the location prune at 365 days.
- 409 `devices_outdated` when PATCH sets `calls_contacts_only: true` and a phone of the child reports `0 < app_version_code < 56` (`firstBuildWithCallScreening = 56`).

- [ ] e2e tests first: `TestFamilyNumbersAreNormalisedAndAdminOnly` (`079 123 45 67`→`+41791234567`, `0041 79…`, `+1 555 0101`; `12`, 11 numbers → 400; guardian 403), `TestContactsOnlySwitchReachesThePhone` (PATCH → desired state carries `calls_contacts_only` and `family_numbers`), `TestContactsOnlyIsRefusedWhileAPhoneIsOutdated`, `TestRejectedCallReportIsIdempotent`, `TestCallScreeningHeldIsRememberedOnceSeen`.
- [ ] Run → red. Implement. Green. Calibrate: normaliser keeps a leading `0`; `ON CONFLICT DO NOTHING` removed; `held_at` overwritten with NULL — each reddens its test.

### Task 3: Engines pass the two fields through

**Files:** `backend/internal/policy/engine.go`, `vectors.json`, `engine_test.go`; Kotlin `enforce/EnforcementEngine.kt`; `backend/internal/enforce/resolve.go`.

**Produces:** `Settings.CallsContactsOnly bool` `json:"calls_contacts_only"`, `Settings.FamilyNumbers []string` `json:"family_numbers"`; `DesiredState.CallsContactsOnly` / `DesiredState.FamilyNumbers` (sorted, never null) with the same JSON names; Kotlin `Settings.callsContactsOnly`, `Settings.familyNumbers`, `DesiredState.callsContactsOnly`, `DesiredState.familyNumbers`.

Rule: passed through in every mode, including tracking-only and pause (a stranger is a stranger
while paused). Not a user restriction (Task 6 owns that).

- [ ] Vectors `contacts_only_passes_through`, `contacts_only_survives_pause_and_tracking_only` → red in both engines, then green; every existing vector unchanged.

### Task 4: fgctl, MCP and console

**Files:** `backend/cmd/fgctl/{commands.go,mcp.go,mcp_console.go,mcp_coverage_test.go}`, console `rules.js` (switch), `family.js` (Familie-Nummern card, admins), `activity.js` (Abgewiesene Anrufe), `overview.js` (amber/red), a setup-checklist line; `tests/e2e/calls_console_test.go`, `tests/e2e/calls_cli_test.go`.

**Produces:** `fgctl family-numbers [set N…]`; MCP `get_family_numbers`, `set_family_numbers`, `get_rejected_calls {device_id, days}`; `set_policy` carries `calls_contacts_only`.

- [ ] `TestMCPCoversEveryParentRoute` and the policy↔console parity test red until the tools and the switch exist; real-Chrome console test asserts the switch, the family numbers card, the amber card text and an Abgewiesene-Anrufe row.

### Task 5: DPC — the decision and the screening service

**Files:** Create `android-dpc/.../calls/CallDecision.kt`, `calls/CallGuardService.kt` (the
`CallScreeningService`), `calls/RejectedCallLog.kt` (encrypted, like `SessionLog`);
`AndroidManifest.xml` (service with `BIND_SCREENING_SERVICE`, intent filter
`android.telecom.CallScreeningService`); tests `CallDecisionTest`, `RejectedCallLogTest`.

**Produces:** `object CallDecision { fun decide(state: DesiredState?, incoming: Boolean, number: String?, normalise: (String) -> String?): Verdict }` with `enum Verdict { ALLOW, REJECT }`; `RejectedCallLog.add(number, atMillis)`, `pending(): List<RejectedCall>`, `acknowledge(ids)`.

Rules: null state, switch off, or not incoming → ALLOW. Number null/blank → ALLOW (Telecom does not
pass hidden numbers anyway; never reject on missing data). Normalised number in `familyNumbers` →
ALLOW. Otherwise REJECT: `setDisallowCall(true).setRejectCall(true).setSkipCallLog(true).setSkipNotification(true)` and log it. The service reads the state from `PolicyCache`, answers within Telecom's 5 s, and on any exception answers ALLOW.

- [ ] Tests (incl. Review Focus 1–3) red against a stub returning ALLOW, then green.

### Task 6: DPC — role, restriction, report, upload

**Files:** Create `calls/CallGuard.kt` (role check, restriction plan, setup notification),
`calls/CallGuardSetupActivity.kt` (opens `RoleManager.createRequestRoleIntent(ROLE_CALL_SCREENING)`);
modify `sync/ConnectionService.kt` (telemetry `callScreening`), `net/ApiClient.kt` (heartbeat
`call_screening`, `POST /device/rejected-calls`), `sync/Synchronizer.kt` (upload the outbox after a
sync), strings (de + en); tests `CallGuardRestrictionTest`, `SynchronizerTest` additions.

**Produces:** `object CallGuardRestriction { const val KEY = "disallow_config_default_apps"; fun plan(sdk: Int, switchOn: Boolean, roleHeld: Boolean, current: Set<String>): RestrictionPlan }` — add only when sdk ≥ 34 and switch on and role held; clear when present and not wanted; nothing below 34. Not in `RestrictionPlanner.MANAGED`.

The setup notification shows while the switch is on and the role is not held; it disappears once
held. `call_screening` is reported only when the switch is on (nil otherwise).

- [ ] `RestrictionKeysMatchThePlatformTest` extended to `KEY == UserManager.DISALLOW_CONFIG_DEFAULT_APPS`.

### Task 7: Real calls on real emulators

**Files:** Create `tests/e2e/emulator_calls_test.go`, `tests/android/calls.sh`.

Grant the role with `cmd role add-role-holder android.app.role.CALL_SCREENING <pkg> 0`; add a
contact through the contacts provider (`content insert`); place calls with `adb emu gsm call`; read
ringing from `dumpsys telecom` / `gsm list`; read the call log with `content query`. Cases: contact
rings; stranger is rejected and absent from the call log and reported to the server; family number
not in contacts rings; switch off → stranger rings (negative control); role removed on API 33 →
heartbeat `call_screening=false` and console red. API 37 and API 33. Calibrate: `CallDecision`
returns ALLOW for strangers → the stranger case red.

### Task 8: Docs and release

`DEPLOYMENT.md` setup step (role dialog on the phone, the phone app's hidden-number switch, WhatsApp
and Samsung Messages settings), IMPLEMENTATION_PLAN Phase 62 with the probe table, versionCode 56 /
0.6.47, release per the house flow, then turn nothing on: the switch stays off until the owner turns
it on (the console now offers it).
