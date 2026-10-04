// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { expectStableControls } from './helpers/stable'

async function open(page: Page, width = 1440, german = false) {
  await page.setViewportSize({ width, height: 900 })
  await page.goto(`/tests/settings-shared-harness.html${german ? '?german' : ''}`)
  await expect(page.getByRole('heading', { name: '2 things need you' })).toBeVisible()
}
const writes = (page: Page) => page.getByLabel('Writes')
const pane = (page: Page) => page.getByRole('dialog', { name: /^(Arbeitscomputer mit ausführlichem Namen|Computer 2)$/ })
async function noOverflow(page: Page) { expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1) }

test('needs-you counts problems, keeps calm kinds and uses the container for narrow actions', async ({ page }) => {
  await open(page)
  await expect(page.locator('.att-row.calm')).toHaveText(/No missed releases/)
  await expect(page.locator('.att-row.calm button')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Verify again' })).toHaveClass(/primary/)
  await expect(page.getByRole('button', { name: 'Open cleanup' })).not.toHaveClass(/primary/)
  await page.locator('.dock-content').evaluate(el => { (el as HTMLElement).style.maxWidth = '520px' })
  const action = await page.getByRole('button', { name: 'Verify again' }).boundingBox()
  const text = await page.locator('.att-name').first().boundingBox()
  expect(action!.x).toBe(text!.x)
  expect(action!.y).toBeGreaterThan(text!.y + text!.height)
  expect(action!.height).toBeGreaterThanOrEqual(44)
  await page.getByRole('button', { name: 'Toggle empty' }).click()
  await expect(page.getByRole('heading', { name: 'Nothing needs you' })).toBeVisible()
  await expect(page.locator('.attention')).toHaveCount(0)
})

test('dock keeps navigation and the clicked row; sheets isolate focus and pin actions through content and series changes', async ({ page }) => {
  for (const width of [1440, 1232, 1231, 1024, 753, 752, 390]) {
    await open(page, width)
    const docked = width - 32 >= 1200
    const row = page.locator('[data-row="1"]')
    const nav = page.getByRole('button', { name: 'Accounts and computers', exact: true })
    // Opening a third column may deliberately compensate with page scroll.
    // This contract is pointer position, so sample viewport boxes here; the
    // reusable guard below uses scroll-container coordinates for pane edits.
    const beforeRow = (await row.boundingBox())!, beforeNav = (await nav.boundingBox())!
    await row.click()
    await expect(page.locator('.settings-frame')).toHaveClass(new RegExp(`mode-${docked ? 'dock' : width - 32 > 720 ? 'side' : 'sheet'}`))
    await expect(pane(page)).toBeVisible()
    await expect(pane(page).getByRole('heading')).toBeFocused()
    const afterRow = (await row.boundingBox())!, afterNav = (await nav.boundingBox())!
    for (const axis of ['x', 'y', 'width', 'height'] as const) expect(Math.abs(afterRow[axis] - beforeRow[axis]), `frame ${width - 32}: clicked row ${axis}`).toBeLessThanOrEqual(.5)
    for (const axis of ['x', 'width'] as const) expect(Math.abs(afterNav[axis] - beforeNav[axis]), `frame ${width - 32}: section column ${axis}`).toBeLessThanOrEqual(.5)
    await expect(page.locator('.dock-content')).toHaveJSProperty('inert', !docked)
    const controls = { close: page.getByRole('button', { name: 'Close details' }), next: page.getByRole('button', { name: 'Next computer' }), apply: page.getByRole('button', { name: 'Apply capacity' }), frame: pane(page), clickedRow: row }
    await expectStableControls({ controls, scrollAreas: { body: pane(page).locator('.pane-body') }, interactions: [
      { name: 'long content', run: async () => { await page.getByRole('button', { name: 'Toggle content' }).evaluate(el => (el as HTMLButtonElement).click()); await expect(pane(page).locator('.pane-body p')).toHaveCount(60) } },
      { name: 'next record', run: async () => { await controls.next.click(); await expect(controls.next).toBeFocused(); await expect(pane(page).locator('.pane-body p')).toHaveCount(2) } },
    ] })
    const input = pane(page).getByRole('textbox')
    await input.focus(); await page.keyboard.press('Escape')
    await expect(pane(page)).toBeVisible(); await expect(pane(page).getByRole('heading')).toBeFocused()
    if (!docked) {
      await controls.apply.focus(); await page.keyboard.press('Tab'); await expect(page.getByRole('button', { name: 'More actions' })).toBeFocused()
    }
    await page.keyboard.press('Escape')
    await expect(pane(page)).toHaveCount(0); await expect(row).toBeFocused()
    await expect(page.locator('.dock-content')).toHaveJSProperty('inert', false)
    if (!docked && width - 32 > 720) {
      await row.click(); await page.locator('.dock').click({ position: { x: 8, y: 8 } })
      await expect(pane(page)).toHaveCount(0); await expect(row).toBeFocused()
    }
    await noOverflow(page)
  }
})

test('popover menus navigate, confirm destructive effects and return focus without moving the trigger', async ({ page }) => {
  await open(page)
  const trigger = page.getByRole('button', { name: 'Page menu', exact: true })
  await expectStableControls({ controls: { trigger }, interactions: [{ name: 'open menu', run: () => trigger.click() }, { name: 'confirm removal', run: () => page.getByRole('menuitem', { name: 'Remove computer…' }).click() }] })
  const confirm = page.getByRole('dialog', { name: 'Remove this computer?' })
  await expect(confirm).toContainText('New work stops and its sign-ins are blocked.')
  await expect(confirm).toContainText('Accounts on other computers keep working.')
  await expect(writes(page)).toHaveText('0')
  await confirm.getByRole('button', { name: 'Cancel' }).click(); await expect(trigger).toBeFocused()
  await trigger.click(); await expect(page.getByRole('menuitem', { name: 'Read quota now' })).toBeFocused()
  await page.keyboard.press('End'); await expect(page.getByRole('menuitem', { name: 'Remove computer…' })).toBeFocused()
  await page.keyboard.press('ArrowDown'); await expect(page.getByRole('menuitem', { name: 'Read quota now' })).toBeFocused()
  await page.keyboard.press('Home'); await page.keyboard.press('ArrowUp'); await expect(page.getByRole('menuitem', { name: 'Remove computer…' })).toBeFocused()
  await page.keyboard.press('Enter'); await confirm.getByRole('button', { name: 'Remove computer', exact: true }).click()
  await expect(writes(page)).toHaveText('1'); await expect(trigger).toBeFocused()
})

test('forms retain failed writes, reserve feedback and respect platform submit and two-step Escape', async ({ page }) => {
  await open(page)
  const trigger = page.getByRole('button', { name: 'Change quota' })
  await trigger.click()
  const form = page.getByRole('dialog', { name: 'Quota warnings' })
  await form.getByRole('textbox').fill('fail')
  const save = form.getByRole('button', { name: /^Save/ })
  await expectStableControls({ controls: { save, cancel: form.getByRole('button', { name: 'Cancel' }), trigger }, interactions: [{ name: 'failed save', run: async () => { await save.click(); await expect(form).toContainText('Could not save. Try again.') } }] })
  await expect(writes(page)).toHaveText('0')
  const mac = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent))
  const input = form.getByRole('textbox')
  await input.fill('20'); await page.keyboard.press(mac ? 'Control+Enter' : 'Meta+Enter')
  await expect(writes(page)).toHaveText('0')
  await page.keyboard.press(mac ? 'Meta+Enter' : 'Control+Enter')
  await expect(writes(page)).toHaveText('1'); await expect(trigger).toBeFocused()
  await trigger.click(); await input.fill('pending'); await save.click()
  await expect(save).toBeDisabled(); await expect(form).toBeVisible()
  await page.keyboard.press(mac ? 'Meta+Enter' : 'Control+Enter'); await expect(writes(page)).toHaveText('1')
  await page.getByRole('button', { name: 'Change identity' }).evaluate(el => (el as HTMLButtonElement).click())
  await expect(form).toHaveCount(0); await expect(writes(page)).toHaveText('1')
  await trigger.click(); await input.focus(); await page.keyboard.press('Escape')
  await expect(form).toBeVisible(); await expect(input).not.toBeFocused()
  await page.keyboard.press('Escape'); await expect(form).toHaveCount(0); await expect(trigger).toBeFocused()
})

