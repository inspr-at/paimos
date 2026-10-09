// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1006 risks: recorded steps land in the wrong lane or say more than their facts; an
// open wait ends at "now" and the lane reads idle; a live update throws away the person's
// time; Replay auto-plays more than once a session or under reduced motion, or plays at
// the wrong speed; Compare is not aligned at step a or loses one of its two sets; the
// moment panel and the in-flight table say something the data does not.
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as flow from '../src/lib/deliveryFlow'
import { createTimeline, criticalPath, laneModel, refreshTimeline, resetTimeline, runEnd, type FlowData } from '../src/lib/deliveryFlow'
import { arionTarget, ARION_MINUTES, compareData, liveData, recordedRuns, replayData, type ApiFlow } from '../src/lib/deliveryFlowData'
import { EXAMPLE_NOW, exampleCompare, exampleLive, exampleReplay } from '../src/lib/deliveryFlowExample'
import * as modes from '../src/lib/deliveryFlowModes'
import { autoplayOnce, createPlayer, flightRows, momentOf, PLAY_MS, wentOf, type Frames } from '../src/lib/deliveryFlowModes'
import * as words from '../src/lib/deliveryFlowText'
import { flowText } from '../src/lib/deliveryFlowText'

const en = flowText('en')
const T0 = Date.parse('2026-10-08T18:00:00Z')
const at = (minutes: number) => new Date(T0 + minutes * 60_000).toISOString()
const actor = (type: 'person' | 'agent' | 'ci' | 'queue', label: string) => ({ type, principal_id: null, label, model: null })
const norm = (p50: number | null = null) => ({ p50_min: p50, p90_min: null, arion_min: null })
function answer(): ApiFlow {
  const item = (id: string, kind: 'release' | 'change', ref: string, ended: number | null, eta: number | null) => ({
    id, kind, ref, title: kind === 'release' ? '' : 'CI fix', prs: [], started_at: at(0), ended_at: ended == null ? null : at(ended), pct_done: 50, current_step_id: null,
    eta: { p50_at: eta == null ? null : at(eta), p90_at: eta == null ? null : at(eta + 10), basis: eta == null ? 'none' as const : 'history' as const, reason: eta == null ? 'too little history' : null },
    target: kind === 'release' ? { minutes: 24, from_step: 'a', source: 'Arion' } : null, next_human_gate: kind === 'release' ? { principal_id: null, what: 'agm1 GO' } : null,
  })
  const step = (n: number, item: string, key: string, kind: 'work' | 'wait' | 'rework' | 'recovery', who: ReturnType<typeof actor>, from: number, to: number | null, extra: Record<string, unknown> = {}) => ({
    id: `s${n}`, item_id: item, step_key: key, round: 1, kind, actor: who, started_at: at(from), ended_at: to == null ? null : at(to),
    outcome: null, wait_reason: null, waits_for: null, side: false, source: 'paimos' as const, norm: norm(), ...extra,
  })
  return {
    project_id: 'p', now: at(60), at: at(60), from: at(0), to: at(60), truncated: false,
    items: [item('r', 'release', '126', null, 80), item('c', 'change', 'AEON-991', null, 75), item('h', 'change', 'AEON-983', null, null)],
    steps: [
      step(1, 'r', 'a', 'work', actor('queue', 'Merge queue'), 10, 15, { outcome: 'ok' }),
      step(2, 'r', 'k', 'work', actor('agent', 'OPS'), 15, 30),
      step(3, 'r', 'mitigation', 'recovery', actor('agent', 'OPS'), 30, 40),
      step(4, 'r', 'ci', 'work', actor('ci', 'Checks'), 40, null, { norm: norm(8) }),
      step(5, 'r', 'hold', 'wait', actor('person', 'Markus'), 90, 100, { wait_reason: 'human_gate' }),
      step(6, 'c', 'review', 'work', actor('agent', 'Reviewer'), 20, 35, { outcome: 'changes', round: 2 }),
      step(7, 'c', 'build', 'rework', actor('agent', 'Builder'), 35, null),
      step(8, 'h', 'hold', 'wait', actor('agent', 'LEAD'), 30, null, { wait_reason: 'dependency', waits_for: 'c' }),
    ],
    incidents: [{ id: 'i', item_id: 'r', started_at: at(28), ended_at: null, severity: 'degraded', summary: 'ready 503', recovery_step_ids: ['s3'] }],
  }
}

