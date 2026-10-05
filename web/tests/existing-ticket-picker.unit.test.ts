// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { APIError } from '../src/lib/api'
import * as membership from '../src/lib/releaseMembership'
import type { Walker } from '../src/lib/journey'
import type { PlanOwner } from '../src/lib/useJourneyData'

function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const scopes: Vue.EffectScope[] = []
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop() })

// Exercise the actual SFC action, with transport and person confirmations mocked.
// No DOM is needed to prove the emitted result of either successful write path.
async function picker(result: membership.MembershipResult, lateMove: boolean) {
  const write = vi.fn().mockResolvedValue(result)
  if (lateMove) write.mockRejectedValueOnce(new APIError(409, 'confirm_move required', {}))
  const confirm = vi.fn().mockResolvedValue(true)
  const confirmOptions = vi.fn().mockResolvedValue(true)
  const owner: PlanOwner = { projectId: 'p', releaseId: 'r', generation: 1 }
  const current = Vue.shallowRef(owner)
  const captureOwner = () => current.value
  const isOwnerCurrent = (candidate: PlanOwner) => candidate === current.value
  const list = vi.fn().mockResolvedValue({ expected_revision: 7, tickets: [option('parent'), option('b')] })
  const modules: Record<string, unknown> = {
    vue: { ...Vue, onMounted: () => {}, onBeforeUnmount: () => {} },
    '../../lib/api': { APIError },
    '../../lib/confirm': { confirmAction: confirm },
    '../../lib/releaseAssign': { confirmOptionMoves: confirmOptions },
    '../../lib/releaseMembership': { ...membership, addReleaseMembership: write, listReleaseTicketOptions: list },
    '../../lib/work': { statusOptions: () => [] },
  }
  const source = readFileSync(new URL('../src/components/journey/ExistingTicketPicker.vue', import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  const { content } = compileScript(descriptor, { id: 'existing-ticket-picker-test' })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: { setup: (props: unknown, context: unknown) => { toggle: (ticket: membership.MembershipTicket) => void; add: () => Promise<void>; load: () => Promise<void>; selected: Vue.Ref<Map<string, membership.MembershipTicket>>; revision: Vue.Ref<number>; loading: Vue.Ref<boolean>; failed: Vue.Ref<string>; note: Vue.Ref<string>; busy: Vue.Ref<boolean> } } } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id.endsWith('.vue')) return { default: {} }
    if (!(id in modules)) throw new Error(`Unexpected dependency ${id}`)
    return modules[id]
  }, exports)
  const scope = Vue.effectScope(); scopes.push(scope)
  const emit = vi.fn()
  const actions = scope.run(() => exports.default!.setup({ projectId: 'p', releaseId: 'r', releaseTitle: 'Next release', epics: [], captureOwner, isOwnerCurrent }, { expose: () => {}, emit }))!
  await actions.load()
  return { ...actions, emit, write, list, confirm, confirmOptions, owner, currentOwner: () => current.value, refresh: () => { current.value = { ...owner, generation: current.value.generation + 1 } } }
}

const option = (id: string): membership.MembershipTicket => ({
  ticket_node_id: id, key: id, title: id, status: 'open', type: 'work',
  feature_node_id: null, release_node_id: null, release_title: null, availability: 'addable',
})
const walker: Walker = { release_node_id: 'r', project_node_id: 'p', state: 'planning', revision: 2, features: [], tickets: [] }

for (const lateMove of [false, true]) for (const overlap of [false, true]) {
  it(`counts returned leaves for a parent${overlap ? ' plus an overlapping leaf' : ''}${lateMove ? ' after move confirmation' : ''}`, async () => {
    const result = { walker, event_id: 81, leaf_node_ids: ['a', 'b', 'c', 'b'] }
    const subject = await picker(result, lateMove)
    subject.toggle(option('parent'))
    if (overlap) subject.toggle(option('b'))
    await subject.add()
    expect(subject.emit.mock.calls).toEqual([['added', { count: 3, result, owner: subject.owner }]])
    expect(subject.write).toHaveBeenCalledTimes(lateMove ? 2 : 1)
    expect(subject.write.mock.lastCall?.[2]).toMatchObject({ ticket_node_ids: overlap ? ['parent', 'b'] : ['parent'], confirm_move: lateMove })
    expect(subject.confirm).toHaveBeenCalledTimes(lateMove ? 1 : 0)
  })
}

