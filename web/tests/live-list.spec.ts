// SPDX-License-Identifier: AGPL-3.0-only
// AEON-326 slice 1b: two browsers on the same project list. A's list follows
// B's changes live: field changes patch rows in place; a ticket closed or
// deleted in B stays in A's list, dimmed and labelled, behind "N updates ·
// Show", and leaves by itself only when A is idle with nothing selected.
// Bulk and inline changes that meet a newer version come back as conflicts
// with Review, never as overwrites.
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { openBoth } from './live-server'

const shots = process.env.LIVE_SHOTS_DIR
const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })
const bulkBar = (page: Page) => page.getByRole('toolbar', { name: /selected ticket/ })
const said = (page: Page) => page.locator('p.live-said')
const mod = process.platform === 'darwin' ? 'Meta' : 'Control'

async function setStatus(page: Page, id: string, key: string, status: string) {
  await page.locator(`#row-${id} .status-btn`).click()
  await page.getByRole('menu', { name: `Status of ${key}` }).getByRole('menuitemradio', { name: status }).click()
}
async function deleteInPanel(page: Page, key: string) {
  await page.goto(`/p/PHAROS/${key}`)
  await panel(page).getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Delete ticket…' }).click()
  await page.getByRole('dialog', { name: `Delete ${key}?` }).getByRole('button', { name: 'Delete ticket' }).click()
  await expect(page).toHaveURL('/p/PHAROS/tickets')
}

test('a ticket closed elsewhere dims in place, then leaves by itself when nothing is going on', async ({ browser }) => {
  const { a, b, errorsA, close } = await openBoth(browser, '/p/PHAROS/tickets')
  try {
    const row = a.locator('#row-n-4')
    await expect(row).toBeVisible()
    await setStatus(b, 'n-4', 'PHAROS-14', 'Cancelled')

    // A: the row keeps its place, dimmed with a label, and the pill counts it.
    await expect(row.locator('.live-label')).toHaveText('Closed')
    await expect(row).toHaveClass(/stale/)
    await expect(a.getByRole('button', { name: '1 update · Show' })).toBeVisible()
    await expect(said(a)).toHaveText('PHAROS-14 was closed elsewhere. Press U to show updates.')
    // Nothing selected, open or moving: after 2 s it applies by itself.
    await expect(row).toHaveCount(0, { timeout: 6000 })
    await expect(a.locator('.live-pill')).toHaveCount(0)
    await expect(a.locator('#row-n-2')).toBeVisible()
    expect(errorsA).toEqual([])
  } finally { await close() }
})

test('with a selection the update waits; Show applies it and keeps the rest of the selection', async ({ browser }) => {
  const { a, b, errorsA, close } = await openBoth(browser, '/p/PHAROS/tickets')
  try {
    for (const id of ['n-1', 'n-2', 'n-4']) await a.locator(`#row-${id} .row-check`).check()
    await expect(bulkBar(a)).toHaveAttribute('aria-label', '3 selected tickets')

    await deleteInPanel(b, 'PHAROS-14')

    const row = a.locator('#row-n-4')
    await expect(row.locator('.live-label')).toHaveText('Deleted')
    await expect(bulkBar(a).getByText('1 selected ticket was deleted')).toBeVisible()
    await expect(a.getByRole('button', { name: '1 update · Show' })).toBeVisible()
    if (shots) { mkdirSync(shots, { recursive: true }); await a.screenshot({ path: `${shots}/list-selection-deleted__1600__light.png` }) }
    // With a selection nothing applies by itself.
    await a.waitForTimeout(3000)
    await expect(row).toBeVisible()

    await a.getByRole('button', { name: '1 update · Show' }).click()
    await expect(row).toHaveCount(0)
    await expect(bulkBar(a)).toHaveAttribute('aria-label', '2 selected tickets')
    await expect(bulkBar(a).getByText(/was deleted/)).toHaveCount(0)
    await expect(a.locator('#row-n-1')).toHaveClass(/selected/)
    await expect(a.locator('#row-n-2')).toHaveClass(/selected/)
    expect(errorsA).toEqual([])
  } finally { await close() }
})

