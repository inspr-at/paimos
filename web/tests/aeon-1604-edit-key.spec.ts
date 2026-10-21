// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1604: e or c pressed while this project's grants are still in flight is
// kept for that ticket. e opens the editor once the person may edit and the
// ticket itself has loaded. c focuses the comment box once they may comment.
// A reader, or a ticket they have since left, gets neither. Neither does a key
// whose wait a field or a dialog interrupted, or one a refreshed denial spent.
// The press itself adds nothing to the page, so the heading stays where it is
// until the editor opens.
import { expect, test, type Page, type Route } from '@playwright/test'
import { mockEffectivePermissions } from './authz-fixtures'
import { fixtures, mockWork } from './work-fixtures'

const article = (page: Page) => page.getByRole('article', { name: 'Ticket details' })
const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })

function holdProjectPermissions() {
  let release = () => {}
  const held = new Promise<void>(resolve => { release = resolve })
  return {
    release: () => release(),
    hold: ({ path, method, query }: { path: string; method: string; query: URLSearchParams }) =>
      path === '/api/me/permissions' && method === 'GET' && query.has('project_id') ? { until: held } : undefined,
  }
}

// The ticket list and the ticket lookup both read GET /api/nodes; holding them
// keeps the workspace on its loading state. The project catalogue still answers.
function holdTicketLookups() {
  let release = () => {}
  const held = new Promise<void>(resolve => { release = resolve })
  return {
    release: () => release(),
    hold: ({ path, method, query }: { path: string; method: string; query: URLSearchParams }) =>
      path === '/api/nodes' && method === 'GET' && query.get('kind') !== 'project' ? { until: held } : undefined,
  }
}

async function painted(page: Page) {
  await page.evaluate(() => new Promise(resolve => { requestAnimationFrame(() => requestAnimationFrame(() => resolve(undefined))) }))
}

test('AEON-1604: e pressed before project permissions load opens the editor once they arrive', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const gate = holdProjectPermissions()
  try {
    await mockWork(page, fixtures(), { hold: gate.hold })
    await page.goto('/p/PHAROS/PHAROS-12?view=full')
    const heading = article(page).getByRole('heading', { name: 'Add an Oracle Cloud connector' })
    await expect(heading).toBeVisible()
    await expect(article(page).getByRole('button', { name: 'Edit', exact: true })).toHaveCount(0)
    const before = await heading.boundingBox()
    if (!before) throw new Error('heading has no box')
    await page.keyboard.press('e')
    await expect(page.locator('.edit-form')).toHaveCount(0)
    const after = await heading.boundingBox()
    if (!after) throw new Error('heading moved off screen')
    expect(Math.abs(after.x - before.x)).toBeLessThanOrEqual(0.5)
    expect(Math.abs(after.y - before.y)).toBeLessThanOrEqual(0.5)
    expect(Math.abs(after.width - before.width)).toBeLessThanOrEqual(0.5)
    expect(Math.abs(after.height - before.height)).toBeLessThanOrEqual(0.5)
    gate.release()
    await expect(page.locator('.edit-form .md-editor').first()).toHaveClass(/split/)
  } finally { gate.release() }
})

test('AEON-1604: a reader who presses e before permissions load gets no editor', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const gate = holdProjectPermissions()
  try {
    await mockWork(page, fixtures(), { readOnly: true, hold: gate.hold })
    await page.goto('/p/PHAROS/PHAROS-12?view=full')
    const details = article(page)
    await expect(details.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await expect(details.getByRole('button', { name: 'Edit', exact: true })).toHaveCount(0)
    await page.keyboard.press('e')
    await expect(page.locator('.edit-form')).toHaveCount(0)
    const answer = page.waitForResponse(response => response.url().includes('/api/me/permissions') && response.url().includes('project_id='))
    gate.release()
    await answer
    await painted(page)
    await expect(details.getByRole('note')).toContainText('You can read this ticket but not change it.')
    await expect(details.getByRole('button', { name: 'Edit', exact: true })).toHaveCount(0)
    await expect(page.locator('.edit-form')).toHaveCount(0)
  } finally { gate.release() }
})

test('AEON-1604: c pressed before project permissions load focuses the comment box once they arrive', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const gate = holdProjectPermissions()
  try {
    await mockWork(page, fixtures(), { hold: gate.hold })
    await page.goto('/p/PHAROS/PHAROS-13')
    const details = panel(page)
    await expect(details.getByRole('heading', { name: 'Run the disposable Hetzner end-to-end check' })).toBeVisible()
    const composer = details.getByLabel('Add a comment')
    await expect(composer).toBeDisabled()
    await page.keyboard.press('e')
    await page.keyboard.press('c')
    await expect(page.locator('.edit-form')).toHaveCount(0)
    await expect(composer).not.toBeFocused()
    gate.release()
    await expect(composer).toBeFocused()
    await expect(page.locator('.edit-form')).toHaveCount(0)
  } finally { gate.release() }
})

