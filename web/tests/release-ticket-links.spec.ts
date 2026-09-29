// SPDX-License-Identifier: AGPL-3.0-only
// Ticket keys in the release history (AEON-173): real links to the ticket's own
// page, a plain click opens the ticket in the app's side panel beside the
// history, keys this workspace does not have stay plain text.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const panel = (page: Page) => sheet(page).getByRole('complementary', { name: 'Ticket details' })
const chips = (page: Page) => sheet(page).locator('.detail .tickets')
const TITLE = 'Connect Hetzner Cloud for managed provisioning'

// The second release names AEON-74 (no such ticket here) and PHAROS-11 (a ticket here).
async function open(page: Page, data = fixtures()) {
  const history = releaseHistory()
  const calls = await mockWork(page, data)
  await mockReleases(page, history)
  await page.goto(`/releases/${history.releases[1].version}`)
  await expect(chips(page).getByRole('link', { name: `PHAROS-11: ${TITLE}` })).toBeVisible()
  return { history, calls }
}

test('a ticket key opens the ticket beside the history, with one lookup for the release', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const errors = watchErrors(page)
  const { history, calls } = await open(page)
  const chip = chips(page).getByRole('link', { name: `PHAROS-11: ${TITLE}` })
  await expect(chip).toHaveAttribute('href', '/p/PHAROS/PHAROS-11')
  // The key in the list of changes is the same link.
  await expect(sheet(page).locator('.changes').getByRole('link', { name: `PHAROS-11: ${TITLE}` })).toHaveAttribute('href', '/p/PHAROS/PHAROS-11')
  const lookups = calls.filter(call => call.path === '/api/nodes/lookup' && call.query.has('keys'))
  expect(lookups).toHaveLength(1)
  expect(lookups[0].query.get('keys')!.split(',').sort()).toEqual(['AEON-74', 'PHAROS-11'])

  await chip.click()
  await expect(panel(page).getByRole('heading', { name: TITLE })).toBeVisible()
  await expect(panel(page).getByRole('button', { name: /Status: In progress/ })).toBeVisible()
  await expect(chip).toHaveAttribute('aria-current', 'true')
  // The history stays: same address, the list and the release beside the ticket.
  await expect(page).toHaveURL(`/releases/${history.releases[1].version}`)
  await expect(sheet(page).getByRole('listbox', { name: 'Releases, newest first' })).toBeVisible()
  const detail = (await sheet(page).locator('.detail').boundingBox())!
  const aside = (await panel(page).boundingBox())!
  expect(aside.x).toBeGreaterThanOrEqual(detail.x + detail.width)
  expect(aside.x + aside.width).toBeLessThanOrEqual(1440)

  // The panel's menus work inside the history (they live in its top layer).
  await panel(page).getByRole('button', { name: 'More actions' }).click()
  await expect(page.getByRole('menu', { name: 'Actions for PHAROS-11' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('menu', { name: 'Actions for PHAROS-11' })).toHaveCount(0)
  await expect(panel(page)).toBeVisible()

  await panel(page).getByRole('button', { name: 'Close ticket details' }).click()
  await expect(panel(page)).toHaveCount(0)
  await expect(chip).toBeFocused()
  await expect(chip).not.toHaveAttribute('aria-current', 'true')
  expect(errors).toEqual([])
})

test('a modified click keeps the link: the ticket opens in a new tab, not the panel', async ({ page }) => {
  await open(page)
  const chip = chips(page).getByRole('link', { name: `PHAROS-11: ${TITLE}` })
  const [tab] = await Promise.all([page.context().waitForEvent('page'), chip.click({ modifiers: ['ControlOrMeta'] })])
  await tab.waitForURL('**/p/PHAROS/PHAROS-11')
  await tab.close()
  await expect(panel(page)).toHaveCount(0)
  await expect(sheet(page)).toBeVisible()
})

test('keys this workspace does not have stay plain text', async ({ page }) => {
  const { history } = await open(page)
  const plain = chips(page).getByText('AEON-74', { exact: true })
  await expect(plain).toHaveJSProperty('tagName', 'SPAN')
  await expect(plain).toHaveAttribute('data-tip', 'AEON-74 is not a ticket in this AEON workspace')
  await expect(sheet(page).locator('.changes').getByText('AEON-74', { exact: true }).first()).toHaveJSProperty('tagName', 'SPAN')
  // Another tracker's key (classic Paimos) likewise.
  await page.goto(`/releases/${history.releases[3].version}`)
  await expect(chips(page).getByText('PAI-1057', { exact: true })).toHaveJSProperty('tagName', 'SPAN')
  await expect(sheet(page).locator('.detail').getByRole('link', { name: /PAI-1057/ })).toHaveCount(0)
})

test('the keyboard opens the panel, Esc closes it and returns focus to the key', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await open(page)
  const chip = chips(page).getByRole('link', { name: `PHAROS-11: ${TITLE}` })
  await chip.focus()
  await page.keyboard.press('Enter')
  await expect(panel(page).getByRole('heading', { name: TITLE })).toBeVisible()
  await expect(panel(page)).toBeFocused()
  // The history's own keys stay out of the panel: j does not move the release.
  const selected = sheet(page).getByRole('option', { selected: true })
  const before = await selected.getAttribute('id')
  await page.keyboard.press('j')
  await expect(selected).toHaveAttribute('id', before!)
  await page.keyboard.press('Escape')
  await expect(panel(page)).toHaveCount(0)
  await expect(chip).toBeFocused()
  await expect(sheet(page)).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(sheet(page)).toHaveCount(0)
})