it('does not invent a leaf count when an older server omits the leaf IDs', async () => {
  const result = { walker, event_id: 81 }
  const subject = await picker(result, false)
  subject.toggle(option('parent'))
  await subject.add()
  expect(subject.emit.mock.calls).toEqual([['added', { count: null, result, owner: subject.owner }]])
})

for (const lateMove of [false, true]) {
  it(`discards a retained picker success after same-release refresh${lateMove ? ' on the move retry' : ''}`, async () => {
    const result = { walker, event_id: 81, leaf_node_ids: ['leaf'] }
    const subject = await picker(result, lateMove), answer = deferred<membership.MembershipResult>(), started = deferred<void>()
    if (lateMove) subject.write.mockImplementationOnce(() => { started.resolve(); return answer.promise })
    else subject.write.mockReset().mockImplementation(() => { started.resolve(); return answer.promise })
    subject.toggle(option('parent'))
    const pending = subject.add()
    await started.promise
    subject.refresh()
    answer.resolve(result)
    await pending
    expect(subject.emit).not.toHaveBeenCalled()
    expect(subject.write).toHaveBeenCalledTimes(lateMove ? 2 : 1)
  })
}

it('does not dispatch membership after its initial confirmation owner is refreshed', async () => {
  const subject = await picker({ walker, event_id: 81 }, false), confirmation = deferred<boolean>()
  subject.confirmOptions.mockReturnValueOnce(confirmation.promise)
  subject.toggle(option('parent'))
  const pending = subject.add()
  expect(subject.confirmOptions).toHaveBeenCalledTimes(1)
  subject.refresh()
  confirmation.resolve(true)
  await pending
  expect(subject.write).not.toHaveBeenCalled()
  expect(subject.emit).not.toHaveBeenCalled()
})

it('does not retry membership after its move confirmation owner is refreshed', async () => {
  const subject = await picker({ walker, event_id: 81 }, true), confirmation = deferred<boolean>(), started = deferred<void>()
  subject.confirm.mockImplementationOnce(() => { started.resolve(); return confirmation.promise })
  subject.toggle(option('parent'))
  const pending = subject.add()
  await started.promise
  subject.refresh()
  confirmation.resolve(true)
  await pending
  expect(subject.write).toHaveBeenCalledTimes(1)
  expect(subject.emit).not.toHaveBeenCalled()
})

it('discards stale picker failure feedback after a same-release refresh', async () => {
  const subject = await picker({ walker, event_id: 81 }, false), answer = deferred<membership.MembershipResult>(), started = deferred<void>()
  subject.write.mockReset().mockImplementation(() => { started.resolve(); return answer.promise })
  subject.toggle(option('parent'))
  const pending = subject.add()
  await started.promise
  subject.refresh()
  answer.reject(new APIError(409, 'Old release revision changed'))
  await pending
  expect(subject.note.value).toBe('')
  expect(subject.busy.value).toBe(false)
  expect(subject.emit).not.toHaveBeenCalled()
})

it('reports a current picker failure without emitting success', async () => {
  const subject = await picker({ walker, event_id: 81 }, false)
  subject.write.mockRejectedValueOnce(new Error('Transport refused'))
  subject.toggle(option('parent'))
  await subject.add()
  expect(subject.note.value).toBe('Transport refused')
  expect(subject.busy.value).toBe(false)
  expect(subject.emit).not.toHaveBeenCalled()
})

it('keeps a picker action busy while its person confirmation is pending', async () => {
  const subject = await picker({ walker, event_id: 81 }, false), confirmation = deferred<boolean>()
  subject.confirmOptions.mockReturnValueOnce(confirmation.promise)
  subject.toggle(option('parent'))
  const pending = subject.add()
  await subject.add()
  expect(subject.confirmOptions).toHaveBeenCalledTimes(1)
  expect(subject.write).not.toHaveBeenCalled()
  confirmation.resolve(true)
  await pending
  expect(subject.write).toHaveBeenCalledTimes(1)
  expect(subject.busy.value).toBe(false)
})

it('refreshes a retained picker revision and submits its retained selection successfully', async () => {
  const result = { walker: { ...walker, revision: 21 }, event_id: 81, leaf_node_ids: ['parent'] }
  const subject = await picker(result, false)
  subject.toggle(option('parent'))
  subject.list.mockResolvedValueOnce({ expected_revision: 20, tickets: [option('parent')] })
  subject.refresh()
  await Vue.nextTick()
  await subject.add()
  expect(subject.write.mock.calls).toEqual([['p', 'r', { expected_revision: 20, ticket_node_ids: ['parent'], confirm_move: false }]])
  expect(subject.emit.mock.calls).toEqual([['added', { count: 1, result, owner: subject.currentOwner() }]])
})

