// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1003 (AEON-994 package 3) risks: a verdict says "on target" for the wrong
// side of the target or by colour alone; the summary names the wrong closest or
// biggest gap; the small chart shades the wrong side as the target zone or draws
// a line through days nobody measured; Learn cannot be opened, pinned or closed
// from the keyboard.
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
vi.mock('../src/lib/api.ts', () => ({ api: async () => new Response('{}'), APIError: class extends Error {} }))
import * as delivery from '../src/lib/delivery'
import * as flow from '../src/lib/deliveryFlow'
import * as flowModes from '../src/lib/deliveryFlowModes'
import * as flowWords from '../src/lib/deliveryFlowText'
import * as numbers from '../src/lib/deliveryNumbers'
import * as simple from '../src/lib/deliverySimple'
import { simpleNumbersOf, sparkGeometry, verdictOf, type SparkModel } from '../src/lib/deliverySimple'
import { TILES } from '../src/lib/deliveryNumbers'
import * as numbersText from '../src/lib/deliveryNumbersText'
import { simpleText } from '../src/lib/deliverySimpleText'
import { deliveryMetrics } from './delivery-numbers-fixtures'

const def = (key: string) => TILES.find(tile => tile.key === key)!
const en = simpleText('en')

it('a verdict is on target up to 1×, close up to 1.5×, far off beyond, with a word and its own shape for each', () => {
  const lower = { value: 8, up: false }, higher = { value: 80, up: true }
  expect(verdictOf(def('pr_ci_wall'), 8, lower, 'ready', en, 'en')).toMatchObject({ level: 'on', word: 'On target', gap: '' })
  expect(verdictOf(def('pr_ci_wall'), 12, lower, 'ready', en, 'en')).toMatchObject({ level: 'close', word: 'Close', gap: '1.5× the target' })
  expect(verdictOf(def('pr_ci_wall'), 13, lower, 'ready', en, 'en')).toMatchObject({ level: 'far', word: 'Far off', gap: '1.6× the target' })
  expect(verdictOf(def('pr_ci_wall'), 41, lower, 'ready', en, 'en')).toMatchObject({ level: 'far', gap: 'about 5× the target' })
  // Higher is better: 70 % against 80 % wanted is close, never "on target".
  expect(verdictOf(def('first_attempt_green'), 70, higher, 'ready', en, 'en')).toMatchObject({ level: 'close', gap: '70% vs. 80% wanted' })
  expect(verdictOf(def('first_attempt_green'), 85, higher, 'ready', en, 'en')).toMatchObject({ level: 'on', word: 'On target' })
  // Missing data, a failed read and a number without a target never get a verdict.
  expect(verdictOf(def('pr_ci_wall'), null, lower, 'ready', en, 'en')).toMatchObject({ level: 'none', word: 'No data yet' })
  expect(verdictOf(def('pr_ci_wall'), 3, lower, 'error', en, 'en')).toMatchObject({ level: 'none', word: 'Not loaded' })
  expect(verdictOf(def('pr_ci_wall'), 3, null, 'ready', en, 'en')).toMatchObject({ level: 'none', word: 'No target yet' })
  expect(verdictOf(def('pr_ci_wall'), 12, lower, 'ready', simpleText('de'), 'de')).toMatchObject({ word: 'Knapp dran', gap: '1,5× das Ziel' })
  // Merge rounds: the server's target is "≤ 20 % by a model"; Simple shows the scripted share, so ≥ 80 % and higher is better.
  expect(simple.targetOf(def('merge_rounds_model_share'), { target: { value: 20, direction: 'max', note: '', source: 'Arion' } } as never)).toEqual({ value: 80, up: true })
})

it('each tile shows its verdict as shape and word: the icon follows the level', () => {
  const source = readFileSync(new URL('../src/components/delivery/SimpleTile.vue', import.meta.url), 'utf8')
  expect(source).toContain("const ICON = { on: 'v-on', close: 'v-close', far: 'v-far', none: 'v-none' } as const")
  expect(source).toMatch(/<AppIcon :name="ICON\[tile\.verdict\.level\]"[^>]*\/>\s*<span[^>]*><b>\{\{ tile\.verdict\.word \}\}<\/b>/)
  const icons = readFileSync(new URL('../src/components/AppIcon.vue', import.meta.url), 'utf8')
  for (const name of ['v-on', 'v-close', 'v-far', 'v-none']) expect(icons).toContain(`name === '${name}'`)
})

