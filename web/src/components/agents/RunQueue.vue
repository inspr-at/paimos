<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { getNode } from '../../lib/api'
import { can } from '../../lib/authz'
import { runNowOnce, type AgentRun } from '../../lib/agents'
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
const mayRunNow = computed(() => session.identity?.principal.kind === 'person' && can('run.create'))
async function runNow(run: AgentRun) {
  if (busy.value) return
  busy.value = run.id; error.value = ''
  try { agents.recordRun(await runNowOnce(run.id)) }
  catch (e) { error.value = e instanceof Error ? e.message : 'The run could not be updated.' }
  finally { busy.value = '' }
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
      <h2>Queued</h2><span class="count mono">{{ pending.length }}</span>
    </header>
    <ul>
      <li v-for="run in pending" :key="run.id">
        <AppIcon :name="run.status === 'queued' || run.wait ? 'clock' : 'check'" :size="14" class="run-icon" />
        <strong class="run-title" :title="titles[run.work_order_id] || `Run ${run.id}`">{{ titles[run.work_order_id] || 'Run' }}</strong>
        <time class="run-when" :datetime="run.created_at">{{ relativeTime(run.created_at, { now: agents.now }) }}</time>
        <span v-if="run.requested_model" class="run-model mono">{{ run.requested_model }}</span>
        <span class="run-state">{{ run.wait ? capacityWaitText(run.wait, 'Agents', agents.now) : launchState(run).label }}</span>
        <button v-if="mayRunNow && run.status === 'queued' && run.wait?.run_now_allowed" type="button" class="btn sm" :disabled="!!busy" @click="runNow(run)">Run now once</button>
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
li { display: flex; align-items: center; gap: 10px; min-height: 40px; padding: 0 10px; border-top: 1px solid var(--line); font-size: 13px; }
.run-icon { flex: none; color: var(--ink-3); }
.run-title { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.run-model { flex: none; max-width: 30%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--ink-2); }
.run-when { flex: none; margin-right: auto; font-size: 12px; color: var(--ink-3); white-space: nowrap; }
.run-state { flex: 0 1 auto; font-size: 12px; color: var(--ink-2); white-space: normal; }
.error { padding: 10px 18px; color: var(--danger); }
@media (max-width: 600px) { li { flex-wrap: wrap; padding: 10px; } .run-title { flex: 1; } .run-state { flex-basis: 100%; } .btn { min-height: 44px; } }
</style>
