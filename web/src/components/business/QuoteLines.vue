<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<!-- Parked (AEON-70, 2026-09-24): quotes and organisations will be ported from Markus's current classic Paimos quote builder; this file is not routed or linked. -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import type { QuoteVersion, Unit } from '../../lib/business'
import { blankLine, isBlank, moveLine, type Draft, type LineResult } from '../../lib/quoteDraft'
import { formatAmount, ratePercent } from './money'
import { useBusiness } from '../../stores/business'
import AppIcon from './BizIcon.vue'
import KeyCap from '../KeyCap.vue'

// Line items like a small spreadsheet: Tab to the next cell, Enter to the same
// cell in the next line (a new line at the end), Shift+Enter back up, Alt+arrows
// or the grip to reorder, Cmd/Ctrl+Backspace to remove a line. Amounts are the
// exact rate × quantity the server will freeze. A frozen version shows its own
// stored rates and amounts, never today's.
const props = defineProps<{ draft: Draft | null; results: LineResult[]; frozen: QuoteVersion | null; editable: boolean }>()
const emit = defineEmits<{ save: []; changed: [] }>()
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const business = useBusiness()
const grid = ref<HTMLElement>()
const dragFrom = ref<number | null>(null)
const dropAt = ref<{ index: number; after: boolean } | null>(null)
const UNITS: { value: Unit; label: string }[] = [{ value: 'hour', label: 'hour' }, { value: 'day', label: 'day' }, { value: 'item', label: 'item' }]
const COLS = ['description', 'cost', 'quantity', 'unit', 'tax'] as const
type Col = typeof COLS[number]

const currency = computed(() => props.frozen?.currency ?? props.draft?.currency ?? 'EUR')
// Cost units a line can use: live with a rate in this currency, plus any a line already names.
const unitOptions = computed(() => {
  const usable = business.usable(null, currency.value)
  const named = new Set(props.draft?.lines.map(line => line.costUnitId).filter(Boolean))
  const extra = business.costUnits.filter(unit => named.has(unit.node.id) && !usable.includes(unit))
  return [...usable, ...extra].map(unit => ({ id: unit.node.id, label: unit.node.title, key: unit.node.key, usable: usable.includes(unit) }))
})
const costUnitName = (id: string) => business.costUnit(id)?.node.title ?? 'Cost unit'
const money = (value: string | null) => { if (!value) return ''; try { return formatAmount(value, currency.value) } catch { return value } }

function focusCell(row: number, col: Col) {
  void nextTick(() => grid.value?.querySelector<HTMLElement>(`[data-row="${row}"][data-col="${col}"]`)?.focus())
}
function changed() { emit('changed') }
function addAfter(index: number) {
  if (!props.draft) return
  props.draft.lines.splice(index + 1, 0, blankLine(props.draft.lines[index]))
  changed()
}
function remove(index: number, col: Col = 'description') {
  const lines = props.draft?.lines
  if (!lines) return
  if (lines.length === 1) { lines.splice(0, 1, blankLine(lines[0])); changed(); focusCell(0, col); return }
  lines.splice(index, 1)
  changed()
  focusCell(Math.max(0, index - 1), col)
}
function move(from: number, to: number, col: Col | null = null) {
  if (!props.draft) return
  const next = moveLine(props.draft.lines, from, to)
  if (next === props.draft.lines) return
  props.draft.lines.splice(0, props.draft.lines.length, ...next)
  changed()
  if (col) focusCell(to, col)
}
function keydown(event: KeyboardEvent) {
  const target = event.target as HTMLElement
  const row = Number(target.dataset.row), col = target.dataset.col as Col | undefined
  if (!props.draft || !col || Number.isNaN(row)) return
  const mod = event.metaKey || event.ctrlKey
  const lines = props.draft.lines
  if (event.key === 'Enter' && mod) { event.preventDefault(); emit('save'); return }
  if (event.key === 'Enter' && event.shiftKey) { event.preventDefault(); if (row > 0) focusCell(row - 1, col); return }
  if (event.key === 'Enter' && !event.altKey) {
    event.preventDefault()
    if (row < lines.length - 1) focusCell(row + 1, col)
    // A new line starts with what it offers.
    else if (!isBlank(lines[row])) { addAfter(row); focusCell(row + 1, 'description') }
    return
  }
  if (event.altKey && (event.key === 'ArrowUp' || event.key === 'ArrowDown')) { event.preventDefault(); move(row, row + (event.key === 'ArrowUp' ? -1 : 1), col); return }
  if (mod && (event.key === 'Backspace' || event.key === 'Delete')) { event.preventDefault(); remove(row, col); return }
  // Up and down walk the lines from text cells; selects keep their own arrows.
  if (target.tagName === 'INPUT' && !event.shiftKey && !mod && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
    const next = row + (event.key === 'ArrowDown' ? 1 : -1)
    if (next >= 0 && next < lines.length) { event.preventDefault(); focusCell(next, col) }
  }
}

