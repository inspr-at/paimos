<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useAssistants } from '../../stores/assistants'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import { assistantCapabilities, assistantDeskLink, assistantMoney, assistantStatus, ASSISTANT_STATUS_ICON, cloneAssistantPolicy, policyChange, policyGroups, type Assistant, type AssistantFacts, type AssistantPolicy } from '../../lib/assistants'
import { assistantCopy } from '../../lib/assistantsCopy'
import { can } from '../../lib/authz'
import { APIError } from '../../lib/api'
import { confirmAction } from '../../lib/confirm'
import { relativeTime } from '../../lib/work'
import { useVisualViewport } from '../../lib/visualViewport'
import { shortcutTyping } from '../../lib/shortcuts'
import type { PanelLayout } from '../../lib/leadPanel'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import HarnessMark from './HarnessMark.vue'
import AssistantBudget from './AssistantBudget.vue'
const props = defineProps<{ item: Assistant; facts: AssistantFacts; now: number; docked: boolean; sheet: boolean; layout: PanelLayout; sidebarAvailable: boolean }>()
const emit = defineEmits<{ close: []; layout: [layout: PanelLayout] }>()
const store = useAssistants(), session = useSession(), projects = useProjects(), words = assistantCopy()
const tabs = ['overview', 'approvals', 'routines', 'policy', 'activity'] as const
type Tab = typeof tabs[number]
const tab = ref<Tab>('overview'), activityFilter = ref<keyof typeof words.filters>('all'), refusalsOnly = ref(false)
const root = ref<HTMLElement>(), menuAnchor = ref<HTMLElement | null>(null)
const editing = ref(false), original = ref<Assistant>(), draft = ref<AssistantPolicy>(), feedback = ref(''), failed = ref(false)
const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const who = computed(() => session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : '')
const mayEdit = computed(() => store.mayEdit(props.item)), pending = computed(() => !!store.busy[props.item.principal_id])
const status = computed(() => assistantStatus(props.item, props.facts, props.now))
const statusWord = computed(() => status.value === 'waiting' && !can('approvals.decide') ? words.waitingOther : words.statuses[status.value])
const vendor = computed(() => props.item.vendor === 'grok' ? 'Grok' : words.vendorUnknown)
const openApprovals = computed(() => props.facts.approvals?.filter(item => !item.decision) ?? null)
const statusLine = computed(() => {
  if (props.item.status === 'deactivated') return words.disconnected
  if (status.value === 'waiting') return words.approvalLine(openApprovals.value!.length)
  if (status.value === 'working' && props.facts.lastActivity) return words.since(time(props.facts.lastActivity.at), props.facts.lastActivity.text)
  if (status.value === 'idle') return props.facts.lastActivity ? words.lastActive(relativeTime(props.facts.lastActivity.at, { now: props.now })) : words.notReported
  if (status.value === 'paused') return props.facts.paused ? words.pausedBy(props.facts.paused.by, time(props.facts.paused.at)) : props.facts.spend?.resumes ? words.resumes(props.facts.spend.resumes) : words.budgetReached
  if (status.value === 'setup') return words.connectVendor(vendor.value)
  if (status.value === 'down') return [props.facts.adapter?.lastReceipt ? words.noReceipt(vendor.value, relativeTime(props.facts.adapter.lastReceipt, { now: props.now }).replace(/ ago$/, '')) : words.notReported, props.facts.adapter?.error].filter(Boolean).join(' · ')
  return words.statusUnavailable
})
const history = computed(() => store.events.filter(event => (event.after?.principal_id ?? event.before?.principal_id) === props.item.principal_id).sort((a, b) => Date.parse(b.at) - Date.parse(a.at) || b.id - a.id))
const activities = computed(() => [
  ...history.value.map(event => ({ key: `policy:${event.id}`, at: event.at, kind: 'policy', text: words.changed(event.actor_principal_id === session.identity?.principal.id ? session.identity.principal.name : event.actor_principal_id.slice(0, 8), time(event.at), policyChange(event)), href: undefined as string | undefined, refused: false })),
  ...(props.facts.activity ?? []).map((event, index) => ({ ...event, key: `activity:${index}` })),
].filter(event => (activityFilter.value === 'all' || event.kind === activityFilter.value) && (!refusalsOnly.value || event.refused)).sort((a, b) => Date.parse(b.at) - Date.parse(a.at)))
const displayPolicy = computed(() => editing.value && draft.value ? draft.value : props.item.policy)
const dirty = computed(() => editing.value && JSON.stringify(draft.value) !== JSON.stringify(original.value?.policy))
const budgetValid = computed(() => !!draft.value && /^(0|[1-9][0-9]{0,11})(\.[0-9]{1,6})?$/.test(draft.value.weekly_budget.amount))
const scopeOptions = computed(() => [
  { label: words.readTickets, scopes: ['nodes.read'] }, { label: words.writeTicketsOnly, scopes: ['nodes.write'] },
  { label: words.readKnowledge, scopes: ['knowledge.read'] }, { label: words.writeKnowledge, scopes: ['knowledge.write'] },
  { label: words.readInbox, scopes: ['inbox.read', 'inbox.receipt'] }, { label: words.messagePeople, scopes: ['inbox.send'] },
])
const otherScopes = computed(() => displayPolicy.value.allowed_scopes.filter(scope => !scopeOptions.value.some(option => option.scopes.includes(scope))))
function time(at: string) { return new Date(at).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) }
function projectName(id: string) { const project = projects.byId(id); return project?.routeKey ?? id }
function toggleProject(id: string, checked: boolean) {
  if (!draft.value) return
  draft.value.allowed_projects = checked ? [...new Set([...draft.value.allowed_projects, id])] : draft.value.allowed_projects.filter(value => value !== id)
}
function toggleScope(scopes: string[], checked: boolean) {
  if (!draft.value) return
  draft.value.allowed_scopes = checked ? [...new Set([...draft.value.allowed_scopes, ...scopes])] : draft.value.allowed_scopes.filter(value => !scopes.includes(value))
}
function beginEdit() {
  menuAnchor.value = null
  if (!mayEdit.value || pending.value) return
  original.value = { ...props.item, policy: cloneAssistantPolicy(props.item.policy) }
  draft.value = cloneAssistantPolicy(props.item.policy); editing.value = true; tab.value = 'policy'; feedback.value = ''; failed.value = false
}
function cancelEdit() { if (pending.value) return; editing.value = false; draft.value = undefined; original.value = undefined; feedback.value = ''; failed.value = false }
async function save() {
  const record = original.value, policy = draft.value, person = who.value
  if (!record || !policy || !mayEdit.value || pending.value || !budgetValid.value || !dirty.value) return
  const submitted = cloneAssistantPolicy(policy)
  feedback.value = ''; failed.value = false
  try {
    const saved = await store.save(record, submitted)
    if (!saved || who.value !== person || props.item.principal_id !== record.principal_id) return
    editing.value = false; original.value = undefined; draft.value = undefined; feedback.value = words.saved
  } catch (error) {
    if (who.value !== person || props.item.principal_id !== record.principal_id) return
    failed.value = true; feedback.value = error instanceof APIError && error.status === 409 ? words.conflict : error instanceof Error ? error.message : words.loadFailed
  }
}
async function disconnect() {
  menuAnchor.value = null
  const record = props.item, person = who.value, revision = record.revision
  if (!mayEdit.value || pending.value) return
  if (!await confirmAction({ title: words.disconnectTitle(record.display_name), body: words.disconnectBody, confirmLabel: words.confirmDisconnect, cancelLabel: words.cancel, danger: true })) return
  if (who.value !== person || props.item.principal_id !== record.principal_id || props.item.revision !== revision || !mayEdit.value) return
  try { if (await store.disconnect(record) && person === who.value && props.item.principal_id === record.principal_id) { cancelEdit(); feedback.value = words.disconnected } }
  catch (error) { if (person === who.value && props.item.principal_id === record.principal_id) { failed.value = true; feedback.value = error instanceof Error ? error.message : words.loadFailed } }
}
function select(next: Tab) { tab.value = next; menuAnchor.value = null }
function tabKey(event: KeyboardEvent) {
  if (event.metaKey || event.ctrlKey || event.altKey || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault(); event.stopPropagation()
  const next = event.key === 'Home' ? tabs[0] : event.key === 'End' ? tabs.at(-1)! : tabs[(tabs.indexOf(tab.value) + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length]!
  select(next); root.value?.querySelector<HTMLButtonElement>(`#assistant-tab-${next}`)?.focus()
}
function panelKey(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopPropagation()
    if (shortcutTyping(event.target)) { (event.target as HTMLElement).blur(); root.value?.focus({ preventScroll: true }) }
    else if (menuAnchor.value) menuAnchor.value = null
    else emit('close')
  }
  if (editing.value && shortcutTyping(event.target) && event.key === 'Enter' && !event.altKey && !event.shiftKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)) { event.preventDefault(); event.stopPropagation(); void save() }
  // Native/OS shortcuts and unmodified typing are never captured.
}
useVisualViewport(root)
watch([() => props.item.principal_id, who], () => { editing.value = false; draft.value = undefined; original.value = undefined; tab.value = 'overview'; activityFilter.value = 'all'; refusalsOnly.value = false; feedback.value = ''; failed.value = false; menuAnchor.value = null; void store.loadHistory() }, { immediate: true })
watch(mayEdit, allowed => { if (!allowed) { editing.value = false; draft.value = undefined; original.value = undefined; menuAnchor.value = null } })
onMounted(() => { root.value?.focus({ preventScroll: true }); void projects.load() })
onBeforeUnmount(() => { draft.value = undefined; original.value = undefined })
defineExpose({ focus: () => root.value?.focus({ preventScroll: true }) })
</script>
<template>
  <aside ref="root" class="session-panel assistant-panel" :class="{ docked, sheet, full: layout === 'full', wide: layout === 'wide' }" :aria-label="words.details" tabindex="-1" @keydown="panelKey">
    <header class="assistant-panel-head">
      <div class="assistant-panel-title"><HarnessMark :harness="item.vendor" :size="16" /><h2>{{ item.display_name }}<small>{{ vendor }}</small></h2><button v-if="sidebarAvailable" type="button" class="icon-btn flat assistant-layout" :aria-label="words.sessionsList" :aria-pressed="layout === 'side'" @click="emit('layout', layout === 'side' ? 'wide' : 'side')"><AppIcon :name="layout === 'side' ? 'layout-focus' : 'layout-list'" /></button><button type="button" class="icon-btn flat assistant-layout" :aria-label="words.fullScreen" :aria-pressed="layout === 'full'" @click="emit('layout', layout === 'full' ? 'side' : 'full')"><AppIcon :name="layout === 'full' ? 'collapse' : 'expand'" /></button><button type="button" class="icon-btn flat assistant-close" :aria-label="words.close" aria-keyshortcuts="Escape" @click="emit('close')"><AppIcon name="close" /></button></div>
      <p class="panel-status" :class="{ down: status === 'down' }"><AppIcon :name="ASSISTANT_STATUS_ICON[status]" />{{ statusWord }}</p>
      <p class="panel-status-line">{{ statusLine }}</p>
      <div class="assistant-actions"><span v-if="!assistantCapabilities.pause" class="unavailable-action">{{ words.pauseUnavailable }}</span><button v-if="mayEdit" type="button" class="icon-btn flat" :aria-label="words.more" aria-haspopup="menu" :aria-expanded="!!menuAnchor" @click="menuAnchor = menuAnchor ? null : $event.currentTarget as HTMLElement"><AppIcon name="more" /></button></div>
      <FloatingPanel v-if="menuAnchor" :anchor="menuAnchor" align="end" :width="260" :label="words.more" @close="restore => { if (restore) menuAnchor?.focus(); menuAnchor = null }"><div role="menu" class="assistant-menu"><button type="button" role="menuitem" :disabled="pending" @click="beginEdit"><AppIcon name="edit" />{{ words.editPolicy }}</button><button v-if="item.status !== 'deactivated'" type="button" role="menuitem" :disabled="pending" @click="disconnect"><AppIcon name="link" />{{ words.disconnect }}</button></div></FloatingPanel>
      <div class="assistant-tabs" role="tablist" :aria-label="words.details" @keydown="tabKey"><button v-for="key in tabs" :id="`assistant-tab-${key}`" :key="key" role="tab" type="button" :aria-selected="tab === key" :tabindex="tab === key ? 0 : -1" :aria-controls="`assistant-pane-${key}`" @click="select(key)">{{ words.tabs[key] }}</button></div>
    </header>
    <div class="assistant-panel-scroll">
      <div v-show="tab === 'overview'" id="assistant-pane-overview" role="tabpanel" aria-labelledby="assistant-tab-overview">
        <section class="assistant-block"><h3>{{ words.thisWeek }}</h3><AssistantBudget :policy="item.policy" :facts="facts" daily /></section>
        <section class="assistant-block"><h3>{{ words.adapter }}</h3><dl><div><dt>{{ words.vendor }}</dt><dd>{{ vendor }}</dd></div><div><dt>{{ words.status }}</dt><dd>{{ facts.adapter ? facts.adapter.state === 'ok' ? words.adapterOk(vendor) : facts.adapter.state === 'down' ? words.statuses.down : words.statuses.setup : words.adapterUnavailable }}</dd></div><div><dt>{{ words.lastReceipt }}</dt><dd>{{ facts.adapter?.lastReceipt ? relativeTime(facts.adapter.lastReceipt, { now }) : words.notReported }}</dd></div><div v-if="facts.adapter?.error"><dt>{{ words.lastError }}</dt><dd>{{ facts.adapter.error }}</dd></div></dl></section>
        <section class="assistant-block"><h3>{{ words.next }}</h3><p>{{ facts.routines === null ? words.routinesUnavailable : facts.routines.length ? facts.routines[0]?.name : words.noRoutines }}</p><RouterLink v-if="openApprovals?.length" :to="assistantDeskLink(item.principal_id)">{{ words.toApprove(openApprovals.length) }} · {{ words.openDesk }}</RouterLink><p v-else>{{ openApprovals === null ? words.approvalsUnavailable : words.noApprovals }}</p></section>
      </div>
      <div v-show="tab === 'approvals'" id="assistant-pane-approvals" role="tabpanel" aria-labelledby="assistant-tab-approvals" class="assistant-block">
        <p v-if="facts.approvals === null">{{ words.approvalsUnavailable }}</p>
        <template v-else><p v-if="!openApprovals?.length">{{ words.noApprovals }}</p><article v-for="approval in openApprovals" :key="approval.id" class="assistant-log-entry"><strong>{{ approval.title }}</strong><p>{{ approval.target }} · {{ relativeTime(approval.at, { now }) }}</p><RouterLink :to="approval.href">{{ words.openDesk }}</RouterLink></article><h3>{{ words.recentDecisions }}</h3><article v-for="approval in facts.approvals.filter(row => row.decision)" :key="approval.id" class="assistant-log-entry"><strong>{{ approval.title }}</strong><p>{{ approval.decision }} · {{ approval.by }} · {{ relativeTime(approval.at, { now }) }}</p></article></template>
      </div>
      <div v-show="tab === 'routines'" id="assistant-pane-routines" role="tabpanel" aria-labelledby="assistant-tab-routines" class="assistant-block">
        <p v-if="facts.routines === null">{{ words.routinesUnavailable }}</p><p v-else-if="!facts.routines.length">{{ words.noRoutines }}</p>
        <article v-for="routine in facts.routines ?? []" :key="routine.id" class="assistant-log-entry"><strong>{{ routine.name }}</strong><p>{{ routine.schedule }} · {{ routine.next ?? words.notReported }}</p><RouterLink v-if="routine.result" :to="routine.result.href">{{ routine.result.label }}</RouterLink><p>{{ routine.paused ? words.statuses.paused : words.statuses.idle }}</p><RouterLink :to="`/agents/routines?routine=${encodeURIComponent(routine.id)}`">{{ words.openRoutine }}</RouterLink></article>
      </div>
      <div v-show="tab === 'policy'" id="assistant-pane-policy" role="tabpanel" aria-labelledby="assistant-tab-policy">
        <p v-if="!mayEdit" class="policy-read-only">{{ words.readOnly }} <span>{{ words.ownerOnly }}</span></p>
        <section class="assistant-block"><h3>{{ words.projects }}</h3><div v-if="editing" class="policy-choices"><label v-for="project in projects.projects" :key="project.id"><input type="checkbox" :checked="draft!.allowed_projects.includes(project.id)" :disabled="pending" @change="toggleProject(project.id, ($event.target as HTMLInputElement).checked)" />{{ project.routeKey }} · {{ project.title }}</label></div><div v-else class="project-chips"><span v-for="id in displayPolicy.allowed_projects" :key="id">{{ projectName(id) }}</span><span v-if="!displayPolicy.allowed_projects.length">{{ words.noProjects }}</span></div><p v-if="editing && projects.error" class="assistant-error" role="alert">{{ projects.error }}</p></section>
        <section class="assistant-block"><h3>{{ words.mayDo }}</h3><div v-if="editing" class="policy-choices"><label v-for="option in scopeOptions" :key="option.label"><input type="checkbox" :checked="option.scopes.every(scope => draft!.allowed_scopes.includes(scope))" :disabled="pending" @change="toggleScope(option.scopes, ($event.target as HTMLInputElement).checked)" />{{ option.label }}</label><p v-for="scope in otherScopes" :key="scope">{{ scope }}</p></div><ul v-else class="policy-scopes"><li v-for="group in policyGroups(displayPolicy.allowed_scopes)" :key="group">{{ group }}</li><li v-if="!displayPolicy.allowed_scopes.length">{{ words.noScopes }}</li></ul></section>
        <section class="assistant-block"><h3>{{ words.budget }}</h3><div v-if="editing" class="policy-budget-fields"><label>{{ words.budget }}<input v-model="draft!.weekly_budget.amount" class="field" inputmode="decimal" maxlength="19" :disabled="pending" :aria-invalid="!budgetValid" /></label><label>{{ words.currency }}<select v-model="draft!.weekly_budget.unit" class="field" :disabled="pending"><option>USD</option><option>EUR</option><option value="tokens">tokens</option><option value="credits">credits</option></select></label></div><p v-else>{{ assistantMoney(displayPolicy.weekly_budget.amount, displayPolicy.weekly_budget.unit) }} {{ words.week }}</p><p>{{ words.resetUnavailable }}</p><p v-if="editing && !budgetValid" class="assistant-error">{{ words.invalidBudget }}</p></section>
        <section class="assistant-block locked-rule" data-locked-rule="outward"><h3><AppIcon name="lock" :size="14" />{{ words.outward }}</h3><strong>{{ words.outwardRule }}</strong><p>{{ words.outwardHelp }}</p></section>
        <section class="assistant-block"><h3>{{ words.approvers }}</h3><p>{{ words.approversUnavailable }}</p></section>
        <section class="assistant-block locked-rule" data-locked-rule="secrets"><h3><AppIcon name="lock" :size="14" />{{ words.locked }}</h3><strong>{{ words.secretsRule }}</strong><button type="button" class="text-link" @click="activityFilter = 'approvals'; refusalsOnly = true; tab = 'activity'">{{ words.refusals }}</button></section>
        <section class="assistant-block policy-audit"><h3>{{ words.tabs.activity }}</h3><p v-for="event in history.slice(0, 5)" :key="event.id">{{ words.changed(event.actor_principal_id === session.identity?.principal.id ? session.identity.principal.name : event.actor_principal_id.slice(0, 8), time(event.at), policyChange(event)) }}</p><p v-if="store.historyState === 'forbidden' || store.historyState === 'error'">{{ words.historyUnavailable }}</p><p v-if="store.historyTruncated">{{ words.truncated }}</p><p class="history-range">{{ words.historyRange }}</p></section>
      </div>
      <div v-show="tab === 'activity'" id="assistant-pane-activity" role="tabpanel" aria-labelledby="assistant-tab-activity" class="assistant-activity">
        <div class="activity-filters"><label for="assistant-activity-filter">{{ words.tabs.activity }}</label><select id="assistant-activity-filter" v-model="activityFilter" class="field" @change="refusalsOnly = false"><option v-for="(label, key) in words.filters" :key="key" :value="key">{{ label }}</option></select></div>
        <p v-if="facts.activity === null">{{ words.activityUnavailable }}</p><p v-if="store.historyState === 'forbidden' || store.historyState === 'error'">{{ words.historyUnavailable }} {{ store.historyError }}</p><p v-if="store.historyTruncated">{{ words.truncated }}</p>
        <article v-for="event in activities" :key="event.key" class="assistant-log-entry"><time :datetime="event.at">{{ new Date(event.at).toLocaleString('en-GB') }}</time><p>{{ event.text }}</p><RouterLink v-if="event.href" :to="event.href">{{ event.href }}</RouterLink></article><p v-if="!activities.length && store.historyState === 'ready' && (activityFilter === 'policy' || facts.activity !== null)">{{ words.noActivity }}</p><p class="history-range">{{ words.historyRange }}</p>
      </div>
    </div>
    <footer class="assistant-save-bar">
      <p class="save-feedback" :class="{ 'assistant-error': failed }" role="status">{{ feedback || (editing ? words.draft : '') }}</p>
      <div v-if="editing && mayEdit" class="save-actions"><button type="button" class="btn" :disabled="pending" @click="cancelEdit">{{ words.cancel }}</button><button type="button" class="btn primary assistant-save" :disabled="pending || !dirty || !budgetValid" :aria-keyshortcuts="mac ? 'Meta+Enter' : 'Control+Enter'" @click="save"><span class="save-label"><span>{{ pending ? words.saving : words.save }}</span><span aria-hidden="true">{{ words.saving }}</span></span><KeyCap k="mod" /><KeyCap k="enter" /></button></div>
    </footer>
  </aside>
