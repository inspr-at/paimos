// SPDX-License-Identifier: AGPL-3.0-only
// AEON-56 / PL1: PL1_SHOTS=before|after writes the same mocked screen matrix.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { businessData, mockBusiness, NOW } from './business-fixtures'
import { crmData, mockCRM } from './crm-fixtures'
import { mockQuotes, quoteWorld } from './quote-list-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { journeyWorld, mockJourney } from './journey-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
import { mockTicketGraph } from './ticket-graph-fixtures'
import { accessWorld, mockAccess } from './access-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

test.use({ launchOptions: { args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] } })
const round = process.env.PL1_SHOTS
const shots = resolve('..', '.agent-shots', round ?? 'after')
const scenarios = [
  { name: 'projects-list', path: '/', ready: '.project-row' },
  { name: 'projects-cards', path: '/', ready: '.card-link' },
  { name: 'tickets-list', path: '/p/PHAROS/tickets', ready: 'tr.ticket-row:not(.ghost)' },
  { name: 'tickets-empty', path: '/p/PHAROS/tickets?q=unmatched', ready: '.table-card .state h2' },
  { name: 'tickets-outline', path: '/p/PHAROS/tickets?view=outline', ready: 'tr.ticket-row:not(.ghost)' },
  { name: 'tickets-graph', path: '/p/PHAROS/tickets?view=graph', ready: '.ticket-graph-canvas[data-ready="true"]' },
  { name: 'ticket-panel', path: '/p/PHAROS/PHAROS-11', ready: '.ticket-ws .title-text' },
  { name: 'ticket-peek', path: '/agents?peek=PHAROS-11', ready: '.ticket-ws .title-text' },
  { name: 'journey', path: '/p/PHAROS?view=journey', ready: '.j-grid' },
  { name: 'knowledge-entries', path: '/p/PHAROS/knowledge', ready: '.k-row' },
  { name: 'knowledge-graph', path: '/p/PHAROS/knowledge?view=graph', ready: '.kg-canvas[data-ready="true"]' },
  { name: 'agents', path: '/agents', ready: '.agents-page .row' },
  { name: 'settings', path: '/settings/personal', ready: '#appearance' },
  { name: 'access', path: '/settings/access/people', ready: 'table.people' },
  { name: 'business', path: '/business', ready: '.biz-page .card' },
  { name: 'quotes', path: '/business/quotes', ready: '[id^="quote-"]' },
]

async function setup(page: Page, name: string) {
  const work = fixtures()
  work.preferences.projects = { view: name === 'projects-cards' ? 'cards' : 'list' }
  await mockWork(page, work)
  if (name === 'tickets-graph') await mockTicketGraph(page)
  await mockAgents(page, agentData({ me: me.id, now: NOW.getTime(),
    projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
    tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
    nodes: Object.fromEntries(work.nodes.map(n => [n.id, { key: n.key, title: n.title }])),
  }))
  await mockBusiness(page, businessData())
  if (name === 'quotes') await mockCRM(page, crmData())
  await mockQuotes(page, quoteWorld())
  await mockSettings(page, settingsData({ photo: true }), { photo: true })
  if (name === 'journey') await mockJourney(page, journeyWorld('plan'))
  const knowledge = knowledgeWorld()
  await mockKnowledge(page, knowledge)
  await mockAccess(page, accessWorld(), { also: mockEffectivePermissions('admin').workspace.permissions })
  const nodes = knowledge.entries.filter(n => n.project === 'p-pharos' && n.status !== 'archived').map(n => ({
    id: n.id, key: n.key, type: n.type, kind: 'knowledge', slug: n.slug, title: n.title, status: n.status, degree: 2, updated_at: n.updated_at,
  }))
  await page.route('**/api/knowledge/graph?*', route => route.fulfill({ json: {
    nodes, edges: nodes.slice(1).map((n, i) => ({ source: nodes[i].id, target: n.id, kind: 'mention', label: 'mentions' })), truncated: false,
  } }))
}

