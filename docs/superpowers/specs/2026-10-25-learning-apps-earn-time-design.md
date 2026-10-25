# Learning apps earn time — design

**Status:** draft 2026-10-25, for the owner's approval. The owner asked: *"all the apps under
<prefix>.\* (there should be 5 at the moment and there will be more) do add extra time when used.
and the default time should be 0 for the moment, so the kids have to play the learning-games to get
time for movies"*, then corrected: *"like bonus-time, it should not decay for a week"*. On the
movie player: *"<the family's own player> is for free, she uses <a media-server app> to watch
movies but it does not matter, the time can be used just like extra-time"*.

The prefix is the owner's own package namespace. It never appears in this repository: it is a
family setting (§4.1), and every test uses `org.example.learn.`.

## 1. Intent

A child earns screen time by playing learning games. Each minute in a learning app turns into a
minute of **Bonuszeit** the child can spend on anything that counts: a film, a game with its own
limit, anything that is not always free. The daily time is set to 0, so on a normal day the only way
to watch a film is to have played first. Learning apps themselves never cost time.

Why: the family wants learning to be the way to screen time, not a chore beside it. Today the
earned-time machinery (FR-22) exists, but the only way to fill it is a task a parent confirms by
hand. Playing should fill it by itself, immediately, on the phone, without a parent in the loop.

Success, on a family phone with daily time 0:
- Before playing, the media app is paused ("no time left") and a learning app opens.
- After 10 minutes in a learning app, the phone shows 10 minutes of Bonuszeit within one poll
  (about 5 minutes), **also with the phone offline**, and the media app opens.
- Watching for 10 minutes spends those 10 minutes and pauses the media app again.
- Minutes earned and not spent are still there the next day, and gone after their seventh day,
  exactly like task Bonuszeit.
- A sixth learning app installed later earns from its first minute, on every child, with nothing
  set per child.

## 2. Goals

- G1. Minutes in a **learning app** earn Bonuszeit, 1 minute per minute, credited to the day they
  were played on.
- G2. Earned minutes behave exactly like FR-22 Bonuszeit: one balance with task credits, valid seven
  days from the day earned, spent oldest first, spent automatically on any counted app once the
  daily time is used up (immediately, with daily time 0) or bedtime has begun, and always on bonus
  apps.
- G3. Learning apps are always free: using one never spends daily time, Extrazeit or Bonuszeit.
- G4. A learning app is recognised by a **package-name prefix** set once for the family, so future
  apps and future children are covered without per-child rules.
- G5. A daily time of **0 minutes** is a real setting ("nothing without extra or earned time"),
  distinct from **no daily limit**.
- G6. Both daily time settings to 0 for both children on the family's server, once the release is
  on both phones.

## 3. Non-goals and anti-goals

Non-goals:
- No exchange rate other than 1:1, and no per-child or per-app rate. (Owner did not ask; adding one
  later means one multiplier at the single place a credit is computed.)
- No daily cap on what can be earned. See §7, risk R1.
- No change to how Extrazeit, task Bonuszeit, *Mehr Zeit erbitten*, pause or bedtime work.
- The family's always-free apps stay always free; this feature does not reclassify any app.

Anti-goals (would make it a failure even if it "works"):
- A child who played offline sees no time until the phone reaches the server.
- A minute is earned twice (once by the phone, again when the server sees the same usage), or two
  syncs of one day's cumulative usage credit it twice.
- Existing families whose daily limit is 0 today ("no limit") wake up to a phone where nothing
  opens.
- The learning-app prefix, or anything that names the owner's apps, lands in the public repo.
- A learning app is paused because there is no time left.

## 4. Decided

### 4.1 Learning apps are a family setting

- One family-wide list of package prefixes, **learning prefixes**, empty by default. A package is a
  learning app when its name starts with one of them. Prefixes are validated as a package-name
  prefix (letters, digits, `_`, dots; ending in `.`), at most 10, each at most 100 characters.
  Changing it is audited (`LEARNING_APPS_UPDATED`) and reaches every phone of the family on its next
  sync (a push wake, as for other policy changes).
- Set through the API (admins only), `fgctl`, the MCP tools, and the console's family settings.
- A learning app is **always free** for every child (G3), unless a parent has **blocked** that one
  app for that child: a block wins and nothing is earned while blocked. Any other per-app rule on a
  learning app (limit, approval, bonus) is overridden by "learning app" and the console says so on
  the app's line (*Lern-App · verdient Zeit*).

### 4.2 Earning

- Earned minutes for a day = the counted foreground time of learning apps that day (same
  measurement as FR-3: screen-off pauses it, FR-3.8 uncounted packages never apply to a learning app
  because they are other packages), in whole minutes, floored, per day — not per app, so three
  apps at 40 s each earn 2 minutes, not 0.
- The credit is kept **per device per day**, as a cumulative figure like the usage totals: each
  phone reports how many learning milliseconds it counted today *while the package was a learning
  app and the phone was enforcing*, and the server merges it with `GREATEST`, as it does
  `foreground_ms`. Re-sent reports cannot double it. *My call (builder check, Q1).*
- **Nothing is credited retroactively.** Because the phone counts a minute as learning only at the
  moment it is measured, setting the prefix does not turn last week's always-free play into a
  balance; removing a prefix does not take back minutes already earned; blocking an app stops it
  earning from then on. A day's credit, once reported, never shrinks. *My call (Q1, Q2): the owner
  asked for time earned by playing, not for a starting grant.*
- A profile's credit for a day is the **sum over its devices**. On the phone, today's balance uses
  its own live count for itself and the server's figures for the profile's other phones; past days
  come from the server. *My call (Q6).*
