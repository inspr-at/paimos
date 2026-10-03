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
vi.mock('../src/lib/themes', () => ({ listThemes: vi.fn(), getActiveTheme: vi.fn(), getTheme: vi.fn(), updateTheme: vi.fn(), selectTheme: vi.fn(), duplicateTheme: vi.fn(), deleteTheme: vi.fn() }))
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

// The same lifecycle/interleaving matrix covers every committed editor mutation.
// Deferred responses establish ordering without sleeps or timing assumptions.
function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (failure: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const mutations = ['select', 'save', 'rename', 'delete', 'duplicate'] as const
const interleavings = ['mounted', 'navigation', 'remount-old', 'remount-committed', 'newer-selection', 'away-and-back', 'newer-record', 'different-person', 'same-person-new-session', 'mounted-new-session', 'old-read', 'failure', 'mounted-failure'] as const
it.each(mutations.flatMap(mutation => interleavings.map(interleaving => ({ mutation, interleaving }))))('$mutation reconciles through $interleaving', async ({ mutation, interleaving }) => {
  const scope = effectScope()
  const editor = scope.run(() => useThemeEditor())!
  const others: ReturnType<typeof effectScope>[] = []
  try {
    await flush()
    const initialAppearance = agentTheme.value
    const saved = record('theme-a', mutation === 'rename' ? 'robot-1' : 'sprite', 2)
    if (mutation === 'rename') saved.name = 'Renamed theme'
    const result = mutation === 'delete'
      ? { ...active(record('default', 'orbit'), 7), selected_theme_id: 'theme-a', fallback_notice: { deleted_theme_id: 'theme-a', deleted_theme_name: 'theme-a' } }
      : mutation === 'save' || mutation === 'rename' ? active(saved) : active(record('theme-b', 'sprite'), 8)
    const response = deferred<ThemeRecord | ActiveTheme | void>()
    vi.mocked(themes.updateTheme).mockImplementation(() => response.promise as Promise<ThemeRecord>)
    vi.mocked(themes.selectTheme).mockImplementation(() => response.promise as Promise<ActiveTheme>)
    vi.mocked(themes.deleteTheme).mockImplementation(() => response.promise as Promise<void>)
    vi.mocked(themes.duplicateTheme).mockResolvedValue(result.theme)
    if (mutation === 'save') editor.draft.value!.values.agents.avatar = 'sprite'
    if (mutation === 'rename') editor.draft.value!.name = saved.name
    const pending = mutation === 'select' ? editor.choose(result.theme)
      : mutation === 'duplicate' ? editor.duplicate(record())
      : mutation === 'delete' ? editor.remove(record()) : editor.save()
    await flush()
    expect(mutation === 'delete' ? themes.deleteTheme : mutation === 'save' || mutation === 'rename' ? themes.updateTheme : themes.selectTheme).toHaveBeenCalledOnce()
    if (interleaving !== 'mounted' && interleaving !== 'mounted-new-session' && interleaving !== 'mounted-failure') scope.stop()
    let expected: ThemeRecord['values']['agents']['avatar'] | undefined = result.theme.values.agents.avatar
    const remount = async (value: ActiveTheme) => {
      vi.mocked(themes.getActiveTheme).mockResolvedValue(value)
      const next = effectScope(); others.push(next)
      const replacement = next.run(() => useThemeEditor())!
      await flush()
      expect(replacement.active.value?.theme.id).toBe(value.theme.id)
      return replacement
    }
    if (interleaving === 'remount-old') await remount(active())
    if (interleaving === 'remount-committed') await remount(result)
    if (interleaving === 'newer-selection') { await remount(active(record('theme-c', 'quill'), 9)); expected = 'quill' }
    if (interleaving === 'away-and-back') { await remount(active(record(), 9)); expected = 'robot-1' }
    if (interleaving === 'newer-record') { await remount({ ...result, theme: record(result.theme.id, 'quill', 3) }); expected = 'quill' }
    if (interleaving === 'different-person') { resetAgentTheme('tenant/bob'); expected = undefined }
    if (interleaving === 'same-person-new-session' || interleaving === 'mounted-new-session') { resetAgentTheme(); resetAgentTheme('tenant/alice'); expected = undefined }
    const staleRead = deferred<ActiveTheme>()
    let returningEditor: ReturnType<typeof useThemeEditor> | undefined
    if (interleaving === 'old-read') {
      vi.mocked(themes.getActiveTheme).mockImplementationOnce(() => staleRead.promise)
      const next = effectScope(); others.push(next); returningEditor = next.run(() => useThemeEditor())
      await flush()
    }
    // The delete follow-up must be stale even when a newer selection has loaded.
    vi.mocked(themes.getActiveTheme).mockResolvedValue(result)
    const readsBeforeCommit = vi.mocked(themes.getActiveTheme).mock.calls.length
    if (interleaving === 'failure' || interleaving === 'mounted-failure') { response.reject(new Error('write failed')); expected = 'robot-1' }
    else response.resolve(mutation === 'delete' ? undefined : mutation === 'save' || mutation === 'rename' ? saved : result)
    await pending
    expect(agentTheme.value?.avatar).toBe(expected)
    if (interleaving === 'different-person' || interleaving === 'same-person-new-session' || interleaving === 'mounted-new-session') {
      // A deletion follow-up must not read the newly signed-in person's theme.
      expect(themes.getActiveTheme).toHaveBeenCalledTimes(readsBeforeCommit)
    }
    if (interleaving === 'mounted') {
      expect(agentTheme.value).not.toBe(initialAppearance)
      expect(editor.active.value?.theme).toEqual(result.theme)
      expect(editor.draft.value).toEqual(result.theme)
      expect(editor.error.value).toBe('')
      expect(editor.busy.value).toBe(false)
    } else {
      expect(editor.active.value?.theme.id).toBe('theme-a')
      expect(editor.active.value?.theme.revision).toBe(1)
    }
    if (interleaving === 'old-read') {
      staleRead.resolve(active()); await flush()
      expect(agentTheme.value?.avatar).toBe(expected)
      // The returning editor must display the committed record, too.
      expect(returningEditor!.active.value?.theme).toEqual(result.theme)
      expect(returningEditor!.draft.value).toEqual(result.theme)
    }
    if (interleaving === 'mounted-failure') {
      expect(editor.error.value).toContain('write failed')
      expect(editor.message.value).not.toMatch(/Saved|Deleted/)
      expect(editor.busy.value).toBe(false)
      if (mutation === 'save' || mutation === 'rename') expect(editor.dirty.value).toBe(true)
      if (mutation === 'duplicate') expect(editor.message.value).toBe('Duplicated.')
    }
  } finally { scope.stop(); others.forEach(scope => scope.stop()) }
})

it.each(['navigation', 'identity-change'] as const)('New theme does not start duplication after its source read outlives %s', async lifecycle => {
  vi.mocked(themes.listThemes).mockResolvedValue({ items: [record(), record('default')], next_cursor: null })
  const scope = effectScope(), source = deferred<ThemeRecord>()
  const editor = scope.run(() => useThemeEditor())!
  try {
    await flush()
    vi.mocked(themes.getTheme).mockReturnValue(source.promise)
    const creating = editor.newTheme()
    expect(themes.getTheme).toHaveBeenCalledWith('default')
    if (lifecycle === 'navigation') scope.stop()
    else { context.session.identity = { tenant: { id: 'tenant' }, principal: { id: 'bob', kind: 'person' } }; resetAgentTheme('tenant/bob'); await flush() }
    source.resolve(record('default')); await creating
    expect(themes.duplicateTheme).not.toHaveBeenCalled()
    expect(themes.selectTheme).not.toHaveBeenCalled()
  } finally { scope.stop() }
})

it('does not select a duplicated record when navigation precedes the creation response', async () => {
  const scope = effectScope(), created = deferred<ThemeRecord>()
  const editor = scope.run(() => useThemeEditor())!
  await flush()
  vi.mocked(themes.duplicateTheme).mockReturnValue(created.promise)
  const duplicating = editor.duplicate(record())
  expect(themes.duplicateTheme).toHaveBeenCalledOnce()
  scope.stop(); created.resolve(record('theme-b', 'sprite')); await duplicating
  expect(themes.selectTheme).not.toHaveBeenCalled()
  expect(agentTheme.value?.avatar).toBe('robot-1')
  expect(editor.items.value.map(theme => theme.id)).toEqual(['theme-a'])
})

it('deleting an inactive theme changes only the list and never reads or replaces the selected appearance', async () => {
  const scope = effectScope(), editor = scope.run(() => useThemeEditor())!
  try {
    await flush()
    editor.items.value.push(record('theme-b', 'sprite'))
    vi.mocked(themes.deleteTheme).mockResolvedValue(undefined)
    await editor.remove(record('theme-b', 'sprite'))
    expect(themes.getActiveTheme).toHaveBeenCalledOnce()
    expect(agentTheme.value?.avatar).toBe('robot-1')
    expect(editor.active.value?.theme.id).toBe('theme-a')
    expect(editor.items.value.map(theme => theme.id)).toEqual(['theme-a'])
    expect(editor.message.value).toBe('Deleted.')
  } finally { scope.stop() }
})

it('reports a successful deletion and a failed fallback read honestly', async () => {
  const scope = effectScope(), editor = scope.run(() => useThemeEditor())!
  try {
    await flush()
    vi.mocked(themes.deleteTheme).mockResolvedValue(undefined)
    vi.mocked(themes.getActiveTheme).mockRejectedValue(new Error('read failed'))
    await editor.remove(record())
    expect(themes.deleteTheme).toHaveBeenCalledOnce()
    expect(editor.active.value).toBeNull()
    expect(editor.draft.value).toBeNull()
    expect(editor.items.value).toEqual([])
    expect(editor.message.value).toBe('Deleted.')
    expect(editor.error.value).toBe('Deleted, but the workspace default could not be loaded. Try again.')
    expect(editor.busy.value).toBe(false)
  } finally { scope.stop() }
})

it('ignores a deletion fallback that returns after the identity session changes', async () => {
  const scope = effectScope(), fallback = deferred<ActiveTheme>()
  const editor = scope.run(() => useThemeEditor())!
  try {
    await flush()
    vi.mocked(themes.deleteTheme).mockResolvedValue(undefined)
    vi.mocked(themes.getActiveTheme).mockReturnValue(fallback.promise)
    const deleting = editor.remove(record()); await flush()
    expect(themes.getActiveTheme).toHaveBeenCalledTimes(2)
    scope.stop(); resetAgentTheme('tenant/bob')
    fallback.resolve({ ...active(record('default', 'orbit')), selected_theme_id: 'theme-a', fallback_notice: { deleted_theme_id: 'theme-a', deleted_theme_name: 'theme-a' } })
    await deleting
    expect(agentTheme.value).toBeNull()
  } finally { scope.stop() }
})
