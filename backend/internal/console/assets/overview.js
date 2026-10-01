'use strict';

/* Übersicht — every child at a glance, and the things a parent does most, one tap away.
 *
 * It replaced two pages. "Home" was one child's phones, each a card of eleven equal grey buttons
 * with the state that mattered (paused? how much time is left?) buried in prose; the guardian
 * window (FR-20, FR-21.3) was every child in German with time and Sperren. Both answered "how are
 * the children doing, and can I give time", so this page answers it once, for every role:
 *
 *   per child — the state in one word, today's time against the limit with the Bonuszeit in gold,
 *   +15 min, Pausieren and Live as the three buttons, the tasks waiting for a decision, and one row
 *   per phone. An admin's phone row opens the phone sheet (Orten, Klingeln, Bildschirm sperren, and
 *   under "Mehr" the rarer ones); a guardian's row shows the phone and nothing it may not do.
 *
 * A guardian never asks for an admin-only route: no /dpc, no commands, no provisioning. The role
 * test counts every 403 as a failure.
 */

async function loadOverview() {
  const children = (await api('/children')).children || [];
  state.children = children;
  renderChildSwitcher();
  const admin = isAdmin();
  return Promise.all(children.map(async (child) => {
    // A guardian cannot set a phone up, so a phone that is not enrolled is nothing to them.
    const all = (await api('/devices?child_id=' + encodeURIComponent(child.id))).devices || [];
    const devices = admin ? all : all.filter((d) => d.enrolled);
    const [states, today, lives] = await Promise.all([
      /* `.desired` is not a detail: the endpoint answers `{desired, input}` and every consumer below
         reads a desired state. Reading the envelope as if it were flat is not a visible error —
         every field simply comes back undefined — so a card once printed "0 min (no daily limit)"
         over a phone that had reported 99 minutes. Unwrapped here, so there is one place to be
         wrong about. */
      Promise.all(devices.map((d) => (d.enrolled
        ? api('/devices/' + d.id + '/desired-state').then((r) => (r && r.desired) || null).catch(() => null)
        : Promise.resolve(null)))),
      // Tolerated as missing: a card that cannot show the tasks still has to show the time.
      api('/children/' + child.id + '/today').catch(() => null),
      // FR-27: the latest position of a phone in Live, and only for those.
      Promise.all(devices.map((d) => (liveActive(d)
        ? api('/devices/' + d.id + '/live?limit=1').catch(() => null)
        : Promise.resolve(null)))),
    ]);
    return { child, today, devices: devices.map((dev, i) => ({ dev, desired: states[i], live: lives[i] })) };
  }));
}

function renderOverview(profiles) {
  if (!profiles.length) {
    return [isAdmin()
      ? emptyCard('family', 'Füge dein erstes Kind hinzu',
        'Regeln, Apps und Bildschirmzeit gehören zu einem Kind. Füge es hier hinzu und richte dann sein Handy ein.',
        el('button', { class: 'btn btn-primary', type: 'button', text: 'Kind hinzufügen', onclick: addChild }))
      : emptyCard('family', 'Noch kein Profil', 'Ein Admin richtet die Profile ein.')];
  }
  return [
    waitingCard(profiles),
    el('div', { class: 'cols' }, profiles.map(childCard)),
  ];
}

/* After every draw: a phone sheet that is open shows the phone as the page now knows it — Sperren
   turns into Entsperren the moment the server says the phone is locked, without closing the sheet. */
function afterOverview(profiles) {
  if (!sheetShows('device')) return;
  const id = document.getElementById('sheet').dataset.device;
  for (const p of profiles) {
    for (const d of p.devices) {
      if (d.dev.id === id) { openDeviceSheet(p.child, d); return; }
    }
  }
}

VIEWS.overview = { load: loadOverview, render: renderOverview, afterRender: afterOverview, perChild: false };

/* ---- the child ------------------------------------------------------------ */

/* The state of a child in one word, with its colour. Paused by a parent beats everything, because
   it is the one a parent has to undo; then what the phones report. */
function childStatus(child, devices, today) {
  const enrolled = devices.filter((d) => d.dev.enrolled);
  if (child.paused) return { cls: 'danger', word: 'Pausiert', reason: 'Nur Anrufe und Nachrichten gehen.' };
  if (!enrolled.length) return { cls: '', word: 'Kein Handy', reason: 'Noch kein Handy eingerichtet.' };
  const reasons = enrolled.map((d) => d.desired && d.desired.suspend_reason).filter(Boolean);
  if (reasons.includes('BEDTIME')) return { cls: 'paused', word: 'Schlafenszeit', reason: 'Apps sind bis zum Morgen pausiert.' };
  if (reasons.includes('QUOTA')) return { cls: 'paused', word: 'Limit erreicht', reason: 'Das Tageslimit ist aufgebraucht.' };
  if (reasons.length) return { cls: 'paused', word: 'Pausiert', reason: 'Apps pausiert: ' + guardianReason(reasons[0]) + '.' };
  // The limit is used up and the apps are still open: that is earned time being spent, and a card
  // reading "Frei" beside a full red bar contradicts itself (tour, 2026-10-01).
  if (enrolled.some((d) => onEarnedTime(d.desired)) && earnedLeft(today) > 0) {
    return { cls: 'gold', word: 'Bonuszeit', reason: 'Tageslimit aufgebraucht — läuft mit Bonuszeit.' };
  }
  const online = enrolled.some((d) => d.dev.state && d.dev.state.online);
  return { cls: 'ok', word: 'Frei', reason: online ? 'Apps sind offen.' : 'Apps sind offen · Handy gerade nicht erreichbar.' };
}

/* Over today's quota with a limit in force: whatever runs now is paid from earned time. */
function onEarnedTime(desired) {
  return !!desired && (desired.daily_limit_minutes || 0) > 0 && (desired.used_minutes || 0) >= (desired.quota_minutes || 0);
}

