# FamilyGuard MDM — Requirements

This document says **what the system must do**. It deliberately says nothing about how; that is
[CONCEPT.md](CONCEPT.md)'s job.

It is the authority for every `FR-…` and `NFR-…` written anywhere in this repository, in both
directions: a citation that does not resolve here is a broken reference, and a requirement nothing
cites is a requirement nobody implemented. `RequirementCitationsTest` enforces both, across Kotlin,
Go, XML and Markdown — see [CONTRIBUTING.md](CONTRIBUTING.md).

It is condensed from an earlier draft that specified mechanisms rather than outcomes. The figures
and mechanism choices from that draft survive in §7 as *non-binding context*, kept because they
record what was once intended and marked because none of them was ever measured.

---

## 1. Purpose

A self-hosted parental-control system for a single family. A parent enrols an Android phone or
tablet as a fully managed device (Android Device Owner), then governs screen time, bedtime,
installed apps and web content from a web console, and can act on the device immediately in an
emergency.

Single-tenant by design: one family, several parents, several children, several devices per child.

---

## 2. Actors

| Actor | Description |
|---|---|
| **Parent** | Authenticates with Google Sign-In. Full control of the family. Roles are additive. |
| **Child** | Does not authenticate. Owns one or more managed devices. |
| **Managed device** | An Android device where the DPC is Device Owner. Authenticates as itself. |
| **Control plane** | Server holding all state; the only authority on policy. |

RBAC roles: `PRIMARY_ADMIN`, `ADMIN`, `GUARDIAN`. `PRIMARY_ADMIN` is the only role that may add
or remove parents; the others differ only in that.

---

## 3. Functional requirements

### FR-1 Enrollment (zero-touch, 6-tap QR)
- FR-1.1 A parent generates a provisioning QR code from the console for a named device belonging
  to a named child.
- FR-1.2 The QR encodes the standard Android Enterprise extras: admin component name, APK download
  location, APK checksum, `SKIP_ENCRYPTION`, `LEAVE_ALL_SYSTEM_APPS_ENABLED`, optional Wi-Fi
  SSID/password/security type, and an `ADMIN_EXTRAS_BUNDLE` carrying the server endpoint and a
  single-use enrollment token.
- FR-1.3 The APK checksum is the SHA-256 of the APK served at the download location, encoded
  URL-safe Base64 **without padding**, computed from the actual bytes served — never hardcoded.
- FR-1.4 The enrollment token is single-use and expires. The device exchanges it exactly once for a
  long-lived device credential. A second attempt to use it fails.
- FR-1.5 The device is provisioned by tapping 6 times on the Setup Wizard welcome screen and
  scanning the QR. The DPC answers `GET_PROVISIONING_MODE` with `FULLY_MANAGED_DEVICE` and applies
  the baseline policy during `ADMIN_POLICY_COMPLIANCE`.
- FR-1.6 The APK the QR points at must be downloadable, unauthenticated, over TLS.

### FR-2 Device Owner hardening
Applied at provisioning and re-applied on every boot:
- FR-2.1 User restrictions: `DISALLOW_SAFE_BOOT`, `DISALLOW_DEBUGGING_FEATURES`,
  `DISALLOW_CONFIG_DATE_TIME`, `DISALLOW_CONFIG_PRIVATE_DNS`, `DISALLOW_ADD_USER`,
  `DISALLOW_INSTALL_UNKNOWN_SOURCES`, `DISALLOW_UNINSTALL_APPS`, and `DISALLOW_INSTALL_APPS` while
  free-installation mode is off. Each one defends a requirement stated elsewhere in this document; a
  restriction that merely sounds strict is not applied. The set is computed by the policy engine and
  sent to the device — the DPC never assembles its own.
- FR-2.2 **Automatic network time is turned on and read back**, so the clock cannot be rolled back
  to defeat a quota. `DISALLOW_CONFIG_DATE_TIME` above is only half of this: it stops the setting
  being *changed*, and therefore freezes whatever state the phone was provisioned in. A device that
  arrived with automatic time off keeps a clock nobody corrects, and the restriction locks that in.
  The value is asserted at the two moments FR-2 names, and the platform's answer is read back —
  `setAutoTimeEnabled` returns void, so an OEM that ignores it is indistinguishable from one that
  complied. *This said `setAutoTimeRequired` until it was implemented; that call is deprecated at
  API 30 and means something different there — "forbid changing it", which on a phone that arrived
  with it off is a lock on the wrong position.*
