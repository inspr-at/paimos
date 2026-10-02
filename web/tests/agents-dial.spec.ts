// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import AxeBuilder from '@axe-core/playwright'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { capacityWorld, NOW } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import type { PlanSnapshot } from '../src/lib/agentsWorking'
const shots = process.env.AEON_540_SHOTS
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
  work.preferences['agents.working.display'] = { folded }
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
      return { ...a, routing: { rank: 1, available_slots: slots } }
    }) })
  })
  const queue = state === 'room' ? [] : Array.from({ length: state === 'off' ? 2 : 3 }, (_, i) => ({ node_id: `waiting-${i}`, key: `AEON-${i}`, title: 'Waiting work', state: 'open', priority: 'normal', estimate_hours: 1, queued: { model_profile_id: null, waiting: false, wait_reason: '' } }))
  await page.route('**/api/queue', route => route.fulfill({ json: { items: queue, manual_order: false, capacity: {} } }))
  return { work, calls, data }
}
const dial = (page: Page) => page.getByRole('region', { name: 'Agents at once' })
const totalMore = (page: Page) => dial(page).getByRole('button', { name: 'One agent more at once' })
const totalFewer = (page: Page) => dial(page).getByRole('button', { name: 'One agent fewer at once' })
for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) for (const state of Object.keys(states) as (keyof typeof states)[]) {
  test(`${width} ${theme} ${state}: stepping, modes and folding keep every control put`, async ({ page }) => {
    // Three stability guards plus optional captures: allow hosted workers=2
    // load without weakening any geometry, interaction or overflow assertion.
    test.setTimeout(90_000)
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const errors = watchErrors(page), { work, calls } = await setup(page, state)
    await page.goto('/agents')
    const card = dial(page), codex = card.locator('[data-key="codex"]'), fold = card.locator('.f-fold')
    const more = totalMore(page), fewer = totalFewer(page)
    await expect(codex).toBeVisible()
    await expect(card.locator('.f-num')).toHaveText(String(states[state].total))
    await expect(card.locator('.f-bot')).toHaveClass(/busy/)
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
      controls: { more, fewer, fold, dial: card.locator('.f-dial'), total: card.locator('.f-num'), row: codex, selectors: codex.getByRole('radiogroup'), none: codex.getByRole('radio', { name: 'No limit', exact: true }), max: codex.getByRole('radio', { name: 'At most', exact: true }), off: codex.getByRole('radio', { name: 'Off', exact: true }) },
      scrollAreas: { card, rows: card.locator('.rows') },
      interactions: [
        { name: 'cross total digit boundary', run: async () => { for (let n = states[state].total; n < 10; n++) await more.click(); await expect(card.locator('.f-num')).toHaveText('10') } },
        { name: 'lower ceiling', run: async () => { await fewer.click(); await expect(card.locator('.f-num')).toHaveText('9') } },
        ...['Off', 'No limit', 'At most'].map(name => ({ name: `mode ${name}`, run: async () => { await codex.getByRole('radio', { name, exact: true }).click(); await expect(codex.getByRole('radio', { name, exact: true })).toHaveAttribute('aria-checked', 'true') } })),
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
      controls: { more, fewer, fold, chips: card.locator('.f-chips'), chip, cycle, dec, inc, claude: card.locator('[data-harness="claude"]'), cursor: card.locator('[data-harness="cursor"]') }, scrollAreas: { card, chips: card.locator('.f-chips') },
      interactions: [
        { name: 'compact plus', run: () => inc.click() }, { name: 'compact minus', run: () => dec.click() },
        ...[1, 2, 3].map(n => ({ name: `cycle harness ${n}`, run: () => cycle.click() })),
      ],
    })
    if (width === 390) expect((await inc.boundingBox())!.width).toBe(24)
    await expect.poll(() => work.preferences['agents.working.display']).toEqual({ folded: true })
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
    expect(calls.filter(c => c.method !== 'GET' && /harness-sessions|\/controls|\/stop|\/interrupt/.test(c.path))).toEqual([])
    expect(errors).toEqual([])
  })
}

