// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1309: retain the panel authority and stability regressions in balanced ESSENTIAL files.
import { expect, test } from '@playwright/test'
import { controlStability } from './control-stability'
import { WORKER, setup, refresh, savedLayout } from './native-session-program-fixtures'

for (const width of [1440]) for (const theme of ['light', 'dark']) {
  test(`AEON-1309: ${width} ${theme} panel controls, layouts and live-reader anchor stay stable`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 900 })
    const { state, worker, errors, world } = await setup(page, theme)
    await page.goto(`/agents/${WORKER}`)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel.getByRole('tab', { name: 'Details', exact: true })).toHaveAttribute('aria-selected', 'true')
    const tabs = Object.fromEntries(['Details', 'Chat', 'Events', 'Terminal'].map(name => [name, panel.getByRole('tab', { name, exact: true })]))
    const stable = await controlStability(page, { ...tabs, menu: panel.getByRole('tablist'), more: panel.getByRole('button', { name: 'More session actions' }), close: panel.getByRole('button', { name: 'Close session details' }), frame: panel })
    for (const name of ['Chat', 'Events', 'Terminal', 'Details']) await stable.check(async () => { await tabs[name]!.click(); await expect(tabs[name]!).toHaveAttribute('aria-selected', 'true') })
    stable.done()
    const headHeight = await panel.locator('.panel-head').evaluate(el => el.getBoundingClientRect().height)
    expect(headHeight).toBeLessThanOrEqual(width <= 720 ? 93 : 45)
    if (width <= 720) {
      for (const name of ['More session actions', 'Close session details']) {
        const box = await panel.getByRole('button', { name }).boundingBox(); expect(box!.width).toBeGreaterThanOrEqual(44); expect(box!.height).toBeGreaterThanOrEqual(44)
      }
    }
    await page.screenshot({ path: info.outputPath(`AEON-1309-${width}-${theme}-details.png`) })

    // WebKit-style anchoring: disable the browser's own correction, scroll the
    // facts into view, then grow a real activity line above the reader.
    await panel.locator('#session-panel-overview').evaluate(el => { (el as HTMLElement).style.overflowAnchor = 'none' })
    const facts = panel.locator('.block.first')
    await facts.evaluate(el => { const scroller = el.closest<HTMLElement>('#session-panel-overview')!; scroller.scrollTop += el.getBoundingClientRect().top - scroller.getBoundingClientRect().top })
    const before = await facts.evaluate(el => el.getBoundingClientRect().top)
    state.note = 'Neue Erkenntnisse zur Sitzungssteuerung und zur Wiederherstellung sichern die aktuelle Prozessidentität.'
    worker.heartbeat_at = '2026-10-06T10:00:04Z'; state.native.revision++
    await refresh(page)
    await expect(panel.locator('.now-step')).toContainText('Neue Erkenntnisse')
    const after = await facts.evaluate(el => el.getBoundingClientRect().top)
    expect(Math.abs(after - before), 'live content above the reader must not move the facts').toBeLessThanOrEqual(.5)

    await panel.getByRole('tab', { name: 'Terminal', exact: true }).click()
    const link = panel.getByRole('link', { name: 'Open live terminal' })
    await expect(link).toHaveAttribute('target', '_blank'); await expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    await page.screenshot({ path: info.outputPath(`AEON-1309-${width}-${theme}-terminal.png`) })
    if (width >= 1100) {
      await savedLayout(page, 'wide', () => panel.getByRole('button', { name: 'Sessions list', exact: true }).click())
      await expect(panel).toHaveAttribute('data-layout', 'wide')
      await expect(page.locator('.agents-list-pane')).toBeHidden()
      await page.screenshot({ path: info.outputPath(`AEON-1309-${width}-${theme}-wide.png`) })
      await savedLayout(page, 'side', () => panel.getByRole('button', { name: 'Sessions list', exact: true }).click())
      await expect(panel).toHaveAttribute('data-layout', 'side')
      await expect(page.locator('.agents-list-pane')).toBeVisible()
    }
    await panel.getByRole('button', { name: 'More session actions' }).click()
    await savedLayout(page, 'full', () => page.getByRole('menuitem', { name: /^Full screen/ }).click())
    await expect(panel).toHaveAttribute('data-layout', 'full')
    await page.screenshot({ path: info.outputPath(`AEON-1309-${width}-${theme}-full.png`) })
    if (width <= 720) {
      await expect(panel.getByRole('tablist')).toBeHidden()
      await panel.getByRole('button', { name: 'More session actions' }).click()
      await page.getByRole('menuitemradio', { name: 'Events', exact: true }).click()
      await expect(panel.locator('#session-panel-events')).toBeVisible()
    }
    // A holder terminal owns Esc, number keys, F and full-screen shortcuts.
    await panel.evaluate(el => { const terminal = document.createElement('div'); terminal.dataset.nativeTerminalHolder = 'true'; terminal.tabIndex = 0; terminal.id = 'holder-key-fixture'; el.append(terminal); terminal.focus() })
    await page.keyboard.press('Escape'); await page.keyboard.press('1'); await page.keyboard.press('Shift+E'); await page.keyboard.press('f')
    await expect(panel).toHaveAttribute('data-layout', 'full')
    await panel.getByRole('button', { name: 'Close session details' }).focus()
    await savedLayout(page, 'side', () => page.keyboard.press('Escape'))
    await expect(panel).toHaveAttribute('data-layout', 'side')
    await page.keyboard.press('2')
    await expect(panel.getByRole('tab', { name: 'Chat', exact: true })).toHaveAttribute('aria-selected', 'true')
    const callsBeforeAuto = world.calls.length
    await page.setViewportSize({ width: 844, height: 340 })
    await panel.getByRole('tab', { name: 'Terminal', exact: true }).click()
    await expect(panel).toHaveAttribute('data-layout', 'full')
    expect(world.calls.slice(callsBeforeAuto).filter(call => call.method !== 'GET' && call.path.includes('agents:panel.layout'))).toEqual([])
    await page.screenshot({ path: info.outputPath(`AEON-1309-${width}-${theme}-short-full.png`) })
    expect(errors).toEqual([])
  })
}
