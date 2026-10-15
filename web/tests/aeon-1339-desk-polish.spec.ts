// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1339 risk: the shipped Decision Desk drifts from the approved rev 11 artifact: Decide does
// nothing on a click, the round stays open after the last decision, the legend wraps, the project
// shows twice with a dot, or the folder tab's shoulder draws a kinked or uneven hairline; and, from
// Tick's fix round 4, the details below that the approved file draws (stamp colour, narrow bar, names).
import { expect, test, type Page } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { expectStableControls } from './helpers/stable'

const heading = (page: Page) => page.getByTestId('desk-paper').getByRole('heading', { level: 2 })
// One open question: approval decided, action merged into the question, rule proposed, the second question gone.
async function oneQuestion(page: Page, theme?: 'light' | 'dark') {
  const world = await mockDecisionDesk(page, { theme })
  world.approval.decision = 'approved'; world.rule.state = 'proposed'; world.questions.splice(1)
  world.questions[0]!.input.source_request_id = world.action.id
  return world
}

test('the memo matches the approved sheet and closes after the last decision', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
    await page.unrouteAll({ behavior: 'ignoreErrors' })
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const world = await oneQuestion(page, theme)
    await page.goto('/decision-desk')
    await expect(page.getByRole('heading', { level: 1, name: 'Decision Desk' })).toBeVisible()
    await expect(page.getByText('Handover-question import')).toHaveCount(0)
    await expect(page.getByText('Sign-ins and account setup')).toHaveCount(0)
    await expect(page.getByTestId('desk-list')).toContainText('Recommends')
    await page.screenshot({ path: testInfo.outputPath(`list-${width}-${theme}.png`), fullPage: true })

    await page.getByTestId('desk-one-by-one').click()
    await expect(heading(page)).toHaveText('Which migration should carry the index?')
    // The project once: its key on the folder tab, its name with the folder icon in the meta; no dot anywhere.
    await expect(page.getByTestId('desk-tab-project')).toHaveText('PRJ-35')
    await expect(page.getByTestId('desk-project-link')).toHaveText('Paimos Aeon')
    await expect(page.locator('.desk-dlg .project-dot, .desk-dlg i[aria-hidden]')).toHaveCount(0)
    // A round of one has no pager, and its primary reads Decide.
    await expect(page.getByTestId('desk-pager')).toHaveCount(0)
    await expect(page.getByTestId('desk-decide')).toHaveAccessibleName(/^Decide\b(?! &)/)
    await expect(page.getByTestId('desk-stamp')).toHaveText('Once')
    await expect(page.getByTestId('desk-line')).toContainText('Parked')
    // Options wrap in the row; there is no second copy of the selected answer and one scroll container.
    await expect(page.getByTestId('desk-answer-summary')).toHaveCount(0)
    expect(await page.getByTestId('desk-body').evaluate(element => getComputedStyle(element).overflowY)).toBe('visible')
    await expect(page.getByTestId('desk-paper')).toHaveCSS('outline-style', 'none')
    if (width === 1440) {
      // The legend never wraps: one line, no Keys fold at 1440.
      const hint = page.locator('.desk-hint')
      await expect(hint).toBeVisible()
      expect(await hint.evaluate(element => element.scrollWidth <= element.clientWidth + 0.5 && element.getBoundingClientRect().height < 30)).toBe(true)
      await expect(page.getByRole('button', { name: 'Keys' })).toBeHidden()
    }
    await page.screenshot({ path: testInfo.outputPath(`memo-${width}-${theme}.png`) })
    if (width === 1440) {
      // Tick: the hairline is gated at 2× (below); at 1× a visual crop of the shoulder beside the sheet corner.
      const tab = (await page.getByTestId('desk-tab').boundingBox())!, stack = (await page.locator('.stack').boundingBox())!
      await page.screenshot({ path: testInfo.outputPath(`shoulder-1x-${theme}.png`), clip: { x: tab.x + tab.width - 30, y: stack.y - 30, width: 60, height: 50 } })
      await page.screenshot({ path: testInfo.outputPath(`corner-1x-${theme}.png`), clip: { x: stack.x + stack.width - 30, y: stack.y - 10, width: 50, height: 40 } })
    }

    await expectStableControls({
      controls: { decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), tab: page.getByTestId('desk-tab'), stamps: page.getByTestId('desk-stamps'), first: page.getByTestId('choice-row-0') },
      scrollAreas: { body: page.getByTestId('desk-body') },
      interactions: [
        { name: 'choose the second option', run: async () => { await page.getByTestId('choice-1').click(); await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true') } },
        { name: 'stamp Always', run: async () => { await page.getByTestId('stamp-always').click(); await expect(page.getByTestId('desk-stamp')).toHaveText('Always') } },
        { name: 'click the unavailable Doctrine stamp', run: async () => { await page.getByTestId('stamp-doctrine').click({ force: true }); await expect(page.getByTestId('stamp-always')).toHaveAttribute('aria-checked', 'true') } },
      ],
    })
    await page.getByTestId('stamp-once').click()
    await page.getByTestId('desk-decide').click()
    // All decided for 1.2 s, with Undo; the tab reads Round finished and Done keeps the primary's size.
    await expect(page.getByTestId('desk-all-decided')).toContainText('All decided')
    await expect(page.getByTestId('desk-all-decided')).toContainText('1 answered, stamped Once.')
    await expect(page.getByTestId('desk-tab')).toContainText('Round finished')
    await page.screenshot({ path: testInfo.outputPath(`all-decided-${width}-${theme}.png`) })
    await expect(page.getByTestId('desk-paper')).toBeHidden({ timeout: 5000 })
    expect(world.calls.filter(call => call.path.endsWith('/decision'))).toHaveLength(1)
    // Undo acts as Change until a withdraw exists (AEON-1340): the memo reopens on the recorded answer.
    const undo = page.getByRole('button', { name: 'Undo', exact: true })
    await expect(page.getByText(/^Decided\. It goes to .+ in a few seconds\.$/)).toBeVisible()
    await undo.click()
    await expect(heading(page)).toHaveText('Which migration should carry the index?')
    await expect(page.getByTestId('desk-status')).toContainText('Change your answer')
    await expect(page.getByTestId('choice-0')).toBeEnabled()
  }
})

