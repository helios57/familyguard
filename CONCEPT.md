# FamilyGuard MDM — Concept

How the system in [REQUIREMENTS.md](REQUIREMENTS.md) is built, and — more usefully — *why* each
piece is shaped the way it is. Where a decision replaced an earlier one, the earlier one is kept
here with the reason it failed, because "we tried the obvious thing and here is what broke" is the
part that does not survive in code.

---

## 1. Shape

Three deliverables, one repository:

```
control-plane (Go)  ──REST + SSE over TLS──  android-dpc (Kotlin, Device Owner)
      │
      ├── PostgreSQL          (all state)
      └── embedded web console (served by the same binary at /)

deploy/                        (kustomize manifests for the cluster)
```

One server binary. One database. One Android app. No message broker, no push provider, no second
container for the UI.

## 2. The three decisions that shape everything

### 2.1 Content filtering is DNS-over-TLS enforced by the Device Owner — not an in-app VPN

The draft ran its own `VpnService`, intercepted every packet, and paired that with
`setAlwaysOnVpnPackage(..., lockdown = true)`. Lockdown means the kernel drops all traffic the VPN
cannot carry. If the VPN is wrong, the device has no network — and the same code blocked its own
uninstall. That is a brick, and it violates NFR-6.

Instead the DPC sets `setGlobalPrivateDnsModeSpecifiedHost` to a filtering DoT resolver and locks it
with `DISALLOW_CONFIG_PRIVATE_DNS`. The OS resolver does the filtering. There is no packet path of
ours to get wrong, nothing to keep alive, no battery cost, and no way for our bug to remove the
device's connectivity.

**What this costs, stated honestly:** a public DoT resolver filters by *its* categories. It cannot
be told "also block `example.com` for Emma". So:

| Layer | Mechanism | Covers |
|---|---|---|
| DNS | Private DNS (DoT) locked by the DO | adult content, ads, trackers — device-wide, every app |
| Apps | `setPackagesSuspended` + `setApplicationHidden` | YouTube app family, any app a parent blocks |
| Browser | Chrome managed `URLBlocklist`, SafeSearch, YouTube restricted mode | custom domains, YouTube on the web |

FR-6.4 (custom domains) and FR-7.2 (YouTube at DNS) are therefore delivered at the **browser and
app** layers, not at DNS. A child who installs a non-managed browser can reach a custom-blocked
domain that the DoT resolver does not itself block. The fix is a self-hosted resolver — AdGuard Home
or Pi-hole speaking DoT — and it is deliberately out of scope here, because reaching it means
exposing port 853, and an ingress that already terminates TLS for other services is not something
this system gets to reconfigure (§5). `dns_host` is per-child configuration, so pointing it at such
a resolver later is a config change, not a redesign.

### 2.2 The command channel is our own — and an event is a wake-up, never a delivery

The draft's dispatcher sent nothing and returned `Success: true`; the app could not have received a
push anyway (the Firebase plugin was never applied and there was no `google-services.json`). Rather
than acquire a Firebase project to feed a channel we cannot test, the server owns the channel.

Commands are **rows in the database**, not fire-and-forget calls:
`QUEUED → DELIVERED → ACKED`, or `FAILED` / `EXPIRED`. Dispatch to an offline device leaves the
command `QUEUED`. The API returns the row's actual state, so there is no code path that reports a
delivery that did not happen (NFR-3).

The push channel is **server-sent events**, not a WebSocket. Push here is one-way — the server says
"something changed", the device answers over ordinary POSTs — so a socket would buy a second
protocol, a second framing-bug surface, and a proxy that has to be configured to upgrade it. SSE is
a plain GET that stays open.

What makes that safe is the rule the whole design leans on: **a command becomes `DELIVERED` when the
device fetches it, never when an event is emitted.** An event only says there is something to fetch.
So a dropped event, a proxy that buffered, a phone in a tunnel — none of them can lose a command;
they cost latency until the next heartbeat and nothing else. Had the event itself been the delivery,
every one of those would be a silent loss reported as a success.

