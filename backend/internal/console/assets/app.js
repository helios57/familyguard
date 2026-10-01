'use strict';

/* Family Guard console — the shell.
 *
 * Plain JavaScript, no build step and no dependencies. That is a security decision before it is a
 * convenience one: the page runs under `script-src 'self'` with no inline script, so there is no
 * bundler output to audit, no third-party code in this origin, and the files you read here are
 * byte-for-byte the files the browser runs.
 *
 * Six classic scripts share one scope, loaded with `defer` in the order index.html lists them: this
 * one (state, API, helpers, shell, router, live updates, sheet), then one per view, each of which
 * registers itself in VIEWS. boot() runs on DOMContentLoaded, after every one of them.
 *
 * The session token lives in localStorage, never in a cookie. A cookie would be sent automatically
 * with every request from anywhere, which is what makes CSRF possible; a token the page has to
 * attach by hand cannot be replayed by another site.
 *
 * One language: German, Swiss spelling (ss, never ß), 24-hour times. Units stay the SI symbols
 * "min" and "h", which are German too.
 */

const API = '/api/v1';
const SESSION_KEY = 'fg.session';
const CHILD_KEY = 'fg.child';

/* The list offered by the button in Regeln › Schutz, and the only one this project names.
 *
 * A URL, never the bytes: AdGuard's lists are GPL-3.0 and FamilyGuard is MIT, so shipping the data
 * would relicense the repo. The phone fetches it directly, and a parent can replace it with any
 * AdGuard- or hosts-style list they prefer. */
const AD_FILTER_SUGGESTED_LIST = 'https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt';

const state = {
  session: null,
  parent: null,
  family: null,
  // What DPC this deployment hosts, so a phone's reported build can be compared with something.
  // Null until /dpc answers, and `{hosted:false}` on a control plane that serves no APK — both are
  // drawn as "nothing to say about updates" rather than as a phone that is up to date.
  dpc: null,
  children: [],
  childId: null,
  data: {},          // per-view payload
  view: 'overview',
  sub: null,         // a view's sub-page: Regeln › zeit | schutz | agenda
  stream: null,
  // Filtering is a property of the person looking, not of the data, so it lives here and survives
  // the re-render a server event triggers. A parent who has typed "tik" into the app search does
  // not want a heartbeat to clear it.
  appFilter: { q: '', rule: 'all', system: false },
  // Which day the Aktivität timeline is showing. Null means "whatever the child's today is", which
  // only the server can answer — the child's timezone is policy, and the parent may be in another.
  timelineDay: null,
  timelineToday: null,
};

/* Each view file adds itself here: { load, render, perChild }. `perChild` views need a child
   selected and show the child switcher; Übersicht and Familie are about the whole family. */
const VIEWS = {};

/* ---- session ------------------------------------------------------------ */

function readSession() {
  try {
    const raw = localStorage.getItem(SESSION_KEY);
    if (!raw) return null;
    const s = JSON.parse(raw);
    // An expired token is discarded here rather than being sent and rejected: the first request of
    // every page load would otherwise be a guaranteed 401.
    if (!s.token || !s.expires || new Date(s.expires) <= new Date()) return null;
    return s;
  } catch (_) {
    return null;
  }
}

/* takeSessionFromHash consumes the fragment the sign-in redirect left behind.
 *
 * The fragment is cleared with replaceState immediately, so the token does not sit in the address
 * bar, in the back stack, or in whatever the parent pastes into a chat later. */
function takeSessionFromHash() {
  const hash = location.hash.slice(1);
  if (!hash.includes('token=') && !hash.includes('error=')) return null;
  const p = new URLSearchParams(hash);
  history.replaceState(null, '', location.pathname + location.search);

  if (p.get('error')) {
    const msg = p.get('error') === 'not_a_parent'
      ? 'Dieses Google-Konto gehört zu keinem Elternteil dieser Familie. Ein Admin kann es hinzufügen.'
      : 'Die Anmeldung hat nicht geklappt. Bitte versuche es nochmals.';
    document.getElementById('signin-message').textContent = msg;
    return null;
  }
  const s = { token: p.get('token'), expires: p.get('expires') };
  if (!s.token) return null;
  localStorage.setItem(SESSION_KEY, JSON.stringify(s));
  return s;
}

