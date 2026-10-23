// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, assistantEvidence, mockAgents, mockAssistants, type AgentMockOptions, type AgentWorld } from './agents-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import { controlStability } from './control-stability'
import AxeBuilder from '@axe-core/playwright'

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
  return { data, calls, work }
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
async function openDesk(page: Page, id?: string) {
  await page.goto(id ? `/decision-desk?item=${encodeURIComponent(id)}` : '/decision-desk')
  await expect(page.getByRole('heading', { name: 'Decision Desk', exact: true })).toBeVisible()
  if (id) await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
}
const panel = (page: Page) => page.getByRole('complementary', { name: 'Session details' })
const row = (page: Page, id: string) => page.locator(`[data-row="s:${id}"]`)

test('pi sessions retain their SVG harness icon and provider account label', async ({ page }) => {
  const { data } = await setup(page)
  const pi = data.sessions.find(item => item.harness === 'pi')!
  Object.assign(pi, { account_label: 'pi / anthropic (local profile)' })
  await openAgents(page)
  const item = row(page, pi.id)
  await expect(item).toBeVisible()
  // Execution's second line is the account (AEON-1211); the harness is named with it in the cell's name.
  await expect(item.locator('.exec-account')).toHaveText('pi / anthropic (local profile)')
  await expect(item.locator('.c-exec')).toHaveAttribute('aria-label', /pi \/ anthropic \(local profile\)\. Pi$/)
  // AEON-1211: the avatar carries the state badge only; a compact row opens with the harness's own mark.
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(item.locator('.op-mark svg[viewBox="165.29 165.29 469.43 469.43"] path').first()).toBeVisible()
  await expect(item.locator('.op-mark')).toHaveAttribute('aria-label', /Pi · pi \/ anthropic \(local profile\)$/)
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

test('the header routes the canonical count to Decision Desk and keeps Agents separate', async ({ page }) => {
  await setup(page); await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  const places = page.getByRole('navigation', { name: 'Places' })
  await expect(places.getByRole('link', { name: 'Agents', exact: true })).toBeVisible()
  await places.getByRole('link', { name: 'Decision Desk, 4 open', exact: true }).click()
  await expect(page).toHaveURL('/decision-desk')
  await expect(places.getByRole('link', { name: 'Decision Desk, 4 open', exact: true })).toHaveAttribute('aria-current', 'page')
})

test('sessions are grouped by project, the lead heading its group, with ticket and heartbeat; details show account and model', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await openAgents(page)
  // AEON-1210: each project is one group in a fixed order, the lead row heading it; Ended folds away at the end.
  // Each row still names its exact state.
  await expect(page.locator('.sessions .grp')).toHaveCount(4)
  expect(await page.locator('.sessions .grp').evaluateAll(list => list.map(el => el.getAttribute('aria-label')))).toEqual(['AEON · Aeon', 'PHAROS · Pharos', 'PRJ-26 · Studio infrastructure', 'Ended'])
  await expect(page.locator('.sessions .grp').nth(1).locator('[role="row"]').first()).toHaveAttribute('data-row', `s:${camy}`)
  await expect(page.getByRole('button', { name: /^Ended\s*1/ })).toBeVisible()
  const lead = row(page, camy)
  await expect(lead.getByRole('link', { name: /Claude Code camy, Working/ })).toBeVisible()
  await expect(lead.locator('.result')).toHaveAttribute('title', /camy$/)
  // A coordinator without the project's lead claim heads its group but says no Lead (AEON-1285).
  await expect(lead.locator('.role')).toHaveCount(0)
  await expect(lead.locator('.key-inline')).toHaveText('PHAROS-11 ·')
  // AEON-1211: the state lives in the avatar (its badge, name and tooltip); a precise word opens line 2.
  await expect(lead.locator('.ava')).toHaveAttribute('aria-label', /: Working$/)
  await expect(row(page, session(5)).locator('.ava')).toHaveAttribute('data-tip', /^Awaiting heartbeat · Heartbeat overdue · /)
  await expect(row(page, session(5)).locator('.l2t')).toContainText(/^Heartbeat overdue/)
  await expect(row(page, session(6)).locator('.ava')).toHaveAttribute('aria-label', /: Working$/)
  // Stopped sessions fold away until asked for.
  await expect(row(page, session(7))).toHaveCount(0)
  await page.getByRole('button', { name: /^Ended/ }).click()
  await expect(row(page, session(7))).toBeVisible()
  // The head counts are filters (AEON-780); the table groups carry the rest.
  await expect(page.getByRole('group', { name: 'Show sessions by state' }).locator('[data-filter="working"]')).toContainText(/^\d+working$/)
  await lead.locator('.agent-link').click()
  await expect(panel(page)).toContainText('Claude Max')
  await expect(panel(page)).toContainText('Fable · high')
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
  await expect(row(page, nova).locator('.exec-model')).toHaveText('GPT-6 Sol · xhigh')
  await expect(row(page, nova).locator('.exec-account')).toHaveText('Codex Pro')
  await expect(row(page, nova).locator('.result')).toHaveText('Add an Oracle Cloud connector')
  await row(page, nova).locator('.agent-link').click()
  const setupBlock = panel(page).locator('section[aria-labelledby="setup-title"]')
  const workBlock = panel(page).locator('section[aria-labelledby="work-title"]')
  await expect(setupBlock).toContainText('Codex')
  await expect(setupBlock).toContainText('GPT-6 Sol · xhigh')
  await expect(setupBlock).toContainText('xhigh')
  await expect(setupBlock).toContainText('Codex Pro')
  await expect(setupBlock).toContainText('1.2.3')
  await expect(workBlock).toContainText('AEON-213')
  await expect(workBlock).toContainText('/Code/aeon-worktrees/tm1-session-metadata')
  await expect(workBlock).toContainText('tm1.session-metadata')
  await expect(setupBlock.locator('.fact').filter({ has: page.locator('dt', { hasText: /^Ticket$/ }) }).getByRole('link', { name: /PHAROS-12/ })).toHaveAttribute('href', '/p/PHAROS/PHAROS-12')
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
  await openDesk(page, `a:${data.approvals[0].id}`)
  const facts = page.getByRole('region', { name: 'Permission request', exact: true })
  // AEON-1339: the requester and expiry are in the memo head; What it runs shows only what runs.
  await expect(page.getByTestId('desk-from')).toHaveText('Harbor Clerk')
  await expect(page.getByTestId('desk-line')).toContainText(/Harbor Clerk waits for this\. Expires in \d+ min/)
  await expect(page.getByRole('dialog')).not.toContainText(`Agent ${principal.slice(0, 8)}`)
  await expect(facts.getByText('Across the whole workspace', { exact: true })).toHaveCount(1)
  await page.goto(`/decision-desk?assistant=${principal}`)
  await expect(page.getByTestId('desk-scope')).toContainText('Showing items from this assistant.')
  await expect(page.locator('.desk-row')).toHaveCount(1)
  await expect(page.getByTestId(`desk-row-a:${data.approvals[0].id}`)).toBeVisible()
  await page.getByTestId('desk-scope').getByRole('link', { name: 'Show all', exact: true }).click()
  await expect(page).toHaveURL('/decision-desk')
  await expect.poll(() => page.locator('.desk-row').count()).toBeGreaterThan(1)
  await page.goto('/decision-desk?assistant=invalid')
  await expect(page.locator('.desk-row')).toHaveCount(0)
  await expect(page.getByTestId('desk-scope')).toContainText('Invalid assistant filter.')
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
  await openDesk(page, `a:${data.approvals[0].id}`)
  await expect(page.getByTestId('desk-from')).toHaveText(`Agent ${principal.slice(0, 8)}`)
  await expect(page.getByRole('dialog')).not.toContainText('Harbor Clerk')
})

test('native approval choices and field shortcuts record the exact source and retain history', async ({ page }) => {
  const { calls, data } = await setup(page)
  const approval = data.approvals.find(a => a.scope === 'run.claim' && !a.decision)!
  await openDesk(page, `a:${approval.id}`)
  await page.getByTestId('choice-0').click()
  const reason = page.getByRole('textbox', { name: 'Reason', exact: true })
  await reason.fill('Fine for this run.'); await reason.press('Enter')
  expect(calls.filter(c => c.path.endsWith('/decision'))).toHaveLength(0)
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-announcement')).toContainText('Decision recorded')
  expect(calls.find(c => c.path.endsWith('/decision'))).toMatchObject({ path: `/api/approvals/${approval.id}/decision`, body: { decision: 'approved', reason: 'Fine for this run.' } })
  expect(approval.decision).toBe('approved')
  await page.getByTestId('desk-close').click()
  await page.getByRole('radio', { name: /^Decided/ }).click()
  await expect(page.getByTestId(`desk-row-a:${approval.id}`)).toBeVisible()
})

test('members can decide lower risk requests but not high risk requests', async ({ page }) => {
  const { calls, data } = await setup(page, { member: true })
  const high = data.approvals.find(a => a.scope === 'harness.control' && !a.decision)!
  await openDesk(page, `a:${high.id}`)
  // AEON-1339: risk is said once, in the memo's head line.
  await expect(page.getByTestId('desk-line')).toContainText('High risk.')
  await expect(page.getByTestId('desk-decide')).toBeDisabled()
  for (const [index, scope] of ['run.claim', 'nodes.read'].entries()) {
    const lower = data.approvals.find(a => a.scope === scope && !a.decision)!
    await openDesk(page, `a:${lower.id}`)
    await page.getByTestId('choice-0').click(); await page.getByTestId('desk-decide').click()
    await expect(page.getByTestId('desk-announcement')).toContainText('Decision recorded')
    expect(calls.filter(c => c.path.endsWith('/decision'))).toHaveLength(index + 1)
    expect(calls.find(c => c.path === `/api/approvals/${lower.id}/decision`)?.body).toMatchObject({ decision: 'approved' })
    expect(lower.decision).toBe('approved')
  }
  expect(high.decision).toBeNull()
})

test('approval controls require the action permission even with an admin legacy role', async ({ page }) => {
  const { calls, data } = await setup(page)
  await page.route('**/api/me/permissions*', route => {
    const effective = mockEffectivePermissions('admin')
    effective.workspace.permissions = effective.workspace.permissions.filter(permission => permission !== 'harness.control')
    return route.fulfill({ json: effective })
  })
  const high = data.approvals.find(a => a.scope === 'harness.control' && !a.decision)!
  await openDesk(page, `a:${high.id}`)
  await expect(page.getByTestId('desk-decide')).toBeDisabled()
  await page.getByTestId('desk-paper').press('Enter')
  expect(calls.filter(c => c.method === 'POST')).toHaveLength(0)
})

test('a failed decision keeps the reason and says why', async ({ page }) => {
  const { data } = await setup(page, { failDecision: true })
  const approval = data.approvals.find(a => !a.decision)!
  await openDesk(page, `a:${approval.id}`)
  await page.getByTestId('choice-1').click()
  const reason = page.getByRole('textbox', { name: 'Reason', exact: true })
  await reason.fill('Use the staging account instead.'); await reason.press('Enter')
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-status')).toContainText('The request expired while you were deciding')
  await expect(reason).toHaveValue('Use the staging account instead.')
  expect(approval.decision).toBeNull()
})

test('read-only people see requests but cannot decide or control', async ({ page }) => {
  const { data, calls } = await setup(page, { readOnly: true })
  await openAgents(page)
  // Controls that cannot work are not offered at all: no row button, no disabled item, no excuse (AEON-291).
  await expect(row(page, camy).getByRole('button', { name: 'Interrupt camy' })).toHaveCount(0)
  await row(page, camy).hover()
  await row(page, camy).getByRole('button', { name: 'Actions for camy' }).click()
  await expect(page.getByRole('menuitem', { name: /Interrupt|Stop now|Pause…|Resume/ })).toHaveCount(0)
  await expect(page.locator('[role="menuitem"][aria-disabled="true"]')).toHaveCount(0)
  await expect(page.getByRole('menu')).not.toContainText('Only people who may write')
  await openDesk(page, `a:${data.approvals.find(a => !a.decision)!.id}`)
  await expect(page.getByTestId('desk-decide')).toBeDisabled()
  expect(calls.filter(c => c.method === 'POST')).toHaveLength(0)
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
  await expect(details.getByRole('heading', { name: /^PHAROS-11 · Connect Hetzner Cloud for managed provisioning/ })).toBeVisible()
  await expect(details).toContainText('coordinator · owned by AEON')
  const ticketFact = details.locator('.fact').filter({ has: page.locator('dt', { hasText: /^Ticket$/ }) })
  await expect(ticketFact.locator('.key')).toHaveText('PHAROS-11')
  await expect(ticketFact.getByRole('link', { name: /PHAROS-11 Connect/ })).toHaveAttribute('href', '/p/PHAROS/PHAROS-11')
  await expect(details).toContainText('Claude Max')
  await expect(details.locator('.metric')).toHaveText([/Running/, /184k/, /22k/, /\$3\.84/])
  await expect(details.locator('.run-row .run-chip')).toHaveText(['Running', 'Completed', 'Failed'])
  await expect(details.locator('.callout')).toHaveCount(0) // Principal-only approval stays in Decision Desk.
  await details.getByRole('tab', { name: /Chat/ }).click()
  await expect(details.locator('.msg')).toHaveCount(4)
  await expect(details.locator('.msg.mine')).toHaveCount(2)
  await expect(details.locator('.msg').last()).toContainText('Awaiting reply')
  // Messages show when they were sent: the clock time on the meta line, the full time on hover (AEON-1438).
  const times = details.locator('.msg .msg-time')
  await expect(times).toHaveCount(4)
  expect(await times.evaluateAll(els => els.map(el => el.textContent === new Date(el.getAttribute('datetime')!).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })))).toEqual([true, true, true, true])
  await expect(times.first()).toHaveAttribute('data-tip', /\d{2}:\d{2}/)
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
  // B7: runs by agent, bounded session messages, sessions tenant-wide. The
  // native thread now uses 50-post windows and keyset catch-up after sends.
  expect(calls.some(c => c.path === '/api/runs' && c.query?.get('agent') === 'a0000000-0000-4000-8000-000000000001')).toBe(true)
  const threadReads = calls.filter(c => c.path === '/api/projects/p-pharos/messages' && c.method === 'GET' && c.query?.get('session') === camy)
  expect(threadReads.some(c => c.query?.get('newest_first') === 'true' && c.query?.get('limit') === '50')).toBe(true)
  expect(threadReads.every(c => c.query?.get('limit') === '50')).toBe(true)
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
  // AEON-1210: the PHAROS lead heads its group; j walks its project's other sessions in list order.
  await expect(row(page, camy)).toBeVisible()
  const order = await page.locator('.sessions .grp', { has: row(page, camy) }).locator('[data-row^="s:"]').evaluateAll(rows => rows.map(r => r.getAttribute('data-row')!.slice(2)))
  expect(order[0]).toBe(camy)
  expect(order).toContain(nova)
  await row(page, camy).locator('.c-time').click()
  await expect(page).toHaveURL(`/agents/${camy}`)
  await expect(panel(page).getByRole('heading', { name: /^PHAROS-11 · Connect Hetzner Cloud for managed provisioning/ })).toBeVisible()
  await panel(page).focus()
  await page.keyboard.press('j')
  await expect(page).toHaveURL(`/agents/${order[1]}`)
  await page.keyboard.press('j')
  await expect(page).toHaveURL(`/agents/${order[2]}`)
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL('/agents')
  await expect(panel(page)).toHaveCount(0)
  await expect(row(page, order[2]!)).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(`/agents/${order[2]}`)
})

