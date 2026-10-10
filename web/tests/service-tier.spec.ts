// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { expectStableControls } from './helpers/stable'
import { mockEffectivePermissions } from './authz-fixtures'
import type { TierReport, TierState } from '../src/lib/serviceTier'

const id = '5e000000-0000-4000-8000-000000000001'
const ownership = { daemon_id: 'fixture', generation: 'a'.repeat(32), process_id: 'b'.repeat(32), root_pid: 1234, group_id: 1234, started_at: '2026-10-02T00:00:00Z' }
// Deliberately synthetic prices, never a production vendor report or catalog pin.
const report: TierReport = { harness: 'codex', model: 'fixture-model', harness_version: 'fixture-cli', adapter_version: 'fixture-adapter', checked_at: '2026-10-02T00:00:00Z', source: 'https://example.invalid/synthetic-price', applies: 'next_run', change_instructions: 'Change it in its terminal.', tiers: [
  { tier: 'default', name: 'Default', offered: true, price_multiplier: 1, usage_multiplier: 1, speed_factor: 1, mechanism: 'none' },
  { tier: 'fast', name: 'Fast', offered: true, price_multiplier: 2, usage_multiplier: 3, speed_factor: 2, mechanism: 'fixture fast' },
  { tier: 'fastest', name: 'Fastest', offered: true, price_multiplier: 6, usage_multiplier: 8, speed_factor: 8, mechanism: 'fixture fastest' },
] }
async function setup(page: Page, options: { unpriced?: boolean; unmanaged?: boolean; ended?: boolean; denied?: boolean; request?: boolean; fail?: boolean; active?: 'default' | 'fast'; agent?: boolean; cancelReceipt?: boolean; managedControls?: boolean; evidence?: boolean; tierRead?: 'delayed' | 'error' } = {}) {
  await mockWork(page, fixtures(), { admin: true, principalKind: options.agent ? 'agent' : 'person' })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  data.approvals.splice(0)
  data.sessions.splice(3)
  const reports = [{ ...report, tiers: report.tiers.map(t => options.unpriced && t.tier !== 'default' ? { ...t, offered: false, price_multiplier: null, speed_factor: null, reason: 'not offered: no published price' } : t) }]
  const active = options.active ?? 'default'
  Object.assign(data.sessions[0]!, { harness: 'codex', display_label: 'Tier fixture', model: report.model, service_tier: active, service_tier_revision: 1, service_tier_reports: reports, service_tier_request: options.request ? 'fast' : null, advertised_capabilities: ['inbox', 'status', 'stop', 'service_tier_v1'], agent_principal_id: options.agent ? me.id : data.sessions[0]!.agent_principal_id, process_ownership: ownership, process_observed_at: new Date().toISOString(), heartbeat_at: new Date().toISOString(), ...(options.unmanaged ? { management_mode: 'unmanaged' } : {}), ...(options.ended ? { phase: 'stopped', stopped_at: new Date().toISOString() } : {}) })
  if (options.managedControls) data.sessions[0]!.advertised_capabilities.push('managed_control_v1')
  for (let i = 1; i < data.sessions.length; i++) Object.assign(data.sessions[i]!, { harness: 'codex', model: report.model, service_tier: 'default', service_tier_reports: [{ ...report, tiers: report.tiers.map((t, j) => ({ ...t, offered: j <= i - 1 })) }], service_tier_revision: 1 })
  await mockAgents(page, data)
  if (options.denied) await page.route('**/api/me/permissions*', async route => {
    const effective = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') || undefined)
    effective.workspace.permissions = effective.workspace.permissions.filter(p => p !== 'harness.control')
    if (effective.project) effective.project.permissions = effective.project.permissions.filter(p => p !== 'harness.control')
    return route.fulfill({ json: effective })
  })
  let state: TierState = { session_id: id, revision: 1, active_tier: active, pending: null, read_only: !!(options.unmanaged || options.ended), read_only_reason: options.unmanaged ? 'Reported by fixture-cli; change it in its terminal.' : options.ended ? 'This session has ended' : '', reports, requests: options.request ? [{ id: 'request-fixture', session_id: id, tier: 'fast', reason: 'QA waits on this screen', state: 'pending', requested_by_principal_id: String(data.sessions[0]!.agent_principal_id), created_at: new Date().toISOString() }] : [] }
  if (options.evidence) {
    state.run_cost = { model: report.model, run_id: String(data.sessions[0]!.run_id), cost_usd: '4.800000000000', default_cost_usd: '2.400000000000', provisional: false, segments: [{ tier: 'fast', price_multiplier: 2 }] }
    state.estimates = report.tiers.map(t => ({ tier: t.tier, actual: t.tier === 'default', n: 1, basis: 'Last run: 2400000 tokens, $2.40, 15 min (10 min model time) at Default. Last completed matching run · 1 run · frozen price version 7. Only model time scales; tools and waits keep their time.', run_id: 'last-run-fixture', cost_usd: String(2.4 * t.price_multiplier!), duration_ms: 300000 + 600000 / t.speed_factor! }))
    state.history = [
      { id: 4, action: 'changed', from_tier: 'default', to_tier: 'fast', actor_id: me.id, actor_name: 'Markus', asked_by_name: 'fixture-agent', at: '2026-10-02T10:00:00Z' },
      { id: 3, action: 'approved', from_tier: 'default', to_tier: 'fast', actor_id: me.id, actor_name: 'Markus', asked_by_name: 'fixture-agent', at: '2026-10-02T09:00:00Z' },
      { id: 2, action: 'requested', from_tier: 'default', to_tier: 'fast', actor_id: 'fixture-agent', actor_name: 'fixture-agent', asked_by_name: 'fixture-agent', at: '2026-10-02T08:00:00Z' },
    ]
  }
  else state.estimates = report.tiers.map(t => ({ tier: t.tier, n: 0, basis: 'No estimate yet · 0 runs. A completed run with final tokens and frozen prices is required.', run_id: null, cost_usd: null, duration_ms: null }))
  let releaseTierRead!: () => void
  const tierReadGate = new Promise<void>(resolve => { releaseTierRead = resolve })
  await page.route('**/api/decision-desk/projection?**', route => {
    const items = state.requests.filter(q => q.state === 'pending').map(q => ({ id: q.id, kind: 'tier_request', project_id: data.sessions[0]!.project_id, revision: state.revision, title: 'Tier request', created_at: q.created_at, held: false, can_decide: !options.denied, href: `/decision-desk?item=t:${q.id}`, source: `/api/projects/${data.sessions[0]!.project_id}/harness-sessions/${id}/tier` }))
    return route.fulfill({ json: { items, counts: { open: items.length, held: 0, chores: 0 }, has_more: false, as_of: new Date().toISOString() } })
  })
  const writes: Record<string, unknown>[] = []
  let reads = 0
  function cancel(requestID: string) {
    const last = options.cancelReceipt === false
      ? { ...state.pending!, state: 'completed' as const, outcome: 'rejected' as const, reason: 'tier_cancelled' }
      : { ...state.pending!, id: requestID, value: state.active_tier!, state: 'completed' as const, outcome: 'applied' as const, reason: 'tier_cancelled_pending' }
    state = { ...state, revision: state.revision + 1, pending: null, last_change: last, requests: state.requests.map(q => ({ ...q, state: 'pending' })) }
    Object.assign(data.sessions[0]!, { service_tier_revision: state.revision })
  }
  await page.route('**/api/projects/*/harness-sessions/*/tier**', async route => {
    if (route.request().method() === 'GET') { reads++; if (options.tierRead === 'delayed') await tierReadGate; if (options.tierRead === 'error') return route.fulfill({ status: 503, json: { error: 'Tier evidence unavailable.' } }); return route.fulfill({ json: state }) }
    const body = route.request().postDataJSON(); writes.push(body)
    if (options.fail) return route.fulfill({ status: 409, json: { error: 'process ownership changed; refresh' } })
    if (route.request().url().endsWith('/ask')) {
      const request = { id: body.request_id, session_id: id, tier: body.tier, reason: body.reason, state: 'pending' as const, requested_by_principal_id: me.id, created_at: new Date().toISOString() }
      state = { ...state, requests: [request] }; return route.fulfill({ status: 201, json: request })
    }
    if (body.expected_revision !== state.revision) return route.fulfill({ status: 409, json: { error: 'revision changed' } })
    if (body.decision === 'decline') state = { ...state, requests: state.requests.map(q => ({ ...q, state: 'declined' })) }
    else if (state.pending && body.tier === state.active_tier) cancel(body.request_id)
    else state = { ...state, revision: state.revision + 1, pending: { id: body.request_id, session_id: id, kind: 'tier', value: body.tier, state: 'pending', outcome: null, reason: null }, requests: state.requests.map(q => ({ ...q, state: q.tier === body.tier ? 'approved' : q.state })) }
    Object.assign(data.sessions[0]!, { service_tier: state.active_tier, service_tier_revision: state.revision, service_tier_request: state.requests.find(q => q.state === 'pending')?.tier ?? null })
    await route.fulfill({ status: 201, json: state })
  })
  return { data, writes, releaseTierRead, getReads: () => reads, cancelElsewhere: () => cancel('other-viewer-undo'), reject: () => { state = { ...state, pending: null, last_change: { ...state.pending!, state: 'completed', outcome: 'rejected', reason: 'vendor_rejected' } } }, confirm: () => { state = { ...state, active_tier: state.pending!.value, pending: null, revision: state.revision + 1 }; Object.assign(data.sessions[0]!, { service_tier: state.active_tier, service_tier_revision: state.revision }) }, getState: () => state }
}
const row = (page: Page) => page.locator(`[data-row="s:${id}"]`)
// The list uses its inline control when a docked panel narrows its container.
// Do not focus a hidden copy: Undo and rejection assertions must still execute.
const glyph = (page: Page) => row(page).locator('[data-tier]:visible')
const picker = (page: Page) => page.getByRole('dialog', { name: 'Change tier', exact: true })

