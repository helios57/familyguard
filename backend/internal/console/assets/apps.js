'use strict';

/* Apps — what one child's phones have, what they may do with it, and what the family installs.
 *
 * Top to bottom in the order a parent needs them: the apps waiting for a decision (with the two
 * answers almost everyone gives, Erlauben and Sperren, on the row itself), the apps on the phone
 * (one row each, the rule shown as a chip; tapping it opens the sheet with every answer explained),
 * then the apps the family installs and the family-wide blocklist.
 *
 * The row used to carry a five-way segmented control whose labels wrapped onto two lines at 360 px
 * ("Always / free"). A chip that says the current answer and a sheet that explains all of them costs
 * one more tap for the rare answer and nothing for the common ones.
 */

async function loadApps() {
  const [rules, devices, catalog, managed] = await Promise.all([
    api('/children/' + state.childId + '/app-rules'),
    api('/devices?child_id=' + encodeURIComponent(state.childId)),
    // The catalog is family-wide and the declared set is per child: "in the catalog" and "declared
    // for this child" are different facts, and the switch is the second one.
    api('/apps').catch(() => ({ apps: [], configured: false })),
    api('/children/' + state.childId + '/managed-apps').catch(() => ({ managed_apps: [] })),
  ]);
  // Family-wide, so fetched once. Tolerated as empty: one endpoint being unavailable must not blank
  // the whole page.
  const blocklist = await api('/family/blocked-packages').catch(() => ({ packages: [] }));
  const list = devices.devices || [];
  // include_system=1 is load bearing: preinstalled bloatware IS a system app — that is exactly why it
  // cannot be uninstalled and has to be hidden — so without the flag com.facebook.katana is filtered
  // out of the inventory and the blocklist reports the household's headline entry as not installed.
  const perDevice = await Promise.all(list.map((d) =>
    d.enrolled
      ? api('/devices/' + d.id + '/apps?include_system=1').catch(() => ({ apps: [] }))
      : Promise.resolve({ apps: [] })));
  // The same envelope as Übersicht — `pending_approval` lives inside `desired`, and read a level too
  // high it is undefined, which is an empty queue that looks exactly like nothing waiting.
  const desired = await Promise.all(list.map((d) =>
    d.enrolled
      ? api('/devices/' + d.id + '/desired-state').then((r) => (r && r.desired) || null).catch(() => null)
      : Promise.resolve(null)));

  /* One row per package, not per install, and COUNTED rather than first-wins: with two phones
     "hidden" is not one fact. An app the phone has stopped reporting keeps its row (the server stamps
     removed_at, so uninstalling to dodge a block stays visible) but is counted apart. */
  const byPackage = new Map();
  perDevice.forEach((res, i) => {
    const deviceId = list[i].id;
    for (const app of res.apps || []) {
      const gone = !!app.removed_at;
      const seen = byPackage.get(app.package_name);
      if (!seen) {
        byPackage.set(app.package_name, {
          ...app,
          devices: gone ? 0 : 1,
          hiddenOn: gone || !app.hidden ? 0 : 1,
          removedOn: gone ? 1 : 0,
          // Which phones' lists still carry it after it was uninstalled (FR-5.11).
          removedFrom: gone ? [deviceId] : [],
          hidden: !gone && !!app.hidden,
          suspended: !gone && !!app.suspended,
        });
      } else if (gone) {
        seen.removedOn += 1;
        seen.removedFrom.push(deviceId);
      } else {
        seen.devices += 1;
        if (app.hidden) { seen.hiddenOn += 1; seen.hidden = true; }
        if (app.suspended) seen.suspended = true;
        // Openable on any phone is openable (FR-3.12).
        if (app.launchable === true) seen.launchable = true;
        if (app.label && !seen.label) seen.label = app.label;
      }
    }
  });
  // The whole rule, not just its action: `limit_minutes` is half the answer for a LIMIT rule.
  const ruleFor = new Map((rules.rules || []).map((r) => [r.package_name, r]));
  // Which apps wait for a decision, from the authority that decides it rather than inferred from
  // "suspended and has no rule" — those come apart at bedtime, when every app is suspended.
  const pending = new Set();
  const free = new Set();
  for (const st of desired) {
    for (const pkg of ((st && st.pending_approval) || [])) pending.add(pkg);
    // Preinstalled apps nobody decided about that stay usable at any hour (FR-5.10).
    for (const pkg of ((st && st.free_by_default) || [])) free.add(pkg);
  }
  return {
    apps: [...byPackage.values()].sort(sortApps),
    ruleFor,
    pending,
    free,
    // The queue as the server reported it, kept apart from `pending`, which is edited in place as a
    // parent answers: taking an answer back has to know whether the app was waiting before.
    pendingAtLoad: new Set(pending),
    enrolled: list.some((d) => d.enrolled),
    catalog: catalog.apps || [],
    catalogConfigured: catalog.configured === true,
    managed: managed.managed_apps || [],
    blocklist: blocklist.packages || [],
  };
}