test('Decide is never a silent no-op: it says why beside the button and moves focus to what is missing', async ({ page }, testInfo) => {
  test.setTimeout(90_000)
  for (const theme of ['light', 'dark'] as const) {
    await page.unrouteAll({ behavior: 'ignoreErrors' })
  await page.setViewportSize({ width: 1440, height: 1000 })
  const world = await mockDecisionDesk(page, { theme })
  world.questions[0]!.suggested_outcome = 'doctrine'; world.questions[0]!.suggestion_reason = 'agent_suggestion'
  await page.goto('/decision-desk')
  await page.getByTestId('desk-row-a:approval-1').click()
  await expect(heading(page)).toHaveText('Allow nodes.read?')
  const decide = page.getByTestId('desk-decide')
  await expect(decide).toHaveAttribute('aria-disabled', 'true')
  await expect(page.getByTestId('desk-stamp')).toHaveText('Not decided')
  await decide.click({ force: true })
  await expect(page.getByTestId('desk-status')).toHaveText('Choose Approve or Deny first.')
  await expect(page.getByTestId('choice-0')).toBeFocused()
  expect(world.calls).toHaveLength(0)
  await page.screenshot({ path: testInfo.outputPath(`decide-why-${theme}.png`) })
  await page.keyboard.press('j')
  // The agent suggested Doctrine, which is unavailable: Once starts selected, with a quiet note, and decides in one press.
  await expect(heading(page)).toHaveText('Which migration should carry the index?')
  await expect(page.getByTestId('stamp-once')).toHaveAttribute('aria-checked', 'true')
  await expect(page.getByTestId('stamp-doctrine')).toHaveAttribute('aria-disabled', 'true')
  await expect(page.getByTestId('desk-suggest-off')).toHaveText('Suggested: Doctrine · not available for this question')
  await expect(page.getByTestId('desk-suggest-off')).toHaveAttribute('data-tip', /didn’t name the rule/)
  await page.screenshot({ path: testInfo.outputPath(`suggest-off-${theme}.png`) })
  await decide.click()
  await expect.poll(() => world.calls.filter(call => call.path.endsWith('/decision')).length).toBe(1)
  expect(world.calls.at(-1)!.body).toMatchObject({ outcome: 'once', option_id: 'partial' })
  // The decided state: the landed stamp, the delivery line in the meanwhile slot, Change and Next.
  await expect(heading(page)).toHaveText('Should the successor continue the review?')
  await page.keyboard.press('k')
  await expect(page.getByTestId('desk-stamp')).toHaveAttribute('aria-label', 'Stamped Once')
  await expect(page.getByTestId('desk-line')).toContainText(/^Decided .+sends to the agent in \d+ s.*Undo$/)
  await expect(page.getByTestId('desk-skip')).toHaveAccessibleName(/^Change/)
  await expect(page.getByTestId('desk-decide')).toHaveAccessibleName(/^Next/)
  await expect(page.getByTestId('choice-0')).toBeDisabled()
  await page.screenshot({ path: testInfo.outputPath(`decided-${theme}.png`) })
  }
})

