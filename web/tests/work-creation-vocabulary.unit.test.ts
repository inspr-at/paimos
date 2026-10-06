// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import { readFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { compileScript, parse } from '@vue/compiler-sfc'
import { baseParse, compile, type TemplateChildNode } from '@vue/compiler-dom'
import ts from 'typescript'
import * as work from '../src/lib/work'
import * as vocabulary from '../src/lib/workVocabulary'
import * as recurrences from '../src/lib/recurrences'
import type { Identity } from '../src/lib/api'

const fixture = vi.hoisted(() => ({ session: {} as { identity: Identity | null }, api: vi.fn() }))
vi.mock('../src/stores/session', () => ({ useSession: () => fixture.session }))
vi.mock('../src/lib/api', () => ({ api: fixture.api }))
const { useWorkVocabulary } = await import('../src/stores/workVocabulary')
let pinia: Pinia
const scopes: Vue.EffectScope[] = []
const identity = (id: string): Identity => ({ tenant: { id: `tenant-${id}`, name: id }, principal: { id: `person-${id}`, name: id } })
const names = (name = '', icon = '') => ({ revision: 1, leaf: { name, icon }, levels: [{ name: 'Vorhaben', icon: 'tree' }] })
const response = (value: ReturnType<typeof names>) => ({ ok: true, json: async () => value })
beforeEach(() => {
  setActivePinia(pinia = createPinia())
  fixture.session = Vue.reactive({ identity: identity('a') })
  fixture.api.mockReset()
  vi.stubGlobal('navigator', { platform: 'Mac' })
})
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); disposePinia(pinia); vi.unstubAllGlobals() })

function source(path: string) {
  // Execute the exact reviewed blob in baseline mode; keep the same assertions.
  return process.env.AEON_648_FIX19_BASELINE === '1'
    ? execFileSync('git', ['show', `b705fb87:web/src/${path}`], { encoding: 'utf8' })
    : readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8')
}
function component(path: string, inlineTemplate = true, extra: Record<string, unknown> = {}) {
  const { descriptor } = parse(source(path))
  const { content } = compileScript(descriptor, { id: 'work-creation-vocabulary', inlineTemplate })
  const modules: Record<string, unknown> = {
    vue: { ...Vue, onMounted: () => {}, onBeforeUnmount: Vue.onScopeDispose },
    '../../lib/work': work, '../../lib/workVocabulary': vocabulary,
    '../../directives/clipTip': { vClipTip: {} },
    '../../stores/workVocabulary': { useWorkVocabulary }, ...extra,
  }
  const exports: { default?: Vue.Component & { setup: (props: object, context: object) => unknown } } = {}
  new Function('require', 'exports', ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)((id: string) => {
    if (id.endsWith('AppIcon.vue')) return { __esModule: true, default: Vue.defineComponent({ props: ['name'], setup: props => () => Vue.h('svg', { 'data-icon': props.name }) }) }
    if (id.endsWith('.vue')) return { __esModule: true, default: Vue.defineComponent({ render: () => null }) }
    if (!(id in modules)) throw new Error(`Unexpected dependency ${id}`)
    return modules[id]
  }, exports)
  return exports.default!
}
const quickProps = { projectId: 'project', knownStates: [], trailing: 0, create: vi.fn(async () => true) }
async function renderBranch(path: string, match: (text: string) => boolean, state: object) {
  const { descriptor } = parse(source(path))
  let branch = ''
  const visit = (nodes: TemplateChildNode[]) => {
    for (const node of nodes) if (node.type === 1) {
      if (match(node.loc.source) && !branch) branch = node.loc.source
      else visit(node.children)
    }
  }
  visit(baseParse(descriptor.template!.content).children)
  expect(branch).not.toBe('')
  const { code } = compile(branch, { mode: 'function' })
  const render = new Function('Vue', code)(Vue)
  return renderToString(Vue.createSSRApp({ render, setup: () => state, components: { AppIcon: Vue.defineComponent({ render: () => null }) } }))
}