The tradeoff versus FCM: a held connection survives Doze less well. Mitigation is a
Device-Owner-granted battery-optimisation exemption plus a foreground service, and a 15-minute
`SYNC_POLICY` pull as a floor. If a command is undeliverable, the console says so instead of lying.
Because the channel is ours, all of it is exercisable end to end on this machine.

### 2.3 Enforcement is a pure function, applied as a diff

Every one-way-state bug in the draft (bedtime that never un-suspends, a trie that cannot forget a
domain, two managers overwriting each other's Chrome policy) has the same cause: policy applied as
a sequence of events rather than as a desired state.

```
EnforcementEngine.compute(policy, usageToday, installedApps, now)  ->  DesiredState
PolicyApplier.apply(DesiredState)   // diffs against actual, converges
```

`DesiredState` carries the complete set of suspended packages, hidden packages, and the complete
Chrome policy bundle. Leaving the bedtime window is not an event that must fire — it is simply a
different result from the same function, and the applier un-suspends because the diff says to.

`EnforcementEngine` touches no Android API, so it is a plain JVM unit test with no emulator.

## 3. Control plane

Go 1.26, Gin, `pgx` against PostgreSQL. Layout:

```
backend/
  cmd/server/main.go     startup order, maintenance sweep, graceful shutdown
  internal/config/       env parsing; refuses to start on a missing or weak secret
  internal/store/        pgx queries + embedded SQL migrations
  internal/auth/         OIDC ID-token verification, session tokens, device tokens
  internal/httpapi/      router, handlers, middleware, SSE hub (stream.go), console mount
  internal/policy/       policy validation and the desired-state compiler
  internal/enforce/      the pure resolver: (policy, usage, apps, now) -> DesiredState
  internal/provisioning/ QR payload, APK checksums
  internal/console/      go:embed static console
```

### 3.1 Authentication

Two principals, no third, and no default-allow branch anywhere.

**Parent.** An ID token is verified here, never trusted: fetch the issuer's JWKS (cached by `kid`,
TTL-bounded), check the RS256 signature, `iss`, `aud` equal to our own OAuth client ID, `exp`/`iat`,
and `email_verified`. The email must already exist in `parents` — there is no auto-provisioning. On
success we mint our own HS256 session token (24 h).

There are two ways to reach that verification and exactly one place that acts on it
(`issueSession`), because a second copy of the membership check is how one path ends up skipping it:

- `POST /api/v1/auth/google` takes an ID token from a client that already holds one.
- `GET /api/v1/auth/google/start` → provider → `GET /api/v1/auth/google/callback` is how a **browser**
  gets one. This server performs the OAuth 2.0 authorization-code exchange itself, with PKCE
  (`S256`) and a `nonce`, holding `state`/verifier/nonce in a short-lived HttpOnly `SameSite=Lax`
  cookie that is deleted at the callback whether it succeeded or not.

The redirect flow exists so the console never loads a third party's script. Google Identity Services
would be one line of HTML, and that script would run *with the console's origin*: able to read the
session token, call the API as the parent, and change what the page shows. Keeping the provider
outside the origin is also what lets the console keep `script-src 'self'` with no `unsafe-inline`.

The session token comes back in the URL **fragment**, never a query parameter — a query parameter is
written to every access log, proxy log and `Referer` between here and the browser; a fragment is
never sent to a server at all. The console consumes it and calls `history.replaceState` immediately,
so it does not sit in the address bar or the back stack.

The issuer, JWKS URL, authorization endpoint and token endpoint are all configuration, defaulting to
Google (`OIDC_ISSUER`, `OIDC_JWKS_URL`, `OAUTH_AUTH_URL`, `OAUTH_TOKEN_URL`, plus `OAUTH_CLIENT_ID`
and `OAUTH_CLIENT_SECRET`). That is what makes this verifier genuinely testable: the e2e suite runs
a local issuer and drives the real verification code with real signatures, including tokens signed
by the wrong key, for the wrong audience, from the wrong issuer, and past expiry.

