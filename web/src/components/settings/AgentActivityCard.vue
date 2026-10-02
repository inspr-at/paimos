<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '../../lib/api'
import SettingsCard from './SettingsCard.vue'

type Mode = 'off' | 'tool_activity' | 'agent_summary'
const options: { value: Mode; label: string; detail: string }[] = [
  { value: 'off', label: 'Off', detail: 'Collect and show no activity.' },
  { value: 'tool_activity', label: 'Tool activity', detail: 'Use tool calls automatically, with no summary tokens.' },
  { value: 'agent_summary', label: 'Agent summary', detail: 'A few words from the agent; use tool activity after ten minutes.' },
]
const mode = ref<Mode>('agent_summary')
const selected = ref<Mode>('agent_summary')
const ready = ref(false)
const saving = ref(false)
const error = ref('')
onMounted(async () => {
  try {
    const response = await api('/settings/agent-activity')
    if (!response.ok) return
    const body = await response.json()
    if (options.some(option => option.value === body.mode)) { mode.value = body.mode; selected.value = body.mode; ready.value = true }
  } catch { /* Keep the control hidden when policy cannot be read. */ }
})
async function save(event: Event) {
  const next = (event.target as HTMLInputElement).value as Mode
  saving.value = true; error.value = ''
  try {
    const response = await api('/settings/agent-activity', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode: next }) })
    if (!response.ok) throw new Error('save')
    mode.value = next
  } catch { selected.value = mode.value; error.value = 'Could not save Agent activity. Try again.' }
  finally { saving.value = false }
}
</script>

<template>
  <SettingsCard v-if="ready" title="Agent activity" icon="agent" anchor="agent-activity">
    <template #lead>What agents show while they work. Applies to every project in this workspace.</template>
    <fieldset :disabled="saving" class="activity-options">
      <legend class="sr-only">Agent activity</legend>
      <label v-for="option in options" :key="option.value" class="activity-option">
        <input v-model="selected" type="radio" name="agent-activity" :value="option.value" @change="save" />
        <span><strong>{{ option.label }}</strong><small>{{ option.detail }}</small></span>
      </label>
    </fieldset>
    <p v-if="error" role="alert" class="save-error">{{ error }}</p>
  </SettingsCard>
</template>

<style scoped>
.activity-options { display: grid; gap: 12px; border: 0; margin: 0; padding: 0; }
.activity-option { display: flex; gap: 10px; align-items: flex-start; cursor: pointer; }
.activity-option input { margin-top: 3px; accent-color: var(--ink); }
.activity-option strong { display: block; font-size: 13px; font-weight: 500; }
.activity-option small { display: block; margin-top: 3px; color: var(--ink-2); font-size: 12px; }
.save-error { color: var(--ink-2); font-size: 12px; }
</style>
