'use strict';

/* Familie — the household rather than one child: who may do what (FR-20), the children, the
 * family's holidays (FR-24), API keys (FR-17), the command-line tool, and the signed-in account.
 */

async function loadFamily() {
  const isPrimary = state.parent && state.parent.role === 'PRIMARY_ADMIN';
  const [parents, keys, cli, holidays] = await Promise.all([
    api('/parents'),
    // Only a primary admin may list keys, so anyone else gets a 403 rather than an empty list. The
    // catch keeps the whole page from failing on a call the reader was never entitled to make.
    isPrimary ? api('/api-keys').catch(() => ({ api_keys: [] })) : Promise.resolve(null),
    // Not through api(): the CLI manifest lives at /fgctl, outside /api/v1 and outside auth. A
    // deployment that ships no CLI answers {"hosted": false}, and an older server 404s — both render
    // as an absence.
    fetch('/fgctl', { headers: { 'Accept': 'application/json' } })
      .then((r) => (r.ok ? r.json() : { hosted: false }))
      .catch(() => ({ hosted: false })),
    api('/family/holidays'),
  ]);
  return { parents: parents.parents || [], keys: keys && (keys.api_keys || []), isPrimary, cli, holidays: holidays.holidays || [] };
}

function renderFamily(data) {
  return [el('div', { class: 'cols' },
    el('div', { class: 'col' }, peopleCard(data), childrenCard(), youCard()),
    el('div', { class: 'col' }, holidaysCard(data), apiKeysCard(data), cliCard(data.cli)))];
}

VIEWS.family = {
  load: loadFamily,
  render: renderFamily,
  perChild: false,
  afterRender: () => {
    state.leaveGuard = () => !!(state.holidayDraft && state.holidayDraft.dirty);
    state.discardDrafts = () => { if (state.holidayDraft) state.holidayDraft.dirty = false; };
  },
};

/* ---- people and rights (FR-20) ----------------------------------------------- */

const ROLES = [['GUARDIAN', 'Betreuungsperson'], ['ADMIN', 'Admin'], ['PRIMARY_ADMIN', 'Haupt-Admin']];

function roleLabel(role) {
  const hit = ROLES.find(([value]) => value === role);
  return hit ? hit[1] : role;
}

/* `selected` is set on the option, not `value` on the select: the options are appended after the
   select is created, and a value set before its option exists does not take. */
function roleSelect(current, label, onchange) {
  return el('select', { class: 'role-select', 'aria-label': label, onchange },
    ROLES.map(([value, text]) => el('option', { value, text, selected: value === current })));
}

function peopleCard(data) {
  const isPrimary = data.isPrimary;
  const card = el('div', { class: 'card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Personen und Rechte' })),
    help('Haupt-Admin: alles. Admin: alles ausser Personen und API-Schlüsseln. Betreuungsperson: nur die Übersicht.',
      'Eine Betreuungsperson sieht, wie es den Kindern heute geht, kann Zeit geben oder nehmen, pausieren, Live starten und Aufgaben bestätigen — aber keine Regeln ändern.'),
    el('ul', { class: 'list' }, data.parents.map((p) => el('li', { class: 'person' },
      el('span', { class: 'label' },
        el('b', { text: p.display_name || p.email }),
        el('small', { text: p.email + ' · ' + roleLabel(p.role) })),
      isPrimary && p.id !== state.parent.id
        ? el('span', { class: 'row-actions' },
          roleSelect(p.role, 'Rolle von ' + p.email, async (e) => {
            const role = e.target.value;
            const yes = await confirmSheet({
              title: 'Rolle ändern?',
              lines: [p.email + ' wird ' + roleLabel(role) + '.'],
              confirmLabel: 'Ändern', danger: false,
            });
            if (!yes) { e.target.value = p.role; return; }
            await act('Rolle geändert', () => api('/parents/' + p.id, { method: 'PATCH', body: { role } }));
            refresh();
          }),
          el('button', {
            class: 'btn btn-quiet btn-danger', type: 'button', text: 'Entfernen',
            onclick: async () => {
              const yes = await confirmSheet({
                title: p.email + ' entfernen?',
                lines: ['Diese Person kann sich danach nicht mehr anmelden.'],
                confirmLabel: 'Entfernen',
              });
              if (!yes) return;
              await act('Person entfernt', () => api('/parents/' + p.id, { method: 'DELETE' }));
              refresh();
            },
          }))
        : (p.id === state.parent.id ? el('span', { class: 'badge', text: 'du' }) : null)))));

  if (isPrimary) {
    const role = roleSelect('GUARDIAN', 'Rolle der neuen Person', null);
    card.append(el('form', {
      class: 'field-row add-person',
      onsubmit: async (e) => {
        e.preventDefault();
        const email = e.target.querySelector('input').value.trim();
        if (!email) return;
        await act('Person hinzugefügt', () => api('/parents', { method: 'POST', body: { email, role: role.value } }));
        refresh();
      },
    },
    el('input', { type: 'email', placeholder: 'person@beispiel.ch', autocapitalize: 'none', autocorrect: 'off', 'aria-label': 'E-Mail der neuen Person' }),
    role,
    el('button', { class: 'btn btn-primary', type: 'submit', text: 'Hinzufügen' })));
  } else {
    card.append(el('p', { class: 'muted', text: 'Nur der Haupt-Admin kann Personen hinzufügen oder Rollen ändern.' }));
  }
  return card;
}

