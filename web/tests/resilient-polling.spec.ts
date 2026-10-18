// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
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

// The page counts every session-list read whose request has settled. Seeing a
// refresh's third attempt on the network is not enough: the next 'online'
// restarts polling and drops a read whose failure has not landed yet, so that
// failure would never count towards the three.
async function countSettledReads(page: Page) {
  await page.addInitScript(() => {
    const list = /\/api\/harness-sessions\?[^/]*$/
    const counter = window as unknown as { settledSessionReads: number }
    counter.settledSessionReads = 0
    const original = window.fetch.bind(window)
    window.fetch = (input, init) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
      const answer = original(input, init)
      return list.test(url) ? answer.finally(() => { counter.settledSessionReads++ }) : answer
    }
  })
}
// Every read the route has seen so far has settled in the page. The app handles
// a settled read in microtasks; a later task runs only after they are done.
async function settledReads(page: Page, count: number) {
  await page.waitForFunction(n => (window as unknown as { settledSessionReads: number }).settledSessionReads >= n, count)
  await page.evaluate(() => new Promise(resolve => setTimeout(resolve)))
}

test('Agents retries a stalled session read and keeps the loaded table through three failed refreshes', async ({ page }) => {
  await countSettledReads(page)
  await agents(page)
  let attempts = 0
  let fail = false
  await page.route(sessions, async route => {
    attempts++
    if (attempts === 1) return route.abort('timedout')
    if (fail) return route.abort('failed')
    return route.fallback()
  })
  await page.goto('/agents')
  await expect(sessionRows(page).first()).toBeVisible()
  expect(attempts).toBeGreaterThanOrEqual(2)
  fail = true
  for (let n = 1; n <= 3; n++) {
    const before = attempts
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    await expect.poll(() => attempts).toBeGreaterThanOrEqual(before + 3)
    await settledReads(page, attempts)
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
  // The live read includes inactive sessions, so the URL is /live?include_inactive=true.
  await page.route('**/api/harness-sessions/live*', async route => {
    attempts++
    if (attempts === 1) return route.abort('timedout')
    return route.fallback()
  })
  await page.goto('/')
  await expect(page.locator('[data-project-id="p-aeon"] .live-chip').first()).toBeVisible()
  expect(attempts).toBeGreaterThanOrEqual(2)
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true }); document.dispatchEvent(new Event('visibilitychange')) })
  const before = attempts
  await page.clock.fastForward(65_000)
  expect(attempts).toBe(before)
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true }); document.dispatchEvent(new Event('visibilitychange')) })
  await expect.poll(() => attempts).toBeGreaterThan(before)
  await context.setOffline(true)
  const offline = attempts
  await page.clock.fastForward(21_000)
  expect(attempts).toBe(offline)
  await context.setOffline(false)
  await expect.poll(() => attempts).toBeGreaterThan(offline)
})
