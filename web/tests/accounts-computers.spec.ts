// SPDX-License-Identifier: AGPL-3.0-only
// AEON-782: Accounts and computers on /agents is a status line and what blocks
// agents (the approved design of AEON-721). The head says the state; unfolded,
// the body holds Needs you, the sign-ins grid and the pacing line. Lists,
// capacity, pacing and the account menu live in Settings (AEON-686, AEON-786);
// every name and cell opens the right panel there.
import AxeBuilder from '@axe-core/playwright'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { ACCOUNTS, NOW, TZ, capacityWorld, type CapacityOptions } from './capacity-fixtures'
import { expectStableControls } from './helpers/stable'
import { controlStability } from './control-stability'
import type { PairingView } from '../src/lib/agentPairing'
import { mockEffectivePermissions } from './authz-fixtures'

test.use({ timezoneId: TZ })
const world: AgentWorld = {
  me: me.id, now: NOW,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}
interface Options extends CapacityOptions { manage?: boolean; thresholds?: { early_percent: number; urgent_percent: number }; unfolded?: boolean }
/** Every sign-in reports ready and both computers are online: the calm state (the studio one is online here). */
function calm(capacity: ReturnType<typeof capacityWorld>) {
  for (const c of capacity.computers as unknown as PairingView[]) {
    const harnesses = [...new Set(c.enrollments.map(e => e.harness))]
    Object.assign(c, {
      connectivity: 'online', revision: 1,
      harness_statuses: Object.fromEntries(harnesses.map(h => [h, 'ready'])), harness_details: Object.fromEntries(harnesses.map(h => [h, { state: 'ready' }])),
      verification_capabilities: Object.fromEntries(harnesses.map(h => [h, { supported: true, policy: 'read_only', reason: '' }])),
    })
  }
}
/** Claude's verification on mbp2607 ran out: the one thing that blocks agents. */
function expireClaude(capacity: ReturnType<typeof capacityWorld>) {
  const c = (capacity.computers as unknown as PairingView[])[0]!
  const e = c.enrollments.find(e => e.account_id === ACCOUNTS.claude)!
  Object.assign(e, { can_verify: true, verification_state: 'expired', verification_expired_ready: false })
  return { c, e }
}
async function setup(page: Page, options: Options = {}) {
  await page.clock.setSystemTime(NOW)
  const work = fixtures()
  await mockWork(page, work, { admin: true })
  const data = agentData(world)
  const capacity = capacityWorld(options)
  calm(capacity)
  data.accounts = (capacity.accounts as unknown as typeof data.accounts).map(a => ({ ...a, registered_by_principal_id: me.id }))
  data.approvals = data.approvals.filter(a => a.decision)
  data.messages = data.messages.filter(m => !m.is_action_request)
  if (options.unfolded) work.preferences['ui.agents.sections'] = { dial: true, accounts: true, sessions: true, queued: true }
  await mockAgents(page, data, { capacity, workingPreference: () => work.preferences['agents.working'] })
  const thresholds = options.thresholds ?? { early_percent: 5, urgent_percent: 2 }
  await page.route('**/api/settings/quota-warnings', route => route.fulfill({ json: thresholds }))
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', ...(options.manage === false ? [] : ['account.manage']), 'run.create', 'run.read', 'models.read', 'work_orders.read']
    return route.fulfill({ json: answer })
  })
  return { capacity, work, data, thresholds }
}
const section = (page: Page) => page.getByRole('region', { name: 'Accounts and computers' })
const title = (page: Page) => section(page).getByRole('button', { name: 'Accounts and computers', exact: true })
const status = (page: Page) => section(page).locator('.fs-sum')
async function open(page: Page) {
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await expect(title(page)).toBeVisible()
  await expect(status(page)).not.toContainText('Reading')
}