function sortApps(a, b) {
  if (a.system_app !== b.system_app) return a.system_app ? 1 : -1;
  return (a.label || a.package_name).localeCompare(b.label || b.package_name, 'de');
}

VIEWS.apps = { load: loadApps, render: renderApps, perChild: true };

/* ---- the answers (FR-5.8, FR-22) ------------------------------------------ */

/* The answers a parent can give about an app, in the order they trade freedom for control. `key` is
   this file's name for the answer and `action` is the server's: "Tageslimit" and "Eigenes Limit" are
   both LIMIT and differ only by whether an allowance rides along.

   Before these existed there were two, Allow and Block, and Allow is the whitelist — it puts an app
   outside bedtime and outside the daily limit for good. So the ordinary answer, "yes, you may have
   this, and it counts like everything else", could not be given at all. */
const CATEGORIES = [
  {
    key: 'ALLOW', action: 'ALLOW', label: 'Immer frei', done: 'Immer frei', cls: 'ok',
    hint: 'Keine Schlafenszeit, kein Tageslimit. Für Apps, die jederzeit gehen müssen.',
  },
  {
    key: 'LIMIT', action: 'LIMIT', label: 'Tageslimit', done: 'Erlaubt, mit dem Tageslimit', cls: '',
    hint: 'Erlaubt. Zählt zum Tageslimit und pausiert in der Schlafenszeit, wie jede andere App.',
  },
  {
    key: 'OWN', action: 'LIMIT', label: 'Eigenes Limit', done: 'Erlaubt, mit eigenem Limit', cls: '',
    hint: 'Erlaubt, mit einer eigenen Zeit pro Tag nur für diese App, zusätzlich zum Tageslimit.',
  },
  {
    key: 'BLOCK', action: 'BLOCK', label: 'Gesperrt', done: 'Gesperrt', cls: 'danger',
    hint: 'Auf dem Handy pausiert und versteckt.',
  },
  // FR-22. Last because it depends on something else being set up: without a daily plan nobody
  // earns anything, and a bonus app then never opens.
  {
    key: 'BONUS', action: 'BONUS', label: 'Bonus-App', done: 'Bonus-App — läuft mit Bonuszeit', cls: 'gold',
    hint: 'Geht nur, solange Bonuszeit da ist — dann auch in der Schlafenszeit oder über dem Tageslimit.',
  },
];

/* Where "Eigenes Limit" starts when it is first chosen. Starting at zero would store a rule meaning
   "no allowance" under an answer that says there is one. */
const DEFAULT_OWN_LIMIT_MINUTES = 60;

/* null means undecided — no rule at all — which with free installation off is what keeps an app
   waiting (FR-5.4). It is a real answer and not a missing one. */
function categoryOf(rule) {
  if (!rule) return null;
  if (rule.action !== 'LIMIT') return rule.action;
  return rule.limit_minutes > 0 ? 'OWN' : 'LIMIT';
}

function ruleChipText(rule) {
  const cat = categoryOf(rule);
  if (cat === null) return 'Keine Regel';
  if (cat === 'OWN') return 'Eigenes Limit ' + fmtMinutes(rule.limit_minutes);
  return CATEGORIES.find((c) => c.key === cat).label;
}

/* The app's round mark: its first letter on a colour derived from the package, so the same app is
   the same colour here and in Aktivität. */
function appMark(app) {
  return el('span', { class: 'app-mark', 'aria-hidden': 'true', style: { background: packageHue(app.package_name) }, text: initial(app.label || app.package_name) });
}

