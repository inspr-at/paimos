// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1036: the dial with its daily limits (design AEON-1030 draft 6). The folded dial is one row, the
// open dial is master and detail, and nothing a person works with ever moves under the pointer.
import { mkdirSync } from 'node:fs'
import AxeBuilder from '@axe-core/playwright'
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { ACCOUNTS, capacityWorld, NOW } from './capacity-fixtures'
import type { PairingView } from '../src/lib/agentPairing'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import { mockDailyPlan, UNTIL, type DailyPlanOptions } from './agents-daily-fixtures'

const shots = process.env.AEON_1036_SHOTS ?? 'test-results/aeon-1036-capsdial'
interface SetupOptions extends DailyPlanOptions { folded?: boolean; waiting?: number; dial?: Record<string, unknown>; room?: Record<string, number>; manage?: boolean; capacityEdit?: (capacity: ReturnType<typeof capacityWorld>) => void }
async function setup(page: Page, options: SetupOptions = {}) {
  await page.clock.setSystemTime(NOW)
  const work = fixtures()
  work.preferences['ui.agents.sections'] = { dial: !options.folded }
  if (options.dial) work.preferences['ui.agents.dial'] = options.dial
  await mockWork(page, work, { admin: true })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  const capacity = capacityWorld()
  options.capacityEdit?.(capacity)
  data.accounts = (capacity.accounts as unknown as typeof data.accounts).filter(a => ['codex', 'claude', 'cursor'].includes(a.harness)).map(a => ({ ...a, registered_by_principal_id: me.id }))
  const calls = await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('account.read')
    if (options.manage !== false) answer.workspace.permissions.push('account.manage')
    return route.fulfill({ json: answer })
  })
  const backend = await mockDailyPlan(page, options)
  // Start advice per account: the first account of a harness carries that harness's free places.
  const room = options.room ?? { codex: 0, claude: 0, cursor: 0 }
  await page.route('**/api/agent-accounts/capacity', route => {
    const answer = capacity.handle('/api/agent-accounts/capacity', 'GET', null)!.json as { account_id: string }[]
    const seen = new Set<string>()
    return route.fulfill({ json: answer.map(a => {
      const harness = data.accounts.find(account => account.id === a.account_id)?.harness
      const slots = harness && !seen.has(harness) ? room[harness] ?? 0 : 0
      if (harness) seen.add(harness)
      return { ...a, routing: slots ? { rank: 1, available_slots: slots } : { rank: 0, available_slots: 0, wait: { code: 'capacity', run_now_allowed: false } } }
    }) })
  })
  const waiting = options.waiting ?? 1
  const queue = Array.from({ length: waiting }, (_, i) => ({ node_id: `waiting-${i}`, key: `AEON-${i}`, title: 'Waiting work', state: 'open', priority: 'normal', estimate_hours: 1, queued: { model_profile_id: null, waiting: true, wait_reason: '' } }))
  await page.route('**/api/queue', route => route.fulfill({ json: { items: queue, manual_order: false, capacity: {} } }))
  return { work, calls, backend, capacity }
}
const dial = (page: Page) => page.getByRole('region', { name: 'Agents at once' })
const more = (page: Page) => dial(page).getByRole('button', { name: 'One agent more at once' })
const fewer = (page: Page) => dial(page).getByRole('button', { name: 'One agent fewer at once' })
const row = (page: Page, key: string) => dial(page).locator(`.row[data-key="${key}"]`)
const chip = (page: Page, key: string) => dial(page).locator(`.f-chip[data-harness="${key}"]`)
/** The live detail on the right; the same-height copies laid under it for sizing have no data-detail. */
const detail = (page: Page, key: string) => dial(page).locator(`[data-detail="${key}"]`)
const writes = (calls: { path: string; method: string }[]) => calls.filter(c => c.method !== 'GET' && /harness-sessions|\/controls|\/stop|\/interrupt|agent-accounts\/boost/.test(c.path))

test('AEON-1036: folded, the dial is one row as high as the other folded cards and flags only what needs attention', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { backend } = await setup(page, { example: 'over', folded: true })
  await page.goto('/agents')
  const card = dial(page), accounts = page.getByRole('region', { name: 'Accounts and computers' })
  await expect(card.locator('.f-chip')).toHaveCount(3)
  await expect(card.locator('.f-live')).toContainText('11 running (1 waiting)')
  // One row: the sentence, the harnesses and the live state share a line, as high as the folded Accounts card.
  const [dialBox, chipsBox, liveBox, accountsBox] = await Promise.all([card.boundingBox(), card.locator('.f-chips').boundingBox(), card.locator('.f-live').boundingBox(), accounts.boundingBox()])
  expect(Math.abs(dialBox!.height - accountsBox!.height)).toBeLessThanOrEqual(0.5)
  expect(Math.abs(chipsBox!.y + chipsBox!.height / 2 - (liveBox!.y + liveBox!.height / 2))).toBeLessThanOrEqual(2)
  // Over pace is one gold flag on Claude; on pace and no daily limit leave no flag.
  await expect(card.locator('.cap-flag')).toHaveCount(1)
  await expect(chip(page, 'claude').locator('.cap-flag')).toHaveClass(/st-over_pace/)
  await expect(chip(page, 'claude').locator('.cap-flag')).toHaveAccessibleName('Claude · at most 2 · ≤ 60 % used today · 4 percentage points over pace. Open the dial at Claude')
  await expect(chip(page, 'codex').locator('.cap-flag')).toHaveCount(0)
  await expect(chip(page, 'cursor').locator('.cap-flag')).toHaveCount(0)
  // Everything else is hover and keyboard focus: the chip says its limit and today's state.
  const tip = chip(page, 'codex').locator('.chip-tip')
  await expect(tip).toBeHidden()
  await chip(page, 'codex').hover()
  await expect(tip).toBeVisible()
  await expect(tip).toHaveText('Codex · at most 6 · ≤ 68 % used today · on pace')
  await page.mouse.move(700, 600)
  await expect(tip).toBeHidden()
  await chip(page, 'cursor').getByRole('button', { name: /^Cursor: / }).first().focus()
  await expect(chip(page, 'cursor').locator('.chip-tip')).toHaveText('Cursor · no own limit · no daily limit')
  await expect(chip(page, 'cursor')).toHaveAttribute('aria-describedby', /tip-cursor$/)
  expect(backend.puts).toEqual([])
})

