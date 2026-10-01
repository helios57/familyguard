'use strict';

/* Regeln — everything that decides what one child's phones allow, in three sub-pages:
 *
 *   Zeit    — Schlafenszeit, Tageslimit, Zeitzone, the daily plan (FR-22), the alarm clock (FR-23)
 *   Schutz  — the switches, blocked websites, the DNS resolver, the ad filter (FR-6)
 *   Agenda  — what is on (FR-24), a calendar read into it (FR-25), and the week as the phone sees it
 *
 * It used to be one column of about 4000 px on a phone, with four Save buttons among switches that
 * saved at once. Now a switch or a single field still saves the moment it changes; the three
 * editors that are documents (plan, alarm week, agenda) are drafts, and ONE bar at the bottom says
 * that something is unsaved and saves all of it. Leaving the page with a draft unsaved asks first.
 */

const RULES_SUBS = [
  { key: 'time', label: 'Zeit', icon: 'clock' },
  { key: 'protection', label: 'Schutz', icon: 'shield' },
  { key: 'agenda', label: 'Agenda', icon: 'calendar' },
];

async function loadRules() {
  const [policy, domains, devices, plan, alarm, agenda, agendaWeek, calendar] = await Promise.all([
    api('/children/' + state.childId + '/policy'),
    api('/children/' + state.childId + '/blocked-domains'),
    api('/devices?child_id=' + encodeURIComponent(state.childId)),
    api('/children/' + state.childId + '/plan'),
    api('/children/' + state.childId + '/alarm'),
    api('/children/' + state.childId + '/agenda'),
    api('/children/' + state.childId + '/agenda/days?days=7'),
    api('/children/' + state.childId + '/calendar'),
  ]);
  // A draft survives a re-read while it holds unsaved edits: a heartbeat arriving between two fields
  // must not throw away the group a parent is halfway through writing.
  const keep = (draft) => draft && draft.childId === state.childId && draft.dirty;
  if (!keep(state.agendaDraft)) {
    state.agendaDraft = { childId: state.childId, dirty: false, entries: (agenda.entries || []).map((e) => ({ ...e })) };
  }
  if (!keep(state.planDraft)) {
    state.planDraft = { childId: state.childId, dirty: false, groups: clonePlan(plan.groups || []) };
  }
  if (!keep(state.alarmDraft)) {
    state.alarmDraft = alarmDraftFrom(alarm);
  }
  return {
    policy,
    domains: domains.domains || [],
    enrolled: (devices.devices || []).some((d) => d.enrolled),
    alarm,
    week: agendaWeek.days || [],
    calendar,
  };
}

function alarmDraftFrom(alarm) {
  const weekdays = (alarm.weekdays || []).slice();
  while (weekdays.length < 7) weekdays.push('');
  const times = new Set(weekdays.filter(Boolean));
  return {
    childId: state.childId, dirty: false, weekdays, skipHolidays: !!alarm.skip_holidays,
    // One time for every day unless the week already has two different ones.
    perDay: times.size > 1,
    time: [...times][0] || '07:00',
  };
}

function renderRules(data) {
  const sub = RULES_SUBS.some((s) => s.key === state.sub) ? state.sub : 'time';
  const nav = el('nav', { class: 'subtabs', 'aria-label': 'Regeln' }, RULES_SUBS.map((s) => el('a', {
    href: s.key === 'time' ? '#/rules' : '#/rules/' + s.key,
    'data-sub': s.key,
    'aria-current': s.key === sub ? 'page' : null,
  }, icon(s.icon, 'icon-sm'), s.label)));

  const out = [];
  // Rules are a property of the child and are saved whether or not a phone exists to carry them, so
  // this page stays fully usable — it just says so, rather than letting a parent set a bedtime and
  // wonder why nothing happened.
  if (!data.enrolled) {
    out.push(notice('info', 'Für dieses Kind ist noch kein Handy eingerichtet.',
      'Was du hier einstellst, wird jetzt gespeichert und gilt, sobald ein Handy da ist.'));
  }
  out.push(nav);
  if (sub === 'time') {
    out.push(el('div', { class: 'cols' },
      screenTimeCard(data),
      alarmCard(data),
      el('div', { class: 'full' }, planCard())));
  } else if (sub === 'protection') {
    out.push(el('div', { class: 'cols' }, switchesCard(data), websitesCard(data), adFilterCard(data)));
  } else {
    out.push(agendaCard(data));
  }
  out.push(saveBar());
  return out;
}