it('the summary counts numbers on target and names the closest and the biggest gap from this window’s numbers', () => {
  const page = simpleNumbersOf(deliveryMetrics() as never, 7, 'ready', false, 'en')
  expect(page.summary).toEqual({
    kind: 'ready', big: 'None of the 10 numbers with a target is on target yet.',
    gaps: [{ label: 'Closest', name: 'how long a review takes', gap: '1.6× the target' }, { label: 'Biggest gap', name: 'from release to live', gap: 'about 5× the target' }],
    week: 'Last 7 days vs. the 7 before: 0 better · 0 worse · 7 steady · 3 without a comparison',
    charts: 'Small charts: one point per day · hatched = no data yet (history starts 11 Sept)',
  })
  expect(page.sections.map(section => [section.title, section.count, section.tiles.map(tile => tile.key)])).toEqual([
    ['Making a change ready', '0 of 4 on target', ['pr_ci_wall', 'first_attempt_green', 'time_to_first_green', 'review_time']],
    ['Getting it merged', '0 of 4 on target', ['pr_open_to_merged', 'queue_run_wall', 'queue_runs_per_pr', 'merge_rounds_model_share']],
    ['Shipping it', '0 of 2 on target', ['release_queue_to_live', 'nightly_green']],
  ])
  const nightly = page.sections[2].tiles[1]
  expect(nightly.verdict).toMatchObject({ level: 'far', gap: 'red every night since 5 Oct' })
  expect(nightly.say).toBe('The full test run failed on all 4 nights it ran, since it started on 5 Oct. Arion wants it green every night.')
  // A number that reaches its target counts as on target, and the gaps skip it.
  const data = deliveryMetrics() as ReturnType<typeof deliveryMetrics>
  const review = data.metrics.find(metric => metric.key === 'review_time')!
  review.windows[0] = { ...review.windows[0], value: 7, p50: 7 }
  const better = simpleNumbersOf(data as never, 7, 'ready', false, 'en')
  expect(better.summary).toMatchObject({ big: '1 of 10 numbers are on target.', gaps: [{ label: 'Closest', name: 'queue tries per change', gap: '1.6× the target' }, { label: 'Biggest gap', name: 'from release to live' }] })
  expect(better.sections[0].count).toBe('1 of 4 on target')
  expect(simpleNumbersOf(deliveryMetrics() as never, 7, 'ready', false, 'de').summary).toMatchObject({ big: 'Noch keine der 10 Zahlen mit Ziel ist im Ziel.', gaps: [{ label: 'Am nächsten dran', name: 'Wie lange ein Review dauert' }, { label: 'Größte Lücke' }] })
  // No data says so; a failed read shows no summary at all, never old numbers.
  expect(simpleNumbersOf(deliveryMetrics({ empty: true }) as never, 7, 'ready', true, 'en').summary).toEqual({ kind: 'nodata', title: 'No numbers yet.', body: 'They appear with the first pull request. The targets below already apply.' })
  const failed = simpleNumbersOf(null, 7, 'error', false, 'en')
  expect(failed.summary).toEqual({ kind: 'error' })
  expect(failed.sections.flatMap(section => section.tiles).every(tile => tile.value === null && tile.empty === 'Not loaded' && tile.verdict.word === 'Not loaded')).toBe(true)
})

