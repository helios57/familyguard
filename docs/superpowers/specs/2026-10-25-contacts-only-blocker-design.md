# Contacts only: strangers cannot reach a child (design, FR-31)

Status: **approved by the owner 2026-10-26** (§6 answered). Calls are built as FR-31; SMS and WhatsApp
stay with their own apps' settings, which the owner has already set on both phones.

## 1. Intent

The owner's words: *"my kids should only get calls, SMS and WhatsApp messages from people in their
address list (whitelist) — no one not in their address list should be able to call or contact them
in any way"*, and *"It's fine if the kids add it, I just don't want strangers to call them."*

So the list of who may reach a child is **the phone's own contacts**, which the child may edit. This
protects against strangers, not against the child. A child who saves a stranger has made them a
contact, and the owner accepts that.

## 2. What the platform allows. This decides the design.

Measured from the API 37 platform source (`android/telecom/CallScreeningService.java`) and public
documentation, 2026-10-25:

| channel | can a device owner stop a stranger? | how |
|---|---|---|
| **Phone calls** (SIM / VoLTE) | **Yes**, before the phone rings | `CallScreeningService` in the call-screening role. Telecom binds it for every incoming `tel:` call before ringing. *"Only calls which are not in the user's contacts are passed for screening, unless the service has been granted READ_CONTACTS."* The response can disallow + reject the call and skip the call log and the missed-call notification. |
| Calls from a **hidden / private / unknown** number | **No, not through that API** | *"Calls with a presentation of RESTRICTED, UNKNOWN, UNAVAILABLE or PAYPHONE are not provided to the CallScreeningService."* These ring through unless the phone app's own "Block unknown/private numbers" switch is on (Samsung Phone › Settings › Block numbers). A DPC can neither set nor read that switch. |
| **SMS / MMS / RCS** | **No**, unless FamilyGuard becomes the SMS app | Since Android 4.4 only the default SMS app receives and stores messages, and only it (or the default dialer) may write the system block list (`BlockedNumberContract`). Samsung Messages has per-number blocking and a spam filter, but no "contacts only" switch (searched; not found). A device owner can make an app the default SMS app (`setDefaultSmsApplication`), but that app would have to be a complete messaging app, RCS included. |
| **WhatsApp** messages and calls | **No** | WhatsApp has no managed configuration and no API for a third party. Its own settings get part of the way: *Privacy › Calls › Silence unknown callers* (the call does not ring but is logged), *Privacy › Groups › My contacts*, *Privacy › Advanced › Block unknown account messages* (only against a flood, not a single message). FamilyGuard can neither set nor read any of them. |

The only routes to more than this are reading WhatsApp's screen through an accessibility service, or
dismissing its notifications from a notification listener. Both are hacks: the message still
arrives, and the first breaks on every WhatsApp update. Both are rejected, under the owner's rule
"no hacks".

## 3. Proposal

### 3.1 Calls: built, enforced, reported (FR-31)

- A new per-child policy switch **"Nur Kontakte dürfen anrufen"**, off by default. It is set through
  the console (child page), the API and MCP `set_policy`, like every other policy field.
- When it is on, the DPC holds the call-screening role and **rejects every incoming call that is not
  from a contact**: no ring, no call-log entry, no missed-call notification for the child.
