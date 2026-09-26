// SPDX-License-Identifier: AGPL-3.0-only
// PN1 / AEON-195: sections own view controls; old bookmarks and ticket links survive.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { journeyWorld, mockJourney } from './journey-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'

test.use({ launchOptions: { args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] } })
const sections = (page: Page) => page.getByRole('tablist', { name: 'Project sections' })
const ticketViews = (page: Page) => page.getByRole('tablist', { name: 'Ticket views' })
const knowledgeViews = (page: Page) => page.getByRole('tablist', { name: 'Knowledge views' })
const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })

async function setup(page: Page) {
  const errors = watchErrors(page)
  await mockWork(page, fixtures())
  await mockJourney(page, journeyWorld('plan'))
  const world = knowledgeWorld()
  await mockKnowledge(page, world)
  await page.route('**/api/knowledge/graph?*', route => {
    const nodes = world.entries.filter(n => n.project === 'p-pharos' && n.status !== 'archived').map(n => ({
      id: n.id, key: n.key, type: n.type, kind: 'knowledge', slug: n.slug, title: n.title, status: n.status, degree: 2, updated_at: n.updated_at,
    }))
    return route.fulfill({ json: { nodes, edges: nodes.slice(1).map((n, i) => ({ source: nodes[i].id, target: n.id, kind: 'mention', label: 'mentions' })), truncated: false } })
  })
  return errors
}

test('section tabs separate Tickets views, Journey and Knowledge views; each keeps its filters', async ({ page }) => {
  test.setTimeout(90_000)
  await page.setViewportSize({ width: 1600, height: 1000 })
  const errors = await setup(page)
  await page.goto('/p/PHAROS/tickets')
  await expect(sections(page).getByRole('tab')).toHaveText(['Tickets', 'Journey', 'Knowledge'])
  await expect(ticketViews(page).getByRole('tab')).toHaveText(['List', 'Outline'])
  await expect(ticketViews(page).getByRole('tab', { name: 'List', exact: true })).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('button', { name: 'Display: Display' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'New ticket', exact: true })).toBeVisible()
  await page.getByRole('searchbox', { name: 'Search tickets in this project' }).fill('Hetzner')
  await expect(page).toHaveURL(/q=Hetzner/)
  await ticketViews(page).getByRole('tab', { name: 'Outline', exact: true }).click()
  await expect(page).toHaveURL(/view=outline/)
  await expect(page.getByRole('treegrid', { name: 'Ticket outline' })).toBeVisible()
  await expect(page.getByRole('searchbox', { name: 'Search tickets in this project' })).toHaveValue('Hetzner')

  await sections(page).getByRole('tab', { name: 'Journey', exact: true }).click()
  await expect(page).toHaveURL('/p/PHAROS/journey')
  await expect(page.getByRole('navigation', { name: 'Project journey' })).toBeVisible()
  await expect(ticketViews(page)).toHaveCount(0)
  await expect(knowledgeViews(page)).toHaveCount(0)
  await page.getByRole('navigation', { name: 'Project journey' }).getByRole('button', { name: '1. Inspire, done' }).click()
  await expect(page).toHaveURL('/p/PHAROS/journey?stage=inspire')

  await sections(page).getByRole('tab', { name: 'Knowledge', exact: true }).click()
  await expect(knowledgeViews(page).getByRole('tab')).toHaveText(['Entries', 'Graph'])
  await expect(page.locator('.k-row')).toHaveCount(8)
  await expect(page.getByRole('button', { name: 'New knowledge entry' })).toBeVisible()
  await page.getByRole('searchbox', { name: 'Search knowledge in Pharos' }).fill('deploy')
  await expect(page).toHaveURL(/q=deploy/)
  await knowledgeViews(page).getByRole('tab', { name: 'Graph', exact: true }).click()
  await expect(page).toHaveURL(/view=graph/)
  await expect(page.locator('.kg-canvas')).toHaveAttribute('data-ready', 'true', { timeout: 15_000 })
  expect(new URL(page.url()).searchParams.has('mode')).toBe(false)
  await expect(page.getByRole('searchbox', { name: 'Search knowledge in Pharos' })).toHaveValue('deploy')

  await sections(page).getByRole('tab', { name: 'Tickets', exact: true }).click()
  await expect(ticketViews(page).getByRole('tab', { name: 'Outline', exact: true })).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('searchbox', { name: 'Search tickets in this project' })).toHaveValue('Hetzner')
  await sections(page).getByRole('tab', { name: 'Knowledge', exact: true }).click()
  await expect(knowledgeViews(page).getByRole('tab', { name: 'Graph', exact: true })).toHaveAttribute('aria-selected', 'true')
  await knowledgeViews(page).getByRole('tab', { name: 'Entries', exact: true }).click()
  await expect(page.locator('.kg-canvas')).toHaveCount(0)
  await expect(page.locator('.k-row').first()).toBeVisible()
  expect(errors).toEqual([])
})

