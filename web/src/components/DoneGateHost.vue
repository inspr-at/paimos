<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, reactive, ref, useId, watch } from 'vue'
import { doneGateState, settleDoneGate } from '../lib/doneGateAsk'
import { gateAction, gateProgress, gateTitle } from '../lib/doneGate'
import { benefitDraft, benefitIssues, pillWords } from '../lib/ticketBenefits'
import AppIcon from './AppIcon.vue'
import { vClipTip } from '../directives/clipTip'

const dialog = ref<HTMLDialogElement>()
const titleId = useId()
const tried = ref(false)
const draft = reactive({ pill_en: '', pill_de: '', benefit_en: '', benefit_de: '', hide_from_release_notes: false })
const fieldEls: Record<string, HTMLElement | null> = {}
const languages = [
  { key: 'en', label: 'English', pill: 'pill_en', benefit: 'benefit_en', pillHint: 'e.g. Hosts in minutes', benefitHint: 'What people gain, in one or two sentences.' },
  { key: 'de', label: 'Deutsch', pill: 'pill_de', benefit: 'benefit_de', pillHint: 'z. B. Hosts in Minuten', benefitHint: 'Was Menschen gewinnen, ohne direkte Anrede.' },
] as const
const order = ['pill_en', 'pill_de', 'benefit_en', 'benefit_de'] as const

const request = computed(() => doneGateState.request)
const title = computed(() => request.value ? gateTitle(request.value.state) : '')
const action = computed(() => request.value ? gateAction(request.value.state) : '')
const progress = computed(() => request.value ? gateProgress(request.value.index, request.value.total) : null)

function bindField(key: string, el: unknown) {
  fieldEls[key] = el instanceof HTMLElement ? el : null
}
function load(fields: Record<string, unknown>) {
  const next = benefitDraft(fields)
  draft.pill_en = next.pill_en
  draft.pill_de = next.pill_de
  draft.benefit_en = next.benefit_en
  draft.benefit_de = next.benefit_de
  draft.hide_from_release_notes = next.hide_from_release_notes
  tried.value = false
}
function countLabel(value: string) {
  const count = pillWords(value)
  if (!count) return '2–4 words'
  return `${count} ${count === 1 ? 'word' : 'words'} of 2–4`
}
function countOff(value: string) {
  const count = pillWords(value)
  return count === 1 || count > 4
}
function invalid(key: typeof order[number]) {
  return benefitIssues(draft).some(issue => issue.startsWith(key))
}
function fieldIssue(key: typeof order[number]) {
  const value = draft[key].trim()
  if (!value) return 'Required'
  if (key.startsWith('pill_') && (pillWords(value) < 2 || pillWords(value) > 4)) return '2–4 words'
  return ''
}
function describedBy(key: typeof order[number]) {
  const ids: string[] = []
  if (key.startsWith('pill_')) ids.push(`${titleId}-${key}-count`)
  if (tried.value && fieldIssue(key)) ids.push(`${titleId}-${key}-error`)
  return ids.join(' ') || undefined
}
function focusFirst() {
  const key = order.find(item => invalid(item)) ?? 'pill_en'
  fieldEls[key]?.focus()
}
function submit() {
  if (benefitIssues(draft).length) {
    tried.value = true
    focusFirst()
    return
  }
  settleDoneGate({
    pill_en: draft.pill_en,
    pill_de: draft.pill_de,
    benefit_en: draft.benefit_en,
    benefit_de: draft.benefit_de,
    hide_from_release_notes: draft.hide_from_release_notes,
  })
}
function onFormKey(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
    event.preventDefault()
    event.stopPropagation()
    submit()
  }
}
function backdrop(event: MouseEvent) {
  if (event.target === dialog.value) settleDoneGate(null)
}
watch(() => doneGateState.request, async (next) => {
  if (!next) {
    if (dialog.value?.open) dialog.value.close()
    return
  }
  load(next.fields)
  if (!dialog.value?.open) dialog.value?.showModal()
  await nextTick()
  focusFirst()
})
</script>

<template>
  <dialog ref="dialog" class="gate" :aria-labelledby="titleId" @cancel.prevent="settleDoneGate(null)" @click="backdrop">
    <form v-if="request" class="card" @submit.prevent="submit" @keydown="onFormKey">
      <span class="grabber" aria-hidden="true" />
      <div class="head">
        <span class="mark" aria-hidden="true"><AppIcon name="sparkle" :size="16" /></span>
        <div class="head-copy">
          <h2 :id="titleId">{{ title }}</h2>
          <p class="quiet">
            <span class="id">{{ request.key }}</span>
            <span v-clip-tip="request.title" class="name" tabindex="0">{{ request.title }}</span>
            <span v-if="progress" class="step">{{ progress }}</span>
          </p>
        </div>
      </div>
      <div class="actions">
        <button type="button" class="btn" @click="settleDoneGate(null)">Not now</button>
        <button type="submit" class="btn primary">{{ action }}</button>
      </div>
      <div class="scroll">
        <div class="languages">
          <div v-for="language in languages" :key="language.key" class="language" :lang="language.key">
            <div class="label-row">
              <label :for="`${titleId}-${language.pill}`">Pill · {{ language.label }}</label>
              <span :id="`${titleId}-${language.pill}-count`" class="count" :class="{ off: countOff(draft[language.pill]) }">{{ countLabel(draft[language.pill]) }}</span>
            </div>
            <input
              :id="`${titleId}-${language.pill}`"
              :ref="(el) => bindField(language.pill, el)"
              v-model="draft[language.pill]"
              class="field"
              :placeholder="language.pillHint"
              :aria-describedby="describedBy(language.pill)"
              :aria-invalid="tried && invalid(language.pill) ? 'true' : undefined"
              autocomplete="off"
            />
            <p v-if="tried && fieldIssue(language.pill)" :id="`${titleId}-${language.pill}-error`" class="field-error">{{ fieldIssue(language.pill) }}</p>
            <label :for="`${titleId}-${language.benefit}`">Benefit · {{ language.label }}</label>
            <textarea
              :id="`${titleId}-${language.benefit}`"
              :ref="(el) => bindField(language.benefit, el)"
              v-model="draft[language.benefit]"
              class="field"
              rows="3"
              :placeholder="language.benefitHint"
              :aria-describedby="describedBy(language.benefit)"
              :aria-invalid="tried && invalid(language.benefit) ? 'true' : undefined"
            />
            <p v-if="tried && fieldIssue(language.benefit)" :id="`${titleId}-${language.benefit}-error`" class="field-error">{{ fieldIssue(language.benefit) }}</p>
          </div>
        </div>
        <label class="hide" data-tip="Hidden tickets still need both languages.">
          <input v-model="draft.hide_from_release_notes" type="checkbox" />
          Hide from release notes
        </label>
      </div>
    </form>
  </dialog>
