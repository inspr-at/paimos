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
async function setup(page: Page, options: { unpriced?: boolean; unmanaged?: boolean; ended?: boolean; denied?: boolean; request?: boolean; fail?: boolean; active?: 'default' | 'fast'; agent?: boolean } = {}) {
  await mockWork(page, fixtures(), { admin: true, principalKind: options.agent ? 'agent' : 'person' })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  data.approvals.splice(0)
  data.sessions.splice(3)
  const reports = [{ ...report, tiers: report.tiers.map(t => options.unpriced && t.tier !== 'default' ? { ...t, offered: false, price_multiplier: null, speed_factor: null, reason: 'not offered: no published price' } : t) }]
  const active = options.active ?? 'default'
  Object.assign(data.sessions[0]!, { harness: 'codex', display_label: 'Tier fixture', model: report.model, service_tier: active, service_tier_revision: 1, service_tier_reports: reports, advertised_capabilities: ['inbox', 'status', 'stop', 'service_tier_v1'], agent_principal_id: options.agent ? me.id : data.sessions[0]!.agent_principal_id, process_ownership: ownership, process_observed_at: new Date().toISOString(), heartbeat_at: new Date().toISOString(), ...(options.unmanaged ? { management_mode: 'unmanaged' } : {}), ...(options.ended ? { phase: 'stopped', stopped_at: new Date().toISOString() } : {}) })
  for (let i = 1; i < data.sessions.length; i++) Object.assign(data.sessions[i]!, { harness: 'codex', model: report.model, service_tier: 'default', service_tier_reports: [{ ...report, tiers: report.tiers.map((t, j) => ({ ...t, offered: j <= i - 1 })) }], service_tier_revision: 1 })
  await mockAgents(page, data)
  if (options.denied) await page.route('**/api/me/permissions*', async route => {
    const effective = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') || undefined)
    effective.workspace.permissions = effective.workspace.permissions.filter(p => p !== 'harness.control')
    if (effective.project) effective.project.permissions = effective.project.permissions.filter(p => p !== 'harness.control')
    return route.fulfill({ json: effective })
  })
  let state: TierState = { session_id: id, revision: 1, active_tier: active, pending: null, read_only: !!(options.unmanaged || options.ended), read_only_reason: options.unmanaged ? 'Reported by fixture-cli; change it in its terminal.' : options.ended ? 'This session has ended' : '', reports, requests: options.request ? [{ id: 'request-fixture', session_id: id, tier: 'fast', reason: 'QA waits on this screen', state: 'pending', requested_by_principal_id: String(data.sessions[0]!.agent_principal_id), created_at: new Date().toISOString() }] : [] }
  const writes: Record<string, unknown>[] = []
  await page.route('**/api/projects/*/harness-sessions/*/tier**', async route => {
    if (route.request().method() === 'GET') return route.fulfill({ json: state })
    const body = route.request().postDataJSON(); writes.push(body)
    if (options.fail) return route.fulfill({ status: 409, json: { error: 'process ownership changed; refresh' } })
    if (route.request().url().endsWith('/ask')) {
      const request = { id: body.request_id, session_id: id, tier: body.tier, reason: body.reason, state: 'pending' as const, requested_by_principal_id: me.id, created_at: new Date().toISOString() }
      state = { ...state, requests: [request] }; return route.fulfill({ status: 201, json: request })
    }
    if (body.expected_revision !== state.revision) return route.fulfill({ status: 409, json: { error: 'revision changed' } })
    if (body.decision === 'decline') state = { ...state, requests: state.requests.map(q => ({ ...q, state: 'declined' })) }
    else if (state.pending && body.tier === state.active_tier) state = { ...state, revision: state.revision + 1, pending: null, requests: state.requests.map(q => ({ ...q, state: 'pending' })) }
    else state = { ...state, revision: state.revision + 1, pending: { id: body.request_id, session_id: id, kind: 'tier', value: body.tier, state: 'pending', outcome: null, reason: null }, requests: state.requests.map(q => ({ ...q, state: q.tier === body.tier ? 'approved' : q.state })) }
    Object.assign(data.sessions[0]!, { service_tier: state.active_tier, service_tier_revision: state.revision })
    await route.fulfill({ status: 201, json: state })
  })
  return { data, writes, confirm: () => { state = { ...state, active_tier: state.pending!.value, pending: null, revision: state.revision + 1 }; Object.assign(data.sessions[0]!, { service_tier: state.active_tier, service_tier_revision: state.revision }) }, getState: () => state }
}
const row = (page: Page) => page.locator(`[data-row="s:${id}"]`)
const glyph = (page: Page) => row(page).locator('.c-tier [data-tier]')
const picker = (page: Page) => page.getByRole('dialog', { name: 'Change tier', exact: true })

