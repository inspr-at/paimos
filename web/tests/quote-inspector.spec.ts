// SPDX-License-Identifier: AGPL-3.0-only
// The quote editor's format inspector (P4): scopes and headers, bold and italic on
// the kept selection, lists and numbering, millimetre fields, section actions on
// the page and in the outline, real section and document settings, the title bar
// (zoom, header chevron, save, PDF, one panel toggle) and the dock, overlay and
// sheet layouts, with axe in light and dark.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { expectStableControls } from './helpers/stable'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockQuoteEditor, NODES, QUOTE_ID, quoteDocument, type QuoteDoc } from './quote-inspector-fixtures'

async function open(page: Page, doc?: QuoteDoc) {
  await mockWork(page, fixtures())
  const mock = await mockQuoteEditor(page, doc)
  await page.goto(`/business/quotes/${QUOTE_ID}`)
  await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
  return mock
}
const inspector = (page: Page) => page.getByRole('complementary', { name: 'Format' })
const scope = (page: Page) => inspector(page).locator('.scope')
const text = (page: Page, node: string) => page.locator(`.quote-page [data-text-id="${node}"]`)
// A caret or selection in a paragraph of the page, as a person would leave it.
async function selectText(page: Page, node: string, from: number, to = from) {
  await text(page, node).scrollIntoViewIfNeeded()
  await text(page, node).click()
  await page.evaluate(({ node, from, to }) => {
    const el = document.querySelector(`.quote-page [data-text-id="${node}"]`)!
    const at = (offset: number) => { let rest = offset; const w = document.createTreeWalker(el, NodeFilter.SHOW_TEXT); let t: Node | null; while ((t = w.nextNode())) { const l = t.textContent!.length; if (rest <= l) return [t, rest] as const; rest -= l } return [el, 0] as const }
    const [a, ao] = at(from), [b, bo] = at(to)
    getSelection()!.setBaseAndExtent(a, ao, b, bo)
  }, { node, from, to })
  // A key press lets the editor read the selection, as it does for arrows and clicks.
  await page.keyboard.press('Shift+ArrowRight'); await page.keyboard.press('Shift+ArrowLeft')
}
const headings = (page: Page) => page.locator('.quote-page .quote-section-heading h2')
async function axe(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').analyze()
  const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')} — ${n.failureSummary?.split('\n').slice(1, 2).join(' ').trim()}`).join('\n')}`)
  expect(summary, summary.join('\n')).toEqual([])
}

test('the inspector follows the selection: one tab style and a scope header that matches the tab', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page)
  const tabs = inspector(page).getByRole('tab')
  await expect(tabs).toHaveText(['Text', 'Section', 'Document'])
  await expect(inspector(page).getByRole('tab', { selected: true })).toHaveText('Document')
  await expect(scope(page)).toContainText('A260924-1')
  await expect(scope(page)).toContainText('Relaunch des Kundenportals')

  await selectText(page, NODES[4]!, 3)
  await expect(inspector(page).getByRole('tab', { selected: true })).toHaveText('Text')
  await expect(scope(page)).toHaveText(/Section 2 of 4\s*Leistungsgegenstand und Vorgehen\s*List item, level 2 · caret/)

  await page.getByRole('textbox', { name: 'Heading section 3' }).click()
  await expect(inspector(page).getByRole('tab', { selected: true })).toHaveText('Section')
  await expect(scope(page)).toHaveText(/Section 3 of 4\s*Termine\s*2 list items/)

  // A tab you choose stays; its header always says what it is about.
  await inspector(page).getByRole('tab', { name: 'Document' }).click()
  await selectText(page, NODES[0]!, 2)
  await expect(inspector(page).getByRole('tab', { selected: true })).toHaveText('Document')
  await expect(scope(page)).toContainText('Document')
  await expect(scope(page)).not.toContainText('Section')
  // Every tab shares one active style.
  const styles = []
  for (const name of ['Text', 'Section', 'Document']) {
    await inspector(page).getByRole('tab', { name }).click()
    styles.push(await inspector(page).getByRole('tab', { name }).evaluate(el => { const c = getComputedStyle(el); return `${c.backgroundImage}|${c.color}|${c.boxShadow}` }))
  }
  expect(new Set(styles).size).toBe(1)
  expect(errors).toEqual([])
})