it('recorded steps become lanes and words from their facts; open steps run on to their expected end in Live', () => {
  const rec = recordedRuns(answer(), { extendOpen: true })
  const [release, change, held] = ['r', 'c', 'h'].map(id => rec.runs.find(r => r.id === id)!)
  expect(release!.steps.map(s => s.lane)).toEqual(['ci', 'ops', 'ops', 'ci', 'you'])
  expect(release!.tag).toBe('126'); expect(release!.title.en).toBe('Release 126'); expect(change!.tag).toBe('991'); expect(change!.title.en).toBe('AEON-991 · CI fix')
  const minutes = (iso: string) => (Date.parse(iso) - rec.origin) / 60_000
  const now = minutes(at(60))
  expect(rec.now).toBe(now)
  // The open check runs its usual 8 min, but at least a minute past now; the held change waits until the awaited run's estimate.
  expect(release!.steps[3]!.end).toBe(now + 1)
  expect(held!.steps[0]!.end).toBe(minutes(at(75)))
  expect(held!.steps[0]!.simple.en).toBe('Waiting for AEON-991 to land first')
  expect(change!.steps[0]!.expert.en).toBe('Review r2 · changes')
  expect(change!.steps[1]!.simple.en).toBe('Fixing it')
  // A recovery step is part of the incident; a step after the run's end is after the run only once it ended.
  expect(release!.steps[2]!.incident).toBe(true)
  expect(release!.incident!.open).toBe(true)
  expect(release!.steps[4]!.after).toBeUndefined()
  expect(release!.target).toEqual({ start: minutes(at(10)), minutes: 24 })
  expect(release!.facts!.gate).toBe('agm1 GO')
  // Replay ends an open step at now, never past it.
  const replay = recordedRuns(answer(), { extendOpen: false })
  expect(replay.runs.find(r => r.id === 'r')!.steps[3]!.end).toBe(now)
  // Live: releases first, then the soonest estimate; runs without one last.
  expect(liveData(rec)!.sets[0]!.runs.map(r => r.id)).toEqual(['r', 'c', 'h'])
})

it('Compare races a release from its step a against the Arion target on one relative axis', () => {
  const data = compareData(recordedRuns(answer(), { extendOpen: false }).runs[0]!, arionTarget('en'))!
  expect(data.origin).toBeNull()
  expect(data.sets).toHaveLength(2)
  expect(data.sets.map(s => s.title?.en)).toEqual(['Release 126 (a → l)', 'Arion target'])
  expect(data.sets[0]!.lanes.slice(0, 3)).toEqual(['review', 'ops', 'ci'])
  expect(criticalPath(data.sets[0]!.main)[0]!.start).toBe(0)
  expect(runEnd(data.sets[1]!.main)).toBeCloseTo(ARION_MINUTES)
  expect(ARION_MINUTES).toBe(24)
  expect(arionTarget('de', 12).steps.at(-1)!.end).toBeCloseTo(12)
  // A run without a step a cannot race.
  expect(compareData(recordedRuns(answer(), { extendOpen: false }).runs[1]!, arionTarget('en'))).toBeNull()
})

it('the moment panel names the active lanes, one idle line, the incident and the selected step', () => {
  const data = exampleLive(), sets = laneModel(data)
  const m = momentOf({ data, sets, T: EXAMPLE_NOW, selected: null, level: 'simple', lang: 'en', text: en })
  expect(m.head).toBe('At 20:25 (now)')
  expect(m.sentence).toMatch(/^Release 126 is live but not working properly \(since 20:14\); /)
  expect(m.sentence).toMatch(/nothing waiting on you\.$/)
  expect(m.lines.map(l => l.lane)).toEqual(['lead', 'ci'])
  expect(m.lines.find(l => l.lane === 'lead')!.label).toBe('983–986 · Waiting for AEON-991 to land first')
  expect(m.idle).toBe('OPS (agent), Reviewer (agent), Builder (agent) · nothing waiting on you')
  expect(m.incident).toEqual({ title: 'Live, but not working properly', meta: 'since 20:14:31 · 10 min so far' })
  const step = sets[0]!.items.find(i => i.step.expert.en === 'Review ×4 · ok')!
  const picked = momentOf({ data, sets, T: EXAMPLE_NOW, selected: step, level: 'expert', lang: 'en', text: en })
  expect(picked.detail!.title).toBe('Review ×4 · ok')
  expect(picked.detail!.rows).toContainEqual(['Start → end', '19:50 → 20:14'])
  expect(picked.detail!.rows).toContainEqual(['Duration', '24 min · p50 13 · Arion 8'])
  expect(picked.incident).toBeNull()
  // In the past the sentence says "was".
  expect(momentOf({ data, sets, T: EXAMPLE_NOW - 5, selected: null, level: 'simple', lang: 'en', text: en }).sentence).toMatch(/^Release 126 was live but not working properly/)
})

