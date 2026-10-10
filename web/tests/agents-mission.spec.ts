// SPDX-License-Identifier: AGPL-3.0-only
// AEON-192: mocked mission-control coverage and opt-in review captures.
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const now = Date.parse('2026-09-26T16:00:00Z')
async function setup(page: Page, theme: 'light' | 'dark' = 'light', reportedMetadata = false, reportedTier = true) {
  await page.clock.install({ time: now })
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {
    'p-pharos': { key: 'PHAROS', title: 'Pharos' }, 'n-1': { key: 'PHAROS-11', title: 'Provisioning lead' }, 'n-2': { key: 'PHAROS-12', title: 'PDF worker image' },
  } })
  const lead = data.sessions[0]!, worker = data.sessions[1]!, stopped = data.sessions[6]!
  Object.assign(lead, { display_label: 'aeon-coordinator', activity_note: 'Coordinating two workers', activity_note_id: 10, heartbeat_at: new Date(now - 5_000).toISOString() })
  Object.assign(worker, { display_label: 'hausv', parent_harness_session_id: lead.id, activity_note: 'Running PDF tests', activity_note_id: 11, heartbeat_at: new Date(now - 8_000).toISOString(), activity_history: [
    { note: 'Running PDF tests', at: new Date(now - 60_000).toISOString() },
    { note: 'Rebuilt worker image', at: new Date(now - 6 * 60_000).toISOString() },
  ] })
  Object.assign(stopped, { parent_harness_session_id: lead.id, display_label: 'past worker' })
  if (reportedMetadata) Object.assign(worker, {
    model: 'gpt-6-sol', reasoning_effort: 'xhigh', account_label: 'Codex Pro', harness_version: '1.2.3',
    brief: 'AEON-213', worktree: '/Code/aeon-worktrees/tm1-session-metadata', branch: 'tm1.session-metadata',
    commits: [{ sha: 'abc1234', subject: 'Store session setup' }, { sha: 'def5678', subject: 'Show work context' }],
  })
  data.sessions.splice(0, data.sessions.length, lead, worker, stopped)
  data.runs.splice(0)
  data.approvals.splice(0)
  data.messages.splice(0)
  data.targets.splice(0)
  // Tier reporting is independent of optional model/account/worktree metadata.
  // Keep the metadata assertions intact and mock the added tier read explicitly.
  for (const session of data.sessions) Object.assign(session, { service_tier: reportedTier ? 'default' : null, service_tier_revision: 1 })
  await mockAgents(page, data)
  let tierReads = 0
  await page.route(/\/api\/projects\/[^/]+\/harness-sessions\/[^/]+\/tier$/, async route => {
    const session = data.sessions.find(s => s.id === new URL(route.request().url()).pathname.split('/').at(-2))
    expect(route.request().method()).toBe('GET')
    expect(session).toBeDefined()
    tierReads++
    await route.fulfill({ json: { session_id: session!.id, revision: 1, active_tier: reportedTier ? 'default' : null, pending: null, read_only: true, read_only_reason: 'This daemon does not support confirmed tier changes.', reports: [], requests: [] } })
  })
  return { lead, worker, stopped, tierReads: () => tierReads }
}

test('live tiles show current steps and rows nest workers with stopped history collapsed', async ({ page }) => {
  const { lead, worker, stopped } = await setup(page)
  await page.goto('/agents')
  // The head counts them (AEON-780); names and steps live in the table (AEON-299).
  await expect(page.getByRole('group', { name: 'Show sessions by state' }).locator('[data-filter="working"]')).toContainText('2working')
  await expect(page.locator(`[data-row="s:${worker.id}"]`)).toHaveAttribute('data-parent', lead.id)
  await expect(page.locator(`[data-row="s:${worker.id}"] .result`)).toHaveText('PDF worker image')
  await expect(page.locator(`[data-row="s:${worker.id}"] .session-name`)).toHaveText('hausv')
  await expect(page.locator(`[data-row="s:${worker.id}"] .result`)).not.toContainText('Running PDF tests')
  await expect(page.locator(`[data-row="s:${stopped.id}"]`)).toHaveCount(0)
  await expect(page.locator('.sessions .thead')).not.toContainText(/Account|Model/)
})