function earnedLeft(today) {
  return (today && today.earned && today.earned.left_minutes) || 0;
}

function guardianReason(reason) {
  return ({ PAUSED: 'pausiert', QUOTA: 'Tageslimit erreicht', BEDTIME: 'Schlafenszeit', EARNED: 'keine Bonuszeit mehr' })[reason] || reason.toLowerCase();
}

/* "Heute 40 min von 45 min (15 min weniger)": the day against the limit in force, with today's
   adjustment said in words rather than folded silently into the number. */
function guardianToday(desired) {
  const used = desired.used_minutes || 0;
  const limit = desired.daily_limit_minutes || 0;
  if (limit <= 0) return 'Heute ' + fmtMinutes(used) + ' (kein Tageslimit)';
  const bonus = desired.bonus_minutes || 0;
  return 'Heute ' + fmtMinutes(used) + ' von ' + fmtMinutes(desired.quota_minutes || 0)
    + (bonus > 0 ? ' (inkl. ' + fmtMinutes(bonus) + ' extra)' : '')
    + (bonus < 0 ? ' (' + fmtMinutes(-bonus) + ' weniger)' : '');
}

/* Time used against the quota as one bar. A limit exists when the plain limit says so: the quota can
   be 0 with a limit in force — a day with its time taken away (FR-21) — and that is "0 min left",
   never "no daily limit". */
function timeMeter(desired, earned) {
  const used = desired.used_minutes || 0;
  const quota = desired.quota_minutes || 0;
  const pct = quota > 0 ? Math.min(100, Math.round((used / quota) * 100)) : 100;
  // Gold, not red, when the time past the limit is earned time: nothing is wrong, the child earned it.
  const cls = used >= quota ? (earned > 0 ? 'bonus' : 'over') : '';
  return el('div', { class: 'meter', role: 'img', 'aria-label': fmtMinutes(used) + ' von ' + fmtMinutes(quota) },
    el('span', { class: cls, style: { width: pct + '%' } }));
}

function childCard(profile) {
  const { child, today, devices } = profile;
  const status = childStatus(child, devices, today);
  const enrolled = devices.filter((d) => d.dev.enrolled);
  const card = el('div', { class: 'card child-card', 'data-child': child.id },
    el('div', { class: 'child-head' },
      el('div', { class: 'avatar', 'aria-hidden': 'true', text: initial(child.name) }),
      el('div', { class: 'grow' },
        el('h2', { text: child.name }),
        el('div', { class: 'state-line', text: status.reason })),
      el('span', { class: 'chip ' + status.cls, 'data-state': status.word, text: status.word })));

  let hasLimit = false;
  const time = el('div', { class: 'time-block' });
  for (const { dev, desired } of enrolled) {
    if (!desired) {
      time.append(el('p', { class: 'muted', text: dev.name + ': noch keine Angaben vom Handy.' }));
      continue;
    }
    // Whether there is a limit comes from the plain limit, never from the quota (FR-21).
    const limited = (desired.daily_limit_minutes || 0) > 0;
    hasLimit = hasLimit || limited;
    time.append(el('p', { class: 'today', text: (enrolled.length > 1 ? dev.name + ' — ' : '') + guardianToday(desired) }));
    if (limited) time.append(timeMeter(desired, earnedLeft(today)));
  }
  if (time.childElementCount) card.append(time);
  const earned = guardianEarned(today);
  if (earned) card.append(earned);

  // Nothing to pause or give time to until a phone is there to carry it.
  if (enrolled.length) card.append(quickActions(child, enrolled, hasLimit));
  for (const { dev, live } of enrolled) {
    const lb = liveActive(dev) ? liveBlock(dev, live) : null;
    if (lb) card.append(lb);
  }

  // FR-5.4: apps paused until a parent answers. The queue lives in Apps; saying it here is what
  // makes a parent find it — a queue nobody sees does not get worked.
  const waiting = new Set();
  for (const { desired } of enrolled) for (const pkg of ((desired && desired.pending_approval) || [])) waiting.add(pkg);
  if (waiting.size && isAdmin()) {
    card.append(el('div', { class: 'notice', 'data-pending': String(waiting.size) },
      icon('apps'),
      el('div', { class: 'grow' },
        el('b', { text: waiting.size === 1 ? '1 App wartet auf deine Entscheidung' : waiting.size + ' Apps warten auf deine Entscheidung' }),
        el('p', { text: 'Bis du antwortest, ist sie auf dem Handy installiert und lässt sich nicht öffnen.' })),
      el('a', { class: 'btn btn-soft', href: '#/apps', text: 'Ansehen', onclick: () => { state.childId = child.id; localStorage.setItem(CHILD_KEY, child.id); state.appFilter = { q: '', rule: 'waiting', system: false }; } })));
  }

  const tasks = guardianTasks(child, today);
  if (tasks) card.append(el('div', {}, el('h3', { class: 'section-title', text: 'Heute' }), tasks));

  const addButton = (primary) => el('button', {
    class: 'btn btn-block ' + (primary ? 'btn-primary' : 'btn-quiet'), type: 'button', 'data-action': 'add-device',
    onclick: () => addDevice(child),
  }, icon('plus'), 'Handy hinzufügen');
  if (devices.length) {
    const rows = el('ul', { class: 'devices' }, devices.map((d) => el('li', {}, deviceRow(child, d))));
    if (isAdmin()) rows.append(el('li', {}, addButton(false)));
    card.append(el('div', { class: 'stack' }, el('h3', { class: 'section-title', text: devices.length === 1 ? 'Handy' : 'Handys' }), rows));
  } else if (isAdmin()) {
    card.append(setupSteps(child), addButton(true));
  } else {
    card.append(el('p', { class: 'muted', text: 'Noch kein Handy eingerichtet.' }));
  }
  return card;
}

