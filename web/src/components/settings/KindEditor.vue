<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import { isSettingsField, settingsSubmitKey } from '../../lib/settingsOverlays'
import { parseKindWords, type KindWords, type WorkKind } from '../../lib/workKinds'
import type { KindsText } from '../../lib/workKindsCopy'
const props = defineProps<{ kind: WorkKind | null; busy: boolean; editable: boolean; error: string; opener: HTMLElement | null; t: KindsText }>()
const emit = defineEmits<{ close: []; save: [words: KindWords] }>()
const draft = ref({ label: props.kind?.label ?? '', hint: props.kind?.hint ?? '', examples: props.kind?.examples.join('\n') ?? '', labels: props.kind?.labels.join(', ') ?? '' })
const form = ref<HTMLFormElement>(), panel = ref<HTMLElement>(), title = ref<HTMLElement>()
const invalid = ref(false), titleId = useId()
const position = ref<Record<string, string>>({})
function place() {
  const row = props.opener?.closest<HTMLElement>('[data-kind]'), card = props.opener?.closest<HTMLElement>('.settings-card')
  const bounds = (row ?? card)?.getBoundingClientRect()
  if (!bounds) return
  const top = Math.max(16, Math.min(row ? bounds.top : props.opener!.getBoundingClientRect().bottom + 8, innerHeight - 280))
  position.value = { left: `${Math.max(12, bounds.left)}px`, top: `${top}px`, width: `${Math.min(bounds.width, innerWidth - 24)}px`, maxHeight: `${innerHeight - top - 16}px` }
}
const error = computed(() => props.error || (invalid.value ? props.t('invalid') : ''))
function save() {
  if (props.busy || !props.editable) return
  const words = parseKindWords(draft.value)
  invalid.value = !words
  if (words && form.value?.reportValidity()) emit('save', words)
}
function keys(event: KeyboardEvent) {
  if (event.defaultPrevented) return
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopImmediatePropagation()
    if (isSettingsField(event.target)) { (event.target as HTMLElement).blur(); title.value?.focus({ preventScroll: true }) }
    else emit('close')
  } else if (settingsSubmitKey(event)) { event.preventDefault(); event.stopImmediatePropagation(); save() }
  else if (event.key === 'Tab' && !event.metaKey && !event.ctrlKey && !event.altKey) {
    const controls = [...panel.value!.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), textarea:not(:disabled), [tabindex="0"]')].filter(el => el.getClientRects().length)
    const first = controls[0], last = controls.at(-1)
    if (!first || !last) { event.preventDefault(); title.value?.focus() }
    else if (event.shiftKey && (document.activeElement === first || document.activeElement === title.value)) { event.preventDefault(); last.focus() }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
  }
}
let background: HTMLElement | null = null, wasInert = false
onMounted(async () => {
  background = document.getElementById('app'); wasInert = background?.inert ?? false
  if (background) background.inert = true
  place(); window.addEventListener('resize', place)
  window.addEventListener('keydown', keys)
  await nextTick(); form.value?.querySelector('input')?.focus({ preventScroll: true })
})
onBeforeUnmount(() => {
  window.removeEventListener('resize', place)
  window.removeEventListener('keydown', keys)
  if (background) background.inert = wasInert
  if (props.opener?.isConnected) props.opener.focus({ preventScroll: true })
})
</script>
<template>
  <Teleport to="body">
    <div class="editor-overlay" @pointerdown.self="emit('close')">
      <section ref="panel" class="kind-editor glass-card" :style="position" role="dialog" aria-modal="true" :aria-labelledby="titleId">
        <header class="editor-head"><AppIcon name="edit" /><h2 :id="titleId" ref="title" tabindex="-1">{{ kind ? `${t('edit')} · ${kind.label}` : t('newKind') }}</h2><button class="btn ghost sm" type="button" :aria-label="t('cancel')" @click="emit('close')"><AppIcon name="close" /></button></header>
        <footer class="editor-actions"><button class="btn ghost" type="button" @click="emit('close')">{{ t('cancel') }}<KeyCap k="Esc" /></button><button class="btn primary" type="button" :disabled="busy || !editable" @click="save">{{ kind ? t('save') : t('create') }} <KeyCap k="mod" /><KeyCap k="enter" /></button></footer>
        <form ref="form" class="kind-form" autocomplete="off" @submit.prevent="save">
          <fieldset :disabled="busy || !editable">
            <label>{{ t('shortName') }}<small>{{ t('nameHint') }}</small><input v-model="draft.label" class="field" name="label" maxlength="40" required></label>
            <label>{{ t('sentence') }}<small>{{ t('sentenceHint') }}</small><input v-model="draft.hint" class="field" name="hint" maxlength="120" required></label>
            <label>{{ t('examples') }}<small>{{ t('examplesHint') }}</small><textarea v-model="draft.examples" class="field" name="examples" rows="3" maxlength="362" /></label>
            <div class="kf-row"><label>{{ t('areas') }}<input class="field" :value="kind?.slug ?? t('generatedArea')" readonly><small>{{ t('areaHint') }}</small></label><label>{{ t('labels') }}<input v-model="draft.labels" class="field" name="labels" maxlength="1598"></label></div>
          </fieldset>
          <p class="feedback" :class="{ err: error }" role="status">{{ error }}</p>
        </form>
      </section>
    </div>
  </Teleport>
</template>
<style scoped>
.editor-overlay { position: fixed; inset: 0; z-index: 65; background: transparent; }
.kind-editor { position: fixed; display: flex; flex-direction: column; background: var(--surface-raised); width: min(42rem, 100%); max-height: calc(100dvh - 32px); overflow: hidden; }
.editor-head { display: flex; flex: none; align-items: center; gap: 10px; padding: 14px 16px; border-bottom: 1px solid var(--line); }.editor-head h2 { flex: 1; min-width: 0; font-size: 16px; overflow-wrap: anywhere; }.editor-head button { flex: none; }
.editor-actions { display: flex; flex: none; flex-wrap: wrap; justify-content: flex-end; gap: 10px; padding: 12px 16px; border-bottom: 1px solid var(--line); }
.kind-form { padding: 16px; overflow: auto; min-height: 0; overscroll-behavior: contain; }
fieldset { display: grid; gap: 12px; border: 0; padding: 0; margin: 0; min-width: 0; }
label { display: grid; gap: 4px; min-width: 0; font-size: 12.5px; font-weight: 600; color: var(--ink-2); }small { font-weight: 400; color: var(--ink-3); }
.field { width: 100%; min-width: 0; }textarea { resize: vertical; min-height: 72px; font: inherit; }.kf-row { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.feedback { margin-top: 12px; min-height: 2lh; font-size: 12px; overflow-wrap: anywhere; }.err { color: var(--danger); }
@media (max-width: 720px) { .editor-overlay { padding: 0; }.kind-editor { inset: 0 !important; width: 100% !important; height: 100dvh; max-height: 100dvh !important; border-radius: 0; }.editor-head { padding-top: max(14px, env(safe-area-inset-top)); }.kind-form { flex: 1; }.editor-actions { order: 1; border-bottom: 0; border-top: 1px solid var(--line); padding-bottom: calc(12px + env(safe-area-inset-bottom)); }.kf-row { grid-template-columns: minmax(0, 1fr); }button, input { min-height: 44px; } }
@media (pointer: coarse) { button, input { min-height: 44px; } }
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
