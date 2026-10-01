'use strict';

/* Aktivität — one child's day, phone by phone: when the screen was on, what was open for how long,
 * where the phone was, and what FamilyGuard itself costs it.
 *
 * Giving time is not repeated here: it is the first button on the child's card in Übersicht, and
 * two places for one action is how they come to behave differently.
 */

async function loadActivity() {
  const devices = await api('/devices?child_id=' + encodeURIComponent(state.childId));
  const list = (devices.devices || []).filter((d) => d.enrolled);
  const day = state.timelineDay ? '?day=' + encodeURIComponent(state.timelineDay) : '';
  // One timeline request per phone: it answers for ONE day in the child's timezone and carries both
  // halves of the day card — the hours and the app totals.
  const [timelines, locations, energy] = await Promise.all([
    Promise.all(list.map((d) => api('/devices/' + d.id + '/usage/timeline' + day).catch(() => null))),
    Promise.all(list.map((d) => api('/devices/' + d.id + '/locations?limit=5').catch(() => null))),
    Promise.all(list.map((d) => api('/devices/' + d.id + '/energy?hours=24').catch(() => null))),
  ]);
  // The server decides what "today" is, in the child's timezone. Asked once, on the first load that
  // did not name a day, and never overwritten — a parent who has stepped back three days must not
  // have the forward bound redefined under them by a background refresh.
  if (!state.timelineDay) {
    const answered = timelines.find((t) => t && t.day);
    if (answered) state.timelineToday = answered.day;
  }
  return { devices: list, timelines, locations, energy };
}

function renderActivity(data) {
  if (!data.devices.length) {
    return [emptyCard('activity', 'Noch nichts aufgezeichnet',
      'Bildschirmzeit, App-Nutzung und Standorte kommen von einem eingerichteten Handy. Sobald eines da ist, füllt sich diese Seite von selbst.',
      setUpAPhoneLink())];
  }
  // The phone's name on the side cards only when the child has more than one phone: with one, the
  // day card above already says whose it is, and the name three times was a third of the headings.
  const many = data.devices.length > 1;
  return data.devices.map((dev, i) => el('div', { class: 'cols wide-left activity-device' },
    dayActivityCard(dev, data.timelines[i]),
    el('div', { class: 'col' },
      locationCard(dev, data.locations[i], many),
      energyCard(dev, data.energy && data.energy[i], many))));
}

VIEWS.activity = { load: loadActivity, render: renderActivity, perChild: true };

/* The words for why an app cannot be used right now (FR-3.10). One table, used everywhere. */
const BLOCKED_TEXT = {
  PAUSED: 'Von einem Elternteil pausiert',
  QUOTA: 'Pausiert — Tageslimit erreicht',
  BEDTIME: 'Pausiert — Schlafenszeit',
  APP_LIMIT: 'Pausiert — eigenes Limit aufgebraucht',
  BLOCKED: 'Gesperrt',
  PENDING: 'Wartet auf deine Freigabe',
  EARNED: 'Pausiert — keine Bonuszeit mehr',
};

/**
 * One day of one phone: when the screen was on, hour by hour, and what was open for how long.
 *
 * The two halves are DIFFERENT MEASUREMENTS of the same day, deliberately: the chart is built from
 * sittings (intervals with a real start and end, 0.6.13 onward), the table from the day totals the
 * phone has reported since 0.6.0 — which are also what the quota is enforced against. An older day
 * therefore has a table and an empty chart, and the card says which it is looking at rather than
 * drawing a flat line that reads as a quiet day.
 *
 * Every hour label and day boundary comes from the CHILD's timezone, which the server resolved.
 */