function signOut(message) {
  localStorage.removeItem(SESSION_KEY);
  state.session = null;
  if (state.stream) { state.stream.abort(); state.stream = null; }
  document.getElementById('app').hidden = true;
  document.getElementById('signin').hidden = false;
  if (message) document.getElementById('signin-message').textContent = message;
}

/* ---- api ---------------------------------------------------------------- */

class ApiError extends Error {
  constructor(status, code, message) {
    super(message || code || ('HTTP ' + status));
    this.status = status;
    this.code = code;
  }
}

const SESSION_EXPIRED = 'Deine Sitzung ist abgelaufen. Bitte melde dich neu an.';

async function api(path, options = {}) {
  const headers = Object.assign({ 'Accept': 'application/json' }, options.headers || {});
  if (state.session) headers['Authorization'] = 'Bearer ' + state.session.token;
  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    options = Object.assign({}, options, { body: JSON.stringify(options.body) });
  }
  // `fetch` is looked up at the moment of the call, never kept in a variable: the browser tests
  // replace window.fetch to count and delay requests, and a saved copy would bypass them.
  const res = await fetch(API + path, Object.assign({}, options, { headers }));
  if (res.status === 401) {
    signOut(SESSION_EXPIRED);
    throw new ApiError(401, 'unauthorized', 'session expired');
  }
  if (res.status === 204) return null;
  const text = await res.text();
  const body = text ? JSON.parse(text) : null;
  if (!res.ok) throw new ApiError(res.status, body && body.error, body && body.message);
  return body;
}

/* upload sends a file, and is the one request that is not JSON.
 *
 * `api` above serialises its body and sets a JSON content type, which is exactly wrong for an APK.
 * Written as a second function rather than as a flag on the first because the two share nothing but
 * the bearer: no Accept negotiation, no 204, and a body that must not be read into a string. */
async function upload(path, file, label) {
  const form = new FormData();
  form.append('apk', file, file.name);
  if (label) form.append('label', label);
  const headers = { 'Accept': 'application/json' };
  if (state.session) headers['Authorization'] = 'Bearer ' + state.session.token;
  // No Content-Type: the browser sets it, with the multipart boundary. Setting it by hand produces
  // a boundary-less header and a body the server cannot parse, and the failure blames the file.
  const res = await fetch(API + path, { method: 'POST', headers, body: form });
  if (res.status === 401) {
    signOut(SESSION_EXPIRED);
    throw new ApiError(401, 'unauthorized', 'session expired');
  }
  const text = await res.text();
  const body = text ? JSON.parse(text) : null;
  if (!res.ok) throw new ApiError(res.status, body && body.error, body && body.message);
  return body;
}

/* ---- helpers ------------------------------------------------------------ */

const el = (tag, attrs = {}, ...kids) => {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'class') n.className = v;
    else if (k === 'text') n.textContent = v;
    else if (k.startsWith('on')) n.addEventListener(k.slice(2), v);
    // A style ATTRIBUTE is blocked by this console's own CSP and fails SILENTLY: `style-src 'self'`
    // is the fallback for `style-src-attr`, so `setAttribute('style', …)` leaves the attribute in
    // the DOM with an EMPTY declaration behind it. Nothing throws, nothing is logged where the page
    // can see it, and the element simply renders with whatever the stylesheet gave it. That is how
    // the quota meter came to draw full at every level of usage for the whole life of this console.
    // CSSOM is not restricted by CSP, so every computed dimension goes through setProperty.
    else if (k === 'style') {
      if (typeof v === 'string') {
        throw new TypeError('el(): style must be an object — a style attribute is dropped by the CSP');
      }
      for (const [prop, value] of Object.entries(v)) n.style.setProperty(prop, value);
    } else n.setAttribute(k, v === true ? '' : String(v));
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    n.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return n;
};

/* An icon from the sprite in index.html. Decorative by default: the control it sits in carries the
   words, so a screen reader hears the label once rather than a symbol name and the label. */
const SVG_NS = 'http://www.w3.org/2000/svg';
function icon(name, extra) {
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('class', 'icon' + (extra ? ' ' + extra : ''));
  svg.setAttribute('aria-hidden', 'true');
  const use = document.createElementNS(SVG_NS, 'use');
  use.setAttribute('href', '#i-' + name);
  svg.append(use);
  return svg;
}