/* The three things a parent reaches for most, as the largest buttons on the card. */
function quickActions(child, enrolled, hasLimit) {
  const buttons = [];
  if (hasLimit) {
    buttons.push(el('button', {
      class: 'btn btn-gold', type: 'button', 'data-minutes': '15',
      'aria-label': '15 Minuten mehr für heute für ' + child.name,
      onclick: () => giveTime(child, 15),
    }, icon('plus'), '15 min'));
  }
  buttons.push(pauseButton(child));
  if (enrolled.length === 1 && !liveActive(enrolled[0].dev)) {
    const dev = enrolled[0].dev;
    buttons.push(el('button', {
      class: 'btn', type: 'button', 'data-action': 'live-start',
      'aria-label': 'Live 30 min für ' + dev.name,
      onclick: () => startLive(dev, 30),
    }, icon('live'), 'Live 30 min'));
  }
  const row = el('div', { class: 'quick' }, buttons);
  if (!hasLimit) return row;
  return el('div', { class: 'stack' }, row,
    el('button', {
      class: 'btn btn-quiet', type: 'button', 'data-action': 'time-sheet',
      onclick: () => openTimeSheet(child),
    }, icon('clock'), 'Zeit für heute anpassen'));
}

/* +15 min, and in the sheet −15 · +30 · +60: extra minutes for the child's current day only
   (FR-3.11, FR-21). Negative takes time away, and the card then says "15 min weniger". */
function giveTime(child, minutes) {
  const label = (minutes > 0 ? '+' : '−') + Math.abs(minutes) + ' min für ' + child.name;
  return act(label, async () => {
    await api('/children/' + child.id + '/bonus', { method: 'POST', body: { minutes } });
    if (sheetShows('time')) closeSheet();
    refresh();
  });
}

function openTimeSheet(child) {
  const change = (minutes) => el('button', {
    class: 'btn' + (minutes > 0 ? ' btn-gold' : ''), type: 'button', 'data-minutes': String(minutes),
    text: (minutes > 0 ? '+' : '−') + Math.abs(minutes) + ' min',
    'aria-label': Math.abs(minutes) + (minutes > 0 ? ' Minuten mehr' : ' Minuten weniger') + ' für heute',
    onclick: () => giveTime(child, minutes),
  });
  openSheet('Zeit für ' + child.name, el('div', { class: 'stack' },
    el('p', { class: 'muted', text: 'Gilt nur für heute. Das Handy übernimmt es innert Sekunden.' }),
    el('div', { class: 'btn-grid' }, change(15), change(30), change(60), change(-15))), 'time');
}

/* Pausieren asks twice: it is the one button here that takes a phone away. The first tap arms it for
   five seconds and changes nothing; Fortsetzen gives something back and needs one tap. */
function pauseButton(child) {
  const set = (paused) => act(paused ? child.name + ' pausiert' : child.name + ' fortgesetzt', async () => {
    await api('/children/' + child.id + '/pause', { method: 'POST', body: { paused } });
    refresh();
  });
  if (child.paused) {
    return el('button', {
      class: 'btn btn-primary', type: 'button', 'data-action': 'unpause',
      onclick: () => set(false),
    }, icon('play'), 'Fortsetzen');
  }
  let armed = null;
  const label = el('span', { text: 'Pausieren' });
  const button = el('button', {
    class: 'btn btn-danger', type: 'button', 'data-action': 'pause',
    'aria-label': child.name + ' pausieren, ausser Anrufen und Nachrichten',
    onclick: () => {
      if (armed) {
        clearTimeout(armed);
        armed = null;
        set(true);
        return;
      }
      label.textContent = 'Wirklich?';
      button.classList.add('btn-danger-solid');
      armed = setTimeout(() => { armed = null; label.textContent = 'Pausieren'; button.classList.remove('btn-danger-solid'); }, 5000);
    },
  }, icon('pause'), label);
  return button;
}

/* ---- Bonuszeit and today's tasks (FR-22) ------------------------------------ */

/* The gold line: Bonuszeit left today, and what expires next. A negative balance is a debt the next
   earned minutes settle, and it is said as one rather than drawn as zero. */
function guardianEarned(today) {
  if (!today) return null;
  const e = today.earned || {};
  if (!today.groups.length && !e.available_minutes && !e.spent_minutes) return null;
  const left = e.left_minutes || 0;
  const next = (e.credits || []).find((c) => c.minutes > 0);
  return el('p', { class: 'earned' + (left < 0 ? ' debt' : ''),
    text: left < 0
      ? 'Bonuszeit: −' + fmtMinutes(-left) + ' (wird mit der nächsten verrechnet)'
      : 'Bonuszeit: ' + fmtMinutes(left)
        + (e.spent_minutes > 0 ? ' (heute ' + fmtMinutes(e.spent_minutes) + ' gebraucht)' : '')
        // "gültig bis" when all of it ends that day; "davon" only when part of it ends sooner.
        + (next && left > 0
          ? (next.minutes >= left ? ' · gültig bis ' : ' · ' + fmtMinutes(next.minutes) + ' davon bis ') + fmtDayDe(next.expires_on)
          : '') });
}

/* One decision on one task of today: Bestätigen, Nicht erledigt, or Rückgängig. */
function decideTask(child, task, decision, label) {
  return el('button', {
    class: 'btn' + (decision === 'confirm' ? ' btn-primary' : decision === 'reject' ? ' btn-danger' : ' btn-quiet'),
    type: 'button', text: label, 'data-task': task.id, 'data-decision': decision,
    'aria-label': label + ': ' + task.title,
    onclick: () => act(label, async () => {
      await api('/children/' + child.id + '/tasks/' + task.id + '/decision', { method: 'POST', body: { decision } });
      refresh();
    }),
  });
}

