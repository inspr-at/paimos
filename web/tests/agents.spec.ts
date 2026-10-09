// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentMockOptions, type AgentWorld } from './agents-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

const world: AgentWorld = {
  me: me.id,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}
const session = (n: number) => `5e000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`
const camy = session(1), nova = session(2), kite = session(4)

async function setup(page: Page, options: AgentMockOptions & { empty?: boolean; readOnly?: boolean; member?: boolean; expert?: boolean } = {}) {
  const work = fixtures()
  // AEON-741: manual Start agent is an expert opt-in.
  if (options.expert) work.preferences['developer-ui'] = { show_expert_start: true }
  await mockWork(page, work, { readOnly: options.readOnly, admin: !options.member })
  const data = agentData({ ...world, empty: options.empty })
  const calls = await mockAgents(page, data, options)
  return { data, calls }
}
async function openAgents(page: Page, path = '/agents') {
  await page.goto(path)
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await expect(page.locator('.agents-page .row').first()).toBeVisible()
}
// Account management lives in Settings → Accounts (AEON-299).
async function grantAccounts(page: Page, manage = false) {
  await page.route('**/api/me/permissions*', route => {
    const effective = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    effective.workspace.permissions.push('account.read')
    if (manage) effective.workspace.permissions.push('account.manage')
    return route.fulfill({ json: effective })
  })
}
const queue = (page: Page) => page.getByRole('region', { name: 'Needs you' })
const panel = (page: Page) => page.getByRole('complementary', { name: 'Session details' })
const row = (page: Page, id: string) => page.locator(`[data-row="s:${id}"]`)

test('pi sessions retain their SVG harness icon and provider account label', async ({ page }) => {
  const { data } = await setup(page)
  const pi = data.sessions.find(item => item.harness === 'pi')!
  Object.assign(pi, { account_label: 'pi / anthropic (local profile)' })
  await openAgents(page)
  const item = row(page, pi.id)
  await expect(item).toBeVisible()
  await expect(item.locator('.exec-account')).toContainText('Pi · pi / anthropic (local profile)')
  await expect(item.locator('svg[viewBox="165.29 165.29 469.43 469.43"] path').first()).toBeVisible()
  if (process.env.PI_PAIRING_SHOTS) {
    for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.emulateMedia({ colorScheme: theme })
      await item.scrollIntoViewIfNeeded()
      await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
      await page.screenshot({ path: test.info().outputPath(`pi-agents__${width}__${theme}.png`), fullPage: true })
    }
  }

})

test('the header links to Agents with a count of what needs you', async ({ page }) => {
  await setup(page)
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  const link = page.getByRole('link', { name: 'Agents, 4 need you' })
  await expect(link).toBeVisible()
  await link.click()
  await expect(page).toHaveURL('/agents')
  // Agents is a place: highlighted in the header, so no breadcrumb repeats it.
  await expect(page.getByRole('navigation', { name: 'Places' }).getByRole('link', { name: 'Agents, 4 need you' })).toHaveAttribute('aria-current', 'page')
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toHaveCount(0)
})

test('sessions are grouped by what they need, with ticket and heartbeat; details show account and model', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await openAgents(page)
  // Three buckets in urgency order; each row still names its exact state.
  await expect(page.locator('.group-row')).toHaveText([/Needs attention\s*\d+/, /Live\s*\d+/, /Ended\s*1/])
  const lead = row(page, camy)
  await expect(lead.getByRole('link', { name: /Claude camy, Working/ })).toBeVisible()
  await expect(lead).toContainText('camy')
  await expect(lead).toContainText('Lead')
  await expect(lead.getByRole('link', { name: 'PHAROS-11' })).toHaveAttribute('href', '/p/PHAROS/PHAROS-11')
  await expect(lead.locator('.agent-state-label')).toHaveText('Working')
  await expect(row(page, session(5)).locator('.agent-state-label')).toHaveText('Heartbeat overdue')
  await expect(row(page, session(6)).locator('.agent-state-label')).toHaveText('Working')
  // Stopped sessions fold away until asked for.
  await expect(row(page, session(7))).toHaveCount(0)
  await page.getByRole('button', { name: /^Ended/ }).click()
  await expect(row(page, session(7))).toBeVisible()
  // The head counts are filters (AEON-780); the table groups carry the rest.
  await expect(page.getByRole('group', { name: 'Show sessions by state' }).locator('[data-filter="working"]')).toContainText(/^\d+working$/)
  await lead.locator('.agent-link').click()
  await expect(panel(page)).toContainText('Claude Max')
  await expect(panel(page)).toContainText('claude-fable-high')
  expect(errors).toEqual([])
})