test('AEON-1036: at the limit the flag is red and says where new work goes', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { example: 'limit', folded: true })
  await page.goto('/agents')
  const flag = chip(page, 'claude').locator('.cap-flag')
  await expect(flag).toHaveClass(/st-at_limit/)
  await expect(flag).toHaveAccessibleName('Claude · at most 2 · at today’s limit · Codex next. Open the dial at Claude')
  const sample = await flag.evaluate(el => ({ background: getComputedStyle(el).backgroundColor, width: el.getBoundingClientRect().width }))
  expect(sample.background).not.toBe('rgba(0, 0, 0, 0)')
  expect(sample.width).toBe(22)
})

test('AEON-1036: a stale reading never claims the limit, although the server also refuses starts then', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { example: 'stale', folded: true })
  await page.goto('/agents')
  await expect(chip(page, 'claude')).toBeVisible()
  await expect(chip(page, 'claude').locator('.cap-flag')).toHaveCount(0)
  await chip(page, 'claude').hover()
  await expect(chip(page, 'claude').locator('.chip-tip')).toHaveText('Claude · at most 2 · usage not measured right now')
})

test('AEON-1036: "● N running (M waiting) ▾" opens the info area, from the folded dial too, and it is remembered', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { work } = await setup(page, { example: 'pace', folded: true })
  await page.goto('/agents')
  const card = dial(page), toggle = card.locator('.f-live'), info = card.locator('.wh')
  await expect(toggle).toHaveAccessibleName(/11 running \(1 waiting\)/)
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await expect(info).toBeHidden()
  // From the folded dial it opens the dial with the area open.
  await toggle.click()
  await expect(card.locator('.fs-tog')).toHaveAttribute('aria-expanded', 'true')
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')
  await expect(info).toBeVisible()
  await expect(info.locator('.wt-big')).toHaveText(/11\s*running\s*1\s*waiting/)
  await expect(info).toContainText('12 needed now · 11 running + 1 waiting · the dial allows 12')
  await expect(info.getByRole('heading', { name: 'Running and waiting' })).toBeVisible()
  await expect(info.getByRole('heading', { name: 'Accounts' })).toBeVisible()
  await expect(info.getByRole('heading', { name: 'Before every start' })).toBeVisible()
  for (const check of ['Fewer than 12 running', 'The harness is not off or at its limit', 'A free place on an account', 'Under today’s limit']) await expect(info.getByText(check)).toBeVisible()
  await expect.poll(() => work.preferences['ui.agents.dial']).toMatchObject({ info_open: true })
  // Open, the same control shows or hides the area and leaves the dial open.
  await toggle.click()
  await expect(info).toBeHidden()
  await expect(card.locator('.fs-tog')).toHaveAttribute('aria-expanded', 'true')
  await expect.poll(() => work.preferences['ui.agents.dial']).toMatchObject({ info_open: false })
  await toggle.click()
  await expect.poll(() => work.preferences['ui.agents.dial']).toMatchObject({ info_open: true })
  await page.reload()
  await expect(info).toBeVisible()
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')
})

/** Every computer is online and its harnesses ready, except Claude on the first computer: its verification ran out. */
function claudeNeedsVerifying(capacity: ReturnType<typeof capacityWorld>) {
  const computers = capacity.computers as unknown as PairingView[]
  for (const c of computers) {
    const harnesses = [...new Set(c.enrollments.map(e => e.harness))]
    Object.assign(c, {
      connectivity: 'online', revision: 1,
      harness_statuses: Object.fromEntries(harnesses.map(h => [h, 'ready'])), harness_details: Object.fromEntries(harnesses.map(h => [h, { state: 'ready' }])),
      verification_capabilities: Object.fromEntries(harnesses.map(h => [h, { supported: true, policy: 'read_only', reason: '' }])),
    })
  }
  const enrollment = computers[0]!.enrollments.find(e => e.account_id === ACCOUNTS.claude)!
  Object.assign(enrollment, { can_verify: true, verification_state: 'expired', verification_expired_ready: false })
  return { computer: computers[0]!, enrollment }
}

test('AEON-1036: the Accounts tile names what blocks a harness and offers Verify again in place, as Accounts and computers does', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  let blocked!: ReturnType<typeof claudeNeedsVerifying>
  await setup(page, { example: 'pace', dial: { info_open: true }, capacityEdit: capacity => { blocked = claudeNeedsVerifying(capacity) } })
  let posted: unknown = null
  await page.route('**/api/agent-pairing/computers/*/enrollments/*/verify', route => {
    posted = route.request().postDataJSON()
    Object.assign(blocked.enrollment, { verification_state: 'queued', verification_run_id: 'b0000000-0000-4000-8000-000000001036' })
    return route.fulfill({ json: { account_id: blocked.enrollment.account_id, run_id: 'b0000000-0000-4000-8000-000000001036' } })
  })
  await page.goto('/agents')
  const tile = dial(page).locator('.wt-acc'), claude = tile.locator('[data-info-harness="claude"]')
  await expect(claude).toContainText('needs verifying on build-7')
  await expect(tile.locator('[data-info-harness="codex"]')).not.toContainText('verifying')
  const verify = claude.locator('.verify-button')
  await expect(verify).toBeVisible()
  await expect(verify).toHaveText(/Verify again/)
  await expectStableControls({
    controls: { more: more(page), fewer: fewer(page), fold: dial(page).locator('.fs-tog'), toggle: dial(page).locator('.f-live'), verify, mark: claude.locator('.mark'), name: claude.locator('.wa-n') },
    interactions: [{ name: 'verify', run: async () => { await verify.click(); await expect(verify).toHaveAttribute('aria-busy', 'true') } }],
  })
  expect(posted).toEqual({ expected_revision: blocked.computer.revision, expected_verification_run_id: null })
  await expect(claude.locator('.wa-s')).toContainText('Waiting for the computer to start')
})

