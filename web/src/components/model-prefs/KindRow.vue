<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'
import ModelChip from './ModelChip.vue'
import SetByBadge from './SetByBadge.vue'
import { canRemoveKind, resetRowLabel, type PrefChoice, type PrefLevel, type PrefViewRow, type WorkKind } from '../../lib/modelPrefs'
defineProps<{ kind: WorkKind; row: PrefViewRow; level: PrefLevel; editable: boolean; busy: boolean; ownLocked: boolean; choices?: PrefChoice[] }>()
defineEmits<{ pick: [bucket: 'normal' | 'complex']; reset: []; lock: []; archive: [] }>()
</script>
<template>
  <tr class="kind-row" :class="{ here: row.changed_here }" :data-kind="kind.slug">
    <th scope="row"><div class="kind-label"><b>{{ kind.label }}</b><button v-if="canRemoveKind(kind, level, editable)" type="button" class="icon-btn sm flat" :aria-label="`Remove ${kind.label}`" :disabled="busy" @click="$emit('archive')"><AppIcon name="close" :size="12" /></button></div><small>{{ kind.hint }}</small><small v-if="kind.system === 'review'">Never the author’s family</small><small v-if="kind.system === 'security'">Reviews: security ladder</small></th>
    <td><span class="phone-label">Normally</span><ModelChip :model="row.normal" :choices="choices" :label="`${kind.label}, normally`" :review="kind.system === 'review'" :disabled="busy || !editable || !!row.locked_by && row.locked_by !== level" @pick="$emit('pick', 'normal')" /></td>
    <td><span class="phone-label">If it’s complex</span><ModelChip :model="row.complex" :choices="choices" :label="`${kind.label}, if complex`" :review="kind.system === 'review'" :disabled="busy || !editable || !!row.locked_by && row.locked_by !== level" @pick="$emit('pick', 'complex')" /></td>
    <td class="by-cell"><SetByBadge :row="row" :level="level" :kind="kind.label" :editable="editable" :busy="busy" :own-locked="ownLocked" @reset="$emit('reset')" @lock="$emit('lock')" /></td>
    <td class="reset-cell"><button type="button" class="icon-btn sm flat row-reset" :style="{ visibility: row.changed_here ? 'visible' : 'hidden' }" :aria-label="`${kind.label}: ${resetRowLabel(level)}`" :disabled="busy || !editable || !!row.locked_by && row.locked_by !== level" @click="$emit('reset')"><AppIcon name="refresh" :size="13" /></button></td>
  </tr>
</template>
<style scoped>
.kind-row > * { padding: 8px 4px; border-bottom: 1px solid var(--line); vertical-align: middle; }
.kind-row.here { background: color-mix(in srgb, var(--lv) 7%, transparent); }
th { width: 23%; text-align: left; font-weight: 400; } th small { display: block; font-size: 11px; color: var(--ink-3); line-height: 1.3; }
.kind-label { display: flex; align-items: center; gap: 3px; min-height: 28px; } b { font-size: 13px; overflow-wrap: anywhere; } .kind-label button { flex: none; }
td { width: 26%; } .by-cell { width: 21%; } .reset-cell { width: 30px; }
.phone-label { display: none; }
@media (max-width: 600px) {
  .kind-row { display: grid; grid-template-columns: minmax(0, 1fr) 36px; grid-template-areas: 'kind kind' 'normal normal' 'complex complex' 'by reset'; gap: 8px; padding: 14px 0; border-bottom: 1px solid var(--line); }
  .kind-row > * { width: auto; padding: 0; border: 0; min-width: 0; } th { grid-area: kind; } td:nth-child(2) { grid-area: normal; } td:nth-child(3) { grid-area: complex; } .by-cell { grid-area: by; } .reset-cell { grid-area: reset; }
  .kind-label { justify-content: space-between; } .phone-label { display: block; margin-bottom: 4px; font-size: 11px; color: var(--ink-3); }
}
</style>
