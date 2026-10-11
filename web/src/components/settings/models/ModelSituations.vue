<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '../../AppIcon.vue'
import HarnessMark from '../../agents/HarnessMark.vue'
import ModelsPopover from './ModelsPopover.vue'
import ModelPicker from './ModelPicker.vue'
import { harnessLabel } from '../../../lib/agentState'
import { getSituationLimits } from '../../../lib/workKinds'
import {
  SITUATIONS, entryFor, lineFallback, lineFamily, pickText, situationCount, situationEmptyText, situationEndText, situationHint, situationOrder, situationRows, situationStep, situationSteps,
  type ModelEntry, type RowView, type SimpleDocument, type SimpleSituation, type SituationKey, type SituationLimitsView, type SituationOrder, type SituationRow,
} from '../../../lib/modelsSimple'

// Different fallback by situation (Models v2, AEON-1144 W3): Expert, admins only. Rows × First build, Fix rounds, Stuck,
// Review. A cell opens one popover with its order, Add, and Same as the row / Back to automatic. The fold grows in
// place; the popovers overlay, so nothing on the page moves (AEON-541). Only For everyone edits; Just me reads.
const props = defineProps<{
  source: SimpleDocument | null; entries: ModelEntry[]; labels: Map<string, string>; order: string[]; fresh: Set<string>
  editable: boolean; busy: boolean; actionKey: string
  cant: (column: string, line: string) => string
  save: (column: string, key: SituationKey, next: SituationOrder | null, seen: number) => Promise<boolean>
}>()
const open = defineModel<boolean>('open', { default: false })

const rows = computed<SituationRow[] | null>(() => props.source?.situations ? situationRows({ doc: props.source, entries: props.entries, labels: props.labels, order: props.order }) : null)
const count = computed(() => rows.value ? situationCount(rows.value) : 0)
const calm = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
// The body stays rendered while the fold closes, so it shrinks instead of vanishing.
const rendered = ref(open.value)
let closing = 0
watch(open, value => {
  clearTimeout(closing)
  if (value) rendered.value = true
  else closing = window.setTimeout(() => { if (!open.value) rendered.value = false }, calm() ? 0 : 260)
})

// The limits come from Settings › Kinds of work, read once when the fold first opens.
const limits = ref<SituationLimitsView | null>(null)
let limitsRead: AbortController | undefined
watch([open, () => props.actionKey], ([value]) => {
  if (!value || limits.value || limitsRead) return
  limitsRead = new AbortController()
  getSituationLimits(limitsRead.signal).then(result => {
    if (Number.isInteger(result?.small_hours) && Number.isInteger(result?.fix_rounds)) limits.value = { small_hours: result.small_hours, fix_rounds: result.fix_rounds }
  }).catch(() => { limitsRead = undefined /* The hints name the situation in words instead. */ })
}, { immediate: true })
onBeforeUnmount(() => { limitsRead?.abort(); clearTimeout(closing) })

const label = (key: SituationKey) => SITUATIONS.find(item => item.key === key)!.label
const id = (column: string, key: SituationKey) => `sx-${column}-${key}`
const name = (line: string | null | undefined) => entryFor(props.entries, line)?.name ?? lineFallback(line)
const withLevel = (line: string | null | undefined, effort: string | null) => pickText(entryFor(props.entries, line), effort, lineFallback(line))
const mark = (line: string | null | undefined, harness?: string | null) => entryFor(props.entries, line)?.harness ?? harness ?? ''
const family = (line: string | null | undefined) => entryFor(props.entries, line) ? '' : lineFamily(line)
const cellWords = (row: SituationRow, key: SituationKey) => {
  const cell = row.cells[key]
  return `${row.label}, ${label(key)}: ${cell ? withLevel(cell.line, cell.effort) + (cell.chain.length ? `, then ${cell.chain.length} more` : '') : key === 'review' ? 'automatic' : 'same as the row'}`
}

// ----- the cell editor and its Add picker: one at a time, both anchored to the cell -----------------------------
// What the person acts on is bound to the cell and the revision on screen when they opened it (code health rule 4).
interface Target { column: string; key: SituationKey; anchor: HTMLElement; seen: number }
const cell = ref<Target | null>(null), adding = ref<Target | null>(null), live = ref('')
const rowOf = (target: Target | null) => target ? rows.value?.find(row => row.column === target.column) ?? null : null
const shownCell = computed<SimpleSituation | null>(() => { const target = cell.value; return target ? rowOf(target)?.cells[target.key] ?? null : null })
const steps = computed(() => situationSteps(shownCell.value))
const title = (target: Target) => `${label(target.key)} · ${rowOf(target)?.label ?? target.column}`
function closeAll() { cell.value = null; adding.value = null }
watch(() => [props.actionKey, props.editable], closeAll)
// A cell whose row left the matrix (a refresh) takes its menus with it.
watch(rows, () => { if (cell.value && !rowOf(cell.value)) cell.value = null; if (adding.value && !rowOf(adding.value)) adding.value = null })