test('AEON-1036: without the right to manage accounts the Accounts tile names the problem and offers no Verify', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { example: 'pace', manage: false, dial: { info_open: true }, capacityEdit: capacity => { claudeNeedsVerifying(capacity) } })
  await page.goto('/agents')
  await expect(dial(page).locator('.wt-acc [data-info-harness="claude"]')).toContainText('needs verifying on build-7')
  await expect(dial(page).locator('.wt-acc').getByRole('button', { name: 'Verify again' })).toHaveCount(0)
})

test('AEON-1036: the info area states demand against the dial, with and without waiting work', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { example: 'pace', waiting: 3, dial: { info_open: true } })
  await page.goto('/agents')
  const info = dial(page).locator('.wh')
  await expect(dial(page).locator('.f-live')).toContainText('11 running (3 waiting)')
  await expect(info).toContainText('14 needed · the dial allows 12')
  await expect(info).not.toContainText('needed now')
  await page.route('**/api/queue', route => route.fulfill({ json: { items: [], manual_order: false, capacity: {} } }))
  await page.reload()
  await expect(dial(page).locator('.f-live')).toHaveText('11 running')
  await expect(dial(page).locator('.f-live .f-waitn')).toHaveCount(0)
  await expect(info).toContainText('Nothing waiting · the dial allows 12')
  await expect(info.locator('.f-wait')).toHaveCount(0)
})

test('AEON-1036: a flag opens the dial at that harness, and a pick is remembered per person', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { work } = await setup(page, { example: 'limit', folded: true })
  await page.goto('/agents')
  const card = dial(page)
  await chip(page, 'claude').locator('.cap-flag').click()
  await expect(card.locator('.fs-tog')).toHaveAttribute('aria-expanded', 'true')
  await expect(row(page, 'claude')).toHaveClass(/sel/)
  await expect(card.locator('.f-sel[data-sel="claude"]')).toHaveAttribute('aria-pressed', 'true')
  await expect(card.locator('.f-sel[data-sel="claude"]')).toBeFocused()
  await expect(detail(page, 'claude').getByRole('heading')).toHaveText('Claude · daily limit: up to 50 % used')
  await expect.poll(() => work.preferences['ui.agents.dial']).toMatchObject({ selected: 'claude' })
  // Another pick replaces it; a click on the row (not its controls) picks too, a click on its controls does not.
  await row(page, 'codex').locator('.sub').click()
  await expect(row(page, 'codex')).toHaveClass(/sel/)
  await expect(row(page, 'claude')).not.toHaveClass(/sel/)
  await expect(detail(page, 'codex').getByRole('heading')).toHaveText('Codex · daily limit: up to 68 % used')
  await row(page, 'claude').getByRole('radio', { name: 'Off', exact: true }).click()
  await expect(row(page, 'codex')).toHaveClass(/sel/)
  await row(page, 'cursor').locator('.nm').click()
  await expect(detail(page, 'cursor').getByRole('heading')).toHaveText('Cursor · no daily limit')
  await expect.poll(() => work.preferences['ui.agents.dial']).toMatchObject({ selected: 'cursor' })
  await page.reload()
  await expect(row(page, 'cursor')).toHaveClass(/sel/)
})

test('AEON-1036: with no pick the dial selects what needs attention', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { example: 'over' })
  await page.goto('/agents')
  await expect(row(page, 'claude')).toHaveClass(/sel/)
  await expect(dial(page).locator('.row.sel')).toHaveCount(1)
})

test('AEON-1036: with nothing to flag it selects the first harness with a daily limit, and a pick that is no row is ignored', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { example: 'pace', dial: { selected: 'gemini' } })
  await page.goto('/agents')
  await expect(row(page, 'codex')).toHaveClass(/sel/)
  await expect(dial(page).locator('.row.sel')).toHaveCount(1)
})

test('AEON-1036: the detail reads week, today, the bar and the pace state, then lists the accounts read-only', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { example: 'over', dial: { selected: 'claude' } })
  await page.goto('/agents')
  const claude = detail(page, 'claude')
  await expect(claude.getByRole('heading')).toHaveText('Claude · daily limit: up to 60 % used')
  await expect(claude).toContainText('54 % used · 46 % left this week · resets Thu 1 Oct 21:02')
  await expect(claude).toContainText('Today: 14 of 20 percentage points used · 6 left today')
  await expect(claude.getByRole('img', { name: 'Today: 14 of 20 percentage points used; 10 is today’s share.' })).toBeVisible()
  await expect(claude.locator('.cb-gold')).toBeVisible()
  await expect(claude.locator('.cb-tick')).toContainText('pace 10')
  await expect(claude.locator('.cap-st')).toHaveText('4 percentage points over pace')
  await expect(claude).toContainText('Stay on pace · 10 percentage points a day')
  await expect(claude).toContainText('up to 60 % used · 40 % left')
  await expect(claude.locator('.cd-acc1')).toHaveText('markus.barta')
  // Resets show only when the vendor reports them (over pace has none).
  await expect(claude.locator('.cap-rs')).toHaveCount(0)
  await row(page, 'codex').locator('.nm').click()
  const codex = detail(page, 'codex')
  await expect(codex.locator('.cd-acc li')).toHaveCount(3)
  await expect(codex.locator('.cd-acc li').nth(0)).toHaveText('agentone64 % used of 68 %floor 10 %')
  await expect(codex.locator('.cd-acc li').nth(1)).toHaveText('markus22 % used of 30 %floor 20 %')
  await expect(codex.locator('.cd-acc li.now')).toHaveText('agentone64 % used of 68 %floor 10 %')
  await expect(codex.getByRole('link', { name: 'Per-account settings' })).toHaveAttribute('href', '/settings/accounts')
  await expect(codex.getByRole('link', { name: 'Add account' })).toHaveAttribute('href', '/settings/accounts#add-account')
  // Billed by use: nothing to pace, and the words never call it "No limit" (the agent-count control already does).
  await row(page, 'cursor').locator('.nm').click()
  const cursor = detail(page, 'cursor')
  await expect(cursor).toContainText('billed by use and have no allowance window')
  await expect(cursor).toContainText('the 12 at once and the account still cap it')
  await expect(row(page, 'cursor').locator('.sub')).toContainText('no daily limit')
  await expect(row(page, 'cursor').locator('.cap-stl')).toHaveText('Nothing to pace')
})

