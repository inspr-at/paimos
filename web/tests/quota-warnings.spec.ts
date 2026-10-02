// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, me, mockWork } from './work-fixtures'
import { mockBusiness, businessData } from './business-fixtures'
import { settingsData, mockSettings } from './settings-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { expectStableControls } from './helpers/stable'

for (const width of [390, 1440]) {
  test(`quota thresholds retain controls through edits, errors and save at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await mockWork(page, fixtures(), { admin: true })
    await mockBusiness(page, businessData())
    await mockSettings(page, settingsData())
    const settings = { early_percent: 10, urgent_percent: 3 }
    let fail = true
    const writes: unknown[] = []
    await page.route('**/api/settings/quota-warnings', route => {
      if (route.request().method() === 'PUT') {
        writes.push(route.request().postDataJSON())
        if (fail) return route.fulfill({ status: 500, json: { error: 'database operation failed' } })
        Object.assign(settings, route.request().postDataJSON())
      }
      return route.fulfill({ json: settings })
    })
    await page.goto('/settings/workspace')
    const card = page.getByRole('region', { name: 'Low-quota warnings' })
    const early = card.getByLabel('Early notice (%)'), urgent = card.getByLabel('Urgent notice (%)')
    const save = card.getByRole('button', { name: 'Save thresholds' })
    await expect(save).toBeEnabled()
    await card.scrollIntoViewIfNeeded()
    await expectStableControls({
      controls: { early, urgent, save, group: card.locator('fieldset') }, scrollAreas: { card },
      interactions: [
        { name: 'urgent cannot equal early', run: async () => { await urgent.fill('10'); await expect(save).toBeDisabled() } },
        { name: 'fraction cannot save', run: async () => { await early.fill('10.5'); await expect(save).toBeDisabled() } },
        { name: 'valid custom thresholds', run: async () => { await early.fill('20'); await urgent.fill('5'); await expect(save).toBeEnabled() } },
        { name: 'failed save stays honest', run: async () => { await save.click(); await expect(card.getByRole('status')).toHaveText('Could not save thresholds. Try again.'); await expect(save).toBeEnabled() } },
        { name: 'durable successful save', run: async () => { fail = false; await save.click(); await expect(card.getByRole('status')).toHaveText('Thresholds saved.'); await expect(save).toBeEnabled() } },
      ],
    })
    expect(writes).toEqual([{ early_percent: 20, urgent_percent: 5 }, { early_percent: 20, urgent_percent: 5 }])
    expect((await new AxeBuilder({ page }).include('#quota-warnings').analyze()).violations).toEqual([])
    await page.reload()
    await expect(early).toHaveValue('20'); await expect(urgent).toHaveValue('5')
  })
}

test('/agents names affected sessions and respects availability-only redaction', async ({ page }) => {
  const world = { me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } }
  await mockWork(page, fixtures(), { admin: true })
  const agents = agentData(world)
  await mockAgents(page, agents)
  const target = agents.sessions[1]!
  const warning = { session_id: target.id, project_id: target.project_id, account_id: agents.accounts[0]!.id, availability: 'limited', details_redacted: true }
  await page.route('**/api/agent-accounts/quota-warnings*', route => route.fulfill({ json: { items: [warning], next_after: null } }))
  await page.goto('/agents')
  const notices = page.getByRole('region', { name: 'Account availability notices' })
  await expect(notices.getByRole('link')).toHaveAttribute('href', `/agents/${target.id}`)
  await expect(notices).toContainText('Limited availability')
  await expect(notices).not.toContainText('%')
  await expect(notices).not.toContainText('Urgent')
  await page.route('**/api/agent-accounts/quota-warnings*', route => route.fulfill({ json: { items: [{ ...warning, details_redacted: false, severity: 'urgent', remaining_percent: 2 }], next_after: null } }))
  await page.reload()
  await expect(notices).toContainText('Urgent notice · 2% remaining')
})
