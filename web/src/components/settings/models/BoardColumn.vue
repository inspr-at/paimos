<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import ColumnHead from './ColumnHead.vue'
import ModelCard from './ModelCard.vue'
import CantNote from './CantNote.vue'
import { boardText, canMove, zonesFor, type BoardCard, type BoardColumn, type BoardContext, type BoardZone } from '../../../lib/modelsBoard'
const props = defineProps<{ column: BoardColumn; context: BoardContext; editable: boolean; busy: boolean; german: boolean; full: boolean; inherited?: boolean; template?: 'best' | 'balanced' | 'save' | null; dragging?: string; target?: { column: string; zone: BoardZone; index: number } }>()
defineEmits<{ open: [column: BoardColumn, card: BoardCard, zone: BoardZone, event: MouseEvent]; keys: [column: BoardColumn, card: BoardCard, event: KeyboardEvent]; press: [column: BoardColumn, card: BoardCard, event: PointerEvent]; menu: [column: BoardColumn, event: MouseEvent]; reset: [column: BoardColumn] }>()
const labels: Record<BoardZone, [string, string]> = { list: ['In this order', 'In dieser Reihenfolge'], top: ['Pinned to top', 'Oben angepinnt'], free: ['People order these', 'Personen ordnen diese'], bottom: ['Pinned to bottom', 'Unten angepinnt'], not: ['Not allowed', 'Nicht erlaubt'] }
const text = (en: string, de: string) => boardText(props.german, en, de)
function number(zone: BoardZone, index: number) { if (zone === 'not') return undefined; return index + 1 + (zone === 'free' ? props.column.top.length : zone === 'bottom' ? props.column.top.length + props.column.free.length : 0) }
</script>
<template>
  <section class="bcol" :data-column="column.column" :aria-label="column.label">
    <ColumnHead :column="column" :layer="context.layer" :german="german" :editable="editable && !busy" :full="full" :inherited="inherited" :template="template" @menu="$emit('menu', column, $event)" @reset="$emit('reset', column)" />
    <template v-for="zone in zonesFor(context.layer)" :key="zone">
      <div v-if="zone !== 'list'" class="divider" aria-hidden="true"><span>{{ text(...labels[zone]) }}</span></div>
      <ol class="zone" :class="{ 'drop-target': target?.column === column.column && target.zone === zone }" :data-zone="zone" :aria-label="`${column.label} · ${text(...labels[zone])}`" :data-empty="editable ? text('Drag here', 'Hierher ziehen') : text('None', 'Keine')">
        <li v-for="(card, index) in column[zone]" :key="card.line" :data-card="card.line"><ModelCard :card="card" :number="number(zone, index)" :german="german" :thinking="column.column.startsWith('review:') ? 'review' : column.thinking.word" :forbidden="zone === 'not'" :placeholder="dragging === `${column.column}/${card.line}`" :movable="editable && !busy && canMove(card, context)" @open="$emit('open', column, card, zone, $event)" @keys="$emit('keys', column, card, $event)" @press="$emit('press', column, card, $event)" /></li>
      </ol>
    </template>
    <p v-if="column.not.some(card => card.lock?.kind === 'cross_family')" class="cross-note"><a href="/settings/policies">{{ text('Cross-family review rule', 'Prüfregel: andere Modellfamilie') }}</a></p>
    <p v-if="context.layer === 'rules' && [...column.top, ...column.bottom].some(card => column.cant.some(item => item.line === card.line))" class="rule-cap-note">{{ text('The rule is kept and applies as soon as this model can do the work.', 'Die Regel bleibt und gilt, sobald dieses Modell die Arbeit kann.') }}</p>
    <CantNote :items="column.cant" :german="german" />
  </section>
</template>
<style scoped>
.bcol { position: relative; display: flex; flex-direction: column; min-width: 0; min-height: calc(var(--board-slots, 8) * 56px + var(--board-head, 72px) + 176px); padding: 12px; border-radius: 16px; background: var(--surface-raised-2); box-shadow: inset 0 0 0 1px var(--line); }.zone { display: grid; gap: 8px; align-content: start; margin: 0; padding: 0; list-style: none; }.zone:empty { height: 40px; border-radius: 10px; box-shadow: inset 0 0 0 1px var(--line); }.zone:empty::after { content: attr(data-empty); display: grid; place-items: center; height: 40px; padding: 0 8px; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; font-size: 11.5px; color: var(--ink-3); }.drop-target { outline: 1px solid var(--chip-teal-line); outline-offset: 3px; background: var(--row-selected); }.divider { display: flex; align-items: center; gap: 8px; height: 16px; margin: 12px 2px 8px; font: 500 10px/16px var(--mono); letter-spacing: .1em; text-transform: uppercase; color: var(--ink-3); }.divider span { white-space: nowrap; }.divider::before, .divider::after { content: ''; flex: 1; height: 1px; background: var(--line-2); }.divider::before { flex: 0 0 8px; }.rule-cap-note { margin: 8px 0; font-size: 11.5px; color: var(--ink-2); }.cross-note { margin: 8px 0; font-size: 11.5px; overflow-wrap: anywhere; }.bcol > .bc-head + .divider { margin-top: 0; }@media (max-width: 860px) { .bcol { scroll-snap-align: start; }.bcol :deep(.bc-head) { position: sticky; top: 0; z-index: 1; background: var(--surface-raised-2); } }
</style>
