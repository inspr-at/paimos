// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1016 (Project Arion WP1.1): the Delivery page shows what the plan is steered by. Risks: a v5 reading
// moves a control or its own tile when the window changes; a count is rounded back from a share; a missing
// or partial fact reads as zero or as a finished number; a reading with no target gets a verdict anyway.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { controlStability } from './control-stability'
import { deliveryMetrics, mockDelivery } from './delivery-numbers-fixtures'
import { mockFlow } from './delivery-flow-fixtures'

async function setup(page: Page, options: { theme?: 'light' | 'dark'; prefs?: Record<string, unknown>; metrics?: unknown } = {}) {
  const work = fixtures()
  work.preferences.theme = { choice: options.theme ?? 'light' }
  work.preferences['delivery:numbers'] = options.prefs ?? { level: 'expert', window: 7 }
  await mockWork(page, work)
  await mockDelivery(page, async () => ({ status: 200, body: options.metrics ?? deliveryMetrics() }))
  await mockFlow(page, { empty: true })
}
const head = (page: Page) => page.locator('.dl-head')
const tile = (page: Page, label: string) => page.getByRole('listitem').filter({ has: page.locator('.t-label', { hasText: label }) })
const simpleTile = (page: Page, name: string) => page.getByTestId('delivery-simple').getByRole('listitem').filter({ has: page.getByRole('heading', { name, level: 4 }) })

const READINGS = [
  'Time to first green (commit)', 'Confirmed flaky runs', 'Inferred ejections', 'Extra queue runs, cause unclassified',
  'Runner wait, worst job', 'Preflight red rate', 'Review audit findings', 'Escaped defects',
]

test('the v5 readings sit in their tiles with exact counts and the plan’s targets', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page)
  await page.goto('/p/AEON/delivery')
  const tiles = page.getByTestId('delivery-tiles')
  await expect(tiles.locator('[aria-busy="true"]')).toHaveCount(0)
  await expect(tiles.getByRole('listitem')).toHaveCount(18)
  for (const label of READINGS) await expect(tile(page, label)).toBeVisible()
  // Targets are v5's: no "3 on a reuse hit", release 61 now and 54 later.
  await expect(tile(page, 'PR CI run (wall)').locator('.t-target')).toHaveText('Target 10 min, then 7')
  await expect(tile(page, 'Merge-queue run').locator('.t-target')).toHaveText('Target 10 min, then 7')
  await expect(tile(page, 'Release → live').locator('.t-target')).toHaveText('Target ~61 min + W, then ~54 + W')
  await expect(tiles).not.toContainText('reuse')
  // Required checks stand beside the workflow's green; exact counts, not shares turned back into counts.
  await expect(tile(page, 'Green on first try').locator('.t-line').nth(0)).toHaveText(/^Required checks \d+% · \d+ of \d+$/)
  await expect(tile(page, 'Time to first green (branch)').locator('.t-line').nth(1)).toHaveText(/^\d+ went green · \d+ never green$/)
  await expect(tile(page, 'Time to first green (commit)').locator('.t-line').nth(1)).toHaveText(/^\d+ went green · \d+ never, \d+ superseded$/)
  await expect(tile(page, 'Confirmed flaky runs').locator('.t-line').nth(0)).toHaveText(/^\d+ confirmed · \d+ suspect · \d+ workflow rescues$/)
  await expect(tile(page, 'Inferred ejections').locator('.t-line').nth(0)).toHaveText(/^\d+ ejections · \d+ merged PRs$/)
  await expect(tile(page, 'Inferred ejections').locator('.t-line').nth(1)).toHaveText('Inferred from required checks')
  await expect(tile(page, 'Extra queue runs, cause unclassified').locator('.t-line').nth(1)).toHaveText('Cause not classified yet')
  await expect(tile(page, 'Extra queue runs, cause unclassified').locator('.t-target')).toHaveText('Target set after the causes are classified')
  await expect(tile(page, 'Runner wait, worst job').locator('.t-line').nth(0)).toHaveText(/^p50 [\d.]+\smin · p90 [\d.]+\smin$/)
  await expect(tile(page, 'Preflight red rate').locator('.t-line').nth(1)).toHaveText('Not counted as CI green')
  await expect(tile(page, 'Review audit findings').locator('.t-line').nth(0)).toHaveText(/^\d+ high · \d+ medium · \d+ low · \d+ clean$/)
  await expect(tile(page, 'Escaped defects').locator('.t-value')).toHaveText('1defect')
  await expect(tile(page, 'Escaped defects').locator('.t-target')).toHaveText('Target: falling')
  // Readings without a target say so instead of inventing one.
  await expect(tile(page, 'Preflight red rate').locator('.t-target')).toHaveText('No Arion target: shown beside first-try green')
})

