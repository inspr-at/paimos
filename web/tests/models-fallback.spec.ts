// SPDX-License-Identifier: AGPL-3.0-only
// Settings › Models v2: the fallback fold and order editor (AEON-1161, approved design AEON-1143/1144). Risk: a fold
// that moves the controls above it, a reorder the keyboard cannot make or hear, an Undo that does not restore the
// order, a member's edit that writes the workspace's row, or a locked row a member can still change. Flows A and C
// port the design's check.mjs to the built page; screenshots go to Playwright's output directory only.
import { expect, test, type Page, type TestInfo } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mockModels, simplePerson, type MockOptions } from './models-simple-fixtures'
import { openModelsSettings } from './models-settings-page'
import { controlStability } from './control-stability'

const SOL = 'openai:sol', OPUS = 'anthropic:opus', SONNET = 'anthropic:sonnet', FABLE = 'anthropic:fable'
const fold = (page: Page, key: string) => page.locator(`[data-fold="${key}"]`)
const steps = (page: Page, key: string) => page.locator(`[data-chain="${key}"] .ch-it .pn`)
const live = (page: Page) => page.locator('[data-models-section] > p.sr-only[role="status"]')
const undo = (page: Page) => page.getByRole('button', { name: 'Undo', exact: true }).last()
async function open(page: Page, options: MockOptions = {}) {
  const problems: string[] = []
  page.on('console', message => { if (['error', 'warning'].includes(message.type())) problems.push(message.text()) }); page.on('pageerror', error => problems.push(String(error)))
  const state = await mockModels(page, { v2: true, ...options }); await openModelsSettings(page)
  return Object.assign(state, { problems })
}
async function openFold(page: Page, key: string) {
  await fold(page, key).click()
  await expect(fold(page, key)).toHaveAttribute('aria-expanded', 'true'); await expect(page.locator(`#fb-${key} .ch-wrap`)).toBeVisible()
}

