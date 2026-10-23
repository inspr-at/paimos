// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { sessionListReads } from './agents-fixtures'
import { mockStartAgent } from './start-agent-fixtures'
import { expectStableControls } from './helpers/stable'
import { me } from './work-fixtures'
import { absoluteTime } from '../src/lib/work'
import type { QueueWireEntry } from '../src/lib/workQueue'

const dialog = (page: Page) => page.getByRole('dialog', { name: 'Start agent', exact: true })
const newAgent = (page: Page) => page.getByRole('button', { name: 'New: start a lead, attach a session or connect a machine', exact: true })
const onceDialog = (page: Page) => page.getByRole('dialog', { name: 'Run once', exact: true })
async function openOnce(page: Page) {
  await page.goto('/agents')
  await newAgent(page).click()
  await page.getByRole('menuitem', { name: /^Run once/ }).click()
  await expect(onceDialog(page)).toBeVisible()
}

test('Run once is available without developer settings and recovers an exact read-only request', async ({ page }) => {
  const mock = await mockStartAgent(page, { expertStart: false, catalog: 'two-hosts', lostOnceResponse: true })
  await page.goto('/agents')
  await newAgent(page).click()
  await expect(page.getByRole('menuitem', { name: /^Start agent manually/ })).toHaveCount(0)
  await page.getByRole('menuitem', { name: /^Run once/ }).click()
  const form = onceDialog(page)
  await form.getByLabel('Project', { exact: true }).selectOption('p-pharos')
  await form.getByLabel('Task', { exact: true }).fill('Inspect the supplied project context')
  await expect(form.getByRole('checkbox', { name: 'May change files', exact: true })).not.toBeChecked()
  await expect(form.getByRole('button', { name: /^Queue run/ })).toBeDisabled()
  const readOnlyWarning = form.locator('.settings-status')
  await expect(readOnlyWarning.locator('strong')).toHaveText('Read-only unavailable')
  const readOnlyDetail = readOnlyWarning.locator('.settings-warning-detail')
  await expect(readOnlyDetail).toHaveText('· Codex read-only sandboxing does not isolate inherited MCP tools and startup hooks.')
  await expect(readOnlyDetail).toBeVisible()
  await expect(readOnlyWarning.locator('.settings-warning')).toHaveAttribute('title', 'Read-only unavailable · Codex read-only sandboxing does not isolate inherited MCP tools and startup hooks.')
  await expectStableControls({
    controls: { summary: form.locator('.run-summary-line'), task: form.getByLabel('Task', { exact: true }), queue: form.getByRole('button', { name: /^Queue run/ }) },
    interactions: [
      { name: 'fold read-only warning', run: async () => {
        await form.getByRole('button', { name: 'Hide', exact: true }).click()
        await expect(readOnlyDetail).toHaveCSS('text-overflow', 'ellipsis')
        await expect(readOnlyDetail).toHaveCSS('white-space', 'nowrap')
        await expect(readOnlyWarning.locator('strong')).toHaveCSS('flex-shrink', '0')
      } },
      { name: 'expand read-only warning', run: async () => {
        await form.getByRole('button', { name: 'Change', exact: true }).click()
        await expect(readOnlyDetail).toHaveCSS('white-space', 'normal')
      } },
    ],
    scrollAreas: { body: form.locator('.launch-body') },
  })
  await form.getByLabel('Host', { exact: true }).selectOption('laptop')
  const text = '  Inspect exactly this context.\nReturn the observations.'
  await form.getByLabel('Instruction (optional)', { exact: true }).fill(text)
  await form.getByRole('button', { name: /^Queue run/ }).click()
  await expect(form.getByRole('alert')).toContainText('Your selections are kept')
  await expect(form.getByLabel('Instruction (optional)', { exact: true })).toHaveValue(text)
  await expect(form.getByRole('checkbox', { name: 'May change files', exact: true })).not.toBeChecked()
  await form.getByRole('button', { name: /^Queue run/ }).dblclick()
  await expect(form.getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
  const writes = mock.calls.filter(call => call.path.endsWith('/run-once'))
  expect(writes).toHaveLength(2)
  expect(writes[0]?.body).toEqual(writes[1]?.body)
  expect(writes[0]?.body).toMatchObject({ instruction: text, repository_mutation_allowed: false, task: 'Inspect the supplied project context' })
  expect(mock.state.runs).toHaveLength(1)
  expect(mock.createdLeaves).toEqual([{ parent_id: 'p-pharos', title: 'Run once: Inspect the supplied project context', body: 'Inspect the supplied project context', fields: { hide_from_release_notes: true } }])
})

test('Run once resets task choices on context changes and never reuses a different instruction', async ({ page }) => {
  const mock = await mockStartAgent(page, { expertStart: false, lostOnceResponse: true })
  await openOnce(page)
  const form = onceDialog(page)
  await form.getByLabel('Project', { exact: true }).selectOption('p-pharos')
  await form.getByLabel('Task', { exact: true }).fill('Project task')
  await form.getByLabel('Instruction (optional)', { exact: true }).fill('Old context instruction')
  await form.getByRole('checkbox', { name: 'May change files', exact: true }).check()
  await form.getByLabel('Context', { exact: true }).selectOption({ label: 'Use an existing ticket' })
  await expect(form.getByLabel('Instruction (optional)', { exact: true })).toHaveValue('')
  await expect(form.getByRole('checkbox', { name: 'May change files', exact: true })).not.toBeChecked()
  await form.getByRole('searchbox').fill('PHAROS-11')
  await form.getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  await form.getByLabel('Instruction (optional)', { exact: true }).fill('First instruction')
  await form.getByRole('checkbox', { name: 'May change files', exact: true }).check()
  await form.getByRole('button', { name: /^Queue run/ }).click()
  await expect(form.getByRole('alert')).toContainText('Your selections are kept')
  await form.getByLabel('Instruction (optional)', { exact: true }).fill('Changed instruction')
  await form.getByRole('button', { name: /^Queue run/ }).click()
  await expect(form.getByRole('alert')).toContainText('already has an active run')
  const writes = mock.calls.filter(call => call.path.endsWith('/run-once'))
  expect(writes[0]?.body?.request_id).not.toBe(writes[1]?.body?.request_id)
  expect(mock.state.runs).toHaveLength(1)
  expect(mock.state.runs[0]?.instruction).toBe('First instruction')
})

for (const theme of ['light', 'dark'] as const) for (const width of [390, 1024, 1440]) {
  test(`Run once controls stay still at ${width} ${theme}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    await mockStartAgent(page, { expertStart: false, catalog: 'claude', theme })
    await openOnce(page)
    const form = onceDialog(page)
    if (width === 390) expect(await form.evaluate(el => el.getBoundingClientRect().width)).toBe(width)
    const task = form.getByLabel('Task', { exact: true })
    await expect(task).toBeFocused()
    const firstField = form.locator('.launch-fields input, .launch-fields select, .launch-fields textarea').first()
    await expect(firstField).toHaveAttribute('id', await task.getAttribute('id') as string)
    const summary = form.locator('.run-settings-summary')
    const summaryLine = form.locator('.run-summary-line')
    await expect(summary).toHaveCSS('border-bottom-width', '0px')
    await expect(summary).toContainText('Runs on Laptop · Claude Code · Laptop account · Max · other-model · Medium')
    await expect(summaryLine).toHaveCSS('height', '44px')
    await expect(summaryLine.locator('p')).toHaveAttribute('title', 'Runs on Laptop · Claude Code · Laptop account · Max · other-model · Medium')
    await expect(summaryLine.locator('p')).toHaveCSS('white-space', 'nowrap')
    await expect(summaryLine.locator('p')).toHaveCSS('text-overflow', 'ellipsis')
    await expect(summary.getByRole('status')).toHaveCount(0)
    await expect(form.getByLabel('Host', { exact: true })).toBeHidden()
    await task.fill('Ausführliche Bestandsaufnahme der bereitgestellten Projektinformationen')
    await form.getByLabel('Project', { exact: true }).selectOption('p-pharos')
    await expect(task).toHaveValue('Ausführliche Bestandsaufnahme der bereitgestellten Projektinformationen')
    const queue = form.getByRole('button', { name: 'Queue run', exact: true })
    await expect(queue.locator('kbd')).toHaveCount(2)
    await expect(form.getByText('Read-only: no tools, no repository access, no external actions.', { exact: true })).toBeVisible()
    const ordered = await form.locator('.launch-fields input:not([type="checkbox"]), .launch-fields select, .launch-fields textarea, .restriction-field').evaluateAll(elements => elements.filter(el => (el as HTMLElement).offsetParent !== null).map(el => el.id || el.className))
    expect(ordered.slice(0, 4).map(id => id.split('-').at(-1))).toEqual(['task', 'context', 'project', 'instruction'])
    await form.screenshot({ path: info.outputPath(`AEON-1688/start-agent-${width}-${theme}.png`) })
    // Explicit expansion can grow downward; existing controls stay anchored.
    await expectStableControls({
      controls: { queue, cancel: form.getByRole('button', { name: 'Cancel', exact: true }), task, summary: summaryLine, context: form.getByLabel('Context', { exact: true }), project: form.getByLabel('Project', { exact: true }), instruction: form.getByLabel('Instruction (optional)', { exact: true }), ...(width === 390 ? { phone: form } : {}) },
      scrollAreas: { body: form.locator('.launch-body') },
      interactions: [{ name: 'expand run settings', run: async () => {
        await summary.getByRole('button', { name: 'Change', exact: true }).click()
        await expect(form.getByLabel('Host', { exact: true })).toBeVisible()
      } }],
    })
    await expect(form.getByLabel('Host', { exact: true })).toHaveValue('laptop')
    await expect(form.getByLabel('Harness', { exact: true })).toHaveValue('claude')
    await expect(form.getByLabel('Account', { exact: true }).locator('option:checked')).toContainText('Laptop account')
    await expect(form.getByLabel('Model', { exact: true }).locator('option:checked')).toHaveText('other-model')
    await expect(form.getByLabel('Thinking', { exact: true }).locator('option:checked')).toHaveText('Medium')
    const checkbox = form.getByRole('checkbox', { name: 'May change files', exact: true })
    if (width === 390) expect(await checkbox.evaluate(el => el.parentElement!.getBoundingClientRect().height)).toBeGreaterThanOrEqual(44)
    await expectStableControls({
      controls: { queue, cancel: form.getByRole('button', { name: 'Cancel', exact: true }), context: form.getByLabel('Context', { exact: true }), host: form.getByLabel('Host', { exact: true }), harness: form.getByLabel('Harness', { exact: true }), account: form.getByLabel('Account', { exact: true }), thinking: form.getByLabel('Thinking', { exact: true }), selectors: form.locator('.pair').first(), instruction: form.getByLabel('Instruction (optional)', { exact: true }), restriction: checkbox, project: form.getByLabel('Project', { exact: true }), task, ...(width === 390 ? { phone: form } : {}) },
      interactions: [
        { name: 'allow changes explicitly', run: () => checkbox.check() },
        { name: 'restore read-only', run: () => checkbox.uncheck() },
        { name: 'long German instruction', run: () => form.getByLabel('Instruction (optional)', { exact: true }).fill('Die bereitgestellten Informationen vollständig auswerten und mögliche Unklarheiten verständlich zusammenfassen. '.repeat(8)) },
      ],
      scrollAreas: { body: form.locator('.launch-body') },
    })
    await form.locator('.run-settings-fields').scrollIntoViewIfNeeded()
    await form.screenshot({ path: info.outputPath(`AEON-1512-${width}-${theme}-expanded.png`) })
    await form.getByRole('button', { name: 'Cancel', exact: true }).click()
    await page.unrouteAll({ behavior: 'wait' })
    await mockStartAgent(page, { expertStart: false, catalog: 'claude', theme, wait: { code: width === 1024 ? 'sign_in' : 'allowance', run_now_allowed: false } })
    await openOnce(page)
    await expect(form.getByLabel('Task', { exact: true })).toBeFocused()
    await expect(form.locator('.settings-toggle')).toHaveAttribute('aria-expanded', 'true')
    await expect(form.getByLabel('Host', { exact: true })).toBeVisible()
    const warning = form.locator('.settings-status')
    const warningLabel = warning.locator('strong'), warningDetail = warning.locator('.settings-warning-detail')
    const label = width === 1024 ? 'Inactive login · switch to it on the computer' : 'Waiting for capacity'
    const detail = width === 1024 ? 'Runs start once this login is active on the computer.' : 'The run queues and starts when this account is ready.'
    await expect(warning).toHaveText(`${label} · ${detail}`)
    await expect(warningLabel).toHaveText(label)
    await expect(warningLabel).toHaveCSS('font-weight', '600')
    await expect(warningDetail).toHaveText(`· ${detail}`)
    await expect(warningDetail).toHaveCSS('font-weight', '400')
    await expect(warning.locator('.settings-warning')).toHaveAttribute('title', `${label} · ${detail}`)
    await expect(summaryLine).toHaveCSS('height', '44px')
    for (const part of [warningLabel, warningDetail]) await expect(part).toHaveCSS('display', 'inline')
    // The label and sentence start on the same line, with natural phone wrapping.
    const labelLine = await warningLabel.evaluate(el => el.getClientRects()[0]!.y)
    const detailLines = await warningDetail.evaluate(el => [...el.getClientRects()].map(rect => rect.y))
    expect(Math.abs(detailLines[0]! - labelLine)).toBeLessThanOrEqual(0.5)
    if (width !== 390) expect(detailLines).toHaveLength(1)
    const expandedWarningHeight = await warning.evaluate(el => el.getBoundingClientRect().height)
    await expectStableControls({
      controls: { queue, cancel: form.getByRole('button', { name: 'Cancel', exact: true }), task, summary: form.locator('.run-summary-line'), ...(width === 390 ? { phone: form } : {}) },
      interactions: [
        { name: 'fold warning settings', run: async () => {
          await form.getByRole('button', { name: 'Hide', exact: true }).click()
          await expect(form.getByLabel('Host', { exact: true })).toBeHidden()
          await expect(warning).toHaveText(`${label} · ${detail}`)
          await expect(summaryLine).toHaveCSS('height', '44px')
          await expect(warningDetail).toHaveCSS('white-space', 'nowrap')
          await expect(warningDetail).toHaveCSS('overflow', 'hidden')
          await expect(warningDetail).toHaveCSS('text-overflow', 'ellipsis')
          await expect(warningLabel).toHaveCSS('white-space', 'nowrap')
          await expect(warningLabel).toHaveCSS('flex-shrink', '0')
          const labelBox = await warningLabel.boundingBox(), detailBox = await warningDetail.boundingBox()
          expect(labelBox).not.toBeNull()
          expect(detailBox).not.toBeNull()
          expect(Math.abs(labelBox!.y - detailBox!.y)).toBeLessThanOrEqual(0.5)
          expect(detailBox!.width).toBeGreaterThan(0)
          const summaryBox = (await summary.boundingBox())!
          expect(labelBox!.x).toBeGreaterThanOrEqual(summaryBox.x - 0.5)
          expect(detailBox!.x + detailBox!.width).toBeLessThanOrEqual(summaryBox.x + summaryBox.width + 0.5)
          expect(await warning.locator('.settings-warning').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
          expect(Math.abs(await warning.evaluate(el => el.getBoundingClientRect().height) - expandedWarningHeight)).toBeLessThanOrEqual(0.5)
          expect(await warningLabel.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
          if (width === 390) expect(await warningDetail.evaluate(el => el.scrollWidth - el.clientWidth)).toBeGreaterThan(0)
          await form.screenshot({ path: info.outputPath(`AEON-1512-${width}-${theme}-warning-folded.png`) })
        } },
        { name: 'expand warning settings', run: async () => {
          await form.getByRole('button', { name: 'Change', exact: true }).click()
          await expect(form.getByLabel('Host', { exact: true })).toBeVisible()
          await expect(warning).toHaveText(`${label} · ${detail}`)
          await expect(summaryLine).toHaveCSS('height', '44px')
          await expect(warningDetail).toHaveCSS('white-space', 'normal')
        } },
      ],
      scrollAreas: { body: form.locator('.launch-body') },
    })
    await form.locator('.run-settings-summary').scrollIntoViewIfNeeded()
    await form.screenshot({ path: info.outputPath(`AEON-1512-${width}-${theme}-warning.png`) })
  })
}
async function open(page: Page, ticket = false) {
  // Ticket dispatch now uses the Queue/assignee menu (work-queue.spec.ts).
  // Keep the explicit account cascade covered through the /agents New menu.
  await page.goto('/agents')
  await newAgent(page).click()
  await page.getByRole('menuitem', { name: /^Start agent manually…/ }).click()
  await expect(dialog(page)).toBeVisible()
  if (ticket) {
    await dialog(page).getByRole('searchbox').fill('PHAROS-11')
    await dialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  }
}
async function ready(page: Page, ticket = false) {
  if (!ticket) {
    await dialog(page).getByRole('searchbox').fill('PHAROS-11')
    await dialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  }
  await expect(dialog(page).getByLabel('Host', { exact: true })).toHaveValue('workstation')
  await expect(dialog(page).getByLabel('Thinking', { exact: true })).not.toHaveValue('')
  await expect(dialog(page).getByText('Account available', { exact: true })).toBeVisible()
}

test('creates, readies and queues a ticket, then follows claim and managed registration', async ({ page }) => {
  const mock = await mockStartAgent(page)
  await open(page)
  await ready(page)
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true }).locator('kbd')).toHaveCount(2)
  await expect(dialog(page).getByRole('option', { name: 'ungranted-model' })).toHaveCount(0)
  await expect(dialog(page).getByText('should-not-render-key')).toHaveCount(0)
  await expect(dialog(page).getByText('Selected because it has the most allowance left.')).toBeVisible()
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).dblclick()
  await expect(dialog(page).getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
  await expect(dialog(page).getByRole('list')).toContainText('Work Mac')
  await expect(dialog(page).getByRole('list')).toContainText('Workspace account · Pro')
  expect(mock.calls.filter(c => c.method === 'POST' && c.path === '/api/work-orders')).toHaveLength(1)
  expect(mock.calls.filter(c => c.method === 'POST' && c.path.endsWith('/runs'))).toHaveLength(1)
  expect(mock.calls.find(c => c.method === 'POST' && c.path.endsWith('/runs'))?.body).toEqual({ agent_principal_id: mock.agentId, model_profile_id: mock.profile.id, requested_account_id: mock.account.id })
  expect(mock.calls.find(c => c.method === 'POST' && c.path === '/api/work-orders')?.body?.parent_id).toBe('n-1')
  expect(mock.calls.some(c => c.path.includes('agent-launch-catalog'))).toBe(false)
  expect(mock.calls.some(c => c.path === '/api/agent-accounts/catalog')).toBe(true)
  mock.claim()
  await dialog(page).getByRole('button', { name: 'Refresh status' }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Claimed', exact: true })).toBeVisible()
  await expect(dialog(page).getByRole('link', { name: 'Open session' })).toHaveCount(0)
  mock.claim(true)
  await dialog(page).getByRole('button', { name: 'Refresh status' }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Managed session connected' })).toBeVisible()
  await expect(dialog(page).getByText('Requested configuration')).toBeVisible()
  await expect(dialog(page).getByText('Reported by the session')).toBeVisible()
  await expect(dialog(page).getByText('workspace-build').first()).toBeVisible()
  await expect(dialog(page).getByText('This differs from the requested configuration.')).toHaveCount(0)
  await expect(dialog(page).getByRole('link', { name: 'Open session' })).toHaveAttribute('href', '/agents/managed-1')
})

test('advanced launch retains its selected ticket and retry reuses the created work order', async ({ page }) => {
  const mock = await mockStartAgent(page, { failQueue: true })
  await open(page, true)
  await expect(dialog(page).getByText('PHAROS-11', { exact: true })).toBeVisible()
  await expect(dialog(page).getByRole('searchbox')).toHaveCount(0)
  await ready(page, true)
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog(page).getByRole('alert')).toContainText('Temporary queue failure')
  await expect(dialog(page).getByLabel('Host', { exact: true })).toHaveValue('workstation')
  mock.state.failQueue = false
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
  expect(mock.calls.filter(c => c.method === 'POST' && c.path === '/api/work-orders')).toHaveLength(1)
})

test('canonical leaf search excludes mixed parent results and queues only the selected leaf', async ({ page }) => {
  const mock = await mockStartAgent(page)
  const reads: URLSearchParams[] = []
  const leaf = { id: 'n-1', key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning', is_leaf: true, fields: {}, kind_slug: 'work' }
  await page.route('**/api/nodes?**', route => {
    const query = new URL(route.request().url()).searchParams
    if (query.get('limit') !== '30' || query.get('sort') !== '-updated_at') return route.fallback()
    reads.push(query)
    // Deliberately include an ineligible row even when shape was requested.
    return route.fulfill({ json: { items: [{ ...leaf, id: 'parent', key: 'PHAROS-10', title: 'Work parent', is_leaf: false }, leaf], next_cursor: null } })
  })
  await open(page)
  const search = dialog(page).getByRole('searchbox')
  await expect(dialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ })).toBeVisible()
  await expectStableControls({
    controls: { search },
    interactions: [{ name: 'filter leaf results', run: async () => {
      await search.fill('PHAROS')
      await expect.poll(() => reads.at(-1)?.get('q')).toBe('PHAROS')
    } }],
    scrollAreas: { results: dialog(page).locator('.ticket-results') },
  })
  expect(reads.length).toBeGreaterThan(0)
  for (const query of reads) {
    expect(query.get('kind')).toBe('work')
    expect(query.get('shape')).toBe('leaf')
  }
  await expect(dialog(page).getByRole('button', { name: /PHAROS-10 Work parent/ })).toHaveCount(0)
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeDisabled()
  await dialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  await ready(page, true)
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
  expect(mock.calls.filter(c => c.method === 'POST' && c.path === '/api/work-orders').map(c => c.body?.parent_id)).toEqual(['n-1'])
})

test('a queue that fails after the work order was written still reads the lists again', async ({ page }) => {
  const mock = await mockStartAgent(page, { failQueue: true })
  await open(page)
  await ready(page)
  const before = sessionListReads(mock.calls)
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog(page).getByRole('alert')).toContainText('Temporary queue failure')
  expect(mock.calls.filter(c => c.method === 'POST' && c.path === '/api/work-orders')).toHaveLength(1)
  await expect.poll(() => sessionListReads(mock.calls)).toBeGreaterThan(before)
})

for (const scenario of [
  { offline: true, label: 'Sign-in probe is stale' },
  { unavailable: true, label: 'Account is draining' },
] as const) {
  test(`reports ${scenario.label} and still pins that account`, async ({ page }) => {
    const mock = await mockStartAgent(page, scenario)
    await open(page)
    await readyAccount(page)
    await expect(dialog(page).getByText(scenario.label, { exact: true })).toBeVisible()
    await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeEnabled()
    await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
    await expect(dialog(page).getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
    expect(mock.calls.find(c => c.method === 'POST' && c.path.endsWith('/runs'))?.body?.requested_account_id).toBe(mock.account.id)
    await dialog(page).getByRole('button', { name: 'Close', exact: true }).click()
    await expect(page.getByRole('region', { name: 'Runs awaiting a session' })).toContainText('Queued')
  })
}

async function readyAccount(page: Page) {
  await dialog(page).getByRole('searchbox').fill('PHAROS-11')
  await dialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  await expect(dialog(page).getByLabel('Account', { exact: true })).not.toHaveValue('')
  await expect(dialog(page).getByLabel('Thinking', { exact: true })).not.toHaveValue('')
}

// Risk: rotation sends a person to the pause toggle, queues another login,
// or moves phone selectors/actions when the login advice wraps differently.
test('AEON-1509 inactive Claude login keeps its account pin and deliberate pause guidance', async ({ page }, info) => {
  const mock = await mockStartAgent(page, { catalog: 'two-hosts' })
  mock.catalog.hosts.splice(0, 1)
  const harness = mock.catalog.hosts[0].harnesses[0]
  const active = { ...harness.accounts[0], label: 'Aktives Claude-Konto für gemeinsame Entwicklungsprojekte' }
  const inactive = { ...active, id: mock.spareAccountId, label: 'Weiteres Claude-Konto für gemeinsame Entwicklungsprojekte', state: 'unavailable', last_probe_ok: false, available: false, unavailable_reasons: ['state' as const, 'probe' as const], wait: { code: 'sign_in' as const, run_now_allowed: false } }
  const paused = { ...inactive, id: 'ac000000-0000-4000-8000-000000000004', label: 'Paused Claude login', state: 'draining', wait: { code: 'state' as const, run_now_allowed: false } }
  harness.accounts = [active, inactive, paused]
  await page.setViewportSize({ width: 1440, height: 1000 })
  await open(page)
  await readyAccount(page)
  const sheet = dialog(page)
  const account = sheet.getByLabel('Account', { exact: true })
  await expect(account).toHaveValue(active.id)
  await expect(sheet.getByText('Account available', { exact: true })).toBeVisible()
  await expect(account.locator(`option[value="${inactive.id}"]`)).toContainText('Inactive login · switch to it on the computer')
  await expect(account.locator(`option[value="${inactive.id}"]`)).not.toContainText('Settings')
  await account.selectOption(inactive.id)
  // Phone options keep name and plan; the selected login's full advice stays
  // in the status below the controls.
  await page.setViewportSize({ width: 390, height: 1000 })
  await expect(sheet.getByRole('status').locator('strong')).toHaveText('Inactive login · switch to it on the computer')
  await expect(sheet.getByRole('status').locator('p')).toHaveText('Runs start once this login is active on the computer.')
  await expect(sheet.getByRole('button', { name: 'Run now once', exact: true })).toHaveCount(0)
  const queue = sheet.getByRole('button', { name: 'Queue run', exact: true })
  for (const theme of ['light', 'dark']) for (const height of [1000, 844]) {
    await page.setViewportSize({ width: 390, height })
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    const frame = await sheet.boundingBox()
    expect(frame).not.toBeNull()
    expect(Math.abs(frame!.y), `${theme} ${height}: phone sheet starts at the viewport top`).toBeLessThanOrEqual(0.5)
    expect(Math.abs(frame!.height - height), `${theme} ${height}: phone sheet fills the viewport`).toBeLessThanOrEqual(0.5)
    await expect(queue).toBeInViewport()
    await expectStableControls({
      controls: {
        frame: sheet, close: sheet.getByRole('button', { name: 'Close start agent', exact: true }),
        account, host: sheet.getByLabel('Host', { exact: true }), harness: sheet.getByLabel('Harness', { exact: true }),
        model: sheet.getByLabel('Model', { exact: true }), thinking: sheet.getByLabel('Thinking', { exact: true }),
        selectors: sheet.locator('.launch-fields'), cancel: sheet.getByRole('button', { name: 'Cancel', exact: true }), queue,
      },
      scrollAreas: { dialog: sheet, body: sheet.locator('.launch-body') },
      interactions: [
        { name: 'choose deliberately paused login', run: async () => {
          await account.selectOption(paused.id)
          await expect(sheet.getByRole('status')).toContainText('Paused in Settings / Accounts')
          await expect(sheet.getByRole('status').locator('p')).toHaveText('The run queues and starts when this account is ready.')
          await expect(queue).toBeInViewport()
        } },
        { name: 'return to inactive login', run: async () => {
          await account.selectOption(inactive.id)
          await expect(sheet.getByRole('status').locator('strong')).toHaveText('Inactive login · switch to it on the computer')
          await expect(sheet.getByRole('status').locator('p')).toHaveText('Runs start once this login is active on the computer.')
          await expect(sheet.getByRole('status')).not.toContainText('Settings')
          await expect(queue).toBeInViewport()
        } },
        { name: 'read login advice in the scrolling body', run: async () => {
          await sheet.getByRole('status').scrollIntoViewIfNeeded()
          await expect(sheet.getByRole('status')).toBeInViewport()
          await expect(queue).toBeInViewport()
        } },
        { name: 'return to the account selector', run: async () => {
          await account.scrollIntoViewIfNeeded()
          await expect(account).toBeInViewport()
          await expect(queue).toBeInViewport()
        } },
      ],
    })
  }
  for (const theme of ['light', 'dark']) for (const width of [390, 1024, 1440]) {
    await page.setViewportSize({ width, height: 1000 })
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await page.screenshot({ path: info.outputPath(`AEON-1509/start-agent-${width}-${theme}.png`), fullPage: true })
  }
  await sheet.getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(sheet.getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
  const queued = mock.calls.filter(call => call.method === 'POST' && call.path.endsWith('/runs'))
  expect(queued).toHaveLength(1)
  expect(queued[0].body?.requested_account_id).toBe(inactive.id)
})

test('an empty model grant is not filled from the tenant catalog', async ({ page }) => {
  await mockStartAgent(page, { catalog: 'empty-grants' })
  await open(page)
  await dialog(page).getByRole('searchbox').fill('PHAROS-11')
  await dialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  await expect(dialog(page).getByText('No model granted', { exact: true })).toBeVisible()
  await expect(dialog(page).locator('[id$="-model-note"]')).toHaveText('No granted model is available to choose.')
  await expect(dialog(page).getByRole('option', { name: 'ungranted-model' })).toHaveCount(0)
  await expect(dialog(page).getByRole('option', { name: 'workspace-build' })).toHaveCount(0)
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeDisabled()
})

// AEON-1510: a catalog explanation must not move the selectors/actions, and
// selecting another granted level must queue that exact profile on that account.
test('AEON-1510: Sonnet explains a single catalog level and keeps every granted level selectable', async ({ page }, info) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const mock = await mockStartAgent(page)
  const harness = mock.catalog.hosts[0].harnesses[0]
  harness.harness = 'claude'
  harness.accounts.splice(1)
  const account = harness.accounts[0]
  account.label = 'agent.one'
  account.models = [{ model: 'sonnet', family: 'anthropic', efforts: [{ effort: 'high', model_profile_id: mock.profile.id, version: '1' }] }]
  Object.assign(mock.profile, { harness: 'claude', family: 'anthropic', model: 'sonnet' })
  await open(page, true)
  await ready(page, true)
  const thinking = dialog(page).getByLabel('Thinking', { exact: true })
  const model = dialog(page).getByLabel('Model', { exact: true })
  const note = dialog(page).locator('[id$="-effort-note"]')
  const queue = dialog(page).getByRole('button', { name: 'Queue run', exact: true })
  const singleLevelNote = 'This account offers only High thinking for this model.'
  await expect(thinking.locator('option')).toHaveText(['Choose a thinking level', 'High'])
  await expect(note).toHaveText(singleLevelNote)
  await expect(thinking).toHaveAccessibleDescription(singleLevelNote)
  await page.screenshot({ path: info.outputPath('sonnet-single-level-390-light.png'), fullPage: true })
  const lowProfile = 'b1000000-0000-4000-8000-000000000003'
  await expectStableControls({
    controls: {
      model, thinking, selectors: model.locator('../..'),
      queue, cancel: dialog(page).getByRole('button', { name: 'Cancel', exact: true }),
      frame: dialog(page),
    },
    scrollAreas: { dialog: dialog(page) },
    interactions: [
      { name: 'clear the only thinking level', run: async () => {
        await thinking.selectOption('')
        await expect(queue).toBeDisabled()
        await expect(note).toHaveText(singleLevelNote)
      } },
      { name: 'choose the granted level again', run: async () => {
        await thinking.selectOption(mock.profile.id)
        await expect(queue).toBeEnabled()
      } },
      { name: 'read an expanded Sonnet grant', run: async () => {
        account.models[0].efforts.push(
          { effort: 'medium', model_profile_id: mock.spareProfileId, version: '1' },
          { effort: 'low', model_profile_id: lowProfile, version: '1' },
        )
        await dialog(page).getByRole('button', { name: 'Close start agent', exact: true }).click()
        await open(page, true)
        await ready(page, true)
        await expect(thinking.locator('option')).toHaveText(['Choose a thinking level', 'Low', 'Medium', 'High'])
        await expect(note).not.toHaveText(singleLevelNote)
      } },
      ...[
        { level: 'Medium', id: mock.spareProfileId },
        { level: 'High', id: mock.profile.id },
        { level: 'Low', id: lowProfile },
      ].map(({ level, id }) => ({ name: `select ${level}`, run: async () => {
        await thinking.selectOption(id)
        await expect(thinking).toHaveValue(id)
        await expect(queue).toBeEnabled()
      } })),
    ],
  })
  await queue.click()
  await expect(dialog(page).getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
  expect(mock.calls.find(call => call.method === 'POST' && call.path.endsWith('/runs'))?.body).toEqual({
    agent_principal_id: mock.agentId, model_profile_id: lowProfile, requested_account_id: account.id,
  })
  await expect(dialog(page).locator('.requested').first()).toContainText('Low')
})

test('a catalog miss stays empty until retry', async ({ page }) => {
  await mockStartAgent(page, { catalog: 'retry' })
  await open(page)
  const alert = dialog(page).getByRole('alert')
  await expect(alert).toContainText('The account catalog did not load')
  await expect(dialog(page).getByLabel('Host', { exact: true })).toBeDisabled()
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeDisabled()
  await alert.getByRole('button', { name: 'Retry catalog' }).click()
  await expect(dialog(page).getByText('Account available', { exact: true })).toBeVisible()
  await expect(dialog(page).getByRole('option', { name: 'ungranted-model' })).toHaveCount(0)
})

test('pi registry model ids stay offered and a secret-shaped id does not', async ({ page }) => {
  await mockStartAgent(page, { catalog: 'pi' })
  await open(page, true)
  await ready(page, true)
  await expect(dialog(page).getByLabel('Model', { exact: true }).locator('option:checked')).toHaveText('anthropic/claude-opus-5')
  await expect(dialog(page).getByRole('option', { name: 'anthropic/claude-sonnet-5', exact: true })).toHaveCount(1)
  await expect(dialog(page).getByRole('option', { name: 'sk-live-token', exact: true })).toHaveCount(0)
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeEnabled()
})

test('a provisional window is unknown and is not treated as fully unused', async ({ page }, info) => {
  test.setTimeout(120_000)
  await mockStartAgent(page, { catalog: 'provisional' })
  await open(page, true)
  await ready(page, true)
  await expect(dialog(page).getByText('Selected because it has the most allowance left.')).toBeVisible()
  const spare = dialog(page).getByRole('option', { name: /Spare account/ })
  await expect(spare).toContainText('No quota reading yet')
  await expect(spare).not.toContainText('100%')
  const account = dialog(page).getByLabel('Account', { exact: true })
  const primary = await account.inputValue()
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
    await expectStableControls({
      controls: { account, model: dialog(page).getByLabel('Model', { exact: true }), thinking: dialog(page).getByLabel('Thinking', { exact: true }), queue: dialog(page).getByRole('button', { name: 'Queue run', exact: true }), cancel: dialog(page).getByRole('button', { name: 'Cancel', exact: true }) },
      scrollAreas: { dialog: dialog(page) },
      interactions: [{ name: 'select an account without a reading', run: async () => {
        await account.selectOption(await spare.getAttribute('value') ?? '')
        await expect(dialog(page).locator('.account-notes > p:not(.sizer)')).toContainText('Agents can start now. The quota shows once this account reports it.')
        await expect(dialog(page)).not.toContainText('Reads after the first run')
        await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeEnabled()
        await dialog(page).screenshot({ path: info.outputPath(`aeon-1511-start-${width}-${theme}-no-reading.png`), animations: 'disabled' })
      } }, { name: 'return to the measured account', run: async () => {
        await account.selectOption(primary)
        await expect(dialog(page).locator('.account-notes > p:not(.sizer)')).toContainText('80%')
      } }],
    })
  }
})

test('a stale grant does not queue or fall back, and catalog refresh drops the hidden model', async ({ page }) => {
  const mock = await mockStartAgent(page, { staleGrant: true })
  await open(page, true)
  await ready(page, true)
  const account = await dialog(page).getByLabel('Account', { exact: true }).inputValue()
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  const alert = dialog(page).getByRole('alert').filter({ hasText: 'no longer allows that model' })
  await expect(alert).toBeVisible()
  await expect(dialog(page).getByRole('heading', { name: 'Queued', exact: true })).toHaveCount(0)
  await expect(dialog(page).getByLabel('Account', { exact: true })).toHaveValue(account)
  await expect(dialog(page).getByLabel('Model', { exact: true })).toHaveValue('')
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeDisabled()
  await alert.getByRole('button', { name: 'Refresh catalog' }).click()
  await expect(dialog(page).getByRole('option', { name: 'workspace-build', exact: true })).toHaveCount(0)
  await expect(dialog(page).getByLabel('Model', { exact: true })).toHaveValue('')
  await expect(dialog(page).getByLabel('Account', { exact: true })).toHaveValue(account)
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeDisabled()
  expect(mock.calls.filter(c => c.method === 'POST' && c.path.endsWith('/runs'))).toHaveLength(1)
  expect(mock.calls.find(c => c.method === 'POST' && c.path.endsWith('/runs'))?.body?.requested_account_id).toBe(account)
})

test('a connected session keeps the request separate from what it reports', async ({ page }) => {
  const mock = await mockStartAgent(page)
  await open(page, true)
  await ready(page, true)
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog(page).getByText('Requested configuration')).toBeVisible()
  mock.claim(true, { model: 'anthropic/claude-opus-5', account_label: 'Other subscription', reasoning_effort: 'xhigh' })
  await dialog(page).getByRole('button', { name: 'Refresh status' }).click()
  await expect(dialog(page).getByRole('heading', { name: 'Managed session connected' })).toBeVisible()
  const reported = dialog(page).locator('.requested').nth(1)
  await expect(reported).toContainText('anthropic/claude-opus-5')
  await expect(reported).toContainText('Other subscription')
  await expect(reported).toContainText('Extra high')
  await expect(dialog(page).getByText('This differs from the requested configuration.')).toBeVisible()
  await expect(dialog(page).locator('.requested').first()).toContainText('workspace-build')
})

test('an unreported session does not reuse the requested labels as fact', async ({ page }) => {
  const mock = await mockStartAgent(page)
  await open(page, true)
  await ready(page, true)
  await dialog(page).getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog(page).getByText('Requested configuration', { exact: true })).toBeVisible()
  mock.claim(true, { model: null, account_label: null, reasoning_effort: null })
  await dialog(page).getByRole('button', { name: 'Refresh status' }).click()
  const reported = dialog(page).locator('.requested').nth(1)
  await expect(reported).toContainText('Unknown')
  await expect(reported).not.toContainText('workspace-build')
  await expect(dialog(page).getByText('The session has not reported every field.')).toBeVisible()
  await expect(dialog(page).getByText('This differs from the requested configuration.')).toHaveCount(0)
})

test('changing host replaces the granted model', async ({ page }, info) => {
  test.setTimeout(120_000)
  await mockStartAgent(page, { catalog: 'two-hosts' })
  await open(page, true)
  await expect(dialog(page).getByLabel('Model', { exact: true }).locator('option:checked')).toHaveText('workspace-build')
  // Risk: a longer host explanation after a harness switch moves every later
  // selector. Reserve the tallest candidate and require exactly zero height change.
  const host = dialog(page).getByLabel('Host', { exact: true })
  const harness = dialog(page).getByLabel('Harness', { exact: true })
  const hostNotes = dialog(page).locator('.host-notes')
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
    await page.evaluate(async () => { await document.fonts.ready })
    const height = await hostNotes.evaluate(element => element.getBoundingClientRect().height)
    expect(height).toBeGreaterThan(0)
    await expectStableControls({
      controls: { host, harness, selectors: dialog(page).locator('.pair').first(), account: dialog(page).getByLabel('Account', { exact: true }), model: dialog(page).getByLabel('Model', { exact: true }), thinking: dialog(page).getByLabel('Thinking', { exact: true }), queue: dialog(page).getByRole('button', { name: 'Queue run', exact: true }), cancel: dialog(page).getByRole('button', { name: 'Cancel', exact: true }) },
      scrollAreas: { dialog: dialog(page) },
      interactions: ['claude', 'codex'].map(value => ({ name: `switch harness to ${value}`, run: async () => {
        await harness.selectOption(value)
        await expect(hostNotes.locator('p:not(.sizer)')).toHaveText(value === 'claude' ? 'Agents can start now. The quota shows once this account reports it.' : 'Chosen because it has the account with the most allowance left.')
        expect(await hostNotes.evaluate(element => element.getBoundingClientRect().height) - height).toBe(0)
        await dialog(page).screenshot({ path: info.outputPath(`aeon-1511-host-note-${width}-${theme}-${value}.png`), animations: 'disabled' })
      } })),
    })
  }
  const heightBeforeHostChange = await hostNotes.evaluate(element => element.getBoundingClientRect().height)
  await host.selectOption({ label: 'Laptop' })
  await expect(hostNotes.locator('p:not(.sizer)')).toHaveCount(0)
  expect(await hostNotes.evaluate(element => element.getBoundingClientRect().height) - heightBeforeHostChange).toBe(0)
  await expect(dialog(page).getByLabel('Harness', { exact: true }).locator('option:checked')).toHaveText('Claude Code')
  await expect(dialog(page).getByLabel('Model', { exact: true }).locator('option:checked')).toHaveText('other-model')
  await expect(dialog(page).getByRole('option', { name: 'workspace-build' })).toHaveCount(0)
  await expect(dialog(page).getByLabel('Thinking', { exact: true }).locator('option:checked')).toHaveText('Medium')
})

test('same-name hosts distinguish offline pairings and queue only the chosen account', async ({ page }, info) => {
  const mock = await mockStartAgent(page, { catalog: 'same-name-hosts' })
  await open(page, true)
  const frame = dialog(page)
  const host = frame.getByLabel('Host', { exact: true })
  const account = frame.getByLabel('Account', { exact: true })
  const oldProbe = mock.catalog.hosts.find(item => item.daemon_id === 'workstation-old')!.harnesses[0].accounts[0]
  const offlineLabel = `Work Mac · Offline · last seen ${absoluteTime(oldProbe.last_probe_at)}`
  await expect(host.locator('option')).toHaveText(['Choose a host', 'Work Mac · Online', offlineLabel])
  await expect(host).toHaveValue('workstation')
  await expect(account).toHaveValue(mock.account.id)
  await host.selectOption('workstation-old')
  await expect(account).not.toHaveValue(mock.account.id)
  const oldAccount = await account.inputValue()
  expect(oldAccount).not.toBe(mock.account.id)
  await expect(account.locator('option').filter({ hasText: 'Workspace account' })).toHaveCount(1)
  const checkHostControls = () => expectStableControls({
    controls: {
      host, hostGroup: frame.locator('.pair').first(), account,
      harness: frame.getByLabel('Harness', { exact: true }), model: frame.getByLabel('Model', { exact: true }),
      thinking: frame.getByLabel('Thinking', { exact: true }),
      queue: frame.getByRole('button', { name: 'Queue run', exact: true }), cancel: frame.getByRole('button', { name: 'Cancel', exact: true }),
    },
    scrollAreas: { frame },
    interactions: ['workstation', 'workstation-old', 'workstation'].map(id => ({ name: `choose ${id}`, run: async () => {
      await host.selectOption(id)
      await expect(account).toHaveValue(id === 'workstation' ? mock.account.id : oldAccount)
    } })),
  })
  // Evidence is local to this run, with no visual equality assertions.
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: 1000 })
    await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
    await host.selectOption('workstation-old')
    await frame.screenshot({ path: info.outputPath(`same-name-hosts-${width}-${theme}.png`) })
    await checkHostControls()
  }
  // When both same-name daemons report online, retain distinct ordinals and
  // exercise the same controls with the longer/shorter host labels.
  oldProbe.last_probe_at = new Date(Date.parse(mock.catalog.as_of) - 2000).toISOString()
  await frame.getByRole('button', { name: 'Cancel', exact: true }).click()
  await open(page, true)
  await expect(host.locator('option')).toHaveText(['Choose a host', 'Work Mac · Online · pairing 1', 'Work Mac · Online · pairing 2'])
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: 1000 })
    await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
    await host.selectOption('workstation-old')
    await frame.screenshot({ path: info.outputPath(`same-name-online-hosts-${width}-${theme}.png`) })
    await checkHostControls()
  }
  await frame.getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(frame.getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
  await expect(frame.locator('.requested')).toContainText('Work Mac · Online · pairing 1')
  const launches = mock.calls.filter(call => call.method === 'POST' && call.path.endsWith('/runs'))
  expect(launches).toHaveLength(1)
  expect(launches[0].body?.requested_account_id).toBe(mock.account.id)
})

test('failed prerequisites keep launch disabled and read-only people have no action', async ({ page }) => {
  await mockStartAgent(page, { forbidden: true })
  await open(page)
  await expect(dialog(page).getByRole('alert')).toContainText('Permission denied')
  await expect(dialog(page).getByRole('button', { name: 'Queue run', exact: true })).toBeDisabled()
  await page.unrouteAll({ behavior: 'wait' })
  await mockStartAgent(page, { readOnly: true })
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Start agent', exact: true })).toHaveCount(0)
  await newAgent(page).click()
  await expect(page.getByRole('menuitem', { name: /^Start agent manually…/ })).toHaveCount(0)
})

for (const theme of ['light', 'dark']) {
  test(`keyboard, axe and phone layout in ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme: theme as 'light' | 'dark' })
    await mockStartAgent(page)
    await open(page, true)
    await ready(page, true)
    await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
    await dialog(page).getByLabel('Host', { exact: true }).focus()
    await page.keyboard.press('Tab')
    await expect(dialog(page).getByLabel('Harness', { exact: true })).toBeFocused()
    const result = await new AxeBuilder({ page }).include('.launch-dialog').withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'best-practice']).analyze()
    expect(result.violations).toEqual([])
    expect(await dialog(page).evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true)
    await page.keyboard.press('Escape')
    await expect(dialog(page)).toBeVisible()
    await expect(dialog(page).getByLabel('Harness', { exact: true })).not.toBeFocused()
    await page.keyboard.press('Escape')
    await expect(dialog(page)).not.toBeVisible()
    await expect(newAgent(page)).toBeFocused()
  })
}

