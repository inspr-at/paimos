// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { expectStableControls } from './helpers/stable'
import { mockDecisionDesk, sampleQuestion } from './decision-desk-fixtures'
import { mockRules } from './rules-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { defaultSchedule } from './capacity-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`cutover header preserves ticket and Business access ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    await mockDecisionDesk(page, { theme })
    await mockBusiness(page, businessData())
    await mockSettings(page, settingsData())
    await page.goto('/p/PHAROS/PHAROS-11?view=full')
    const places = page.getByRole('navigation', { name: 'Places' })
    const key = page.getByRole('navigation', { name: 'Breadcrumb' }).locator('.crumb.current')
    const gear = page.getByRole('button', { name: /^App and workspace/ })
    await expect(key).toHaveText('PHAROS-11')
    await expect.poll(() => key.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    await expect(places.getByRole('link')).toHaveCount(width < 600 ? 3 : 4)
    expect(await page.locator('.app-header').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
    const dir = testInfo.outputPath('desk-shots')
    await mkdir(dir, { recursive: true })
    await page.screenshot({ path: join(dir, `header-business-${width}-${theme}.png`) })
    const menu = page.getByRole('menu', { name: 'App and workspace' })
    await expectStableControls({ controls: { places, desk: places.getByRole('link', { name: /Decision Desk/ }), agents: places.getByRole('link', { name: 'Agents', exact: true }), key, gear }, interactions: [
      { name: 'open app menu', run: async () => { await gear.click(); await expect(menu).toBeVisible() } },
      { name: 'close app menu', run: async () => { await menu.press('Escape'); await expect(menu).toBeHidden(); await expect(gear).toBeFocused() } },
    ] })
    await gear.click()
    await expect(menu.getByRole('menuitem', { name: 'Business', exact: true })).toHaveCount(width < 600 ? 1 : 0)
    const menuItems = await menu.getByRole('menuitem').all()
    await menu.press('Home')
    for (const item of menuItems) { await expect(item).toBeFocused(); await page.keyboard.press('ArrowDown') }
    await expect(menuItems[0]!).toBeFocused()
    await page.screenshot({ path: join(dir, `menu-business-${width}-${theme}.png`) })
    if (width < 600) await menu.getByRole('menuitem', { name: 'Business', exact: true }).click()
    else { await menu.press('Escape'); await page.keyboard.press('g'); await page.keyboard.press('b') }
    await expect(page).toHaveURL('/business')
  })
}

async function openFirst(page: Page, waitForPermission = true) {
  await page.goto('/decision-desk')
  await expect(page.getByRole('heading', { name: 'Decision Desk', exact: true })).toBeVisible()
  await page.getByTestId('desk-row-q:question-1').click()
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Open source record', exact: true })).toHaveAttribute('href', '/api/questions/question-1')
  if (waitForPermission) await expect(page.getByTestId('desk-decide')).toBeEnabled()
}
for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`memo stability ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    const world = await mockDecisionDesk(page, { long: true, theme })
    await openFirst(page)
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    const captureDir = testInfo.outputPath('desk-shots')
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
      { name: 'server-enabled Always stamp and shortcut', run: async () => { await expect(page.getByTestId('stamp-always')).toBeEnabled(); await page.getByTestId('desk-paper').press('a'); await expect(page.getByTestId('stamp-always')).toHaveAttribute('aria-pressed', 'true') } },
      { name: 'return to Once and leave unmapped Doctrine unavailable', run: async () => { await page.getByTestId('stamp-once').click(); await expect(page.getByTestId('stamp-doctrine')).toBeDisabled(); await page.getByTestId('desk-paper').press('d'); await expect(page.getByTestId('stamp-once')).toHaveAttribute('aria-pressed', 'true') } },
      { name: 'type and finish P.S.', run: async () => { await page.getByRole('textbox', { name: 'P.S. for the agent' }).fill('Keep the acceptance criteria.'); await page.getByRole('textbox', { name: 'P.S. for the agent' }).press('Enter') } },
      { name: 'skip to next', run: async () => { await page.getByTestId('desk-skip').click(); await expect(page.getByTestId('desk-pager')).toContainText('3 of'); await expect(page.getByTestId('desk-announcement')).toContainText('Skipped for this round') } },
      { name: 'page back', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('desk-pager')).toContainText('2 of') } },
      { name: 'decide and advance', run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1); await expect(page.getByTestId('desk-pager')).toContainText('3 of'); await expect(page.getByTestId('desk-decide')).toBeFocused() } },
      { name: 'revisit decision', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('desk-decide')).toContainText('Next') } },
      { name: 'correct and restart delivery', run: async () => { await page.getByTestId('choice-0').click(); await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(2); await expect(page.getByTestId('desk-pager')).toContainText('3 of') } },
      { name: 'return to attachments', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByRole('button', { name: 'Open Index plan' })).toBeVisible() } },
      { name: 'lightbox and return focus', run: async () => { const attachment = page.getByRole('button', { name: 'Open Index plan' }); await attachment.click(); await expect(page.getByRole('dialog', { name: 'Index plan, attachment 1 of 1' })).toBeVisible(); await page.getByRole('button', { name: 'Close viewer' }).click(); await expect(attachment).toBeFocused(); await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible() } },
    ] })
    expect(world.calls.map(call => call.body.expected_revision)).toEqual([1, 2])
    expect(world.calls[0]?.body.answer).toBe('Keep the query scoped to this tenant.')
    const dir = testInfo.outputPath('desk-shots')
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
  await expect(page.getByTestId('desk-pager')).toContainText('3 of'); await expect(page.getByTestId('desk-pager')).toBeFocused()
  await page.getByTestId('desk-paper').press('Escape'); await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).not.toBeVisible()
  await expect(page.getByTestId('desk-row-q:question-1')).toBeFocused()
})

