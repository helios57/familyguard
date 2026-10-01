/* The console's service worker (FR-28.4): it shows what the server pushes — a child asking for more
 * time, a task reported done — while the console is closed, and opens the console on a tap.
 *
 * It caches nothing. A console that works offline would show yesterday's phones as today's, and
 * the whole console is a no-cache revalidation away anyway.
 */
'use strict';

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()));

self.addEventListener('push', (event) => {
  let m = {};
  try { m = event.data ? event.data.json() : {}; } catch (e) { m = { body: event.data && event.data.text() }; }
  const title = m.title || 'Family Guard';
  event.waitUntil(Promise.all([
    self.registration.showNotification(title, {
      body: m.body || '',
      tag: m.tag || undefined,
      renotify: !!m.tag,
      icon: '/icon.svg',
      badge: '/icon.svg',
      data: { url: m.url || '#/' },
    }),
    // An open console redraws at once rather than at its next event.
    self.clients.matchAll({ type: 'window' }).then((wins) => wins.forEach((w) => w.postMessage({ type: 'push', tag: m.tag || '' }))),
  ]));
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const target = new URL('/' + ((event.notification.data && event.notification.data.url) || '#/'), self.location.origin).href;
  event.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((wins) => {
    for (const w of wins) {
      if (new URL(w.url).origin === self.location.origin) {
        return w.focus().then((f) => (f || w).navigate ? (f || w).navigate(target) : f);
      }
    }
    return self.clients.openWindow(target);
  }));
});
