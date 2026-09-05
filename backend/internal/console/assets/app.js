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

const state = {
  session: null,
  parent: null,
  family: null,
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
    else n.setAttribute(k, v === true ? '' : String(v));
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
    const [me, family, children] = await Promise.all([
      api('/me'), api('/family'), api('/children'),
    ]);
    state.parent = me;
    state.family = family;
    state.children = children.children || [];
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
    main.replaceChildren(...view.render(data));
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return;
    if (mine !== refreshToken) return;
    main.replaceChildren(el('div', { class: 'card full' },
      el('h2', { text: 'Could not load this page' }),
      el('p', { class: 'muted', text: err.message }),
      el('button', { class: 'btn', type: 'button', text: 'Try again', onclick: refresh })));
  }
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
  clearTimeout(nudge);
  nudge = setTimeout(maybeRefresh, 400);
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
  const states = await Promise.all(list.map((d) =>
    d.enrolled ? api('/devices/' + d.id + '/desired-state').catch(() => null) : Promise.resolve(null)));
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
            style: 'width:' + Math.min(100, Math.round((used / quota) * 100)) + '%',
          }))
        : el('span', { class: 'badge', text: fmtMinutes(used) }));
  });
  return el('div', { class: 'card full strip' }, ...rows);
}

function deviceCard(dev, desired) {
  const st = dev.state || {};
  const online = st.online;
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
      && el('span', { class: 'badge', text: 'app ' + st.app_version_name }));

  const body = [head, facts];

  if (desired) {
    const used = desired.used_minutes || 0;
    const quota = desired.quota_minutes || 0;
    if (quota > 0) {
      const pct = Math.min(100, Math.round((used / quota) * 100));
      body.push(el('div', { class: 'stack' },
        el('div', { class: 'row' },
          el('span', { class: 'muted', text: 'Screen time today' }),
          el('span', { text: fmtMinutes(used) + ' of ' + fmtMinutes(quota) })),
        el('div', { class: 'meter' }, el('span', { class: used >= quota ? 'over' : '', style: 'width:' + pct + '%' }))));
    } else {
      body.push(el('p', { class: 'muted', text: 'Screen time today: ' + fmtMinutes(used) + ' (no daily limit)' }));
    }
    if (desired.suspend_reason) {
      body.push(el('p', { class: 'muted', text: 'Apps are paused right now: ' + desired.suspend_reason.toLowerCase() + '.' }));
    }
    if ((desired.pending_approval || []).length) {
      body.push(el('p', { class: 'muted', text: desired.pending_approval.length + ' app(s) waiting for your approval — see Apps.' }));
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
    cmd('LOCATE_NOW', 'Locate'),
    cmd('SYNC_POLICY', 'Sync now')));

  body.push(el('div', { class: 'btn-grid' },
    el('button', { class: 'btn btn-quiet', type: 'button', text: 'Setup QR', onclick: () => showProvisioning(dev) }),
    el('button', { class: 'btn btn-quiet', type: 'button', text: 'Recovery code', onclick: () => showRecovery(dev) }),
    // Always offered, never conditioned on a comparison this page could make: the server does not
    // parse the APK it hosts, so it does not know its version name, and a button that appeared only
    // when the console thought an update was due would hide the one case worth having it for — a
    // phone whose reported version is wrong or missing. The phone compares version codes against
    // the file it downloaded and answers "already running the current build" when there is nothing
    // to do, which is a fact it can establish and this page cannot.
    cmd('UPDATE_APP', 'Update app')));

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

async function showProvisioning(dev) {
  const out = await act('QR ready', () => api('/devices/' + dev.id + '/provisioning', { method: 'POST' }));
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
  openSheet('Set up ' + dev.name, holder);
}

