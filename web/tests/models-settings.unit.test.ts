// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { reactive, ref } from 'vue'
import { api } from '../src/lib/api'
import { putPreferenceRow, putPreferenceScope, resetPreference } from '../src/lib/modelPrefsApi'
import { dismissBoardLine, putBoardOrder, putBoardProfile, resetBoardOrder } from '../src/lib/modelsBoardApi'
import { modelsSettingsLink, preferenceFailure } from '../src/lib/modelsSettings'
import { boardFixture, boardPerson } from './models-board-fixtures'
import { deferred, flush, setupSource } from './record-source'
vi.mock('../src/lib/api', async original => ({ ...await original<typeof import('../src/lib/api')>(), api: vi.fn() }))
beforeEach(() => { vi.resetAllMocks(); vi.mocked(api).mockImplementation(async () => new Response('{}', { status: 200 })) })
afterEach(() => vi.unstubAllGlobals())

it('binds every legacy and ranked person write, including delete and dismiss, to the captured canonical person', async () => {
  const context = { layer: 'mine' as const, situation: 'first' as const }
  await putPreferenceScope('person', { revision: 3, residency: 'eu' }, undefined, boardPerson)
  await putPreferenceRow('person', 'backend', { revision: 3, normal: { mode: 'auto' } }, undefined, boardPerson)
  await resetPreference('person', 3, undefined, 'backend', boardPerson)
  await putBoardOrder(context, 'backend', { rank: ['openai:sol'], not: [] }, 3, boardPerson)
  await resetBoardOrder(context, 'backend', 3, boardPerson)
  await putBoardProfile(context, { thinking: 'deep' }, 3, boardPerson)
  await dismissBoardLine(context, 'openai:nova', 3, boardPerson)
  expect(api).toHaveBeenCalledTimes(7)
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
it('the page discards a held resolution when the mounted person changes and restores that person’s mode', async () => {
  vi.stubGlobal('document', { documentElement: { lang: 'en' } })
  const stored = new Map<string, string>(), identity = ref({ tenant: { id: 'tenant' }, principal: { id: 'first', name: 'First', kind: 'person' } })
  vi.stubGlobal('localStorage', { getItem: (key: string) => stored.get(key), setItem: (key: string, value: string) => stored.set(key, value) })
  const scopeModule = await import('../src/lib/identityScope')
  const route = { query: {}, path: '/settings/models' }, held = deferred<any>(), actionKey = ref('first'), document = ref(boardFixture())
  const session = { get identity() { return identity.value }, authenticationCurrent: () => true }
  const editor = { document, actionKey, loading: ref(false), busy: ref(false), error: ref(''), editable: ref(true), key: actionKey }
  const resolve = vi.fn(() => held.promise)
  const shell = setupSource('components/settings/ModelsSection.vue', {}, {
    'vue-router': { useRoute: () => route, useRouter: () => ({ replace: vi.fn() }) }, '../../stores/session': { useSession: () => session }, '../../stores/projects': { useProjects: () => ({ projects: [], load: vi.fn() }) },
    '../../lib/authz': { can: () => true }, '../../lib/identityScope': scopeModule, '../../lib/useModelsBoard': { useModelsBoard: () => editor },
    '../../lib/modelsBoardApi': { getBoard: async () => boardFixture(), getBoardCoverage: async () => ({ consumers: [] }), resolveBoardModel: resolve }, '../../lib/modelsBoard': await import('../src/lib/modelsBoard'),
  })
  shell.state.setMode('simple'); expect(stored.get(`models-page/tenant/${boardPerson}/mode`)).toBe('simple')
  document.value = { ...boardFixture(), person_id: 'linked-person' }; await flush()
  expect(shell.state.mode.value).toBe('auto'); expect(shell.state.stateOwner.value).toBe('tenant/linked-person')
  shell.state.setMode('simple'); expect(stored.get('models-page/tenant/linked-person/mode')).toBe('simple')
  // Return to the first canonical person before exercising the login boundary.
  document.value = boardFixture(); await flush()
  expect(shell.state.mode.value).toBe('simple')
  document.value = { ...boardFixture(), revision: 4 }; await flush(); expect(resolve).toHaveBeenCalledTimes(3)
  identity.value = { tenant: { id: 'tenant' }, principal: { id: 'second', name: 'Second', kind: 'person' } }; actionKey.value = 'second'; document.value = null as any
  await flush(); held.resolve({ profile: { family: 'anthropic' }, trace: {} }); await flush()
  expect(shell.state.resolution.value).toBeNull(); expect(shell.state.reviewer.value).toBeNull(); expect(shell.state.mode.value).toBe('auto')
  expect(resolve).toHaveBeenCalledTimes(3); shell.stop()
})
it('a canonical-person change clears proof folds and discards a held evidence page under the same login', async () => {
  const stored = new Map<string, string>(), held = deferred<any>(), read = vi.fn(() => held.promise)
  vi.stubGlobal('localStorage', { getItem: (key: string) => stored.get(key), setItem: (key: string, value: string) => stored.set(key, value) })
  const props = reactive({ columns: [], german: false, person: 'first-canonical-person' })
  const session = { identity: { tenant: { id: 'tenant' }, principal: { id: 'login-principal', name: 'Name' } }, authenticationCurrent: () => true }
  const proof = setupSource('components/settings/models/ProofFreshness.vue', props, {
    'vue-router': { useRoute: () => ({ hash: '' }) }, '../../../stores/session': { useSession: () => session },
    '../../../lib/authz': { can: () => true, onAccessChange: () => () => {} }, '../../../lib/identityScope': await import('../src/lib/identityScope'),
    '../../../lib/modelsBoardApi': { getBoardEvidence: read, getEvidenceProfiles: async () => ({ profiles: [], truncated: false }) }, '../../../lib/modelsBoard': await import('../src/lib/modelsBoard'),
  })
  proof.state.toggle('more'); proof.state.toggle('proof'); await flush()
  expect(read).toHaveBeenCalledTimes(1); expect(stored.get('models-page/tenant/first-canonical-person/proof')).toContain('proof')
  props.person = 'second-canonical-person'; await flush()
  held.resolve({ items: [{ id: 'first-person-record' }], next_cursor: null }); await flush()
  expect(proof.state.folds.value).toEqual([]); expect(proof.state.evidence.value).toBeNull(); expect(read).toHaveBeenCalledTimes(1)
  expect(proof.state.owner.value).toBe('tenant/second-canonical-person'); proof.stop()
})