function openCell(row: SituationRow, key: SituationKey, event: MouseEvent) {
  const anchor = event.currentTarget as HTMLElement
  if (cell.value?.anchor === anchor) { cell.value = null; return }
  closeAll()
  if (props.source) cell.value = { column: row.column, key, anchor, seen: props.source.revision }
}
function refocus(column: string, key: SituationKey) { void nextTick(() => document.getElementById(id(column, key))?.focus({ preventScroll: true })) }
async function write(target: Target, next: SituationOrder | null) {
  await props.save(target.column, target.key, next, target.seen)
  refocus(target.column, target.key)
}
function startAdd() {
  const target = cell.value
  if (!target || props.busy) return
  cell.value = null
  // The picker opens on the cell once the editor has let go of it.
  void nextTick(() => { if (target.anchor.isConnected) adding.value = target })
}
function removeStep(index: number) {
  const target = cell.value
  if (!target || props.busy) return
  // The order comes from the open cell, so it is read before the editor closes.
  const next = situationOrder(steps.value.filter((_, at) => at !== index))
  cell.value = null
  void write(target, next)
}
function sameAsRow() {
  const target = cell.value
  if (!target || props.busy) return
  cell.value = null
  void write(target, null)
}
const addRow = computed<RowView | null>(() => {
  const target = adding.value, row = rowOf(target)
  if (!target || !row) return null
  // Reviews start at the highest level; the other situations at the row's level.
  return { key: id(target.column, target.key), column: target.column, label: row.label, isDefault: row.isDefault, line: null, effort: target.key === 'review' ? 'max' : row.effort, entry: row.entry, mine: false, reset: false, remove: false, editable: true, lock: null, unavailable: null }
})
const addCant = (line: string) => {
  const target = adding.value
  if (!target) return ''
  return situationSteps(rowOf(target)?.cells[target.key]).some(step => step.line === line) ? 'already in this order' : props.cant(target.column, line)
}
async function choose(entry: ModelEntry, effort: string | null) {
  const target = adding.value
  adding.value = null
  const row = rowOf(target)
  if (!target || !row) return
  const shown = row.cells[target.key], before = situationSteps(shown)
  if (before.some(step => step.line === entry.line)) { live.value = `${entry.name} is already in this order.`; refocus(target.column, target.key); return }
  await write(target, situationOrder([...before, situationStep(target.key, entry, effort)], shown ? undefined : target.key === 'review' ? 'auto' : 'row'))
}
</script>
<template>
  <div class="sx" data-situations>
    <button id="sx-fold" type="button" class="sx-sum" :aria-expanded="open" aria-controls="sx-body" @click="open = !open">
      <AppIcon class="sx-chev" name="chevron-right" :size="13" />
      <span class="sx-s"><b>Different fallback by situation</b> · {{ rows ? (count ? `${count} set` : 'none set') : 'couldn’t load' }}</span>
      <span class="tag-exp">Expert</span>
    </button>
    <div id="sx-body" class="sx-fold" :class="{ open }" role="region" aria-label="Different fallback by situation">
      <div class="sx-fold-in">
        <div v-if="rendered" class="sx-in">
          <template v-if="rows">
            <p class="sx-lead">A row can use another order when work is small, in fix rounds, or stuck, and prefer a reviewer. An empty cell uses the row.{{ editable ? '' : ' ' }}<b v-if="!editable">Change these under For everyone.</b></p>
            <div class="sx-t" role="group" aria-label="Kind of work by situation">
              <div class="sx-r sx-h" aria-hidden="true"><span /><span v-for="item in SITUATIONS" :key="item.key" class="sx-c"><b>{{ item.label }}</b><small data-sx-hint>{{ situationHint(item.key, limits) }}</small></span></div>
              <div v-for="row in rows" :key="row.column" class="sx-r" :data-sx-row="row.column">
                <span v-clip-tip class="sx-k">{{ row.label }}</span>
                <template v-for="item in SITUATIONS" :key="item.key">
                  <button v-if="editable" :id="id(row.column, item.key)" type="button" class="sx-cell" :class="{ set: !!row.cells[item.key] }" :data-sit="`${row.column}:${item.key}`" aria-haspopup="dialog" :aria-expanded="cell?.anchor.id === id(row.column, item.key) || adding?.anchor.id === id(row.column, item.key)" :aria-label="`${cellWords(row, item.key)}. Change`" @click="openCell(row, item.key, $event)">
                    <span class="sx-cl" aria-hidden="true">{{ item.label }}</span>
                    <span class="sx-cv">
                      <template v-if="row.cells[item.key]"><span class="hg" aria-hidden="true"><HarnessMark :harness="mark(row.cells[item.key]!.line, row.cells[item.key]!.harness)" :provider="family(row.cells[item.key]!.line)" :size="11" /></span><span v-clip-tip="withLevel(row.cells[item.key]!.line, row.cells[item.key]!.effort)" class="cell-t">{{ name(row.cells[item.key]!.line) }}</span><span v-if="row.cells[item.key]!.chain.length" class="cell-more">+{{ row.cells[item.key]!.chain.length }}</span></template>
                      <span v-else class="cell-e">{{ item.key === 'review' ? 'Automatic' : 'Same' }}</span>
                    </span>
                  </button>
                  <span v-else :id="id(row.column, item.key)" class="sx-cell ro" :class="{ set: !!row.cells[item.key] }" :data-sit="`${row.column}:${item.key}`">
                    <span class="sr-only">{{ cellWords(row, item.key) }}</span>
                    <span class="sx-cl" aria-hidden="true">{{ item.label }}</span>
                    <span class="sx-cv" aria-hidden="true">
                      <template v-if="row.cells[item.key]"><span class="hg"><HarnessMark :harness="mark(row.cells[item.key]!.line, row.cells[item.key]!.harness)" :provider="family(row.cells[item.key]!.line)" :size="11" /></span><span class="cell-t">{{ name(row.cells[item.key]!.line) }}</span><span v-if="row.cells[item.key]!.chain.length" class="cell-more">+{{ row.cells[item.key]!.chain.length }}</span></template>
                      <span v-else class="cell-e">{{ item.key === 'review' ? 'Automatic' : 'Same' }}</span>
                    </span>
                  </span>
                </template>
              </div>
            </div>
          </template>
          <p v-else class="sx-lead" role="status">The situation orders couldn’t load. Agents keep using them; reload to see them here.</p>
        </div>
      </div>
    </div>
    <p class="sr-only" aria-live="polite">{{ live }}</p>
    <ModelsPopover v-if="cell && rowOf(cell)" :open="true" :anchor="cell.anchor" :label="title(cell)" labelledby="sxp-t" :width="380" @update:open="value => { if (!value) cell = null }">
      <div class="sxp" data-situation-popover>
        <h3 id="sxp-t">{{ title(cell) }}</h3>
        <template v-if="shownCell && steps.length">
          <ol class="sxp-l" :aria-label="`Order for ${title(cell)}`">
            <li v-for="(step, index) in steps" :key="step.line" class="sxp-it">
              <span class="sxp-n" aria-hidden="true">{{ index + 1 }}</span>
              <span class="sxp-pick"><span class="hg" aria-hidden="true"><HarnessMark :harness="mark(step.line)" :provider="family(step.line)" :size="12" /></span><span v-clip-tip class="pn">{{ name(step.line) }}<span v-if="(entryFor(entries, step.line)?.efforts.length ?? 0) > 1 && step.effort" class="pe"> · {{ step.effort }}</span></span></span>
              <span class="sxp-d">{{ entryFor(entries, step.line) ? harnessLabel(entryFor(entries, step.line)!.harness) : 'not in the model registry' }}</span>
              <button type="button" class="icon-btn flat sxp-x" :aria-label="`Take ${name(step.line)} out`" data-tip="Take it out" :disabled="busy" @click="removeStep(index)"><AppIcon name="close" :size="13" /></button>
            </li>
          </ol>
          <p class="sxp-end"><AppIcon name="wait" :size="14" /><span>{{ situationEndText(cell.key, shownCell) }}</span></p>
        </template>
        <p v-else class="sxp-empty">{{ situationEmptyText(cell.key) }}</p>
        <div class="sxp-acts">
          <button type="button" class="add sm" data-sx-add data-autofocus aria-haspopup="listbox" aria-expanded="false" :disabled="busy" @click="startAdd"><AppIcon name="plus" :size="14" /><span>{{ steps.length ? 'Add to this order' : 'Use another order' }}</span></button>
          <span class="grow" />
          <button v-if="steps.length" type="button" class="link-btn quiet" data-sx-clear :disabled="busy" @click="sameAsRow">{{ cell.key === 'review' ? 'Back to automatic' : 'Same as the row' }}</button>
        </div>
      </div>
    </ModelsPopover>
    <ModelsPopover v-if="adding && addRow" :open="true" :anchor="adding.anchor" :label="`Model for ${title(adding)}`" role="presentation" :width="Math.max(adding.anchor.offsetWidth, 520)" @update:open="value => { if (!value) adding = null }">
      <ModelPicker :entries="entries" :row="addRow" :name="title(adding)" :cant="addCant" :fresh="fresh" @choose="choose" />
    </ModelsPopover>
  </div>
</template>
