// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { fixtures, mockWork } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { expectStableControls } from './helpers/stable'
import type { AutopilotSettings, AutopilotProposal, AutomaticChange, ProjectOverride } from '../src/lib/statusAutopilot'

async function setup(page: Page, role: 'admin' | 'member' = 'admin') {
  await mockWork(page, fixtures(), { admin: role === 'admin' })
  await mockBusiness(page, businessData({ role }), { role })
  await mockSettings(page, settingsData())
  // The business fixture freezes Date.now(). TicketWorkspace's capture
  // listener and Vue's target listener need an advancing event clock.
  await page.clock.setSystemTime(new Date('2026-10-01T18:00:00Z'))
  await page.route('**/api/settings/model-provider', route => route.fulfill({ json: { enabled: false, base_url: '', chat_model: '', embedding_model: '', features: { crm_note_rewrite: false, embeddings: false }, provider_id: '', revision: 0, has_api_key: false } }))
  const settings: AutopilotSettings = { enabled: true, revision: 0, rules: { new: { enabled: true, days: 7 }, backlog: { enabled: true, days: 90 }, blocked: { enabled: true, days: 14 }, progress: { enabled: true, days: 3 }, done: { enabled: true, days: 14 }, publish: { enabled: true }, accept: { enabled: true, days: 30 } } }
  const changes: AutomaticChange[] = [{ event_id: 41, node_id: 'ticket-1', key: 'ORB-142', title: 'Touch ID sign-in for the desktop app', actor: 'Status autopilot', rule: 'progress', reason: 'No session, branch or PR activity for 3 days.', from: 'in_progress', to: 'open', at: '2026-10-01T17:00:00Z', undone: false, undoable: true }]
  const suggestions: AutomaticChange[] = ['triage_list', 'cancel_suggested', 'blocked_reminder', 'missed_release'].map((flag, i) => ({ ...changes[0]!, event_id: 100 + i, key: `ORB-${i + 1}`, rule: (['new', 'backlog', 'blocked', 'done'] as const)[i]!, from: ['new', 'backlog', 'blocked', 'done'][i]!, to: flag, reason: `Current ${flag}`, at: '2026-01-01T00:00:00Z', undoable: false, changed_since: true }))
  const overrides: Record<string, ProjectOverride> = {}
  const proposals: AutopilotProposal[] = []
  const resolutions: unknown[] = []
  const writes: unknown[] = []; let conflict = false; let undo = 0
  await page.route('**/api/settings/status-autopilot', async route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON(); writes.push(body)
      if (conflict) return route.fulfill({ status: 409, json: { error: 'settings changed' } })
      Object.assign(settings, { enabled: body.enabled, rules: body.rules, revision: settings.revision + 1 })
      if (body.confirm_upgrade) Object.assign(settings, { suggest_until: null, effective_mode: settings.server_mode === 'suggest' ? 'suggest' : 'on' })
    }
    return route.fulfill({ json: settings })
  })
  await page.route('**/api/projects/*/status-autopilot', async route => {
    const id = route.request().url().split('/').at(-2)!
    const o = overrides[id] ??= { mode: 'inherit', effective_enabled: true, revision: 0 }
    if (route.request().method() === 'PUT') { const body = route.request().postDataJSON(); o.mode = body.mode; o.revision++ }
    o.effective_enabled = o.mode === 'on' || o.mode === 'inherit' && settings.enabled
    return route.fulfill({ json: o })
  })
  await page.route('**/api/status-autopilot/changes*', route => route.fulfill({ json: { items: route.request().url().includes('suggestions=true') ? suggestions : changes } }))
  await page.route('**/api/status-autopilot/proposals', route => route.fulfill({ json: { items: proposals } }))
  await page.route('**/api/status-autopilot/proposals/*', route => {
    resolutions.push(route.request().postDataJSON())
    const id = Number(route.request().url().split('/').at(-1))
    const index = proposals.findIndex(proposal => proposal.event_id === id)
    if (index >= 0) proposals.splice(index, 1)
    return route.fulfill({ json: {} })
  })
  await page.route('**/api/events/41/undo', route => { undo++; changes[0]!.undone = true; changes[0]!.undoable = false; return route.fulfill({ status: 201, json: { id: 42 } }) })
  return { settings, suggestions, proposals, resolutions, writes, conflict: () => { conflict = true }, undos: () => undo }
}