function renderApps(data) {
  const managed = managedAppsCard(data);

  if (!data.apps.length) {
    // Two different reasons for the same blank list, with different next steps: there is no phone,
    // or there is one and it has not reported yet. The managed and blocklist cards stay either way:
    // choosing what a phone should have does not depend on the phone having reported.
    return [el('div', { class: 'cols' }, managed, familyBlocklistCard(data)), data.enrolled
      ? emptyCard('apps', 'Noch keine Apps gemeldet',
        'Das Handy ist eingerichtet, hat die Liste seiner Apps aber noch nicht geschickt. Das tut es kurz nach dem Einrichten und dann einmal am Tag.')
      : emptyCard('apps', 'Noch keine Apps gemeldet',
        'Ein Handy schickt die Liste seiner Apps kurz nach dem Einrichten. Bis dahin steht hier nichts.',
        setUpAPhoneLink())];
  }

  const f = state.appFilter;

  /* One tap: one request, then the page is drawn from the answer that was just given. This used to
   * end in `refresh()` — nine requests per tap — and a parent working through a queue met "too many
   * requests" a dozen apps in. The rule is what was just sent; the re-read happens once, when the
   * tapping stops. */
  const setRule = async (app, category, minutes) => {
    const pkg = app.package_name;
    const c = CATEGORIES.find((x) => x.key === category);
    const landed = await tried(category === null ? 'Wartet wieder auf eine Entscheidung' : c.done, () =>
      category === null
        ? api('/children/' + state.childId + '/app-rules?package_name=' + encodeURIComponent(pkg), { method: 'DELETE' })
        : api('/children/' + state.childId + '/app-rules', {
          method: 'PUT',
          body: { package_name: pkg, action: c.action, limit_minutes: c.action === 'LIMIT' ? (minutes || 0) : 0 },
        }));
    // A write that did not land must never be drawn as if it had.
    if (!landed) { refresh(); return; }
    if (category === null) {
      data.ruleFor.delete(pkg);
      // Back to waiting, but only if it was waiting to begin with — the server is the authority on
      // that, which is why this reads the set as it arrived rather than guessing.
      if (data.pendingAtLoad.has(pkg)) data.pending.add(pkg);
    } else {
      data.ruleFor.set(pkg, { package_name: pkg, action: c.action, limit_minutes: c.action === 'LIMIT' ? (minutes || 0) : 0 });
      data.pending.delete(pkg);
    }
    redraw();
    if (sheetShows('rule')) {
      if (category === 'OWN') openRuleSheet(app, data, setRule);
      else closeSheet();
    }
    nudgeRefresh(1200);
  };
  data.setRule = setRule;

  const familyBlocked = new Set(data.blocklist.map((e) => e.package_name));

  /* FR-5.11: an app no phone reports any more can be taken off the list. Only then — an installed app
     would be back with the next inventory — and the rule stays, so a blocked game that is reinstalled
     is still blocked. */
  const forget = (app) => (app.devices || !(app.removedFrom || []).length) ? null : el('button', {
    class: 'btn btn-quiet', type: 'button', text: 'Aus der Liste entfernen', 'data-action': 'forget',
    'aria-label': (app.label || app.package_name) + ' aus der Liste entfernen',
    onclick: async () => {
      const ok = await tried('Aus der Liste entfernt', () => Promise.all(app.removedFrom.map((id) =>
        api('/devices/' + id + '/apps/' + encodeURIComponent(app.package_name), { method: 'DELETE' }))));
      if (ok) refresh();
    },
  });

  const row = (app) => {
    const rule = data.ruleFor.get(app.package_name) || null;
    const cat = categoryOf(rule);
    /* An app the family blocklist covers is hidden whatever this child's rule says, and saying so is
       the difference between understanding why it is missing and concluding the block did not work.
       "Immer frei" is the documented exemption, so the note names it. */
    const family = familyBlocked.has(app.package_name)
      ? el('small', { text: (rule && rule.action) === 'ALLOW'
        ? 'Für die ganze Familie gesperrt — für dieses Kind erlaubt'
        : 'Für die ganze Familie gesperrt. «Immer frei» macht eine Ausnahme für dieses Kind.' })
      : null;
    // Reported for every app, not only blocked ones: bedtime hides nothing but suspends everything,
    // and "why is this greyed out" is the same question.
    const restrained = !app.devices
      ? el('small', { text: 'Nicht mehr auf dem Handy.' })
      : !rule && data.free.has(app.package_name)
        ? el('small', { text: 'Immer frei (vorinstalliert) — wähle eine Regel, um das zu ändern.' })
        : rule && rule.action === 'BONUS'
          ? el('small', { text: 'Bonus-App: läuft nur mit Bonuszeit aus dem Tagesplan.' })
          : app.hidden
            ? el('small', { text: 'Gerade auf dem Handy versteckt.' })
            : app.suspended
              ? el('small', { text: 'Gerade auf dem Handy pausiert.' })
              : null;
    const chipClass = cat === null ? (data.pending.has(app.package_name) ? 'warn' : '') : CATEGORIES.find((c) => c.key === cat).cls;
    return el('li', { 'data-package': app.package_name },
      appMark(app),
      el('span', { class: 'label' },
        el('b', { text: app.label || app.package_name }),
        el('small', { text: app.package_name + (app.system_app ? ' · System' : '') }),
        family,
        restrained,
        forget(app)),
      el('button', {
        class: 'btn rule-chip chip ' + chipClass, type: 'button', 'data-rule-chip': cat || 'none',
        'aria-label': 'Regel für ' + (app.label || app.package_name) + ': ' + ruleChipText(rule),
        onclick: () => openRuleSheet(app, data, setRule),
      }, ruleChipText(rule), icon('chevron-right', 'icon-sm')));
  };

  const matches = (app) => {
    const rule = data.ruleFor.get(app.package_name) || null;
    // Preinstalled apps with an icon — Kamera, Galerie, Chrome — are apps a child opens and a parent
    // decides about. Only the services with no icon wait behind the switch. A system app that is
    // hidden or already has a rule stays visible, so a parent can always find what they decided.
    const openable = app.launchable === true || app.hidden || rule !== null;
    if (!f.system && app.system_app && !openable) return false;
    const action = rule && rule.action;
    if (f.rule === 'allowed' && action !== 'ALLOW') return false;
    if (f.rule === 'blocked' && action !== 'BLOCK') return false;
    if (f.rule === 'none' && rule !== null) return false;
    if (f.rule === 'waiting' && !data.pending.has(app.package_name)) return false;
    const q = f.q.trim().toLowerCase();
    if (!q) return true;
    return (app.label || '').toLowerCase().includes(q) || app.package_name.toLowerCase().includes(q);
  };

  const list = el('ul', { class: 'list applist' });
  const count = el('p', { class: 'list-count' });

  /* Repainting only the list, never the toolbar: rebuilding the search field on every keystroke would
     move the caret to the end of it, which makes correcting a typo impossible. */
  const paint = () => {
    const shown = data.apps.filter(matches);
    count.textContent = shown.length === data.apps.length
      ? data.apps.length + (data.apps.length === 1 ? ' App' : ' Apps')
      : shown.length + ' von ' + data.apps.length + ' Apps';
    list.replaceChildren(...shown.map(row));
    if (!shown.length) list.append(el('li', {}, el('span', { class: 'muted', text: 'Nichts passt zu diesem Filter.' })));
  };

  const search = el('input', {
    type: 'search', value: f.q, placeholder: 'Apps suchen',
    'aria-label': 'Apps suchen', autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false',
    oninput: (e) => { f.q = e.target.value; paint(); },
  });

  const filter = el('div', { class: 'filters', role: 'group', 'aria-label': 'Filter' }, [
    ['all', 'Alle'], ['waiting', 'Wartend'], ['blocked', 'Gesperrt'], ['allowed', 'Immer frei'], ['none', 'Keine Regel'],
  ].map(([value, label]) => el('button', {
    class: 'pill', type: 'button', text: label, 'data-filter': value,
    'aria-pressed': String(f.rule === value),
    onclick: (e) => {
      f.rule = value;
      for (const b of e.currentTarget.parentNode.children) b.setAttribute('aria-pressed', String(b === e.currentTarget));
      paint();
    },
  })));

  const system = el('label', { class: 'switch' },
    el('span', { class: 'switch-label' }, 'Hintergrunddienste zeigen',
      el('small', { text: 'Teile von Android ohne Symbol — nichts, was ein Kind öffnen kann.' })),
    el('input', {
      type: 'checkbox', checked: f.system,
      onchange: (e) => { f.system = e.target.checked; paint(); },
    }));

  paint();

  return [
    pendingApprovalCard(data),
    el('div', { class: 'card full' },
      el('div', { class: 'card-head' }, el('h2', { text: 'Apps auf dem Handy' })),
      help('Was das Handy meldet. Eine Regel hier installiert oder entfernt nichts.'),
      el('div', { class: 'toolbar' }, search, filter, system),
      count,
      list),
    el('div', { class: 'cols' }, managed, familyBlocklistCard(data)),
  ];
}

