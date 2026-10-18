// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { mockPairing } from './agent-pairing-fixtures'
import { controlStability } from './control-stability'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`isolated account selection stays stable at ${width}px in ${theme}`, async ({ page }, testInfo) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: 1000 })
    await mockWork(page, fixtures())
    const accounts = ['claude', 'claude', 'codex', 'codex', 'codex'].map((harness, index) => ({
      account_key: `${harness}-${index}`, harness,
      label: `${harness} · Entwicklung und Qualitätssicherung ${index + 1}`,
      config_home_id: String(index + 1).repeat(64),
      model_profile_id: '66666666-6666-4666-8666-666666666666',
    }))
    const calls = await mockPairing(page, { requested_accounts: accounts })
    await page.goto('/agents/register-agent')
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await page.getByLabel('Pairing code').fill('123-456-789')
    await page.getByRole('button', { name: 'Look up code' }).click()
    const review = page.getByRole('region', { name: 'Pairing review' })
    await expect(review.getByRole('checkbox', { name: /Connect account/ })).toHaveCount(5)
    const clicked = review.getByRole('checkbox', { name: `Connect account ${accounts[1]!.label}`, exact: true })
    const guard = await controlStability(page, {
      connect: review.getByRole('button', { name: 'Connect your machine', exact: true }),
      deny: review.getByRole('button', { name: 'Deny', exact: true }),
      selectors: review.locator('.harness').first(),
      selectedRow: clicked.locator('..'),
      clicked,
    })
    await guard.check(() => clicked.uncheck())
    await guard.check(() => clicked.check())
    guard.done()
    await page.screenshot({ path: testInfo.outputPath(`pairing-${width}-${theme}.png`), fullPage: true })
    await review.getByRole('button', { name: 'Connect your machine', exact: true }).click()
    await expect.poll(() => calls.some(call => call.path.endsWith('/approve'))).toBe(true)
    const approval = calls.find(call => call.path.endsWith('/approve'))!.body as { selected_account_keys: string[] }
    expect(approval.selected_account_keys).toEqual(accounts.map(account => account.account_key))
    expect(errors).toEqual([])
  })
}