test('AEON-1219: registry navigation and hints respect menus, fields, IME and native keys', async ({ page }, testInfo) => {
  const errors = watchErrors(page)
  const { calls } = await setup(page)
  await openAgents(page)
  const search = page.getByRole('button', { name: 'Search everything', exact: true })
  const more = page.getByRole('button', { name: 'More agent actions' })
  const guard = await controlStability(page, { search, more })
  await guard.check(async () => {
    await more.click()
    await page.keyboard.press('j')
    await page.keyboard.press('p')
    await expect(page.getByRole('dialog', { name: 'More agent actions' })).toBeVisible()
    await expect(page).toHaveURL('/agents')
    await page.keyboard.press('Escape')
  })
  guard.done()
  // AEON-1210 groups the list by project; navigation follows the displayed lead and its workers.
  await expect(row(page, camy)).toBeVisible()
  const order = await page.locator('.sessions .grp', { has: row(page, camy) }).locator('[data-row^="s:"]').evaluateAll(rows => rows.map(r => r.getAttribute('data-row')!.slice(2)))
  expect(order[0]).toBe(camy)
  expect(order.length).toBeGreaterThan(1)
  const nextSession = order[1]!
  await row(page, camy).focus()
  for (const modifiers of ['Control', 'Meta']) for (const key of ['s', 'r', 'd', 'p', 'a']) {
    await page.keyboard.press(`${modifiers}+${key}`)
  }
  await row(page, camy).dispatchEvent('keydown', { key: 'j', code: 'KeyJ', isComposing: true })
  await expect(row(page, camy)).toBeFocused()
  await page.keyboard.press('j')
  await expect(row(page, nextSession)).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(`/agents/${nextSession}`)
  await panel(page).focus()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL('/agents')
  await expect(row(page, nextSession)).toBeFocused()
  // P1 owns this list's search: / focuses it while the global palette keeps Ctrl K.
  await page.keyboard.press('/')
  await expect(page.getByRole('dialog', { name: 'Search and commands' })).toHaveCount(0)
  await expect(page.getByRole('searchbox', { name: 'Search sessions in this list' })).toBeFocused()
  await page.keyboard.press('Control+k')
  const palette = page.getByRole('dialog', { name: 'Search and commands' })
  await expect(palette).toBeVisible()
  const input = palette.getByRole('combobox')
  await input.fill('jpr')
  await input.press('j')
  await expect(input).toHaveValue('jprj')
  await page.keyboard.press('Escape')
  await row(page, nextSession).focus()
  await page.keyboard.press('?')
  const sheet = page.getByRole('dialog', { name: 'Keyboard shortcuts' })
  await expect(sheet).toBeVisible()
  const next = sheet.locator('.row').filter({ has: page.locator('dd', { hasText: /^Next \/ previous session$/ }) })
  await expect(next).toContainText('J')
  await expect(next).toContainText('Down arrow')
  await expect(sheet.locator('dd', { hasText: /^Decision Desk$/ })).toBeVisible()
  // AEON-1196 P1 now registers this handler; AEON-1219's pre-P1 absence is stale.
  const listSearch = sheet.locator('.row').filter({ has: page.locator('dd', { hasText: /^Search sessions \(this list\)$/ }) })
  await expect(listSearch).toBeVisible()
  await expect(listSearch).toContainText('F')
  await expect(listSearch).toContainText('/')
  await expect(sheet.locator('dd', { hasText: /^Jump the divider:/ })).toBeVisible()
  const closeLabel = sheet.getByText('Close the details to List only', { exact: true })
  await expect(closeLabel).toBeVisible()
  await expect(closeLabel.locator('svg')).toHaveCount(1)
  await expect(closeLabel).not.toContainText('→')
  const close = sheet.getByRole('button', { name: 'Close shortcuts' })
  const sheetGuard = await controlStability(page, { close })
  await sheetGuard.check(async () => {
    await page.keyboard.press('j')
    await expect(close).toBeFocused()
  })
  sheetGuard.done()
  await close.click()
  expect(calls.some(call => call.method === 'POST' && /\/(pause|resume)$/.test(call.path))).toBe(false)
  // Review evidence only: no screenshot equality or theme/width test matrix.
  for (const theme of ['light', 'dark'] as const) for (const width of [1440, 1024, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await page.locator('main').focus()
    await page.screenshot({ path: testInfo.outputPath(`agents-${width}-${theme}.png`), fullPage: true })
    await page.keyboard.press('?')
    await expect(sheet).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`shortcuts-${width}-${theme}.png`), fullPage: true })
    await close.click()
  }
  await page.keyboard.press('g'); await page.keyboard.press('d')
  await expect(page).toHaveURL('/decision-desk')
  expect(errors).toEqual([])
})