// The design's own probes (check.mjs), scoped to the fallback folds and the menus over them.
async function audit(page: Page, min: number) {
  return page.evaluate(min => {
    const vis = (element: Element) => { const box = element.getBoundingClientRect(), style = getComputedStyle(element); return box.width > 0 && box.height > 0 && style.visibility !== 'hidden' && style.display !== 'none' }
    const roots = [...document.querySelectorAll('[data-models-section] .fb, .mdl-pop')]
    const small = roots.flatMap(root => [...root.querySelectorAll('button, [role="option"], input:not([type="checkbox"]), [tabindex="0"]')]).filter(vis)
      .map(element => ({ element, box: element.getBoundingClientRect() })).filter(({ box }) => box.width < min - 0.5 || box.height < min - 0.5)
      .map(({ element, box }) => `${element.className}[${(element.textContent || element.getAttribute('aria-label') || '').trim().slice(0, 18)}] ${Math.round(box.width)}x${Math.round(box.height)}`)
    const accents = roots.flatMap(root => [...root.querySelectorAll('*')]).filter(vis).flatMap(element => {
      const style = getComputedStyle(element), width = (side: string) => parseFloat(style.getPropertyValue(`border-${side}-width`)) || 0, color = (side: string) => style.getPropertyValue(`border-${side}-color`)
      const clear = (value: string) => /rgba\(.*,\s*0\)$|transparent/.test(value)
      return [['left', 'right'], ['top', 'bottom'], ['right', 'left'], ['bottom', 'top']].filter(([a, b]) => width(a!) >= 2 && !clear(color(a!)) && (width(b!) < width(a!) || color(b!) !== color(a!))).map(([a]) => `${element.className} border-${a}`)
    })
    const clipped = [...document.querySelectorAll('[data-models-section] .ch-end-l, [data-models-section] .fb .seg button, [data-models-section] .fb .add span')].filter(vis).filter(element => element.scrollWidth > element.clientWidth + 1).map(element => element.textContent?.trim())
    return { small, accents, clipped, overflow: document.documentElement.scrollWidth - innerWidth, dialogs: document.querySelectorAll('[data-models-section] dialog').length }
  }, min)
}
async function common(page: Page, min: number) {
  const found = await audit(page, min)
  expect(found.overflow, 'horizontal scroll').toBeLessThanOrEqual(1)
  expect(found.dialogs, 'no dialog element').toBe(0)
  expect(found.accents, 'no coloured edge accents').toEqual([])
  expect(found.small, `targets ≥ ${min} px`).toEqual([])
  expect(found.clipped, 'one-line labels not clipped').toEqual([])
}
/** Page positions, independent of where the page (or its scroll container) is scrolled. */
const tops = (page: Page, selector = '[data-models-section] .m-list .row, [data-models-section] .next, [data-models-section] #ex-h') => page.evaluate(selector => [...document.querySelectorAll(selector)].map(element => {
  let top = element.getBoundingClientRect().top + scrollY
  for (let parent = element.parentElement; parent; parent = parent.parentElement) top += parent.scrollTop
  return Math.round(top)
}).join(','), selector)
/** A menu overlays: it opens inside the viewport, nothing on the page moves, and Esc gives focus back. */
async function overlay(page: Page, trigger: string, min: number) {
  await page.locator(trigger).evaluate(element => element.scrollIntoView({ block: 'center' }))
  const before = await tops(page)
  await page.locator(trigger).click(); await expect(page.locator('.mdl-pop')).toBeVisible()
  const inside = await page.locator('.mdl-pop').evaluate(panel => { const box = panel.getBoundingClientRect(); return box.left >= -0.5 && box.top >= -0.5 && box.right <= innerWidth + 0.5 && box.bottom <= innerHeight + 0.5 })
  expect(inside, `${trigger} menu inside the viewport`).toBe(true)
  expect(await tops(page), `${trigger} menu moves nothing`).toBe(before)
  expect((await audit(page, min)).small, `${trigger} menu targets ≥ ${min} px`).toEqual([])
  await page.keyboard.press('Escape'); await expect(page.locator('.mdl-pop')).toHaveCount(0); await expect(page.locator(trigger)).toBeFocused()
}
// The harness scrolls inside its main element, so the whole Models card is captured as an element.
const shot = (page: Page, testInfo: TestInfo, name: string, card = true) => card ? page.locator('[data-models-section] .m-card').screenshot({ path: testInfo.outputPath(`${name}.png`) }) : page.screenshot({ path: testInfo.outputPath(`${name}.png`) })