/* "Wartet auf dich": what a child reported as done and nobody has answered yet, across every
   profile, above the cards. Drawn only when something waits — an empty queue on every visit is how
   a full one stops being read. */
function waitingCard(profiles) {
  const rows = [];
  for (const { child, today } of profiles) {
    for (const g of (today && today.groups) || []) {
      for (const t of g.tasks) {
        if (t.state !== 'REPORTED') continue;
        rows.push(el('li', {},
          el('span', { class: 'label' },
            el('b', { text: t.title }),
            el('small', { text: child.name + ' · ' + g.title + (t.note ? ' · ' + t.note : '') })),
          el('div', { class: 'row-actions' },
            decideTask(child, t, 'confirm', 'Bestätigen'),
            decideTask(child, t, 'reject', 'Nicht erledigt'))));
      }
    }
  }
  if (!rows.length) return null;
  return el('div', { class: 'card full waiting-card' },
    el('div', { class: 'card-head' },
      el('h2', { text: 'Wartet auf dich' }),
      el('span', { class: 'badge gold', text: String(rows.length) })),
    el('ul', { class: 'list' }, rows));
}

const TASK_STATE = { OPEN: 'offen', REPORTED: 'gemeldet', CONFIRMED: 'bestätigt', REJECTED: 'nicht erledigt' };

/* Today's groups on a child's card, one line each — "Morgen · 2/2 · +15 min verdient" — opening to
   the tasks. Every task that is not confirmed can be confirmed from there (a child who forgot to tap
   "Fertig" still did it), and a confirmed one taken back. The reported ones are also in "Wartet auf
   dich" above, which is where a parent acts on them; listing every task of the day on the card
   pushed the phones below the fold (tour, 2026-10-01). A group stays open across redraws once
   opened. */
function guardianTasks(child, today) {
  if (!today || !today.groups.length) return null;
  state.openGroups = state.openGroups || new Set();
  return el('div', { class: 'stack guardian-tasks' }, today.groups.map((g) => {
    const done = g.tasks.filter((t) => t.state === 'CONFIRMED').length;
    const reported = g.tasks.filter((t) => t.state === 'REPORTED').length;
    const key = child.id + '/' + (g.id || g.title);
    const earned = g.credited_minutes > 0;
    const details = el('details', { class: 'task-group', 'data-group': g.id || g.title, open: state.openGroups.has(key) },
      el('summary', {},
        el('span', { class: 'grow' },
          el('b', { text: g.title }),
          el('small', { text: fmtWindow(g.starts_at, g.ends_at) + ' · ' + done + ' von ' + g.tasks.length + ' bestätigt'
            + (reported ? ' · ' + reported + ' gemeldet' : '') })),
        el('span', { class: 'chip ' + (earned ? 'gold' : ''), text: earned
          ? '+' + fmtMinutes(g.credited_minutes) + ' verdient'
          : '+' + fmtMinutes(g.earned_minutes) }),
        icon('chevron-right', 'icon-sm')),
      el('ul', { class: 'list' }, g.tasks.map((t) => el('li', {},
        el('span', { class: 'label' },
          el('span', { text: t.title }),
          el('small', { text: TASK_STATE[t.state] + (t.note ? ' · ' + t.note : '') })),
        t.state === 'CONFIRMED'
          ? decideTask(child, t, 'undo', 'Rückgängig')
          : decideTask(child, t, 'confirm', 'Bestätigen')))));
    details.addEventListener('toggle', () => {
      if (details.open) state.openGroups.add(key); else state.openGroups.delete(key);
    });
    return details;
  }));
}

/* ---- Live (FR-27) ------------------------------------------------------------ */

function liveActive(dev) {
  return !!(dev.live_until && new Date(dev.live_until).getTime() > Date.now());
}

function startLive(dev, minutes) {
  return act('Live für ' + dev.name, async () => {
    await api('/devices/' + dev.id + '/live', { method: 'POST', body: { minutes } });
    refresh();
  });
}

/**
 * Live: a phone that stays connected and reports its position every 10 s for a while — a child
 * walking home, a phone that has gone missing. A guardian may start it, which is the point: the
 * walk home is theirs as often as a parent's.
 *
 * The position shown is the phone's own last report with its age and accuracy, because a dot with
 * no time on it reads as "here now" when it may be ten minutes old.
 */
function liveBlock(dev, live) {
  if (!dev.enrolled) return null;
  if (!liveActive(dev)) {
    return el('div', { 'data-live': 'off' },
      el('button', { class: 'btn btn-block', type: 'button', 'data-action': 'live-start', onclick: () => startLive(dev, 30) },
        icon('live'), 'Live 30 min'));
  }
  const last = live && (live.locations || [])[0];
  return el('div', { class: 'notice info', 'data-live': 'on' },
    icon('live'),
    el('div', { class: 'grow stack' },
      el('div', {},
        el('b', { text: 'Live bis ' + fmtClock(dev.live_until) + (/* one line per phone when a child has two */ '') }),
        last
          ? el('p', { text: 'Letzte Position ' + fmtTime(last.captured_at)
            + (last.accuracy_m ? ' · ±' + Math.round(last.accuracy_m) + ' m' : '') })
          : el('p', { class: 'muted', text: 'Warte auf die erste Position — ein schlafendes Handy erfährt es innert 5 Minuten.' })),
      el('div', { class: 'btn-row' },
        last ? el('a', {
          class: 'btn btn-soft', target: '_blank', rel: 'noreferrer noopener',
          href: 'https://www.openstreetmap.org/?mlat=' + last.latitude + '&mlon=' + last.longitude + '#map=17/' + last.latitude + '/' + last.longitude,
        }, icon('map'), 'Karte') : null,
        el('button', { class: 'btn', type: 'button', 'data-action': 'live-more',
          onclick: () => startLive(dev, Math.min(120, Math.ceil((new Date(dev.live_until).getTime() - Date.now()) / 60000) + 30)) }, '+30 min'),
        el('button', { class: 'btn', type: 'button', 'data-action': 'live-stop', onclick: () => act('Live beendet', async () => {
          await api('/devices/' + dev.id + '/live', { method: 'DELETE' });
          refresh();
        }) }, 'Beenden'))));
}

