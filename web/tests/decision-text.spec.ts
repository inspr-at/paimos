// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page, type Locator } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { expectStableControls } from './helpers/stable'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { journeyWorld, mockJourney } from './journey-fixtures'

const title = 'Welche mandantenbezogene Datenbankmigration soll nach der vollständigen Sicherheitsprüfung und der Abstimmung mit allen Verantwortlichen den zusätzlichen Suchindex für die langfristige Nachvollziehbarkeit der Entscheidungen übernehmen?'
const choiceTitle = 'Den ausschließlich mandantenbezogenen Teilindex mit nachvollziehbarer Sicherheitsprüfung und dauerhaft dokumentierter Freigabe verwenden'
const description = Array(8).fill('Vor der Freigabe müssen sämtliche Berechtigungen innerhalb der abschließenden Transaktion erneut geprüft und die Auswirkungen auf alle betroffenen Datensätze vollständig dokumentiert werden.').join(' ')
const tail = 'Abschließende Bedingung: keine mandantenübergreifenden Schreibzugriffe.'

async function shot(page: Page, name: string) {
  const dir = join(process.cwd(), 'test-results/aeon-629-decision-text')
  await mkdir(dir, { recursive: true })
  await page.screenshot({ path: join(dir, `${name}.png`) })
}
const scrollKeys = ['Home', 'ArrowDown', 'PageDown', 'Space', 'End', 'ArrowUp', 'PageUp', 'Shift+Space', 'Home', 'ArrowLeft', 'ArrowRight']
async function nativeScrollKey(area: Locator, key: string) {
  await area.focus()
  await area.evaluate(element => {
    element.removeAttribute('data-scroll-key-prevented')
    document.addEventListener('keydown', event => {
      element.setAttribute('data-scroll-key-prevented', String(event.defaultPrevented))
    }, { once: true })
  })
  await area.press(key)
  await expect(area, `${key} retains native scrolling`).toHaveAttribute('data-scroll-key-prevented', 'false')
  if (key === 'Home') await expect.poll(() => area.evaluate(el => el.scrollTop)).toBe(0)
  if (key === 'ArrowDown' || key === 'End') await expect.poll(() => area.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
}
async function textFits(page: Page, selector: string) {
  await expect.poll(() => page.locator(selector).evaluate(el => el.scrollHeight - el.clientHeight)).toBeLessThanOrEqual(1)
}

for (const width of [390, 768, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`full Decision Desk context and stable series ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const world = await mockDecisionDesk(page, { long: true, theme })
    world.questions[0]!.input.question = title
    world.questions[0]!.input.options[0]!.title = choiceTitle
    world.questions[0]!.input.options[0]!.description = description + tail
    await page.goto('/decision-desk')
    await page.getByTestId('desk-row-q:question-1').click()
    await expect(page.getByTestId('desk-decide')).toBeEnabled()
    const heading = page.locator('.memo-heading h2'), summary = page.getByTestId('desk-answer-summary')
    await expect(heading).toHaveAttribute('data-tip', title)
    await expect(page.getByTestId('desk-full-title')).toHaveText('Question in full' + title)
    await page.getByTestId('desk-close').press('Tab')
    await expect(heading).toBeFocused()
    await expect(page.locator('.tooltip')).toHaveText(title)
    await page.getByTestId('desk-paper').focus()
    await expect(summary).toContainText(choiceTitle)
    await expect(summary).toContainText(description + tail)
    await expect.poll(() => summary.evaluate(el => el.scrollHeight > el.clientHeight)).toBe(true)
    await shot(page, `desk-${width}-${theme}`)
    const choices = page.getByTestId('desk-choices')
    await expectStableControls({
      controls: {
        actions: page.locator('.desk-toolbar .action-buttons'), decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), pager: page.getByTestId('desk-pager'),
        stamps: page.getByTestId('desk-stamps'), selectors: choices, first: page.getByTestId('choice-row-0'), second: page.getByTestId('choice-row-1'), custom: page.getByTestId('choice-row-2'), summary,
        frame: page.getByTestId('desk-frame'),
      },
      scrollAreas: { body: page.getByTestId('desk-body'), summary },
      interactions: [
        ...scrollKeys.map(key => ({ name: `read selected details with ${key}`, run: async () => {
          await nativeScrollKey(summary, key)
          await expect(page.getByTestId('choice-0')).toHaveAttribute('aria-checked', 'true')
          await expect(summary).toContainText(description + tail)
          expect(world.calls).toHaveLength(0)
        } })),
        { name: 'read the full selected option', run: async () => { await summary.focus(); await summary.press('End'); await expect.poll(() => summary.evaluate(el => el.scrollTop)).toBeGreaterThan(0) } },
        { name: 'select the second option', run: async () => { await page.getByTestId('choice-1').click(); await expect(summary).toContainText('Use a full index'); await expect(summary).not.toContainText(choiceTitle) } },
        { name: 'select and write a custom answer', run: async () => { await page.getByTestId('choice-2').click(); const field = page.getByRole('textbox', { name: 'Something else' }); await field.fill('Ein mandantenbezogener Schreibzugriff.'); await field.press('Escape'); await expect(summary).toContainText('Ein mandantenbezogener Schreibzugriff.') } },
        { name: 'select the first option again', run: async () => { await page.getByTestId('choice-0').click(); await expect(summary).toContainText(description + tail) } },
        { name: 'skip to a short title', run: async () => { await page.getByTestId('desk-skip').click(); await expect(page.getByTestId('desk-pager')).toContainText('2 of'); await expect(page.getByTestId('desk-full-title')).toHaveCount(0); await expect(heading).not.toHaveAttribute('data-tip') } },
        { name: 'return to the long title', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('desk-pager')).toContainText('1 of'); await expect(page.getByTestId('desk-full-title')).toContainText(title) } },
        { name: 'decide and advance with focus retained', run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1); expect(world.calls[0]!.body.option_id).toBe(world.questions[0]!.input.options[0]!.id); await expect(page.getByTestId('desk-pager')).toContainText('2 of'); await expect(page.getByTestId('desk-decide')).toBeFocused() } },
      ],
    })
  })

  test(`approval and held request reveals ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const work = fixtures(); work.preferences.theme = { choice: theme }
    await mockWork(page, work, { admin: true })
    const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
    const approval = data.approvals.find(a => a.scope === 'nodes.read' && !a.decision)!
    approval.rationale = description + tail
    const shortApproval = data.approvals.find(a => a.scope === 'run.claim' && !a.decision)!
    shortApproval.rationale = 'Kurz.'
    const held = data.messages.find(m => m.is_action_request)!
    held.body = description + tail
    await mockAgents(page, data)
    await page.goto('/agents')
    const card = page.locator(`[data-row="a:${approval.id}"]`), request = page.locator(`[data-row="m:${held.id}"]`)
    await expect(card.getByRole('button', { name: 'Show all', exact: true })).toBeVisible()
    await expectStableControls({ controls: { reveal: card.locator('.reveal-text'), approve: card.getByRole('button', { name: 'Approve', exact: true }), deny: card.getByRole('button', { name: 'Deny', exact: true }) }, interactions: [
      { name: 'reveal rationale', run: async () => { await card.getByRole('button', { name: 'Show all', exact: true }).click(); await textFits(page, `[data-row="a:${approval.id}"] .why .text`); await expect(card.locator('.why')).toContainText(tail) } },
      { name: 'collapse rationale', run: async () => { await card.getByRole('button', { name: 'Show less', exact: true }).click(); await expect(card.locator('.why .text')).not.toHaveClass(/full/) } },
    ] })
    await card.getByRole('button', { name: 'Approve', exact: true }).click()
    await expect(card.locator('.why .text')).toHaveClass(/full/)
    await textFits(page, `[data-row="a:${approval.id}"] .why .text`)
    await expectStableControls({ controls: { cancel: card.getByRole('button', { name: 'Cancel', exact: true }), approve: card.getByRole('button', { name: 'Approve permission', exact: true }) }, scrollAreas: { form: card.locator('form') }, interactions: [
      { name: 'type a reason below full context', run: async () => { await card.getByLabel('Reason (optional)').fill('Nach vollständiger Prüfung.') } },
    ] })
    await shot(page, `approval-${width}-${theme}`)
    await card.getByRole('button', { name: 'Cancel', exact: true }).click()
    const short = page.locator(`[data-row="a:${shortApproval.id}"]`)
    await expect(short.getByRole('button', { name: 'Show all', exact: true })).toHaveCount(0)
    await expectStableControls({ controls: { reveal: request.locator('.reveal-text'), answer: request.getByRole('button', { name: 'Answer', exact: true }), resolve: request.getByRole('button', { name: 'Resolve', exact: true }), dismiss: request.getByRole('button', { name: 'Dismiss', exact: true }) }, interactions: [
      { name: 'reveal held request', run: async () => { await request.getByRole('button', { name: 'Show all', exact: true }).click(); await textFits(page, `[data-row="m:${held.id}"] .why .text`) } },
      { name: 'collapse held request', run: async () => { await request.getByRole('button', { name: 'Show less', exact: true }).click(); await expect(request.locator('.why .text')).not.toHaveClass(/full/) } },
    ] })
    await request.getByRole('button', { name: 'Resolve', exact: true }).click()
    await expect(request.locator('.why .text')).toHaveClass(/full/)
    await textFits(page, `[data-row="m:${held.id}"] .why .text`)
    await shot(page, `held-${width}-${theme}`)
    expect(data.controls).toHaveLength(0)
  })

  test(`release description reveal and reset ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const work = fixtures(); work.preferences.theme = { choice: theme }
    work.nodes.find(n => n.id === 'n-1')!.body = '# Sicherheitsprüfung\n\n' + description + '\n\n' + tail
    work.nodes.find(n => n.id === 'n-2')!.body = 'Kurze Beschreibung.'
    await mockWork(page, work); const journey = journeyWorld('plan'), calls = await mockJourney(page, journey)
    await page.goto('/p/PHAROS?view=journey&walk=PHAROS-11')
    const walker = page.getByRole('dialog', { name: 'Release walker', exact: true })
    await expect(walker).toBeVisible()
    if (width < 901) await walker.press('i')
    const toggle = walker.locator('.description-toggle'), text = walker.locator('.description')
    await expect(toggle).toHaveText('Show full description')
    const screens = walker.getByRole('group', { name: 'Screens' })
    const firstScreen = screens.getByRole('button', { name: 'Before: card grid', exact: true })
    await expect(firstScreen).toHaveAttribute('aria-pressed', 'true')
    await expect(walker.locator('img.shot')).toHaveAttribute('alt', 'Before: card grid')
    await expect(text).not.toHaveAttribute('data-tip')
    await expectStableControls({ controls: { reveal: toggle, screens }, scrollAreas: { description: text }, interactions: [
      { name: 'show full description', run: async () => { await toggle.click(); await expect(toggle).toHaveAttribute('aria-expanded', 'true'); await expect(text).toHaveCSS('mask-image', 'none'); await text.focus(); await text.press('End'); await expect.poll(() => text.evaluate(el => el.scrollTop)).toBeGreaterThan(0); await expect(text).toContainText(tail) } },
      ...scrollKeys.map(key => ({ name: `read release description with ${key}`, run: async () => {
        await nativeScrollKey(text, key)
        await expect(firstScreen).toHaveAttribute('aria-pressed', 'true')
        await expect(walker.locator('img.shot')).toHaveAttribute('alt', 'Before: card grid')
        expect(journey.walkers['r-2']!.tickets.find(ticket => ticket.ticket_node_id === 'n-1')!.included).toBe(true)
        expect(calls.filter(call => call.method !== 'GET' && call.path.endsWith('/plan'))).toHaveLength(0)
        await expect(page).toHaveURL(/walk=PHAROS-11/)
      } })),
    ] })
    await shot(page, `walker-${width}-${theme}`)
    await walker.press('ArrowRight')
    await expect(walker.locator('.info-title')).toHaveText('Add an Oracle Cloud connector')
    await expect(toggle).toHaveCount(0)
    await expect(text).toContainText('Kurze Beschreibung.')
    await expect(text).not.toHaveClass(/revealed/)
    await walker.press('ArrowLeft')
    await expect(toggle).toHaveText('Show full description')
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  })
}