test('closing a session panel goes back instead of adding history, like the ticket panel', async ({ page }) => {
  await setup(page)
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await page.getByRole('link', { name: /^Agents/ }).click()
  await expect(page.locator('.agents-page .row').first()).toBeVisible()
  await row(page, camy).locator('.agent-link').click()
  await expect(page).toHaveURL(`/agents/${camy}`)
  await row(page, nova).locator('.agent-link').click()
  await expect(page).toHaveURL(`/agents/${nova}`)
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL('/agents')
  await expect(row(page, nova)).toBeFocused()
  await page.goBack()
  await expect(page).toHaveURL('/')
})

test('a held action request opens the exact native desk memo', async ({ page }) => {
  const { data } = await setup(page)
  const held = data.messages.find(m => m.is_action_request)!
  await page.goto(`/agents?needs=m:${held.id}`)
  await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).toBeVisible()
  // AEON-1339: a short request is the memo's heading, as the artifact draws it.
  await expect(page.getByTestId('desk-paper')).toContainText('Please merge the release fix once CI is green')
  await expect(page.getByRole('link', { name: 'Source', exact: true })).toHaveAttribute('href', `/agents?needs=m:${held.id}`)
})

test('held action requests resolve with an optional note through their original endpoint', async ({ page }) => {
  const { calls, data } = await setup(page)
  const held = data.messages.find(m => m.is_action_request)!
  await openDesk(page, `m:${held.id}`)
  await page.getByTestId('choice-2').click()
  await expect(page.getByTestId('choice-2')).toHaveAttribute('aria-checked', 'true')
  await page.getByTestId('choice-1').click()
  const note = page.getByRole('textbox', { name: 'P.S. for the agent' })
  await note.fill('Merged it myself after CI.'); await note.press('Enter')
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-announcement')).toContainText('Decision recorded')
  const resolution = calls.find(c => c.path.endsWith('/resolution'))!
  expect(resolution.path).toBe(`/api/projects/p-frozen/messages/${held.id}/resolution`)
  expect(resolution.body).toEqual({ decision: 'resolved', note: 'Merged it myself after CI.' })
  await expect(page.getByRole('link', { name: 'Decision Desk, 3 open' })).toBeVisible()
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
  // AEON-1172: no limit chip; a window set by hand shows as Set by you inside the details.
  await expect(claude.locator('.chip.mine')).toHaveCount(0)
  await expect(claude).toContainText('Team')
  await claude.getByRole('button', { name: 'Details for Claude Max' }).click()
  await expect(claude.locator('ul.mine .what')).toContainText('5,000,000 tokens')
  await expect(claude.locator('ul.mine')).not.toContainText('%')
  await expect(claude.getByRole('meter')).toHaveCount(0)
  await panel.getByRole('button', { name: 'Close details' }).click()
  await accounts.locator('.list-row').filter({ hasText: 'Codex Pro' }).click()
  const codex = panel.locator('.account').filter({ hasText: 'Codex Pro' })
  await panel.locator('[data-limits]').getByRole('switch', { name: 'Agents may use it · Codex Pro' }).click()
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
  await expect(page.getByRole('region', { name: 'Sign-ins and connections' })).toHaveCount(0)
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
  await openDesk(page)
  await expect(page.locator('[data-testid^=\"desk-row-a:\"]')).toHaveCount(3)
})

test('the ticket panel shows the bound agent’s attention state and links to its session', async ({ page }) => {
  await setup(page)
  await page.goto('/p/PHAROS/PHAROS-11')
  const details = page.getByRole('complementary', { name: 'Ticket details' })
  // Its principal has an unbound approval; the ticket and Agents keep this session working.
  const chip = details.getByRole('link', { name: 'Claude Code camy: Working. Open the session' })
  await expect(chip).toBeVisible()
  await chip.click()
  await expect(page).toHaveURL(`/agents/${camy}`)
  await expect(panel(page).getByRole('heading', { name: /^PHAROS-11 · Connect Hetzner Cloud for managed provisioning/ })).toBeVisible()
})

