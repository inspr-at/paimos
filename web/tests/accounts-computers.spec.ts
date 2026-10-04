// SPDX-License-Identifier: AGPL-3.0-only
// AEON-499: Accounts and computers on /agents, computer first. Each computer is
// a card with its status and its accounts inside, one readiness state each; an
// offline computer says why and the one fix and never calls an account ready;
// pacing is one summary button; destructive actions live in row menus.
// AEON499_SHOTS=<dir> also writes screenshots at 1280 and 390.
import { mkdirSync } from 'node:fs'
import AxeBuilder from '@axe-core/playwright'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { ACCOUNTS, NOW, TZ, capacityWorld, type CapacityOptions } from './capacity-fixtures'
import { expectStableControls } from './helpers/stable'
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
async function setup(page: Page, options: CapacityOptions & { manage?: boolean } = {}) {
  await page.clock.setSystemTime(NOW)
  const work = fixtures()
  await mockWork(page, work, { admin: true })
  const data = agentData(world)
  const capacity = capacityWorld(options)
  data.accounts = (capacity.accounts as unknown as typeof data.accounts).map(a => ({ ...a, registered_by_principal_id: me.id }))
  data.approvals = data.approvals.filter(a => a.decision)
  data.messages = data.messages.filter(m => !m.is_action_request)
  await mockAgents(page, data, { capacity, workingPreference: () => work.preferences['agents.working'] })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.read', ...(options.manage === false ? [] : ['account.manage']), 'run.create', 'run.read', 'models.read', 'work_orders.read']
    return route.fulfill({ json: answer })
  })
  return { capacity, work, data }
}
const panel = (page: Page) => page.getByRole('region', { name: 'Accounts and computers' })
const computer = (page: Page, name: string) => panel(page).getByRole('region', { name: `Computer ${name}` })
async function open(page: Page) {
  await page.goto('/agents')
  await expect(computer(page, 'mbp2607')).toBeVisible()
}
const shots = process.env.AEON499_SHOTS
async function shot(page: Page, name: string) {
  if (!shots) return
  mkdirSync(shots, { recursive: true })
  await panel(page).screenshot({ path: `${shots}/${name}.png`, animations: 'disabled' })
}

test('one card per computer: offline says why and the fix, and never calls an account ready', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page)
  await open(page)
  const studio = computer(page, 'studio')
  await expect(studio.getByText(/^Offline since /)).toBeVisible()
  await expect(studio.getByRole('status')).toContainText('stopped reporting at')
  await expect(studio.getByRole('status').locator('code')).toHaveText('aeon-agentd status')
  await expect(studio.getByRole('row')).toHaveCount(1)
  await expect(studio.getByRole('row').first()).toContainText('Paused · computer offline')
  await expect(studio.getByText('Ready', { exact: true })).toHaveCount(0)
  // The legend only where a bar is drawn: the online card has bars, the offline one none.
  await expect(studio.getByText('stop here tonight')).toHaveCount(0)
  const mbp = computer(page, 'mbp2607')
  await expect(mbp.getByText(/^Online · seen /)).toBeVisible()
  await expect(mbp.getByText('stop here tonight')).toBeVisible()
  await expect(mbp.getByRole('row').filter({ hasText: 'Spare' })).toContainText('% left')
  await expect(panel(page).getByText(/ of 6 ready · studio offline$/)).toBeVisible()
  // No trash icon on a row: removal lives in the row menu.
  await expect(panel(page).getByRole('button', { name: /^Remove/ })).toHaveCount(0)
  const axe = await new AxeBuilder({ page }).include('.ac').analyze()
  expect(axe.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
  await shot(page, 'desktop-offline-readings')
})

