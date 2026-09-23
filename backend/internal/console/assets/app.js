'use strict';

/* Family Guard console.
 *
 * Plain ES modules-free JavaScript, no build step and no dependencies. That is a security decision
 * before it is a convenience one: the page runs under `script-src 'self'` with no inline script, so
 * there is no bundler output to audit, no third-party code in this origin, and the file you read
 * here is byte-for-byte the file the browser runs.
 *
 * The session token lives in localStorage, never in a cookie. A cookie would be sent automatically
 * with every request from anywhere, which is what makes CSRF possible; a token the page has to
 * attach by hand cannot be replayed by another site.
 */

const API = '/api/v1';
const SESSION_KEY = 'fg.session';
const CHILD_KEY = 'fg.child';

/* The list offered by the button in the Rules tab, and the only one this project names.
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
  view: 'home',
  loading: false,
  stream: null,
  // Filtering is a property of the person looking, not of the data, so it lives here and survives
  // the re-render a server event triggers. A parent who has typed "tik" into the app search does
  // not want a heartbeat to clear it.
  appFilter: { q: '', rule: 'all', system: false },
  // Which day the Activity timeline is showing. Null means "whatever the child's today is", which
  // only the server can answer — the child's timezone is policy, and the parent may be in another.
  // `timelineToday` is the server's answer to that, remembered so the forward step knows where to
  // stop rather than walking into empty days that were never going to have anything in them.
  timelineDay: null,
  timelineToday: null,
};

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
      ? 'That Google account is not a parent in this family. Ask an admin to add it.'
      : 'Sign-in failed. Please try again.';
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

async function api(path, options = {}) {
  const headers = Object.assign({ 'Accept': 'application/json' }, options.headers || {});
  if (state.session) headers['Authorization'] = 'Bearer ' + state.session.token;
  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    options = Object.assign({}, options, { body: JSON.stringify(options.body) });
  }
  const res = await fetch(API + path, Object.assign({}, options, { headers }));
  if (res.status === 401) {
    signOut('Your session expired. Please sign in again.');
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
    signOut('Your session expired. Please sign in again.');
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
 * against: the parent taps "Lock", nothing changes, and nothing says so. */
async function act(label, fn) {
  try {
    const out = await fn();
    toast(label);
    return out;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return null;
    toast(label + ' failed: ' + err.message, true);
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
    if (!(err instanceof ApiError && err.status === 401)) toast(label + ' failed: ' + err.message, true);
    return false;
  }
}

