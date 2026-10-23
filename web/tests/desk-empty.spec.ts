// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1406 (Tick's approved design): the empty Open tab is a calm picture with one line.
// Risks: the card jumps between reading (D), all clear (A) and a silent source (B); a silent
// source still reads as all clear; both theme pictures show; a narrow phone scrolls sideways.
// AEON-1631: a desk that did not answer shows the all-clear art while the badge counts items.
import { expect, test, type Locator, type Page, type TestInfo } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import { expectStableControls } from './helpers/stable'
import { fixtures, mockWork } from './work-fixtures'

// Every Desk source answers with an empty page. Kept here rather than in
// decision-desk-fixtures.ts, whose importers are at the 15-spec impact bound.
async function mockEmptyDesk(page: Page, theme: 'light' | 'dark', withItem = false) {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  await mockWork(page, data)
  const reads: string[] = []
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url()), path = url.pathname, read = route.request().method() === 'GET'
    if (read) reads.push(path + url.search)
    if (path === '/api/decision-desk/projection') return route.fulfill({ json: { items: withItem ? [{ id: 'accounts-1477', kind: 'account_matrix', revision: 1, title: 'Confirm the shared agent accounts', created_at: '2026-10-17T13:00:00Z', held: false, can_decide: true, href: '/settings/accounts', source: '/api/settings/accounts' }] : [], counts: { open: withItem ? 1 : 0, held: 0, chores: 0 }, has_more: false, as_of: new Date().toISOString() } })
    if (path === '/api/decision-desk') return route.fulfill({ json: { items: [], has_more: false } })
    if (path === '/api/approvals') return route.fulfill({ json: [] })
    if (path === '/api/rules/doctrine/inbox') return route.fulfill({ json: { items: [], pending: 0 } })
    if (read && (path === '/api/stepup-requests' || path === '/api/key-trim-proposals')) return route.fulfill({ json: { items: [], has_more: false } })
    if (path === '/api/harness-sessions') return route.fulfill({ json: { items: [], next_cursor: null } })
    if (read && path.endsWith('/messages')) return route.fulfill({ json: { items: [], next_after: 0 } })
    return route.fallback()
  })
  return reads
}

