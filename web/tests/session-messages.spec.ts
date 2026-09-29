// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const now = Date.parse('2026-09-28T20:00:00Z')
async function setup(page: Page, theme: 'light' | 'dark' = 'light') {
  await page.clock.install({ time: now })
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
  const worker = data.sessions[0]!
  Object.assign(worker, { display_label: 'Current lead', run_id: null })
  data.sessions.splice(1); data.runs.splice(0); data.approvals.splice(0)
  const original = data.messages[1]!
  data.messages.splice(0, data.messages.length,
    { ...original, id: 'history', body: 'Old unbound status', created_at: new Date(now - 120_000).toISOString(), sender_label: 'paimos:Previous lead', from: 'paimos:Previous lead' } as typeof original,
    { ...original, id: 'other', body: 'Stop the ghost session', recipient_session_id: 'other-session', sender_session_id: undefined, sender_label: 'Markus', sender_principal_id: me.id, recipient_principal_id: worker.agent_principal_id } as typeof original,
    { ...original, id: 'current1', sent_event_id: 201, body: 'The release checks are ready.', sender_session_id: worker.id, sender_label: 'Original lead', created_at: new Date(now - 60_000).toISOString() } as typeof original,
    { ...original, id: 'current2', sent_event_id: 202, body: 'The release checks are ready.', sender_session_id: worker.id, sender_label: 'Original lead', created_at: new Date(now - 30_000).toISOString() } as typeof original,
  )
  const calls = await mockAgents(page, data)
  return { data, worker, calls }
}

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`${theme} ${width}: session thread excludes other sessions and collapses consecutive duplicates`, async ({ page }) => {
    const { worker } = await setup(page, theme)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto(`/agents/${worker.id}`)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    // Messages live in their own tab (AEON-273).
    await panel.getByRole('tab', { name: /Messages/ }).click()
    const thread = panel.getByRole('list', { name: 'Messages', exact: true })
    await expect(thread.getByRole('listitem')).toHaveCount(1)
    await expect(thread).toContainText('Original lead')
    await expect(thread).toContainText('×2')
    await expect(panel.getByText('Old unbound status')).not.toBeVisible()
    await expect(panel.getByText('Stop the ghost session')).not.toBeVisible()
    await panel.locator('.session-messages').scrollIntoViewIfNeeded()
    expect(await panel.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    const dir = process.env.SESSION_MESSAGES_SHOTS
    if (dir) { mkdirSync(dir, { recursive: true }); await page.screenshot({ path: resolve(dir, `${theme}-${width}.png`), fullPage: true }) }
    await expect(panel.getByText(/Other sessions of/)).toHaveCount(0)
    await expect(panel.getByRole('region', { name: 'Inbox attention' })).toHaveCount(0)
  })
}

test('send targets the selected generation and a concurrent stop disables the composer', async ({ page }) => {
  const { worker } = await setup(page)
  let sent: Record<string, unknown> | undefined
  await page.route('**/api/projects/*/messages', route => {
    if (route.request().method() !== 'POST') return route.fallback()
    sent = route.request().postDataJSON()
    return route.fulfill({ status: 409, json: { code: 'session_ended', message: 'This session has ended.' } })
  })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await panel.getByRole('textbox', { name: 'Message to Current lead' }).fill('Only this session')
  await panel.getByRole('button', { name: 'Send', exact: true }).click()
  expect(sent?.recipient_session_id).toBe(worker.id)
  await expect(panel.getByText('This session has ended.', { exact: true })).toHaveCount(1)
  await expect(panel.getByRole('button', { name: 'Send', exact: true })).toHaveCount(0)
})

