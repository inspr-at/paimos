<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { MergePreview, ConflictChoices } from '../../../lib/quoteMerge'
import { conflictPlace, conflictValue } from '../../../lib/quotes/conflicts'
import type { QuoteDocumentData } from '../../../lib/quotes/types'
import AppIcon from '../../AppIcon.vue'
import QuoteIcon from '../inspector/QuoteIcon.vue'

// Reviewing a save that met someone else's newer one. Both copies are kept
// until you choose: changes in different places merge by themselves; where you
// both changed the same thing, each overlap asks which version stays. Nothing is
// saved until "Save the result". Your copy can also be downloaded first.
const props = defineProps<{ open: boolean; preview: MergePreview | null; durableRecovery: boolean; document: QuoteDocumentData | null; actorName?: string }>()
const emit = defineEmits<{ resolve: [choices: ConflictChoices]; export: []; discard: []; close: [] }>()
const dialog = ref<HTMLDialogElement>()
const choices = ref<ConflictChoices>({})
const conflicts = computed(() => props.preview?.conflicts ?? [])
const decided = computed(() => conflicts.value.filter(c => !!choices.value[c.path]).length)
const ready = computed(() => !!props.preview?.document && conflicts.value.every(c => !!choices.value[c.path]))
const other = computed(() => props.actorName || 'Someone else')
let opener: HTMLElement | null = null
watch(() => props.open, async open => {
  if (open) {
    choices.value = {}
    opener = document.activeElement as HTMLElement
    await nextTick()
    if (!dialog.value?.open) dialog.value?.showModal()
    dialog.value?.querySelector<HTMLElement>('[data-autofocus]')?.focus()
  } else if (dialog.value?.open) { dialog.value.close(); opener?.focus({ preventScroll: true }) }
}, { immediate: true })
function all(side: 'mine' | 'theirs') { choices.value = Object.fromEntries(conflicts.value.map(c => [c.path, side])) }
</script>

<template>
  <dialog ref="dialog" class="review" aria-labelledby="quote-conflict-title" aria-describedby="quote-conflict-lead" @cancel.prevent="emit('close')">
    <div class="card">
      <header class="head">
        <span class="head-icon" aria-hidden="true"><QuoteIcon name="history" :size="17" /></span>
        <div class="titles">
          <h2 id="quote-conflict-title">{{ conflicts.length ? 'Choose which changes stay' : 'Merge the newer draft' }}</h2>
          <p id="quote-conflict-lead" class="lead">
            <template v-if="conflicts.length">{{ other }} saved changes while you were editing. Most merge by themselves; {{ conflicts.length === 1 ? 'one place was' : `${conflicts.length} places were` }} changed by both of you.</template>
            <template v-else>{{ other }} saved changes while you were editing. They are in other places than yours, so both can be kept.</template>
          </p>
        </div>
        <button type="button" class="icon-btn sm flat" aria-label="Keep editing" data-tip="Keep editing · Esc" @click="emit('close')"><AppIcon name="close" :size="15" /></button>
      </header>

      <p v-if="!durableRecovery" class="warn" role="alert"><AppIcon name="alert" :size="14" /><span>This browser cannot keep a copy of your work. Download it before you close this tab.</span></p>

      <div v-if="conflicts.length" class="bulk" role="group" aria-label="Choose for every overlap">
        <span class="progress">{{ decided }} of {{ conflicts.length }} chosen</span>
        <button type="button" class="btn sm ghost" @click="all('mine')">Keep all of mine</button>
        <button type="button" class="btn sm ghost" @click="all('theirs')">Take all of theirs</button>
      </div>
      <ol v-if="conflicts.length" class="conflicts">
        <li v-for="(conflict, index) in conflicts" :key="conflict.path" class="conflict">
          <fieldset>
            <legend class="place">{{ conflictPlace(conflict.path, document) }}</legend>
            <div class="sides">
              <label class="side" :class="{ on: choices[conflict.path] === 'mine' }">
                <input v-model="choices[conflict.path]" class="radio" type="radio" :name="`conflict-${index}`" value="mine" :data-autofocus="index === 0 ? '' : undefined" />
                <span class="side-text"><span class="side-name">Mine</span><span class="value">{{ conflictValue(conflict.mine, conflict.path) }}</span></span>
              </label>
              <label class="side" :class="{ on: choices[conflict.path] === 'theirs' }">
                <input v-model="choices[conflict.path]" class="radio" type="radio" :name="`conflict-${index}`" value="theirs" />
                <span class="side-text"><span class="side-name">{{ actorName ? `${actorName}’s` : 'Theirs' }}</span><span class="value">{{ conflictValue(conflict.theirs, conflict.path) }}</span></span>
              </label>
            </div>
          </fieldset>
        </li>
      </ol>
      <p v-else-if="preview && !preview.document" class="warn" role="alert"><AppIcon name="alert" :size="14" /><span>These two copies cannot be merged automatically. Download yours, then reload the newer draft.</span></p>

      <footer class="foot">
        <button type="button" class="btn ghost" @click="emit('export')"><AppIcon name="download" :size="14" />Download my copy</button>
        <span class="spacer" />
        <button type="button" class="btn danger-ghost" @click="emit('discard')">Discard mine and reload</button>
        <button type="button" class="btn primary" :disabled="!ready" :data-autofocus="conflicts.length ? undefined : ''" @click="emit('resolve', choices)">{{ conflicts.length ? 'Save the result' : 'Merge and save' }}</button>
      </footer>
    </div>
  </dialog>
