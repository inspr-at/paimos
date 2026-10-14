// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ARRIVAL_HOLD_MS, arrive, type ArrivalPage, type ArrivalSpot } from '../src/lib/arrival.ts'

// AEON-1365: a page whose layout, scroll position and clock the test moves by hand.
function fakePage(spot: ArrivalSpot) {
  const world = { spot, gone: false, clock: 0, scrolls: 0, watching: false, listening: false, changed: () => {}, took: () => {}, timers: [] as { run: () => void; ms: number; cleared: boolean }[] }
  const page: ArrivalPage = {
    measure: () => world.gone ? null : { ...world.spot },
    scroll: () => { world.scrolls++ },
    watch: changed => { world.watching = true; world.changed = changed; return () => { world.watching = false } },
    person: took => { world.listening = true; world.took = took; return () => { world.listening = false } },
    later: (run, ms) => { const timer = { run, ms, cleared: false }; world.timers.push(timer); return () => { timer.cleared = true } },
    now: () => world.clock,
  }
  // The browser moved: layout, a scroll, or both, then told the hold.
  const tell = (change: Partial<ArrivalSpot>) => { Object.assign(world.spot, change); world.changed() }
  return { page, world, tell }
}
const start = (): ArrivalSpot => ({ box: 'main', scroll: 0, offset: 600, goal: 600 })
const released = (world: ReturnType<typeof fakePage>['world']) => !world.watching && !world.listening && world.timers.every(timer => timer.cleared)

test('content that arrives above the target after the scroll brings it back to the top, and our own scroll keeps the hold', () => {
  const { page, world, tell } = fakePage(start())
  arrive(page)
  assert.equal(world.scrolls, 1)
  // The smooth scroll on its way: closer to the spot each frame, nothing to redo.
  world.clock = 50; tell({ scroll: 200 }); tell({ scroll: 450 })
  assert.equal(world.scrolls, 1)
  // The kinds arrive above the limits, late: the target moves down while the scroll is under way.
  world.clock = 1500; tell({ offset: 1150, goal: 1150 })
  assert.equal(world.scrolls, 2)
  tell({ scroll: 900 }); tell({ scroll: 1150 })
  // A box that could not scroll far enough until the content below arrived scrolls again too.
  tell({ goal: 1200, offset: 1200 })
  assert.equal(world.scrolls, 3)
  // Content moving the target into another scroll box counts as a move as well.
  tell({ box: 'html' })
  assert.equal(world.scrolls, 4)
  // A page too short to scroll at all: the data that makes it long arrives late and still lands.
  const short = fakePage({ box: 'html', scroll: 0, offset: 500, goal: 0 })
  arrive(short.page)
  short.world.clock = 2000; short.tell({ box: 'main', offset: 1150, goal: 1150 })
  assert.equal(short.world.scrolls, 2)
  // Data right behind the scroll, before its first frame, is a move too.
  const quick = fakePage(start())
  arrive(quick.page)
  quick.world.clock = 30; quick.tell({ offset: 1150, goal: 1150 })
  assert.equal(quick.world.scrolls, 2)
  assert.ok(!released(world) && !released(short.world) && !released(quick.world))
})

test('a scroll away from the target or any input by the person lets go; later changes stay with them', () => {
  for (const takeOver of ['scroll', 'person', 'cancelled'] as const) {
    const { page, world, tell } = fakePage(start())
    arrive(page)
    if (takeOver === 'scroll') { tell({ scroll: 300 }); tell({ scroll: 0 }) } // back to the top: the person, or a test resetting the page
    else if (takeOver === 'person') { tell({ scroll: 300 }); world.took() }
    else { world.clock = 500; tell({ offset: 1150, goal: 1150 }) } // our scroll never moved: someone stopped it with theirs
    assert.ok(released(world), takeOver)
    tell({ offset: 1300, goal: 1300 })
    assert.equal(world.scrolls, 1, takeOver)
  }
})

test('the hold is bounded: it ends after its window, when the target leaves, or when the page lets it go', () => {
  const timed = fakePage(start())
  arrive(timed.page)
  assert.deepEqual(timed.world.timers.map(timer => timer.ms), [ARRIVAL_HOLD_MS])
  timed.world.timers[0]!.run()
  assert.ok(released(timed.world))
  timed.tell({ offset: 1150, goal: 1150 })
  assert.equal(timed.world.scrolls, 1)

  const left = fakePage(start())
  arrive(left.page)
  left.world.gone = true; left.world.changed()
  assert.ok(released(left.world))
  assert.equal(left.world.scrolls, 1)

  const cancelled = fakePage(start())
  const letGo = arrive(cancelled.page)
  letGo(); letGo()
  assert.ok(released(cancelled.world))
  cancelled.tell({ offset: 1150, goal: 1150 })
  assert.equal(cancelled.world.scrolls, 1)
})