it('the in-flight table gives each run its step, wait, estimate and a verdict with shape and word', () => {
  const rows = flightRows({ data: exampleLive(), lang: 'en', text: en })
  expect(rows.map(r => r.tag)).toEqual(['126', '991', '983–986', '993'])
  const release = rows[0]!
  expect(release.waiting).toEqual({ text: 'the checks, then you: agm1 GO', you: true })
  expect(release.expected).toBe('healthy ~20:35')
  expect(release.verdict).toEqual({ level: 'far', word: 'Far off', text: 'about 3× the target' })
  expect(release.etaX).toBe('20:35 · 20:45')
  expect(rows[2]!.waiting).toEqual({ text: 'AEON-991', you: false })
  expect(rows[3]!.expected).toBe('no reliable estimate yet')
  expect(rows[3]!.etaX).toBe('none: first build on this model')
})

it('where the time went splits the critical path and lists the incident and the biggest waits', () => {
  const data = exampleReplay('r126')!
  const went = wentOf(data.sets[0]!.main, { level: 'expert', lang: 'en', text: en })
  expect(went.parts.map(p => p.kind)).toEqual(['work', 'wait', 'rework', 'inc'])
  expect(went.parts.reduce((n, p) => n + p.share, 0)).toBeCloseTo(1)
  expect(went.items[0]).toMatchObject({ kind: 'inc', minutes: '20 min' })
  expect(went.items.filter(i => i.note === 'alongside').length).toBeGreaterThan(0)
})

it('the player walks the whole run in 60 s at 1x and 30 s at 2x, and stops at the end', () => {
  const queue: ((ts: number) => void)[] = []
  const frames: Frames = { request: cb => queue.push(cb), cancel: () => { queue.length = 0 } }
  const run = (ms: number, t0: number) => { let ts = t0; while (queue.length && ts - t0 < ms) { ts += 50; queue.shift()!(ts) } return ts }
  for (const [speed, ms] of [[1, PLAY_MS], [2, PLAY_MS / 2]] as const) {
    const t = createTimeline(), data = exampleReplay('r126')!
    resetTimeline(t, data, { narrow: false })
    const player = createPlayer(t, frames)
    player.state.speed = speed
    player.play()
    run(ms / 2, 0)
    expect((t.T - t.p0) / (t.p1 - t.p0)).toBeCloseTo(0.5, 1)
    expect(player.state.playing).toBe(true)
    run(ms, 1e6)
    expect(t.T).toBe(t.p1)
    expect(player.state.playing).toBe(false)
  }
})

it('auto-play is granted once per session', () => {
  const store = new Map<string, string>()
  const storage = { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => { store.set(k, v) } }
  const first = autoplayOnce(storage)
  expect(first()).toBe(true)
  expect(first()).toBe(false)
  expect(autoplayOnce(storage)()).toBe(false)
})

it('a live update keeps the person viewing the past, and moves the playhead with now while following', () => {
  const t = createTimeline(), data = exampleLive()
  resetTimeline(t, data, { narrow: false })
  const later: FlowData = { ...data, now: data.now! + 1 }
  refreshTimeline(t, later)
  expect(t.T).toBe(later.now)
  t.follow = false; t.T = later.now! - 20
  refreshTimeline(t, { ...later, now: later.now! + 1 })
  expect(t.T).toBe(later.now! - 20)
})

