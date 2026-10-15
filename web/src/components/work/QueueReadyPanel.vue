<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { APIError, getNode, updateNode, type ListItem } from '../../lib/api'
import { can } from '../../lib/authz'
import { conditionWords, entryFailure, entryLabel, entryStatus, tickEntryCheck, type EntryCheck, type EntryFailure, type EntryRecord } from '../../lib/entryChecks'
import { useEntryChecks } from '../../lib/useEntryChecks'
import { rowStore } from '../../lib/rowStore'
import { firstBenefitGap, firstPillGap } from '../../lib/ticketBenefits'
import { applyQueueEstimate, queueReadiness, type QueueWireEntry } from '../../lib/workQueue'
import { absoluteTime, statusMeta } from '../../lib/work'
import { isSettingsField, settingsSubmitKey } from '../../lib/settingsOverlays'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import FloatingPanel from './FloatingPanel.vue'
import TicketBenefits from './TicketBenefits.vue'
const props = withDefaults(defineProps<{ row: ListItem; projectId: string; anchor: HTMLElement; mode?: 'queue' | 'entry' | 'status' | 'record'; toStatus?: string; entryRecord?: EntryRecord; refusal?: EntryFailure }>(), { mode: 'queue', toStatus: 'in_progress' })
const emit = defineEmits<{ close: [restore: boolean]; queued: [entry: QueueWireEntry]; changed: [] }>()
const queue = useWorkQueue(), session = useSession()
const current = ref(props.row), checks = useEntryChecks(() => current.value)
const block = computed(() => props.mode === 'record' ? props.entryRecord : checks.data.value?.blocks.find(b => b.to_status === entryStatus(props.toStatus)))
const rows = computed(() => {
  const items = block.value?.checks ?? []
  // Keep the relative order in each group stable while fixes are saved.
  return [...items.filter(c => c.applicable), ...items.filter(c => !c.applicable)]
})
const failed = computed(() => block.value?.checks.filter(c => c.applicable && !c.passed) ?? [])
const canEdit = computed(() => can('nodes.write', props.projectId))
const person = computed(() => session.identity?.principal.kind === 'person')
const readonly = computed(() => props.mode === 'record')
const busy = ref(false), error = ref(''), fixing = ref<EntryCheck | null>(null), text = ref(''), reason = ref(''), anyway = ref(props.mode === 'status'), reasonField = ref<HTMLTextAreaElement>()
const releaseFields = ref<Record<string, unknown>>({ ...props.row.fields })
const releaseInvalidKey = ref('')
const originId = props.row.id, originActor = session.identity?.principal.id, originTenant = session.identity?.tenant.id
const alive = () => props.row.id === originId && session.identity?.principal.id === originActor && session.identity?.tenant.id === originTenant
let disposed = false
onBeforeUnmount(() => { disposed = true })
watch(() => [props.row.id, props.projectId, session.identity?.principal.id, session.identity?.tenant.id], () => { emit('close', false) })
watch(() => props.row.updated_at, () => { if (!busy.value && props.row.id === originId) current.value = props.row })
const source = computed(() => checks.data.value?.definition?.source === 'project' ? `${props.row.project?.key ?? 'This'} project definition` : `${props.row.project?.key ?? 'This project'} uses the workspace default`)
const sourcePath = computed(() => checks.data.value?.definition?.source === 'project' ? `/p/${encodeURIComponent(props.row.project?.key ?? props.projectId)}/settings#definitions` : '/settings?section=policies#definitions')
const lede = computed(() => readonly.value ? 'A record of the checks evaluated on entry.' : `Checks to enter ${statusMeta(props.toStatus).label}. Agents move it only when every check passes.`)
const latest = computed(() => checks.data.value?.latest_refusal)
const currentRecord = computed(() => checks.data.value?.entries.find(e => e.to_status === entryStatus(current.value.state)))
const editLabel = (check: EntryCheck) => check.id === 'estimate_set' ? 'Suggest estimate' : check.id === 'acceptance_present' ? 'Write criteria' : check.id === 'blocker_named' ? 'Name it' : check.id === 'release_copy_set' || check.id === 'pills_benefits_set' ? 'Write it' : null
function historyLabel(check: EntryCheck) {
  if (readonly.value) return check.passed ? 'Passed on entry' : `Overridden on entry · ${checks.data.value?.blocks.flatMap(b => b.checks).find(c => c.id === check.id)?.passed ? 'passes now' : 'still missing'}`
  if (check.passed && check.checked_by) return `Checked by ${check.checked_by.name}${check.checked_at ? ` · ${absoluteTime(check.checked_at)}` : ''}`
  if (!check.applicable) return conditionWords(check)
  const record = currentRecord.value
  if (!check.passed && record?.override_reason && record.checks.some(c => c.id === check.id && !c.passed)) return `Overridden · ${check.reason}`
  return check.reason
}
async function refresh() {
  const sent = rowStore.mark(), node = await getNode(originId)
  if (!alive() || disposed) return
  rowStore.adoptNode(node, sent, { show: true }); current.value = { ...current.value, ...node }
  await checks.refresh()
  if (alive() && !disposed) emit('changed')
}
async function run(action: () => Promise<void>) {
  if (busy.value || !alive() || disposed) return
  busy.value = true; error.value = ''
  try { await action() } catch (e) { if (alive() && !disposed) error.value = e instanceof Error ? e.message : 'The change could not be saved.' }
  finally { if (!disposed) busy.value = false }
}
async function fix(check: EntryCheck) {
  if (!canEdit.value || readonly.value) return
  if (check.id === 'release_copy_set' || check.id === 'pills_benefits_set') {
    releaseFields.value = { ...current.value.fields }; releaseInvalidKey.value = ''; fixing.value = check; return
  }
  if (check.id !== 'estimate_set') {
    fixing.value = check
    text.value = check.id === 'acceptance_present' ? String(current.value.fields.acceptance_criteria ?? '') : String(current.value.fields.blocker ?? '')
    return
  }
  await run(async () => { const advice = await queueReadiness(originId); if (!alive()) return; await applyQueueEstimate(originId, advice.suggested_estimate_hours); await refresh() })
}
async function saveFix() {
  if (!fixing.value || !canEdit.value) return
  if (fixing.value.id === 'release_copy_set' || fixing.value.id === 'pills_benefits_set') {
    const revision = current.value.updated_at, fields = { ...releaseFields.value }
    const gap = firstPillGap(fields) ?? (fields.hide_from_release_notes === true || fields.no_release_needed === true ? null : firstBenefitGap(fields))
    if (gap) { releaseInvalidKey.value = gap.key; return }
    releaseInvalidKey.value = ''
    await run(async () => { await updateNode(originId, { fields }, { ifUnmodifiedSince: revision }); await refresh(); if (alive()) fixing.value = null }); return
  }
  if (!text.value.trim()) return
  const key = fixing.value.id === 'acceptance_present' ? 'acceptance_criteria' : 'blocker', revision = current.value.updated_at, value = text.value.trim(), fields = { ...current.value.fields, [key]: value }
  await run(async () => { await updateNode(originId, { fields }, { ifUnmodifiedSince: revision }); await refresh(); if (alive()) fixing.value = null })
}
async function tick(check: EntryCheck) {
  if (!check.can_tick || readonly.value) return
  const revision = checks.data.value?.revision ?? current.value.updated_at
  await run(async () => { await tickEntryCheck(originId, check.id, !check.passed, revision); await refresh() })
}
function openReason() { anyway.value = true; void nextTick(() => reasonField.value?.focus()) }
async function move() {
  if (!person.value || !canEdit.value || readonly.value || !block.value || checks.loading.value || checks.error.value || (failed.value.length && !reason.value.trim())) return
  const revision = current.value.updated_at, value = reason.value.trim(), state = props.toStatus, sent = rowStore.mark()
  await run(async () => {
    try {
      const node = await updateNode(originId, { state, ...(failed.value.length ? { override_reason: value } : {}) }, { ifUnmodifiedSince: revision })
      if (!alive() || disposed) return
      rowStore.wrote(node, sent)
      toast(`${props.row.key} is now ${statusMeta(node.state).label}`); emit('changed'); emit('close', true)
    } catch (e) {
      if (entryFailure(e) || e instanceof APIError && e.status === 412) await refresh()
      throw e
    }
  })
}
async function add() {
  if (!block.value || failed.value.length || checks.loading.value || !can('run.create', props.projectId)) return
  await run(async () => { const result = await queue.add(props.projectId, originId); if (result && alive() && !disposed) { emit('queued', result); emit('close', true) } })
}
function keys(event: KeyboardEvent) {
  if (settingsSubmitKey(event)) { event.preventDefault(); event.stopPropagation(); if (fixing.value) void saveFix(); else if (anyway.value) void move() }
  else if (event.key === 'Escape' && isSettingsField(event.target)) { event.preventDefault(); event.stopPropagation(); (event.target as HTMLElement).blur(); reasonField.value?.closest<HTMLElement>('[role=dialog]')?.focus() }
}
</script>
<template>
  <FloatingPanel :anchor="anchor" :width="448" :tallest="680" :label="`${row.key}: ${entryLabel(toStatus)} checks`" cycle sheet compact-sheet field-escape settings-keys @close="restore => emit('close', restore)">
    <div class="nr" @keydown="keys">
      <header class="nr-head"><h3>{{ entryLabel(toStatus) }} <span v-if="block" class="nr-count">{{ readonly && entryRecord?.override_reason ? '· overridden' : `${block.passed} of ${block.total}` }}</span></h3><span class="mono">{{ row.key }}</span><button type="button" class="icon-btn flat" aria-label="Close checklist" @click="emit('close', true)"><AppIcon name="close" :size="14" /></button></header>
      <p class="nr-lede">{{ lede }}</p>
      <div class="nr-body">
        <p v-if="checks.loading.value && !block" role="status">Checking…</p>
        <p v-if="checks.error.value" role="alert" class="error">{{ checks.error.value }} <button type="button" class="link-btn" @click="checks.refresh()">Retry</button></p>
        <p v-if="latest" class="nr-refusal"><AppIcon name="list-checks" :size="14" /><span>{{ latest.by.name }} couldn’t move it to {{ statusMeta(latest.to_status).label }}: {{ latest.failed.map(c => `${c.label}${c.owner ? ` · waiting for ${c.owner.name}` : ''}`).join(', ') }}<small> · {{ absoluteTime(latest.at) }}<template v-if="latest.count > 1"> · {{ latest.count }} attempts</template></small></span></p>
        <p v-else-if="refusal" class="nr-refusal">Still missing: {{ refusal.failed.map(c => c.label).join(', ') }}.</p>
        <p v-if="readonly && entryRecord?.override_reason" class="nr-record">{{ entryRecord.by.name }} · {{ absoluteTime(entryRecord.at) }} · {{ entryRecord.override_reason }}</p>
        <ul class="nr-list"><li v-for="check in rows" :key="check.id" class="nr-item" :class="{ ok: check.passed, na: !check.applicable, missing: check.applicable && !check.passed }">
          <span class="nr-mark"><AppIcon :name="!check.applicable ? 'minus' : check.passed ? 'circle-check' : readonly ? 'skip' : 'circle'" :size="15" /></span>
          <span class="nr-label"><b>{{ check.label }}</b><small :data-tip="historyLabel(check)">{{ historyLabel(check) }}</small></span>
          <button v-if="!readonly && check.can_tick" type="button" class="btn sm" :class="{ primary: check.owner?.id === session.identity?.principal.id && !check.passed }" :disabled="busy" @click="tick(check)"><span class="tick-label"><span class="measure" aria-hidden="true">Mark checked</span><span>{{ check.passed ? 'Undo' : 'Mark checked' }}</span></span></button>
          <button v-else-if="!readonly && check.applicable && editLabel(check) && canEdit" type="button" class="btn sm" :disabled="busy || checks.loading.value || check.passed" @click="fix(check)">{{ editLabel(check) }}</button>
        </li></ul>
        <p v-if="block && !block.total && !checks.loading.value">No checks. Tickets enter {{ statusMeta(toStatus).label }} freely.</p>
        <div class="nr-foot">
          <RouterLink v-if="checks.data.value?.definition" :to="sourcePath" class="nr-source">{{ source }}<AppIcon name="chevron-right" :size="12" /></RouterLink>
          <div v-if="!readonly" class="nr-actions"><button v-if="person && canEdit && block?.total" type="button" class="link-btn" :aria-expanded="anyway" @click="openReason">Move to {{ statusMeta(toStatus).label }} anyway…</button><span v-else-if="!person && failed.length" class="nr-agent"><AppIcon name="lock" :size="12" />Agents move it only when every check passes.</span><button v-if="mode === 'queue'" type="button" class="btn sm primary queue-submit" :disabled="busy || checks.loading.value || !block || failed.length > 0 || !can('run.create', projectId)" @click="add"><AppIcon name="queue-add" :size="13" />Queue</button></div>
        </div>
        <form v-if="fixing && !readonly" class="nr-form" @submit.prevent="saveFix"><div class="nr-form-actions"><button type="button" class="btn sm" :disabled="busy" @click="fixing = null">Cancel</button><button type="submit" class="btn sm primary desktop-submit" :disabled="busy || (!text.trim() && fixing.id !== 'release_copy_set' && fixing.id !== 'pills_benefits_set')">Save <KeyCap k="mod" /><KeyCap k="enter" /></button></div><TicketBenefits v-if="fixing.id === 'release_copy_set' || fixing.id === 'pills_benefits_set'" :fields="releaseFields" editing :disabled="busy" :invalid-key="releaseInvalidKey" @change="(key, value) => releaseFields[key] = value" /><label v-else :for="`entry-fix-${row.id}`">{{ fixing.id === 'acceptance_present' ? 'Acceptance criteria' : 'Name the blocker' }}</label><textarea v-if="fixing.id !== 'release_copy_set' && fixing.id !== 'pills_benefits_set'" :id="`entry-fix-${row.id}`" v-model="text" class="field" rows="3" :disabled="busy" /></form>
        <form v-if="anyway && person && canEdit && !readonly" class="nr-form" @submit.prevent="move"><div class="nr-form-actions"><button type="button" class="btn sm" :disabled="busy" @click="anyway = false">Cancel</button><button type="submit" class="btn sm primary desktop-submit" :disabled="busy || !block || checks.loading.value || !!checks.error.value || (failed.length > 0 && !reason.trim())">Move to {{ statusMeta(toStatus).label }} <KeyCap k="mod" /><KeyCap k="enter" /></button></div><label :for="`entry-reason-${row.id}`">Why move it now?</label><textarea :id="`entry-reason-${row.id}`" ref="reasonField" v-model="reason" class="field" rows="3" maxlength="500" :disabled="busy" /><p class="nr-hint"><template v-if="failed.length">Required. It’s logged in {{ row.key }}’s activity with the missing checks: {{ failed.map(c => c.label).join(', ') }}.</template><template v-else>Every check passes. No override reason is needed.</template></p></form>
        <p v-if="error" role="alert" class="error">{{ error }}</p>
      </div>
      <div v-if="!readonly" class="nr-phone-actions"><button type="button" class="btn" @click="emit('close', true)">Cancel</button><button v-if="fixing" type="button" class="btn primary" :disabled="busy || (!text.trim() && fixing.id !== 'release_copy_set' && fixing.id !== 'pills_benefits_set')" @click="saveFix">Save <KeyCap k="mod" /><KeyCap k="enter" /></button><button v-else-if="mode === 'queue'" type="button" class="btn primary" :disabled="busy || checks.loading.value || !block || failed.length > 0 || !can('run.create', projectId)" @click="add">Queue</button><button v-else-if="person && canEdit" type="button" class="btn primary" :disabled="busy || !block || checks.loading.value || !!checks.error.value || (failed.length > 0 && !reason.trim())" @click="move">Move to {{ statusMeta(toStatus).label }}</button></div>
    </div>
  </FloatingPanel>