test('no reading yet is said once, with Check now; unsupported limits and offline computers stay honest', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page, { unmeasured: true, unread: true, unreadCodex: true })
  await open(page)
  // Codex can report a quota: show the empty state once and let the person refresh it.
  const codex = computer(page, 'mbp2607').getByRole('row').filter({ hasText: 'Spare' })
  await expect.poll(async () => (await codex.innerText()).split('No reading yet').length - 1).toBe(1)
  await expect(codex.getByRole('button', { name: 'Check now' })).toBeVisible()
  await expect(codex.getByRole('button', { name: 'Check now' })).toBeEnabled()
  await expect(codex.getByRole('meter')).toHaveCount(0)
  // AEON-623: Pi has no quota reader; a refresh or a run cannot produce its limit.
  const pi = computer(page, 'mbp2607').getByRole('row').filter({ hasText: 'Pi on hsb1' })
  await expect(pi.getByText("Pi doesn't show its limit · one run at a time by day", { exact: true })).toHaveCount(1)
  await expect(pi).not.toContainText('No reading yet')
  await expect(pi).not.toContainText('usage limit')
  await expect(pi.getByRole('button', { name: 'Check now' })).toHaveCount(0)
  await expect(pi.getByRole('meter')).toHaveCount(0)
  const offline = computer(page, 'studio').getByRole('row').first()
  await expect(offline.getByText('No reading while the computer is offline', { exact: true })).toHaveCount(1)
  await expect(offline.getByRole('button', { name: 'Check now' })).toHaveCount(0)
  await expect(offline.getByRole('meter')).toHaveCount(0)
  await shot(page, 'desktop-unmeasured')
})

// AEON-499 review: refresh() never rejected, so a failed read said "No reading yet".
test('Check now says what came back: a failure as a failure, still nothing, then the reading', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { capacity } = await setup(page, { unreadCodex: true })
  await open(page)
  const codex = computer(page, 'mbp2607').getByRole('row').filter({ hasText: 'Spare' })
  const toasts = page.locator('.toast')
  let mode: 'fail' | 'empty' | 'read' = 'fail'
  await page.route('**/api/agent-accounts/capacity', async route => {
    if (route.request().method() !== 'GET' || mode === 'empty') return route.fallback()
    if (mode === 'fail') return route.fulfill({ status: 500, json: { error: 'internal error' } })
    const answer = capacity.handle('/api/agent-accounts/capacity', 'GET', undefined)!.json as { account_id: string; windows: unknown[] }[]
    const main = answer.find(a => a.account_id === ACCOUNTS.main)!
    return route.fulfill({ json: answer.map(a => (a.account_id === ACCOUNTS.spare ? { ...a, windows: main.windows } : a)) })
  })
  // Failed: a 500 is an error with the server's answer, never the reassuring "no reading yet".
  await codex.getByRole('button', { name: 'Check now' }).click()
  await expect(toasts.filter({ hasText: /^Capacity could not be read: The server answered .internal error. \(500\)\. Please try again\./ })).toBeVisible()
  await expect(toasts.filter({ hasText: 'No reading yet' })).toHaveCount(0)
  await expect(codex.getByRole('button', { name: 'Check now' })).toBeEnabled()
  await expect(codex.getByRole('meter')).toHaveCount(0)
  // Empty: the read worked and still has no reading.
  mode = 'empty'
  await codex.getByRole('button', { name: 'Check now' }).click()
  await expect(toasts.filter({ hasText: 'No reading yet — readings need a managed run.' })).toBeVisible()
  await expect(codex.getByText('No reading yet', { exact: true })).toHaveCount(1)
  await expect(codex.getByRole('button', { name: 'Check now' })).toBeEnabled()
  await expect(codex.getByRole('meter')).toHaveCount(0)
  // Successful: the reading arrives and replaces the button with the bar.
  mode = 'read'
  await codex.getByRole('button', { name: 'Check now' }).click()
  await expect(toasts.filter({ hasText: 'Codex on mbp2607: reading updated.' })).toBeVisible()
  await expect(codex.getByRole('meter')).toHaveCount(1)
  await expect(codex).toContainText('42% left')
  await expect(codex.getByRole('button', { name: 'Check now' })).toHaveCount(0)
  await expect(codex.getByText('No reading yet', { exact: true })).toHaveCount(0)
})

