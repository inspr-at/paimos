// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import AxeBuilder from '@axe-core/playwright'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { capacityWorld, defaultSchedule, NOW } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import type { PlanSnapshot } from '../src/lib/agentsWorking'
import { SETTLE_MS } from '../src/lib/useAgentPlan'
const shots = process.env.AEON_647_SHOTS ?? 'test-results/aeon-647'
const states = {
  wind: { total: 5, running: { codex: 12, claude: 1, cursor: 3 }, limits: { codex: 4, claude: 2, cursor: 'off' } },
  room: { total: 8, running: { codex: 2, claude: 1, cursor: 0 }, limits: { codex: 4, claude: 'no_limit', cursor: 'no_limit' } },
  zero: { total: 0, running: { codex: 1, claude: 1, cursor: 0 }, limits: { codex: 4, claude: 'no_limit', cursor: 'no_limit' } },
  off: { total: 8, running: { codex: 3, claude: 1, cursor: 0 }, limits: { codex: 'no_limit', claude: 'no_limit', cursor: 'off' } },
  full: { total: 8, running: { codex: 2, claude: 1, cursor: 0 }, limits: { codex: 'no_limit', claude: 'no_limit', cursor: 'no_limit' } },
} satisfies Record<string, Pick<PlanSnapshot, 'total' | 'running' | 'limits'>>
async function setup(page: Page, state: keyof typeof states = 'wind', folded = false) {
  await page.clock.setSystemTime(NOW)
  const work = fixtures(), initial = states[state]
  work.preferences['agents.working'] = { total: initial.total, limits: initial.limits }
  work.preferences['ui.agents.sections'] = { dial: !folded }
  await mockWork(page, work, { admin: true })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  const capacity = capacityWorld()
  data.accounts = (capacity.accounts as unknown as typeof data.accounts).filter(a => ['codex', 'claude', 'cursor'].includes(a.harness)).map(a => ({ ...a, registered_by_principal_id: me.id }))
  const calls = await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('account.read', 'account.manage')
    return route.fulfill({ json: answer })
  })
  await page.route('**/api/agents/plan', route => route.fulfill({ json: { ...work.preferences['agents.working'], principal_id: me.id, running: initial.running, running_total: Object.values(initial.running).reduce((n, x) => n + x, 0), source: 'plan', updated_at: null } }))
  await page.route('**/api/agent-accounts/capacity', route => {
    const answer = capacity.handle('/api/agent-accounts/capacity', 'GET', null)!.json as { account_id: string }[]
    const available = { wind: { codex: 3, claude: 2, cursor: 2 }, room: { codex: 4, claude: 3, cursor: 2 }, zero: { codex: 4, claude: 2, cursor: 2 }, off: { codex: 2, claude: 2, cursor: 2 }, full: { codex: 0, claude: 0, cursor: 0 } }[state]
    const seen = new Set<string>()
    return route.fulfill({ json: answer.map(a => {
      const harness = data.accounts.find(account => account.id === a.account_id)?.harness
      const slots = harness && !seen.has(harness) ? available[harness as keyof typeof available] ?? 0 : 0
      if (harness) seen.add(harness)
      return { ...a, routing: state === 'full' ? { rank: 0, available_slots: 0, wait: { code: 'capacity', run_now_allowed: false } } : { rank: 1, available_slots: slots } }
    }) })
  })
  const queue = state === 'room' ? [] : Array.from({ length: state === 'off' ? 2 : 3 }, (_, i) => ({ node_id: `waiting-${i}`, key: `AEON-${i}`, title: 'Waiting work', state: 'open', priority: 'normal', estimate_hours: 1, queued: { model_profile_id: null, waiting: false, wait_reason: '' } }))
  await page.route('**/api/queue', route => route.fulfill({ json: { items: queue, manual_order: false, capacity: {} } }))
  return { work, calls, data }
}
const dial = (page: Page) => page.getByRole('region', { name: 'Agents at once' })
const totalMore = (page: Page) => dial(page).getByRole('button', { name: 'One agent more at once' })
const totalFewer = (page: Page) => dial(page).getByRole('button', { name: 'One agent fewer at once' })

