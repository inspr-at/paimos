// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page, type Locator } from '@playwright/test'
import { openAttachSession } from './agents-menu-fixtures'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'

// AEON-440: a started attach is easy to find. /agents lists the owner's waiting
// requests (no code), the terminal's link only fills the code in, and expiry or
// cancellation is shown instead of silence.
const shots = process.env.ATTACH_DISCOVERABLE_SHOTS ?? '/private/tmp/aeon-440-shots'
// The page runs on this clock and every expiry is relative to it, so no assertion depends on the wall time.
const NOW = Date.parse('2026-09-30T12:00:00Z')
const PENDING = '**/api/agent-pairing/attach/pending'

type State = 'pending' | 'approved' | 'detached' | 'unreachable'
function request(id: string, state: State, options: { mins?: number; host?: string; harness?: string; mode?: 'lease'; consent?: 'aeon' | 'local_auth' } = {}) {
  return {
    request_id: id, request_digest: 'a'.repeat(64), consent_digest: 'b'.repeat(64), consent_mode: options.consent ?? 'aeon', state,
    expires_at: new Date(NOW + (options.mins ?? 9) * 60_000).toISOString(),
    snapshot: {
      ...(options.mode ? { mode: options.mode } : {}), platform: 'darwin', computer_id: 'computer-fixture', project_id: 'p-pharos', ticket_id: 'n-2', host: options.host ?? 'Markus’s MacBook', harness: options.harness ?? 'claude',
      transcript: options.mode ? '' : '/Users/markus/.claude/projects/pharos/session.jsonl', file_id: options.mode ? '' : '1:234567',
      process: { pid: 4812, uid: 501, started: '2026-09-30T00:12:30Z', executable: '/Users/markus/.local/bin/claude', cwd: '/Users/markus/Code/pharos' },
    },
  }
}

async function setup(page: Page, options: { theme?: 'light' | 'dark'; manage?: boolean; paused?: boolean } = {}) {
  await page.clock.install({ time: NOW })
  // Freeze before navigation creates polling timers. A slow CI page load must
  // not move the intended pause point into the past; requests expire in 9 min.
  if (options.paused) await page.clock.pauseAt(NOW + 60_000)
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
  await page.route('**/api/me/leaving-at', route => route.fulfill({ json: { deadline_at: null, request_id: null, hosts: 'all', stop_in_flight: false, owner_principal_id: me.id, items: [] } }))
  await page.route('**/api/nodes/p-pharos', route => route.fulfill({ json: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' } }))
  await page.route('**/api/nodes/n-2', route => route.fulfill({ json: { id: 'n-2', key: 'PHAROS-12', title: 'PDF worker image' } }))
  return data
}
async function list(page: Page, requests: ReturnType<typeof request>[]) {
  await page.route(PENDING, route => route.fulfill({ json: { requests } }))
}
const strip = (page: Page) => page.locator('.queue:has(.attach-item)')
async function chooseAndDecide(dialog: Locator, choice: 'Allow' | 'Decline') {
  await dialog.getByRole('radio', { name: choice, exact: true }).check()
  await dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ }).click()
}
const fits = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)

for (const theme of ['light', 'dark'] as const) for (const width of [390, 1024, 1440]) {
  test(`New menu preserves authorized attach and header controls ${theme} ${width}`, async ({ page }) => {
    await setup(page, { theme })
    await list(page, [])
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/agents')
    const add = page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true })
    await expectStableControls({
      controls: { add, more: page.getByRole('button', { name: 'More agent actions' }) },
      scrollAreas: { header: page.locator('.agents-page .page-head') },
      interactions: [
        { name: 'open authorized New menu', run: async () => {
          await add.click()
          await expect(page.getByRole('menuitem', { name: /Attach a running session/ })).toBeVisible()
          await expect(page.getByRole('menuitem', { name: /Connect your machine/ })).toBeVisible()
          mkdirSync(shots, { recursive: true })
          await page.screenshot({ path: `${shots}/new-menu-${width}-${theme}.png` })
        } },
        { name: 'close New menu', run: async () => { await page.keyboard.press('Escape'); await expect(page.getByRole('menu')).toHaveCount(0) } },
      ],
    })
  })
}

for (const theme of ['light', 'dark'] as const) for (const width of [1440, 390]) {
  test(`attach controls never move from code to memo or settled state ${theme} ${width}`, async ({ page }) => {
    await setup(page, { theme, paused: true })
    await page.setViewportSize({ width, height: 900 })
    let state: State = 'pending'
    const posts: string[] = []
    await page.route('**/api/agent-pairing/attach/**', route => {
      if (route.request().method() === 'GET') return route.fallback()
      posts.push(route.request().url())
      if (route.request().url().endsWith('/approve')) state = 'approved'
      return route.fulfill({ json: request('guard', state, { consent: 'local_auth' }) })
    })
    await list(page, [request('guard', 'pending', { consent: 'local_auth' })])
    await page.goto('/agents#attach=123456789')
    const dialog = page.getByRole('dialog')
    const actions = dialog.locator(width === 390 ? '.at-bottom .desk-btn' : '.at-top .desk-btn')
    const positions = async () => Promise.all([actions.nth(0).boundingBox(), actions.nth(1).boundingBox(), dialog.getByRole('button', { name: 'Close attach review' }).boundingBox()])
    await expect(dialog.getByLabel('Attach code')).toHaveValue('123 456 789')
    const initial = await positions()
    expect(posts).toEqual([])
    await dialog.getByLabel('Attach code').fill('123456789')
    expect(await positions()).toEqual(initial)
    await dialog.getByRole('button', { name: /Find request/ }).click()
    await expect(dialog).toContainText('PDF worker image')
    expect(await positions()).toEqual(initial)
    await expect(dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeDisabled()
    await page.clock.resume()
    expect((await new AxeBuilder({ page }).include('dialog').analyze()).violations).toEqual([])
    for (const choice of ['Allow', 'Decline', 'Allow']) {
      await dialog.getByRole('radio', { name: choice, exact: true }).check()
      expect(await positions()).toEqual(initial)
      expect(posts).toHaveLength(1)
    }
    await dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ }).click()
    await expect(dialog).toContainText('Waiting for confirmation on Markus’s MacBook.')
    expect(await positions()).toEqual(initial)
    expect(posts).toHaveLength(2)
    expect(await fits(page)).toBe(true)
    expect(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).include('dialog').analyze()).violations).toEqual([])
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: `${shots}/memo-guard-${theme}-${width}.png` })
  })
}