/* ---- the phone ------------------------------------------------------------ */

/**
 * Whether this phone is running an older build than the one the server hosts, and which.
 *
 * Null whenever the question cannot be answered from measurements: no build reported by the phone,
 * no build parsed by the server, or a version code of zero on either side. "Up to date" and "nobody
 * could tell" look identical on a card, and only one of them is a fact. The comparison is on
 * version CODES, which are integers and monotone.
 */
function updateBehind(st) {
  const hosted = state.dpc;
  if (!hosted || !hosted.hosted || !hosted.version_code) return null;
  if (!st.app_version_code) return null;
  if (hosted.version_code <= st.app_version_code) return null;
  return { hosted: hosted.version_name || ('Build ' + hosted.version_code) };
}

/* Whether this phone is MEASURABLY running the build the server hosts. Unknown is never current. */
function updateCurrent(st) {
  const hosted = state.dpc;
  if (!hosted || !hosted.hosted || !hosted.version_code) return false;
  if (!st.app_version_code) return false;
  return hosted.version_code <= st.app_version_code;
}

/** Online, but not holding its stream: the screen is off and it checks in (FR-26.2). */
function resting(dev) {
  return !!(dev.state && dev.state.online) && dev.stream_open === false;
}

function linkState(dev) {
  const online = dev.state && dev.state.online;
  return resting(dev) ? 'resting' : (online ? 'listening' : 'offline');
}

/**
 * Everything on this phone a parent may have to do something about, most important first. `short`
 * is the phone row's one line; `full` is the block in the phone sheet. Every rule here reads a
 * MEASURED false or a reported error: `undefined` is a phone that has not said — an older DPC does
 * not report the field — and a warning there would be an alarm about a device nothing is wrong with.
 */
function deviceIssues(dev, desired) {
  const st = dev.state || {};
  const issues = [];
  const behind = updateBehind(st);

  /* Only while the server actually has something this phone has not taken: `update_error` is
     last-reported, and a failure recorded against the build that is already the newest one has
     nothing left to clear it. `updateCurrent`, not `!behind`: an UNKNOWN must keep the warning. */
  if (st.update_error && !updateCurrent(st)) {
    issues.push({ short: 'Update fehlgeschlagen', full: () => notice('warn', 'Dieses Handy hat das letzte Update nicht übernommen.',
      st.update_error + '.' + (behind
        ? ' Der Server bietet ' + behind.hosted + ' an. FamilyGuard versucht es selbst wieder; klappt es dauerhaft nicht, muss das Handy mit seinem QR-Code neu eingerichtet werden.'
        : '')) });
  }

  /* Named for what a parent sees rather than for the API: nobody presses Klingeln and thinks "my
     alarms are being coalesced". EVERY remedy that applies is listed, ordered by how much it
     changes: the pilot phone reported power_exempt=false AND exact_alarms=false on one heartbeat,
     and showing one switch would have cost a second trip to Settings. */
  if (st.power_exempt === false || st.exact_alarms === false) {
    const steps = [];
    if (st.power_exempt === false) {
      steps.push(el('li', {},
        el('b', { text: 'Im Hintergrund laufen lassen' }),
        el('small', { text: 'Einstellungen → Apps → FamilyGuard → Akku → Nicht eingeschränkt. Auf Samsung zusätzlich Einstellungen → Akku → Einschränkungen der Hintergrundnutzung und FamilyGuard aus «Apps im Standby-Modus» und «Apps im Tiefschlafmodus» entfernen.' })));
    }
    if (st.exact_alarms === false) {
      steps.push(el('li', {},
        el('b', { text: 'Zur richtigen Zeit aufwachen lassen' }),
        el('small', { text: 'Einstellungen → Apps → FamilyGuard → Wecker und Erinnerungen → erlauben.' })));
    }
    issues.push({ short: 'Wird im Hintergrund gebremst', full: () => el('div', { class: 'stack' },
      notice('warn', 'Dieses Handy bremst FamilyGuard im Hintergrund.',
        'Orten, Klingeln und Sperren können Minuten brauchen, solange das Handy schläft, und nichts meldet einen Fehler — es geht nichts verloren, es kommt nur später an. '
        + 'FamilyGuard kann das nicht selbst erlauben — dafür gibt es keine Geräteinhaber-Schnittstelle. '
        + 'Am schnellsten geht es auf dem Handy: öffne dort FamilyGuard, neben jeder Einstellung unten ist ein Knopf «Einstellungen öffnen», der direkt zum Schalter führt. '
        + (steps.length > 1
          ? 'Zwei Einstellungen auf dem Handy müssen geändert werden, und beide zählen — die ganzen Wege, falls du selbst hinnavigieren willst:'
          : 'Eine Einstellung auf dem Handy muss geändert werden — der ganze Weg, falls du selbst hinnavigieren willst:')),
      el('ol', { class: 'steps' }, steps)) });
  }

  // Spelled out, because the number it invalidates is on the same card: without the grant every
  // app reads zero minutes, so "0 min von 1 h" is not a child who stayed off their phone.
  if (st.usage_access === false) {
    issues.push({ short: 'Bildschirmzeit wird nicht gemessen', full: () => notice('warn', 'Die Bildschirmzeit wird auf diesem Handy nicht gemessen.',
      'Öffne auf dem Handy Einstellungen → Apps → Spezieller App-Zugriff → Nutzungsdatenzugriff und schalte FamilyGuard ein. Bis dahin zeigt jede App null Minuten und Tageslimits greifen nie. '
      + 'FamilyGuard kann das nicht selbst einschalten — Android erlaubt das keiner App, auch keinem Geräteinhaber.') });
  }

  /* What the phone MEASURED about its filter, which is a different question from what the parent
     asked for. The phone's own reason, verbatim, and the guess only when there is none (FR-6.11): a
     console that guesses at a remedy sends a parent to fix something that is not broken. */
  if (desired && desired.ad_filter && st.ad_filter_running === false) {
    issues.push({ short: 'Werbefilter läuft nicht', full: () => notice('warn', 'Der Werbefilter läuft auf diesem Handy nicht.',
      st.ad_filter_reason
        ? 'Das Handy sagt: ' + st.ad_filter_reason + '.'
        : 'Er ist für dieses Kind eingeschaltet, also startet ihn das Handy bei der nächsten Synchronisation. Bleibt er aus, wurde die Liste vielleicht nicht geladen — prüfe die Filterliste unter Regeln › Schutz und ob das Handy online ist.') });
  }
  return issues;
}