// AEON-499 review: --gold-ink on the 17% gold tint was about 4.04:1 in light.
for (const theme of ['light', 'dark'] as const) {
  test(`warning pills and fixes clear 4.5:1 in ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 1100 })
    const { work } = await setup(page)
    work.preferences.theme = { choice: theme }
    await open(page)
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    const warn = panel(page).locator('.pill.warn, .n-copy')
    expect(await warn.count()).toBeGreaterThan(1)
    // Composite each tint over the raised surface on a canvas, then measure text against it.
    const ratios = await warn.evaluateAll(els => {
      const canvas = document.createElement('canvas')
      canvas.width = canvas.height = 1
      const ctx = canvas.getContext('2d', { willReadFrequently: true })!
      const surface = getComputedStyle(document.documentElement).getPropertyValue('--surface-raised').trim()
      const paint = (...fills: string[]) => { ctx.clearRect(0, 0, 1, 1); for (const f of fills) { ctx.fillStyle = f; ctx.fillRect(0, 0, 1, 1) } return [...ctx.getImageData(0, 0, 1, 1).data.slice(0, 3)] }
      const lum = (rgb: number[]) => { const [r, g, b] = rgb.map(v => { const c = v / 255; return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4 }); return 0.2126 * r + 0.7152 * g + 0.0722 * b }
      const tinted = (el: Element): string[] => { const out: string[] = []; for (let n: Element | null = el; n && n !== document.documentElement; n = n.parentElement) { const bg = getComputedStyle(n).backgroundColor; if (bg && !/^rgba\(0, 0, 0, 0\)$|transparent/.test(bg) && n.matches('.pill, .notice')) out.unshift(bg) } return out }
      return els.map(el => {
        const bg = paint(surface, ...tinted(el))
        const fg = paint(surface, ...tinted(el), getComputedStyle(el).color)
        const [hi, lo] = [lum(fg), lum(bg)].sort((a, b) => b - a)
        return { text: (el.textContent || '').trim(), ratio: Math.round(((hi + 0.05) / (lo + 0.05)) * 100) / 100 }
      })
    })
    for (const r of ratios) expect(r.ratio, `${r.text} in ${theme}`).toBeGreaterThanOrEqual(4.5)
  })
}

test('pacing is one summary button; the existing settings and editors open from it', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { capacity } = await setup(page)
  await open(page)
  const button = panel(page).getByRole('button', { name: /5 days · keep auto · no nights/ })
  await expect(button).toBeVisible()
  await button.click()
  const pacing = page.getByRole('dialog', { name: 'Pacing' })
  await expect(pacing.getByRole('radiogroup', { name: 'Work days a week' })).toBeVisible()
  await shot(page, 'desktop-pacing')
  await pacing.getByRole('radio', { name: '7' }).click()
  await expect.poll(() => capacity.puts.length).toBeGreaterThan(0)
  await expect(panel(page).getByRole('button', { name: /7 days · keep auto · no nights/ })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(pacing).toHaveCount(0)
})

test('the row menu carries Sprint, Hold and Remove; the computer menu its own actions', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page)
  await open(page)
  await computer(page, 'mbp2607').getByRole('button', { name: 'More for Codex Spare' }).click()
  const menu = page.getByRole('menu')
  await expect(menu.getByRole('menuitem', { name: /Sprint Codex until reset/ })).toBeVisible()
  await expect(menu.getByRole('menuitem', { name: /Remove account/ })).toBeVisible()
  await shot(page, 'desktop-row-menu')
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
  await computer(page, 'studio').getByRole('button', { name: 'More for studio' }).click()
  await expect(page.getByRole('menu').getByRole('menuitem', { name: /Details/ })).toBeVisible()
})

test('phone: stacked rows, no sideways scroll', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 900 })
  await setup(page)
  await open(page)
  await expect(computer(page, 'studio').getByRole('status')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
  await shot(page, 'phone')
})

test('Agents at once saves one ceiling and independent harness limits', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { work } = await setup(page)
  await open(page)
  const working = page.getByRole('region', { name: 'Agents at once' })
  await expect(working.locator('.f-num')).toHaveText('15')
  await working.getByRole('button', { name: 'One agent more at once' }).click()
  await expect(working.locator('.f-num')).toHaveText('16')
  await expect.poll(() => work.preferences['agents.working']).toEqual({ total: 16, limits: {} })
  const codex = working.locator('[data-key="codex"]')
  await codex.getByRole('radio', { name: 'At most', exact: true }).click()
  await codex.getByRole('button', { name: 'Codex: at most one more' }).click()
  await expect(codex.locator('.lim-num')).toHaveText('3')
  await expect.poll(() => work.preferences['agents.working']).toEqual({ total: 16, limits: { codex: 3 } })
  await expect(working).not.toContainText(/assigned|flexible|target|plan/i)
  const axe = await new AxeBuilder({ page }).include('.working').analyze()
  expect(axe.violations.map(v => v.id)).toEqual([])
  if (shots) { mkdirSync(shots, { recursive: true }); await working.screenshot({ path: `${shots}/working.png`, animations: 'disabled' }) }
  await page.setViewportSize({ width: 390, height: 900 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
  if (shots) await working.screenshot({ path: `${shots}/working-phone.png`, animations: 'disabled' })
})

for (const running of [0, 2]) {
  test(`with ${running} running the backend's default ceiling stays 15`, async ({ page }) => {
    const { data } = await setup(page)
    data.sessions = data.sessions.filter(s => !s.stopped_at).slice(0, running)
    await open(page)
    const working = page.getByRole('region', { name: 'Agents at once' })
    await expect(working.locator('.f-num')).toHaveText('15')
    await expect(working.locator('.f-live')).toContainText(`${running} running`)
    await expect(working.getByRole('button', { name: 'One agent fewer at once' })).toBeEnabled()
    await working.getByRole('button', { name: 'One agent more at once' }).click()
    await expect(working.locator('.f-num')).toHaveText('16')
  })
}

