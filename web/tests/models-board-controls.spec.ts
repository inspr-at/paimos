// SPDX-License-Identifier: AGPL-3.0-only
// Risks: one-write edits, immutable inherited rules, stale identity/Undo, and AEON-541 controls.
import { expect, test } from '@playwright/test'
import { boardPerson, boardPin } from './models-board-fixtures'
import { controlStability } from './control-stability'
import { card, col, headerControls, layer, mockBoard, open } from './models-board-page'

test('keyboard moves write once with person and revision, keep pins and other controls still, and cross Not allowed', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  const guard = await controlStability(page, { ...headerControls(page), pin: card(page, boardPin.line, 'frontend'), firstSlot: col(page).locator('[data-zone="list"] li').first(), frame: page.locator('[data-board-scroll]') })
  await guard.check(async () => { await card(page, 'anthropic:sonnet').focus(); await page.keyboard.press('Alt+ArrowUp'); await expect(col(page).locator('[data-zone="list"] li').first()).toHaveAttribute('data-card', 'anthropic:sonnet') })
  expect(state.writes).toHaveLength(1); expect(state.writes[0]).toMatchObject({ person: boardPerson, body: { revision: 3, rank: ['anthropic:sonnet', 'openai:sol', 'anthropic:opus', 'openai:astra', 'anthropic:fable'], not: [] } })
  await expect(card(page, 'anthropic:sonnet')).toBeFocused()
  await guard.check(async () => { await card(page, 'anthropic:fable').focus(); await page.keyboard.press('Alt+ArrowDown'); await expect(col(page).locator('[data-zone="not"] [data-line="anthropic:fable"]')).toBeVisible() })
  expect(state.writes).toHaveLength(2)
  await guard.check(async () => { await card(page, boardPin.line, 'frontend').focus(); await page.keyboard.press('Alt+ArrowDown'); expect(state.writes).toHaveLength(2) })
  guard.done()
  await card(page, 'anthropic:fable').press('Enter'); await page.getByRole('menuitem', { name: 'Allow again', exact: true }).click()
  await expect(col(page).locator('[data-zone="list"] li').last()).toHaveAttribute('data-card', 'anthropic:fable')
})

test('pointer drag keeps a placeholder, pins and surrounding columns still and sends only the dropped order', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  const source = card(page, 'anthropic:sonnet'), destination = card(page, 'openai:sol')
  const guard = await controlStability(page, { ...headerControls(page), pin: card(page, boardPin.line, 'frontend'), adjacent: col(page, 'frontend'), firstSlot: col(page).locator('[data-zone="list"] li').first() })
  const a = (await source.boundingBox())!, b = (await destination.boundingBox())!
  await page.mouse.move(a.x + 40, a.y + 20); await page.mouse.down()
  await guard.check(async () => { await page.mouse.move(a.x + 48, a.y + 22); await expect(source).toHaveClass(/placeholder/); expect(state.writes).toHaveLength(0) })
  await guard.check(async () => { await page.mouse.move(b.x + 40, b.y + 8, { steps: 4 }); await page.mouse.up(); await expect(col(page).locator('[data-zone="list"] li').first()).toHaveAttribute('data-card', 'anthropic:sonnet') })
  expect(state.writes).toHaveLength(1); await expect(page.locator('[data-board-ghost]')).toHaveCount(0); guard.done()
})

