// SPDX-License-Identifier: AGPL-3.0-only
// The title column is flexible, so its divider used to drag without moving.
// Dragging it now fixes a width, the preference keeps it, the keyboard moves it,
// and a double-click gives the spare width back.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'

const rows = (page: Page) => page.locator('tr.ticket-row:not(.ghost)')
const titleHeader = (page: Page) => page.locator('thead th.c-title')
const titleHandle = (page: Page) => page.getByRole('separator', { name: 'Resize Title column' })

async function openList(page: Page) {
  await page.setViewportSize({ width: 1440, height: 900 })
  const data = fixtures()
  await mockWork(page, data)
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  return data
}

async function dragTitle(page: Page, dx: number) {
  const handle = titleHandle(page)
  const box = (await handle.boundingBox())!
  const x = box.x + box.width / 2
  const y = box.y + box.height / 2
  await page.mouse.move(x, y)
  await page.mouse.down()
  await page.mouse.move(x + dx, y, { steps: 8 })
  await page.mouse.up()
}

function savedTitle(data: { preferences: Record<string, Record<string, unknown>> }) {
  const widths = (data.preferences['list:p-pharos'] as { widths?: { title?: number } } | undefined)?.widths
  return widths?.title
}

test('dragging the title divider widens it and keeps the other columns within their bounds', async ({ page }) => {
  const data = await openList(page)
  const before = (await titleHeader(page).boundingBox())!.width
  await dragTitle(page, 80)
  await expect.poll(async () => Math.round((await titleHeader(page).boundingBox())!.width)).toBeGreaterThan(before + 40)
  await expect.poll(() => savedTitle(data) ?? 0).toBeGreaterThan(before + 40)
  for (const name of ['Key', 'Status', 'Priority', 'Assignee', 'Updated']) {
    const handle = page.getByRole('separator', { name: `Resize ${name} column` })
    const min = Number(await handle.getAttribute('aria-valuemin'))
    const now = Number(await handle.getAttribute('aria-valuenow'))
    expect(now).toBeGreaterThanOrEqual(min)
  }
})

test('the title width is still applied after a reload', async ({ page }) => {
  const data = await openList(page)
  const before = (await titleHeader(page).boundingBox())!.width
  await dragTitle(page, 80)
  await expect.poll(() => savedTitle(data) ?? 0).toBeGreaterThan(before + 40)
  const saved = savedTitle(data)!
  await page.reload()
  await expect(rows(page)).toHaveCount(5)
  await expect.poll(async () => Math.round((await titleHeader(page).boundingBox())!.width)).toBeGreaterThan(saved - 8)
  await expect.poll(async () => Math.round((await titleHeader(page).boundingBox())!.width)).toBeLessThan(saved + 8)
})

test('arrow keys resize the focused title divider and aria-valuenow follows', async ({ page }) => {
  await openList(page)
  const handle = titleHandle(page)
  await handle.focus()
  const before = Number(await handle.getAttribute('aria-valuenow'))
  expect(before).toBeGreaterThan(0)
  await page.keyboard.press('ArrowRight')
  await expect.poll(async () => Number(await handle.getAttribute('aria-valuenow'))).toBeGreaterThan(before + 8)
  const widened = Number(await handle.getAttribute('aria-valuenow'))
  await expect.poll(async () => Math.round((await titleHeader(page).boundingBox())!.width)).toBeGreaterThan(before + 8)
  await page.keyboard.press('ArrowLeft')
  await expect.poll(async () => Number(await handle.getAttribute('aria-valuenow'))).toBeLessThan(widened - 8)
})

test('double-clicking the title divider resets it to automatic', async ({ page }) => {
  const data = await openList(page)
  const before = Math.round((await titleHeader(page).boundingBox())!.width)
  await dragTitle(page, 80)
  await expect.poll(() => savedTitle(data) ?? 0).toBeGreaterThan(before + 40)
  await titleHandle(page).dblclick()
  await expect.poll(() => savedTitle(data)).toBeUndefined()
  await expect.poll(async () => Math.round((await titleHeader(page).boundingBox())!.width)).toBeLessThan(before + 24)
  await expect.poll(async () => Math.round((await titleHeader(page).boundingBox())!.width)).toBeGreaterThan(before - 24)
})