it('quick creation retains the default Ticket name while submitting a work draft', async () => {
  const store = useWorkVocabulary(); store.accept(names())
  const html = await renderToString(Vue.createSSRApp(component('components/work/QuickCreateRow.vue'), quickProps))
  expect(html).toContain('aria-label="Type: Ticket"')
  expect(html).toContain('placeholder="Ticket title"')
  expect(html).toContain('aria-label="New ticket title"')
  expect(html).not.toContain('Work item')
  const scope = Vue.effectScope(); scopes.push(scope)
  const state = scope.run(() => component('components/work/QuickCreateRow.vue', false).setup(quickProps, { expose() {} })) as { draft: { title: string; kind: string }; submit: () => Promise<void> }
  state.draft.title = '  A new title  '
  await state.submit()
  expect(quickProps.create).toHaveBeenLastCalledWith(expect.objectContaining({ title: 'A new title', kind: 'work' }))
})

it('detail deletion names the on-screen workspace level', async () => {
  const deletion = await renderBranch('components/work/TicketHeaderBar.vue', text => text.startsWith('<button') && text.includes("pick('delete')"), { canDelete: true, kind: 'work', levelName: 'Vorhaben', workNoun: vocabulary.workNoun, kindLabel: work.kindLabel, pick() {} })
  expect(deletion).toContain('Delete Vorhaben…')
})

it('type properties resolve unenriched work parents through the workspace vocabulary', async () => {
  const property = await renderBranch('components/work/TicketProperties.vue', text => text.startsWith('<span class="prop-static"') && text.includes('workIcon(item)'), { item: { kind_slug: 'work', is_leaf: false, depth: 1 }, vocabulary: { value: names('Arbeitsschritt') }, workIcon: vocabulary.workIcon, workLabel: vocabulary.workLabel, kindLabel: work.kindLabel })
  expect(property).toContain('Vorhaben')
})

it('quick creation uses custom leaf spelling and icon under a parent', async () => {
  useWorkVocabulary().accept(names('Arbeitsschritt', 'check'))
  const html = await renderToString(Vue.createSSRApp(component('components/work/QuickCreateRow.vue'), { ...quickProps, initialEpic: { id: 'deep', key: 'PRJ-42', title: 'Deep parent' } }))
  expect(html).toContain('aria-label="Type: Arbeitsschritt"')
  expect(html).toContain('aria-label="New Arbeitsschritt title"')
  expect(html).toContain('placeholder="Arbeitsschritt in PRJ-42"')
  expect(html).toContain('data-icon="check"')
  const scope = Vue.effectScope(); scopes.push(scope)
  const state = scope.run(() => component('components/work/QuickCreateRow.vue', false).setup(quickProps, { expose() {} })) as { kindOptions: Vue.ComputedRef<{ value: string; label: string }[]>; subject: Vue.ComputedRef<string> }
  expect(Vue.unref(state.kindOptions)).toEqual([{ value: 'work', label: 'Arbeitsschritt' }])
  expect(Vue.unref(state.subject)).toBe('the new Arbeitsschritt')
})

it('read-only details name the workspace level on screen', async () => {
  const html = await renderBranch('components/work/TicketWorkspace.vue', text => text.startsWith('<p') && text.includes('You can read this'), { editable: false, item: { kind_slug: 'work', level_name: 'Vorhaben' }, vocabulary: { value: names() }, workNoun: vocabulary.workNoun, workLabel: vocabulary.workLabel, kindLabel: work.kindLabel })
  expect(html).toContain('You can read this Vorhaben but not change it.')
})

it('child creation preserves custom names in its heading, action and parent context', async () => {
  const html = await renderToString(Vue.createSSRApp(component('components/work/ChildList.vue'), {
    children: [], editable: true, loading: false, childLabel: 'Arbeitsschritt', parentLabel: 'Vorhaben', progress: { done: 0, total: 0, percent: 0 }, add: vi.fn(),
  }))
  expect(html).toContain('Children in this Vorhaben')
  expect(html).toContain('Arbeitsschritt <span')
  expect(html).toContain('Add Arbeitsschritt')
  expect(html).toContain('No children yet.')
  expect(html).not.toContain('Arbeitsschritts')
})