`OAUTH_CLIENT_SECRET` is required rather than optional. Defaulting it to "browser sign-in off"
produces a server whose console shows a sign-in button that can never work — a deployment that looks
finished and is not.

Bootstrap: `BOOTSTRAP_PARENT_EMAILS` seeds the first `PRIMARY_ADMIN` on an empty database and does
nothing afterwards.

**Device.** The QR carries a single-use enrollment token. `POST /api/v1/enroll` exchanges it once
for a 256-bit device token; the token is stored as a SHA-256 hash, and the enrollment token is
consumed atomically so a replay gets `409`. Every route under `/api/v1/device` accepts only this
credential, and every device query is scoped by the authenticated `device_id` — a device cannot name
another device: no device route takes a device id from the request, and the one route that takes an
id at all (`POST /device/commands/{id}/ack`) passes the authenticated device alongside it, so the
`WHERE` clause matches nothing for a command belonging to someone else.

### 3.2 API

Four principals, and the group a route sits in is the whole of its authorisation — there is no route
that checks a token by hand, because that is the one that gets forgotten.

| Principal | Routes |
|---|---|
| none | `GET /healthz`, `GET /readyz`; `GET /`, `/index.html`, `/app.css`, `/app.js`, `/manifest.webmanifest`, `/icon.svg` |
| none (this *is* the authentication) | `POST /api/v1/auth/google`, `GET /api/v1/auth/google/start`, `GET /api/v1/auth/google/callback` |
| enrollment token | `POST /api/v1/enroll` |
| parent session | `GET /api/v1/me`, `/family`, `/parents`, `/audit`, `/events` (SSE); `POST`/`DELETE` `/parents/{id}` (primary admin only); `/children` and `/children/{id}` `GET POST PATCH DELETE`; `/children/{id}/policy` `GET PATCH`; `/children/{id}/app-rules` `GET PUT DELETE`; `/children/{id}/blocked-domains` `GET POST DELETE`; `POST /children/{id}/devices`; `/devices` and `/devices/{id}` `GET PATCH DELETE`; `POST /devices/{id}/provisioning`; `GET /devices/{id}/` `recovery-code`, `recovery-events`, `apps`, `usage`, `locations`, `desired-state`, `commands`; `POST /devices/{id}/commands` |
| device token | `POST /api/v1/device/` `heartbeat`, `inventory`, `usage`, `location`, `recovery-event`, `commands/{id}/ack`; `GET /api/v1/device/` `policy`, `commands`, `stream` (SSE) |

`GET /api/v1/device/commands` is the route that matters most: fetching is what marks a command
`DELIVERED`, so delivery is recorded by the act that delivers it (§2.2).

`GET /api/v1/devices/{id}/desired-state` returns what the server's own resolver computes for that
device right now. It is the console's answer to "why is this app blocked", and it is also what lets
the e2e suite compare the server's resolver against the DPC's on identical input.

Middleware, in order: request ID, recovery, security headers, body-size limit, rate limit, CORS
(exact origins from config, never `*`), then the principal guard on the route group.

The rate limiter is a bounded LRU of token buckets with idle eviction and a hard cap — the draft's
was an unbounded map keyed by client IP.

### 3.3 Event streams

Two SSE endpoints, one hub: `GET /api/v1/device/stream` for a device, `GET /api/v1/events` for the
console. Both are plain authenticated GETs that stay open, so they carry the same auth as every
other route and need no second scheme.

An event carries **identifiers, never authoritative state** — `{"type":"command.queued","device":…}`
and nothing more. The receiver reads the state back from the API, which is the only place that
records having read it. Put the payload in the event and the fetch becomes optional, and the moment
it is optional the delivery record stops matching reality.

Every stream opens with a `hello` event, so a client can distinguish "connected, nothing has
happened" from "not connected". Publishing never blocks: it happens on the request goroutine of the
parent who made the change, and a phone on a slow link must not be able to hold that request open.
Streams have an age cap and clients reconnect; on shutdown the hub closes every stream first, or
`http.Server.Shutdown` waits for connections that by design never end.

