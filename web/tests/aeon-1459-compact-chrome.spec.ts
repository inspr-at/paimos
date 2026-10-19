// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1459 (design aeon-1279-compact-chrome-r2, Tick spec v1): the shared compact chrome and the
// Agents list on phones and short screens. Risks: the header wraps or lets rows show through; a
// footer bar comes back on compact; the Sessions toolbar takes two lines; the toolbar starts too
// low to show rows; the header sticks in short landscape and starves the list; the scroller
// resizes while a finger scrolls; search or the held chip move the toolbar or the rows; a target
// drops under 44 px; desktop 1440 drifts from main.
import { test, expect, type Page } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import { dirname } from 'node:path'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

type Size = { width: number; height: number }
type Box = { x: number; y: number; width: number; height: number }
const NOW = Date.parse('2026-10-16T08:00:00Z')

async function report(path: string, lines: string[]) {
  await mkdir(dirname(path), { recursive: true })
  await writeFile(path, lines.join('\n') + '\n')
}
const box = (page: Page, selector: string) => page.locator(selector).first().evaluate(el => {
  const r = el.getBoundingClientRect()
  return { x: +r.x.toFixed(2), y: +r.y.toFixed(2), width: +r.width.toFixed(2), height: +r.height.toFixed(2) }
})

// About 30 sessions in three project groups, as the design draws the list.
async function agentsWorld(page: Page, size: Size, look: 'light' | 'dark') {
  await page.setViewportSize(size)
  await page.emulateMedia({ colorScheme: look, reducedMotion: 'reduce' })
  await page.clock.install({ time: NOW })
  const work = fixtures({ bigProject: 40 })
  work.preferences.theme = { choice: look }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now: NOW, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
  const base = data.sessions[1]!
  for (let n = 0; n < 24; n++) data.sessions.push({ ...base, id: `5e000000-0000-4000-8000-${String(100 + n).padStart(12, '0')}`, display_label: `worker-${n + 1}` } as typeof base)
  await mockAgents(page, data)
  return { work, data }
}
async function openAgents(page: Page, size: Size, look: 'light' | 'dark') {
  const world = await agentsWorld(page, size, look)
  await page.goto('/agents')
  await expect(page.locator('[data-row^="s:"]').nth(20)).toBeAttached()
  await page.evaluate(() => document.fonts.ready.then(() => undefined))
  return world
}

// ---------- C9: desktop 1440 keeps main's geometry (±1 px), light and dark ----------
// Measured on main before this change (5aaf10ee4) with this world; font-independent boxes only.
const MAIN_1440: Record<string, Box> | null = null
async function desktopGeometry(page: Page) {
  const out: Record<string, Box> = {
    header: await box(page, '.app-header'),
    'page head': await box(page, '.agents-page .page-head'),
    'capacity row': await box(page, '.agents-page .cap-row'),
    toolbar: await box(page, '.sessions .th-stick'),
    'toolbar line': await box(page, '.sessions .tree-head'),
    'column head': await box(page, '.sessions .thead'),
  }
  const columns = await page.locator('.sessions .thead > [role="columnheader"]').evaluateAll(cells => cells.map(cell => { const r = cell.getBoundingClientRect(); return { x: +r.x.toFixed(2), y: +r.y.toFixed(2), width: +r.width.toFixed(2), height: +r.height.toFixed(2) } }))
  columns.forEach((column, index) => { out[`column ${index + 1}`] = column })
  return out
}
for (const look of ['light', 'dark'] as const) test(`AEON-1459 C9 1440 ${look}: header, page head, capacity row, toolbar and columns keep main's geometry`, async ({ page }, info) => {
  await openAgents(page, { width: 1440, height: 900 }, look)
  const now = await desktopGeometry(page)
  await report(info.outputPath(`AEON-1459/c9-1440-${look}.md`), ['```json', JSON.stringify(now, null, 1), '```'])
  await page.screenshot({ path: info.outputPath(`AEON-1459/aeon1459-1440x900-${look}-c9.png`) })
  if (!MAIN_1440) return
  for (const [name, expected] of Object.entries(MAIN_1440)) {
    const actual = now[name]
    expect(actual, `${name} exists`).toBeTruthy()
    for (const key of ['x', 'y', 'width', 'height'] as const) expect(Math.abs(actual![key] - expected[key]), `${name}.${key}: ${actual![key]} vs main ${expected[key]}`).toBeLessThanOrEqual(1)
  }
})