</template>

<style scoped>
.review { width: min(var(--dialog-l), calc(100vw - 24px)); max-height: calc(100dvh - 24px); padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.review::backdrop { background: var(--scrim); backdrop-filter: blur(2px); }
.card { display: grid; gap: 14px; max-height: calc(100dvh - 24px); overflow: auto; padding: 20px 22px 18px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); }
.head { display: flex; align-items: flex-start; gap: 12px; }
.head-icon { display: grid; place-items: center; flex-shrink: 0; width: 36px; height: 36px; border-radius: 50%; background: var(--gold-wash); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--warn-ink); }
.titles { flex: 1; min-width: 0; }
h2 { font-size: 18px; }
.lead { margin-top: 4px; font-size: 13.5px; line-height: 1.5; color: var(--ink-2); }
.warn { display: flex; align-items: flex-start; gap: 8px; padding: 10px 12px; border-radius: 10px; background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); font-size: 13px; color: var(--ink); }
.warn svg { flex-shrink: 0; margin-top: 2px; color: var(--danger); }
.bulk { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.progress { flex: 1; font-size: 12.5px; color: var(--ink-2); font-variant-numeric: tabular-nums; }
.conflicts { display: grid; gap: 10px; max-height: 46vh; overflow: auto; margin: 0; padding: 0; list-style: none; }
fieldset { margin: 0; padding: 0; border: 0; min-width: 0; }
.place { padding: 0 0 6px; font-size: 13px; font-weight: 650; color: var(--ink); }
.sides { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.side { display: flex; align-items: flex-start; gap: 10px; min-width: 0; padding: 10px 12px; border-radius: 10px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); cursor: pointer; }
@media (hover: hover) { .side:hover { box-shadow: inset 0 0 0 1px var(--line-2); } }
.side.on { background: var(--row-selected); box-shadow: inset 0 0 0 1.5px var(--teal); }
.side:focus-within { box-shadow: inset 0 0 0 1.5px var(--teal), var(--focus-ring); }
.radio { flex-shrink: 0; width: 15px; height: 15px; margin: 2px 0 0; accent-color: var(--teal); }
.side-text { display: grid; gap: 2px; min-width: 0; }
.side-name { font-size: 12px; font-weight: 650; color: var(--ink-2); }
.value { font-size: 13px; line-height: 1.45; color: var(--ink); overflow-wrap: anywhere; }
.foot { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.spacer { flex: 1; }
.danger-ghost { color: var(--danger); }
@media (max-width: 600px) {
  .card { padding: 16px; }
  .sides { grid-template-columns: 1fr; }
  .foot .btn { flex: 1 1 100%; height: 44px; justify-content: center; }
  .spacer { display: none; }
}
@media print { .review { display: none; } }
</style>
