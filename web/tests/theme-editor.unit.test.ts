// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { effectScope, nextTick, reactive } from 'vue'
import type { ActiveTheme, ThemeRecord } from '../src/lib/themes'
import { agentTheme, resetAgentTheme } from '../src/lib/agentTheme'
import { useThemeEditor } from '../src/stores/themeEditor'
import * as themes from '../src/lib/themes'

const context = vi.hoisted(() => ({ session: {} as { identity: { tenant: { id: string }; principal: { id: string; kind: string } } | null } }))
vi.mock('../src/stores/session', () => ({ useSession: () => context.session }))
vi.mock('../src/lib/authz', () => ({ can: () => true }))
vi.mock('../src/lib/themes', () => ({ listThemes: vi.fn(), getActiveTheme: vi.fn(), updateTheme: vi.fn(), selectTheme: vi.fn(), duplicateTheme: vi.fn() }))
const record = (id = 'theme-a', avatar = 'robot-1', revision = 1): ThemeRecord => ({
  id, tenant_id: 'tenant', name: id, scope: 'personal', owner_principal_id: 'alice', revision, created_at: '', updated_at: '',
  values: { primary: { light: '#123456', dark: null }, secondary: { light: '#654321', dark: null }, recurring_marker: { source: 'primary', custom: null }, agents: { avatar: avatar as ThemeRecord['values']['agents']['avatar'], ring: null, hover: false, size: null, palette: 'standard' } },
})
const active = (theme = record(), revision = 7): ActiveTheme => ({ theme, revision, default_theme_id: 'default', selected_theme_id: theme.id, fallback_notice: null })
const flush = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); await nextTick() }
beforeEach(() => {
  vi.resetAllMocks()
  context.session = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'alice', kind: 'person' } } })
  resetAgentTheme('tenant/alice')
  vi.mocked(themes.listThemes).mockResolvedValue({ items: [record()], next_cursor: null })
  vi.mocked(themes.getActiveTheme).mockResolvedValue(active())
})
async function pendingSave() {
  const scope = effectScope()
  const editor = scope.run(() => useThemeEditor())!
  await flush()
  expect(editor.busy.value).toBe(false)
  expect(agentTheme.value?.avatar).toBe('robot-1')
  editor.draft.value!.values.agents.avatar = 'sprite'
  let release!: (theme: ThemeRecord) => void
  vi.mocked(themes.updateTheme).mockImplementation(() => new Promise(resolve => { release = resolve }))
  const saving = editor.save()
  expect(themes.updateTheme).toHaveBeenCalledOnce()
  scope.stop()
  return { editor, saving, release }
}
it('reconciles a successful save after the settings scope unmounts', async () => {
  const { editor, saving, release } = await pendingSave()
  release(record('theme-a', 'sprite', 2)); await saving
  expect(agentTheme.value?.avatar).toBe('sprite')
  // Disposed editor state stays untouched; only the shared appearance updates.
  expect(editor.active.value!.theme.revision).toBe(1)
})
it('ignores a late save after identity changes while settings is unmounted', async () => {
  const { saving, release } = await pendingSave()
  context.session.identity = { tenant: { id: 'tenant' }, principal: { id: 'bob', kind: 'person' } }
  resetAgentTheme('tenant/bob')
  release(record('theme-a', 'sprite', 2)); await saving
  expect(agentTheme.value).toBeNull()
})
it('ignores an old save after another editor selects a different theme', async () => {
  const { saving, release } = await pendingSave()
  vi.mocked(themes.getActiveTheme).mockResolvedValue(active(record('theme-b', 'quill'), 8))
  const next = effectScope(); next.run(() => useThemeEditor()); await flush()
  expect(agentTheme.value?.avatar).toBe('quill')
  release(record('theme-a', 'sprite', 2)); await saving
  expect(agentTheme.value?.avatar).toBe('quill')
  next.stop()
})
it('ignores an old save after selecting away and back to the same theme', async () => {
  const { saving, release } = await pendingSave()
  vi.mocked(themes.getActiveTheme).mockResolvedValue(active(record(), 9))
  const next = effectScope(); next.run(() => useThemeEditor()); await flush()
  release(record('theme-a', 'sprite', 2)); await saving
  expect(agentTheme.value?.avatar).toBe('robot-1')
  next.stop()
})
it('ignores an old save after a newer revision of the same selection loads', async () => {
  const { saving, release } = await pendingSave()
  vi.mocked(themes.getActiveTheme).mockResolvedValue(active(record('theme-a', 'quill', 3)))
  const next = effectScope(); next.run(() => useThemeEditor()); await flush()
  release(record('theme-a', 'sprite', 2)); await saving
  expect(agentTheme.value?.avatar).toBe('quill')
  next.stop()
})
it('keeps a new session neutral if the same person signs back in before an old save returns', async () => {
  const { saving, release } = await pendingSave()
  resetAgentTheme(); resetAgentTheme('tenant/alice')
  release(record('theme-a', 'sprite', 2)); await saving
  expect(agentTheme.value).toBeNull()
})