it('a partial window with full calendar coverage names the missing facts beside the source', () => {
  // Risk: truncated facts are "partial" while every calendar day is covered, and Simple
  // then says only "Partial · Measured from GitHub" (AEON-1003 fix 2).
  const reason = 'Some facts are missing: a day had more runs than GitHub lists (1 000).'
  const data = deliveryMetrics()
  const wall = data.metrics.find(metric => metric.key === 'pr_ci_wall')!
  wall.reason = reason
  wall.status = 'partial'
  const seven = wall.windows.find(window => window.days === 7)!
  seven.status = 'partial'
  seven.coverage = { ...seven.coverage, full: true }
  const tile = simpleNumbersOf(data as never, 7, 'ready', false, 'en').sections.flatMap(section => section.tiles).find(item => item.key === 'pr_ci_wall')!
  expect(tile.partial).toBe(true)
  expect(tile.foot).toBe(`Measured from GitHub · ${reason}`)
  expect(tile.learn.body).toContain('p50 (median)')
  expect(tile.learn.body).toContain(reason)
  const german = simpleNumbersOf(data as never, 7, 'ready', false, 'de').sections.flatMap(section => section.tiles).find(item => item.key === 'pr_ci_wall')!
  expect(german.foot).toBe(`Gemessen über GitHub · ${reason}`)
  expect(german.learn.body).toContain(reason)
  // A short calendar span keeps its coverage phrase. This reason belongs to the full-coverage case.
  const merge = simpleNumbersOf(deliveryMetrics() as never, 7, 'ready', false, 'en').sections.flatMap(section => section.tiles).find(item => item.key === 'merge_rounds_model_share')!
  expect(merge.foot).toContain('covers 4 of 7 days')
  expect(merge.foot).not.toContain(reason)
})

const spark = (values: (number | null)[], statuses: SparkModel['statuses'], target: SparkModel['target']): SparkModel =>
  ({ kind: 'line', percent: false, values, statuses, target, nights: [], unit: 'day', mode: 'ready' })

it('the small chart shades the good side of the target and hatches only what no source covers', () => {
  // Lower is better: the zone runs from the target line down to the axis.
  const down = sparkGeometry(spark([16, null, 18, 14], ['ok', 'empty', 'ok', 'partial'], { value: 8, up: false, label: 'target 8 min' }), 200, 62)
  expect(down.zone!.y).toBeCloseTo(down.target!.y)
  expect(down.zone!.y + down.zone!.height).toBeCloseTo(down.plot.y + down.plot.height)
  expect(down.axis.arrow).toMatch(/^M2\.5 50/)
  // A covered day without samples is a gap in the line, never 0 and never hatched; partial days dash their segment.
  expect(down.hatches).toEqual([])
  expect(down.segments.map(segment => segment.partial)).toEqual([true])
  expect(down.dots.map(dot => dot.last)).toEqual([false, true])
  // Higher is better: the zone runs from the top down to the target line; days before the source are hatched.
  const up = sparkGeometry({ ...spark([null, null, null, 40, 50], ['no_data', 'no_data', 'no_data', 'ok', 'ok'], { value: 80, up: true, label: 'target 80%' }), percent: true }, 400, 62)
  expect(up.zone!.y).toBe(up.plot.y)
  expect(up.zone!.y + up.zone!.height).toBeCloseTo(up.target!.y)
  expect(up.hatches).toEqual([{ x: up.plot.x, width: up.plot.width * 3 / 5, label: true }])
  expect(up.axis.arrow).toMatch(/^M2\.5 12/)
})

// ---------- Mounted components ----------
type El = { tag: string; props: Record<string, unknown>; children: El[]; parent: El | null; text?: string }
const el = (tag: string): El => ({ tag, props: {}, children: [], parent: null })
const detach = (child: El) => { if (child.parent) child.parent.children.splice(child.parent.children.indexOf(child), 1); child.parent = null }
const renderer = Vue.createRenderer<El, El>({
  createElement: el, createText: text => ({ ...el('#text'), text }), createComment: () => el('#comment'),
  setText: (node, text) => { node.text = text }, setElementText: (node, text) => { node.children = [{ ...el('#text'), text, parent: node }] },
  querySelector: () => el('body'), parentNode: node => node.parent, nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
  patchProp: (node, key, _old, value) => { node.props[key] = value }, remove: detach,
  insert: (child, parent, anchor) => {
    detach(child); child.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    if (index < 0) parent.children.push(child); else parent.children.splice(index, 0, child)
  },
})
const all = (node: El): El[] => [node, ...node.children.flatMap(all)]
const textOf = (node: El): string => node.text ?? node.children.map(textOf).join('')
const apps: Vue.App[] = []
afterEach(() => { for (const app of apps.splice(0)) app.unmount(); vi.unstubAllGlobals() })

