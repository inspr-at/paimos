// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { mockModelsSettings, openModelsSettings } from './models-settings-page'
import { boardPerson } from './models-board-fixtures'
import { controlStability } from './control-stability'
const panel = (page: Page) => page.locator('.pane')
const next = (page: Page) => page.locator('[data-setup-next]')
async function forward(page: Page) { await next(page).click() }
async function setup(page: Page) { await page.locator('[data-models-setup]').click(); await expect(panel(page).locator('legend')).toContainText(/Speed or quality|Tempo oder Qualität/) }

// Risk: five questions could shift actions, save more than once, erase own
// columns, or a late write/Undo could cross the person boundary.
test('guided setup preserves controls and own columns, saves once with exact Undo and discards stale writes', async ({ page }, testInfo) => {
  test.setTimeout(180_000)
  const state = await mockModelsSettings(page, { setup: true }); state.seedOrder('backend', ['anthropic:sonnet', 'openai:sol'])
  await openModelsSettings(page, '?mode=simple&identity=1')
  await setup(page)
  const controls = { frame: panel(page), back: page.locator('[data-setup-back]'), next: next(page), close: panel(page).locator('.pane-close') }
  const guard = await controlStability(page, controls)
  const options = await controlStability(page, { ...controls, group: page.locator('[data-setup-options]'), row: panel(page).locator('.option').first() })
  await options.check(async () => { await page.getByLabel('Quality first').check() }); options.done()
  await guard.check(async () => { await forward(page); await page.getByLabel('Max out my accounts').check() })
  await guard.check(async () => { await forward(page) })
  const rank = page.locator('[data-setup-rank]'), opus = rank.locator('[data-line="anthropic:opus"]')
  const rankGuard = await controlStability(page, { ...controls, group: rank, clickedRow: rank.locator('li').nth(1) })
  await rankGuard.check(async () => { await opus.press('Alt+ArrowUp'); await expect(opus).toBeFocused() }); rankGuard.done()
  const from = await opus.boundingBox(), to = await rank.locator('li').first().boundingBox()
  await guard.check(async () => { await page.mouse.move(from!.x + 30, from!.y + 20); await page.mouse.down(); await page.mouse.move(to!.x + 30, to!.y + 20, { steps: 8 }); await page.mouse.up(); await expect(rank.locator('li').first()).toHaveAttribute('data-setup-line', 'anthropic:opus') })
  await guard.check(async () => { await opus.press('Enter'); await page.getByRole('menuitem', { name: 'Move to the bottom', exact: true }).click(); await expect(rank.locator('li').last()).toHaveAttribute('data-setup-line', 'anthropic:opus') })
  await guard.check(async () => { await opus.click(); await page.getByRole('menuitem', { name: 'Move to the top', exact: true }).click(); await expect(rank.locator('li').first()).toHaveAttribute('data-setup-line', 'anthropic:opus') })
  await guard.check(async () => { await forward(page); await panel(page).locator('input[value="deep"]').check() })
  await guard.check(async () => { await forward(page); await page.getByLabel('Whatever the workspace allows').check() })
  await guard.check(async () => { await forward(page); await expect(page.locator('[data-setup-diff]')).toContainText('Now'); await expect(next(page)).toContainText('Apply') }); guard.done()
  const mutations = () => state.writes.filter(write => !write.dryRun)
  expect(mutations()).toHaveLength(0)
  await forward(page); await expect(panel(page)).toBeHidden(); await expect(page.locator('[data-models-setup]')).toBeFocused()
  expect(mutations()).toHaveLength(1)
  expect(mutations()[0]).toMatchObject({ path: '/model-preferences/profile', person: boardPerson, body: { template: 'best', usage: 'maxout', thinking: 'deep', residency: null, other_order: { rank: ['anthropic:opus', 'openai:sol', 'anthropic:sonnet', 'openai:astra', 'anthropic:fable'], not: [] }, revision: 3 } })
  await expect(page.locator('[data-column="backend"] [data-zone="list"] li').first()).toHaveAttribute('data-card', 'anthropic:sonnet')
  await page.getByRole('button', { name: 'Undo', exact: true }).click(); await expect.poll(() => mutations().length).toBe(2)
  expect(mutations()[1]!.body).toMatchObject({ template: null, usage: null, thinking: null, residency: null, other_order: null, revision: 4 })
  await setup(page); for (let step = 0; step < 5; step++) await forward(page)
  state.setFail(409); await forward(page); await expect(panel(page).locator('[role="alert"]')).toContainText('change was not saved')
  expect(mutations()).toHaveLength(3); await expect(page.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0)
  state.setFail(); const release = state.holdNext(); await forward(page); await expect.poll(() => mutations().length).toBe(4)
  state.setPerson('22222222-2222-4222-8222-222222222222'); await page.locator('[data-change-person]').click(); release()
  await expect(panel(page)).toBeHidden(); await expect(page.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0)

  // Evidence is captured from the same behaviour spec, never pixel assertions.
  for (const width of [1440, 1280, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1100 }); await page.emulateMedia({ colorScheme: theme })
    await page.addInitScript(value => { document.addEventListener('DOMContentLoaded', () => { document.documentElement.dataset.theme = value }) }, theme)
    await page.addInitScript(person => { localStorage.removeItem(`models-page/board-tenant/${person}/setup-visited`) }, boardPerson)
    await mockModelsSettings(page, { setup: true, german: true }); await openModelsSettings(page, '?mode=simple&lang=de')
    await expect(page.locator('[data-setup-welcome]')).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`welcome-${width}-${theme}-de.png`) })
    await setup(page)
    const evidence = await controlStability(page, { frame: panel(page), back: page.locator('[data-setup-back]'), next: next(page), close: panel(page).locator('.pane-close') })
    await evidence.check(async () => { await page.getByLabel('Qualität zuerst').check(); await forward(page); await forward(page) })
    await page.screenshot({ path: testInfo.outputPath(`rank-${width}-${theme}-de.png`) })
    await evidence.check(async () => { await page.locator('[data-setup-rank] [data-line="anthropic:opus"]').press('Alt+ArrowUp'); await forward(page); await forward(page); await forward(page); await expect(page.locator('[data-setup-diff]')).toBeVisible() }); evidence.done()
    await page.screenshot({ path: testInfo.outputPath(`diff-${width}-${theme}-de.png`) })
    await panel(page).locator('.pane-close').click(); await expect(page.locator('[data-models-setup]')).toBeFocused()
  }
  await mockModelsSettings(page, { setup: true, manage: false }); await openModelsSettings(page, '?agent=1')
  await expect(page.locator('[data-models-setup]')).toHaveCount(0)
})