test('one popover owns the frame and closes for outside presses, moved triggers, resize and identity changes', async ({ page }) => {
  await open(page)
  const trigger = page.getByRole('button', { name: 'Page menu', exact: true })
  await trigger.click(); await page.getByRole('button', { name: 'Other menu', exact: true }).click()
  await expect(page.getByRole('menu', { name: 'Other actions' })).toBeVisible(); await expect(page.locator('.popover')).toHaveCount(1)
  await page.locator('.needs-head').click(); await expect(page.locator('.popover')).toHaveCount(0)
  await trigger.click(); await page.setViewportSize({ width: 1300, height: 900 }); await expect(page.locator('.popover')).toHaveCount(0)
  await trigger.click(); await page.evaluate(() => window.scrollBy(0, 100)); await expect(page.locator('.popover')).toHaveCount(0)
  await trigger.click(); await page.getByRole('button', { name: 'Change identity' }).evaluate(el => (el as HTMLButtonElement).click()); await expect(page.locator('.popover')).toHaveCount(0)
  await page.locator('[data-row="1"]').click()
  await page.getByRole('button', { name: 'Accounts and computers', exact: true }).click()
  await expect(pane(page)).toHaveCount(0)
})

test('approved shared shapes fit long German copy at phone, tablet and desktop in both themes', async ({ page }) => {
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
    await open(page, width, true)
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await noOverflow(page)
    await page.screenshot({ path: `test-results/aeon-695/needs-${width}-${theme}.png` })
    await page.locator('[data-row="1"]').click()
    await page.getByRole('button', { name: 'Toggle content' }).evaluate(el => (el as HTMLButtonElement).click())
    await expect(pane(page).locator('.pane-body p')).toHaveCount(60)
    await noOverflow(page)
    await page.screenshot({ path: `test-results/aeon-695/panel-${width}-${theme}.png` })
    await page.getByRole('button', { name: 'Apply capacity' }).click()
    const pop = page.getByRole('dialog', { name: 'Warnschwellen für Kontingente ändern' })
    await expect(pop).toBeVisible()
    const r = await pop.boundingBox(), frame = await page.locator('.settings-frame').boundingBox()
    expect(r!.x).toBeGreaterThanOrEqual(frame!.x + 12 - .5)
    expect(r!.x + r!.width).toBeLessThanOrEqual(frame!.x + frame!.width - 12 + .5)
    expect(r!.y).toBeGreaterThanOrEqual(12)
    expect(r!.y + r!.height).toBeLessThanOrEqual(888)
    await page.screenshot({ path: `test-results/aeon-695/form-${width}-${theme}.png` })
    await pop.getByRole('button', { name: 'Cancel' }).click()
    await page.getByRole('button', { name: 'More actions' }).click()
    await page.screenshot({ path: `test-results/aeon-695/menu-${width}-${theme}.png` })
    await page.getByRole('menuitem', { name: 'Remove computer…' }).click()
    await page.screenshot({ path: `test-results/aeon-695/confirm-${width}-${theme}.png` })
  }
})
