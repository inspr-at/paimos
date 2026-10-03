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
const heading = ref<HTMLElement>()
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
    const next = order.map(row => row.querySelector<HTMLElement>('button:not(:disabled)')).find(Boolean) ?? heading.value
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
</script>

<template>
  <section v-if="pending.length" class="run-queue glass-card" aria-label="Runs awaiting a session">
    <header>
      <h2 ref="heading" tabindex="-1">Queued</h2><span class="count mono">{{ pending.length }}</span>
    </header>
    <ul ref="list">
      <li v-for="run in pending" :key="run.id">
        <AppIcon :name="run.status === 'queued' || run.wait ? 'clock' : 'check'" :size="14" class="run-icon" />
        <strong class="run-title" :title="titles[run.work_order_id] || `Run ${run.id}`">{{ titles[run.work_order_id] || 'Run' }}</strong>
        <time class="run-when" :datetime="run.created_at">{{ relativeTime(run.created_at, { now: agents.now }) }}</time>
        <span v-if="run.requested_model" class="run-model mono">{{ run.requested_model }}</span>
        <span class="run-state">{{ run.wait ? capacityWaitText(run.wait, 'Agents', agents.now) : launchState(run).label }}</span>
        <button v-if="mayRunNow && run.status === 'queued'" type="button" class="btn sm ghost move" :disabled="!!busy" @click="openMove(run)">Move to…</button>
        <div v-if="moveFor === run.id" class="move-list">
          <button v-for="dest in destinations(run)" :key="dest.key" type="button" class="btn sm ghost" :disabled="!!busy" @click="move(run, dest.body)">{{ dest.label }}</button>
          <p v-if="!destinations(run).length" class="move-empty">No account to move this run to.</p>
        </div>
        <button v-if="mayRunNow && run.status === 'queued' && run.wait?.run_now_allowed" type="button" class="btn sm" :disabled="!!busy" @click="runNow(run)">Run now once</button>
        <button v-if="mayRunNow && run.status === 'queued'" type="button" class="btn sm ghost cancel" :disabled="!!busy" @click="cancel(run)">{{ cancelling === run.id ? 'Cancelling…' : 'Cancel' }}<span class="sr-only"> {{ titles[run.work_order_id] || 'run' }}</span></button>
      </li>
    </ul>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
  </section>
</template>

<style scoped>
.run-queue { overflow: clip; }
header { display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px 10px; padding: 14px 18px 10px; }
h2 { font-size: 15px; font-weight: 650; color: var(--ink); }
.count { font-size: 12px; color: var(--ink-3); }
ul { list-style: none; padding: 0 8px 8px; margin: 0; }
li { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 10px; min-height: 40px; padding: 8px 10px; border-top: 1px solid var(--line); font-size: 13px; }
.run-icon { flex: none; color: var(--ink-3); }
.run-title { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.run-model { flex: none; max-width: 30%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--ink-2); }
.run-when { flex: none; margin-right: auto; font-size: 12px; color: var(--ink-3); white-space: nowrap; }
.run-state { flex: 0 1 auto; font-size: 12px; color: var(--ink-2); white-space: normal; }
.error { padding: 10px 18px; color: var(--danger); }
.move, .cancel { color: var(--ink-2); }
.move-list { display: flex; flex-wrap: wrap; gap: 6px; flex-basis: 100%; max-width: 100%; }
.move-empty { margin: 0; color: var(--ink-3); font-size: 12px; }
@media (max-width: 600px) { li { flex-wrap: wrap; padding: 10px; } .run-title { flex: 1; } .run-state, .move-list { flex-basis: 100%; } .btn { min-height: 44px; } }
</style>