// ---------- Drag to reorder ----------
function dragStart(event: DragEvent, index: number) {
  dragFrom.value = index
  event.dataTransfer?.setData('text/plain', String(index))
  if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move'
}
function dragOver(event: DragEvent, index: number) {
  if (dragFrom.value === null) return
  event.preventDefault()
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
  dropAt.value = { index, after: event.clientY > rect.top + rect.height / 2 }
}
function drop(event: DragEvent) {
  event.preventDefault()
  if (dragFrom.value === null || !dropAt.value) return dragEnd()
  const from = dragFrom.value
  let to = dropAt.value.index + (dropAt.value.after ? 1 : 0)
  if (to > from) to -= 1
  dragEnd()
  move(from, to)
}
function dragEnd() { dragFrom.value = null; dropAt.value = null }
defineExpose({ focusFirst: () => focusCell(0, 'description'), focusLast: () => focusCell(Math.max(0, (props.draft?.lines.length ?? 1) - 1), 'description') })
</script>

<template>
  <div class="lines-card" :class="{ editing: editable && !!draft }">
    <div class="lines-head" aria-hidden="true">
      <span /><span class="c-pos">#</span><span>Description</span><span>Cost unit</span><span class="num">Qty</span><span>Unit</span><span class="num">Rate</span><span class="num">Tax</span><span class="num">Amount</span><span />
    </div>

    <!-- A frozen version: its own stored lines, rates and amounts. -->
    <ol v-if="frozen && !(editable && draft)" class="lines" :aria-label="`Lines of version ${frozen.version}`">
      <li v-for="line in frozen.lines" :key="line.position" class="line frozen">
        <span class="c-grip" />
        <span class="c-pos mono">{{ line.position + 1 }}</span>
        <span class="c-desc">{{ line.description }}</span>
        <span class="c-cost"><AppIcon name="tag" :size="11" class="faint-icon" /><span class="cost-name">{{ costUnitName(line.cost_unit_node_id) }}</span></span>
        <span class="c-qty num mono">{{ line.quantity.replace(/\.?0+$/, '') }}</span>
        <span class="c-unit">{{ line.unit }}</span>
        <span class="c-rate num mono">{{ money(line.rate_amount) }}</span>
        <span class="c-tax num mono">{{ ratePercent(line.tax_rate) }}<small>%</small></span>
        <span class="c-amount num mono">{{ money(line.net_amount) }}<span class="rate-hint">× {{ money(line.rate_amount) }}/{{ line.unit === 'hour' ? 'h' : line.unit }}</span></span>
        <span />
      </li>
    </ol>

    <!-- The draft of the next version. -->
    <ol v-else-if="draft" ref="grid" class="lines" aria-label="Lines of the next version" @keydown="keydown" @drop="drop" @dragend="dragEnd">
      <li
        v-for="(line, index) in draft.lines" :key="line.id" class="line" :class="{ dragging: dragFrom === index, 'drop-before': dropAt?.index === index && !dropAt.after, 'drop-after': dropAt?.index === index && dropAt.after, problem: results[index]?.problem }"
        @dragover="dragOver($event, index)"
      >
        <span class="c-grip" :draggable="editable" :data-tip="editable ? `Drag to reorder · ${mac ? 'Option' : 'Alt'} Up or Down` : undefined" @dragstart="dragStart($event, index)"><AppIcon v-if="editable" name="grip" :size="14" /></span>
        <span class="c-pos mono">{{ index + 1 }}</span>
        <span class="c-desc">
          <input v-model="line.description" class="cell" :data-row="index" data-col="description" :aria-label="`Line ${index + 1} description`" placeholder="What is offered" maxlength="2000" :readonly="!editable" autocomplete="off" @input="changed" />
        </span>
        <span class="c-cost">
          <select v-model="line.costUnitId" class="cell select" :class="{ unset: !line.costUnitId }" :data-row="index" data-col="cost" :aria-label="`Line ${index + 1} cost unit`" :disabled="!editable" @change="changed">
            <option value="" disabled>Cost unit</option>
            <option v-for="option in unitOptions" :key="option.id" :value="option.id">{{ option.label }}{{ option.usable ? '' : ' (no rate)' }}</option>
          </select>
          <AppIcon name="chevron" :size="11" class="select-chev" />
        </span>
        <span class="c-qty">
          <input v-model="line.quantity" class="cell num mono" :class="{ bad: line.quantity && !results[index]?.quantity }" inputmode="decimal" :data-row="index" data-col="quantity" :aria-label="`Line ${index + 1} quantity`" placeholder="0" :readonly="!editable" autocomplete="off" @input="changed" />
        </span>
        <span class="c-unit">
          <select v-model="line.unit" class="cell select" :data-row="index" data-col="unit" :aria-label="`Line ${index + 1} unit`" :disabled="!editable" @change="changed">
            <option v-for="unit in UNITS" :key="unit.value" :value="unit.value">{{ unit.label }}</option>
          </select>
          <AppIcon name="chevron" :size="11" class="select-chev" />
        </span>
        <span class="c-rate num mono">
          <template v-if="results[index]?.rate">{{ money(results[index].rate!.bill_amount) }}</template>
          <span v-else-if="line.costUnitId" class="no-rate" :data-tip="`No ${draft.currency} rate per ${line.unit} is in force today`"><AppIcon name="alert" :size="12" />none</span>
        </span>
        <span class="c-tax">
          <input v-model="line.tax" class="cell num mono" :class="{ bad: line.tax && !results[index]?.taxRate }" inputmode="decimal" :data-row="index" data-col="tax" :aria-label="`Line ${index + 1} tax percent`" placeholder="0" :readonly="!editable" autocomplete="off" @input="changed" /><small aria-hidden="true">%</small>
        </span>
        <span class="c-amount num mono">
          <template v-if="results[index]?.net">{{ money(results[index].net) }}</template>
          <span v-else class="faint">—</span>
          <span v-if="results[index]?.rate" class="rate-hint">× {{ money(results[index].rate!.bill_amount) }}/{{ line.unit === 'hour' ? 'h' : line.unit }}</span>
          <span v-else-if="line.costUnitId" class="rate-hint warn">no {{ line.unit }} rate</span>
        </span>
        <span class="c-act">
          <button v-if="editable" type="button" class="icon-btn sm flat remove" :aria-label="`Remove line ${index + 1}`" :data-tip="`Remove line · ${mac ? 'Cmd' : 'Ctrl'} Backspace`" @click="remove(index)"><AppIcon name="trash" :size="13" /></button>
        </span>
      </li>
    </ol>
    <div v-if="editable && draft" class="lines-foot">
      <button type="button" class="add-line" @click="addAfter(draft.lines.length - 1); focusCell(draft.lines.length - 1, 'description')"><AppIcon name="plus" :size="12" />Add line</button>
      <span class="keys-hint" aria-hidden="true"><kbd class="keycap">Tab</kbd> next cell · <kbd class="keycap"><AppIcon name="enter" /></kbd> next line · <KeyCap k="alt" /><KeyCap k="up" /> move</span>
    </div>
  </div>
