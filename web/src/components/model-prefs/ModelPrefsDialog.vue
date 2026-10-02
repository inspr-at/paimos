<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import RulesDialog from '../rules/RulesDialog.vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import LevelTabs from './LevelTabs.vue'
import ProvidersPanel from './ProvidersPanel.vue'
import KindTable from './KindTable.vue'
import ModelPicker from './ModelPicker.vue'
import { canAddKind, lowerLevelLabel, modelCopy, providerWarning, resetLevelLabel, summaryLabel, type ModelSelector } from '../../lib/modelPrefs'
import { useModelPrefsEditor } from '../../lib/useModelPrefsEditor'
import type { PrefsContext } from '../../lib/modelPrefsCommand'
const props = defineProps<{ context: PrefsContext }>()
const emit = defineEmits<{ close: [] }>()
const editor = useModelPrefsEditor(props.context.project?.id, props.context.level ?? (props.context.project ? 'project' : 'person'))
const { doc, level, profiles, candidates, loading, busy, notice, catalogError, outside } = editor
const view = computed(() => doc.value?.views[level.value]), scope = computed(() => doc.value?.levels[level.value])
const editable = computed(() => doc.value?.can[`edit_${level.value}`] ?? false)
const picker = ref<{ kind: string; bucket: 'normal' | 'complex' } | null>(null)
const pickedKind = computed(() => doc.value?.kinds.find(k => k.id === picker.value?.kind))
const pickedRow = computed(() => view.value?.rows.find(r => r.kind_id === picker.value?.kind))
const addOpen = ref(false), newKind = ref(''), archiveKind = ref(''), resetAll = ref(false)
const labelInput = ref<HTMLInputElement>(), content = ref<HTMLElement>()
const focusedKind = computed(() => doc.value?.kinds.find(k => !k.archived_at && k.slug === props.context.kind) ?? doc.value?.kinds.find(k => k.system === 'other'))
const whyRow = computed(() => view.value?.rows.find(r => r.kind_id === focusedKind.value?.id))
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
onMounted(async () => { await editor.load(); await nextTick(); if (props.context.kind) content.value?.querySelector(`[data-kind="${CSS.escape(props.context.kind)}"]`)?.scrollIntoView({ block: 'nearest' }) })
function switchLevel(value: typeof level.value) { if (busy.value) return; level.value = value; picker.value = null; addOpen.value = false; archiveKind.value = ''; resetAll.value = false; notice.value = '' }
async function add() { if (!newKind.value.trim() || newKind.value.trim().length > 60 || busy.value) return; await editor.addKind(newKind.value); if (!notice.value) { newKind.value = ''; addOpen.value = false } }
async function openAdd() { addOpen.value = !addOpen.value; await nextTick(); if (addOpen.value) labelInput.value?.focus() }
function choose(value: ModelSelector) { const target = picker.value; if (!target) return; picker.value = null; void editor.row(target.kind, { bucket: target.bucket, value }) }
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
    <template #footer><div class="prefs-footer"><p class="summary">{{ summaryLabel(level, view?.changes ?? 0) }}</p><button type="button" class="btn ghost reset-all" :disabled="busy || !editable || !view?.changes" @click="resetAll = !resetAll">{{ resetLevelLabel(level) }}</button><button type="button" class="btn primary done" :disabled="busy" data-autofocus @click="emit('close')">Done <kbd class="keycap">Esc</kbd></button></div></template>
    <div ref="content" class="prefs-content" :style="{ '--lv': `var(--level-${level})` }">
      <p v-if="loading && !doc" role="status">Loading preferences…</p>
      <template v-if="doc && view && scope">
        <p class="read-only">{{ editable ? level === 'default' ? 'The default applies to everyone.' : level === 'person' ? 'Your settings apply across your projects.' : `Settings for ${context.project?.title}.` : level === 'default' ? 'Read only. Only workspace admins change the default.' : level === 'project' ? 'Read only. Project permission is needed to change this level.' : 'Read only. Sign in as a person to change your settings.' }}</p>
        <ProvidersPanel :level="level" :view="view.residency" :mode="doc.residency_lock_mode" :locked="scope.residency_locked" :editable="editable" :busy="busy" @choose="value => editor.scope({ residency: value })" @lock="value => editor.scope({ residency_locked: value })" />
        <section class="kinds-section" aria-label="Kinds of work"><h3 class="eyebrow">Kinds of work</h3><KindTable :doc="doc" :level="level" :busy="busy" @pick="(kind, bucket) => picker = { kind, bucket }" @reset="editor.reset" @lock="kind => editor.row(kind, undefined, true)" @archive="kind => archiveKind = archiveKind === kind ? '' : kind" /></section>
        <div class="legend"><span v-for="value in (['default', 'person', 'project'] as const)" :key="value"><span class="level-dot" :style="{ '--lv': `var(--level-${value})` }" />{{ value === 'default' ? 'Default' : value === 'person' ? 'You, all your projects' : `This project${context.project ? ` (${context.project.title})` : ''}` }}</span></div>
        <label v-if="level !== 'project'" class="switch prefs-lock"><input type="checkbox" :checked="!scope.prefs_locked" :disabled="busy || !editable || level === 'person' && !!doc.levels.default?.prefs_locked" @change="editor.scope({ prefs_locked: !($event.target as HTMLInputElement).checked })">{{ lowerLevelLabel(level) }}</label>
        <button v-if="canAddKind(doc, level)" type="button" class="btn ghost add-kind" :disabled="busy" @click="openAdd"><AppIcon name="plus" :size="14" />Add a kind of work {{ level === 'project' ? 'for this project only' : '(becomes a ticket area)' }}</button>
        <p class="footnote"><AppIcon name="refresh" :size="12" /> follows new versions · <AppIcon name="pin" :size="12" /> pinned · {{ mac ? 'Option' : 'Alt' }}-click Set by to toggle a row lock. Kinds of work are ticket areas.</p>
        <form v-if="addOpen" class="add-form" @submit.prevent="add"><label for="model-kind-label">Name</label><input id="model-kind-label" ref="labelInput" v-model="newKind" maxlength="60" :disabled="busy" autocomplete="off"><button class="btn" type="submit" :disabled="busy || !newKind.trim()">Add <KeyCap k="mod" /><KeyCap k="enter" /></button></form>
        <div v-if="archiveKind" class="confirmation"><p>Remove {{ doc.kinds.find(k => k.id === archiveKind)?.label }}? Tickets keep their area and use Everything else.</p><button class="btn" type="button" :disabled="busy" @click="editor.archive(archiveKind).then(() => { if (!notice) archiveKind = '' })">Remove kind</button><button class="btn ghost" type="button" :disabled="busy" @click="archiveKind = ''">Cancel</button></div>
        <div v-if="resetAll" class="confirmation"><p>Reset this level? Its settings will be inherited from the broader level.</p><button class="btn" type="button" :disabled="busy" @click="editor.reset()?.then(() => { if (!notice) resetAll = false })">Confirm reset</button><button class="btn ghost" type="button" :disabled="busy" @click="resetAll = false">Cancel</button></div>
        <p v-if="providerWarning(view.residency)" class="warning" role="status"><AppIcon name="alert" :size="15" />{{ providerWarning(view.residency) }}</p>
        <div v-if="outside.length" class="warning outside"><p>{{ outside.length }} agents are still running on providers outside this setting. Nothing is stopped automatically.</p><div><a v-for="(id, i) in outside" :key="id" :href="`/agents?run=${encodeURIComponent(id)}`">Agent {{ i + 1 }}</a></div></div>
        <section v-if="context.why && whyRow" class="why"><h3>Why this model?</h3><p v-if="context.preview">Shown on the ticket: {{ context.preview }}. These are your current preferences; planning may use the assignee’s settings.</p><p>{{ focusedKind?.label }} · set by {{ whyRow.set_by || 'default' }}{{ whyRow.locked_by ? ` · locked by ${whyRow.locked_by}` : '' }}.</p><p>Normally: {{ modelCopy(whyRow.normal, focusedKind?.system === 'review').tip }}</p><p>If it’s complex: {{ modelCopy(whyRow.complex, focusedKind?.system === 'review').tip }}</p><p>Automatic keeps the ticket’s role. Review family and security rules still apply.</p></section>
      </template>
      <p class="save-status" role="status">{{ busy ? 'Saving…' : notice }}</p><button v-if="!doc && !loading" type="button" class="btn" @click="editor.load">Try again</button>
    </div>
    <ModelPicker v-if="picker && pickedKind && pickedRow && view" :key="`${level}-${picker.kind}-${picker.bucket}`" :label="`${pickedKind.label} · ${picker.bucket === 'normal' ? 'Normally' : 'If it’s complex'}`" :profiles="profiles" :choices="view.choices" :candidates="candidates" :selector="pickedRow[picker.bucket].selector" :residency="view.residency" :review="pickedKind.system === 'review'" :catalog-error="catalogError" @close="picker = null" @choose="choose" />
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
.kinds-section { display: grid; gap: 8px; } .prefs-footer { display: grid; grid-template-columns: minmax(0, 1fr) 210px 90px; gap: 8px; align-items: center; width: 100%; } .summary { margin: 0; font-size: 12px; color: var(--ink-2); } .reset-all { font-size: 12px; white-space: normal; height: 40px; } .done { height: 40px; }
.legend { display: flex; flex-wrap: wrap; gap: 8px 16px; color: var(--ink-2); font-size: 12px; } .legend > span { display: inline-flex; align-items: center; gap: 6px; }
.prefs-lock { font-size: 12px; white-space: normal; } .add-kind { justify-self: start; min-height: 44px; height: auto; white-space: normal; text-align: left; }
.footnote { color: var(--ink-3); font-size: 12px; line-height: 1.6; } .footnote svg { display: inline-block; vertical-align: -2px; }
.add-form { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; } .add-form input { min-width: 0; width: 180px; height: 36px; padding: 0 8px; }
.confirmation { display: flex; flex-wrap: wrap; gap: 8px; } .confirmation p { width: 100%; font-size: 12px; } .warning { display: flex; align-items: flex-start; gap: 8px; padding: 12px; border-radius: 10px; background: var(--gold-wash); color: var(--warn-ink); font-size: 12px; }
.warning svg { flex: none; } .why { display: grid; gap: 6px; padding: 12px 0; border-top: 1px solid var(--line); font-size: 12px; color: var(--ink-2); } .why h3 { color: var(--ink); font-size: 13px; }
.outside { display: grid; } .outside > div { display: flex; flex-wrap: wrap; gap: 8px 16px; }
.save-status { min-height: 18px; font-size: 12px; color: var(--ink-2); }
@media (max-width: 600px) { .prefs-footer { grid-template-columns: minmax(0, 1fr) 90px; } .summary { grid-column: 1 / -1; min-height: 18px; } .reset-all { width: 100%; } .prefs-lock { min-height: 44px; } .read-only { min-height: 36px; } }
</style>
