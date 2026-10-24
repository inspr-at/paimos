// SPDX-License-Identifier: AGPL-3.0-only
import { inflateSync } from 'node:zlib'
import { test, expect, type Page } from '@playwright/test'
import { me, showProjectHead } from './work-fixtures'
import { headerStorageKey } from '../src/lib/projectHeader'
import { mockTicketGraph, ticketGraphWorld } from './ticket-graph-fixtures'
import type { TicketGraphLink } from '../src/lib/ticketGraph'

test.use({
  viewport: { width: 1600, height: 1000 },
  reducedMotion: 'no-preference',
  launchOptions: { args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] },
})
test.setTimeout(60_000)

// The graph composition was designed for the roomy header; the default is now Compact.
async function mockHeaderGraph(page: Page, world = ticketGraphWorld()) {
  await page.addInitScript(({ key }) => localStorage.setItem(key, JSON.stringify({ density: 'comfortable', roomy: 'comfortable' })), { key: headerStorageKey('t1', me.id) })
  const mocked = await mockTicketGraph(page, world)
  // The header reads Queue independently. A real-server 404 used to insert
  // Queue unavailable during the geometry/pinned-frame checks (AEON-1261).
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], count: 0, manual_order: false, capacity: { queued_hours: 0, parallel_runs: 1, work_hours: 0, warning: false } } }))
  return mocked
}

const surface = (page: Page) => page.locator('.header-glimpse-canvas')
const ready = (page: Page) => expect(surface(page)).toHaveAttribute('data-ready', 'true', { timeout: 20_000 })
// setViewportSize resolves before the page has the new size. With the glimpse
// running on CI's software GL, window.innerWidth kept the old width for 2.7–3 s
// in nightly runs and past 5 s on PR shards; the glimpse then cleared within
// 6 ms. Wait until the page reports the size; the reaction keeps its budget.
async function resizeSeen(page: Page, size: { width: number; height: number }) {
  await page.setViewportSize(size)
  await expect.poll(() => page.evaluate(() => window.innerWidth), { timeout: 20_000 }).toBe(size.width)
}
// Keep the individual signal regressions independent of the task fallback.
// Holding timeout deliveries leaves their chosen viewport/frame signal as the
// only recovery path, including callbacks queued before the hold begins.
async function holdTimeoutsWhile(page: Page, key: string, field?: string) {
  await page.addInitScript(({ key, field }) => {
    const timeout = window.setTimeout.bind(window)
    window.setTimeout = (handler, delay, ...args) => timeout(typeof handler === 'function' ? (...values: unknown[]) => {
      const state = (window as unknown as Record<string, unknown>)[key]
      const holding = field ? (state as Record<string, unknown> | undefined)?.[field] : state
      if (holding !== true) handler(...values)
    } : handler, delay, ...args)
  }, { key, field })
}
// Fixed 60fps steps, released after layout, so the contrast shot pauses on one
// orbit angle. 17ms stays above the renderer's frame gate. Do not relax 0.78.
const GLIMPSE_PIN_FRAMES = 192
async function releasePinnedGlimpse(page: Page) {
  await page.evaluate(async () => {
    await document.fonts.ready
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    const release = (window as unknown as { __aeonReleaseGlimpsePin?: () => void }).__aeonReleaseGlimpsePin
    if (!release) throw new Error('glimpse pin was not installed')
    release()
  })
  await expect(surface(page)).toHaveAttribute('data-glimpse-pin', String(GLIMPSE_PIN_FRAMES), { timeout: 20_000 })
}

const protectedSelectors = ['.title-line > *', '.description', '.head-stats .stat', '.head-stats .q-warn', '.head-stats .head-work', '.head-stats .group-count', '.head-stats .status-count', '.header-activity > *', '.project-tabs', '.project-navigation a', '.project-navigation button', '.project-navigation label', '.view-bar .view-tab', '.view-bar .tab', '.view-bar .changes', '.toolbar-wrap']
async function protectedBoxes(page: Page) {
  return page.locator(protectedSelectors.join(', ')).evaluateAll(elements => elements.filter(el => el.getClientRects().length).map(el => {
    const box = el.getBoundingClientRect()
    return { x: box.x, y: box.y, width: box.width, height: box.height }
  }).filter(box => box.width > 0 && box.height > 0))
}
async function expectClearOfText(page: Page) {
  // setViewportSize resolves before the browser dispatches resize. Require a
  // measurement of this viewport, rather than accepting the previous frame.
  await expect(page.locator('.glimpse-col')).toHaveAttribute('data-measured-viewport', String(page.viewportSize()!.width))
  await expect(page.locator('.glimpse-col')).toHaveAttribute('data-measured', 'true')
  await expect(page.locator('.glimpse-canvas')).toBeVisible()
  const region = (await page.locator('.glimpse-canvas').boundingBox())!
  const header = (await page.locator('.project-head').boundingBox())!
  const toolbar = (await page.locator('.toolbar-wrap').boundingBox())!
  const main = (await page.locator('#main').boundingBox())!
  expect(Math.abs(region.x - header.x)).toBeLessThan(1)
  expect(Math.abs(region.width - header.width)).toBeLessThan(1)
  expect(Math.abs(region.y - main.y)).toBeLessThan(2)
  expect(Math.abs(region.y + region.height - toolbar.y)).toBeLessThan(1)
  const controls = (await page.locator('.glimpse-controls').boundingBox())!
  const spot = await page.locator('.glimpse-col').getAttribute('data-spot')
  for (const box of await protectedBoxes(page)) {
    const overlap = controls.x < box.x + box.width && controls.x + controls.width > box.x && controls.y < box.y + box.height && controls.y + controls.height > box.y
    expect(overlap, `hover controls stay clear of text and toolbar: ${JSON.stringify({ controls, box, spot, viewport: page.viewportSize() })}`).toBe(false)
  }
}

function sized(count: number, withLinks: boolean) {
  const world = ticketGraphWorld()
  world.graph.nodes = world.graph.nodes.slice(0, count).map(node => ({ ...node, type: 'ticket', status: 'backlog', status_category: 'open' }))
  for (const node of world.graph.nodes) {
    const work = world.work.nodes.find(item => item.id === node.id)!
    work.kind_slug = 'ticket'
    work.state = 'backlog'
  }
  const ids = new Set(world.graph.nodes.map(node => node.id))
  const linked = world.graph.links.filter(link => ids.has(link.source) && ids.has(link.target))
  const links: TicketGraphLink[] = withLinks ? (linked.length ? linked : [{ source: world.graph.nodes[0].id, target: world.graph.nodes[1].id, kind: 'relates' }]) : []
  world.graph.links = links
  return world
}

