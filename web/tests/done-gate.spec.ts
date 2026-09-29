// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors, type Call } from './work-fixtures'

const shots = '/private/tmp/claude-501/-Users-markus-Code-aeon/a4527da9-f872-45f5-a2f2-48dde0ce2ce5/scratchpad/shots/aeon-303/iter1'
const benefits = {
  pill_en: 'Clear release notes',
  pill_de: 'Verständliche Release Notes',
  benefit_en: 'Tickets explain what you gain.',
  benefit_de: 'Tickets erklären den Nutzen.',
}
const gate = (page: Page) => page.getByRole('dialog', { name: "Before it's done: what does the user gain?" })
const grid = (page: Page) => page.getByRole('grid', { name: 'Tickets' })
const row = (page: Page, key: string) => grid(page).locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

async function scene(page: Page, theme: 'light' | 'dark', width: number) {
  await page.emulateMedia({ colorScheme: theme })
  await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
}

async function expectLayout(page: Page, width: number) {
  const dialog = gate(page)
  const en = await dialog.getByLabel('Pill · English').boundingBox()
  const de = await dialog.getByLabel('Pill · Deutsch').boundingBox()
  expect(en && de).toBeTruthy()
  if (width === 1600) expect(de!.x).toBeGreaterThan(en!.x + en!.width)
  else {
    expect(de!.y).toBeGreaterThan(en!.y + en!.height)
    const card = await dialog.locator('form').boundingBox()
    const viewport = page.viewportSize()!
    expect(card).toBeTruthy()
    expect(card!.y + card!.height).toBeGreaterThan(viewport.height - 30)
  }
  const pageOverflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  const cardOverflow = await dialog.locator('form').evaluate(el => el.scrollWidth - el.clientWidth)
  expect(pageOverflow).toBeLessThanOrEqual(1)
  expect(cardOverflow).toBeLessThanOrEqual(1)
}