for (const width of [1440, 390]) for (const theme of ['light', 'dark']) test.describe(`at ${width} ${theme}`, () => {
  const phone = width < 600, min = phone ? 44 : 24
  test.use(phone ? { viewport: { width, height: 860 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } : { viewport: { width, height: 1000 } })

  test(`A · For everyone: folds, menus, keyboard reorder and Undo`, async ({ page }, testInfo) => {
    const state = await open(page)
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await page.getByRole('button', { name: 'For everyone' }).click(); await expect(page.locator('[data-row="design"] [data-lock]')).toBeVisible()
    await expect(fold(page, 'all')).toHaveAccessibleName('Fallback for the default: if GPT-6.1 Sol can’t run, then Opus 5.5, then Wait')
    await expect(fold(page, 'design')).toContainText('If Opus 5.5 can’t run'); await expect(fold(page, 'design')).toContainText('the default’s fallback')
    await openFold(page, 'all'); await openFold(page, 'design')
    await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · xhigh', 'Opus 5.5 · max']); await expect(steps(page, 'design')).toHaveText(['Opus 5.5 · xhigh', 'Sonnet 5.5 · xhigh'])
    await expect(page.locator('#fb-all .ch-end')).toContainText('Then wait. Nothing outside this order runs.')
    await expect(page.locator('#fb-design .ch-end-d')).toHaveText(/GPT-6\.1 Sol\s*Opus 5\.5\s*Wait/)
    await expect(page.locator('#fb-all')).toContainText('At a daily cap, the Agents dial follows this order too.')
    await expect(page.locator('[data-never="design:xai:grok"]')).toContainText('Never for UI design: Grok 4.7 · Markus, 2 Oct')
    await expect(page.locator('[data-unnever="design:xai:grok"]')).toBeVisible()
    await common(page, min)
    await overlay(page, '[data-pick="all"]', min)
    await overlay(page, '[data-chadd="design"]', min)
    await overlay(page, '[data-why]', min)
    await shot(page, testInfo, `fallback-${width}-${theme}-everyone`)
    // Closing a fold moves nothing above it.
    const above = await tops(page, '[data-models-section] .row.def, [data-models-section] #ex-h, [data-fold="design"]')
    await fold(page, 'design').click(); await expect(page.locator('#fb-design .ch-wrap')).toHaveCount(0)
    expect(await tops(page, '[data-models-section] .row.def, [data-models-section] #ex-h, [data-fold="design"]')).toBe(above)
    // Add a fallback from the keyboard: type, Enter; it joins at the row's level and focus returns to Add.
    await page.locator('[data-chadd="all"]').click(); await expect(page.getByRole('combobox')).toBeFocused()
    await expect(page.locator(`.mdl-pop [data-line="${SOL}"]`)).toHaveCount(0); await expect(page.locator(`.mdl-pop [data-line="${OPUS}"]`)).toHaveCount(0)
    if (!phone) await shot(page, testInfo, `fallback-${width}-${theme}-add`, false)
    await page.keyboard.type('Sonnet'); await page.keyboard.press('Enter')
    await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · xhigh', 'Opus 5.5 · max', 'Sonnet 5.5 · xhigh']); await expect(page.locator('#chadd-all')).toBeFocused()
    // ↑ on a focused handle reorders, says so, and keeps focus on the step it moved.
    await page.locator('[data-move="all:1"]').focus(); await page.keyboard.press('ArrowUp')
    await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · xhigh', 'Sonnet 5.5 · xhigh', 'Opus 5.5 · max'])
    await expect(live(page)).toHaveText('Sonnet 5.5 moved to step 2 of 3.'); await expect(page.locator('[data-move="all:0"]')).toBeFocused()
    await expect(fold(page, 'all')).toHaveText(/Sonnet 5\.5.*Opus 5\.5.*Wait/)
    // Autosave with Undo; Undo restores the previous order.
    await expect(page.locator('.toast').filter({ hasText: 'Saved for everyone' }).last()).toBeVisible()
    await undo(page).click()
    await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · xhigh', 'Opus 5.5 · max', 'Sonnet 5.5 · xhigh']); await expect(fold(page, 'all')).toHaveText(/Opus 5\.5.*Sonnet 5\.5.*Wait/)
    expect(state.writes.map(write => [write.method, `${write.path}${write.search}`, write.body])).toEqual([
      // Levels replace the order's own level; it is written back at once, so rows that follow the default keep following it.
      ['PUT', '/model-preferences/orders/other/first?for=default', { rank: [SOL, OPUS, SONNET], efforts: { [SOL]: 'xhigh', [OPUS]: 'max', [SONNET]: 'xhigh' }, revision: 5 }],
      ['PUT', '/model-preferences/orders/other/first/thinking?for=default', { effort: 'xhigh', revision: 6 }],
      ['PUT', '/model-preferences/orders/other/first?for=default', { rank: [SOL, SONNET, OPUS], revision: 7 }],
      ['PUT', '/model-preferences/orders/other/first?for=default', { rank: [SOL, OPUS, SONNET], revision: 8 }],
    ])
    expect(state.writes.some(write => write.person)).toBe(false)
    await common(page, min)
    expect(state.problems).toEqual([])
  })

  test(`C · a member: a locked row is read-only, an edit makes a workspace row hers`, async ({ page }, testInfo) => {
    const state = await open(page, { member: true })
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await openFold(page, 'concept'); await openFold(page, 'design')
    await expect(page.locator('[data-chain="design"] .grip')).toHaveCount(0); await expect(page.locator('[data-chadd="design"]')).toHaveCount(0)
    await expect(page.locator('[data-chain="design"] button, #fb-design [data-end]')).toHaveCount(0)
    await expect(steps(page, 'design')).toHaveText(['Opus 5.5 · xhigh', 'Sonnet 5.5 · xhigh'])
    await expect(page.locator('#fb-design')).toContainText('Locked for everyone, with its order.'); await expect(page.locator('#fb-design .ch-end')).toContainText('Then the default’s fallback GPT-6.1 Sol')
    await expect(page.locator('[data-never="design:xai:grok"]')).toContainText('Never for UI design: Grok 4.7 · Markus, 2 Oct'); await expect(page.locator('[data-unnever]')).toHaveCount(0)
    await expect(page.locator('[data-row="concept"] .mine')).toHaveCount(0); await expect(page.locator('[data-end="concept:wait"]')).toHaveAttribute('aria-checked', 'true')
    await common(page, min)
    await page.locator('[data-end="concept:default"]').click()
    await expect(page.locator('[data-row="concept"] .mine')).toHaveText('yours · reset'); await expect(page.locator('[data-end="concept:default"]')).toHaveAttribute('aria-checked', 'true')
    await expect(page.locator('[data-end="concept:default"]')).toBeFocused()
    await expect(page.locator('#fb-concept .ch-end-d')).toHaveText(/GPT-6\.1 Sol\s*Sonnet 5\.5\s*Wait/)
    expect(state.writes).toEqual([expect.objectContaining({ method: 'PUT', path: '/model-preferences/orders/concept/first', search: '?for=me', person: simplePerson, body: { rank: [FABLE], end: 'default', revision: 3 } })])
    expect(state.orders.default.get('concept')).toMatchObject({ end: 'wait' })
    await shot(page, testInfo, `fallback-${width}-${theme}-mia`)
    await undo(page).click(); await expect(page.locator('[data-row="concept"] .mine')).toHaveCount(0)
    expect(state.writes.at(-1)).toMatchObject({ method: 'DELETE', path: '/model-preferences/orders/concept/first', search: '?for=me&revision=4', person: simplePerson })
    expect(state.problems).toEqual([])
  })
})

test('a step drags by its handle to a new place; the order keeps its height while it moves', async ({ page }) => {
  const state = await open(page)
  await page.getByRole('button', { name: 'For everyone' }).click(); await openFold(page, 'all')
  await page.locator('[data-chadd="all"]').click(); await page.locator(`.mdl-pop [data-line="${FABLE}"] .lvc[data-eff="high"]`).click()
  await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · xhigh', 'Opus 5.5 · max', 'Fable 5.1 · high'])
  const list = page.locator('[data-chain="all"]'), height = (await list.boundingBox())!.height
  const grip = (await page.locator('[data-move="all:1"]').boundingBox())!, target = page.locator('[data-chain="all"] .ch-it[data-i="0"]'), goal = (await target.boundingBox())!
  await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2); await page.mouse.down()
  await page.mouse.move(goal.x + 60, goal.y + goal.height / 2, { steps: 6 })
  await expect(target).toHaveClass(/\bover\b/); await expect(page.locator('[data-chain="all"] .ch-it[data-i="1"]')).toHaveClass(/\bdragging\b/)
  expect(Math.abs((await list.boundingBox())!.height - height)).toBeLessThanOrEqual(.5)
  await page.mouse.up()
  await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · xhigh', 'Fable 5.1 · high', 'Opus 5.5 · max'])
  expect(state.writes.at(-1)!.body).toEqual({ rank: [SOL, FABLE, OPUS], revision: 7 })
  // A click on a handle without moving changes nothing.
  await page.locator('[data-move="all:0"]').click(); expect(state.writes).toHaveLength(3)
})

