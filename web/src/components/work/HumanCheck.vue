<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import type { ListItem } from '../../lib/api'
import type { SaveResult } from '../../lib/useTicket'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import PersonStepOwner from './PersonStepOwner.vue'
import { usePersonSteps } from '../../lib/personSteps'
import { useDecisionDesk } from '../../stores/decisionDesk'

const props = defineProps<{ item: ListItem; editable: boolean; save: (text: string | null) => Promise<SaveResult>; names: Map<string, string> }>()
const session = useSession(), steps = usePersonSteps(() => props.item), desk = useDecisionDesk()
const step = computed(() => steps.data.value?.steps.find(value => value.check_id === 'human'))
const mayTick = computed(() => props.editable && person.value && step.value?.can_tick === true)
const mine = computed(() => step.value?.owner.id === session.identity?.principal.id)
const busy = ref(false)
const checkButton = ref<HTMLButtonElement>()
const undoButton = ref<HTMLButtonElement>()
const pending = computed(() => props.item.human_check?.trim() ?? '')
const completed = computed(() => {
  const value = props.item.fields.human_check_completed as { text?: unknown; by?: unknown } | undefined
  return value && typeof value.text === 'string' && typeof value.by === 'string' ? { text: value.text, by: value.by } : null
})
const checker = computed(() => completed.value?.by === session.identity?.principal.id ? 'you' : props.names.get(completed.value?.by ?? '') ?? 'a person')
const person = computed(() => session.identity?.principal.kind === 'person')
async function set(text: string | null) {
  if (busy.value || !mayTick.value) return
  busy.value = true
  let result: SaveResult = 'error'
  try { result = await props.save(text) } finally { busy.value = false }
  if (result === 'ok') {
    void desk.refresh()
    await nextTick()
    ;(text === null ? undoButton.value : checkButton.value)?.focus({ preventScroll: true })
  }
}
</script>

<template>
  <div v-if="pending || completed" class="human-check" :class="{ done: !pending }" role="note" :aria-label="pending ? 'Needs a human check' : 'Human check done'">
    <AppIcon :name="pending ? 'person-check' : 'check'" :size="16" class="hc-icon" />
    <div class="hc-text">
      <span v-if="pending"><strong>Needs a human check:</strong> {{ pending }}</span>
      <span v-else><strong>Human check done</strong> by {{ checker }}: {{ completed?.text }}</span>
      <span class="hc-sub">Automatic moves to Delivered and Accepted skip this ticket until it is checked.<template v-if="step && !mayTick"> {{ step.owner.name }} or a project admin ticks this.</template></span>
      <PersonStepOwner v-if="step" :step="step" :disabled="busy" @changed="result => { steps.data.value = result; void desk.refresh() }" />
      <span v-else class="hc-owner-pending" role="status">{{ steps.error.value || 'Reading who checks this…' }}<button v-if="steps.error.value" type="button" class="link-btn" @click="steps.refresh()">Retry</button></span>
    </div>
    <button v-if="mayTick && pending" ref="checkButton" type="button" class="btn sm" :class="{ primary: mine }" :disabled="busy" @click="set(null)">Mark checked</button>
    <button v-else-if="mayTick && completed" ref="undoButton" type="button" class="btn sm ghost" :disabled="busy" @click="set(completed!.text)">Undo</button>
  </div>
</template>

<style scoped>
.human-check { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 4px 10px; margin-top: 12px; padding: 9px 10px 9px 12px; border-radius: 10px; background: var(--gold-wash); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--gold) 28%, transparent); }
.hc-owner-pending { min-height: 18px; color: var(--ink-2); font-size: 12px; }
.human-check > .btn { align-self: start; }
.hc-icon { color: var(--warn-ink); align-self: start; margin-top: 2px; }
.hc-text { display: grid; min-width: 0; font-size: 13px; color: var(--ink); line-height: 1.45; overflow-wrap: anywhere; }
.hc-text strong { font-weight: 600; }
.hc-sub { font-size: 12px; color: var(--ink-2); }
.done { background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); }
.done .hc-icon { color: var(--ok); }
@media (max-width: 600px) { .human-check { grid-template-columns: auto minmax(0, 1fr); } .human-check .btn { grid-column: 2; justify-self: start; } }
</style>