async function showRecovery(dev) {
  const out = await act('Recovery code', () => api('/devices/' + dev.id + '/recovery-code'));
  if (!out) return;
  openSheet('Recovery code for ' + dev.name, el('div', { class: 'stack' },
    el('p', { class: 'muted', text: 'Type this on the phone to unlock it when there is no internet. Keep it where your child cannot read it.' }),
    el('div', { class: 'code', text: out.recovery_code })));
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
    toggle('allow_child_installs', 'Let them install apps', 'Off means new apps wait for your approval.'),
    toggle('youtube_blocked', 'Block YouTube', 'Blocks the app and the site.'),
    toggle('bedtime_enabled', 'Bedtime', 'Pauses apps overnight. Calls always work.'));

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
    el('p', { class: 'muted', text: 'Times are in ' + p.timezone + '.' }));

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
      : el('p', { class: 'muted', text: 'Only the built-in adult-content filter is active.' }),
    el('p', { class: 'muted', text: 'Filtering uses ' + p.dns_host + '.' }));

  const cards = [rules, bedtime, domains];
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
  const list = devices.devices || [];
  const perDevice = await Promise.all(list.map((d) =>
    d.enrolled ? api('/devices/' + d.id + '/apps').catch(() => ({ apps: [] })) : Promise.resolve({ apps: [] })));

  // One row per package, not per install: a family with two phones should not see Chrome twice, and
  // the rule is a property of the child anyway.
  const byPackage = new Map();
  perDevice.forEach((res) => {
    for (const app of res.apps || []) {
      if (!byPackage.has(app.package_name)) byPackage.set(app.package_name, app);
    }
  });
  const ruleFor = new Map((rules.rules || []).map((r) => [r.package_name, r.action]));
  return {
    apps: [...byPackage.values()].sort(sortApps),
    ruleFor,
    enrolled: list.some((d) => d.enrolled),
    catalog: catalog.apps || [],
    catalogConfigured: catalog.configured === true,
    managed: managed.managed_apps || [],
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

function renderApps(data) {
  const managed = managedAppsCard(data);

  if (!data.apps.length) {
    // Two different reasons for the same blank list, and they need different next steps: there is no
    // phone, or there is one and it has not reported yet. Saying "set up a phone" to somebody who
    // already did is the empty state telling them to redo work they have done.
    // The managed card stays: choosing what a phone should have does not depend on the phone
    // having reported yet, and a parent setting one up wants to pick the apps before it arrives.
    return [managed, data.enrolled
      ? emptyCard('▦', 'No apps reported yet',
        'The phone is enrolled but has not sent its list of installed apps. It does that shortly after setup and then once a day.')
      : emptyCard('▦', 'No apps reported yet',
        'A phone sends the list of what is installed on it shortly after it is set up. Nothing is listed here until one does.',
        setUpAPhoneLink())];
  }

  const f = state.appFilter;

  const setRule = async (pkg, action) => {
    await act(action === null ? 'Rule cleared' : (action === 'BLOCK' ? 'Blocked' : 'Allowed'), () =>
      action === null
        ? api('/children/' + state.childId + '/app-rules?package_name=' + encodeURIComponent(pkg), { method: 'DELETE' })
        : api('/children/' + state.childId + '/app-rules', { method: 'PUT', body: { package_name: pkg, action } }));
    refresh();
  };

  /* Three states, named. The previous control was two buttons where tapping the lit one again
     cleared the rule — a hidden third state whose only documentation was a line of prose above the
     list, and which is indistinguishable from a mis-tap. */
  const row = (app) => {
    const rule = data.ruleFor.get(app.package_name) || null;
    const choice = (label, value) => el('button', {
      class: 'btn', type: 'button', text: label,
      'aria-pressed': String(rule === value),
      onclick: () => setRule(app.package_name, value),
    });
    return el('li', {},
      el('span', { class: 'label' },
        el('b', { text: app.label || app.package_name }),
        el('small', { text: app.package_name + (app.system_app ? ' · system' : '') })),
      el('div', { class: 'seg' },
        choice('Allow', 'ALLOW'), choice('Block', 'BLOCK'), choice('Default', null)));
  };

  const matches = (app) => {
    if (!f.system && app.system_app) return false;
    const rule = data.ruleFor.get(app.package_name) || null;
    if (f.rule === 'allowed' && rule !== 'ALLOW') return false;
    if (f.rule === 'blocked' && rule !== 'BLOCK') return false;
    if (f.rule === 'none' && rule !== null) return false;
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
    ['all', 'All'], ['blocked', 'Blocked'], ['allowed', 'Allowed'], ['none', 'No rule'],
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

  return [managed, el('div', { class: 'card full' },
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
  const [usage, locations, audit, policy] = await Promise.all([
    Promise.all(list.map((d) => api('/devices/' + d.id + '/usage').catch(() => null))),
    Promise.all(list.map((d) => api('/devices/' + d.id + '/locations?limit=5').catch(() => null))),
    api('/audit?limit=40').catch(() => ({ entries: [] })),
    api('/children/' + state.childId + '/policy'),
  ]);
  return { devices: list, usage, locations, audit: audit.entries || [], policy };
}

function renderActivity(data) {
  if (!data.devices.length) {
    return [emptyCard('◔', 'Nothing recorded yet',
      'Screen time, app usage and locations all come from an enrolled phone. Once one is set up, this page fills itself in.',
      setUpAPhoneLink())];
  }
  const cards = [];

  data.devices.forEach((dev, i) => {
    const usage = data.usage[i];
    if (usage) {
      const history = usage.history || [];
      const peak = Math.max(1, ...history.map((h) => h.minutes));
      const quota = data.policy ? data.policy.daily_limit_minutes : 0;
      cards.push(el('div', { class: 'card' },
        el('div', { class: 'card-head' }, el('h2', { text: dev.name }),
          el('span', { class: 'badge', text: fmtMinutes(usage.minutes) + ' today' })),
        el('div', { class: 'bars' }, history.map((h) => el('div', {
          class: 'bar' + (quota && h.minutes > quota ? ' over' : ''),
          style: 'height:' + Math.round((h.minutes / peak) * 100) + '%',
          title: h.day + ': ' + fmtMinutes(h.minutes),
        }))),
        el('p', { class: 'muted', text: 'Last ' + history.length + ' days, ' + usage.timezone + '.' }),
        (usage.packages || []).length
          ? el('ul', { class: 'list' }, usage.packages.slice(0, 5).map((s) => el('li', {},
            el('span', { class: 'label' }, el('b', { text: s.package_name })),
            el('span', { class: 'badge', text: fmtMinutes(Math.round(s.foreground_ms / 60000)) }))))
          : el('p', { class: 'muted', text: 'No app-level detail reported for today.' })));
    }

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

  cards.push(el('div', { class: 'card full' },
    el('div', { class: 'card-head' }, el('h2', { text: 'Recent changes' })),
    data.audit.length
      ? el('ul', { class: 'list' }, data.audit.slice(0, 25).map((e) => el('li', {},
        el('span', { class: 'label' },
          el('b', { text: e.action.replaceAll('_', ' ').toLowerCase() }),
          el('small', { text: e.actor_type.toLowerCase() + ' · ' + fmtTime(e.occurred_at) })))))
      : el('p', { class: 'muted', text: 'Nothing recorded yet.' })));

  return cards;
}

/* ---- family ------------------------------------------------------------- */

async function loadFamily() {
  const isPrimary = state.parent && state.parent.role === 'PRIMARY_ADMIN';
  const [parents, keys] = await Promise.all([
    api('/parents'),
    // Only a primary admin may list keys, so anyone else gets a 403 rather than an empty list. The
    // catch keeps the whole screen from failing on a call the reader was never entitled to make.
    isPrimary ? api('/api-keys').catch(() => ({ api_keys: [] })) : Promise.resolve(null),
  ]);
  return { parents: parents.parents || [], keys: keys && (keys.api_keys || []), isPrimary };
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

  return [parents, children, apiKeysCard(data), you];
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