const fmtTime = (iso) => {
  if (!iso) return 'never';
  const d = new Date(iso);
  const mins = Math.round((Date.now() - d.getTime()) / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return mins + ' min ago';
  if (mins < 60 * 24) return Math.round(mins / 60) + ' h ago';
  return d.toLocaleDateString();
};

/* Megabytes, one decimal, because an APK is the one number in this console a parent compares
 * against their own phone's storage. Bytes would be unreadable and "large" would be a judgement. */
const fmtSize = (bytes) => {
  if (!bytes) return '';
  const mb = bytes / (1024 * 1024);
  return (mb >= 10 ? Math.round(mb) : Math.round(mb * 10) / 10) + ' MB';
};

const fmtMinutes = (m) => {
  if (!m) return '0 min';
  const h = Math.floor(m / 60);
  return h ? h + ' h ' + (m % 60) + ' min' : m + ' min';
};

/* One sitting's length. Seconds below a minute rather than "0 min": this is a single interval with
 * a start and an end a parent can see on the strip, and rounding it to nothing would contradict the
 * picture next to it. Whole days' worth of usage still goes through fmtMinutes. */
const fmtDuration = (seconds) => {
  const s = Math.max(0, Math.round(seconds || 0));
  if (s < 60) return s + ' s';
  return fmtMinutes(Math.round(s / 60));
};

/* ---- chrome: one navigation, in two places ------------------------------ */

/* `#mainnav` and `#child-switcher` are single elements that MOVE between the header row and the
 * drawer as the viewport crosses this breakpoint. They are not written twice and hidden.
 *
 * app.css used to justify a bottom tab bar at every width by saying a sidebar "would mean
 * maintaining two navigations". That objection is correct — the second copy is always the one that
 * stops being updated — and relocating the node is what answers it instead of accepting it. */
const WIDE = window.matchMedia('(min-width: 900px)');

function placeChrome() {
  const nav = document.getElementById('mainnav');
  const kids = document.getElementById('child-switcher');
  if (WIDE.matches) {
    const anchor = document.getElementById('signout');
    anchor.parentNode.insertBefore(nav, anchor);
    anchor.parentNode.insertBefore(kids, anchor);
    closeDrawer();
  } else {
    document.getElementById('drawer-nav').append(nav);
    document.getElementById('drawer-children').append(kids);
  }
}

/* What the ☰ button claims, decided by READING the dialog rather than by remembering which
 * handler is running.
 *
 * `dialog.close()` fires its `close` event as a QUEUED TASK, not synchronously. So a close followed
 * quickly by an open — pick a destination in the drawer, which closes it, then open it again — can
 * deliver the `close` event AFTER the drawer is back on screen, and a handler that wrote 'false'
 * because it was the close handler told a screen reader the menu was shut while it was open. Caught
 * in CI on 2026-09-05 by TestConsoleRendersOnAPhone/drawer, on a tree that had passed the identical
 * suite minutes earlier: it is a race, so it is green almost always. */
function syncMenuButton() {
  const d = document.getElementById('drawer');
  document.getElementById('menu-open').setAttribute('aria-expanded', d.open ? 'true' : 'false');
}

function openDrawer() {
  const d = document.getElementById('drawer');
  if (d.open) return;
  if (typeof d.showModal === 'function') d.showModal();
  else d.setAttribute('open', '');
  syncMenuButton();
}

function closeDrawer() {
  const d = document.getElementById('drawer');
  if (!d.open) return;
  if (typeof d.close === 'function') d.close();
  else d.removeAttribute('open');
}

/* ---- shell -------------------------------------------------------------- */

async function boot() {
  state.session = takeSessionFromHash() || readSession();
  document.getElementById('signout').addEventListener('click', () => signOut('Signed out.'));
  document.getElementById('drawer-signout').addEventListener('click', () => signOut('Signed out.'));
  document.getElementById('sheet-close').addEventListener('click', closeSheet);

  document.getElementById('menu-open').addEventListener('click', openDrawer);
  document.getElementById('drawer-close').addEventListener('click', closeDrawer);
  // One place to undo the button's state, so Esc, the backdrop and the ✕ cannot disagree about it
  // — and it asks the dialog what it is rather than asserting what it must be. See syncMenuButton.
  document.getElementById('drawer').addEventListener('close', syncMenuButton);
  // Picking a destination closes the menu; leaving it open over the page it just navigated to is
  // the drawer bug every hand-rolled one has.
  document.getElementById('mainnav').addEventListener('click', closeDrawer);

  placeChrome();
  WIDE.addEventListener('change', placeChrome);
  window.addEventListener('hashchange', onRoute);

  if (!state.session) {
    document.getElementById('signin').hidden = false;
    return;
  }
  document.getElementById('signin').hidden = true;
  document.getElementById('app').hidden = false;

  try {
    const [me, family, children, dpc] = await Promise.all([
      api('/me'), api('/family'), api('/children'),
      // Caught rather than awaited alongside the rest: a deployment that cannot say which build it
      // hosts must still show a parent their family. The console then simply says nothing about
      // updates, which is the honest rendering of not knowing.
      api('/dpc').catch(() => null),
    ]);
    state.parent = me;
    state.family = family;
    state.children = children.children || [];
    state.dpc = dpc;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return;
    toast('Could not load your family: ' + err.message, true);
    return;
  }

  const familyName = state.family ? state.family.name : 'Family';
  document.getElementById('family-name').textContent = familyName;
  document.getElementById('drawer-family').textContent = familyName;
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
  nav.append(el('button', {
    class: 'pill', type: 'button', text: '+ Child', onclick: addChild,
  }));
  document.getElementById('drawer-children-label').hidden = false;
  renderCrumb();
}

/* Which child is on screen, for the widths where the switcher itself is in the drawer. Without it
   every screen below the header is ambiguous the moment a family has two children. */
function renderCrumb() {
  const child = state.children.find((c) => c.id === state.childId);
  document.getElementById('crumb').textContent = child ? child.name : '';
}

function selectChild(id) {
  state.childId = id;
  localStorage.setItem(CHILD_KEY, id);
  // A different child is a different set of apps; carrying the previous child's search across is a
  // filter the parent did not ask for and cannot see the cause of.
  state.appFilter = { q: '', rule: 'all', system: false };
  // Same reasoning as the filter above, and one more: a day that exists for one child's timezone
  // may not be the other's today at all.
  state.timelineDay = null;
  state.timelineToday = null;
  renderChildSwitcher();
  closeDrawer();
  refresh();
}

function onRoute() {
  const want = (location.hash.replace('#/', '') || 'home').split('?')[0];
  state.view = ['home', 'rules', 'apps', 'activity', 'family'].includes(want) ? want : 'home';
  for (const tab of document.querySelectorAll('.tab')) {
    if (tab.dataset.tab === state.view) tab.setAttribute('aria-current', 'page');
    else tab.removeAttribute('aria-current');
  }
  closeDrawer();
  refresh();
}

const VIEWS = {
  home: { load: loadHome, render: renderHome },
  rules: { load: loadRules, render: renderRules },
  apps: { load: loadApps, render: renderApps },
  activity: { load: loadActivity, render: renderActivity },
  family: { load: loadFamily, render: renderFamily },
};

/* ---- empty states ------------------------------------------------------- */

/* On day one this console has no devices, no apps, no usage and no history, so four of its five
 * screens are made entirely of these. That is the first impression, not the edge case — and the
 * version this replaced answered it with four different one-line shrugs ("Nothing to show yet",
 * "No apps reported yet") that named no cause and offered no way out.
 *
 * Nothing here invents a row to fill the space. An empty screen says why it is empty and what the
 * one next action is; it does not draw a fake device to look busy. */
function emptyCard(icon, title, body, action) {
  return el('div', { class: 'card full empty' },
    el('div', { class: 'empty-icon', 'aria-hidden': 'true', text: icon }),
    el('h2', { text: title }),
    el('p', { text: body }),
    action || null);
}

const setUpAPhoneLink = () => el('a', { class: 'btn btn-primary', href: '#/home', text: 'Set up a phone' });

let refreshToken = 0;
async function refresh() {
  const view = VIEWS[state.view];
  const mine = ++refreshToken;
  const main = document.getElementById('view');

  if (!state.childId && state.view !== 'family') {
    main.replaceChildren(emptyCard('♦', 'Add your first child',
      'Rules, apps and screen time all belong to a child. Add one here, then set up their phone.',
      el('button', { class: 'btn btn-primary', type: 'button', text: 'Add a child', onclick: addChild })));
    return;
  }
  try {
    const data = await view.load();
    if (mine !== refreshToken) return;   // a newer refresh already won
    state.data = data;
    // Filtered, because a section with nothing to say returns null — the approval queue when
    // nothing is waiting — and `replaceChildren(null)` appends the TEXT "null" to the page.
    // `el` already drops empty children; this is the one mount point that did not.
    main.replaceChildren(...view.render(data).filter((n) => n !== null && n !== undefined && n !== false));
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return;
    if (mine !== refreshToken) return;
    main.replaceChildren(el('div', { class: 'card full' },
      el('h2', { text: 'Could not load this page' }),
      el('p', { class: 'muted', text: err.message }),
      el('button', { class: 'btn', type: 'button', text: 'Try again', onclick: refresh })));
  }
}

/* redraw re-renders the current view from the data already in hand, with no network at all.
 *
 * The companion to `refresh`, and the difference is the whole reason a parent can answer a hundred
 * waiting apps: `refresh` re-reads everything the tab is built from — for the Apps tab that is
 * seven or eight requests — and the console called it after EVERY tap. Answering an app then cost
 * nine requests instead of one, and the owner met a "too many requests" page around the fifteenth
 * app. The answer a parent just gave is already known here: it is what was sent. So the page is
 * drawn from it at once, and the authoritative re-read is left to `nudgeRefresh`, which coalesces.
 */
function redraw() {
  if (!state.data) { refresh(); return; }
  const view = VIEWS[state.view];
  document.getElementById('view').replaceChildren(
    ...view.render(state.data).filter((n) => n !== null && n !== undefined && n !== false));
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
        if (res.status === 401) { signOut('Your session expired.'); return; }
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

/* nudgeRefresh asks for ONE authoritative re-read once things stop happening.
 *
 * The same timer as the event stream's, on purpose: a burst of writes produces both a burst of
 * local redraws and a burst of server events about those same writes, and every one of them is a
 * request to re-read the page that is already being re-read. Sharing the timer makes the whole
 * burst cost one refresh instead of one per write plus one per event.
 */
function nudgeRefresh(delay) {
  clearTimeout(nudge);
  nudge = setTimeout(maybeRefresh, delay);
}

/* A refresh replaces every child of #view, which takes the field the parent is typing in with it.
 * A heartbeat arriving mid-sentence must not do that, so the nudge waits — and re-arms rather than
 * being dropped, because a nudge that is silently discarded is a screen that stops updating for as
 * long as a cursor happens to sit in a box. */
function maybeRefresh() {
  const a = document.activeElement;
  if (a && a.closest && a.closest('#view') && /^(INPUT|SELECT|TEXTAREA)$/.test(a.tagName)) {
    nudge = setTimeout(maybeRefresh, 3000);
    return;
  }
  refresh();
}

/* ---- home --------------------------------------------------------------- */

async function loadHome() {
  const devices = await api('/devices?child_id=' + encodeURIComponent(state.childId));
  const list = devices.devices || [];
  /* `.desired` is not a detail: the endpoint answers `{desired, input}` and every consumer below
     reads a desired state. Reading the envelope as if it were flat is not a visible error — every
     field simply comes back undefined — so the card printed "Screen time today: 0 min (no daily
     limit)" over a phone that had reported 99 minutes, the "apps are paused right now" line never
     appeared, and neither did the one saying apps were waiting for a decision. That last one is
     why a parent had no way to learn the queue existed. Unwrapped here rather than at each use, so
     there is one place to be wrong about. */
  const states = await Promise.all(list.map((d) =>
    d.enrolled
      ? api('/devices/' + d.id + '/desired-state').then((r) => (r && r.desired) || null).catch(() => null)
      : Promise.resolve(null)));
  return { devices: list, states };
}

function renderHome(data) {
  if (!data.devices.length) return [setupCard()];

  const cards = [];
  // Only worth drawing for a second device. With one, the card below it is already the answer to
  // "is it all right", and a strip repeating it is decoration that costs a screenful.
  if (data.devices.length > 1) cards.push(statusStrip(data));

  cards.push(...data.devices.map((dev, i) => deviceCard(dev, data.states[i])));

  cards.push(el('div', { class: 'card full' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Add another phone' })),
    el('p', { class: 'muted', text: 'Create the device here, then scan its QR on a factory-reset phone.' }),
    el('button', { class: 'btn btn-primary btn-block', type: 'button', text: 'Add a device', onclick: addDevice })));
  return cards;
}

/* The whole of the home screen until the first phone is enrolled — which is where every new install
   starts, and where this console previously showed an empty list above a card called "Add a phone".
   The three steps are the ones the enrolment actually needs; the factory-reset requirement is not
   advice, it is what Android demands before it will hand device-owner rights to anything. */
function setupCard() {
  const child = state.children.find((c) => c.id === state.childId);
  return el('div', { class: 'card full' },
    el('h2', { text: child ? 'Set up ' + child.name + '’s first phone' : 'Set up the first phone' }),
    el('p', { class: 'muted', text: 'The phone has to be factory-reset first: Family Guard is installed as the device owner, and Android only allows that on a phone with no account set up on it yet.' }),
    el('ol', { class: 'steps' },
      el('li', {}, el('b', { text: 'Add the phone here' }),
        el('small', { text: 'Give it a name you will recognise later. Nothing is sent to the phone yet.' })),
      el('li', {}, el('b', { text: 'Factory-reset the phone' }),
        el('small', { text: 'Settings → System → Reset options → Erase all data.' })),
      el('li', {}, el('b', { text: 'Scan the QR this page shows you' }),
        el('small', { text: 'On the welcome screen, tap the same spot six times. The phone then asks for a QR code.' }))),
    el('button', { class: 'btn btn-primary btn-block', type: 'button', text: 'Add a phone', onclick: addDevice }));
}

/* One line per phone: reachable, charged, and how much of today's allowance is gone. The point is
   that a parent with two or three phones gets the answer without opening anything. */
function statusStrip(data) {
  const rows = data.devices.map((dev, i) => {
    const st = dev.state || {};
    const desired = data.states[i];
    const used = desired ? (desired.used_minutes || 0) : 0;
    const quota = desired ? (desired.quota_minutes || 0) : 0;
    return el('div', { class: 'strip-row' },
      el('span', { class: 'dot ' + (st.online ? 'online' : 'offline') }),
      el('span', { class: 'strip-name', text: dev.name }),
      dev.locked ? el('span', { class: 'badge danger', text: 'locked' }) : null,
      st.battery_level !== null && st.battery_level !== undefined
        ? el('span', { class: 'badge', text: st.battery_level + '%' })
        : null,
      quota > 0
        ? el('span', { class: 'meter', title: fmtMinutes(used) + ' of ' + fmtMinutes(quota) },
          el('span', {
            class: used >= quota ? 'over' : '',
            style: { width: Math.min(100, Math.round((used / quota) * 100)) + '%' },
          }))
        : el('span', { class: 'badge', text: fmtMinutes(used) }));
  });
  return el('div', { class: 'card full strip' }, ...rows);
}

/**
 * Whether this phone is running an older build than the one the server hosts, and which.
 *
 * Null whenever the question cannot be answered from measurements: no build reported by the phone,
 * no build parsed by the server, or a version code of zero on either side. That is the whole point
 * of the function — "up to date" and "nobody could tell" look identical on a card, and only one of
 * them is a fact. The comparison is on version CODES, which are integers and monotone; the names
 * are what a person reads and are never compared.
 */
function updateBehind(st) {
  const hosted = state.dpc;
  if (!hosted || !hosted.hosted || !hosted.version_code) return null;
  if (!st.app_version_code) return null;
  if (hosted.version_code <= st.app_version_code) return null;
  return { hosted: hosted.version_name || ('build ' + hosted.version_code) };
}

/*
 * Whether this phone is MEASURABLY running the build the server hosts.
 *
 * `updateBehind` folds three states into one null — "behind is false", "the phone has not said"
 * and "the server cannot say" — which is right for a badge nobody should draw on a guess, and
 * wrong for deciding whether a stored failure is still true. This is the half that can be
 * asserted: both version codes are known and the phone's is not lower. Anything else is
 * unknown, and unknown must never read as current.
 */
function updateCurrent(st) {
  const hosted = state.dpc;
  if (!hosted || !hosted.hosted || !hosted.version_code) return false;
  if (!st.app_version_code) return false;
  return hosted.version_code <= st.app_version_code;
}

function deviceCard(dev, desired) {
  const st = dev.state || {};
  const online = st.online;
  const behind = dev.enrolled ? updateBehind(st) : null;
  const head = el('div', { class: 'card-head' },
    el('h2', {}, el('span', { class: 'dot ' + (online ? 'online' : 'offline') }), ' ' + dev.name),
    el('span', { class: 'badge' + (online ? ' ok' : ''), text: online ? 'online' : fmtTime(st.last_seen_at) }));

  const facts = el('div', { class: 'wrap' },
    !dev.enrolled && el('span', { class: 'badge warn', text: 'not enrolled' }),
    dev.locked && el('span', { class: 'badge danger', text: 'locked by you' }),
    st.battery_level !== null && st.battery_level !== undefined
      && el('span', { class: 'badge', text: st.battery_level + '%' + (st.charging ? ' charging' : '') }),
    dev.model && el('span', { class: 'badge', text: dev.model }),
    dev.os_version && el('span', { class: 'badge', text: 'Android ' + dev.os_version }),
    // The FamilyGuard build on the phone, and nothing at all when it has not said. An enrolled
    // device that has never reported one is running a DPC from before this field existed; showing
    // "app 0" there would be a version no build ever had.
    dev.enrolled && st.app_version_name
      && el('span', { class: 'badge' + (behind ? ' warn' : ''), text: 'app ' + st.app_version_name }),
    // Only when the comparison could actually be made — see `updateBehind`. A phone that has not
    // reported a build, or a server that cannot say which one it hosts, gets no badge at all
    // rather than an "up to date" nobody checked.
    behind && el('span', { class: 'badge warn', text: '\u2192 ' + behind.hosted }),
    // Only on a measured false. `undefined` is a phone that has not said — an older DPC does not
    // report the field — and a warning there would be an alarm about a device nothing is wrong
    // with, which is the kind that teaches you to ignore the badge.
    st.usage_access === false
      && el('span', { class: 'badge warn', text: 'screen time not measured' }),
    // Same rule as the badge above it: only a measured false. An older DPC does not report the
    // field, and a phone that has not said is not a phone that is restricted.
    st.power_exempt === false
      && el('span', { class: 'badge warn', text: 'battery restricted' }),
    // Separate badge because it is a separate switch with a separate remedy, and shown WHENEVER it
    // is false — including alongside the battery badge. It used to be suppressed when
    // power_exempt was also false, on the reasoning that battery restriction is the bigger effect
    // and the parent should be sent to the setting that changes more. The pilot phone reported
    // both false on one heartbeat (2026-09-07 19:00Z) and that reasoning became a defect: it hides
    // the existence of the second switch until the first is fixed, which is a second trip to
    // Settings and a second day of waiting to find out. Order the remedies; do not hide one.
    st.exact_alarms === false
      && el('span', { class: 'badge warn', text: 'alarms not exact' }),
    // FR-6.10. Three states, and the third is why this is not a boolean: `true` is a tunnel the
    // phone has confirmed is up, `false` is one it says is down, and `undefined` is a phone that
    // has not reported — an older DPC, or a Play build, which carries no filter at all to report
    // on (FR-15.8). Only the first two draw anything, and the warning is conditioned on the parent
    // having ASKED for it: "not running" on a child whose filter is off is not news.
    st.ad_filter_running === true
      && el('span', { class: 'badge ok', text: 'ad filter on' }),
    desired && desired.ad_filter && st.ad_filter_running === false
      && el('span', { class: 'badge warn', text: 'ad filter not running' }));

  const body = [head, facts];

  // Above the screen-time notice, because it is about the app that measures it: a phone whose
  // update failed is a phone running code that may be the reason anything else on this card is
  // wrong. Shown verbatim — the text is Android's own words, and paraphrasing an error this console
  // has never seen would be inventing a diagnosis.
  //
  // **Only while the server actually has something this phone has not taken.** `update_error` is
  // last-reported and the phone clears it by reporting an empty one, so a failure recorded against
  // the build that is already the newest one has nothing left to clear it — see `UpdateReport` on
  // the DPC side, fixed in 0.6.10. Measured on the family phone 2026-09-20: a lost connection
  // during one `apk-info` call left "This phone did not take the last update" on the card for 81
  // minutes and counting, while the phone sat on the newest build heartbeating every 60 seconds.
  // A warning that cannot go away is one a parent learns to scroll past, which costs the next
  // warning too.
  //
  // `updateCurrent` and not `!behind`: a phone that has not reported a build, or a server that
  // cannot say which one it hosts, is UNKNOWN, and an unknown must keep showing the warning —
  // suppressing on "we could not tell" is how a real stuck phone would go quiet.
  if (st.update_error && !updateCurrent(st)) {
    body.push(el('p', { class: 'warn' },
      el('strong', { text: 'This phone did not take the last update. ' }),
      st.update_error + '.',
      behind
        ? ' The server is offering ' + behind.hosted + '. FamilyGuard retries by itself; if it keeps'
          + ' failing, the phone has to be set up again from its QR code.'
        : ''));
  }

  // Before the screen-time notice, because this one explains lateness in everything the phone
  // does, including the measurement that notice is about.
  if (st.power_exempt === false || st.exact_alarms === false) {
    // Named for what a parent sees rather than for the API: nobody presses Ring and thinks "my
    // alarms are being coalesced". Measured on the pilot phone 2026-09-07 while restricted — a
    // 15-minute update check firing 6m51s, 21m44s and 8m20s late, and a one-second stream
    // reconnect taking 83–495 s asleep against 1.5 s awake.
    //
    // EVERY remedy that applies is listed, ordered by how much it changes. This was an either/or
    // and that was wrong: the pilot phone reported power_exempt=false AND exact_alarms=false on
    // the same heartbeat, so the parent was shown one switch and would have discovered the second
    // only after fixing the first and waiting for a fresh heartbeat. Two switches, one trip.
    const steps = [];
    if (st.power_exempt === false) {
      steps.push(el('li', {},
        el('b', { text: 'Let it run in the background' }),
        el('small', { text: 'Settings \u2192 Apps \u2192 FamilyGuard \u2192 Battery \u2192 Unrestricted. On Samsung, also open Settings \u2192 Battery \u2192 Background usage limits and remove FamilyGuard from Sleeping apps and Deep sleeping apps.' })));
    }
    if (st.exact_alarms === false) {
      steps.push(el('li', {},
        el('b', { text: 'Let it wake at the right moment' }),
        el('small', { text: 'Settings \u2192 Apps \u2192 FamilyGuard \u2192 Alarms and reminders \u2192 allow.' })));
    }
    body.push(el('p', { class: 'warn' },
      el('strong', { text: 'This phone is delaying FamilyGuard in the background. ' }),
      'Ring, Lock and Locate can take minutes to arrive while the phone is asleep, and nothing '
      + 'reports an error when they do \u2014 the work is not lost, only late. '
      + 'FamilyGuard cannot grant this itself \u2014 there is no device-owner API for it. '
      + 'The quickest way is on the phone: open FamilyGuard there and each setting below has an '
      + 'Open settings button beside it that goes straight to the switch. '
      + (steps.length > 1
        ? 'Two settings on the phone need changing, and both matter \u2014 the full paths, if you '
          + 'would rather navigate yourself:'
        : 'One setting on the phone needs changing \u2014 the full path, if you would rather '
          + 'navigate yourself:')));
    body.push(el('ol', { class: 'steps' }, steps));
  }

  if (st.usage_access === false) {
    // Spelled out, because the number it invalidates is shown two lines below it. Without the
    // grant every app reads zero minutes, so "0m of 60m" is not a child who stayed off their
    // phone — it is a measurement that never happened.
    body.push(el('p', { class: 'warn' },
      el('strong', { text: 'Screen time is not being measured on this phone. ' }),
      'On the phone, open Settings \u2192 Apps \u2192 Special app access \u2192 Usage access and turn '
      + 'FamilyGuard on. Until then every app reads zero minutes and daily limits never apply. '
      + 'FamilyGuard cannot grant this itself \u2014 Android does not let any app, even a device '
      + 'owner, turn it on.'));
  }

  // What the phone MEASURED about its filter, which is a different question from what the parent
  // asked for — and the only one worth showing here. The Rules tab already says what was asked.
  if (desired && desired.ad_filter) {
    if (st.ad_filter_running === false) {
      /* The phone's own reason, verbatim, and the guess only when there is none (FR-6.11).
         This line used to say the list had probably not downloaded and to check that the phone was
         online. Measured on the family phone on 2026-09-20 it was wrong on both counts — 180423
         rules were compiled and the heartbeat was thirty seconds old — and the real reason, which
         the phone knew and printed in its own notification shade, was that the network had named no
         resolver to forward queries to. A console that guesses at a remedy sends a parent to fix
         something that is not broken. An older DPC reports nothing here, and for that one the guess
         is still better than silence. */
      body.push(el('p', { class: 'warn' },
        el('strong', { text: 'The ad filter is not running on this phone. ' }),
        st.ad_filter_reason
          ? 'The phone says: ' + st.ad_filter_reason + '.'
          : 'It is switched on for this child, so the phone will start it at its next sync. If it '
            + 'stays off, the list may not have downloaded \u2014 check the filter list in Rules, '
            + 'and that the phone is online.'));
    } else if (st.ad_filter_running === true) {
      body.push(el('p', { class: 'muted', text: 'Ad filter: running'
        + (st.ad_filter_rules ? ' with ' + st.ad_filter_rules.toLocaleString() + ' rules' : '')
        + (st.ad_filter_fetched_at ? ', list fetched ' + fmtTime(st.ad_filter_fetched_at) : '')
        + '.' }));
    }
    // `undefined` draws nothing at all: a phone that has not reported is not a phone with a
    // problem, and a line saying so would be a measurement nobody took.
  }

  if (desired) {
    const used = desired.used_minutes || 0;
    const quota = desired.quota_minutes || 0;
    if (quota > 0) {
      const pct = Math.min(100, Math.round((used / quota) * 100));
      body.push(el('div', { class: 'stack' },
        el('div', { class: 'row' },
          el('span', { class: 'muted', text: 'Screen time today' }),
          el('span', { text: fmtMinutes(used) + ' of ' + fmtMinutes(quota)
            + (desired.bonus_minutes ? ' (incl. ' + fmtMinutes(desired.bonus_minutes) + ' extra)' : '') })),
        el('div', { class: 'meter' }, el('span', { class: used >= quota ? 'over' : '', style: { width: pct + '%' } })),
        dev.child_id ? bonusButtons(dev) : null));
    } else {
      body.push(el('p', { class: 'muted', text: 'Screen time today: ' + fmtMinutes(used) + ' (no daily limit)' }));
    }
    if (desired.suspend_reason) {
      body.push(el('p', { class: 'muted', text: 'Apps are paused right now: ' + desired.suspend_reason.toLowerCase() + '.' }));
    }
    if ((desired.pending_approval || []).length) {
      body.push(el('p', { class: 'muted', text: desired.pending_approval.length
        + ' app(s) are paused waiting for your decision — Apps, at the top.' }));
    }
  } else if (dev.enrolled) {
    body.push(el('p', { class: 'muted', text: 'No state reported yet.' }));
  }

  const cmd = (type, label) => el('button', {
    class: 'btn', type: 'button', text: label, disabled: !dev.enrolled,
    onclick: () => act(label, async () => {
      await api('/devices/' + dev.id + '/commands', { method: 'POST', body: { type } });
      refresh();
    }),
  });

  body.push(el('div', { class: 'btn-grid' },
    dev.locked ? cmd('UNLOCK_DEVICE', 'Unlock') : cmd('LOCK_NOW', 'Lock now'),
    cmd('TRIGGER_ALARM', 'Ring'),
    // Not a toggle, unlike Lock/Unlock above, and the difference is what this console KNOWS. The
    // server records `locked`, so that pair can show the one button that applies; nothing reports
    // whether a siren is playing, so a toggle here would have to guess — and guessing wrong hides
    // the stop from the one person trying to press it. Both are therefore always offered. Stopping
    // a siren that is not ringing is answered "not ringing" and is not an error.
    //
    // Its absence is the defect this pair exists for: on 2026-09-07 a parent rang the phone, could
    // not stop it from the console because STOP_ALARM appeared nowhere in these assets, could not
    // stop it on the handset either, and the siren ran its full five-minute cap. STOP_ALARM had
    // been in FR-9's table, accepted by the API and implemented on the phone the whole time.
    cmd('STOP_ALARM', 'Stop ringing'),
    cmd('LOCATE_NOW', 'Locate'),
    cmd('SYNC_POLICY', 'Sync now')));

  body.push(el('div', { class: 'btn-grid' },
    // Two labels, because the button does two different things. On a phone that has never enrolled
    // it shows the code that sets it up; on one that is working it REVOKES it — and "Setup QR"
    // reads like "show me that code again", which is how the first real phone was disconnected.
    el('button', {
      class: 'btn btn-quiet', type: 'button',
      text: dev.enrolled ? 'Replace phone' : 'Setup QR',
      onclick: () => showProvisioning(dev),
    }),
    el('button', { class: 'btn btn-quiet', type: 'button', text: 'Recovery code', onclick: () => showRecovery(dev) }),
    // Still always offered, and still never *conditioned* on the comparison above. The button is
    // what a parent reaches for when a phone's reported version is wrong or missing, which is
    // exactly the case the comparison cannot see; the phone answers "already running the current
    // build" when there is nothing to do, which is a fact it establishes and this page cannot.
    //
    // The label carries the comparison when there is one, because since FR-15.6 phones update by
    // themselves within a quarter of an hour and the button is now the impatient path rather than
    // the only one.
    cmd('UPDATE_APP', behind ? 'Update to ' + behind.hosted : 'Update app')));

  return el('div', { class: 'card' }, ...body.filter(Boolean));
}

async function addDevice() {
  const name = prompt('What is this phone called? (e.g. "Mia\'s Pixel")');
  if (!name) return;
  const dev = await act('Device added', () =>
    api('/children/' + state.childId + '/devices', { method: 'POST', body: { name } }));
  if (dev) { await refresh(); showProvisioning(dev); }
}

async function addChild() {
  const name = prompt('Child\'s name');
  if (!name) return;
  const child = await act('Child added', () => api('/children', { method: 'POST', body: { name, birth_year: null } }));
  if (!child) return;
  state.children.push(child);
  selectChild(child.id);
}

/* A confirmation the parent has to read, drawn in the console's own sheet.
 *
 * Deliberately not window.confirm, for three reasons that all bite on the one path that needs it.
 * The browser draws it at the TOP of the screen, which on a phone is the furthest point from the
 * thumb and the easiest thing to dismiss by reflex. It cannot mark the destructive answer as
 * destructive, so "give up this phone" and "cancel" are the same grey. And it blocks the page's
 * JavaScript entirely: headless Chrome sits on it forever, so the guard that measures this console
 * on a phone could not open the sheet behind it, and neither could anything else. A confirmation
 * no instrument can reach is a confirmation nobody has shown works.
 *
 * Resolves false for every way out that is not the button — Esc, the backdrop, the sheet's own X.
 * The default for a destructive question is no. */
function confirmSheet({ title, lines, confirmLabel, cancelLabel }) {
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
    // fallback in openSheet, where closing fires no event at all and the promise would otherwise
    // never settle — leaving the parent looking at a closed sheet and a console that has stopped.
    const dismissed = () => finish(false);
    dlg.addEventListener('close', dismissed);
    x.addEventListener('click', dismissed);

    openSheet(title, el('div', { class: 'stack' },
      lines.map((line) => el('p', { text: line })),
      el('button', {
        class: 'btn btn-danger btn-block', type: 'button', text: confirmLabel,
        onclick: () => { finish(true); closeSheet(); },
      }),
      el('button', {
        class: 'btn btn-block', type: 'button', text: cancelLabel || 'Cancel',
        onclick: () => { finish(false); closeSheet(); },
      })));
  });
}

async function showProvisioning(dev) {
  // The server refuses this with 409 unless the acknowledgement is sent, so this sheet is the
  // second lock and not the only one: a script, an API key or a stale page cannot get past the
  // first one, and this one exists so the parent reads the consequence in their own words.
  if (dev.enrolled && !await confirmSheet({
    title: 'Give up ' + dev.name + '?',
    lines: [
      'A new setup code revokes the phone that is enrolled now. It stops reporting immediately.',
      'To get it working again you have to pick that phone up and type the new code into it: '
      + 'FamilyGuard \u203a Recovery \u203a Re-link this phone. Nothing you can do from here will '
      + 'bring it back.',
      // Not "needs a factory reset", which is what this line said until 2026-09-06 and which is
      // false. An older build has no Re-link screen, but redeeming the recovery code drops
      // no_install_unknown_sources along with everything else, so the phone can install the current
      // build from its own browser — same signing key, so Device Owner and app data survive. The
      // sentence mattered: a parent read it and concluded the phone was unrecoverable.
      'A phone running an older build has no Re-link screen yet. Use Recovery code first, then open '
      + '/dpc.apk in that phone\u2019s browser and install over the top — no factory reset, no cable.',
      'Only do this for a phone you are replacing, or one you have lost.',
    ],
    confirmLabel: 'Replace ' + dev.name,
    cancelLabel: 'Keep it as it is',
  })) return;
  const out = await act('QR ready', () => api('/devices/' + dev.id + '/provisioning', {
    method: 'POST', body: { replace_enrolled: dev.enrolled === true },
  }));
  if (!out) return;
  const holder = el('div', { class: 'stack' });
  // The SVG comes from our own server and is inserted as markup because that is what it is. It is
  // not user input: it is generated from the payload this server just built.
  const wrap = el('div');
  wrap.innerHTML = out.svg;
  holder.append(
    el('p', { class: 'muted', text: 'On a factory-reset phone, tap the welcome screen six times and scan this code. It expires ' + fmtTime(out.expires_at).replace(' ago', ' from now') + '.' }),
    wrap,
    el('p', { class: 'muted', text: 'Scanning it again later needs a new code — this one can only be used once.' }));
  // The same code in type-able form, because a phone that is ALREADY a device owner cannot be
  // provisioned again: there is no welcome screen to tap six times short of a factory reset, so on
  // the one device that most needs a new credential the QR above is unreachable. This is what goes
  // into the phone's own Re-link field. Shown only when the server sent it, so an older backend
  // renders the sheet it always did rather than an empty box captioned as a code.
  if (out.setup_code) {
    holder.append(
      el('p', { class: 'muted', text: 'Already set up, and you are re-linking it? Type this on the phone instead: FamilyGuard \u203a Recovery \u203a Re-link this phone.' }),
      el('div', { class: 'code', text: out.setup_code }));
  }
  openSheet('Set up ' + dev.name, holder);
}

async function showRecovery(dev) {
  const out = await act('Recovery code', () => api('/devices/' + dev.id + '/recovery-code'));
  if (!out) return;
  // Two things use this code, and until 2026-09-06 the sheet named only the first. The second is
  // the one a parent reaches for in an emergency: a phone that was revoked, or that is running a
  // build with no Re-link screen, is recovered through here and not through a factory reset. A
  // parent who does not know that reads "no button" and concludes the phone is gone.
  const stuck = (n, text) => el('p', { class: 'muted', text: n + '. ' + text });
  openSheet('Recovery code for ' + dev.name, el('div', { class: 'stack' },
    el('p', { class: 'muted', text: 'Type this on the phone to unlock it when there is no internet. Keep it where your child cannot read it.' }),
    el('div', { class: 'code', text: out.recovery_code }),
    el('h3', { text: 'Phone unlinked, or on an old build?' }),
    el('p', { class: 'muted', text: 'The same code brings it back. No factory reset, no cable. Do not restart the phone between steps 1 and 2.' }),
    // Revoke-first is not a nicety. The release ends the moment a policy arrives from the server,
    // so on a phone whose credential still works it lasts until the next sync — which can be less
    // time than the download in step 2, and nothing on the phone says why it re-locked.
    el('p', { class: 'muted', text: 'Still linked and reporting? Press Replace phone first. A phone that can still reach this site puts every restriction back the moment it syncs, which can be before the download finishes.' }),
    stuck(1, 'On the phone: FamilyGuard \u203a Recovery \u203a enter this code. Every restriction lifts.'),
    stuck(2, 'In the phone\u2019s own browser, open /dpc.apk on this site and install over the top. Your settings survive.'),
    stuck(3, 'Here: Replace phone, to get a fresh setup code.'),
    stuck(4, 'On the phone: FamilyGuard \u203a Recovery \u203a Re-link this phone, and type that code.')));
}

/* ---- rules -------------------------------------------------------------- */

async function loadRules() {
  const [policy, domains, devices] = await Promise.all([
    api('/children/' + state.childId + '/policy'),
    api('/children/' + state.childId + '/blocked-domains'),
    api('/devices?child_id=' + encodeURIComponent(state.childId)),
  ]);
  return {
    policy,
    domains: domains.domains || [],
    enrolled: (devices.devices || []).some((d) => d.enrolled),
  };
}

function renderRules(data) {
  const p = data.policy;
  const save = async (patch, label) => {
    await act(label, async () => {
      state.data.policy = await api('/children/' + state.childId + '/policy', { method: 'PATCH', body: patch });
    });
    refresh();
  };

  const toggle = (key, title, hint, invert) => {
    const input = el('input', {
      type: 'checkbox', checked: (invert ? !p[key] : p[key]) || false,
      onchange: (e) => save({ [key]: invert ? !e.target.checked : e.target.checked }, title),
    });
    return el('label', { class: 'switch' },
      el('span', { class: 'switch-label' }, title, hint ? el('small', { text: hint }) : null), input);
  };

  const rules = el('div', { class: 'card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Rules' })),
    toggle('tracking_only', 'Watch only', 'See what is happening, change nothing on the phone.'),
    // FR-5.3. The hint used to read "Off means new apps wait for your approval", which is what
    // happens to an app that arrives ANYWAY — it is not what off does. Off applies
    // `no_install_apps`, so the Play Store refuses every install on that phone, and a parent
    // looking for why one named app will not install finds nothing about that app anywhere.
    // Measured the first time this was used with a real family: "i cant install whatsapp".
    toggle('allow_child_installs', 'Let them install apps',
      'Off blocks the Play Store entirely \u2014 nothing new can be installed, and anything that does arrive waits for your approval. Turn it on to add an app, then off again.'),
    toggle('youtube_blocked', 'Block YouTube', 'Blocks the app and the site.'),
    toggle('bedtime_enabled', 'Bedtime', 'Pauses apps overnight. Calls always work.'),
    // FR-5.6. Last in the card and worded as what it costs, because it is the only switch here
    // whose "on" makes the phone easier to interfere with rather than harder — and the only one
    // whose effect a parent cannot undo by flipping it back if the phone has meanwhile stopped
    // reaching the console.
    toggle('allow_debugging', 'Allow developer options and USB debugging',
      'For a phone you are developing on. Off switches adb off, and a phone that then loses contact with this console cannot be reached over USB either.'),
    // FR-5.7. Beside the adb switch because it is the other one that exists for the adult holding
    // the phone: with it off, nothing can take an app off that phone, including you over USB.
    toggle('allow_uninstall', 'Allow apps to be removed',
      'Turn on while you are setting the phone up or restoring a backup. Off means apps cannot be uninstalled \u2014 by them, or by you over USB.'));

  const bedtime = el('div', { class: 'card' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Bedtime and screen time' })),
    el('div', { class: 'field-row' },
      el('div', {}, el('label', { for: 'bt-start', text: 'Starts' }),
        el('input', { id: 'bt-start', type: 'time', value: p.bedtime_start, onchange: (e) => save({ bedtime_start: e.target.value }, 'Bedtime start') })),
      el('div', {}, el('label', { for: 'bt-end', text: 'Ends' }),
        el('input', { id: 'bt-end', type: 'time', value: p.bedtime_end, onchange: (e) => save({ bedtime_end: e.target.value }, 'Bedtime end') }))),
    el('div', {}, el('label', { for: 'quota', text: 'Daily screen time (minutes, 0 = no limit)' }),
      el('input', {
        id: 'quota', type: 'number', min: '0', max: '1440', inputmode: 'numeric', value: p.daily_limit_minutes,
        onchange: (e) => save({ daily_limit_minutes: Number(e.target.value) }, 'Daily limit'),
      })),
    // Editable, not prose. It was one line reading "Times are in Europe/Zurich." over a value the
    // console could not change — and this is the field every other time on the screen is measured
    // against: bedtime, the daily reset, and therefore the quota. A family that moves, or a policy
    // row created with the server's default rather than theirs, had no way to correct it.
    el('div', {}, el('label', { for: 'tz', text: 'Time zone' }),
      el('input', {
        id: 'tz', type: 'text', value: p.timezone || '', placeholder: 'Europe/Zurich',
        autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false',
        onchange: (e) => save({ timezone: e.target.value.trim() }, 'Time zone'),
      }),
      el('p', { class: 'muted', text: 'Bedtime and the daily reset use this zone. An IANA name \u2014 the server refuses anything it cannot look up, so a typo is answered rather than saved.' })));

  const domains = el('div', { class: 'card full' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Blocked websites' })),
    el('form', {
      class: 'field-row',
      onsubmit: async (e) => {
        e.preventDefault();
        const input = e.target.querySelector('input');
        const domain = input.value.trim();
        if (!domain) return;
        await act('Blocked ' + domain, () =>
          api('/children/' + state.childId + '/blocked-domains', { method: 'POST', body: { domain } }));
        refresh();
      },
    },
      el('input', { type: 'text', name: 'domain', placeholder: 'example.com', autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false' }),
      el('button', { class: 'btn btn-primary', type: 'submit', text: 'Block' })),
    data.domains.length
      ? el('ul', { class: 'list' }, data.domains.map((d) => el('li', {},
        el('span', { class: 'label' }, el('b', { text: d })),
        el('button', {
          class: 'btn btn-quiet btn-danger', type: 'button', text: 'Remove',
          onclick: async () => {
            await act('Unblocked ' + d, () => api('/children/' + state.childId +
              '/blocked-domains?domain=' + encodeURIComponent(d), { method: 'DELETE' }));
            refresh();
          },
        }))))
      : el('p', { class: 'muted', text: 'No sites are blocked by name.' }),
    // Editable, and legitimately clearable.
    //
    // This was one line of prose reading "Filtering uses <host>." — a setting the console showed
    // and could not change, over a default nobody had chosen. Empty is a real answer here: it means
    // the phone uses whatever encrypted resolver the network offers, which is what a phone without
    // this app on it does.
    el('div', {}, el('label', { for: 'dns', text: 'Encrypted DNS resolver (optional)' }),
      el('input', {
        id: 'dns', type: 'text', value: p.dns_host || '', placeholder: 'none',
        autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false',
        onchange: (e) => save({ dns_host: e.target.value.trim() }, 'DNS resolver'),
      }),
      el('p', { class: 'muted', text: p.dns_host
        ? 'Names resolved through ' + p.dns_host + '. A resolver only sees names, so it cannot remove advertising an app fetches over its own connection.'
        : 'Empty: the phone uses the network\u2019s own resolver. Leaving it empty is fine \u2014 a DNS resolver cannot see, and so cannot remove, advertising served inside an app.' })));

  // FR-6.6 to FR-6.9. Its own card rather than a switch in Rules, because it is the only setting
  // here that needs a second value to do anything at all — a switch with no list is a switch that
  // silently filters nothing, and the phone reports exactly that back (see the Home card).
  const adfilter = el('div', { class: 'card full' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Advertising inside apps' })),
    toggle('ad_filter', 'Filter advertising and trackers',
      'Runs on the phone itself, so it reaches advertising inside games and apps — which is where most of it is.'),
    el('div', {}, el('label', { for: 'adlist', text: 'Filter list' }),
      el('input', {
        id: 'adlist', type: 'text', value: p.ad_filter_list_url || '', placeholder: 'https://…',
        autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false',
        onchange: (e) => save({ ad_filter_list_url: e.target.value.trim() }, 'Filter list'),
      }),
      // FamilyGuard ships the fetcher and never the list: the good lists are GPL-3.0 and this is an
      // MIT project, so the URL is offered and the bytes stay where their licence put them.
      !p.ad_filter_list_url
        ? el('button', {
          class: 'btn btn-quiet', type: 'button', text: 'Use AdGuard’s list',
          onclick: () => save({ ad_filter_list_url: AD_FILTER_SUGGESTED_LIST }, 'Filter list'),
        })
        : null,
      el('p', { class: 'muted', text: p.ad_filter_list_url
        ? 'The phone fetches this list once a day and filters against it locally. FamilyGuard does not host it.'
        : 'Without a list there is nothing to filter, so the switch above does nothing until one is set. Any AdGuard- or hosts-style list works.' })),
    el('p', { class: 'muted', text: 'The phone routes its own traffic through FamilyGuard to do this, and blocks by name only — it never reads the contents of a connection. Some games that pay themselves with adverts stop at the point where the advert would play; that is the trade.' }));

  const cards = [rules, bedtime, domains, adfilter];
  // Rules are a property of the child and are saved whether or not a phone exists to carry them, so
  // this screen stays fully usable — it just says so, rather than letting a parent set a bedtime and
  // wonder why nothing happened.
  if (!data.enrolled) {
    cards.unshift(el('div', { class: 'card full' },
      el('p', { class: 'muted', text: 'No phone is enrolled for this child yet. Anything set here is saved now and applies the moment one is.' }),
      setUpAPhoneLink()));
  }
  return cards;
}

/* ---- apps --------------------------------------------------------------- */

async function loadApps() {
  const [rules, devices, catalog, managed] = await Promise.all([
    api('/children/' + state.childId + '/app-rules'),
    api('/devices?child_id=' + encodeURIComponent(state.childId)),
    // The catalog is family-wide and the declared set is per child; both are needed to draw one
    // switch per application, because "in the catalog" and "declared for this child" are different
    // facts and the switch is the second one.
    api('/apps').catch(() => ({ apps: [], configured: false })),
    api('/children/' + state.childId + '/managed-apps').catch(() => ({ managed_apps: [] })),
  ]);
  // Family-wide, so it is fetched once and not per child. Tolerated as empty on failure for the
  // same reason as the catalog above: one endpoint being unavailable must not blank the whole tab.
  const blocklist = await api('/family/blocked-packages').catch(() => ({ packages: [] }));
  const list = devices.devices || [];
  // include_system=1 is load bearing, and its absence made the whole blocklist blind. Preinstalled
  // bloatware IS a system app — that is exactly why it cannot be uninstalled and has to be hidden
  // instead — so a request without this flag returns an inventory with com.facebook.katana filtered
  // out of it. The blocklist card then reported the household's headline entry as "not installed on
  // any phone here", the datalist could never offer it, and the "Show system apps" switch below
  // filtered a list that had never contained one. Three controls, none of them evaluating anything.
  // The switch still defaults to off: 500 rows is not a list a parent reads.
  const perDevice = await Promise.all(list.map((d) =>
    d.enrolled
      ? api('/devices/' + d.id + '/apps?include_system=1').catch(() => ({ apps: [] }))
      : Promise.resolve({ apps: [] })));
  // The same envelope as `loadHome` — see the note there. `pending_approval` lives inside
  // `desired`, and read a level too high it is undefined, which is an empty queue that looks
  // exactly like a family with nothing waiting.
  const desired = await Promise.all(list.map((d) =>
    d.enrolled
      ? api('/devices/' + d.id + '/desired-state').then((r) => (r && r.desired) || null).catch(() => null)
      : Promise.resolve(null)));

  // One row per package, not per install: a family with two phones should not see Chrome twice, and
  // the rule is a property of the child anyway.
  // Counted, not first-wins: with two phones "hidden" is not one fact, and a card that showed the
  // first device's answer would report a block as done while the second child still has the app.
  // An app the phone has stopped reporting keeps its row (the server stamps removed_at rather than
  // deleting, so uninstalling to dodge a block stays visible) but is counted apart: folding it into
  // `devices` makes an app nobody has read "on the phone and not hidden yet" forever, and a state
  // that can never be reached is a state a parent learns to ignore.
  const byPackage = new Map();
  perDevice.forEach((res) => {
    for (const app of res.apps || []) {
      const gone = !!app.removed_at;
      const seen = byPackage.get(app.package_name);
      if (!seen) {
        byPackage.set(app.package_name, {
          ...app,
          devices: gone ? 0 : 1,
          hiddenOn: gone || !app.hidden ? 0 : 1,
          removedOn: gone ? 1 : 0,
          hidden: !gone && !!app.hidden,
          suspended: !gone && !!app.suspended,
        });
      } else if (gone) {
        seen.removedOn += 1;
      } else {
        seen.devices += 1;
        if (app.hidden) { seen.hiddenOn += 1; seen.hidden = true; }
        if (app.suspended) seen.suspended = true;
        if (app.label && !seen.label) seen.label = app.label;
      }
    }
  });
  // The whole rule, not just its action. `limit_minutes` is half the answer for a LIMIT rule and
  // dropping it here is what would make the console show "Daily limit" over an app that in fact
  // has 20 minutes of its own.
  const ruleFor = new Map((rules.rules || []).map((r) => [r.package_name, r]));
  // Which apps are still waiting for a decision, from the authority that decides it rather than
  // inferred from "suspended and has no rule" — those two come apart at bedtime, when every app is
  // suspended and none of them is pending.
  const pending = new Set();
  for (const st of desired) {
    for (const pkg of ((st && st.pending_approval) || [])) pending.add(pkg);
  }
  return {
    apps: [...byPackage.values()].sort(sortApps),
    ruleFor,
    pending,
    // The queue as the server reported it, kept separate from `pending` because `pending` is
    // edited in place as a parent answers. Taking an answer back has to know whether the app was
    // waiting before the answer was given, and after the first edit `pending` can no longer say.
    pendingAtLoad: new Set(pending),
    enrolled: list.some((d) => d.enrolled),
    catalog: catalog.apps || [],
    catalogConfigured: catalog.configured === true,
    managed: managed.managed_apps || [],
    blocklist: blocklist.packages || [],
  };
}

/* One row per package, newest build first.
 *
 * The catalog holds every version ever registered — that is what makes a rollback possible — but a
 * parent chooses an APPLICATION, and the phone is sent one version of it. Showing every build as
 * its own switch would ask them to pick a version code. */
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
    .sort((x, y) => (x.newest.label || x.newest.package_name)
      .localeCompare(y.newest.label || y.newest.package_name));
}

function sortApps(a, b) {
  if (a.system_app !== b.system_app) return a.system_app ? 1 : -1;
  return (a.label || a.package_name).localeCompare(b.label || b.package_name);
}

/* ---- the applications a parent chooses (FR-16) --------------------------- */

/* The catalog card, drawn above the inventory.
 *
 * Two lists that look alike and are not: this one is what a parent DECIDES the phone should have,
 * the one below is what the phone REPORTS it has. They are kept apart, and each says which it is,
 * because a parent who confuses them either blocks an app expecting it to be removed or withdraws
 * one expecting it to be merely hidden. */
function managedAppsCard(data) {
  const card = el('div', { class: 'card full' },
    el('div', { class: 'card-head' },
      el('h2', { text: 'Apps you install' }),
      el('button', {
        class: 'btn btn-quiet', type: 'button', text: 'Manage catalog',
        onclick: () => openCatalogSheet(data),
      })));

  if (!data.catalogConfigured) {
    // Not an error, and not a blank list. A deployment may legitimately host no applications, and
    // "empty" and "not set up" need different actions from whoever is reading.
    card.append(el('p', { class: 'muted', text: 'This server is not set up to host applications. Set APK_DIR on the control plane and give it a writable folder.' }));
    return card;
  }

  const declared = new Set(data.managed.map((m) => m.package_name));
  const groups = catalogByPackage(data.catalog);

  // A package a parent declared whose build is no longer in the catalog. It is in the declared set
  // and the phone is told nothing about it, so it must be visible: silence here is an app a parent
  // believes they installed.
  const orphans = data.managed.filter((m) => !m.available);

  if (!groups.length && !orphans.length) {
    card.append(el('p', { class: 'muted', text: 'No applications have been added to this family yet.' }));
    card.append(el('button', {
      class: 'btn btn-primary btn-block', type: 'button', text: 'Add an app',
      onclick: () => openCatalogSheet(data),
    }));
    return card;
  }

  const toggle = async (pkg, on) => {
    await act(on ? 'Added to this phone' : 'Removed from this phone', () =>
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
          text: newest.package_name + ' · ' + (newest.version_name || 'build ' + newest.version_code) +
            (versions.length > 1 ? ' · ' + versions.length + ' builds' : '') +
            (newest.size_bytes ? ' · ' + fmtSize(newest.size_bytes) : ''),
        })),
      el('label', { class: 'switch switch-bare' },
        el('input', {
          type: 'checkbox',
          checked: declared.has(newest.package_name),
          'aria-label': 'Install ' + (newest.label || newest.package_name) + ' on this phone',
          onchange: (e) => toggle(newest.package_name, e.target.checked),
        })))),
    orphans.map((m) => el('li', {},
      el('span', { class: 'label' },
        el('b', { text: m.package_name }),
        el('small', { class: 'warn', text: 'chosen for this phone, but no build of it is in the catalog — nothing will be installed' })),
      el('button', {
        class: 'btn btn-quiet btn-danger', type: 'button', text: 'Remove',
        onclick: () => toggle(m.package_name, false),
      })))));

  card.append(el('p', { class: 'muted', text: 'The phone installs these by itself and puts one back if it is removed. Bedtime and app rules still apply to them.' }));
  return card;
}