for (const width of [320, 390]) test(`${width}: phone Change tier menu opens and restores its actual trigger`, async ({ page }) => {
  await page.setViewportSize({ width, height: 900 })
  const { writes } = await setup(page, { managedControls: true })
  await page.goto(`/agents/${id}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  const more = panel.getByRole('button', { name: 'More session controls', exact: true })
  await expectStableControls({ controls: { more }, interactions: [
    { name: 'open tier from overflow', run: async () => {
      await more.click()
      await page.getByRole('menuitem', { name: 'Change tier…', exact: true }).click()
      await expect(picker(page)).toBeVisible()
      await expect(page.getByRole('menu', { name: 'More session actions' })).toHaveCount(0)
      await picker(page).getByRole('button', { name: /Cancel/ }).click()
      await expect(picker(page)).toHaveCount(0)
      await expect(more).toBeFocused()
    } },
  ] })
  expect(writes).toHaveLength(0)
  // Selecting through the same menu targets this session, rather than merely
  // opening an unrelated or detached sheet.
  await more.click()
  await page.getByRole('menuitem', { name: 'Change tier…', exact: true }).click()
  await expect(picker(page).locator('[data-option=fast]')).toHaveAttribute('aria-disabled', 'false')
  await picker(page).locator('[data-option=fast]').click()
  await picker(page).locator('.tp-go').click()
  await expect(page.locator('.tier-toast')).toBeVisible()
  expect(writes).toHaveLength(1)
  expect(writes[0]).toMatchObject({ tier: 'fast', expected_revision: 1, expected_ownership: ownership })
})

test('1440: compact Default tier control keeps its box through confirmation and Undo', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { writes, confirm, getState } = await setup(page)
  await page.goto(`/agents/${id}`)
  await expect(page.locator('.agents-page')).toHaveClass(/panel-open/)
  const list = page.locator('.sessions'), host = row(page).locator('.host-badge'), actions = row(page).locator('.more')
  expect((await list.boundingBox())!.width).toBeLessThanOrEqual(880)
  await expect(glyph(page)).toHaveCount(1)
  await expect(glyph(page)).toBeVisible()
  await expect(glyph(page)).toHaveAccessibleName(/^Service tier: Default/)
  await expectStableControls({ controls: { tier: glyph(page), host, actions, row: row(page) }, scrollAreas: { list }, interactions: [
    { name: 'keyboard step from Default', run: async () => {
      await glyph(page).focus(); await expect(glyph(page)).toBeFocused()
      await glyph(page).press('ArrowRight'); await expect(page.locator('.tier-toast')).toBeVisible()
    } },
    { name: 'daemon confirms Fast', run: async () => {
      confirm()
      // Reading the actual receipt makes the active tier (and price) appear.
      await page.locator('.tier-toast').getByRole('button', { name: 'Undo', exact: true }).click()
      await expect(glyph(page)).toHaveAccessibleName(/^Service tier: Fast/)
      await expect.poll(() => writes.length).toBe(2)
    } },
  ] })
  expect(getState().pending?.value).toBe('default')
  expect(writes[0]).toMatchObject({ tier: 'fast', expected_revision: 1, expected_ownership: ownership })
  expect(writes[1]).toMatchObject({ tier: 'default', expected_revision: 3, expected_ownership: ownership })
})

for (const width of [1280, 1366, 1440]) test(`${width}: panel-open session table fits and keeps Host and overflow controls`, async ({ page }) => {
  await page.setViewportSize({ width, height: 900 })
  const { data } = await setup(page)
  Object.assign(data.sessions[0]!, { eta_ready_at: new Date(Date.now() + 600_000).toISOString(), progress_pct: 50 })
  await page.goto(`/agents/${id}`)
  await expect(page.locator('.agents-page')).toHaveClass(/panel-open/)
  await expect(row(page)).toBeVisible()
  const list = page.locator('.sessions'), actions = row(page).locator('.more'), host = row(page).locator('.host-badge')
  await row(page).hover()
  await expect(actions).toBeVisible(); await expect(host).toBeVisible()
  const measure = async () => {
    const dimensions = await list.evaluate(el => ({ scroll: el.scrollWidth, client: el.clientWidth }))
    expect(dimensions.scroll).toBeLessThanOrEqual(dimensions.client)
    const listBox = (await list.boundingBox())!, actionBox = (await actions.boundingBox())!, hostBox = (await host.boundingBox())!
    expect(actionBox.width).toBeGreaterThan(0); expect(hostBox.width).toBeGreaterThan(0)
    for (const box of [actionBox, hostBox]) {
      expect(box.x).toBeGreaterThanOrEqual(listBox.x)
      expect(box.x + box.width).toBeLessThanOrEqual(listBox.x + listBox.width)
    }
    expect(await actions.evaluate(el => { const r = el.getBoundingClientRect(); return !!document.elementFromPoint(r.right - 1, r.y + r.height / 2)?.closest('.more') })).toBe(true)
  }
  await measure()
  await expectStableControls({ controls: { actions, host, row: row(page) }, interactions: [{ name: 'open overflow', run: async () => { await actions.click(); await expect(page.getByRole('menuitem', { name: 'Change tier…' })).toBeVisible() } }] })
  await measure()
})
test('an unopened row shows its pending agent tier request', async ({ page }) => {
  await setup(page, { request: true }); await page.goto('/agents')
  await expect(row(page)).toContainText('asks for Fast')
  await expect(page.getByRole('region', { name: 'Service tier', exact: true })).toHaveCount(0)
})
test('daemon rejection clears the switch toast and shows the actual outcome', async ({ page }) => {
  const { reject, writes } = await setup(page)
  await page.goto('/agents'); await glyph(page).focus(); await glyph(page).press('ArrowRight')
  await expect(page.locator('.tier-toast')).toBeVisible()
  reject()
  await expect(page.getByText('Change to Fast was not applied. vendor rejected', { exact: true })).toBeVisible()
  await expect(page.locator('.tier-toast')).toHaveCount(0)
  expect(writes).toHaveLength(1)
})
for (const cancelReceipt of [false, true]) {
  test(`pending Undo leaves no panel alert (${cancelReceipt ? 'Undo receipt' : 'cancelled control'})`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    const { getState, writes } = await setup(page, { cancelReceipt })
    await page.goto(`/agents/${id}`)
    const panel = page.getByRole('region', { name: 'Service tier', exact: true })
    await expect(panel.locator('.source')).toContainText('fixture-adapter')
    await glyph(page).focus(); await glyph(page).press('ArrowRight')
    await expect(page.locator('.tier-toast')).toBeVisible()
    await expectStableControls({ controls: { change: panel.getByRole('button', { name: 'Change tier', exact: true }), glyph: glyph(page), row: row(page) }, interactions: [{ name: 'Undo pending', run: async () => {
      await page.locator('.tier-toast').getByRole('button', { name: 'Undo', exact: true }).click()
      await expect(page.locator('.tier-toast')).toHaveCount(0)
    } }] })
    expect(writes).toHaveLength(2); expect(getState().pending).toBeNull(); expect(getState().active_tier).toBe('default')
    await expect(panel.getByRole('alert')).toHaveCount(0)
    // A reopened panel must not rediscover an error in the durable receipt.
    await page.goto('/agents'); await page.goto(`/agents/${id}`)
    await expect(panel.locator('.source')).toContainText('fixture-adapter')
    await expect(panel.getByRole('alert')).toHaveCount(0)
  })
  test(`another viewer's Undo shows a neutral cancellation (${cancelReceipt ? 'Undo receipt' : 'cancelled control'})`, async ({ page }) => {
    await page.clock.install(); await page.setViewportSize({ width: 1440, height: 900 })
    const { cancelElsewhere, getReads } = await setup(page, { cancelReceipt })
    await page.goto(`/agents/${id}`)
    const panel = page.getByRole('region', { name: 'Service tier', exact: true })
    await expect(panel.locator('.source')).toContainText('fixture-adapter')
    await glyph(page).focus(); await glyph(page).press('ArrowRight')
    await expect(page.locator('.tier-toast')).toBeVisible()
    const before = getReads(); cancelElsewhere(); await page.clock.runFor(1200)
    await expect.poll(getReads).toBeGreaterThan(before)
    await expect(page.getByText('Change to Fast was cancelled.', { exact: true })).toBeVisible()
    await expect(page.locator('.tier-toast')).toHaveCount(0)
    await expect(panel.getByRole('alert')).toHaveCount(0)
  })
}
test('a rejection can be dismissed without hiding a later rejection', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { reject } = await setup(page)
  await page.goto(`/agents/${id}`)
  const panel = page.getByRole('region', { name: 'Service tier', exact: true })
  await expect(panel.locator('.source')).toContainText('fixture-adapter')
  await glyph(page).focus(); await glyph(page).press('ArrowRight'); await expect(page.locator('.tier-toast')).toBeVisible()
  reject(); await expect(panel.getByRole('alert')).toContainText('vendor rejected')
  await expectStableControls({ controls: { change: panel.getByRole('button', { name: 'Change tier', exact: true }), row: row(page) }, interactions: [{ name: 'dismiss rejection', run: async () => {
    await panel.getByRole('button', { name: 'Dismiss', exact: true }).click(); await expect(panel.getByRole('alert')).toHaveCount(0)
  } }] })
  // A fresh read in the same viewer must keep the dismissal.
  await page.getByRole('button', { name: 'Change tier', exact: true }).click()
  await expect(picker(page)).toBeVisible(); await picker(page).getByRole('button', { name: /Cancel/ }).click()
  await expect(panel.getByRole('alert')).toHaveCount(0)
  await glyph(page).focus(); await glyph(page).press('ArrowRight'); await expect(page.locator('.tier-toast')).toBeVisible()
  reject(); await expect(panel.getByRole('alert')).toContainText('vendor rejected')
})
test('starting the next change retires the previous panel rejection', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { reject } = await setup(page)
  await page.goto(`/agents/${id}`)
  const panel = page.getByRole('region', { name: 'Service tier', exact: true })
  await expect(panel.locator('.source')).toContainText('fixture-adapter')
  await glyph(page).focus(); await glyph(page).press('ArrowRight'); await expect(page.locator('.tier-toast')).toBeVisible()
  reject(); await expect(panel.getByRole('alert')).toContainText('vendor rejected')
  await glyph(page).focus(); await glyph(page).press('ArrowRight'); await expect(page.locator('.tier-toast')).toBeVisible()
  await expect(panel).toContainText('Switching to Fast')
  await expect(panel.getByRole('alert')).toHaveCount(0)
})
test('heartbeat-only list refreshes do not reload the tier panel', async ({ page }) => {
  await page.clock.install()
  const { data, getReads } = await setup(page)
  await page.goto(`/agents/${id}`)
  await expect(page.locator('.service-tier .source')).toContainText('fixture-adapter')
  const initial = getReads(); expect(initial).toBeGreaterThan(0)
  Object.assign(data.sessions[0]!, { heartbeat_at: new Date(Date.now() + 20_000).toISOString(), display_label: 'Heartbeat refreshed fixture' })
  await page.clock.runFor(20_000)
  await expect(row(page)).toContainText('Heartbeat refreshed fixture')
  // A render-frame barrier lets the watcher and its network dispatch complete.
  await page.clock.resume()
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => resolve())))
  expect(getReads()).toBe(initial)
})

