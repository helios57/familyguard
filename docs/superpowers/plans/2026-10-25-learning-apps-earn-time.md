# Learning apps earn time, and any button stops the siren — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Minutes in a learning app (a family-wide package prefix) earn Bonuszeit 1:1, a daily limit
of 0 becomes a real setting, and — riding the same DPC release — any hardware button silences the
find-my-phone siren.

**Architecture:** The two engines (Go `backend/internal/policy/engine.go`, Kotlin
`enforce/EnforcementEngine.kt`) learn three inputs — learning prefixes, an explicit daily-limit
switch, and this device's learning minutes today — and are held together by
`backend/internal/policy/vectors.json`. The phone counts learning time in a third `UsageLedger`
beside the earned-time one and reports it with the day totals; the server merges it with
`GREATEST` into `usage_samples.learning_ms` and adds it to the seven-day balance as one credit per
device per day. The siren gets a full-screen stop screen that also listens to the keys and the
screen turning on/off.

**Tech Stack:** Go 1.27 + pgx (server), Kotlin/Android (DPC, minSdk 29), vanilla JS console,
Go e2e harness with real Postgres, real Chrome and a real emulator.

**Spec:** `docs/superpowers/specs/2026-10-25-learning-apps-earn-time-design.md` (approved
2026-10-25). The siren half was described to the owner in chat on 2026-10-25 (*"Wenn das Handy
klingelt (suchfunkion) soll es aufhören zu klingeln wenn der ein Knopf betätigt wird"*): any button —
side key (screen on or off), volume up or down — stops it; a full-screen *Stopp*; the first 2 s and
the screen's own timeout do not count.

## Global Constraints

- Public repo: no owner prefix, no children's names, no server internals. Tests use `org.example.learn.`
  or an emulator package. The house hygiene grep (kept outside the repo) must be clean on every staged diff.
- Release **0.6.45, versionCode 54**, image tag 0.6.45; FR-30 (learning apps), FR-9.4 (siren stop);
  IMPLEMENTATION_PLAN Phase 60.
- Rate 1:1, whole minutes floored per device per day over all learning packages.
- Credits never retroactive, never shrink; profile day credit = sum over devices.
- Old clients sending only `daily_limit_minutes: 0` mean "no limit".
- German UI strings, Swiss spelling (no ß): *Lern-App · verdient Zeit*, *Heute erspielt: N min*,
  *davon erspielt*, *Kein Tageslimit*, *Keine Zeit mehr — spiel ein Lernspiel, um Zeit zu verdienen*.
- Latest versions of anything touched; push straight to `main`; server before phones; watch CI and
  Release runs; argocd commit is the deploy; set the prefix and daily time 0 only after both phones
  report 0.6.45.
- Calibrate every new test by breaking the rule it guards (value change, not symbol deletion).

## Review Focus

1. A learning app on the family blocklist or blocked for one child must stay blocked and earn nothing — Task 1 vector `learning_app_blocked_stays_blocked`.
2. A 0.6.44 phone against the new server with daily time 0 + switch on would read "no limit"; the rollout order (Task 10) is the only guard — Task 10 checks both phones' `app_version_code` ≥ 54 before writing the policy.
3. Usage reported twice (retry) or out of order must not raise the credit beyond the larger figure — Task 3 test `TestLearningMsMergesWithGreatest`.
4. A learning minute measured while the phone is paused or in watch-only mode earns nothing — Task 6 test `LearningAccountTest.nothingWhilePausedOrWatchOnly`.
5. The siren's own heads-up / full-screen launch wakes the screen and must not stop it — Task 8 test `SirenKeysTest.screenOnInsideGraceIsIgnored`.

---

### Task 1: Go engine and shared vectors

**Files:** Modify `backend/internal/policy/engine.go`, `backend/internal/policy/vectors.json`, `backend/internal/policy/engine_test.go`

**Interfaces — Produces** (JSON names are the contract both engines share):
- `Settings.LearningPrefixes []string` `json:"learning_prefixes"`
- `Settings.DailyLimitEnabled *bool` `json:"daily_limit_enabled,omitempty"` — nil ⇒ `DailyLimitMinutes > 0`
- `Input.LearningMinutesToday int` `json:"learning_minutes_today"`
- `DesiredState.LearningPackages []string` `json:"learning_packages"` (installed, matched, not blocked; sorted, never null)
- `func IsLearning(pkg string, prefixes []string) bool` — `strings.HasPrefix(pkg, p)` for any non-empty p
- `func LimitOn(s Settings) bool`

Rules: a learning package (matched and not in the effective blocked set incl. family blocklist and
YouTube) is treated as ALLOW: never pending, never quota/bedtime-suspended, its own limit and BONUS
rule ignored; pause still suspends it. Gold = `EarnedAvailableMinutes + LearningMinutesToday -
EarnedSpentMinutesToday`. Every `limit > 0` test becomes `LimitOn`; `RemainingMinutes`,
`quotaReached` and `nextChangeAt` follow it.

- [ ] Step 1: add vectors `limit_on_zero_suspends_counted`, `limit_off_zero_is_unlimited` (no switch, minutes 0), `learning_app_free_at_zero`, `learning_app_blocked_stays_blocked`, `learning_app_family_blocked_stays_blocked`, `learning_app_paused_is_suspended`, `learning_minutes_open_counted_app` (limit on 0, `learning_minutes_today: 10` ⇒ counted app not suspended, `earned_active: "QUOTA"`, `earned_minutes_left: 10`), `learning_app_ignores_bonus_and_own_limit`.
- [ ] Step 2: `go test ./internal/policy/` → FAIL on the new vectors.
- [ ] Step 3: implement. Step 4: PASS, and every pre-existing vector unchanged.
- [ ] Step 5: calibrate (make `IsLearning` return false) → the learning vectors red; restore.

### Task 2: Kotlin engine on the same vectors

**Files:** Modify `enforce/EnforcementEngine.kt`, `enforce/EarnedAttribution.kt`, `plan/TimeView.kt`, `recovery/RecoveryActivity.kt:730`

**Interfaces — Consumes:** Task 1 JSON names. **Produces:** `Settings.learningPrefixes: List<String>`, `Settings.dailyLimitEnabled: Boolean?`, `Input.learningMinutesToday: Int`, `DesiredState.learningPackages: List<String>`, `EnforcementEngine.isLearning(pkg, prefixes)`, `EnforcementEngine.limitOn(settings)`.

- [ ] Step 1: `EnforcementEngineVectorsTest` (unchanged) runs Task 1's vectors → FAIL.
- [ ] Step 2: mirror Task 1 line for line. `EarnedAttribution.contextOf`: `state.learningPackages` join `exempt`; `budgetLeftMs` keyed on `limitOn`. `TimeView` and `RecoveryActivity` gate on `limitOn`, not `dailyLimitMinutes > 0`.
- [ ] Step 3: add `EarnedAttributionTest.learningAppIsNeverCharged`. `./gradlew :app:testDebugUnitTest --rerun-tasks` → PASS.

### Task 3: Migration and store (FR-30.1, FR-30.3, FR-30.4)

**Files:** Create `backend/internal/store/migrations/0030_learning_apps.sql`; modify `store/family.go`, `store/policy.go`, `store/telemetry.go`, `store/screentime.go`, `store/models.go`; tests beside them.

Migration: `families.learning_prefixes TEXT[] NOT NULL DEFAULT '{}'`; `policies.daily_limit_enabled BOOLEAN NOT NULL DEFAULT false` then `UPDATE policies SET daily_limit_enabled = daily_limit_minutes > 0`; `day_limits.limit_enabled BOOLEAN` (NULL for old rows ⇒ `minutes > 0`); `usage_samples.learning_ms BIGINT NOT NULL DEFAULT 0 CHECK (learning_ms >= 0)`.

**Produces:**
- `(*Store).LearningPrefixes(ctx) ([]string, error)`, `(*Store).SetLearningPrefixes(ctx, []string) error`
- `PolicyUpdate.DailyLimitEnabled *bool`; `Policy.DailyLimitEnabled bool`
- usage upsert takes `learning map[string]int64`, merged `GREATEST`
- `(*Store).LearningCredits(ctx, childID uuid.UUID, since string, excludeDevice uuid.UUID, excludeDay string) ([]earned.Credit, error)` — per device per day `floor(sum(learning_ms)/60000)`, summed per day; the (excludeDevice, excludeDay) pair left out (uuid.Nil excludes nothing)
- `(*Store).LearningMinutesForDay(ctx, deviceID uuid.UUID, day string) (int, error)`

- [ ] Step 1: tests `TestLearningMsMergesWithGreatest` (report 5 min, then 3 min ⇒ 5), `TestLearningCreditsSumDevicesAndExclude`, `TestMigrationKeepsZeroAsNoLimit`, `TestLearningPrefixesRoundTrip`.
- [ ] Step 2: FAIL. Step 3: implement. Step 4: PASS (`go test ./internal/store/`).

### Task 4: Server API, resolver and audit (FR-30.1, FR-30.3, FR-30.4, FR-30.6)

**Files:** Modify `httpapi/deviceapi.go` (report `learning` map beside `earned`), `httpapi/family.go` or new `httpapi/learning.go`, `httpapi/parents.go` (policy PATCH), `httpapi/screentime.go`, `httpapi/plan.go` (today), `enforce/resolve.go`; e2e `tests/e2e/learning_test.go`.

- `GET /family/learning-prefixes` (every parent role, API keys) → `{"prefixes":[...],"matches":{"<prefix>":N}}` (N = installed packages across the family's devices); `PUT` same body, admins only, validation `^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]*)*$`, ≤10 × ≤100 chars, dedup; audit `LEARNING_APPS_UPDATED`; push-wake every family device.
- Policy PATCH: `daily_limit_enabled` sets the switch; `daily_limit_minutes` without the switch sets it to `minutes > 0`. GET returns both.
- Resolver: `Settings.LearningPrefixes`, `DailyLimitEnabled`; `Input.LearningMinutesToday` = `LearningMinutesForDay(device, today)`; desired-state balance credits = tasks + `LearningCredits(child, since, device, today)`. `Resolver.Balance` (console) excludes nothing.
- `day_limits` records `limit_enabled`; Extrazeit (`screentime.go:85`) and time-request answers gate on the switch.
- `GET /children/:id/today` adds `learned_today_minutes` (sum of devices).
- [ ] Step 1: e2e tests `TestLearningPrefixesAdminOnlyAndValidated`, `TestLearningUsageEarnsBonuszeit` (prefix `org.example.learn.`, device reports 7.5 min ⇒ `learned_today_minutes: 7`, balance +7; re-report same ⇒ unchanged), `TestDesiredStateLeavesOutTheDevicesOwnTodayCredit`, `TestDailyLimitSwitch` (old body `{daily_limit_minutes:0}` ⇒ off; `{daily_limit_enabled:true,daily_limit_minutes:0}` ⇒ on 0; Extrazeit allowed at on-0).
- [ ] Step 2: FAIL. Step 3: implement. Step 4: `tests/e2e/run.sh -run 'Learning|DailyLimitSwitch'` PASS; then full `tests/run_all.sh secret-scan backend manifests image e2e android-unit`.

### Task 5: fgctl and MCP

**Files:** `backend/cmd/fgctl/…`, MCP tool table (where `set_policy` lives); `tests/e2e/fgctl_test.go`, `mcp_console_test.go` pattern.
- `fgctl learning-apps [--set p1,p2 | --clear]`; `fgctl policy … --daily-limit N` keeps meaning, new `--no-daily-limit` and `--daily-limit-on`.
- MCP `get_learning_apps`, `set_learning_apps`; `set_policy` accepts `daily_limit_enabled`.
- [ ] Test `TestFgctlLearningApps`, `TestMCPSetLearningApps` FAIL → implement → PASS.

### Task 6: Phone counts and reports learning time (FR-30.3, FR-30.7)

**Files:** Create `usage/LearningAccount.kt` (+ test); modify `sync/ConnectionService.kt` (open it beside `EarnedAccount`, charge on `onCredited`, report `learning` totals, fill `Input.learningMinutesToday`), `net/ApiClient.kt` (report field), `enforce/TodayReport.kt`, `plan/TimeView.kt` (*Heute erspielt*), `policy/SupportMessageManager.kt` (blocked-screen line), `res/values*/strings.xml`.

**Produces:** `class LearningAccount(val ledger: UsageLedger)` with `var context: LearningContext?`, `fun credit(credited: Map<String, Map<String, Long>>)`, `fun minutesToday(day: String): Int` (floor of the day's sum / 60 000); `data class LearningContext(day, learning: Set<String>, enforcing: Boolean)` where enforcing = !trackingOnly && suspendReason != PAUSED. File `family-guard-learning`.
- [ ] Tests `LearningAccountTest.creditsOnlyLearningPackages`, `.nothingWhilePausedOrWatchOnly`, `.floorsTheDaySumNotEachApp` (3 × 40 s ⇒ 2), `.otherDayCreditsNothing` FAIL → implement → PASS (`--rerun-tasks`).

### Task 7: Console (FR-30.7)

**Files:** `backend/internal/console/assets/family.js` (Lern-Apps card: prefixes, matches, edit for admins), `apps.js` (learning line *Lern-App · verdient Zeit* + *Sperren*), `overview.js`/`activity.js` (*davon erspielt*), `rules.js` (daily time: *Kein Tageslimit* switch + minutes, 0 allowed when on), `app.css`.
- [ ] Browser e2e `TestConsoleLearningApps` (set prefix, app line label, Übersicht shows *davon erspielt 7 min*), `TestConsoleDailyLimitZero` FAIL → implement → PASS; screenshots at 360 px and 1440 px looked at.

### Task 8: Any button stops the siren (FR-9.4)

**Files:** Create `commands/SirenKeys.kt` (pure decision) + `SirenKeysTest`; create `commands/SirenActivity.kt` (show-when-locked, turn-screen-on, keep-screen-on, big *Stopp*, `onKeyDown` any key ⇒ stop); modify `commands/AndroidPlatform.kt` (`showStopControl` adds full-screen intent to `SirenActivity`; registers a `SCREEN_ON`/`SCREEN_OFF` receiver while ringing; `hideStopControl` unregisters and finishes the activity), `AndroidManifest.xml`, strings.

**Produces:** `class SirenKeys(startedAtMs: Long, graceMs: Long = 2_000)` with `fun onKey(nowMs): Boolean`, `fun onScreenOn(nowMs): Boolean`, `fun onScreenOff(nowMs, keptOnByUs: Boolean): Boolean` — true = stop. Screen-off counts only while `SirenActivity` holds keep-screen-on (then it can only be the side key); screen-on counts after grace.
- [ ] Tests `screenOnInsideGraceIsIgnored`, `screenOnAfterGraceStops`, `screenOffByTimeoutIsIgnored`, `screenOffWhileKeptOnStops`, `anyKeyStops` FAIL → implement → PASS.

### Task 9: Real-emulator proofs

**Files:** Create `tests/e2e/emulator_learning_test.go` + `tests/android/learning.sh`; `tests/e2e/emulator_siren_test.go` + `tests/android/siren.sh` (pattern: `emulator_bonus_test.go` / `bonus.sh`).
- Learning: limit on 0; counted target = a preinstalled launchable app given rule LIMIT; learning prefix = another preinstalled launchable app's package. Target suspended → learning app in foreground ≥ 2 min with the network cut (`svc wifi/data disable`) → target unsuspended (package manager readback) → target in foreground until the minutes are used → suspended again.
- Siren: `TRIGGER_ALARM` → `adb shell input keyevent KEYCODE_VOLUME_DOWN` (and in a second run `KEYCODE_POWER`) after 3 s → siren stopped (stop control gone, `dumpsys audio` no alarm player) within 5 s; negative control: no key ⇒ still ringing after 10 s.
- Run on API 33 and API 36 images. Calibrate each by breaking its rule once.

### Task 10: Docs, release 0.6.45, rollout

- [ ] REQUIREMENTS FR-30 (+ FR-3.4/FR-3.11/FR-28.1 wording for the switch), FR-9.4; IMPLEMENTATION_PLAN Phase 60 with rulings and calibration table; DEPLOYMENT: how to set learning prefixes.
- [ ] Commit, push, watch CI + Release; release commit 0.6.45 / 54, tag v0.6.45; watch both runs.
- [ ] argocd scratchpad worktree: image 0.6.45; push; verify pod by digest; `/readyz`; desired state for both phones still sane (prefix unset ⇒ nothing changed).
- [ ] Phones self-update; wait until both report `app_version_code` 54.
- [ ] Set the family prefix to the owner's prefix via MCP (never in the repo); verify `matches` = 5 per device; then `set_policy` daily limit on, 0 minutes, both children; verify desired state: counted apps suspended, learning apps free, `learning_packages` has 5.
- [ ] Update memory open-items.