for (const [old, canonical, section, view] of [
  ['/p/PHAROS', '/p/PHAROS/tickets', 'Tickets', 'List'],
  ['/p/PHAROS?view=list&q=Hetzner', '/p/PHAROS/tickets?view=list&q=Hetzner', 'Tickets', 'List'],
  ['/p/PHAROS?view=outline&closed=1', '/p/PHAROS/tickets?view=outline&closed=1', 'Tickets', 'Outline'],
  ['/p/PHAROS?view=journey&stage=plan', '/p/PHAROS/journey?stage=plan', 'Journey', ''],
  ['/projects/p-pharos/journey/plan', '/p/PHAROS/journey?stage=plan', 'Journey', ''],
  ['/p/PHAROS?view=knowledge', '/p/PHAROS/knowledge', 'Knowledge', 'Entries'],
  ['/p/PHAROS/knowledge?mode=graph', '/p/PHAROS/knowledge?view=graph', 'Knowledge', 'Graph'],
  ['/p/PHAROS/knowledge?view=entries&mode=graph', '/p/PHAROS/knowledge?view=entries', 'Knowledge', 'Entries'],
  ['/p/PHAROS/knowledge/guideline?mode=graph', '/p/PHAROS/knowledge?type=guideline&view=graph', 'Knowledge', 'Graph'],
]) test(`legacy bookmark replaces history: ${old}`, async ({ page }) => {
  await setup(page)
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible()
  await page.goto(old)
  await expect(page).toHaveURL(canonical)
  await expect(sections(page).getByRole('tab', { name: section, exact: true })).toHaveAttribute('aria-selected', 'true')
  if (view) await expect((section === 'Tickets' ? ticketViews(page) : knowledgeViews(page)).getByRole('tab', { name: view, exact: true })).toHaveAttribute('aria-selected', 'true')
  await page.goBack()
  await expect(page).toHaveURL('/')
})

