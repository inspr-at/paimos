// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { effectScope, ref, type EffectScope } from 'vue'
import * as Vue from 'vue'
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { APIError } from '../src/lib/api'
import type { Walker } from '../src/lib/journey'
import { useJourneyData } from '../src/lib/useJourneyData'
import { usePlan } from '../src/lib/usePlan'
import { toast } from '../src/lib/toast'

const transport = vi.hoisted(() => ({ getWalker: vi.fn(), putPlan: vi.fn(), addPlanTicket: vi.fn(), listWork: vi.fn() }))
vi.mock('../src/lib/journey', async original => ({ ...(await original<typeof import('../src/lib/journey')>()), ...transport }))
vi.mock('../src/lib/toast', () => ({ toast: vi.fn() }))

function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function walker(project = 'project-a', release = 'release-a', revision = 7): Walker {
  return {
    project_node_id: project, release_node_id: release, revision, state: 'planning',
    features: [{ feature_node_id: 'shared-feature', epic_key: 'F-1', title: 'Feature', selection: 'some', included_count: 1, open_count: 2 }],
    tickets: [0, 1].map(i => ({ ticket_node_id: `${release}-${i}`, key: `T-${i}`, title: `Ticket ${i}`, feature_node_id: 'shared-feature', included: i === 0, position: i, estimated_hours: null, screen_node_ids: [] })),
  }
}
let scope: EffectScope
beforeEach(() => {
  vi.clearAllMocks()
  transport.getWalker.mockReset().mockImplementation(async (project, release) => walker(project, release))
  transport.putPlan.mockReset().mockImplementation(async (project, release, body) => ({ ...walker(project, release, body.expected_revision + 1), tickets: walker(project, release).tickets.map(t => ({ ...t, included: body.included_ticket_ids.includes(t.ticket_node_id) })) }))
  transport.addPlanTicket.mockReset()
  transport.listWork.mockReset().mockResolvedValue([])
  scope = effectScope()
})
const apps: Vue.App[] = []
afterEach(() => { for (const app of apps.splice(0)) app.unmount(); scope.stop(); vi.unstubAllGlobals() })
async function setup() {
  const project = ref<string | null>('project-a'), editable = ref(true), afterSave = vi.fn()
  const data = scope.run(() => useJourneyData(project, ref(null)))!
  const plan = scope.run(() => usePlan(data, editable, afterSave))!
  await data.loadWalker('release-a')
  return { project, editable, afterSave, data, plan }
}
function holdWrite() {
  const started = deferred<void>(), answer = deferred<Walker>()
  transport.putPlan.mockImplementationOnce(() => { started.resolve(); return answer.promise })
  return { started: started.promise, answer }
}

// Execute the real form's setup/watchers without a browser or its visual tree.
// This checks ownership of drafts and feedback; no geometry is claimed here.
function planForm(context: Awaited<ReturnType<typeof setup>>) {
  vi.stubGlobal('window', { matchMedia: () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }) })
  const store = { load: vi.fn() }
  const ctx = { data: context.data, plan: context.plan, editable: context.editable, releaseLabel: ref('Release A'), project: ref({ id: 'project-a' }) }
  const { descriptor } = parse(readFileSync(new URL('../src/components/journey/PlanStage.vue', import.meta.url), 'utf8'))
  const { content } = compileScript(descriptor, { id: 'plan-owner-test' })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: Vue.Component } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id === 'vue') return Vue
    if (id === '../../lib/api') return { APIError }
    if (id === '../../lib/toast') return { toast }
    if (id === '../../lib/journeyContext') return { useJourneyContext: () => ctx }
    if (id === '../../stores/journey') return { useJourney: () => store }
    if (id === '../../lib/journey' || id === '../../lib/work' || id.endsWith('.vue')) return {}
    throw new Error(`Unexpected form dependency: ${id}`)
  }, exports)
  const renderer = Vue.createRenderer<object, object>({
    createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
    setText: () => {}, setElementText: () => {}, parentNode: () => null, nextSibling: () => null,
    patchProp: () => {}, remove: () => {}, insert: () => {},
  })
  const app = renderer.createApp({ ...exports.default, render: () => null })
  const vm = app.mount({})
  apps.push(app)
  const state = vm.$.setupState as Record<string, unknown>
  return { state, submit: state.addTicket as () => Promise<void>, store, ctx }
}

