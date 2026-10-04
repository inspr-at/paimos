// SPDX-License-Identifier: AGPL-3.0-only
// Part B exercises Part A's published PR #125 contract without another queue.
import { existsSync, mkdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, me, mockWork } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import { readyGaps } from '../src/lib/workQueue'
import type { QueueWireEntry } from '../src/lib/workQueue'
const shots = process.env.WORK_QUEUE_SHOTS ?? '../.agent-shots/queue'
const now = '2026-10-01T18:00:00Z'
const agent = '33333333-3333-4333-8333-333333333333', account = '44444444-4444-4444-8444-444444444444', profile = '55555555-5555-4555-8555-555555555555'
const row = (page: Page, key: string) => page.locator('tr.ticket-row:not(.ghost)').filter({ has: page.locator('.key-btn', { hasText: new RegExp(`^${key}$`) }) })
async function world(page: Page, options: { viewer?: boolean; missing?: boolean; offset?: number; relationBlocker?: boolean; failRead?: boolean; noStart?: boolean; stale?: boolean; staleMissing?: boolean; longTitle?: boolean; assigned?: boolean; live?: boolean } = {}) {
  mkdirSync(shots, { recursive: true })
  const data = fixtures()
  for (const node of data.nodes) if (node.kind_slug !== 'epic') node.fields = { ...node.fields, estimate_hours: 3, acceptance_criteria: '- [ ] Verified with evidence' }
  const n3 = data.nodes.find(n => n.id === 'n-3')!, n4 = data.nodes.find(n => n.id === 'n-4')!
  n3.state = 'open'; n3.fields.assignee = null
  n4.state = 'blocked'; n4.fields = { ...n4.fields, priority: 'high', blocker: 'PHAROS-11' }
  if (options.missing) { n4.fields.estimate_hours = null; n4.fields.acceptance_criteria = ''; n4.fields.blocker = '' }
  if (options.relationBlocker) n4.fields.blocker = ''
  const n6 = data.nodes.find(n => n.id === 'n-6')!; n6.state = 'delivered'
  data.preferences['list:p-pharos'] = { visible: ['status', 'priority', 'assignee', 'estimate', 'suggested'] }
  const staleNode = data.nodes.find(n => n.id === 'n-2')!
  if (options.stale || options.assigned || options.live) {
    staleNode.state = 'in_progress'; staleNode.fields.assignee = options.assigned ? me.id : null
    staleNode.queue_stale = !!options.stale
    if (options.staleMissing) staleNode.fields.acceptance_criteria = ''
    if (options.longTitle) staleNode.title = 'Liegengebliebene Arbeit für den nächsten freien Agenten einreihen und die Wiederaufnahme zuverlässig überprüfen'
  }
  const calls = await mockWork(page, data, { readOnly: options.viewer })
  const state = { ids: options.missing || options.relationBlocker ? ['n-3'] : ['n-4', 'n-3'], targeted: new Map<string, string>(), manual: false, failRead: !!options.failRead, failWrite: false, queueCalls: [] as { method: string; path: string; body: Record<string, unknown> | null }[] }
  const gapsFor = (node: typeof n4) => readyGaps({ fields: node.fields, state: node.state }).filter(gap => !(options.relationBlocker && node.id === 'n-4' && gap === 'blocker'))
  const entries = (): QueueWireEntry[] => state.ids.map((id, i) => {
    const node = data.nodes.find(n => n.id === id)!, targeted = state.targeted.get(id)
    return { node_id: id, key: node.key, title: node.title, state: node.state, priority: String(node.fields.priority ?? 'medium'), estimate_hours: Number(node.fields.estimate_hours), queued: {
      run_id: `run-${id}`, position: targeted ? 1 : i + 1 + (options.offset ?? 0), by: { ...me, kind: 'person' }, at: now,
      target_agent_id: targeted ?? null, expected_agent_id: agent, model_profile_id: profile,
      expected_start_at: node.state === 'blocked' || options.noStart ? null : '2026-10-01T20:00:00Z', waiting: !!targeted || node.state === 'blocked', wait_reason: targeted ? 'Waiting for agent capacity' : node.state === 'blocked' ? 'Blocked by PHAROS-11; waits until unblocked' : '',
    } }
  })
  const undoTokens = new Map<string, { run_id: string; revision: string }>()
  const snapshot = () => ({ items: entries(), count: state.ids.length, manual_order: state.manual, capacity: { queued_hours: 6, parallel_runs: 1, work_hours: 6, warning: true } })
  await page.route('**/api/**', async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname, method = req.method()
    const body = req.postData() ? req.postDataJSON() as Record<string, unknown> : null
    const json = (value: unknown, status = 200) => route.fulfill({ status, json: value })
    if (path === '/api/me/permissions') {
      const permissions = mockEffectivePermissions(options.viewer ? 'viewer' : 'admin', url.searchParams.get('project_id') ?? undefined)
      if (!options.viewer) permissions.workspace.permissions.push('run.create', 'models.read', 'account.read')
      return json(permissions)
    }
    if (path === '/api/models') return json([{ id: profile, slug: 'codex-sol-xhigh', model: 'gpt-6-sol', effort: 'xhigh', enabled: true, harness: 'codex', family: 'openai', tier: 'priority' }])
    if (path === '/api/business/principals') return json([...data.people.map(p => ({ ...p, kind: 'person' })), { id: agent, name: 'Codex builder', kind: 'agent' }])
    if (path === '/api/agent-accounts/catalog') return json({ as_of: now, role: 'build', hosts: [{ daemon_id: 'workstation', label: 'Work Mac', harnesses: [{ harness: 'codex', default_account_id: account, accounts: [{
      id: account, label: 'Workspace account', plan: 'Pro', registered_by_principal_id: agent, state: 'available', last_probe_at: now, last_probe_ok: true,
      available: false, unavailable_reasons: ['capacity'], remaining_fraction: .8, windows: [],
      models: [{ model: 'gpt-6-sol', family: 'openai', efforts: [{ effort: 'xhigh', model_profile_id: profile, version: '2' }] }], default_model_profile_id: profile,
    }] }] }] })
    if (path === '/api/releases') return json({ schema: 'inspr.release-history.v1', current: '261001180000.0.0', releases: [0, 16, 32].map((ago, i) => ({
      version: `26100118000${i}.0.0`, codename: i === 0 ? 'Cloud City' : `Earlier ${i}`, state: 'published', published_at: new Date(Date.parse(now) - ago * 3_600_000).toISOString(), tickets: i === 0 ? ['PHAROS-16'] : [], changes: [],
    })) })
    if (!path.startsWith('/api/queue')) return route.fallback()
    state.queueCalls.push({ method, path, body })
    if (method !== 'GET' && (options.viewer || state.failWrite)) return json({ error: options.viewer ? 'forbidden' : 'Queue change refused' }, options.viewer ? 403 : 409)
    if (path === '/api/queue' && method === 'GET') return state.failRead ? json({ error: 'Queue read failed' }, 503) : json(snapshot())
    if (path === '/api/queue' && method === 'POST') {
      expect(Object.keys(body!)).toEqual(body!.agent_principal_id ? ['node_id', 'agent_principal_id', 'model_profile_id', 'requested_account_id'] : ['node_id'])
      const node = data.nodes.find(n => n.id === body!.node_id)!
      const gaps = gapsFor(node)
      if (gaps.length) return json({ error: 'Not ready', code: 'queue_not_ready', readiness: { queueable: true, ready: false, missing: gaps, suggested_estimate_hours: 3 } }, 422)
      if (!state.ids.includes(node.id)) state.ids.push(node.id)
      if (body!.agent_principal_id) state.targeted.set(node.id, String(body!.agent_principal_id))
      const wasStale = node.queue_stale === true && node.state === 'in_progress'
      if (['new', 'backlog'].includes(node.state) || wasStale) { node.state = 'open'; node.queue_stale = false; node.updated_at = new Date(Date.parse(node.updated_at) + 1000).toISOString() }
      const entry = entries().find(item => item.node_id === node.id)!
      if (wasStale) { const token = { run_id: entry.queued.run_id, revision: node.updated_at }; undoTokens.set(node.id, token); return json({ ...entry, undo: token }) }
      return json(entry)
    }
    if (path === '/api/queue/reset') { state.manual = false; state.ids.sort((a, b) => a === 'n-4' ? -1 : b === 'n-4' ? 1 : a.localeCompare(b)); return json(snapshot()) }
    const match = /^\/api\/queue\/([^/]+)(?:\/(readiness|estimate|move|undo))?$/.exec(path)
    if (!match) return json({ error: 'Unknown queue endpoint' }, 404)
    const [, id, action] = match, node = data.nodes.find(n => n.id === id)!
    if (action === 'undo') {
      const token = undoTokens.get(id!)
      if (!token || body?.run_id !== token.run_id || body?.revision !== node.updated_at) return json({ error: 'Ticket changed since queueing' }, 409)
      state.ids = state.ids.filter(value => value !== id); undoTokens.delete(id!)
      node.state = 'in_progress'; node.queue_stale = true; node.updated_at = new Date(Date.parse(node.updated_at) + 1000).toISOString()
      return json({ removed: true })
    }
    if (method === 'DELETE') { state.ids = state.ids.filter(value => value !== id); state.targeted.delete(id); return json({ removed: true }) }
    if (action === 'move') {
      const from = state.ids.indexOf(id), to = Number(body!.position) - 1 - (options.offset ?? 0)
      state.ids.splice(from, 1); state.ids.splice(Math.max(0, to), 0, id); state.manual = true; return json(snapshot())
    }
    if (action === 'estimate') { node.fields.estimate_hours = body!.estimate_hours; node.updated_at = new Date(Date.parse(node.updated_at) + 1000).toISOString() }
    const missing = gapsFor(node)
    return json({ stale: node.queue_stale === true, queueable: node.state !== 'in_progress' || node.queue_stale === true, ready: !missing.length && (node.state !== 'in_progress' || node.queue_stale === true), missing, suggested_estimate_hours: 3, security_review_required: false })
  })
  await page.clock.setSystemTime(new Date(now))
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto('/p/PHAROS?sort=key&closed=1')
  await expect(row(page, 'PHAROS-13')).toBeVisible()
  await expect(page.getByRole('button', { name: options.failRead ? /Queue read failed/ : /queued\. Open the work queue/ })).toBeVisible()
  return { data, state, calls }
}
test('key copies, row opens, Queue toggles without a fake status, and q/bulk use the same run service', async ({ page }) => {
  const { state, data } = await world(page)
  await page.evaluate(() => Object.defineProperty(navigator, 'clipboard', { value: { writeText: async (value: string) => { (window as Window & { copied?: string }).copied = value } }, configurable: true }))
  await row(page, 'PHAROS-12').getByRole('button', { name: 'Copy PHAROS-12' }).click()
  await expect(page.getByText('PHAROS-12 copied', { exact: true })).toBeVisible()
  await expect(page.getByRole('complementary', { name: 'Ticket details' })).toHaveCount(0)
  await row(page, 'PHAROS-12').getByRole('button', { name: 'Queue PHAROS-12', exact: true }).click()
  await expect(row(page, 'PHAROS-12').locator('.c-status')).toContainText('Open')
  await expect(row(page, 'PHAROS-12').locator('.c-status')).toContainText('#3')
  expect(data.nodes.find(n => n.id === 'n-2')!.state).toBe('open')
  await row(page, 'PHAROS-12').locator('.title-text').click()
  const drawer = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(drawer.locator('.q-card')).toContainText('Queued')
  await page.keyboard.press('q')
  await expect(drawer.locator('.q-card')).toHaveCount(0)
  await drawer.getByRole('button', { name: 'Close ticket details' }).click()
  await expect(drawer).toHaveCount(0)
  for (const key of ['PHAROS-12', 'PHAROS-13']) { await row(page, key).hover(); await row(page, key).getByRole('checkbox', { name: `Select ${key}` }).check() }
  await page.keyboard.press('q')
  await expect.poll(() => state.ids.includes('n-2')).toBe(true)
  expect(state.queueCalls.filter(call => call.method === 'POST' && call.path === '/api/queue').length).toBe(2)
})
test('keyboard and drag moves persist, use workspace positions, retain focus, reset and filter membership', async ({ page }) => {
  const { state, calls } = await world(page, { offset: 4 })
  await page.getByRole('button', { name: /queued\. Open the work queue/ }).click()
  const panel = page.getByRole('dialog', { name: 'Work queue', exact: true })
  await expect(panel.getByRole('button', { name: 'Move PHAROS-14 to the top' })).toBeDisabled()
  await panel.getByRole('button', { name: 'Move PHAROS-13 to the top' }).click()
  expect(state.queueCalls.filter(call => call.path.endsWith('/move')).at(-1)?.body).toEqual({ position: 5 })
  await expect(panel.getByRole('button', { name: 'Move PHAROS-13 to the top' })).toBeDisabled()
  await panel.getByRole('button', { name: 'Reset shared queue across projects' }).click()
  await panel.getByRole('button', { name: /^Number 6: PHAROS-13/ }).focus()
  await page.keyboard.press('Alt+ArrowUp')
  await expect(panel.getByRole('note')).toContainText('Manual order')
  expect(state.queueCalls.find(call => call.path.endsWith('/move'))?.body).toEqual({ position: 5 })
  await expect(panel.getByRole('button', { name: /^Number 5: PHAROS-13/ })).toBeFocused()
  await panel.getByRole('button', { name: 'Reset shared queue across projects' }).click()
  await expect(panel.getByRole('note')).toHaveCount(0)
  await panel.locator('[data-ticket="n-3"]').dragTo(panel.locator('[data-ticket="n-4"]'), { targetPosition: { x: 120, y: 2 } })
  await expect.poll(() => state.ids[0]).toBe('n-3')
  await panel.getByRole('button', { name: 'Show queued in the list' }).click()
  await expect(page).toHaveURL(/status=queued/)
  await expect(page.locator('tr.ticket-row:not(.ghost)')).toHaveCount(2)
  expect(calls.filter(call => call.path === '/api/nodes').every(call => !call.query.get('state')?.includes('queued'))).toBe(true)
  await row(page, 'PHAROS-13').locator('.title-text').click()
  const drawer = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(drawer.getByRole('button', { name: 'Move to top', exact: true })).toBeDisabled()
  await drawer.getByRole('button', { name: 'Close ticket details' }).click()
  await row(page, 'PHAROS-14').locator('.title-text').click()
  await drawer.getByRole('button', { name: 'Move to top', exact: true }).click()
  expect(state.queueCalls.filter(call => call.path.endsWith('/move')).at(-1)?.body).toEqual({ position: 5 })
  await expect(drawer.getByRole('button', { name: 'Move to top', exact: true })).toBeDisabled()
})
test('not-ready fixes apply server estimate and explicitly save criteria/blocker without erasing other fields', async ({ page }) => {
  const { data, state } = await world(page, { missing: true })
  await row(page, 'PHAROS-14').hover()
  await row(page, 'PHAROS-14').getByRole('button', { name: /Queue PHAROS-14/ }).click()
  const panel = page.getByRole('dialog', { name: /PHAROS-14: what is missing/ })
  await expect(panel.getByRole('button', { name: 'Queue', exact: true })).toBeDisabled()
  await expect(panel.getByRole('button', { name: 'Apply ~3 h' })).toBeVisible()
  await expect(panel.getByRole('status')).toHaveText('Still missing: estimate, acceptance criteria, a named blocker.')
  for (const theme of ['light', 'dark']) { await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme); await page.screenshot({ path: join(shots, `not-ready-${theme}.png`) }) }
  await panel.getByRole('button', { name: 'Apply ~3 h' }).click()
  await expect(panel.getByRole('status')).toHaveText('Still missing: acceptance criteria, a named blocker.')
  await panel.getByRole('button', { name: 'Draft criteria' }).click()
  await panel.getByRole('textbox', { name: 'Draft acceptance criteria' }).fill('- [ ] Verified in both themes')
  await panel.getByRole('button', { name: 'Save criteria' }).click()
  await panel.getByRole('button', { name: 'Name it' }).click()
  await panel.getByRole('textbox', { name: 'Name the blocker' }).fill('PHAROS-11')
  await panel.getByRole('button', { name: 'Save blocker' }).click()
  await expect(panel.getByRole('button', { name: 'Queue', exact: true })).toBeEnabled()
  await expect(panel.getByRole('status')).toHaveText('Queued work meets the definition of ready.')
  await panel.getByRole('button', { name: 'Queue', exact: true }).click()
  await expect.poll(() => state.ids.includes('n-4')).toBe(true)
  const node = data.nodes.find(n => n.id === 'n-4')!
  expect(node.state).toBe('blocked'); expect(node.fields.priority).toBe('high'); expect(node.fields.estimate_hours).toBe(3)
})
test('advanced assignment uses only granted profile/account, busy targets stay in their own nondraggable line', async ({ page }) => {
  const { state } = await world(page)
  await row(page, 'PHAROS-12').getByRole('button', { name: /Change assignee/ }).click()
  const menu = page.getByRole('dialog', { name: 'Assignee of PHAROS-12', exact: true })
  const search = menu.getByRole('searchbox', { name: 'Find assignee' })
  await search.pressSequentially('markus')
  await expect(search).toHaveValue('markus')
  await search.press('Enter')
  await expect(row(page, 'PHAROS-12').locator('.c-assignee')).toContainText('Markus Barta')
  await row(page, 'PHAROS-12').getByRole('button', { name: /Change assignee/ }).click()
  await expect(menu.getByRole('menuitemradio', { name: /Queue: next free agent/ })).toBeVisible()
  await expect(menu.getByRole('menuitemradio', { name: /Codex builder.*Workspace account/ })).toBeVisible()
  for (const theme of ['light', 'dark']) { await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme); await page.screenshot({ path: join(shots, `assignee-${theme}.png`) }) }
  await menu.getByRole('menuitemradio', { name: /Codex builder.*Workspace account/ }).click()
  await expect(row(page, 'PHAROS-12').locator('.q-word')).toContainText('Codex builder')
  expect(state.queueCalls.find(call => call.body?.agent_principal_id)?.body).toEqual({ node_id: 'n-2', agent_principal_id: agent, model_profile_id: profile, requested_account_id: account })
  await row(page, 'PHAROS-12').getByRole('button', { name: /Change assignee/ }).click()
  await expect(menu.getByRole('menuitemradio', { name: /Codex builder.*Workspace account/ })).toHaveAttribute('aria-checked', 'true')
  await expect(menu.getByRole('menuitemradio', { name: /Queue: next free agent/ })).toHaveAttribute('aria-checked', 'false')
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: /queued\. Open the work queue/ }).click()
  const own = page.getByRole('list', { name: 'Waiting for a specific agent' })
  await expect(own).toContainText('PHAROS-12'); await expect(own.locator('li')).not.toHaveAttribute('draggable', 'true')
  await page.keyboard.press('Escape')
  await row(page, 'PHAROS-13').getByRole('button', { name: /Change assignee/ }).click()
  const queuedMenu = page.getByRole('dialog', { name: 'Assignee of PHAROS-13', exact: true })
  await expect(queuedMenu).toContainText('replaces its current queue place')
  await queuedMenu.getByRole('menuitemradio', { name: /Codex builder.*Workspace account/ }).click()
  await expect.poll(() => state.targeted.has('n-3')).toBe(true)
  expect(state.queueCalls.some(call => call.path === '/api/queue/n-3' && call.method === 'DELETE')).toBe(true)
})
test('a live blocks relation satisfies readiness for the dot, Queue, bulk and Start now', async ({ page }) => {
  const { state } = await world(page, { relationBlocker: true })
  const blocked = row(page, 'PHAROS-14')
  await expect(blocked.locator('.q-btn')).not.toHaveClass(/unready/)
  await blocked.hover(); await blocked.getByRole('button', { name: 'Queue PHAROS-14', exact: true }).click()
  await expect.poll(() => state.ids.includes('n-4')).toBe(true)
  await expect(page.getByRole('dialog', { name: /what is missing/ })).toHaveCount(0)
  await blocked.getByRole('button', { name: /Remove PHAROS-14 from the queue/ }).click()
  await blocked.getByRole('checkbox', { name: 'Select PHAROS-14' }).check(); await page.keyboard.press('q')
  await expect.poll(() => state.ids.includes('n-4')).toBe(true)
  await page.keyboard.press('Escape')
  await blocked.getByRole('button', { name: /Change assignee/ }).click()
  const menu = page.getByRole('dialog', { name: 'Assignee of PHAROS-14', exact: true })
  await expect(menu.getByRole('button', { name: /Not ready/ })).toHaveCount(0)
  await menu.getByRole('menuitemradio', { name: /Codex builder.*Workspace account/ }).click()
  await expect.poll(() => state.targeted.has('n-4')).toBe(true)
})
test('a failed first queue read exposes its error and Retry before a snapshot exists', async ({ page }) => {
  const { state } = await world(page, { failRead: true })
  await page.getByRole('button', { name: /Queue read failed/ }).click()
  const panel = page.getByRole('dialog', { name: 'Work queue', exact: true })
  await expect(panel.getByRole('alert')).toContainText('Queue read failed')
  await expect(panel.getByText('Loading the queue…', { exact: true })).toHaveCount(0)
  state.failRead = false
  await panel.getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(panel.getByRole('alert')).toHaveCount(0)
  await expect(panel.getByRole('list', { name: 'Queue for the next free agent' }).locator('li')).toHaveCount(2)
  await expect(page.getByRole('button', { name: /2 queued\. Open the work queue/ })).toBeVisible()
})
test('shared queued rows without expected start show a labeled local release prediction', async ({ page }) => {
  await world(page, { noStart: true })
  const suggested = row(page, 'PHAROS-13').locator('.c-suggested .plan-rel')
  await expect(suggested).toHaveText('~Next')
  await expect(suggested).toHaveAccessibleDescription(/Visible queue estimate: wait ~3 h at 1 parallel runs/)
  await expect(suggested).toHaveAccessibleDescription(/Other projects not included/)
  await expect(row(page, 'PHAROS-14').locator('.c-suggested .plan-rel')).toHaveText('—')
})
test('failed writes preserve membership and viewer shortcuts cannot dispatch', async ({ page }) => {
  const { state } = await world(page)
  state.failWrite = true
  await row(page, 'PHAROS-12').hover()
  await row(page, 'PHAROS-12').getByRole('button', { name: 'Queue PHAROS-12', exact: true }).click()
  await expect(page.getByText('Queue change refused', { exact: true })).toBeVisible()
  expect(state.ids).not.toContain('n-2')
  const viewer = await page.context().newPage(), other = await world(viewer, { viewer: true })
  await expect(row(viewer, 'PHAROS-12').locator('.q-btn')).toHaveAttribute('aria-disabled', 'true')
  await row(viewer, 'PHAROS-12').hover()
  await row(viewer, 'PHAROS-12').locator('.q-btn').click({ force: true }); await viewer.keyboard.press('q')
  expect(other.state.queueCalls.every(call => call.method === 'GET')).toBe(true)
  await viewer.close()
})
test('approved queue surfaces in light/dark and narrow layouts, with accessible dialogs and release evidence', async ({ page }) => {
  await world(page); mkdirSync(shots, { recursive: true })
  for (const theme of ['light', 'dark']) {
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    await expect(row(page, 'PHAROS-15').locator('.c-suggested .plan-rel')).toHaveText('~Next release')
    await expect(row(page, 'PHAROS-16').locator('.c-suggested .plan-rel')).toHaveText('Cloud City')
    await expect(row(page, 'PHAROS-16').locator('.c-suggested .plan-rel')).toHaveAccessibleDescription(/261001180000\.0\.0/)
    await page.getByRole('button', { name: /queued\. Open the work queue/ }).click()
    await expect(page.getByRole('dialog', { name: 'Work queue', exact: true })).toBeVisible()
    const violations = (await new AxeBuilder({ page }).include('.floating').withTags(['wcag2a', 'wcag2aa']).analyze()).violations
    expect(violations).toEqual([])
    await page.screenshot({ path: join(shots, `queue-${theme}.png`) })
    await page.keyboard.press('Escape')
    await row(page, 'PHAROS-14').locator('.q-ind').hover()
    await expect(page.locator('.hcard')).toContainText('Blocked by PHAROS-11')
    await page.screenshot({ path: join(shots, `hover-${theme}.png`) })
    await row(page, 'PHAROS-13').locator('.title-text').click()
    await expect(page.getByRole('complementary', { name: 'Ticket details' }).locator('.q-card')).toBeVisible()
    await page.screenshot({ path: join(shots, `drawer-${theme}.png`) })
    await page.getByRole('button', { name: 'Close ticket details' }).click()
    await page.setViewportSize({ width: 390, height: 844 })
    await expect(page.locator('html')).toHaveJSProperty('scrollWidth', 390)
    await page.getByRole('button', { name: /queued\. Open the work queue/ }).click()
    await expect(page.getByRole('dialog', { name: 'Work queue', exact: true })).toBeVisible()
    await page.screenshot({ path: join(shots, `queue-phone-${theme}.png`) })
    await page.keyboard.press('Escape'); await page.setViewportSize({ width: 1600, height: 1000 })
  }
  const fragment = process.env.WORK_QUEUE_FRAGMENT
  if (fragment && existsSync(fragment)) {
    const approved = await page.context().newPage()
    await approved.setViewportSize({ width: 1600, height: 1000 }); await approved.setContent(readFileSync(fragment, 'utf8'))
    for (const theme of ['light', 'dark']) {
      await approved.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
      await approved.screenshot({ path: join(shots, `approved-fragment-${theme}.png`), fullPage: true })
    }
    await approved.close()
  }
})


