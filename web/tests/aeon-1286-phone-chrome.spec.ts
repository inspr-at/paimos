// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1286: compact phone chrome. Baseline measurement (first round).
import { test, expect, type Page } from '@playwright/test'
import { writeFile, mkdir } from 'node:fs/promises'
import { dirname } from 'node:path'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockLeadFlow, LEAD_SESSION } from './lead-flow-fixtures'

const SIZES = [{ width: 390, height: 664 }, { width: 390, height: 844 }, { width: 844, height: 340 }, { width: 320, height: 568 }]

// Rows fully on screen: both their top and bottom edges take the pointer.
const fullyVisible = (page: Page, rows: string) => page.evaluate(selector => [...document.querySelectorAll<HTMLElement>(selector)].filter(row => {
  const box = row.getBoundingClientRect(), x = box.left + Math.min(box.width / 2, 120)
  if (box.height === 0 || box.top < 0 || box.bottom > innerHeight) return false
  const top = document.elementFromPoint(x, box.top + 1), bottom = document.elementFromPoint(x, box.bottom - 1)
  return !!top && !!bottom && row.contains(top) && row.contains(bottom)
}).length, rows)
const firstRowTop = (page: Page, rows: string) => page.evaluate(selector => document.querySelector(selector)?.getBoundingClientRect().top ?? -1, rows)

async function report(path: string, lines: string[]) {
  await mkdir(dirname(path), { recursive: true })
  await writeFile(path, lines.join('\n') + '\n')
}

for (const size of SIZES) {
  test(`AEON-1286 baseline ticket list ${size.width}x${size.height}`, async ({ page }, info) => {
    await page.setViewportSize(size)
    await page.clock.setSystemTime(new Date('2026-10-16T08:00:00Z'))
    await mockWork(page, fixtures({ bigProject: 40 }))
    await page.goto('/p/AEON')
    await expect(page.locator('tr.ticket-row').nth(5)).toBeVisible()
    const rows = await fullyVisible(page, 'tr.ticket-row'), top = await firstRowTop(page, 'tr.ticket-row')
    await page.screenshot({ path: info.outputPath(`AEON-1286/baseline-tickets-${size.width}x${size.height}.png`) })
    await page.locator('main').evaluate(main => { main.scrollTop = 600 })
    const scrolled = await fullyVisible(page, 'tr.ticket-row')
    await page.screenshot({ path: info.outputPath(`AEON-1286/baseline-tickets-scrolled-${size.width}x${size.height}.png`) })
    await report(info.outputPath(`AEON-1286/baseline-tickets-${size.width}x${size.height}.md`), [`tickets ${size.width}x${size.height}: rows ${rows} at top (first row top ${top}), ${scrolled} after scrolling 600`])
  })

  test(`AEON-1286 baseline agents ${size.width}x${size.height}`, async ({ page }, info) => {
    const now = Date.parse('2026-10-16T08:00:00Z')
    await page.setViewportSize(size)
    await page.clock.install({ time: now })
    await mockWork(page, fixtures(), { admin: true })
    const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
    const base = data.sessions[1]!
    for (let n = 0; n < 24; n++) data.sessions.push({ ...base, id: `5e000000-0000-4000-8000-${String(100 + n).padStart(12, '0')}`, display_label: `worker-${n + 1}` } as typeof base)
    await mockAgents(page, data)
    await page.goto('/agents')
    await expect(page.locator('[data-row^="s:"]').nth(5)).toBeVisible()
    const rows = await fullyVisible(page, '[data-row^="s:"]'), top = await firstRowTop(page, '[data-row^="s:"]')
    await page.screenshot({ path: info.outputPath(`AEON-1286/baseline-agents-${size.width}x${size.height}.png`) })
    await page.locator('main').evaluate(main => { main.scrollTop = 800 })
    const scrolled = await fullyVisible(page, '[data-row^="s:"]')
    await page.screenshot({ path: info.outputPath(`AEON-1286/baseline-agents-scrolled-${size.width}x${size.height}.png`) })
    await report(info.outputPath(`AEON-1286/baseline-agents-${size.width}x${size.height}.md`), [`agents ${size.width}x${size.height}: rows ${rows} at top (first row top ${top}), ${scrolled} after scrolling 800`])
  })

  test(`AEON-1286 baseline chat ${size.width}x${size.height}`, async ({ page }, info) => {
    await page.setViewportSize(size)
    await mockLeadFlow(page, { lead: 'working' })
    await page.route('**/api/**', route => {
      const path = new URL(route.request().url()).pathname
      if (['/api/models', '/api/approvals', '/api/agent-accounts', '/api/agent-accounts/capacity', '/api/agent-accounts/capacity/schedule'].includes(path)) return route.fulfill({ json: [] })
      return route.fallback()
    })
    await page.goto(`/agents/${LEAD_SESSION}?tab=chat`)
    const panel = page.getByRole('complementary', { name: 'Session details', exact: true })
    await expect(panel.getByRole('tab', { name: /^Chat/ })).toHaveAttribute('aria-selected', 'true')
    await page.waitForTimeout(500)
    const m = await page.evaluate(() => {
      const box = (s: string) => { const el = document.querySelector(s); if (!el) return null; const r = el.getBoundingClientRect(); return [Math.round(r.top), Math.round(r.bottom), Math.round(r.height)] }
      return { panel: box('.session-panel'), head: box('.panel-head'), bar: box('.pane-bar'), thread: box('#session-panel-messages .thread-scroll'), composer: box('#session-panel-messages .composer'), vh: innerHeight }
    })
    await page.screenshot({ path: info.outputPath(`AEON-1286/baseline-chat-${size.width}x${size.height}.png`) })
    await panel.getByRole('button', { name: 'More session actions' }).first().click()
    await page.screenshot({ path: info.outputPath(`AEON-1286/baseline-chat-menu-${size.width}x${size.height}.png`) })
    await report(info.outputPath(`AEON-1286/baseline-chat-${size.width}x${size.height}.md`), [`chat ${size.width}x${size.height}: ${JSON.stringify(m)} thread share ${m.thread ? (m.thread[2] / m.vh).toFixed(3) : 'n/a'}`])
  })
}
