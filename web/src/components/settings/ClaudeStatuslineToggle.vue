<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref, watch } from 'vue'
import { brand } from '../../lib/brand'
import { claudeStatuslineCopy, setClaudeStatusline } from '../../lib/agents'
import type { AgentAccount } from '../../lib/agents'
const props = defineProps<{ account: AgentAccount }>()
const emit = defineEmits<{ changed: [] }>()
const enabled = ref(props.account.statusline_enabled === true)
const busy = ref(false)
const error = ref('')
watch(() => props.account.statusline_enabled, value => { enabled.value = value === true })
async function toggle() {
  if (busy.value) return
  busy.value = true; error.value = ''
  try {
    const result = await setClaudeStatusline(props.account.id, !enabled.value)
    enabled.value = result.enabled
    emit('changed')
  } catch { error.value = 'Could not change the status line. Try again.' }
  finally { busy.value = false }
}
</script>

<template>
  <div class="statusline-setting">
    <button type="button" role="switch" :aria-checked="enabled" :disabled="busy" class="statusline-switch" @click="toggle">
      <span>{{ claudeStatuslineCopy(account.statusline_opt_in, brand.short_name) }}</span>
      <span class="switch-track" :class="{ on: enabled }" aria-hidden="true"><span /></span>
    </button>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-else-if="enabled && account.reading_support !== 'statusline'" role="status">Applies when this computer connects; an existing status line is kept.</p>
  </div>
</template>

<style scoped>
.statusline-setting { margin-top: 8px; }
.statusline-switch { display: flex; align-items: center; justify-content: space-between; gap: 16px; width: 100%; min-height: 44px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); text-align: left; font: inherit; font-size: 13px; padding: 6px 0; cursor: pointer; }
.statusline-switch:focus-visible { outline: 2px solid var(--teal-ink); outline-offset: 3px; }
.statusline-switch:disabled { opacity: .6; cursor: wait; }
.switch-track { display: flex; align-items: center; flex: 0 0 34px; height: 20px; padding: 3px; box-sizing: border-box; border-radius: 12px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); }
.switch-track span { width: 14px; height: 14px; border-radius: 50%; background: var(--ink-3); }
.switch-track.on { background: var(--chip-teal-bg); }
.switch-track.on span { margin-left: auto; background: var(--teal-ink); }
p { font-size: 12px; color: var(--ink-2); margin: 2px 0 6px; }
</style>