test('stale progress queues as Open with Undo and keeps list controls still', async ({ page }) => {
  const { state, data } = await world(page, { stale: true })
  const ticket = row(page, 'PHAROS-12'), queue = ticket.locator('.q-btn')
  await ticket.hover()
  await expect(queue).toHaveAttribute('aria-disabled', 'false')
  await expect(queue).toHaveAttribute('data-tip', 'In progress, but nobody is working on it. Queue it for the next free agent · q')
  await expect(ticket.locator('.stale-hint.shown')).toHaveCount(1)
  await expectStableControls({ controls: { queue, copyKey: ticket.locator('.key-btn'), clickedRow: ticket }, interactions: [
    { name: 'queue stale work', run: async () => {
      await queue.click()
      await expect(ticket.locator('.c-status')).toContainText('Open')
      await expect(queue).toHaveAttribute('aria-pressed', 'true')
      await expect(page.getByText('PHAROS-12 queued', { exact: true })).toBeVisible()
      expect(state.ids).toContain('n-2'); expect(data.nodes.find(n => n.id === 'n-2')!.state).toBe('open')
    } },
    { name: 'Undo queue addition', run: async () => {
      await page.getByRole('button', { name: 'Undo', exact: true }).click()
      await expect(ticket.locator('.c-status')).toContainText('In progress')
      await expect(queue).toHaveAttribute('aria-pressed', 'false')
      await expect(ticket.locator('.stale-hint.shown')).toHaveCount(1)
      expect(state.ids).not.toContain('n-2'); expect(data.nodes.find(n => n.id === 'n-2')!.state).toBe('in_progress')
      await ticket.hover()
    } },
  ] })
})

