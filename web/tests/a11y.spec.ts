// SPDX-License-Identifier: AGPL-3.0-only
// axe (WCAG 2.1 A and AA) over every shipped screen, light and dark, on mocked data.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { businessData, mira, mockBusiness, type BusinessMockOptions } from './business-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'

const world = {
  me: me.id,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}
async function signedIn(page: Page, empty = false) {
  await mockWork(page, fixtures())
  await mockAgents(page, agentData({ ...world, empty }))
  await mockKnowledge(page, knowledgeWorld())
}
// The first request needs approvals.decide, approvals.decide_high and the
// underlying harness.control permission; the admin fixture grants all three.
async function signedInAdmin(page: Page) {
  await mockWork(page, fixtures(), { admin: true })
  await mockAgents(page, agentData({ ...world }))
  await mockKnowledge(page, knowledgeWorld())
}
function business(options: BusinessMockOptions = {}) {
  return async (page: Page) => {
    await mockWork(page, fixtures())
    await mockAgents(page, agentData({ ...world, empty: true }))
    await mockBusiness(page, businessData(options), options)
  }
}
// Another session changes the Thursday entry before this page's correction lands.
async function businessConflict(page: Page) {
  await mockWork(page, fixtures())
  await mockAgents(page, agentData({ ...world, empty: true }))
  const data = businessData()
  data.meanwhile['e-4'] = { note: 'Sketch v2 by Mira' }
  await mockBusiness(page, data)
}
async function editEntry(page: Page) {
  await expect(page.locator('[data-entry="e-4"]')).toBeVisible()
  await page.locator('[data-entry="e-4"]').focus()
  await page.keyboard.press('e')
  await expect(page.getByRole('form', { name: 'Edit entry' })).toBeVisible()
}
async function signedOut(page: Page) {
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/me') return route.fulfill({ status: 401, json: { error: 'unauthorized', dev_mode: true } })
    if (path === '/api/version') return route.fulfill({ json: { version: '260924020145.0.0', scheme: 'inspr-calendar-v2' } })
    return route.fulfill({ status: 404, json: { error: 'not found' } })
  })
}