// The Agents panel and the header preview use the Desk row anatomy: a kind mark, the project with the
// folder icon, and links that bring the person back after the last decision.
test('panel and preview rows name the kind and project, and link back', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
    await page.unrouteAll({ behavior: 'ignoreErrors' })
  await page.setViewportSize({ width, height: 1000 })
  await mockDecisionDesk(page, { theme })
  await page.goto('/tests/desk-panel-harness.html')
  const panel = page.getByRole('region', { name: 'Decision Desk', exact: true })
  await expect(panel.getByTestId('agents-desk-item-q:question-1')).toHaveAttribute('href', '/decision-desk?from=agents&item=q:question-1')
  await expect(panel.locator('.mark.k-question').first()).toBeVisible()
  await expect(panel.locator('.row-project i, .project-dot')).toHaveCount(0)
  await expect(panel.getByTestId('agents-desk-item-m:action-1')).toHaveText('Action request')
  await panel.screenshot({ path: testInfo.outputPath(`panel-${width}-${theme}.png`) })
  await page.goto('/agents')
  await page.getByRole('button', { name: 'Preview the Decision Desk' }).click()
  const row = page.getByTestId('desk-preview-item-q:question-1')
  await expect(row).toHaveAttribute('href', '/decision-desk?from=preview&item=q:question-1')
  await expect(row.locator('.dp-ic.k-question')).toBeVisible()
  await expect(row.locator('.dp-project')).toHaveText('Paimos Aeon')
  // Fix round 4: the preview's rule row names its project, and action requests have one name everywhere.
  await expect(page.getByTestId('desk-preview-item-r:rule-1').locator('.dp-project')).toHaveText('Paimos Aeon')
  await expect(page.getByTestId('desk-preview-item-m:action-1')).toContainText('Action request')
  await expect(page.getByTestId('desk-preview-item-m:action-1')).not.toContainText(/Human request|Action request · Action request/)
  await page.screenshot({ path: testInfo.outputPath(`preview-${width}-${theme}.png`) })
  }
  await page.unrouteAll({ behavior: 'ignoreErrors' })
  await page.setViewportSize({ width: 1440, height: 1000 })
  const world = await oneQuestion(page)
  await page.goto('/agents')
  await page.getByRole('button', { name: 'Preview the Decision Desk' }).click()
  await page.getByTestId('desk-preview-item-q:question-1').click()
  await expect(page).toHaveURL('/decision-desk?from=preview&item=q:question-1')
  await expect(heading(page)).toHaveText('Which migration should carry the index?')
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-all-decided')).toBeVisible()
  await expect(page).toHaveURL('/agents', { timeout: 5000 })
  await expect(page.locator('[data-place="desk"] .place-main')).toBeFocused()
  expect(world.calls.filter(call => call.path.endsWith('/decision'))).toHaveLength(1)
  await expect(page.getByText(/^Decided\. It goes to .+ in a few seconds\.$/)).toBeVisible()
})