/* The sheet with every answer, each with one line saying what it does. The chosen one is marked, and
   "Eigenes Limit" shows its minutes in the sheet, where there is room to explain them. */
function openRuleSheet(app, data, setRule) {
  const rule = data.ruleFor.get(app.package_name) || null;
  const cat = categoryOf(rule);
  const own = (rule && rule.limit_minutes) || DEFAULT_OWN_LIMIT_MINUTES;
  const option = (key, label, hint, onpick) => el('button', {
    class: 'rule-option', type: 'button', 'data-rule': key, 'aria-pressed': String(cat === key || (key === 'none' && cat === null)),
    onclick: onpick,
  }, el('span', {}, el('b', { text: label }), el('small', { text: hint })));

  const options = CATEGORIES.map((c) => option(c.key, c.label, c.hint, () => setRule(app, c.key, c.key === 'OWN' ? own : 0)));
  // Undecided has an answer of its own, offered only once a decision exists: tapping the lit answer
  // again must not be the hidden way back to waiting.
  if (cat !== null) {
    options.push(option('none', 'Keine Regel', 'Zählt zum Tageslimit; kam die App nach dem Einrichten, wartet sie wieder auf deine Entscheidung.', () => setRule(app, null, 0)));
  }

  /* The number only exists while its answer is selected: a minutes field next to an app that is
     always free reads as a limit nobody enforces, and the server refuses that pair outright. */
  const minutes = cat !== 'OWN' ? null : el('label', { class: 'own-limit' },
    el('span', { class: 'switch-label', text: 'Minuten pro Tag, nur für diese App' }),
    el('input', {
      type: 'number', min: '1', max: '1440', step: '5', value: String(own),
      'aria-label': 'Minuten pro Tag für ' + (app.label || app.package_name),
      onchange: (e) => {
        const n = Math.round(Number(e.target.value));
        if (!Number.isFinite(n) || n < 1 || n > 1440) {
          // Put the stored value back rather than sending one the server will refuse: a field that
          // keeps a rejected number looks saved.
          e.target.value = String(own);
          toast('Minuten müssen zwischen 1 und 1440 liegen.');
          return;
        }
        setRule(app, 'OWN', n);
      },
    }));

  openSheet(app.label || app.package_name, el('div', { class: 'stack' },
    el('p', { class: 'muted', text: app.package_name }),
    el('div', { class: 'rule-options' }, options),
    minutes), 'rule');
}