Consequently the server carries **no `WriteTimeout`** — it applies to the whole response, so any
value at all would sever every stream on a fixed schedule. `ReadHeaderTimeout` and `ReadTimeout`
still bound the request side.

### 3.4 Console

Static HTML, CSS and vanilla JavaScript, embedded with `go:embed` and served by the same binary. No
build step, no framework, no CDN — a strict CSP is servable because every asset is local, and the
container image has no writable web root, so there is no path where a running pod serves JavaScript
other than the one that was built.

Assets are mounted **route by route**, not with a catch-all. A catch-all is one line shorter, it is
the classic directory-traversal surface, and here it would also answer HTML with `200` for a
mistyped API path — so a client would parse a web page as its error body instead of reading the JSON
`404` that says what actually happened.

Each file is hashed once at startup; the ETag is that hash and `Cache-Control` is `no-cache`, so
every load revalidates and a deploy is visible immediately. Immutable caching would need hashed
filenames, and there is no build step to produce them.

The session token lives in `localStorage`, deliberately **not** in a cookie: nothing sends it
automatically, so there is no CSRF surface to defend. The cost is that XSS could read it — which is
what the CSP and the no-inline-script rule are there to prevent, and both are asserted by tests
rather than assumed.

**Mobile-first (FR-13.2).** The parent uses this from a phone. The base stylesheet targets a 360 px
portrait viewport and widens with `min-width` media queries; there are no fixed pixel widths and no
horizontal page scroll. Navigation is a bottom tab bar within thumb reach, the child switcher is a
horizontally scrollable pill row, tables reflow into stacked cards below 720 px, and every
interactive element is at least 44 px tall. Inputs are 16 px, below which iOS Safari zooms the page
on focus and leaves it zoomed. The viewport meta sets `viewport-fit=cover` and does **not** set
`user-scalable=no` or a `maximum-scale`, so a parent can still zoom in on a small label. The
provisioning QR renders at the full width of a phone screen, and a web app manifest makes the
console installable to the home screen.

### 3.5 Data model

`families`, `parents`, `children`, `devices`, `device_state`, `policies`, `app_rules`,
`blocked_domains`, `installed_apps`, `usage_samples`, `commands`, `locations`, `recovery_events`,
`audit_log`. Migrations are embedded in the binary and applied at startup inside a transaction with
an advisory lock.

No Redis: nothing needed a cache or a bus once commands became database rows.

## 4. Android DPC

Kotlin on Java 17, `minSdk 29`, `targetSdk 34`, `compileSdk 35`, package `io.github.helios57.familyguard`.

minSdk is 29 rather than the 26 this section first named, because
`DevicePolicyManager.setGlobalPrivateDnsModeSpecifiedHost` — the whole of FR-6.1 — arrived in API 29.
On 26–28 the call does not exist, so the app would enforce every other control while silently
leaving DNS filtering off. Refusing to install there is the honest behaviour; a version check that
logs and carries on is a device that looks protected and is not.

```
admin/          AdminReceiver, AdminService (OEM-killer immunity)
provisioning/   GetProvisioningModeActivity, PolicyComplianceActivity
enroll/         EnrollmentManager — token exchange, credential storage
net/            ControlPlaneClient (REST), CommandStream (SSE + backoff), ConnectionService (FGS)
policy/         PolicyStore, PolicyModel, PolicyApplier,
                HardeningManager, AppSuspensionManager, ChromePolicyManager, DnsPolicyManager
usage/          UsageTracker (monotonic), UsageService
enforce/        EnforcementEngine  ← pure, no Android imports
command/        CommandExecutor
alarm/          AlarmService          location/ LocationOnceFetcher
recovery/       RecoveryManager, RecoveryActivity
ui/             StatusActivity
```

Credentials and the recovery hash live in `EncryptedSharedPreferences`. The only exported
components are the ones Android requires to be exported (admin receiver, admin service,
provisioning activities, launcher activity), each guarded by `BIND_DEVICE_ADMIN` where applicable;
the boot and package receivers are internal.