- FR-2.3 **A factory reset always works, and Factory Reset Protection is not registered.**
  `DISALLOW_FACTORY_RESET` is never emitted, and the engine refuses to emit it. Wiping the phone from
  the recovery menu is the one escape hatch that depends on nothing this project ships: it survives a
  bad policy, a wrong DNS host, an expired credential and a control plane that will not answer. FRP
  is not registered either, and there is no column to hold an account for it — FRP takes a Google
  *account id*, which is not the OIDC subject and cannot be derived from it, so binding the hardware
  to a subject would leave a reset phone demanding an account nobody can sign into. That is NFR-6's
  exact failure mode. Turning either on later is a decision that arrives as a policy setting a parent
  can see and switch off, never as a constant compiled into the DPC.
- FR-2.4 Any inter-process command entry point on the device is guarded so that no third-party app
  can inject a policy change.

### FR-3 Screen time
- FR-3.1 The device measures per-package foreground time and reports it to the control plane.
- FR-3.2 Measurement uses a monotonic clock, so changing the wall clock cannot inflate or reset it.
- FR-3.3 Measurement pauses while the screen is off.
- FR-3.4 A per-child global daily limit (minutes) is enforced. Reaching it suspends non-exempt apps
  for the rest of the day; the day boundary is the device's local midnight.
- FR-3.5 The console shows today's usage per child and per app.

### FR-4 Bedtime
- FR-4.1 Per-child bedtime window with start and end time; the window may cross midnight.
- FR-4.2 Entering the window suspends non-exempt apps. **Leaving the window un-suspends them.**
  No policy state may be one-way.
- FR-4.3 Bedtime times come from policy. No schedule may be hardcoded on the device.

### FR-5 App governance
- FR-5.1 The device reports its installed-app inventory, and reports newly installed apps as they
  appear.
- FR-5.2 A parent can allow or block any individual app. Blocked apps are suspended and hidden.
- FR-5.3 **Free-installation mode** (per child): the child may install apps freely from the Play
  Store; new apps are inventoried and reported but not blocked.
- FR-5.4 When free-installation mode is off, a newly installed app not on the allow-list is
  suspended, and the parent is notified in the console.
- FR-5.5 **Critical whitelist**: dialer, SMS/messaging, contacts, emergency information, settings
  and the package installer can never be suspended or hidden by any rule, quota, bedtime, or
  command. Emergency calling must work at every moment of every policy state.

### FR-6 Content filtering
- FR-6.1 System-wide DNS filtering of adult content, ads and trackers, enforced by the Device Owner
  and not changeable on the device.
- FR-6.2 The filtering endpoint is configurable per child in the console.
- FR-6.3 Managed-browser policy: SafeSearch enforced, YouTube restricted mode enforced, and a
  URL blocklist applied to the managed browser.
- FR-6.4 A parent can add and **remove** custom blocked domains. Removal must actually restore
  access — no rule store may be append-only.
- FR-6.5 Content filtering must never be able to remove the device's ability to reach the control
  plane or place an emergency call.

### FR-7 YouTube killswitch
One toggle per child that blocks YouTube across every layer available to us:
- FR-7.1 Suspend and hide the YouTube app family (YouTube, YouTube Kids, YouTube Music, and known
  third-party clients).
- FR-7.2 Block YouTube and its media domains at the DNS layer.
- FR-7.3 Add YouTube URLs to the managed-browser blocklist.
- FR-7.4 The inverse command lifts all of the above. The toggle is symmetric.

### FR-8 Tracking-only mode
Per child. Usage is measured and reported, but no quota, bedtime, or app suspension is enforced.
Content filtering and hardening remain in effect.

### FR-9 Instant commands
The parent triggers these from the console; the device acts as soon as it receives them:

| Command | Effect |
|---|---|
| `LOCK_NOW` | Lock the keyguard immediately. |
| `UNLOCK_DEVICE` | Clear the lock so the parent can hand the device back. |
| `TRIGGER_ALARM` | Maximum-volume siren on the alarm stream, overriding Do Not Disturb and the silent switch, plus continuous vibration. |
| `STOP_ALARM` | Silence it. |
| `LOCATE_NOW` | One high-accuracy GPS fix, reported back, then location hardware released. |
| `BLOCK_YOUTUBE_ALL` / `UNBLOCK_YOUTUBE_ALL` | FR-7. |
| `SYNC_POLICY` | Re-fetch and re-apply policy now. |
| `UPDATE_APP` | Install the DPC build the control plane hosts, over the one running (FR-15). |

