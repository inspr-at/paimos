// SPDX-License-Identifier: AGPL-3.0-only
// AEON-304: session rows on phones. Each row reads as one compact block: the
// avatar beside the title (up to two lines) with the heartbeat inline in the
// meta line under it, the overflow button top-right on the title line, then one
// execution line and one state line. Tree lines run through the avatar column.
//
//   AEON304_SHOTS=<dir> npx playwright test -c playwright.ui.config.ts tests/agents-phone-rows.spec.ts
//
// writes <width>-<theme>.png (the sessions card) for review by eye.
import { expect, test, type Locator, type Page } from '@playwright/test'
import type { HarnessSession } from '../src/lib/agents'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const now = Date.parse('2026-09-29T08:28:00Z')
const ago = (seconds: number) => new Date(now - seconds * 1000).toISOString()
const id = (n: number) => `5e000000-0000-4000-8000-${String(n).padStart(12, '0')}`
const lead = id(1)
const row = (page: Page, n: number) => page.locator(`[data-row="s:${id(n)}"]`)
const shots = process.env.AEON304_SHOTS

async function setup(page: Page, theme: 'light' | 'dark') {
  await page.clock.install({ time: now })
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const nodes = {
    'p-aeon': { key: 'AEON', title: 'Aeon' },
    'n-263': { key: 'AEON-263', title: 'Simplify agent rules setup: clear defaults, one import, no duplicate screens' },
    'n-251': { key: 'AEON-251', title: 'AR4 Shadow mode: compare the new rules engine against the classic one' },
    'n-250': { key: 'AEON-250', title: 'AR3 Importer: today’s documents into rules' },
    'n-297': { key: 'AEON-297', title: 'Capacity readings: account windows and live usage' },
  }
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-263', restore: 'n-251', web: 'n-250', release: 'n-297', approvals: 'n-6' }, nodes })
  // The shared fixture factory exposes narrower types than its wire payload.
  const base = data.sessions[0]! as unknown as HarnessSession
  const ticket = (key: keyof typeof nodes) => ({ ticket_node_id: key, ticket: { id: key, ...nodes[key] } })
  const session = (n: number, fields: Record<string, unknown> = {}) => ({
    ...base, id: id(n), role: 'worker', parent_harness_session_id: lead, brief: undefined, ticket_node_id: null, ticket: null,
    agent: { id: base.agent_principal_id, name: 'aeon-coordinator' }, harness: 'grok', model: 'grok-4.7', reasoning_effort: 'xhigh', account_label: undefined,
    phase: 'working', activity: 'busy', heartbeat_at: ago(4), created_at: ago(900), ...fields,
  }) as unknown as typeof data.sessions[number]
  data.sessions.splice(0, data.sessions.length,
    session(1, { parent_harness_session_id: null, role: 'coordinator', display_label: 'AEON lead (Claude)', harness: 'claude', model: 'claude-opus-5-5', reasoning_effort: 'xhigh', account_label: 'Claude Max', ...ticket('n-263'), created_at: ago(7200) }),
    session(2, { display_label: 'aeon-251-shadow', ...ticket('n-251') }),
    session(3, { display_label: 'aeon-250-rules-importer', ...ticket('n-250') }),
    session(4, { display_label: 'aeon-297-capacity-readings', harness: 'codex', model: 'gpt-6-sol', reasoning_effort: 'high', account_label: 'Codex Pro', ...ticket('n-297') }),
    session(5, { display_label: 'nested-scout', parent_harness_session_id: id(4), harness: 'claude', model: 'claude-sonnet-5', reasoning_effort: 'high' }),
    ...Array.from({ length: 3 }, (_, i) => session(10 + i, { display_label: `stopped-${i}`, phase: 'stopped', activity: 'idle', stopped_at: ago(3600 + i), heartbeat_at: ago(3600 + i) })),
    // A silent session makes "Clear stale" appear; archived ones make "Removed N".
    session(20, { parent_harness_session_id: null, role: 'worker', display_label: 'silent-scout', phase: 'waiting', activity: 'idle', heartbeat_at: ago(1500) }),
    ...Array.from({ length: 12 }, (_, i) => session(30 + i, { parent_harness_session_id: null, display_label: `removed-${i}`, phase: 'stopped', activity: 'idle', archived_at: ago(90_000), stopped_at: ago(90_000) })),
  )
  data.approvals.splice(0)
  data.messages.splice(0)
  data.targets.splice(0)
  await mockAgents(page, data)
}

type Box = { x: number; y: number; width: number; height: number }
const box = async (locator: Locator): Promise<Box> => (await locator.boundingBox())!
const midY = (b: Box) => b.y + b.height / 2
const midX = (b: Box) => b.x + b.width / 2
async function capture(page: Page, name: string) {
  if (!shots) return
  await page.locator('.sessions').screenshot({ path: test.info().outputPath(`${name}.png`), animations: 'disabled' })
}

