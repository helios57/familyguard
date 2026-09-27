# Phase 6 — Calendar import: Implementation Plan

> **For agentic workers:** executed inline by its author (superpowers:executing-plans), as the owner
> asked ("Finish all planned phases", 2026-09-27). Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A profile's agenda can include an existing calendar, read-only: an ICS address (Google's
"secret address in iCal format", a school's published timetable) whose events appear in the week, on
the phone's Heute screen and in the CLI beside the agenda's own entries.

**Spec:** `docs/superpowers/specs/2026-09-27-daily-plan-design.md` §9 (*"Later, optional: read an
existing calendar (ICS URL) read-only as an extra source … nothing in the model prevents it"*); FR-25.

## Decisions

- **Read-only, server-side.** The server fetches the calendar; the phone never sees the address. An
  ICS address is a credential — whoever holds a Google secret address reads the calendar — so it is
  stored, shown to admins who set it, and never logged or audited in full (the audit carries the
  host).
- **Parsed with `github.com/emersion/go-ical`**, whose recurrence expansion is built on `rrule-go`:
  RRULE, RDATE and EXDATE, TZID, all-day events. A hand-written RRULE engine is exactly the kind of
  code that is right on the day it is written and wrong on the first calendar that uses BYSETPOS.
- **Events are items like the agenda's**, with `source: "calendar"`; an all-day event is
  `all_day: true`. A holiday does not hide them: a calendar event is on its date on purpose, like a
  single entry. An event spanning days appears on each day it touches.
- **Freshness from reads, not a scheduler.** The raw calendar is kept with the time it was fetched.
  Setting the address fetches it at once and refuses one that cannot be read or parsed. A read that
  finds it older than `CALENDAR_MAX_AGE` (default 30 minutes) answers from what is kept and fetches
  in the background, one fetch per profile at a time; a failed fetch keeps the last good copy and
  records its error, which the console shows.
- **The fetch is fenced.** `https://` or `webcal://` (read as https), at most 1 MiB, 10 s, at most
  three redirects, and no private, loopback or link-local address — checked on the address actually
  dialled, so a redirect or a DNS answer cannot lead it into the cluster. `CALENDAR_ALLOW_LOCAL=true`
  lifts the address fence and allows `http://`, for a bench: the e2e suite serves its calendars
  from localhost, and a second harness without the flag proves the fence refuses them.
- Roles: admins only, like the agenda.

## Server

- Migration 0022: `calendar_sources (child_id, url, body, fetched_at, last_error, last_attempt_at)`.
- `GET|PUT|DELETE /children/:id/calendar`: `{"url", "fetched_at", "error", "events"}`.
- `internal/agenda`: calendar occurrences merged into `Expand` for the requested days in the
  profile's zone.
- Audit `CALENDAR_SET`, `CALENDAR_REMOVED` (detail: host only).

## Phone

- `AgendaItem.allDay`, `.source`; now / next skip all-day items; the Heute screen lists today's
  all-day items (*Heute: Schulreise (ganztägig)*) and marks them in tomorrow's list.

## Console, CLI, MCP

- The Agenda card: *Calendar (optional)* — the address, *Save*, *Remove*, and what the last read
  found (n events, when; or the error). The week marks calendar items.
- `fgctl calendar <child> [--set URL | --remove]`; MCP `get_calendar`, `set_calendar`.

## Tests

- Go unit: a timed event in its TZID shown in the profile's zone; an all-day event; a weekly RRULE
  with an EXDATE; an event across midnight on both days; a holiday not hiding an event.
- e2e: set an address served by the test, read the week with the calendar's events beside the
  entries; change the served file and see the week follow once it is stale; a calendar that cannot be
  parsed refused; a failing fetch keeping the last good copy with its error; the device block
  carrying an event; remove; audit; a guardian refused; with the fence on, localhost refused.
- Chrome at 360 px: set the address, see its events marked in the week, remove it.
- fgctl/MCP; the emulator: the Heute screen shows an all-day calendar event
  (`tests/android/agenda.sh`, extended).

## Release

0.6.24, server first.
