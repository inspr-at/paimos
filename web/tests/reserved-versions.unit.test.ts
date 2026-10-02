// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { effectScope, nextTick, reactive, ref, type EffectScope } from 'vue'
import type { Identity } from '../src/lib/api'
import { readPreference, writePreference } from '../src/lib/preferences'
import { useDeveloperSettings } from '../src/lib/developerSettings'
import { useReleases } from '../src/stores/releases'
import type { Release, ReleaseHistory } from '../src/lib/releases'

const session = reactive<{ identity: Identity | null }>({ identity: null })
const lastSeen = ref({ last_seen: '261001000000.0.0' })
const running = { version: '261002120000.0.0', scheme: 'inspr-calendar-v2' }
vi.mock('../src/stores/session', () => ({ useSession: () => session }))
vi.mock('../src/stores/version', () => ({ useVersion: () => ({ value: running, load: async () => {} }) }))
vi.mock('../src/lib/preferences', () => ({
  readPreference: vi.fn(), writePreference: vi.fn(),
  usePreference: () => ({ value: lastSeen, ready: Promise.resolve(), save: vi.fn() }),
}))

let scope: EffectScope
let id = 0
const person = (tenant = 'workspace'): Identity => ({ principal: { id: `person-${++id}`, name: 'Person', kind: 'person' }, tenant: { id: tenant, name: tenant } })
function settings() { return scope.run(() => useDeveloperSettings())! }
beforeEach(() => {
  scope?.stop()
  scope = effectScope()
  setActivePinia(createPinia())
  session.identity = person()
  vi.mocked(readPreference).mockReset().mockResolvedValue(null)
  vi.mocked(writePreference).mockReset().mockResolvedValue(true)
})

it.each([null, {}, { show_reserved_versions: 'true' }, { show_reserved_versions: 1 }, { show_reserved_versions: false }])('defaults off for missing or malformed preferences (%j)', async value => {
  vi.mocked(readPreference).mockResolvedValue(value)
  const prefs = settings()
  await nextTick()
  expect(prefs.showReservedVersions.value).toBe(false)
})

it('saves each choice without discarding the other setting or unknown fields', async () => {
  vi.mocked(readPreference).mockResolvedValue({ show_flow_controls: true, future_field: 'preserve' })
  const prefs = settings()
  await prefs.setShowReservedVersions(true)
  expect(prefs.showReservedVersions.value).toBe(true)
  expect(prefs.showFlowControls.value).toBe(true)
  expect(writePreference).toHaveBeenLastCalledWith('developer-ui', { show_flow_controls: true, show_reserved_versions: true, future_field: 'preserve' })
  await prefs.setShowFlowControls(false)
  expect(prefs.showReservedVersions.value).toBe(true)
  expect(writePreference).toHaveBeenLastCalledWith('developer-ui', { show_flow_controls: false, show_reserved_versions: true, future_field: 'preserve' })
})

it('waits for the initial read and a successful save before revealing reservations', async () => {
  let read!: (value: Record<string, unknown>) => void
  let save!: (value: boolean) => void
  vi.mocked(readPreference).mockReturnValue(new Promise(resolve => { read = resolve }))
  vi.mocked(writePreference).mockReturnValue(new Promise(resolve => { save = resolve }))
  const prefs = settings()
  const writing = prefs.setShowReservedVersions(true)
  expect(writePreference).not.toHaveBeenCalled()
  read({ show_flow_controls: true })
  await nextTick()
  expect(prefs.showReservedVersions.value).toBe(false)
  save(true)
  await writing
  expect(prefs.showReservedVersions.value).toBe(true)
  expect(writePreference).toHaveBeenCalledWith('developer-ui', { show_flow_controls: true, show_reserved_versions: true })
})

it('a failed save retains the old choice and can be retried', async () => {
  const prefs = settings()
  vi.mocked(writePreference).mockResolvedValueOnce(false)
  await prefs.setShowReservedVersions(true)
  expect(prefs.showReservedVersions.value).toBe(false)
  expect(prefs.failed.value).toBe(true)
  await prefs.setShowReservedVersions(true)
  expect(prefs.showReservedVersions.value).toBe(true)
  expect(prefs.failed.value).toBe(false)
})

it('scopes choices to the person and workspace and leaves agents off', async () => {
  const owner = session.identity!
  const prefs = settings()
  await prefs.setShowReservedVersions(true)
  session.identity = { ...owner, tenant: { id: 'another-workspace', name: 'Other' } }
  await nextTick()
  expect(prefs.showReservedVersions.value).toBe(false)
  session.identity = person()
  await nextTick()
  expect(prefs.showReservedVersions.value).toBe(false)
  session.identity = { ...owner, principal: { ...owner.principal, kind: 'agent' } }
  await nextTick()
  await prefs.setShowReservedVersions(true)
  expect(prefs.showReservedVersions.value).toBe(false)
  session.identity = owner
  await nextTick()
  expect(prefs.showReservedVersions.value).toBe(true)
  expect(writePreference).toHaveBeenCalledTimes(1)
})

it('an account switch during the initial read cannot save into the next workspace', async () => {
  let read!: (value: Record<string, unknown>) => void
  vi.mocked(readPreference).mockReturnValueOnce(new Promise(resolve => { read = resolve }))
  const prefs = settings()
  const writing = prefs.setShowReservedVersions(true)
  session.identity = person('another-workspace')
  await nextTick()
  read({})
  await writing
  expect(writePreference).not.toHaveBeenCalled()
  expect(prefs.showReservedVersions.value).toBe(false)
})

function release(version: string, state: Release['state']): Release { return { version, state } as Release }
it('the footer counts visible new versions and does not invent one for hidden-only history', async () => {
  const prefs = settings()
  const store = useReleases()
  store.history = { current: running.version, releases: [release(running.version, 'reserved')] } as ReleaseHistory
  await store.start()
  expect(store.newCount).toBe(0)
  await prefs.setShowReservedVersions(true)
  expect(store.newCount).toBe(1)
  store.history.releases.push(release('261002100000.0.0', 'published'), release('261003100000.0.0', 'published'))
  expect(store.newCount).toBe(2) // A future release beyond the running build is not new here.
  await prefs.setShowReservedVersions(false)
  expect(store.newCount).toBe(1)
})
