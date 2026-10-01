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

RBAC roles: `PRIMARY_ADMIN`, `ADMIN`, `GUARDIAN` — what each may do is FR-20.

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
- FR-1.7 Issuing a new provisioning QR for a device that is **already enrolled** revokes that
  phone the instant the code exists: it stops reporting, and nothing done from the console brings
  it back — the way back is FR-1.8, which needs the phone in someone's hand, and on a build without
  that screen it is still a factory reset. The request must therefore carry an explicit
  acknowledgement that the phone is being replaced; without one the server refuses and leaves the
  credential intact. Asking for a QR must never be a way to lose a working phone by accident. The
  console states the cost in the parent's own words before it sends the acknowledgement, and shows
  the minted code in type-able form next to the QR, because a phone that is already a device owner
  has no welcome screen left to scan one with.
- FR-1.8 A phone whose credential the server no longer accepts must be able to re-link **on the
  phone**, with a setup code the parent generates in the console, without a factory reset. The
  phone learns it has been unlinked from a `401` — and only from a `401`, because every other
  non-retryable status is a fault in one request rather than a statement about the credential —
  and says so in an ongoing notification. **The re-link field itself is offered on the recovery
  screen whenever the phone holds a credential, gated on nothing else** — the `401` flag explains
  why re-linking is needed and cannot say whether it is possible, so a phone revoked while it was
  switched off, or one whose service has not managed to run since, would otherwise show a parent
  nothing at all. Nothing is spent by offering it: a code the server did not mint is refused at the
  server. Re-linking is the deliberate
  exception to FR-1.4's once-only exchange, and it is bounded by three things: the server address
  comes from the stored credential and never from what was typed, so a code cannot move the phone
  to another control plane; it needs a code the server minted, so a phone a parent deliberately
  cut off stays cut off; and it replaces the credential and the recovery material as one unit, so
  the recovery code the console shows afterwards is the one that works.

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
- FR-3.5 The console shows usage per child and per app, for a day the parent chooses. Today is where
  it opens and there is no way to step past it — a day that cannot have happened yet renders as an
  empty card, which is indistinguishable from a phone that stopped reporting.
- FR-3.6 Screen time depends on an access grant no code on the device can give itself
  (`PACKAGE_USAGE_STATS` is an appop, not a runtime permission). Where it is missing, every query
  returns nothing and every app reads zero minutes — which is indistinguishable from a child who
  did not use their phone. The device reports whether it holds the grant, the console says so
  wherever it shows a number that depends on it, and the phone offers the setting in one tap.
  Unmeasurable is never presented as measured. **The grant is noticed when it happens**, not at the
  next sync: the phone watches the appop, so the warning clears and the first measurement is
  reported while the parent is still holding the phone. A notice that survives the act it asked for
  is read as the app being broken, and on a phone that cannot reach the server there is no next sync
  to correct it.
- FR-3.7 The device records **what ran when**, not only how long: each sitting is kept as an interval
  — package, start, end — and the console draws a child's day from it as well as from the totals.
  The two answer different questions and neither substitutes for the other. "Ninety minutes of
  YouTube" says nothing about whether that was one afternoon or a phone picked up thirty times, and
  a parent who wants to know what their child was doing at nine o'clock cannot read it off a total
  at all.

  Three properties make the record trustworthy, and each is a way it would otherwise be quietly
  wrong:

  - **A sitting is stored whole, never split at a midnight.** Splitting means deciding *whose*
    midnight, and the phone's current zone and the policy's can differ and can change between the
    measurement and the send. The interval is kept as the platform timestamped it and the overlap
    question is asked at read time, with the child's timezone.
  - **A sitting carries its TRUE start**, even when the poll that reports it opened hours later. The
    day totals deliberately want the opposite — the part that fell inside the window being credited,
    or a session would be counted again on every poll it survives — so the record is kept separately
    from them rather than derived from them. Without this a two-hour film reads as twenty-four
    five-minute sittings, which is indistinguishable from a child who opened it twenty-four times.
  - **A sitting is an event, so it is delivered with an acknowledgement.** Day totals are cumulative
    and the server merges them with `GREATEST`, so a report that never arrives is repaired by the
    next one; a sitting that is dropped before the server has it is simply gone. The phone keeps a
    durable queue and forgets only what the server has said it holds, and a re-delivered sitting
    cannot shorten the one already stored.

  **What the console draws from it is a chart of the day, hour by hour, with a table of per-app
  totals under it** — one column per hour of the child's own day, each column showing how much of
  that hour the screen was on. The sitting is the record; the chart is the summary a parent reads.
  Three properties are part of the requirement rather than of the drawing:

  - **Each hour is the sittings intersected with it**, not the sittings filed under the hour they
    began in. A sitting is stored whole, so "how long was the screen on between eight and nine" is
    an overlap question at every level, not only at the day boundary.
  - **A full column is a full hour**, fixed, never scaled to the busiest hour of the day. A scaled
    axis makes twenty minutes and four hours draw identically on their own days, so two days cannot
    be compared — which is the only thing the chart is for.
  - **Every hour of the day is answered, including the empty ones**, and the day has 23 or 25 hours
    on the two mornings the clocks move. A chart that omits its quiet hours looks like one that
    failed to load.

  Each app carries its **number as text and a bar** (FR-3.9): the number is what a parent acts on when
  deciding whether to set a limit, and the bar — drawn against its limit — is what shows how far along
  it is. Apps used for under a minute are counted rather than listed — they all round to "0 min" — but
  the count is shown, because dropping them silently would understate the day.

  The chart and the table are **different measurements**, and the console says which one is missing
  rather than blending them: the chart comes from sittings, which exist only from the build that
  records them onward, and the table from the cumulative day totals every reporting build has sent.

  The console distinguishes **"no sittings in this day"** from **"this phone has never reported
  one"**. They look identical on screen and have opposite remedies — the first is a child who did
  not use their phone, the second is a device that is not reporting.
- FR-3.8 **Only use counts.** Foreground time on the phone's home screen, on System UI (the shade,
  the lock screen, recents) and in FamilyGuard itself is shown but not counted toward any limit. The
  phone reports which app is its home screen on every heartbeat, because a child can change it, and
  the phone's own offline count leaves out the same packages the server does. Measured 2026-09-23:
  the family phone lay on its charger with "Stay awake" on and spent its whole 60-minute limit —
  57 of the 120 minutes counted were the home screen being shown, with nobody holding the phone.
