# FamilyGuard MDM

A parent-run mobile device manager for a family's Android phones. One Go binary serves the REST API,
the event stream and the parent console; one Android app runs as **Device Owner** on the child's
phone and applies the policy the server publishes.

It is not a product and not a fork of one. It exists because the household needs three things a
commercial MDM will not do: keep the data on hardware the family owns, stay small enough that one
person can read all of it, and — above all — **never be able to brick the phone it manages**.

> **Status: not deployed anywhere yet.** Every layer is built and tested; no cluster is running it.
> [`deploy/`](deploy/) holds a complete, buildable Kubernetes example, and
> [DEPLOYMENT.md](DEPLOYMENT.md) walks it end to end. See [Deployment](#deployment).

- [REQUIREMENTS.md](REQUIREMENTS.md) — what it must do, as numbered FR/NFR rows.
- [CONCEPT.md](CONCEPT.md) — how it is built, and the three architectural decisions that shape it.
- [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) — phase-by-phase status, the calibration records,
  and the traceability table from each requirement to the test that proves it.
- [SECURITY.md](SECURITY.md) — the trust boundaries, what an attacker gets from each one, what this
  system deliberately does not defend against, and how to report a vulnerability.
- [CONTRIBUTING.md](CONTRIBUTING.md) — how to run the six test layers, the calibration rule every
  guard here is held to, and the conventions that are not obvious from the code.
- [DEPLOYMENT.md](DEPLOYMENT.md) — the runbook for the first deployment, in the order the steps have
  to happen, including the ones no sync can do for itself.
- [deploy/](deploy/) — the Kubernetes manifests themselves, as a worked example: namespace,
  PostgreSQL, a verified nightly backup, control plane, Ingress, and the secret contract the rest of
  them depend on.

---

## What exists today

| Piece | Where | State |
|---|---|---|
| Control plane (Go 1.26) | `backend/` | API, event stream, policy compiler, console — running and tested |
| Parent console (vanilla JS, mobile-first) | `backend/internal/console/assets/` | served by the same binary at `/` |
| Android DPC (Kotlin, Device Owner) | `android-dpc/` | provisioning, enrollment, sync, hardening, app/Chrome/DNS policy, screen time, instant commands, the offline recovery hatch, and the status block the phone shows for itself |
| Test layers | `tests/` | secret-scan, backend, manifests, image, e2e, android-unit, android-instrumented |
| Kubernetes manifests | `deploy/` | a complete worked example — `kubectl kustomize deploy` renders it; nothing is deployed |

`IMPLEMENTATION_PLAN.md` is the authority on what is finished. It says "not measured" where a thing
has not been run, and that is deliberate — see [Test integrity](#test-integrity).

## The three decisions worth knowing before reading the code

1. **Content filtering is DNS-over-TLS set by the Device Owner, not an in-app VPN.** A `VpnService`
   with `lockdown = true` means the phone has no network whenever our code is wrong. The OS resolver
   filters instead; there is no packet path of ours to get wrong. The cost is stated honestly in
   CONCEPT.md §2.1: custom domain blocking lands at the Chrome-policy layer, not at DNS.
2. **An event is a wake-up, never a delivery.** Commands are rows in PostgreSQL with an explicit
   lifecycle. The server never reports a command succeeded because it sent one — it reports what the
   device acknowledged. A dropped SSE connection costs latency, never correctness.
3. **The phone stays recoverable.** `no_factory_reset` is in `FORBIDDEN_RESTRICTIONS` — never set,
   and actively cleared on every sync if something else set it. The physical escape hatch is a
   factory reset the parent can always perform, and no policy this system publishes can close it —
   `TestFactoryResetIsNeverBlocked` checks both views over real HTTP, the console's answer and the
   payload the phone applies, because a serialisation that dropped the strip on one side only would
   be invisible to a test that asked once.

## Building

Prerequisites: Go 1.26.6+, JDK 26, Android SDK (platform 37.1 and build-tools 37.0.0 to compile
against; an API 29 system image for the emulator the instrumented layer uses), Docker, and Chrome or
Chromium (the e2e suite lays the parent console out on a 360×800 phone and measures it; without a
browser that layer reports **2, not measured** rather than skipping quietly).

```bash
# control plane
cd backend && go build ./cmd/server

# container image (build context is backend/ on purpose — see the Dockerfile's header)
docker build -t familyguard-control-plane:dev backend/

# Android DPC — debug APK
cd android-dpc && ./gradlew :app:assembleDebug
```

The Gradle wrapper pins 9.7.0, on AGP 9.3.1 and Kotlin 2.4.10. `allWarningsAsErrors` is on, and it
is load-bearing rather than tidiness: a deprecation on a `DevicePolicyManager` call means the
platform changed a contract underneath the app, which is exactly the class of change that surfaces
as a real phone behaving differently from the emulator.

Two version numbers here are separate decisions, and only one of them reaches a phone:

- **The JDK that runs Gradle** — `JAVA_VERSION` 26, the version CI pins. It is a build-time tool, so
  it is simply the newest that was measured to work; Temurin 26.0.2 and 25.0.4 both build and test
  green. AGP documents JDK 17 as its minimum, which is a floor and not a ceiling.
- **The bytecode target** — `jvmTarget` 21, which is the number that reaches a phone. It sat at 17
  until something measured otherwise, because Java 21 class files dex without complaint and D8
  accepting them says nothing about whether the result *runs* on API 29 — a Galaxy S20, the floor
  this project holds. The instrumented layer is what settles that, and on 2026-08-18 it ran at 21
  against an API 29 emulator: both passes green, before and after a real reboot. A physical S20 is
  still the instrument this has not been put in front of.

## Running the control plane locally

The server refuses to start on an incomplete or weak configuration rather than defaulting its way
into one — a 16-byte session key is rejected, not padded.

| Variable | Required | Meaning |
|---|---|---|
| `DATABASE_URL` | yes | pgx connection string; migrations run under an advisory lock at startup |
| `SESSION_SIGNING_KEY` | yes | ≥32 bytes; signs parent session tokens |
| `OAUTH_CLIENT_ID` | yes | Google OAuth client the ID tokens must be addressed to |
| `OAUTH_CLIENT_SECRET` | yes | for the authorization-code exchange |
| `PUBLIC_URL` | yes | the externally reachable base URL; the QR payload is minted against it |
| `BOOTSTRAP_PARENT_EMAILS` | yes | the first parents, comma-separated; nobody else can sign in until a parent invites them |
| `APK_PATH` | yes | the signed DPC on disk; served at `/dpc.apk` |
| `APK_CERT_PATH` | yes | the signing certificate, hashed into the provisioning QR |
| `APK_URL` | no | overrides the download URL in the QR when the APK is served elsewhere |
| `ALLOWED_ORIGINS` | no | exact-origin CORS allowlist; empty means same-origin only |
| `TRUSTED_PROXIES` | no | who may set `X-Forwarded-For`; empty means nobody |

Three of those are secrets — `SESSION_SIGNING_KEY`, `OAUTH_CLIENT_SECRET` and the password inside
`DATABASE_URL`. Never put a real value for any of them on a command line, in a shell history file or
in a commit; in the cluster they arrive through the Secret described in
[`deploy/README.md`](deploy/README.md), and nowhere in this repository is one of them written down.
See [SECURITY.md](SECURITY.md).

## Enrolling a phone

1. Factory-reset the child's phone (or start from the setup wizard on a fresh one).
2. In the console, add the child and their device; the server mints a one-time provisioning QR.
3. Tap the setup wizard's welcome screen six times to open the QR reader, and scan it.
4. The phone downloads the APK from `PUBLIC_URL/dpc.apk`, verifies it against the certificate hash in
   the QR payload, and installs it as Device Owner.
5. The DPC enrolls with the one-time token, opens the event stream, and applies the policy.

The QR token is single-use and the server refuses a replay — that is
`TestEnrollmentCredentialsAreSingleUse` in the e2e suite, not a claim. That the QR points at an APK
this server actually serves, and that the certificate hash matches it, is
`TestTheQRPointsAtAnAPKThisServerServes`.

## Testing

```bash
tests/run_all.sh            # every layer
tests/run_all.sh --list     # the layer names
tests/run_all.sh backend e2e
```

Exit status is three-valued, and this is the most important convention in the repository:

| Status | Meaning |
|---|---|
| `0` | every layer that was asked for ran and passed |
| `1` | a layer ran and failed |
| `2` | a layer **could not be run at all** — not a pass, never reported as one |

A missing emulator, an absent Docker daemon or a Gradle project that will not configure each end in
`2` with the reason printed next to the layer. The summary always prints which layers defined the
result, because the scope of a sweep is where its blind spot lives.

The first layer is `secret-scan`: gitleaks over the **full commit history**, which is the only scan
that means anything — a value is a finding at the commit that introduced it, forever, so deleting the
file at HEAD clears nothing. It runs with `--redact`, so neither the terminal nor a CI log ever
carries a value. Without gitleaks on `PATH` it reports **2, not measured**, naming the version CI
pins; it does the same outside a git checkout, where a directory scan would read ~0 bytes and exit 0.
There is deliberately **no allowlist** — no `.gitleaksignore`, no `paths` exemption. A fingerprint is
pinned to `commit:file:rule:line` and is void the moment history is rewritten; a path exemption over
`*_test.go` clears one finding by blinding the scanner to every credential ever pasted into a test.
Where a fixture tripped the scan, the fixture stopped being secret-shaped instead — a test asserting
a token's *length* never needed a token's *entropy*. See IMPLEMENTATION_PLAN.md 6.9, which is the
record of getting that wrong first.

Individual layers:

```bash
tests/e2e/run.sh            # starts PostgreSQL in Docker, builds the real binary, drives it over HTTP
tests/e2e/calibrate-mobile.sh   # breaks the console 14 ways; each break must go red naming its rule
tests/android/instrumented.sh   # provisions a device owner and REBOOTS the device — read it first
cd android-dpc && ./gradlew :app:testDebugUnitTest --rerun
```

`E2E_CHROME` overrides which browser is used; `run.sh` otherwise takes the first of
`google-chrome`, `google-chrome-stable`, `chromium`, `chromium-browser` on `PATH` and prints the
binary and version it found, so a run's own output records what measured it.

The e2e module has no dependencies at all and cannot import the server's packages, so it cannot
check the server against the server's own constants. Its ID tokens are signed with `crypto/rsa`
rather than with the JWT library the server verifies with.

Three guards inside `android-unit` are about the repository rather than the app.
`RequirementCitationsTest` holds every `FR-…`/`NFR-…` written anywhere to REQUIREMENTS.md in both
directions; `DocumentationLinksTest` holds every link between the Markdown documents to a file and a
heading that exist; and `DocumentedVersionsTest` holds every version number the documents state to
the build file that defines it — CONCEPT.md named 34 and 35 for `targetSdk` and `compileSdk` long
after the build had moved to 37, and the `Prerequisites:` line above is the one a new contributor
installs from. All three walk
the tree from its root, which is why the Gradle line above carries `--rerun`: a Markdown-only edit
leaves the test task *up to date*, and Gradle skips a guard exactly when you change the thing it
watches.

### Test integrity

Every guard in this repository has been **calibrated**: deliberately broken, observed red, restored.
The calibration records are in `IMPLEMENTATION_PLAN.md` (for example "5.3 calibration — 39 breaks,
39 red"), and each one names the tests that caught each break.

One of those records is executable rather than written down. `tests/e2e/calibrate-mobile.sh` breaks
the parent console fourteen ways in turn — an unparsable `app.js`, a card wider than the viewport, a
sideways-scrolling list, 13 px inputs, a header that scrolls away, content hidden behind it, chrome
that eats the screen, a menu button that cannot be seen, a drawer that stays open over the page it
navigated to, a drawer whose links sit at the top of the screen instead of under the thumb, a
sign-in button below the fold, an oversized sheet, a shrunken QR, a 30 px button — and requires the
mobile test to go red *naming that rule*, then requires green once the file is
restored. It found a real defect in its own subject the first time it ran: the pinned-navigation
assertion measured the bar only at the end of a long page, where a `position: static` bar also sits
at the bottom of the viewport, so the check passed its own known-bad input. That lesson survived the
navigation moving to the top — the same trap exists at the other end of the page, so the header is
now measured after scrolling rather than before.

This is not ceremony. The dominant defect class in policy-enforcement code is a control that passes
having evaluated nothing — a guard that is defined, unit-tested and never called; an assertion whose
subject is always absent; a coverage report for a suite that cannot run. A test that has never been
seen to fail has not been shown to work, so an uncalibrated layer is deliberately left *out* of
`tests/run_all.sh` rather than reporting green.

It applies to the fixtures too, once a fixture carries logic. `LoopbackServer.stopAnswering()` is
how nine tests spell *the server cannot be reached*; it holds the port and answers nothing, because
the spelling it replaced — `close()` — made those tests depend on nothing else in the JVM binding a
port they had just released. That went wrong twice, once on CI. `LoopbackServerTest` pins all three
properties, and the calibration is the reason there are three: the obvious one, *the request fails
with an IOException*, **passes on the broken implementation** (IMPLEMENTATION_PLAN.md 6.10).

The same discipline applies on the phone. `setPackagesSuspended`, `addUserRestriction`,
`setApplicationRestrictions` and `setGlobalPrivateDnsModeSpecifiedHost` are *requests*: the platform
can accept one and not apply it. Every applier reads the state back and reports
"accepted, and the device reports otherwise" as a failure, because a console showing a configured
filter over a phone that is filtering nothing is worse than no console at all.

## Deployment

Nothing is deployed **from this repository**, and nothing here describes a particular cluster.
[`deploy/`](deploy/) holds the manifests that would deploy it — a namespace, PostgreSQL, a nightly
backup that restores what it dumps, the control plane, a Service each and an Ingress — and the
`manifests` test layer renders them on every push and asserts ten properties of the result:

```bash
kubectl kustomize deploy      # or: tests/run_all.sh manifests
```

They are written as an **example** rather than as one cluster's configuration. Every site-specific
value is `example.com`, `/srv/familyguard` or `REPLACE_ME`, so a value that still says `example`
after you adapt it is one you have not adapted. [`deploy/README.md`](deploy/README.md) lists the
five edits and the three preconditions no `apply` can satisfy for itself — DNS, the pre-owned
database directory, and the signed APK that must be on disk before the control plane will start.

The image comes from this repository's own CI: pushing a `vX.Y.Z` tag runs
[`.github/workflows/release.yml`](.github/workflows/release.yml), which re-runs every verification
layer against the tagged tree and publishes `ghcr.io/helios57/familyguard-control-plane:X.Y.Z`. The
tag is refused unless the Android app carries the same version, so the phone and the server never
report two numbers for one system.
`0.1.0` was published on 2026-08-18; a cluster pointed at a version that has *not* been tagged gets
`ImagePullBackOff` with `manifest unknown`, which is the expected state for an unreleased pin rather
than a fault.

Because this repository is public its GHCR package should be public too, so the cluster pulls
anonymously and there is no `imagePullSecrets`, no registry credential to mint, rotate or leak. GHCR
does not always inherit a repository's visibility flip, though, and a private package presents as
`ImagePullBackOff` with `unauthorized` — so this is checked rather than assumed (measured 2026-09-03:
`0.1.0` answers **200** anonymously, `latest` answers **404**, the tag this workflow never publishes):

```bash
t=$(curl -s "https://ghcr.io/token?scope=repository:helios57/familyguard-control-plane:pull" | \
    sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $t" \
  https://ghcr.io/v2/helios57/familyguard-control-plane/manifests/0.1.0   # 200 public, 401/403 private
```

A public repository is also what turns Sigstore provenance on: GitHub stores no attestation for a
user-owned *private* repository at any permission, and the refusal lands on the publish step with
the image already pushed. The workflow reads the visibility back from the API rather than assuming
it, skips the step when it cannot succeed, and states in every release summary which of the two
provenances the image carries — because a skipped step is green, and "publish succeeded" otherwise
reads as "attested".

Nothing is ever applied by hand. Whatever runs this should read it from git.

## Layout

```
backend/
  cmd/server/            the only binary
  internal/auth/         JWKS cache, RS256 ID tokens, HS256 sessions, device-token hashing
  internal/config/       env parsing that refuses rather than defaults
  internal/console/      the parent console and its assets
  internal/enforce/      the server-side half of the enforcement model
  internal/httpapi/      routes, middleware, handlers
  internal/policy/       policy model, validation, compilation to the device bundle
  internal/provisioning/ QR payloads, enrollment tokens
  internal/store/        pgx pool, embedded migrations
android-dpc/
  app/src/main/          the DPC: policy/, sync/, provisioning/, enforce/, ui/
  app/src/test/          JVM unit tests — pure Kotlin, milliseconds
  app/src/androidTest/   instrumented tests that require a device-owned phone
deploy/
  *.yaml                 the Kubernetes example, and the secret contract it depends on
tests/
  run_all.sh             the dispatcher and its three-valued status
  e2e/                   black-box suite, its own module, no dependencies
  android/               the on-device runner
  image/                 container image assertions
tools/
  gen-recovery-vectors.py  regenerates the recovery-code vectors both halves replay
.github/workflows/
  ci.yml                 five of the six layers, on every push and pull request
  release.yml            the same five again on a v* tag, then the image push
```

The decision layers — `backend/internal/policy` and `android-dpc/.../enforce/EnforcementEngine.kt` —
are two implementations of one model, reconciled by `vectors.json`, which both replay.

## Licence

MIT — see [LICENSE](LICENSE). It is a permissive licence and it carries the usual warranty
disclaimer, which is worth reading literally here: this software takes Device Owner rights on a
phone, and nobody but the person deploying it is answerable for what it does to that phone.
