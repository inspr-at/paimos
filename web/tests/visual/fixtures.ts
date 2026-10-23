// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { expect, type Page } from '@playwright/test'
import { fixtures, liveAgent, me, mockWork } from '../work-fixtures'
import { agentData, mockAgents } from '../agents-fixtures'
import { accessWorld, mockAccess, COORDINATOR } from '../access-fixtures'
import { knowledgeWorld, mockKnowledge } from '../knowledge-fixtures'
import { mockDecisionDesk } from '../decision-desk-fixtures'
import { businessData, mockBusiness } from '../business-fixtures'
import { mockQuoteEditor, QUOTE_ID, NODES, quoteDocument, type QuoteDoc } from '../quote-inspector-fixtures'
import { mockPublicQuote } from '../quote-list-fixtures'
import { mockSettings, settingsData } from '../settings-fixtures'
import { mockRulesScale, scaleImportFile } from '../rules-scale-fixtures'
import { mockModels } from '../models-simple-fixtures'
import { mockLeadActions } from '../lead-flow-fixtures'
import { deliveryMetrics, mockDelivery } from '../delivery-numbers-fixtures'
import { mockFlow } from '../delivery-flow-fixtures'
import { capacityWorld, NOW as CAPACITY_NOW } from '../capacity-fixtures'
import { mockDailyPlan } from '../agents-daily-fixtures'
import { mockStartAgent } from '../start-agent-fixtures'
import { mockChatThread } from '../chat-thread-fixtures'
import type { QuoteProfileSnapshot } from '../../src/lib/quotes/types'
import type { VisualSetup, Theme } from './checkpoint'
import { visualChrome } from './chrome-fixture'

export const LONG = 'Projektübergreifende Entwicklungszusammenarbeit und langfristige Infrastrukturverantwortung mit vollständiger Qualitätsprüfung'
export const NOW = Date.parse('2026-09-23T12:00:00Z')
async function healthyLead(page: Page) {
  await page.route('**/api/projects/*/lead', route => {
    if (route.request().method() !== 'GET') return route.fallback()
    const lead = { project_id: new URL(route.request().url()).pathname.split('/')[3]!, revision: 0, generation: 0,
      session_id: null, state: 'none' as const, reason: '', process_active: false, automatic_launch_enabled: false }
    return route.fulfill({ json: { ...lead, actions: mockLeadActions(lead) } })
  })
}
const nodes = { 'p-pharos': { key: 'PRJ-17', title: LONG }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' },
  'n-1': { key: 'PHAROS-11', title: LONG }, 'n-2': { key: 'PHAROS-12', title: 'Connector recovery' }, 'n-a1': { key: 'AEON-1', title: 'Aeon foundation' } }
function agentsData(now = NOW) {
  return agentData({ me: me.id, now, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
    tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes })
}
async function work(page: Page, theme: Theme) {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.projects[0]!.title = LONG
  data.nodes.find(node => node.id === 'n-1')!.title = LONG
  data.nodes.find(node => node.id === 'n-epic')!.title = `Übergeordnetes Arbeitsziel · ${LONG}`
  await mockWork(page, data, { admin: true })
  await visualChrome(page, data, NOW)
  return data
}

export const projects: VisualSetup = async (page, _profile, theme) => {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.projects[0]!.title = LONG
  data.live.push(...[1, 2, 3].map(index => liveAgent({ project_id: 'p-pharos', session_id: `live-${index}`,
    principal_id: `33333333-3333-4333-8333-33333333333${index}`, name: `Worker ${index}`, ticket: null }, index)))
  await mockWork(page, data, { liveTruncated: true })
  await visualChrome(page, data, NOW)
  await page.goto('/?view=cards')
  await expect(page.locator('.live-chip').first()).toBeVisible()
  await expect(page.getByText('Studio infrastructure', { exact: true }).first()).toBeVisible()
}

export const tickets: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  await page.goto('/p/PHAROS/tickets?group_by=status&priority=high,medium')
  await expect(page.getByRole('grid', { name: 'Tickets' })).toBeVisible()
  await expect(page.locator('#row-n-1 .title-text')).toHaveAttribute('data-tip', LONG)
}