/* The apps waiting for a decision, above everything else.
 *
 * They were once in the list below — among about five hundred rows, with nothing marking them — and
 * a queue nobody can see is a queue that does not get worked: on 2026-09-20 four apps had waited long
 * enough for the parent to conclude the phone was broken. Rendered only when non-empty. */
function pendingApprovalCard(data) {
  const waiting = data.apps.filter((a) => data.pending.has(a.package_name));
  if (!waiting.length) return null;
  return el('div', { class: 'card full pending-card' },
    el('div', { class: 'card-head' },
      el('h2', { text: 'Wartet auf deine Entscheidung' }),
      el('span', { class: 'badge warn', 'data-count': String(waiting.length), text: String(waiting.length) })),
    help('Diese kamen nach dem Einrichten aufs Handy und sind pausiert, bis du antwortest.',
      'Bis dahin sieht das Kind sie installiert und kann sie nicht öffnen — eine App, die hier liegen bleibt, sieht aus wie ein kaputtes Handy.'),
    el('ul', { class: 'list applist' }, waiting.map((app) => el('li', { class: 'pending', 'data-package': app.package_name },
      appMark(app),
      el('span', { class: 'label' },
        el('b', { text: app.label || app.package_name }),
        el('small', { text: app.package_name })),
      el('div', { class: 'pending-actions' },
        // "Erlauben" is the ordinary yes: the app counts like every other. "Immer frei" is the
        // exemption, and it lives in the sheet with the rest.
        el('button', { class: 'btn btn-primary', type: 'button', 'data-rule': 'LIMIT', onclick: () => data.setRule(app, 'LIMIT', 0) }, icon('check'), 'Erlauben'),
        el('button', { class: 'btn btn-danger', type: 'button', 'data-rule': 'BLOCK', onclick: () => data.setRule(app, 'BLOCK', 0) }, icon('x'), 'Sperren')),
      el('button', { class: 'btn btn-quiet btn-block', type: 'button', 'data-action': 'more-rules', onclick: () => openRuleSheet(app, data, data.setRule) }, 'Andere Regel …')))));
}

/* ---- the applications a parent chooses (FR-16) --------------------------- */

