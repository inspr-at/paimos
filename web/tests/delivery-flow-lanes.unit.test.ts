// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1005 risks: zooming re-packs rows so lanes jump; a label overflows its step or
// loses "since"; slivers vanish or overlap instead of merging into "+N"; the wheel,
// keys or a follow tick move the time when they should move only the window.
import { expect, it } from 'vitest'
import {
  CHAR_W, createTimeline, criticalPath, fitAll, fitLabel, following, followTick, keyInput, laneFrame, laneModel, laneRows, panBy, placeIncidentCaption, placeLane, placeOverviewTargets, resetTimeline, ROW_CAP, rowPieces, setTime, stepAt, timeLabel, wheelInput, zoomPreset, zoomTo,
  type FlowData, type FlowRun, type FlowStep, type Lane, type LaneSet, type OverviewHit,
} from '../src/lib/deliveryFlow'
import { exampleLive, EXAMPLE_NOW } from './delivery-flow-example-fixtures'
import { hintParts, flowText } from '../src/lib/deliveryFlowText'
import { liveData, recordedRuns } from '../src/lib/deliveryFlowData'
import { liveHarnessFlow } from './delivery-flow-live-fixtures'
import { currentStep, momentOf } from '../src/lib/deliveryFlowModes'

const words = (en: string) => ({ en, de: en })
const step = (start: number, end: number, lane: Lane = 'ops', kind: FlowStep['kind'] = 'work'): FlowStep => ({ start, end, lane, kind, expert: words(`s${start}`), simple: words(`s${start}`) })
const data = (steps: FlowStep[], now: number | null = null): FlowData => {
  const run: FlowRun = { id: 'r', tag: '1', title: words('Run'), steps }
  return { origin: new Date(2026, 9, 8).getTime(), now, range: [0, 200], play: [10, 190], sets: [{ runs: [run], lanes: ['ops', 'ci'], main: run }] }
}

/** Two steps share a row only one after the other: never two bars on top of each other. */
function overlaps(set: LaneSet): string[] {
  const out: string[] = []
  for (const a of set.items) for (const b of set.items) {
    if (a === b || a.lane !== b.lane || a.row !== b.row) continue
    if (a.step.start < b.step.end - 1e-6 && b.step.start < a.step.end - 1e-6) out.push(`${a.lane}:${a.row} ${a.step.start}-${a.step.end} × ${b.step.start}-${b.step.end}`)
  }
  return out
}

it('rows are reserved once per data set: each step takes the first free row and a lane grows to every row it needs', () => {
  const [set] = laneModel(data([step(0, 30), step(10, 20), step(20, 40), step(25, 26), step(25, 27), step(25, 28), step(50, 60), step(0, 5, 'ci')]))
  // Five overlap at 25: the fifth gets a fifth row instead of stacking in the fourth (AEON-1656).
  expect(set!.rows).toEqual({ ops: 5, ci: 1 })
  const rowOf = (start: number, end: number) => set!.items.find(i => i.step.start === start && i.step.end === end)!.row
  expect([rowOf(0, 30), rowOf(10, 20), rowOf(20, 40), rowOf(25, 26), rowOf(25, 27), rowOf(25, 28), rowOf(50, 60)]).toEqual([0, 1, 1, 2, 3, 4, 0])
  expect(overlaps(set!)).toEqual([])
  // The example keeps its six lanes; the model does not depend on the window.
  const example = laneModel(exampleLive())[0]!
  expect(example.lanes).toEqual(['you', 'lead', 'ops', 'review', 'build', 'ci'])
  expect(overlaps(example)).toEqual([])
})

