<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, useId } from 'vue'
import { TLDR_MAX, validateTLDR, type RuleTLDR } from '../../lib/rules'

// Inline editor for one explanation (AEON-314): English, optional German. It
// writes the draft only; the explanation goes live with the normal publish.
// When the explained text changed since it was written, the unchanged words
// can be confirmed with "Still fits".
const props = defineProps<{
  value: RuleTLDR | null | undefined
  /** Saves and answers an error message, or null when saved. null removes the explanation. */
  save: (value: { en: string; de?: string } | null) => Promise<string | null>
}>()
const emit = defineEmits<{ done: [] }>()
const id = useId()
const en = ref(props.value?.en ?? '')
const de = ref(props.value?.de ?? '')
const busy = ref(false)
const error = ref('')
const input = ref<HTMLInputElement>()
onMounted(() => input.value?.focus({ preventScroll: true }))

const changed = computed(() => en.value.trim() !== (props.value?.en ?? '') || de.value.trim() !== (props.value?.de ?? ''))
const confirming = computed(() => !!props.value?.check && !changed.value)
const label = computed(() => busy.value ? 'Saving…' : confirming.value ? 'Still fits' : 'Save draft')

async function run(value: { en: string; de?: string } | null) {
  if (busy.value) return
  if (value) {
    const issue = validateTLDR(value.en, true) ?? validateTLDR(value.de ?? '', false)
    if (issue) { error.value = issue; return }
  }
  busy.value = true
  error.value = ''
  try {
    const failure = await props.save(value)
    if (failure) error.value = failure
    else emit('done')
  } finally { busy.value = false }
}
function submit() {
  if (!changed.value && !confirming.value) { emit('done'); return }
  const trimmed = de.value.trim()
  void run(trimmed ? { en: en.value.trim(), de: trimmed } : { en: en.value.trim() })
}
</script>

<template>
  <form class="tldr-edit" :aria-labelledby="`${id}-en-label`" @submit.prevent="submit" @keydown.esc.stop.prevent="emit('done')">
    <label class="fld">
      <span :id="`${id}-en-label`">Explanation <span class="opt">for people; agents never receive it</span></span>
      <input ref="input" v-model="en" class="field" :maxlength="TLDR_MAX" placeholder="What it does and why, in one short line" autocomplete="off">
    </label>
    <label class="fld">
      <span>German <span class="opt">optional</span></span>
      <input v-model="de" class="field" :maxlength="TLDR_MAX" lang="de" autocomplete="off">
    </label>
    <p v-if="confirming" class="note">The rule text changed after this was written. Check it still fits.</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div class="buttons">
      <button v-if="value" type="button" class="btn sm ghost remove" :disabled="busy" @click="run(null)">Remove</button>
      <button type="button" class="btn sm ghost" :disabled="busy" @click="emit('done')">Cancel</button>
      <button type="submit" class="btn sm primary" :disabled="busy || (!changed && !confirming) || !en.trim()">{{ label }}</button>
    </div>
  </form>
</template>

<style scoped>
.tldr-edit { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 8px 10px; }
.fld { display: grid; gap: 4px; min-width: 0; color: var(--ink-2); font-size: 12px; font-weight: 650; }
.fld .field { height: 34px; font-weight: 450; color: var(--ink); }
.opt { color: var(--ink-3); font-weight: 450; }
.note, .error { grid-column: 1 / -1; margin: 0; font-size: 12.5px; line-height: 1.4; }
.note { color: var(--ink-2); }
.error { color: var(--danger); }
.buttons { grid-column: 1 / -1; display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 6px; }
.buttons .remove { margin-right: auto; }
@media (max-width: 600px) { .tldr-edit { grid-template-columns: minmax(0, 1fr); } }
</style>