/* The phone's badges: short facts and warnings, each only on a measurement. */
function deviceBadges(dev, desired) {
  const st = dev.state || {};
  const behind = dev.enrolled ? updateBehind(st) : null;
  return el('div', { class: 'wrap device-badges' },
    !dev.enrolled && el('span', { class: 'badge warn', text: 'nicht eingerichtet' }),
    dev.locked && el('span', { class: 'badge danger', text: 'Bildschirm gesperrt' }),
    // The FamilyGuard build on the phone, and nothing at all when it has not said.
    dev.enrolled && st.app_version_name
      && el('span', { class: 'badge' + (behind ? ' warn' : ''), text: 'App ' + st.app_version_name }),
    behind && el('span', { class: 'badge warn', text: '→ ' + behind.hosted }),
    st.usage_access === false
      && el('span', { class: 'badge warn', text: 'Bildschirmzeit nicht gemessen' }),
    // Separate badges, shown WHENEVER false — including together: two switches, two remedies, and
    // hiding the second until the first is fixed costs a parent a second day of waiting.
    st.power_exempt === false
      && el('span', { class: 'badge warn', text: 'Akku eingeschränkt' }),
    st.exact_alarms === false
      && el('span', { class: 'badge warn', text: 'Wecker nicht exakt' }),
    // FR-23.4: the alarm clock still rings, but as a notification rather than over the lock screen.
    st.alarm_full_screen === false
      && el('span', { class: 'badge warn', text: 'Wecker: nur Mitteilung', title: 'Android lässt FamilyGuard den Wecker auf diesem Handy nicht über dem Sperrbildschirm zeigen. Er klingelt trotzdem, mit Stopp und Schlummern in der Mitteilung.' }),
    // FR-6.10. Three states: `true` is a tunnel the phone has confirmed is up, `false` one it says
    // is down, and `undefined` a phone that has not reported (an older DPC, or a Play build with no
    // filter at all, FR-15.8). The warning only when the parent ASKED for the filter.
    st.ad_filter_running === true
      && el('span', { class: 'badge ok', text: 'Werbefilter an' }),
    desired && desired.ad_filter && st.ad_filter_running === false
      && el('span', { class: 'badge warn', text: 'Werbefilter läuft nicht' }));
}

function deviceStatusText(dev) {
  const st = dev.state || {};
  if (!dev.enrolled) return 'Noch nicht eingerichtet';
  if (st.online) return resting(dev) ? 'Ruht' : 'Online';
  return 'Zuletzt ' + fmtTime(st.last_seen_at);
}

/* One row per phone. An admin's row is a button that opens the phone sheet; a guardian's row is the
   same picture with nothing to press, because every action in that sheet is an admin's. */
function deviceRow(child, entry) {
  const { dev, desired } = entry;
  const st = dev.state || {};
  const issues = dev.enrolled ? deviceIssues(dev, desired) : [];
  const facts = [
    el('span', {}, el('span', { class: 'dot ' + (dev.enrolled ? (resting(dev) ? 'resting' : (st.online ? 'online' : 'offline')) : '') }), ' ', deviceStatusText(dev)),
    st.battery_level !== null && st.battery_level !== undefined
      ? el('span', { text: st.battery_level + ' %' + (st.charging ? ' lädt' : '') }) : null,
    dev.locked ? el('span', { class: 'warn', text: 'gesperrt' }) : null,
  ];
  const inner = [
    el('span', { class: 'device-icon', 'aria-hidden': 'true' }, icon('phone')),
    el('span', { class: 'grow' },
      el('b', { text: dev.name }),
      el('small', {}, facts),
      issues.length ? el('small', { class: 'warn', text: issues[0].short + (issues.length > 1 ? ' · +' + (issues.length - 1) : '') }) : null),
  ];
  const attrs = { class: 'device-row', 'data-device': dev.id, 'data-link': dev.enrolled ? linkState(dev) : 'none' };
  if (!isAdmin()) return el('div', attrs, inner);
  if (!dev.enrolled) {
    return el('div', attrs, inner,
      el('button', { class: 'btn btn-primary', type: 'button', 'data-action': 'setup', onclick: () => showProvisioning(dev) }, icon('qr'), 'Einrichten'));
  }
  return el('button', Object.assign(attrs, {
    type: 'button', 'aria-label': dev.name + ' — Details und Aktionen',
    onclick: () => openDeviceSheet(child, entry),
  }), inner, icon('chevron-right'));
}

/* The phone sheet: every fact and warning about one phone, and every command. The commands a parent
   needs in a hurry come first; the rest wait under "Mehr". */
