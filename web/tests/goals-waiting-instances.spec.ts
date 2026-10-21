// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1495: Waiting on you across instances, to Tick's approved design
// (aeon-1494-goals-inflight-gaps-approved.html, decision 2). Risks: the sibling's
// questions mix into this instance's order, the count hides how many wait elsewhere,
// a sibling link stays inside this app, a sibling that cannot be read breaks the band
// or takes a different slot (moving the goals), a person who is not the owner reads a
// sibling at all, and the asker or the Coming step goes missing. Phone keeps the count,
// the first question with its tag, "N more · 1 on pma" and Open Desk.
// Screenshots go to each test's output directory.
import { test, expect, type Locator, type Page, type TestInfo } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

test.use({ timezoneId: 'Europe/Vienna', locale: 'en-GB' })
const NOW = Date.parse('2026-10-21T17:00:00Z')
const HOUR = 3_600_000
const iso = (ms: number) => new Date(ms).toISOString()
const SIBLING = 'https://pma.example'
const SUMMARY = `${SIBLING}/api/decision-desk/owner-summary`
test.beforeEach(async ({ page }) => { await page.clock.install({ time: new Date(NOW) }) })

type Sibling = 'ok' | 'down'
async function mockBand(page: Page, options: { owner: boolean; sibling: Sibling }) {
  const reads = { sibling: 0, origins: [] as string[] }
  let state = options.sibling
  await mockWork(page, fixtures())
  await page.route(/\/api\/goals(\?.*)?$/, route => route.fulfill({ json: {
    items: [], totals: { moving: 0, queued: 0, paused: 0, done: 0 }, totals_partial: false, quiet_projects: { count: 0, keys: [] }, truncated: false, read_through: 1, generation: 1,
  }, headers: { 'Aeon-Contract': 'goals/1.5' } }))
  await page.route(/\/api\/decision-desk\/projection/, route => route.fulfill({ json: {
    items: [
      { id: '00000000-0000-4000-8000-000000000001', kind: 'question', project_id: 'p-aeon', revision: 1, title: 'Which identity should agent reviews run as for the gatekeeper go-live?', created_at: iso(NOW - 3 * HOUR), held: false, href: '', source: '', ticket_key: 'AEON-1394', asked_by: { id: 'p-lead', name: 'AEON-LEAD' } },
      { id: '00000000-0000-4000-8000-000000000002', kind: 'question', project_id: 'p-pharos', revision: 1, title: 'Run the 60-minute voice soak test on the Aithema test host?', created_at: iso(NOW - 2 * HOUR), held: true, href: '', source: '', asked_by: { id: 'p-ops', name: 'OPS' } },
      { id: '00000000-0000-4000-8000-000000000003', kind: 'approval', project_id: 'p-aeon', revision: 1, title: 'Approval · nodes.write', created_at: iso(NOW - HOUR), held: false, href: '', source: '' },
    ],
    counts: { open: 3, held: 1, chores: 0 }, has_more: false, as_of: iso(NOW),
    upcoming: [{ node_id: 'n-ait', check_id: 'human', project_id: 'p-aeon', ticket_key: 'AIT-140', label: 'Your Aithema test session (~20 min, whenever you like)' }],
  } }))
  await page.route(/\/api\/decision-desk\/instances$/, route => route.fulfill({ json: { here: 'ppm', siblings: options.owner ? [{ tag: 'pma', origin: SIBLING }] : [] } }))
  await page.route(SUMMARY, route => {
    reads.sibling++
    const origin = route.request().headers().origin ?? ''
    reads.origins.push(origin)
    const cors = { 'access-control-allow-origin': origin, 'access-control-allow-credentials': 'true', vary: 'Origin' }
    if (state === 'down') return route.fulfill({ status: 401, json: { error: 'authentication required' }, headers: cors })
    return route.fulfill({ headers: cors, json: {
      instance: 'pma', counts: { open: 1, held: 0 }, as_of: iso(NOW),
      items: [{ id: '00000000-0000-4000-8000-0000000000a1', kind: 'question', project_key: 'PORTAL', title: 'Deploy the new sign-in flow to the second test host?', asked_by: 'PORTAL', held: false, href: '/decision-desk?item=q:00000000-0000-4000-8000-0000000000a1' }],
    } })
  })
  return { reads, set: (next: Sibling) => { state = next } }
}
async function openBand(page: Page) {
  await page.goto('/?view=goals')
  const band = page.getByRole('region', { name: 'Waiting on you' })
  await expect(band.locator('.waiting-title')).toBeVisible()
  return band
}
const box = (locator: Locator) => locator.evaluate(el => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } })
async function shot(page: Page, info: TestInfo, name: string) { await page.screenshot({ path: info.outputPath(`${name}.png`), fullPage: true }) }

