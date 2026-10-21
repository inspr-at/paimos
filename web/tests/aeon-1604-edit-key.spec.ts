// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1604: e or c pressed while this project's grants are still in flight is
// kept for that ticket. e opens the editor once the person may edit. c focuses
// the comment box once they may comment. A reader, or a ticket they have since
// left, gets neither. The press itself adds nothing to the page, so the heading
// stays where it is until the editor opens.
import { expect, test, type Page } from '@playwright/test'
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
