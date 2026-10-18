// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { SETTINGS_SECTIONS, adminOnly, keyState, sectionGranted, sectionOf, settingsLink, visibleSections, type AgentKey } from '../src/lib/settings.ts'

test('Personal, Developer and Policies are for everyone; workspace sections follow their grants', () => {
  assert.deepEqual(visibleSections(false).map(s => s.id), ['personal', 'theme', 'developer', 'vocabulary', 'policies', 'routines'])
  assert.deepEqual(visibleSections(true).map(s => s.label), ['Personal', 'Theme', 'Developer', 'Workspace', 'Vocabulary', 'Policies', 'Routines', 'Agents', 'Autopilot', 'Business', 'Product portal'])
  assert.equal(sectionOf('kinds'), 'kinds')
  assert.equal(visibleSections(false, permission => permission === 'models.read').some(section => section.id === 'kinds'), true)
  assert.equal(visibleSections(true).some(section => section.id === 'kinds'), false)
  assert.equal(visibleSections(true).some(section => section.id === 'models'), false)
  assert.deepEqual(visibleSections(true, permission => permission === 'models.read').map(section => section.id), ['personal', 'theme', 'developer', 'workspace', 'vocabulary', 'kinds', 'policies', 'models', 'routines', 'agents', 'autopilot', 'business', 'portal'])
  assert.equal(sectionOf('projects'), 'vocabulary')
  assert.equal(sectionOf('agents'), 'agents')
  assert.equal(sectionOf('autopilot'), 'autopilot')
  assert.equal(sectionOf('theme'), 'theme')
  assert.equal(settingsLink('theme', 'agents'), '/settings/theme#agents')
  assert.equal(sectionOf('business'), 'business')
  assert.equal(sectionOf('developer'), 'developer')
  assert.equal(sectionOf(undefined), 'personal')
  assert.equal(sectionOf('nope'), 'personal')
  assert.equal(settingsLink('business', 'quotes'), '/settings/business#quotes')
  assert.equal(visibleSections(false, permission => permission === 'rules.read').some(section => section.id === 'agent-rules'), true)
  assert.equal(visibleSections(true).some(section => section.id === 'agent-rules'), false)
})

test('Business opens to people who may manage plugins, for its parts card (AEON-1189)', () => {
  // Measured against what everyone sees, so a new everyone-section (Routines, AEON-1182)
  // keeps this true: the grant adds Business, and only Business, and takes nothing away.
  const everyone = visibleSections(false).map(section => section.id)
  const plugins = visibleSections(false, permission => permission === 'plugins.manage').map(section => section.id)
  assert.deepEqual(plugins.filter(id => !everyone.includes(id)), ['business'])
  assert.deepEqual(plugins.filter(id => id !== 'business'), everyone)
  assert.equal(visibleSections(false, permission => permission === 'plugins.read').some(section => section.id === 'business'), false)
  const business = SETTINGS_SECTIONS.find(section => section.id === 'business')!
  assert.equal(sectionGranted(business, false, permission => permission === 'plugins.manage'), true)
  assert.equal(sectionGranted(business, false, () => false), false)
  assert.equal(adminOnly(business), false)
  assert.equal(adminOnly(SETTINGS_SECTIONS.find(section => section.id === 'portal')!), true)
})

test('Accounts, Computers and Capacity are three sections, each for people who may read accounts (AEON-1171)', () => {
  const parts = visibleSections(false, permission => permission === 'account.read').filter(section => ['accounts', 'computers', 'capacity'].includes(section.id))
  assert.deepEqual(parts.map(s => [s.id, s.group, s.label, s.summary, s.permission]), [
    ['accounts', 'Agents and automation', 'Accounts', 'Which logins agents use', 'account.read'],
    ['computers', 'Agents and automation', 'Computers', 'Where agents run', 'account.read'],
    ['capacity', 'Agents and automation', 'Capacity', 'How much, and when', 'account.read'],
  ])
  assert.equal(visibleSections(true).some(section => ['accounts', 'computers', 'capacity'].includes(section.id)), false)
  for (const id of ['accounts', 'computers', 'capacity']) assert.equal(sectionOf(id), id)
})

test('agent keys read as active, expired or revoked', () => {
  const key = (extra: Partial<AgentKey>): AgentKey => ({ id: '1', principal_id: 'p', name: 'k', prefix: 'ab12', scopes: [], created_at: '2026-09-01T00:00:00Z', expires_at: null, last_used_at: null, revoked_at: null, ...extra })
  const now = Date.parse('2026-09-24T12:00:00Z')
  assert.equal(keyState(key({}), now), 'active')
  assert.equal(keyState(key({ expires_at: '2026-09-20T00:00:00Z' }), now), 'expired')
  assert.equal(keyState(key({ expires_at: '2026-10-20T00:00:00Z' }), now), 'active')
  assert.equal(keyState(key({ revoked_at: '2026-09-02T00:00:00Z', expires_at: '2026-09-20T00:00:00Z' }), now), 'revoked')
})

test('the sender saves what is filled in and keeps the logo it does not edit', async () => {
  const { senderWrite, senderError } = await import('../src/lib/settings.ts')
  assert.deepEqual(senderWrite({ company: 'Old', logo_file_id: 'f1', logo_sha256: 'ab' }, { company: ' INSPR Studio ', city: 'Graz', iban: 'at00 1234', bic: '' }),
    { logo_file_id: 'f1', logo_sha256: 'ab', company: 'INSPR Studio', city: 'Graz', iban: 'AT00 1234' })
  assert.match(senderError(409, 'settings revision is stale'), /changed elsewhere/)
  assert.match(senderError(400, 'invalid numbering time zone'), /Europe\/Vienna/)
})
