<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import AppIcon from '../../AppIcon.vue'
import KeyCap from '../../KeyCap.vue'
import { settingsSubmitKey } from '../../../lib/settingsOverlays'
import { HARNESS_NAME, HARNESS_ORDER, routeLabel, routesFor, splitLevels, type DraftErrors, type Harness, type LineDraft, type RegistryLine } from '../../../lib/modelRegistry'

// The inline form of the Model registry: one for a new model, one under an existing
// entry. It never moves a control: hints and errors share one reserved line per
// field, and the form message and the remove question open below the buttons.
export interface RemoveQuestion { state: 'loading' | 'ready' | 'error'; text?: string; removing?: boolean }
const props = defineProps<{ mode: 'new' | 'manual' | 'auto'; line: RegistryLine | null; errors: DraftErrors; saving: boolean; formError: string; question: RemoveQuestion | null }>()
const draft = defineModel<LineDraft>('draft', { required: true })
const emit = defineEmits<{ save: []; cancel: []; askRemove: []; keep: []; remove: []; retry: []; clear: [field: keyof DraftErrors] }>()
const uid = useId()
const root = ref<HTMLFormElement | null>(null)
const routes = computed(() => routesFor(draft.value.harness))
const namespaced = computed(() => draft.value.harness === 'pi' || draft.value.harness === 'opencode')
const title = computed(() => props.line ? `Edit ${props.line.name}` : 'Add model')
const slugHint = computed(() => props.errors.slug || (props.mode === 'auto' ? 'From auto-discovery.' : namespaced.value && draft.value.route === 'openrouter' ? `Any OpenRouter slug, as ${HARNESS_NAME[draft.value.harness]} passes it on.` : ''))
const placeholder = computed(() => draft.value.harness === 'pi' && draft.value.route === 'openrouter' ? 'openrouter/qwen/qwen3-coder' : namespaced.value ? `${draft.value.route}/model-id` : 'model-id')

const patch = (change: Partial<LineDraft>) => { draft.value = { ...draft.value, ...change } }
const typed = (event: Event) => (event.target as HTMLInputElement | HTMLSelectElement).value
// pi and OpenCode ids start with their route; changing the route changes that first segment.
function swapPrefix(slug: string, from: string, to: string, harness: Harness) {
  return (harness === 'pi' || harness === 'opencode') && slug.toLowerCase().startsWith(`${from}/`) ? `${to}/${slug.slice(from.length + 1)}` : slug
}
function setHarness(harness: Harness) {
  const route = routesFor(harness)[0]!
  patch({ harness, route, slug: swapPrefix(draft.value.slug, draft.value.route, route, harness) })
}
function setRoute(route: string) { patch({ route, slug: swapPrefix(draft.value.slug, draft.value.route, route, draft.value.harness) }) }
function commitLevels() {
  if (!draft.value.level.trim()) return
  patch({ efforts: splitLevels(draft.value.level, draft.value.efforts), level: '' }); emit('clear', 'efforts')
}
function removeLevel(index: number) { patch({ efforts: draft.value.efforts.filter((_, at) => at !== index) }) }
function levelKey(event: KeyboardEvent) {
  if (event.isComposing) return
  if (event.key === 'Enter' && !event.metaKey && !event.ctrlKey) { event.preventDefault(); commitLevels() }
  else if (event.key === 'Backspace' && !draft.value.level && draft.value.efforts.length) removeLevel(draft.value.efforts.length - 1)
}
function onKey(event: KeyboardEvent) {
  if (settingsSubmitKey(event)) { event.preventDefault(); emit('save'); return }
  if (event.key !== 'Escape' || event.isComposing) return
  event.preventDefault(); event.stopPropagation()
  // Esc leaves a field first, then closes the remove question, then the form.
  if (event.target instanceof HTMLElement && event.target.closest('input, select, textarea')) { root.value?.focus(); return }
  if (props.question) { emit('keep'); return }
  emit('cancel')
}
function focusFor(found: DraftErrors) {
  root.value?.querySelector<HTMLElement>(found.name ? '[data-reg-name]' : found.slug ? '[data-reg-slug]' : '[data-reg-level]')?.focus()
}
defineExpose({ focusName: () => root.value?.querySelector<HTMLElement>('[data-reg-name]')?.focus(), focusFor })
</script>