test('detail shows now, activity, ticket status and hides unreported fields', async ({ page }) => {
  const { worker } = await setup(page)
  await page.goto(`/agents/${worker.id}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel).toContainText('Running PDF tests')
  await expect(panel.locator('.activity-timeline li')).toHaveCount(2)
  await expect(panel.locator('.head-sub')).toContainText('PHAROS-12')
  await expect(panel.locator('.service-tier .tier-head strong')).toHaveText('Default')
  await expect(panel).not.toContainText('Not reported')
  await expect(panel).not.toContainText('Account not reported')
  await panel.getByRole('button', { name: 'More session actions', exact: true }).click()
  await expect(page.getByRole('menuitem', { name: 'Interrupt this step', exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(panel.getByRole('button', { name: 'Stop now…', exact: true })).toBeVisible()
})

test('an unreported service tier stays explicit while optional setup fields stay absent', async ({ page }) => {
  const { worker, tierReads } = await setup(page, 'light', false, false)
  await page.goto(`/agents/${worker.id}`)
  await expect.poll(tierReads).toBeGreaterThan(0)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel.locator('.service-tier .tier-head strong')).toHaveText('Not reported')
  await expect(panel.locator('.facts dt').filter({ hasText: /^(Model|Account|Worktree|Branch)$/ })).toHaveCount(0)
  await expect(panel).not.toContainText('Account not reported')
  await expect(panel).not.toContainText('Unmocked route')
})

// The track hangs from the parent's fold button and ends on the child's fold
// centre (AEON-784, AEON-908): the stem and the child's elbow are one line,
// joined at the row edge, and the elbow meets the child on its glyph's centre line.
for (const width of [1600, 390]) for (const reportedMetadata of [false, true]) {
  test(`tree guide joins the parent's fold button to the child at ${width}px${reportedMetadata ? ' with reported metadata' : ''}`, async ({ page }) => {
    const { lead, worker } = await setup(page, 'light', reportedMetadata)
    await page.setViewportSize({ width, height: 1000 })
    await page.goto('/agents')
    await expect(page.locator(`[data-row="s:${worker.id}"]`)).toBeVisible()
    const geometry = await page.evaluate(({ lead, worker }) => {
      const parent = document.querySelector(`[data-row="s:${lead}"]`)!
      const child = document.querySelector(`[data-row="s:${worker}"]`)!
      const rect = (el: Element) => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height, bottom: r.bottom } }
      const elbow = getComputedStyle(child.querySelector('.tree-guide.elbow')!, '::after')
      const last = getComputedStyle(child.querySelector('.tree-guide.elbow')!, '::before')
      return {
        fold: rect(parent.querySelector('.tree-fold')!),
        parent: rect(parent.querySelector('.agent-glyph')!),
        child: rect(child.querySelector('.agent-glyph')!),
        slot: rect(child.querySelector('.tree-fold-space')!),
        row: rect(parent),
        stem: rect(parent.querySelector('.tree-stem')!),
        guide: rect(child.querySelector('.tree-guide.elbow')!),
        elbow: { top: Number.parseFloat(elbow.top), height: Number.parseFloat(elbow.height), width: Number.parseFloat(elbow.width) },
        lastHeight: Number.parseFloat(last.height),
      }
    }, { lead: lead.id, worker: worker.id })
    const close = (a: number, b: number, by = 2) => expect(Math.abs(a - b)).toBeLessThan(by)
    // Fold button first, then the glyph; the stem leaves below the button and runs to the row edge.
    expect(geometry.fold.x + geometry.fold.width).toBeLessThanOrEqual(geometry.parent.x)
    expect(geometry.stem.x).toBeGreaterThanOrEqual(geometry.fold.x)
    expect(geometry.stem.x).toBeLessThanOrEqual(geometry.fold.x + geometry.fold.width)
    if (width > 900) close(geometry.stem.x, geometry.fold.x + geometry.fold.width / 2)
    expect(geometry.stem.y).toBeGreaterThanOrEqual(geometry.parent.y + geometry.parent.height / 2)
    close(geometry.stem.bottom, geometry.row.bottom)
    close(geometry.stem.bottom, geometry.guide.y)
    close(geometry.guide.x, geometry.stem.x)
    // The elbow turns on the child's glyph centre line and ends on the child's fold centre.
    close(geometry.guide.y + geometry.elbow.top + geometry.elbow.height, geometry.child.y + geometry.child.height / 2)
    expect(Math.abs((geometry.guide.x + geometry.elbow.width) - (geometry.slot.x + geometry.slot.width / 2)), 'elbow meets fold centre').toBeLessThanOrEqual(0.5)
    expect(geometry.guide.y + geometry.lastHeight).toBeLessThan(geometry.child.y + geometry.child.height / 2)
  })
}

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`${theme} ${width}px fits without horizontal scroll and captures review image`, async ({ page }) => {
    const { worker } = await setup(page, theme, true)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto('/agents')
    await expect(page.getByRole('group', { name: 'Show sessions by state' }).locator('[data-filter="working"]')).toContainText('2working')
    await expect(page.locator(`[data-row="s:${worker.id}"] .exec-model`)).toHaveText('gpt-6-sol · xhigh')
    await expect(page.locator(`[data-row="s:${worker.id}"] .exec-account`)).toHaveText('Codex · Codex Pro')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    const pass = process.env.AM1_CAPTURE_PASS
    if (pass) {
      const dir = resolve('..', '.agent-shots')
      mkdirSync(dir, { recursive: true })
      await page.screenshot({ path: resolve(dir, `am1-${pass}-${theme}-${width}-page.png`), fullPage: true })
      await page.locator('.sessions').screenshot({ path: resolve(dir, `am1-${pass}-${theme}-${width}-sessions.png`) })
    }
    await page.goto(`/agents/${worker.id}`)
    await expect(page.locator('.activity-timeline li')).toHaveCount(2)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel.locator('section[aria-labelledby="setup-title"]')).toContainText('Codex Pro')
    await expect(panel.locator('section[aria-labelledby="work-title"]')).toContainText('tm1.session-metadata')
    expect(await panel.locator('.scroll').evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    if (pass) {
      await page.screenshot({ path: resolve('..', '.agent-shots', `am1-${pass}-${theme}-${width}-detail.png`), fullPage: true })
      await panel.locator('section[aria-labelledby="setup-title"]').evaluate(el => el.scrollIntoView({ block: 'start' }))
      await panel.screenshot({ path: resolve('..', '.agent-shots', `am1-${pass}-${theme}-${width}-setup-work.png`) })
    }
  })
}