it('ten concurrent builders take ten rows; above twelve a lane folds into "+N more" until a person expands it', () => {
  // AEON-1697: the wave-A intervals, including open current attempts,
  // must survive the API projection → Live → packing path, not just fake steps.
  const answer = liveHarnessFlow()
  const live = liveData(recordedRuns(answer, { extendOpen: true }))!
  expect(live.sets[0]!.runs).toHaveLength(10)
  const [recorded] = laneModel(live)
  expect(recorded!.items).toHaveLength(21)
  expect(recorded!.rows.build).toBe(10)
  expect(new Set(recorded!.items.map(item => item.row)).size).toBe(10)
  expect(overlaps(recorded!)).toEqual([])
  expect(laneRows(recorded!, 'build', false)).toEqual({ bars: 10, fold: null, total: 10 })
  expect(laneRows(recorded!, 'build', true)).toEqual({ bars: 10, fold: null, total: 10 })
  // An unfinished current step cannot end before the playhead while the headline
  // still calls it running. The moment and avatar must point at that open step.
  expect(answer.steps.filter(step => step.ended_at == null)).toHaveLength(10)
  for (const item of answer.items) {
    expect(answer.steps.find(step => step.id === item.current_step_id)).toMatchObject({ item_id: item.id, ended_at: null })
  }
  const now = live.now!, main = recorded!.main
  const current = currentStep(main, now)!
  expect(main.tag).toBe('1674')
  expect(current.facts).toMatchObject({ id: main.facts!.currentStepId, open: true, round: 2 })
  expect(current.end).toBeGreaterThan(now)
  const avatarStep = stepAt(criticalPath(main), now)!
  expect(avatarStep).toBe(current)
  expect(recorded!.items.find(item => item.step === avatarStep)).toMatchObject({ run: main, lane: 'build' })
  const moment = momentOf({ data: live, sets: [recorded!], T: now, selected: null, level: 'expert', lang: 'en', text: flowText('en') })
  expect(moment.lines.find(line => line.lane === 'build')).toMatchObject({ who: 'Builder', kind: 'work', label: '1674 · Build r2 · gpt-6.1-sol', more: 9 })
  expect(moment.sentence).toContain('Builder on “Being built”')
  expect(moment.idle).not.toContain('Builder')
  const builders = (n: number) => Array.from({ length: n }, (_, k) => step(10 + k, 60 + k, 'build'))
  const lanes = (steps: FlowStep[]): FlowData => { const d = data(steps); d.sets[0]!.lanes = ['build', 'ci']; return d }
  const [ten] = laneModel(lanes(builders(10)))
  expect(ten!.rows.build).toBe(10)
  expect(new Set(ten!.items.map(i => i.row))).toEqual(new Set([0, 1, 2, 3, 4, 5, 6, 7, 8, 9]))
  expect(overlaps(ten!)).toEqual([])
  expect(laneRows(ten!, 'build', false)).toEqual({ bars: 10, fold: null, total: 10 })
  // Exactly at the cap every row still shows.
  const [twelve] = laneModel(lanes(builders(ROW_CAP)))
  expect(laneRows(twelve!, 'build', false)).toEqual({ bars: ROW_CAP, fold: null, total: ROW_CAP })
  // Sixteen: eleven rows of bars and one "+5 more" row; expanded, all sixteen and a "fewer" row.
  const [sixteen] = laneModel(lanes([...builders(16), step(70, 80, 'build')]))
  expect(sixteen!.rows.build).toBe(16)
  expect(overlaps(sixteen!)).toEqual([])
  const folded = laneRows(sixteen!, 'build', false)
  expect([folded.bars, folded.total, folded.fold?.kind]).toEqual([ROW_CAP - 1, ROW_CAP, 'more'])
  expect(folded.fold!.hidden.map(i => i.row)).toEqual([11, 12, 13, 14, 15])
  const open = laneRows(sixteen!, 'build', true)
  expect([open.bars, open.total, open.fold?.kind, open.fold?.hidden.length]).toEqual([16, 17, 'fewer', 0])
  // A lane within the cap ignores the expansion.
  expect(laneRows(sixteen!, 'ci', true)).toEqual({ bars: 1, fold: null, total: 1 })
})