// Eight open tickets, exactly one edge, six of them isolated. Work-list fields
// stay in step so a Tickets filter can shrink that visible set.
function eightWithOneEdge() {
  const world = ticketGraphWorld()
  const nodes = world.graph.nodes.slice(0, 8).map((node, index) => ({
    ...node,
    type: 'ticket' as const,
    status: 'backlog',
    status_category: 'open' as const,
    priority: index === 7 ? 'medium' : 'high',
  }))
  for (const node of nodes) {
    const work = world.work.nodes.find(item => item.id === node.id)
    if (!work) throw new Error(`missing work node ${node.id}`)
    work.kind_slug = 'ticket'
    work.state = 'backlog'
    work.fields = { ...work.fields, priority: node.priority }
  }
  world.graph.nodes = nodes
  world.graph.links = [{ source: nodes[0].id, target: nodes[1].id, kind: 'relates' }]
  return world
}

// Eight high-priority tickets with no edge between them, plus one edge that
// only joins two low-priority tickets outside that filter.
function eightUnlinkedWhenFiltered() {
  const world = ticketGraphWorld()
  const nodes = world.graph.nodes.slice(0, 10).map((node, index) => ({
    ...node,
    type: 'ticket' as const,
    status: 'backlog',
    status_category: 'open' as const,
    priority: index < 8 ? 'high' : 'low',
  }))
  for (const node of nodes) {
    const work = world.work.nodes.find(item => item.id === node.id)
    if (!work) throw new Error(`missing work node ${node.id}`)
    work.kind_slug = 'ticket'
    work.state = 'backlog'
    work.fields = { ...work.fields, priority: node.priority }
  }
  world.graph.nodes = nodes
  world.graph.links = [{ source: nodes[8].id, target: nodes[9].id, kind: 'relates' }]
  return world
}

test('a wide project with tickets and a link shows a labelless 60fps glimpse', async ({ page }) => {
  await mockHeaderGraph(page)
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  await expect(surface(page)).toHaveAttribute('data-fps', '60')
  await expect(surface(page)).toHaveAttribute('data-labels', 'off')
  await expect(surface(page)).toHaveAttribute('data-glimpse', '')
  await expect(page.locator('.glimpse-col .graph-label')).toHaveCount(0)
  const layer = page.locator('.glimpse-canvas')
  await expect(layer).toHaveCSS('opacity', '0.35')
  const mask = await layer.evaluate(el => getComputedStyle(el).maskImage || getComputedStyle(el).webkitMaskImage)
  expect(mask).toContain('gradient')
  const hit = await page.evaluate(() => {
    const title = document.getElementById('project-title')!
    const rect = title.getBoundingClientRect()
    return document.elementFromPoint(rect.left + 12, rect.top + rect.height / 2)?.id ?? ''
  })
  expect(hit).toBe('project-title')
})

test('the glimpse follows the viewer’s Off motion preference without adding labels', async ({ page }) => {
  const world = ticketGraphWorld()
  world.work.preferences['graph-motion'] = { pace: 'off' }
  await mockHeaderGraph(page, world)
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  await expect(surface(page)).toHaveAttribute('data-orbit-seconds', '0')
  await expect(surface(page)).toHaveAttribute('data-labels-ready', 'true', { timeout: 20_000 })
  await expect(page.locator('.glimpse-col .graph-label')).toHaveCount(0)
})

