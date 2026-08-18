# FamilyGuard MDM — Implementation Plan

Executes [CONCEPT.md](CONCEPT.md) against [REQUIREMENTS.md](REQUIREMENTS.md).
Status is updated as phases land.

**Definition of done for every phase:** the code builds, its tests pass, *and each new guard has
been calibrated* — broken deliberately, observed red, restored. A phase that cannot run its tests
reports "not measured" and does not count as done.

## Where this stands

**Phases 0–7 are done and calibrated.** `tests/run_all.sh` runs six layers: a secret scan over the
full commit history, the Go control plane, the container image under the manifest's own
restrictions, the e2e suite against a real server binary / a real PostgreSQL / a real browser, the
Android unit suite, and the instrumented suite on a device-owned emulator across a real reboot.
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

- **Safe-area insets.** `env(safe-area-inset-bottom)` is 0 in headless Chrome, so the padding that
  keeps the console's tab bar clear of a home indicator is declared and unverified. A notched phone
  is the only instrument that settles it.
- **NFR-10's screen-off half.** The connection is one held-open stream rather than a poll loop, and
  location is one-shot with a 30 s budget, but nothing measures battery over a night. Owed a
  measurement, not a test.
- **API 29 itself.** `minSdk` is 29 because `setGlobalPrivateDnsModeSpecifiedHost` is; nothing here
  runs on a 29 device.
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
the project holds, so testing above it measures the wrong device.

## Phase 1 — Repository skeleton

| # | Task |
|---|---|
| 1.1 | `.gitignore`, `README.md` describing what actually exists, `SECURITY.md` |
| 1.2 | Go module `github.com/helios57/familyguard/backend`, `go.mod` with the minimum dependency set |
| 1.3 | Gradle project: root build, version catalog, wrapper 9.7.0, `app` module targeting Java 17 |
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
through the real redirect chain, and measures each of the five tabs plus the provisioning sheet:
tap-target width **and** height, elements past the viewport, sideways scrollers, input font size, the
tab bar's resting position, whether the last card can be scrolled out from under it, and the QR's
rendered size. The driver is hand-written against the CDP wire protocol — about 430 lines including
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

`tests/e2e/calibrate-mobile.sh` is that calibration, checked in: it breaks the console nine ways in
turn — the parenthesis, an over-wide card, an unintended sideways scroller, a 13 px input, a static
tab bar, a view with no bottom padding, an over-wide sheet, a 60 px QR, and 30 px buttons — and each
run must go red **naming that rule**; red for the wrong reason counts as a failure. It ends by
restoring the assets and requiring green, without which a guard that fails on everything would look
perfectly calibrated. Run 2026-08-18: **all 9 bind.**

Still **not measured**: safe-area insets. `env(safe-area-inset-bottom)` is 0 in headless Chrome, so
the padding that keeps the tab bar clear of a home indicator is asserted by nothing here; it is
declared in app.css and unverified, and a notched phone is the only instrument that would settle it.

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
**50 classes, 447 tests, 0 failures, 0 errors, 0 skipped** — counted from
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

Last re-measured **2026-08-18**, all six layers:

| Layer | Result |
|---|---|
| `backend` | PASS — build, vet, `vet -tags integration`, test, `gofmt` |
| `image` | PASS — 12 assertions, 0 failed |
| `e2e` | PASS — **24 tests**, 62.0 s, on `postgres:18.6`, the image the deployment runs ([6.11](#611--the-suite-was-proving-it-about-a-database-nobody-would-run)) |
| `android-unit` | PASS — **431 tests in 49 classes**, the 49th being `DocumentationLinksTest` ([6.12](#612--fifty-links-between-eight-documents-and-nothing-checking-one-of-them)) |
| `android-instrumented` | PASS — **8 tests provisioned, then 1 across a real `adb reboot`**, every method `src/androidTest` declares, on a freshly wiped AVD ([6.13](#613--a-device-owner-that-only-a-factory-reset-can-remove)) |
| `secret-scan` | FAIL in this checkout, and the paragraph below is why |

`secret-scan` is the one layer whose result is a property of the *repository* rather than of
the tree: it reads the full history, and in a checkout carrying the pre-rename history it reports
the fixtures and draft documents that were secret-shaped at the commits that introduced them, all of
which are absent from the tree. The same scan restricted to the working tree — `--no-git` — reports
**0 findings**, re-calibrated on 2026-08-18 by planting a well-formed AWS key (1 finding, rc=1) and
removing it again (0 findings, rc=0) — see [6.14](#614--the-calibration-probe-the-scanner-is-allowed-to-ignore)
for the probe that did *not* work and why that mattered. This is the point of §6.9: the finding
belongs to the commit, so the honest fix was never an allowlist.

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

## Phase 7 — Deployment preparation (no deploy)

| # | Task |
|---|---|
| 7.1 | Multi-stage Dockerfile, non-root, read-only root filesystem, no shell in the final image |
| 7.2 | GitHub Actions job publishing `ghcr.io/helios57/familyguard-control-plane:<semver>` on tag |
| 7.3 | `deploy/` — kustomization, namespace, postgres, control-plane, ingress on `guard.example.com`, and the Secret *contract* the deployment has to satisfy |
| 7.4 | Render it, calibrated: a deliberately broken manifest must fail the render before a clean one is believed |
| 7.5 | Deployment runbook: DNS record, secret creation, first-parent bootstrap, APK publication, device enrollment |

**Not deployed.** The manifests are published as a worked example and left unapplied.

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
| FR-13.2 mobile-first | 4.4, 6.5 | e2e `TestConsoleRendersOnAPhone` — the five views measured in a real browser at 360x800: no horizontal page scroll, no sideways-scrolling list, every touch target >= 44 px, no card wider than the viewport, 16 px inputs, the tab bar pinned and nothing hidden behind it, the QR legible. Driven against a **seeded** family, because an empty console lays out perfectly and the overflow this catches comes from a long device name or a package id. `tests/e2e/calibrate-mobile.sh` is the executable calibration record: nine breaks, each required to go red *naming its own rule*, then green on restore — and it found a real defect in its subject the first time it ran (the tab-bar check measured the bar only at the end of a long page, where a `position: static` bar also sits at the bottom). Source-level companions: `TestConsoleDeclaresTheMobileViewport` — a missing viewport meta makes the whole mobile suite vacuous at 980 px |
| FR-13.3 installable, no desktop-only input | 4.4, 6.7 | e2e `TestTheConsoleInstallsToAPhone` — Chrome's own verdict over CDP (`Page.getAppManifest`, `Page.getInstallabilityErrors`, `Page.getManifestIcons`), opened by a fail-closed negative control on `about:blank` so that an empty error list cannot mean "this browser computes nothing". `TestConsoleNeedsNoDesktopOnlyInput` covers the second half — no `:hover`, `contextmenu`, `dblclick`, `accesskey` or mouse-only pointer event in any served asset — with a byte-count floor so an empty 200 cannot read as clean. **Calibrated 8/8** (four each, including one harness break per side); the manifest-route checks that used to stand alone here read back our own bytes and are not a statement about installing anything. Not proven: behaviour on a real cellular connection, and installation on any engine other than Chrome |
| FR-13.4 phone states its own condition | 5.9 | `DeviceStatusTest` — 25 cases over the pure composer, including the three the console cannot see: a policy received but never applied, a device out of contact, and a phone that cannot measure usage at all. The last is the one this row exists for: `NOT_MEASURED` is a third level, carried through `ForegroundReader.spans()` returning `null` rather than an empty list, and asserted to render as prominently as a fault rather than as a zero. `SynchronizerTest` (4 tests) pins the contact stamp to receipt and nowhere else, so the line cannot report a week-old phone as freshly synced. Three independent guards keep the device token off a screen anyone holding the phone can read — the composer's output, a source scan (`ManifestAndPlatformCallsTest` *the status block never reads the device token*), and the rendered view tree (`StatusScreenTest`). Instrumented `UsageAccessTest` revokes the real `GET_USAGE_STATS` appop, **reads the mode back from the system**, and asserts the screen says so — the appop cannot be granted by `setPermissionGrantState` and a revoked one makes `queryEvents` return nothing rather than throw, which is the silent zero this whole requirement is about. **Calibrated 38/38** (32 JVM, 6 on-device) — see the record above |
| FR-14 audit | 3.7 | e2e `TestEveryAuditedActionIsWritten` — all **21** audited actions driven over real HTTP (17 parent-side, 4 device-side), each asserted as a row naming actor type, actor id, action, target type and target *id*; nine detail keys checked so the row says *which* change was made; every row required to carry a `request_id`; and a source-scanning ratchet over `internal/httpapi/*.go` that fails when a 22nd action appears. **Calibrated 6/6** — see the record below. Also `TestRecoveryAndAudit`, which checks ten action names |
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
