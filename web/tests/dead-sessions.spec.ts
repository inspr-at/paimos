// SPDX-License-Identifier: AGPL-3.0-only
// AEON-291: dead sessions clean themselves up; the session menu offers only what works.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'

const world: AgentWorld = { me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {
  'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
  'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' }, 'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'AEON-275', title: 'Cursor worker for the release notes' },
} }
const minutes = (n: number) => new Date(Date.now() - n * 60_000).toISOString()
const shots = process.env.DEAD_SESSIONS_SCREENSHOT_DIR
const row = (page: Page, id: string) => page.locator(`[data-row="s:${id}"]`)

async function setup(page: Page) {
  await mockWork(page, fixtures(), { admin: true })
  const data = agentData(world)
  await mockAgents(page, data)
  const [lead, live, , , silent, , , old] = data.sessions
  // The AEON-275 Cursor worker the launcher never stopped: the server closed it.
  const lost = data.sessions[8]!
  Object.assign(lost, { display_label: 'cursor-275', harness: 'cursor', management_mode: 'unmanaged', run_id: null, has_problem: false, advertised_capabilities: ['inbox', 'status'], ticket_node_id: 'n-6', ticket: { id: 'n-6', key: 'AEON-275', title: 'Cursor worker for the release notes' }, phase: 'stopped', stopped_at: minutes(20), stop_reason: 'heartbeat_lost', heartbeat_at: minutes(35) })
  // Unmanaged and silent for twelve minutes: No heartbeat, not yet swept.
  Object.assign(silent!, { display_label: 'grok-quiet', heartbeat_at: minutes(12) })
  // Unmanaged and fresh.
  Object.assign(live!, { display_label: 'codex-outside', management_mode: 'unmanaged', advertised_capabilities: ['inbox', 'status'] })
  // Managed with settings.
  Object.assign(lead!, { display_label: 'claude-lead', advertised_capabilities: ['inbox', 'status', 'steer', 'interrupt', 'stop', 'managed_control_v1', 'rename', 'model', 'effort'],
    process_ownership: { daemon_id: 'imac0-daemon', generation: 'a'.repeat(32), process_id: 'b'.repeat(32), root_pid: 4242, group_id: 4242, started_at: minutes(70) } })
  // The daemon confirms ownership with every heartbeat; keep it fresh on each read.
  const touch = () => { lead!.process_observed_at = new Date().toISOString() }
  touch()
  // Ended more than a day ago: only History shows it.
  Object.assign(old!, { display_label: 'old-run', stopped_at: minutes(26 * 60), heartbeat_at: minutes(26 * 60 + 5) })
  const views: string[] = []
  await page.route(/\/api\/harness-sessions\?/, async route => {
    const q = new URL(route.request().url()).searchParams
    if (q.get('ticket') || q.get('agent') || q.get('project')) return route.fallback()
    views.push(q.get('view') ?? '')
    touch()
    const cutoff = Date.now() - 24 * 3_600_000
    const ended = (s: Record<string, unknown>) => Math.max(Date.parse(String(s.stopped_at ?? '')) || 0, Date.parse(String(s.archived_at ?? '')) || 0)
    const items = data.sessions.filter(s => q.get('view') !== 'current' || !(ended(s) && ended(s) < cutoff))
    return route.fulfill({ json: { items, next_cursor: null } })
  })
  let event = 900
  const undone: number[] = []
  const restore = new Map<number, () => Record<string, unknown>>()
  await page.route('**/harness-sessions/*/remove', async route => {
    const id = /harness-sessions\/([^/]+)\/remove/.exec(route.request().url())![1]
    const s = data.sessions.find(x => x.id === id)!
    const before = { ...s }
    Object.assign(s, { archived_at: new Date().toISOString(), recovery_process_state: 'unknown', phase: 'stopped', stopped_at: s.stopped_at ?? new Date().toISOString(), stop_reason: s.stop_reason ?? 'removed_process_unknown', revision: Number(s.revision) + 1 })
    const eventId = ++event
    restore.set(eventId, () => Object.assign(s, { ...before, archived_at: null, recovery_process_state: null, revision: Number(s.revision) + 1 }))
    await route.fulfill({ json: { session: s, message: 'Record removed; process not stopped by removal.', processes_signalled: false, process_state: 'unknown', event_id: eventId } })
  })
  await page.route('**/api/events/*/undo', async route => {
    const id = Number(/events\/(\d+)\/undo/.exec(route.request().url())![1])
    undone.push(id)
    const after = restore.get(id)!()
    await route.fulfill({ status: 201, json: { id: id + 1000, type: 'harness.restored', undo_of: id, after } })
  })
  const managed: Record<string, unknown>[] = [], legacy: string[] = []
  await page.route('**/harness-sessions/*/managed-controls', async route => {
    const body = route.request().postDataJSON() as Record<string, unknown>
    managed.push(body)
    await route.fulfill({ status: 201, json: { id: body.request_id, session_id: lead!.id, kind: body.kind, state: 'pending', sequence: 1, outcome: null, reason: null, created_at: new Date().toISOString(), claimed_at: null, completed_at: null, expires_at: new Date(Date.now() + 45_000).toISOString() } })
  })
  await page.route(/\/harness-sessions\/[^/]+\/controls\/(interrupt|stop)$/, async route => { legacy.push(route.request().url()); await route.fulfill({ status: 409, json: { error: 'use managed controls' } }) })
  return { data, lost, silent: silent!, live: live!, lead: lead!, old: old!, views, undone, managed, legacy }
}

