# Console redesign — design

**Status:** approved 2026-09-30 (*"Continue till it's finished, don't wait all the time or ask me,
just do it but good"*) and built as 0.6.33 — IMPLEMENTATION_PLAN Phase 49; where the build departed
from this text, the plan's *Rulings* say so. The owner asked: *"I want the console to be
enhanced for mobile usage, the ux and ui needs massive enhancement. Also the desktop version need
enhancement. Optimize it"*. The owner's decision: the whole console in German (Swiss spelling, no ß,
24-hour times).

## 1. What the console is for, and what is wrong with it

Two parents and a guardian use it, mostly on a phone and mostly for something quick: give time,
see why apps are paused, find or ring a phone, allow an app that is waiting. A laptop is used for
the long jobs: the plan, the alarm week, the agenda, the app list.

A screenshot tour of every view at 360 px and 1440 px (`tests/e2e/tour_test.go`, 2026-09-30) found:

- **Overview (Home).** Each phone card is eleven grey buttons of equal weight; an unset phone shows
  all of them disabled. The state that matters — paused or not, time left — is in prose. The strip at
  the top repeats the cards below it. "Android Android 14".
- **Rules.** About 4000 px of scrolling on a phone: switches, bedtime, plan, alarm, one-date alarm,
  agenda, calendar, week, blocked websites and the ad filter in one column. Four separate Save
  buttons, while switches save at once. Times show as "07:00 AM".
- **Apps.** Every app carries a five-way control whose labels wrap onto two lines. The family
  blocklist runs its fields together ("suggestedNot installed on any phone h|ere.Second half").
- **Activity.** Repeats the screen-time bar and the time buttons from Home; a long amber paragraph
  above the chart; the location as bare coordinates.
- **Everywhere.** The guardian view is German and everything else English. Explanations are always
  expanded. On a laptop the cards sit in narrow columns beside empty space.

Success: on a 360 px phone, the common jobs (time, pause, find, ring, allow an app) take one or two
taps from opening the console, with no scrolling past things that are not asked for; on a laptop the
long jobs use the width. Every behaviour the console has today is still reachable.

## 2. Out of scope

No new features and no API changes, except where a view needs a field the API already has. No build
step and no framework: the files stay hand-written and served byte-for-byte (`console.go`). The CSP
stays as it is — no external map tiles, no inline style.

## 3. Structure

### 3.1 Navigation

Five destinations, the same at every width:

| tab | holds |
|---|---|
| **Übersicht** | every child's status, their phones, the quick actions, tasks waiting for confirmation |
| **Regeln** | per child, in three sub-tabs: *Zeit* (bedtime, daily limit, time zone, plan, alarm), *Schutz* (the switches, blocked websites, ad filter), *Agenda* (agenda, calendar, the week) |
| **Apps** | per child: waiting for approval, the app list, apps the family installs, the family blocklist |
| **Aktivität** | per child: the day's timeline and per-app bars, where the phone was, energy |
| **Familie** | people and rights, children, holidays, API keys, command line, signed in |

- **Phone (< 900 px):** a bottom tab bar, within thumb reach, with icon and label. The header holds
  the family name and the child switcher (the children as a segmented control, scrolling sideways
  when there are many). The drawer goes; nothing is hidden behind a menu.