test('session setup and work context appear from the mocked harness API', async ({ page }) => {
  const { data } = await setup(page)
  for (const item of data.sessions) Object.assign(item, { service_tier: 'default', service_tier_revision: 1 })
  await page.route(/\/api\/projects\/[^/]+\/harness-sessions\/[^/]+\/tier$/, route => {
    expect(route.request().method()).toBe('GET')
    return route.fulfill({ json: { session_id: new URL(route.request().url()).pathname.split('/').at(-2), revision: 1, active_tier: 'default', pending: null, read_only: true, read_only_reason: 'This daemon does not support confirmed tier changes.', reports: [], requests: [] } })
  })
  Object.assign(data.sessions[1]!, {
    model: 'gpt-6-sol', reasoning_effort: 'xhigh', account_label: 'Codex Pro', harness_version: '1.2.3',
    brief: 'AEON-213', worktree: '/Code/aeon-worktrees/tm1-session-metadata', branch: 'tm1.session-metadata',
    commits: [{ sha: 'abc1234', subject: 'Store session setup' }, { sha: 'def5678', subject: 'Show work context' }],
  })
  await openAgents(page)
  await expect(row(page, nova).locator('.exec-model')).toHaveText('gpt-6-sol · xhigh')
  await expect(row(page, nova).locator('.exec-account')).toHaveText('Codex · Codex Pro')
  await expect(row(page, nova).locator('.result')).toHaveText('Add an Oracle Cloud connector')
  await row(page, nova).locator('.agent-link').click()
  const setupBlock = panel(page).locator('section[aria-labelledby="setup-title"]')
  const workBlock = panel(page).locator('section[aria-labelledby="work-title"]')
  await expect(setupBlock).toContainText('Codex')
  await expect(setupBlock).toContainText('gpt-6-sol')
  await expect(setupBlock).toContainText('xhigh')
  await expect(setupBlock).toContainText('Codex Pro')
  await expect(setupBlock).toContainText('1.2.3')
  await expect(workBlock).toContainText('AEON-213')
  await expect(workBlock).toContainText('/Code/aeon-worktrees/tm1-session-metadata')
  await expect(workBlock).toContainText('tm1.session-metadata')
  await expect(panel(page).locator('.head-sub').getByRole('link', { name: /PHAROS-12/ })).toHaveAttribute('href', '/p/PHAROS/PHAROS-12')
  await expect(workBlock.locator('.commits li')).toHaveText(['abc1234Store session setup', 'def5678Show work context'])
  await row(page, session(3)).locator('.agent-link').click()
  await expect(panel(page).locator('section[aria-labelledby="work-title"]')).toHaveCount(0)
  await expect(panel(page).locator('.service-tier .tier-head strong')).toHaveText('Default')
  await expect(panel(page)).not.toContainText('Not reported')
  await expect(panel(page)).not.toContainText('Unmocked route')
})

test('an approval from an agent with no session and no address shows its name', async ({ page }) => {
  const { data } = await setup(page)
  const principal = 'a0000000-0000-4000-8000-000000000099'
  data.approvals.unshift({
    id: 'a9000000-0000-4000-8000-000000000099',
    agent_principal_id: principal,
    agent_name: 'Harbor Clerk',
    scope: 'nodes.read',
    resource_kind: 'tenant',
    resource_id: null,
    run_id: null,
    rationale: 'Read the workspace to draft the capture notes.',
    expires_at: new Date(Date.now() + 60 * 60_000).toISOString(),
    proposed_at: new Date().toISOString(),
    decision: null,
    decided_by_principal_id: null,
    risk: 'high',
  })
  await openAgents(page)
  const card = queue(page).locator('.item', { hasText: 'Harbor Clerk' })
  await expect(card).toBeVisible()
  await expect(card).toContainText('Harbor Clerk')
  await expect(card.locator('time.expiry')).toHaveAttribute('datetime', data.approvals[0].expires_at)
  await expect(card.locator('time.expiry')).toContainText('Expires in')
  await expect(card).not.toContainText(`Agent ${principal.slice(0, 8)}`)
  // The asker pill carries an agent icon, centered on the name.
  const pill = card.locator('.who')
  const icon = pill.locator('.who-icon svg')
  await expect(icon).toBeVisible()
  const [p, i] = [await pill.boundingBox(), await icon.boundingBox()]
  expect(Math.abs((p!.y + p!.height / 2) - (i!.y + i!.height / 2))).toBeLessThanOrEqual(1)
  // A resource shows its label once, not label and title twice.
  await expect(card.getByText('the whole workspace')).toHaveCount(1)
})

