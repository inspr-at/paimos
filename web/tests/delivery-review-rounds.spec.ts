// SPDX-License-Identifier: AGPL-3.0-only
// Risks: absent/open outcomes turn into first-pass successes, a late answer
// crosses a window/project boundary, or new values/tooltips move controls.
import { expect, test, type Page, type TestInfo } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { fixtures, mockWork } from './work-fixtures'
import { deliveryMetrics, mockDelivery } from './delivery-numbers-fixtures'
import { controlStability } from './control-stability'
import { reviewAnswer, longReviewAnswer, type ReviewAnswerKind } from './delivery-review-rounds-fixtures'
import { reviewRuleText } from '../src/lib/reviewRuleText'
import type { WindowDays } from '../src/lib/deliveryNumbers'

const evidence: { width: number; theme: string; language: string; controlsStable: boolean; horizontalOverflow: number }[] = []
// The approved remote browser lane also proves the affected source-mounted units
// and the glyph guard, so new Delivery imports cannot strand those regressions.
test.beforeAll(async ({}, info) => {
  const run = promisify(execFile), options = { encoding: 'utf8' as const, timeout: 60_000, maxBuffer: 1024 * 1024 }
  const units = await run(process.execPath, ['node_modules/vitest/vitest.mjs', 'run', 'tests/delivery-simple.unit.test.ts', 'tests/delivery-pref-owner.unit.test.ts', '--maxWorkers=1', '--no-fileParallelism', '--retry=0'], options)
  const glyphs = await run(process.execPath, ['--test', 'tests/text-glyphs.test.ts'], options)
  await info.attach('delivery-units-and-glyphs', { body: units.stdout + units.stderr + glyphs.stdout + glyphs.stderr, contentType: 'text/plain' })
})
test.afterAll(async ({}, info) => { await writeFile(info.outputPath('review-rounds-checks.json'), JSON.stringify(evidence, null, 2) + '\n') })

function barrier() { let release!: () => void; const promise = new Promise<void>(resolve => { release = resolve }); return { promise, release } }
async function rest(page: Page) { await page.evaluate(async () => { await document.fonts.ready; await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))) }) }
async function shot(page: Page, info: TestInfo, name: string, target = page.getByTestId('review-rounds-tile')) {
  await target.scrollIntoViewIfNeeded(); await rest(page)
  await target.screenshot({ path: info.outputPath(`${name}.png`), animations: 'disabled' })
}
async function setup(page: Page, options: { theme?: 'light' | 'dark'; hold?: boolean } = {}) {
  const world = fixtures()
  world.preferences.theme = { choice: options.theme ?? 'light' }
  world.preferences['delivery:numbers'] = { level: 'expert', window: 7 }
  await mockWork(page, world)
  await mockDelivery(page, () => ({ status: 200, body: deliveryMetrics() }))
  const state = { work: world, kind: 'ready' as ReviewAnswerKind, long: false, status: 200, held: options.hold ? barrier() : null, reads: 0 }
  await page.route('**/api/projects/*/delivery/review-rounds?*', async route => {
    const url = new URL(route.request().url()), project = decodeURIComponent(url.pathname.split('/')[3]!), days = Number(url.searchParams.get('window') ?? 7) as WindowDays
    state.reads++
    const held = state.held
    if (held) await held.promise
    const answer = state.long ? longReviewAnswer(project, days) : reviewAnswer(project, days, state.kind)
    await route.fulfill({ status: state.status, json: state.status === 200 ? { ...answer, reviewer_name: 'REVIEWER-SENTINEL', builder_name: 'BUILDER-SENTINEL', model_name: 'MODEL-SENTINEL' } : { error: 'read failed' } })
  })
  return state
}
const tile = (page: Page) => page.getByTestId('review-rounds-tile')
const chart = (page: Page) => page.getByTestId('review-rounds-trend')
const trigger = (page: Page) => tile(page).locator('.rr-open')
const panel = (page: Page) => page.getByTestId('review-rounds-panel')
async function settled(page: Page) {
  await expect(tile(page).locator('.t-main')).toHaveText('2.0', { timeout: 45_000 })
  await expect(page.locator('.dl .sk')).toHaveCount(0, { timeout: 15_000 })
  await rest(page)
}

