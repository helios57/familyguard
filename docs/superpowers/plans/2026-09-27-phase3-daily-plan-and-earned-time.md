# Phase 3 — Daily plan and earned time (Bonuszeit): Implementation Plan

> **For agentic workers:** executed inline by its author (superpowers:executing-plans), as the owner
> asked. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A profile has a daily plan of task groups; the child reports a task done on the phone; a
parent or guardian confirms it; a group whose tasks are all confirmed earns gold **Bonuszeit** — a
7-day, oldest-first balance, spent automatically on any app once the daily budget is used or bedtime
has begun, and always on the apps marked as bonus apps, which run on nothing else.

**Spec:** `docs/superpowers/specs/2026-09-27-daily-plan-design.md` §5, §6, §7; FR-22; owner rulings D2–D8.

## The accounting — decided here, because it decides everything else

A minute is paid from earned time when it is a **bonus app**, or falls **inside bedtime**, or comes
**after the daily budget is spent**; otherwise from the budget. Always-free, preinstalled-free,
critical and uncounted apps never cost earned time. That is a statement about *when* a minute was
used, so it cannot be recovered from day totals: a child who spends the budget on a game in the
morning and chats on WhatsApp (always usable, but counted) in the evening would be charged gold for
the chat by any formula over totals.

**So the phone attributes each measured window at the moment it measures it** — the same ~5-minute
windows `UsageTracker` already produces — using the state in force for that window:

- `EarnedAttribution.attribute(window, ctx) -> Map<pkg, goldMs>`, pure and unit-tested:
  uncounted and exempt (allowed, free-by-default, critical) → 0; bonus → all; governed in bedtime →
  all; governed otherwise → whatever of the window's counted time exceeds the budget left, shared
  among the governed apps in proportion, capped at their total.
- A second ledger (`earned` ms per day per package) next to the usage ledger, cumulative like it,
  reported with the day totals (`earned_ms`), merged on the server with `GREATEST`.

Both engines then stay pure and take two numbers:

- `used_minutes_today` becomes counted **minus** earned (a gold-paid minute is not also a budget
  minute), on the server and in the phone's offline recount;
- `earned_spent_minutes_today`, and `settings.earned_available_minutes` — the server's balance at
  the start of today plus today's credits.

The server's balance: a day-by-day simulation over the last 7 days of credits and of each day's
reported spend (all devices of the profile), oldest credit first, credits dropped after their 7th
day, an overdraft carried as a debt the next credit settles. Two phones can spend the same gold
before they sync (accepted in the spec).

## Engine (both, shared vectors)

- `gold = earned_available_minutes − earned_spent_minutes_today`.
- Pause and a parent's block win as before.
- If the reason would be BEDTIME or QUOTA and `gold > 0`: nothing is suspended for it; the state
  reports `suspend_reason = ""` and `earned_active = "BEDTIME" | "QUOTA"` (what gold is covering now).
- Bonus apps (app rule `BONUS`): suspended when `gold ≤ 0`, whatever the time — with the block
  reason `EARNED` ("nur mit Bonuszeit"); usable when `gold > 0` even in bedtime or at the limit.
- New outputs: `earned_minutes_left` (may be negative: a debt), `earned_spent_minutes`, `earned_active`.

## Server

- Migration 0019: `plan_groups`, `plan_tasks` (soft-deleted, so history keeps its names),
  `task_days`, `earned_credits`, `usage_samples.earned_ms`, `app_rules` action `BONUS`.
- `PUT /children/:id/plan` (admins) replaces the plan as one document, keeping ids;
  `GET /children/:id/plan`.
- `GET /children/:id/today` (everyone): today's groups, tasks and their states, the balance and its
  credits, today's spend.
- `POST /children/:id/tasks/:task/decision {"decision": "confirm"|"reject"|"undo"}` (everyone):
  confirming the last open task of a group creates the day's credit; undoing withdraws it.
- Device: `today` block in `GET /device/policy`; `POST /device/tasks/:task/report` — only a task of
  this device's profile, of a group that runs today, inside its window; audited, notifies parents.
- Audit: `PLAN_UPDATED`, `TASK_REPORTED` (device), `TASK_CONFIRMED`, `TASK_REJECTED`, `TASK_UNDONE`,
  `EARNED_TIME_CREDITED`.

## Phone

- Heute screen: the groups of today with their window, each task with *Fertig* (or its state:
  gemeldet · bestätigt · nicht erledigt), and the gold Bonuszeit with what expires when.
- Reporting needs the server; without it the button says so.
- Notice text while gold is being spent: *"Bonuszeit läuft — noch 25 min"*.

## Console

- Admin (Rules tab): the plan editor — groups (title, weekdays, from–to, minutes) and their tasks.
- Admin (Apps tab): a fifth answer, *Bonus app*.
- Guardian window: *Wartet auf dich* (reported tasks with Bestätigen / Nicht erledigt), each
  card's tasks for today with a confirm button, and the gold balance.

## CLI / MCP

`fgctl plan <child> [--set file.json]`, `fgctl today <child>`, `fgctl confirm|reject <child> <task>`;
MCP `get_today`, `decide_task`, `get_plan`, `set_plan`.

## Tests

Shared vectors for every row of the engine rules; Go tests for the balance simulation (FIFO, expiry,
withdrawal, debt, DST); Kotlin tests for the attribution; e2e for the plan document, the report window,
confirm → credit → a bonus app usable at the limit, undo → debt, the audit rows; Chrome for the
editor and the guardian's confirmations; the emulator for a bonus app suspended at zero gold and
usable with gold, read from the package manager.

## Release

0.6.20; the phase-1 recipe; the family phone once it is awake and charged.
