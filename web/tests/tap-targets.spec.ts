// SPDX-License-Identifier: AGPL-3.0-only
import { mkdir } from 'node:fs/promises'
import { expect, test, type Locator, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { expectStableControls } from './helpers/stable'
import { mockEffectivePermissions } from './authz-fixtures'
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
async function setup(page: Page, theme: 'light' | 'dark', density: 'comfortable' | 'compact') {
  const readinessCalls: string[] = []
  await page.addInitScript(() => {
    const openedTabs: string[] = []
    Object.assign(window, { openedTabs })
    window.open = url => { openedTabs.push(String(url)); return null }
  })
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.preferences['list:display'] = { density }
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
  await page.route('**/api/me/permissions*', route => {
    const permissions = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    permissions.workspace.permissions.push('run.create')
    return route.fulfill({ json: permissions })
  })
  await page.route('**/api/queue**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/queue' && route.request().method() === 'GET') return route.fulfill({ json: {
      items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: null, warning: false },
    } })
    if (path === '/api/queue/n-2/readiness' && route.request().method() === 'GET') {
      readinessCalls.push('n-2')
      return route.fulfill({ json: { queueable: true, ready: false, missing: ['estimate', 'criteria'], suggested_estimate_hours: 3, security_review_required: false } })
    }
    return route.fallback()
  })
  return { readinessCalls }
}