test('project rules retain workspace pins, require a reason, preserve submit controls, and surface a server refusal', async ({ page }) => {
  const state = await mockBoard(page); await open(page); await layer(page, 'Project rules')
  const inherited = card(page, boardPin.line, 'frontend')
  await inherited.press('Alt+ArrowDown'); expect(state.writes).toHaveLength(0)
  await inherited.press('Enter'); await expect(page.getByRole('menuitem', { name: /Locked/ })).toContainText(boardPin.why); await page.keyboard.press('Escape')
  await card(page, 'openai:sol', 'frontend').press('Enter'); await page.getByRole('menuitem', { name: 'Pin to bottom', exact: true }).click()
  const field = page.getByPlaceholder('One sentence'), save = page.getByRole('button', { name: /Save reason/ })
  const guard = await controlStability(page, { save, field, pin: inherited, head: page.locator('.bhead') })
  await guard.check(async () => { await save.click(); await expect(page.getByRole('dialog').getByRole('alert')).toContainText('One sentence'); expect(state.writes).toHaveLength(0) })
  await guard.check(() => field.fill('Keep this account for complex work'))
  await field.press('Enter'); expect(state.writes).toHaveLength(0)
  guard.done()
  const modifier = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent) ? 'Meta' : 'Control')
  await field.press(`${modifier}+Enter`)
  await expect(col(page, 'frontend').locator('[data-zone="bottom"] [data-line="openai:sol"]')).toBeVisible()
  expect(state.writes).toHaveLength(1); expect(state.writes[0]!.body).toMatchObject({ revision: 2, top: [{ line: boardPin.line, why: boardPin.why }], bottom: [{ line: 'openai:sol', why: 'Keep this account for complex work' }] })
  state.setFail(422)
  await card(page, 'openai:sol', 'frontend').press('Enter'); await page.getByRole('menuitem', { name: 'No rule', exact: true }).click()
  await expect(page.locator('.feedback [role="alert"]')).toContainText('looser_than_workspace')
  await expect(col(page, 'frontend').locator('[data-zone="bottom"] [data-line="openai:sol"]')).toBeVisible()
})

test('capabilities stay separate from locks, rules may name incapable models, and reviews keep the author family excluded', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  await expect(col(page, 'design').getByLabel('Can’t do this here')).toContainText('Grok')
  await expect(col(page, 'design').locator('[data-line="xai:grok"]')).toHaveCount(0)
  await expect(col(page, 'review:openai').locator('[data-zone="not"] [data-line="openai:sol"]')).toHaveAttribute('aria-disabled', 'true')
  await expect(col(page, 'review:openai').getByRole('link', { name: 'Cross-family review rule' })).toHaveAttribute('href', '/settings/policies')
  await layer(page, 'Workspace rules')
  await col(page, 'design').getByRole('button', { name: /column menu/ }).click(); await page.getByRole('menuitem', { name: 'Add a rule…', exact: true }).click(); await page.getByRole('menuitem', { name: 'Grok 4.7', exact: true }).click(); await page.getByRole('menuitem', { name: 'Pin to bottom', exact: true }).click()
  await page.getByPlaceholder('One sentence').fill('Use when tools become available'); await page.getByRole('button', { name: /Save reason/ }).click()
  await expect(col(page, 'design').locator('[data-zone="bottom"] [data-line="xai:grok"]')).toBeVisible()
  await expect(col(page, 'design')).toContainText('The rule is kept')
  expect(state.writes).toHaveLength(1)
})

test('new catalog lines stay unplaced until one order places them and the tray and board height stay still', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  await expect(page.locator('.board [data-line="openai:nova"]')).toHaveCount(0)
  const tray = page.locator('.tray [data-line="openai:nova"]')
  const guard = await controlStability(page, { ...headerControls(page), board: page.locator('[data-board-scroll]'), pin: card(page, boardPin.line, 'frontend'), tray })
  await guard.check(async () => { await tray.press('Enter'); await page.getByRole('menuitem', { name: 'Backend build', exact: true }).click(); await expect(col(page).locator('[data-zone="list"] li').last()).toHaveAttribute('data-card', 'openai:nova') })
  expect(state.writes).toHaveLength(1); expect(state.writes[0]!.body.rank).toEqual(['openai:sol', 'anthropic:sonnet', 'anthropic:opus', 'openai:astra', 'anthropic:fable', 'openai:nova']); guard.done()
})

test('column visibility persists and restores fixed ordering while mandatory columns stay visible', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  const originalOrder = await page.locator('[data-column]').evaluateAll(elements => elements.map(element => element.getAttribute('data-column')))
  await col(page).getByRole('button', { name: /column menu/ }).click(); await page.getByRole('menuitem', { name: /^Hide this column/ }).click()
  await expect(col(page)).toHaveCount(0); expect(state.writes[0]!.body).toMatchObject({ hidden_kinds: ['backend'], revision: 3 })
  await expect(col(page, 'other').getByRole('button', { name: /column menu/ })).toHaveCount(0)
  await page.locator('[data-board-columns]').click(); await page.getByRole('menuitem', { name: 'Backend build', exact: true }).click()
  await expect(col(page)).toBeVisible(); expect(await page.locator('[data-column]').evaluateAll(elements => elements.map(element => element.getAttribute('data-column')))).toEqual(originalOrder)
  expect(state.writes).toHaveLength(2)
})

