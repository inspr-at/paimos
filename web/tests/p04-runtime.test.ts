// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1676: execute the production setup/watchers with held IO and delivered
// viewport samples. Native canvas picking, focus and geometry stay in browsers.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import * as glimpse from '../src/lib/headerGlimpse.ts'
import * as navigation from '../src/components/work/projectNavigation.ts'
import * as list from '../src/lib/ticketList.ts'
import * as adapter from '../src/lib/ticketGraphRenderer.ts'
import * as figures from '../src/lib/groupFigures.ts'
import * as workFigures from '../src/lib/workFigures.ts'
import * as figuresText from '../src/lib/workFiguresText.ts'
import { APIError, figuresPath, type ListQuery, type ProjectFigures } from '../src/lib/api.ts'
import type { GraphData, GraphRenderer, GraphRendererOptions } from '../src/lib/graphRenderer.ts'
import type { TicketGraph } from '../src/lib/ticketGraph.ts'
import { deferred, flush } from './record-source.ts'
import { wholeFigures } from './work-figures-fixture.ts'

class Element extends EventTarget {
  width = 1600
  height = 200
  clientWidth = 800
  clientHeight = 400
  style = { setProperty() {} }
  classList = { contains: (_name: string) => false }
  closest() { return null }
  getBoundingClientRect() {
    return { x: 0, y: 0, left: 0, top: 0, right: this.width, bottom: this.height, width: this.width, height: this.height }
  }
  focus() {}
}

// Only browser boundaries are replaced. No layout assertion is made against
// these boxes. Deliveries and clocks are advanced explicitly, without sleeps.
function environment() {
  let next = 0
  const frames = new Map<number, () => void>(), tasks = new Map<number, () => void>(), intervals = new Map<number, () => void>()
  const schedule = (queue: Map<number, () => void>, callback: () => void) => { queue.set(++next, callback); return next }
  const drain = (queue: Map<number, () => void>) => { const pending = [...queue.values()]; queue.clear(); pending.forEach(run => run()) }
  const media = new Map<string, EventTarget & { matches: boolean }>()
  const root = new Element(), body = new Element()
  const document = Object.assign(new EventTarget(), { documentElement: root, body, hidden: false, fullscreenElement: null, getElementById: (_id: string) => null })
  const window = Object.assign(new EventTarget(), {
    innerWidth: 1600, innerHeight: 900, visualViewport: new EventTarget(),
    matchMedia(query: string) {
      if (!media.has(query)) media.set(query, Object.assign(new EventTarget(), { matches: query.includes('min-width') }))
      return media.get(query)!
    },
    setTimeout: (run: () => void) => schedule(tasks, run), clearTimeout: (id: number) => tasks.delete(id),
    setInterval: (run: () => void) => schedule(intervals, run), clearInterval: (id: number) => intervals.delete(id),
    requestIdleCallback: (run: () => void) => schedule(tasks, run), cancelIdleCallback: (id: number) => tasks.delete(id),
  })
  class Observer {
    targets = new Set<unknown>()
    disconnected = false
    readonly deliver: (entries: unknown[]) => void
    constructor(deliver: (entries: unknown[]) => void) { this.deliver = deliver; observers.push(this) }
    observe(target: unknown) { this.targets.add(target) }
    disconnect() { this.disconnected = true; this.targets.clear() }
  }
  const observers: Observer[] = []
  const bindings = {
    window, document, ResizeObserver: Observer, MutationObserver: Observer, IntersectionObserver: Observer,
    HTMLElement: Element, getComputedStyle: () => ({ display: 'block', getPropertyValue: () => '' }),
    requestAnimationFrame: (run: () => void) => schedule(frames, run), cancelAnimationFrame: (id: number) => frames.delete(id),
    requestIdleCallback: window.requestIdleCallback, clearTimeout: window.clearTimeout,
  }
  return {
    ...bindings, bindings, observers, frames, tasks, intervals,
    frame: () => drain(frames), idle: () => drain(tasks),
    media(query: string, matches: boolean) {
      const target = window.matchMedia(query)
      target.matches = matches
      target.dispatchEvent(Object.assign(new Event('change'), { matches }))
    },
  }
}
type Environment = ReturnType<typeof environment>
type Mutation = { before: string; after: string }