// AEON-1477: a transient failure must never replace good rows or move controls.
// AEON-1616 (R18): empty phone-only rows must not bury the first item or empty state.
// Extend these existing cases: the normal desktop gap and zero movement elsewhere, including failures.
// Tick: phone refresh feedback must clear the paused notice and the toast stack, leaving its retry reachable.
for (const [width, theme] of [[1440, 'dark'], [1024, 'light'], [390, 'light']] as const) {
  test(`AEON-1477 refresh failures keep the items and hint geometry ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    const goodAt = new Date('2026-10-17T13:58:00Z')
    await page.clock.setFixedTime(goodAt)
    await mockEmptyDesk(page, theme, true)
    let failure = 0, reads = 0, empty = false
    await page.route(url => url.pathname === '/api/decision-desk/projection', route => {
      reads++
      if (empty) return route.fulfill({ json: { items: [], counts: { open: 0, held: 0, chores: 0 }, has_more: false, as_of: goodAt.toISOString() } })
      return failure ? route.fulfill({ status: failure, json: { error: 'Unavailable' } }) : route.fallback()
    })
    await page.goto('/decision-desk')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    const list = page.getByTestId('desk-list'), row = page.getByTestId('desk-row-u:accounts-1477'), hint = page.getByTestId('desk-refresh-hint')
    await expect(row).toBeVisible()
    await expect(list).toHaveAttribute('aria-busy', 'false')
    const refresh = async (status: number) => {
      failure = status
      const before = reads
      await page.evaluate(() => window.dispatchEvent(new Event('focus')))
      await expect.poll(() => reads).toBeGreaterThan(before)
      await expect(list).toHaveAttribute('aria-busy', 'false')
      await expect(row).toBeVisible()
      await expect(page.getByTestId('desk-failed')).toHaveCount(0)
      await expect(page.getByTestId('desk-empty')).toHaveCount(0)
    }
    await documentFonts(page)
    const spacing = async () => {
      const geometry = await page.evaluate(() => {
        const tabs = document.querySelector('[role="radiogroup"][aria-label="Show"]')!.getBoundingClientRect()
        const list = document.querySelector('[data-testid="desk-list"]')!.getBoundingClientRect()
        const bar = document.querySelector('.dp-bar')!
        const controls = ['#desk-title', '[data-testid="desk-notice"]', '[data-testid="desk-tab-open"]', '[data-testid="desk-tab-decided"]'].map(selector => {
          const r = document.querySelector(selector)!.getBoundingClientRect()
          return { selector, x: r.x, y: r.y, width: r.width, height: r.height }
        })
        return { gap: list.top - tabs.bottom, token: parseFloat(getComputedStyle(bar).marginBottom), list: { x: list.x, y: list.y, width: list.width }, controls }
      })
      // Written before the assertion so a negative-control run retains the old spacing and unaffected controls too.
      await writeFile(testInfo.outputPath(`AEON-1616-measured-${width}-${theme}.md`), JSON.stringify({ width, theme, ...geometry }) + '\n')
      expect(geometry.token, 'the list has a positive desktop spacing token').toBeGreaterThan(0)
      expect(geometry.gap, 'the first card or empty frame follows the tabs at the desktop gap').toBeGreaterThanOrEqual(0)
      expect(geometry.gap, 'no hidden filter or refresh line before the first card or empty frame').toBeLessThanOrEqual(geometry.token)
      return geometry
    }
    const filled = await spacing()
    await page.screenshot({ path: testInfo.outputPath(`AEON-1616-items-${width}-${theme}.png`) })
    const anchors = { heading: page.getByRole('heading', { level: 1, name: 'Decision Desk' }), tabs: page.getByRole('radiogroup', { name: 'Show' }), open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided'), row, slot: page.getByTestId('desk-refresh-slot') }
    const before = await Promise.all(Object.values(anchors).map(control => control.boundingBox()))
    await expectStableControls({ controls: anchors, scrollAreas: { page: page.locator('html'), list }, interactions: [
      { name: 'one 429 keeps the row without a hint', run: async () => { await refresh(429); await expect(hint).toHaveCount(0) } },
      { name: 'second failure shows the quiet hint', run: async () => {
        await refresh(500)
        await expect(hint).toContainText('Refresh failed · retrying · showing')
        await expect(hint).not.toHaveClass(/escalated/)
        if (width === 390) await page.screenshot({ path: testInfo.outputPath(`AEON-1616-hint-first-390-${theme}.png`) })
        const retry = hint.getByRole('button', { name: 'Try again' })
        await retry.focus()
        await expect(hint.locator('.refresh-retry')).toHaveCSS('opacity', '1')
        await page.screenshot({ path: testInfo.outputPath(`AEON-1477-quiet-${width}-${theme}.png`) })
      } },
      { name: 'the outage escalates after sixty seconds', run: async () => {
        // Fixed wall time leaves timer cadence alone: no polling failures are
        // added by this jump. The view's existing injected clock ticks in place.
        await page.clock.setFixedTime(new Date(goodAt.getTime() + 60_001))
        await expect(hint).toHaveClass(/escalated/)
        await expect(hint).toContainText("The desk hasn't refreshed since")
        await expect(hint.getByRole('button', { name: 'Try again' })).toBeVisible()
        await expect(hint.locator('.refresh-retry')).toHaveCSS('opacity', '1')
        if (width === 390) await page.screenshot({ path: testInfo.outputPath(`AEON-1616-hint-escalated-390-${theme}.png`) })
        await page.screenshot({ path: testInfo.outputPath(`AEON-1477-escalated-${width}-${theme}.png`) })
      } },
      { name: 'retry recovers in place', run: async () => {
        failure = 0
        await hint.getByRole('button', { name: 'Try again' }).click()
        await expect(hint).toHaveCount(0)
        await expect(list).toHaveAttribute('aria-busy', 'false')
      } },
      { name: 'five failures escalate without waiting sixty seconds', run: async () => {
        for (let count = 1; count <= 5; count++) {
          await refresh(500)
          if (count === 1) await expect(hint).toHaveCount(0)
          else if (count < 5) await expect(hint).not.toHaveClass(/escalated/)
        }
        await expect(hint).toHaveClass(/escalated/)
      } },
    ].map(interaction => ({ ...interaction, run: async () => {
      await interaction.run()
      expect(await Promise.all(Object.values(anchors).map(control => control.boundingBox()))).toEqual(before)
      expect(await spacing()).toEqual(filled)
    } })) })
    // The brief asks for zero pixels, beyond the shared guard's ±0.5 px limit.
    expect(await Promise.all(Object.values(anchors).map(control => control.boundingBox()))).toEqual(before)

    if (width === 390) {
      // The same unavailable connection leaves the Desk read failed and the shell's live summary paused.
      // Publish through the real footer API so this is the actual CompactNotice, with its own styles/z-index.
      // The scoped fixture retracts its publication before the rest of this existing case continues.
      const notice = page.locator('.compact-notice'), toastHost = page.locator('.toast-host')
      const retry = hint.getByRole('button', { name: 'Try again', exact: true })
      const controls = { heading: anchors.heading, tabs: anchors.tabs, open: anchors.open, decided: anchors.decided, row, list }
      const controlsAt = await Promise.all(Object.values(controls).map(control => control.boundingBox()))
      const stacks: { state: string; hint: Awaited<ReturnType<typeof box>>; obstacle: Awaited<ReturnType<typeof box>>; gap: number; retryHit: boolean }[] = []
      const above = async (state: string, obstacle: Locator) => {
        await expect(hint).toBeVisible()
        await expect(obstacle).toBeVisible()
        await expect.poll(async () => {
          const h = await box(hint), o = await box(obstacle)
          return o.y - h.y - h.height
        }, `${state}: eight pixels between the hint and the overlay stack`).toBe(8)
        const h = await box(hint), o = await box(obstacle)
        expect(h.y, `${state}: the whole hint stays on screen`).toBeGreaterThanOrEqual(0)
        const retryHit = await retry.evaluate(button => {
          const r = button.getBoundingClientRect()
          return document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2) === button
        })
        expect(retryHit, `${state}: nothing covers the centre of Try again`).toBe(true)
        expect(await Promise.all(Object.values(controls).map(control => control.boundingBox())), `${state}: zero pixels of movement in the Desk`).toEqual(controlsAt)
        stacks.push({ state, hint: h, obstacle: o, gap: o.y - h.y - h.height, retryHit })
      }
      let toastIds: number[] = []
      await expectStableControls({ controls, scrollAreas: { page: page.locator('html'), list }, interactions: [
        { name: 'paused updates stack below the escalated hint', run: async () => {
          await page.evaluate(async updatedAt => {
            // @ts-expect-error Vite resolves the app's Vue instance.
            const { effectScope } = await import('/node_modules/.vite/deps/vue.js')
            // @ts-expect-error Vite resolves browser source URLs.
            const { useFooterSummary } = await import('/src/lib/footerSummary.ts')
            // @ts-expect-error Vite resolves browser source URLs.
            const { useDecisionDesk } = await import('/src/stores/decisionDesk.ts')
            const desk = useDecisionDesk()
            const scope = effectScope()
            scope.run(() => useFooterSummary(() => null, () => ({ updatedAt, paused: desk.failures > 0 })))
            Object.assign(window, { deskPhoneNoticeStop: () => scope.stop() })
          }, goodAt.getTime())
          await expect(notice).toHaveText('Updates paused · retrying')
          await above('paused notice', notice)
          await page.screenshot({ path: testInfo.outputPath(`AEON-1616-hint-with-notice-390-${theme}.png`) })
        } },
        { name: 'a growing toast stack stays below the hint', run: async () => {
          toastIds = await page.evaluate(async () => {
            // @ts-expect-error Vite resolves browser source URLs.
            const { toast } = await import('/src/lib/toast.ts')
            return ['The connection could not be refreshed.', 'The last Desk items are still here.'].map(message => toast(message, { sticky: true }))
          })
          await expect(toastHost.locator('.toast')).toHaveCount(2)
          await above('toast stack', toastHost)
          const n = await box(notice), h = await box(hint)
          expect(n.y - h.y - h.height, 'the paused notice remains below the hint too').toBeGreaterThanOrEqual(8)
          await page.screenshot({ path: testInfo.outputPath(`AEON-1616-hint-with-toasts-390-${theme}.png`) })
          const beforeRead = reads
          await retry.click()
          await expect.poll(() => reads, 'the unobstructed retry still reads the Desk').toBeGreaterThan(beforeRead)
          await expect(list).toHaveAttribute('aria-busy', 'false')
          await above('retry with overlays', toastHost)
        } },
        { name: 'removing the overlays restores the original phone anchor', run: async () => {
          await page.evaluate(async ids => {
            // @ts-expect-error Vite resolves browser source URLs.
            const { dismiss } = await import('/src/lib/toast.ts')
            ids.forEach(dismiss)
            ;(window as unknown as { deskPhoneNoticeStop(): void }).deskPhoneNoticeStop()
          }, toastIds)
          await expect(notice).toHaveCount(0)
          await expect(toastHost.locator('.toast')).toHaveCount(0)
          await expect.poll(() => anchors.slot.boundingBox()).toEqual(before.at(-1))
        } },
      ] })
      await writeFile(testInfo.outputPath(`AEON-1616-stack-390-${theme}.md`), JSON.stringify({ width, theme, stacks, movement: 0 }) + '\n')
    }

    // A genuinely empty desk uses the same first-card slot; the title and tabs remain exactly where they were.
    await refresh(0)
    const fixed = { heading: anchors.heading, tabs: anchors.tabs, open: anchors.open, decided: anchors.decided, slot: anchors.slot }
    const fixedAt = await Promise.all(Object.values(fixed).map(control => control.boundingBox()))
    await expectStableControls({ controls: fixed, scrollAreas: { page: page.locator('html') }, interactions: [{ name: 'last item leaves an empty desk in the same slot', run: async () => {
      empty = true
      const beforeRead = reads
      await page.evaluate(() => window.dispatchEvent(new Event('focus')))
      await expect.poll(() => reads).toBeGreaterThan(beforeRead)
      await expect(list).toHaveAttribute('aria-busy', 'false')
      await expect(page.getByTestId('desk-clear')).toHaveAttribute('data-state', 'clear')
      await expect(page.getByTestId('desk-empty')).toHaveText('Nothing waits for you')
      expect(await Promise.all(Object.values(fixed).map(control => control.boundingBox()))).toEqual(fixedAt)
    } }] })
    const cleared = await spacing()
    expect(cleared).toEqual(filled)
    expect((await box(page.getByTestId('desk-clear'))).y, 'the empty state begins inside the same card hairline').toBe(await list.evaluate(element => element.getBoundingClientRect().y + element.clientTop))
    await onePicture(page, theme)
    await page.screenshot({ path: testInfo.outputPath(`AEON-1616-empty-${width}-${theme}.png`) })
    await writeFile(testInfo.outputPath(`AEON-1616-layout-${width}-${theme}.md`), JSON.stringify({ width, theme, filled, cleared, movement: 0 }) + '\n')
  })
}

async function documentFonts(page: Page) { await page.evaluate(() => document.fonts.ready.then(() => undefined)) }

// AEON-1477 / AEON-1616 (Tick): wide rows keep their inline filter and hint; narrow rows show the stamp line
// only on Decided and overlay refresh feedback. Switching tabs never moves the title, tabs or round action.
async function mockFailingDesk(page: Page) {
  await page.clock.setFixedTime(new Date('2026-10-17T13:58:00Z'))
  await mockEmptyDesk(page, 'light', true)
  const world = { failure: 0, reads: 0 }
  await page.route(url => url.pathname === '/api/decision-desk/projection', route => {
    world.reads++
    return world.failure ? route.fulfill({ status: world.failure, json: { error: 'Unavailable' } }) : route.fallback()
  })
  await page.goto('/decision-desk')
  await expect(page.getByTestId('desk-row-u:accounts-1477')).toBeVisible()
  await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')
  await documentFonts(page)
  // Two failed focus refreshes raise the quiet hint (one alone stays silent).
  const failTwice = async () => {
    world.failure = 500
    for (let attempt = 0; attempt < 2; attempt++) {
      const before = world.reads
      await page.evaluate(() => window.dispatchEvent(new Event('focus')))
      await expect.poll(() => world.reads).toBeGreaterThan(before)
      await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')
    }
    await expect(page.getByTestId('desk-refresh-hint')).toContainText('Refresh failed · retrying · showing')
  }
  return { world, failTwice }
}

async function box(locator: Locator) { return (await locator.boundingBox())! }

for (const width of [1440, 1024, 390]) {
  test(`AEON-1477 Open and Decided keep controls still at ${width}, with and without the hint`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    const { failTwice } = await mockFailingDesk(page)
    const list = page.getByTestId('desk-list'), row = page.getByTestId('desk-row-u:accounts-1477'), hint = page.getByTestId('desk-refresh-hint')
    const tabs = page.getByRole('radiogroup', { name: 'Show' }), stamps = page.getByRole('radiogroup', { name: 'Stamp' }), slot = page.getByTestId('desk-refresh-slot')
    const wide = width >= 1024
    const controls = { heading: page.getByRole('heading', { level: 1, name: 'Decision Desk' }), tabs, open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided') }
    const controlsAt = await Promise.all(Object.values(controls).map(control => control.boundingBox()))
    const unchanged = async () => expect(await Promise.all(Object.values(controls).map(control => control.boundingBox()))).toEqual(controlsAt)
    const crop = async (name: string) => page.screenshot({ path: testInfo.outputPath(`AEON-1477-fix4-${name}-${width}.png`), clip: { x: 0, y: 0, width, height: Math.min(900, (await box(list)).y + 180) } })
    const sweep = async (label: string, hinted: boolean) => {
      const listAt = (await box(list)).y, rowAt = (await box(row)).y
      if (hinted) await crop('open-hint')
      await page.getByTestId('desk-tab-decided').click()
      await expect(stamps).toBeVisible()
      await expect(page.getByTestId('desk-empty')).toBeVisible()
      await unchanged()
      const t = await box(tabs), f = await box(stamps), s = await box(slot)
      if (wide) {
        expect((await box(list)).y, `${width} ${label}: the inline filter adds no height`).toBe(listAt)
        expect(Math.abs(f.y + f.height / 2 - (t.y + t.height / 2)), `${width} ${label}: the filter sits on the tabs' line`).toBeLessThanOrEqual(0.5)
        expect(f.x, `${width} ${label}: the filter follows the tabs`).toBeGreaterThanOrEqual(t.x + t.width + 10)
        expect(s.x, `${width} ${label}: the hint slot follows the filter`).toBeGreaterThanOrEqual(f.x + f.width)
        expect(s.y, `${width} ${label}: the hint slot is the row's own height`).toBeGreaterThanOrEqual(t.y - 0.5)
        expect(s.y + s.height).toBeLessThanOrEqual(t.y + t.height + 0.5)
      } else {
        const gap = await page.locator('.dp-bar').evaluate(bar => parseFloat(getComputedStyle(bar).rowGap))
        expect(f.y, `${width} ${label}: the visible filter follows the tabs without an empty hint line`).toBe(t.y + t.height + gap)
        expect((await box(list)).y, `${width} ${label}: only the visible filter adds a line`).toBe(listAt + gap + f.height)
        expect(s.y, `${width} ${label}: feedback is overlaid below the controls`).toBeGreaterThan(t.y + t.height)
      }
      if (hinted) {
        const h = await box(hint)
        if (wide) expect(Math.abs(h.x + h.width - (s.x + s.width)), `${width}: the hint is right-aligned at the row's end`).toBeLessThanOrEqual(0.5)
        if (wide) expect(h.y, `${width}: the hint fits its slot`).toBeGreaterThanOrEqual(s.y - 0.5)
        else expect(h.y + h.height, `${width}: the overlay grows upward above the footer`).toBe(s.y + s.height)
        expect(h.y + h.height).toBeLessThanOrEqual(s.y + s.height + 0.5)
        // A fine pointer keeps the hover/focus reveal: nothing points at the hint here.
        await expect(hint.locator('.refresh-retry')).toHaveCSS('opacity', '0')
        await crop('decided-hint')
      }
      await page.getByTestId('desk-tab-open').click()
      await expect(stamps).toBeHidden()
      await unchanged()
      expect((await box(list)).y, `${width} ${label}: back on Open the list is where it was`).toBe(listAt)
      expect((await box(row)).y).toBe(rowAt)
      return listAt
    }
    const quiet = await sweep('without the hint', false)
    await failTwice()
    expect((await box(list)).y, `${width}: the hint appears in its reserved place`).toBe(quiet)
    expect(await sweep('with the hint', true)).toBe(quiet)
  })
}