test('calm: one status line folded; unfolded, nothing needs you and the grid names every sign-in', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page)
  await open(page)
  await expect(title(page)).toHaveAttribute('aria-expanded', 'false')
  await expect(status(page)).toHaveText('All 6 ready · 2 computers online')
  await expect(section(page).getByRole('link', { name: /Manage/ })).toHaveAttribute('href', '/settings/accounts')
  await expect(section(page).getByRole('button', { name: 'Verify again' })).toHaveCount(0)
  // Folded means no body: nothing to tab into, and none of today's cards, menus or pacing summary.
  await expect(section(page).getByRole('heading', { name: 'Nothing needs you' })).toBeHidden()
  await title(page).click()
  await expect(section(page).getByRole('heading', { name: 'Nothing needs you' })).toBeVisible()
  await expect(section(page).getByText('Everything else is working.')).toBeVisible()
  const grid = section(page).getByRole('table')
  await expect(grid.getByRole('columnheader')).toHaveText([/Account/, /mbp2607/, /studio/])
  await expect(grid.getByRole('row')).toHaveCount(7)
  await expect(grid.getByRole('link', { name: 'Claude on mbp2607: Ready' })).toBeVisible()
  await expect(grid.getByText('Claude is not signed in on studio')).toBeAttached()
  await expect(section(page).locator('.acc-foot')).toContainText('Pacing: 5 days · keep auto · no nights')
  await expect(section(page).getByRole('region', { name: /^Computer / })).toHaveCount(0)
  await expect(section(page).getByRole('button', { name: /days · keep/ })).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'Account availability notices' })).toHaveCount(0)
  expect((await new AxeBuilder({ page }).include('.acc-section').analyze()).violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
})

test('a problem: the line names the first thing and counts the rest, and Verify again fixes it from the folded head', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { capacity } = await setup(page, { thresholds: { early_percent: 10, urgent_percent: 3 } })
  const { c, e } = expireClaude(capacity)
  let posted: unknown = null
  await page.route('**/api/agent-pairing/computers/*/enrollments/*/verify', route => {
    posted = route.request().postDataJSON()
    Object.assign(e, { verification_state: 'queued', verification_run_id: 'b0000000-0000-4000-8000-000000000782' })
    return route.fulfill({ json: { account_id: e.account_id, run_id: 'b0000000-0000-4000-8000-000000000782' } })
  })
  await open(page)
  // Claude on mbp2607 plus Codex low (9% left this week, early warning at 10%).
  await expect(status(page)).toHaveText('5 of 6 ready · Claude needs verifying on mbp2607 · +1')
  const verify = section(page).locator('.fs-act .verify-button')
  const guard = await controlStability(page, { verify, toggle: title(page), manage: section(page).getByRole('link', { name: /Manage/ }) })
  await guard.check(async () => { await verify.click(); await expect(verify).toHaveAttribute('aria-busy', 'true') })
  await expect(section(page).getByRole('status').filter({ hasText: 'Waiting for the computer to start' })).toBeVisible()
  guard.done()
  expect(posted).toEqual({ expected_revision: c.revision, expected_verification_run_id: null })
  // The queued check stays visible and disabled; readiness still awaits its result.
  await expect(verify).toBeDisabled()
  await expect(status(page)).toHaveText('5 of 6 ready · Codex is low: 9% left this week')
})

