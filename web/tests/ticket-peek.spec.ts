// SPDX-License-Identifier: AGPL-3.0-only
// AEON-202: a ticket click opens the app side panel and stays on the current view.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { mockTicketGraph, ticketGraphWorld } from './ticket-graph-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'

test.use({ launchOptions: { args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] } })

const world: AgentWorld = {
  me: me.id,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}
const session = (n: number) => `5e000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`
const peek = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })
const pathOf = (page: Page) => new URL(page.url()).pathname

// Live rows keep painting, so a button on this page does not hold still for
// Playwright's stability check. The URL wait is armed first. The click skips
// that check, and the navigation is the next turn (AEON-447).
async function openInProject(page: Page, button: Locator, path: string) {
  await expect(button).toBeVisible()
  return Promise.all([
    page.waitForURL(`**${path}`, { timeout: 15_000 }),
    button.click({ force: true }),
  ])
}

async function clickShown(target: Locator) {
  await expect(target).toBeVisible()
  await target.click({ force: true })
}

// data-ready means the renderer has started, not that the simulation has stopped.
// Test builds publish read-only state: data-settled (engine stopped, this frame's
// hit bitmap painted), one marker per node with its canvas pixel and
// data-node-pickable, and data-hovered, which is force-graph's own hover. The
// click is a plain press and release once that hover names the target, so the
// library's click dispatch is what selects the node (AEON-447).
async function clickGraphNode(canvas: Locator, id: string, timeout: number) {
  const page = canvas.page()
  const node = canvas.locator(`[data-node-id="${id}"]`)
  const surface = canvas.locator('canvas').first()
  const read = async () => ({ box: await surface.boundingBox(), x: Number(await node.getAttribute('data-node-x')), y: Number(await node.getAttribute('data-node-y')) })
  await expect(async () => {
    await expect(canvas).toHaveAttribute('data-settled', 'true', { timeout: 2_000 })
    await expect(node).toHaveAttribute('data-node-pickable', 'true', { timeout: 2_000 })
    const { box, x, y } = await read()
    if (!box || !Number.isFinite(x) || !Number.isFinite(y)) throw new Error(`graph node ${id} has no canvas position`)
    await page.mouse.move(box.x + x, box.y + y)
    await expect(canvas).toHaveAttribute('data-hovered', id, { timeout: 2_000 })
    // Still the same settled view under a stationary pointer.
    await expect(canvas).toHaveAttribute('data-settled', 'true', { timeout: 500 })
    expect(await read()).toEqual({ box, x, y })
  }).toPass({ timeout })
  await page.mouse.down()
  await page.mouse.up()
}

async function openAgents(page: Page) {
  await mockWork(page, fixtures())
  await mockAgents(page, agentData(world))
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible({ timeout: 20_000 })
  await expect(page.locator('.agents-page .row').first()).toBeVisible({ timeout: 20_000 })
}

