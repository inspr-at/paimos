// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page, type Locator } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { expectStableControls } from './helpers/stable'
import { mockDecisionDesk } from './decision-desk-fixtures'

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
// Include the shared pseudo target when checking adjacent coarse-pointer actions.
async function hitBox(control: Locator) {
  return control.evaluate(el => {
    const rect = el.getBoundingClientRect(), pseudo = getComputedStyle(el, '::before')
    const width = pseudo.content !== 'none' ? Math.max(rect.width, parseFloat(pseudo.width) || 0) : rect.width
    const height = pseudo.content !== 'none' ? Math.max(rect.height, parseFloat(pseudo.height) || 0) : rect.height
    return { left: rect.x + (rect.width - width) / 2, top: rect.y + (rect.height - height) / 2, width, height }
  })
}
async function revealGeometry(control: Locator, neighbors: Locator, coarse: boolean) {
  const box = await hitBox(control)
  if (coarse) { expect(box.width).toBeGreaterThanOrEqual(44); expect(box.height).toBeGreaterThanOrEqual(44) }
  for (const neighbor of await neighbors.all()) {
    const other = await hitBox(neighbor)
    const overlapX = Math.min(box.left + box.width, other.left + other.width) - Math.max(box.left, other.left)
    const overlapY = Math.min(box.top + box.height, other.top + other.height) - Math.max(box.top, other.top)
    expect(overlapX > 0 && overlapY > 0, 'reveal target must not overlap neighboring actions or rows').toBe(false)
  }
  // Check visible glyph bounds, including wrapped translations.
  const fits = await control.evaluate(async el => {
    await document.fonts.ready
    const button = el.getBoundingClientRect(), walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
    let node: Node | null
    while ((node = walker.nextNode())) {
      if (getComputedStyle(node.parentElement!).visibility === 'hidden') continue
      const range = document.createRange(); range.selectNodeContents(node)
      for (const rect of range.getClientRects()) {
        if (rect.left < button.left - .5 || rect.right > button.right + .5 || rect.top < button.top - .5 || rect.bottom > button.bottom + .5) return false
      }
    }
    return true
  })
  expect(fits, 'visible reveal labels fit without clipping or overflowing').toBe(true)
}
for (const device of [{ width: 390, coarse: true }, { width: 1024, coarse: true }, { width: 1440, coarse: false }]) for (const theme of ['light', 'dark'] as const) {
  test.describe(`reveal sizing ${device.width} ${device.coarse ? 'coarse' : 'fine'} ${theme}`, () => {
    test.use({ hasTouch: device.coarse })
    test('approval and held controls fit both labels with disjoint stable targets', async ({ page }) => {
      await page.setViewportSize({ width: device.width, height: 1000 })
      expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(device.coarse)
      const world = await mockDecisionDesk(page, { theme })
      world.approval.rationale = description + tail
      world.action.body = description + tail
      await page.goto('/decision-desk')
      for (const id of ['a:approval-1', 'm:action-1']) {
        await page.getByTestId(`desk-row-${id}`).click()
        const context = page.locator('.context-column').getByText(description + tail, { exact: true })
        // The canonical memo exposes complete source text immediately, without
        // the retired inline Show all / Show less control.
        await expect(context).toBeVisible()
        expect(await context.evaluate(el => el.scrollHeight - el.clientHeight)).toBeLessThanOrEqual(1)
        const first = page.getByTestId('choice-0'), second = page.getByTestId('choice-1')
        await revealGeometry(first, second, device.coarse)
        await revealGeometry(second, first, device.coarse)
        await expectStableControls({
          controls: { first, second, choices: page.getByTestId('desk-choices'), actions: page.getByTestId('desk-actions').locator('.action-buttons'), decide: page.getByTestId('desk-decide'), close: page.getByTestId('desk-close') },
          scrollAreas: { body: page.getByTestId('desk-body') },
          interactions: [
            { name: 'choose the first response', run: async () => { await first.click(); await expect(first).toHaveAttribute('aria-checked', 'true'); await expect(context).toContainText(tail) } },
            { name: 'choose the second response', run: async () => { await second.click(); await expect(second).toHaveAttribute('aria-checked', 'true'); await expect(context).toContainText(tail) } },
          ],
        })
        await shot(page, `context-${id}-${device.width}-${theme}`)
        await page.getByTestId('desk-close').click()
      }
      expect(world.calls).toHaveLength(0)
    })
  })
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
    await heading.focus()
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
        { name: 'skip to a short title', run: async () => { await page.getByTestId('desk-skip').click(); await expect(page.getByTestId('desk-pager')).toContainText('3 of'); await expect(page.getByTestId('desk-full-title')).toHaveCount(0); await expect(heading).not.toHaveAttribute('data-tip') } },
        { name: 'return to the long title', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('desk-pager')).toContainText('2 of'); await expect(page.getByTestId('desk-full-title')).toContainText(title) } },
        { name: 'decide and advance with focus retained', run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1); expect(world.calls[0]!.body.option_id).toBe(world.questions[0]!.input.options[0]!.id); await expect(page.getByTestId('desk-pager')).toContainText('3 of'); await expect(page.getByTestId('desk-decide')).toBeFocused() } },
      ],
    })
  })

  test(`approval and held request reveals ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const world = await mockDecisionDesk(page, { theme })
    world.approval.rationale = description + tail
    world.action.body = description + tail
    await page.goto('/decision-desk')
    const body = page.getByTestId('desk-body')
    const controls = () => ({ decide: page.getByTestId('desk-decide'), close: page.getByTestId('desk-close'), choices: page.getByTestId('desk-choices'), first: page.getByTestId('choice-row-0'), second: page.getByTestId('choice-row-1') })
    for (const id of ['a:approval-1', 'm:action-1']) {
      await page.getByTestId(`desk-row-${id}`).click()
      const context = page.locator('.context-column').getByText(description + tail, { exact: true })
      await expect(context).toBeVisible()
      expect(await context.evaluate(el => el.scrollHeight - el.clientHeight)).toBeLessThanOrEqual(1)
      await expectStableControls({ controls: controls(), scrollAreas: { body }, interactions: [
        { name: 'select the alternate response beside full context', run: async () => { await page.getByTestId('choice-1').click(); await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true'); await expect(context).toContainText(tail) } },
        { name: 'return to the first response beside full context', run: async () => { await page.getByTestId('choice-0').click(); await expect(page.getByTestId('choice-0')).toHaveAttribute('aria-checked', 'true'); await expect(context).toContainText(tail) } },
        { name: 'type a note without moving the choices', run: async () => { await page.getByRole('textbox', { name: id.startsWith('a:') ? 'Reason' : 'P.S. for the agent', exact: true }).fill('Nach vollständiger Prüfung.'); await expect(context).toContainText(tail) } },
      ] })
      await shot(page, `full-context-${id}-${width}-${theme}`)
      await page.getByTestId('desk-close').click()
    }
    world.approval.rationale = 'Kurz.'
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    await expect(page.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled()
    await page.getByTestId('desk-row-a:approval-1').click()
    await expect(page.locator('.context-column').getByText('Kurz.', { exact: true })).toBeVisible()
    await expect(page.locator('.context-column')).not.toContainText(tail)
    expect(world.calls).toHaveLength(0)
  })
}
