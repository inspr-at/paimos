<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import type { AgentAccount, AllowanceWrite } from '../../lib/agents'
import { accountName } from '../../lib/accountCascade'
import AppIcon from '../AppIcon.vue'
import {
  emptyAllowanceDraft, formatInstant, localZone, PACE_OPTIONS, reviewAllowance, UNIT_OPTIONS,
  type AllowanceDraft,
} from './allowanceWindow'

const props = defineProps<{
  account: AgentAccount
  now: number
  busy: boolean
  serverMessage: string
  uncertain: boolean
  pending: AllowanceWrite | null
}>()
const emit = defineEmits<{ save: [body: AllowanceWrite]; cancel: []; check: []; revise: [] }>()

const draft = ref<AllowanceDraft>(emptyAllowanceDraft())
const submitted = ref(false)
const startInput = ref<HTMLInputElement | null>(null)
const alertEl = ref<HTMLElement | null>(null)
const zone = localZone()
const name = computed(() => accountName(props.account))
const review = computed(() => reviewAllowance(draft.value, props.account.windows ?? [], props.now))
const problem = computed(() => props.serverMessage || (submitted.value && !review.value.ok ? review.value.message : ''))
const describedBy = computed(() => problem.value ? `${props.account.id}-allowance-error` : undefined)

onMounted(() => { if (!props.uncertain) startInput.value?.focus() })
watch(problem, async value => {
  if (!value) return
  await nextTick()
  alertEl.value?.focus()
})
watch(() => props.account.id, () => {
  draft.value = emptyAllowanceDraft()
  submitted.value = false
})

function assign(patch: Partial<AllowanceDraft>) {
  draft.value = { ...draft.value, ...patch }
  if (props.uncertain || props.serverMessage) emit('revise')
}
function onSubmit() {
  submitted.value = true
  if (props.busy || props.uncertain || !review.value.ok) return
  emit('save', review.value.body)
}
</script>

