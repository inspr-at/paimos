<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../../AppIcon.vue'
import ModelCard from './ModelCard.vue'
import type { BoardCard, BoardColumn } from '../../../lib/modelsBoard'
const props = defineProps<{ cards: BoardCard[]; columns: BoardColumn[]; editable: boolean; german: boolean }>()
function placed(card: BoardCard) { const count = props.columns.filter(column => [...column.list, ...column.not].some(value => value.line === card.line)).length; return count ? props.german ? `in ${count} Spalte${count === 1 ? '' : 'n'}` : `in ${count} column${count === 1 ? '' : 's'}` : props.german ? 'noch in keiner Spalte' : 'in no column yet' }
defineEmits<{ open: [card: BoardCard, event: MouseEvent]; press: [card: BoardCard, event: PointerEvent] }>()
</script>
<template><div v-if="cards.length" class="tray"><div class="tray-head"><b><AppIcon name="sparkle" :size="14" />{{ german ? 'Neu im Katalog' : 'New in the catalog' }}</b><small>{{ german ? 'Nirgends genutzt, bis jemand es in eine Spalte zieht.' : 'Not used anywhere until someone drags it into a column.' }}</small></div><ol class="tray-list"><li v-for="card in cards" :key="card.line"><ModelCard :card="card" :subtitle="placed(card)" :movable="editable" :german="german" @open="$emit('open', card, $event)" @press="$emit('press', card, $event)" /><span>{{ german ? 'Neu' : 'New' }}</span></li></ol></div></template>
<style scoped>.tray { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 16px; margin-top: 12px; padding: 8px 12px; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }.tray-head { display: grid; gap: 2px; }.tray-head b { display: flex; align-items: center; gap: 6px; font-size: 13px; }.tray-head small { font-size: 12px; color: var(--ink-3); }.tray-list { flex: 1 1 320px; display: flex; flex-wrap: wrap; gap: 8px; min-width: 0; max-width: 100%; margin: 0; padding: 0; list-style: none; }.tray-list li { display: flex; align-items: center; gap: 8px; width: min(320px, 100%); }.tray-list li > button { flex: 1; min-width: 0; }.tray-list li > span { color: var(--teal-ink); font-size: 12px; }</style>