</template>

<style scoped>
.lines-card { container: lines / inline-size; border-radius: 12px; box-shadow: inset 0 0 0 1px var(--line); background: var(--surface-sunken); overflow: clip; }
.lines-card.editing { background: var(--field-bg); box-shadow: inset 0 0 0 1px var(--line-2); }
.lines-head, .line { display: grid; grid-template-columns: 18px 22px minmax(140px, 1fr) minmax(110px, 170px) 64px 76px 84px 58px 112px 30px; align-items: center; column-gap: 6px; padding: 0 8px 0 4px; }
.lines-head { height: 32px; border-bottom: 1px solid var(--line); font: 500 9.5px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.num { text-align: right; justify-content: flex-end; }
.lines { margin: 0; padding: 0; list-style: none; }
.line { position: relative; min-height: 40px; border-bottom: 1px solid var(--line); font-size: 13.5px; }
.line:last-child { border-bottom: 0; }
.line > span { min-width: 0; display: flex; align-items: center; }
@media (hover: hover) { .editing .line:hover { background: var(--row-hover); } }
.line:focus-within { background: var(--row-selected); }
.line.problem .c-pos { color: var(--warn-ink); }
.line.dragging { opacity: .45; }
/* Where a dragged line lands: a caret in the gap between lines, not an edge of either. */
.line.drop-before::before, .line.drop-after::after { content: ''; position: absolute; left: 10px; right: 10px; height: 2px; border-radius: 2px; background: var(--teal); pointer-events: none; }
.line.drop-before::before { top: -1px; }
.line.drop-after::after { bottom: -1px; }
.c-grip { justify-content: center; color: var(--ink-3); cursor: grab; }
.c-grip:not([draggable="true"]) { cursor: default; }
.c-pos { justify-content: center; font-size: 11px; color: var(--ink-3); }
.frozen .c-desc { padding: 9px 6px; line-height: 1.35; color: var(--ink); }
.frozen .c-cost { gap: 6px; font-size: 12.5px; color: var(--ink-2); overflow: hidden; white-space: nowrap; }
.frozen .c-qty, .frozen .c-rate, .frozen .c-amount, .frozen .c-tax { padding: 0 6px; font-size: 12.5px; }
.frozen .c-unit { font-size: 12.5px; color: var(--ink-2); padding: 0 6px; }
.frozen .c-amount { color: var(--ink); font-weight: 600; }
.faint-icon { color: var(--ink-3); flex-shrink: 0; }
.cost-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cell { width: 100%; min-width: 0; height: 30px; padding: 0 6px; border: 0; border-radius: 6px; background: transparent; color: var(--ink); font-size: 13.5px; }
.cell::placeholder { color: var(--ink-3); }
.cell:hover:not([readonly]):not(:disabled) { background: var(--surface-raised-2); box-shadow: inset 0 0 0 1px var(--line); }
.cell:focus { background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--teal), 0 0 0 3px color-mix(in srgb, var(--primary-tint) 35%, transparent); }
.cell.mono { font-family: var(--mono); font-size: 12.5px; font-variant-numeric: tabular-nums; font-variant-ligatures: none; }
.cell.num { text-align: right; }
.cell.bad { color: var(--danger); box-shadow: inset 0 0 0 1px var(--danger-line); }
.c-cost, .c-unit { position: relative; }
.select { appearance: none; padding-right: 20px; cursor: pointer; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.select.unset { color: var(--ink-3); }
.select:disabled { cursor: default; opacity: 1; }
.select-chev { position: absolute; right: 6px; color: var(--ink-3); pointer-events: none; }
.c-tax small { margin-left: 2px; font-size: 11px; color: var(--ink-3); }
.c-tax .cell { padding-right: 2px; }
.c-rate { padding: 0 4px; font-size: 12.5px; color: var(--ink-2); }
.no-rate { display: inline-flex; align-items: center; gap: 4px; color: var(--warn-ink); font-size: 12px; font-family: var(--font); }
.c-amount { padding: 0 4px; font-size: 12.5px; color: var(--ink); font-weight: 600; flex-direction: column; align-items: flex-end !important; justify-content: center; }
.rate-hint { display: none; font-size: 10.5px; font-weight: 400; color: var(--ink-3); }
.rate-hint.warn { color: var(--warn-ink); font-family: var(--font); }
.faint { color: var(--ink-3); font-weight: 400; }
.c-act { justify-content: center; }
.remove { width: 26px; height: 26px; color: var(--ink-3); opacity: 0; }
.line:hover .remove, .line:focus-within .remove { opacity: 1; }
.remove:hover { color: var(--danger); background: var(--danger-bg); }
.lines-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 40px; padding: 4px 10px 4px 6px; border-top: 1px solid var(--line); }
.add-line { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; }
.add-line:hover { background: var(--row-hover); }
.add-line:focus-visible { box-shadow: var(--focus-ring); }
.keys-hint { display: inline-flex; align-items: center; gap: 4px; font-size: 11.5px; color: var(--ink-3); }
.keys-hint .keycap + .keycap { margin-left: 1px; }
@media (hover: none) { .remove { opacity: 1; } .keys-hint { display: none; } }