test('250 attention ticket pills stay steady for 3 seconds with bounded lookups', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const world = await setup(page)
  const base = world.suggestions[0]!
  world.suggestions.splice(0, world.suggestions.length, ...Array.from({ length: 250 }, (_, i) => ({
    ...base, event_id: 1000 + i, node_id: `pill-${i}`, key: `AEON-${1001 + i}`,
    title: `Langfristige Überprüfung der Zugriffsberechtigungen und nachvollziehbare Freigabe ${i + 1}`,
  })))
  const lookups: string[][] = []
  await page.route('**/api/nodes/lookup**', route => {
    const keys = (new URL(route.request().url()).searchParams.get('keys') ?? '').split(',').filter(Boolean)
    if (!keys.length) return route.fallback()
    lookups.push(keys)
    return route.fulfill({ json: { items: keys.map(key => ({ requested_key: key, key, id: `pill-${key}`, title: key, state: 'new', project_id: 'p-aeon' })) } })
  })
  await page.goto('/settings/workspace')
  const attention = page.getByRole('region', { name: 'Tickets needing attention', exact: true })
  await expect(attention.locator('a.ticket-link')).toHaveCount(250)
  await expect(attention.locator('.ticket-plain')).toHaveCount(0)
  const before = lookups.length
  // Observe replacements as well as class mutations: a flickering link may be
  // removed and recreated rather than have its class changed in place.
  const observation = await attention.evaluate(async region => {
    const snapshot = () => [...region.querySelectorAll('.key-badge')].map(el => `${el.tagName}:${el.textContent}:${el.className}`)
    const baseline = snapshot()
    let mutations = 0
    const observer = new MutationObserver(records => {
      mutations += records.filter(record => record.type === 'attributes' || [...record.addedNodes, ...record.removedNodes].some(node => node instanceof Element && (node.matches('.key-badge') || node.querySelector('.key-badge')))).length
    })
    observer.observe(region, { subtree: true, childList: true, attributes: true, attributeFilter: ['class'] })
    await new Promise(resolve => setTimeout(resolve, 3000))
    observer.disconnect()
    return { baseline, after: snapshot(), mutations }
  })
  expect(observation.baseline).toHaveLength(250)
  expect(observation.after).toEqual(observation.baseline)
  expect(observation.mutations).toBe(0)
  expect(lookups.length).toBe(before)
  expect(lookups.length).toBeLessThanOrEqual(4) // 250 suggestions plus the recent-change key.
  expect(lookups.every(keys => keys.length <= 100)).toBe(true)
  expect(world.suggestions.every(item => lookups.flat().filter(key => key === item.key).length === 1)).toBe(true)

  const first = attention.locator('a.ticket-link').first()
  await first.scrollIntoViewIfNeeded()
  await expectStableControls({
    controls: { ticket: first, row: attention.locator('.change').first() }, scrollAreas: { attention },
    interactions: [{ name: 'hover ticket', run: () => first.hover() }, { name: 'focus ticket', run: () => first.focus() }],
  })
  const folder = testInfo.outputPath('aeon-668-pills')
  await mkdir(folder, { recursive: true })
  for (const width of [390, 1024, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await first.scrollIntoViewIfNeeded()
    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
      const path = `${folder}/attention-${width}-${theme}.png`
      await page.screenshot({ path })
      await testInfo.attach(`attention-${width}-${theme}`, { path, contentType: 'image/png' })
    }
  }
})

