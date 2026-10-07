// SPDX-License-Identifier: AGPL-3.0-only
// Risks: lost verification progress/results, moving controls, wrapping metrics,
// off-centre tree tracks, and the hours editor overlapping the app footer.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { ACCOUNTS, capacityWorld, NOW, TZ } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { defaultHostPolicy } from '../src/lib/hostCapacity'
import type { PairingView } from '../src/lib/agentPairing'
import { controlStability } from './control-stability'

test.use({ timezoneId: TZ })
const run = 'b0000000-0000-4000-8000-000000000908'
async function setup(page: Page, theme: 'light' | 'dark') {
  await page.clock.install({ time: NOW })
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  work.preferences['ui.agents.sections'] = { dial: true, accounts: true, sessions: true, queued: true }
  await mockWork(page, work, { admin: true })
  const world = capacityWorld(), data = agentData({ me: me.id, now: NOW, projects: {}, tickets: {}, nodes: {} })
  data.accounts = world.accounts as unknown as typeof data.accounts
  data.approvals = []; data.messages = []; data.targets = []
  const c = world.computers[0] as unknown as PairingView
  c.revision = 4
  for (const machine of world.computers as unknown as PairingView[]) {
    machine.connectivity = 'online'
    machine.harness_statuses = { codex: 'ready', claude: 'ready', grok: 'ready', cursor: 'ready' }
    machine.harness_details = { codex: { state: 'ready' }, claude: { state: 'ready' }, grok: { state: 'ready' }, cursor: { state: 'ready' } }
  }
  c.verification_capabilities = { ...c.verification_capabilities, claude: { supported: true, policy: 'read_only', reason: '' } }
  const e = c.enrollments.find(e => e.account_id === ACCOUNTS.claude)!
  Object.assign(e, { can_verify: true, verification_state: 'expired', verification_expired_ready: false, label: 'markus.barta@augmentoring.com', local_cleanup: 'confirmed' })
  const second = c.enrollments.find(e => e.account_id === ACCOUNTS.grok)!
  Object.assign(second, { harness: 'claude', label: 'administration-mit-langer-deutscher-kontobezeichnung@augmentoring.com', state: 'revoked', verification_state: 'not_selected' })
  Object.assign(world.accounts.find(a => a.id === ACCOUNTS.grok)!, { harness: 'claude', label: second.label })
  Object.assign(c, { host_capacity: { policy: defaultHostPolicy(), signals: { load: 32.4, cores: 18, memory_used_gb: 39.3, memory_total_gb: 64, memory_pressure: 'normal', power: 'plugged_in', thermal: 'normal' }, running: 8, queued: 4, reported_at: new Date(NOW).toISOString(), reason: '', load_limit: 0, history: [] } })
  const base = data.sessions[0]!
  data.sessions = [
    { ...base, display_label: 'Release lead', parent_harness_session_id: null, role: 'coordinator' },
    { ...base, id: '5e000000-0000-4000-8000-000000000908', display_label: 'Prüfung der verschachtelten Arbeitsaufträge', parent_harness_session_id: base.id, role: 'worker' },
    { ...base, id: '5e000000-0000-4000-8000-000000000909', display_label: 'Nested worker', parent_harness_session_id: '5e000000-0000-4000-8000-000000000908', role: 'worker' },
  ] as typeof data.sessions
  await mockAgents(page, data, { capacity: world })
  await page.route('**/api/agent-accounts/*/capacity/approve', route => route.fulfill({ status: 204 }))
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin')
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', 'account.manage', 'settings.manage']
    return route.fulfill({ json: answer })
  })
  await page.route('**/api/settings/quota-warnings', route => route.fulfill({ json: { early_percent: 5, urgent_percent: 2 } }))
  const refresh = async () => {
    const response = page.waitForResponse(r => new URL(r.url()).pathname === '/api/agent-pairing/computers')
    await page.clock.runFor(20_001)
    await response
  }
  return { world, c, e, refresh }
}

