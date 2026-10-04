<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import RulesDialog from '../rules/RulesDialog.vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import LevelTabs from './LevelTabs.vue'
import ProvidersPanel from './ProvidersPanel.vue'
import KindTable from './KindTable.vue'
import ModelPicker from './ModelPicker.vue'
import { canAddKind, LEVEL_LABEL, RESIDENCY_LABEL, lowerLevelLabel, resetLevelLabel, summaryLabel, type TicketModelResolution, type ModelSelector } from '../../lib/modelPrefs'
import { useModelPrefsEditor } from '../../lib/useModelPrefsEditor'
import { getTicketModelResolution } from '../../lib/modelPrefsApi'
import type { PrefsContext } from '../../lib/modelPrefsCommand'
const props = defineProps<{ context: PrefsContext }>()
const emit = defineEmits<{ close: [] }>()
const editor = useModelPrefsEditor(props.context.project?.id, props.context.level ?? (props.context.project ? 'project' : 'person'))
const { doc, level, loading, busy, notice, outside } = editor
const view = computed(() => doc.value?.views[level.value]), scope = computed(() => doc.value?.levels[level.value])
const editable = computed(() => doc.value?.can[`edit_${level.value}`] ?? false)
let pickerOpener: HTMLElement | null = null
function openPicker(kind: string, bucket: 'normal' | 'complex') { if (busy.value) return; pickerOpener = document.activeElement as HTMLElement; picker.value = { kind, bucket } }
async function closePicker() { picker.value = null; await nextTick(); pickerOpener?.focus({ preventScroll: true }) }
const picker = ref<{ kind: string; bucket: 'normal' | 'complex' } | null>(null)
const pickedKind = computed(() => doc.value?.kinds.find(k => k.id === picker.value?.kind))
const pickedRow = computed(() => view.value?.rows.find(r => r.kind_id === picker.value?.kind))
const addOpen = ref(false), newKind = ref(''), archiveKind = ref(''), resetAll = ref(false)
const labelInput = ref<HTMLInputElement>(), addButton = ref<HTMLButtonElement>(), content = ref<HTMLElement>()
const focusedKind = computed(() => doc.value?.kinds.find(k => !k.archived_at && k.slug === props.context.kind) ?? doc.value?.kinds.find(k => k.system === 'other'))
const resolution = ref<TicketModelResolution | null>(null), whyError = ref(''), whyLoading = ref(false)
const trace = computed(() => resolution.value?.trace)
const traceKind = computed(() => doc.value?.kinds.find(k => k.slug === trace.value?.kind)?.label ?? trace.value?.kind)
const whyAbort = new AbortController()
onBeforeUnmount(() => whyAbort.abort())
async function loadWhy() {
  if (!props.context.why) return
  if (!props.context.ticket) { whyError.value = 'Ticket identity is missing; its model resolution cannot be explained.'; return }
  whyLoading.value = true; whyError.value = ''
  try {
    const value = await getTicketModelResolution(props.context.ticket, whyAbort.signal)
    if (!whyAbort.signal.aborted) resolution.value = value
  } catch (error) { if (!whyAbort.signal.aborted) whyError.value = error instanceof Error ? error.message : 'Model resolution could not be loaded' }
  finally { if (!whyAbort.signal.aborted) whyLoading.value = false }
}
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const resetKeep = ref<HTMLButtonElement>(), resetButton = ref<HTMLButtonElement>()
async function toggleReset(value: boolean) {
  if (busy.value) return
  if (value) notice.value = ''
  resetAll.value = value
  await nextTick()
  ;(value ? resetKeep.value : resetButton.value)?.focus({ preventScroll: true })
}
async function confirmReset() { await editor.reset(); if (!notice.value) await toggleReset(false) }
async function focusKind(kind: string | undefined) {
  await nextTick()
  const slug = doc.value?.kinds.find(k => k.id === kind)?.slug
  const chip = slug ? content.value?.querySelector<HTMLButtonElement>(`[data-kind="${CSS.escape(slug)}"] .model-chip:not(:disabled)`) : null
  ;(chip ?? resetButton.value)?.focus({ preventScroll: true })
}
async function resetRow(kind: string) {
  await editor.reset(kind)
  if (!notice.value) await focusKind(kind)
}
async function remove(kind: string) {
  // Removed kinds resolve to Everything else; its surviving chip is the focus target.
  const fallback = doc.value?.kinds.find(k => k.system === 'other')?.id
  await editor.archive(kind)
  if (!notice.value) { archiveKind.value = ''; await focusKind(fallback) }
}
onMounted(async () => {
  await Promise.all([editor.load(), loadWhy()]); await nextTick()
  if (!props.context.why && props.context.kind && focusedKind.value) content.value?.querySelector(`[data-kind="${CSS.escape(focusedKind.value.slug)}"]`)?.scrollIntoView({ block: 'nearest' })
})
function switchLevel(value: typeof level.value) { if (busy.value) return; level.value = value; picker.value = null; addOpen.value = false; archiveKind.value = ''; resetAll.value = false; notice.value = ''; outside.value = [] }
async function add() { if (!newKind.value.trim() || newKind.value.trim().length > 60 || busy.value) return; await editor.addKind(newKind.value); if (!notice.value) { newKind.value = ''; addOpen.value = false; await nextTick(); addButton.value?.focus({ preventScroll: true }) } }
async function openAdd() { if (busy.value) return; addOpen.value = !addOpen.value; await nextTick(); if (addOpen.value) labelInput.value?.focus() }
function choose(value: ModelSelector) { const target = picker.value; if (!target) return; void closePicker(); void editor.row(target.kind, { bucket: target.bucket, value }) }
function keys(event: KeyboardEvent) {
  const target = event.target as HTMLElement
  const field = target.matches('input,textarea,select,[contenteditable="true"]')
  if (event.key === 'Escape' && field) { event.preventDefault(); event.stopPropagation(); target.blur(); return }
  if (event.key === 'Enter' && field && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) && !event.altKey) { event.preventDefault(); event.stopPropagation(); void add() }
}
</script>
<template>
  <RulesDialog title="Which models do which work" lede="Default, then You, then Project. The dot shows where each setting comes from." size="wide" class="model-prefs-dialog" :busy="busy" @close="emit('close')" @keydown.capture="keys">
    <template #pinned><p class="eyebrow">Agents · settings</p><LevelTabs :level="level" :project="context.project?.title" :busy="busy" @change="switchLevel" /></template>
    <template #footer><div class="prefs-footer">
      <p class="summary" role="status" aria-atomic="true" :tabindex="notice ? 0 : undefined"><span class="save-status">{{ busy ? 'Saving…' : notice }}</span><span v-if="!busy && !notice">{{ resetAll ? level === 'default' ? 'Every default goes back to Automatic and Any provider.' : `Remove ${view?.changes ?? 0} ${(view?.changes ?? 0) === 1 ? 'change' : 'changes'}?` : summaryLabel(level, view?.changes ?? 0) }}</span></p>
      <button ref="resetButton" type="button" class="btn ghost reset-all" :disabled="!editable" :aria-disabled="busy || !view?.changes" @click="!busy && !!view?.changes && (resetAll ? confirmReset() : toggleReset(true))">{{ resetAll ? 'Confirm reset' : resetLevelLabel(level) }}</button>
      <button v-if="resetAll" ref="resetKeep" type="button" class="btn ghost done" :aria-disabled="busy" @click="toggleReset(false)">Keep</button>
      <button v-else type="button" class="btn primary done" :aria-disabled="busy" data-autofocus @click="!busy && emit('close')">Done <kbd class="keycap">Esc</kbd></button>
    </div></template>
    <div ref="content" class="prefs-content" :style="{ '--lv': `var(--level-${level})` }">
      <p v-if="loading && !doc" role="status">Loading preferences…</p>
      <section v-if="context.why" class="why" aria-label="Why this model?">
        <h3>Why this model?</h3><p v-if="context.preview">Shown on the ticket: {{ context.preview }}.</p>
        <p v-if="whyLoading" role="status">Loading the ticket’s resolution…</p>
        <p v-if="whyError" role="status">{{ whyError }}</p>
        <button v-if="whyError && context.ticket" type="button" class="btn ghost" @click="loadWhy">Try explanation again</button>
        <ol v-if="trace && resolution">
          <li>Kind of work: <b>{{ traceKind }}</b>, {{ trace.kind_source === 'fallback' ? 'Everything else because the ticket’s area has no active kind' : 'from the ticket’s area' }}.</li>
          <li>Complexity: <b>{{ trace.bucket === 'complex' ? 'If it’s complex' : 'Normally' }}</b>{{ trace.complexity ? ` (${trace.complexity})` : '' }}, {{ trace.complexity_source === 'role' ? 'from the ticket’s role' : trace.complexity_source === 'person' ? 'set by a person' : trace.complexity_source === 'agent' ? 'set by an agent' : 'from the ticket' }}.</li>
          <li><b>{{ LEVEL_LABEL[trace.set_by] || 'Default' }}</b> selects {{ trace.mode === 'auto' ? `Automatic: the ${resolution.role} role ladder` : trace.mode === 'latest' ? 'a model that follows new versions' : 'a pinned model' }}.<template v-if="trace.locked_by"> Locked by {{ LEVEL_LABEL[trace.locked_by] }}; lower levels are not used.</template><template v-if="trace.prefs"> {{ trace.prefs }}.</template></li>
          <li>Allowed providers: <b>{{ RESIDENCY_LABEL[trace.residency.value] }}</b>, set by {{ LEVEL_LABEL[trace.residency.set_by] }}.<template v-if="trace.residency.locked_by"> Locked by {{ LEVEL_LABEL[trace.residency.locked_by] }}.</template><template v-if="trace.residency.loosened_lock"> Looser than the lock; the trace records the choice.</template><template v-if="trace.ticket_requirement && trace.ticket_requirement !== trace.residency.value"> Ticket requirement: {{ RESIDENCY_LABEL[trace.ticket_requirement] }}. Effective rule: {{ RESIDENCY_LABEL[resolution.residency] }}.</template></li>
          <li><template v-if="trace.fallback">Automatic fallback: {{ trace.fallback }}. </template><template v-if="trace.blocked">Work waits: {{ trace.blocked }}.</template><template v-else-if="resolution.profile">Runs <b>{{ resolution.profile.display_name || resolution.profile.model }} {{ resolution.profile.model_version }} · {{ resolution.profile.effort }}</b> today via {{ resolution.profile.harness }}.</template><template v-else>{{ resolution.owner_required ? 'An owner decision is required; nothing runs yet.' : 'No allowed model route; work waits.' }}</template><template v-if="trace.hard?.length"> Hard rules: {{ trace.hard.join(', ') }}.</template></li>
        </ol>
      </section>
      <template v-if="doc && view && scope">
        <p class="read-only">{{ editable ? level === 'default' ? 'The default applies to everyone.' : level === 'person' ? 'Your settings apply across your projects.' : `Settings for ${context.project?.title}.` : level === 'default' ? 'Read only. Only workspace admins change the default.' : level === 'project' ? 'Read only. Project permission is needed to change this level.' : 'Read only. Sign in as a person to change your settings.' }}</p>
        <ProvidersPanel :level="level" :view="view.residency" :mode="doc.residency_lock_mode" :locked="scope.residency_locked" :editable="editable" :busy="busy" :outside="outside" :truncated="view.choices_truncated" @show-runs="emit('close')" @choose="value => editor.scope({ residency: value })" @lock="value => editor.scope({ residency_locked: value })" />
        <section class="kinds-section" aria-label="Kinds of work"><h3 class="eyebrow">Kinds of work</h3><KindTable :doc="doc" :level="level" :busy="busy" :archive-kind="archiveKind" @remove="remove" @keep="archiveKind = ''" @pick="openPicker" @reset="resetRow" @lock="kind => editor.row(kind, undefined, true)" @archive="kind => archiveKind = archiveKind === kind ? '' : kind" /></section>
        <div class="legend"><span v-for="value in (['default', 'person', 'project'] as const)" :key="value"><span class="level-dot" :style="{ '--lv': `var(--level-${value})` }" />{{ value === 'default' ? 'Default' : value === 'person' ? 'You, all your projects' : `This project${context.project ? ` (${context.project.title})` : ''}` }}</span></div>
        <label v-if="level !== 'project'" class="switch prefs-lock"><input type="checkbox" :checked="!scope.prefs_locked" :disabled="!editable || level === 'person' && !!doc.levels.default?.prefs_locked" :aria-disabled="busy || !editable || level === 'person' && !!doc.levels.default?.prefs_locked" @click="busy && $event.preventDefault()" @change="!busy && editor.scope({ prefs_locked: !($event.target as HTMLInputElement).checked })">{{ lowerLevelLabel(level) }}</label>
        <button v-if="canAddKind(doc, level)" ref="addButton" type="button" class="btn ghost add-kind" :aria-disabled="busy" @click="openAdd"><AppIcon name="plus" :size="14" />Add a kind of work {{ level === 'project' ? 'for this project only' : '(becomes a ticket area)' }}</button>
        <p class="footnote"><AppIcon name="refresh" :size="12" /> follows new versions · <AppIcon name="pin" :size="12" /> pinned · {{ mac ? 'Option' : 'Alt' }}-click Set by to toggle a row lock. Kinds of work are ticket areas.</p>
        <form v-if="addOpen" class="add-form" @submit.prevent="add"><label for="model-kind-label">Name</label><input id="model-kind-label" ref="labelInput" v-model="newKind" maxlength="60" :readonly="busy" :aria-disabled="busy" autocomplete="off"><button class="btn" type="submit" :disabled="!newKind.trim()" :aria-disabled="busy || !newKind.trim()">Add <KeyCap k="mod" /><KeyCap k="enter" /></button></form>
      </template>
      <button v-if="!doc && !loading" type="button" class="btn" @click="editor.load">Try again</button>
    </div>
    <ModelPicker v-if="picker && pickedKind && pickedRow && view" :key="`${level}-${picker.kind}-${picker.bucket}`" :label="`${pickedKind.label} · ${picker.bucket === 'normal' ? 'Normally' : 'If it’s complex'}`" :choices="view.choices" :selector="pickedRow[picker.bucket].selector" :residency="view.residency" :review="pickedKind.system === 'review'" :truncated="view.choices_truncated" @close="closePicker" @choose="choose" />
  </RulesDialog>
