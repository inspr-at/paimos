// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`inbox capability ${theme} ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const work = fixtures()
    work.preferences.theme = { choice: theme }
    await mockWork(page, work, { admin: true })
    const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
    const oneShot = data.sessions[3]!
    oneShot.advertised_capabilities = ['status', 'stop']
    oneShot.management_mode = 'managed'
    oneShot.run_id = 'one-shot-run'
    const interactive = data.sessions[0]!
    interactive.advertised_capabilities = ['status']
    interactive.run_id = null
    const managed = data.sessions[1]!
    managed.activity = 'idle'
    await mockAgents(page, data)
    await page.goto('/agents')
    const row = page.locator(`[data-row="s:${oneShot.id}"]`)
    await expect(row.getByText('No inbox', { exact: true })).toBeVisible()
    await expect(row.locator('.no-inbox')).toHaveAttribute('title', /managed worker/)
    await expect(page.locator(`[data-row="s:${managed.id}"] .no-inbox`)).toHaveCount(0)
    await expect(page.locator(`[data-row="s:${interactive.id}"] .no-inbox`)).toHaveCount(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
    const box = await row.locator('.no-inbox').boundingBox()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(width)
    const out = process.env.INBOX_SHOTS
    if (out) {
      mkdirSync(out, { recursive: true })
      await row.scrollIntoViewIfNeeded()
      await page.screenshot({ path: resolve(out, `inbox-${theme}-${width}.png`), fullPage: true })
    }
  })
}