for (const platform of ['MacIntel', 'Win32']) {
  test(`attach keyboard keeps browser shortcuts native and requires explicit lookup on ${platform}`, async ({ page }) => {
    await page.addInitScript(platform => Object.defineProperty(navigator, 'platform', { value: platform }), platform)
    await setup(page, { paused: true })
    await list(page, [])
    const posts: string[] = []
    await page.route('**/api/agent-pairing/attach/**', route => {
      if (route.request().method() === 'GET') return route.fallback()
      posts.push(route.request().url())
      return route.fulfill({ json: request('keys', route.request().url().endsWith('/approve') ? 'approved' : 'pending') })
    })
    await page.goto('/agents#attach=123456789')
    const dialog = page.getByRole('dialog'), input = dialog.getByLabel('Attach code')
    await expect(input).toBeFocused()
    expect(posts).toEqual([])
    await input.press('Enter')
    expect(posts).toEqual([])
    const native = await input.evaluate(el => ['s', 'r', 'd', 'p', 'a'].map(key => {
      const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, metaKey: true })
      el.dispatchEvent(event); return event.defaultPrevented
    }))
    expect(native).toEqual([false, false, false, false, false])
    const controlSave = await input.evaluate(el => { const event = new KeyboardEvent('keydown', { key: 's', bubbles: true, cancelable: true, ctrlKey: true }); el.dispatchEvent(event); return event.defaultPrevented })
    expect(controlSave).toBe(false)
    const shiftedEscape = await input.evaluate(el => { const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true, shiftKey: true }); el.dispatchEvent(event); return event.defaultPrevented })
    expect(shiftedEscape).toBe(false)
    await expect(input).toBeFocused()
    await input.press(platform === 'MacIntel' ? 'Meta+Enter' : 'Control+Enter')
    await expect(dialog).toContainText('PDF worker image')
    expect(posts).toHaveLength(1)
    await dialog.locator('.paper').focus()
    await page.keyboard.press('1')
    await expect(dialog.getByRole('radio', { name: 'Allow', exact: true })).toBeChecked()
    expect(posts).toHaveLength(1)
    await page.keyboard.press('Enter')
    await expect(dialog).toContainText('Approved. Keep the attach terminal open')
    expect(posts).toHaveLength(2)
    await dialog.getByRole('button', { name: 'Close attach review' }).click()
    await openAttachSession(page)
    await expect(input).toBeFocused()
    await input.press('Escape')
    await expect(dialog).toBeVisible()
    await expect(input).not.toBeFocused()
    await page.keyboard.press('Escape')
    await expect(dialog).not.toBeVisible()
  })
}