it('drops a queued toggle when the selected release changes before dispatch', async () => {
  const { data, plan, afterSave } = await setup()
  const pending = plan.toggleTicket('release-a-0')
  await data.loadWalker('release-b')
  expect(await pending).toBe(false)
  expect(transport.putPlan).not.toHaveBeenCalled()
  expect(data.walker.value.value).toEqual(walker('project-a', 'release-b'))
  expect(afterSave).not.toHaveBeenCalled()
})

it('drops a queued toggle when its project changes and comes back synchronously', async () => {
  const { project, data, plan } = await setup()
  const pending = plan.toggleTicket('release-a-0')
  project.value = 'project-b'; project.value = 'project-a'
  await data.loadWalker('release-a', true)
  expect(await pending).toBe(false)
  expect(transport.putPlan).not.toHaveBeenCalled()
})

it('rechecks editability immediately before a queued write', async () => {
  const { editable, data, plan } = await setup()
  const before = data.walker.value.value
  const pending = plan.toggleTicket('release-a-0')
  editable.value = false
  expect(await pending).toBe(false)
  expect(transport.putPlan).not.toHaveBeenCalled()
  expect(data.walker.value.value).toEqual(before)
})

it('discards a late plan success without refreshing the newly selected release', async () => {
  const { data, plan, afterSave } = await setup()
  const held = holdWrite(), pending = plan.toggleTicket('release-a-0')
  await held.started
  expect(transport.putPlan).toHaveBeenCalledWith('project-a', 'release-a', { expected_revision: 7, ordered_ticket_ids: ['release-a-0', 'release-a-1'], included_ticket_ids: [] })
  await data.loadWalker('release-b')
  held.answer.resolve(walker('project-a', 'release-a', 8))
  expect(await pending).toBe(false)
  expect(data.walker.value.value).toEqual(walker('project-a', 'release-b'))
  expect(afterSave).not.toHaveBeenCalled()
  expect(plan.saving.value).toBe(false)
})

it('discards a late plan conflict without rollback toast or stale refresh', async () => {
  const { data, plan, afterSave } = await setup()
  const held = holdWrite(), pending = plan.toggleTicket('release-a-0')
  await held.started
  await data.loadWalker('release-b')
  transport.getWalker.mockClear()
  held.answer.reject(new APIError(409, 'Old release changed'))
  expect(await pending).toBe(false)
  expect(data.walker.value.value).toEqual(walker('project-a', 'release-b'))
  expect(transport.getWalker).not.toHaveBeenCalled()
  expect(toast).not.toHaveBeenCalled()
  expect(afterSave).not.toHaveBeenCalled()
})

it('resets partial feature selection memory for a different release owner', async () => {
  const { data, plan } = await setup()
  expect(await plan.toggleFeature('shared-feature')).toBe(true)
  expect(plan.memory.value.get('shared-feature')).toEqual(['release-a-0'])
  await data.loadWalker('release-b')
  expect(plan.memory.value.size).toBe(0)
  expect(await plan.toggleFeature('shared-feature')).toBe(true)
  expect(transport.putPlan).toHaveBeenLastCalledWith('project-a', 'release-b', { expected_revision: 7, ordered_ticket_ids: ['release-b-0', 'release-b-1'], included_ticket_ids: ['release-b-0', 'release-b-1'] })
})

it('serializes same-owner toggles using the preceding saved revision', async () => {
  const { data, plan, afterSave } = await setup()
  const held = holdWrite(), first = plan.toggleTicket('release-a-0')
  await held.started
  const second = plan.toggleTicket('release-a-1')
  held.answer.resolve({ ...walker('project-a', 'release-a', 8), tickets: walker().tickets.map(t => ({ ...t, included: false })) })
  expect(await first).toBe(true)
  expect(await second).toBe(true)
  expect(transport.putPlan).toHaveBeenLastCalledWith('project-a', 'release-a', { expected_revision: 8, ordered_ticket_ids: ['release-a-0', 'release-a-1'], included_ticket_ids: ['release-a-1'] })
  expect(data.walker.value.value?.revision).toBe(9)
  expect(afterSave).toHaveBeenCalledTimes(2)
})

