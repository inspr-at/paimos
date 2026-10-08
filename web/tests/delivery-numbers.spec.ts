// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1002 (AEON-994 package 2, Simple default since AEON-1003): Project › Delivery. Risks: the window, level or view
// switch moves a control; the person's choices are lost or not saved; a failed read
// shows old or zero numbers; people without delivery.read see the section.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { controlStability } from './control-stability'
import { deliveryMetrics, mockDelivery, type MetricsOptions } from './delivery-numbers-fixtures'

async function setup(page: Page, options: { theme?: 'light' | 'dark'; lang?: 'en' | 'de'; grant?: boolean; prefs?: Record<string, unknown> } = {}) {
  const work = fixtures()
  work.preferences.theme = { choice: options.theme ?? 'light' }
  if (options.prefs) work.preferences['delivery:numbers'] = options.prefs
  await mockWork(page, work)
  if (options.lang === 'de') { const data = settingsData(); data.profile.locale = 'de-AT'; await mockSettings(page, data) }
  let answer: { status: number; body: unknown } = { status: 200, body: deliveryMetrics() }
  let hold: Promise<void> | null = null
  const reads: number[] = []
  await mockDelivery(page, async () => { reads.push(Date.now()); if (hold) await hold; return answer }, options.grant === false ? [] : ['delivery.read'])
  return {
    work, reads,
    answer: (status: number, body: unknown) => { answer = { status, body } },
    metrics: (metricOptions: MetricsOptions) => { answer = { status: 200, body: deliveryMetrics(metricOptions) } },
    holdNext: () => { let release!: () => void; hold = new Promise<void>(resolve => { release = resolve }); return () => { hold = null; release() } },
  }
}
const head = (page: Page) => page.locator('.dl-head')
const tile = (page: Page, label: string) => page.getByRole('listitem').filter({ has: page.locator('.t-label', { hasText: label }) })

test('Delivery is absent without delivery.read, and its address leads to the tickets', async ({ page }) => {
  await setup(page, { grant: false })
  await page.goto('/p/AEON/delivery')
  await expect(page).toHaveURL(/\/p\/AEON\/tickets$/)
  const sections = page.getByRole('tablist', { name: 'Project sections' })
  await expect(sections.getByRole('tab', { name: 'Knowledge' })).toBeVisible()
  await expect(sections.getByRole('tab', { name: 'Delivery' })).toHaveCount(0)
})