test('AEON-1036: vendor-reported resets are shown, and nothing else is claimed about them', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { example: 'limit', dial: { selected: 'claude' } })
  await page.goto('/agents')
  await expect(detail(page, 'claude').locator('.cap-rs')).toHaveText('2 resets · first expires Sat 3 Oct 18:02')
  await expect(detail(page, 'claude').getByRole('button', { name: /reset/i })).toHaveCount(0)
})

test('AEON-1036: each row shows its usage lines and pace state in a fixed 88 px row', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { example: 'limit' })
  await page.goto('/agents')
  for (const [key, usage, pace] of [['codex', '6 running · ≤ 68 % used today', 'On pace'], ['claude', '2 running · ≤ 50 % used today', 'At today’s limit · Codex next'], ['cursor', '3 running · no daily limit', 'Nothing to pace']] as const) {
    await expect(row(page, key).locator('.sub')).toHaveText(usage)
    await expect(row(page, key).locator('.cap-stl')).toHaveText(pace)
    expect((await row(page, key).boundingBox())!.height).toBe(88)
  }
  await expect(row(page, 'claude').locator('.cap-stl')).toHaveClass(/st-at_limit/)
  await expect(row(page, 'codex').locator('.cap-stl')).toHaveClass(/st-on_pace/)
})

