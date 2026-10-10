<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { getNode, listNodes, updateNode, type ListItem } from '../../lib/api'
import { can } from '../../lib/authz'
import { rowStore } from '../../lib/rowStore'
import { applyQueueEstimate, queueReadiness, readyGaps, type QueueReadiness, type ReadyGap, type QueueWireEntry } from '../../lib/workQueue'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'
const props = defineProps<{ row: ListItem; projectId: string; anchor: HTMLElement }>()
const emit = defineEmits<{ close: [restore: boolean]; queued: [entry: QueueWireEntry] }>()
const queue = useWorkQueue(), session = useSession()
const current = ref(props.row)
const readiness = ref<QueueReadiness | null>(null), loading = ref(true)
const gaps = computed(() => readiness.value?.missing.filter((gap): gap is ReadyGap => gap !== 'status') ?? readyGaps(current.value))
const busy = ref(false), error = ref(''), naming = ref(false), blocker = ref(''), drafting = ref(false), criteria = ref('')
const canEdit = computed(() => can('nodes.write', props.projectId))
const candidates = ref<ListItem[]>([])
async function nameBlocker() {
  naming.value = true
  try { candidates.value = (await listNodes({ within: props.projectId, kind: ['ticket', 'task'], limit: 4, sort: '-updated_at' })).items.filter(item => item.id !== props.row.id).slice(0, 3) }
  catch { candidates.value = [] }
}
async function unblock() {
  if (busy.value || !canEdit.value) return
  busy.value = true; error.value = ''
  try { await updateNode(props.row.id, { state: 'open' }, { ifUnmodifiedSince: current.value.updated_at }); await refresh(); toast(`${props.row.key} is Open`) }
  catch (e) { error.value = e instanceof Error ? e.message : 'The status could not be saved.' }
  finally { busy.value = false }
}
async function refresh() {
  const sent = rowStore.mark()
  readiness.value = await queueReadiness(props.row.id)
  const node = await getNode(props.row.id)
  rowStore.adoptNode(node, sent, { show: true }); current.value = { ...current.value, fields: node.fields, state: node.state, updated_at: node.updated_at }
}
onMounted(async () => { try { await refresh() } catch (e) { error.value = e instanceof Error ? e.message : 'Readiness could not be checked.' } finally { loading.value = false } })
const lines: { id: ReadyGap; label: string; detail: string; action: string }[] = [
  { id: 'estimate', label: 'Estimate', detail: 'Nothing is dispatched without one', action: 'Suggest estimate' },
  { id: 'criteria', label: 'Acceptance criteria', detail: 'What must be true when it is done', action: 'Draft criteria' },
  { id: 'blocker', label: 'No unnamed blocker', detail: 'Blocked, but by what?', action: 'Name it' },
]
const statusLine = computed(() => {
  if (loading.value) return 'Checking the definition of ready…'
  if (!readiness.value) return 'Readiness could not be checked.'
  if (readiness.value.ready) return 'Queued work meets the definition of ready.'
  const missing = readiness.value.missing.map(gap => gap === 'status' ? 'a queueable ticket status' : gap === 'blocker' ? 'a named blocker' : lines.find(line => line.id === gap)!.label.toLowerCase())
  return `Still missing: ${missing.join(', ')}.`
})
async function fix(kind: ReadyGap) {
  if (busy.value) return
  if (kind === 'blocker' && !naming.value) { void nameBlocker(); return }
  if (kind === 'criteria' && !drafting.value) {
    // Reuse explicit checkboxes in the description as an editable draft.
    criteria.value = (current.value.body ?? '').split('\n').filter(line => /^\s*[-*]\s+\[[ xX]\]\s+\S/.test(line)).join('\n')
    drafting.value = true; return
  }
  if (kind === 'blocker' && !blocker.value.trim()) { error.value = 'Name the ticket or reason blocking this work.'; return }
  if (kind === 'criteria' && !criteria.value.trim()) { error.value = 'Describe what must be true when this ticket is done.'; return }
  busy.value = true; error.value = ''
  try {
    if (kind === 'estimate') {
      const advice = await queueReadiness(props.row.id)
      await applyQueueEstimate(props.row.id, advice.suggested_estimate_hours)
    } else {
      if (!canEdit.value) return
      await updateNode(props.row.id, { fields: { ...current.value.fields, ...(kind === 'criteria' ? { acceptance_criteria: criteria.value.trim() } : { blocker: blocker.value.trim() }) } }, { ifUnmodifiedSince: current.value.updated_at })
    }
    await refresh(); toast(`${props.row.key}: ${kind === 'estimate' ? 'suggested estimate applied' : kind === 'criteria' ? 'criteria saved' : 'blocker named'}`)
  }
  catch (e) { error.value = e instanceof Error ? e.message : 'The fix could not be saved.' }
  finally { busy.value = false }
}
async function add() {
  if (!readiness.value?.ready || loading.value || busy.value) return
  busy.value = true; error.value = ''
  const id = props.row.id, project = props.projectId, actor = session.identity?.principal.id
  try {
    const entry = await queue.add(project, id)
    if (!entry || props.row.id !== id || props.projectId !== project || session.identity?.principal.id !== actor) return
    emit('queued', entry); emit('close', true)
  }
  catch (e) { error.value = e instanceof Error ? e.message : 'The ticket could not be queued.' }
  finally { busy.value = false }
}
</script>
<template>
  <FloatingPanel :anchor="anchor" :width="380" :label="`${row.key}: what is missing to queue it`" cycle @close="restore => emit('close', restore)">
    <div class="nr">
      <div class="nr-head"><p class="eyebrow">{{ readiness?.ready ? 'Ready to queue' : 'Not ready to queue' }}</p><span class="mono">{{ row.key }}</span></div>
      <p class="nr-lede" role="status">{{ statusLine }}</p>
      <ul class="nr-list"><li v-for="line in lines" :key="line.id" class="nr-item" :class="{ ok: !gaps.includes(line.id) }">
        <span class="nr-mark"><AppIcon :name="gaps.includes(line.id) ? 'alert' : 'check'" :size="13" /></span>
        <span class="nr-label">{{ line.label }}<small v-if="gaps.includes(line.id)">{{ line.detail }}</small></span>
        <button v-if="gaps.includes(line.id)" type="button" class="btn sm" :disabled="busy || loading || (line.id !== 'estimate' && !canEdit)" @click="fix(line.id)"><AppIcon v-if="line.id !== 'blocker'" name="sparkle" :size="13" />{{ naming && line.id === 'blocker' ? 'Save blocker' : drafting && line.id === 'criteria' ? 'Save criteria' : line.id === 'estimate' && readiness ? `Apply ~${readiness.suggested_estimate_hours} h` : line.action }}</button>
        <input v-if="line.id === 'blocker' && naming" v-model="blocker" class="field" aria-label="Name the blocker" placeholder="Ticket key or blocking reason" :disabled="busy" @keydown.enter.prevent="fix('blocker')" />
        <span v-if="line.id === 'blocker' && naming" class="nr-pick"><button v-for="candidate in candidates" :key="candidate.id" type="button" class="btn sm" :disabled="busy" :data-tip="candidate.title" @click="blocker = candidate.key; fix('blocker')">{{ candidate.key }}</button><button type="button" class="btn sm ghost" :disabled="busy || !canEdit" @click="unblock">Not blocked</button></span>
        <textarea v-if="line.id === 'criteria' && drafting" v-model="criteria" class="field" aria-label="Draft acceptance criteria" placeholder="- [ ] What must be true when this is done" rows="3" :disabled="busy" />
      </li></ul>
      <p v-if="error" role="alert" class="error">{{ error }}</p>
      <div class="nr-foot"><span>{{ gaps.length ? 'Fix the lines above; Queue then works.' : 'All set: it goes in by priority.' }}</span><button type="button" class="btn sm primary" :disabled="busy || loading || !readiness?.ready" @click="add"><AppIcon name="queue-add" :size="13" />Queue</button></div>
    </div>
  </FloatingPanel>
</template>
<style scoped>
.nr { display: grid; gap: 8px; padding: 4px; }
.nr-head, .nr-foot { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 2px 6px; }
.nr-head .mono, .nr-foot { font-size: 11.5px; color: var(--ink-3); }
.nr-lede { padding: 0 6px; font-size: 12.5px; color: var(--ink-2); }
.nr-list { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.nr-item { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 9px; min-height: 38px; padding: 4px 6px; border-radius: 8px; font-size: 13px; }
.nr-item:not(.ok) { background: var(--surface-sunken); }
.nr-mark { display: grid; place-items: center; color: var(--queue-wait-ink); }
.ok .nr-mark { color: var(--ok); }
.nr-label { flex: 1 1 120px; }
.nr-label small { display: block; font-size: 11.5px; color: var(--ink-3); }
.nr-item > .field { flex: 1 1 100%; min-width: 0; }
.nr-pick { display: flex; flex-wrap: wrap; gap: 4px; flex: 1 1 100%; }
.nr-foot { border-top: 1px solid var(--line); padding-top: 8px; }
.error { color: var(--danger); font-size: 12px; }
</style>
