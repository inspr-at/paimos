// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { reactive, ref } from 'vue'
import { api } from '../src/lib/api'
import { putPreferenceRow, putPreferenceScope, resetPreference } from '../src/lib/modelPrefsApi'
import { modelsSettingsLink, preferenceFailure } from '../src/lib/modelsSettings'
import { simplePerson as boardPerson } from './models-simple-fixtures'
import { deferred, flush, setupSource } from './record-source'
vi.mock('../src/lib/api', async original => ({ ...await original<typeof import('../src/lib/api')>(), api: vi.fn() }))
beforeEach(() => { vi.resetAllMocks(); vi.mocked(api).mockImplementation(async () => new Response('{}', { status: 200 })) })
afterEach(() => vi.unstubAllGlobals())

it('binds every legacy person write to the captured canonical person', async () => {
  await putPreferenceScope('person', { revision: 3, residency: 'eu' }, undefined, boardPerson)
  await putPreferenceRow('person', 'backend', { revision: 3, normal: { mode: 'auto' } }, undefined, boardPerson)
  await resetPreference('person', 3, undefined, 'backend', boardPerson)
  expect(api).toHaveBeenCalledTimes(3)
  for (const [, init] of vi.mocked(api).mock.calls) expect(new Headers(init!.headers).get('If-Prefs-Person')).toBe(boardPerson)
})
it.each([409, 428])('legacy refusal %i gives a sentence and preserves the HTTP status', async status => {
  vi.mocked(api).mockResolvedValue(new Response(JSON.stringify({ error: 'person_precondition_required' }), { status }))
  await expect(putPreferenceScope('person', { revision: 3 }, undefined, boardPerson)).rejects.toMatchObject({ status, message: preferenceFailure(status) })
})
it('preserves project, kind and ticket context when entry points navigate to Models', () => {
  const link = new URL(modelsSettingsLink({ project: { id: 'project-a' }, level: 'project', kind: 'backend', ticket: 'AEON-879', why: true }), 'http://example.invalid')
  expect(link.pathname).toBe('/settings/models')
  expect(Object.fromEntries(link.searchParams)).toEqual({ project_id: 'project-a', layer: 'rules', kind: 'backend', ticket: 'AEON-879', why: '1' })
})
it('the account posture discards a held save after switching records and keeps failures honest', async () => {
  vi.stubGlobal('navigator', { platform: 'Linux', userAgent: 'test' })
  const accountUsage = await import('../src/lib/accountUsage')
  const first = { account_id: 'a', posture: 'balanced' as const, source: 'person' as const, floor_percent: 10, own_floor_percent: 10, revision: 3, binding_revision: 2, can_set_posture: true, can_set_floor: true }
  const second = { ...first, account_id: 'b', posture: 'careful' as const, revision: 7 }
  const held = deferred<any>(), write = vi.fn(() => held.promise), read = vi.fn(async () => ({ accounts: [{ account_id: 'a', usage_policy: first }, { account_id: 'b', usage_policy: second }], has_more: false }))
  const props = reactive({ accountId: 'a', german: false }), session = { identity: { tenant: { id: 'tenant' }, principal: { id: 'owner', kind: 'person' } }, authenticationCurrent: () => true }
  const view = setupSource('components/settings/AccountUsagePosture.vue', props, { '../../stores/session': { useSession: () => session }, '../../lib/authz': { can: () => true, onAccessChange: () => () => {} }, '../../lib/identityScope': await import('../src/lib/identityScope'), '../../lib/accountUsage': { ...accountUsage, getUsageOverview: read, putAccountUsage: write } })
  await flush(); view.state.save({ posture: 'maxout' }); expect(write.mock.calls[0]![0]).toEqual(first)
  props.accountId = 'b'; await flush(); held.resolve({ ...first, posture: 'maxout', revision: 4 }); await flush()
  expect(view.state.policy.value).toEqual(second); expect(view.emitted).toEqual([])
  write.mockRejectedValue(new Error('refused')); view.state.save({ posture: 'maxout' }); await flush()
  expect(view.state.policy.value.posture).toBe('careful'); expect(view.state.error.value).toContain('could not be confirmed'); expect(view.emitted).toEqual([])
  view.stop()
})