test('Lost contact is an ended row with a bin; one click removes, Undo brings it back', async ({ page }) => {
  const errors = watchErrors(page)
  const { lost, undone } = await setup(page)
  await page.goto('/agents')
  // Not a Problem under Needs attention: it sits with the ended sessions.
  await expect(page.locator('.group-row.attention')).not.toContainText('cursor-275')
  await page.getByRole('button', { name: /^Stopped/ }).click()
  await expect(row(page, lost.id)).toContainText('Lost contact')
  const bin = row(page, lost.id).getByRole('button', { name: 'Remove cursor-275' })
  await page.mouse.move(0, 0)
  await expect(bin).toHaveCSS('opacity', '1')
  await bin.click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(row(page, lost.id)).toHaveCount(0)
  const toast = page.getByText('Removed cursor-275', { exact: true })
  await expect(toast).toBeVisible()
  await page.getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(row(page, lost.id)).toBeVisible()
  await expect(row(page, lost.id)).toContainText('Lost contact')
  expect(undone).toHaveLength(1)
  expect(errors).toEqual([])
})

test('No heartbeat rows carry a bin; live rows keep the confirm', async ({ page }) => {
  const { silent, live } = await setup(page)
  await page.goto('/agents')
  await expect(row(page, silent.id)).toContainText('No heartbeat')
  await row(page, silent.id).getByRole('button', { name: 'Remove grok-quiet' }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(row(page, silent.id)).toHaveCount(0)
  await expect(row(page, live.id).getByRole('button', { name: /^Remove/ })).toHaveCount(0)
  await row(page, live.id).hover()
  await row(page, live.id).getByRole('button', { name: 'Actions for codex-outside' }).click()
  await page.getByRole('menuitem', { name: 'Remove…' }).click()
  await expect(page.getByRole('dialog', { name: 'Remove codex-outside?' })).toContainText('Its process keeps running')
})

test('an unmanaged session menu offers only what works, with one quiet line', async ({ page }) => {
  const { live } = await setup(page)
  await page.goto('/agents')
  await row(page, live.id).hover()
  await row(page, live.id).getByRole('button', { name: 'Actions for codex-outside' }).click()
  const menu = page.getByRole('menu', { name: 'Actions for codex-outside' })
  await expect(menu.getByRole('menuitem')).toHaveText([/^Open PHAROS-12/, 'Copy session id', 'Remove…'])
  await expect(menu).toContainText(/Runs outside aeon — stop it in its terminal/i)
  await expect(menu.getByRole('menuitem', { name: /Interrupt|Stop/ })).toHaveCount(0)
  await expect(page.locator('[role="menuitem"][aria-disabled="true"], [role="menuitem"]:disabled')).toHaveCount(0)
  await menu.getByRole('menuitem', { name: /^Open PHAROS-12/ }).click()
  await expect(page).toHaveURL(/\/PHAROS-12$/)
})

test('a managed session menu offers Interrupt, Stop, settings and Remove', async ({ page }) => {
  const { lead, managed, legacy } = await setup(page)
  await page.goto('/agents')
  await row(page, lead.id).hover()
  await row(page, lead.id).getByRole('button', { name: 'Actions for claude-lead' }).click()
  const menu = page.getByRole('menu', { name: 'Actions for claude-lead' })
  await expect(menu.getByRole('menuitem')).toHaveText([/^Interrupt/, 'Stop session…', 'Name, model, effort', /^Open PHAROS-11/, 'Copy session id', 'Remove…'])
  await expect(menu).not.toContainText('Runs outside')
  // managed_control_v1: Interrupt goes through the ownership-aware route, bound to the process generation.
  await menu.getByRole('menuitem', { name: /^Interrupt/ }).click()
  await expect.poll(() => managed.length).toBe(1)
  expect(managed[0]).toMatchObject({ kind: 'interrupt', expected_ownership: { daemon_id: 'imac0-daemon', root_pid: 4242 } })
  expect(legacy).toEqual([])
  await row(page, lead.id).hover()
  await row(page, lead.id).getByRole('button', { name: 'Actions for claude-lead' }).click()
  await page.getByRole('menu', { name: 'Actions for claude-lead' }).getByRole('menuitem', { name: 'Name, model, effort' }).click()
  await expect(page).toHaveURL(new RegExp(`/agents/${lead.id}$`))
})

test('a managed_control_v1 session without fresh ownership offers no Interrupt or Stop', async ({ page }) => {
  const { lead } = await setup(page)
  await page.route(/\/api\/harness-sessions\?/, async route => route.fallback())
  lead.process_observed_at = new Date(Date.now() - 5 * 60_000).toISOString()
  delete (lead as Record<string, unknown>).process_ownership
  await page.goto('/agents')
  await row(page, lead.id).hover()
  await row(page, lead.id).getByRole('button', { name: 'Actions for claude-lead' }).click()
  await expect(page.getByRole('menu', { name: 'Actions for claude-lead' }).getByRole('menuitem', { name: /Interrupt|Stop session/ })).toHaveCount(0)
})

test('sessions that ended over a day ago leave the list and stay in History', async ({ page }) => {
  const { old, lost, views } = await setup(page)
  await page.goto('/agents')
  await page.getByRole('button', { name: /^Stopped/ }).click()
  await expect(row(page, lost.id)).toBeVisible()
  await expect(row(page, old.id)).toHaveCount(0)
  expect(views.every(v => v === 'current')).toBe(true)
  await page.getByRole('button', { name: 'Show history: every ended or removed session' }).click()
  await expect(page.getByRole('heading', { name: 'History' })).toBeVisible()
  await expect(row(page, old.id)).toBeVisible()
  expect(views).toContain('all')
  // A link straight to it finds it in History too.
  await page.goto(`/agents/${old.id}`)
  await expect(page.getByRole('complementary', { name: 'Session details' })).toContainText('old-run')
})

test('the panel of a session outside Aeon offers no Interrupt or Stop, one quiet line instead', async ({ page }) => {
  const { live, lost } = await setup(page)
  await page.goto(`/agents/${live.id}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel).toContainText(/Runs outside aeon — stop it in its terminal/i)
  await expect(panel.getByRole('button', { name: /^(Interrupt|Stop)$/ })).toHaveCount(0)
  await expect(panel.getByRole('button', { name: 'Remove codex-outside' })).toHaveText('Remove…')
  await page.goto(`/agents/${lost.id}`)
  await expect(panel).toContainText('Lost contact')
  await expect(panel).not.toContainText('Runs outside')
  await panel.getByRole('button', { name: 'Remove cursor-275' }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByText('Removed cursor-275', { exact: true })).toBeVisible()
})

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`dead sessions visual ${width} ${theme}`, async ({ page }) => {
    const { lost, live, silent } = await setup(page)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await page.goto('/agents')
    await page.evaluate(t => { document.documentElement.dataset.theme = t }, theme)
    await page.getByRole('button', { name: /^Stopped/ }).click()
    await expect(row(page, lost.id)).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    if (width === 390) {
      for (const button of await row(page, silent.id).locator('.c-actions button').all()) {
        const box = (await button.boundingBox())!
        expect(box.width).toBeGreaterThanOrEqual(44)
        expect(box.height).toBeGreaterThanOrEqual(44)
      }
    }
    if (shots) {
      await row(page, lost.id).scrollIntoViewIfNeeded()
      await page.mouse.move(0, 0)
      await page.screenshot({ path: `${shots}/list-${width}-${theme}.png` })
    }
    await row(page, live.id).scrollIntoViewIfNeeded()
    await row(page, live.id).hover()
    await row(page, live.id).getByRole('button', { name: 'Actions for codex-outside' }).click()
    await expect(page.getByRole('menu', { name: 'Actions for codex-outside' })).toBeVisible()
    if (width === 390) for (const item of await page.getByRole('menuitem').all()) expect((await item.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    if (shots) await page.screenshot({ path: `${shots}/menu-unmanaged-${width}-${theme}.png` })
    await page.keyboard.press('Escape')
    await row(page, lost.id).getByRole('button', { name: 'Remove cursor-275' }).click()
    await expect(page.getByRole('button', { name: 'Undo', exact: true })).toBeVisible()
    if (shots) await page.screenshot({ path: `${shots}/undo-${width}-${theme}.png` })
    await page.getByRole('button', { name: 'Show history: every ended or removed session' }).click()
    await expect(page.getByRole('heading', { name: 'History' })).toBeVisible()
    if (shots) { await page.getByRole('heading', { name: 'History' }).scrollIntoViewIfNeeded(); await page.screenshot({ path: `${shots}/history-${width}-${theme}.png` }) }
    await page.goto(`/agents/${live.id}`)
    await page.evaluate(t => { document.documentElement.dataset.theme = t }, theme)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel).toContainText(/Runs outside aeon/i)
    if (width === 390) expect((await panel.getByRole('button', { name: 'Remove codex-outside' }).boundingBox())!.height).toBeGreaterThanOrEqual(44)
    if (shots) await page.screenshot({ path: `${shots}/panel-unmanaged-${width}-${theme}.png` })
  })
}