test('old approvals land on Decision Desk; runs and pacing land on Agents', async ({ page }) => {
  await setup(page)
  for (const path of ['/approvals', '/runs', '/pacing']) {
    await page.goto(path)
    await expect(page).toHaveURL(path === '/approvals' ? '/decision-desk' : '/agents')
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
    await expect(page.getByRole('link', { name: 'Decision Desk, 4 open' })).toBeVisible()
    // Row controls live in an overflow menu on phones.
    const { calls } = { calls: [] as string[] }
    page.on('request', request => { if (request.method() === 'POST') calls.push(new URL(request.url()).pathname) })
    await row(page, nova).evaluate(el => el.scrollIntoView({ block: 'center' }))
    await page.getByRole('button', { name: 'Actions for nova' }).click()
    await page.getByRole('menuitem', { name: /^Interrupt/ }).click()
    await expect.poll(() => calls.some(path => path.endsWith(`/harness-sessions/${nova}/controls/interrupt`))).toBe(true)
    // P1 moved Desk to the app header and Usage/keys into More. Verify the
    // destinations, then measure the persistent header controls through P5's sheet.
    const more = page.getByRole('button', { name: 'More agent actions', exact: true })
    await more.click()
    await expect(page.getByRole('menuitem', { name: /^Usage/ })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: /^Agent keys/ })).toBeVisible()
    await page.keyboard.press('Escape')
    await expectStableControls({ controls: {
      desk: page.getByRole('navigation', { name: 'Places' }).getByRole('link', { name: 'Decision Desk, 4 open', exact: true }),
      more,
      new: page.getByRole('button', { name: 'New: start a lead, attach a session or connect a machine', exact: true }),
    }, interactions: [
      { name: 'open a full-height session', run: async () => { await row(page, camy).locator('.c-agent a').click(); await expect(panel(page)).toBeVisible(); expect(await panel(page).boundingBox()).toEqual({ x: 0, y: 0, width: 390, height: 844 }) } },
      { name: 'return to the phone overview', run: async () => { await page.keyboard.press('Escape'); await expect(panel(page)).toHaveCount(0) } },
    ] })
    // Nothing is lost on a phone: Usage and Agent keys sit in More.
    await page.locator('.page-head').getByRole('button', { name: 'More agent actions' }).click()
    await expect(page.getByRole('menuitem', { name: /^Usage/ })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: /^Agent keys/ })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByRole('menuitem', { name: /^Usage/ })).toHaveCount(0)
    expect(errors).toEqual([])
  })
}

