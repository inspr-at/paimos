// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockRules } from './rules-fixtures'

for (const width of [1600, 390]) for (const theme of ['light', 'dark']) {
  test(`large budget and per-client delivery ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme as 'light' | 'dark', reducedMotion: 'reduce' })
    await mockWork(page, fixtures())
    await mockSettings(page, settingsData())
    const mock = await mockRules(page, { blockingClients: [
      { host: 'workstation-with-a-long-hostname.local', harness: 'claude', version: '260928120000.0.0', max_session_file_bytes: 12000 },
      { host: 'codex-host', harness: 'codex', max_session_file_bytes: 32768 },
      { host: 'updated-host', harness: 'claude', max_session_file_bytes: 512000 },
    ] })
    await page.goto('/settings/agent-rules')
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    const section = page.getByRole('region', { name: 'Budget' })
    await expect(section).toContainText('≈ 3,000 tokens loaded into every session')
    await section.getByText('Client delivery').click()
    await expect(section).toContainText('last seven days')
    await expect(section.getByTitle('workstation-with-a-long-hostname.local')).toBeVisible()
    await expect(section).toContainText('claude · 260928120000.0.0')
    await section.getByRole('button', { name: 'Change', exact: true }).click()
    await section.getByLabel(/^Total/).fill('500001')
    await section.getByRole('button', { name: 'Save budget' }).click()
    await expect(section.getByRole('alert')).toContainText('between 2,000 and 500,000')
    expect(mock.calls.filter(c => c.method === 'PUT' && c.path === '/api/rules/budget')).toHaveLength(0)
    await section.getByLabel(/^Total/).fill('500000')
    await expect(section).toContainText('≈ 125,000 tokens loaded into every session')
    await expect(section).toContainText('Every agent session starts with this much context; it costs tokens and money.')
    const clients = section.locator('.compatibility li')
    await expect(clients.nth(0)).toContainText('12,000 bytestruncated')
    await expect(clients.nth(1)).toContainText('32,768 bytestruncated')
    await expect(clients.nth(2)).toContainText('500,000 bytes')
    await expect(clients.nth(2)).not.toContainText('truncated')
    await section.getByRole('button', { name: 'Save budget' }).click()
    await expect(section).toContainText('500,000 bytes per session file')
    await expect(section.getByRole('alert')).toHaveCount(0)
    await section.getByRole('button', { name: 'Change', exact: true }).click()
    await section.locator('.tip summary').click()
    await expect(section.locator('.tip')).toContainText('start around 4–16 KB')
    await expect(section.locator('.tip')).toContainText('one rule, one place')
    await expect(section.locator('.tip a')).toHaveCount(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    if (process.env.AEON_432_SHOTS) {
      mkdirSync(process.env.AEON_432_SHOTS, { recursive: true })
      await page.getByRole('button', { name: 'Dismiss', exact: true }).click()
      if (width === 390) await page.setViewportSize({ width, height: 1600 })
      await section.scrollIntoViewIfNeeded()
      await section.screenshot({ path: `${process.env.AEON_432_SHOTS}/budget-edit-${width}-${theme}.png` })
    }
    await section.getByLabel('Tip language').selectOption('de')
    await expect(section.locator('.tip')).toContainText('eine Regel, ein Ort')
    await expect(section.locator('.tip')).toContainText('Ende 2026')
    await expect(section.locator('.tip ul')).toHaveAttribute('lang', 'de')
    expect(mock.calls.filter(c => c.method === 'PUT' && c.path === '/api/rules/budget')).toEqual([{ method: 'PUT', path: '/api/rules/budget', body: { max_bytes: 500000, layer_max_bytes: {} } }])
  })
}

test('cost guidance is live, nonblocking and changes at the documented thresholds', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  const mock = await mockRules(page)
  await page.goto('/settings/agent-rules')
  const section = page.getByRole('region', { name: 'Budget' })
  await expect(section.getByText('Client delivery')).toHaveCount(0)
  await section.getByRole('button', { name: 'Change', exact: true }).click()
  await expect(section).toContainText('2,000 to 500,000; default 12,000')
  await section.getByLabel(/^Total/).fill('64000')
  await expect(section.locator('.warning')).toHaveCount(0)
  await section.getByLabel(/^Total/).fill('64001')
  await expect(section).toContainText('≈ 16,001 tokens loaded into every session')
  await expect(section.locator('.warning')).toContainText('consider loading details on demand')
  await section.getByLabel(/^Total/).fill('128000')
  await expect(section.locator('.warning.strong')).toHaveCount(0)
  await section.getByLabel(/^Total/).fill('128001')
  await expect(section.locator('.warning.strong')).toBeVisible()
  await expect(section.getByRole('button', { name: 'Save budget' })).toBeEnabled()
  await section.getByLabel(/^Total/).fill('500000')
  await section.getByLabel(/^Project/).fill('500001')
  await section.getByRole('button', { name: 'Save budget' }).click()
  await expect(section.getByRole('alert')).toContainText('A project cap is between 500 bytes and the total')
  await section.getByLabel(/^Project/).fill('500000')
  await section.getByRole('button', { name: 'Save budget' }).click()
  await expect(section).toContainText('500,000 bytes per session file · Project up to 500,000')
  expect(mock.calls.filter(c => c.method === 'PUT' && c.path === '/api/rules/budget')).toEqual([{ method: 'PUT', path: '/api/rules/budget', body: { max_bytes: 500000, layer_max_bytes: { project: 500000 } } }])
})