for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`${width} ${theme}: options, actions and phone frame hold still`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 }); await page.emulateMedia({ colorScheme: theme })
    const errors: string[] = []; page.on('pageerror', e => errors.push(e.message))
    await setup(page, { active: width === 390 ? 'fast' : 'default' })
    await page.goto('/agents')
    if (width === 390) {
      const host = row(page).locator('.host-badge'), tier = row(page).locator('.phone-tier [data-tier]')
      await expect(host).toBeVisible()
      const hb = (await host.boundingBox())!, tb = (await tier.boundingBox())!
      expect(tb.x + tb.width).toBeLessThanOrEqual(hb.x + .5)
      await tier.click()
    }
    else await glyph(page).click()
    await expect(picker(page).getByRole('radio', { name: /Fastest:/ })).toHaveAttribute('aria-disabled', 'false')
    const controls = { choices: picker(page).getByRole('radiogroup'), fast: picker(page).locator('[data-option=fast]'), default: picker(page).locator('[data-option=default]'), fastest: picker(page).locator('[data-option=fastest]'), cancel: picker(page).getByRole('button', { name: /Cancel/ }), confirm: picker(page).locator('.tp-go'), ...(width === 390 ? { frame: picker(page) } : {}) }
    await expectStableControls({ controls, scrollAreas: { body: picker(page), notes: picker(page).locator('.tp-notes') }, interactions: ['fastest', 'default', 'fast'].map(t => ({ name: `choose ${t}`, run: async () => { await picker(page).locator(`[data-option=${t}]`).click(); await expect(picker(page).locator(`[data-option=${t}]`)).toHaveAttribute('aria-checked', 'true') } })) })
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
    expect(errors).toEqual([])
    if (process.env.SERVICE_TIER_SHOTS) { await mkdir(process.env.SERVICE_TIER_SHOTS, { recursive: true }); await page.screenshot({ path: `${process.env.SERVICE_TIER_SHOTS}/${width}-${theme}-picker.png` }) }
    await picker(page).getByRole('button', { name: /Cancel/ }).click()
    await expect(picker(page)).toHaveCount(0)
  })
}