for (const width of [1600, 390]) {
  test(`a live session with no target can be messaged, and only that session receives it (${width})`, async ({ page }) => {
    await page.clock.install({ time: now })
    const work = fixtures(); work.preferences.theme = { choice: 'light' }
    await mockWork(page, work, { admin: true })
    const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
    const first = data.sessions[0]!
    Object.assign(first, { display_label: 'aithema-4c', role: 'worker', run_id: null, ticket_node_id: null, phase: 'working', activity: 'idle', heartbeat_at: new Date(now).toISOString(), stopped_at: null, stop_reason: null, management_mode: 'managed', advertised_capabilities: ['inbox', 'status'] })
    const second = { ...first, id: '5e000000-0000-4000-8000-000000000099', display_label: 'aithema-other', created_at: new Date(now - 60_000).toISOString() }
    data.sessions.splice(0, data.sessions.length, first, second)
    data.targets.splice(0); data.messages.splice(0); data.approvals.splice(0); data.runs.splice(0)
    await mockAgents(page, data)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto(`/agents/${first.id}?tab=messages`)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    const composer = panel.getByRole('textbox', { name: 'Message to aithema-4c' })
    await expect(composer).toBeEnabled()
    await expect(panel.getByText('has no message address')).toHaveCount(0)
    await expect(panel.getByText("delivered when the session's inbox hook runs")).toHaveCount(0)
    const [request] = await Promise.all([
      page.waitForRequest(r => r.method() === 'POST' && r.url().includes('/messages')),
      composer.fill('Only this generation').then(() => panel.getByRole('button', { name: 'Send', exact: true }).click()),
    ])
    expect(request.postDataJSON()).toMatchObject({ to: first.agent_principal_id, recipient_session_id: first.id, body: 'Only this generation' })
    await expect(panel.getByRole('list', { name: 'Messages', exact: true })).toContainText('Only this generation')
    await expect(panel.getByRole('status').filter({ hasText: "delivered when the session's inbox hook runs" })).toHaveText("Delivered when the session's inbox hook runs.")
    expect(await panel.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.goto(`/agents/${second.id}?tab=messages`)
    const other = page.getByRole('complementary', { name: 'Session details' })
    await expect(other.getByRole('textbox', { name: 'Message to aithema-other' })).toBeEnabled()
    await expect(other.getByText('Only this generation')).toHaveCount(0)
    await expect(other.getByText("delivered when the session's inbox hook runs")).toHaveCount(0)
  })
}

for (const older of ['delivered', 'read'] as const) {
  test(`an older ${older} message does not hide the hook wait for a new pending send`, async ({ page }) => {
    await page.clock.install({ time: now })
    const work = fixtures(); work.preferences.theme = { choice: 'light' }
    await mockWork(page, work, { admin: true })
    const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
    const first = data.sessions[0]!
    Object.assign(first, { display_label: 'review-worker', role: 'worker', run_id: null, ticket_node_id: null, phase: 'working', activity: 'idle', heartbeat_at: new Date(now).toISOString(), stopped_at: null, stop_reason: null, management_mode: 'unmanaged', advertised_capabilities: ['inbox', 'status'], inbox_seen_via: 'drain' })
    const template = data.messages[1]!
    const earlier = '3e000000-0000-4000-8000-000000000007'
    data.sessions.splice(0, data.sessions.length, first)
    data.targets.splice(0); data.approvals.splice(0); data.runs.splice(0)
    data.messages.splice(0, data.messages.length, { ...template, id: earlier, body: 'Earlier message', sender_principal_id: me.id, recipient_principal_id: first.agent_principal_id, recipient_session_id: first.id, sender_session_id: undefined, sender_label: 'Markus', to: first.agent_principal_id, project: first.project_id, sent_event_id: 200, created_at: new Date(now - 60_000).toISOString() })
    await mockAgents(page, data)
    await page.route('**/api/inbox/message-status?*', route => {
      const ids = new URL(route.request().url()).searchParams.get('ids')!.split(',')
      return route.fulfill({ json: { items: ids.map(id => ({ message_id: id, status: id === earlier ? older : 'sent', delivered_at: id === earlier ? new Date(now - 60_000).toISOString() : null, read_at: id === earlier && older === 'read' ? new Date(now - 30_000).toISOString() : null, deliver_by: new Date(now + 240_000).toISOString() })) } })
    })
    await page.goto(`/agents/${first.id}?tab=messages`)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel.locator(`.delivery.${older}`)).toHaveCount(1)
    await panel.getByRole('textbox', { name: 'Message to review-worker' }).fill('New message waiting')
    await panel.getByRole('button', { name: 'Send', exact: true }).click()
    await expect(panel.getByRole('list', { name: 'Messages', exact: true })).toContainText('New message waiting')
    await expect(panel.locator('.delivery.sent')).toHaveCount(1)
    await expect(panel.locator(`.delivery.${older}`)).toHaveCount(1)
    await expect(panel.getByRole('status').filter({ hasText: "Delivered when the session's inbox hook runs." })).toHaveText("Delivered when the session's inbox hook runs.")
  })
}

test('a send marked not_delivered shows that failure instead of the hook wait', async ({ page }) => {
  await page.clock.install({ time: now })
  const work = fixtures(); work.preferences.theme = { choice: 'light' }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
  const first = data.sessions[0]!
  Object.assign(first, { display_label: 'review-worker', role: 'worker', run_id: null, ticket_node_id: null, phase: 'working', activity: 'idle', heartbeat_at: new Date(now).toISOString(), stopped_at: null, stop_reason: null, management_mode: 'unmanaged', advertised_capabilities: ['inbox', 'status'] })
  data.sessions.splice(0, data.sessions.length, first)
  data.targets.splice(0); data.messages.splice(0); data.approvals.splice(0); data.runs.splice(0)
  await mockAgents(page, data)
  await page.route('**/api/inbox/message-status?*', route => {
    const ids = new URL(route.request().url()).searchParams.get('ids')!.split(',')
    return route.fulfill({ json: { items: ids.map(id => ({ message_id: id, status: 'not_delivered', reason: 'deadline', delivered_at: null, read_at: null, deliver_by: new Date(now + 240_000).toISOString() })) } })
  })
  await page.goto(`/agents/${first.id}?tab=messages`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await panel.getByRole('textbox', { name: 'Message to review-worker' }).fill('New message waiting')
  await panel.getByRole('button', { name: 'Send', exact: true }).click()
  await expect(panel.getByRole('list', { name: 'Messages', exact: true })).toContainText('New message waiting')
  await expect(panel.locator('[data-status=not_delivered]')).toHaveCount(1)
  await expect(panel.getByText('Not delivered · not confirmed in time')).toBeVisible()
  await expect(panel.getByRole('status').filter({ hasText: "Delivered when the session's inbox hook runs." })).toHaveCount(0)
})

test('a stopped session has no active Send action', async ({ page }) => {
  const { worker } = await setup(page)
  Object.assign(worker, { phase: 'stopped', stopped_at: new Date(now).toISOString(), stop_reason: 'done' })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel.getByText('This session has ended.', { exact: true })).toBeVisible()
  await expect(panel.getByRole('textbox')).toHaveCount(0)
})