- FR-3.9 The console draws **each app's use against its limit**, on the day the parent is looking
  at: a bar per app with the app's own limit marked on it, the day's counted total against the daily
  limit (including any extra time, FR-3.11), and the uncounted time as its own line. The limit shown
  for a past day is the limit **that applied that day**, recorded while it was current — never
  today's limit drawn over last week. A day from before limits were recorded says so.
- FR-3.10 **Why an app cannot be used is said where it is noticed.** On the phone: Android's own
  "blocked by your administrator" screen carries one line naming the state (daily limit reached, and
  how much of it; bedtime and until when; or blocked/limit/approval, see FamilyGuard); a
  notification says so for as long as a daily limit or bedtime pauses apps; and FamilyGuard's own
  screen lists today's apps, each with its minutes, its limit and its reason. In the console: every
  app's line says what governs it (always free, counts, own limit, blocked, waiting for approval,
  not counted) and, today, whether and why it is paused now. Both derive the reason from the same
  engine output the phone enforces, with the same names. The child reads it in the phone's
  language; German is provided.
- FR-3.11 **Extra time for today.** A parent can add minutes to a child's daily limit for the
  current day only, from the console (+15, +30, +60) or the API. The extra time belongs to the
  child's calendar day in the policy's timezone and ends at its midnight even on a phone that is
  offline and cannot be told. Grants on one day add up, to at most a day's 1 440 minutes; with no
  daily limit there is nothing to add to, and the request says so. Every grant is audited.
- FR-3.12 **Bedtime and the daily limit pause what a child can open**, and nothing else: the phone
  reports which apps have a launcher entry, and an app that has none — the hundred system services
  every phone carries, an emergency handler, a sync agent, a keyboard — is left running. A parent's
  explicit block, an app's own limit and an app waiting for approval are unaffected: those are
  decisions about one app. A phone that does not report the flag keeps the previous behaviour.
  Measured 2026-09-23: at the daily limit the family phone tried to pause 101 apps, among them
  `com.samsung.android.emergency`; the platform refused most of them, on every sync.

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
- FR-5.8 **A parent has four answers for an app, not two.** Until 2026-09-20 there were two,
  ALLOW and BLOCK, and ALLOW is the FR-5.5 whitelist — an allowed app is outside bedtime and
  outside the daily limit, permanently. Combined with FR-5.4 that left no way to say the ordinary
  thing: a parent who merely wanted to approve an app had to exempt it from every schedule, and
  the only alternative was to leave it suspended. Measured on family hardware that day: WhatsApp,
  Threema, Signal and Audible all sat suspended awaiting approval for exactly this reason, and
  turning bedtime off changed nothing because bedtime was never what held them. The four:
  - **always free** (ALLOW) — exempt from bedtime and from the daily limit;
  - **daily limit** (LIMIT, no allowance) — approved, and governed like every other app: it counts
    against the daily limit and it pauses at bedtime;
  - **individual limit** (LIMIT with an allowance) — as above, plus a daily allowance of its own,
    spent independently of the shared one, so an app can run out while screen time remains;
  - **always blocked** (BLOCK) — suspended and hidden.
  No rule at all remains a fifth state, "undecided", and is what keeps an app in FR-5.4's queue.
  A per-app allowance binds at the next evaluation rather than to the second, and it never sets a
  suspend reason: the phone is not in a quota state, one app is.
- FR-5.9 **Some apps are never suspended, whatever any rule says.** FR-5.5 covers the packages the
  *phone* needs to keep working. This covers the ones the *family* decided reaching each other does
  not depend on a schedule: the messengers in use (WhatsApp, Threema, Signal) and Audible. They
  survive bedtime, a spent quota, the family blocklist, an explicit BLOCK and FR-5.4's approval
  hold. The list is carried by the server as well as compiled into the DPC, and travels to the
  device in the policy input, so a change to it takes effect on the next sync rather than on the
  next app update. Two consequences are deliberate: bedtime does not reach these apps, and a BLOCK
  rule against one does nothing — the same way a BLOCK against the dialer already does nothing.
  A third set is not ours to choose: Android refuses to let any device owner suspend the packages it
  protects — on phones with Google Play the Play Store (the package verifier), and device-admin apps
  such as Google's Android Device Policy. The phone names them (`KEPT-BY-ANDROID`) and does not count
  the refusal as a failure (since 0.6.30); a refusal to *release* an app still is one.
- FR-5.10 **Preinstalled apps a child can open are always free by default.** Camera, Gallery, Clock,
  Calculator, Contacts and the like are part of the phone: with no rule of the parent's, they stay
  usable at bedtime and when the daily limit is spent. The exceptions are the preinstalled apps that
  ARE screen time — browsers, app stores, search, AI assistants and video (Chrome, Samsung Internet,
  Play Store, Galaxy Store, Google, Gemini, Bixby, YouTube) — which count toward the daily limit like
  any installed app. That list is carried by the server and travels to the phone in its policy, so it
  can change without a phone update. Any rule the parent sets wins: a preinstalled app can be
  blocked, limited or put on the daily limit like any other. Only apps with a launcher entry are
  concerned (FR-3.12); a phone that does not report it keeps the previous behaviour. The console and
  the phone both say "always free (preinstalled)" rather than showing no category. Chosen by the
  owner on 2026-09-23 after Camera and Gallery were paused by the daily limit and could not be found
  in the Apps list.
- FR-5.11 **An app no phone reports any more can be removed from the Apps list.** Only then: an
  installed app cannot be removed from the list, because the list is what the phone reports. The
  parent's rule for it is kept — a blocked game that comes back is still blocked — and the removal
  is audited.
- FR-5.6 **Developer options and adb are a per-child switch**, defaulting to off (the restriction
  applied). `no_debugging_features` is the one restriction whose cost falls on whoever administers
  the phone rather than on the child: applying it as Device Owner switches adb off, the setting
  outlives every reboot, and it cannot be undone from outside the device — so on a phone that has
  also stopped reaching the control plane it removes the last way in. It stays the default because
  adb un-suspends anything; the switch exists so the phone somebody is developing against can
  decline it, visibly, from the console, and have it back on the next sync.