test('offered-only chevrons share one centre; hover, step and pending Undo hold boxes', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { writes } = await setup(page)
  await page.goto('/agents')
  await expect(glyph(page)).toBeVisible()
  const centres = await page.locator('.c-tier .tier-glyph').evaluateAll(elements => elements.map(el => { const r = el.getBoundingClientRect(); return r.x + r.width / 2 }))
  expect(centres).toHaveLength(3); expect(Math.max(...centres) - Math.min(...centres)).toBeLessThanOrEqual(.5)
  await expectStableControls({ controls: { row: row(page), cell: row(page).locator('.c-tier'), glyph: glyph(page) }, interactions: [
    { name: 'hover', run: () => row(page).locator('.c-tier').hover() },
    { name: 'step up', run: async () => { await glyph(page).focus(); await glyph(page).press('ArrowRight'); await expect(page.locator('.tier-toast')).toBeVisible() } },
    { name: 'Undo pending', run: async () => { await page.locator('.tier-toast').getByRole('button', { name: 'Undo' }).click(); await expect(page.locator('.tier-toast')).toHaveCount(0) } },
  ] })
  expect(writes).toHaveLength(2); expect(writes[0]).toMatchObject({ tier: 'fast', expected_revision: 1, expected_ownership: ownership }); expect(writes[1]).toMatchObject({ tier: 'default', expected_revision: 2, expected_ownership: ownership })
})
test('Undo after daemon confirmation queues a reverse change; modified arrows stay native', async ({ page }) => {
  const { writes, confirm } = await setup(page)
  await page.goto('/agents'); await expect(glyph(page)).toBeVisible()
  await glyph(page).focus(); await glyph(page).press('Control+ArrowRight'); expect(writes).toHaveLength(0)
  await glyph(page).press('ArrowRight'); await expect(page.locator('.tier-toast')).toBeVisible()
  confirm()
  await page.locator('.tier-toast').getByRole('button', { name: 'Undo' }).click()
  await expect.poll(() => writes.length).toBe(2)
  expect(writes[1]).toMatchObject({ tier: 'default', expected_revision: 3 })
})
test('Enter opens and confirms, Escape cancels and restores the glyph', async ({ page }) => {
  const { writes } = await setup(page)
  await page.goto('/agents'); await glyph(page).focus(); await glyph(page).press('Enter')
  await expect(picker(page).locator('[data-option=fast]')).toHaveAttribute('aria-disabled', 'false')
  await expect(picker(page).getByRole('radio', { name: /Default:/ })).toBeFocused()
  await page.keyboard.press('ArrowRight'); await expect(picker(page).locator('[data-option=fast]')).toHaveAttribute('aria-checked', 'true')
  await page.keyboard.press('Escape'); await expect(glyph(page)).toBeFocused(); expect(writes).toHaveLength(0)
  await glyph(page).press('Space'); await picker(page).locator('[data-option=fast]').click(); await page.keyboard.press('Enter')
  await expect(picker(page)).toHaveCount(0); expect(writes[0]?.tier).toBe('fast')
})
test('no cited price disables paid tiers and hides their chevrons', async ({ page }) => {
  const { writes } = await setup(page, { unpriced: true })
  await page.goto('/agents'); await expect(glyph(page).locator('path')).toHaveCount(1)
  await glyph(page).click(); await expect(picker(page).getByRole('radio', { name: /Fast: not offered: no published price/ })).toHaveAttribute('aria-disabled', 'true')
  await expect(picker(page).locator('[data-option=default]')).toHaveAttribute('aria-disabled', 'false')
  await picker(page).locator('[data-option=default]').focus(); await page.keyboard.press('ArrowRight')
  await expect(picker(page).locator('[data-option=default]')).toHaveAttribute('aria-checked', 'true'); expect(writes).toHaveLength(0)
})
for (const kind of ['unmanaged', 'ended', 'denied'] as const) test(`${kind} sessions remain read-only`, async ({ page }) => {
  const { writes } = await setup(page, { [kind]: true })
  await page.goto(`/agents/${id}`)
  await expect(page.getByRole('region', { name: 'Service tier', exact: true })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Service tier', exact: true }).getByRole('button', { name: 'Change tier', exact: true })).toHaveCount(0)
  expect(writes).toHaveLength(0)
})
for (const width of [1440, 390]) test(`${width}: the panel request points to stable desk actions for approval`, async ({ page }) => {
  await page.setViewportSize({ width, height: 900 })
  const { writes } = await setup(page, { request: true })
  await page.goto(`/agents/${id}`)
  const request = page.locator('.tier-request')
  await expect(request).toContainText('QA waits on this screen')
  await expect(request.getByRole('link', { name: 'Review in Decision Desk' })).toHaveAttribute('href', '/decision-desk?item=t:request-fixture')
  await request.getByRole('link', { name: 'Review in Decision Desk' }).click()
  await expect(page.getByTestId('choice-0')).toBeEnabled()
  await expectStableControls({ controls: { approve: page.getByTestId('choice-0'), decline: page.getByTestId('choice-1'), decide: page.getByTestId('desk-decide') }, interactions: [{ name: 'choose the protected approval', run: async () => { await page.getByTestId('choice-0').click() } }, { name: 'approve through the native desk adapter', run: async () => { await page.getByTestId('desk-decide').click(); await expect(page.getByTestId('desk-announcement')).toContainText('application waits for the next safe point') } }] })
  expect(writes[0]).toMatchObject({ decision: 'approve', tier: 'fast', expected_ownership: ownership })
})
test('the panel request can be declined in the desk without a tier change', async ({ page }) => {
  const { writes, getState } = await setup(page, { request: true })
  await page.goto(`/agents/${id}`); await page.locator('.tier-request').getByRole('link', { name: 'Review in Decision Desk' }).click()
  await page.getByTestId('choice-1').click(); await page.getByTestId('desk-decide').click()
  await expect.poll(() => getState().requests[0]?.state).toBe('declined'); expect(getState().active_tier).toBe('default'); expect(writes[0]?.decision).toBe('decline')
})
test('a rejected write reports the error and offers no false-success Undo', async ({ page }) => {
  await setup(page, { fail: true }); await page.goto('/agents')
  await glyph(page).focus(); await glyph(page).press('ArrowRight')
  await expect(page.getByText('Tier outcome unconfirmed: process ownership changed; refresh', { exact: true })).toBeVisible()
  await expect(page.locator('.tier-toast')).toHaveCount(0)
})

for (const width of [320, 390]) test(`${width}: agent request labels keep phone footer controls still`, async ({ page }) => {
  await page.setViewportSize({ width, height: 900 })
  await setup(page, { agent: true })
  await page.goto(`/agents/${id}`)
  await page.getByRole('button', { name: 'Ask for a tier', exact: true }).click()
  await expect(picker(page).locator('[data-option=fast]')).toHaveAttribute('aria-disabled', 'false')
  await picker(page).getByRole('textbox', { name: 'Why' }).fill('QA waits on this screen')
  await expectStableControls({
    controls: {
      frame: picker(page), choices: picker(page).getByRole('radiogroup'),
      default: picker(page).locator('[data-option=default]'), fast: picker(page).locator('[data-option=fast]'), fastest: picker(page).locator('[data-option=fastest]'),
      cancel: picker(page).getByRole('button', { name: /Cancel/ }), confirm: picker(page).locator('.tp-go'),
    },
    scrollAreas: { body: picker(page), notes: picker(page).locator('.tp-notes') },
    interactions: ['fastest', 'fast', 'default'].map(t => ({ name: `ask for ${t}`, run: async () => {
      await picker(page).locator(`[data-option=${t}]`).click()
      await expect(picker(page).locator(`[data-option=${t}]`)).toHaveAttribute('aria-checked', 'true')
    } })),
  })
})

test('an agent asks for its own tier; field shortcuts and the active tier stay intact', async ({ page }) => {
  const { writes, getState } = await setup(page, { agent: true })
  await page.goto(`/agents/${id}`)
  await page.getByRole('button', { name: 'Ask for a tier', exact: true }).click()
  await expect(picker(page).locator('[data-option=fast]')).toHaveAttribute('aria-disabled', 'false')
  await picker(page).locator('[data-option=fast]').click()
  const reason = picker(page).getByRole('textbox', { name: 'Why' })
  const mac = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform))
  await reason.fill('replace this text')
  await reason.press(mac ? 'Meta+a' : 'Control+a')
  await reason.pressSequentially('QA waits on this screen')
  await expect(reason).toHaveValue('QA waits on this screen')
  await reason.press('Enter'); expect(writes).toHaveLength(0)
  await reason.press('Escape'); await expect(picker(page)).toBeVisible()
  await reason.focus()
  await reason.press(mac ? 'Meta+Enter' : 'Control+Enter')
  await expect(picker(page)).toHaveCount(0)
  await expect(page.locator('.tier-request')).toContainText('You asked for Fast')
  await expect(page.locator('.tier-request')).toContainText('Waiting for a person')
  expect(writes[0]).toMatchObject({ tier: 'fast', reason: 'QA waits on this screen' })
  expect(getState().active_tier).toBe('default')
})