for (const width of [1440, 1024, 390, 320]) {
  test(`review rounds at ${width}: tile, weekly trend and neutral panel at rest in both themes`, async ({ page }, info) => {
    test.setTimeout(120_000)
    await page.setViewportSize({ width, height: width <= 390 ? 844 : 1000 })
    const state = await setup(page)
    for (const theme of ['light', 'dark'] as const) {
      state.work.preferences.theme = { choice: theme }
      state.work.preferences['delivery:numbers'] = { level: 'expert', window: 7 }
      await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
      await page.goto('/p/AEON/delivery')
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
      await settled(page)
      await expect(tile(page).locator('.rr-gate')).toHaveText('gate 1.5')
      const type = await tile(page).evaluate(root => ({ main: parseFloat(getComputedStyle(root.querySelector('.t-main')!).fontSize), gate: parseFloat(getComputedStyle(root.querySelector('.rr-gate')!).fontSize) }))
      expect(type.gate).toBeLessThan(type.main)
      await expect(tile(page)).toContainText('4 tickets · 1 still open')
      await expect(tile(page).locator('.t-delta')).toHaveText('−1.0 · better')
      await expect(tile(page)).toContainText('Goal 1.0: ok on the first look')
      const labels = await page.getByTestId('delivery-tiles').locator('.t-label').allTextContents()
      expect(labels.slice(-2)).toEqual(['Escaped defects', 'Review rounds until ok'])
      await shot(page, info, `expert-${width}-${theme}-tile`)
      if (width >= 1024) await shot(page, info, `expert-${width}-${theme}-key-numbers`, page.getByTestId('delivery-tiles'))
      await tile(page).locator('.info').click()
      await expect(page.locator('.dl-tip')).toContainText('Tickets that are still open are counted separately, never as one round.')
      await expect(page.locator('.dl-tip')).not.toContainText('Arion')
      await expect(tile(page).locator('.info')).toHaveAttribute('aria-describedby', /dl-definition-/)
      await page.keyboard.press('Escape')
      await expect(page.locator('.dl-tip')).toHaveCount(0)
      await chart(page).locator('.plot').focus()
      await page.keyboard.press('Home')
      await expect(chart(page).locator('.readout')).toHaveText('Week 42 · design 3.0 · gate 2.0 · 1 ticket')
      await page.keyboard.press('End')
      await expect(chart(page).locator('.readout')).toHaveText('Week 43 · design 1.0 · gate 1.0 · 3 tickets')
      await expect(chart(page).locator('path.g-line.dashed')).toHaveCount(1)
      await expect(chart(page).locator('.g-reference')).toHaveCount(1)
      await expect(chart(page).locator('.g-target')).toHaveCount(0)
      await chart(page).locator('.plot').evaluate(el => (el as HTMLElement).blur())
      await page.mouse.move(3, 3)
      await shot(page, info, `expert-${width}-${theme}-trend`, chart(page))
      await trigger(page).scrollIntoViewIfNeeded()
      const guard = await controlStability(page, { tile: trigger(page), info: tile(page).locator('.info'), action: tile(page).locator('.rr-action') })
      await guard.check(async () => { await trigger(page).click(); await expect(panel(page)).toBeVisible() })
      await expect(panel(page).locator('.rr-key')).toHaveText(['AEON-1718', 'AEON-1719'])
      await expect(panel(page).locator('.rr-counts')).toHaveText(['design 3 · gate 2', 'open · round 2'])
      await expect(panel(page).locator('.rr-rule-count')).toHaveText(['2', '1', '1'])
      expect(await panel(page).locator('.rr-chip').evaluateAll(chips => chips.every(chip => !chip.closest('a')))).toBe(true)
      const internal = await controlStability(page, { close: panel(page).getByRole('button', { name: 'Close Esc' }), row: panel(page).locator('.rr-ticket-link').first(), ...(width <= 520 ? { frame: panel(page) } : {}) })
      await internal.check(async () => {
        await page.keyboard.press('Tab')
        await panel(page).locator('.rr-rules .rr-chip').first().focus()
        await expect(page.locator('.tooltip')).toHaveText(reviewRuleText('U2', 'en'))
      })
      internal.done()
      for (const sentinel of ['REVIEWER-SENTINEL', 'BUILDER-SENTINEL', 'MODEL-SENTINEL']) {
        await expect(panel(page)).not.toContainText(sentinel)
        await expect(tile(page)).not.toContainText(sentinel)
        await expect(chart(page)).not.toContainText(sentinel)
      }
      if (width <= 520) {
        const frame = (await panel(page).boundingBox())!, footer = (await panel(page).locator('.rr-actions').boundingBox())!
        expect(frame.height).toBeCloseTo(844, 0)
        expect(Math.abs(footer.y + footer.height - frame.y - frame.height)).toBeLessThanOrEqual(.5)
        for (const row of await panel(page).locator('.rr-ticket-link, .rr-rule-row').all()) expect((await row.boundingBox())!.height).toBeCloseTo(44, 0)
      }
      await panel(page).getByRole('button', { name: 'Close Esc' }).focus()
      await shot(page, info, `expert-${width}-${theme}-panel`, panel(page))
      await guard.check(async () => { await page.keyboard.press('Escape'); await expect(panel(page)).toHaveCount(0) })
      await expect(trigger(page)).toBeFocused()
      guard.done()
      await page.locator('.dl-head').getByRole('radio', { name: 'Simple', exact: true }).click()
      const simple = page.getByTestId('review-rounds-simple')
      await expect(simple.locator('.s-val')).toHaveText('2.0')
      await expect(simple.locator('.s-trend')).toHaveText('−1.0 · better')
      await expect(simple.locator('.s-name')).toHaveText('Review rounds')
      await expect(simple.locator('.learn, .s-spark, .s-verdict, .s-say')).toHaveCount(0)
      await expect(chart(page)).toHaveCount(0)
      await expect(panel(page)).toHaveCount(0)
      await shot(page, info, `simple-${width}-${theme}`, simple)
      const horizontalOverflow = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)
      expect(horizontalOverflow).toBeLessThanOrEqual(1)
      evidence.push({ width, theme, language: 'en', controlsStable: true, horizontalOverflow })
      // Each theme starts with Expert and a fresh copy of the same known cohort.
      await page.locator('.dl-head').getByRole('radio', { name: 'Expert', exact: true }).click()
    }
    expect(state.reads).toBeGreaterThan(0)
  })
}