test('an approval without agent_name falls back to the short agent id', async ({ page }) => {
  const { data } = await setup(page)
  const principal = 'a0000000-0000-4000-8000-000000000098'
  data.approvals.unshift({
    ...data.approvals[0],
    id: 'a9000000-0000-4000-8000-000000000098',
    agent_principal_id: principal,
    agent_name: null,
    scope: 'nodes.read',
    resource_kind: 'tenant',
    resource_id: null,
    run_id: null,
    rationale: 'Read the workspace to draft the capture notes.',
    expires_at: new Date(Date.now() + 60 * 60_000).toISOString(),
    proposed_at: new Date().toISOString(),
    decision: null,
    decided_by_principal_id: null,
    risk: 'high',
  })
  delete (data.approvals[0] as { agent_name?: string | null }).agent_name
  await openAgents(page)
  const card = queue(page).locator('.item', { hasText: `Agent ${principal.slice(0, 8)}` })
  await expect(card).toBeVisible()
  await expect(card).not.toContainText('Harbor Clerk')
})

test('approvals: j and k move, a opens a reason, Enter records the decision', async ({ page }) => {
  const { calls, data } = await setup(page)
  await openAgents(page)
  await page.keyboard.press('j')
  const first = queue(page).locator('.item:not(.signin)').first()
  await expect(first).toHaveClass(/active/)
  await expect(first).toContainText('Interrupt or stop agent sessions')
  await expect(first).toContainText('High risk')
  await expect(queue(page).locator('.item:not(.signin)').nth(2)).toContainText('Low risk')
  await expect(first).toContainText(/Expires in \d+m/)
  // Only the selected request's Approve is the filled primary; the emphasis moves with j and k.
  await expect(first.getByRole('button', { name: 'Approve' })).toHaveClass(/primary/)
  await expect(queue(page).locator('.item:not(.signin)').nth(1).getByRole('button', { name: 'Approve' })).toHaveClass(/approve-soft/)
  await page.keyboard.press('j')
  await expect(queue(page).locator('.item:not(.signin)').nth(1)).toHaveClass(/active/)
  await expect(queue(page).locator('.item:not(.signin)').nth(1).getByRole('button', { name: 'Approve' })).toHaveClass(/primary/)
  await expect(first.getByRole('button', { name: 'Approve' })).not.toHaveClass(/primary/)
  await page.keyboard.press('a')
  const reason = queue(page).getByLabel('Reason (optional)')
  await expect(reason).toBeFocused()
  await reason.fill('Fine for this run.')
  await page.keyboard.press('Enter')
  // The card itself confirms (AEON-505), then folds away.
  await expect(queue(page).locator('.item.settled')).toContainText('Approved·nova may claim a run and start work')
  expect(calls.find(c => c.path.endsWith('/decision'))?.body).toEqual({ decision: 'approved', reason: 'Fine for this run.' })
  expect(data.approvals.find(a => a.scope === 'run.claim' && a.decision === 'approved' && a.agent_principal_id.endsWith('2'))).toBeTruthy()
  await expect(queue(page).locator('.item:not(.signin)')).toHaveCount(3)
  // d opens a denial; Escape backs out without a request.
  await page.keyboard.press('d')
  await expect(queue(page).getByLabel('Why not? The agent sees this.')).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(queue(page).getByLabel('Why not? The agent sees this.')).toHaveCount(0)
  expect(calls.filter(c => c.path.endsWith('/decision'))).toHaveLength(1)
  await queue(page).getByRole('button', { name: /^Decided/ }).click()
  await expect(queue(page).locator('.past')).toHaveCount(5)
})

