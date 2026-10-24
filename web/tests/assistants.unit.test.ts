// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia } from 'pinia'
import { createSSRApp, defineComponent, h, reactive } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { assistantOrder, assistantStatus, budgetPercent, listAssistants, policyChange, retainAssistantOrder, unavailableAssistantFacts, type Assistant, type AssistantFacts } from '../src/lib/assistants'
import { readAssistantRoutines } from '../src/lib/assistantRoutines'
import type { DeskProjection } from '../src/lib/decisionDesk'
import { useAssistants } from '../src/stores/assistants'
import { assistantCopy } from '../src/lib/assistantsCopy'
import AssistantsSection from '../src/components/agents/AssistantsSection.vue'
import AssistantPanel from '../src/components/agents/AssistantPanel.vue'
import { assistantEvidence } from './agents-fixtures'

vi.mock('../src/lib/assistants', async original => ({ ...await original<typeof import('../src/lib/assistants')>(), listAssistants: vi.fn() }))
vi.mock('../src/lib/assistantRoutines', async original => ({ ...await original<typeof import('../src/lib/assistantRoutines')>(), readAssistantRoutines: vi.fn() }))
vi.mock('../src/stores/session', () => ({ useSession: () => viewer }))
vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ load: async () => {}, byId: () => ({ routeKey: 'AEON' }) }) }))
vi.mock('../src/stores/decisionDesk', () => ({ useDecisionDesk: () => desk }))
vi.mock('../src/lib/sharedRequests', () => ({ subscribeBrowserEvents: (events: string[], changed: (event?: string) => void) => { routineHints = { events, changed }; return () => {} } }))
vi.mock('../src/lib/authz', () => ({ can: () => true, permissionsKnown: () => true, ensurePermissions: async () => {}, onAccessRevalidate: () => () => {} }))
const viewer = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'owner', kind: 'person' } } })
const desk = reactive<{ projection: DeskProjection | null; failures: number; error: string; refresh: () => Promise<boolean> }>({ projection: null, failures: 0, error: '', refresh: vi.fn(async () => true) })
let pinia: ReturnType<typeof createPinia>
let routineHints: { events: string[]; changed: (event?: string) => void }
beforeEach(() => {
  pinia = createPinia(); setActivePinia(pinia)
  viewer.identity.principal.id = 'owner'; viewer.identity.principal.kind = 'person'
  desk.projection = null; desk.failures = 0; desk.error = ''
  vi.mocked(desk.refresh).mockClear()
  vi.mocked(listAssistants).mockReset(); vi.mocked(readAssistantRoutines).mockReset()
  vi.mocked(listAssistants).mockResolvedValue({ items: assistantEvidence(Date.now(), 'owner').items, truncated: false })
  vi.mocked(readAssistantRoutines).mockResolvedValue({ rows: [], truncated: false })
})
afterEach(() => { disposePinia(pinia); vi.useRealTimers() })

async function renderApprovalViews(partial: boolean, count = 2) {
  const now = Date.parse('2026-10-23T16:00:00Z')
  const { items, facts } = assistantEvidence(now, 'owner')
  const amy = items.find(item => item.display_name === 'Amy')!
  const values: AssistantFacts = { ...facts[amy.principal_id]!, approvals: facts[amy.principal_id]!.approvals!.slice(0, count), approvalsTruncated: partial }
  const store = useAssistants()
  vi.spyOn(store, 'loadHistory').mockResolvedValue()
  vi.spyOn(store, 'loadRoutines').mockResolvedValue()
  const app = createSSRApp({ render: () => h('main', [
    h(AssistantsSection, { items: [amy], facts: { [amy.principal_id]: values }, state: 'ready', error: '', truncated: false, canConnect: true, canApprove: true, now, sortEpoch: 0, selected: '', owner: 'owner' }),
    h(AssistantPanel, { item: amy, facts: values, now, docked: true, sheet: false, layout: 'side', sidebarAvailable: true }),
  ]) })
  app.component('RouterLink', defineComponent({ props: ['to'], setup: (props, { slots }) => () => h('a', { href: props.to }, slots.default?.()) }))
  return { html: await renderToString(app), id: amy.principal_id }
}