test('failed and stale writes keep drafts and announce failure without advancing', async ({ page }) => {
  const world = await mockDecisionDesk(page); world.denyWrite = true
  await openFirst(page); await page.getByTestId('choice-2').click()
  const field = page.getByRole('textbox', { name: 'Something else' }); await field.fill('Keep this draft'); await field.press('Enter')
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-status')).toContainText('changed')
  await expect(page.getByTestId('desk-pager')).toContainText('2 of'); await expect(field).toHaveValue('Keep this draft')
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
  await expect(page.getByTestId('desk-pager')).toContainText('2 of 5')
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

test('native approval facts preserve requester, risk, target and expiry without a session', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  Object.assign(world.approval, { agent_name: 'Harbor Clerk', scope: 'stage.deploy', resource_kind: 'tenant', resource_id: null, risk: 'high', target: { hosts: ['qa-fixture'], environment: 'staging', service: 'fixture-service', image: 'fixture-image', change: 'fixture-change' } })
  await page.goto('/agents?needs=a:approval-1')
  const facts = page.getByRole('region', { name: 'Permission request', exact: true })
  await expect(facts).toContainText('Asked by Harbor Clerk')
  await expect(facts).toContainText('High risk')
  await expect(facts).toContainText('The whole workspace')
  await expect(facts.locator('time')).toHaveAttribute('datetime', world.approval.expires_at)
  await expect(facts.getByRole('region', { name: /^Deploy target:/ })).toContainText('qa-fixture · staging')
  await expect(facts).toContainText('fixture-image')
  await expect(page.getByRole('link', { name: 'Open source record', exact: true })).toHaveAttribute('href', '/agents?needs=a:approval-1')
  await page.getByTestId('desk-close').click()
  world.approval.agent_name = null
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled()
  await page.getByTestId('desk-row-a:approval-1').click()
  await expect(facts).toContainText('Asked by Agent agent-1')
  expect(world.calls).toHaveLength(0)
})

test('expiry while the memo is open blocks the selected approval', async ({ page }) => {
  const time = new Date('2026-10-02T12:00:00Z'); await page.clock.install({ time })
  const world = await mockDecisionDesk(page); world.approval.expires_at = new Date(time.getTime() + 60_000).toISOString()
  world.projectionTime = time.getTime()
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
  await expect(page.getByTestId('desk-pager')).toContainText('2 of 5')
  await expectStableControls({ controls: {
    actions: page.getByTestId('desk-actions').locator('.action-buttons'), decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), pager: page.getByTestId('desk-pager'),
    stamps: page.getByTestId('desk-stamps'), selectors: page.getByTestId('desk-choices'), clickedRow: page.getByTestId('choice-row-0'), frame: page.getByTestId('desk-frame'),
  }, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
    { name: 'record the old question as a new decision', run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1); await expect(page.getByTestId('desk-pager')).toContainText('3 of 5') } },
    { name: 'return to the fresh answer', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('desk-pager')).toContainText('2 of 5') } },
    { name: 'refresh the fresh answer among more than 100 decisions', run: async () => {
      await page.evaluate(() => window.dispatchEvent(new Event('focus')))
      await expect(page.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled()
      await expect(page.getByTestId('desk-status')).not.toContainText('source could not be confirmed')
      await expect(page.getByTestId('desk-decide')).toBeEnabled()
    } },
    { name: 'choose a correction', run: async () => { await page.getByTestId('choice-1').click(); await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true') } },
    { name: 'record the correction without moving controls', run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(2); await expect(page.getByTestId('desk-pager')).toContainText('3 of 5') } },
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
  await expect(page.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled()
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
  await page.getByTestId('desk-skip').press('Enter'); await expect(page.getByTestId('desk-pager')).toContainText('3 of')
  expect(world.calls).toHaveLength(0)
  await page.getByTestId('desk-pager').press('Enter'); await expect(page.getByRole('listbox')).toBeFocused()
  await page.getByRole('listbox').press('ArrowUp'); await page.getByRole('listbox').press('Enter')
  await expect(page.getByTestId('desk-pager')).toContainText('2 of')
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
  await expect(page.getByTestId('desk-pager')).toContainText('2 of')
  await expect(page.getByTestId('desk-announcement')).not.toContainText('Decision recorded')
})
test('approval project names and doctrine PR context stay visible', async ({ page }) => {
  await mockDecisionDesk(page); await page.goto('/decision-desk')
  await page.getByTestId('desk-row-a:approval-1').click(); await expect(page.locator('.memo-heading .eyebrow')).toHaveText('Paimos Aeon')
  await page.getByTestId('desk-close').click(); await page.getByTestId('desk-row-r:rule-1').click()
  await expect(page.getByRole('link', { name: 'Open the doctrine pull request' })).toHaveAttribute('href', 'https://github.com/inspr-at/inspr-modules/pull/123')
  await expect(page.getByRole('region', { name: 'Unavailable stamps' })).toContainText('own protected decision flow')
})


test('legacy links select the canonical merged question and one server count', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  world.questions[0]!.askers[0]!.input.source_request_id = world.action.id
  await page.goto('/agents?needs=m:action-1')
  await expect(page).toHaveURL(/decision-desk\?item=m/)
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  await expect(page.getByTestId('desk-paper').getByRole('heading', { name: 'Which migration should carry the index?' })).toBeVisible()
  await expect(page.getByTestId('desk-pager')).toContainText('of 4')
  await page.getByTestId('desk-close').click()
  await expect(page.getByTestId('desk-row-m:action-1')).toHaveCount(0)
  await expect(page.locator('.places').getByRole('link', { name: 'Decision Desk, 4 open', exact: true })).toBeVisible()
  await expect(page.locator('.view-tabs').getByRole('button', { name: 'Open 4' })).toBeVisible()
  expect(world.calls).toHaveLength(0)
})