// Tick's visual check at c2e307d (fix round 4). Risk: the chosen stamp stays neutral, list rows lose the ticket
// the memo names, approvals repeat raw ids, risk, asker and expiry, action requests carry three names, and the
// stamp line runs two sentences together.
// One test with steps: a row per case would push the shared desk fixture's browser fan-out past 300 cases.
test('fix round 4: stamp colours, ticket chips, one name, no raw ids, the narrow bar and the Decided filter', async ({ page }, testInfo) => {
  test.setTimeout(420_000)
  await test.step('outcome-coloured stamps, the memo ticket on rows, one name and no raw ids', async () => {
    const tone = (id: string) => page.getByTestId(id).evaluate(element => getComputedStyle(element).borderTopColor)
    // A token as the memo resolves it, in the current theme.
    const token = (name: string) => page.evaluate(variable => {
      const probe = document.createElement('span'); probe.style.color = `var(${variable})`
      document.querySelector('.desk-dlg')!.appendChild(probe); const color = getComputedStyle(probe).color; probe.remove(); return color
    }, name)
    for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
      await page.unrouteAll({ behavior: 'ignoreErrors' })
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      const world = await mockDecisionDesk(page, { theme })
      world.questions[0]!.suggested_outcome = 'doctrine'; world.questions[0]!.suggestion_reason = 'agent_suggestion'
      await page.goto('/decision-desk')
      // The rows carry the ticket the memo names: the chip, its title and "Parked AEON-1".
      const question = page.getByTestId('desk-row-q:question-1')
      await expect(question.locator('.res-key')).toHaveText('AEON-1')
      await expect(question.locator('.status-line')).toContainText('Parked AEON-1')
      const rule = page.getByTestId('desk-row-r:rule-1')
      await expect(rule).toContainText('An agent proposes a rule change for')
      await expect(rule.locator('.res-key')).toHaveText('AEON-1')
      const action = page.getByTestId('desk-row-m:action-1')
      await expect(action.locator('.what')).toHaveText('Action request')
      await expect(action).toContainText(world.action.body)
      await expect(page.getByTestId('desk-row-a:approval-1')).not.toContainText('n-a1')
      await expect(page.getByTestId('desk-list')).not.toContainText(/needs your steer|Human request/)
      await page.screenshot({ path: testInfo.outputPath(`fix4-list-${width}-${theme}.png`), fullPage: true })

      await page.getByTestId('desk-one-by-one').click()
      await expect(heading(page)).toHaveText('Allow nodes.read?')
      // What it runs shows only what runs; the ticket is the Re chip, risk, asker and expiry are in the head.
      await expect(page.getByTestId('desk-re')).toContainText('AEON-1')
      await expect(page.getByTestId('desk-line')).toContainText(/waits for this\. Expires in \d+ min · Low risk\./)
      await expect(page.getByTestId('desk-runs')).toContainText('What it runs')
      await expect(page.getByTestId('desk-runs')).not.toContainText(/Ticket n-a1|n-a1|\brisk\b|Asked by|expires/i)
      await page.screenshot({ path: testInfo.outputPath(`fix4-approval-${width}-${theme}.png`) })

      await page.keyboard.press('j')
      await expect(heading(page)).toHaveText('Which migration should carry the index?')
      // The chosen stamp takes its outcome's colour; the others stay ink-3.
      await expect(page.getByTestId('stamp-once')).toHaveAttribute('aria-checked', 'true')
      await expect(page.getByTestId('stamp-once')).toHaveClass(/\bt-ok\b/)
      await expect.poll(() => tone('stamp-once')).toBe(await token('--ok'))
      await expect.poll(() => tone('stamp-always')).toBe(await token('--ink-3'))
      await expect(page.getByTestId('desk-stamp-desc')).toContainText('it stays in Decided. Suggested: Doctrine · not available for this question')
      await page.screenshot({ path: testInfo.outputPath(`fix4-stamp-once-${width}-${theme}.png`) })
      await page.getByTestId('stamp-always').click()
      await expect(page.getByTestId('stamp-always')).toHaveClass(/\bt-teal\b/)
      await expect.poll(() => tone('stamp-always')).toBe(await token('--teal-ink'))
      await expect.poll(() => tone('stamp-once')).toBe(await token('--ink-3'))
      await page.getByTestId('stamp-requirement').click()
      await expect.poll(() => tone('stamp-requirement')).toBe(await token('--gold-ink'))
      await page.screenshot({ path: testInfo.outputPath(`fix4-stamp-requirement-${width}-${theme}.png`) })

      await page.keyboard.press('j'); await page.keyboard.press('j')
      await expect(page.getByTestId('desk-tab-kind')).toHaveText('Action request')
      await expect(heading(page)).toHaveText(world.action.body)
      await expect(page.getByRole('dialog')).not.toContainText(/needs your steer|Human request/)
    }
  })

  // Risk: on narrow desks a reserved native-action row leaves an empty band under Skip and Decide, the tab cuts
  // the kind, or Edit appearing for the next item moves the buttons under the pointer (AEON-541).
  await test.step('the narrow bar has no empty band, the tab reads the kind, Edit appears above a pinned bar', async () => {
    for (const [width, height] of [[390, 844], [688, 1000]] as const) for (const theme of ['light', 'dark'] as const) {
      await page.unrouteAll({ behavior: 'ignoreErrors' })
      await page.setViewportSize({ width, height })
      await mockDecisionDesk(page, { theme })
      await page.goto('/decision-desk')
      // CI's Linux sans (DejaVu Sans Bold) sets "Handover question" ~15 % wider than macOS does and cut it at 390
      // beside "PRJ-35" and "5 of 5". Verdana Bold is as wide, so every platform meets CI's metrics; key and pager
      // use the bundled JetBrains Mono everywhere.
      await page.addStyleTag({ content: '.folder-tab .ft-kind { font-family: Verdana, "DejaVu Sans", sans-serif !important; }' })
      await page.getByTestId('desk-one-by-one').click()
      await expect(heading(page)).toHaveText('Allow nodes.read?')
      expect(await page.getByTestId('desk-tab-kind').evaluate(element => getComputedStyle(element).fontFamily), 'the tab uses the wide font').toContain('Verdana')
      await expect(page.getByTestId('desk-native')).toBeHidden()
      const frame = (await page.getByTestId('desk-frame').boundingBox())!, decide = (await page.getByTestId('desk-decide').boundingBox())!
      expect(frame.y + frame.height - (decide.y + decide.height), 'Decide sits at the foot of the sheet').toBeLessThanOrEqual(16)
      await page.screenshot({ path: testInfo.outputPath(`fix4-bar-${width}-${theme}.png`) })
      const kinds: string[] = []
      const fits = async () => {
        const kind = page.getByTestId('desk-tab-kind')
        expect(await kind.evaluate(element => element.scrollWidth <= element.clientWidth + 0.5), `the tab reads "${await kind.innerText()}" in full`).toBe(true)
        kinds.push(await kind.innerText())
      }
      await fits()
      const next = (title: string) => ({ name: `next item: ${title}`, run: async () => { await page.keyboard.press('j'); await expect(heading(page)).toHaveText(title); await fits() } })
      await expectStableControls({
        controls: { decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), tab: page.getByTestId('desk-tab'), pager: page.getByTestId('desk-pager') },
        interactions: [next('Which migration should carry the index?'), next('Should the successor continue the review?'), next('The reviewer needs guidance before trying again.'), next('Keep mutation checks in the transaction')],
      })
      expect(kinds).toEqual(['Approval', 'Question', 'Handover question', 'Action request', 'Rule change'])
      // The rule offers Edit: a compact row above Skip and Decide, inside the sheet.
      const edit = (await page.getByTestId('desk-edit-rule').boundingBox())!, after = (await page.getByTestId('desk-decide').boundingBox())!
      await expect(page.getByTestId('desk-edit-rule')).toBeVisible()
      expect(edit.y + edit.height, 'Edit sits above Decide').toBeLessThanOrEqual(after.y)
      expect(edit.y, 'Edit stays inside the sheet').toBeGreaterThan(frame.y)
      await page.screenshot({ path: testInfo.outputPath(`fix4-bar-edit-${width}-${theme}.png`) })
    }
  })

  // Risk: the Decided stamp filter wraps "Approvals" to a second row on a phone and pushes the list.
  await test.step('the Decided stamp filter stays one line and scrolls sideways', async () => {
    for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
      await page.unrouteAll({ behavior: 'ignoreErrors' })
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      const world = await mockDecisionDesk(page, { theme })
      world.approval.decision = 'approved'
      await page.goto('/decision-desk')
      await page.getByTestId('desk-tab-decided').click()
      const filter = page.getByRole('radiogroup', { name: 'Stamp' })
      await expect(filter).toBeVisible()
      const line = () => filter.evaluate(element => ({ rows: new Set([...element.querySelectorAll('button')].map(button => Math.round(button.getBoundingClientRect().top))).size, height: element.getBoundingClientRect().height, overflow: element.scrollWidth - element.clientWidth, room: element.clientWidth }))
      const before = await line()
      expect(before.rows, 'one line of stamps').toBe(1)
      expect(before.height).toBeLessThan(50)
      if (width === 390) expect(before.overflow, 'the filter scrolls sideways at 390').toBeGreaterThan(0)
      const listTop = async () => (await page.getByTestId('desk-list').boundingBox())!.y
      const top = await listTop()
      await expectStableControls({
        controls: { open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided'), filter },
        interactions: [
          { name: 'choose Approvals', run: async () => { await filter.getByRole('radio', { name: 'Approvals' }).click(); await expect(filter.getByRole('radio', { name: 'Approvals' })).toHaveAttribute('aria-checked', 'true') } },
          { name: 'choose All', run: async () => { await filter.getByRole('radio', { name: 'All' }).click(); await expect(filter.getByRole('radio', { name: 'All' })).toHaveAttribute('aria-checked', 'true') } },
        ],
      })
      expect((await line()).rows).toBe(1)
      expect(Math.abs(await listTop() - top), 'the list starts where it did').toBeLessThanOrEqual(0.5)
      await page.screenshot({ path: testInfo.outputPath(`fix4-decided-filter-${width}-${theme}.png`) })
    }
  })
})