// Touch has no hover: Try again shows whenever the hint does, on a 44 × 44 target that adds no height.
test.describe('touch', () => {
  test.use({ hasTouch: true, isMobile: true })
  test('AEON-1477 on touch Try again shows with the hint on a 44 px target', async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 844 })
    const { world, failTwice } = await mockFailingDesk(page)
    const list = page.getByTestId('desk-list'), hint = page.getByTestId('desk-refresh-hint')
    const before = await box(list)
    await failTwice()
    expect((await box(list)).y, 'the hint and its target add no height').toBe(before.y)
    const retry = hint.getByRole('button', { name: 'Try again' })
    await expect(hint.locator('.refresh-retry')).toHaveCSS('opacity', '1')
    await expect(retry).toBeVisible()
    const target = await box(retry)
    expect(target.width, 'Try again is at least 44 px wide').toBeGreaterThanOrEqual(44)
    expect(target.height, 'Try again is at least 44 px tall').toBeGreaterThanOrEqual(44)
    // The whole box answers a tap: each corner hits the button, not a neighbour drawn over it.
    expect(await retry.evaluate(button => {
      const r = button.getBoundingClientRect()
      return [[r.left + 1, r.top + 1], [r.right - 1, r.top + 1], [r.left + 1, r.bottom - 1], [r.right - 1, r.bottom - 1]].map(([x, y]) => button.contains(document.elementFromPoint(x, y)))
    })).toEqual([true, true, true, true])
    await page.screenshot({ path: testInfo.outputPath('AEON-1477-fix4-touch-retry-390.png'), clip: { x: 0, y: 0, width: 390, height: Math.min(844, (await box(list)).y + 180) } })
    world.failure = 0
    await retry.tap()
    await expect(hint).toHaveCount(0)
    expect((await box(list)).y).toBe(before.y)
  })
})

