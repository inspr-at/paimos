// SPDX-License-Identifier: AGPL-3.0-only
// AEON-326 slice 1a: two browsers on the same ticket. B's ticket panel follows
// A's changes through the live event stream: fields patch in place with a
// brief tint and a polite announcement, a change during an edit waits and
// meets the save as a conflict, and a deletion shows the ticket as gone.
import { expect, test, type Page } from '@playwright/test'
import { openBoth } from './live-server'

const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })

test('a change in one browser patches the open ticket in the other in place', async ({ browser }) => {
  const { a, b, live, errors, close } = await openBoth(browser, '/p/PHAROS/PHAROS-12')
  try {
    const wsA = panel(a), wsB = panel(b)
    await expect(wsB.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()

    await wsA.getByRole('heading', { name: 'Add an Oracle Cloud connector' }).click()
    await wsA.getByLabel('Title', { exact: true }).fill('Add an Oracle Cloud Always Free connector')
    await a.keyboard.press('Enter')
    await expect(wsA.getByRole('heading', { name: 'Add an Oracle Cloud Always Free connector' })).toBeVisible()

    // B: the title patches in place, tinted briefly, and is announced politely.
    const title = wsB.getByRole('heading', { name: 'Add an Oracle Cloud Always Free connector' })
    await expect(title).toBeVisible()
    await expect(wsB.locator('.inline-title')).toHaveClass(/live-tint/)
    await expect(wsB.locator('p.sr-only[role="status"]')).toHaveText('PHAROS-12 was updated elsewhere: title.')
    await expect(wsB.locator('.inline-title')).not.toHaveClass(/live-tint/, { timeout: 4000 })
    // The row under the panel is the same ticket, patched in place too.
    await expect(b.locator('#row-n-2')).toContainText('Always Free')

    await a.keyboard.press('p')
    await a.getByRole('menu', { name: 'Priority of PHAROS-12' }).getByRole('menuitemradio', { name: 'High' }).click()
    await expect(wsB.getByRole('button', { name: /Priority: High/ })).toBeVisible()
    await expect(wsB.locator('p.sr-only[role="status"]')).toHaveText('PHAROS-12 was updated elsewhere: priority.')
    // A's own change is not announced to A.
    await expect(wsA.locator('p.sr-only[role="status"]')).toHaveText('')

    // Every connection asked for live mode; reconnects resumed after the last event.
    expect(live.requests.every(r => r.after === 'latest')).toBe(true)
    expect(live.requests.filter(r => r.page === 'b' && r.resumed).length).toBeGreaterThan(0)
    expect(errors).toEqual([])
  } finally { await close() }
})

test('a change during an edit waits, meets the save as a conflict and keeps the draft', async ({ browser }) => {
  const { a, b, data, close } = await openBoth(browser, '/p/PHAROS/PHAROS-12')
  try {
    const wsA = panel(a), wsB = panel(b)
    await expect(wsB.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await wsB.getByRole('button', { name: 'Edit', exact: true }).click()
    const draftTitle = wsB.locator('#edit-title')
    await draftTitle.fill('Oracle connector, my wording')

    await a.keyboard.press('s')
    await a.getByRole('menu', { name: 'Status of PHAROS-12' }).getByRole('menuitemradio', { name: 'In progress' }).click()
    await expect(wsA.getByRole('button', { name: /Status: In progress/ })).toBeVisible()

    // B is told, and nothing moved under the editor.
    await expect(wsB.getByRole('status').filter({ hasText: 'Changed elsewhere meanwhile' })).toBeVisible()
    await expect(draftTitle).toHaveValue('Oracle connector, my wording')

    // The save still sends the revision B started from: a conflict, not an overwrite.
    await draftTitle.focus()
    await b.keyboard.press(`${mod}+Enter`)
    await expect(b.getByText('PHAROS-12 was changed elsewhere. The newer version is shown; your draft is kept.')).toBeVisible()
    await expect(draftTitle).toHaveValue('Oracle connector, my wording')
    await expect(wsB.getByRole('status').filter({ hasText: 'Changed elsewhere meanwhile' })).toHaveCount(0)
    // Saving again keeps A's status: the draft took the newer value where B did not edit.
    await b.keyboard.press(`${mod}+Enter`)
    await expect(wsB.getByRole('heading', { name: 'Oracle connector, my wording' })).toBeVisible()
    await expect(wsB.getByRole('button', { name: /Status: In progress/ })).toBeVisible()
    const node = data.nodes.find(n => n.id === 'n-2')!
    expect(node.title).toBe('Oracle connector, my wording')
    expect(node.state).not.toBe('backlog')
  } finally { await close() }
})

test('a ticket deleted in one browser reads as gone in the other, on a phone too', async ({ browser }) => {
  const { a, b, errors, close } = await openBoth(browser, '/p/PHAROS/PHAROS-14', { width: 390, height: 844 })
  try {
    const wsB = panel(b)
    await expect(wsB.getByRole('heading', { name: 'Visual acceptance of the version pill' })).toBeVisible()
    await panel(a).getByRole('button', { name: 'More actions' }).click()
    await a.getByRole('menuitem', { name: 'Delete ticket…' }).click()
    await a.getByRole('dialog', { name: 'Delete PHAROS-14?' }).getByRole('button', { name: 'Delete ticket' }).click()
    await expect(a).toHaveURL('/p/PHAROS/tickets')

    await expect(wsB.getByRole('alert')).toContainText('PHAROS-14 is no longer here')
    expect(await b.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    expect(errors).toEqual([])
  } finally { await close() }
})