for (const scenario of scenarios) for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`PL1 ${scenario.name} ${width} ${theme}`, async ({ page }) => {
    test.setTimeout(60_000)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await setup(page, scenario.name)
    // Some business fixtures set a fixed clock. Let animation time flow for
    // graph layout and keep agent heartbeats on the fixture's reference date.
    await page.clock.setSystemTime(NOW)
    await page.goto(scenario.path)
    await expect(page.locator(scenario.ready).first()).toBeVisible({ timeout: 30_000 })
    if (scenario.name === 'tickets-outline') await page.getByRole('button', { name: 'Expand PHAROS-10' }).click()
    await page.evaluate(() => document.fonts.ready)
    await page.waitForTimeout(scenario.name.endsWith('graph') ? 1500 : 500)
    if (round) {
      mkdirSync(shots, { recursive: true })
      await page.screenshot({ path: resolve(shots, `${scenario.name}-${width}-${theme}.png`) })
      const overflow = await page.evaluate(() => [...document.querySelectorAll<HTMLElement>('main *')].filter(el => {
        const r = el.getBoundingClientRect(), css = getComputedStyle(el)
        return r.width > 0 && r.height > 0 && r.top < innerHeight && r.bottom > 0 &&
          css.overflowX === 'hidden' && css.textOverflow !== 'ellipsis' && el.scrollWidth > el.clientWidth + 2
      }).map(el => ({ selector: el.className, text: el.textContent?.slice(0, 100), width: el.clientWidth, scroll: el.scrollWidth })))
      writeFileSync(resolve(shots, `${scenario.name}-${width}-${theme}.json`), JSON.stringify(overflow, null, 2))
      const detail: Record<string, string> = { settings: '.profile .fields', access: '.people', journey: '.add-row', agents: '.item.held', 'ticket-panel': '.sections', 'ticket-peek': '.sections' }
      if (width === 390 && detail[scenario.name]) {
        await page.locator(detail[scenario.name]).first().evaluate(el => el.scrollIntoView({ block: 'start' }))
        await page.screenshot({ path: resolve(shots, `${scenario.name}-${width}-${theme}-detail.png`) })
      }
      if (scenario.name === 'agents' || scenario.name === 'projects-cards') {
        const contrast = await new AxeBuilder({ page }).withRules(['color-contrast']).exclude('.calendar-version').analyze()
        writeFileSync(resolve(shots, `${scenario.name}-${width}-${theme}-contrast.json`), JSON.stringify(contrast.violations, null, 2))
      }
      const metrics = await page.evaluate(() => ['.k-frame', '.k-layout', '.k-list', '.k-group', '.k-rail', '.graph-controls .btn', '.graph-controls select', '.graph-controls .seg', '.add-row .field', '.add-row .btn', '.person .names', '.person .role-btn span:first-child', '.person .role-text'].flatMap(selector => [...document.querySelectorAll(selector)].slice(0, 2).map(el => {
        const b = el.getBoundingClientRect(); return { selector, x: b.x, y: b.y, width: b.width, height: b.height }
      })))
      writeFileSync(resolve(shots, `${scenario.name}-${width}-${theme}-metrics.json`), JSON.stringify(metrics, null, 2))
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    if (round === 'before') return

    if (scenario.name === 'knowledge-entries') {
      const right = await page.locator('.k-list').evaluate(el => el.getBoundingClientRect().right)
      for (const group of await page.locator('.k-group').all()) expect((await group.boundingBox())!.x + (await group.boundingBox())!.width).toBeLessThanOrEqual(right + 1)
    }
    if (scenario.name.endsWith('graph')) {
      const heights = await page.locator('.graph-controls > .btn, .graph-controls > .seg, .graph-label-control select, .graph-menu summary').evaluateAll(els => els.map(el => el.getBoundingClientRect().height))
      expect(Math.max(...heights) - Math.min(...heights)).toBeLessThanOrEqual(1)
    }
    if (scenario.name === 'agents') {
      const queue = (await page.locator('.queue').boundingBox())!
      for (const button of await page.locator('.item.held .row-actions button').all()) {
        const box = (await button.boundingBox())!
        // Subpixel borders land a fraction past the queue; a whole pixel still fails.
        expect(Math.floor(box.x + box.width)).toBeLessThanOrEqual(Math.ceil(queue.x + queue.width))
      }
      const contrast = await new AxeBuilder({ page }).include('.cap').withRules(['color-contrast']).analyze()
      expect(contrast.violations).toEqual([])
    }
    if (scenario.name === 'tickets-empty') {
      const state = page.locator('.table-card .state')
      await expect(state.getByRole('button')).toHaveCount(1)
      await expect(state.locator('p')).toHaveCount(0)
      await state.getByRole('button', { name: 'Clear filters' }).click()
      await expect(page.locator('tr.ticket-row:not(.ghost)').first()).toBeVisible()
    }
    if (width !== 390) return
    if (scenario.name === 'projects-list') {
      const row = page.locator('.project-row').first(), box = (await row.boundingBox())!, text = (await row.locator('.project-text').boundingBox())!
      expect(Math.abs((text.x - box.x) - (box.x + box.width - text.x - text.width))).toBeLessThanOrEqual(1)
    }
    if (scenario.name === 'journey') {
      const heights = await page.locator('.add-row .field, .add-row .btn').evaluateAll(els => els.map(el => el.getBoundingClientRect().height))
      expect(Math.min(...heights)).toBeGreaterThanOrEqual(44)
      expect(Math.max(...heights) - Math.min(...heights)).toBeLessThanOrEqual(1)
    }
    if (scenario.name === 'settings') {
      const first = (await page.getByLabel('First name').boundingBox())!, last = (await page.locator('.last_name .label').boundingBox())!
      expect(last.y - first.y - first.height).toBeLessThanOrEqual(24)
    }
    if (scenario.name === 'access') {
      for (const row of await page.locator('.person').all()) {
        const name = (await row.locator('.names').boundingBox())!, role = (await row.locator('.role-btn span, .role-text').first().boundingBox())!
        expect(Math.abs(name.x - role.x)).toBeLessThanOrEqual(1)
      }
    }
    if (scenario.name === 'quotes') {
      const heights = await page.locator('.facets .btn').evaluateAll(els => els.map(el => el.getBoundingClientRect().height))
      expect(heights.length).toBeGreaterThan(0)
      for (const height of heights) expect(height).toBe(44)
    }
  })
}