test.describe('settings written from the detail', () => {
  test('Pace: the choice and the points a day are written whole, with every harness and the total untouched', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const { backend, work } = await setup(page, { example: 'pace', dial: { selected: 'claude' } })
    await page.goto('/agents')
    const claude = detail(page, 'claude')
    await claude.getByRole('button', { name: 'Pace' }).click()
    await expect(claude.getByRole('button', { name: 'Pace' })).toHaveAttribute('aria-expanded', 'true')
    await expect.poll(() => work.preferences['ui.agents.dial']).toMatchObject({ folds: { 'claude:pace': true } })
    const points = claude.getByLabel('Percentage points a day')
    await expect(points).toHaveValue('10')
    await expect(claude.getByText('default ·')).toBeVisible()
    await expect(claude.getByRole('link', { name: 'edit' })).toHaveAttribute('href', '/settings/accounts')
    await points.fill('15'); await points.press('Enter')
    await expect.poll(() => backend.puts.length).toBe(1)
    expect(backend.puts[0]!.value).toEqual({
      total: 12, limits: { codex: 6, claude: 2, cursor: 'no_limit' },
      daily: {
        claude: { pace: { mode: 'pace', points_per_day: 15 }, boost_today: null, at_limit: 'ladder' },
        codex: { pace: { mode: 'pace', points_per_day: null }, boost_today: null, at_limit: 'ladder' },
        cursor: { pace: { mode: 'pace', points_per_day: null }, boost_today: null, at_limit: 'ladder' },
      },
    })
    expect(backend.puts[0]!.expected_updated_at).toBeNull()
    await expect(claude).toContainText('Stay on pace · 15 percentage points a day')
    await expect(claude.getByText('default is 10 ·')).toBeVisible()
    // The default is followed, not copied.
    await points.fill('10'); await points.press('Enter')
    await expect.poll(() => backend.puts.length).toBe(2)
    expect(backend.puts[1]!.value.daily!.claude!.pace).toEqual({ mode: 'pace', points_per_day: null })
    expect(backend.puts[1]!.expected_updated_at).not.toBeNull()
    await claude.getByRole('radio', { name: 'Use everything before the reset' }).check()
    await expect.poll(() => backend.puts.length).toBe(3)
    expect(backend.puts[2]!.value.daily!.claude!.pace).toEqual({ mode: 'everything', points_per_day: null })
    await expect(claude).toContainText('Use everything before the reset')
    await expect(points).toBeDisabled()
    await expect(row(page, 'claude').locator('.cap-stl')).toHaveText('Uses everything before the reset')
    await expect(row(page, 'claude').locator('.sub')).toContainText('no daily limit')
  })

  test('Pace: a wrong number is said in place and nothing is written', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const { backend } = await setup(page, { example: 'pace', dial: { selected: 'claude', folds: { 'claude:pace': true } } })
    await page.goto('/agents')
    const claude = detail(page, 'claude'), points = claude.getByLabel('Percentage points a day')
    for (const bad of ['0', '51', 'ten', '2.5', '']) {
      await points.fill(bad); await points.press('Enter')
      await expect(claude.getByText('Whole numbers from 1 to 50.')).toBeVisible()
      await expect(points).toHaveValue('10')
      await points.blur(); await points.focus()
      await expect(claude.getByText('Whole numbers from 1 to 50.')).toBeHidden()
      await points.blur()
    }
    await points.fill('20'); await points.press('Escape')
    await expect(points).toHaveValue('10')
    expect(backend.puts).toEqual([])
  })

  test('Boost today: one number for today, typed as used or left, restated both ways, ending at the person’s midnight', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const { backend } = await setup(page, { example: 'pace', dial: { selected: 'claude', folds: { 'claude:boost': true } } })
    await page.goto('/agents')
    const claude = detail(page, 'claude'), amount = claude.getByLabel('Allow up to, in percent')
    await expect(claude).toContainText('off')
    await expect(amount).toHaveValue('')
    await expect(amount).toHaveAttribute('placeholder', '50')
    await amount.fill('60'); await amount.press('Enter')
    await expect.poll(() => backend.puts.length).toBe(1)
    expect(backend.puts[0]!.value.daily!.claude!.boost_today).toEqual({ limit_used_pct: 60, entered_as: 'used', until: UNTIL })
    await expect(claude).toContainText('Up to 60 % used · 40 % left today. Ends at midnight.')
    // Left restates the same boost; typing left stores the used percentage.
    await claude.getByRole('radio', { name: 'left', exact: true }).click()
    await expect.poll(() => backend.puts.length).toBe(2)
    expect(backend.puts[1]!.value.daily!.claude!.boost_today).toEqual({ limit_used_pct: 60, entered_as: 'left', until: UNTIL })
    await expect(amount).toHaveValue('40')
    await amount.fill('30'); await amount.press('Enter')
    await expect.poll(() => backend.puts.length).toBe(3)
    expect(backend.puts[2]!.value.daily!.claude!.boost_today).toEqual({ limit_used_pct: 70, entered_as: 'left', until: UNTIL })
    await expect(claude).toContainText('Up to 70 % used · 30 % left today. Ends at midnight.')
    // A wrong number is said in place and writes nothing.
    await amount.fill('101'); await amount.press('Enter')
    await expect(claude.getByText('Whole percentages from 0 to 100.')).toBeVisible()
    expect(backend.puts).toHaveLength(3)
    // Empty clears it, like Clear.
    await amount.fill(''); await amount.press('Enter')
    await expect.poll(() => backend.puts.length).toBe(4)
    expect(backend.puts[3]!.value.daily!.claude!.boost_today).toBeNull()
    await expect(claude).toContainText('off')
    await amount.fill('80'); await amount.press('Enter')
    await expect.poll(() => backend.puts.length).toBe(5)
    await claude.getByRole('button', { name: 'Clear' }).click()
    await expect.poll(() => backend.puts.length).toBe(6)
    expect(backend.puts[5]!.value.daily!.claude!.boost_today).toBeNull()
  })

  test('Boost today is read-only without the right to manage accounts', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const { backend } = await setup(page, { example: 'pace', manage: false, dial: { selected: 'claude', folds: { 'claude:boost': true } } })
    await page.goto('/agents')
    const claude = detail(page, 'claude')
    await expect(claude.getByLabel('Allow up to, in percent')).toBeDisabled()
    await expect(claude.getByRole('radio', { name: 'used', exact: true })).toBeDisabled()
    await expect(claude).toContainText('Changing today’s limit needs the right to manage accounts.')
    expect(backend.puts).toEqual([])
  })

  test('Boost today is read-only for accounts the person may not change; the server refuses that write too', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const { backend } = await setup(page, { example: 'pace', readOnly: true, dial: { selected: 'claude', folds: { 'claude:boost': true, 'claude:pace': true } } })
    await page.goto('/agents')
    const claude = detail(page, 'claude')
    await expect(claude.getByLabel('Allow up to, in percent')).toBeDisabled()
    await expect(claude).toContainText('Only the owner of each account can change today’s limit.')
    // Pace and the choice at the limit are the person's own plan and stay editable.
    await expect(claude.getByLabel('Percentage points a day')).toBeEnabled()
    expect(backend.puts).toEqual([])
  })

  test('At the limit: follow the model ladder or wait, and nothing else', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const { backend } = await setup(page, { example: 'limit', dial: { selected: 'claude' } })
    await page.goto('/agents')
    const claude = detail(page, 'claude'), choice = claude.getByRole('radiogroup', { name: 'At the limit' })
    await expect(choice.getByRole('radio')).toHaveCount(2)
    await expect(choice.getByRole('radio', { name: 'Follow the model ladder' })).toHaveAttribute('aria-checked', 'true')
    await expect(claude).toContainText('Codex next on the model ladder.')
    await expect(claude.getByRole('link', { name: 'Models' })).toHaveAttribute('href', '/settings/models')
    await choice.getByRole('radio', { name: 'Wait' }).click()
    await expect.poll(() => backend.puts.length).toBe(1)
    expect(backend.puts[0]!.value.daily!.claude!.at_limit).toBe('wait')
    await expect(claude).toContainText('New work waits until midnight. Running agents finish.')
    await expect(row(page, 'claude').locator('.cap-stl')).toHaveText('At today’s limit · new work waits')
    await choice.getByRole('radio', { name: 'Follow the model ladder' }).click()
    await expect.poll(() => backend.puts.length).toBe(2)
    expect(backend.puts[1]!.value.daily!.claude!.at_limit).toBe('ladder')
  })

  test('At the limit works from the keyboard: the arrow keys pick the other option and take focus with them', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const { backend } = await setup(page, { example: 'limit', dial: { selected: 'claude' } })
    await page.goto('/agents')
    const choice = detail(page, 'claude').getByRole('radiogroup', { name: 'At the limit' })
    const ladder = choice.getByRole('radio', { name: 'Follow the model ladder' }), wait = choice.getByRole('radio', { name: 'Wait' })
    // Only the chosen option is in the tab order, so the arrows are the way to the other one.
    await expect(ladder).toHaveAttribute('tabindex', '0'); await expect(wait).toHaveAttribute('tabindex', '-1')
    await ladder.focus()
    const picks: { key: string; to: 'ladder' | 'wait' }[] = [{ key: 'ArrowRight', to: 'wait' }, { key: 'ArrowLeft', to: 'ladder' }, { key: 'ArrowDown', to: 'wait' }, { key: 'ArrowUp', to: 'ladder' }, { key: 'End', to: 'wait' }, { key: 'Home', to: 'ladder' }]
    for (const [i, { key, to }] of picks.entries()) {
      await page.keyboard.press(key)
      await expect.poll(() => backend.puts.length, key).toBe(i + 1)
      expect(backend.puts[i]!.value.daily!.claude!.at_limit, key).toBe(to)
      const [now, other] = to === 'wait' ? [wait, ladder] : [ladder, wait]
      await expect(now, key).toHaveAttribute('aria-checked', 'true'); await expect(now, key).toBeFocused(); await expect(now, key).toHaveAttribute('tabindex', '0')
      await expect(other, key).toHaveAttribute('aria-checked', 'false'); await expect(other, key).toHaveAttribute('tabindex', '-1')
    }
    // The key on the option that is already chosen writes nothing.
    await page.keyboard.press('Home')
    await expect(ladder).toBeFocused()
    expect(backend.puts).toHaveLength(picks.length)
  })

  test('a failed write keeps the confirmed settings on screen and says so', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const { backend } = await setup(page, { example: 'pace', dial: { selected: 'claude', folds: { 'claude:pace': true } } })
    backend.failPuts = 1
    await page.goto('/agents')
    const claude = detail(page, 'claude')
    await claude.getByRole('radio', { name: 'Use everything before the reset' }).click()
    await expect(dial(page).locator('.f-live')).toContainText('Couldn’t save the daily limit. Please try again.')
    await expect(claude).toContainText('Stay on pace · 10 percentage points a day')
    await expect(claude.getByRole('radio', { name: 'Stay on pace' })).toBeChecked()
    expect(backend.puts).toHaveLength(1)
  })

  test('a changed plan revision is re-read and the edit is not forced over it', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    const { backend } = await setup(page, { example: 'pace', dial: { selected: 'claude' } })
    backend.conflicts = 1
    await page.goto('/agents')
    await detail(page, 'claude').getByRole('radio', { name: 'Wait' }).click()
    await expect(dial(page).locator('.f-live')).toContainText('changed elsewhere')
    await expect(detail(page, 'claude').getByRole('radio', { name: 'Follow the model ladder' })).toHaveAttribute('aria-checked', 'true')
    expect(backend.puts).toHaveLength(1)
  })
})

