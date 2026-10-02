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
    const captureDir = process.env.AEON_DESK_SHOTS || testInfo.outputPath('desk-shots')
    await mkdir(captureDir, { recursive: true })
    await page.screenshot({ path: join(captureDir, `memo-${width}-${theme}-start.png`) })
    const controls = {
      actions: page.getByTestId('desk-actions').locator('.action-buttons'), decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), pager: page.getByTestId('desk-pager'),
      stamps: page.getByTestId('desk-stamps'), selector: page.getByTestId('desk-choices'), clickedRow: page.getByTestId('choice-row-0'),
      frame: page.getByTestId('desk-frame'),
    }
    await expectStableControls({ controls, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
      { name: 'select another answer', run: async () => { await page.getByTestId('choice-1').click(); await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true') } },
      { name: 'write custom answer', run: async () => { await page.getByTestId('choice-2').click(); await page.getByRole('textbox', { name: 'Something else' }).fill('Keep the query scoped to this tenant.'); await page.getByRole('textbox', { name: 'Something else' }).press('Enter'); await expect(page.getByTestId('desk-status')).toContainText('answer is set') } },
      { name: 'unavailable stamp stays neutral', run: async () => { await expect(page.getByTestId('stamp-always')).toBeDisabled(); await page.getByTestId('desk-paper').press('a'); await expect(page.getByTestId('stamp-once')).toHaveAttribute('aria-pressed', 'true') } },
      { name: 'type and finish P.S.', run: async () => { await page.getByRole('textbox', { name: 'P.S. for the agent' }).fill('Keep the acceptance criteria.'); await page.getByRole('textbox', { name: 'P.S. for the agent' }).press('Enter') } },
      { name: 'skip to next', run: async () => { await page.getByTestId('desk-skip').click(); await expect(page.getByTestId('desk-pager')).toContainText('2 of'); await expect(page.getByTestId('desk-announcement')).toContainText('Skipped for this round') } },
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

test('denied reads keep the memo blocked and inaccessible context explicit', async ({ page }) => {
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
  await page.getByTestId('choice-0').click(); await page.getByTestId('desk-decide').click()
  await expect.poll(() => world.calls.length).toBe(1)
  expect(world.calls[0]?.path).toBe('/api/approvals/approval-1/decision')
  await page.getByTestId('desk-close').click(); await page.getByTestId('desk-row-m:action-1').click()
  const reply = page.getByRole('textbox', { name: 'Reply to the agent' }); await page.getByTestId('choice-0').click(); await reply.fill('Continue with the tenant test.'); await reply.press('Enter')
  await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(2)
  expect(world.calls[1]?.body).toMatchObject({ to: world.action.sender_principal_id, reply_to: 'action-1', recipient_session_id: 'original-generation', body: 'Continue with the tenant test.' })
  await page.getByTestId('desk-close').click(); await page.getByTestId('desk-row-r:rule-1').click(); await page.getByTestId('desk-decide').click()
  await expect.poll(() => world.calls.length).toBe(3)
  expect(world.calls[2]?.path).toBe('/api/rules/doctrine/inbox/rule-1/pull-request')
})

test('expiry while the memo is open blocks the selected approval', async ({ page }) => {
  const time = new Date('2026-10-02T12:00:00Z'); await page.clock.install({ time })
  const world = await mockDecisionDesk(page); world.approval.expires_at = new Date(time.getTime() + 60_000).toISOString()
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-a:approval-1').click(); await page.getByTestId('choice-0').click()
  await expect(page.getByTestId('desk-decide')).toBeEnabled()
  await page.clock.runFor(60_001)
  await expect(page.getByTestId('desk-status')).toContainText('expired'); await expect(page.getByTestId('desk-decide')).toBeDisabled()
  expect(world.calls).toHaveLength(0)
})

test('wide screens reuse server-advertised request-bound phone verification', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 1000 })
  await page.addInitScript(() => Object.defineProperty(navigator, 'credentials', { value: { get: async () => ({ id: 'test-credential', rawId: new Uint8Array([1]).buffer, type: 'public-key', authenticatorAttachment: 'platform', getClientExtensionResults: () => ({}), response: { clientDataJSON: new Uint8Array([2]).buffer, authenticatorData: new Uint8Array([3]).buffer, signature: new Uint8Array([4]).buffer, userHandle: null } }) } }))
  const world = await mockDecisionDesk(page, { phone: true })
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-a:approval-1').click(); await page.getByTestId('choice-0').click(); await expect(page.getByTestId('desk-decide')).toBeEnabled(); await page.getByTestId('desk-decide').click()
  await expect.poll(() => world.calls.length).toBe(2)
  expect(world.calls.map(call => call.path)).toEqual(['/api/phone-approvals/approval/approval-1/options', '/api/phone-approvals/approval/approval-1/decision'])
  expect(world.calls[1]?.body).toMatchObject({ decision: 'approved', request_hash: 'bound-request-hash', challenge_id: 'challenge-bound-to-choice' })
  await expect(page.getByTestId('desk-announcement')).toContainText('verified approval workflow')
})

test('tier requests use the original process ownership and tier revision', async ({ page }) => {
  const world = await mockDecisionDesk(page, { tier: true })
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-t:tier-request-1').click(); await page.getByTestId('choice-0').click(); await expect(page.getByTestId('desk-decide')).toBeEnabled()
  await expect(page.getByTestId('stamp-always')).toBeDisabled()
  await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1)
  expect(world.calls[0]?.path).toBe('/api/projects/p-aeon/harness-sessions/tier-session/tier/requests/tier-request-1/decision')
  expect(world.calls[0]?.body).toMatchObject({ tier: 'default', expected_revision: 2, expected_ownership: world.ownership, decision: 'approve' })
  await expect(page.getByTestId('desk-announcement')).toContainText('application waits for the next safe point')
})