let toastTimer = null;
function toast(message, isError) {
  const t = document.getElementById('toast');
  t.textContent = message;
  t.className = 'toast' + (isError ? ' error' : '');
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, isError ? 6000 : 2800);
}

/* act runs a mutation and always reports what happened.
 *
 * A silent catch here would be the console's version of the failure this project is written
 * against: the parent taps "Sperren", nothing changes, and nothing says so. */
async function act(label, fn) {
  try {
    const out = await fn();
    toast(label);
    return out;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return null;
    toast(label + ' — fehlgeschlagen: ' + err.message, true);
    return null;
  }
}

/* Did the write land? `act` answers with the response body, and an endpoint that answers 204
 * returns null through it — which is the same value it returns when the request FAILED. A caller
 * that is about to redraw the page from what it just wrote has to know the difference, so it asks
 * this instead. */
async function tried(label, fn) {
  try {
    await fn();
    toast(label);
    return true;
  } catch (err) {
    if (!(err instanceof ApiError && err.status === 401)) toast(label + ' — fehlgeschlagen: ' + err.message, true);
    return false;
  }
}

const fmtTime = (iso) => {
  if (!iso) return 'nie';
  const d = new Date(iso);
  const mins = Math.round((Date.now() - d.getTime()) / 60000);
  if (mins < 1) return 'gerade eben';
  if (mins < 60) return 'vor ' + mins + ' min';
  if (mins < 60 * 24) return 'vor ' + Math.round(mins / 60) + ' h';
  return d.toLocaleDateString('de-CH');
};

/* The same instant, forwards: "in 30 min". */
const fmtIn = (iso) => {
  const mins = Math.round((new Date(iso).getTime() - Date.now()) / 60000);
  if (mins < 1) return 'gleich';
  if (mins < 60) return 'in ' + mins + ' min';
  return 'in ' + Math.round(mins / 60) + ' h';
};

const fmtClock = (iso) => new Date(iso).toLocaleTimeString('de-CH', { hour: '2-digit', minute: '2-digit' });

/* Megabytes, one decimal, because an APK is the one number in this console a parent compares
 * against their own phone's storage. */
const fmtSize = (bytes) => {
  if (!bytes) return '';
  const mb = bytes / (1024 * 1024);
  return (mb >= 10 ? Math.round(mb) : Math.round(mb * 10) / 10) + ' MB';
};

const fmtMinutes = (m) => {
  if (!m) return '0 min';
  const h = Math.floor(m / 60);
  // "1 h", not "1 h 0 min": a round hour is the common case — it is what most limits are.
  return h ? h + ' h' + (m % 60 ? ' ' + (m % 60) + ' min' : '') : m + ' min';
};

const WEEKDAYS_SHORT = ['Mo', 'Di', 'Mi', 'Do', 'Fr', 'Sa', 'So'];
const WEEKDAYS_LONG = ['Montag', 'Dienstag', 'Mittwoch', 'Donnerstag', 'Freitag', 'Samstag', 'Sonntag'];

/* `2026-10-03` as a day is said here: "Sa 3.10.". Parsed as a calendar date, never through Date's
   ISO parser, which reads a bare date as UTC midnight and names the day before everywhere west of
   Greenwich. */
const fmtDayDe = (iso) => {
  const [y, m, d] = iso.split('-').map(Number);
  const at = new Date(y, m - 1, d);
  return ['So', 'Mo', 'Di', 'Mi', 'Do', 'Fr', 'Sa'][at.getDay()] + ' ' + d + '.' + m + '.';
};
const fmtDate = fmtDayDe;

/* One sitting's length. Seconds below a minute rather than "0 min": this is a single interval with
 * a start and an end a parent can see on the strip, and rounding it to nothing would contradict the
 * picture next to it. */
const fmtDuration = (seconds) => {
  const s = Math.max(0, Math.round(seconds || 0));
  if (s < 60) return s + ' s';
  return fmtMinutes(Math.round(s / 60));
};

/* "07:00–08:00", or "ganzer Tag" for a window that is the whole day. */
function fmtWindow(from, to) {
  return from === '00:00' && (to === '23:59' || to === '24:00') ? 'ganzer Tag' : from + '–' + to;
}

