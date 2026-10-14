// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page, type Locator } from '@playwright/test'
import { expectStableControls } from './helpers/stable'
import { mockDecisionDesk, refreshDesk } from './decision-desk-fixtures'

const title = 'Welche mandantenbezogene Datenbankmigration soll nach der vollständigen Sicherheitsprüfung und der Abstimmung mit allen Verantwortlichen den zusätzlichen Suchindex für die langfristige Nachvollziehbarkeit der Entscheidungen übernehmen?'
const choiceTitle = 'Den ausschließlich mandantenbezogenen Teilindex mit nachvollziehbarer Sicherheitsprüfung und dauerhaft dokumentierter Freigabe verwenden'
const description = Array(8).fill('Vor der Freigabe müssen sämtliche Berechtigungen innerhalb der abschließenden Transaktion erneut geprüft und die Auswirkungen auf alle betroffenen Datensätze vollständig dokumentiert werden.').join(' ')
const tail = 'Abschließende Bedingung: keine mandantenübergreifenden Schreibzugriffe.'

async function shot(page: Page, name: string) {
  await page.screenshot({ path: test.info().outputPath(`${name.replace(/[^a-z0-9_-]/gi, '-')}.png`) })
}
// AEON-1339: the sheet is the one scroll container; arrows choose and move, the reading keys stay native.
const scrollKeys = ['Home', 'PageDown', 'Space', 'End', 'PageUp', 'Shift+Space', 'Home']
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
  if (key === 'End') await expect.poll(() => area.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
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
  const overflow = await control.evaluate(async el => {
    await document.fonts.ready
    const button = el.getBoundingClientRect(), walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
    const overflow: { text: string | null; parent: string; clip: string; clipPath: string; button: DOMRect; textBox: DOMRect }[] = []
    let node: Node | null
    while ((node = walker.nextNode())) {
      if (getComputedStyle(node.parentElement!).visibility === 'hidden') continue
      // AEON-569, merge #458: KeyCap exposes a clipped 1px screen-reader
      // label. Range geometry includes that unpainted text; exclude it only
      // when the actual clipping, size and positioning prove it is hidden.
      const parent = node.parentElement!, style = getComputedStyle(parent), parentBox = parent.getBoundingClientRect()
      if (style.position === 'absolute' && style.overflow === 'hidden' && style.clipPath === 'inset(50%)' && parentBox.width <= 1 && parentBox.height <= 1) continue
      const range = document.createRange(); range.selectNodeContents(node)
      for (const rect of range.getClientRects()) {
        if (rect.left < button.left - .5 || rect.right > button.right + .5 || rect.top < button.top - .5 || rect.bottom > button.bottom + .5) {
          const style = getComputedStyle(node.parentElement!)
          overflow.push({ text: node.textContent, parent: node.parentElement!.outerHTML, clip: style.clip, clipPath: style.clipPath, button: button.toJSON(), textBox: rect.toJSON() })
        }
      }
    }
    for (const glyph of el.querySelectorAll('svg')) {
      const style = getComputedStyle(glyph)
      if (style.visibility === 'hidden' || glyph.getClientRects().length === 0) continue
      const rect = glyph.getBoundingClientRect()
      if (rect.left < button.left - .5 || rect.right > button.right + .5 || rect.top < button.top - .5 || rect.bottom > button.bottom + .5) overflow.push({ text: 'SVG glyph', parent: glyph.outerHTML, clip: style.clip, clipPath: style.clipPath, button: button.toJSON(), textBox: rect.toJSON() })
    }
    return overflow
  })
  expect(overflow, 'visible reveal labels fit without clipping or overflowing').toEqual([])
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
        const decide = page.getByTestId('desk-decide'), skip = page.getByTestId('desk-skip'), close = page.getByTestId('desk-close')
        await revealGeometry(decide, skip.or(close), device.coarse)
        await revealGeometry(skip, decide.or(close), device.coarse)
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
    const heading = page.locator('.memo-heading h2'), paper = page.getByTestId('desk-paper')
    // The approved artifact: the title wraps in full (no clamp, no tooltip, no "Question in full"),
    // and the option's description wraps in its row, with no second copy of it.
    await expect(heading).toHaveText(title)
    await expect(heading).not.toHaveAttribute('data-tip')
    await expect(page.getByTestId('desk-full-title')).toHaveCount(0)
    await expect(page.getByTestId('desk-answer-summary')).toHaveCount(0)
    await expect(page.getByTestId('choice-row-0')).toContainText(choiceTitle)
    await expect(page.getByTestId('choice-row-0')).toContainText(description + tail)
    expect(await page.getByTestId('choice-row-0').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
    await paper.focus()
    await shot(page, `desk-${width}-${theme}`)
    const choices = page.getByTestId('desk-choices')
    const bar = { actions: page.locator('.desk-toolbar .action-buttons'), decide: page.getByTestId('desk-decide'), skip: page.getByTestId('desk-skip'), close: page.getByTestId('desk-close'), pager: page.getByTestId('desk-pager'), frame: page.getByTestId('desk-frame') }
    await expectStableControls({
      controls: { ...bar, stamps: page.getByTestId('desk-stamps'), selectors: choices, first: page.getByTestId('choice-row-0'), second: page.getByTestId('choice-row-1'), custom: page.getByTestId('choice-row-2') },
      scrollAreas: { body: page.getByTestId('desk-body'), paper },
      interactions: [
        ...scrollKeys.map(key => ({ name: `read the sheet with ${key}`, run: async () => {
          await nativeScrollKey(paper, key)
          await expect(page.getByTestId('choice-0')).toHaveAttribute('aria-checked', 'true')
          expect(world.calls).toHaveLength(0)
        } })),
        { name: 'select the second option', run: async () => { await page.getByTestId('choice-1').click(); await expect(page.getByTestId('choice-1')).toHaveAttribute('aria-checked', 'true'); await expect(page.getByTestId('choice-row-0')).toContainText(description + tail) } },
        { name: 'select and write a custom answer', run: async () => { await page.getByTestId('choice-2').click(); const field = page.getByRole('textbox', { name: 'Something else' }); await field.fill('Ein mandantenbezogener Schreibzugriff.'); await field.press('Escape'); await expect(field).toHaveValue('Ein mandantenbezogener Schreibzugriff.') } },
        { name: 'select the first option again', run: async () => { await page.getByTestId('choice-0').click(); await expect(page.getByTestId('choice-0')).toHaveAttribute('aria-checked', 'true') } },
      ],
    })
    await expectStableControls({ controls: bar, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
      { name: 'skip to a short title', run: async () => { await page.getByTestId('desk-skip').click(); await expect(page.getByTestId('desk-pager')).toContainText('3 of'); await expect(heading).not.toHaveText(title) } },
      { name: 'return to the long title', run: async () => { await paper.press('ArrowLeft'); await expect(page.getByTestId('desk-pager')).toContainText('2 of'); await expect(heading).toHaveText(title) } },
      { name: 'decide and advance with focus retained', run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => world.calls.length).toBe(1); expect(world.calls[0]!.body.option_id).toBe(world.questions[0]!.input.options[0]!.id); await expect(page.getByTestId('desk-pager')).toContainText('3 of'); await expect(page.getByTestId('desk-decide')).toBeFocused() } },
    ] })
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
    await refreshDesk(page, world)
    await page.getByTestId('desk-row-a:approval-1').click()
    await expect(page.locator('.context-column').getByText('Kurz.', { exact: true })).toBeVisible()
    await expect(page.locator('.context-column')).not.toContainText(tail)
    expect(world.calls).toHaveLength(0)
  })
}
