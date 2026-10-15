<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { vClipTip } from '../../directives/clipTip'
import { getNode } from '../../lib/api'
import type { DeskItem } from '../../lib/decisionDesk'
import { markPersonStep, reassignPersonStep, type PersonStep, type PersonSteps } from '../../lib/personSteps'
import { rowStore } from '../../lib/rowStore'
import { ticketPath } from '../../lib/work'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import PersonStepOwner from '../work/PersonStepOwner.vue'

const props = defineProps<{ item: DeskItem }>()
const emit = defineEmits<{ settled: [item: DeskItem]; busy: [value: boolean] }>()
const session = useSession(), root = ref<HTMLElement>(), more = ref<HTMLElement | null>(null), picker = ref<InstanceType<typeof PersonStepOwner>>(), busy = ref(false), error = ref(''), height = ref<number>()
const tickButton = ref<HTMLButtonElement>(), undoButton = ref<HTMLButtonElement>()
const step = computed(() => props.item.personStep!)
const href = computed(() => ticketPath(props.item.projectKey ?? props.item.projectId, step.value.ticket_key))
const resolution = ref(props.item.decided ? props.item.answer ?? '' : ''), undo = ref(props.item.personStepUndo)
let disposed = false
const identity = () => `${session.identity?.tenant.id}:${session.identity?.principal.id}`
function freeze() { height.value = root.value?.getBoundingClientRect().height }
async function publish(result: PersonSteps) {
  const actor = identity(), sent = rowStore.mark()
  try { const node = await getNode(result.node_id); if (!disposed && actor === identity()) rowStore.adoptNode(node, sent, { show: true }) }
  catch { /* Native write is confirmed; the next list read refreshes its row. */ }
}
async function checked() {
  if (busy.value || !step.value.can_tick) return
  const original = { ...step.value }, actor = identity()
  freeze(); busy.value = true; emit('busy', true); error.value = ''
  try {
    const result = await markPersonStep(original, true), updated = result.steps.find(value => value.id === original.id)
    if (disposed || actor !== identity() || props.item.personStep?.revision !== original.revision) return
    if (!updated?.checked) throw new Error('Completion was not confirmed. Retry after refreshing.')
    resolution.value = 'Checked by you · moves to Decided'; undo.value = { step: updated }
    emit('settled', { ...props.item, personStep: updated, personStepUndo: undo.value, decided: true, answer: resolution.value })
    await nextTick(); if (!disposed) undoButton.value?.focus({ preventScroll: true })
    void publish(result)
  } catch (e) { if (!disposed && actor === identity()) error.value = e instanceof Error ? e.message : 'The check could not be saved.' }
  finally { if (!disposed && actor === identity()) { busy.value = false; emit('busy', false) } }
}
function reassigned(result: PersonSteps) {
  const original = step.value, updated = result.steps.find(value => value.id === original.id)
  if (!updated) { error.value = 'The step changed. Refresh before continuing.'; return }
  freeze(); more.value = null
  if (updated.owner.id === original.owner.id) return
  resolution.value = `Asked ${updated.owner.name} · it left your Desk`
  undo.value = { step: updated, owner: original.owner.id, event: result.undo_reassignment }
  emit('settled', { ...props.item, personStep: updated, personStepUndo: undo.value, decided: true, answer: resolution.value })
  void nextTick(() => { if (!disposed) undoButton.value?.focus({ preventScroll: true }) })
  void publish(result)
}
async function undoChange() {
  if (busy.value || !undo.value) return
  const action = undo.value, actor = identity(), id = step.value.id
  busy.value = true; emit('busy', true); error.value = ''
  try {
    const result = action.owner ? await reassignPersonStep(action.step, action.owner, action.event) : await markPersonStep(action.step, false)
    const updated = result.steps.find(value => value.id === id)
    if (disposed || actor !== identity() || step.value.id !== id) return
    if (!updated) throw new Error('The step changed. Refresh before continuing.')
    resolution.value = ''; undo.value = undefined
    emit('settled', { ...props.item, personStep: updated, personStepUndo: undefined, decided: false, answer: undefined })
    await nextTick(); if (!disposed) tickButton.value?.focus({ preventScroll: true })
    void publish(result)
  } catch (e) { if (!disposed && actor === identity()) error.value = e instanceof Error ? e.message : 'Undo could not be saved.' }
  finally { if (!disposed && actor === identity()) { busy.value = false; emit('busy', false) } }
}
function ask() { const anchor = more.value; more.value = null; if (anchor) picker.value?.openAt(anchor) }
onBeforeUnmount(() => { disposed = true; emit('busy', false) })
</script>
<template>
  <div ref="root" class="person-request" :class="{ resolved: !!resolution }" :style="height ? { height: `${height}px` } : {}" :data-testid="`desk-row-${item.id}`">
    <span class="request-kind">Action request</span>
    <span class="request-topic"><RouterLink :to="href" class="request-title">{{ resolution || item.title }}</RouterLink><small v-clip-tip="error || `${item.projectName} · ${step.ticket_key} · ${step.ticket_title} · ${item.meanwhile}`" :role="error ? 'alert' : undefined">{{ error || `${item.projectName} · ${step.ticket_key} · ${step.ticket_title} · ${item.meanwhile}` }}</small></span>
    <span class="request-actions">
      <button v-if="resolution && undo" ref="undoButton" type="button" class="btn sm" :disabled="busy" @click="undoChange">Undo</button>
      <template v-else><button ref="tickButton" type="button" class="btn sm primary" :disabled="busy || !step.can_tick" @click="checked"><AppIcon name="person-check" :size="13" />Mark checked</button><button type="button" class="icon-btn sm flat" aria-haspopup="menu" :aria-expanded="!!more" :aria-label="`More for ${item.title}`" :disabled="busy" @click="more = $event.currentTarget as HTMLElement"><AppIcon name="more" :size="15" /></button></template>
    </span>
    <FloatingPanel v-if="more" :anchor="more" :width="300" label="More" @close="more = null"><div class="request-menu" role="menu"><RouterLink :to="href" role="menuitem">Open {{ step.ticket_key }}</RouterLink><button type="button" role="menuitem" :disabled="!step.can_tick" @click="ask">Ask someone else…</button></div></FloatingPanel>
    <PersonStepOwner ref="picker" :step="step" headless @changed="reassigned" @busy="value => { busy = value; emit('busy', value) }" />
  </div>