for (const colorScheme of ['light', 'dark'] as const) for (const width of [390, 1024, 1440]) {
  test(`cutover overview and session retain long German context at ${width} in ${colorScheme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 }); await page.emulateMedia({ colorScheme })
    const { data } = await setup(page)
    const brief = 'Die Berechtigungsanfrage mit nachvollziehbarer Begründung prüfen und den ursprünglichen Arbeitskontext für die nachfolgende Sitzung erhalten.'
    data.sessions.find(session => session.id === camy)!.brief = brief
    await openAgents(page)
    await expect(row(page, camy)).toContainText(brief)
    await expect(page.getByRole('region', { name: 'Needs you', exact: true })).toHaveCount(0)
    await expect(page.getByRole('link', { name: 'Decision Desk, 4 open', exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({ path: testInfo.outputPath(`agents-overview-${width}-${colorScheme}.png`), fullPage: true })
    await row(page, camy).locator('.agent-link').click(); await expect(panel(page)).toBeVisible()
    await expect(panel(page)).toContainText(brief)
    await panel(page).getByText(brief, { exact: true }).scrollIntoViewIfNeeded()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({ path: testInfo.outputPath(`agents-session-${width}-${colorScheme}.png`), fullPage: true })
    const details = panel(page)
    const ticketFact = details.locator('.fact').filter({ has: page.locator('dt', { hasText: /^Ticket$/ }) })
    const ticket = ticketFact.getByRole('link', { name: /^PHAROS-11 Connect/ })
    await expect(ticketFact.locator('.key')).toHaveText('PHAROS-11')
    await expect(ticketFact.locator('.faint')).toHaveText('· In progress')
    // AEON-1240: the fact reads as one sentence; its state never starts a line of its own, at any width.
    const [factText, factState] = await Promise.all([ticketFact.locator('dd').boundingBox(), ticketFact.locator('.ticket-state').boundingBox()])
    expect(factState!.x).toBeGreaterThan(factText!.x + 4)
    await expect(ticket).toHaveAttribute('href', '/p/PHAROS/PHAROS-11')
    await ticketFact.evaluate(element => element.scrollIntoView({ block: 'center' }))
    if (width === 390) expect((await ticket.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    await expectStableControls({
      controls: { header: details.locator('.pane-bar'), actions: details.getByRole('group', { name: 'Panel actions' }), tabs: details.getByRole('tablist'), ticket },
      scrollAreas: { details: details.locator('.scroll') },
      interactions: [
        { name: 'hover the ticket title', run: async () => {
          await ticket.hover()
          await expect(ticket.locator('.ticket-title')).toHaveCSS('text-decoration-line', 'underline')
          await expect(ticket.locator('.key')).toHaveCSS('text-decoration-line', 'none')
        } },
        { name: 'focus the ticket link', run: async () => { await ticket.focus(); await expect(ticket).toBeFocused() } },
      ],
    })
    await ticket.blur()
    await details.locator('.pane-bar').hover()
    await ticketFact.evaluate(element => element.scrollIntoView({ block: 'center' }))
    await expect(ticketFact.locator('.faint')).toBeInViewport({ ratio: 1 })
    await page.screenshot({ path: testInfo.outputPath(`details-ticket-${width}-${colorScheme}.png`), fullPage: true })
  })
}

for (const colorScheme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`SC2 ticket detail uses the available width at ${width} in ${colorScheme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme })
    await setup(page)
    await openAgents(page)
    // Every form carries the ticket key in the name; its link lives in the session's details.
    await row(page, camy).locator('.l2').click()
    await panel(page).getByRole('link', { name: /^PHAROS-11/ }).first().click()
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

// AEON-1705 §9: the risk is false live facts, duplicate session identities, or
// policy actions drifting to another record/person while the right panel updates.
test('assistants use the existing Agents dock with stable tabs, locked policy and audited revision saves', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  // The approved remote entry point exposes Playwright. Run the affected unit
  // and glyph guard in that same remote checkout before the interactions.
  const units = execFileSync(process.execPath, ['node_modules/vitest/vitest.mjs', 'run', 'tests/assistants.unit.test.ts', '--maxWorkers=1'], { encoding: 'utf8', timeout: 60_000, maxBuffer: 1024 * 1024 })
  const glyphs = execFileSync(process.execPath, ['--test', 'tests/text-glyphs.test.ts'], { encoding: 'utf8', timeout: 60_000, maxBuffer: 1024 * 1024 })
  await testInfo.attach('assistant-units-and-glyphs', { body: units + glyphs, contentType: 'text/plain' })
  const errors = watchErrors(page)
  const { work } = await setup(page)
  work.preferences['ui.agents.sections'] = { dial: false }
  const { items } = assistantEvidence(Date.now(), me.id)
  const { writes, events, routineWrites, routines, runOwners, routineReads } = await mockAssistants(page, items, me.id)
  let policyAccess = true
  await page.route('**/api/me/permissions*', route => {
    const permissions = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    permissions.workspace.permissions.push('events.read')
    if (policyAccess) permissions.workspace.permissions.push('agents.create', 'recurrences.manage')
    return route.fulfill({ json: permissions })
  })
  const shot = async (name: string) => page.screenshot({ path: testInfo.outputPath(`aeon-1705-${name}-fix1.png`), fullPage: true })
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await openAgents(page)
  const section = page.locator('.assistants')
  const dialFold = page.getByRole('button', { name: 'Fold the dial', exact: true })
  if (await dialFold.isVisible()) await dialFold.click()
  await expect(section.getByRole('heading')).toHaveText('Assistants · 2')
  await expect(section).toHaveClass(/\bglass-card\b/)
  const layout = page.locator('[data-layout-cycle]')
  await expect(section.locator('.assistants-head')).toHaveAttribute('aria-owns', 'agents-layout-cycle')
  await expect(layout).toHaveAttribute('id', 'agents-layout-cycle')
  await expect(layout).toBeVisible()
  await expect(layout).toHaveAccessibleName(/^Layout: .+ Next: /)
  await expect(layout).toHaveAttribute('data-tip', /Next:/)
  const layoutFrame = await layout.boundingBox(), sectionHead = await section.locator('.assistants-head').boundingBox()
  expect(layoutFrame!.x).toBeGreaterThanOrEqual(sectionHead!.x)
  expect(layoutFrame!.x + layoutFrame!.width).toBeLessThanOrEqual(sectionHead!.x + sectionHead!.width)
  expect(layoutFrame!.y).toBeGreaterThanOrEqual(sectionHead!.y)
  expect(layoutFrame!.y + layoutFrame!.height).toBeLessThanOrEqual(sectionHead!.y + sectionHead!.height)
  const cardStyles = await page.locator('.assistants, .sessions').evaluateAll(cards => cards.map(card => {
    const style = getComputedStyle(card)
    return [style.backgroundImage, style.borderTopColor, style.borderTopWidth, style.borderRadius, style.boxShadow]
  }))
  expect(cardStyles[0]).toEqual(cardStyles[1])
  await expect(section.locator('.assistant-row')).toHaveCount(2)
  expect(await section.locator('.assistant-row').evaluateAll(rows => rows.map(row => row.getBoundingClientRect().height))).toEqual([56, 56])
  const positions = await page.evaluate(() => ({ capacity: document.querySelector('.cap-row')!.getBoundingClientRect().bottom, assistants: document.querySelector('.assistants')!.getBoundingClientRect(), sessions: document.querySelector('.sessions')!.getBoundingClientRect().top }))
  expect(positions.capacity).toBeLessThanOrEqual(positions.assistants.top)
  expect(positions.assistants.bottom).toBeLessThanOrEqual(positions.sessions)
  await expect(page.locator(`[data-row="s:${session(5)}"]`)).toHaveCount(0)
  // Health/spend stay unknown. The Desk confirms there are no open asks for
  // these assistants; owned routines use D's revisioned API.
  await expect(section.locator('.assistant-state').first()).toHaveText('Status unavailable')
  await expect(section.locator('.budget-unknown')).toHaveCount(2)
  for (const selector of ['.budget-unknown', '.adapter-column']) {
    for (const cell of await section.locator(selector).all()) {
      await expect(cell).toHaveText('—')
      await expect(cell).toHaveAttribute('title', 'Not available yet')
      await expect(cell).toHaveAttribute('data-tip', 'Not available yet')
    }
  }
  await expect(section.locator('.assistant-approvals > span')).toHaveCount(2)
  for (const cell of await section.locator('.assistant-approvals > span').all()) {
    await expect(cell).toHaveText('—')
    await expect(cell).not.toHaveAttribute('data-tip', 'Not available yet')
  }
  const lastActivity = section.locator('.assistant-row').first().locator('.last-column')
  await expect(lastActivity).toHaveText('Ran Inbox triage 1 · 30 min ago')
  await expect(lastActivity).not.toHaveAttribute('data-tip', 'Not available yet')
  await expect(section.locator('.assistant-row').first().locator('.routines-column')).not.toHaveAttribute('data-tip', 'Not available yet')
  await expect(section.locator('.assistant-open').first()).toHaveAccessibleDescription(/Ran Inbox triage 1 · 30 min ago/)
  expect(await lastActivity.evaluate(cell => cell.scrollWidth <= cell.clientWidth)).toBe(true)
  await section.locator('.budget-unknown').first().hover()
  await expect(page.locator('.tooltip:popover-open')).toHaveText('Not available yet')
  const amy = items.find(item => item.display_name === 'Amy')!
  await section.hover()
  const stableRows = await controlStability(page, { layout, amy: section.locator(`[data-assistant="${amy.principal_id}"] .assistant-open`), scout: section.locator('.assistant-row').nth(1).locator('.assistant-open') }, { tolerance: 0 })
  await stableRows.check(async () => {
    amy.display_name = 'Zamy'; amy.revision++
    await page.evaluate(() => window.dispatchEvent(new Event('focus')))
    await expect(section.locator('.assistant-name strong').first()).toHaveText('Zamy')
  })
  stableRows.done()
  await page.mouse.move(1, 1)
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(section.locator('.assistant-name strong').first()).toHaveText('Scout')
  amy.display_name = 'Amy'; amy.revision++
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(section.locator(`[data-assistant="${amy.principal_id}"] strong`)).toHaveText('Amy')
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(section.locator('.assistant-name strong').first()).toHaveText('Amy')
  expect((await new AxeBuilder({ page }).include('.assistants').withRules(['color-contrast']).analyze()).violations).toEqual([])
  await shot('1440-light-typical')
  await page.emulateMedia({ colorScheme: 'dark' }); await shot('1440-dark-typical')
  expect((await new AxeBuilder({ page }).include('.assistants').withRules(['color-contrast']).analyze()).violations).toEqual([])
  await page.emulateMedia({ colorScheme: 'light' })
  await page.setViewportSize({ width: 1024, height: 1000 })
  await expect(section.locator('.last-column').first()).toBeHidden()
  await expect(section.locator('.folded-facts').first()).toBeVisible()
  await shot('1024-light-folded')
  await page.emulateMedia({ colorScheme: 'dark' }); await shot('1024-dark-folded')
  await page.emulateMedia({ colorScheme: 'light' })
  await page.setViewportSize({ width: 1440, height: 1000 })
  const policyRevision = amy.revision
  await section.getByRole('button', { name: 'Amy · Status unavailable', exact: true }).click()
  const details = page.getByRole('complementary', { name: 'Assistant details', exact: true })
  await expect(details).toBeVisible()
  const overview = details.getByRole('tabpanel', { name: 'Overview', exact: true })
  await expect(overview.locator('.overview-pending')).toHaveText("Spend and adapter health appear once they're connected.")
  await expect(overview).not.toContainText('Spend and daily totals are not available yet.')
  await expect(overview).not.toContainText('Adapter health is not available yet.')
  await expect(overview).not.toContainText('Assistant approvals are not available yet.')
  await expect(overview.locator('[data-tip="Not available yet"]')).toHaveCount(3)
  await expect(overview).toContainText('No open approvals')
  await expect(details.locator('.assistant-panel-head')).not.toContainText('Pause and resume for this assistant are not available yet.')
  await expect(page.getByRole('complementary', { name: 'Session details' })).toHaveCount(0)
  const tabs = details.getByRole('tablist')
  const controls = { tabs, close: details.getByRole('button', { name: 'Close assistant details' }), frame: details, ...Object.fromEntries(['Overview', 'Approvals', 'Routines', 'Policy', 'Activity'].map(name => [name, tabs.getByRole('tab', { name, exact: true })])) }
  await expectStableControls({ controls, scrollAreas: { content: details.locator('.assistant-panel-scroll') }, interactions: ['Approvals', 'Routines', 'Policy', 'Activity', 'Overview'].map(name => ({ name, run: async () => {
    await tabs.getByRole('tab', { name, exact: true }).click()
    if (name === 'Approvals') {
      const pane = details.getByRole('tabpanel', { name: 'Approvals', exact: true })
      await expect(pane).toContainText('No open approvals')
      await expect(pane).toContainText('Recent decisions are unavailable.')
      await expect(pane.getByRole('button')).toHaveCount(0)
    }
    if (name === 'Routines') {
      const pane = details.getByRole('tabpanel', { name: 'Routines', exact: true })
      await expect(pane.locator('[data-routine]')).toHaveCount(3)
      await expect(pane).toContainText('Every Monday, Tuesday, Wednesday, Thursday, Friday at 08:00')
      await expect(pane.getByRole('link', { name: 'AEON-1 · Aeon foundation' }).first()).toHaveAttribute('href', '/p/AEON/AEON-1')
      const row = pane.locator('[data-routine]').first(), action = row.getByRole('button')
      const routineGuard = await controlStability(page, { row, action, tabs, close: controls.close })
      await routineGuard.check(async () => { await action.click(); await expect(action).toHaveAccessibleName('Resume Inbox triage 1') })
      await routineGuard.check(async () => { await action.click(); await expect(action).toHaveAccessibleName('Pause Inbox triage 1') })
      routineGuard.done()
      expect(routineWrites).toEqual([{ id: routines[0]!.id, action: 'pause', expected_revision: 1 }, { id: routines[0]!.id, action: 'resume', expected_revision: 2 }])
      const readsBefore = routineReads.length
      runOwners[routines[1]!.id] = 'af000000-0000-4000-8000-000000000099'
      const ownerGuard = await controlStability(page, Object.fromEntries([0, 1, 2].map(index => [`routine ${index + 1}`, pane.locator('[data-routine]').nth(index).getByRole('button')])))
      await ownerGuard.check(async () => {
        await page.evaluate(() => (window as unknown as { fixtureQuietStreams: { emit: (name: string, data: string, id: number) => void } }).fixtureQuietStreams.emit('recurrence.updated', '{}', 1705))
        await expect(pane.locator('[data-routine]').nth(1)).toContainText('The latest run belongs to another owner.')
        await expect(pane.locator('[data-routine]').nth(1).getByRole('link', { name: 'AEON-1 · Aeon foundation' })).toHaveCount(0)
      })
      ownerGuard.done()
      expect(routineReads.slice(readsBefore)).not.toContain(`/api/recurrences/${routines[1]!.id}/runs/${routines[1]!.last_run.id}/agent-result`)
    }
    if (name === 'Policy') {
      const policy = details.getByRole('tabpanel', { name: 'Policy', exact: true })
      await expect(policy).toContainText('Read and write tickets')
      await expect(policy.getByText('nodes.read', { exact: true })).toHaveCount(0)
      for (const locked of ['outward', 'secrets']) await expect(details.locator(`[data-locked-rule="${locked}"] input, [data-locked-rule="${locked}"] select`)).toHaveCount(0)
    }
    await shot(`1440-light-panel-${name.toLowerCase()}`)
    await page.emulateMedia({ colorScheme: 'dark' }); await shot(`1440-dark-panel-${name.toLowerCase()}`)
    await page.emulateMedia({ colorScheme: 'light' })
  } })) })
  await details.getByRole('button', { name: 'More assistant actions' }).click()
  const pause = page.getByRole('menuitem', { name: 'Pause', exact: true })
  await expect(pause).toBeDisabled()
  await expect(pause).toHaveAttribute('data-tip', 'Pause and resume for this assistant are not available yet.')
  await expect(pause).toHaveAccessibleDescription('Pause and resume for this assistant are not available yet.')
  await pause.hover()
  await expect(page.locator('.tooltip:popover-open')).toHaveText('Pause and resume for this assistant are not available yet.')
  await page.getByRole('menuitem', { name: 'Edit policy', exact: true }).click()
  await expect(details.getByRole('heading', { name: 'Budget', exact: true })).toBeVisible()
  const budget = details.getByRole('textbox', { name: 'Per week', exact: true })
  await expect(budget).toHaveValue('10')
  await budget.scrollIntoViewIfNeeded()
  const save = details.getByRole('button', { name: /^Save/ })
  const saveGuard = await controlStability(page, { budget, save, cancel: details.getByRole('button', { name: /^Cancel/ }), tabs })
  await saveGuard.check(() => budget.fill('15'))
  saveGuard.done()
  await shot('1440-light-panel-policy-edit')
  await page.emulateMedia({ colorScheme: 'dark' }); await shot('1440-dark-panel-policy-edit')
  await page.emulateMedia({ colorScheme: 'light' })
  await save.click()
  await expect(details.getByRole('status')).toHaveText('Policy saved')
  expect(writes).toHaveLength(1)
  expect(writes[0]).toMatchObject({ id: amy.principal_id, body: { expected_revision: policyRevision, policy: { weekly_budget: { amount: '15', unit: 'USD' }, outward_action_rule: 'approval_required', never_ask_person_for_secrets_or_manual_steps: true } } })
  expect(events).toHaveLength(1)
  await expect(details.locator('.policy-audit')).toContainText(/Changed by .*: Budget: \$10 to \$15/)
  // A conflicting write must retain the draft and never report a successful save.
  await details.getByRole('button', { name: 'More assistant actions' }).click()
  await page.getByRole('menuitem', { name: 'Edit policy', exact: true }).click()
  await budget.fill('20'); amy.revision++
  await save.click()
  await expect(details.getByRole('status')).toContainText('The policy changed while this draft was open')
  await expect(budget).toHaveValue('20')
  expect(writes.at(-1)?.body.expected_revision).toBe(policyRevision + 1)
  await details.getByRole('button', { name: /^Cancel/ }).click()
  await details.getByRole('button', { name: 'Close assistant details' }).click()
  await expect(details).toHaveCount(0)
  policyAccess = false
  await page.reload()
  await section.getByRole('button', { name: 'Amy · Status unavailable', exact: true }).click()
  await details.getByRole('tab', { name: 'Policy', exact: true }).click()
  await expect(details).toContainText('Only people with Policy access can change this.')
  await expect(details.getByRole('textbox')).toHaveCount(0)
  await expect(details.getByRole('button', { name: 'More assistant actions' })).toHaveCount(0)
  expect((await new AxeBuilder({ page }).include('.assistant-panel').withRules(['color-contrast']).analyze()).violations).toEqual([])
  await shot('1440-light-panel-policy-readonly')
  await page.emulateMedia({ colorScheme: 'dark' }); await shot('1440-dark-panel-policy-readonly')
  await page.emulateMedia({ colorScheme: 'light' })
  for (const width of [390, 320]) {
    await details.getByRole('button', { name: 'Close assistant details' }).click()
    await page.setViewportSize({ width, height: 844 })
    await section.scrollIntoViewIfNeeded()
    expect(await section.locator('.assistant-row').first().evaluate(el => el.getBoundingClientRect().height)).toBe(64)
    if (width === 390) {
      await shot('390-light-cards')
      await page.emulateMedia({ colorScheme: 'dark' }); await shot('390-dark-cards')
      await page.emulateMedia({ colorScheme: 'light' })
    }
    await section.getByRole('button', { name: 'Amy · Status unavailable', exact: true }).click()
    const sheet = await details.boundingBox()
    expect(sheet?.height).toBe(844)
    expect(await details.getByRole('tab', { name: 'Overview', exact: true }).evaluate(el => el.getBoundingClientRect().height)).toBe(44)
    await expectStableControls({ controls: { tabs: details.getByRole('tablist'), close: details.getByRole('button', { name: 'Close assistant details' }), sheet: details }, scrollAreas: { body: details.locator('.assistant-panel-scroll') }, interactions: ['Overview', 'Policy'].map(name => ({ name, run: async () => {
      await details.getByRole('tab', { name, exact: true }).click()
      if (width === 390) {
        await shot(`390-light-sheet-${name.toLowerCase()}`)
        await page.emulateMedia({ colorScheme: 'dark' }); await shot(`390-dark-sheet-${name.toLowerCase()}`)
        await page.emulateMedia({ colorScheme: 'light' })
      }
    } })) })
    expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0)
  }
  await details.getByRole('button', { name: 'Close assistant details' }).click()
  await expect(details).toHaveCount(0)
  items.push(...Array.from({ length: 5 }, (_, n) => ({ ...amy, principal_id: `af000000-0000-4000-8000-00000000000${n}`, display_name: `Research ${n}` })))
  await page.reload()
  await expect(section.locator('.assistant-row')).toHaveCount(5)
  await section.getByRole('button', { name: 'Show all (7)', exact: true }).click()
  await expect(section.locator('.assistant-row')).toHaveCount(7)
  await page.reload()
  await expect(section.locator('.assistant-row')).toHaveCount(7)
  const longName = 'Rechercheassistentin für bereichsübergreifende Wissenspflege und Posteingang'
  amy.display_name = longName; amy.revision++
  await page.setViewportSize({ width: 390, height: 844 })
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.reload()
  await expect(section.locator(`[data-assistant="${amy.principal_id}"] strong`)).toHaveAttribute('data-tip', longName)
  await section.scrollIntoViewIfNeeded(); await shot('390-dark-long-german-name')
  await section.locator(`[data-assistant="${amy.principal_id}"] .assistant-open`).click()
  await expect(details.locator('.assistant-title-name')).toHaveAttribute('data-tip', longName)
  await shot('390-dark-sheet-long-german-name')
  const identityGuard = await controlStability(page, { sheet: details, tabs: details.getByRole('tablist'), close: details.getByRole('button', { name: 'Close assistant details' }) })
  await identityGuard.check(async () => { amy.display_name = 'Amy'; amy.revision++; await page.evaluate(() => window.dispatchEvent(new Event('focus'))); await expect(details.locator('.assistant-title-name')).toHaveText('Amy') })
  identityGuard.done()
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0)
  expect(errors).toEqual([])
})

