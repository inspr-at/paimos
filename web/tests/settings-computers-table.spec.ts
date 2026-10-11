// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1174 (design AEON-1149, package P4): Settings › Computers is one table, a
// row per computer and a column per account, with a New starts chip per row.
// Risks: a cell or list action of the old grid and list becomes unreachable; the
// chip writes defaults or drops a policy field instead of changing only the mode;
// a chip save moves the chip, the row or its neighbours; Undo overwrites a newer
// change; the phone layout scrolls sideways or has targets under 44 px; past six
// accounts the computer names scroll out of view. AEON1174_SHOTS=1 also writes
// the part at 1440, 1024 and 390, light and dark, into the test's output folder.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { ACCOUNTS, capacityWorld, NOW, TZ, type CapacityOptions } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'
import { defaultHostPolicy, type HostCapacityPolicy } from '../src/lib/hostCapacity'

test.use({ timezoneId: TZ })
// Every option differs from the defaults, so a write that fell back to them shows.
const POLICY: HostCapacityPolicy = { mode: 'smart', maximum_agents: 8, maximum_load: 12, wait_when_busy: true, ease_on_battery: false, ease_when_hot: true, consider_activity: true }
const ADDRESS = 'markus.barta@example.com'
type Host = { policy: HostCapacityPolicy }
const hostView = (policy: HostCapacityPolicy, running: number) => ({ policy, signals: { load: 6.2, cores: 14, memory_pressure: 'normal', power: 'plugged_in', thermal: 'normal' }, running, queued: 0, reported_at: new Date(NOW).toISOString(), reason: '', load_limit: 14, history: [] })

async function setup(page: Page, o: CapacityOptions & { manage?: boolean; expired?: boolean } = {}) {
  const manage = o.manage ?? true
  await page.clock.setSystemTime(NOW)
  await mockWork(page, fixtures(), { admin: true })
  const world = capacityWorld(o), data = agentData({ me: me.id, now: NOW, projects: {}, tickets: {}, nodes: {} })
  data.accounts = world.accounts as unknown as typeof data.accounts
  // Claude's login is an address: the column shows its name, the tooltip the whole address.
  world.accounts.find(a => a.id === ACCOUNTS.claude)!.label = ADDRESS
  const [build7, studio] = world.computers as unknown as ({ computer_id: string; revision: number; host_capacity: Host; enrollments: Record<string, unknown>[] } & Record<string, unknown>)[]
  build7!.revision = 4; studio!.revision = 2
  for (const c of [build7!, studio!]) c.enrollments.forEach(e => Object.assign(e, { can_verify: true, verification_state: 'completed' }))
  build7!.enrollments.find(e => e.account_id === ACCOUNTS.claude)!.label = ADDRESS
  if (o.expired) Object.assign(build7!.enrollments.find(e => e.account_id === ACCOUNTS.claude)!, { verification_state: 'expired', verification_expired_ready: false, verified_at: new Date(NOW - 86_400_000).toISOString(), verification_expires_at: new Date(NOW - 3_600_000).toISOString() })
  Object.assign(build7!, { harness_statuses: { codex: 'ready', claude: 'ready', grok: 'ready', cursor: 'ready', pi: 'ready' }, verification_capabilities: { ...(build7!.verification_capabilities as object), claude: { supported: true, policy: 'read_only', reason: '' } }, host_capacity: hostView(POLICY, 4) })
  Object.assign(studio!, { host_capacity: hostView({ ...defaultHostPolicy(), mode: 'fixed', maximum_load: 8 }, 0) })
  await mockAgents(page, data, { capacity: world })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin')
    answer.workspace.permissions = [...answer.workspace.permissions.filter(p => manage || !['account.manage', 'settings.manage'].includes(p)), 'account.read', ...(manage ? ['account.manage', 'settings.manage'] : [])]
    return route.fulfill({ json: answer })
  })
  await page.route('**/api/settings/quota-warnings', route => route.fulfill({ json: { early_percent: 10, urgent_percent: 3 } }))
  const writes: { computer: string; body: unknown }[] = []
  let conflict = false
  await page.route('**/api/agent-pairing/computers/*/capacity', route => {
    const body = route.request().postDataJSON() as { expected_revision: number; policy: HostCapacityPolicy }, id = new URL(route.request().url()).pathname.split('/').at(-2)!
    const c = [build7!, studio!].find(x => x.computer_id === id)!
    writes.push({ computer: id, body })
    if (conflict || body.expected_revision !== c.revision) return route.fulfill({ status: 409, json: { error: 'computer changed; review settings again' } })
    c.host_capacity.policy = body.policy; c.revision++
    return route.fulfill({ json: c.host_capacity })
  })
  await page.goto('/settings/computers')
  await expect(page.locator(`[data-computer="${build7!.computer_id}"]`)).toBeVisible()
  return { build7: build7!, studio: studio!, writes, setConflict: (value: boolean) => { conflict = value } }
}
const row = (page: Page, id: string) => page.locator(`tr[data-row="${id}"]`)
const chip = (page: Page, id: string) => row(page, id).locator('button.ns')