test('hover shows Open graph and Pause; clicking the glimpse opens the graph', async ({ page }) => {
  await mockHeaderGraph(page)
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  const controls = page.locator('.glimpse-controls')
  await expect.poll(() => controls.evaluate(el => getComputedStyle(el).opacity)).toBe('0')
  await page.locator('.project-head').hover()
  await expect.poll(() => controls.evaluate(el => getComputedStyle(el).opacity)).toBe('1')
  await expect(page.getByRole('button', { name: 'Open graph', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Pause motion', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Pause motion', exact: true }).click()
  await expect(page).not.toHaveURL(/view=graph/)
  await expect(surface(page)).toHaveAttribute('data-motion', 'still')
  await expect(page.getByRole('button', { name: 'Resume motion', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Open graph', exact: true }).click()
  await expect(page).toHaveURL('/p/PHAROS/tickets?view=graph')
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
})

test('the glimpse waits until the page is idle', async ({ page }) => {
  await page.addInitScript(() => {
    const pending: IdleRequestCallback[] = []
    window.requestIdleCallback = (cb: IdleRequestCallback) => { pending.push(cb); return pending.length }
    window.cancelIdleCallback = () => {}
    ;(window as unknown as { __releaseIdle: () => void }).__releaseIdle = () => {
      for (const cb of pending.splice(0)) cb({ didTimeout: false, timeRemaining: () => 30 } as IdleDeadline)
    }
  })
  await mockHeaderGraph(page)
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await expect(surface(page)).toHaveCount(0)
  await page.evaluate(() => (window as unknown as { __releaseIdle: () => void }).__releaseIdle())
  await ready(page)
})

test('narrow viewports and small or unlinked graphs stay empty', async ({ page }) => {
  await mockHeaderGraph(page, sized(7, true))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await expect.poll(() => page.locator('[data-header-glimpse]').getAttribute('data-header-glimpse')).toBe('off')
  await expect(surface(page)).toHaveCount(0)
})

test('eight tickets without a link stay empty', async ({ page }) => {
  const { calls } = await mockHeaderGraph(page, sized(8, false))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await expect.poll(() => calls.length).toBeGreaterThan(0)
  await page.waitForTimeout(400)
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
})

test('eight visible tickets with exactly one edge show the glimpse', async ({ page }) => {
  const world = eightWithOneEdge()
  expect(world.graph.nodes).toHaveLength(8)
  expect(world.graph.links).toHaveLength(1)
  await mockHeaderGraph(page, world)
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveAttribute('data-shown', '8', { timeout: 20_000 })
})

test('a priority filter that leaves seven of those tickets hides the glimpse', async ({ page }) => {
  const { calls } = await mockHeaderGraph(page, eightWithOneEdge())
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveAttribute('data-shown', '8', { timeout: 20_000 })
  const before = calls.length
  await page.goto('/p/PHAROS/tickets?priority=high'); await showProjectHead(page)
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await expect.poll(() => calls.length).toBeGreaterThan(before)
  await page.waitForTimeout(500)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
})

test('a priority filter that keeps eight tickets but no link hides the glimpse', async ({ page }) => {
  const world = eightUnlinkedWhenFiltered()
  expect(world.graph.nodes.filter(node => node.priority === 'high')).toHaveLength(8)
  expect(world.graph.links).toHaveLength(1)
  const { calls } = await mockHeaderGraph(page, world)
  await page.goto('/p/PHAROS/tickets?priority=high'); await showProjectHead(page)
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await expect.poll(() => calls.length).toBeGreaterThan(0)
  await page.waitForTimeout(500)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
})

test('eight linked tickets appear once the viewport reaches 1280', async ({ page }) => {
  // Risk: CI's software GL can consume the assertion's wall-clock budget
  // before the shrink's callbacks run. Drive the page clock explicitly so
  // the viewport reaction is checked after one 250ms fallback delivery.
  await page.clock.install()
  await page.setViewportSize({ width: 1279, height: 900 })
  await mockHeaderGraph(page, sized(8, true))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
  await expect(surface(page)).toHaveCount(0)
  await resizeSeen(page, { width: 1280, height: 900 })
  await ready(page)
  await page.clock.pauseAt(new Date(await page.evaluate(() => Date.now()) + 1000))
  await resizeSeen(page, { width: 390, height: 844 })
  await page.clock.runFor(250)
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
})

// Risk: a resize delivery has the new root width while window/media hints and
// a geometry read still describe the previous frame. Retain the delivered
// measurement; another stale read must not leave the painted graph mounted.
// Deliver that sample explicitly: software GL can delay Chromium's native
// resize delivery beyond the assertion budget. Other cases cover real resizes.
test('AEON-1401: the delivered viewport width clears a glimpse while geometry and media reads stay wide', async ({ page }) => {
  await page.addInitScript(() => {
    const state = { holding: false, rootWidths: [] as number[], framesHeld: 0, tasksHeld: 0, deliverRoot: undefined as ((width: number) => void) | undefined }
    ;(window as unknown as { __aeonDeliveredViewport: typeof state }).__aeonDeliveredViewport = state
    const rect = Element.prototype.getBoundingClientRect
    Element.prototype.getBoundingClientRect = function () {
      const box = rect.call(this)
      return state.holding && this === document.documentElement ? new DOMRect(box.x, box.y, 1280, box.height) : box
    }
    const media = window.matchMedia.bind(window)
    window.matchMedia = query => {
      const list = media(query)
      if (query === '(min-width: 1280px)') {
        Object.defineProperty(list, 'matches', { get: () => state.holding || media(query).matches })
        list.addEventListener('change', event => { if (state.holding) event.stopImmediatePropagation() }, { capture: true })
      }
      return list
    }
    window.addEventListener('resize', event => { if (state.holding) event.stopImmediatePropagation() }, { capture: true })
    window.visualViewport?.addEventListener('resize', event => { if (state.holding) event.stopImmediatePropagation() }, { capture: true })
    const frame = window.requestAnimationFrame.bind(window)
    window.requestAnimationFrame = callback => frame(time => {
      if (state.holding) { state.framesHeld++; return }
      callback(time)
    })
    const timeout = window.setTimeout.bind(window), interval = window.setInterval.bind(window)
    const task = (handler: TimerHandler): TimerHandler => typeof handler === 'function' ? (...args: unknown[]) => {
      if (state.holding) { state.tasksHeld++; return }
      handler(...args)
    } : handler
    window.setTimeout = ((handler: TimerHandler, delay?: number, ...args: unknown[]) => timeout(task(handler), delay, ...args)) as typeof window.setTimeout
    window.setInterval = ((handler: TimerHandler, delay?: number, ...args: unknown[]) => interval(task(handler), delay, ...args)) as typeof window.setInterval
    const Observer = window.ResizeObserver
    window.ResizeObserver = class extends Observer {
      constructor(callback: ResizeObserverCallback) {
        const deliver: ResizeObserverCallback = (entries, observer) => {
          for (const entry of entries) if (state.holding && entry.target === document.documentElement) state.rootWidths.push(entry.contentRect.width)
          callback(entries, observer)
        }
        super((entries, observer) => { if (!state.holding) deliver(entries, observer) })
        const observe = this.observe.bind(this)
        this.observe = (target, options) => {
          if (target === document.documentElement) state.deliverRoot = width => {
            const size = { inlineSize: width, blockSize: document.documentElement.clientHeight }
            deliver([{
              target, contentRect: new DOMRectReadOnly(0, 0, width, size.blockSize),
              borderBoxSize: [size], contentBoxSize: [size], devicePixelContentBoxSize: [size],
            }], this)
          }
          observe(target, options)
        }
      }
    }
  })
  await page.setViewportSize({ width: 1280, height: 900 })
  await mockHeaderGraph(page, sized(8, true))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  await page.evaluate(() => {
    const state = (window as unknown as { __aeonDeliveredViewport: { holding: boolean; deliverRoot?: (width: number) => void } }).__aeonDeliveredViewport
    if (!state.deliverRoot) throw new Error('The document-element resize observer was not registered')
    state.holding = true
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 1280 })
    state.deliverRoot(390)
  })
  await expect.poll(() => page.evaluate(() => ({
    window: window.innerWidth,
    geometry: document.documentElement.getBoundingClientRect().width,
    media: matchMedia('(min-width: 1280px)').matches,
    delivered: (window as unknown as { __aeonDeliveredViewport: { rootWidths: number[] } }).__aeonDeliveredViewport.rootWidths.at(-1),
  }))).toEqual({ window: 1280, geometry: 1280, media: true, delivered: 390 })
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
})

// PR shard: 1279 → 1280 → 390 left the painted glimpse on after innerWidth was
// already 390. The paint sampler, the wide-only interval and a document-element
// observer can all be gone together. The header's own box still changes size.
test('AEON-1467: a painted glimpse hides below 1280 after the paint sampler and the document observer are gone', async ({ page }) => {
  await page.addInitScript(() => {
    const state = { sealed: false, intervals: [] as number[], timeouts: [] as number[], frames: [] as number[], documentDrops: 0 }
    ;(window as unknown as { __aeonGlimpseSeal: typeof state }).__aeonGlimpseSeal = state
    const nativeInterval = window.setInterval.bind(window)
    const nativeTimeout = window.setTimeout.bind(window)
    const nativeFrame = window.requestAnimationFrame.bind(window)
    window.setInterval = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) => {
      const id = nativeInterval(handler, timeout, ...args)
      state.intervals.push(id as unknown as number)
      return id
    }) as typeof window.setInterval
    window.setTimeout = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) => {
      const id = nativeTimeout(handler, timeout, ...args)
      state.timeouts.push(id as unknown as number)
      return id
    }) as typeof window.setTimeout
    window.requestAnimationFrame = callback => {
      const id = nativeFrame(callback)
      state.frames.push(id)
      return id
    }
    window.addEventListener('resize', event => { if (state.sealed) event.stopImmediatePropagation() }, { capture: true })
    window.visualViewport?.addEventListener('resize', event => { if (state.sealed) event.stopImmediatePropagation() }, { capture: true })
    const media = window.matchMedia.bind(window)
    window.matchMedia = query => {
      const list = media(query)
      if (query === '(min-width: 1280px)') list.addEventListener('change', event => { if (state.sealed) event.stopImmediatePropagation() }, { capture: true })
      return list
    }
    const Observer = window.ResizeObserver
    window.ResizeObserver = class extends Observer {
      constructor(callback: ResizeObserverCallback) {
        super((entries, observer) => {
          if (!state.sealed) { callback(entries, observer); return }
          const delivered = entries.filter(entry => {
            if (entry.target !== document.documentElement) return true
            state.documentDrops++
            return false
          })
          if (delivered.length) callback(delivered, observer)
        })
      }
    }
  })
  await page.setViewportSize({ width: 1280, height: 900 })
  await mockHeaderGraph(page, sized(8, true))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  await page.evaluate(() => {
    const state = (window as unknown as { __aeonGlimpseSeal: { sealed: boolean; intervals: number[]; timeouts: number[]; frames: number[] } }).__aeonGlimpseSeal
    for (const id of state.intervals) window.clearInterval(id)
    for (const id of state.timeouts) window.clearTimeout(id)
    for (const id of state.frames) window.cancelAnimationFrame(id)
    state.intervals.length = 0
    state.timeouts.length = 0
    state.frames.length = 0
    state.sealed = true
    window.requestAnimationFrame = () => 0
    window.setTimeout = (() => 0) as typeof window.setTimeout
    window.setInterval = (() => 0) as typeof window.setInterval
  })
  await resizeSeen(page, { width: 390, height: 844 })
  await expect.poll(() => page.evaluate(() => ({
    width: window.innerWidth,
    layout: document.documentElement.getBoundingClientRect().width,
  }))).toEqual({ width: 390, layout: 390 })
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
})