test('AEON-720: idle dial reports real account room and explains unknown readings without inventing waiting agents', async ({ page }) => {
  const { data, calls } = await setup(page, 'room')
  let reading: 'ready' | 'missing' | 'blocked' = 'ready'
  let capacityReads = 0
  await page.route('**/api/agents/plan', route => route.fulfill({ json: { total: 5, limits: {}, principal_id: me.id, running: {}, running_total: 0, source: 'plan', updated_at: null } }))
  await page.route('**/api/agent-accounts/capacity', route => {
    capacityReads++
    return route.fulfill({ json: data.accounts.map(a => ({
      account_id: a.id, schedule: defaultSchedule(),
      windows: [], routing: reading === 'ready' ? { rank: 1, available_slots: 2 }
        : { rank: 0, available_slots: 0, wait: { code: reading === 'missing' ? 'reading' : 'schedule', run_now_allowed: false } },
    })) })
  })
  await page.clock.install({ time: NOW })
  await page.goto('/agents')
  const card = dial(page), detail = card.locator('.f-info')
  const capture = async () => {
    mkdirSync(shots, { recursive: true })
    for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
      await page.setViewportSize({ width, height: 1800 })
      await page.emulateMedia({ colorScheme: theme })
      expect(await card.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
      await card.screenshot({ path: `${shots}/${width}-${theme}-idle-${reading}.png`, animations: 'disabled' })
    }
    await page.setViewportSize({ width: 1440, height: 1800 })
  }
  await expect(card.locator('.f-now')).toContainText('0 are running; 5 more can start as work comes in.')
  await expect(detail).toContainText(`Room for ${data.accounts.length * 2} more right now.`)
  await expect(detail).toContainText('quota not measured yet')
  await expect(card.locator('.f-wait')).toHaveText('No work queued.')
  await expect(card.locator('.f-now')).not.toContainText('wait')
  await expect(detail).not.toContainText('Full right now')
  await capture()
  await expectStableControls({
    controls: { more: totalMore(page), fewer: totalFewer(page), fold: card.locator('.fs-tog'), choices: card.locator('[data-key="codex"] .seg'), row: card.locator('[data-key="codex"]') },
    scrollAreas: { card },
    interactions: [
      { name: 'reading disappears during a poll', run: async () => {
        const before = capacityReads
        reading = 'missing'
        await page.clock.fastForward(30_000)
        await expect.poll(() => capacityReads).toBeGreaterThan(before)
        await expect(detail).toContainText('Account room is not measured yet; see the reasons below.')
        await expect(detail).toContainText('a current reading is missing')
        await expect(card.locator('.f-live')).toHaveText('0 running · account room not measured yet')
        await capture()
      } },
      { name: 'scheduled hours block new starts', run: async () => {
        reading = 'blocked'
        await page.clock.fastForward(30_000)
        await expect(detail).toContainText('outside scheduled hours')
        await expect(detail).toContainText('No account starts are available right now.')
        await expect(detail).not.toContainText('Full right now')
        await expect(card.locator('.f-now')).not.toContainText('wait')
      } },
    ],
  })
  await capture()
  expect(calls.filter(c => c.method !== 'GET' && /harness-sessions|\/controls|\/stop|\/interrupt/.test(c.path))).toEqual([])
})