test('a v5 reading never moves its own tile or a control when the window or the level changes', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page)
  await page.goto('/p/AEON/delivery')
  await expect(page.getByTestId('delivery-tiles').locator('[aria-busy="true"]')).toHaveCount(0)
  const windows = head(page).getByRole('radiogroup', { name: 'Chart window' })
  const levels = head(page).getByRole('radiogroup', { name: 'Level of detail' })
  const named: Record<string, ReturnType<typeof tile>> = {}
  for (const label of READINGS) named[label] = tile(page, label)
  const guard = await controlStability(page, {
    windows, levels, views: head(page).getByRole('tablist', { name: 'Delivery views' }),
    ...Object.fromEntries(Object.entries(named).flatMap(([label, locator]) => [[`${label} tile`, locator], [`${label} label`, locator.locator('.t-label')], [`${label} value`, locator.locator('.t-value')], [`${label} source`, locator.locator('.t-foot')]])),
  })
  for (const days of [30, 90, 180, 365, 7]) {
    await guard.check(() => windows.getByRole('radio', { name: `${days} days` }).click())
    await expect(windows.getByRole('radio', { name: `${days} days` })).toHaveAttribute('aria-checked', 'true')
  }
  guard.done()
  // Simple: the new sections keep their controls and their Learn buttons still, too.
  await levels.getByRole('radio', { name: 'Simple' }).click()
  await expect(page.getByRole('heading', { name: 'Keeping it safe', level: 3 })).toBeVisible()
  const learn = simpleTile(page, 'Waiting for a runner').getByRole('button', { name: /^Learn:/ })
  const simpleGuard = await controlStability(page, {
    windows, levels, learn, defectsLearn: simpleTile(page, 'Defects that reached production').getByRole('button', { name: /^Learn:/ }),
    runner: simpleTile(page, 'Waiting for a runner'), audits: simpleTile(page, 'Findings in review audits'),
  })
  for (const days of [30, 90, 7]) await simpleGuard.check(() => windows.getByRole('radio', { name: `${days} days` }).click())
  simpleGuard.done()
})

for (const width of [400, 1024, 1440]) {
  test(`Simple keeps every Learn and section still through every window at ${width}`, async ({ page }) => {
    // Risk (found while building AEON-1016): more numbers make the summary sentences longer, so another window wrapped one
    // line more and every Learn button below moved by a line.
    await page.setViewportSize({ width, height: 900 })
    await setup(page, { prefs: { level: 'simple', window: 7 } })
    await page.goto('/p/AEON/delivery')
    await expect(page.getByTestId('delivery-simple').locator('[aria-busy="true"]')).toHaveCount(0)
    const windows = head(page).getByRole('radiogroup', { name: 'Chart window' })
    const guard = await controlStability(page, {
      firstLearn: simpleTile(page, 'How long the PR checks take').getByRole('button', { name: /^Learn:/ }),
      runnerLearn: simpleTile(page, 'Waiting for a runner').getByRole('button', { name: /^Learn:/ }),
      defectsLearn: simpleTile(page, 'Defects that reached production').getByRole('button', { name: /^Learn:/ }),
      safeHeading: page.getByRole('heading', { name: 'Keeping it safe', level: 3 }),
      summary: page.getByTestId('delivery-summary'),
    })
    for (const days of [30, 90, 180, 365, 7]) await guard.check(() => windows.getByRole('radio', { name: `${days} days` }).click())
    guard.done()
  })
}

