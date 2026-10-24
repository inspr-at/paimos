// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page, type Route } from '@playwright/test'
import { agentData, mockAgents } from './agents-fixtures'
import { fixtures, liveAgent, me, mockWork } from './work-fixtures'

async function agents(page: Page) {
  await mockWork(page, fixtures())
  await mockAgents(page, agentData({
    me: me.id,
    projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
    tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  }))
}
const sessions = '**/api/harness-sessions?*'
const sessionRows = (page: Page) => page.locator('.sessions [data-row^="s:"]')

test('Agents retries a stalled session read and keeps the loaded table through three failed refreshes', async ({ page }) => {
  await agents(page)
  let attempts = 0
  let fail = false
  await page.route(sessions, async route => {
    const query = new URL(route.request().url()).searchParams
    // Project-tree reads share this endpoint but do not refresh the table.
    if (query.get('view') !== 'current' || query.has('project') || query.has('ticket') || query.has('agent')) return route.fallback()
    attempts++
    if (attempts === 1) return route.abort('timedout')
    if (fail) return route.abort('failed')
    return route.fallback()
  })
  await page.goto('/agents')
  await expect(sessionRows(page).first()).toBeVisible()
  expect(attempts).toBeGreaterThanOrEqual(2)
  await expect.poll(() => page.evaluate(async () => {
    const agents = (await import('/src/stores/agents.ts')).useAgents()
    return agents.loaded && !agents.loading
  })).toBe(true)
  fail = true
  for (let n = 1; n <= 3; n++) {
    const before = attempts
    // Join the table's complete refresh, including its status update. Counting
    // transport settlements can include a different reader or finish before
    // the last retry; another online event would then invalidate that failure.
    // Observe its action without starting a read, so a missing online handler fails.
    const error = await page.evaluate(async () => {
      const agents = (await import('/src/stores/agents.ts')).useAgents()
      const refresh = new Promise<void>((resolve, reject) => {
        const stop = agents.$onAction(({ name, after, onError }) => {
          if (name !== 'loadAll') return
          stop()
          after(() => resolve())
          onError(reject)
        })
        window.dispatchEvent(new Event('online'))
      })
      // Project-tree readers can finish while the table GET is in backoff.
      // Complete both readers as well as the table action: their transports
      // cannot stand in for the table's completed failure.
      await Promise.all([
        refresh,
        ...['p-pharos', 'p-aeon'].map(project => fetch(`/api/harness-sessions?view=current&project=${project}`).catch(() => {})),
      ])
      return agents.sessionsError
    })
    expect(error).toBe('No connection')
    await expect.poll(() => attempts).toBeGreaterThanOrEqual(before + 3)
    await expect(page.getByRole('region', { name: 'Agents', exact: true }).getByRole('status').filter({ hasText: 'Update delayed' })).toBeVisible()
    await expect(sessionRows(page).first()).toBeVisible()
    if (n < 3) await expect(page.getByText('Sessions could not be refreshed:')).toHaveCount(0)
  }
  await expect(page.getByText('Sessions could not be refreshed:', { exact: false })).toBeVisible()
  await expect(sessionRows(page).first()).toBeVisible()
  fail = false
  await page.getByRole('button', { name: 'Try again' }).last().click()
  await expect(page.getByRole('region', { name: 'Agents', exact: true }).getByRole('status').filter({ hasText: 'Update delayed' })).toHaveCount(0)
  await expect(page.getByText('Sessions could not be refreshed:', { exact: false })).toHaveCount(0)
})

test('Live Agents pauses in a hidden tab and refreshes when shown and back online', async ({ page, context }) => {
  const data = fixtures()
  data.live.push(liveAgent({ project_id: 'p-aeon', name: 'aeon-worker' }))
  await mockWork(page, data)
  let attempts = 0
  let holdWakeRead = false
  let wakeRead!: Route
  let markWakeRead!: () => void
  const wakeReadStarted = new Promise<void>(resolve => { markWakeRead = resolve })
  // The live read includes inactive sessions, so the URL is /live?include_inactive=true.
  await page.route('**/api/harness-sessions/live*', async route => {
    attempts++
    if (attempts === 1) return route.abort('timedout')
    if (holdWakeRead) { holdWakeRead = false; wakeRead = route; markWakeRead(); return }
    return route.fallback()
  })
  await page.goto('/')
  await expect(page.locator('[data-project-id="p-aeon"] .live-chip').first()).toBeVisible()
  expect(attempts).toBeGreaterThanOrEqual(2)
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true }); document.dispatchEvent(new Event('visibilitychange')) })
  const before = attempts
  await page.clock.fastForward(65_000)
  expect(attempts).toBe(before)
  holdWakeRead = true
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true }); document.dispatchEvent(new Event('visibilitychange')) })
  await wakeReadStarted
  await expect.poll(() => attempts).toBeGreaterThan(before)
  await context.setOffline(true)
  await expect.poll(() => page.evaluate(() => navigator.onLine)).toBe(false)
  const offline = attempts
  // Fail the read that began while visible only after the browser is offline.
  // Its GET backoff must stay quiet too, not just the next polling tick.
  const failed = page.waitForEvent('requestfailed', request => request === wakeRead.request())
  await wakeRead.abort('internetdisconnected')
  await failed
  await page.clock.runFor(1_000)
  await page.clock.fastForward(21_000)
  expect(attempts).toBe(offline)
  await context.setOffline(false)
  await expect.poll(() => attempts).toBeGreaterThan(offline)
})