// ---------- Component: FlowView with its children stubbed ----------
type Node = { tag: string; text: string; props: Record<string, unknown>; children: Node[]; parent: Node | null }
const node = (tag: string, text = ''): Node => ({ tag, text, props: {}, children: [], parent: null })
function detach(el: Node) { if (el.parent) el.parent.children.splice(el.parent.children.indexOf(el), 1); el.parent = null }
const renderer = Vue.createRenderer<Node, Node>({
  createElement: tag => node(tag), createText: text => node('#text', text), createComment: () => node('#comment'),
  setText: (el, text) => { el.text = text }, setElementText: (el, text) => { el.children = []; el.text = text },
  parentNode: el => el.parent, nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1] ?? null,
  patchProp: (el, key, _old, value) => { el.props[key] = value }, remove: detach,
  insert: (child, parent, anchor) => {
    detach(child); child.parent = parent
    const i = anchor ? parent.children.indexOf(anchor) : -1
    if (i < 0) parent.children.push(child); else parent.children.splice(i, 0, child)
  },
})
const all = (el: Node): Node[] => [el, ...el.children.flatMap(all)]
const textOf = (el: Node): string => el.text + el.children.map(textOf).join('')
const apps: Vue.App[] = []
let media = { matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }
beforeEach(() => {
  vi.useFakeTimers()
  media = { matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }
  vi.stubGlobal('window', { matchMedia: () => media })
  vi.stubGlobal('requestAnimationFrame', (cb: () => void) => setTimeout(cb, 0))
})
afterEach(() => { for (const app of apps.splice(0)) app.unmount(); vi.useRealTimers(); vi.unstubAllGlobals() })

let compiled: Vue.Component | null = null
function flowView(): Vue.Component {
  if (compiled) return compiled
  const stub = (tag: string) => ({ __esModule: true, default: { inheritAttrs: true, render: () => Vue.h(tag) } })
  const modules: Record<string, unknown> = {
    vue: Vue, '../AppIcon.vue': stub('icon'), './FlowLanes.vue': stub('lanes'), './FlowOverview.vue': stub('overview'),
    './InFlightTable.vue': stub('inflight'), './MomentPanel.vue': stub('moment'), './TimeWent.vue': stub('went'),
    '../../lib/deliveryFlow': flow, '../../lib/deliveryFlowModes': modes, '../../lib/deliveryFlowText': words,
  }
  const source = readFileSync(new URL('../src/components/delivery/FlowView.vue', import.meta.url), 'utf8')
  const { content } = compileScript(parse(source).descriptor, { id: 'flow-view-test', inlineTemplate: true })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: Vue.Component } = {}
  new Function('require', 'exports', outputText)((id: string) => { if (!(id in modules)) throw new Error(`Unexpected dependency ${id}`); return modules[id] }, exports)
  return compiled = exports.default!
}
function mount(props: Record<string, unknown>) {
  const state = Vue.reactive({ status: 'ready', empty: null, choices: [], choice: null, level: 'simple', lang: 'en', ...props })
  const app = renderer.createApp({ render: () => Vue.h(flowView(), state) })
  const root = node('root'); apps.push(app); app.mount(root)
  const byTest = (id: string) => all(root).find(el => el.props['data-testid'] === id)
  const stubNode = (tag: string) => all(root).find(el => el.tag === tag)!
  return {
    state, byTest, stubNode,
    chip: () => textOf(byTest('flow-chip')!.children.find(c => c.props.class === 'stack')!.children[0]!),
    follow: () => byTest('flow-follow')!,
    timeline: () => stubNode('lanes').props.timeline as flow.Timeline,
  }
}

it('Live follows now; a playhead drag shows "Viewing" and "Back to now"; a live update keeps the view', async () => {
  const data = exampleLive()
  const view = mount({ data, dataKey: 'live', mode: 'live' })
  await Vue.nextTick()
  expect(view.chip()).toBe('Live · now 20:25')
  expect(view.follow().props.disabled).toBe(true)
  // The lanes move the time the way a playhead drag does.
  const t = view.timeline()
  t.follow = false; t.T = EXAMPLE_NOW - 20
  await Vue.nextTick()
  expect(view.chip()).toBe('Viewing 20:05')
  expect(view.follow().props.disabled).toBe(false)
  expect(textOf(view.follow())).toContain('Back to now')
  // New live data for the same view keeps the time the person is looking at.
  view.state.data = { ...data, now: EXAMPLE_NOW + 1 }
  await Vue.nextTick(); await Vue.nextTick()
  expect(view.chip()).toBe('Viewing 20:05')
  ;(view.follow().props.onClick as () => void)()
  await Vue.nextTick()
  expect(view.chip()).toBe('Live · now 20:26')
  expect(view.follow().props.disabled).toBe(true)
  // The moment panel speaks about the moment shown.
  expect((view.stubNode('moment').props.moment as modes.Moment).head).toBe('At 20:26 (now)')
})

