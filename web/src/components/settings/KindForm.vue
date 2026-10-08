<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, useId, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import { isSettingsField, settingsSubmitKey } from '../../lib/settingsOverlays'
import { parseKindWords, type KindWords, type WorkKind } from '../../lib/workKinds'
import type { KindsText } from '../../lib/workKindsCopy'
// The editor of one kind, in place of its row (a new kind: at the end of the
// list). Not a dialog: no overlay, no focus trap, Tab moves on naturally.
const props = defineProps<{ kind: WorkKind | null; busy: boolean; editable: boolean; error: string; t: KindsText }>()
const emit = defineEmits<{ close: []; save: [words: KindWords]; dirty: [dirty: boolean] }>()
const start = { label: props.kind?.label ?? '', hint: props.kind?.hint ?? '', examples: props.kind?.examples.join('\n') ?? '', labels: props.kind?.labels.join(', ') ?? '' }
const draft = ref({ ...start })
const root = ref<HTMLElement>(), form = ref<HTMLFormElement>()
const invalid = ref(false), more = ref(false), moreId = useId()
const dirty = computed(() => draft.value.label !== start.label || draft.value.hint !== start.hint || draft.value.examples !== start.examples || draft.value.labels !== start.labels)
watch(dirty, value => emit('dirty', value))
watch(draft, () => { invalid.value = false }, { deep: true })
const error = computed(() => props.error || (invalid.value ? props.t('invalid') : ''))
const areaNote = computed(() => props.kind?.system ? props.t('builtIn') : props.kind ? props.t('areaHint') : props.t('areaCreated'))
function save() {
  if (props.busy || !props.editable) return
  if (props.kind && !dirty.value) { emit('close'); return }
  const words = parseKindWords(draft.value)
  invalid.value = !words
  // A problem in a field behind More options must never stay out of sight.
  if (!words) { if (draft.value.label.trim() && draft.value.hint.trim()) more.value = true; return }
  if (form.value?.reportValidity()) emit('save', words)
}
// Esc leaves a field first and cancels on the next press, so a stray key never
// discards a draft (repo keyboard convention, AEON-541). ⌘/Ctrl+Enter saves from anywhere.
function keys(event: KeyboardEvent) {
  if (event.defaultPrevented) return
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopPropagation()
    if (isSettingsField(event.target)) { (event.target as HTMLElement).blur(); root.value?.focus({ preventScroll: true }) }
    else emit('close')
  } else if (settingsSubmitKey(event)) { event.preventDefault(); event.stopPropagation(); save() }
}
onMounted(async () => {
  await nextTick()
  form.value?.querySelector<HTMLElement>('input:not([readonly]):not(:disabled)')?.focus({ preventScroll: true })
  root.value?.scrollIntoView({ block: 'nearest' })
})
</script>
<template>
  <li :id="kind ? `kind-${kind.slug}` : 'kind-new'" ref="root" class="kind-edit" tabindex="-1" @keydown="keys">
    <form ref="form" class="kf" autocomplete="off" :aria-label="kind ? `${t('edit')} ${kind.label}` : t('newKind')" @submit.prevent="save">
      <fieldset :disabled="busy || !editable">
        <label class="kf-f">{{ t('name') }}<input v-model="draft.label" class="field" name="label" maxlength="40" required :readonly="!!kind?.system" :placeholder="t('namePlaceholder')"></label>
        <label class="kf-f">{{ t('sentenceLabel') }}<small>{{ t('sentenceAsk') }}</small><input v-model="draft.hint" class="field" name="hint" maxlength="120" required :placeholder="t('sentencePlaceholder')"></label>
        <button type="button" class="kf-more" :aria-expanded="more" :aria-controls="moreId" @click="more = !more"><AppIcon name="chevron-right" :size="14" />{{ t('moreOptions') }} <span class="faint">{{ t('moreOptionsHint') }}</span></button>
        <div v-show="more" :id="moreId" class="kf-adv">
          <label class="kf-f">{{ t('examplesLabel') }}<small>{{ t('examplesUpTo') }}</small><textarea v-model="draft.examples" class="field" name="examples" rows="3" maxlength="362" /></label>
          <div class="kf-2">
            <label class="kf-f">{{ t('area') }}<small>{{ areaNote }}</small><input class="field" name="area" :value="kind?.slug ?? t('generatedArea')" readonly></label>
            <label class="kf-f">{{ t('labels') }}<small>{{ t('labelsHint') }}</small><input v-model="draft.labels" class="field" name="labels" maxlength="1598"></label>
          </div>
        </div>
      </fieldset>
      <div class="kf-acts"><button class="btn ghost" type="button" @click="emit('close')">{{ t('cancel') }}<KeyCap k="Esc" /></button><button class="btn primary" type="submit" :disabled="busy || !editable">{{ kind ? t('save') : t('create') }} <KeyCap k="mod" /><KeyCap k="enter" /></button></div>
      <p v-if="error" class="feedback" role="alert">{{ error }}</p>
    </form>
  </li>
</template>
<style scoped>
.kind-edit { margin: 8px 0; padding: 16px; border-radius: 12px; background: var(--surface-raised-2); box-shadow: inset 0 0 0 1px var(--chip-teal-line); scroll-margin: 20px 0; }
.kind-edit:focus { outline: none; }
.kf { display: grid; gap: 12px; min-width: 0; }
fieldset { display: grid; gap: 12px; min-width: 0; margin: 0; padding: 0; border: 0; }
.kf-f { display: grid; gap: 4px; min-width: 0; font-size: 12.5px; font-weight: 600; color: var(--ink-2); }
.kf-f small { font-weight: 400; color: var(--ink-3); }
.field { width: 100%; min-width: 0; }
textarea { resize: vertical; min-height: 72px; font: inherit; }
.kf-more { display: inline-flex; align-items: center; gap: 6px; justify-self: start; min-height: 32px; padding: 0 4px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); font: inherit; font-size: 12.5px; font-weight: 600; text-align: left; }
.kf-more:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.kf-more .faint { font-weight: 400; color: var(--ink-3); }
.kf-more svg { flex: none; transition: transform .15s ease; }
.kf-more[aria-expanded="true"] svg { transform: rotate(90deg); }
.kf-adv { display: grid; gap: 12px; padding: 12px; border-radius: 10px; background: var(--surface-sunken); }
.kf-2 { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.kf-acts { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
.feedback { margin: 0; font-size: 12px; color: var(--danger); overflow-wrap: anywhere; }
@container body (max-width: 640px) {
  .kf-2 { grid-template-columns: minmax(0, 1fr); }
  .kf-more .faint, .kf-acts :deep(.keycap) { display: none; }
}
@media (prefers-reduced-motion: reduce) { .kf-more svg { transition: none; } }
@media (pointer: coarse), (max-width: 720px) { button, .kf-more, .field:not(textarea) { min-height: 44px; } }
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
