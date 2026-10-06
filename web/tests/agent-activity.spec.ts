// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { businessData, mockBusiness } from './business-fixtures'

const now = Date.parse('2026-10-01T21:00:00Z')
const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString()
async function agents(page: Page, off = false, theme: 'light' | 'dark' = 'light') {
  await page.clock.install({ time: now })
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: { 'p-pharos': { key: 'PHAROS', title: 'Pharos' }, 'n-2': { key: 'PHAROS-12', title: 'Activity reporting' } } })
  const worker = data.sessions[1]!
  Object.assign(worker, { display_label: 'Activity worker', agent_activity_mode: off ? 'off' : 'agent_summary', current_activity: off ? null : { text: 'Running Go tests', source: 'auto', at: ago(1) } })
  data.sessions.splice(0, data.sessions.length, worker); data.runs.splice(0); data.approvals.splice(0); data.messages.splice(0)
  await mockAgents(page, data)
  await page.route(`**/api/projects/${worker.project_id}/harness-sessions/${worker.id}`, route => route.fulfill({ json: { ...worker, current_activity_history: off ? [] : [{ text: 'Committing', source: 'auto', at: ago(5) }, { text: 'Running Go tests', source: 'auto', at: ago(17) }] } }))
  return worker
}

test('table and details show activity and phase durations; Off hides both', async ({ page }) => {
  const worker = await agents(page)
  await page.goto('/agents')
  await expect(page.locator(`[data-row="s:${worker.id}"] .current-activity`)).toHaveText('Running Go tests')
  await page.goto(`/agents/${worker.id}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel.locator('.now-step')).toHaveText('Running Go tests')
  await expect(panel.getByRole('list', { name: 'Current activity history' }).getByRole('listitem')).toHaveText([/5 min.*Committing/, /12 min.*Running Go tests/])
})

test('Off shows no activity line or current activity history', async ({ page }) => {
  const worker = await agents(page, true)
  await page.goto('/agents')
  await expect(page.locator(`[data-row="s:${worker.id}"]`)).toBeVisible()
  await expect(page.locator('.current-activity')).toHaveCount(0)
  await page.goto(`/agents/${worker.id}`)
  await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  await expect(page.getByRole('list', { name: 'Current activity history' })).toHaveCount(0)
})

for (const text of ['AKIAIOSFODNN7EXAMPLE', 'https://user:pass@host/a', 'FOO=secret', 'Editing AKIAIOSFODNN7EXAMPLE.go', 'Editing sk-live.go', 'Editing id-rsa.go', 'A\u0301KIAIOSFODNN7EXAMPLE']) {
  test(`credential text is hidden in current activity and legacy fallback: ${text}`, async ({ page }) => {
    const worker = await agents(page)
    for (const current of [null, { text, source: 'auto', at: ago(1) }]) {
      Object.assign(worker, { activity_note: text, current_activity: current })
      await page.goto('/agents')
      await expect(page.locator(`[data-row="s:${worker.id}"]`)).toBeVisible()
      await expect(page.locator(`[data-row="s:${worker.id}"] .current-activity`)).toHaveCount(0)
      await page.goto(`/agents/${worker.id}`)
      const panel = page.getByRole('complementary', { name: 'Session details' })
      await expect(panel).toBeVisible()
      await expect(panel.locator('.now-step')).not.toContainText(text)
    }
  })
}

test('workspace administrators can choose each activity mode and recover a failed save', async ({ page }) => {
  await mockWork(page, fixtures(), { admin: true })
  await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
  await mockSettings(page, settingsData())
  let mode = 'agent_summary'; const writes: string[] = []; let fail = false
  await page.route('**/api/settings/agent-activity', route => {
    if (route.request().method() === 'PUT') {
      if (fail) return route.fulfill({ status: 503, json: { error: 'unavailable' } })
      mode = route.request().postDataJSON().mode; writes.push(mode)
    }
    return route.fulfill({ json: { mode } })
  })
  await page.goto('/settings/agents')
  const options = page.getByRole('radiogroup', { name: 'Agent activity' })
  await expect(options.getByRole('radio', { name: /^Agent summary/ })).toBeChecked()
  await options.getByRole('radio', { name: /^Tool activity/ }).check()
  await expect(options.getByRole('radio', { name: /^Tool activity/ })).toBeChecked()
  await options.getByRole('radio', { name: /^Off/ }).check()
  await expect(options.getByRole('radio', { name: /^Off/ })).toBeChecked()
  fail = true
  await options.getByRole('radio', { name: /^Agent summary/ }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'Could not save' })).toBeVisible()
  await expect(options.getByRole('radio', { name: /^Agent summary/ })).toBeChecked()
  expect(writes).toEqual(['tool_activity', 'off'])
})

test('activity fits phone and desktop in both themes', async ({ browser }) => {
  const shots = process.env.AGENT_ACTIVITY_SHOTS ?? resolve('..', '.agent-shots', 'agent-activity')
  mkdirSync(shots, { recursive: true })
  for (const theme of ['light', 'dark'] as const) for (const width of [390, 1600]) {
    const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 1000 }, colorScheme: theme })
    const page = await context.newPage(); const worker = await agents(page, false, theme)
    await page.goto('/agents')
    await expect(page.locator(`[data-row="s:${worker.id}"] .current-activity`)).toBeVisible()
    await page.locator(`[data-row="s:${worker.id}"] .current-activity`).scrollIntoViewIfNeeded()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    await page.screenshot({ path: resolve(shots, `${theme}-${width}-list.png`), fullPage: true })
    await page.goto(`/agents/${worker.id}`)
    await expect(page.getByRole('list', { name: 'Current activity history' })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    await page.screenshot({ path: resolve(shots, `${theme}-${width}-detail.png`), fullPage: true })
    await context.close()
  }
})
