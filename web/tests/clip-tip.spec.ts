// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page, type Locator } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockPairing, pairingView } from './agent-pairing-fixtures'
import { mockTicketGraph, ticketGraphWorld } from './ticket-graph-fixtures'
import { expectStableControls } from './helpers/stable'
import { clipSession, longAgent, longComputer, longName } from './clip-tip-fixtures'

const shots = 'test-results/aeon-632a-clip'
const fixShots = `${shots}/tooltip-fix`
const overflowShots = `${shots}/overflow-fix4`
const touchShots = `${shots}/touch-fix5`
const overflowText = Array.from({ length: 20 }, (_, index) => `${index + 1}. ${longName}`).join('\n')
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
async function scrollPage(page: Page, top: number) {
  // The scroll event is asynchronous. Cross its dispatch before asserting so
  // an old, still-visible tooltip cannot satisfy the disclosure assertion.
  await page.evaluate(top => new Promise<void>(resolve => {
    window.addEventListener('scroll', () => requestAnimationFrame(() => resolve()), { once: true })
    window.scrollTo(0, top)
  }), top)
  await expect.poll(() => page.evaluate(() => scrollY)).toBe(top)
}
async function pauseForGesture(page: Page) {
  // The protocol call crosses processes while the clock is still running.
  // Pause ahead of that transit; every gesture's tested delay starts afterward.
  await page.clock.pauseAt(await page.evaluate(() => Date.now() + 1000))
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
        await standalone.tap(); await expect(page.locator('.tooltip')).toHaveCount(0)
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

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test.describe(`tooltip fix: ${width}px ${theme}`, () => {
    test.use({ viewport: { width, height: 400 }, colorScheme: theme })

    test('keyboard disclosure survives pointer movement until Escape or blur', async ({ page }) => {
      const errors = watchErrors(page)
      await setupHarness(page, theme)
      const name = page.locator('.standalone')
      const change = page.getByRole('button', { name: 'Change name' })
      await keyboardTip(page, name, longName)
      await expect(name).toBeFocused()
      await expectStableControls({ controls: { name, change }, interactions: [
        { name: 'pointer leaves the keyboard source', run: async () => {
          await name.hover()
          await page.mouse.move(1, 1)
          await expect(name).toBeFocused()
          await expect(page.locator('.tooltip')).toHaveText(longName)
        } },
        { name: 'pointer crosses another tooltip source', run: async () => {
          await change.evaluate(el => el.setAttribute('data-tip', 'Andere Aktion'))
          await change.hover()
          await expect(name).toBeFocused()
          await expect(page.locator('.tooltip')).toHaveText(longName)
        } },
        { name: 'ordinary key keeps the focused disclosure', run: async () => {
          await name.press('a')
          await expect(page.locator('.tooltip')).toHaveText(longName)
        } },
        { name: 'Escape dismisses without a pointer reopening it', run: async () => {
          await name.press('Escape')
          await expect(page.locator('.tooltip')).toHaveCount(0)
          await expect(name).toBeFocused()
          await page.evaluate(() => document.body.appendChild(document.createElement('span')))
          await expect(page.locator('.tooltip')).toHaveCount(0)
        } },
        { name: 'focus leaves the source', run: async () => {
          await keyboardTip(page, name, longName)
          await change.focus()
          await expect(page.locator('.tooltip')).toHaveText('Andere Aktion')
          await change.evaluate(el => el.removeAttribute('data-tip'))
          await expect(page.locator('.tooltip')).toHaveCount(0)
        } },
      ] })
      await noOverflow(page)
      await keyboardTip(page, name, longName)
      await page.mouse.move(1, 1)
      await expect(page.locator('.tooltip')).toHaveText(longName)
      await page.screenshot({ path: `${fixShots}/keyboard-${width}-${theme}.png` })
      expect(errors).toEqual([])
    })

    test('focus disclosure follows real scrolling; pointer disclosure dismisses', async ({ page }) => {
      const errors = watchErrors(page)
      await setupHarness(page, theme)
      const name = page.locator('.standalone')
      const change = page.getByRole('button', { name: 'Change name' })
      await keyboardTip(page, name, longName)
      const before = (await name.boundingBox())!
      await expectStableControls({ controls: { name, change }, interactions: [
        { name: 'scroll the focused name', run: async () => {
          await scrollPage(page, 50)
          await expect(name).toBeFocused()
          await expect(page.locator('.tooltip')).toHaveText(longName)
          const tipBox = (await page.locator('.tooltip').boundingBox())!
          const nameBox = (await name.boundingBox())!
          expect(nameBox.y).toBeCloseTo(before.y - 50, 1)
          expect(tipBox.y + tipBox.height).toBeCloseTo(nameBox.y - 8, 0)
        } },
        { name: 'scroll the focused name offscreen', run: async () => {
          await scrollPage(page, 250)
          await expect(name).toBeFocused()
          await expect(page.locator('.tooltip')).toHaveText(longName)
          expect((await page.locator('.tooltip').boundingBox())!.y).toBeGreaterThanOrEqual(8)
        } },
      ] })
      await page.screenshot({ path: `${fixShots}/scroll-${width}-${theme}.png` })
      await page.evaluate(() => (document.activeElement as HTMLElement)?.blur())
      await scrollPage(page, 0)
      await name.hover()
      await expect(page.locator('.tooltip')).toHaveText(longName)
      await scrollPage(page, 50)
      await expect(page.locator('.tooltip')).toHaveCount(0)
      expect(errors).toEqual([])
    })

    for (const side of ['default', 'end'] as const) {
      test(`long ${side} disclosure is viewport bounded and scrolls inside`, async ({ page }) => {
        const errors = watchErrors(page)
        await setupHarness(page, theme)
        const name = page.locator('.standalone')
        const longText = Array.from({ length: 20 }, (_, index) => `${index + 1}. ${longName}`).join('\n')
        await name.evaluate((el, options) => {
          el.textContent = options.text
          if (options.side === 'end') el.setAttribute('data-tip-side', 'end')
        }, { text: longText, side })
        await expect(name).toHaveAttribute('data-tip', longText)
        await keyboardTip(page, name, longText)
        const tip = page.locator('.tooltip')
        const checkBounds = async () => {
          const box = (await tip.boundingBox())!
          expect(box.y).toBeGreaterThanOrEqual(8)
          expect(box.y + box.height).toBeLessThanOrEqual(392)
          expect(box.x).toBeGreaterThanOrEqual(8)
          expect(box.x + box.width).toBeLessThanOrEqual(width - 8)
          expect(await tip.evaluate(el => el.scrollHeight - el.clientHeight)).toBeGreaterThan(0)
          expect(await tip.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
        }
        await checkBounds()
        await expectStableControls({ controls: { name, change: page.getByRole('button', { name: 'Change name' }) }, interactions: [
          { name: 'read overflowing disclosure with the wheel', run: async () => {
            await tip.hover()
            await page.mouse.wheel(0, 200)
            await expect.poll(() => tip.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
            await expect(name).toBeFocused()
            await expect(tip).toHaveText(longText)
            expect(await page.evaluate(() => scrollY)).toBe(0)
            await checkBounds()
          } },
          { name: 'move source towards viewport top', run: async () => {
            await scrollPage(page, 160)
            await expect(tip).toHaveText(longText)
            await checkBounds()
          } },
        ] })
        await noOverflow(page)
        await page.screenshot({ path: `${fixShots}/long-${side}-${width}-${theme}.png` })
        expect(errors).toEqual([])
      })
    }
  })
}

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test.describe(`overflow fix4: ${width}px ${theme}`, () => {
    test.use({ viewport: { width, height: 400 }, colorScheme: theme, hasTouch: true })

    async function picker(page: Page) {
      await setupHarness(page, theme)
      await page.getByRole('button', { name: 'option', exact: true }).click()
      const panel = page.getByRole('dialog', { name: 'Assignee of PHAROS-11' })
      const row = panel.locator('.menu-item').first()
      const label = row.locator('.label')
      await label.evaluate((el, text) => { el.textContent = text }, overflowText)
      await expect(label).toHaveAttribute('data-tip', overflowText)
      await keyboardTip(page, row, overflowText)
      const tip = page.locator('.tooltip')
      await expect(tip).toHaveClass(/scrollable/)
      return { panel, row, tip }
    }

    async function finalLine(page: Page, tip: Locator, shot: string) {
      // Every navigation key must scroll this region, without moving the page
      // or the picker's active row. Browser animation is observed, never slept.
      await page.keyboard.press('ArrowDown')
      await expect.poll(() => tip.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
      await page.keyboard.press('PageDown')
      await expect.poll(() => tip.evaluate(el => el.scrollTop)).toBeGreaterThan(100)
      await page.keyboard.press('End')
      await expect.poll(() => tip.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThanOrEqual(1)
      await expect(tip).toContainText(`20. ${longName}`)
      await expect(tip).toHaveAttribute('role', 'region')
      await expect(tip).not.toHaveAttribute('aria-hidden', 'true')
      await expect(tip).toBeFocused()
      const box = (await tip.boundingBox())!
      expect(box.x).toBeGreaterThanOrEqual(8)
      expect(box.x + box.width).toBeLessThanOrEqual(width - 8)
      expect(box.y).toBeGreaterThanOrEqual(8)
      expect(box.y + box.height).toBeLessThanOrEqual(392)
      await page.screenshot({ path: `${overflowShots}/${shot}-text-${width}-${theme}.png` })
      await page.keyboard.press('PageUp')
      await expect.poll(() => tip.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeGreaterThan(0)
      const beforeUp = await tip.evaluate(el => el.scrollTop)
      await page.keyboard.press('ArrowUp')
      await expect.poll(() => tip.evaluate(el => el.scrollTop)).toBeLessThan(beforeUp)
      await page.keyboard.press('Home')
      await expect.poll(() => tip.evaluate(el => el.scrollTop)).toBe(0)
    }

    test('keyboard reaches the final line and Escape restores the standalone source', async ({ page }) => {
      const errors = watchErrors(page)
      await setupHarness(page, theme)
      const name = page.locator('.standalone')
      await name.evaluate((el, text) => { el.textContent = text }, overflowText)
      await keyboardTip(page, name, overflowText)
      const tip = page.locator('.tooltip')
      const pageTop = await page.evaluate(() => scrollY)
      await expectStableControls({ controls: { name, change: page.getByRole('button', { name: 'Change name' }) }, interactions: [
        { name: 'Tab into full text', run: async () => { await page.keyboard.press('Tab'); await expect(tip).toBeFocused() } },
        { name: 'keyboard reads the final line', run: async () => { await finalLine(page, tip, 'standalone'); expect(await page.evaluate(() => scrollY)).toBe(pageTop) } },
        { name: 'Escape returns focus without reopening', run: async () => { await page.keyboard.press('Escape'); await expect(tip).toHaveCount(0); await expect(name).toBeFocused() } },
      ] })
      await noOverflow(page)
      await page.screenshot({ path: `${overflowShots}/standalone-${width}-${theme}.png` })
      expect(errors).toEqual([])
    })

    test('Tab reads picker overflow and Escape returns to the same option before closing', async ({ page }) => {
      const errors = watchErrors(page)
      const { panel, row, tip } = await picker(page)
      const short = panel.locator('.menu-item').last()
      const pageTop = await page.evaluate(() => scrollY)
      await expectStableControls({ controls: { row, short, group: panel.locator('.menu'), title: panel.locator('.menu-title') }, interactions: [
        { name: 'Tab into picker full text', run: async () => { await page.keyboard.press('Tab'); await expect(tip).toBeFocused(); await expect(panel).toBeVisible() } },
        { name: 'scroll without picker navigation', run: async () => { await finalLine(page, tip, 'picker-keyboard'); await expect(short).not.toBeFocused(); expect(await page.evaluate(() => scrollY)).toBe(pageTop) } },
        { name: 'Shift Tab returns to source', run: async () => { await page.keyboard.press('Shift+Tab'); await expect(row).toBeFocused(); await expect(tip).toBeVisible() } },
        { name: 'Escape dismisses only the full text', run: async () => {
          await page.keyboard.press('Tab'); await expect(tip).toBeFocused()
          await page.keyboard.press('Escape'); await expect(tip).toHaveCount(0); await expect(row).toBeFocused(); await expect(panel).toBeVisible()
        } },
      ], scrollAreas: { picker: panel } })
      await noOverflow(page)
      await page.screenshot({ path: `${overflowShots}/picker-keyboard-${width}-${theme}.png` })
      await page.keyboard.press('Escape')
      await expect(panel).toHaveCount(0)
      expect(errors).toEqual([])
    })

    for (const input of ['scrollbar', 'touch'] as const) {
      test(`${input} scrolling keeps the originating picker open`, async ({ page }) => {
        const errors = watchErrors(page)
        const { panel, row, tip } = await picker(page)
        await expectStableControls({ controls: { row, short: panel.locator('.menu-item').last(), group: panel.locator('.menu'), title: panel.locator('.menu-title') }, interactions: [
          { name: `${input} interacts with overflowing text`, run: async () => {
            const box = (await tip.boundingBox())!
            if (input === 'scrollbar') {
              // Press the scrollbar gutter/track itself, then wheel inside the
              // region. Both pointerdown and scrolling must belong to the panel.
              await page.mouse.click(box.x + box.width - 2, box.y + box.height / 2)
              await expect(panel).toBeVisible()
              await tip.hover(); await page.mouse.wheel(0, 200)
            } else {
              const cdp = await page.context().newCDPSession(page)
              try {
                const x = box.x + box.width / 2, y = box.y + box.height - 40
                await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] })
                await expect(panel).toBeVisible()
                for (const distance of [30, 60, 90, 120]) await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y: y - distance }] })
                await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
              } finally { await cdp.detach() }
            }
            await expect.poll(() => tip.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
            await expect(panel).toBeVisible(); await expect(tip).toHaveText(overflowText)
            await expect(page.getByRole('status')).toBeEmpty()
          } },
        ], scrollAreas: { picker: panel } })
        await noOverflow(page)
        await page.screenshot({ path: `${overflowShots}/picker-${input}-${width}-${theme}.png` })
        expect(errors).toEqual([])
      })
    }
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
      await workspace.getByRole('button', { name: 'No epic. Choose an epic' }).tap()
      const panel = page.getByRole('dialog', { name: 'Epic for PHAROS-14' })
      const row = panel.getByRole('option')
      await expect(row.locator('.title')).toHaveAttribute('data-tip', longName)
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
