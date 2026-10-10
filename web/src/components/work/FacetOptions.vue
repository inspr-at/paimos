<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { vClipTip } from '../../directives/clipTip'
import { optionState, type Dimension, type FacetOption } from '../../lib/ticketList'
import { workIconChoice } from '../../lib/workVocabulary'
import AppIcon from '../AppIcon.vue'
import PersonAvatar from './PersonAvatar.vue'
import PriorityIcon from './PriorityIcon.vue'
import StatusIcon from './StatusIcon.vue'

// Checkbox rows for one filter. A value is included (checked), excluded ("not",
// the button at the end or the minus key) or neither; see ticketList.ts. An
// option that stands for several values is mixed while only some are chosen.
const props = withDefaults(defineProps<{ dimension: Dimension; options: FacetOption[]; selected: string[]; excludable?: boolean }>(), { excludable: true })
const state = (option: FacetOption) => optionState(props.selected, option)
const emit = defineEmits<{ toggle: [value: string]; exclude: [value: string] }>()
// Keep keyboard focus on the option just toggled, also when its label text was clicked.
function changed(event: Event, value: string) {
  (event.target as HTMLInputElement).focus({ preventScroll: true })
  emit('toggle', value)
}
function keydown(event: KeyboardEvent, value: string, excludable: boolean) {
  if (excludable && (event.key === '-' || event.key === 'x' || event.key === '!')) {
    event.preventDefault(); event.stopPropagation()
    emit('exclude', value)
  }
}
function move(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'j', 'k'].includes(event.key)) return
  const root = event.currentTarget as HTMLElement
  const items = [...root.querySelectorAll<HTMLInputElement>('input')]
  const index = items.indexOf(document.activeElement as HTMLInputElement)
  const next = event.key === 'ArrowDown' || event.key === 'j' ? Math.min(items.length - 1, index + 1) : Math.max(0, index - 1)
  event.preventDefault(); event.stopPropagation()
  items[next]?.focus()
}
</script>

<template>
  <div class="facet-options" role="group" @keydown="move">
    <div
      v-for="option in options" :key="option.value" class="facet-option"
      :class="{ muted: option.count === 0 && !state(option), out: state(option) === 'out' }"
    >
      <label class="facet-main">
        <input
          class="check-box" type="checkbox" :checked="state(option) === 'in'" :indeterminate="state(option) === 'mixed'"
          :aria-describedby="state(option) === 'out' ? `not-${dimension}` : state(option) === 'mixed' ? `some-${dimension}` : undefined"
          @change="changed($event, option.value)" @keydown="keydown($event, option.value, excludable)"
        />
        <AppIcon v-if="dimension === 'status' && option.value === 'queued'" name="queue" :size="14" class="queue-glyph" /><StatusIcon v-else-if="dimension === 'status'" :state="option.value" />
        <template v-else-if="dimension === 'priority'"><PriorityIcon v-if="option.value !== 'none'" :priority="option.value" /><span v-else class="no-icon" /></template>
        <template v-else-if="dimension === 'assignee'"><PersonAvatar v-if="option.value !== 'none'" :id="option.value" :name="option.label" :size="18" /><AppIcon v-else name="user" :size="14" class="faint" /></template>
        <AppIcon v-else-if="dimension === 'type'" :name="workIconChoice(option.icon ?? '', option.value === 'leaf' ? 'ticket' : 'epic')" :size="14" class="kind" :class="{ epic: option.value !== 'leaf' }" />
        <template v-else-if="dimension === 'tag'"><i v-if="option.value !== 'none'" class="tag-dot" :data-color="option.color || undefined" aria-hidden="true" /><AppIcon v-else name="tag" :size="14" class="faint" /></template>
        <AppIcon v-else-if="dimension === 'epic'" name="epic" :size="14" :class="option.value === 'none' ? 'faint' : 'kind epic'" />
        <AppIcon v-else-if="dimension === 'cost'" name="coin" :size="14" class="faint" />
        <AppIcon v-else-if="dimension === 'human_check'" name="person-check" :size="14" class="faint" />
        <AppIcon v-else name="box" :size="14" class="faint" />
        <span v-if="state(option) === 'out'" class="not-tag">not</span>
        <span v-clip-tip class="option-label">{{ option.label }}</span>
        <span v-if="option.hint" class="hint mono">{{ option.hint }}</span>
        <span v-if="option.count !== undefined" class="count mono">{{ option.count }}</span>
      </label>
      <button
        v-if="excludable" type="button" class="not-btn" tabindex="-1" :aria-pressed="state(option) === 'out'"
        :aria-label="`Exclude ${option.label}`" :data-tip="state(option) === 'out' ? 'Excluded · click to stop' : 'Exclude · minus key'"
        @click="emit('exclude', option.value)"
      ><AppIcon name="not" :size="13" /></button>
    </div>
    <span :id="`not-${dimension}`" class="sr-only">Excluded</span>
    <span :id="`some-${dimension}`" class="sr-only">Partly chosen</span>
  </div>