test('one table: a row per computer, a column per account; every cell and list action of the old grid and list is reachable', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { build7, studio } = await setup(page)
  const table = page.getByRole('table', { name: 'Computers and their sign-ins' })
  // Six accounts, each a column with its vendor and short name; the whole address is the tooltip.
  await expect(table.locator('thead .c-acc')).toHaveCount(6)
  const claudeHead = table.locator('thead .ct-ah').filter({ hasText: 'Claude' })
  await expect(claudeHead.locator('small')).toHaveText('markus.barta')
  await expect(claudeHead).toHaveAttribute('data-tip', ADDRESS)
  await expect(table.locator('thead .c-ns')).toHaveText('New starts')
  // The computer column: the name, then agents running or when it was last seen.
  await expect(row(page, build7.computer_id).locator('.pc small')).toHaveText('4 of 8 agents running')
  await expect(row(page, studio.computer_id).locator('.pc small')).toHaveText('Offline · seen 3 h ago')
  await expect(row(page, studio.computer_id)).toHaveClass(/dim/)
  // Cells say one word; an account not signed in on a computer says so to screen readers.
  const claudeCell = row(page, build7.computer_id).locator(`.si[data-signin="${ACCOUNTS.claude}"]`)
  await expect(claudeCell.locator('.sv')).toHaveText('Ready')
  await expect(row(page, studio.computer_id).locator(`.si[data-signin="${ACCOUNTS.studio}"] .sv`)).toHaveText('Offline')
  await expect(row(page, studio.computer_id).locator('td.none .sr-only').first()).toHaveText(/is not signed in on studio$/)
  // The chip says the mode; an offline computer's chip only reads.
  await expect(chip(page, build7.computer_id)).toHaveAccessibleName('New starts on build-7: Smart. Change')
  await expect(row(page, studio.computer_id).locator('.ns.ro')).toHaveText('Fixed · load 8')
  // A cell opens the computer at that sign-in; the row opens the computer; a column head opens the account.
  await claudeCell.click()
  const pane = page.getByRole('dialog', { name: /^build-7/ })
  await expect(pane.locator(`.si-row.is-target[data-signin="${ACCOUNTS.claude}"]`)).toBeVisible()
  await expect(pane.getByRole('button', { name: /New starts/ })).toHaveAttribute('aria-expanded', 'true')
  await expect(row(page, build7.computer_id)).toHaveClass(/current/)
  await pane.getByRole('button', { name: 'More actions for build-7' }).click()
  await expect(page.getByRole('menuitem', { name: /Rename/ })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: /Remove computer/ })).toBeVisible()
  await page.keyboard.press('Escape')
  await row(page, studio.computer_id).locator('.pc').click()
  await expect(page.getByRole('dialog', { name: /^studio/ })).toBeVisible()
  await claudeHead.click()
  await expect(page.getByRole('dialog', { name: 'Claude' })).toBeVisible()
  // Connect a computer, and the notes under the table.
  await expect(page.getByRole('link', { name: 'Connect a computer' })).toHaveAttribute('href', '/agents/register-agent')
  await expect(page.locator('.ct-foot')).toContainText('PAIMOS stores status and timestamps, never the vendor credential.')
  await expect(page.locator('.ct-foot')).toContainText('Only new starts wait; running agents are never stopped.')
  expect(errors).toEqual([])
})

