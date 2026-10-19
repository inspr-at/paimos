// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1497 (Tick's approved fuel gauge): the open Agents dial reads quota first — the headline,
// quota left over time with the burn strip, a battery per subscription — and the folded dial
// carries a mini quota-left line for Claude and Codex. The risks: words or chart states that do
// not follow the server's projection, a default range that misses the headline's moment, and
// anything that moves under the pointer (hover, a range step, a live update, loading → ready,
// the mini lines appearing). The outlook is the Go projection's own fixture.
import { test, expect, type Locator, type Page, type Route } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import { dailyPlan, mockDailyPlan } from './agents-daily-fixtures'
import type { QuotaOutlook } from '../src/lib/quotaOutlook'
import { OUTLOOK as scenarios } from './quota-outlook-fixtures'

// The plan's person lives in Vienna (UTC+2 on the fixture's day): the readings' 18:20 UTC is 20:20.
const HEAD = 'Claude lasts until Mon ~20:20, then 3 h without until account-b@ refills at 22:59.'
const SHORT = 'lasts until Mon 20:20 · refill 22:59'

interface Gate { release: () => void }
interface Options { folded?: boolean; selected?: string; outlook?: QuotaOutlook | 'error'; gate?: boolean }
async function setup(page: Page, options: Options = {}) {
  const work = fixtures()
  work.preferences['ui.agents.sections'] = { dial: !options.folded }
  work.preferences['ui.agents.dial'] = { selected: options.selected ?? 'claude' }
  await mockWork(page, work, { admin: true })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  const capacity = capacityWorld()
  data.accounts = (capacity.accounts as unknown as typeof data.accounts).filter(a => ['codex', 'claude', 'cursor'].includes(a.harness)).map(a => ({ ...a, registered_by_principal_id: me.id }))
  await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('account.read', 'account.manage')
    return route.fulfill({ json: answer })
  })
  await mockDailyPlan(page, { example: 'pace' })
  // The plan's daily accounts are the outlook's accounts (one family, the person's own), so the rows
  // reserved while the outlook is read are the rows it then fills.
  const plan = dailyPlan({ example: 'pace' }) as ReturnType<typeof dailyPlan> & { daily_state: Record<string, { accounts: { account_id: string; label: string }[] }> }
  for (const group of scenarios.fast.groups) {
    const state = plan.daily_state[group.harness]
    if (state) state.accounts = group.accounts.map((a, i) => ({ ...state.accounts[Math.min(i, state.accounts.length - 1)]!, account_id: a.account_id, label: a.label }))
  }
  await page.route('**/api/agents/plan', route => route.fulfill({ json: plan }))
  await page.route('**/api/queue', route => route.fulfill({ json: { items: [], manual_order: false, capacity: {} } }))
  const state = { outlook: options.outlook ?? scenarios.fast, reads: 0 }
  let open!: () => void
  const gated = new Promise<void>(resolve => { open = resolve })
  if (!options.gate) open()
  await page.route('**/api/agent-accounts/quota-outlook', async (route: Route) => {
    state.reads++
    await gated
    return state.outlook === 'error' ? route.fulfill({ status: 500, json: { error: 'unavailable' } }) : route.fulfill({ json: state.outlook })
  })
  return { state, gate: { release: open } as Gate }
}
const body = (page: Page) => page.locator('.dial-card')
const control = (page: Page) => page.locator('.cdial')
const row = (page: Page, key: string) => body(page).locator(`.row[data-key="${key}"]`)
const detail = (page: Page, key: string) => body(page).locator(`[data-detail="${key}"]`)
const cell = (page: Page, key: string) => control(page).locator(`.du-cell[data-cell="${key}"]`)
/** A live update: the poller reads again when the browser comes back online. */
const goOnline = (page: Page) => page.evaluate(() => window.dispatchEvent(new Event('online')))
/** Every match shows its words whole: nothing clipped behind an ellipsis or spilled past its slot. */
async function expectWhole(cells: Locator) {
  for (const el of await cells.all()) expect(await el.evaluate(e => e.scrollWidth - e.clientWidth), `${await el.textContent()} is cut`).toBeLessThanOrEqual(0)
}

