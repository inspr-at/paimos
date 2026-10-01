<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { listNodes, type ListItem, APIError } from '../lib/api'
import { can, ensurePermissions, onAccessChange } from '../lib/authz'
import { createScope, scopeOwner } from '../lib/identityScope'
import { pendingApprovals, heldRequests, scopeLabel, canDecideApproval } from '../lib/agentState'
import type { Approval, MessagePage } from '../lib/agents'
import type { Journey } from '../lib/journey'
import { loadUsageDashboard } from '../lib/usageDashboard'
import type { UsageDashboard, AllowanceWindow } from '../lib/usageFormat'
import { formatDollars } from '../lib/planning'
import { absoluteTime } from '../lib/work'
import { useSession } from '../stores/session'
import { useProjects } from '../stores/projects'
import { briefingCost, briefingJSON, briefingRange, eventFact, loadBriefingEvents, loadBriefingOutcomes, loadBriefingPreference, outcomeFact, recommendedStep, saveBriefingPreference, validBriefingTime, type BriefingFact, type BriefingNeed, type BriefingPreference, type BriefingRange } from '../lib/morningBriefing'
import AppIcon from '../components/AppIcon.vue'
import PlanningCell from '../components/work/PlanningCell.vue'

const session = useSession(), projects = useProjects()
const person = computed(() => session.identity?.principal.kind === 'person')
const owner = () => person.value ? scopeOwner(session.identity) : ''
const scope = createScope(owner), lane = scope.lane(), writes = scope.lane()
const range = ref<BriefingRange | null>(null), preference = ref<BriefingPreference | null>(null)
const time = ref('08:00'), loading = ref(false), saving = ref(false), error = ref(''), saveError = ref('')
const delivered = ref<BriefingFact[]>([]), failures = ref<BriefingFact[]>([]), needs = ref<BriefingNeed[]>([])
const usage = ref<UsageDashboard | null>(null), tickets = ref<ListItem[]>([]), notices = ref<string[]>([])
const showAll = ref(false)
const next = computed(() => recommendedStep(needs.value, failures.value))
const cost = computed(() => usage.value ? briefingCost(usage.value.totals) : null)
const usageSource = computed(() => range.value ? `/api/usage/dashboard?${new URLSearchParams({ from: range.value.from, to: range.value.to })}` : '')
const visibleTickets = computed(() => tickets.value.filter(t => t.planning?.cost && can('harness.read', t.project?.id ?? undefined)))
const sections = computed(() => [
  { key: 'delivered', title: 'Finished and delivered', empty: 'No completion or delivery recorded in this window.', rows: delivered.value },
  { key: 'failures', title: 'Reviews and checks', empty: 'No failed review, failed CI check or revert recorded in this window.', rows: failures.value },
])
function accountUsage(account: AllowanceWindow) {
  if (account.used === null) return 'Usage not reported'
  if (account.unit === 'cost_micros') return `${formatDollars(account.used / 1_000_000)} of ${formatDollars(account.allowance / 1_000_000)} used`
  return `${account.used.toLocaleString()} of ${account.allowance.toLocaleString()} ${account.unit} used`
}
const humanActions = new Set(['confirm_brief', 'decide', 'approve_requirements', 'approve_candidate', 'approve_deploy', 'approve_permit'])
function clear() {
  range.value = null; preference.value = null; delivered.value = []; failures.value = []; needs.value = []
  usage.value = null; tickets.value = []; notices.value = []; error.value = ''; saveError.value = ''; loading.value = false; saving.value = false
}
function load() {
  if (!owner()) return
  loading.value = true; error.value = ''
  void lane.run(({ after, signal }) => after(Promise.all([loadBriefingPreference(signal), projects.load(), ensurePermissions()]), ([pref]) => {
    if (projects.error) throw new Error('Projects could not be loaded. Try again before reading the briefing.')
    preference.value = pref ?? {}
    time.value = validBriefingTime(pref?.time) ? pref.time : '08:00'
    // A refresh keeps the original window; opening the page again starts a new one.
    range.value ??= briefingRange(pref)
    const window = range.value
    return after(read(window, signal), data => {
      delivered.value = data.delivered; failures.value = data.failures; needs.value = data.needs
      usage.value = data.usage; tickets.value = data.tickets; notices.value = data.notices
      // Mark only a complete, successful visit. A failed/partial source never skips unseen work.
      if (!data.complete) return
      const value = { ...pref, time: time.value, last_visit: window.to }
      return after(saveBriefingPreference(value, signal), () => { preference.value = value })
    })
  }), { failed: cause => { error.value = cause instanceof Error ? cause.message : 'Briefing could not be loaded.' }, settled: () => { loading.value = false } })
}
async function read(window: BriefingRange, signal: AbortSignal) {
  const notes: string[] = []; let complete = true
  async function source<T>(label: string, pending: Promise<T>): Promise<T | null> {
    try { return await pending } catch (cause) {
      if (signal.aborted || cause instanceof APIError && cause.status === 401) throw cause
      if (cause instanceof APIError && cause.status === 403) notes.push(`${label} is unavailable with your current access.`)
      else { complete = false; notes.push(`${label} could not be loaded. Retry to include it.`) }
      return null
    }
  }
  const projectList = [...projects.projects]
  // Resolve the existing project permission cache before admitting project costs or person actions.
  for (let i = 0; i < projectList.length; i += 3) await Promise.all(projectList.slice(i, i + 3).map(p => ensurePermissions(p.id)))
  const usageAllowed = can('harness.read') || projectList.some(p => can('harness.read', p.id))
  const [outcomes, events, approvals, dashboard] = await Promise.all([
    source('Outcome history', loadBriefingOutcomes(window, signal)),
    source('Event history', loadBriefingEvents(window, signal, can('harness.read'))),
    source('Approvals', briefingJSON<Approval[]>('/approvals?limit=200', signal)),
    usageAllowed ? source('Usage', loadUsageDashboard(window, signal)) : Promise.resolve(null),
  ])
  if (outcomes?.truncated || events?.truncated || dashboard?.truncated) { complete = false; notes.push('This window has more records than the briefing can show. Your previous visit has been kept.') }
  if (window.capped) notes.push('Your last visit was more than 366 days ago. This briefing covers the latest 366 days.')
  if (approvals?.length === 200) { complete = false; notes.push('Approvals show the latest 200 requests. Open Agents to check older requests.') }
  const ids = [...new Set([...(outcomes?.items.map(o => o.ticket_node_id) ?? []), ...(events?.items.filter(e => eventFact(e, 'p', 't')).map(e => e.node_id).filter((id): id is string => !!id) ?? []), ...(approvals?.filter(a => a.resource_kind === 'node').map(a => a.resource_id).filter((id): id is string => !!id) ?? [])])]
  const rows: ListItem[] = []
  for (let i = 0; i < ids.length; i += 200) {
    const page = await source('Ticket details', listNodes({ ids: ids.slice(i, i + 200), limit: 200 }, { signal }))
    if (page) rows.push(...page.items)
  }
  // Run telemetry is attached to its work order. Its ticket parent supplies the item link.
  const parentIds = [...new Set(rows.filter(n => n.kind_slug === 'work_order' && n.parent?.kind_slug === 'ticket').map(n => n.parent!.id))].filter(id => !rows.some(n => n.id === id))
  for (let i = 0; i < parentIds.length; i += 200) {
    const page = await source('Ticket details', listNodes({ ids: parentIds.slice(i, i + 200), limit: 200 }, { signal }))
    if (page) rows.push(...page.items)
  }
  const nodeById = new Map(rows.map(t => [t.id, t])), byProject = new Map(projectList.map(p => [p.id, p]))
  const facts: BriefingFact[] = [], failed: BriefingFact[] = []
  for (const item of outcomes?.items ?? []) {
    const fact = outcomeFact(item, byProject.get(item.project_id)?.routeKey ?? '')
    if (fact) (item.kind === 'ticket_done' || item.kind === 'released' ? facts : failed).push(fact)
  }
  for (const event of events?.items ?? []) {
    const bound = nodeById.get(event.node_id ?? '')
    const node = bound?.kind_slug === 'work_order' && bound.parent?.kind_slug === 'ticket' ? nodeById.get(bound.parent.id) : bound
    if (!node || !['ticket', 'work_order'].includes(node.kind_slug) || event.type === 'run.telemetry' && !can('harness.read', node.project?.id)) continue
    const fact = eventFact(event, byProject.get(node.project?.id ?? '')?.routeKey ?? '', node.key)
    // Both logs may capture the initial delivered transition. Keep the outcome source once.
    const alreadyDelivered = outcomes?.items.some(o => o.ticket_node_id === node.id && o.kind === 'ticket_done' && o.payload.to_state === 'delivered')
    if (fact && !(event.type === 'node.updated' && alreadyDelivered)) facts.push(fact)
  }
  const waiting: BriefingNeed[] = []
  const pending = pendingApprovals(approvals ?? [], Date.now())
  for (const approval of pending) {
    const node = nodeById.get(approval.resource_id ?? ''), projectId = node?.kind_slug === 'project' ? node.id : node?.project?.id
    if (!canDecideApproval(approval, permission => can(permission, projectId))) continue
    const href = `/agents?needs=${encodeURIComponent(`a:${approval.id}`)}`
    waiting.push({ id: `a:${approval.id}`, title: scopeLabel(approval.scope), detail: approval.rationale, href, source: href })
  }
  for (let i = 0; i < projectList.length; i += 3) await Promise.all(projectList.slice(i, i + 3).map(async project => {
    const root = `/projects/${encodeURIComponent(project.id)}`
    const [messages, journey] = await Promise.all([
      can('inbox.manage', project.id) ? source(`Human requests in ${project.routeKey}`, briefingJSON<MessagePage>(`${root}/messages?pending=true&limit=200`, signal)) : Promise.resolve(null),
      can('journey.act', project.id) ? source(`Decisions in ${project.routeKey}`, briefingJSON<Journey>(`${root}/journey`, signal)) : Promise.resolve(null),
    ])
    if (messages?.items.length === 200) { complete = false; notes.push(`Human requests in ${project.routeKey} may be incomplete. Open Agents for the full queue.`) }
    for (const message of heldRequests(messages?.items ?? [])) {
      const href = `/agents?needs=${encodeURIComponent(`m:${message.id}`)}`
      waiting.push({ id: `m:${message.id}`, title: `Human check · ${project.routeKey}`, detail: message.body, href, source: href })
    }
    const action = journey?.next_action
    if (action?.available && humanActions.has(action.key) && !pending.some(a => a.id === action.approval_request_id)) {
      const href = `/p/${encodeURIComponent(project.routeKey)}/journey`
      waiting.push({ id: `j:${project.id}`, title: `${project.routeKey} · ${action.label}`, detail: action.reason ?? '', href, source: href })
    }
  }))
  facts.sort((a, b) => Date.parse(b.at) - Date.parse(a.at)); failed.sort((a, b) => Date.parse(b.at) - Date.parse(a.at))
  // Project reads can finish in any order; keep the next step deterministic.
  const approvalOrder = new Map(pending.map((a, i) => [`a:${a.id}`, i]))
  waiting.sort((a, b) => (approvalOrder.get(a.id) ?? Infinity) - (approvalOrder.get(b.id) ?? Infinity) || a.id.localeCompare(b.id))
  const factIds = new Set([...parentIds, ...(outcomes?.items.map(o => o.ticket_node_id) ?? []), ...(events?.items.filter(e => eventFact(e, 'p', 't')).map(e => e.node_id) ?? [])])
  return { delivered: facts, failures: failed, needs: waiting, usage: dashboard, tickets: rows.filter(t => factIds.has(t.id)), notices: notes, complete }
}
function saveTime() {
  if (!validBriefingTime(time.value) || !preference.value || loading.value) return
  saving.value = true; saveError.value = ''
  const value = { ...preference.value, time: time.value }
  void writes.run(({ after, signal }) => after(saveBriefingPreference(value, signal), () => { preference.value = value }), {
    failed: () => { saveError.value = 'Reminder time could not be saved. Try again.' }, settled: () => { saving.value = false },
  })
}
const stopAccess = onAccessChange(() => { scope.reset(); clear(); load() })
watch(() => scopeOwner(session.identity), () => { scope.reset(); clear(); load() }, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { stopAccess(); scope.dispose() })
</script>