it('a same-release reload invalidates an in-flight write and queued successor', async () => {
  const { data, plan, afterSave } = await setup()
  const held = holdWrite(), first = plan.toggleTicket('release-a-0')
  await held.started
  const second = plan.toggleTicket('release-a-1')
  transport.getWalker.mockResolvedValueOnce(walker('project-a', 'release-a', 20))
  await data.loadWalker('release-a', true)
  held.answer.resolve(walker('project-a', 'release-a', 8))
  expect(await first).toBe(false)
  expect(await second).toBe(false)
  expect(transport.putPlan).toHaveBeenCalledTimes(1)
  expect(data.walker.value.value?.revision).toBe(20)
  expect(afterSave).not.toHaveBeenCalled()
})

it('an old save cannot clear the busy state or block writes for a new owner', async () => {
  const { data, plan } = await setup()
  const old = holdWrite(), first = plan.toggleTicket('release-a-0')
  await old.started
  await data.loadWalker('release-b')
  expect(plan.saving.value).toBe(false)
  const current = holdWrite(), second = plan.toggleTicket('release-b-0')
  await current.started
  expect(plan.saving.value).toBe(true)
  old.answer.reject(new Error('Old transport failed'))
  expect(await first).toBe(false)
  expect(plan.saving.value).toBe(true)
  current.answer.resolve(walker('project-a', 'release-b', 8))
  expect(await second).toBe(true)
  expect(plan.saving.value).toBe(false)
  expect(toast).not.toHaveBeenCalled()
})

it('only the latest walker load can install a result after release A B A navigation', async () => {
  const { data } = await setup()
  const old = deferred<Walker>()
  transport.getWalker.mockReturnValueOnce(old.promise)
  const pending = data.loadWalker('release-a', true)
  await data.loadWalker('release-b')
  transport.getWalker.mockResolvedValueOnce(walker('project-a', 'release-a', 20))
  await data.loadWalker('release-a')
  old.resolve(walker('project-a', 'release-a', 8))
  await pending
  expect(data.walker.value.value?.revision).toBe(20)
})

it('rolls back a current-owner transport failure and surfaces its real error', async () => {
  const { data, plan, afterSave } = await setup()
  transport.putPlan.mockRejectedValueOnce(new Error('Transport refused'))
  expect(await plan.toggleTicket('release-a-0')).toBe(false)
  expect(data.walker.value.value).toEqual(walker())
  expect(toast).toHaveBeenCalledWith('The plan was not saved: Transport refused', { tone: 'error' })
  expect(afterSave).not.toHaveBeenCalled()
})

it('reloads a current-owner conflict from its captured release', async () => {
  const { data, plan } = await setup()
  transport.putPlan.mockRejectedValueOnce(new APIError(409, 'Revision conflict'))
  transport.getWalker.mockResolvedValueOnce(walker('project-a', 'release-a', 20))
  expect(await plan.toggleTicket('release-a-0')).toBe(false)
  expect(transport.getWalker).toHaveBeenLastCalledWith('project-a', 'release-a')
  expect(data.walker.value.value?.revision).toBe(20)
  expect(toast).toHaveBeenCalledWith('The plan changed elsewhere (Revision conflict). The newest plan is shown.', { tone: 'error' })
})

it('drops an explicitly captured ticket creation owner before dispatch after navigation', async () => {
  const { data } = await setup()
  const owner = data.capturePlanOwner()!
  await data.loadWalker('release-b')
  expect(await data.addTicket(owner, 'Old draft', 'shared-feature', true)).toBeNull()
  expect(transport.addPlanTicket).not.toHaveBeenCalled()
})

it('a late ticket creation result cannot replace another project or refresh its work', async () => {
  const { project, data } = await setup()
  const owner = data.capturePlanOwner()!, answer = deferred<Walker>()
  transport.addPlanTicket.mockReturnValueOnce(answer.promise)
  const pending = data.addTicket(owner, 'Captured draft', 'shared-feature', true)
  expect(transport.addPlanTicket).toHaveBeenCalledWith('project-a', 'release-a', { title: 'Captured draft', feature_node_id: 'shared-feature', included: true, expected_revision: 7, idempotency_key: expect.any(String) })
  project.value = 'project-b'
  await data.loadWalker('release-a')
  answer.resolve(walker('project-a', 'release-a', 8))
  expect(await pending).toBeNull()
  expect(data.walker.value.value).toEqual(walker('project-b', 'release-a'))
  expect(transport.listWork).not.toHaveBeenCalled()
})

