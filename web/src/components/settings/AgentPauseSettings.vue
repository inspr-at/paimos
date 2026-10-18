<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { LEVELS, type PauseLevel } from '../../lib/agentPause'
import { useAgentPause } from '../../stores/agentPause'
const pause = useAgentPause(), saving = ref(false)
onMounted(() => { void pause.loadSettings() })
async function change(event: Event) { saving.value = true; await pause.setDefault((event.target as HTMLSelectElement).value as PauseLevel); saving.value = false }
</script>
<template>
  <div v-if="pause.person" class="pause-setting"><label for="default-pause-level">Default when pausing<small>Preselected in Pause and Pause all. Wind-down chooses a level from the time left.</small></label><select id="default-pause-level" :value="pause.defaultLevel === 'stop_now' ? 'pause' : pause.defaultLevel" :disabled="saving" @change="change"><option v-for="level in LEVELS.filter(l => l.value !== 'stop_now')" :key="level.value" :value="level.value">{{ level.name }}</option></select><p v-if="pause.settingsError" role="alert">{{ pause.settingsError }}</p></div>
</template>
<style scoped>
.pause-setting{display:grid;grid-template-columns:minmax(0,1fr) 150px;gap:12px;align-items:center;border-top:1px solid var(--line);padding:14px 0}.pause-setting label{display:grid;gap:4px;font-size:13px}.pause-setting small{font-size:12px;color:var(--ink-3)}.pause-setting select{height:36px;width:100%;border:1px solid var(--line);border-radius:6px;background:var(--field-bg);color:var(--ink);font-size:13px}.pause-setting p{grid-column:1/-1;color:var(--danger);font-size:12px}@media(max-width:600px){.pause-setting{grid-template-columns:minmax(0,1fr)}.pause-setting select{height:44px}}
</style>