// The mounted min-width list can miss `change` while the WebGL loop is running.
// Shrinking must still clear the glimpse. This listener swallow is the regression
// for that miss: the layout width is the only signal left.
test('a dropped wide-media change still hides the glimpse below 1280', async ({ page }) => {
  await page.addInitScript(() => {
    const original = window.matchMedia.bind(window)
    window.matchMedia = (query: string) => {
      const list = original(query)
      if (query === '(min-width: 1280px)') {
        const ignore = () => {}
        Object.defineProperty(list, 'addEventListener', { configurable: true, value: ignore })
        Object.defineProperty(list, 'removeEventListener', { configurable: true, value: ignore })
      }
      return list
    }
  })
  await page.setViewportSize({ width: 1600, height: 900 })
  await mockHeaderGraph(page, sized(8, true))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  await resizeSeen(page, { width: 390, height: 844 })
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
})

// Risk: software GL delays the next animation frame after innerWidth changes.
// With the other viewport notifications lost, a frame-based fallback cannot
// clear the decoration. The visual viewport delivery must work independently.
test('AEON-1337: a visual viewport resize clears the glimpse before the next animation frame', async ({ page }) => {
  await holdTimeoutsWhile(page, '__aeonViewportFrameHold', 'holding')
  await page.addInitScript(() => {
    const rootDeliveries = new Map<ResizeObserver, () => void>()
    const state = {
      holding: false, framesHeld: 0, rootsDropped: 0, visualResizes: 0,
      dropRoot() {
        if (!rootDeliveries.size) throw new Error('No root viewport observer was installed')
        for (const deliver of rootDeliveries.values()) deliver()
      },
    }
    ;(window as unknown as { __aeonViewportFrameHold: typeof state }).__aeonViewportFrameHold = state
    const frame = window.requestAnimationFrame.bind(window)
    window.requestAnimationFrame = callback => frame(time => {
      if (state.holding) { state.framesHeld++; return }
      callback(time)
    })
    const interval = window.setInterval.bind(window)
    window.setInterval = (handler, delay, ...args) => interval(typeof handler === 'function' ? (...values: unknown[]) => {
      if (!state.holding) handler(...values)
    } : handler, delay, ...args)
    window.addEventListener('resize', event => { if (state.holding) event.stopImmediatePropagation() }, { capture: true })
    const media = window.matchMedia.bind(window)
    window.matchMedia = query => {
      const list = media(query)
      if (query === '(min-width: 1280px)') list.addEventListener('change', event => { if (state.holding) event.stopImmediatePropagation() }, { capture: true })
      return list
    }
    const Observer = window.ResizeObserver
    window.ResizeObserver = class extends Observer {
      private readonly deliverRoot: () => void
      constructor(callback: ResizeObserverCallback) {
        const deliver: ResizeObserverCallback = (entries, observer) => {
          const delivered = entries.filter(entry => {
            if (!state.holding) return true
            if (entry.target === document.documentElement) state.rootsDropped++
            return false
          })
          if (delivered.length) callback(delivered, observer)
        }
        super(deliver)
        // Inject the intercepted notification, rather than requiring software
        // GL to produce a native observer delivery while its frames are held.
        // Only target is read by the dropping seam; no geometry reaches the app.
        this.deliverRoot = () => deliver([{ target: document.documentElement } as ResizeObserverEntry], this)
      }
      observe(target: Element, options?: ResizeObserverOptions) {
        super.observe(target, options)
        if (target === document.documentElement) rootDeliveries.set(this, this.deliverRoot)
      }
      unobserve(target: Element) {
        super.unobserve(target)
        if (target === document.documentElement) rootDeliveries.delete(this)
      }
      disconnect() {
        super.disconnect()
        rootDeliveries.delete(this)
      }
    }
    window.visualViewport!.addEventListener('resize', event => {
      if (!state.holding) return
      // Native deliveries may be delayed or absent while software GL is held.
      // Release exactly this signal after the real layout reaches phone width.
      if (event.isTrusted) { event.stopImmediatePropagation(); return }
      state.visualResizes++
    }, { capture: true })
  })
  await page.setViewportSize({ width: 1279, height: 900 })
  await mockHeaderGraph(page, sized(8, true))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
  await page.setViewportSize({ width: 1280, height: 900 })
  await ready(page)
  await page.evaluate(() => { (window as unknown as { __aeonViewportFrameHold: { holding: boolean } }).__aeonViewportFrameHold.holding = true })
  await expect.poll(() => page.evaluate(() => (window as unknown as { __aeonViewportFrameHold: { framesHeld: number } }).__aeonViewportFrameHold.framesHeld > 0)).toBe(true)
  await resizeSeen(page, { width: 390, height: 844 })
  await expect.poll(() => page.evaluate(() => document.documentElement.getBoundingClientRect().width)).toBe(390)
  // Every fallback is held, including body observer deliveries and the narrow
  // interval. The glimpse must still be on before the visual event is released.
  await expect(page.locator('[data-header-glimpse="on"]')).toBeAttached()
  await page.evaluate(() => {
    const state = (window as unknown as { __aeonViewportFrameHold: { dropRoot: () => void } }).__aeonViewportFrameHold
    state.dropRoot()
    window.visualViewport!.dispatchEvent(new Event('resize'))
  })
  await expect.poll(() => page.evaluate(() => {
    const state = (window as unknown as { __aeonViewportFrameHold: { framesHeld: number; rootsDropped: number; visualResizes: number } }).__aeonViewportFrameHold
    return {
      width: document.documentElement.getBoundingClientRect().width,
      frameHeld: state.framesHeld > 0,
      rootDropped: state.rootsDropped > 0,
      visualDelivered: state.visualResizes > 0,
    }
  })).toEqual({ width: 390, frameHeld: true, rootDropped: true, visualDelivered: true })
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
})