it('assistant approval status lines use singular and plural forms in both languages', () => {
  // Risk: one pending approval reads as plural, including a partial count.
  for (const [language, one, two, prefix] of [
    ['en', '1 action needs an approval', '2 actions need an approval', 'At least '],
    ['de', '1 Aktion braucht eine Freigabe', '2 Aktionen brauchen eine Freigabe', 'Mindestens '],
  ] as const) {
    const words = assistantCopy(language)
    expect(words.approvalLine(1)).toBe(one)
    expect(words.approvalLine(2)).toBe(two)
    expect(words.approvalLine(1, true)).toBe(prefix + one)
    expect(words.approvalLine(2, true)).toBe(prefix + two)
  }
})

it('assistant policy refusals reach row activity without claiming execution or dropping configured approvers', async () => {
  // Risk: denied work looks active, or saving an unrelated field erases the
  // server's approver/reset policy because the controls are not exposed yet.
  const store = useAssistants()
  await store.load()
  const amy = store.items.find(item => item.display_name === 'Amy')!
  const now = Date.now()
  const event = { id: 71, type: 'assistant.policy_evaluated', at: new Date(now).toISOString(), actor_principal_id: 'lead', after: { principal_id: amy.principal_id, allowed: false, reason: 'assistant_weekly_budget_exhausted' } }
  store.events = [event]
  expect(store.facts[amy.principal_id]?.lastActivity).toMatchObject({ text: assistantCopy().policyBudgetDenied, policyOnly: true })
  expect(assistantStatus(amy, store.facts[amy.principal_id]!, now)).not.toBe('working')
  expect(policyChange({ ...event, after: { ...event.after, reason: 'assistant_project_denied' } })).toBe(assistantCopy().policyProjectDenied)
  expect(policyChange({ ...event, after: { ...event.after, reason: 'assistant_scope_denied: nodes.write' } })).toBe(assistantCopy().policyScopeDenied)
  const { cloneAssistantPolicy } = await import('../src/lib/assistants')
  const original = { ...amy.policy, approver_person_ids: ['owner'], weekly_reset_day: 0 }
  const draft = cloneAssistantPolicy(original)
  expect(draft).toEqual(original)
  draft.approver_person_ids!.push('another')
  expect(original.approver_person_ids).toEqual(['owner'])
})

it('assistant partial approval links expose explanations to the shared tooltip', async () => {
  // Risk: native titles leave keyboard users without the partial-list warning.
  for (const partial of [true, false]) {
    const { html, id } = await renderApprovalViews(partial)
    const row = html.match(/<span class="assistant-approvals"[^>]*>([\s\S]*?)<\/span>/)?.[1] ?? ''
    const next = html.match(/<section class="assistant-block"[^>]*><h3[^>]*>Next<\/h3>([\s\S]*?)<\/section>/)?.[1] ?? ''
    const links = [row, next].flatMap(part => [...part.matchAll(/<a\b[^>]*>/g)].map(match => match[0]).filter(tag => tag.includes(`href="/decision-desk?assistant=${id}"`)))
    expect(links).toHaveLength(2) // The row link and the panel's Next link.
    for (const link of links) {
      if (partial) {
        expect(link).toContain(`title="${assistantCopy().truncated}"`)
        expect(link).toContain(`data-tip="${assistantCopy().truncated}"`)
      } else {
        expect(link).not.toContain('title=')
        expect(link).not.toContain('data-tip=')
      }
    }
  }
})