- FR-5.7 **Uninstalling apps is a per-child switch**, defaulting to off (the restriction applied).
  `no_uninstall_apps` is set on the user, and the Device Owner runs as that user, so it binds
  whoever administers the phone as much as it binds the child: `adb uninstall` answers
  `DELETE_FAILED_USER_RESTRICTED`. Without a switch, putting an app on a governed phone is one-way
  — restoring a backup from an older handset, or undoing an install that went wrong, has no remedy
  short of a factory reset. It stays the default because removing an app is how a child escapes a
  suspension; the switch exists so a parent can open the hatch for as long as they need it and
  close it again from the console. The pre-sync boot floor (FR-2.1) is deliberately **not** covered:
  a phone rebooted while the switch is on comes back restricted until its next sync, because that
  floor runs before any policy is known.

### FR-6 Content filtering
- FR-6.1 System-wide DNS filtering, enforced by the Device Owner, **when a resolver is configured**,
  and then not changeable on the device. **No resolver is configured by default** (2026-09-06): the
  product shipped defaulting to a third-party filtering resolver, which is a filtering choice nobody
  made and does not do the job its name suggests — a DoT resolver sees names, so it cannot remove
  advertising an app fetches over its own connection to its own backend. With no resolver set the
  phone uses opportunistic encrypted DNS, and `disallow_config_private_dns` is **not** applied,
  because there is then no policy for it to protect. In-app advertising is not addressed at this
  layer at all and cannot be — FR-6.6 to FR-6.9 are what address it.
- FR-6.2 The filtering endpoint is configurable per child in the console.
- FR-6.3 Managed-browser policy: SafeSearch enforced, YouTube restricted mode enforced, and a
  URL blocklist applied to the managed browser.
- FR-6.4 A parent can add and **remove** custom blocked domains. Removal must actually restore
  access — no rule store may be append-only.
- FR-6.5 Content filtering must never be able to remove the device's ability to reach the control
  plane or place an emergency call.
- FR-6.6 Advertising and tracker filtering happens **on the device**, from filter lists it fetches
  and compiles itself — never by pointing the phone at somebody else's filtering resolver. A
  resolver only ever sees names, so it cannot touch advertising an app fetches over its own
  connection to its own backend, which is most of what a game shows a child. Lists are held as a
  URL and a hash; this project ships the fetcher and never the list data, which is licensed
  separately from this code.
- FR-6.7 The filter runs as a `VpnService` that **never uses lockdown mode** and fails open in every
  direction: anything it cannot parse, name or decide is carried. A tunnel that is up and carrying
  nothing is worse than no tunnel — the phone has no internet, nothing on the screen says why, and
  the child can report it only as *"the internet is weird"*. Switching the filter off must never
  need more than a sync, which is why FR-6.5's host can never be filtered whatever a list says.
- FR-6.8 Connections are **terminated locally** so the name inside one can be read: the server name
  from a TLS ClientHello, or the `Host:` header of a plain request. This is what reaches the
  advertising FR-6.1 structurally cannot — an SDK with a hardcoded address, or one resolving its own
  names over DoH inside its own session, never asks the resolver and is invisible to every
  DNS-based filter. A blocked connection is **reset**, never dropped: a reset is an error every
  client already handles, where a drop is a socket that hangs until its own timeout.
- FR-6.9 QUIC (UDP/443) is dropped, so a client falls back to TLS over TCP within a few hundred
  milliseconds and the name arrives in clear text. This is the one deliberate breakage in the
  feature, and it is the price of covering the advertising libraries that reach for QUIC first. It
  costs the fallback delay on the first connection to a host, once.
- FR-6.10 A parent can switch the filter on and set its list **from the console**, and the console
  shows what the **phone measured** rather than what was asked for: whether a tunnel is actually up,
  how many rules it loaded, and when it last fetched the list. The two are separate questions and
  can disagree — a switch that is on and a tunnel that never came up is the state a parent has to be
  able to see, and the one a console that echoes the setting back would hide. Every one of the three
  is three-valued: *not reported* is a phone that has not said, never a phone with the filter off,
  because the Play build carries no filter to report on at all (FR-15.8). A list URL must be
  `https://`; anything else is refused by the console and by the phone, since whatever can rewrite a
  plain-HTTP list decides what the phone refuses to connect to — including, once, this control plane.

- FR-6.11 When the filter is switched on and no tunnel is running, the console says **why**, in the
  phone's own words. A tunnel stands down for reasons a parent can act on — the list has not been
  fetched, the phone has not allowed the connection, the network named no resolver to forward to —
  and each one has a different remedy. Reported on the heartbeat like every other measurement, so
  it is the phone's answer rather than the server's guess, and blank whenever a tunnel is up: a
  reason left standing after the thing it explained is over is how a console teaches a parent to
  ignore it. Measured 2026-09-20: the filter was on with 180423 rules compiled and the tunnel had
  been down for hours, the phone knew exactly why, and it said so only in its own notification
  shade — the console showed a switch that was on and a tunnel that was not, with no way to learn
  which of three unrelated faults it was.

  **And never blank while no tunnel is running**, which is the half the first attempt missed. The
  phone that was measured had recorded nothing at all: its service had never run, so there were no
  words to send, and "nothing recorded" and "nothing to explain" were the same empty string on the
  wire. They have opposite remedies. A filter that was asked for and never started now says so, as
  do the four cases that reach no running service — the platform refusing the foreground start, the
  platform refusing this app as the always-on connection, a revoked connection, and a forwarder that
  would not bind.

  **And "starting" is its own answer.** The phone reports on the same sync that re-applies the
  policy, and a report sent while the tunnel is being built used to carry the empty reason the last
  run left behind — which the console can only show as "has not started on this phone". Measured
  2026-09-23: the family phone showed "Ad filter on" in its own shade while the console said the
  filter had never started. A tunnel being built now says it is starting.
- FR-6.12 A sync that leaves the tunnel's plan unchanged — the same route, the same resolvers —
  leaves the running tunnel alone. Every sync re-applies the whole policy, and rebuilding the tunnel
  each time closes every connection it is carrying: every five minutes with the screen on, and on
  every change a parent makes anywhere in the policy. A new filter list does not need a new tunnel;
  its rules replace the old ones inside the running filter.