test('failed writes are honest, old Undo cannot overwrite a later edit, and an identity change discards a held result', async ({ page }) => {
  const state = await mockBoard(page, { fail: 409 }); await open(page)
  await card(page, 'anthropic:sonnet').press('Alt+ArrowUp'); await expect(page.getByRole('alert')).toContainText('not saved'); await expect(page.locator('.test-toasts button')).toHaveCount(0)
  state.setFail()
  await card(page, 'anthropic:sonnet').press('Alt+ArrowUp'); await expect(page.locator('.test-toasts button')).toHaveCount(1)
  const undo = page.locator('.test-toasts button').first()
  await card(page, 'anthropic:fable').press('Alt+ArrowDown'); await expect(page.locator('.test-toasts button')).toHaveCount(2)
  await undo.click(); expect(state.writes).toHaveLength(3); await expect(page.locator('.test-toasts')).toContainText('Undo is no longer available')
  const release = state.holdNext()
  await card(page, 'openai:sol').press('Alt+ArrowDown'); await expect.poll(() => state.writes.length).toBe(4)
  await page.locator('[data-change-person]').click(); await expect(page.locator('.test-toasts button')).toHaveCount(0)
  const reads = state.reads.length; release(); await expect(page.locator('[data-model-board]')).toHaveAttribute('aria-busy', 'false')
  expect(state.reads.length).toBe(reads); await expect(page.locator('.test-toasts button')).toHaveCount(0)
})

test('template preview writes no settings, shows Now to After, keeps personal columns and applies with guarded Undo', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  await page.locator('[data-template]').click(); await expect(page.getByRole('dialog')).toContainText('Now → After')
  expect(state.writes).toHaveLength(1); expect(state.writes[0]!.body).toEqual({ template: 'best', revision: 3 }); expect(state.writes[0]!.path).toBe('/model-preferences/profile')
  await expect(page.getByRole('dialog')).toContainText('Columns you ordered yourself stay.')
  const guard = await controlStability(page, { apply: page.getByRole('button', { name: /^Apply/ }), head: page.locator('.bhead') })
  await guard.check(() => page.getByRole('button', { name: 'Cancel', exact: true }).focus()); guard.done()
  await page.getByRole('button', { name: /^Apply/ }).click(); await expect(page.getByRole('dialog')).toHaveCount(0)
  expect(state.writes).toHaveLength(2); expect(state.writes[1]!.body).toEqual({ template: 'best', revision: 3 })
  await page.getByRole('button', { name: 'Undo', exact: true }).click(); await expect.poll(() => state.writes.length).toBe(3)
  expect(state.writes[2]!.body).toEqual({ template: null, revision: 4 })
})

test('agent readers can inspect all layers but have no write controls or move actions', async ({ page }) => {
  const state = await mockBoard(page, { manage: false }); await open(page, '?agent=1')
  await expect(page.locator('[data-board-columns]')).toHaveCount(0); await expect(page.locator('.bc-r1 button')).toHaveCount(0)
  await card(page, 'anthropic:sonnet').press('Alt+ArrowUp'); expect(state.writes).toHaveLength(0)
  await layer(page, 'Workspace default'); await expect(page.locator('.layer-note')).toContainText('Read only')
  await layer(page, 'Workspace rules'); await expect(col(page).locator('[data-zone]')).toHaveCount(4)
  await expect(col(page).locator('[data-line="openai:sol"]')).toHaveAttribute('aria-disabled', 'true'); expect(state.writes).toHaveLength(0)
})

