// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it, vi } from 'vitest'
vi.mock('../src/lib/api', () => ({ api: vi.fn(), APIError: class extends Error {} }))
import { definitionDiff, resetChanges, ruleWords, sameRule, type DefinitionChecks } from '../src/lib/definitions'

// Risk: a full copy is misrepresented as a partial merge, later changes look
// deliberate, or a reset confirmation omits a condition, owner or order change.
it('AEON-1316 describes complete copies, conditions, owners and their retained base', () => {
  const base: DefinitionChecks = { in_progress: [{ id: 'estimate_set' }, { id: 'acceptance_present' }], done: [{ id: 'manual:review', label: 'Reviewed', owner_id: 'ada' }] }
  const workspace: DefinitionChecks = { in_progress: [{ id: 'estimate_set', labels: ['ui'] }, { id: 'acceptance_present' }, { id: 'no_active_build' }] }
  const own: DefinitionChecks = { in_progress: [{ id: 'estimate_set' }, { id: 'design_attached' }], done: [{ id: 'manual:review', label: 'Reviewed', owner_id: 'ada' }] }
  expect(definitionDiff(own, workspace, base).map(r => [r.item.id,r.kind])).toEqual([
    ['estimate_set','waiting_changed'], ['design_attached','added'], ['acceptance_present','left'], ['no_active_build','waiting_new'], ['manual:review','waiting_removed'],
  ])
  expect(definitionDiff({}, workspace).every(r => r.kind === 'left')).toBe(true)
  expect(sameRule({ id: 'design_attached' }, { id: 'design_attached', condition: 'ui', labels: [] })).toBe(true)
  expect(sameRule({ id: 'estimate_set', labels: ['UI','code'] }, { id: 'estimate_set', labels: ['code','ui'] })).toBe(true)
  expect(sameRule({ id: 'estimate_set', kinds: ['Work'] }, { id: 'estimate_set', kinds: ['work'] })).toBe(false)
  expect(sameRule({ id: 'manual:review', label: 'Reviewed', owner_id: 'ada' }, { id: 'manual:review', label: 'Reviewed', owner_id: 'grace' })).toBe(false)
  expect(ruleWords({ id: 'estimate_set', kinds: ['work'], labels: ['ui','security'] })).toBe('All tickets · Kinds: work · Labels: ui, security')
  expect(resetChanges(own, workspace)).toEqual([
    'Dropped: Approved design attached, Reviewed.', 'Comes back: Acceptance criteria written, No other build in flight.', 'Uses the workspace version: Estimate set.',
  ])
  expect(resetChanges({ in_progress: [{id:'acceptance_present'},{id:'estimate_set'}] }, base)).toContain('Ready returns to the workspace check order.')
})
