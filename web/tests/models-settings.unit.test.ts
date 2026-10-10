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
it('the account reset switch discards a held save after switching records and keeps failures honest', async () => {
  vi.stubGlobal('navigator', { platform: 'Linux', userAgent: 'test' })
  const accountUsage = await import('../src/lib/accountUsage')
  const first = { account_id: 'a', posture: 'balanced' as const, source: 'person' as const, floor_percent: 10, own_floor_percent: 10, revision: 3, binding_revision: 2, can_set_posture: true, can_set_floor: true }
  const second = { ...first, account_id: 'b', revision: 7 }, credits = { count: 1, expires_at: ['2026-10-04T08:00:00Z'], source: 'vendor' as const }
  const saved = (account_id: string, revision: number) => ({ account_id, reset_policy: 'auto_before_expiry' as const, revision, binding_revision: 2, resets: credits, reset_plan: null, undo_supported: false })
  const held = deferred<any>(), write = vi.fn((..._args: unknown[]) => held.promise), read = vi.fn(async () => ({ accounts: [{ account_id: 'a', harness: 'codex', usage_policy: first, resets: credits, reset_policy: 'suggest' as const, reset_plan: null }, { account_id: 'b', harness: 'claude', usage_policy: second, resets: credits, reset_policy: 'suggest' as const, reset_plan: null }], has_more: false }))
  const props = reactive({ accountId: 'a', german: false }), session = { identity: { tenant: { id: 'tenant' }, principal: { id: 'owner', kind: 'person' } }, authenticationCurrent: () => true }
  const view = setupSource('components/settings/AccountUsagePosture.vue', props, { '../../stores/session': { useSession: () => session }, '../../lib/authz': { can: () => true, onAccessChange: () => () => {} }, '../../lib/identityScope': await import('../src/lib/identityScope'), '../../lib/accountUsage': { ...accountUsage, getUsageOverview: read, putResetPolicy: write } })
  await flush(); expect(view.state.resets.value).toMatchObject({ harness: 'codex', policy: 'suggest', credits })
  view.state.setResets('auto_before_expiry'); expect(write.mock.calls[0]!.slice(0, 2)).toEqual([first, 'auto_before_expiry'])
  // Record B is on screen when A's save lands: A's result must not touch B.
  props.accountId = 'b'; await flush(); held.resolve(saved('a', 4)); await flush()
  expect(view.state.policy.value).toEqual(second); expect(view.state.resets.value.policy).toBe('suggest'); expect(view.emitted).toEqual([])
  // A reply that does not advance the shared floor/reset revision is not a success.
  write.mockResolvedValue(saved('b', 7)); view.state.setResets('auto_before_expiry'); await flush()
  expect(view.state.resets.value.policy).toBe('suggest'); expect(view.state.error.value).toContain('could not be confirmed'); expect(view.emitted).toEqual([])
  write.mockResolvedValue(saved('b', 8)); view.state.setResets('auto_before_expiry'); await flush()
  expect(write.mock.calls.at(-1)!.slice(0, 2)).toEqual([second, 'auto_before_expiry'])
  expect(view.state.resets.value.policy).toBe('auto_before_expiry'); expect(view.state.policy.value.revision).toBe(8); expect(view.state.error.value).toBe(''); expect(view.emitted).toEqual([['changed']])
  view.stop()
})