VIEWS.rules = {
  load: loadRules,
  render: renderRules,
  perChild: true,
  // The drafts of this page are the reason to ask before leaving it.
  afterRender: () => {
    // Not while a save is on its way: the drafts are still marked changed until the server answers,
    // and asking "discard?" about edits that are being saved is a question with no right answer.
    state.leaveGuard = () => rulesDirty() && !state.rulesSaving;
    state.discardDrafts = () => {
      for (const d of [state.planDraft, state.alarmDraft, state.agendaDraft]) if (d) d.dirty = false;
    };
    updateSaveBar();
  },
};

/* ---- the policy: fields that save at once ----------------------------------- */

async function savePolicy(patch, label) {
  await act(label, async () => {
    const updated = await api('/children/' + state.childId + '/policy', { method: 'PATCH', body: patch });
    if (state.dataView === 'rules') state.data.policy = updated;
  });
  refresh();
}

function policySwitch(p, key, title, hint, invert) {
  const input = el('input', {
    type: 'checkbox', checked: (invert ? !p[key] : p[key]) || false,
    onchange: (e) => savePolicy({ [key]: invert ? !e.target.checked : e.target.checked }, title + (e.target.checked ? ' an' : ' aus')),
  });
  return el('label', { class: 'switch' },
    el('span', { class: 'switch-label' }, title, hint ? el('small', { text: hint }) : null), input);
}

function screenTimeCard(data) {
  const p = data.policy;
  return el('div', { class: 'card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Schlafenszeit und Tageslimit' })),
    policySwitch(p, 'bedtime_enabled', 'Schlafenszeit', 'Pausiert Apps über Nacht. Anrufe gehen immer.'),
    el('div', { class: 'field-row' },
      el('div', {}, el('label', { for: 'bt-start', text: 'Beginnt' }),
        el('input', { id: 'bt-start', type: 'time', value: p.bedtime_start, onchange: (e) => savePolicy({ bedtime_start: e.target.value }, 'Schlafenszeit ab ' + e.target.value) })),
      el('div', {}, el('label', { for: 'bt-end', text: 'Endet' }),
        el('input', { id: 'bt-end', type: 'time', value: p.bedtime_end, onchange: (e) => savePolicy({ bedtime_end: e.target.value }, 'Schlafenszeit bis ' + e.target.value) }))),
    el('div', {}, el('label', { for: 'quota', text: 'Bildschirmzeit pro Tag (Minuten, 0 = kein Limit)' }),
      el('input', {
        id: 'quota', type: 'number', min: '0', max: '1440', inputmode: 'numeric', value: p.daily_limit_minutes,
        onchange: (e) => savePolicy({ daily_limit_minutes: Number(e.target.value) }, 'Tageslimit ' + fmtMinutes(Number(e.target.value))),
      })),
    // Editable, not prose: this is the zone every other time on the page is measured against —
    // bedtime, the daily reset and therefore the quota.
    el('div', {}, el('label', { for: 'tz', text: 'Zeitzone' }),
      el('input', {
        id: 'tz', type: 'text', value: p.timezone || '', placeholder: 'Europe/Zurich',
        autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false',
        onchange: (e) => savePolicy({ timezone: e.target.value.trim() }, 'Zeitzone'),
      })),
    help('Schlafenszeit und Tagesbeginn richten sich nach dieser Zone.',
      'Ein IANA-Name wie Europe/Zurich. Der Server lehnt ab, was er nicht findet, ein Tippfehler wird also gemeldet statt gespeichert.'));
}

function switchesCard(data) {
  const p = data.policy;
  return el('div', { class: 'card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Was erlaubt ist' })),
    policySwitch(p, 'tracking_only', 'Nur beobachten', 'Sehen, was passiert, aber nichts auf dem Handy ändern.'),
    // FR-5.3. Off applies `no_install_apps`, so the Play Store refuses every install on that phone —
    // measured the first time this was used with a real family: "i cant install whatsapp".
    policySwitch(p, 'allow_child_installs', 'Apps installieren lassen',
      'Aus sperrt den Play Store ganz — nichts Neues lässt sich installieren, und was trotzdem ankommt, wartet auf deine Freigabe. Zum Hinzufügen einer App einschalten, danach wieder aus.'),
    policySwitch(p, 'youtube_blocked', 'YouTube sperren', 'Sperrt die App und die Website.'),
    // FR-5.6 and FR-5.7: the two switches that exist for the adult holding the phone, worded as what
    // they cost — they are the only ones whose "on" makes the phone easier to interfere with.
    policySwitch(p, 'allow_debugging', 'Entwickleroptionen und USB-Debugging erlauben',
      'Für ein Handy, an dem du entwickelst. Aus schaltet adb ab, und ein Handy, das dann den Kontakt zu dieser Konsole verliert, ist auch über USB nicht mehr erreichbar.'),
    policySwitch(p, 'allow_uninstall', 'Apps entfernen erlauben',
      'Einschalten, während du das Handy einrichtest oder ein Backup zurückspielst. Aus heisst: Apps lassen sich nicht deinstallieren — weder vom Kind noch von dir über USB.'));
}

