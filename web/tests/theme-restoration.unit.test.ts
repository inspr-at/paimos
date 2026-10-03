// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import type { ActiveTheme, ThemeRecord } from '../src/lib/themes'

const mocks = vi.hoisted(() => ({ session: { identity: null as unknown } }))
vi.mock('../src/stores/session', () => ({ useSession: () => mocks.session }))
vi.mock('../src/lib/authz', () => ({ can: () => true }))
vi.mock('../src/lib/themes', () => ({ listThemes: vi.fn(), getActiveTheme: vi.fn() }))

const ME = 'tenant/person'
const theme = (avatar: 'robot-1' | 'sprite', revision: number): ThemeRecord => ({
  id: 'personal', tenant_id: 'tenant', name: 'Personal', scope: 'personal', owner_principal_id: 'person', revision, created_at: '', updated_at: '',
  values: { primary: { light: '#0e6f6c', dark: null }, secondary: { light: '#d69b31', dark: null }, recurring_marker: { source: 'secondary', custom: null },
    agents: { avatar, palette: 'standard', ring: null, hover: false, size: null } },
})
const active = (theme: ThemeRecord): ActiveTheme => ({ theme, default_theme_id: 'default', selected_theme_id: theme.id, revision: 7, fallback_notice: null })
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(yes => { resolve = yes })
  return { promise, resolve }
}
// Drain the known Promise.all → execute → dispatch chain, including answers
// whose token was superseded (idle() follows only the current operation).
async function drainAnswer() { for (let i = 0; i < 8; i++) await Promise.resolve() }
beforeEach(() => { vi.resetModules(); vi.resetAllMocks(); mocks.session = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } } }) })
afterEach(() => vi.unstubAllGlobals())

it.each(['restoration first', 'editor first'])('a newer restoration owns runtime and editor in both response orders: %s', async order => {
  const api = await import('../src/lib/themes')
  const runtime = await import('../src/lib/agentTheme')
  const { useThemeEditor } = await import('../src/stores/themeEditor')
  const older = deferred<ActiveTheme>(), newer = deferred<ActiveTheme>(), requested = deferred<void>()
  const oldTheme = theme('robot-1', 1), newTheme = theme('sprite', 2)
  vi.mocked(api.listThemes).mockResolvedValue({ items: [oldTheme], next_cursor: null })
  vi.mocked(api.getActiveTheme).mockReturnValueOnce(older.promise).mockImplementationOnce(() => { requested.resolve(); return newer.promise })
  // Baseline restoration reads directly through fetch; the fixed path uses
  // the shared editor API. The same barrier and snapshots exercise either.
  vi.stubGlobal('fetch', vi.fn(async () => { requested.resolve(); return new Response(JSON.stringify(await newer.promise)) }))
  runtime.resetAgentTheme(ME)
  const editor = useThemeEditor()
  expect(api.getActiveTheme).toHaveBeenCalledTimes(1)
  const restoring = runtime.restoreAgentTheme(ME)
  await requested.promise
  const olderFinished = order === 'editor first'
  if (olderFinished) { older.resolve(active(oldTheme)); await drainAnswer() }
  else { newer.resolve(active(newTheme)); await restoring }
  if (olderFinished) { newer.resolve(active(newTheme)); await restoring }
  else { older.resolve(active(oldTheme)); await drainAnswer() }
  await editor.idle()
  expect(runtime.agentTheme.value?.avatar).toBe('sprite')
  expect(editor.active.value).toEqual(active(newTheme))
  expect(editor.draft.value).toEqual(newTheme)
  expect(editor.items.value).toEqual([newTheme])
  expect(runtime.agentTheme.value).toEqual(editor.active.value!.theme.values.agents)
})
