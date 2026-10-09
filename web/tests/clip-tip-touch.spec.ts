// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

import { mockTicketGraph, ticketGraphWorld } from './ticket-graph-fixtures'
import { expectStableControls } from './helpers/stable'
import { longName } from './clip-tip-fixtures'
import { shots, touchShots, tableShots, noOverflow, pauseForGesture, setupHarness } from './clip-tip-playwright-fixtures'

test('touch selection reveals the same full graph card after leaving ticket details', async ({ browser }) => {
  test.setTimeout(60_000)
  for (const width of [390, 768, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
    const context = await browser.newContext({ viewport: { width, height: 1000 }, hasTouch: true, colorScheme: theme, reducedMotion: 'reduce' })
    const page = await context.newPage()
    const errors = watchErrors(page)
    try {
      const world = ticketGraphWorld()
      world.work.preferences.theme = { choice: theme }
      const node = { ...world.graph.nodes[1]!, title: longName }
      world.graph = { nodes: [node], links: [], truncated: false }
      world.work.nodes.find(row => row.id === node.id)!.title = longName
      await mockTicketGraph(page, world)
      await page.goto('/p/PHAROS/tickets?view=graph')
      const canvas = page.locator('.ticket-graph-canvas')
      await expect(canvas).toHaveAttribute('data-ready', 'true')
      await page.getByRole('button', { name: 'Fit graph to view' }).click()
      const box = (await canvas.boundingBox())!
      await page.touchscreen.tap(box.x + box.width / 2, box.y + box.height / 2)
      await expect(page).toHaveURL(/PHAROS-101\?view=graph/)
      await page.mouse.move(1, 1)
      await page.keyboard.press('Escape')
      await expect(page).toHaveURL('/p/PHAROS/tickets?view=graph')
      await expect(page.locator('.tg-tooltip')).toContainText(longName)
      await noOverflow(page)
      // The graph fills the viewport; full-page capture resizes that viewport
      // and feeds its ResizeObserver rather than capturing the user's layout.
      await page.screenshot({ path: `${shots}/ticket-graph-${width}-${theme}.png` })
      await expectStableControls({ controls: { fit: page.getByRole('button', { name: 'Fit graph to view' }) }, interactions: [{ name: 'clear selection', run: async () => { await canvas.press('Escape'); await expect(page.locator('.tg-tooltip')).toHaveCount(0) } }] })
      expect(errors).toEqual([])
    } finally { await context.close() }
  }
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test.describe(`touch fix5: ${width}px ${theme}`, () => {
    test.use({ viewport: { width, height: 1000 }, colorScheme: theme, hasTouch: true })

    test('tap activates a list row and never reveals its clipped name', async ({ page }) => {
      const errors = watchErrors(page)
      await setupHarness(page, theme)
      const name = page.locator('.standalone')
      const row = page.locator('.child-row').first()
      await expectStableControls({ controls: { name, row, change: page.getByRole('button', { name: 'Change name' }) }, interactions: [
        { name: 'tap standalone text', run: async () => { await name.tap(); await expect(page.locator('.tooltip')).toHaveCount(0) } },
        { name: 'tap opens the child on the first try', run: async () => {
          await row.tap()
          await expect(page.getByRole('status')).toHaveText('PHAROS-11')
          await expect(page.locator('.tooltip')).toHaveCount(0)
        } },
      ] })
      await noOverflow(page)
      await page.screenshot({ path: `${touchShots}/tap-${width}-${theme}.png` })
      expect(errors).toEqual([])
    })

    test('long press reads a production epic picker before a tap selects and closes it', async ({ page }) => {
      const errors = watchErrors(page)
      const data = fixtures(); data.preferences.theme = { choice: theme }
      data.nodes.find(node => node.id === 'n-epic')!.title = longName
      const calls = await mockWork(page, data)
      await page.goto('/p/PHAROS/PHAROS-14')
      const workspace = page.getByRole('complementary', { name: 'Ticket details' })
      await workspace.getByRole('button', { name: 'No parent. Choose a parent' }).tap()
      const panel = page.getByRole('dialog', { name: 'Parent for PHAROS-14' })
      const row = panel.getByRole('option')
      await expect(row.locator('.title')).toHaveAttribute('data-tip', longName)
      // Clear autofocus disclosure through ordinary touch input before the
      // hold. Older hosts must reach the missing 500 ms disclosure assertion,
      // rather than fail because the search's initial focus already showed it.
      await panel.getByText('Parent', { exact: true }).tap()
      await expect(page.locator('.tooltip')).toHaveCount(0)
      const writes = () => calls.filter(call => call.method !== 'GET')
      const before = writes().length
      await page.clock.install()
      const cdp = await page.context().newCDPSession(page)
      try {
        await expectStableControls({ controls: { row, search: panel.getByRole('combobox'), group: panel.getByRole('listbox') }, interactions: [
          { name: 'hold reveals at 500 ms without selection', run: async () => {
            await pauseForGesture(page)
            const box = (await row.boundingBox())!
            await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: box.x + box.width / 2, y: box.y + box.height / 2 }] })
            await page.clock.runFor(499)
            await expect(page.locator('.tooltip')).toHaveCount(0)
            await page.clock.runFor(1)
            await expect(page.locator('.tooltip')).toHaveText(longName)
            await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
            await page.clock.resume()
            await expect(panel).toBeVisible()
            await expect(page.locator('.tooltip')).toHaveText(longName)
            expect(writes()).toHaveLength(before)
          } },
          { name: 'Escape dismisses full text without closing the picker', run: async () => {
            await page.keyboard.press('Escape')
            await expect(page.locator('.tooltip')).toHaveCount(0)
            await expect(panel).toBeVisible()
          } },
        ] })
        await noOverflow(page)
        // Reveal again for the evidence, then use the row's normal tap action.
        await pauseForGesture(page)
        const box = (await row.boundingBox())!
        await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: box.x + box.width / 2, y: box.y + box.height / 2 }] })
        await page.clock.runFor(500)
        await expect(page.locator('.tooltip')).toHaveText(longName)
        await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
        await page.clock.resume()
        await page.screenshot({ path: `${touchShots}/production-picker-${width}-${theme}.png` })
        await row.tap()
        await expect(panel).toHaveCount(0)
        await expect(page.locator('.tooltip')).toHaveCount(0)
        await expect.poll(() => data.nodes.find(node => node.id === 'n-4')!.parent_id).toBe('n-epic')
        const move = calls.filter(call => call.method === 'POST' && call.path === '/api/nodes/n-4/move')
        expect(move).toHaveLength(1)
        expect(move[0]!.body).toMatchObject({ parent_id: 'n-epic' })
        expect(errors).toEqual([])
      } finally { await page.clock.resume(); await cdp.detach() }
    })

    test('production table hold discloses without selection and the next tap opens the ticket', async ({ page }) => {
      const errors = watchErrors(page)
      const data = fixtures(); data.preferences.theme = { choice: theme }
      data.preferences.releases = { last_seen: '260923120000.0.0' }
      data.nodes.find(node => node.id === 'n-1')!.title = longName
      const calls = await mockWork(page, data)
      await page.goto('/p/PHAROS')
      const table = page.getByRole('grid', { name: 'Tickets' })
      const row = table.locator('#row-n-1')
      const name = row.locator('.title-text')
      const selected = table.locator('.ticket-row.selected')
      const bulk = page.getByRole('toolbar', { name: /selected ticket/ })
      await expect(name).toHaveAttribute('data-tip', longName)
      await expect(selected).toHaveCount(0)
      await page.clock.install()
      const cdp = await page.context().newCDPSession(page)
      try {
        await expectStableControls({ controls: { row, name, copy: row.getByRole('button', { name: 'Copy PHAROS-11' }) }, interactions: [
          { name: 'clipped-name hold owns disclosure before the table selection threshold', run: async () => {
            await pauseForGesture(page)
            const box = (await name.boundingBox())!
            await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: box.x + box.width / 2, y: box.y + box.height / 2 }] })
            await page.clock.runFor(479)
            await expect(selected).toHaveCount(0)
            await expect(page.locator('.tooltip')).toHaveCount(0)
            await page.clock.runFor(1)
            await expect(selected).toHaveCount(0)
            await page.clock.runFor(19)
            await expect(page.locator('.tooltip')).toHaveCount(0)
            // A native hold can raise contextmenu before its compatibility
            // click; it must not hand the gesture back to row selection.
            await name.dispatchEvent('contextmenu', { bubbles: true, cancelable: true })
            await expect(selected).toHaveCount(0)
            await page.clock.runFor(1)
            await expect(page.locator('.tooltip')).toHaveText(longName)
            await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
            await page.clock.resume()
            await expect(page.locator('.tooltip')).toHaveText(longName)
            await expect(selected).toHaveCount(0)
            await expect(bulk).toHaveCount(0)
            await expect(page).toHaveURL(/\/p\/PHAROS\/tickets$/)
            expect(calls.filter(call => call.method !== 'GET')).toHaveLength(0)
          } },
          { name: 'Escape dismisses disclosure without moving row controls', run: async () => {
            await page.screenshot({ path: `${tableShots}/disclosure-${width}-${theme}.png` })
            await page.keyboard.press('Escape')
            await expect(page.locator('.tooltip')).toHaveCount(0)
            await expect(selected).toHaveCount(0)
          } },
        ] })
        await noOverflow(page)
        // The compatibility click was consumed, but no row suppression flag
        // may swallow this fresh native tap on the same record.
        await name.tap()
        await expect(page).toHaveURL(/\/p\/PHAROS\/PHAROS-11$/)
        await expect(page.getByRole('complementary', { name: 'Ticket details' })).toBeVisible()
        await expect(page.locator('.tooltip')).toHaveCount(0)
        expect(calls.filter(call => call.method !== 'GET')).toHaveLength(0)
        expect(errors).toEqual([])
      } finally { await page.clock.resume(); await cdp.detach() }
    })

    if (width === 390) test('production phone selection holds still work and clipped holds preserve an existing selection', async ({ page }) => {
      const errors = watchErrors(page)
      const data = fixtures(); data.preferences.theme = { choice: theme }
      data.preferences.releases = { last_seen: '260923120000.0.0' }
      data.nodes.find(node => node.id === 'n-1')!.title = longName
      data.nodes.find(node => node.id === 'n-2')!.title = 'Kurz'
      const calls = await mockWork(page, data)
      await page.goto('/p/PHAROS')
      const table = page.getByRole('grid', { name: 'Tickets' })
      const clippedRow = table.locator('#row-n-1'), shortRow = table.locator('#row-n-2')
      const name = clippedRow.locator('.title-text'), short = shortRow.locator('.title-text')
      const selectedKeys = table.locator('.ticket-row.selected .key')
      await expect(name).toHaveAttribute('data-tip', longName)
      await expect(short).not.toHaveAttribute('data-clip-tip')
      await page.clock.install()
      const cdp = await page.context().newCDPSession(page)
      try {
        await pauseForGesture(page)
        const box = (await short.boundingBox())!
        await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: box.x + box.width / 2, y: box.y + box.height / 2 }] })
        await page.clock.runFor(479)
        await expect(selectedKeys).toHaveCount(0)
        await page.clock.runFor(1)
        await expect(selectedKeys).toHaveText(['PHAROS-12'])
        await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
        await page.clock.resume()
        await expect(selectedKeys).toHaveText(['PHAROS-12'])
        await expect(page.locator('.tooltip')).toHaveCount(0)
        // Ordinary taps continue to toggle when selection is already active.
        await name.tap()
        await expect(selectedKeys).toHaveText(['PHAROS-11', 'PHAROS-12'])
        await expectStableControls({ controls: { row: clippedRow, name, check: clippedRow.getByRole('checkbox', { name: 'Select PHAROS-11' }), clear: page.getByRole('button', { name: 'Clear the selection' }) }, interactions: [
          { name: 'hold reads an already selected row without toggling it', run: async () => {
            await pauseForGesture(page)
            const box = (await name.boundingBox())!
            await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: box.x + box.width / 2, y: box.y + box.height / 2 }] })
            await page.clock.runFor(500)
            await expect(page.locator('.tooltip')).toHaveText(longName)
            await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
            await page.clock.resume()
            await expect(selectedKeys).toHaveText(['PHAROS-11', 'PHAROS-12'])
          } },
          { name: 'dismiss retains selection and row controls', run: async () => {
            await page.keyboard.press('Escape')
            await expect(page.locator('.tooltip')).toHaveCount(0)
            await expect(selectedKeys).toHaveText(['PHAROS-11', 'PHAROS-12'])
          } },
        ] })
        await name.tap()
        await expect(selectedKeys).toHaveText(['PHAROS-12'])
        await expect(page).toHaveURL(/\/p\/PHAROS\/tickets$/)
        await noOverflow(page)
        expect(calls.filter(call => call.method !== 'GET')).toHaveLength(0)
        expect(errors).toEqual([])
      } finally { await page.clock.resume(); await cdp.detach() }
    })

    test('movement beyond 8 px and scrolling cancel pending long presses', async ({ page }) => {
      const errors = watchErrors(page)
      await setupHarness(page, theme)
      const name = page.locator('.standalone')
      await page.clock.install()
      // Synthetic pointers allow exact 8 px boundary and cancellation events;
      // the production test above covers real touch and its compatibility click.
      const pointer = (type: string, x = 100) => name.dispatchEvent(type, { pointerType: 'touch', pointerId: 42, isPrimary: true, button: 0, clientX: x, clientY: 100, bubbles: true })
      await expectStableControls({ controls: { name, change: page.getByRole('button', { name: 'Change name' }) }, interactions: [
        { name: '8 px remains a hold, 9 px cancels it', run: async () => {
          await pauseForGesture(page)
          await pointer('pointerdown'); await pointer('pointermove', 108)
          await page.clock.runFor(500)
          await expect(page.locator('.tooltip')).toHaveText(longName)
          await pointer('pointerup', 108)
          await page.keyboard.press('Escape')
          await pointer('pointerdown'); await pointer('pointermove', 109)
          await page.clock.runFor(500)
          await expect(page.locator('.tooltip')).toHaveCount(0)
          await pointer('pointerup', 109)
          await name.dispatchEvent('click', { pointerType: 'touch', pointerId: 42, detail: 1 })
          await expect(page.locator('.tooltip')).toHaveCount(0)
          await page.clock.resume()
        } },
        { name: 'container scroll cancels before the threshold', run: async () => {
          await pauseForGesture(page)
          await pointer('pointerdown'); await page.clock.runFor(250)
          await page.locator('.harness').dispatchEvent('scroll')
          await page.clock.runFor(500)
          await expect(page.locator('.tooltip')).toHaveCount(0)
          await pointer('pointerup')
          await name.dispatchEvent('click', { pointerType: 'touch', pointerId: 42, detail: 1 })
          await expect(page.locator('.tooltip')).toHaveCount(0)
          await page.clock.resume()
        } },
      ] })
      await noOverflow(page)
      expect(errors).toEqual([])
    })

    test('real touch scrolling cancels a hold without blocking the page', async ({ page }) => {
      const errors = watchErrors(page)
      await setupHarness(page, theme)
      await page.setViewportSize({ width, height: 400 })
      const name = page.locator('.standalone')
      await page.clock.install()
      const cdp = await page.context().newCDPSession(page)
      try {
        await pauseForGesture(page)
        const box = (await name.boundingBox())!
        const x = box.x + box.width / 2, y = box.y + box.height / 2
        await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] })
        await page.clock.runFor(200)
        for (const distance of [10, 30, 60, 90]) await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y: y - distance }] })
        await page.clock.runFor(500)
        await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
        await page.clock.resume()
        await expect.poll(() => page.evaluate(() => scrollY)).toBeGreaterThan(0)
        await expect(page.locator('.tooltip')).toHaveCount(0)
        expect(errors).toEqual([])
      } finally { await page.clock.resume(); await cdp.detach() }
    })

    test('long press remains until an outside tap; cancellation and changed records cannot reveal stale text', async ({ page }) => {
      const errors = watchErrors(page)
      await setupHarness(page, theme)
      const name = page.locator('.standalone')
      const change = page.getByRole('button', { name: 'Change name' })
      await page.clock.install()
      const pointer = (type: string, id = 42) => name.dispatchEvent(type, { pointerType: 'touch', pointerId: id, isPrimary: id === 42, button: 0, clientX: 100, clientY: 100 })
      await expectStableControls({ controls: { name, change }, interactions: [
        { name: 'release and focus loss retain text until outside tap', run: async () => {
          await pauseForGesture(page)
          await pointer('pointerdown'); await page.clock.runFor(500)
          await expect(page.locator('.tooltip')).toHaveText(longName)
          await pointer('pointerup')
          await name.dispatchEvent('focusout')
          await page.clock.runFor(2000)
          await expect(page.locator('.tooltip')).toHaveText(longName)
          await page.clock.resume()
          await change.tap()
          await expect(page.locator('.tooltip')).toHaveCount(0)
          await change.tap()
          await expect(name).toHaveAttribute('data-tip', longName)
        } },
        { name: 'pointer cancellation leaves no delayed disclosure', run: async () => {
          await pauseForGesture(page)
          await pointer('pointerdown'); await pointer('pointercancel')
          await page.clock.runFor(500)
          await expect(page.locator('.tooltip')).toHaveCount(0)
          await page.clock.resume()
        } },
        { name: 'a second finger cancels the first hold', run: async () => {
          await pauseForGesture(page)
          await pointer('pointerdown'); await pointer('pointerdown', 43)
          await page.clock.runFor(500)
          await expect(page.locator('.tooltip')).toHaveCount(0)
          await pointer('pointerup', 43); await pointer('pointerup')
          await page.clock.resume()
        } },
        { name: 'a reused row cannot reveal the old record', run: async () => {
          await pauseForGesture(page)
          await pointer('pointerdown')
          await name.evaluate(el => { el.textContent = `Other record: ${el.textContent}` })
          // The directive measures mutations in the next animation frame.
          await page.clock.runFor(16)
          await expect(name).toHaveAttribute('data-tip', `Other record: ${longName}`)
          await page.clock.runFor(500)
          await expect(page.locator('.tooltip')).toHaveCount(0)
          await pointer('pointerup')
          await page.clock.resume()
        } },
      ] })
      await noOverflow(page)
      expect(errors).toEqual([])
    })
  })
}