test('AEON-1604: e remembered for one ticket does not edit the ticket opened next', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const gate = holdProjectPermissions()
  try {
    await mockWork(page, fixtures(), { hold: gate.hold })
    await page.goto('/p/PHAROS/PHAROS-12?view=full')
    await expect(article(page).getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await page.keyboard.press('e')
    await page.goto('/p/PHAROS/PHAROS-11?view=full')
    const next = article(page).getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })
    await expect(next).toBeVisible()
    await expect(page.locator('.edit-form')).toHaveCount(0)
    const answer = page.waitForResponse(response => response.url().includes('/api/me/permissions') && response.url().includes('project_id='))
    gate.release()
    await answer
    await painted(page)
    await expect(next).toBeVisible()
    await expect(page.locator('.edit-form')).toHaveCount(0)
  } finally { gate.release() }
})

test('AEON-1604: e pressed while the ticket loads opens the editor when the ticket arrives after its permissions', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const gate = holdProjectPermissions()
  const lookups = holdTicketLookups()
  try {
    await mockWork(page, fixtures(), { hold: request => gate.hold(request) ?? lookups.hold(request) })
    await page.goto('/p/PHAROS/PHAROS-12?view=full')
    const details = article(page)
    const loading = details.getByRole('status', { name: 'Loading ticket' })
    await expect(loading).toBeVisible()
    await expect(details.getByRole('button', { name: 'Edit', exact: true })).toHaveCount(0)
    await page.keyboard.press('e')
    // Permissions first: Edit appears while the ticket is still loading.
    gate.release()
    await expect(details.getByRole('button', { name: 'Edit', exact: true })).toBeVisible()
    await painted(page)
    await expect(loading).toBeVisible()
    await expect(page.locator('.edit-form')).toHaveCount(0)
    lookups.release()
    await expect(page.locator('.edit-form .md-editor').first()).toHaveClass(/split/)
  } finally { gate.release(); lookups.release() }
})

test('AEON-1604: c is spent when the person enters and leaves a field before permissions load', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const gate = holdProjectPermissions()
  try {
    await mockWork(page, fixtures(), { hold: gate.hold })
    await page.goto('/p/PHAROS/PHAROS-13')
    const details = panel(page)
    await expect(details.getByRole('heading', { name: 'Run the disposable Hetzner end-to-end check' })).toBeVisible()
    const composer = details.getByLabel('Add a comment')
    await expect(composer).toBeDisabled()
    await page.keyboard.press('c')
    const search = page.getByRole('searchbox', { name: 'Search tickets in this project' })
    await search.click()
    await expect(search).toBeFocused()
    await page.keyboard.press('Escape')
    await expect(search).not.toBeFocused()
    const answer = page.waitForResponse(response => response.url().includes('/api/me/permissions') && response.url().includes('project_id='))
    gate.release()
    await answer
    await expect(composer).toBeEnabled()
    await painted(page)
    await expect(composer).not.toBeFocused()
  } finally { gate.release() }
})

test('AEON-1604: e is spent when a dialog opens and closes before permissions load', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const gate = holdProjectPermissions()
  try {
    await mockWork(page, fixtures(), { hold: gate.hold })
    await page.goto('/p/PHAROS/PHAROS-12')
    const details = panel(page)
    const heading = details.getByRole('heading', { name: 'Add an Oracle Cloud connector' })
    await expect(heading).toBeVisible()
    await expect(details.getByRole('button', { name: 'Edit', exact: true })).toHaveCount(0)
    await page.keyboard.press('e')
    await page.keyboard.press('Shift+F')
    const menu = page.getByRole('dialog', { name: 'Add a filter' })
    await expect(menu).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(menu).toHaveCount(0)
    await expect(heading).toBeVisible()
    gate.release()
    await expect(details.getByRole('button', { name: 'Edit', exact: true })).toBeVisible()
    await painted(page)
    await expect(page.locator('.edit-form')).toHaveCount(0)
  } finally { gate.release() }
})

test('AEON-1604: e is spent by a refreshed denial that lands after the request it waited on, so a later grant opens nothing', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  // Every project grant request waits here until this test answers it.
  const asked: Route[] = []
  await page.route(url => url.pathname === '/api/me/permissions' && url.searchParams.has('project_id'), route => { asked.push(route) })
  const answer = async (route: Route, role: 'member' | 'viewer') => {
    const projectId = new URL(route.request().url()).searchParams.get('project_id') ?? undefined
    const landed = page.waitForResponse(response => response.request() === route.request())
    await route.fulfill({ json: mockEffectivePermissions(role, projectId) })
    await landed
    await painted(page)
  }
  await page.goto('/p/PHAROS/PHAROS-12?view=full')
  const details = article(page)
  await expect(details.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
  await expect.poll(() => asked.length).toBe(1)
  await page.keyboard.press('e')
  // Window focus asks again; the request e waited on no longer counts.
  await page.evaluate(() => { window.dispatchEvent(new Event('focus')) })
  await expect.poll(() => asked.length).toBe(2)
  await answer(asked[0], 'member')
  await expect(details.getByRole('button', { name: 'Edit', exact: true })).toHaveCount(0)
  await answer(asked[1], 'viewer')
  await expect(details.getByRole('note')).toContainText('You can read this ticket but not change it.')
  await expect(page.locator('.edit-form')).toHaveCount(0)
  // A later grant arrives without another key press.
  await page.evaluate(() => { window.dispatchEvent(new Event('focus')) })
  await expect.poll(() => asked.length).toBe(3)
  await answer(asked[2], 'member')
  await expect(details.getByRole('button', { name: 'Edit', exact: true })).toBeVisible()
  await painted(page)
  await expect(page.locator('.edit-form')).toHaveCount(0)
})