for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`verification progress and computer panel remain stable ${width} ${theme}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    const { c, e, refresh } = await setup(page, theme)
    let release!: () => void
    const barrier = new Promise<void>(resolve => { release = resolve })
    let posted: unknown
    await page.route('**/api/agent-pairing/computers/*/enrollments/*/verify', async route => {
      posted = route.request().postDataJSON()
      await barrier
      Object.assign(e, { verification_state: 'queued', verification_run_id: run })
      await route.fulfill({ json: { account_id: e.account_id, run_id: run } })
    })
    await page.goto('/agents')
    const section = page.getByRole('region', { name: 'Accounts and computers' })
    const row = section.locator(`[data-need="verify:${ACCOUNTS.claude}:d0000000-0000-4000-8000-000000000001"]`)
    const verify = row.locator('.verify-button')
    await expect(verify).toBeVisible()
    const guard = await controlStability(page, { verify })
    await guard.check(async () => { await verify.click(); await expect(verify).toHaveAttribute('aria-busy', 'true') })
    await expect(verify).toHaveAccessibleName('Verifying…')
    await guard.check(async () => { release(); await expect(section).toContainText('Waiting for the computer to start') })
    expect(posted).toEqual({ expected_revision: c.revision, expected_verification_run_id: null })
    await guard.check(async () => { Object.assign(e, { verification_state: 'running' }); await refresh(); await expect(section).toContainText('Check running') })
    await page.screenshot({ path: info.outputPath(`agents-verifying-${width}-${theme}.png`), fullPage: true })
    await guard.check(async () => { Object.assign(e, { verification_state: 'completed' }); await refresh(); await expect(section).toContainText('Verification passed.') })
    guard.done()
    await expect(verify).toBeEnabled()
    await expect(section.getByRole('heading', { name: '1 thing needs you' })).toHaveCount(0)
    await page.screenshot({ path: info.outputPath(`agents-result-${width}-${theme}.png`), fullPage: true })
    await section.getByRole('button', { name: 'Accounts and computers', exact: true }).click()
    await expect(section.locator('.verification-feedback')).toHaveText('Verification passed.')
    await page.screenshot({ path: info.outputPath(`agents-folded-result-${width}-${theme}.png`), fullPage: true })
    Object.assign(e, { verification_state: 'expired', verification_run_id: null })
    await page.goto(`/settings/accounts?computer=${c.computer_id}`)
    const pane = page.locator('section.pane')
    const footer = pane.locator('.pane-foot .verify-button')
    await expect(footer).toBeVisible()
    await expect(pane.locator('.si-who .name-link').filter({ hasText: 'markus.barta' })).toHaveText('Claude · markus.barta@augmentoring.com')
    await expect(pane).not.toContainText('not_selected')
    await expect(pane).not.toContainText('claude needs')
    for (const value of ['39.3 / 64 GB', 'Plugged in']) {
      const dd = pane.locator('.facts dd').filter({ hasText: value })
      await expect(dd).toBeVisible()
      expect(await dd.evaluate(el => el.getBoundingClientRect().height)).toBeLessThan(30)
    }
    const identity = pane.locator('.si-who .name-link').filter({ hasText: 'administration-mit' })
    await identity.scrollIntoViewIfNeeded()
    await identity.focus()
    await expect(identity).toHaveAttribute('data-tip', /administration-mit-langer/)
    await page.screenshot({ path: info.outputPath(`computer-panel-${width}-${theme}.png`), fullPage: true })
    const panelGuard = await controlStability(page, { footer, close: pane.getByRole('button', { name: 'Close details' }), frame: pane })
    await panelGuard.check(async () => { await footer.click(); await expect(footer).toHaveAttribute('aria-busy', 'true') })
    await panelGuard.check(async () => { Object.assign(e, { verification_state: 'queued' }); c.harness_statuses = { ...c.harness_statuses, claude: 'blocked' }; c.harness_details = { ...c.harness_details, claude: { state: 'blocked', reason: 'probe_failed' } }; await refresh(); await expect(pane.locator('.si-feedback')).toContainText('Verification is queued. Waiting for this computer: Account availability check failed.') })
    panelGuard.done()
    await page.screenshot({ path: info.outputPath(`computer-result-${width}-${theme}.png`), fullPage: true })
    await page.goto(`/settings/accounts?account=${ACCOUNTS.claude}`)
    const useRow = pane.locator(`.use-sec [data-account="${ACCOUNTS.claude}"]`)
    const use = useRow.getByRole('switch', { name: /Agents may use it/ })
    await use.scrollIntoViewIfNeeded()
    const useGuard = await controlStability(page, { use, details: useRow.getByRole('button', { name: /^Details for/ }), close: pane.getByRole('button', { name: 'Close details' }) })
    const initial = await use.getAttribute('aria-checked')
    const toggleUse = async (checked: string) => {
      await use.click()
      if (checked === 'false') await page.getByRole('dialog', { name: /^Drain / }).getByRole('button', { name: 'Drain account', exact: true }).click()
      await expect(use).toHaveAttribute('aria-checked', checked)
      await expect(use).not.toHaveAttribute('aria-disabled', 'true')
    }
    await useGuard.check(() => toggleUse(initial === 'true' ? 'false' : 'true'))
    expect(await use.locator('.thumb').evaluate(el => getComputedStyle(el).backgroundColor)).toBe('rgb(251, 250, 246)')
    await useGuard.check(() => toggleUse(initial!))
    useGuard.done()
    await page.screenshot({ path: info.outputPath(`account-use-${width}-${theme}.png`), fullPage: true })
  })

  test(`tree, hours spacing and light switch thumbs ${width} ${theme}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    await setup(page, theme)
    await page.goto('/agents')
    const tree = page.getByRole('treegrid', { name: 'Agent sessions' })
    await expect(tree.locator('.tree-stem')).toHaveCount(2)
    const joints = await tree.locator('.row:has(.tree-stem)').evaluateAll(rows => rows.map(row => {
      const fold = row.querySelector('.tree-fold')!.getBoundingClientRect()
      const stem = row.querySelector('.tree-stem')!.getBoundingClientRect()
      return { centre: fold.x + fold.width / 2, stem: stem.x + stem.width / 2, y: fold.y + fold.height / 2, start: stem.y }
    }))
    for (const joint of joints) expect(Math.abs(joint.centre - joint.stem), 'stem centred under chevron').toBeLessThanOrEqual(.5)
    const rows = await tree.locator('.row').evaluateAll(rows => rows.map(row => { const r = row.getBoundingClientRect(); return { y: r.y, bottom: r.bottom } }))
    for (let i = 1; i < rows.length; i++) expect(rows[i]!.y - rows[i - 1]!.bottom).toBeGreaterThanOrEqual(0)
    const fold = tree.locator('.tree-fold').first()
    const treeGuard = await controlStability(page, { fold, row: tree.locator('.row').first() })
    await treeGuard.check(async () => { await fold.click(); await expect(tree.locator('.row')).toHaveCount(1) })
    await treeGuard.check(async () => { await fold.click(); await expect(tree.locator('.row')).toHaveCount(3) })
    treeGuard.done()
    await page.screenshot({ path: info.outputPath(`session-tree-${width}-${theme}.png`), fullPage: true })
    await page.goto('/settings/accounts#capacity-and-load')
    const nights = page.getByRole('switch', { name: 'Agents at night' })
    await nights.scrollIntoViewIfNeeded()
    const switchGuard = await controlStability(page, { nights, gear: page.getByRole('button', { name: 'Customize night and shifts' }) })
    await switchGuard.check(async () => { await nights.click(); await expect(nights).toHaveAttribute('aria-checked', 'true') })
    expect(await nights.evaluate(el => getComputedStyle(el, '::after').backgroundColor)).toBe('rgb(251, 250, 246)')
    switchGuard.done()
    await page.getByRole('button', { name: 'Customize night and shifts' }).click()
    const editor = page.getByRole('dialog', { name: 'Agents outside your hours' })
    await expect(editor).toBeVisible()
    const modelRows = Object.fromEntries((await editor.locator('.model').all()).map((row, index) => [`model${index}`, row]))
    const controls = { ...(width === 390 ? { frame: editor } : {}), ...modelRows, save: editor.getByRole('button', { name: 'Save', exact: true }), cancel: editor.getByRole('button', { name: 'Cancel', exact: true }), options: editor.locator('.models') }
    const hoursGuard = await controlStability(page, controls)
    for (const option of await editor.locator('.model').all()) await hoursGuard.check(async () => { await option.click() })
    await hoursGuard.check(async () => { await editor.locator('.model').nth(1).click() })
    hoursGuard.done()
    await editor.locator('.ed-body').evaluate(el => { el.scrollTop = 0 })
    if (width > 720) {
      const box = await editor.boundingBox()
      expect(box!.y + box!.height, 'space above app footer').toBeLessThanOrEqual(944)
    }
    await page.screenshot({ path: info.outputPath(`outside-hours-${width}-${theme}.png`), fullPage: true })
    await editor.getByRole('button', { name: 'Cancel', exact: true }).click()
    await page.getByRole('button', { name: 'Customize work week' }).click()
    const week = page.getByRole('dialog', { name: 'Work week', exact: true })
    const monday = week.getByRole('switch', { name: 'Monday', exact: true })
    const weekGuard = await controlStability(page, { monday, row: week.locator('.wk-row').first(), save: week.getByRole('button', { name: 'Save', exact: true }), cancel: week.getByRole('button', { name: 'Cancel', exact: true }), ...(width === 390 ? { frame: week } : {}) })
    const wasOn = await monday.getAttribute('aria-checked')
    await weekGuard.check(async () => { await monday.click(); await expect(monday).toHaveAttribute('aria-checked', wasOn === 'true' ? 'false' : 'true') })
    expect(await monday.evaluate(el => getComputedStyle(el, '::after').backgroundColor)).toBe('rgb(251, 250, 246)')
    weekGuard.done()
    await page.screenshot({ path: info.outputPath(`work-week-${width}-${theme}.png`), fullPage: true })
  })
}