function childrenCard() {
  return el('div', { class: 'card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Kinder' })),
    el('ul', { class: 'list' }, state.children.map((c) => el('li', {},
      el('span', { class: 'avatar', 'aria-hidden': 'true', text: initial(c.name) }),
      el('span', { class: 'label' }, el('b', { text: c.name }),
        c.birth_year ? el('small', { text: 'Jahrgang ' + c.birth_year }) : null),
      el('button', {
        class: 'btn btn-quiet', type: 'button', text: 'Umbenennen',
        onclick: async () => {
          const name = await askSheet({ title: c.name + ' umbenennen', label: 'Neuer Name', value: c.name, confirmLabel: 'Speichern' });
          if (!name) return;
          await act('Umbenannt', () => api('/children/' + c.id, { method: 'PATCH', body: { name, birth_year: c.birth_year } }));
          const refreshed = await api('/children');
          state.children = refreshed.children || [];
          renderChildSwitcher();
          refresh();
        },
      })))),
    el('button', { class: 'btn btn-soft btn-block', type: 'button', onclick: addChild }, icon('plus'), 'Kind hinzufügen'));
}

function youCard() {
  return el('div', { class: 'card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Angemeldet' })),
    el('p', { class: 'muted', text: state.parent ? state.parent.email + ' · ' + roleLabel(state.parent.role) : '' }),
    el('button', { class: 'btn btn-block', type: 'button', 'data-action': 'signout', onclick: () => signOut('Abgemeldet.') }, icon('signout'), 'Abmelden'));
}

/* ---- holidays (FR-24.6) ------------------------------------------------------ */

function holidaysCard(data) {
  if (!state.holidayDraft || !state.holidayDraft.dirty) {
    state.holidayDraft = { dirty: false, holidays: (data.holidays || []).map((h) => ({ ...h })) };
  }
  const draft = state.holidayDraft;
  const save = el('button', { class: 'btn btn-primary', type: 'button', text: 'Ferien speichern', 'data-holiday': 'save', disabled: !draft.dirty });
  const changed = () => { draft.dirty = true; save.disabled = false; };
  const restructure = (fn) => { fn(); draft.dirty = true; redraw(); };
  const field = (obj, key, attrs) => el('input', {
    ...attrs, value: obj[key] || '', 'data-field': key,
    oninput: (e) => { obj[key] = e.target.value; changed(); },
  });
  const length = (h) => {
    if (!h.starts_on || !h.ends_on) return '';
    const [a, b] = [h.starts_on, h.ends_on].map((d) => Date.UTC(...d.split('-').map((n, i) => Number(n) - (i === 1 ? 1 : 0))));
    const n = Math.round((b - a) / 86400000) + 1;
    return n > 0 ? n + (n === 1 ? ' Tag' : ' Tage') : '';
  };
  save.addEventListener('click', async () => {
    const body = { holidays: draft.holidays.map((h) => ({ ...(h.id ? { id: h.id } : {}), title: h.title, starts_on: h.starts_on, ends_on: h.ends_on })) };
    const saved = await act('Ferien gespeichert', () => api('/family/holidays', { method: 'PUT', body }));
    if (!saved) return;
    state.holidayDraft = { dirty: false, holidays: (saved.holidays || []).map((h) => ({ ...h })) };
    if (state.dataView === 'family') state.data.holidays = saved.holidays;
    redraw();
  });
  return el('div', { class: 'card holidays-card' },
    el('div', { class: 'card-head' },
      el('h2', { text: 'Ferien' }),
      el('button', { class: 'btn btn-soft', type: 'button', 'data-holiday': 'add', onclick: () => restructure(() => draft.holidays.push({ title: '', starts_on: '', ends_on: '' })) },
        icon('plus'), 'Ferien')),
    help('Für die ganze Familie.', 'Wiederkehrende Agenda-Einträge pausieren an diesen Tagen, und ebenso ein Wecker mit «Nicht in den Ferien».'),
    draft.holidays.length ? null : el('p', { class: 'muted', text: 'Noch keine Ferien eingetragen.' }),
    draft.holidays.map((h, i) => el('div', { class: 'holiday plan-group stack' },
      field(h, 'title', { type: 'text', placeholder: 'z. B. Herbstferien', 'aria-label': 'Titel' }),
      el('div', { class: 'field-row' },
        el('div', {}, el('label', { text: 'Erster Tag' }), field(h, 'starts_on', { type: 'date', 'aria-label': 'Erster Tag' })),
        el('div', {}, el('label', { text: 'Letzter Tag' }), field(h, 'ends_on', { type: 'date', 'aria-label': 'Letzter Tag' }))),
      el('div', { class: 'row' },
        el('span', { class: 'muted', text: length(h) }),
        el('button', {
          class: 'btn btn-quiet btn-danger', type: 'button', text: 'Entfernen', 'data-holiday': 'remove',
          onclick: () => restructure(() => draft.holidays.splice(i, 1)),
        })))),
    el('div', { class: 'row' }, el('span'), save));
}

