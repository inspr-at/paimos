<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Building it › Worst in this window (AEON-1493): the five worst items of one number, anchored to its tile and
// flipping above when there is no room below; a bottom sheet at 760 px and narrower. Each row links to the ticket
// and shows one mark per round. Enter on the tile opens it with the first row focused; Esc closes and returns focus
// to the tile, an outside click closes. ↑/↓ move between the rows.
import { computed, ref } from 'vue'
import AppIcon from '../AppIcon.vue'
import RoundMark from './RoundMark.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import TicketPeekLink from '../TicketPeekLink.vue'
import { vClipTip } from '../../directives/clipTip'
import type { BuildingTileView, RoundOutcome } from '../../lib/deliveryBuilding'
import type { BuildingText } from '../../lib/deliveryBuildingText'
import { ticketPath } from '../../lib/work'

const props = defineProps<{ tile: BuildingTileView; anchor: HTMLElement; routeKey: string; text: BuildingText; focusFirst: boolean }>()
const emit = defineEmits<{ close: [restoreFocus: boolean] }>()
const list = ref<HTMLElement>()
const LEGEND: RoundOutcome[] = ['passed', 'changes', 'failed', 'merge_forward']
const legend = computed(() => props.tile.worst.rows.some(row => row.rounds.includes('none')) ? [...LEGEND, 'none' as const] : LEGEND)

function move(event: KeyboardEvent) {
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
  const rows = [...(list.value?.querySelectorAll<HTMLElement>('.wp-row') ?? [])]
  if (!rows.length) return
  event.preventDefault()
  const at = rows.indexOf(document.activeElement as HTMLElement)
  rows[Math.max(0, Math.min(rows.length - 1, at < 0 ? 0 : at + (event.key === 'ArrowDown' ? 1 : -1)))].focus()
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="440" :tallest="560" :label="tile.worst.title" cycle phone-sheet :sheet-below="760" class="dl-worst" data-testid="delivery-worst" @close="restore => emit('close', restore)" @keydown="move">
    <div class="wp-grab" aria-hidden="true" />
    <div class="wp-head">
      <div class="wp-tt" :data-autofocus="focusFirst && tile.worst.rows.length ? undefined : ''" tabindex="-1">
        <h3>{{ tile.worst.title }}</h3>
        <p v-clip-tip>{{ tile.worst.sub }}</p>
      </div>
      <button type="button" class="wp-x" :aria-label="text.close" @click="emit('close', true)"><AppIcon name="close" :size="14" /></button>
    </div>
    <ul ref="list" class="wp-list">
      <li v-for="(row, index) in tile.worst.rows" :key="row.nodeId">
        <TicketPeekLink class="wp-row" :ticket-key="row.key" :href="ticketPath(routeKey, row.key)" :aria-label="row.said" :data-autofocus="focusFirst && index === 0 ? '' : undefined" @click="emit('close', false)">
          <span class="wp-key">{{ row.key }}</span>
          <span v-clip-tip="row.title" class="wp-title">{{ row.title }}</span>
          <span class="wp-val">{{ row.value }}</span>
          <span class="wp-strip" aria-hidden="true"><RoundMark v-for="(outcome, r) in row.rounds" :key="r" :outcome="outcome" /><span v-if="row.more" class="wp-more">{{ row.more }}</span></span>
        </TicketPeekLink>
      </li>
      <li v-if="!tile.worst.rows.length" class="wp-empty">{{ text.wpEmpty }}</li>
    </ul>
    <div class="wp-foot">
      <p class="wp-legend" aria-hidden="true"><span v-for="outcome in legend" :key="outcome"><RoundMark :outcome="outcome" />{{ text.kinds[outcome] }}</span></p>
      <p class="wp-filters"><AppIcon name="filter" :size="13" />{{ text.filters }}</p>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.wp-grab { display: none; }
.wp-head { display: flex; align-items: flex-start; gap: 8px; padding: 6px 2px 8px 10px; }
.wp-tt { flex: 1; min-width: 0; outline: none; }
.wp-tt h3 { margin: 0; font: 650 14px/1.35 var(--font); color: var(--ink); }
.wp-tt p { margin: 1px 0 0; font-size: 12px; line-height: 1.45; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.wp-x { display: inline-grid; place-items: center; flex: none; width: 34px; height: 34px; margin: -2px 0 0; padding: 0; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); cursor: pointer; }
.wp-x:hover { background: var(--row-hover); color: var(--teal-ink); }
.wp-x:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.wp-list { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.wp-row { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; grid-template-areas: "key title val" ". strip strip"; align-items: center; gap: 4px 10px; min-height: 52px; padding: 8px 10px; border-radius: var(--radius-row); color: inherit; text-decoration: none; }
.wp-row:hover { background: var(--row-hover); }
.wp-row:focus-visible { outline: none; background: var(--row-hover); box-shadow: var(--focus-ring); }
.wp-key { grid-area: key; font: 600 11.5px/18px var(--mono); letter-spacing: .02em; color: var(--teal-ink); white-space: nowrap; }
.wp-title { grid-area: title; min-width: 0; font-size: 13px; line-height: 18px; color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.wp-val { grid-area: val; font-size: 12.5px; font-weight: 650; line-height: 18px; font-variant-numeric: tabular-nums; color: var(--ink); white-space: nowrap; }
.wp-strip { grid-area: strip; display: flex; flex-wrap: wrap; align-items: center; gap: 4px; min-height: 14px; }
.wp-more { margin-left: 2px; font-size: 11px; color: var(--ink-3); }
.wp-empty { padding: 12px 10px; font-size: 12.5px; color: var(--ink-2); }
.wp-foot { display: grid; gap: 6px; margin: 6px 10px 0; padding-top: 10px; border-top: 1px solid var(--line); }
.wp-legend { display: flex; flex-wrap: wrap; gap: 4px 14px; margin: 0; font-size: 11.5px; color: var(--ink-2); }
.wp-legend span { display: inline-flex; align-items: center; gap: 5px; white-space: nowrap; }
.wp-filters { display: flex; align-items: center; gap: 6px; margin: 0; font-size: 11.5px; color: var(--ink-3); }
.wp-filters :deep(svg) { flex: none; color: var(--ink-3); }
/* The sheet: a grip, the close button and rows at thumb size, the safe area kept below. */
.bottom .wp-grab { display: block; width: 36px; height: 4px; margin: 2px auto 2px; border-radius: 2px; background: var(--line-2); }
.bottom .wp-head { align-items: center; padding: 4px 0 6px 10px; }
.bottom .wp-tt h3 { font-size: 15px; }
.bottom .wp-x { width: 44px; height: 44px; margin: 0; }
.bottom .wp-row { min-height: 60px; padding: 10px; }
@media (pointer: coarse) { .wp-x { width: 44px; height: 44px; margin: -8px -6px -8px 0; } .wp-row { min-height: 56px; } }
</style>

<style>
/* The panel itself is FloatingPanel's: opaque, anchored or a bottom sheet. */
.floating.dl-worst.pop { padding: 6px 6px 10px; }
.floating.dl-worst.pop.bottom { padding: 6px 8px max(14px, env(safe-area-inset-bottom)); border-radius: 18px 18px 0 0; }
</style>
