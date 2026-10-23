// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { assistantOrder, assistantStatus, budgetPercent, policyChange, retainAssistantOrder, unavailableAssistantFacts, type Assistant } from '../src/lib/assistants'
import { assistantCopy } from '../src/lib/assistantsCopy'
import AssistantsSection from '../src/components/agents/AssistantsSection.vue'
import { assistantEvidence } from './agents-fixtures'

it('assistant facts retain the specified status order, exact budget words and independent Desk links', async () => {
  // Risk: plausible zero/healthy values, wrong ordering, or an approval link
  // swallowing the whole-row action would misrepresent an assistant's work.
  const now = Date.parse('2026-10-23T16:00:00Z') // Every fixture time derives from this clock.
  const { items, facts } = assistantEvidence(now, 'owner')
  const [scout, amy] = items as [Assistant, Assistant]
  const row = (name: string, n: number) => ({ ...amy, principal_id: name, display_name: name, policy: { ...amy.policy, weekly_budget: { ...amy.policy.weekly_budget } }, revision: n })
  const working = row('Working', 2), paused = row('Paused', 3), down = row('Down', 4), setup = row('Setup', 5)
  facts[working.principal_id] = { ...unavailableAssistantFacts(), adapter: { state: 'ok', lastReceipt: new Date(now).toISOString() }, lastActivity: { at: new Date(now - 1000).toISOString(), text: 'Replied' } }
  facts[paused.principal_id] = { ...unavailableAssistantFacts(), spend: { amount: '10', daily: [] } }
  facts[down.principal_id] = { ...unavailableAssistantFacts(), adapter: { state: 'down', lastReceipt: new Date(now - 7200_000).toISOString(), error: 'Vendor unavailable' } }
  facts[setup.principal_id] = { ...unavailableAssistantFacts(), adapter: { state: 'setup', lastReceipt: null } }
  const all = [setup, scout, paused, working, down, amy]
  expect(assistantOrder(all, facts, now)).toEqual([amy.principal_id, down.principal_id, working.principal_id, scout.principal_id, paused.principal_id, setup.principal_id])
  const original = assistantOrder(all, facts, now)
  facts[scout.principal_id]!.approvals = [{ id: 'new', title: 'Send', target: 'AEON', at: new Date(now).toISOString(), href: '/decision-desk' }]
  expect(retainAssistantOrder(all, original)).toEqual(original)
  expect(assistantStatus(paused, facts[paused.principal_id]!, now)).toBe('paused')
  expect(assistantStatus(amy, unavailableAssistantFacts(), now)).toBe('unavailable')
  expect(budgetPercent(scout.policy, facts[scout.principal_id]!)).toBeCloseTo(84)
  expect(budgetPercent(amy.policy, unavailableAssistantFacts())).toBeNull()
  expect(budgetPercent(amy.policy, { ...facts[amy.principal_id]!, spend: { amount: '8', daily: [] } })).toBe(80)
  const exactPolicy = { ...amy.policy, weekly_budget: { ...amy.policy.weekly_budget, amount: '999999999999.999999' } }
  expect(budgetPercent(exactPolicy, { ...facts[amy.principal_id]!, spend: { amount: '999999999999.999998', daily: [] } })).toBeLessThan(100)
  expect(budgetPercent(exactPolicy, { ...facts[amy.principal_id]!, spend: { amount: '999999999999.999999', daily: [] } })).toBe(100)
  const app = createSSRApp(AssistantsSection, { items, facts, state: 'ready', error: '', truncated: false, canConnect: true, canApprove: true, now, sortEpoch: 0, selected: '', owner: 'owner' })
  app.component('RouterLink', defineComponent({ props: ['to'], setup: (props, { slots }) => () => h('a', { href: props.to }, slots.default?.()) }))
  const html = await renderToString(app)
  expect(html).toContain('Waiting for you')
  expect(html).toContain('$8.40 of $10')
  expect(html).toContain('84 %')
  expect(html).toContain(`href="/decision-desk?assistant=${amy.principal_id}"`)
  expect(html).toContain('2 to approve')
  expect(html).toContain('Grok · ok')
  expect(assistantCopy('de').statuses.waiting).toBe('Wartet auf Freigabe')
  expect(policyChange({ id: 1, at: new Date(now).toISOString(), type: 'assistant.policy_updated', actor_principal_id: 'owner', before: amy, after: { ...amy, policy: { ...amy.policy, weekly_budget: { amount: '15', unit: 'USD' } } } })).toBe('Weekly budget: $10 → $15')
})