function source(path: string, mutation?: Mutation) {
  let value = readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8')
  if (mutation) {
    assert.equal(value.split(mutation.before).length, 2, 'negative control must change exactly one guard')
    value = value.replace(mutation.before, mutation.after)
  }
  return value
}

// Compile setup, not a second state machine. Hook callbacks run in the same
// order as mount/unmount; refs are supplied where a native renderer sets them.
function setup(path: string, props: object, modules: Record<string, unknown>, env: Environment, mutation?: Mutation) {
  const mounted: (() => void)[] = [], unmounted: (() => void)[] = []
  const vue = { ...Vue, onMounted: (run: () => void) => mounted.push(run), onBeforeUnmount: (run: () => void) => unmounted.push(run) }
  const input = source(path, mutation)
  const content = path.endsWith('.vue') ? compileScript(parse(input).descriptor, { id: path }).content : input
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: Record<string, any> = {}
  new Function('require', 'exports', ...Object.keys(env.bindings), outputText)((id: string) => {
    if (id === 'vue') return vue
    if (id.endsWith('.vue')) return { __esModule: true, default: {} }
    assert.ok(id in modules, `unbound production dependency: ${id}`)
    return modules[id]
  }, exports, ...Object.values(env.bindings))
  const emitted: unknown[][] = [], scope = Vue.effectScope()
  const state = scope.run(() => path.endsWith('.vue')
    ? exports.default.setup(props, { expose() {}, emit: (...args: unknown[]) => emitted.push(args) })
    : exports.useGroupFigures(props))
  let stopped = false
  return {
    state, emitted, mount: () => mounted.forEach(run => run()),
    stop() { if (stopped) return; stopped = true; unmounted.forEach(run => run()); scope.stop() },
  }
}

const graphData: GraphData = { nodes: [{ id: 'a', label: 'A', group: '', color: '--teal', weight: 1 }], links: [] }
function engine(dimension: '2d' | '3d' = '3d') {
  return { dimension, data: test.mock.fn(), emphasis: test.mock.fn(), theme: test.mock.fn(), resize: test.mock.fn(), fit: test.mock.fn(), focus: test.mock.fn(), motion: test.mock.fn(), labels: test.mock.fn(), frameRate: test.mock.fn(), interact: test.mock.fn(), dispose: test.mock.fn() } satisfies GraphRenderer
}
function canvas(mutation?: Mutation) {
  const env = environment(), requests: { dimension: string; options: GraphRendererOptions; result: ReturnType<typeof deferred<GraphRenderer | null>> }[] = []
  const props = Vue.reactive({ data: graphData, glimpse: true, fps: 60, selectedId: '', searching: false })
  const app = setup('components/graph/GraphCanvas.vue', props, {
    'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({ replace() {} }) },
    '../../lib/graphRenderer': { createGraphRenderer: (_host: unknown, dimension: string, options: GraphRendererOptions) => {
      const result = deferred<GraphRenderer | null>(); requests.push({ dimension, options, result }); return result.promise
    } },
    '../../lib/graphMotion': { useGraphMotion: () => ({ seconds: Vue.ref(120) }) },
    '../../lib/graphFocus': { exitGraphFullscreen() {}, requestGraphFullscreen() {} },
  }, env, mutation)
  app.state.host.value = new Element()
  app.mount()
  return { ...app, env, props, requests }
}