export const agents: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  const data = agentsData()
  data.sessions[0]!.display_label = 'Release coordination'
  data.sessions[1]!.parent_harness_session_id = data.sessions[0]!.id
  data.sessions[2]!.parent_harness_session_id = data.sessions[1]!.id
  data.sessions[2]!.project_id = 'p-pharos'
  data.sessions[2]!.project = nodes['p-pharos']
  data.sessions[6]!.parent_harness_session_id = data.sessions[0]!.id
  data.sessions[1]!.host = 'entwicklungsarbeitsplatz.infrastruktur.example.invalid'
  data.accounts[0]!.label = `Gemeinsam verwaltetes Entwicklungskonto · ${LONG}`
  await mockAgents(page, data, { now: () => NOW })
  // This reference depicts a successful lead read. Missing action projections
  // remain covered separately by the lead-panel refusal test.
  await healthyLead(page)
  const permissionRead = page.waitForResponse(response => new URL(response.url()).pathname === '/api/approvals' && response.request().method() === 'GET')
  await page.goto('/agents')
  const permissions = await permissionRead
  expect(permissions.status(), 'The Agents reference requires a successful permission-request read').toBe(200)
  expect(await permissions.json()).toEqual(data.approvals)
  await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible()
  await expect(page.locator('.sessions [data-row]').first()).toBeVisible()
  await expect(page.getByRole('alert').filter({ hasText: 'Permission requests could not be loaded:' })).toHaveCount(0)
  const lead = page.locator(`[data-row="s:${data.sessions[0]!.id}"]`)
  await lead.locator('.history-toggle').click()
  await expect(page.locator(`[data-row="s:${data.sessions[6]!.id}"]`)).toBeVisible()
}

async function setupSessionChatUnavailable(page: Page, theme: Theme) {
  await work(page, theme)
  const data = agentsData(), worker = data.sessions[0]!
  worker.display_label = 'Release coordination'
  worker.activity = 'idle'
  const message = data.messages[0]!
  Object.assign(message, { body: `## Übergabebericht\n\n${LONG}.\n\n\`git status --short\` bleibt lesbar.\n\n[Prüfbericht öffnen](https://example.invalid/report)`,
    sender_session_id: worker.id, sender_principal_id: worker.agent_principal_id, recipient_principal_id: me.id,
    created_at: new Date(NOW - 60_000).toISOString() })
  await mockAgents(page, data, { now: () => NOW })
  await healthyLead(page)
  // This separate refusal fixture retains the legacy post/composer and typed 503.
  const conversation = '7c000000-0000-4000-8000-000000001674'
  await page.route('**/api/**', route => {
    const request = route.request(), path = new URL(request.url()).pathname
    if (request.method() !== 'GET') return route.fallback()
    if (path === `/api/projects/${worker.project_id}/harness-sessions/${worker.id}/native-context`) return route.fulfill({ status: 503,
      json: { reason: 'feature_disabled', request_id: null, retryable: false } })
    if (path === `/api/chat-sessions/${worker.id}/thread`) return route.fulfill({ json: {
      contract: 'chat-v1', conversation_id: conversation, role: { contract: 'chat-v1' }, revision: '1', binding_epoch: '1',
      readiness: { state: 'offline', capabilities: [], reason: 'session_offline' },
    } })
    if (path === `/api/chat-threads/${conversation}/messages`) return route.fulfill({ json: {
      contract: 'chat-v1', items: [], has_more_before: false, has_more_after: false,
      before_cursor: null, after_cursor: null, snapshot_cursor: 'p02', migration_epoch: '0',
    } })
    if (path === `/api/chat-threads/${conversation}/live`) return route.fulfill({ contentType: 'text/event-stream',
      body: 'event: ready\ndata: {"contract":"chat-live-v1"}\n\n' })
    return route.fallback()
  })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await panel.getByRole('tab', { name: /^Chat/ }).click()
  await expect(panel.getByRole('textbox', { name: 'Message to Release coordination', exact: true })).toBeVisible()
  await expect(panel.getByText('Übergabebericht', { exact: true })).toBeVisible()
}
export const sessionChat: VisualSetup = async (page, _profile, theme) => {
  const { worker } = await mockChatThread(page, { theme, now: NOW, clock: false, today: true, reference: true })
  await visualChrome(page, fixtures(), NOW, { agents: false })
  await healthyLead(page)
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel.getByRole('textbox', { name: 'Message to AEON-LEAD', exact: true })).toBeVisible()
  const thread = panel.getByRole('list', { name: 'Messages', exact: true })
  await expect(thread.locator('.msg.desk')).toHaveCount(2)
  await expect(thread.getByRole('article', { name: /^Decision Desk: Merge AEON-1182/ })).toContainText('Answered: Yes, merge now')
  await expect(thread.getByRole('article', { name: /^Decision Desk: Ship the 135 notes/ })).toContainText('Waiting for you')
  await expect(thread.getByRole('separator', { name: 'Today', exact: true })).toHaveCount(1)
  await expect(thread.locator('.msg.mine').last()).toContainText('Start it now. Keep the CI fixes in their own PR.')
  await page.evaluate(() => document.fonts.ready)
  await panel.locator('.thread-scroll').evaluate(element => { element.scrollTop = 0 })
  await expect(thread.getByRole('separator', { name: 'Today', exact: true })).toBeInViewport({ ratio: 1 })
  for (const card of await thread.locator('.msg.desk').all()) await expect(card).toBeInViewport({ ratio: 1 })
  await expect(thread.locator('.msg.mine').last()).toBeInViewport({ ratio: 1 })
}
export const sessionChatUnavailable: VisualSetup = async (page, _profile, theme) => setupSessionChatUnavailable(page, theme)