/* The catalog itself: what this family can install, and the two ways in.
 *
 * A sheet rather than a sixth tab, because adding an application is a rare administrative act and
 * the thing a parent does often — deciding which child gets it — is the switch on the card behind
 * this. */
function openCatalogSheet(data) {
  const body = el('div', { class: 'stack' });

  const status = el('p', { class: 'muted' });
  const say = (message, isError) => {
    status.textContent = message;
    status.className = isError ? 'warn' : 'muted';
  };

  const file = el('input', {
    type: 'file', accept: '.apk,application/vnd.android.package-archive',
    'aria-label': 'Choose an APK file',
  });
  const label = el('input', { type: 'text', placeholder: 'Name (optional)', 'aria-label': 'Name for this app' });

  const form = el('form', {
    class: 'stack',
    onsubmit: async (e) => {
      e.preventDefault();
      const chosen = file.files && file.files[0];
      if (!chosen) { say('Choose an APK file first.', true); return; }
      say('Uploading ' + chosen.name + '…');
      // Nothing here names the package: the server reads it, the version and the signer out of the
      // archive. A name typed in the box is a display label and cannot change what is installed.
      const out = await act('Added ' + chosen.name, () => upload('/apps', chosen, label.value.trim()));
      if (out) { closeSheet(); refresh(); }
    },
  },
    el('label', { class: 'field' }, el('span', { text: 'Upload an APK' }), file),
    label,
    el('button', { class: 'btn btn-primary btn-block', type: 'submit', text: 'Upload' }));

  const scan = el('button', {
    class: 'btn btn-block', type: 'button', text: 'Scan the server folder',
    onclick: async () => {
      const out = await act('Folder scanned', () => api('/apps/scan', { method: 'POST' }));
      if (!out) return;
      const failed = Object.entries(out.failed || {});
      if (failed.length) {
        // Named rather than counted. "3 files could not be read" sends an operator to look at all
        // of them; the filename and the reason sends them to the one that is wrong.
        say(failed.map(([name, why]) => name + ': ' + why).join('\n'), true);
      } else {
        say((out.registered || []).length + ' new application(s) registered.');
      }
      refresh();
    },
  });

  const rows = catalogByPackage(data.catalog).flatMap(({ versions }) =>
    versions.sort((a, b) => b.version_code - a.version_code).map((a) => el('li', {},
      el('span', { class: 'label' },
        el('b', { text: (a.label || a.package_name) + ' ' + (a.version_name || a.version_code) }),
        el('small', {
          text: a.package_name + ' · build ' + a.version_code + ' · ' + fmtSize(a.size_bytes) +
            ' · min SDK ' + a.min_sdk + ' · ' + (a.source === 'NODE' ? 'from the server folder' : 'uploaded'),
        })),
      el('button', {
        class: 'btn btn-quiet btn-danger', type: 'button', text: 'Delete',
        onclick: async () => {
          if (!confirm('Delete ' + a.package_name + ' build ' + a.version_code + ' from the catalog?')) return;
          await act('Deleted', () => api('/apps/' + a.id, { method: 'DELETE' }));
          closeSheet();
          refresh();
        },
      }))));

  body.append(
    form,
    el('p', { class: 'muted', text: 'Or copy .apk files into the server\'s app folder and scan it. Nothing is trusted from the file name — the package, the version and the signing key are read out of the archive.' }),
    scan,
    status,
    el('h3', { text: 'In the catalog' }),
    rows.length
      ? el('ul', { class: 'list' }, rows)
      : el('p', { class: 'muted', text: 'Nothing yet.' }));

  openSheet('App catalog', body);
}

