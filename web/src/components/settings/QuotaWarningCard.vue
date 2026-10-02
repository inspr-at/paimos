<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api } from '../../lib/api'
import { validQuotaThresholds, type QuotaWarningSettings } from '../../lib/quotaWarnings'
import { useSession } from '../../stores/session'
import SettingsCard from './SettingsCard.vue'
import KeyCap from '../KeyCap.vue'
const session = useSession()
const identity = computed(() => `${session.identity?.tenant.id ?? ''}/${session.identity?.principal.id ?? ''}`)
const early = ref(10), urgent = ref(3), ready = ref(false), saving = ref(false), feedback = ref('')
let generation = 0
onBeforeUnmount(() => { generation++ })
const valid = computed(() => validQuotaThresholds({ early_percent: Number(early.value), urgent_percent: Number(urgent.value) }))
watch(identity, async () => {
  const turn = ++generation
  ready.value = false; saving.value = false; feedback.value = ''; early.value = 10; urgent.value = 3
  if (!session.identity) return
  try {
    const response = await api('/settings/quota-warnings')
    if (!response.ok) throw new Error('read')
    const body: QuotaWarningSettings = await response.json()
    if (!validQuotaThresholds(body)) throw new Error('read')
    if (turn !== generation) return
    early.value = body.early_percent; urgent.value = body.urgent_percent; ready.value = true
  } catch { if (turn === generation) feedback.value = 'Thresholds could not be loaded. Reload to retry.' }
}, { immediate: true })
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
function keys(event: KeyboardEvent) {
  if (!(event.target instanceof HTMLInputElement) || event.isComposing || event.repeat) return
  if (event.key === 'Escape' && !event.metaKey && !event.ctrlKey && !event.altKey) {
    event.preventDefault(); event.stopPropagation(); event.target.blur()
  }
  if (event.key !== 'Enter') return
  event.preventDefault()
  if ((mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) && !event.altKey && !event.shiftKey) {
    event.stopPropagation(); void save()
  }
}
async function save() {
  if (!ready.value || saving.value || !valid.value) return
  const turn = generation, owner = identity.value
  const next = { early_percent: Number(early.value), urgent_percent: Number(urgent.value) }
  saving.value = true; feedback.value = ''
  try {
    const response = await api('/settings/quota-warnings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(next) })
    if (!response.ok) throw new Error('save')
    const saved: QuotaWarningSettings = await response.json()
    if (!validQuotaThresholds(saved)) throw new Error('save')
    if (turn !== generation || owner !== identity.value) return
    early.value = saved.early_percent; urgent.value = saved.urgent_percent; feedback.value = 'Thresholds saved.'
  } catch { if (turn === generation && owner === identity.value) feedback.value = 'Could not save thresholds. Try again.' }
  finally { if (turn === generation && owner === identity.value) saving.value = false }
}
</script>
<template>
  <SettingsCard title="Low-quota warnings" icon="agent" anchor="quota-warnings">
    <template #lead>Two notices from fresh measured usage, with no reminders between them. Private quota figures follow the account owner's sharing choice.</template>
    <form @submit.prevent="save" @keydown="keys">
      <fieldset class="thresholds" :disabled="!ready || saving">
        <legend class="sr-only">Percent remaining</legend>
        <label for="quota-early">Early notice (%)<input id="quota-early" v-model.number="early" type="number" min="2" max="50" step="1" inputmode="numeric" aria-describedby="quota-guidance" /></label>
        <label for="quota-urgent">Urgent notice (%)<input id="quota-urgent" v-model.number="urgent" type="number" min="1" max="49" step="1" inputmode="numeric" aria-describedby="quota-guidance" /></label>
      </fieldset>
      <button class="btn sm" type="submit" :disabled="!ready || saving || !valid" :aria-busy="saving">Save thresholds <KeyCap k="mod" /><KeyCap k="enter" /></button>
      <p id="quota-guidance" class="guidance">1–50% remaining; urgent must be lower than early.</p>
      <p class="feedback" role="status" aria-live="polite">{{ feedback || (ready && !valid ? 'Use whole percentages, with urgent below early.' : '\u00a0') }}</p>
    </form>
  </SettingsCard>
</template>
<style scoped>
.thresholds { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; border: 0; padding: 0; margin: 0 0 12px; max-width: 330px; }
label { display: grid; gap: 6px; font-size: 13px; color: var(--ink-2); }
input { width: 100%; height: 36px; padding: 0 10px; border: 1px solid var(--line-2); border-radius: 6px; background: var(--surface); color: var(--ink); font: 500 14px/1 var(--mono); }
.guidance, .feedback { font-size: 12px; line-height: 1.4; color: var(--ink-2); margin: 8px 0 0; }
.feedback { min-height: 2.8em; max-width: 330px; }
</style>
