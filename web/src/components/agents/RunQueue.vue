<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { getNode } from '../../lib/api'
import { can } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { listGroups, setRunTarget, type AccountGroup, type AgentRun } from '../../lib/agents'
import { runNowOnce } from '../../lib/agentRows'
import { useSession } from '../../stores/session'
import { capacityWaitText } from '../../lib/capacityWait'
import { activeRun, launchState } from '../../lib/startAgent'
import { useAgents } from '../../stores/agents'
import AppIcon from '../AppIcon.vue'
import { relativeTime } from '../../lib/work'
import FoldSection from './FoldSection.vue'
import { useSectionPrefs } from '../../stores/sectionPrefs'

const agents = useAgents()
const session = useSession()
const busy = ref('')
const error = ref('')
const moveFor = ref('')
const groups = ref<AccountGroup[]>([])
let groupsLoaded = false
const mayRunNow = computed(() => session.identity?.principal.kind === 'person' && can('run.create'))
function harnessOf(run: AgentRun) {
  return agents.models.find(model => model.id === run.model_profile_id)?.harness ?? ''
}
function destinations(run: AgentRun) {
  const harness = harnessOf(run)
  if (!harness) return []
  const accounts = agents.accounts.filter(account => account.harness === harness).map(account => ({
    key: `account:${account.id}`,
    label: account.host_label ? `${account.label} · ${account.host_label}` : account.label,
    body: { account_id: account.id },
  }))
  const grouped = groups.value.filter(group => group.harness === harness).map(group => ({
    key: `group:${group.id}`,
    label: group.name,
    body: { group_id: group.id },
  }))
  return [...accounts, ...grouped]
}
async function openMove(run: AgentRun) {
  if (moveFor.value === run.id) { moveFor.value = ''; return }
  moveFor.value = run.id
  if (groupsLoaded) return
  try { groups.value = await listGroups() } catch { groups.value = [] }
  groupsLoaded = true
}
async function move(run: AgentRun, body: { account_id?: string; group_id?: string }) {
  if (busy.value) return
  moveFor.value = ''
  busy.value = run.id
  error.value = ''
  try {
    await setRunTarget(run.id, body)
    void agents.afterWrite()
    await agents.refreshAgentRuns(run.agent_principal_id)
  } catch (e) { error.value = e instanceof Error ? e.message : 'The run could not be moved.' }
  finally { busy.value = '' }
}
async function runNow(run: AgentRun) {
  if (busy.value) return
  busy.value = run.id; error.value = ''
  try { const started = await runNowOnce(run.id); void agents.afterWrite(() => { agents.admitRun(started) }) }
  catch (e) { error.value = e instanceof Error ? e.message : 'The run could not be updated.' }
  finally { busy.value = '' }
}
// A queued run that never started can be cancelled; its holds go back (AEON-402).
const list = ref<HTMLElement>()
const root = ref<InstanceType<typeof FoldSection>>()
// With no run left to focus, focus goes to the section's own fold control.
const heading = () => (root.value?.$el as HTMLElement | undefined)?.querySelector<HTMLElement>('.fs-title, .fs-tog') ?? null
const cancelling = ref('')
// Cancelling the last row closes this section; the page then takes focus.
const emit = defineEmits<{ emptied: [] }>()
async function cancel(run: AgentRun) {
  if (busy.value) return
  const title = titles.value[run.work_order_id] || 'this run'
  const ok = await confirmAction({ title: `Cancel ${title}?`, body: 'It has not started. Its reserved capacity goes back to the account.', confirmLabel: 'Cancel run', cancelLabel: 'Keep it', danger: true })
  if (!ok) return
  const index = pending.value.findIndex(item => item.id === run.id)
  busy.value = run.id; cancelling.value = run.id; error.value = ''; moveFor.value = ''
  try {
    await agents.cancelQueuedRun(run)
    toast(`Cancelled: ${titles.value[run.work_order_id] || 'the run'}.`)
    // A disabled button takes no focus: settle busy before moving focus.
    busy.value = ''; cancelling.value = ''
    await nextTick()
    // Focus stays in the list: the next run's first action, else the one before,
    // else the section heading; the page takes it when the section closes.
    const rows = [...(list.value?.querySelectorAll<HTMLElement>('li') ?? [])]
    const order = [...rows.slice(index), ...rows.slice(0, index).reverse()]
    const next = order.map(row => row.querySelector<HTMLElement>('button:not(:disabled)')).find(Boolean) ?? heading()
    if (next) next.focus()
    else emit('emptied')
  } catch (e) { error.value = e instanceof Error ? e.message : 'The run could not be cancelled.' }
  finally { busy.value = ''; cancelling.value = '' }
}
const titles = ref<Record<string, string>>({})
const pending = computed(() => Object.values(agents.runs).filter(run => (run.status === 'failed' && run.wait?.code === 'vendor') || (activeRun(run) && !agents.sessions.some(s => s.run_id === run.id && s.management_mode === 'managed'))))
watch(() => pending.value.map(r => r.work_order_id), async ids => {
  for (const id of new Set(ids)) {
    if (titles.value[id]) continue
    try { titles.value[id] = (await getNode(id)).title } catch { /* Keep the durable run ID when the order is not readable. */ }
  }
}, { immediate: true })
const stateText = (run: AgentRun) => run.wait ? capacityWaitText(run.wait, 'Agents', agents.now) : launchState(run).label