it('agent summaries name the on-screen level instead of the internal work kind', () => {
  const scope = Vue.effectScope(); scopes.push(scope)
  const state = scope.run(() => component('components/work/TicketAgentWork.vue', false, {
    '../../stores/agents': { useAgents: () => ({ loaded: true }) },
    '../../lib/deliveryRating': { loadNodeRatings: async () => [] },
    '../../lib/ticketAgentWork': { loadTicketAgentWork: async () => ({ sessions: [] }) },
  }).setup({ nodeId: 'parent', kind: 'work', levelName: 'Vorhaben' }, { expose() {} })) as { noun: Vue.ComputedRef<string>; rollup: Vue.ComputedRef<string>; report: Vue.Ref<unknown> }
  state.report.value = { includes_descendants: true, sessions: [{}] }
  expect(state.noun.value).toBe('Vorhaben')
  expect(state.rollup.value).toBe('Totals include this Vorhaben and its descendant leaves.')
})

it('recurrence templates display the configured leaf name and child description', async () => {
  useWorkVocabulary().accept(names('Arbeitsschritt', 'check'))
  const context: { teleports?: Record<string, string> } = {}
  await renderToString(Vue.createSSRApp(component('components/recurrences/RecurrenceEditor.vue', true, {
    vue: { ...Vue, onMounted() {}, onBeforeUnmount() {}, watch: () => () => {} },
    '../../lib/api': {}, '../../lib/authz': { can: () => true }, '../../lib/recurrences': recurrences,
    '../../lib/useIdentityScope': { useIdentityScope: () => ({ owner: Vue.computed(() => 'tenant/person'), lane: () => ({ cancel() {} }) }) },
  }), { project: { id: 'project', routeKey: 'PRJ' }, source: { id: 'parent', key: 'PRJ-1', title: 'Parent', kind_slug: 'work', is_leaf: false, fields: {}, body: '' } }), context)
  const html = context.teleports?.body
  expect(html).toBeDefined()
  expect(html).toContain('<span class="field-label">Arbeitsschritt</span>')
  expect(html).toContain('Creates a child Arbeitsschritt of PRJ-1 each time')
})

it('workspace naming reads are shared and stale identities cannot restore old names', async () => {
  let release!: (value: ReturnType<typeof response>) => void
  const old = new Promise<ReturnType<typeof response>>(resolve => { release = resolve })
  fixture.api.mockReturnValueOnce(old).mockResolvedValueOnce(response(names('Other step')))
  const store = useWorkVocabulary(), first = store.load()
  const shared = store.load()
  expect(fixture.api).toHaveBeenCalledTimes(1)
  const oldSignal = fixture.api.mock.calls[0]![1].signal as AbortSignal
  fixture.session.identity = identity('b')
  expect(oldSignal.aborted).toBe(true)
  expect(store.leaf.name).toBe('Ticket')
  await store.load()
  release(response(names('Old private name'))); await Promise.all([first, shared])
  expect(store.leaf.name).toBe('Other step')
  expect(store.loaded).toBe(true)
  fixture.session.identity = null
  expect(store.leaf.name).toBe('Ticket')
  expect(store.loaded).toBe(false)
})

it('failed workspace naming reads expose failure and remain retryable', async () => {
  fixture.api.mockResolvedValueOnce({ ok: false }).mockResolvedValueOnce(response(names('Retry step')))
  const store = useWorkVocabulary()
  await store.load()
  expect(store.loaded).toBe(false)
  expect(store.error).toBe('Workspace names could not be read. Try again before creating.')
  await store.load()
  expect(store.loaded).toBe(true)
  expect(store.error).toBe('')
  expect(store.leaf.name).toBe('Retry step')
})

it('unsaved naming drafts and late reads cannot overwrite the accepted names', async () => {
  let release!: (value: ReturnType<typeof response>) => void
  fixture.api.mockReturnValueOnce(new Promise<ReturnType<typeof response>>(resolve => { release = resolve }))
  const store = useWorkVocabulary(), reading = store.load()
  const saved = names('Saved step')
  store.accept(saved)
  saved.leaf.name = 'Unsaved draft'; saved.levels[0].name = 'Unsaved parent'
  expect(store.leaf.name).toBe('Saved step')
  expect(store.value.levels[0].name).toBe('Vorhaben')
  release(response(names('Old read'))); await reading
  expect(store.leaf.name).toBe('Saved step')
  expect(store.loaded).toBe(true)
})
