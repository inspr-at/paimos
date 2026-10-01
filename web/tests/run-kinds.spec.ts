// SPDX-License-Identifier: AGPL-3.0-only
// Capture the actual /agents page/component on CI with deterministic API fixtures.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const now = Date.parse('2026-10-01T14:00:00Z')
const before = process.env.AEON_501_CAPTURE === 'before'

test('execution kinds and person-specific host names on the real agents table', async ({ page }, info) => {
  await page.clock.install({ time: now })
  await page.setViewportSize({ width: 1600, height: 1000 })
  const work = fixtures()
  await mockWork(page, work, { admin: true })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: { 'n-1': { key: 'PHAROS-42', title: 'Ship the execution kinds' } } })
  const lead = data.sessions[0]!
  Object.assign(lead, { display_label: 'Release coordinator', model: 'claude-fixture', role: 'coordinator', harness: 'claude', run_id: null, host: 'mbp2607', progress_pct: 50, eta_live_at: new Date(now + 20 * 60_000).toISOString(), heartbeat_at: new Date(now - 5_000).toISOString() })
  const ai = { ...data.sessions[1]!, run_id: null, ticket_node_id: lead.ticket_node_id, ticket: lead.ticket, parent_harness_session_id: lead.id, display_label: 'AI worker', harness: 'codex', model: 'gpt-fixture', reasoning_effort: 'high', host: 'mbp2606', phase: 'working', activity: 'busy', progress_pct: 20, eta_ready_at: new Date(now + 10 * 60_000).toISOString() }
  const xai = { ...ai, id: '52000000-0000-4000-8000-000000000001', display_label: 'xAI worker', harness: 'grok', model: 'grok-fixture' }
  const media = { ...ai, id: '52000000-0000-4000-8000-000000000002', display_label: 'Media worker', harness: 'media', model: null, reasoning_effort: null, generator: 'higgsfield/kling3_0', progress_pct: 40 }
  const terminal = { ...ai, id: '52000000-0000-4000-8000-000000000003', display_label: 'Terminal worker', harness: 'terminal', model: null, reasoning_effort: null, command: 'ffmpeg', progress_pct: 90 }
  data.sessions.splice(0, data.sessions.length, lead, ai, xai, media, terminal)
  data.runs.splice(0); data.approvals.splice(0); data.messages.splice(0); data.targets.splice(0)
  await mockAgents(page, data)
  const names = new Map<string, string>()
  await page.route('**/api/me/host-labels', async route => {
    if (route.request().method() === 'PUT') {
      const { host, label } = route.request().postDataJSON()
      if (label === null) names.delete(host); else names.set(host, label)
      await route.fulfill({ json: { host, label: label ?? host } })
    } else await route.fulfill({ json: [...names].map(([host, label]) => ({ host, label })) })
  })
  await page.goto('/agents')
  const accounts = page.getByRole('button', { name: /^Accounts and computers/ })
  if (await accounts.getAttribute('aria-expanded') === 'true') await accounts.click()
  await expect(page.locator(`[data-row="s:${ai.id}"]`)).toBeVisible()
  const table = page.getByRole('table', { name: 'Agent sessions' })
  if (!before) {
    await expect(table.getByRole('columnheader').filter({ hasText: /^Name/ })).toBeVisible()
    await expect(table.getByRole('columnheader').filter({ hasText: /^Host/ })).toBeVisible()
    await expect(page.locator(`[data-row="s:${media.id}"] .c-exec`)).toContainText('higgsfield/kling3_0')
    await expect(page.locator(`[data-row="s:${terminal.id}"] .c-exec`)).toContainText('ffmpeg')
    await expect(page.locator('[data-run-kind="media"]')).toHaveCount(1)
    await expect(page.locator('[data-run-kind="terminal"]')).toHaveCount(1)
    const slots = await page.locator('.exec-icon').evaluateAll(items => items.map(item => {
      const rect = item.getBoundingClientRect()
      return { width: rect.width, height: rect.height, border: getComputedStyle(item).borderWidth }
    }))
    for (const slot of slots) { expect(slot.width).toBe(slot.height); expect(slot.border).toBe('0px') }
    const labelLefts = await page.locator('.exec-copy').evaluateAll(items => items.map(item => item.getBoundingClientRect().left))
    expect(new Set(labelLefts).size).toBe(1)
    const workerHost = page.locator(`[data-row="s:${ai.id}"] .host-badge`)
    const original = await workerHost.boundingBox()
    await workerHost.hover()
    const pencil = page.locator(`[data-row="s:${ai.id}"] .host-pencil`)
    await expect(pencil).toHaveCSS('opacity', '1')
    expect((await pencil.boundingBox())!.x).toBeGreaterThanOrEqual(original!.x + original!.width)
    expect(await workerHost.boundingBox()).toEqual(original)
    await workerHost.click()
    const dialog = page.getByRole('dialog', { name: 'Your name for this computer' })
    await expect(dialog.getByRole('textbox')).toBeEnabled()
    await dialog.getByRole('textbox').fill('Cancelled name')
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(workerHost).toContainText('mbp2606')
    expect(names.size).toBe(0)
    await workerHost.click()
    await expect(dialog.getByRole('textbox')).toBeEnabled()
    await dialog.getByRole('textbox').fill("David's MacBook")
    await dialog.getByRole('button', { name: 'Save', exact: true }).click()
    for (const child of [ai, xai, media, terminal]) await expect(page.locator(`[data-row="s:${child.id}"] .host-badge`)).toContainText("David's MacBook")
    await expect(page.locator(`[data-row="s:${lead.id}"] .host-badge`)).toContainText('mbp2607')
    expect((await workerHost.boundingBox())?.width).toBe(original?.width)
    await workerHost.click()
    await expect(dialog.getByRole('button', { name: "Use 'mbp2606'" })).toBeEnabled()
    await dialog.getByRole('button', { name: "Use 'mbp2606'" }).click()
    await expect(workerHost).toContainText('mbp2606')
    // Long labels remain within the exact same badge, visibly ellipsised.
    await workerHost.click()
    await expect(dialog.getByRole('textbox')).toBeEnabled()
    await dialog.getByRole('textbox').fill('A'.repeat(128))
    await dialog.getByRole('button', { name: 'Save', exact: true }).click()
    expect(await workerHost.locator('.host-name').evaluate(el => el.scrollWidth > el.clientWidth && getComputedStyle(el).textOverflow === 'ellipsis')).toBe(true)
    await workerHost.click()
    await expect(dialog.getByRole('button', { name: "Use 'mbp2606'" })).toBeEnabled()
    await dialog.getByRole('button', { name: "Use 'mbp2606'" }).click()
  }
  // Keep the same row focused in both captures so the existing tint is comparable.
  await page.locator(`[data-row="s:${ai.id}"]`).focus()
  await page.mouse.move(0, 0)
  const root = process.env.AEON_501_SHOTS
  if (root) {
    mkdirSync(root, { recursive: true })
    const layout = await page.locator('[data-row^="s:"]').evaluateAll(items => items.map(item => {
      const row = item.getBoundingClientRect()
      const state = item.querySelector('.c-state')!.getBoundingClientRect()
      return { id: item.getAttribute('data-row'), height: row.height, stateLeft: state.left }
    }))
    const path = join(root, 'before-layout.json')
    if (before) writeFileSync(path, JSON.stringify(layout))
    else {
      const baseline = JSON.parse(readFileSync(path, 'utf8')) as typeof layout
      for (const row of layout) {
        const original = baseline.find(item => item.id === row.id)!
        expect(row.height, `${row.id} retains the base row height`).toBeCloseTo(original.height, 1)
        expect(row.stateLeft, `${row.id} retains the base state position`).toBe(original.stateLeft)
      }
    }
  }
  await page.screenshot({ path: root ? join(root, `${before ? 'before' : 'after'}.png`) : info.outputPath(`${before ? 'before' : 'after'}.png`), fullPage: true, animations: 'disabled' })
  await page.locator('.sessions').screenshot({ path: root ? join(root, `${before ? 'before' : 'after'}-sessions.png`) : info.outputPath(`${before ? 'before' : 'after'}-sessions.png`), animations: 'disabled' })
})
