// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1003 (AEON-994 package 3): Numbers · Simple. Risks: switching the window or
// level moves a control or a tile's Learn button; Learn cannot be opened, pinned
// and closed from the keyboard; the verdicts or the summary disagree with the numbers.
import { expect, test, type Locator, type Page, type Route } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { controlStability } from './control-stability'
import { deliveryMetrics, mockDelivery } from './delivery-numbers-fixtures'

async function setup(page: Page, options: { theme?: 'light' | 'dark'; lang?: 'en' | 'de' } = {}) {
  const work = fixtures()
  work.preferences.theme = { choice: options.theme ?? 'light' }
  await mockWork(page, work)
  if (options.lang === 'de') { const data = settingsData(); data.profile.locale = 'de-AT'; await mockSettings(page, data) }
  let answer: { status: number; body: unknown } = { status: 200, body: deliveryMetrics() }
  await mockDelivery(page, async () => answer)
  return {
    answer: (status: number, body: unknown) => { answer = { status, body } },
    metrics: (metricOptions: Parameters<typeof deliveryMetrics>[0] = {}) => { answer = { status: 200, body: deliveryMetrics(metricOptions) } },
  }
}
const head = (page: Page) => page.locator('.dl-head')
const simpleTile = (page: Page, name: string) => page.getByTestId('delivery-simple').getByRole('listitem').filter({ has: page.getByRole('heading', { name, level: 4 }) })

const copy = (lang: 'en' | 'de') => lang === 'de'
  ? {
      window: 'Zeitraum der Diagramme', level: 'Detailgrad', days: (n: number) => `${n} Tage`,
      warning: 'Nicht gespeichert. Gilt nur hier.', retrySave: 'Erneut speichern',
      learn: 'Lernen: Wie lange die PR-Checks dauern', last: 'Lernen: Voller Testlauf jede Nacht', tile: 'Wie lange die PR-Checks dauern',
      summary: 'Letzte 7 Tage gegen die 7 davor', failed: 'Die Lieferzahlen konnten nicht geladen werden.', notLoaded: 'Nicht geladen', retry: 'Erneut versuchen',
    }
  : {
      window: 'Chart window', level: 'Level of detail', days: (n: number) => `${n} days`,
      warning: 'Not saved. The choice stays on this page.', retrySave: 'Save again',
      learn: 'Learn: How long the PR checks take', last: 'Learn: Full test run each night', tile: 'How long the PR checks take',
      summary: 'Last 7 days vs. the 7 before', failed: 'Delivery numbers could not be loaded.', notLoaded: 'Not loaded', retry: 'Retry',
    }