- FR-9.1 A command is only reported as delivered when the device has acknowledged it. A dispatch
  that reached nothing must surface as *not delivered*, never as success.
- FR-9.2 Commands issued while a device is offline are queued and delivered when it reconnects, or
  expire with a visible status.
- FR-9.3 The console shows each command's state: queued, delivered, acknowledged, failed, expired.

### FR-10 Telemetry
The device periodically reports: battery level, charging state, screen state, connectivity, OS
version, model, policy version in effect, and last-seen time. The console shows this live and marks
a device offline when it stops reporting.

### FR-11 Multi-parent, multi-device
- FR-11.1 Several parents per family, each with a Google identity and a role.
- FR-11.2 Several children per family; several devices per child.
- FR-11.3 A command may be broadcast to all of one child's devices at once.

### FR-12 Emergency recovery (anti-brick)
- FR-12.1 A device that is offline and locked down must be recoverable **on the device itself**,
  without the control plane and without a factory reset.
- FR-12.2 Recovery un-suspends every app, clears the lock, and disables enforcement until the
  device next reaches the control plane.
- FR-12.3 The recovery secret is **per device**, generated at enrollment, never shared between
  devices, and never published in documentation or source.
- FR-12.4 Recovery attempts are rate-limited with escalating lockout.
- FR-12.5 Every recovery attempt, successful or not, is reported to the control plane when the
  device next connects.

### FR-13 Parent console, and what the phone says for itself
- FR-13.1 A web UI, authenticated with Google Sign-In, covering: overview and live telemetry per
  device; child switcher; screen time; app governance; bedtime and quota; network and filtering;
  provisioning QR; instant actions; and an audit log of policy changes and commands.
- FR-13.2 **Mobile-first.** The console is primarily used from a parent's phone, so every view must
  be fully usable on a narrow screen (360 px and up) in portrait: no horizontal scrolling of the
  page, touch targets at least 44 px, navigation reachable one-handed, tables reflowing to cards,
  and the provisioning QR legible on a phone. Desktop is the widened case, not the design target.
- FR-13.3 The console must be installable to the home screen and work over a mobile connection —
  no dependency on a desktop-only input (hover, right-click, keyboard shortcuts).
- FR-13.4 **The phone states its own condition**, on the recovery screen, to whoever is holding it:
  what it measured today, which policy version it has actually applied, and when it last reached the
  control plane. The console knows only what it sent; a phone that never applied a policy, has not
  been reached in a week, or cannot see usage at all looks identical from the server. Anything the
  phone could not measure must be shown as *not measured* — never as a zero, and never less
  prominently than a fault. No secret appears on this screen: it is reachable by anyone holding the
  phone.

### FR-14 Auditability
Every policy change, command, enrollment and recovery attempt is recorded with actor, target,
timestamp and outcome, and is readable in the console.

### FR-15 Keeping the DPC current
The app on the phone *is* the enforcement, so a phone left on an old build is a gap the parent
cannot see and cannot close by hand: the device is locked down, and the child is not going to
update it. Replacing the DPC is therefore the control plane's job, and it must be doable at any
time on a device that is already enrolled and already hardened.

- FR-15.1 The control plane hosts one DPC build and states, to a device that asks, its build
  number, its size and the checksum of the bytes it will serve. The description and the bytes are
  the same artifact: a download that does not match what was announced is refused, not installed.
- FR-15.2 A parent can tell a device to install the hosted build, from the console, as an instant
  command (FR-9) with the same queued / delivered / acknowledged / failed states as every other.
- FR-15.3 The phone refuses to install anything that is not strictly newer than what it is running,
  is not the same package, is not signed by the same signer as the app already installed, or does
  not match the announced checksum — and refuses when it cannot check one of those rather than
  assuming it passed. Each refusal is reported with its reason.
- FR-15.4 The install replaces the running process, so the acknowledgement is sent before the
  install is committed and proves only that the device accepted the command. The evidence that it
  worked is the build number in the device's next telemetry report (FR-10), which the console shows
  per device.
- FR-15.5 A DPC that has replaced itself resumes enforcing on its own, without the child or the
  parent touching the phone, and without waiting for the next reboot.

---

## 4. Non-functional requirements

