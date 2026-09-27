# Daily plan, earned time, roles, alarm and agenda — design

Status: **built**, all six phases, 2026-09-27 — 0.6.18 (roles) to 0.6.24 (calendar import); see
`IMPLEMENTATION_PLAN.md` Phases 34–39 and FR-20 … FR-25 in `REQUIREMENTS.md`. Still to measure on the
family phone by its owner: the alarm ringing after the phone has lain unused, over a PIN lock screen.

This extends FamilyGuard from "what may not be used" to "what the day holds": a child sees the
day's tasks, the next alarm and what is on today; finishing a group of tasks, confirmed by a parent,
earns time; and a second, smaller console window lets a guardian do the four things that come up
during a day without touching the rules.

Requirement numbers below are proposals; each is entered in `REQUIREMENTS.md` when its phase
starts, after checking the number is still free (FR-19 is the highest in use today).

---

## 1. What was asked, and what was decided

The family asked for, in their words (translated):

- an **alarm clock** per child;
- a **list of tasks per day**, grouped, each group with a start and an end time — e.g. *07:00–20:00:
  feed the cat, practise piano/violin (10 min), homework (15 min+)*, then *30 minutes of film time*;
  *20:00–21:30: brush teeth, pyjamas*, then *+15 minutes*;
- **specific apps unlocked (plus time) once a group is done**;
- a display of **what is on** — school, or whatever else is coming up — alongside or instead of the
  calendar, where some entries are optional;
- everything **configurable per child**, in the console and through the CLI/MCP;
- a **separate console window for a guardian** with: lock/unlock the whole phone (except
  communication apps), add time, reduce time, confirm that tasks were done;
- a **new role** for that window, alongside admin, also signing in with Google, and an **admin
  screen for rights**;
- the same app on an **adult's phone** (ad filter, remote debugging, a YouTube limit), whose owner is
  herself a guardian.

Decisions taken with the owner while designing:

| # | Decision |
|---|---|
| D1 | **Confirming a task needs the server.** The child reports a task done on the phone; it counts only once a parent confirms it. There is no offline confirmation. |
| D2 | **Earned time is a balance, called Bonuszeit**, shown in gold and separately from the daily budget. It is *in addition to* the daily budget. |
| D3 | **Earned time lasts 7 days** from the day it was earned, and does not expire at the end of the group's window. Oldest credit is spent first. |
| D4 | **Earned time is universal**: any app may run on it once the daily budget is spent. |
| D5 | **Bonus apps** (e.g. the film apps) are marked per app. They run **only** on earned time, never on the daily budget. |
| D6 | **Earned time beats bedtime.** |
| D7 | **Earned time is spent automatically** — no "start" button. When the daily budget is spent, when bedtime begins, or whenever a bonus app is in front, each minute is taken from the balance while any is left. |
| D8 | **The guardian's pause beats everything**, earned time included. A parent's block on a single app also stays in force. |
| D9 | The existing `GUARDIAN` role becomes that role, **restricted to an allowlist**. Admins keep everything. |
| D10 | A guardian **may act on her own profile** (e.g. give herself time). Accepted for now; every such act is audited under her name. |
| D11 | A task's duration ("10 min") is **text**, not a timer: a parent confirms anyway. |
| D12 | **No push notification** to the guardian in the first phases; the guardian window shows what is waiting when it is opened. |
| D13 | Alarm and agenda as proposed (sections 8 and 9). |

**Precedence, highest first:** guardian pause → parent's block on one app (or an app waiting for
approval) → earned time → bedtime → daily budget. Critical packages (dialer, SMS, launcher, settings, keyboards — FR-5.5) are
never suspended by any of them.

---

## 2. Roles and rights (proposed FR-20)

### What exists today

`parents.role` is one of `PRIMARY_ADMIN`, `ADMIN`, `GUARDIAN`. **The role is enforced on 7 of the 48
parent routes**; the other 41 — creating and deleting children, changing policy, app rules, devices,
commands — accept any parent. A `GUARDIAN` can therefore do almost everything an admin can. The
console always adds a new parent as `ADMIN` and offers no way to choose or change a role.

### What changes

| Role | May |
|---|---|
| `PRIMARY_ADMIN` | everything, including adding/removing people, changing roles, and API keys |
| `ADMIN` | everything except people, roles and API keys |
| `GUARDIAN` | only the guardian window (section 6): read profiles and today's status; pause/unpause; adjust today's time; confirm/reject reported tasks |