function dayActivityCard(dev, timeline) {
  if (!timeline) {
    return el('div', { class: 'card' },
      el('div', { class: 'card-head' }, el('h2', { text: dev.name })),
      notice('warn', null, 'Dieser Tag konnte für ' + dev.name + ' nicht geladen werden.'));
  }

  const hours = timeline.hours || [];
  const apps = timeline.apps || [];
  const screen = timeline.screen_time || null;
  const usedMs = apps.reduce((sum, a) => sum + (a.foreground_ms || 0), 0);
  const chartSeconds = hours.reduce((sum, h) => sum + h.seconds, 0);

  // The child's zone, not the parent's; a browser that cannot resolve the name says so instead of
  // silently drawing the parent's own hours onto the child's day.
  let fmtHour = null;
  let fmtDay = null;
  try {
    const h = new Intl.DateTimeFormat('de-CH', { timeZone: timeline.timezone, hour: '2-digit', hour12: false });
    const d = new Intl.DateTimeFormat('de-CH', { timeZone: timeline.timezone, weekday: 'short', day: 'numeric', month: 'short' });
    // The hour digits alone: German formats an hour-only time as "00 Uhr", and eight of those do
    // not fit under a 320-pixel chart.
    fmtHour = (ms) => (h.formatToParts(new Date(ms)).find((p) => p.type === 'hour') || { value: '' }).value;
    fmtDay = (ms) => d.format(new Date(ms));
  } catch (e) {
    fmtHour = null;
  }
  const localZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const hourLabel = fmtHour || ((ms) => String(new Date(ms).getHours()).padStart(2, '0'));
  const dayLabel = fmtDay ? fmtDay(Date.parse(timeline.from)) : timeline.day;

  const step = (iconName, by, disabled) => el('button', {
    class: 'btn btn-quiet btn-icon', type: 'button', disabled: disabled || false,
    'aria-label': by < 0 ? 'Vorheriger Tag' : 'Nächster Tag',
    onclick: () => { state.timelineDay = shiftDay(timeline.day, by); refresh(); },
  }, icon(iconName));
  const atToday = !!state.timelineToday && timeline.day >= state.timelineToday;

  /* One column per hour the day actually had — 23 or 25 on the two mornings the clocks move. Full
     height is a full hour, FIXED rather than scaled to the busiest hour: a scaled axis draws twenty
     minutes and four hours identically on their own days. */
  const columns = hours.map((h) => {
    const share = Math.max(0, Math.min(1, h.seconds / 3600));
    return el('div', { class: 'hr-col', title: hourLabel(Date.parse(h.start)) + ' · ' + fmtDuration(h.seconds) },
      el('div', { class: 'hr-fill' + (h.seconds ? '' : ' empty'), style: { height: (share * 100).toFixed(1) + '%' } }));
  });
  // A label every three hours: eight fit a 320-pixel phone, each on a real local hour.
  const ticks = hours.map((h, i) => el('span', {
    class: 'hr-tick' + (i % 3 === 0 ? '' : ' blank'),
    text: i % 3 === 0 ? hourLabel(Date.parse(h.start)) : '',
  }));

  let chart;
  if (!hours.length) {
    chart = el('p', { class: 'muted', text: 'Keine Stunden für diesen Tag.' });
  } else if (!chartSeconds) {
    // "Nothing was opened" and "this phone has never reported one" look identical on screen and have
    // opposite remedies; only the server can tell them apart (`ever_reported`).
    chart = timeline.ever_reported
      ? el('div', {},
        el('div', { class: 'hr-chart' }, columns),
        el('div', { class: 'hr-axis' }, ticks),
        el('p', { class: 'muted', text: 'Der Bildschirm war an diesem Tag nie an.' }))
      : el('div', { class: 'no-chart', 'data-chart': 'never-reported' },
        help('Für ' + dev.name + ' gibt es noch keine Stundenansicht.',
          dev.name + ' hat noch nie gemeldet, wann sein Bildschirm an war, deshalb ist die Ansicht leer statt flach. Die Liste unten ist gemessen. '
          + 'Die Stunden kommen, sobald das Handy eine Version hat, die sie aufzeichnet — es schickt sie bei der nächsten Synchronisation nach dem Update.'));
  } else {
    chart = el('div', {},
      el('div', {
        class: 'hr-chart', role: 'img',
        'aria-label': 'Bildschirm an: ' + fmtDuration(chartSeconds) + ' am ' + timeline.day + ', nach Stunden in ' + timeline.timezone,
      }, columns),
      el('div', { class: 'hr-axis' }, ticks));
  }

  return el('div', { class: 'card day-card' },
    el('div', { class: 'card-head' },
      el('h2', { text: dev.name }),
      el('span', { class: 'badge', text: fmtMinutes(screen ? screen.counted_minutes : Math.round(usedMs / 60000)) })),
    el('div', { class: 'tl-nav' },
      step('chevron-left', -1, false),
      el('span', { class: 'muted', text: dayLabel + ' · ' + timeline.timezone }),
      step('chevron-right', 1, atToday)),
    fmtHour ? null : notice('warn', null, 'Dieser Browser kennt die Zeitzone ' + timeline.timezone
      + ' nicht, die Stunden unten sind deshalb in ' + localZone + '.'),
    chart,
    // Drawn from the same rows either way, so without this a phone that measures nothing renders as
    // a day of zeros. Only on a measured false.
    ((dev.state || {}).usage_access === false)
      ? notice('warn', 'Diese Zahlen sind nicht gemessen.', 'Der Nutzungsdatenzugriff ist auf '
        + dev.name + ' aus, deshalb meldet jede App null. Schalte ihn in den Einstellungen des Handys ein: Apps → Spezieller App-Zugriff → Nutzungsdatenzugriff.')
      : null,
    screen ? screenTimeSummary(dev, screen) : null,
    appUsageTable(apps, screen));
}