test('a reviewed request expires in place and cannot send an approval', async ({ page }) => {
  await setup(page, { paused: true })
  await list(page, [request('expires', 'pending', { mins: 2 })])
  const posts: string[] = []
  await page.route('**/api/agent-pairing/attach/**', route => { if (route.request().method() === 'GET') return route.fallback(); posts.push(route.request().url()); return route.fulfill({ json: request('expires', 'approved') }) })
  await page.goto('/agents')
  await strip(page).getByRole('button', { name: 'Review' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('radio', { name: 'Allow', exact: true }).check()
  await expect(dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeEnabled()
  await page.clock.fastForward(120_000)
  await expect(dialog).toContainText('Request expired. Run aeon-agentd attach again.')
  await expect(dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeDisabled()
  await dialog.locator('.paper').focus()
  await page.keyboard.press('1')
  await page.keyboard.press('Enter')
  expect(posts).toEqual([])
})

test('attach approvals share the queue count and expiry ordering, never a code or inline Allow', async ({ page }) => {
  const data = await setup(page, { paused: true })
  data.approvals.push({ id: 'permission', agent_name: 'worker', resource_id: 'p-pharos', run_id: null, decision: null, decided_by_principal_id: null, agent_principal_id: data.sessions[0]!.agent_principal_id, scope: 'nodes.read', resource_kind: 'node', rationale: 'Read this project', expires_at: new Date(NOW + 4 * 60_000).toISOString(), proposed_at: new Date(NOW).toISOString(), risk: 'low' })
  await list(page, [request('later', 'pending', { mins: 6 }), request('sooner', 'pending', { mins: 2, mode: 'lease' })])
  await page.goto('/agents')
  const queue = strip(page)
  await expect(queue.locator('.count-badge')).toHaveText('3')
  const rows = queue.locator('.items > li')
  await expect(rows).toHaveCount(3)
  expect(await rows.evaluateAll(rows => rows.map(row => row.getAttribute('data-row')))).toEqual(['t:sooner', 'a:permission', 't:later'])
  await expect(queue.locator('.attach-item').getByRole('button', { name: /Allow/ })).toHaveCount(0)
  await expect(queue).not.toContainText('123456789')
  await expect(queue.locator('.attach-item').first()).toContainText('Status only')
  await page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true }).click()
  await expect(page.getByRole('menuitem', { name: /Attach a running session/ }).locator('.count-badge')).toHaveCount(0)
  await page.keyboard.press('Escape')
})

test('round navigation and choices keep controls fixed, Skip sends no decision', async ({ page }) => {
  await setup(page, { paused: true })
  await list(page, [request('one', 'pending', { mins: 4 }), request('two', 'pending', { mins: 8, host: 'Another Mac with a much longer host name', mode: 'lease' })])
  const posts: string[] = []
  await page.route('**/api/agent-pairing/attach/**', route => { if (route.request().method() === 'GET') return route.fallback(); posts.push(route.request().url()); return route.fulfill({ json: request('one', 'approved') }) })
  await page.goto('/agents')
  await strip(page).getByRole('button', { name: 'Review' }).first().click()
  const dialog = page.getByRole('dialog')
  const controls = dialog.locator('.desk-top button')
  const positions = async () => controls.evaluateAll(items => items.map(item => { const { x, y, width, height } = item.getBoundingClientRect(); return { x, y, width, height } }))
  const before = await positions()
  await dialog.getByRole('button', { name: 'Next request', exact: true }).click()
  await expect(dialog).toContainText('Another Mac with a much longer host name')
  expect(await positions()).toEqual(before)
  await dialog.getByRole('radio', { name: 'Allow', exact: true }).check()
  expect(await positions()).toEqual(before)
  await dialog.getByRole('button', { name: 'Previous request', exact: true }).click()
  expect(await positions()).toEqual(before)
  await dialog.locator('.paper').focus()
  await page.keyboard.press('s')
  await expect(dialog).toContainText('Another Mac with a much longer host name')
  expect(await positions()).toEqual(before)
  expect(posts).toEqual([])
})

test('a declined request settles then folds into Decided', async ({ page }) => {
  await setup(page, { paused: true })
  let state: State = 'pending'
  await page.route(PENDING, route => route.fulfill({ json: { requests: [request('fold', state)] } }))
  await page.route('**/api/agent-pairing/attach/fold/revoke', route => { state = 'detached'; return route.fulfill({ json: request('fold', state) }) })
  await page.goto('/agents')
  await strip(page).getByRole('button', { name: 'Review' }).click()
  await chooseAndDecide(page.getByRole('dialog'), 'Decline')
  await expect(strip(page).locator('[data-outcome="declined"]')).toContainText('You declined it')
  await page.getByRole('button', { name: 'Close attach review' }).click()
  await page.clock.fastForward(7_000)
  await expect(page.locator('.attach-item')).toHaveCount(0)
  await page.getByRole('button', { name: 'Decided', exact: false }).click()
  await expect(page.locator('.attach-past')).toContainText('Declined')
})

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
  await expect(row).toContainText('and share its conversation')
  await expect(row).toContainText('PHAROS-12')
  await expect(row).toContainText('Your terminal on Markus’s MacBook waits')
  await expect(row.getByRole('button', { name: /Allow/ })).toHaveCount(0)
  await expect(row).toContainText(/Expires in [89]m/)
  expect(posts).toEqual([])
  await row.getByRole('button', { name: 'Review' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('heading', { name: 'Watch a running session' })).toBeVisible()
  await expect(dialog).toContainText('Requested by a process on Markus’s MacBook.')
  await expect(dialog).toContainText('Pairing your computer does not link a session.')
  await expect(dialog).toContainText('PDF worker image')
  // Reviewing is not approving: nothing was sent, and there is no code to type.
  expect(posts).toEqual([])
  await expect(dialog.getByLabel('Attach code')).toHaveCount(0)
  await chooseAndDecide(dialog, 'Allow')
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
  await chooseAndDecide(page.getByRole('dialog'), 'Decline')
  await expect(page.getByRole('dialog')).toContainText('You declined it. Nothing was shared.')
  await page.getByRole('button', { name: 'Close attach review' }).click()
  await expect(strip(page).locator('[data-outcome="declined"]')).toContainText('You declined it')
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
  await expect(strip(page).locator('[data-outcome="approved"]')).toContainText('Confirm with Touch ID on Markus’s MacBook.')
  const expired = strip(page).locator('[data-outcome="expired"]')
  await expect(expired).toContainText('Codex on Linux workstation')
  await expect(expired).toContainText('Request expired. Run aeon-agentd attach again.')
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
  await page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true }).click()
  await expect(page.getByRole('menuitem', { name: /Attach a running session/ })).toHaveCount(0)
  await page.keyboard.press('Escape')
  expect(asked).toEqual([])
})

