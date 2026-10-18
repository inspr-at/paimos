// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import { hostname, platform, arch } from 'node:os'
import { execFileSync } from 'node:child_process'
import { graphMeasurementFault, instrumentationSource, measurementPass, measurementReducedMotion } from './measure.mjs'

const actions = ['list', 'outline', 'graph', 'return-list', 'filter', 'sort', 'search', 'hide-closed', 'open-ticket', 'close-ticket', 'decision-desk', 'agents', 'header-hover', 'scroll']
const listReady = { type: 'list' }
const graphReady = { type: 'graph' }
const ticketReady = { type: 'ticket' }

async function failGraphDimension(page) {
  const dimension = await page.evaluate(() => window.__baselineGraphDimension || '')
  if (!dimension) return
  const fault = graphMeasurementFault(dimension)
  if (fault) throw new Error(fault)
}
async function waitReady(page, requirement) {
  await page.waitForFunction(requirement => window.__baselineGraphDimension || window.__baselineReady(requirement), requirement)
  await failGraphDimension(page)
}
async function waitUsable(page) {
  await page.waitForFunction(() => window.__baselineGraphDimension || window.__baseline.usable !== null)
  await failGraphDimension(page)
}

async function idle(page) {
  await page.waitForFunction(() => !document.querySelector('table[aria-busy="true"], .head-skeleton, .ws-skeleton, [aria-label="Loading sessions"]'))
}
async function nativeClick(locator, predicate) {
  await expect(locator).toBeVisible()
  await locator.evaluate((element, source) => { window.__baselineArm(source); element.click() }, predicate)
}
async function setSearch(page, value, measured) {
  const input = page.getByRole('searchbox', { name: 'Search tickets in this project' })
  await expect(input).toBeVisible()
  await input.evaluate((element, { value, measured, listReady }) => {
    if (measured) window.__baselineArm({ ...listReady, query: ['q', value] })
    element.focus()
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(element, value)
    element.dispatchEvent(new Event('input', { bubbles: true }))
  }, { value, measured, listReady })
  if (!measured) {
    await page.waitForFunction(value => new URLSearchParams(location.search).get('q') === value, value)
    await idle(page)
  }
}
async function prepare(page, project, action) {
  await page.goto(`/p/${project}/tickets`, { waitUntil: 'commit' })
  await waitReady(page, listReady)
  if (action === 'return-list') {
    await page.getByRole('tab', { name: 'Outline', exact: true }).click()
    await waitReady(page, { ...listReady, role: 'treegrid' })
  }
  if (action === 'close-ticket') {
    await page.locator('tr.ticket-row:not(.ghost) .title-link').first().click()
    await waitReady(page, ticketReady)
  }
  if (action === 'filter') {
    await page.getByRole('button', { name: 'Add a filter', exact: true }).click()
    await page.getByRole('menu', { name: 'Filter by' }).getByRole('menuitem', { name: 'Priority', exact: true }).click()
    await expect(page.getByRole('checkbox', { name: /^High/ })).toBeVisible()
  }
  await idle(page)
}
async function act(page, project, action, measure = true, cache = 'cold') {
  const armClick = async (locator, predicate) => {
    if (measure) await nativeClick(locator, predicate)
    else { await locator.click(); await waitReady(page, predicate) }
  }
  if (action === 'list') {
    if (measure && cache === 'warm') {
      await armClick(page.locator(`a[href^="/p/${project}"]`).first(), listReady)
      await waitUsable(page)
      return
    }
    // Document first paint is measured from the Navigation Timing origin.
    await page.addInitScript(({ listReady }) => {
      addEventListener('DOMContentLoaded', () => {
        window.__baselineArm(listReady); window.__baseline.start = 0
      }, { once: true })
    }, { listReady })
    await page.goto(`/p/${project}/tickets`, { waitUntil: 'commit' })
  } else if (['outline', 'graph', 'return-list'].includes(action)) {
    const name = action === 'outline' ? 'Outline' : action === 'graph' ? 'Graph' : 'List'
    const ready = action === 'graph' ? graphReady : { ...listReady, role: action === 'outline' ? 'treegrid' : 'grid' }
    await armClick(page.getByRole('tab', { name, exact: true }), ready)
  } else if (action === 'filter') {
    await armClick(page.getByRole('checkbox', { name: /^High/ }), { ...listReady, query: ['priority', 'high'] })
  } else if (action === 'sort') {
    const header = page.locator('th[data-column-id="title"]')
    const previous = await header.getAttribute('aria-sort')
    await armClick(header.locator('button.th-sort'), { ...listReady, sortBefore: previous })
  } else if (action === 'search') await setSearch(page, 'Synthetic task 12', measure)
  else if (action === 'hide-closed') {
    const checkbox = page.getByRole('checkbox', { name: 'Hide closed', exact: true })
    const checked = await checkbox.isChecked()
    await armClick(checkbox, { ...listReady, checked: !checked })
  } else if (action === 'open-ticket') {
    await armClick(page.locator('tr.ticket-row:not(.ghost) .title-link').first(), ticketReady)
  } else if (action === 'close-ticket') {
    await armClick(page.getByRole('button', { name: 'Close ticket details', exact: true }), { ...listReady, absent: '.ticket-ws' })
  } else if (action === 'decision-desk' || action === 'agents') {
    const desk = action === 'decision-desk'
    await armClick(page.locator(`a[href="/${desk ? 'decision-desk' : 'agents'}"]`).first(), { type: desk ? 'desk' : 'agents' })
  } else if (action === 'header-hover') {
    const header = page.locator('th[data-column-id="title"] button.th-sort')
    await page.mouse.move(0, 0)
    await header.evaluate((element, measure) => {
      if (measure) element.addEventListener('pointerover', () => window.__baselineArm({ type: 'hover' }), { once: true })
    }, measure)
    await header.hover()
    if (!measure) await expect(page.locator('.tooltip')).toBeVisible()
  } else if (action === 'scroll') {
    await page.locator('#main').evaluate((element, measure) => {
      if (measure) window.__baselineArm({ type: 'scroll' })
      element.scrollTop = 300
    }, measure)
    if (!measure) await waitReady(page, { type: 'scroll' })
  }
  if (measure) await waitUsable(page)
  await idle(page)
}

