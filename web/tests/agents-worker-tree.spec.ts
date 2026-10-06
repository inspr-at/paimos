// SPDX-License-Identifier: AGPL-3.0-only
// Coordinator: PLAYWRIGHT_PORT=5835 WT1_SCREENSHOT_DIR=<scratchpad> npm test -- agents-worker-tree.spec.ts agents-hierarchy.spec.ts
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import type { HarnessSession } from '../src/lib/agents'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const now = Date.parse('2026-09-26T16:00:00Z')
const ago = (seconds: number) => new Date(now - seconds * 1000).toISOString()
const id = (n: number) => `5e000000-0000-4000-8000-${String(n).padStart(12, '0')}`
const lead = id(1)
const row = (page: Page, key: string) => page.locator(`[data-row="s:${key}"]`)
const children = (page: Page, key = lead) => page.locator(`.sessions .row[data-parent="${key}"]`)
const order = (page: Page, key = lead) => children(page, key).evaluateAll(rows => rows.map(r => r.getAttribute('data-row')!.slice(2)))
const history = (page: Page, key = lead) => row(page, key).locator('.history-toggle')
// Every parent has its own fold button (AEON-784); its name says what is under it.
const expand = (page: Page, key = lead) => row(page, key).locator('.tree-fold')

async function refresh(page: Page) {
  const response = page.waitForResponse(r => new URL(r.url()).pathname === '/api/harness-sessions')
  await page.evaluate(() => window.dispatchEvent(new Event('test:worker-refresh')))
  await response
}

async function setup(page: Page, theme: 'light' | 'dark' = 'light') {
  await page.clock.install({ time: now })
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  await page.addInitScript(() => {
    class Stream extends EventTarget {
      onopen: (() => void) | null = null
      onerror: (() => void) | null = null
      receive = () => this.dispatchEvent(new Event('harness.stopped'))
      constructor() { super(); window.addEventListener('test:worker-refresh', this.receive) }
      close() { window.removeEventListener('test:worker-refresh', this.receive) }
    }
    Object.assign(window, { EventSource: Stream })
  })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  // The shared fixture factory exposes narrower types than its wire payload.
  const base = data.sessions[0]! as unknown as HarnessSession
  const session = (n: number, fields: Record<string, unknown> = {}) => ({
    ...base, id: id(n), role: 'worker', parent_harness_session_id: lead, display_label: `worker-${n}`,
    agent: { id: base.agent_principal_id, name: 'aeon-coordinator' },
    phase: 'working', activity: 'busy', heartbeat_at: ago(1), created_at: ago(600), ...fields,
  }) as unknown as typeof data.sessions[number]
  data.sessions.splice(0, data.sessions.length,
    session(1, { parent_harness_session_id: null, role: 'coordinator', display_label: 'Release lead', created_at: ago(1800) }),
    // Oldest starts and newest heartbeats belong to history: neither may lead.
    ...Array.from({ length: 8 }, (_, index) => session(10 + index, { phase: 'stopped', stopped_at: ago(index), heartbeat_at: ago(index), created_at: ago(1200 + index) })),
    session(2, { display_label: 'ss1-handoff-supersession', heartbeat_at: ago(20) }),
    session(3, { display_label: 'Starting worker', phase: 'starting', heartbeat_at: ago(5) }),
    session(4, { display_label: 'Permission checks', heartbeat_at: ago(10) }),
    session(5, { display_label: 'Tree connectors', heartbeat_at: ago(15) }),
  )
  data.approvals.splice(0)
  data.messages.splice(0)
  data.targets.splice(0)
  await mockAgents(page, data)
  return { data, session }
}

test('four live workers lead eight collapsed stopped workers, and labels distinguish their shared principal', async ({ page }) => {
  const { data } = await setup(page)
  await page.goto('/agents')
  await expect(expand(page)).toHaveAttribute('aria-label', 'Fold Release lead: 4 sub-agents, 4 working')
  await expect(history(page)).toHaveText('8 stopped')
  await expect(row(page, lead).locator('.worker-tools')).toHaveText('8 stopped')
  await expect(history(page)).toHaveAttribute('aria-expanded', 'false')
  // Equal start times: session ID decides, never the latest heartbeat (AEON-468).
  await expect.poll(() => order(page)).toEqual([id(2), id(3), id(4), id(5)])
  await expect(row(page, id(2)).getByRole('link', { name: 'Claude ss1-handoff-supersession, Working' })).toBeVisible()
  await expect(children(page)).not.toContainText(['aeon-coordinator'])
  await history(page).focus()
  await page.keyboard.press('Enter')
  await expect(history(page)).toHaveAttribute('aria-expanded', 'true')
  await expect.poll(() => order(page)).toEqual([id(2), id(3), id(4), id(5), ...Array.from({ length: 8 }, (_, n) => id(10 + n))])
  await expand(page).click()
  await expect(children(page)).toHaveCount(0)
  await expect(row(page, lead).locator('.kid-count')).toHaveText('12 sub-agents')
  await expand(page).click()
  await expect(children(page)).toHaveCount(12)
  // A fresh heartbeat on the last worker leaves every row where it is.
  Object.assign(data.sessions.find(s => s.id === id(5))!, { heartbeat_at: ago(0) })
  await refresh(page)
  expect(await order(page)).toEqual([id(2), id(3), id(4), id(5), ...Array.from({ length: 8 }, (_, n) => id(10 + n))])
  await expect(history(page)).toHaveAttribute('aria-expanded', 'true')
  await history(page).focus()
  await page.keyboard.press('Space')
  await expect(children(page)).toHaveCount(4)
  await expect(history(page)).toBeFocused()
  await history(page).click()
  await page.reload()
  await expect(children(page)).toHaveCount(4)
  await expect(history(page)).toHaveAttribute('aria-expanded', 'false')
})