for (const theme of ['light', 'dark'] as const) for (const width of [390, 1024, 1440]) {
  test(`AEON-1508: lead queue replaces manual setup and keeps controls still at ${width} ${theme}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const mock = await mockStartAgent(page, { theme, ticketTitle: 'Agentensteuerung für gemeinsame Entwicklungsprojekte und zuverlässige Übergaben an den Projektverantwortlichen' })
    await page.route('**/api/projects/p-pharos/lead', route => route.fulfill({ json: {
      project_id: 'p-pharos', revision: 4, generation: 1, session_id: null, state: 'paused', reason: 'admission_not_configured', process_active: false, automatic_launch_enabled: false,
    } }))
    const queuePosts: unknown[] = []
    const queueItems: QueueWireEntry[] = []
    let refuse = true
    await page.route('**/api/queue**', async route => {
      if (route.request().method() === 'GET') return route.fulfill({ json: { items: queueItems, manual_order: false, capacity: { queued_hours: queueItems.length * 2, parallel_runs: 0, work_hours: null, warning: false } } })
      queuePosts.push(route.request().postDataJSON())
      if (refuse) return route.fulfill({ status: 503, json: { error: 'The lead queue is unavailable. Try again.' } })
      const entry: QueueWireEntry = {
        node_id: 'n-1', project_id: 'p-pharos', key: 'PHAROS-11', title: 'Agentensteuerung', state: 'open', priority: 'high', estimate_hours: 2,
        queued: { run_id: 'lead-demand', position: 1, by: { ...me, kind: 'person' }, at: new Date().toISOString(), target_agent_id: null, expected_agent_id: null, model_profile_id: null, expected_start_at: null, waiting: true, wait_reason: 'Automatic launch is not enabled' },
      }
      queueItems.push(entry)
      return route.fulfill({ json: entry })
    })
    await open(page)
    const sheet = dialog(page), actions = sheet.locator('footer'), primary = actions.locator('button[type="submit"]'), secondary = actions.locator('button[type="button"]').first()
    const catalogsBefore = mock.calls.filter(call => call.path === '/api/agent-accounts/catalog').length
    await sheet.getByRole('searchbox').fill('PHAROS-11')
    await expectStableControls({
      controls: { close: sheet.getByRole('button', { name: 'Close start agent' }), cancel: actions.getByRole('button', { name: 'Cancel', exact: true }) },
      scrollAreas: { body: sheet.locator('.launch-body') },
      interactions: [{ name: 'choose configured work', run: async () => {
        await sheet.getByRole('button', { name: /^PHAROS-11 Agentensteuerung/ }).click()
        await expect(sheet.getByText('This project starts work through its lead.', { exact: true })).toBeVisible()
        await expect(primary).toHaveAccessibleName('Queue for lead')
      } }],
    })
    await expect(sheet.getByLabel('Host', { exact: true })).toHaveCount(0)
    await expect(sheet.getByLabel('Harness', { exact: true })).toHaveCount(0)
    await expect(sheet.getByText('Automatic launch is not enabled for this workspace yet. Queued work stays queued.', { exact: true })).toBeVisible()
    await expect(primary).toBeEnabled()
    expect(mock.calls.filter(call => call.path === '/api/agent-accounts/catalog')).toHaveLength(catalogsBefore)
    await sheet.screenshot({ path: info.outputPath(`AEON-1508-lead-hint-${width}-${theme}.png`) })
    await expectStableControls({
      controls: { actions, primary, secondary, change: sheet.getByRole('button', { name: 'Change ticket', exact: true }), ...(width === 390 ? { frame: sheet } : {}) },
      scrollAreas: { body: sheet.locator('.launch-body') },
      interactions: [
        { name: 'failed queue request', run: async () => {
          await primary.click()
          await expect(sheet.getByRole('alert')).toHaveText('The lead queue is unavailable. Try again.')
          await expect(primary).toBeEnabled()
          await expect(secondary).toHaveAccessibleName('Cancel')
        } },
        { name: 'successful retry', run: async () => {
          refuse = false
          const mac = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent))
          await primary.focus()
          const before = { primary: await primary.boundingBox(), secondary: await secondary.boundingBox() }
          expect(before.primary).not.toBeNull()
          expect(before.secondary).not.toBeNull()
          await page.keyboard.press(mac ? 'Meta+Enter' : 'Control+Enter')
          await expect(sheet.getByText('Queued for the lead. Nothing has started yet.', { exact: true })).toBeVisible()
          await expect(primary).toHaveAccessibleName('Queued')
          await expect(primary).toBeDisabled()
          await expect(secondary).toHaveAccessibleName('Close')
          await expect(secondary.locator('kbd')).toHaveText('Esc')
          await expect(secondary).toBeEnabled()
          await expect(primary.locator(':scope > svg > path')).toHaveAttribute('d', 'm3.4 8.4 3 3 6.2-6.8')
          expect(await primary.boundingBox(), 'queued primary stays in exactly the same slot').toEqual(before.primary)
          expect(await secondary.boundingBox(), 'Close stays in exactly the same slot as Cancel').toEqual(before.secondary)
        } },
      ],
    })
    expect(queuePosts).toEqual([{ node_id: 'n-1' }, { node_id: 'n-1' }])
    expect(queueItems).toHaveLength(1)
    expect(queueItems[0]?.queued.target_agent_id).toBeNull()
    expect(mock.calls.filter(call => call.method !== 'GET' && (call.path.startsWith('/api/work-orders') || call.path === '/api/agent-accounts/pins'))).toEqual([])
    await sheet.screenshot({ path: info.outputPath(`AEON-1508-queued-${width}-${theme}.png`) })
    await secondary.click()
    await expect(sheet).not.toBeVisible()
    expect(queuePosts).toEqual([{ node_id: 'n-1' }, { node_id: 'n-1' }])
    expect(queueItems).toHaveLength(1)
  })
}

for (const theme of ['light', 'dark'] as const) {
  for (const width of [1600, 390]) {
    test(`screenshot ${width} ${theme}`, async ({ page }, info) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.emulateMedia({ colorScheme: theme })
      await mockStartAgent(page)
      await page.goto('/settings/accounts')
      await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
      const accounts = page.locator('#agent-accounts')
      // AEON-686: the Accounts overview lists each account as a row; its logins open in the docked panel.
      await expect(accounts.locator('.list-row[data-account]').first()).toBeVisible()
      await accounts.screenshot({ path: info.outputPath(`acu1-accounts-${width}-${theme}.png`) })
      await open(page)
      await ready(page)
      await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
      await dialog(page).screenshot({ path: info.outputPath(`acu1-start-${width}-${theme}.png`) })
    })
  }
}

for (const theme of ['light', 'dark'] as const) for (const width of [390, 1024, 1440]) {
  test(`New menu restores its trigger without moving header controls at ${width} ${theme}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await mockStartAgent(page)
    await page.route('**/api/me', route => route.fulfill({ json: {
      principal: { id: me.id, name: me.name, kind: 'person', roles: ['admin'] },
      tenant: { id: 't1', name: 'Agentensteuerung für gemeinsame Entwicklungsprojekte und zuverlässige Übergaben' },
    } }))
    await page.goto('/agents')
    await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
    const header = page.locator('.agents-page .page-head')
    await expectStableControls({
      controls: { new: newAgent(page), more: header.getByRole('button', { name: 'More agent actions', exact: true }) },
      scrollAreas: { header },
      interactions: [
        { name: 'open New menu', run: async () => {
          await newAgent(page).click()
          await expect(page.getByRole('menuitem', { name: /^Start agent manually…/ })).toBeVisible()
          await page.screenshot({ path: info.outputPath(`agents-header-${width}-${theme}.png`), fullPage: true })
        } },
        { name: 'open start dialog', run: async () => {
          await page.getByRole('menuitem', { name: /^Start agent manually…/ }).click()
          await expect(dialog(page)).toBeVisible()
        } },
        { name: 'close start dialog', run: async () => {
          await dialog(page).getByRole('button', { name: 'Close start agent', exact: true }).click()
          await expect(dialog(page)).not.toBeVisible()
          await expect(newAgent(page)).toBeFocused()
        } },
      ],
    })
  })
}
