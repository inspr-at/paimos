// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1285: agent rows say how a session ended in words, mark only the project's claimed lead,
// and fold a failed attempt away once its run family retried it. Risk: a raw stop code reaches
// the page; every coordinator reads Lead; a retried failure keeps its red row (and its bin) until
// someone removes it; the fold moves rows under the person. Time is the page clock, never a sleep.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { controlStability } from './control-stability'
import type { ProjectLead } from '../src/lib/lead'

const NOW = Date.parse('2026-10-14T10:00:00Z')
const ago = (minutes: number) => new Date(NOW - minutes * 60_000).toISOString()
const sid = (n: number) => `5e000000-0000-4000-8000-0000000012${String(n).padStart(2, '0')}`
const AEON_LEAD = sid(1), TICK = sid(2), R2 = sid(3), R3 = sid(4), ORPHAN = sid(5)
const GATE = 'aeon1280-switch-script-gate'
const RAW_CODES = /process_failed|process_exited|harness_crashed|lease_expired|operator_stop|heartbeat_lost|Reported stop reason/

async function setup(page: Page, theme: 'light' | 'dark', failedMinutes: number, retried: boolean) {
  await page.clock.install({ time: NOW })
  const work = fixtures(); work.preferences.theme = { choice: theme }
  work.preferences['ui.agents.sections'] = { dial: false }
  await mockWork(page, work, { admin: true })
  const data = agentData({
    now: NOW, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
    nodes: { 'n-1': { key: 'PHAROS-7', title: 'Fleet list' }, 'n-a1': { key: 'AEON-1280', title: 'Switch script gate' }, 'n-5': { key: 'PRJ-26-5', title: 'Release check' } },
  })
  const base = { ...data.sessions[0]!, run_id: null, parent_harness_session_id: null, model: 'claude-opus-5-5', reasoning_effort: 'xhigh', account_label: 'agent.one@example.com' }
  const aeon = { project_id: 'p-aeon', project: { id: 'p-aeon', key: 'AEON', title: 'Aeon' } }
  const gate = { ...aeon, role: 'worker', ticket_node_id: 'n-a1', ticket: { id: 'n-a1', key: 'AEON-1280', title: 'Switch script gate' }, owner_principal_id: me.id }
  // AEON's claimed lead and Tick, a coordinator without the claim.
  data.sessions.push({ ...base, ...aeon, id: AEON_LEAD, role: 'coordinator', display_label: 'aeon-lead', ticket_node_id: null, ticket: null, created_at: ago(300) })
  data.sessions.push({ ...base, ...aeon, id: TICK, role: 'coordinator', display_label: 'tick', ticket_node_id: null, ticket: null, created_at: ago(200) })
  // The real case: the gate's r2 failed at start; r3 is its retry.
  data.sessions.push({ ...base, ...gate, id: R2, display_label: `${GATE}-r2`, phase: 'stopped', activity: 'idle', heartbeat_at: ago(failedMinutes + 0.2), stopped_at: ago(failedMinutes), stop_reason: 'process_failed', finished: false, created_at: ago(failedMinutes + 0.5) })
  const retry = { ...base, ...gate, id: R3, display_label: `${GATE}-r3`, phase: 'working', activity: 'busy', heartbeat_at: ago(0.2), created_at: ago(Math.max(0.5, failedMinutes - 1)) }
  if (retried) data.sessions.push(retry)
  // A failure nobody retried stays a problem, with its bin.
  data.sessions.push({ ...base, project_id: 'p-frozen', project: { id: 'p-frozen', key: 'PRJ-26', title: 'Studio infrastructure' }, id: ORPHAN, role: 'worker', display_label: 'release-check', ticket_node_id: 'n-5', ticket: { id: 'n-5', key: 'PRJ-26-5', title: 'Release check' }, phase: 'stopped', activity: 'idle', heartbeat_at: ago(20.2), stopped_at: ago(20), stop_reason: 'process_failed', finished: false, created_at: ago(25) })
  await mockAgents(page, data)
  const lead = (project: string, session: string | null): ProjectLead => ({ project_id: project, state: session ? 'working' : 'none', reason: '', revision: 1, generation: session ? 1 : 0, session_id: session, process_active: !!session, automatic_launch_enabled: true })
  const leads: Record<string, ProjectLead> = { 'p-pharos': lead('p-pharos', data.sessions[0]!.id), 'p-aeon': lead('p-aeon', AEON_LEAD), 'p-frozen': lead('p-frozen', null) }
  await page.route(/\/api\/projects\/[^/]+\/lead$/, route => {
    const id = decodeURIComponent(new URL(route.request().url()).pathname.split('/')[3]!)
    return leads[id] && route.request().method() === 'GET' ? route.fulfill({ json: leads[id] }) : route.fulfill({ status: 404, json: { error: 'not found' } })
  })
  await page.route(url => url.pathname === '/api/queue', route => route.fulfill({ json: { items: [], count: 0, manual_order: false, capacity: { queued_hours: 0, parallel_runs: 4, work_hours: null, warning: false } } }))
  return { data, retry }
}

