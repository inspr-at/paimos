<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { vClipTip } from '../../directives/clipTip'
import { nextTick, ref } from 'vue'
import type { ListItem } from '../../lib/api'
import { statusMeta } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import PriorityIcon from './PriorityIcon.vue'
import StatusIcon from './StatusIcon.vue'

// The children of an epic (tickets) or a ticket (tasks), with progress and an
// inline add row that stays open for the next title.
const props = defineProps<{ children: ListItem[]; loading: boolean; editable: boolean; childLabel: 'work item' | 'ticket' | 'task'; progress: { done: number; total: number; percent: number } | null; progressError?: string; add: (title: string) => Promise<ListItem | null> }>()
const emit = defineEmits<{ open: [key: string] }>()
const adding = ref(false)
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const draft = ref('')
const busy = ref(false)
const input = ref<HTMLInputElement>()
async function startAdd() { adding.value = true; await nextTick(); input.value?.focus() }
async function submit() {
  const title = draft.value.trim()
  if (!title || busy.value) return
  busy.value = true
  const created = await props.add(title)
  busy.value = false
  if (created) draft.value = ''
  await nextTick(); input.value?.focus()
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'Enter' && (mac ? event.metaKey : event.ctrlKey)) { event.preventDefault(); void submit() }
  else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); if (event.target instanceof HTMLElement && event.target.matches('input')) event.target.blur(); else { adding.value = false; draft.value = '' } }
}
defineExpose({ startAdd })
</script>

<template>
  <section class="children" :aria-label="childLabel === 'work item' ? 'Child work items' : `${childLabel === 'ticket' ? 'Tickets' : 'Tasks'} in this ${childLabel === 'ticket' ? 'epic' : 'ticket'}`">
    <header class="children-head">
      <h3 class="eyebrow">{{ childLabel === 'work item' ? 'Children' : childLabel === 'ticket' ? 'Tickets' : 'Tasks' }} <span class="count">{{ children.length }}</span></h3>
      <span v-if="progress?.total" class="progress" :data-tip="`${progress.done} of ${progress.total} leaves done`"><span class="bar"><i :style="{ width: `${progress.percent}%` }" /></span><span class="mono pct">{{ progress.done }}/{{ progress.total }}</span></span>
      <span v-else-if="!progress" class="progress" role="status" :aria-label="progressError || 'Loading leaf progress…'"><span class="progress-status" :data-tip="progressError || 'Loading leaf progress…'">{{ progressError ? 'Leaf progress unavailable' : 'Loading leaf progress…' }}</span></span>
    </header>
    <div v-if="editable" class="add-row">
      <label v-if="adding" class="add-field">
        <AppIcon name="plus" :size="13" />
        <input ref="input" v-model="draft" class="add-input" :placeholder="`New ${childLabel} title`" :aria-label="`New ${childLabel} title`" :disabled="busy" @keydown="keydown" @blur="!draft && (adding = false)" />
        <KeyCap k="mod" /><KeyCap k="enter" />
      </label>
      <button v-else type="button" class="add-btn" @click="startAdd"><AppIcon name="plus" :size="13" />Add {{ childLabel }}</button>
    </div>
    <ul v-if="children.length" class="child-list">
      <li v-for="child in children" :key="child.id">
        <button type="button" class="child-row" :class="{ closed: statusMeta(child.state).closed }" @click="emit('open', child.key)">
          <StatusIcon :state="child.state" :size="13" :data-tip="statusMeta(child.state).label" />
          <span class="key">{{ child.key }}</span>
          <span v-clip-tip class="child-title">{{ child.title }}</span>
          <PriorityIcon v-if="child.priority" :priority="child.priority" :size="13" />
        </button>
      </li>
    </ul>
    <div v-else-if="loading" class="child-skeleton" aria-hidden="true"><span class="skeleton" /><span class="skeleton short" /></div>
    <p v-else class="none">No {{ childLabel }}s yet.</p>

  </section>
</template>

<style scoped>
.children-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 6px; }
.children-head .eyebrow { margin: 0; }
.count { margin-left: 4px; color: var(--ink-2); letter-spacing: 0; }
.progress { display: inline-flex; align-items: center; gap: 8px; width: 160px; }
.progress-status { font-size: 11px; white-space: nowrap; color: var(--ink-2); }
.progress .bar { flex: 1; height: 5px; }
.pct { font-size: 11px; color: var(--ink-2); }
.child-list { margin: 0; padding: 4px; list-style: none; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.child-row { display: flex; align-items: center; gap: 9px; width: 100%; height: 32px; padding: 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13px; text-align: left; }
@media (hover: hover) { .child-row:hover { background: var(--row-hover); } }
.child-row:active { background: var(--row-selected); }
.child-row:focus-visible { box-shadow: var(--focus-ring); }
.child-row.closed .child-title { color: var(--ink-2); }
.key { flex-shrink: 0; width: 88px; font: 500 11px/1 var(--mono); color: var(--ink-2); font-variant-ligatures: none; }
.child-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.none { font-size: 13px; color: var(--ink-3); }
.child-skeleton { display: grid; gap: 10px; padding: 12px; }
.child-skeleton .short { width: 60%; }
.add-row { margin-top: 6px; }
.add-btn { display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 10px; margin-left: -2px; border: 0; border-radius: 8px; background: transparent; color: var(--teal-ink); font-size: 13px; font-weight: 600; }
.add-btn:hover { background: var(--row-hover); }
.add-btn:focus-visible { box-shadow: var(--focus-ring); }
.add-field { display: flex; align-items: center; gap: 8px; height: 36px; padding: 0 10px; border-radius: 10px; background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--glass-rim); color: var(--ink-3); }
.add-field:focus-within { box-shadow: var(--focus-ring); }
.add-input { flex: 1; min-width: 0; height: 100%; border: 0; background: transparent; color: var(--ink); font-size: 13.5px; }
.add-input:focus { box-shadow: none; }
.key, .child-title { line-height: 18px; }
@media (max-width: 720px) {
  .child-row { height: auto; min-height: 44px; padding-block: 6px; align-items: flex-start; }
  .key { width: 76px; }
  .child-title { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; white-space: normal; overflow-wrap: anywhere; }
  .child-row > svg { flex-shrink: 0; margin-top: 2px; }
}
</style>
