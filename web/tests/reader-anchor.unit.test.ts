// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1279: live updates without moving the reader. Risk: a touch scroll counts as settled while
// the finger is still down or momentum still runs (a live change then lands mid-swipe); it never
// settles where the browser has no scrollend (Safari); the anchor scrolls at the very top, scrolls
// by a sub-pixel, follows a row that left instead of one still on screen, or loses its mark when a
// component updates twice in one flush. Timers are injected.
import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as columns from '../src/lib/columns'
import { setupSource, sourceModule, sourceText } from './record-source'

const { SETTLE_MS, readerAnchor, touchScroll } = sourceModule<typeof import('../src/lib/readerAnchor')>('lib/readerAnchor.ts', {})

function manualTimers() {
  let now = 0, serial = 0
  const queue = new Map<number, { at: number; fn: () => void }>()
  return {
    set: (fn: () => void, ms: number) => { queue.set(++serial, { at: now + ms, fn }); return serial },
    clear: (id: unknown) => { queue.delete(id as number) },
    advance(ms: number) {
      now += ms
      for (const [id, task] of [...queue].sort((a, b) => a[1].at - b[1].at)) if (task.at <= now) { queue.delete(id); task.fn() }
    },
  }
}
// A touch event carries how many fingers are still down.
function fire(target: EventTarget, type: string, fingers?: number) {
  target.dispatchEvent(fingers === undefined ? new Event(type) : Object.assign(new Event(type), { touches: { length: fingers } }))
}

describe('touchScroll', () => {
  it('runs from the touch until momentum is quiet, never while a finger is down', () => {
    const target = new EventTarget(), timers = manualTimers()
    const touch = touchScroll(target, timers)
    expect(touch.active.value).toBe(false)
    fire(target, 'touchstart', 1)
    expect(touch.active.value).toBe(true)
    // A finger resting on the screen keeps it running, however long.
    timers.advance(10 * SETTLE_MS)
    expect(touch.active.value).toBe(true)
    fire(target, 'touchend', 0)
    // Momentum: each scroll restarts the quiet spell.
    for (let i = 0; i < 5; i++) { timers.advance(SETTLE_MS - 1); fire(target, 'scroll') }
    expect(touch.active.value).toBe(true)
    timers.advance(SETTLE_MS)
    expect(touch.active.value).toBe(false)
    touch.stop()
  })

  it('ends at scrollend once no finger is down, and ignores scrolls without a touch', () => {
    const target = new EventTarget(), timers = manualTimers()
    const touch = touchScroll(target, timers)
    fire(target, 'scroll')
    expect(touch.active.value, 'a wheel or a script scroll is no touch').toBe(false)
    fire(target, 'touchstart', 1)
    fire(target, 'touchstart', 2)
    fire(target, 'scrollend')
    expect(touch.active.value, 'two fingers are down').toBe(true)
    fire(target, 'touchend', 1)
    fire(target, 'scrollend')
    expect(touch.active.value, 'one finger is still down').toBe(true)
    fire(target, 'touchcancel', 0)
    fire(target, 'scrollend')
    expect(touch.active.value).toBe(false)
    // A new touch starts over; stop() removes every listener.
    touch.stop()
    fire(target, 'touchstart', 1)
    expect(touch.active.value).toBe(false)
  })
})

// A row whose box the test sets; `isConnected` follows the list it sits in.
function rowAt(top: number, height = 60) {
  const row = { top, height, connected: true }
  const el = { getBoundingClientRect: () => ({ top: row.top, bottom: row.top + row.height, height: row.height }), get isConnected() { return row.connected } } as unknown as HTMLElement
  return { row, el }
}
function scrollerAt(top: number, scrollTop: number) {
  return { scrollTop, getBoundingClientRect: () => ({ top }) } as unknown as HTMLElement
}

describe('readerAnchor', () => {
  it('never restores a pane capture into the page when the split closes', () => {
    const pane = scrollerAt(100, 600), page = scrollerAt(50, 134)
    let scroller = pane
    const rows = [rowAt(110), rowAt(170)]
    const anchor = readerAnchor({ scroller: () => scroller, rows: () => rows.map(r => r.el) })
    anchor.capture()
    scroller = page
    for (const row of rows) row.row.top += 1195
    anchor.restore()
    expect(page.scrollTop, 'closing the split preserves the page scroll').toBe(134)
    expect(pane.scrollTop, 'the former pane is unchanged too').toBe(600)
    // Subsequent live updates in the page still anchor normally.
    anchor.capture()
    for (const row of rows) row.row.top += 60
    anchor.restore()
    expect(page.scrollTop).toBe(194)
  })

  it('keeps the first row on screen where it was when rows arrive above it', () => {
    const scroller = scrollerAt(100, 900)
    const rows = [rowAt(20), rowAt(80), rowAt(140), rowAt(200)]
    const anchor = readerAnchor({ scroller: () => scroller, rows: () => rows.map(r => r.el), top: () => 120 })
    anchor.capture()
    // Three rows arrive above: everything moves down 180 px.
    for (const r of rows) r.row.top += 180
    anchor.restore()
    expect(scroller.scrollTop).toBe(1080)
  })

  it('keeps the first capture when the component updates twice before its restore', () => {
    const scroller = scrollerAt(100, 900)
    const rows = [rowAt(110), rowAt(170)]
    const anchor = readerAnchor({ scroller: () => scroller, rows: () => rows.map(r => r.el) })
    anchor.capture()
    for (const r of rows) r.row.top += 24
    // The second pass of the same flush sees the rows already moved; it must not reset the mark.
    anchor.capture()
    anchor.restore()
    expect(scroller.scrollTop).toBe(924)
    // After the restore the next update captures afresh.
    anchor.capture()
    for (const r of rows) r.row.top -= 10
    anchor.restore()
    expect(scroller.scrollTop).toBe(914)
  })

  it('drops a mark whose update never restored once its flush is over', async () => {
    const scroller = scrollerAt(100, 900)
    const rows = [rowAt(110)]
    const anchor = readerAnchor({ scroller: () => scroller, rows: () => rows.map(r => r.el) })
    anchor.capture()
    await Promise.resolve()
    rows[0]!.row.top += 300
    anchor.capture()
    rows[0]!.row.top += 24
    anchor.restore()
    expect(scroller.scrollTop, 'only this flush’s move counts').toBe(924)
  })

  it('follows the next row on screen when the first one leaves, and stays still at the top or for a sub-pixel', () => {
    const scroller = scrollerAt(100, 600)
    const rows = [rowAt(110), rowAt(170), rowAt(230)]
    const anchor = readerAnchor({ scroller: () => scroller, rows: () => rows.map(r => r.el) })
    anchor.capture()
    rows[0]!.row.connected = false
    rows[1]!.row.top -= 60; rows[2]!.row.top -= 60
    anchor.restore()
    expect(scroller.scrollTop, 'the next row keeps its place').toBe(540)
    // A sub-pixel move is no move.
    anchor.capture()
    rows[1]!.row.top += 0.4
    anchor.restore()
    expect(scroller.scrollTop).toBe(540)
    // At the very top new rows show where they belong.
    const top = scrollerAt(100, 0)
    const atTop = readerAnchor({ scroller: () => top, rows: () => rows.map(r => r.el) })
    atTop.capture()
    rows[1]!.row.top += 120
    atTop.restore()
    expect(top.scrollTop).toBe(0)
  })
})

