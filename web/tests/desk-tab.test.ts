// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1473: the Decision Desk count in the browser tab. Risk: the tab shows a count
// the badge does not, moves when nothing new arrived, loops, or keeps moving under
// reduced motion. Time is an injected clock; nothing here sleeps.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createDeskTab, dotLayout, NEW_DECISION, NOTICE_MS, POP_FRAME_MS, POP_SCALES, tabCount, titlePrefix, type DeskReading } from '../src/lib/deskTab.ts'

function fakeClock() {
  let now = 0, seq = 0
  const timers = new Map<number, { at: number; run: () => void }>()
  return {
    setTimeout(run: () => void, ms: number) { const id = ++seq; timers.set(id, { at: now + ms, run }); return id },
    clearTimeout(id: unknown) { timers.delete(id as number) },
    pending: () => timers.size,
    advance(ms: number) {
      const end = now + ms
      for (;;) {
        const next = [...timers.entries()].filter(([, t]) => t.at <= end).sort((a, b) => a[1].at - b[1].at || a[0] - b[0])[0]
        if (!next) break
        timers.delete(next[0]); now = next[1].at; next[1].run()
      }
      now = end
    },
  }
}

function harness(options: { hidden?: boolean; reduced?: boolean } = {}) {
  const clock = fakeClock()
  const state = { hidden: options.hidden ?? false, reduced: options.reduced ?? false, prefix: '', notice: '', paints: [] as Array<[number | null, number]>, notices: [] as string[] }
  const tab = createDeskTab({
    prefix: text => { state.prefix = text },
    notice: text => { state.notice = text; state.notices.push(text) },
    paint: (count, scale) => { state.paints.push([count, scale]) },
    hidden: () => state.hidden, reducedMotion: () => state.reduced, clock,
  })
  const read = (count: number | null, extra: Partial<DeskReading> = {}) => tab.update({ owner: 't:person', count, error: false, ...extra })
  return { clock, state, tab, read }
}

test('the tab counts like the badge: none at 0, (1), (9), then (9+)', () => {
  assert.deepEqual([0, 1, 9, 10, 250].map(titlePrefix), ['', '(1) ', '(9) ', '(9+) ', '(9+) '])
  assert.equal(titlePrefix(null), '')
  assert.equal(tabCount(-1), '')
})

test('a digit needs 24 device pixels; below that the dot is plain, and 9+ widens into a capsule', () => {
  const small = dotLayout(16, 3)!, large = dotLayout(32, 3)!, many = dotLayout(32, 12)!
  assert.equal(small.label, '')
  assert.equal(large.label, '3')
  assert.equal(many.label, '9+')
  assert.ok(many.width > many.height, '9+ is wider than tall')
  assert.equal(large.width, large.height, 'one digit sits in a round dot')
  for (const dot of [small, large, many]) assert.ok(Math.abs(dot.x + dot.width - (dot === small ? 16 : 32)) < 1e-9 && Math.abs(dot.y + dot.height - (dot === small ? 16 : 32)) < 1e-9, 'anchored bottom-right')
  assert.ok(dotLayout(32, 3, 1.2)!.height > large.height, 'the pop grows the dot')
  assert.equal(dotLayout(32, 0), null)
})

test('the first count of a page shows at rest: no pop, no notice', () => {
  const { state, read, clock } = harness()
  read(3)
  assert.equal(state.prefix, '(3) ')
  assert.deepEqual(state.paints, [[3, 1]])
  assert.equal(state.notices.length, 0)
  assert.equal(clock.pending(), 0)
})

test('a rise in a visible tab pops the dot once in six frames, then stops', () => {
  const { state, read, clock } = harness()
  read(3); state.paints.length = 0
  read(4)
  assert.equal(state.prefix, '(4) ')
  clock.advance(POP_FRAME_MS * POP_SCALES.length * 3)
  assert.deepEqual(state.paints, POP_SCALES.map(scale => [4, scale]))
  assert.equal(Math.max(...state.paints.map(([, scale]) => scale)), 1.2)
  assert.deepEqual(state.paints.at(-1), [4, 1], 'it ends at rest')
  assert.equal(state.notices.length, 0, 'a visible tab keeps its title')
  assert.equal(clock.pending(), 0, 'nothing loops')
})