test('a step comes out and Undo puts it back with its level; a level changes in the step’s own menu', async ({ page }) => {
  const state = await open(page)
  await page.getByRole('button', { name: 'For everyone' }).click(); await openFold(page, 'design')
  await page.locator('[data-chrm="design:0"]').click()
  await expect(steps(page, 'design')).toHaveText(['Opus 5.5 · xhigh']); await expect(page.locator('#chadd-design')).toBeFocused()
  await expect(fold(page, 'design')).toHaveText(/If Opus 5\.5 can’t run\s*the default’s fallback/)
  await undo(page).click(); await expect(steps(page, 'design')).toHaveText(['Opus 5.5 · xhigh', 'Sonnet 5.5 · xhigh'])
  expect(state.writes.map(write => write.body)).toEqual([{ rank: [OPUS], revision: 5 }, { rank: [OPUS, SONNET], efforts: { [OPUS]: 'xhigh', [SONNET]: 'xhigh' }, revision: 6 }, { effort: 'xhigh', revision: 7 }])
  // The step's own menu picks its level; the order and the pick keep theirs.
  await page.locator('[data-chpick="design:0"]').click(); await expect(page.locator(`.mdl-pop [data-line="${SONNET}"]`)).toHaveAttribute('aria-selected', 'true')
  await expect(page.locator(`.mdl-pop [data-line="${OPUS}"]`)).toHaveCount(0)
  await page.locator(`.mdl-pop [data-line="${SONNET}"] .lvc[data-eff="max"]`).click()
  await expect(steps(page, 'design')).toHaveText(['Opus 5.5 · xhigh', 'Sonnet 5.5 · max']); await expect(page.locator('#chp-design-0')).toBeFocused()
  expect(state.writes.slice(-2).map(write => write.body)).toEqual([{ rank: [OPUS, SONNET], efforts: { [OPUS]: 'xhigh', [SONNET]: 'max' }, revision: 8 }, { effort: 'xhigh', revision: 9 }])
  expect(state.problems).toEqual([])
})