/* One row per package, newest build first: a parent chooses an APPLICATION, and the phone is sent one
   version of it. */
function catalogByPackage(apps) {
  const byPackage = new Map();
  for (const a of apps) {
    const seen = byPackage.get(a.package_name);
    if (!seen) byPackage.set(a.package_name, { newest: a, versions: [a] });
    else {
      seen.versions.push(a);
      if (a.version_code > seen.newest.version_code) seen.newest = a;
    }
  }
  return [...byPackage.values()]
    .sort((x, y) => (x.newest.label || x.newest.package_name).localeCompare(y.newest.label || y.newest.package_name, 'de'));
}

/* Two lists that look alike and are not: this one is what a parent DECIDES the phone should have,
 * the one above is what the phone REPORTS it has. Each says which it is, because a parent who
 * confuses them either blocks an app expecting it to be removed or withdraws one expecting it to be
 * merely hidden. */
function managedAppsCard(data) {
  const card = el('div', { class: 'card' },
    el('div', { class: 'card-head' },
      el('h2', { text: 'Apps, die du installierst' }),
      el('button', { class: 'btn btn-quiet', type: 'button', text: 'Katalog', 'data-action': 'catalog', onclick: () => openCatalogSheet(data) })));

  if (!data.catalogConfigured) {
    // Not an error, and not a blank list: "empty" and "not set up" need different actions.
    card.append(el('p', { class: 'muted', text: 'Dieser Server ist nicht zum Hosten von Apps eingerichtet. Setze APK_DIR auf der Steuerzentrale und gib ihm einen beschreibbaren Ordner.' }));
    return card;
  }

  const declared = new Set(data.managed.map((m) => m.package_name));
  const groups = catalogByPackage(data.catalog);
  // A package declared whose build is no longer in the catalog: the phone is told nothing about it,
  // so it must be visible — silence here is an app a parent believes they installed.
  const orphans = data.managed.filter((m) => !m.available);

  if (!groups.length && !orphans.length) {
    card.append(el('p', { class: 'muted', text: 'Noch keine App für diese Familie hinzugefügt.' }));
    card.append(el('button', { class: 'btn btn-soft btn-block', type: 'button', 'data-action': 'catalog', onclick: () => openCatalogSheet(data) }, icon('plus'), 'App hinzufügen'));
    return card;
  }

  const toggle = async (pkg, on) => {
    await act(on ? 'Wird auf diesem Handy installiert' : 'Von diesem Handy entfernt', () =>
      on
        ? api('/children/' + state.childId + '/managed-apps/' + encodeURIComponent(pkg), { method: 'PUT' })
        : api('/children/' + state.childId + '/managed-apps/' + encodeURIComponent(pkg), { method: 'DELETE' }));
    refresh();
  };

  card.append(el('ul', { class: 'list' },
    groups.map(({ newest, versions }) => el('li', {},
      el('span', { class: 'label' },
        el('b', { text: newest.label || newest.package_name }),
        el('small', {
          text: newest.package_name + ' · ' + (newest.version_name || 'Build ' + newest.version_code) +
            (versions.length > 1 ? ' · ' + versions.length + ' Builds' : '') +
            (newest.size_bytes ? ' · ' + fmtSize(newest.size_bytes) : ''),
        })),
      el('label', { class: 'switch switch-bare' },
        el('input', {
          type: 'checkbox',
          checked: declared.has(newest.package_name),
          'aria-label': (newest.label || newest.package_name) + ' auf diesem Handy installieren',
          onchange: (e) => toggle(newest.package_name, e.target.checked),
        })))),
    orphans.map((m) => el('li', {},
      el('span', { class: 'label' },
        el('b', { text: m.package_name }),
        el('small', { class: 'warn', text: 'für dieses Handy gewählt, aber keine Version davon ist im Katalog — es wird nichts installiert' })),
      el('button', { class: 'btn btn-quiet btn-danger', type: 'button', text: 'Entfernen', onclick: () => toggle(m.package_name, false) })))));

  card.append(help('Das Handy installiert diese selbst.', 'Wird eine entfernt, installiert das Handy sie wieder. Schlafenszeit und App-Regeln gelten auch für sie.'));
  return card;
}

/* The catalog itself: what this family can install, and the two ways in. A sheet rather than a
   page, because adding an application is a rare administrative act; the thing a parent does often
   is the switch on the card behind it. */