it('P03: delayed table resize deliveries settle on the live width and discard superseded frame callbacks', () => {
  const { descriptor } = parse(sourceText('components/work/TicketTable.vue'))
  const source = ts.createSourceFile('TicketTable.ts', descriptor.scriptSetup!.content, ts.ScriptTarget.Latest, true)
  const names = ['finishCardSettle', 'settleCardWidth', 'applyCardSize', 'measureSoon']
  const declarations = source.statements.filter(statement => ts.isFunctionDeclaration(statement) && names.includes(statement.name?.text ?? ''))
  expect(declarations).toHaveLength(names.length)
  const body = ts.transpileModule(declarations.map(statement => statement.getText(source)).join('\n'), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  let live = 1000
  const frames: (() => void)[] = [], width = ref(1000), cardEpoch = ref(0), cardWidthSettled = ref(false)
  const dependencies = { ...columns, width, cardEpoch, cardWidthSettled, readCardWidth: () => live,
    requestAnimationFrame: (callback: () => void) => { frames.push(callback) }, window: { innerWidth: 1440 } }
  const make = new Function(...Object.keys(dependencies), `let cardFrame = 0, columnsAlive = true, resizing = false, lastInner = 1440, viewportGrew = false, shrinkCeiling = null, shrinkBefore = null; ${body}; return { applyCardSize, measureSoon }`)
  const table = make(...Object.values(dependencies)) as { applyCardSize: (entries: { contentRect: { width: number } }[]) => void; measureSoon: () => void }
  const drain = () => {
    let count = 0
    while (frames.length) { expect(++count).toBeLessThanOrEqual(64); frames.shift()!() }
  }
  table.applyCardSize([{ contentRect: { width: 760 } }])
  expect(width.value).toBe(760)
  drain()
  expect(width.value).toBe(1000)
  expect(cardWidthSettled.value).toBe(true)
  expect(cardEpoch.value).toBe(1)
  live = 760; table.applyCardSize([{ contentRect: { width: 760 } }])
  live = 1100; table.applyCardSize([{ contentRect: { width: 1100 } }])
  drain()
  expect(width.value).toBe(1100)
  expect(cardEpoch.value).toBe(2)
})

it('P03: editor fit math preserves growth and only spends the scroller slack on delete, late font or resize', () => {
  const pane = { parentElement: null, scrollTop: 900, scrollHeight: 1200, clientHeight: 300 }
  const values: Record<string, string> = {}
  const area = { parentElement: pane, offsetWidth: 300, offsetHeight: 560, value: 'draft', placeholder: '', style: {
    height: '560px', setProperty: (key: string, value: string) => { values[key] = value }, removeProperty: (key: string) => { delete values[key] },
  } }
  const mirror = { value: '', scrollHeight: 120, offsetHeight: 2, clientHeight: 0 }
  vi.stubGlobal('getComputedStyle', () => ({ overflowY: 'auto' }))
  vi.stubGlobal('document', { scrollingElement: null, fonts: { removeEventListener() {} } })
  const props = { modelValue: 'draft', label: 'Description', minRows: 5, split: false }
  const component = setupSource('components/work/MarkdownEditor.vue', props, {})
  try {
    component.state.area.value = area; component.state.mirror.value = mirror
    component.state.refit()
    expect(area.style.height).toBe('560px')
    pane.scrollHeight = 1300 // 100px of real slack can be reclaimed, no more.
    component.state.refit()
    expect(area.style.height).toBe('460px')
    area.offsetHeight = 460; mirror.scrollHeight = 180
    component.state.fit(component.state.hold(true))
    expect(area.style.height).toBe('460px')
    mirror.scrollHeight = 900
    component.state.fit(component.state.hold(true))
    expect(area.style.height).toBe('560px')
    props.split = true
    component.state.fit(component.state.hold(true))
    expect(area.style.height).toBe('')
    expect(values['--md-fit']).toBe('903px')
    expect(pane.scrollTop).toBe(900)
  } finally { component.stop(); vi.unstubAllGlobals() }
})