for (const theme of ['light', 'dark'] as const) for (const width of [375, 390, 430]) {
  test(`phone session rows are compact and aligned at ${width}px in ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1400 })
    await setup(page, theme)
    await page.goto('/agents')
    await expect(row(page, 5)).toBeVisible()
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await capture(page, `${width}-${theme}`)

    for (const n of [1, 2, 3, 4, 5, 20]) {
      const r = row(page, n)
      const title = await box(r.locator('.result'))
      const identity = await box(r.locator('.agent-link'))
      const avatar = await box(r.locator('.bot'))
      const menu = await box(r.locator('.more'))
      const time = await box(r.locator('.session-context time'))
      const exec = await box(r.locator('.c-exec'))
      const state = await box(r.locator('.c-state'))
      const lineHeight = await r.locator('.result').evaluate(el => parseFloat(getComputedStyle(el).lineHeight))
      // Title, heartbeat and menu share the first row: the avatar's row.
      expect(Math.abs(midY(menu) - (title.y + lineHeight / 2)), `menu on the title line (row ${n})`).toBeLessThanOrEqual(3)
      expect(menu.width).toBeGreaterThanOrEqual(44)
      expect(menu.height).toBeGreaterThanOrEqual(44)
      expect(menu.x + menu.width).toBeLessThanOrEqual(width)
      expect(time.y, `time inside the identity block (row ${n})`).toBeGreaterThanOrEqual(identity.y)
      expect(time.y + time.height).toBeLessThanOrEqual(identity.y + identity.height + 1)
      expect(time.x).toBeGreaterThan(title.x)
      expect(time.x + time.width).toBeLessThanOrEqual(menu.x)
      expect(title.y).toBeLessThanOrEqual(avatar.y + 2)
      // The title may wrap to two lines, never more.
      expect(title.height).toBeLessThanOrEqual(lineHeight * 2 + 1)
      // The execution and state lines start under the glyph (AEON-784): the fold column keeps the tree.
      expect(exec.y).toBeGreaterThanOrEqual(identity.y + identity.height - 1)
      // Tier controls now have their own reserved line. The execution copy
      // still fits one compact line, and the group grows only by that control.
      const executionCopy = await box(r.locator('.exec-copy'))
      const tier = await box(r.locator('.phone-tier'))
      expect(executionCopy.height).toBeLessThanOrEqual(22)
      expect(tier.y).toBeGreaterThanOrEqual(executionCopy.y + executionCopy.height)
      const executionIcon = await box(r.locator('.exec-icon'))
      expect(executionIcon.height).toBeLessThanOrEqual(18)
      expect(exec.height).toBeLessThanOrEqual(Math.max(executionCopy.height, executionIcon.height) + tier.height + 6.5)
      expect(Math.abs(exec.x - avatar.x)).toBeLessThanOrEqual(1)
      expect(state.y).toBeGreaterThanOrEqual(exec.y + exec.height - 1)
      expect(Math.abs(state.x - avatar.x)).toBeLessThanOrEqual(1)
      if (await r.locator('.ticket-chip').count()) {
        const chip = await box(r.locator('.ticket-chip'))
        expect(Math.abs(midY(chip) - midY(state)), `state and ticket on one line (row ${n})`).toBeLessThanOrEqual(3)
      }
    }
    // Long titles use the full width up to the menu before wrapping.
    const lead0 = await box(row(page, 1).locator('.result'))
    const leadMenu = await box(row(page, 1).locator('.more'))
    expect(leadMenu.x - (lead0.x + lead0.width)).toBeLessThanOrEqual(16)

    // Tree lines run in the fold column, 16 px per level (AEON-784), never through glyphs or text.
    const leadFold = await box(row(page, 1).locator('.tree-fold'))
    const leadAvatar = await box(row(page, 1).locator('.bot'))
    const stem = await box(row(page, 1).locator('.tree-stem'))
    expect(leadFold.width).toBeGreaterThanOrEqual(44)
    expect(leadFold.height).toBeGreaterThanOrEqual(44)
    expect(stem.x).toBeGreaterThanOrEqual(leadFold.x)
    expect(stem.x).toBeLessThan(leadAvatar.x)
    expect(stem.y).toBeGreaterThanOrEqual(leadFold.y + leadFold.height - 1)
    for (const [child, parent] of [[2, 1], [4, 1], [5, 4]] as const) {
      const guide = await box(row(page, child).locator('.tree-guide.elbow'))
      const parentStem = await box(row(page, parent).locator('.tree-stem'))
      const childAvatar = await box(row(page, child).locator('.bot'))
      const text = await box(row(page, child).locator('.who'))
      expect(Math.abs(guide.x - parentStem.x), `guide of ${child} continues its parent's stem`).toBeLessThanOrEqual(1)
      expect(childAvatar.x - guide.x, `readable indent for ${child}`).toBeGreaterThanOrEqual(8)
      expect(text.x).toBeGreaterThan(guide.x + 8)
    }

    // Worker toggles and header actions: one line, 44 px targets.
    for (const toggle of await row(page, 1).locator('.worker-toggle').all()) expect((await box(toggle)).height).toBeGreaterThanOrEqual(44)
    const heading = await box(page.locator('#sessions-title'))
    for (const button of await page.locator('.sessions .head-tools button').all()) {
      const b = await box(button)
      expect(b.height).toBeGreaterThanOrEqual(44)
      expect(Math.abs(midY(b) - midY(heading))).toBeLessThanOrEqual(3)
    }

    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    expect(await page.locator('.sessions .row').evaluateAll(rows => rows.filter(el => el.scrollWidth > el.clientWidth + 1).map(el => el.getAttribute('data-row')))).toEqual([])
  })
}

for (const theme of ['light', 'dark'] as const) {
  test(`desktop keeps the session table at 1600px in ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width: 1600, height: 1000 })
    await setup(page, theme)
    await page.goto('/agents')
    await expect(row(page, 5)).toBeVisible()
    await capture(page, `1600-${theme}`)
    await expect(page.locator('.sessions .thead')).toBeVisible()
    await expect(row(page, 1).locator('.c-beat time')).toBeVisible()
    await expect(row(page, 1).locator('.session-context time')).toBeHidden()
    // Table cells share one line per row.
    const r = row(page, 2)
    const [title, beat, exec] = [await box(r.locator('.result')), await box(r.locator('.c-beat')), await box(r.locator('.c-exec'))]
    expect(Math.abs(midY(beat) - midY(exec))).toBeLessThanOrEqual(12)
    expect(beat.x).toBeGreaterThan(title.x + title.width)
  })
}