function openCatalogSheet(data) {
  const status = el('p', { class: 'muted' });
  const say = (message, isError) => {
    status.textContent = message;
    status.className = isError ? 'warn' : 'muted';
  };

  const file = el('input', { type: 'file', accept: '.apk,application/vnd.android.package-archive', 'aria-label': 'APK-Datei wählen' });
  const label = el('input', { type: 'text', placeholder: 'Name (optional)', 'aria-label': 'Name für diese App' });

  const form = el('form', {
    class: 'stack',
    onsubmit: async (e) => {
      e.preventDefault();
      const chosen = file.files && file.files[0];
      if (!chosen) { say('Wähle zuerst eine APK-Datei.', true); return; }
      say(chosen.name + ' wird hochgeladen …');
      // Nothing here names the package: the server reads it, the version and the signer out of the
      // archive. A name typed in the box is a display label and cannot change what is installed.
      const out = await act(chosen.name + ' hinzugefügt', () => upload('/apps', chosen, label.value.trim()));
      if (out) { closeSheet(); refresh(); }
    },
  },
  el('label', { class: 'field' }, el('span', { text: 'APK hochladen' }), file),
  label,
  el('button', { class: 'btn btn-primary btn-block', type: 'submit', text: 'Hochladen' }));

  const scan = el('button', {
    class: 'btn btn-block', type: 'button', text: 'Server-Ordner durchsuchen',
    onclick: async () => {
      const out = await act('Ordner durchsucht', () => api('/apps/scan', { method: 'POST' }));
      if (!out) return;
      const failed = Object.entries(out.failed || {});
      // Named rather than counted: the filename and the reason send an operator to the one file
      // that is wrong.
      if (failed.length) say(failed.map(([name, why]) => name + ': ' + why).join('\n'), true);
      else say((out.registered || []).length + ' neue App(s) registriert.');
      refresh();
    },
  });

  const rows = catalogByPackage(data.catalog).flatMap(({ versions }) =>
    versions.sort((a, b) => b.version_code - a.version_code).map((a) => el('li', {},
      el('span', { class: 'label' },
        el('b', { text: (a.label || a.package_name) + ' ' + (a.version_name || a.version_code) }),
        el('small', {
          text: a.package_name + ' · Build ' + a.version_code + ' · ' + fmtSize(a.size_bytes) +
            ' · ab Android-SDK ' + a.min_sdk + ' · ' + (a.source === 'NODE' ? 'aus dem Server-Ordner' : 'hochgeladen'),
        })),
      el('button', {
        class: 'btn btn-quiet btn-danger', type: 'button', text: 'Löschen',
        onclick: async () => {
          const yes = await confirmSheet({
            title: 'Aus dem Katalog löschen?',
            lines: [a.package_name + ', Build ' + a.version_code + '.'],
            confirmLabel: 'Löschen',
          });
          if (!yes) return;
          await act('Gelöscht', () => api('/apps/' + a.id, { method: 'DELETE' }));
          refresh();
        },
      }))));

  openSheet('App-Katalog', el('div', { class: 'stack' },
    form,
    el('p', { class: 'muted', text: 'Oder .apk-Dateien in den App-Ordner des Servers kopieren und ihn durchsuchen. Nichts wird dem Dateinamen geglaubt — Paket, Version und Signatur werden aus der Datei gelesen.' }),
    scan,
    status,
    el('h3', { class: 'section-title', text: 'Im Katalog' }),
    rows.length ? el('ul', { class: 'list' }, rows) : el('p', { class: 'muted', text: 'Noch nichts.' })), 'catalog');
}

/* ---- the family blocklist (FR-18) ---------------------------------------
 *
 * Its own card rather than another answer on each app row: the row is about one child, this is about
 * the household. Nothing here uninstalls. Entries are hidden and suspended, which survives a
 * reinstall and is undone by removing the entry.
 */

/* What the phones say about one blocked package. Three states and not two: "not installed here" is a
   working entry, "hidden" is the phone confirming, and "not hidden yet" is the only one that needs a
   parent — normal for the minute before the phone's next sync, so it says what it waits for. */
function blocklistState(seen) {
  if (!seen || (!seen.devices && !seen.removedOn)) return 'Auf keinem Handy hier installiert.';
  if (!seen.devices) return 'Deinstalliert — der Eintrag verhindert, dass sie zurückkommt.';
  if (seen.hiddenOn >= seen.devices) {
    return seen.devices > 1 ? 'Auf allen ' + seen.devices + ' Handys versteckt.' : 'Auf dem Handy versteckt.';
  }
  if (seen.hiddenOn === 0) return 'Auf dem Handy und noch nicht versteckt — wartet auf die Synchronisation.';
  return 'Auf ' + seen.hiddenOn + ' von ' + seen.devices + ' Handys versteckt.';
}