- **Laptop (≥ 900 px):** a left sidebar with the five destinations and the children below them; the
  content is two columns where the view has two kinds of thing (Übersicht: children | phones;
  Regeln: the sub-tab's cards side by side; Aktivität: day | location and energy).
- A guardian sees Übersicht only, as today, with the actions a guardian holds.

Today's *Today* (guardian) and *Home* views merge into Übersicht.

### 3.2 Übersicht

Per child, a status card:

- the state in one word with its colour — *Frei*, *Pausiert — Tageslimit*, *Schlafenszeit*,
  *Gesperrt*, *Pausiert von dir* — and the reason under it in one line;
- time used against the limit as one bar, with earned time (Bonuszeit) in gold on the same bar;
- the three most used actions as primary buttons: **+15 Min** (a small menu for −15/+30/+60),
  **Pausieren/Fortsetzen**, **Live/Orten**;
- tasks waiting for confirmation, with Bestätigen/Ablehnen, when there are any.

Per phone, one row: status dot, name, battery, "vor 3 Min", and *ruht* when it rests. Tapping the row
opens the phone sheet (§3.6): **Orten · Klingeln · Sperren** first, then *Mehr* with Sync, App
aktualisieren, Handy ersetzen, Wiederherstellungscode. An unset phone's row offers only **Einrichten**
(the QR). *Handy hinzufügen* is a quiet row at the end, not a full-width blue button.

### 3.3 Regeln

Sub-tabs as a segmented control under the header; the chosen one is kept in the URL
(`#/regeln/zeit`). Switches save at once, as today, with the toast. Forms (plan, alarm, agenda,
calendar) show one sticky **Änderungen speichern** bar at the bottom while something is changed, and
leaving the view with unsaved changes asks first. The alarm week becomes one row of seven day chips
plus one time, with a per-day time only when the days differ. Times are 24-hour `<input type=time>`
with `lang="de-CH"`.

### 3.4 Apps

Waiting apps first, each with **Erlauben / Blockieren**. The list: icon letter, name, the rule as a
chip (*Frei*, *Tageslimit*, *Eigenes Limit 60 Min*, *Blockiert*, *Bonus-App*), the package name as
secondary text. Tapping the chip opens a sheet with the five rules as a radio list, each with one
line of explanation, and the own-limit minutes when that one is chosen. Search and the filter chips
stay. The family blocklist gets real separators and a collapsed "Vorgeschlagen" group.

### 3.5 Aktivität

The day navigator and the chart first; per-app bars below. The note about a phone that has not yet
reported its screen hours becomes one line with ⓘ. The time buttons are not repeated here. The
location shows the place as "vor 5 Min · Karte öffnen" (the OpenStreetMap link it already has), the
coordinates as secondary text. Energy collapses to a summary line that expands.

### 3.6 Components

- **Sheet:** the existing `<dialog>`, as a bottom sheet on a phone and a centred panel on a laptop.
  Used for the phone actions, the app rule, the QR and the recovery code, and confirmations.
- **Help:** each card's explanation shrinks to one line; an ⓘ button expands the rest in place.
- **Icons:** inline SVG from one sprite in `index.html`, replacing the text symbols (☼ ◉ ⚖ ▦).
- **Tokens:** one set of colours, radii, spacing and a type scale in `:root`, with a dark set, and
  status colours (ok, warn, paused, danger, bonus-gold) used the same way everywhere.

### 3.7 Code

`app.js` is 3,300 lines. It is split into classic scripts loaded in order, sharing the page's
scope as today: `app.js` (state, API, router, shell, components), `overview.js`, `rules.js`,
`apps.js`, `activity.js`, `family.js`. Each is added to `consoleRoutes`. German strings are written
in place; there is one language and no translation layer.

## 4. Testing

- Every browser test in `tests/e2e` keeps its assertion; where it selected by structure or English
  text that changed, the selector or string follows the new console. A test that asserted a layout
  now gone (the drawer) is rewritten to assert its replacement (the bottom bar is pinned, every
  destination is reachable).
- `mobile_test.go` measures every new view and sheet at 360 px: no sideways scroll, no tap target
  under 44 px, no input font under 16 px. `laptop_test.go` keeps "one navigation at every width".
  Both get the new sub-tabs and sheets added to what they measure.
- `calibrate-mobile.sh` is re-run: every layout rule still goes red on its known-bad input.
- New assertions: each common job (give 15 min, pause, find, ring, allow a waiting app) is reachable
  in at most two taps from Übersicht at 360 px, measured by driving the page.
- The tour is re-run after each stage and the pictures read.

## 5. Stages

Each stage ships on its own, with the suite green and the tour read:

1. Tokens, icons, shell and navigation (bottom bar, sidebar), German shell strings, Übersicht with
   the phone sheet (Today and Home merged).
2. Regeln with its sub-tabs and the sticky save bar.
3. Apps with the rule sheet and the blocklist fix.
4. Aktivität and Familie; the rest of the strings.