for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`${width} ${theme}: options, actions and phone frame hold still`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 }); await page.emulateMedia({ colorScheme: theme })
    const errors: string[] = []; page.on('pageerror', e => errors.push(e.message))
    await setup(page, { active: width === 390 ? 'fast' : 'default' })
    await page.goto('/agents')
    if (width === 390) await row(page).locator('.phone-tier [data-tier]').click()
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
for (const width of [1440, 390]) test(`${width}: the panel request retains its actions after approval`, async ({ page }) => {
  await page.setViewportSize({ width, height: 900 })
  const { writes } = await setup(page, { request: true })
  await page.goto(`/agents/${id}`)
  const request = page.locator('.tier-request')
  await expect(request).toContainText('QA waits on this screen')
  await expectStableControls({ controls: { approve: request.getByRole('button', { name: 'Approve Fast' }), decline: request.getByRole('button', { name: 'Decline' }) }, interactions: [{ name: 'approve', run: async () => { await request.getByRole('button', { name: 'Approve Fast' }).click(); await expect(request).toContainText('Approved · Fast') } }] })
  expect(writes[0]).toMatchObject({ decision: 'approve', tier: 'fast', expected_ownership: ownership })
})
test('the panel request can be declined without a tier change', async ({ page }) => {
  const { writes, getState } = await setup(page, { request: true })
  await page.goto(`/agents/${id}`); await page.locator('.tier-request').getByRole('button', { name: 'Decline' }).click()
  await expect(page.locator('.tier-request')).toContainText('Declined · Fast'); expect(getState().active_tier).toBe('default'); expect(writes[0]?.decision).toBe('decline')
})
test('a rejected write reports the error and offers no false-success Undo', async ({ page }) => {
  await setup(page, { fail: true }); await page.goto('/agents')
  await glyph(page).focus(); await glyph(page).press('ArrowRight')
  await expect(page.getByText('Tier outcome unconfirmed: process ownership changed; refresh', { exact: true })).toBeVisible()
  await expect(page.locator('.tier-toast')).toHaveCount(0)
})

test('an agent asks for its own tier; field shortcuts and the active tier stay intact', async ({ page }) => {
  const { writes, getState } = await setup(page, { agent: true })
  await page.goto(`/agents/${id}`)
  await page.getByRole('button', { name: 'Ask for a tier', exact: true }).click()
  await expect(picker(page).locator('[data-option=fast]')).toHaveAttribute('aria-disabled', 'false')
  await picker(page).locator('[data-option=fast]').click()
  const reason = picker(page).getByRole('textbox', { name: 'Why' })
  await reason.fill('QA waits on this screen')
  await reason.press('Enter'); expect(writes).toHaveLength(0)
  await reason.press('Control+a'); await expect(reason).toHaveValue('QA waits on this screen')
  await reason.press('Escape'); await expect(picker(page)).toBeVisible()
  await reason.focus()
  const mac = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform))
  await reason.press(mac ? 'Meta+Enter' : 'Control+Enter')
  await expect(picker(page)).toHaveCount(0)
  await expect(page.locator('.tier-request')).toContainText('You asked for Fast')
  await expect(page.locator('.tier-request')).toContainText('Waiting for a person')
  expect(writes[0]).toMatchObject({ tier: 'fast', reason: 'QA waits on this screen' })
  expect(getState().active_tier).toBe('default')
})