function familyBlocklistCard(data) {
  const canEdit = isAdmin();
  const card = el('div', { class: 'card blocklist-card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Für alle gesperrt' })),
    help(canEdit ? 'Gilt für jedes Kind, auch für später hinzugefügte.' : 'Gilt für jedes Kind. Ändern kann es ein Admin.',
      'Diese Apps sind versteckt und laufen nicht; sie werden nicht deinstalliert, und wer einen Eintrag entfernt, bekommt die App zurück.'));

  const remove = async (pkg) => {
    await act('Für alle entsperrt', () =>
      api('/family/blocked-packages?package_name=' + encodeURIComponent(pkg), { method: 'DELETE' }));
    refresh();
  };

  const reported = new Map(data.apps.map((a) => [a.package_name, a]));
  const entry = (e) => {
    const seen = reported.get(e.package_name);
    return el('li', { 'data-package': e.package_name },
      el('span', { class: 'label' },
        el('b', { text: e.label || (seen && seen.label) || e.package_name }),
        el('small', { text: e.package_name + (e.source === 'BUILTIN' ? ' · Vorschlag' : '') }),
        // Read back from the phone, never from the rule (FR-18.6): a parent asking "is the bloatware
        // gone?" is asking what the device says it is doing.
        el('small', { class: seen && seen.hiddenOn === 0 ? 'warn' : '', text: blocklistState(seen) }),
        e.reason ? el('small', { text: e.reason }) : null),
      canEdit ? el('button', { class: 'btn', type: 'button', text: 'Entsperren', onclick: () => remove(e.package_name) }) : null);
  };

  // The built-in suggestions are the long tail — Meta's and Microsoft's preinstall machinery — and
  // most of them are on no phone here. Those that ARE on a phone are shown; the rest fold away.
  const present = (e) => { const seen = reported.get(e.package_name); return seen && (seen.devices || seen.removedOn); };
  const own = data.blocklist.filter((e) => e.source !== 'BUILTIN' || present(e));
  const quiet = data.blocklist.filter((e) => e.source === 'BUILTIN' && !present(e));
  if (own.length) card.append(el('ul', { class: 'list' }, own.map(entry)));
  if (quiet.length) {
    card.append(el('details', { class: 'help' },
      el('summary', {}, icon('info', 'icon-sm'), el('span', { text: quiet.length + ' weitere Vorschläge, auf keinem Handy hier' })),
      el('ul', { class: 'list' }, quiet.map(entry))));
  }
  if (!data.blocklist.length) card.append(el('p', { class: 'muted', text: 'Für die ganze Familie ist nichts gesperrt.' }));
  if (!canEdit) return card;

  /* The datalist is the point of putting this on the Apps page: a parent picks from what their own
     phones reported rather than typing a package name from memory. Free text still works, because an
     app that is not installed yet is exactly the one worth blocking before it arrives. */
  const blocked = new Set(data.blocklist.map((e) => e.package_name));
  const listID = 'blocklist-candidates';
  const input = el('input', {
    type: 'text', placeholder: 'Paketname, z. B. com.facebook.katana', list: listID,
    'aria-label': 'Paket für die ganze Familie sperren',
    autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false',
  });
  const datalist = el('datalist', { id: listID },
    data.apps.filter((a) => !blocked.has(a.package_name)).map((a) => el('option', {
      value: a.package_name,
      label: a.label && a.label !== a.package_name ? a.label : '',
    })));

  card.append(el('form', {
    class: 'toolbar',
    onsubmit: async (e) => {
      e.preventDefault();
      const pkg = input.value.trim();
      if (!pkg) return;
      const seen = reported.get(pkg);
      // `tried`, not `act`: an endpoint answering 204 returns null through `act`, which would read as
      // a failure and leave the field full.
      const ok = await tried('Für alle gesperrt', () => api('/family/blocked-packages', {
        method: 'PUT',
        body: { package_name: pkg, label: seen ? seen.label || '' : '', reason: '' },
      }));
      if (ok) { input.value = ''; refresh(); }
    },
  }, input, datalist, el('button', { class: 'btn btn-primary', type: 'submit', text: 'Für alle sperren' })));
  return card;
}