async function lateRenderer(mutation?: Mutation) {
  const app = canvas(mutation), late = engine()
  try {
    await flush()
    assert.equal(app.requests.length, 1, 'the renderer setup is pending')
    app.stop()
    assert.equal(app.requests[0]!.options.signal.aborted, true, 'leaving the graph aborts its setup')
    app.requests[0]!.result.resolve(late)
    await flush()
    assert.equal(late.dispose.mock.callCount(), 1, 'a late renderer must be disposed')
    assert.equal(late.data.mock.callCount(), 0, 'an unmounted graph never receives late data')
    assert.equal(late.resize.mock.callCount(), 0)
    assert.equal(app.state.ready.value, false)
    assert.ok(app.env.observers.every(observer => observer.disconnected), 'observers disconnect on leaving the graph')
  } finally { app.stop() }
}
test('P04 graph: leaving during setup aborts and disposes the late renderer without publishing readiness', () => lateRenderer())

test('P04 graph: changing dimension makes the pending engine obsolete and preserves the current engine', async () => {
  const app = canvas(), old = engine(), current = engine('2d')
  try {
    await flush()
    app.state.setDimension('2d')
    await flush()
    assert.equal(app.requests.length, 2)
    assert.deepEqual(app.requests.map(request => request.dimension), ['3d', '2d'])
    assert.equal(app.requests[0]!.options.signal.aborted, true)
    app.requests[1]!.result.resolve(current)
    await flush()
    assert.equal(app.state.ready.value, true)
    assert.deepEqual(current.data.mock.calls[0]!.arguments, [graphData])
    app.requests[0]!.result.resolve(old)
    await flush()
    assert.equal(old.dispose.mock.callCount(), 1)
    assert.equal(old.data.mock.callCount(), 0)
    app.requests[1]!.options.labelsSettled!(true)
    app.requests[0]!.options.labelsSettled!(false)
    assert.equal(app.state.labelsReady.value, true, 'a late label callback cannot unset the current engine')
    app.props.fps = 30
    app.props.data = { nodes: [{ ...graphData.nodes[0]!, id: 'b' }], links: [] }
    await flush()
    assert.deepEqual(current.frameRate.mock.calls.at(-1)!.arguments, [30])
    assert.equal(current.data.mock.calls.at(-1)!.arguments[0], app.props.data)
    app.stop()
    assert.equal(current.dispose.mock.callCount(), 1)
    assert.equal(old.dispose.mock.callCount(), 1)
    app.requests[1]!.options.labelsSettled!(false)
    assert.equal(app.state.labelsReady.value, true, 'unmounted label callbacks are inert')
  } finally { app.stop() }
})

function topology(prefix: string): TicketGraph {
  const nodes = Array.from({ length: 8 }, (_, index) => ({
    id: `${prefix}-${index}`, key: `${prefix}-${index + 1}`, title: `${prefix} ticket ${index + 1}`,
    type: 'ticket' as const, status: 'new', status_category: 'open' as const, priority: null,
    parent_id: null, release_id: null, updated_at: '2026-10-01T00:00:00Z', link_count: index < 2 ? 1 : 0,
  }))
  return { nodes, links: [{ source: nodes[0]!.id, target: nodes[1]!.id, kind: 'relates' }], truncated: false }
}
function header(mutation?: Mutation) {
  const env = environment(), push = test.mock.fn()
  const route = Vue.reactive<{ meta: { projectSection: string }; query: Record<string, string> }>({ meta: { projectSection: 'tickets' }, query: {} })
  const props = Vue.reactive({ projectId: 'project-a', projectKey: 'A', ticketCount: 8, enabled: true, filters: undefined as list.ListFilters | undefined, fps: 60 })
  const requests: { id: string; filters: list.ListFilters; signal: AbortSignal; result: ReturnType<typeof deferred<{ data: TicketGraph; visible: TicketGraph }>> }[] = []
  const app = setup('components/work/HeaderGlimpse.vue', props, {
    'vue-router': { useRoute: () => route, useRouter: () => ({ push }) },
    './projectNavigation': navigation, '../../lib/ticketList': list, '../../lib/ticketGraphRenderer': adapter,
    '../../lib/headerGlimpse': { ...glimpse, loadTicketGraphContext: (id: string, filters: list.ListFilters, signal: AbortSignal) => {
      const result = deferred<{ data: TicketGraph; visible: TicketGraph }>(); requests.push({ id, filters, signal, result }); return result.promise
    } },
  }, env, mutation)
  const paused = test.mock.fn()
  app.state.canvas.value = { setPaused: paused }
  app.mount()
  return {
    ...app, env, props, route, requests, push, paused,
    async start() { env.idle(); await flush(); assert.equal(requests.length, 1, 'idle starts one context read') },
    async answer(index: number, prefix: string) { const data = topology(prefix); requests[index]!.result.resolve({ data, visible: data }); await flush() },
  }
}