test('Needs you lists what blocks agents with the fix or its details; a low-quota notice moves here with its urgency', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { capacity, thresholds } = await setup(page, { unfolded: true, thresholds: { early_percent: 10, urgent_percent: 3 } })
  expireClaude(capacity)
  await open(page)
  const needs = section(page).getByRole('heading', { name: '2 things need you' })
  await expect(needs).toBeVisible()
  const rows = section(page).locator('.att-row')
  await expect(rows).toHaveCount(2)
  await expect(rows.nth(0)).toContainText('Claude verification on mbp2607')
  await expect(rows.nth(0)).toContainText('Verification expired. New agents wait until the sign-in passes again.')
  await expect(rows.nth(0).getByRole('button', { name: 'Verify again' })).toBeVisible()
  await expect(rows.nth(1)).toContainText('Codex is low: 9% left this week')
  await expect(rows.nth(1)).toContainText('Early warning, below 10%. Resets tomorrow 18:02.')
  await expect(rows.nth(1).getByRole('button', { name: 'Details' })).toBeVisible()
  // Raise the urgent line above the same 9%: the notice reads as urgent.
  Object.assign(thresholds, { early_percent: 20, urgent_percent: 10 })
  await page.reload()
  await expect(section(page).locator('.att-row').filter({ hasText: 'Codex is low: 9% left this week' })).toContainText('Urgent warning, below 10%.')
  expect((await new AxeBuilder({ page }).include('.acc-section').analyze()).violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
})

test('people without account management see Details, not Verify again', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { capacity } = await setup(page, { manage: false, unfolded: true })
  expireClaude(capacity)
  await open(page)
  await expect(status(page)).toContainText('Claude needs verifying on mbp2607')
  await expect(section(page).getByRole('button', { name: 'Verify again' })).toHaveCount(0)
  await expect(section(page).locator('.att-row').first().getByRole('button', { name: 'Details' })).toBeVisible()
})

test('a name, a cell and Details open the right panel in Settings', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { capacity } = await setup(page, { unfolded: true, thresholds: { early_percent: 10, urgent_percent: 3 } })
  expireClaude(capacity)
  await open(page)
  await section(page).locator('.att-row').filter({ hasText: 'Codex is low' }).getByRole('button', { name: 'Details' }).click()
  await expect(page).toHaveURL(/\/settings\/accounts$/)
  await expect(page.locator('section.pane')).toContainText('Shared quota')
  await page.goBack()
  await section(page).getByRole('link', { name: 'Claude verification on mbp2607' }).click()
  await expect(page).toHaveURL(/\/settings\/accounts$/)
  const panel = page.locator('section.pane')
  await expect(panel).toContainText('mbp2607')
  await expect(panel.locator(`[data-signin="${ACCOUNTS.claude}"]`)).toBeVisible()
  await page.goBack()
  await section(page).getByRole('table').getByRole('link', { name: /^Codex on mbp2607/ }).first().click()
  await expect(page).toHaveURL(/\/settings\/accounts$/)
  await expect(page.locator('section.pane')).toContainText('mbp2607')
  await page.goBack()
  await section(page).getByRole('table').getByRole('rowheader').first().getByRole('link').click()
  await expect(page).toHaveURL(/\/settings\/accounts$/)
  await expect(page.locator('section.pane')).toContainText('Shared quota')
})

test('Away stays in the head with Back now, and one click ends it', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page, { away: true })
  await open(page)
  const away = section(page).locator('.away')
  await expect(away).toContainText('Away until Mon 5 Oct')
  await away.getByRole('button', { name: 'Back now: end Away' }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Welcome back. Agents follow your plan again.' })).toBeVisible()
  await expect(away).toHaveCount(0)
})

test('an approval link shows the folded section once and offers that account\'s check; the fold preference stays', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { work } = await setup(page)
  await page.goto(`/agents?verify_account=${ACCOUNTS.claude}`)
  await expect(title(page)).toHaveAttribute('aria-expanded', 'true')
  await expect(section(page).locator('.att-row').first()).toContainText('Claude verification on mbp2607')
  expect(work.preferences['ui.agents.sections']).toBeUndefined()
  // An explicit fold wins while the link is still in the address.
  await title(page).click()
  await expect(title(page)).toHaveAttribute('aria-expanded', 'false')
  expect(new URL(page.url()).searchParams.get('verify_account')).toBe(ACCOUNTS.claude)
})