/* A weekday bitmask (bit 0 = Monday) as a person says it: "täglich", "Mo–Fr", "Sa, So", "Mo, Mi–Fr". */
function fmtDays(mask) {
  if ((mask & 127) === 127) return 'täglich';
  if (!(mask & 127)) return 'keine Tage';
  const runs = [];
  for (let i = 0; i < 7; i++) {
    if (!(mask & (1 << i))) continue;
    let j = i;
    while (j + 1 < 7 && (mask & (1 << (j + 1)))) j++;
    runs.push(j - i >= 2 ? WEEKDAYS_SHORT[i] + '–' + WEEKDAYS_SHORT[j]
      : j > i ? WEEKDAYS_SHORT[i] + ', ' + WEEKDAYS_SHORT[j] : WEEKDAYS_SHORT[i]);
    i = j;
  }
  return runs.join(', ');
}

/* An editor that is one line until opened: the summary says what the entry IS ("Schule · Mo–Fr ·
   08:00–12:00"), the fields are inside. `item._open` keeps it open across redraws and marks a new
   entry, which opens by itself; it is never sent to the server, because every save builds its body
   field by field. */
function editorBox(item, title, meta, ...content) {
  const box = el('details', { class: 'editor', open: !!item._open },
    el('summary', {},
      el('span', { class: 'grow' }, el('b', { text: title }), el('small', { text: meta })),
      icon('chevron-right', 'icon-sm')),
    el('div', { class: 'editor-body stack' }, content));
  box.addEventListener('toggle', () => { item._open = box.open; });
  return box;
}

/** `2026-09-20` plus or minus whole days, done in UTC where a day is always 86400000 ms. */
function shiftDay(day, by) {
  const [y, m, d] = day.split('-').map(Number);
  const at = new Date(Date.UTC(y, m - 1, d) + by * 86400000);
  return at.toISOString().slice(0, 10);
}

/* `YYYY-MM-DD` plus days in the profile's timezone. Intl gives the date parts in that zone; the
   arithmetic is then done on the calendar date in UTC, where a day is always a day. */
function dayInZone(timezone, plusDays) {
  let parts;
  try {
    parts = new Intl.DateTimeFormat('en-CA', { timeZone: timezone || undefined, year: 'numeric', month: '2-digit', day: '2-digit' })
      .format(new Date());
  } catch (e) {
    parts = new Date().toISOString().slice(0, 10);
  }
  return shiftDay(parts, plusDays);
}

/* Help that stays one line until asked for. Every card used to open with a paragraph that was always
   expanded; a parent who has read it once should not have to scroll past it every time. */
function help(summary, ...paragraphs) {
  return el('details', { class: 'help' },
    el('summary', {}, icon('info', 'icon-sm'), el('span', { text: summary })),
    paragraphs.map((p) => (typeof p === 'string' ? el('p', { text: p }) : p)));
}

/* A warning a parent has to act on: a coloured block with a title, never a grey line among grey
   lines. `kind` is warn (default), danger or info. */
function notice(kind, title, ...body) {
  return el('div', { class: 'notice' + (kind && kind !== 'warn' ? ' ' + kind : '') },
    icon(kind === 'info' ? 'info' : 'alert'),
    el('div', {}, title ? el('b', { text: title }) : null, body.map((b) => (typeof b === 'string' ? el('p', { text: b }) : b))));
}

/* The first letter of a name, for the round marks beside children and apps. */
const initial = (name) => ((name || '?').trim()[0] || '?').toUpperCase();

/**
 * A stable colour per name, so the same app is the same colour every time the card is drawn.
 * Derived from the name rather than from the row's position: position changes with the day.
 */
function packageHue(name) {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360;
  return 'hsl(' + h + ' 58% 48%)';
}

function isGuardian() { return !!state.parent && state.parent.role === 'GUARDIAN'; }
function isAdmin() { return !!state.parent && (state.parent.role === 'PRIMARY_ADMIN' || state.parent.role === 'ADMIN'); }

/* ---- shell -------------------------------------------------------------- */