async function staleContext(mutation?: Mutation) {
  const app = header(mutation)
  try {
    await app.start()
    app.props.projectId = 'project-b'; app.props.projectKey = 'B'
    await flush()
    assert.equal(app.requests.length, 2)
    assert.equal(app.requests[0]!.signal.aborted, true, 'changing project aborts its context')
    await app.answer(1, 'B')
    assert.equal(app.state.paint.value, true)
    await app.answer(0, 'A')
    assert.deepEqual(app.state.data.value.nodes.map((node: { id: string }) => node.id), topology('B').nodes.map(node => node.id), 'an old project context must not replace the current graph')
    assert.ok(app.state.data.value.nodes.every((node: { href: string }) => node.href.startsWith('/p/B/')))
    app.props.filters = list.filtersFromQuery({ priority: 'high' })
    await flush()
    assert.equal(app.state.paint.value, false, 'the old scope disappears while the new context is pending')
    assert.deepEqual(app.requests[2]!.filters.priority, ['high'])
    app.props.filters = list.filtersFromQuery({ priority: 'low' })
    await flush()
    assert.equal(app.requests[2]!.signal.aborted, true)
    await app.answer(3, 'Low')
    await app.answer(2, 'High')
    assert.deepEqual(app.state.data.value.nodes.map((node: { id: string }) => node.id), topology('Low').nodes.map(node => node.id), 'a held old filter cannot replace the current graph')
    app.props.filters = list.filtersFromQuery({ q: 'pending when leaving' })
    await flush()
    assert.equal(app.state.paint.value, false)
    app.stop()
    assert.equal(app.requests[4]!.signal.aborted, true)
    await app.answer(4, 'Filtered')
    assert.equal(app.state.paint.value, false)
  } finally { app.stop() }
}
test('P04 header: a held old project or filter context cannot paint after the scope leaves', () => staleContext())

test('P04 header: section searches stay out of Tickets and Open graph carries the effective filter identity', async () => {
  const app = header()
  try {
    app.route.meta.projectSection = 'knowledge'; app.route.query = { q: 'knowledge-only' }
    await app.start()
    assert.equal(app.requests[0]!.filters.q, '')
    await app.answer(0, 'A')
    app.route.meta.projectSection = 'tickets'; app.route.query = { q: 'server body match', status: 'in_progress', assignee: 'person-a' }
    await flush()
    assert.equal(app.requests[1]!.filters.q, 'server body match')
    app.props.filters = list.filtersFromQuery({ q: 'saved-view body', priority: 'high', type: 'leaf', status: 'qa', v: '11111111-1111-4111-8111-111111111111' })
    await flush()
    assert.equal(app.requests[1]!.signal.aborted, true)
    assert.equal(app.requests[2]!.filters.q, 'saved-view body')
    app.state.openGraph()
    assert.deepEqual(app.push.mock.calls[0]!.arguments, [{ path: '/p/A/tickets', query: {
      q: 'saved-view body', priority: 'high', type: 'leaf', status: 'qa', v: '11111111-1111-4111-8111-111111111111', view: 'graph',
    } }])
  } finally { app.stop() }
})

