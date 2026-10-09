// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1006 (AEON-994 package 6): Delivery › Flow modes over the recorded flow and its
// server-sent hints. Risks: a live hint is not read, or its update throws away the time
// the person is viewing; Replay auto-plays more than once, or under reduced motion; the
// play controls, picker or panel move while playing or moving the time; Compare loses a
// set or never says when the target was live.
import { expect, test, type Page, type Route } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { controlStability } from './control-stability'
import { deliveryMetrics, mockDelivery } from './delivery-numbers-fixtures'
import { mockFlow, RUN, ZONE } from './delivery-flow-fixtures'

test.use({ timezoneId: ZONE })

async function setup(page: Page, options: { theme?: 'light' | 'dark'; lang?: 'en' | 'de'; level?: 'simple' | 'expert'; empty?: boolean; record?: boolean } = {}) {
  const work = fixtures()
  work.preferences.theme = { choice: options.theme ?? 'light' }
  work.preferences['delivery:numbers'] = { level: options.level ?? 'simple', window: 7 }
  // The hint travels on the real flow EventSource. The fixture's quiet stream
  // never requests that URL, so a routed hint cannot be followed.
  await mockWork(page, work, { nativeEvents: true })
  if (options.lang === 'de') { const data = settingsData(); data.profile.locale = 'de-AT'; await mockSettings(page, data) }
  await mockDelivery(page, async () => ({ status: 200, body: deliveryMetrics() }), ['delivery.read'])
  return mockFlow(page, { empty: options.empty, record: options.record })
}
const lanes = (page: Page) => page.getByTestId('flow-lanes')
const chip = (page: Page) => page.getByTestId('flow-chip').locator('.shown')
const clock = (page: Page) => page.getByTestId('flow-clock')
const play = (page: Page) => page.getByTestId('flow-play')
const modes = (page: Page) => page.getByTestId('flow-modes')
async function center(page: Page, selector: string) {
  const box = (await page.locator(selector).first().boundingBox())!
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 }
}

test('Live reads the recorded runs, follows a server hint, and keeps the time the person is viewing', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1300 })
  const flow = await setup(page)
  await page.goto('/p/AEON/delivery?view=flow')
  await expect(chip(page)).toHaveText('Live · now 20:25')
  await expect(page.getByTestId('delivery-updated')).toHaveText('Now 20:25 · live')
  await expect(page.getByRole('status').filter({ hasText: 'example' })).toHaveCount(0)
  await expect(page.getByTestId('flow-moment-head')).toContainText('At 20:25 (now) Release 126 is live but not working properly (since 20:14)')
  const table = page.getByTestId('flow-inflight')
  await expect(table.locator('tbody tr')).toHaveCount(4)
  await expect(table.locator('tbody tr').first()).toContainText('the checks, then you: release GO')
  await expect(table.locator('tbody tr').first()).toContainText('healthy ~20:35')
  await expect(lanes(page).getByTestId('flow-avatar')).toHaveCount(1)

  const guard = await controlStability(page, {
    modes: modes(page), chip: page.getByTestId('flow-chip'), follow: page.getByTestId('flow-follow'),
    zoom: page.getByTestId('flow-bar').getByRole('radiogroup', { name: 'Zoom' }), lanes: lanes(page), moment: page.getByTestId('flow-moment'),
  })
  // Only a playhead drag moves the time: "Viewing HH:MM" and "Back to now".
  const pill = await center(page, '[data-testid="flow-playhead"] .ln-phpill')
  await guard.check(async () => { await page.mouse.move(pill.x, pill.y); await page.mouse.down(); await page.mouse.move(pill.x - 150, pill.y, { steps: 8 }); await page.mouse.up() })
  await expect(chip(page)).toHaveText(/^Viewing \d\d:\d\d$/)
  const viewing = await chip(page).textContent()
  await expect(page.getByTestId('flow-follow')).toContainText('Back to now')

  // A hint on the stream reads the flow again; the new "now" does not take the person's time away.
  const reads = flow.reads.filter(r => r.includes('/delivery/flow?')).length
  flow.setNow('20:26')
  await guard.check(async () => { flow.hint('delivery.step', RUN.r126); await expect.poll(() => flow.reads.filter(r => r.includes('/delivery/flow?')).length).toBeGreaterThan(reads) })
  await expect(page.getByTestId('delivery-updated')).toHaveText('Now 20:26 · live')
  await expect(chip(page)).toHaveText(viewing!)
  await guard.check(() => page.getByTestId('flow-follow').click())
  await expect(chip(page)).toHaveText('Live · now 20:26')
  await expect(page.getByTestId('flow-moment-head')).toContainText('At 20:26 (now)')
  guard.done()
})