test('an unavailable list stays quiet', async ({ page }) => {
  await setup(page)
  await page.route(PENDING, route => route.fulfill({ status: 503, json: { error: 'unavailable' } }))
  await page.goto('/agents')
  await expect(page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true })).toBeVisible()
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
  await expect(dialog.getByRole('heading', { name: 'Enter the code from your terminal' })).toBeVisible()
  await expect(dialog.getByLabel('Attach code')).toHaveValue('123 456 789')
  await expect.poll(() => new URL(page.url()).hash).toBe('')
  expect(new URL(page.url()).pathname).toBe('/agents')
  // The code rides in the fragment only: no request carries it, and opening the link sent nothing.
  expect(sent).toEqual([])
  expect(urls.filter(url => url.includes('123456789'))).toEqual([])
  mkdirSync(shots, { recursive: true })
  await page.screenshot({ path: `${shots}/link-prefilled.png` })
  await dialog.getByRole('button', { name: /Find request/ }).click()
  await expect(dialog).toContainText('Requested by a process on Markus’s MacBook.')
  expect(sent).toEqual([{ url: expect.stringMatching(/\/attach\/lookup$/), body: { user_code: '123456789' } }])
  // Still waiting for the person's own click.
  await dialog.getByRole('radio', { name: 'Allow', exact: true }).check()
  await expect(dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeEnabled()
  expect(sent).toHaveLength(1)
})

test('a link with anything but nine digits opens nothing', async ({ page }) => {
  await setup(page)
  await page.goto('/agents#attach=12345')
  await expect(page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true })).toBeVisible()
  await page.waitForTimeout(400)
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

test('German pairing guidance still only prefills the approval code', async ({ browser }) => {
  const page = await browser.newPage({ locale: 'de-AT' })
  try {
    await setup(page)
    const sent: string[] = []
    await page.route('**/api/agent-pairing/attach/**', route => {
      if (route.request().method() === 'GET') return route.fallback()
      sent.push(route.request().url())
      return route.fulfill({ json: request('r-de', 'pending') })
    })
    await page.goto('/agents#attach=123456789')
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Enter the code from your terminal' })).toBeVisible()
    await expect(dialog).toContainText('Verknüpfe jede laufende Sitzung separat mit ihrem Ticket.')
    await expect(dialog.getByLabel('Verknüpfungscode')).toHaveValue('123 456 789')
    await expect.poll(() => new URL(page.url()).hash).toBe('')
    expect(sent).toEqual([])
    await dialog.getByRole('button', { name: /Find request/ }).click()
    await expect(dialog).toContainText('Computer gekoppelt · Diese Sitzung ist noch nicht verknüpft')
    expect(sent).toHaveLength(1)
  } finally { await page.close() }
})

test('a person who cannot attach gets no dialog from the link, and the code leaves the address bar', async ({ page }) => {
  await setup(page, { manage: false })
  await page.goto('/agents#attach=123456789')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await expect.poll(() => new URL(page.url()).hash).toBe('')
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

const OLA = { principal: { id: '33333333-3333-4333-8333-333333333333', name: 'Ola Nordmann', kind: 'person', roles: ['admin'] }, tenant: { id: 't2', name: 'Other Studio' } }
const gate = () => { let open!: () => void; const passed = new Promise<void>(resolve => { open = resolve }); return { passed, open } }

test('a list still on its way when the person changes is never shown to the next person', async ({ page }) => {
  await setup(page)
  const previous = gate(), next = gate(), permissions = gate()
  let switched = false, olaAsked = 0, anyAsked = 0, permissionsAsked = false
  // Whoever asks before Ola signs in gets the previous person's list, held until released.
  await page.route(PENDING, async route => {
    anyAsked++
    const forOla = switched
    if (forOla) olaAsked++
    await (forOla ? next : previous).passed
    await route.fulfill({ json: { requests: [forOla ? request('r-next', 'pending', { host: 'Ola’s Mac' }) : request('r-previous', 'pending', { host: 'Previous person’s Mac' })] } }).catch(() => undefined)
  })
  await page.goto('/agents')
  await expect.poll(() => anyAsked).toBeGreaterThanOrEqual(1)
  // Ola signs in to another workspace; the next navigation refreshes the session. Her
  // permissions are slow, so the old answer lands while nobody is allowed yet, and the
  // new list is slow too: the old rows must not show in between.
  switched = true
  await page.route('**/api/me', route => route.fulfill({ json: OLA }))
  await page.route('**/api/me/permissions*', async route => {
    permissionsAsked = true
    await permissions.passed
    const value = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    value.workspace.permissions = [...value.workspace.permissions, 'account.manage']
    await route.fulfill({ json: value })
  })
  await page.locator('[data-row^="s:"] .c-state').first().click()
  await expect(page).toHaveURL(/\/agents\/.+/)
  await expect.poll(() => permissionsAsked).toBe(true)
  previous.open()
  await page.waitForTimeout(300)
  permissions.open()
  await expect.poll(() => olaAsked).toBeGreaterThanOrEqual(1)
  await page.waitForTimeout(300)
  await expect(page.getByText('Previous person’s Mac')).toHaveCount(0)
  await expect(strip(page)).toHaveCount(0)
  next.open()
  await expect(strip(page).locator('[data-outcome="waiting"]')).toContainText('Ola’s Mac')
  await expect(page.getByText('Previous person’s Mac')).toHaveCount(0)
})

test('the attach code never reaches a sign-in address, even when the session has ended', async ({ page }) => {
  await setup(page)
  const urls: string[] = []
  page.on('request', r => urls.push(r.url()))
  await page.goto('/agents')
  await expect(page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true })).toBeVisible()
  await page.route('**/api/me', route => route.fulfill({ status: 401, json: { error: 'unauthorized' } }))
  await page.evaluate(() => { location.hash = '#attach=123456789' })
  await expect(page).toHaveURL(/\/signin\?error=expired&return=(\/|%2F)agents$/)
  expect(page.url()).not.toMatch(/123456789|attach/)
  // A reload sends this address to the server; it carries no code.
  await page.reload()
  expect(urls.filter(url => /123456789|attach=/.test(url))).toEqual([])
  expect(await page.evaluate(() => `${sessionStorage.getItem('aeon.signInReturn') ?? ''}${localStorage.length}`)).not.toMatch(/123456789/)
})

