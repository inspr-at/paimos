// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { effectScope, nextTick, reactive, type EffectScope } from 'vue'
import type { ActiveTheme, ThemeRecord } from '../src/lib/themes'
const mocks = vi.hoisted(() => ({ session: { identity: null as unknown }, permissions: new Set<string>() }))
vi.mock('../src/stores/session', () => ({ useSession: () => mocks.session }))
vi.mock('../src/lib/authz', () => ({ can: (permission: string) => mocks.permissions.has(permission) }))
vi.mock('../src/lib/themes', () => ({ listThemes: vi.fn(), getActiveTheme: vi.fn(), getTheme: vi.fn(), selectTheme: vi.fn(), updateTheme: vi.fn(), duplicateTheme: vi.fn(), deleteTheme: vi.fn() }))
import * as api from '../src/lib/themes'
import { useThemeEditor } from '../src/stores/themeEditor'
const theme = (): ThemeRecord => ({ id: 'personal', tenant_id: 'tenant', name: 'Copper', scope: 'personal', owner_principal_id: 'person', revision: 4, created_at: '', updated_at: '', values: { primary: { light: '#0e6f6c', dark: null }, secondary: { light: '#b5642a', dark: null }, recurring_marker: { source: 'secondary', custom: null }, agents: { avatar: 'robot-5', ring: 'still', hover: true, size: 90, palette: 'deutan' } } })
const chosen = (): ActiveTheme => ({ theme: theme(), default_theme_id: 'default', selected_theme_id: 'personal', revision: 12, fallback_notice: null })
let scope: EffectScope
let editor: ReturnType<typeof useThemeEditor>
async function settle() { await nextTick(); await Promise.resolve(); await nextTick() }
beforeEach(async () => {
  vi.resetAllMocks(); mocks.permissions = new Set(['profile.write'])
  mocks.session = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } } })
  vi.mocked(api.listThemes).mockResolvedValue({ items: [theme()], next_cursor: null })
  vi.mocked(api.getActiveTheme).mockResolvedValue(chosen())
  scope = effectScope(); editor = scope.run(() => useThemeEditor())!; await settle()
})
afterEach(() => scope.stop())
it('save captures theme revision, preserves Agents and keeps failed edits', async () => {
  editor.draft.value!.values.primary.light = '#3a5fc4'
  vi.mocked(api.updateTheme).mockRejectedValue(new Error('Write failed'))
  await editor.save()
  expect(editor.dirty.value).toBe(true); expect(editor.error.value).toBe('Write failed'); expect(editor.message.value).toBe('')
  expect(api.updateTheme).toHaveBeenCalledWith(expect.objectContaining({ id: 'personal', revision: 4, values: expect.objectContaining({ agents: theme().values.agents }) }))
  vi.mocked(api.updateTheme).mockImplementation(async record => ({ ...record, revision: 5 }))
  await editor.save(); expect(editor.dirty.value).toBe(false); expect(editor.message.value).toBe('Saved.')
})
it('selection uses its independent CAS and never claims a failed choice', async () => {
  vi.mocked(api.selectTheme).mockRejectedValue(Object.assign(new Error('Conflict'), { status: 409 }))
  await editor.choose({ ...theme(), id: 'other' })
  expect(api.selectTheme).toHaveBeenCalledWith('other', 12); expect(editor.active.value!.theme.id).toBe('personal'); expect(editor.conflict.value).toBe(true)
})
it('duplicate failure after creation reports partial success honestly', async () => {
  vi.mocked(api.duplicateTheme).mockResolvedValue({ ...theme(), id: 'copy' }); vi.mocked(api.selectTheme).mockRejectedValue(new Error('Unavailable'))
  await editor.duplicate(theme())
  expect(editor.items.value.some(item => item.id === 'copy')).toBe(true); expect(editor.error.value).toMatch(/copy was created, but could not be selected/)
})
it('a permission loss stops personal writes and workspace themes remain read-only', async () => {
  expect(editor.editable({ ...theme(), scope: 'workspace' })).toBe(false)
  mocks.permissions.clear(); editor.draft.value!.name = 'Changed'; await editor.save()
  expect(api.updateTheme).not.toHaveBeenCalled()
})
it('API-shaped default themes need settings.manage, including save and delete attempts', async () => {
  const shared: ThemeRecord = { ...theme(), id: 'default', name: 'Porcelain', scope: 'default', owner_principal_id: null }
  vi.mocked(api.getActiveTheme).mockResolvedValue({ ...chosen(), theme: shared, selected_theme_id: null })
  await editor.load()
  expect(editor.editable(shared)).toBe(false)
  editor.draft.value!.name = 'Changed default'; await editor.save()
  editor.discard(); await editor.remove(shared)
  expect(api.updateTheme).not.toHaveBeenCalled(); expect(api.deleteTheme).not.toHaveBeenCalled()
  mocks.permissions.add('settings.manage')
  expect(editor.editable(shared)).toBe(true)
  editor.draft.value!.name = 'Changed default'
  vi.mocked(api.updateTheme).mockImplementation(async record => ({ ...record, revision: 5 }))
  await editor.save()
  expect(api.updateTheme).toHaveBeenCalledWith(expect.objectContaining({ id: 'default', scope: 'default', revision: 4 }))
  expect(editor.message.value).toBe('Saved.')
})
it('successful selection clears a deletion conflict and allows the fresh theme to save', async () => {
  vi.mocked(api.deleteTheme).mockRejectedValue(Object.assign(new Error('Theme changed'), { status: 409 }))
  await editor.remove(theme())
  expect(editor.conflict.value).toBe(true); expect(editor.error.value).toBe('Theme changed')
  const other = { ...theme(), id: 'other', name: 'Other', revision: 9 }
  vi.mocked(api.selectTheme).mockResolvedValue({ ...chosen(), theme: other, selected_theme_id: 'other', revision: 13 })
  await editor.choose(other)
  expect(editor.active.value!.theme.id).toBe('other'); expect(editor.conflict.value).toBe(false)
  expect(editor.error.value).toBe('')
  editor.draft.value!.name = 'Edited other'
  vi.mocked(api.updateTheme).mockImplementation(async record => ({ ...record, revision: 10 }))
  await editor.save()
  expect(api.updateTheme).toHaveBeenCalledWith(expect.objectContaining({ id: 'other', revision: 9, name: 'Edited other' }))
  expect(editor.message.value).toBe('Saved.')
})
it('a deletion conflict on another record does not disable the selected theme', async () => {
  vi.mocked(api.deleteTheme).mockRejectedValue(Object.assign(new Error('Other theme changed'), { status: 409 }))
  await editor.remove({ ...theme(), id: 'other' })
  expect(editor.error.value).toBe('Other theme changed'); expect(editor.conflict.value).toBe(false)
  editor.draft.value!.name = 'Still editable'
  vi.mocked(api.updateTheme).mockImplementation(async record => ({ ...record, revision: 5 }))
  await editor.save()
  expect(api.updateTheme).toHaveBeenCalledWith(expect.objectContaining({ id: 'personal', revision: 4 }))
})
it('a failed reload keeps an unresolved save conflict from allowing a blind resave', async () => {
  editor.draft.value!.name = 'Changed'
  vi.mocked(api.updateTheme).mockRejectedValue(Object.assign(new Error('Theme changed'), { status: 409 }))
  await editor.save()
  vi.mocked(api.getActiveTheme).mockRejectedValue(new Error('Reload failed'))
  await editor.load(); await editor.save()
  expect(editor.conflict.value).toBe(true); expect(editor.error.value).toBe('Reload failed')
  expect(api.updateTheme).toHaveBeenCalledTimes(1)
})
it.each(['selection', 'save', 'delete'] as const)('pagination preserves an unresolved %s conflict until fresh active state loads', async operation => {
  editor.cursor.value = 'next-page'
  const failure = Object.assign(new Error('Theme changed; reload before editing'), { status: 409 })
  if (operation === 'selection') {
    vi.mocked(api.selectTheme).mockRejectedValue(failure)
    await editor.choose({ ...theme(), id: 'other' })
  } else if (operation === 'save') {
    editor.draft.value!.name = 'Unsaved name'
    vi.mocked(api.updateTheme).mockRejectedValue(failure)
    await editor.save()
  } else {
    vi.mocked(api.deleteTheme).mockRejectedValue(failure)
    await editor.remove(theme())
  }
  expect(editor.conflict.value).toBe(true)
  expect(editor.error.value).toBe(failure.message)
  const retainedDraft = JSON.parse(JSON.stringify(editor.draft.value))
  let release!: (page: Awaited<ReturnType<typeof api.listThemes>>) => void
  vi.mocked(api.listThemes).mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
  const paging = editor.more()
  const pendingFeedback = editor.error.value
  release({ items: [{ ...theme(), id: 'later' }], next_cursor: null })
  await paging
  expect(pendingFeedback).toBe(failure.message)
  expect(api.listThemes).toHaveBeenLastCalledWith('next-page')
  expect(editor.items.value.map(item => item.id)).toEqual(['personal', 'later'])
  expect(editor.cursor.value).toBeNull()
  expect(editor.draft.value).toEqual(retainedDraft)
  expect(editor.conflict.value).toBe(true)
  expect(editor.error.value).toBe(failure.message)
  vi.mocked(api.getActiveTheme).mockResolvedValue({ ...chosen(), theme: { ...theme(), name: 'Fresh theme', revision: 5 }, revision: 13 })
  await editor.load()
  expect(editor.draft.value!.name).toBe('Fresh theme')
  expect(editor.conflict.value).toBe(false)
  expect(editor.error.value).toBe('')
})
it('identity change resets drafts and drops a delayed save result', async () => {
  let release!: (record: ThemeRecord) => void
  vi.mocked(api.updateTheme).mockImplementation(() => new Promise(resolve => { release = resolve }))
  editor.draft.value!.name = 'Old person draft'; const saving = editor.save()
  mocks.session.identity = null; await settle()
  release({ ...theme(), name: 'Old person draft', revision: 5 }); await saving
  expect(editor.draft.value).toBeNull(); expect(editor.items.value).toEqual([]); expect(editor.message.value).toBe('')
})
it('a revision conflict prevents blind resave; discard reloads the current record', async () => {
  editor.draft.value!.name = 'Changed'; vi.mocked(api.updateTheme).mockRejectedValue(Object.assign(new Error('Conflict'), { status: 409 }))
  await editor.save(); await editor.save(); expect(api.updateTheme).toHaveBeenCalledTimes(1)
  vi.mocked(api.getActiveTheme).mockResolvedValue({ ...chosen(), theme: { ...theme(), name: 'Newer', revision: 8 } })
  editor.discard(); await settle(); expect(editor.draft.value!.name).toBe('Newer'); expect(editor.draft.value!.revision).toBe(8)
})
