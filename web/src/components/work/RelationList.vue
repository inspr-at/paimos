<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { RELATION_ORDER } from '../../lib/relations'
import type { RelatedNode } from '../../lib/useTicket'
import { statusMeta } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import StatusIcon from './StatusIcon.vue'

// Blocks, blocked by, relates to and the rest: key chips grouped by how they
// relate, each opening the other ticket. Where the ticket can be changed, Link
// (r) adds one and each chip has a remove button; Delete on a chip removes it
// too. The caller confirms and offers Undo; focus stays in the list.
// editable offers Link (relations.write); removable offers removal (relations.delete)
// and follows editable when a host does not say otherwise.
const props = defineProps<{ related: RelatedNode[]; editable?: boolean; removable?: boolean; unlink?: (entry: RelatedNode) => Promise<boolean> }>()
const canRemove = computed(() => props.removable ?? props.editable)
const emit = defineEmits<{ open: [key: string]; link: [anchor: HTMLElement] }>()
const root = ref<HTMLElement>()
const groups = computed(() => {
  const map = new Map<string, RelatedNode[]>()
  for (const entry of props.related) { if (!map.has(entry.label)) map.set(entry.label, []); map.get(entry.label)!.push(entry) }
  return [...map.entries()].sort((a, b) => RELATION_ORDER.indexOf(a[0]) - RELATION_ORDER.indexOf(b[0]))
})
const flat = computed(() => groups.value.flatMap(([, entries]) => entries))
const nameOf = (entry: RelatedNode) => entry.node?.key ?? 'an unavailable ticket'

async function remove(entry: RelatedNode) {
  if (!canRemove.value || !props.unlink) return
  const index = flat.value.indexOf(entry)
  if (!(await props.unlink(entry))) return
  // The chip is gone: its neighbour takes focus, or Link when none is left.
  await nextTick()
  const chips = root.value?.querySelectorAll<HTMLElement>('.rel-open')
  const next = chips?.[Math.min(index, chips.length - 1)]
  ;(next ?? root.value?.querySelector<HTMLElement>('.link-btn'))?.focus()
}
function chipKey(event: KeyboardEvent, entry: RelatedNode) {
  if (event.key !== 'Delete' && event.key !== 'Backspace') return
  if (!props.editable) return
  event.preventDefault()
  void remove(entry)
}
defineExpose({ el: root })
</script>

<template>
  <section v-if="related.length || editable" ref="root" class="relations" aria-label="Relations">
    <div class="rel-head">
      <h3 class="eyebrow">Relations</h3>
      <button v-if="editable" type="button" class="link-btn" aria-haspopup="dialog" aria-keyshortcuts="r" data-tip="Link to another ticket · r" @click="emit('link', $event.currentTarget as HTMLElement)">
        <AppIcon name="plus" :size="12" /><span>Link</span>
      </button>
    </div>
    <p v-if="!related.length" class="empty">No links yet. Blockers, duplicates and related work show here.</p>
    <div v-for="[label, entries] in groups" :key="label" class="relation-group">
      <span class="relation-label">{{ label }}</span>
      <span class="chips">
        <span
          v-for="entry in entries" :key="entry.relation.id" class="rel-chip"
          :class="{ closed: entry.node && statusMeta(entry.node.state).closed, blocker: entry.node && label === 'Blocked by' && !statusMeta(entry.node.state).closed, missing: !entry.node, removable: canRemove }"
        >
          <button
            v-if="entry.node" type="button" class="rel-open" :data-tip="`${entry.node.key} · ${statusMeta(entry.node.state).label}\n${entry.node.title}`"
            :aria-label="`${label} ${entry.node.key}: ${entry.node.title}`" :aria-keyshortcuts="canRemove ? 'Delete' : undefined"
            @click="emit('open', entry.node.key)" @keydown="chipKey($event, entry)"
          >
            <StatusIcon :state="entry.node.state" :size="11" /><span class="rel-key">{{ entry.node.key }}</span>
          </button>
          <span v-else class="rel-open unavailable" data-tip="This ticket is not available">Unavailable</span>
          <button v-if="canRemove" type="button" class="rel-remove" :aria-label="`Remove link: ${label} ${nameOf(entry)}`" data-tip="Remove link" @click="remove(entry)">
            <AppIcon name="close" :size="10" />
          </button>
        </span>
      </span>
    </div>
  </section>
</template>

<style scoped>
.rel-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 26px; margin: 0 0 6px; }
.rel-head .eyebrow { margin: 0; }
.link-btn { display: inline-flex; align-items: center; gap: 5px; height: 26px; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); font-size: 12px; font-weight: 600; }
@media (hover: hover) { .link-btn:hover { color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--glass-rim); background: var(--row-hover); } }
.link-btn:focus-visible { box-shadow: var(--focus-ring); }
.empty { font-size: 12.5px; color: var(--ink-3); line-height: 1.45; }
.relation-group { display: flex; align-items: baseline; gap: 12px; min-height: 30px; }
.relation-label { flex-shrink: 0; width: 96px; font-size: 12.5px; color: var(--ink-2); }
.chips { display: flex; flex-wrap: wrap; gap: 6px; padding-bottom: 4px; }
.rel-chip { display: inline-flex; align-items: center; height: 24px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); }
.rel-open { display: inline-flex; align-items: center; gap: 6px; height: 100%; padding: 0 9px 0 7px; border: 0; border-radius: 999px; background: transparent; color: var(--ink); font: 500 11.5px/1 var(--mono); font-variant-ligatures: none; white-space: nowrap; }
.removable .rel-open { padding-right: 4px; }
@media (hover: hover) { button.rel-open:hover { background: var(--row-hover); } .rel-chip:has(button.rel-open:hover) { box-shadow: inset 0 0 0 1px var(--glass-rim); } }
.rel-open:focus-visible, .rel-remove:focus-visible { box-shadow: var(--focus-ring); }
.closed .rel-key { color: var(--ink-3); text-decoration: line-through; text-decoration-color: var(--line-2); }
.rel-chip.blocker { box-shadow: inset 0 0 0 1px var(--danger-line); }
.unavailable { color: var(--ink-3); font-family: var(--font); cursor: default; }
.rel-remove { display: inline-grid; place-items: center; width: 22px; height: 22px; margin-right: 1px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: var(--ink-3); }
@media (hover: hover) { .rel-remove:hover { color: var(--danger); background: var(--danger-bg); } }
/* Phones and touch: the same chips, with a full finger's reach. */
@media (max-width: 600px), (pointer: coarse) {
  .link-btn { height: 40px; padding: 0 14px; font-size: 13px; }
  .rel-chip { height: 40px; }
  .rel-open { padding: 0 10px 0 12px; font-size: 12.5px; }
  .removable .rel-open { padding-right: 6px; }
  .rel-remove { width: 36px; height: 36px; margin-right: 2px; }
  .relation-label { line-height: 40px; }
}
</style>