// R18: the attention grid and selection controls must remain usable by people
// using a screen reader, including retained resolutions and view-only rows.
test('axe: Needs attention controls and retained resolutions in light and dark', async ({ page }) => {
  await signedIn(page)
  const items = [0, 1, 2].map(index => ({
    event_id: index + 1, node_id: `attention-${index}`, project_id: 'p-aeon', key: `AEON-${index + 10}`,
    title: 'Die vollständigen Abrechnungseinstellungen für sämtliche Arbeitsbereiche aktualisieren',
    kind: 'triage', from: 'new', to: 'backlog', reason: 'Review the current suggestion.',
    revision: '2026-10-04T08:00:00Z', at: '2026-10-03T08:00:00Z', editable: index !== 2, applicable: index !== 2,
    unavailable_reason: index === 2 ? 'Editing this ticket needs permission' : undefined,
  }))
  await page.route('**/api/status-autopilot/attention**', route => {
    const request = route.request(), path = new URL(request.url()).pathname
    if (path.endsWith('/actions')) return route.fulfill({ json: { items: request.postDataJSON().items.map((item: { event_id: number }) => ({ event_id: item.event_id, ok: true, resolution_event_id: 1000 + item.event_id, revision: '2026-10-05T08:00:00Z' })) } })
    if (path.endsWith('/groups')) return route.fulfill({ json: { total: 3, truncated: false, groups: [{ id: 'p-aeon', project_id: 'p-aeon', key: 'AEON', title: 'Aeon', total: 3, counts: { triage: 3 }, applicable: 2, editable: 2, override_mode: 'on', can_manage: false }] } })
    return route.fulfill({ json: { items, total: 3, counts: { triage: 3 }, next_cursor: null, facets: { projects: [], assignees: [] }, facets_truncated: false } })
  })
  for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width: theme === 'light' ? 1440 : 390, height: 1000 })
    await page.goto('/tickets?view=needs-attention')
    const grid = page.getByRole('grid', { name: 'Tickets needing attention' })
    const first = grid.locator('#row-attention-1')
    await expect(first).toBeVisible()
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    await page.evaluate(() => document.fonts.ready)
    const scan = async () => {
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.version-coordinate').exclude('.calendar-version').analyze()
      expect(results.violations.map(v => ({ rule: v.id, impact: v.impact, nodes: v.nodes.map(n => ({ target: n.target, failure: n.failureSummary })) }))).toEqual([])
    }
    await scan()
    await first.getByRole('button', { name: /^Apply to/ }).click()
    await expect(first.getByRole('button', { name: 'Undo for AEON-10' })).toBeFocused()
    await scan()
    await first.getByRole('button', { name: 'Undo for AEON-10' }).click()
    await grid.locator('.group-check').check()
    await expect(page.getByRole('toolbar', { name: 'Selected tickets' })).toBeVisible()
    await scan()
    await page.getByRole('toolbar', { name: 'Selected tickets' }).getByRole('button', { name: 'Clear the selection' }).click()
    await grid.locator('#row-group-p-aeon').getByRole('button', { name: 'More for AEON' }).click()
    const menu = page.getByRole('menu')
    await expect(menu).toBeVisible()
    // The phone sheet's heading, Close, and any alert sit beside the menu.
    // Only menuitem buttons may be owned by role=menu (axe aria-required-children).
    expect(await menu.evaluate(el => [...el.children].every(child => child.getAttribute('role') === 'menuitem'))).toBe(true)
    await expect(menu.getByRole('heading')).toHaveCount(0)
    await expect(menu.getByRole('button', { name: 'Close' })).toHaveCount(0)
    await expect(menu.getByRole('alert')).toHaveCount(0)
    if (theme === 'dark') {
      await expect(page.getByRole('heading', { name: 'More for AEON' })).toBeVisible()
      await expect(page.getByRole('button', { name: 'Close' })).toBeVisible()
      await page.getByRole('menuitem').first().focus()
      await page.keyboard.press('ArrowDown')
      await expect(page.getByRole('menuitem').nth(1)).toBeFocused()
    }
    await scan()
    await page.keyboard.press('Escape')
    await expect(grid.locator('#group-more-p-aeon')).toBeFocused()
  }
})