for (const theme of ['light', 'dark'] as const) {
  for (const width of [1600, 390] as const) {
    test(`one ticket asks before Done (${width} ${theme})`, async ({ page }) => {
      const errors = watchErrors(page)
      await scene(page, theme, width)
      const calls = await mockWork(page, fixtures())
      await page.goto('/p/PHAROS')
      await row(page, 'PHAROS-11').getByRole('button', { name: /Change status of PHAROS-11/ }).click()
      await page.getByRole('menuitemradio', { name: 'Done' }).click()
      const dialog = gate(page)
      await expect(dialog).toBeVisible()
      await expect(dialog.getByText('PHAROS-11', { exact: true })).toBeVisible()
      await expect(dialog.getByText('2–4 words', { exact: true }).first()).toBeVisible()
      await dialog.getByLabel('Pill · English').fill('Hosts')
      await expect(dialog.locator('.count.off')).toHaveText('1 word of 2–4')
      await expectLayout(page, width)
      mkdirSync(shots, { recursive: true })
      await page.screenshot({ path: `${shots}/dialog-${width}-${theme}.png`, fullPage: false })
      await dialog.getByRole('button', { name: 'Mark done' }).click()
      await expect(dialog).toBeVisible()
      await expect(dialog.getByLabel('Pill · English')).toHaveAttribute('aria-invalid', 'true')
      await dialog.getByLabel('Pill · English').fill(benefits.pill_en)
      await dialog.getByLabel('Pill · Deutsch').fill(benefits.pill_de)
      await dialog.getByLabel('Benefit · English').fill(benefits.benefit_en)
      await dialog.getByLabel('Benefit · Deutsch').fill(benefits.benefit_de)
      await dialog.getByRole('button', { name: 'Mark done' }).click()
      await expect(dialog).toHaveCount(0)
      await expect(page.getByText('PHAROS-11 is now Done')).toBeVisible()
      const patch = calls.find(call => call.method === 'PATCH' && call.path === '/api/nodes/n-1')
      expect(patch?.headers['if-unmodified-since']).toBeTruthy()
      const body = patch?.body as { state: string; fields: Record<string, unknown> }
      expect(body.state).toBe('done')
      expect(body.fields).toMatchObject({
        priority: 'high', assignee: me.id, release: { id: 5668, label: 'v4.7.8' }, ...benefits,
      })
      expect(body.fields.tags).toEqual([
        { id: 16, name: 'CUSTOMERPORTAL', color: 'blue' },
        { id: 5, name: 'hsb8', color: 'green' },
      ])
      expect(body.fields.hide_from_release_notes).toBeUndefined()
      await expect(page.getByText('before done:')).toHaveCount(0)
      await expect(page.getByText('pill_en is required')).toHaveCount(0)
      expect(errors).toEqual([])
    })

    test(`bulk Done asks only for the ticket that is not ready (${width} ${theme})`, async ({ page }) => {
      const errors = watchErrors(page)
      await scene(page, theme, width)
      const data = fixtures()
      Object.assign(data.nodes.find(node => node.key === 'PHAROS-12')!.fields, benefits)
      const calls = await mockWork(page, data)
      await page.goto('/p/PHAROS')
      await expect(grid(page).locator('tr.ticket-row:not(.ghost)')).toHaveCount(5)
      await grid(page).focus()
      await page.keyboard.press('x')
      await page.keyboard.press('Shift+J')
      await page.keyboard.press('Shift+J')
      await page.keyboard.press('s')
      await page.keyboard.press('7')
      const dialog = gate(page)
      await expect(dialog).toBeVisible()
      await expect(dialog.getByText('PHAROS-11', { exact: true })).toBeVisible()
      const bulk = calls.filter(call => call.path === '/api/nodes/bulk')
      expect(bulk).toHaveLength(1)
      expect(bulk[0].body).toEqual({ ids: ['n-2', 'n-3'], state: 'done' })
      await expect(page.getByText('2 tickets are now Done')).toBeVisible()
      if (width === 390) await page.keyboard.press('Escape')
      else await dialog.getByRole('button', { name: 'Not now' }).click()
      await expect(dialog).toHaveCount(0)
      await expect(row(page, 'PHAROS-11').locator('.status-btn')).toHaveText('In progress')
      await expect(row(page, 'PHAROS-12')).toHaveCount(0)
      await expect(row(page, 'PHAROS-13')).toHaveCount(0)
      await expect(page.getByText('before done:')).toHaveCount(0)
      await expect(page.getByText('pill_en is required')).toHaveCount(0)
      expect(errors).toEqual([])
    })

    test(`keyboard Done asks before the status changes (${width} ${theme})`, async ({ page }) => {
      const errors = watchErrors(page)
      await scene(page, theme, width)
      const calls = await mockWork(page, fixtures())
      await page.goto('/p/PHAROS')
      await expect(grid(page).locator('tr.ticket-row:not(.ghost)')).toHaveCount(5)
      await page.keyboard.press('j')
      await expect(row(page, 'PHAROS-11')).toHaveAttribute('aria-selected', 'true')
      await page.keyboard.press('s')
      await page.keyboard.press('7')
      const dialog = gate(page)
      await expect(dialog).toBeVisible()
      await expect(dialog.getByText('PHAROS-11', { exact: true })).toBeVisible()
      await dialog.getByLabel('Pill · English').fill(benefits.pill_en)
      await dialog.getByLabel('Pill · Deutsch').fill(benefits.pill_de)
      await dialog.getByLabel('Benefit · English').fill(benefits.benefit_en)
      await dialog.getByLabel('Benefit · Deutsch').fill(benefits.benefit_de)
      await dialog.getByRole('button', { name: 'Mark done' }).click()
      await expect(page.getByText('PHAROS-11 is now Done')).toBeVisible()
      const patch = calls.find((call: Call) => call.method === 'PATCH' && call.path === '/api/nodes/n-1')
      expect((patch?.body as { state?: string; fields?: { pill_en?: string } }).state).toBe('done')
      expect((patch?.body as { fields?: { pill_en?: string } }).fields?.pill_en).toBe(benefits.pill_en)
      await expect(page.getByText('before done:')).toHaveCount(0)
      expect(errors).toEqual([])
    })
  }
}
