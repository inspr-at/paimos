// SPDX-License-Identifier: AGPL-3.0-only
// Models v2 W3 (AEON-1162, approved design AEON-1144): the Quick jobs row with its Why?, and the admin-only fold
// "Different fallback by situation". Risk: members get a control they cannot use, Just me edits workspace orders,
// a cell popover leaves the screen or moves the page, or a cell change cannot be undone.
import { expect, test, type Locator, type Page } from '@playwright/test'
import { mockModels, type MockOptions } from './models-simple-fixtures'
import { openModelsSettings } from './models-settings-page'
import { controlStability } from './control-stability'

const cell = (page: Page, key: string) => page.locator(`[data-sit="${key}"]`)
const option = (page: Page, line: string) => page.locator(`.mdl-pop [data-line="${line}"]`)
const toast = (page: Page, text: string) => page.locator('.toast').filter({ hasText: text }).first()
async function open(page: Page, options: MockOptions = {}) {
  const problems: string[] = []
  page.on('console', message => { if (['error', 'warning'].includes(message.type())) problems.push(message.text()) }); page.on('pageerror', error => problems.push(String(error)))
  const state = await mockModels(page, options); await openModelsSettings(page)
  return Object.assign(state, { problems })
}
/** The overlay is fully on screen (design check: "inside the viewport"). */
const inView = (locator: Locator) => locator.evaluate(element => {
  const rect = element.getBoundingClientRect()
  return rect.left >= -0.5 && rect.top >= -0.5 && rect.right <= innerWidth + 0.5 && rect.bottom <= innerHeight + 0.5
})
/** Page positions of the rows and lines an overlay must not move (design check: "moves nothing"). */
const tops = (page: Page) => page.locator('.m-list .row, .sx-sum, .sx-cell, .next').evaluateAll(elements => elements.map(element => Math.round(element.getBoundingClientRect().top + scrollY)))
/** Visible controls smaller than the target size (24 px with a mouse, 44 px on touch). */
const small = (scope: Locator, min: number) => scope.locator('button, [role="option"], input, .sx-cell').evaluateAll((elements, size) => elements
  .filter(element => { const rect = element.getBoundingClientRect(), style = getComputedStyle(element); return rect.width > 0 && rect.height > 0 && style.visibility !== 'hidden' })
  .filter(element => { const rect = element.getBoundingClientRect(); return rect.width < size - 0.5 || rect.height < size - 0.5 })
  .map(element => `${element.className} ${(element.textContent ?? '').trim().slice(0, 20)} ${Math.round(element.getBoundingClientRect().width)}x${Math.round(element.getBoundingClientRect().height)}`), min)
/** Coloured edge accents (design check): a thick one-sided border, or a hard one-sided inset shadow. */
const accents = (page: Page, selector: string) => page.locator(selector).evaluateAll(roots => roots.flatMap(root => [root, ...root.querySelectorAll('*')]).flatMap(element => {
  const style = getComputedStyle(element), rect = element.getBoundingClientRect(), out: string[] = []
  if (!rect.width || !rect.height || style.visibility === 'hidden') return out
  const width = (side: string) => parseFloat(style.getPropertyValue(`border-${side}-width`)) || 0, colour = (side: string) => style.getPropertyValue(`border-${side}-color`), clear = (value: string) => /rgba\(.*,\s*0\)$|transparent/.test(value)
  for (const [a, b] of [['left', 'right'], ['top', 'bottom'], ['right', 'left'], ['bottom', 'top']] as const) if (width(a) >= 2 && !clear(colour(a)) && (width(b) < width(a) || colour(b) !== colour(a))) out.push(`${element.className} border-${a}`)
  for (const match of style.boxShadow.matchAll(/inset\s+(-?[\d.]+)px\s+(-?[\d.]+)px\s+([\d.]+)px|(-?[\d.]+)px\s+(-?[\d.]+)px\s+([\d.]+)px(?:\s+-?[\d.]+px)?\s+inset/g)) {
    const x = +(match[1] ?? match[4]), y = +(match[2] ?? match[5]), blur = +(match[3] ?? match[6])
    if ((Math.abs(x) >= 2 || Math.abs(y) >= 2) && blur <= 1) out.push(`${element.className} inset ${x}/${y}`)
  }
  return out
}))