test('window, level and view switches keep every control still and are the person’s own', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const world = await setup(page)
  await page.goto('/p/AEON/tickets')
  await page.getByRole('tablist', { name: 'Project sections' }).getByRole('tab', { name: 'Delivery' }).click()
  await expect(page).toHaveURL(/\/p\/AEON\/delivery$/)
  const windows = head(page).getByRole('radiogroup', { name: 'Chart window' })
  const levels = head(page).getByRole('radiogroup', { name: 'Level of detail' })
  const views = head(page).getByRole('tablist', { name: 'Delivery views' })
  // Defaults: Simple and 7 days, until the person chooses.
  await expect(windows.getByRole('radio', { name: '7 days' })).toHaveAttribute('aria-checked', 'true')
  await expect(levels.getByRole('radio', { name: 'Simple' })).toHaveAttribute('aria-checked', 'true')
  // Simple is the default level (AEON-1003); its summary names the window.
  await expect(page.getByTestId('delivery-summary')).toContainText('Last 7 days vs. the 7 before')
  await expect(head(page).getByTestId('delivery-updated')).toContainText('· live')

  const guard = await controlStability(page, {
    title: head(page).getByRole('heading', { name: 'Delivery' }), numbers: views.getByRole('tab', { name: 'Numbers' }), flow: views.getByRole('tab', { name: 'Flow' }),
    windows, first: windows.getByRole('radio', { name: '7 days' }), last: windows.getByRole('radio', { name: '365 days' }),
    levels, simple: levels.getByRole('radio', { name: 'Simple' }), expert: levels.getByRole('radio', { name: 'Expert' }),
  })
  for (const days of [30, 90, 180, 365, 7]) {
    await guard.check(() => windows.getByRole('radio', { name: `${days} days` }).click())
    await expect(windows.getByRole('radio', { name: `${days} days` })).toHaveAttribute('aria-checked', 'true')
    await expect(page.getByTestId('delivery-summary')).toContainText(`Last ${days} days vs. the ${days} before`)
  }
  await guard.check(() => levels.getByRole('radio', { name: 'Expert' }).click())
  await expect(tile(page, 'PR CI run (wall)').locator('.t-value')).toHaveText(/^\d+min$/)
  await expect(page.getByText('Key numbers · last 7 days · change against the 7 days before')).toBeVisible()
  await guard.check(() => levels.getByRole('radio', { name: 'Simple' }).click())
  // Arrow keys move the window choice without leaving the group.
  await windows.getByRole('radio', { name: '7 days' }).focus()
  await guard.check(() => page.keyboard.press('ArrowRight'))
  await expect(windows.getByRole('radio', { name: '30 days' })).toBeFocused()
  await expect(windows.getByRole('radio', { name: '30 days' })).toHaveAttribute('aria-checked', 'true')
  await guard.check(() => levels.getByRole('radio', { name: 'Expert' }).click())
  guard.done()
  // Flow keeps the window switch's slot, hidden; nothing beside it moves.
  const flowGuard = await controlStability(page, {
    title: head(page).getByRole('heading', { name: 'Delivery' }), numbers: views.getByRole('tab', { name: 'Numbers' }), flow: views.getByRole('tab', { name: 'Flow' }),
    controls: head(page).locator('.dl-ctrls'), levels, expert: levels.getByRole('radio', { name: 'Expert' }),
  })
  await flowGuard.check(() => views.getByRole('tab', { name: 'Flow' }).click())
  await expect(page).toHaveURL(/view=flow/)
  await expect(windows).toBeHidden()
  await expect(page.getByText('No flow data recorded yet.')).toBeVisible()
  await flowGuard.check(() => views.getByRole('tab', { name: 'Numbers' }).click())
  await expect(windows).toBeVisible()
  flowGuard.done()

  // Saved server-side for this person, and back after a reload.
  await expect.poll(() => world.work.preferences['delivery:numbers']).toEqual({ level: 'expert', window: 30 })
  await page.reload()
  await expect(windows.getByRole('radio', { name: '30 days' })).toHaveAttribute('aria-checked', 'true')
  await expect(levels.getByRole('radio', { name: 'Expert' })).toHaveAttribute('aria-checked', 'true')
})

test('tiles and charts tell partial, missing and clipped data apart and never show stale numbers', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const world = await setup(page, { prefs: { level: 'expert', window: 30 } })
  const release = world.holdNext()
  await page.goto('/p/AEON/delivery')
  const tiles = page.getByTestId('delivery-tiles')
  await expect(tiles.locator('[aria-busy="true"]')).toHaveCount(10)
  release()
  await expect(tiles.locator('[aria-busy="true"]')).toHaveCount(0)
  // 30 days reach back before the backfill (11 Sept): partial, with the coverage named.
  await expect(tile(page, 'PR CI run (wall)').locator('.t-foot')).toContainText('Partial')
  await expect(tile(page, 'PR CI run (wall)').locator('.t-foot')).toContainText(/covers \d+ of 30 days/)
  await expect(tile(page, 'Merge rounds').locator('.t-value')).toHaveText('2 of 10scripted')
  await expect(tile(page, 'Release → live (csb1)').locator('.t-line').nth(1)).toHaveText('Release 126 · 8 Oct')
  await expect(tile(page, 'Nightly full run').locator('.t-value')).toHaveText('0 of 4nights green')
  await expect(tile(page, 'Nightly full run')).toContainText('Red 4 in a row')
  // The release window before had no release: no comparison, never a zero.
  await expect(tile(page, 'Release → live (csb1)').locator('.t-delta')).toHaveText('No data in the 30 days before')
  // The chart readout names the bucket under the pointer; keys move it.
  const chart = page.getByRole('article').filter({ has: page.getByRole('heading', { name: 'PR CI run (wall)', level: 4 }) })
  await expect(chart.locator('.readout')).toContainText(/^Today · p50/)
  await chart.getByRole('group').focus()
  await page.keyboard.press('Home')
  await expect(chart.locator('.readout')).toHaveText(/ · No data yet$/)
  await page.keyboard.press('End')
  await expect(chart.locator('.readout')).toContainText(/^Today · p50 .* runs$/)
  // A failed refresh replaces every number: nothing old stands in for new.
  world.answer(500, { error: 'boom' })
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
  await expect(page.getByRole('alert')).toContainText('Delivery numbers could not be loaded.')
  await expect(tiles.locator('.t-value.empty')).toHaveCount(10)
  await expect(tiles.locator('.t-value.empty').first()).toHaveText('Not loaded')
  world.metrics({})
  await page.getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(page.getByRole('alert')).toHaveCount(0)
  await expect(tile(page, 'PR CI run (wall)').locator('.t-value')).toHaveText(/^\d+min$/)
})

