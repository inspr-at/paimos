// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { APIError } from '../src/lib/api'
import * as membership from '../src/lib/releaseMembership'
import type { Walker } from '../src/lib/journey'

const scopes: Vue.EffectScope[] = []
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop() })

// Exercise the actual SFC action, with transport and person confirmations mocked.
// No DOM is needed to prove the emitted result of either successful write path.
function picker(result: membership.MembershipResult, lateMove: boolean) {
  const write = vi.fn().mockResolvedValue(result)
  if (lateMove) write.mockRejectedValueOnce(new APIError(409, 'confirm_move required', {}))
  const confirm = vi.fn().mockResolvedValue(true)
  const modules: Record<string, unknown> = {
    vue: { ...Vue, onMounted: () => {}, onBeforeUnmount: () => {} },
    '../../lib/api': { APIError },
    '../../lib/confirm': { confirmAction: confirm },
    '../../lib/releaseAssign': { confirmOptionMoves: vi.fn().mockResolvedValue(true) },
    '../../lib/releaseMembership': { ...membership, addReleaseMembership: write },
    '../../lib/work': { statusOptions: () => [] },
  }
  const source = readFileSync(new URL('../src/components/journey/ExistingTicketPicker.vue', import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  const { content } = compileScript(descriptor, { id: 'existing-ticket-picker-test' })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: { setup: (props: unknown, context: unknown) => { toggle: (ticket: membership.MembershipTicket) => void; add: () => Promise<void> } } } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id.endsWith('.vue')) return { default: {} }
    if (!(id in modules)) throw new Error(`Unexpected dependency ${id}`)
    return modules[id]
  }, exports)
  const scope = Vue.effectScope(); scopes.push(scope)
  const emit = vi.fn()
  const actions = scope.run(() => exports.default!.setup({ projectId: 'p', releaseId: 'r', releaseTitle: 'Next release', epics: [] }, { expose: () => {}, emit }))!
  return { ...actions, emit, write, confirm }
}

const option = (id: string): membership.MembershipTicket => ({
  ticket_node_id: id, key: id, title: id, status: 'open', type: 'work',
  feature_node_id: null, release_node_id: null, release_title: null, availability: 'addable',
})
const walker: Walker = { release_node_id: 'r', project_node_id: 'p', state: 'planning', revision: 2, features: [], tickets: [] }

for (const lateMove of [false, true]) for (const overlap of [false, true]) {
  it(`counts returned leaves for a parent${overlap ? ' plus an overlapping leaf' : ''}${lateMove ? ' after move confirmation' : ''}`, async () => {
    const result = { walker, event_id: 81, leaf_node_ids: ['a', 'b', 'c', 'b'] }
    const subject = picker(result, lateMove)
    subject.toggle(option('parent'))
    if (overlap) subject.toggle(option('b'))
    await subject.add()
    expect(subject.emit.mock.calls).toEqual([['added', { count: 3, result }]])
    expect(subject.write).toHaveBeenCalledTimes(lateMove ? 2 : 1)
    expect(subject.write.mock.lastCall?.[2]).toMatchObject({ ticket_node_ids: overlap ? ['parent', 'b'] : ['parent'], confirm_move: lateMove })
    expect(subject.confirm).toHaveBeenCalledTimes(lateMove ? 1 : 0)
  })
}

it('does not invent a leaf count when an older server omits the leaf IDs', async () => {
  const result = { walker, event_id: 81 }
  const subject = picker(result, false)
  subject.toggle(option('parent'))
  await subject.add()
  expect(subject.emit.mock.calls).toEqual([['added', { count: null, result }]])
})