// Chromium excludes the right/bottom boundary of a box. Sample -22 exactly,
// and the last fraction of a pixel inside +22, on all four sides of the centre.
async function expectReach(control: Locator, expanded: boolean) {
  await control.scrollIntoViewIfNeeded()
  const hits = await control.evaluate(el => {
    const rect = el.getBoundingClientRect(), x = rect.x + rect.width / 2, y = rect.y + rect.height / 2
    return [[-22, 0], [21.99, 0], [0, -22], [0, 21.99]].map(([dx, dy]) => {
      const hit = document.elementFromPoint(x + dx!, y + dy!)
      return { dx, dy, own: !!hit && (hit === el || el.contains(hit)), hit: hit?.outerHTML.slice(0, 200) }
    })
  })
  if (expanded) expect(hits, 'all four edges of the 44px reach belong to the control').toEqual(hits.map(hit => ({ ...hit, own: true })))
  if (expanded) {
    const bounds = await control.evaluate(el => {
      const row = el.closest('tr.ticket-row')
      if (!row) return null
      const rect = el.getBoundingClientRect(), parent = row.getBoundingClientRect()
      const centre = rect.y + rect.height / 2
      return { top: centre - 22 - parent.top, bottom: parent.bottom - (centre + 22) }
    })
    if (bounds) {
      expect(bounds.top, 'hit area stays below the row top').toBeGreaterThanOrEqual(0)
      expect(bounds.bottom, 'hit area stays above the row bottom').toBeGreaterThanOrEqual(0)
    }
  }
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
// Actual pointer events can round fractional coordinates. Click 1px inside
// the boundary; expectReach separately checks the full 44px hit-test footprint.
async function edgeClick(page: Page, control: Locator, expanded: boolean, phone: boolean) {
  const rect = (await control.boundingBox())!
  const x = rect.x + rect.width / 2 + (expanded && !phone ? 21 : 0)
  const y = rect.y + rect.height / 2 + (expanded && phone ? 21 : 0)
  const hit = await control.evaluate((el, { x, y }) => {
    const target = document.elementFromPoint(x, y)
    return { own: !!target && (target === el || el.contains(target)), x, y, hit: target?.outerHTML.slice(0, 200) }
  }, { x, y })
  expect(hit, 'the coordinate click still hits the control').toMatchObject({ own: true })
  await page.mouse.click(x, y)
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
  { width: 768, coarse: false }, { width: 768, coarse: true }, { width: 1024, coarse: true }, { width: 1440, coarse: false },
]) for (const theme of ['light', 'dark'] as const) for (const density of ['comfortable', 'compact'] as const) {
  test.describe(`${width}px ${coarse ? 'coarse' : 'fine'} ${theme} ${density}`, () => {
    test.use({ viewport: { width, height: 1100 }, hasTouch: coarse })
    test('ticket and tier controls keep their visuals, reach and position', async ({ page }) => {
      const expanded = width <= 720 || coarse
      const phone = width <= 720
      const { readinessCalls } = await setup(page, theme, density)
      await page.goto('/p/PHAROS/tickets?view=outline&closed=1')
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
      expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(coarse)
      const table = page.getByRole('treegrid', { name: 'Ticket outline' })
      const row = table.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-10$/ }) })
      const twisty = row.locator('.twisty'), status = row.locator('.status-btn')
      await expect(twisty).toBeVisible()
      await expect(page.locator('.table-card')).toHaveClass(new RegExp(density))
      const visual = (await twisty.boundingBox())!
      expect(visual.width).toBe(width <= 720 ? 36 : 18)
      expect(visual.height).toBe(width <= 720 ? 36 : 22)
      expect((await status.boundingBox())!.height).toBe(density === 'compact' ? 22 : phone ? 24 : 26)
      await expectReach(twisty, expanded)
      await expectReach(status, expanded)
      await capture(page, `tickets-${width}-${coarse ? 'coarse' : 'fine'}-${theme}-${density}`)
      await expectStableControls({ controls: { row, twisty, status, title: row.locator('.title-link') }, interactions: [
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
      await expectReach(nested, expanded)
      // Consecutive rows must own every edge of their status targets. Clicking
      // each edge must open that exact row's menu, without moving either row.
      const first = table.locator('#row-n-1'), second = table.locator('#row-n-2')
      await expect(first).toBeVisible(); await expect(second).toBeVisible()
      expect(await first.evaluate(el => el.nextElementSibling?.id)).toBe('row-n-2')
      if (!phone) {
        for (const ticket of [first, second]) {
          const height = (await ticket.boundingBox())!.height
          if (coarse) expect(height).toBeGreaterThanOrEqual(44)
          else expect(height).toBe(density === 'compact' ? 30 : 36)
        }
      }
      if (expanded) {
        for (const [ticket, key] of [[first, 'PHAROS-11'], [second, 'PHAROS-12']] as const) {
          const control = ticket.locator('.status-btn')
          await expectReach(control, true)
          const edges = [[-21, 0], [21, 0], [0, -21], [0, 21]] as const
          const menu = page.getByRole('menu', { name: `Status of ${key}`, exact: true })
          await expectStableControls({ controls: { first, second, status: control }, interactions: edges.map(([dx, dy]) => ({
            name: `${key} status edge ${dx},${dy}`,
            run: async () => {
              const rect = (await control.boundingBox())!
              await page.mouse.click(rect.x + rect.width / 2 + dx, rect.y + rect.height / 2 + dy)
              await expect(menu).toBeVisible()
              await page.keyboard.press('Escape'); await expect(menu).toHaveCount(0)
            },
          })) })
        }
      }
      if (!phone) {
        const queue = second.locator('.row-actions .q-btn')
        const open = second.getByRole('button', { name: 'Open PHAROS-12 in a new tab', exact: true })
        // Fine-pointer rows reveal their actions on hover; touch cases have
        // already selected this row through its status action above. Focus
        // then keeps the actions visible while their dialogs open and close.
        if (!coarse) await second.hover()
        await expect(queue).toBeVisible()
        await queue.focus()
        await expect(queue).toBeVisible(); await expect(open).toBeVisible()
        const a = (await queue.boundingBox())!, b = (await open.boundingBox())!
        expect(a.width).toBe(density === 'compact' ? 22 : 24)
        expect(a.height).toBe(a.width); expect(b.width).toBe(a.width); expect(b.height).toBe(a.height)
        const centres = b.x + b.width / 2 - (a.x + a.width / 2)
        if (coarse) {
          expect(centres, 'neighbouring 44px action targets must not overlap').toBeGreaterThanOrEqual(44)
          await expectReach(queue, true); await expectReach(open, true)
          const rowBox = (await second.boundingBox())!, title = (await second.locator('.title-link').boundingBox())!
          expect(a.x + a.width / 2 - 22, 'Queue target starts after the title link').toBeGreaterThanOrEqual(title.x + title.width)
          expect(b.x + b.width / 2 + 22, 'Open target stays inside its row').toBeLessThanOrEqual(rowBox.x + rowBox.width)
        } else expect(centres).toBe(density === 'compact' ? 24 : 26)
        const edges = coarse ? [[-21, 0], [21, 0], [0, -21], [0, 21]] as const : [[0, 0]] as const
        const missing = page.getByRole('dialog', { name: 'PHAROS-12: what is missing to queue it', exact: true })
        let openedCount = 0
        const openedTabs = () => page.evaluate(() => (window as Window & { openedTabs: string[] }).openedTabs)
        await expectStableControls({ controls: { row: second, actions: second.locator('.row-actions'), queue, open, title: second.locator('.title-link') }, interactions: edges.flatMap(([dx, dy]) => [
          { name: `Queue edge ${dx},${dy}`, run: async () => {
            const rect = (await queue.boundingBox())!, before = readinessCalls.length
            await page.mouse.click(rect.x + rect.width / 2 + dx, rect.y + rect.height / 2 + dy)
            await expect(missing).toBeVisible()
            await expect(missing.getByRole('status')).toContainText('Still missing: estimate, acceptance criteria.')
            expect(readinessCalls.length).toBeGreaterThan(before)
            expect(readinessCalls.every(id => id === 'n-2')).toBe(true)
            expect(await openedTabs()).toHaveLength(openedCount)
            await page.keyboard.press('Escape'); await expect(missing).toHaveCount(0)
          } },
          { name: `Open edge ${dx},${dy}`, run: async () => {
            const rect = (await open.boundingBox())!, before = readinessCalls.length
            await page.mouse.click(rect.x + rect.width / 2 + dx, rect.y + rect.height / 2 + dy)
            openedCount++
            const opened = await openedTabs()
            expect(opened).toHaveLength(openedCount)
            expect(new URL(opened.at(-1)!, page.url()).pathname).toBe('/p/PHAROS/PHAROS-12')
            expect(readinessCalls).toHaveLength(before)
            await expect(missing).toHaveCount(0)
          } },
        ]) })
        await capture(page, `tickets-${width}-${coarse ? 'coarse' : 'fine'}-${theme}-${density}-actions`)
      }
      await capture(page, `tickets-${width}-${coarse ? 'coarse' : 'fine'}-${theme}-${density}-consecutive`)
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)

      await page.goto('/agents')
      const session = page.locator(`[data-row="s:${sessionId}"]`)
      const tier = session.locator('.tier-mark:visible')
      await expect(tier).toBeVisible()
      // The initial report can replace a read-only span with the live button.
      // Wait for the actionable control before measuring its settled visual.
      await expect(tier).toHaveAttribute('aria-haspopup', 'dialog')
      await expect.poll(async () => (await tier.boundingBox())?.height).toBe(26)
      await expectReach(tier, expanded)
      await capture(page, `agents-${width}-${coarse ? 'coarse' : 'fine'}-${theme}-${density}`)
      // Element screenshots can scroll the enclosing list. Restore the control
      // to the viewport before the guard records its coordinate-click baseline.
      await tier.scrollIntoViewIfNeeded()
      const picker = page.getByRole('dialog', { name: 'Change tier', exact: true })
      await expectStableControls({ controls: { tier, session, title: session.locator('.result') }, interactions: [
        { name: 'tier picker at hit-area edge', run: async () => { await edgeClick(page, tier, expanded, phone); await expect(picker).toBeVisible() } },
        { name: 'dismiss tier picker', run: async () => { await picker.getByRole('button', { name: /Cancel/ }).click(); await expect(picker).toHaveCount(0) } },
      ] })
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
    })
  })
}
