// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

// AEON-440: a started attach is easy to find. /agents lists the owner's waiting
// requests (no code), the terminal's link only fills the code in, and expiry or
// cancellation is shown instead of silence.
const shots = process.env.ATTACH_DISCOVERABLE_SHOTS ?? '/private/tmp/aeon-440-shots'
const PENDING = '**/api/agent-pairing/attach/pending'

type State = 'pending' | 'approved' | 'detached' | 'unreachable'
function request(id: string, state: State, options: { mins?: number; host?: string; harness?: string; mode?: 'lease'; consent?: 'aeon' | 'local_auth' } = {}) {
  return {
    request_id: id, request_digest: 'a'.repeat(64), consent_digest: 'b'.repeat(64), consent_mode: options.consent ?? 'aeon', state,
    expires_at: new Date(Date.now() + (options.mins ?? 9) * 60_000).toISOString(),
    snapshot: {
      ...(options.mode ? { mode: options.mode } : {}), platform: 'darwin', computer_id: 'computer-fixture', project_id: 'p-pharos', ticket_id: 'n-2', host: options.host ?? 'Markus’s MacBook', harness: options.harness ?? 'claude',
      transcript: options.mode ? '' : '/Users/markus/.claude/projects/pharos/session.jsonl', file_id: options.mode ? '' : '1:234567',
      process: { pid: 4812, uid: 501, started: '2026-09-30T00:12:30Z', executable: '/Users/markus/.local/bin/claude', cwd: '/Users/markus/Code/pharos' },
    },
  }
}