// Risk: software-rendered frames stop during a shrink, so the paint sampler
// cannot recover missed window/media/root notifications. The independent
// visual-viewport event must clear an already painted glimpse without a frame.
test('AEON-1414: a visual viewport shrink clears the glimpse while frames and other resize notifications are held', async ({ page }) => {
  await holdTimeoutsWhile(page, '__aeonHeldViewport', 'holding')
  await page.addInitScript(() => {
    const state = { holding: false, visualWidths: [] as number[] }
    ;(window as unknown as { __aeonHeldViewport: typeof state }).__aeonHeldViewport = state
    const frame = window.requestAnimationFrame.bind(window)
    window.requestAnimationFrame = callback => frame(time => { if (!state.holding) callback(time) })
    window.addEventListener('resize', event => { if (state.holding) event.stopImmediatePropagation() }, { capture: true })
    const media = window.matchMedia.bind(window)
    window.matchMedia = query => {
      const list = media(query)
      if (query === '(min-width: 1280px)') list.addEventListener('change', event => { if (state.holding) event.stopImmediatePropagation() }, { capture: true })
      return list
    }
    const Observer = window.ResizeObserver
    window.ResizeObserver = class extends Observer {
      constructor(callback: ResizeObserverCallback) {
        super((entries, observer) => {
          const delivered = entries.filter(entry => !state.holding || entry.target !== document.documentElement)
          if (delivered.length) callback(delivered, observer)
        })
      }
    }
    window.visualViewport?.addEventListener('resize', () => {
      if (state.holding) state.visualWidths.push(window.visualViewport!.width)
    })
  })
  await mockHeaderGraph(page, sized(8, true))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  await expect(page.locator('[data-header-glimpse="on"]')).toBeAttached()
  await page.evaluate(() => { (window as unknown as { __aeonHeldViewport: { holding: boolean } }).__aeonHeldViewport.holding = true })
  await resizeSeen(page, { width: 390, height: 844 })
  await expect.poll(() => page.evaluate(() => ({
    layout: document.documentElement.getBoundingClientRect().width,
    visual: (window as unknown as { __aeonHeldViewport: { visualWidths: number[] } }).__aeonHeldViewport.visualWidths.at(-1),
  }))).toEqual({ layout: 390, visual: 390 })
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
})

// Risk: software GL stalls animation frames at the same time as the viewport
// notifications. Advance an injected clock with every other recovery path
// held, so a task must clear the mounted glimpse after the real layout shrinks.
test('AEON-1414: a shrink clears the glimpse while viewport notifications and animation frames are stalled', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-10-18T03:00:00Z') })
  await page.addInitScript(() => {
    const state = { holding: false, framesHeld: 0, rootsDropped: 0 }
    ;(window as unknown as { __aeonViewportStall: typeof state }).__aeonViewportStall = state
    const frame = window.requestAnimationFrame.bind(window)
    window.requestAnimationFrame = callback => frame(time => {
      if (state.holding) { state.framesHeld++; return }
      callback(time)
    })
    window.addEventListener('resize', event => { if (state.holding) event.stopImmediatePropagation() }, { capture: true })
    window.visualViewport?.addEventListener('resize', event => { if (state.holding) event.stopImmediatePropagation() }, { capture: true })
    const media = window.matchMedia.bind(window)
    window.matchMedia = query => {
      const list = media(query)
      if (query === '(min-width: 1280px)') list.addEventListener('change', event => { if (state.holding) event.stopImmediatePropagation() }, { capture: true })
      return list
    }
    const Observer = window.ResizeObserver
    window.ResizeObserver = class extends Observer {
      constructor(callback: ResizeObserverCallback) {
        super((entries, observer) => {
          const delivered = entries.filter(entry => {
            if (!state.holding || entry.target !== document.documentElement) return true
            state.rootsDropped++
            return false
          })
          if (delivered.length) callback(delivered, observer)
        })
      }
    }
  })
  await page.setViewportSize({ width: 1279, height: 900 })
  await mockHeaderGraph(page, sized(8, true))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
  await expect(surface(page)).toHaveCount(0)
  await resizeSeen(page, { width: 1280, height: 900 })
  await ready(page)
  await page.clock.pauseAt(new Date(await page.evaluate(() => Date.now()) + 1000))
  await page.evaluate(() => { (window as unknown as { __aeonViewportStall: { holding: boolean } }).__aeonViewportStall.holding = true })
  await resizeSeen(page, { width: 390, height: 844 })
  await page.clock.runFor(1000)
  await expect.poll(() => page.evaluate(() => {
    const state = (window as unknown as { __aeonViewportStall: { framesHeld: number; rootsDropped: number } }).__aeonViewportStall
    return { layout: document.documentElement.getBoundingClientRect().width, framesHeld: state.framesHeld > 0, rootsDropped: state.rootsDropped > 0 }
  })).toEqual({ layout: 390, framesHeld: true, rootsDropped: true })
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
})