test('field changes patch in place; a regrouped row waits as moved until u', async ({ browser }) => {
  const { a, b, errorsA, close } = await openBoth(browser, '/p/PHAROS/tickets?group=status')
  try {
    const group = (label: string) => a.locator('tbody').filter({ has: a.locator('.group-label', { hasText: new RegExp(`^${label}$`) }) })
    await expect(group('Backlog').locator('#row-n-2')).toBeVisible()

    // A title changes in place: tinted briefly and announced, no pill.
    await b.goto('/p/PHAROS/PHAROS-12?group=status')
    await panel(b).getByRole('heading', { name: 'Add an Oracle Cloud connector' }).click()
    await panel(b).getByLabel('Title', { exact: true }).fill('Add an Oracle Cloud Always Free connector')
    await b.keyboard.press('Enter')
    const row = a.locator('#row-n-2')
    await expect(row).toContainText('Always Free')
    await expect(row).toHaveClass(/live-flash/)
    await expect(said(a)).toHaveText('PHAROS-12 was updated elsewhere: title.')
    await expect(a.locator('.live-pill')).toHaveCount(0)
    await expect(row).not.toHaveClass(/live-flash/, { timeout: 4000 })

    // A status change would regroup it: it stays in Backlog, marked, until A asks.
    await panel(b).getByRole('button', { name: /Status:/ }).first().click()
    await b.getByRole('menu', { name: 'Status of PHAROS-12' }).getByRole('menuitemradio', { name: 'In progress' }).click()
    await expect(row.locator('.live-label')).toHaveText('Moved')
    await expect(group('Backlog').locator('#row-n-2')).toBeVisible()
    await expect(row.locator('.c-status')).toContainText('In progress')
    // u applies it at once.
    await a.locator('table.tickets').focus()
    await a.keyboard.press('u')
    await expect(group('In progress').locator('#row-n-2')).toBeVisible()
    await expect(row.locator('.live-label')).toHaveCount(0)
    await expect(a.locator('.live-pill')).toHaveCount(0)
    expect(errorsA).toEqual([])
  } finally { await close() }
})