test.describe('with motion', () => {
  test.use({ reducedMotion: 'no-preference' })
  test('Replay opens at the start of the latest release and auto-plays once per session', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1300 })
    const flow = await setup(page)
    await page.goto('/p/AEON/delivery?view=flow')
    await expect(chip(page)).toHaveText('Live · now 20:25')
    await modes(page).getByRole('radio', { name: 'Replay' }).click()
    await expect(page).toHaveURL(/mode=replay/)
    await expect(page.getByTestId('flow-pick')).toHaveValue(RUN.r126)
    await expect.poll(() => flow.reads.includes(`run:${RUN.r126}`)).toBe(true)
    await expect(page.getByTestId('flow-head')).toContainText('Replay Release 126 · 18:21 to live and healthy 20:34 (2 h 13) in about a minute')
    await expect(play(page)).toHaveAttribute('aria-label', 'Pause')
    await expect.poll(async () => (await clock(page).textContent())?.startsWith('18:21 · 0 min')).toBe(false)
    const guard = await controlStability(page, {
      pick: page.getByTestId('flow-pick'), play: play(page), speed: page.getByTestId('flow-bar').getByRole('radiogroup', { name: 'Speed' }),
      clock: clock(page), follow: page.getByTestId('flow-follow'), lanes: lanes(page), moment: page.getByTestId('flow-moment'),
    })
    await guard.check(() => page.getByTestId('flow-bar').getByRole('radio', { name: '2×' }).click())
    await guard.check(() => play(page).click())
    await expect(play(page)).toHaveAttribute('aria-label', 'Play')
    const paused = await clock(page).textContent()
    await page.waitForTimeout(300)
    await expect(clock(page)).toHaveText(paused!)
    // Where the time went, for the run replayed.
    await expect(page.getByTestId('flow-went')).toContainText('Release 126')
    guard.done()
    // Once per session: back in Replay, the run waits for the person.
    await modes(page).getByRole('radio', { name: 'Live' }).click()
    await expect(chip(page)).toHaveText('Live · now 20:25')
    await modes(page).getByRole('radio', { name: 'Replay' }).click()
    await expect(play(page)).toBeVisible()
    await page.waitForTimeout(1200)
    await expect(play(page)).toHaveAttribute('aria-label', 'Play')
    await expect(clock(page)).toHaveText('18:21 · 0 min in')
  })
})

test('Replay under reduced motion shows the run still; the handle and Shift+arrows move the time', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1300 })
  await setup(page)
  await page.goto('/p/AEON/delivery?view=flow&mode=replay')
  await expect(play(page)).toBeDisabled()
  await expect(page.getByTestId('flow-rm')).toHaveText('Reduced motion: drag the time handle to step through.')
  await expect(page.getByTestId('flow-head')).toContainText('(2 h 13)')
  await expect(page.getByTestId('flow-head')).not.toContainText('in about a minute')
  await page.waitForTimeout(1000)
  await expect(clock(page)).toHaveText('18:21 · 0 min in')
  await lanes(page).focus()
  await page.keyboard.press('Shift+ArrowRight')
  await expect(clock(page)).toHaveText('18:22 · 1 min in')
})

