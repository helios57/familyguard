# FamilyGuard MDM — Implementation Plan

Executes [CONCEPT.md](CONCEPT.md) against [REQUIREMENTS.md](REQUIREMENTS.md).
Status is updated as phases land.

**Definition of done for every phase:** the code builds, its tests pass, *and each new guard has
been calibrated* — broken deliberately, observed red, restored. A phase that cannot run its tests
reports "not measured" and does not count as done.

## Where this stands

**Phases 0–8 are done and calibrated.** `tests/run_all.sh` runs seven layers: a secret scan over
the full commit history, the Go control plane, the deployment manifests, the container image under
the manifest's own restrictions, the e2e suite against a real server binary / a real PostgreSQL / a
real browser, the Android unit suite, and the instrumented suite on a device-owned emulator across a
real reboot.
Every requirement in `REQUIREMENTS.md` has a row in [Traceability](#traceability) naming the test
that proves it, and the rows that name nothing say so.

Running it repeatedly is part of that, not ceremony: the sixteenth full sweep is what found a guard
that had been green fifteen times while proving nothing about one token in sixteen, and behind it a
real property of the session credential — see [6.6](#66--the-row-that-passed-94-of-the-time).

Reconciling the traceability table against the requirement text rather than against the test names
found the next one. **FR-13.3 was proven by nothing that could answer it**: the manifest was checked
by reading back the bytes this repository writes, which is not a statement about installing anything,
and its second half — no dependency on a desktop-only input — was satisfied only by the accident that
nobody had written a `:hover` rule yet. Both halves are now measured, the first by Chrome itself over
CDP and the second by a scan of the served assets, 8 breaks and 8 red — see
[6.7](#67--fr-133-and-the-difference-between-a-manifest-and-an-install). Calibrating that work
surfaced an intermittent red in the e2e harness that had nothing to do with it and would have been
written off as Docker: the postgres readiness probe was answering about a temporary server the
entrypoint was about to destroy — [6.8](#68--the-readiness-probe-that-was-measuring-a-server-about-to-be-destroyed).

The push that followed produced the third of these, from the one instrument that had never been read
at all. Four of the five CI jobs passed on their first real execution; the fifth reported six leaks
in a repository that holds no secret, and the resolution had to be reached without the values, since
the job redacts them and printing one would have been the actual leak —
[6.9](#69--six-leaks-in-a-repository-that-has-no-secrets).

**Nothing is deployed.** Phase 7 produced [`deploy/`](deploy/) — a kustomization that renders
complete — and [`DEPLOYMENT.md`](DEPLOYMENT.md), which carries the seven steps in the order they have
to happen. Six of them are things no sync can do for itself: DNS, the two data directories and their
ownership, the signed APK **and its DER certificate** in place before the first apply, the
`familyguard-secret` contract, the Google OAuth client, and the published image. Each is enumerated
because each fails silently or fails as the same sentence — "the pod will not start" — and a runbook
whose steps all produce the same symptom is a runbook that has not been written yet.

**What is honestly not measured**, and stays that way until real hardware answers it:

- **Safe-area insets.** `env(safe-area-inset-top)` and `env(safe-area-inset-bottom)` are 0 in
  headless Chrome, so the padding that keeps the console's header clear of a notch and its toast
  clear of a home indicator is declared and unverified. A notched phone is the only instrument that
  settles it.
- **NFR-10's screen-off half.** The connection is one held-open stream rather than a poll loop, and
  location is one-shot with a 30 s budget, but nothing measures battery over a night. Owed a
  measurement, not a test.
- **A physical Galaxy S20.** API 29 itself is no longer on this list: the instrumented layer now runs
  against an API 29 emulator, and doing so at the floor rather than at a convenient API level is what
  found both defects in [7.6](#76--the-first-run-at-the-floor-and-the-two-things-it-found). What an
  emulator cannot answer is the hardware half — a real modem, a real battery, a real vendor ROM with
  its own restrictions. `jvmTarget` 21 was taken on the emulator's evidence and is the first thing
  worth re-running if a device ever turns up.
- **The release gate has been executed, but not from this repository.** `release.yml`'s three
  refusals and its digest check all behaved on their first real run during development; what did not
  was the step after them, and it failed twice for two unrelated reasons where only one was visible
  at a time. The first tag pushed the image and then could not store its provenance for want of
  `attestations: write`; the second had that permission and hit the refusal underneath it — GitHub
  stores no attestations for **user-owned private repositories** at all, at any permission. Both
  refusals land on the same step, with the image *already pushed*, and the second is undiscoverable
  until the first is fixed. The step is now gated on a visibility read back from the API rather than
  on an assumption, and the job prints which of the two provenances the image carries, because a
  skipped step is green and "publish succeeded" otherwise reads as "attested". **In this repository
  the workflow has not run at all: there is no `v*` tag and no published image yet.** That is the
  honest state, and it is also the last claim in this document that a first release will settle.
  The lesson generalises past this one workflow: a gate that has never been executed is a claim, and
  each refusal it raises can be hiding the next.

---

## Phase 0 — Toolchain

| # | Task | Verification |
|---|---|---|
| 0.1 | Install Android `cmdline-tools`, SDK platform 37.1, build-tools 37.0.0, platform-tools | `sdkmanager --list_installed` shows them |
| 0.2 | Point `JAVA_HOME` at JDK 26 | `./gradlew -version` reports JVM 26 |
| 0.3 | Install an emulator system image + create an AVD for instrumented tests | `avdmanager list avd` shows it |
| 0.4 | Confirm Docker can run PostgreSQL for e2e | container starts, `pg_isready` succeeds |

Status: **done** — re-measured 2026-08-18 after the toolchain upgrade: `~/Android/Sdk` carries
cmdline-tools, platform-tools, emulator, a system image, build-tools 34/35/36/**37.0.0** and
platforms android-34/35/**37.0**/**37.1**; Docker server 29.7.2; Go **1.26.6**; JDK **26.0.2**.

0.2 changed. It used to read *"pin `JAVA_HOME` to system JDK 21 — Studio's bundled JBR 25 is too new
for AGP"*, and that is now measurably false: on 2026-08-18 `:app:assembleDebug`,
`:app:testDebugUnitTest` and `:app:assembleDebugAndroidTest` were all green under Temurin 26.0.2
(1m13s) and under 25.0.4 (2m13s). AGP 9.3.1 documents JDK 17 as its minimum, which is a floor rather
than a ceiling. An emulator on **API 29** is what the instrumented layer needs — that is the floor
the project holds, so testing above it measures the wrong device. That emulator now exists
(`familyguard29`), and it is also what made the bytecode level decidable: `jvmTarget` moved 17 → 21
on 2026-08-18 only after both instrumented passes came back green **on it**, because D8 accepts
21 class files either way and a green build was never evidence about the phone.

## Phase 1 — Repository skeleton

| # | Task |
|---|---|
| 1.1 | `.gitignore`, `README.md` describing what actually exists, `SECURITY.md` |
| 1.2 | Go module `github.com/helios57/familyguard/backend`, `go.mod` with the minimum dependency set |
| 1.3 | Gradle project: root build, version catalog, wrapper 9.7.0, `app` module at `jvmTarget` 21 |
| 1.4 | GitHub Actions: Go build/vet/test/gofmt, Android assemble + unit tests, e2e suite |

Status: **done — 1.4 written and statically calibrated, never yet executed by GitHub.**

- **1.1** — `README.md` and `SECURITY.md` are written. `SECURITY.md` states the boundaries this
  system loses as plainly as the ones it holds: a technical child with physical possession of the
  phone, custom-domain blocking outside the managed browser, and a compromised control plane are
  each named, because a security document that implies coverage it does not have is worse than one
  that admits a gap.
- **1.4** — `.github/workflows/ci.yml` (backend, android, e2e, image, secret-scan) and
  `release.yml` (7.2). Two properties are worth recording:
  - **Each job delegates to `tests/run_all.sh <layer>`** rather than restating the commands. The
    three-valued status lives in that script, and a `2` fails the job, so "could not be run" cannot
    become green by passing through YAML. A second definition of the gate written in workflow steps
    would drift from the first, and the drift would only ever be found by a bad release — which is
    also why `release.yml` *calls* `ci.yml` with `workflow_call` instead of paraphrasing it.
  - **Every third-party action is pinned by commit SHA**, with the version in a trailing comment. A
    tag is a mutable pointer its owner can move, and a workflow holding `packages: write` plus a
    moved tag is a supply chain.
  - The android job assembles *and* unit-tests, because `testDebugUnitTest` never runs the manifest
    merger or the packager: a broken manifest is invisible to a fully green unit run, and the
    manifest is where this app declares the `DeviceAdminReceiver` it cannot work without.

**Calibrated statically, and the limits of that stated.** `actionlint` 1.7.12 with shellcheck 0.11.0
reports **0 findings** on both files — and that number means nothing until the tool has been shown
to discriminate, so four known-bad copies were linted first: a `needs:` naming a job that does not
exist, an expression reading `steps.<no-such-step>.outputs`, an unquoted variable inside a `run:`
block (which is the only probe that proves the shellcheck integration is actually wired), and an
invalid runner label. **All four red, the unmodified file green.**

What that does *not* prove: no job in either workflow has ever run on GitHub. A green lint is a
claim about syntax and expressions, not about whether the Android SDK is on the runner image or
whether the e2e harness survives Docker-in-Actions. Both files are therefore evidence of intent
until the first push, and the first run is expected to find something.

## Phase 2 — Control plane: foundation

| # | Task | Tests |
|---|---|---|
| 2.1 | `internal/config` — parse and validate env; refuse to start without a session key ≥32 bytes, a database URL, an OAuth client ID | unit: each missing/weak value is rejected |
| 2.2 | `internal/store` — pgx pool, embedded migrations, advisory-locked startup migration | integration: migrate twice, second is a no-op |
| 2.3 | Schema: families, parents, children, devices, device_state, policies, app_rules, blocked_domains, installed_apps, usage_samples, commands, locations, recovery_events, audit_log | |
| 2.4 | `internal/auth` — JWKS fetch + cache, RS256 ID-token verification (iss/aud/exp/email_verified), HS256 session tokens, device-token hashing | unit: valid; wrong key; wrong aud; wrong iss; expired; unverified email; unknown kid — each rejected with a distinct error |
| 2.5 | `internal/httpapi/middleware` — request ID, security headers, body limit, bounded LRU rate limiter, exact-origin CORS | unit: limiter evicts and caps; 413 on oversize; 429 on burst |

Status: **done and calibrated.** 2.2's migrate-twice check landed with the e2e harness as
`TestStateSurvivesRestart`: it stops the server, starts a second one against the same populated
database, and asserts the data is still there and still correct. The second start re-runs the
migration path under the advisory lock, so the no-op property is exercised against real rows rather
than against an empty schema, which is the case that would actually break.

## Phase 3 — Control plane: domain

| # | Task | Tests |
|---|---|---|
| 3.1 | `internal/policy` — policy model, validation, compilation to the device bundle, version stamping | unit: bedtime across midnight, tracking-only suppresses enforcement, invalid times rejected |
| 3.2 | `internal/provisioning` — QR payload assembly, APK SHA-256 URL-safe unpadded checksum computed from the served bytes | unit: checksum matches a known vector; payload carries the required extras |
| 3.3 | Parent handlers — family, parents, children, policies, app rules, domains, devices, usage, locations, audit | e2e |
| 3.4 | Enrollment — single-use token exchange, device credential issue, recovery code generation | e2e incl. replay → 409 |
| 3.5 | `internal/httpapi/stream.go` — SSE hub: subscriber registry, non-blocking publish, age cap, close-on-shutdown | e2e incl. an unauthenticated stream refused |
| 3.6 | Commands — insert, dispatch, state machine, expiry sweep | e2e: offline queue → reconnect delivery → ack |
| 3.7 | Audit log written on every mutation | e2e: change a policy, read it back from `/audit` |

Status: **done and calibrated.** 3.3–3.7 are proven by the e2e suite (6.1–6.3), which now exists and
runs: 21 tests against a real server binary and a real PostgreSQL. Until it ran, the only thing
established about the handlers was that they built and vetted cleanly, and the suite's first
execution proved that is not evidence they work — it found four product defects, one of them in 3.6
(a fetched command answered `QUEUED` while the row it had just written said `DELIVERED`, which would
have desynchronised a real device from the console permanently) and one in 3.3 (FR-5.4 novelty
measured from a timestamp instead of a recorded baseline).

3.5 is SSE rather than a WebSocket hub (see CONCEPT.md), so there is no `internal/hub`. Two design
decisions landed here and are recorded in CONCEPT.md: fetching `GET /device/commands` is what marks
a command DELIVERED (the stream is only a wake-up, so a dropped frame costs latency and never a
fabricated delivery — NFR-3), and browser sign-in is an authorization-code + PKCE exchange this
server performs itself (`internal/httpapi/oauth.go`), which is what lets the console keep
`script-src 'self'` and adds `OAUTH_CLIENT_SECRET` / `OAUTH_AUTH_URL` / `OAUTH_TOKEN_URL`.

### 3.7 — the audit log, and why a name-only check was not enough

3.7 was carried as **NOT PROVEN** for most of this project. `TestRecoveryAndAudit` checked that ten
action *names* appear in `/audit`, which is real evidence and stops well short of the requirement:
it says nothing about who the row was attributed to, nothing about which object it was written
against, and — the part that decays — nothing at all about the eleven actions it does not name. A
suite pinned to the actions that existed the day it was written reports "audit covered" forever.

`tests/e2e/audit_test.go` closes it. One journey drives all **21** audited actions over real HTTP —
17 parent-side and 4 device-side — and asserts four things:

- **the row exists and is attributed correctly**: actor type, actor id, action, target type and
  target *id*, compared as a tuple. Target id is what catches a copy-paste between handlers; an
  action name alone still matches when `DEVICE_REMOVED` is logged against the child.
- **the row says which change was made**: nine detail keys (`PARENT_ADDED.email`, `CHILD_ADDED.name`,
  `DEVICE_RENAMED.name`, `APP_RULE_SET.package`, `COMMAND_FAILED.error`, …). A handler auditing the
  right action with an empty detail leaves a parent unable to tell what was blocked.
- **every row carries a `request_id`**, which is what joins it to a server log line. `audit()` sets
  it for every caller, so a row without one was written by a path that bypassed the helper.
- **completeness**: the covered set is compared against the action literals scanned out of
  `internal/httpapi/*.go`. A 22nd audited action fails this with its own name in the message.

Two things about that scan are deliberate. It **fails rather than returns empty** when the source
cannot be read or the call shapes stop matching — a scan that found no files would report "every
action is covered", which is the exact defect this file exists to prevent. And it does not try to
expand the one call site that *computes* its name, `"COMMAND_"+cmd.State`: the reachable states are
decided in the store, not at the call site, so expanding the prefix against all five command states
would demand rows for `COMMAND_QUEUED`, `COMMAND_DELIVERED` and `COMMAND_EXPIRED`, which that line
can never write. That is a permanent red, and a permanently red assertion gets muted within a week.
The test drives both reachable names by hand and pins the *count* of computed sites at 1 instead, so
a second one stops the suite and makes a human decide what it can emit.

#### 3.7 calibration — 6 breaks, 6 red, and one bug the first run found

Each break edits a **handler**, never the test, and the calibrator asserts the failure message
contains a specific substring — a test that fails for the wrong reason is not calibrated, it is
merely fragile.

| # | break | red |
|---|---|---|
| 1 | delete the `auditParent` call in `renameDevice` | `no audit row for DEVICE_RENAMED …` (and the detail assertion, honestly cascading) |
| 2 | audit `DEVICE_REMOVED` against `"child"` instead of `"device"` | `no audit row for DEVICE_REMOVED …` |
| 3 | pass `nil` detail to `APP_RULE_SET` | `no APP_RULE_SET row whose detail["package"] is com.example.game` |
| 4 | add a new `DEVICE_INSPECTED` audit call | `not driven by this test: DEVICE_INSPECTED` |
| 5 | make a second call site compute its name | `the handlers now have 2 audit call sites with a computed action name, not the 1 …` |
| 6 | write a row via `store.Audit` directly, around the helper | `audit row 22 (CHILD_REMOVED) carries no request_id: map[]` |

The first run of the finished test was red for a reason worth keeping: `[A-Z][A-Z0-9_]+` matched
`"COMMAND_"` as if it were a whole action name, so the completeness check demanded a row for an
action named `COMMAND_` that no handler can write. Requiring the captured name to end in an
alphanumeric is what separates a complete name from the prefix of a computed one. It is the same
shape as the two smoke.sh defects in 7.1: the assertion was wrong before the product was.

### NFR-5 — "an empty system shows as empty", and the three different ways it fails

NFR-5 was carried as **NOT PROVEN** with an honest note: the traceability table pointed at
`TestDeviceLifecycleJourney`, which starts from an empty database and only ever reads numbers it
caused. That is evidence and it is not the assertion — a build that also shipped a demo family would
keep it green forever, because it never looks at anything it did not create itself.

`tests/e2e/emptiness_test.go` closes it. The requirement turned out to be three claims that fail in
different places, so a single "it starts empty" assertion would have covered at most one of them:

- **Structural — nothing in the deployable artifact plants rows.** That is a claim about the *build*,
  not about a running server, and no amount of probing one instance proves it: a seed that ships in
  the schema is present in every deployment. So section 1 scans the migrations the binary embeds,
  before a server is started, for `INSERT INTO` / `COPY … FROM`. It strips `--` comments first,
  because a permanently red assertion gets muted within a week and the word appears in prose.
  The console gets the same treatment over HTTP — the *served* bytes, not the source tree — for
  markup baked into the three containers everything renders into, and for literal UUIDs in `app.js`.
- **Behavioural — a running server reports only what it was configured with.** Bootstrap writes
  exactly two kinds of row, the family named from `FAMILY_NAME` and one parent per address in
  `BOOTSTRAP_PARENT_EMAILS`. Both are a deployer's configuration, not data this build invented, and
  the assertion is that there is nothing else: the family carries the configured name, `/parents`
  holds exactly the two configured addresses with the roles the ordering implies, and `/audit` holds
  exactly **one** row — the sign-in the test itself just performed.
- **Epistemic — an unknown must not be reported as a measurement.** This is the one this project
  keeps re-learning. A device that has enrolled and never sent telemetry has no battery level;
  answering `battery_level: 0` tells a parent something false about their child's phone. `getDevice`
  already said so in a comment, and a comment is not a control. The test requires the field **absent
  or null**, and then — the half that makes it mean anything — sends a heartbeat of real zeros
  (`battery_level: 0, charging: false, screen_on: false`) and requires those to read back as zeros.
  Without that second half, a server that dropped every zero would pass.

Two Go-specific traps shape how it reads. `json.Unmarshal` maps **both `[]` and `null` onto a nil
slice**, so a decoded struct cannot tell an empty collection from a missing one — every assertion
that cares reads the raw bytes off the wire (`map[string]json.RawMessage`). And `omitempty` on a
pointer omits only nil, which is why the first run of this test was red three times over `""` rather
than `null`: the field is *absent*, not null, and the assertion had to accept both. On a plain `int`
the same tag would also drop a measured `0` — a flat battery reported as "never checked in".

The growth ratchet is section 8. It scans `internal/store/*.go` for every method returning a slice
and requires each to be either driven by an endpoint here or listed in `collectionsCoveredElsewhere`
**with a written reason**, and it checks the other direction too, so an entry naming a function that
no longer exists is a failure rather than dead weight. It reports `15 in the store, 11 driven here,
4 covered elsewhere with a reason`. Like 3.7's scan it **fails rather than returns empty**: a scan
that matched nothing would report "every collection covered", which is the defect this file exists
to prevent.

#### NFR-5 calibration — 11 breaks, 11 red, each naming the thing it broke

Every break edits **product** source — a migration, the store, the server, a console asset — never
the test, and the calibrator asserts the failure message contains a specific substring.

| # | break | red |
|---|---|---|
| 1 | add `0002_demo.sql` with an `INSERT INTO families` | `0002_demo.sql:2 plants rows (INSERT INTO)` — and, independently, `the family is named "Demo Family"` |
| 2 | move the migrations out from under the scan (embed and `ReadDir` fixed, so the server still works) | `could not read the migrations at … (0 files, err <nil>): a check that scans no files reports clean for the same reason a passing one does` |
| 3 | `Bootstrap` seeds a demo child | `GET /children: "children" came back as [{…"name":"Demo Child"…}]` |
| 4 | `ListChildren` returns `var out []Child` instead of `[]Child{}` | `came back as null, want the literal []` |
| 5 | `GetDeviceState` fabricates `battery_level: 100` when none was reported | `has battery_level=100, want the field absent or null` |
| 6 | `TouchDevice` drops a reported `0` as though it were unmeasured | `has it back as "" (present=false). A measured zero must be reported as zero` |
| 7 | add an unwired `ListSessions` to the store | `the store returns collections this test never reads: [ListSessions]` |
| 8 | rename `ListLocations` and its one call site | both directions: `never reads: [ListRecentLocations]` **and** `claims to cover store functions that no longer exist: [ListLocations]` |
| 9 | bake a child pill into `<nav id="child-switcher">` | `ships with content: "<a class=\"pill\" href=\"#/home\">Emma</a>"` |
| 10 | put a literal UUID in `app.js` | `contains the literal id …: a client that names a specific row is carrying data, not code` |
| 11 | rename the exported type `Store` → `DB` across `backend/**/*.go` (10 files, product still correct) | `found no collection-returning functions in 8 store files: the pattern no longer matches the signatures, so this check is measuring nothing` |

Cases 2 and 11 are the ones worth keeping, because they calibrate the **fail-closed branches
themselves** rather than the assertions standing in front of them — the paths that decide whether a
green means anything at all.

Case 2 only measures that path if the server is left fully working, so the break moves the migrations
directory **and** repoints the `//go:embed` and the `fs.ReadDir` with it. Without that the server
cannot migrate, the suite is red before the scan runs, and the case would have recorded a pass for a
reason that has nothing to do with emptiness.

Case 11 was written after the other ten, to settle a claim that was about to be recorded here as
fact: that the store scan's *"pattern matched nothing"* branch could not be tripped by any plausible
product edit, and was therefore uncalibrated by construction. That was wrong. Renaming an exported
type is an everyday refactor, it leaves the product entirely correct, and it takes the regex
`^func \(s \*Store\)` to zero matches — which is exactly the shape this branch exists to catch, since
without it a blinded scan would have gone on reporting "every collection is covered". Both
fail-closed branches are now measured, and neither is taken on trust.

### 3.2 — the APK the QR describes, and the two rules that had drifted apart

3.2 computed a checksum over a file, and 7.3 mounted a directory the same file was expected to be
in, and nothing connected the two. The QR could carry a perfectly correct checksum for bytes the
phone would never receive, and every test passed — because each half was tested against itself.

`GET /dpc.apk` closes it: when `APK_PATH` is configured the server serves that file, and the QR's
`ExtraPackageChecksum` is computed from the same path. The e2e test
`TestTheQRPointsAtAnAPKThisServerServes` now mints a QR against a real server, reads the download
location out of the payload, fetches it over a fresh HTTP client **with no credential** (a phone
being provisioned has none, so a route that required one would be undiscoverable until a real
device failed), and asserts the downloaded bytes hash to the checksum the QR published.

**Calibrated: 5 breaks, 5 red** — drop the `Content-Type` header; replace `http.ServeFile` with
`os.ReadFile` + `Write` (which silently loses Range support); delete the 404 branch; disable the
`os.Stat` liveness check; append one byte after serving.

The first of those five is the one worth recording, because it was `NOT A GUARD` on the first
attempt. Deleting the explicit `Content-Type` left the suite green: `http.ServeFile` types a
response from the file extension, Go's `mime` package reads `/etc/mime.types`, and this host's
copy maps `.apk` to the Android type — so the assertion was measuring the developer machine's mime
database. The container the server actually runs in is distroless: no `/etc/mime.types`, nothing for
`.apk` in Go's builtin table, so `ServeFile` would sniff and report a real APK as a zip. The fixture
is now named `familyguard.dpcfixture` and the helper calls `t.Fatalf` if `mime.TypeByExtension` ever
starts recognising that extension, so the test cannot quietly go back to measuring the host.

**One real defect fell out of wiring the e2e case up: two validators, one rule, already drifted.**
`config` rejected every `http` APK URL outright while `provisioning.requireHTTPS` deliberately
exempted loopback — so the deployment shape where the server hosts its own APK could not be started
at all, and the failure read as *"that configuration is invalid"* rather than *"these two disagree"*.
`requireHTTPS` is now exported as `provisioning.RequireProvisioningURL` and `config` calls it; the
rule has one definition. Applying it also revealed that `PUBLIC_URL` — which becomes `server_url`
inside the provisioning payload — had never been checked for cleartext at all, so it is checked now,
at startup rather than at the first QR request.

## Phase 4 — Parent console

| # | Task |
|---|---|
| 4.1 | Static console (HTML/CSS/vanilla JS), Google Sign-In, embedded via `go:embed` and served at `/` |
| 4.2 | Views: overview + live telemetry, child switcher, screen time, apps, bedtime + quota, network, provisioning QR, instant actions, audit |
| 4.3 | Session token kept in memory; every API call authenticated; no state in the page that the server does not hold |
| 4.4 | **Mobile-first layout (FR-13.2/13.3)** — 360 px portrait baseline, bottom tab navigation, cards instead of tables below 720 px, ≥44 px touch targets, no horizontal page scroll, web app manifest for home-screen install |

Status: **done and calibrated for 4.1–4.4, source and rendered.** The console is three hand-written files under `internal/console/assets/` plus a manifest
and an icon, embedded with `go:embed` and mounted route by route (`internal/httpapi/console.go`) —
no build step, so the bytes a reviewer reads are the bytes a browser runs. 4.3 differs from the plan
in one place: the session token is kept in `localStorage`, not in memory, because a token that dies
on every tab restore makes the phone case unusable; it is deliberately not a cookie, which is what
keeps CSRF off the table.

Nine guards in `console_test.go`, each broken deliberately and observed red on 2026-08-17: viewport
meta deleted; viewport disabling zoom; inline event handler; inline `<script>` body; inline `style`
attribute; a reference to an unmounted asset; a wrong content type; an unconditional 304; a
catch-all that swallows unknown paths. The content-type check failed its own calibration on the
first attempt — it compared the response against the handler's own table, so flipping that table to
`text/plain` left it green. It now carries an independent table.

**4.4 is measured on a rendered page** (`tests/e2e/browser_test.go`, `tests/e2e/mobile_test.go`).
The nine source guards above are worth having and every one of them is satisfiable by a console that
is unusable on a phone — app.css's own header promises "no tap target smaller than 44 px" while its
`.pill` rule said `min-height: 36px`, and no guard that reads text can disagree with that. So the
suite drives headless Chrome over CDP at 360×800 with touch emulation and `mobile: true`, signs in
through the real redirect chain, and measures the signed-out screen, each of the five views, the
navigation drawer and the provisioning sheet: tap-target width **and** height, elements past the
viewport, sideways scrollers, input font size, whether the sign-in button is above the fold, whether
the header is still at the top of the viewport at the end of a long page, whether the first card is
below the header rather than behind it, how much of the screen the permanent chrome costs, whether
the drawer opens, is modal, closes on Escape, closes behind the link it followed and keeps its
destinations in the lower part of the screen, and the QR's rendered size. The driver is hand-written against the CDP wire protocol — about 430 lines including
an RFC 6455 client — so the e2e module keeps its zero-dependency property and no version matrix can
stop the check from running. Chrome is a **precondition**, not an optional extra: absent, `run.sh`
exits **2, NOT MEASURED**, because a suite that skips this layer and exits 0 is exactly the green it
exists to prevent.

It found four things on its first run, in the order they mattered:

1. **`app.js` did not parse.** One unbalanced parenthesis in `renderActivity`, so *not one line of
   the console's JavaScript had ever executed in a browser* — the page rendered its `<noscript>` text
   and nothing else. It was served 200, `text/javascript`, 32 127 bytes, and passed every asset,
   ETag, CSP, manifest and viewport guard the whole time. This is the defect class this project is
   built against, found by the first instrument capable of seeing it, and the guard against it is now
   one line: `typeof boot` must be `"function"` before anything is measured.
2. **`.pill` was 36 px tall** — the child switcher, one of the two most-tapped controls in the
   console, and the only one below the floor the stylesheet's own header promises. Now `var(--tap)`.
3. **`#sheet-close` measured 43×44.** A tap target is an area, not a height: `.btn` sets a minimum
   height, so a button whose label is a single glyph is as wide as the glyph. Icon-only buttons now
   carry `min-width: var(--tap)`.
4. **Two of the guard's own checks were not binding.** A readiness selector that could not tell two
   screens apart returned instantly against the previous view, so a measurement was being taken of
   the wrong screen under the right screen's name; and the tab-bar assertion measured the bar *after*
   scrolling to the end of the page, where a `position: static` bar has flowed to the bottom of the
   viewport too — it stayed **green** on that known-bad input. Both are fixed; the second was found
   by calibration and by nothing else.

`tests/e2e/calibrate-mobile.sh` is that calibration, checked in: it breaks the console fourteen ways
in turn — the parenthesis, an over-wide card, an unintended sideways scroller, a 13 px input, a
static header, a fixed header with the content behind it, a header that eats the screen, a hidden
menu button, a drawer with both of its close-on-navigate paths removed, a drawer whose destinations
are top-aligned instead of thumb-high, a sign-in card pushed below the fold, an over-wide sheet, a
60 px QR, and 30 px buttons — and each run must go red **naming that rule**; red for the wrong reason
counts as a failure. It ends by restoring the assets and requiring
green, without which a guard that fails on everything would look perfectly calibrated.

Two of those cases exist because a single break was not enough. The drawer is closed on navigation by
*two* paths — a listener on the nav element, which also covers tapping the link for the screen you
are already on, and `onRoute`'s own call — so removing either alone leaves the rule green while
looking calibrated. Both go.

Still **not measured**: safe-area insets. `env(safe-area-inset-top)` and `-bottom` are 0 in headless
Chrome, so the padding that keeps the header clear of a notch and the toast clear of a home indicator
is asserted by nothing here; it is declared in app.css and unverified, and a real phone is the only
instrument that would settle it.

CONCEPT.md was brought back in line with what exists at the end of this phase: §2.2 and §3.3 said
WebSocket, §3.2's route table predated most of the routes, §3.1 omitted the browser sign-in flow
entirely, and §5 had no configuration contract at all. A concept describing a design nobody built is
worse than no concept, because it is the document a reviewer trusts.

## Phase 5 — Android DPC

| # | Task | Tests |
|---|---|---|
| 5.1 | Manifest, admin policy XML, application class, exported surface minimised | instrumented |
| 5.2 | Provisioning activities + admin receiver; baseline hardening applied at compliance and on boot, from the restriction set the server sent. No FRP, and an instrumented test asserting factory reset is still permitted after compliance and after a reboot (FR-2.3) | instrumented |
| 5.3 | `enroll` + `net` — token exchange, credential storage, REST client, SSE stream with backoff, foreground connection service. An event only wakes a sync; the fetch is what delivers | instrumented |
| 5.4 | `enforce/EnforcementEngine` — pure desired-state computation | JVM unit: bedtime enter *and leave*, quota, tracking-only, app rules, YouTube set, critical whitelist immunity |
| 5.5 | `policy` appliers — hardening, app suspension, Chrome (single writer), DNS | JVM unit for the Chrome bundle; instrumented for the rest |
| 5.6 | `usage` — monotonic per-package foreground tracking, screen-off pause, daily rollover; the reporters that send it; and the enforcement alarm that acts on `next_change_at` | JVM unit |
| 5.7 | `commands/` — lock, unlock, alarm, stop alarm, locate, YouTube on/off, sync | JVM unit for the executor, the queue, the handlers, the siren, the probe and the lock; source guards for the two defects no runtime test can reach |
| 5.8 | `recovery` — per-device code, PBKDF2 verification, escalating lockout, event reporting, and the released state a recovered device enforces | JVM unit: 7 suites, 63 tests, cross-language vectors against the Go implementation |
| 5.9 | `status/` — the on-device status block: what this phone measured, what it applied, when it last reached the server, and the three-valued level that keeps *not measured* from rendering as zero | JVM unit for the pure composer and the contact stamp; source guard for the token; instrumented for the render, the tap targets and the real usage-access appop |
| 5.10 | `RequirementCitationsTest` — the requirements document wired to the code that claims it, in both directions | JVM unit: no id cited that is not defined, no id defined that nothing claims, plus a half that fails if the scan read nothing |
| 5.11 | `policy/ClockPolicy.kt` + `DpmClockGateway` — automatic network time asserted **and read back** (FR-2.2), inside the baseline both call sites already run | JVM unit for the pure manager and for its effect on the baseline outcome; source guard for the API 29/30 split |

Status: **5.1–5.11 done and calibrated, on the JVM and on a real device.**

The Gradle project builds (`:app:assembleDebug`, AGP 9.3.1 / Kotlin 2.4.10 / JDK 26) and
`:app:testDebugUnitTest` ran **47 classes, 423 tests, 0 failures, 0 errors, 0 skipped** at the close
of this phase. The current figure, after Phase 6 and the 2026-08-18 toolchain upgrade, is
**51 classes, 450 tests, 0 failures, 0 errors, 0 skipped** — counted from
`app/build/test-results/testDebugUnitTest/*.xml`, not from Gradle's own summary line, which says
`BUILD SUCCESSFUL` for a task that ran nothing. minSdk is **29**, not the 26 CONCEPT.md §5 first named:
`setGlobalPrivateDnsModeSpecifiedHost` is API 29, so on 26–28 FR-6.1 cannot be met at all and the app
would enforce everything else while silently leaving DNS filtering off. The reason is recorded at the
`minSdk` line itself, where the next person to lower it will be standing.

**5.4** is `enforce/EnforcementEngine.kt` — a pure Kotlin port of `backend/internal/policy`, with no
Android import, so its tests run on the JVM in 0.2 s. Two engines exist because the phone must keep
enforcing bedtime and the quota offline (FR-9) while the console must show what is in effect now;
the cost of that is drift, and drift is silent. `vectors.json` is the defence: the same 20 cases, the
same expected output, replayed by `TestSharedVectors` (Go) and `EnforcementEngineVectorsTest`
(Kotlin). Every one matched on the first run.

Two things had to be fixed before that meant anything:

- **The vectors were not on the test classpath at all.** AGP records a source directory as a path,
  not as a provider carrying its producer, so `resources.srcDir(copyPolicyVectors.map { … })` left
  the copy task out of the task graph and the directory empty. The test failed loudly on the missing
  resource, which is the only reason it was noticed — a suite that had treated "no vectors" as
  "nothing to disagree with" would have reported success while replaying nothing. `build.gradle.kts`
  now states the dependency, and the test pins both the vector count and the number of comparisons.
- **A comment claimed something measurably false.** It said `ISO_OFFSET_DATE_TIME` omits `:ss` when
  seconds are zero, so the explicit RFC 3339 pattern was load-bearing. Measured on JDK 21: it does
  not — the two formats are identical for every instant this engine emits, and swapping them is
  green. It is `ZonedDateTime.toString()` that drops the seconds, and that one is red. The comment
  now says what was measured rather than what was assumed.

### 5.4 calibration — 10 breaks, 10 accounted for

| break | verdict | what went red |
|---|---|---|
| `next.format(RFC3339)` → `next.toString()` | RED | vectors: `next_change_at` `…T07:00+02:00[Europe/Zurich]` ≠ `…T07:00:00+02:00` |
| bedtime window end made inclusive | RED | vectors: "the end minute is already outside the window" — `suspend_reason` BEDTIME ≠ "" |
| `com.android.settings` dropped from the critical list | RED | vectors: it appears in `suspended_packages` |
| `youtu.be` dropped from the YouTube domains | RED | vectors: `blocked_domains` short by one |
| `@SerialName("safe_search")` misspelled | RED | vectors: *expects "safe_search", which is not a field of DesiredState* |
| `no_factory_reset` added to the hardening set, net intact | **GREEN, as expected** | the forbidden-restriction filter caught it — which is why the case below exists |
| …and the net entry removed too | RED | hardening: *in the idle state the engine set no_factory_reset* |
| `no_safe_boot` removed (the positive control) | RED | hardening: *hardening is not in effect at all — the absence checks would pass vacuously* |
| unknown timezone falls back to UTC | RED | hardening: `InvalidPolicyInput` expected, nothing thrown |
| out-of-range clock returns 0 | RED | hardening: `InvalidPolicyInput` expected, nothing thrown |

The `FORBIDDEN_RESTRICTIONS` filter removes nothing today — none of the four names is ever added —
so deleting it changes no output and any test would stay green either way. It is calibrated by
simulating the future mistake instead, twice: once with the net (green, caught) and once without
(red). This is the same two-step used for the Go side, and it is the only form of evidence available
for a guard that never fires.

### 5.5 (decision half) — `RestrictionPlanner`, and the pre-sync baseline

Applying a desired state to `DevicePolicyManager` is two jobs, and only one of them can brick a
phone. `policy/RestrictionPlanner.kt` holds that one: given what `UserManager` reports is in effect
and what the desired state asks for, it returns exactly which restrictions to add and which to clear.
The Android side is then a loop over two lists with no judgement in it, and every judgement is
covered by a JVM test that runs in milliseconds instead of an instrumented one that needs a phone.

Three decisions live there, and each has a way of being silently wrong:

- **`MANAGED` bounds what the DPC will touch.** A restriction outside it is left exactly as it is,
  whoever set it. Clearing everything not in the desired state would quietly undo an OEM's setting,
  or one this app managed last release and no longer names — a change nobody asked for and nobody
  would see.
- **The forbidden set is filtered here as well as on the server, and the duplication is the point.**
  The server already refuses to send `no_factory_reset`, so this filter is unreachable through the
  product's own control plane. It exists for the case where the control plane is not the product's
  own: a DPC that applies whatever JSON it is handed makes an un-wipeable phone one bad response
  away, and the phone is the half that cannot be rolled back.
- **Because `MANAGED` contains the forbidden names and the desired state never can, a forbidden
  restriction that is somehow already in effect always lands in `clear`.** The device returns to a
  wipeable state on the next sync rather than staying stuck in whatever set it.

`EnforcementEngine.BASELINE_RESTRICTIONS` is the second half of the same idea, aimed at the window
before the server has ever been reached: provisioning compliance, and a boot that precedes the first
successful sync. The DPC needs *something* to apply there, and the obvious move — a separately
written "provisioning defaults" list — is a second policy nobody remembers to update, after which the
device silently drops to it after every reboot. Instead `compute()` builds from the same constant, so
the baseline is by construction a subset of every state the engine can produce.

### 5.5 calibration — 5 breaks, 5 as expected

| break | verdict | what went red |
|---|---|---|
| tracking-only quietly drops a baseline restriction | RED | baseline: *the tracking-only state drops no_unknown_sources from the baseline* |
| `no_factory_reset` added to `BASELINE_RESTRICTIONS` | RED | baseline: *the baseline the DPC applies unprompted must not contain a forbidden restriction* — **and nothing else**: `factory reset is never blocked` and `unreadable input is refused` both still passed, because `compute()` filters the name back out. The unprompted path is the hole, and only the new assertion sees it |
| planner's forbidden filter dropped | RED | planner: *a forbidden restriction is never applied, however it arrives* |
| `MANAGED` no longer covers the forbidden names | RED | planner: *already in effect is cleared* + *every forbidden restriction is managed* |
| `clear` computed over `current` instead of `MANAGED` | RED | planner: *an unmanaged restriction is left alone* + *an empty desired state clears everything this app manages and nothing else* |

Restored green after each. The second row is the one worth keeping: it is a break that every
previously existing test agreed was fine.

### 5.1 / 5.2 — provisioning, the boot path, and the first code that touches a device

`admin/AdminReceiver` + `res/xml/device_admin.xml`, `provisioning/GetProvisioningModeActivity` and
`PolicyComplianceActivity` (the two the platform calls during `afw#setup` / QR provisioning),
`admin/BootReceiver`, and the applier the last three share: `policy/HardeningManager`, which turns a
`RestrictionPlanner` decision into `DevicePolicyManager` calls through the `RestrictionGateway`
interface. `DpmRestrictionGateway` is the only implementation that touches Android, which is what
lets `HardeningManagerTest` cover the applier on the JVM — including the cases a device cannot
easily be put into, such as a platform that accepts a call and does not honour it.

**`plan` and `floor` are different methods because a boot must never weaken a device.** The boot
path and provisioning compliance run before the server has ever been reached, so they apply
`BASELINE_RESTRICTIONS` — but applying it through `plan` would *clear* everything the baseline does
not name. A phone that has been syncing for months reboots, and until the next successful sync
`no_install_apps` and `no_debugging_features` are gone. A child can open that window on demand by
restarting the phone, and nothing about the device would look wrong during it. `floor` adds what is
missing, clears only what is forbidden, and takes nothing else away. The distinction is invisible on
a fresh phone — on an empty `current` the two agree exactly — so every test of it seeds `current`,
and one test asserts the agreement explicitly so the next reader does not mistake a passing
fresh-device case for coverage of the difference.

The instrumented layer is `tests/android/instrumented.sh` rather than a bare
`connectedDebugAndroidTest`, for three reasons that are all about not lying:

- `WipeabilityTest` **fails rather than skips** on an unmanaged device, because a skip would be a
  green in the one layer that can see the phone. Something then has to establish device ownership,
  or every developer emulator reports a red that is not about the product.
- "Still wipeable after a reboot" (FR-2.3) cannot be observed by a process running inside the
  device's own uptime. It needs a real `adb reboot` between two runs, and the second run has to
  *read* the state the boot left rather than re-apply it — hence `WipeableAsFoundTest` as its own
  class, excluded from pass 1.
- A filtered instrumentation run that matches nothing exits 0 having opened no test. Both passes
  count the testcases that actually executed and refuse a green that came from an empty run.

### The restriction keys are the platform's, and a wrong one is silent

`no_config_private_dns` is not an Android user-restriction key. The constant is
`DISALLOW_CONFIG_PRIVATE_DNS`, and its value is **`disallow_config_private_dns`** — the one key in
this set that does not start with `no_`, which is exactly why the wrong spelling looked right in
code review. `DevicePolicyManager.addUserRestriction` accepts an unknown key, logs, and applies
nothing: no exception, no error return, no failed call to notice.

Every layer this repo had was green on it. 24 JVM tests, the 20 shared vectors, the Go suite and 18
e2e journeys all passed, because each of them compared our spelling against our spelling. The
emulator was the first thing in the project to ask the platform anything, and it found the defect on
its first run: `NOT-IN-EFFECT=[no_config_private_dns]`, absent from every section of `dumpsys user`.
The authority is the SDK itself — `javap -constants android.os.UserManager` on
`~/Android/Sdk/platforms/android-34/android.jar`.

`vectors.json` was never going to catch it, and the reason generalises: only **3 of its 20** vectors
assert `user_restrictions` at all. A shared-vector file is evidence about the cases it encodes, not
about the fields it happens to mention in some of them.

The root-cause fix is `RestrictionKeysMatchThePlatformTest`, which closes the chain: the Go engine
is asserted equal to `vectors.json`, `vectors.json` to the Kotlin engine, and now the Kotlin engine
to `android.os.UserManager`. Java compile-time `String` constants are inlined by kotlinc, so it runs
on the JVM in milliseconds with no Android runtime. It has two tests, because either alone leaves a
hole: one asserts every constant equals the platform's, plus a positive control that the platform
values read back non-empty (`assertEquals("", "")` twelve times is a suite that proves nothing); the
other walks `EnforcementEngine`'s fields by reflection and fails if a `RESTRICTION_*` constant is
not in the binding map, so adding a thirteenth key cannot silently escape the check. All four call
sites were corrected — `engine.go`, `EnforcementEngine.kt`, three `vectors.json` arrays (re-sorted;
the correct key sorts before every `no_*`) and one e2e expectation. Every other restriction literal
in the repo was re-read against the SDK and matched.

### 5.1 / 5.2 calibration — 5 code breaks, 14 harness breaks

| break | what went red |
|---|---|
| the old `no_config_private_dns` spelling restored | `every restriction key is the platform's own` **and** `every shared vector produces the same desired state as the Go engine` |
| `RESTRICTION_SMS` dropped from the binding map | `no restriction constant escapes the binding` |
| `floor` clears like `plan` | 4 tests, JVM and instrumented: `a boot never weakens a device that has already synced`, `a boot still clears a forbidden restriction`, `the floor never takes away what a sync put there`, `the floor still clears a forbidden restriction, and only that` |
| `floor` drops its forbidden clear entirely | `a boot still clears a forbidden restriction`, `the floor still clears a forbidden restriction, and only that` |
| `floor` stops filtering the forbidden set | `a forbidden restriction in the required set is never applied` |

The harness is a guard too, and its failures are false NOT MEASUREDs, which train the reader to
ignore the layer exactly as fast as a false green does. Each row was produced against the real
emulator or a stub SDK, and each restored:

| break | verdict |
|---|---|
| `ANDROID_HOME`/`ANDROID_SDK_ROOT`/`PATH`/`HOME` with no adb behind any of them | NOT MEASURED, *no adb on PATH, in ANDROID_HOME/ANDROID_SDK_ROOT or in ~/Android/Sdk* |
| stub SDK reporting zero attached devices | NOT MEASURED, *no device or emulator is attached* |
| stub SDK reporting two, `ANDROID_SERIAL` unset | NOT MEASURED, *refusing to provision an unspecified one* |
| the ownership probe pointed at a dumpsys service that does not exist | NOT MEASURED, *dumpsys device_policy returned nothing readable* |
| pass 1 told to expect 99 testcases | NOT MEASURED, *pass 1 reported success without running its tests* (having really run 3) |
| `--no-reboot` | NOT MEASURED, *skipped the after-reboot pass, which is the FR-2.3 evidence* |
| the testcase counter run against an old marker, then a fresh one | 3, then 0 — it can both see and fail to see |
| the emulator shut down while instrumentation was running | **FAIL** — the defect below; NOT MEASURED, *the device went offline during pass 1*, after the fix |
| the admin probe pointed at a package that is not an enabled admin | NOT MEASURED, *is the recorded device owner but is NOT an enabled device admin* |
| the admin probe pointed at a dumpsys section that does not exist | NOT MEASURED, *has no 'Enabled Device Admins' section* |
| `PKG` pointed at an app that is not installed, so provisioning had to be attempted and could not succeed | NOT MEASURED after 5 attempts, carrying `Accounts:0` beside dpm's claim about accounts |
| a `*Test.kt` on disk carrying no `@Test` | NOT MEASURED, *these test classes exist in src/test but reported nothing: ZzzCalibrationTest* |
| the unit layer's results path pointed one directory sideways | NOT MEASURED, *no JUnit XML under …: the task exited 0 having reported nothing* |
| one assertion in `RestrictionPlannerTest` inverted | FAIL, *31 tests, 1 failures, 0 errors* — the count is reported on the red path too |

Six harness defects came out of that pass. The first two have the same shape — a failure that
counted nothing, reported as *nothing ran*:

- **`ran_count` returned 0 after a perfectly good 3-test run.** Three causes, each independently
  fatal: `xargs -r command grep` exits **127**, because `command` is a shell builtin xargs cannot
  exec (and bare `grep` is a ugrep wrapper here, so it cannot simply be dropped); AGP names the
  report after the device — `TEST-familyguard34(AVD) - 14-_app-.xml`, spaces and parentheses — so
  the file list must travel as arguments, never through a pipe that word-splits; and `grep -c`
  counts lines, not occurrences.
- **`adb wait-for-device` carries no deadline, so the `|| result` beneath it was unreachable.** A
  device that never comes back does not fail there, it blocks there — measured, by shutting the
  emulator down mid-run: the script printed *rebooting the device* and nothing ever again, and had
  to be killed by hand. A layer whose whole contract is to end in one of three states cannot have a
  fourth. It now waits under `timeout`, given the adb **binary** rather than the shell function that
  wraps it, since `timeout` cannot exec a function and would exit 127 — a wait that never happened,
  which reads exactly like a device that never came back. `adb reboot` needs no such clock: on an
  absent device it exits 1 in milliseconds, measured alongside.
- **The offline branch itself did not work, and only the calibration could have shown that.** It
  asked `adb devices` whether the phone was still attached, and `adb devices` is a cache of
  transports rather than a probe: the shut-down emulator went on being listed as `device` for
  several seconds, so the snapshot taken the instant Gradle failed saw a healthy phone and the layer
  reported **FAIL** — the precise false red the branch exists to prevent. It now round-trips to the
  device with `adb shell true`, retried over a window. The retry matters in the direction that is
  easy to miss: a phone that blips must still resolve to FAIL, because filing a genuine product
  failure as "not measured" is a red nobody ever looks at again.

- **"Already the device owner" was a claim about a record, not about whether the DPC can act.**
  `dumpsys device_policy` reports the device owner and the list of *enabled device admins* in
  separate sections, and the abrupt shutdowns above left them disagreeing: the owner record
  survived, the admin list came back empty, and every `DevicePolicyManager` call failed with *"Admin
  … does not exist or is not owned by uid 10192"*. The harness announced ownership and ran the suite
  anyway, so three tests went red naming restrictions that were never the problem — an environment
  fault spent as product credibility. It now checks the admin section too and reports NOT MEASURED
  with the remedy. The direction is safe: a DPC that failed to declare its admin never becomes
  device owner at all, so this cannot launder a genuine red into "not measured".

- **Provisioning failed once and succeeded by hand seconds later, with nothing changed between.** On
  a device that has just been wiped, the first `dpm set-device-owner` is refused; the identical
  sequence typed by hand a few seconds afterwards prints *Success*. The platform is still settling
  after first boot and says so in the least helpful way it has — the accounts message again, on a
  device measured at `Accounts: 0`. A one-shot attempt therefore reports NOT MEASURED for a device
  that is perfectly provisionable, which is the false red the whole layer is built to avoid. It now
  retries five times over twenty seconds, and when it does give up it prints `dumpsys`'s own account
  count beside dpm's claim, so the reader is handed the disproof rather than sent hunting for an
  account that does not exist.
- **The unit layer reported PASS having started no test JVM, and left artifacts that misdescribed it
  by a factor of eight.** `:app:testDebugUnitTest` came back `FROM-CACHE`, which as a *verdict* is
  defensible — identical inputs, identical result. What is not defensible is the evidence: the
  results directory on disk then described **4 tests in one class**, while forcing the same task to
  execute runs **31 tests in six classes**. Anyone opening those files to answer "what did this gate
  actually check" is misled in the direction of comfort, and nothing in the output says so. The layer
  now clears the results, runs with `--rerun`, and reads the JUnit XML back: how many tests, how many
  classes, and — the half that keeps working as the suite grows — whether every `*Test.kt` on disk
  reported at all. That last check is the one this cannot otherwise see: a test class that stops
  being discovered, renamed or moved to the wrong package or stripped of its annotations in a
  refactor, takes its assertions out of the suite while every surviving test passes and the task
  still exits 0. A count pinned to a number would need editing on every new test, and would be edited
  to whatever the run printed; a comparison against the source tree cannot be.

The last three are the reason a guard that has never fired is not evidence. Each branch was written
deliberately, commented with the case it was for, reviewed, and wrong — and every run of the suite
agreed with them, because none of them had ever lost a device, and none had ever been asked what it
had actually measured.

### 5.3 calibration — 39 breaks, 39 red

`5.3` is the phone's whole conversation with the server: `net/SseParser`, `net/Backoff`,
`net/ApiClient`, `net/EventStream`, `enroll/Enroller`, `sync/Synchronizer` and the applier plumbing
in `sync/StateApplier`. Every one of the 39 breaks below reddened at least one named test, and the
suite was green again after each restore (`after restore: green`, exit 0). No `NOT A GUARD`, no
`DID NOT COMPILE`, no `SKIP` — so no test in this phase is carrying a mechanism that another
mechanism already rescues, and no break failed to compile and thereby went unexercised.

| unit | breaks | representative |
|---|---|---|
| `SseParser` | 3 | an unnamed field treated as data; all whitespace stripped after the colon rather than one space; an unknown field allowed to make a frame |
| `Backoff` | 3 | the exponent cap lost; jitter narrowed to a band; `reset` made a no-op |
| `ApiClient` | 8 | a revoked credential reported retryable; the base URL left unnormalised; the refusal read from the input stream instead of the error stream; the bearer sent under a header nothing reads; an unmeasured telemetry field encoded as `null` rather than omitted |
| `EventStream` | 3 | the backoff reset on a socket rather than on an established stream; a revoked credential retried while a server fault gives up; the `connected` frame treated as a wake-up |
| `Enroller` | 6 | a second token spent by a device that already holds a credential; a 200 with no credential stored anyway; a restarting backend confused with a spent token; cleartext accepted (for any host, and regardless of build type); the URL checked as a string instead of a parsed host |
| `Synchronizer` | 9 | the desired state computed from the server's instant instead of the device clock; the fetched policy cached after the apply rather than before; an empty policy applied when there is neither fetch nor cache; the applied version advanced after a failed apply; the version the server echoed believed rather than the one read back from the cache |
| `StateApplier` | 7 | the composite loop stopped at the first problem; the applier name dropped from the merged key; a thrown applier logged instead of reported; the `missing` and `stillForbidden` reports dropped; the pre-sync floor applied instead of what the server asked for |

Two of those breaks are worth reading as a pair, because they are the same failure in opposite
directions and only one of them looks like a bug:

- **`Synchronizer` believing the policy version the server echoed back.** The heartbeat then claims a
  version the phone may never have applied. Nothing local goes wrong; the console shows a device
  that is up to date because the device repeated the number it was sent.
- **`Synchronizer` advancing the applied version after an apply that failed.** Same end state,
  arrived at from the other side. The test that catches both — *a failed apply does not advance the
  version the heartbeat claims* — is the one assertion in this phase that the console's correctness
  rests on.

**One real defect came out of writing these tests, in `RestrictionApplier`.** A restriction the
platform *threw* on is recorded in `failures` and also appears in `missing`, and the code wrote
`failures` first — so `missing` overwrote the specific reason with *"requested, accepted, and not in
effect"*. The parent would be told the platform had agreed and quietly done nothing, when it had
said no out loud and given a reason. `missing` is now filtered against `failures`, and the test
asserts the message both starts with `add: ` and does **not** contain `accepted` — a test that only
checked the key was non-empty would have passed against the defect.

### 5.5 calibration — 39 breaks, 38 red on the first pass, 39 after one test was added

`5.5` is the three policy managers the phone applies through — `policy/AppSuspensionManager.kt`
(with its pure `AppSuspensionPlanner`), `policy/ChromePolicyManager.kt`, `policy/DnsPolicyManager.kt`
— plus the three appliers in `sync/StateApplier.kt` that drive them. Baseline **154 tests green**;
39 mutations, one at a time, each followed by a restore.

| unit | breaks | representative |
|---|---|---|
| `AppSuspensionPlanner` | 6 | suspend everything wanted, ignoring what is already suspended; never reveal; honour the server's list even for protected packages; drop the packages that are not installed |
| `AppSuspensionManager` | 8 | trust the installed-package read unconditionally; suspend before releasing; believe an accepted suspension; let the hiding reason overwrite the suspension reason; charge only the first package of a batch that threw |
| `ChromePolicyManager` | 8 | misspell `URLBlocklist`; YouTube restricted mode moderate rather than strict; incognito enabled; the blocklist unsorted and undeduplicated; compare a stored `String[]` to a `List` with `equals`; skip the read-back |
| `DnsPolicyManager` | 9 | treat an empty host as a host; re-apply a host already in effect; report `HOST_NOT_SERVING` without naming the host; believe the return code; send the host untrimmed; report a throwing platform as clean |
| the three appliers | 8 | drop the hidden half of the desired state; drop the accepted-and-not-in-effect problems; allowlist the whole URL rather than its host; apply an empty host whatever the policy says |

Thirty-eight went red naming at least one test. **One came back `NOT A GUARD`**, and it is the most
useful line in the table:

> `plan: release only what is still visible (the original defect)` —
> `release = currentlySuspended.filter { it !in wantSuspended }` → `… && it in installed`

The whole suite stayed green under it. Reading why: every existing test that asserts a release does
so for a package the `installed` read also reports, so gating the release on `installed` changes
nothing any of them can see. The one case it does change is a package that is **no longer wanted and
has gone invisible** — and that case had no test.

The failure it lets through is the unrecoverable-restraint shape this project exists to avoid. A
package is suspended by a bedtime rule; the platform stops reporting it in `getInstalledApplications`
(suspension and hiding both make packages invisible to some queries); the parent deletes the rule.
The console shows no rule. The phone keeps the app suspended, forever, because a read that came back
short is being used to decide what may be undone. Nothing is red anywhere.

The neighbouring test — *a suspended package that went invisible is not released* — is its mirror
image and is why `release` is computed against what is *wanted* rather than against what is
installed: while the package is still wanted, an invisible read must not hand the app back. Both
directions are needed, and having only one is what made the guard blind: one rule that is right for
the first case and permanently wrong for the second.

Fixed by adding *a package no longer wanted is released even after it went invisible*, not by
weakening the mutation. Re-calibrated: with the mutation applied, `155 tests completed, 1 failed`,
and the one failure is the new test — confirming both that it binds to this change and that no other
test in the class does. Restored byte-identical against the pre-sweep backup, suite green.

### The guard the calibration broke — `run_all.sh`'s class-comparison check

Re-running `tests/run_all.sh android-unit` after the fix reported **NOT MEASURED**:

```
gradle exited 0 but these test classes exist in src/test but reported nothing: StateApplierTest
```

This was not caused by the new test. The check compared the classes that *reported* against the
`*Test.kt` files that *exist*, taking the file name as the unit. `StateApplierTest.kt` declares five
classes — `CompositeApplierTest`, `RestrictionApplierTest`, `AppApplierTest`, `ChromeApplierTest`,
`DnsApplierTest` — and **none of them is called `StateApplierTest`**, so a suite in which all five
ran and passed read as silent. A false red.

It was also a false green in the other direction, which is the worse half.
`AppSuspensionManagerTest.kt` declares two classes and only one name was ever demanded, so
`AppSuspensionPlannerTest` — the class that carries fourteen of the assertions above — could have
stopped being discovered entirely with the guard still green.

The unit of comparison is now the **declared class**, parsed out of each file: 21 classes to account
for rather than 18 files. `private` is deliberately not excluded from the parse, because making a
test class private is one of the ways it stops being discovered, and a rule that skipped private
classes would lose sight of the class on both sides at once. `abstract` is excluded, since a base
class legitimately never reports on its own.

Calibrated, three probes:

| probe | result |
|---|---|
| a declared class that reports nothing (`@Test` annotations removed from `BackoffTest`, so it compiles and runs and simply contributes no testcases) | **RED** — `these test classes exist in src/test but reported nothing: BackoffTest (BackoffTest.kt)`, with **gradle exiting 0**, which is the exact false green the check exists for |
| a `*Test.kt` that declares no test class at all (the shape a refactor leaves behind) | **RED** — `these *Test.kt files declare no test class: ProbeTest.kt` |
| the unmodified tree | **GREEN** — `155 tests in 21 classes, all green` |

The first probe was first attempted by making the class `private`, and that is worth recording
because it is a probe that *looks* like it worked: the layer went red. It went red because the build
failed — a different mechanism rescuing the test, which proves nothing about this guard. The probe
was replaced with one that compiles.

### The app's shape — four guards added after 5.5, eleven breaks, eleven red

`ManifestAndPlatformCallsTest` already checked three properties that no runtime test on an
unprovisioned device can reach. Four more belong in the same place, because each protects something
whose failure is silent on a real phone:

| guard | what it stops |
|---|---|
| `the permissions the shipped app asks for are exactly the ones it needs` | an *added* permission is scope creep on an app that runs with the platform's trust; a *removed* one is worse — dropping `QUERY_ALL_PACKAGES` leaves `getInstalledApplications` filtered, so every blocked package reads "not installed", every sync reports clean, and the console becomes fiction with nothing red anywhere |
| `only the files written to check the platform hold a device-policy handle` | the read-back that catches accepted-and-not-applied lives in the gateways, so a `DevicePolicyManager` obtained anywhere else is a call whose result nobody verifies |
| `the install watcher is registered at runtime, not declared in the manifest` | `ACTION_PACKAGE_ADDED` is an implicit broadcast; since Android 8 a manifest-declared receiver for one is **never called**. Moving it into the manifest is the tidier-looking option, compiles, installs, and produces a phone where a newly installed app is unrestrained until the next poll — with nothing in any log, because no broadcast was dropped |
| `cleartext is refused by the shipping config, and the debug carve-out matches Enroller` | `network_security_config.xml` is the only mechanism that catches an https→http **redirect** — `Enroller` refuses a cleartext URL that arrives in a QR, but a redirect is a URL nobody in this codebase ever saw. It binds at runtime on a phone, so an `android:networkSecurityConfig` attribute lost in a manifest edit takes the whole protection with it and changes no test result anywhere |

The permission guard reads the **merged** manifest, which is what ships, and that immediately paid
for itself: the merged manifest declares **eight** permissions, not the seven this project authors.
`androidx.core` injects `io.github.helios57.familyguard.DYNAMIC_RECEIVER_NOT_EXPORTED_PERMISSION` — with a
matching `signature`-level `<permission>` — because `ConnectionService` registers the install watcher
with `ContextCompat.RECEIVER_NOT_EXPORTED`. It is scoped to this app's own package and holdable only
by something signed with this key, so it grants nothing outside the app; it is listed with that
reason rather than discovered later. A second assertion requires the seven `android.permission.*`
entries to come from *this* repo's manifest, so a library supplying one of them keeps the set equal
but still goes red — `QUERY_ALL_PACKAGES` arriving from a dependency is a permission whose lifetime
is that dependency's next version bump.

The handle detector matches on *holding* a `DevicePolicyManager`, not on a list of method names: a
forbidden-name list only ever catches the calls someone thought of, and there is no way to reach the
service at all without first obtaining one. Constant access — `DevicePolicyManager.EXTRA_…` in the
two provisioning activities — is deliberately not a match, because a constant changes nothing on the
device. Three files hold one, each with its reason recorded in the test.

The transport guard checks four things, and breaks 10 and 11 are the two directions of the one that
matters most: the cleartext hosts are written **twice**, once as XML domains in the debug overlay and
once as a Kotlin `setOf` in `Enroller`, and either list alone lets a cleartext control plane through.
Two lists in two languages in two files is the definition of something that drifts, and the file's
own comment already said so — it just had nothing enforcing it. `CLEARTEXT_HOSTS` is read out of the
source text rather than off the constant, because the constant is `private` and a test is not a
reason to widen it. The debug overlay is held to the release shape minus exactly those three names,
including *no user-added CAs in debug either*: trusting them on a bench is the usual advice and it is
wrong here, because it would mean the configuration exercised on a bench is not the one that ships
and a TLS problem would first appear on a child's phone.

Calibrated, eleven breaks, all to the product and none to the test:

| # | break | result |
|---|---|---|
| 1 | manifest: drop `QUERY_ALL_PACKAGES` | **RED** — *the permissions the shipped app asks for are exactly the ones it needs* |
| 2 | manifest: add `ACCESS_FINE_LOCATION`, which nothing asked for | **RED** — same test, other direction |
| 3 | a file outside the three obtains a device-policy handle | **RED** — *only the files written to check the platform hold a device-policy handle* |
| 4 | `ConnectionService` stops holding one (its self-grant of `POST_NOTIFICATIONS` removed) | **RED** — same test, other direction |
| 5 | `PACKAGE_ADDED` added to `BootReceiver`'s manifest intent-filter | **RED** — *the install watcher is registered at runtime, not declared in the manifest* |
| 6 | the runtime `addAction(Intent.ACTION_PACKAGE_ADDED)` replaced | **RED** — same test |
| 7 | the release network security config permits cleartext | **RED** — *cleartext is refused by the shipping config, and the debug carve-out matches Enroller* |
| 8 | the release config adds `<certificates src="user" />` | **RED** — same test |
| 9 | the manifest stops pointing at `@xml/network_security_config` | **RED** — same test |
| 10 | a fourth host added to the debug cleartext carve-out | **RED** — same test |
| 11 | `Enroller.CLEARTEXT_HOSTS` drops `10.0.2.2` | **RED** — same test, other direction |
| | restored | suite green, 8 tests in the class, 0 failures |

**Break 6 is the one that changed the test.** Both source scans originally read the raw file, and
this file's own KDoc explains *why* `ACTION_PACKAGE_ADDED` must be registered at runtime — so the
comment names the token the assertion looks for. Measured rather than assumed: with the pre-fix
version of the guard and break 6 applied, the result is **NOT A GUARD — stayed green**. The comment
outliving the call it describes is the normal way that happens, so both scans now strip comments
first, and break 6 is the calibration of that stripping rather than of the assertion around it.

Two probes had to be thrown away before those six, both for the same reason as the `private` probe
above — the layer went red through a mechanism that was not the guard:

- Removing `grantOwnNotificationPermission` orphans three imports, and `allWarningsAsErrors` turns an
  unused import into a build failure. `DID NOT COMPILE`, not `RED`. The mutation now removes the
  imports with it.
- The second attempt used a regex for the function's KDoc-through-closing-brace. A lazy
  `/**…*/` pattern anchors on the **leftmost** matching comment in the file, so it deleted every
  member between that comment and this function — including `onBind`. The red read
  *"Class 'ConnectionService' is not abstract and does not implement abstract base class member
  'onBind'"*, which is a red about the mutation and says nothing about the guard. Replaced with
  brace-matching.

### 5.6 — screen time, and the alarm the engine had already assumed

Two halves that only look unrelated. FR-3 needs the phone to *measure* how long each app was in the
foreground; FR-4 needs it to *act* at an instant nobody is holding the phone at. Both are places
where the product would keep reporting success while doing nothing.

**The measurement** is seven small files in `usage/`, split so that everything with a rule in it runs
on the JVM: `UsageStatsForegroundReader` is the platform, `SpanFolder` turns its resume/pause events
into spans, `DayAttribution` cuts them at local midnight, `ScreenOnClock` is the monotonic budget,
`UsageLedger` is the arithmetic and the persistence, `EncryptedUsageStore` is where it survives a
process death, and `UsageTracker` is the sequence they run in.

Four decisions carry the requirement, and each is a place the obvious code is wrong:

- **The budget is monotonic, and it is a ceiling** (FR-3.2). What the ledger adds is bounded by
  `SystemClock.elapsedRealtime()`, never by the difference between two wall-clock readings. A child
  who can reach the date setting would otherwise be able to erase a day's usage — or manufacture six
  hours of it — from Settings.
- **Time with the screen off is not time on the phone** (FR-3.3). The clock banks its interval on the
  screen-off broadcast rather than at the next drain, so a night's sleep is not credited to whatever
  app was last in the foreground, and `SpanFolder` closes the open span at the moment the screen went
  off rather than at the end of the window.
- **Not-measured is not zero.** `PACKAGE_USAGE_STATS` is an appop; being Device Owner does not grant
  it and no device-owner API can (`setPermissionGrantState` covers runtime permissions only). Without
  it every query returns nothing — every package reads zero minutes, the daily limit is never
  reached, and the console shows a child who spent the day off their phone, which a parent cannot
  tell from the real thing. The reader answers `null`, and `UsageTick.NotMeasured` carries the reason
  the whole way up. The grant is a one-time step per device and is in DEPLOYMENT.md.
- **What is sent is the day's cumulative total, never a delta.** The server upserts with
  `GREATEST(stored, reported)`, which makes a duplicate, an out-of-order arrival and a retry all
  harmless — but only for cumulative figures. That is also why the totals are persisted: a phone
  rebooting at 19:00 and counting from zero would report *below* what the server holds, `GREATEST`
  would keep the old number, and the whole evening would be invisible to the quota. They live in
  their own encrypted preferences file (not a key in the policy cache's, so that dropping an
  unparseable policy cannot take the day's minutes with it) and are written with `commit`, not
  `apply` — an async write that had not landed when the platform stopped the service restarts the
  counter at zero.

Two senders sit on top, in `sync/`. `UsageReporter` treats **every day still in the ledger as
outstanding when the process starts**, which is the cheapest correct answer to two losses at once: a
POST that fails at 23:58 would otherwise strand the evening forever, because the day never changes
again and nothing would re-send it. `InventoryReporter` (FR-5.1) sends the installed-app list only
when a digest of it changes, does not advance that digest until the server has accepted the send, and
reports `NotMeasured` rather than an empty inventory when the device cannot read its own app list —
an empty list would otherwise unsuspend every blocked app on the next resolution.

The device's own measurement does not replace the server's: `Synchronizer` resolves the quota against
`maxOf(server, device)`. Neither side can roll the other backwards, which is the same rule as the
upsert, applied at the other end.

**The alarm** is the half that was missing entirely. `EnforcementEngine` already computed
`DesiredState.nextChangeAt` — the earlier of the next bedtime edge and, when a daily limit is set,
the next local midnight — and its own KDoc claimed *"the device sets one exact alarm for this instead
of polling"*. Nothing did. The value was produced, serialised, logged, and read by no one; the device
re-evaluated only in `pollWhileAwake`, which runs while the screen is on, every five minutes. So a
phone put down at 20:30 with a 21:00 bedtime enforced nothing at 21:00, and nothing anywhere went
red: the console shows the bedtime, the state carries the instant, the device agrees with all of it
and is asleep.

`EnforcementAlarm` is the rule and runs on the JVM; `AlarmManagerPlatform` is the twelve lines that
touch `AlarmManager`. Three of its decisions are the opposite of the obvious one:

- **An empty instant cancels.** A policy with no bedtime and no limit changes nothing on its own, and
  a wake-up left booked from the policy before it would fire for the rest of the device's life.
- **An unreadable instant does *not* cancel.** Cancelling would trade "the next edge is at an unknown
  time" for "there is no next edge", which is strictly worse and indistinguishable from a policy that
  never changes. Whatever was booked stands, and the line is logged at ERROR.
- **A past-due edge is deferred, not dropped** — floored to `now + 10 s`, so a state that keeps
  producing a past instant cannot become a wake loop.

Re-booking is unconditional on purpose. Skipping an unchanged instant would need the class to know
whether the alarm it booked has already fired; it cannot, and guessing wrong once yields a device
that never wakes again. `onDestroy` cancels, because a wake-up that outlives the service answering it
restarts that service at every edge forever.

`SCHEDULE_EXACT_ALARM` is declared in the manifest and *checked at run time* — it is an appop, and an
app targeting Android 13 or later does not hold it by default. Device-owner exemption is documented
on some versions and is not something this code asserts: `canScheduleExactAlarms()` decides, and the
fallback is `setAndAllowWhileIdle` (delayable, but still doze-piercing), reported as `INEXACT` in the
line the service logs rather than silently. The wake-up is a `getForegroundService` PendingIntent, so
the alarm can start the service into the foreground from the background, and `FLAG_IMMUTABLE` is
mandatory from Android 12.

### 5.6 calibration — 37 breaks, 37 red

Ten test classes, 37 deliberate defects, each one a change to the **product** — Kotlin under
`app/src/main/`, or the manifest — never to a test. The run opens with a positive half: all ten
classes must be green *unmodified*, because a red proves nothing about a suite that was already
failing. Then each break is applied on its own, the owning class is run with
`./gradlew :app:testDebugUnitTest --offline --tests <class>`, and the file is restored before the
next one.

**What this calibration establishes is narrower than the e2e one, and the difference is worth
stating.** The Go calibrations assert a specific *named substring* in the failure, so a case that
went red for an unrelated reason is caught. Here the assertion is that **one named class** turns
red — the `--tests` filter is the scoping — not that a particular assertion inside it fired. A
break that reddened its class through some other test in the same file would be counted. That is
the weaker claim, and it is the claim the table below makes.

| class | breaks | what each one does to the product |
|---|---|---|
| `UsageLedgerTest` | 3 | the monotonic budget stops being a ceiling; nothing is persisted across a restart; pruning keeps the **oldest** days |
| `ScreenOnClockTest` | 4 | time with the screen off is counted; a backwards timestamp subtracts from the budget; a repeated screen-on discards the open session; a repeated screen-off banks the interval twice |
| `SpanFolderTest` | 4 | a resume leaves the previous span open; a stale pause ends the session; arrival order is trusted; screen-off does not close the span |
| `DayAttributionTest` | 3 | a span is not split at midnight; an unknown zone falls back to the device's; the day key uses a second formatter |
| `UsageTrackerTest` | 5 | an unreadable platform reads as **zero usage**; the budget is not the monotonic clock's; the first poll reports rather than establishing the window; a backwards wall clock is measured anyway; and the requirement-level break — **every guard on the screen-off budget removed at once** |
| `UsageReporterTest` | 2 | a restart does not re-send the outstanding days; a failed send is dropped instead of retried |
| `InventoryReporterTest` | 4 | the digest advances before the server accepts; an unreadable list is sent as *no apps*; the digest is delimited rather than length-prefixed; only package names are hashed |
| `SynchronizerTest` | 3 | the device's own minutes are ignored; the two numbers are summed instead of maxed; the device's measurement replaces the server's |
| `EnforcementAlarmTest` | 7 | no next change leaves the old wake-up booked; an unreadable instant cancels the standing wake-up; a past edge is booked with no floor, so it can wake-loop; an inexact wake-up is reported as exact; the offset in the instant is read as local time; a refusal is reported without the platform's reason; an unchanged instant is skipped instead of re-booked |
| `ManifestAndPlatformCallsTest` | 2 | `SCHEDULE_EXACT_ALARM` is not declared; `PACKAGE_USAGE_STATS` is not declared |

Three of them are worth naming separately, because each is a defect that leaves the app *working*:

- **"an unreadable platform reads as zero usage"** is the whole of the not-measured argument in one
  line. Delete `UsageTick.NotMeasured` and the app still runs, still reports, still shows a child on
  the console — reporting a day of no phone use that nobody can distinguish from the real thing.
- **"an unreadable list is sent as no apps"** is the same shape one layer over, and it is worse than
  silence: an empty inventory unsuspends every blocked app at the next resolution.
- **"an unchanged instant is skipped instead of re-booked"** is the one that looks like an
  optimisation. It is the break that would be written by someone tidying up, and it produces a phone
  that stops waking after the first edge it handles.

The two manifest cases are the only ones whose subject is a declaration rather than a behaviour, and
they exist because nothing else in the suite can see a missing `<uses-permission>`: the Kotlin
compiles, the unit tests pass, and the permission is absent on the device.

### 5.7 — instant commands, and the deadlock that leaves everything green

FR-9 is the half of the product a parent actually presses: lock the phone now, ring it, find it,
re-read the policy now. The server side has existed since 3.5 — the queue, the ack endpoint, the push
wake-up — and the console has had the buttons since 4.x. What was missing is the end that runs them.
Until this phase a parent could queue a command, watch the row go `DELIVERED`, and get no answer
ever, because the device fetched nothing and executed nothing.

Same split as everywhere else in Phase 5: the rules are plain Kotlin with JVM tests, and the platform
is four small classes that cannot be tested without a phone. `CommandExecutor` and `CommandQueue` are
the mechanics, `CommandHandlers` is the eight-entry map, `LockManager`, `SirenController` and
`LocationProbe` hold the decisions, and `commands/AndroidPlatform.kt` is the ringtone player, the
vibrator, the main-looper timer and `LocationManager`.

Ten decisions carry the requirement, and every one of them is a place where the obvious code produces
a console that looks right:

- **A command is answered by executing it.** `CommandQueue` binds `fetch` to the executor, so there
  is no call that hands a caller the fetched rows. The server marks a row `DELIVERED` the moment it
  is handed over, and a device that could take delivery without running the command would leave that
  row looking answered for the rest of its life. Two callers, one of them careful, is the shape this
  removes.
- **Each command is acknowledged as it finishes, never in a batch at the end.** A drain of five where
  the third throws would otherwise lose the acknowledgements of the two that had already succeeded —
  a phone that locked, reporting that it did not.
- **An ack that fails is its own category, not a failure.** `ExecutionReport.unacknowledged` means the
  command ran and the phone could not say so. Folding it into `failed` invites the parent to press
  again on a phone that is already locked; folding it into `done` loses it entirely.
- **A non-UUID command id never reaches a request path.** Refused in the executor and again in
  `ApiClient.ackCommand`, because the id arrives off the wire and one layer of validation on a value
  that becomes a URL is one layer.
- **`LOCATE_NOW` delivers the position before it acknowledges the command.** A `Done` whose position
  never arrived is a console that shows nothing and reports success — so a delivery that throws is a
  failed command with the transport's own reason in it. The acknowledgement itself carries no
  coordinate: the position belongs in the row the console draws a map from, and duplicating it into a
  free-text result is two sources for one fact that will disagree.
- **A cached fix keeps its true timestamp.** The parent is shown "here, twenty minutes ago" rather
  than a present tense they would walk somewhere on. Re-dating a fallback to `now` is the single edit
  that turns this feature into a hazard, and it is the kind of edit that looks like normalisation.
- **Not-permitted is not "no position".** A revoked location permission is something a parent can
  fix; a phone in a basement is not. Both arrive as an empty answer unless the probe distinguishes
  them, and `LocationProbe` does — including the permission that is revoked *between* the check and
  the request, which falls through to the cached fix rather than losing it.
- **The siren carries its own deadline, on the main looper.** Five minutes, armed through a `Handler`
  and not the connection's coroutine scope, because the auto-stop has to survive the event stream
  dropping while the phone is ringing — which is precisely the situation the cap exists for. A siren
  whose only stop is a command that can no longer arrive is a phone that screams until someone
  reboots it.
- **The alarm volume is captured at the first start only.** A second `TRIGGER_ALARM` while ringing
  extends the deadline and does not re-capture — otherwise the volume saved for restoration is the
  one the siren itself raised, and `STOP_ALARM` leaves the phone at maximum alarm volume permanently.
- **A phone with no PIN cannot be locked, and says so.** `LOCK_NOW` on a device with no lock-screen
  credential reports the failure rather than a keyguard that is dismissed with a swipe.

Four of the eight handlers — `SYNC_POLICY`, `UNLOCK_DEVICE`, `BLOCK_YOUTUBE_ALL`,
`UNBLOCK_YOUTUBE_ALL` — do their work by re-syncing, and each fails when the sync did. A sync served
from the cache has applied *something* and still failed at the thing the parent asked for; answering
"policy re-fetched" for it is a false green with a person reading it.

**And that is where the one defect no runtime test can reach lives.** `syncLock` is a `Mutex`, and a
`Mutex` is not reentrant. Because those four handlers re-sync, a command drain moved inside
`syncLock.withLock { … }` — which is where anyone tidying the connection loop would put it — takes
the lock a second time through `runSync` and stops there forever. Nothing goes red: the service keeps
its notification, the stream stays open, the console shows a device that is online and heartbeating,
and every command from the first `SYNC_POLICY` onwards sits `DELIVERED` and unanswered. `syncAndDrain`
therefore syncs under the lock and drains outside it, and since `ConnectionService` is Android and no
JVM test can reach it, the source is the only available evidence:
`ManifestAndPlatformCallsTest` *the command drain runs outside the sync lock* brace-matches every
`syncLock.withLock` block and fails if a drain call is inside one. It calibrates itself first — a
literal snippet that *has* the bug must be flagged, the same snippet without the drain must not, the
real file must contain a drain call and a `withLock` at all — so its clean answer on the real file
means something rather than being the answer a reader that found nothing would also give.

**The command types are read out of the Go source, not restated in Kotlin.** A list copied into a
test is a list that agrees with itself. `CommandHandlersTest` parses the `CmdType*` constants and the
`ValidCommandTypes` map out of `backend/internal/store/models.go` and asserts set equality with the
handler map, in both directions, refusing to run at all if it cannot find the file or parses either
side as empty. The failure it exists for is ordinary: the server ships a ninth command type before
the fleet updates, a parent presses the new button, and the console shows a queued row answered —
correctly, by the executor — with "this device does not implement it". The guard turns that into a
red build on the commit that adds the type.

**This is the phase that made NFR-10's permission guard go red on purpose,** exactly as the
traceability table predicted it would. `LOCATE_NOW` needs location, so three permissions were added
to the manifest with their reasons, and the whitelist in `ManifestAndPlatformCallsTest` grew three
entries carrying those reasons. `COARSE` is declared alongside `FINE` because from Android 12 a
FINE-only request is answered with COARSE when the user picks approximate location, and an app that
never declared COARSE then gets nothing at all; `BACKGROUND` because the command arrives while the
phone is in a pocket and this app has no UI to be in front of. The probe is one shot with a 30 s
budget and releases the receiver in a `finally` on both API paths — there is still no polling, which
is the half of NFR-10 that is structural.

Nothing here calls `wipeData` or `setFactoryResetProtectionPolicy`, and the platform-call guard that
would catch either is unchanged: the phone stays factory-resettable.

### 5.7 calibration — 11 breaks, 11 red

Same method as 5.6 — every break is a change to the **product** (Kotlin under `app/src/main/`, or the
Go authority), never to a test, applied one at a time and restored before the next, with the sha256
of every touched file re-checked after each restore. The positive half runs first: **305 tests in 35
classes, 0 failures, 0 errors, 0 skipped**, counted out of
`app/build/test-results/testDebugUnitTest/*.xml` rather than taken from `BUILD SUCCESSFUL`.

**The claim is stronger than 5.6's, and that is the point of the harness.** 5.6 asserted that the
owning *class* turned red, so a break that reddened its class through some other test in the same
file would have counted. Here the harness parses each `<testcase>` element and asserts that the
**named test method** failed. A break that compiles but runs nothing is `NOT MEASURED (nothing ran;
the build failed before the tests)`; a break where the named test did not run is
`NOT MEASURED (the named test did not run)`; a break where the named test passed is
`STILL GREEN — this test does not bind to what was broken`. None of the eleven landed in those
states, and the harness prints which of the *other* tests in the class went red alongside, so a
break that reddens four things is visible as one that reddens four things.

| # | break to the product | named test that had to fail | result |
|---|---|---|---|
| 1 | `CommandHandlers.LOCATE_NOW` renamed to `"LOCATE_NOW_V2"` | *the handlers implement exactly the command types the server accepts* | **RED** |
| 2 | `backend/internal/store/models.go` renamed away entirely | same test — proves it cannot pass by comparing against nothing | **RED** |
| 3 | the executor's id check weakened to `&& command.id.isEmpty()` | *a command whose id is not a UUID is refused rather than executed* | **RED** |
| 4 | acknowledgements deferred into a list and flushed after the loop | *acknowledges each command as it finishes, not in a batch at the end* | **RED** |
| 5 | `CommandQueue.drain()` swallows the fetch exception | *a failed fetch propagates rather than reporting an empty queue* | **RED** |
| 6 | `timer.arm(…)` removed from `SirenController.start()` | *it silences itself when no stop ever arrives* (3 others in the class also red) | **RED** |
| 7 | a second `TRIGGER_ALARM` re-captures `restoreVolume` | *a second trigger extends the deadline and does not restart the tone* | **RED** |
| 8 | the cached fix re-dated with `.copy(capturedAtEpochMillis = now())` | *no fresh fix falls back to the cached one, with its true age* | **RED** |
| 9 | `LockManager`'s no-credential branch returns `null` | *a phone with no PIN reports a failure rather than a lock that does not hold* | **RED** |
| 10 | `locate()`'s report-failure catch emptied | *a position that cannot be delivered is a failed command, not a successful one* | **RED** |
| 11 | `syncAndDrain`'s body wrapped in `syncLock.withLock { … }` | *the command drain runs outside the sync lock* | **RED** |

Break 2 is the one that makes break 1 mean anything. A cross-language guard that reads a file it
cannot find has two failure modes that look identical from the outside — the sets agree, or nothing
was compared — and the second is what a moved file, a renamed package or a test run from a different
working directory produces. The guard throws rather than passing, and break 2 is the measurement of
that.

Break 11 is the only one in the suite whose subject is a *source file's shape* rather than a
behaviour, for the reason given above: there is no test that can reach it, and the bug it describes
has no symptom. Its failure message names the offending line —
`expected:<[]> but was:<[if (tick.pending > 0) drain(commands, tick.pending, why)]>` — and no other
test in `ManifestAndPlatformCallsTest` went red with it, so the guard binds to that line and not to
the file being edited at all.

### 5.8 — offline recovery, and the escape hatch that must not become the brick

FR-12 is the path the whole product is judged by on the day it goes wrong: a child locked out of
their school app on a Monday morning, in a car park, with the phone showing no bars. Every other
feature may degrade when the server is unreachable. This one has to work *because* the server is
unreachable, and it has to work for a parent who is already having a bad morning.

Eight files, seven suites, **63 JVM tests**, and no Android import outside the two adapters — which
is what makes the whole feature testable without a device:

| file | what it is |
|---|---|
| `RecoveryCode.kt` | normalisation, PBKDF2-HmacSHA256 derivation, constant-time comparison, `RecoveryVerifier` |
| `RecoveryLockout.kt` | the escalating, persisted, capped rate limit |
| `RecoveryMode.kt` | the released flag, and `releasedState()` — what a recovered device enforces |
| `RecoveryJournal.kt` | the queue of attempts made offline, flushed oldest-first |
| `RecoveryController.kt` | the four of them in the one order that has no bad prefix |
| `RecoveryActivity.kt` | a text field and a button, and nothing else |
| `AndroidRecovery.kt` / `AndroidRecoveryStore.kt` | the adapters: the same wiring `ConnectionService` uses, and one encrypted file |

Five decisions in it are load-bearing, and each is written down where the next person will be
standing rather than only here.

**Normalisation is wider than it looks, and it is wide on purpose.** The code is read off one screen
and typed into another, so it arrives with whatever the source and the keyboard did to it: a
non-breaking space pasted from a web console, an en dash autocorrected out of a hyphen, an invisible
soft hyphen from a line wrap. Both sides fold before comparing, and *both sides must fold
identically* — a device that rejects a code the console accepted is indistinguishable, from the car
park, from a device that is broken. Go and Java disagree about what a space is: `unicode.IsSpace`
strips **U+0085, U+00A0, U+2007 and U+202F**, which `Character.isWhitespace` does not, and
`Character.isWhitespace` strips **U+001C–U+001F**, which `unicode.IsSpace` does not. Neither
predicate is usable, so `GO_SPACE` is a hand-written set on the Kotlin side and the Go side spells
out the same one. The same trap on the other axis: `strings.ToUpper` is simple case mapping and
`String.uppercase()` is full case mapping, so `ß` becomes `SS` in Kotlin and stays `ß` in Go. The
Kotlin uses the simple mapping explicitly. None of this is reasoning that should be trusted —
`recovery-vectors.json` is 60 cases replayed by `RecoveryVectorsTest` on the JVM and embedded by the
Go side, and each of the four divergences above has its own case. **The cases do not come from
either implementation.** Every `normalize` expectation is a literal in
`tools/gen-recovery-vectors.py`, written from the rule in `tokens.go`'s doc comment rather than from
running it; every `derive` case carries its canonical form as a literal too, and the generator
hashes *that* with Python's `hashlib`. So the digests are evidence about RFC 8018, and the
normalisations are evidence about the specification — vectors taken from Go would have asserted only
that Go does what Go does, which is the shape of a test that cannot fail.

**The code is ASCII, and a Go test is what keeps it that way.** A JCE provider turns the `char[]`
password into bytes, and providers disagree above U+007F — the JDK encodes UTF-8, Bouncy Castle's
PKCS#5 v2 scheme has historically taken the low byte. `TestRecoveryAlphabetIsAscii` lives in
`backend/internal/auth`, not in the DPC, because the alphabet is the server's and a test here could
only assert against a copy. It also pins the read-aloud exclusions (`I L O U 0 1`), which are a
product decision a tidying edit would otherwise undo silently.

**The lockout is capped, and the cap is the requirement.** ~100 bits from a 30-character alphabet is
not brute-forceable, so the rate limit exists for the shoulder-surfing case: a child who saw four of
the five groups is 30 guesses from the fifth, which is under a minute unthrottled. Two free
mistakes, then 30 s / 2 min / 10 min / 1 h, and never longer than an hour however many failures
accumulate (NFR-6). The clock is the other way this could become permanent and it is handled rather
than assumed away: a stored deadline further out than the cap cannot have been written by this code
against this clock, so the clock moved and the deadline is dropped — without which a phone whose NTP
sync jumped it back a year would refuse every attempt for a year.

**A recovered device enforces nothing, stays enrolled, and stays factory-resettable.**
`releasedState()` is `DesiredState()`, and `ReleasedStateTest` encodes it with `encodeDefaults` and
refuses any field that is not `false`, `0`, `""` or `[]` — so a future field whose neutral value is
`true` fails the build rather than shipping a recovery that leaves one restriction standing. It does
*not* dismiss the keyguard, because no platform call does that for a device owner on API 26+; what
`locked = false` buys is that the standing parent lock stops re-asserting, so the device PIN gets the
person in. It does *not* un-enroll: the next successful sync takes the phone straight back under
management, which is the difference between a bad morning and re-provisioning from scratch. And FRP
is still off (§5.2), so the physical escape hatch behind the escape hatch is intact.

**The release ends when the server is reached, not when the network is.** `RecoveryMode` has no
expiry — a recovery that quietly re-enforced itself twenty minutes later is the same brick with a
delay — and `Synchronizer.applyFrom` clears it only when the policy it is applying came from the
server. A phone can have four bars and still be refused, and a refusal is exactly when the device
must stay released. `SynchronizerTest` binds all four arms of that: server policy clears, cache does
not, an unreachable server does not, and a *refused* server does not.

**The order after a code verifies has no bad prefix.** Journal the attempt, set the flag, apply the
release. Killed after the first, the device still enforces and has the attempt to report; killed
after the second, it is not yet released but will be on the next sync or alarm, because
`Synchronizer` reads the flag on every path. Only the reverse order has a prefix that is a real
failure — a phone released with no record of why and no flag to end it.

### 5.8 calibration — 58 breaks (47 Kotlin, 11 Go), 58 red

Same contract as 5.7: every break is a change to the **product**, applied one at a time, restored in
a `finally` with the file's sha256 re-checked, and the named test must go red. The positive half runs
first — **42 classes, 375 tests, 0 failures, 0 errors, 0 skipped**, counted out of
`app/build/test-results/testDebugUnitTest/*.xml` — and every suite is re-run green after the last
restore. Exit is 0 only if nothing stayed green **and** nothing was not measured. The harness is a one-off
mutation script, run once per half and not checked in — see [the note on calibration
harnesses](#a-note-on-the-calibration-harnesses).

**The harness itself had to be rebuilt first, and that is the finding worth keeping.** Its earlier
form was hard-wired to one source file and one suite, so every break could only ever name the
cross-language vector suite. Under that version the `usable()` work-factor break was reported as
"stayed green" — not because the guard was missing, but because the only test the harness could name
was one that could not reach it. A harness that cannot name the right test manufactures false
findings exactly as readily as a missing test manufactures false greens. Each break now carries its
own **(file, suite, test)** triple, over 8 product files and 9 suites, and three pre-flight checks run
before any Gradle time is spent: every file exists, every anchor string appears **exactly once** (this
caught a Synchronizer anchor that matched two different call sites), and every named test actually
exists in its suite.

Kotlin, by the file broken — all 47 red:

| product file | breaks | the kind of thing broken |
|---|---|---|
| `RecoveryCode.kt` | 11 | each of the four Go/Java normalisation divergences, the alphabet-adjacent folds, a 128-bit key, `isEqual(derived, derived)`, standard vs RawURL base64, the empty-fold refusal |
| `RecoveryVerifier` (same file) | 6 | `iterations >= 0`, the URL alphabet seen through the salt, deriving at the verifier's own work factor, a throwing derivation escaping instead of denying, verifying the entry unfolded |
| `RecoveryLockout.kt` | 11 | the policy numbers, the free-attempt boundary, the past-the-end fallback, non-accumulating failures, an unpersisted deadline, `recordSuccess`, the off-by-one at the end of a wait, both sides of the clock-repair boundary |
| `RecoveryJournal.kt` | 6 | `take` vs `takeLast` on overflow, saving the queue unchanged, carrying on past a retryable refusal, `failed`/`dropped` swapped both ways, a `RuntimeException` escaping `flush` |
| `RecoveryMode.kt` | 3 | re-stamping the release instant, writing on a no-op clear, a released state that leaves one restriction standing |
| `RecoveryController.kt` | 4 | the `usable()` check removed, journal and release swapped, journalling a locked-out submission, not clearing the failure count on success |
| `Synchronizer.kt` | 5 | all four arms of "clears on server policy only", plus the release claiming the version it just stopped enforcing |
| `RecoveryActivity.kt` / `AndroidManifest.xml` | 2 | the recovery screen reading an extra from whatever started it; the launcher entry answering a second action |

Go, all 11 red: the pre-widening normaliser, the U+001C mirror, full case mapping, an off-by-one work
factor, a 16-byte key, verifying an entry that folds to nothing, verifying anything that derives
without error, a non-ASCII character in the alphabet, `O` restored to it, a hyphen in it, and the
vector file made all-rejecting — the last of which is what stops a constant-`false` verifier from
passing the vector suite.

**One break stayed green on the first full run, and it found a real hole.** Raising
`FREE_ATTEMPTS` from 2 to 3 left the entire `RecoveryLockoutTest` suite green — including the test
named *the third mistake is the first that waits*, which then cheerfully asserted that the fourth
one was. All 23 references to the constants in that file went through `RecoveryLockout.FREE_ATTEMPTS`
and `RecoveryLockout.ESCALATION` as symbols, which is what keeps the suite readable and what made it
blind: a suite parameterised by the constant it is checking re-reads the new value and agrees with
it. The numbers belong to FR-12.4 and NFR-6, not to the class, so
`the escalation is the one FR-12_4 specifies, in literal milliseconds` now states them as literals in
one place. A second break was added alongside it — the first escalation step changed from 30 s to
60 s, which no other test in the suite can see either — and both are red. That is the pattern to
watch for elsewhere: **a constant referenced symbolically by every one of its tests is a constant
with no test at all.**

### 5.9 — the status screen, and the fact that must not render as zero

FR-13.4 asks for a status screen on the phone. The reason it is worth a phase of its own is that it
is the only place a question the console cannot answer gets asked: **the console knows what it sent,
and only the phone knows what happened to it.** Every fault this repo has spent its calibrations
hunting — a policy that arrived and was never applied, a device that has not been reached in a week,
a usage grant that was never given — is invisible from the server, because from the server all three
look like a quiet phone.

The screen is built from a pure model (`status/DeviceStatus.kt`, no Android import) fed by a device
adapter (`status/AndroidDeviceStatus.kt`, no judgements). The split is what lets twenty-five cases be
asserted in 0.2 s on the JVM instead of on a handset, and it is why the composer could be broken
thirty-two ways below without an emulator.

**`StatusLevel` has three values, and the third one is the product.** `OK`, `ATTENTION`, and
`NOT_MEASURED` — not two levels with an unknown folded into one of them. A phone whose usage grant
was revoked at breakfast measures zero minutes all day: the quota is never reached, the console shows
a child who spent the day off their phone, and nothing anywhere is red. That is not a hypothetical
failure mode, it is what `PACKAGE_USAGE_STATS` does — it is an appop rather than a runtime
permission, `setPermissionGrantState` cannot grant it, and a revoked appop does not make
`queryEvents` throw. It makes it return nothing at all. So `ForegroundReader.spans()` answers `null`
for "could not measure" and an empty list for "measured, and there was none", `screenTimeToday`
propagates only the first as `null`, and `deviceStatus` renders it as a `NOT_MEASURED` line carrying
the reason verbatim.

`NOT_MEASURED` is drawn exactly as prominently as `ATTENTION` — same `⚠`, same `colorError`, same
"Needs attention." prefix in the `contentDescription`. A phone that cannot measure screen time is not
in a better state than one whose policy is stale; it is in a state where nobody knows, and drawing
that as a muted aside is how it stays unnoticed for a month. Colour is never the only signal, for the
parent reading the phone in sunlight and the one who is colour-blind.

Three smaller decisions the calibration then pinned:

- **The contact stamp is written on receipt and nowhere else.** Not in `applyFrom`, which also runs
  for a cached policy and would report a phone that has not seen the server in a week as having
  reached it a minute ago; and not after the apply, because a policy that arrived and failed to apply
  is still proof the network and the credential work. Three of the four Synchronizer breaks are about
  where the stamp is *not* written, because that is where this line learns to lie.
- **A clock this device cannot parse stamps 0, which renders as "never".** An unparseable clock is a
  bug, and a bug must surface as a phone that looks out of contact, never as one that looks freshly
  synced.
- **There is no device token on this screen, and there must never be one.** It is the launcher entry,
  reachable by anyone holding the phone, so anything on it is public to the child. The device id is
  not a secret and the parent needs it to match the phone to the console; the token would let whoever
  read the screen impersonate the device. Three independent guards, deliberately at different layers:
  the composer's output (`DeviceStatusTest`), a source scan of the three files that build the screen
  (`ManifestAndPlatformCallsTest`), and the rendered view tree on a real device (`StatusScreenTest`).

The block sits **below** the recovery code field. The screen's reason for existing is the escape
hatch, and the person who needs that is standing over a phone with the keyboard already covering half
of it; making them scroll past a status report to reach the field would be the status report costing
somebody the thing they came for. A parent who came to *read* scrolls once.

### 5.9 calibration — 38 breaks (32 JVM, 6 on-device), 38 red

Two harnesses, one per half, the same shape as 5.8: each break carries its own
`(file, anchor, replacement, suite, test)` and the named test must go red.
**32 breaks, 0 stayed green, 0 not measured** on the JVM; **6 breaks, 0 stayed green, 0 not
measured** on the device-owner emulator. Every suite green again after every restore, verified by
sha256 against the pre-break file.

Most of the JVM breaks are one-token *collapses*, because that is the shape this phase's bug takes:
null folded to zero, `NOT_MEASURED` folded into `OK`, "could not ask" folded into "the answer is no",
`>` widened to `!=`, a 24-hour threshold widened to 48, `>=` narrowed to `>`, a rounding that goes up
instead of down. None of them is a compile error and none would survive a careful reader — which is
exactly why they need a test rather than a reader.

Two breaks **did not compile on the first pass and were rewritten**, because a break that does not
compile has measured nothing. Both replaced a null check with `if (false)`, which deletes Kotlin's
smart cast and makes the *other* branch a type error. The collapse had to be expressed inside the
branch instead — which is also the honest shape of the bug: nobody deletes the null check, they
answer it with a zero.

**The six on-device breaks exist because five failures leave every JVM test green:** a row inflated
into a container that was never found, a control too small to hit, a value laid out wider than the
screen, a token that reaches the rendered tree, and — the one this phase is named for — a usage grant
that is never actually read. Each was broken and observed red: the reader returning `emptyList()`
instead of `null`, the gatherer answering `0L`, only the first row added, an 8 dp button, a 900 dp
value, and the token rendered beside the id.

#### Three instrument failures, and what they cost

The on-device claim was almost recorded as a product bug. Checking it by hand, the screen kept
reporting `0 minutes` with the appop revoked — the exact false green the phase exists to prevent —
and it was wrong three times over, each failure silent:

1. `uiautomator dump` was sent to `/dev/null` and the file then `cat`-ed. A dump that fails leaves the
   *previous* dump in place, so the read-back is byte-identical to a successful one.
2. **`am force-stop` is a no-op on a device-owner app.** Same pid before and after — an active device
   admin is protected from it. Every relaunch was delivering an intent to the already-running
   activity, which does not re-run `onStart`, so the screen being read had been rendered *before* the
   grant was revoked.
3. The full JVM calibration was left running in the background while an instrumented build ran
   against the same Gradle project. A calibration harness mutates the sources the other build
   compiles; the 5/5 green that came back was taken from an APK of unknown provenance and had to be
   discarded and re-taken. **Never run two Gradle jobs against this project at once.**

The product was correct the whole time. What was missing was an instrument, and the fix is
`UsageAccessTest`: it toggles the real appop through `uiautomation`, **reads the mode back from the
system** rather than trusting `appops set` (which prints nothing on success and nothing on failure),
asserts the granted half first so a reader hard-wired to return `null` cannot pass, and restores the
grant in `@After` — a device left revoked would measure zero screen time for every later run, which
is the bug itself.

The same pass found `StatusScreenTest`'s token check returning early when the device is not enrolled:
a phone with no token cannot leak one, so it passed having asserted nothing — and that is the *usual*
state, since no CI device or fresh emulator is ever enrolled. It now seeds its own credential, asserts
the seeded id reaches the screen (so a missing enrollment block cannot masquerade as an absent token),
and restores whatever was there before.

**And one break that never ran at all.** The new source-scan guard was written through a Python
heredoc, which turned `"""\bdeviceToken\b"""` into a regex delimited by two **backspace characters**
— a pattern that matches nothing, in a guard whose whole job is to match. It would have reported every
file clean forever. The guard's own positive calibration half is what caught it, in the first run.
The repo was then swept for control characters in `*.kt,*.go,*.xml,*.md,*.ts,*.py`; this was the only
occurrence. It is the cheapest possible reminder that **a scanner is only as trustworthy as its last
demonstrated match.**

### 5.10 — the requirements document, wired to the code that claims it

Every requirement id in this repository was, until now, a comment nobody checked. `grep -rln
"REQUIREMENTS.md"` over `*.kt *.go *.sh *.py` returned **nothing**: not one guard, test or script had
ever opened the document the whole project is written against. The citations were decoration.

`RequirementCitationsTest` closes that, in both directions:

- **Every id cited anywhere in the repository exists.** It found four that did not — `FR-13.4`, cited
  in twelve places across Kotlin, Go and Markdown while the requirement was never written down at
  all; and three misnumbered ids in `backend/internal/store` — one `.1` and two `.4` sub-clauses that
  were never written, where the requirements actually meant are FR-14, FR-4.2 and FR-9.2. A wrong
  citation is worse than none: it is a claim that a requirement is covered, pointing at a requirement
  that does not exist, and the reviewer who follows it lands nowhere and assumes they mistyped.

  *This document is scanned too, which is why the paragraph above describes the three bad ids rather
  than spelling them. Writing them out here would re-create exactly the dangling citations it is
  reporting — the guard said so, by name and by file, the first time this section was written.*
- **Every requirement is claimed by something.** This is the half that found the real defect. Eight
  ids were defined and cited by nothing, and seven turned out to be implemented-but-unmentioned —
  FR-1.5, FR-1.6, FR-2.4, FR-4.3, FR-11.2, FR-12.1 and NFR-13 each got the sentence that names them
  in the code that satisfies them. **FR-2.2 was not.** See 5.11.

Two traps in the guard itself, both fixed before it was believed:

- It flagged **its own source**, whose KDoc names the four bad ids and one deliberately fabricated
  one. The exemption is one path, and it is paired with an assertion that the exempted path still
  resolves to a real file — so renaming the test makes the exemption fail loudly instead of quietly
  widening to nothing.
- `.kts` was missing from the scanned extensions, so `app/build.gradle.kts` — the file that carries
  the `minSdk` NFR-13 cites — was invisible. A scanner is only as wide as its extension list, and the
  list is the part nobody re-reads.

It also has a *"the scan read the repository"* test that fails if either set is implausibly small, if
any of `kt`/`go`/`xml`/`md` stops appearing among the citing files, or if a deliberately fabricated id
is ever found to be "defined" — because two empty sets compare equal, and a walk that resolved the
wrong directory would otherwise be the greenest result in the file.

The document was wrong in one more place, found the same way: **NFR-13 said "Android 8.0 (API 26)
minimum" while `minSdk` is 29.** `setGlobalPrivateDnsModeSpecifiedHost` is API 29, so on 26–28 FR-6.1
cannot be met at all — the app would install, enforce everything else, and leave filtering silently
off. Refusing to install is the honest behaviour, and the requirement now says so.

### 5.11 — FR-2.2, the requirement nothing implemented

The second half of the traceability sweep found one id that no comment could fix. `setAutoTimeRequired`
appeared **nowhere in the repository** — a case-insensitive search for `autotime|auto_time|AUTO_TIME`
matched exactly one line, the requirement itself.

It matters more than its two lines suggest. Bedtime and the daily quota are both computed from the
device clock (`Synchronizer.now()`), and `DISALLOW_CONFIG_DATE_TIME` — which *is* in the baseline —
only stops the setting being **changed**. It therefore freezes whatever state the phone was
provisioned in: a device that arrived with automatic time off keeps a clock nobody corrects, the
restriction locks that in, and a child who sets it back an hour each evening never reaches the quota
and never enters the bedtime window. Nothing is red anywhere. The console shows a child who simply
stopped using their phone at 20:00.

Implemented on the pattern the DNS and hardening code already use: a `ClockGateway` interface, a pure
`ClockPolicyManager` that holds every decision and is covered by JVM tests, and a `DpmClockGateway`
adapter thin enough to have no branch to get wrong except the one it exists for. Three decisions worth
recording:

- **It lives in `HardeningManager.applyBaseline()`, not in its callers.** FR-2 is *"applied at
  provisioning and re-applied on every boot"*, and `applyBaseline` is already invoked from exactly
  those two places. A branch at each call site would be a second place to forget.
- **The constructor parameter is required, not defaulted.** The default that makes a call site compile
  is the one that silently drops half of FR-2 — the same reasoning `Synchronizer.recovery` is required
  for. It cost eleven test edits and buys a wiring that cannot forget the clock and still build.
- **The sync path deliberately does not touch it**, and reports `clock = null` rather than a success.
  Automatic time is not a user restriction, so no desired state can carry it; claiming a pass for
  something never looked at would let a fifteen-minute sync paper over a genuine baseline failure.

The version split is the one thing no JVM test can execute, so it is asserted by reading the source.
`setAutoTimeRequired` still compiles on every supported version, which makes collapsing the split look
like a simplification — and on API 30+ it means *"forbid changing automatic time"* rather than *"turn
it on"*, a lock on the wrong position and precisely the failure FR-2.2 exists to prevent. Calibrating
that break turned up a second guard nobody wrote: **`-Werror` plus a `@Suppress("DEPRECATION")` scoped
to the API-29 branch alone means the naive collapse does not compile at all.**

### 5.10–5.11 calibration — 19 breaks, 19 red

Eighteen product breaks plus the allowlist entry, each applied to the shipping code, run, and
restored; a break that failed to apply aborted the harness, and a break that failed to compile was
recorded as **NOT MEASURED, not a red**. Every one was observed red by at least one named test, and
the harness re-ran the restored tree at the end to prove it had put everything back.

The three that were most worth doing, because each is a shape that passes every other test:

- **The read-back replaced with `true`.** Six tests red. This is the failure the class exists for: an
  OEM that accepts `setAutoTimeEnabled` and does nothing is indistinguishable from one that complied.
- **`ClockOutcome.ok` returning `true`.** Six tests red — the whole point of a three-state outcome is
  lost the moment `ok` stops reading `failure`.
- **The source scan pointed at a file that is not there.** All three of its own tests red, including
  the *"the scan read the gateway"* half. A scan that reads nothing finds no violations, which is
  byte-identical to a clean result.

One break was recorded **NOT MEASURED** on its first form and re-run in a form that compiles: see the
`-Werror` note above. It is the only case in this project where the compiler turned out to be a
stronger guard than the test written for it.

## Phase 6 — Test suites

| # | Task |
|---|---|
| 6.1 | `tests/e2e/run.sh` — PostgreSQL in Docker, local OIDC issuer, real server binary; exit 0/1/2 |
| 6.2 | Journey tests covering FR-1 … FR-14 |
| 6.3 | Negative controls: unauthenticated, bad signature, wrong aud, wrong iss, expired, non-allowlisted parent, replayed enrollment, cross-device access, oversized body, rate limit, WS without auth |
| 6.4 | `tests/run_all.sh` — secret scan, Go unit, container image, Go e2e, Android unit, Android instrumented; reports 2 for any layer not executed |
| 6.5 | **Calibration pass** — break each guard, record the red, restore |

Status: **done and calibrated.** Measured in **one invocation with no layer named**
(`tests/run_all.sh`, rc=0) — naming layers is how a sweep's scope quietly narrows, so the number that
matters is that every layer ran: the secret scan; backend build / vet / vet -tags integration / test
/ gofmt; **12 image assertions, 0 failed**; the e2e suite against a real server binary, a real
PostgreSQL and a real browser; the Android unit suite; and the instrumented layer on a device-owned
emulator across a real `adb reboot` — **8 tests provisioned, then 1 as the boot left it**, which is
every one of the 9 methods `src/androidTest` declares. The Android unit numbers are counted from
`app/build/test-results/testDebugUnitTest/*.xml`, not from Gradle's summary line — `BUILD SUCCESSFUL`
is what a task that ran nothing also prints.

Last re-measured **2026-08-18**, all layers, in one invocation with no layer named:

| Layer | Result |
|---|---|
| `secret-scan` | PASS — gitleaks 8.30.1 over the full history, **0 findings, 0 allowlist entries** |
| `backend` | PASS — build, vet, `vet -tags integration`, test, `gofmt` |
| `manifests` | PASS — 10 assertions, 0 failed ([8.1](#81--the-backup-that-had-to-be-restored-to-be-a-backup)) |
| `image` | PASS — 12 assertions, 0 failed |
| `e2e` | PASS — **24 tests**, on `postgres:18.6`, the image the deployment runs ([6.11](#611--the-suite-was-proving-it-about-a-database-nobody-would-run)) |
| `android-unit` | PASS — **450 tests in 51 classes**, the last three being the repository guards ([6.12](#612--fifty-links-between-eight-documents-and-nothing-checking-one-of-them), [6.15](#615--the-versions-the-documents-state-and-the-build-file-that-defines-them)) |
| `android-instrumented` | PASS — **16 tests provisioned, then 1 across a real `adb reboot`**, on an **API 29** emulator, the floor ([7.6](#76--the-first-run-at-the-floor-and-the-two-things-it-found)) |

**`secret-scan` reads the repository, not the tree, and that is why it used to fail here.** In the
checkout that carried the pre-rename history it reported the fixtures and draft documents that were
secret-shaped at the commits that introduced them — every one of them absent from the working tree,
so the same scan restricted to the tree (`--no-git`) reported 0. The finding belonged to the commit,
which is why the honest fix was never an allowlist. This repository begins at one clean commit, so
the distinction no longer bites and the full-history scan passes; the calibration is what keeps that
meaningful — a planted, well-formed AWS key takes it to 1 finding and rc=1, removing it returns 0 and
rc=0. See [6.14](#614--the-calibration-probe-the-scanner-is-allowed-to-ignore) for the probe that did
*not* work and why that mattered more than the one that did.

6.4's class-comparison check was itself found wrong during the 5.5 calibration and rewritten to
compare against *declared classes* rather than file names; the three probes that now make its green
mean something are recorded under [5.5](#the-guard-the-calibration-broke--run_allshs-class-comparison-check).

### A note on the calibration harnesses

Every calibration record in this document was produced by a mutation harness: a script that edits the
**product** one anchor at a time, runs the named suite, requires the named test to go red, restores
the file and re-checks its sha256, and reports *stayed green* and *not measured* as distinct
outcomes from *red*. They fail closed — an anchor that no longer appears in the source is NOT
MEASURED, never a silent skip — which is what stops a harness from reporting a clean sweep of breaks
it never applied.

**They are not checked in, and that is a deliberate trade.** Each one is pinned to the source as it
stood on the day it ran, by exact substring; a month later most of their anchors would be gone, and
what would be left in the repository is a directory of scripts that exit 2 and that nobody runs — the
ballast this project's own rules say to remove. The durable artefact is the record: every break is
listed here with the test that caught it, so re-deriving one costs a `sed` and a suite run.

The exception is [`tests/e2e/calibrate-mobile.sh`](tests/e2e/calibrate-mobile.sh), which **is** checked
in and maintained. It calibrates against the three console asset files rather than against Kotlin or
Go internals, so its anchors are stable, it runs in about four minutes, and its subject — a rendered
page — is the one place in this repository where a guard cannot be read for correctness and has to be
executed to be believed.

The three-valued status is the point of the dispatcher, and the Android layers earned it twice over
— they reported **NOT MEASURED** for the whole of Phases 1–4, because a green that quietly omitted
the half of the system that touches the child's phone would be the most misleading output this repo
could produce, and they report it again the moment a device is provisionable but not provisioned, or
a unit run cannot account for what it ran.

The first thing 6.1 established is that it had never run: `run.sh` was committed mode 644, so every
invocation ended in `Permission denied` and the exit status being read belonged to the `echo` after
it. Once executable it found five defects — a fetched command answered `QUEUED` while the row said
`DELIVERED`; FR-5.4 novelty inverted so that turning free installation off queued the entire
preinstalled catalogue; a usage day that depended on the machine's clock; `"{}"` accepted where an
absent field was meant; and a validation message containing its own `"; "` separator. Four of the
five were in the product, not the tests.

### 6.5 results

18 breaks, each applied to a single line, verified to still compile, run as
`./tests/e2e/run.sh -run '^<Test>$'`, then restored and the tree checked with `git diff --quiet`.
An edit whose substring did not match exactly once was reported NOT MEASURED rather than run — a
sed that matches nothing is itself a false calibration.

| break | test | result |
|---|---|---|
| drop `jwt.WithAudience` from ID-token parsing | `TestIDTokenIsVerifiedNotTrusted` | red |
| drop `jwt.WithIssuer` | `TestIDTokenIsVerifiedNotTrusted` | red |
| stop requiring `email_verified` (`if !verified && false`) | `TestIDTokenIsVerifiedNotTrusted` | red |
| widen the device-scoping in the command ack `WHERE` | `TestOneDeviceCannotActOnAnother` | red |
| raise the body limit 1000× | `TestOversizedBodiesAreRefusedAsTooLarge` | red |
| neuter the rate limiter | `TestRateLimitProtectsTheServer` | red |
| answer every preflight, not only allowlisted origins | `TestCORSAllowsOnlyTheConfiguredOrigins` | red |
| drop `X-Frame-Options` | `TestSecurityHeadersOnEveryAnswer` | red |
| `MinSessionKeyBytes` 32 → 1 | `TestStartupRefusesBadConfiguration` | red |
| stop comparing the OAuth `nonce` | `TestBrowserSignInFailureModes` | red |
| add `no_factory_reset` to the hardening set, guard intact | `TestFactoryResetIsNeverBlocked` | **green — the guard caught it** |
| add `no_factory_reset` **and** remove the guard | `TestFactoryResetIsNeverBlocked` | red |

Two guards turned out to be **redundant pairs**, and a single-line break left each test green. That
result is not a hole, but it is the thing a one-line calibration would have got wrong: it would have
been recorded as "the test binds" when the test binds only to the pair.

| break | test | result |
|---|---|---|
| session expiry: drop `jwt.WithExpirationRequired()` only | `TestSessionTokensAreForgeryResistant` | green — rescued by the `exp == nil` check |
| session expiry: drop the `exp == nil` check only | `TestSessionTokensAreForgeryResistant` | green — rescued by the parse option |
| session expiry: drop **both** | `TestSessionTokensAreForgeryResistant` | red |
| enrolment: stop nulling `enrollment_token_hash` only | `TestEnrollmentCredentialsAreSingleUse` | green — rescued by `enrollment_expires_at = NULL` |
| enrolment: stop nulling `enrollment_expires_at` only | `TestEnrollmentCredentialsAreSingleUse` | green — rescued by the hash no longer matching |
| enrolment: stop nulling **both** | `TestEnrollmentCredentialsAreSingleUse` | red |

Both redundancies are now documented at the line that would be deleted (`auth/tokens.go` `Verify`,
`store/devices.go` `ConsumeEnrollment`), because nothing goes red when one half is removed and the
survivor silently becomes a single point of failure.

`no_factory_reset` is a third case of the same family and is called out in `policy/engine.go`: it is
never added to the base restriction set, so the strip that removes it currently removes something
that was never there. It is deliberate belt-and-braces against a future edit — the owner's escape
hatch (FR-2.3) — but it means deleting the strip changes no output at all, so the calibration had to
simulate the future mistake instead of removing the guard.

### 6.6 — the row that passed 94% of the time

Found by a sweep, not by a review: on 2026-08-18 the sixteenth full run of `tests/run_all.sh` turned
`TestSessionTokensAreForgeryResistant` red — `GET /api/v1/me: expected 401, got 200` — on a tree that
had not changed since the fifteenth run passed. A test that fails on unchanged code is either a real
race or a guard that was never binding; this one was the second.

The row was *"a real token with one signature character changed"*, and the helper behind it replaced
the last character of the token with a different character. That reads like a one-bit change and is
not one. An HMAC-SHA256 signature is 32 bytes; base64url spells 32 bytes in 43 characters, which is
258 bits of alphabet for 256 bits of signature, so the final character carries **two bits that decode
to nothing**. Four characters therefore spell the same signature, and the "tampered" token was, 6% of
the time, the genuine token wearing a different hat. Measured against the server's own issuer: **23 of
400** such mutations were accepted.

So the row had been green for fifteen sweeps while proving nothing about one in sixteen of the tokens
it was handed, and the day it finally spoke it looked exactly like a regression in the product.

Two root causes, and both were fixed rather than the flake being papered over:

1. **The helper's mutation was not guaranteed to be a mutation.** `tamper` now decodes the signature,
   flips a bit of the first byte and re-encodes canonically — a change to the signature itself, which
   no encoding property can undo.
2. **The product accepted four spellings of one credential.** `SessionIssuer.Verify` now parses with
   `jwt.WithStrictDecoding()`, which requires the trailing padding bits to be zero (RFC 4648 §3.5).
   This is not a forgery fix — an attacker still cannot produce a signature — it is what makes a
   session token *one string*, so that it can be compared, logged or revoked by value, and so that
   "one character was changed" is a true statement about it. The OIDC verifier deliberately does
   **not** do this, and says why at the line: the token there is Google's, this server keeps no state
   keyed on it, and refusing a token an issuer spelled unusually would lock every parent out of the
   console with no way back in.

The input that used to slip through is now its own row, in both suites, with the helper asserting
that its respelling decodes to *identical bytes* before handing it over — otherwise a red would prove
only that a different signature is refused, which the row above it already proves.

A third defect surfaced during the calibration: the rejection loop reported `expected 401, got 200`
without naming which of its eleven tokens had been accepted, and it stopped at the first. It now
`Errorf`s per row with the row's own name and reports every wrong row in one run. A table-driven
guard that hides which row failed is a guard that can only be debugged by bisecting it.

| break | suite | result |
|---|---|---|
| drop `jwt.WithStrictDecoding()` | e2e `TestSessionTokensAreForgeryResistant` | **red, naming** *"a real token whose signature is respelled with non-zero padding bits"* — and no other row |
| drop `jwt.WithStrictDecoding()` | unit `TestSessionRejects` | **red** — `accepted a second spelling of a valid signature (ends '8' -> '9')` |
| verify nothing (`ParseUnverified`), strict decoding left on | e2e `TestSessionTokensAreForgeryResistant` | **red on four rows**: wrong key, alg none, *one signature **byte** changed*, expired — the respelling row stays green because strict decoding is exactly what still refuses it |
| restore both | both | green; `tests/e2e/run.sh -run TestSessionTokensAreForgeryResistant -count=20` passed 20/20, where the old helper would have failed about one run in four |

The `-count=20` is the part worth keeping. The old row was not wrong every time — it was wrong 6% of
the time, which is under the noise floor of a suite anybody runs once. A guard whose subject is
randomly generated has to be run against many subjects before "it passed" means anything.

### 6.7 — FR-13.3, and the difference between a manifest and an install

FR-13.3 says the console must be installable to the home screen. Until now that sentence was proven
by `TestConsoleIsServedAndMobileReady`, which reads the manifest route: it is served, it parses, it
declares a `start_url`, a `display` and an icon. Every one of those assertions reads back bytes this
repository wrote. None of them is a statement about installing anything.

The two are not the same claim, and the gap is not theoretical — each of the three breaks below
leaves a manifest that passes the source-level checks unchanged and a console that Chrome refuses to
install. So the new `TestTheConsoleInstallsToAPhone` asks the engine that implements the rule, over
CDP: `Page.getAppManifest` for what the browser parsed, `Page.getInstallabilityErrors` for the
verdict, `Page.getManifestIcons` for the icon it would actually put on the home screen.

An empty installability-error list is the shape of two different facts — "this page installs" and
"this browser computes nothing" — so the test opens on `about:blank`, which has no manifest at all
and must therefore produce errors. If it does not, the run stops there and says the measurement is
not available, rather than reporting the requirement green on an instrument that was switched off.
That is the same fail-closed shape as `run_all.sh`'s exit 2.

`Page.getManifestIcons` is experimental and marked deprecated upstream, so it is called through
`tryCall`: a browser that does not implement it logs "NOT measured by this run" and the rest of the
test still stands. `Page.getInstallabilityErrors` is experimental too, and there the same failure is
fatal — it is the assertion, and a suite that skipped it would report FR-13.3 as green.

**The calibration found a defect in the new guard itself.** The manifest-error struct declared
`critical` as a `bool`; CDP sends `"critical": 0`. `encoding/json` fails that field, and the error
path is the only place it can ever be reached — with a valid manifest the list is empty and nothing
is decoded, so the test is green whether the field is right or wrong. It surfaced under break 3 as
`Page.getAppManifest: json: cannot unmarshal number into Go struct field .errors.critical of type
bool`, in place of the message the break existed to produce. A guard whose reporting path is only
exercised by a red is a guard nobody has run.

The type is now pinned twice, and the second pin is the cheap one: the report formats `critical` with
`%d`, so putting the `bool` back is a `go vet` failure — *"format %d has arg e.Critical of wrong type
bool"* — before any browser starts. Verified both ways: with `%d` the wrong type will not build, and
with `%v` restored alongside it the run reaches the browser and fails on the decode, which is how the
defect originally presented.

| break | result |
|---|---|
| the manifest declares no `icons` | **red**: `manifest-missing-suitable-icon minimum-icon-size-in-pixels=144`, `no-acceptable-icon`, and the icon assertion — *"would land on the home screen as a generated letter tile"* |
| `"display": "browser"` instead of `standalone` | **red**: `manifest-display-not-supported` |
| `start_url` moved off-origin | **red twice, in two layers**: the parse error *"property 'start_url' ignored, should be same origin as document"* (`critical=0`, the field that had been a `bool`) and the verdict `start-url-not-valid` |
| *harness*: the negative control navigates to the console instead of `about:blank` | **red**: *"this browser is not computing them at all; every assertion below would pass having evaluated nothing"* |
| all restored | green, together with `TestConsoleRendersOnAPhone` |

Chrome 151 accepts the single `image/svg+xml` icon at `sizes: any` — worth stating, because the
`minimum-icon-size-in-pixels=144` in the first break is what a raster icon set would have to satisfy,
and it would be easy to read that number as a rule the shipped manifest is evading.

FR-13.3 has a second half — *"no dependency on a desktop-only input (hover, right-click, keyboard
shortcuts)"* — and it was satisfied by nothing at all, in the strongest sense: the console contains no
`:hover` rule, no `contextmenu`, no `dblclick` and no `accesskey`, so the requirement held because
nobody had written the offending line. That is the state a guard exists to preserve, because a
hover-only affordance is invisible to every other check here: it renders, it lays out, it passes the
360 px suite, and it is simply unreachable with a thumb.

`TestConsoleNeedsNoDesktopOnlyInput` scans the three served assets — index, stylesheet, script —
through the router rather than the embedded filesystem, so an asset that stopped being served cannot
quietly stop being scanned. The list is the *desktop-only* inputs and not "everything a mouse can do":
a phone has a keyboard, so key handling is allowed; a phone has no pointer that can rest somewhere, so
hover is not. It carries a byte-count floor, because a router change that answered every path with an
empty 200 would otherwise read as the cleanest result available.

| break | result |
|---|---|
| `.btn:hover { opacity: .8 }` appended to `app.css` | **red**: */app.css declares a :hover rule, which a touch screen cannot produce* |
| `accesskey="s"` on the first button in `index.html` | **red**: */ declares an accesskey, which needs a hardware modifier* |
| a `mouseenter` listener appended to `app.js` | **red**: */app.js declares a mouse-only pointer event* |
| *harness*: the scan points at a path the router does not serve | **red**: *"/nope.css is answered with 404, so this scan is not reading the console at all"* |
| all restored | green |

### 6.8 — the readiness probe that was measuring a server about to be destroyed

Found while calibrating 6.7, in a run that had nothing to do with it: `psql "CREATE DATABASE
\"e2e_1242503_1\"": exit status 2`, once, on a tree that passed the run before and the run after. The
easy reading is "Docker was flaky". It was not.

`run.sh` waited for postgres with `docker exec … pg_isready -U postgres`, which without `-h` uses the
**unix socket** — and during the official image's entrypoint the socket is served by a *temporary*
server that initdb uses and then shuts down. Polled at 20 Hz on `postgres:17-alpine` — the image
this harness pinned before §6.11 aligned it with the deployment; the entrypoint that creates the
window is the official image's own, not that tag's, so the fix is not version-specific:

| probe | first ready | then |
|---|---|---|
| `pg_isready` (socket) | **6.02 s** | **not ready again at 6.87 s** — the temporary server going away |
| `pg_isready -h 127.0.0.1` (TCP, inside the container) | **7.32 s** | stays ready |

So there was a 0.85 s window in which the loop broke out, exported the connection details and started
the suite against a server that no longer existed by the time the first statement ran. The loop polls
every 0.5 s, so which side of the window a run lands on is a matter of scheduling — which is exactly
what an intermittent, infrastructure-shaped red looks like.

The fix is one flag. The temporary server is started with `listen_addresses` empty, so TCP is the one
thing the init phase cannot answer; the probe stays *inside* the container, so the original reason for
that choice — docker-proxy binds the mapped port before postgres can answer anything — is untouched.

Calibrated both halves: probing a port nothing listens on makes `run.sh` print
`NOT MEASURED: postgres did not become ready within 30s` and exit **2** rather than 0 or 1, and the
table above is the positive half — the probe demonstrably distinguishes the two servers rather than
agreeing with whatever is there.

This is the same defect as every other entry in this document, wearing infrastructure clothes: a
control that passed having evaluated something other than its subject.

### 6.9 — six leaks in a repository that has no secrets

The first push turned the workflows from a static claim into a measurement, and the measurement
disagreed with the claim on one job out of five:

| job | first real execution |
|---|---|
| backend (build, vet, vet -tags integration, test, gofmt) | success |
| android (assemble + unit tests) | success |
| e2e (black box against a real server and a real PostgreSQL) | success |
| image (distroless smoke test) | success |
| secret-scan (gitleaks over the full history) | **failure — `leaks found: 6`** |

The log said nothing else, and that is by design: the job runs `--redact`, so a leak is reported as
a count and a rule, never as a value. A red that cannot be read is still a red that has to be
resolved, so the scan was reproduced locally — the same gitleaks 8.30.1, fetched and checked against
the same SHA-256 the workflow pins, over the same 40 commits — into a JSON report that stayed in a
scratchpad. Reproducing it exactly is what makes the local answer a statement about the CI red:
40 commits scanned, 6 leaks, same rule, same files.

Nothing was printed. The report's `Secret` field was used only to compute a length and a hash, to
mask the value out of its own match line, and to ask `grep -rlF` whether it appears anywhere else —
a question answered by **file names**. Six findings, four distinct values:

| where | what it is |
|---|---|
| `DeviceStatusTest.kt:371` | a deliberately secret-shaped constant. The test asserts a token **cannot** reach the phone's status screen, so the fixture has to look like the thing it must not leak |
| `provisioning_test.go:21` (2) | a synthetic enrollment token and device id in `goodParams()`, feeding the QR-payload builder |
| `docs/API_REFERENCE.md` (2), `docs/PROVISIONING_GUIDE.md` (1) | one example value repeated in the original draft's API docs, in files deleted when the draft was replaced |

Three independent reasons none of them is a credential: this system has never been deployed, so no
server has ever minted an enrollment token; enrollment tokens are single-use and stored hashed; and
each value appears at HEAD only in the test file that introduced it — two of them nowhere at all —
including a search of the deployment repository, which knows none of them.

**Editing the files at HEAD could not have fixed it.** Three of the six were in files that no longer
existed, and gitleaks scans history: a value is a finding at the commit that introduced it, forever.
So the first resolution was an allowlist — a `.gitleaksignore` pinning `commit:file:rule:line`, five
entries, each exempting exactly one finding.

That allowlist was measured, both halves, and both halves passed. Each of its five lines was
load-bearing (removing one at a time returned 1, 2, 1, 1 and 1 findings — six in total, so nothing
in it was redundant and nothing suppressed a sixth thing that was never counted), and it did not
blind the files it named: in a throwaway clone, a freshly generated secret committed into the very
file an entry exempts was still found — including **at the same line number**, caught because the
commit differed. The entry pins a moment in history, not a location in a file.

**And it was still the wrong fix, for a reason no measurement of it could reach.** A fingerprint is
`commit:file:rule:line`. Rewriting history — which this repository then did, publishing as a fresh
repository with a single initial commit — changes every commit SHA, so every entry becomes void at
once, and the allowlist decays into five lines that exempt nothing while looking exactly like five
lines that do. Re-pinning them is circular: the amend that re-pins is itself a new commit.

The root-cause fix was to stop the fixtures being secret-shaped. `DeviceStatusTest.kt` needed a
token of the right *length*, never a token's entropy, so it builds one (`"fgt_" + "abcdefgh…"
.take(20)`); `provisioning_test.go` needed two 43-character base64-ish strings, so it computes them
with `strings.Repeat`. Both assertions are unchanged and still pass — `Params.validate()` only checks
those two fields for emptiness — and the scanner now returns zero findings over the whole tree with
**no allowlist at all**. That is the state this repository ships in: no `.gitleaksignore`, no `paths`
exemption, nothing to go stale.

It has since held up on a third fixture. `CipherPreferencesTest` (§8) stored a JWT-shaped base64
blob as its `SECRET`, and gitleaks flagged it as a `generic-api-key` at entropy 4.77 — the one
finding over the whole tree. Every assertion that uses that constant is an exact substring check, so
its entropy was buying the tests nothing while making it indistinguishable from a real token to a
scanner. Lowering it to a readable `"not-a-real-token-…"` returned the scan to zero findings with
the allowlist still empty, and the reason is written at the constant so the next person does not
"improve" it back.

The general form is worth more than the instance: **an allowlist is a claim pinned to a state, and a
control pinned to state is a control with an expiry date nobody wrote down.** A `paths =
["*_test.go"]` would have been one line instead of five, and would have blinded the scanner to every
credential ever pasted into a Go test — the same shape as a coverage waiver citing a suite that
cannot run. The narrow allowlist avoided that failure and found a different one.

The **does-not-blind** measurement took three attempts, and the two that failed are the record worth
keeping — both produced a clean number from a probe that had measured nothing:

- The plant was `ApiKey: "AKIA…"`, which `generic-api-key` does not match in that spelling. It came
  back **0 findings** — which reads exactly like "the allowlist swallowed it". The conclusion would
  have been that the allowlist is far broader than it is, and the fix for that non-problem would have
  been to widen the test until it agreed. The shape used in the end (`api_key = "…"`) was one already
  observed to be caught, so a miss could only mean the allowlist.
- Before that, `git commit -a -- <path>` failed — `-a` with a path is not a command git will run —
  so two consecutive runs scanned a clone in which nothing had been committed and reported **0
  findings each**, a clean result from a probe that had planted nothing. The harness now prints the
  commit it made and counts the added lines its own diff contains before it believes any number.

The scan is now the sweep's first layer rather than a thing that was done once by hand:
`tests/run_all.sh secret-scan` runs the same gitleaks 8.30.1 over the same full history, with
`--redact`, so a local run and the CI job answer the same question. It reports **2, not measured**
when gitleaks is absent from `PATH`, and again when it is asked to scan something that is not a git
checkout — there a directory scan reads ~0 bytes and exits 0, which is the shape of a clean result
produced by a scan that examined nothing.

### 6.10 — a test that asserted a property it did not own

The commit that turned `secret-scan` green turned `android` red. One test failed in CI:

```
SynchronizerTest > an unreachable server leaves a recovered device released FAILED
    java.lang.AssertionError at SynchronizerTest.kt:519
```

Line 519 is `assertEquals(RECOVERED_AT, recoveryStore.activeSince())`. It reproduced locally once, in
a different method of the same class — `a recovered device applies the released state instead of its
cached policy`, `ClassCastException: SyncResult$Applied cannot be cast to SyncResult$Released` — and
then not again in 3 class runs, 15 instrumented class runs and 8 instrumented full-suite runs. So:
intermittent, and not a date bomb.

**What the failure necessarily means.** Both tests set a recovery, arrange for the server to be
unreachable, and sync. `recovery.clear()` has exactly one caller in main —
`Synchronizer.applyFrom`, under `if (source == PolicySource.SERVER)` — and `PolicySource.SERVER` is
reachable only when `api.policy()` returned without throwing. `RecoveryMode.active()` is
`store.activeSince() != null`, with no expiry and no clock in it. The client's `Json` is not
lenient, so an empty or malformed body raises `SerializationException`, which is not what either
failure was. Therefore, in both runs, **something served a well-formed `PolicyResponse` on a port
the test had just closed.**

**What was ruled out, with numbers.** Every hypothesis below was measured, not reasoned about:

| Hypothesis | Probe | Result |
|---|---|---|
| A closed ephemeral port can accept again | bind → close → connect, 20 000 iterations | 20 000 refused, 0 connected |
| TCP self-connect on loopback (source port = destination port) | 200 000 connects to one fixed closed port | 200 000 refused, 0 connected, 0 self |
| Another socket takes the port in the close→connect window | 100 000 iterations with 4 threads churning ephemeral binds | 0 stolen, 99 994 refused, 6 other |
| Test methods overlap, so two servers are alive at once | JUnit 4.13.2, no `maxParallelForks`, no `forkEvery`, no JUnit-Platform parallelism | one JVM, methods sequential |
| A clock or a date makes it fire | every clock in the path is injected; `active()` is a null check | no real clock is read |
| Leaked JVM-wide state (proxy, `setDefault`, a shared responder) | grep of the whole test tree for `setProperty`/`setDefault`/statics | none |

**The mechanism was not identified, and this record does not claim otherwise.** What was identified
is the defect in the test, which is a different thing and the one that matters:

```kotlin
server.close()                    // "the server is unreachable"
val result = synchronizer().sync() as SyncResult.Released
```

`close()` expresses *unreachable* as a claim about a port the fixture **has just given up**. From
that line onward the test owns nothing: the port is the operating system's to reallocate, and the
test's verdict depends on nothing else in the JVM binding it first. That the window is small makes
the test usually right, which is worse than being wrong — a control that is usually right is one
whose red is read as noise. Nine sites across two classes were written that way.

**The fix is to hold the port.** `LoopbackServer.stopAnswering()` keeps the `ServerSocket` bound and
answers nothing: each connection is accepted, its request read and recorded, then dropped without a
response. The client gets `SocketException: Unexpected end of file from server` — an `IOException`,
the same branch a refused connection lands in, measured with a standalone probe (3/3 attempts, 2–14
ms) rather than assumed. Nothing can answer a port the fixture never releases, so the failure mode
becomes impossible instead of unlikely, whatever produced it.

Nine sites converted: eight in `SynchronizerTest`, one in `EnrollerTest`. The three remaining
`server.close()` calls are `@After` teardown and keep their meaning.

**6.10 calibration — the fixture's own tests.** `stopAnswering` is now load-bearing across nine
tests, so `LoopbackServerTest` holds it to three properties and each was calibrated against a
known-bad implementation:

| Break | still owns its port | fails with IOException | records what it was asked |
|---|---|---|---|
| `stopAnswering()` implemented as the old `close()` | **RED** | pass | **RED** |
| the silent flag ignored, so it answers anyway | pass | **RED** | pass |
| restored | pass | pass | pass |

The first row is the whole point. **The obvious assertion — that the request fails with an
`IOException` — passes on the broken implementation**, because HTTP cannot distinguish "refused"
from "accepted and dropped". A fixture test that checked only that would have certified the defect.

**6.10 calibration — the converted tests still bind.** Converting nine tests is exactly the change
that could disarm them, so the two recovery tests were re-checked against product breaks:

| Break in `Synchronizer` | red tests |
|---|---|
| the cache path reports `PolicySource.SERVER` (the CI failure's own signature) | `a fetch that fails falls back to the cached policy`, `a server error falls back to the cache rather than refusing`, and **both** recovery tests |
| `if (recovery.active()) return release()` removed | `the alarm path releases a recovered device too` and **both** recovery tests |
| restored | none — 428 tests, 0 failures |

**One measured surprise, left unpinned.** A silent server sees the GET **twice**: `HttpURLConnection`
retries an idempotent request once when the peer closes before the status line. The fixture test
asserts the requests it recorded are all the request under test, not that there is exactly one — the
retry count is the JDK's business, and pinning it would make the suite go red on a JDK that stopped
retrying without anything being wrong.

**What this does not establish.** Repeated green runs do not prove the race is gone; absence of a
rare failure is not evidence over any number of runs I have patience for. The claim is narrower and
checkable: the test no longer depends on a property it cannot own.

### 6.11 — the suite was proving it about a database nobody would run

Found while reconciling the documentation against the manifests, which is a different instrument
from running the tests and found something running them could not.

`deploy/postgres.yaml` pins **`postgres:18.6`**. `tests/e2e/run.sh` and `tests/image/smoke.sh` both
defaulted to **`postgres:17-alpine`**, and CI pre-pulled the same. So the layer whose whole claim is
*a real server binary against a real PostgreSQL* was making that claim about a different major
version — and a different base distribution — than the one the deployment would start. Every green
was honest about what it ran and silent about what it did not cover: the embedded migrations, the
advisory lock they take, and every `pgx` call had been exercised on 17 and on nothing else.

Nothing was red, and nothing would have gone red. A major-version incompatibility surfaces on the
first startup of the real deployment, which is the moment with the least diagnostic support and the
most people waiting — and the e2e suite would still have been green at that moment, which is the
part that makes this a test-integrity defect rather than a version-pinning preference.

The fix is one line in each of three files: the harnesses now default to the deployed image, with
`E2E_PG_IMAGE` still overriding. Re-measured on `postgres:18.6`: e2e **24 tests, PASS** in 62.0 s,
image **12 assertions, PASS**. Both had been green on 17-alpine too, which is the whole point — the
change bought no new red, it bought a green that is about the database the deployment starts.

Generalised: **a fixture's version pin is a scope declaration, and an unstated scope is read as
"everything".** Anywhere a test stands up a dependency, the version it stands up is part of what the
green means, and it belongs next to the version the deployment pins — or it drifts, silently, in the
direction of whatever was convenient when the harness was written.

### 6.12 — fifty links between eight documents, and nothing checking one of them

The same reconciliation pass that found 6.11 kept turning up references — README to CONCEPT, CONCEPT
to REQUIREMENTS, the plan to its own sections — and none of them was checked by anything. Fifty
internal links across eight Markdown documents, twelve of them naming a section, all hand-written.

A broken one fails the way GitHub fails it: **silently**. A dead path is a 404; a dead anchor is
worse, because GitHub simply lands the reader at the top of the page, and a reader who followed
"see §6.9" and arrived at the title concludes the document is stale rather than that the link is.
This is the same defect the citation scanner exists for — a reference that looks like evidence and
resolves to nothing — pointed at the other kind of reference.

`DocumentationLinksTest` now holds every link to a file that exists, and every anchored link to a
heading that exists. It runs in the `android-unit` layer beside `RequirementCitationsTest`, for the
same reason: the references do not respect language or directory boundaries, so a guard that did
would be blind to most of them.

**The first draft of it was a false-red generator, and that is the part worth recording.** GitHub's
anchor rule lowercases a heading, drops everything that is not a letter, digit, space, `_` or `-`,
and maps **each remaining space to its own hyphen** — it does not collapse runs. So `### 6.6 — the
row` is `#66--the-row`, the double hyphen being the space either side of the em dash. A slug function
that collapses whitespace — the obvious one, and the one written first — reported **6 of the 12
anchored links dead**, every one of them a correct link to a real heading. A checker that cries wolf
is worse than no checker: its findings get edited away, and the edits break links that worked. The
rule is now pinned by an assertion on that exact string, so "tidying" the slug goes red immediately.

Two smaller traps, both encoded:

- **Fenced code blocks are stripped before the heading scan.** README's build section contains the
  shell comment `# control plane` inside a fence. Read as a heading it invents an anchor that
  resolves, which would hide a genuinely broken link rather than report it.
- **Gradle cannot see the inputs.** The test task's inputs are compiled classes, so editing only
  Markdown leaves it `UP-TO-DATE` and Gradle skips it — the guard stops running exactly when you
  change what it watches. `tests/run_all.sh` already invoked the task with `--rerun` for the
  class-comparison check (see 6.5); this is the second guard that depends on it, and both README and
  CONTRIBUTING now say why the flag is there rather than leaving it as a habit.

**Calibration — 5 breaks, 5 red.** Each break was applied to the tree, run, and reverted.

| Break | Red test |
|---|---|
| a link to `NOPE_MISSING.md` added to CONCEPT.md | `every link points at a file that exists` |
| a link to `README.md#no-such-heading-anywhere` | `every link to a section points at a heading that exists` |
| `## Deployment` renamed in README.md | **both** the section check and the scanner's own positive control |
| the link pattern replaced with one that matches nothing | `the scan read the documents, and the anchor rule is GitHub's` |
| restored | none — 3 tests, 0 failures |

The third row is why the positive control is pinned to a heading README links to *itself*. An
arbitrary heading would make the pin the only thing standing between a rename and a green suite, and
it would go red on a rename that broke nothing. Pinned to a linked heading, the pin and the real
check fail together, for the real reason.

### 6.13 — a device owner that only a factory reset can remove

A full sweep once reported `android-instrumented` **NOT MEASURED**, correctly,
and gave the reason as `dpm`'s: *"Not allowed to set the device owner because there are already some
accounts on the device"*, on an emulator where `dumpsys account` reports `Accounts: 0`. The truthful
part was underneath, truncated: the AVD still carried a **previously installed build, under a package name
this project no longer uses**, as its device owner.

A device owner can only be replaced by a factory reset. So the script's five retries, five seconds
apart, were spent on a condition that could not change, and the message they produced pointed at an
account fiction that had already cost an hour once (6.5's own notes say so).

`tests/android/instrumented.sh` now reads the current owner component before the loop and refuses
immediately when it is somebody else, naming the component and the way out (`-wipe-data`, or a
factory reset on a phone). Still `NOT MEASURED`, never `FAIL` — nothing about the product was
exercised, and a `FAIL` here would say the product is broken when it was never run.

Calibrated on the real known-bad input, which was sitting right there: against the un-wiped AVD the
new branch fires in seconds, naming the foreign owner in the reason — `ComponentInfo{<the other
package>/…}` — and exits `rc=2`.
After `emulator -avd familyguard34 -wipe-data`, `dumpsys` reports `Device Owner Type: -1` and the
layer provisions normally.

Generalised, and it is the counterpart to 6.11: **a rename is not finished when the code compiles.**
The old identity survives wherever something else recorded it — a device-owner record, a signing
certificate, a runner's image cache — and every one of those is outside the tree the compiler reads.

### 6.14 — the calibration probe the scanner is allowed to ignore

Re-calibrating the working-tree secret scan looked like a formality and produced the most instructive
red-that-wasn't in this document. The probe was `AKIAIOSFODNN7EXAMPLE`, planted in the tree; gitleaks
reported **no leaks found, rc=0**, on a file it had demonstrably read — the byte count went up by
exactly the probe's length.

That string is the canonical AWS documentation key, and **gitleaks allowlists it by design**, along
with the other well-known example credentials. So the probe was a value the scanner is *supposed* to
ignore, and the calibration measured that fact rather than the scanner's ability to find anything.

The three readings available at that moment were "the scanner is broken", "the tree is clean" and
"the probe is wrong", and only the last one is true. Two of them are comfortable and one of them —
*the tree is clean* — would have ended the calibration with a green that meant nothing, which is the
failure this whole section exists to prevent. The instrument was settled by running gitleaks on a
two-line file outside the repository: the documentation key → 0 findings, a key of the same shape
with different characters → **2 findings, rc=1**. Only then did the in-tree probe mean anything:
planted → 1 finding, rc=1; removed → 0 findings, rc=0.

Generalised: **a calibration probe copied from documentation is a probe the tool may be built to
ignore.** Example credentials, `example.com`, `555` phone numbers, `test@test.com` — the values that
come to hand when you need something realistic-looking are exactly the values scanners, linters and
validators carry exemptions for. Draw the probe from the shape of the thing, not from a document
about it. And when a probe stays green, the first hypothesis is the probe.

### 6.15 — the versions the documents state, and the build file that defines them

Found by reconciling the docs against the toolchain rather than against each other, the same way
[6.7](#67--fr-133-and-the-difference-between-a-manifest-and-an-install) was found. **CONCEPT.md said
34 and 35 for `targetSdk` and `compileSdk`.** The build had been on 37 for as long as there was a 37,
and nothing anywhere would ever have said so — a stale version in prose fails silently by
construction, because prose is not compiled. It is not a cosmetic defect either: the README's
`Prerequisites:` line is what a new contributor installs from, and a wrong number there costs an
afternoon before it costs a build. The bytecode target had drifted the other way in the same session:
three documents said 17 while the build had moved to 21, which is the same defect pointed at a number
that *does* reach a phone.

`DocumentedVersionsTest` is the third guard of the family that already holds
[`RequirementCitationsTest`](#510--the-requirements-document-wired-to-the-code-that-claims-it) and
[`DocumentationLinksTest`](#612--fifty-links-between-eight-documents-and-nothing-checking-one-of-them),
and it asks the question neither of them does. Those two ask whether a reference *resolves*; this one
asks whether a stated fact is still *true*. In all three the repository's own files are the authority
and the document is what has to move.

**Two mechanisms, because prose and code do not read the same.**

- **Identifier-anchored** — wherever a document writes a build-file identifier next to a value
  (`` `minSdk 29` ``, `` `jvmTarget` 21 ``, `targetSdk = 37`), the value must be the one the build
  file holds. This cannot produce a false red: those identifiers appear in exactly one context. It is
  also why several sentences were *edited to name the identifier* — "the bytecode target is 17" is
  unguardable, "`jvmTarget` 21" is guarded by construction, and the edit is the cheap half.
- **A registry of prose claims** — the sentences that state a version without naming an identifier:
  the README's `Prerequisites:` line, `platform 37.1 and build-tools 37.0.0`, the Gradle/AGP/Kotlin
  pins. Each is a regex pinned to the phrasing it guards, and **each must match at least once.** That
  requirement is the whole value of the mechanism: a regex that quietly stops matching is a guard
  that has been switched off, which is the failure this repository keeps finding. So rewording one of
  those sentences turns the suite red on purpose — that is precisely the moment to re-check the
  number, because it is the moment someone is already editing the line.

What it deliberately does **not** do is hunt prose for version-shaped numbers in general. The README
says AGP "documents JDK 17 as its minimum", which is a floor and is true; a scanner that saw `JDK 17`
beside a pinned `JAVA_VERSION` of 26 would report it, and a checker that cries wolf gets its findings
edited away rather than its bug fixed — [6.12](#612--fifty-links-between-eight-documents-and-nothing-checking-one-of-them)
is the same lesson learned the expensive way. The blind spot is stated in the test's own header
rather than papered over.

**Calibration — 6 breaks, 6 red.** Each was applied, run, and reverted.

| Break | Red test |
|---|---|
| CONCEPT.md's SDK line put back to the values it carried before the fix — the original defect | `every version a document states next to its identifier is the one the build uses` |
| README's `Prerequisites:` changed to `JDK 25` | `every registered prose claim still matches, and still states the right version` |
| *"The Gradle wrapper pins 9.7.0"* reworded to *"is pinned to 9.7.0"*, number untouched | the same test, on the **dead-claim** half — the reword, not the number |
| `minSdk = 29` → `28` **in the build file**, docs untouched | the identifier test, naming all five doc sites at once |
| the identifier pattern's value group replaced with one that matches nothing | `the scan read the build files and the documents` — 0 claims found |
| the `jvmTarget` authority regex pointed at a symbol the build file does not contain | **all three** — a guard that cannot read its authority must not report agreement |
| restored | none — 3 tests, 0 failures |

**Its first real finding was in this section.** The full sweep that followed went red on six claims,
every one of them in the prose describing the guard — here and in the README — because a document
that records what a wrong version *looked like* has to quote one, and quoting one next to its
identifier is indistinguishable from asserting it. The resolution is the one
[6.9](#69--six-leaks-in-a-repository-that-has-no-secrets) reached for the secret scanner and for the
same reason: **change the shape of the value, never add an exemption.** These sentences now name the
numbers away from the identifier — *34 and 35 for `targetSdk` and `compileSdk`* rather than the
identifier-then-number form the guard reads as a claim — which is both truthful about the past and
unambiguous about the present. The sentence you are reading went red once more before it settled,
because its first draft quoted that form as an example, which is the joke and also the evidence: the
guard cannot tell a quotation from an assertion, and it should not try. An ignore-marker would have
worked once and then become a place a genuinely stale version could sit forever. The guard still has zero exemptions, exactly as the secret scan does.

Two notes on the calibration itself, both of which cost a pass.

**The first attempt reported GREEN for two breaks `sed` had never applied.** The edits' regexes did
not match, the run therefore measured the unbroken tree, and *green* is exactly what an unapplied
break produces — indistinguishable from a guard that does not bind. The harness now refuses to
report a result unless the break is verified present in the file first, and deletes the JUnit XML
before each run so that a task which fails to execute reports **NOT MEASURED** rather than replaying
the previous break's results. A calibration harness is a control, and it needs its own control.

**The first form of the fourth break was rejected rather than counted.** Moving the build-tools pin
to 37.0.1 moved the authority, but that version is not installed, so AGP failed at configuration and
no test ran at all — NOT MEASURED, correctly, and it would have read as a pass
in a harness that only checked for absent failures. `minSdk = 29 → 28` moves the authority without
touching anything the build has to resolve, and it is the break that actually demonstrates the
direction of the check: the build file moved, every document stayed still, and the guard named all
five of them.

## Phase 7 — Deployment preparation (no deploy)

| # | Task |
|---|---|
| 7.1 | Multi-stage Dockerfile, non-root, read-only root filesystem, no shell in the final image |
| 7.2 | GitHub Actions job publishing `ghcr.io/helios57/familyguard-control-plane:<semver>` on tag |
| 7.3 | `deploy/` — kustomization, namespace, postgres, control-plane, ingress on `guard.example.com`, and the Secret *contract* the deployment has to satisfy |
| 7.4 | Render it, calibrated: a deliberately broken manifest must fail the render before a clean one is believed |
| 7.5 | Deployment runbook: DNS record, secret creation, first-parent bootstrap, APK publication, device enrollment |

**Not deployed *from this repository*.** The manifests here are a worked example and stay one;
the deployment that exists runs from a private infrastructure repository, which is where the
real host, the real secret store and the cluster's own conventions live. See Phase 8.

Status: **7.1 through 7.5 done. The image layer is calibrated and registered.**

- **7.1** — `backend/Dockerfile` is a two-stage build ending in `gcr.io/distroless/static-debian13`
  pinned by digest: no shell, no package manager, `USER 65532`, and the deployment mounts the root
  filesystem read-only with an `emptyDir` for `/tmp`. `tests/image/smoke.sh` asserts **twelve**
  properties of the built image, and all twelve are **calibrated** — each was broken in the
  Dockerfile or the Go source, observed red, and restored — so `image` is now a registered layer in
  `tests/run_all.sh` and CI invokes it through `tests/run_all.sh image`.

  The calibration is worth recording, because two of the twelve assertions were **defects in the
  test rather than in the image**, and both were the kind that reports success having measured
  nothing:

  - the uid check read `docker top`, which renders uid 0 through the *host's* `ps` and resolves it
    to the name `root`. With `USER 0:0` in the Dockerfile the suite printed
    `ok  the running process is uid root, not root` — a container running as root, passing the
    assertion that exists to forbid it. It now reads `/proc/<hostpid>/status`, which is numeric.
  - a server that crashes under `--read-only` exits before docker reports a mapped port, so
    `docker port` printed nothing and the script exited **2 — "could not read the mapped port"**
    before reaching the very assertion meant to catch that crash. A NOT MEASURED in the place a
    FAIL belongs is a false green wearing an honest label. The port read moved inside the liveness
    loop, and `notmeasured` is now reserved for "the container is alive and docker still will not
    tell us the port".

  The digest pin was made real in the same pass. The Dockerfile had said "pinned by digest" in prose
  while ending in `:nonroot`, so the twelfth assertion — *the final stage is pinned by digest, not a
  moving tag* — was added and the tag replaced with the index digest, after checking with
  `docker manifest inspect` that it is a multi-arch **index**
  (`application/vnd.oci.image.index.v1+json`, five children) and not a per-platform manifest, which
  would have built on one architecture only. `smoke.sh` now reads both `FROM` lines out of the
  Dockerfile instead of repeating the image names, so the pre-pull can never cover a different image
  than the build.
- **7.2** — `.github/workflows/release.yml`, on a `v*.*.*` tag. Three refusals before anything is
  published, because a push to a registry is not something you can take back: the tag must name a
  plain semver,
  it must be an ancestor of `origin/main`, and its version must equal the DPC's `versionName` — a
  server published as 0.2.0 against an APK that still says 0.1.0 gives a parent two numbers for one
  system. It then runs the whole gate again via `uses: ./.github/workflows/ci.yml` rather than
  restating the jobs, because a release that re-ran a *paraphrase* of the gate would measure
  something other than what the gate measures, and the difference would only be discovered by a bad
  release. `latest` is deliberately not published. The publish step prints the **digest** and exits
  **2** if the build reported none — an empty digest is "not measured", and a release nobody can
  verify is worse than one that failed. Signed provenance is attempted and, on this repository,
  cannot be produced: see the release entry in the open items above. Every image still carries
  BuildKit's in-registry `provenance` and `sbom` attestation manifests, which travel with it and
  depend on no GitHub feature.
- **7.3/7.4** — [`deploy/`](deploy/): a `kustomization.yaml` over a namespace, a postgres, the
  control plane and an ingress, plus `secret.example.yaml`, which is the one file the kustomization
  deliberately does **not** include. A Secret full of placeholders that applies cleanly is worse than
  one that is absent: the first deploys credentials nobody chose, the second fails.

  The render is calibrated rather than trusted. `kubectl kustomize deploy` on the tree as it stands
  emits a Namespace, two Services, two Deployments and an Ingress, rc=0; with one manifest
  deliberately corrupted it exits **1** with a `MalformedYAMLError` naming the file. That second half
  is the one that matters, because `kustomize build` is not a YAML validator by default and a
  renderer that accepts everything reports a clean render of a broken tree.

  Writing the runbook is what found the one real defect in this phase. `control-plane.yaml` set
  `APK_PATH` and not `APK_CERT_PATH`. The server treats the certificate as optional and would have
  started cleanly, so nothing would have been red — but with only the APK path the QR carries only
  `PROVISIONING_DEVICE_ADMIN_PACKAGE_CHECKSUM`, the hash of the APK *file*, which Android deprecated
  in favour of the signature checksum. The signature checksum is what Android 7+ verifies. The
  failure would have surfaced at the one moment it is least recoverable: a parent standing over a
  factory-reset phone, scanning a QR that provisioning refuses. Fixed by adding the variable and the
  DER file it points at; the runbook now treats both files as prerequisites of the first apply.

  What `deploy/` deliberately does not do is describe one site. Five values are named in
  [`deploy/README.md`](deploy/README.md) as the ones a reader must change, and the Secret is a
  *contract* — five key names — rather than a mechanism, because External Secrets, Sealed Secrets,
  SOPS and a secret-manager operator all satisfy it and picking one for the reader would be picking
  wrong for most of them.
- **7.5** — [`DEPLOYMENT.md`](DEPLOYMENT.md). Seven numbered steps in the order they have to happen,
  six of which no sync can do for itself: DNS for `guard.example.com`; pre-`chown`
  `/srv/familyguard/postgres` to `50140:50140` and `chmod 700` (there is deliberately no
  fix-permissions init container — it would have to run as root, a larger hole than the problem it
  closes); create `/srv/familyguard/apk` and put **both** the signed DPC and its DER signing
  certificate there *before* the first apply, because the server `stat`s both at startup and a QR
  minted against a missing file is a checksum for nothing; populate `familyguard-secret`; create the
  Google OAuth client; and publish the image. It also carries the APK signing procedure — there is no
  Gradle `signingConfig` on purpose, so the keystore is never a build input — the verification steps
  that read state back from the authority rather than from a green ArgoCD tile, and the rollback
  table whose last row is the factory reset that always works.

---

### 7.6 — the first run at the floor, and the two things it found

`android-instrumented` had never executed. It was run on an **API 29** emulator — the floor NFR-13
names, and the floor because a Galaxy S20 shipped Android 10 — rather than on the API 34 AVD that
already existed, on the grounds that a layer run above its floor measures the wrong device. It
failed, twice over, and both failures were real.

**A key cache that outlived the key.** `KeystoreSecretCipher` caches the loaded `SecretKey`
process-wide, and the cache is what makes generation happen at most once. It also meant the handle
could outlive the keystore entry it names, after which the platform answered every `Cipher.init`
with `InvalidKeyException: Keystore operation failed` / `KeyStoreException: Key not found` for the
rest of the process's life. `loadOrGenerate` already documented the recovery — *"the difference
between a device that re-enrolls and a device that throws on every read forever"* — but it sat
**behind** the cache, so after the first successful load it could never run again. A guard that
exists and cannot be called, in exactly the situation it was written for.

Four of the seven `StoreEncryptionTest` cases failed and three passed, which is the shape of the
bug rather than a coincidence: the `@After` deletes the test alias, so the tests that happened to
run first passed and everything after them hit the dangling handle. The host suite could not have
seen this at all — it drives the layer above with a fake cipher that has no keystore in it.

Fixed by routing both operations through a `withKey` wrapper that drops the cached entry on
`InvalidKeyException`, reports it, and retries exactly **once**. One retry, not a loop: a second
failure means the key is not merely absent, and the exception is then the honest answer. On `open`
the retry ends in `AEADBadTagException` — a different exception, so it cannot recurse — because the
blob was sealed under a key that no longer exists, and `CipherPreferences` turns that into "absent,
and reported", which is the recovery the storage layer already documents. A named test,
`aKeyDeletedUnderneathTheCipherIsReplacedRatherThanWedgingIt`, pins it; it was written first and
observed red with `Key not found` before the fix.

**A test whose precondition the platform does not allow.** `WipeabilityTest.aNonBootBroadcastIsIgnored`
cleared the baseline and asserted that no baseline restriction remained, so that "still absent after
a non-boot broadcast" would mean something. On API 29 that is unsatisfiable: provisioning a device
owner leaves `no_add_user` as a **base** restriction — `dumpsys user` lists it under `Restrictions:`,
not under `Device policy local restrictions:` — and no `clearUserRestriction` removes it.
`UserManager.getUserRestrictions()` returns the *effective* union of both, so a device owner cannot
even distinguish its own restrictions from the platform's through that API.

The first change was to the *message*: it said "clearing the baseline did not take" without naming
what survived, which is the same defect the sibling assertion twelve lines above had already been
fixed for. Naming it produced `[no_add_user]` and the diagnosis in one run instead of two.

The test now asserts the **delta** — whatever genuinely cleared must still be clear after the
broadcast — and keeps the self-check in the form the platform permits: at least one restriction must
have cleared, or a re-apply would be undetectable and a green would mean nothing. **Calibrated**:
with the `intent.action != ACTION_BOOT_COMPLETED` guard removed from `BootReceiver`, it fails and
names all five restrictions that were wrongly re-applied, so the rewrite did not make it vacuous.

The general form, and the reason this is filed as a finding rather than a fix: **a test that asserts
over an effective set is asserting about everything that can write to that set, not only about the
code under test.** Both defects were invisible to a fully green host suite, and one of them was
invisible to a fully green run one API level up.

---

## Phase 8 — Release, and the first deployment

`v0.1.0`, 2026-08-18. Everything below was read back from an authority rather than from the output
of the thing being checked.

**The image.** The `v0.1.0` tag ran `release.yml`: the `guard` job (semver, on `main`, DPC
`versionName` equal to the tag), then the whole of `ci.yml` as a called workflow, then `publish`.
`ghcr.io/helios57/familyguard-control-plane:0.1.0` is
`sha256:e8abceff609c5ea6adf4dc529c8e69453b330e7b26c2677cf304547fd02f8b67` — the registry's
`docker-content-digest` header and the build's own push output agree, which is what makes it
evidence rather than a transcription. An unauthenticated GET of the manifest returns 200, so the
package is public and no pull secret is involved. The provenance attestation was created; the
`repos/{repo} --jq .visibility` gate in the workflow exists because GitHub refuses one for a
user-owned *private* repository, and a refusal there would otherwise fail a release for a reason
that has nothing to do with the release.

**The DPC.** Signed on a workstation, never in CI — see DEPLOYMENT.md for why and how. Signing
certificate SHA-256 `b62cda948ad3a08ecb2af47d1617173db9bdaf3b31bb63b036ff91addb8a8e10`, which is
both what `apksigner verify` reports for the APK and what `sha256sum` reports for the exported
DER. Those two numbers coming from different tools reading different files is the point: nothing
downstream compares them, and if they disagreed the provisioning QR would carry a checksum for a
certificate the phone never receives.

**The deployment** is a commit in a private infrastructure repository, not a `kubectl apply`. The
running pod's `imageID` was checked against the digest above — not against the tag it says it was
pulled by — and the served `/dpc.apk` was downloaded and compared byte-for-byte against the signed
artifact.

**What this does not establish**, recorded here rather than discovered later:

- **No phone has enrolled against a deployed instance.** The provisioning path is covered by the
  e2e and instrumented suites, which is not the same claim.
- **Parent sign-in is unverified.** The Google OAuth client does not exist yet, and creating one
  needs a browser session that the deploying machine has no path to. The chain up to Google is
  measured and correct — `/api/v1/auth/google/start` redirects to `accounts.google.com` with the
  right `redirect_uri`, `scope=openid email profile`, PKCE `S256`, a nonce and a state — so the
  only missing thing is the credential. Until it exists Google answers `invalid_client`.

**What the first minutes of uptime found.** Eight consecutive 404s from one address, each logged as
`"path":""`. `c.FullPath()` returns the matched route *template*, which is the right thing to log —
it keeps device ids out of the log and the cardinality bounded — but for a request that matched
nothing it is the empty string, so every 404 line reported nothing at all. That is this
repository's own recurring defect in miniature: a control that runs and evaluates nothing. Fixed in
`loggedPath`, which falls back to the request path with the query string dropped (that is where
codes and tokens travel), truncated, and with control characters replaced. Five tests, calibrated:
four go red against `c.FullPath()`, and the fifth — the one asserting that a *matched* route still
logs `/api/v1/devices/:id` rather than the id — stays green, which is what makes it a negative
control rather than a fifth copy of the same assertion.

### 8.1 — the backup that had to be restored to be a backup

The control plane went live with no backup of its database, which is the same posture as every
other application on the cluster it runs on. For this one that is the wrong posture, and the reason
is specific rather than general: every row in `devices` is a phone that was factory-reset and
provisioned by hand, and the enrollment cannot be reconstructed from anything else the server
holds. Losing the database is not "restore yesterday and lose a day" — it is walking to each phone,
resetting it again, and scanning a new QR.

So `deploy/backup.yaml`: a nightly `pg_dump --format=custom`, and then, in the same job, four
checks that have to pass before the file is allowed to be called a backup.

| check | the failure it exists for |
|---|---|
| size > 1 KiB | `pg_dump` can exit 0 having written nothing; an empty file is the shape a permissions failure takes |
| `pg_restore --list` | a dump interrupted mid-write is a valid file that ends early, and reads as good until a restore reaches the missing part |
| restore into a scratch database, `--exit-on-error` | pg_restore's **default** is to report errors and still exit 0 — without the flag this step is a control that evaluates nothing |
| row counts, table by table | a restore into an empty database proves the archive parses, not that it carries the rows |

Only then is the `.tmp` renamed. **A dump that failed verification never becomes "the backup."**

**The first run passed, and the pass was worth nothing.** The log read
`families=1 parents=1 children=0 devices=0 policies=0` on both sides. Four of the five numbers were
zero, and `0 == 0` agrees whether or not the restore carried anything — the same claim as zero
errors in a window with zero traffic. Two changes followed. The comparison now covers **every**
table in `public`, discovered at run time rather than listed here (a hand-written list stops
covering the table added next month, and goes on passing), and the table *names* are part of the
compared string, so a restore that silently dropped a table differs even when every surviving table
matches. And when the database holds zero rows the job says so in as many words: the archive is
restorable, and that it carries data is **not** established today.

**Calibrated, on a throwaway postgres, three halves:** a faithful restore agrees; a copy missing one
row differs; a copy missing a whole table differs. The first attempt at that harness failed on
`FATAL: the database system is shutting down` two commands after a green `pg_isready` — the postgres
image runs a temporary server for `initdb` and `pg_isready` answers yes to it, which is
[6.8](#68--the-readiness-probe-that-was-measuring-a-server-about-to-be-destroyed) again in a
different costume.

**Then observed in the cluster**, which is a different claim from calibrated on a throwaway. The
strengthened job ran against the live database at `20260818T234313Z`: 30238 bytes, 89 TOC entries
read back, restored into a scratch database with `--exit-on-error`, and fifteen tables named with
their counts on **both** sides — `app_rules audit_log blocked_domains children commands
device_state devices families installed_apps locations parents policies recovery_events
schema_migrations usage_samples`. Five had been listed by hand before; ten more exist. It closed
with `(compared 3 rows)` rather than the zero-row refusal, and that is the half worth stating: the
refusal and the count read the same query, so a run that printed a correct non-zero total is a run
that exercised the input the refusal depends on.

**A new `manifests` layer**, because Phase 7.4 recorded the render as calibrated once, by hand —
true on one day and unchecked since. A manifest directory is the artifact most able to rot without a
symptom here: no Go test compiles it, no suite applies it, and the failure surfaces in a cluster.
Ten assertions, each a property something else in the repository depends on rather than "does
kustomize exit 0", and each broken and observed red:

| # | assertion | break |
|---|---|---|
| 1 | `deploy/` renders, non-empty | an unterminated quote in the CronJob schedule |
| 2 | every object still present | `ingress.yaml` commented out of the kustomization |
| 3 | no `Secret` in the render | `secret.example.yaml` added to `resources` |
| 4 | every pod runs non-root | backup job `runAsUser: 0` |
| 5 | no floating tag | `postgres:latest` |
| 6 | backup and database on the **identical** postgres image | backup on `18.5` |
| 7 | the APK directory is mounted read-only | `readOnly: false` |
| 8 | startup, readiness and liveness probes declared | `startupProbe` removed |
| 9 | database strategy `Recreate` | `RollingUpdate` |
| 10 | the backup job restores what it dumped | the restore call removed |

Assertion 6 is not tidiness: `pg_dump` refuses to dump a server newer than itself, and a newer
`pg_dump` writes an archive an older `pg_restore` cannot read. Two tags that agree today are two
tags somebody bumps one at a time.

**The calibration found a defect in assertion 10 rather than in the manifest**, which is the second
time that has happened in this repository ([7.1](#phase-7--deployment-preparation-no-deploy) was the
first). Its first form looked for the string `pg_restore`, and the break deleted the restore while
leaving the cheap `pg_restore --list` parse in place — so the assertion stayed green with the thing
it exists to require gone. It now names the invocation it means, `pg_restore --dbname=`, and
requires `--exit-on-error` alongside it.

**Still open:** the dumps sit on the same storage as the data. That covers a bad migration and a
`DELETE` without a `WHERE`; it does not cover losing the machine, and RAID is not a backup — it
survives one disk, not one wrong command. Copying them off-host is a job on another machine, and no
cron entry in this repository can honestly claim to have done it.

---

## Phase 9 — Keeping the DPC current (FR-15)

The app on the phone *is* the enforcement, and until this phase the only way to replace it was to
factory-reset the child's phone and re-scan a QR code. A fleet you cannot update is a fleet frozen
at whatever build was current the day each phone was provisioned — and the node's own APK was
already a version behind the server it talks to.

**The shape.** The server hosts one DPC and answers `GET /device/apk-info` with the build number,
the size and the checksum of the bytes it will serve; `UPDATE_APP` is an ordinary instant command
(FR-9), so it queues, delivers, and acknowledges like every other one. The device downloads, checks,
and installs over itself with `PackageInstaller` as device owner — no prompt, because a device owner
is exempt from one, and no `REQUEST_INSTALL_PACKAGES` prompt is what makes this usable on a phone
nobody is holding.

**Five checks before anything is committed** (FR-15.3), each one because the platform's own version
of it fails later, more quietly, or on the phone: the same package name, a strictly greater build
number, the signer equal to the one already installed, the length the server announced, and the
checksum the server announced. `tests/android/calibrate-update.sh` breaks each one in turn and
records the refusal.

**The acknowledgement is sent before the commit** (FR-15.4), and that is not an optimisation: the
install kills the process that would otherwise send it. The confirmation that an update actually
happened is therefore the build number in the *next heartbeat*, read from the package manager rather
than from `BuildConfig` — and the DPC only gets to send one because `MY_PACKAGE_REPLACED` restarts
the foreground service the install killed (FR-15.5). Without that receiver the feature's failure
mode is the worst one available: a phone that updated, is managed, enforces nothing, and looks from
the console exactly like a phone that went offline.

### 9.1 — the layer that can see it, and the tunnel the product kept cutting

The JVM tests prove the five checks against fakes and the e2e tests prove the endpoints against a
real server. Neither can install anything, so `tests/android/self-update.sh` exists: it builds the
DPC twice from one tree (`-PbuildOffset=1` adds one to the build number and changes nothing else),
installs the lower one on a device, makes it device owner, and hands the rest to
`TestTheServerReplacesTheDPCOnARealDevice`.

**The first three runs failed identically, and the failure was the product working.** Each ended
with *"the console still shows the phone running build 0"*, and the server's request log showed the
same shape every time: one `POST /enroll`, one `GET /device/policy`, and then silence — no heartbeat,
ever. The suspicion was a crash inside the policy applier. It was not. The device's own log said:

```
09-04 22:37:19.591 DevicePolicyManager: Changing user restriction no_debugging_features
                                        on user 0 to: true caller: …io.github.helios57.familyguard…
09-04 22:37:28.435 FamilyGuard/Connection: start: applied v1 from SERVER — …
09-04 22:37:32.840 FamilyGuard/Connection: start: inventory not delivered: Failed to connect to /127.0.0.1:38723
```

The test reached the phone's control plane over `adb reverse`, and the first policy any device
applies contains `no_debugging_features` — so the DPC switched off the debug bridge that the tunnel
ran through, mid-test, exactly as it is supposed to on a child's phone. Every measurement after that
line was of a phone with no route to the server. The apply had succeeded; the heartbeat that would
have proved it was refused on a loopback port that no longer forwarded anywhere, and swallowed by
the `catch (_: IOException)` that keeps a phone with no signal from spamming its log.

**Three things came out of that, and the first is a habit rather than a fix.** The failure was
unfalsifiable from the test's own output: a DPC that crashed, one that was never started, and one
whose heartbeat was refused all print *"the console still shows build 0"*. The test now dumps the
phone's log and its running services on failure (`dumpDeviceLogOnFailure`), and says NOT MEASURED in
those words when `logcat -d` comes back empty — which it can, because the platform truncates that
buffer on rotation and returns zero bytes with status 0.

**The route is now `10.0.2.2`**, the emulator's fixed alias for the machine it runs on, which maps
to that machine's loopback — so the harness still listens on `127.0.0.1` and the phone reaches the
same socket by a name the DPC cannot revoke. The DPC already allowed it: `Enroller.CLEARTEXT_HOSTS`
is `setOf("localhost", "127.0.0.1", "10.0.2.2")` and the debug network security config names the
same three. The *server* allowed two of the three, and that one-name difference is what had forced
the tunnel. `RequireProvisioningURL` now names the same three, as a map beside the function rather
than as a chain of `==`, and `TestTheEmulatorHostAliasIsExemptToo` pins it with `10.0.2.3` as the
negative control — one digit away, because a rule written as a prefix would let a network through
where one host was meant.

**The layer is one-shot per device, and that is stated rather than worked around.** Everything
needing adb — install, device owner, the enrollment instrumentation, the reboot that starts the
service the way a boot does — happens before the first sync. Everything after it is read from the
control plane, which is how a parent sees the phone anyway. `confirmInstalledBuild` still asks the
platform directly and reports NOT MEASURED rather than passing quietly when it cannot, because on a
device where adb survives that read is the strongest evidence there is and an assertion that
silently stops running is one nobody notices. Putting a device back means a restart with
`-wipe-data`; there is no shell that can clear the restriction, since the restriction is precisely
the one that closes that door. `run_all.sh` runs this layer last for that reason, and
`require_one_device` now names it when it finds a device listed as `offline`.

### 9.2 — the permission the platform will not let a device owner take

Moving the route to `10.0.2.2` did not fix it. Run 4 failed with the same sentence, now over the new
address: *"failed to connect to /10.0.2.2 (port 37727) from /10.0.2.16 (port 51828) after 15000ms"*.
A shell on the same phone reached the same host and port in the same second and got HTTP 200 back,
repeatedly, on either interface — so it was not routing, not the NAT, and not the server.

The instrument that turned a twenty-minute run into a two-second one is `run-as`: the APKs are debug
builds, so `adb shell run-as io.github.helios57.familyguard toybox nc 10.0.2.2 <port>` runs the
identical command as the *app's* uid. Side by side against one `python3 -m http.server`:

```
shell (uid 2000)                          HTTP/1.0 200 OK
run-as (uid 10234, the app)               nc: connect: Connection timed out
```

Which made it a per-uid question, and `dumpsys connectivity trafficcontroller` answered it:

```
sLocalNetAccessMap (default is true meaning global):
  LocalNetAccessKey{... remoteAddress=10.0.2.0/120}: false
```

**Android 37 sorts every destination into global or local and drops an app's packets to a local one
unless the app holds `ACCESS_LOCAL_NETWORK`.** `10.0.2.0/24` is the emulator's own subnet, so the
bench control plane is local by construction; `adb`'s uid is exempt, which is why every shell probe
disagreed with every app probe. Dropped rather than refused, so the app sees a connect timeout and
its log names nothing — the same sentence a phone prints for a server that is down.

This is not only a bench property. A family that self-hosts the control plane at home reaches it at
`192.168.x.x` from phones on the same Wi-Fi, and those phones would fail exactly this way, silently.
`guard.example.com` over the internet is global and unaffected — which is why the product had never
shown it.

**A device owner cannot grant it to itself, and the way that fails is the interesting part.**
`setPermissionGrantState(..., GRANTED)` returns **true**, from a clean state and from a policy-fixed
one, and `checkSelfPermission` stays `DENIED`; `POST_NOTIFICATIONS` and the three location
permissions granted from the identical loop in the identical call. `adb shell pm grant` works, and
the grant then survives the DPC re-applying its whole policy. So three things changed:

- the app **declares** the permission, because a permission that is not declared cannot be granted by
  anyone, and with it a parent has a route (Settings → the app → Permissions → Local network devices);
- `grantOwnPermissions` **reads the result back** with `checkSelfPermission` instead of believing the
  return value, and logs `"… is not held (policy accepted=true)"` — the sentence that would have
  saved four runs. A grant that is believed rather than read is a permission the app only thinks it
  has, and every feature behind it then fails naming something else;
- the bench grants it explicitly (`device.sh`'s `allow_local_network`, read back from `dumpsys` and
  reported as NOT MEASURED if it did not take), and `DEPLOYMENT.md` carries the step for a
  control plane on the family's own network.

### 9.3 — a command queued in the gap was not delayed, it was lost

Run 5 got the phone enrolled, rebooted, reporting its build and taking commands — and still failed,
with the console showing the old build four minutes after `UPDATE_APP` was queued. The first guess
was that the deadline was simply shorter than the DPC's five-minute poll. Raising it to eight
minutes was run 6, and run 6 failed too, with the phone visibly healthy the whole way:

```
23:55:11      POST /api/v1/devices/:id/commands  the test queues UPDATE_APP  ← no stream open yet
23:55:12.327  start: inventory sent=265          the start sync finishes; the stream opens after it
00:00:13      poll: screen time NOT MEASURED     the phone is alive and polling
00:05:15      poll: screen time NOT MEASURED     …and still has not been told
```

**The poll is not a backstop.** `pollWhileAwake` calls `enforceFromCache()`: it measures, re-enforces
and re-books, and it fetches nothing — deliberately, because the quota has to bite on a phone with no
signal. So the event stream is the *only* path by which a command reaches a device, a publish reaches
the streams that are open at that instant and nothing else, and the `connected` frame was being
swallowed rather than treated as a wake-up. A command queued while a device had no stream open was
therefore not late — it was lost, until some later unrelated event happened to wake a sync that found
it. Every reconnect has this window, and the server closes every stream at fifteen minutes.

This test lands in that window by construction rather than by luck: it waits for the heartbeat that
reports the running build, and the stream opens a beat after it. That is what made a product defect
reproducible instead of occasional.

**The fix is in the product.** `EventStream` now forwards the `connected` frame to the wake handler
as well as resetting the backoff, so a device syncs the moment its stream is established and finds
anything queued while it was away. The cost is one extra fetch per connection — four an hour in
steady state. The reasoning it replaces ("a sync per connection would mean a phone on a flapping
network syncing continuously") was written into `EventStreamTest` as an assertion; it is now the
opposite assertion, with the measurement in the comment, and it is calibrated: restoring the single
`continue` takes both of the frame's tests red.

`updateDeadline` stays as a named constant, now five minutes, and it covers work rather than a race:
a 16 MB download over the emulator's NAT, the install, the process the install kills coming back on
`MY_PACKAGE_REPLACED`, and its first heartbeat.

### 9.4 — the run that passed, and what it saw

Run 7 got all the way through the install and then failed on the test's own spelling: it compared the
command state against `"acked"` while the store spells it `ACKED` (`store.CmdAcked`). Three sites,
one of them inside `awaitCommandSettled`, which therefore recognised no terminal state at all — a
command that had been acknowledged in under a second was reported as *"still ACKED after 5m0s"*.
A comparison that can never be true is the same shape as everything else in this phase: it does not
fail, it waits, and then it blames the product.

**Run 8 passed in 176 s**, `RESULT PASS the server replaced build 2 with build 3 on the device, and
the DPC reported the new build back`, and the phone's own log is the second witness:

```
00:35:22.863  FamilyGuard/Connection: wake:connected: commands done=1
00:35:23.766  PackageManager: installation completed for package:io.github.helios57.familyguard
00:35:25.116  FamilyGuardUpdate: this app was replaced; restarting the connection
00:35:25.812  FamilyGuardUpdate: self-update installed
00:35:34.301  FamilyGuard/Connection: wake:connected: commands done=1     ← the second, declined
```

Two seconds from "the phone found the command" to "the platform installed it", and the DPC back on
the connection a second after that — on a phone whose adb had been off since the first policy
applied. The last line is the negative control: the same command again, executed and answered
"already running" rather than reinstalling an equal build.

One measured cost is left as it is. The second command downloads the 16 MB APK again before
declining it, because every one of the five checks is made against the *file* and none against the
server's announcement — `ApkInfo` carries a URL, a size and a checksum, and deliberately no build
number. That is the design (§ the five checks), and the alternative is to let a server's claim about
its own artifact decide whether a phone downloads it. A parent pressing the button twice pays for it;
the phone never installs on the strength of something it was told.

### 9.5 — two failed READS, reported as two facts about the device

The first full `tests/run_all.sh` after the feature landed went **seven layers green and stopped on
the eighth**, and both of the ways it stopped were the same defect wearing different words:

```
android-self-update  NOT MEASURED  ACCESS_LOCAL_NETWORK is still not granted to …
android-self-update  NOT MEASURED  dumpsys device_policy returned nothing readable …
```

Neither sentence was true. By hand, on that same device, three minutes later: `pm grant` granted the
permission first try and it stayed granted through six reads over twelve seconds, and
`dumpsys device_policy` answered 454 lines, ten times in a row, immediately after a reinstall. What
both guards had actually seen was a **read that did not come back** — and each had turned that into
a statement about the phone.

That is the phase's own lesson arriving from the other side. A guard that fails closed is right to
refuse a green, but "I could not read this" and "I read this and it says no" are different findings,
and only one of them is about the product. Reported as the second, they send the next person to
audit a permission model that is working.

The mechanism is not mysterious, and it is not fully pinned down either. `dumpsys` gives each
service about ten seconds; at that moment the machine was finishing two Gradle assembles, running a
second emulator, and had just taken two `adb install -r -d`s. It does not reproduce on an idle
machine, which is exactly why it cannot be diagnosed at the call site — so both reads are now
**retried before they are believed**:

- `dpm_dump()` is the single place `dumpsys device_policy` is read, retrying up to five times over
  ~8 s for the one header line the service always emits, and returning 2 — never a guess — if it
  never arrives. All three former call sites go through it.
- `allow_local_network()` grants and reads back in the same loop, and keeps **`pm grant`'s own
  words** for the failure message. The old version discarded them to `/dev/null`, which is why the
  first failure could only be guessed at.

Calibrated, both halves each:

| probe | result |
|---|---|
| `dpm_dump` against an `adb` that prints nothing | `NOT MEASURED … returned nothing readable`, rc=2, **after 10 s** — the elapsed time is the evidence it retried rather than gave up |
| `dpm_dump` against the real device | `already the device owner`, rc=0, instant |
| `allow_local_network` against a package that is not installed | `NOT MEASURED … pm grant's last words: Failure [package not found]` — the sentence the old version threw away |
| `allow_local_network` from the revoked, `POLICY_FIXED` state the sweep hit | granted, read back from `dumpsys`, rc=0 |

The last row also kills a plausible theory that was worth killing: a permission left `POLICY_FIXED`
by the DPC's own `setPermissionGrantState` is **still grantable** by `pm grant`, and restarting the
DPC over a granted permission does **not** revoke it (measured over six reads). The device-owner
call is inert here, not hostile — so the Settings remedy in DEPLOYMENT.md stands.

With both retries in place the layer passed on the harder device: **157.5 s**, on a phone that had
already been through the whole instrumented layer on the same boot, from a deliberately revoked
permission. That is the case that had failed twice.

Then, from a freshly wiped AVD, **`tests/run_all.sh` in one continuous invocation: eight layers, all
eight PASS**, ending in `every requested layer ran and passed` — secret-scan, backend, manifests,
image, e2e, android-unit (461 tests in 52 classes), android-instrumented (provisioned and after a
real reboot), android-self-update (148.9 s). One sweep rather than eight remembered ones, because
the scope of a run is where its blind spot lives, and a summary is only worth what it covered.

---

## Phase 10 — the release that was refused, and the two defects it found

`v0.2.0` was tagged on a commit whose CI had been **green on every one of the six jobs minutes
earlier**. Its own `verify` — the same `ci.yml`, re-run against the tagged tree, which exists
because a tag can be pushed at a commit whose CI was cancelled or force-pushed past — came back
with **e2e and image red**. `publish` never ran, so nothing reached GHCR and no release exists.
That is the gate working, and it is the only reason either defect below was found: both are races,
and a race is green almost always.

The tag stands, unpublished, and `v0.2.1` carries the fixes. Moving it would have been the one
change a consumer cannot detect (DEPLOYMENT.md), and the price of not moving it is one version
number that names nothing — which is a fact, and cheaper than a tag that lies.

### 10.1 — the drawer told a screen reader the menu was shut while it was open

`TestConsoleRendersOnAPhone/drawer`: *the menu button's aria-expanded is "false" while the drawer is
open*.

`dialog.close()` fires its `close` event as a **queued task**, not synchronously. The console's
close handler wrote `aria-expanded="false"` unconditionally, because it was the close handler. So
closing and reopening inside one task — a parent picking a destination, which closes the drawer, then
reaching for the menu again — delivers the `close` event *after* `showModal()` has already set the
attribute to `true`, and the last writer wins. The drawer is open; assistive technology is told it is
not.

The fix is to read the authority instead of remembering the event: one `syncMenuButton()` that sets
the attribute from `drawer.open`, called from both paths.

**The calibration is the interesting half.** Breaking `syncMenuButton` outright reddened *both*
assertions, which proves nothing about the new one — so the original defect was restored verbatim
instead (open still sets `true`; the close listener writes `false` blindly). Under that, the
pre-existing check at `mobile_test.go:595` **passed** and only the new one failed. That is the
measurement: the old assertion does not bind to this defect, and it caught it once, in CI, by luck.
The new one forces the ordering — `closeDrawer(); openDrawer()` in a single evaluation — and counts
the `close` events as its positive control, so a `close` that never arrived cannot leave the
attribute at `true` and pass having tested nothing. Red on the defect, green on the fix.

### 10.2 — the smoke test's database probe was measuring a server that was about to be shut down

`dial tcp …:5432: connect: connection refused`, and five of twelve assertions failing at once, which
reads exactly like Docker being flaky.

`tests/image/smoke.sh` waited with `pg_isready -U postgres`. Without `-h 127.0.0.1` that uses the
**unix socket**, and during the postgres entrypoint's `initdb` phase the socket is served by a
temporary server that is about to be stopped. `tests/e2e/run.sh` had already found and fixed this;
the smoke script held the second copy of the loop, and the copy that gets fixed is the one whose
failure was seen most recently.

Measured here on the image this script actually uses, rather than cited from the other one: against a
fresh `postgres:18.6`, the socket answers ready at **1.53 s** and TCP not until **1.95 s**. Four
hundred milliseconds in which the old probe returns success and the very next connection is refused.
The temporary server runs with `listen_addresses` empty, so TCP is precisely what the init phase
cannot fake. With `-h 127.0.0.1`: **12 passed, 0 failed**.

Neither of these is a retry around a flaky step. A retry would have made both symptoms go away and
left a console that mislabels its own menu and a probe that reports on the wrong process.

### 10.3 — the node was still serving the first build, and the swap is two changes

FR-15 shipped in 0.2.1 and did nothing, because `/dpc.apk` is a file on the node's host path and not
part of any image: it was still the build published on 2026-08-18, `versionCode 1` / `versionName
0.1.0`, read with `aapt2` out of the bytes the running server actually served rather than from a
manifest. Replaced 2026-09-05 with `versionCode 4` / `0.2.1`, sha256 `eafeab56…`.

Three things about that are worth keeping, and only the second was expected.

**The certificate did not need to change, and that is checkable rather than assumable.** The digest
of a DER-encoded certificate *is* that file's own hash, so `sha256sum familyguard.der` on the node
and the `certificate SHA-256 digest` `apksigner` prints for the new APK are the same number —
`b62cda94…` both ways. The key an enrolled phone would trust is the key that signed the new build.

**A swap under a running pod ships nothing.** The server hashes the APK once at startup and
publishes that value in every provisioning QR and every `UPDATE_APP` response, so `/dpc.apk` stops
serving the file entirely and answers `503 apk_changed` instead. That is not a theory about the
guard in `internal/httpapi/apk.go`; it is what the deployment returned, 173 bytes of it, between the
file landing and the pod restarting — and the same request over the same file returned **200** with
`13330105` bytes hashing to `eafeab56…` once it had. A production guard observed red and then green,
which is the only version of that claim worth writing down. The restart is driven by a pod-template
annotation carrying the file's sha256, so the swap is a commit; `kubectl rollout restart` writes
drift that the next sync removes and leaves nothing recording which build the node serves.

**No phone is enrolled.** The `devices` table is empty (1 parent, 1 child, as a positive control on
the same query), which corrects a claim carried in this project's notes for two days: that the fleet
was already on a build newer than the node's, so `UPDATE_APP` would refuse. There was no fleet. The
consequence is better than the one that was planned for — the first phone provisioned gets 0.2.1
directly — and the untested path is unchanged: **no real phone has taken an update, only an
emulator.**

---

## Phase 11 — a security review of the whole tree, and what a second pass is actually for

A read of every file that handles a credential, a request body or a downloaded byte, against a
deployment that was already running. Nothing here was a live exploit: the auth paths, the CSP, the
device bearer hashing, the container's uid, the workflow permissions and the SHA-pinned actions all
held up. What a second pass finds is a different class — **bounds that were never stated, and claims
in prose that nothing enforces.** Six of those, and one comment that had gone false.

### 11.1 — the parent's BLOCK is defeated by a claim from the phone, and that is the requirement

The first thing this review found looked like a privilege inversion: a device sends
`critical_packages` at enrollment, and that list beats a parent's explicit `BLOCK`, an exhausted
quota, bedtime and a parent lock. It is reachable by anything holding an enrollment token.

I changed both engines so a parent's BLOCK won, and **that was wrong.** The shared vector
`critical whitelist survives bedtime, an exhausted quota, an explicit block and a parent lock`
(`backend/internal/policy/vectors.json`) encodes the opposite deliberately, FR-5.5 says the dialer
*"can never be suspended or hidden by any rule"*, and NFR-6(a) is emergency calling. Both edits were
reverted. **A shared vector that goes red is the design telling you the finding is a feature** — the
one place in this repository where two independently written engines have to agree, and neither may
edit it to make itself pass.

So the fix is at the boundary, where it costs the requirement nothing: `sanitizeCriticalPackages`
(`backend/internal/httpapi/deviceapi.go:490`) caps the list at **32** entries, drops anything not
shaped like a package name (two dot-separated segments, no leading digit, ≤255 bytes — Android's own
limit), and the enrollment audit entry now records **the names, not the count**, plus how many were
refused. It never fails the enrollment: a phone mid-provisioning in front of a parent must not be
turned away over an advisory list.

Calibrated in `deviceapi_test.go` — a real phone's four packages survive byte-identical (the
positive control, because a sanitiser that dropped everything would pass every other subtest),
`../../etc/passwd`, `https://…`, `com..double`, `com.9leading` and a 300-byte name are dropped, and
42 entries store 32 and report 10.

### 11.2 — the JWKS verifier accepted any RSA key Google might publish, including a 512-bit one

`backend/internal/auth/oidc.go:274` parsed the modulus and used it. Nothing checked its size, so an
attacker who could answer the JWKS fetch — a compromised discovery document, a hostile resolver —
could publish a key small enough to factor and mint parent sessions. A 2048-bit floor now refuses it
by name. Calibrated at 512, 1024 and **2040** bits (the interesting one: it is 8 bits short, so an
off-by-one in the byte-to-bit arithmetic would pass it) with 2048 as the negative control.

### 11.3 — "single tenant" was a sentence, not a constraint

`0001_init.sql`'s header said the schema is single-tenant and every table hangs off `families`. True
as an intention; **no query filters on `family_id`**, so the isolation the sentence implies does not
exist anywhere in the code. Two families in that table is a data state no code path can describe.

Filtering forty queries for a family that cannot be created is the expensive way to make the sentence
true. `0003_single_family.sql` makes the second row impossible instead —
`CREATE UNIQUE INDEX families_is_a_singleton ON families ((TRUE))` — and 0001's header now says what
its `family_id` columns are (a record of which family a row belongs to) and what they are not (a
tenant filter). Applied by the e2e layer against a real PostgreSQL.

### 11.4 — nginx would buffer 64 MB for an unauthenticated client against a server that refuses at 1 MiB

`proxy-body-size: "64m"` on the ingress, and the comment said it was for the DPC download — which is
a *response*, so the comment was not merely stale, it named the wrong direction. Every route was
enumerated: nothing takes an upload, and the app's own `MAX_BODY_BYTES` is 1 MiB. With
`proxy_request_buffering` on by default, the 63 MB in between was work nginx would do before the
server got a chance to refuse. Now `2m`, which sits just above the app's limit so the client still
gets the app's JSON error envelope with its request id rather than nginx's HTML.

### 11.5 — a digest written in prose, two releases stale

The comment above the image line in `control-plane.yaml` named `0.1.0`'s digest while the tag beside
it said `0.2.1`. Nothing renders a comment, nothing applies it, and nothing goes red — which is the
whole failure mode of recording a digest in prose. Corrected against two independent authorities
that agree: the registry's `docker-content-digest` header for the `0.2.1` manifest, and the
`imageID` of the pod this cluster is running.

### 11.6 — the DPC would write whatever the server sent, forever

`AppUpdater` streamed the APK to disk with no ceiling. The server is the trust anchor, so this is
not an attack so much as a failure mode: a proxy that never closes, or a control plane that has been
taken over, fills the phone's storage. `ceilingFor()`
(`android-dpc/…/update/AppUpdater.kt:191`) allows the declared size plus 4 MB of slack, or 256 MB
when the server declares nothing, and abandons the download past it. Calibrated by a harness that
serves more bytes than it declared.

### 11.7 — the toolchain, and two tools that reported success having failed

Everything versioned was resolved against a live registry: **Go 1.27.1** (`go.mod`, both modules,
the Dockerfile and CI), **AGP 9.4.0**, **Gradle 9.7.1** with its published checksum, two action SHAs,
and five Go modules. `README.md`'s prose went red in `DocumentedVersionsTest` on the first sweep,
naming two lines — the guard from 6.15 working exactly as built.

Two false greens are worth keeping:

- **`staticcheck` and `govulncheck` printed *"export data version 4 is greater than maximum
  supported version 2"* and exited 0.** A tool built against an older toolchain cannot read the new
  one's export data, reports that as an internal error, and still succeeds. Rebuilt under
  `GOTOOLCHAIN=go1.27.1`; the real answers are staticcheck clean and *"No vulnerabilities found"*.
  This is the §3.9 shape: a tool's exit status was a claim about a different artifact than the one
  it was asked about.
- **`govulncheck -mode=binary` over-reported.** It named `x/crypto/ssh` and `openpgp` symbols in the
  released binary; `go list -deps ./cmd/server` shows neither is imported, with 11 other x/crypto
  packages matching as the positive control. `-ldflags="-s -w"` strips the symbol information the
  binary mode needs, and it falls back to module-level reporting without saying so.

### 11.8 — what was looked at and left alone

Recording this because "reviewed and found nothing" is only worth something if it says what it
covered: the six CI jobs' permissions and pinning, the Dockerfile's distroless pin and uid, the CSP
and the rest of `middleware.go`, the device bearer's SHA-256 storage and single-use enrollment, the
SSE stream, the policy engine and `resolve.go`, the backup CronJob's password handling and its
restore-and-count verification, the Android manifest's one exported component and its
`RECEIVER_NOT_EXPORTED` / `FLAG_MUTABLE` neighbours, and the network security config. `gitleaks dir .`
clean.

**Three residuals are known and not fixed**, each because the fix would cost more than the risk:
`ApiClient.readAll` and `EventStream.readLine` read server responses without a bound (the server is
the trust anchor, and 11.6 bounds the one case that writes to disk); `createCommand`'s
`Params map[string]any` is unvalidated beyond the 1 MiB body cap and parent authentication; and
child and device names have no length limit. **One is a real gap and is the owner's call:** the
`familyguard` namespace has **no NetworkPolicy**, while the CNI demonstrably enforces them elsewhere
on this cluster. A wrong selector there takes down the family's MDM and the nightly backup job, so it
is reported rather than pushed.

### 11.9 — 0.2.2, and the order the two halves have to go in

Everything above was inert until a tag moved: the deployed image predated all of it. `v0.2.2`
publishes it — `sha256:37739b4925639562837bfff32c3e4c520d5c516c263e7ed4c5ad34d6ced089f7`, agreed on
by the publish job's `containerimage.digest` and the registry's `docker-content-digest` header for
the `0.2.2` manifest, with `latest` answering 404 to the same query as the control that the read
discriminates. The running pod's `imageID` is that digest.

**The APK swap and the manifest commit are one release in two places, and the order is not free.**
The server hashes `/srv/apk/familyguard.apk` once at startup, so the pod-template annotation has to
describe what is on disk *at the moment the new pod starts*. Committing first restarts a pod that
hashes the OLD file and then finds the new one under it — `503 apk_changed` until a second restart.
So: swap the file, then commit. Between the two, `/dpc.apk` returned exactly that 503 with its
reason in the body, which is the 10.3 guard observed again rather than assumed, and nothing was
enrolled to see it.

Signed with the same key — `apksigner verify --print-certs` and the exported DER agree on
`b62cda94…`, so the DER on the node was already correct and was not touched. `aapt2 dump badging`
on the **signed file** reports `versionCode='5' versionName='0.2.2'`; the build script is not the
authority for what shipped. `curl https://…/dpc.apk | sha256sum` returns `c76ec970…`, the hash of
the bytes signed locally. 0.2.1 is archived beside 0.1.0.

**Migration 0003 was calibrated in production, against the family's own row.** A second `families`
row was attempted inside a transaction that rolls back either way: refused with
*"duplicate key value violates unique constraint `families_is_a_singleton`"*. The positive control
is a temp-table insert in the same session, which succeeded — so the refusal is the constraint and
not a permission. `families=1` before and after. A constraint that has never refused anything has
not been shown to work, and this one now has.

### 11.10 — the NetworkPolicy, and the allow the policy could not narrow

11.8 left this as the owner's call because a wrong selector takes the family's MDM offline. It was
made, and the selector that would have done exactly that is worth naming: **ingress-nginx runs
`hostNetwork: true` on this node**, so Cilium sees its requests with the `host` identity and the
obvious rule — `fromEndpoints: namespace: ingress` — matches nothing. A policy written that way is
green in every renderer and takes the console, `/dpc.apk` and every enrolled phone down on sync.

The gap was measured before the fix, from a pod in an unrelated namespace, with controls on both
sides — `kubernetes.default.svc:443` OPEN to show the probe connects at all, a port with no listener
closed to show it can tell the difference:

| probe | before | after |
|---|---|---|
| `familyguard-db:5432` from `paperless` | **OPEN** | closed |
| `familyguard-control-plane:8080` from `paperless` | **OPEN** | closed |
| `kubernetes.default.svc:443` from `paperless` | OPEN | OPEN |

And the service kept working across it: `/`, `/healthz`, `/readyz` (`database: ok`) and `/dpc.apk`
all 200, the APK still hashing to `c76ec970…`, egress to `www.googleapis.com:443` and
`oauth2.googleapis.com:443` OPEN from inside the namespace with `:80` closed as the control that the
port scoping binds, and a real backup Job run end to end under the policy — dump, restore into a
scratch database, row counts compared table by table, `schema_migrations=3`.

`endpointSelector: {}` rather than `app=`: the nightly backup Job's pods carry only
`job-name`/`controller-uid` labels, so a keyed selector would have left the one workload that reads
the entire database unenforced.

**One claim in the first version of that file was wrong, and the datapath is what caught it.** It
said the host rule's `toPorts: 8080` left PostgreSQL unreachable from the host network. It does not:

```
$ cilium-dbg bpf policy get 509        # the familyguard-db endpoint
Allow  Ingress  reserved:host  ANY       … prefix 0     ← Cilium's, not ours
Allow  Ingress  reserved:host  8080/TCP  … prefix 24    ← ours, narrowing nothing
```

`allow-localhost` defaults to `auto`, under which the host reaches every local endpoint
unconditionally. From the node, `10.1.0.152:5432` is still OPEN. Two things follow. The six
hostNetwork pods on this node all carry that identity. And the explicit host rule was never what
kept the console alive — the implicit allow was, which means the rule's *calibration* had been
passing for the wrong reason.

It is not closed, and that is a decision rather than an omission: `allow-localhost: policy` is a
cluster-wide switch affecting host-to-pod traffic in every other namespace, and reconfiguring a
working cluster to tighten one new app is the wrong direction. The residual is small in the shape
that matters — PostgreSQL's data directory is a hostPath, so root on this node already holds the
database as files whether or not it can open 5432.

### 11.11 — the second copy, and the array it is actually on

The last of 11.8's open items. `backup.yaml` had said since it was written that its dumps land on
the same RAID as the data and that copying them off was "a separate job"; `backup-mirror.yaml` is
that job, at 03:50, half an hour behind the dump.

**The destination is md0** — `/media/raid`, two USB-attached 16.4 TB Seagates striped as RAID0,
xfs. It shares no spindle with md1, where the database and its dumps live (`/proc/mdstat`, and the
target directory's `df` resolves to `/dev/md0` rather than to the root filesystem under an empty
mountpoint). Being precise about what that buys is the point: it survives md1 dying, a bad
migration and a `DELETE` without a `WHERE`; RAID0 has no redundancy of its own, so either of those
two disks loses the whole copy; and both copies are still attached to this machine, so fire, theft
and anything that gets root here still take both. Half the gap, and the half that remains is
labelled in the file rather than quietly dropped.

**A separate CronJob, not four more lines in the one that works.** If the mirror target vanishes,
the thing that must not stop is the dump-and-verify — so a missing mirror is a red mirror, never a
night with no backup. The verified-backup path is byte-identical; only its header comment changed,
which does not alter the applied object.

`type: Directory` on the mirror hostPath is load-bearing. Unmounted array → the pod refuses to
start, naming the missing directory. `DirectoryOrCreate` would create it on the root filesystem
under the empty mountpoint and mirror every dump onto the wrong disk, reporting success.

Three things the job refuses to call success: an empty source directory (that means the *other*
job is broken, and "0 files, all good" on that morning is exactly the control-that-evaluates-nothing
this deployment keeps finding); a destination file whose name is right and whose sha256 is not; and
a closing count on the destination below the source. Copies are written dot-prefixed and renamed,
so an interrupted one never carries the final name.

Calibrated on the real directories, three runs:

| run | source | result |
|---|---|---|
| first, empty mirror | 21 dumps | 21 copied, 0 already correct |
| again, nothing changed | 21 dumps | **0 copied, 21 already correct** |
| after `truncate -s -64` on one mirrored file | 21 dumps | **MISMATCH named, 1 re-copied**, 20 already correct |

and the two copies of the corrupted file hash identically afterwards. The middle row is what shows
the job is not simply re-copying everything every night; the third is what shows the hash check
binds rather than trusting a filename. **Not calibrated: the empty-source refusal** — testing it
means emptying the directory it exists to protect, so it is a plain count guard that has never been
observed firing.
### 11.12 — can the phone be bricked? measured on a device, not argued from the source

The owner asked the question that has to be answered before a real phone is handed over: *installed
as Device Owner on a Galaxy S20, can it still be factory reset, or can it end up bricked?* The
answer is yes it can be reset — and the evidence below is what makes that a measurement rather than
a claim, because everything the repo had until now argued it from the source.

**The DPC has no wipe API at all.** Grepped across `app/src/main`: no `wipeData`, no `wipeDevice`,
no `setFactoryResetProtectionPolicy`, no `setMaximumFailedPasswordsForWipe`, no `resetPassword`, no
`clearDeviceOwnerApp`, no `setLockTaskPackages`, no `setKeyguardDisabled`, no password policy of any
kind. The whole `DevicePolicyManager` surface it touches is user restrictions, package
suspend/hide, private DNS, automatic time, and `lockNow`. There is no code path that wipes the
phone and none that can lock a parent out behind a credential.

**Four layers keep `no_factory_reset` off, and three of them actively remove it rather than merely
not adding it:**

| layer | what it does | file |
|---|---|---|
| the Go engine | `forbiddenRestrictions` subtracted from every computed set | `backend/internal/policy/engine.go:306` |
| the Kotlin engine | same set, same subtraction, so a phone talking to a *different* server is covered | `EnforcementEngine.compute()` |
| `RestrictionPlanner.plan()` | filters the forbidden keys out of whatever a desired state carries | applied on every sync |
| `RestrictionPlanner.floor()` | puts every forbidden key it finds in effect into the plan's **clear** list | provisioning compliance **and every boot** |

`HardeningManager` then reads the restrictions back from `UserManager` and reports
`stillForbidden` — an un-wipeable phone — rather than trusting the calls it made.

#### the gap: an absence assertion is worth what its positive control is worth

`WipeabilityTest` and `WipeableAsFoundTest` assert `no_factory_reset` is **absent**. Their positive
control proves the *baseline* landed, which shows the gateway works. It does not show that this
particular key would be visible if it were in effect — and the restriction constants are hand-copied
strings that the platform accepts silently when wrong. That is not hypothetical here:
`RESTRICTION_PRIVATE_DNS` was measurably `no_config_private_dns` on both sides, and the emulator
dropped it without a word. A misspelled `no_factory_reset` would leave every wipeability assertion
in this repo passing forever while the real restriction went unpoliced.

`FactoryResetRecoveryTest` (new, instrumented) closes it from the other side: it **sets**
`no_factory_reset` through the real `DpmRestrictionGateway`, asserts the platform reports it — the
control the absence tests cannot carry — and only then asks the DPC to deal with it, once through
`applyBaseline()` (the provisioning path) and once through `BootReceiver` (the path a real phone
runs on every boot). An `@After` clears the restriction whatever happened above and asserts the
device came back: it is the only test in the repo that deliberately makes a device un-wipeable, and
leaving it that way would be worse than the bug it looks for.

#### calibration

Every guard was broken, watched go red, and restored.

| what was broken | layer | result |
|---|---|---|
| `plan()` stops filtering the forbidden set | JVM | RED — `RestrictionPlannerTest > a forbidden restriction is never applied, however it arrives` **and** `RestrictionApplierTest > a forbidden restriction is never requested, whatever the server sends`; 2 of 462 |
| `floor()` stops clearing a forbidden key it finds | JVM | RED — `RestrictionPlannerTest > the floor still clears a forbidden restriction, and only that` **and** `HardeningManagerTest > a boot still clears a forbidden restriction it finds in effect`; 2 of 462 |
| `floor()` stops clearing, on a real device | instrumented | RED — both `FactoryResetRecoveryTest` cases, and the failure text carries the platform's own effective set with `no_factory_reset` in it: *"a boot left no_factory_reset in effect… In effect: [disallow_config_private_dns, no_add_clone_profile, …, no_factory_reset, …]"* |
| the Go engine emits `no_factory_reset`, filter deleted | backend | RED — `TestNoRestrictionCanBlockCallingOrRecovery`: *restriction "no_factory_reset" would break emergency use* |

The third row is the one that matters most: it proves the platform genuinely *reports* the
restriction, so the green runs are absence measurements and not silence.

#### the result, and the authority it was read from

`tests/android/instrumented.sh` → **PASS, provisioned and after a real reboot.** Pass 1: 18
testcases, `WipeabilityTest` 3 and `FactoryResetRecoveryTest` 2. Pass 2 after a real `adb reboot`:
`WipeableAsFoundTest`, 1 testcase. Read back from the platform afterwards, `dumpsys user` for user 0:

```
Effective restrictions:
  no_add_private_profile  no_add_user  no_uninstall_apps  no_add_managed_profile
  no_install_unknown_sources  disallow_config_private_dns  no_add_clone_profile
  no_safe_boot  no_config_date_time
```

Nine restrictions, and `no_factory_reset` is not among them. `no_safe_boot` blocks Safe Mode, which
is not the recovery menu.

#### three defects the run itself found

**1. AGP splits its instrumentation arguments on commas, so pass 1 had been FAIL-by-construction
since the FR-15 work landed.** `tests/android/instrumented.sh` excluded two classes with
`-Pandroid.testInstrumentationRunnerArguments.notClass=$AS_FOUND,$ENROLL`. AGP's own `--info` line
shows what reached the device:

```
am instrument -r -w -e notClass io.github.helios57.familyguard.WipeableAsFoundTest -e additionalTestOutputDir … 
```

The second class became a malformed second argument and was dropped in silence, so
`ServerDrivenEnrollmentTest` ran in every sweep and failed every time — on a filter that reads
correctly in the script and is correct at the runner: `am instrument -e notClass A,B` by hand
excludes both, measured. Fixed by a marker annotation, `@SequencedByAScript`, which is one value
with no comma in it and cannot narrow itself however many classes end up wearing it.

**2. The `dumpsys device_policy` retry guarded the header, not the section its caller reads.** A
truncated dump carries `Current Device Policy Manager state` and stops before `Enabled Device
Admins`, and `dpm_dump` accepted it — so `admin_is_enabled` returned "could not determine" with no
retry, and the sweep ended NOT MEASURED against a device that was fine. Measured at load 30 shortly
after two Gradle installs; the same dump read by hand a moment later was complete, admin enabled,
service bound. `dpm_dump` now takes the marker the caller needs and retries for it.

**3. A green pass whose numeric floor could not see the wrong test set.** The sweep that found
defect 1 ran 18 testcases against a floor of 3 — comfortably over, and running the wrong classes.
`ran_class_count` now names `WipeabilityTest` and `FactoryResetRecoveryTest` and refuses a pass 1
that did not run them: a filter that matched *everything except* the FR-2.3 evidence is not caught
by counting.

**And one environment fault that is not a product fault.** A crashed instrumentation run leaves its
UiAutomation registration behind, and every later run then dies in `getUiAutomation()` with
*"UiAutomationService … already registered!"*, which surfaces in the JUnit XML as an unrelated
`IllegalStateException: Not connected!` from whatever `@After` ran next — here `UsageAccessTest`,
which was green on the same device and the same code an hour earlier. Only the logcat says why. A
reboot clears it, and the sweep was green afterwards. Recorded rather than fixed: the fix belongs in
the runner, not here.

#### what is NOT proven, and cannot be from this machine

- **The recovery-menu wipe on a physical Galaxy S20.** No emulator can exercise Vol-Up + Power. What
  is proven is that the restriction that would block the Settings reset is absent, and that no DPC
  API can reach the bootloader path at all.
- **Google Factory Reset Protection.** FR-2.3 says FRP is not registered and the code agrees — no
  `setFactoryResetProtectionPolicy` anywhere, and no column to hold an account for it. What the
  project cannot control is FRP armed by *Google* because a Google account was signed in on the
  phone before or after enrollment. That is Samsung/Google behaviour, not this system's.
- **`tests/android/self-update.sh` was deliberately not run.** It enrolls the device against a live
  control plane, and the first policy it applies carries `no_debugging_features` — which switches
  `adb` off permanently on that AVD. Running it to tidy a summary would have cost the device.

#### the one hatch that does close, and it is not the reset

`EnforcementEngine.compute()` adds `no_debugging_features` unconditionally once a device has synced
— deliberately, and it is out of the pre-sync baseline for exactly this reason. On a real phone that
means **USB debugging is off after the first successful sync, and the Developer-options toggle is
disabled**. It is recoverable through the console or the offline recovery code, and it does not
touch the reset. But a parent who expects to keep `adb` on a pilot phone should know it goes away,
and turning it into a policy setting a parent can see is a decision, not a defect.

Related, and measured on the emulator: **`adb shell dpm remove-active-admin` refuses** —
*`SecurityException: Attempt to remove non-test admin`* — because the shipping DPC is not a
`testOnly` build. There is no adb route out of Device Owner either. Factory reset is the route out,
which is precisely why FR-2.3 is the requirement it is.

---

## Phase 12 — installing other applications (FR-16), and a credential that is not a browser (FR-17)

The owner asked for one thing: *"add the feature to install other apk as well (for example
muplay)"*, with two decisions taken up front — the APKs come from **files on the node plus an upload
in the console and a REST endpoint**, everything reachable **with an API key as well, for the MCP**;
and the install model is a **declared set per child**, not a queue of commands.

The declared set is the decision that shaped everything else. A command queue would have been less
code: press *install*, enqueue `INSTALL_APP`, the phone acks. It is also the shape that cannot
answer "what is supposed to be on this phone" — a queue records what somebody asked for once, and
the phone's actual contents drift away from it the first time a child uninstalls something. A
declared set is re-evaluated on every sync, so it self-heals, it survives a re-enrollment, and the
console's switch and the phone's contents are the same fact rather than two facts that agree for a
while. It also means the whole feature reuses the machinery that was already there: the set rides in
`desired-state` beside bedtime and the app rules, both engines compute it, and the shared vectors
police them against each other.

### 12.1 — which restrictions bind the device owner, measured on a phone rather than argued from the source

The feature is unimplementable if the DPC's own hardening stops it, and three restrictions were
candidates. All three are set by this product on a normal phone: `no_install_unknown_sources` and
`no_uninstall_apps` are in `BASELINE_RESTRICTIONS`, so they are on before the phone has ever reached
the server, and `no_install_apps` joins them whenever a parent turns child installs off.

Reading `PackageInstallerService` would have been the cheap answer and the wrong method: the
device-owner exemptions live there, they have moved between releases, and one of the two refusals
below is delivered to a broadcast receiver rather than raised. So it was measured, on the emulator,
with a real second application built from `android-dpc/fixture-app/` and staged into the test APK's
assets. Measured 2026-09-05, API 37:

| restriction | binds a device owner? | shape of the refusal |
|---|---|---|
| `no_install_unknown_sources` | **no** | — install succeeds with it in effect |
| `no_install_apps` | **yes** | `SecurityException: User restriction prevents installing`, thrown **synchronously from `createSession`**, before a session exists |
| `no_uninstall_apps` | **yes** | `STATUS_FAILURE_BLOCKED` (2) / `DELETE_FAILED_USER_RESTRICTED`, delivered to the **result receiver**, package still installed |

Three consequences, and each is now a test rather than a paragraph:

1. **`no_install_unknown_sources` is not lifted, and that is the load-bearing half of the table.**
   It is the restriction in effect during every managed install this product will ever do. Had it
   bound, FR-16 would have required opening the child's own sideloading path for the length of every
   install on every sync. `noInstallUnknownSourcesDoesNotBindTheDeviceOwner`.
2. **The other two must be lifted, so `HardeningManager.withoutRestrictions` exists** — and its
   window is as narrow as it can be: the download happens outside it, and only the installer session
   and the uninstall run inside. `ManagedAppApplier` gets that for free from the deferred-commit
   shape FR-15 already had (`UpdateOutcome.Staged(identity, from, commit)` separates verifying from
   committing), which is the second time that shape has paid for itself. A 30 MB download inside the
   window would leave `no_install_apps` off for minutes.
3. **The two refusals have two shapes, so a caller that guards only against exceptions reads a
   blocked removal as a successful one.** That is not hypothetical: it is what
   `noUninstallAppsBindsTheDeviceOwnerAndFailsAsAStatusNotAThrow` asserts, and it is why
   `AndroidInstaller.uninstallAwaiting` waits for the status instead of returning when the call
   returns.

**Two of these tests originally asserted the opposite.** They were written from the spike's
hypothesis — that a device owner is exempt from its own restrictions — and they went red the first
time the layer ran against a device, with `SecurityException: User restriction prevents installing`
on a line that expected `STATUS_SUCCESS`. The implementation was already right (the lift was written
because the design assumed the platform would bind); the *tests* were the artefact carrying the
guess. They now assert the refusal, by name, and each says what to do on the day it stops failing:
if the platform stops binding, the lift is unnecessary and should be **removed** rather than left
open.

The narrowness itself is a separate measurement, because "the lift can be closed before the commit"
is a different claim from "the lift is needed at all":
`theInstallRestrictionIsCheckedWhenTheSessionOpensAndNotAtCommit` opens the session with the
restriction lifted, puts it **back on before `session.commit()`**, and requires the install to
succeed anyway. If the platform re-checked at commit, the narrow window would produce
`INSTALL_FAILED_USER_RESTRICTED` on any phone whose parent had turned child installs off, and the
applier would retry it forever.

**A third test was wrong in a way that only a whole-class run could show, and it is the more
instructive one.** `aDeclaredAppIsInstalledThenUpgradedThenWithdrawn` withdrew the declaration with a
raw `PackageInstaller.uninstall`, and came back `STATUS_FAILURE_BLOCKED` (2) — because the baseline
had `no_uninstall_apps` in effect by then, exactly as the table above says it would. The test was
asserting the product's behaviour while bypassing the product's own recipe for it. It now withdraws
through `HardeningManager.withoutRestrictions`, the same call `ManagedAppApplier` makes, and then
asserts the restriction is **back** afterwards — so the test measures the lift rather than routing
around it. This is the shape a test is most likely to hide: run first, before anything has applied
the baseline, it passes. It only fails once something else in the class has hardened the phone,
which makes it look like flakiness or like a bad neighbour rather than like a wrong test.

The same flaw was in the class's own `@Before`: it removed a leftover fixture with a raw uninstall
under `runCatching`, so on a device where a previous aborted run had left both the fixture installed
**and** `no_uninstall_apps` set, the cleanup failed silently and the first assertion then reported
*"the fixture is not installed yet and is already a removal candidate"* — a message about the
product, produced entirely by the harness. `removeAnyLeftoverFixture()` now clears the restriction,
uninstalls, and **fails the run** if the leftover survives, on the ground that a class which cannot
establish its own preconditions is measuring the state a previous run left rather than the one it
set up.

Final state, measured 2026-09-05 on API 37: all six tests green in one run of the class
(`OK (6 tests)`), after one earlier attempt was aborted by the emulator fault described below.

**Getting that run required fixing the harness, and the bug it had is the same defect class this
document keeps recording: a readiness check that passes having checked the wrong thing.**
`tests/android/instrumented.sh` waited for `sys.boot_completed`, slept ten seconds, and blindly sent
`input keyevent 82`. On this emulator user 0 reaches `RUNNING_UNLOCKED` about **twenty seconds after**
that property flips — and the package service answers earlier still, so every readiness signal the
script had was satisfied while credential-encrypted storage was shut. Tests then failed in two shapes,
neither of which says "locked":

* anything opening `SharedPreferences` died with *"SharedPreferences in credential encrypted storage
  are not available until after user (id 0) is unlocked"*, which reads as a defect in the encrypted
  store;
* `ActivityScenario.launch(RecoveryActivity::class.java)` died with *"Unable to resolve activity for:
  Intent { … cmp=io.github.helios57.familyguard.**test**/…RecoveryActivity }"*, which reads as a
  manifest or packaging problem and sends you to inspect a manifest that is correct. It is neither.
  `ActivityInvoker.getIntentForActivity` builds the intent against the **target** context, calls
  `resolveActivity`, and **on null silently rebuilds it against the TEST context** — which can never
  resolve, because the activity is not in that APK. A non-`directBootAware` activity does not resolve
  while the user is locked, so the null *is* the lock, and the message you get is about the fallback.
  Disassembling `androidx.test:monitor:1.8.0` was the only way to see that; the stack trace names
  neither the lock nor the fallback.

Calibrated both ways on the same tree, no other change: `StatusScreenTest` failed **4/4 on four
consecutive runs** without the wait and passed **4/4** with it. `wait_for_unlocked_user` now runs
before pass 1 and after the reboot, and reports **NOT MEASURED** rather than red if the user never
unlocks — because "the device was never ready" and "the product is broken" must not arrive as the
same colour.

**The emulator on this host is separately a false-red generator, and its failures also name the
product.** `surfaceflinger` aborts in its `RegionSampling` thread with
`Assertion failed: !rcEnc->featureInfo()->hasReadColorBufferDma` — a host-emulator/guest-image GL
feature mismatch, nothing to do with this repository — and takes `system_server` with it, which
re-locks the user and starts the cycle above again. Measured: every ~45 s by default, and about every
five minutes with `com.android.systemui` disabled, which is what made a clean run reachable at all
(SystemUI is what asks for region sampling; none of these tests use it). `-gpu swiftshader_indirect`,
`-feature -Vulkan`, `-feature -GLDMA,-GLDMA2` and a cold boot each changed nothing. So an
instrumented result from this host is evidence only if the run completed without
`INSTRUMENTATION_ABORTED` and executed the expected number of testcases — the harness already counts
them, and a truncated run reports **NOT MEASURED**. A gradle result of *12 of 24 tests, one failure,
empty failure message* is that truncation, not a red.

The final green: `pass 1 — provisioned state: gradle rc=0, testcases executed=24`, `pass 2 — as the
boot left it: … executed=1`, `RESULT PASS provisioned and after a real reboot`, 2026-09-05, API 37.

**So the harness had to learn the difference between a dead platform and a red test — and the first
version of that net was measured swallowing a real red.** `pass()` already turned an *adb* dropout
into NOT MEASURED, but adb stays perfectly healthy while `system_server` dies: gradle reports failing
tests, and the XML holds a truncated run. The new net reads the run's own output for
`Can't find service:` / `Transport endpoint is not connected` — taken from the output rather than
from the device afterwards, because the platform is back inside twenty seconds and by then the
evidence is gone.

Calibrated with a deliberate `Assert.fail` in the first class pass 1 runs:

| # | state | expected | measured |
|---|---|---|---|
| CAL H1 | broken test, platform also died in the same run | FAIL | **NOT MEASURED — the net swallowed the red.** The whole point of the layer, lost to its own safety net |
| CAL H2 | same break, after `messaged_failure_count` was added | FAIL | FAIL, printing *"1 failing testcase(s) carry a message, so this is a red and not a dead platform"* |
| CAL H3 | break removed | PASS | PASS, 24 + 1 testcases |
| CAL H4 | the counter, against an XML holding one real failure and one crash-truncated one | 1 | 1 |

CAL H1 is the one to keep. **A net that catches genuine failures is worse than no net**, and it took
a calibration to see it: the run *looked* correctly classified, because the platform really had died.
AGP writes the testcase it was interrupted in as `<failure></failure>` — no message, no stack — while
a real assertion writes the throwable. That is the whole discriminator, and a message-carrying
failure now wins over the crash signature.

Three consecutive runs of the two device layers, unchanged, show all three verdicts in order:
`NOT MEASURED (the platform died under pass 1)`, then `NOT MEASURED (pass 1 passed without running
WipeabilityTest)` — the pre-existing per-class count guard catching a truncation gradle had called
success — then `PASS` for both layers. A two-valued harness would have reported those first two runs
as reds against FR-16.

### 12.2 — the JVM calibration, including one break that proved a test binds to nothing

`ManagedAppApplierTest` drives the applier with every dependency a function, so the whole convergence
decision runs off a device: a `Phone` harness records what restrictions were in effect **at the
moment** each half ran, which is the only way to assert a narrow window. Asserting on the gateway's
final state cannot tell a lift that closed immediately from one that stayed open for the whole
download.

| # | break | expected | measured |
|---|---|---|---|
| CAL 1a | removal set `installedByThisApp().plus(keep).filterTo(…) { it !in keep }` | red | **green — the break was a no-op.** Adding `keep` and then filtering `keep` out is the original set. A calibration that changes nothing measures nothing, and it read exactly like a test that does not bind |
| CAL 1b | removal set `installedByThisApp().toSortedSet()` — remove the `it !in keep` filter | red | 2 red |
| CAL 1c | removal set always empty | red | 3 red |
| CAL 2 | `withoutRestrictions` restores `keys` (what it was asked to lift) instead of `lifted` (what it read back as actually in effect) | red | 3 red |
| CAL 3 | `AppUpdater` check 4 disabled — accept an archive whose package is not the one that was asked for | red | 3 red |
| CAL M1 | the catalog volume mounted `readOnly: true` | red | 1 red, naming the mount |
| CAL M2 | the catalog volume not mounted at all | red | 1 red |
| CAL M3 | `APK_DIR` pointed at the read-only DPC directory | red | 1 red |
| CAL M4 | `APK_DIR` unset | **green, and say why** | green, printing *"APK\_DIR is unset: this deployment hosts no application catalog, nothing to check"* |
| CAL C1 | `input[type=file] { min-height: 30px; font-size: 13px }` | red | 4 messages: two from the generic 360 px sweep and two naming the upload control |
| CAL C2 | the mobile guard's seed withholds the catalog upload | red | red — *"the catalog sheet lists 0 builds … the row layout is NOT MEASURED"* |

**CAL 1 is the one worth reading.** `it never removes a package it did not install` is the
assertion that keeps a withdrawn declaration from reaching an app the child installed, and it is the
most important thing in the class. Calibration says **it binds to nothing at the JVM layer**: the
applier is only ever *handed* the set of packages this app installed, so there is no input that
makes it reach outside that set, and the two breaks that did go red went red on the *other* two
tests. The doc comment now says so, and points at the control that does bind —
`ManagedInstallTest.theRemovalCandidatesAreOnlyWhatThisAppInstalled`, which measures
`getInstallSourceInfo` on a device with a positive control (the phone reports more than one package)
and an assertion that the returned set is a strict subset. A guard defined, unit-tested and never
actually exercised is the defect class this repository keeps finding; a guard whose unit test cannot
fail is the same defect with a green next to it.

CAL 1a is worth keeping in the table for the opposite reason. It is a calibration that failed to
calibrate, and for a few minutes it read as "this test does not bind" — which happened to be the
right conclusion, reached from a break that could not have shown it. **A break that changes no
behaviour proves nothing about the test, in either direction.**

### 12.3 — two encodings of one number, and the seam that pays for it

The catalog stores each APK's SHA-256 as **hex**, because that is what every other tool prints:
`sha256sum`, `apksigner`, the console, an operator comparing two lines by eye. The device compares
against **base64url unpadded**, because that is what `/device/apk-info` already publishes and what
the DPC already computes for its own self-update.

Two encodings of one number is a smell, and the choice was where to pay for it. `resolve.go`'s
`checksumB64` converts at the one seam where the policy is built, so the phone has exactly one
comparison to make and the operator has exactly one readable digest. A row whose stored digest does
not decode to 32 bytes is **dropped from the policy** rather than sent with an empty checksum —
because the Kotlin engine's `normalizeManagedApps` drops an entry with an empty checksum anyway, and
one reason in one place beats the same outcome reached twice.

### 12.4 — the console, and a layout guard that measured a card it never saw

Three new surfaces: the *Apps you install* card with a switch per package, the catalog sheet
(upload, scan the node's folder, delete a build), and the API-keys card with the one-time token
reveal. All of it in the same hand-written vanilla JS under `script-src 'self'`.

`TestConsoleRendersOnAPhone` is the guard that measures a rendered 360 px page, and it was **green on
all of it before any of it was drawn.** The card only renders switches when the deployment has a
catalog, `newHarness(t)` sets no `APK_DIR`, and `seedAFamilyWorthLookingAt` registered nothing — so
the Apps screen carried a single line of prose where the switch list belongs, and the suite reported
a measured layout for a card it had never seen. The fix is two lines: the guard's harness is now
`catalogHarness(t)`, and the seed uploads the same fixture APK the catalog suite uses, with a label
long enough to be the string that overflows.

The catalog sheet is measured separately, because it is not in the DOM until it opens and because it
holds the one control in this console the browser draws for itself: `<input type="file">`. Its
button's size comes from the platform, not the stylesheet, and it is the single control most likely
to break the 44 px promise. Both halves are asserted — the generic sweep and a named check that says
*"the APK file input is 41 px tall … picking a file is the first act of adding an app"* — and the
sheet's row count is a `Fatal`, so a sheet with nothing in it is NOT MEASURED rather than a pass.

### 12.5 — the writable directory, and the mistake the manifest guard exists to catch

`APK_DIR` is the only path this process writes to, in a container whose root filesystem is read-only
and which runs as uid 65532. The obvious way to configure it is to copy the stanza above it — and
that stanza carries `readOnly: true`, because it holds the DPC whose checksum the provisioning QR
was computed from and nothing may overwrite it.

That copy produces a pod that never becomes ready, with a message about a directory nobody changed.
So `Load()` checks it at startup (stat, is-a-directory, and a probe file actually created and
removed) rather than at the first upload, and `tests/manifests/inspect.py catalog-dir-writable`
checks the *manifest*: a mount exists for `APK_DIR`, it is not `readOnly`, and it is not the same
volume as `APK_PATH`. It reports and returns rather than failing when `APK_DIR` is unset, because a
deployment that hosts no applications is correct — CAL M4 above is the calibration that the skip is
a skip and not a green.

The `hostPath` is `type: Directory`, not `DirectoryOrCreate`, for the same reason: kubelet creates a
missing one owned by root at 0755, the startup check then refuses, and the cause would be four
characters in a manifest.

### 12.6 — what FR-17 deliberately does not have

An API key is a second spelling of the same parent: same family, same role, no scopes. The
alternative — a permission model with two independent sources of truth — fails by granting something
in one and not the other, and is discovered by whoever could not do their job.

- **No expiry.** The realistic holder is a long-running MCP server, so any expiry offered is either
  set to "never" (a field that does nothing) or set and forgotten (an outage whose cause is a date).
  Revocation is the control that matters; it is checked in the same `WHERE` clause that resolves the
  key, so there is no window between the lookup and the check; and `last_used_at` is what tells you
  which key to revoke.
- **No route to a credential.** `requireInteractiveParent` refuses a key on session creation, parent
  creation, and key creation and revocation. A stolen key cannot grow itself a second foothold that
  outlives its own revocation.
- **No deletion by default.** Revoked keys stay listed, because "was this ever used, and when did it
  stop" is the first question after a laptop goes missing, and the audit log cannot answer it alone
  — it records the parent a key acted *as*.

### 12.7 — 0.3.0, and a limit that had to be carved rather than widened

Everything above 12.6 was inert until a tag moved. `v0.3.0` publishes it —
`sha256:609d62d4aef2bd336dd7e0e4188e44d7d622ce75cbfd68df5ca8954f8405b98d`, agreed on by the publish
job's `containerimage.digest` (run 33990865516) and the registry's `docker-content-digest` header
for the `0.3.0` manifest, read back anonymously. `latest` answers 404 to the same query, which is
the control showing the read discriminates rather than echoing whatever it is handed. The running
pod's `imageID` is that digest.

**The secret scan failed first, and the fix is worth recording because the easy fix was the wrong
one.** `tests/e2e/apps_test.go` needed a string *shaped* like an API key, to prove that a key which
was never issued is refused exactly as a bad session token is. gitleaks read it as a
`generic-api-key` and failed the push. The value was never a secret — the server mints the whole
token, so nothing outside it can produce one — but `fgk_` is deliberately designed to be
unmistakable in a repository or a log, and that design is what makes a plausible body behind it read
as real. So the literal is now assembled from parts at the point of use, and the occurrence already
in history is excused by **fingerprint** (`commit:file:rule:line`) in `.gitleaksignore` rather than
by a rule or path allowlist, which would keep excusing every future line of the same shape. Two
directions, measured: the entry as written gives `rc=0, no leaks found`; the same entry pointing one
row lower gives `rc=1` and the finding returns. This project has been bitten by the general version
of that mistake before — a calibration probe copied out of a vendor's documentation turned out to be
allowlisted by the scanner's own defaults, so the probe measured nothing.

**The APK swap goes BEFORE the manifest commit**, for the reason 11.9 recorded: the server hashes
`/srv/apk/familyguard.apk` once at startup, so the pod-template annotation has to describe what is
on disk when the new pod starts. Done in that order, and the evidence is that `/dpc.apk` never
answered `503 apk_changed` at all. `aapt2 dump badging` on the **signed file** reports
`versionCode='6' versionName='0.3.0' minSdkVersion='29'`; the build script is not the authority for
what shipped. Same key — `apksigner verify --print-certs` prints `b62cda94…`, which is what the
untouched DER on the node hashes to, so the certificate was not re-exported. And the two checksums
the server computed at startup were checked against the files rather than trusted:

| logged at startup | is base64url of | file |
|---|---|---|
| `package_checksum` `36I6i-g8F_…` | `dfa23a8b…` | the APK on the node |
| `signature_checksum` `tizalIrToI7…` | `b62cda94…` | the DER on the node |

`curl https://…/dpc.apk | sha256sum` returns `dfa23a8b…` — the bytes served over the internet are
byte-identical to the file signed locally.

**The upload limit could not be raised; it had to be carved.** The ingress caps every request body
on this host at 2m, and that number is load-bearing: with `proxy_request_buffering` on by default,
nginx accepts and buffers a whole body before forwarding, so a wide ceiling lets an *unauthenticated*
client make the ingress hold that much per request against a server that refuses at 1 MiB. Widening
it to land one endpoint would reconfigure a working limit for the sake of a new feature. So FR-16's
upload gets a second Ingress on the same host carrying one path, `/api/v1/apps`, at 64m, with
`proxy-request-buffering: off` — nginx forwards the headers immediately, so `requireParent` runs and
rejects while the body is still on the wire instead of after 64 MB is buffered.

Four probes, unauthenticated, against the live host:

| # | probe | measured | what it shows |
|---|---|---|---|
| CAL R1 | `POST /api/v1/apps`, 2 bytes | **401** JSON | the route exists and nginx forwards |
| CAL R2 | `POST /api/v1/apps`, 3 MB | **401** JSON | the carve-out holds — the *app* answered |
| CAL R3 | `POST /api/v1/children`, 3 MB | **413** HTML | the 2m rule is untouched elsewhere |
| CAL R4 | `POST /api/v1/children`, 2 bytes | **401** JSON | that path still works normally |

R2 is the measurement; R3 is what makes it mean something. Without R3 a green R2 is equally
consistent with having widened the limit everywhere.

**The writable directory is proven by the pod being ready, not by the manifest saying so.**
`config.go` creates a temp file in `APK_DIR` at startup and refuses to serve if it cannot — so a
`Running`/`ready=true` pod with `restarts=0` is a positive statement that
`/media/raid5/apps/familyguard/apps` is mounted and writable by uid 65532. The hostPath is
`type: Directory` rather than `DirectoryOrCreate` precisely so the *other* failure is loud: kubelet
would create a missing one as root:0755, the server would refuse correctly, and the log would send
whoever read it to the application instead of to the volume.

**Still not measured at the time of the release:** the API 29 floor, and any of this on hardware
rather than an emulator. The second half of that is partly superseded by Phase 13 — read it there.

## Phase 13 — the first real phone

**2026-09-06 00:55:27 UTC+2: a phone enrolled against the deployed instance and it worked.** Every
claim below is read out of the control plane's database, not reported from the handset.

The chain, from the audit log:

| time | actor | action |
|---|---|---|
| 00:47:50 | PARENT | `PARENT_SIGNED_IN` |
| 00:48:01 | PARENT | `DEVICE_ADDED`, then `ENROLLMENT_ISSUED` (30-minute window) |
| 00:55:27 | DEVICE | `DEVICE_ENROLLED` |

So the whole FR-1 path ran on real hardware for the first time: QR, an unauthenticated `/dpc.apk`
download onto a factory-reset phone, device-owner provisioning, the one-shot enrollment exchange,
and then a device credential that keeps working. The handset is a **Samsung flagship on Android 16
(API 36)** — which matters because Samsung's device-owner behaviour is not something an emulator
shows you, and because of what it is *not*, below.

What it has done since:

- `device_state` reports `app_version_name 0.3.0`, `app_version_code 6` — the phone is running the
  build released two hours earlier, and says so itself rather than being assumed to.
- `policy_version 1`, `connectivity wifi`, battery and screen state present; last heartbeat under
  three minutes old at the time of reading.
- **504 rows in `installed_apps`.** The inventory path is not a stub.
- Six `critical_packages` reported by the device and unioned into the never-suspend set, which is
  the FR-13.2 mechanism working with a real launcher, dialer and IME rather than an emulator's.

**What this does NOT establish, recorded here rather than discovered later.**

- **It is not the floor device.** API 36 is one below the API 37 emulator everything else was
  measured on, so almost none of the floor risk is retired. The sharpest example is
  `AndroidInstaller.installerOf`, the function that bounds what the managed-app applier may
  uninstall: it is version-split, `getInstallSourceInfo` on API 30+ and the deprecated
  `getInstallerPackageName` below. **That fallback has now still never executed anywhere** — not on
  the emulator, not on this phone. The floor is API 29.
- **FR-16 has never run on a phone.** `apps` 0 rows, `child_managed_apps` 0 rows. Everything in
  Phase 12 above is an emulator and an e2e result. Nothing has been installed onto this handset by
  the catalog.
- **No command has been issued** (`commands` 0) and **the recovery code has never been redeemed**
  (`recovery_events` 0).
- **FR-15 self-update has not run on hardware.** The phone was provisioned directly onto 0.3.0
  rather than upgraded onto it, so the update path is still emulator-only — the same gap 11.9
  described, unchanged.
- **`usage_samples` 0 and `locations` 0, and that is expected rather than a defect.**
  `PACKAGE_USAGE_STATS` is an **appop**: declaring it in the manifest does not grant it and no
  device-owner API can, so a parent has to walk Settings → Apps → Special app access → Usage access
  by hand. Until that is done, a zero count here measures the grant and says nothing about the
  feature — exactly the "zero in a window with no traffic" shape this project treats as *not
  measured*.

## Traceability

Each requirement maps to the phase that implements it and the test that proves it.

**Every name below was read out of the source, not out of the plan.** The table used to hold
aspirational names — `TestJourneyEnrollment`, `TestJourneyTelemetry`, `TestEmptySystemIsEmpty` and
nine others — written when the phases were designed and never reconciled with what got built. That
is its own false green: a traceability table naming tests that do not exist reads exactly like one
naming tests that pass, and the only way to tell the two apart is to go and look. Reconciled against
`grep '^func Test'` and `find -name '*Test.kt'`, which found **two requirements with no test at
all**; both are now marked, and marked is the point.

Reconciling the *rows* against `REQUIREMENTS.md` — a second pass, because the first checked only that
the named tests exist — found the other half of the same defect: **NFR-8 and NFR-10 had no row at
all.** A requirement absent from a traceability table reads as covered to anyone scanning it, and
unlike a wrong test name there is nothing to grep for. Both now have rows, and both say what is not
proven.

| Requirement | Implemented in | Proven by |
|---|---|---|
| FR-1 enrollment / QR | 3.2, 3.4, 5.2, 5.3 | e2e `TestDeviceLifecycleJourney`, `TestEnrollmentCredentialsAreSingleUse`, `TestTheQRPointsAtAnAPKThisServerServes`; `TestQRHoldsTheRealPayload`, `TestPayloadCarriesTheExtrasAndroidActuallyReads`, `TestExtraNamesAreExactlyAndroidsSpelling`, `TestChecksumIsComputedFromRealBytes`, `TestAPKIsServedWithTheChecksumTheQRWouldCarry`; `EnrollerTest` |
| FR-2 hardening | 5.2 | `HardeningManagerTest`, `EnforcementEngineHardeningTest`, `RestrictionKeysMatchThePlatformTest`; `TestNoRestrictionCanBlockCallingOrRecovery` |
| FR-2.2 automatic network time | 5.11 | `ClockPolicyManagerTest` (8 cases over the pure manager) and `HardeningClockTest` (3) — the read-back that catches a device accepting `setAutoTimeEnabled` and staying off, an unreadable clock reported as a failure rather than assumed on, and the two facts about where it runs: a clock that will not be fixed makes the **whole baseline** not-ok even with every restriction in effect, and a sync reports `clock = null` rather than a success for something it never looked at. `ClockGatewayVersionSplitTest` reads `DpmClockGateway.kt` for the API 29/30 split, which no JVM test can execute and whose collapse is invisible — `setAutoTimeRequired` compiles everywhere and means *forbid changing it* on API 30+. **Calibrated 19/19.** Found by 5.10: the requirement was cited by nothing because nothing implemented it |
| FR-3 screen time | 5.6, 3.3 | both halves. Server: `TestQuotaIsReadForTheLocalDay`, `TestDayKeyMatchesTheDayTheResolverReads`, `TestUnknownTimezoneIsAnErrorNotAFallback`. Device (5.6): `SpanFolderTest`, `DayAttributionTest`, `ScreenOnClockTest`, `UsageLedgerTest`, `UsageTrackerTest`, `UsageReporterTest` — the monotonic ceiling (FR-3.2), the screen-off pause (FR-3.3), the cut at local midnight, and `UsageTick.NotMeasured` rather than a zero when `PACKAGE_USAGE_STATS` was never granted, which is the failure that would otherwise show a parent a child who spent the day off their phone |
| FR-4 bedtime | 5.4 | `EnforcementEngineVectorsTest` against the shared vectors; `TestSharedVectors`, `TestVectorsCoverTheEnforcementRequirements`, `TestNextChangeAtIsInTheFuture` |
| FR-5 apps | 5.4, 5.5, 3.3 | `RestrictionPlannerTest`, `AppSuspensionManagerTest`, `StateApplierTest`; `TestAppRulesSplitByAction`, `TestCriticalPackagesAreNeverSuspended`, `TestUninstalledAppsAreNotSuspended`, `TestHiddenPackagesAreAlsoSuspended`, `TestSystemAppsStayEnabledByRequest`; e2e `TestPolicyEnforcementJourney`; and its two preconditions, which fail silently rather than loudly — `ManifestAndPlatformCallsTest` *the permissions the shipped app asks for are exactly the ones it needs* (`QUERY_ALL_PACKAGES`, without which every blocked package reads "not installed") and *the install watcher is registered at runtime, not declared in the manifest* (without which a newly installed app is unrestrained until the next poll) |
| FR-6 filtering | 5.5 | `ChromePolicyManagerTest`, `DnsPolicyManagerTest`; `TestNormalizeDomainMatchesTheStore` |
| FR-7 YouTube | 5.4, 5.5 | `EnforcementEngineVectorsTest` (the YouTube set is symmetric across the vectors), `ChromePolicyManagerTest` (`ForceGoogleSafeSearch`, restricted mode strict) |
| FR-8 tracking-only | 5.4 | `TestTrackingOnlyKeepsFilteringAndHardening`, `EnforcementEngineVectorsTest` |
| FR-9 commands | 3.5, 3.6, 5.7 | both halves. Server: `TestParentLockIsStateNotACommand`; e2e `TestPushWakeUps`. Device (5.7): `CommandExecutorTest`, `CommandQueueTest`, `CommandHandlersTest`, `SirenControllerTest`, `LocationProbeTest`, `LockManagerTest` — a command is answered by executing it and not by fetching it, each ack lands as its command finishes, an ack that fails is `unacknowledged` rather than `failed`, `LOCATE_NOW` delivers the position *before* it acknowledges, a cached fix keeps its true timestamp, the siren carries its own five-minute deadline, and a phone with no PIN reports the failure rather than a keyguard dismissed by a swipe. Plus the two guards nothing runtime can reach: `CommandHandlersTest` *the handlers implement exactly the command types the server accepts*, which parses `ValidCommandTypes` out of `backend/internal/store/models.go` rather than restating it, and `ManifestAndPlatformCallsTest` *the command drain runs outside the sync lock*, which is the only place the non-reentrant-`Mutex` deadlock can be caught before the day a parent presses a button and nothing ever answers. **Calibrated 11/11**, each verdict naming the individual test method — see the record above |
| FR-10 telemetry | 3.5, 5.3 | `SynchronizerTest`, `EventStreamTest`, `SseParserTest`, `ApiClientTest`, `BackoffTest`; e2e `TestPushWakeUps`, `TestDeviceLifecycleJourney` |
| FR-11 multi-parent/device | 3.3 | e2e `TestDeviceLifecycleJourney`, `TestOneDeviceCannotActOnAnother` |
| FR-12 recovery | 5.8, 3.4 | both halves. Server: `TestRecoveryCodeRoundTrip`, `TestRecoveryCodesAreUniquePerDevice`, `TestHashTokenDiscriminates`, `TestRecoveryAlphabetIsAscii`. Device: 7 JVM suites / 63 tests over `recovery/` — `RecoveryVectorsTest` replays the 60 cases in `backend/internal/auth/recovery-vectors.json` — hand-written normalisations plus PBKDF2 digests from Python's `hashlib`, a third implementation neither half shares — so a normalisation or derivation divergence is red in CI rather than in a car park. **58 breaks, 58 red** (47 Kotlin, 11 Go); the one that stayed green on the first pass found a missing test rather than a missing guard, and is recorded under [5.8](#58-calibration--58-breaks-47-kotlin-11-go-58-red) |
| FR-13 / FR-13.1 console | 4.x | e2e `TestConsoleIsServedAndMobileReady`, `TestBrowserSignInJourney`; `TestConsoleServesEveryRoute`, `TestConsoleDeclaresTheMobileViewport`, `TestConsoleReferencesOnlyMountedAssets`, `TestConsoleHasNoInlineScriptOrStyle`, `TestConsoleRevalidatesWithETag`, `TestConsoleDoesNotSwallowUnknownPaths` |
| FR-13.2 mobile-first | 4.4, 6.5 | e2e `TestConsoleRendersOnAPhone` — the signed-out screen, the five views, the drawer and the provisioning sheet measured in a real browser at 360x800: no horizontal page scroll, no sideways-scrolling list, every touch target >= 44 px, no card wider than the viewport, 16 px inputs, the sign-in button above the fold, the header still at the top after scrolling to the end, the first card below it rather than behind it, permanent chrome under 15% of the screen, the drawer modal and closing on Escape and on navigation, the QR legible. Driven against a **seeded** family, because an empty console lays out perfectly and the overflow this catches comes from a long device name or a package id. `tests/e2e/calibrate-mobile.sh` is the executable calibration record: fourteen breaks, each required to go red *naming its own rule*, then green on restore — and it found a real defect in its subject the first time it ran (the pinned-navigation check measured the bar only at the end of a long page, where a `position: static` bar also sits at the bottom). Source-level companions: `TestConsoleDeclaresTheMobileViewport` — a missing viewport meta makes the whole mobile suite vacuous at 980 px. **Partial on one clause:** "navigation reachable one-handed" is no longer fully met — the menu opens from the top-left corner, the least reachable point on a large phone. Accepted on the owner's explicit preference for a top navigation (2026-09-03); the drawer's destinations are placed in its lower half, so only the opening tap is affected, and that placement is itself asserted (`#drawer-nav .tab` must start below 35% of the screen) and calibrated rather than left as prose |
| FR-13.3 installable, no desktop-only input | 4.4, 6.7 | e2e `TestTheConsoleInstallsToAPhone` — Chrome's own verdict over CDP (`Page.getAppManifest`, `Page.getInstallabilityErrors`, `Page.getManifestIcons`), opened by a fail-closed negative control on `about:blank` so that an empty error list cannot mean "this browser computes nothing". `TestConsoleNeedsNoDesktopOnlyInput` covers the second half — no `:hover`, `contextmenu`, `dblclick`, `accesskey` or mouse-only pointer event in any served asset — with a byte-count floor so an empty 200 cannot read as clean. **Calibrated 8/8** (four each, including one harness break per side); the manifest-route checks that used to stand alone here read back our own bytes and are not a statement about installing anything. Not proven: behaviour on a real cellular connection, and installation on any engine other than Chrome |
| FR-13.4 phone states its own condition | 5.9 | `DeviceStatusTest` — 25 cases over the pure composer, including the three the console cannot see: a policy received but never applied, a device out of contact, and a phone that cannot measure usage at all. The last is the one this row exists for: `NOT_MEASURED` is a third level, carried through `ForegroundReader.spans()` returning `null` rather than an empty list, and asserted to render as prominently as a fault rather than as a zero. `SynchronizerTest` (4 tests) pins the contact stamp to receipt and nowhere else, so the line cannot report a week-old phone as freshly synced. Three independent guards keep the device token off a screen anyone holding the phone can read — the composer's output, a source scan (`ManifestAndPlatformCallsTest` *the status block never reads the device token*), and the rendered view tree (`StatusScreenTest`). Instrumented `UsageAccessTest` revokes the real `GET_USAGE_STATS` appop, **reads the mode back from the system**, and asserts the screen says so — the appop cannot be granted by `setPermissionGrantState` and a revoked one makes `queryEvents` return nothing rather than throw, which is the silent zero this whole requirement is about. **Calibrated 38/38** (32 JVM, 6 on-device) — see the record above |
| FR-14 audit | 3.7 | e2e `TestEveryAuditedActionIsWritten` — all **21** audited actions driven over real HTTP (17 parent-side, 4 device-side), each asserted as a row naming actor type, actor id, action, target type and target *id*; nine detail keys checked so the row says *which* change was made; every row required to carry a `request_id`; and a source-scanning ratchet over `internal/httpapi/*.go` that fails when a 22nd action appears. **Calibrated 6/6** — see the record below. Also `TestRecoveryAndAudit`, which checks ten action names |
| FR-15 keeping the DPC current | 9 | three layers, and only the third can see it. JVM: `AppUpdaterTest` drives the five checks with every dependency a function, so the whole decision runs off a device. Server + e2e: `TestAPKInfoDescribesTheFileThisServerWillHandOver`, `TestAPKInfoIsNotFoundWhenTheServerHostsNoDPC`, `TestAParentCanTellThePhoneToUpdateItself`, `TestTheHeartbeatReportsWhichDPCThePhoneIsRunning`, `TestAnAPKReplacedUnderTheRunningServerIsRefused`, and `apk_test.go`'s seven over the bytes themselves. Device: **`tests/android/self-update.sh` + `TestTheServerReplacesTheDPCOnARealDevice`**, which builds the DPC twice from one tree, enrols the lower build against a real server and watches the higher one arrive — passed 2026-09-05 in 176 s, with the phone's own log as the second witness (`wake:connected: commands done=1` → `PackageManager: installation completed` → `FamilyGuardUpdate: self-update installed`) on a device whose adb had been off since the first policy applied. Its negative control is the same command again, declined as "already running". `tests/android/calibrate-update.sh` breaks each of the five checks in turn and records the refusal. **Not proven anywhere:** the update path on a phone that is not an emulator, and the `MY_PACKAGE_REPLACED` restart on an OEM build that kills background starts more aggressively than AOSP |
| FR-16 managed applications | 12 | four layers, and the one that decided the design is the device. Server: `TestAnUploadedAPKIsReadRatherThanDescribed`, `TestMultipartAndRawBodyAgree`, `TestTwoVersionsOfOneAppBothLive`, `TestTheSameFileTwiceIsNotAConflict`, `TestAPackageSignedByAnotherKeyIsRefused`, `TestWhatIsNotAnAPKIsRefusedAsSuch`, `TestTheDirectoryOnTheNodeIsAlsoASource`, `TestADeploymentWithoutAnAPKDirSaysSo`, `TestDeletingAnAppRemovesItsFileToo`, `TestAManagedAppDownloadNeedsADeviceCredential`, plus `internal/apk`'s parser tests. Policy: `TestDeclaringAnAppReachesThePhoneAsSomethingItCanFetch`, `TestAnUpgradeIsANewVersionInTheSamePolicy`, `TestWithdrawingAnAppRemovesItFromThePolicy`, `TestDeclaringSomethingTheCatalogDoesNotHaveIsRefused`, `TestTheConsoleSeesADeclarationWithNothingBehindIt`; the shared vectors carry three new cases so both engines normalise a declared set identically. JVM: `ManagedAppApplierTest` (12) and `AppUpdaterTest`'s four new cases. **Device: `ManagedInstallTest`** — the restriction matrix in [12.1](#121--which-restrictions-bind-the-device-owner-measured-on-a-phone-rather-than-argued-from-the-source), the install→upgrade→withdraw lifecycle against a real second application, and `getInstallSourceInfo` as a real filter. **Calibrated 11/11** ([12.2](#122--the-jvm-calibration-including-one-break-that-proved-a-test-binds-to-nothing)), and the record includes one assertion that binds to nothing at the JVM layer and says so. **Not proven:** any of it on hardware rather than an emulator, and the API 29 floor |
| FR-17 API keys | 12 | e2e `TestAnAPIKeyIsTheSameParent`, `TestTheTokenIsShownOnceAndNeverAgain`, `TestRevokingAKeyEndsItImmediately`, `TestAKeyCannotMintACredential`, `TestAKeyThatWasNeverIssuedIsNotDistinguishable`, `TestOnlyThePrimaryAdminMintsKeys`, `TestAKeyNeedsAName`, `TestTheAuditTrailTellsAScriptFromAPerson` — the last two of those are the ones that matter most: a key must not be able to mint a credential that outlives its own revocation, and an audit row must say a script acted rather than a person |
| NFR-1/2 auth | 2.4, 3.x | e2e `TestBrowserSignInJourney`, `TestBrowserSignInFailureModes`, `TestIDTokenIsVerifiedNotTrusted`, `TestSessionTokensAreForgeryResistant`, `TestOneDeviceCannotActOnAnother`; `TestVerifyRejects`, `TestVerifyAcceptsGenuineToken`, `TestVerifyDoesNotFetchJWKSPerToken`, `TestUnknownKidRefreshIsRateLimited`, `TestRefreshKeepsCacheOnBadDocument`, `TestSessionRejects`, `TestSessionRoundTrip`, `TestSessionIssuerRefusesWeakKey`, `TestBearerToken` |
| NFR-3 no fabricated success | 3.6, 5.3, 5.5 | `TestEveryReadFailureIsReported`; and the mutation sweeps — 5.3's 39 breaks and 5.5's 39, each one a place the code could have believed a return code instead of reading state back |
| NFR-4 persistence | 2.2 | e2e `TestStateSurvivesRestart` |
| NFR-5 no fake data | 3.x | e2e `TestAFreshSystemShowsAsEmpty` — three independent halves, because the requirement fails in three places: *structural* (the embedded migrations are scanned for `INSERT INTO`/`COPY … FROM` before a server starts, and the **served** console bytes for baked-in markup and literal UUIDs), *behavioural* (a fresh server reports only its configured family and parents, 11 collections come back as the literal `[]` rather than `null`, and every computed field of `desired-state` is non-null), and *epistemic* (a device that has never reported telemetry has `battery_level` **absent or null**, never `0` — with a negative control that a heartbeat of real zeros still reads back as zeros). Plus a two-directional growth ratchet over `internal/store/*.go`, so a 16th collection or a renamed one stops the suite. **Calibrated 11/11**, including both of the test's own fail-closed branches — see the record above. `TestDeviceLifecycleJourney` remains corroborating evidence, not the assertion |
| NFR-6 unbrickable | 2.1 concept, 5.2, 5.4 | e2e `TestFactoryResetIsNeverBlocked` (both views, over real HTTP); `TestNoRestrictionCanBlockCallingOrRecovery`, `TestEveryStateIsReversible`; `HardeningManagerTest`, `RestrictionPlannerTest`; `ManifestAndPlatformCallsTest` *nothing calls a platform API that could wipe the device or block resetting it*, which is the only place a `wipeData` or `setFactoryResetProtectionPolicy` call can be caught before the day it runs; instrumented `WipeabilityTest`, `WipeableAsFoundTest` |
| NFR-7 secrets | 1.1, 1.4, 7.3 | the `secret-scan` job — gitleaks 8.30.1, checksum-verified, over the full history |
| NFR-8 transport | 2.4, 5.3 | `TestHSTSOnlyOverTLS`, `TestLoadAcceptsCleartextOnlyOnLoopback`, `TestLocalhostIsExemptFromHTTPS`; `EnrollerTest` *a cleartext server URL is refused in a release build*, *an https URL is accepted without the cleartext exemption*; and the second mechanism — `network_security_config.xml`, which catches the https→http redirect the first one cannot see — by `ManifestAndPlatformCallsTest` *cleartext is refused by the shipping config, and the debug carve-out matches Enroller*, which also holds the debug overlay's cleartext hosts equal to `Enroller.CLEARTEXT_HOSTS` in both directions |
| NFR-10 battery | 5.3, 5.6, 5.7 | **Half proven.** `ManifestAndPlatformCallsTest` *the permissions the shipped app asks for are exactly the ones it needs* is what keeps the "no location polling" half structural rather than a promise. **5.7 turned it red on purpose and the red was answered rather than suppressed**, which is the whole reason the guard is written in both directions: `LOCATE_NOW` (REQUIREMENTS.md line 136) needs location, so `ACCESS_FINE_LOCATION`, `ACCESS_COARSE_LOCATION` and `ACCESS_BACKGROUND_LOCATION` are now declared, each with its reason in the manifest and in the whitelist — COARSE because Android 12 answers a FINE-only request with COARSE when the user picks approximate location, BACKGROUND because the command arrives while the phone is in a pocket and this app has no UI to be in front of. What the permissions do *not* buy is a poll loop: `LocationProbe` is one shot with a 30 s budget, and `AndroidLocationSource` releases the receiver in a `finally` on both API paths, so a fix that times out does not leave GNSS running. The screen-off idleness half still has no test — the connection is one held-open stream rather than a poll loop, but nothing asserts it. Owed a measurement, not a test |
| NFR-9 abuse resistance | 2.5 | e2e `TestRateLimitProtectsTheServer`, `TestOversizedBodiesAreRefusedAsTooLarge`, `TestMalformedRequestsAreRefusedWithAReason`, `TestCORSAllowsOnlyTheConfiguredOrigins`, `TestSecurityHeadersOnEveryAnswer`; `TestRateLimiter*` (7), `TestRateLimitCannotBeEscapedByAForgedHeader`, `TestBodyLimit`, `TestCORS*`, `TestSecurityHeaders`, `TestHSTSOnlyOverTLS` |
| NFR-11 deployability | 7.1, 7.3, 7.4, 7.5 | `deploy/` renders under `kubectl kustomize`, calibrated against a deliberately broken manifest; `DEPLOYMENT.md`; `tests/image/smoke.sh` — **twelve assertions, all calibrated**, registered as the `image` layer of `tests/run_all.sh`. Two of the twelve were false greens the calibration itself found (`docker top` printing "uid root, not root" as a pass; a crash under `--read-only` reported as NOT MEASURED because `docker port` says nothing about an exited container) |
| NFR-13 supported platforms | 5.11, 5.10 | `app/build.gradle.kts` sets `minSdk = 29`, and `RequirementCitationsTest` is what ties the number to the requirement. The requirement itself was **wrong** until 5.10 — it said API 26, while `setGlobalPrivateDnsModeSpecifiedHost` is API 29, so a 26–28 install would have enforced everything except FR-6.1 and left filtering silently off. Proven on the floor as of 2026-08-18: the `android-instrumented` layer runs on an API 29 emulator — 16 testcases in the provisioned pass, 1 after a real reboot — and it was the *first* run at 29 that found two defects an API 34 run had not (§7.6) |
| every requirement, both directions | 5.10 | `RequirementCitationsTest` — no id cited anywhere in the repo that `REQUIREMENTS.md` does not define (it found four — FR-13.4, cited twelve times and never written, plus three misnumbered store citations), and no requirement that nothing claims (it found eight, one of which was a genuine gap — see FR-2.2). Scans `kt kts go md xml sh py ts yaml yml sql`; a third test fails if either set is implausibly small, if any of `kt`/`go`/`xml`/`md` stops appearing among the citing files, or if a fabricated id is ever reported as defined — two empty sets compare equal, and a walk that resolved the wrong directory is the greenest result available |
| NFR-12 test integrity | 6.5, 1.4 | the calibration records in this document; `run_all.sh`'s comparison of the classes that reported against the classes *declared* in `src/test` (file names were the unit until the 5.5 sweep found that wrong in both directions); the four known-bad probes that made `actionlint`'s zero findings mean something |