it('reconciles the same selection after settings remounts with its old revision', async () => {
  const { saving, release } = await pendingSave()
  const next = effectScope(); next.run(() => useThemeEditor()); await flush()
  release(record('theme-a', 'sprite', 2)); await saving
  expect(agentTheme.value?.avatar).toBe('sprite')
  next.stop()
})
it('an old active-theme read completing after Save cannot undo reconciliation', async () => {
  const { saving, release } = await pendingSave()
  let read!: (value: ActiveTheme) => void
  vi.mocked(themes.getActiveTheme).mockImplementation(() => new Promise(resolve => { read = resolve }))
  const next = effectScope(); next.run(() => useThemeEditor())
  release(record('theme-a', 'sprite', 2)); await saving
  expect(agentTheme.value?.avatar).toBe('sprite')
  read(active()); await flush()
  expect(agentTheme.value?.avatar).toBe('sprite')
  next.stop()
})

async function pendingSelection(duplicate = false, dispose = true) {
  const scope = effectScope()
  const editor = scope.run(() => useThemeEditor())!
  await flush()
  const target = record('theme-b', 'sprite')
  vi.mocked(themes.duplicateTheme).mockResolvedValue(target)
  let release!: (value: ActiveTheme) => void
  vi.mocked(themes.selectTheme).mockImplementation(() => new Promise(resolve => { release = resolve }))
  const selecting = duplicate ? editor.duplicate(record()) : editor.choose(target)
  await flush()
  expect(themes.selectTheme).toHaveBeenCalledWith('theme-b', 7)
  if (dispose) scope.stop()
  return { editor, selecting, release, target, scope }
}
for (const duplicate of [false, true]) {
  const operation = duplicate ? 'duplicate-and-select' : 'selection'
  it(`reconciles committed ${operation} after navigation`, async () => {
    const { editor, selecting, release, target } = await pendingSelection(duplicate)
    release(active(target, 8)); await selecting
    expect(agentTheme.value?.avatar).toBe('sprite')
    expect(editor.active.value!.theme.id).toBe('theme-a')
  })
  it(`ignores ${operation} from an earlier identity session`, async () => {
    const { selecting, release, target } = await pendingSelection(duplicate)
    resetAgentTheme(); resetAgentTheme('tenant/alice')
    release(active(target, 8)); await selecting
    expect(agentTheme.value).toBeNull()
  })
  it(`a mounted editor cannot bypass ${operation} identity-session guards`, async () => {
    const { editor, selecting, release, target, scope } = await pendingSelection(duplicate, false)
    resetAgentTheme(); resetAgentTheme('tenant/alice')
    release(active(target, 8)); await selecting
    expect(agentTheme.value).toBeNull()
    expect(editor.active.value!.theme.id).toBe('theme-a')
    scope.stop()
  })
  it(`ignores ${operation} after a newer selection loads`, async () => {
    const { selecting, release, target } = await pendingSelection(duplicate)
    vi.mocked(themes.getActiveTheme).mockResolvedValue(active(record('theme-c', 'quill'), 9))
    const next = effectScope(); next.run(() => useThemeEditor()); await flush()
    release(active(target, 8)); await selecting
    expect(agentTheme.value?.avatar).toBe('quill')
    next.stop()
  })
  it(`ignores ${operation} after selecting away and back`, async () => {
    const { selecting, release, target } = await pendingSelection(duplicate)
    vi.mocked(themes.getActiveTheme).mockResolvedValue(active(record(), 9))
    const next = effectScope(); next.run(() => useThemeEditor()); await flush()
    release(active(target, 8)); await selecting
    expect(agentTheme.value?.avatar).toBe('robot-1')
    next.stop()
  })
  it(`${operation} cannot overwrite a newer revision of its committed result`, async () => {
    const { selecting, release, target } = await pendingSelection(duplicate)
    vi.mocked(themes.getActiveTheme).mockResolvedValue(active(record('theme-b', 'quill', 3), 8))
    const next = effectScope(); next.run(() => useThemeEditor()); await flush()
    release(active(target, 8)); await selecting
    expect(agentTheme.value?.avatar).toBe('quill')
    next.stop()
  })
  it(`an old active-theme read cannot undo committed ${operation}`, async () => {
    const { selecting, release, target } = await pendingSelection(duplicate)
    let read!: (value: ActiveTheme) => void
    vi.mocked(themes.getActiveTheme).mockImplementation(() => new Promise(resolve => { read = resolve }))
    const next = effectScope(); next.run(() => useThemeEditor())
    release(active(target, 8)); await selecting
    expect(agentTheme.value?.avatar).toBe('sprite')
    read(active()); await flush()
    expect(agentTheme.value?.avatar).toBe('sprite')
    next.stop()
  })
}