for (const width of [1440, 1024, 390]) {
  const phone = width < 720
  test(`AEON-1497 ${width}: the detail answers first, the chart follows the projection, and nothing moves`, async ({ page }, info) => {
    test.setTimeout(120_000)
    await page.setViewportSize({ width, height: 1100 })
    await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
    const errors = watchErrors(page)
    const { state } = await setup(page)
    await page.goto('/agents')
    if (phone) { await control(page).locator('.fs-tog').click() }
    const claude = detail(page, 'claude')
    // The rows: the running count beside the name, the group's usage as the sub line with its state icon.
    await expect(row(page, 'claude').locator('.sub')).toHaveText(SHORT)
    await expect(row(page, 'claude').locator('.sub .vw')).toHaveAccessibleName('Too fast')
    await expect(row(page, 'codex').locator('.sub')).toHaveText('enough until refill')
    await expect(row(page, 'cursor').locator('.sub')).toHaveText('Quota not reported · 2 accounts')
    // The headline, the pool in muted ink, the verdict with the safe pace.
    await expect(claude.locator('.ub-hd')).toHaveText(HEAD)
    await expect(claude.locator('.ub-l2')).toHaveText('77 % of the pool used · 3 accounts · sampled 13:10')
    await expect(claude.locator('.ub-v')).toHaveText('Too fast · Sustainable: up to 2.0 %/h (now 2.2 %/h)')
    // The chart: measured, the current pace dashed, the safe pace dotted, the empty stretch named under the axis.
    const plot = claude.locator('.ub-plot')
    await expect(plot.locator('svg .cx-past')).toHaveCount(1)
    await expect(plot.locator('svg .cx-cur')).toHaveCount(1)
    await expect(plot.locator('svg .cx-safe')).toHaveCount(1)
    await expect(plot.locator('svg .cx-empty-t').first()).toHaveText('empty 20:20–22:59')
    await expect(plot.locator('svg .cx-now-t')).toHaveText('now')
    await expect(claude.locator('.ub-info')).toHaveText('Burn: 24 h pace 2.2 %/h · 6 h pace 2.4 %/h')
    // The default range reaches the refill the headline names (Mon 22:59): 2 days ahead.
    const sv = claude.locator(phone ? '.ub-pop .ub-sv' : '.ub-steps .ub-sv')
    if (phone) await claude.locator('.ub-dots').click()
    await expect(sv).toHaveText(phone ? ['1 d', '2 d'] : ['3 d', '2 d'])
    if (phone) await claude.locator('.ub-dots').click()
    // The batteries: what is left, the refill, the forecast; only the account that empties the pool warns.
    if (phone) await claude.locator('.ub-acc-t').click()
    const acc = claude.locator('.ub-acc')
    await expect(acc).toHaveCount(3)
    await expect(acc.nth(2)).toContainText('account-c@')
    await expect(acc.nth(2)).toContainText('69 %')
    await expect(acc.nth(2).locator('.ub-fc')).toHaveText('empty ~Mon 20:20')
    await expect(acc.nth(2).locator('.ub-st')).toHaveText('active')
    await expect(acc.nth(0).locator('.ub-fc')).toHaveText('waits for refill')
    // The forecast never truncates ("empty ~Mon 20:20" whole); the account name yields first.
    await expectWhole(claude.locator('.ub-fc'))
    // Evidence with the rows centred in the viewport, clear of the fixed app status bar.
    const rowsShot = async (name: string) => {
      await claude.locator('.ub-list').evaluate(el => el.scrollIntoView({ block: 'center' }))
      await page.screenshot({ path: info.outputPath(`${width}-light-${name}-rows.png`), animations: 'disabled' })
    }
    await rowsShot('too-fast')
    await expect(claude.getByRole('link', { name: 'Accounts and limits Settings' })).toHaveAttribute('href', '/settings/accounts')
    // The shipped folds follow.
    await expect(claude.getByRole('button', { name: 'Pace' })).toBeVisible()
    await expect(claude.getByRole('button', { name: 'Boost today' })).toBeVisible()
    for (const theme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
      await body(page).screenshot({ path: info.outputPath(`${width}-${theme}-too-fast.png`), animations: 'disabled' })
    }
    await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })

    // 0 px: range steps, hovering hours and a live update keep every control put.
    const pace = claude.getByRole('button', { name: 'Pace' })
    const controls = phone
      ? { dots: claude.locator('.ub-dots'), plot, info: claude.locator('.ub-info'), list: claude.locator('.ub-acc-t'), link: claude.locator('.ub-link a'), pace }
      : { pastLess: claude.getByRole('button', { name: 'One day less back' }), pastMore: claude.getByRole('button', { name: 'One day more back' }), aheadLess: claude.getByRole('button', { name: 'One day less ahead' }), aheadMore: claude.getByRole('button', { name: 'One day more ahead' }), plot, info: claude.locator('.ub-info'), legend: claude.locator('.ub-legend'), battery: acc.nth(0), link: claude.locator('.ub-link a'), pace }
    const step = (name: string, expected: string[]) => async () => {
      if (phone) { await claude.locator('.ub-dots').click(); await claude.locator('.ub-pop').getByRole('button', { name }).click(); await expect(claude.locator('.ub-pop .ub-sv')).toHaveText(expected); await claude.locator('.ub-dots').click(); await expect(claude.locator('.ub-pop')).toHaveCount(0) }
      else { await claude.getByRole('button', { name }).click(); await expect(claude.locator('.ub-steps .ub-sv')).toHaveText(expected) }
    }
    const past = phone ? 1 : 3
    // The data side may retain already public pool lines while concealing an
    // enrollment. Tick's private slots must replace its details in place.
    const privateOutlook = structuredClone(scenarios.fast)
    Object.assign(privateOutlook.groups[0]!.accounts[1]!, { state: 'private', pooled: false, used_percent: null, limit_percent: null, resets_at: null, read_at: null, reading_age_seconds: null, forecast: null })
    const privateAccount = acc.nth(1)
    const privacyControls = { privateRow: privateAccount, name: privateAccount.locator('.ub-an'), battery: privateAccount.locator('.ub-bat'), refill: privateAccount.locator('.ub-rs'), forecast: privateAccount.locator('.ub-fc') }
    await expectStableControls({
      controls: { ...controls, ...privacyControls },
      scrollAreas: { body: body(page) },
      interactions: [
        { name: 'one day more back', run: step('One day more back', [`${past + 1} d`, '2 d']) },
        { name: 'no day ahead hides the projection', run: async () => { await step('One day less ahead', [`${past + 1} d`, '1 d'])(); await step('One day less ahead', [`${past + 1} d`, '0 d'])(); await expect(plot.locator('svg .cx-cur')).toHaveCount(0); await expect(plot.locator('svg .cx-empty-t')).toHaveCount(0) } },
        { name: 'two days ahead again', run: async () => { await step('One day more ahead', [`${past + 1} d`, '1 d'])(); await step('One day more ahead', [`${past + 1} d`, '2 d'])(); await expect(plot.locator('svg .cx-cur')).toHaveCount(1) } },
        { name: 'hover an hour', run: async () => { await plot.scrollIntoViewIfNeeded(); const b = (await plot.boundingBox())!; await page.mouse.move(b.x + b.width * 0.3, b.y + b.height / 2); await expect(claude.locator('.ub-info')).toContainText(/ · \d+ % left · .*measured/) } },
        { name: 'leave it', run: async () => { await page.mouse.move(1, 1); await expect(claude.locator('.ub-info')).toHaveText(/^Burn: 24 h pace/) } },
        { name: 'an account becomes private without moving its slots', run: async () => {
          const reads = state.reads; state.outlook = privateOutlook; await goOnline(page)
          await expect.poll(() => state.reads).toBeGreaterThan(reads)
          await expect(privateAccount.locator('.ub-an')).toHaveText('Private account')
          await expect(privateAccount.locator('.ub-name')).not.toHaveAttribute('data-clip-tip', '')
          await expect(privateAccount.locator('.ub-an svg')).toHaveCount(1)
          await expect(privateAccount.locator('.ub-bat')).toHaveText('—')
          await expect(privateAccount.locator('.ub-rs')).toHaveText('—')
          await expect(privateAccount.locator('.ub-fc')).toHaveText('details hidden')
          await expect(privateAccount.locator('.bat')).toHaveCount(0)
          await expectWhole(claude.locator('.ub-fc'))
          await expect(claude.locator('.ub-hd')).toHaveText('Claude lasts until Mon ~20:20, then 3 h without until another account refills at 22:59.')
          await expect(claude.locator('.private-k')).toHaveText('private')
          await expect(plot.locator('.cx-private')).toHaveCount(1)
          await expect(claude).not.toContainText('account-b@')
          await plot.focus(); await plot.press('Home'); await expect(claude.locator('.ub-info')).not.toContainText('account-b@'); await plot.blur()
          for (const theme of ['light', 'dark'] as const) {
            await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
            await body(page).screenshot({ path: info.outputPath(`${width}-${theme}-private.png`), animations: 'disabled' })
          }
          await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
          await rowsShot('private')
        } },
        { name: 'a live update turns the verdict', run: async () => { const reads = state.reads; state.outlook = scenarios.slow; await goOnline(page); await expect.poll(() => state.reads).toBeGreaterThan(reads); await expect(claude.locator('.ub-hd')).toHaveText('About 23 % will refill unused on Wed. There’s room for more work.'); await expect(row(page, 'claude').locator('.sub')).toHaveText('23 % unused at refill') } },
      ],
    })
    await body(page).screenshot({ path: info.outputPath(`${width}-light-too-slow.png`), animations: 'disabled' })
    // A stale reading says its age in the muted line and in the row's status column.
    const stale = structuredClone(scenarios.fast)
    stale.groups[0]!.pool!.sampled_age_seconds = 3 * 3600
    stale.groups[0]!.accounts[0]!.reading_age_seconds = 3 * 3600
    state.outlook = stale; await goOnline(page)
    await expect(claude.locator('.ub-l2')).toHaveText('77 % of the pool used · 3 accounts · sampled 3 h ago')
    await expect(acc.locator('.ub-st')).toHaveText(['3 h ago', '', 'active'])
    // On phones the status column grows with its words (at least 6.5ch), so an age or "active" never spills.
    if (phone) await expectWhole(claude.locator('.ub-st'))
    await expectWhole(claude.locator('.ub-fc'))
    await rowsShot('stale')
    // An expired window has neither a measured refill marker nor a tooltip
    // claiming that one happened. This is the Go-built response, not a mock
    // with the troublesome events hand-removed.
    state.outlook = scenarios.expired
    await goOnline(page)
    await expect(claude.locator('.ub-l2')).toContainText('Current headroom is unknown')
    await expect(plot.locator('.cx-refill-t').filter({ hasText: 'refilled' })).toHaveCount(0)
    await expect(plot.locator('.cx-past')).toHaveCount(1)
    await plot.focus(); await plot.press('Home'); await plot.press('ArrowUp'); await plot.press('ArrowUp'); await plot.press('ArrowUp')
    await expect(claude.locator('.ub-info')).not.toContainText('refilled')
    await plot.blur()
    // The fixed headline slot discloses a long sentence through the shared
    // tooltip on both hover and keyboard focus, without enlarging its frame.
    const long = structuredClone(scenarios.fast)
    const longLabel = 'Abonnement für langlaufende Hintergrundaufgaben und gemeinsam genutzte Entwicklungsumgebungen '.repeat(3).trim()
    long.groups[0]!.accounts[1]!.label = longLabel
    state.outlook = long; await goOnline(page)
    const headline = claude.locator('.ub-hd')
    const full = `Claude lasts until Mon\u00a0~20:20, then 3\u00a0h without until ${longLabel} refills at 22:59.`
    await expect(headline).toHaveText(full)
    await expect(headline).toHaveAttribute('data-tip', full)
    await headline.hover(); await expect(page.locator('.tooltip')).toHaveText(full)
    await page.mouse.move(1, 1); await page.keyboard.press('Tab'); await headline.focus(); await expect(page.locator('.tooltip')).toHaveText(full)
    await headline.blur()
    // Cursor reports no quota: a quiet chart sentence and dashed rings, never a curve.
    await row(page, 'cursor').locator('.nm').click()
    const cursor = detail(page, 'cursor')
    await expect(cursor.locator('.ub-hd')).toHaveText('Quota not reported· 2 accounts')
    await expect(cursor.locator('.ub-empty')).toHaveText('Cursor doesn’t report quota. Burn appears once it does.')
    await expect(cursor.locator('.ub-plot svg')).toHaveCount(0)
    if (phone) await cursor.locator('.ub-acc-t').click()
    await expect(cursor.locator('.ub-nr')).toHaveText(['Quota not reported', 'Quota not reported'])
    await body(page).screenshot({ path: info.outputPath(`${width}-light-cursor.png`), animations: 'disabled' })
    if (phone) {
      // Every new target a finger uses reaches 44 px.
      for (const target of [cursor.locator('.ub-acc-t'), detail(page, 'cursor').locator('.ub-link a')]) {
        const b = (await target.boundingBox())!
        expect(b.height).toBeGreaterThanOrEqual(44)
      }
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
    expect(errors).toEqual([])
  })
}