</template>
<style>
.model-prefs-dialog .level-dot { display: inline-block; width: 8px; height: 8px; flex: none; border-radius: 50%; background: var(--lv, var(--level-default)); }
/* Settings are long content: actions remain below the scrolling body. */
.model-prefs-dialog > .card > .pinned { order: 0; } .model-prefs-dialog > .card > .body { order: 1; } .model-prefs-dialog > .card > .foot { order: 2; }
.model-prefs-dialog > .card > .body { display: block; }
</style>
<style scoped>
.prefs-content { display: grid; gap: 16px; min-width: 0; } .prefs-content p, h3 { margin: 0; } .read-only { font-size: 12px; color: var(--ink-3); min-height: 18px; }
.kinds-section { display: grid; gap: 8px; } .prefs-footer { display: grid; grid-template-columns: minmax(0, 1fr) 210px 110px; gap: 8px; align-items: center; width: 100%; } .summary { margin: 0; min-width: 0; height: 36px; font-size: 12px; line-height: 18px; color: var(--ink-2); overflow: auto; overflow-wrap: anywhere; } .reset-all { font-size: 12px; white-space: normal; height: 40px; } .done { height: 40px; }
.legend { display: flex; flex-wrap: wrap; gap: 8px 16px; color: var(--ink-2); font-size: 12px; } .legend > span { display: inline-flex; align-items: center; gap: 6px; }
.prefs-lock { font-size: 12px; white-space: normal; } .add-kind { justify-self: start; min-height: 44px; height: auto; white-space: normal; text-align: left; }
.footnote { color: var(--ink-3); font-size: 12px; line-height: 1.6; } .footnote svg { display: inline-block; vertical-align: -2px; }
.add-form { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; } .add-form input { min-width: 0; width: 180px; height: 36px; padding: 0 8px; }
.why ol { margin: 0; padding-left: 20px; display: grid; gap: 5px; } .why { display: grid; gap: 6px; padding: 12px 0; border-top: 1px solid var(--line); font-size: 12px; color: var(--ink-2); } .why h3 { color: var(--ink); font-size: 13px; }
@media (max-width: 600px) { .prefs-footer { grid-template-columns: minmax(0, 1fr) 110px; } .summary { grid-column: 1 / -1; } .reset-all { width: 100%; } .prefs-lock { min-height: 44px; } .read-only { min-height: 36px; } }
</style>
