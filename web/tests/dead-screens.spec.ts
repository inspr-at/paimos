// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1187: contextual shortcuts reach their live owner, with stable controls
// and captures of the existing UI at phone, tablet and desktop sizes.
import { expect, test, type Page } from '@playwright/test'
import { agentData, mockAgents } from './agents-fixtures'
import { fixtures, me, mockWork } from './work-fixtures'
import { HOST_A, mockLeadFlow } from './lead-flow-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'

async function theme(page: Page, look: 'light' | 'dark') {
  await page.evaluate(value => { document.documentElement.dataset.theme = value }, look)
}

for (const width of [390, 1024, 1440]) for (const look of ['light', 'dark'] as const) {
  test(`approval evidence reaches the Decision Desk at ${width} ${look}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await mockWork(page, fixtures(), { admin: true })
    const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
    const session = data.sessions[1]!
    Object.assign(session, { needs_attention: true, attention_reasons: [{ kind: 'approval', scope: 'run', actor: 'person', count: 1, blocking: true, location: 'approvals' }] })
    await mockAgents(page, data)
    await page.goto(`/agents/${session.id}`)
    await theme(page, look)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await panel.getByRole('tab', { name: 'Details', exact: true }).click()
    const requests = panel.getByRole('region', { name: 'Session state evidence' }).getByRole('link', { name: 'Permission requests' })
    await expect(requests).toHaveAttribute('href', '/decision-desk')
    await expectStableControls({ controls: { requests }, scrollAreas: { body: panel.locator('#session-panel-overview') }, interactions: [
      { name: 'hover the request shortcut', run: () => requests.hover() },
      { name: 'focus the request shortcut', run: () => requests.focus() },
    ] })
    await page.screenshot({ path: info.outputPath(`evidence-${width}-${look}.png`), fullPage: true })
    await requests.click()
    await expect(page).toHaveURL(/\/decision-desk$/)
    await expect(page.getByRole('heading', { name: 'Decision Desk', level: 1 })).toBeVisible()
  })

  test(`Check computers reaches Computers settings at ${width} ${look}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await mockLeadFlow(page, { lead: 'waiting_for_room', reason: 'host_unavailable', longText: true })
    await page.route('**/api/me/permissions*', route => {
      const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
      answer.workspace.permissions.push('account.read', 'account.manage')
      return route.fulfill({ json: answer })
    })
    await page.goto('/p/PHAROS')
    await theme(page, look)
    const band = page.locator('section.lead')
    const computers = band.getByRole('button', { name: 'Check computers' })
    await expectStableControls({ controls: { computers }, scrollAreas: { band }, interactions: [
      { name: 'hover Check computers', run: () => computers.hover() },
      { name: 'focus Check computers', run: () => computers.focus() },
    ] })
    await page.screenshot({ path: info.outputPath(`lead-${width}-${look}.png`), fullPage: true })
    await computers.click()
    await expect(page).toHaveURL(/\/settings\/computers$/)
    await expect(page.locator('#agent-accounts')).toHaveAttribute('data-part', 'computers')
    await page.locator(`[data-computer="${HOST_A}"]`).click()
    const details = page.getByRole('dialog', { name: 'build-7' })
    await expect(details.getByText('No load history available', { exact: true })).toBeVisible()
    const close = details.getByRole('button', { name: 'Close details' })
    await expectStableControls({ controls: { close }, scrollAreas: { body: details.locator('.pane-body') }, interactions: [
      { name: 'focus computer details close', run: () => close.focus() },
      { name: 'hover computer details close', run: () => close.hover() },
    ] })
    await page.screenshot({ path: info.outputPath(`computer-${width}-${look}.png`), fullPage: true })
  })
}