test('AEON-720: accounts owned by another person never become dial room or a false full', async ({ page }) => {
  const { data } = await setup(page, 'room')
  for (const a of data.accounts) Object.assign(a, { owner_person_id: 'other-person', registered_by_principal_id: 'other-agent' })
  await page.route('**/api/agents/plan', route => route.fulfill({ json: { total: 5, limits: {}, principal_id: me.id, running: {}, running_total: 0, source: 'plan', updated_at: null } }))
  await page.goto('/agents')
  const card = dial(page)
  await expect(card.locator('.f-info')).toContainText('No linked accounts.')
  await expect(card.locator('.f-info')).toContainText('Codex no linked accounts')
  await expect(card.locator('.f-live')).toHaveText('0 running · no account starts available')
  await expect(card.locator('.f-now')).not.toContainText('wait')
  await expect(card.locator('.f-info')).not.toContainText('Full right now')
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  const state = 'wind' as const
  test(`${width} ${theme} ${state}: stepping, modes and folding keep every control put`, async ({ page }) => {
    // Measure each changed control through steps, drafts, modes and folding.
    test.setTimeout(90_000)
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const errors = watchErrors(page), { work, calls } = await setup(page, state)
    await page.goto('/agents')
    const card = dial(page), codex = card.locator('[data-key="codex"]'), fold = card.locator('.fs-tog')
    const more = totalMore(page), fewer = totalFewer(page)
    await expect(codex).toBeVisible()
    await expect(card.locator('.f-num')).toHaveText(String(states[state].total))
    await expect(card.locator('.f-bot')).toHaveClass(/busy/)
    expect(await card.locator('.f-dial').evaluate(el => el.firstElementChild?.classList.contains('f-bot'))).toBe(true)
    expect(await card.locator('.f-bot').evaluate(el => el.getAnimations({ subtree: true }).length)).toBe(0)
    const expected = { wind: '16 running · winding down to 5', room: '3 running · room for 5 more', zero: '2 running · nothing new starts', off: '4 running · room for 4 more', full: '3 running · accounts full' }
    await expect(card.locator('.f-live')).toContainText(expected[state])
    if (shots) {
      mkdirSync(shots, { recursive: true })
      const bounds = (await card.boundingBox())!
      // An element screenshot inside the app's scroller otherwise clips long
      // phone details. Expand only the capture height, then restore the guard's viewport.
      await page.setViewportSize({ width, height: Math.ceil(Math.max(1000, bounds.y + bounds.height + 100)) })
      await card.screenshot({ path: `${shots}/${width}-${theme}-${state}-unfolded.png`, animations: 'disabled' })
      await page.setViewportSize({ width, height: 1000 })
      await fold.click()
      await card.screenshot({ path: `${shots}/${width}-${theme}-${state}-folded.png`, animations: 'disabled' })
      await fold.click()
    }
    await expectStableControls({
      controls: { more, fewer, fold, dial: card.locator('.f-dial'), total: card.locator('.f-num'), row: codex, selectors: codex.getByRole('radiogroup'), none: codex.getByRole('radio', { name: 'No limit', exact: true }), max: codex.getByRole('radio', { name: 'At most', exact: true }), off: codex.getByRole('radio', { name: 'Off', exact: true }), stepper: codex.locator('.f-pm'), minus: codex.locator('.pm').first(), plus: codex.locator('.pm').last(), value: codex.locator('.f-n'), mark: codex.locator('.f-mode') },
      scrollAreas: { card, rows: card.locator('.rows') },
      interactions: [
        { name: 'cross total digit boundary', run: async () => { for (let n = states[state].total; n < 10; n++) await more.click(); await expect(card.locator('.f-num')).toHaveText('10') } },
        { name: 'lower ceiling', run: async () => { await fewer.click(); await expect(card.locator('.f-num')).toHaveText('9') } },
        ...['Off', 'No limit', 'At most'].map(name => ({ name: `mode ${name}`, run: async () => { await codex.getByRole('radio', { name, exact: true }).click(); await expect(codex.getByRole('radio', { name, exact: true })).toHaveAttribute('aria-checked', 'true') } })),
        { name: 'type total without moving its slot', run: async () => { await card.locator('.f-num .value').click(); await expect(card.locator('.f-num input')).toBeFocused() } },
        { name: 'apply total on Enter', run: async () => { await card.locator('.f-num input').fill('10'); await card.locator('.f-num input').press('Enter'); await expect(card.locator('.f-num')).toHaveText('10') } },
        { name: 'type a harness limit', run: async () => { await codex.locator('.value').click(); await expect(codex.locator('input')).toBeFocused() } },
        { name: 'apply clamped ceiling', run: async () => { await codex.locator('input').fill('99'); await codex.locator('input').press('Enter'); await expect(codex.locator('.f-n')).toHaveText('30') } },
        { name: '30 plus gives no own limit', run: async () => { await codex.locator('.pm').last().click(); await expect(codex.locator('.value svg')).toBeVisible(); await expect(codex.locator('.pm').last()).toBeDisabled(); await expect(codex.locator('.value')).toHaveAttribute('data-tip', /up to 10 now/) } },
        { name: 'no own limit minus uses effective ceiling', run: async () => { await codex.locator('.pm').first().click(); await expect(codex.locator('.f-n')).toHaveText('9') } },
        { name: 'Home sets off', run: async () => { await codex.locator('.pm').last().focus(); await codex.locator('.pm').last().press('Home'); await expect(codex.locator('.f-n')).toHaveText('off') } },
        { name: 'End sets no own limit', run: async () => { await codex.locator('.pm').last().press('End'); await expect(codex.locator('.value svg')).toBeVisible() } },
        ...[1, 2, 3].map(n => ({ name: `expanded mark cycle ${n}`, run: () => codex.locator('.f-mode').click() })),
        { name: 'return to at most', run: () => codex.getByRole('radio', { name: 'At most', exact: true }).click() },
      ],
    })
    await expectStableControls({
      controls: { more, fewer, fold, dial: card.locator('.f-dial') }, scrollAreas: { card },
      interactions: [
        { name: 'fold', run: async () => { await fold.click(); await expect(fold).toHaveAttribute('aria-expanded', 'false') } },
        { name: 'unfold', run: async () => { await fold.click(); await expect(fold).toHaveAttribute('aria-expanded', 'true') } },
      ],
    })
    await fold.click()
    const chip = card.locator('[data-harness="codex"]'), cycle = chip.locator('.f-mode'), dec = chip.locator('.pm').first(), inc = chip.locator('.pm').last()
    await expectStableControls({
      controls: { more, fewer, fold, chips: card.locator('.f-chips'), chip, cycle, dec, inc, claude: card.locator('[data-harness="claude"]'), cursor: card.locator('[data-harness="cursor"]'), value: chip.locator('.f-n') }, scrollAreas: { card, chips: card.locator('.f-chips') },
      interactions: [
        { name: 'compact plus', run: () => inc.click() }, { name: 'compact minus', run: () => dec.click() },
        ...[1, 2, 3].map(n => ({ name: `cycle harness ${n}`, run: () => cycle.click() })),
        { name: 'folded draft starts in its value slot', run: () => chip.locator('.value').click() },
        { name: 'typed zero gives off on blur', run: async () => { await chip.locator('input').fill('0'); await more.focus(); await expect(chip.locator('.f-n')).toHaveText('off') } },
        { name: 'off plus gives one', run: async () => { await inc.click(); await expect(chip.locator('.f-n')).toHaveText('1') } },
        { name: 'one minus gives off', run: async () => { await dec.click(); await expect(chip.locator('.f-n')).toHaveText('off') } },
        { name: 'empty value starts in its slot', run: () => chip.locator('.value').click() },
        { name: 'empty value gives no own limit', run: async () => { await chip.locator('input').fill(''); await chip.locator('input').press('Enter'); await expect(chip.locator('.value svg')).toBeVisible() } },
        { name: 'cancel a draft', run: async () => { await chip.locator('.value').click(); await chip.locator('input').fill('6'); await chip.locator('input').press('Escape'); await expect(chip.locator('.value svg')).toBeVisible(); await expect(chip.locator('.value')).toBeFocused() } },
      ],
    })
    expect((await inc.boundingBox())!.width).toBe(22)
    await expect.poll(() => work.preferences['ui.agents.sections']).toMatchObject({ dial: false })
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
    expect(calls.filter(c => c.method !== 'GET' && /harness-sessions|\/controls|\/stop|\/interrupt/.test(c.path))).toEqual([])
    expect(errors).toEqual([])
  })
}

test('fold memory survives reload; mode and step keys preserve browser shortcuts', async ({ page }) => {
  const { work } = await setup(page, 'wind', true)
  await page.goto('/agents')
  const card = dial(page), fold = card.locator('.fs-tog'), more = totalMore(page)
  await expect(fold).toHaveAttribute('aria-expanded', 'false')
  await more.focus(); await more.press('ArrowUp')
  await expect(card.locator('.f-num')).toHaveText('6')
  await more.press('Control+ArrowUp'); await expect(card.locator('.f-num')).toHaveText('6')
  await more.press('Meta+ArrowUp'); await expect(card.locator('.f-num')).toHaveText('6')
  await expect.poll(() => work.preferences['agents.working']).toMatchObject({ total: 6 })
  await page.reload(); await expect(fold).toHaveAttribute('aria-expanded', 'false')
  await expect(card.locator('.f-num')).toHaveText('6')
  await fold.click()
  const row = card.locator('[data-key="codex"]'), max = row.getByRole('radio', { name: 'At most', exact: true })
  await max.focus(); await max.press('ArrowRight')
  await expect(row.getByRole('radio', { name: 'Off', exact: true })).toBeFocused()
  await expect(row.getByRole('radio', { name: 'Off', exact: true })).toHaveAttribute('aria-checked', 'true')
  const axe = await new AxeBuilder({ page }).include('.working').analyze()
  expect(axe.violations.map(v => v.id)).toEqual([])
})

test('a failed ceiling write restores the confirmed value and keeps the live feedback in place', async ({ page }) => {
  await setup(page)
  await page.route('**/api/preferences/agents.working', route => route.request().method() === 'PUT' ? route.fulfill({ status: 403, json: { error: 'forbidden' } }) : route.fallback())
  await page.goto('/agents')
  const card = dial(page), more = totalMore(page)
  await expect(card.locator('.f-num')).toHaveText('5')
  await expectStableControls({ controls: { more, fewer: totalFewer(page), fold: card.locator('.fs-tog'), live: card.locator('.f-live') }, interactions: [{ name: 'write failure', run: async () => { await more.click(); await expect(card.locator('.f-live')).toContainText('Couldn’t save'); await expect(card.locator('.f-num')).toHaveText('5') } }] })
  await expect(card.locator('.f-now')).toContainText('16 are running')
})

test('zero and thirty are real ceilings; idle glyph rests and disabled boundaries keep focus', async ({ page }) => {
  const { work } = await setup(page, 'zero')
  work.preferences['agents.working'] = { total: 1, limits: { codex: 0, claude: 'no_limit', cursor: 'off' } }
  await page.route('**/api/agents/plan', route => route.fulfill({ json: { ...work.preferences['agents.working'], principal_id: me.id, running: {}, running_total: 0, source: 'plan', updated_at: null } }))
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.goto('/agents')
  const card = dial(page), more = totalMore(page), fewer = totalFewer(page)
  await expect(card.locator('.f-num')).toHaveText('1')
  await expect(card.locator('[data-key="codex"] .lim-num')).toHaveText('0')
  await expect(card.locator('.f-bot')).not.toHaveClass(/busy/)
  expect(await card.locator('.f-bot').evaluate(el => el.getAnimations({ subtree: true }).length)).toBe(0)
  // Only the released value is saved, after the settle time; wait for that save to land.
  const savedOne = page.waitForResponse(r => r.request().method() === 'PUT' && r.url().endsWith('/api/preferences/agents.working') && r.request().postDataJSON().value.total === 1)
  await expectStableControls({ controls: { more, fewer, fold: card.locator('.fs-tog'), unit: card.locator('.f-unit') }, interactions: [{ name: 'one to zero', run: async () => { await fewer.click(); await expect(card.locator('.f-num')).toHaveText('0'); await expect(more).toBeFocused() } }, { name: 'zero to one', run: async () => { await more.click(); await expect(card.locator('.f-num')).toHaveText('1') } }] })
  await savedOne
  await expect.poll(() => work.preferences['agents.working']).toMatchObject({ total: 1 })
  work.preferences['agents.working'] = { total: 29, limits: { codex: 30 } }
  await page.reload()
  await expect(card.locator('.f-num')).toHaveText('29')
  await more.click()
  await expect(card.locator('.f-num')).toHaveText('30')
  await expect(more).toBeDisabled()
  await expect(fewer).toBeFocused()
  await expect(card.locator('[data-key="codex"] .pm').last()).toBeEnabled()
  await card.locator('[data-key="codex"] .pm').last().click()
  await expect(card.locator('[data-key="codex"] .value svg')).toBeVisible()
  await expect(card.locator('[data-key="codex"] .pm').last()).toBeDisabled()
})

test('stored numeric zero stays visible and boundaries do not save unchanged values', async ({ page }) => {
  const { work } = await setup(page, 'zero')
  work.preferences['agents.working'] = { total: 30, limits: { codex: 0 } }
  let writes = 0
  await page.route('**/api/preferences/agents.working', async route => {
    if (route.request().method() === 'PUT') writes++
    await route.fallback()
  })
  await page.goto('/agents')
  const card = dial(page), row = card.locator('[data-key="codex"]'), plus = row.locator('.pm').last(), minus = row.locator('.pm').first()
  await expect(row.locator('.f-n')).toHaveText('0')
  await expect(minus).toBeDisabled()
  await plus.focus(); await plus.press('ArrowDown')
  await expect(row.locator('.f-n')).toHaveText('0')
  await totalFewer(page).focus(); await totalFewer(page).press('ArrowUp')
  expect(writes).toBe(0)
  await plus.click()
  await expect(row.locator('.f-n')).toHaveText('1')
  await expect(minus).toBeEnabled()
  await expect.poll(() => writes).toBe(1)
  await expect.poll(() => work.preferences['agents.working']).toMatchObject({ total: 30, limits: { codex: 1 } })
  await minus.click()
  await expect(row.locator('.f-n')).toHaveText('off')
  await expect(minus).toBeDisabled()
  await expect(card.locator('.f-live')).toHaveAttribute('role', 'status')
})

test('save failure remains visible after a successful scheduled poll', async ({ page }) => {
  await page.clock.install({ time: NOW })
  await setup(page)
  await page.route('**/api/preferences/agents.working', route => route.request().method() === 'PUT' ? route.fulfill({ status: 500, json: { error: 'save failed' } }) : route.fallback())
  let reads = 0
  await page.route('**/api/agents/plan', async route => { reads++; await route.fallback() })
  await page.goto('/agents')
  await expect(dial(page).locator('.f-num')).toHaveText('5')
  await totalMore(page).click()
  // The released value is saved once the settle time has passed.
  await page.clock.runFor(SETTLE_MS)
  await expect(dial(page).locator('.f-live')).toContainText('Couldn’t save')
  const initialReads = reads
  await page.clock.fastForward(15_000)
  await expect.poll(() => reads).toBeGreaterThan(initialReads)
  await expect(dial(page).locator('.f-live')).toContainText('Couldn’t save')
})

test('a conflicted save re-reads newer limits before the next deliberate change', async ({ page }) => {
  const { work } = await setup(page, 'room')
  let revision: string | null = null
  const puts: { value: { total: number; limits: Record<string, unknown> }; expected_updated_at: string | null }[] = []
  await page.route('**/api/agents/plan', route => route.fulfill({ json: { ...work.preferences['agents.working'], principal_id: me.id, running: {}, running_total: 0, source: 'plan', updated_at: revision } }))
  await page.route('**/api/preferences/agents.working', route => {
    if (route.request().method() !== 'PUT') return route.fallback()
    const request = route.request().postDataJSON()
    puts.push(request)
    if (puts.length === 1) {
      work.preferences['agents.working'] = { total: 7, limits: { claude: 'off' } }
      revision = '2026-10-02T18:00:00.000001Z'
      return route.fulfill({ status: 409, json: { error: 'changed' } })
    }
    expect(request.expected_updated_at).toBe(revision)
    work.preferences['agents.working'] = request.value
    revision = '2026-10-02T18:00:00.000002Z'
    return route.fulfill({ json: { key: 'agents.working', value: request.value, updated_at: revision } })
  })
  await page.goto('/agents')
  await expect(dial(page).locator('.f-num')).toHaveText('8')
  await totalFewer(page).click()
  await expect(dial(page).locator('.f-live')).toContainText('changed elsewhere')
  await expect(dial(page).locator('.f-num')).toHaveText('7')
  await expect(dial(page).locator('[data-key="claude"]').getByRole('radio', { name: 'Off', exact: true })).toHaveAttribute('aria-checked', 'true')
  await totalFewer(page).click()
  await expect(dial(page).locator('.f-num')).toHaveText('6')
  await expect.poll(() => puts.length).toBe(2)
  expect(puts[0]!.expected_updated_at).toBeNull()
  expect(puts[1]!.value).toEqual({ total: 6, limits: { claude: 'off' } })
})

test('running agents hover, look and blink with motion allowed, and stop under reduced motion', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await setup(page, 'room')
  await page.goto('/agents')
  const glyph = dial(page).locator('.f-bot')
  await expect(glyph).toHaveClass(/busy/)
  const animations = await glyph.evaluate(el => el.getAnimations({ subtree: true }).map(animation => ({ name: (animation as CSSAnimation).animationName, state: animation.playState, iterations: animation.effect!.getTiming().iterations })))
  expect(animations.map(a => a.name.replace(/^(f-(?:blink|hover|look|tip)).*$/, '$1')).sort()).toEqual(['f-blink', 'f-hover', 'f-look', 'f-tip'])
  expect(animations.every(a => a.state === 'running' && a.iterations === Infinity)).toBe(true)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await expect.poll(() => glyph.evaluate(el => el.getAnimations({ subtree: true }).length)).toBe(0)
})