for (const width of [1440, 1024]) {
  test(`AEON-1497 ${width}: the folded dial's mini lines sit in the existing gaps (0 px), Claude and Codex only`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 900 })
    await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
    const { gate } = await setup(page, { folded: true, gate: true })
    await page.goto('/agents')
    await expect(control(page).locator('.f-chip')).toHaveCount(3)
    // Measure the control and its units before the outlook answers, then again with the lines in place.
    const units = { dial: control(page), total: control(page).locator('[data-unit="total"]'), claude: cell(page, 'claude'), codex: cell(page, 'codex'), cursor: cell(page, 'cursor'), live: control(page).locator('.f-live') }
    await expectStableControls({
      controls: units,
      scrollAreas: { control: control(page) },
      interactions: [{ name: 'the outlook arrives', run: async () => {
        gate.release()
        if (width >= 1100) await expect(cell(page, 'claude').locator('.mini')).toBeVisible()
        await expect(control(page).locator('[data-unit="claude"]')).toHaveAccessibleName(/lasts until Mon\s20:20 · refill 22:59/)
      } }],
    })
    if (width >= 1100) {
      await expect(cell(page, 'claude').locator('.mini-svg')).toHaveClass(/m-dry/)
      await expect(cell(page, 'codex').locator('.mini')).toBeVisible()
      await expect(cell(page, 'codex').locator('.mini-svg')).not.toHaveClass(/m-dry/)
      await expect(cell(page, 'cursor').locator('.mini')).toHaveCount(0)
      await expect(control(page).locator('.mini')).toHaveCount(2)
      // Hover: the shipped − and + appear over the gap, the line steps aside, an opaque tip says it in words.
      await cell(page, 'claude').locator('.du-mark').hover()
      await expect(cell(page, 'claude').locator('.du-tip')).toHaveText('Claude lasts until Mon 20:20 · refill 22:59')
      await expect.poll(() => cell(page, 'claude').locator('.du-tip').evaluate(el => getComputedStyle(el).opacity)).toBe('1')
      await expect.poll(() => cell(page, 'claude').locator('.mini').evaluate(el => getComputedStyle(el).opacity)).toBe('0')
      await page.locator('.cap-row').screenshot({ path: info.outputPath(`${width}-folded-hover.png`), animations: 'disabled' })
      await page.mouse.move(1, 1)
    } else {
      // Below 1100 px there is no line; the unit's name still says it.
      await expect(control(page).locator('.mini')).toHaveCount(0)
    }
    await page.locator('.cap-row').screenshot({ path: info.outputPath(`${width}-folded.png`), animations: 'disabled' })
  })
}

