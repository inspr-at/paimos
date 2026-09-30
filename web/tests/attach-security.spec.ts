// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

const shots = process.env.ATTACH_SHOTS ?? '/private/tmp/aeon-258-shots'
type Computer = { computer_id: string; name: string; capability: string }
const computer = (capability: string, name = 'Studio Mac', id = '22222222-2222-4222-8222-222222222222'): Computer => ({ computer_id: id, name, capability })
const limitation: Record<string, string> = {
  unsupported: 'Studio Mac cannot confirm on the Mac',
  unsigned: 'Studio Mac needs a signed daemon',
  no_gui: 'Studio Mac has no graphical session',
  policy: 'Studio Mac cannot use Touch ID or the Mac password',
  unreported: 'Studio Mac has not reported Touch ID support',
}
const stayedOff = (capability: string) => `${limitation[capability]}, so watches there stay off.`

async function setup(page: Page, theme: 'light' | 'dark' = 'light', options: { fail?: boolean; mode?: string; saved?: boolean; computers?: Computer[] } = {}) {
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work)
  await mockSettings(page, settingsData())
  await page.route('**/api/me/permissions*', route => {
    const value = mockEffectivePermissions('admin')
    value.workspace.permissions.push('profile.read', 'profile.write')
    return route.fulfill({ json: value })
  })
  let mode = options.mode ?? 'aeon'
  let savedFlag = options.saved ?? mode === 'local_auth'
  const computers = options.computers ?? [computer('available')]
  await page.route('**/api/me/security/session-watching', route => {
    if (route.request().method() === 'PUT') {
      if (options.fail) return route.fulfill({ status: 503, json: { error: 'unavailable' } })
      const body = route.request().postDataJSON()
      expect(Object.keys(body)).toEqual(['consent_mode'])
      mode = body.consent_mode
      savedFlag = true
    }
    return route.fulfill({ json: { consent_mode: mode, consent_saved: savedFlag, local_auth_computers: computers } })
  })
}
async function open(page: Page, theme: 'light' | 'dark', width: number) {
  await page.setViewportSize({ width, height: 1000 })
  await page.goto('/settings/personal#security')
  const card = page.getByRole('region', { name: 'Security', exact: true })
  await expect(card.getByRole('heading', { name: 'Session watching' })).toBeVisible()
  return card
}
async function fits(page: Page, card: ReturnType<Page['getByRole']>) {
  await card.evaluate(el => el.scrollIntoView({ block: 'center' }))
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  expect(await card.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
}
async function shot(page: Page, name: string) {
  mkdirSync(shots, { recursive: true })
  await page.screenshot({ path: `${shots}/${name}.png` })
}

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`session watching security ${theme} ${width}`, async ({ page }) => {
    await setup(page, theme, { mode: 'local_auth', saved: false })
    const card = await open(page, theme, width)
    const aeon = card.getByRole('radio', { name: /Approve in / })
    const mac = card.getByRole('radio', { name: /Also confirm on the Mac/ })
    const aeonLabel = card.locator('label').filter({ hasText: 'Approve in' })
    const macLabel = card.locator('label').filter({ hasText: 'Also confirm on the Mac' })
    await expect(mac).toBeChecked()
    await expect(mac).toBeEnabled()
    await expect(macLabel).toContainText('Default')
    await expect(aeonLabel).not.toContainText('Default')
    await expect(card).toContainText('After approval here, confirm with Touch ID or your Mac password.')
    await expect(card).toContainText('This default asks for Touch ID on a Mac that can use it.')
    await expect(card).not.toContainText('stay off')
    await expect(card).toContainText('active watches keep their current approval')
    await expect(card.getByRole('button', { name: 'Save setting' })).toBeEnabled()
    await shot(page, `security-default-${theme}-${width}`)
    await aeon.check()
    await expect(card).toContainText('does not ask for Touch ID')
    await expect(card).not.toContainText('This default asks for Touch ID')
    await card.getByRole('button', { name: 'Save setting' }).click()
    await expect(card.getByRole('status')).toHaveText('Saved for future approvals.')
    await page.reload()
    await expect(aeon).toBeChecked()
    await expect(macLabel).toContainText('Default')
    await expect(aeonLabel).not.toContainText('Default')
    await expect(card.getByRole('button', { name: 'Save setting' })).toBeDisabled()
    await expect(card).toContainText('does not ask for Touch ID')
    await mac.check()
    await card.getByRole('button', { name: 'Save setting' }).click()
    await expect(card.getByRole('status')).toHaveText('Saved for future approvals.')
    await page.reload()
    await expect(mac).toBeChecked()
    await fits(page, card)
    const audit = await new AxeBuilder({ page }).include('#security').analyze()
    expect(audit.violations).toEqual([])
    await shot(page, `security-${theme}-${width}`)
  })

  for (const capability of Object.keys(limitation)) {
    test(`mac confirmation unavailable ${capability} ${theme} ${width}`, async ({ page }) => {
      await setup(page, theme, { computers: [computer(capability)] })
      const card = await open(page, theme, width)
      const mac = card.getByRole('radio', { name: /Also confirm on the Mac/ })
      await expect(mac).toBeDisabled()
      await expect(card.getByRole('radio', { name: /Approve in / })).toBeChecked()
      await expect(card.locator('label').filter({ hasText: 'Approve in' })).toContainText('Default')
      await expect(card.locator('label').filter({ hasText: 'Also confirm on the Mac' })).not.toContainText('Default')
      await expect(card).toContainText(capability === 'no_gui'
        ? `${limitation[capability]}, so SSH and headless attaches keep approval in AEON.`
        : `${limitation[capability]}, so approval stays in AEON.`)
      await expect(card).not.toContainText('stay off')
      await expect(card).not.toContainText('After approval here')
      await expect(card.getByRole('button', { name: 'Save setting' })).toBeDisabled()
      await fits(page, card)
      if (capability === 'unsigned') {
        const audit = await new AxeBuilder({ page }).include('#security').analyze()
        expect(audit.violations).toEqual([])
        await shot(page, `security-unsigned-${theme}-${width}`)
      }
    })
  }

  test(`mixed computers keep mac confirmation with named exceptions ${theme} ${width}`, async ({ page }) => {
    await setup(page, theme, { mode: 'local_auth', saved: false, computers: [computer('available'), computer('unsupported', 'Linux builder', '33333333-3333-4333-8333-333333333333')] })
    const card = await open(page, theme, width)
    const mac = card.getByRole('radio', { name: /Also confirm on the Mac/ })
    await expect(mac).toBeEnabled()
    await expect(mac).toBeChecked()
    await expect(card).toContainText('Linux builder cannot confirm on the Mac, so approval stays in AEON.')
    await expect(card).not.toContainText('stay off')
    await expect(card).toContainText('After approval here, confirm with Touch ID or your Mac password.')
    await expect(card).not.toContainText('Studio Mac cannot confirm')
    await expect(card.getByRole('button', { name: 'Save setting' })).toBeEnabled()
    await fits(page, card)
    const audit = await new AxeBuilder({ page }).include('#security').analyze()
    expect(audit.violations).toEqual([])
    await shot(page, `security-mixed-${theme}-${width}`)
    await card.getByRole('button', { name: 'Save setting' }).click()
    await page.reload()
    await expect(mac).toBeChecked()
    await expect(card).toContainText('Linux builder cannot confirm on the Mac, so watches there stay off.')
  })
}

