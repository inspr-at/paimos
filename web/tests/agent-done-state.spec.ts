// SPDX-License-Identifier: AGPL-3.0-only
// AEON-437: a worker that finished (reported 100% and left with a recorded clean exit)
// shows Done, anything else that stopped shows Ended, and only a failure stays red, in
// /agents and in the ticket list's assignee cell. Silence at 100% is no finish.
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, liveAgent, mockWork } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'

const world: AgentWorld = {
  me: 'u-me',
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}
const shots = process.env.AEON_437_SHOTS
if (shots) test.use({ deviceScaleFactor: 4 })
const shot = async (page: Page, name: string) => { if (shots) { mkdirSync(shots, { recursive: true }); await page.screenshot({ path: join(shots, `${name}.png`) }) } }
const sessionRow = (page: Page, n: number) => page.locator(`[data-row="s:5e000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}"]`)

for (const theme of ['light', 'dark'] as const) {
  test(`finished, ended and failed sessions read differently on /agents (${theme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: theme })
    await page.setViewportSize({ width: 1600, height: 1000 })
    await mockWork(page, fixtures(), { admin: true })
    const data = agentData({ ...world })
    // The server sends finished: true for the clean 100% exit only; here it also sits beside the stop reason.
    Object.assign(data.sessions[6]!, { progress_pct: 100, stop_reason: 'process_exited', finished: true })
    Object.assign(data.sessions[7]!, { run_id: null, progress_pct: 40, stop_reason: 'process_exited', finished: false, activity_note: 'Reviewing the diff before stopping' })
    Object.assign(data.sessions[8]!, { progress_pct: 100, stop_reason: 'process_failed', finished: false })
    // Reported 100%, then went quiet for nine minutes: no recorded exit, so the heartbeat warning stays.
    Object.assign(data.sessions[4]!, { progress_pct: 100 })
    await mockAgents(page, data)
    await page.goto('/agents')
    await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
    await expect(sessionRow(page, 5).locator('.agent-state-label')).toHaveAttribute('data-state', 'awaiting')
    await expect(sessionRow(page, 5).locator('.c-state')).not.toContainText('Done')
    await expect(sessionRow(page, 9).locator('.agent-state-label')).toHaveAttribute('data-state', 'problem')
    await page.locator('.group-toggle').filter({ hasText: 'Ended' }).click()
    await expect(sessionRow(page, 7).locator('.agent-state-label')).toHaveAttribute('data-state', 'done')
    await expect(sessionRow(page, 7).locator('.c-state')).toContainText('Done')
    await expect(sessionRow(page, 8).locator('.agent-state-label')).toHaveAttribute('data-state', 'stopped')
    await expect(sessionRow(page, 8).locator('.c-state')).toContainText('Ended')
    await expect(sessionRow(page, 7).locator('.eta-cell')).toHaveCount(0)
    await shot(page, `agents-${theme}`)
    await page.goto('/agents/5e000000-0000-4000-8000-000000000008')
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel.locator('.now-step')).toHaveText('Reviewing the diff before stopping')
    await expect(panel.locator('.agent-state-label').first()).toContainText('Ended')
    await page.goto('/agents/5e000000-0000-4000-8000-000000000007')
    await expect(panel.locator('.now-meta').first()).toContainText('Done')
    await shot(page, `panel-done-${theme}`)
  })
}

test('the ticket list keeps a silent 100% worker on its heartbeat warning and shows no avatar for a stopped one', async ({ page }) => {
  await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z'))
  const at = Date.parse('2026-09-23T12:00:00Z')
  const minutesAgo = (minutes: number) => new Date(at - minutes * 60_000).toISOString()
  const ticket = (id: string, key: string, title: string) => ({ id, key, title, project_id: 'p-pharos' })
  const stopped = { phase: 'stopped', stopped_at: minutesAgo(5), heartbeat_at: minutesAgo(6), progress_pct: 100 } as const
  const data = fixtures()
  data.live.push(
    // Reported 100%, then went quiet with no exit recorded: the heartbeat warning stays.
    liveAgent({ project_id: 'p-pharos', session_id: 's-quiet', name: 'wren', progress_pct: 100, heartbeat_at: minutesAgo(12), ticket: ticket('n-2', 'PHAROS-12', 'Add an Oracle Cloud connector') }),
    // Stopped sessions are history, not workers on the ticket: a clean exit, a hidden
    // reason and a plain stop alike leave the assignee cell without a red stopped avatar.
    liveAgent({ project_id: 'p-pharos', session_id: 's-done', name: 'hausv', ...stopped, stop_reason: 'process_exited', finished: true, ticket: ticket('n-1', 'PHAROS-11', 'Connect Hetzner Cloud for managed provisioning') }),
    liveAgent({ project_id: 'p-pharos', session_id: 's-hidden', name: 'kite', ...stopped, finished: true, ticket: ticket('n-3', 'PHAROS-13', 'Hetzner Cloud quota check') }),
    liveAgent({ project_id: 'p-pharos', session_id: 's-plain', name: 'amy', ...stopped, stop_reason: 'stopped', finished: false, ticket: ticket('n-4', 'PHAROS-14', 'Add a DigitalOcean connector') }),
  )
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS')
  const row = (key: string) => page.getByRole('grid', { name: 'Tickets' }).locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
  await expect(row('PHAROS-12').locator('.live-bot')).toHaveAttribute('data-state', 'unresponsive')
  for (const key of ['PHAROS-11', 'PHAROS-13', 'PHAROS-14']) await expect(row(key).locator('.live-bot')).toHaveCount(0)
  if (shots) await row('PHAROS-12').locator('.c-assignee').screenshot({ path: join(shots, 'worker-PHAROS-12.png') })
})