</template>
<style scoped>
.nr { display: flex; flex-direction: column; gap: 8px; padding: 4px; max-height: calc(var(--floating-max) - 14px); min-width: 0; }
.nr-head { display: flex; align-items: center; gap: 8px; padding-left: 6px; flex: none; }
.nr-head h3 { flex: 1; min-width: 0; font-size: 14.5px; font-weight: 600; }
.nr-count { font-weight: 500; color: var(--ink-2); }
.nr-head .mono { font-size: 11.5px; color: var(--ink-3); }
.nr-lede { flex: none; padding: 0 6px; font-size: 12.5px; color: var(--ink-2); }
.nr-body { min-height: 0; overflow: auto; display: grid; gap: 8px; }
.nr-refusal { display: flex; align-items: flex-start; gap: 8px; padding: 7px 10px; border-radius: 9px; background: var(--surface-sunken); font-size: 12px; line-height: 1.5; color: var(--ink-2); }
.nr-refusal svg { flex: none; margin-top: 2px; }
.nr-record { padding: 4px 6px; color: var(--ink-2); font-size: 12.5px; overflow-wrap: anywhere; }
.nr-list { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.nr-item { display: flex; align-items: center; gap: 10px; height: 76px; box-sizing: border-box; padding: 5px 6px 5px 8px; border-radius: 9px; font-size: 13px; }
.nr-item.missing { background: var(--surface-sunken); }
.nr-item.na { opacity: .7; }
.nr-mark { display: grid; place-items: center; flex: none; width: 18px; color: var(--queue-wait-ink); }
.ok .nr-mark { color: var(--ok); }
.nr-label { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.nr-label b { font-weight: 600; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; }
.tick-label { display: grid; }
.tick-label > span { grid-area: 1 / 1; }
.tick-label .measure { visibility: hidden; }
.nr-label small { display: block; font-size: 12px; line-height: 1.45; color: var(--ink-3); height: 2.9em; overflow: hidden; }
.nr-foot { display: grid; gap: 8px; border-top: 1px solid var(--line); padding: 8px 6px 2px; }
.nr-source { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; color: var(--ink-3); }
.nr-actions { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 8px; }
.nr-agent { display: flex; gap: 6px; color: var(--ink-3); font-size: 12px; }
.nr-phone-actions { display: none; }
.nr-form { display: grid; gap: 6px; padding: 10px; background: var(--surface-sunken); border-radius: 10px; }
.nr-form-actions { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 8px; }
.nr-form label { font-size: 12.5px; font-weight: 600; }
.nr-hint { font-size: 12px; color: var(--ink-3); overflow-wrap: anywhere; }
.error { color: var(--danger); font-size: 12px; padding: 0 6px; }
@media (max-width: 600px) { .nr { height: 100%; max-height: none; padding: max(8px, env(safe-area-inset-top)) 4px max(8px, env(safe-area-inset-bottom)); } .nr-body { flex: 1; } .nr-phone-actions { display: flex; gap: 8px; justify-content: space-between; padding-top: 8px; border-top: 1px solid var(--line); flex: none; } .nr-phone-actions .btn { min-height: 44px; } .queue-submit, .desktop-submit { display: none; } .nr-item { height: 88px; } .nr-form-actions .btn { min-height: 44px; } }
</style>