test('Compare races release 126 from step a against the Arion target on one axis', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1300 })
  await setup(page)
  await page.goto(`/p/AEON/delivery?view=flow&mode=compare&run=${RUN.r126}`)
  await expect(page.getByTestId('flow-pick')).toHaveValue(RUN.r126)
  await expect(lanes(page).locator('.fl-ttl')).toHaveText(['Release 126 (a → l)', 'Arion target'])
  await expect(lanes(page).getByTestId('flow-avatar')).toHaveCount(2)
  await expect(page.getByTestId('flow-head')).toContainText('Release 126 vs. the Arion target, queue to live: 1 h 10 instead of 24 min')
  await expect(clock(page)).toHaveText('+0 min')
  await lanes(page).focus()
  await page.keyboard.press('End')
  await expect(clock(page)).toHaveText(/ · target reached$/)
  await expect(page.getByTestId('flow-moment-head')).toContainText(/the Arion target was live after 24 min\./i)
  await expect(page.getByTestId('flow-went').locator('.went')).toHaveCount(2)
})

for (const [width, theme, lang] of [[1440, 'light', 'en'], [1440, 'dark', 'en'], [400, 'light', 'en'], [400, 'dark', 'en'], [1440, 'light', 'de']] as const) {
  test(`Flow modes at ${width} ${theme} ${lang} keep controls still`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 400 ? 2600 : 1500 })
    await setup(page, { theme, lang, level: width === 400 ? 'simple' : theme === 'dark' ? 'expert' : 'simple' })
    for (const mode of ['live', 'replay', 'compare'] as const) {
      await page.goto(`/p/AEON/delivery?view=flow${mode === 'live' ? '' : `&mode=${mode}`}`)
      await expect(page.getByTestId('flow-moment')).toBeVisible()
      const controls = {
        modes: modes(page), follow: page.getByTestId('flow-follow'), lanes: lanes(page), moment: page.getByTestId('flow-moment'),
        ...(mode === 'live' ? { chip: page.getByTestId('flow-chip') } : { pick: page.getByTestId('flow-pick'), play: play(page) }),
      }
      const guard = await controlStability(page, controls)
      await guard.check(async () => { await lanes(page).focus(); await page.keyboard.press('Shift+ArrowRight') })
      await guard.check(async () => { await lanes(page).focus(); await page.keyboard.press('End') })
      await guard.check(() => page.getByTestId('flow-follow').click())
      guard.done()
      if (mode === 'compare') await expect(page.getByTestId('flow-moment-head')).toContainText(/the Arion target was live after 24 min/i)
      await page.mouse.move(0, 0)
      await page.locator('.fl').evaluate(el => el.scrollIntoView({ block: 'start' }))
      await page.locator('.fl').screenshot({ path: info.outputPath(`aeon-994-p6-modes/flow-${mode}-${width}-${theme}-${lang}.png`) })
    }
  })
}