- FR-6.13 The filter delivers every byte a destination sends, in order, however far the app falls
  behind. A destination is read faster than an app reads its socket; what the app's window has no
  room for yet waits and is sent as the app acknowledges, the read from the destination pauses
  while the app is that far behind, and a destination that finishes is ended after its last byte,
  never before. Measured 2026-09-23: without it, every download larger than a window or two lost
  its middle — Jellyfin's 650 KB scripts arrived as 172 KB and the app showed a black screen, and a
  4 MiB download through a real kernel tunnel arrived as exactly 65 536 bytes.

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
- FR-12.6 A boot must not undo a recovery. The boot-time baseline of FR-2 is a floor for a *managed*
  device; on a released one it is applied with an empty required set, so it adds nothing and still
  clears the forbidden set FR-2.3 names. Without this the release ends at the next reboot with
  nothing said — and it ends precisely when the sync that would legitimately end it is the thing
  that cannot happen, because a release is only ever used when the control plane is out of reach.

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
- FR-13.5 **One language: German**, Swiss spelling (ss, never ß), 24-hour times — the console and
  the phone alike, including the phone's screens for the parent holding it (status, recovery,
  re-linking). The console names a phone's controls by the words the phone shows. Built in 0.6.33.
- FR-13.6 **What a parent does most is one or two taps from opening the console.** Übersicht shows
  every child: the state in one word (*Frei*, *Limit erreicht*, *Schlafenszeit*, *Pausiert*), today's
  time against the limit with the Bonuszeit, **+15 min**, **Pausieren** and **Live** as its buttons,
  the tasks waiting, and one row per phone; a phone's row opens its sheet with Orten, Klingeln and
  Bildschirm sperren first. Apps waiting for a decision offer Erlauben and Sperren on the row. Forms
  that are documents (plan, alarm week, agenda) save through one bar that appears only while
  something is unsaved, and leaving with unsaved changes asks. Built in 0.6.33.
- FR-13.4 **The phone states its own condition**, on the recovery screen, to whoever is holding it:
  what it measured today, which policy version it has actually applied, and when it last reached the
  control plane. The console knows only what it sent; a phone that never applied a policy, has not
  been reached in a week, or cannot see usage at all looks identical from the server. Anything the
  phone could not measure must be shown as *not measured* — never as a zero, and never less
  prominently than a fault. No secret appears on this screen: it is reachable by anyone holding the
  phone. It is written in the phone's language (FR-13.5), and a fact the person holding the phone can
  fix — background activity, exact alarms, usage access — offers the settings screen that fixes it.

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
- FR-15.6 A device installs a newer hosted build **by itself**, without a parent pressing anything.
  FR-15.2 stays — a parent can still ask — but it is the shortcut, not the mechanism. Nobody watches
  a version number on a child's phone, so an update that needs to be asked for is an update that
  does not happen; and the phones that most need a fix are the ones whose parent is least likely to
  be looking. The device decides, on evidence it can obtain cheaply: the control plane states the
  version it hosts (FR-15.1), so a device that is already current spends one small request and no
  download. Every refusal in FR-15.3 still applies, unchanged, to the archive that is downloaded.
- FR-15.7 A self-update that did not result in a new build running is **reported to the parent**,
  in the platform's own words, and the report clears itself once a newer build is seen running. The
  acknowledgement in FR-15.4 is a statement about the future: it is sent before the install and says
  nothing about whether the install happened. Without this requirement every failure downstream of
  it is invisible — the command shows acknowledged, the phone keeps reporting, and the version
  simply never changes, which looks identical to a phone that is already current.
- FR-15.8 There are **two builds of this app and they are not the same binary**, because Google Play
  forbids what each other half needs. Play's Device and Network Abuse policy bans ad filtering
  outright, and Play refuses the `UPDATE_PACKAGES_WITHOUT_USER_ACTION` declaration that FR-15.6
  rests on — measured, with no declaration form and no appeal. So the self-hosted build carries the
  ad filter and updates itself, and the Play build carries neither. The difference is a build flag
  (`-PplayBuild=true`), not a fork: one source tree, one signer, one set of tests, and the feature
  compiled out at its edges rather than branched around. A phone running the Play build reports
  *nothing* about the filter — never "off" — so FR-6.10's three-valued rule is what keeps the
  console honest about which build a phone is running.

### FR-16 Applications the parent chooses (managed apps)
A locked-down phone cannot install anything, which is the point — and it is also why a child ends
up with a phone that has nothing on it a parent actually wanted them to have. Putting an
application on the phone must therefore be something the parent does from the console, once, for a
child; and the phone must arrive at that state by itself, on a device the child cannot install on.

- FR-16.1 The control plane holds a catalog of application builds. A build enters it either by
  being uploaded, or by being copied into the server's application directory on the node and
  scanned — an operator with shell access must not have to go through a browser to add a 200 MB
  file. Nothing about a build is taken on the uploader's word: the package name, version, size,
  minimum SDK and signer are read out of the archive itself.
- FR-16.2 A build can be uploaded through the console and through the REST API, and the API accepts
  both a browser's multipart form and a raw request body, so `curl --data-binary @app.apk` is a
  first-class way in. The control plane refuses what is not a readable Android application, and
  says which of the two it was: not an APK, or larger than this server accepts.
- FR-16.3 A parent declares, per child, which applications that child's phone should have. It is a
  **set**, not a queue of install commands: the device converges on it at every sync, so an install
  that failed retries by itself and an application the child removed comes back, without a parent
  having to notice that anything went wrong. Declaring an application is not allowing it — a
  managed app is suspended at bedtime, hidden by a block rule and counted against the quota like
  any other (FR-4, FR-5, FR-3).
- FR-16.4 A package's signer is pinned at its first registration, and a later build of the same
  package signed by a different key is refused. This is trust on first registration, not signature
  verification: it does not establish that the first build was genuine, and it is not claimed to.
  What it does is make the second one unable to differ silently — which is the failure that
  matters, because a phone will accept an update to an app it already has.
- FR-16.5 Withdrawing an application from a child's set uninstalls it from that child's phone. A
  parent who removes an application must not have to also find and press a second control, and must
  not be told the removal is done while the application is still on the phone.