function websitesCard(data) {
  const p = data.policy;
  return el('div', { class: 'card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Websites' })),
    el('form', {
      class: 'toolbar',
      onsubmit: async (e) => {
        e.preventDefault();
        const input = e.target.querySelector('input');
        const domain = input.value.trim();
        if (!domain) return;
        await act(domain + ' gesperrt', () =>
          api('/children/' + state.childId + '/blocked-domains', { method: 'POST', body: { domain } }));
        refresh();
      },
    },
    el('div', { class: 'field-row' },
      el('input', { type: 'text', name: 'domain', placeholder: 'beispiel.ch', 'aria-label': 'Website sperren', autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false' }),
      el('button', { class: 'btn btn-primary', type: 'submit', text: 'Sperren' }))),
    data.domains.length
      ? el('ul', { class: 'list' }, data.domains.map((d) => el('li', {},
        el('span', { class: 'label' }, el('b', { text: d })),
        el('button', {
          class: 'btn btn-quiet btn-danger', type: 'button', text: 'Entfernen', 'aria-label': d + ' entsperren',
          onclick: async () => {
            await act(d + ' entsperrt', () => api('/children/' + state.childId +
              '/blocked-domains?domain=' + encodeURIComponent(d), { method: 'DELETE' }));
            refresh();
          },
        }))))
      : el('p', { class: 'muted', text: 'Keine Website ist mit Namen gesperrt.' }),
    el('h3', { class: 'section-title', text: 'Verschlüsseltes DNS (optional)' }),
    // Empty is a real answer here: the phone uses whatever encrypted resolver the network offers.
    el('input', {
      id: 'dns', type: 'text', value: p.dns_host || '', placeholder: 'keiner', 'aria-label': 'DNS-Server',
      autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false',
      onchange: (e) => savePolicy({ dns_host: e.target.value.trim() }, 'DNS-Server'),
    }),
    help(p.dns_host ? 'Namen werden über ' + p.dns_host + ' aufgelöst.' : 'Leer: das Handy nimmt den Server des Netzes. Das ist in Ordnung.',
      'Ein DNS-Server sieht nur Namen. Werbung, die eine App über ihre eigene Verbindung holt, kann er weder sehen noch entfernen — dafür ist der Werbefilter da.'));
}

/* FR-6.6 to FR-6.9: its own card because it is the only setting here that needs a second value to
   do anything at all — a switch with no list silently filters nothing, and the phone reports
   exactly that back (see the phone sheet in Übersicht). */
function adFilterCard(data) {
  const p = data.policy;
  return el('div', { class: 'card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Werbung in Apps' })),
    policySwitch(p, 'ad_filter', 'Werbung und Tracker filtern',
      'Läuft auf dem Handy selbst und erreicht so die Werbung in Spielen und Apps — dort ist die meiste.'),
    el('div', {}, el('label', { for: 'adlist', text: 'Filterliste' }),
      el('input', {
        id: 'adlist', type: 'text', value: p.ad_filter_list_url || '', placeholder: 'https://…',
        autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false',
        onchange: (e) => savePolicy({ ad_filter_list_url: e.target.value.trim() }, 'Filterliste'),
      })),
    // FamilyGuard ships the fetcher and never the list: the good lists are GPL-3.0 and this is an MIT
    // project, so the URL is offered and the bytes stay where their licence put them.
    !p.ad_filter_list_url
      ? el('button', {
        class: 'btn btn-soft btn-block', type: 'button', text: 'Liste von AdGuard verwenden',
        onclick: () => savePolicy({ ad_filter_list_url: AD_FILTER_SUGGESTED_LIST }, 'Filterliste'),
      })
      : null,
    p.ad_filter_list_url
      ? el('p', { class: 'muted', text: 'Das Handy lädt diese Liste einmal am Tag und filtert damit. FamilyGuard hostet sie nicht.' })
      : notice('warn', null, 'Ohne Liste gibt es nichts zu filtern — der Schalter bewirkt erst etwas, wenn eine gesetzt ist. Jede Liste im AdGuard- oder hosts-Format geht.'),
    help('Wie es funktioniert',
      'Das Handy leitet seinen eigenen Verkehr durch FamilyGuard und sperrt nur nach Namen — es liest nie den Inhalt einer Verbindung. Manche Spiele, die sich mit Werbung bezahlen, bleiben an der Stelle stehen, wo die Werbung laufen würde; das ist der Preis.'));
}

