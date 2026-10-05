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


test('modal scroll lock preserves the already-scrolled shell and scrollbar geometry', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 900 })
  await page.goto('/tests/settings-shared-harness.html?scroll-shell')
  const shell = page.locator('main')
  const row = page.locator('[data-row="1"]')
  await expect(row).toBeVisible()
  await shell.evaluate(el => { el.scrollTop = 140 })
  const before = await shell.evaluate(el => ({ top: el.scrollTop, width: el.clientWidth }))
  expect(before.top).toBe(140)
  const rowBefore = (await row.boundingBox())!
  await row.click()
  await expect(pane(page)).toBeVisible()
  expect(await shell.evaluate(el => el.scrollTop)).toBe(before.top)
  expect(await shell.evaluate(el => el.clientWidth)).toBe(before.width)
  expect((await row.boundingBox())!.y).toBeCloseTo(rowBefore.y, 1)
  await page.getByRole('button', { name: 'Close details' }).click()
  await expect(pane(page)).toHaveCount(0)
  await expect(row).toBeFocused()
  expect(await shell.evaluate(el => el.scrollTop)).toBe(before.top)
  expect(await shell.evaluate(el => el.clientWidth)).toBe(before.width)
  expect((await row.boundingBox())!.y).toBeCloseTo(rowBefore.y, 1)
})

test('nested popovers contain forward and reverse Tab in menus, confirmations and forms', async ({ page }) => {
  await open(page, 390)
  await page.locator('[data-row="1"]').click()
  await page.getByRole('button', { name: 'More actions' }).click()
  const menu = page.getByRole('menu', { name: 'Computer actions' })
  const first = menu.getByRole('menuitem', { name: 'Read quota now' })
  const last = menu.getByRole('menuitem', { name: 'Remove computer…' })
  await first.focus(); await page.keyboard.press('Shift+Tab'); await expect(last).toBeFocused()
  await page.keyboard.press('Tab'); await expect(first).toBeFocused()
  await page.keyboard.press('Tab'); await expect(last).toBeFocused()
  await last.click()
  const confirm = page.getByRole('dialog', { name: 'Remove this computer?' })
  const cancel = confirm.getByRole('button', { name: 'Cancel' })
  const remove = confirm.getByRole('button', { name: 'Remove computer', exact: true })
  await cancel.focus(); await page.keyboard.press('Shift+Tab'); await expect(remove).toBeFocused()
  await page.keyboard.press('Tab'); await expect(cancel).toBeFocused()
  await cancel.click()
  await page.getByRole('button', { name: 'Apply capacity' }).click()
  const form = page.getByRole('dialog', { name: 'Quota warnings' })
  const input = form.getByRole('textbox'), save = form.getByRole('button', { name: /^Save/ })
  await input.focus(); await page.keyboard.press('Shift+Tab'); await expect(save).toBeFocused()
  await page.keyboard.press('Tab'); await expect(input).toBeFocused()
  await page.keyboard.press('Tab'); await expect(form.getByRole('button', { name: 'Cancel' })).toBeFocused()
  await page.keyboard.press('Tab'); await expect(save).toBeFocused()
})

for (const platform of ['MacIntel', 'Linux x86_64']) test(`plain Enter never submits staged fields on ${platform}`, async ({ page }) => {
  await page.addInitScript(platform => { Object.defineProperty(navigator, 'platform', { value: platform }) }, platform)
  await open(page)
  await page.getByRole('button', { name: 'Change quota' }).click()
  const form = page.getByRole('dialog', { name: 'Quota warnings' })
  await form.getByRole('textbox').fill('20')
  await page.keyboard.press('Enter')
  await expect(form).toBeVisible(); await expect(writes(page)).toHaveText('0')
  await page.keyboard.press(platform === 'MacIntel' ? 'Meta+Enter' : 'Control+Enter')
  await expect(writes(page)).toHaveText('1')
  await page.getByRole('button', { name: 'Change quota' }).click()
  await form.getByRole('button', { name: /^Save/ }).focus(); await page.keyboard.press('Enter')
  await expect(writes(page)).toHaveText('2')
})