it('assistant approval counts and panel status lines announce complete updates politely', async () => {
  // Risk: live Desk changes are silent, or the whole identity poll is announced.
  for (const count of [2, 1]) {
    const { html } = await renderApprovalViews(false, count)
    for (const tag of [html.match(/<span class="assistant-approvals"[^>]*>/)?.[0], html.match(/<p class="panel-status-line"[^>]*>/)?.[0]]) {
      expect(tag).toContain('aria-live="polite"')
      expect(tag).toContain('aria-atomic="true"')
    }
    expect(html).toContain(`${count} to approve`)
    expect(html).toContain(assistantCopy().approvalLine(count))
    expect(html.match(/<section[^>]*class="assistants glass-card[^"]*"[^>]*>/)?.[0]).not.toContain('aria-live=')
  }
})

it('assistant identity polls reuse discovered routines instead of repeating the owner and history sweep', async () => {
  // Risk: each 20-second identity poll repeats hundreds of per-routine reads.
  const store = useAssistants()
  await store.load()
  await vi.waitFor(() => expect(store.facts[store.items[0]!.principal_id]?.routines).toEqual([]))
  expect(readAssistantRoutines).toHaveBeenCalledTimes(1)
  await store.load()
  await store.load()
  expect(readAssistantRoutines).toHaveBeenCalledTimes(1)
  vi.useFakeTimers()
  const stop = store.watchPage()
  expect(routineHints.events).toEqual(expect.arrayContaining(['recurrence.updated', 'recurrence.agent_result', 'assistant.policy_updated', 'assistant.disabled']))
  routineHints.changed('recurrence.updated')
  routineHints.changed('recurrence.agent_result')
  await vi.advanceTimersByTimeAsync(400)
  expect(readAssistantRoutines).toHaveBeenCalledTimes(2)
  routineHints.changed('assistant.policy_updated')
  await vi.advanceTimersByTimeAsync(400)
  expect(readAssistantRoutines).toHaveBeenCalledTimes(3)
  await store.loadRoutines() // Opening the panel explicitly refreshes its data.
  expect(readAssistantRoutines).toHaveBeenCalledTimes(4)
  routineHints.changed('approval.proposed')
  routineHints.changed('approval.denied')
  await vi.advanceTimersByTimeAsync(400)
  expect(desk.refresh).toHaveBeenCalledTimes(1)
  expect(readAssistantRoutines).toHaveBeenCalledTimes(4)
  stop()
})

it('assistant approvals use only the current persons Desk items and expose partial or failed coverage', async () => {
  // Risk: unconnected approvals hide waiting work, or another principal's asks
  // and a partial Desk page are presented as this assistant's complete count.
  const store = useAssistants()
  await store.load()
  const amy = store.items.find(item => item.display_name === 'Amy')!
  const scout = store.items.find(item => item.display_name === 'Scout')!
  const now = Date.now()
  const row = (id: string, principal: string): DeskProjection['items'][number] => ({ id, kind: 'approval', title: `Outward action ${id}`, created_at: new Date(now).toISOString(), revision: 1, held: true, project_id: 'project', ticket_key: 'AEON-1', asked_by: { id: principal, name: 'Same display name' }, href: `/decision-desk?item=a:${id}`, source: `/api/approvals/${id}` })
  expect(store.facts[amy.principal_id]?.approvals).toBeNull()
  desk.projection = { items: [row('one', amy.principal_id), row('two', amy.principal_id), row('unrelated', 'another-principal'), { ...row('doctrine', amy.principal_id), kind: 'doctrine' }], counts: { open: 4, held: 4, chores: 0 }, has_more: false, as_of: new Date(now).toISOString() }
  expect(store.facts[amy.principal_id]?.approvals?.map(item => item.id)).toEqual(['a:one', 'a:two'])
  expect(store.facts[scout.principal_id]?.approvals).toEqual([])
  expect(assistantStatus(amy, store.facts[amy.principal_id]!, now)).toBe('waiting')
  expect(assistantOrder(store.items, store.facts, now)[0]).toBe(amy.principal_id)
  expect(store.facts[amy.principal_id]?.approvalsTruncated).toBe(false)
  expect(store.facts[amy.principal_id]?.approvals?.[0]).toMatchObject({ title: 'Outward action one', target: 'AEON-1', href: `/decision-desk?assistant=${amy.principal_id}&item=a%3Aone` })
  desk.projection.has_more = true
  expect(store.facts[amy.principal_id]?.approvalsTruncated).toBe(true)
  expect(assistantCopy('en').toApprove(2, true)).toBe('2+ to approve')
  expect(assistantCopy('de').toApprove(2, true)).toBe('2+ zur Freigabe')
  desk.projection.has_more = false; desk.failures = 1
  expect(store.facts[amy.principal_id]?.approvalsTruncated).toBe(true)
  expect(store.facts[amy.principal_id]?.approvalsError).toBeTruthy()
  desk.projection = null
  expect(store.facts[amy.principal_id]?.approvals).toBeNull()
  viewer.identity.principal.id = 'other-owner'
  expect(store.items).toEqual([])
  expect(store.facts).toEqual({})
})

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
  const render = async (rows: Assistant[], values = facts) => {
    const app = createSSRApp(AssistantsSection, { items: rows, facts: values, state: 'ready', error: '', truncated: false, canConnect: true, canApprove: true, now, sortEpoch: 0, selected: '', owner: 'owner' })
    app.component('RouterLink', defineComponent({ props: ['to'], setup: (props, { slots }) => () => h('a', { href: props.to }, slots.default?.()) }))
    return renderToString(app)
  }
  const html = await render(items)
  expect(html).toContain('assistants glass-card')
  expect(html).toContain('Waiting for you')
  expect(html).toContain('$8.40 of $10')
  expect(html).toContain('84 %')
  expect(html).toContain(`href="/decision-desk?assistant=${amy.principal_id}"`)
  expect(html).toContain('2 to approve')
  expect(html).toContain('Grok · ok')
  const unknown = await render([amy], { [amy.principal_id]: unavailableAssistantFacts() })
  expect(unknown).toContain('Status unavailable')
  for (const field of ['last-column', 'budget-unknown', 'routines-column', 'adapter-column']) {
    expect(unknown).toMatch(new RegExp(`class="${field}"[^>]*title="Not available yet"[^>]*>—</span>`))
  }
  expect(unknown).toMatch(/class="assistant-approvals"[^>]*><span title="Open approvals are unavailable\." data-tip="Open approvals are unavailable\."[^>]*>—<\/span>/)
  expect(unknown).not.toContain('>Unavailable<')
  const empty = await render([])
  expect(empty).toContain('assistants glass-card')
  expect(empty).toContain('Assistants · 0')
  expect(empty).toContain('No assistants yet.')
  expect(assistantCopy('en').budget).toBe('Budget')
  expect(assistantCopy('en').perWeek).toBe('Per week')
  expect(assistantCopy('de').budget).toBe('Budget')
  expect(assistantCopy('de').perWeek).toBe('Pro Woche')
  expect(assistantCopy('en').ran('Inbox triage 1', '30 min ago')).toBe('Ran Inbox triage 1 · 30 min ago')
  expect(assistantCopy('de').ran('Posteingang', 'vor 30 Minuten')).toBe('Posteingang ausgeführt · vor 30 Minuten')
  expect(assistantCopy('de').statuses.waiting).toBe('Wartet auf Freigabe')
  expect(policyChange({ id: 1, at: new Date(now).toISOString(), type: 'assistant.policy_updated', actor_principal_id: 'owner', before: amy, after: { ...amy, policy: { ...amy.policy, weekly_budget: { amount: '15', unit: 'USD' } } } })).toBe('Budget: $10 to $15')
  expect(assistantCopy('de').budgetChange('$10', '$15')).toBe('$10 auf $15')
})
