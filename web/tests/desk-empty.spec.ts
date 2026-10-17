// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1406 (Tick's approved design): the empty Open tab is a calm picture with one line.
// Risks: the card jumps between reading (D), all clear (A) and a silent source (B); a silent
// source still reads as all clear; both theme pictures show; a narrow phone scrolls sideways.
import { expect, test, type Page } from '@playwright/test'
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

// AEON-1477: a transient failure must never replace good rows, and feedback
// must occupy its existing slot in both themes and at phone/tablet/desk widths.
for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1477 refresh failures keep the items and hint geometry ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    const goodAt = new Date('2026-10-17T13:58:00Z')
    await page.clock.setFixedTime(goodAt)
    await mockEmptyDesk(page, theme, true)
    let failure = 0, reads = 0
    await page.route(url => url.pathname === '/api/decision-desk/projection', route => {
      reads++
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
      await expect(page.getByTestId('desk-first-error')).toHaveCount(0)
      await expect(page.getByTestId('desk-empty')).toHaveCount(0)
    }
    await documentFonts(page)
    const anchors = { open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided'), row, slot: page.getByTestId('desk-refresh-slot') }
    const before = await Promise.all(Object.values(anchors).map(control => control.boundingBox()))
    await expectStableControls({ controls: anchors, scrollAreas: { page: page.locator('html'), list }, interactions: [
      { name: 'one 429 keeps the row without a hint', run: async () => { await refresh(429); await expect(hint).toHaveCount(0) } },
      { name: 'second failure shows the quiet hint', run: async () => {
        await refresh(500)
        await expect(hint).toContainText('Refresh failed · retrying · showing')
        await expect(hint).not.toHaveClass(/escalated/)
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
    } })) })
    // The brief asks for zero pixels, beyond the shared guard's ±0.5 px limit.
    expect(await Promise.all(Object.values(anchors).map(control => control.boundingBox()))).toEqual(before)
  })
}

async function documentFonts(page: Page) { await page.evaluate(() => document.fonts.ready.then(() => undefined)) }

test('AEON-1477 a genuinely failed first load never claims an empty desk', async ({ page }) => {
  await mockEmptyDesk(page, 'light')
  await page.route(url => url.pathname === '/api/decision-desk/projection', route => route.fulfill({ status: 500, json: { error: 'Unavailable' } }))
  await page.goto('/decision-desk')
  await expect(page.getByTestId('desk-first-error')).toContainText('The desk couldn’t be loaded')
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

for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1406 empty desk states keep the card still ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    const reads = await mockEmptyDesk(page, theme)
    // D: the first read is held at a barrier, so the reading state is on screen until released.
    let release!: () => void
    const held = new Promise<void>(resolve => { release = resolve })
    await page.route(url => url.pathname === '/api/decision-desk/projection', async route => { await held; await route.fallback() })
    let approvalsFail = false
    await page.route(url => url.pathname === '/api/approvals', route => approvalsFail ? route.fulfill({ status: 503, json: { error: 'Unavailable' } }) : route.fallback())
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
        { name: 'A → B a source did not answer', run: async () => {
          approvalsFail = true
          await refreshDesk(page, reads)
          await expect(frame).toHaveAttribute('data-state', 'incomplete')
          await expect(quiet).toHaveText(/^Approvals didn’t answer, so this may be incomplete\s*·\s*Try again$/)
          // The calm line replaces the source list; there is one Try again, and no alert look.
          await expect(page.getByRole('button', { name: 'Try again', exact: true })).toHaveCount(1)
          await expect(page.locator('.source-warnings')).toHaveCount(0)
          await expect(page.getByTestId('desk-list')).toHaveClass(/\bclear\b/)
          await expect(page.getByTestId('desk-empty')).toHaveText('Nothing waits for you')
          await onePicture(page, theme)
          await page.screenshot({ path: testInfo.outputPath(`B-${width}-${theme}.png`) })
        } },
        { name: 'B Try again → A', run: async () => {
          approvalsFail = false
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
  })
}
