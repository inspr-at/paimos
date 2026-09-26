// SPDX-License-Identifier: AGPL-3.0-only
// AEON-192: mocked mission-control coverage and opt-in review captures.
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const now = Date.parse('2026-09-26T16:00:00Z')
async function setup(page: Page, theme: 'light' | 'dark' = 'light') {
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
  data.sessions.splice(0, data.sessions.length, lead, worker, stopped)
  data.runs.splice(0)
  data.approvals.splice(0)
  data.messages.splice(0)
  data.targets.splice(0)
  await mockAgents(page, data)
  return { lead, worker, stopped }
}

test('live tiles show current steps and rows nest workers with stopped history collapsed', async ({ page }) => {
  const { lead, worker, stopped } = await setup(page)
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Live now' })).toBeVisible()
  await expect(page.locator('.live-now .tile')).toHaveCount(2)
  await expect(page.locator('.live-now .tile').filter({ hasText: 'hausv' })).toContainText('Running PDF tests')
  await expect(page.locator('.live-now .tile .live-bot .robot')).toHaveCount(2)
  await expect(page.locator('.live-now .tile .live-bot .glint')).toHaveCount(0)
  await expect(page.locator(`[data-row="s:${worker.id}"]`)).toHaveAttribute('data-parent', lead.id)
  await expect(page.locator(`[data-row="s:${worker.id}"]`)).toContainText('Running PDF tests')
  await expect(page.locator(`[data-row="s:${stopped.id}"]`)).toHaveCount(0)
  await expect(page.locator('.sessions .thead')).not.toContainText(/Account|Model/)
})

test('detail shows now, activity, ticket status and hides unreported fields', async ({ page }) => {
  const { worker } = await setup(page)
  await page.goto(`/agents/${worker.id}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel).toContainText('Running PDF tests')
  await expect(panel.locator('.activity-timeline li')).toHaveCount(2)
  await expect(panel.locator('.ticket-card')).toContainText('PHAROS-12')
  await expect(panel).not.toContainText('Not reported')
  await expect(panel).not.toContainText('Account not reported')
  await expect(panel.getByRole('button', { name: /Interrupt/ })).toBeVisible()
  await expect(panel.getByRole('button', { name: /Stop/ })).toBeVisible()
})

for (const width of [1600, 390]) {
  test(`tree guide meets the child glyph at ${width}px`, async ({ page }) => {
    const { lead, worker } = await setup(page)
    await page.setViewportSize({ width, height: 1000 })
    await page.goto('/agents')
    await expect(page.locator(`[data-row="s:${worker.id}"]`)).toBeVisible()
    const geometry = await page.evaluate(({ lead, worker }) => {
      const parent = document.querySelector(`[data-row="s:${lead}"]`)!
      const child = document.querySelector(`[data-row="s:${worker}"]`)!
      const badge = (row: Element) => row.querySelector('.agent-glyph')!.getBoundingClientRect()
      const stem = parent.querySelector('.tree-stem')!.getBoundingClientRect()
      const guide = child.querySelector('.tree-guide.elbow')!.getBoundingClientRect()
      const elbow = getComputedStyle(child.querySelector('.tree-guide.elbow')!, '::after')
      const last = getComputedStyle(child.querySelector('.tree-guide.elbow')!, '::before')
      const toggle = parent.querySelector('.worker-toggle')!.getBoundingClientRect()
      return {
        parent: { x: badge(parent).x, y: badge(parent).y, width: badge(parent).width, height: badge(parent).height },
        child: { x: badge(child).x, y: badge(child).y, width: badge(child).width, height: badge(child).height },
        stem: { x: stem.x, y: stem.y, bottom: stem.bottom, height: stem.height },
        guide: { x: guide.x, y: guide.y, bottom: guide.bottom },
        elbow: { top: Number.parseFloat(elbow.top), height: Number.parseFloat(elbow.height), width: Number.parseFloat(elbow.width) },
        lastHeight: Number.parseFloat(last.height),
        toggle: { x: toggle.x, width: toggle.width },
      }
    }, { lead: lead.id, worker: worker.id })
    const close = (a: number, b: number) => expect(Math.abs(a - b)).toBeLessThan(2)
    close(geometry.stem.x, geometry.parent.x + geometry.parent.width / 2)
    close(geometry.stem.y, geometry.parent.y + geometry.parent.height)
    close(geometry.stem.bottom, geometry.guide.y)
    close(geometry.guide.y + geometry.elbow.top + geometry.elbow.height, geometry.child.y + geometry.child.height / 2)
    close(geometry.guide.x + geometry.elbow.width, geometry.child.x)
    expect(geometry.toggle.x).toBeGreaterThan(geometry.parent.x + geometry.parent.width)
    expect(geometry.guide.y + geometry.lastHeight).toBeLessThan(geometry.child.y + geometry.child.height / 2)
  })
}

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`${theme} ${width}px fits without horizontal scroll and captures review image`, async ({ page }) => {
    const { worker } = await setup(page, theme)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto('/agents')
    await expect(page.locator('.live-now .tile')).toHaveCount(2)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    const pass = process.env.AM1_CAPTURE_PASS
    if (pass) {
      const dir = resolve('..', '.agent-shots')
      mkdirSync(dir, { recursive: true })
      await page.screenshot({ path: resolve(dir, `am1-${pass}-${theme}-${width}-page.png`), fullPage: true })
    }
    await page.goto(`/agents/${worker.id}`)
    await expect(page.locator('.activity-timeline li')).toHaveCount(2)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    if (pass) await page.screenshot({ path: resolve('..', '.agent-shots', `am1-${pass}-${theme}-${width}-detail.png`), fullPage: true })
  })
}
