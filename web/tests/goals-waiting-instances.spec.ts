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
const keptGoal = {
  id: 'g-wait', key: 'G-WAIT', title: 'Keep the header still', kind: 'goal', project_id: 'p-aeon', workspace: false, expansion: 'full',
  counts: { outcomes: 1, tickets: 1 }, items: [], steps: [], next_step: null,
  projects: [{ id: 'p-aeon', key: 'AEON', name: 'Aeon', group_ids: [] }], waits: { count: 0, question_ids: [] },
  progress: { pct: 0, computed_pct: 0, source: 'computed', basis: 'count', total: 1, done: 0, items_without_estimate: 0, items_partially_estimated: 0 },
  eta: { source: 'none', partial: false }, passed: false, states: { moving: 1, queued: 0, paused: 0, done: 0 }, done: false,
  stall: { stalled: false, since: iso(NOW) }, read_through: 1, generation: 1,
}
async function mockBand(page: Page, options: { owner: boolean; sibling: Sibling; lines?: number; goal?: boolean; instances?: Sibling; desk?: 'items' | 'empty' }) {
  const reads = { sibling: 0, origins: [] as string[] }
  let state = options.sibling
  let instances: Sibling = options.instances ?? 'ok'
  const lines = options.lines ?? 1
  const deskItems = options.desk === 'empty' ? [] : [
    { id: '00000000-0000-4000-8000-000000000001', kind: 'question', project_id: 'p-aeon', revision: 1, title: 'Which identity should agent reviews run as for the gatekeeper go-live?', created_at: iso(NOW - 3 * HOUR), held: false, href: '', source: '', ticket_key: 'AEON-1394', asked_by: { id: 'p-lead', name: 'AEON-LEAD' } },
    { id: '00000000-0000-4000-8000-000000000002', kind: 'question', project_id: 'p-pharos', revision: 1, title: 'Run the 60-minute voice soak test on the Aithema test host?', created_at: iso(NOW - 2 * HOUR), held: true, href: '', source: '', asked_by: { id: 'p-ops', name: 'OPS' } },
    { id: '00000000-0000-4000-8000-000000000003', kind: 'approval', project_id: 'p-aeon', revision: 1, title: 'Approval · nodes.write', created_at: iso(NOW - HOUR), held: false, href: '', source: '' },
  ]
  await mockWork(page, fixtures())
  await page.route(/\/api\/goals(\?.*)?$/, route => route.fulfill({ json: {
    items: options.goal ? [keptGoal] : [], totals: options.goal ? { moving: 1, queued: 0, paused: 0, done: 0 } : { moving: 0, queued: 0, paused: 0, done: 0 }, totals_partial: false, quiet_projects: { count: 0, keys: [] }, truncated: false, read_through: 1, generation: 1,
  }, headers: { 'Aeon-Contract': 'goals/1.5' } }))
  await page.route(/\/api\/decision-desk\/projection/, route => route.fulfill({ json: {
    items: deskItems,
    counts: options.desk === 'empty' ? { open: 0, held: 0, chores: 0 } : { open: 3, held: 1, chores: 0 }, has_more: false, as_of: iso(NOW),
    upcoming: [{ node_id: 'n-ait', check_id: 'human', project_id: 'p-aeon', ticket_key: 'AIT-140', label: 'Your Aithema test session (~20 min, whenever you like)' }],
  } }))
  await page.route(/\/api\/decision-desk\/instances$/, route => {
    if (instances === 'down') return route.fulfill({ status: 503, json: { error: 'unavailable' } })
    return route.fulfill({ json: { here: 'ppm', siblings: options.owner ? [{ tag: 'pma', origin: SIBLING }] : [] } })
  })
  await page.route(SUMMARY, route => {
    reads.sibling++
    const origin = route.request().headers().origin ?? ''
    reads.origins.push(origin)
    const cors = { 'access-control-allow-origin': origin, 'access-control-allow-credentials': 'true', vary: 'Origin' }
    if (state === 'down') return route.fulfill({ status: 401, json: { error: 'authentication required' }, headers: cors })
    return route.fulfill({ headers: cors, json: {
      instance: 'pma', counts: { open: lines, held: 0 }, as_of: iso(NOW),
      items: Array.from({ length: lines }, (_, index) => ({
        id: `00000000-0000-4000-8000-0000000000a${index + 1}`, kind: 'question', project_key: 'PORTAL', asked_by: 'PORTAL', held: false,
        title: index === 0 ? 'Deploy the new sign-in flow to the second test host?' : 'Confirm the second host’s backup before the window?',
        href: `/decision-desk?item=q:00000000-0000-4000-8000-0000000000a${index + 1}`,
      })),
    } })
  })
  return { reads, set: (next: Sibling) => { state = next }, setInstances: (next: Sibling) => { instances = next } }
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

const still = (before: { x: number; y: number; width: number; height: number }, after: { x: number; y: number; width: number; height: number }) => {
  for (const key of ['x', 'y', 'width', 'height'] as const) expect(Math.abs(before[key] - after[key])).toBeLessThanOrEqual(0.5)
}

test('two sibling lines keep the goals controls still when a refresh fails and recovers', async ({ page }) => {
  test.setTimeout(60_000)
  for (const width of [1440, 390] as const) {
    await page.setViewportSize({ width, height: width > 760 ? 900 : 844 })
    const mock = await mockBand(page, { owner: true, sibling: 'ok', lines: 2, goal: true })
    const band = await openBand(page)
    await expect(band.locator('a.waiting-item')).toHaveCount(5)
    const head = page.locator('.goal-head')
    const control = width > 760 ? head.getByRole('button', { name: /Sort by lands/i }) : head.locator('.head-expand')
    await expect(control).toBeVisible()
    const [beforeHead, beforeControl] = await Promise.all([box(head), box(control)])
    mock.set('down')
    await page.clock.fastForward(60_000)
    const slots = band.locator('.w-err-li')
    await expect(slots).toHaveCount(2)
    await expect(slots.first()).toContainText(/couldn’t be read/)
    // Phone hides every row after the first, including the sibling slots these lines replace.
    if (width > 760) await expect(slots.first()).toBeVisible()
    else await expect(slots.first()).toBeHidden()
    const [duringHead, duringControl] = await Promise.all([box(head), box(control)])
    still(beforeHead, duringHead)
    still(beforeControl, duringControl)
    mock.set('ok')
    const retry = slots.first().locator('button.w-retry')
    await retry.click(width > 760 ? undefined : { force: true })
    await expect(band.locator('a.waiting-item')).toHaveCount(5)
    await expect(band.locator('.w-err-li')).toHaveCount(0)
    const [afterHead, afterControl] = await Promise.all([box(head), box(control)])
    still(beforeHead, afterHead)
    still(beforeControl, afterControl)
  }
})

test('unknown coverage is not an empty desk', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const mock = await mockBand(page, { owner: true, sibling: 'ok', instances: 'down', desk: 'empty' })
  await page.goto('/?view=goals')
  const band = page.getByRole('region', { name: 'Waiting on you' })
  await expect(band).toContainText('Other instances couldn’t be read')
  await expect(band).not.toContainText('Nothing waits on you')
  await expect(band.locator('a.waiting-item')).toHaveCount(0)
  expect(mock.reads.sibling).toBe(0)
  mock.setInstances('ok')
  await band.getByRole('button', { name: 'Try again' }).click()
  await expect(band.locator('a.waiting-item')).toContainText('Deploy the new sign-in flow')
  await expect(band).not.toContainText('Other instances couldn’t be read')
  await expect(band).not.toContainText('Nothing waits on you')
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