// Risk: the software-rendered canvas outlives the last viewport notification.
// Drop the root observer as well as resize/media events after a ready wide
// frame. Real layout still shrinks, so painting must follow it without a timer.
test('AEON-1472: an active glimpse stops after all viewport notifications are dropped', async ({ page }) => {
  await holdTimeoutsWhile(page, '__aeonViewportDrops', 'dropping')
  await page.addInitScript(() => {
    const state = { dropping: false, rootDrops: 0 }
    ;(window as unknown as { __aeonViewportDrops: typeof state }).__aeonViewportDrops = state
    window.addEventListener('resize', event => { if (state.dropping) event.stopImmediatePropagation() }, { capture: true })
    window.visualViewport?.addEventListener('resize', event => { if (state.dropping) event.stopImmediatePropagation() }, { capture: true })
    const matchMedia = window.matchMedia.bind(window)
    window.matchMedia = query => {
      const list = matchMedia(query)
      if (query === '(min-width: 1280px)') list.addEventListener('change', event => { if (state.dropping) event.stopImmediatePropagation() }, { capture: true })
      return list
    }
    const NativeObserver = window.ResizeObserver
    window.ResizeObserver = class extends NativeObserver {
      constructor(callback: ResizeObserverCallback) {
        super((entries, observer) => {
          const delivered = entries.filter(entry => {
            if (!state.dropping || entry.target !== document.documentElement) return true
            state.rootDrops++
            return false
          })
          if (delivered.length) callback(delivered, observer)
        })
      }
    }
  })
  await mockHeaderGraph(page, sized(8, true))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  // Drain the existing two-frame follow-up before removing the next signals.
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => requestAnimationFrame(() => {
    (window as unknown as { __aeonViewportDrops: { dropping: boolean } }).__aeonViewportDrops.dropping = true
    resolve()
  })))))
  await resizeSeen(page, { width: 390, height: 844 })
  await expect.poll(() => page.evaluate(() => ({
    layout: document.documentElement.getBoundingClientRect().width,
    dropped: (window as unknown as { __aeonViewportDrops: { rootDrops: number } }).__aeonViewportDrops.rootDrops > 0,
  }))).toEqual({ layout: 390, dropped: true })
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
})

// 835d2134: 1279 → 1280 → 390 left the painted glimpse on. Resize, the media
// change and the document-element observer can all stay silent together while
// the canvas paints. The paint frame is the read that must still clear it.
test('AEON-1477 a painted glimpse hides below 1280 when resize, media and the document observer stay silent', async ({ page }) => {
  await holdTimeoutsWhile(page, '__aeonDropViewportSignals')
  await page.addInitScript(() => {
    const dropping = () => (window as unknown as { __aeonDropViewportSignals?: boolean }).__aeonDropViewportSignals === true
    window.addEventListener('resize', event => { if (dropping()) event.stopImmediatePropagation() }, { capture: true })
    window.visualViewport?.addEventListener('resize', event => { if (dropping()) event.stopImmediatePropagation() }, { capture: true })
    const originalMedia = window.matchMedia.bind(window)
    window.matchMedia = (query: string) => {
      const list = originalMedia(query)
      if (query === '(min-width: 1280px)') list.addEventListener('change', event => { if (dropping()) event.stopImmediatePropagation() }, { capture: true })
      return list
    }
    const Original = window.ResizeObserver
    window.ResizeObserver = class extends Original {
      constructor(callback: ResizeObserverCallback) {
        super((entries, observer) => {
          if (dropping() && entries.every(entry => entry.target === document.documentElement)) return
          callback(entries, observer)
        })
      }
    }
  })
  await page.setViewportSize({ width: 1600, height: 900 })
  await mockHeaderGraph(page, sized(8, true))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  await page.evaluate(() => { (window as unknown as { __aeonDropViewportSignals?: boolean }).__aeonDropViewportSignals = true })
  await page.setViewportSize({ width: 390, height: 844 })
  await expect.poll(() => page.evaluate(() => [window.innerWidth, window.matchMedia('(min-width: 1280px)').matches])).toEqual([390, false])
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
})

// The PR shard has shrunk 1280 → 390 without a usable resize or wide-media
// change for the header (AEON-1410 CI). Drop both for the shrink only: the
// layout box is the one signal left, and it must still clear the glimpse.
test('a shrink that delivers neither resize nor the wide-media change still hides the glimpse below 1280', async ({ page }) => {
  await page.addInitScript(() => {
    const dropping = () => (window as unknown as { __aeonDropViewportSignals?: boolean }).__aeonDropViewportSignals === true
    // Registered before the app, so stopping here stops every later listener.
    window.addEventListener('resize', event => { if (dropping()) event.stopImmediatePropagation() }, { capture: true })
    const original = window.matchMedia.bind(window)
    window.matchMedia = (query: string) => {
      const list = original(query)
      if (query === '(min-width: 1280px)') list.addEventListener('change', event => { if (dropping()) event.stopImmediatePropagation() }, { capture: true })
      return list
    }
  })
  await page.setViewportSize({ width: 1600, height: 900 })
  await mockHeaderGraph(page, sized(8, true))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  await page.evaluate(() => { (window as unknown as { __aeonDropViewportSignals?: boolean }).__aeonDropViewportSignals = true })
  await resizeSeen(page, { width: 390, height: 844 })
  // The layout really moved, and the header still holds the glimpse slot:
  // the off marker proves a decision, not an unmounted header.
  await expect.poll(() => page.evaluate(() => [window.innerWidth, window.matchMedia('(min-width: 1280px)').matches])).toEqual([390, false])
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
})

// Risk: the root observer fires while both window/media hints still describe
// the earlier wide viewport. Hold those hints and drop their events so only
// real layout can end painting; no sleep or renderer timing decides the test.
test('AEON-1471: a narrow root clears the glimpse while window and media hints stay wide', async ({ page }) => {
  await page.addInitScript(() => {
    const holding = () => (window as unknown as { __aeonHoldWideHints?: boolean }).__aeonHoldWideHints === true
    window.addEventListener('resize', event => { if (holding()) event.stopImmediatePropagation() }, { capture: true })
    const original = window.matchMedia.bind(window)
    window.matchMedia = (query: string) => {
      const list = original(query)
      if (query === '(min-width: 1280px)') {
        Object.defineProperty(list, 'matches', { configurable: true, get: () => holding() || original(query).matches })
        list.addEventListener('change', event => { if (holding()) event.stopImmediatePropagation() }, { capture: true })
      }
      return list
    }
  })
  await page.setViewportSize({ width: 1280, height: 900 })
  await mockHeaderGraph(page, sized(8, true))
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  await page.evaluate(() => {
    (window as unknown as { __aeonHoldWideHints?: boolean }).__aeonHoldWideHints = true
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 1280 })
  })
  await page.setViewportSize({ width: 390, height: 844 })
  await expect.poll(() => page.evaluate(() => ({
    window: window.innerWidth,
    media: window.matchMedia('(min-width: 1280px)').matches,
    layout: document.documentElement.getBoundingClientRect().width,
  }))).toEqual({ window: 1280, media: true, layout: 390 })
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveCount(0)
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
})