<template>
  <form ref="root" class="reg-ed" tabindex="-1" novalidate :aria-label="title" data-reg-editor @submit.prevent @keydown="onKey">
    <label class="fl" :for="`${uid}-name`">Name</label>
    <div class="fv">
      <input :id="`${uid}-name`" class="field" data-reg-name maxlength="128" placeholder="e.g. Qwen3 Coder" autocomplete="off" :value="draft.name" :aria-invalid="!!errors.name" :aria-describedby="`${uid}-name-h`" @input="patch({ name: typed($event) }); emit('clear', 'name')">
      <p :id="`${uid}-name-h`" class="slot" :class="{ err: errors.name }">{{ errors.name }}</p>
    </div>

    <span :id="`${uid}-run`" class="fl">How it runs</span>
    <div class="fv">
      <div class="ed-run" role="group" :aria-labelledby="`${uid}-run`">
        <select class="field" aria-label="Harness" :value="draft.harness" :disabled="mode !== 'new'" @change="setHarness(typed($event) as Harness)"><option v-for="id in HARNESS_ORDER" :key="id" :value="id">{{ HARNESS_NAME[id] }}</option></select>
        <span class="via">via</span>
        <select class="field" aria-label="Route or provider" :value="draft.route" :disabled="mode === 'auto' || routes.length < 2" @change="setRoute(typed($event))"><option v-for="route in routes" :key="route" :value="route">{{ routeLabel(route) || route }}</option></select>
      </div>
      <p class="slot" />
    </div>

    <label class="fl" :for="`${uid}-slug`">Model slug</label>
    <div class="fv">
      <input :id="`${uid}-slug`" class="field mono" data-reg-slug maxlength="128" spellcheck="false" autocomplete="off" :placeholder="placeholder" :value="draft.slug" :disabled="mode === 'auto'" :aria-invalid="!!errors.slug" :aria-describedby="`${uid}-slug-h`" @input="patch({ slug: typed($event) }); emit('clear', 'slug')">
      <p :id="`${uid}-slug-h`" class="slot" :class="{ err: errors.slug }">{{ slugHint }}</p>
    </div>

    <span :id="`${uid}-lv`" class="fl">Thinking levels</span>
    <div class="fv">
      <div class="levels" role="group" :aria-labelledby="`${uid}-lv`">
        <div class="chip-scroll">
          <span v-for="(level, index) in draft.efforts" :key="level" class="chip">{{ level }}<button type="button" class="chip-x" :aria-label="`Remove level ${level}`" @click="removeLevel(index)"><AppIcon name="close" :size="11" /></button></span>
        </div>
        <input class="chip-in" data-reg-level placeholder="Add a level" aria-label="Add a thinking level (its own name)" spellcheck="false" autocomplete="off" :value="draft.level" :aria-invalid="!!errors.efforts" :aria-describedby="`${uid}-lv-h`" @input="patch({ level: typed($event) })" @keydown="levelKey" @blur="commitLevels">
      </div>
      <p :id="`${uid}-lv-h`" class="slot" :class="{ err: errors.efforts }">{{ errors.efforts || 'The model’s own names, lowest first, e.g. low · medium · high · xhigh.' }}</p>
    </div>

    <label class="fl" :for="`${uid}-note`">Note</label>
    <div class="fv">
      <input :id="`${uid}-note`" class="field" data-reg-note maxlength="80" placeholder="e.g. strongest for building" autocomplete="off" :value="draft.note" :aria-describedby="`${uid}-note-h`" @input="patch({ note: typed($event) })">
      <p :id="`${uid}-note-h`" class="slot">Shown under its name in every picker.</p>
    </div>

    <div class="ed-foot">
      <button v-if="line" type="button" class="btn sm ghost danger-t" data-reg-remove :aria-expanded="!!question" @click="emit('askRemove')"><AppIcon name="trash" :size="14" />Remove model</button>
      <span class="grow" />
      <button type="button" class="btn sm ghost" data-reg-cancel @click="emit('cancel')">Cancel <KeyCap k="Esc" /></button>
      <button type="button" class="btn sm primary" data-reg-save :aria-busy="saving" :aria-disabled="saving" @click="emit('save')">{{ line ? 'Save' : 'Add' }} <KeyCap k="mod" /><KeyCap k="enter" /></button>
    </div>
    <p v-if="formError" class="ed-msg" role="alert" data-reg-error><AppIcon name="alert" :size="14" />{{ formError }}</p>
    <div v-if="question" class="ed-confirm" data-reg-confirm :role="question.state === 'error' ? 'alert' : 'status'">
      <p v-if="question.state === 'loading'">Checking where {{ line?.name }} is used…</p>
      <p v-else-if="question.state === 'error'">It could not be checked where {{ line?.name }} is used, so it can’t be removed yet.</p>
      <p v-else>{{ question.text }}</p>
      <div class="ed-acts">
        <button type="button" class="btn sm ghost" data-reg-keep @click="emit('keep')">Keep it</button>
        <button v-if="question.state === 'error'" type="button" class="btn sm" data-reg-retry-remove @click="emit('retry')">Try again</button>
        <button type="button" class="btn sm danger" data-reg-confirm-remove :aria-disabled="question.state !== 'ready' || question.removing" @click="emit('remove')">Remove</button>
      </div>
    </div>
  </form>