/* ---- the family blocklist (FR-18) ---------------------------------------
 *
 * Its own card rather than a fourth button on each app row. The row's Allow/Block/Default is about
 * one child; this is about the household, and putting the two controls side by side would make the
 * difference something a parent has to read a tooltip to learn.
 *
 * Nothing here uninstalls. Entries are hidden and suspended, which survives a reinstall and is
 * undone by removing the entry — so the worst outcome of a wrong guess is an app that comes back at
 * the next sync.
 */
/* What the phones say about one blocked package. `seen` is the merged inventory row, or undefined.
 *
 * Three states and not two. "Not installed here" is a working entry, not a failure. "Hidden" is the
 * phone confirming. "Not hidden yet" is the only one that needs a parent, and it is normal for the
 * minute between blocking an app and the phone's next sync — so it says what it is waiting for
 * rather than announcing a fault. */
function blocklistState(seen) {
  if (!seen || (!seen.devices && !seen.removedOn)) return 'Not installed on any phone here.';
  // Present on nothing, but a phone did report it once. Naming it as removed rather than as never
  // installed is the difference a parent needs: the entry is what stops it coming back.
  if (!seen.devices) return 'Uninstalled — the entry keeps it from coming back.';
  if (seen.hiddenOn >= seen.devices) {
    return seen.devices > 1
      ? 'Hidden on all ' + seen.devices + ' phones.'
      : 'Hidden on the phone.';
  }
  if (seen.hiddenOn === 0) return 'On the phone and not hidden yet — waiting for it to sync.';
  return 'Hidden on ' + seen.hiddenOn + ' of ' + seen.devices + ' phones.';
}

