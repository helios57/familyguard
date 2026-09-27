# Phase 5 — Agenda and holidays: Implementation Plan

> **For agentic workers:** executed inline by its author (superpowers:executing-plans), as the owner
> asked ("Finish all planned phases", 2026-09-27). Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A profile has an agenda — recurring entries (school, training) and single ones (a dentist
appointment), any of them optional — and the family has holidays, during which recurring entries and,
where a profile asks for it, the alarm do not apply. The phone shows now / next, today and tomorrow;
the console shows a week.

**Spec:** `docs/superpowers/specs/2026-09-27-daily-plan-design.md` §7 (now / next on the Today
screen), §8 (*not during holidays*), §9, §10; FR-24.

## Decisions

- **The agenda is a document per profile**, replaced as a whole like the plan: an entry sent with its
  id is edited in place, one without is new, one left out is retired (never deleted — the family's
  records are kept). An entry is *recurring* (weekdays bit set, Monday = 1, from–to) or *single* (a
  date, from–to); both have a title, an optional place and an *optional* flag.
- **Holidays are family-wide** date ranges with a title, also a document. A holiday suppresses
  recurring entries only; a single entry on a holiday still happens (someone put it there on purpose).
- **The server expands**, the phone displays. `GET /children/:id/agenda/days?from=&days=` answers
  each day's items in order, with the holiday that applies. The phone is sent today and tomorrow in
  the profile's calendar beside its policy, and picks the one whose date is today on its own clock, so
  a phone that stayed offline past midnight shows the right day.
- **The alarm's *not during holidays*** is a flag on the profile's alarm. The phone is sent the
  holidays from today on with the alarm rule and skips a holiday's date unless a date change was set
  for it — an explicit change for one date always wins.
- **Now / next** is decided on the phone from the day's items and its clock (pure, tested).
- Roles: admins read and write both; the guardian window does not show them.

## Server

- Migration 0021: `agenda_entries (id, child_id, kind, title, place, optional, weekdays, day,
  starts_at, ends_at, position, retired_at)`, `holidays (id, family_id, title, starts_on, ends_on,
  retired_at)`, `alarm_settings (child_id, skip_holidays)`.
- `GET|PUT /children/:id/agenda`, `GET /children/:id/agenda/days`, `GET|PUT /family/holidays`;
  `skip_holidays` on `GET|PUT /children/:id/alarm`.
- Device: `agenda` block (`days`: today and tomorrow) and `alarm.skip_holidays` + `alarm.holidays`.
- Audit: `AGENDA_UPDATED`, `HOLIDAYS_UPDATED`; the alarm's flag rides `ALARM_UPDATED`.

## Phone

- `agenda/AgendaDays` (the block), `AgendaNow.of(day, now)` → current and next item, the Heute
  screen's *Jetzt*, *Danach*, the rest of today and tomorrow, *(freiwillig)* on optional items.
- `NextAlarm` gains holidays: a date inside a holiday is skipped when `skip_holidays`, unless changed.

## Console, CLI, MCP

- Rules tab: an *Agenda* card — entries (title, place, repeating on days or on one date, from–to,
  optional) saved as one document, and the week from today below it.
- Family tab: *Holidays* (title, first and last day) saved as one document.
- Alarm card: *Not during holidays*.
- `fgctl agenda <child> [--set file]`, `fgctl week <child>`, `fgctl holidays [--set file]`; alarm
  `--set` accepts `skip_holidays`. MCP `get_agenda`, `set_agenda`, `get_week`, `get_holidays`,
  `set_holidays`; `set_alarm` takes `skip_holidays`.

## Tests

- Go/e2e: the agenda document and its checks and ids; the expansion — a recurring entry on its
  weekdays only, a single entry on its date, a holiday suppressing the recurring but not the single,
  order by time; the device block; holidays document and checks; the alarm flag and the holidays sent
  to the phone; audit; a guardian refused.
- Kotlin unit: `AgendaNowTest` (before the first item, inside one, between, after the last, the
  offline phone picking the right day), `NextAlarmTest` + holidays (skipped, not skipped without the
  flag, a date change on a holiday rings).
- Chrome at 360 px: an admin adds a recurring and a single entry and sees them in the week; a holiday
  in the Family tab empties the recurring day; *Not during holidays* stored; layout guard.
- fgctl/MCP against the real binary.
- Emulator: the Heute screen shows *Jetzt* / *Danach* and tomorrow's items from a real server
  (`tests/android/agenda.sh`).

## Release

0.6.23, server first.