test('members read Quick jobs and its Why, and never see the situation fold', async ({ page }) => {
  const state = await open(page, { member: true, situations: true })
  await expect(page.locator('[data-quick]')).toContainText('Quick jobs'); await expect(page.locator('[data-quick]')).toContainText('Automatic · the cheapest model that qualifies')
  await expect(page.locator('[data-situations]')).toHaveCount(0)
  const before = await tops(page)
  await page.locator('[data-quick-why]').click()
  const why = page.getByRole('dialog', { name: 'Quick jobs' })
  await expect(why).toContainText('Scout and mechanical work')
  await expect(why).toContainText('Right now: GPT-6 Luna · low through Codex.')
  await expect(why).toContainText('If none qualifies, the default runs: GPT-6.1 Sol · high.')
  expect(await inView(why)).toBe(true); expect(await tops(page)).toEqual(before)
  await page.keyboard.press('Escape'); await expect(why).toHaveCount(0); await expect(page.locator('[data-quick-why]')).toBeFocused()
  expect(state.problems).toEqual([])
})

test('an admin on Just me reads the workspace situations; empty cells read Same or Automatic', async ({ page }) => {
  const state = await open(page, { situations: true })
  await expect(page.getByRole('button', { name: 'Just me' })).toHaveAttribute('aria-pressed', 'true')
  const fold = page.locator('#sx-fold')
  await expect(fold).toHaveAttribute('aria-expanded', 'false'); await expect(fold).toContainText('Different fallback by situation · 3 set'); await expect(fold).toContainText('Expert')
  await fold.click(); await expect(fold).toHaveAttribute('aria-expanded', 'true')
  await expect(page.locator('.sx-lead')).toContainText('An empty cell uses the row. Change these under For everyone.')
  expect(await page.locator('.sx-h small').allTextContents()).toEqual(['2 h or less', 'after findings', 'after 3 rounds', 'another family'])
  await expect(page.locator('.sx-t button')).toHaveCount(0)
  await expect(cell(page, 'other:small')).toHaveText('Same'); await expect(cell(page, 'other:review')).toHaveText('Automatic')
  await expect(cell(page, 'design:fix')).toHaveText('Same'); await expect(cell(page, 'concept:review')).toHaveText('Automatic')
  await expect(cell(page, 'other:fix')).toContainText('Grok 4.7'); await expect(cell(page, 'other:stuck')).toContainText('GPT-6 Astra'); await expect(cell(page, 'other:stuck').locator('.cell-more')).toHaveText('+1')
  await expect(cell(page, 'design:stuck')).toContainText('Fable 5.1')
  await expect(cell(page, 'other:stuck').locator('.sr-only')).toHaveText('Default, Stuck: GPT-6 Astra · xhigh, then 1 more')
  // Read-only means no popover and no write.
  await cell(page, 'other:stuck').click(); await expect(page.locator('.mdl-pop')).toHaveCount(0)
  expect(state.writes).toHaveLength(0); expect(state.problems).toEqual([])
})

