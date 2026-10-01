// SPDX-License-Identifier: AGPL-3.0-only
// No fetch handler or response cache: signed-in context always comes from the server.
self.addEventListener('push', event => {
  let path = '/agents'
  try {
    const value = event.data.json()
    if (typeof value.url === 'string' && /^\/phone-approvals\/(approval|attach)\/[\da-f]{8}(-[\da-f]{4}){3}-[\da-f]{12}$/i.test(value.url)) path = value.url
  } catch { /* Invalid messages still show a generic notification. */ }
  event.waitUntil(self.registration.showNotification('Aeon needs your decision', {
    body: 'Open the app to review the request and verify with your device passkey.',
    tag: path, data: { path },
  }))
})
self.addEventListener('notificationclick', event => {
  event.notification.close()
  const raw = event.notification.data?.path
  const path = typeof raw === 'string' && /^\/phone-approvals\/(approval|attach)\/[\da-f]{8}(-[\da-f]{4}){3}-[\da-f]{12}$/i.test(raw) ? raw : '/agents'
  // A click opens a review; it never calls a decision API or an approval action.
  event.waitUntil(self.clients.openWindow(new URL(path, self.location.origin).href))
})
