<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '../../AppIcon.vue'
import HarnessMark from '../../agents/HarnessMark.vue'
import {
  defaultTail, endText, entryFor, fallbackSentence, lineFallback, lineFamily, routeNote, showsEffort, stepText,
  type ChainEnd, type ModelEntry, type RowView, type SimpleNever, type SimpleStep, type StepState,
} from '../../../lib/modelsSimple'

// A row's fallback (AEON-1161): one quiet sentence under the pick that opens the order in place. The fold grows
// downward only, so nothing above it moves (AEON-541). Steps reorder by dragging the handle, or with ↑ ↓ on a focused
// handle; each step keeps its own level; the order ends in Wait or the default's fallback. A lock makes it read-only.
const props = defineProps<{
  row: RowView; base: RowView | undefined; entries: ModelEntry[]; routes: Map<string, string[]>; states: Map<string, StepState>
  open: boolean; everyone: boolean; picking: number | 'add' | null
  allow: (never: SimpleNever) => boolean; who: (never: SimpleNever) => string
}>()
const emit = defineEmits<{ toggle: []; move: [from: number, to: number, keyboard: boolean]; remove: [index: number]; pick: [index: number | null, anchor: HTMLElement]; end: [end: ChainEnd]; allow: [never: SimpleNever] }>()

const editable = computed(() => props.row.editable && !props.row.draft)
const name = computed(() => props.row.isDefault ? 'the default' : props.row.label)
const kind = computed(() => props.row.isDefault ? 'all work' : props.row.label)
const sentence = computed(() => fallbackSentence(props.row, props.entries))
const sentenceText = computed(() => `If ${sentence.value.pick} can’t run → ${sentence.value.steps.join(' → ')}`)
const sentenceLabel = computed(() => `Fallback for ${name.value}: if ${sentence.value.pick} can’t run, then ${sentence.value.steps.join(', then ')}`)
const tail = computed(() => defaultTail(props.base, props.entries))
const first = computed<SimpleStep>(() => ({ line: props.row.line ?? '', effort: props.row.effort }))
const count = computed(() => props.row.chain.length + 1)
const several = computed(() => [props.row.line, ...props.row.chain.map(step => step.line)].some(line => (props.routes.get(line ?? '')?.length ?? 0) > 1))

const entry = (step: SimpleStep) => entryFor(props.entries, step.line)
const effortOf = (step: SimpleStep) => showsEffort(entry(step)) ? step.effort : null
const nameOf = (step: SimpleStep) => entry(step)?.name ?? lineFallback(step.line)
const markOf = (step: SimpleStep) => entry(step)?.harness ?? ''
const familyOf = (step: SimpleStep) => entry(step) ? '' : lineFamily(step.line)
const noteOf = (step: SimpleStep, lead?: string | null) => routeNote(props.routes.get(step.line), lead)
const because = (never: SimpleNever) => { const why = never.why.trim().replace(/[.\s]+$/, ''); return why ? `${why}. ` : '' }

// ----- the fold: mounted while open or closing; it opens by height only -----------------------------------------
const calm = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
const fold = ref<HTMLElement>(), mounted = ref(props.open), expanded = ref(props.open)
let timer: ReturnType<typeof setTimeout> | undefined
watch(() => props.open, async open => {
  clearTimeout(timer)
  if (open) {
    mounted.value = true
    await nextTick()
    void fold.value?.offsetHeight
    if (props.open) expanded.value = true
    return
  }
  expanded.value = false
  timer = setTimeout(() => { if (!props.open) mounted.value = false }, calm() ? 0 : 260)
})
onBeforeUnmount(() => clearTimeout(timer))

