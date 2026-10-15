// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1359 (design r4b, AEON-1356/1357): the lifeline strip on each ticket and Delivery › Tickets.
// Risks: the lifeline moves the properties, its caption, markers or strip when it lands or on hover, keys and taps;
// a marker sits off its crossing, a reopen loses its "again" gates or the final status gets two markers; the tips let
// the page show through; Delivery's filters or Learn buttons move when another window or filter is counted; the
// in-progress stripes slide where they must not (or at all with reduced motion).
import { expect, test, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { controlStability } from './control-stability'
import { deliveryMetrics, mockDelivery } from './delivery-numbers-fixtures'

test.use({ timezoneId: 'Europe/Vienna' })

const H = 3600
const GENERATED = '2026-10-14T15:40:00Z' // Wed 14 Oct 17:40 in Vienna, as in the design
type Actor = { name: string; kind: 'agent' | 'person' | 'unknown' }
const creator: Actor = { name: 'Markus Barta', kind: 'person' }, worker: Actor = { name: 'Harbor worker', kind: 'agent' }
const lead: Actor = { name: 'Pharos lead', kind: 'agent' }, mira: Actor = { name: 'Mira Holm', kind: 'person' }
const TERMINAL = ['done', 'delivered', 'accepted', 'cancelled', 'archived']

/** A lifeline from [status, hours after creation, who moved it in] steps, as the AEON-1369 API answers it. */
function lifeline(nodeId: string, created: string, steps: [string, number, Actor][]) {
  const start = Date.parse(created), at = (hours: number) => new Date(start + hours * H * 1000).toISOString()
  const seen = new Set<string>()
  const visits = steps.map(([status, hours, by], i) => {
    const next = steps[i + 1], last = i === steps.length - 1, open = last && !TERMINAL.includes(status)
    const visit = {
      status, from: at(hours), to: next ? at(next[1]) : open ? null : at(hours),
      seconds: next ? (next[1] - hours) * H : open ? (Date.parse(GENERATED) - Date.parse(at(hours))) / 1000 : 0,
      moved_by: by, next_status: next?.[0] ?? null, ended_by: next?.[2] ?? null, release: null, source: 'event', again: seen.has(status), ongoing: open,
    }
    seen.add(status)
    return visit
  })
  return { node_id: nodeId, generated_at: GENERATED, visits, complete: true, inferred: false, gap_count: 0, initial_offset_seconds: 0 }
}
// PHAROS-11, still in progress after going back from QA: one quiet and one attention note (AIT-135 in the design).
const REWORK = lifeline('n-1', '2026-10-13T06:00:00Z', [['new', 0, creator], ['in_progress', 2, worker], ['qa', 7.4, worker], ['in_progress', 8, lead]])
// PHAROS-15, reopened after Done and done again (AIT-157 in the design): doing and done twice, the last one final.
const REOPEN = lifeline('n-5', '2026-10-12T16:00:00Z', [['new', 0, creator], ['in_progress', 1, worker], ['qa', 5, worker], ['done', 6, lead], ['in_progress', 9, mira], ['qa', 11, worker], ['done', 12, lead]])

const D = 86_400
function timing(window: number, final: 'delivered' | 'done' = 'delivered', scale = 1) {
  const step = (key: string, n: number, p50: number, p90: number, prevN = 0, prevP50: number | null = null) => ({ step: key, n, p50: p50 * scale, p90: p90 * scale, prev_n: prevN, prev_p50: prevP50, prev_p90: null })
  const steps = final === 'delivered'
    ? [step('created_to_live', 108, 1.5 * D, 5.9 * D, 90, 2.8 * D), step('new_to_picked_up', 128, 6.7 * H, 30 * H, 120, 9 * H), step('in_progress', 117, 2.4 * H, 8 * H, 100, 2.5 * H), step('qa_to_merged', 117, 51 * 60, 2.5 * H, 95, 40 * 60), step('done_to_live', 108, 19 * H, 2 * D), step('live_to_accepted', 80, D, 2 * D, 70, D)]
    : [step('created_to_done', 129, 1.2 * D, 4 * D), step('new_to_picked_up', 140, 6 * H, 30 * H), step('in_progress', 129, 2 * H, 9 * H), step('qa_to_merged', 129, 40 * 60, 2 * H)]
  const buckets = Array.from({ length: Math.min(window, 30) }, (_, k, all) => {
    const day = new Date(Date.parse('2026-10-14T00:00:00Z') - (all.length - 1 - k) * D * 1000).toISOString().slice(0, 10)
    const before = day < '2026-09-08', quiet = k % 5 === 2
    return { from: `${day}T00:00:00Z`, to: `${day}T23:59:59Z`, state: before ? 'no_data' : 'observed', n: before || quiet ? 0 : 3 + (k % 4), p50: before || quiet ? null : (1 + (k % 3) * 0.3) * D * scale, p90: before || quiet ? null : (3 + (k % 2)) * D * scale }
  })
  return { project_id: 'p-aeon', generated_at: GENERATED, window, final_status: final, steps, rework: 22, coverage: { tickets: 166, complete: 166, inferred: 0, gaps: 0, since: '2026-09-07T10:59:26Z', partial: window > 30 }, buckets }
}

function gate() { let open!: () => void; const wait = new Promise<void>(resolve => { open = resolve }); return { wait, open } }
async function setup(page: Page, options: { theme?: 'light' | 'dark'; hold?: Promise<void>; lifelineStatus?: number } = {}) {
  const work = fixtures()
  work.preferences.theme = { choice: options.theme ?? 'light' }
  await mockWork(page, work)
  let lifelineStatus = options.lifelineStatus ?? 200
  await page.route('**/api/nodes/*/lifeline', async route => {
    await options.hold
    const id = new URL(route.request().url()).pathname.split('/')[3]
    const body = id === 'n-1' ? REWORK : id === 'n-5' ? REOPEN : null
    if (lifelineStatus !== 200 || !body) return route.fulfill({ status: lifelineStatus === 200 ? 404 : lifelineStatus, json: { error: 'not here' } })
    return route.fulfill({ json: body })
  })
  const asked: URLSearchParams[] = []
  let ticketHold: Promise<void> | null = null, final: 'delivered' | 'done' = 'delivered'
  await page.route('**/api/projects/*/delivery/tickets?*', async route => {
    const query = new URL(route.request().url()).searchParams
    asked.push(query)
    if (ticketHold) await ticketHold
    const window = Number(query.get('window'))
    const body = { ...timing(window, final, query.get('type') === 'bug' ? 1.4 : 1), project_id: route.request().url().includes('/p-pharos/') ? 'p-pharos' : 'p-aeon' }
    return route.fulfill({ json: body })
  })
  await mockDelivery(page, () => ({ status: 200, body: deliveryMetrics() }))
  return {
    asked, failLifeline: (status: number) => { lifelineStatus = status },
    holdTickets: (hold: Promise<void> | null) => { ticketHold = hold },
    finalStatus: (next: 'delivered' | 'done') => { final = next },
  }
}

const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })
const life = (page: Page) => panel(page).getByTestId('ticket-lifeline')
const strip = (page: Page) => life(page).getByRole('group', { name: 'Time in each status. Arrow keys move through the steps.' })
const markers = (page: Page) => life(page).getByRole('group', { name: 'Gates crossed and the current status' }).getByRole('button')
const segments = (page: Page) => strip(page).locator('.sg')
const tip = (page: Page) => page.locator('.lf-tip')
const box = async (locator: Locator) => (await locator.boundingBox())!