test('admins allow a Never line again for everyone; Undo sets it again with its reason', async ({ page }) => {
  const state = await open(page)
  await page.getByRole('button', { name: 'For everyone' }).click(); await openFold(page, 'design')
  await page.locator('[data-unnever="design:xai:grok"]').click()
  await expect(page.locator('[data-never="design:xai:grok"]')).toHaveCount(0); await expect(live(page)).toHaveText('Grok 4.7 is allowed again for UI design.')
  expect(state.writes.at(-1)).toMatchObject({ method: 'DELETE', path: '/model-preferences/orders/design/first/never/xai%3Agrok', search: '?revision=2' })
  await undo(page).click(); await expect(page.locator('[data-never="design:xai:grok"]')).toBeVisible()
  expect(state.writes.at(-1)).toMatchObject({ method: 'PUT', path: '/model-preferences/orders/design/first/never/xai%3Agrok', body: { why: 'Moved here from the earlier model board', revision: 3 } })
  // On Just me the line reads as a fact, without Allow again; the fold stays open across the switch.
  await page.getByRole('button', { name: 'Just me' }).click(); await expect(fold(page, 'design')).toHaveAttribute('aria-expanded', 'true')
  await expect(page.locator('[data-never="design:xai:grok"]')).toBeVisible(); await expect(page.locator('[data-unnever]')).toHaveCount(0)
})