function openDeviceSheet(child, entry) {
  const { dev, desired, live } = entry;
  const st = dev.state || {};
  const behind = updateBehind(st);

  const cmd = (type, label, iconName, cls) => el('button', {
    class: 'btn' + (cls ? ' ' + cls : ''), type: 'button', 'data-command': type,
    onclick: () => act(label, async () => {
      await api('/devices/' + dev.id + '/commands', { method: 'POST', body: { type } });
      refresh();
    }),
  }, iconName ? icon(iconName) : null, label);

  const facts = el('dl', { class: 'facts' },
    el('div', {}, el('dt', { text: 'Status' }),
      el('dd', { 'data-link': linkState(dev) }, deviceStatusText(dev))),
    st.battery_level !== null && st.battery_level !== undefined
      ? el('div', {}, el('dt', { text: 'Akku' }), el('dd', { text: st.battery_level + ' %' + (st.charging ? ', lädt' : '') })) : null,
    dev.model ? el('div', {}, el('dt', { text: 'Modell' }), el('dd', { text: dev.model })) : null,
    // "Android 14", never "Android Android 14": the field is sometimes the bare number and
    // sometimes already the full name, depending on the build that reported it.
    dev.os_version ? el('div', {}, el('dt', { text: 'System' }), el('dd', { text: /^android/i.test(dev.os_version) ? dev.os_version : 'Android ' + dev.os_version })) : null);

  const body = [facts, deviceBadges(dev, desired)];
  for (const issue of deviceIssues(dev, desired)) body.push(issue.full());
  if (desired && desired.ad_filter && st.ad_filter_running === true) {
    body.push(el('p', { class: 'muted', text: 'Werbefilter läuft'
      + (st.ad_filter_rules ? ' mit ' + st.ad_filter_rules.toLocaleString('de-CH') + ' Regeln' : '')
      + (st.ad_filter_fetched_at ? ', Liste geladen ' + fmtTime(st.ad_filter_fetched_at) : '') + '.' }));
  }
  if (desired) {
    body.push(el('p', { class: 'today', text: guardianToday(desired) }));
    if (desired.suspend_reason) {
      body.push(el('p', { class: 'muted', text: 'Apps gerade pausiert: ' + guardianReason(desired.suspend_reason) + '.' }));
    }
  } else {
    body.push(el('p', { class: 'muted', text: 'Das Handy hat noch keinen Zustand gemeldet.' }));
  }

  // FR-26.3: a phone the server can push to hears of Sperren, Klingeln and Orten within seconds; the
  // check-in is then the fallback, not the route.
  if (resting(dev)) {
    body.push(st.push_registered
      ? el('p', { class: 'muted', 'data-resting': 'push',
        text: 'Der Bildschirm ist aus, also ruht das Handy, um Akku zu sparen. Sperren, Klingeln und Orten wecken es per Push innert Sekunden; kommt ein Push nicht an, holt es sie beim nächsten Melden ab.' })
      : el('p', { class: 'muted', 'data-resting': 'poll',
        text: 'Der Bildschirm ist aus, also ruht das Handy, um Akku zu sparen, und meldet sich alle 5 Minuten. Sperren, Klingeln und Orten erreichen es beim nächsten Melden, oder sofort, wenn jemand den Bildschirm einschaltet.' }));
  }
  body.push(liveBlock(dev, live));

  body.push(el('h3', { class: 'section-title', text: 'Aktionen' }));
  body.push(el('div', { class: 'action-grid' },
    cmd('LOCATE_NOW', 'Orten', 'pin'),
    cmd('TRIGGER_ALARM', 'Klingeln', 'bell'),
    // Not a toggle, unlike Sperren/Entsperren below, and the difference is what this console KNOWS:
    // the server records `locked`, but nothing reports whether a siren is playing. Its absence is
    // the defect this pair exists for — on 2026-09-07 a parent rang the phone, could not stop it,
    // and the siren ran its full five-minute cap.
    cmd('STOP_ALARM', 'Klingeln stoppen', 'bell-off'),
    dev.locked ? cmd('UNLOCK_DEVICE', 'Entsperren', 'unlock') : cmd('LOCK_NOW', 'Bildschirm sperren', 'lock')));

  body.push(el('h3', { class: 'section-title', text: 'Mehr' }));
  body.push(el('div', { class: 'action-grid' },
    cmd('SYNC_POLICY', 'Jetzt synchronisieren', 'sync', 'btn-quiet'),
    // Always offered and never *conditioned* on the comparison: the button is what a parent reaches
    // for when a phone's reported version is wrong or missing, which is exactly the case the
    // comparison cannot see. The label carries the comparison when there is one.
    cmd('UPDATE_APP', behind ? 'Auf ' + behind.hosted + ' aktualisieren' : 'App aktualisieren', 'download', 'btn-quiet'),
    // "Handy ersetzen", never "QR": on a working phone it REVOKES it, and a label that read like
    // "show me that code again" is how the first real phone was disconnected.
    el('button', { class: 'btn btn-quiet', type: 'button', 'data-action': 'replace', onclick: () => showProvisioning(dev) }, icon('qr'), 'Handy ersetzen'),
    el('button', { class: 'btn btn-quiet', type: 'button', 'data-action': 'recovery', onclick: () => showRecovery(dev) }, icon('key'), 'Wiederherstellungscode')));

  openSheet(dev.name, el('div', { class: 'stack device-sheet' }, body.filter(Boolean)), 'device');
  document.getElementById('sheet').dataset.device = dev.id;
}

/* ---- setting a phone up ------------------------------------------------------ */

/* The whole of a child's card until the first phone is enrolled — where every new install starts.
   The factory-reset requirement is not advice: it is what Android demands before it hands
   device-owner rights to anything. */