test('bulk and inline changes that meet a newer version are conflicts with Review', async ({ browser }) => {
  const { a, b, live, data, close } = await openBoth(browser, '/p/PHAROS/tickets')
  try {
    // A hears nothing for a while (a slow connection), so it still shows the old versions.
    live.hold('a')
    await b.goto('/p/PHAROS/PHAROS-12')
    await expect(panel(b).getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await b.keyboard.press('p')
    await b.getByRole('menu', { name: 'Priority of PHAROS-12' }).getByRole('menuitemradio', { name: 'High' }).click()
    await expect(panel(b).getByRole('button', { name: /Priority: High/ })).toBeVisible()

    // Bulk: PHAROS-14 changes, PHAROS-12 is a conflict and keeps B's value.
    for (const id of ['n-2', 'n-4']) await a.locator(`#row-${id} .row-check`).check()
    await bulkBar(a).getByRole('button', { name: 'Priority' }).click()
    await a.getByRole('menu', { name: 'Priority of 2 tickets' }).getByRole('menuitemradio', { name: 'Low' }).click()
    const conflict = a.locator('.toast').filter({ hasText: 'PHAROS-12 was changed elsewhere meanwhile and kept its newer version.' })
    await expect(conflict).toBeVisible()
    expect(data.nodes.find(n => n.id === 'n-2')!.fields.priority).toBe('high')
    expect(data.nodes.find(n => n.id === 'n-4')!.fields.priority).toBe('low')
    // Review: the conflicted ticket becomes the selection, with its newer value.
    await conflict.getByRole('button', { name: 'Review' }).click()
    await expect(bulkBar(a)).toHaveAttribute('aria-label', '1 selected ticket')
    await expect(a.locator('#row-n-2')).toHaveClass(/selected/)
    await expect(a.locator('#row-n-2 .c-prio')).toContainText('High')
    await a.keyboard.press('Escape')

    // Inline: a status change on a row B changed meanwhile is not saved; Review opens it.
    await b.goto('/p/PHAROS/PHAROS-14')
    await panel(b).getByRole('heading', { name: 'Visual acceptance of the version pill' }).click()
    await panel(b).getByLabel('Title', { exact: true }).fill('Visual acceptance of the version pill, dark too')
    await b.keyboard.press('Enter')
    await expect(panel(b).getByRole('heading', { name: /dark too/ })).toBeVisible()
    await setStatus(a, 'n-4', 'PHAROS-14', 'In progress')
    const inline = a.locator('.toast').filter({ hasText: 'PHAROS-14 was changed elsewhere, so your status change was not saved.' })
    await expect(inline).toBeVisible()
    expect(data.nodes.find(n => n.id === 'n-4')!.state).toBe('new')
    await inline.getByRole('button', { name: 'Review' }).click()
    await expect(a).toHaveURL(/\/p\/PHAROS\/PHAROS-14/)
    await expect(panel(a).getByRole('heading', { name: /dark too/ })).toBeVisible()
  } finally { await close() }
})

test('a selected row keeps the version A saw: a bulk change meets the newer one as a conflict', async ({ browser }) => {
  const { a, b, data, errorsA, close } = await openBoth(browser, '/p/PHAROS/tickets')
  try {
    for (const id of ['n-2', 'n-4']) await a.locator(`#row-${id} .row-check`).check()
    await b.goto('/p/PHAROS/PHAROS-12')
    await expect(panel(b).getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await b.keyboard.press('p')
    await b.getByRole('menu', { name: 'Priority of PHAROS-12' }).getByRole('menuitemradio', { name: 'High' }).click()

    // A: the selected row waits as changed, with the priority A saw.
    const row = a.locator('#row-n-2')
    await expect(row.locator('.live-label')).toHaveText('Changed')
    await expect(row.locator('.c-prio')).not.toContainText('High')
    await expect(a.getByRole('button', { name: '1 update · Show' })).toBeVisible()
    await expect(said(a)).toHaveText('PHAROS-12 was updated elsewhere: priority. Press U to show updates.')

    await bulkBar(a).getByRole('button', { name: 'Priority' }).click()
    await a.getByRole('menu', { name: 'Priority of 2 tickets' }).getByRole('menuitemradio', { name: 'Low' }).click()
    await expect(a.locator('.toast').filter({ hasText: 'PHAROS-12 was changed elsewhere meanwhile and kept its newer version.' })).toBeVisible()
    expect(data.nodes.find(n => n.id === 'n-2')!.fields.priority).toBe('high')
    expect(data.nodes.find(n => n.id === 'n-4')!.fields.priority).toBe('low')
    expect(errorsA).toEqual([])
  } finally { await close() }
})

test('the row under an open editor keeps its revision: saving meets the change as a conflict', async ({ browser }) => {
  const { a, b, data, errorsA, close } = await openBoth(browser, '/p/PHAROS/tickets')
  try {
    await a.locator('#row-n-2 .title-link').click()
    const wsA = panel(a)
    await wsA.getByRole('button', { name: 'Edit', exact: true }).click()
    await wsA.locator('#edit-title').fill('Oracle connector, my wording')

    await b.goto('/p/PHAROS/PHAROS-12')
    await expect(panel(b).getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await b.keyboard.press('p')
    await b.getByRole('menu', { name: 'Priority of PHAROS-12' }).getByRole('menuitemradio', { name: 'High' }).click()
    await expect(panel(b).getByRole('button', { name: /Priority: High/ })).toBeVisible()

    // The list read lands too, and waits: the row A edits keeps what A saw.
    const row = a.locator('#row-n-2')
    await expect(row.locator('.live-label')).toHaveText('Changed')
    await expect(wsA.getByRole('status').filter({ hasText: 'Changed elsewhere meanwhile' })).toBeVisible()
    await wsA.locator('#edit-title').focus()
    await a.keyboard.press(`${mod}+Enter`)
    await expect(a.getByText('PHAROS-12 was changed elsewhere. The newer version is shown; your draft is kept.')).toBeVisible()
    expect(data.nodes.find(n => n.id === 'n-2')!.title).toBe('Add an Oracle Cloud connector')
    expect(data.nodes.find(n => n.id === 'n-2')!.fields.priority).toBe('high')
    expect(errorsA).toEqual([])
  } finally { await close() }
})

test('Show brings a waiting change into a selected row', async ({ browser }) => {
  const { a, b, errorsA, close } = await openBoth(browser, '/p/PHAROS/tickets')
  try {
    await a.locator('#row-n-2 .row-check').check()
    await b.goto('/p/PHAROS/PHAROS-12')
    await panel(b).getByRole('heading', { name: 'Add an Oracle Cloud connector' }).click()
    await panel(b).getByLabel('Title', { exact: true }).fill('Add an Oracle Cloud Always Free connector')
    await b.keyboard.press('Enter')
    const row = a.locator('#row-n-2')
    await expect(row.locator('.live-label')).toHaveText('Changed')
    await expect(row).not.toContainText('Always Free')
    await a.getByRole('button', { name: '1 update · Show' }).click()
    await expect(row).toContainText('Always Free')
    await expect(row.locator('.live-label')).toHaveCount(0)
    await expect(row).toHaveClass(/selected/)
    expect(errorsA).toEqual([])
  } finally { await close() }
})

test('a list read that fails is tried again', async ({ browser }) => {
  const { a, b, errorsA, close } = await openBoth(browser, '/p/PHAROS/tickets')
  try {
    let failed = 0
    await a.route(url => url.pathname === '/api/nodes' && url.searchParams.has('ids'), route => {
      if (!failed++) return route.fulfill({ status: 503, json: { error: 'Unavailable' } })
      return route.fallback()
    })
    await b.goto('/p/PHAROS/PHAROS-12')
    await panel(b).getByRole('heading', { name: 'Add an Oracle Cloud connector' }).click()
    await panel(b).getByLabel('Title', { exact: true }).fill('Add an Oracle Cloud Always Free connector')
    await b.keyboard.press('Enter')
    await expect.poll(() => failed).toBeGreaterThan(0)
    await expect(a.locator('#row-n-2')).toContainText('Always Free', { timeout: 6000 })
    expect(failed).toBeGreaterThan(1)
    expect(errorsA.filter(error => !/503/.test(error))).toEqual([])
  } finally { await close() }
})

test('on a phone the marked row, the pill and the deleted selection fit 390 px', async ({ browser }) => {
  for (const colorScheme of ['light', 'dark'] as const) {
    const { a, b, errorsA, close } = await openBoth(browser, '/p/PHAROS/tickets', { a: { width: 390, height: 844 }, colorScheme })
    try {
      await a.getByRole('button', { name: 'Select' }).click()
      for (const key of ['PHAROS-11', 'PHAROS-14']) await a.getByRole('checkbox', { name: `Select ${key}` }).click()
      await deleteInPanel(b, 'PHAROS-14')
      const row = a.locator('#row-n-4')
      await expect(row.locator('.live-label')).toHaveText('Deleted')
      await expect(a.locator('.phone-pick-status')).toContainText(/2 selected\s*·\s*1 deleted\s*·\s*Cancel/)
      await expect(a.getByRole('button', { name: '1 update · Show' })).toBeVisible()
      expect(await a.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
      if (shots) { mkdirSync(shots, { recursive: true }); await a.screenshot({ path: `${shots}/list-phone-deleted__390__${colorScheme}.png` }) }
      await a.getByRole('button', { name: '1 update · Show' }).click()
      await expect(row).toHaveCount(0)
      await expect(a.locator('.phone-pick-status')).toContainText('1 selected')
      expect(errorsA).toEqual([])
    } finally { await close() }
  }
})

// Screenshots for review (LIVE_SHOTS_DIR): the waiting states at 1600, light and dark.
test('screenshots of the waiting states', async ({ browser }) => {
  test.skip(!shots, 'set LIVE_SHOTS_DIR to capture')
  for (const colorScheme of ['light', 'dark'] as const) {
    const { a, b, close } = await openBoth(browser, '/p/PHAROS/tickets', { a: { width: 1600, height: 1000 }, colorScheme })
    try {
      await a.locator('#row-n-1 .row-check').check()
      await setStatus(b, 'n-4', 'PHAROS-14', 'Cancelled')
      await setStatus(b, 'n-2', 'PHAROS-12', 'In progress')
      await expect(a.locator('#row-n-4 .live-label')).toHaveText('Closed')
      await a.mouse.move(800, 900)
      mkdirSync(shots!, { recursive: true })
      await a.screenshot({ path: `${shots}/list-waiting__1600__${colorScheme}.png` })
      await a.getByRole('button', { name: /update/ }).click()
      await expect(a.locator('.live-pill')).toHaveCount(0)
      await a.screenshot({ path: `${shots}/list-applied__1600__${colorScheme}.png` })
    } finally { await close() }
  }
})