/* ---- API keys (FR-17) ---------------------------------------------------- */

/* A key is the same parent, arriving without a browser. The card says so in those words rather than
 * talking about scopes, because there are none: a key reaches everything its creator reaches. The one
 * exception — it cannot mint another credential — is stated too, since it is why revoking is enough. */
function apiKeysCard(data) {
  const card = el('div', { class: 'card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'API-Schlüssel' })));

  if (!data.isPrimary) {
    card.append(el('p', { class: 'muted', text: 'Nur der Haupt-Admin kann API-Schlüssel sehen oder erstellen.' }));
    return card;
  }
  card.append(help('Ein Schlüssel lässt ein Skript oder einen Assistenten als dich handeln, ohne Browser.',
    'Er erreicht alles, was du erreichst, ausser einen weiteren Schlüssel oder eine Person zu erstellen oder zu widerrufen.'));

  const keys = data.keys || [];
  if (keys.length) {
    card.append(el('ul', { class: 'list' }, keys.map((k) => el('li', {},
      el('span', { class: 'label' },
        el('b', { text: k.name }),
        el('small', {
          class: k.revoked_at ? 'warn' : null,
          // The prefix, never the key: it identifies one in a log or a config file, and cannot be
          // used for anything.
          text: k.prefix + '… · ' + (k.revoked_at ? 'widerrufen ' + fmtTime(k.revoked_at) : 'zuletzt benutzt ' + fmtTime(k.last_used_at)),
        })),
      k.revoked_at
        ? el('button', {
          class: 'btn btn-quiet btn-danger', type: 'button', text: 'Löschen',
          onclick: async () => {
            if (!await confirmSheet({ title: k.name + ' löschen?', lines: ['Das Protokoll zeigt danach auf nichts mehr.'], confirmLabel: 'Löschen' })) return;
            await act('Schlüssel gelöscht', () => api('/api-keys/' + k.id, { method: 'DELETE' }));
            refresh();
          },
        })
        : el('button', {
          class: 'btn btn-quiet btn-danger', type: 'button', text: 'Widerrufen',
          onclick: async () => {
            if (!await confirmSheet({ title: k.name + ' widerrufen?', lines: ['Was ihn benutzt, hört sofort auf zu funktionieren.'], confirmLabel: 'Widerrufen' })) return;
            await act('Schlüssel widerrufen', () => api('/api-keys/' + k.id + '/revoke', { method: 'POST' }));
            refresh();
          },
        })))));
  } else {
    card.append(el('p', { class: 'muted', text: 'Noch keine Schlüssel.' }));
  }

  card.append(el('form', {
    class: 'field-row',
    onsubmit: async (e) => {
      e.preventDefault();
      const input = e.target.querySelector('input');
      const name = input.value.trim();
      if (!name) return;
      const created = await act('Schlüssel erstellt', () => api('/api-keys', { method: 'POST', body: { name } }));
      if (created) showKeyOnce(created);
      refresh();
    },
  },
  el('input', { type: 'text', placeholder: 'Wofür?', 'aria-label': 'Name des neuen Schlüssels', autocapitalize: 'none' }),
  el('button', { class: 'btn btn-primary', type: 'submit', text: 'Erstellen' })));
  return card;
}

/* The one time the token is readable. Only its hash is stored, so this cannot be shown again by any
 * request, by an operator, or by reading the database — and the sheet says so at the moment the value
 * is on screen, rather than in documentation nobody is reading while copying a secret. */
function showKeyOnce(key) {
  const copy = el('button', {
    class: 'btn btn-primary btn-block', type: 'button', text: 'Kopieren',
    onclick: async () => {
      try {
        await navigator.clipboard.writeText(key.token);
        toast('Kopiert');
      } catch (err) {
        // A clipboard the browser refuses is not a failure of this feature: the value is on screen.
        toast('Kopieren ging nicht — markiere den Schlüssel und kopiere ihn von Hand.', true);
      }
    },
  });
  openSheet('Diesen Schlüssel jetzt kopieren', el('div', { class: 'stack' },
    el('p', { text: 'Nur jetzt ist ' + key.name + ' lesbar. Gespeichert wird nur ein Hash, er kann also nicht nochmals gezeigt werden.' }),
    el('code', { class: 'code-block', text: key.token }),
    copy,
    el('p', { class: 'muted', text: 'Als Authorization: Bearer-Header senden. Wird er bekannt, widerrufe ihn hier — das wirkt sofort, und ein Schlüssel kann keinen weiteren erstellen, um seinen Widerruf zu überleben.' })), 'key');
}

/* ---- the command-line tool ------------------------------------------------ */

/* Rendered from whatever the server says it hosts rather than from a hardcoded list, so a deployment
   built without the cross-compile stage shows nothing instead of six dead links. */
const PLATFORMS = {
  'linux/amd64': 'Linux · x86-64',
  'linux/arm64': 'Linux · ARM64',
  'windows/amd64': 'Windows · x86-64',
  'windows/arm64': 'Windows · ARM64',
  'darwin/amd64': 'macOS · Intel',
  'darwin/arm64': 'macOS · Apple Silicon',
};

function likelyOS() {
  const ua = navigator.userAgent || '';
  if (/Windows/i.test(ua)) return 'windows';
  if (/Mac OS X|Macintosh/i.test(ua)) return 'darwin';
  if (/Linux|X11|Android/i.test(ua)) return 'linux';
  return '';
}

function cliCard(cli) {
  const card = el('div', { class: 'card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Befehlszeile' })));
  if (!cli || !cli.hosted || !(cli.artifacts || []).length) {
    card.append(el('p', { class: 'muted', text: 'Dieser Server hostet das Befehlszeilen-Werkzeug nicht.' }));
    return card;
  }
  card.append(help('fgctl ist die API dieses Servers im Terminal, Version ' + cli.version + '.',
    'Dasselbe Programm ist ein MCP-Server, den ein Assistent steuern kann. Danach: fgctl login --url ' + location.origin
    + ' — es fragt nach einem API-Schlüssel, den du oben erstellen kannst. «fgctl self-update» ersetzt es durch das, was dieser Server hostet.'));
  // The visitor's own OS first. Only the OS is guessed, never the architecture — a wrong arch hands
  // someone a binary that will not start, while a wrong order costs them one glance.
  const mine = likelyOS();
  const sorted = (cli.artifacts || []).slice().sort((a, b) => {
    const am = a.os === mine ? 0 : 1;
    const bm = b.os === mine ? 0 : 1;
    return am - bm || (a.os + a.arch).localeCompare(b.os + b.arch);
  });
  card.append(el('ul', { class: 'list' }, sorted.map((a) => el('li', {},
    el('span', { class: 'label' },
      el('b', { text: PLATFORMS[a.os + '/' + a.arch] || (a.os + ' · ' + a.arch) }),
      // The checksum is shown, not hidden behind a click: it is the only way to check a download.
      el('small', { text: (a.size / (1024 * 1024)).toFixed(1) + ' MB · sha256 ' + a.sha256 })),
    el('a', { class: 'btn btn-quiet', href: a.url, download: a.name }, icon('download'), 'Laden')))));
  return card;
}