for (const width of [1440, 390]) test(`all seven harnesses fit at ${width} and remain independent of the total`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  const { work } = await setup(page, 'room', true)
  work.preferences['agents.working'] = { total: 1, limits: { codex: 30, claude: 30, cursor: 30, grok: 'no_limit', pi: 'off', gemini: 0, opencode: 3 } }
  await page.goto('/agents')
  const card = dial(page)
  await expect(card.locator('.f-chip')).toHaveCount(7)
  await expect(card.locator('[data-harness="codex"] .f-n')).toHaveText('30')
  await expect(card.locator('[data-harness="gemini"] .f-n')).toHaveText('0')
  expect(await card.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
  await expectStableControls({ controls: { more: totalMore(page), fewer: totalFewer(page), fold: card.locator('.fs-tog') }, interactions: [{ name: 'unfold seven harnesses', run: () => card.locator('.fs-tog').click() }] })
  await expect(card.locator('.rows > li')).toHaveCount(7)
  await expect(card.locator('[data-key="claude"] .lim-num')).toHaveText('30')
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
})

test('linked viewer keeps room on alias-owned accounts while counts use the canonical owner', async ({ page }) => {
  const { work, data } = await setup(page, 'room')
  data.accounts = data.accounts.map(a => ({ ...a, owner_person_id: me.id }))
  await page.route('**/api/agents/plan', route => route.fulfill({ json: { ...work.preferences['agents.working'], principal_id: 'canonical-person', running: { codex: 2, claude: 1 }, running_total: 3, source: 'plan', updated_at: null } }))
  await page.goto('/agents')
  await expect(dial(page).locator('.f-right')).toContainText('Room for 9 more right now.')
  await expect(dial(page).locator('.f-right')).toContainText('Codex room for 4 more')
  await expect(dial(page).locator('.f-live')).toContainText('3 running · room for 5 more')
})