for (const orientation of ['down', 'up']) for (const kind of ['form', 'confirmation']) test(`multiline ${kind} errors keep ${orientation}-opening actions stationary`, async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/tests/settings-shared-harness.html?fail-confirm')
  if (orientation === 'up') await page.locator('[data-row="1"]').click()
  if (kind === 'form') {
    await page.getByRole('button', { name: orientation === 'up' ? 'Apply capacity' : 'Change quota' }).click()
    await page.getByRole('dialog', { name: 'Quota warnings' }).getByRole('textbox').fill('fail-long')
  } else {
    await page.getByRole('button', { name: orientation === 'up' ? 'Footer menu' : 'Page menu', exact: true }).click()
    await page.getByRole('menuitem', { name: 'Remove computer…' }).click()
  }
  const dialog = page.getByRole('dialog', { name: kind === 'form' ? 'Quota warnings' : 'Remove this computer?' })
  const action = dialog.getByRole('button', { name: kind === 'form' ? /^Save/ : 'Remove computer', exact: kind !== 'form' })
  expect(await dialog.evaluate(el => el.style.top === 'auto')).toBe(orientation === 'up')
  await expectStableControls({ controls: { action, cancel: dialog.getByRole('button', { name: 'Cancel' }), ...(kind === 'form' ? { input: dialog.getByRole('textbox') } : {}) }, scrollAreas: { feedback: dialog }, interactions: [
    { name: 'multiline failure', run: async () => { await action.click(); await expect(dialog).toContainText('The computer could not be reached'); await expect(writes(page)).toHaveText('0') } },
  ] })
  const feedback = dialog.locator('.qhint.err, [role="alert"]')
  await expect(feedback).toBeVisible()
  expect(await feedback.evaluate(el => el.getBoundingClientRect().height)).toBeGreaterThan(60)
  await action.click(); await expect(writes(page)).toHaveText('0'); await expect(dialog).toBeVisible()
})

for (const orientation of ['down', 'up']) test(`tall ${orientation}-opening popovers stay separated from their trigger through keyboard viewport changes`, async ({ page }) => {
  await page.addInitScript(() => {
    const viewport = new EventTarget()
    Object.assign(viewport, { width: 390, height: 900, offsetTop: 0, offsetLeft: 0, scale: 1 })
    Object.defineProperty(window, 'visualViewport', { value: viewport, configurable: true })
  })
  await page.setViewportSize({ width: 390, height: 900 })
  await page.goto('/tests/settings-shared-harness.html?overflow-content')
  const trigger = page.getByRole('button', { name: orientation === 'up' ? 'Apply capacity' : 'Change quota' })
  if (orientation === 'up') await page.locator('[data-row="1"]').click()
  else await trigger.evaluate(el => {
    // Keep the page trigger within the keyboard viewport; the footer trigger
    // follows the sheet's visual viewport naturally in the upward case.
    Object.assign((el as HTMLElement).style, { position: 'fixed', top: '100px', right: '24px' })
  })
  await trigger.click()
  const form = page.getByRole('dialog', { name: 'Quota warnings' })
  await form.getByRole('textbox', { name: 'Early notice' }).focus()
  for (const viewport of [{ height: 900, offsetTop: 0 }, { height: 480, offsetTop: 0 }, { height: 300, offsetTop: 40 }, { height: 900, offsetTop: 0 }]) {
    await page.evaluate(viewport => {
      Object.assign(window.visualViewport!, viewport)
      window.visualViewport!.dispatchEvent(new Event('resize'))
      window.visualViewport!.dispatchEvent(new Event('scroll'))
    }, viewport)
    await expect.poll(() => form.evaluate(el => el.style.getPropertyValue('--vv-h'))).toBe(`${viewport.height}px`)
    const separation = async () => {
      const panel = (await form.boundingBox())!, anchor = (await trigger.boundingBox())!
      return orientation === 'up' ? anchor.y - panel.y - panel.height : panel.y - anchor.y - anchor.height
    }
    await expect.poll(separation, `trigger-facing edge at visual height ${viewport.height}`).toBeCloseTo(4, 0)
    expect(await form.evaluate(el => el.style.top === 'auto')).toBe(orientation === 'up')
    const panel = (await form.boundingBox())!
    expect(panel.height).toBeGreaterThan(0)
    expect(panel.y).toBeGreaterThanOrEqual(viewport.offsetTop + 12 - .5)
    expect(panel.y + panel.height).toBeLessThanOrEqual(viewport.offsetTop + viewport.height - 12 + .5)
    await expect(form.getByRole('textbox', { name: 'Early notice' })).toBeFocused()
  }
})