test('the lifeline lands without moving anything above it, says every visit, and keeps hover, keys and tips in place', async ({ page }) => {
  test.setTimeout(90_000)
  await page.setViewportSize({ width: 1440, height: 1000 })
  const held = gate()
  await setup(page, { hold: held.wait })
  await page.goto('/p/PHAROS/PHAROS-11')
  const status = panel(page).getByRole('button', { name: 'Status: In progress. Change status' }).first()
  await expect(life(page).locator('.life-cap')).toContainText('Time in each status')
  const updated = panel(page).locator('p.meta').filter({ hasText: 'Updated' })
  const below = await box(updated)
  const guard = await controlStability(page, { status, caption: life(page).locator('.life-cap'), markerRow: life(page).locator('.gates'), strip: strip(page) })
  await guard.check(async () => { held.open(); await expect(markers(page)).toHaveCount(3) })
  // Pattern A: the line under the strip may only grow downward.
  expect((await box(updated)).y).toBeGreaterThanOrEqual(below.y - 0.5)

  await expect(life(page).locator('.life-cap')).toHaveText(/Time in each status\s*1\.4d so far/)
  await expect(life(page).locator('.life-line')).toHaveText('New 2h · In progress 5.4h, usually 2.4h · QA 36m · In progress again 1.1d so far, usually 2.4h')
  await expect(life(page).locator('.life-line .usual.attn')).toHaveText(', usually 2.4h')
  await expect(markers(page).nth(0)).toHaveAccessibleName('Entered open · Tue 13 Oct 08:00 · created')
  await expect(markers(page).nth(1)).toHaveAccessibleName('Entered doing · Tue 13 Oct 10:00 · by Harbor worker')
  await expect(markers(page).nth(2)).toHaveAccessibleName('In progress now · since Tue 13 Oct 16:00 · by Pharos lead')
  // Fills follow the status icons; the live segment always slides (f-now), past ones only under the pointer.
  expect(await segments(page).evaluateAll(els => els.map(el => [...el.classList].filter(c => c.startsWith('f') && c !== 'f-short').sort().join(' ')))).toEqual(['f-ring fc-new', 'f-diag fc-progress', 'f-halo fc-qa', 'f-diag f-now fc-progress'])
  // Every marker a cluster did not move is centred exactly on its notch, and the strip runs the full width.
  const notches = await life(page).locator('.gate-notch').evaluateAll(els => els.map(el => { const r = el.getBoundingClientRect(); return r.left + r.width / 2 }))
  const centres = await markers(page).evaluateAll(els => els.map(el => { const r = el.querySelector('svg')!.getBoundingClientRect(); return { x: r.left + r.width / 2, moved: el.hasAttribute('data-displaced') } }))
  centres.forEach((centre, k) => { if (!centre.moved) expect(Math.abs(centre.x - notches[k]), `marker ${k}`).toBeLessThanOrEqual(0.01) })
  const stripBox = await box(strip(page)), lastSegment = await box(segments(page).last())
  expect(Math.abs(lastSegment.x + lastSegment.width - (stripBox.x + stripBox.width))).toBeLessThanOrEqual(1)

  // Hover, keys and marker focus show opaque tips and move nothing.
  const qa = segments(page).nth(2)
  await guard.check(async () => {
    const r = await box(qa)
    await page.mouse.move(r.x + r.width / 2, r.y + r.height / 2)
    await expect(tip(page)).toBeVisible()
  })
  await expect(tip(page).locator('p')).toHaveText(['QA · 36m', 'Tue 13 Oct 15:24 → 16:00', 'Pharos lead (agent) moved it to In progress', 'Usually 51m for QA → merged; 1 in 10 over 2.5h.'])
  expect(await tip(page).evaluate(el => getComputedStyle(el).backgroundColor)).toMatch(/^rgb\(\d+, \d+, \d+\)$/)
  await guard.check(async () => { await page.mouse.move(5, 5); await expect(tip(page)).toHaveCount(0) })
  await guard.check(async () => {
    await strip(page).focus()
    await page.keyboard.press('End')
    await expect(tip(page).locator('p')).toHaveText(['In progress again · 1.1d so far', 'Tue 13 Oct 16:00 → now', 'Still in progress: nobody has moved it on yet', 'Slower than 9 in 10 tickets: usually 2.4h for In progress.'])
    await expect(tip(page).locator('p.attn')).toHaveCount(1)
  })
  await guard.check(async () => {
    await page.keyboard.press('Home')
    await expect(tip(page).locator('p').first()).toHaveText('New · 2h')
    await page.keyboard.press('ArrowRight')
    await expect(tip(page).locator('p').first()).toHaveText('In progress · 5.4h')
    await page.keyboard.press('Escape')
    await expect(tip(page)).toHaveCount(0)
  })
  await expect(panel(page)).toBeVisible() // Esc closed the tip, not the ticket
  await guard.check(async () => {
    await markers(page).nth(1).hover()
    await expect(tip(page)).toHaveText('Entered doing · Tue 13 Oct 10:00 · by Harbor worker')
  })
  guard.done()
})