test('bold and italic toggle on the selection the page keeps; Cmd or Ctrl+B and I work from the panel too', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page)
  await selectText(page, NODES[2]!, 4, 9)
  const bold = inspector(page).getByRole('button', { name: 'Bold' })
  const italic = inspector(page).getByRole('button', { name: 'Italic' })
  await expect(bold).toHaveAttribute('aria-pressed', 'false')
  await bold.click()
  await expect(bold).toHaveAttribute('aria-pressed', 'true')
  await expect(text(page, NODES[2]!).locator('strong')).toHaveText('gehen')
  // The selection is still there, and still in the text.
  expect(await page.evaluate(() => getSelection()!.toString())).toBe('gehen')
  await expect(page.locator('.quote-page .quote-prose').nth(1)).toBeFocused()
  await page.keyboard.press('ControlOrMeta+i')
  await expect(italic).toHaveAttribute('aria-pressed', 'true')
  await expect(text(page, NODES[2]!).locator('strong em')).toHaveText('gehen')
  await inspector(page).getByRole('button', { name: 'Clear formatting' }).click()
  await expect(bold).toHaveAttribute('aria-pressed', 'false')
  await expect(text(page, NODES[2]!).locator('strong, em')).toHaveCount(0)
  // Undo is global: the title bar steps back through the same history.
  await page.getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(text(page, NODES[2]!).locator('strong em')).toHaveText('gehen')
  // A selection across two paragraphs with different styles reads as mixed.
  await selectText(page, NODES[2]!, 0, 9)
  await expect(bold).toHaveAttribute('aria-pressed', 'mixed')
})

test('formatting a range across paragraphs preserves both runs and undo', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page)
  await text(page, NODES[0]!).click()
  await page.evaluate(([first, second]) => {
    const node = (id: string) => {
      const walker = document.createTreeWalker(document.querySelector(`.quote-page [data-text-id="${id}"]`)!, NodeFilter.SHOW_TEXT)
      let current: Node | null
      while ((current = walker.nextNode())) if ((current.textContent?.length ?? 0) > 5) return current
      throw new Error('Text node missing')
    }
    const start = node(first), end = node(second)
    getSelection()!.setBaseAndExtent(start, 4, end, 5)
  }, [NODES[0]!, NODES[1]!])
  await page.keyboard.press('Shift+ArrowRight'); await page.keyboard.press('Shift+ArrowLeft')
  await inspector(page).getByRole('button', { name: 'Bold' }).click()
  await expect(text(page, NODES[0]!).locator('strong')).toContainText('bestehende')
  await expect(text(page, NODES[1]!).locator('strong')).toContainText('Ziel')
  await page.getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(text(page, NODES[0]!).locator('strong')).toHaveCount(0)
  await expect(text(page, NODES[1]!).locator('strong')).toHaveCount(0)
})

