// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

// setSystemTime lets time flow (setFixedTime would freeze Vue's event timestamps).
test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

test('projects list by last activity with classic keys, descriptions, counts and progress', async ({ page }) => {
  const errors = watchErrors(page)
  const calls = await mockWork(page, fixtures())
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
  const rows = page.getByRole('list', { name: 'Projects', exact: true }).getByRole('link')
  await expect(rows).toHaveCount(3)
  await expect(rows.nth(0)).toContainText('PHAROS')
  await expect(rows.nth(0)).toContainText('Fleet management and host access for the INSPR family.')
  await expect(rows.nth(1)).toContainText('AEON')
  await expect(rows.nth(1)).toContainText('The successor of Paimos.')
  await expect(rows.nth(2)).toContainText('PRJ-26')
  await expect(rows.nth(2)).toContainText('Frozen')
  await expect(rows.nth(0)).toHaveAttribute('href', '/p/PHAROS')
  await expect(rows.nth(0).locator('time')).toHaveText('2 hours ago')
  await expect(rows.nth(0).locator('time')).toHaveAttribute('data-tip', /Wed, 23 Sept? 2026/)
  await expect(page.getByText('3 projects ·')).toBeVisible()
  expect(calls.some(call => call.path === '/api/projects' && call.query.get('include_archived') === 'true')).toBe(true)
  expect(calls.filter(call => call.path === '/api/nodes' && call.query.get('kind') === 'project')).toHaveLength(1)
  expect(errors).toEqual([])
})

test('archived projects stay hidden until the Archived chip shows them, in their own group', async ({ page }) => {
  const calls = await mockWork(page, fixtures())
  await page.goto('/')
  const list = page.getByRole('list', { name: 'Projects', exact: true })
  await expect(list.getByRole('link')).toHaveCount(3)
  const chip = page.getByRole('group', { name: 'Groups' }).getByRole('button', { name: 'Archived, 1 project' })
  await expect(chip).toHaveAttribute('aria-pressed', 'false')
  await expect(page.getByText('Hidden:')).toBeVisible()
  await chip.click()
  await expect(chip).toHaveAttribute('aria-pressed', 'true')
  await expect(list.getByRole('link')).toHaveCount(4)
  await expect(page.getByRole('list', { name: 'Archived', exact: true }).getByRole('link', { name: /GLINT/ })).toBeVisible()
  await expect(page.getByText('Hidden:')).toHaveCount(0)
  // Shown or hidden is the person's own, remembered on the server.
  await expect.poll(() => calls.findLast(call => call.method === 'PUT' && call.path === '/api/preferences/project-groups')?.body).toEqual({ value: { hidden: [] } })
})

test('filtering highlights matches and an empty result offers to clear', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/')
  // The '/' shortcut is bound when the page mounts; press it once the list is there.
  await expect(page.getByRole('list', { name: 'Projects', exact: true }).getByRole('link').first()).toBeVisible()
  await page.keyboard.press('/')
  await expect(page.getByLabel('Filter projects')).toBeFocused()
  await page.keyboard.type('fleet')
  const list = page.getByRole('list', { name: 'Projects', exact: true })
  await expect(list.getByRole('link')).toHaveCount(1)
  await page.getByLabel('Filter projects').fill('pha')
  await expect(list.locator('mark')).toHaveText(['PHA', 'Pha'])
  await page.getByLabel('Filter projects').fill('nothing like this')
  await expect(page.getByRole('heading', { name: 'No project matches “nothing like this”' })).toBeVisible()
  await page.getByRole('button', { name: 'Clear filter' }).click()
  await expect(list.getByRole('link')).toHaveCount(3)
  await expect(page.getByLabel('Filter projects')).toBeFocused()
})

test('the sort control orders by name, open tickets and progress', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/')
  const names = page.getByRole('list', { name: 'Projects', exact: true }).locator('.name')
  await expect(names).toHaveText(['Pharos', 'Aeon', 'Studio infrastructure'])
  await page.getByRole('button', { name: /^Display/ }).click()
  await page.getByRole('dialog', { name: 'Display options' }).getByRole('radio', { name: 'Name' }).click()
  await expect(page).toHaveURL('/?sort=name')
  await expect(names).toHaveText(['Aeon', 'Pharos', 'Studio infrastructure'])
  await page.getByRole('dialog', { name: 'Display options' }).getByRole('radio', { name: 'Open tickets' }).click()
  await expect(names).toHaveText(['Pharos', 'Aeon', 'Studio infrastructure'])
  await page.keyboard.press('Escape')
  // The Display button keeps its word and shows the sort as its icon (AEON-174).
  await expect(page.getByRole('button', { name: 'Display, sorted by open tickets' })).toHaveAttribute('data-sort', 'open')
})

test('key badges share one column as wide as the widest badge', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/')
  const names = page.getByRole('list', { name: 'Projects', exact: true }).locator('.project-text')
  await expect(names).toHaveCount(3)
  const lefts = await names.evaluateAll(els => els.map(el => Math.round(el.getBoundingClientRect().left)))
  expect(new Set(lefts).size).toBe(1)
  const widest = Math.max(...await page.locator('.project-row .key-badge').evaluateAll(els => els.map(el => el.getBoundingClientRect().right)))
  expect(lefts[0] - widest).toBeLessThanOrEqual(21)
})

test('j, k and Enter open a project from the keyboard', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects', exact: true }).getByRole('link')).toHaveCount(3)
  await page.keyboard.press('j')
  await page.keyboard.press('j')
  await expect(page.getByRole('link', { name: /^AEON/ })).toBeFocused()
  await page.keyboard.press('k')
  await expect(page.getByRole('link', { name: /^PHAROS/ })).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL('/p/PHAROS/tickets')
  await expect(page.getByRole('heading', { name: 'Pharos', level: 1 })).toBeVisible()
  // The breadcrumb continues from the Projects place.
  await expect(page.getByRole('navigation', { name: 'Places' }).getByRole('link', { name: 'Projects' })).toHaveAttribute('aria-current', 'true')
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toHaveText('/PHAROSPharos')
})

test('loading shows skeleton rows; a failure explains itself and retries', async ({ page }) => {
  await mockWork(page, fixtures(), { failProjects: true })
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Projects could not be loaded' })).toBeVisible()
  await expect(page.getByText('Projects are resting')).toBeVisible()
  await page.unroute('**/api/**')
  let release: () => void = () => {}
  const gate = new Promise<void>(resolve => { release = resolve })
  await mockWork(page, fixtures())
  await page.route('**/api/projects*', async route => { await gate; await route.fallback() })
  await page.getByRole('button', { name: 'Try again' }).click()
  await expect(page.getByRole('status', { name: 'Loading projects' })).toBeVisible()
  release()
  await expect(page.getByRole('list', { name: 'Projects', exact: true }).getByRole('link')).toHaveCount(3)
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`projects stack on a phone in ${colorScheme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme })
    await mockWork(page, fixtures())
    await page.goto('/')
    const first = page.getByRole('list', { name: 'Projects', exact: true }).getByRole('link').first()
    await expect(first).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    const box = await first.boundingBox()
    expect(box!.width).toBeGreaterThan(330)
    await expect(first.locator('.stats-line')).toBeVisible()
    expect(errors).toEqual([])
  })
}
