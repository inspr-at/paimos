<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { setUsageProbe, type AgentAccount } from '../../lib/agents'
import { useSession } from '../../stores/session'
import { toast } from '../../lib/toast'

const props = defineProps<{ account: AgentAccount }>()
const emit = defineEmits<{ changed: [] }>()
const session = useSession()
const identity = computed(() => `${session.identity?.tenant.id}/${session.identity?.principal.id}/${props.account.id}/${props.account.link_revision ?? 0}`)
const enabled = ref(props.account.usage_probe_enabled === true)
const busy = ref(false)
watch(identity, () => { enabled.value = props.account.usage_probe_enabled === true; busy.value = false })
watch(() => props.account.usage_probe_enabled, value => { enabled.value = value === true })
async function toggle() {
  if (busy.value) return
  const binding = identity.value
  const id = props.account.id
  const revision = props.account.link_revision ?? 0
  const next = !enabled.value
  busy.value = true
  try {
    await setUsageProbe(id, revision, next)
    if (binding !== identity.value) return
    enabled.value = next
    emit('changed')
  } catch {
    if (binding === identity.value) toast('Could not confirm the change. Reload accounts before trying again.', { tone: 'error' })
  } finally { if (binding === identity.value) busy.value = false }
}
</script>

<template>
  <div class="usage-setting">
    <button type="button" role="switch" :aria-checked="enabled" :disabled="busy" class="usage-switch" :aria-describedby="`usage-note-${account.id}`" @click="toggle">
      <span>Read usage with this CLI's own login</span>
      <span class="switch-track" :class="{ on: enabled }" aria-hidden="true"><span /></span>
    </button>
    <p :id="`usage-note-${account.id}`">Uses unofficial provider endpoints; they may change or stop. You are responsible for following your provider's terms.</p>
  </div>
</template>

<style scoped>
.usage-setting { margin-top: 8px; }
.usage-switch { display: flex; align-items: center; justify-content: space-between; gap: 16px; width: 100%; min-height: 44px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); text-align: left; font: inherit; font-size: 13px; padding: 6px 0; cursor: pointer; }
.usage-switch:focus-visible { outline: 2px solid var(--teal-ink); outline-offset: 3px; }
.usage-switch:disabled { opacity: .6; cursor: wait; }
.switch-track { display: flex; align-items: center; flex: 0 0 34px; height: 20px; padding: 3px; box-sizing: border-box; border-radius: 12px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); }
.switch-track span { width: 14px; height: 14px; border-radius: 50%; background: var(--ink-3); }
.switch-track.on { background: var(--chip-teal-bg); }
.switch-track.on span { margin-left: auto; background: var(--teal-ink); }
p { font-size: 12px; color: var(--ink-2); margin: 2px 0 6px; }
</style>
