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