test('phone sheet and popover actions follow a keyboard-shrunken visual viewport', async ({ page }) => {
  await page.addInitScript(() => {
    const viewport = new EventTarget()
    Object.assign(viewport, { width: 390, height: 900, offsetTop: 0, offsetLeft: 0, scale: 1 })
    Object.defineProperty(window, 'visualViewport', { value: viewport, configurable: true })
  })
  await open(page, 390)
  await page.locator('[data-row="1"]').click()
  await page.getByRole('button', { name: 'Apply capacity' }).click()
  const form = page.getByRole('dialog', { name: 'Quota warnings' })
  await form.getByRole('textbox').focus()
  for (const viewport of [{ height: 480, offsetTop: 0 }, { height: 430, offsetTop: 50 }]) {
    await page.evaluate(viewport => {
      Object.assign(window.visualViewport!, viewport)
      window.visualViewport!.dispatchEvent(new Event('resize'))
      window.visualViewport!.dispatchEvent(new Event('scroll'))
    }, viewport)
    await expect(form).toBeVisible()
    await expect.poll(async () => (await pane(page).boundingBox())!.y + (await pane(page).boundingBox())!.height).toBeLessThanOrEqual(viewport.offsetTop + viewport.height + .5)
    const sheet = (await pane(page).boundingBox())!, popover = (await form.boundingBox())!
    expect(sheet.y).toBeGreaterThanOrEqual(viewport.offsetTop - .5)
    expect(popover.y).toBeGreaterThanOrEqual(viewport.offsetTop + 12 - .5)
    expect(popover.y + popover.height).toBeLessThanOrEqual(viewport.offsetTop + viewport.height - 12 + .5)
    const save = (await form.getByRole('button', { name: /^Save/ }).boundingBox())!
    expect(save.y + save.height).toBeLessThanOrEqual(viewport.offsetTop + viewport.height + .5)
    await expect(form.getByRole('textbox')).toBeFocused()
  }
  await form.getByRole('textbox').fill('20')
  await form.getByRole('button', { name: /^Save/ }).click()
  await expect(writes(page)).toHaveText('1')
})

