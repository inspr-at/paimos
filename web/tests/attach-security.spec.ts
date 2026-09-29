// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

const shots = process.env.ATTACH_SHOTS ?? '/private/tmp/aeon-258-shots'
async function setup(page: Page, theme: 'light' | 'dark' = 'light', fail = false) {
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work)
  await mockSettings(page, settingsData())
  await page.route('**/api/me/permissions*', route => {
    const value = mockEffectivePermissions('admin')
    value.workspace.permissions.push('profile.read', 'profile.write')
    return route.fulfill({ json: value })
  })
  let mode = 'aeon'
  await page.route('**/api/me/security/session-watching', route => {
    if (route.request().method() === 'PUT') {
      if (fail) return route.fulfill({ status: 503, json: { error: 'unavailable' } })
      const body = route.request().postDataJSON()
      expect(Object.keys(body)).toEqual(['consent_mode'])
      mode = body.consent_mode
    }
    return route.fulfill({ json: { consent_mode: mode } })
  })
}
for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`session watching security ${theme} ${width}`, async ({ page }) => {
    await setup(page, theme)
    await page.setViewportSize({ width, height: 1000 })
    await page.goto('/settings/personal#security')
    const card = page.getByRole('region', { name: 'Security', exact: true })
    await expect(card.getByRole('radio', { name: /Approve in / })).toBeChecked()
    await expect(card).toContainText('Unavailable on Linux')
    await expect(card).toContainText('active watches keep their current approval')
    await expect(card.getByRole('button', { name: 'Save setting' })).toBeDisabled()
    await card.getByRole('radio', { name: /Also confirm on the Mac/ }).check()
    await card.getByRole('button', { name: 'Save setting' }).click()
    await expect(card.getByRole('status')).toHaveText('Saved for future approvals.')
    await page.reload()
    await expect(card.getByRole('radio', { name: /Also confirm on the Mac/ })).toBeChecked()
    await card.evaluate(el => el.scrollIntoView({ block: 'center' }))
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    expect(await card.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    const audit = await new AxeBuilder({ page }).include('#security').analyze()
    expect(audit.violations).toEqual([])
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: `${shots}/security-${theme}-${width}.png` })
  })
}
test('a failed security save never claims the stricter mode was stored', async ({ page }) => {
  await setup(page, 'light', true)
  await page.goto('/settings/personal#security')
  const card = page.getByRole('region', { name: 'Security', exact: true })
  await card.getByRole('radio', { name: /Also confirm on the Mac/ }).check()
  await card.getByRole('button', { name: 'Save setting' }).click()
  await expect(card.getByRole('alert')).toContainText('Your setting was not saved')
  await page.reload()
  await expect(card.getByRole('radio', { name: /Approve in / })).toBeChecked()
})