test('a member keeps a step a workspace pin holds last where it is, and the fold says why', async ({ page }) => {
  const state = await open(page, { member: true, pinned: true })
  await openFold(page, 'all')
  await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · high', 'Sonnet 5.5 · high', 'Fable 5.1 · high'])
  await expect(page.locator('[data-move="all:0"]')).toBeVisible(); await expect(page.locator('[data-chrm="all:0"]')).toBeVisible()
  await expect(page.locator('[data-move="all:1"]')).toHaveCount(0); await expect(page.locator('[data-chrm="all:1"]')).toHaveCount(0)
  await expect(page.locator('[data-fixed="all"]')).toHaveText('Fable 5.1 stays the last step: it is set for everyone.')
  // The step above it moves neither below it from the keyboard nor by dragging.
  await page.locator('[data-move="all:0"]').focus(); await page.keyboard.press('ArrowDown'); await expect(page.locator('[data-move="all:0"]')).toBeFocused()
  const grip = (await page.locator('[data-move="all:0"]').boundingBox())!, last = (await page.locator('[data-chain="all"] .ch-it[data-i="1"]').boundingBox())!
  await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2); await page.mouse.down()
  await page.mouse.move(last.x + 60, last.y + last.height / 2, { steps: 6 }); await page.mouse.up()
  await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · high', 'Sonnet 5.5 · high', 'Fable 5.1 · high'])
  expect(state.writes).toEqual([])
  // Its level is still the person's own to choose.
  await page.locator('[data-chpick="all:1"]').click(); await page.locator(`.mdl-pop [data-line="${FABLE}"] .lvc[data-eff="max"]`).click()
  await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · high', 'Sonnet 5.5 · high', 'Fable 5.1 · max'])
  expect(state.writes.map(write => [write.path, write.person, write.body])).toEqual([
    ['/model-preferences/orders/other/first', simplePerson, { rank: [SOL, SONNET, FABLE], efforts: { [SOL]: 'high', [SONNET]: 'high', [FABLE]: 'max' }, revision: 3 }],
    ['/model-preferences/orders/other/first/thinking', simplePerson, { effort: 'high', revision: 4 }],
  ])
  expect(state.problems).toEqual([])
})

test('an admin takes a step a workspace pin holds last out with its pin; Undo sets both again, and moving it lifts the pin too', async ({ page }) => {
  const state = await open(page, { pinned: true })
  const pins = () => state.rules.filter(rule => rule.lock === 'bottom').map(rule => [rule.column, rule.line, rule.why])
  const writes = (from: number) => state.writes.slice(from).map(write => [write.method, write.path, write.body])
  await page.getByRole('button', { name: 'For everyone' }).click(); await openFold(page, 'all')
  await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · xhigh', 'Opus 5.5 · max', 'Fable 5.1 · xhigh'])
  await expect(page.locator('[data-fixed="all"]')).toHaveCount(0); await expect(page.locator('[data-move="all:1"]')).toBeVisible()
  await page.locator('[data-chrm="all:1"]').click()
  await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · xhigh', 'Opus 5.5 · max']); await expect(fold(page, 'all')).not.toContainText('Fable')
  expect(writes(0)).toEqual([
    ['PUT', '/model-rules/workspace/other', { top: [], bottom: [], not: {}, revision: 2 }],
    ['PUT', '/model-preferences/orders/other/first', { rank: [SOL, OPUS], revision: 5 }],
  ])
  expect(pins()).toEqual([])
  await undo(page).click()
  await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · xhigh', 'Opus 5.5 · max', 'Fable 5.1 · xhigh'])
  expect(pins()).toEqual([['other', FABLE, 'Last resort on the earlier model board']])
  expect(writes(2)).toEqual([
    ['PUT', '/model-rules/workspace/other', { top: [], bottom: [{ line: FABLE, why: 'Last resort on the earlier model board' }], not: {}, revision: 3 }],
    ['PUT', '/model-preferences/orders/other/first', { rank: [SOL, OPUS, FABLE], efforts: { [SOL]: 'xhigh', [OPUS]: 'max', [FABLE]: 'xhigh' }, revision: 6 }],
    ['PUT', '/model-preferences/orders/other/first/thinking', { effort: 'xhigh', revision: 7 }],
  ])
  // ↑ on its handle: the pin is lifted in the same change, and the order keeps the place it shows.
  await page.locator('[data-move="all:1"]').focus(); await page.keyboard.press('ArrowUp')
  await expect(steps(page, 'all')).toHaveText(['GPT-6.1 Sol · xhigh', 'Fable 5.1 · xhigh', 'Opus 5.5 · max'])
  await expect(live(page)).toHaveText('Fable 5.1 moved to step 2 of 3.'); await expect(page.locator('[data-move="all:0"]')).toBeFocused()
  expect(writes(5)).toEqual([
    ['PUT', '/model-rules/workspace/other', { top: [], bottom: [], not: {}, revision: 4 }],
    ['PUT', '/model-preferences/orders/other/first', { rank: [SOL, FABLE, OPUS], revision: 8 }],
  ])
  expect(pins()).toEqual([])
  expect(state.problems).toEqual([])
})