// Risk (AEON-1022): the release record under the card - the full test run and rehearsal timing, the qualification
// evidence reference and the rollback class - is missing, says more than the record reported, or moves the controls
// above it when the time moves, the mode changes or the data arrives. It sits below the card and grows downward.
for (const [width, theme, lang] of [[1440, 'light', 'en'], [1440, 'dark', 'en'], [1024, 'light', 'en'], [1024, 'dark', 'en'], [390, 'light', 'de'], [390, 'dark', 'en']] as const) {
  test(`the release record shows catalogue timing, qualification evidence and rollback class and moves nothing (${width} ${theme} ${lang})`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 390 ? 2800 : 1500 })
    await setup(page, { theme, lang, level: 'simple', record: true })
    // AEON-998: a de-AT profile still uses the one English app language; the German wording is covered by the unit test.
    const copy = { title: 'Release record', catalogue: '22 min · green · usually 21 min', rehearsal: '11 min · green', rollback: 'The previous version can be restarted as it is' }
    const record = page.getByTestId('flow-record')
    for (const mode of ['replay', 'compare', 'live'] as const) {
      await page.goto(`/p/AEON/delivery?view=flow&mode=${mode}`)
      await expect(page.getByTestId('flow-moment')).toBeVisible()
      await expect(record, mode).toBeVisible()
      await expect(record.locator('h3'), mode).toHaveText(copy.title)
      await expect(page.getByTestId('flow-record-catalogue'), mode).toHaveText(copy.catalogue)
      await expect(page.getByTestId('flow-record-rehearsal'), mode).toHaveText(copy.rehearsal)
      await expect(page.getByTestId('flow-record-evidence'), mode).toContainText('AEON-487/comment/native-qualification')
      await expect(page.getByTestId('flow-record-rollback'), mode).toHaveText(copy.rollback)
      await expect(record.locator('dd'), mode).toHaveCount(4)
      // Controls and the card keep their boxes while the time moves; the panel below does not shift either.
      const guard = await controlStability(page, {
        modes: modes(page), follow: page.getByTestId('flow-follow'), lanes: lanes(page), moment: page.getByTestId('flow-moment'), record,
        ...(mode === 'live' ? { chip: page.getByTestId('flow-chip') } : { pick: page.getByTestId('flow-pick'), play: play(page) }),
      })
      await guard.check(async () => { await lanes(page).focus(); await page.keyboard.press('Shift+ArrowRight') })
      await guard.check(async () => { await lanes(page).focus(); await page.keyboard.press('End') })
      await guard.check(() => page.getByTestId('flow-follow').click())
      guard.done()
      // Phones read it in one column and nothing runs past the screen.
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect(overflow, `${mode}: horizontal overflow`).toBeLessThanOrEqual(0)
      await page.mouse.move(0, 0)
      await record.scrollIntoViewIfNeeded()
      await record.screenshot({ path: info.outputPath(`aeon-1022-flowcontract/record-${mode}-${width}-${theme}-${lang}.png`) })
      if (process.env.AEON1061_CAPTURE === '1' && mode === 'live') {
        await page.screenshot({ path: info.outputPath(`aeon-1061/delivery-${width}-${theme}-${lang}.png`), fullPage: true })
      }
    }
  })
}

test('a release no record has reported on says "not recorded" in the same four rows', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1300 })
  await setup(page)
  await page.goto('/p/AEON/delivery?view=flow&mode=replay')
  const record = page.getByTestId('flow-record')
  await expect(record).toBeVisible()
  await expect(record.locator('dd')).toHaveCount(4)
  await expect(record.locator('dd')).toHaveText(['not recorded', 'not recorded', 'not recorded', 'not recorded'])
  await expect(page.getByRole('status').filter({ hasText: 'example' })).toHaveCount(0)
})

