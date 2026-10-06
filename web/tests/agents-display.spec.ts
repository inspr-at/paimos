// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { ACCOUNTS, capacityWorld, NOW } from './capacity-fixtures'
import { expectStableControls } from './helpers/stable'

const id = (n: number) => `5e000000-0000-4000-8000-${String(n).padStart(12, '0')}`
const row = (page: Page, n: number) => page.locator(`[data-row="s:${id(n)}"]`)
const dial = (page: Page) => page.getByRole('region', { name: 'Agents at once' })
const liveCount = (page: Page) => page.locator('.sessions .group-row.live .mono')
const shots = process.env.AEON691_SHOTS

async function setup(page: Page, theme: 'light' | 'dark' = 'light', unrelated = false, endedCount = 696) {
  await page.clock.setSystemTime(NOW)
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  work.preferences['agents.working'] = { total: 20, limits: {} }
  work.preferences['agents.working.display'] = { folded: true }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now: NOW, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {
    'p-pharos': { key: 'PHAROS', title: 'Pharos' },
    'n-1': { key: 'PHAROS-11', title: 'Zuverlässige Sitzungsanzeigen für die Veröffentlichungskoordination' },
  } })
  const base = data.sessions[0]!
  const session = (n: number, stopped = false) => ({
    ...base, id: id(n), display_label: n === 1 ? 'AEON-LEAD' : `Veröffentlichungsprüfung und Kapazitätsabgleich ${n}`,
    role: n === 1 ? 'coordinator' : 'worker', harness: n === 1 ? 'claude' : 'codex',
    management_mode: 'unmanaged', run_id: null, parent_harness_session_id: n === 1 ? null : id(1),
    phase: stopped ? 'stopped' : 'working', activity: 'busy',
    created_at: new Date(NOW - (1000 + n) * 1000).toISOString(),
    heartbeat_at: new Date(NOW - 1000).toISOString(),
    stopped_at: stopped ? new Date(NOW - 60_000).toISOString() : null,
    eta_ready_at: new Date(NOW + 5 * 60_000).toISOString(), eta_reported_at: new Date(NOW).toISOString(), progress_pct: 80,
  }) as unknown as typeof data.sessions[number]
  data.sessions = [...Array.from({ length: 4 }, (_, i) => session(i + 1)), ...Array.from({ length: endedCount }, (_, i) => session(i + 10, true))]
  data.approvals = []; data.messages = []; data.runs = []; data.targets = []
  const capacity = capacityWorld()
  data.accounts = capacity.accounts.filter(a => a.id === ACCOUNTS.main).map(a => ({ ...a, registered_by_principal_id: me.id, owner_person_id: unrelated ? 'another-person' : me.id })) as unknown as typeof data.accounts
  const calls = await mockAgents(page, data, { capacity, workingPreference: () => work.preferences['agents.working'] })
  // Preserve real pagination: the shared mock otherwise truncates this large
  // fixture at its first page and cannot prove stopped-history behavior.
  await page.route('**/api/harness-sessions?*', route => {
    const q = new URL(route.request().url()).searchParams
    const start = Number(q.get('cursor') ?? 0), limit = Math.min(250, Number(q.get('limit') ?? 50))
    const items = data.sessions.slice(start, start + limit)
    return route.fulfill({ json: { items, next_cursor: start + limit < data.sessions.length ? String(start + limit) : null } })
  })
  await page.route('**/api/agent-accounts/capacity', route => {
    const answer = capacity.handle('/api/agent-accounts/capacity', 'GET', null)!.json as { account_id: string }[]
    return route.fulfill({ json: answer.filter(a => a.account_id === ACCOUNTS.main).map(a => ({ ...a, routing: { rank: 1, available_slots: 17 } })) })
  })
  await page.route('**/api/projects/*/harness-sessions/*/tier', route => {
    const sessionId = new URL(route.request().url()).pathname.split('/').at(-2)
    return route.fulfill({ json: { session_id: sessionId, revision: 0, active_tier: null, pending: null, read_only: true, reports: [], requests: [] } })
  })
  return { data, calls }
}

test('live families count all external workers, hide hundreds of ended siblings and retain counts when folded', async ({ page }) => {
  await setup(page)
  await page.goto('/agents')
  await expect(page.locator('.live-total')).toHaveText('4 live')
  await expect(liveCount(page)).toHaveText('4')
  await expect(page.locator('.sessions .row')).toHaveCount(4)
  await expect(row(page, 1).locator('.history-toggle')).toHaveText('696 stopped')
  await expect(row(page, 1).locator('.history-toggle')).toHaveAttribute('aria-expanded', 'false')
  await expect(dial(page).locator('.f-live')).toContainText('4 running · room for 16 more · your agents')
  await expect(dial(page).locator('.f-live')).toHaveAttribute('data-tip', /including sessions started outside PAIMOS/)
  const toggle = row(page, 1).locator('.worker-toggle').first()
  await expectStableControls({
    controls: { lead: row(page, 1), workers: toggle, history: row(page, 1).locator('.history-toggle'), sort: page.locator('.th-sort').first() },
    scrollAreas: { sessions: page.locator('.sessions') },
    interactions: [
      { name: 'fold live family', run: async () => { await toggle.click(); await expect(page.locator('.sessions .row')).toHaveCount(1); await expect(liveCount(page)).toHaveText('4') } },
      { name: 'reveal live workers', run: async () => { await toggle.click(); await expect(page.locator('.sessions .row')).toHaveCount(4); await expect(liveCount(page)).toHaveText('4') } },
    ],
  })
  await row(page, 2).locator('.agent-link').click()
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel.locator('.now-eta .report-source')).toHaveText('Agent report')
  await expect(panel.locator('.now-eta .pct')).toHaveText('80%')
  await expect(panel.locator('.now-eta .report-source')).toHaveAttribute('data-tip', /self-reported.*not measured/)
  // A direct ended-worker link exposes only that child, leaving its 695 ended
  // siblings folded. No deletion or cleanup write is needed to tidy the list.
  await page.goto(`/agents/${id(10)}`)
  await expect(row(page, 10)).toBeVisible()
  await expect(row(page, 11)).toHaveCount(0)
  await expect(row(page, 10).locator('.session-estimate')).toHaveCount(0)
  await expect(liveCount(page)).toHaveText('4')
})