test('assigned In progress explains who is working using the display status', async ({ page }) => {
  const { state } = await world(page, { assigned: true })
  const ticket = row(page, 'PHAROS-12'), queue = ticket.locator('.q-btn')
  await ticket.hover()
  await expect(queue).toHaveAttribute('aria-disabled', 'true')
  await expect(queue).toHaveAttribute('data-tip', "Already in progress (Markus Barta), can't be queued.")
  await expect(ticket.locator('.stale-hint.shown')).toHaveCount(0)
  await queue.focus(); await page.keyboard.press('Enter')
  expect(state.queueCalls.filter(call => call.method === 'POST')).toHaveLength(0)
  await ticket.locator('.title-text').click()
  const drawer = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(drawer.locator('.q-btn')).toHaveAttribute('data-tip', "Already in progress (Markus Barta), can't be queued.")
})

test('live-work In progress stays disabled even when worker details are hidden', async ({ page }) => {
  const { state } = await world(page, { live: true })
  const queue = row(page, 'PHAROS-12').locator('.q-btn')
  await expect(queue).toHaveAttribute('aria-disabled', 'true')
  await expect(queue).toHaveAttribute('data-tip', "Already in progress (assigned or active work), can't be queued.")
  await row(page, 'PHAROS-12').hover(); await queue.focus(); await page.keyboard.press('Enter')
  expect(state.queueCalls.filter(call => call.method === 'POST')).toHaveLength(0)
})