it('a live update grows lanes below the pointer only: nothing above it moves until the pointer has left', () => {
  const rowH = 20, gaps = 4
  // Without a pointer every lane is its rows tall, at the cursor.
  expect(placeLane({ y: 18, h: 44, rowH }, 3, 18, rowH, gaps, null)).toEqual({ y: 18, h: 64, rowH })
  // A lane that ended above the pointer keeps its height and fits a new row inside.
  const above = placeLane({ y: 18, h: 44, rowH }, 3, 18, rowH, gaps, 200)
  expect(above.y).toBe(18)
  expect(above.h).toBe(44)
  expect(above.rowH * 3 + gaps).toBeCloseTo(44)
  // Fewer rows above the pointer: rows return to full height inside the same lane.
  expect(placeLane({ y: 18, h: 44, rowH: 13.33 }, 1, 18, rowH, gaps, 200)).toEqual({ y: 18, h: 44, rowH })
  // The lane under the pointer keeps its top and row height and grows downward.
  expect(placeLane({ y: 62, h: 44, rowH }, 4, 62, rowH, gaps, 70)).toEqual({ y: 62, h: 84, rowH })
  // A lane below the pointer follows at its natural height.
  expect(placeLane({ y: 106, h: 24, rowH }, 2, 146, rowH, gaps, 70)).toEqual({ y: 146, h: 44, rowH })
  // A lane drawn for the first time, or one whose top has moved, is placed naturally.
  expect(placeLane(undefined, 2, 18, rowH, gaps, 200)).toEqual({ y: 18, h: 44, rowH })
  expect(placeLane({ y: 30, h: 44, rowH }, 3, 18, rowH, gaps, 200)).toEqual({ y: 18, h: 64, rowH })
})

it('a label falls back from tag and text, to the text, to the duration, to an ellipsis that keeps "since"', () => {
  // AEON-1698 changes tagged bars deliberately: the key is dropped last. These
  // fallbacks still apply to a single-run bar, which has no tag to preserve.
  const base = { text: 'Fix being checked', pad: 6 }
  expect(fitLabel({ ...base, tag: '126', room: 400 })).toBe('126 · Fix being checked')
  expect(fitLabel({ ...base, room: 120 })).toBe('Fix being checked')
  expect(fitLabel({ ...base, text: 'Waiting for a reviewer', duration: '14 min', pad: 16, room: 70 })).toBe('14 min')
  // A cut wait keeps "since" when the text no longer fits. 160 px holds
  // "14 min · since 19:36" (140 px) and must not fall back to "14 min".
  expect(fitLabel({ ...base, text: 'Waiting for a reviewer', since: ' · since 19:36', duration: '14 min', pad: 16, room: 160 })).toBe('14 min · since 19:36')
  expect(fitLabel({ ...base, text: 'Fixing the notes', since: ' · since 18:33', duration: '18 min', pad: 16, room: 160 })).toBe('18 min · since 18:33')
  expect(fitLabel({ ...base, text: 'Waiting for a reviewer', since: ' · since 19:36', duration: '14 min', pad: 16, room: 100 })).toBe('')
  const cut = fitLabel({ ...base, tag: '126', text: 'Being built, a bit longer than usual', since: ' · since 19:47', room: 200 })
  expect(cut).toMatch(/^126 · Being.*… · since 19:47$/)
  expect(cut.length * 5.9 + 6).toBeLessThan(200)
  expect(fitLabel({ ...base, room: 40 })).toBe('')
  const answer = liveHarnessFlow()
  const run = recordedRuns(answer, { extendOpen: true }).runs.find(run => run.tag === '1676')!
  const text = run.steps.find(step => step.facts?.round === 3)!.expert.en
  expect(text).toBe('Build r3 · gpt-6.1-sol')
  const input = { tag: run.tag, text, pad: 6 }
  // At the old row-1 width the description fitted alone and hid the run key.
  expect(fitLabel({ ...input, room: 160 })).toMatch(/^1676 · /)
  expect(fitLabel({ ...input, room: 110 })).toBe('1676 · Build r3…')
  expect(fitLabel({ ...input, room: 75 })).toBe('1676 · Bu…')
  expect(fitLabel({ ...input, room: 40 })).toBe('1676')
  const keyRoom = input.tag.length * CHAR_W + input.pad + 6
  expect(fitLabel({ ...input, room: keyRoom + 0.5 })).toBe('1676')
  expect(fitLabel({ ...input, room: keyRoom - 0.5 })).toBe('')
  // Every rendered label still fits, and even clipped waits identify their run.
  for (const tag of ['1676', '983–986']) for (const since of ['', ' · since 19:36']) for (const room of [30, 40, 75, 100, 140, 160, 400]) {
    const label = fitLabel({ tag, text, since, duration: '14 min', pad: 16, room })
    if (!label) continue
    expect(label.startsWith(tag)).toBe(true)
    expect(label.length * CHAR_W + 16 + 6).toBeLessThan(room)
  }
  expect(fitLabel({ tag: '983–986', text: 'Waiting for a reviewer', since: ' · since 19:36', duration: '14 min', pad: 16, room: 160 })).toBe('983–986 · since 19:36')
})