async function boot() {
  state.session = takeSessionFromHash() || readSession();
  document.getElementById('signout').addEventListener('click', () => signOut('Abgemeldet.'));
  document.getElementById('sheet-close').addEventListener('click', closeSheet);
  window.addEventListener('hashchange', onRoute);

  if (!state.session) {
    document.getElementById('signin').hidden = false;
    return;
  }
  document.getElementById('signin').hidden = true;
  document.getElementById('app').hidden = false;

  try {
    const [me, family, children] = await Promise.all([api('/me'), api('/family'), api('/children')]);
    state.parent = me;
    state.family = family;
    state.children = children.children || [];
    // Not asked at all by a guardian (FR-20.1): the answer would be a 403, and a guardian's page
    // asking for what only an admin may read is exactly what the role test looks for. Caught: a
    // deployment that cannot say which build it hosts must still show a parent their family.
    state.dpc = isGuardian() ? null : await api('/dpc').catch(() => null);
    if (isGuardian()) {
      document.getElementById('mainnav').hidden = true;
      document.getElementById('app').classList.add('no-nav');
      document.getElementById('view').classList.add('no-tabbar');
    }
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return;
    toast('Deine Familie konnte nicht geladen werden: ' + err.message, true);
    return;
  }

  const familyName = state.family ? state.family.name : 'Familie';
  document.getElementById('family-name').textContent = familyName;
  document.getElementById('nav-family').textContent = familyName;
  const remembered = localStorage.getItem(CHILD_KEY);
  state.childId = state.children.some((c) => c.id === remembered)
    ? remembered
    : (state.children[0] ? state.children[0].id : null);

  renderChildSwitcher();
  onRoute();
  openStream();
}

function renderChildSwitcher() {
  const nav = document.getElementById('child-switcher');
  nav.replaceChildren();
  for (const child of state.children) {
    nav.append(el('button', {
      class: 'pill',
      type: 'button',
      // Written on both states rather than only on the pressed one: a toggle that drops the
      // attribute when it is off tells a screen reader nothing about the off state.
      'aria-pressed': String(child.id === state.childId),
      text: child.name,
      onclick: () => selectChild(child.id),
    }));
  }
  if (isAdmin()) {
    nav.append(el('button', { class: 'pill pill-add', type: 'button', text: '+ Kind', onclick: addChild }));
  }
}

function selectChild(id) {
  state.childId = id;
  localStorage.setItem(CHILD_KEY, id);
  // A different child is a different set of apps; carrying the previous child's search across is a
  // filter the parent did not ask for and cannot see the cause of.
  state.appFilter = { q: '', rule: 'all', system: false };
  // Same reasoning, and one more: a day that exists for one child's timezone may not be the
  // other's today at all.
  state.timelineDay = null;
  state.timelineToday = null;
  renderChildSwitcher();
  refresh();
}

/* The route is the hash: #/overview, #/rules (= its first sub-page) or #/rules/protection, #/apps,
   #/activity, #/family. The old #/home and #/guardian land on Übersicht, which replaced both. */
const ROUTE_ALIASES = { '': 'overview', home: 'overview', guardian: 'overview' };
const PAGE_TITLES = { overview: 'Übersicht', rules: 'Regeln', apps: 'Apps', activity: 'Aktivität', family: 'Familie' };

