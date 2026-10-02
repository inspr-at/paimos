// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { expectStableControls } from './helpers/stable'
import { mockDecisionDesk, sampleQuestion } from './decision-desk-fixtures'

async function openFirst(page: Page, waitForPermission = true) {
  await page.goto('/decision-desk')
  await expect(page.getByRole('heading', { name: 'Decision Desk', exact: true })).toBeVisible()
  await page.getByTestId('desk-row-q:question-1').click()
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  if (waitForPermission) await expect(page.getByTestId('desk-decide')).toBeEnabled()
}
for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`memo stability ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    const world = await mockDecisionDesk(page, { long: true, theme })
    await openFirst(page)
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    const controls = {
      actions: page.getByTestId('desk-actions').locator('.action-buttons'), decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), pager: page.getByTestId('desk-pager'),
      stamps: page.getByTestId('desk-stamps'), selector: page.getByTestId('desk-choices'), clickedRow: page.getByTestId('choice-row-0'),
      ...(width === 390 ? { frame: page.getByTestId('desk-frame') } : {}),
    }
    await expectStableControls({ controls, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
      { name: 'select another answer', run: async () => { await page.getByTestId('choice-1').click(); await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true') } },
      { name: 'write custom answer', run: async () => { await page.getByTestId('choice-2').click(); await page.getByRole('textbox', { name: 'Something else' }).fill('Keep the query scoped to this tenant.'); await page.getByRole('textbox', { name: 'Something else' }).press('Enter'); await expect(page.getByTestId('desk-status')).toContainText('answer is set') } },
      { name: 'unavailable stamp stays neutral', run: async () => { await expect(page.getByTestId('stamp-always')).toBeDisabled(); await page.getByTestId('desk-paper').press('a'); await expect(page.getByTestId('stamp-once')).toHaveAttribute('aria-pressed', 'true') } },
      { name: 'type and finish P.S.', run: async () => { await page.getByRole('textbox', { name: 'P.S. for the agent' }).fill('Keep the acceptance criteria.'); await page.getByRole('textbox', { name: 'P.S. for the agent' }).press('Enter') } },
      { name: 'skip to next', run: async () => { await page.getByTestId('desk-skip').click(); await expect(page.getByTestId('desk-pager')).toContainText('2 of') } },
      { name: 'page back', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('desk-pager')).toContainText('1 of') } },
      { name: 'decide and advance', run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1); await expect(page.getByTestId('desk-pager')).toContainText('2 of'); await expect(page.getByTestId('desk-decide')).toBeFocused() } },
      { name: 'revisit decision', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('desk-decide')).toContainText('Next') } },
      { name: 'correct and restart delivery', run: async () => { await page.getByTestId('choice-0').click(); await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(2); await expect(page.getByTestId('desk-pager')).toContainText('2 of') } },
      { name: 'return to attachments', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByRole('button', { name: 'Open Index plan' })).toBeVisible() } },
      { name: 'lightbox and return focus', run: async () => { const attachment = page.getByRole('button', { name: 'Open Index plan' }); await attachment.click(); await expect(page.getByRole('dialog', { name: 'Index plan, attachment 1 of 1' })).toBeVisible(); await page.getByRole('button', { name: 'Close viewer' }).click(); await expect(attachment).toBeFocused(); await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible() } },
    ] })
    expect(world.calls.map(call => call.body.expected_revision)).toEqual([1, 2])
    expect(world.calls[0]?.body.answer).toBe('Keep the query scoped to this tenant.')
    const dir = process.env.AEON_DESK_SHOTS || testInfo.outputPath('desk-shots')
    await mkdir(dir, { recursive: true })
    await page.screenshot({ path: join(dir, `memo-${width}-${theme}.png`) })
  })
}

test('keyboard editing, pager, IME and browser shortcuts stay separate', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  await openFirst(page)
  const field = page.getByRole('textbox', { name: 'Something else' })
  await page.getByTestId('desk-paper').press('3'); await expect(field).toBeFocused()
  await field.fill('My answer includes 4 and a.'); await field.press('Enter')
  expect(world.calls).toHaveLength(0); await expect(page.getByTestId('desk-status')).toContainText('answer is set')
  await page.getByTestId('desk-paper').press('3'); await expect(field).toBeFocused()
  expect(await field.evaluate(input => (input as HTMLInputElement).selectionStart)).toBe('My answer includes 4 and a.'.length)
  const native = await field.evaluate(element => ['s', 'r', 'd', 'p', 'a'].map(key => {
    const event = new KeyboardEvent('keydown', { key, ctrlKey: true, bubbles: true, cancelable: true }); element.dispatchEvent(event); return event.defaultPrevented
  }))
  expect(native).toEqual([false, false, false, false, false])
  const ime = await field.evaluate(element => { const event = new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, bubbles: true, cancelable: true }); element.dispatchEvent(event); return event.defaultPrevented })
  expect(ime).toBe(false)
  await field.press('Escape'); await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  await page.getByTestId('desk-pager').click(); await page.getByRole('listbox').press('ArrowDown'); await page.getByRole('listbox').press('Enter')
  await expect(page.getByTestId('desk-pager')).toContainText('2 of'); await expect(page.getByTestId('desk-pager')).toBeFocused()
  await page.getByTestId('desk-paper').press('Escape'); await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).not.toBeVisible()
  await expect(page.getByTestId('desk-row-q:question-1')).toBeFocused()
})

test('failed and stale writes keep drafts and announce failure without advancing', async ({ page }) => {
  const world = await mockDecisionDesk(page); world.denyWrite = true
  await openFirst(page); await page.getByTestId('choice-2').click()
  const field = page.getByRole('textbox', { name: 'Something else' }); await field.fill('Keep this draft'); await field.press('Enter')
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-status')).toContainText('changed')
  await expect(page.getByTestId('desk-pager')).toContainText('1 of'); await expect(field).toHaveValue('Keep this draft')
  await expect(page.getByTestId('desk-announcement')).toContainText('not confirmed')
})

test('refresh does not replace the active memo and new arrivals wait for a boundary', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  await openFirst(page)
  world.questions.unshift(sampleQuestion('new-arrival', { question: 'New arrival first in the response' }))
  world.questions.find(question => question.id === 'question-1')!.revision++
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.getByTestId('desk-status')).toContainText('source changed')
  await expect(page.getByTestId('desk-paper').getByRole('heading', { name: 'Which migration should carry the index?' })).toBeVisible()
  await expect(page.getByTestId('desk-pager')).toContainText('1 of 5')
  await expect(page.getByTestId('desk-decide')).toBeDisabled()
})

test('denied reads and permission races never appear as successful decisions', async ({ page }) => {
  const world = await mockDecisionDesk(page, { denied: true }); world.denyContext = true
  await openFirst(page, false)
  await expect(page.getByTestId('desk-decide')).toBeDisabled()
  await expect(page.getByTestId('desk-body')).toContainText('Ticket context is unavailable or inaccessible')
  await page.getByTestId('desk-close').click(); world.failQuestions = true
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await expect(page.getByText('Questions could not be read.', { exact: false })).toBeVisible()
  expect(world.calls).toHaveLength(0)
})

test('native approvals, action replies and rule changes use only their own endpoints', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-a:approval-1').click()
  await page.getByTestId('desk-decide').click()
  await expect.poll(() => world.calls.length).toBe(1)
  expect(world.calls[0]?.path).toBe('/api/approvals/approval-1/decision')
  await page.getByTestId('desk-close').click(); await page.getByTestId('desk-row-m:action-1').click()
  const reply = page.getByRole('textbox', { name: 'Reply to the agent' }); await page.getByTestId('choice-0').click(); await reply.fill('Continue with the tenant test.'); await reply.press('Enter')
  await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(2)
  expect(world.calls[1]?.body).toMatchObject({ reply_to: 'action-1', recipient_session_id: 'original-generation', body: 'Continue with the tenant test.' })
  await page.getByTestId('desk-close').click(); await page.getByTestId('desk-row-r:rule-1').click(); await page.getByTestId('desk-decide').click()
  await expect.poll(() => world.calls.length).toBe(3)
  expect(world.calls[2]?.path).toBe('/api/rules/doctrine/inbox/rule-1/pull-request')
})

test('phone approval and expiry races fail closed', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const world = await mockDecisionDesk(page)
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-a:approval-1').click(); await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-status')).toContainText('Phone verification')
  expect(world.calls).toHaveLength(0)
  await page.getByTestId('desk-close').click(); world.approval.expires_at = '2000-01-01T00:00:00Z'
  await page.getByRole('button', { name: 'Refresh', exact: true }).click(); await page.getByTestId('desk-row-a:approval-1').click()
  await expect(page.getByTestId('desk-status')).toContainText('expired'); await expect(page.getByTestId('desk-decide')).toBeDisabled()
})

test('phone decisions reuse the request-bound user verification ceremony', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.addInitScript(() => Object.defineProperty(navigator, 'credentials', { value: { get: async () => ({ id: 'test-credential', rawId: new Uint8Array([1]).buffer, type: 'public-key', authenticatorAttachment: 'platform', getClientExtensionResults: () => ({}), response: { clientDataJSON: new Uint8Array([2]).buffer, authenticatorData: new Uint8Array([3]).buffer, signature: new Uint8Array([4]).buffer, userHandle: null } }) } }))
  const world = await mockDecisionDesk(page, { phone: true })
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-a:approval-1').click(); await expect(page.getByTestId('desk-decide')).toBeEnabled(); await page.getByTestId('desk-decide').click()
  await expect.poll(() => world.calls.length).toBe(2)
  expect(world.calls.map(call => call.path)).toEqual(['/api/phone-approvals/approval/approval-1/options', '/api/phone-approvals/approval/approval-1/decision'])
  expect(world.calls[1]?.body).toMatchObject({ decision: 'approved', request_hash: 'bound-request-hash', challenge_id: 'challenge-bound-to-choice' })
  await expect(page.getByTestId('desk-announcement')).toContainText('verified approval workflow')
})

test('tier requests use the original process ownership and tier revision', async ({ page }) => {
  const world = await mockDecisionDesk(page, { tier: true })
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-t:tier-request-1').click(); await expect(page.getByTestId('desk-decide')).toBeEnabled()
  await expect(page.getByTestId('stamp-always')).toBeDisabled()
  await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1)
  expect(world.calls[0]?.path).toBe('/api/projects/p-aeon/harness-sessions/tier-session/tier/requests/tier-request-1/decision')
  expect(world.calls[0]?.body).toMatchObject({ tier: 'default', expected_revision: 2, expected_ownership: world.ownership, decision: 'approve' })
  await expect(page.getByTestId('desk-announcement')).toContainText('application waits for the next safe point')
})

test('tier ownership races retain the request without reporting success', async ({ page }) => {
  const world = await mockDecisionDesk(page, { tier: true }); world.denyWrite = true
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-t:tier-request-1').click(); await expect(page.getByTestId('desk-decide')).toBeEnabled(); await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-status')).toContainText('ownership changed')
  await expect(page.getByTestId('desk-announcement')).toContainText('not confirmed')
  expect(world.tier.requests[0]!.state).toBe('pending')
})