test('typed totals clamp, invalid drafts do not write, and field shortcuts remain native', async ({ page }) => {
  const { work } = await setup(page, 'room', true)
  let writes = 0
  await page.route('**/api/preferences/agents.working', async route => {
    if (route.request().method() === 'PUT') writes++
    await route.fallback()
  })
  await page.goto('/agents')
  const card = dial(page), total = card.locator('.f-num'), chip = card.locator('[data-harness="codex"]')
  await expect(total).toHaveText('8')
  for (const draft of ['', '-1', '1.5', 'bad']) {
    await total.locator('.value').click(); await total.locator('input').fill(draft); await total.locator('input').press('Enter')
    await expect(total).toHaveText('8')
  }
  await chip.locator('.value').click(); await chip.locator('input').fill('6')
  await chip.locator('input').press('Control+Enter'); await expect(chip.locator('input')).toBeFocused()
  await chip.locator('input').press('Meta+Enter'); await expect(chip.locator('input')).toBeFocused()
  await chip.locator('input').press('ArrowUp'); await expect(chip.locator('input')).toHaveValue('6')
  await chip.locator('input').press('Escape'); await expect(chip.locator('.f-n')).toHaveText('4')
  await chip.locator('.pm').first().focus(); await chip.locator('.pm').first().press('Control+Home')
  await chip.locator('.pm').first().press('Meta+End'); await expect(chip.locator('.f-n')).toHaveText('4')
  expect(writes).toBe(0)
  await total.locator('.value').click(); await total.locator('input').fill('300'); await total.locator('input').press('Enter')
  await expect(total).toHaveText('30')
  await expect.poll(() => work.preferences['agents.working']).toMatchObject({ total: 30 })
  await total.locator('.value').click(); await total.locator('input').fill('0'); await chip.locator('.value').focus()
  await expect(total).toHaveText('0')
  await expect.poll(() => work.preferences['agents.working']).toMatchObject({ total: 0 })
})