function onRoute() {
  const [want, sub] = location.hash.replace(/^#\/?/, '').split('?')[0].split('/');
  const view = ROUTE_ALIASES[want] || want;
  // Unsaved edits on the page being left: asked in the sheet, and the hash put back if the answer
  // is "stay". `leaveGuard` is set by a view with drafts (Regeln) and cleared when they are saved.
  // Only when the VIEW changes: a view's sub-pages share its drafts, so moving between them loses
  // nothing.
  if (state.leaveGuard && state.leaveGuard() && (VIEWS[view] ? view : 'overview') !== state.view) {
    const back = state.routeHash;
    history.replaceState(null, '', back);
    confirmSheet({
      title: 'Änderungen verwerfen?',
      lines: ['Auf dieser Seite ist noch etwas nicht gespeichert.'],
      confirmLabel: 'Verwerfen',
      cancelLabel: 'Weiter bearbeiten',
    }).then((discard) => {
      if (!discard) return;
      if (state.discardDrafts) state.discardDrafts();
      state.leaveGuard = null;
      location.hash = '#/' + want + (sub ? '/' + sub : '');
    });
    return;
  }
  const next = VIEWS[view] ? view : 'overview';
  // A view that set a guard sets it again when it draws; leaving it clears it.
  if (next !== state.view) { state.leaveGuard = null; state.discardDrafts = null; }
  state.view = next;
  state.sub = sub || null;
  // FR-20: a guardian has one page. An old link to an admin page lands there too, rather than on a
  // "could not load" made of 403s.
  if (isGuardian()) { state.view = 'overview'; state.sub = null; }
  state.routeHash = location.hash || '#/overview';
  for (const tab of document.querySelectorAll('.tab')) {
    if (tab.dataset.tab === state.view) tab.setAttribute('aria-current', 'page');
    else tab.removeAttribute('aria-current');
  }
  document.getElementById('page-title').textContent = PAGE_TITLES[state.view] || '';
  // The child switcher only where a page is about one child.
  document.getElementById('kids-row').hidden = !(VIEWS[state.view] && VIEWS[state.view].perChild) || !state.children.length;
  refresh();
}

/* ---- empty states ------------------------------------------------------- */

/* On day one this console has no devices, no apps, no usage and no history, so most of its screens
 * are made of these. That is the first impression, not the edge case. Nothing here invents a row to
 * fill the space: an empty screen says why it is empty and what the one next action is. */
function emptyCard(iconName, title, body, action) {
  return el('div', { class: 'card full empty' },
    el('div', { class: 'empty-icon', 'aria-hidden': 'true' }, icon(iconName)),
    el('h2', { text: title }),
    el('p', { text: body }),
    action || null);
}

const setUpAPhoneLink = () => el('a', { class: 'btn btn-primary', href: '#/overview', text: 'Handy einrichten' });

let refreshToken = 0;
async function refresh() {
  const view = VIEWS[state.view];
  const mine = ++refreshToken;
  const main = document.getElementById('view');

  if (!state.childId && view.perChild) {
    main.replaceChildren(emptyCard('family', 'Füge dein erstes Kind hinzu',
      'Regeln, Apps und Bildschirmzeit gehören zu einem Kind. Füge es hier hinzu und richte dann sein Handy ein.',
      isAdmin() ? el('button', { class: 'btn btn-primary', type: 'button', text: 'Kind hinzufügen', onclick: addChild }) : null));
    return;
  }
  // A different page than the one on screen: after a moment with nothing to show, say that it is
  // coming rather than leaving the previous page (or, on first load, nothing) standing under the new
  // tab. Not at once, because a fast load would flash it; and never on a background refresh of the
  // same page, which keeps what it shows until the new data is in.
  const arriving = state.dataView !== state.view;
  const waiting = arriving && setTimeout(() => {
    if (mine !== refreshToken) return;
    main.setAttribute('aria-busy', 'true');
    main.replaceChildren(el('div', { class: 'view-loading', role: 'status' },
      el('span', { class: 'spinner', 'aria-hidden': 'true' }), el('span', { text: 'Lädt …' })));
  }, 200);
  try {
    const data = await view.load();
    if (waiting) clearTimeout(waiting);
    main.removeAttribute('aria-busy');
    if (mine !== refreshToken) return;   // a newer refresh already won
    state.data = data;
    state.dataView = state.view;
    // Filtered, because a section with nothing to say returns null, and `replaceChildren(null)`
    // appends the TEXT "null" to the page.
    main.replaceChildren(...view.render(data).filter((n) => n !== null && n !== undefined && n !== false));
    if (view.afterRender) view.afterRender(data);
  } catch (err) {
    if (waiting) clearTimeout(waiting);
    main.removeAttribute('aria-busy');
    if (err instanceof ApiError && err.status === 401) return;
    if (mine !== refreshToken) return;
    main.replaceChildren(el('div', { class: 'card full' },
      el('h2', { text: 'Diese Seite konnte nicht geladen werden' }),
      el('p', { class: 'muted', text: err.message }),
      el('button', { class: 'btn', type: 'button', text: 'Nochmals versuchen', onclick: refresh })));
  }
}

/* redraw re-renders the current view from the data already in hand, with no network at all.
 *
 * The companion to `refresh`, and the difference is the whole reason a parent can answer a hundred
 * waiting apps: `refresh` re-reads everything the tab is built from, and the console once called it
 * after EVERY tap — nine requests per answer, and "too many requests" around the fifteenth app. The
 * answer a parent just gave is already known here: it is what was sent. So the page is drawn from
 * it at once, and the authoritative re-read is left to `nudgeRefresh`, which coalesces. */
function redraw() {
  if (!state.data) { refresh(); return; }
  // A write that answers after the parent moved to another tab must not draw that tab from the data
  // of the one they left. The new tab's own load is already on its way.
  if (state.dataView !== state.view) return;
  const view = VIEWS[state.view];
  document.getElementById('view').replaceChildren(
    ...view.render(state.data).filter((n) => n !== null && n !== undefined && n !== false));
  if (view.afterRender) view.afterRender(state.data);
}

/* ---- live updates ------------------------------------------------------- */

/* openStream subscribes to the server's event stream.
 *
 * fetch + ReadableStream rather than EventSource, because EventSource cannot send an Authorization
 * header. The alternatives would be a cookie (CSRF) or the token in the query string (every access
 * log on the path), so the extra twenty lines here buy a real property.
 *
 * An event is only a nudge to re-read; it never carries state. A dropped frame therefore costs a
 * few seconds of staleness and can never show something that is not true. */
function openStream() {
  if (state.stream) state.stream.abort();
  const ctl = new AbortController();
  state.stream = ctl;
  let backoff = 1000;

  (async function loop() {
    while (!ctl.signal.aborted && state.session) {
      try {
        const res = await fetch(API + '/events', {
          headers: { 'Authorization': 'Bearer ' + state.session.token, 'Accept': 'text/event-stream' },
          signal: ctl.signal,
        });
        if (res.status === 401) { signOut(SESSION_EXPIRED); return; }
        if (!res.ok || !res.body) throw new Error('stream unavailable');
        backoff = 1000;

        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buf = '';
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buf += decoder.decode(value, { stream: true });
          let cut;
          while ((cut = buf.indexOf('\n\n')) >= 0) {
            const frame = buf.slice(0, cut);
            buf = buf.slice(cut + 2);
            handleFrame(frame);
          }
        }
      } catch (err) {
        if (ctl.signal.aborted) return;
      }
      // Reconnect with backoff. The server closes the stream every 15 minutes on purpose, so a
      // clean end is the normal case and must not be treated as an error.
      await new Promise((r) => setTimeout(r, backoff));
      backoff = Math.min(backoff * 2, 30000);
    }
  })();
}