for (const width of [1440, 390] as const) {
  test(`the owner sees this instance first, then the sibling, with tags, askers and Coming at ${width}`, async ({ page }, info) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: width > 760 ? 900 : 844 })
    const { reads } = await mockBand(page, { owner: true, sibling: 'ok' })
    const band = await openBand(page)
    await expect(band.locator('.n-badge')).toHaveText('4')
    expect(reads.sibling).toBe(1)
    expect(reads.origins[0]).not.toBe(SIBLING)
    const lines = band.locator('.waiting-item')
    await expect(lines).toHaveCount(4)
    // This instance first, held first within it; then the sibling.
    await expect(lines.locator('.wtag')).toHaveText(['ppm', 'ppm', 'ppm', 'pma'])
    await expect(lines.nth(0)).toContainText('Run the 60-minute voice soak test')
    await expect(lines.nth(1)).toContainText('Which identity should agent reviews run as')
    await expect(lines.nth(3)).toContainText('Deploy the new sign-in flow')
    const coming = band.locator('.coming')
    await expect(coming).toContainText('Coming · Your Aithema test session (~20 min, whenever you like) (AIT-140)')
    const away = lines.nth(3)
    await expect(away).toHaveAttribute('href', `${SIBLING}/decision-desk?item=q:00000000-0000-4000-8000-0000000000a1`)
    await expect(away).toHaveAttribute('target', '_blank')
    await expect(away).toHaveAttribute('rel', 'noopener noreferrer')
    if (width > 760) {
      await expect(band.locator('.w-split')).toHaveText('· 1 on pma')
      await expect(lines.nth(0).locator('.wby')).toHaveText('· asked by OPS')
      await expect(lines.nth(1).locator('.wby')).toHaveText('· asked by AEON-LEAD')
      await expect(lines.nth(2).locator('.wby')).toHaveCount(0)
      await expect(away.locator('.wby')).toHaveText('· asked by PORTAL')
      await expect(away.locator('.wkey')).toHaveText('PORTAL')
      await expect(away.locator('.wopen svg')).toHaveCount(1)
      await expect(lines.nth(0).locator('.wopen svg')).toHaveCount(0)
      // The tag sits in the icon column, the key in the key column, for both instances.
      const [hereTag, awayTag, icon] = await Promise.all([box(lines.nth(0).locator('.wtag')), box(away.locator('.wtag')), box(band.locator('.w-ic'))])
      expect(Math.abs((hereTag.x + hereTag.width / 2) - (icon.x + icon.width / 2))).toBeLessThanOrEqual(1)
      expect(Math.abs((awayTag.x + awayTag.width / 2) - (icon.x + icon.width / 2))).toBeLessThanOrEqual(1)
    } else {
      // Phone: the first question with its tag, then "N more · 1 on pma", and Open Desk.
      await expect(lines.nth(0)).toBeVisible()
      for (const n of [1, 2, 3]) await expect(lines.nth(n)).toBeHidden()
      await expect(lines.nth(0).locator('.wtag')).toBeVisible()
      await expect(band.locator('.w-more')).toHaveText('3 more · 1 on pma')
      const open = await box(band.getByRole('link', { name: /Open Desk/ }))
      expect(open.height).toBeGreaterThanOrEqual(44)
    }
    await shot(page, info, `waiting-${width}-light`)
    await page.emulateMedia({ colorScheme: 'dark' })
    await shot(page, info, `waiting-${width}-dark`)
    expect(errors).toEqual([])
  })
}

test('an unreadable sibling is one quiet line in the same slot; Try again reads it in place', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const mock = await mockBand(page, { owner: true, sibling: 'down' })
  const band = await openBand(page)
  const failed = band.locator('.w-err')
  await expect(failed).toHaveText(/pma\s*couldn’t be read\s*·\s*Try again/)
  // This instance's lines stay; the count names only what was read.
  await expect(band.locator('.n-badge')).toHaveText('3')
  await expect(band.locator('.w-split')).toHaveCount(0)
  await expect(band.locator('a.waiting-item')).toHaveCount(3)
  const [slot, line, desk, first] = await Promise.all([box(failed), box(band.locator('a.waiting-item').last()), box(band.getByRole('link', { name: /Open Desk/ })), box(band.locator('a.waiting-item').first())])
  // The quiet line takes a question line's slot (0 px).
  expect(Math.abs(slot.height - line.height)).toBeLessThanOrEqual(0.5)
  await shot(page, info, 'waiting-1440-unreadable-light')
  await page.emulateMedia({ colorScheme: 'dark' })
  await shot(page, info, 'waiting-1440-unreadable-dark')
  mock.set('ok')
  await failed.getByRole('button', { name: 'Try again' }).click()
  const away = band.locator('a.waiting-item').last()
  await expect(away).toContainText('Deploy the new sign-in flow')
  await expect(band.locator('.n-badge')).toHaveText('4')
  // Reading again moved nothing above the slot, and the line kept its height.
  const [after, deskAfter, firstAfter] = await Promise.all([box(away), box(band.getByRole('link', { name: /Open Desk/ })), box(band.locator('a.waiting-item').first())])
  expect(after.y).toBeCloseTo(slot.y, 0)
  expect(Math.abs(after.height - slot.height)).toBeLessThanOrEqual(0.5)
  expect(deskAfter).toEqual(desk)
  expect(firstAfter).toEqual(first)
  expect(mock.reads.sibling).toBe(2)
})

test('a person who is not the owner never reads a sibling and sees no instance tags', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { reads } = await mockBand(page, { owner: false, sibling: 'ok' })
  const band = await openBand(page)
  await expect(band.locator('.n-badge')).toHaveText('3')
  await expect(band.locator('.waiting-item')).toHaveCount(3)
  await expect(band.locator('.wtag')).toHaveCount(0)
  await expect(band.locator('.waiting-item').first().locator('.wby')).toHaveText('· asked by OPS')
  expect(reads.sibling).toBe(0)
})