test('folds grow in place: the controls above a fold, and the fold’s own controls, never move (AEON-541)', async ({ page }) => {
  await open(page)
  await page.getByRole('button', { name: 'For everyone' }).click()
  const top = { scope: page.locator('[data-scope-group]'), catalog: page.locator('[data-catalog-fold]'), defaultPick: page.locator('[data-pick="all"]'), defaultFold: fold(page, 'all') }
  const first = await controlStability(page, top)
  await first.check(async () => { await openFold(page, 'all') })
  await first.check(async () => { await fold(page, 'all').click(); await expect(page.locator('#fb-all .ch-wrap')).toHaveCount(0) }); first.done()
  await openFold(page, 'all')
  const below = await controlStability(page, { ...top, designPick: page.locator('[data-pick="design"]'), designFold: fold(page, 'design') })
  await below.check(async () => { await openFold(page, 'design') })
  await below.check(async () => { await fold(page, 'design').click(); await expect(page.locator('#fb-design .ch-wrap')).toHaveCount(0) }); below.done()
  // Inside an open fold: reordering, a level change and the ending keep the add button and the ending still.
  await openFold(page, 'design')
  // A step's own button is as wide as its model and level; the controls around it are measured.
  const inside = await controlStability(page, { add: page.locator('#chadd-design'), defaultChoice: page.locator('[data-end="design:default"]'), waitChoice: page.locator('[data-end="design:wait"]'), stepHandle: page.locator('[data-move="design:0"]'), stepRemove: page.locator('[data-chrm="design:0"]') })
  await inside.check(async () => { await page.locator('[data-end="design:wait"]').click(); await expect(page.locator('[data-end="design:wait"]')).toHaveAttribute('aria-checked', 'true') })
  await inside.check(async () => { await page.locator('[data-end="design:default"]').press('ArrowRight'); await expect(page.locator('[data-end="design:default"]')).toHaveAttribute('aria-checked', 'true') })
  await inside.check(async () => { await page.locator('[data-chpick="design:0"]').click(); await page.locator('.mdl-pop [data-line="anthropic:sonnet"] .lvc[data-eff="high"]').click(); await expect(steps(page, 'design')).toHaveText(['Opus 5.5 · xhigh', 'Sonnet 5.5 · high']) })
  inside.done()
})

test('the folds and their menus have no accessibility violations in light and dark', async ({ page }) => {
  await open(page, { down: true })
  await page.getByRole('button', { name: 'For everyone' }).click(); await openFold(page, 'all'); await openFold(page, 'design')
  await expect(page.locator('#fb-design .ch-st').first()).toHaveText('runs now, not through Claude Code')
  for (const theme of ['light', 'dark']) {
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    const results = await new AxeBuilder({ page }).include('[data-models-section]').analyze()
    expect(results.violations.map(violation => violation.id), theme).toEqual([])
    await page.locator('[data-chadd="design"]').click(); await expect(page.locator('.mdl-pop')).toBeVisible()
    const menu = await new AxeBuilder({ page }).include('.mdl-pop').analyze()
    expect(menu.violations.map(violation => violation.id), `${theme} menu`).toEqual([])
    await page.keyboard.press('Escape')
  }
})
