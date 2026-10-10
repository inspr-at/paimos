<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import type { WorkKind } from '../../lib/workKinds'
import type { KindsText } from '../../lib/workKindsCopy'
const props = defineProps<{ kind: WorkKind; index: number; editable: boolean; busy: boolean; german: boolean; t: KindsText }>()
const emit = defineEmits<{ edit: [kind: WorkKind]; menu: [kind: WorkKind, anchor: HTMLElement]; move: [kind: WorkKind, direction: -1 | 1, focus: HTMLElement] }>()
const count = computed(() => props.german ? `${props.kind.ticket_count} Tickets` : `${props.kind.ticket_count} ${props.kind.ticket_count === 1 ? 'ticket' : 'tickets'}`)
function editRow(event: MouseEvent | KeyboardEvent) {
  if (!props.editable || props.busy || (event.target as HTMLElement).closest('button, a, input, textarea')) return
  emit('edit', props.kind)
}
// Alt+↑/↓ on the focused row; Everything else stays last, so it never moves.
function reorder(event: KeyboardEvent, direction: -1 | 1) {
  if (!props.editable || props.kind.system === 'other') return
  event.preventDefault()
  emit('move', props.kind, direction, event.currentTarget as HTMLElement)
}
</script>
<template>
  <li :id="`kind-${kind.slug}`" class="kind" :class="{ editable }" :data-kind="kind.slug" :tabindex="editable ? 0 : -1" :aria-label="`${kind.label}. ${kind.hint} ${count}.`" @click="editRow" @keydown.enter.self.prevent="editRow" @keydown.up.alt.exact.self="reorder($event, -1)" @keydown.down.alt.exact.self="reorder($event, 1)">
    <span class="kind-n" aria-hidden="true"><AppIcon v-if="kind.system === 'other'" name="minus" :size="12" /><template v-else>{{ index + 1 }}</template></span>
    <span class="kind-t"><b class="kind-name">{{ kind.label }}</b><span class="kind-def">{{ kind.hint }}</span></span>
    <span class="kind-c">{{ count }}</span>
    <span v-if="editable" class="kind-a">
      <button type="button" class="btn ghost sm" :aria-label="`${t('edit')} ${kind.label}`" :disabled="busy" @click="emit('edit', props.kind)"><AppIcon name="edit" :size="14" /><span class="kind-el">{{ t('edit') }}</span></button>
      <button type="button" class="icon-btn sm" aria-haspopup="menu" :aria-label="`${kind.label}: ${t('moreActions')}`" :disabled="busy" @click="emit('menu', props.kind, $event.currentTarget as HTMLElement)"><AppIcon name="more" :size="16" /></button>
    </span>
  </li>
</template>
<style scoped>
.kind { display: grid; grid-template-columns: 24px minmax(0, 1fr) auto auto; align-items: center; gap: 12px; min-height: 64px; padding: 10px 8px; border-top: 1px solid var(--line); scroll-margin-top: 20px; }
.kind:first-child { border-top: 0; }
.kind.editable { cursor: pointer; }
.kind:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 10px; }
@media (hover: hover) { .kind.editable:hover { background: var(--row-hover); } }
.kind-n { display: inline-grid; place-items: center; width: 24px; height: 24px; border-radius: 6px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font: 600 11px/1 var(--mono); color: var(--ink-2); }
.kind-t { display: grid; gap: 2px; min-width: 0; }
.kind-name { font-size: 14px; font-weight: 650; overflow-wrap: anywhere; }
.kind-def { font-size: 13px; line-height: 1.45; color: var(--ink-2); overflow-wrap: anywhere; }
.kind-c { font-size: 12px; color: var(--ink-3); white-space: nowrap; }
.kind-a { display: inline-flex; align-items: center; gap: 4px; }
@container body (max-width: 640px) {
  .kind { grid-template-columns: 24px minmax(0, 1fr) auto; align-items: start; }
  .kind-n { margin-top: 2px; }
  .kind-c { grid-column: 2; grid-row: 2; }
  .kind-a { grid-column: 3; grid-row: 1 / 3; }
  .kind-el { display: none; }
}
@media (pointer: coarse), (max-width: 720px) { .kind-a .btn, .kind-a .icon-btn { min-width: 44px; min-height: 44px; justify-content: center; } }
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