/* ---- drafts and the save bar -------------------------------------------------- */

function rulesDirty() {
  return [state.planDraft, state.alarmDraft, state.agendaDraft].some((d) => d && d.dirty && d.childId === state.childId);
}

function markDirty(draft) {
  draft.dirty = true;
  updateSaveBar();
}

function updateSaveBar() {
  const bar = document.getElementById('savebar');
  if (bar) bar.hidden = !rulesDirty();
}

function saveBar() {
  return el('div', { id: 'savebar', class: 'savebar', role: 'region', 'aria-label': 'Ungespeicherte Änderungen', hidden: true },
    el('span', { text: 'Ungespeicherte Änderungen' }),
    el('div', { class: 'row-actions' },
      el('button', { class: 'btn btn-quiet', type: 'button', 'data-save': 'discard', text: 'Verwerfen', onclick: () => {
        for (const d of [state.planDraft, state.alarmDraft, state.agendaDraft]) if (d) d.dirty = false;
        refresh();
      } }),
      el('button', { class: 'btn btn-primary', type: 'button', 'data-save': 'all', text: 'Speichern', onclick: saveRules })));
}

/* Saves every draft that has changes, one document at a time, in the order the page shows them. A
   draft the server refuses stays as typed and dirty, so the parent can fix what the server named;
   the ones that landed are clean. */
async function saveRules() {
  const jobs = [];
  if (state.planDraft && state.planDraft.dirty) jobs.push(savePlan);
  if (state.alarmDraft && state.alarmDraft.dirty) jobs.push(saveAlarm);
  if (state.agendaDraft && state.agendaDraft.dirty) jobs.push(saveAgenda);
  state.rulesSaving = true;
  try {
    for (const job of jobs) await job();
  } finally {
    state.rulesSaving = false;
  }
  updateSaveBar();
  if (!rulesDirty()) refresh();
}

/* ---- the daily plan (FR-22) ---------------------------------------------- */

const clonePlan = (groups) => groups.map((g) => ({ ...g, tasks: (g.tasks || []).map((t) => ({ ...t })) }));
const newPlanTask = () => ({ title: '', note: '' });
const newPlanGroup = () => ({
  title: '', weekdays: 127, starts_at: '07:00', ends_at: '20:00', earned_minutes: 30, tasks: [newPlanTask()], _open: true,
});

/* The day buttons: Mo … So as seven toggles over a weekday bitmask, bit 0 = Monday. */
function dayToggles(get, set, label) {
  return el('div', { class: 'plan-days', role: 'group', 'aria-label': label || 'Tage' },
    WEEKDAYS_SHORT.map((name, bit) => el('button', {
      class: 'btn', type: 'button', text: name, 'data-day': String(bit),
      'aria-label': WEEKDAYS_LONG[bit],
      'aria-pressed': String((get() & (1 << bit)) !== 0),
      onclick: (e) => {
        set(get() ^ (1 << bit));
        e.currentTarget.setAttribute('aria-pressed', String((get() & (1 << bit)) !== 0));
      },
    })));
}

/* The plan editor: groups of tasks, each with its days, its window and the minutes it earns. Edited
 * as a draft and saved as ONE document, because that is how the server takes it: a PUT that keeps
 * the ids it already knows, so an edited group keeps its history and a new one starts fresh. Typing
 * changes the draft and nothing else — re-rendering on every keystroke would move the caret — and
 * only adding or removing a row redraws. */
