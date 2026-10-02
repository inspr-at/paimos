// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { canAddKind, canRemoveKind, compareModelVersions, modelCopy, pickerReason, profileLine, providerDisabled, providerWarning, resetLevelLabel, resetRowLabel, setByLabel, summaryLabel, visibleKinds } from '../src/lib/modelPrefs'
import { PREF_MODELS, prefsDocument, prefsFixture } from './model-prefs-fixtures'
describe('model preferences presentation', () => {
  it('uses plain provenance, reset and summary copy', () => {
    expect(setByLabel({ locked_by: 'default', changed_here: true, set_by: 'person' })).toBe('Locked by default')
    expect(setByLabel({ locked_by: 'person', changed_here: false, set_by: 'person' })).toBe('Locked by you')
    expect(setByLabel({ locked_by: '', changed_here: true, set_by: 'person' })).toBe('Changed here')
    expect(resetRowLabel('project')).toBe('Reset to your setting')
    expect(resetRowLabel('person')).toBe('Reset to default')
    expect(resetLevelLabel('person')).toBe('Reset all your changes')
    expect(summaryLabel('person', 1)).toBe('1 change on your level.')
    expect(summaryLabel('project', 2)).toBe('2 changes for this project.')
  })
  it('never removes system kinds and scopes add and project-only kinds correctly', () => {
    const doc = prefsFixture()
    for (const kind of doc.kinds.filter(k => k.system)) for (const level of ['default', 'person', 'project'] as const) expect(canRemoveKind(kind, level, true)).toBe(false)
    expect(canRemoveKind(doc.kinds[0]!, 'default', true)).toBe(true)
    expect(canRemoveKind(doc.kinds[0]!, 'person', true)).toBe(false)
    expect(canRemoveKind(doc.kinds[4]!, 'project', true)).toBe(true)
    expect(canRemoveKind(doc.kinds[4]!, 'default', true)).toBe(false)
    expect(canAddKind(doc, 'person')).toBe(false)
    doc.can.add_default_kind = false; expect(canAddKind(doc, 'default')).toBe(false)
    expect(visibleKinds(doc.kinds, 'person').map(k => k.slug)).not.toContain('firmware')
    expect(visibleKinds(doc.kinds, 'project').map(k => k.slug)).toContain('firmware')
  })
  it('supports freeze, tighten-only and the approved warn mode under locks', () => {
    const view = { value: 'eu' as const, set_by: 'default' as const, locked_by: 'default' as const, lock_value: 'eu' as const, loosened_lock: false, qualifying_routes: 0 }
    for (const [mode, value, disabled] of [['freeze', 'any', true], ['freeze', 'eu', false], ['freeze', 'local', true], ['tighten_only', 'any', true], ['tighten_only', 'local', false], ['warn', 'any', false]] as const) expect(providerDisabled(value, view, 'person', mode)).toBe(disabled)
    expect(providerDisabled('any', view, 'default', 'freeze')).toBe(false)
    expect(providerWarning({ ...view, value: 'any', loosened_lock: true })).toContain('Looser than the lock')
    expect(providerWarning(view)).toBe('')
  })
  it('uses model versions rather than profile revisions for latest and pins', () => {
    expect(profileLine(PREF_MODELS[0]!)).toEqual({ line: 'sol', version: '6.1' })
    expect(profileLine(PREF_MODELS[2]!)).toEqual({ line: 'fable', version: '5.1' })
    expect(profileLine({ ...PREF_MODELS[2]!, model: 'claude-opus' })).toEqual({ line: 'opus', version: 'alias' })
    expect(profileLine({ ...PREF_MODELS[0]!, harness: 'opencode', model: 'google/gemini-2.5-pro' })).toEqual({ line: 'gemini-pro', version: '2.5' })
    expect(compareModelVersions('5.10', '5.9')).toBe(1)
    expect(compareModelVersions('alias', '99')).toBe(1)
    expect(compareModelVersions('5.1.0', '5.1')).toBe(0)
    const model = prefsDocument(prefsFixture()).views.person!.rows[0]!.normal
    expect(modelCopy(model).name).toBe('Automatic')
    expect(modelCopy(model).tip).toContain('ticket\'s own role')
    expect(modelCopy({ ...model, unavailable_reason: 'retired' }).detail).toContain('Automatic')
  })
  it('restricts reviewer choices by floor, ladder and qualified harness; residency waits stay explicit', () => {
    const residency = { value: 'any' as const, set_by: 'default' as const, loosened_lock: false, qualifying_routes: 4 }
    const candidates = [{ profile_id: PREF_MODELS[1]!.id, selected: false, skip_reasons: [] }]
    expect(pickerReason(PREF_MODELS[1]!, true, candidates, residency)).toContain('Codex is not qualified')
    expect(pickerReason(PREF_MODELS[0]!, true, candidates, residency)).toContain('strong or frontier')
    expect(pickerReason(PREF_MODELS[3]!, true, candidates, residency)).toBe('Outside the review ladder')
    expect(pickerReason(PREF_MODELS[0]!, false, [], { ...residency, value: 'local', qualifying_routes: 0 })).toContain('No local model route')
  })
})