**Critical whitelist** is a constant in `EnforcementEngine`, subtracted from every suspension set
before it is returned. No caller can suspend the dialer, and the unit test proves it by asking for
exactly that.

**Recovery.** At enrollment the server generates a per-device recovery code, shows it to the parent
in the console, and delivers PBKDF2 salt + iterations + hash to the device in the policy bundle.
Verification is local, so it works with no network. Failed attempts escalate a persisted lockout.
Every attempt is queued and reported when the device reconnects.

## 5. Deployment

[`deploy/`](deploy/) is a kustomization that renders the whole system: a `Namespace`, a pinned
Postgres with `strategy: Recreate` on a `hostPath` under `/srv/familyguard/`, the control plane, and
an `Ingress` on **`guard.example.com`** terminated by cert-manager. It is a complete worked example
rather than one site's configuration — `kubectl kustomize deploy` renders it as it stands, and the
five values that are actually site-specific are listed in [`deploy/README.md`](deploy/README.md).

The one thing `deploy/` deliberately does not contain is the Secret. It *names* it —
`familyguard-secret`, five keys, spelled out in `deploy/secret.example.yaml` — and leaves how it is
populated to the deployment: External Secrets, Sealed Secrets, SOPS and an operator reading a secret
manager all satisfy the same contract. A repository that shipped a real Secret would be a repository
that had leaked one.

The control-plane image is built by GitHub Actions and published to GHCR under an immutable semver
tag; `:latest` is never used and never pinned against. `strategy: Recreate` on both Deployments is
not a default — the database owns a `hostPath` that two writers must never share, and the control
plane holds server-sent-event streams whose per-device ordering a second replica would silently
interleave. Neither failure is visible in a rollout that reports success.

The deployment adapts to the cluster it lands on, never the other way round: nothing in `deploy/`
edits an ingress controller, a cert-manager issuer, a GitOps controller or any workload that was
already there (NFR-11).

**Configuration.** Every setting is an environment variable, validated at startup, and the server
refuses to start on a bad one — reporting *all* the problems at once, because fixing a deployment
one restart per mistake is how a misconfiguration survives a maintenance window.

| Required | |
|---|---|
| `DATABASE_URL` | PostgreSQL DSN |
| `OAUTH_CLIENT_ID` / `OAUTH_CLIENT_SECRET` | the Google OAuth client; the secret is required, because defaulting it to "browser sign-in off" yields a console with a button that can never work |
| `SESSION_SIGNING_KEY` | ≥ 32 bytes; short keys are rejected, not padded |
| `PUBLIC_URL` | absolute; the OAuth redirect and the QR payload are built from it |

| Optional | Default | |
|---|---|---|
| `OIDC_ISSUER`, `OIDC_JWKS_URL`, `OAUTH_AUTH_URL`, `OAUTH_TOKEN_URL` | Google | overridden by the e2e suite to point at a local issuer, which is what makes the verifier testable |
| `BOOTSTRAP_PARENT_EMAILS` | — | seeded once; with no parents in the database and this empty, the server refuses to start rather than run an instance nobody can sign in to |
| `APK_URL`, `APK_PATH`, `APK_CERT_PATH` | — | see below |
| `ALLOWED_ORIGINS` | same-origin only | exact origins; `*` is rejected |
| `TRUSTED_PROXIES` | none | client IPs are not believed without it |
| `RATE_LIMIT_PER_MINUTE`, `MAX_BODY_BYTES` | 120, 1 MiB | |
| `AUDIT_RETENTION_DAYS`, `LOCATION_RETENTION_DAYS` | 365, 30 | a child's location history is the most sensitive thing here, so it expires soon and by default; the audit log is the record of what the adults did and is kept for a year. Zero is rejected — it would mean "delete everything older than now" |
| `FAMILY_NAME`, `ADDR`, `LOG_LEVEL`, `DPC_COMPONENT` | | |