test('AEON-1497: loading keeps the reserved heights, a failed read says so with Try again, stale readings say their age', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1100 })
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  const { state, gate } = await setup(page, { gate: true, outlook: 'error' })
  await page.goto('/agents')
  const claude = detail(page, 'claude'), pace = claude.getByRole('button', { name: 'Pace' })
  await expect(claude.locator('.ub-sum[aria-busy="true"]')).toBeVisible()
  await expect(row(page, 'claude').locator('.sub .skeleton')).toBeVisible()
  await body(page).screenshot({ path: info.outputPath('1440-light-loading.png'), animations: 'disabled' })
  await expectStableControls({
    controls: { pace, boost: claude.getByRole('button', { name: 'Boost today' }), claudeRow: row(page, 'claude'), codexRow: row(page, 'codex') },
    interactions: [
      { name: 'the read fails', run: async () => { gate.release(); await expect(claude.locator('.err')).toHaveText('Usage couldn’t be read·Try again'); await expect(row(page, 'claude').locator('.sub')).toHaveText('Usage not read') } },
      { name: 'try again', run: async () => {
        const stale = structuredClone(scenarios.fast)
        stale.groups[0]!.pool!.sampled_age_seconds = 3 * 3600
        state.outlook = stale
        await claude.getByRole('button', { name: 'Try again' }).click()
        await expect(claude.locator('.ub-hd')).toHaveText(HEAD)
        await expect(claude.locator('.ub-l2')).toHaveText('77 % of the pool used · 3 accounts · sampled 3 h ago')
      } },
    ],
  })
  await body(page).screenshot({ path: info.outputPath('1440-light-stale.png'), animations: 'disabled' })
})
