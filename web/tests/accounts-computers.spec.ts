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
  data.accounts = capacity.accounts as unknown as typeof data.accounts
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

test('no reading yet is said once, with Check now; an offline computer has no reading', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page, { unmeasured: true, unread: true })
  await open(page)
  // Every vendor without a reading, Pi and Cursor included: "No reading yet" and Check now (Ready.dc.html).
  const pi = computer(page, 'mbp2607').getByRole('row').filter({ hasText: 'Pi on hsb1' })
  await expect(pi).toContainText('No reading yet')
  await expect(pi).not.toContainText('usage limit')
  await expect(pi.getByRole('button', { name: 'Check now' })).toBeVisible()
  await expect(computer(page, 'studio').getByRole('row').first()).toContainText('No reading while the computer is offline')
  await shot(page, 'desktop-unmeasured')
})

// AEON-499 review: refresh() never rejected, so a failed read said "No reading yet".
test('Check now says what came back: a failure as a failure, still nothing, then the reading', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  const { capacity } = await setup(page, { unread: true })
  await open(page)
  const pi = computer(page, 'mbp2607').getByRole('row').filter({ hasText: 'Pi on hsb1' })
  const toasts = page.locator('.toast')
  let mode: 'fail' | 'empty' | 'read' = 'fail'
  await page.route('**/api/agent-accounts/capacity', async route => {
    if (route.request().method() !== 'GET' || mode === 'empty') return route.fallback()
    if (mode === 'fail') return route.fulfill({ status: 500, json: { error: 'internal error' } })
    const answer = capacity.handle('/api/agent-accounts/capacity', 'GET', undefined)!.json as { account_id: string; windows: unknown[] }[]
    const spare = answer.find(a => a.windows.length)!
    return route.fulfill({ json: answer.map(a => (a.account_id === ACCOUNTS.pi ? { ...a, windows: spare.windows } : a)) })
  })
  // Failed: a 500 is an error with the server's answer, never the reassuring "no reading yet".
  await pi.getByRole('button', { name: 'Check now' }).click()
  await expect(toasts.filter({ hasText: /^Capacity could not be read: The server answered .internal error. \(500\)\. Please try again\./ })).toBeVisible()
  await expect(toasts.filter({ hasText: 'No reading yet for Pi' })).toHaveCount(0)
  await expect(pi.getByRole('button', { name: 'Check now' })).toBeEnabled()
  // Empty: the read worked and still has no reading.
  mode = 'empty'
  await pi.getByRole('button', { name: 'Check now' }).click()
  await expect(toasts.filter({ hasText: 'No reading yet for Pi on mbp2607. It arrives with its next run.' })).toBeVisible()
  // Successful: the reading arrives and replaces the button with the bar.
  mode = 'read'
  await pi.getByRole('button', { name: 'Check now' }).click()
  await expect(toasts.filter({ hasText: 'Pi on mbp2607: reading updated.' })).toBeVisible()
  await expect(pi.getByRole('meter')).toHaveCount(1)
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