test('stale progress hints and ticket actions stay still in both themes at phone tablet and desktop widths', async ({ page }) => {
  await world(page, { stale: true, longTitle: true })
  for (const width of [390, 1024, 1440]) {
    await page.setViewportSize({ width, height: 1000 })
    const ticket = row(page, 'PHAROS-12')
    for (const theme of ['light', 'dark']) {
      await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
      for (const dismiss of await page.locator('.toast-close').all()) await dismiss.click()
      await ticket.hover()
      await expect(ticket.locator('.stale-hint.shown')).toHaveCount(1)
      await page.screenshot({ path: join(shots, `aeon-682-list-${width}-${theme}.png`) })
      await ticket.locator('.title-text').click()
      const drawer = page.getByRole('complementary', { name: 'Ticket details' })
      await expect(drawer.locator('.stale-hint.shown')).toHaveCount(1)
      const action = drawer.locator('.q-btn')
      await expectStableControls({ controls: { queue: action, close: drawer.getByRole('button', { name: 'Close ticket details' }) }, interactions: [
        { name: 'queue stale ticket in its detail view', run: async () => { await action.click(); await expect(action).toHaveAttribute('aria-pressed', 'true') } },
        { name: 'Undo in detail view', run: async () => { await page.getByRole('button', { name: 'Undo', exact: true }).click(); await expect(action).toHaveAttribute('aria-pressed', 'false') } },
      ] })
      for (const dismiss of await page.locator('.toast-close').all()) await dismiss.click()
      await page.screenshot({ path: join(shots, `aeon-682-ticket-${width}-${theme}.png`) })
      await drawer.getByRole('button', { name: 'Close ticket details' }).click()
    }
  }
})


test('stale progress queued through readiness fixes also offers Undo', async ({ page }) => {
  const { data, state } = await world(page, { stale: true, staleMissing: true })
  const ticket = row(page, 'PHAROS-12')
  await ticket.hover(); await ticket.locator('.q-btn').click()
  const panel = page.getByRole('dialog', { name: 'PHAROS-12: what is missing to queue it' })
  await panel.getByRole('button', { name: 'Draft criteria' }).click()
  await panel.getByRole('textbox', { name: 'Draft acceptance criteria' }).fill('- [ ] Recovery verified')
  await panel.getByRole('button', { name: 'Save criteria' }).click()
  await expect(panel.getByRole('button', { name: 'Queue', exact: true })).toBeEnabled()
  await panel.getByRole('button', { name: 'Queue', exact: true }).click()
  await expect(ticket.locator('.c-status')).toContainText('Open')
  await page.getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(ticket.locator('.c-status')).toContainText('In progress')
  expect(state.ids).not.toContain('n-2')
  expect(data.nodes.find(n => n.id === 'n-2')!.fields.acceptance_criteria).toBe('- [ ] Recovery verified')
})