test('members can decide lower risk requests but not high risk requests', async ({ page }) => {
  const { calls } = await setup(page, { member: true })
  await openAgents(page)
  const items = queue(page).locator('.item:not(.signin)')
  await expect(items.first()).toContainText('High risk')
  await expect(items.first().getByRole('button', { name: 'Approve' })).toHaveCount(0)
  await items.first().focus()
  await page.keyboard.press('a')
  await expect(items.first().getByLabel('Reason (optional)')).toHaveCount(0)
  await expect(items.nth(1).getByRole('button', { name: 'Approve' })).toBeVisible()
  await items.nth(1).getByRole('button', { name: 'Approve' }).click()
  await items.nth(1).getByRole('button', { name: 'Approve permission' }).click()
  await expect.poll(() => calls.filter(c => c.path.endsWith('/decision')).length).toBe(1)
  await expect(items.nth(1)).toContainText('Low risk')
  await items.nth(1).getByRole('button', { name: 'Approve' }).click()
  await items.nth(1).getByRole('button', { name: 'Approve permission' }).click()
  await expect.poll(() => calls.filter(c => c.path.endsWith('/decision')).length).toBe(2)
})

test('approval controls require the action permission even with an admin legacy role', async ({ page }) => {
  await setup(page)
  await page.route('**/api/me/permissions*', route => {
    const effective = mockEffectivePermissions('admin')
    effective.workspace.permissions = effective.workspace.permissions.filter(permission => permission !== 'harness.control')
    return route.fulfill({ json: effective })
  })
  await openAgents(page)
  const high = queue(page).locator('.item:not(.signin)').first()
  await expect(high).toContainText('High risk')
  await expect(high.getByRole('button', { name: 'Approve' })).toHaveCount(0)
  await high.focus()
  await page.keyboard.press('a')
  await expect(high.getByLabel('Reason (optional)')).toHaveCount(0)
})

test('a failed decision keeps the reason and says why', async ({ page }) => {
  await setup(page, { failDecision: true })
  await openAgents(page)
  const item = queue(page).locator('.item:not(.signin)').first()
  await item.getByRole('button', { name: 'Deny' }).click()
  await queue(page).getByLabel('Why not? The agent sees this.').fill('Use the staging account instead.')
  await item.getByRole('button', { name: 'Deny permission' }).click()
  await expect(item.getByRole('alert')).toHaveText('The request expired while you were deciding')
  await expect(queue(page).getByLabel('Why not? The agent sees this.')).toHaveValue('Use the staging account instead.')
})

test('read-only people see requests but cannot decide or control', async ({ page }) => {
  await setup(page, { readOnly: true })
  await openAgents(page)
  await expect(queue(page).locator('.item:not(.signin)')).toHaveCount(4)
  await expect(queue(page).getByRole('button', { name: 'Approve' })).toHaveCount(0)
  // Controls that cannot work are not offered at all: no row button, no disabled item, no excuse (AEON-291).
  await expect(row(page, camy).getByRole('button', { name: 'Interrupt camy' })).toHaveCount(0)
  await row(page, camy).hover()
  await row(page, camy).getByRole('button', { name: 'Actions for camy' }).click()
  await expect(page.getByRole('menuitem', { name: /Interrupt|Stop now|Pause…|Resume/ })).toHaveCount(0)
  await expect(page.locator('[role="menuitem"][aria-disabled="true"]')).toHaveCount(0)
  await expect(page.getByRole('menu')).not.toContainText('Only people who may write')
})