test('another person’s visible account never becomes your full account or your capacity', async ({ page }) => {
  await setup(page, 'light', true)
  await page.route('**/api/agents/plan', route => route.fulfill({ json: { total: 20, limits: {}, principal_id: me.id, running: {}, running_total: 0, source: 'plan', updated_at: null } }))
  await page.goto('/agents')
  await expect(page.locator('.live-total')).toHaveText('4 live')
  await expect(dial(page).locator('.f-live')).toContainText('0 running · account room not measured yet · your agents')
  await expect(dial(page).locator('.f-live')).not.toContainText('accounts full')
})

test('ended workers remain recoverable through an opt-in fold without cleanup writes', async ({ page }) => {
  const { data, calls } = await setup(page, 'light', false, 3)
  await page.goto('/agents')
  const history = row(page, 1).locator('.history-toggle')
  await expect(history).toHaveAttribute('aria-expanded', 'false')
  await expectStableControls({
    controls: { lead: row(page, 1), history, workers: row(page, 1).locator('.worker-toggle').first() },
    scrollAreas: { sessions: page.locator('.sessions') },
    interactions: [
      { name: 'show ended workers', run: async () => { await history.click(); await expect(page.locator('.sessions .row')).toHaveCount(7); await expect(history).toHaveAttribute('aria-expanded', 'true'); await expect(row(page, 10)).toHaveClass(/stopped/) } },
      { name: 'hide ended workers', run: async () => { await history.click(); await expect(page.locator('.sessions .row')).toHaveCount(4); await expect(liveCount(page)).toHaveText('4') } },
    ],
  })
  await history.click()
  await expect(row(page, 10)).toBeVisible()
  await page.reload()
  await expect(history).toHaveAttribute('aria-expanded', 'false')
  await expect(row(page, 10)).toHaveCount(0)
  expect(data.sessions.filter(session => session.stopped_at)).toHaveLength(3)
  expect(calls.filter(call => call.path.includes('harness-sessions') && call.method !== 'GET')).toEqual([])
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`${width} ${theme}: existing Agents layout shows the report source and stable capacity controls`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await setup(page, theme)
    await page.goto('/agents')
    await expect(liveCount(page)).toHaveText('4')
    await expect(dial(page).locator('.f-live')).toContainText('room for 16 more')
    await expect(row(page, 2).locator('.report-source')).toBeVisible()
    await expect(row(page, 2).locator('.pct')).toHaveText('80%')
    await expectStableControls({
      controls: { more: dial(page).getByRole('button', { name: 'One agent more at once' }), fewer: dial(page).getByRole('button', { name: 'One agent fewer at once' }), fold: dial(page).locator('.f-fold') },
      scrollAreas: { dial: dial(page), page: page.locator('.agents-page') },
      interactions: [{ name: 'increase total without moving actions', run: async () => { await dial(page).getByRole('button', { name: 'One agent more at once' }).click(); await expect(dial(page).locator('.f-num')).toHaveText('21') } }],
    })
    if (shots) {
      mkdirSync(shots, { recursive: true })
      await page.screenshot({ path: join(shots, `agents-${width}-${theme}.png`) })
      await page.locator('.sessions').scrollIntoViewIfNeeded()
      await page.screenshot({ path: join(shots, `agents-sessions-${width}-${theme}.png`) })
    }
    await row(page, 2).locator('.agent-link').click()
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel.locator('.now-eta .report-source')).toBeVisible()
    await expectStableControls({
      controls: { overview: panel.getByRole('tab', { name: 'Overview' }), messages: panel.getByRole('tab', { name: 'Messages' }), tabs: panel.getByRole('tablist'), close: panel.getByRole('button', { name: 'Close session details' }), frame: panel },
      scrollAreas: { panel },
      interactions: [
        { name: 'show messages', run: async () => { await panel.getByRole('tab', { name: 'Messages' }).click(); await expect(panel.getByRole('tab', { name: 'Messages' })).toHaveAttribute('aria-selected', 'true') } },
        { name: 'return to reported progress', run: async () => { await panel.getByRole('tab', { name: 'Overview' }).click(); await expect(panel.locator('.now-eta .report-source')).toBeVisible() } },
      ],
    })
    if (shots) await page.screenshot({ path: join(shots, `agents-session-details-${width}-${theme}.png`) })
  })
}