test('a typed write failure restores the confirmed harness value', async ({ page }) => {
  await setup(page, 'room', true)
  await page.route('**/api/preferences/agents.working', route => route.request().method() === 'PUT' ? route.fulfill({ status: 403, json: { error: 'forbidden' } }) : route.fallback())
  await page.goto('/agents')
  const card = dial(page), chip = card.locator('[data-harness="codex"]')
  await expect(chip.locator('.f-n')).toHaveText('4')
  await expectStableControls({
    controls: { value: chip.locator('.f-n'), minus: chip.locator('.pm').first(), plus: chip.locator('.pm').last(), fold: card.locator('.fs-tog'), live: card.locator('.f-live') },
    interactions: [{ name: 'failed typed limit', run: async () => {
      await chip.locator('.value').click(); await chip.locator('input').fill('30'); await chip.locator('input').press('Enter')
      await expect(card.locator('.f-live')).toContainText('Couldn’t save')
      await expect(chip.locator('.f-n')).toHaveText('4')
    } }],
  })
})

test('a changed plan revision discards a typed draft even when its value is unchanged', async ({ page }) => {
  await page.clock.install({ time: NOW })
  const { work } = await setup(page, 'room', true)
  let revision: string | null = null, reads = 0, writes = 0
  await page.route('**/api/agents/plan', route => {
    reads++
    return route.fulfill({ json: { ...work.preferences['agents.working'], principal_id: me.id, running: {}, running_total: 0, source: 'plan', updated_at: revision } })
  })
  await page.route('**/api/preferences/agents.working', async route => {
    if (route.request().method() === 'PUT') writes++
    await route.fallback()
  })
  await page.goto('/agents')
  const total = dial(page).locator('.f-num')
  await expect(total).toHaveText('8')
  await total.locator('.value').click(); await total.locator('input').fill('25')
  const initialReads = reads
  revision = '2026-10-03T13:00:00.000001Z'
  await page.clock.fastForward(15_000)
  await expect.poll(() => reads).toBeGreaterThan(initialReads)
  await expect(total.locator('input')).toHaveCount(0)
  await expect(total).toHaveText('8')
  await totalMore(page).focus()
  expect(writes).toBe(0)
})

test.describe('phone touch targets', () => {
  test.use({ viewport: { width: 390, height: 1000 }, hasTouch: true, isMobile: true })
  test('every dial target is at least 44 px and all seven harnesses stay stable', async ({ page }) => {
    const { work } = await setup(page, 'room', true)
    work.preferences['agents.working'] = { total: 8, limits: { codex: 4, claude: 'no_limit', cursor: 'off', grok: 2, pi: 'off', gemini: 'no_limit', opencode: 3 } }
    await page.goto('/agents')
    const card = dial(page), chip = card.locator('[data-harness="codex"]'), fold = card.locator('.fs-tog')
    await expect(chip).toBeVisible()
    const targets = async () => {
      const sizes = await card.locator('button:visible').evaluateAll(elements => elements.map(el => { const box = el.getBoundingClientRect(); return { width: box.width, height: box.height } }))
      expect(sizes.length).toBeGreaterThan(0)
      for (const size of sizes) { expect(size.width).toBeGreaterThanOrEqual(44); expect(size.height).toBeGreaterThanOrEqual(44) }
    }
    await targets()
    // Folded on a phone, the mark sits between − and the value, and still cycles.
    const xs = await Promise.all(['.pm.dec', '.f-mode', '.value-slot', '.pm.inc'].map(async s => (await chip.locator(s).boundingBox())!.x))
    expect(xs).toEqual([...xs].sort((a, b) => a - b))
    await expectStableControls({
      controls: { fold, more: totalMore(page), fewer: totalFewer(page), chip, cycle: chip.locator('.f-mode'), minus: chip.locator('.pm').first(), plus: chip.locator('.pm').last(), value: chip.locator('.f-n') },
      scrollAreas: { card, chips: card.locator('.f-chips') },
      interactions: [
        { name: 'touch step', run: () => chip.locator('.pm').last().tap() },
        ...[1, 2, 3].map(n => ({ name: `touch cycle ${n}`, run: () => chip.locator('.f-mode').tap() })),
      ],
    })
    await fold.tap(); await expect(card.locator('.rows > li')).toHaveCount(7)
    await targets()
    await expectStableControls({
      controls: { fold, more: totalMore(page), selectors: card.locator('[data-key="codex"] .seg'), plus: card.locator('[data-key="codex"] .pm').last(), row: card.locator('[data-key="codex"]') },
      scrollAreas: { card },
      interactions: ['No limit', 'Off', 'At most'].map(name => ({ name: `touch mode ${name}`, run: () => card.locator('[data-key="codex"]').getByRole('radio', { name, exact: true }).tap() })),
    })
  })
})