test('a project without data or without a repository says so in place', async ({ page }) => {
  const world = await setup(page)
  world.metrics({ empty: true })
  await page.goto('/p/AEON/delivery')
  await expect(page.getByRole('status')).toContainText('No delivery data yet.')
  await expect(page.getByRole('status')).toContainText('inspr-at/aeon')
  await expect(page.getByTestId('delivery-summary')).toContainText('No numbers yet.')
  await expect(page.getByTestId('delivery-simple').locator('.s-val.empty').first()).toHaveText('No data yet')
  world.metrics({ noSource: true })
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
  await expect(page.getByRole('status')).toContainText('No repository is linked yet.')
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) for (const lang of ['en', 'de'] as const) {
  if (lang === 'de' && width === 1024) continue
  test(`Delivery numbers at ${width} ${theme} ${lang} keep controls still`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await setup(page, { theme, lang, prefs: { level: 'expert', window: 7 } })
    await page.goto('/p/AEON/delivery')
    const windows = head(page).getByRole('radiogroup', { name: lang === 'de' ? 'Zeitraum der Diagramme' : 'Chart window' })
    const levels = head(page).getByRole('radiogroup', { name: lang === 'de' ? 'Detailgrad' : 'Level of detail' })
    await expect(page.getByTestId('delivery-tiles').locator('[aria-busy="true"]')).toHaveCount(0)
    const guard = await controlStability(page, { windows, levels, views: head(page).getByRole('tablist') })
    await guard.check(() => windows.getByRole('radio', { name: lang === 'de' ? '90 Tage' : '90 days' }).click())
    await guard.check(() => windows.getByRole('radio', { name: lang === 'de' ? '7 Tage' : '7 days' }).click())
    guard.done()
    await page.mouse.move(0, 0)
    await page.locator('.dl').evaluate(el => { for (let p = el.parentElement; p; p = p.parentElement) p.scrollTop = 0; window.scrollTo(0, 0) })
    await page.screenshot({ path: info.outputPath(`aeon-994-p2-shell/delivery-${width}-${theme}-${lang}-top.png`) })
    // The whole page in one picture: as tall as the Delivery section reaches.
    const bottom = await page.locator('.dl').evaluate(el => { let top = el.getBoundingClientRect().bottom; for (let p = el.parentElement; p; p = p.parentElement) top += p.scrollTop; return top + window.scrollY })
    await page.setViewportSize({ width, height: Math.ceil(Math.min(6000, bottom + 80)) })
    await page.locator('.dl').evaluate(el => { for (let p = el.parentElement; p; p = p.parentElement) p.scrollTop = 0; window.scrollTo(0, 0) })
    await page.screenshot({ path: info.outputPath(`aeon-994-p2-shell/delivery-${width}-${theme}-${lang}.png`) })
  })
}
