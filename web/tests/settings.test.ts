// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { keyState, sectionOf, senderLine, settingsLink, visibleSections, type AgentKey } from '../src/lib/settings.ts'

test('Personal and Developer are for everyone; workspace sections follow their grants', () => {
  assert.deepEqual(visibleSections(false).map(s => s.id), ['personal', 'developer'])
  assert.deepEqual(visibleSections(true).map(s => s.label), ['Personal', 'Developer', 'Workspace', 'Business', 'Projects', 'Product portal'])
  assert.equal(sectionOf('business'), 'business')
  assert.equal(sectionOf('developer'), 'developer')
  assert.equal(sectionOf(undefined), 'personal')
  assert.equal(sectionOf('nope'), 'personal')
  assert.equal(settingsLink('business', 'quotes'), '/settings/business#quotes')
  assert.equal(visibleSections(false, permission => permission === 'rules.read').some(section => section.id === 'agent-rules'), true)
  assert.equal(visibleSections(true).some(section => section.id === 'agent-rules'), false)
})

test('agent keys read as active, expired or revoked', () => {
  const key = (extra: Partial<AgentKey>): AgentKey => ({ id: '1', principal_id: 'p', name: 'k', prefix: 'ab12', scopes: [], created_at: '2026-09-01T00:00:00Z', expires_at: null, last_used_at: null, revoked_at: null, ...extra })
  const now = Date.parse('2026-09-24T12:00:00Z')
  assert.equal(keyState(key({}), now), 'active')
  assert.equal(keyState(key({ expires_at: '2026-09-20T00:00:00Z' }), now), 'expired')
  assert.equal(keyState(key({ expires_at: '2026-10-20T00:00:00Z' }), now), 'active')
  assert.equal(keyState(key({ revoked_at: '2026-09-02T00:00:00Z', expires_at: '2026-09-20T00:00:00Z' }), now), 'revoked')
})

test('the quote sender reads as one line of what is set', () => {
  assert.equal(senderLine({ company: 'INSPR Studio', city: 'Graz', country: 'AT', iban: 'x' }), 'INSPR Studio · Graz · AT')
  assert.equal(senderLine({ company: ' ', city: 'Graz' }), 'Graz')
  assert.equal(senderLine(null), '')
})

test('the sender saves what is filled in and keeps the logo it does not edit', async () => {
  const { senderWrite, senderError } = await import('../src/lib/settings.ts')
  assert.deepEqual(senderWrite({ company: 'Old', logo_file_id: 'f1', logo_sha256: 'ab' }, { company: ' INSPR Studio ', city: 'Graz', iban: 'at00 1234', bic: '' }),
    { logo_file_id: 'f1', logo_sha256: 'ab', company: 'INSPR Studio', city: 'Graz', iban: 'AT00 1234' })
  assert.match(senderError(409, 'settings revision is stale'), /changed elsewhere/)
  assert.match(senderError(400, 'invalid numbering time zone'), /Europe\/Vienna/)
})