</template>
<style scoped>
.assistant-panel { position: fixed; z-index: 15; top: calc(var(--header-h) + 10px); right: 10px; bottom: calc(var(--footer-h) + 10px); width: min(560px, calc(100vw - 20px)); display: flex; flex-direction: column; min-width: 0; min-height: 0; outline: none; border: 1px solid var(--glass-edge); border-radius: var(--radius); background: var(--surface); box-shadow: var(--shadow); overflow: hidden; }
.assistant-panel.docked { position: sticky; inset: auto; top: 0; flex: 1 1 0; width: auto; height: var(--pane-h); }
.assistant-panel.sheet { z-index: 40; inset: calc(var(--header-h) + 8px) 16px calc(var(--footer-h) + 8px); width: auto; }
.assistant-panel.full { position: fixed; z-index: 60; inset: var(--vv-top, 0px) 0 auto; width: auto; height: var(--vv-h, 100dvh); border: 0; border-radius: 0; }
.assistant-panel-head { flex: none; padding: 12px 16px 0; border-bottom: 1px solid var(--line); }
.assistant-panel-title { display: flex; align-items: flex-start; gap: 8px; min-height: 44px; }
.assistant-panel-title > :deep(svg) { margin-top: 4px; width: 16px; height: 16px; }
h2 { margin: 0; min-width: 0; flex: 1; overflow-wrap: anywhere; font-size: 18px; font-weight: 650; }
h2 small { display: block; margin-top: 4px; font-size: 12px; font-weight: 400; color: var(--ink-2); }
.assistant-close { flex: none; }
.panel-status { display: flex; align-items: center; gap: 7px; margin: 10px 0 4px; font-size: 13px; font-weight: 600; }
.down { color: var(--danger); }
.panel-status-line { margin: 0; color: var(--ink-2); font-size: 12px; line-height: 1.5; height: 36px; overflow-y: auto; overflow-wrap: anywhere; }
.assistant-actions { display: flex; align-items: center; justify-content: space-between; height: 44px; gap: 8px; }
.unavailable-action { font-size: 12px; color: var(--ink-2); }
.assistant-tabs { display: flex; gap: 2px; height: 44px; overflow-x: auto; scrollbar-width: thin; }
.assistant-tabs button { flex: none; height: 44px; border: 0; border-radius: 5px; padding: 0 12px; background: transparent; color: var(--ink-2); font-size: 12px; font-weight: 600; white-space: nowrap; }
.assistant-tabs button[aria-selected="true"] { color: var(--teal-ink); background: var(--row-hover); }
.assistant-tabs button:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
.assistant-panel-scroll { min-height: 0; flex: 1; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; }
.assistant-block { padding: 18px 20px; border-bottom: 1px solid var(--line); font-size: 13px; line-height: 1.5; }
.assistant-block h3 { margin: 0 0 10px; font-size: 13px; font-weight: 650; }
.assistant-block p { margin: 6px 0; color: var(--ink-2); overflow-wrap: anywhere; }
.assistant-block a, .assistant-activity a, .text-link { color: var(--teal-ink); }
dl { margin: 0; } dl > div { display: grid; grid-template-columns: minmax(80px, 1fr) minmax(0, 2fr); gap: 14px; padding: 5px 0; }
dt { color: var(--ink-2); } dd { margin: 0; overflow-wrap: anywhere; }
.policy-read-only { margin: 0; padding: 16px 20px 0; color: var(--ink-2); font-size: 12px; line-height: 1.5; }
.policy-read-only span { display: block; }
.project-chips { display: flex; flex-wrap: wrap; gap: 8px; }
.project-chips > span { color: var(--ink-2); }
.policy-scopes { list-style: none; margin: 0; padding: 0; color: var(--ink-2); }
.policy-choices { display: grid; gap: 0; }
.policy-choices label { display: flex; align-items: center; gap: 10px; min-height: 44px; color: var(--ink); }
.policy-budget-fields { display: flex; flex-wrap: wrap; gap: 10px; }
.policy-budget-fields label { display: grid; gap: 6px; flex: 1 1 10em; min-width: 0; }
.policy-budget-fields input, .policy-budget-fields select { width: 100%; min-width: 0; min-height: 44px; }
.locked-rule h3 { display: flex; align-items: center; gap: 7px; }
.locked-rule strong { font-size: 13px; font-weight: 600; }
.text-link { display: block; min-height: 44px; padding: 8px 0; border: 0; background: transparent; text-align: left; font-size: 12px; cursor: pointer; }
.history-range { color: var(--ink-2); font-size: 11px; }
.assistant-activity { padding: 16px 20px; font-size: 13px; color: var(--ink-2); }
.activity-filters { display: flex; align-items: center; gap: 12px; min-height: 44px; margin-bottom: 12px; }
.activity-filters select { min-height: 44px; min-width: 0; flex: 1; }
.assistant-log-entry { padding: 14px 0; border-bottom: 1px solid var(--line); overflow-wrap: anywhere; }
.assistant-log-entry p { margin: 6px 0; }.assistant-log-entry time { color: var(--ink-2); font-size: 11px; }
.assistant-save-bar { flex: none; padding: 8px 16px max(8px, env(safe-area-inset-bottom)); border-top: 1px solid var(--line); }
.save-feedback { min-height: 18px; margin: 0; color: var(--ink-2); font-size: 12px; overflow-wrap: anywhere; }
.save-actions { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 8px; padding-top: 8px; }
.save-actions .btn { min-height: 44px; }
.save-label { display: inline-grid; }.save-label > span { grid-area: 1 / 1; }.save-label > span[aria-hidden] { visibility: hidden; }
.assistant-error { color: var(--danger) !important; }
.assistant-menu { display: grid; }.assistant-menu button { display: flex; align-items: center; gap: 8px; min-height: 44px; padding: 8px 12px; border: 0; border-radius: 5px; color: var(--ink); background: transparent; text-align: left; font-size: 13px; }.assistant-menu button:hover { background: var(--row-hover); }
@media (max-width: 720px), (max-height: 480px) {
  .assistant-panel, .assistant-panel.sheet, .assistant-panel.docked { position: fixed; z-index: 40; inset: var(--vv-top, 0px) 0 auto; width: 100%; height: var(--vv-h, 100dvh); border: 0; border-radius: 0; padding-inline: env(safe-area-inset-left) env(safe-area-inset-right); }
  .assistant-panel-head { padding-inline: 12px; }.assistant-tabs { margin-inline: -4px; }
  .assistant-block, .assistant-activity { padding-inline: 16px; }
  .assistant-layout { display: none; }
}
</style>