// Ola signs in to another workspace in this tab. The address gets a harmless fragment:
// the guard that runs on every navigation refreshes the session, so she is the identity
// from here on while anything asked for the previous person may still be on its way.
async function signInAsOla(page: Page) {
  await page.route('**/api/me', route => route.fulfill({ json: OLA }))
  await page.evaluate(() => { location.hash = '#switch' })
}
const ADMIN_PERMISSIONS = (project?: string) => {
  const value = mockEffectivePermissions('admin', project)
  value.workspace.permissions = [...value.workspace.permissions, 'account.manage']
  return value
}

test('a reset queued between the scope check and continuation cannot open A’s code for B', async ({ page }) => {
  await setup(page)
  // Instrument the real scope at precisely the r3 gap: after permissions, the
  // last nextTick check queues an identity switch before the caller can open.
  // No production hook or altered component is needed.
  await page.route('**/src/lib/identityScope.ts*', async route => {
    const response = await route.fetch()
    const source = await response.text()
    const checked = /if \(!live\(\)\) throw new StaleScopeError\(\);?/g
    expect(source).toMatch(checked)
    await route.fulfill({ response, body: source.replace(checked, '$&\nglobalThis.__attachPermissionProbe?.(value);') })
  })
  await page.goto('/agents')
  await expect(page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true })).toBeVisible()
  await page.evaluate(async ola => {
    // @ts-expect-error Vite serves this module in the browser execution context.
    const { useSession } = await import('/src/stores/session.ts')
    const session = useSession()
    const state = window as typeof window & { __attachPermissionProbe?: (value: unknown) => void; __attachOpenedFor?: string[] }
    state.__attachOpenedFor = []
    const original = HTMLDialogElement.prototype.showModal
    HTMLDialogElement.prototype.showModal = function () { state.__attachOpenedFor!.push(session.identity?.principal.id ?? ''); original.call(this) }
    let permissionsChecked = false
    state.__attachPermissionProbe = value => {
      if (value === 'known') permissionsChecked = true
      else if (permissionsChecked && value === undefined) {
        delete state.__attachPermissionProbe
        queueMicrotask(() => { session.identity = ola })
      }
    }
  }, OLA)
  await page.evaluate(() => { location.hash = '#attach=123456789' })
  await expect.poll(() => new URL(page.url()).hash).toBe('')
  await expect.poll(() => page.evaluate(() => '__attachPermissionProbe' in window)).toBe(false)
  const opened = await page.evaluate(() => (window as typeof window & { __attachOpenedFor: string[] }).__attachOpenedFor)
  expect(opened).not.toContain(OLA.principal.id)
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByLabel('Attach code')).toHaveValue('')
})

test('signing in as another person in a second tab invalidates the old tab before accepting lists', async ({ page, context }) => {
  await setup(page)
  let active = false, reads = 0
  await page.route(PENDING, route => { reads++; return route.fulfill({ json: { requests: [request(active ? 'r-ola' : 'r-markus', 'pending', { host: active ? 'Ola’s Mac' : 'Markus’s old Mac' })] } }) })
  await page.goto('/agents')
  await expect(strip(page)).toContainText('Markus’s old Mac')
  await openAttachSession(page)
  await page.getByLabel('Attach code').fill('123456789')
  // Two independent browser execution contexts share this origin's cookies and
  // storage, as real tabs do. Separate BrowserContexts would isolate both.
  const other = await context.newPage()
  await setup(other)
  await other.route('**/api/me', route => active ? route.fulfill({ json: OLA }) : route.fulfill({ status: 401, json: { dev_mode: true } }))
  await other.route('**/api/auth/dev-login', route => { active = true; return route.fulfill({ status: 204 }) })
  await page.route('**/api/me', route => route.fulfill({ json: OLA }))
  await list(other, [request('r-ola', 'pending', { host: 'Ola’s Mac' })])
  await other.goto('/signin')
  await other.getByLabel('Email address').fill('ola@example.test')
  await other.getByRole('button', { name: 'Continue with email' }).click()
  await other.goto('/agents')
  await expect(strip(other)).toContainText('Ola’s Mac')
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(strip(page)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true })).toHaveCount(0)
  await expect(page.getByText('Your session has ended.', { exact: false })).toBeVisible()
  const stopped = reads
  await page.clock.fastForward(15_000)
  expect(reads).toBe(stopped)
  await expect(page.getByText('Ola’s Mac')).toHaveCount(0)
  // Only a nonce is shared: the code, identity and auth cookie are never stored.
  const stored = await page.evaluate(() => localStorage.getItem('aeon.auth.generation'))
  expect(stored).toMatch(/^[0-9a-f-]{36}$/)
  expect(stored).not.toContain('123456789')
  await other.close()
})