it('reconciles retained selections with refreshed availability and move details', async () => {
  const subject = await picker({ walker, event_id: 81 }, false)
  subject.toggle(option('parent'))
  subject.toggle(option('b'))
  const moved = { ...option('parent'), title: 'Fresh title', availability: 'other_release' as const, release_node_id: 'other', release_title: 'Other release' }
  subject.list.mockResolvedValueOnce({ expected_revision: 20, tickets: [moved, { ...option('b'), availability: 'included' }] })
  subject.refresh()
  await Vue.nextTick()
  expect([...subject.selected.value.values()]).toEqual([moved])
  await subject.add()
  expect(subject.confirmOptions).toHaveBeenCalledWith([moved], 'Next release')
  expect(subject.write.mock.lastCall?.[2]).toEqual({ expected_revision: 20, ticket_node_ids: ['parent'], confirm_move: true })
})

it('blocks a retained picker write until its refreshed options arrive', async () => {
  const subject = await picker({ walker, event_id: 81 }, false), answer = deferred<membership.TicketOptions>()
  subject.toggle(option('parent'))
  subject.list.mockReturnValueOnce(answer.promise)
  subject.refresh()
  await Vue.nextTick()
  await subject.add()
  expect(subject.write).not.toHaveBeenCalled()
  expect(subject.selected.value.has('parent')).toBe(true)
  answer.resolve({ expected_revision: 20, tickets: [option('parent')] })
  await Vue.nextTick()
  await subject.add()
  expect(subject.write.mock.lastCall?.[2].expected_revision).toBe(20)
})

it('ignores an older options response after a retained picker owner refresh', async () => {
  const subject = await picker({ walker, event_id: 81 }, false), old = deferred<membership.TicketOptions>()
  subject.toggle(option('parent'))
  subject.list.mockReturnValueOnce(old.promise)
  const pending = subject.load()
  subject.list.mockResolvedValueOnce({ expected_revision: 20, tickets: [option('parent')] })
  subject.refresh()
  await Vue.nextTick()
  old.resolve({ expected_revision: 7, tickets: [{ ...option('parent'), availability: 'closed' }] })
  await pending
  expect(subject.revision.value).toBe(20)
  expect(subject.selected.value.has('parent')).toBe(true)
  await subject.add()
  expect(subject.write.mock.lastCall?.[2].expected_revision).toBe(20)
})

for (const lateMove of [false, true]) it(`reloads a revision conflict for a successful person retry${lateMove ? ' after move confirmation' : ''}`, async () => {
  const result = { walker: { ...walker, revision: 21 }, event_id: 81, leaf_node_ids: ['parent'] }
  const subject = await picker(result, lateMove)
  subject.write.mockRejectedValueOnce(new APIError(409, 'Release revision changed'))
  subject.toggle(option('parent'))
  subject.list.mockResolvedValueOnce({ expected_revision: 20, tickets: [option('parent')] })
  await subject.add()
  expect(subject.emit).not.toHaveBeenCalled()
  expect(subject.selected.value.has('parent')).toBe(true)
  expect(subject.note.value).toMatch(/revision|release changed/i)
  await subject.add()
  expect(subject.write.mock.calls.map(call => call[2].expected_revision)).toEqual(lateMove ? [7, 7, 20] : [7, 20])
  expect(subject.emit.mock.calls).toEqual([['added', { count: 1, result, owner: subject.owner }]])
})

it('keeps failed conflict recovery honest and blocks another stale write', async () => {
  const subject = await picker({ walker, event_id: 81 }, false)
  subject.toggle(option('parent'))
  subject.write.mockRejectedValueOnce(new APIError(409, 'Release revision changed'))
  subject.list.mockRejectedValueOnce(new Error('Options refresh refused'))
  await subject.add()
  expect(subject.failed.value).toBe('Options refresh refused')
  expect(subject.selected.value.has('parent')).toBe(true)
  expect(subject.emit).not.toHaveBeenCalled()
  await subject.add()
  expect(subject.write).toHaveBeenCalledTimes(1)
  subject.list.mockResolvedValueOnce({ expected_revision: 20, tickets: [option('parent')] })
  await subject.load()
  await subject.add()
  expect(subject.write.mock.lastCall?.[2].expected_revision).toBe(20)
  expect(subject.emit).toHaveBeenCalledTimes(1)
})