for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`${width} ${theme}: frozen cost, tier history and sourced estimates keep controls stable`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 }); await page.emulateMedia({ colorScheme: theme })
    const errors: string[] = []; page.on('pageerror', e => errors.push(e.message))
    await setup(page, { evidence: true })
    await page.goto(`/agents/${id}`)
    const panel = page.getByRole('region', { name: 'Service tier', exact: true })
    await expect(panel.locator('.estimate-source')).toContainText('1 run · frozen price version 7')
    await expect(page.locator('.tier-cost')).toHaveText('Fast ×2 · $2.40 at Default')
    await expect(page.getByRole('list', { name: 'Recent session changes' })).toContainText('Tier Default to Fast · by Markus, asked by fixture-agent')
    await panel.getByRole('button', { name: 'Change tier', exact: true }).click()
    const dialog = picker(page)
    await expect(dialog.locator('[data-option=fast] .tp-estimate')).toContainText('≈ $4.80')
    await expect(dialog.locator('[data-option=fast] .tp-estimate')).toContainText('≈ 10 min')
    await expect(dialog.locator('[data-option=default] .tp-estimate')).toContainText('$2.40')
    await expect(dialog.locator('[data-option=default] .tp-estimate')).toContainText('15 min')
    await expect(dialog.locator('[data-option=default] .tp-estimate')).not.toContainText('≈')
    const controls = { choices: dialog.getByRole('radiogroup'), fast: dialog.locator('[data-option=fast]'), default: dialog.locator('[data-option=default]'), fastest: dialog.locator('[data-option=fastest]'), cancel: dialog.getByRole('button', { name: /Cancel/ }), confirm: dialog.locator('.tp-go'), ...(width === 390 ? { frame: dialog } : {}) }
    await expectStableControls({ controls, scrollAreas: { body: dialog, notes: dialog.locator('.tp-notes') }, interactions: ['fast', 'fastest', 'default'].map(t => ({ name: `estimate choice ${t}`, run: async () => { await dialog.locator(`[data-option=${t}]`).click(); await expect(dialog.locator(`[data-option=${t}]`)).toHaveAttribute('aria-checked', 'true') } })) })
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
    expect(errors).toEqual([])
  })
}
test('zero-run estimates are explicit in the panel and picker', async ({ page }) => {
  await setup(page); await page.goto(`/agents/${id}`)
  const panel = page.getByRole('region', { name: 'Service tier', exact: true })
  await expect(panel.locator('.estimate-source')).toContainText('0 runs')
  await expect(panel.locator('.compare-estimate').first()).toContainText('no estimate yet')
  await panel.getByRole('button', { name: 'Change tier', exact: true }).click()
  await expect(picker(page).locator('[data-option=fast] .tp-estimate')).toContainText('no estimate yet')
})