test('an expired sign-in says Expired in its cell and Needs you verifies it', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1000 })
  const { build7 } = await setup(page, { expired: true })
  const cell = row(page, build7.computer_id).locator(`.si[data-signin="${ACCOUNTS.claude}"]`)
  await expect(cell.locator('.sv')).toHaveText('Expired')
  await expect(cell).toHaveAttribute('data-tip', 'Verification expired. New agents wait until it passes again.')
  await expect(page.locator('.needs-block').getByRole('button', { name: 'Verify again' })).toHaveCount(1)
})

test('the New starts chip saves the mode with every other field kept, without moving anything, and Undo restores it', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { build7, studio, writes } = await setup(page)
  const button = chip(page, build7.computer_id)
  const guard = await controlStability(page, { chip: button, row: row(page, build7.computer_id), computer: row(page, build7.computer_id).locator('.pc'), cell: row(page, build7.computer_id).locator(`.si[data-signin="${ACCOUNTS.claude}"]`), other: row(page, studio.computer_id) })
  await guard.check(async () => {
    await button.click()
    const menu = page.getByRole('menu', { name: 'New starts on build-7' })
    await expect(menu.getByRole('heading', { name: 'New starts on build-7' })).toBeVisible()
    await expect(menu.getByRole('menuitemradio', { name: /^Smart/ })).toHaveAttribute('aria-checked', 'true')
    await expect(menu.getByRole('menuitemradio', { name: /^Smart/ })).toContainText('Waits while it is busy or hot.')
    await expect(menu.getByRole('menuitemradio', { name: /^Fixed limit/ })).toContainText('above 12.')
    await expect(menu.getByRole('menuitem', { name: /panel/ })).toContainText('Maximum agents: 8.')
    await menu.getByRole('menuitemradio', { name: /^Fixed limit/ }).click()
    await expect(menu).toHaveCount(0)
    await expect(button).toHaveAccessibleName('New starts on build-7: Fixed · load 12. Change')
  })
  expect(writes).toEqual([{ computer: build7.computer_id, body: { expected_revision: 4, policy: { ...POLICY, mode: 'fixed' } } }])
  await expect(page.getByText('New starts on build-7: Fixed limit · running agents keep working')).toBeVisible()
  await guard.check(async () => {
    await page.getByRole('button', { name: 'Undo', exact: true }).click()
    await expect(button).toHaveAccessibleName('New starts on build-7: Smart. Change')
  })
  // Undo writes the earlier policy back at the revision the save produced.
  expect(writes[1]).toEqual({ computer: build7.computer_id, body: { expected_revision: 5, policy: POLICY } })
  guard.done()
  // The panel's New starts shows the same value.
  await row(page, build7.computer_id).locator('.pc').click()
  await expect(page.getByRole('dialog', { name: /^build-7/ }).getByRole('radiogroup', { name: 'Load setting' }).getByRole('radio', { name: 'Smart' })).toHaveAttribute('aria-checked', 'true')
  expect(errors).toEqual([])
})

test('a changed computer: the chip says so and saves nothing; Undo never overwrites a newer change', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { build7, writes, setConflict } = await setup(page)
  const button = chip(page, build7.computer_id)
  setConflict(true)
  await button.click()
  await page.getByRole('menu', { name: 'New starts on build-7' }).getByRole('menuitemradio', { name: /^Off/ }).click()
  await expect(page.getByText('build-7 changed elsewhere. New starts was not saved; it shows the current setting now.')).toBeVisible()
  await expect(button).toHaveAccessibleName('New starts on build-7: Smart. Change')
  await expect(page.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0)
  setConflict(false)
  await button.click()
  await page.getByRole('menu', { name: 'New starts on build-7' }).getByRole('menuitemradio', { name: /^Off/ }).click()
  await expect(button).toHaveAccessibleName('New starts on build-7: Off. Change')
  // Someone else changes it before Undo: the server refuses, and the chip keeps what is stored.
  setConflict(true)
  await page.getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(page.getByText('build-7 changed elsewhere. New starts was not undone; it shows the current setting now.')).toBeVisible()
  await expect(button).toHaveAccessibleName('New starts on build-7: Off. Change')
  expect(writes.map(w => (w.body as { policy: HostCapacityPolicy }).policy.mode)).toEqual(['off', 'off', 'smart'])
})

