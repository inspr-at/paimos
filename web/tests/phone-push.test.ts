// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import test from 'node:test'

const source = readFileSync(new URL('../public/phone-approvals-sw.js', import.meta.url), 'utf8')
function worker() {
  const handlers = new Map<string, (event: Record<string, unknown>) => void>()
  const notices: { title: string; options: Record<string, unknown> }[] = [], opened: string[] = []
  vm.runInNewContext(source, { URL, self: {
    addEventListener: (name: string, handler: (event: Record<string, unknown>) => void) => handlers.set(name, handler),
    location: { origin: 'https://aeon.example' },
    registration: { showNotification: (title: string, options: Record<string, unknown>) => { notices.push({ title, options }); return Promise.resolve() } },
    clients: { openWindow: (url: string) => { opened.push(url); return Promise.resolve() } },
  } })
  return { handlers, notices, opened }
}
const id = '11111111-1111-4111-8111-111111111111'
test('push exposes no request context or approval actions and never caches responses', () => {
 for (const kind of ['approval', 'attach', 'stepup']) {
  const w = worker()
  w.handlers.get('push')!({ data: { json: () => ({ url: `/phone-approvals/${kind}/${id}`, decision: 'approved', secret: 'private context' }) }, waitUntil: () => {} })
  assert.equal(w.notices.length, 1)
  assert.equal(JSON.stringify(w.notices).includes('private context'), false)
  assert.equal(w.notices[0].options.actions, undefined)
  assert.equal(w.handlers.has('fetch'), false)
  w.handlers.get('notificationclick')!({ notification: { data: w.notices[0].options.data, close: () => {} }, waitUntil: () => {} })
  assert.equal(w.opened[0], `https://aeon.example/phone-approvals/${kind}/${id}`)
 }
})
test('malformed and foreign notification paths remain on the same origin', () => {
  for (const url of ['https://attacker.example/approve', '//attacker.example', `/phone-approvals/approval/${id}?approve=true`, `/phone-approvals/stepup/${id}?approve=true`, '/api/approvals/decision', '/phone-approvals/approval/invalid']) {
    const w = worker()
    w.handlers.get('push')!({ data: { json: () => ({ url }) }, waitUntil: () => {} })
    assert.equal((w.notices[0].options.data as { path: string }).path, '/agents')
    w.handlers.get('notificationclick')!({ notification: { data: { path: url }, close: () => {} }, waitUntil: () => {} })
    assert.equal(w.opened[0], 'https://aeon.example/agents')
  }
})
