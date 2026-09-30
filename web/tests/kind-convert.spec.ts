// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, type Call } from './work-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })

async function noHorizontalScroll(page: Page) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(1)
}

for (const width of [1600, 390]) {
  test(`convert a ticket to an epic at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    const data = fixtures()
    const calls = await mockWork(page, data)
    await page.goto('/p/PHAROS/PHAROS-11')
    const ws = panel(page)
    await ws.getByRole('button', { name: 'More actions' }).click()
    await page.getByRole('menuitem', { name: 'Convert to…' }).click()
    const sheet = page.getByRole('dialog', { name: 'Convert PHAROS-11' })
    await expect(sheet.getByText('PHAROS-11 keeps its key, history, relations, comments and attachments, and becomes an epic.')).toBeVisible()
    await sheet.getByRole('button', { name: 'Convert to epic' }).click()
    await expect(sheet).toBeHidden()
    await expect(ws.locator('dd').filter({ hasText: 'Epic' })).toBeVisible()
    await expect(page.getByText('PHAROS-11 is now epic')).toBeVisible()
    const post = calls.find((call: Call) => call.method === 'POST' && call.path === '/api/nodes/n-1/convert')
    expect(post?.body).toEqual({ to_kind: 'epic' })
    expect(post?.headers['if-unmodified-since']).toBeTruthy()
    await noHorizontalScroll(page)
  })
}

test('convert lists children the new kind cannot keep', async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 900 })
  const data = fixtures()
  const calls = await mockWork(page, data)
  await page.route('**/api/kinds', route => route.fulfill({
    json: {
      items: ['epic', 'ticket', 'task', 'project'].map(slug => ({
        id: `k-${slug}`, slug, label: slug[0].toUpperCase() + slug.slice(1), short_prefix: slug.slice(0, 3).toUpperCase(), icon: slug,
        allowed_child_kinds: slug === 'ticket' ? ['task'] : null,
        field_schema: {},
      })),
    },
  }))
  await page.goto('/p/PHAROS/PHAROS-10')
  const ws = panel(page)
  await ws.getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Convert to…' }).click()
  const sheet = page.getByRole('dialog', { name: 'Convert PHAROS-10' })
  await sheet.getByRole('radio', { name: 'Ticket' }).click()
  await expect(sheet.getByRole('list', { name: 'Blocking children' })).toContainText('PHAROS-11')
  await expect(sheet.getByRole('list', { name: 'Blocking children' })).toContainText('PHAROS-12')
  await expect(sheet.getByRole('button', { name: 'Convert to ticket' })).toBeDisabled()
  await sheet.getByRole('radio', { name: 'Task' }).click()
  await expect(sheet.getByRole('button', { name: 'Convert to task' })).toBeEnabled()
  expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/convert'))).toHaveLength(0)
  await page.setViewportSize({ width: 390, height: 844 })
  await noHorizontalScroll(page)
})

test('arrow keys move the kind choice and escape returns to more actions', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  const data = fixtures()
  await mockWork(page, data)
  await page.goto('/p/PHAROS/PHAROS-10')
  const more = panel(page).getByRole('button', { name: 'More actions' })
  await more.click()
  await page.getByRole('menuitem', { name: 'Convert to…' }).click()
  const sheet = page.getByRole('dialog', { name: 'Convert PHAROS-10' })
  const ticket = sheet.getByRole('radio', { name: 'Ticket' })
  const task = sheet.getByRole('radio', { name: 'Task' })
  await expect(ticket).toBeChecked()
  await ticket.focus()
  await ticket.press('ArrowDown')
  await expect(task).toBeChecked()
  await expect(task).toBeFocused()
  await task.press('ArrowUp')
  await expect(ticket).toBeChecked()
  await expect(ticket).toBeFocused()
  await ticket.press('ArrowRight')
  await expect(task).toBeChecked()
  await expect(task).toBeFocused()
  await task.press('ArrowLeft')
  await expect(ticket).toBeChecked()
  await expect(ticket).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(sheet).toBeHidden()
  await expect(more).toBeFocused()
})

test('a long blocker list scrolls inside the sheet at 390', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const data = fixtures()
  const epic = data.nodes.find(node => node.id === 'n-epic')
  if (epic) epic.fields = { priority: 'high', scratch: 'keep' }
  for (let index = 0; index < 30; index++) {
    data.nodes.push({
      id: `n-block-${index}`, key: `PHAROS-${20 + index}`, kind_slug: 'ticket', title: `Blocker ${index + 1}`,
      body: '', state: 'open', fields: {}, parent_id: 'n-epic', project: 'p-pharos',
      created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:00:00Z',
    })
  }
  await mockWork(page, data)
  await page.route('**/api/kinds', route => route.fulfill({
    json: {
      items: ['epic', 'ticket', 'task', 'project'].map(slug => ({
        id: `k-${slug}`, slug, label: slug[0].toUpperCase() + slug.slice(1), short_prefix: slug.slice(0, 3).toUpperCase(), icon: slug,
        allowed_child_kinds: slug === 'ticket' ? ['task'] : null,
        field_schema: slug === 'task' ? { type: 'object', additionalProperties: false, properties: { priority: { type: 'string' } } } : {},
      })),
    },
  }))
  await page.goto('/p/PHAROS/PHAROS-10')
  await panel(page).getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Convert to…' }).click()
  const sheet = page.getByRole('dialog', { name: 'Convert PHAROS-10' })
  await sheet.getByRole('radio', { name: 'Ticket' }).click()
  await expect(sheet.getByRole('list', { name: 'Blocking children' }).getByRole('listitem')).toHaveCount(32)
  const cancel = sheet.getByRole('button', { name: 'Cancel' })
  const box = await cancel.boundingBox()
  expect(box).toBeTruthy()
  expect(box!.y).toBeGreaterThanOrEqual(0)
  expect(box!.y + box!.height).toBeLessThanOrEqual(844)
  const scrolled = await sheet.locator('.convert-scroll').evaluate(el => el.scrollHeight > el.clientHeight)
  expect(scrolled).toBe(true)
  await sheet.getByRole('radio', { name: 'Task' }).click()
  await expect(sheet.getByRole('list', { name: 'Fields that move to the history' })).toContainText('scratch')
  await expect(sheet.getByRole('button', { name: 'Convert to task' })).toBeEnabled()
  await noHorizontalScroll(page)
})