test('the session panel shows the ticket, runs, telemetry and the thread, and sends messages', async ({ page }) => {
  const { calls, data } = await setup(page)
  for (const m of data.messages.filter(m => m.project === 'p-pharos')) Object.assign(m, m.sender_principal_id === data.me ? { recipient_session_id: camy } : { sender_session_id: camy, sender_label: 'camy' })
  let releaseReceipt!: () => void, receiptStarted!: () => void
  const receiptHeld = new Promise<void>(resolve => { releaseReceipt = resolve })
  const receiptRequested = new Promise<void>(resolve => { receiptStarted = resolve })
  await page.route('**/api/inbox/message-status?*', async route => {
    const ids = new URL(route.request().url()).searchParams.get('ids')!.split(',')
    const sent = data.sent.find(message => ids.includes(message.id))
    if (!sent) return route.fallback()
    receiptStarted(); await receiptHeld
    // Historical posts already reached this thread. A new receipt must not
    // reclassify them as queued while it confirms the current send.
    return route.fulfill({ json: { items: ids.map(id => ({ message_id: id, status: 'delivered', delivered_at: id === sent.id ? new Date().toISOString() : data.messages.find(message => message.id === id)!.created_at, read_at: null, deliver_by: null })) } })
  })
  await openAgents(page, `/agents/${camy}`)
  const details = panel(page)
  await expect(details.getByRole('heading', { name: /camy/ })).toBeVisible()
  await expect(details).toContainText('lead session · owned by AEON')
  await expect(details.locator('.head-sub .ticket-chip')).toHaveText('PHAROS-11')
  await expect(details.getByRole('link', { name: /PHAROS-11 Connect/ })).toHaveAttribute('href', '/p/PHAROS/PHAROS-11')
  await expect(details).toContainText('Claude Max')
  await expect(details.locator('.metric')).toHaveText([/Running/, /184k/, /22k/, /\$3\.84/])
  await expect(details.locator('.run-row .run-chip')).toHaveText(['Running', 'Completed', 'Failed'])
  await expect(details.locator('.callout')).toHaveCount(0) // Principal-only approval stays in Needs you, not on this session.
  await details.getByRole('tab', { name: /Chat/ }).click()
  await expect(details.locator('.msg')).toHaveCount(4)
  await expect(details.locator('.msg.mine')).toHaveCount(2)
  await expect(details.locator('.msg').last()).toContainText('Awaiting reply')
  // Messages show when they were sent: relative, with the exact time on hover.
  await expect(details.locator('.msg .msg-time')).toHaveText(['52m ago', '47m ago', '31m ago', '6m ago'])
  await expect(details.locator('.msg .msg-time').first()).toHaveAttribute('data-tip', /\d{2}:\d{2}/)
  await details.getByRole('button', { name: 'Reply' }).last().click()
  await expect(details.locator('.replying')).toContainText('Counts are in')
  const field = details.getByRole('textbox', { name: 'Message to camy' })
  await field.fill('Sort stale hosts last.')
  // The current composer uses a direct send action; the delivery payload stays exact.
  await expect(details.locator('.steer-send')).toBeEnabled()
  const mac = await page.evaluate(() => /Mac|iPhone|iPad|iPod/.test(navigator.platform))
  await field.press(mac ? 'Meta+Enter' : 'Control+Enter')
  await receiptRequested
  await expect(details.getByRole('list', { name: 'Queued messages', exact: true })).toContainText('Sort stale hosts last.')
  await expect(details.locator('.msg')).toHaveCount(4)
  releaseReceipt()
  await expect(details.locator('.msg')).toHaveCount(5)
  await expect(details.locator('.msg').last()).toContainText('Sort stale hosts last.')
  await expect(details.getByRole('list', { name: 'Queued messages', exact: true })).toHaveCount(0)
  const sent = calls.find(c => c.method === 'POST' && c.path.endsWith('/messages'))?.body as Record<string, unknown>
  expect(sent).toMatchObject({ to: 'claude:camy', recipient_session_id: camy, body: 'Sort stale hosts last.', delivery_level: 'steer', expects_reply: false, is_action_request: false, reply_to: '3e000000-0000-4000-8000-000000000004' })
  expect(typeof sent.idempotency_key).toBe('string')
  // B7: runs by agent, messages newest first in one page, sessions tenant-wide.
  expect(calls.some(c => c.path === '/api/runs' && c.query?.get('agent') === 'a0000000-0000-4000-8000-000000000001')).toBe(true)
  expect(calls.some(c => c.path === '/api/projects/p-pharos/messages' && c.method === 'GET' && c.query?.get('session') === camy && c.query?.get('newest_first') === 'true' && c.query?.get('limit') === '200')).toBe(true)
  expect(calls.some(c => /\/api\/projects\/[^/]+\/harness-sessions$/.test(c.path))).toBe(false)
})