function planCard() {
  const draft = state.planDraft;
  const changed = () => markDirty(draft);
  const restructure = (fn) => { fn(); draft.dirty = true; redraw(); };

  const field = (obj, key, attrs, parse) => el('input', {
    ...attrs, value: obj[key] === undefined || obj[key] === null ? '' : String(obj[key]),
    oninput: (e) => { obj[key] = parse ? parse(e.target.value) : e.target.value; changed(); },
  });

  const group = (g, gi) => el('div', { class: 'plan-group' }, editorBox(g, g.title || 'Neue Gruppe',
    [fmtDays(g.weekdays), fmtWindow(g.starts_at, g.ends_at), '+' + fmtMinutes(Number(g.earned_minutes) || 0),
      g.tasks.length === 1 ? '1 Aufgabe' : g.tasks.length + ' Aufgaben'].join(' · '),
    el('div', {}, el('label', { text: 'Gruppe' }),
      field(g, 'title', { type: 'text', 'data-field': 'title', placeholder: 'z. B. Morgen', 'aria-label': 'Name der Gruppe' })),
    dayToggles(() => g.weekdays, (v) => { g.weekdays = v; changed(); }),
    el('div', { class: 'field-row plan-times' },
      el('div', {}, el('label', { text: 'Von' }), field(g, 'starts_at', { type: 'time', 'data-field': 'starts_at', 'aria-label': 'Von' })),
      el('div', {}, el('label', { text: 'Bis' }), field(g, 'ends_at', { type: 'time', 'data-field': 'ends_at', 'aria-label': 'Bis' })),
      el('div', {}, el('label', { text: 'Gibt Bonuszeit (min)' }),
        field(g, 'earned_minutes', { type: 'number', min: '0', max: '1440', inputmode: 'numeric', 'data-field': 'earned_minutes', 'aria-label': 'Verdiente Minuten' }, Number))),
    el('div', { class: 'stack' }, g.tasks.map((t, ti) => el('div', { class: 'plan-task field-row' },
      field(t, 'title', { type: 'text', 'data-field': 'task-title', placeholder: 'Aufgabe', 'aria-label': 'Aufgabe' }),
      field(t, 'note', { type: 'text', 'data-field': 'task-note', placeholder: 'Notiz', 'aria-label': 'Notiz' }),
      el('button', {
        class: 'btn btn-quiet btn-icon', type: 'button', 'data-plan': 'remove-task',
        'aria-label': 'Diese Aufgabe entfernen', disabled: g.tasks.length === 1,
        onclick: () => restructure(() => g.tasks.splice(ti, 1)),
      }, icon('x'))))),
    el('div', { class: 'row' },
      el('button', {
        class: 'btn', type: 'button', 'data-plan': 'add-task',
        onclick: () => restructure(() => g.tasks.push(newPlanTask())),
      }, icon('plus'), 'Aufgabe'),
      el('button', {
        class: 'btn btn-quiet btn-danger', type: 'button', text: 'Gruppe entfernen', 'data-plan': 'remove-group',
        onclick: () => restructure(() => draft.groups.splice(gi, 1)),
      }))));

  return el('div', { class: 'card plan-card' },
    el('div', { class: 'card-head' },
      el('h2', { text: 'Tagesplan' }),
      el('button', {
        class: 'btn btn-soft', type: 'button', 'data-plan': 'add-group',
        onclick: () => restructure(() => draft.groups.push(newPlanGroup())),
      }, icon('plus'), 'Gruppe')),
    help('Aufgaben, die das Kind auf dem Handy abhakt — sind alle einer Gruppe bestätigt, gibt es Bonuszeit.',
      'Wenn du oder eine Betreuungsperson jede Aufgabe einer Gruppe bestätigt habt, gibt es deren Minuten als Bonuszeit — nutzbar nach dem Tageslimit, in der Schlafenszeit und für Bonus-Apps. Verdiente Zeit gilt 7 Tage.'),
    draft.groups.length
      ? el('div', { class: 'stack' }, draft.groups.map(group))
      : el('p', { class: 'muted', text: 'Noch kein Plan. Füge eine Gruppe hinzu — zum Beispiel «Morgen», 07:00–08:00, mit «Zähne putzen» und «Bett machen».' }));
}

async function savePlan() {
  const draft = state.planDraft;
  // Only what the server keeps: an id when there is one, so a new row is new and an old row is the
  // same row.
  const body = {
    groups: draft.groups.map((g) => ({
      ...(g.id ? { id: g.id } : {}),
      title: g.title, weekdays: g.weekdays, starts_at: g.starts_at, ends_at: g.ends_at,
      earned_minutes: Number(g.earned_minutes) || 0,
      tasks: g.tasks.map((t) => ({ ...(t.id ? { id: t.id } : {}), title: t.title, note: t.note || '' })),
    })),
  };
  const saved = await act('Tagesplan gespeichert', () =>
    api('/children/' + state.childId + '/plan', { method: 'PUT', body }));
  // A refused plan stays as typed, so the parent can fix what the server named.
  if (!saved) return;
  state.planDraft = { childId: state.childId, dirty: false, groups: clonePlan(saved.groups || []) };
}

/* ---- the alarm clock (FR-23.6) -------------------------------------------- */

/* The week as seven day chips and one time — the common case, a school week at 06:45 — and a time per
   day only when the days really differ. Below it one date can be changed: "kein Wecker" or another
   time, and the dates already changed are listed with Entfernen. */
