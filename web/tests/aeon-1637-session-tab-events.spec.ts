// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1637 exploration: leaving Events on a lead session whose native context is feature_disabled.
import { test, expect, type Page } from '@playwright/test'
import { mockLeadFlow, LEAD_SESSION } from './lead-flow-fixtures'

const panel = (page: Page) => page.locator('aside.session-panel')
const tab = (page: Page, name: string) => panel(page).getByRole('tab', { name: new RegExp(`^${name}`) })

async function setup(page: Page, preferences: Record<string, Record<string, unknown>> = {}) {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(`pageerror: ${error.message}\n${error.stack ?? ''}`))
  page.on('console', message => { if (message.type() === 'error' && !/Failed to load resource/.test(message.text())) errors.push(`console: ${message.text()} @ ${JSON.stringify(message.location())}`) })
  const world = await mockLeadFlow(page, { lead: 'working' })
  Object.assign(world.data.preferences, preferences)
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (['/api/models', '/api/approvals', '/api/agent-accounts', '/api/agent-accounts/capacity', '/api/agent-accounts/capacity/schedule'].includes(path)) return route.fulfill({ json: [] })
    if (path.endsWith('/native-context')) return route.fulfill({ status: 503, json: { reason: 'feature_disabled' } })
    return route.fallback()
  })
  return errors
}

const entries: { name: string; open: (page: Page) => Promise<void>; preferences?: Record<string, Record<string, unknown>> }[] = [
  { name: 'agents-link', open: async page => { await page.goto(`/agents/${LEAD_SESSION}?tab=events`) } },
  { name: 'agents-tap', open: async page => { await page.goto(`/agents/${LEAD_SESSION}`); await expect(tab(page, 'Lead')).toBeVisible(); await tab(page, 'Events').click() } },
  { name: 'agents-pinned-chat-remembered-events', preferences: { 'view:lead-panel': { last: 'events', default: 'chat' } }, open: async page => { await page.goto(`/agents/${LEAD_SESSION}`); await expect(tab(page, 'Chat')).toHaveAttribute('aria-selected', 'true'); await tab(page, 'Events').click() } },
  { name: 'overlay-tap', open: async page => {
    await page.goto('/agents'); await expect(page.locator(`[data-row="s:${LEAD_SESSION}"]`)).toBeVisible()
    await page.evaluate(async () => { const { openLeadPanel } = await import('/src/lib/leadOverlay.ts' /* @vite-ignore */); openLeadPanel('p-pharos') })
    await expect(tab(page, 'Lead')).toBeVisible(); await tab(page, 'Events').click()
  } },
]

for (const width of [390, 1440]) for (const entry of entries) {
  test(`AEON-1637 explore ${width} ${entry.name}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    const errors = await setup(page, entry.preferences)
    await entry.open(page)
    await expect(tab(page, 'Events')).toHaveAttribute('aria-selected', 'true')
    await expect(panel(page)).toContainText('Live events are not available for this session yet.')
    for (const target of ['Lead', 'Details', 'Chat']) {
      await tab(page, target).click()
      await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
      expect.soft(await tab(page, target).getAttribute('aria-selected'), `${target} selected`).toBe('true')
      expect.soft(await page.locator('.toast').allTextContents(), `${target} toasts`).toEqual([])
      await page.screenshot({ path: info.outputPath(`after-${target}.png`) })
      await tab(page, 'Events').click()
      await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
    }
    expect.soft(errors.join('\n----\n')).toBe('')
  })
}