test('review sheet scrolling retains its footer and never dismisses a scrolled list', async ({ page }, info) => {
  const state = await setup(page)
  state.long = true
  for (const width of [320, 390]) {
    await page.setViewportSize({ width, height: 844 })
    await page.goto('/p/AEON/delivery')
    await expect(tile(page).locator('.t-main')).toHaveText('3.0', { timeout: 45_000 })
    await expect(page.locator('.dl .sk')).toHaveCount(0)
    await trigger(page).click()
    await expect(panel(page).locator('.rr-ticket-link')).toHaveCount(10)
    await rest(page)
    const body = panel(page).locator('.rr-lists')
    expect(await body.evaluate(el => el.scrollHeight - el.clientHeight)).toBeGreaterThan(0)
    const scrolling = await controlStability(page, { close: panel(page).getByRole('button', { name: 'Close Esc' }), frame: panel(page), row: panel(page).locator('.rr-ticket-link').first() })
    await scrolling.check(async () => {
      await body.evaluate(el => el.scrollTo(0, el.scrollHeight))
      await expect.poll(() => body.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
      await body.evaluate(el => {
        const touch = (y: number) => new Touch({ identifier: 1, target: el, clientX: 80, clientY: y })
        el.dispatchEvent(new TouchEvent('touchstart', { bubbles: true, touches: [touch(180)] }))
        el.dispatchEvent(new TouchEvent('touchend', { bubbles: true, changedTouches: [touch(270)] }))
      })
      await expect(panel(page)).toBeVisible()
    })
    scrolling.done()
    const frame = (await panel(page).boundingBox())!, footer = (await panel(page).locator('.rr-actions').boundingBox())!
    expect(Math.abs(footer.y + footer.height - frame.y - frame.height)).toBeLessThanOrEqual(.5)
    await shot(page, info, `phone-${width}-scrolled-sheet`, panel(page))
    await page.keyboard.press('Escape')
  }
})

test('review round states preserve controls and never replace missing or open outcomes with one', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const state = await setup(page, { hold: true })
  await page.goto('/p/AEON/delivery')
  await expect(tile(page)).toHaveAttribute('aria-busy', 'true')
  await shot(page, info, 'state-loading')
  state.kind = 'no_data'; state.held!.release(); state.held = null
  await expect(tile(page).locator('.t-value')).toHaveText('No data yet')
  await expect(page.locator('.dl .sk')).toHaveCount(0)
  await shot(page, info, 'state-no-data')
  await trigger(page).click()
  await expect(panel(page)).toContainText('No tickets needed a second round.')
  await expect(panel(page)).toContainText('No rules cited yet.')
  await page.keyboard.press('Escape')
  const guard = await controlStability(page, { tile: trigger(page), info: tile(page).locator('.info'), action: tile(page).locator('.rr-action'), windows: page.locator('.dl-head').getByRole('radiogroup', { name: 'Chart window' }) })
  state.kind = 'partial'
  await guard.check(async () => { await page.getByRole('radio', { name: '30 days', exact: true }).click(); await expect(tile(page).locator('.t-main')).toHaveText('2.0') })
  await expect(tile(page).locator('.t-foot')).toContainText('Partial')
  await expect(tile(page).locator('.t-foot')).toContainText('covers 1 of 2 weeks')
  await shot(page, info, 'state-partial')
  state.kind = 'open_only'
  await guard.check(async () => { await page.getByRole('radio', { name: '90 days', exact: true }).click(); await expect(tile(page).locator('.t-main')).toHaveText('–') })
  await expect(tile(page)).toContainText('1 ticket · 1 still open')
  await shot(page, info, 'state-open-only')
  state.status = 503
  await guard.check(async () => { await page.getByRole('radio', { name: '180 days', exact: true }).click(); await expect(tile(page).locator('.t-value')).toHaveText('Not loaded') })
  await expect(tile(page).locator('.t-main')).toHaveCount(0)
  await shot(page, info, 'state-error')
  state.status = 200; state.kind = 'ready'
  await guard.check(async () => { await tile(page).getByRole('button', { name: 'Retry', exact: true }).click(); await expect(tile(page).locator('.t-main')).toHaveText('2.0') })
  guard.done()
})