test('the list type is one segmented control; numbering options show only for numbers, with a labelled preview', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page)
  await selectText(page, NODES[3]!, 2)
  const list = inspector(page).getByRole('radiogroup', { name: 'List type' })
  await expect(list.getByRole('radio', { checked: true })).toHaveText('Numbers')
  const preview = inspector(page).locator('.preview')
  await expect(preview).toHaveText(/This item reads\s*2\.1/)
  await inspector(page).getByRole('radiogroup', { name: 'Numbers count' }).getByRole('radio', { name: 'Own count' }).click()
  await expect(preview).toHaveText(/This item reads\s*1$/)
  // How the list counts is one choice, and the current one shows.
  const counting = inspector(page).getByRole('radiogroup', { name: 'Counting' })
  await expect(counting.getByRole('radio', { checked: true })).toHaveText('Automatic')
  await expect(inspector(page).getByLabel('Start numbering at')).toHaveCount(0)
  await counting.getByRole('radio', { name: 'Start at' }).click()
  await expect(counting.getByRole('radio', { checked: true })).toHaveText('Start at')
  const start = inspector(page).getByLabel('Start numbering at')
  await expect(start).toHaveValue('1')
  await start.fill('4'); await start.press('Enter')
  await expect(preview).toHaveText(/This item reads\s*4$/)
  await expect(page.locator(`.quote-page [data-node-id="${NODES[3]}"] .quote-marker`)).toHaveText('4')
  await counting.getByRole('radio', { name: 'Automatic' }).click()
  await expect(preview).toHaveText(/This item reads\s*1$/)
  await counting.getByRole('radio', { name: 'Continue' }).click()
  await expect(counting.getByRole('radio', { checked: true })).toHaveText('Continue')
  // Bullets: numbering steps aside, the bullet glyphs come in.
  await list.getByRole('radio', { name: 'Bullets' }).click()
  await expect(inspector(page).getByRole('heading', { name: 'Numbering' })).toHaveCount(0)
  const bullets = inspector(page).getByRole('radiogroup', { name: 'Bullet' })
  await expect(bullets.getByRole('radio', { checked: true })).toHaveAccessibleName('Dot')
  await bullets.getByRole('radio', { name: 'Dash' }).click()
  await expect(page.locator(`.quote-page [data-node-id="${NODES[3]}"] .quote-marker`)).toHaveText('–')
  // Arrow keys move through the segments like radios.
  await list.getByRole('radio', { name: 'Bullets' }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(list.getByRole('radio', { checked: true })).toHaveText('Numbers')
  // Level: indent and outdent.
  await selectText(page, NODES[3]!, 2)
  await inspector(page).getByRole('button', { name: 'Indent' }).click()
  await expect(inspector(page).locator('.level-text')).toHaveText('Level 2 of 6')
  await inspector(page).getByRole('button', { name: 'Outdent' }).click()
  await expect(inspector(page).locator('.level-text')).toHaveText('Level 1 of 6')
  // A plain paragraph: no list, nothing to outdent.
  await selectText(page, NODES[2]!, 1)
  await expect(list.getByRole('radio', { checked: true })).toHaveText('None')
  await expect(inspector(page).getByRole('button', { name: 'Outdent' })).toBeDisabled()
})

test('millimetre fields: the unit inside, arrows 0.5, Shift 5, drag to scrub, one column, reset only when moved', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page)
  await selectText(page, NODES[7]!, 1)
  const across = inspector(page).getByRole('spinbutton', { name: 'Marker across' })
  const reset = inspector(page).getByRole('button', { name: 'Reset' })
  await expect(across).toHaveValue('0')
  await expect(reset).toHaveCount(0)
  // The unit sits inside the field, and the three fields share one column.
  const boxes = await inspector(page).locator('.mm-input').evaluateAll(els => els.map(el => { const r = el.getBoundingClientRect(), u = el.querySelector('.mm-unit')!.getBoundingClientRect(); return { left: Math.round(r.left), right: Math.round(r.right), inside: u.left > r.left && u.right < r.right } }))
  expect(new Set(boxes.map(b => `${b.left}-${b.right}`)).size).toBe(1)
  expect(boxes.every(b => b.inside)).toBe(true)
  await across.focus()
  await page.keyboard.press('ArrowUp')
  await expect(across).toHaveValue('0.5')
  await page.keyboard.press('Shift+ArrowUp')
  await expect(across).toHaveValue('5.5')
  await expect(page.locator(`.quote-page [data-node-id="${NODES[7]}"] .quote-marker`)).toHaveCSS('transform', /matrix/)
  await expect(reset).toBeVisible()
  await reset.click()
  await expect(across).toHaveValue('0')
  await expect(reset).toHaveCount(0)
  // Scrubbing: drag the label sideways; 4px per half millimetre.
  const label = inspector(page).locator('label', { hasText: 'Text starts' })
  const box = (await label.boundingBox())!
  await page.mouse.move(box.x + 10, box.y + box.height / 2)
  await page.mouse.down()
  await page.mouse.move(box.x + 30, box.y + box.height / 2, { steps: 4 })
  await page.mouse.move(box.x + 50, box.y + box.height / 2, { steps: 4 })
  await page.mouse.up()
  await expect(inspector(page).getByRole('spinbutton', { name: 'Text starts' })).toHaveValue('5')
  // Out of range: said plainly, and Escape brings the value back.
  const down = inspector(page).getByRole('spinbutton', { name: 'Marker down' })
  await down.fill('99')
  await expect(down).toHaveAttribute('aria-invalid', 'true')
  await expect(inspector(page).getByText('Use −20 to 20 mm.')).toBeVisible()
  await down.press('Escape')
  await expect(down).toHaveValue('0')
})