test('a reopen crosses doing and done again into one final marker, and the strip sits at the same offset for every ticket', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page)
  const offsetOf = async () => { const section = await box(life(page)), s = await box(strip(page)); return s.y - section.y }
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(markers(page)).toHaveCount(3)
  const first = await offsetOf()
  await page.goto('/p/PHAROS/PHAROS-15')
  await expect(markers(page)).toHaveCount(5)
  expect(await offsetOf()).toBe(first)
  expect(await markers(page).evaluateAll(els => els.map(el => el.getAttribute('aria-label')))).toEqual([
    'Entered open · Mon 12 Oct 18:00 · created', 'Entered doing · Mon 12 Oct 19:00 · by Harbor worker', 'Entered done · Tue 13 Oct 00:00 · by Pharos lead',
    'Entered doing again · Tue 13 Oct 03:00 · by Mira Holm', 'Entered done again · Tue 13 Oct 06:00 · by Pharos lead · current status',
  ])
  expect(await markers(page).evaluateAll(els => els.map(el => el.getAttribute('data-kind')))).toEqual(['gate', 'gate', 'gate', 'gate', 'final'])
  await expect(life(page).locator('.life-cap')).toHaveText(/12h from created to Done/)
  await expect(life(page).locator('.life-line')).toHaveText('New 1h · In progress 4h, usually 2.4h · QA 1h · Done 3h · In progress again 2h · QA again 1h · Done')
  // The final marker sits on the strip's last pixel.
  const s = await box(strip(page)), last = await box(life(page).locator('.gate-notch').last())
  expect(Math.round(last.x - s.x)).toBe(Math.floor(s.width) - 1)
})

