<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useDeveloperSettings } from '../../lib/developerSettings'
import { useSession } from '../../stores/session'
import SettingsCard from './SettingsCard.vue'

const session = useSession()
const { showFlowControls, setShowFlowControls, saving, failed } = useDeveloperSettings()
async function change(event: Event) {
  const input = event.target as HTMLInputElement
  await setShowFlowControls(input.checked)
  input.checked = showFlowControls.value
}
</script>

<template>
  <div class="section-stack">
    <SettingsCard title="Developer" icon="gear" anchor="flow-controls">
      <template #lead>For people working on Paimos itself. These preferences apply only to you.</template>
      <label class="flow-choice">
        <input type="checkbox" role="switch" :checked="showFlowControls" :disabled="saving || session.identity?.principal.kind !== 'person'"
          aria-describedby="flow-controls-hint" @change="change" />
        <span>Show the flow controls (not yet tested end to end)</span>
      </label>
      <p id="flow-controls-hint" class="hint">Reveals the footer flow control, the project Journey tab, its stages and the release walker. Off by default.</p>
      <p v-if="saving" role="status" class="hint">Saving your preference…</p>
      <p v-if="failed" role="alert" class="error-line">Your flow preference could not be saved. Please try the switch again.</p>
    </SettingsCard>
  </div>
</template>

<style scoped>
.flow-choice { display: flex; align-items: center; gap: 12px; min-height: 44px; font-size: 13.5px; font-weight: 600; cursor: pointer; }
input { flex-shrink: 0; width: 18px; height: 18px; accent-color: var(--teal); }
input:focus-visible { outline: 2px solid var(--teal); outline-offset: 3px; }
.hint { margin-top: 6px; color: var(--ink-2); font-size: 12.5px; line-height: 1.5; }
.error-line { margin-top: 12px; color: var(--danger); font-size: 13px; }
</style>