test('section actions live on the section: handle, context menu, Alt+Up/Down, add below, delete with Undo', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page)
  await expect(headings(page)).toHaveText(['Ausgangslage', 'Leistungsgegenstand und Vorgehen', 'Termine', 'Mitwirkung des Auftraggebers'])
  await page.locator('.quote-page [data-section-id]').nth(1).hover()
  await page.getByRole('button', { name: 'Section 2 actions' }).click()
  const menu = page.getByRole('menu', { name: 'Actions for section 2' })
  await expect(menu.getByRole('menuitem')).toHaveText([/^Add section below$/, /^Move up/, /^Move down/, /^Delete section$/])
  await menu.getByRole('menuitem', { name: 'Add section below' }).click()
  await expect(headings(page)).toHaveCount(5)
  await expect(headings(page).nth(2)).toHaveText('')
  await page.getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(headings(page)).toHaveCount(4)
  // Right click on a heading opens the same actions.
  await page.getByRole('textbox', { name: 'Heading section 3' }).click({ button: 'right' })
  await page.getByRole('menu', { name: 'Actions for section 3' }).getByRole('menuitem', { name: /Move up/ }).click()
  await expect(headings(page)).toHaveText(['Ausgangslage', 'Termine', 'Leistungsgegenstand und Vorgehen', 'Mitwirkung des Auftraggebers'])
  // Alt+Down from the heading moves it back, and focus stays with it.
  await page.getByRole('textbox', { name: 'Heading section 2' }).click()
  await page.keyboard.press('Alt+ArrowDown')
  await expect(headings(page)).toHaveText(['Ausgangslage', 'Leistungsgegenstand und Vorgehen', 'Termine', 'Mitwirkung des Auftraggebers'])
  await expect(page.getByRole('textbox', { name: 'Heading section 3' })).toBeFocused()
  // Delete asks nothing; the toast offers Undo.
  await page.getByRole('button', { name: 'Section 4 actions' }).click()
  await page.getByRole('menuitem', { name: 'Delete section' }).click()
  await expect(headings(page)).toHaveCount(3)
  await expect(page.getByText('Deleted section 4, “Mitwirkung des Auftraggebers”.')).toBeVisible()
  await page.getByRole('button', { name: 'Undo', exact: true }).last().click()
  await expect(headings(page)).toHaveCount(4)
})

test('the outline in the Section tab reorders by drag and by Alt+Up/Down', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page)
  await inspector(page).getByRole('tab', { name: 'Section' }).click()
  const rows = inspector(page).locator('.outline-row .row-title')
  await expect(rows).toHaveText(['Ausgangslage', 'Leistungsgegenstand und Vorgehen', 'Termine', 'Mitwirkung des Auftraggebers'])
  await inspector(page).locator('.outline-row').nth(0).locator('.grip').dragTo(inspector(page).locator('.outline-row').nth(2), { targetPosition: { x: 40, y: 30 } })
  await expect(rows).toHaveText(['Leistungsgegenstand und Vorgehen', 'Termine', 'Ausgangslage', 'Mitwirkung des Auftraggebers'])
  await expect(headings(page)).toHaveText(['Leistungsgegenstand und Vorgehen', 'Termine', 'Ausgangslage', 'Mitwirkung des Auftraggebers'])
  await inspector(page).getByRole('button', { name: /Ausgangslage/ }).focus()
  await page.keyboard.press('Alt+ArrowUp'); await page.keyboard.press('Alt+ArrowUp')
  await expect(rows).toHaveText(['Ausgangslage', 'Leistungsgegenstand und Vorgehen', 'Termine', 'Mitwirkung des Auftraggebers'])
  await expect(inspector(page).getByRole('button', { name: /Ausgangslage/ })).toBeFocused()
})