function familyBlocklistCard(data) {
  const canEdit = state.parent && (state.parent.role === 'PRIMARY_ADMIN' || state.parent.role === 'ADMIN');
  const card = el('div', { class: 'card full' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Blocked for everyone' })),
    el('p', { class: 'muted', text: canEdit
      ? 'Applies to every child, including any added later. These apps are hidden and cannot run; they are not uninstalled, and removing an entry brings the app back.'
      : 'Applies to every child. Ask an admin to change it.' }));

  const remove = async (pkg) => {
    await act('Unblocked for everyone', () =>
      api('/family/blocked-packages?package_name=' + encodeURIComponent(pkg), { method: 'DELETE' }));
    refresh();
  };

  const reported = new Map(data.apps.map((a) => [a.package_name, a]));

  card.append(el('ul', { class: 'list' }, data.blocklist.map((e) => {
    const seen = reported.get(e.package_name);
    return el('li', {},
      el('span', { class: 'label' },
        el('b', { text: e.label || (seen && seen.label) || e.package_name }),
        el('small', { text: e.package_name + (e.source === 'BUILTIN' ? ' · suggested' : '') }),
        // Read back from the phone, never from the rule (FR-18.6). The rule is what was asked for;
        // this is what the device says it is doing, and a parent asking "is the bloatware gone?"
        // is asking the second question. Saying which packages are present at all separates "this
        // is doing something" from "this is covering a package no phone here has" — and the second
        // is not a defect: an entry is what stops a preinstall coming back.
        el('small', { class: seen && seen.hiddenOn === 0 ? 'warn' : 'muted', text: blocklistState(seen) }),
        e.reason ? el('small', { class: 'muted', text: e.reason }) : null),
      canEdit
        ? el('button', { class: 'btn', type: 'button', text: 'Unblock', onclick: () => remove(e.package_name) })
        : null);
  })));

  if (!data.blocklist.length) {
    card.append(el('p', { class: 'muted', text: 'Nothing is blocked for the whole family.' }));
  }

  if (!canEdit) return card;

  /* The datalist is the point of putting this on the Apps tab: a parent picks from what their own
     phones reported rather than typing a package name from memory. Free text still works, because
     an app that is not installed yet is exactly the one worth blocking before it arrives. */
  const blocked = new Set(data.blocklist.map((e) => e.package_name));
  const options = data.apps.filter((a) => !blocked.has(a.package_name));
  const listID = 'blocklist-candidates';
  const input = el('input', {
    type: 'text', placeholder: 'Package name, e.g. com.facebook.katana', list: listID,
    'aria-label': 'Package to block for the whole family',
    autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false',
  });
  const datalist = el('datalist', { id: listID },
    options.map((a) => el('option', {
      value: a.package_name,
      label: a.label && a.label !== a.package_name ? a.label : '',
    })));

  const submit = async () => {
    const pkg = input.value.trim();
    if (!pkg) return;
    const seen = reported.get(pkg);
    const ok = await act('Blocked for everyone', () => api('/family/blocked-packages', {
      method: 'PUT',
      body: { package_name: pkg, label: seen ? seen.label || '' : '', reason: '' },
    }));
    if (ok) { input.value = ''; refresh(); }
  };

  card.append(el('form', {
    class: 'toolbar',
    onsubmit: (e) => { e.preventDefault(); submit(); },
  }, input, datalist, el('button', { class: 'btn btn-primary', type: 'submit', text: 'Block' })));

  return card;
}

/* The four answers a parent can give about an app (FR-5.8), in the order they trade freedom for
   control. `key` is this file's name for the answer and `action` is the server's: "Daily limit" and
   "Own limit" are both LIMIT and differ only by whether an allowance rides along, because the
   difference a parent cares about ("does this app have its own clock") is not the difference the
   schema draws ("is this app approved").

   Before these existed there were two, Allow and Block, and Allow is the whitelist — it puts an app
   outside bedtime and outside the daily limit for good. So the ordinary answer, "yes, you may have
   this, and it counts like everything else", could not be given at all: approving an app and
   exempting it from every schedule were the same tap. */
const CATEGORIES = [
  {
    key: 'ALLOW', action: 'ALLOW', label: 'Always free', done: 'Always free',
    hint: 'No bedtime, no daily limit. Use it for the apps that must work at any hour.',
  },
  {
    key: 'LIMIT', action: 'LIMIT', label: 'Daily limit', done: 'Approved, with the daily limit',
    hint: 'Approved. Counts against the daily limit and pauses at bedtime, like every other app.',
  },
  {
    key: 'OWN', action: 'LIMIT', label: 'Own limit', done: 'Approved, with its own limit',
    hint: 'Approved, with a daily allowance for this app alone, on top of the daily limit.',
  },
  {
    key: 'BLOCK', action: 'BLOCK', label: 'Always blocked', done: 'Blocked',
    hint: 'Suspended and hidden on the phone.',
  },
];

/* Where "Own limit" starts when it is first chosen. A number had to be picked, and starting at zero
   would store a rule meaning "no allowance" under a button that says there is one. */
const DEFAULT_OWN_LIMIT_MINUTES = 60;

/* null means undecided — no rule at all — which with free installation off is what keeps an app
   waiting (FR-5.4). It is a real answer and not a missing one. */
function categoryOf(rule) {
  if (!rule) return null;
  if (rule.action !== 'LIMIT') return rule.action;
  return rule.limit_minutes > 0 ? 'OWN' : 'LIMIT';
}

/* The apps waiting for a decision, drawn above everything else.
 *
 * They were always in the list below — among about five hundred rows, sorted alphabetically, with
 * nothing marking them — and the only thing that said so was one line of grey text on the Home tab
 * pointing at a tab with no way to filter for them. A queue nobody can see is a queue that does not
 * get worked: measured on the family phone on 2026-09-20, four apps had been sitting in it long
 * enough for the parent to conclude the phone was broken.
 *
 * Rendered only when it is non-empty. An empty "waiting for approval" card on every visit is how a
 * real one stops being read. */
function pendingApprovalCard(data) {
  const waiting = data.apps.filter((a) => data.pending.has(a.package_name));
  if (!waiting.length) return null;
  return el('div', { class: 'card full' },
    el('div', { class: 'card-head' },
      el('h2', { text: 'Waiting for your decision' }),
      el('span', { class: 'badge', text: String(waiting.length) })),
    el('p', { class: 'muted', text:
      'These arrived after the phone was set up and are paused until you answer. '
      + 'Until then the child sees them installed and unable to open — so an app left here looks '
      + 'like a broken phone rather than like a question.' }),
    el('ul', { class: 'list applist' }, waiting.map(data.pendingRow)));
}

function renderApps(data) {
  const managed = managedAppsCard(data);

  if (!data.apps.length) {
    // Two different reasons for the same blank list, and they need different next steps: there is no
    // phone, or there is one and it has not reported yet. Saying "set up a phone" to somebody who
    // already did is the empty state telling them to redo work they have done.
    // The managed card stays: choosing what a phone should have does not depend on the phone
    // having reported yet, and a parent setting one up wants to pick the apps before it arrives.
    // The blocklist card stays even with no inventory: it is family-wide and pre-populated, so a
    // parent can see and edit it before any phone has reported.
    return [managed, familyBlocklistCard(data), data.enrolled
      ? emptyCard('▦', 'No apps reported yet',
        'The phone is enrolled but has not sent its list of installed apps. It does that shortly after setup and then once a day.')
      : emptyCard('▦', 'No apps reported yet',
        'A phone sends the list of what is installed on it shortly after it is set up. Nothing is listed here until one does.',
        setUpAPhoneLink())];
  }

  const f = state.appFilter;

  /* One tap: one request, then the page is drawn from the answer that was just given.
   *
   * This used to end in `refresh()`, which re-reads everything the tab is built from — rules, the
   * devices, each device's inventory and desired state, the catalog, the declared set and the
   * family blocklist. Nine requests per tap, and a parent working through a queue of waiting apps
   * met "too many requests" a dozen apps in. Nothing about the rule needs re-reading: the rule is
   * what was just sent. The re-read still happens, once, when the tapping stops. */
  const setRule = async (pkg, category, minutes) => {
    const c = CATEGORIES.find((x) => x.key === category);
    const landed = await tried(category === null ? 'Waiting for a decision again' : c.done, () =>
      category === null
        ? api('/children/' + state.childId + '/app-rules?package_name=' + encodeURIComponent(pkg), { method: 'DELETE' })
        : api('/children/' + state.childId + '/app-rules', {
          method: 'PUT',
          body: { package_name: pkg, action: c.action, limit_minutes: c.action === 'LIMIT' ? (minutes || 0) : 0 },
        }));
    // A write that did not land must never be drawn as if it had: re-read instead, so the page
    // shows what the server holds rather than what was attempted.
    if (!landed) { refresh(); return; }
    if (category === null) {
      data.ruleFor.delete(pkg);
      // Back to waiting, but only if it was waiting to begin with. An app the phone never held
      // for a decision does not join the queue by having its rule removed, and the server is the
      // authority on that — which is why this reads the set as it arrived rather than guessing.
      if (data.pendingAtLoad.has(pkg)) data.pending.add(pkg);
    } else {
      data.ruleFor.set(pkg, {
        package_name: pkg,
        action: c.action,
        limit_minutes: c.action === 'LIMIT' ? (minutes || 0) : 0,
      });
      data.pending.delete(pkg);
    }
    redraw();
    nudgeRefresh(1200);
  };

  const familyBlocked = new Set(data.blocklist.map((e) => e.package_name));

  /* The category control: four answers plus "undecided", which is a real state and not the absence
     of one. Tapping the lit button again does NOT clear the rule — that was the old two-button
     control's hidden third state, indistinguishable from a mis-tap — so undecided has a button of
     its own and only appears once a decision has been made. */
  const categoryControl = (app) => {
    const rule = data.ruleFor.get(app.package_name) || null;
    const cat = categoryOf(rule);
    const own = (rule && rule.limit_minutes) || DEFAULT_OWN_LIMIT_MINUTES;

    const seg = el('div', { class: 'seg' }, CATEGORIES.map((c) => el('button', {
      class: 'btn', type: 'button', text: c.label, title: c.hint,
      'aria-pressed': String(cat === c.key),
      onclick: () => setRule(app.package_name, c.key, c.key === 'OWN' ? own : 0),
    })).concat(cat === null ? [] : [el('button', {
      class: 'btn', type: 'button', text: 'Undecided',
      title: 'Put it back to waiting for a decision.',
      'aria-pressed': 'false',
      onclick: () => setRule(app.package_name, null, 0),
    })]));

    /* The number only exists while the answer it belongs to is selected. A minutes field sitting
       next to an app that is always free reads as a limit nobody is enforcing — and the server
       refuses that pair outright rather than storing it, so the two agree. */
    const minutes = cat !== 'OWN' ? null : el('label', { class: 'own-limit' },
      el('span', { class: 'switch-label' }, 'Minutes a day, for this app alone'),
      el('input', {
        type: 'number', min: '1', max: '1440', step: '5', value: String(own),
        'aria-label': 'Daily minutes for ' + (app.label || app.package_name),
        onchange: (e) => {
          const n = Math.round(Number(e.target.value));
          if (!Number.isFinite(n) || n < 1 || n > 1440) {
            // Put the stored value back rather than sending one the server will refuse: a field
            // that keeps a rejected number looks saved.
            e.target.value = String(own);
            toast('Minutes must be between 1 and 1440.');
            return;
          }
          setRule(app.package_name, 'OWN', n);
        },
      }));
    return minutes ? el('div', { class: 'app-choice' }, seg, minutes) : seg;
  };

  const row = (app) => {
    const rule = data.ruleFor.get(app.package_name) || null;
    /* An app the family blocklist covers shows "Default" here, which is true of this child's rule
       and false about the phone — the app is hidden. Saying so is the difference between a parent
       understanding why it is missing and concluding the block did not work. Allow is the documented
       exemption, so the note names it. */
    const family = familyBlocked.has(app.package_name)
      ? el('small', { class: 'muted', text: (rule && rule.action) === 'ALLOW'
        ? 'Blocked for the family — allowed for this child'
        : 'Blocked for the whole family. Allow to make an exception for this child.' })
      : null;
    // Reported for every app, not only blocked ones: a bedtime hides nothing but suspends
    // everything, and "why is this greyed out" is the same question. A row the phone has stopped
    // reporting says so first — its Allow/Block buttons still work, and a rule set against an app
    // that is no longer there is worth knowing about before it is set, not after.
    const restrained = !app.devices
      ? el('small', { class: 'muted', text: 'Not on the phone any more.' })
      : app.hidden
        ? el('small', { class: 'muted', text: 'Hidden on the phone right now.' })
        : app.suspended
          ? el('small', { class: 'muted', text: 'Paused on the phone right now.' })
          : null;
    return el('li', {},
      el('span', { class: 'label' },
        el('b', { text: app.label || app.package_name }),
        el('small', { text: app.package_name + (app.system_app ? ' · system' : '') }),
        family,
        restrained),
      categoryControl(app));
  };

  const matches = (app) => {
    if (!f.system && app.system_app) return false;
    const rule = data.ruleFor.get(app.package_name) || null;
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

  /* Repainting only the list, never the toolbar: rebuilding the search field on every keystroke
     would move the caret to the end of it, which makes correcting a typo impossible. */
  const paint = () => {
    const shown = data.apps.filter(matches);
    count.textContent = shown.length === data.apps.length
      ? data.apps.length + (data.apps.length === 1 ? ' app' : ' apps')
      : shown.length + ' of ' + data.apps.length + ' apps';
    list.replaceChildren(...shown.map(row));
    if (!shown.length) {
      list.append(el('li', {}, el('span', { class: 'muted', text: 'Nothing matches that filter.' })));
    }
  };

  const search = el('input', {
    type: 'search', value: f.q, placeholder: 'Search apps',
    'aria-label': 'Search apps', autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false',
    oninput: (e) => { f.q = e.target.value; paint(); },
  });

  const filter = el('div', { class: 'seg' }, [
    ['all', 'All'], ['waiting', 'Waiting'], ['blocked', 'Blocked'], ['allowed', 'Allowed'], ['none', 'No rule'],
  ].map(([value, label]) => el('button', {
    class: 'btn', type: 'button', text: label,
    'aria-pressed': String(f.rule === value),
    onclick: (e) => {
      f.rule = value;
      for (const b of e.target.parentNode.children) b.setAttribute('aria-pressed', String(b === e.target));
      paint();
    },
  })));

  const system = el('label', { class: 'switch' },
    el('span', { class: 'switch-label' }, 'Show system apps',
      el('small', { text: 'The dialler, the settings app and the rest of Android.' })),
    el('input', {
      type: 'checkbox', checked: f.system,
      onchange: (e) => { f.system = e.target.checked; paint(); },
    }));

  paint();

  // The same row builder, so a decision made in the queue and one made in the list below cannot
  // drift apart into two controls that offer different answers.
  data.pendingRow = row;

  return [pendingApprovalCard(data), managed, familyBlocklistCard(data), el('div', { class: 'card full' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Apps on the phone' })),
    el('p', { class: 'muted', text: 'What the phone reports it has. Allowing or blocking one here does not install or remove it.' }),
    el('div', { class: 'toolbar' }, search, filter, system),
    count,
    list)];
}

/* ---- activity ----------------------------------------------------------- */

async function loadActivity() {
  const devices = await api('/devices?child_id=' + encodeURIComponent(state.childId));
  const list = (devices.devices || []).filter((d) => d.enrolled);
  const day = state.timelineDay ? '?day=' + encodeURIComponent(state.timelineDay) : '';
  // One request per phone for the whole tab. The timeline endpoint answers for ONE day in the
  // child's timezone and carries both halves of this page — the hours and the app totals — so the
  // separate /usage and /audit calls this used to make are gone rather than merely unread.
  const [timelines, locations] = await Promise.all([
    Promise.all(list.map((d) => api('/devices/' + d.id + '/usage/timeline' + day).catch(() => null))),
    Promise.all(list.map((d) => api('/devices/' + d.id + '/locations?limit=5').catch(() => null))),
  ]);
  // The server decides what "today" is, in the child's timezone. Asked once, on the first load that
  // did not name a day, and never overwritten — a parent who has stepped back three days must not
  // have the forward bound quietly redefined under them by a background refresh.
  if (!state.timelineDay) {
    const answered = timelines.find((t) => t && t.day);
    if (answered) state.timelineToday = answered.day;
  }
  return { devices: list, timelines, locations };
}

/**
 * One day of one phone: when the screen was on, hour by hour, and what was open for how long.
 *
 * The two halves are DIFFERENT MEASUREMENTS of the same day and that is deliberate, because they
 * fail in different ways and a parent should be able to tell which one is missing.
 *
 * The chart is built from sittings — intervals with a real start and end — which only exist from
 * 0.6.13 onward. The table is built from the cumulative day totals the phone has reported since
 * 0.6.0, which are also what the daily quota is enforced against. So an older day has a table and
 * an empty chart, and that is an honest picture rather than a broken one: the card says which of
 * the two it is looking at rather than drawing a flat line and letting it read as a quiet day.
 *
 * Every hour label and every day boundary comes from the CHILD's timezone, which the server
 * resolved — a parent reading this from another country sees their child's evening as an evening.
 */
function dayActivityCard(dev, timeline) {
  if (!timeline) {
    return el('div', { class: 'card' },
      el('div', { class: 'card-head' }, el('h2', { text: dev.name })),
      el('p', { class: 'warn', text: 'This day could not be loaded for ' + dev.name + '.' }));
  }

  const hours = timeline.hours || [];
  const apps = timeline.apps || [];
  const screen = timeline.screen_time || null;
  const usedMs = apps.reduce((sum, a) => sum + (a.foreground_ms || 0), 0);
  const chartSeconds = hours.reduce((sum, h) => sum + h.seconds, 0);

  // The child's zone, not the parent's, and a browser that cannot resolve the name says so instead
  // of silently drawing the parent's own hours onto the child's day.
  let fmtHour = null;
  let fmtDay = null;
  try {
    const h = new Intl.DateTimeFormat([], { timeZone: timeline.timezone, hour: '2-digit', hour12: false });
    const d = new Intl.DateTimeFormat([], {
      timeZone: timeline.timezone, weekday: 'short', day: 'numeric', month: 'short',
    });
    fmtHour = (ms) => h.format(new Date(ms));
    fmtDay = (ms) => d.format(new Date(ms));
  } catch (e) {
    fmtHour = null;
  }
  const localZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const hourLabel = fmtHour || ((ms) => String(new Date(ms).getHours()).padStart(2, '0'));
  const dayLabel = fmtDay ? fmtDay(Date.parse(timeline.from)) : timeline.day;

  // ---- the day stepper ----
  const step = (glyph, by, disabled) => el('button', {
    class: 'btn btn-quiet', type: 'button', text: glyph, disabled: disabled || false,
    'aria-label': by < 0 ? 'Previous day' : 'Next day',
    onclick: () => { state.timelineDay = shiftDay(timeline.day, by); refresh(); },
  });
  const atToday = !!state.timelineToday && timeline.day >= state.timelineToday;

  // ---- the chart ----
  //
  // One column per hour the day actually had — 23 or 25 of them on the two mornings the clocks
  // move, because the server walks instants rather than clock numbers. Full height is a full hour,
  // fixed rather than scaled to the busiest hour: a scaled axis makes twenty minutes and four hours
  // draw identically on their own days, and the question a parent is asking is "how much of that
  // hour", not "how does this hour compare with the rest of this one day".
  const columns = hours.map((h) => {
    const share = Math.max(0, Math.min(1, h.seconds / 3600));
    return el('div', {
      class: 'hr-col', title: hourLabel(Date.parse(h.start)) + ' · ' + fmtDuration(h.seconds),
    }, el('div', {
      class: 'hr-fill' + (h.seconds ? '' : ' empty'),
      style: { height: (share * 100).toFixed(1) + '%' },
    }));
  });
  // A label every three hours: eight of them fit a 320-pixel phone, and each lands on a real local
  // hour even on a day that has 23 or 25.
  const ticks = hours.map((h, i) => el('span', {
    class: 'hr-tick' + (i % 3 === 0 ? '' : ' blank'),
    text: i % 3 === 0 ? hourLabel(Date.parse(h.start)) : '',
  }));

  let chart;
  if (!hours.length) {
    chart = el('p', { class: 'muted', text: 'No hours to draw for this day.' });
  } else if (!chartSeconds) {
    // "Nothing was opened" and "this phone has never reported one" look identical on screen and
    // have opposite remedies. Only the server can tell them apart, which is why it answers
    // `ever_reported` on every request rather than on a second one.
    chart = timeline.ever_reported
      ? el('div', {},
        el('div', { class: 'hr-chart' }, columns),
        el('div', { class: 'hr-axis' }, ticks),
        el('p', { class: 'muted', text: 'The screen was not on at any point on this day.' }))
      : el('p', { class: 'warn', text: dev.name + ' has never reported when its screen was on, so '
          + 'this chart is empty rather than flat. The table below is measured. The chart fills in '
          + 'once the phone is running a build that records it — it sends the first hours at its '
          + 'next sync after updating.' });
  } else {
    chart = el('div', {},
      el('div', {
        class: 'hr-chart', role: 'img',
        'aria-label': 'Screen on for ' + fmtDuration(chartSeconds) + ' on ' + timeline.day
          + ', by hour in ' + timeline.timezone,
      }, columns),
      el('div', { class: 'hr-axis' }, ticks));
  }

  return el('div', { class: 'card' },
    el('div', { class: 'card-head' },
      el('h2', { text: dev.name }),
      el('span', { class: 'badge', text: fmtMinutes(screen ? screen.counted_minutes : Math.round(usedMs / 60000)) })),
    el('div', { class: 'toolbar tl-nav' },
      step('◀', -1, false),
      el('span', { class: 'muted', text: dayLabel + ' · ' + timeline.timezone }),
      step('▶', 1, atToday)),
    fmtHour ? null : el('p', { class: 'warn', text: 'This browser does not know the timezone '
      + timeline.timezone + ', so the hours below are shown in ' + localZone + ' instead.' }),
    chart,
    // Drawn from the same rows either way, so without this a phone that can measure nothing renders
    // as a day of zeros — a picture of a child who did not touch their phone. Only on a measured
    // false; `undefined` is a phone that has not said.
    ((dev.state || {}).usage_access === false)
      ? el('p', { class: 'warn', text: 'These numbers are not measured. Usage access is off on '
          + dev.name + ', so every app reports zero. Turn it on in the phone’s Settings '
          + '→ Apps → Special app access → Usage access.' })
      : null,
    screen ? screenTimeSummary(dev, screen) : null,
    appUsageTable(apps, screen));
}

/** The words for why an app cannot be used right now (FR-3.10). One table, used everywhere. */
const BLOCKED_TEXT = {
  QUOTA: 'Paused — daily limit reached',
  BEDTIME: 'Paused — bedtime',
  APP_LIMIT: 'Paused — its own limit is used up',
  BLOCKED: 'Blocked',
  PENDING: 'Waiting for your approval',
};

/**
 * The day against its limit (FR-3.9), and "+ time today" (FR-3.11).
 *
 * Counted minutes only: the home screen, System UI and FamilyGuard itself are shown as a separate,
 * uncounted line. On 2026-09-23 a phone left on its charger with the screen on spent its whole
 * daily limit on the home screen, and a meter that counted that would have shown a limit a child
 * never used as used up.
 */
function screenTimeSummary(dev, screen) {
  const used = screen.counted_minutes || 0;
  const limit = (screen.daily_limit_minutes || 0) + (screen.bonus_minutes || 0);
  const parts = [];
  if (!screen.limit_recorded) {
    parts.push(el('p', { class: 'muted', text: 'Screen time counted: ' + fmtMinutes(used)
      + '. The limit that applied on this day was not recorded — days before this was added have none.' }));
  } else if (limit > 0) {
    const pct = Math.min(100, Math.round((used / limit) * 100));
    parts.push(el('div', { class: 'stack' },
      el('div', { class: 'row' },
        el('span', { class: 'muted', text: screen.is_today ? 'Screen time today' : 'Screen time' }),
        el('span', { text: fmtMinutes(used) + ' of ' + fmtMinutes(limit)
          + (screen.bonus_minutes ? ' (' + fmtMinutes(screen.daily_limit_minutes) + ' + '
            + fmtMinutes(screen.bonus_minutes) + ' extra)' : '') })),
      el('div', { class: 'meter', role: 'img', 'aria-label': fmtMinutes(used) + ' of ' + fmtMinutes(limit) },
        el('span', { class: used >= limit ? 'over' : '', style: { width: pct + '%' } }))));
  } else {
    parts.push(el('p', { class: 'muted', text: 'Screen time counted: ' + fmtMinutes(used) + ' (no daily limit).' }));
  }
  if (screen.uncounted_minutes > 0) {
    parts.push(el('p', { class: 'muted', text: 'Home screen and system: ' + fmtMinutes(screen.uncounted_minutes)
      + ', not counted.' }));
  }
  if (screen.is_today && BLOCKED_TEXT[screen.suspend_reason]) {
    parts.push(el('p', { class: 'warn', text: BLOCKED_TEXT[screen.suspend_reason]
      + ': every app that is not always free is paused on ' + dev.name + '.' }));
  }
  if (screen.is_today && screen.daily_limit_minutes > 0) parts.push(bonusButtons(dev));
  return el('div', { class: 'stack st-summary' }, parts);
}

/** "+ time today": extra minutes for the child's current day only (FR-3.11). */
function bonusButtons(dev) {
  const grant = (minutes) => el('button', {
    class: 'btn', type: 'button', text: '+' + minutes + ' min',
    'aria-label': 'Give ' + minutes + ' extra minutes today',
    onclick: () => act('+' + minutes + ' min today', async () => {
      await api('/children/' + dev.child_id + '/bonus', { method: 'POST', body: { minutes } });
      toast(minutes + ' extra minutes for today. ' + dev.name + ' picks them up within seconds.');
      refresh();
    }),
  });
  return el('div', { class: 'stack' },
    el('span', { class: 'muted', text: 'Extra time, today only:' }),
    el('div', { class: 'btn-grid' }, grant(15), grant(30), grant(60)));
}

/**
 * Every app that was open on the day, longest first, as text.
 *
 * Text rather than a second chart on purpose: this is the half a parent acts on — it is the number
 * that decides whether a limit gets set — and a bar a thumb has to estimate against an axis is not
 * a number. The chart above answers "when", this answers "how much".
 *
 * Sub-minute rows are counted rather than listed. They round to "0 min", and a screenful of apps
 * all reporting zero is how a real list gets learned as noise — but dropping them silently would
 * understate the day, so the count says how many there are.
 */
function appUsageTable(apps, screen) {
  if (!apps.length) {
    return el('p', { class: 'muted', text: 'No app was open on this day.' });
  }
  const minutes = (a) => Math.round(a.foreground_ms / 60000);
  const shown = apps.filter((a) => minutes(a) >= 1 || a.blocked);
  const brief = apps.length - shown.length;
  if (!shown.length) {
    return el('p', { class: 'muted', text: apps.length + ' app(s) were opened, none for a full minute.' });
  }
  // One scale for every bar in the table, so two apps can be compared by eye: the longest use or
  // the largest own limit, whichever is further.
  const scale = Math.max(1, ...shown.map((a) => Math.max(minutes(a), a.limit_minutes || 0)));
  const pct = (m) => Math.min(100, (m / scale) * 100).toFixed(1) + '%';

  return el('div', {},
    el('ul', { class: 'app-bars' }, shown.map((a) => {
      const used = minutes(a);
      const own = a.limit_minutes || 0;
      const over = own > 0 && used >= own;
      return el('li', { class: 'app-bar' },
        el('div', { class: 'row' },
          el('span', {},
            el('span', { class: 'swatch', 'aria-hidden': 'true', style: { background: packageHue(a.package_name) } }),
            // The label is joined on from the phone's inventory and is empty for an app that has
            // since been uninstalled — the package name is the honest fallback, not a placeholder.
            el('b', { text: a.label || a.package_name })),
          el('span', { class: 'num', text: own > 0 ? fmtMinutes(used) + ' of ' + fmtMinutes(own) : fmtMinutes(used) })),
        el('div', {
          class: 'ubar', role: 'img',
          'aria-label': (a.label || a.package_name) + ': ' + fmtMinutes(used)
            + (own > 0 ? ' of its own ' + fmtMinutes(own) + ' limit' : ''),
        },
        el('span', { class: 'ubar-fill' + (over ? ' over' : '') + (a.counted === false ? ' uncounted' : ''), style: { width: pct(used) } }),
        own > 0 ? el('span', { class: 'ubar-limit', title: 'Limit ' + fmtMinutes(own), style: { left: pct(own) } }) : null),
        el('small', { text: a.package_name + (a.system_app ? ' · system' : '') + ' · ' + appStatusText(a, screen) }));
    })),
    brief
      ? el('p', { class: 'muted', text: brief + ' more app(s) were opened for under a minute.' })
      : null);
}

/** What governs an app, and why it is paused if it is (FR-3.10). */
function appStatusText(a, screen) {
  const rule = a.counted === false ? 'Not counted (home screen / system)'
    : a.rule === 'ALLOW' ? 'Always free'
      : a.rule === 'BLOCK' ? 'Blocked by you'
        : (a.limit_minutes || 0) > 0 ? 'Own limit ' + fmtMinutes(a.limit_minutes) + ' a day, and counts toward the daily limit'
          : 'Counts toward the daily limit';
  const now = screen && screen.is_today && BLOCKED_TEXT[a.blocked] ? ' · now: ' + BLOCKED_TEXT[a.blocked] : '';
  return rule + now;
}
/** `2026-09-20` plus or minus whole days, done in UTC where a day is always 86400000 ms. */
function shiftDay(day, by) {
  const [y, m, d] = day.split('-').map(Number);
  const at = new Date(Date.UTC(y, m - 1, d) + by * 86400000);
  return at.toISOString().slice(0, 10);
}

/**
 * A stable colour per package, so the same app is the same colour every time the card is drawn.
 *
 * Derived from the name rather than from the row's position: position changes with the day, and a
 * strip where yesterday's blue is today's orange teaches a parent nothing. Mid lightness and modest
 * saturation so it reads on both themes, which are the console's two grounds.
 */
function packageHue(name) {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360;
  return 'hsl(' + h + ' 58% 48%)';
}

function renderActivity(data) {
  if (!data.devices.length) {
    return [emptyCard('◔', 'Nothing recorded yet',
      'Screen time, app usage and locations all come from an enrolled phone. Once one is set up, this page fills itself in.',
      setUpAPhoneLink())];
  }
  const cards = [];

  data.devices.forEach((dev, i) => {
    cards.push(dayActivityCard(dev, data.timelines[i]));

    const locs = data.locations[i];
    if (locs && (locs.locations || []).length) {
      cards.push(el('div', { class: 'card' },
        el('div', { class: 'card-head' }, el('h2', { text: 'Where ' + dev.name + ' was' })),
        el('ul', { class: 'list' }, locs.locations.map((l) => el('li', {},
          el('span', { class: 'label' },
            el('b', { text: l.latitude.toFixed(5) + ', ' + l.longitude.toFixed(5) }),
            el('small', { text: fmtTime(l.captured_at) + (l.accuracy_m ? ' · ±' + Math.round(l.accuracy_m) + ' m' : '') })),
          el('a', {
            class: 'btn btn-quiet', target: '_blank', rel: 'noreferrer noopener',
            href: 'https://www.openstreetmap.org/?mlat=' + l.latitude + '&mlon=' + l.longitude + '#map=16/' + l.latitude + '/' + l.longitude,
            text: 'Map',
          }))))));
    }
  });

  return cards;
}

async function loadFamily() {
  const isPrimary = state.parent && state.parent.role === 'PRIMARY_ADMIN';
  const [parents, keys, cli] = await Promise.all([
    api('/parents'),
    // Only a primary admin may list keys, so anyone else gets a 403 rather than an empty list. The
    // catch keeps the whole screen from failing on a call the reader was never entitled to make.
    isPrimary ? api('/api-keys').catch(() => ({ api_keys: [] })) : Promise.resolve(null),
    // Not through api(): the CLI manifest lives at /fgctl, outside /api/v1 and outside auth, so it
    // takes neither the base path nor the bearer token. A deployment that ships no CLI answers
    // {"hosted": false}, and a server too old to know the route 404s — both render as an absence,
    // which is why the catch returns the same shape rather than propagating.
    fetch('/fgctl', { headers: { 'Accept': 'application/json' } })
      .then((r) => (r.ok ? r.json() : { hosted: false }))
      .catch(() => ({ hosted: false })),
  ]);
  return { parents: parents.parents || [], keys: keys && (keys.api_keys || []), isPrimary, cli };
}

/* ---- API keys (FR-17) ---------------------------------------------------- */

/* A key is the same parent, arriving without a browser.
 *
 * The card says so in those words rather than talking about scopes, because there are none: a key
 * reaches everything its creator reaches. The one exception — it cannot mint another credential —
 * is stated here too, since it is the reason revoking one is enough. */
function apiKeysCard(data) {
  const card = el('div', { class: 'card full' },
    el('div', { class: 'card-head' }, el('h2', { text: 'API keys' })));

  if (!data.isPrimary) {
    card.append(el('p', { class: 'muted', text: 'Only the primary admin can see or create API keys.' }));
    return card;
  }

  card.append(el('p', { class: 'muted', text: 'A key lets a script or an assistant act as you, without a browser. It reaches everything you reach, except creating or revoking another key or parent.' }));

  const keys = data.keys || [];
  if (keys.length) {
    card.append(el('ul', { class: 'list' }, keys.map((k) => el('li', {},
      el('span', { class: 'label' },
        el('b', { text: k.name }),
        el('small', {
          class: k.revoked_at ? 'warn' : null,
          // The prefix, never the key. It is what identifies one in a log or a config file, and it
          // cannot be used for anything.
          text: k.prefix + '… · ' + (k.revoked_at
            ? 'revoked ' + fmtTime(k.revoked_at)
            : 'last used ' + fmtTime(k.last_used_at)),
        })),
      k.revoked_at
        ? el('button', {
          class: 'btn btn-quiet btn-danger', type: 'button', text: 'Delete',
          onclick: async () => {
            if (!confirm('Delete the record of ' + k.name + '? The audit trail will point at nothing.')) return;
            await act('Key deleted', () => api('/api-keys/' + k.id, { method: 'DELETE' }));
            refresh();
          },
        })
        : el('button', {
          class: 'btn btn-quiet btn-danger', type: 'button', text: 'Revoke',
          onclick: async () => {
            if (!confirm('Revoke ' + k.name + '? Anything using it stops working immediately.')) return;
            await act('Key revoked', () => api('/api-keys/' + k.id + '/revoke', { method: 'POST' }));
            refresh();
          },
        })))));
  } else {
    card.append(el('p', { class: 'muted', text: 'No keys yet.' }));
  }

  card.append(el('form', {
    class: 'field-row',
    onsubmit: async (e) => {
      e.preventDefault();
      const input = e.target.querySelector('input');
      const name = input.value.trim();
      if (!name) return;
      const created = await act('Key created', () => api('/api-keys', { method: 'POST', body: { name } }));
      if (created) showKeyOnce(created);
      refresh();
    },
  },
    el('input', { type: 'text', placeholder: 'What is it for?', 'aria-label': 'Name for the new key', autocapitalize: 'none' }),
    el('button', { class: 'btn btn-primary', type: 'submit', text: 'Create' })));

  return card;
}

/* The one time the token is readable.
 *
 * Only its hash is stored, so this cannot be shown again by any request, by an operator, or by
 * reading the database. The sheet says that at the moment the value is on screen rather than in
 * documentation nobody is looking at while copying a secret. */
function showKeyOnce(key) {
  const value = el('code', { class: 'code-block', text: key.token });
  const copy = el('button', {
    class: 'btn btn-primary btn-block', type: 'button', text: 'Copy',
    onclick: async () => {
      try {
        await navigator.clipboard.writeText(key.token);
        toast('Copied');
      } catch (err) {
        // A clipboard the browser refuses is not a failure of this feature: the value is on screen
        // and can be selected. Saying so beats a toast that reads like the key was not created.
        toast('Could not copy — select the key and copy it by hand.', true);
      }
    },
  });
  openSheet('Copy this key now', el('div', { class: 'stack' },
    el('p', { text: 'This is the only time ' + key.name + ' can be read. Only a hash of it is stored, so it cannot be shown again.' }),
    value,
    copy,
    el('p', { class: 'muted', text: 'Send it as an Authorization: Bearer header. If it leaks, revoke it here — that is immediate, and a key cannot create another one to survive its own revocation.' })));
}

/* The CLI download. Rendered from whatever the server says it hosts rather than from a hardcoded
   list, so a deployment built without the cross-compile stage shows nothing instead of six dead
   links — and a platform added later appears here with no console change. */

const PLATFORMS = {
  'linux/amd64': 'Linux · x86-64',
  'linux/arm64': 'Linux · ARM64',
  'windows/amd64': 'Windows · x86-64',
  'windows/arm64': 'Windows · ARM64',
  'darwin/amd64': 'macOS · Intel',
  'darwin/arm64': 'macOS · Apple silicon',
};

function likelyOS() {
  const ua = navigator.userAgent || '';
  if (/Windows/i.test(ua)) return 'windows';
  if (/Mac OS X|Macintosh/i.test(ua)) return 'darwin';
  if (/Linux|X11|Android/i.test(ua)) return 'linux';
  return '';
}

function megabytes(n) {
  return (n / (1024 * 1024)).toFixed(1) + ' MB';
}

function cliCard(cli) {
  const card = el('div', { class: 'card full' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Command line' })));

  if (!cli || !cli.hosted || !(cli.artifacts || []).length) {
    card.append(el('p', { class: 'muted', text: 'This server does not host the command-line tool.' }));
    return card;
  }

  card.append(el('p', { class: 'muted', text:
    'fgctl is this server\u2019s API from a terminal, and the same binary is an MCP server an '
    + 'assistant can drive. Version ' + cli.version + '.' }));

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
      // The checksum is shown, not hidden behind a details element: it is the only way to check a
      // download, and one nobody performs if it takes a click to find.
      el('small', { text: megabytes(a.size) + ' · sha256 ' + a.sha256 })),
    el('a', { class: 'btn btn-quiet', href: a.url, download: a.name, text: 'Download' })))));

  card.append(el('p', { class: 'muted', text:
    'Then: fgctl login --url ' + location.origin + ' — it asks for an API key, which you can mint '
    + 'above. `fgctl self-update` replaces it with whatever this server hosts.' }));
  return card;
}

