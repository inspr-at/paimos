// SPDX-License-Identifier: AGPL-3.0-only
// AEON-569 replaces queue folding with a stable memo round. Native permission
// writes, server-confirmed feedback, history and focus remain the guarantees.
import { test, expect, type Page } from '@playwright/test'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { expectStableControls } from './helpers/stable'

async function openApproval(page: Page) {
  await page.goto('/agents?needs=a:approval-1')
  await expect(page).toHaveURL('/decision-desk?item=a:approval-1')
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  await page.getByTestId('choice-0').click()
  await expect(page.getByTestId('desk-decide')).toBeEnabled()
}
function actions(page: Page) {
  return { decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), pager: page.getByTestId('desk-pager') }
}

for (const width of [1440, 1024, 390]) for (const motion of ['no-preference', 'reduce'] as const) for (const decision of ['approved', 'denied'] as const) {
  test(`${width} ${motion}: ${decision} confirms only after the native server and keeps actions and focus`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ reducedMotion: motion })
    const world = await mockDecisionDesk(page, { long: true })
    let release!: () => void, entered!: () => void
    const gate = new Promise<void>(resolve => { release = resolve })
    const received = new Promise<void>(resolve => { entered = resolve })
    const writes: Record<string, unknown>[] = []
    await page.route('**/api/approvals/approval-1/decision', async route => {
      writes.push(route.request().postDataJSON()); entered(); await gate; await route.fallback()
    })
    await openApproval(page)
    await expectStableControls({ controls: { ...actions(page), selectors: page.getByTestId('desk-choices'), approve: page.getByTestId('choice-0'), deny: page.getByTestId('choice-1') }, interactions: [
      { name: 'choose deny', run: async () => { await page.getByTestId('choice-1').click(); await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true') } },
      { name: 'choose final answer and enter reason', run: async () => { await page.getByTestId(decision === 'approved' ? 'choice-0' : 'choice-1').click(); await page.getByRole('textbox', { name: 'Reason', exact: true }).fill('Fine for this run.'); await page.getByRole('textbox', { name: 'Reason', exact: true }).press('Enter') } },
    ] })
    await expectStableControls({ controls: actions(page), scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
      { name: 'send the protected decision and wait at the server barrier', run: async () => {
        await page.getByTestId('desk-decide').click(); await received
        await expect(page.getByTestId('desk-decide')).toBeDisabled()
        await expect(page.getByTestId('desk-status')).toContainText('Recording')
        await expect(page.getByTestId('desk-announcement')).not.toContainText('Decision recorded')
        await expect(page.getByTestId('desk-pager')).toContainText('1 of')
        expect(world.approval.decision).toBeNull(); expect(world.calls).toHaveLength(0)
        expect(writes).toEqual([{ decision, reason: 'Fine for this run.' }])
      } },
      { name: 'confirm native response and move to the next item', run: async () => {
        release(); await expect(page.getByTestId('desk-announcement')).toContainText('Decision recorded')
        await expect(page.getByTestId('desk-pager')).toContainText('2 of')
        await expect(page.getByTestId('desk-decide')).toBeFocused()
        expect(world.approval.decision).toBe(decision)
        expect(world.calls.map(call => call.path)).toEqual(['/api/approvals/approval-1/decision'])
      } },
    ] })
    await page.getByTestId('desk-paper').press('ArrowLeft')
    await expect(page.getByTestId('desk-decide')).toHaveText(/Next/)
    await expect(page.getByTestId('choice-0')).toBeDisabled()
    await expect(page.getByTestId('desk-revoke')).toBeVisible({ visible: decision === 'approved' })
    await page.getByTestId('desk-close').click()
    await page.getByRole('button', { name: /^Decided/ }).click()
    await expect(page.getByTestId('desk-row-a:approval-1')).toBeVisible()
  })
}

for (const width of [1440, 1024, 390]) {
  test(`${width}: a rejected native decision retains its reason and never advances or reports success`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const world = await mockDecisionDesk(page); world.denyWrite = true
    await openApproval(page)
    await page.getByRole('textbox', { name: 'Reason', exact: true }).fill('Keep my reason.')
    await page.getByRole('textbox', { name: 'Reason', exact: true }).press('Enter')
    await expectStableControls({ controls: actions(page), interactions: [{ name: 'failed decision', run: async () => {
      await page.getByTestId('desk-decide').click()
      await expect(page.getByTestId('desk-status')).toContainText('Approval expired')
      await expect(page.getByTestId('desk-announcement')).toContainText('not confirmed')
      await expect(page.getByTestId('desk-decide')).toBeEnabled()
      await expect(page.getByTestId('desk-pager')).toContainText('1 of')
      await expect(page.getByRole('textbox', { name: 'Reason', exact: true })).toHaveValue('Keep my reason.')
    } }] })
    expect(world.approval.decision).toBeNull()
    expect(world.calls.map(call => call.path)).toEqual(['/api/approvals/approval-1/decision'])
  })
}

test('a mismatched native response is an unconfirmed decision, not success', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  await page.route('**/api/approvals/approval-1/decision', route => route.fulfill({ json: { ...world.approval, id: 'different-source', decision: 'approved' } }))
  await openApproval(page); await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-status')).toContainText('native approval decision was not confirmed')
  await expect(page.getByTestId('desk-announcement')).toContainText('not confirmed')
  await expect(page.getByTestId('desk-pager')).toContainText('1 of')
  expect(world.approval.decision).toBeNull()
})

test('a denied native read never enables or sends a protected decision', async ({ page }) => {
  const world = await mockDecisionDesk(page, { denied: true })
  await page.goto('/agents?needs=a:approval-1')
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  await expect(page.getByTestId('desk-decide')).toBeDisabled()
  await page.getByTestId('desk-paper').press('Enter')
  expect(world.calls).toHaveLength(0)
})

test('a failed Revoke retains the native grant and reports the failure', async ({ page }) => {
  const world = await mockDecisionDesk(page); world.approval.decision = 'approved'; world.denyWrite = true
  await page.goto('/agents?needs=a:approval-1')
  await expect(page.getByTestId('desk-revoke')).toBeEnabled()
  await page.getByTestId('desk-revoke').click()
  await expect(page.getByTestId('desk-status')).toContainText('Approval expired')
  await expect(page.getByTestId('desk-revoke')).toBeEnabled()
  expect(world.approval.decision).toBe('approved')
  expect(world.calls.map(call => call.path)).toEqual(['/api/approvals/approval-1/revoke'])
})