test('the Section tab sets real section formatting: number style, a new page, extra space', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page)
  await page.getByRole('textbox', { name: 'Heading section 3' }).click()
  await inspector(page).getByRole('radiogroup', { name: 'Section number style' }).getByRole('radio', { name: 'Roman capitals' }).click()
  await expect(page.locator('.quote-page [data-section-id]').nth(2).locator('.quote-section-number')).toHaveText('III.')
  await expect(inspector(page).locator('.note strong').first()).toHaveText('III.')
  const pageOf = () => page.locator('.quote-page').filter({ has: page.getByRole('textbox', { name: 'Heading section 3' }) }).getAttribute('data-page')
  const before = await pageOf()
  await inspector(page).getByRole('checkbox', { name: 'Start on a new page' }).check()
  await expect.poll(pageOf).not.toBe(before)
  const space = inspector(page).getByRole('spinbutton', { name: 'Before the section' })
  await space.focus(); await page.keyboard.press('Shift+ArrowUp')
  await expect(space).toHaveValue('5')
  await expect(page.locator('.quote-page [data-section-id]').nth(2)).toHaveCSS('padding-top', /^1[89]/)
  await inspector(page).getByRole('button', { name: 'Reset' }).click()
  await expect(space).toHaveValue('0')
})

test('the Document tab holds this quote’s settings and links to the templates', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page)
  await expect(inspector(page).getByLabel('Currency')).toBeDisabled()
  await expect(inspector(page)).toContainText('Fixed once positions are priced.')
  // Dates read as the document prints them, in the panel and on the paper; no native date inputs.
  await expect(page.locator('input[type="date"]')).toHaveCount(0)
  const valid = inspector(page).getByRole('button', { name: /^Valid until:/ })
  await expect(valid).toHaveText('24.10.2026')
  await expect(page.getByRole('button', { name: 'Gültig bis: Saturday, 24 October 2026' })).toHaveText('24.10.2026')
  await valid.click()
  const picker = page.getByRole('dialog', { name: 'Valid until' })
  await expect(picker.getByRole('button', { name: 'Saturday, 24 October 2026' })).toBeFocused()
  await picker.getByLabel('Date').fill('01.09.2026'); await picker.getByLabel('Date').press('Enter')
  await expect(inspector(page).getByRole('alert')).toHaveText('Valid until is before the quote date.')
  await expect(page.getByRole('button', { name: /^Gültig bis:/ })).toHaveText('01.09.2026')
  // The month grid by keyboard: a week on, a month on, Enter picks.
  await valid.click()
  await page.keyboard.press('ArrowDown'); await page.keyboard.press('PageDown')
  await page.keyboard.press('Enter')
  await expect(valid).toHaveText('08.10.2026')
  await expect(inspector(page)).toContainText('Open for 14 days.')
  // On the paper the picker opens from the date itself.
  await page.getByRole('button', { name: /^Angebotsdatum:/ }).click()
  await expect(page.getByRole('dialog', { name: 'Angebotsdatum' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('button', { name: /^Angebotsdatum:/ })).toBeFocused()
  await inspector(page).getByRole('link', { name: 'Edit templates' }).click()
  await expect(page).toHaveURL('/settings/business#quotes')
})