// name, setup, path, then what to wait for or do before the scan
const screens: [string, (page: Page) => Promise<void>, string, (page: Page) => Promise<void>][] = [
  ['projects', signedIn, '/', async page => { await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible() }],
  ['project list', signedIn, '/p/PHAROS', async page => { await expect(page.locator('tr.ticket-row:not(.ghost)')).toHaveCount(5) }],
  ['project outline', signedIn, '/p/PHAROS?view=outline', async page => { await expect(page.locator('tr.ticket-row:not(.ghost)').first()).toBeVisible() }],
  ['ticket panel', signedIn, '/p/PHAROS/PHAROS-11', async page => { await expect(page.getByRole('complementary', { name: 'Ticket details' }).getByRole('heading').first()).toBeVisible() }],
  ['ticket full page', signedIn, '/p/PHAROS/PHAROS-11?view=full', async page => { await expect(page.getByRole('article', { name: 'Ticket details' })).toBeVisible() }],
  ['command palette', signedIn, '/', async page => {
    await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
    await page.keyboard.press('Control+k'); await page.keyboard.type('hetzner')
    await expect(page.getByRole('dialog', { name: 'Search and commands' }).getByRole('option').first()).toBeVisible()
  }],
  ['account menu', signedIn, '/', async page => {
    await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
    await page.getByRole('button', { name: /^Account for/ }).click()
    await expect(page.getByRole('menu', { name: 'Account' })).toBeVisible()
  }],
  ['shortcut sheet', signedIn, '/p/PHAROS', async page => {
    await expect(page.locator('tr.ticket-row:not(.ghost)')).toHaveCount(5)
    await page.keyboard.press('Shift+?')
    await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible()
  }],
  ['sign-in', signedOut, '/signin', async page => { await expect(page.getByLabel('Email address')).toBeVisible() }],
  ['sign-in error', signedOut, '/signin?error=denied', async page => { await expect(page.getByRole('alert')).toBeVisible() }],
  ['not found', signedIn, '/nope/here', async page => { await expect(page.getByRole('heading', { name: 'A little off the path.' })).toBeVisible() }],
  ['error page', async page => {
    await signedIn(page)
    await page.route(url => url.pathname.endsWith('/src/views/AgentsView.vue'), route => route.fulfill({ contentType: 'application/javascript', body: 'throw new TypeError("The view could not start")' }))
  }, '/agents', async page => { await expect(page.getByRole('heading', { name: 'This page stumbled.' })).toBeVisible() }],
  ['agents', signedIn, '/agents', async page => { await expect(page.locator('.agents-page .row').first()).toBeVisible() }],
  ['agents empty', page => signedIn(page, true), '/agents', async page => { await expect(page.getByRole('heading', { name: 'No agent has connected yet' })).toBeVisible() }],
  ['agents session panel', signedIn, '/agents/5e000000-0000-4000-8000-000000000001?tab=messages', async page => {
    // AEON-313: unbound history of other sessions is no longer shown in this
    // thread, so the audit covers the conversation as the session shows it.
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel.getByRole('region', { name: 'Conversation' })).toBeVisible()
    await expect(panel.getByText(/^Other sessions of/)).toHaveCount(0)
  }],
  ['agents session overview', signedIn, '/agents/5e000000-0000-4000-8000-000000000001?tab=overview', async page => {
    await expect(page.getByRole('complementary', { name: 'Session details' }).locator('.now-step')).toBeVisible()
  }],
  ['business setup', business({ enabled: [] }), '/business', async page => { await expect(page.getByRole('heading', { name: 'Set up Business' })).toBeVisible() }],
  ['business overview', business(), '/business', async page => { await expect(page.getByRole('list', { name: 'Time per ticket' })).toBeVisible() }],
  ['hours week', business(), '/business/hours', async page => { await expect(page.locator('.week-grid tbody tr').first()).toBeVisible() }],
  ['hours ticket picker', business(), '/business/hours', async page => {
    await expect(page.locator('.week-grid tbody tr').first()).toBeVisible()
    await page.getByRole('button', { name: /^Ticket:/ }).click()
    await page.getByRole('combobox', { name: 'Ticket' }).fill('pharos')
    await expect(page.getByRole('listbox', { name: 'Ticket' }).getByRole('option').first()).toBeVisible()
  }],
  ['hours editing an entry', business(), '/business/hours', editEntry],
  ['hours entry conflict', businessConflict, '/business/hours', async page => {
    await editEntry(page)
    await page.keyboard.type('2h')
    await page.keyboard.press('Enter')
    await expect(page.getByRole('alert').filter({ hasText: 'This entry changed while you were editing.' })).toBeVisible()
  }],
  ['hours locked week', business(), `/business/hours?person=${mira.id}&week=2026-09-14`, async page => { await expect(page.locator('[data-entry="e-7"] .lock')).toBeVisible() }],
  ['hours approvals', business(), '/business/hours?view=approvals&period=p-me-38', async page => { await expect(page.getByRole('complementary', { name: 'Period review' }).locator('.t-row').first()).toBeVisible() }],
  ['rates', business(), '/business/rates', async page => { await expect(page.getByRole('table', { name: 'Rates of Development' })).toBeVisible() }],
  ['rates add form', business(), '/business/rates', async page => {
    await expect(page.getByRole('table', { name: 'Rates of Development' })).toBeVisible()
    await page.getByRole('button', { name: 'Add rate' }).nth(1).click()
    await expect(page.getByRole('combobox', { name: 'Unit' })).toBeFocused()
  }],
  ['knowledge tab', signedIn, '/p/PHAROS/knowledge', async page => { await expect(page.locator('.k-row').first()).toBeVisible() }],
  ['knowledge entry', signedIn, '/p/PHAROS/knowledge/runbook/deploy-release', async page => { await expect(page.locator('.e-body')).toBeVisible() }],
  ['knowledge across projects', signedIn, '/knowledge?q=deploy', async page => { await expect(page.locator('.kp-row').first()).toBeVisible() }],
  ['knowledge docked entry', signedIn, '/p/PHAROS/knowledge?entry=runbook/deploy-release', async page => { await page.setViewportSize({ width: 1440, height: 900 }); await expect(page.locator('.entry-page.dock .e-body')).toBeVisible() }],
  ['agents approve', signedInAdmin, '/agents', async page => {
    await expect(page.locator('.agents-page .row').first()).toBeVisible()
    await page.keyboard.press('j'); await page.keyboard.press('a')
    await expect(page.getByLabel('Reason (optional)')).toBeFocused()
  }],
]