- It joins the existing seven-day balance (FR-22.4) alongside task credits. Balance = task credits +
  learning credits of the last seven days − spending. Spending stays **oldest first, per window as
  measured** (FR-22.5): a film at 10:00 draws on yesterday's credit before today's, which leaves the
  freshest minutes to last longest. *My call (Q3): one rule for all Bonuszeit.*
- Not earned in watch-only mode (FR-8) — nothing is enforced there, and a balance built up while
  nothing could be spent would appear the day enforcement is switched on. *My call.*
- Pause (FR-21) still pauses learning apps like every other non-critical app, so nothing is earned
  while paused. Bedtime does **not** pause a learning app (it is always free) and minutes in it
  during bedtime earn as at any other hour. *My call: always free means always free.*

### 4.3 The phone is the first to know

- The phone computes today's learning credit from its own measurement and adds it to the balance it
  received from the server, so the credit is usable offline. "Immediately" means at the engine's
  next evaluation after the minutes are measured, i.e. within the existing poll (~5 min); no new
  trigger. *My call (Q5).* For today, the
  phone uses the larger of its own count and the server's count for this device; for past days it
  uses the server's. Two phones of one profile may each count their own minutes before syncing; the
  server's balance, summed over the profile's devices, is authoritative once both have synced. This
  is the same accepted divergence as FR-22.4.
- The phone's engine, the server's desired state and the console all compute "learning app" and
  "earned today" from the same inputs (prefix list + usage), as FR-3.10 already requires for the
  pause reason.

### 4.4 Daily time 0 is real

- A policy gets an explicit **daily limit on/off**. Migration: on where `daily_limit_minutes > 0`,
  off elsewhere, so every existing setting keeps its meaning (anti-goal 3).
- With the limit on, 0 minutes is valid: every counted app is paused once Extrazeit and Bonuszeit
  are spent, with reason *Tageszeit aufgebraucht* / quota. *Mehr Zeit erbitten* (FR-28.1) and the
  console's +15/+30/+60 (FR-3.11) work with daily time 0; they are gated on "limit on", no longer on
  "minutes > 0".
- API, `fgctl`, MCP and console all express "no limit" as the switch off, and 0 as 0. An old client
  that sends only `daily_limit_minutes: 0` keeps today's meaning (switch off), so the console and
  `fgctl` of 0.6.44 do not lock a phone by accident. The explicit switch is the **only** way to say
  "on, 0 minutes"; a request with minutes > 0 and no switch turns it on, as today. *My call
  (compatibility over purity; builder check Q4).*

### 4.5 What the child and the parents see

- Phone, Heute screen: Bonuszeit shows earned learning minutes as part of its balance, with one line
  for today: *Heute erspielt: 12 min*. The "what is left now" number (FR-28.3) includes them. A
  paused counted app's blocked screen says *Keine Zeit mehr — spiel ein Lernspiel, um Zeit zu
  verdienen* while learning apps exist on the phone.
- Console, Übersicht and Aktivität: today's earned learning minutes appear inside the gold Bonuszeit
  figure, with *davon erspielt* as its own amount. Apps page: each learning app reads *Lern-App ·
  verdient Zeit*, and its rule control is replaced by that label plus *Sperren*.
- Family settings: the learning prefixes, with the number of installed apps each one matches.

### 4.6 Release

- Server and phone change together: new policy fields, new desired-state fields, new engine logic.
  Server first (it must answer old phones correctly: an old phone ignores the new fields, and with
  no prefix set nothing changes), then the DPC as **0.6.45, versionCode 54**, through the existing
  self-update path, on both family phones.
- Only after both phones report 0.6.45: set the family's learning prefix, then daily time 0 (limit
  on) for both children. Before that, a 0.6.44 phone would read 0 as "no limit".
- Requirement **FR-30** in `REQUIREMENTS.md`, Phase 60 in `IMPLEMENTATION_PLAN.md`; DEPLOYMENT.md
  says how to set the prefix.

## 5. Verification that has to exist

- Engine unit tests (phone and server) for: learning minutes credited per day across apps; credit
  idempotent under repeated cumulative reports; blocked learning app earns nothing; learning app
  free with balance 0 and daily time 0; counted app paused at 0 and opened by earned minutes; spend
  oldest first across a task credit and a learning credit; a credit dropped after its seventh day;
  limit-off vs limit-on-0 distinguished; migration keeps `0` = off.
- e2e through the real server and a real browser: set the prefix, report learning usage, see the
  balance and *davon erspielt* in the console; an API key may read, not set, the prefix.
- On a real emulator with the real DPC: daily time 0, a stand-in learning app under
  `org.example.learn.` opened for a few minutes, the stand-in media app goes from paused to usable
  within one poll **with the network cut**, and pauses again once the minutes are watched.
- Each test calibrated by breaking the rule it guards (house rule).

## 6. Left to the builder

- Names of fields, endpoints, tables and the console wording beyond the German strings above.
- Whether the per-device learning figure is a column on the usage rows or its own table, as long as
  §4.2's idempotence and no-retroactivity hold.
- How the phone keeps "its own count for today" across a reboot (it already persists day totals).

## 7. Risks

- R1. **A learning app left open earns time.** Measurement stops when the screen goes off, but a
  phone with "Stay awake" on a charger keeps it on (measured 2026-09-23 for the home screen, FR-3.8).
  Accepted for now; a cap or an inactivity rule is a follow-up if it happens.
- R2. Whoever controls the apps under the prefix controls how time is earned. Here that is the
  owner's own namespace; a family that sets someone else's prefix hands that publisher the key.