for (const height of [900, 300]) for (const kind of ['form', 'confirmation']) test(`overflowing ${kind} content scrolls with pinned actions at visual height ${height}`, async ({ page }) => {
  await page.addInitScript(() => {
    const viewport = new EventTarget()
    Object.assign(viewport, { width: 390, height: 900, offsetTop: 0, offsetLeft: 0, scale: 1 })
    Object.defineProperty(window, 'visualViewport', { value: viewport, configurable: true })
  })
  await page.setViewportSize({ width: 390, height: 900 })
  await page.goto('/tests/settings-shared-harness.html?overflow-content&fail-confirm&german')
  await page.locator('[data-row="1"]').click()
  if (kind === 'form') {
    await page.getByRole('button', { name: 'Apply capacity' }).click()
    await page.getByRole('dialog', { name: 'Warnschwellen für Kontingente ändern' }).getByRole('textbox', { name: 'Early notice' }).fill('fail-long')
  } else {
    await page.getByRole('button', { name: 'Footer menu' }).click()
    await page.getByRole('menuitem', { name: 'Remove computer…' }).click()
  }
  const dialog = page.getByRole('dialog', { name: kind === 'form' ? 'Warnschwellen für Kontingente ändern' : 'Remove this computer?' })
  const action = dialog.getByRole('button', { name: kind === 'form' ? /^Save/ : 'Remove computer', exact: kind !== 'form' })
  const cancel = dialog.getByRole('button', { name: 'Cancel' })
  const offsetTop = height === 900 ? 0 : 40
  await page.evaluate(({ height, offsetTop }) => {
    Object.assign(window.visualViewport!, { height, offsetTop })
    window.visualViewport!.dispatchEvent(new Event('resize'))
    window.visualViewport!.dispatchEvent(new Event('scroll'))
  }, { height, offsetTop })
  await expect.poll(() => dialog.evaluate(el => el.style.getPropertyValue('--vv-h'))).toBe(`${height}px`)
  await expect.poll(async () => (await dialog.boundingBox())!.y + (await dialog.boundingBox())!.height).toBeLessThanOrEqual(offsetTop + height - 12 + .5)
  await expect.poll(async () => (await dialog.boundingBox())!.y).toBeGreaterThanOrEqual(offsetTop + 12 - .5)
  // Visibility alone accepts controls clipped by an overflow:hidden ancestor.
  // Require the whole action row to fit inside both panel and visual viewport.
  const frame = (await dialog.boundingBox())!
  for (const control of [action, cancel]) {
    const box = (await control.boundingBox())!
    expect(box.y, 'action starts inside panel').toBeGreaterThanOrEqual(frame.y)
    expect(box.y + box.height, 'action ends inside panel').toBeLessThanOrEqual(frame.y + frame.height + .5)
    expect(box.y + box.height, 'action ends above keyboard').toBeLessThanOrEqual(offsetTop + height - 12 + .5)
    await expect(control).toBeInViewport({ ratio: 1 })
  }
  const body = dialog.locator('.staged-body')
  await expect.poll(() => body.evaluate(el => el.scrollHeight - el.clientHeight)).toBeGreaterThan(0)
  await expectStableControls({ controls: { action, cancel, frame: dialog }, scrollAreas: { content: body }, interactions: [
    { name: 'scroll all copy and fields', run: async () => {
      await body.evaluate(el => { el.scrollTop = el.scrollHeight })
      expect(await body.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
      if (kind === 'form') await expect(dialog.getByRole('textbox', { name: 'Weitere Warnschwelle 8' })).toBeInViewport({ ratio: 1 })
      else await expect(dialog.locator('.keeps')).toBeInViewport()
    } },
    { name: 'failed write keeps actions pinned', run: async () => {
      await action.click()
      await expect(writes(page)).toHaveText('0')
      await expect(dialog.getByRole('alert')).toContainText('The computer could not be reached')
    } },
    { name: 'read full failure', run: async () => {
      await body.evaluate(el => { el.scrollTop = el.scrollHeight })
      await expect(dialog.getByRole('alert')).toBeInViewport()
      expect(await body.evaluate(el => Math.abs(el.scrollHeight - el.clientHeight - el.scrollTop))).toBeLessThanOrEqual(1)
    } },
  ] })
  for (const theme of ['light', 'dark']) {
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await page.screenshot({ path: `test-results/aeon-695/overflow-${kind}-${height}-${theme}.png` })
  }
  await action.focus(); await page.keyboard.press('Tab')
  if (kind === 'form') await expect(dialog.getByRole('textbox', { name: 'Early notice' })).toBeFocused()
  else await expect(cancel).toBeFocused()
  await cancel.click(); await expect(dialog).toHaveCount(0)
  await expect(page.getByRole('button', { name: kind === 'form' ? 'Apply capacity' : 'Footer menu' })).toBeFocused()
})