let nudge = null;
function handleFrame(frame) {
  let type = 'message';
  let data = '';
  for (const line of frame.split('\n')) {
    if (line.startsWith('event:')) type = line.slice(6).trim();
    else if (line.startsWith('data:')) data += line.slice(5).trim();
  }
  if (type === 'connected' || !data) return;
  // Coalesced: a policy change fans out one event per device, and re-rendering five times in a row
  // would make the page flicker for no extra information.
  nudgeRefresh(400);
}

/* nudgeRefresh asks for ONE authoritative re-read once things stop happening. The same timer as the
 * event stream's, on purpose: a burst of writes produces both local redraws and server events about
 * those same writes, and sharing the timer makes the whole burst cost one refresh. */
function nudgeRefresh(delay) {
  clearTimeout(nudge);
  nudge = setTimeout(maybeRefresh, delay);
}

/* A refresh replaces every child of #view, which takes the field the parent is typing in with it.
 * A heartbeat arriving mid-sentence must not do that, so the nudge waits — and re-arms rather than
 * being dropped, because a nudge that is silently discarded is a screen that stops updating for as
 * long as a cursor happens to sit in a box. The same holds for a field in the open sheet. */
function maybeRefresh() {
  const a = document.activeElement;
  if (a && a.closest && a.closest('#view, #sheet') && /^(INPUT|SELECT|TEXTAREA)$/.test(a.tagName)) {
    nudge = setTimeout(maybeRefresh, 3000);
    return;
  }
  refresh();
}

/* ---- sheet -------------------------------------------------------------- */

/* The sheet is one <dialog>: a bottom sheet on a phone, a panel on a laptop. `kind` names what is in
   it, so a view that redraws after a server event can refresh a sheet that shows its data — the
   phone sheet — and leave a QR code or a confirmation alone. */