test('AEON-1036: honest states: no reading is not on pace, and nothing is flagged', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { example: 'unknown', folded: true })
  await page.goto('/agents')
  await expect(chip(page, 'claude')).toBeVisible()
  await expect(dial(page).locator('.cap-flag')).toHaveCount(0)
  await dial(page).locator('.fs-tog').click()
  await expect(row(page, 'claude').locator('.cap-stl')).toHaveText('Usage not measured right now')
  await expect(row(page, 'claude').locator('.sub')).toHaveText('2 running')
  await row(page, 'claude').locator('.nm').click()
  const claude = detail(page, 'claude')
  await expect(claude).toContainText('This week: not measured right now')
  await expect(claude).toContainText('Today: not measured yet')
  await expect(claude.locator('.cap-st')).toHaveText('Usage not measured right now')
  await expect(claude.locator('.cap-st')).toHaveClass(/st-unknown/)
})

test('AEON-1036: a harness with no account says so and links to add one', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { example: 'pace', without: ['claude'], dial: { selected: 'claude' } })
  await page.goto('/agents')
  await expect(detail(page, 'claude').getByRole('heading')).toHaveText('Claude · no account yet')
  await expect(detail(page, 'claude').getByRole('link', { name: 'Add account' })).toHaveAttribute('href', '/settings/accounts#add-account')
  await expect(row(page, 'claude').locator('.cap-stl')).toHaveText('No account to read')
  await expect(dial(page).locator('.cap-flag')).toHaveCount(0)
})

test('AEON-1036: an older plan without daily fields still draws the dial', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page)
  await page.route('**/api/agents/plan', route => route.fulfill({ json: { total: 8, limits: { codex: 4 }, principal_id: me.id, running: { codex: 2 }, running_total: 2, source: 'plan', updated_at: null } }))
  await page.goto('/agents')
  await expect(dial(page).locator('.f-num')).toHaveText('8')
  await expect(row(page, 'codex').locator('.cap-stl')).toHaveText('No account to read')
  await expect(dial(page).locator('.f-live')).toContainText('2 running')
})

test('AEON-1036: the old header Boost today is gone and nothing writes to its endpoint', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { calls } = await setup(page, { example: 'pace' })
  await page.goto('/agents')
  await expect(dial(page).locator('.f-dial')).toBeVisible()
  await expect(page.locator('[data-boost-today]')).toHaveCount(0)
  await expect(page.locator('.working .boost-today')).toHaveCount(0)
  await expect(detail(page, 'codex').getByRole('button', { name: 'Boost today' })).toBeVisible()
  expect(calls.filter(c => /agent-accounts\/boost/.test(c.path))).toEqual([])
})