</template>

<style scoped>
.queue-glyph { color: var(--teal-ink); }
.facet-options { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1px; }
.facet-option { position: relative; display: flex; align-items: center; min-height: 32px; border-radius: 8px; }
.facet-main { display: flex; align-items: center; gap: 10px; flex: 1; min-width: 0; min-height: inherit; padding: 0 6px 0 10px; font-size: 13.5px; cursor: pointer; }
@media (hover: hover) { .facet-option:hover { background: var(--row-hover); } }
.facet-option:focus-within { background: var(--row-selected); }
.facet-option:active { background: var(--row-selected); }
.facet-option.muted .option-label { color: var(--ink-3); }
.option-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.facet-option.out .option-label { color: var(--ink-2); }
.not-tag { flex-shrink: 0; height: 17px; padding: 0 6px; border-radius: 999px; background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); font: 600 10px/17px var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.hint { flex-shrink: 0; font-size: 11px; color: var(--ink-3); }
.count { flex-shrink: 0; min-width: 18px; text-align: right; font-size: 11.5px; color: var(--ink-3); }
.facet-option:focus-within .count, .facet-option:focus-within .hint { color: var(--ink-2); }
.no-icon { width: 14px; height: 2px; border-radius: 2px; background: var(--line-2); }
.kind, .faint { flex-shrink: 0; color: var(--ink-3); }
.kind.epic { color: var(--kind-parent); }
.not-btn { display: grid; place-items: center; flex-shrink: 0; width: 26px; height: 26px; margin-right: 3px; padding: 0; border: 0; border-radius: 7px; background: transparent; color: var(--ink-3); opacity: 0; }
.facet-option:hover .not-btn, .facet-option:focus-within .not-btn, .not-btn[aria-pressed="true"] { opacity: 1; }
.not-btn:hover { background: var(--chip-bg); color: var(--ink); }
.not-btn[aria-pressed="true"] { color: var(--danger); background: var(--danger-bg); }
@media (hover: none) { .not-btn { opacity: 1; } }
.tag-dot { flex-shrink: 0; width: 8px; height: 8px; margin: 0 3px; border-radius: 50%; background: var(--ink-3); }
.tag-dot[data-color="blue"] { background: var(--label-blue); }
.tag-dot[data-color="red"] { background: var(--label-red); }
.tag-dot[data-color="green"] { background: var(--label-green); }
.tag-dot[data-color="yellow"], .tag-dot[data-color="orange"] { background: var(--label-yellow); }
.tag-dot[data-color="purple"] { background: var(--label-purple); }
.tag-dot[data-color="teal"], .tag-dot[data-color="cyan"] { background: var(--label-teal); }
.tag-dot[data-color="pink"] { background: var(--label-pink); }

@media (max-width: 720px) {
  .facet-option { height: 52px; min-height: 52px; }
  .option-label { line-height: 18px; }
  .option-label { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; white-space: normal; overflow-wrap: anywhere; }
}
</style>