test.describe('reduced motion', () => {
  test.use({ reducedMotion: 'reduce' })
  test('shows nothing', async ({ page }) => {
    await mockHeaderGraph(page)
    await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
    await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
    await page.waitForTimeout(1600)
    await expect(surface(page)).toHaveCount(0)
  })
})

// Absence only counts once the developer choice has been read and rendered.
async function gotoSettled(page: Page, path: string) {
  const read = page.waitForResponse(response => new URL(response.url()).pathname === '/api/preferences/developer-ui')
  await page.goto(path)
  await read
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
}

test('the glimpse is off unless the developer setting turns it on, even after an earlier Display choice', async ({ page }) => {
  const world = ticketGraphWorld()
  delete world.work.preferences['developer-ui']
  world.work.preferences['list:display'] = { headerGraph: true }
  await mockHeaderGraph(page, world)
  await gotoSettled(page, '/p/PHAROS/tickets')
  await expect(page.locator('[data-header-glimpse]')).toHaveCount(0)
  await page.getByRole('button', { name: /^Display:/ }).click()
  await expect(page.getByRole('checkbox', { name: 'Graph in project header' })).toHaveCount(0)
  await page.keyboard.press('Escape')
  await page.goto('/settings/developer')
  const toggle = page.getByRole('switch', { name: 'Graph in project header', exact: true })
  await expect(toggle).not.toBeChecked()
  await toggle.check()
  await expect.poll(() => (world.work.preferences['developer-ui'] as { show_header_graph?: boolean } | undefined)?.show_header_graph).toBe(true)
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  await page.goto('/settings/developer')
  await page.getByRole('switch', { name: 'Graph in project header', exact: true }).uncheck()
  await expect.poll(() => (world.work.preferences['developer-ui'] as { show_header_graph?: boolean } | undefined)?.show_header_graph).toBe(false)
  await gotoSettled(page, '/p/PHAROS/tickets')
  await expect(page.locator('[data-header-glimpse]')).toHaveCount(0)
})

test('leaving the project releases the WebGL context', async ({ page }) => {
  await page.addInitScript(() => {
    const live = new Set<WebGLRenderingContext | WebGL2RenderingContext>()
    ;(window as unknown as { __aeonLiveGL: typeof live }).__aeonLiveGL = live
    const proto = HTMLCanvasElement.prototype
    const original = proto.getContext
    proto.getContext = function (this: HTMLCanvasElement, type: string, attributes?: CanvasRenderingContext2DSettings) {
      const ctx = original.call(this, type, attributes)
      if (ctx && (type === 'webgl' || type === 'webgl2' || type === 'experimental-webgl')) live.add(ctx as WebGLRenderingContext)
      return ctx
    } as typeof proto.getContext
  })
  await mockHeaderGraph(page)
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  await expect.poll(() => page.evaluate(() => (window as unknown as { __aeonLiveGL: Set<unknown> }).__aeonLiveGL.size)).toBeGreaterThan(0)
  await page.getByRole('link', { name: 'PAIMOS AEON home' }).click()
  await expect(page).toHaveURL('/')
  await expect(page.locator('[data-header-glimpse="on"] canvas')).toHaveCount(0)
  await expect.poll(() => page.evaluate(() => [...(window as unknown as { __aeonLiveGL: Set<WebGLRenderingContext> }).__aeonLiveGL].filter(gl => !gl.isContextLost()).length)).toBe(0)
})

test('long title and description stay separate from the glimpse while resizing', async ({ page }) => {
  const world = ticketGraphWorld(), project = world.work.projects[0]
  project.title = 'Pharos fleet management and operator workspace for every environment'
  project.description = 'Fleet management, service health, deployment history and host access across every environment in the INSPR family.'
  await mockHeaderGraph(page, world)
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  for (const width of [1600, 1280, 1600]) {
    await page.setViewportSize({ width, height: 1000 })
    await expectClearOfText(page)
  }
})

test('the 2D fallback also frames the dense core with faded edges', async ({ page }) => {
  await page.addInitScript(() => {
    const original = HTMLCanvasElement.prototype.getContext
    HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement, type: string, attributes?: CanvasRenderingContext2DSettings) {
      return type.includes('webgl') ? null : original.call(this, type, attributes)
    } as typeof original
  })
  await mockHeaderGraph(page)
  await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
  await ready(page)
  await expect(surface(page)).toHaveAttribute('data-dimension', '2d')
  await page.waitForTimeout(1600)
  await expectClearOfText(page)
  await expectSoftFittedGraph(page)
})