test('on a phone the ticket is a full-screen sheet over the history', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await open(page)
  const chip = chips(page).getByRole('link', { name: `PHAROS-11: ${TITLE}` })
  await chip.click()
  await expect(panel(page).getByRole('heading', { name: TITLE })).toBeVisible()
  const box = (await panel(page).boundingBox())!
  expect(box).toEqual({ x: 0, y: 0, width: 390, height: 844 })
  await panel(page).getByRole('button', { name: 'Close ticket details' }).click()
  await expect(panel(page)).toHaveCount(0)
  await expect(chip).toBeFocused()
})

// The server now answers no key (access lost, another workspace): registered last, so it wins.
const answerNothing = (page: Page) => page.route('**/api/nodes/lookup**', route => route.fulfill({ json: { items: [] } }))

test('a lost project access takes the link and the open ticket away at the next access check', async ({ page }) => {
  const { calls } = await open(page)
  const chip = chips(page).getByRole('link', { name: `PHAROS-11: ${TITLE}` })
  await chip.click()
  await expect(panel(page).getByRole('heading', { name: TITLE })).toBeVisible()
  const before = calls.filter(call => call.path === '/api/nodes/lookup').length
  await answerNothing(page)
  // Window focus asks for the same person's access again (as any navigation does).
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(chips(page).getByRole('link')).toHaveCount(0)
  await expect(chips(page).getByText('PHAROS-11', { exact: true })).toHaveJSProperty('tagName', 'SPAN')
  await expect(panel(page).getByRole('heading', { name: 'This ticket could not be opened' })).toBeVisible()
  await expect(sheet(page).getByText(TITLE)).toHaveCount(0)
  // Answered by the server under the new access, not from the earlier answers.
  expect(calls.filter(call => call.path === '/api/nodes/lookup').length).toBe(before)
})

test('another person or workspace starts without the earlier answers', async ({ page }) => {
  const { calls, history } = await open(page)
  await expect(chips(page).getByRole('link')).toHaveCount(1)
  const asked = calls.filter(call => call.path === '/api/nodes/lookup' && call.query.has('keys')).length
  let lookups = 0
  await page.route('**/api/nodes/lookup**', route => { lookups++; return route.fulfill({ json: { items: [] } }) })
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: '33333333-3333-4333-8333-333333333333', name: 'Ola Nordmann', kind: 'person', roles: ['member'] }, tenant: { id: 't2', name: 'Other Studio' } } }))
  // Any navigation refreshes the session: step to the next release and back, one settled step at a time.
  const rows = sheet(page).getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option')
  await rows.nth(2).click()
  await expect(page).toHaveURL(`/releases/${history.releases[2].version}`)
  await rows.nth(1).click()
  await expect(page).toHaveURL(`/releases/${history.releases[1].version}`)
  await expect(chips(page).getByText('PHAROS-11', { exact: true })).toHaveJSProperty('tagName', 'SPAN')
  await expect(sheet(page).getByRole('link', { name: new RegExp(TITLE) })).toHaveCount(0)
  expect(lookups).toBeGreaterThan(0)
  expect(calls.filter(call => call.path === '/api/nodes/lookup' && call.query.has('keys')).length).toBe(asked)
})

