import { createApp, h, nextTick, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'

import { usePaimos6CommandPalette } from './usePaimos6CommandPalette'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

const defaultSettings = {
  schema_version: 1 as const, default_shortcut: 'Mod+KeyK', instance_shortcut: null,
  user_shortcut: null, effective_shortcut: 'Mod+KeyK', source: 'default' as const,
}
const emptySearch = { schema_version: 1 as const, query: 'amy', sessions: [], nodes: [], knowledge: [] }

describe('v6 command coordinator authority fencing (PAI-866)', () => {
  it('clears settings/results synchronously and ignores stale project or principal work', async () => {
    const firstSettings = deferred<typeof defaultSettings>()
    const secondSettings = deferred<typeof defaultSettings>()
    const pendingSearch = deferred<typeof emptySearch>()
    const loadSettings = vi.fn().mockReturnValueOnce(firstSettings.promise).mockReturnValueOnce(secondSettings.promise)
    const loadSearch = vi.fn().mockReturnValue(pendingSearch.promise)
    const principalId = ref<number | null>(1)
    const authorityKey = ref('instance-a:user-1:permission-1')
    const projectId = ref<number | null>(42)
    const routeKey = ref('/dev/paimos-6?project=42')
    let subject!: ReturnType<typeof usePaimos6CommandPalette>
    const root = document.createElement('div')
    const app = createApp({ setup() {
      subject = usePaimos6CommandPalette({ principalId, authorityKey, projectId, routeKey, loadSettings, loadSearch })
      return () => h('div')
    } })
    app.mount(root)

    authorityKey.value = 'instance-b:user-2:permission-2'
    expect(subject.settings.value).toBeNull()
    firstSettings.resolve(defaultSettings)
    await nextTick()
    expect(subject.settings.value).toBeNull()
    secondSettings.resolve(defaultSettings)
    await nextTick()
    expect(subject.effectiveShortcut.value).toBe('Mod+KeyK')

    subject.show()
    subject.query.value = 'amy'
    expect(subject.searchState.value).toBe('loading')
    projectId.value = 99
    expect(subject.open.value).toBe(false)
    expect(subject.search.value).toBeNull()
    pendingSearch.resolve(emptySearch)
    await nextTick()
    expect(subject.search.value).toBeNull()

    principalId.value = null
    authorityKey.value = 'logged-out'
    expect(subject.open.value).toBe(false)
    expect(subject.settings.value).toBeNull()
    app.unmount()
  })

  it('opens only on the effective code chord outside editable targets', async () => {
    const principalId = ref<number | null>(1)
    let subject!: ReturnType<typeof usePaimos6CommandPalette>
    const root = document.createElement('div')
    const app = createApp({ setup() {
      subject = usePaimos6CommandPalette({
        principalId, authorityKey: ref('a'), projectId: ref(1), routeKey: ref('/v6'),
        loadSettings: vi.fn().mockResolvedValue(defaultSettings), loadSearch: vi.fn(),
      })
      return () => h('input', { id: 'editable' })
    } })
    document.body.appendChild(root)
    app.mount(root)
    await nextTick()
    window.dispatchEvent(new KeyboardEvent('keydown', { code: 'KeyK', key: 'κ', ctrlKey: true, bubbles: true }))
    expect(subject.open.value).toBe(true)
    subject.close()
    root.querySelector('input')!.dispatchEvent(new KeyboardEvent('keydown', { code: 'KeyK', ctrlKey: true, bubbles: true }))
    expect(subject.open.value).toBe(false)
    app.unmount()
    root.remove()
  })
})