// Risk: a refused save shows as a second element that takes room or covers a control. It is one plain line with
// an inline Retry INSIDE the fixed "Updated …" line, the same box in both states (AEON-541, AEON-1003 round 7).
const updatedLine = /^(Updated|Aktualisiert) \S+/
for (const lang of ['en', 'de'] as const) {
  test(`a failed preference save keeps the choice and shows it with a retry inside the ${lang === 'de' ? 'German' : 'English'} Updated line`, async ({ page }) => {
    test.setTimeout(90_000)
    const text = copy(lang)
    await page.setViewportSize({ width: 1440, height: 1000 })
    await setup(page, { lang })
    let reject = true
    await page.route('**/api/preferences/delivery*', async route => {
      if (route.request().method() !== 'PUT') return route.fallback()
      if (reject) return route.fulfill({ status: 500, json: { error: 'not saved' } })
      return route.fallback()
    })
    await page.goto('/p/AEON/delivery')
    const windows = head(page).getByRole('radiogroup', { name: text.window })
    const levels = head(page).getByRole('radiogroup', { name: text.level })
    const chosen = windows.getByRole('radio', { name: text.days(30) })
    const line = page.getByTestId('delivery-updated')
    const warning = line.getByTestId('delivery-pref-error')
    const simple = page.getByTestId('delivery-simple')
    await expect(windows.getByRole('radio', { name: text.days(7) })).toHaveAttribute('aria-checked', 'true')
    await expect(line).toHaveText(updatedLine)
    await expect(page.getByTestId('delivery-pref-error')).toHaveCount(0)
    const learn = simple.getByRole('button', { name: text.learn })
    const last = simple.getByRole('button', { name: text.last })
    // The failure must not push or cover anything: the head, its controls, the line and Learn are measured through the failure and the retry.
    const guard = await controlStability(page, { windows, levels, learn, last, head: head(page), line, status: page.locator('.dl-status') })
    await guard.check(async () => {
      await chosen.click()
      await expect(warning).toBeVisible()
    })
    await expect(chosen).toHaveAttribute('aria-checked', 'true')
    await expect(warning).toContainText(text.warning)
    await expect(page.locator('[data-testid="delivery-pref-error"]')).toHaveCount(1)
    // The whole sentence is readable on a desktop line; the line keeps its place beside the retry.
    expect(await warning.locator('.dl-fail-text').evaluate(el => el.scrollWidth <= el.clientWidth + 1), 'the sentence is not clipped').toBe(true)
    const [lineBox, retryBox] = await Promise.all([line, warning.getByRole('button', { name: text.retrySave, exact: true })].map(item => item.boundingBox()))
    expect(retryBox!.x).toBeGreaterThanOrEqual(lineBox!.x - .5)
    expect(retryBox!.x + retryBox!.width).toBeLessThanOrEqual(lineBox!.x + lineBox!.width + .5)
    reject = false
    await guard.check(async () => {
      await warning.getByRole('button', { name: text.retrySave, exact: true }).click()
      await expect(warning).toHaveCount(0)
    })
    guard.done()
    await expect(line).toHaveText(updatedLine)
    await expect(chosen).toHaveAttribute('aria-checked', 'true')

    // Phone: the same line is one 44 px slot, the sentence wraps inside it, and Learn still stays put through failure and retry.
    await page.setViewportSize({ width: 400, height: 860 })
    reject = true
    await page.reload()
    const phoneWindows = head(page).getByRole('radiogroup', { name: text.window })
    const phoneLevels = head(page).getByRole('radiogroup', { name: text.level })
    const phoneChosen = phoneWindows.getByRole('radio', { name: text.days(90) })
    await expect(phoneWindows.getByRole('radio', { name: text.days(30) })).toHaveAttribute('aria-checked', 'true')
    await expect(page.getByTestId('delivery-summary')).toContainText(lang === 'de' ? 'Letzte 30 Tage gegen die 30 davor' : 'Last 30 days vs. the 30 before')
    const phoneLearn = page.getByTestId('delivery-simple').getByRole('button', { name: text.learn })
    const phoneLast = page.getByTestId('delivery-simple').getByRole('button', { name: text.last })
    const phone = await controlStability(page, { windows: phoneWindows, levels: phoneLevels, learn: phoneLearn, last: phoneLast, head: head(page), line, status: page.locator('.dl-status') })
    await phone.check(async () => {
      await phoneChosen.click()
      await expect(warning).toBeVisible()
    })
    await expect(warning).toContainText(text.warning)
    expect(await warning.locator('.dl-fail-text').evaluate(el => el.scrollHeight <= el.clientHeight + 1), 'the sentence fits its two lines').toBe(true)
    const phoneRetry = warning.getByRole('button', { name: text.retrySave, exact: true })
    const [phoneLine, phoneWarn, phoneLearnBox] = await Promise.all([line, warning, phoneLearn].map(item => item.boundingBox()))
    expect(phoneLine!.height, 'one 44 px slot').toBeGreaterThanOrEqual(44 - .5)
    expect(phoneWarn!.y + phoneWarn!.height).toBeLessThanOrEqual(phoneLine!.y + phoneLine!.height + .5)
    expect(phoneLine!.y + phoneLine!.height).toBeLessThanOrEqual(phoneLearnBox!.y + .5)
    // The retry's touch area (base.css) is at least 44 px and stays inside the line.
    const touch = await phoneRetry.evaluate(el => {
      const style = getComputedStyle(el, '::before'), rect = el.getBoundingClientRect()
      const width = parseFloat(style.width), height = parseFloat(style.height)
      return { width, height, top: rect.y + rect.height / 2 - height / 2, bottom: rect.y + rect.height / 2 + height / 2 }
    })
    expect(touch.width, 'retry touch width').toBeGreaterThanOrEqual(44)
    expect(touch.height, 'retry touch height').toBeGreaterThanOrEqual(44)
    expect(touch.top).toBeGreaterThanOrEqual(phoneLine!.y - .5)
    expect(touch.bottom).toBeLessThanOrEqual(phoneLine!.y + phoneLine!.height + .5)
    reject = false
    await phone.check(async () => {
      await phoneRetry.click()
      await expect(warning).toHaveCount(0)
    })
    phone.done()
    await expect(line).toHaveText(updatedLine)
    await expect(phoneChosen).toHaveAttribute('aria-checked', 'true')
  })
}