function alarmCard(data) {
  const draft = state.alarmDraft;
  const tz = data.policy.timezone;
  const changed = () => markDirty(draft);

  const chips = el('div', { class: 'plan-days alarm-days', role: 'group', 'aria-label': 'Wecktage' },
    WEEKDAYS_SHORT.map((name, i) => el('button', {
      class: 'btn', type: 'button', text: name, 'data-day': String(i), 'data-alarm': 'day-toggle',
      'aria-label': WEEKDAYS_LONG[i], 'aria-pressed': String(!!draft.weekdays[i]),
      onclick: () => {
        draft.weekdays[i] = draft.weekdays[i] ? '' : (draft.perDay ? '07:00' : draft.time);
        draft.dirty = true;
        redraw();
      },
    })));

  let times;
  if (!draft.perDay) {
    times = el('div', { class: 'field-row' },
      el('div', {}, el('label', { for: 'alarm-time', text: 'Weckzeit' }),
        el('input', {
          id: 'alarm-time', type: 'time', value: draft.time, 'data-alarm': 'time-all',
          oninput: (e) => {
            draft.time = e.target.value;
            for (let i = 0; i < 7; i++) if (draft.weekdays[i]) draft.weekdays[i] = e.target.value;
            changed();
          },
        })),
      el('div', {}, el('label', { text: ' ' }),
        el('button', { class: 'btn btn-quiet btn-block', type: 'button', 'data-alarm': 'per-day', text: 'Pro Tag verschieden',
          onclick: () => { draft.perDay = true; redraw(); } })));
  } else {
    times = el('div', { class: 'alarm-week' }, WEEKDAYS_LONG.map((name, i) => draft.weekdays[i]
      ? el('div', { class: 'alarm-day', 'data-day': String(i) },
        el('span', { text: name }),
        el('input', {
          type: 'time', value: draft.weekdays[i], 'data-alarm': 'time', 'aria-label': 'Weckzeit am ' + name,
          oninput: (e) => { draft.weekdays[i] = e.target.value; changed(); },
        }))
      : null));
  }

  const tomorrow = dayInZone(tz, 1);
  const dayInput = el('input', { type: 'date', value: tomorrow, min: dayInZone(tz, 0), max: dayInZone(tz, 60), 'data-alarm': 'day', 'aria-label': 'Datum' });
  const dayTime = el('input', { type: 'time', value: draft.time || '07:00', 'data-alarm': 'day-time', 'aria-label': 'Weckzeit an diesem Datum' });
  const setDay = async (time) => {
    const day = dayInput.value;
    if (!day) return;
    const ok = await tried(time ? 'Wecker am ' + fmtDayDe(day) + ' um ' + time : 'Kein Wecker am ' + fmtDayDe(day), () =>
      api('/children/' + state.childId + '/alarm/days/' + day, { method: 'PUT', body: { time } }));
    if (ok) refresh();
  };
  const changes = (data.alarm.overrides || []).map((o) => el('li', {},
    el('span', { class: 'label' }, el('b', { text: fmtDayDe(o.day) }),
      el('small', { text: o.time ? 'klingelt um ' + o.time : 'kein Wecker' })),
    el('button', {
      class: 'btn btn-quiet', type: 'button', text: 'Entfernen', 'data-alarm': 'day-clear', 'data-date': o.day,
      'aria-label': fmtDayDe(o.day) + ' wieder wie die Woche',
      onclick: async () => {
        const ok = await tried(fmtDayDe(o.day) + ' wieder wie die Woche', () =>
          api('/children/' + state.childId + '/alarm/days/' + o.day, { method: 'DELETE' }));
        if (ok) refresh();
      },
    })));

  return el('div', { class: 'card alarm-card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Wecker' })),
    help('Klingelt auf dem Handy, auch ohne Verbindung.',
      'Das Kind kann ihn stoppen oder 5 Minuten schlummern lassen, aber nicht verstellen. Zeiten in ' + (tz || 'der Zeitzone des Profils') + '.'),
    chips,
    draft.weekdays.some(Boolean) ? times : el('p', { class: 'muted', text: 'Kein Wecktag gewählt — der Wecker ist aus.' }),
    // FR-24.4. With the week because it is saved with it, and it changes what the week means.
    el('label', { class: 'switch' },
      el('span', { class: 'switch-label' }, 'Nicht in den Ferien',
        el('small', { text: 'Kein Wecker an den Ferien der Familie (unter Familie). Ein unten geändertes Datum klingelt trotzdem.' })),
      el('input', {
        type: 'checkbox', checked: !!draft.skipHolidays, 'data-alarm': 'skip-holidays',
        onchange: (e) => { draft.skipHolidays = e.target.checked; changed(); },
      })),
    el('h3', { class: 'section-title', text: 'Ein bestimmtes Datum' }),
    el('div', { class: 'field-row' }, dayInput, dayTime),
    el('div', { class: 'row' },
      el('button', { class: 'btn', type: 'button', text: 'Um diese Zeit', 'data-alarm': 'day-set', onclick: () => setDay(dayTime.value) }),
      el('button', { class: 'btn', type: 'button', text: 'Kein Wecker', 'data-alarm': 'day-off', onclick: () => setDay(null) })),
    changes.length
      ? el('ul', { class: 'list alarm-changes' }, changes)
      : el('p', { class: 'muted alarm-changes', text: 'Keine Ausnahmen — jeder Tag folgt der Woche.' }));
}