test('P04 header: visibility and occlusion never resume a graph the person paused', async () => {
  const app = header()
  try {
    await app.start(); await app.answer(0, 'A')
    assert.deepEqual(app.paused.mock.calls.at(-1)!.arguments, [false])
    app.state.togglePause()
    assert.deepEqual(app.paused.mock.calls.at(-1)!.arguments, [true])
    app.env.document.hidden = true; app.env.document.dispatchEvent(new Event('visibilitychange'))
    app.env.document.hidden = false; app.env.document.dispatchEvent(new Event('visibilitychange'))
    assert.deepEqual(app.paused.mock.calls.at(-1)!.arguments, [true], 'returning to the document preserves Pause')
    app.state.occluded.value = true
    app.state.togglePause()
    assert.deepEqual(app.paused.mock.calls.at(-1)!.arguments, [true], 'Resume does not animate an occluded graph')
    app.state.occluded.value = false; app.state.applyMotion()
    assert.deepEqual(app.paused.mock.calls.at(-1)!.arguments, [false])
    app.env.media('(prefers-reduced-motion: reduce)', true)
    await flush()
    assert.equal(app.state.paint.value, false)
    assert.equal(app.env.frames.size, 0, 'stopping paint cancels its frame loop')
    app.stop()
    assert.equal(app.env.intervals.size, 0)
    assert.equal(app.env.tasks.size, 0)
    assert.ok(app.env.observers.every(observer => observer.disconnected))
  } finally { app.stop() }
})

async function deliveredWidth(mutation?: Mutation) {
  const app = header(mutation)
  try {
    await app.start(); await app.answer(0, 'A')
    assert.equal(app.state.paint.value, true)
    const observer = app.env.observers.find(candidate => candidate.targets.has(app.env.document.documentElement))!
    assert.ok(observer, 'the viewport observer is installed')
    // No frames, tasks, window events or fresh geometry: deliver only the
    // observer's narrow sample while all synchronous hints still say wide.
    observer.deliver([{ target: app.env.document.documentElement, borderBoxSize: [{ inlineSize: 390 }], contentRect: { width: 390 } }])
    await flush()
    assert.equal(app.state.paint.value, false, 'a delivered narrow width must clear the painted glimpse')
    app.env.media('(min-width: 1280px)', true)
    await flush()
    assert.equal(app.state.paint.value, false, 'a late wide-media callback cannot revive it')
  } finally { app.stop() }
}
test('P04 header: a held-delivery narrow root sample wins over stale media and geometry', () => deliveredWidth())

