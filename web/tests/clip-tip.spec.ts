// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page, type Locator } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockPairing, pairingView } from './agent-pairing-fixtures'
import { mockTicketGraph, ticketGraphWorld } from './ticket-graph-fixtures'
import { expectStableControls } from './helpers/stable'
import { clipSession, longAgent, longComputer, longName } from './clip-tip-fixtures'

const shots = 'test-results/aeon-632a-clip'
async function noOverflow(page: Page) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)
  const offenders = overflow > 1 ? await page.evaluate(() => [...document.querySelectorAll('main *')]
    .filter(el => el.getBoundingClientRect().right > innerWidth + 1).slice(0, 10)
    .map(el => ({ tag: el.tagName, class: el.className, width: el.getBoundingClientRect().width, right: el.getBoundingClientRect().right }))) : []
  expect(overflow, JSON.stringify(offenders)).toBeLessThanOrEqual(1)
}
async function keyboardTip(page: Page, control: Locator, text: string) {
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
  await page.keyboard.press('ArrowRight')
  await control.focus()
  await expect(page.locator('.tooltip')).toHaveText(text)
}
async function setupHarness(page: Page, theme: string, query = '') {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.nodes.find(node => node.id === 'n-epic')!.title = longName
  data.nodes.push({ ...data.nodes.find(node => node.id === 'n-epic')!, id: 'short-epic', key: 'PHAROS-99', title: 'Kurz' })
  await mockWork(page, data)
  await mockPairing(page)
  await page.route('**/api/harness-sessions?*', route => route.fulfill({ json: { items: [{ ...clipSession, heartbeat_at: new Date().toISOString() }], next_cursor: null } }))
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ json: { computers: [
    pairingView({ computer_id: '33333333-3333-4333-8333-333333333333', computer_name: longComputer, computer_state: 'connected', state: 'redeemed', setup_state: 'connected', connectivity: 'online', local_processes: 'drained', local_cleanup: 'confirmed' }),
    pairingView({ computer_id: '33333333-3333-4333-8333-333333333334', request_id: '11111111-1111-4111-8111-111111111112', computer_name: longComputer, computer_state: 'revoked', state: 'revoked', local_processes: 'drained', local_cleanup: 'confirmed' }),
  ] } }))
  await page.goto(`/tests/clip-tip-harness.html${query}`)
  await expect(page.getByRole('heading', { name: 'Ganze Namen' })).toBeVisible()
  await expect(page.locator('.agent-name')).toHaveText(longAgent)
  await expect(page.locator('.computer .name')).toHaveText(longComputer)
  await page.evaluate(() => document.fonts.ready)
}