test('a ticket pill on /agents opens the peek and stays on /agents', async ({ page }) => {
  test.setTimeout(60_000)
  await page.setViewportSize({ width: 1600, height: 1000 })
  await openAgents(page)
  const chip = page.locator('.sessions').getByRole('link', { name: 'PHAROS-11' }).first()
  await expect(chip).toHaveAttribute('href', '/p/PHAROS/PHAROS-11')
  await clickShown(chip)
  await expect(peek(page).getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
  await expect(peek(page).getByRole('button', { name: 'Open in project' })).toBeVisible()
  expect(pathOf(page)).toBe('/agents')
  expect(new URL(page.url()).searchParams.get('peek')).toBe('PHAROS-11')
  const list = (await page.locator('.sessions').boundingBox())!
  const box = (await peek(page).boundingBox())!
  expect(list.x + list.width).toBeLessThanOrEqual(box.x + 2)
  expect(box.x + box.width).toBeLessThanOrEqual(1600)

  await openInProject(page, peek(page).getByRole('button', { name: 'Open in project' }), '/p/PHAROS/PHAROS-11')
  await expect(page.getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
})

test('cmd-click on an agents ticket keeps the real link', async ({ page }) => {
  test.setTimeout(60_000)
  await openAgents(page)
  const chip = page.locator('.sessions').getByRole('link', { name: 'PHAROS-11' }).first()
  await expect(chip).toBeVisible()
  await expect(peek(page)).toHaveCount(0)
  // One modified click. It opens the ticket in a new tab and leaves the peek closed (AEON-447).
  const pending = page.context().waitForEvent('page', { timeout: 15_000 })
  await chip.click({ modifiers: ['ControlOrMeta'], force: true })
  const tab = await pending
  await tab.waitForURL('**/p/PHAROS/PHAROS-11')
  await tab.close()
  await expect(peek(page)).toHaveCount(0)
  expect(pathOf(page)).toBe('/agents')
})

test('a session-row ticket and a panel ticket peek without covering the session text', async ({ page }) => {
  test.setTimeout(60_000)
  await page.setViewportSize({ width: 1600, height: 1000 })
  await openAgents(page)
  const live = page.locator(`[data-row="s:${session(1)}"]`).getByRole('link', { name: 'PHAROS-11' })
  await clickShown(live)
  await expect(peek(page).getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
  expect(pathOf(page)).toBe('/agents')
  await page.keyboard.press('Escape')
  await expect(peek(page)).toHaveCount(0)
  await expect(live).toBeFocused()

  await clickShown(page.locator('.sessions').getByRole('link', { name: /Claude camy, Working/ }))
  await expect(page).toHaveURL(`/agents/${session(1)}`)
  await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  await clickShown(page.getByRole('complementary', { name: 'Session details' }).locator('.ticket-detail'))
  await expect(peek(page)).toBeVisible()
  await expect(page.getByRole('complementary', { name: 'Session details' })).toHaveCount(0)
  await expect(peek(page).getByRole('button', { name: 'Back to session' })).toBeVisible()
  expect(pathOf(page)).toBe(`/agents/${session(1)}`)
  const ticket = (await peek(page).boundingBox())!
  const list = (await page.locator('.sessions').boundingBox())!
  expect(list.x + list.width).toBeLessThanOrEqual(ticket.x + 2)
  await peek(page).getByRole('button', { name: 'Back to session' }).click()
  await expect(peek(page)).toHaveCount(0)
  await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  expect(pathOf(page)).toBe(`/agents/${session(1)}`)
})

test('a ticket from another project in the tickets graph opens the peek', async ({ page }) => {
  test.setTimeout(60_000)
  const data = ticketGraphWorld()
  data.graph = {
    nodes: [{ id: 'n-a1', key: 'AEON-1', title: 'Aeon foundation', type: 'ticket', status: 'backlog', status_category: 'open', priority: 'high', parent_id: null, release_id: null, updated_at: '2026-09-26T12:00:00Z', link_count: 0 }],
    links: [], truncated: false,
  }
  await mockTicketGraph(page, data)
  // This component fixture deliberately projects a cross-project relation.
  // Supply the same membership for the shared context read while retaining
  // AEON-1's real project for the peek and Open in project assertions below.
  await page.route('**/api/nodes?*', route => {
    const query = new URL(route.request().url()).searchParams
    return query.get('within') === 'p-pharos' && query.get('limit') === '500'
      ? route.fulfill({ json: { items: [{ id: 'n-a1' }], next_cursor: null } })
      : route.fallback()
  })
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto('/p/PHAROS/tickets?view=graph')
  const canvas = page.locator('.ticket-graph-canvas')
  await expect(canvas).toHaveAttribute('data-ready', 'true', { timeout: 20_000 })
  await clickGraphNode(canvas, 'n-a1', 20_000)
  await expect(peek(page).getByRole('heading', { name: 'Aeon foundation' })).toBeVisible()
  expect(pathOf(page)).toBe('/p/PHAROS/tickets')
  expect(new URL(page.url()).searchParams.get('view')).toBe('graph')
  expect(new URL(page.url()).searchParams.get('peek')).toBe('AEON-1')
  await openInProject(page, peek(page).getByRole('button', { name: 'Open in project' }), '/p/AEON/AEON-1')
})

test('a knowledge-graph ticket node opens the peek instead of leaving the graph', async ({ page }) => {
  test.setTimeout(60_000)
  await mockWork(page, fixtures())
  await mockKnowledge(page, knowledgeWorld())
  await page.route('**/api/knowledge/graph?*', route => {
    const include = new URL(route.request().url()).searchParams.get('include') === 'tickets'
    const entry = { id: 'k-1', key: 'PHAROS-K1', type: 'runbook', kind: 'knowledge', slug: 'deploy-release', title: 'Deploy a release', status: 'active', degree: 1, updated_at: '2026-09-26T12:00:00Z' }
    const ticket = { id: 'n-1', key: 'PHAROS-11', type: 'ticket', kind: 'ticket', slug: '', title: 'Connect Hetzner Cloud for managed provisioning', degree: 1, status: 'in-progress', updated_at: entry.updated_at }
    return route.fulfill({ json: { nodes: include ? [ticket] : [entry], edges: [], truncated: false } })
  })
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto('/p/PHAROS/knowledge?view=graph')
  const canvas = page.locator('.kg-canvas')
  await expect(canvas).toHaveAttribute('data-ready', 'true', { timeout: 30_000 })
  await page.getByRole('button', { name: 'Show linked tickets' }).click()
  await expect(canvas).toHaveAttribute('data-ready', 'true')
  await clickGraphNode(canvas, 'n-1', 30_000)
  await expect(peek(page).getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
  expect(pathOf(page)).toBe('/p/PHAROS/knowledge')
  expect(new URL(page.url()).searchParams.get('peek')).toBe('PHAROS-11')
})

test('release history still opens its own ticket panel', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const history = releaseHistory()
  await mockWork(page, fixtures())
  await mockReleases(page, history)
  await page.goto(`/releases/${history.releases[1].version}`)
  const sheet = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
  const chip = sheet.getByRole('link', { name: 'PHAROS-11: Connect Hetzner Cloud for managed provisioning' }).first()
  await chip.click()
  const panel = sheet.getByRole('complementary', { name: 'Ticket details' })
  await expect(panel.getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
  await expect(panel.getByRole('button', { name: 'Open in project' })).toBeVisible()
  expect(new URL(page.url()).searchParams.get('peek')).toBeNull()
  await expect(page).toHaveURL(`/releases/${history.releases[1].version}`)
  await openInProject(page, panel.getByRole('button', { name: 'Open in project' }), '/p/PHAROS/PHAROS-11')
})

test('ticket peek screenshots', async ({ page }) => {
  test.setTimeout(90_000)
  await openAgents(page)
  await clickShown(page.locator('.sessions').getByRole('link', { name: /Claude camy, Working/ }))
  await clickShown(page.getByRole('complementary', { name: 'Session details' }).locator('.ticket-detail'))
  await expect(peek(page).getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    for (const width of [1600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await expect(peek(page).getByRole('button', { name: 'Open in project' })).toBeVisible()
      await page.screenshot({ path: `../.agent-shots/ticket-peek-${colorScheme}-${width}.png` })
    }
  }
})