- **NFR-1 Authentication.** No request is served without proof of identity. Parents authenticate
  with a verified Google ID token (audience-checked against our own client ID) and are matched
  against an allow-list of permitted parent accounts. Devices authenticate with their own
  credential. There is no unauthenticated path to any state, and no fallback that grants access
  when verification fails.
- **NFR-2 Authorization.** A device may only read and write its own records. A parent may only act
  within their own family.
- **NFR-3 No fabricated success.** Any operation that cannot reach its target returns an error.
  Applies to command dispatch, policy sync and filter fetching alike.
- **NFR-4 Persistence.** All state survives a restart of every component. Nothing that a parent
  configured may live only in memory.
- **NFR-5 No fake data.** No seeded demo families, children or devices in any deployable build.
  An empty system shows as empty.
- **NFR-6 Unbrickable.** No policy, command or failure mode may leave a device unable to (a) place
  an emergency call, (b) reach the control plane, or (c) be recovered per FR-12. Any lockdown that
  depends on a component working must fail open if that component is not working.
- **NFR-7 Secrets.** No credential, token or key is committed to the repository, printed in a log,
  or embedded in a build artifact.
- **NFR-8 Transport.** All parent and device traffic is over TLS.
- **NFR-9 Abuse resistance.** Rate limiting on authentication and command endpoints, request size
  limits, and OWASP baseline security headers. Rate-limiter state must be bounded.
- **NFR-10 Battery.** Device-side background work is idle when the screen is off, and location
  hardware is used only for the duration of a single fix.
- **NFR-11 Deployability.** Deploys into a single-node Kubernetes cluster from git, on its own
  hostname and in its own namespace, sharing nothing with whatever else the cluster already runs.
  No manual `kubectl` step: anything applied by hand is undone by the next sync.
- **NFR-12 Test integrity.** Every test must be able to fail. Each control is calibrated against a
  known-bad input before it is trusted. A suite that cannot run reports *not measured*, never pass.
  Tests must cover the rejection paths (unauthenticated, wrong owner, malformed input, rate limit,
  oversized body), not only the happy paths.
- **NFR-13 Supported platforms.** Android 10 (API 29) minimum; target current stable. The device
  must be provisionable from an out-of-box or factory-reset state. *This said API 26 until the DNS
  requirement was implemented: `setGlobalPrivateDnsModeSpecifiedHost` is API 29, so on 26–28 FR-6.1
  cannot be met at all and the app would enforce everything else while silently leaving filtering
  off. Refusing to install is the honest behaviour.*

---

## 5. Constraints

- Self-hosted on hardware the family already owns; no third-party MDM SaaS.
- Deployment is GitOps-only: the cluster's state comes from a git repository, and nothing is applied
  by hand. Anything applied by hand is undone by the next sync, which is the point.
- The service adapts to whatever cluster it lands in, and no service already running there is
  modified to accommodate it. A new deployment that requires an existing one to be reconfigured has
  put a working system at risk to install an unproven one.
- Single family. Multi-tenancy is not a requirement and must not be built speculatively.

---

## 6. Non-goals

- Multi-tenant SaaS, billing, or public sign-up.
- iOS.
- TLS interception or a root CA on the device. Filtering must not break certificate pinning.
- Rooting, custom recovery, or any OEM-specific exploit.
- Covert operation: the device shows that it is managed. This is a family tool, not spyware.
- Call/SMS content interception, message reading, or microphone/camera access.
- Google Play private-channel distribution; the APK is self-hosted.

---

## 7. Non-binding context from the draft

Recorded for traceability. **None of these were measured, and none are requirements.**

- Claimed figures: sub-1.5 ms command dispatch, <0.5 %/24 h battery drain, >150 k rules/s filter
  parsing, <0.2 µs per filtering decision, 97 000+ filter rules, 16 k-slot LRU cache.
- Mechanisms the draft assumed: an in-app `VpnService` doing TLS SNI and DNS inspection with a
  reverse-domain trie; Firebase Cloud Messaging as the command channel; Redis as a cache and bus;
  PostgreSQL 18; a gRPC service definition (never implemented; the transport was REST + WebSocket).
- The draft shipped a **single master recovery PIN, the same on every device, printed in its own
  README**. That is what FR-12.3 exists to forbid, and it is worth keeping the shape of the mistake
  even though the value itself is not repeated here: a recovery secret that is shared is a recovery
  secret that leaks once and is then gone everywhere, and one written into documentation has already
  leaked. Any device provisioned by that build has to be re-provisioned; there is no migration.
