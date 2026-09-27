# Phase 4 — Alarm: Implementation Plan

> **For agentic workers:** executed inline by its author (superpowers:executing-plans), as the owner
> asked ("Finish all planned phases", 2026-09-27). Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A profile has an alarm clock — a time per weekday, or off, and a change for one date
("morgen 06:00", "morgen aus"). The phone rings on time with no connection and through Doze, full
screen, with Stop and Snooze; only a parent changes the schedule.

**Spec:** `docs/superpowers/specs/2026-09-27-daily-plan-design.md` §7 (the next alarm on the Today
screen), §8, §10, §12; FR-23.

## Decisions

- **The schedule is data the phone computes from, not a list of instants the server books.** The
  phone must ring offline, across a timezone change and across DST, so it holds the rule and computes
  the next occurrence itself (`NextAlarm`, pure, unit-tested). The server never computes an instant.
- **A date change is an override**: a time, or off, for one calendar day in the profile's timezone.
  Today up to 60 days ahead; a past day is refused. Removing it returns the day to the schedule.
- **Holidays are phase 5.** Nothing here reads them; phase 5 adds the flag *not during holidays* and
  the phone's rule gains one input.
- **Booked with `AlarmManager.setAlarmClock`** — the one alarm the platform promises to deliver on
  time in Doze, and the one that shows the next alarm in the status bar and on the lock screen. It is
  re-booked after a ring, a boot, an app update, a policy change, a time or timezone change.
- **Ringing** is a foreground service playing the alarm ringtone on the alarm stream, with vibration,
  and a notification whose full-screen intent opens the alarm screen on the lock screen. *Stop* ends
  it; *Schlummern* rings again in 5 minutes; it stops itself after 10 minutes. The alarm volume has a
  floor of half the stream while it rings (a child cannot mute an alarm clock by sliding it to zero)
  and is restored after — the Siren's measured rule that an unread volume is never "restored" as 0.
- **If the full-screen intent is refused** (`NotificationManager.canUseFullScreenIntent()` false),
  the ring still happens as a high-priority notification with the sound; the phone reports which of
  the two it has, so the console can say so (the spec's stated fallback, §15).
- Roles: admins read and write the schedule and the overrides. The guardian window does not show it.

## Server

- Migration 0020: `alarm_times (child_id, weekday 1..7 Monday first, time HH:MM)`, absent = off;
  `alarm_overrides (child_id, day, time NULL = off)`.
- `GET /children/:id/alarm` → `{"weekdays": ["06:30", …7, "" = off], "overrides": [{"day", "time"|null}]}`
  (today onward); `PUT /children/:id/alarm {"weekdays": [...]}`;
  `PUT /children/:id/alarm/days/:day {"time": "06:00"|null}`; `DELETE /children/:id/alarm/days/:day`.
- Device: `alarm` block beside `today` in `GET /device/policy` — weekdays, overrides from today, and
  the timezone.
- Audit: `ALARM_UPDATED`, `ALARM_DAY_SET`, `ALARM_DAY_CLEARED`. `notifyChild` after each, so the
  phone re-books within seconds.

## Phone

- `alarm/AlarmSchedule` (the block), `NextAlarm.next(schedule, now, zone)`, `EncryptedAlarmStore`,
  `AlarmClockBooker` (setAlarmClock + the snooze), `AlarmFireReceiver`, `AlarmRingService`,
  `AlarmActivity` (show when locked, turn screen on), `TimeChangeReceiver` (TIME_SET,
  TIMEZONE_CHANGED, MY_PACKAGE_REPLACED); BootReceiver re-books.
- The Heute screen's first line: *"Wecker: morgen 06:30"* / *"Wecker: heute 06:30"* / none.
- The device state reports `alarm_full_screen` (true/false/null) with the heartbeat.

## Console, CLI, MCP

- Rules tab: an *Alarm* card — seven rows (day, time, on/off), *Save alarm*; *Tomorrow* with
  as scheduled / off / a time; the upcoming date changes with *Remove*.
- `fgctl alarm <child> [--set file.json]`, `fgctl alarm-day <child> <YYYY-MM-DD|tomorrow> <HH:MM|off|clear>`;
  MCP `get_alarm`, `set_alarm`, `set_alarm_day`.

## Tests

- Kotlin unit: `NextAlarmTest` (weekday; today's time passed → next day; an override's time and
  off; an override in the past ignored; all off → none; DST spring gap and autumn repeat), the booker
  (snooze beats a later scheduled alarm, a stop clears the snooze), the ringer's volume floor and
  restore, Today's alarm line.
- e2e: the schedule document and its checks; a date change today and in 60 days, a past day and day
  61 refused; clear; the device block; the audit rows; a guardian refused.
- Chrome at 360 px: the Alarm card saves Monday–Friday 06:30 and *morgen aus*, read back from the
  server; layout guard.
- fgctl/MCP against the real binary.
- Emulator (`tests/android/alarm.sh`): an alarm two minutes ahead is booked as an alarm clock
  (`dumpsys alarm`); with Wi-Fi and data off and the device forced into Doze it rings at the minute —
  the ring service running and an alarm-usage player active (`dumpsys audio`); *Stop* silences it and
  the next occurrence is booked.
- **On the family phone, by its owner:** the alarm rings with the screen off after the phone has lain
  unused, and shows over the lock screen. Recorded in Phase 37.3 as measured or not measured.

## Release

0.6.21, server first.