test('idle follows working; newly stopped workers hide immediately and histories stay independent', async ({ page }) => {
  const { data, session } = await setup(page)
  data.sessions.push(session(90, { role: 'coordinator', parent_harness_session_id: null, display_label: 'Second lead' }), session(91, { parent_harness_session_id: id(90), phase: 'stopped', stopped_at: ago(0) }))
  Object.assign(data.sessions.find(s => s.id === id(4))!, { activity: 'idle', heartbeat_at: ago(0) })
  await page.goto('/agents')
  await expect.poll(() => order(page)).toEqual([id(2), id(3), id(5), id(4)])
  await expect(expand(page)).toHaveAttribute('aria-label', 'Fold Release lead: 4 sub-agents, 3 working')
  await history(page).click()
  await expect(children(page, id(90))).toHaveCount(0)
  await history(page, id(90)).click()
  await history(page).click()
  Object.assign(data.sessions.find(s => s.id === id(5))!, { phase: 'stopped', stopped_at: ago(0) })
  await refresh(page)
  await expect(row(page, id(5))).toHaveCount(0)
  await expect(history(page)).toHaveText('9 stopped')
  await expect(expand(page)).toHaveAttribute('aria-label', 'Fold Release lead: 3 sub-agents, 2 working')
  await expect(children(page, id(90))).toHaveCount(1)
})

test('a lead with only stopped workers opens history only through its stopped count', async ({ page }) => {
  const { data } = await setup(page)
  for (const worker of data.sessions.filter(s => s.id !== lead)) Object.assign(worker, { phase: 'stopped', stopped_at: ago(0) })
  await page.goto('/agents')
  await expect(children(page)).toHaveCount(0)
  // Nothing live to fold: no fold button, only the stopped count.
  await expect(expand(page)).toHaveCount(0)
  await expect(history(page)).toHaveText('12 stopped')
  await history(page).click()
  await expect(children(page)).toHaveCount(12)
  await history(page).click()
  await expect(children(page)).toHaveCount(0)
})

test('nested live workers stay visible through stopped parents and each level closes its last connector', async ({ page }) => {
  const { data, session } = await setup(page)
  Object.assign(data.sessions.find(s => s.id === id(3))!, { phase: 'stopped', stopped_at: ago(0) })
  data.sessions.push(session(100, { parent_harness_session_id: id(3), display_label: 'Nested scout' }), session(101, { parent_harness_session_id: id(3), phase: 'stopped', stopped_at: ago(0) }))
  await page.goto('/agents')
  await expect(row(page, id(100))).toHaveAttribute('data-depth', '2')
  await expect(row(page, id(101))).toHaveCount(0)
  await expect(row(page, id(100)).locator('.tree-guide.continues')).toHaveCount(1)
  await expect(row(page, id(100)).locator('.tree-guide.elbow.last')).toHaveCount(1)
  await expect(row(page, id(5)).locator('.tree-guide.elbow.last')).toHaveCount(1)
  await history(page, id(3)).click()
  await expect(row(page, id(101))).toBeVisible()
  await expect(row(page, id(100)).locator('.tree-guide.elbow.last')).toHaveCount(0)
  await expect(row(page, id(101)).locator('.tree-guide.elbow.last')).toHaveCount(1)
  await expect(row(page, id(10))).toHaveCount(0)
  await expand(page, id(3)).click()
  await expect(row(page, id(100))).toHaveCount(0)
  await expect(row(page, id(3)).locator('.tree-stem')).toHaveCount(0)
  await page.goto(`/agents/${id(100)}`)
  await expect(row(page, id(100))).toBeVisible()
})