</template>
<style scoped>
.person-request { position: relative; display: grid; grid-template-columns: 120px minmax(0, 1fr) auto; align-items: center; gap: 22px; padding: 20px 0; box-sizing: border-box; border-bottom: 1px solid var(--line); color: var(--ink); }
.person-request:hover { background: var(--row-hover); }
.request-kind { font-size: 11px; color: var(--ink-3); }
.request-topic { min-width: 0; }
.request-title { display: block; color: inherit; text-decoration: none; font-size: 14px; font-weight: 550; overflow-wrap: anywhere; }
.request-topic small { display: block; margin-top: 7px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--ink-3); }
.request-topic small[role=alert] { color: var(--warn-ink); }
.request-actions { display: flex; align-items: center; gap: 8px; justify-self: end; }
.request-actions > .btn { white-space: nowrap; }
.resolved { color: var(--ink-2); }
.request-menu { display: grid; }
.request-menu a,.request-menu button { display: flex; align-items: center; padding: 10px; min-height: 44px; box-sizing: border-box; border: 0; background: transparent; color: var(--ink); font: inherit; text-decoration: none; text-align: left; }
.request-menu a:hover,.request-menu button:hover { background: var(--row-hover); }
@media (max-width: 720px) { .person-request { grid-template-columns: minmax(0, 1fr) auto; gap: 10px; padding: 16px 0; } .request-kind { grid-column: 1 / -1; } .request-title { font-size: 13px; } }
@media (pointer: coarse) { .request-actions button { min-height: 44px; } }
</style>