The provisioning QR carries two checksums, both computed from bytes at startup, never typed in:
`SignatureChecksum` is the SHA-256 of the APK's **signing certificate** (`APK_CERT_PATH`) — what
Android 7+ actually verifies, and what survives a rebuild — and `PackageChecksum` is the SHA-256 of
the APK file (`APK_PATH`). Setting `APK_URL` without at least one of them is rejected: it produces a
server that starts, looks configured, and fails the one action provisioning depends on. Both paths
are `stat`ed at startup, because a path typo that first surfaces with a parent standing over a
factory-reset phone is the worst possible time to find it.

A maintenance sweep runs at startup and on a ticker: expire overdue commands, prune audit rows and
prune locations past their retention. It **logs the row counts** — "maintenance ran" with no numbers
is indistinguishable from a sweep whose predicate matched nothing because it was wrong.

## 6. Testing

The rule for this repository: **a test that has never failed has not been shown to work.** Every
guard below is calibrated by breaking it, observing the red, and restoring it.

- **Go unit tests** — enforcement/policy compilation, bedtime windows across midnight, quota
  arithmetic, QR payload and checksum, JWKS verification, token hashing, rate-limiter bounds.
- **Go end-to-end** (`tests/e2e/run.sh`): starts PostgreSQL in Docker, a local OIDC issuer, and the
  real server binary, then drives it as a black box over HTTP. Every journey from §3 of the
  requirements, and for each one a negative control that must be rejected — unauthenticated, bad
  signature, wrong audience, wrong issuer, expired, non-allowlisted parent, replayed enrollment
  token, device reading another device's data, a parent token on a device route, oversized body,
  rate limit, an SSE stream opened without a token. Exit `0` pass, `1` fail, `2` **not measured**.
- **Console guards** — the CSP contract is enforced from both sides: the header, and page-side
  assertions that there is no inline `<script>` body, no `on*=` handler and no `style=` attribute.
  Under `script-src 'self'` those fail *silently* in a browser, so a button whose handler never runs
  looks exactly like a button whose handler has a bug. The viewport meta is asserted for the same
  reason: without it the page lays out at 980 px and scales down, every 44 px tap target becomes
  about 16 px, and nothing about the result looks broken.
- **Android JVM unit tests** — `EnforcementEngine` across every policy combination including
  leaving the bedtime window, the critical whitelist, tracking-only mode, quota rollover; the
  Chrome bundle composition; recovery hashing and lockout; reconnect backoff.
- **Android instrumented tests** — on an emulator promoted to Device Owner: provisioning
  callbacks, suspension and un-suspension round-trip, DNS policy, command execution.
- **Container image assertions** — twelve properties of the built image, checked by running it
  under the restrictions the manifest imposes: non-root, read-only root filesystem, no shell, no
  package manager, a pinned base digest.
- **A secret scan over the full history**, first because it is the only finding that cannot be
  undone after a push. There is no allowlist; where a fixture tripped it, the fixture stopped being
  secret-shaped.
- **Two guards over the repository itself**, riding in the Android JVM layer because they are
  language-agnostic and that layer already scans from the root: every `FR-…`/`NFR-…` written
  anywhere resolves to [REQUIREMENTS.md](REQUIREMENTS.md) and every requirement is claimed by
  something, and every link between these documents points at a file and a heading that exist. Both
  failure modes are silent — a citation to a number nobody wrote reads like a correct one, and a
  dead anchor lands the reader at the top of the page — which is what makes them worth a guard
  rather than a review habit.

`tests/run_all.sh` runs all six and reports `2` for any layer it could not execute, rather than
omitting it from the count. [CONTRIBUTING.md](CONTRIBUTING.md) is the operating manual for it.

## 7. What this concept deliberately does not build

Multi-tenancy, iOS, TLS interception, a self-hosted DNS resolver, FCM, Redis, gRPC, horizontal
autoscaling, and the draft's performance claims. Each is either a non-goal in the requirements or
buys nothing for one family on one node.
