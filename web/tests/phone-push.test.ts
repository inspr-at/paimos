// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import test from 'node:test'

const source = readFileSync(new URL('../public/phone-approvals-sw.js', import.meta.url), 'utf8')
function worker(clients: unknown[] = []) {
  const handlers = new Map<string, (event: Record<string, unknown>) => void>()
  const notices: { title: string; options: Record<string, unknown> }[] = [], opened: string[] = []
  vm.runInNewContext(source, { URL, self: {
    addEventListener: (name: string, handler: (event: Record<string, unknown>) => void) => handlers.set(name, handler),
    location: { origin: 'https://aeon.example' },
    registration: { showNotification: (title: string, options: Record<string, unknown>) => { notices.push({ title, options }); return Promise.resolve() } },
    clients: { matchAll: async () => clients, openWindow: (url: string) => { opened.push(url); return Promise.resolve() } },
  } })
  return { handlers, notices, opened }
}
const id = '11111111-1111-4111-8111-111111111111'
test('push exposes no request context or approval actions and never caches responses', async () => {
 for (const kind of ['approval', 'attach', 'stepup']) {
  let pending: Promise<unknown> | undefined
  const w = worker()
  w.handlers.get('push')!({ data: { json: () => ({ url: `/phone-approvals/${kind}/${id}`, decision: 'approved', secret: 'private context' }) }, waitUntil: () => {} })
  assert.equal(w.notices.length, 1)
  assert.equal(JSON.stringify(w.notices).includes('private context'), false)
  assert.equal(w.notices[0].options.actions, undefined)
  assert.equal(w.handlers.has('fetch'), false)
  w.handlers.get('notificationclick')!({ notification: { data: w.notices[0].options.data, close: () => {} }, waitUntil: (p: Promise<unknown>) => { pending = p } })
  await pending
  assert.equal(w.opened[0], `https://aeon.example/phone-approvals/${kind}/${id}`)
 }
})
test('malformed and foreign notification paths remain on the same origin', async () => {
  for (const url of ['https://attacker.example/approve', '//attacker.example', `/phone-approvals/approval/${id}?approve=true`, `/phone-approvals/stepup/${id}?approve=true`, '/api/approvals/decision', '/phone-approvals/approval/invalid', 'https://attacker.example/decision-desk?item=' + id, '/decision-desk?item=invalid', '/decision-desk?item=q:' + id + '&approve=true']) {
    let pending: Promise<unknown> | undefined
  const w = worker()
    w.handlers.get('push')!({ data: { json: () => ({ url }) }, waitUntil: () => {} })
    assert.equal((w.notices[0].options.data as { path: string }).path, '/agents')
    w.handlers.get('notificationclick')!({ notification: { data: { path: url }, close: () => {} }, waitUntil: (p: Promise<unknown>) => { pending = p } })
    await pending
    assert.equal(w.opened[0], 'https://aeon.example/agents')
  }
})

// Risk: Desk deep links expose unrelated fields or open another tab instead of
// focusing the existing authenticated app. Phone routes retain their own copy.
test('Desk push accepts bounded copy and focuses an existing PAIMOS tab', async () => {
  for (const prefix of ['', 'q:', 'a:', 'm:']) {
    const path = `/decision-desk?item=${prefix}${id}`
    const navigated: string[] = []
    let focused = 0, pending: Promise<unknown> | undefined
    const tab = { url: 'https://aeon.example/agents', navigate: async (url: string) => { navigated.push(url); return tab }, focus: async () => { focused++; return tab } }
    const w = worker([{ url: 'https://attacker.example/' }, tab])
    w.handlers.get('push')!({ data: { json: () => ({ url: path, title: 'PAIMOS · Decision waiting', body: 'First line · AEON', tag: `desk-question-${id}`, comment: 'private comment' }) }, waitUntil: () => {} })
    assert.equal(w.notices[0].title, 'PAIMOS · Decision waiting')
    assert.equal(w.notices[0].options.body, 'First line · AEON')
    assert.equal(w.notices[0].options.tag, `desk-question-${id}`)
    assert.equal(JSON.stringify(w.notices).includes('private comment'), false)
    w.handlers.get('notificationclick')!({ notification: { data: w.notices[0].options.data, close: () => {} }, waitUntil: (p: Promise<unknown>) => { pending = p } })
    await pending
    assert.deepEqual(navigated, [`https://aeon.example${path}`])
    assert.equal(focused, 1)
    assert.equal(w.opened.length, 0)
  }
  const w = worker()
  w.handlers.get('push')!({ data: { json: () => ({ url: '/decision-desk', title: '3 decisions waiting', body: 'x'.repeat(1000), tag: 'desk-summary' }) }, waitUntil: () => {} })
  assert.equal(w.notices[0].title, '3 decisions waiting')
  assert.equal((w.notices[0].options.body as string).length, 400)
  assert.equal(w.notices[0].options.tag, 'desk-summary')
})