function renderFamily(data) {
  const isPrimary = data.isPrimary;

  const parents = el('div', { class: 'card full' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Parents' })),
    el('ul', { class: 'list' }, data.parents.map((p) => el('li', {},
      el('span', { class: 'label' },
        el('b', { text: p.display_name || p.email }),
        el('small', { text: p.email + ' · ' + p.role.replaceAll('_', ' ').toLowerCase() })),
      isPrimary && p.id !== state.parent.id
        ? el('button', {
          class: 'btn btn-quiet btn-danger', type: 'button', text: 'Remove',
          onclick: async () => {
            if (!confirm('Remove ' + p.email + '?')) return;
            await act('Parent removed', () => api('/parents/' + p.id, { method: 'DELETE' }));
            refresh();
          },
        })
        : el('span', { class: 'badge', text: p.id === state.parent.id ? 'you' : '' })))));

  if (isPrimary) {
    parents.append(el('form', {
      class: 'field-row',
      onsubmit: async (e) => {
        e.preventDefault();
        const email = e.target.querySelector('input').value.trim();
        if (!email) return;
        await act('Parent added', () => api('/parents', { method: 'POST', body: { email, role: 'ADMIN' } }));
        refresh();
      },
    },
      el('input', { type: 'email', placeholder: 'parent@example.com', autocapitalize: 'none', autocorrect: 'off' }),
      el('button', { class: 'btn btn-primary', type: 'submit', text: 'Add' })));
  } else {
    parents.append(el('p', { class: 'muted', text: 'Only the primary admin can add or remove parents.' }));
  }

  const children = el('div', { class: 'card full' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Children' })),
    el('ul', { class: 'list' }, state.children.map((c) => el('li', {},
      el('span', { class: 'label' }, el('b', { text: c.name }),
        el('small', { text: c.birth_year ? 'born ' + c.birth_year : '' })),
      el('button', {
        class: 'btn btn-quiet', type: 'button', text: 'Rename',
        onclick: async () => {
          const name = prompt('New name', c.name);
          if (!name) return;
          await act('Renamed', () => api('/children/' + c.id, { method: 'PATCH', body: { name, birth_year: c.birth_year } }));
          const refreshed = await api('/children');
          state.children = refreshed.children || [];
          renderChildSwitcher();
          refresh();
        },
      })))),
    el('button', { class: 'btn btn-block', type: 'button', text: 'Add a child', onclick: addChild }));

  const you = el('div', { class: 'card full' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Signed in' })),
    el('p', { class: 'muted', text: state.parent ? state.parent.email + ' · ' + state.parent.role.replaceAll('_', ' ').toLowerCase() : '' }),
    el('button', { class: 'btn btn-block', type: 'button', text: 'Sign out', onclick: () => signOut('Signed out.') }));

  return [parents, children, apiKeysCard(data), cliCard(data.cli), you];
}

/* ---- sheet -------------------------------------------------------------- */

function openSheet(title, body) {
  document.getElementById('sheet-title').textContent = title;
  const holder = document.getElementById('sheet-body');
  holder.replaceChildren(body);
  const dlg = document.getElementById('sheet');
  if (typeof dlg.showModal === 'function') dlg.showModal();
  else dlg.setAttribute('open', '');
}

function closeSheet() {
  const dlg = document.getElementById('sheet');
  if (typeof dlg.close === 'function') dlg.close();
  else dlg.removeAttribute('open');
}

boot();