test('For everyone edits a cell in a popover that stays on screen and moves nothing; Undo restores it', async ({ page }) => {
  const state = await open(page, { situations: true })
  await page.getByRole('button', { name: 'For everyone' }).click()
  const fold = page.locator('#sx-fold'); await fold.click()
  const fix = cell(page, 'design:fix'); await expect(fix).toHaveText('Same')
  const guard = await controlStability(page, { scope: page.getByRole('button', { name: 'For everyone' }), quick: page.locator('[data-quick-why]'), pick: page.locator('[data-pick="all"]'), fold, fix, stuck: cell(page, 'other:stuck'), next: page.locator('[data-why]') })
  const before = await tops(page)
  await guard.check(async () => { await fix.click(); await expect(page.getByRole('dialog', { name: 'Fix rounds · UI design' })).toBeVisible() })
  const popover = page.getByRole('dialog', { name: 'Fix rounds · UI design' })
  expect(await inView(popover)).toBe(true); expect(await tops(page)).toEqual(before); expect(await small(popover, 24)).toEqual([])
  expect(await accents(page, '[data-quick], [data-situations], .mdl-pop')).toEqual([])
  await expect(popover).toContainText('Same as the row: its pick, then its fallback.'); await expect(popover.locator('[data-sx-clear]')).toHaveCount(0)
  await expect(popover.getByRole('button', { name: 'Use another order' })).toBeFocused()
  await guard.check(async () => { await page.keyboard.press('Escape'); await expect(popover).toHaveCount(0) })
  await expect(fix).toBeFocused()
  // Add opens the model menu on the cell; a level click picks model and level, written as the cell's own order.
  await fix.click(); await popover.getByRole('button', { name: 'Use another order' }).click()
  await expect(page.getByRole('combobox')).toBeFocused(); expect(await inView(page.locator('.mdl-pop'))).toBe(true)
  await guard.check(() => option(page, 'anthropic:sonnet').locator('.lvc[data-eff="xhigh"]').click())
  await expect(fix).toContainText('Sonnet 5.5'); await expect(fix).toBeFocused(); await expect(fold).toContainText('4 set')
  expect(state.writes.at(-1)).toMatchObject({ method: 'PUT', path: '/model-preferences/orders/design/fix', search: '?for=default', body: { rank: ['anthropic:sonnet'], efforts: { 'anthropic:sonnet': 'xhigh' }, end: 'row', revision: 5 } })
  await expect(toast(page, 'Saved for everyone')).toBeVisible()
  await page.getByRole('button', { name: 'Undo', exact: true }).last().click()
  await expect(fix).toHaveText('Same')
  expect(state.writes.at(-1)).toMatchObject({ method: 'DELETE', path: '/model-preferences/orders/design/fix', search: '?for=default&revision=6' })
  // Taking a step out keeps the rest of the order, its levels and its ending.
  await cell(page, 'other:stuck').click()
  const stuck = page.getByRole('dialog', { name: 'Stuck · Default' })
  await expect(stuck.locator('.sxp-it')).toHaveCount(2); await expect(stuck).toContainText('Then wait.')
  await stuck.getByRole('button', { name: 'Take GPT-6 Astra out' }).click()
  await expect(cell(page, 'other:stuck')).toContainText('Opus 5.5'); await expect(cell(page, 'other:stuck').locator('.cell-more')).toHaveCount(0)
  expect(state.writes.at(-1)).toMatchObject({ method: 'PUT', path: '/model-preferences/orders/other/stuck', body: { rank: ['anthropic:opus'], efforts: { 'anthropic:opus': 'max' }, revision: 7 } })
  expect(state.writes.at(-1)!.body).not.toHaveProperty('end'); expect(state.situations.get('other:stuck')?.end).toBe('wait')
  // "Same as the row" removes the cell's order; Review goes back to automatic and adds with an automatic ending.
  await cell(page, 'other:fix').click(); await page.getByRole('dialog', { name: 'Fix rounds · Default' }).getByRole('button', { name: 'Same as the row' }).click()
  await expect(cell(page, 'other:fix')).toHaveText('Same')
  expect(state.writes.at(-1)).toMatchObject({ method: 'DELETE', path: '/model-preferences/orders/other/fix', search: '?for=default&revision=8' })
  await cell(page, 'design:review').click()
  const review = page.getByRole('dialog', { name: 'Review · UI design' })
  await expect(review).toContainText('Automatic: the first qualified model of another family.')
  await review.getByRole('button', { name: 'Use another order' }).click(); await page.getByRole('combobox').fill('sol'); await page.keyboard.press('Enter')
  await expect(cell(page, 'design:review')).toContainText('GPT-6.1 Sol')
  expect(state.writes.at(-1)).toMatchObject({ method: 'PUT', path: '/model-preferences/orders/design/review', body: { rank: ['openai:sol'], efforts: { 'openai:sol': 'xhigh' }, end: 'auto' } })
  await cell(page, 'design:review').click()
  await expect(review).toContainText('Then automatic, always another family.'); await expect(review.getByRole('button', { name: 'Back to automatic' })).toBeVisible()
  guard.done(); expect(state.problems).toEqual([])
})

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 860 }, hasTouch: true, isMobile: true })
  test('each cell names its situation, every target is 44 px and the popovers stay on screen', async ({ page }) => {
    const state = await open(page, { situations: true, longLabels: true })
    await page.getByRole('button', { name: 'For everyone' }).click(); await page.locator('#sx-fold').click()
    await expect(page.locator('.sx-h')).toBeHidden()
    await expect(cell(page, 'other:small').locator('.sx-cl')).toHaveText('First build'); await expect(cell(page, 'other:small').locator('.sx-cl')).toBeVisible()
    await expect(cell(page, 'design:review').locator('.sx-cl')).toHaveText('Review')
    expect(await small(page.locator('[data-situations]'), 44)).toEqual([])
    expect(await small(page.locator('[data-quick]'), 44)).toEqual([])
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0)
    for (const key of ['other:stuck', 'design:review']) {
      await cell(page, key).scrollIntoViewIfNeeded(); const before = await tops(page)
      await cell(page, key).click()
      const popover = page.locator('.mdl-pop'); await expect(popover).toBeVisible()
      expect(await inView(popover)).toBe(true); expect(await tops(page)).toEqual(before); expect(await small(popover, 44)).toEqual([])
      expect(await accents(page, '[data-situations], .mdl-pop')).toEqual([])
      await page.keyboard.press('Escape'); await expect(popover).toHaveCount(0)
    }
    await page.locator('[data-quick-why]').scrollIntoViewIfNeeded(); await page.locator('[data-quick-why]').click()
    expect(await inView(page.getByRole('dialog', { name: 'Quick jobs' }))).toBe(true)
    expect(state.problems).toEqual([])
  })
})