function pngPixels(buf: Buffer) {
  let offset = 8, width = 0, height = 0, channels = 4
  const idat: Buffer[] = []
  while (offset + 8 < buf.length) {
    const len = buf.readUInt32BE(offset)
    const type = buf.toString('ascii', offset + 4, offset + 8)
    const data = buf.subarray(offset + 8, offset + 8 + len)
    if (type === 'IHDR') { width = data.readUInt32BE(0); height = data.readUInt32BE(4); channels = data[9] === 2 ? 3 : 4 }
    else if (type === 'IDAT') idat.push(data)
    else if (type === 'IEND') break
    offset += 12 + len
  }
  const raw = inflateSync(Buffer.concat(idat))
  const stride = width * channels
  const out = new Uint8Array(width * height * 4)
  const paeth = (a: number, b: number, c: number) => {
    const p = a + b - c, pa = Math.abs(p - a), pb = Math.abs(p - b), pc = Math.abs(p - c)
    return pa <= pb && pa <= pc ? a : pb <= pc ? b : c
  }
  let src = 0
  let prev = new Uint8Array(stride)
  for (let y = 0; y < height; y++) {
    const filter = raw[src++]
    const row = raw.subarray(src, src + stride)
    src += stride
    const cur = new Uint8Array(stride)
    for (let i = 0; i < stride; i++) {
      const left = i >= channels ? cur[i - channels] : 0, up = prev[i], ul = i >= channels ? prev[i - channels] : 0, v = row[i]
      cur[i] = filter === 0 ? v : filter === 1 ? (v + left) & 255 : filter === 2 ? (v + up) & 255 : filter === 3 ? (v + ((left + up) >> 1)) & 255 : (v + paeth(left, up, ul)) & 255
    }
    for (let x = 0; x < width; x++) {
      out.set(cur.subarray(x * channels, x * channels + 3), (y * width + x) * 4)
      out[(y * width + x) * 4 + 3] = channels === 4 ? cur[x * channels + 3] : 255
    }
    prev = cur
  }
  return { width, height, data: out }
}
function contrast(a: [number, number, number], b: [number, number, number]) {
  const lin = (c: number) => { const s = c / 255; return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4 }
  const L = (rgb: [number, number, number]) => 0.2126 * lin(rgb[0]) + 0.7152 * lin(rgb[1]) + 0.0722 * lin(rgb[2])
  const [hi, lo] = [L(a), L(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}
async function textContrast(page: Page, selector: string) {
  const box = (await page.locator(selector).first().boundingBox())!
  const shot = await page.screenshot({ clip: { x: box.x, y: box.y, width: Math.max(8, box.width), height: Math.max(8, box.height) } })
  const { width, height, data } = pngPixels(shot)
  let dark = 255, light = 0
  let darkPx: [number, number, number] = [0, 0, 0], lightPx: [number, number, number] = [255, 255, 255]
  const lum = (r: number, g: number, b: number) => 0.2126 * r + 0.7152 * g + 0.0722 * b
  for (let i = 0; i < width * height; i++) {
    const px: [number, number, number] = [data[i * 4], data[i * 4 + 1], data[i * 4 + 2]]
    const value = lum(px[0], px[1], px[2])
    if (value < dark) { dark = value; darkPx = px }
    if (value > light) { light = value; lightPx = px }
  }
  return contrast(darkPx, lightPx)
}

async function expectSoftFittedGraph(page: Page, checkExtent = false) {
  const pinned = await surface(page).getAttribute('data-glimpse-pin')
  await page.locator('.project-head').hover()
  await page.getByRole('button', { name: 'Pause motion', exact: true }).click()
  await page.mouse.move(0, 0)
  if (!pinned) await page.waitForTimeout(400)
  await expect(surface(page)).toHaveAttribute('data-motion', 'still')
  const layer = page.locator('.glimpse-canvas'), clip = (await layer.boundingBox())!
  // Isolate painted graph pixels from browser text antialiasing: hiding an
  // opacity layer can change the rasterization of otherwise identical text.
  // Visibility preserves every measured exclusion box and the current mask.
  const hiddenText = await page.addStyleTag({ content: `${protectedSelectors.join(', ')} { visibility: hidden !important; }` })
  const masked = pngPixels(await page.screenshot({ clip }))
  // Height-led framing intentionally lets outliers enter the surrounding fade.
  const original = await layer.getAttribute('style')
  await layer.evaluate(el => { (el as HTMLElement).style.opacity = '0' })
  const background = pngPixels(await page.screenshot({ clip }))
  await layer.evaluate((el, style) => el.setAttribute('style', style ?? ''), original)
  await hiddenText.evaluate(el => el.remove())
  for (const [name, pixels] of [['masked', masked]] as const) {
    const points: { x: number; y: number }[] = []
    for (let y = 0; y < pixels.height; y++) for (let x = 0; x < pixels.width; x++) {
      const i = (y * pixels.width + x) * 4
      if ([0, 1, 2].some(c => Math.abs(pixels.data[i + c] - background.data[i + c]) > 2)) points.push({ x, y })
    }
    expect(points.length, `${name}: graph actually paints`).toBeGreaterThan(80)
    if (checkExtent) {
      const free = (await page.locator('.glimpse-col').boundingBox())!
      const width = Math.max(...points.map(p => p.x)) - Math.min(...points.map(p => p.x))
      const height = Math.max(...points.map(p => p.y)) - Math.min(...points.map(p => p.y))
      await test.info().attach(`cloud-${page.viewportSize()!.width}`, { body: JSON.stringify({ width, height, freeWidth: free.width, headerHeight: free.height }), contentType: 'application/json' })
      // The flow pill no longer reserves a header row. Height-led framing on the
      // shorter column paints a cloud just under the old 80% width at 1600.
      const span = (page.viewportSize()?.width ?? 0) >= 1600 ? .78 : .80
      expect.soft(width / free.width, 'cloud spans the header width').toBeGreaterThanOrEqual(span)
      expect.soft(height / free.height, 'cloud spans the header height').toBeGreaterThanOrEqual(.65)
    }
    expect(Math.min(...points.map(p => p.x)), `${name}: left padding`).toBeGreaterThanOrEqual(8)
    // Height-led framing lets a side outlier fade out. On a wide header that no
    // longer reserves a row for the flow pill, that outlier sits a few pixels
    // closer to the edge than the old 8px inset.
    const rightInset = (page.viewportSize()?.width ?? 0) >= 1600 ? 2 : 8
    expect(Math.max(...points.map(p => p.x)), `${name}: right padding`).toBeLessThan(pixels.width - rightInset)
    const boxes = await protectedBoxes(page)
    const intrusions = points.filter(point => boxes.some(box => point.x + clip.x >= box.x && point.x + clip.x < box.x + box.width && point.y + clip.y >= box.y && point.y + clip.y < box.y + box.height))
    expect(intrusions, 'no graph pixels inside a protected bounding box').toHaveLength(0)
  }
}

for (const scheme of ['light', 'dark'] as const) {
  test.describe(`header shots ${scheme}`, () => {
    test.use({ colorScheme: scheme })
    for (const width of [1600, 1280, 390]) test(`keeps AA contrast at ${width}`, async ({ page }, testInfo) => {
      const errors: string[] = []
      page.on('pageerror', error => errors.push(error.message))
      const world = await mockHeaderGraph(page)
      if (width >= 1280) await page.addInitScript(`window.__aeonGlimpsePin = { frames: ${GLIMPSE_PIN_FRAMES} }`)
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.goto('/p/PHAROS/tickets'); await showProjectHead(page)
      if (width >= 1280) await ready(page)
      else await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
      if (width >= 1280) await releasePinnedGlimpse(page)
      if (width >= 1280) {
        await expectClearOfText(page)
        await expect(surface(page)).toHaveAttribute('data-dimension', '3d')
        await expect(page.locator('.glimpse-canvas')).toHaveCSS('opacity', scheme === 'light' ? '0.35' : '0.3')
      }
      await page.screenshot({ path: testInfo.outputPath(`header-${width}-${scheme}.png`) })
      if (width >= 1280) {
        await expectSoftFittedGraph(page, true)
        await page.locator('.project-head').hover()
        await page.screenshot({ path: testInfo.outputPath(`header-${width}-${scheme}-hover.png`) })
      }
      for (const selector of ['#project-title', '.description', '.group-count b']) {
        expect(await textContrast(page, selector), `${selector} ${width} ${scheme}`).toBeGreaterThanOrEqual(4.5)
      }
      expect(errors).toEqual([])
    })
  })
}
