<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useDeveloperSettings } from '../../lib/developerSettings'
import { useSession } from '../../stores/session'
import SettingsCard from './SettingsCard.vue'

const session = useSession()
const { showFlowControls, setShowFlowControls, showReservedVersions, setShowReservedVersions, showExpertStart, setShowExpertStart, saving, failed } = useDeveloperSettings()
async function change(event: Event) {
  const input = event.target as HTMLInputElement
  await setShowFlowControls(input.checked)
  input.checked = showFlowControls.value
}
async function changeReserved(event: Event) {
  const input = event.target as HTMLInputElement
  await setShowReservedVersions(input.checked)
  input.checked = showReservedVersions.value
}
async function changeExpert(event: Event) {
  const input = event.target as HTMLInputElement
  await setShowExpertStart(input.checked)
  input.checked = showExpertStart.value
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
      <label id="reserved-versions" class="flow-choice reserved-choice">
        <input type="checkbox" role="switch" :checked="showReservedVersions" :disabled="saving || session.identity?.principal.kind !== 'person'"
          aria-describedby="reserved-versions-hint" @change="changeReserved" />
        <span>Show reserved versions</span>
      </label>
      <p id="reserved-versions-hint" class="hint">Includes reserved, never-published versions in the release history. They always count in the statistics. Off by default.</p>
      <label id="expert-start" class="flow-choice reserved-choice">
        <input type="checkbox" role="switch" :checked="showExpertStart" :disabled="saving || session.identity?.principal.kind !== 'person'"
          aria-describedby="expert-start-hint" @change="changeExpert" />
        <span>Start agents manually (expert)</span>
      </label>
      <p id="expert-start-hint" class="hint">Shows Start agent manually on the Agents page and Start now on… on a queued ticket: you pick ticket, host, harness, account, model and thinking yourself. The same start checks apply. Off by default; project leads start workers for you.</p>
      <p v-if="saving" role="status" class="hint">Saving your preference…</p>
      <p v-if="failed" role="alert" class="error-line">Your developer preference could not be saved. Please try the switch again.</p>
    </SettingsCard>
  </div>
</template>

<style scoped>
.flow-choice { display: flex; align-items: center; gap: 12px; min-height: 44px; font-size: 13.5px; font-weight: 600; cursor: pointer; }
.reserved-choice { margin-top: 18px; scroll-margin-top: 20px; }
input { flex-shrink: 0; width: 18px; height: 18px; accent-color: var(--teal); }
input:focus-visible { outline: 2px solid var(--teal); outline-offset: 3px; }
.hint { margin-top: 6px; color: var(--ink-2); font-size: 12.5px; line-height: 1.5; }
.error-line { margin-top: 12px; color: var(--danger); font-size: 13px; }
</style>