async function saveAlarm() {
  const draft = state.alarmDraft;
  const saved = await act('Wecker gespeichert', () =>
    api('/children/' + state.childId + '/alarm', { method: 'PUT', body: { weekdays: draft.weekdays, skip_holidays: draft.skipHolidays } }));
  if (!saved) return;
  state.alarmDraft = alarmDraftFrom(saved);
  if (state.dataView === 'rules') state.data.alarm = saved;
}

/* ---- the agenda (FR-24.6) ---------------------------------------------------- */

const newAgendaEntry = () => ({ kind: 'RECURRING', title: '', place: '', optional: false, weekdays: 31, day: '', starts_at: '08:00', ends_at: '12:00', _open: true });

/* The agenda editor: repeating entries (school, training) and entries on one date, saved as one
   document like the plan, and below it the week as the server expands it — which is what the phone
   shows, holidays applied. */
function agendaCard(data) {
  const draft = state.agendaDraft;
  const changed = () => markDirty(draft);
  const restructure = (fn) => { fn(); draft.dirty = true; redraw(); };
  const field = (obj, key, attrs) => el('input', {
    ...attrs, value: obj[key] || '', 'data-field': key,
    oninput: (e) => { obj[key] = e.target.value; changed(); },
  });
  const entry = (e, i) => {
    const kind = el('select', {
      'data-field': 'kind', 'aria-label': 'Wiederholung',
      onchange: (ev) => restructure(() => {
        e.kind = ev.target.value;
        if (e.kind === 'SINGLE' && !e.day) e.day = dayInZone(data.policy.timezone, 1);
      }),
    },
    el('option', { value: 'RECURRING', text: 'Jede Woche', selected: e.kind === 'RECURRING' }),
    el('option', { value: 'SINGLE', text: 'Ein Datum', selected: e.kind === 'SINGLE' }));
    const when = e.kind === 'SINGLE'
      ? field(e, 'day', { type: 'date', 'aria-label': 'Datum' })
      : dayToggles(() => e.weekdays, (v) => { e.weekdays = v; changed(); });
    return el('div', { class: 'agenda-entry plan-group' }, editorBox(e, e.title || 'Neuer Eintrag',
      [e.kind === 'SINGLE' ? (e.day ? fmtDayDe(e.day) : 'ein Datum') : fmtDays(e.weekdays), fmtWindow(e.starts_at, e.ends_at),
        e.place, e.optional ? 'freiwillig' : ''].filter(Boolean).join(' · '),
      el('div', { class: 'field-row' },
        field(e, 'title', { type: 'text', placeholder: 'z. B. Schule', 'aria-label': 'Titel' }),
        field(e, 'place', { type: 'text', placeholder: 'Ort (optional)', 'aria-label': 'Ort' })),
      kind, when,
      el('div', { class: 'field-row' },
        el('div', {}, el('label', { text: 'Von' }), field(e, 'starts_at', { type: 'time', 'aria-label': 'Von' })),
        el('div', {}, el('label', { text: 'Bis' }), field(e, 'ends_at', { type: 'time', 'aria-label': 'Bis' }))),
      el('div', { class: 'row' },
        el('label', { class: 'alarm-on' },
          el('input', {
            type: 'checkbox', checked: !!e.optional, 'data-field': 'optional',
            onchange: (ev) => { e.optional = ev.target.checked; changed(); },
          }),
          el('span', { text: 'Freiwillig' })),
        el('button', {
          class: 'btn btn-quiet btn-danger', type: 'button', text: 'Entfernen', 'data-agenda': 'remove',
          onclick: () => restructure(() => draft.entries.splice(i, 1)),
        }))));
  };
  const week = el('div', { class: 'agenda-week' }, data.week.map((d) => el('div', { class: 'agenda-day' },
    el('b', { text: fmtDayDe(d.day) + (d.holiday ? ' · ' + d.holiday : '') }),
    d.items.length
      ? el('ul', { class: 'list' }, d.items.map((it) => el('li', {},
        el('span', { class: 'label' },
          el('span', { text: (it.all_day ? '' : it.starts_at + '–' + it.ends_at + ' ') + it.title }),
          el('small', { text: [it.all_day ? 'ganzer Tag' : '', it.place, it.optional ? 'freiwillig' : '', it.source === 'calendar' ? 'Kalender' : '']
            .filter(Boolean).join(' · ') })))))
      : el('p', { class: 'muted', text: d.holiday ? 'Ferien — nichts Wiederkehrendes.' : 'Nichts.' }))));

  return el('div', { class: 'cols' },
    el('div', { class: 'card agenda-card' },
      el('div', { class: 'card-head' },
        el('h2', { text: 'Agenda' }),
        el('button', { class: 'btn btn-soft', type: 'button', 'data-agenda': 'add', onclick: () => restructure(() => draft.entries.push(newAgendaEntry())) },
          icon('plus'), 'Eintrag')),
      help('Was ansteht: Schule, Training, Termine.',
        'Das Handy zeigt, was jetzt, als Nächstes und morgen ansteht. Wiederkehrende Einträge pausieren in den Ferien der Familie; Einträge an einem Datum nicht.'),
      draft.entries.length ? el('div', { class: 'stack' }, draft.entries.map(entry)) : el('p', { class: 'muted', text: 'Noch keine Einträge.' }),
      calendarBlock(data)),
    el('div', { class: 'card agenda-card' },
      el('div', { class: 'card-head' }, el('h2', { text: 'Diese Woche' })),
      week));
}