function component(file: string, modules: Record<string, unknown>, inline = true) {
  const { descriptor } = parse(readFileSync(new URL(`../src/components/delivery/${file}`, import.meta.url), 'utf8'))
  const { content } = compileScript(descriptor, { id: file, inlineTemplate: inline })
  const exports: { default?: Vue.Component } = {}
  new Function('require', 'exports', ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)((id: string) => {
    if (!(id in modules)) throw new Error(`Unexpected dependency ${id}`)
    return modules[id]
  }, exports)
  return exports.default!
}

it('TileSpark draws the green target zone, the dashed target, "better" on the axis and hatched days', async () => {
  vi.stubGlobal('ResizeObserver', class { constructor(private callback: (entries: unknown[]) => void) {} observe() { this.callback([{ contentRect: { width: 240 } }]) } disconnect() {} })
  const TileSpark = component('TileSpark.vue', { vue: Vue, '../../lib/deliverySimple': simple, '../../lib/deliveryNumbersText': numbersText })
  const model = spark([null, 20, 18, 16], ['no_data', 'ok', 'ok', 'ok'], { value: 8, up: false, label: 'target 8 min' })
  const app = renderer.createApp(TileSpark, { model, text: en })
  const root = el('root'); apps.push(app); app.mount(root)
  await Vue.nextTick()
  const nodes = all(root), expected = sparkGeometry(model, 240, 62)
  const zone = nodes.find(node => node.tag === 'rect' && node.props.class === 'sp-zone')!
  expect([zone.props.y, zone.props.height]).toEqual([expected.zone!.y, expected.zone!.height])
  const line = nodes.find(node => node.tag === 'line' && node.props.class === 'sp-tgt')!
  expect(line.props.y1).toBe(expected.target!.y)
  expect(nodes.filter(node => node.tag === 'text').map(textOf)).toEqual(['target 8 min', 'better'])
  expect(nodes.filter(node => node.tag === 'rect' && node.props.class === 'g-nodata')).toHaveLength(1)
  expect(nodes.find(node => node.tag === 'div')!.props['aria-hidden']).toBe('true')
})

it('Learn opens on focus, pins with Enter or a click, and Esc closes it and keeps focus on the button', async () => {
  vi.stubGlobal('window', { innerWidth: 1440, innerHeight: 900 })
  const scope = Vue.effectScope()
  const Learn = component('LearnPopover.vue', { vue: Vue, '../AppIcon.vue': { default: {} } }, false) as { setup: (props: object, context: object) => Record<string, unknown> }
  const props = Vue.reactive({ id: 'pr_ci_wall', name: 'How long the PR checks take', body: 'p50 …', expert: 'PR CI run (wall)', target: 'Target 8 min', labels: { learn: 'Learn', expertName: 'In the Expert view', arion: 'Project Arion' } })
  const state = scope.run(() => Learn.setup(props, { expose: () => {} }))!
  const focused: string[] = []
  ;(state.button as Vue.Ref).value = { focus: () => focused.push('learn'), getBoundingClientRect: () => ({ left: 0, right: 100, top: 0, bottom: 26 }), contains: () => false }
  const open = state.open as Vue.Ref<boolean>, pinned = state.pinned as Vue.Ref<boolean>
  ;(state.show as (pin: boolean) => void)(false) // focus
  expect([open.value, pinned.value]).toEqual([true, false])
  ;(state.onClick as () => void)() // Enter on a button is a click
  expect([open.value, pinned.value]).toEqual([true, true])
  const key = state.onKey as (event: unknown) => void
  let stopped = false
  key({ key: 'Escape', stopPropagation: () => { stopped = true } })
  expect([open.value, pinned.value, stopped, focused]).toEqual([false, false, true, ['learn']])
  // A second press of Enter on a pinned Learn closes it; Esc with nothing open does nothing.
  ;(state.onClick as () => void)(); (state.onClick as () => void)()
  expect(open.value).toBe(false)
  key({ key: 'Escape', stopPropagation: () => { throw new Error('nothing to close') } })
  scope.stop()
})