test('a rise in a background tab says New decision for two seconds, then settles on the count', () => {
  const { state, read, clock } = harness({ hidden: true })
  read(1); state.paints.length = 0
  read(2)
  assert.equal(state.notice, NEW_DECISION)
  assert.deepEqual(state.paints, [[2, 1]], 'the dot changes once, without frames')
  clock.advance(NOTICE_MS - 1)
  assert.equal(state.notice, NEW_DECISION)
  clock.advance(1)
  assert.equal(state.notice, '')
  assert.equal(state.prefix, '(2) ')
  assert.equal(clock.pending(), 0)
})

test('reduced motion changes only the number: no frames, no notice', () => {
  for (const hidden of [false, true]) {
    const { state, read, clock } = harness({ hidden, reduced: true })
    read(1); state.paints.length = 0
    read(5)
    assert.equal(state.prefix, '(5) ')
    assert.deepEqual(state.paints, [[5, 1]])
    assert.equal(state.notices.length, 0)
    assert.equal(clock.pending(), 0)
  }
})

test('a store reading again keeps the count; the same count afterwards moves nothing', () => {
  const { state, read } = harness()
  read(3); state.paints.length = 0
  read(null)
  assert.equal(state.prefix, '(3) ', 'a read in flight is not a count of zero')
  read(3)
  assert.deepEqual(state.paints, [])
  read(2); read(null); read(2)
  assert.deepEqual(state.paints, [[2, 1]], 'a fall is shown at rest')
})

test('a failed read shows no count; a recovery is a rise only above the last count read', () => {
  const { state, read, clock } = harness()
  read(3); state.paints.length = 0
  read(null, { error: true })
  assert.equal(state.prefix, '')
  assert.deepEqual(state.paints, [[null, 1]])
  read(3)
  assert.equal(state.prefix, '(3) ')
  assert.deepEqual(state.paints.at(-1), [3, 1])
  clock.advance(1000)
  assert.equal(state.paints.length, 2, 'the same count back is no pop')
  read(4); clock.advance(1000)
  assert.equal(state.paints.length, 2 + POP_SCALES.length)
})

test('signed out, an agent principal or another person starts from nothing', () => {
  const { state, tab, read, clock } = harness()
  read(3); read(4); clock.advance(50)
  tab.update({ owner: '', count: 4, error: false })
  assert.equal(state.prefix, '')
  assert.deepEqual(state.paints.at(-1), [null, 1], 'the plain icon is back')
  clock.advance(1000)
  assert.deepEqual(state.paints.at(-1), [null, 1], 'a pop in flight was cancelled')
  state.paints.length = 0
  tab.update({ owner: 't:other', count: 7, error: false })
  assert.equal(state.prefix, '(7) ')
  clock.advance(1000)
  assert.deepEqual(state.paints, [[7, 1]], 'a new person’s first count is no rise')
})

test('a tab sent to the background ends its pop at rest; one brought forward drops its notice', () => {
  const { state, tab, read, clock } = harness()
  read(1); read(2); clock.advance(POP_FRAME_MS)
  state.hidden = true; tab.visibility(true)
  assert.deepEqual(state.paints.at(-1), [2, 1])
  assert.equal(clock.pending(), 0)
  read(3)
  assert.equal(state.notice, NEW_DECISION)
  state.hidden = false; tab.visibility(false)
  assert.equal(state.notice, '')
  assert.equal(clock.pending(), 0)
})

test('dispose leaves the plain title and icon', () => {
  const { state, tab, read } = harness({ hidden: true })
  read(1); read(2)
  tab.dispose()
  assert.equal(state.prefix, '')
  assert.equal(state.notice, '')
  assert.deepEqual(state.paints.at(-1), [null, 1])
})