test('AEON-1477 a genuinely failed first load never claims an empty desk', async ({ page }) => {
  await mockEmptyDesk(page, 'light')
  await page.route(url => url.pathname === '/api/decision-desk/projection', route => route.fulfill({ status: 500, json: { error: 'Unavailable' } }))
  await page.goto('/decision-desk')
  await expect(page.getByTestId('desk-failed')).toContainText('The desk couldn’t be loaded.')
  await expect(page.getByTestId('desk-clear')).toHaveCount(0)
  await expect(page.getByTestId('desk-empty')).toHaveCount(0)
  await expect(page.getByTestId('desk-refresh-hint')).toHaveCount(0)
  await expect(page.getByTestId('desk-tab-open')).toContainText('?')
})
// A focus refresh, awaited by its question read; one already in flight would swallow the event.
async function refreshDesk(page: Page, reads: string[]) {
  const count = () => reads.filter(path => path.startsWith('/api/decision-desk?state=open')).length
  await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')
  const before = count()
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect.poll(count).toBeGreaterThan(before)
  await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')
}

async function noSideways(page: Page, step: string) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), `${step}: horizontal scroll`).toBeLessThanOrEqual(0)
}
// Exactly the picture of the current theme is shown and loaded, decorative, with its size set.
async function onePicture(page: Page, theme: 'light' | 'dark') {
  const shown = page.locator(`.all-clear .art img.${theme}`), hidden = page.locator(`.all-clear .art img.${theme === 'light' ? 'dark' : 'light'}`)
  await expect(shown).toBeVisible()
  await expect(hidden).toBeHidden()
  for (const image of [shown, hidden]) {
    await expect(image).toHaveAttribute('alt', '')
    await expect(image).toHaveAttribute('width', '840')
    await expect(image).toHaveAttribute('height', '560')
  }
  await expect.poll(() => shown.evaluate(image => (image as HTMLImageElement).complete && (image as HTMLImageElement).naturalWidth)).toBe(840)
  // Decoded and painted before any screenshot: the picture decodes asynchronously.
  await shown.evaluate(async image => { await (image as HTMLImageElement).decode(); await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))) })
}

