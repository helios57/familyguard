# Energy: an active and a passive mode, a push wake-up, and a lighter ad filter — design

**Status:** design, awaiting the owner's review. Decisions below marked *(owner)* were taken by the
owner on 2026-09-28; everything else is proposed.

## 1. Why

The owner: *"It is draining too much energy … For now it would be fine to have 2 modes. Active (like
today) active when the screen is on and also when triggered on the console for about 30 minutes (use
case tracking the phone live while stolen or the child is walking home). Passive (screen off) only ping
all 5 minutes to see if there is an update. Unless there is a better solution."* And: *"It's also ok to
whitelist some traffic to go around if it's safe and guaranteed not to contain ads."*

What was measured before this design (IMPLEMENTATION_PLAN Phase 40): the largest drain was a defect,
not the architecture — the ad filter's upstream thread spun one core at 100 % with the screen off
(10 min 48 s CPU in 12 minutes on an idle emulator). Fixed in 0.6.25. What remains is structural:

| wake source today | cost with the screen off |
|---|---|
| the connection stream's keepalive, every **20 s** | on mobile data each packet holds the radio in its high-power state for its tail (~10 s on LTE), so the radio is up about half the time |
| the stream re-opened every 15 min, each time with a full sync | one request and one sync per 15 min |
| the update check, every 15 min | one request per 15 min |
| the ad filter carrying **all** traffic (`0.0.0.0/0`) | every packet of every app crosses FamilyGuard's process |

A 5-minute ping costs about 15 times fewer radio wake-ups than a 20 s keepalive. A push message costs
nothing until something happens.

## 2. The modes (FR-26)

`PowerMode` is decided by one pure function of four facts:

```
decide(screenOn, screenOffSince, liveUntil, now) → ACTIVE | PASSIVE
  ACTIVE  if liveUntil > now                       — Live, from the console
  ACTIVE  if screenOn
  ACTIVE  if now − screenOffSince < 60 s           — grace: a glance at the clock is not a mode change
  PASSIVE otherwise
```

- **ACTIVE** is today's behaviour: the stream is open, changes arrive within a second. During Live it
  also reports the location (§4).
- **PASSIVE** closes the stream. The phone wakes for three reasons only:
  1. a **push** from the server (§3) — a parent did something that concerns this phone;
  2. a **safety poll**: every **30 min** when push works, every **5 min** when it does not (no Google
     Play services, no push configured on the server, or no push token yet);
  3. the screen coming on, which is ACTIVE again and syncs at once.

  Each wake is one sync: desired state, pending commands, heartbeat, and — at most every 6 hours in
  PASSIVE — the update check. The poll is booked with `setExactAndAllowWhileIdle` (Doze allows it;
  12 per hour stays well inside the 72-per-hour quota for exact alarms). A measured trap applies: a
  coroutine `delay` stops while the phone sleeps, so nothing in PASSIVE waits on one.
- The screen-on sync is what makes the passive delay harmless for the child's own actions: a child who
  asks for more time is looking at the phone, so the phone is ACTIVE and the answer arrives at once.

## 3. The push wake-up — Firebase Cloud Messaging *(owner: chosen over a poll alone)*

- **What is sent:** a high-priority data message whose payload is `{"t":"sync"}` — no child, no
  command, no name. Google sees that the server woke the app, and when; nothing else. The phone
  answers it by syncing from the control plane, exactly as it would after a poll.
- **When:** whenever the server already notifies a phone over the stream — a command queued (Locate,
  Siren, Lock …), the policy, plan, time grant, alarm or agenda changed, Live started — and the phone
  has no open stream. Coalesced to at most one push per phone per 10 s.
- **Server:** FCM HTTP v1 with a service account. Configured by `FCM_CREDENTIALS_FILE` (a mounted
  secret) — absent, push is off and every phone polls every 5 min; nothing else changes.
- **Phone:** `firebase-messaging`, initialised **at runtime** from options the server hands out with
  the desired state (`project_id`, `application_id`, `api_key`, `sender_id`). The public repository
  therefore carries no `google-services.json` and no project of anyone's; a self-hoster configures
  their own project on the server. The phone reports its push token in the heartbeat; a token that FCM
  answers `UNREGISTERED` is dropped and the phone falls back to the 5-minute poll until it reports a
  new one.