test('loading, a failed read with Try again, and an empty workspace each say so in the status place', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page)
  let fail = true
  await page.route('**/api/agent-accounts/capacity', route => route.request().method() === 'GET' && fail ? route.fulfill({ status: 500, json: { error: 'down' } }) : route.fallback())
  await open(page)
  await expect(status(page)).toContainText('Accounts could not be read')
  fail = false
  await status(page).getByRole('button', { name: 'Try again' }).click()
  await expect(status(page)).toHaveText('All 6 ready · 2 computers online')
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ json: { computers: [] } }))
  await page.route('**/api/agent-accounts', route => route.request().method() === 'GET' ? route.fulfill({ json: [] }) : route.fallback())
  await page.route('**/api/agent-accounts/capacity', route => route.fulfill({ json: [] }))
  await page.reload()
  await expect(status(page)).toContainText('No accounts yet')
  await expect(status(page).getByRole('link', { name: 'Connect a computer' })).toHaveAttribute('href', '/agents/register-agent')
})

// Verify again leaves the head when the section unfolds. If the head wrapped only while it was
// there, the toggle jumped half a row gap on unfold (the CI Linux fonts at 1024). The head now wraps
// by the container's width alone, in two fixed rows. Scanning the widths above the phone layout
// makes that independent of the font the machine happens to have.
test('the head keeps its height, and the toggle stays put, whether or not Verify again is shown', async ({ page }) => {
  // Tall enough that the section stays in view at every width, so nothing scrolls under the measurement.
  await page.setViewportSize({ width: 1100, height: 3000 })
  const { capacity } = await setup(page, { thresholds: { early_percent: 10, urgent_percent: 3 } })
  expireClaude(capacity)
  await open(page)
  const head = section(page).locator('.fs-head')
  const toggle = section(page).locator('.fs-tog')
  const verify = section(page).locator('.fs-act')
  const measure = async () => ({ head: (await head.boundingBox())!, toggle: (await toggle.boundingBox())! })
  let scanned = 0
  for (let width = 1100; width >= 700; width -= 20) {
    await page.setViewportSize({ width, height: 3000 })
    await expect(verify).toBeVisible()
    const folded = await measure()
    await title(page).click()
    await expect(verify).toHaveCount(0)
    const unfolded = await measure()
    await title(page).click()
    await expect(verify).toBeVisible()
    expect(Math.abs(unfolded.toggle.y - folded.toggle.y), `toggle.y at ${width}`).toBeLessThanOrEqual(0.5)
    expect(Math.abs(unfolded.head.height - folded.head.height), `head height at ${width}`).toBeLessThanOrEqual(0.5)
    scanned++
  }
  expect(scanned).toBeGreaterThan(10)
})