test('a failed lifeline keeps its height, says so, and retries', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const held = gate()
  const world = await setup(page, { hold: held.wait, lifelineStatus: 500 })
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(life(page)).toHaveAttribute('aria-busy', 'true')
  const loading = await box(life(page))
  held.open()
  await expect(life(page).locator('.life-line')).toHaveText('Time in each status could not be loaded. Retry')
  expect((await box(life(page))).height).toBeCloseTo(loading.height, 1)
  world.failLifeline(200)
  await life(page).getByRole('button', { name: 'Retry' }).click()
  await expect(markers(page)).toHaveCount(3)
})

test.describe('on a phone', () => {
  test.use({ hasTouch: true })
  test('a tap pins a segment tip and a tap anywhere in the marker row picks the nearest marker', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 860 })
    await setup(page, { theme: 'dark' })
    await page.goto('/p/PHAROS/PHAROS-11')
    await expect(markers(page)).toHaveCount(3)
    const guard = await controlStability(page, { strip: strip(page), markerRow: life(page).locator('.gates') })
    await guard.check(async () => { await segments(page).nth(1).tap(); await expect(tip(page).locator('p').first()).toHaveText('In progress · 5.4h') })
    await guard.check(async () => { await segments(page).nth(1).tap(); await expect(tip(page)).toHaveCount(0) })
    const row = await box(life(page).locator('.gates'))
    await guard.check(async () => {
      await page.touchscreen.tap(row.x + row.width - 4, row.y + row.height / 2)
      await expect(tip(page)).toHaveText(/^In progress now · since/)
    })
    guard.done()
    // The 44 px reach of the end markers grows inwards: nothing pushes the sheet sideways.
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
  })
})

// ---------- Delivery › Tickets ----------
const head = (page: Page) => page.locator('.dl-head')
const tickets = (page: Page) => page.getByTestId('delivery-tickets')