// An error is visible in place; a late response cannot belong to another
// computer's sheet after navigation changes the selected record.
test('verification errors stay honest and changing computer discards pending feedback', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 1000 })
  const { c, world } = await setup(page, 'dark')
  const other = world.computers[1]! as unknown as PairingView
  other.revision = 7
  other.enrollments.push({ ...c.enrollments.find(e => e.account_id === ACCOUNTS.claude)! })
  const targets: string[] = []
  let reject = true, release!: () => void
  const barrier = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/agent-pairing/computers/*/enrollments/*/verify', async route => {
    targets.push(new URL(route.request().url()).pathname)
    if (reject) { await route.fulfill({ status: 409, json: { code: 'revision_conflict' } }); return }
    await barrier
    await route.fulfill({ json: { account_id: ACCOUNTS.claude, run_id: run } })
  })
  // The same approved account can be enrolled on multiple computers. The
  // clicked attention row must request only its own computer's verification.
  await page.goto('/settings/accounts')
  const attention = page.locator(`[data-need="verify:${other.computer_id}:${ACCOUNTS.claude}"]`)
  const attentionVerify = attention.locator('.verify-button')
  const attentionGuard = await controlStability(page, { attentionVerify })
  await attentionGuard.check(async () => { await attentionVerify.click(); await expect(attention.getByRole('status')).toContainText('account or verification changed') })
  attentionGuard.done()
  expect(targets).toEqual([`/api/agent-pairing/computers/${other.computer_id}/enrollments/${ACCOUNTS.claude}/verify`])
  await page.goto(`/settings/accounts?computer=${c.computer_id}`)
  const pane = page.locator('section.pane'), verify = pane.locator('.pane-foot .verify-button')
  const guard = await controlStability(page, { verify, close: pane.getByRole('button', { name: 'Close details' }) })
  await guard.check(async () => { await verify.click(); await expect(pane.locator('.si-feedback')).toContainText('account or verification changed') })
  await expect(verify).toBeEnabled()
  await expect(pane).not.toContainText('Verification passed.')
  guard.done()
  reject = false
  await verify.click()
  await expect(verify).toHaveAttribute('aria-busy', 'true')
  await page.goto(`/settings/accounts?computer=${world.computers[1]!.computer_id}`)
  await expect(pane).toContainText(world.computers[1]!.computer_name)
  release()
  await expect(pane.locator('.si-feedback')).toHaveCount(0)
  await expect(pane).not.toContainText('Verifying…')
})