test('review rounds discard a late answer after the window changes', async ({ page }) => {
  const held = barrier(), requested = barrier(), served = barrier()
  await setup(page)
  await page.route('**/api/projects/*/delivery/review-rounds?window=30', async route => {
    requested.release(); await held.promise
    const old = reviewAnswer('p-aeon', 30)
    old.current.design_mean = 9
    await route.fulfill({ json: old })
    served.release()
  })
  await page.goto('/p/AEON/delivery')
  await settled(page)
  await page.getByRole('radio', { name: '30 days', exact: true }).click()
  await requested.promise
  await page.getByRole('radio', { name: '90 days', exact: true }).click()
  await expect(tile(page).locator('.t-main')).toHaveText('2.0')
  held.release(); await served.promise; await rest(page)
  await expect(tile(page).locator('.t-main')).toHaveText('2.0')
  await expect(page.getByRole('radio', { name: '90 days', exact: true })).toHaveAttribute('aria-checked', 'true')
})

for (const width of [1440, 390]) {
  test(`review rounds German catalog at ${width} keeps the long labels readable`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.goto('/tests/delivery-review-rounds-harness.html')
    await expect(tile(page).locator('.t-main')).toHaveText('2,0', { timeout: 45_000 })
    await expect(tile(page)).toContainText('4 Tickets · 1 noch offen')
    await expect(tile(page)).toContainText('Ziel 1,0: Freigabe beim ersten Blick')
    await shot(page, info, `german-${width}-tile`)
    await trigger(page).click()
    await expect(panel(page)).toContainText('Am häufigsten genannte Regeln')
    await page.keyboard.press('Tab')
    await panel(page).locator('.rr-rules .rr-chip').first().focus()
    await expect(page.locator('.tooltip')).toHaveText(reviewRuleText('U2', 'de'))
    await shot(page, info, `german-${width}-rule-tooltip`, panel(page))
    const germanControls = await controlStability(page, { close: panel(page).getByRole('button', { name: 'Schließen Esc' }), row: panel(page).locator('.rr-ticket-link').first() })
    await germanControls.check(async () => { await panel(page).getByRole('button', { name: 'Schließen Esc' }).focus() })
    germanControls.done()
    await expect(page.locator('.tooltip')).toHaveCount(0)
    if (width <= 520) {
      const frame = (await panel(page).boundingBox())!, footer = (await panel(page).locator('.rr-actions').boundingBox())!
      expect(Math.abs(footer.y + footer.height - frame.y - frame.height)).toBeLessThanOrEqual(.5)
    }
    await shot(page, info, `german-${width}-panel`, panel(page))
    const horizontalOverflow = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)
    expect(horizontalOverflow).toBeLessThanOrEqual(1)
    evidence.push({ width, theme: 'light', language: 'de', controlsStable: true, horizontalOverflow })
  })
}
