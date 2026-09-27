// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import type { MetadataChange } from '../src/lib/agents'

const now = Date.parse('2026-09-27T10:00:00Z')
const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString()

async function setup(page: Page, theme: 'light' | 'dark') {
  await page.clock.install({ time: now })
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const data = agentData({
    now, me: me.id,
    projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
    tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
    nodes: { 'p-pharos': { key: 'PHAROS', title: 'Pharos' }, 'n-2': { key: 'PHAROS-12', title: 'PDF worker image' } },
  })
  const worker = data.sessions[1]!
  Object.assign(worker, {
    display_label: 'pdf worker', model: 'gpt-6-sol', reasoning_effort: 'xhigh', account_label: 'Codex Pro',
  })
  const history: MetadataChange[] = [
    { field: 'model', previous_value: 'gpt-6-luna', value: 'gpt-6-sol', at: ago(5) },
    { field: 'reasoning_effort', previous_value: 'medium', value: 'xhigh', at: ago(2) },
    { field: 'display_label', previous_value: 'hausv', value: 'pdf worker', at: ago(1) },
  ]
  data.sessions.splice(0, data.sessions.length, worker)
  data.runs.splice(0)
  data.approvals.splice(0)
  data.messages.splice(0)
  await mockAgents(page, data)
  const detail = { requests: 0 }
  // Production SN1 exposes metadata_history only on this detail endpoint.
  await page.route(`**/api/projects/${worker.project_id}/harness-sessions/${worker.id}`, route => {
    detail.requests++
    return route.fulfill({ json: { ...worker, metadata_history: history } })
  })
  return { worker, history, detail }
}

test('list shows current metadata and detail supplies the change history', async ({ page }) => {
  const { worker, detail } = await setup(page, 'light')
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto('/agents')
  const row = page.locator(`[data-row="s:${worker.id}"]`)
  await expect(row.locator('.session-meta')).toHaveText('Codex · gpt-6-sol · xhigh · Codex Pro')
  await expect(row.locator('.meta-change')).toHaveCount(0)
  expect(detail.requests).toBe(0)
  await page.goto(`/agents/${worker.id}`)
  const changes = page.getByRole('complementary', { name: 'Session details' }).locator('.metadata-history')
  await expect(changes.getByRole('listitem')).toHaveText([
    /Name hausv to pdf worker/,
    /Effort medium to xhigh/,
    /Model gpt-6-luna to gpt-6-sol/,
  ])
  expect(detail.requests).toBe(1)
  const borderLeft = await changes.locator('li').first().evaluate(element => getComputedStyle(element).borderLeftWidth)
  expect(borderLeft).toBe('0px')
})

test('an open panel refreshes metadata history after a heartbeat without an activity-note change', async ({ page }) => {
  const { worker, history, detail } = await setup(page, 'light')
  await page.goto(`/agents/${worker.id}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  const changes = panel.getByRole('list', { name: 'Recent session changes' })
  await expect(changes).toContainText('Model gpt-6-luna to gpt-6-sol')
  expect(detail.requests).toBe(1)
  const unchangedNote = worker.activity_note
  Object.assign(worker, { model: 'gpt-6-astra', reasoning_effort: 'high', display_label: 'release worker', heartbeat_at: new Date(now + 10_000).toISOString() })
  history.push({ field: 'model', previous_value: 'gpt-6-sol', value: 'gpt-6-astra', at: new Date(now + 10_000).toISOString() })
  history.push({ field: 'reasoning_effort', previous_value: 'xhigh', value: 'high', at: new Date(now + 10_001).toISOString() })
  expect(worker.activity_note).toBe(unchangedNote)
  await page.clock.runFor(20_100)
  await expect(panel.getByRole('heading', { name: 'release worker', exact: true })).toBeVisible()
  await expect(changes).toContainText('Model gpt-6-sol to gpt-6-astra')
  await expect(changes).toContainText('Effort xhigh to high')
  await expect(panel.locator('.fact').filter({ has: page.locator('dt', { hasText: /^Model$/ }) })).toContainText('gpt-6-astra')
  expect(detail.requests).toBe(2)
  // An unchanged list poll must not add one detail request per clock tick/row.
  await page.clock.runFor(20_100)
  expect(detail.requests).toBe(2)
})

for (const pass of [1, 2]) for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`change history fits at ${width}px in ${theme}, pass ${pass}`, async ({ page }) => {
    const { worker } = await setup(page, theme)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto('/agents')
    const row = page.locator(`[data-row="s:${worker.id}"]`)
    await expect(row.locator('.session-meta')).toContainText('gpt-6-sol · xhigh')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    const dir = resolve('..', '.agent-shots')
    mkdirSync(dir, { recursive: true })
    await page.screenshot({ path: resolve(dir, `sm1-pass${pass}-${theme}-${width}-list.png`), fullPage: true })
    await page.goto(`/agents/${worker.id}`)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    const changes = panel.locator('.metadata-history')
    await expect(changes).toContainText('Model gpt-6-luna to gpt-6-sol')
    await changes.scrollIntoViewIfNeeded()
    expect(await panel.locator('.scroll').evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true)
    await panel.screenshot({ path: resolve(dir, `sm1-pass${pass}-${theme}-${width}-detail.png`) })
  })
}
