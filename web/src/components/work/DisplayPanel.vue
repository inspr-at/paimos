<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { GROUPS, type GroupBy, type ListFilters } from '../../lib/ticketList'
import type { ColumnId } from '../../lib/columns'
import type { SortKey } from '../../lib/work'
import { useModelDisplay } from '../../lib/prefs'
import AppIcon from '../AppIcon.vue'
import ColumnPicker from './ColumnPicker.vue'
import SortEditor from './SortEditor.vue'
import HeaderRoomyChoice from './HeaderRoomyChoice.vue'

// The Display menu: grouping, sort, row height and columns of this list. In a
// saved view these are part of the view; otherwise columns are the person's own.
defineProps<{
  filters: ListFilters
  view: 'list' | 'outline' | 'journey'
  density: 'comfortable' | 'compact'
  columns?: { order: ColumnId[]; visible: ColumnId[]; customised: boolean; notes?: Partial<Record<string, string>> } | null
  grouped?: boolean
  headerGraph?: boolean
  sheet?: boolean
  projectHeader?: boolean
}>()
// The header graph is on until a person turns it off. Callers that omit the
// prop keep that default so the switch does not flash off.
const { modelDisplay, set: setModelDisplay } = useModelDisplay()
const emit = defineEmits<{
  group: [value: GroupBy]; sort: [keys: SortKey[]]; density: [value: 'comfortable' | 'compact']
  columns: [order: ColumnId[], visible: ColumnId[]]; columnsReset: []
  expandAll: []; collapseAll: []; expandGroups: []; collapseGroups: []
  headerGraph: [value: boolean]
}>()
</script>

<template>
  <div class="display-panel" :class="{ sheet }">
    <template v-if="view === 'list'">
      <p class="eyebrow">Group by</p>
      <div class="group-grid" role="radiogroup" aria-label="Group by">
        <button
          v-for="option in GROUPS" :key="option.value" type="button" role="radio" class="group-option" :aria-checked="filters.group === option.value"
          :data-autofocus="filters.group === option.value ? '' : undefined" @click="emit('group', option.value)"
        >{{ option.label }}</button>
      </div>
      <div class="pair">
        <button type="button" class="btn sm" :disabled="!grouped" @click="emit('expandGroups')"><AppIcon name="expand-all" :size="13" />Expand groups</button>
        <button type="button" class="btn sm" :disabled="!grouped" @click="emit('collapseGroups')"><AppIcon name="collapse-all" :size="13" />Collapse groups</button>
      </div>
    </template>
    <template v-else>
      <p class="eyebrow">Outline</p>
      <div class="pair">
        <button type="button" class="btn sm" data-autofocus @click="emit('expandAll')"><AppIcon name="expand-all" :size="13" />Expand all</button>
        <button type="button" class="btn sm" @click="emit('collapseAll')"><AppIcon name="collapse-all" :size="13" />Collapse all</button>
      </div>
    </template>
    <SortEditor class="section" :sort="filters.sort" :stable="sheet" @change="keys => emit('sort', keys)" />
    <div class="section">
      <p class="eyebrow">Row height</p>
      <div class="seg wide" role="radiogroup" aria-label="Row height">
        <button type="button" role="radio" :aria-checked="density === 'comfortable'" @click="emit('density', 'comfortable')"><AppIcon name="rows-comfortable" :size="14" />Comfortable</button>
        <button type="button" role="radio" :aria-checked="density === 'compact'" @click="emit('density', 'compact')"><AppIcon name="rows-compact" :size="14" />Compact</button>
      </div>
    </div>
    <HeaderRoomyChoice v-if="projectHeader" class="section" />
    <ColumnPicker v-if="columns" class="section" :order="columns.order" :visible="columns.visible" :customised="columns.customised" :notes="columns.notes" :reserve-notes="sheet" @change="(order, visible) => emit('columns', order, visible)" @reset="emit('columnsReset')" />
    <div class="section model-display">
      <div class="model-choice"><span>Effort meter</span><div class="seg" role="radiogroup" aria-label="Effort meter">
        <button v-for="on in [true, false]" :key="String(on)" type="button" role="radio" :aria-checked="modelDisplay.effortMeter === on" @click="setModelDisplay('effortMeter', on)">{{ on ? 'On' : 'Off' }}</button>
      </div></div>
      <div class="model-choice"><span>Model names</span><div class="seg" role="radiogroup" aria-label="Model names">
        <button v-for="name in ['full', 'short'] as const" :key="name" type="button" role="radio" :aria-checked="modelDisplay.modelNames === name" @click="setModelDisplay('modelNames', name)">{{ name === 'full' ? 'Full' : 'Short' }}</button>
      </div></div>
      <div class="model-choice"><span>Version</span><div class="seg" role="radiogroup" aria-label="Version">
        <button v-for="version in ['show', 'hide'] as const" :key="version" type="button" role="radio" :aria-checked="modelDisplay.modelVersion === version" @click="setModelDisplay('modelVersion', version)">{{ version === 'show' ? 'Show' : 'Hide' }}</button>
      </div></div>
    </div>
    <div class="section">
      <label class="switch">
        <input type="checkbox" :checked="headerGraph !== false" @change="emit('headerGraph', ($event.target as HTMLInputElement).checked)" />
        <span>Graph in project header</span>
      </label>
    </div>
  </div>
</template>

<style scoped>
.model-display { display: grid; gap: 8px; }
.model-choice { display: flex; align-items: center; justify-content: space-between; gap: 12px; color: var(--ink-2); font-size: 12.5px; }
.model-choice .seg { display: grid; grid-template-columns: repeat(2, 1fr); min-width: 116px; }
.display-panel { display: grid; gap: 8px; padding: 6px 8px 8px; }
.group-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 4px; padding: 3px; border-radius: 12px; background: var(--seg-bg); }
.group-option { height: 28px; padding: 0 4px; border: 0; border-radius: 9px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.group-option:hover { color: var(--ink); }
.group-option[aria-checked="true"] { background: var(--seg-on); color: var(--ink); font-weight: 600; box-shadow: var(--shadow-btn); }
.group-option:focus-visible { box-shadow: var(--focus-ring); }
.pair { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px; }
.pair .btn { min-width: 0; padding-inline: 4px; gap: 4px; font-size: 12px; }
.section { margin-top: 6px; padding-top: 10px; border-top: 1px solid var(--line); }
.seg.wide { display: grid; grid-auto-flow: column; grid-auto-columns: 1fr; }
.seg.wide button { height: 30px; }
/* In a sheet, sort keys grow last, away from the fixed choices above them. */
.sheet .sort-editor { order: 1; }
.sheet .group-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.sheet .group-option, .sheet .pair .btn, .sheet .seg button { height: 44px; }
.sheet :deep(.head) { min-height: 44px; }
.sheet :deep(.reset), .sheet :deep(.add), .sheet :deep(.sort-field), .sheet :deep(.icon-btn) { height: 44px; }
.sheet :deep(.sort-editor .icon-btn) { width: 44px; flex: none; }
.sheet :deep(.row:not(.noted)) { height: 44px; }
.sheet :deep(.moves .icon-btn) { width: 44px; flex: none; }
.sheet :deep(.moves) { opacity: 1; }
.sheet :deep(.fine) { display: none; }
</style>