test('upgrade proposals offer Apply and Dismiss while stale proposals stay guarded', async ({ page }) => {
  const world = await setup(page)
  Object.assign(world.settings, { server_mode: 'on', effective_mode: 'suggest', suggest_until: '2026-10-02T18:00:00Z' })
  const base: AutopilotProposal = { event_id: 200, node_id: 'ticket-1', key: 'ORB-142', title: 'Review before moving', rule: 'progress', reason: 'No session, branch or PR activity for 3 days.', from: 'in_progress', to: 'open', at: '2026-10-01T17:00:00Z', changed_since: false, applicable: true }
  world.proposals.push(base, { ...base, event_id: 201, key: 'ORB-143', changed_since: true, applicable: false })
  await page.goto('/settings/workspace')
  const card = page.getByRole('region', { name: 'Status autopilot', exact: true })
  await expect(card.getByText('Suggest', { exact: true })).toBeVisible()
  await expect(card.getByText(/After this upgrade/)).toBeVisible()
  expect(world.writes).toHaveLength(0)
  const changes = page.getByRole('region', { name: 'Changes', exact: true })
  await expect(changes.getByRole('button', { name: 'Apply proposal for ORB-143', exact: true })).toBeDisabled()
  await changes.getByRole('button', { name: 'Apply proposal for ORB-142', exact: true }).click()
  await expect(changes.getByText('ORB-142', { exact: true })).toHaveCount(0)
  await changes.getByRole('button', { name: 'Dismiss proposal for ORB-143', exact: true }).click()
  await expect(changes.getByText('No proposed changes.')).toBeVisible()
  expect(world.resolutions).toEqual([{ action: 'apply' }, { action: 'dismiss' }])
})

test('owner explicitly confirms upgrade while server Suggest remains the cap', async ({ page }) => {
  const world = await setup(page)
  await page.route('**/api/me/permissions*', route => route.fulfill({ json: { workspace: { role: { id: 'owner', key: 'owner', name: 'Owner' }, permissions: ['settings.manage', 'ownership.transfer', 'nodes.read', 'nodes.write'] }, project: null } }))
  Object.assign(world.settings, { server_mode: 'on', effective_mode: 'suggest', suggest_until: '2026-10-02T18:00:00Z' })
  await page.goto('/settings/workspace')
  const button = page.getByRole('button', { name: 'Enable automatic changes', exact: true })
  await button.click()
  await expect(button).toHaveCount(0)
  expect(world.writes).toHaveLength(1)
  expect(world.writes[0]).toMatchObject({ confirm_upgrade: true, expected_revision: 0 })
  Object.assign(world.settings, { server_mode: 'suggest', effective_mode: 'suggest', suggest_until: '2026-10-02T18:00:00Z' })
  await page.reload()
  await expect(button).toBeDisabled()
  await expect(page.getByText('The server operator requires Suggest mode. Proposed changes wait for Apply or Dismiss.')).toBeVisible()
})

test('approved rules, master switch, validation, inheritance and audited Undo', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/settings/workspace')
  const card = page.getByRole('region', { name: 'Status autopilot', exact: true })
  await expect(card.getByRole('switch', { name: 'Status autopilot', exact: true })).toBeChecked()
  await expect(card.locator('.rules')).toHaveCSS('margin-top', '14px')
  await expect(card.locator('.rule').first()).toHaveCSS('border-top-width', '1px')
  await expect(card.locator('.rule').first()).toHaveCSS('border-top-style', 'solid')
  expect(await card.locator('input[type=number]').evaluateAll(inputs => inputs.map(input => (input as HTMLInputElement).value))).toEqual(['7', '90', '14', '3', '14', '30'])
  await expect(card.getByText('On publish', { exact: true })).toBeVisible()
  const accept = card.getByLabel('Days delivered without objection before Accepted')
  await accept.fill('366'); await accept.blur()
  await expect(accept).toHaveAttribute('aria-invalid', 'true'); expect(world.writes).toHaveLength(0)
  await accept.fill('45'); await accept.blur()
  await expect(card.getByText(/Accepted 45 days later/)).toBeVisible()
  await card.getByRole('switch', { name: 'Delivered to Accepted' }).uncheck()
  await expect(accept).toBeDisabled()
  await card.getByRole('switch', { name: 'Delivered to Accepted' }).check()
  await expect(accept).toBeEnabled()
  await card.getByRole('switch', { name: 'Status autopilot', exact: true }).uncheck()
  await expect(accept).toBeDisabled()
  await expect(card.getByText('Off: nothing moves on its own. Suggestions are still listed.')).toBeVisible()
  const attention = page.getByRole('region', { name: 'Tickets needing attention', exact: true })
  for (const [i, label] of ['Triage list', 'Cancel suggested', 'Blocked reminders', 'Missed releases'].entries()) {
    await expect(attention.getByRole('region', { name: label, exact: true }).getByText(`ORB-${i + 1}`, { exact: true })).toBeVisible()
  }
  const group = page.getByRole('radiogroup', { name: /Status autopilot in/ }).first()
  await group.getByRole('radio', { name: 'On', exact: true }).click()
  await expect(group.getByRole('radio', { name: 'On', exact: true })).toHaveAttribute('aria-checked', 'true')
  await group.getByRole('radio', { name: 'On', exact: true }).press('ArrowRight')
  await expect(group.getByRole('radio', { name: 'Off', exact: true })).toHaveAttribute('aria-checked', 'true')
  const recent = page.getByRole('region', { name: 'Recent automatic changes' })
  await expect(recent.getByText('Status autopilot', { exact: true })).toBeVisible()
  await expect(recent.getByText('No session, branch or PR activity for 3 days.')).toBeVisible()
  await recent.getByRole('button', { name: /Undo: put ORB-142 back/ }).click()
  await expect(recent.getByText('Undone · back to In progress')).toBeVisible(); expect(world.undos()).toBe(1)
})