test('interrupt goes straight out, Stop now asks first, and outside sessions have no managed controls', async ({ page }) => {
  const { data, calls } = await setup(page)
  data.sessions.find(s => s.id === camy)!.display_label = 'camy'
  await openAgents(page)
  await row(page, nova).getByRole('button', { name: 'Actions for nova' }).click()
  await page.getByRole('menuitem', { name: /^Interrupt this step/ }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Interrupt sent to nova.' })).toBeVisible()
  expect(calls.some(c => c.method === 'POST' && c.path.endsWith(`/harness-sessions/${nova}/controls/interrupt`))).toBe(true)
  await expect(page.locator('.toast').filter({ hasText: 'nova applied the interrupt.' })).toBeVisible({ timeout: 8000 })
  // Stop asks first, so it sits in the row's overflow menu.
  await expect(row(page, camy).getByRole('button', { name: 'Stop now…', exact: true })).toHaveCount(0)
  await row(page, camy).getByRole('button', { name: 'Actions for camy' }).click()
  await page.getByRole('menuitem', { name: 'Stop now…', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Stop now camy', exact: true })
  await expect(dialog).toBeVisible()
  await dialog.getByRole('button', { name: /^Cancel/ }).click()
  expect(calls.some(c => c.method === 'POST' && (c.path.endsWith('/pause') || c.path.endsWith('/controls/stop')))).toBe(false)
  await row(page, camy).getByRole('button', { name: 'Actions for camy' }).click()
  await page.getByRole('menuitem', { name: 'Stop now…', exact: true }).click()
  await dialog.getByRole('button', { name: /^Stop now/ }).click()
  await expect.poll(() => calls.filter(c => c.method === 'POST' && c.path.endsWith(`/harness-sessions/${camy}/pause`))).toHaveLength(1)
  expect(calls.find(c => c.path.endsWith(`/harness-sessions/${camy}/pause`))?.body).toEqual({ level: 'stop_now', note: '' })
  expect(calls.some(c => c.path.endsWith('/controls/stop'))).toBe(false)
  await expect(row(page, session(5)).getByRole('button', { name: 'Interrupt amy' })).toHaveCount(0)
  await row(page, session(5)).hover()
  await row(page, session(5)).getByRole('button', { name: 'Actions for amy' }).click()
  // Outside AEON: no managed interrupt; cooperative Pause/Stop use the durable API.
  await expect(page.getByRole('menuitem', { name: /^Interrupt/ })).toHaveCount(0)
  await expect(page.getByRole('menuitem', { name: 'Pause…', exact: true })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: 'Stop now…', exact: true })).toBeVisible()
  await expect(page.getByRole('menu')).toContainText('Runs outside AEON — stop it in its terminal')
})

test('Enter opens a session, j and k move the panel along, Escape closes it', async ({ page }) => {
  await setup(page)
  await openAgents(page)
  // A click anywhere on the row that is not a link or button opens the session.
  // Rows follow urgency, then start time: kite, which needs you and started first, sits right above nova.
  await row(page, kite).locator('.c-state').click()
  await expect(page).toHaveURL(`/agents/${kite}`)
  await expect(panel(page).getByRole('heading', { name: /kite/ })).toBeVisible()
  await panel(page).focus()
  await page.keyboard.press('j')
  await expect(page).toHaveURL(`/agents/${nova}`)
  await expect(panel(page).getByRole('heading', { name: /nova/ })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL('/agents')
  await expect(panel(page)).toHaveCount(0)
  await expect(row(page, nova)).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(`/agents/${nova}`)
})

test('closing a session panel goes back instead of adding history, like the ticket panel', async ({ page }) => {
  await setup(page)
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await page.getByRole('link', { name: /^Agents/ }).click()
  await expect(page.locator('.agents-page .row').first()).toBeVisible()
  await row(page, camy).locator('.c-state').click()
  await expect(page).toHaveURL(`/agents/${camy}`)
  await row(page, nova).locator('.c-state').click()
  await expect(page).toHaveURL(`/agents/${nova}`)
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL('/agents')
  await expect(row(page, nova)).toBeFocused()
  await page.goBack()
  await expect(page).toHaveURL('/')
})

test('a held action request opens the asking agent’s conversation', async ({ page }) => {
  await setup(page)
  await openAgents(page)
  const held = queue(page).locator('.item.held')
  await expect(held).toContainText('Held for you')
  await expect(held).toContainText('Please merge the release fix once CI is green')
  await expect(held).toContainText('9 min ago')
  await held.getByRole('button', { name: 'Answer' }).click()
  await expect(page).toHaveURL(`/agents/${kite}`)
})

test('held action requests resolve or dismiss with an optional note, by button or keyboard', async ({ page }) => {
  const { calls, data } = await setup(page)
  await openAgents(page)
  const held = queue(page).locator('.item.held')
  // j reaches the held request after the three permission requests; a resolves, d dismisses.
  for (let i = 0; i < 4; i++) await page.keyboard.press('j')
  await expect(held).toHaveClass(/active/)
  await expect(held.getByRole('button', { name: 'Resolve' })).toHaveClass(/primary/)
  await page.keyboard.press('d')
  const note = held.getByLabel('Note (optional)')
  await expect(note).toBeFocused()
  await expect(held).toContainText('The held message is not delivered.')
  await page.keyboard.press('Escape')
  await expect(note).toHaveCount(0)
  await held.getByRole('button', { name: 'Resolve' }).click()
  await held.getByLabel('Note (optional)').fill('Merged it myself after CI.')
  await held.getByRole('button', { name: 'Mark resolved' }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Resolved: the request from kite is answered.' })).toBeVisible()
  await expect(queue(page).locator('.item.held')).toHaveCount(0)
  const resolution = calls.find(c => c.path.endsWith('/resolution'))!
  expect(resolution.path).toBe(`/api/projects/p-frozen/messages/${data.messages[4].id}/resolution`)
  expect(resolution.body).toEqual({ decision: 'resolved', note: 'Merged it myself after CI.' })
  await expect(page.getByRole('link', { name: 'Agents, 3 need you' })).toBeVisible()
})

test('Settings lists limits set by hand as Set by you, never as a vendor percent; admins can drain and resume', async ({ page }) => {
  const { calls } = await setup(page)
  await grantAccounts(page, true)
  await page.goto('/settings/accounts')
  // AEON-686: each account's logins and switches live in its docked panel.
  const accounts = page.locator('#agent-accounts')
  const panel = page.locator('section.pane')
  await accounts.locator('.list-row').filter({ hasText: 'Claude Max' }).click()
  const claude = panel.locator('.account').filter({ hasText: 'Claude Max' })
  await expect(claude.locator('.chip.mine')).toHaveText('Set by you')
  await expect(claude).toContainText('Team')
  await claude.getByRole('button', { name: 'Details for Claude Max' }).click()
  await expect(claude.locator('ul.mine .what')).toContainText('5,000,000 tokens')
  await expect(claude.locator('ul.mine')).not.toContainText('%')
  await expect(claude.getByRole('meter')).toHaveCount(0)
  await panel.getByRole('button', { name: 'Close details' }).click()
  await accounts.locator('.list-row').filter({ hasText: 'Codex Pro' }).click()
  const codex = panel.locator('.account').filter({ hasText: 'Codex Pro' })
  await codex.getByRole('switch', { name: /Agents may use it/ }).click()
  await page.getByRole('dialog', { name: 'Drain Codex Pro?' }).getByRole('button', { name: 'Drain account' }).click()
  await expect(codex.locator('.state')).toHaveText('Paused in Settings')
  expect(calls.find(c => c.method === 'PATCH')?.body).toEqual({ state: 'draining' })
})

test('accounts explain themselves when the person may not see them', async ({ page }) => {
  await setup(page, { accountsForbidden: true })
  await grantAccounts(page)
  await page.goto('/settings/accounts')
  await expect(page.locator('#agent-accounts')).toContainText('visible to workspace admins')
})

test('an empty workspace explains how an agent connects', async ({ page }) => {
  await setup(page, { empty: true, expert: true })
  await page.route('**/api/me/permissions*', route => {
    const effective = mockEffectivePermissions('admin')
    effective.workspace.permissions.push('run.create')
    return route.fulfill({ json: effective })
  })
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'No agent has connected yet' })).toBeVisible()
  await expect(page.locator('.connect .lead')).toHaveText('Start an agent to queue a run; its daemon connects when an account is ready.')
  // No request waits (a failed check is not a sign-in prompt).
  await expect(queue(page).locator('.item:not(.signin)')).toHaveCount(0)
  // The empty state says it once; the head only shows faint zero counts.
  await expect(page.getByRole('group', { name: 'Show sessions by state' }).locator('.count:not(.zero)')).toHaveCount(0)
  await page.locator('.connect').getByRole('button', { name: 'Start agent' }).click()
  await expect(page.getByRole('dialog', { name: 'Start agent' }).getByRole('button', { name: 'Queue run' })).toBeVisible()
})