test('stored ceiling arrives without a made-up total while the snapshot loads', async ({ page }) => {
  await setup(page)
  let release: () => void = () => {}
  const held = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/agents/plan', async route => {
    await held
    return route.fulfill({ json: { total: 3, limits: {}, principal_id: me.id, running: { codex: 5 }, running_total: 5, source: 'legacy', updated_at: null } })
  })
  await open(page)
  const working = page.getByRole('region', { name: 'Agents at once' })
  await expect(working).toContainText('Reading the total…')
  await expect(working.locator('.f-num')).toHaveCount(0)
  release()
  await expect(working.locator('.f-num')).toHaveText('3')
  await expect(working.locator('.f-num')).toHaveAttribute('aria-label', 'Run up to 3 at once')
  await expect(working.locator('.f-live')).toContainText('5 running · winding down to 3')
})

// AEON-581: the pin is a prerequisite, independent of usage or connectivity.
test('Touch ID pairing readiness and upgrade guidance keep the computer control in place', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 1000 })
  const { capacity } = await setup(page)
  Object.assign(capacity.computers[0]!, { local_auth_pinned: true })
  await open(page)
  const card = computer(page, 'mbp2607')
  await expect(card.getByText('Touch ID confirmation: ready (pairing key pinned)', { exact: true })).toBeVisible()
  const more = card.getByRole('button', { name: 'More for mbp2607', exact: true })
  const before = await more.boundingBox()
  Object.assign(capacity.computers[0]!, { local_auth_pinned: false })
  await page.reload()
  await expect(card.getByText(/Touch ID confirmation: needs pairing upgrade/)).toBeVisible()
  await expect(card.getByText(/run aeon-agentd status on this computer for its exact pairing command/)).toBeVisible()
  const after = await more.boundingBox()
  expect(before).not.toBeNull()
  expect(after).not.toBeNull()
  expect(after!.x).toBe(before!.x)
  expect(after!.y).toBe(before!.y)
  expect(after!.width).toBe(before!.width)
  expect(after!.height).toBe(before!.height)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`AEON-685: owner verification stays put at ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1100 })
    const { capacity, data } = await setup(page)
    const c = capacity.computers[0] as unknown as PairingView
    const e = c.enrollments.find(e => e.account_id === ACCOUNTS.claude)!
    e.can_verify = true
    e.verification_state = 'expired'
    e.verification_expired_ready = true
    c.verification_capabilities = { claude: { supported: true, policy: 'read_only', reason: '' } }
    c.revision = 1
    c.harness_statuses = { claude: 'ready' }
    c.harness_details = { claude: { state: 'ready' } }
    e.label = 'Markus Barta – langfristige Überprüfung des persönlichen Agentenkontos'
    data.accounts.find(a => a.id === ACCOUNTS.claude)!.label = e.label
    let attempts = 0
    await page.route('**/api/agent-pairing/computers/*/enrollments/*/verify', async route => {
      expect(route.request().postDataJSON()).toEqual({ expected_revision: 1, expected_verification_run_id: e.verification_run_id })
      attempts++
      if (attempts === 1) return route.fulfill({ status: 403, json: { code: 'forbidden' } })
      e.verification_run_id = `b0000000-0000-4000-8000-${String(683 + attempts).padStart(12, '0')}`
      e.verification_state = 'queued'
      e.verification_expired_ready = false
      return route.fulfill({ json: { account_id: e.account_id, run_id: e.verification_run_id, expires_at: '2026-10-01T13:00:00Z' } })
    })
    await page.addInitScript(theme => { document.documentElement.dataset.theme = theme }, theme)
    await page.goto(`/agents?verify_account=${ACCOUNTS.claude}`)
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    const row = panel(page).locator(`[data-account="${ACCOUNTS.claude}"]`)
    const button = row.getByRole('button', { name: 'Verify again', exact: true })
    const more = row.getByRole('button', { name: /^More for/ })
    await expect(button).toBeVisible()
    await expect(row.getByText('Ready', { exact: true })).toBeVisible()
    await expectStableControls({
      controls: { verify: button, more, clickedRow: row }, scrollAreas: { panel: panel(page) },
      interactions: [
        { name: 'denied verification', run: async () => { await button.click(); await expect(page.getByText('Only the account owner may verify it again.', { exact: true })).toBeVisible(); await expect(row.getByText('Ready', { exact: true })).toBeVisible() } },
        { name: 'queued verification', run: async () => { await button.click(); await expect(row.getByText('Verification queued', { exact: true })).toBeVisible() } },
        { name: 'retry binds to the refreshed run', run: async () => { await expect(button).toBeEnabled(); await button.click(); await expect(button).toBeEnabled(); await expect(row.getByText('Verification queued', { exact: true })).toBeVisible() } },
      ],
    })
    expect(attempts).toBe(3)
    mkdirSync('test-results/aeon-685', { recursive: true })
    await panel(page).screenshot({ path: `test-results/aeon-685/accounts-${width}-${theme}.png`, animations: 'disabled' })
  })
}

test('AEON-685: tablet verification control is independent of the status label width', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 1100 })
  const { capacity } = await setup(page)
  const c = capacity.computers[0] as unknown as PairingView
  const e = c.enrollments.find(e => e.account_id === ACCOUNTS.claude)!
  Object.assign(e, { can_verify: true, verification_state: 'expired', verification_expired_ready: true })
  Object.assign(c, { revision: 1, harness_statuses: { claude: 'ready' }, harness_details: { claude: { state: 'ready' } }, verification_capabilities: { claude: { supported: true, policy: 'read_only', reason: '' } } })
  await open(page)
  const row = panel(page).locator(`[data-account="${ACCOUNTS.claude}"]`)
  const button = row.getByRole('button', { name: 'Verify again', exact: true })
  await expect(row.getByText('Ready', { exact: true })).toBeVisible()
  expect(await row.evaluate(el => getComputedStyle(el).gridTemplateColumns.split(' ').length)).toBe(3)
  await expectStableControls({
    controls: { verify: button, more: row.getByRole('button', { name: /^More for/ }), clickedRow: row },
    scrollAreas: { panel: panel(page) },
    interactions: [{ name: 'server status changes', run: async () => {
      Object.assign(e, { verification_state: 'queued', verification_expired_ready: false })
      await page.reload()
      await expect(row.getByText('Verification queued', { exact: true })).toBeVisible()
    } }],
  })
})

for (const width of [390, 900, 1024, 1280, 1440]) {
test(`AEON-685: approval link expands once and honors folding at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1100 })
  const { work } = await setup(page)
  work.preferences['agents-page'] = { setupFolded: true }
  await page.goto(`/agents?verify_account=${ACCOUNTS.claude}`)
  const fold = panel(page).getByRole('button', { name: 'Accounts and computers', exact: true })
  await expect(fold).toHaveAttribute('aria-expanded', 'true')
  await expect(computer(page, 'mbp2607')).toBeVisible()
  await expectStableControls({
    controls: { fold, modelPreferences: panel(page).getByRole('button', { name: 'Account model preferences', exact: true }) },
    scrollAreas: { panel: panel(page) },
    interactions: [
      { name: 'fold with approval query still present', run: async () => {
        await fold.click()
        await expect(fold).toHaveAttribute('aria-expanded', 'false')
        await expect(computer(page, 'mbp2607')).toHaveCount(0)
        await expect.poll(() => work.preferences['agents-page']).toEqual({ setupFolded: true })
        expect(new URL(page.url()).searchParams.get('verify_account')).toBe(ACCOUNTS.claude)
      } },
      { name: 'explicitly expand again', run: async () => {
        await fold.click()
        await expect(fold).toHaveAttribute('aria-expanded', 'true')
        await expect(computer(page, 'mbp2607')).toBeVisible()
        await expect.poll(() => work.preferences['agents-page']).toEqual({ setupFolded: false })
      } },
      { name: 'remember folded choice', run: async () => {
        await fold.click()
        await expect(fold).toHaveAttribute('aria-expanded', 'false')
        await expect.poll(() => work.preferences['agents-page']).toEqual({ setupFolded: true })
      } },
    ],
  })
  await page.goto('/agents')
  await expect(fold).toHaveAttribute('aria-expanded', 'false')
  await expect(computer(page, 'mbp2607')).toHaveCount(0)
  // A later approval link is a new request to reveal its account.
  await page.goto(`/agents?verify_account=${ACCOUNTS.spare}`)
  await expect(fold).toHaveAttribute('aria-expanded', 'true')
  await expect(computer(page, 'mbp2607')).toBeVisible()
})
}

test('AEON-685: queued verification reports a failed pairing refresh', async ({ page }) => {
  const { capacity } = await setup(page)
  const c = capacity.computers[0] as unknown as PairingView
  const e = c.enrollments.find(e => e.account_id === ACCOUNTS.claude)!
  Object.assign(e, { can_verify: true, verification_state: 'expired', verification_expired_ready: true })
  Object.assign(c, { revision: 1, harness_statuses: { claude: 'ready' }, harness_details: { claude: { state: 'ready' } }, verification_capabilities: { claude: { supported: true, policy: 'read_only', reason: '' } } })
  await open(page)
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ status: 500, json: { error: 'pairing unavailable' } }))
  await page.route('**/api/agent-pairing/computers/*/enrollments/*/verify', route => route.fulfill({ json: { account_id: e.account_id, run_id: 'b0000000-0000-4000-8000-000000000685' } }))
  await panel(page).locator(`[data-account="${ACCOUNTS.claude}"]`).getByRole('button', { name: 'Verify again', exact: true }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Verification was queued, but the account list could not refresh.' })).toBeVisible()
})