for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`${width} ${theme}: delayed vendor evidence keeps picker and panel actions put`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 }); await page.emulateMedia({ colorScheme: theme })
    const { releaseTierRead } = await setup(page, { evidence: true, tierRead: 'delayed' })
    await page.goto(`/agents/${id}`)
    const panel = page.getByRole('region', { name: 'Service tier', exact: true })
    const change = panel.getByRole('button', { name: 'Change tier', exact: true })
    await change.click()
    const dialog = picker(page)
    await expect(dialog).toBeVisible()
    await expect(dialog.locator('[data-option=fast] .tp-text')).toContainText('Checking vendor report')
    await expect(dialog.locator('[data-option=fast] .tp-estimate')).toHaveText('—')
    await expect(dialog.locator('.estimate-source')).toHaveCount(0)
    await expect(panel.locator('.estimate-source')).toHaveCount(0)
    await expectStableControls({ controls: {
      choices: dialog.getByRole('radiogroup'), fast: dialog.locator('[data-option=fast]'),
      cancel: dialog.getByRole('button', { name: /Cancel/ }), confirm: dialog.locator('.tp-go'),
      ...(width === 390 ? { frame: dialog } : { panelChange: change }),
    }, scrollAreas: { dialog, panelBody: page.locator('.session-panel > .scroll') }, interactions: [{ name: 'resolve delayed evidence', run: async () => {
      releaseTierRead()
      await expect(dialog.locator('[data-option=fast] .tp-estimate')).toContainText('≈ $4.80')
      await expect(page.locator('.tier-cost')).toHaveText('Fast ×2 · $2.40 at Default')
    } }] })
    for (const option of ['default', 'fast', 'fastest']) {
      const small = dialog.locator(`[data-option=${option}] .tp-estimate small`)
      expect(await small.evaluate(el => el.getBoundingClientRect().height)).toBeLessThanOrEqual(12)
    }
  })
}
test('failed evidence reads show placeholders, never a zero-run or vendor conclusion', async ({ page }) => {
  await setup(page, { tierRead: 'error' }); await page.goto(`/agents/${id}`)
  const panel = page.getByRole('region', { name: 'Service tier', exact: true })
  await expect(panel.getByRole('alert')).toContainText('Tier evidence unavailable')
  await expect(panel.locator('.compare-estimate').first()).toHaveText('—')
  await expect(panel.locator('.estimate-source')).toHaveCount(0)
  await panel.getByRole('button', { name: 'Change tier', exact: true }).click()
  const dialog = picker(page)
  await expect(dialog.getByRole('alert')).toContainText('Tier evidence unavailable')
  await expect(dialog.locator('[data-option=fast] .tp-estimate')).toHaveText('—')
  await expect(dialog.locator('[data-option=fast] .tp-text')).not.toContainText('not offered')
  await expect(dialog.locator('.estimate-source')).toHaveCount(0)
})