test('a failed refresh keeps Learn still through the error and the retry', async ({ page }) => {
  test.setTimeout(120_000)
  for (const width of [1440, 400]) for (const lang of ['en', 'de'] as const) {
    const text = copy(lang)
    await page.setViewportSize({ width, height: width === 400 ? 860 : 1000 })
    const world = await setup(page, { lang })
    await page.goto('/p/AEON/delivery')
    const simple = page.getByTestId('delivery-simple')
    const learn = simple.getByRole('button', { name: text.learn })
    const last = simple.getByRole('button', { name: text.last })
    const summary = page.getByTestId('delivery-summary')
    await expect(summary).toContainText(text.summary)
    // Phone source chips wrap to several rows. The status region and Learn must keep that geometry through the error and the retry.
    const guard = await controlStability(page, {
      learn, last, summary, status: page.locator('.dl-status'),
      windows: head(page).getByRole('radiogroup', { name: text.window }),
      levels: head(page).getByRole('radiogroup', { name: text.level }),
    })
    world.answer(500, { error: 'boom' })
    await guard.check(async () => {
      await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
      await expect(page.getByRole('alert')).toContainText(text.failed)
      await expect(simple.locator('.s-val.empty').first()).toHaveText(text.notLoaded)
      await expect(summary).toContainText(text.failed)
    })
    world.metrics({})
    await guard.check(async () => {
      await page.getByRole('button', { name: text.retry, exact: true }).click()
      await expect(page.getByRole('alert')).toHaveCount(0)
      await expect(summary).toContainText(text.summary)
      await expect(simple.getByRole('listitem').filter({ has: page.getByRole('heading', { name: text.tile, level: 4 }) }).locator('.s-val')).toHaveText('16min')
    })
    guard.done()
  }
})