it('panning an incident onto the right edge keeps its caption inside a 320 px frame', () => {
  const data = exampleLive()
  const incident = data.sets[0]!.main.incident!
  const t = createTimeline()
  resetTimeline(t, data, { narrow: true })
  const { x0, x1 } = laneFrame(320)
  // The old clamp parked the text at x1 + 4 (318 in this frame) once the incident
  // sat on the right edge. Pan there, still keeping the incident on screen.
  panBy(t, incident.start - (t.v1 - t.v0) + 0.4 - t.v0)
  const X = (m: number) => x0 + (m - t.v0) / (t.v1 - t.v0) * (x1 - x0)
  const ix0 = X(incident.start)
  expect(incident.start).toBeLessThan(t.v1)
  expect(Math.min(Math.max(ix0 + 6, x0 + 4), x1 - 10) + 14).toBeCloseTo(318, 5)
  const label = `Live, but not working properly · since ${timeLabel(data, incident.start)}`
  expect(label).toBe('Live, but not working properly · since 20:14')
  const placed = placeIncidentCaption({ text: label, x0, x1, anchor: Math.max(ix0 + 6, x0 + 4) })
  const right = placed.x + (placed.text ? 14 + placed.text.length * CHAR_W : 10)
  expect(placed.x).toBeGreaterThanOrEqual(x0)
  expect(right).toBeLessThanOrEqual(x1)
  expect(placed.x + 14).toBeLessThanOrEqual(x1)
  expect(placed.full).toBe(label)
  expect(placed.text === label || placed.text.endsWith('…')).toBe(true)
  // A caption that fits stays on its incident instead of being pulled to the edge.
  const fitted = placeIncidentCaption({ text: 'Hi · since 20:14', x0, x1, anchor: 100 })
  expect(fitted).toMatchObject({ x: 100, text: 'Hi · since 20:14', full: 'Hi · since 20:14', healthyX: null })
  // A closed incident's healthy note is drawn only when it fits, and stays readable either way.
  const wide = placeIncidentCaption({ text: 'Recovered · 20 min', x0: 84, x1: 1200, anchor: 200, healthy: { text: 'healthy again', anchor: 400 } })
  expect(wide.healthyX).toBe(400)
  expect(wide.healthyX! + 13 + 'healthy again'.length * CHAR_W).toBeLessThanOrEqual(1200)
  const cramped = placeIncidentCaption({ text: label, x0, x1, anchor: ix0 + 6, healthy: { text: 'healthy again', anchor: x1 - 4 } })
  expect(cramped.healthyX).toBeNull()
  expect(cramped.full).toBe(`${label} · healthy again`)
  expect(cramped.x + 14 + cramped.text.length * CHAR_W).toBeLessThanOrEqual(x1)
})

it('a wider face than the 5.9 px estimate still ends the incident caption inside the frame', () => {
  // CI web-shard 11 (Linux Chromium, 368 px frame): the painted caption ended at
  // 378.6. CHAR_W 5.9 plus no outline said that string ended on the plot edge.
  const ink = (value: string) => value.length * 6.3 + 2
  const label = 'Live, but not working properly · since 20:14'
  const { x0, x1 } = laneFrame(368)
  const anchor = x1 - 28
  const placed = placeIncidentCaption({ text: label, x0, x1, anchor, textWidth: ink })
  const right = placed.x + (placed.text ? 14 + ink(placed.text) : 10)
  expect(placed.x).toBeGreaterThanOrEqual(x0)
  expect(right).toBeLessThanOrEqual(x1)
  expect(placed.full).toBe(label)
  expect(placed.text.endsWith('…')).toBe(true)
  expect(ink(label)).toBeGreaterThan(x1 - x0 - 14)
  const healthy = placeIncidentCaption({
    text: label, x0, x1, anchor, textWidth: ink, healthy: { text: 'healthy again', anchor: x1 - 4 },
  })
  expect(healthy.healthyX).toBeNull()
  expect(healthy.full).toBe(`${label} · healthy again`)
  expect(healthy.x + 14 + ink(healthy.text)).toBeLessThanOrEqual(x1)
})