const row = (page: Page, id: string) => page.locator(`.sessions [data-row="s:${id}"]`)

for (const theme of ['light', 'dark'] as const) for (const width of [1440, 390]) {
  test(`${theme} ${width}: stop reasons in words, Lead only for the claimed lead, a retried failure folds into Ended`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const errors = watchErrors(page)
    const { data } = await setup(page, theme, 30, true)
    await page.goto('/agents')
    await expect(row(page, R3)).toBeVisible()
    // Lead means the project's lead claim: PHAROS's and AEON's leads say it, Tick, a coordinator without it, does not.
    for (const id of [data.sessions[0]!.id, AEON_LEAD]) {
      await expect(row(page, id).locator('.role')).toHaveText('Lead')
      await expect(row(page, id).locator('.ava')).toHaveAttribute('aria-label', /^Lead /)
    }
    await expect(row(page, TICK).locator('.role')).toHaveCount(0)
    await expect(row(page, TICK).locator('.ava')).not.toHaveAttribute('aria-label', /^Lead /)
    // A failure nobody retried stays a problem in words, with its bin on the wide table.
    await expect(row(page, ORPHAN).locator('.ava')).toHaveAttribute('data-state', 'problem')
    await expect(row(page, ORPHAN).locator('.l2t')).toHaveText('The worker process failed · Retry or check the ticket')
    if (width === 1440) await expect(row(page, ORPHAN).locator('.bin')).toBeVisible()
    // The retried r2 already sits in the folded Ended group, though it failed before this rule existed.
    await expect(row(page, R2)).toHaveCount(0)
    await expect(page.locator('.sessions')).not.toContainText(RAW_CODES)
    // Unfolding Ended moves nothing above it.
    const guard = await controlStability(page, { retry: row(page, R3), orphan: row(page, ORPHAN), tick: row(page, TICK) })
    await guard.check(() => page.getByRole('button', { name: /^Ended\s*\d/ }).click())
    guard.done()
    const r2 = row(page, R2)
    await expect(r2.locator('.ava')).toHaveAttribute('aria-label', /: Ended$/)
    await expect(r2.locator('.l2t')).toHaveText(`Retried as ${GATE}-r3 · Process failed`)
    await expect(page.locator('.sessions')).not.toContainText(RAW_CODES)
    await r2.scrollIntoViewIfNeeded()
    await page.screenshot({ path: info.outputPath(`AEON-1285-rows-${width}-${theme}.png`), fullPage: true })
    // Its history stays in its details, which lead to the retry.
    await r2.locator('.agent-link').click()
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel.locator('.now-step')).toHaveText(`Retried as ${GATE}-r3 · Process failed`)
    await expect(panel.getByRole('link', { name: 'Open the retry' })).toHaveAttribute('href', `/agents/${R3}`)
    await expect(panel).not.toContainText(RAW_CODES)
    await page.screenshot({ path: info.outputPath(`AEON-1285-details-${width}-${theme}.png`) })
    expect(errors).toEqual([])
  })
}

// The retry arrives while the page is open: the failed row keeps its place and says it was retried,
// then moves to Ended 10 minutes after it ended, like any ended row. No one removes it.
test('a failure retried while the page is open keeps its place, names the retry, then moves to Ended', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const errors = watchErrors(page)
  const { data, retry } = await setup(page, 'light', 2, false)
  await page.goto('/agents')
  const r2 = row(page, R2)
  await expect(r2.locator('.ava')).toHaveAttribute('data-state', 'problem')
  await expect(r2.locator('.l2t')).toHaveText('The worker process failed · Retry or check the ticket')
  await page.mouse.move(0, 0)
  data.sessions.push(retry)
  await page.clock.runFor(21_000)
  await expect(row(page, R3)).toBeVisible()
  await expect(r2.locator('.ava')).toHaveAttribute('aria-label', /: Ended$/)
  await expect(r2.locator('.l2t')).toHaveText(new RegExp(`^Retried as ${GATE}-r3 · Process failed \\d{1,2}:58( [AP]M)? · moves to Ended at \\d{1,2}:08( [AP]M)?$`))
  await page.clock.runFor(8 * 60_000)
  await expect(r2).toHaveCount(0)
  await expect(row(page, R3)).toBeVisible()
  expect(errors).toEqual([])
})