// AEON-784 risk: depth hides trouble and the keyboard cannot reach it. A problem
// three levels down marks every ancestor and is named on folded parents; ← and →
// fold, unfold and walk; the rows say their level and place to assistive tech.
test('a deep problem marks every ancestor, folded parents name it, and arrow keys fold and walk the tree', async ({ page }) => {
  const { data, session } = await setup(page)
  data.sessions.push(session(100, { parent_harness_session_id: id(2), display_label: 'Mid lead' }), session(200, { parent_harness_session_id: id(100), display_label: 'Deep worker', has_problem: true }))
  await page.goto('/agents')
  await expect(page.getByRole('treegrid', { name: 'Agent sessions' })).toBeVisible()
  await expect(row(page, id(200))).toHaveAttribute('aria-level', '4')
  await expect(row(page, id(2))).toHaveAttribute('aria-expanded', 'true')
  await expect(row(page, id(4))).toHaveAttribute('aria-posinset', '3')
  await expect(row(page, id(4))).toHaveAttribute('aria-setsize', '4')
  await expect(row(page, id(4))).not.toHaveAttribute('aria-expanded', /.*/)
  // The family sits with its most urgent member; only ancestors carry the dot.
  await expect(page.locator('.group-row.attention + .row')).toHaveAttribute('data-row', `s:${lead}`)
  for (const key of [lead, id(2), id(100)]) await expect(row(page, key).locator('.roll-dot')).toHaveCount(1)
  for (const key of [id(200), id(3)]) await expect(row(page, key).locator('.roll-dot')).toHaveCount(0)

  await row(page, id(100)).focus()
  await page.keyboard.press('ArrowLeft')
  await expect(row(page, id(200))).toHaveCount(0)
  await expect(row(page, id(100))).toHaveAttribute('aria-expanded', 'false')
  await expect(row(page, id(100)).locator('.kid-count')).toHaveText('1 sub-agent')
  await expect(row(page, id(100)).locator('.roll.problem')).toHaveText('1 problem below')
  await expect(row(page, id(100))).toBeFocused()
  await page.keyboard.press('ArrowLeft')
  await expect(row(page, id(2))).toBeFocused()
  await page.keyboard.press('ArrowLeft')
  await expect(row(page, id(100))).toHaveCount(0)
  await expect(row(page, id(2)).locator('.kid-count')).toHaveText('2 sub-agents')
  await expect(row(page, id(2)).locator('.roll.problem')).toHaveText('1 problem below')
  await page.keyboard.press('ArrowRight')
  await expect(row(page, id(100))).toBeVisible()
  await expect(row(page, id(2))).toBeFocused()
  await page.keyboard.press('ArrowRight')
  await expect(row(page, id(100))).toBeFocused()
  await page.keyboard.press('ArrowRight')
  await page.keyboard.press('ArrowRight')
  await expect(row(page, id(200))).toBeFocused()
  // j moves over visible rows, so it leaves the deep row for the next sibling family.
  await page.keyboard.press('j')
  await expect(row(page, id(3))).toBeFocused()
  // Folding with the button from deep below takes the cursor to the folded parent.
  await expand(page, id(2)).click()
  await expect(row(page, id(100))).toHaveCount(0)
  await expect(expand(page, id(2))).toBeFocused()
  await expect(row(page, id(2))).toHaveClass(/\bactive\b/)
})

for (const theme of ['light', 'dark'] as const) {
  for (const width of [1440, 390]) {
    test(`worker tree fits ${width}px in ${theme}, passes axe, and captures coordinator evidence`, async ({ page }, testInfo) => {
      await page.setViewportSize({ width, height: 960 })
      await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
      const { data, session } = await setup(page, theme)
      data.sessions.push(session(100, { parent_harness_session_id: id(3), display_label: 'Nested worker with a long descriptive label' }))
      await page.goto('/agents')
      await expect(row(page, id(100))).toBeVisible()
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
      const dir = process.env.WT1_SCREENSHOT_DIR ?? testInfo.outputDir
      mkdirSync(dir, { recursive: true })
      for (const state of ['live', 'history']) {
        if (state === 'history') await history(page).click()
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
        expect(await page.locator('.sessions .row').evaluateAll(rows => rows.filter(row => row.scrollWidth > row.clientWidth + 1).map(row => row.getAttribute('data-row')))).toEqual([])
        const axe = await new AxeBuilder({ page }).include('.sessions').withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'best-practice']).analyze()
        expect(axe.violations).toEqual([])
        await page.locator('.sessions').screenshot({ path: join(dir, `wt1-${width}-${theme}-${state}.png`), animations: 'disabled' })
      }
    })
  }
}

test('a stopped lead links its successor instead of claiming live workers', async ({ page }) => {
  const { data, session } = await setup(page)
  data.sessions.push(session(90, { role: 'coordinator', parent_harness_session_id: null, display_label: 'Resumed lead' }))
  Object.assign(data.sessions.find(s => s.id === lead)!, { phase: 'stopped', stopped_at: ago(0), handed_over_to_id: id(90) })
  for (const worker of data.sessions.filter(s => s.parent_harness_session_id === lead && s.phase !== 'stopped')) {
    Object.assign(worker, { parent_harness_session_id: id(90), adopted_from_id: lead })
  }
  await page.goto('/agents')
  await page.locator('.group-toggle').filter({ hasText: 'Ended' }).click()
  await expect(row(page, lead).getByRole('link', { name: 'Handed over to Resumed lead' })).toBeVisible()
  await expect(row(page, lead).locator('.worker-tools')).toHaveCount(0)
  await expect(row(page, id(2)).getByRole('link', { name: 'Adopted from Release lead' })).toBeVisible()
  await expect(row(page, lead).locator('.history-toggle')).toContainText('8 stopped')
})