- FR-16.6 The FamilyGuard app itself is not a catalog entry. It updates through FR-15, which is a
  different mechanism with different checks, and two descriptions of what version of itself the
  phone should run is a loop, not redundancy.

### FR-17 Non-interactive access (API keys)
Everything a parent can do must also be doable by something that is not sitting at a browser — a
script, or an MCP server acting for a parent. That is one credential format presenting the **same**
parent identity, not a second authorization surface: a key is a parent, subject to the same role
checks, reading and writing the same records, and appearing in the audit trail as itself.

- FR-17.1 A key is issued by a primary admin, is shown exactly once at creation, and is stored only
  as a hash. A console that can show a key again is a console that is holding one.
- FR-17.2 A key authenticates every parent endpoint, with one exception: it cannot create or revoke
  a credential — neither another key nor a parent. Revocation has to be sufficient, so the set of
  things a leaked key can use to outlive its own revocation must be empty.
- FR-17.3 Revocation is immediate and is what the design relies on instead of expiry. Each key
  records when it was last used, because that is what tells a parent which key to revoke.
- FR-17.4 The audit trail distinguishes a key from a person: every entry names the actor type, so
  "who changed this" is answerable without inferring it from the hour of the day.

### FR-18 Applications nobody in the family should have (the blocklist)
A phone arrives with software on it that the family did not choose — a social network, a vendor's
promotional service, an installer whose job is to put those back. Deciding to be rid of one of those
is not a decision about a child; it is a decision about the household, and having to repeat it per
child, and again for every child added later, makes it a decision that decays.

So the family has one blocklist, and it is the same instrument as an app rule pointed at a wider
scope. It does not uninstall: entries are hidden and suspended, which is reversible from the console
and survives a reinstall, and which never removes an app that a factory reset would not restore.

- FR-18.1 The list belongs to the family. Every child is subject to it, including a child added
  after an entry was written, and a change to it reaches every enrolled device.
- FR-18.2 An entry covers a package whether or not it is installed now. Blocking a preinstall whose
  installer stub is still resident, and blocking only what is on the phone today, are the two ways
  this fails; both are the same omission.
- FR-18.3 A child-level ALLOW rule exempts that child from a family entry. It does not lift that
  child's own BLOCK: a household default must never overrule a decision made about one child.
- FR-18.4 FR-5.5 outranks the list. A package the device reports as critical — its dialer, launcher,
  settings, SMS app or any enabled keyboard — is not hidden however it got onto the list.
- FR-18.5 The list ships with a curated set of entries, seeded once. A parent may delete any of
  them, and a deletion is permanent: nothing re-applies the curated set on restart.
- FR-18.6 The console reports what the **phone** says, not what the rule asked for. Each device
  reports, per package, whether it currently has it hidden or suspended, and the blocklist shows
  that back: hidden, on the phone but not hidden yet, or not installed here. A package the device
  has hidden stays in the inventory — hiding clears the installed-for-this-user flag, and a phone
  that dropped it from the list would report the blocked app as absent, answering "is it gone?"
  with the one word that is both wrong and reassuring.

### FR-19 Remote debugging (adb from anywhere)
When something breaks on a child's phone, the fastest way to find out why is the phone's own log —
and the phone is at school, or at a grandparent's, on a network the parent is not on. So a parent can
reach the phone's own adb through the control plane, from anywhere, with the API key they already
use for the command line (FR-17). It exists to shorten fixing this product, and it is built to be
removed again: nothing else depends on it.

A remote shell on somebody's phone is the most powerful thing in this system, so every requirement
below is about who can open it, what it can reach, and who can see that it is open.

- FR-19.1 A parent opens a session with `fgctl adb <device>`, which listens on a local port for
  `adb connect` or, once, `adb pair`. **Nothing is opened unless the child's Allow debugging is
  on** — that switch is what lets adb run on the phone at all — and a session that cannot be opened
  is refused before the phone is asked, in a sentence rather than a timeout.
- FR-19.2 The phone dials back, authenticated with its own device token, and a session belongs to
  exactly one phone: another phone presenting the same session id is refused, and a session id is
  good for one dial.