for (const [section, view] of [['tickets', 'list'], ['tickets', 'outline'], ['journey', ''], ['knowledge', 'entries'], ['knowledge', 'graph']]) {
  test(`ticket side panel retains ${section}/${view} through reload, expand, collapse and close`, async ({ page }) => {
    await page.setViewportSize({ width: 1600, height: 1000 })
    const errors = await setup(page)
    const query = new URLSearchParams({ section, ...(view ? { view } : { stage: 'plan' }) })
    await page.goto(`/p/PHAROS/PHAROS-11?${query}`)
    if (section === 'tickets') await expect(page).toHaveURL(`/p/PHAROS/PHAROS-11?view=${view}`)
    await expect(panel(page).getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
    await expect(sections(page).getByRole('tab', { name: section[0].toUpperCase() + section.slice(1), exact: true })).toHaveAttribute('aria-selected', 'true')
    await panel(page).getByRole('button', { name: 'Open as full page' }).click()
    await expect(page).toHaveURL(/panel=full/)
    await page.reload()
    await page.getByRole('button', { name: 'Show beside the list' }).click()
    await expect(panel(page)).toBeVisible()
    await expect(sections(page).getByRole('tab', { name: section[0].toUpperCase() + section.slice(1), exact: true })).toHaveAttribute('aria-selected', 'true')
    if (view) await expect((section === 'tickets' ? ticketViews(page) : knowledgeViews(page)).getByRole('tab', { name: view[0].toUpperCase() + view.slice(1), exact: true })).toHaveAttribute('aria-selected', 'true')
    await panel(page).getByRole('button', { name: 'Close ticket details' }).click()
    await expect(page).toHaveURL(`/p/PHAROS/${section}?${view ? `view=${view}` : 'stage=plan'}`)
    await expect(panel(page)).toHaveCount(0)
    expect(errors).toEqual([])
  })
}

test('an open ticket stays mounted as its background section changes', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await setup(page)
  await page.goto('/p/PHAROS/tickets')
  await page.locator('#row-n-1').click()
  await expect(panel(page)).toBeVisible()
  for (const section of ['Journey', 'Knowledge', 'Tickets']) {
    await sections(page).getByRole('tab', { name: section, exact: true }).click()
    await expect(page).toHaveURL(section === 'Tickets'
      ? '/p/PHAROS/PHAROS-11'
      : new RegExp(`/p/PHAROS/PHAROS-11\\?section=${section.toLowerCase()}`))
    await expect(panel(page).getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
  }
  await panel(page).getByRole('button', { name: 'Close ticket details' }).click()
  await expect(page).toHaveURL('/p/PHAROS/tickets')
})

test('legacy ticket links still open a panel, including the Journey and full-page variants', async ({ page }) => {
  await setup(page)
  await page.goto('/p/PHAROS/PHAROS-11?view=journey&stage=plan')
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-11?stage=plan&section=journey')
  await expect(panel(page)).toBeVisible()
  await expect(sections(page).getByRole('tab', { name: 'Journey', exact: true })).toHaveAttribute('aria-selected', 'true')
  await page.goto('/p/PHAROS/PHAROS-11?view=full')
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-11?view=full')
  await expect(page.getByRole('button', { name: 'Show beside the list' })).toBeVisible()
})

test('a linked ticket opened from an existing Knowledge entry keeps Knowledge behind it', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 })
  await setup(page)
  await page.goto('/p/PHAROS/knowledge/runbook/deploy-release')
  await page.locator('.link-row').filter({ hasText: 'PHAROS-11' }).click()
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-11?section=knowledge')
  await expect(panel(page)).toBeVisible()
  await expect(sections(page).getByRole('tab', { name: 'Knowledge', exact: true })).toHaveAttribute('aria-selected', 'true')
  await panel(page).getByRole('button', { name: 'Close ticket details' }).click()
  await expect(page).toHaveURL('/p/PHAROS/knowledge')
})

test('a legacy graph selection on a phone stays in Graph and Entries clears the selection', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page)
  await page.goto('/p/PHAROS/knowledge?mode=graph&entry=runbook/deploy-release')
  await expect(page).toHaveURL(url => url.pathname === '/p/PHAROS/knowledge' && url.searchParams.get('view') === 'graph' && url.searchParams.get('entry') === 'runbook/deploy-release' && !url.searchParams.has('mode'))
  await expect(page.locator('.kg-canvas')).toHaveAttribute('data-ready', 'true', { timeout: 15_000 })
  await knowledgeViews(page).getByRole('tab', { name: 'Entries', exact: true }).click()
  await expect(page).toHaveURL('/p/PHAROS/knowledge?view=entries')
  await expect(page.locator('.k-row')).toHaveCount(8)
})