test('live evidence ignores sibling sessions and batches selected telemetry over five seconds', async ({ page }) => {
  const at = new Date()
  await page.clock.install({ time: at })
  await page.clock.pauseAt(new Date(at.getTime() + 1000))
  await page.addInitScript(() => {
    class Stream extends EventTarget {
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      receive = (event: Event) => {
        const { name, after } = (event as CustomEvent<{ name: string; after: unknown }>).detail
        this.dispatchEvent(new MessageEvent(name, { data: JSON.stringify({ after }) }))
      }
      constructor() { super(); window.addEventListener('test:tier-evidence', this.receive) }
      close() { window.removeEventListener('test:tier-evidence', this.receive) }
    }
    Object.assign(window, { EventSource: Stream })
  })
  const { data, getReads } = await setup(page)
  await page.goto(`/agents/${id}`)
  await expect(page.getByRole('region', { name: 'Service tier', exact: true }).locator('.estimate-source')).toContainText('0 runs')
  const before = getReads()
  const emit = (name: string, after: unknown) => page.evaluate(({ name, after }) => window.dispatchEvent(new CustomEvent('test:tier-evidence', { detail: { name, after } })), { name, after })
  const batch = () => Promise.all([
    page.waitForResponse(response => new URL(response.url()).pathname === '/api/harness-sessions'),
    page.clock.runFor(400),
  ])
  await emit('run.telemetry', { run_id: 'sibling-run' }); await batch()
  await emit('harness.usage_reported', { session_id: 'sibling-session' }); await batch()
  expect(getReads()).toBe(before)
  await emit('run.telemetry', { run_id: data.sessions[0]!.run_id })
  await expect.poll(getReads).toBe(before + 1)
  for (let i = 0; i < 12; i++) {
    await emit('harness.usage_reported', { session_id: id }); await batch()
  }
  expect(getReads()).toBe(before + 1)
  await page.clock.runFor(199); expect(getReads()).toBe(before + 1)
  await page.clock.runFor(1); await expect.poll(getReads).toBe(before + 2)
  await page.clock.runFor(5000); expect(getReads()).toBe(before + 2)
})