export const decisionDesk: VisualSetup = async (page, profile, theme) => {
  await work(page, theme)
  const world = await mockDecisionDesk(page, { theme, long: true, phone: true })
  await visualChrome(page, fixtures(), NOW, { agents: false })
  world.projectionTime = NOW
  world.approval.expires_at = new Date(NOW + 3_600_000).toISOString()
  world.approval.proposed_at = new Date(NOW - 600_000).toISOString()
  world.questions[0]!.input.context = `${LONG}.\n\n${LONG}.`
  for (const question of world.questions) { question.created_at = new Date(NOW - 120_000).toISOString(); question.updated_at = question.created_at }
  await page.goto('/decision-desk')
  await page.getByTestId('desk-row-q:question-1').click()
  await expect(page.getByTestId('stamp-doctrine')).toBeDisabled()
  await expect(page.getByTestId('desk-choices')).toBeVisible()
  await page.getByTestId('stamp-doctrine').scrollIntoViewIfNeeded()
  await page.getByTestId('stamp-doctrine').press('Tab')
  await page.keyboard.press('Shift+Tab')
  const reason = 'Not available: the agent didn’t name the rule this answer would change, so there is nothing to propose a change to.'
  await expect(page.getByTestId('stamp-doctrine')).toHaveAccessibleDescription(reason)
  if (profile === 'phone') {
    // AEON-1688 moved the phone reason into the row. Preserve that approved
    // presentation and the full accessible reason, rather than opening an
    // obsolete tooltip over its neighbouring choice.
    await expect(page.getByTestId('stamp-doctrine')).toBeFocused()
    await expect(page.getByTestId('stamp-words-doctrine')).toHaveText('Not available: the agent named no rule.')
    await expect(page.locator('.tooltip')).toHaveCount(0)
  } else await expect(page.locator('.tooltip')).toHaveText(reason)
}

export const access: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  const world = accessWorld()
  world.agents.find(agent => agent.principal_id === COORDINATOR)!.name = LONG
  world.roles.find(role => role.key === 'admin')!.description = `${LONG}. Nur im aktuellen Berechtigungsrahmen zulässig.`
  await mockAccess(page, world)
  await page.goto('/settings/access/agents')
  await page.getByRole('button', { name: `Role of ${LONG}: Member. Change`, exact: true }).click()
  const picker = page.getByRole('dialog', { name: `Role of ${LONG}`, exact: true })
  await picker.getByRole('radio', { name: /^Admin/ }).click()
  await expect(picker.getByRole('region', { name: 'What changes' })).toBeVisible()
}

export const knowledge: VisualSetup = async (page, profile, theme) => {
  await work(page, theme)
  const data = knowledgeWorld(), entry = data.entries.find(entry => entry.slug === 'deploy-release')!
  entry.body = `# Freigabeprüfung\n\n${LONG}.\n\n## Prüfplan\n\n| Schritt | Ergebnis |\n| --- | --- |\n| Berechtigungen | Vor der Änderung erneut prüfen |\n| Übergabe | Vollständigen Bericht erhalten |\n\n${LONG}.`
  await mockKnowledge(page, data)
  await page.goto(profile === 'desktop' ? '/p/PHAROS/knowledge?entry=runbook/deploy-release' : '/p/PHAROS/knowledge/runbook/deploy-release')
  await expect(page.locator('.e-body table')).toBeVisible()
  if (profile === 'desktop') await expect(page.locator('.entry-page.dock')).toBeVisible()
}