// Away and an expired verification are the widest head: both pills beside the status, then Verify
// again, Manage and the title. Between the phone layout and a wide one the controls stopped fitting
// one row and Manage left the card (AEON-782 gate, 700 to 800 px). The head wraps by the container's
// width alone, never by what is shown, so these controls keep their place through hover, focus and
// unfolding (which removes Verify again), and nothing leaves the card at any width in between.
test('Away with an expired verification keeps the head inside the card and its controls put at 640, 720 and 800', async ({ page }) => {
  test.setTimeout(90_000) // the same kind of width scan: ~17 s on CI against the 30 s default
  await page.setViewportSize({ width: 800, height: 3000 })
  const { capacity } = await setup(page, { away: true, thresholds: { early_percent: 10, urgent_percent: 3 } })
  expireClaude(capacity)
  await open(page)
  const card = section(page)
  const manage = card.getByRole('link', { name: /Manage/ })
  const back = card.getByRole('button', { name: 'Back now: end Away' })
  const verify = card.locator('.fs-act').getByRole('button', { name: 'Verify again' })
  const toggle = card.locator('.fs-tog')
  const spill = () => card.evaluate(el => {
    const edge = el.getBoundingClientRect().right
    const out: string[] = []
    for (const node of el.querySelectorAll('*')) {
      const box = node.getBoundingClientRect()
      if ((box.width >= 1 || box.height >= 1) && box.right - edge > 1) out.push(`${node.tagName.toLowerCase()}.${(node.getAttribute('class') || '').split(/\s+/)[0]} +${Math.round(box.right - edge)}px`)
    }
    return { out, over: document.documentElement.scrollWidth - document.documentElement.clientWidth }
  })
  for (let width = 1100; width >= 640; width -= 20) {
    await page.setViewportSize({ width, height: 3000 })
    await expect(verify).toBeVisible()
    const found = await spill()
    expect(found.out, `${width}px spills`).toEqual([])
    expect(found.over, `${width}px page scrolls`).toBeLessThanOrEqual(1)
  }
  for (const width of [640, 720, 800]) {
    await page.setViewportSize({ width, height: 3000 })
    await expect(verify).toBeVisible()
    expect((await spill()).out, `${width}px spills`).toEqual([])
    await expectStableControls({
      controls: { toggle, title: title(page), manage, back },
      scrollAreas: { page: page.locator('html') },
      interactions: [
        { name: 'hover Back now', run: () => back.hover() },
        { name: 'hover Verify again', run: () => verify.hover() },
        { name: 'hover Manage', run: () => manage.hover() },
        { name: 'focus Verify again', run: () => verify.focus() },
        { name: 'unfold removes Verify again', run: async () => { await title(page).click(); await expect(verify).toHaveCount(0); await expect(card.getByRole('table')).toBeVisible() } },
        { name: 'fold shows it again', run: async () => { await title(page).click(); await expect(verify).toBeVisible() } },
      ],
    })
  }
  // Fractional container widths between the two ranges (640 < w < 641) must still get the two-row head.
  await page.setViewportSize({ width: 800, height: 3000 })
  for (const width of [640.25, 640.5, 640.75]) {
    await card.evaluate((el, w) => { (el as HTMLElement).style.width = `${w}px` }, width)
    await expect(verify).toBeVisible()
    expect((await spill()).out, `${width}px container spills`).toEqual([])
  }
  await card.evaluate(el => { (el as HTMLElement).style.width = '' })
})

// A wide status (a font wider than ours) pushes Away to the row below the state, where Verify again
// joins it. Verify again leaving on unfold must not change that row's height, or Away (centred in
// the row) drops half the difference: back.y moved 1 px on the CI Linux fonts at 720 (AEON-782 gate).
// Widening the state's letters stands in for the font, so this holds on any machine.
async function wideAway(page: Page, width: number) {
  await page.setViewportSize({ width, height: 3000 })
  const { capacity } = await setup(page, { away: true, thresholds: { early_percent: 10, urgent_percent: 3 } })
  expireClaude(capacity)
  await open(page)
  await page.addStyleTag({ content: '.acc-section .fs-sum .t { letter-spacing: 2px; }' })
  await page.evaluate(() => document.fonts.ready)
  const card = section(page)
  const verify = card.locator('.fs-act').getByRole('button', { name: 'Verify again' })
  const away = card.locator('.away')
  const back = card.getByRole('button', { name: 'Back now: end Away' })
  const measure = async () => ({ away: (await away.boundingBox())!, back: (await back.boundingBox())! })
  return { verify, measure }
}

// Retain the positive-case guard over every original width. The actual fold
// interactions run below in bounded groups, instead of 82 clicks in one case.
test('the wide status scan encounters Away beside Verify again', async ({ page }) => {
  // The old single scan folded and unfolded 41 widths and failed on the last width under the 30 s
  // default (AEON-887 and AEON-886 merge queues). The budget only guards against a hang.
  test.setTimeout(90_000)
  const { verify, measure } = await wideAway(page, 1100)
  let shared = 0
  for (let width = 1100; width >= 700; width -= 10) {
    await page.setViewportSize({ width, height: 3000 })
    await expect(verify).toBeVisible()
    const folded = await measure()
    const beside = Math.abs((await verify.boundingBox())!.y + 14 - (folded.away.y + 13)) <= 1
    if (beside) shared++
  }
  expect(shared, 'widths where Away and Verify again share a row').toBeGreaterThan(0)
})