test('assistant Desk approvals keep independent links, waiting order and partial coverage', async ({ page }, testInfo) => {
  // Risk: merged outward approvals never reach Agents, or a partial/failed read
  // claims a complete count. Deciding must remain exclusively on the Desk.
  test.setTimeout(120_000)
  const errors = watchErrors(page), now = Date.now()
  const { data, work } = await setup(page)
  work.preferences['ui.agents.sections'] = { dial: false }
  const { items } = assistantEvidence(now, me.id)
  const amy = items.find(item => item.display_name === 'Amy')!
  amy.display_name = 'Zamy' // Waiting must outrank Scout's alphabetical position.
  await mockAssistants(page, items, me.id)
  const asks = [1, 2].map(n => ({ ...data.approvals[0]!, id: `da000000-0000-4000-8000-00000000000${n}`, agent_principal_id: amy.principal_id, agent_name: 'Same display name', scope: 'approvals.request.outward_message', resource_kind: 'node', resource_id: 'n-a1', run_id: null, rationale: `Send the approved summary ${n}`, decision: null, proposed_at: new Date(now - n * 60_000).toISOString(), expires_at: new Date(now + 3600_000).toISOString() }))
  data.approvals.push(...asks)
  let partial = false, readable = true, mayApprove = true
  let release!: () => void
  const held = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/me/permissions*', route => {
    const effective = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    if (!mayApprove) effective.workspace.permissions = effective.workspace.permissions.filter(permission => permission !== 'approvals.decide')
    return route.fulfill({ json: effective })
  })
  await page.route('**/api/decision-desk/projection?**', async route => {
    await held
    if (!readable) return route.fulfill({ status: 503, json: { error: 'Desk temporarily unavailable' } })
    const cursor = Number(new URL(route.request().url()).searchParams.get('cursor') ?? 0)
    const rows = asks.map(ask => ({ id: ask.id, kind: 'approval', revision: 1, title: ask.rationale, created_at: ask.proposed_at, expires_at: ask.expires_at, held: true, can_decide: mayApprove, asked_by: { id: ask.agent_principal_id, name: ask.agent_name }, ticket_key: 'AEON-1', href: `/decision-desk?item=a:${ask.id}`, source: `/api/approvals/${ask.id}` }))
    const other = data.approvals[0]!
    rows.push({ ...rows[0]!, id: other.id, asked_by: { id: other.agent_principal_id, name: asks[0]!.agent_name }, href: `/decision-desk?item=a:${other.id}`, source: `/api/approvals/${other.id}` })
    return route.fulfill({ json: { items: rows, counts: { open: partial ? 1003 : 3, held: partial ? 1003 : 3, chores: 0 }, has_more: partial, ...(partial ? { next_cursor: String(cursor + 1) } : {}), as_of: new Date(now).toISOString() } })
  })
  await page.setViewportSize({ width: 1440, height: 1000 })
  await openAgents(page)
  const section = page.locator('.assistants'), amyRow = section.locator(`[data-assistant="${amy.principal_id}"]`)
  await expect(section.locator('.assistant-row')).toHaveCount(2)
  await expect(section.locator('.assistant-name strong').first()).toHaveText('Scout')
  release()
  const link = amyRow.getByRole('link', { name: '2 to approve', exact: true })
  await expect(link).toHaveAttribute('href', `/decision-desk?assistant=${amy.principal_id}`)
  await expect(section.locator('.assistant-name strong').first()).toHaveText('Zamy')
  await expect(amyRow.locator('.assistant-state')).toHaveText('Waiting for you')
  await amyRow.locator('.assistant-open').focus()
  await page.keyboard.press('Tab')
  await expect(link).toBeFocused()
  await expect(page.getByRole('complementary', { name: 'Assistant details', exact: true })).toHaveCount(0)
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(`/decision-desk?assistant=${amy.principal_id}`)
  await expect(page.getByTestId('desk-scope')).toContainText('Showing items from this assistant.')
  for (const ask of asks) await expect(page.getByTestId(`desk-row-a:${ask.id}`)).toBeVisible()
  await expect(page.getByTestId(`desk-row-a:${data.approvals[0]!.id}`)).toHaveCount(0)
  await openAgents(page)
  await expect(link).toBeVisible()
  amy.display_name = 'Rechercheassistentin für bereichsübergreifende Wissenspflege und Posteingang'; amy.revision++
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(amyRow.locator('strong')).toHaveAttribute('data-tip', amy.display_name)
  const details = page.getByRole('complementary', { name: 'Assistant details', exact: true })
  const expectClearApprovalBands = async () => {
    const bands = await amyRow.evaluate(row => {
      const box = (selector: string) => { const r = row.querySelector(selector)!.getBoundingClientRect(); return { x: r.x, y: r.y, right: r.right, bottom: r.bottom, height: r.height } }
      return { row: row.getBoundingClientRect().height, status: box('.assistant-state'), spend: box('.phone-facts'), link: box('.assistant-approvals a') }
    })
    expect(bands.row).toBe(64)
    expect(bands.link.height).toBeGreaterThanOrEqual(44)
    expect(bands.link.y, 'approval hit area starts below the status band').toBeGreaterThanOrEqual(bands.status.bottom)
    expect(bands.link.x, 'approval hit area stays beside the spend facts').toBeGreaterThanOrEqual(bands.spend.right)
  }
  // A narrow card on a wide viewport must use the same non-overlapping bands.
  await section.evaluate(element => { element.style.maxWidth = '500px' })
  await expectClearApprovalBands()
  await section.evaluate(element => { element.style.removeProperty('max-width') })
  const checks: { width: number; theme: string; approvals: number; overflow: number }[] = []
  for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await section.scrollIntoViewIfNeeded()
    if (width === 390) {
      await expectClearApprovalBands()
      await amyRow.locator('.assistant-state').click()
      await expect(details).toBeVisible()
      await details.getByRole('button', { name: 'Close assistant details' }).click()
      await expect(page).toHaveURL('/agents')
    }
    await page.screenshot({ path: testInfo.outputPath(`aeon-1705-fix5-${width}-${theme}-waiting.png`), fullPage: true })
    await amyRow.locator('.assistant-open').click()
    const tabs = details.getByRole('tablist'), close = details.getByRole('button', { name: 'Close assistant details' })
    await expectStableControls({ controls: { tabs, close, overview: tabs.getByRole('tab', { name: 'Overview', exact: true }), approvals: tabs.getByRole('tab', { name: 'Approvals', exact: true }), ...(width === 390 ? { sheet: details } : {}) }, scrollAreas: { body: details.locator('.assistant-panel-scroll') }, interactions: [{ name: 'open approvals without moving controls', run: async () => {
      await tabs.getByRole('tab', { name: 'Approvals', exact: true }).click()
      const pane = details.getByRole('tabpanel', { name: 'Approvals', exact: true })
      await expect(pane.locator('.assistant-log-entry')).toHaveCount(2)
      await expect(pane).toContainText('Send the approved summary 1')
      await expect(pane).toContainText('AEON-1')
      await expect(pane).toContainText('Recent decisions are unavailable.')
      await expect(pane.getByRole('button')).toHaveCount(0)
      await expect(pane.getByRole('link', { name: 'Open on the Desk', exact: true }).first()).toHaveAttribute('href', `/decision-desk?assistant=${amy.principal_id}&item=a%3A${asks[0]!.id}`)
    } }] })
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    expect(overflow).toBeLessThanOrEqual(0)
    checks.push({ width, theme, approvals: 2, overflow })
    await page.screenshot({ path: testInfo.outputPath(`aeon-1705-fix5-${width}-${theme}-approvals.png`), fullPage: true })
    await close.click()
  }
  await amyRow.locator('.assistant-open').click()
  await details.getByRole('tab', { name: 'Approvals', exact: true }).click()
  await page.clock.install()
  const wake = async () => {
    await page.evaluate(() => (window as unknown as { fixtureQuietStreams: { emit: (name: string, data: string, id: number) => void } }).fixtureQuietStreams.emit('approval.proposed', '{}', 1705))
    await page.clock.fastForward(3001)
  }
  const countGuard = await controlStability(page, { assistant: amyRow.locator('.assistant-open'), approvalLink: amyRow.locator('.assistant-approvals a') })
  const digitLeft = () => amyRow.locator('.assistant-approvals a').evaluate(link => {
    const range = document.createRange()
    range.setStart(link.firstChild!, 0); range.setEnd(link.firstChild!, 1)
    return range.getBoundingClientRect().x
  })
  const beforeDigit = await digitLeft()
  await countGuard.check(async () => {
    partial = true
    await wake()
    await expect(amyRow.getByRole('link', { name: '2+ to approve', exact: true })).toHaveAttribute('title', 'This list is partial; more entries are available.')
  })
  countGuard.done()
  expect(Math.abs(await digitLeft() - beforeDigit), 'the first digit stays still when the count becomes partial').toBeLessThanOrEqual(.5)
  await expect(details.locator('.panel-status-line')).toContainText('At least 2 actions need an approval')
  await expect(details.getByRole('tabpanel', { name: 'Approvals', exact: true })).toContainText('This list is partial; more entries are available.')
  readable = false
  await wake()
  await expect(details.getByRole('alert')).toHaveText('Open approvals could not be refreshed. Check again on the Desk.')
  readable = true; partial = false; mayApprove = false
  await page.reload()
  await expect(amyRow.locator('.assistant-state')).toHaveText('Waiting for an approval')
  await expect(amyRow.getByRole('link', { name: '2 to approve', exact: true })).toBeVisible()
  await testInfo.attach('aeon-1705-fix5-checks.json', { body: JSON.stringify(checks), contentType: 'application/json' })
  expect(errors).toEqual([])
})

