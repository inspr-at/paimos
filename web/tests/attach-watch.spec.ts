// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page, type Locator } from '@playwright/test'
import { openAttachSession } from './agents-menu-fixtures'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents, sessionListReads } from './agents-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

const shots = process.env.ATTACH_SHOTS ?? '/private/tmp/aeon-258-shots'
let calls: Awaited<ReturnType<typeof mockAgents>>
async function setup(page: Page, grant = true, theme: 'light' | 'dark' = 'light') {
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ json: { computers: [] } }))
  await page.route('**/api/me/permissions*', route => {
    const project = new URL(route.request().url()).searchParams.get('project_id') ?? undefined
    const value = mockEffectivePermissions('admin', project)
    value.workspace.permissions = [...value.workspace.permissions, 'account.manage']
    if (grant && value.project) value.project.permissions = [...value.project.permissions, 'harness.watch']
    return route.fulfill({ json: value })
  })
  const data = agentData({
    me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
    tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
    nodes: { 'p-pharos': { key: 'PHAROS', title: 'Pharos' }, 'n-2': { key: 'PHAROS-12', title: 'PDF worker image' } },
  })
  const worker = data.sessions[1]!
  Object.assign(worker, { management_mode: 'unmanaged', advertised_capabilities: [], watch: { request_id: 'attach-fixture', owner_id: me.id, state: 'active', lease_until: new Date(Date.now() + 60_000).toISOString() } })
  data.sessions.splice(0, data.sessions.length, worker)
  data.runs.splice(0); data.approvals.splice(0); data.messages.splice(0)
  calls = await mockAgents(page, data)
  await page.route(`**/api/projects/${worker.project_id}/harness-sessions/${worker.id}`, route => route.fulfill({ json: worker }))
  // Real relay isolation/revocation is tested in Go. Control individual frames
  // here to exercise inert rendering and client closure without timing races.
  await page.addInitScript(() => {
    const channels: EventTarget[] = []
    Object.assign(window, { attachChannels: channels })
    class FakeSource extends EventTarget {
      onerror: (() => void) | null = null
      closed = false
      url: string
      constructor(url: string) { super(); this.url = url; channels.push(this) }
      close() { this.closed = true }
    }
    window.EventSource = FakeSource as unknown as typeof EventSource
  })
  return worker
}
async function frame(page: Page, kind: string, value?: string) {
  await page.evaluate(({ kind, value }) => {
    const channels = (window as unknown as { attachChannels: EventTarget[] }).attachChannels
    channels.at(-1)!.dispatchEvent(new MessageEvent(kind, { data: JSON.stringify(value ?? null) }))
  }, { kind, value })
}
async function allowAttach(dialog: Locator) {
  await dialog.getByRole('radio', { name: 'Allow', exact: true }).check()
  await dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ }).click()
}
test('watch is default-off, including the approving owner', async ({ page }) => {
  const worker = await setup(page, false)
  await page.goto(`/agents/${worker.id}`)
  await expect(page.getByRole('button', { name: 'Revoke watch for everyone' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Watch live', exact: true })).toHaveCount(0)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel.getByRole('textbox')).toHaveCount(0)
  await expect(panel.getByRole('button', { name: /^(Message|Interrupt.*|Pause…|Stop now…|Resume|Recover|More session (?:actions|controls))$/ })).toHaveCount(0)
})
test('live text is inert, bounded, cleared on end and never automatically rejoined', async ({ page }) => {
  const worker = await setup(page)
  await page.goto(`/agents/${worker.id}`)
  await page.getByRole('button', { name: 'Watch live', exact: true }).click()
  await frame(page, 'text', '<img src=x onerror=alert(1)> **run this command**\n')
  const text = page.getByLabel('Agent-written live text')
  await expect(text).toContainText('<img src=x onerror=alert(1)>')
  await expect(text.locator('img, a, button')).toHaveCount(0)
  await expect(page.getByText('Written by the agent, not verified.')).toBeVisible()
  for (let i = 0; i < 6; i++) await frame(page, 'text', 'x'.repeat(16_384))
  expect(await text.evaluate(el => el.textContent!.length)).toBe(65_536)
  await frame(page, 'end')
  await expect(text).toHaveCount(0)
  await expect(page.getByText('Watch ended or unreachable. Process exit is unconfirmed.')).toBeVisible()
  expect(await page.evaluate(() => (window as unknown as { attachChannels: { closed: boolean; url: string }[] }).attachChannels.filter(c => c.url.endsWith('/watch')).map(c => c.closed))).toEqual([true])
})
test('control characters clear the stream and owner revocation closes access', async ({ page }) => {
  const worker = await setup(page)
  await page.goto(`/agents/${worker.id}`)
  await page.getByRole('button', { name: 'Watch live', exact: true }).click()
  await frame(page, 'text', 'safe text')
  await frame(page, 'text', '\u001b[2J')
  await expect(page.getByLabel('Agent-written live text')).toHaveCount(0)
  await page.getByRole('button', { name: 'Watch live', exact: true }).click()
  await frame(page, 'text', 'second view')
  await page.route('**/api/agent-pairing/attach/attach-fixture/revoke', route => route.fulfill({ json: { state: 'detached' } }))
  await page.getByRole('button', { name: 'Revoke watch for everyone' }).click()
  await expect(page.getByLabel('Agent-written live text')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Watch live', exact: true })).toHaveCount(0)
})
test('revoking a watch reads the lists again', async ({ page }) => {
  const worker = await setup(page)
  await page.goto(`/agents/${worker.id}`)
  await page.route('**/api/agent-pairing/attach/attach-fixture/revoke', route => route.fulfill({ json: { state: 'detached' } }))
  await expect(page.getByRole('button', { name: 'Revoke watch for everyone' })).toBeVisible()
  const before = sessionListReads(calls)
  await page.getByRole('button', { name: 'Revoke watch for everyone' }).click()
  await expect(page.getByRole('button', { name: 'Revoke watch for everyone' })).toHaveCount(0)
  await expect.poll(() => sessionListReads(calls)).toBeGreaterThan(before)
  // The refresh returns another row for the same consent. It cannot reopen a
  // watch the owner just revoked, even if its projection still says active.
  await expect(page.getByRole('button', { name: 'Watch live', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Revoke watch for everyone' })).toHaveCount(0)
})

test('approving an attach request reads the lists again', async ({ page }) => {
  await setup(page)
  const review = {
    request_id: 'approve-fixture', request_digest: 'a'.repeat(64), consent_digest: 'b'.repeat(64), consent_mode: 'aeon', state: 'pending', expires_at: new Date(Date.now() + 600_000).toISOString(),
    snapshot: { platform: 'darwin', computer_id: 'computer-fixture', project_id: 'p-pharos', ticket_id: 'n-2', host: 'Markus’s MacBook', harness: 'codex', transcript: '/Users/markus/.codex/sessions/session.jsonl', file_id: '1:234567', process: { pid: 4812, uid: 501, started: '2026-09-29T00:12:30Z', executable: '/opt/homebrew/bin/codex', cwd: '/Users/markus/Code/pharos' } },
  }
  await page.route('**/api/nodes/p-pharos', route => route.fulfill({ json: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' } }))
  await page.route('**/api/nodes/n-2', route => route.fulfill({ json: { id: 'n-2', key: 'PHAROS-12', title: 'PDF worker image' } }))
  await page.route('**/api/agent-pairing/attach/**', route => route.fulfill({ json: route.request().url().endsWith('/lookup') ? review : { ...review, state: 'approved' } }))
  await page.goto('/agents')
  await openAttachSession(page)
  await page.getByLabel('Attach code', { exact: true }).fill('123456789')
  await page.getByRole('button', { name: /Find request/ }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('PDF worker image')
  const before = sessionListReads(calls)
  await allowAttach(dialog)
  await expect(dialog.getByText('Approved. Keep the attach terminal open to share new turns.')).toBeVisible()
  await expect.poll(() => sessionListReads(calls)).toBeGreaterThan(before)
})

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) for (const consent_mode of ['aeon', 'local_auth'] as const) {
  test(`approval and live watch layout ${theme} ${width} ${consent_mode}`, async ({ page }) => {
    const worker = await setup(page, true, theme)
    await page.setViewportSize({ width, height: 1000 })
    await page.goto(`/agents/${worker.id}`)
    await page.getByRole('button', { name: 'Watch live', exact: true }).click()
    await frame(page, 'text', 'Checking the migration fixture.\nThe project binding is unchanged.\nNext: run the targeted tests.\n')
    await expect(page.getByLabel('Agent-written live text')).toBeVisible()
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: `${shots}/watch-${theme}-${width}.png` })
    await page.getByRole('button', { name: 'Close session details' }).click()
    const snapshot = { platform: 'darwin', computer_id: 'computer-fixture', project_id: 'p-pharos', ticket_id: 'n-2', host: 'Markus’s MacBook', harness: 'codex', transcript: '/Users/markus/.codex/sessions/2026/09/29/session.jsonl', file_id: '1:234567', process: { pid: 4812, uid: 501, started: '2026-09-29T00:12:30Z', executable: '/opt/homebrew/bin/codex', cwd: '/Users/markus/Code/pharos' } }
    const review = { consent_mode, consent_digest: 'b'.repeat(64), request_id: 'attach-fixture', request_digest: 'a'.repeat(64), state: 'pending', expires_at: new Date(Date.now() + 600_000).toISOString(), snapshot }
    await page.route('**/api/nodes/p-pharos', route => route.fulfill({ json: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' } }))
    await page.route('**/api/nodes/n-2', route => route.fulfill({ json: { id: 'n-2', key: 'PHAROS-12', title: 'PDF worker image' } }))
    let approved = false
    await page.route('**/api/agent-pairing/attach/**', async route => {
      if (route.request().method() === 'GET') return route.fallback()
      expect(route.request().url()).not.toContain('123456789')
      if (route.request().url().endsWith('/lookup')) {
        expect(route.request().postDataJSON()).toEqual({ user_code: '123456789' })
        return route.fulfill({ json: review })
      }
      expect(route.request().postDataJSON()).toEqual({ request_digest: review.request_digest, consent_digest: review.consent_digest })
      approved = true
      return route.fulfill({ json: { ...review, state: 'approved' } })
    })
    await openAttachSession(page)
    await page.getByLabel('Attach code', { exact: true }).fill('123 456 789')
    await page.getByRole('button', { name: /Find request/ }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Requested by a process on Markus’s MacBook.', { exact: false })).toBeVisible()
    await expect(dialog.getByText('Only allow if you started this watch yourself.')).toBeVisible()
    await expect(dialog.getByText('Watch the conversation', { exact: true })).toBeVisible()
    await expect(dialog.getByText('Status only (no conversation text)', { exact: true })).toHaveCount(0)
    await expect(dialog.getByText('PID 4812 · UID 501')).toBeVisible()
    // The actual bytes being approved must be visible without opening details.
    await expect(dialog.locator('details')).not.toHaveAttribute('open', '')
    await expect(dialog.locator('.margin').getByText(snapshot.transcript, { exact: true })).toBeVisible()
    await expect(dialog.locator('.margin').getByText(snapshot.file_id, { exact: true })).toBeVisible()
    await expect(dialog.getByRole('radio', { name: 'Allow', exact: true })).toBeVisible()
    await expect(dialog.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeDisabled()
    await expect(dialog).not.toContainText('Linux and a headless Mac keep this approval')
    await expect(dialog).toContainText('PDF worker image')
    expect(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).include('dialog').analyze()).violations).toEqual([])
    await page.screenshot({ path: `${shots}/approval-${consent_mode}-${theme}-${width}.png` })
    await allowAttach(dialog)
    await expect(dialog.getByText(consent_mode === 'local_auth' ? 'Waiting for confirmation on Markus’s MacBook. Nothing is shared until you confirm there.' : 'Approved. Keep the attach terminal open to share new turns.')).toBeVisible()
    expect(approved).toBe(true)
  })
}

for (const role of ['owner', 'admin'] as const) test(`only an owner can explicitly delegate viewing without having it: ${role}`, async ({ page }) => {
  const { accessWorld, mockAccess, REGISTRY } = await import('./access-fixtures')
  await mockWork(page, fixtures())
  await mockAccess(page, accessWorld({ role }))
  await page.route('**/api/authz/permissions', route => route.fulfill({ json: [...REGISTRY, { key: 'harness.watch', group: 'Agents', description: 'View new conversation turns', risk: 'high', grantable_at: ['workspace', 'project'], agent_grantable: false }] }))
  await page.goto('/settings/access/roles/new')
  const permission = page.getByRole('checkbox', { name: /View live conversations/ })
  if (role === 'owner') {
    await expect(permission).toBeEnabled()
    await permission.check()
    await expect(permission).toBeChecked()
  } else await expect(permission).toBeDisabled()
})

test('strict approval cannot proceed on a Linux daemon', async ({ page }) => {
  await setup(page)
  await page.route('**/api/agent-pairing/attach/lookup', route => route.fulfill({ json: {
    request_id: 'linux-fixture', request_digest: 'a'.repeat(64), consent_digest: 'b'.repeat(64), consent_mode: 'local_auth', state: 'pending', expires_at: new Date(Date.now() + 600_000).toISOString(),
    snapshot: { platform: 'linux', computer_id: 'computer-fixture', project_id: 'p-pharos', ticket_id: 'n-2', host: 'Linux workstation', harness: 'codex', transcript: '/home/owner/session.jsonl', file_id: '1:234', process: { pid: 4812, uid: 1000, started: 'fixture', executable: '/usr/bin/codex', cwd: '/home/owner/work' } },
  } }))
  await page.goto('/agents')
  await openAttachSession(page)
  await page.getByLabel('Attach code', { exact: true }).fill('123456789')
  await page.getByRole('button', { name: /Find request/ }).click()
  await expect(page.getByRole('dialog')).toContainText('Local confirmation is unavailable on this computer')
  await expect(page.getByRole('dialog')).not.toContainText('Linux and a headless Mac keep this approval')
  await expect(page.getByRole('dialog').getByRole('radio', { name: 'Allow', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: /^Decide(?: & next)?(?:\s|$)/ })).toBeDisabled()
  await expect(page.getByRole('dialog').getByRole('radio', { name: 'Decline', exact: true })).toBeEnabled()
})

test('an Aeon approval on Linux says that computer keeps this approval', async ({ page }) => {
  await setup(page)
  await page.route('**/api/agent-pairing/attach/lookup', route => route.fulfill({ json: {
    request_id: 'linux-fixture', request_digest: 'a'.repeat(64), consent_digest: 'b'.repeat(64), consent_mode: 'aeon', state: 'pending', expires_at: new Date(Date.now() + 600_000).toISOString(),
    snapshot: { platform: 'linux', computer_id: 'computer-fixture', project_id: 'p-pharos', ticket_id: 'n-2', host: 'Linux workstation', harness: 'codex', transcript: '/home/owner/session.jsonl', file_id: '1:234', process: { pid: 4812, uid: 1000, started: 'fixture', executable: '/usr/bin/codex', cwd: '/home/owner/work' } },
  } }))
  await page.route('**/api/nodes/p-pharos', route => route.fulfill({ json: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' } }))
  await page.route('**/api/nodes/n-2', route => route.fulfill({ json: { id: 'n-2', key: 'PHAROS-12', title: 'PDF worker image' } }))
  await page.goto('/agents')
  await openAttachSession(page)
  await page.getByLabel('Attach code', { exact: true }).fill('123456789')
  await page.getByRole('button', { name: /Find request/ }).click()
  await expect(page.getByRole('dialog')).toContainText('This computer keeps approval in AEON.')
})

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) for (const consent_mode of ['aeon', 'local_auth'] as const) {
  test(`metadata-only attach approval and status ${theme} ${width} ${consent_mode}`, async ({ page }) => {
    const worker = await setup(page, true, theme)
    Object.assign(worker.watch!, { mode: 'lease' })
    await page.setViewportSize({ width, height: 1000 })
    await page.goto(`/agents/${worker.id}`)
    await expect(page.getByRole('heading', { name: 'Attached session' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Watch live', exact: true })).toHaveCount(0)
    expect(await page.evaluate(() => (window as unknown as { attachChannels: { url: string }[] }).attachChannels.filter(c => c.url.endsWith('/watch')).length)).toBe(0)
    await page.getByRole('button', { name: 'Close session details' }).click()
    const review = {
      request_id: 'lease-fixture', request_digest: 'a'.repeat(64), consent_digest: 'b'.repeat(64), consent_mode, state: 'pending', expires_at: new Date(Date.now() + 600_000).toISOString(),
      snapshot: { mode: 'lease', platform: 'darwin', computer_id: 'computer-fixture', project_id: 'p-pharos', ticket_id: 'n-2', host: 'Markus’s MacBook', harness: 'codex', transcript: '', file_id: '', process: { pid: 4812, uid: 501, started: '2026-09-29T17:00:00Z', executable: '/opt/homebrew/bin/codex', cwd: '/Users/markus/Code/pharos' } },
    }
    await page.route('**/api/nodes/p-pharos', route => route.fulfill({ json: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' } }))
    await page.route('**/api/nodes/n-2', route => route.fulfill({ json: { id: 'n-2', key: 'PHAROS-12', title: 'PDF worker image' } }))
    let approvals = 0
    await page.route('**/api/agent-pairing/attach/**', async route => {
      if (route.request().method() === 'GET') return route.fallback()
      expect(route.request().url()).not.toContain('123456789')
      if (route.request().url().endsWith('/lookup')) {
        expect(route.request().postDataJSON()).toEqual({ user_code: '123456789' })
        return route.fulfill({ json: review })
      }
      expect(route.request().postDataJSON()).toEqual({ request_digest: review.request_digest, consent_digest: review.consent_digest })
      approvals++
      return route.fulfill({ json: { ...review, state: 'approved' } })
    })
    await openAttachSession(page)
    await page.getByLabel('Attach code', { exact: true }).fill('123 456 789')
    await page.getByRole('button', { name: /Find request/ }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Attach a running session' })).toBeVisible()
    await expect(dialog).toContainText('PDF worker image')
    await expect(dialog).toContainText('Session status only; no conversation text is read or shared.')
    await expect(dialog.getByText('Status only (no conversation text)', { exact: true })).toBeVisible()
    await expect(dialog.getByText('Watch the conversation', { exact: true })).toHaveCount(0)
    await expect(dialog).not.toContainText('Linux and a headless Mac keep this approval')
    await expect(dialog).toContainText('Requested by a process on Markus’s MacBook.')
    await expect(dialog.getByText('Transcript', { exact: true })).toHaveCount(0)
    await expect(dialog.getByText('New turns will be visible', { exact: false })).toHaveCount(0)
    expect(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).include('dialog').analyze()).violations).toEqual([])
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: `${shots}/lease-approval-${consent_mode}-${theme}-${width}.png` })
    await allowAttach(dialog)
    await expect(dialog).toContainText(consent_mode === 'local_auth' ? 'Waiting for confirmation on Markus’s MacBook. Nothing is shared until you confirm there.' : 'Approved. Keep the attach terminal open to report session status.')
    await expect(dialog.getByRole('radio', { name: 'Allow', exact: true })).toHaveCount(0)
    await expect(dialog.getByRole('button', { name: 'Detach session', exact: true })).toBeVisible()
    expect(approvals).toBe(1)
  })
}

for (const state of ['detached', 'unreachable', 'confirmed_exited'] as const) {
  test(`metadata-only terminal state ${state}`, async ({ page }) => {
    const worker = await setup(page, true)
    Object.assign(worker.watch!, { mode: 'lease', state: state === 'confirmed_exited' ? 'detached' : state, ...(state === 'confirmed_exited' ? { process_state: 'confirmed_exited' } : {}) })
    await page.goto(`/agents/${worker.id}`)
    await expect(page.getByRole('heading', { name: 'Attached session' })).toBeVisible()
    await expect(page.getByText(state === 'confirmed_exited' ? 'Process exit confirmed.' : `Session ${state}. Process exit is unconfirmed.`)).toBeVisible()
    await expect(page.getByRole('button', { name: 'Watch live', exact: true })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Detach session', exact: true })).toHaveCount(0)
  })
}

for (const state of ['detached', 'unreachable', 'confirmed_exited'] as const) {
  test(`conversation watch terminal state ${state}`, async ({ page }) => {
    const worker = await setup(page, true)
    Object.assign(worker.watch!, { state: state === 'confirmed_exited' ? 'detached' : state, ...(state === 'confirmed_exited' ? { process_state: 'confirmed_exited' } : {}) })
    await page.goto(`/agents/${worker.id}`)
    await expect(page.getByRole('heading', { name: 'Live conversation' })).toBeVisible()
    await expect(page.getByText(state === 'confirmed_exited' ? 'Process exit confirmed.' : `Watch ${state}. Process exit is unconfirmed.`)).toBeVisible()
    await expect(page.getByRole('button', { name: 'Watch live', exact: true })).toHaveCount(0)
  })
}