test('Delivery › Tickets says the typical and slow time per step, and its filters and Learn buttons stay put while another window or filter is counted', async ({ page }) => {
  test.setTimeout(90_000)
  await page.setViewportSize({ width: 1440, height: 1000 })
  const world = await setup(page)
  await page.goto('/p/AEON/delivery')
  await expect(tickets(page).getByRole('heading', { name: 'Tickets, from created to live', level: 3 })).toBeVisible()
  await expect(tickets(page).locator('.cnt')).toHaveText('108 tickets went live')
  await expect(page.locator('.dl-sub')).toHaveText('How fast tickets go from created to live and changes from pull request to live, measured all the time; changes against the Project Arion targets.')
  await expect(page.locator('.src').filter({ hasText: 'Status history' })).toHaveText('Status history · every status change since 7 Sep')
  const hero = page.getByTestId('delivery-tickets-hero')
  await expect(hero.locator('.s-val')).toHaveText('1.5d')
  await expect(hero.locator('.tk-slow')).toHaveText('Slow: 1 in 10 over 5.9d')
  await expect(hero.locator('.s-trend')).toHaveText('Better than the 7 days before (2.8d)')
  await expect(hero.locator('.typ-line > span')).toHaveText(['New → picked up 6.7h', 'In progress 2.4h', 'QA → merged 51m', 'Done → live 19h'])
  await expect(hero.locator('.s-say')).toHaveText('Half of the tickets go live within 1.5d of being created; 1 in 10 takes 5.9d or longer. The longest steps: Done → live and New → picked up.')
  await expect(hero.locator('.s-foot')).toHaveText('From the event log · 108 tickets went live · 22 went back from QA')
  expect(await hero.locator('.typ .sg').evaluateAll(els => els.map(el => el.getAttribute('data-step')))).toEqual(['new_to_picked_up', 'in_progress', 'qa_to_merged', 'done_to_live'])
  expect(await hero.locator('.typ .sg').evaluateAll(els => els.map(el => [...el.classList].filter(c => c.startsWith('f') && c !== 'f-short').sort().join(' ')))).toEqual(['f-ring fc-new', 'f-diag fc-progress', 'f-halo fc-qa', 'f-tint fc-done'])
  const steps = tickets(page).locator('.tk-steps > [role="listitem"]')
  await expect(steps.locator('h4')).toHaveText(['New → picked up', 'In progress', 'QA → merged', 'Done → live', 'Live → accepted'])
  await expect(steps.nth(2).locator('.s-count')).toHaveText('22 went back from QA')
  await expect(steps.nth(0).locator('.s-say')).toHaveText('Half of the tickets are picked up within 6.7h of being created.')
  // The chart bridges the quiet days: one path per drawn pair, none across a hatched "no data yet" day.
  await expect(hero.locator('.readout')).toHaveText(/^Today · p50 /)

  const windows = head(page).getByRole('radiogroup', { name: 'Chart window' })
  const type = tickets(page).getByRole('radiogroup', { name: 'Type' })
  const heroLearn = hero.getByRole('button', { name: 'Learn: From created to live' })
  const stepLearn = steps.nth(4).getByRole('button', { name: 'Learn: Live → accepted' })
  const ciLearn = page.getByTestId('delivery-simple').getByRole('button', { name: 'Learn: How long the PR checks take' })
  const guard = await controlStability(page, { windows, type, heroLearn, stepLearn, ciLearn })
  const held = gate()
  world.holdTickets(held.wait)
  await guard.check(async () => {
    await windows.getByRole('radio', { name: '30 days' }).click()
    await expect(hero).toHaveAttribute('aria-busy', 'true')
  })
  await guard.check(async () => { held.open(); await expect(hero).not.toHaveAttribute('aria-busy', 'true') })
  world.holdTickets(null)
  await guard.check(async () => {
    await type.getByRole('radio', { name: 'Bug' }).click()
    await expect(hero.locator('.s-val')).toHaveText('2.1d')
  })
  expect(world.asked.at(-1)?.get('type')).toBe('bug')
  expect(world.asked.at(-1)?.get('window')).toBe('30')
  guard.done()

  // Learn says the step's own figures and its source.
  await stepLearn.focus()
  await expect(page.locator('.dl-learn')).toContainText('From live until a person accepts it, or the autopilot one day later.')
  await expect(page.locator('.dl-learn')).toContainText('Source: event log, every status change since 7 Sep.')
})

test('Delivery › Tickets in Expert: six tiles and Created → live as the first trend', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page)
  await page.goto('/p/AEON/delivery')
  await head(page).getByRole('radiogroup', { name: 'Level of detail' }).getByRole('radio', { name: 'Expert' }).click()
  const expert = page.getByTestId('delivery-tickets-expert')
  await expect(expert.locator('.tiles-cap')).toHaveText('Tickets · last 7 days · change against the 7 days before')
  await expect(expert.locator('.t-label')).toHaveText(['Created → live', 'New → picked up', 'In progress', 'QA → merged', 'Done → live', 'Live → accepted'])
  await expect(expert.getByRole('listitem').nth(3)).toContainText('117 merged · 22 went back')
  await expect(page.getByTestId('delivery-charts').locator('.chart-card h4').first()).toHaveText('Created → live')
})