test('long German waiting details wrap at every evidence width and theme', async ({ page }) => {
  await setup(page, 'room')
  const reason = 'Die automatische Bearbeitung wartet auf die Freigabe der zuständigen Projektverantwortlichen und die Synchronisierung der verfügbaren Agentenkonten.'
  await page.route('**/api/queue', route => route.fulfill({ json: { items: [{ node_id: 'waiting-de', key: 'AEON-647', title: 'Freigabe der automatischen Bearbeitung', state: 'open', priority: 'normal', estimate_hours: 1, queued: { model_profile_id: null, waiting: true, wait_reason: reason } }], manual_order: false, capacity: {} } }))
  await page.goto('/agents')
  const card = dial(page)
  await expect(card.locator('.f-wait')).toContainText(reason)
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: 1600 })
    await page.emulateMedia({ colorScheme: theme })
    expect(await card.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
    await card.screenshot({ path: `${shots}/${width}-${theme}-long-german-unfolded.png`, animations: 'disabled' })
  }
})

// AEON-781: press and hold on − and +, like a remote's volume button.
test('AEON-781: a held + speeds up, pauses before a mode boundary and saves only the final value once', async ({ page }) => {
  await page.clock.install({ time: NOW })
  await setup(page, 'wind')
  const puts: { limits: Record<string, unknown> }[] = []
  await page.route('**/api/preferences/agents.working', async route => {
    if (route.request().method() === 'PUT') puts.push(route.request().postDataJSON().value)
    await route.fallback()
  })
  await page.goto('/agents')
  const row = dial(page).locator('[data-key="codex"]'), plus = row.locator('.pm.inc'), minus = row.locator('.pm.dec'), value = row.locator('.f-n'), said = row.locator('[aria-live]')
  await expect(value).toHaveText('4')
  await page.clock.pauseAt(NOW + 60_000)
  await plus.hover(); await page.mouse.down()
  await expect(value).toHaveText('5')
  await page.clock.runFor(449); await expect(value).toHaveText('5')
  await page.clock.runFor(1); await expect(value).toHaveText('6')
  await page.clock.runFor(360 + 290); await expect(value).toHaveText('8')
  await page.clock.runFor(20_000)
  // 30 → no own limit is a boundary: the hold pauses there instead of crossing.
  await expect(value).toHaveText('30')
  await expect(plus).toBeEnabled()
  expect(puts).toEqual([])
  await expect(said).toHaveText('')
  await page.mouse.up()
  await page.clock.runFor(SETTLE_MS)
  await expect.poll(() => puts.length).toBe(1)
  expect(puts[0]!.limits.codex).toBe(30)
  await expect(said).toHaveText('at most 30')
  // A fresh press crosses.
  await page.mouse.down(); await page.mouse.up()
  await expect(row.locator('.value svg')).toBeVisible()
  await expect(plus).toBeDisabled()
  // Down from no own limit, the hold pauses at 1 instead of turning the harness off.
  await minus.hover(); await page.mouse.down()
  await page.clock.runFor(20_000)
  await expect(value).toHaveText('1')
  await page.mouse.up()
  await page.mouse.down(); await page.mouse.up()
  await expect(value).toHaveText('off')
  await page.clock.runFor(SETTLE_MS)
  await expect.poll(() => puts.at(-1)?.limits.codex).toBe('off')
  expect(puts.length).toBeLessThanOrEqual(4)
})

test('AEON-781: an arrow key keeps its own pace; leaving, blur and a disabled end stop a hold', async ({ page }) => {
  await page.clock.install({ time: NOW })
  const { work } = await setup(page, 'wind')
  await page.goto('/agents')
  const card = dial(page), total = card.locator('.f-num'), more = totalMore(page), fewer = totalFewer(page)
  await expect(total).toHaveText('5')
  await page.clock.pauseAt(NOW + 60_000)
  // The OS key repeat (repeat: true) is ignored; the hold paces itself.
  await more.focus()
  await page.keyboard.down('ArrowUp'); await expect(total).toHaveText('6')
  for (let i = 0; i < 5; i++) await page.keyboard.down('ArrowUp')
  await expect(total).toHaveText('6')
  await page.clock.runFor(450); await expect(total).toHaveText('7')
  await page.keyboard.up('ArrowUp')
  await page.clock.runFor(5_000); await expect(total).toHaveText('7')
  // Leaving the button ends a pointer hold.
  await more.hover(); await page.mouse.down(); await expect(total).toHaveText('8')
  await page.mouse.move(1, 1)
  await page.clock.runFor(5_000); await expect(total).toHaveText('8')
  await page.mouse.up()
  // A window blur ends a hold.
  await more.hover(); await page.mouse.down(); await expect(total).toHaveText('9')
  await page.evaluate(() => window.dispatchEvent(new Event('blur')))
  await page.clock.runFor(5_000); await expect(total).toHaveText('9')
  await page.mouse.up()
  // Held down to 0, − runs out, the hold ends and focus moves to +.
  await fewer.hover(); await page.mouse.down()
  await page.clock.runFor(20_000)
  await expect(total).toHaveText('0'); await expect(fewer).toBeDisabled(); await expect(more).toBeFocused()
  await page.mouse.up()
  // A long press opens no context menu and selects no text.
  expect(await more.evaluate(el => el.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true })))).toBe(false)
  expect(await more.evaluate(el => getComputedStyle(el).userSelect)).toBe('none')
  await page.clock.runFor(SETTLE_MS)
  await expect.poll(() => work.preferences['agents.working']).toMatchObject({ total: 0 })
})