- FR-19.3 The phone finds its own Wireless debugging port — adbd picks a new one each time — and
  connects to it on its own loopback. It looks on the loopback first, for the listener that answers
  the adb greeting with STLS, so it depends neither on mDNS nor on the network (0.6.32: on the
  family's Android 13 phone mDNS never announced the port while the ad filter ran); mDNS is the
  second way. When Wireless debugging is off, the phone asks for it as device owner and reads the
  setting back. A parent can also name the port the phone shows. **Pairing is the one step that needs a person at the phone**: only the phone
  can show a pairing code, and that is where Android keeps the decision about which computers it
  trusts.
- FR-19.4 The server relays and nothing more. It never speaks adb and cannot read what it carries:
  adb's pairing and its connection are TLS between the parent's adb and the phone's adbd.
- FR-19.5 **The phone shows that someone is connected**, for as long as any session is open, in a
  notification the child can see.
- FR-19.6 Every session is audited when it is asked for and when it ends, with how long it lasted
  and how much it carried. A session ends after an hour idle and after four hours regardless.
- FR-19.7 When the phone cannot open a session — Wireless debugging off, no port announced, adbd
  refusing — its own reason reaches the parent at once.
- FR-19.8 **A session reaches a phone that is awake.** While its screen is off the phone goes quiet
  on purpose (NFR-10), so a request waits for the phone's next contact and gives up after 30 s with
  a sentence saying to wake the phone. Keeping the connection open through sleep would make the
  phone reachable at any hour at a battery cost the owner ruled out on 2026-09-23: *"its fine to only
  reach when the phone is awake, otherwhise the batter drains too fast"*.

The five requirements below were agreed with the owner on 2026-09-27 and are designed in
[`docs/superpowers/specs/2026-09-27-daily-plan-design.md`](docs/superpowers/specs/2026-09-27-daily-plan-design.md).
FR-20 is built (phase 1, 0.6.18), FR-21 (phase 2, 0.6.19), FR-22 (phase 3, 0.6.20), FR-23 (phase 4, 0.6.22), FR-24
(phase 5, 0.6.23) and FR-25 (phase 6, 0.6.24).

### FR-20 Roles and rights (phase 1)
Measured 2026-09-27: the role was checked on 7 of 48 parent routes, so a guardian could delete a
child.

- FR-20.1 Every parent endpoint names the roles that may call it, and a route that names none fails
  the build's tests. A `GUARDIAN` may read `/me`, the family, the profiles, the devices and each
  device's desired state (the state only, never the input it was computed from), give or take
  time today and pause or unpause a profile (FR-21), and receive the event stream — nothing else;
  FR-22 adds the guardian window's task confirmations. Reading the family blocklist moved from
  every parent to admins, because the guardian window lists no apps.
- FR-20.2 Only a `PRIMARY_ADMIN` changes a role (`PATCH /parents/:id`), and only from the console
  (FR-17.2's reasoning: an API key is refused). Nobody changes their own role (409); the last
  `PRIMARY_ADMIN` cannot be demoted, even by two primary admins demoting each other at the same
  moment (409, "the family must keep at least one primary admin"). A change holds on the parent's
  next request — the same session token — and for their API keys, and is audited as
  `PARENT_ROLE_CHANGED` with `from` and `to`.
- FR-20.3 The console offers People & rights to the primary admin: add a person with a role
  (Guardian by default), change a role, remove a person; their own row offers neither. A `GUARDIAN`
  who signs in sees only the guardian window — since 0.6.33 that is Übersicht (FR-13.6), without its
  phone commands — whatever link they arrived on: one card per profile with today's time against the
  limit, why apps are paused, and +15 (−15, +30, +60 in its sheet) where there is a limit. The page
  requests nothing a guardian may not read.

### FR-21 Pause and today's time (phase 2)
- FR-21.1 A parent or guardian can **pause** a profile's phones: every app the child can open is
  suspended — always-free and preinstalled-free apps included — except the critical packages (FR-5.5)
  and the family's always-usable communication apps (FR-5.9: WhatsApp, Signal, Threema, Audible). It
  is a policy state, not a command: it outranks bedtime and the daily limit, is honoured in
  tracking-only mode like LOCK_NOW, survives a reboot and an offline phone (the phone caches the input
  it recomputes from), and ends only when someone unpauses — which restores exactly the state it
  replaced. Audited as `PROFILE_PAUSED` / `PROFILE_UNPAUSED`. *The separate admin-kept communication
  list the design proposed was not built: the family decided this exact list on 2026-09-20 and it
  already travels in every policy.*
- FR-21.2 Today's time can be **added or taken away** (−1440 … 1440 minutes, not 0). More than 1440
  minutes of extra time is refused; taking more than the day has leaves the day at zero minutes, and
  zero with a limit is a limit reached, never "no limit" — the engine reports the plain limit beside
  the day's quota so no screen can confuse the two. Audited as `BONUS_GRANTED` / `TIME_REDUCED`.
- FR-21.3 The guardian window offers both: *Pausieren* (armed by a first tap, done by a second within
  five seconds) or *Fortsetzen*, and −15 · +15 · +30 · +60; an admin has the same page as the first
  tab, *Übersicht* (until 0.6.33 *Sperren*/*Entsperren* on a separate *Today* tab). The phone says so
  in German: *"Deine Eltern haben dein Handy pausiert. Anrufen und Nachrichten gehen weiter."* `fgctl pause|unpause`, `fgctl bonus ±n` and the MCP tools
  `pause_profile` and `adjust_time_today` do the same.

### FR-22 Daily plan and earned time (phase 3)
A profile has a plan of task groups, each with weekdays, a window and the minutes it earns. The child
reports a task done on the phone; it counts only when a parent confirms it, which needs the server. A
group whose tasks are all confirmed earns **Bonuszeit**: a balance shown apart from the daily budget,
valid for seven days from the day it was earned, spent oldest first. It is spent automatically on any
app once the daily budget is used up or bedtime has begun, and always on apps marked as bonus apps,
which run on nothing else. Precedence: pause, then a parent's block on one app, then earned time, then
bedtime, then the daily budget.

- FR-22.1 **The plan** is one document per profile (`GET|PUT /children/:id/plan`, admins): up to 12
  groups, each with a title, weekdays (a bit set, Monday 1 … Sunday 64), a window `HH:MM`–`HH:MM` on
  one day, 0–1440 minutes and 1–20 tasks (title ≤ 120, note ≤ 200). A group or task sent with its id
  is edited in place and keeps its history; one without is new; one left out is retired, never
  deleted. Audited as `PLAN_UPDATED`. The console edits it under *Regeln › Zeit*.
- FR-22.2 **Reporting** is the child's *Fertig* on the phone's Heute screen
  (`POST /device/tasks/:task/report`): only a task of the phone's own profile, of a group that runs
  today, inside its window, and only with the server reachable. It earns nothing by itself; it asks a
  parent (`TASK_REPORTED`).
- FR-22.3 **Deciding** (`POST /children/:id/tasks/:task/decision`, every parent role): confirm, reject
  or undo, for today. A task need not have been reported to be confirmed. Confirming a group's last
  open task credits its minutes for the day (`EARNED_TIME_CREDITED`); any decision that leaves the
  group incomplete withdraws the credit (`EARNED_TIME_WITHDRAWN`). The guardian window lists reported
  tasks under *Wartet auf dich* with *Bestätigen* / *Nicht erledigt*, shows each card's tasks for
  today with *Bestätigen* (or *Rückgängig* once confirmed), and the balance in gold — a debt in red —
  with the day the oldest credit runs out.
- FR-22.4 **The balance** (`GET /children/:id/today`): credits from the last seven days, spent oldest
  first, dropped after their seventh day; spending beyond the balance is carried as a debt the next
  credit settles. Two phones of one profile can spend the same minutes before they sync (accepted).
- FR-22.5 **Spending** is attributed on the phone, per measured window, as it is measured: a minute of
  a bonus app, a minute inside bedtime, and a minute after the daily budget is spent are paid in
  earned time; always-free, preinstalled-free, critical and uncounted apps never are. A minute paid
  in earned time does not also count against the budget. Nothing is charged while there is no earned
  time left or the profile is in watch-only mode: nothing could pay, and a charge would only become a
  debt against minutes not yet earned (measured on a family phone in watch-only mode, 2026-09-27).
- FR-22.6 **Bonus apps** are the fifth answer on the Apps page, *Bonus-App* (rule `BONUS`): suspended whenever no
  earned time is left, whatever the hour — the console says *Paused — no earned time left* (reason
  `EARNED`) — and usable while there is some, even in bedtime or past the limit.
- FR-22.7 `fgctl plan <child> [--set file.json]` (the document `--json` prints is the one `--set`
  takes), `fgctl today <child>`, `fgctl confirm|reject|undo <child> <task>`, and the MCP tools
  `get_plan`, `set_plan`, `get_today` and `decide_task` do the same.

### FR-23 Alarm (phase 4)
A profile has an alarm time per weekday, a one-off override for the next day and an option to stay
silent in holidays. It rings on the phone with no connection, through Doze, as a full-screen alarm the
child can stop or snooze but not reschedule.

- FR-23.1 **The week** (`GET|PUT /children/:id/alarm`, admins): seven entries, Monday first, each
  `HH:MM` or `""` for a day with no alarm. Audited as `ALARM_UPDATED`. The phone is sent the rule and
  the profile's timezone beside its policy (`alarm`), never an instant, and computes the next ring
  itself — so it rings offline and across a change of timezone or daylight saving.
- FR-23.2 **A change for one date** (`PUT|DELETE /children/:id/alarm/days/:day`, admins): a time, or
  `null` for no alarm that day, from today up to 60 days ahead in the profile's calendar; a past day,
  a later one and an impossible date are refused. One change per day; removing it returns the day to
  the week. Audited as `ALARM_DAY_SET` / `ALARM_DAY_CLEARED`. The phone is sent the changes from today
  on.
- FR-23.3 **Booking.** The phone computes the next ring from the rule — a date change before the
  week; a time a spring-forward day skips rings when the clock resumes, one a fall-back day repeats
  rings once — and books it with `AlarmManager.setAlarmClock`, the alarm the platform delivers on time
  in Doze and shows in the status bar and on the lock screen. It re-books after a new rule, a ring, a
  snooze, a stop, a reboot, an update of the app and a change of time or timezone. The rule and the
  ring state live in credential-encrypted storage, so a phone restarted overnight and not yet
  unlocked books nothing until it is unlocked.
- FR-23.4 **Ringing.** The alarm tone on the alarm stream, looping, with vibration, for at most ten
  minutes; the alarm stream is raised to at least half while it rings and restored after when its
  level could be read. A full-screen alarm over the lock screen offers *Stopp* and *Schlummern 5 Min.*;
  where the platform withholds the full-screen intent the same controls come as a high-priority
  notification, and the sound is the same. Nothing on the phone changes the schedule.
- FR-23.5 **The Heute screen** says the next alarm first: *Wecker: heute 06:30*, *morgen 06:30*, or
  the weekday.
- FR-23.6 The console's *Regeln › Zeit* has a *Wecker* card (the week; one date set to a time or to no
  alarm; the date changes listed with *Remove*), and a device card says *alarm: notification only*
  when the phone reports that it may not take over the lock screen (`alarm_full_screen: false`).
  `fgctl alarm <child> [--set file.json]`, `fgctl alarm-day <child> <date|today|tomorrow>
  <HH:MM|off|clear>` — today and tomorrow in the profile's timezone — and the MCP tools `get_alarm`,
  `set_alarm` and `set_alarm_day` do the same.

### FR-24 Agenda (phase 5)
A profile has recurring and single agenda entries, any of them optional, and the family has holidays
that suspend recurring entries. The phone shows today and tomorrow; the console shows a week.

- FR-24.1 **The agenda** is one document per profile (`GET|PUT /children/:id/agenda`, admins): up to
  50 entries, each *repeating* (`RECURRING`, weekdays Monday = 1 … Sunday = 64) or on *one date*
  (`SINGLE`, a real date), with a title (≤ 80), an optional place (≤ 80), from–to on one day, and an
  *optional* flag. An entry sent with its id is edited in place; one without is new; one left out is
  retired, never deleted. Audited as `AGENDA_UPDATED`.
- FR-24.2 **Holidays** are the family's (`GET|PUT /family/holidays`, admins): up to 50 ranges with a
  title, first and last day included, at most 120 days long, kept by id like the agenda. Audited as
  `HOLIDAYS_UPDATED`; every profile's phones re-read.
- FR-24.3 **The days** (`GET /children/:id/agenda/days?from=&days=1…31`) are expanded on the server:
  a repeating entry on its weekdays, a single entry on its date, in time order, with the holiday that
  applies. A holiday suppresses the repeating entries only — a single entry on a holiday still
  happens. The phone is sent today and tomorrow in the profile's calendar beside its policy
  (`agenda`).
- FR-24.4 **Not during holidays** is a flag on a profile's alarm (`skip_holidays` on
  `PUT /children/:id/alarm`). The phone is sent the holidays that have not ended with the alarm rule,
  and rings on no date inside one — unless that date was changed on its own, which always wins.
- FR-24.5 **The Heute screen** shows the holiday, *Jetzt* (what is on now, until when), *Danach* (the
  next item today) and *Morgen* (tomorrow's items, or its holiday), with the place and *(freiwillig)*
  for optional entries. Days are chosen by date on the phone's clock in the profile's timezone, so a
  phone offline past midnight shows the right day and no tomorrow it was not sent.
- FR-24.6 The console's *Regeln › Agenda* has an *Agenda* card — entries every week or on one date, with
  place, from–to and *Optional*, saved as one document, and *This week* below as the server expands
  it — and the *Wecker* card a *Nicht in den Ferien* switch; *Familie* has *Ferien* (title, first
  and last day, its length). `fgctl agenda <child> [--set file]`, `fgctl week <child> [--from] [--days]`,
  `fgctl holidays [--set file]` (and `skip_holidays` in `fgctl alarm --set`), and the MCP tools
  `get_agenda`, `set_agenda`, `get_week`, `get_holidays`, `set_holidays` and `set_alarm`'s
  `skip_holidays` do the same.


### FR-25 Calendar import (phase 6)
A profile's agenda can include an existing calendar, read-only, from its iCalendar address.

- FR-25.1 **The address** (`GET|PUT|DELETE /children/:id/calendar`, admins) is `https://` or `webcal://`
  (read as https). Setting it reads it at once: an address that cannot be read, or that does not
  answer an iCalendar file, is refused rather than stored to fail later. The address is a credential
  — a secret calendar address reads the calendar — so it is shown to admins and never logged; the
  audit (`CALENDAR_SET`, `CALENDAR_REMOVED`) carries its host only.
- FR-25.2 **The fetch is fenced**: at most 1 MiB, 10 seconds, three redirects, and never to a private,
  loopback, link-local or shared (100.64/10) address — checked on the address actually dialled, so
  neither a DNS answer nor a redirect leads into the cluster. `CALENDAR_ALLOW_LOCAL=true` lifts the
  fence and allows plain http, for a bench only.
- FR-25.3 **The events join the days** of FR-24.3 as items with `source: "calendar"`: each occurrence on
  every day it touches, clipped to that day, all-day events (`all_day`) first; recurring events with
  their exceptions and moved occurrences; cancelled ones left out; a holiday does not hide them. The
  last good copy is kept with when it was read; a read that finds it older than `CALENDAR_MAX_AGE`
  (30 minutes) answers from it and reads again in the background, one read per profile at a time, and
  a failed read keeps the copy and records its error.
- FR-25.4 **The Heute screen** says today's all-day events on their own line (*Heute: Schulreise
  (ganztägig)*) — they are neither *Jetzt* nor *Danach* — and marks them in tomorrow's list.
- FR-25.5 The console's Agenda card has *Calendar (optional)*: the address, *Save calendar*, *Remove*,
  and what the last read found (*n events in the coming 60 days, read …*, or the error); the week marks
  calendar events and all-day ones. `fgctl calendar <child> [--set address | --remove]` and the MCP
  tools `get_calendar` and `set_calendar` (an empty address removes it) do the same.

### FR-26 Energy: an active and a passive mode (2026-09-28)
The phone spends energy on its connection only while someone can benefit from it. Design:
`docs/superpowers/specs/2026-09-28-energy-modes-design.md`.

- FR-26.1 **Two modes**, decided from the screen, a 60 s grace after it goes off, and Live (FR-27):
  ACTIVE keeps the connection stream open as before; PASSIVE closes it.
- FR-26.2 **In PASSIVE the phone wakes** for a push from the server, for a safety poll (every 30 minutes
  when push works, every 5 minutes when it does not), and for the screen coming on; each wake is one
  sync, and the update check runs at most every 6 hours. Built in 0.6.27 without push, so every poll was 5
  minutes (an exact allow-while-idle alarm; measured in forced Doze on the emulator: a queued command
  arrived in 5 min 11 s); since 0.6.29 a phone polls every 30 minutes once a push has arrived for the
  address it holds, and every 5 until then. The update check is every 6 hours in both modes — *Update app* is the
  shortcut. The server says whether a phone holds its stream (`stream_open`); a phone is online while
  it does, or within 11 minutes of its last check-in, and the console calls one that is online but
  not listening *resting*, with what that means for Lock and Ring.
- FR-26.3 **The push** carries no content (`{"t":"sync"}`), is sent only where the stream would have
  told an open connection, and is optional on the server: without its credentials every phone polls.
  Built in 0.6.29: FCM HTTP v1, the phone addressed by its Firebase Installation ID (`push_token` in
  the heartbeat; the device view says only `push_registered`), at most one push per phone per 10 s,
  one push at once when the server receives a new address (the proof the phone waits for before it
  stretches its poll), and an address FCM calls unregistered dropped. The Firebase project is
  configured on the server (`FCM_CREDENTIALS` and three public values) and handed to the phone with its
  policy. Measured on the emulator against the real FCM: registered 3 s after enrolling, and a command
  queued for the phone in forced Doze acknowledged after 6 s.
- FR-26.4 **The ad filter** routes only DNS after 5 minutes of screen off (held while media audio
  plays) and full traffic again when the screen comes on; the dialer, the SMS app, the carrier's IMS
  service, Signal, Threema, Google Play services and the Jellyfin clients (0.6.31) bypass it entirely. The bypass is built in 0.6.26; the default
  dialer and SMS app bypass only when they shipped with the phone. The screen-off route is built in
  0.6.29; "media audio" is the music stream or a call, and sound starting after the narrowing does not
  widen it again (the rebuild would cut the stream), so it narrows at most once per screen-off.
- FR-26.5 **The heartbeat reports the energy FamilyGuard spends**: its CPU time, the wake-ups it caused
  by kind, and the time in each mode and filter route. Built in 0.6.26 (CPU, data, wake-ups by kind),
  the mode times in 0.6.27 and the route times in 0.6.29: `GET
  /devices/:id/energy`, `fgctl energy`, MCP `get_energy`, and the *Energie* card under *Aktivität*.

### FR-27 Live mode (2026-09-28)
- FR-27.1 A parent or guardian starts Live for a device from the console (*Live 30 min*), fgctl or MCP,
  and can stop it; it is audited, and a push reaches a phone whose stream is closed (since 0.6.29; before
  that, its next poll).
- FR-27.2 While Live runs the phone is ACTIVE whatever its screen does and reports its GPS location
  every 10 seconds; the console shows the position following the reports, with the time and accuracy
  of the last fix.
- FR-27.3 `GET|POST|DELETE /devices/:id/live` (admins and guardians): default 30 minutes, at most 120;
  extending keeps the session's start. A guardian reads the positions captured since the session
  began and never the history before it (`/devices/:id/locations` stays an admin's). Audited as
  `LIVE_STARTED` / `LIVE_STOPPED`. `fgctl live <device> [--start [--minutes N] | --stop]`, MCP
  `get_live`, `start_live`, `stop_live`. Built in 0.6.27; the phone's service takes the `location`
  type only for the session.
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
- **NFR-14 The running notice is as quiet as the platform allows.** A foreground service must
  declare itself, and this one should: a DPC that hid what it was doing would be the wrong thing to
  build. But it says so at `IMPORTANCE_MIN` — no status-bar icon, no sound, no badge, one collapsed
  line at the bottom of the shade — because a persistent notification a family looks at all day is
  one they learn to dismiss, and the notices that *do* need acting on (unlinked, usage access) share
  the shade with it. A channel's importance is fixed when the channel is created, so lowering it
  means a new channel id and deleting the old one; a release that only edits the importance changes
  nothing on a phone that already has the app.

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