it('slivers in a row merge into one "+N" cluster; a lone sliver stays a step', () => {
  const row = [step(0, 0.5), step(0.6, 1), step(1.1, 1.5), step(10, 20), step(30, 30.5)].map(s => ({ step: s }))
  const X = (m: number) => m * 4
  const pieces = rowPieces(row, X, 0, 400)
  expect(pieces.map(p => p.kind)).toEqual(['cluster', 'step', 'step'])
  const cluster = pieces[0]!
  expect(cluster.kind === 'cluster' && [cluster.items.length, cluster.from, cluster.to, cluster.w]).toEqual([3, 0, 1.5, 26])
  // Zoomed in, the same steps are wide enough to stand alone.
  expect(rowPieces(row, (m: number) => m * 40, 0, 4000).every(p => p.kind === 'step')).toBe(true)
  // Steps outside the window are left out.
  expect(rowPieces(row, X, 50, 100)).toHaveLength(1)
})

it('Live opens at 45 min with now at 65 % (20 on a phone) and presets zoom around now while following', () => {
  const t = createTimeline()
  resetTimeline(t, exampleLive(), { narrow: false })
  expect([t.T, t.v1 - t.v0, (EXAMPLE_NOW - t.v0) / (t.v1 - t.v0)]).toEqual([EXAMPLE_NOW, 45, 0.65])
  expect(timeLabel(exampleLive(), t.v0)).toBe('19:55')
  expect(zoomPreset(t)).toBeNull()
  zoomTo(t, 15, EXAMPLE_NOW, 0.65)
  expect(zoomPreset(t)).toBe(15)
  fitAll(t)
  expect(zoomPreset(t)).toBe('fit')
  resetTimeline(t, exampleLive(), { narrow: true })
  expect(t.v1 - t.v0).toBe(20)
  expect(timeLabel({ origin: null }, 14)).toBe('+14 min')
})

it('the wheel pans or zooms the window and never moves the time; a plain vertical wheel is left to the page', () => {
  const t = createTimeline()
  resetTimeline(t, data([step(0, 200)], 100), { narrow: false })
  const T = t.T, span = t.v1 - t.v0
  expect(wheelInput(t, { deltaX: 0, deltaY: 120, zoomKey: false, shiftKey: false, at: 90, minutesPerPx: 0.05 })).toBe(false)
  expect([t.v1 - t.v0, t.follow]).toEqual([span, true])
  // ⌘/Ctrl+wheel keeps the minute under the pointer where it is.
  const frac = (90 - t.v0) / span
  expect(wheelInput(t, { deltaX: 0, deltaY: -200, zoomKey: true, shiftKey: false, at: 90, minutesPerPx: 0.05 })).toBe(true)
  expect(t.v1 - t.v0).toBeLessThan(span)
  expect((90 - t.v0) / (t.v1 - t.v0)).toBeCloseTo(frac, 6)
  const v0 = t.v0
  expect(wheelInput(t, { deltaX: 100, deltaY: 10, zoomKey: false, shiftKey: false, at: 90, minutesPerPx: 0.05 })).toBe(true)
  expect(t.v0).toBeCloseTo(v0 + 5, 6)
  expect([t.T, t.follow]).toEqual([T, false])
})

it('Shift+←/→ move the time a minute, plain arrows pan, and Home/End jump within the run', () => {
  const t = createTimeline()
  resetTimeline(t, data([step(0, 200)], 100), { narrow: false })
  const v0 = t.v0
  expect(keyInput(t, 'ArrowRight', true)).toBe('time')
  expect([t.T, t.v0]).toEqual([101, v0])
  expect(keyInput(t, 'ArrowLeft', true)).toBe('time')
  expect(keyInput(t, 'ArrowLeft', true)).toBe('time')
  expect(t.T).toBe(99)
  expect(keyInput(t, 'ArrowRight', false)).toBe('window')
  expect([t.T, t.v0]).toEqual([99, v0 + 4.5])
  // Moving the time out of the window brings the window along.
  expect(keyInput(t, 'Home', false)).toBe('time')
  expect(t.T).toBe(10)
  expect(t.T >= t.v0 && t.T <= t.v1).toBe(true)
  expect(keyInput(t, 'Enter', false)).toBe('select')
  expect(keyInput(t, 'x', false)).toBeNull()
})