test('missing, partial and unwatched facts are said as that, never as zero', async ({ page }) => {
  // A project whose jobs are not read yet: required checks and runner wait have no samples (no data) and say why;
  // audits and defects were never reported (no data), and once watched, none recorded is not a "0 happened".
  const data = deliveryMetrics()
  const find = (key: string) => data.metrics.find(metric => metric.key === key)!
  for (const key of ['required_checks_green', 'runner_wait']) {
    const metric = find(key)
    metric.status = 'no_data'
    metric.reason = '4 runs without job facts yet, so their required checks and runner waits are not counted.'
    for (const window of metric.windows) Object.assign(window, { status: 'no_data', n: 0, value: null, p50: null, p90: null })
  }
  const defects = find('escaped_defects')
  defects.windows[0] = { ...defects.windows[0], status: 'no_data', n: 0, value: null, counts: {} } as never
  const audits = find('review_audits')
  audits.windows[0] = { ...audits.windows[0], status: 'no_data', n: 0, value: null, counts: {}, coverage: { ...audits.windows[0].coverage, covered_days: 0, from: null, full: false } } as never
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { metrics: data })
  await page.goto('/p/AEON/delivery')
  await expect(page.getByTestId('delivery-tiles').locator('[aria-busy="true"]')).toHaveCount(0)
  await expect(tile(page, 'Runner wait, worst job').locator('.t-value')).toHaveText('No data yet')
  const requiredLine = tile(page, 'Green on first try').locator('.t-line').nth(0)
  await expect(requiredLine).toContainText('0 of')
  await expect(requiredLine).toContainText('4 runs without job facts yet')
  // Watched, nothing recorded: "0 recorded". Not watched yet: no data.
  await expect(tile(page, 'Escaped defects').locator('.t-value')).toHaveText('0recorded')
  await expect(tile(page, 'Escaped defects').locator('.t-line').nth(0)).toHaveText('None recorded')
  await expect(tile(page, 'Review audit findings').locator('.t-value')).toHaveText('No data yet')
  // The reason is one hover away, on the definition button.
  await tile(page, 'Runner wait, worst job').getByRole('button', { name: /Definition of/ }).focus()
  await expect(page.getByRole('tooltip')).toContainText('4 runs without job facts yet')
  // Simple says the same in words.
  await page.getByRole('radiogroup', { name: 'Level of detail' }).getByRole('radio', { name: 'Simple' }).click()
  await expect(simpleTile(page, 'Waiting for a runner').locator('.s-val')).toHaveText('No data yet')
  await expect(simpleTile(page, 'Defects that reached production').locator('.s-val')).toHaveText('0recorded')
  await expect(simpleTile(page, 'Findings in review audits').locator('.s-val')).toHaveText('No data yet')
  // No target: no verdict colour or word, only "No target yet".
  await expect(simpleTile(page, 'Defects that reached production').getByTestId('verdict')).toHaveText('No target yet')
  // The section without any target shows no "0 of 0 on target" count.
  await expect(page.getByRole('region', { name: 'Keeping it safe' }).locator('.cnt')).toHaveText('')
})

for (const width of [400, 1440]) for (const theme of ['light', 'dark'] as const) for (const level of ['expert', 'simple'] as const) {
  test(`Delivery v5 readings at ${width} ${theme} ${level}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 400 ? 860 : 1000 })
    await setup(page, { theme, prefs: { level, window: 30 } })
    await page.goto('/p/AEON/delivery')
    const area = page.getByTestId(level === 'expert' ? 'delivery-tiles' : 'delivery-simple')
    await expect(area.locator('[aria-busy="true"]')).toHaveCount(0)
    await expect(page.getByTestId(level === 'expert' ? 'delivery-charts' : 'delivery-simple')).toBeVisible()
    // Phones: every control is at least 44 px, and the page never scrolls sideways.
    if (width === 400) {
      expect((await page.getByRole('radiogroup', { name: 'Chart window' }).getByRole('radio').first().boundingBox())!.height).toBeGreaterThanOrEqual(44)
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
    }
    await page.mouse.move(0, 0)
    await page.locator('.dl').evaluate(el => { for (let p = el.parentElement; p; p = p.parentElement) p.scrollTop = 0; window.scrollTo(0, 0) })
    const bottom = await page.locator('.dl').evaluate(el => { let top = el.getBoundingClientRect().bottom; for (let p = el.parentElement; p; p = p.parentElement) top += p.scrollTop; return top + window.scrollY })
    await page.setViewportSize({ width, height: Math.ceil(Math.min(9000, bottom + 80)) })
    await page.locator('.dl').evaluate(el => { for (let p = el.parentElement; p; p = p.parentElement) p.scrollTop = 0; window.scrollTo(0, 0) })
    await page.screenshot({ path: info.outputPath(`aeon-1016-deliveryfit/delivery-${level}-${width}-${theme}.png`) })
  })
}