test('a failing sessions read is a real error with a retry, and approvals keep working', async ({ page }) => {
  await setup(page, { sessionsMissing: true, messagesMissing: true })
  await page.goto('/agents')
  const failure = page.getByRole('region', { name: 'Sessions' }).getByRole('alert')
  await expect(failure).toContainText('Sessions could not be loaded')
  await expect(failure).toContainText('(404)')
  await expect(failure.getByRole('button', { name: 'Try again' })).toBeVisible()
  await expect(queue(page).locator('.item:not(.signin)')).toHaveCount(3)
})

test('the ticket panel shows the bound agent’s attention state and links to its session', async ({ page }) => {
  await setup(page)
  await page.goto('/p/PHAROS/PHAROS-11')
  const details = page.getByRole('complementary', { name: 'Ticket details' })
  // Its principal has an unbound approval; the ticket and Agents keep this session working.
  const chip = details.getByRole('link', { name: 'Claude camy: Working. Open the session' })
  await expect(chip).toBeVisible()
  await chip.click()
  await expect(page).toHaveURL(`/agents/${camy}`)
  await expect(panel(page).getByRole('heading', { name: /camy/ })).toBeVisible()
})

test('old approvals, runs and pacing addresses land on Agents', async ({ page }) => {
  await setup(page)
  for (const path of ['/approvals', '/runs', '/pacing']) {
    await page.goto(path)
    await expect(page).toHaveURL('/agents')
  }
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`on a phone the overview fits and a session opens full screen in ${colorScheme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme })
    await setup(page)
    await openAgents(page)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth && document.querySelector('main')!.scrollWidth <= innerWidth)).toBe(true)
    for (const item of await queue(page).locator('.item:not(.signin)').all()) {
      const box = (await item.boundingBox())!
      expect(box.x + box.width).toBeLessThanOrEqual(390)
    }
    await expect(page.getByRole('link', { name: 'Agents, 4 need you' })).toBeVisible()
    // Row controls live in an overflow menu on phones.
    const { calls } = { calls: [] as string[] }
    page.on('request', request => { if (request.method() === 'POST') calls.push(new URL(request.url()).pathname) })
    await row(page, nova).evaluate(el => el.scrollIntoView({ block: 'center' }))
    await page.getByRole('button', { name: 'Actions for nova' }).click()
    await page.getByRole('menuitem', { name: /^Interrupt/ }).click()
    await expect.poll(() => calls.some(path => path.endsWith(`/harness-sessions/${nova}/controls/interrupt`))).toBe(true)
    await row(page, camy).locator('.c-agent a').click()
    expect(await panel(page).boundingBox()).toEqual({ x: 0, y: 0, width: 390, height: 844 })
    expect(errors).toEqual([])
  })
}

for (const colorScheme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`SC2 ticket detail uses the available width at ${width} in ${colorScheme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme })
    await setup(page)
    await openAgents(page)
    await row(page, camy).getByRole('link', { name: 'PHAROS-11', exact: true }).click()
    const ticket = page.getByRole('complementary', { name: 'Ticket details' })
    await expect(ticket).toBeVisible()
    const box = (await ticket.boundingBox())!
    if (width === 1600) {
      const list = (await page.locator('.main-col').boundingBox())!
      expect(list.x + list.width).toBeLessThanOrEqual(box.x)
      expect(box.x - list.x - list.width).toBeLessThan(80)
    } else expect(box.width).toBe(390)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await expect(page.locator('.accounts-disclosure .accounts')).not.toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`sc2-${colorScheme}-${width}-ticket.png`), fullPage: true })
  })
}