test('tier ownership races retain the request without reporting success', async ({ page }) => {
  const world = await mockDecisionDesk(page, { tier: true }); world.denyWrite = true
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-t:tier-request-1').click(); await page.getByTestId('choice-0').click(); await expect(page.getByTestId('desk-decide')).toBeEnabled(); await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-status')).toContainText('ownership changed')
  await expect(page.getByTestId('desk-announcement')).toContainText('not confirmed')
  expect(world.tier.requests[0]!.state).toBe('pending')
})

test('HTML attachments use the existing separate-origin sandbox and restore memo focus', async ({ page }) => {
  await mockDecisionDesk(page, { html: true })
  await page.context().route('https://preview.example.org/preview/**', route => route.fulfill({ contentType: 'text/html', body: '<!doctype html><button onclick="this.textContent=\'Changed\'">Live fragment</button>' }))
  await openFirst(page)
  const attachment = page.getByRole('button', { name: 'Open Index plan' })
  await attachment.click()
  const iframe = page.locator('iframe.html-preview')
  await expect(iframe).toHaveAttribute('sandbox', 'allow-scripts')
  await expect(iframe).toHaveAttribute('referrerpolicy', 'no-referrer')
  await expect(iframe).toHaveAttribute('src', /^https:\/\/preview\.example\.org\//)
  await page.frameLocator('iframe.html-preview').getByRole('button', { name: 'Live fragment' }).click()
  await expect(page.frameLocator('iframe.html-preview').getByRole('button', { name: 'Changed' })).toBeVisible()
  await page.getByRole('button', { name: 'Close viewer' }).click(); await expect(attachment).toBeFocused()
})

test('short memos stay short before a round requires a scrolling body', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1200 })
  await mockDecisionDesk(page, { short: true })
  await openFirst(page)
  const before = await page.getByTestId('desk-frame').boundingBox()
  expect(before!.height).toBeLessThan(900)
  expect(await page.getByTestId('desk-body').evaluate(element => element.scrollHeight - element.clientHeight)).toBeLessThanOrEqual(1)
  await expectStableControls({ controls: { actions: page.getByTestId('desk-actions').locator('.action-buttons'), stamps: page.getByTestId('desk-stamps'), selectors: page.getByTestId('desk-choices'), clicked: page.getByTestId('choice-row-0') }, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [{ name: 'select without growing a short memo', run: async () => { await page.getByTestId('choice-1').click(); await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true') } }] })
})

// Fix-round regressions: these exercise the reviewed 4c276ce3 failures.
test('Enter on focused Deny selects Deny without granting the preselected approval', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-a:approval-1').click()
  await expect(page.getByTestId('choice-1')).toBeEnabled()
  await page.getByTestId('desk-paper').focus()
  for (let tabs = 0; tabs < 30 && !await page.getByTestId('choice-1').evaluate(el => el === document.activeElement); tabs++) await page.keyboard.press('Tab')
  await expect(page.getByTestId('choice-1')).toBeFocused(); await page.keyboard.press('Enter')
  await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true')
  expect(world.calls).toHaveLength(0)
  await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1)
  expect(world.calls[0]!.body.decision).toBe('denied')
})
test('protected requests require an explicit choice before Decide or memo Enter', async ({ page }) => {
  const world = await mockDecisionDesk(page, { tier: true })
  await page.goto('/decision-desk')
  for (const id of ['a:approval-1', 't:tier-request-1']) {
    await page.getByTestId(`desk-row-${id}`).click(); await expect(page.getByTestId('choice-0')).toBeEnabled()
    await expect(page.getByTestId('choice-0')).toHaveAttribute('aria-checked', 'false')
    await expect(page.getByTestId('desk-decide')).toBeDisabled(); await page.getByTestId('desk-paper').press('Enter')
    expect(world.calls).toHaveLength(0); await page.getByTestId('desk-close').click()
  }
})
for (const [kind, id] of [['approval', 'a:approval-1'], ['tier', 't:tier-request-1']] as const) {
  test(`memo Enter without a selected ${kind} choice keeps the answer hint and focus`, async ({ page }) => {
    const world = await mockDecisionDesk(page, { tier: true })
    await page.goto('/decision-desk'); await page.getByTestId(`desk-row-${id}`).click()
    await expect(page.getByTestId('choice-0')).toBeEnabled()
    await expect(page.locator('[data-field="answer"]')).toHaveCount(0)
    const paper = page.getByTestId('desk-paper')
    await paper.focus()
    await expectStableControls({
      controls: { actions: page.getByTestId('desk-actions').locator('.action-buttons'), decide: page.getByTestId('desk-decide'), stamps: page.getByTestId('desk-stamps'), selectors: page.getByTestId('desk-choices'), clicked: page.getByTestId('choice-row-1') },
      scrollAreas: { body: page.getByTestId('desk-body') },
      interactions: [{ name: 'Enter without a choice', run: async () => {
        await paper.press('Enter')
        await expect(page.getByTestId('desk-answer-summary')).toHaveText('Choose an answer above.')
        await expect(page.getByTestId('desk-status')).toHaveText('Choose an answer above.')
        await expect(paper).toBeFocused()
        await expect(page.getByTestId('desk-decide')).toBeDisabled()
        expect(world.calls).toHaveLength(0)
      } }, { name: 'choose after validation', run: async () => {
        await paper.press('2')
        await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true')
        await expect(page.getByTestId('desk-answer-summary')).not.toContainText('Editing')
        await expect(page.getByTestId('desk-decide')).toBeEnabled()
        expect(world.calls).toHaveLength(0)
      } }],
    })
  })
}
test('memo Enter on an empty custom answer still focuses its answer field', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  await openFirst(page)
  await page.getByTestId('choice-2').click()
  const field = page.getByRole('textbox', { name: 'Something else' })
  await expect(field).toBeFocused(); await field.press('Enter')
  await expect(page.getByTestId('desk-paper')).toBeFocused()
  await page.getByTestId('desk-paper').press('Enter')
  await expect(page.getByTestId('desk-status')).toHaveText('Set an answer before deciding.')
  await expect(field).toBeFocused()
  await expect(page.getByTestId('desk-answer-summary')).toContainText('Editing')
  expect(world.calls).toHaveLength(0)
})
test('open questions remain visible behind 100 older answered questions', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  world.questions.unshift(...Array.from({ length: 100 }, (_, at) => ({ ...sampleQuestion(`old-${at}`), state: 'answered' as const })))
  await page.goto('/decision-desk')
  await expect(page.getByTestId('desk-row-q:question-1')).toBeVisible()
  expect(world.reads.some(path => path.includes('state=open'))).toBe(true)
})
for (const width of [1440, 390]) test(`newest answered questions and fresh corrections survive refresh ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  const world = await mockDecisionDesk(page, { long: true })
  const history = Array.from({ length: 105 }, (_, at) => {
    const question = sampleQuestion(`history-${String(at).padStart(3, '0')}`, { question: `Historical answer ${at}` })
    const created_at = new Date(Date.UTC(2026, 0, 1, 0, at)).toISOString()
    return { ...question, revision: 2, state: 'answered' as const, answer: { id: `answer-history-${at}`, revision: 2, answer: 'Old answer', option_id: 'partial', outcome: 'once' as const, decided_by: 'person', created_at, deliver_after: created_at } }
  })
  world.questions.unshift(...history)
  await openFirst(page)
  await expectStableControls({ controls: {
    actions: page.getByTestId('desk-actions').locator('.action-buttons'), decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), pager: page.getByTestId('desk-pager'),
    stamps: page.getByTestId('desk-stamps'), selectors: page.getByTestId('desk-choices'), clickedRow: page.getByTestId('choice-row-0'), frame: page.getByTestId('desk-frame'),
  }, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
    { name: 'record the old question as a new decision', run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1); await expect(page.getByTestId('desk-pager')).toContainText('2 of') } },
    { name: 'return to the fresh answer', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('desk-pager')).toContainText('1 of') } },
    { name: 'refresh the fresh answer among more than 100 decisions', run: async () => {
      await page.evaluate(() => window.dispatchEvent(new Event('focus')))
      await expect(page.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled()
      await expect(page.getByTestId('desk-status')).not.toContainText('source could not be confirmed')
      await expect(page.getByTestId('desk-decide')).toBeEnabled()
    } },
    { name: 'choose a correction', run: async () => { await page.getByTestId('choice-1').click(); await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true') } },
    { name: 'record the correction without moving controls', run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(2); await expect(page.getByTestId('desk-pager')).toContainText('2 of') } },
  ] })
  expect(world.calls.map(call => call.body.expected_revision)).toEqual([1, 2])
  expect(world.calls[1]!.body.option_id).toBe('full')
  await page.getByTestId('desk-close').click()
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await page.getByRole('button', { name: /^Decided/ }).click()
  const rows = page.locator('.desk-list .desk-row')
  await expect(rows).toHaveCount(100)
  await expect(rows.first()).toHaveAttribute('data-testid', 'desk-row-q:question-1')
  await expect(rows.first()).toContainText('Use a full index.')
  await expect(rows.nth(1)).toHaveAttribute('data-testid', 'desk-row-q:history-104')
  await expect(page.getByTestId('desk-row-q:history-000')).toHaveCount(0)
  await page.getByRole('button', { name: 'Load 100 more' }).click()
  await expect(rows).toHaveCount(106)
  await expect(rows.last()).toHaveAttribute('data-testid', 'desk-row-q:history-000')
  expect(world.reads.some(path => path.includes('state=answered&order=desc') && path.includes('&cursor='))).toBe(true)
  await page.reload()
  await page.getByRole('button', { name: /^Decided/ }).click()
  await expect(rows.first()).toHaveAttribute('data-testid', 'desk-row-q:question-1')
  await rows.first().click(); await page.getByTestId('choice-0').click()
  await expect(page.getByTestId('desk-decide')).toBeEnabled()
})
test('Load 100 more resumes the answered cursor without rereading loaded pages', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  world.questions.push(...Array.from({ length: 205 }, (_, at) => ({ ...sampleQuestion(`answered-${at}`), state: 'answered' as const })))
  await page.goto('/decision-desk')
  await page.getByRole('button', { name: /^Decided/ }).click()
  const rows = page.locator('.desk-list .desk-row'), load = page.getByRole('button', { name: 'Load 100 more' })
  await expect(rows).toHaveCount(100)
  const answeredReads = () => world.reads.filter(path => path.includes('state=answered'))
  const initial = answeredReads().length
  await load.click(); await expect(rows).toHaveCount(200)
  expect(answeredReads()).toHaveLength(initial + 1)
  const secondCursor = new URL(answeredReads().at(-1)!, 'https://test.invalid').searchParams.get('cursor')
  expect(secondCursor).toBeTruthy()
  await load.click(); await expect(rows).toHaveCount(205)
  expect(answeredReads()).toHaveLength(initial + 2)
  expect(new URL(answeredReads().at(-1)!, 'https://test.invalid').searchParams.get('cursor')).not.toBe(secondCursor)
  await expect(load).toHaveCount(0)
  const beforeRefresh = answeredReads().length
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled()
  await expect(rows).toHaveCount(205)
  expect(answeredReads()).toHaveLength(beforeRefresh + 3)
  expect(new URL(answeredReads()[beforeRefresh]!, 'https://test.invalid').searchParams.has('cursor')).toBe(false)
})

test('failed continuation retains loaded decisions and retries the same cursor', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  world.questions.push(...Array.from({ length: 105 }, (_, at) => ({ ...sampleQuestion(`answered-${at}`), state: 'answered' as const })))
  await page.goto('/decision-desk'); await page.getByRole('button', { name: /^Decided/ }).click()
  const rows = page.locator('.desk-list .desk-row'), load = page.getByRole('button', { name: 'Load 100 more' })
  await expect(rows).toHaveCount(100)
  world.failQuestions = true
  await load.click()
  await expect(page.locator('.source-warnings')).toContainText('More questions could not be read')
  await expect(rows).toHaveCount(100)
  await expect(load).toBeEnabled()
  const failed = world.reads.at(-1)
  world.failQuestions = false
  await load.click(); await expect(rows).toHaveCount(105)
  expect(world.reads.at(-1)).toBe(failed)
  await expect(page.locator('.source-warnings')).toHaveCount(0)
})

test('loaded open and answered pages survive focus refresh with their memo still actionable', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  world.questions.push(...Array.from({ length: 100 }, (_, at) => sampleQuestion(`open-${at}`)))
  world.questions.push(...Array.from({ length: 101 }, (_, at) => ({ ...sampleQuestion(`answered-${at}`), state: 'answered' as const })))
  await page.goto('/decision-desk'); await page.getByRole('button', { name: 'Load 100 more' }).click()
  await page.getByTestId('desk-row-q:open-99').click()
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect.poll(() => world.reads.filter(path => path.includes('offset=100') && path.includes('state=open')).length).toBeGreaterThanOrEqual(2)
  await expect(page.getByTestId('desk-decide')).toBeEnabled(); await page.getByTestId('desk-close').click()
  await page.getByRole('button', { name: /^Decided/ }).click(); await page.getByRole('button', { name: 'Load 100 more' }).click()
  await expect(page.getByTestId('desk-row-q:answered-100')).toBeVisible()
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await expect(page.getByTestId('desk-row-q:answered-100')).toBeVisible()
})
test('narrow desktop approvals use the native API when server verification is unavailable', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 }); const world = await mockDecisionDesk(page)
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-a:approval-1').click()
  await page.getByTestId('choice-1').click(); await page.getByTestId('desk-decide').click()
  await expect.poll(() => world.calls.length).toBe(1)
  expect(world.calls[0]!.path).toBe('/api/approvals/approval-1/decision')
})
test('Decided rounds count only items arriving after the round starts', async ({ page }) => {
  const world = await mockDecisionDesk(page); world.questions[0]!.state = 'answered'
  await page.goto('/decision-desk'); await page.getByRole('button', { name: /^Decided/ }).click()
  await page.getByTestId('desk-row-q:question-1').click()
  await expect(page.locator('.arrival-hint')).toHaveCount(0)
  world.questions.push(sampleQuestion('actual-arrival'))
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.locator('.arrival-hint')).toHaveText('1 new for the next round')
})
test('P.S. Enter confirms the note and supplies one decision announcement region', async ({ page }) => {
  await mockDecisionDesk(page); await openFirst(page)
  const field = page.getByRole('textbox', { name: 'P.S. for the agent' }); await field.fill('Separate note'); await field.press('Enter')
  await expect(page.getByTestId('desk-status')).toContainText('note is set')
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' }).locator('[role="status"][aria-live]')).toHaveCount(1)
})

test('focused Skip, pager, attachment and Close retain their native Enter actions', async ({ page }) => {
  const world = await mockDecisionDesk(page); await openFirst(page)
  await page.getByTestId('desk-skip').press('Enter'); await expect(page.getByTestId('desk-pager')).toContainText('2 of')
  expect(world.calls).toHaveLength(0)
  await page.getByTestId('desk-pager').press('Enter'); await expect(page.getByRole('listbox')).toBeFocused()
  await page.getByRole('listbox').press('ArrowUp'); await page.getByRole('listbox').press('Enter')
  await expect(page.getByTestId('desk-pager')).toContainText('1 of')
  await page.getByRole('button', { name: 'Open Index plan' }).press('Enter')
  await expect(page.getByRole('dialog', { name: 'Index plan, attachment 1 of 1' })).toBeVisible()
  await page.getByRole('button', { name: 'Close viewer' }).click()
  await page.getByTestId('desk-close').press('Enter'); await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).not.toBeVisible()
  expect(world.calls).toHaveLength(0)
})
test('permissions revoked while a memo is open block its write and retain its draft', async ({ page }) => {
  const world = await mockDecisionDesk(page); await openFirst(page)
  await page.getByTestId('choice-2').click(); await page.getByRole('textbox', { name: 'Something else' }).fill('Keep my answer')
  world.denyPermission = true
  await page.evaluate(async () => { const authz = await import('/src/lib/authz.ts'); await authz.accessChanged() })
  await expect(page.getByTestId('desk-decide')).toBeDisabled()
  await expect(page.getByRole('textbox', { name: 'Something else' })).toHaveValue('Keep my answer')
  expect(world.calls).toHaveLength(0)
})
test('a source refresh during a blocked response clears Recording without advancing', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  let release!: () => void; world.hold = new Promise(resolve => { release = resolve })
  await openFirst(page); await page.getByTestId('desk-decide').click()
  await expect.poll(() => world.calls.length).toBe(1)
  world.questions[0]!.revision++
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect.poll(() => world.reads.filter(path => path.startsWith('/api/decision-desk')).length).toBeGreaterThanOrEqual(2)
  await expect(page.getByTestId('desk-status')).toContainText('source changed')
  release()
  await expect(page.getByTestId('desk-status')).toContainText('source changed while recording')
  await expect(page.getByTestId('desk-status')).not.toContainText('Recording')
  await expect(page.getByTestId('desk-pager')).toContainText('1 of')
  await expect(page.getByTestId('desk-announcement')).not.toContainText('Decision recorded')
})
test('approval project names and doctrine PR context stay visible', async ({ page }) => {
  await mockDecisionDesk(page); await page.goto('/decision-desk')
  await page.getByTestId('desk-row-a:approval-1').click(); await expect(page.locator('.memo-heading .eyebrow')).toHaveText('Paimos Aeon')
  await page.getByTestId('desk-close').click(); await page.getByTestId('desk-row-r:rule-1').click()
  await expect(page.getByRole('link', { name: 'Open the doctrine pull request' })).toHaveAttribute('href', 'https://github.com/inspr-at/inspr-modules/pull/123')
  await expect(page.getByRole('region', { name: 'Unavailable stamps' })).toContainText('own protected decision flow')
})
