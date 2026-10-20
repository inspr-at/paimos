// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1460: controls must not move while a phone list is searched, filtered or updated.
import { test, expect } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import { fixtures, mockWork } from './work-fixtures'

test('AEON-1460 design truth and main geometry before the phone changes', async ({ page }, info) => {
  test.setTimeout(120_000)
  await page.setViewportSize({ width: 390, height: 664 })
  await page.goto('/tests/fixtures/aeon-1460-design.html?v=tickets')
  await expect(page.locator('.tk-bar')).toBeVisible()
  await page.evaluate(() => document.fonts.ready)
  const truth = await page.locator('.tk-bar, .tk-row').evaluateAll(items => items.slice(0, 4).map(el => {
    const r = el.getBoundingClientRect()
    return { name: el.className, x: r.x, y: r.y, width: r.width, height: r.height }
  }))
  await page.screenshot({ path: info.outputPath('aeon1460-design-truth.png') })
  const baseline = []
  for (const width of [1024, 1440]) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: 900 })
    await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z'))
    const data = fixtures({ bigProject: 40 })
    data.preferences.theme = { choice: theme }
    await mockWork(page, data)
    await page.goto('/p/PHAROS?group=none')
    await expect(page.locator('tr.ticket-row').first()).toBeVisible()
    await page.evaluate(() => document.fonts.ready)
    const geometry = await page.locator('.toolbar-wrap, .toolbar-wrap .toolbar, .tickets thead, tr.ticket-row').evaluateAll(items => items.slice(0, 8).map(el => {
      const r = el.getBoundingClientRect()
      return { name: el.className, x: r.x, y: r.y, width: r.width, height: r.height }
    }))
    baseline.push({ width, theme, geometry })
    await page.screenshot({ path: info.outputPath(`aeon1460-main-${width}-${theme}.png`) })
  }
  await writeFile(info.outputPath('aeon1460-main-geometry.md'), JSON.stringify({ truth, baseline }, null, 2))
})
