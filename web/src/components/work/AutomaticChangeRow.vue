<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref } from 'vue'
import { undoEvent } from '../../lib/api'
import { automaticTarget, type AutomaticChange } from '../../lib/statusAutopilot'
import { absoluteTime, relativeTime, statusMeta } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import StatusIcon from './StatusIcon.vue'
const props = defineProps<{ change: AutomaticChange; recent?: boolean }>()
const emit = defineEmits<{ undone: [] }>()
const saving = ref(false)
const undone = ref(false)
const conflict = ref(false)
const error = ref('')
async function undo() {
  saving.value = true; error.value = ''
  try { await undoEvent(props.change.event_id); undone.value = true; emit('undone') }
  catch (e) { error.value = e instanceof Error ? e.message : 'Undo failed. Try again.'; if (/409|changed|conflict/i.test(error.value)) conflict.value = true }
  finally { saving.value = false }
}
</script>
<template>
  <div class="auto-body" :class="{ undone: change.undone || undone, recent }">
    <p class="change-line"><strong>Status autopilot</strong>
      <span class="value"><StatusIcon :state="change.from" :size="12" />{{ statusMeta(change.from).label }}</span>
      <AppIcon name="arrow" :size="11" class="arrow" /><span class="sr-only"> to </span>
      <span class="value"><AppIcon v-if="automaticTarget(change.to)" :name="change.to === 'triage_list' ? 'list' : change.to === 'blocked_reminder' ? 'clock' : 'alert'" :size="12" /><StatusIcon v-else :state="change.to" :size="12" />{{ automaticTarget(change.to) || statusMeta(change.to).label }}</span>
      <span class="sep">·</span><time :datetime="change.at" :data-tip="absoluteTime(change.at)">{{ relativeTime(change.at) }}</time>
    </p>
    <div class="reason-and-undo"><p class="reason-line">{{ change.reason }}</p><div class="undo-control">
      <span v-if="change.undone || undone" class="undone-mark">Undone · back to {{ statusMeta(change.from).label }}</span>
      <button v-else-if="change.undoable && !conflict" type="button" class="btn sm" :disabled="saving" :aria-label="`Undo: put ${change.key} back to ${statusMeta(change.from).label}`" @click="undo"><AppIcon name="rollback" :size="13" />{{ saving ? 'Undoing…' : 'Undo' }}</button>
      <span v-else class="undone-mark">{{ conflict || change.changed_since ? 'Changed since' : 'Undo unavailable' }}</span>
    </div></div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </div>
</template>
<style scoped>
.auto-body { display: grid; gap: 2px; min-width: 0; }
.change-line { display: flex; align-items: center; flex-wrap: wrap; gap: 0 4px; min-height: 22px; font-size: 12.5px; color: var(--ink-2); }
.change-line strong { color: var(--ink); font-weight: 600; }
.value { display: inline-flex; align-items: center; gap: 4px; color: var(--ink); }
.arrow, .sep, time, .undone-mark { color: var(--ink-3); }
.reason-and-undo { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; }
.reason-line { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; font-size: 12.5px; line-height: 1.45; color: var(--ink-2); }
.reason-line > span { overflow-wrap: anywhere; }
.undone .change-line { opacity: .6; }
.recent { grid-template-columns: minmax(0, 1fr) auto; }
.recent .reason-and-undo { display: contents; }
.recent .change-line, .recent .reason-line { grid-column: 1; }
.recent .undo-control { grid-column: 2; grid-row: 1 / span 2; align-self: center; }
@media (max-width: 600px) { .recent { grid-template-columns: minmax(0, 1fr); }.recent .undo-control { grid-column: 1; grid-row: auto; } }
.error { color: var(--danger); font-size: 12.5px; }
</style>