test('without management the chip only reads, and cells still open the computer', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { build7 } = await setup(page, { manage: false })
  await expect(page.locator('button.ns')).toHaveCount(0)
  await expect(row(page, build7.computer_id).locator('.ns.ro')).toHaveText('Smart')
  await expect(page.getByRole('link', { name: 'Connect a computer' })).toHaveCount(0)
  await row(page, build7.computer_id).locator(`.si[data-signin="${ACCOUNTS.claude}"]`).click()
  await expect(page.getByRole('dialog', { name: /^build-7/ })).toBeVisible()
})

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 860 }, hasTouch: true })
  test('each computer is a stack of rows, nothing scrolls sideways, targets are 44 px and the chip moves nothing', async ({ page }) => {
    const errors = watchErrors(page)
    const { build7, writes } = await setup(page)
    await expect(page.locator('.ct thead')).toBeHidden()
    const cell = row(page, build7.computer_id).locator(`.si[data-signin="${ACCOUNTS.claude}"]`)
    await expect(cell.locator('.cl')).toHaveText('Claude · markus.barta')
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(0)
    const small = await page.locator('.ct-zone').evaluate(zone => [...zone.querySelectorAll<HTMLElement>('button, a')].filter(e => e.offsetParent).map(e => { const r = e.getBoundingClientRect(); return { name: e.getAttribute('aria-label') ?? e.textContent?.trim(), w: r.width, h: r.height } }).filter(r => r.w < 43.5 || r.h < 43.5))
    expect(small).toEqual([])
    const button = chip(page, build7.computer_id)
    const guard = await controlStability(page, { chip: button, row: row(page, build7.computer_id), cell })
    await guard.check(async () => {
      await button.tap()
      await page.getByRole('menu', { name: 'New starts on build-7' }).getByRole('menuitemradio', { name: /^Off/ }).tap()
      await expect(button).toHaveAccessibleName('New starts on build-7: Off. Change')
    })
    guard.done()
    expect(writes).toHaveLength(1)
    expect(errors).toEqual([])
  })
})

test('past six accounts the computer column stays in view while the table scrolls sideways', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const { build7 } = await setup(page, { apiKey: true })
  const wrap = page.locator('.ct-wrap'), name = row(page, build7.computer_id).locator('.pc')
  await expect(page.locator('.ct thead .c-acc')).toHaveCount(7)
  await expect(page.locator('.ct')).toHaveClass(/many/)
  expect(await wrap.evaluate(e => e.scrollWidth - e.clientWidth)).toBeGreaterThan(0)
  const before = await name.boundingBox()
  await wrap.evaluate(e => { e.scrollLeft = e.scrollWidth })
  await expect.poll(() => wrap.evaluate(e => e.scrollLeft)).toBeGreaterThan(0)
  const after = await name.boundingBox()
  expect(Math.abs(after!.x - before!.x)).toBeLessThanOrEqual(0.5)
  await expect(name).toBeInViewport()
})

test('evidence: the Computers part at 1440, 1024 and 390, light and dark', async ({ page }, info) => {
  test.skip(!process.env.AEON1174_SHOTS, 'evidence run only')
  const { build7 } = await setup(page, { expired: true })
  for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: width > 600 ? 1000 : 860 })
    await page.emulateMedia({ colorScheme: theme })
    await page.goto('/settings/computers')
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    await expect(page.locator(`[data-computer="${build7.computer_id}"]`)).toBeVisible()
    await page.screenshot({ path: info.outputPath(`computers-${width}-${theme}.png`), fullPage: true })
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(0)
    await chip(page, build7.computer_id).click()
    await expect(page.getByRole('menu', { name: 'New starts on build-7' })).toBeVisible()
    await page.screenshot({ path: info.outputPath(`computers-chip-${width}-${theme}.png`) })
    await page.keyboard.press('Escape')
    await row(page, build7.computer_id).locator('.pc').click()
    await expect(page.getByRole('dialog', { name: /^build-7/ })).toBeVisible()
    await page.screenshot({ path: info.outputPath(`computers-panel-${width}-${theme}.png`), fullPage: true })
  }
})