</template>

<style scoped>
/* The form grows under its entry; labels left, fields aligned, one reserved line per field for hints and errors. */
.reg-ed { display: grid; grid-template-columns: 132px minmax(0, 1fr); align-items: start; gap: 4px 16px; margin: 4px 0 12px; padding: 18px 16px 14px; border-radius: 14px; background: var(--surface-raised); box-shadow: 0 0 0 1px var(--line-2), var(--shadow-btn); outline: none; }
.fl { padding-top: 9px; font-size: 13px; font-weight: 600; color: var(--ink); }
.fv { min-width: 0; }
.reg-ed .field { height: 38px; }
.reg-ed .field.mono { font: 500 12.5px/1 var(--mono); }
.reg-ed select.field { width: auto; min-width: 120px; padding-right: 28px; }
.ed-run { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.via { font-size: 13px; color: var(--ink-3); }
.slot { min-height: 20px; margin-top: 4px; font-size: 12px; line-height: 1.45; color: var(--ink-3); }
.slot.err { color: var(--danger); }
/* One field-height row. Chips scroll sideways; the input keeps its own column, so adding or removing a level cannot move it, the note, or the actions. */
.levels { display: grid; grid-template-columns: minmax(0, 1fr) 190px; align-items: center; height: 38px; padding: 0 6px; border-radius: var(--radius-s); background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); overflow: hidden; }
.levels:focus-within { box-shadow: var(--field-inset), 0 0 0 1px var(--teal); }
.chip-scroll { display: flex; flex-wrap: nowrap; align-items: center; gap: 6px; min-width: 0; height: 38px; overflow-x: auto; overflow-y: hidden; scrollbar-width: none; }
.chip-scroll::-webkit-scrollbar { display: none; }
.chip { display: inline-flex; flex: none; align-items: center; gap: 2px; height: 28px; padding: 0 2px 0 10px; border-radius: 999px; background: var(--surface-sunken); font: 500 12px/1 var(--mono); color: var(--ink); white-space: nowrap; }
.chip-x { display: grid; place-items: center; width: 24px; height: 24px; padding: 0; border: 0; border-radius: 999px; background: transparent; color: var(--ink-3); }
.chip-x:hover { background: var(--row-hover); color: var(--ink); }
.chip-in { box-sizing: border-box; width: 100%; min-width: 0; height: 28px; padding: 0 6px; border: 0; background: transparent; color: var(--ink); font: 500 12.5px/1 var(--mono); }
.chip-in:focus-visible { outline: none; }
.ed-foot, .ed-confirm, .ed-msg { grid-column: 1 / -1; }
.ed-foot { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: 4px; padding-top: 12px; border-top: 1px solid var(--line); }
.grow { flex: 1 1 auto; }
.ed-foot [aria-disabled="true"] { cursor: default; }
.btn.ghost.danger-t { color: var(--danger); }
.ed-msg { display: flex; align-items: flex-start; gap: 8px; margin-top: 8px; font-size: 13px; line-height: 1.5; color: var(--danger); }
.ed-msg :deep(svg) { flex: none; margin-top: 3px; }
.ed-confirm { display: flex; flex-wrap: wrap; align-items: flex-start; justify-content: flex-end; gap: 8px 16px; margin-top: 8px; padding-top: 12px; border-top: 1px solid var(--line); }
.ed-confirm p { flex: 1 1 320px; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.ed-acts { display: flex; gap: 8px; }
.ed-acts [aria-disabled="true"] { opacity: .55; cursor: default; }
@container (max-width: 600px) {
  .reg-ed { grid-template-columns: minmax(0, 1fr); gap: 2px; padding: 14px 12px 12px; }
  .fl { padding-top: 6px; }
  .ed-run select.field { flex: 1 1 150px; min-width: 0; }
}
@media (pointer: coarse) { .chip-x { width: 32px; height: 32px; } }
</style>
<style scoped src="../../../styles/settingsButtons.css"></style>