// ----- reorder: the handle drags (mouse, pen and touch alike); ↑ ↓ on a focused handle -----------------------
const drag = ref<{ from: number; over: number; pointer: number } | null>(null)
const list = ref<HTMLElement>()
function grab(event: PointerEvent, index: number) {
  if (!editable.value || event.button !== 0) return
  drag.value = { from: index, over: index, pointer: event.pointerId }
  ;(event.currentTarget as HTMLElement).setPointerCapture?.(event.pointerId)
}
function drift(event: PointerEvent) {
  const state = drag.value
  if (!state || event.pointerId !== state.pointer) return
  event.preventDefault()
  const items = [...(list.value?.querySelectorAll<HTMLElement>('.ch-it[data-i]') ?? [])]
  let over = state.over
  for (const item of items) {
    const box = item.getBoundingClientRect()
    if (event.clientY >= box.top && event.clientY <= box.bottom) over = Number(item.dataset.i)
  }
  if (items.length) {
    if (event.clientY < items[0]!.getBoundingClientRect().top) over = 0
    if (event.clientY > items.at(-1)!.getBoundingClientRect().bottom) over = items.length - 1
  }
  if (over !== state.over) drag.value = { ...state, over }
}
function drop(event: PointerEvent) {
  const state = drag.value
  if (!state || event.pointerId !== state.pointer) return
  drag.value = null
  if (state.over !== state.from) emit('move', state.from, state.over, false)
}
function cancel() { drag.value = null }
function stepKeys(event: KeyboardEvent, index: number, handle: boolean) {
  if (!editable.value || event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return
  // Arrows move a step from its handle; anywhere else in the step, Alt with an arrow does.
  if (!handle && !event.altKey) return
  event.preventDefault()
  const to = index + (event.key === 'ArrowUp' ? -1 : 1)
  if (to >= 0 && to < props.row.chain.length) emit('move', index, to, true)
}
function endKeys(event: KeyboardEvent) {
  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return
  event.preventDefault()
  const next: ChainEnd = props.row.end === 'default' ? 'wait' : 'default'
  emit('end', next)
  void nextTick(() => (event.currentTarget as HTMLElement | null)?.querySelector<HTMLElement>(`[data-end="${props.row.key}:${next}"]`)?.focus())
}
</script>
<template>
  <div class="fb">
    <button type="button" class="fb-sum" :data-fold="row.key" :aria-expanded="open" :aria-controls="`fb-${row.key}`" :aria-label="sentenceLabel" @click="emit('toggle')">
      <AppIcon class="fb-chev" name="chevron-right" :size="13" />
      <span v-clip-tip="sentenceText" class="fb-s">If {{ sentence.pick }} can’t run<template v-for="(part, index) in sentence.steps" :key="index"><AppIcon class="ar" name="arrow" :size="12" /><span class="fb-p">{{ part }}</span></template></span>
    </button>
    <div :id="`fb-${row.key}`" ref="fold" class="fold" :class="{ open: expanded }" :role="mounted ? 'region' : undefined" :aria-label="mounted ? `Fallback for ${name}` : undefined">
      <div class="fold-in">
        <div v-if="mounted" class="ch-wrap">
          <ol ref="list" class="ch" :data-chain="row.key" :aria-label="`Order for ${name}`" @pointermove="drift" @pointerup="drop" @pointercancel="cancel" @lostpointercapture="cancel">
            <li class="ch-it first">
              <span class="grip-sp" aria-hidden="true" />
              <span class="ch-n" aria-hidden="true">1</span>
              <span class="ch-pick ro"><span class="hg" aria-hidden="true"><HarnessMark :harness="markOf(first)" :provider="familyOf(first)" :size="12" /></span><span class="pn">{{ nameOf(first) }}<span v-if="effortOf(first)" class="pe"> · {{ effortOf(first) }}</span></span></span>
              <span class="ch-d">{{ noteOf(first, row.entry ? row.entry.harness : null) }}</span>
              <span v-if="states.get(first.line)" class="ch-st" :class="{ ok: states.get(first.line)!.runs }"><AppIcon :name="states.get(first.line)!.runs ? 'play' : 'alert'" :size="12" />{{ states.get(first.line)!.text }}</span>
            </li>
            <li
              v-for="(step, index) in row.chain" :key="step.line" class="ch-it" :data-i="index"
              :class="{ dragging: drag?.from === index, over: drag && drag.over === index && drag.from !== index }"
              @keydown="stepKeys($event, index, false)"
            >
              <button
                v-if="editable" type="button" class="grip" :data-move="`${row.key}:${index}`"
                :aria-label="`Move ${nameOf(step)}, step ${index + 2} of ${count}. Use the arrow keys`" data-tip="Drag, or press ↑ ↓"
                @pointerdown="grab($event, index)" @keydown.stop="stepKeys($event, index, true)"
              ><AppIcon name="grip" :size="14" /></button>
              <span v-else class="grip-sp" aria-hidden="true" />
              <span class="ch-n" aria-hidden="true">{{ index + 2 }}</span>
              <button
                v-if="editable" :id="`chp-${row.key}-${index}`" type="button" class="ch-pick" :data-chpick="`${row.key}:${index}`"
                aria-haspopup="listbox" :aria-expanded="picking === index" :aria-label="`Step ${index + 2}: ${stepText(entries, step)}. Change`"
                @click="emit('pick', index, $event.currentTarget as HTMLElement)"
              ><span class="hg" aria-hidden="true"><HarnessMark :harness="markOf(step)" :provider="familyOf(step)" :size="12" /></span><span class="pn">{{ nameOf(step) }}<span v-if="effortOf(step)" class="pe"> · {{ effortOf(step) }}</span></span></button>
              <span v-else class="ch-pick ro"><span class="hg" aria-hidden="true"><HarnessMark :harness="markOf(step)" :provider="familyOf(step)" :size="12" /></span><span class="pn">{{ nameOf(step) }}<span v-if="effortOf(step)" class="pe"> · {{ effortOf(step) }}</span></span></span>
              <span class="ch-d">{{ noteOf(step) }}</span>
              <span v-if="states.get(step.line)" class="ch-st" :class="{ ok: states.get(step.line)!.runs }"><AppIcon :name="states.get(step.line)!.runs ? 'play' : 'alert'" :size="12" />{{ states.get(step.line)!.text }}</span>
              <button v-if="editable" type="button" class="icon-btn flat x sm" :data-chrm="`${row.key}:${index}`" :aria-label="`Take ${nameOf(step)} out of the fallback`" data-tip="Take it out" @click="emit('remove', index)"><AppIcon name="close" :size="13" /></button>
            </li>
          </ol>
          <div v-if="editable" class="ch-add"><button :id="`chadd-${row.key}`" type="button" class="add sm" :data-chadd="row.key" aria-haspopup="listbox" :aria-expanded="picking === 'add'" @click="emit('pick', null, $event.currentTarget as HTMLElement)"><AppIcon name="plus" :size="14" /><span>Add a fallback</span></button></div>
          <div class="ch-end">
            <AppIcon name="wait" :size="15" />
            <span v-if="row.isDefault" class="ch-end-d solo"><b>Then wait.</b> Nothing outside this order runs.</span>
            <template v-else>
              <span :id="`che-${row.key}`" class="ch-end-l">Then</span>
              <span v-if="editable" class="seg sm" role="radiogroup" :aria-labelledby="`che-${row.key}`" @keydown="endKeys">
                <button type="button" role="radio" :data-end="`${row.key}:default`" :aria-checked="row.end === 'default'" :tabindex="row.end === 'default' ? 0 : -1" @click="emit('end', 'default')">the default’s fallback</button>
                <button type="button" role="radio" :data-end="`${row.key}:wait`" :aria-checked="row.end === 'wait'" :tabindex="row.end === 'wait' ? 0 : -1" @click="emit('end', 'wait')">wait</button>
              </span>
              <b v-else>{{ endText(row.end, false) }}</b>
              <span class="ch-end-d"><template v-if="row.end === 'default'"><template v-for="(part, index) in tail" :key="index"><AppIcon v-if="index" class="ar" name="arrow" :size="12" /><b v-if="index === tail.length - 1">{{ part }}</b><template v-else>{{ part }}</template></template></template><template v-else>Nothing outside this order runs.</template></span>
            </template>
          </div>
          <p v-for="never in row.never" :key="never.line" class="ch-never" :data-never="`${row.key}:${never.line}`">
            <AppIcon name="not" :size="13" />
            <span :data-tip="`${because(never)}It is skipped even in someone’s own order.`">Never for {{ kind }}: <b>{{ nameOf({ line: never.line, effort: null }) }}</b> · {{ who(never) }}<span class="sr-only">. {{ because(never) }}It is skipped even in someone’s own order.</span></span>
            <button v-if="allow(never)" type="button" class="link-btn quiet" :data-unnever="`${row.key}:${never.line}`" @click="emit('allow', never)">Allow again</button>
          </p>
          <p v-if="row.lock && !everyone" class="ch-hint"><AppIcon name="lock" :size="12" />Locked for everyone, with its order.</p>
          <p v-else-if="editable && row.chain.length > 1" class="ch-hint">Drag to reorder, or focus a handle and use ↑ ↓.</p>
          <p v-if="several" class="ch-hint"><AppIcon name="route" :size="12" />A model with several routes tries each one before the next step.</p>
          <p v-if="row.isDefault" class="ch-hint"><AppIcon name="gauge" :size="12" />At a daily cap, the Agents dial follows this order too.</p>
        </div>
      </div>
    </div>
  </div>
</template>