test('signing out and back in as A in a second tab never revives A’s old link continuation', async ({ page, context }) => {
  await setup(page)
  const slow = gate()
  let asked = 0
  await page.route('**/api/me/permissions*', async route => { asked++; await slow.passed; await route.fulfill({ json: ADMIN_PERMISSIONS() }).catch(() => undefined) })
  await page.goto('/agents#attach=123456789')
  await expect.poll(() => asked).toBeGreaterThan(0)
  const other = await context.newPage()
  await setup(other)
  await other.route('**/api/auth/logout', route => route.fulfill({ status: 204 }))
  await other.route('**/api/auth/dev-login', route => route.fulfill({ status: 204 }))
  await other.goto('/agents')
  await expect(other.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true })).toBeVisible()
  await other.evaluate(async () => {
    // @ts-expect-error Vite serves this module in the browser execution context.
    const { useSession } = await import('/src/stores/session.ts')
    const session = useSession()
    await session.signOut()
    session.devMode = true
    await session.devLogin('markus@barta.com')
    await session.refresh()
  })
  slow.open()
  await expect(page.getByText('Your session has ended.', { exact: false })).toBeVisible()
  await page.clock.fastForward(10_000)
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true })).toHaveCount(0)
  await other.close()
})

test('an OIDC round trip in another tab invalidates the old review and records the returned session', async ({ page, context }) => {
  await setup(page)
  await page.goto('/agents#attach=123456789')
  await expect(page.getByLabel('Attach code')).toHaveValue('123 456 789')
  const other = await context.newPage()
  await setup(other)
  let returned = false
  await other.route('**/api/me', route => returned ? route.fulfill({ json: OLA }) : route.fulfill({ status: 401, json: { dev_mode: false } }))
  await other.route('**/api/auth/login', route => { returned = true; return route.fulfill({ status: 302, headers: { location: '/agents' } }) })
  await list(other, [request('r-ola', 'pending', { host: 'Ola’s Mac' })])
  await other.goto('/signin')
  await other.getByRole('link', { name: 'Sign in', exact: true }).click()
  await expect(strip(other)).toContainText('Ola’s Mac')
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true })).toHaveCount(0)
  expect(await other.evaluate(() => sessionStorage.getItem('aeon.auth.pending'))).toBeNull()
  await other.close()
})

// Every async path of the attach flow answers to the person who started it (AEON-440).
test('a link code waiting for slow permissions never opens for the next person', async ({ page }) => {
  await setup(page)
  const slow = gate()
  let switched = false, before = 0
  await page.route('**/api/me/permissions*', async route => {
    if (!switched) { before++; await slow.passed }
    await route.fulfill({ json: ADMIN_PERMISSIONS(new URL(route.request().url()).searchParams.get('project_id') ?? undefined) }).catch(() => undefined)
  })
  await page.goto('/agents#attach=123456789')
  await expect.poll(() => before).toBeGreaterThanOrEqual(1)
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await expect.poll(() => new URL(page.url()).hash).toBe('')
  await page.waitForTimeout(300)
  await expect(page.getByRole('dialog')).toHaveCount(0)
  // Ola's permissions answer at once; the previous person's are still on their way.
  switched = true
  await signInAsOla(page)
  await expect(page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true })).toBeVisible()
  slow.open()
  await page.waitForTimeout(400)
  // The code was the previous person's: it opens nothing for Ola.
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByLabel('Attach code')).toHaveValue('')
  // Her own link still works.
  await page.evaluate(() => { location.hash = '#attach=987654321' })
  await expect(page.getByLabel('Attach code')).toHaveValue('987 654 321')
})