async function chromeTrace(cdp, testInfo, filename) {
  const complete = new Promise(resolve => cdp.once('Tracing.tracingComplete', resolve))
  await cdp.send('Tracing.end')
  const { stream } = await complete
  let raw = ''
  for (;;) {
    const part = await cdp.send('IO.read', { handle: stream, size: 1048576 })
    raw += part.base64Encoded ? Buffer.from(part.data, 'base64').toString('utf8') : part.data
    if (raw.length > 64 * 1024 * 1024) throw Error('Trace exceeded 64 MiB bound')
    if (part.eof) break
  }
  await cdp.send('IO.close', { handle: stream })
  // Chrome JSON is kept with a .md suffix because LEAD's artifact transport
  // carries .md/.png only. Rename to .json to open in Chrome Performance.
  const trace = JSON.parse(raw)
  const safe = JSON.stringify(trace, (key, value) => /headers|cookie|postData|authorization/i.test(key) ? undefined : value)
  await writeFile(testInfo.outputPath(filename), safe)
}

for (const project of ['AIT', 'AEON']) for (const cache of ['cold', 'warm']) for (const action of actions) {
  test(`AEON-1355 ${project} ${cache} ${action}`, async ({ browser, baseURL }, testInfo) => {
    const context = await browser.newContext({ baseURL, viewport: { width: 1440, height: 1000 }, reducedMotion: measurementReducedMotion })
    // Cookies are generated for this local synthetic person and never logged,
    // saved as storage state, or included in measured network/trace artifacts.
    const login = await context.request.post('/api/auth/dev-login', { data: { email: 'perf@example.invalid', tenant: 'perf1355' } })
    expect(login.ok()).toBe(true)
    const page = await context.newPage()
    await page.addInitScript({ content: instrumentationSource })
    const cdp = await context.newCDPSession(page)
    await cdp.send('Network.enable')
    if (cache === 'cold') await cdp.send('Network.setCacheDisabled', { cacheDisabled: true })
    try {
      if (action !== 'list') await prepare(page, project, action)
      if (cache === 'warm') {
        await act(page, project, action, false)
        // Reload prerequisites in the same context: HTTP/V8 caches are warm;
        // this deliberately does not claim a retained Pinia list cache.
        if (action !== 'list') await prepare(page, project, action)
        else {
          await page.locator('a[href="/"]').first().click()
          await expect(page.locator(`a[href^="/p/${project}"]`).first()).toBeVisible()
        }
      }
      const requests = new Map()
      cdp.on('Network.requestWillBeSent', event => {
        const url = new URL(event.request.url)
        if (url.origin !== baseURL) throw Error('Unexpected external measured request')
        requests.set(event.requestId, { path: url.pathname + url.search, method: event.request.method, start: event.timestamp, type: event.type })
      })
      cdp.on('Network.responseReceived', event => {
        const request = requests.get(event.requestId)
        if (request) Object.assign(request, { status: event.response.status, cached: !!(event.response.fromDiskCache || event.response.fromServiceWorker), timing: event.response.timing })
      })
      cdp.on('Network.loadingFinished', event => {
        const request = requests.get(event.requestId)
        if (request) Object.assign(request, { end: event.timestamp, bytes: event.encodedDataLength })
      })
      cdp.on('Network.loadingFailed', event => {
        const request = requests.get(event.requestId)
        if (request) Object.assign(request, { failed: event.errorText })
      })
      await cdp.send('Tracing.start', { categories: 'devtools.timeline,v8,blink.user_timing,disabled-by-default-v8.cpu_profiler', transferMode: 'ReturnAsStream' })
      await act(page, project, action, true, cache)
      if (action === 'graph') {
        const dimension = await page.locator('.ticket-graph-canvas').getAttribute('data-dimension')
        const fault = graphMeasurementFault(dimension)
        if (fault) throw new Error(fault)
      }
      const metrics = await page.evaluate(() => {
        const m = window.__baseline
        m.tasks.push(...window.__baselineObserver.takeRecords().map(entry => ({ start: entry.startTime, duration: entry.duration })))
        return { first_paint_ms: m.start === 0 ? performance.getEntriesByName('first-paint')[0]?.startTime ?? null : m.paint - m.start,
          usable_ms: m.usable - m.start,
          long_tasks: m.tasks.filter(entry => entry.start < m.usable && entry.start + entry.duration > m.start).map(entry => ({ start_ms: entry.start - m.start, duration_ms: entry.duration })) }
      })
      await chromeTrace(cdp, testInfo, 'chrome-trace.json.md')
      const waterfall = [...requests.values()]
      const first = Math.min(...waterfall.map(request => request.start))
      for (const request of waterfall) {
        request.start_ms = (request.start - first) * 1000
        request.duration_ms = request.end ? (request.end - request.start) * 1000 : null
        delete request.start; delete request.end
      }
      const budget = action === 'list' ? (cache === 'cold' ? 1000 : 300) : 100
      const row = { project, cache, action, sample: 1, source_sha: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), host: hostname(), platform: platform(), arch: arch(), chromium: browser.version(),
        ...metrics, budget_ms: budget, pass: measurementPass(action, metrics.usable_ms, budget, metrics.long_tasks),
        request_count: waterfall.length, network: waterfall }
      await writeFile(testInfo.outputPath('metrics.json.md'), JSON.stringify(row, null, 2))
      console.log(`AEON1355 ${JSON.stringify({ project, cache, action, usable_ms: Math.round(metrics.usable_ms), long_tasks: metrics.long_tasks.length })}`)
    } finally { await context.close() }
  })
}