// Risk: a refused save and a failed read stand together, and the save warning's veil hides the
// read error and its Retry. Both alerts and both retries must stay reachable, in either order, and
// neither alert nor its retry may take another place or size when the other failure comes or goes.
for (const lang of ['en', 'de'] as const) {
  it(`with a refused save and a failed read together, both alerts and both retries stay reachable (${lang})`, async () => {
    const labels = lang === 'de'
      ? { load: 'Erneut versuchen', save: 'Erneut speichern', failed: 'Die Lieferzahlen konnten nicht geladen werden.' }
      : { load: 'Retry', save: 'Save again', failed: 'Delivery numbers could not be loaded.' }
    vi.stubGlobal('document', { addEventListener() {}, removeEventListener() {} })
    vi.stubGlobal('window', { addEventListener() {}, removeEventListener() {}, innerWidth: 1440, innerHeight: 900 })
    let read: () => Promise<unknown> = async () => deliveryMetrics()
    let reload = async () => {}
    const failures = new Set<(key: string) => void>()
    const preferenceSaves = Vue.reactive({ saving: new Set<string>(), failed: new Set<string>() })
    const pref = { value: Vue.ref({}), ready: Promise.resolve(), save: () => {} }
    // A .vue module is imported through __importDefault, so it needs the ES module flag to be seen as a default export.
    const sfc = (component: Vue.Component) => ({ __esModule: true, default: component })
    const stub = sfc({ render: () => null })
    const DeliveryView = component('DeliveryView.vue', {
      vue: Vue,
      'vue-router': { useRoute: () => Vue.reactive({ query: {}, path: '/p/AEON/delivery', hash: '' }), useRouter: () => ({ replace: () => {} }) },
      '../AppIcon.vue': sfc({ props: ['name', 'size'], render: () => Vue.h('svg') }),
      '../work/ProjectTabs.vue': stub, './ExpertTile.vue': stub, './FlowView.vue': stub, './LevelSwitch.vue': stub,
      './SimpleNumbers.vue': stub, './TrendChart.vue': stub, './WindowSwitch.vue': stub,
      '../../lib/delivery': delivery, '../../lib/deliveryFlow': flow, '../../lib/deliveryFlowModes': flowModes, '../../lib/deliveryFlowText': flowWords, '../../lib/deliveryNumbersText': numbersText,
      // Flow is not under test here: it reads nothing, shows no banner and offers nothing to choose.
      '../../lib/useDeliveryFlow': { useDeliveryFlow: () => ({ status: Vue.ref('ready'), data: Vue.ref(null), example: Vue.ref(false), empty: Vue.ref(null), choices: Vue.ref([]), runId: Vue.ref(null), key: Vue.ref(''), truncated: Vue.ref(false), retry() {} }) },
      '../../lib/deliveryNumbers': { ...numbers, readDeliveryMetrics: () => read() },
      '../../lib/preferences': { onPreferenceFailure: (listener: (key: string) => void) => { failures.add(listener); return () => { failures.delete(listener) } }, preferenceSaves, usePreference: () => pref },
      '../../lib/usePolledData': { usePoller: (load: () => Promise<void>) => { reload = load; return { start: () => { void load() }, stop() {}, restart() {} } } },
      '../../directives/clipTip': { vClipTip: {} },
      '../../stores/profile': { useProfile: () => ({ profile: { locale: lang === 'de' ? 'de-AT' : 'en' }, load: async () => {} }) },
    })
    const app = renderer.createApp(DeliveryView, { project: { id: 'p1', routeKey: 'AEON', title: 'Aeon' } })
    const root = el('root'); apps.push(app); app.mount(root)
    const settle = async () => { for (let turn = 0; turn < 6; turn++) { await Promise.resolve(); await Vue.nextTick() } }
    await settle()
    const alert = (id: string) => all(root).find(node => node.props['data-testid'] === id)
    const veiled = (node: El | null): boolean => node ? /\bveiled\b/.test(String(node.props.class ?? '')) || veiled(node.parent) : false
    const retryOf = (id: string) => all(alert(id)!).find(node => node.tag === 'button')!
    // What decides an alert's place and size: its own classes and every container's (a veil only hides),
    // its retry's classes and tip. The browser spec measures the boxes; this keeps their inputs from depending on the other failure.
    const layoutOf = (id: string) => {
      const classes = (node: El | null): string[] => node ? [String(node.props.class ?? '').replace(/\bveiled\b/g, '').trim(), ...classes(node.parent)] : []
      return JSON.stringify({ alert: classes(alert(id)!), retry: [classes(retryOf(id))[0], retryOf(id).props['data-tip'] ?? null] })
    }
    const hiddenStatus = () => all(root).find(node => /\bdl-status\b/.test(String(node.props.class ?? '')))!
    hiddenStatus().getBoundingClientRect = () => ({ height: 40 })
    const refuseSave = async () => { for (const listener of failures) listener(numbers.DELIVERY_PREFS_KEY); await settle() }
    const failRead = async () => { read = async () => { throw new Error('boom') }; await reload(); await settle() }
    const bothReachable = () => {
      for (const [id, label] of [['delivery-pref-error', labels.save], ['delivery-load-error', labels.load]] as const) {
        expect(alert(id), id).toBeDefined()
        expect(veiled(alert(id)!), `${id} is not hidden by a veil`).toBe(false)
        expect(textOf(retryOf(id)), `${id} retry`).toBe(label)
      }
      expect(textOf(alert('delivery-load-error')!)).toContain(labels.failed)
    }

    // The save is refused first, then the read fails.
    expect(alert('delivery-load-error')).toBeUndefined()
    await refuseSave()
    expect(alert('delivery-pref-error')).toBeDefined()
    const saveAlone = layoutOf('delivery-pref-error')
    await failRead()
    bothReachable()
    expect(layoutOf('delivery-pref-error'), 'the save alert and retry keep their place when the read fails').toBe(saveAlone)
    const readWithSave = layoutOf('delivery-load-error')
    // The read retry works alone: the save warning stays, reachable and in place.
    read = async () => deliveryMetrics()
    ;(retryOf('delivery-load-error').props.onClick as () => void)(); await settle()
    expect(alert('delivery-load-error')).toBeUndefined()
    expect(veiled(alert('delivery-pref-error')!)).toBe(false)
    expect(layoutOf('delivery-pref-error'), 'the save alert and retry keep their place when the read recovers').toBe(saveAlone)
    // The save retry works alone: the read error stays, reachable and in place.
    await failRead()
    ;(retryOf('delivery-pref-error').props.onClick as () => void)(); await settle()
    expect(alert('delivery-pref-error')).toBeUndefined()
    expect(veiled(alert('delivery-load-error')!)).toBe(false)
    expect(layoutOf('delivery-load-error'), 'the read alert and retry keep their place when the save recovers').toBe(readWithSave)

    // The read fails first, then the save is refused; the save retry works alone.
    expect(veiled(alert('delivery-load-error')!)).toBe(false)
    preferenceSaves.failed.add('me/delivery:numbers')
    await refuseSave()
    bothReachable()
    expect(layoutOf('delivery-load-error'), 'the read alert and retry keep their place when the save is refused').toBe(readWithSave)
    expect(layoutOf('delivery-pref-error'), 'the save alert and retry take the same place whichever failure came first').toBe(saveAlone)
    preferenceSaves.failed.clear()
    ;(retryOf('delivery-pref-error').props.onClick as () => void)(); await settle()
    expect(alert('delivery-pref-error')).toBeUndefined()
    expect(veiled(alert('delivery-load-error')!)).toBe(false)
    expect(textOf(retryOf('delivery-load-error'))).toBe(labels.load)
  })
}