for (const width of [1440, 1024, 400]) for (const theme of ['light', 'dark'] as const) {
  test(`${width} ${theme}: folding, picking a harness, folds and the info area keep every control put`, async ({ page }, info) => {
    test.setTimeout(120_000)
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const errors = watchErrors(page)
    const { calls, backend } = await setup(page, { example: 'over', dial: { selected: 'claude' } })
    await page.goto('/agents')
    const card = dial(page), fold = card.locator('.fs-tog'), toggle = card.locator('.f-live')
    await expect(row(page, 'claude')).toBeVisible()
    mkdirSync(shots, { recursive: true })
    // The capture grows the viewport to the card; an element screenshot otherwise clips a long phone dial.
    const shoot = async (name: string) => {
      const bounds = (await card.boundingBox())!
      await page.setViewportSize({ width, height: Math.ceil(Math.max(1000, bounds.y + bounds.height + 100)) })
      await card.screenshot({ path: `${shots}/${width}-${theme}-${name}.png`, animations: 'disabled' })
      await card.screenshot({ path: info.outputPath(`${width}-${theme}-${name}.png`), animations: 'disabled' })
      await page.setViewportSize({ width, height: 1000 })
    }
    await shoot('open-over-pace')

    // 1 · Fold and unfold, and the info area, keep the head still; the rows are below the area and move with it by design.
    const head: Record<string, Locator> = { more: more(page), fewer: fewer(page), fold, dial: card.locator('.f-dial'), toggle, chevron: toggle.locator('> svg') }
    await expectStableControls({
      controls: head,
      scrollAreas: { card },
      interactions: [
        { name: 'fold', run: async () => { await fold.click(); await expect(fold).toHaveAttribute('aria-expanded', 'false') } },
        { name: 'unfold', run: async () => { await fold.click(); await expect(fold).toHaveAttribute('aria-expanded', 'true') } },
        { name: 'open the info area', run: async () => { await toggle.click(); await expect(card.locator('.wh')).toBeVisible() } },
        { name: 'close the info area', run: async () => { await toggle.click(); await expect(card.locator('.wh')).toBeHidden() } },
        { name: 'fold with the info area open', run: async () => { await toggle.click(); await fold.click(); await expect(fold).toHaveAttribute('aria-expanded', 'false') } },
        { name: 'unfold with the info area open', run: async () => { await fold.click(); await expect(fold).toHaveAttribute('aria-expanded', 'true') } },
        { name: 'close the info area again', run: async () => { await toggle.click(); await expect(card.locator('.wh')).toBeHidden() } },
      ],
    })
    await toggle.click()
    await shoot('open-info-over-pace')
    await toggle.click()

    // 2 · Picking a harness moves nothing: not the rows, not their selectors and steppers, not the frame of the detail.
    const keys = ['codex', 'claude', 'cursor'] as const
    const controls: Record<string, Locator> = { ...head, frame: card.locator('#dial-detail') }
    for (const key of keys) Object.assign(controls, { [`${key} row`]: row(page, key), [`${key} choice`]: row(page, key).getByRole('radiogroup'), [`${key} pick`]: row(page, key).locator('.f-sel'), [`${key} mark`]: row(page, key).locator('.f-mode'), [`${key} step`]: row(page, key).locator('.f-pm') })
    const pick = (key: string) => async () => { await row(page, key).locator('.nm').click(); await expect(row(page, key)).toHaveClass(/sel/) }
    const mode = (name: string) => async () => { const radio = row(page, 'codex').getByRole('radio', { name, exact: true }); await radio.click(); await expect(radio).toHaveAttribute('aria-checked', 'true') }
    await expectStableControls({
      controls, scrollAreas: { card, rows: card.locator('.rows') },
      interactions: [
        { name: 'pick codex', run: pick('codex') },
        { name: 'pick cursor (no daily limit)', run: pick('cursor') },
        { name: 'pick claude', run: pick('claude') },
        { name: 'pick codex by keyboard', run: async () => { await row(page, 'codex').locator('.f-sel').focus(); await page.keyboard.press('Enter'); await expect(row(page, 'codex')).toHaveClass(/sel/) } },
        { name: 'switch codex to Off', run: mode('Off') },
        { name: 'switch codex back to At most', run: mode('At most') },
        { name: 'pick claude again', run: pick('claude') },
      ],
    })

    // 3 · Folds open downward only: what is above them, the head, the rows and the first fold header, stays.
    const claude = detail(page, 'claude')
    const pace = claude.getByRole('button', { name: 'Pace' }), boost = claude.getByRole('button', { name: 'Boost today' })
    await expectStableControls({
      controls: { ...head, claude: row(page, 'claude'), codex: row(page, 'codex'), cursor: row(page, 'cursor'), headline: claude.getByRole('heading'), bar: claude.getByRole('img'), pace },
      scrollAreas: { card, rows: card.locator('.rows') },
      interactions: [
        { name: 'open Pace', run: async () => { await pace.click(); await expect(pace).toHaveAttribute('aria-expanded', 'true'); await expect(claude.getByLabel('Percentage points a day')).toBeVisible() } },
        { name: 'type a wrong number', run: async () => { const points = claude.getByLabel('Percentage points a day'); await points.fill('99'); await points.press('Enter'); await expect(claude.getByText('Whole numbers from 1 to 50.')).toBeVisible() } },
        { name: 'open Boost today', run: async () => { await boost.click(); await expect(boost).toHaveAttribute('aria-expanded', 'true') } },
        { name: 'type a wrong boost', run: async () => { const amount = claude.getByLabel('Allow up to, in percent'); await amount.fill('101'); await amount.press('Enter'); await expect(claude.getByText('Whole percentages from 0 to 100.')).toBeVisible() } },
        { name: 'close Pace', run: async () => { await pace.click(); await expect(pace).toHaveAttribute('aria-expanded', 'false') } },
        { name: 'close Boost today', run: async () => { await boost.click(); await expect(boost).toHaveAttribute('aria-expanded', 'false') } },
        { name: 'open both', run: async () => { await pace.click(); await boost.click(); await expect(boost).toHaveAttribute('aria-expanded', 'true') } },
      ],
    })
    await shoot('claude-folds-over-pace')
    await row(page, 'cursor').locator('.nm').click()
    await shoot('cursor-no-daily-limit')

    // 4 · Folded: one row on a wide card, icons with counts on a phone.
    await fold.click()
    await expect(fold).toHaveAttribute('aria-expanded', 'false')
    await shoot('folded-over-pace')
    if (width < 600) {
      expect((await chip(page, 'codex').locator('.f-ph').boundingBox())!.height).toBeGreaterThanOrEqual(44)
      await expect(chip(page, 'claude').locator('.f-ph.st-over_pace')).toBeVisible()
      await expect(chip(page, 'codex').locator('.pm.dec')).toBeHidden()
    } else {
      await expectStableControls({
        controls: { ...head, codex: chip(page, 'codex'), claude: chip(page, 'claude'), cursor: chip(page, 'cursor'), flag: chip(page, 'claude').locator('.cap-flag'), steps: chip(page, 'codex').locator('.f-pm') },
        scrollAreas: { card },
        interactions: [
          { name: 'step Codex', run: async () => { await chip(page, 'codex').locator('.pm.inc').click(); await expect(chip(page, 'codex').locator('.f-n')).toHaveText('7') } },
          { name: 'step Codex back', run: async () => { await chip(page, 'codex').locator('.pm.dec').click(); await expect(chip(page, 'codex').locator('.f-n')).toHaveText('6') } },
          { name: 'hover the Claude chip', run: async () => { await chip(page, 'claude').hover(); await expect(chip(page, 'claude').locator('.chip-tip')).toBeVisible() } },
          { name: 'leave it', run: async () => { await page.mouse.move(1, 1); await expect(chip(page, 'claude').locator('.chip-tip')).toBeHidden() } },
        ],
      })
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
    expect(writes(calls)).toEqual([])
    // Picking and folding wrote no daily setting; only the plan's own limits changed above.
    expect(backend.puts.filter(p => p.value.daily)).toEqual([])
    expect(errors).toEqual([])
  })
}

test('AEON-1036: the folded dial keeps its place when a flag appears', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.clock.install({ time: NOW })
  const { backend } = await setup(page, { example: 'pace', folded: true })
  await page.goto('/agents')
  const card = dial(page)
  await expect(chip(page, 'claude')).toBeVisible()
  await expectStableControls({
    controls: { more: more(page), fewer: fewer(page), fold: card.locator('.fs-tog'), dial: card.locator('.f-dial'), codex: chip(page, 'codex'), claude: chip(page, 'claude'), cursor: chip(page, 'cursor'), live: card.locator('.f-live > svg') },
    scrollAreas: { card },
    interactions: [{ name: 'Claude goes over pace while the dial is folded', run: async () => {
      backend.example = 'over'
      backend.settings = { ...backend.settings, claude: { ...backend.settings.claude!, boost_today: { limit_used_pct: 60, entered_as: 'used', until: UNTIL } } }
      await page.clock.fastForward(16_000)
      await expect(chip(page, 'claude').locator('.cap-flag')).toHaveClass(/st-over_pace/)
    } }, { name: 'then at its limit', run: async () => {
      backend.example = 'limit'; backend.settings = { ...backend.settings, claude: { ...backend.settings.claude!, boost_today: null } }
      await page.clock.fastForward(16_000)
      await expect(chip(page, 'claude').locator('.cap-flag')).toHaveClass(/st-at_limit/)
    } }],
  })
})