/* Narrow (docked panel, phones): each line takes two rows. */
@container lines (max-width: 700px) {
  .lines-head { display: none; }
  .line {
    grid-template-columns: 18px 22px minmax(0, 1fr) 58px 64px 50px minmax(84px, auto) 30px;
    grid-template-areas: "grip pos desc desc desc desc desc act" ". . cost qty unit tax amount .";
    row-gap: 2px; padding-top: 6px; padding-bottom: 6px;
  }
  .c-grip { grid-area: grip; } .c-pos { grid-area: pos; } .c-desc { grid-area: desc; } .c-act { grid-area: act; }
  .c-cost { grid-area: cost; } .c-qty { grid-area: qty; } .c-unit { grid-area: unit; } .c-tax { grid-area: tax; } .c-amount { grid-area: amount; }
  .c-rate { display: none !important; }
  .rate-hint { display: block; }
  .frozen .c-desc { padding: 2px 6px; }
  .frozen .c-cost { padding: 0 6px; }
}
@container lines (max-width: 420px) {
  .line { grid-template-columns: 18px 22px minmax(0, 1fr) 52px 58px 46px 30px; grid-template-areas: "grip pos desc desc desc desc act" ". . cost cost qty unit ." ". . tax amount amount amount ."; }
  .c-tax { justify-content: flex-start; }
}
</style>