<template>
  <main class="briefing-page" aria-labelledby="briefing-title">
    <header class="page-head">
      <p class="eyebrow">Since your last briefing</p>
      <h1 id="briefing-title">Morning briefing</h1>
      <p v-if="range" class="summary">{{ range.first ? 'Your first briefing · last 24 hours' : 'Since your last visit' }} · {{ absoluteTime(range.from) }} to {{ absoluteTime(range.to) }}</p>
      <div v-if="person" class="briefing-tools">
        <label for="briefing-time">Daily reminder at</label><input id="briefing-time" v-model="time" class="field" type="time" :disabled="loading || saving" @change="saveTime" />
        <span class="muted">your device’s local time</span>
        <button type="button" class="btn sm" :disabled="loading || saving" @click="load"><AppIcon name="refresh" :size="14" />Refresh</button>
      </div>
      <p v-if="saveError" role="alert">{{ saveError }}</p>
    </header>
    <p v-if="!person" class="glass-card note">Sign in as a person to read your briefing.</p>
    <p v-else-if="loading" class="glass-card note" role="status">Reading your briefing sources…</p>
    <template v-else>
      <p v-if="error" class="glass-card note" role="alert">{{ error }} <button class="btn sm" type="button" @click="load">Try again</button></p>
      <aside v-if="notices.length" class="glass-card note" aria-label="Briefing coverage"><p v-for="notice in notices" :key="notice">{{ notice }}</p></aside>
      <section v-if="next" class="glass-card next-step" aria-label="Recommended next step"><p class="eyebrow">One next step</p><RouterLink :to="next.href">{{ next.title }}</RouterLink><p v-if="next.detail">{{ next.detail }}</p><RouterLink class="source" :to="next.source">Source</RouterLink></section>
      <div v-if="range" class="briefing-grid">
        <section class="glass-card briefing-section" aria-labelledby="briefing-needs">
          <header><h2 id="briefing-needs">Needs you now</h2><span class="count-badge">{{ needs.length }}</span></header>
          <p v-if="!needs.length" class="muted">No person action in the sources that answered.</p>
          <ul v-else><li v-for="need in (showAll ? needs : needs.slice(0, 8))" :key="need.id"><RouterLink class="fact-title" :to="need.href">{{ need.title }}</RouterLink><p v-if="need.detail">{{ need.detail }}</p><RouterLink class="source" :to="need.source">Source</RouterLink></li></ul>
        </section>
        <section v-for="section in sections" :key="section.key" class="glass-card briefing-section" :aria-labelledby="`briefing-${section.key}`">
          <header><h2 :id="`briefing-${section.key}`">{{ section.title }}</h2><span class="count-badge">{{ section.rows.length }}</span></header>
          <p v-if="!section.rows.length" class="muted">{{ section.empty }}</p>
          <ul v-else><li v-for="fact in (showAll ? section.rows : section.rows.slice(0, 8))" :key="fact.id"><RouterLink class="fact-title" :to="fact.href">{{ fact.title }}</RouterLink><p v-if="fact.detail">{{ fact.detail }}</p><div class="fact-meta"><time :datetime="fact.at">{{ absoluteTime(fact.at) }}</time><a v-if="fact.source.startsWith('/api/')" class="source" :href="fact.source" target="_blank" rel="noopener">{{ fact.source.startsWith('/api/outcomes?') ? 'Source outcome' : 'Source event' }}</a><RouterLink v-else class="source" :to="fact.source">Source outcome</RouterLink></div></li></ul>
        </section>
        <section class="glass-card briefing-section cost-section" aria-labelledby="briefing-cost">
          <header><h2 id="briefing-cost">What it cost</h2><a class="source" :href="usageSource" target="_blank" rel="noopener">Usage source</a></header>
          <template v-if="usage && cost">
            <p class="cost-value"><AppIcon v-if="cost.measured" name="check" :size="16" aria-label="Measured reports" />{{ cost.value }}</p><p>{{ cost.detail }}</p>
            <p class="muted">Lifetime usage of sessions started in this window. API list value is approximate; it is not an invoice or spend during the window.</p>
            <template v-if="usage.allowance.state === 'visible' && usage.allowance.windows.length"><h3>Accounts now</h3><ul><li v-for="account in usage.allowance.windows" :key="account.window_id"><RouterLink :to="'/agents'">{{ account.label }}</RouterLink><p>{{ accountUsage(account) }} · {{ account.provisional ? 'provisional' : 'reported' }}</p><time :datetime="account.ends_at">Window ends {{ absoluteTime(account.ends_at) }}</time></li></ul></template>
            <p v-else class="muted">{{ usage.allowance.state === 'withheld' ? 'Account budgets require account access.' : 'No account budget windows recorded.' }}</p>
          </template>
          <p v-else class="muted">Usage is unavailable with your current access or did not answer.</p>
          <div v-if="visibleTickets.length" class="planning-table"><h3>Recorded ticket totals</h3><p class="muted">Measured and estimated figures use the planning columns. Totals cover each ticket’s sessions, not only this window.</p><table><thead><tr><th>Ticket</th><th>≈ Cost</th><th>Paid</th></tr></thead><tbody><tr v-for="ticket in visibleTickets" :key="ticket.id"><td><RouterLink :to="`/p/${encodeURIComponent(projects.byId(ticket.project?.id ?? '')?.routeKey ?? '')}/${encodeURIComponent(ticket.key)}`">{{ ticket.key }}</RouterLink></td><td><PlanningCell column="list_cost" :row="ticket" /></td><td><PlanningCell column="paid" :row="ticket" /></td></tr></tbody></table></div>
        </section>
      </div>
      <button v-if="!showAll && [needs.length, delivered.length, failures.length].some(n => n > 8)" type="button" class="btn" @click="showAll = true">Show all briefing items</button>
    </template>
  </main>