// Risk: a refused save and a failed read stand together. The save failure is the Updated line itself and the
// read error sits on the status row below it; each Retry works on its own, in either order, and neither the
// line, the read alert nor either retry moves or resizes when the other failure comes or goes (AEON-541).
test('a refused save and a failed read together keep both alerts and both retries, and Learn still', async ({ page }) => {
  test.setTimeout(240_000)
  let reject = false
  const refuseSave = async (route: Route) => {
    if (route.request().method() !== 'PUT') return route.fallback()
    if (reject) return route.fulfill({ status: 500, json: { error: 'not saved' } })
    return route.fallback()
  }
  for (const width of [1440, 800, 400]) for (const lang of ['en', 'de'] as const) {
    const text = copy(lang)
    const phone = width === 400
    await page.setViewportSize({ width, height: phone ? 860 : 1000 })
    const world = await setup(page, { lang })
    reject = false
    await page.route('**/api/preferences/delivery*', refuseSave)
    await page.goto('/p/AEON/delivery')
    const simple = page.getByTestId('delivery-simple')
    const learn = simple.getByRole('button', { name: text.learn })
    const last = simple.getByRole('button', { name: text.last })
    const summary = page.getByTestId('delivery-summary')
    const windows = head(page).getByRole('radiogroup', { name: text.window })
    const line = page.getByTestId('delivery-updated')
    const saveAlert = line.getByTestId('delivery-pref-error'), loadAlert = page.getByTestId('delivery-load-error')
    const saveRetry = saveAlert.getByRole('button', { name: text.retrySave, exact: true })
    const loadRetry = loadAlert.getByRole('button', { name: text.retry, exact: true })
    await expect(summary).toContainText(text.summary)
    const guard = await controlStability(page, {
      learn, last, summary, head: head(page), line, windows,
      levels: head(page).getByRole('radiogroup', { name: text.level }),
    })
    const refresh = () => page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
    const choose = (days: number) => windows.getByRole('radio', { name: text.days(days) }).click()
    // A retry's slot: its box and its alert's box, in document coordinates. The first sighting of each
    // is the reference; every later sighting, alone or together with the other failure, must match it.
    const boxOf = (item: Locator) => item.evaluate(el => {
      const rect = el.getBoundingClientRect()
      return { x: rect.x + window.scrollX, y: rect.y + window.scrollY, width: rect.width, height: rect.height }
    })
    const slots: Record<string, Awaited<ReturnType<typeof boxOf>>> = {}
    const inSlot = async (name: 'save' | 'read', alert: Locator, retry: Locator, when: string) => {
      const now = { alert: await boxOf(alert), retry: await boxOf(retry) }
      for (const part of ['retry', 'alert'] as const) {
        const key = `${name} ${part}`
        const first = slots[key] ??= now[part]
        for (const side of ['x', 'y', 'width', 'height'] as const) {
          expect(Math.abs(now[part][side] - first[side]), `${width} ${lang} ${key} ${side} ${when}`).toBeLessThanOrEqual(.5)
        }
      }
    }
    const saveSlot = (when: string) => inSlot('save', saveAlert, saveRetry, when)
    const readSlot = (when: string) => inSlot('read', loadAlert, loadRetry, when)
    // The save failure sits in the Updated line, the read alert on the status row below it, both above Learn.
    const bothShown = async () => {
      await expect(saveAlert).toBeVisible(); await expect(loadAlert).toBeVisible()
      await expect(saveAlert).toContainText(text.warning); await expect(loadAlert).toContainText(text.failed)
      await expect(saveRetry).toBeVisible(); await expect(loadRetry).toBeVisible()
      const [lineBox, save, load, saveButton, loadButton, learnBox] = await Promise.all([line, saveAlert, loadAlert, saveRetry, loadRetry, learn].map(item => item.boundingBox()))
      expect(save!.x, `${width} ${lang} save failure inside the line`).toBeGreaterThanOrEqual(lineBox!.x - .5)
      expect(save!.x + save!.width, `${width} ${lang} save failure inside the line`).toBeLessThanOrEqual(lineBox!.x + lineBox!.width + .5)
      expect(save!.y, `${width} ${lang} save failure inside the line`).toBeGreaterThanOrEqual(lineBox!.y - .5)
      expect(save!.y + save!.height, `${width} ${lang} save failure inside the line`).toBeLessThanOrEqual(lineBox!.y + lineBox!.height + .5)
      expect(lineBox!.y + lineBox!.height, `${width} ${lang} the line ends before the read alert`).toBeLessThanOrEqual(load!.y + .5)
      for (const [alert, button, name] of [[save, saveButton, 'save'], [load, loadButton, 'read']] as const) {
        expect(button!.x, `${name} retry inside its alert`).toBeGreaterThanOrEqual(alert!.x - .5)
        expect(button!.x + button!.width, `${name} retry inside its alert`).toBeLessThanOrEqual(alert!.x + alert!.width + .5)
        expect(button!.y, `${name} retry inside its alert`).toBeGreaterThanOrEqual(alert!.y - .5)
        expect(button!.y + button!.height, `${name} retry inside its alert`).toBeLessThanOrEqual(alert!.y + alert!.height + .5)
        expect(alert!.y + alert!.height, `${name} alert above Learn`).toBeLessThanOrEqual(learnBox!.y + .5)
      }
      if (phone) {
        expect(loadButton!.width, 'read retry touch width').toBeGreaterThanOrEqual(44); expect(loadButton!.height, 'read retry touch height').toBeGreaterThanOrEqual(44)
        // The save retry is text in the line; its 44 px touch area (base.css) lies inside the 44 px line.
        const touch = await saveRetry.evaluate(el => { const style = getComputedStyle(el, '::before'); return { width: parseFloat(style.width), height: parseFloat(style.height) } })
        expect(touch.width, 'save retry touch width').toBeGreaterThanOrEqual(44); expect(touch.height, 'save retry touch height').toBeGreaterThanOrEqual(44)
        expect(lineBox!.height, 'the line holds the touch area').toBeGreaterThanOrEqual(44 - .5)
      }
    }
    const bothSlots = async (when: string) => { await bothShown(); await saveSlot(when); await readSlot(when) }

    // The save fails first, then the read: the save retry is measured alone, then with the read failure.
    reject = true
    await guard.check(async () => { await choose(30); await expect(saveAlert).toBeVisible(); await saveSlot('alone, before the read fails') })
    world.answer(500, { error: 'boom' })
    await guard.check(async () => { await refresh(); await bothSlots('with both failures (save first)') })
    // The save retry works alone: the read error stays, and its retry keeps the slot it had beside the save warning.
    reject = false
    await guard.check(async () => { await saveRetry.click(); await expect(saveAlert).toHaveCount(0); await expect(loadAlert).toBeVisible(); await readSlot('alone, after the save recovers') })
    // The read retry works alone.
    world.metrics({})
    await guard.check(async () => { await loadRetry.click(); await expect(loadAlert).toHaveCount(0); await expect(summary).not.toContainText(text.failed) })

    // The read fails first, then the save; a read retry that fails again keeps both.
    world.answer(500, { error: 'boom' })
    await guard.check(async () => { await refresh(); await expect(loadAlert).toBeVisible(); await readSlot('alone, before the save fails') })
    reject = true
    await guard.check(async () => { await choose(90); await bothSlots('with both failures (read first)') })
    await guard.check(async () => { await loadRetry.click(); await bothSlots('after a read retry that fails again') })
    // The read retry works while the save warning stays; the save retry keeps its slot.
    world.metrics({})
    await guard.check(async () => { await loadRetry.click(); await expect(loadAlert).toHaveCount(0); await expect(saveAlert).toBeVisible(); await saveSlot('alone, after the read recovers') })
    reject = false
    await guard.check(async () => { await saveRetry.click(); await expect(saveAlert).toHaveCount(0) })
    guard.done()
    await page.unroute('**/api/preferences/delivery*', refuseSave)
  }
})

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