function grouped(mutation?: Mutation) {
  const env = environment(), requests: { id: string; query: ListQuery; signal: AbortSignal; result: ReturnType<typeof deferred<ProjectFigures>> }[] = []
  const options = {
    owner: Vue.ref('tenant-a/person-a'), projectId: Vue.ref<string | null>('project-a'),
    filters: Vue.ref(list.filtersFromQuery({ group: 'priority' })),
    groups: Vue.ref<list.RowGroup[]>([{ key: 'high', label: 'High', rows: [], total: 1 }]), ready: Vue.ref(true), freshness: Vue.ref(0),
  }
  const app = setup('lib/useGroupFigures.ts', options, {
    './api': { APIError, figuresPath, getProjectFigures: (id: string, query: ListQuery, signal: AbortSignal) => {
      const result = deferred<ProjectFigures>(); requests.push({ id, query, signal, result }); return result.promise
    } },
    './groupFigures': figures, './workFigures': workFigures, './workFiguresText': figuresText,
  }, env, mutation)
  return { ...app, env, options, requests, async idle() { env.frame(); env.idle(); await flush() } }
}
async function changedScope(kind: 'filter' | 'project' | 'person', mutation?: Mutation) {
  const app = grouped(mutation), first = { ...wholeFigures(), view_key: 'old-scope' }
  try {
    await app.idle()
    assert.equal(app.requests.length, 1)
    assert.equal(app.requests[0]!.id, 'project-a')
    assert.deepEqual(app.requests[0]!.query.priority, ['high'])
    app.requests[0]!.result.resolve(first)
    await flush()
    assert.equal(app.state.groupFigures.value.get('high')?.figures, first)
    // Refresh the same group, but leave its answer held across the scope
    // change. The list/header are not ready, so no new idle read can land.
    app.options.freshness.value++
    await flush()
    assert.equal(app.requests.length, 2)
    app.options.ready.value = false
    if (kind === 'filter') app.options.filters.value = list.filtersFromQuery({ group: 'priority', assignee: 'person-b' })
    if (kind === 'project') app.options.projectId.value = 'project-b'
    if (kind === 'person') app.options.owner.value = 'tenant-a/person-b'
    await flush()
    assert.equal(app.state.groupFigures.value.has('high'), false, 'a changed scope must clear old figures before idle')
    assert.equal(app.requests[1]!.signal.aborted, true)
    app.requests[1]!.result.resolve({ ...first, view_key: 'held-old-scope' })
    await flush()
    assert.equal(app.state.groupFigures.value.has('high'), false, 'a late count cannot refill another scope')
    app.options.ready.value = true
    await flush(); await app.idle()
    assert.equal(app.requests.length, 3)
    assert.equal(app.requests[2]!.id, kind === 'project' ? 'project-b' : 'project-a')
    assert.deepEqual(app.requests[2]!.query.assignee, kind === 'filter' ? ['person-b'] : [])
    const current = { ...first, view_key: 'current-scope' }
    app.requests[2]!.result.resolve(current)
    await flush()
    assert.equal(app.state.groupFigures.value.get('high')?.figures, current)
    app.options.freshness.value++
    await flush()
    assert.equal(app.requests.length, 4)
    app.stop()
    assert.equal(app.requests[3]!.signal.aborted, true, 'leaving aborts the pending count')
    app.requests[3]!.result.resolve({ ...first, view_key: 'after-unmount' })
    await flush()
    assert.equal(app.state.groupFigures.value.get('high')?.figures, current, 'a stopped loader cannot publish another count')
  } finally { app.stop() }
}
for (const kind of ['filter', 'project', 'person'] as const) {
  test(`P04 figures: a changed ${kind} clears counts before idle and rejects a held old answer`, () => changedScope(kind))
}

// A passing replacement must also reject the risk when its production guard
// is removed. Mutants exist only in memory; no product file or Git tree changes.
const negative = (run: () => Promise<void>, message: string) => assert.rejects(run, (error: unknown) =>
  error instanceof assert.AssertionError && error.message.includes(message))
test('P04 negative control: missing late-renderer disposal is detected', () => negative(() => lateRenderer({
  before: 'if (request.signal.aborted || !mounted) { next.dispose(); return }', after: 'if (request.signal.aborted || !mounted) { return }',
}), 'a late renderer must be disposed'))
test('P04 negative control: accepting an aborted graph context is detected', () => negative(() => staleContext({
  before: 'if (controller.signal.aborted || !allowed.value) return', after: 'if (!allowed.value) return',
}), 'an old project context must not replace the current graph'))
test('P04 negative control: ignoring a delivered viewport sample is detected', () => negative(() => deliveredWidth({
  before: 'if (viewport) observedViewportWidth = viewport.borderBoxSize[0]?.inlineSize ?? viewport.contentRect.width', after: 'if (viewport) observedViewportWidth = Infinity',
}), 'a delivered narrow width must clear the painted glimpse'))
test('P04 negative control: keeping old-scope counts until idle is detected', () => negative(() => changedScope('filter', {
  before: 'dropChanged(id, owner, group)', after: '/* negative control: old scope retained */',
}), 'a changed scope must clear old figures before idle'))