</template>

<style scoped>
.briefing-page { max-width: 1120px; margin: 0 auto; padding: 28px 24px 48px; }
.page-head { margin-bottom: 22px; }
h1 { margin: 5px 0 8px; } h2 { margin: 0; font-size: 17px; } h3 { margin: 22px 0 8px; font-size: 14px; }
.briefing-tools { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 16px; font-size: 12px; }
.briefing-tools input { width: 110px; } .briefing-tools button { margin-left: auto; }
.briefing-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 18px; margin-bottom: 20px; }
.briefing-section, .next-step, .note { padding: 20px; min-width: 0; }
.briefing-section header { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 16px; }
.next-step { margin-bottom: 18px; } .next-step > a:first-of-type { display: inline-block; margin-top: 8px; font-size: 17px; font-weight: 600; }
p { font-size: 13px; line-height: 1.6; overflow-wrap: anywhere; } .muted, time { color: var(--ink-3); } .summary { font-size: 12px; color: var(--ink-2); }
ul { margin: 0; padding: 0; list-style: none; } li { padding: 14px 0; box-shadow: inset 0 -1px 0 var(--line); } li:first-child { padding-top: 0; } li:last-child { box-shadow: none; padding-bottom: 0; }
li p { margin: 6px 0; white-space: pre-wrap; } .fact-title { font-weight: 600; font-size: 13px; } .source { font-size: 12px; color: var(--teal-ink); }
.fact-meta { display: flex; flex-wrap: wrap; gap: 8px 14px; align-items: center; font-size: 11px; }
.cost-value { display: flex; align-items: center; gap: 6px; font-size: 26px; margin: 0; font-variant-numeric: tabular-nums; }
.planning-table { overflow-x: auto; } table { width: 100%; border-collapse: collapse; font-size: 12px; } th, td { padding: 10px 6px; text-align: right; box-shadow: inset 0 -1px 0 var(--line); } th:first-child, td:first-child { text-align: left; }
a:focus-visible, button:focus-visible, input:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 4px; }
@media (max-width: 700px) { .briefing-page { padding: 20px 14px 36px; } .briefing-grid { grid-template-columns: minmax(0, 1fr); gap: 14px; } .briefing-section, .next-step, .note { padding: 16px; } }
</style>