for (const width of [390, 768, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test.describe(`${width}px ${theme}`, () => {
    test.use({ viewport: { width, height: 1000 }, colorScheme: theme, hasTouch: width === 390 })
    test('clipped work and agent names reveal without moving controls; phone pickers reserve two lines', async ({ page }) => {
      const errors = watchErrors(page)
      await setupHarness(page, theme)
      await noOverflow(page)
      const shot = async (name: string) => page.screenshot({ path: `${shots}/${name}-${width}-${theme}.png`, fullPage: true })
      const standalone = page.locator('.standalone')
      await expect(standalone).toHaveAttribute('data-tip', longName)
      await expectStableControls({
        controls: { name: standalone, change: page.getByRole('button', { name: 'Change name' }), pickers: page.getByRole('navigation') },
        interactions: [{ name: 'keyboard disclosure', run: async () => { await page.getByRole('button', { name: 'Close picker' }).focus(); await page.keyboard.press('Tab'); await expect(standalone).toBeFocused(); await expect(page.locator('.tooltip')).toHaveText(longName) } },
          { name: 'shorter reused name', run: async () => { await page.getByRole('button', { name: 'Change name' }).click(); await expect(standalone).not.toHaveAttribute('data-tip'); } },
          { name: 'restore long name', run: async () => { await page.getByRole('button', { name: 'Change name' }).click(); await expect(standalone).toHaveAttribute('data-tip', longName); } }],
        scrollAreas: { page: page.locator('.harness') },
      })
      if (width === 390) {
        await standalone.tap(); await expect(page.locator('.tooltip')).toHaveText(longName)
      }
      const child = page.locator('.child-row').first()
      await keyboardTip(page, child, longName)
      await expectStableControls({ controls: { child }, interactions: [{ name: 'hover name', run: async () => { await child.hover(); await expect(page.locator('.tooltip')).toHaveText(longName) } }] })
      const facet = page.locator('.facet-option').first()
      const checkbox = facet.getByRole('checkbox')
      await expectStableControls({ controls: { row: facet, checkbox, group: page.locator('.facet-options') }, interactions: [
        { name: 'include', run: async () => { await checkbox.check(); await expect(checkbox).toBeChecked() } },
        { name: 'exclude', run: async () => { await facet.getByRole('button', { name: /^Exclude/ }).click(); await expect(facet).toHaveClass(/out/) } },
      ] })
      await keyboardTip(page, checkbox, longName)
      const open = page.locator('.problem-chip .open')
      await expectStableControls({ controls: { open, line: page.locator('.live-line') }, interactions: [{ name: 'agent name focus', run: () => keyboardTip(page, page.locator('.who'), longAgent) }] })
      await expect(open).toBeInViewport()
      expect(await open.evaluate(el => el.clientWidth)).toBeGreaterThan(40)
      await keyboardTip(page, page.locator('.agent-chip'), `${longAgent} · Codex · Working`)
      await page.getByRole('button', { name: /^Revoked \(/ }).click()
      const revoked = page.locator('.revoked-name')
      await expect(revoked).toHaveAttribute('data-tip', longComputer)
      await expectStableControls({ controls: { refresh: page.getByRole('button', { name: 'Refresh computers' }), revoked }, interactions: [{ name: 'computer focus', run: () => keyboardTip(page, revoked, longComputer) }] })
      await noOverflow(page); await shot('work-agents-names')
      if (width === 390) {
        expect(await page.locator('.child-title').first().evaluate(el => el.clientHeight)).toBe(36)
        expect(await facet.evaluate(el => el.clientHeight)).toBe(52)
      }

      for (const kind of ['epic', 'label', 'option', 'relation']) {
        await page.getByRole('button', { name: kind, exact: true }).click()
        const dialog = page.locator('.floating')
        await expect(dialog).toBeVisible()
        const nameClass = kind === 'label' ? '.name' : kind === 'option' ? '.label' : '.title'
        const name = dialog.locator(nameClass).filter({ hasText: longName }).first()
        await expect(name).toHaveAttribute('data-tip', longName)
        const row = name.locator('..')
        const short = dialog.locator('.option, .menu-item').filter({ hasText: 'Kurz' }).first()
        await expectStableControls({ controls: { row, short, group: dialog.locator(kind === 'option' ? '.menu' : '.options'), search: kind === 'option' ? dialog.locator('.menu-title') : dialog.locator('input') }, interactions: [
          { name: 'focus full name', run: () => keyboardTip(page, kind === 'relation' ? dialog.getByRole('combobox') : row, longName) },
          { name: 'hover full name', run: async () => { await name.hover(); await expect(page.locator('.tooltip')).toHaveText(longName) } },
          { name: 'next option', run: () => short.hover() },
          { name: 'previous option', run: () => row.hover() },
        ], scrollAreas: { picker: dialog } })
        if (width === 390) {
          expect(await row.evaluate(el => el.clientHeight)).toBe(52)
          expect(await name.evaluate(el => el.clientHeight)).toBe(36)
        }
        if (kind === 'label') {
          await expectStableControls({ controls: { row, apply: dialog.getByRole('button', { name: 'Apply' }), group: dialog.locator('.options') }, interactions: [{ name: 'toggle label', run: async () => { await row.click(); await expect(row).toHaveAttribute('aria-checked', 'true') } }] })
        }
        await noOverflow(page); await shot(`${kind}-picker`)
        await page.keyboard.press('Escape')
        await expect(dialog).toHaveCount(0)
      }
      expect(errors).toEqual([])
    })

    test('lifecycle: delayed single epic discloses while search keeps focus', async ({ page }) => {
      const errors = watchErrors(page)
      await setupHarness(page, theme, '?single-epic')
      let release!: () => void
      let started!: () => void
      const requested = new Promise<void>(resolve => { started = resolve })
      const response = new Promise<void>(resolve => { release = resolve })
      const epic = { ...fixtures().nodes.find(node => node.id === 'n-epic')!, title: longName }
      await page.route('**/api/nodes?*', async route => {
        started()
        await response
        await route.fulfill({ json: { items: [epic], next_cursor: null } })
      })
      await page.getByRole('button', { name: 'epic', exact: true }).focus()
      await page.keyboard.press('Enter')
      await requested
      const search = page.getByRole('combobox', { name: 'Find an epic' })
      await expect(search).toBeFocused()
      await expect(search).toHaveAttribute('aria-activedescendant', 'epic-option-0')
      await expect(page.getByRole('option')).toHaveCount(0)
      await expectStableControls({ controls: { search }, interactions: [
        { name: 'single delayed result mounts', run: async () => {
          release()
          await expect(page.getByRole('option')).toHaveCount(1)
          await expect(search).toBeFocused()
          await expect(page.locator('.tooltip')).toHaveText(longName)
        } },
        { name: 'arrow at unchanged index', run: async () => {
          await search.press('ArrowDown')
          await expect(search).toHaveAttribute('aria-activedescendant', 'epic-option-0')
          await expect(page.locator('.tooltip')).toHaveText(longName)
        } },
      ] })
      await noOverflow(page)
      await page.screenshot({ path: `${shots}/lifecycle-epic-${width}-${theme}.png`, fullPage: true })
      expect(errors).toEqual([])
    })

    for (const mode of ['hover', 'keyboard'] as const) {
      for (const transition of ['name', 'unclipped', 'removed'] as const) {
        test(`lifecycle: ${mode} disclosure follows ${transition} without refocus`, async ({ page }) => {
          const errors = watchErrors(page)
          await setupHarness(page, theme)
          const name = page.locator('.standalone')
          if (mode === 'hover') await name.hover()
          else {
            await page.getByRole('button', { name: 'Close picker' }).focus()
            await page.keyboard.press('Tab')
            await expect(name).toBeFocused()
          }
          await expect(page.locator('.tooltip')).toHaveText(longName)
          const nextName = `Neuer Datensatz — ${longName}`
          const checkInteraction = async () => {
            if (mode === 'keyboard') await expect(name).toBeFocused()
            else expect(await name.evaluate(el => el.matches(':hover'))).toBe(true)
          }
          const change = page.getByRole('button', { name: 'Change name' })
          await expectStableControls({
            controls: transition === 'removed' ? { change } : { name, change },
            interactions: [{ name: `source ${transition}`, run: async () => {
              // No pointer movement, focus change or click masks the source
              // transition. The fixture owns its Tab stop even when unclipped.
              if (transition === 'removed') {
                await name.evaluate(el => {
                  // Replace the record in its existing slot, as a virtualized
                  // list does, so source removal cannot shift nearby controls.
                  const replacement = el.cloneNode(false) as HTMLElement
                  replacement.removeAttribute('data-tip')
                  replacement.removeAttribute('data-clip-tip')
                  replacement.removeAttribute('tabindex')
                  replacement.textContent = 'Datensatz entfernt'
                  el.replaceWith(replacement)
                })
                await expect(page.locator('.tooltip')).toHaveCount(0)
              } else {
                await name.evaluate((el, text) => { el.textContent = text }, transition === 'name' ? nextName : 'Kurz')
                await checkInteraction()
                if (transition === 'name') {
                  await expect(name).toHaveAttribute('data-tip', nextName)
                  await expect(page.locator('.tooltip')).toHaveText(nextName)
                } else {
                  await expect(name).not.toHaveAttribute('data-tip')
                  await expect(page.locator('.tooltip')).toHaveCount(0)
                }
              }
            } }],
          })
          await noOverflow(page)
          await page.screenshot({ path: `${shots}/lifecycle-${mode}-${transition}-${width}-${theme}.png`, fullPage: true })
          expect(errors).toEqual([])
        })
      }
    }

    test('ticket titles keep the existing two-line phone layout and reveal full desktop names', async ({ page }) => {
      const data = fixtures(); data.preferences.theme = { choice: theme }
      data.nodes.find(node => node.id === 'n-1')!.title = longName
      await mockWork(page, data)
      await page.goto('/p/PHAROS/tickets')
      const title = page.locator('#row-n-1 .title-text')
      await expect(title).toHaveAttribute('data-tip', longName)
      const link = title.locator('..')
      await expectStableControls({ controls: { title: link }, interactions: [{ name: 'focus title', run: () => keyboardTip(page, link, longName) }] })
      const grid = page.getByRole('grid', { name: 'Tickets' })
      await grid.focus()
      for (let i = 0; i < 8 && await grid.getAttribute('aria-activedescendant') !== 'row-n-1'; i++) await grid.press('ArrowDown')
      await expect(grid).toHaveAttribute('aria-activedescendant', 'row-n-1')
      await expect(page.locator('.tooltip')).toHaveText(longName)
      if (width === 390) expect(await title.evaluate(el => getComputedStyle(el).webkitLineClamp)).toBe('2')
      await noOverflow(page)
      await page.screenshot({ path: `${shots}/ticket-table-${width}-${theme}.png`, fullPage: true })
    })
  })
}

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