for (const colorScheme of ['light', 'dark'] as const) {
  for (const [name, setup, path, ready] of screens) {
    test(`axe: ${name} in ${colorScheme}`, async ({ page }) => {
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
      await setup(page)
      await page.goto(path)
      await ready(page)
      await page.waitForTimeout(250)
      // The version coordinate (and the footer's calendar version) is the vendored INSPR calendar-version display (pinned presentation);
      // its digit colours are reported to its owner rather than restyled here.
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.version-coordinate').exclude('.calendar-version').analyze()
      const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')} — ${n.failureSummary?.split('\n').slice(1, 2).join(' ').trim()}`).join('\n')}`)
      expect(summary, summary.join('\n')).toEqual([])
    })
  }
}

// U11 screens: the wide panel with its context column, edit mode, the column
// picker and the attachment viewer.
const wideScreens: [string, number, string, (page: Page) => Promise<void>][] = [
  ['wide panel with attachments', 1920, '/p/PHAROS/PHAROS-11', async page => { await expect(page.locator('.attachments.gallery .tile').first()).toBeVisible() }],
  ['edit mode', 1440, '/p/PHAROS/PHAROS-11', async page => {
    await expect(page.locator('.attachments .tile').first()).toBeVisible()
    await page.keyboard.press('e')
    await expect(page.getByRole('form', { name: 'Edit PHAROS-11' })).toBeVisible()
  }],
  ['column picker', 1440, '/p/PHAROS', async page => {
    await expect(page.locator('tr.ticket-row:not(.ghost)')).toHaveCount(5)
    await page.getByRole('button', { name: /^Display/ }).click()
    await expect(page.getByRole('dialog', { name: 'Display options' }).getByRole('checkbox', { name: 'Epic' })).toBeVisible()
  }],
  ['attachment viewer', 1440, '/p/PHAROS/PHAROS-11', async page => {
    await page.locator('.attachments .tile').first().click()
    await expect(page.locator('dialog.lightbox .name')).toBeVisible()
  }],
  ['attachment compare', 1440, '/p/PHAROS/PHAROS-11', async page => {
    await page.locator('.attachments .tile').first().click()
    await page.keyboard.press('c')
    await expect(page.getByRole('slider', { name: 'Compare divide' })).toBeVisible()
  }],
]
for (const colorScheme of ['light', 'dark'] as const) {
  for (const [name, width, path, ready] of wideScreens) {
    test(`axe: ${name} in ${colorScheme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 1000 })
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
      await signedIn(page)
      await page.goto(path)
      await ready(page)
      await page.waitForTimeout(250)
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.version-coordinate').exclude('.calendar-version').analyze()
      const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')} — ${n.failureSummary?.split('\n').slice(1, 2).join(' ').trim()}`).join('\n')}`)
      expect(summary, summary.join('\n')).toEqual([])
    })
  }
}