test('fold memory survives reload; mode and step keys preserve browser shortcuts', async ({ page }) => {
  const { work } = await setup(page, 'wind', true)
  await page.goto('/agents')
  const card = dial(page), fold = card.locator('.f-fold'), more = totalMore(page)
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
  await expectStableControls({ controls: { more, fewer: totalFewer(page), fold: card.locator('.f-fold'), live: card.locator('.f-live') }, interactions: [{ name: 'write failure', run: async () => { await more.click(); await expect(card.locator('.f-live')).toContainText('Couldn’t save'); await expect(card.locator('.f-num')).toHaveText('5') } }] })
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
  await expectStableControls({ controls: { more, fewer, fold: card.locator('.f-fold'), unit: card.locator('.f-unit') }, interactions: [{ name: 'one to zero', run: async () => { await fewer.click(); await expect(card.locator('.f-num')).toHaveText('0'); await expect(more).toBeFocused() } }, { name: 'zero to one', run: async () => { await more.click(); await expect(card.locator('.f-num')).toHaveText('1') } }] })
  await expect.poll(() => work.preferences['agents.working']).toMatchObject({ total: 1 })
  work.preferences['agents.working'] = { total: 29, limits: { codex: 30 } }
  await page.reload()
  await expect(card.locator('.f-num')).toHaveText('29')
  await more.click()
  await expect(card.locator('.f-num')).toHaveText('30')
  await expect(more).toBeDisabled()
  await expect(fewer).toBeFocused()
  await expect(card.locator('[data-key="codex"] button').last()).toBeDisabled()
})

test('expanded limits cannot create bare zero; API zero and total boundaries do not save unchanged values', async ({ page }) => {
  const { work } = await setup(page, 'zero')
  work.preferences['agents.working'] = { total: 0, limits: { codex: 1 } }
  let writes = 0
  await page.route('**/api/preferences/agents.working', async route => {
    if (route.request().method() === 'PUT') writes++
    await route.fallback()
  })
  await page.goto('/agents')
  const card = dial(page), row = card.locator('[data-key="codex"]'), plus = row.getByRole('button', { name: 'Codex: at most one more' }), minus = row.getByRole('button', { name: 'Codex: at most one fewer' })
  await expect(minus).toBeDisabled()
  await plus.focus(); await plus.press('ArrowDown')
  await expect(row.locator('.lim-num')).toHaveText('1')
  await row.getByRole('radio', { name: 'At most', exact: true }).click()
  await totalMore(page).focus(); await totalMore(page).press('ArrowDown')
  await expect(card.locator('.f-num')).toHaveText('0')
  expect(writes).toBe(0)
  await expect(card.locator('.f-num')).not.toHaveAttribute('aria-live')
  await expect(card.locator('.f-live')).toHaveAttribute('role', 'status')
  work.preferences['agents.working'] = { total: 30, limits: { codex: 0 } }
  await page.reload()
  await expect(row.locator('.lim-num')).toHaveText('0')
  await expect(minus).toBeDisabled()
  await card.locator('.f-fold').click()
  const storedZero = card.locator('[data-harness="codex"]')
  await expect(storedZero.locator('.pm').first()).toBeDisabled()
  await expect(storedZero.locator('.pm').last()).toBeEnabled()
  await storedZero.locator('.pm').last().focus()
  await storedZero.locator('.pm').last().press('ArrowLeft')
  await expect(storedZero.locator('.f-n')).toHaveText('0')
  await card.locator('.f-fold').click()
  await totalFewer(page).focus(); await totalFewer(page).press('ArrowUp')
  await plus.focus(); await plus.press('ArrowDown')
  expect(writes).toBe(0)
  await plus.click()
  await expect(row.locator('.lim-num')).toHaveText('1')
  await expect(minus).toBeDisabled()
  await expect.poll(() => writes).toBe(1)
  await expect.poll(() => work.preferences['agents.working']).toMatchObject({ total: 30, limits: { codex: 1 } })
  await card.locator('.f-fold').click()
  const chip = card.locator('[data-harness="codex"]')
  await chip.locator('.pm').first().click()
  await expect(chip.locator('.f-n')).toHaveText('off')
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
  await expectStableControls({ controls: { more: totalMore(page), fewer: totalFewer(page), fold: card.locator('.f-fold') }, interactions: [{ name: 'unfold seven harnesses', run: () => card.locator('.f-fold').click() }] })
  await expect(card.locator('.rows > li')).toHaveCount(7)
  await expect(card.locator('[data-key="claude"] .lim-num')).toHaveText('30')
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
})

test('linked viewer keeps room on alias-owned accounts while counts use the canonical owner', async ({ page }) => {
  const { work, data } = await setup(page, 'room')
  data.accounts = data.accounts.map(a => ({ ...a, owner_person_id: me.id }))
  await page.route('**/api/agents/plan', route => route.fulfill({ json: { ...work.preferences['agents.working'], principal_id: 'canonical-person', running: { codex: 2, claude: 1 }, running_total: 3, source: 'plan', updated_at: null } }))
  await page.goto('/agents')
  await expect(dial(page).locator('.f-right')).toContainText('Room for 9 more right now, 12 at once in all.')
  await expect(dial(page).locator('.f-live')).toContainText('3 running · room for 5 more')
})