- **Honest limits:** FCM needs Google Play services (both family phones have it). Android may
  deprioritise high-priority messages from an app that never shows a notification; a sync that
  changed nothing the child can see shows none, so the safety poll stays as the floor, not as an
  optimisation.
- **Setup:** one Firebase project with an Android app for `io.github.helios57.familyguard`, and one
  service-account key stored in the secret store. The Firebase CLI on the build machine is signed in,
  so this can be done from here — on the owner's say-so, since it creates a project in the owner's
  Google account.

## 4. Live mode (FR-27)

- **Console:** *Live 30 min* on the device card, with the remaining time and *Stop*. fgctl
  `live <device> [--minutes N | --stop]`, MCP `start_live` / `stop_live`. Admins and guardians alike —
  a guardian walking a child home is the use case. Audited.
- **Server:** `devices.live_until`; the desired state carries it; starting Live sends a push.
- **Phone:** while `liveUntil > now` the mode is ACTIVE whatever the screen does, and the phone
  reports its location every **10 s** *(owner)* from GPS (`LocationManager`, `minTime` 10 s), in a
  foreground service of type `location`. Reports go over the open connection; the server keeps them
  like Locate results (retention: at least one year, per the owner's standing rule).
- **Console map:** the device's position follows the reports while Live runs, with the time of the
  last fix and its accuracy. When Live ends the phone returns to its mode by the screen.
- A lost or stolen phone: Live from the console reaches it by push in seconds even with the screen
  off and the stream closed — that is the reason push was chosen.

## 5. The ad filter

- **DNS only while the screen is off** *(owner)*. Nobody sees an ad with the screen off, so after
  **5 minutes** of screen off the tunnel is re-established with only the tunnel resolver routed
  (the existing `RouteMode.DNS_ONLY`): ad domains are still refused, but app traffic no longer
  crosses FamilyGuard. The screen coming on restores `FULL` at once.
  - Re-establishing the tunnel cuts connections that were carried by it. So the switch waits while
    media audio is playing (`AudioManager.isMusicActive`) — music with the screen off must not stop
    every time — and it never happens more than once per screen-off.
- **Traffic that bypasses the filter entirely** *(owner)* — `addDisallowedApplication`, installed
  packages only:
  - the default dialer, the default SMS app and the carrier's IMS service (VoLTE, Wi-Fi calling);
  - Signal and Threema (every published Threema package);
  - Google Play services — **the owner's decision, with its risk stated**: Google's ads SDK runs inside
    Play services, so ads *fetched on behalf of other apps* through it are no longer filtered. It is
    also the package that carries push, which is a reason of its own to keep it off the tunnel.
  - WhatsApp is deliberately **not** on the list: it has shown ads in Status since 2025.
- **Idle cost of the filter's threads:** the upstream selector sleeps until there is work (no 500 ms
  timeout), the DNS forwarder likewise; housekeeping runs only while flows exist.

## 6. Knowing whether it worked

Energy is only an improvement if it is measured on the family phones, and remote `adb` needs a
pairing code each time. So the heartbeat gains a small **energy self-report**: the process's CPU time,
the wake-ups FamilyGuard caused by kind (push, poll, stream reconnect, screen-on sync), and the share
of time in each mode and route. fgctl `energy <device>` and the device detail show it per hour. It
ships **first**, so the family phones give a baseline on today's behaviour before anything else
changes.

## 7. Phases

| phase | content | needs |
|---|---|---|
| 1 | energy self-report; the bypass list; the filter threads sleeping when idle | — |
| 2 | the modes with the 5-minute poll; Live mode end to end (server, console, fgctl/MCP, phone, 10 s location) | — |
| 3 | push: server sender, runtime-initialised FCM on the phone, token in the heartbeat; safety poll → 30 min | the Firebase project and its key |
| 4 | DNS-only route after 5 min of screen off, held while audio plays | — |

Each phase: unit tests, e2e against the real server, the emulator for the phone parts (Doze forced
with `dumpsys deviceidle force-idle` — the poll and the push must both wake a dozing phone), probes
calibrated as value changes, a release, and a day of the self-report on the family phones before the
next one.

## 8. Not in this design

- A server-side resolver (the owner ruled out any default filtering resolver).
- WorkManager for the poll: 15-minute floor and deferred in Doze.
- Geofences ("tell me when she is home"): natural on top of Live, not asked for.
