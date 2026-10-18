// SPDX-License-Identifier: AGPL-3.0-only
// AEON-556: a cold history open must await the person's developer preference
// before counting new versions, highlighting them or saving the last visit.
import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { ref } from 'vue'
import { readPreference, usePreference } from '../src/lib/preferences'
import { getReleases, type ReleaseHistory } from '../src/lib/releases'
import { useReleases } from '../src/stores/releases'
import { releaseHistory } from './releases-fixtures'

const fixture = vi.hoisted(() => ({ scope: 0, current: '' }))
vi.mock('../src/stores/session', () => ({ useSession: () => ({ identity: { tenant: { id: `workspace-${fixture.scope}` }, principal: { id: 'person', kind: 'person' } } }) }))
vi.mock('../src/stores/version', () => ({ useVersion: () => ({ value: { version: fixture.current }, load: () => Promise.resolve() }) }))
vi.mock('../src/lib/preferences', () => ({ readPreference: vi.fn(), usePreference: vi.fn(), writePreference: vi.fn() }))
vi.mock('../src/lib/releases', async original => ({ ...await original<typeof import('../src/lib/releases')>(), getReleases: vi.fn() }))

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  fixture.scope++
})

for (const choice of [true, false, null]) {
  it(`waits for a delayed developer-ui read before counting and marking seen (${choice})`, async () => {
    const history = releaseHistory(Date.parse('2026-10-02T10:00:00Z')) as ReleaseHistory
    fixture.current = history.current
    const lastSeen = history.releases[4]!.version
    const save = vi.fn()
    vi.mocked(usePreference).mockReturnValue({ value: ref({ last_seen: lastSeen }), ready: Promise.resolve(), save })
    let finish!: (value: Record<string, unknown> | null) => void
    vi.mocked(readPreference).mockReturnValue(new Promise(resolve => { finish = resolve }))
    vi.mocked(getReleases).mockResolvedValue(history)
    const store = useReleases()
    await store.start()
    await store.load()
    expect(readPreference).toHaveBeenCalledWith('developer-ui')
    expect(store.newCount).toBeNull()

    let marked = false
    const opening = store.markSeen().then(() => { marked = true })
    await Promise.resolve()
    await Promise.resolve()
    expect(marked).toBe(false)
    expect(save).not.toHaveBeenCalled()
    expect(store.lastSeen).toBe(lastSeen)
    expect(store.highlight.size).toBe(0)

    finish(choice === null ? null : { show_reserved_versions: choice })
    await vi.waitFor(() => expect(store.newCount).toBe(choice ? 4 : 3))
    await opening
    expect(store.highlight.size).toBe(choice ? 4 : 3)
    expect(store.highlight.has(history.releases[2]!.version)).toBe(choice === true)
    expect(save).toHaveBeenCalledExactlyOnceWith({ last_seen: history.current }, 0)
  })
}