<template>
  <form class="allowance" :aria-labelledby="`${account.id}-allowance-title`" @submit.prevent="onSubmit">
    <h3 :id="`${account.id}-allowance-title`">Allowance window for {{ name }}</h3>
    <p class="hint">Enter the usage limit you want to allow for this account.</p>
    <dl class="preview">
      <div><dt>Account</dt><dd>{{ name }}</dd></div>
      <div><dt>Time zone</dt><dd>{{ zone }}</dd></div>
      <template v-if="review.ok">
        <div><dt>Starts</dt><dd><time :datetime="review.startsAt">{{ formatInstant(review.startsAt, zone) }}</time><span class="instant">{{ review.startsAt }}</span></dd></div>
        <div><dt>Ends</dt><dd><time :datetime="review.endsAt">{{ formatInstant(review.endsAt, zone) }}</time><span class="instant">{{ review.endsAt }}</span></dd></div>
      </template>
    </dl>
    <p v-if="!review.ok" class="hint">The exact start and end appear here, in this time zone, before the window can be saved.</p>
    <p v-else-if="review.startsInPast" class="hint">The start is already in the past. It will be saved as shown, not moved forward.</p>
    <p v-if="review.ok && review.overlap" class="hint">This overlaps an existing {{ review.body.unit.replace('_', ' ') }} window on this account. Saving sends it once. The server refuses the overlap, and it is not sent again.</p>
    <p v-if="problem" :id="`${account.id}-allowance-error`" ref="alertEl" class="problem" role="alert" tabindex="-1"><AppIcon name="alert" :size="13" />{{ problem }}</p>
    <template v-if="uncertain">
      <p v-if="pending" class="hint">Unconfirmed request: <time :datetime="pending.starts_at">{{ formatInstant(pending.starts_at, zone) }}</time> to <time :datetime="pending.ends_at">{{ formatInstant(pending.ends_at, zone) }}</time>, {{ pending.allowance }} {{ pending.unit.replace('_', ' ') }}.</p>
      <div class="actions">
        <button type="button" class="btn sm" :disabled="busy" @click="emit('check')">Check again</button>
        <button type="button" class="btn sm ghost" :disabled="busy" @click="emit('revise')">Enter a different window</button>
        <button type="button" class="btn sm ghost" @click="emit('cancel')">Cancel</button>
      </div>
    </template>
    <template v-else>
      <div class="grid">
        <label>Starts (your local time)
          <input ref="startInput" class="field" type="datetime-local" :value="draft.startsLocal" required :aria-invalid="problem ? true : undefined" :aria-describedby="describedBy" @input="assign({ startsLocal: ($event.target as HTMLInputElement).value })" />
        </label>
        <label>Ends (your local time)
          <input class="field" type="datetime-local" :value="draft.endsLocal" required :aria-invalid="problem ? true : undefined" :aria-describedby="describedBy" @input="assign({ endsLocal: ($event.target as HTMLInputElement).value })" />
        </label>
        <label>Allowance
          <input class="field" type="number" min="1" step="1" :value="draft.allowance ?? ''" required :aria-invalid="problem ? true : undefined" :aria-describedby="describedBy" @input="assign({ allowance: ($event.target as HTMLInputElement).value === '' ? null : Number(($event.target as HTMLInputElement).value) })" />
        </label>
        <label>Unit
          <select class="field" :value="draft.unit" @change="assign({ unit: ($event.target as HTMLSelectElement).value as AllowanceDraft['unit'] })">
            <option v-for="option in UNIT_OPTIONS" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>
        </label>
        <label>Pace
          <select class="field" :value="draft.pace" @change="assign({ pace: ($event.target as HTMLSelectElement).value as AllowanceDraft['pace'] })">
            <option v-for="option in PACE_OPTIONS" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>
        </label>
        <label>Burst ratio
          <input class="field" type="number" min="0" max="1" step="0.0001" :value="draft.burst ?? ''" required :aria-invalid="problem ? true : undefined" :aria-describedby="describedBy" @input="assign({ burst: ($event.target as HTMLInputElement).value === '' ? null : Number(($event.target as HTMLInputElement).value) })" />
        </label>
      </div>
      <p class="hint">Cost in micros counts millionths of a US dollar: 1,000,000 is 1 USD. Burst ratio, from 0 to 1, is how far usage may run ahead of a steady or front-loaded pace.</p>
      <div class="actions">
        <button type="submit" class="btn sm primary" :disabled="busy" :aria-busy="busy">{{ busy ? 'Saving…' : 'Save allowance window' }}</button>
        <button type="button" class="btn sm ghost" @click="emit('cancel')">Cancel</button>
      </div>
    </template>
    <p class="sr-only" role="status">{{ busy ? 'Saving allowance window' : '' }}</p>
  </form>
</template>

<style scoped>
.allowance { margin-top: 8px; padding: 10px; border-radius: 10px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.allowance h3 { margin: 0; font: 600 13px/1.3 var(--font); color: var(--ink); }
.hint, .problem { margin: 6px 0 0; font-size: 12px; color: var(--ink-2); }
.problem { display: flex; align-items: flex-start; gap: 6px; color: var(--danger); }
.problem:focus { outline: none; box-shadow: var(--focus-ring); border-radius: 6px; }
.preview { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px 12px; margin: 8px 0 0; }
.preview div { min-width: 0; }
.preview dt { color: var(--ink-3); font-size: 11px; font-weight: 600; }
.preview dd { margin: 1px 0 0; color: var(--ink); font-size: 12.5px; font-weight: 600; overflow-wrap: anywhere; }
.instant { display: block; margin-top: 1px; color: var(--ink-3); font: 500 11px/1.3 var(--mono); font-weight: 500; font-variant-numeric: tabular-nums; }
.grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px 10px; margin-top: 8px; }
label { display: grid; gap: 4px; min-width: 0; color: var(--ink-2); font-size: 12px; font-weight: 600; }
.actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 10px; }
@media (max-width: 640px) {
  .preview, .grid { grid-template-columns: minmax(0, 1fr); }
  .actions .btn { min-height: 44px; }
}
</style>