test('assistant admission waiting sort retries after hover and focus leave the section', async ({ page }) => {
  // Risk: the first Desk read arrives during interaction, permanently losing
  // waiting-first admission order or moving a row under the pointer/focus.
  const errors = watchErrors(page), now = Date.now()
  const { work } = await setup(page)
  work.preferences['ui.agents.sections'] = { dial: false }
  const { items } = assistantEvidence(now, me.id)
  const amy = items.find(item => item.display_name === 'Amy')!
  amy.display_name = 'Zamy'
  await mockAssistants(page, items, me.id)
  let held: Promise<void>
  await page.route('**/api/decision-desk/projection?**', async route => {
    await held
    return route.fulfill({ json: { items: [{ id: 'da000000-0000-4000-8000-000000000001', kind: 'approval', revision: 1, title: 'Send the summary', created_at: new Date(now).toISOString(), held: true, asked_by: { id: amy.principal_id, name: amy.display_name }, href: '/decision-desk?item=a:da000000-0000-4000-8000-000000000001' }], counts: { open: 1, held: 1, chores: 0 }, has_more: false, as_of: new Date(now).toISOString() } })
  })
  await page.setViewportSize({ width: 1440, height: 1000 })
  for (const interaction of ['pointer', 'focus']) await test.step(interaction, async () => {
    let release!: () => void
    held = new Promise<void>(resolve => { release = resolve })
    await page.mouse.move(0, 0)
    await openAgents(page)
    const section = page.locator('.assistants'), amyRow = section.locator(`[data-assistant="${amy.principal_id}"]`)
    await expect(section.locator('.assistant-name strong').first()).toHaveText('Scout')
    const open = amyRow.locator('.assistant-open')
    if (interaction === 'pointer') await section.hover()
    else await open.focus()
    const guard = await controlStability(page, { amy: open, scout: section.locator('.assistant-row').first().locator('.assistant-open') }, { tolerance: 0 })
    await guard.check(async () => {
      release()
      await expect(amyRow.getByRole('link', { name: '1 to approve', exact: true })).toBeVisible()
      await expect(section.locator('.assistant-name strong').first()).toHaveText('Scout')
    })
    if (interaction === 'focus') await guard.check(async () => {
      await amyRow.getByRole('link').focus()
      await expect(section.locator('.assistant-name strong').first()).toHaveText('Scout')
    })
    guard.done()
    if (interaction === 'pointer') await page.mouse.move(0, 0)
    else await page.locator('h1').evaluate(element => { element.tabIndex = -1; element.focus() })
    await expect(section.locator('.assistant-name strong').first()).toHaveText('Zamy')
  })
  expect(errors).toEqual([])
})

