// SPDX-License-Identifier: AGPL-3.0-only
// P09: exercise the mounted tab reader with one clock, without opening a browser
// for timer/owner permutations. Canvas rendering is covered in desk-tab.test.ts.
import { afterEach, expect, it, vi } from 'vitest'
import { effectScope, reactive, type EffectScope } from 'vue'
import { DESK_TAB_STALE_MS, useDeskTab } from '../src/lib/useDeskTab'

const output = vi.hoisted(() => ({ prefix: vi.fn(), notice: vi.fn() }))
vi.mock('../src/lib/brand', () => ({ setTitlePrefix: output.prefix, setTitleNotice: output.notice }))
vi.mock('../src/lib/theme', async () => ({ dark: (await import('vue')).ref(false) }))
vi.mock('../src/stores/session', () => ({ useSession: () => session }))
vi.mock('../src/stores/decisionDesk', () => ({ useDecisionDesk: () => desk }))

const person = (id: string, kind = 'person') => ({ tenant: { id: 'tenant' }, principal: { id, kind } })
const session = reactive({ identity: person('A') as ReturnType<typeof person> | null })
const refresh = vi.fn(async () => {})
const desk = reactive({ count: 3 as number | null, error: '', projection: null as object | null, refreshPastCancel: refresh })
let scope: EffectScope | undefined
afterEach(() => { scope?.stop(); scope = undefined; vi.useRealTimers(); vi.unstubAllGlobals(); vi.clearAllMocks() })

function start() {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-10-01T10:00:00Z'))
  session.identity = person('A'); desk.count = 3; desk.error = ''; desk.projection = null
  const document = Object.assign(new EventTarget(), {
    visibilityState: 'hidden', querySelector: () => null,
    createElement: () => ({ getContext: () => null }),
  })
  const focus = vi.fn(), sound = vi.fn()
  vi.stubGlobal('document', document)
  vi.stubGlobal('window', { setTimeout, clearTimeout, setInterval, clearInterval, focus, AudioContext: sound })
  vi.stubGlobal('matchMedia', () => ({ matches: false }))
  scope = effectScope(); scope.run(useDeskTab)
  return { document, focus, sound }
}

it('refreshes a stale background badge through the shared reader, resets its age on a projection, and disposes the poll', async () => {
  const { focus, sound } = start()
  expect(output.prefix).toHaveBeenLastCalledWith('(3) ')
  await vi.advanceTimersByTimeAsync(DESK_TAB_STALE_MS - 1)
  expect(refresh).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(1)
  expect(refresh).toHaveBeenCalledTimes(1)
  desk.projection = {}
  await vi.advanceTimersByTimeAsync(DESK_TAB_STALE_MS - 1)
  expect(refresh).toHaveBeenCalledTimes(1)
  await vi.advanceTimersByTimeAsync(1)
  expect(refresh).toHaveBeenCalledTimes(2)
  expect(focus).not.toHaveBeenCalled(); expect(sound).not.toHaveBeenCalled()
  scope!.stop()
  expect(vi.getTimerCount()).toBe(0)
  await vi.advanceTimersByTimeAsync(DESK_TAB_STALE_MS * 2)
  expect(refresh).toHaveBeenCalledTimes(2)
  expect(output.prefix).toHaveBeenLastCalledWith('')
})

it('drops a pending background notice on person, agent and sign-out changes and never polls for an agent or visitor', async () => {
  start()
  desk.count = 4
  expect(output.notice).toHaveBeenLastCalledWith('New decision')
  session.identity = person('B')
  expect(output.notice).toHaveBeenLastCalledWith('')
  expect(output.prefix).toHaveBeenLastCalledWith('(4) ')
  session.identity = person('agent', 'agent')
  expect(output.prefix).toHaveBeenLastCalledWith('')
  await vi.advanceTimersByTimeAsync(DESK_TAB_STALE_MS * 2)
  expect(refresh).not.toHaveBeenCalled()
  session.identity = null
  await vi.advanceTimersByTimeAsync(DESK_TAB_STALE_MS * 2)
  expect(refresh).not.toHaveBeenCalled()
  expect(output.notice).toHaveBeenLastCalledWith('')
})