- **Every route declares its roles explicitly.** A route registered without a declaration fails a
  test that walks the router, so a future endpoint cannot be left open by omission. This replaces
  "gate the dangerous ones" with "allow the listed ones".
- **People & rights screen** (Family tab, primary admin only): add a person with a role, change a
  person's role, remove a person. The last `PRIMARY_ADMIN` cannot be demoted or removed. Every change
  is audited.
- An **API key is its parent** (FR-17), so a guardian's key gets exactly the guardian's rights.
- **Before rollout**, the live family's parent rows are read (read-only) to see who is `GUARDIAN`
  today, because this change takes rights away from any such account.

---

## 3. Profiles — a managed person need not be a child

The data model calls a managed person a *child*. An adult's phone (ad filter, remote debugging, a
YouTube limit) fits the same model unchanged: a profile with a policy and devices. The console and
the phone say **"profile"** where they now say "child", so an adult is not listed as one. Nothing
else is special about an adult's profile: tasks, alarm and agenda are optional for every profile.

Practical note, not a design choice: FamilyGuard must be **Device Owner** to suspend apps, and
Android only allows that on a phone that has just been reset, or has no accounts on it. Putting it
on an adult's existing phone means a factory reset, or removing every account before provisioning.

---

## 4. Pause and today's time (proposed FR-21)

### Pause ("Handy sperren")

- A **state**, not a command: `policies.paused` (+ who and when). Leaving it is the same function
  giving a different answer (CONCEPT §2.3), so it survives reboots, a lost event and an offline
  phone, and ends only when someone unpauses.
- While paused, everything is suspended **except** the critical packages and the family's
  **communication apps** — a list the admin keeps (e.g. phone, messages, a messenger), family-wide
  because the same messenger is used by everyone.
- Named *pause* in code and UI to keep it apart from the existing **Lock** command, which locks the
  screen once (`lockNow`) and changes no app.

### Add and reduce today's time

- The existing "extra time today" (FR-3.11, table `bonus_minutes`, positive only) becomes a **signed
  adjustment** for the day: `+15/+30/+60` and `−15/−30`. The effective daily budget is
  `max(0, limit + adjustments)`. Reducing below what was already used pauses immediately.
- It adjusts the **daily budget only**. Earned time changes only through tasks (and, for
  corrections, through the admin API).
- Every adjustment is audited with its sign and author. The name `bonus_minutes` is retired in code
  so that "bonus" means one thing: earned time.

---

## 5. Daily plan and earned time (proposed FR-22)

### The plan

Per profile:

- **Groups**: title, weekdays, start and end time (in the policy's timezone), minutes earned.
  Example: *Tag*, Mon–Sun, 07:00–20:00, 30 min; *Abend*, Mon–Sun, 20:00–21:30, 15 min.
- **Tasks** in a group: title, optional note ("10 min"), order.
- **Bonus apps**: an app rule `BONUS`, next to `ALLOW`, `BLOCK` and `LIMIT`.

### A task's day

```
open ──child taps "Fertig"──▶ reported ──parent confirms──▶ confirmed
  ▲                            │                               │
  └────────parent rejects──────┘◀────────parent undoes─────────┘
```

- The child can report a task **during its group's window**. A parent can confirm or reject until
  the end of that day, and can confirm a task nobody reported (the child forgot to tap).
- Reporting needs a connection (D1). Without one the phone says so — "Keine Verbindung — später
  nochmal" — rather than pretending it was sent.
- **A group is done when all its tasks for that day are confirmed.** That creates exactly one
  **credit** for (profile, group, day). Undoing a confirmation withdraws the credit; if part of it was
  already spent, the balance goes negative and the next credit settles it.

### Earned time (Bonuszeit)

- **Credits**: minutes, earned on day *D*, usable through day *D+6* (earned Monday → last usable
  Sunday), in the profile's timezone.
- **Spending** is decided minute by minute from what the phone already records (FR-3.7 sittings).
  A minute is paid from earned time when any of these holds, and from the daily budget otherwise:
  - the app is a **bonus app**;
  - it falls **inside bedtime**;
  - the **daily budget is already spent**.

  Uncounted time (home screen, System UI, FamilyGuard — FR-3.8) is never paid for at all.
- **Balance** = unexpired credits − minutes spent, oldest credit first. The server keeps the ledger
  and sends the phone the balance at the start of the day, the credits earned today, and each
  credit's expiry (for display). The phone subtracts today's spending itself, so the balance runs out
  correctly offline.
- **Two devices, one balance.** Both phones spend from the same balance until they next sync, so it
  can briefly go negative. The next credit settles it. Accepted: exactness here would need the
  server to meter every minute live, which an offline phone cannot do.

### Enforcement

The engine (`EnforcementEngine.kt` and the Go resolver, held together by the shared vectors) gains
three inputs — `paused` and the communication packages, bonus apps, and the earned-time balance —
and applies the precedence in section 1:

| Situation | Normal apps | Bonus apps |
|---|---|---|
| paused | suspended (except critical + communication) | suspended |
| app blocked by a parent | suspended | suspended |
| balance > 0, daily budget left, not bedtime | run on budget | run on earned time |
| balance > 0, budget spent **or** bedtime | run on earned time | run on earned time |
| balance ≤ 0, budget left, not bedtime | run on budget | suspended |
| balance ≤ 0, budget spent **or** bedtime | suspended (as today) | suspended |

Per-app limits (FR-5.8 `LIMIT`) still apply on top: earned time extends the day, not one app's cap.
Always-free apps (FR-5.8 `ALLOW`, FR-5.10 preinstalled) stay free and never cost earned time; a pause
suspends them too. "Parent's block" includes an app waiting for approval (FR-5.4).

### Saying so (FR-3.10 continued)

- Because spending is automatic (D7), the phone **says when it starts**: the notification that today
  reads "daily limit reached" becomes *"Bonuszeit läuft — noch 25 min"* while earned time is being
  spent. The support line on the blocked screen names the empty balance when that is the reason.
- Earned time is **gold** everywhere — phone and console — and always shown **apart from** the daily
  budget: *"Tageszeit 45 / 60 min · Bonuszeit 50 min (20 min gültig bis So)"*.

---

## 6. The guardian window

A page of its own in the console, laid out for a phone and installable to the home screen. A
`GUARDIAN` lands there and sees nothing else. An admin sees it as the first tab and still has the
full console.

1. **Waiting for you**: reported tasks, newest first, each with *Bestätigen* / *Nicht erledigt*, and a
   *Gruppe bestätigen* shortcut.
2. **One card per profile**: state in one line (free · 45 / 60 min · Bettzeit · paused), earned-time
   balance in gold, and the buttons **Sperren / Entsperren** and **−15 · +15 · +30**.

Pausing asks for a second tap, because it is the one button that takes a phone away. Every action
answers with the phone's state once the phone has applied it, not when the server accepted it
(NFR-3).

---

## 7. The child's phone: the Today screen

FamilyGuard's own screen becomes the child's day, top to bottom:

1. the **next alarm**;
2. **now / next** from the agenda;
3. the day's **groups** — window, tasks, *Fertig* per task, state (offen · wartet · bestätigt);
4. **earned time** in gold, with what expires when;
5. the existing screen-time list (FR-3.10).

The recovery screen stays where it is; this does not change how a parent recovers a phone.

---

## 8. Alarm (proposed FR-23)

- Per profile: a time per weekday, or off; option *not during holidays* (section 9); a one-off
  override for the next day ("morgen 06:00", "morgen aus") from the console or API.
- The schedule lives in the policy, so the phone **rings with no connection**. It books the next
  occurrence with `AlarmManager.setAlarmClock` — the one alarm type the platform promises to deliver
  on time through Doze — and re-books after a ring, a reboot, a policy change and a timezone change.
- Ringing: a full-screen alarm with sound on the alarm stream, reusing the Ring command's
  `Siren`. The child can **stop** it or **snooze 5 minutes**; the schedule can only be changed by a
  parent.
- **To be measured on the family phone before it is called done**, because this project has already
  found that Doze defers other alarms silently: that it rings with the screen off after an hour in a
  pocket, and that the full-screen alarm shows on the lock screen for a sideloaded Device Owner app
  on Android 16 (`USE_EXACT_ALARM`, `USE_FULL_SCREEN_INTENT`).

---

## 9. Agenda (proposed FR-24)

- Per profile: **recurring** entries (weekdays, from–to, title, place) — school, training — and
  **single** entries (date, time, title). Any entry can be **optional**, and is shown as such.
- **Holidays**: family-wide date ranges during which recurring entries — and alarms marked *not
  during holidays* — do not apply.
- Shown on the phone as today and tomorrow, and in the console per profile as a week.
- **Later, optional**: read an existing calendar (ICS URL) read-only as an extra source. Not in the
  first version; nothing in the model prevents it.

---

## 10. API, CLI and MCP

Everything is an API endpoint first (FR-17), then an `fgctl` subcommand and an MCP tool with the same
authorization: plan (get/set groups, tasks), reported tasks (list, confirm, reject, undo), earned time
(balance, credits, admin correction), pause/unpause, today's adjustment, bonus-app rules, alarm
(schedule, override), agenda (entries, holidays), people and roles. The device gets one new write:
report a task, authenticated as the device and scoped to its own profile (NFR-2).

## 11. Data model (new or changed)

| Table / column | Purpose |
|---|---|
| `policies.paused`, `paused_at`, `paused_by` | pause state |
| `family_communication_packages` | apps a pause leaves running |
| `bonus_minutes` → signed day adjustment | add/reduce today's budget |
| `app_rules.action` + `BONUS` | bonus apps |
| `plan_groups`, `plan_tasks` | the plan |
| `task_days` (profile, day, task, state, reported_at, decided_by, decided_at) | a task's day |
| `earned_credits` (profile, group, day, minutes, expires_on, confirmed_by) | credits |
| `earned_spend_days` (profile, day, minutes) | spending per day, derived from sittings |
| `alarms`, `alarm_overrides` | alarm |
| `agenda_entries`, `holidays` | agenda |

## 12. Testing

The project's rule applies unchanged: every control is shown red on a known-bad input before its
green counts, against the real artefact rather than a mock.

- **Engine**: new shared vectors for every row of the table in section 5 and for the precedence
  (pause beats earned time; earned time beats bedtime; a parent's block beats earned time; bonus app
  at zero balance; critical and communication apps under pause), run by both engines.
- **Ledger**: Go unit tests for FIFO spending, 7-day expiry, a withdrawn credit, negative balance,
  and the two DST days.
- **Roles**: a matrix test that calls **every** route as each role and asserts the allowlist, plus
  the router walk that fails on an undeclared route; e2e against a real server for the guardian's
  four actions and a sample of refusals.
- **Browser**: the guardian window in real Chrome at phone width — confirm a task, see the gold
  balance change, pause and see the phone's state come back.
- **Phone**: unit tests for the next-alarm calculation (weekday, override, holiday, DST), the Today
  screen's states; instrumentation on the emulator for a report reaching the server.
- **On the family phone**: report → confirm → a bonus app opens and the balance falls; bedtime with
  earned time left; pause leaves the dialer and the messenger; the alarm rings with the screen off.

## 13. Phases

Each phase ships on its own and is verified on the family phone before the next starts.

1. **Roles and rights** — the allowlist, the router test, the people & rights screen. First because
   it closes the open `GUARDIAN` rights and the adult's phone depends on it.
2. **Pause and today's time** — pause, signed adjustment, the guardian window without tasks.
3. **Daily plan and earned time** — plan, reporting, confirming, credits, ledger, bonus apps, the
   engine change, the Today screen.
4. **Alarm.**
5. **Agenda.**
6. *(optional)* Calendar import.

## 14. Not in this design

Push notifications to the guardian (D12); timers for tasks (D11); rewards other than time; tasks
that differ by date rather than by weekday; photo proof of a task. Each can be added later without
changing what is built here.

## 15. Risks and open points

- **Full-screen alarm and exact alarm permission** on Android 16 for a sideloaded Device Owner app
  are not yet measured (section 8). If the full-screen alarm is refused, the fallback is a
  high-priority notification with the alarm sound, which rings but does not take over the screen.
- **Automatic spending** (D7) means a child can spend earned time without meaning to; the
  notification in section 5 is the mitigation, and the owner chose this knowingly.
- **Earned time beats bedtime** (D6): a child with a balance can use the phone at night until the
  balance is gone. Pause is the parent's override.
- **A guardian acting on her own profile** (D10) is accepted for now; tightening it later is a rule
  in the allowlist, not a redesign.
- **Restricting `GUARDIAN`** takes rights away from any existing guardian account; checked before
  rollout (section 2).