async function setup(page: Page, options: { theme?: 'light' | 'dark'; manage?: boolean } = {}) {
  const work = fixtures()
  work.preferences.theme = { choice: options.theme ?? 'light' }
  await mockWork(page, work, { admin: true })
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ json: { computers: [] } }))
  await page.route('**/api/me/permissions*', route => {
    const project = new URL(route.request().url()).searchParams.get('project_id') ?? undefined
    const value = mockEffectivePermissions('admin', project)
    if (options.manage !== false) value.workspace.permissions = [...value.workspace.permissions, 'account.manage']
    return route.fulfill({ json: value })
  })
  const data = agentData({
    me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
    tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
    nodes: { 'p-pharos': { key: 'PHAROS', title: 'Pharos' }, 'n-2': { key: 'PHAROS-12', title: 'PDF worker image' } },
  })
  data.runs.splice(0); data.approvals.splice(0); data.messages.splice(0)
  await mockAgents(page, data)
  await page.route('**/api/nodes/p-pharos', route => route.fulfill({ json: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' } }))
  await page.route('**/api/nodes/n-2', route => route.fulfill({ json: { id: 'n-2', key: 'PHAROS-12', title: 'PDF worker image' } }))
}
async function list(page: Page, requests: ReturnType<typeof request>[]) {
  await page.route(PENDING, route => route.fulfill({ json: { requests } }))
}
const strip = (page: Page) => page.getByRole('group', { name: 'Attach requests' })
const fits = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)

test('a waiting attach is listed with computer, harness and expiry, and one click reviews it', async ({ page }) => {
  await setup(page)
  await list(page, [request('r-wait', 'pending')])
  const posts: { url: string; body: unknown }[] = []
  await page.route('**/api/agent-pairing/attach/**', route => {
    if (route.request().method() === 'GET') return route.fallback()
    posts.push({ url: route.request().url(), body: route.request().postDataJSON() })
    return route.fulfill({ json: request('r-wait', 'approved') })
  })
  await page.goto('/agents')
  const row = strip(page).locator('[data-outcome="waiting"]')
  mkdirSync(shots, { recursive: true })
  await expect(row).toContainText('Claude on Markus’s MacBook')
  await page.screenshot({ path: `${shots}/single-waiting.png` })
  await expect(row).toContainText('Wants to watch the conversation')
  await expect(row).toContainText(/Expires in [89]m/)
  expect(posts).toEqual([])
  await row.getByRole('button', { name: 'Review' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('heading', { name: 'Watch a running session' })).toBeVisible()
  await expect(dialog).toContainText('Requested by a process on Markus’s MacBook.')
  await expect(dialog).toContainText('PDF worker image')
  // Reviewing is not approving: nothing was sent, and there is no code to type.
  expect(posts).toEqual([])
  await expect(dialog.getByLabel('Attach code')).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Allow live watch' }).click()
  await expect(dialog).toContainText('Approved. Keep the attach terminal open to share new turns.')
  expect(posts).toHaveLength(1)
  expect(posts[0].url).toMatch(/\/api\/agent-pairing\/attach\/r-wait\/approve$/)
  expect(posts[0].body).toEqual({ request_digest: 'a'.repeat(64), consent_digest: 'b'.repeat(64) })
})

test('a decision refreshes the list instead of waiting for the next poll', async ({ page }) => {
  await setup(page)
  let state: State = 'pending'
  let reads = 0
  await page.route(PENDING, route => { reads++; return route.fulfill({ json: { requests: [request('r-wait', state)] } }) })
  await page.route('**/api/agent-pairing/attach/r-wait/revoke', route => { state = 'detached'; return route.fulfill({ json: request('r-wait', 'detached') }) })
  await page.goto('/agents')
  await strip(page).getByRole('button', { name: 'Review' }).click()
  const before = reads
  await page.getByRole('dialog').getByRole('button', { name: 'Decline' }).click()
  await expect(page.getByRole('dialog')).toContainText('This watch ended.')
  await page.getByRole('button', { name: 'Close attach review' }).click()
  await expect(strip(page).locator('[data-outcome="cancelled"]')).toContainText('Request cancelled')
  expect(reads).toBeGreaterThan(before)
})

test('approved, expired and cancelled requests say so, and ended ones can be dismissed', async ({ page }) => {
  await setup(page)
  await list(page, [
    request('r-strict', 'approved', { consent: 'local_auth' }),
    request('r-old', 'unreachable', { host: 'Linux workstation', harness: 'codex' }),
    request('r-no', 'detached', { mode: 'lease' }),
  ])
  await page.goto('/agents')
  await expect(strip(page).locator('[data-outcome="approved"]')).toContainText('Approved. Confirm with Touch ID on that Mac.')
  const expired = strip(page).locator('[data-outcome="expired"]')
  await expect(expired).toContainText('Codex on Linux workstation')
  await expect(expired).toContainText('Request expired. Start a new attach in the terminal.')
  await expect(strip(page).locator('[data-outcome="cancelled"]')).toContainText('Request cancelled')
  // Nothing waits, so no Review action and no primary button.
  await expect(strip(page).getByRole('button', { name: 'Review' })).toHaveCount(0)
  await expired.getByRole('button', { name: 'Dismiss this attach request' }).click()
  await expect(strip(page).locator('[data-outcome="expired"]')).toHaveCount(0)
  await expect(strip(page).locator('[data-outcome="cancelled"]')).toHaveCount(1)
})

test('a request past its expiry reads as expired before the next poll says so', async ({ page }) => {
  await setup(page)
  await list(page, [request('r-late', 'pending', { mins: 0.15 })])
  await page.goto('/agents')
  await expect(strip(page).locator('[data-outcome="waiting"]')).toContainText(/Expires in \d+s/)
  await expect(strip(page).locator('[data-outcome="expired"]')).toContainText('Request expired', { timeout: 14_000 })
  await expect(strip(page).getByRole('button', { name: 'Review' })).toHaveCount(0)
})

test('an empty list shows nothing, and people who cannot attach never ask', async ({ page }) => {
  await setup(page, { manage: false })
  const asked: string[] = []
  await page.route(PENDING, route => { asked.push(route.request().url()); return route.fulfill({ json: { requests: [request('r-wait', 'pending')] } }) })
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await page.waitForTimeout(700)
  await expect(strip(page)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Attach session' })).toHaveCount(0)
  expect(asked).toEqual([])
})

test('an unavailable list stays quiet', async ({ page }) => {
  await setup(page)
  await page.route(PENDING, route => route.fulfill({ status: 503, json: { error: 'unavailable' } }))
  await page.goto('/agents')
  await expect(page.getByRole('button', { name: 'Attach session' })).toBeVisible()
  await page.waitForTimeout(700)
  await expect(strip(page)).toHaveCount(0)
  await expect(page.getByRole('alert')).toHaveCount(0)
})

test('the terminal link fills the code in, is removed from the address bar, and approves nothing', async ({ page }) => {
  await setup(page)
  const urls: string[] = []
  page.on('request', r => urls.push(r.url()))
  const sent: { url: string; body: unknown }[] = []
  await page.route('**/api/agent-pairing/attach/**', route => {
    if (route.request().method() === 'GET') return route.fallback()
    sent.push({ url: route.request().url(), body: route.request().postDataJSON() })
    return route.fulfill({ json: request('r-link', 'pending') })
  })
  await page.goto('/agents#attach=123456789')
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('heading', { name: 'Attach a running session' })).toBeVisible()
  await expect(dialog.getByLabel('Attach code')).toHaveValue('123 456 789')
  await expect.poll(() => new URL(page.url()).hash).toBe('')
  expect(new URL(page.url()).pathname).toBe('/agents')
  // The code rides in the fragment only: no request carries it, and opening the link sent nothing.
  expect(sent).toEqual([])
  expect(urls.filter(url => url.includes('123456789'))).toEqual([])
  mkdirSync(shots, { recursive: true })
  await page.screenshot({ path: `${shots}/link-prefilled.png` })
  await dialog.getByRole('button', { name: 'Review session' }).click()
  await expect(dialog).toContainText('Requested by a process on Markus’s MacBook.')
  expect(sent).toEqual([{ url: expect.stringMatching(/\/attach\/lookup$/), body: { user_code: '123456789' } }])
  // Still waiting for the person's own click.
  await expect(dialog.getByRole('button', { name: 'Allow live watch' })).toBeEnabled()
  expect(sent).toHaveLength(1)
})

test('a link with anything but nine digits opens nothing', async ({ page }) => {
  await setup(page)
  await page.goto('/agents#attach=12345')
  await expect(page.getByRole('button', { name: 'Attach session' })).toBeVisible()
  await page.waitForTimeout(400)
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

test('a person who cannot attach gets no dialog from the link, and the code leaves the address bar', async ({ page }) => {
  await setup(page, { manage: false })
  await page.goto('/agents#attach=123456789')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await expect.poll(() => new URL(page.url()).hash).toBe('')
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`pending attach layout ${theme} ${width}`, async ({ page }) => {
    await setup(page, { theme })
    await page.setViewportSize({ width, height: 900 })
    await list(page, [
      request('r-wait', 'pending', { mins: 8 }),
      request('r-strict', 'approved', { consent: 'local_auth', host: 'Markus’s MacBook Pro with a very long computer name that must be cut' }),
      request('r-old', 'unreachable', { host: 'Linux workstation', harness: 'codex' }),
      request('r-no', 'detached', { mode: 'lease', harness: 'grok' }),
    ])
    await page.goto('/agents')
    await expect(strip(page).locator('[data-outcome="waiting"]')).toBeVisible()
    await expect(strip(page).locator('[data-outcome="cancelled"]')).toBeVisible()
    expect(await fits(page)).toBe(true)
    expect(await strip(page).evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).include('[aria-label="Attach requests"]').analyze()).violations).toEqual([])
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: `${shots}/pending-${theme}-${width}.png` })
    // Every action is a real, reachable control at this width.
    const review = strip(page).getByRole('button', { name: 'Review' })
    const box = await review.boundingBox()
    expect(box && box.x >= 0 && box.x + box.width <= width).toBe(true)
    await review.click()
    await expect(page.getByRole('dialog')).toContainText('Requested by a process on Markus’s MacBook.')
    expect(await page.getByRole('dialog').evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    await page.screenshot({ path: `${shots}/pending-review-${theme}-${width}.png` })
  })
}