</template>

<style scoped>
.gate {
  position: fixed;
  inset: 96px 0 auto;
  margin: 0 auto;
  width: min(680px, calc(100vw - 48px));
  max-height: calc(100dvh - 112px);
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--ink);
  overflow: visible;
}
.gate::backdrop { background: var(--scrim); backdrop-filter: blur(2px); }
.card {
  display: flex;
  flex-direction: column;
  box-sizing: border-box;
  max-height: min(680px, calc(100dvh - 112px));
  padding: 22px 24px 18px;
  border-radius: var(--radius);
  border: 1px solid var(--glass-edge);
  background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2));
  box-shadow: var(--shadow-pop), var(--shadow);
}
.grabber { display: none; }
.head { flex: none; display: flex; gap: 12px; align-items: flex-start; min-width: 0; }
.head-copy { min-width: 0; flex: 1; }
.mark {
  display: grid; place-items: center; flex: none; width: 32px; height: 32px; border-radius: 999px;
  box-shadow: inset 0 0 0 1px var(--glass-edge); color: var(--ink-2);
}
h2 { margin: 0; font-size: 18px; line-height: 1.3; text-wrap: balance; }
.quiet { display: flex; gap: 8px; align-items: baseline; min-width: 0; margin: 4px 0 0; color: var(--ink-3); font-size: 12.5px; }
.quiet .id { flex: none; font-family: var(--mono); letter-spacing: .02em; color: var(--ink-2); font-variant-ligatures: none; }
/* Keep actions still between series items; clipped titles remain focusable. */
.quiet .name { flex: 1; min-width: 0; height: 2lh; line-height: 1.4; overflow: hidden; white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
.quiet .step { flex: none; font-variant-numeric: tabular-nums; }
@media (pointer: coarse) { .quiet .name { line-height: max(1.4em, 22px); } }
.scroll { flex: 1; min-height: 0; margin-top: 16px; overflow: auto; overscroll-behavior: contain; }
.languages { display: grid; gap: 14px; }
.language { display: grid; gap: 6px; min-width: 0; align-content: start; }
.label-row { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; }
label { font-size: 12px; color: var(--ink-2); }
.count { font: 500 11px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; font-variant-ligatures: none; }
.count.off { color: var(--warn); }
.field { width: 100%; min-width: 0; }
textarea.field { height: auto; min-height: 72px; padding-top: 8px; padding-bottom: 8px; resize: vertical; line-height: 1.45; }
.field[aria-invalid="true"] { box-shadow: 0 0 0 1px var(--warn); }
.field-error { margin: 0; font-size: 12px; line-height: 1.35; color: var(--warn); }
.hide { display: inline-flex; align-items: center; gap: 8px; justify-self: start; margin-top: 2px; cursor: pointer; }
.hide input { width: 16px; height: 16px; margin: 0; }
.actions {
  flex: none; display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px;
}
@media (min-width: 701px) {
  .languages { grid-template-columns: 1fr 1fr; gap: 16px; }
}
@media (max-width: 700px) {
  .gate {
    width: 100%;
    max-width: 100%;
    height: 100dvh;
    max-height: none;
    margin: auto 0 0;
    inset: auto 0 0 0;
  }
  .card {
    height: 100%;
    max-height: none;
    padding: 0 0 calc(12px + env(safe-area-inset-bottom));
    border: 0;
    border-top: 1px solid var(--glass-edge);
    border-radius: 20px 20px 0 0;
    background: var(--surface-raised);
    box-shadow: 0 -18px 40px -18px rgba(0, 0, 0, .35);
  }
  .grabber { display: block; align-self: center; width: 40px; height: 4px; margin: 8px auto 0; border-radius: 999px; background: var(--line-2); }
  .head { padding: 12px 16px 0; }
  h2 { font-size: 17px; }
  .scroll { order: 1; padding: 0 16px; }
  .field { height: auto; min-height: 44px; font-size: 16px; }
  textarea.field { min-height: 88px; font-size: 16px; }
  .hide { min-height: 44px; font-size: 15px; }
  .actions { order: 2; margin: 12px 0 0; padding: 12px 16px 0; border-top: 1px solid var(--line); background: var(--surface-raised); }
  .actions .btn { height: 44px; flex: 1; }
}
@media (max-width: 700px) and (prefers-reduced-motion: no-preference) {
  .gate[open] .card { animation: sheet-up .24s cubic-bezier(.2, .7, .2, 1); }
  @keyframes sheet-up { from { transform: translateY(40px); opacity: .6; } to { transform: none; opacity: 1; } }
}
</style>