export const models: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  await mockModels(page, { longLabels: true, fallback: true })
  // The isolated Models harness has no application header. References must
  // exercise the real shell, whose main pane owns Settings scrolling.
  await page.goto('/settings/models')
  await expect(page.locator('[data-models-ready="true"]')).toBeVisible()
  await expect(page.locator('.app-header')).toBeVisible()
  await page.getByRole('button', { name: 'For everyone' }).click()
  const picker = page.locator('[data-pick="design"]')
  await picker.evaluate(element => {
    if (!element.closest('main#main')) throw new Error('Models capture must scroll the settings pane')
    // Limit the native nearest scroll to the Settings pane. Scrolling all
    // ancestors can push the shell/header out of the approved capture frame.
    const options = { block: 'nearest', inline: 'nearest', container: 'nearest' } as const
    element.scrollIntoView(options)
  })
  await picker.click()
  await expect(page.locator('.mdl-pop .pm-lock')).toBeVisible()
  await expect(page.locator('.app-header')).toBeVisible()
}

export const deliveryNumbers: VisualSetup = async (page, profile, theme) => {
  await work(page, theme)
  await mockDelivery(page, () => ({ status: 200, body: deliveryMetrics() }))
  await mockFlow(page, { empty: true })
  await page.goto('/p/AEON/delivery')
  await expect(page.getByTestId('delivery-summary')).toBeVisible()
  await page.locator('main').evaluate(panel => panel.scrollTo({ top: 0, left: 0, behavior: 'instant' }))
  await expect(page.locator('main')).toHaveJSProperty('scrollTop', 0)
  if (profile === 'phone') {
    await page.evaluate(() => document.fonts.ready)
    const heading = page.getByRole('heading', { name: 'Delivery', exact: true })
    await heading.evaluate(element => {
      element.scrollIntoView({ block: 'start', behavior: 'instant' })
      const header = document.querySelector('.app-header')!
      // main owns page-flow scrolling on phones. Correct scrollIntoView's
      // scroll-padding offset so the heading starts just below the app header.
      document.querySelector('main')!.scrollBy({
        top: element.getBoundingClientRect().top - header.getBoundingClientRect().height, behavior: 'instant',
      })
    })
    await expect(heading).toBeInViewport({ ratio: 1 })
    await expect(page.getByTestId('delivery-summary').locator('.big')).toHaveText(/\d+ of 13 numbers are on target\./)
    await expect(page.getByTestId('delivery-summary').locator('.big')).toBeInViewport({ ratio: 1 })
  }
}

export const hours: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  await mockBusiness(page, businessData())
  await page.goto('/business/hours')
  await expect(page.locator('[data-entry="e-4"]')).toBeVisible()
  await page.locator('[data-entry="e-4"]').focus()
  await page.keyboard.press('e')
  await expect(page.getByRole('form', { name: 'Edit entry' })).toBeVisible()
}

// Use the actual bundled INSPR profile and serve only its allowlisted assets.
async function visualQuoteProfile(page: Page): Promise<QuoteProfileSnapshot> {
  const root = new URL('../../../internal/business/quotes/profiles/inspr/', import.meta.url)
  const { definition } = JSON.parse(readFileSync(new URL('profile.json', root), 'utf8'))
  const snapshot = { id: '7e5a0000-0000-4000-8000-000000001674', revision: 1, definition }
  await page.route(`**/api/quote-profiles/${snapshot.id}*`, route => route.fulfill({ json: {
    ...snapshot, name: 'INSPR', archived_at: null, created_at: new Date(NOW).toISOString(), updated_at: new Date(NOW).toISOString(),
  } }))
  const allowed = new Set([...definition.fonts.map((face: { asset_id: string }) => face.asset_id), definition.footer.asset_id])
  await page.route('**/api/quote-profiles/assets/**', route => {
    const asset = decodeURIComponent(new URL(route.request().url()).pathname.split('/assets/')[1]!)
    if (!allowed.has(asset)) throw new Error(`Unexpected INSPR quote asset: ${asset}`)
    return route.fulfill({ body: readFileSync(new URL(asset, root)), contentType: asset.endsWith('.woff2') ? 'font/woff2' : 'image/svg+xml' })
  })
  return snapshot
}

