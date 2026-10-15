// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1352: fast switches between the project sections hung the tab on Delivery, its cards stayed
// on skeletons. Risks: Delivery reads again and reopens its hint stream whenever the project page
// renders, so fast switches stack reads and streams; Delivery code blocks the main thread; the
// cards never leave their skeletons. Counts are per visit, so they hold however fast the clicks land.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { deliveryMetrics, mockDelivery } from './delivery-numbers-fixtures'
import { mockFlow, ZONE } from './delivery-flow-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'

test.use({ timezoneId: ZONE })
const sections = (page: Page) => page.getByRole('tablist', { name: 'Project sections' })
const CYCLES = 20
// Delivery's own sources, as the dev server names them.
const DELIVERY_CODE = /\/src\/(components\/delivery\/|lib\/(delivery|useDeliveryFlow))/

interface Frame { duration: number; scripts: { url: string; fn: string; invoker: string; ms: number }[] }
interface Probe { frames: Frame[]; flowStreams: { opened: number; open: number; most: number } }

/** Long animation frames with their scripts, and every flow hint stream the page opens and closes. */
async function probe(page: Page) {
  await page.addInitScript(() => {
    const state = { frames: [] as unknown[], flowStreams: { opened: 0, open: 0, most: 0 } }
    Object.assign(window, { deliveryProbe: state })
    try {
      new PerformanceObserver(list => {
        for (const entry of list.getEntries() as unknown as { duration: number; scripts: { sourceURL: string; sourceFunctionName: string; invoker: string; duration: number }[] }[]) {
          if (entry.duration > 200) state.frames.push({ duration: Math.round(entry.duration), scripts: entry.scripts.map(s => ({ url: s.sourceURL, fn: s.sourceFunctionName, invoker: s.invoker, ms: Math.round(s.duration) })) })
        }
      }).observe({ type: 'long-animation-frame', buffered: true })
    } catch { /* Chromium has it; the counts below still hold */ }
    // Whatever EventSource the page ends up with (the fixtures install a quiet one), count the flow streams.
    let current: typeof EventSource | undefined
    const wrap = (Base: typeof EventSource) => class extends Base {
      private flow: boolean
      private closedOnce = false
      constructor(url: string | URL, init?: EventSourceInit) {
        super(url, init)
        this.flow = String(url).includes('/delivery/flow/stream')
        if (this.flow) { state.flowStreams.opened++; state.flowStreams.open++; state.flowStreams.most = Math.max(state.flowStreams.most, state.flowStreams.open) }
      }
      close() {
        if (this.flow && !this.closedOnce) { this.closedOnce = true; state.flowStreams.open-- }
        super.close()
      }
    }
    const native = window.EventSource
    Object.defineProperty(window, 'EventSource', {
      configurable: true,
      get: () => current,
      set: (Base: typeof EventSource) => { current = Base && Object.defineProperty(wrap(Base), 'name', { value: 'EventSource' }) },
    })
    window.EventSource = native
  })
}

async function setup(page: Page, flow: 'empty' | 'recorded') {
  await probe(page)
  const errors = watchErrors(page)
  const work = fixtures()
  work.preferences['delivery:numbers'] = { level: 'simple', window: 30 }
  await mockWork(page, work)
  await mockKnowledge(page, knowledgeWorld())
  await mockDelivery(page, async () => ({ status: 200, body: deliveryMetrics() }), ['delivery.read'])
  const runs = await mockFlow(page, { empty: flow === 'empty' })
  let metrics = 0
  page.on('request', request => { if (request.url().includes('/delivery/metrics')) metrics++ })
  return { errors, reads: () => ({ metrics, flow: runs.reads.length }) }
}

/** Tickets → Knowledge → Delivery → Settings as fast as the clicks land, then back to Delivery. */
async function switchFast(page: Page) {
  for (let cycle = 0; cycle < CYCLES; cycle++) {
    for (const name of ['Tickets', 'Knowledge', 'Delivery', 'Settings']) await sections(page).getByRole('tab', { name, exact: true }).click()
  }
  await sections(page).getByRole('tab', { name: 'Delivery', exact: true }).click()
  // Visits to Delivery: the first load, one per cycle and the last.
  return CYCLES + 2
}
const result = (page: Page) => page.evaluate(() => (window as unknown as { deliveryProbe: Probe }).deliveryProbe)
function fromDelivery(frames: Frame[]) {
  return frames.filter(frame => frame.scripts.some(script => DELIVERY_CODE.test(script.url)))
}

test('Numbers: fast section switches keep Delivery responsive and its cards render', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  await page.setViewportSize({ width: 1440, height: 1000 })
  const world = await setup(page, 'empty')
  await page.goto('/p/AEON/delivery')
  await expect(page.getByTestId('delivery-summary')).toBeVisible()
  await expect(page.locator('.dl [aria-busy="true"]')).toHaveCount(0)

  const visits = await switchFast(page)
  await expect(page).toHaveURL(/\/p\/AEON\/delivery$/)
  // The cards leave their skeletons.
  await expect(page.getByTestId('delivery-summary')).not.toHaveAttribute('aria-busy', 'true')
  await expect(page.locator('.dl [aria-busy="true"]')).toHaveCount(0)
  await expect(page.locator('.dl .sk')).toHaveCount(0)
  await expect(page.locator('.s-tile .s-val').first()).toBeVisible()

  const probed = await result(page)
  await testInfo.attach('long-frames.json', { body: JSON.stringify(probed.frames, null, 1), contentType: 'application/json' })
  expect(fromDelivery(probed.frames), 'no long frame over 200 ms runs Delivery code').toEqual([])
  // One numbers read per visit at most; Numbers never opens the flow stream.
  expect(world.reads().metrics).toBeLessThanOrEqual(visits)
  expect(probed.flowStreams.opened).toBe(0)
  expect(world.errors).toEqual([])
})

test('Flow: fast section switches keep one hint stream per mounted view and its lanes render', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  await page.setViewportSize({ width: 1440, height: 1000 })
  const world = await setup(page, 'recorded')
  await page.goto('/p/AEON/delivery?view=flow')
  await expect(page.getByTestId('flow-moment')).toBeVisible()

  // Delivery keeps ?view=flow while the person moves around the project, so every visit mounts Flow.
  const visits = await switchFast(page)
  await expect(page).toHaveURL(/\/p\/AEON\/delivery\?view=flow$/)
  await expect(page.getByTestId('flow-moment')).toBeVisible()
  await expect(page.locator('.dl .sk')).toHaveCount(0)

  const probed = await result(page)
  await testInfo.attach('long-frames.json', { body: JSON.stringify(probed.frames, null, 1), contentType: 'application/json' })
  expect(fromDelivery(probed.frames), 'no long frame over 200 ms runs Delivery code').toEqual([])
  // One stream per mounted view: at most one per visit, never two at once, and only the shown view's stays open.
  expect(probed.flowStreams.opened).toBeLessThanOrEqual(visits)
  expect(probed.flowStreams.most).toBe(1)
  expect(probed.flowStreams.open).toBe(1)
  // Live reads the run list and the last hours once per visit; parent renders add nothing.
  expect(world.reads().flow).toBeLessThanOrEqual(2 * visits)
  expect(world.errors).toEqual([])
})