test('provider writes keep selectors still and support Undo', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  const guard = await controlStability(page, headerControls(page))
  await guard.check(async () => { await page.locator('[data-board-project]').click(); await page.getByRole('menuitem', { name: 'AEON', exact: true }).click(); await expect(page.locator('[data-board-project] .select-value > span').first()).toHaveText('AEON'); await expect(page.locator('[data-board-ready="true"]')).toBeVisible() })
  await guard.check(async () => { await page.locator('[data-board-project]').click(); await page.getByRole('menuitem', { name: 'Any project', exact: true }).click(); await expect(page.locator('[data-board-project] .select-value > span').first()).toHaveText('Any project'); await expect(page.locator('[data-board-ready="true"]')).toBeVisible() })
  expect(state.writes).toHaveLength(0)
  await guard.check(async () => { await page.locator('[data-board-providers]').click(); await page.getByRole('menuitem', { name: 'EU-hosted only', exact: true }).click(); await expect(page.locator('[data-board-providers]')).toContainText('EU-hosted only') })
  expect(state.writes).toHaveLength(1); expect(state.writes[0]).toMatchObject({ person: boardPerson, body: { residency: 'eu', revision: 3 } })
  await guard.check(async () => { await page.getByRole('button', { name: 'Undo', exact: true }).click(); await expect(page.locator('[data-board-providers]')).toContainText('As the workspace') }); guard.done()
  expect(state.writes[1]!.body).toEqual({ residency: null, revision: 4 })
})

test('an empty eligible list explains the provider wait and keeps held cards locked', async ({ page }) => {
  const state = await mockBoard(page, { noRoute: true }); await open(page)
  await expect(page.getByRole('status').filter({ hasText: 'no route qualifies today' })).toBeVisible()
  await expect(col(page).locator('[data-zone="list"]')).toBeEmpty()
  await card(page, 'openai:sol').press('Alt+ArrowUp'); expect(state.writes).toHaveLength(0)
})

test('full screen is a route with inert surroundings; popovers own Esc and Done restores scroll and focus', async ({ page }) => {
  await mockBoard(page); await open(page)
  const main = page.locator('.app-shell > main')
  await main.evaluate(element => { element.scrollTop = 60 })
  await page.locator('[data-board-scroll]').evaluate(element => { element.scrollLeft = 120 })
  const top = await main.evaluate(element => element.scrollTop)
  await page.locator('[data-models-fullscreen]').click(); await expect(page).toHaveURL(/\/settings\/models\/board/)
  await expect(page.locator('.fullboard')).toBeVisible(); await expect(page.locator('dialog')).toHaveCount(0)
  expect(await main.evaluate(element => (element as HTMLElement).inert)).toBe(true)
  const guard = await controlStability(page, { done: page.locator('[data-board-done]'), selectors: page.locator('.fullboard .bhead'), frame: page.locator('.fullboard') })
  await guard.check(async () => { await card(page, 'openai:sol').press('Enter'); await expect(page.getByRole('menu')).toBeVisible(); await page.keyboard.press('Escape'); await expect(page.getByRole('menu')).toHaveCount(0) }); guard.done()
  await page.locator('[data-board-done]').click(); await expect(page).toHaveURL(/\/settings\/models\?/)
  await expect(page.locator('[data-models-fullscreen]')).toBeFocused()
  expect(await main.evaluate(element => element.scrollTop)).toBe(top)
  expect(await page.locator('[data-board-scroll]').evaluate(element => element.scrollLeft)).toBe(120)
  expect(await main.evaluate(element => (element as HTMLElement).inert)).toBe(false)
  await page.locator('[data-models-fullscreen]').click(); await expect(page.locator('.fullboard [data-board-ready="true"]')).toBeVisible(); await page.keyboard.press('Escape'); await expect(page.locator('[data-models-fullscreen]')).toBeFocused()
})

test('rules Move to appends a second pin instead of inserting it ahead of pins already there', async ({ page }) => {
  const state = await mockBoard(page); await open(page); await layer(page, 'Workspace rules')
  async function pin(line: string, why: string) {
    await card(page, line).press('Enter')
    await page.getByRole('menuitem', { name: 'Pin to top', exact: true }).click()
    await page.getByPlaceholder('One sentence').fill(why)
    await page.getByRole('button', { name: /Save reason/ }).click()
    await expect(col(page).locator(`[data-zone="top"] [data-line="${line}"]`)).toBeVisible()
  }
  await pin('openai:sol', 'First account for this column')
  await pin('anthropic:sonnet', 'Second account stays behind the first')
  const tops = state.writes.map(write => (write.body.top as { line: string }[]).map(pin => pin.line))
  expect(tops).toEqual([['openai:sol'], ['openai:sol', 'anthropic:sonnet']])
})