/**
 * The day against its limit (FR-3.9). Counted minutes only: the home screen, System UI and
 * FamilyGuard itself are a separate, uncounted line — on 2026-09-23 a phone left on its charger with
 * the screen on spent its whole daily limit on the home screen.
 */
function screenTimeSummary(dev, screen) {
  const used = screen.counted_minutes || 0;
  const bonus = screen.bonus_minutes || 0;
  const limit = Math.max(0, (screen.daily_limit_minutes || 0) + bonus);
  const parts = [];
  if (!screen.limit_recorded) {
    parts.push(el('p', { class: 'muted', text: 'Gezählte Bildschirmzeit: ' + fmtMinutes(used)
      + '. Das Limit an diesem Tag wurde nicht aufgezeichnet — Tage vor dieser Funktion haben keins.' }));
  } else if ((screen.daily_limit_minutes || 0) > 0) {
    const pct = limit > 0 ? Math.min(100, Math.round((used / limit) * 100)) : 100;
    parts.push(el('div', { class: 'stack' },
      el('div', { class: 'row' },
        el('span', { class: 'muted', text: screen.is_today ? 'Bildschirmzeit heute' : 'Bildschirmzeit' }),
        el('span', { text: fmtMinutes(used) + ' von ' + fmtMinutes(limit)
          + (bonus > 0 ? ' (' + fmtMinutes(screen.daily_limit_minutes) + ' + ' + fmtMinutes(bonus) + ' extra)' : '')
          + (bonus < 0 ? ' (' + fmtMinutes(screen.daily_limit_minutes) + ' − ' + fmtMinutes(-bonus) + ' heute)' : '') })),
      el('div', { class: 'meter', role: 'img', 'aria-label': fmtMinutes(used) + ' von ' + fmtMinutes(limit) },
        el('span', { class: used >= limit ? 'over' : '', style: { width: pct + '%' } }))));
  } else {
    parts.push(el('p', { class: 'muted', text: 'Gezählte Bildschirmzeit: ' + fmtMinutes(used) + ' (kein Tageslimit).' }));
  }
  if (screen.uncounted_minutes > 0) {
    parts.push(el('p', { class: 'muted', text: 'Startbildschirm und System: ' + fmtMinutes(screen.uncounted_minutes) + ', nicht gezählt.' }));
  }
  if (screen.is_today && BLOCKED_TEXT[screen.suspend_reason]) {
    parts.push(notice('warn', null, BLOCKED_TEXT[screen.suspend_reason] + ': jede App, die nicht immer frei ist, ist auf ' + dev.name + ' pausiert.'));
  }
  return el('div', { class: 'stack st-summary' }, parts);
}

/**
 * Every app that was open on the day, longest first, as text: this is the half a parent acts on, and
 * a bar a thumb has to estimate against an axis is not a number. Sub-minute rows are counted rather
 * than listed — a screenful of "0 min" is how a real list gets learned as noise.
 */
