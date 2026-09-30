# Console redesign — implementation plan

> Executed inline (the owner: *"Continue till it's finished, don't wait all the time or ask me, just
> do it but good"*). Steps are stages; each ships on its own.

**Goal:** the console of `docs/superpowers/specs/2026-09-30-console-redesign-design.md` — German,
bottom bar / sidebar, Übersicht, Regeln sub-tabs, the app rule sheet — with every behaviour the
console has today still reachable and every browser test's assertion kept.

**Architecture:** no build step, no framework. `index.html` loads classic scripts with `defer`, in
order, sharing one scope: `app.js` (state, API, helpers, shell, router, stream, sheet, icons), then
`overview.js`, `rules.js`, `apps.js`, `activity.js`, `family.js`; `boot()` runs on
`DOMContentLoaded`, which fires after every deferred script has run. Each file is registered in
`httpapi/console.go`'s `consoleRoutes`.

**Spec:** `docs/superpowers/specs/2026-09-30-console-redesign-design.md`.

## Global constraints

- CSP stays `script-src 'self'; style-src 'self'`: no inline script, no style attribute (CSSOM only,
  through `el(…, { style: {…} })`).
- 44 px tap targets, 16 px inputs, no sideways scroll at 360 px (mobile_test.go measures them).
- German, Swiss spelling (ss, never ß), 24-hour times, `lang="de-CH"` on the document.
- Guardian (FR-20): Übersicht only, and only the routes open to `everyone` (`/me`, `/family`,
  `/children`, bonus, pause, today, task decisions, `/devices`, live, desired-state, `/events`).
- Every warning the old console showed is still shown (update failed, battery restricted, alarms
  not exact, usage access off, ad filter not running, resting/push, alarm notification-only).

## Review focus

1. A guardian's page must not request an admin-only route (roles_console_test counts 403s).
2. A background refresh must not discard a half-typed form (plan, alarm, agenda, holidays drafts).
3. Unsaved form edits must survive switching Regeln sub-tabs, and leaving the tab with them asks.
4. The app rule sheet must write exactly the rule the old segmented control wrote (LIMIT with
   minutes for "Eigenes Limit", DELETE for "Offen").
5. The bottom bar must not cover the last control of a long view, nor the toast.

## Routes

`#/overview` (Übersicht), `#/rules/time|protection|agenda`, `#/apps`, `#/activity`, `#/family`.
`#/home` and `#/guardian` land on `#/overview`; `#/rules` on `#/rules/time`.

## Stage 1 — shell, look, Übersicht (0.6.33)

- `app.css` rewritten around tokens: colours (light/dark), status colours (ok, warn, paused, danger,
  gold), radii, spacing, type scale. Components: card, list row, chip, button (primary / secondary /
  quiet / danger), segmented control, switch, meter with gold bonus segment, sheet as bottom sheet
  below 900 px and centred panel above, bottom bar, sidebar.
- `index.html`: SVG icon sprite; the shell as one grid whose areas move with the breakpoint (no node
  is moved by script any more, and the drawer goes): top bar (family, child switcher, sign-out),
  `#mainnav` (bottom bar below 900 px, sidebar above), `#view`.
- `app.js`: German strings for the shell, the router above, `placeChrome`/drawer removed, the child
  switcher hidden on Übersicht (which shows every child).
- `overview.js`: per child the status card (state word and colour, reason, meter with Bonuszeit,
  **+15 Min** with a menu for −15/+30/+60, **Sperren/Entsperren** armed twice as today, **Live**),
  tasks waiting and today's tasks, and one row per phone. An admin's row opens the phone sheet:
  Orten, Klingeln, Klingeln stoppen, Sperren/Entsperren, then *Mehr*: Jetzt synchronisieren, App
  aktualisieren, Handy ersetzen, Wiederherstellungscode; facts and every warning in full. A row
  shows its most important warning as one line. An unset phone offers *Einrichten*. *Handy
  hinzufügen* per child. The first-run card when a child has no phone.
- Tests: every browser test that touched Home, the guardian window, the drawer or the navigation
  follows the new structure; laptop_test asserts one navigation (the sidebar) and no drawer;
  mobile_test measures the bottom bar pinned and every destination reachable, the phone sheet, and
  the two-tap rule for +15, Sperren, Orten, Klingeln.

## Stage 2 — Regeln (0.6.34)

- `rules.js`: the sub-tab control; *Zeit*: Schlafenszeit, Tageslimit, Zeitzone, Tagesplan, Wecker
  (the week as seven day chips plus one time, a per-day time when they differ, "Nicht in den
  Ferien", one date); *Schutz*: the six switches, blockierte Websites, DNS, Werbefilter; *Agenda*:
  entries, Kalender, the week.
- One sticky **Änderungen speichern** bar for the drafts (plan, alarm, agenda), saving each dirty
  draft in turn; leaving with unsaved changes asks in the sheet.

## Stage 3 — Apps (0.6.35)

- `apps.js`: waiting apps first with **Erlauben** (= Tageslimit, the ordinary yes) / **Blockieren**
  and *Andere Wahl…*; the list with the rule as a chip; the rule sheet (five answers + Offen, the own
  minutes); the catalog card and sheet; the family blocklist with its fields separated and the
  suggested entries collapsed.

## Stage 4 — Aktivität, Familie, the rest (0.6.36)

- `activity.js`: day stepper, chart, bars; the "never reported" note as one line with ⓘ; location
  as "vor 5 Min · Karte öffnen"; energy as a summary that expands.
- `family.js`: Personen & Rechte, Kinder, Ferien, API-Schlüssel, Befehlszeile, Angemeldet.
- The remaining strings; the tour read at both widths; docs (REQUIREMENTS FR-13, CONCEPT,
  IMPLEMENTATION_PLAN phase), calibrate-mobile.sh re-run.

## Rulings

- The child switcher stays in the top bar at every width rather than moving into the sidebar: the
  spec's sidebar placement would need the node moved by script again, which is the machinery this
  plan removes. Cost if wrong: the switcher sits at the top on a laptop instead of at the left.
- Route keys stay English (`overview`, `rules/time`) while every visible word is German: they are
  identifiers, tests and bookmarks read them, and a URL with an umlaut is worse than one without.
- The four stages were built before any of them shipped, and ship together as 0.6.33, rather than as
  0.6.33–0.6.36: releasing Übersicht alone would have left the family a console half German and half
  English for days, and every stage rewrites the same shell. Each view was tested on its own before
  the next was written. Cost if wrong: one larger release to roll back instead of four smaller ones.
- The guardian's time buttons: +15 on the card, −15 · +30 · +60 in a sheet ("Zeit für heute
  anpassen"), where the spec had "+15 min with a small menu". Cost if wrong: one more tap for −15.
- A child with no enrolled phone gets no Pausieren and no time buttons — there is nothing for them to
  act on. The guardian page used to offer Sperren there.
- Undecided is "Keine Regel", not "Offen": in German "offen" also reads as "allowed".
- The phone's German strings follow the console ("Schlafenszeit", "pausiert"), not the other way
  round, because Google's own Family Link uses "Schlafenszeit" and a parent's pause is not a lock.
- Found outside the console and fixed in the same release: the alarm's VibratorManager crash below
  API 31, and Android lint becomes a CI gate.