- **Family numbers always pass.** A family-wide list of numbers (the parents', set by an admin) is
  allowed even when the child has deleted or never saved them. *My call:* the alternative is a parent
  who cannot reach their child because a contact was deleted, which is the one failure this feature
  must never cause.
- **Emergency callbacks always pass.** The platform does not screen in emergency-callback mode. This
  is to be measured, not assumed.
- **What parents see:** each rejected call is reported to the server as number and time, and the
  console's Aktivität shows "Abgewiesene Anrufe" per day. *My call:* without it, a feature that
  silently drops calls has no visible proof that it works, and a parent cannot spot a wrongly
  blocked number.
- **The role must stay put.** The parent grants the call-screening role once during setup (one system
  dialog, no device-owner API grants it silently). From API 34 the DPC sets
  `DISALLOW_CONFIG_DEFAULT_APPS` so the child cannot take the role away. On API 33 (the S20) it cannot
  prevent that, so the DPC checks at every sync that it still holds the role. If not, the phone
  reports it and the console shows the child's card in red ("Anrufschutz aus").
- **READ_CONTACTS is not requested.** The platform then passes only non-contacts, so the DPC never
  reads the contact list and needs no contact lookup of its own. The family-numbers list is the one
  thing it compares against. *My call:* least data, and the contacts decision is made by the
  platform's own matching, which is better than ours.

### 3.2 Hidden numbers: a setup step, stated plainly

The parent turns on the phone app's "Block unknown/private numbers" switch once, during setup.
DEPLOYMENT.md and the console's setup checklist say so. FamilyGuard cannot check it, and the console
says *"nicht prüfbar"* rather than showing a green tick.
*Open for measurement:* whether the DPC can end such a call itself (`ANSWER_PHONE_CALLS` +
`TelecomManager.endCall`, deprecated since API 29). If it can, the phone would ring for a moment
first. This goes in only if the emulator shows it works.

### 3.3 SMS and WhatsApp: not built (owner, 2026-10-26)

Recommendation: **FamilyGuard does not take over SMS or WhatsApp.** The setup checklist names the
WhatsApp settings above and Samsung Messages' spam filter, and the console states that these two
channels are *not enforced* by FamilyGuard. A parental control that claims to cover WhatsApp and
cannot would be worse than one that says so.

The owner chose the checklist for both (§6), and has already set those switches on both phones.

### 3.4 Settled after a builder's read (all my calls)

- **Phones already in use get the role through the phone itself.** Turning the switch on makes
  the child's phone show a notification, "Anrufschutz einrichten". Tapping it opens the system role
  dialog, which a parent confirms with the phone in hand. The other route is the existing remote-adb
  tunnel (`cmd role add-role-holder`), to be measured. Until the role is held, the card shows
  **amber "wartet auf Freigabe am Handy"**, not red. Red means the role was held and was then lost.
- **Family numbers**: one list per family. Only an admin may edit it, through the console's Familie
  page, the API and MCP. At most 10 numbers. Stored as E.164, and a parent may type `079…`, `0041…`
  or `+41…` (normalised with the family's country, default CH). It travels to the phone in the
  desired state, and the phone compares it with `PhoneNumberUtils` against the SIM's country.
- **Rejected calls**: stored per event (number as E.164, time, device), visible wherever Aktivität is
  visible, and kept **one year**, following the owner's retention rule for location data ("keep it
  for at least 1 year"). Never in the public repo's fixtures. Tests use `+1 555 01xx` numbers.
- **Fail-open is a documented gap.** If FamilyGuard is not running or answers too late (5 s), Telecom
  rings the call, and no API can do better. The check at every sync covers "role held" **and**
  "service last answered a screening", so a force-stopped DPC shows up as well.
- **Taking the role from Samsung's caller-ID/spam app** (Smart Call or Hiya) is accepted: while the
  switch is on, every stranger is rejected, spam included. Switching it off keeps the role and lets
  every call through. Handing the role back would need the dialog again.
- **The `endCall` fallback for hidden numbers ships only if the emulator shows it working.** If the
  emulator cannot produce a hidden-number call, it does not ship, and §3.2's manual switch stands. It
  never holds up the release.
- **Old phones**: same precedent as on-at-0. The server refuses the switch (409 `devices_outdated`)
  while a phone of that child reports a build without screening support.

## 4. Non-goals

- Blocking outgoing calls. A child may call anyone, emergency numbers above all.
- Stopping a child from adding contacts (owner: fine).
- Reading, storing or uploading the child's contact list, messages or WhatsApp content.
- Any change to Jellyfin, games or time rules.

## 5. How it is proven (owner: "an actual test instead of mocks")

The emulator has a real Telecom stack and a simulated modem:

- `adb emu gsm call <number>` places a real incoming call, and `adb emu sms send` a real SMS.
  Contacts are inserted through the contacts provider. The ringing state is read from
  `dumpsys telecom`, never from the app's own claim.
- Cases:
  - a contact rings;
  - a stranger does not ring and leaves no call-log entry;
  - a family number that is not a contact rings;
  - switch off: a stranger rings (the negative control);
  - role removed on API 33: the phone reports it.
- Both images, API 37 and API 33. Calibrate by breaking a value (e.g. the response's
  `setDisallowCall`), never by deleting a symbol.
- What only the owner can measure: a real SIM call on the S24 and the S20, a hidden-number call, and
  whether Samsung's Phone app honours a third-party screening app (Truecaller and Hiya rely on it, so
  it is expected, not proven).

## 6. Decisions the owner made

1. **WhatsApp: its own settings, set by hand, not enforced** (owner, 2026-10-26: *"a) habe ich schon
   gemacht (bei beiden) dann kann man da nichts mehr tun, das pass so"*). Already set on both phones.
2. **SMS: Samsung Messages' spam filter, strangers' SMS still arrive** (the same answer). No
   FamilyGuard SMS app.
3. **Family numbers always get through** (owner, 2026-10-25: *"unsere (kontakt) nummer sollen immer
   durchkommen"*), even if a child deletes them from her contacts.