test('history remains reachable without a live session and revokes through the native API', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  world.approval.decision = 'approved'
  world.approvalOutsideList = true
  await page.goto('/approvals?item=a:approval-1')
  await expect(page).toHaveURL(/decision-desk/)
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  await expect(page.getByTestId('desk-revoke')).toBeEnabled()
  await expectStableControls({ controls: { revoke: page.getByTestId('desk-revoke'), next: page.getByTestId('desk-decide'), pager: page.getByTestId('desk-pager') }, interactions: [
    { name: 'revoke the original grant', run: async () => { await page.getByTestId('desk-revoke').click(); await expect(page.getByTestId('desk-status')).toContainText('grant was revoked'); await expect(page.getByTestId('desk-revoke')).toBeDisabled() } },
  ] })
  expect(world.calls.map(call => call.path)).toEqual(['/api/approvals/approval-1/revoke'])
  await expect(page.getByTestId('desk-decide')).toContainText('Next')
})

test('projection access errors show an unknown count and an actionable Retry', async ({ page }) => {
  const world = await mockDecisionDesk(page); world.failProjection = true
  await page.goto('/decision-desk')
  await expect(page.getByRole('button', { name: 'Retry', exact: true })).toBeEnabled()
  await expect(page.locator('.places').getByRole('link', { name: 'Decision Desk, count unavailable', exact: true })).toBeVisible()
  await expect(page.locator('.view-tabs').getByRole('button', { name: 'Open ?' })).toBeVisible()
  world.failProjection = false
  await page.getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(page.locator('.places').getByRole('link', { name: 'Decision Desk, 5 open', exact: true })).toBeVisible()
  await expect(page.getByTestId('desk-row-q:question-1')).toBeVisible()
})