test('names still loading for the previous person never enable approval for the next one', async ({ page }) => {
  await setup(page)
  const first = gate(), second = gate()
  let switched = false
  for (const id of ['p-pharos', 'n-2']) await page.route(`**/api/nodes/${id}`, async route => { await (switched ? second : first).passed; await route.fallback() })
  await list(page, [request('r-wait', 'pending')])
  await page.goto('/agents')
  const dialog = page.getByRole('dialog')
  await strip(page).getByRole('button', { name: 'Review' }).click()
  await expect(dialog.getByText('Loading…')).toHaveCount(2)
  switched = true
  await signInAsOla(page)
  await expect(dialog).toHaveCount(0)
  // Ola's list carries the same kind of request; she opens it and her own names are still on their way.
  await strip(page).getByRole('button', { name: 'Review' }).click()
  await expect(dialog.getByText('Loading…')).toHaveCount(2)
  first.open()
  await page.waitForTimeout(400)
  // The previous person's names land late: they fill nothing in for her, and approval stays off.
  await expect(dialog.getByText('Loading…')).toHaveCount(2)
  await dialog.getByRole('radio', { name: 'Allow', exact: true }).check()
  await expect(dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeDisabled()
  second.open()
  await expect(dialog).toContainText('PDF worker image')
  await dialog.getByRole('radio', { name: 'Allow', exact: true }).check()
  await expect(dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeEnabled()
})

test('a decision still on its way when the person changes shows nothing to the next person', async ({ page }) => {
  await setup(page)
  const answer = gate()
  let approvals = 0
  await page.route('**/api/agent-pairing/attach/**', async route => {
    if (route.request().method() === 'GET') return route.fallback()
    approvals++
    await answer.passed
    await route.fulfill({ json: request('r-wait', 'approved') }).catch(() => undefined)
  })
  await list(page, [request('r-wait', 'pending')])
  await page.goto('/agents')
  await strip(page).getByRole('button', { name: 'Review' }).click()
  await chooseAndDecide(page.getByRole('dialog'), 'Allow')
  await expect.poll(() => approvals).toBe(1)
  await signInAsOla(page)
  await expect(page.getByRole('dialog')).toHaveCount(0)
  answer.open()
  await page.waitForTimeout(400)
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByText('Approved. Keep the attach terminal open')).toHaveCount(0)
})

// Keep native Response.json() decoding a live body after the 2xx status arrived.
// Like a fetch body, this stream errors if the POST's signal aborts. The previous
// json() promise stub ignored that signal and hid the close/decoding race.
async function holdDecisionBody(page: Page, action: 'approve' | 'revoke') {
  await page.evaluate(action => {
    const original = window.fetch.bind(window)
    const state = window as typeof window & { __attachBody?: HeldDecisionBody }
    window.fetch = async (...args) => {
      const response = await original(...args)
      if (!String(args[0]).endsWith(`/r-wait/${action}`)) return response
      const bytes = new Uint8Array(await response.arrayBuffer())
      const signal = args[1]?.signal
      const held: HeldDecisionBody = { decoding: false, aborted: false, finish: () => {} }
      const body = new ReadableStream<Uint8Array>({
        start(controller) {
          const abort = () => { held.aborted = true; controller.error(signal?.reason) }
          signal?.addEventListener('abort', abort, { once: true })
          held.finish = () => {
            if (held.aborted) return
            signal?.removeEventListener('abort', abort)
            controller.enqueue(bytes); controller.close()
          }
          if (signal?.aborted) abort()
        },
      })
      const delayed = new Response(body, { status: response.status, statusText: response.statusText, headers: response.headers })
      const json = delayed.json.bind(delayed)
      delayed.json = () => { held.decoding = true; return json() }
      state.__attachBody = held
      return delayed
    }
  }, action)
}
interface HeldDecisionBody { decoding: boolean; aborted: boolean; finish: () => void }
const decisionBody = (page: Page) => page.evaluate(() => {
  const held = (window as typeof window & { __attachBody?: HeldDecisionBody }).__attachBody
  return { decoding: held?.decoding ?? false, aborted: held?.aborted ?? false }
})
const finishDecisionBody = (page: Page) => page.evaluate(() => (window as typeof window & { __attachBody: HeldDecisionBody }).__attachBody.finish())

for (const action of ['approve', 'revoke'] as const) {
  test(`an accepted ${action} refreshes pending requests and sessions after its review closes during body decoding`, async ({ page }) => {
    const data = await setup(page, { paused: true })
    let state: State = 'pending', pendingReads = 0, sessionReads = 0
    await page.route(PENDING, route => { pendingReads++; return route.fulfill({ json: { requests: [request('r-wait', state)] } }) })
    await page.route('**/api/harness-sessions?*', route => { sessionReads++; return route.fallback() })
    await page.route(`**/api/agent-pairing/attach/r-wait/${action}`, route => {
      state = action === 'approve' ? 'approved' : 'detached'
      Object.assign(data.sessions[0], { host: 'Accepted attach session', revision: 3 })
      return route.fulfill({ json: request('r-wait', state) })
    })
    await page.goto('/agents')
    const session = page.locator(`[data-row="s:${data.sessions[0].id}"]`)
    await expect(session).toContainText('imac0')
    await strip(page).getByRole('button', { name: 'Review' }).click()
    await page.getByRole('dialog').getByRole('radio', { name: 'Allow', exact: true }).check()
    await expect(page.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeEnabled()
    // The clock has not advanced since startup: only the accepted write can refresh.
    await holdDecisionBody(page, action)
    await chooseAndDecide(page.getByRole('dialog'), action === 'approve' ? 'Allow' : 'Decline')
    await expect.poll(() => decisionBody(page)).toEqual({ decoding: true, aborted: false })
    await page.getByRole('button', { name: 'Close attach review' }).click()
    expect(await decisionBody(page)).toEqual({ decoding: true, aborted: false })
    const before = { pendingReads, sessionReads }
    await finishDecisionBody(page)
    await expect.poll(() => pendingReads).toBeGreaterThan(before.pendingReads)
    // afterWrite batches session reads in a zero-delay timer, scheduled once the
    // native body decoder finishes. Flush it without reaching a polling tick.
    await page.clock.runFor(1)
    await expect.poll(() => sessionReads).toBeGreaterThan(before.sessionReads)
    await expect(session).toContainText('Accepted attach session')
    await expect(strip(page).locator(`[data-outcome="${action === 'approve' ? 'approved' : 'declined'}"]`)).toBeVisible()
    await expect(page.getByRole('dialog')).toHaveCount(0)
    await openAttachSession(page)
    await expect(page.getByLabel('Attach code')).toHaveValue('')
    await expect(page.getByRole('dialog')).not.toContainText('Approved. Keep the attach terminal open')
  })

  test(`an accepted ${action} body is aborted and dropped when the identity changes during decoding`, async ({ page }) => {
    await setup(page, { paused: true })
    await list(page, [request('r-wait', 'pending')])
    await page.route(`**/api/agent-pairing/attach/r-wait/${action}`, route => route.fulfill({ json: request('r-wait', action === 'approve' ? 'approved' : 'detached') }))
    await page.goto('/agents')
    await strip(page).getByRole('button', { name: 'Review' }).click()
    await page.getByRole('dialog').getByRole('radio', { name: 'Allow', exact: true }).check()
    await expect(page.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeEnabled()
    await holdDecisionBody(page, action)
    await chooseAndDecide(page.getByRole('dialog'), action === 'approve' ? 'Allow' : 'Decline')
    await expect.poll(() => decisionBody(page)).toEqual({ decoding: true, aborted: false })
    await list(page, [request('r-next', 'pending', { host: 'Ola’s Mac' })])
    await signInAsOla(page)
    await expect.poll(() => decisionBody(page)).toEqual({ decoding: true, aborted: true })
    await expect(page.getByRole('dialog')).toHaveCount(0)
    await expect(strip(page)).toContainText('Ola’s Mac')
    let rereads = 0
    await page.route('**/api/harness-sessions?*', route => { rereads++; return route.fallback() })
    await page.route(PENDING, route => { rereads++; return route.fallback() })
    await finishDecisionBody(page)
    await page.clock.runFor(1)
    await page.waitForTimeout(200)
    expect(rereads).toBe(0)
    await expect(strip(page)).toContainText('Ola’s Mac')
    await expect(strip(page).locator('[data-outcome="approved"], [data-outcome="cancelled"]')).toHaveCount(0)
    await expect(page.getByRole('dialog')).toHaveCount(0)
    await expect(page.getByRole('alert')).toHaveCount(0)
  })
}

test('a link followed inside the open Agents page fills the code in again', async ({ page }) => {
  await setup(page)
  await page.goto('/agents')
  await expect(page.getByRole('button', { name: 'New: start an agent, attach a session or connect a machine', exact: true })).toBeVisible()
  for (const code of ['123456789', '987654321']) {
    await page.evaluate(hash => { location.hash = hash }, `#attach=${code}`)
    await expect(page.getByLabel('Attach code')).toHaveValue(`${code.slice(0, 3)} ${code.slice(3, 6)} ${code.slice(6)}`)
    await expect.poll(() => new URL(page.url()).hash).toBe('')
    await page.getByRole('button', { name: 'Close attach review' }).click()
  }
})

for (const entry of ['the list', 'a typed code'] as const) {
  test(`approval waits for the project and ticket names (${entry})`, async ({ page }) => {
    await setup(page)
    const names = gate()
    for (const id of ['p-pharos', 'n-2']) await page.route(`**/api/nodes/${id}`, async route => { await names.passed; await route.fallback() })
    const posts: string[] = []
    await page.route('**/api/agent-pairing/attach/**', route => {
      if (route.request().method() === 'GET') return route.fallback()
      posts.push(route.request().url())
      return route.fulfill({ json: route.request().url().endsWith('/lookup') ? request('r-wait', 'pending') : request('r-wait', 'approved') })
    })
    await list(page, [request('r-wait', 'pending')])
    await page.goto('/agents')
    const dialog = page.getByRole('dialog')
    if (entry === 'the list') await strip(page).getByRole('button', { name: 'Review' }).click()
    else {
      await openAttachSession(page)
      await dialog.getByLabel('Attach code').fill('123456789')
      await dialog.getByRole('button', { name: /Find request/ }).click()
    }
    await expect(dialog).toContainText('Requested by a process on Markus’s MacBook.')
    // Still reading: names are pending, so the button is off and nothing can be sent.
    await expect(dialog.getByText('Loading…')).toHaveCount(2)
    await dialog.getByRole('radio', { name: 'Allow', exact: true }).check()
    const allow = dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })
    await expect(allow).toBeDisabled()
    await expect(dialog.getByRole('radio', { name: 'Decline', exact: true })).toBeEnabled()
    await allow.click({ force: true })
    expect(posts.filter(url => url.endsWith('/approve'))).toEqual([])
    names.open()
    await expect(dialog).toContainText('PDF worker image')
    await expect(allow).toBeEnabled()
    await allow.click()
    await expect(dialog).toContainText('Approved. Keep the attach terminal open to share new turns.')
    // Deciding does not take the names away.
    await expect(dialog).toContainText('PDF worker image')
    await expect(dialog).toContainText('Pharos')
    await expect(dialog.getByText('Loading…')).toHaveCount(0)
  })
}

test('names that cannot be read fall back to the ids and approval is possible', async ({ page }) => {
  await setup(page)
  await page.route('**/api/nodes/p-pharos', route => route.fulfill({ status: 404, json: { error: 'not_found' } }))
  await page.route('**/api/nodes/n-2', route => route.fulfill({ status: 404, json: { error: 'not_found' } }))
  await list(page, [request('r-wait', 'pending')])
  await page.goto('/agents')
  await strip(page).getByRole('button', { name: 'Review' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('radio', { name: 'Allow', exact: true }).check()
  await expect(dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeEnabled()
  await expect(dialog.locator('dd[title="p-pharos"]')).toHaveText('p-pharos')
  await expect(dialog.locator('dd[title="n-2"]')).toHaveText('n-2')
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
    expect((await new AxeBuilder({ page }).include('.queue').analyze()).violations).toEqual([])
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