test('a project that does not mark releases stops at QA → merged and reads Created → done', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const world = await setup(page)
  world.finalStatus('done')
  await page.goto('/p/AEON/delivery')
  await expect(tickets(page).getByRole('heading', { name: 'Tickets, from created to done', level: 3 })).toBeVisible()
  await expect(tickets(page).locator('.tk-steps h4')).toHaveText(['New → picked up', 'In progress', 'QA → merged'])
})

test.describe('motion', () => {
  test.use({ reducedMotion: 'no-preference' })
  test('the live segment slides, past segments and the step bar only under the pointer, swatches never', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 })
    await setup(page)
    await page.goto('/p/PHAROS/PHAROS-11')
    await expect(markers(page)).toHaveCount(3)
    const running = (root: Locator) => root.evaluate(el => el.getAnimations({ subtree: true }).filter(a => (a as CSSAnimation).animationName === 'diag-slide' && a.playState === 'running').length)
    await page.mouse.move(5, 5)
    expect(await running(strip(page))).toBe(1)
    const r = await box(segments(page).nth(0))
    await page.mouse.move(r.x + r.width / 2, r.y + r.height / 2)
    await expect.poll(() => running(strip(page))).toBe(2)
    await page.goto('/p/AEON/delivery')
    const typ = page.getByTestId('delivery-tickets-typical')
    await expect(typ.locator('.sg')).toHaveCount(4)
    await page.mouse.move(5, 5)
    expect(await running(typ)).toBe(0)
    await typ.hover()
    await expect.poll(() => running(typ)).toBe(1)
    expect(await running(page.getByTestId('delivery-tickets-hero').locator('.typ-line'))).toBe(0)
  })
})

test('with reduced motion nothing slides, not even the live segment', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page)
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(markers(page)).toHaveCount(3)
  const r = await box(segments(page).nth(1))
  await page.mouse.move(r.x + r.width / 2, r.y + r.height / 2)
  expect(await page.evaluate(() => document.getAnimations().filter(a => (a as CSSAnimation).animationName === 'diag-slide').length)).toBe(0)
})

// ---------- Evidence: the lifeline and Delivery › Tickets at 390, 960 and 1440, light and dark ----------
for (const width of [390, 960, 1440] as const) {
  for (const theme of ['light', 'dark'] as const) {
    test(`AEON-1359 screenshots at ${width} ${theme}`, async ({ page }, info) => {
      test.setTimeout(90_000)
      await page.setViewportSize({ width, height: width === 390 ? 860 : 1000 })
      await setup(page, { theme })
      const shot = async (name: string, full = false) => {
        if (full) {
          const viewport = page.viewportSize()!
          const extra = await page.evaluate(() => { const main = document.querySelector('main'); if (main) main.scrollTop = 0; return main ? main.scrollHeight - main.clientHeight : 0 })
          await page.setViewportSize({ width, height: Math.min(viewport.height + extra + 8, 7000) })
          await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
          await page.screenshot({ path: info.outputPath(`aeon-1359/${name}-${width}-${theme}.png`) })
          await page.setViewportSize(viewport)
        } else await page.screenshot({ path: info.outputPath(`aeon-1359/${name}-${width}-${theme}.png`) })
      }
      for (const key of ['PHAROS-11', 'PHAROS-15']) {
        await page.goto(`/p/PHAROS/${key}`)
        await expect(markers(page).first()).toBeVisible()
        await page.mouse.move(0, 0)
        await life(page).scrollIntoViewIfNeeded()
        await shot(`lifeline-${key}`)
        const seg = await box(segments(page).nth(1))
        if (width === 390) await page.mouse.click(seg.x + seg.width / 2, seg.y + seg.height / 2)
        else await page.mouse.move(seg.x + seg.width / 2, seg.y + seg.height / 2)
        await expect(tip(page)).toBeVisible()
        await shot(`lifeline-${key}-tip`)
        await markers(page).last().hover()
        await shot(`lifeline-${key}-marker`)
      }
      await page.goto('/p/AEON/delivery')
      await expect(page.getByTestId('delivery-tickets-hero').locator('.s-val')).toHaveText('1.5d')
      await page.mouse.move(0, 0)
      await shot('delivery-simple', true)
      await head(page).getByRole('radiogroup', { name: 'Level of detail' }).getByRole('radio', { name: 'Expert' }).click()
      await expect(page.getByTestId('delivery-tickets-expert').locator('.t-value').first()).toHaveText('1.5d')
      await shot('delivery-expert', true)
    })
  }
}