// Every one of the original 41 widths keeps both fold/unfold interactions and
// both 0.5 px assertions. Each case has at most 16 clicks and its own fixture.
for (const first of [1100, 1020, 940, 860, 780, 700]) {
  const last = Math.max(700, first - 70)
  test(`Away and Back now stay put through fold and unfold from ${first} to ${last}px`, async ({ page }) => {
    // Same hang-only budget as the wide-status scan this case was split from (AEON-887).
    test.setTimeout(90_000)
    const { verify, measure } = await wideAway(page, first)
    for (let width = first; width >= last; width -= 10) {
      await page.setViewportSize({ width, height: 3000 })
      await expect(title(page)).toHaveAttribute('aria-expanded', 'false')
      await expect(verify).toBeVisible()
      const folded = await measure()
      await title(page).click()
      await expect(title(page)).toHaveAttribute('aria-expanded', 'true')
      await expect(verify).toHaveCount(0)
      const unfolded = await measure()
      await title(page).click()
      await expect(verify).toBeVisible()
      const refolded = await measure()
      expect(Math.abs(unfolded.away.y - folded.away.y), `away.y at ${width}`).toBeLessThanOrEqual(0.5)
      expect(Math.abs(unfolded.back.y - folded.back.y), `back.y at ${width}`).toBeLessThanOrEqual(0.5)
      expect(Math.abs(refolded.away.y - folded.away.y), `refolded away.y at ${width}`).toBeLessThanOrEqual(0.5)
      expect(Math.abs(refolded.back.y - folded.back.y), `refolded back.y at ${width}`).toBeLessThanOrEqual(0.5)
    }
  })
}

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`the head and the body keep their controls put through fold, verify and refresh at ${width} ${theme}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1100 })
    const { capacity } = await setup(page, { thresholds: { early_percent: 10, urgent_percent: 3 } })
    const { e } = expireClaude(capacity)
    await page.route('**/api/agent-pairing/computers/*/enrollments/*/verify', route => {
      Object.assign(e, { verification_state: 'queued', verification_run_id: 'b0000000-0000-4000-8000-000000000782' })
      return route.fulfill({ json: { account_id: e.account_id, run_id: 'b0000000-0000-4000-8000-000000000782' } })
    })
    await page.addInitScript(t => { document.documentElement.dataset.theme = t }, theme)
    await open(page)
    await page.evaluate(t => { document.documentElement.dataset.theme = t }, theme)
    const toggle = section(page).locator('.fs-tog')
    const manage = section(page).getByRole('link', { name: /Manage/ })
    await expect(section(page).locator('.fs-sum')).toContainText('Claude needs verifying on mbp2607')
    await expectStableControls({
      controls: { toggle, title: title(page), manage },
      scrollAreas: { page: page.locator('html') },
      interactions: [
        { name: 'unfold', run: async () => { await title(page).click(); await expect(section(page).getByRole('table')).toBeVisible(); await info.attach('unfolded', { body: await section(page).screenshot({ animations: 'disabled' }), contentType: 'image/png' }) } },
        { name: 'verify from Needs you', run: async () => { await section(page).locator('.att-row').first().getByRole('button', { name: 'Verify again' }).click(); await expect(section(page)).toContainText('Waiting for the computer to start the check.'); await expect(section(page).locator('.verify-button')).toHaveAttribute('aria-busy', 'true') } },
        { name: 'fold again', run: async () => { await toggle.click(); await expect(section(page).getByRole('table')).toBeHidden() } },
      ],
    })
    if (width === 390) {
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
      const box = (await manage.boundingBox())!
      expect(box.width).toBeGreaterThanOrEqual(43.5); expect(box.height).toBeGreaterThanOrEqual(43.5)
    }
  })
}