// Queued runs fold like the other sections (AEON-784): open by default, the
// fold per person. Folded, the head says how many wait and why. A ?run= link
// opens the section for this visit without changing the person's preference.
const sections = useSectionPrefs()
const revealed = ref(false)
const open = computed(() => sections.open.queued || revealed.value)
function toggleFold() {
  if (revealed.value && !sections.open.queued) revealed.value = false
  else sections.toggle('queued')
}
const summary = computed(() => {
  const states = new Set(pending.value.map(stateText))
  return `${pending.value.length} waiting${states.size === 1 ? ` · ${[...states][0]}` : ''}`
})
defineExpose({ reveal: () => { revealed.value = true } })
</script>

<template>
  <FoldSection v-if="pending.length" ref="root" class="run-queue" label="Runs awaiting a session" :open="open" :tip="open ? 'Fold queued runs' : 'Unfold queued runs'" @toggle="toggleFold">
    <template #title>Queued<span class="count mono">{{ pending.length }}</span></template>
    <template #head><span v-if="!open" class="fs-sum">{{ summary }}</span></template>
    <ul ref="list">
      <li v-for="run in pending" :id="`run-${run.id}`" :key="run.id" tabindex="-1">
        <AppIcon :name="run.status === 'queued' || run.wait ? 'clock' : 'check'" :size="14" class="run-icon" />
        <span class="run-main">
          <strong class="run-title" :title="titles[run.work_order_id] || `Run ${run.id}`">{{ titles[run.work_order_id] || 'Run' }}</strong>
          <span class="run-meta">
            <time class="run-when" :datetime="run.created_at">{{ relativeTime(run.created_at, { now: agents.now }) }}</time>
            <span v-if="run.requested_model" class="run-model mono">{{ run.requested_model }}</span>
            <span class="run-state">{{ stateText(run) }}</span>
          </span>
        </span>
        <span class="run-acts">
          <button v-if="mayRunNow && run.status === 'queued'" type="button" class="btn sm ghost move" :disabled="!!busy" :aria-expanded="moveFor === run.id" @click="openMove(run)">Move to…</button>
          <button v-if="mayRunNow && run.status === 'queued' && run.wait?.run_now_allowed" type="button" class="btn sm" :disabled="!!busy" @click="runNow(run)">Run now once</button>
          <button v-if="mayRunNow && run.status === 'queued'" type="button" class="btn sm ghost cancel" :disabled="!!busy" @click="cancel(run)">{{ cancelling === run.id ? 'Cancelling…' : 'Cancel' }}<span class="sr-only"> {{ titles[run.work_order_id] || 'run' }}</span></button>
        </span>
        <div v-if="moveFor === run.id" class="move-list">
          <button v-for="dest in destinations(run)" :key="dest.key" type="button" class="btn sm ghost" :disabled="!!busy" @click="move(run, dest.body)">{{ dest.label }}</button>
          <p v-if="!destinations(run).length" class="move-empty">No account to move this run to.</p>
        </div>
      </li>
    </ul>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
  </FoldSection>
</template>

<style scoped>
.run-queue { overflow: clip; }
.count { font-size: 12px; font-weight: 500; color: var(--ink-3); }
.fs-sum { min-width: 0; font-size: 13px; color: var(--ink-2); }
ul { list-style: none; padding: 0 8px 8px; margin: 0; }
/* Title and its facts on the left, actions at the line end; a destination list opens below. */
li { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 12px; min-height: 52px; padding: 8px 10px; border-top: 1px solid var(--line); font-size: 13px; }
li:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 8px; }
.run-icon { flex: none; color: var(--ink-3); }
.run-main { display: grid; flex: 1 1 260px; min-width: 0; gap: 2px; }
.run-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.run-meta { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 0; min-width: 0; font-size: 12px; color: var(--ink-3); }
.run-meta > * + *::before { content: '·'; margin: 0 .45em; color: var(--ink-3); }
.run-when { white-space: nowrap; }
.run-model { min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); }
.run-state { color: var(--ink-2); white-space: normal; }
.run-acts { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 4px; margin-left: auto; }
.error { padding: 10px 18px; color: var(--danger); }
.move, .cancel { color: var(--ink-2); }
.move-list { display: flex; flex-wrap: wrap; gap: 6px; flex-basis: 100%; max-width: 100%; padding-left: 26px; }
.move-empty { margin: 0; color: var(--ink-3); font-size: 12px; }
@media (max-width: 600px) { li { padding: 10px; } .run-acts { flex-basis: 100%; justify-content: flex-start; margin-left: 26px; } .btn { min-height: 44px; } .move-list { padding-left: 0; } }
</style>