it('adopts current-owner ticket creation and refreshes work for that project', async () => {
  const { data } = await setup()
  const saved = walker('project-a', 'release-a', 8)
  transport.addPlanTicket.mockResolvedValueOnce(saved)
  expect(await data.addTicket(data.capturePlanOwner()!, 'New draft', null, true)).toEqual(saved)
  expect(data.walker.value.value).toEqual(saved)
  expect(transport.listWork).toHaveBeenCalledWith('project-a')
})

it('discards a plan result after its view scope is disposed', async () => {
  const { data, plan, afterSave } = await setup()
  const held = holdWrite(), pending = plan.toggleTicket('release-a-0')
  await held.started
  const displayed = data.walker.value.value
  scope.stop()
  held.answer.resolve(walker('project-a', 'release-a', 8))
  expect(await pending).toBe(false)
  expect(data.walker.value.value).toBe(displayed)
  expect(afterSave).not.toHaveBeenCalled()
  expect(toast).not.toHaveBeenCalled()
})

it('the ticket form preserves a new release draft and busy state after an old success', async () => {
  const context = await setup(), form = planForm(context)
  const old = deferred<Walker>(), current = deferred<Walker>()
  transport.addPlanTicket.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
  form.state.newTitle = 'Old draft'; form.state.newFeature = 'shared-feature'
  const first = form.submit()
  await context.data.loadWalker('release-b')
  expect(form.state.newTitle).toBe('')
  expect(form.state.newFeature).toBe('')
  form.ctx.releaseLabel.value = 'Release B'
  form.state.newTitle = 'New draft'
  const second = form.submit()
  old.resolve(walker('project-a', 'release-a', 8))
  await first
  expect(form.state.newTitle).toBe('New draft')
  expect(form.state.adding).toBe(true)
  expect(toast).not.toHaveBeenCalled()
  expect(form.store.load).not.toHaveBeenCalled()
  current.resolve(walker('project-a', 'release-b', 8))
  await second
  expect(form.state.newTitle).toBe('')
  expect(form.state.adding).toBe(false)
  expect(toast).toHaveBeenCalledWith('The ticket joins release b.')
  expect(form.store.load).toHaveBeenCalledWith('project-a', true)
})

it('the ticket form discards late failure feedback after release navigation', async () => {
  const context = await setup(), form = planForm(context), answer = deferred<Walker>()
  transport.addPlanTicket.mockReturnValueOnce(answer.promise)
  form.state.newTitle = 'Old draft'
  const pending = form.submit()
  await context.data.loadWalker('release-b')
  form.state.newTitle = 'New draft'
  answer.reject(new APIError(409, 'Old plan changed'))
  await pending
  expect(form.state.newTitle).toBe('New draft')
  expect(toast).not.toHaveBeenCalled()
  expect(form.store.load).not.toHaveBeenCalled()
  expect(context.data.walker.value.value).toEqual(walker('project-a', 'release-b'))
})

it('the ticket form rechecks editability before submitting its captured draft', async () => {
  const context = await setup(), form = planForm(context)
  form.state.newTitle = 'Draft to retain'
  context.editable.value = false
  await form.submit()
  expect(transport.addPlanTicket).not.toHaveBeenCalled()
  expect(form.state.newTitle).toBe('Draft to retain')
})

it('a foreign-release membership result cannot patch the plan or report success', async () => {
  const context = await setup(), form = planForm(context)
  await context.data.loadWalker('release-b')
  const addedExisting = form.state.addedExisting as (payload: unknown) => Promise<void>
  await addedExisting({ count: 1, result: { walker: walker(), event_id: 123 } })
  expect(context.data.walker.value.value).toEqual(walker('project-a', 'release-b'))
  expect(transport.listWork).not.toHaveBeenCalled()
  expect(form.store.load).not.toHaveBeenCalled()
  expect(toast).not.toHaveBeenCalled()
})