test('AEON-781: a save conflict ends a hold; the next step needs a fresh press', async ({ page }) => {
  await page.clock.install({ time: NOW })
  const { work } = await setup(page, 'wind')
  let revision: string | null = null, answer!: () => void
  const gate = new Promise<void>(resolve => { answer = resolve })
  const puts: { value: { total: number }; expected_updated_at: string | null }[] = []
  await page.route('**/api/agents/plan', route => route.fulfill({ json: { ...work.preferences['agents.working'], principal_id: me.id, running: {}, running_total: 0, source: 'plan', updated_at: revision } }))
  await page.route('**/api/preferences/agents.working', async route => {
    if (route.request().method() !== 'PUT') return route.fallback()
    const request = route.request().postDataJSON()
    puts.push(request)
    if (puts.length === 1) {
      await gate
      work.preferences['agents.working'] = { total: 12, limits: {} }
      revision = '2026-10-02T18:00:00.000001Z'
      return route.fulfill({ status: 409, json: { error: 'changed' } })
    }
    work.preferences['agents.working'] = request.value
    revision = '2026-10-02T18:00:00.000002Z'
    return route.fulfill({ json: { key: 'agents.working', value: request.value, updated_at: revision } })
  })
  await page.goto('/agents')
  const total = dial(page).locator('.f-num'), more = totalMore(page), said = dial(page).locator('.f-pm-total [aria-live]')
  await expect(total).toHaveText('5')
  await page.clock.pauseAt(NOW + 60_000)
  await more.click()
  await page.clock.runFor(SETTLE_MS)
  await expect.poll(() => puts.length).toBe(1)
  // A hold starts while that write is still out.
  await more.hover(); await page.mouse.down()
  await expect(total).toHaveText('7')
  answer()
  await expect(dial(page).locator('.f-live')).toContainText('changed elsewhere')
  await expect(total).toHaveText('12')
  // The hold has ended: still held, nothing repeats, the warning stays and the newly read total is announced.
  await expect(said).toHaveText('Run up to 12 at once')
  await page.clock.runFor(5_000)
  await expect(total).toHaveText('12')
  await expect(dial(page).locator('.f-live')).toContainText('changed elsewhere')
  await page.mouse.up()
  await page.clock.runFor(SETTLE_MS)
  expect(puts).toHaveLength(1)
  // A fresh press edits the newly read revision.
  await page.mouse.down(); await page.mouse.up()
  await expect(total).toHaveText('13')
  await page.clock.runFor(SETTLE_MS)
  await expect.poll(() => puts.length).toBe(2)
  expect(puts[1]).toEqual({ value: { total: 13, limits: {} }, expected_updated_at: '2026-10-02T18:00:00.000001Z' })
})

test('AEON-781: the chevron comes first, folding moves nothing above the body, and the fold follows the person', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  const { work } = await setup(page, 'wind')
  delete work.preferences['ui.agents.sections']
  await page.goto('/agents')
  const card = dial(page), tog = card.locator('.fs-tog')
  // First visit: open.
  await expect(tog).toHaveAttribute('aria-expanded', 'true')
  await expect(tog).toHaveAttribute('aria-label', 'Agents at once: fold')
  expect(await card.locator('.fs-head').evaluate(el => el.firstElementChild?.classList.contains('fs-tog'))).toBe(true)
  await expect(card.locator('[data-key="codex"]')).toBeVisible()
  // Sample every frame of the fold: the chevron and the sentence with its stepper stay put.
  const watchFold = () => card.evaluate(async el => {
    const parts = ['.fs-tog', '.f-dial', '.f-num'].map(s => el.querySelector(s)!)
    const rects = () => JSON.stringify(parts.map(p => { const r = p.getBoundingClientRect(); return [r.x, r.y, r.width, r.height] }))
    const before = rects(), body = el.querySelector('.fs-body')!
    let done = false
    body.addEventListener('transitionend', () => { done = true }, { once: true })
    ;(el.querySelector('.fs-tog') as HTMLButtonElement).click()
    let frames = 0, moved = 0
    while (!done) { await new Promise(r => requestAnimationFrame(r)); frames++; if (rects() !== before) moved++ }
    return { frames, moved }
  })
  const folding = await watchFold()
  expect(folding.frames).toBeGreaterThan(1)
  expect(folding.moved).toBe(0)
  await expect(tog).toHaveAttribute('aria-expanded', 'false')
  await expect(card.locator('.fs-body')).toHaveAttribute('inert', '')
  await expect(card.locator('[data-key="codex"]')).toBeHidden()
  await expect(card.locator('[data-harness="codex"]')).toBeVisible()
  await expect.poll(() => work.preferences['ui.agents.sections']).toEqual({ dial: false, accounts: false, sessions: true, queued: true })
  // The fold follows the person, and the cache paints it before the server answers.
  let release!: () => void
  const held = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/preferences/ui.agents.sections', async route => { if (route.request().method() === 'GET') await held; await route.fallback() })
  await page.reload()
  await expect(tog).toHaveAttribute('aria-expanded', 'false')
  release()
  const unfolding = await watchFold()
  expect(unfolding.moved).toBe(0)
  await expect(card.locator('[data-key="codex"]')).toBeVisible()
  await expect.poll(() => work.preferences['ui.agents.sections']).toMatchObject({ dial: true })
})

test('AEON-781: a dial folded before the fold sections stays folded', async ({ page }) => {
  const { work } = await setup(page, 'wind')
  delete work.preferences['ui.agents.sections']
  work.preferences['agents.working.display'] = { folded: true }
  await page.goto('/agents')
  await expect(dial(page).locator('.fs-tog')).toHaveAttribute('aria-expanded', 'false')
  await expect(dial(page).locator('[data-harness="codex"]')).toBeVisible()
})