it('following: Live keeps now at 65 % until the playhead leaves now; a run keeps its playhead in view', () => {
  const live = createTimeline()
  resetTimeline(live, data([step(0, 200)], 100), { narrow: false })
  expect(following(live)).toBe(true)
  live.follow = false; setTime(live, 90)
  expect(following(live)).toBe(false)
  const replay = createTimeline()
  resetTimeline(replay, data([step(0, 200)]), { narrow: false })
  expect([replay.T, replay.v0]).toEqual([10, 6])
  replay.T = 40; followTick(replay)
  expect(40 >= replay.v0 && 40 <= replay.v0 + (replay.v1 - replay.v0) * 0.72).toBe(true)
  replay.follow = false
  const v0 = replay.v0
  replay.T = 120; followTick(replay)
  expect(replay.v0).toBe(v0)
})

it('a 15 min window at the phone overview edge keeps the three resize targets apart', () => {
  // 320 px overview, the whole example run (210 min). The review's right-end case
  // put both 44 px handles on x=232–276 and the playhead over both grips.
  const width = 320, span = 210, frame = laneFrame(width)
  const X = (minute: number) => frame.x0 + (minute / span) * (frame.x1 - frame.x0)
  const area = (a: OverviewHit, b: OverviewHit) => Math.max(0, Math.min(a.hx + a.hw, b.hx + b.hw) - Math.max(a.hx, b.hx))
  const ownsGrip = (handle: { hx: number; hw: number; gx: number }, play: OverviewHit) => {
    expect(handle.gx - 4).toBeGreaterThanOrEqual(handle.hx)
    expect(handle.gx + 4).toBeLessThanOrEqual(handle.hx + handle.hw)
    expect(handle.gx + 4 <= play.hx || handle.gx - 4 >= play.hx + play.hw).toBe(true)
  }
  const right = placeOverviewTargets({ width, left: X(span - 15), right: X(span), play: X(span), hit: 44, height: 64 })
  expect(right.handles.map(handle => handle.hx)).toEqual([188, 232])
  expect(right.play.hx).toBe(276)
  expect([right.handles[0]!.hw, right.handles[1]!.hw, right.play.hw]).toEqual([44, 44, 44])
  expect(area(right.handles[0]!, right.handles[1]!) + area(right.handles[0]!, right.play) + area(right.handles[1]!, right.play)).toBe(0)
  expect(right.play.hx + right.play.hw).toBe(width)
  for (const handle of right.handles) ownsGrip(handle, right.play)
  const left = placeOverviewTargets({ width, left: X(0), right: X(15), play: X(0), hit: 44, height: 64 })
  expect(area(left.handles[0]!, left.handles[1]!) + area(left.handles[0]!, left.play) + area(left.handles[1]!, left.play)).toBe(0)
  expect(left.handles[0]!.hx).toBeGreaterThanOrEqual(0)
  expect(left.handles[1]!.hx).toBeGreaterThanOrEqual(left.handles[0]!.hx + left.handles[0]!.hw)
  for (const handle of left.handles) ownsGrip(handle, left.play)
  // Fit all still puts the right handle on the screen edge and leaves both grips on the brush.
  const fit = placeOverviewTargets({ width, left: X(0), right: X(span), play: X(135), hit: 44, height: 64 })
  expect(fit.handles[1]!.hx + fit.handles[1]!.hw).toBe(width)
  expect(fit.handles[0]!.gx).toBeCloseTo(X(0), 5)
  expect(fit.handles[1]!.gx).toBeCloseTo(X(span), 5)
  expect(area(fit.handles[0]!, fit.handles[1]!) + area(fit.handles[0]!, fit.play) + area(fit.handles[1]!, fit.play)).toBe(0)
  const fine = placeOverviewTargets({ width: 1440, left: 400, right: 900, play: 700, hit: 0, height: 64 })
  expect(fine.handles.map(handle => [handle.gx, handle.hw])).toEqual([[400, 8], [900, 8]])
})

it('the hint draws its keys instead of typing arrow symbols, in both languages', () => {
  for (const lang of ['en', 'de'] as const) {
    const parts = hintParts(flowText(lang).hint)
    expect(parts.filter(p => 'key' in p).map(p => 'key' in p && p.key)).toEqual(['mod', 'left', 'right', 'left', 'right', 'up', 'down'])
    expect(parts.some(p => 'text' in p && /[←→↑↓⌘]/.test(p.text))).toBe(false)
  }
})
