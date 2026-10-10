// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { APIError, api } from '../src/lib/api'
import { getModelPreferences, putPreferenceRow, putPreferenceScope } from '../src/lib/modelPrefsApi'
import { useModelPrefsEditor } from '../src/lib/useModelPrefsEditor'
import { prefsDocument, prefsFixture, PREF_MODELS } from './model-prefs-fixtures'
vi.mock('../src/lib/api', async original => ({ ...await original<typeof import('../src/lib/api')>(), api: vi.fn() }))
const lifecycle = vi.hoisted(() => ({ cleanups: [] as (() => void)[] }))
vi.mock('vue', async original => ({ ...await original<typeof import('vue')>(), onBeforeUnmount: (cleanup: () => void) => lifecycle.cleanups.push(cleanup) }))
vi.mock('../src/lib/modelPrefsApi', async original => ({ ...await original<typeof import('../src/lib/modelPrefsApi')>(), getModelPreferences: vi.fn(), putPreferenceRow: vi.fn(), putPreferenceScope: vi.fn() }))
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
beforeEach(() => {
  vi.resetAllMocks(); lifecycle.cleanups.length = 0
  vi.mocked(getModelPreferences).mockResolvedValue(prefsDocument(prefsFixture()))
})
it('captures the row, project and revision; saves one row optimistically and rolls back a rejected write', async () => {
  const editor = useModelPrefsEditor('project-a', 'person'); await editor.load()
  const before = JSON.parse(JSON.stringify(editor.doc.value))
  vi.mocked(putPreferenceRow).mockRejectedValue(new APIError(422, 'locked_above'))
  const saving = editor.row('backend', { bucket: 'normal', value: { mode: 'pinned', profile_id: PREF_MODELS[2]!.id } })
  expect(editor.doc.value!.views.person!.rows[0]!.changed_here).toBe(true)
  await saving
  expect(putPreferenceRow).toHaveBeenCalledWith('person', 'backend', expect.objectContaining({ revision: 0, normal: { mode: 'pinned', profile_id: PREF_MODELS[2]!.id }, complex: { mode: 'auto' } }), 'project-a', before.person_id)
  expect(editor.doc.value).toEqual(before); expect(editor.notice.value).toBe('locked_above'); expect(editor.busy.value).toBe(false)
})
it('refreshes a revision conflict without claiming that the write succeeded', async () => {
  const editor = useModelPrefsEditor(undefined, 'person'); await editor.load()
  vi.mocked(putPreferenceScope).mockRejectedValue(new APIError(409, 'stale_revision', { code: 'stale_revision' }))
  const fresh = prefsDocument(prefsFixture()); fresh.levels.person!.revision = 7
  vi.mocked(getModelPreferences).mockResolvedValue(fresh)
  await editor.scope({ residency: 'eu' })
  expect(editor.doc.value!.levels.person!.revision).toBe(7)
  expect(editor.notice.value).toBe('Changed elsewhere, refreshed')
})
it('drops a held load and held mutation when its person or project context is disposed', async () => {
  const held = deferred<ReturnType<typeof prefsDocument>>()
  vi.mocked(getModelPreferences).mockReturnValueOnce(held.promise)
  const editor = useModelPrefsEditor('old-project', 'person'), loading = editor.load()
  lifecycle.cleanups[0]!(); held.resolve(prefsDocument(prefsFixture())); await loading
  expect(editor.doc.value).toBeNull()
  const next = useModelPrefsEditor('new-project', 'person'); await next.load()
  const write = deferred<Awaited<ReturnType<typeof putPreferenceScope>>>()
  vi.mocked(putPreferenceScope).mockReturnValue(write.promise)
  const saving = next.scope({ residency: 'local' })
  lifecycle.cleanups[1]!()
  const reads = vi.mocked(getModelPreferences).mock.calls.length
  write.resolve({ level: prefsFixture().levels.person!, revision: 1, running_outside: ['old-run'], residency: prefsDocument(prefsFixture()).views.person!.residency }); await saving
  expect(next.outside.value).toEqual([]); expect(getModelPreferences).toHaveBeenCalledTimes(reads)
})
it('reports a successful write followed by failed refresh and prevents further edits', async () => {
  const editor = useModelPrefsEditor(undefined, 'person'); await editor.load()
  vi.mocked(putPreferenceScope).mockResolvedValue({ level: prefsFixture().levels.person!, revision: 1, running_outside: [], residency: prefsDocument(prefsFixture()).views.person!.residency })
  vi.mocked(getModelPreferences).mockRejectedValue(new Error('offline'))
  await editor.scope({ residency: 'eu' })
  expect(editor.notice.value).toContain('Saved, but'); expect(editor.doc.value).toBeNull()
  await editor.scope({ residency: 'any' }); expect(putPreferenceScope).toHaveBeenCalledTimes(1)
})

it('preserves section locks on provider changes and snapshots inherited residency when locking', async () => {
  const doc = prefsDocument(prefsFixture()); doc.levels.person!.prefs_locked = true
  vi.mocked(getModelPreferences).mockResolvedValue(doc)
  const editor = useModelPrefsEditor(undefined, 'person'); await editor.load()
  vi.mocked(putPreferenceScope).mockResolvedValue({ level: doc.levels.person!, revision: 1, running_outside: [], residency: doc.views.person!.residency })
  await editor.scope({ residency: 'eu' })
  expect(putPreferenceScope).toHaveBeenLastCalledWith('person', { revision: 0, residency: 'eu', residency_locked: false, prefs_locked: true }, undefined, doc.person_id)
  await editor.scope({ residency_locked: true })
  expect(putPreferenceScope).toHaveBeenLastCalledWith('person', { revision: 0, residency: 'any', residency_locked: true, prefs_locked: true }, undefined, doc.person_id)
})

it('loads preference evidence with one request and no independent review ladder', async () => {
  const editor = useModelPrefsEditor('project-a', 'person'); await editor.load()
  expect(getModelPreferences).toHaveBeenCalledTimes(1)
  expect(api).not.toHaveBeenCalled()
  expect(editor.doc.value!.views.person!.choices).toHaveLength(PREF_MODELS.length)
})
