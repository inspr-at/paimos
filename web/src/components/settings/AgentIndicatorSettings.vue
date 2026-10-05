<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { AGENT_STATE_KEY, useAgentAppearance } from '../../lib/agentAppearance'
import { onPreferenceFailure } from '../../lib/preferences'
const { choice, save } = useAgentAppearance()
const failed = ref(false)
onBeforeUnmount(onPreferenceFailure(key => { if (key === AGENT_STATE_KEY) failed.value = true }))
function change(patch: Parameters<typeof save>[0]) { failed.value = false; save(patch) }
</script>
<template>
  <div class="agent-behaviour">
    <div class="thresholds">
      <label>Yellow after<input class="field" type="number" min="1" max="1439" step="1" aria-label="Yellow after (minutes)" :value="choice.yellowMinutes" @change="change({ yellowMinutes: Number(($event.target as HTMLInputElement).value) })" /><span>min</span></label>
      <label>Red after<input class="field" type="number" :min="choice.yellowMinutes + 1" max="1440" step="1" aria-label="Red after (minutes)" :value="choice.redMinutes" @change="change({ redMinutes: Number(($event.target as HTMLInputElement).value) })" /><span>min</span></label>
    </div>
    <p class="hint">Heartbeat warnings say when a working agent goes quiet. Red follows yellow.</p>
    <p class="save-status" :role="failed ? 'alert' : 'status'"><template v-if="failed">Heartbeat warnings could not be saved.<button class="btn sm" type="button" @click="change({})">Try again</button></template></p>
    <RouterLink class="appearance-link" to="/settings/theme#agents">Avatar, motion, size and state colours in Theme</RouterLink>
  </div>
</template>
<style scoped>
.thresholds { display: flex; flex-wrap: wrap; align-items: center; gap: 12px 20px; }
.thresholds label { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; }
.thresholds .field { width: 5em; min-height: 44px; text-align: right; font-variant-numeric: tabular-nums; }
.thresholds span, .hint { font-size: 12px; color: var(--ink-2); }.hint { margin-top: 8px; }
.save-status { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; min-height: 44px; font-size: 12px; color: var(--danger); }
.appearance-link { display: inline-flex; align-items: center; min-height: 44px; font-size: 12px; color: var(--teal-ink); text-underline-offset: 3px; }
</style>