test('stale setting writes explain recovery and keep the saved limits', async ({ page }) => {
  const world = await setup(page); await page.goto('/settings/workspace'); world.conflict()
  const card = page.getByRole('region', { name: 'Status autopilot', exact: true })
  await card.getByLabel('Days delivered without objection before Accepted').fill('60')
  await card.getByLabel('Days delivered without objection before Accepted').blur()
  await expect(page.locator('.autopilot').getByRole('alert')).toContainText('Another admin changed these settings')
  await page.locator('.autopilot').getByRole('button', { name: 'Reload', exact: true }).click()
  await expect(card.getByLabel('Days delivered without objection before Accepted')).toHaveValue('30')
})

test('members have no workspace autopilot controls', async ({ page }) => {
  await setup(page, 'member'); await page.goto('/settings/workspace')
  await expect(page.getByRole('region', { name: 'Status autopilot', exact: true })).toHaveCount(0)
})

test('light, dark and narrow settings evidence beside the approved fragment', async ({ page }, testInfo) => {
  await setup(page); await page.goto('/settings/workspace')
  const folder = process.env.STATUS_AUTOPILOT_SHOTS ?? testInfo.outputPath('shots'); await mkdir(folder, { recursive: true })
  for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width: 1440, height: 2400 })
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await expect(page.getByRole('region', { name: 'Recent automatic changes' })).toBeVisible()
    const path = `${folder}/status-autopilot-${theme}.png`
    await page.locator('.autopilot').screenshot({ path }); await testInfo.attach(`status-autopilot-${theme}`, { path, contentType: 'image/png' })
  }
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByRole('region', { name: 'Status autopilot', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
})

test('ticket Activity shows the autopilot reason, automatic filter and guarded Undo', async ({ page }) => {
  await setup(page)
  const change: AutomaticChange = { event_id: 41, node_id: 'n-1', key: 'PHR-12', title: 'Ticket', actor: 'Status autopilot', rule: 'progress', reason: 'No session, branch or PR activity for 3 days.', from: 'in_progress', to: 'open', at: '2026-10-01T17:00:00Z', undone: false, undoable: true }
  let undoCalls = 0
  await page.route('**/api/events/41/undo', route => { undoCalls++; change.undone = true; change.undoable = false; return route.fulfill({ status: 201, json: { id: 42 } }) })
  await page.route('**/api/nodes/n-1/activity*', route => route.fulfill({ json: { items: [{ id: '41', type: 'change', at: change.at, author: { id: 'system', name: 'System', automatic: true, job: 'status-autopilot', reason: change.reason }, changes: [{ field: 'status', from: 'in_progress', to: 'open' }], automatic_change: change }], next_cursor: null } }))
  await page.goto('/p/PHAROS/PHAROS-11')
  const activity = page.getByRole('region', { name: 'Activity', exact: true })
  await expect(activity.getByText('Status autopilot', { exact: true })).toBeVisible()
  await expect(activity.getByText(change.reason)).toBeVisible()
  await activity.getByRole('radio', { name: 'Automatic', exact: true }).click()
  await expect(activity.getByRole('radio', { name: 'Automatic', exact: true })).toBeChecked()
  await expect(activity.getByText(change.reason)).toBeVisible()
  await activity.getByRole('button', { name: /Undo: put PHR-12 back/ }).click()
  await expect.poll(() => undoCalls).toBe(1)
  await expect(activity.getByText('Undone · back to In progress')).toBeVisible()
})