function appUsageTable(apps, screen) {
  if (!apps.length) return el('p', { class: 'muted', text: 'An diesem Tag war keine App offen.' });
  const minutes = (a) => Math.round(a.foreground_ms / 60000);
  const shown = apps.filter((a) => minutes(a) >= 1 || a.blocked);
  const brief = apps.length - shown.length;
  if (!shown.length) {
    return el('p', { class: 'muted', text: apps.length + ' App(s) wurden geöffnet, keine eine ganze Minute lang.' });
  }
  // One scale for every bar, so two apps can be compared by eye: the longest use or the largest own
  // limit, whichever is further.
  const scale = Math.max(1, ...shown.map((a) => Math.max(minutes(a), a.limit_minutes || 0)));
  const pct = (m) => Math.min(100, (m / scale) * 100).toFixed(1) + '%';

  return el('div', {},
    el('h3', { class: 'section-title', text: 'Apps' }),
    el('ul', { class: 'app-bars' }, shown.map((a) => {
      const used = minutes(a);
      const own = a.limit_minutes || 0;
      const over = own > 0 && used >= own;
      return el('li', { class: 'app-bar' },
        el('div', { class: 'row' },
          el('span', {},
            el('span', { class: 'swatch', 'aria-hidden': 'true', style: { background: packageHue(a.package_name) } }),
            // Empty for an app since uninstalled — the package name is the honest fallback.
            el('b', { text: a.label || a.package_name })),
          el('span', { class: 'num', text: own > 0 ? fmtMinutes(used) + ' von ' + fmtMinutes(own) : fmtMinutes(used) })),
        el('div', {
          class: 'ubar', role: 'img',
          'aria-label': (a.label || a.package_name) + ': ' + fmtMinutes(used) + (own > 0 ? ' von eigenen ' + fmtMinutes(own) : ''),
        },
        el('span', { class: 'ubar-fill' + (over ? ' over' : '') + (a.counted === false ? ' uncounted' : ''), style: { width: pct(used) } }),
        own > 0 ? el('span', { class: 'ubar-limit', title: 'Limit ' + fmtMinutes(own), style: { left: pct(own) } }) : null),
        el('small', { text: a.package_name + (a.system_app ? ' · System' : '') + ' · ' + appStatusText(a, screen) }));
    })),
    brief ? el('p', { class: 'muted', text: brief + ' weitere App(s) waren weniger als eine Minute offen.' }) : null);
}

/** What governs an app, and why it is paused if it is (FR-3.10). */
function appStatusText(a, screen) {
  const rule = a.counted === false ? 'Nicht gezählt (Startbildschirm / System)'
    : a.rule === 'ALLOW' ? 'Immer frei'
      : !a.rule && a.free_by_default ? 'Immer frei (vorinstalliert)'
        : a.rule === 'BLOCK' ? 'Von dir gesperrt'
          : a.rule === 'BONUS' ? 'Bonus-App, läuft nur mit Bonuszeit'
            : (a.limit_minutes || 0) > 0 ? 'Eigenes Limit ' + fmtMinutes(a.limit_minutes) + ' pro Tag, zählt zum Tageslimit'
              : 'Zählt zum Tageslimit';
  const now = screen && screen.is_today && BLOCKED_TEXT[a.blocked] ? ' · jetzt: ' + BLOCKED_TEXT[a.blocked] : '';
  return rule + now;
}

/* Where the phone was: the latest position as the answer, with its age and accuracy, and the ones
   before folded away. A dot with no time on it reads as "here now" when it may be an hour old. */
