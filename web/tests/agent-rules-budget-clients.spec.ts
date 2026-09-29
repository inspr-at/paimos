// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockRules } from './rules-fixtures'

for (const width of [1600, 390]) for (const theme of ['light', 'dark']) {
  test(`client budget gate ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme as 'light' | 'dark', reducedMotion: 'reduce' })
    await mockWork(page, fixtures())
    await mockSettings(page, settingsData())
    const mock = await mockRules(page, { blockingClients: [{ host: 'workstation-with-a-long-hostname.local', harness: 'claude', version: '260928120000.0.0', max_session_file_bytes: 12000 }] })
    await page.goto('/settings/agent-rules')
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    const section = page.getByRole('region', { name: 'Budget' })
    await section.getByText('Larger files need a client update').click()
    await expect(section).toContainText('last seven days')
    await expect(section.getByTitle('workstation-with-a-long-hostname.local')).toBeVisible()
    await expect(section).toContainText('claude · 260928120000.0.0')
    await section.getByRole('button', { name: 'Change', exact: true }).click()
    await section.getByLabel(/^Total/).fill('64000')
    await section.getByRole('button', { name: 'Save budget' }).click()
    await expect(section.getByRole('alert')).toContainText('between 2,000 and 12,000')
    expect(mock.calls.filter(c => c.method === 'PUT' && c.path === '/api/rules/budget')).toHaveLength(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    if (process.env.AEON_328_SHOTS) {
      await section.screenshot({ path: `${process.env.AEON_328_SHOTS}/budget-edit-${width}-${theme}.png` })
      await section.getByRole('button', { name: 'Cancel' }).click()
      await page.screenshot({ path: `${process.env.AEON_328_SHOTS}/budget-${width}-${theme}.png`, fullPage: true })
    }
  })
}

test('all compatible clients unlock the larger total and layer caps', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  const mock = await mockRules(page, { budgetCeiling: 64000 })
  await page.goto('/settings/agent-rules')
  const section = page.getByRole('region', { name: 'Budget' })
  await expect(section.getByText('Larger files need a client update')).toHaveCount(0)
  await section.getByRole('button', { name: 'Change', exact: true }).click()
  await expect(section).toContainText('2,000 to 64,000; default 12,000')
  await section.getByLabel(/^Total/).fill('64001')
  await section.getByRole('button', { name: 'Save budget' }).click()
  await expect(section.getByRole('alert')).toContainText('between 2,000 and 64,000')
  await section.getByLabel(/^Total/).fill('64000')
  await section.getByLabel(/^Project/).fill('48000')
  await section.getByRole('button', { name: 'Save budget' }).click()
  await expect(section).toContainText('64,000 bytes per session file · Project up to 48,000')
  if (process.env.AEON_328_SHOTS) {
    await page.getByRole('button', { name: 'Dismiss', exact: true }).click()
    await section.screenshot({ path: `${process.env.AEON_328_SHOTS}/budget-compatible.png` })
  }
  expect(mock.calls.filter(c => c.method === 'PUT' && c.path === '/api/rules/budget')).toEqual([{ method: 'PUT', path: '/api/rules/budget', body: { max_bytes: 64000, layer_max_bytes: { project: 48000 } } }])
})
