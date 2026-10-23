// SPDX-License-Identifier: AGPL-3.0-only
// No fetch handler or response cache: signed-in context always comes from the server.
const uuid = '[\\da-f]{8}(-[\\da-f]{4}){3}-[\\da-f]{12}'
const phonePath = new RegExp(`^/phone-approvals/(approval|attach|stepup)/${uuid}$`, 'i')
const deskPath = new RegExp(`^/decision-desk(?:\\?item=(?:[qam]:)?${uuid})?$`, 'i')
// Browser-originated sibling notices hand off through a fragment. The app
// validates the current person and exact configured sibling before navigating.
const siblingPath = value => value.length <= 2048 && /^\/instances\/open#instance=[a-z][a-z0-9-]{0,15}&url=[^#\s]+&owner=[^#\s]+$/.test(value)
const safePath = value => typeof value === 'string' && (phonePath.test(value) || deskPath.test(value) || siblingPath(value)) ? value : '/agents'
self.addEventListener('push', event => {
  let value = {}
  try { value = event.data.json() ?? {} } catch { /* Invalid messages show a generic notification. */ }
  const path = safePath(value.url)
  const desk = deskPath.test(path)
  const title = desk && (value.title === 'PAIMOS · Decision waiting' || /^\d{1,4} decisions waiting$/.test(value.title)) ? value.title : 'Aeon needs your decision'
  event.waitUntil(self.registration.showNotification(title, {
    body: desk && typeof value.body === 'string' ? value.body.slice(0, 400) : 'Open the app to review the request and verify with your device passkey.',
    tag: desk && (value.tag === 'desk-summary' || /^desk-(question|approval|action_request)-[\da-f-]{36}$/i.test(value.tag)) ? value.tag : path,
    data: { path },
  }))
})
self.addEventListener('notificationclick', event => {
  event.notification.close()
  const path = safePath(event.notification.data?.path)
  // A click opens a review; it never calls a decision API or an approval action.
  event.waitUntil((async () => {
    const url = new URL(path, self.location.origin).href
    const clients = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
    const existing = clients.find(client => {
      try { return new URL(client.url).origin === self.location.origin } catch { return false }
    })
    if (existing) {
      try {
        const navigated = await existing.navigate(url)
        if (navigated) { await navigated.focus(); return }
      } catch { /* A tab closed during navigation; open a fresh one. */ }
    }
    await self.clients.openWindow(url)
  })())
})
