// SPDX-License-Identifier: AGPL-3.0-only
import { mkdir } from 'node:fs/promises'
import { expect, test, type Locator, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { expectStableControls } from './helpers/stable'
import type { TierReport } from '../src/lib/serviceTier'

const sessionId = '5e000000-0000-4000-8000-000000000001'
const longName = 'Berechtigungsprüfung für die österreichische Unternehmensverwaltung und Wiederherstellungskoordination'
const report: TierReport = {
  harness: 'codex', model: 'fixture-model', harness_version: 'fixture-cli', adapter_version: 'fixture-adapter',
  checked_at: '2026-10-03T10:00:00Z', source: 'https://example.invalid/synthetic-price',
  applies: 'next_run', change_instructions: 'Change it in its terminal.',
  tiers: ['default', 'fast', 'fastest'].map((tier, i) => ({
    tier: tier as 'default' | 'fast' | 'fastest', name: tier, offered: true,
    price_multiplier: i + 1, usage_multiplier: i + 1, speed_factor: i + 1, mechanism: 'fixture',
  })),
}
async function setup(page: Page, theme: 'light' | 'dark') {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.nodes.find(n => n.id === 'n-epic')!.title = longName
  data.nodes.find(n => n.id === 'n-2')!.title = longName
  // Keep the person's chosen columns constant while lazy children load: this
  // spec checks hit areas, rather than the automatic-column policy.
  data.preferences['list:p-pharos'] = { visible: ['status', 'priority', 'assignee', 'updated'] }
  await mockWork(page, data, { admin: true })
  const agents = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
    tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  agents.sessions.splice(1); agents.approvals.splice(0); agents.messages.splice(0)
  Object.assign(agents.sessions[0]!, {
    harness: 'codex', model: report.model, display_label: longName, service_tier: 'fast', service_tier_revision: 1,
    service_tier_reports: [report], advertised_capabilities: ['inbox', 'stop', 'service_tier_v1'],
    process_ownership: { daemon_id: 'fixture', generation: 'a'.repeat(32), process_id: 'b'.repeat(32), root_pid: 1234, group_id: 1234, started_at: '2026-10-03T10:00:00Z' },
    process_observed_at: new Date().toISOString(), heartbeat_at: new Date().toISOString(),
  })
  await mockAgents(page, agents)
  await page.route('**/api/projects/*/harness-sessions/*/tier', route => route.fulfill({ json: {
    session_id: sessionId, revision: 1, active_tier: 'fast', pending: null, read_only: false, reports: [report], requests: [],
  } }))
}

// Chromium excludes the right/bottom boundary of a box. Sample -22 exactly,
// and the last fraction of a pixel inside +22, on all four sides of the centre.
async function expectReach(control: Locator, expanded: boolean, phone: boolean) {
  await control.scrollIntoViewIfNeeded()
  const hits = await control.evaluate(el => {
    const rect = el.getBoundingClientRect(), x = rect.x + rect.width / 2, y = rect.y + rect.height / 2
    return [[-22, 0], [21.99, 0], [0, -22], [0, 21.99]].map(([dx, dy]) => {
      const hit = document.elementFromPoint(x + dx!, y + dy!)
      return { dx, dy, own: !!hit && (hit === el || el.contains(hit)), hit: hit?.outerHTML.slice(0, 200) }
    })
  })
  // Phone cards have room for all four edges. Dense desktop table rows retain
  // their existing height: sticky/group headers can cover the vertical reach.
  const exposed = phone ? hits : hits.filter(hit => hit.dy === 0)
  if (expanded) expect(exposed, 'exposed edges of the 44px reach belong to the control').toEqual(exposed.map(hit => ({ ...hit, own: true })))
  const pseudo = await control.evaluate(el => getComputedStyle(el, '::before').content)
  expect(pseudo).toBe(expanded ? '""' : 'none')
  if (expanded) {
    const size = await control.evaluate(el => {
      const style = getComputedStyle(el, '::before')
      return { width: parseFloat(style.width), height: parseFloat(style.height) }
    })
    expect(size.width).toBeGreaterThanOrEqual(44); expect(size.height).toBeGreaterThanOrEqual(44)
  }
}
async function edgeClick(page: Page, control: Locator, expanded: boolean, phone: boolean) {
  const rect = (await control.boundingBox())!
  await page.mouse.click(rect.x + rect.width / 2 + (expanded && !phone ? 21.99 : 0), rect.y + rect.height / 2 + (expanded && phone ? 21.99 : 0))
}
async function capture(page: Page, label: string) {
  const directory = 'test-results/aeon-630-tap-targets'
  await mkdir(directory, { recursive: true })
  // Capture the relevant list, including the control, rather than the page's
  // independently scrolling main area (a full-page shot can show its tail).
  await page.locator(label.startsWith('tickets-') ? '.table-card' : '.sessions').screenshot({ path: `${directory}/${label}.png`, animations: 'disabled' })
}

for (const { width, coarse } of [
  { width: 390, coarse: false }, { width: 700, coarse: false }, { width: 720, coarse: false },
  { width: 768, coarse: false }, { width: 1024, coarse: true }, { width: 1440, coarse: false },
]) for (const theme of ['light', 'dark'] as const) {
  test.describe(`${width}px ${coarse ? 'coarse' : 'fine'} ${theme}`, () => {
    test.use({ viewport: { width, height: 1100 }, hasTouch: coarse })
    test('ticket and tier controls keep their visuals, reach and position', async ({ page }) => {
      const expanded = width <= 720 || coarse
      const phone = width <= 720
      await setup(page, theme)
      await page.goto('/p/PHAROS/tickets?view=outline&closed=1')
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
      expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(coarse)
      const table = page.getByRole('treegrid', { name: 'Ticket outline' })
      const row = table.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-10$/ }) })
      const twisty = row.locator('.twisty'), status = row.locator('.status-btn')
      await expect(twisty).toBeVisible()
      const visual = (await twisty.boundingBox())!
      expect(visual.width).toBe(width <= 720 ? 36 : 18)
      expect(visual.height).toBe(width <= 720 ? 36 : 22)
      expect((await status.boundingBox())!.height).toBe(width <= 720 ? 24 : 26)
      await expectReach(twisty, expanded, phone)
      await expectReach(status, expanded, phone)
      await capture(page, `tickets-${width}-${theme}`)
      await expectStableControls({ controls: { row, twisty, status, ...(phone ? { title: row.locator('.title-link') } : {}) }, interactions: [
        { name: 'hover arrow', run: () => twisty.hover() },
        { name: 'expand at hit-area edge', run: async () => {
          await edgeClick(page, twisty, expanded, phone)
          await expect(twisty).toHaveAttribute('aria-expanded', 'true')
          await expect(table.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-12$/ }) })).toBeVisible()
        } },
        { name: 'collapse at hit-area edge', run: async () => {
          await edgeClick(page, twisty, expanded, phone); await expect(twisty).toHaveAttribute('aria-expanded', 'false')
        } },
        { name: 'status menu at hit-area edge', run: async () => {
          await edgeClick(page, status, expanded, phone); await expect(page.getByRole('menu', { name: 'Status of PHAROS-10' })).toBeVisible()
        } },
        { name: 'dismiss status menu', run: async () => { await page.keyboard.press('Escape'); await expect(page.getByRole('menu', { name: 'Status of PHAROS-10' })).toHaveCount(0) } },
      ] })
      // Nested arrows carry inline indentation offsets. Their reach must stay
      // centred when phone rows switch from absolute positioning to flow.
      await twisty.click()
      const nested = table.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-12$/ }) }).locator('.twisty')
      await expectReach(nested, expanded, phone)
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)

      await page.goto('/agents')
      const session = page.locator(`[data-row="s:${sessionId}"]`)
      const tier = session.locator('.tier-mark:visible')
      await expect(tier).toBeVisible()
      expect((await tier.boundingBox())!.height).toBe(26)
      await expectReach(tier, expanded, phone)
      await capture(page, `agents-${width}-${theme}`)
      const picker = page.getByRole('dialog', { name: 'Change tier', exact: true })
      await expectStableControls({ controls: { tier, session, title: session.locator('.result') }, interactions: [
        { name: 'tier picker at hit-area edge', run: async () => { await edgeClick(page, tier, expanded, phone); await expect(picker).toBeVisible() } },
        { name: 'dismiss tier picker', run: async () => { await picker.getByRole('button', { name: /Cancel/ }).click(); await expect(picker).toHaveCount(0) } },
      ] })
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
    })
  })
}
