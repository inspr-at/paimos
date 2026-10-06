<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api } from '../../lib/api'
import { can } from '../../lib/authz'
import { validQuotaThresholds, type QuotaWarningSettings } from '../../lib/quotaWarnings'
import { useSession } from '../../stores/session'
import { toast } from '../../lib/toast'
import SettingsPopover from './SettingsPopover.vue'
const emit = defineEmits<{ changed: [settings: QuotaWarningSettings] }>()
const session = useSession()
const identity = computed(() => `${session.identity?.tenant.id ?? ''}/${session.identity?.principal.id ?? ''}`)
const early = ref(10), urgent = ref(3), saved = ref<QuotaWarningSettings>({ early_percent: 10, urgent_percent: 3 })
const ready = ref(false), saving = ref(false), feedback = ref(''), open = ref(false), anchor = ref<HTMLElement | null>(null)
const manage = computed(() => session.identity?.principal.kind === 'person' && can('settings.manage'))
let generation = 0
onBeforeUnmount(() => { generation++ })
const valid = computed(() => validQuotaThresholds({ early_percent: Number(early.value), urgent_percent: Number(urgent.value) }))
watch([identity, () => can('account.read')], async () => {
  const turn = ++generation
  ready.value = false; saving.value = false; feedback.value = ''; open.value = false; early.value = 10; urgent.value = 3
  if (!session.identity || !can('account.read')) return
  try {
    const response = await api('/settings/quota-warnings')
    if (!response.ok) throw new Error('read')
    const body: QuotaWarningSettings = await response.json()
    if (!validQuotaThresholds(body)) throw new Error('read')
    if (turn !== generation) return
    saved.value = body; early.value = body.early_percent; urgent.value = body.urgent_percent; ready.value = true; emit('changed', body)
  } catch { if (turn === generation) feedback.value = 'Thresholds could not be loaded.' }
}, { immediate: true })
watch(manage, allowed => { if (!allowed) open.value = false })
function edit(event: Event) { anchor.value = event.currentTarget as HTMLElement; early.value = saved.value.early_percent; urgent.value = saved.value.urgent_percent; feedback.value = ''; open.value = true }
async function write(next: QuotaWarningSettings, expected: QuotaWarningSettings) {
  const response = await api('/settings/quota-warnings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...next, expected_early_percent: expected.early_percent, expected_urgent_percent: expected.urgent_percent }) })
  if (!response.ok) throw new Error(response.status === 409 ? 'Thresholds changed. Reopen Change before saving.' : 'Could not save thresholds. Try again.')
  const body: QuotaWarningSettings = await response.json()
  if (!validQuotaThresholds(body)) throw new Error('Saved thresholds could not be confirmed.')
  return body
}
async function save() {
  if (!manage.value || !ready.value || saving.value || !valid.value) return
  const turn = generation, owner = identity.value, previous = { ...saved.value }
  const next = { early_percent: Number(early.value), urgent_percent: Number(urgent.value) }
  saving.value = true; feedback.value = ''
  try {
    const body = await write(next, previous)
    if (turn !== generation || owner !== identity.value) return
    saved.value = body; emit('changed', body); open.value = false
    toast('Low-quota warnings saved.', { action: { label: 'Undo', run: () => {
      if (turn !== generation || owner !== identity.value || !manage.value) return
      void write(previous, body).then(restored => { if (turn === generation && owner === identity.value) { saved.value = restored; emit('changed', restored); toast('Low-quota warnings restored.') } }).catch(error => { if (turn === generation && owner === identity.value) toast(error.message, { tone: 'error' }) })
    } } })
  } catch (error) { if (turn === generation && owner === identity.value) feedback.value = error instanceof Error ? error.message : 'Could not save thresholds.' }
  finally { if (turn === generation && owner === identity.value) saving.value = false }
}
</script>
<template>
  <p id="quota-warnings" class="quota-line">Low-quota warnings: <template v-if="ready">early {{ saved.early_percent }} %, urgent {{ saved.urgent_percent }} %</template><template v-else>{{ feedback || 'Loading…' }}</template><template v-if="ready && manage"> · <button type="button" @click="edit">Change</button></template></p>
  <SettingsPopover v-model:open="open" :anchor="anchor" label="Low-quota warnings" mode="form" :context-key="identity" :busy="saving || !valid" :error="feedback || (!valid ? 'Use whole percentages, with urgent below early.' : '')" hint="1–50% remaining; urgent must be lower than early." @submit="save">
    <div class="thresholds"><label>Early notice (%)<input v-model.number="early" type="number" min="2" max="50" step="1" inputmode="numeric" :disabled="saving" /></label><label>Urgent notice (%)<input v-model.number="urgent" type="number" min="1" max="49" step="1" inputmode="numeric" :disabled="saving" /></label></div>
  </SettingsPopover>
</template>
<style scoped>
.quota-line { margin: 12px 0 0; color: var(--ink-3); font-size: 12.5px; }.quota-line button { border: 0; padding: 0; background: transparent; color: var(--teal-ink); text-decoration: underline; }.thresholds { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: 12px; }.thresholds label { display: grid; gap: 6px; font-size: 13px; color: var(--ink-2); }.thresholds input { width: 100%; min-height: 36px; padding: 4px 8px; border: 1px solid var(--line-2); border-radius: var(--radius-s); background: var(--field-bg); color: var(--ink); }@media(pointer:coarse) { .quota-line button, .thresholds input { min-height: 44px; } }
</style>