it('Replay auto-plays once per session and never with reduced motion', async () => {
  const queue: ((ts: number) => void)[] = []
  const frames: Frames = { request: cb => queue.push(cb), cancel: () => { queue.length = 0 } }
  const store = new Map<string, string>()
  const once = autoplayOnce({ getItem: k => store.get(k) ?? null, setItem: (k, v) => { store.set(k, v) } })
  const first = mount({ data: exampleReplay('r126'), dataKey: 'replay|r126', mode: 'replay', autoplay: once, frames })
  await Vue.nextTick()
  const play = () => first.byTest('flow-play')!
  expect(play().props['aria-label']).toBe('Play')
  vi.advanceTimersByTime(400)
  await Vue.nextTick()
  expect(play().props['aria-label']).toBe('Pause')
  const t = first.timeline(), start = t.T
  for (let ts = 0; ts < 3000 && queue.length; ts += 50) queue.shift()!(ts)
  expect(t.T).toBeGreaterThan(start)
  ;(play().props.onClick as () => void)()
  await Vue.nextTick()
  expect(play().props['aria-label']).toBe('Play')
  // A second replay in the same session waits for the person.
  const again = mount({ data: exampleReplay('c991'), dataKey: 'replay|c991', mode: 'replay', autoplay: once, frames })
  vi.advanceTimersByTime(1000)
  await Vue.nextTick()
  expect(again.byTest('flow-play')!.props['aria-label']).toBe('Play')

  // Reduced motion: no auto-play, play disabled, the run shown statically from its start.
  media.matches = true
  const still = mount({ data: exampleReplay('r126'), dataKey: 'replay|r126|rm', mode: 'replay', autoplay: autoplayOnce(null), frames })
  await Vue.nextTick()
  vi.advanceTimersByTime(2000)
  await Vue.nextTick()
  expect(still.byTest('flow-play')!.props.disabled).toBe(true)
  expect(textOf(still.byTest('flow-rm')!)).toBe('Reduced motion: drag the time handle to step through.')
  expect(still.timeline().T).toBe(still.timeline().p0)
})

it('Compare shows two lane sets on one relative axis and says when the target was live', async () => {
  const view = mount({ data: exampleCompare('en'), dataKey: 'compare|r126', mode: 'compare' })
  await Vue.nextTick()
  const sets = view.stubNode('lanes').props.sets as flow.LaneSet[]
  expect(sets.map(s => s.title?.en)).toEqual(['Release 126 (a → l)', 'Arion target'])
  expect(textOf(view.byTest('flow-head')!)).toMatch(/^Release 126 vs\. the Arion target, queue to live: 1 h 10 instead of 24 min/)
  const t = view.timeline()
  expect([t.v0, t.v1]).toEqual([t.r0, t.r1])
  expect(textOf(view.byTest('flow-clock')!)).toBe('+0 min')
  t.follow = false; t.T = 30
  await Vue.nextTick()
  const moment = view.stubNode('moment').props.moment as modes.Moment
  expect(moment.sentence).toMatch(/the Arion target was live after 24 min\.$/)
  expect(moment.lines.find(l => l.kind === 'done')!.label).toBe('live after +24 min')
  expect(textOf(view.byTest('flow-clock')!)).toBe('+30 min · target reached')
  expect((view.stubNode('went').props.runs as modes.Went[]).map(w => w.title)).toEqual(['Release 126 (a → l)', 'Arion target'])
})

it('the example replays release 126 final and the changes so far', () => {
  const release = exampleReplay('r126')!, change = exampleReplay('c991')!
  expect(release.now).toBeNull()
  expect(release.play[1]).toBeCloseTo(20 * 60 + 34 + 1 / 60)
  expect(Math.max(...change.sets[0]!.main.steps.map(s => s.end))).toBe(EXAMPLE_NOW)
  expect(replayData(change.sets[0]!.main, change.origin!, EXAMPLE_NOW)!.play[1]).toBe(EXAMPLE_NOW)
})