export const quoteEditor: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  const document: QuoteDoc = quoteDocument()
  document.profile = await visualQuoteProfile(page)
  document.sections[0]!.nodes[0]!.text = `${LONG}. **Vollständige Prüfung** vor jeder Freigabe.`
  await mockQuoteEditor(page, document)
  await page.goto(`/business/quotes/${QUOTE_ID}`)
  await expect(page.locator('.quote-document')).toBeVisible()
  await page.locator(`[data-node-id="${NODES[0]}"]`).first().click()
  const panel = page.getByRole('complementary', { name: 'Format' })
  if (!await panel.isVisible()) await page.getByRole('button', { name: 'Format panel' }).click()
  await panel.getByRole('tab', { name: 'Text', exact: true }).click()
  await expect(panel).toBeVisible()
}

export const publicQuote: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  await mockPublicQuote(page, { acceptable: true, now: NOW, profile: await visualQuoteProfile(page) })
  await page.goto('/offers/sel-demo/tok-example')
  await expect(page.locator('#pq-title')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Prüfen und annehmen', exact: true })).toBeVisible()
}

export const agentRules: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  await mockSettings(page, settingsData())
  await mockRulesScale(page, { state: 'empty' })
  await page.route('**/api/rules/doctrine/inbox/summary', route => route.fulfill({ json: { pending: 0 } }))
  await page.route('**/api/rules/doctrine', route => route.fulfill({ json: { revision: 0, sources: [] } }))
  await page.goto('/settings/agent-rules')
  await page.getByRole('button', { name: 'Import rules' }).click()
  await page.locator('#draft-import-file').setInputFiles(scaleImportFile())
  await expect(page.getByRole('button', { name: /Import 14 sets as drafts/ })).toBeVisible()
  await page.getByText('Secrets', { exact: true }).click()
}

export const capacity: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  await page.clock.setFixedTime(CAPACITY_NOW)
  const data = agentsData(CAPACITY_NOW), capacity = capacityWorld()
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  await mockAgents(page, data, { capacity, now: () => CAPACITY_NOW })
  await mockDailyPlan(page, { example: 'pace' })
  await page.goto('/settings/capacity')
  await page.evaluate(() => document.fonts.ready)
  const picker = page.locator('button[data-pace="claude"]')
  // Finish layout and scroll before opening: FloatingPanel closes when its
  // trigger moves, so late font metrics must never choose a theme's state.
  await picker.evaluate(element => element.scrollIntoView({ block: 'center', behavior: 'instant' }))
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
  await picker.click()
  await expect(picker).toHaveAttribute('aria-expanded', 'true')
  await expect(page.getByRole('dialog', { name: /^Daily limit for/ })).toBeVisible()
}

export const startAgent: VisualSetup = async (page, _profile, theme) => {
  await page.clock.setFixedTime(CAPACITY_NOW)
  await mockStartAgent(page, { theme, now: CAPACITY_NOW, expertStart: false, catalog: 'claude',
    wait: { code: 'allowance', until: new Date(CAPACITY_NOW + 1_800_000).toISOString(), run_now_allowed: false } })
  await visualChrome(page, fixtures(), CAPACITY_NOW, { agents: false, models: false })
  await mockDailyPlan(page, { example: 'pace' })
  await page.route('**/api/decision-desk/projection*', route => route.fulfill({ json: { items: [], counts: { open: 0, held: 0, chores: 0 }, has_more: false, as_of: new Date(CAPACITY_NOW).toISOString() } }))
  await page.route('**/api/agent-accounts/quota-outlook*', route => route.fulfill({ json: {
    generated_at: new Date(CAPACITY_NOW).toISOString(), anchor_hour: new Date(Math.floor(CAPACITY_NOW / 3_600_000) * 3_600_000).toISOString(), history_hours: 168, horizon_hours: 168, truncated: false, groups: [],
  } }))
  await page.goto('/agents')
  await page.getByRole('button', { name: 'New: start a lead, attach a session or connect a machine', exact: true }).click()
  await page.getByRole('menuitem', { name: /^Run once/ }).click()
  const dialog = page.getByRole('dialog', { name: 'Run once', exact: true })
  await dialog.getByRole('textbox', { name: 'Task', exact: true }).fill(LONG)
  await dialog.getByLabel('Context', { exact: true }).selectOption({ label: 'Create a ticket in a project' })
  await dialog.getByLabel('Project', { exact: true }).selectOption('p-pharos')
  await expect(dialog.getByRole('checkbox', { name: 'May change files', exact: true })).not.toBeChecked()
  await expect(dialog.getByText('Read-only: no tools, no repository access, no external actions.', { exact: true })).toBeVisible()
  await expect(dialog.locator('.settings-status')).toBeVisible()
  const fold = dialog.locator('.settings-toggle')
  if (await fold.getAttribute('aria-expanded') === 'true') await fold.click()
  await expect(fold).toHaveAttribute('aria-expanded', 'false')
  await expect(dialog.locator('.run-summary-line')).toContainText('Runs on')
}