function locationCard(dev, locs, many) {
  const list = (locs && locs.locations) || [];
  const head = el('div', {}, el('h2', { text: 'Standort' }), many ? el('p', { class: 'muted', text: dev.name }) : null);
  if (!list.length) {
    return el('div', { class: 'card location-card' }, head,
      el('p', { class: 'muted', text: 'Noch keine Position. «Orten» im Handy-Menü unter Übersicht fragt das Handy danach.' }));
  }
  const mapLink = (l, cls) => el('a', {
    class: 'btn ' + (cls || 'btn-soft'), target: '_blank', rel: 'noreferrer noopener',
    href: 'https://www.openstreetmap.org/?mlat=' + l.latitude + '&mlon=' + l.longitude + '#map=16/' + l.latitude + '/' + l.longitude,
  }, icon('map'), 'Karte');
  const line = (l) => fmtTime(l.captured_at) + (l.accuracy_m ? ' · ±' + Math.round(l.accuracy_m) + ' m' : '');
  const [last, ...earlier] = list;
  return el('div', { class: 'card location-card' }, head,
    el('div', { class: 'row' },
      el('span', { class: 'label' },
        el('b', { text: line(last) }),
        el('small', { class: 'muted', text: last.latitude.toFixed(5) + ', ' + last.longitude.toFixed(5) })),
      mapLink(last)),
    earlier.length
      ? el('details', { class: 'help' },
        el('summary', {}, icon('pin', 'icon-sm'), el('span', { text: earlier.length === 1 ? '1 frühere Position' : earlier.length + ' frühere Positionen' })),
        el('ul', { class: 'list' }, earlier.map((l) => el('li', {},
          el('span', { class: 'label' }, el('b', { text: line(l) }), el('small', { text: l.latitude.toFixed(5) + ', ' + l.longitude.toFixed(5) })),
          mapLink(l, 'btn-quiet')))))
      : null);
}

/**
 * What FamilyGuard spends on the phone over the last 24 hours, as the phone measured it (FR-26.5).
 * The battery's own rate is shown beside FamilyGuard's share because the question a parent asks is
 * "why is the battery empty", and the answer can be "not us". A phone that has not reported says so
 * rather than drawing zeros.
 */
const MIN_UNPLUGGED_MINUTES = 15;

function energyCard(dev, e, many) {
  const head = el('div', {}, el('h2', { text: 'Energie' }), many ? el('p', { class: 'muted', text: dev.name }) : null);
  const t = e && e.total;
  if (!t || !(t.minutes > 0)) {
    return el('div', { class: 'card', 'data-energy': 'none' }, head,
      el('p', { class: 'muted', text: 'Noch nicht gemeldet. Ein Handy meldet, was FamilyGuard verbraucht, sobald es 0.6.26 oder neuer hat.' }));
  }
  const hours = t.minutes / 60;
  const wakes = t.stream_opens + t.events + t.polls + t.pushes + t.other_syncs;
  // A rate needs time to mean anything: one percent over a minute reads as "−60 % per hour". Below
  // a quarter of an hour unplugged it is said as too short, never computed (tour, 2026-10-01:
  // "−3234.6 % pro Stunde" from a one-second interval).
  const battery = t.unplugged_minutes >= MIN_UNPLUGGED_MINUTES
    ? 'Akku −' + (t.battery_used / (t.unplugged_minutes / 60)).toFixed(1) + ' % pro Stunde ohne Ladegerät'
    : t.unplugged_minutes > 0
      ? 'Akku: noch zu kurz ohne Ladegerät gemessen, um eine Rate zu nennen'
      : 'Akku nicht gemessen (die ganze Zeit am Laden)';
  const facts = [
    'FamilyGuard braucht ' + (t.cpu_ms / 1000 / hours).toFixed(1) + ' s Rechenzeit pro Stunde',
    (wakes / hours).toFixed(1) + ' Aufwecker pro Stunde',
  ];
  // FR-26.1 and FR-26.4: whether the savings happen at all. Left out for a phone that does not report
  // them, rather than drawn as 0 %.
  const share = (part, rest) => (part == null || rest == null || part + rest <= 0) ? null
    : Math.round(100 * part / (part + rest));
  const resting = share(t.passive_ms, t.active_ms);
  if (resting != null) facts.push('Ruht (Bildschirm aus) ' + resting + ' % der Zeit');
  const dnsOnly = share(t.route_dns_ms, t.route_full_ms);
  if (dnsOnly != null) facts.push('Werbefilter nur DNS ' + dnsOnly + ' % seiner Zeit');
  facts.push('gemessen über die letzten ' + hours.toFixed(1) + ' h');
  return el('div', { class: 'card', 'data-energy': 'reported' }, head,
    el('p', { text: battery }),
    el('details', { class: 'help' },
      el('summary', {}, icon('info', 'icon-sm'), el('span', { text: 'Was FamilyGuard selbst verbraucht' })),
      el('ul', { class: 'list' }, facts.map((f) => el('li', { text: f })))));
}