test('zero assistants show the permission-gated existing connect flow without sample rows', async ({ page }, testInfo) => {
  test.setTimeout(90_000)
  const { work } = await setup(page)
  work.preferences['ui.agents.sections'] = { dial: false }
  const items: ReturnType<typeof assistantEvidence>['items'] = []
  await mockAssistants(page, items, me.id)
  let connectAccess = true
  await page.route('**/api/me/permissions*', route => {
    const permissions = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    if (connectAccess) permissions.workspace.permissions.push('agents.create')
    return route.fulfill({ json: permissions })
  })
  await page.goto('/agents')
  const section = page.locator('.assistants')
  for (const permitted of [true, false]) {
    connectAccess = permitted
    await page.reload()
    const fold = page.getByRole('button', { name: 'Fold the dial', exact: true })
    if (await fold.isVisible()) await fold.click()
    await expect(section).toContainText('No assistants yet.')
    await expect(section).toHaveClass(/\bglass-card\b/)
    await expect(section.locator('.assistants-head')).toContainText('Assistants · 0')
    await expect(section.locator('.assistant-row')).toHaveCount(0)
    const connect = section.getByRole('link', { name: 'Connect an assistant', exact: true })
    await expect(connect).toHaveCount(permitted ? 1 : 0)
    if (permitted) await expect(connect).toHaveAttribute('href', '/agents/register-agent?kind=assistant')
    for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
      if (width === 1440) {
        await expect(section.locator('.assistants-head')).toHaveAttribute('aria-owns', 'agents-layout-cycle')
        await expect(page.locator('#agents-layout-cycle')).toHaveAccessibleName(/^Layout: .+ Next: /)
        await expect(page.locator('#agents-layout-cycle')).toHaveAttribute('data-tip', /Next:/)
      }
      await page.screenshot({ path: testInfo.outputPath(`aeon-1705-${width}-${theme}-empty-${permitted ? 'connect' : 'readonly'}-fix1.png`), fullPage: true })
      expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0)
    }
  }
  connectAccess = true
  await page.reload()
  await section.getByRole('link', { name: 'Connect an assistant', exact: true }).click()
  await expect(page).toHaveURL('/agents/register-agent?kind=assistant')
  await expect(page.getByRole('heading', { name: 'Connect an assistant', exact: true })).toBeVisible()
  await expect(page.getByText('Creates an identity without credentials, project access or spend authority.', { exact: false })).toBeVisible()
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill('Harbor Clerk')
  await page.getByRole('button', { name: 'Connect an assistant', exact: false }).click()
  await expect(page).toHaveURL(/\/agents\?assistant=/)
  await expect(page.getByRole('complementary', { name: 'Assistant details' }).getByRole('heading', { name: /Harbor Clerk/ })).toBeVisible()
  expect(items).toHaveLength(1)
  expect(items[0]!.policy.allowed_projects).toEqual([])
  expect(items[0]!.policy.weekly_budget.amount).toBe('0')
  const phonePanel = page.getByRole('complementary', { name: 'Assistant details' })
  await phonePanel.getByRole('button', { name: 'More assistant actions' }).click()
  const phonePause = page.getByRole('menuitem', { name: 'Pause', exact: true })
  await expect(phonePause).toBeDisabled()
  await expect(phonePause.locator('.pause-reason')).toBeVisible()
  await expect(phonePause.locator('.pause-reason')).toHaveText('Pause and resume for this assistant are not available yet.')
  await page.getByRole('menuitem', { name: 'Edit policy', exact: true }).click()
  const phoneBudget = phonePanel.getByRole('textbox', { name: 'Per week', exact: true })
  await phoneBudget.scrollIntoViewIfNeeded()
  await expectStableControls({ controls: { sheet: phonePanel, save: phonePanel.getByRole('button', { name: /^Save/ }), cancel: phonePanel.getByRole('button', { name: /^Cancel/ }), budget: phoneBudget, tabs: phonePanel.getByRole('tablist') }, scrollAreas: { body: phonePanel.locator('.assistant-panel-scroll') }, interactions: [{ name: 'edit a policy on the phone', run: () => phoneBudget.fill('2') }] })
  await page.screenshot({ path: testInfo.outputPath('aeon-1705-390-dark-sheet-policy-edit-fix1.png'), fullPage: true })
  await phoneBudget.press('ControlOrMeta+Enter')
  await expect(phonePanel.getByRole('status')).toHaveText('Policy saved')
  expect(items[0]!.policy.weekly_budget.amount).toBe('2')
  // Even a principal carrying person-only permission names must use its own
  // self-policy route and never acquire Policy editing controls as an agent.
  const self = items[0]!, agentRequests: string[] = []
  page.on('request', request => { if (new URL(request.url()).pathname.startsWith('/api/assistants')) agentRequests.push(new URL(request.url()).pathname) })
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: self.principal_id, name: self.display_name, kind: 'agent', roles: ['admin'] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  await page.reload()
  const details = page.getByRole('complementary', { name: 'Assistant details' })
  await expect(details).toBeVisible()
  await details.getByRole('tab', { name: 'Policy', exact: true }).click()
  await expect(details).toContainText('Only people with Policy access can change this.')
  await expect(details.getByRole('textbox')).toHaveCount(0)
  await expect(details.getByRole('button', { name: 'More assistant actions' })).toHaveCount(0)
  expect(agentRequests).toContain(`/api/assistants/${self.principal_id}/policy`)
  expect(agentRequests).not.toContain('/api/assistants')
})