test('an exact question link recovers its canonical row beyond the bulk detail budget', async ({ page }) => {
  const world = await mockDecisionDesk(page)
  world.questions.push(...Array.from({ length: 201 }, (_, at) => sampleQuestion(`overflow-${at}`, { question: `Original overflow question ${at}` })))
  await page.goto('/decision-desk?item=q:overflow-200')
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  await expect(page.getByTestId('desk-paper').getByRole('heading', { name: 'Original overflow question 200' })).toBeVisible()
  await expect(page.getByTestId('desk-decide')).toBeEnabled()
  expect(world.reads).toContain('/api/questions/overflow-200')
  await page.getByTestId('desk-close').click()
  await expect(page.getByTestId('desk-row-q:overflow-200')).toHaveCount(1)
  await expect(page.locator('.view-tabs').getByRole('button', { name: 'Open 206' })).toBeVisible()
  expect(world.calls).toHaveLength(0)
})

for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`cutover overview and agents ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    const world = await mockDecisionDesk(page, { theme, tier: true })
    world.questions[0]!.input.question = 'Welche verbindliche Entscheidung erhält die mandantenspezifischen Berechtigungen bei der Fortsetzung der Qualitätsprüfung?'
    await page.goto('/decision-desk')
    await expect(page.getByTestId('desk-row-q:question-1')).toBeVisible()
    const dir = testInfo.outputPath('aeon-569-desk-cutover'); await mkdir(dir, { recursive: true })
    await page.screenshot({ path: join(dir, `overview-${width}-${theme}.png`) })
    await expectStableControls({ controls: { refresh: page.getByRole('button', { name: 'Refresh', exact: true }), review: page.getByRole('button', { name: 'One at a time' }), open: page.locator('.view-tabs button').nth(0), decided: page.locator('.view-tabs button').nth(1), header: page.locator('.places') }, scrollAreas: { view: page.locator('.decision-desk') }, interactions: [
      { name: 'switch to history', run: async () => { await page.locator('.view-tabs button').nth(1).click(); await expect(page.getByRole('combobox', { name: 'Filter decided by stamp' })).toBeVisible() } },
      { name: 'return to open', run: async () => { await page.locator('.view-tabs button').nth(0).click(); await expect(page.getByTestId('desk-row-q:question-1')).toBeVisible() } },
    ] })
    const account = { id: 'signin-account', account_key: 'fixture-account', harness: 'codex', daemon_id: 'fixture-host', host_label: 'Arbeitsrechner für mandantenspezifische Qualitätsprüfungen', label: 'Qualitätsprüfung', registered_by_principal_id: 'person', state: 'unavailable', last_probe_ok: false, created_at: new Date().toISOString() }
    await page.route('**/api/agent-accounts', route => route.fulfill({ json: [account] }))
    await page.route('**/api/agent-accounts/capacity', route => route.fulfill({ json: [{ account_id: account.id, probe_failure: 'auth_failed', schedule: defaultSchedule(), windows: [] }] }))
    await page.route('**/api/agent-accounts/capacity/schedule', route => route.fulfill({ json: [{ scope: 'user', schedule: { ...defaultSchedule(), reserve: 'auto' } }] }))
    await page.addInitScript(() => Object.defineProperty(navigator, 'clipboard', { value: { writeText: async () => { throw new Error('Clipboard unavailable') } } }))
    await page.goto('/agents')
    await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Needs you', exact: true })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Approve', exact: true })).toHaveCount(0)
    await expect(page.locator('.places').getByRole('link', { name: 'Decision Desk, 6 open', exact: true })).toBeVisible()
    const chores = page.getByRole('region', { name: 'Sign-ins and connections' })
    await expect(chores).toBeVisible()
    await expectStableControls({ controls: { copy: chores.getByRole('button', { name: 'Copy command' }), header: page.locator('.places') }, interactions: [{ name: 'copy failure stays below controls', run: async () => {
      await chores.getByRole('button', { name: 'Copy command' }).click(); await expect(chores.getByRole('status')).toContainText('could not be copied')
    } }] })
    await page.screenshot({ path: join(dir, `agents-${width}-${theme}.png`) })
    expect(await page.locator('.app-header').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)

    await page.goto('/agents/tier-session')
    const tierReview = page.getByRole('region', { name: 'Service tier', exact: true }).getByRole('link', { name: 'Review in Decision Desk', exact: true })
    await expect(tierReview).toHaveAttribute('href', '/decision-desk?item=t:tier-request-1')
    await tierReview.scrollIntoViewIfNeeded()
    await expectStableControls({ controls: { review: tierReview }, scrollAreas: { panel: page.locator('.session-panel .scroll').first() }, interactions: [{ name: 'follow the original tier request and return', run: async () => {
      await tierReview.click(); await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
      await page.getByTestId('desk-close').click(); await page.goto('/agents/tier-session'); await expect(tierReview).toBeVisible(); await tierReview.scrollIntoViewIfNeeded()
    } }] })
    await expect(page.getByRole('button', { name: 'Decline', exact: true })).toHaveCount(0)
    expect(world.calls).toHaveLength(0)
    await page.screenshot({ path: join(dir, `session-tier-${width}-${theme}.png`) })

    // The retired briefing link continues to the canonical Agents panel.
    await page.goto('/briefing')
    await expect(page).toHaveURL('/agents')
    const briefingDesk = page.getByRole('region', { name: 'Decision Desk', exact: true })
    await expect(briefingDesk).toContainText(world.questions[0]!.input.question)
    await expect(briefingDesk.getByLabel('Open decisions')).toHaveText('6')
    await expectStableControls({ controls: { review: page.getByTestId('agents-desk-review'), history: page.getByTestId('agents-desk-history'), header: page.locator('.places') }, interactions: [{ name: 'refresh canonical panel', run: async () => {
      await page.evaluate(() => window.dispatchEvent(new Event('focus')))
      await expect(briefingDesk).toContainText(world.questions[0]!.input.question)
      await expect(briefingDesk.getByLabel('Open decisions')).toHaveText('6')
    } }] })
    await page.screenshot({ path: join(dir, `briefing-${width}-${theme}.png`) })

    await mockRules(page, { settings: true })
    await page.route('**/api/rules/doctrine', route => route.fulfill({ json: { sources: [], proposals_enabled: true } }))
    await page.route('**/api/rules/doctrine/proposals', route => route.fulfill({ json: { proposals: [] } }))
    await page.goto('/settings/agent-rules')
    const doctrine = page.getByRole('region', { name: /Doctrine/ })
    await expect(doctrine.getByText('Agent proposals are reviewed in')).toBeVisible()
    await expect(doctrine.getByRole('link', { name: 'Decision Desk', exact: true })).toHaveAttribute('href', '/decision-desk')
    await expectStableControls({ controls: { link: doctrine.getByRole('button', { name: 'Link repository', exact: true }), desk: doctrine.getByRole('link', { name: 'Decision Desk', exact: true }) }, interactions: [{ name: 'open and close source dialog', run: async () => {
      await doctrine.getByRole('button', { name: 'Link repository', exact: true }).click(); await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).click()
    } }] })
    await page.screenshot({ path: join(dir, `doctrine-${width}-${theme}.png`) })
  })
}

// AEON-569 fix2: the added place must fit root pages as well as ticket trails.
for (const width of [320, 1024]) for (const theme of ['light', 'dark'] as const) {
  test(`cutover shell fits root and ticket controls ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    await mockDecisionDesk(page, { theme })
    await mockBusiness(page, businessData())
    for (const path of ['/', '/tickets', '/p/PHAROS', '/p/PHAROS/PHAROS-11?view=full']) {
      await page.goto(path)
      const places = page.getByRole('navigation', { name: 'Places' })
      const gear = page.getByRole('button', { name: /^App and workspace/ })
      await expect(places.getByRole('link', { name: /Decision Desk/ })).toBeVisible()
      await page.evaluate(() => document.fonts.ready)
      await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0)
      await expect.poll(() => page.locator('.app-header').evaluate(el => el.scrollWidth - el.clientWidth)).toBe(0)
      for (const control of await places.getByRole('link').all()) {
        if (!await control.isVisible()) continue
        const box = await control.boundingBox()
        expect(box).toBeTruthy()
        expect(box!.width).toBeGreaterThan(0)
        expect(box!.x).toBeGreaterThanOrEqual(0)
        expect(box!.x + box!.width).toBeLessThanOrEqual(width)
        if (width === 320) expect(box!.width).toBeGreaterThanOrEqual(44)
      }
      const fold = page.getByRole('button', { name: 'PHAROS: project header, expanded', exact: true })
      if (await fold.isVisible()) {
        const box = await fold.boundingBox(), search = await page.getByRole('button', { name: 'Search everything', exact: true }).boundingBox()
        expect(box && search).toBeTruthy()
        expect(box!.width).toBeGreaterThanOrEqual(44)
        expect(box!.x + box!.width, 'project fold must not overlap Search').toBeLessThanOrEqual(search!.x)
      }
      await expectStableControls({ controls: { places, gear }, interactions: [
        { name: 'open and close app menu', run: async () => { await gear.click(); await expect(page.getByRole('menu', { name: 'App and workspace' })).toBeVisible(); await page.keyboard.press('Escape'); await expect(gear).toBeFocused() } },
      ] })
      await page.screenshot({ path: testInfo.outputPath(`${path === '/' ? 'projects' : path.includes('view=full') ? 'ticket' : path.includes('/p/') ? 'project' : 'tickets'}-${width}-${theme}.png`) })
    }
  })
}

