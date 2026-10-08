// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1003 (AEON-994 package 3): Numbers · Simple. Risks: switching the window or
// level moves a control or a tile's Learn button; Learn cannot be opened, pinned
// and closed from the keyboard; the verdicts or the summary disagree with the numbers.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { controlStability } from './control-stability'
import { deliveryMetrics, mockDelivery } from './delivery-numbers-fixtures'

async function setup(page: Page, options: { theme?: 'light' | 'dark'; lang?: 'en' | 'de' } = {}) {
  const work = fixtures()
  work.preferences.theme = { choice: options.theme ?? 'light' }
  await mockWork(page, work)
  if (options.lang === 'de') { const data = settingsData(); data.profile.locale = 'de-AT'; await mockSettings(page, data) }
  await mockDelivery(page, () => ({ status: 200, body: deliveryMetrics() }))
}
const head = (page: Page) => page.locator('.dl-head')
const simpleTile = (page: Page, name: string) => page.getByTestId('delivery-simple').getByRole('listitem').filter({ has: page.getByRole('heading', { name, level: 4 }) })

test('Simple explains each number in plain words, and its controls stay still through every window', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page)
  await page.goto('/p/AEON/delivery')
  const summary = page.getByTestId('delivery-summary')
  await expect(summary).toContainText('None of the 10 numbers with a target is on target yet.')
  await expect(summary).toContainText('Closest: how long a review takes (1.6× the target). Biggest gap: from release to live (about 5× the target).')
  await expect(page.getByRole('heading', { name: 'Making a change ready', level: 3 })).toBeVisible()
  await expect(page.getByTestId('delivery-simple').getByRole('listitem').filter({ has: page.locator('.s-name') })).toHaveCount(10)
  const checks = simpleTile(page, 'How long the PR checks take')
  await expect(checks.locator('.s-val')).toHaveText('16min')
  await expect(checks.getByTestId('verdict')).toHaveText('Far off · about 3× the target')
  await expect(checks.getByTestId('verdict').locator('svg')).toHaveCount(1)
  await expect(checks.locator('.s-dir')).toHaveText('lower is better')
  await expect(checks.locator('.sp-zone')).toHaveCount(1)
  await expect(simpleTile(page, 'Conflicts solved by script').locator('.s-foot')).toHaveText('Partialcovers 4 of 7 days')

  const windows = head(page).getByRole('radiogroup', { name: 'Chart window' })
  const levels = head(page).getByRole('radiogroup', { name: 'Level of detail' })
  const learn = checks.getByRole('button', { name: 'Learn: How long the PR checks take' })
  const guard = await controlStability(page, {
    windows, levels, views: head(page).getByRole('tablist', { name: 'Delivery views' }),
    learn, lastLearn: simpleTile(page, 'Full test run each night').getByRole('button', { name: /^Learn:/ }),
  })
  for (const days of [30, 90, 180, 365, 7]) {
    await guard.check(() => windows.getByRole('radio', { name: `${days} days` }).click())
    await expect(summary).toContainText(`Last ${days} days vs. the ${days} before`)
  }
  guard.done()

  // Learn: focus shows the plain words with the proper terms, Enter pins, Esc closes and keeps focus.
  await learn.focus()
  const tip = page.getByRole('tooltip')
  await expect(tip).toContainText('p50 (median) 16 min: half of the runs were faster than this.')
  await expect(tip).toContainText('In the Expert view: PR CI run (wall) · GitHub App + backfill')
  await expect(learn).toHaveAttribute('aria-expanded', 'true')
  await expect(learn).toHaveAttribute('aria-describedby', 'dl-learn-pr_ci_wall')
  await page.keyboard.press('Enter')
  await page.mouse.move(5, 5)
  await expect(tip).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(tip).toHaveCount(0)
  await expect(learn).toBeFocused()
  await expect(learn).toHaveAttribute('aria-expanded', 'false')
  // Tab to the next Learn: the first closes, the next opens; nothing on the page moved.
  await page.keyboard.press('Enter')
  await expect(tip).toBeVisible()
  await page.keyboard.press('Tab')
  await expect(simpleTile(page, 'Checks green on the first try').getByRole('button', { name: /^Learn:/ })).toBeFocused()
  await expect(page.getByRole('tooltip')).toHaveCount(1)
  await expect(page.getByRole('tooltip')).toContainText('First-attempt green rate: 35% of 161 first attempts ended green.')
})

for (const width of [400, 1440]) for (const theme of ['light', 'dark'] as const) for (const lang of ['en', 'de'] as const) {
  if (lang === 'de' && theme === 'light') continue
  test(`Delivery Simple at ${width} ${theme} ${lang}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 400 ? 860 : 1000 })
    await setup(page, { theme, lang })
    await page.goto('/p/AEON/delivery')
    await expect(page.getByTestId('delivery-summary')).toBeVisible()
    await expect(page.getByTestId('delivery-simple').locator('[aria-busy="true"]')).toHaveCount(0)
    const windows = head(page).getByRole('radiogroup', { name: lang === 'de' ? 'Zeitraum der Diagramme' : 'Chart window' })
    const levels = head(page).getByRole('radiogroup', { name: lang === 'de' ? 'Detailgrad' : 'Level of detail' })
    const learn = page.getByTestId('delivery-simple').getByRole('button', { name: lang === 'de' ? /^Lernen:/ : /^Learn:/ }).first()
    const guard = await controlStability(page, { windows, levels, learn })
    await guard.check(() => windows.getByRole('radio', { name: lang === 'de' ? '90 Tage' : '90 days' }).click())
    await guard.check(() => windows.getByRole('radio', { name: lang === 'de' ? '7 Tage' : '7 days' }).click())
    guard.done()
    // Touch targets: Learn is at least 44 px tall on phones.
    if (width === 400) expect((await learn.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    await page.mouse.move(0, 0)
    await page.locator('.dl').evaluate(el => { for (let p = el.parentElement; p; p = p.parentElement) p.scrollTop = 0; window.scrollTo(0, 0) })
    const bottom = await page.locator('.dl').evaluate(el => { let top = el.getBoundingClientRect().bottom; for (let p = el.parentElement; p; p = p.parentElement) top += p.scrollTop; return top + window.scrollY })
    await page.setViewportSize({ width, height: Math.ceil(Math.min(7000, bottom + 80)) })
    await page.locator('.dl').evaluate(el => { for (let p = el.parentElement; p; p = p.parentElement) p.scrollTop = 0; window.scrollTo(0, 0) })
    await page.screenshot({ path: info.outputPath(`aeon-994-p3-simple/delivery-simple-${width}-${theme}-${lang}.png`) })
    if (width === 1440) {
      await page.setViewportSize({ width, height: 1000 })
      await learn.focus()
      await expect(page.getByRole('tooltip')).toBeVisible()
      await page.screenshot({ path: info.outputPath(`aeon-994-p3-simple/delivery-simple-${width}-${theme}-${lang}-learn.png`) })
    }
  })
}