export const ticketWorkspace: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  await mockAgents(page, agentsData(), { now: () => NOW })
  await page.goto('/p/PHAROS/PHAROS-11')
  const ticket = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(ticket.locator('.attachments')).toBeVisible()
  // Tick 2026-10-23: the checklist stays read-only. Its attachment preview is
  // the approved keyboard-focus checkpoint, using the real button and ring.
  const preview = ticket.locator('.attachments').getByRole('button', { name: /^Open / }).first()
  await expect(preview).toBeVisible()
  await page.keyboard.press('Tab')
  await preview.focus()
  await expect(preview).toBeFocused()
  await expect(preview).toHaveJSProperty('tagName', 'BUTTON')
  await expect(preview).toBeEnabled()
  expect(await preview.evaluate(element => element.matches(':focus-visible'))).toBe(true)
  await expect(ticket.locator('.ws-composer')).toBeVisible()
}

// A real revision-checked cell save exposes Undo while other cells remain mixed.
export const accountUseMatrix: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  await page.clock.setFixedTime(CAPACITY_NOW)
  const data = agentsData(CAPACITY_NOW), capacity = capacityWorld()
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  await mockAgents(page, data, { capacity, now: () => CAPACITY_NOW })
  const contexts = [{ id: 'ctx-default', name: 'Default', kind: 'default' }, { id: 'ctx-long', name: LONG, kind: 'regular' }]
  const accounts = data.accounts.slice(0, 3).map((account, index) => ({ id: account.id, label: index === 0 ? LONG : account.label,
    harness: account.harness, plan: account.plan, billing_mode: 'subscription', owner_id: null }))
  const cells = accounts.map(account => ({ account_id: account.id, context_id: 'ctx-default', allowed: true }))
  let revision = 5
  await page.route('**/api/account-use**', async route => {
    if (route.request().method() === 'GET') return route.fulfill({ json: {
      rules: { new_accounts: 'ask', new_contexts: 'ask', new_projects: 'default', new_models: 'allow', revision,
        enforced_at: null, confirmation_required: false, confirmed_at: null }, accounts,
      contexts: contexts.map(context => ({ ...context, archived_at: null, new_accounts_override: null, revision: 1 })),
      cells, next_account: null, next_context: null, running_outside: [], running_outside_truncated: false,
      holding_projects: { count: 0, first: [] },
    } })
    const body = route.request().postDataJSON()
    if (body.expected_revision !== revision) return route.fulfill({ status: 409, json: { error: 'account_use_revision_conflict' } })
    const changes = body.changes as typeof cells
    expect(changes).toHaveLength(1)
    const undo = changes.map(cell => ({ ...cell, allowed: cells.some(old => old.account_id === cell.account_id && old.context_id === cell.context_id) }))
    cells.push(...changes)
    await route.fulfill({ json: { revision: ++revision, changes, undo } })
  })
  await page.goto('/settings/accounts')
  const table = page.getByRole('table', { name: 'Where accounts may work' })
  await expect(table).toBeVisible()
  await table.locator(`[data-use-cell="${accounts[0]!.id}:ctx-long"]`).click()
  await expect(page.locator('.toast').getByRole('button', { name: 'Undo', exact: true })).toBeVisible()
}
