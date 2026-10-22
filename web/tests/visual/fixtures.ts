// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Page } from '@playwright/test'
import { fixtures, liveAgent, me, mockWork } from '../work-fixtures'
import { agentData, mockAgents } from '../agents-fixtures'
import { accessWorld, mockAccess, COORDINATOR } from '../access-fixtures'
import { knowledgeWorld, mockKnowledge } from '../knowledge-fixtures'
import { mockDecisionDesk } from '../decision-desk-fixtures'
import { businessData, mockBusiness } from '../business-fixtures'
import { mockQuoteEditor, QUOTE_ID, NODES, quoteDocument } from '../quote-inspector-fixtures'
import { mockPublicQuote } from '../quote-list-fixtures'
import { mockSettings, settingsData } from '../settings-fixtures'
import { mockRulesScale, scaleImportFile } from '../rules-scale-fixtures'
import { mockModels } from '../models-simple-fixtures'
import { openModelsSettings } from '../models-settings-page'
import { deliveryMetrics, mockDelivery } from '../delivery-numbers-fixtures'
import { mockFlow } from '../delivery-flow-fixtures'
import { capacityWorld, NOW as CAPACITY_NOW } from '../capacity-fixtures'
import { mockDailyPlan } from '../agents-daily-fixtures'
import { mockStartAgent } from '../start-agent-fixtures'
import type { VisualSetup, Theme } from './checkpoint'
import { visualChrome } from './chrome-fixture'

export const LONG = 'Projektübergreifende Entwicklungszusammenarbeit und langfristige Infrastrukturverantwortung mit vollständiger Qualitätsprüfung'
export const NOW = Date.parse('2026-09-23T12:00:00Z')
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
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible()
  await expect(page.locator('.sessions [data-row]').first()).toBeVisible()
  const lead = page.locator(`[data-row="s:${data.sessions[0]!.id}"]`)
  await lead.locator('.history-toggle').click()
  await expect(page.locator(`[data-row="s:${data.sessions[6]!.id}"]`)).toBeVisible()
}

export const sessionChat: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  const data = agentsData(), worker = data.sessions[0]!
  worker.display_label = 'Release coordination'
  worker.activity = 'idle'
  const message = data.messages[0]!
  Object.assign(message, { body: `## Übergabebericht\n\n${LONG}.\n\n\`git status --short\` bleibt lesbar.\n\n[Prüfbericht öffnen](https://example.invalid/report)`,
    sender_session_id: worker.id, sender_principal_id: worker.agent_principal_id, recipient_principal_id: me.id,
    created_at: new Date(NOW - 60_000).toISOString() })
  await mockAgents(page, data, { now: () => NOW })
  await page.goto(`/agents/${worker.id}?tab=messages`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await panel.getByRole('tab', { name: /^Chat/ }).click()
  await expect(panel.getByRole('textbox', { name: 'Message to Release coordination', exact: true })).toBeVisible()
  await expect(panel.getByText('Übergabebericht', { exact: true })).toBeVisible()
}

export const decisionDesk: VisualSetup = async (page, _profile, theme) => {
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
  await expect(page.locator('.tooltip')).toContainText('Doctrine needs an authorized source')
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
  await openModelsSettings(page)
  await page.getByRole('button', { name: 'For everyone' }).click()
  await page.locator('[data-pick="design"]').click()
  await expect(page.locator('.mdl-pop .pm-lock')).toBeVisible()
}

export const deliveryNumbers: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  await mockDelivery(page, () => ({ status: 200, body: deliveryMetrics() }))
  await mockFlow(page, { empty: true })
  await page.goto('/p/AEON/delivery')
  await expect(page.getByTestId('delivery-summary')).toBeVisible()
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

export const quoteEditor: VisualSetup = async (page, _profile, theme) => {
  await work(page, theme)
  const document = quoteDocument()
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
  await mockPublicQuote(page, { acceptable: true, now: NOW })
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
  await page.clock.setFixedTime(CAPACITY_NOW)
  await work(page, theme)
  const data = agentsData(CAPACITY_NOW), capacity = capacityWorld()
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  await mockAgents(page, data, { capacity, now: () => CAPACITY_NOW })
  await mockDailyPlan(page, { example: 'pace' })
  await page.goto('/settings/capacity')
  await page.locator('button[data-pace="claude"]').click()
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
  const check = ticket.getByRole('checkbox').first()
  await expect(check, 'Tick fixture prerequisite: focusable checklist in the ticket workspace').toBeEnabled()
  await check.focus()
  await expect(check).toBeFocused()
  await expect(ticket.locator('.ws-composer')).toBeVisible()
}

// A real revision-checked cell save exposes Undo while other cells remain mixed.
export const accountUseMatrix: VisualSetup = async (page, _profile, theme) => {
  await page.clock.setFixedTime(CAPACITY_NOW)
  await work(page, theme)
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