function setupSteps(child) {
  return el('div', { class: 'stack' },
    el('ol', { class: 'steps' },
      el('li', {}, el('b', { text: 'Handy hier hinzufügen' }),
        el('small', { text: 'Gib ihm einen Namen, den du später wiedererkennst. Noch wird nichts ans Handy gesendet.' })),
      el('li', {}, el('b', { text: 'Handy zurücksetzen' }),
        el('small', { text: 'Einstellungen → System → Zurücksetzen → Alle Daten löschen. FamilyGuard wird als Geräteinhaber installiert, und das erlaubt Android nur auf einem Handy ohne eingerichtetes Konto.' })),
      el('li', {}, el('b', { text: 'Den QR-Code scannen, den diese Seite zeigt' }),
        el('small', { text: 'Auf dem Willkommensbildschirm sechsmal auf dieselbe Stelle tippen. Das Handy fragt dann nach einem QR-Code.' }))));
}

async function addDevice(child) {
  const name = await askSheet({ title: 'Handy für ' + child.name, label: 'Wie heisst das Handy?', placeholder: 'z. B. ' + child.name + 's Pixel', confirmLabel: 'Hinzufügen' });
  if (!name) return;
  const dev = await act('Handy hinzugefügt', () =>
    api('/children/' + child.id + '/devices', { method: 'POST', body: { name } }));
  if (dev) { await refresh(); showProvisioning(dev); }
}

async function showProvisioning(dev) {
  // The server refuses this with 409 unless the acknowledgement is sent, so this sheet is the
  // second lock and not the only one: a script, an API key or a stale page cannot get past the
  // first one, and this one exists so the parent reads the consequence in their own words.
  if (dev.enrolled && !await confirmSheet({
    title: dev.name + ' ersetzen?',
    lines: [
      'Ein neuer Einrichtungscode widerruft das Handy, das jetzt eingerichtet ist. Es meldet sich ab sofort nicht mehr.',
      'Um es wieder in Gang zu bringen, musst du dieses Handy in die Hand nehmen und den neuen Code eintippen: '
      + 'FamilyGuard › Wiederherstellung › Handy neu verbinden. Von hier aus lässt es sich nicht zurückholen.',
      // Not "needs a factory reset", which is false: redeeming the recovery code lets the phone
      // install the current build from its own browser — same key, so Device Owner survives.
      'Ein Handy mit einer älteren Version hat noch kein «Neu verbinden». Nimm zuerst den Wiederherstellungscode, '
      + 'öffne dann /dpc.apk im Browser dieses Handys und installiere darüber — ohne Zurücksetzen, ohne Kabel.',
      'Mach das nur für ein Handy, das du ersetzt oder verloren hast.',
    ],
    confirmLabel: dev.name + ' ersetzen',
    cancelLabel: 'So lassen',
  })) return;
  const out = await act('QR-Code bereit', () => api('/devices/' + dev.id + '/provisioning', {
    method: 'POST', body: { replace_enrolled: dev.enrolled === true },
  }));
  if (!out) return;
  const holder = el('div', { class: 'stack' });
  // The SVG comes from our own server and is inserted as markup because that is what it is. It is
  // not user input: it is generated from the payload this server just built.
  const wrap = el('div');
  wrap.innerHTML = out.svg;
  holder.append(
    el('p', { class: 'muted', text: 'Auf einem zurückgesetzten Handy sechsmal auf den Willkommensbildschirm tippen und diesen Code scannen. Er läuft ' + fmtIn(out.expires_at) + ' ab.' }),
    wrap,
    el('p', { class: 'muted', text: 'Später nochmals scannen braucht einen neuen Code — dieser gilt nur einmal.' }));
  // The same code in type-able form, because a phone that is ALREADY a device owner cannot be
  // provisioned again: there is no welcome screen short of a factory reset.
  if (out.setup_code) {
    holder.append(
      el('p', { class: 'muted', text: 'Schon eingerichtet und du verbindest es neu? Tippe stattdessen dies auf dem Handy ein: FamilyGuard › Wiederherstellung › Handy neu verbinden.' }),
      el('div', { class: 'code', text: out.setup_code }));
  }
  openSheet(dev.name + ' einrichten', holder, 'provisioning');
}

async function showRecovery(dev) {
  const out = await act('Wiederherstellungscode', () => api('/devices/' + dev.id + '/recovery-code'));
  if (!out) return;
  // Two things use this code, and until 2026-09-06 the sheet named only the first. The second is
  // the one a parent reaches for in an emergency: a revoked phone, or one on a build with no Re-link
  // screen, is recovered through here and not through a factory reset.
  const step = (n, text) => el('p', { class: 'muted', text: n + '. ' + text });
  openSheet('Wiederherstellungscode für ' + dev.name, el('div', { class: 'stack' },
    el('p', { class: 'muted', text: 'Tippe ihn auf dem Handy ein, um es ohne Internet zu entsperren. Bewahre ihn dort auf, wo dein Kind ihn nicht lesen kann.' }),
    el('div', { class: 'code', text: out.recovery_code }),
    el('h3', { text: 'Handy getrennt oder auf einer alten Version?' }),
    el('p', { class: 'muted', text: 'Derselbe Code holt es zurück. Ohne Zurücksetzen, ohne Kabel. Starte das Handy zwischen Schritt 1 und 2 nicht neu.' }),
    // Revoke-first is not a nicety: the release ends the moment a policy arrives from the server.
    el('p', { class: 'muted', text: 'Noch verbunden und meldet sich? Zuerst «Handy ersetzen». Ein Handy, das diese Seite noch erreicht, setzt jede Einschränkung wieder, sobald es synchronisiert — vielleicht bevor der Download fertig ist.' }),
    step(1, 'Auf dem Handy: FamilyGuard › Wiederherstellung › diesen Code eingeben. Alle Einschränkungen fallen weg.'),
    step(2, 'Im Browser des Handys /dpc.apk auf dieser Seite öffnen und darüber installieren. Die Einstellungen bleiben.'),
    step(3, 'Hier: «Handy ersetzen», für einen neuen Einrichtungscode.'),
    step(4, 'Auf dem Handy: FamilyGuard › Wiederherstellung › Handy neu verbinden, und diesen Code eintippen.')), 'recovery');
}