test('AEON-1036: the dial and its detail pass axe, open with both folds and folded, in both themes', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page, { example: 'over', dial: { selected: 'claude', info_open: true, folds: { 'claude:pace': true, 'claude:boost': true } } })
  await page.goto('/agents')
  await expect(detail(page, 'claude')).toBeVisible()
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    expect((await new AxeBuilder({ page }).include('.working').analyze()).violations.map(v => v.id), `open ${theme}`).toEqual([])
  }
  await dial(page).locator('.fs-tog').click()
  await expect(dial(page).locator('.f-chip')).toHaveCount(3)
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    expect((await new AxeBuilder({ page }).include('.working').analyze()).violations.map(v => v.id), `folded ${theme}`).toEqual([])
  }
})

test.describe('phone', () => {
  test.use({ viewport: { width: 400, height: 1000 }, hasTouch: true, isMobile: true })
  test('AEON-1036: icons with counts open the dial at their harness; every target is 44 px and the detail sits under the list', async ({ page }) => {
    const { work } = await setup(page, { example: 'over', folded: true })
    await page.goto('/agents')
    const card = dial(page)
    await expect(card.locator('.f-ph')).toHaveCount(3)
    // Two lines in all: the sentence, then the harnesses with the running state on the right.
    const [sentence, chips, live] = await Promise.all([card.locator('.f-dial').boundingBox(), card.locator('.f-chips').boundingBox(), card.locator('.f-live').boundingBox()])
    expect(chips!.y).toBeGreaterThan(sentence!.y + sentence!.height - 1)
    expect(live!.x).toBeGreaterThan(chips!.x + chips!.width - 1)
    await expect(chip(page, 'claude').locator('.f-ph')).toHaveClass(/st-over_pace/)
    await expect(chip(page, 'claude').locator('.f-ph')).toHaveText('2')
    await expect(chip(page, 'cursor').locator('.f-ph')).toHaveText('∞')
    await chip(page, 'codex').locator('.f-ph').tap()
    await expect(card.locator('.fs-tog')).toHaveAttribute('aria-expanded', 'true')
    await expect(row(page, 'codex')).toHaveClass(/sel/)
    await expect.poll(() => work.preferences['ui.agents.dial']).toMatchObject({ selected: 'codex' })
    const targets = async () => {
      const sizes = await card.locator('button:visible').evaluateAll(elements => elements.map(el => { const box = el.getBoundingClientRect(); return { name: el.getAttribute('aria-label') ?? el.textContent?.trim() ?? '', width: box.width, height: box.height } }))
      expect(sizes.length).toBeGreaterThan(0)
      for (const size of sizes) { expect(size.width, size.name).toBeGreaterThanOrEqual(44); expect(size.height, size.name).toBeGreaterThanOrEqual(44) }
    }
    await targets()
    await row(page, 'claude').locator('.nm').tap()
    await detail(page, 'claude').getByRole('button', { name: 'Pace' }).tap()
    await detail(page, 'claude').getByRole('button', { name: 'Boost today' }).tap()
    await targets()
    // The detail sits under the list on a phone; the rows keep one height and nothing runs off the side.
    expect((await detail(page, 'claude').boundingBox())!.y).toBeGreaterThan((await row(page, 'cursor').boundingBox())!.y)
    for (const key of ['codex', 'claude', 'cursor']) expect((await row(page, key).boundingBox())!.height).toBe(200)
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
  })
})