for (const [width, theme] of [[1440, 'dark'], [390, 'light']] as const) {
  test(`AEON-1406 empty desk states keep the card still ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    const reads = await mockEmptyDesk(page, theme)
    // D: the first read is held at a barrier, so the reading state is on screen until released.
    let release!: () => void
    const held = new Promise<void>(resolve => { release = resolve })
    await page.route(url => url.pathname === '/api/decision-desk/projection', async route => { await held; await route.fallback() })
    // B is for a minor source only (AEON-1631): key trims are read by the Desk alone.
    let trimsFail = false
    await page.route(url => url.pathname === '/api/key-trim-proposals', route => trimsFail ? route.fulfill({ status: 503, json: { error: 'Unavailable' } }) : route.fallback())
    await page.goto('/decision-desk')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    const frame = page.getByTestId('desk-clear'), quiet = page.getByTestId('desk-unanswered')
    await expect(frame).toHaveAttribute('data-state', 'reading')
    await expect(frame.getByRole('status').filter({ hasText: 'Reading the desk…' })).toBeVisible()
    await expect(page.getByTestId('desk-tab-open')).toHaveText(/^Open\s*$/)
    await expect(page.getByTestId('desk-empty')).toHaveCount(0)
    await expect(frame.locator('img')).toHaveCount(0)
    await noSideways(page, 'D')
    await page.screenshot({ path: testInfo.outputPath(`D-${width}-${theme}.png`) })

    // D keeps A's height: the card, the frame and the tabs stay put when the read arrives.
    const still = { card: page.getByTestId('desk-list'), frame, open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided'),
      heading: page.getByRole('heading', { level: 1, name: 'Decision Desk' }) }
    await expectStableControls({
      controls: still,
      scrollAreas: { page: page.locator('html') },
      interactions: [
        { name: 'D reading → A all clear', run: async () => {
          release()
          await expect(frame).toHaveAttribute('data-state', 'clear')
          await expect(page.getByTestId('desk-empty')).toHaveText('Nothing waits for you')
          await expect(quiet).toHaveText('')
          await expect(page.getByTestId('desk-tab-open')).toHaveText(/^Open\s*0$/)
          await expect(page.locator('.source-warnings')).toHaveCount(0)
          await onePicture(page, theme)
          testInfo.annotations.push({ type: `A height ${width} ${theme}`, description: String((await frame.boundingBox())!.height) })
          await page.screenshot({ path: testInfo.outputPath(`A-${width}-${theme}.png`) })
        } },
      ],
    })
    // B's line has its slot in A: the picture, the line and the slot itself stay put too.
    await expectStableControls({
      controls: { ...still, art: frame.locator('.art'), line: page.getByTestId('desk-empty'), slot: quiet },
      scrollAreas: { page: page.locator('html') },
      interactions: [
        { name: 'A → B a minor source did not answer', run: async () => {
          trimsFail = true
          await refreshDesk(page, reads)
          await expect(frame).toHaveAttribute('data-state', 'incomplete')
          await expect(quiet).toHaveText(/^Key trims didn’t answer, so this may be incomplete\s*·\s*Try again$/)
          // The calm line replaces the source list; there is one Try again, and no alert look.
          await expect(page.getByRole('button', { name: 'Try again', exact: true })).toHaveCount(1)
          await expect(page.locator('.source-warnings')).toHaveCount(0)
          await expect(page.getByTestId('desk-list')).toHaveClass(/\bclear\b/)
          await expect(page.getByTestId('desk-empty')).toHaveText('Nothing waits for you')
          await onePicture(page, theme)
          await page.screenshot({ path: testInfo.outputPath(`B-${width}-${theme}.png`) })
        } },
        { name: 'B Try again → A', run: async () => {
          trimsFail = false
          await page.getByRole('button', { name: 'Try again', exact: true }).click()
          await expect(frame).toHaveAttribute('data-state', 'clear')
          await expect(quiet).toHaveText('')
          await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')
        } },
      ],
    })

    // C: a project scope says so in one plain line, without the picture or a second scope line.
    await page.goto('/decision-desk?project=p-pharos')
    await expect(page.getByTestId('desk-empty')).toHaveText(/^Nothing waits for you in Pharos\s*·\s*Show all$/)
    await expect(page.getByTestId('desk-clear')).toHaveCount(0)
    await expect(page.getByTestId('desk-list').locator('img')).toHaveCount(0)
    await expect(page.getByTestId('desk-scope')).toHaveCount(0)
    const showAll = page.getByTestId('desk-empty').getByRole('link', { name: 'Show all' })
    await expect(showAll).toHaveAttribute('href', '/decision-desk')
    await noSideways(page, 'C')
    await page.screenshot({ path: testInfo.outputPath(`C-${width}-${theme}.png`) })
    await showAll.click()
    await expect(page.getByTestId('desk-clear')).toHaveAttribute('data-state', 'clear')

    await failedDeskFillsItself(page, testInfo, width, theme)
  })
}

// AEON-1631 (Tick's failed-desk design), state E of the cases above: a desk that did not answer never looks clear
// and reads itself again. Risks: the badge counts items while the Open tab shows the all-clear art; the retry never
// comes, comes early or stacks; Try again, the tabs or the rows move when the panel, its note or the stale note change.
// It runs inside the AEON-1406 cases (same widths and themes), so the file keeps its pinned case count (AEON-1619).
const accountRow = (n: number) => ({ id: `accounts-${n}`, kind: 'account_matrix', revision: 1, title: `Confirm the shared agent accounts ${n}`, created_at: '2026-10-21T17:00:00Z', held: false, can_decide: true, href: '/settings/accounts', source: '/api/settings/accounts' })
async function failedDeskFillsItself(page: Page, testInfo: TestInfo, width: number, theme: 'light' | 'dark') {
  // Time stands still unless the test moves it: every retry below happens at a known instant.
  const at = new Date('2026-10-21T17:47:00Z')
  await page.clock.install({ time: at })
  await page.clock.pauseAt(new Date(at.getTime() + 1000))
  // The empty desk stays mocked from the case above; this route answers the projection first.
  // fail: the read errors; partial: the badge counts two items the list does not carry; good: both rows.
  let mode: 'fail' | 'partial' | 'good' = 'fail', reads = 0
  await page.route(url => url.pathname === '/api/decision-desk/projection', route => {
    reads++
    if (mode === 'fail') return route.fulfill({ status: 503, json: { error: 'Unavailable' } })
    return route.fulfill({ json: { items: mode === 'good' ? [accountRow(1), accountRow(2)] : [], counts: { open: 2, held: 0, chores: 0 }, has_more: false, as_of: at.toISOString() } })
  })
  await page.goto('/decision-desk')
  await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
  const panel = page.getByTestId('desk-failed'), retry = panel.getByRole('button', { name: 'Try again', exact: true })
  const retrying = panel.getByText('Retrying automatically', { exact: true }), stillNot = panel.getByText('Still not loading. Your items are safe; try again later.', { exact: true })
  const neverClear = async (step: string) => {
    await expect(page.getByTestId('desk-clear'), `${step}: no all-clear frame`).toHaveCount(0)
    await expect(page.getByText('Nothing waits for you'), `${step}: no all-clear line`).toHaveCount(0)
    await expect(page.getByTestId('desk-list').locator('img'), `${step}: no all-clear art`).toHaveCount(0)
  }
  await expect(panel).toHaveAttribute('data-retry', 'retrying')
  // The notice offer decides within its own bound before the list shows; move past it once.
  await page.clock.runFor(1000)
  await expect(panel.locator('.failed-line')).toHaveText('The desk couldn’t be loaded.')
  await expect(retrying).toBeVisible()
  await expect(stillNot).toBeHidden()
  await expect(retry).toBeEnabled()
  await neverClear('first read failed')
  await documentFonts(page)
  await page.screenshot({ path: testInfo.outputPath(`AEON-1631-failed-${width}-${theme}.png`) })

  // The shared stability guard waits for animation frames, which a paused clock never paints, so the boxes are
  // read directly here and must not move at all.
  const boxes = (controls: Record<string, Locator>) => Promise.all(Object.entries(controls).map(async ([name, control]) => [name, await control.boundingBox()] as const))
  const still = { open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided'), retry, icon: panel.locator('svg').first() }
  const panelAt = await boxes(still)
  mode = 'partial'
  const beforeCount = reads
  await page.clock.runFor(1999); expect(reads, 'no retry before 3 s').toBe(beforeCount)
  await page.clock.runFor(1); await expect.poll(() => reads, 'one retry at 3 s').toBe(beforeCount + 1)
  await expect(panel.locator('.failed-line')).toHaveText('2 items are waiting, but the list couldn’t be loaded.')
  // The tab shows the badge's own count beside the panel that says the list did not load.
  await expect(page.getByTestId('desk-tab-open')).toHaveText(/^Open\s*2$/)
  await expect(retrying).toBeVisible()
  await expect(retry).toBeEnabled()
  await neverClear('badge two, list empty')
  expect(await boxes(still), 'the count sentence moves neither Try again nor the tabs').toEqual(panelAt)
  await noSideways(page, 'E count')
  await page.screenshot({ path: testInfo.outputPath(`AEON-1631-count-${width}-${theme}.png`) })
  // The 10 s retry fills the rows; the list starts where the panel did.
  mode = 'good'
  const listAt = (await box(page.getByTestId('desk-list'))).y, before = reads
  await page.clock.runFor(9999); expect(reads, 'no retry before 10 s').toBe(before)
  await page.clock.runFor(1); await expect.poll(() => reads, 'one retry at 10 s').toBe(before + 1)
  expect(await boxes({ open: still.open, decided: still.decided }), 'the tabs stay while rows replace the panel').toEqual(panelAt.slice(0, 2))
  const first = page.getByTestId('desk-row-u:accounts-1'), second = page.getByTestId('desk-row-u:accounts-2')
  await expect(first).toBeVisible(); await expect(second).toBeVisible()
  await expect(panel).toHaveCount(0)
  await neverClear('rows arrived')
  expect((await box(page.getByTestId('desk-list'))).y).toBe(listAt)
  await page.screenshot({ path: testInfo.outputPath(`AEON-1631-filled-${width}-${theme}.png`) })

  // Rows from this visit stay when a later read misses the desk, with a quiet note in its reserved slot.
  const stale = page.getByTestId('desk-stale'), list = page.getByTestId('desk-list')
  const rows = { open: still.open, list, first, second, slot: page.getByTestId('desk-refresh-slot') }
  await expect(list).toHaveAttribute('aria-busy', 'false')
  const rowsAt = await boxes(rows)
  mode = 'partial'
  const staleCount = reads
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect.poll(() => reads).toBeGreaterThan(staleCount)
  await expect(stale).toBeVisible()
  await expect(stale).toHaveText(/^Showing the list from \d{2}:\d{2}\s*·\s*Try again$/)
  await expect(stale.getByRole('button', { name: 'Try again', exact: true })).toBeVisible()
  await expect(list).toHaveAttribute('aria-busy', 'false')
  await expect(first).toBeVisible(); await expect(second).toBeVisible()
  await neverClear('stale rows')
  expect(await boxes(rows), 'the stale note takes its reserved slot').toEqual(rowsAt)
  await noSideways(page, 'E stale')
  await page.screenshot({ path: testInfo.outputPath(`AEON-1631-stale-${width}-${theme}.png`) })
  // The 3 s retry reads the desk again and the note leaves its slot as quietly.
  mode = 'good'
  const goodCount = reads
  await page.clock.runFor(2999); expect(reads, 'no stale retry before 3 s').toBe(goodCount)
  await page.clock.runFor(1); await expect.poll(() => reads, 'one stale retry at 3 s').toBe(goodCount + 1)
  await expect(stale).toHaveCount(0)
  await expect(list).toHaveAttribute('aria-busy', 'false')
  expect(await boxes(rows), 'the note leaves without moving the rows').toEqual(rowsAt)
}