test('a ticket still on its way when the person or workspace changes never shows', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { history } = await open(page)
  // The panel's request for the ticket is slow; the answer (for the former caller) comes last.
  let held = 0
  await page.route('**/api/nodes?*', async route => {
    if (!new URL(route.request().url()).searchParams.has('q')) return route.fallback()
    held++
    await new Promise(resolve => setTimeout(resolve, 1500))
    return route.fallback().catch(() => {})
  })
  // Record the title if it is ever shown in a ticket panel, however briefly.
  await page.evaluate(title => {
    const w = window as unknown as { staleShown: boolean }
    w.staleShown = false
    new MutationObserver(() => { if (document.querySelector('.ticket-ws')?.textContent?.includes(title)) w.staleShown = true })
      .observe(document.body, { subtree: true, childList: true, characterData: true })
  }, TITLE)
  await chips(page).getByRole('link', { name: `PHAROS-11: ${TITLE}` }).click()
  await expect(panel(page).getByRole('status', { name: 'Loading ticket' })).toBeVisible()
  await expect.poll(() => held).toBe(1)
  // Now someone else, in another workspace that has no such ticket; a navigation refreshes the session.
  await answerNothing(page)
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: '33333333-3333-4333-8333-333333333333', name: 'Ola Nordmann', kind: 'person', roles: ['member'] }, tenant: { id: 't2', name: 'Other Studio' } } }))
  await sheet(page).getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option').nth(2).click()
  await expect(page).toHaveURL(`/releases/${history.releases[2].version}`)
  await expect(panel(page).getByRole('heading', { name: 'This ticket could not be opened' })).toBeVisible()
  // The held answer lands after this; it must not show.
  await page.waitForTimeout(2000)
  await expect(panel(page).getByRole('heading', { name: 'This ticket could not be opened' })).toBeVisible()
  expect(await page.evaluate(() => (window as unknown as { staleShown: boolean }).staleShown)).toBe(false)
})

test('a toast raised in the panel stays visible and actionable after the history closes', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { calls } = await open(page)
  await chips(page).getByRole('link', { name: `PHAROS-11: ${TITLE}` }).click()
  await panel(page).getByRole('button', { name: /Status: In progress/ }).click()
  await page.getByRole('menuitemradio', { name: 'Done' }).click()
  const toast = page.locator('.toast').filter({ hasText: 'PHAROS-11 is now Done' })
  await expect(toast).toBeVisible()
  await sheet(page).getByRole('button', { name: 'Close release history' }).click()
  await expect(sheet(page)).toHaveCount(0)
  await expect(toast).toBeVisible()
  await toast.getByRole('button', { name: 'Undo' }).click()
  await expect(toast).toHaveCount(0)
  const patches = calls.filter(call => call.method === 'PATCH' && call.path === '/api/nodes/n-1').map(call => (call.body as { state?: string }).state)
  expect(patches).toEqual(['done', 'in-progress'])
})

test('the status menu offers the project’s own states, as the project list does', async ({ page }) => {
  // PHAROS spells in-progress with a hyphen; the ticket itself is in the backlog.
  const data = fixtures()
  data.nodes.find(n => n.key === 'PHAROS-11')!.state = 'backlog'
  data.nodes.find(n => n.key === 'PHAROS-12')!.state = 'in-progress'
  const { calls } = await open(page, data)
  await chips(page).getByRole('link', { name: `PHAROS-11: ${TITLE}` }).click()
  await panel(page).getByRole('button', { name: /Status: Backlog/ }).click()
  await page.getByRole('menuitemradio', { name: 'In progress' }).click()
  await expect(panel(page).getByRole('button', { name: /Status: In progress/ })).toBeVisible()
  const patch = calls.find(call => call.method === 'PATCH' && call.path === '/api/nodes/n-1')
  expect((patch?.body as { state?: string }).state).toBe('in-progress')
})

for (const colorScheme of ['light', 'dark'] as const) {
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    test(`axe: a ticket open beside the history ${viewport.width} ${colorScheme}`, async ({ page }) => {
      await page.setViewportSize(viewport)
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
      await open(page)
      if (viewport.width > 600) await chips(page).getByRole('link', { name: `PHAROS-11: ${TITLE}` }).hover()
      await sheet(page).locator('.changes').getByRole('link', { name: `PHAROS-11: ${TITLE}` }).click()
      await expect(panel(page).getByRole('heading', { name: TITLE })).toBeVisible()
      await page.waitForTimeout(250)
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').analyze()
      const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')} — ${n.failureSummary?.split('\n').slice(1, 2).join(' ').trim()}`).join('\n')}`)
      expect(summary, summary.join('\n')).toEqual([])
    })
  }
}