// The sheet outline: tab, shoulder and sheet are one path; the hairline keeps one weight (±0.25 px)
// through both shoulder junctions and the sheet corner, with no fill seam under the tab (Markus 10-14 09:47).
test.describe('sheet outline at device scale factor 2', () => {
  test.use({ deviceScaleFactor: 2 })
  test('one even hairline from tab to sheet', async ({ page }, testInfo) => {
    test.setTimeout(90_000)
    for (const theme of ['light', 'dark'] as const) {
      await page.unrouteAll({ behavior: 'ignoreErrors' })
    await page.setViewportSize({ width: 1440, height: 1000 })
    await oneQuestion(page, theme)
    await page.goto('/decision-desk')
    await page.getByTestId('desk-one-by-one').click()
    await expect(heading(page)).toBeVisible()
    const path = page.locator('.sheet-shape path')
    await expect(path).toHaveAttribute('d', /^M.+Z$/)
    expect((await path.getAttribute('d'))!.match(/A13\.5 13\.5/g)).toHaveLength(6)
    const geo = await page.evaluate(() => {
      const svg = document.querySelector('.sheet-shape')!.getBoundingClientRect(), tab = document.querySelector('[data-testid="desk-tab"]')!.getBoundingClientRect(), stack = document.querySelector('.stack')!.getBoundingClientRect()
      const snap = (v: number) => Math.round(v * devicePixelRatio) / devicePixelRatio
      return { svg: { x: svg.x, y: svg.y }, tabRight: snap(tab.right) - 0.5, tabTop: snap(tab.top) + 0.5, tabLeft: snap(tab.left), sheetTop: snap(stack.top) + 0.5, sheetRight: snap(stack.right) - 0.5, r: 13.5 }
    })
    const clip = { x: Math.floor(geo.tabRight - 40), y: Math.floor(geo.tabTop - 10), width: Math.ceil(geo.sheetRight - geo.tabRight + 60), height: Math.ceil(geo.sheetTop - geo.tabTop + 50) }
    const png = await page.screenshot({ clip })
    await testInfo.attach(`shoulder-${theme}`, { body: png, contentType: 'image/png' })
    await page.screenshot({ path: testInfo.outputPath(`shoulder-2x-${theme}.png`), clip: { x: geo.tabRight - 30, y: geo.sheetTop - 30, width: 60, height: 50 } })
    await page.screenshot({ path: testInfo.outputPath(`corner-2x-${theme}.png`), clip: { x: geo.sheetRight - 30, y: geo.sheetTop - 10, width: 50, height: 40 } })
    const result = await page.evaluate(async ({ b64, clip, geo }) => {
      const image = new Image(); image.src = `data:image/png;base64,${b64}`; await image.decode()
      const canvas = new OffscreenCanvas(image.width, image.height), ctx = canvas.getContext('2d')!
      ctx.drawImage(image, 0, 0)
      const { data, width } = ctx.getImageData(0, 0, image.width, image.height), scale = image.width / clip.width
      const lum = (x: number, y: number) => { const i = (Math.round(y) * width + Math.round(x)) * 4; return 0.2126 * data[i]! + 0.7152 * data[i + 1]! + 0.0722 * data[i + 2]! }
      // Bilinear luminance at a CSS-px point in viewport coordinates.
      const at = (px: number, py: number) => {
        const x = (px - clip.x) * scale - 0.5, y = (py - clip.y) * scale - 0.5, x0 = Math.floor(x), y0 = Math.floor(y), fx = x - x0, fy = y - y0
        return lum(x0, y0) * (1 - fx) * (1 - fy) + lum(x0 + 1, y0) * fx * (1 - fy) + lum(x0, y0 + 1) * (1 - fx) * fy + lum(x0 + 1, y0 + 1) * fx * fy
      }
      // The hairline's ink across the edge at a point, along its normal from the glass (t < 0) into the paper (t > 0).
      const ink = (x: number, y: number, nx: number, ny: number) => {
        const step = 0.125, samples: { t: number; v: number }[] = []
        for (let t = -3; t <= 3 + 1e-9; t += step) samples.push({ t, v: at(x + nx * t, y + ny * t) })
        const mean = (from: number, to: number) => { const rows = samples.filter(s => s.t >= from && s.t <= to); return rows.reduce((sum, s) => sum + s.v, 0) / rows.length }
        const glass = mean(-3, -2.25), paper = mean(2.25, 3)
        return samples.reduce((sum, s) => sum + Math.abs(s.v - (s.t < 0 ? glass : paper)) * step, 0)
      }
      const average = (points: [number, number, number, number][]) => points.reduce((sum, p) => sum + ink(...p), 0) / points.length
      const { tabRight: TR, sheetTop: S0, sheetRight: R, r } = geo
      // The straight tab edge: glass on the right, paper on the left.
      const midY = S0 - r - 3
      const straight = average([-1.2, 0, 1.2].map(d => [TR, midY + d, -1, 0]))
      const arcs: Record<string, number[]> = { shoulder: [], corner: [] }
      for (const share of [0, 0.15, 0.3, 0.5, 0.7, 0.85, 1]) {
        // The concave shoulder: centre in the glass, paper away from it.
        const a = Math.PI - share * Math.PI / 2, cx = TR + r, cy = S0 - r
        arcs.shoulder!.push(average([-1.2, 0, 1.2].map(d => { const b = a + d / r; return [cx + r * Math.cos(b), cy + r * Math.sin(b), Math.cos(b), Math.sin(b)] as [number, number, number, number] })))
        // The sheet's top-right corner: centre in the paper.
        const c = -Math.PI / 2 + share * Math.PI / 2, kx = R - r, ky = S0 + r
        arcs.corner!.push(average([-1.2, 0, 1.2].map(d => { const b = c + d / r; return [kx + r * Math.cos(b), ky + r * Math.sin(b), -Math.cos(b), -Math.sin(b)] as [number, number, number, number] })))
      }
      // No seam where the tab meets the sheet: a flat paper profile across the sheet top inside the tab.
      const seamX = TR - 60, seam: number[] = []
      for (let t = -3; t <= 3; t += 0.25) seam.push(at(seamX, S0 + t))
      return { straight, shoulder: arcs.shoulder!.map(v => v / straight), corner: arcs.corner!.map(v => v / straight), seam: Math.max(...seam) - Math.min(...seam) }
    }, { b64: png.toString('base64'), clip, geo })
    await writeFile(testInfo.outputPath(`stroke-2x-${theme}.md`), `# Sheet hairline at 2x, ${theme}\n\n\`\`\`json\n${JSON.stringify(result, null, 2)}\n\`\`\`\n`)
    expect(result.straight, 'the straight edge carries ink').toBeGreaterThan(1)
    for (const [name, weights] of [['shoulder', result.shoulder], ['corner', result.corner]] as const) for (const [at, weight] of weights.entries()) {
      expect(Math.abs(weight - 1), `${name} weight ${weight.toFixed(2)} px at sample ${at}`).toBeLessThanOrEqual(0.25)
    }
    expect(result.seam, 'no fill seam under the tab').toBeLessThanOrEqual(3)
    }
  })
})
