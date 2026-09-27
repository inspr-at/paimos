// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, liveAgent, mockWork, watchErrors, type Call, type Fixtures } from './work-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const at = Date.parse('2026-09-23T12:00:00Z')
const ticket = (id: string, key: string, title: string, project_id = 'p-pharos') => ({ id, key, title, project_id })
const grid = (page: Page) => page.getByRole('grid', { name: 'Tickets' })
const row = (page: Page, key: string) => grid(page).locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const liveCalls = (calls: Call[]) => calls.filter(call => call.path === '/api/harness-sessions/live')

function withWorkers(data: Fixtures = fixtures()) {
  data.live.push(
    liveAgent({ project_id: 'p-pharos', session_id: 's-fault', principal_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', name: 'fault', has_problem: true, ticket: ticket('n-epic', 'PHAROS-10', 'Guarded multi-cloud provisioning') }),
    liveAgent({ project_id: 'p-pharos', session_id: 's-hausv', principal_id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', name: 'hausv', ticket: ticket('n-1', 'PHAROS-11', 'Connect Hetzner Cloud for managed provisioning') }),
    liveAgent({ project_id: 'p-pharos', session_id: 's-wren', principal_id: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', name: 'wren', needs_attention: true, ticket: ticket('n-2', 'PHAROS-12', 'Add an Oracle Cloud connector') }),
    liveAgent({ project_id: 'p-pharos', name: undefined, harness: 'codex', ticket: ticket('n-2', 'PHAROS-12', 'Add an Oracle Cloud connector') }),
    liveAgent({ project_id: 'p-pharos', session_id: 's-retired', name: 'retired', phase: 'stopped', activity: 'idle', stopped_at: new Date(at - 60_000).toISOString(), stop_reason: 'completed', ticket: ticket('n-2', 'PHAROS-12', 'Add an Oracle Cloud connector') }),
    liveAgent({ project_id: 'p-pharos', session_id: 's-quiet', name: 'quiet', activity: 'idle', heartbeat_at: new Date(at - 10 * 60_000).toISOString(), ticket: ticket('n-3', 'PHAROS-13', 'Run the disposable Hetzner end-to-end check') }),
    liveAgent({ project_id: 'p-pharos', session_id: 's-nova', principal_id: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd', name: 'nova', ticket: ticket('n-4', 'PHAROS-14', 'Visual acceptance of the version pill') }),
    liveAgent({ project_id: 'p-aeon', session_id: 's-leak', name: 'aeon-leak', ticket: ticket('n-4', 'PHAROS-14', 'Visual acceptance of the version pill') }),
    liveAgent({ project_id: 'p-pharos', session_id: 's-moved', name: 'moved-off', ticket: ticket('n-a1', 'AEON-1', 'Aeon foundation', 'p-aeon') }),
  )
  return data
}

test('assignee shows the live worker beside a human owner, and one feed serves every row', async ({ page }) => {
  const errors = watchErrors(page)
  const calls = await mockWork(page, withWorkers())
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto('/p/PHAROS')
  await expect(row(page, 'PHAROS-14').locator('.c-assignee')).toContainText('nova')
  await expect(row(page, 'PHAROS-14').locator('.c-assignee .empty')).toHaveCount(0)
  await expect(row(page, 'PHAROS-14').getByRole('button', { name: /more workers/ })).toHaveCount(0)
  const owned = row(page, 'PHAROS-11').locator('.c-assignee')
  await expect(owned).toContainText('Markus Barta')
  await expect(owned).toContainText('hausv')
  await expect(row(page, 'PHAROS-10').getByRole('link', { name: /fault/ })).toHaveAccessibleName(/problem/i)
  await expect(row(page, 'PHAROS-13').locator('.live-bot')).toHaveAttribute('data-state', 'stale')
  await expect(row(page, 'PHAROS-13').getByRole('link', { name: /quiet/ })).toHaveAccessibleName(/idle/i)
  await expect(row(page, 'PHAROS-13').getByRole('link', { name: /quiet/ })).not.toHaveAccessibleName(/\bworking\b/i)
  await expect(page.getByText('retired')).toHaveCount(0)
  await expect(page.getByText('aeon-leak')).toHaveCount(0)
  await expect(page.getByText('moved-off')).toHaveCount(0)
  await expect(row(page, 'PHAROS-14').locator('.title-workers')).toHaveCount(0)
  expect(liveCalls(calls)).toHaveLength(1)
  expect(liveCalls(calls)[0]!.query.get('include_inactive')).toBe('true')
  const heights = await grid(page).locator('tr.ticket-row:not(.ghost)').evaluateAll(els => els.map(el => Math.round(el.getBoundingClientRect().height)))
  expect(new Set(heights).size).toBe(1)
  expect(errors).toEqual([])
})

test('several workers disclose each state, and the control does not open the ticket', async ({ page }) => {
  const calls = await mockWork(page, withWorkers(), { liveTruncated: true })
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto('/p/PHAROS')
  const line = row(page, 'PHAROS-12')
  await expect(line.locator('.c-assignee')).toContainText('wren')
  await line.getByRole('button', { name: '1 more worker on PHAROS-12. Show each worker' }).click()
  await expect(page).toHaveURL(/\/p\/PHAROS(\/tickets)?\/?$/)
  await expect(page).not.toHaveURL(/PHAROS-12/)
  await expect(page.locator('tr.ticket-row.open')).toHaveCount(0)
  const dialog = page.getByRole('dialog', { name: 'Workers on PHAROS-12' })
  await expect(dialog).toContainText('wren')
  await expect(dialog).toContainText('Codex agent')
  await expect(dialog.getByText('Needs something')).toBeVisible()
  await expect(dialog.getByText('Working')).toBeVisible()
  await expect(dialog.getByText('Session details are withheld')).toBeVisible()
  await expect(dialog.getByText('retired')).toHaveCount(0)
  await expect(dialog.getByText('More live sessions were left out of this update.')).toBeVisible()
  await dialog.getByRole('link', { name: /wren/ }).click()
  await expect(page).toHaveURL(/\/agents\/s-wren$/)
  expect(liveCalls(calls)).toHaveLength(1)
})

test('a single worker opens its session and leaves the ticket row alone', async ({ page }) => {
  await mockWork(page, withWorkers())
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto('/p/PHAROS')
  await row(page, 'PHAROS-14').getByRole('link', { name: /nova/ }).click()
  await expect(page).toHaveURL(/\/agents\/s-nova$/)
  await expect(page).not.toHaveURL(/PHAROS-14/)
})

test('a forbidden live read invents no workers', async ({ page }) => {
  const errors = watchErrors(page)
  await mockWork(page, withWorkers(), { liveStatus: 403 })
  await page.goto('/p/PHAROS')
  await expect(row(page, 'PHAROS-11').locator('.c-assignee')).toContainText('Markus Barta')
  await expect(row(page, 'PHAROS-14').locator('.c-assignee')).toHaveText('—')
  await expect(page.getByText('nova')).toHaveCount(0)
  expect(errors).toEqual([])
})

test('active workers earn the Assignee column when nobody is assigned, and a saved choice still hides it', async ({ page }) => {
  const open = withWorkers()
  for (const node of open.nodes) delete node.fields.assignee
  const calls = await mockWork(page, open)
  await page.setViewportSize({ width: 1400, height: 900 })
  await page.goto('/p/PHAROS')
  await expect(page.getByRole('columnheader', { name: 'Assignee' })).toBeVisible()
  await expect(row(page, 'PHAROS-14').locator('.c-assignee')).toContainText('nova')

  const saved = withWorkers()
  saved.preferences['list:p-pharos'] = { visible: ['status', 'priority', 'updated'] }
  const savedCalls = await mockWork(page, saved)
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto('/p/PHAROS')
  await expect(page.getByRole('columnheader', { name: 'Assignee' })).toHaveCount(0)
  await expect(row(page, 'PHAROS-14').locator('.title-workers')).toContainText('nova')
  expect(savedCalls.filter(call => call.method === 'PUT' && call.path.includes('/api/preferences/list:'))).toHaveLength(0)
  expect(calls.filter(call => call.path === '/api/harness-sessions/live').length).toBeGreaterThan(0)
})

test('a narrow list keeps a compact worker cue in the title', async ({ page }) => {
  await mockWork(page, withWorkers())
  await page.setViewportSize({ width: 800, height: 800 })
  await page.goto('/p/PHAROS')
  await expect(page.getByRole('columnheader', { name: 'Assignee' })).toHaveCount(0)
  await expect(row(page, 'PHAROS-14').locator('.title-workers')).toContainText('nova')
  await row(page, 'PHAROS-14').locator('.title-workers').getByRole('link', { name: /nova/ }).click()
  await expect(page).toHaveURL(/\/agents\/s-nova$/)
})

test('rebinding follows the next poll, and leaving the list stops it', async ({ page }) => {
  const data = withWorkers()
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS')
  await expect(row(page, 'PHAROS-14').locator('.c-assignee')).toContainText('nova')
  const nova = data.live.find(agent => agent.session_id === 's-nova')!
  nova.ticket = ticket('n-1', 'PHAROS-11', 'Connect Hetzner Cloud for managed provisioning')
  await page.clock.fastForward(21_000)
  await expect(row(page, 'PHAROS-14').locator('.c-assignee')).toHaveText('—')
  await row(page, 'PHAROS-11').getByRole('button', { name: /more worker on PHAROS-11/ }).click()
  await expect(page.getByRole('dialog', { name: 'Workers on PHAROS-11' })).toContainText('nova')
  await expect(page).not.toHaveURL(/PHAROS-11/)
  await page.goto('/p/AEON')
  await expect(page.getByRole('heading', { name: 'Aeon', level: 1 })).toBeVisible()
  await expect(page.getByText('nova')).toHaveCount(0)
  const before = liveCalls(calls).length
  await page.goto('/settings/personal')
  await expect(page.getByRole('heading', { name: 'Settings', level: 1 })).toBeVisible()
  const parked = liveCalls(calls).length
  await page.clock.fastForward(25_000)
  expect(liveCalls(calls).length).toBe(parked)
  expect(before).toBeLessThan(8)
})

test('screenshots and two self-inspections at 1600 and 390', async ({ page }) => {
  mkdirSync('../.agent-shots', { recursive: true })
  await mockWork(page, withWorkers())
  for (const pass of [1, 2]) {
    for (const colorScheme of ['light', 'dark'] as const) {
      for (const width of [1600, 390] as const) {
        await page.setViewportSize({ width, height: width === 1600 ? 1000 : 844 })
        await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
        await page.goto('/p/PHAROS')
        await expect(row(page, 'PHAROS-14').getByRole('link', { name: /nova/ })).toBeVisible()
        const sample = row(page, 'PHAROS-14')
        const border = await sample.evaluate(el => getComputedStyle(el).borderLeftWidth)
        expect(border).toBe('0px')
        expect(await sample.getAttribute('data-agent-state')).toBeNull()
        if (width === 1600) {
          await expect(sample.locator('.c-assignee')).toContainText('nova')
          await expect(sample.locator('.title-workers')).toHaveCount(0)
          const heights = await grid(page).locator('tr.ticket-row:not(.ghost)').evaluateAll(els => els.map(el => Math.round(el.getBoundingClientRect().height)))
          expect(new Set(heights).size, `pass ${pass} ${colorScheme}`).toBe(1)
        } else {
          await expect(page.getByRole('columnheader', { name: 'Assignee' })).toHaveCount(0)
          await expect(sample.locator('.title-workers')).toContainText('nova')
        }
        await page.screenshot({ path: `../.agent-shots/ta1-ticket-workers-${colorScheme}-${width}.png`, fullPage: true })
      }
    }
  }
})