async function saveAgenda() {
  const draft = state.agendaDraft;
  const body = {
    entries: draft.entries.map((e) => ({
      ...(e.id ? { id: e.id } : {}), kind: e.kind, title: e.title, place: e.place || '', optional: !!e.optional,
      ...(e.kind === 'RECURRING' ? { weekdays: e.weekdays } : { day: e.day }),
      starts_at: e.starts_at, ends_at: e.ends_at,
    })),
  };
  const saved = await act('Agenda gespeichert', () => api('/children/' + state.childId + '/agenda', { method: 'PUT', body }));
  if (!saved) return;
  state.agendaDraft = { childId: state.childId, dirty: false, entries: (saved.entries || []).map((e) => ({ ...e })) };
}

/* FR-25.5: a calendar read into the agenda, read-only. The address is a credential (a secret
   calendar address reads the calendar), so the status says what the last read found rather than
   repeating the address. One field, so it saves on its own button rather than through the bar. */
function calendarBlock(data) {
  const cal = data.calendar || {};
  const input = el('input', {
    type: 'url', value: cal.url || '', placeholder: 'https://… .ics oder webcal://…', 'data-calendar': 'url',
    'aria-label': 'Kalenderadresse', autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false',
  });
  const status = !cal.url
    ? 'Kein Kalender. Füge die iCal-Adresse eines Kalenders ein (in Google Kalender: Einstellungen → der Kalender → Privatadresse im iCal-Format).'
    : (cal.error ? 'Das letzte Lesen ist fehlgeschlagen: ' + cal.error + '. Gezeigt wird, was ' + fmtTime(cal.fetched_at) + ' gelesen wurde.'
      : cal.events + (cal.events === 1 ? ' Termin' : ' Termine') + ' in den nächsten 60 Tagen, gelesen ' + fmtTime(cal.fetched_at) + '.');
  const save = async () => {
    const ok = await tried('Kalender gespeichert', () =>
      api('/children/' + state.childId + '/calendar', { method: 'PUT', body: { url: input.value.trim() } }));
    if (ok) refresh();
  };
  const remove = async () => {
    const ok = await tried('Kalender entfernt', () => api('/children/' + state.childId + '/calendar', { method: 'DELETE' }));
    if (ok) refresh();
  };
  return el('div', { class: 'stack calendar-block' },
    el('h3', { class: 'section-title', text: 'Kalender (optional)' }),
    input,
    el('p', { class: 'muted calendar-status' + (cal.error ? ' warn' : ''), text: status }),
    el('div', { class: 'row' },
      el('button', { class: 'btn btn-primary', type: 'button', text: 'Kalender speichern', 'data-calendar': 'save', onclick: save }),
      cal.url ? el('button', { class: 'btn btn-quiet btn-danger', type: 'button', text: 'Entfernen', 'data-calendar': 'remove', onclick: remove }) : null));
}