function openSheet(title, body, kind) {
  document.getElementById('sheet-title').textContent = title;
  const holder = document.getElementById('sheet-body');
  holder.replaceChildren(body);
  const dlg = document.getElementById('sheet');
  dlg.dataset.kind = kind || '';
  if (!dlg.open) {
    if (typeof dlg.showModal === 'function') dlg.showModal();
    else dlg.setAttribute('open', '');
  }
}

function closeSheet() {
  const dlg = document.getElementById('sheet');
  dlg.dataset.kind = '';
  if (typeof dlg.close === 'function') dlg.close();
  else dlg.removeAttribute('open');
}

function sheetShows(kind) {
  const dlg = document.getElementById('sheet');
  return dlg.open && dlg.dataset.kind === kind;
}

/* A confirmation the parent has to read, drawn in the console's own sheet.
 *
 * Deliberately not window.confirm: the browser draws it at the TOP of the screen, the furthest
 * point from the thumb and the easiest thing to dismiss by reflex; it cannot mark the destructive
 * answer as destructive; and it blocks the page's JavaScript entirely, so no instrument can reach
 * the sheet behind it.
 *
 * Resolves false for every way out that is not the button — Esc, the backdrop, the sheet's own X.
 * The default for a destructive question is no. */
function confirmSheet({ title, lines, confirmLabel, cancelLabel, danger = true }) {
  return new Promise((resolve) => {
    const dlg = document.getElementById('sheet');
    const x = document.getElementById('sheet-close');
    let answered = false;
    const finish = (ok) => {
      if (answered) return;
      answered = true;
      dlg.removeEventListener('close', dismissed);
      x.removeEventListener('click', dismissed);
      resolve(ok);
    };
    // `close` covers Esc and the backdrop on a real <dialog>; the click covers the attribute
    // fallback in openSheet, where closing fires no event at all.
    const dismissed = () => finish(false);
    dlg.addEventListener('close', dismissed);
    x.addEventListener('click', dismissed);

    openSheet(title, el('div', { class: 'stack' },
      lines.map((line) => el('p', { text: line })),
      el('button', {
        class: 'btn btn-block ' + (danger ? 'btn-danger-solid' : 'btn-primary'), type: 'button', text: confirmLabel,
        'data-confirm': 'yes',
        onclick: () => { finish(true); closeSheet(); },
      }),
      el('button', {
        class: 'btn btn-block', type: 'button', text: cancelLabel || 'Abbrechen',
        'data-confirm': 'no',
        onclick: () => { finish(false); closeSheet(); },
      })), 'confirm');
  });
}

/* A question with one text answer, in the sheet: the replacement for window.prompt, which a phone
   draws as a system dialog at the top of the screen and a laptop as a grey box with no context. */
function askSheet({ title, label, value, placeholder, confirmLabel, type }) {
  return new Promise((resolve) => {
    const dlg = document.getElementById('sheet');
    let answered = false;
    const finish = (v) => {
      if (answered) return;
      answered = true;
      dlg.removeEventListener('close', dismissed);
      resolve(v);
    };
    const dismissed = () => finish(null);
    dlg.addEventListener('close', dismissed);
    const input = el('input', { type: type || 'text', value: value || '', placeholder: placeholder || '', 'aria-label': label, 'data-ask': 'value' });
    openSheet(title, el('form', {
      class: 'stack',
      onsubmit: (e) => {
        e.preventDefault();
        const v = input.value.trim();
        if (!v) return;
        finish(v);
        closeSheet();
      },
    },
    el('label', { class: 'field' }, el('span', { text: label }), input),
    el('button', { class: 'btn btn-primary btn-block', type: 'submit', text: confirmLabel || 'OK' })), 'ask');
    setTimeout(() => input.focus(), 50);
  });
}

async function addChild() {
  const name = await askSheet({ title: 'Kind hinzufügen', label: 'Name des Kindes', confirmLabel: 'Hinzufügen' });
  if (!name) return;
  const child = await act(name + ' hinzugefügt', () => api('/children', { method: 'POST', body: { name, birth_year: null } }));
  if (!child) return;
  state.children.push(child);
  selectChild(child.id);
}

document.addEventListener('DOMContentLoaded', boot);