// Risk: moving attachments to chores must retain expiry, consent scope and receipts.
for (const width of [390, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`connection facts and review stay bound to the original request ${width} ${theme}`, async ({ page }, testInfo) => {
    const now = Date.parse('2026-10-09T12:00:00Z')
    await page.clock.install({ time: now })
    await page.clock.pauseAt(now)
    await page.setViewportSize({ width, height: 1000 })
    // The connection read succeeds while unrelated session/account APIs fail.
    await mockDecisionDesk(page, { theme })
    await page.route('**/api/me/permissions*', route => {
      const permissions = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
      permissions.workspace.permissions.push('account.manage', 'approvals.read', 'questions.read', 'questions.decide', 'rules.write')
      return route.fulfill({ json: permissions })
    })
    const review = { request_id: 'connection-original', request_digest: 'a'.repeat(64), consent_digest: 'b'.repeat(64), consent_mode: 'aeon', state: 'pending', expires_at: new Date(now + 120_000).toISOString(), snapshot: { platform: 'darwin', computer_id: 'computer-fixture', project_id: 'p-pharos', ticket_id: 'n-2', host: 'Arbeitsrechner für mandantenspezifische Qualitätsprüfungen', harness: 'codex', transcript: '/fixture/session.jsonl', file_id: '1:234', process: { pid: 1234, uid: 501, started: new Date(now).toISOString(), executable: '/fixture/codex', cwd: '/fixture/project' } } }
    await page.route('**/api/agent-pairing/attach/pending', route => route.fulfill({ json: { requests: [review] } }))
    const writes: string[] = []
    await page.route('**/api/agent-pairing/attach/*/approve', route => { writes.push(new URL(route.request().url()).pathname); return route.fulfill({ json: { ...review, state: 'approved' } }) })
    await page.goto('/agents')
    const chores = page.getByRole('region', { name: 'Sign-ins and connections', exact: true })
    const row = chores.locator('li').filter({ hasText: review.snapshot.host })
    const button = row.getByRole('button', { name: 'Review connection', exact: true })
    await expect(row).toContainText('and share its conversation')
    await expect(row).toContainText('PHAROS-12')
    await expect(row).toContainText('Your terminal on')
    await expect(row.locator('time')).toHaveAttribute('datetime', review.expires_at)
    await expect(row).toContainText('Expires in 2m')
    await expectStableControls({ controls: { review: button, row }, interactions: [
      { name: 'open native request and cancel', run: async () => { await button.click(); const dialog = page.getByRole('dialog'); await expect(dialog).toContainText(review.snapshot.host); expect(writes).toEqual([]); await page.getByRole('button', { name: 'Close attach review' }).click() } },
    ] })
    await page.screenshot({ path: testInfo.outputPath(`connection-${width}-${theme}.png`) })
    await button.click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('radio', { name: 'Allow', exact: true }).check()
    await expect(dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeEnabled()
    await page.clock.fastForward(120_001)
    await expect(dialog).toContainText('Request expired. Run aeon-agentd attach again.')
    await expect(dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeDisabled()
    expect(writes).toEqual([])
  })
}