test('switching Knowledge view while a search waits for the session guard keeps both changes', async ({ page }) => {
  await setup(page)
  await page.goto('/p/PHAROS/knowledge?view=entries')
  await expect(page.locator('.k-row')).toHaveCount(8)
  let entered!: () => void, release!: () => void
  const requested = new Promise<void>(resolve => { entered = resolve })
  const allowed = new Promise<void>(resolve => { release = resolve })
  let held = false
  await page.route('**/api/me', async route => {
    if (!held) { held = true; entered(); await allowed }
    await route.fallback()
  })
  await knowledgeViews(page).getByRole('tab', { name: 'Graph', exact: true }).click()
  await requested
  await page.getByRole('searchbox', { name: 'Search knowledge in Pharos' }).fill('deploy')
  release()
  await expect(page).toHaveURL(url => url.searchParams.get('view') === 'graph' && url.searchParams.get('q') === 'deploy')
  await expect(knowledgeViews(page).getByRole('tab', { name: 'Graph', exact: true })).toHaveAttribute('aria-selected', 'true')
})

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`${theme} ${width}px: SVG tabs, arrow keys, focus order and no horizontal page scroll`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.emulateMedia({ colorScheme: theme })
    await setup(page)
    await page.route('**/api/preferences/theme', route => route.fulfill({ json: { key: 'theme', value: theme } }))
    await page.goto('/p/PHAROS/tickets')
    const tickets = sections(page).getByRole('tab', { name: 'Tickets', exact: true })
    await tickets.focus()
    await page.keyboard.press('ArrowRight')
    await expect(sections(page).getByRole('tab', { name: 'Journey', exact: true })).toBeFocused()
    await expect(page).toHaveURL('/p/PHAROS/journey')
    await page.keyboard.press('End')
    await expect(sections(page).getByRole('tab', { name: 'Knowledge', exact: true })).toBeFocused()
    await expect(page).toHaveURL('/p/PHAROS/knowledge')
    await page.keyboard.press('Tab')
    await expect(knowledgeViews(page).getByRole('tab', { name: 'Entries', exact: true })).toBeFocused()
    await page.keyboard.press('ArrowLeft')
    await expect(knowledgeViews(page).getByRole('tab', { name: 'Graph', exact: true })).toBeFocused()
    await expect(page).toHaveURL('/p/PHAROS/knowledge?view=graph')
    await expect(page.locator('.kg-canvas')).toHaveAttribute('data-ready', 'true', { timeout: 15_000 })
    await page.keyboard.press('Home')
    await expect(page).toHaveURL('/p/PHAROS/knowledge?view=entries')
    await expect(page.locator('.k-row').first()).toBeVisible()
    for (const section of ['Tickets', 'Journey', 'Knowledge']) {
      await sections(page).getByRole('tab', { name: section, exact: true }).click()
      await expect(sections(page).getByRole('tab', { name: section, exact: true })).toHaveAttribute('aria-selected', 'true')
      await expect(sections(page).locator('[tabindex="0"]')).toHaveCount(1)
      await expect(sections(page).locator('button > svg')).toHaveCount(3)
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
      for (const tab of await sections(page).getByRole('tab').all()) {
        const box = await tab.boundingBox()
        expect(box!.x).toBeGreaterThanOrEqual(0)
        expect(box!.x + box!.width).toBeLessThanOrEqual(width)
      }
    }
    await sections(page).getByRole('tab', { name: 'Tickets', exact: true }).click()
    await ticketViews(page).getByRole('tab', { name: 'List', exact: true }).focus()
    await page.keyboard.press('ArrowRight')
    await expect(page).toHaveURL('/p/PHAROS/tickets?view=outline')
    await expect(ticketViews(page).getByRole('tab', { name: 'Outline', exact: true })).toBeFocused()
    await page.keyboard.press('Enter') // Must not invoke the list's open-ticket shortcut.
    await expect(panel(page)).toHaveCount(0)
  })
}