// Risk (AEON-1003 round 7, kept with the reviewed AEON-1007 placement): a refused preference save covers or moves the
// Flow mode buttons. The failure takes the headline's fixed slot: with recorded runs, the labelled example and while
// loading, the page head, the Updated line, the headline slot and the Flow mode controls keep their boxes (±0.5 px),
// stay clickable, and the headline returns after the retry.
for (const lang of ['en', 'de'] as const) {
  test(`a refused preference save takes the Flow headline slot and leaves the mode controls in place and clickable (${lang})`, async ({ page }) => {
    test.setTimeout(120_000)
    // AEON-998: a de-AT profile still uses the one English app language.
    const copy = { level: 'Level of detail', expert: 'Expert', warning: 'The window and level could not be saved. This choice stays on this page.', retry: 'Save again', now: 'Now 20:25 · live' }
    let reject = true
    for (const width of [1440, 400]) for (const scenario of ['recorded', 'example', 'loading'] as const) {
      await page.setViewportSize({ width, height: width === 400 ? 2000 : 1300 })
      await setup(page, { lang, empty: scenario === 'example' })
      let open: () => void = () => {}
      const gate = scenario === 'loading' ? new Promise<void>(resolve => { open = resolve }) : null
      const hold = async (route: Route) => { await gate; await route.fallback() }
      if (gate) await page.route(/\/api\/projects\/[^/]+\/delivery\/flow(\?|$)/, hold)
      const refuse = async (route: Route) => route.request().method() === 'PUT' && reject ? route.fulfill({ status: 500, json: { error: 'not saved' } }) : route.fallback()
      await page.route('**/api/preferences/delivery*', refuse)
      reject = true
      await page.goto('/p/AEON/delivery?view=flow')
      const where = `${scenario} ${width} ${lang}`
      const line = page.getByTestId('delivery-updated')
      const headline = page.getByTestId('flow-head')
      const failure = headline.getByTestId('delivery-pref-error')
      const levels = page.locator('.dl-head').getByRole('radiogroup', { name: copy.level })
      const expert = levels.getByRole('radio', { name: copy.expert, exact: true })
      if (scenario === 'recorded') await expect(line, where).toHaveText(copy.now)
      else if (scenario === 'example') await expect(page.locator('.flow-empty'), where).toBeVisible()
      else await expect(page.getByTestId('flow-loading').first(), where).toBeVisible()
      const baseline = (await line.textContent())!.trim()
      // The Updated line names Live's moment and clears in Replay and Compare, so its box is held only while the mode stays Live.
      const held = {
        head: page.locator('.dl-head'), headline, levels, views: page.locator('.dl-views'), modes: modes(page), top: page.locator('.fl-top'),
      }
      const guard = await controlStability(page, { ...held, line })
      // The save is refused. Wherever it shows, the mode buttons still take a real click (a cover would intercept it).
      const anywhere = page.getByTestId('delivery-pref-error')
      await guard.check(async () => { await expert.click(); await expect(anywhere, where).toBeVisible() })
      guard.done()
      const moving = await controlStability(page, held)
      await moving.check(async () => { await modes(page).locator('[data-mode="replay"]').click({ timeout: 5000 }); await expect(page, where).toHaveURL(/mode=replay/) })
      await moving.check(async () => { await modes(page).locator('[data-mode="live"]').click({ timeout: 5000 }); await expect(page, where).not.toHaveURL(/mode=/) })
      moving.done()
      // The failure takes the headline's slot. Back on Live, the Updated line says what it said, and the example stays shown.
      await expect(failure, where).toBeVisible()
      await expect(failure, where).toContainText(copy.warning)
      await expect(anywhere, `${where}: one failure`).toHaveCount(1)
      await expect(line.getByTestId('delivery-pref-error'), `${where}: not in the Updated line`).toHaveCount(0)
      await expect(line, where).toHaveText(baseline)
      if (scenario === 'recorded') await expect(headline.locator('.big'), `${where}: the headline yields its slot`).toHaveCount(0)
      if (scenario === 'example') await expect(page.locator('.flow-empty'), `${where}: the example stays shown`).toBeVisible()
      await expect(expert, where).toHaveAttribute('aria-checked', 'true')
      // The retry works and the headline returns. Live's line is back, so its box is held again.
      reject = false
      const restored = await controlStability(page, { ...held, line })
      await restored.check(async () => { await failure.getByRole('button', { name: copy.retry, exact: true }).click(); await expect(failure, where).toHaveCount(0) })
      restored.done()
      await expect(expert, where).toHaveAttribute('aria-checked', 'true')
      await expect(line, where).toHaveText(baseline)
      if (scenario === 'recorded') await expect(headline.locator('.big'), where).toBeVisible()
      open()
      await page.unroute('**/api/preferences/delivery*', refuse)
      if (gate) await page.unroute(/\/api\/projects\/[^/]+\/delivery\/flow(\?|$)/, hold)
    }
  })
}