test('the title bar: compact zoom, the header chevron beside PDF, save state and one panel toggle', async ({ page }, info) => {
  test.setTimeout(120_000)
  await page.setViewportSize({ width: 1440, height: 900 })
  const { calls } = await open(page)
  const face = page.getByRole('button', { name: /^Zoom 100 %/ })
  await expect(face).toBeVisible()
  await page.getByRole('button', { name: /^Zoom in/ }).click()
  await expect(page.getByRole('button', { name: /^Zoom 125 %/ })).toBeVisible()
  await expect(page.locator('.paper-stack')).toHaveCSS('zoom', '1.25')
  await page.getByRole('button', { name: /^Zoom out/ }).click()
  await page.getByRole('button', { name: /^Zoom 100 %/ }).click()
  const pop = page.getByRole('dialog', { name: 'Zoom' })
  await expect(pop.getByRole('group', { name: 'Zoom presets' }).getByRole('button')).toHaveText(['25', '50', '75', '100', '125', '150', '175', '200', '250', '300', '400', '600', '800'])
  // Everything is visible at once: no scrolling inside the popover.
  expect(await pop.evaluate(el => el.scrollHeight <= el.clientHeight + 1)).toBe(true)
  await pop.getByRole('button', { name: 'Fit page' }).click()
  await expect(page.getByRole('button', { name: /^Zoom \d+ %, fit page/ })).toBeVisible()
  await page.getByRole('button', { name: /^Zoom \d+ %/ }).click()
  await pop.getByLabel('Other').fill('900'); await pop.getByLabel('Other').press('Enter')
  await expect(pop.getByText('Use a whole percent from 25 to 800.')).toBeVisible()
  await pop.getByLabel('Other').fill('333'); await pop.getByLabel('Other').press('Enter')
  await expect(page.getByRole('button', { name: /^Zoom 333 %/ })).toBeVisible()

  // The chevron folds the app header away and brings it back; it sits right after PDF.
  const order = await page.locator('.titlebar .right button, .titlebar .right a').evaluateAll(els => els.map(el => el.getAttribute('aria-label')))
  expect(order.indexOf('Hide the app header')).toBe(order.indexOf('PDF') + 1)
  await expect(page.getByRole('banner')).toBeVisible()
  await page.getByRole('button', { name: 'Hide the app header' }).click()
  await expect(page.locator('.app-header')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Show the app header' })).toHaveAttribute('aria-expanded', 'false')
  await page.getByRole('button', { name: 'Show the app header' }).click()
  await expect(page.locator('.app-header')).toBeVisible()

  // One toggle for the panel; the docked panel has no second close button.
  await expect(page.getByRole('button', { name: 'Format panel' })).toHaveCount(1)
  await expect(inspector(page).getByRole('button', { name: 'Close format' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Format panel' }).click()
  await expect(inspector(page)).toHaveCount(0)
  await page.getByRole('button', { name: 'Format panel' }).click()
  await expect(inspector(page)).toBeVisible()

  // Save state: clean, then an edit, then saved; PDF saves first and prints.
  const save = page.getByRole('button', { name: 'Save draft' })
  await expect(save).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Undo', exact: true })).toBeDisabled()
  await selectText(page, NODES[2]!, 4, 9)
  await inspector(page).getByRole('button', { name: 'Bold' }).click()
  await expect(save).toBeEnabled()
  await expect(page.getByRole('button', { name: 'Undo', exact: true })).toBeEnabled()
  await page.getByRole('button', { name: 'PDF' }).click()
  await expect.poll(() => page.evaluate(() => (window as unknown as { printed: number }).printed)).toBe(1)
  const patch = calls.find(c => c.method === 'PATCH' && c.path.endsWith('/draft'))!
  const node = (patch.body as { document: QuoteDoc }).document.sections[1]!.nodes[0] as { marks?: unknown }
  expect(node.marks).toEqual([{ start: 4, end: 9, bold: true }])
  await expect(save).toBeDisabled()
  await expect(page.locator('.save')).toContainText('Saved')
  // AEON-1688: phone toolbar actions share one ghost surface; only the open panel is tinted.
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await open(page)
    await page.evaluate(async theme => { document.documentElement.dataset.theme = theme; await document.fonts.ready }, theme)
    const bar = page.locator('.titlebar'), pdf = bar.getByRole('button', { name: 'PDF', exact: true })
    const chevron = bar.locator('.chevron'), more = bar.getByRole('button', { name: 'More actions', exact: true })
    const details = bar.getByRole('button', { name: 'Details', exact: true }), format = bar.getByRole('button', { name: 'Format panel', exact: true })
    const before = await bar.boundingBox()
    await expectStableControls({ controls: { bar, pdf, chevron, more, details, format }, interactions: [
      { name: 'open Details', run: async () => {
        await details.click()
        await expect(details).toHaveAttribute('aria-pressed', 'true')
        if (width === 390) {
          for (const control of [pdf, chevron, more, format]) {
            await expect(control).toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
            await expect(control).toHaveCSS('box-shadow', 'none')
          }
          await expect(details).not.toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
        }
      } },
      { name: 'switch to Format', run: async () => {
        await format.click()
        await expect(format).toHaveAttribute('aria-pressed', 'true')
        if (width === 390) {
          await expect(details).toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
          await expect(format).not.toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
        }
      } },
    ] })
    expect(await bar.boundingBox(), 'switching panels moves the toolbar by 0 px').toEqual(before)
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0)
    await page.screenshot({ path: info.outputPath(`AEON-1688/quote-editor-${width}-${theme}.png`) })
  }
})

// Nothing in the panel reaches past its inner gutter, whichever tab and layout.
async function insideGutter(page: Page) {
  const out = await page.locator('.quote-inspector-slot .panel').evaluate(panel => {
    const box = panel.getBoundingClientRect(), pad = parseFloat(getComputedStyle(panel).paddingRight)
    return [...panel.querySelectorAll<HTMLElement>('*')].filter(el => {
      const r = el.getBoundingClientRect()
      return r.width > 0 && r.height > 0 && !el.closest('svg') && r.right > box.right - pad + 0.5
    }).map(el => `${el.tagName}.${el.className}`)
  })
  expect(out).toEqual([])
}
test('every tab stays inside the panel gutter, docked, floating and as a sheet', async ({ page }) => {
  for (const [width, height] of [[1440, 900], [1024, 800], [390, 844]] as const) {
    await page.setViewportSize({ width, height })
    if (width === 1440) await open(page); else await page.reload()
    await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
    await selectText(page, NODES[4]!, 2)
    if (width < 1100) await page.getByRole('button', { name: 'Format panel' }).click()
    for (const tab of ['Text', 'Section', 'Document']) {
      await inspector(page).getByRole('tab', { name: tab }).click()
      await insideGutter(page)
    }
  }
})

test('narrower screens float the panel over the page; phones get a sheet; nothing is cut at 390', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 800 })
  await open(page)
  await expect(inspector(page)).toHaveCount(0)
  await page.getByRole('button', { name: 'Format panel' }).click()
  await expect(inspector(page)).toBeVisible()
  const panel = (await page.locator('.quote-inspector-slot').boundingBox())!
  expect(panel.x + panel.width).toBeLessThanOrEqual(1024 - 7)
  await page.keyboard.press('Escape')
  await expect(inspector(page)).toHaveCount(0)

  await page.setViewportSize({ width: 390, height: 844 })
  await selectText(page, NODES[3]!, 2)
  await page.getByRole('button', { name: 'Format panel' }).click()
  await expect(inspector(page).getByRole('tab', { selected: true })).toHaveText('Text')
  const sheet = (await page.locator('.quote-inspector-slot').boundingBox())!
  expect(Math.round(sheet.y + sheet.height)).toBeGreaterThanOrEqual(844 - 60)
  const cut = await page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.titlebar *, .quote-inspector-slot *')].filter(el => {
    const r = el.getBoundingClientRect(), c = getComputedStyle(el)
    if (r.width <= 1 || r.height <= 1 || c.display === 'none' || el.closest('svg')) return false
    return r.left < -0.5 || r.right > innerWidth + 0.5 || ((c.overflowX === 'hidden' || c.overflowX === 'auto') && el.scrollWidth > el.clientWidth + 1 && c.textOverflow !== 'ellipsis')
  }).map(el => `${el.tagName}.${el.className}`))
  expect(cut).toEqual([])
  await inspector(page).getByRole('button', { name: 'Close format' }).click()
  await expect(inspector(page)).toHaveCount(0)
})

test('an issued quote is read only: Details instead of Format, nothing on the paper changes', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  await mockQuoteEditor(page, quoteDocument(), { state: 'issued' })
  await page.goto(`/business/quotes/${QUOTE_ID}`)
  await expect(page.locator('.save')).toContainText('Read only')
  await expect(page.getByRole('button', { name: /Section \d actions/ })).toHaveCount(0)
  // Formatting an issued version means nothing: the panel shows its Details (U18).
  await expect(page.getByRole('button', { name: 'Format panel' })).toHaveCount(0)
  await expect(inspector(page)).toHaveCount(0)
  await expect(page.getByRole('complementary', { name: 'Details' })).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Angebotstitel' })).toHaveCount(0)
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`axe: the editor with each inspector scope and the zoom popover in ${colorScheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme })
    await page.setViewportSize({ width: 1440, height: 900 })
    await open(page)
    await axe(page)
    await selectText(page, NODES[3]!, 2)
    await expect(inspector(page).locator('.preview')).toBeVisible()
    await axe(page)
    await inspector(page).getByRole('tab', { name: 'Section' }).click()
    await axe(page)
    await page.getByRole('button', { name: /^Zoom \d+ %/ }).click()
    await axe(page)
  })
}
