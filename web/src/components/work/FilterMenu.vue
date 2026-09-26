<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { DIMENSIONS, type Dimension, type ListFilters } from '../../lib/ticketList'
import AppIcon, { type IconName } from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'
import StatusIcon from './StatusIcon.vue'

// "Filter": every way to narrow the list, the quick ones included, then the date.
const props = withDefaults(defineProps<{ anchor: HTMLElement | null; filters: ListFilters; dimensions?: Dimension[]; showDate?: boolean }>(), { showDate: true })
const dimensions = computed(() => DIMENSIONS.filter(d => !props.dimensions || props.dimensions.includes(d.key)))
const emit = defineEmits<{ choose: [dimension: Dimension | 'date']; close: [restoreFocus: boolean] }>()
const ICONS: Record<Dimension, IconName> = { status: 'check', priority: 'gauge', assignee: 'user', type: 'ticket', tag: 'tag', epic: 'epic', cost: 'coin', release: 'box' }
const list = ref<HTMLElement>()
function move(event: KeyboardEvent) {
  const items = [...(list.value?.querySelectorAll<HTMLButtonElement>('button') ?? [])]
  const index = items.indexOf(document.activeElement as HTMLButtonElement)
  let next = -1
  if (event.key === 'ArrowDown' || event.key === 'j') next = Math.min(items.length - 1, index + 1)
  else if (event.key === 'ArrowUp' || event.key === 'k') next = Math.max(0, index - 1)
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = items.length - 1
  if (next >= 0) { event.preventDefault(); event.stopPropagation(); items[next]?.focus() }
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="236" label="Add a filter" @close="restore => emit('close', restore)">
    <p class="eyebrow title">Filter by</p>
    <div ref="list" class="menu" role="menu" aria-label="Filter by" @keydown="move">
      <button
        v-for="(dimension, index) in dimensions" :key="dimension.key" type="button" role="menuitem" class="menu-item"
        :class="{ secondary: !dimension.primary }" :data-autofocus="index === 0 ? '' : undefined" @click="emit('choose', dimension.key)"
      >
        <StatusIcon v-if="dimension.key === 'status'" state="in_progress" :size="13" class="lead" />
        <AppIcon v-else :name="ICONS[dimension.key]" :size="14" class="lead" :class="dimension.key" />
        <span class="label">{{ dimension.title }}</span>
        <span v-if="filters[dimension.key].length" class="on-count mono">{{ filters[dimension.key].length }}</span>
        <AppIcon name="chevron-right" :size="13" class="go" />
      </button>
      <span v-if="showDate" class="divider" role="separator" />
      <button v-if="showDate" type="button" role="menuitem" class="menu-item" @click="emit('choose', 'date')">
        <AppIcon name="calendar" :size="14" class="lead" />
        <span class="label">Date</span>
        <span v-if="filters.date" class="on-count mono">1</span>
        <AppIcon name="chevron-right" :size="13" class="go" />
      </button>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.title { padding: 6px 10px 4px; }
.menu { display: grid; gap: 1px; }
.menu-item { display: flex; align-items: center; gap: 10px; height: 32px; padding: 0 8px 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
@media (hover: hover) { .menu-item:hover { background: var(--row-hover); } }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.lead { flex-shrink: 0; color: var(--ink-3); }
.lead.epic { color: var(--gold); }
.label { flex: 1; }
.on-count { display: inline-grid; place-items: center; min-width: 17px; height: 17px; padding: 0 5px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-size: 10.5px; font-weight: 700; }
.go { color: var(--ink-3); }
.divider { height: 1px; margin: 4px 6px; background: var(--line); }
</style>