test('a failed security save never claims the stricter mode was stored', async ({ page }) => {
  await setup(page, 'light', { fail: true })
  await page.goto('/settings/personal#security')
  const card = page.getByRole('region', { name: 'Security', exact: true })
  await card.getByRole('radio', { name: /Also confirm on the Mac/ }).check()
  await card.getByRole('button', { name: 'Save setting' }).click()
  await expect(card.getByRole('alert')).toContainText('Your setting was not saved')
  await page.reload()
  await expect(card.getByRole('radio', { name: /Approve in / })).toBeChecked()
})

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`saved mac confirmation without a capable computer ${theme} ${width}`, async ({ page }) => {
    await setup(page, theme, { mode: 'local_auth', computers: [computer('unsigned')] })
    const card = await open(page, theme, width)
    const mac = card.getByRole('radio', { name: /Also confirm on the Mac/ })
    await expect(mac).toBeChecked()
    await expect(mac).toBeDisabled()
    await expect(card.getByText('Mac confirmation is saved, but no paired computer can run it. New watches stay off until you choose approval in AEON.')).toBeVisible()
    await expect(card).toContainText(stayedOff('unsigned'))
    await fits(page, card)
    const audit = await new AxeBuilder({ page }).include('#security').analyze()
    expect(audit.violations).toEqual([])
    await shot(page, `security-saved-unavailable-${theme}-${width}`)
    await card.getByRole('radio', { name: /Approve in / }).check()
    await card.getByRole('button', { name: 'Save setting' }).click()
    await expect(card.getByRole('status').filter({ hasText: 'Saved for future approvals.' })).toBeVisible()
    await page.reload()
    await expect(card.getByRole('radio', { name: /Approve in / })).toBeChecked()
    await expect(mac).toBeDisabled()
    await expect(card).toContainText('Studio Mac needs a signed daemon, so approval stays in AEON.')
    await expect(card).not.toContainText('stay off')
  })

  test(`no paired computer ${theme} ${width}`, async ({ page }) => {
    await setup(page, theme, { computers: [] })
    const card = await open(page, theme, width)
    await expect(card.getByRole('radio', { name: /Also confirm on the Mac/ })).toBeDisabled()
    await expect(card.locator('label').filter({ hasText: 'Approve in' })).toContainText('Default')
    await expect(card).toContainText('No paired computer has reported Touch ID support.')
    await fits(page, card)
    if (theme === 'light' && width === 390) await shot(page, 'security-none-light-390')
    if (theme === 'dark' && width === 1600) await shot(page, 'security-none-dark-1600')
  })
}

test('a long computer name wraps at 390', async ({ page }) => {
  const name = 'Workstation with a very long name that has to wrap inside the security card'
  await setup(page, 'light', { mode: 'local_auth', computers: [computer('unreported', name)] })
  const card = await open(page, 'light', 390)
  await expect(card).toContainText(`${name} has not reported Touch ID support, so watches there stay off.`)
  await fits(page, card)
  await shot(page, 'security-long-name-light-390')
})
