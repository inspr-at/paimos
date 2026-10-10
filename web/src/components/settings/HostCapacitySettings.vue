<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '../../lib/api'
import type { PairingView } from '../../lib/agentPairing'
import { defaultHostPolicy, hostCapacityReason, validHostPolicy, type HostCapacityPolicy } from '../../lib/hostCapacity'
import { settingsSubmitKey } from '../../lib/settingsOverlays'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
const props = defineProps<{ computer: PairingView; manage: boolean; ownerKey: string }>()
const emit = defineEmits<{ saved: [] }>()
const expanded = ref(false), busy = ref(false), message = ref('')
const draft = ref<HostCapacityPolicy>(defaultHostPolicy())
const revision = ref(0)
const key = computed(() => `${props.ownerKey}/${props.computer.computer_id}`)
watch(key, () => { draft.value = { ...(props.computer.host_capacity?.policy ?? defaultHostPolicy()) }; revision.value = props.computer.revision ?? 0; expanded.value = false; message.value = ''; busy.value = false }, { immediate: true })
const dirty = computed(() => JSON.stringify(draft.value) !== JSON.stringify(props.computer.host_capacity?.policy ?? defaultHostPolicy()))
watch(() => props.computer.revision, () => { if (!dirty.value && !busy.value) { draft.value = { ...(props.computer.host_capacity?.policy ?? defaultHostPolicy()) }; revision.value = props.computer.revision ?? 0 } })
const summary = computed(() => {
  const v = props.computer.host_capacity
  if (!v) return 'Off · no load report yet'
  if (props.computer.computer_state !== 'connected') return `${v.policy.mode === 'smart' ? 'Smart' : v.policy.mode === 'fixed' ? 'Fixed limit' : 'Off'} · new starts blocked`
  return `${v.policy.mode === 'fixed' ? `Fixed limit ${v.policy.maximum_load}` : v.policy.mode === 'smart' ? 'Smart' : 'Off'}${v.reason ? ` · waiting: ${hostCapacityReason(v.reason)}` : ' · new starts allowed'}`
})
async function save() {
  if (!props.manage || busy.value || !dirty.value || !validHostPolicy(draft.value) || !revision.value) return
  const owner = key.value, computer = props.computer.computer_id, reviewed = revision.value, policy = { ...draft.value }
  busy.value = true; message.value = ''
  try {
    const response = await api(`/agent-pairing/computers/${computer}/capacity`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_revision: reviewed, policy }) })
    if (!response.ok) throw new Error(response.status === 409 ? 'Computer changed. Reopen its settings before applying.' : 'Capacity settings could not be saved.')
    if (key.value !== owner) return
    const body = await response.json()
    if (key.value !== owner) return
    if (!body?.policy || !validHostPolicy(body.policy)) throw new Error('Saved settings could not be confirmed. Refresh the computer.')
    draft.value = { ...body.policy }; message.value = 'All changes saved'; toast('Capacity settings saved. Running agents keep working.'); emit('saved')
  } catch (error) { if (key.value === owner) message.value = error instanceof Error ? error.message : 'Capacity settings could not be saved.' }
  finally { if (key.value === owner) busy.value = false }
}
function keys(event: KeyboardEvent) {
  if (settingsSubmitKey(event)) { event.preventDefault(); event.stopPropagation(); void save() }
}
function modeKeys(event: KeyboardEvent) {
  const all: HostCapacityPolicy['mode'][] = ['smart', 'fixed', 'off']
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key) || event.metaKey || event.ctrlKey || event.altKey) return
  event.preventDefault()
  const current = all.indexOf(draft.value.mode)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? 2 : (current + (event.key === 'ArrowRight' ? 1 : 2)) % 3
  draft.value.mode = all[next]!
  const buttons = (event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('button'); buttons[next]?.focus()
}
</script>
<template>
  <section class="capacity-settings" aria-label="Capacity and load" @keydown="keys">
    <button class="disclosure" type="button" :aria-expanded="expanded" aria-controls="host-capacity-form" @click="expanded = !expanded"><AppIcon name="chevron-right" :class="{ opened: expanded }" /><b>Capacity and load</b><span>{{ summary }}</span></button>
    <div v-if="expanded" id="host-capacity-form" class="cap-body">
      <template v-if="manage && computer.computer_state === 'connected'">
        <div class="cap-row">
          <div class="seg" role="radiogroup" aria-label="Load setting" @keydown="modeKeys"><button v-for="mode in (['smart', 'fixed', 'off'] as const)" :key="mode" type="button" role="radio" :aria-checked="draft.mode === mode" :tabindex="draft.mode === mode ? 0 : -1" @click="draft.mode = mode">{{ mode === 'smart' ? 'Smart' : mode === 'fixed' ? 'Fixed limit' : 'Off' }}</button></div>
          <button class="btn sm apply" type="button" :disabled="!dirty || busy || !validHostPolicy(draft)" @click="save">Apply <KeyCap k="mod" /><KeyCap k="enter" /></button>
        </div>
        <p class="save-state" role="status">{{ message || (dirty ? 'Unsaved changes' : 'All changes saved') }}</p>
        <label class="setting"><span><b>Maximum agents</b><small>Never more than this many at once. 0 keeps the existing unlimited setting.</small></span><input v-model.number="draft.maximum_agents" type="number" min="0" max="64" step="1" inputmode="numeric" /></label>
        <div v-if="draft.mode === 'smart'">
          <p class="mode-copy">Smart sets the load limit from this computer’s {{ computer.host_capacity?.signals?.cores ?? 'reported' }} cores and starts new agents whenever there is room.</p>
          <label v-for="option in ([{ key: 'wait_when_busy', title: 'Wait when the computer is busy', copy: 'Pause new starts while load or memory pressure is high.' }, { key: 'ease_on_battery', title: 'Use less power on battery', copy: 'Lower the start limit while unplugged.' }, { key: 'ease_when_hot', title: 'Ease off when the computer gets hot', copy: 'Give it room to cool down.' }, { key: 'consider_activity', title: 'Be gentler while I’m using this computer', copy: 'Off by default. Reads only whether keyboard or mouse are in use, never what you type.' }] as const)" :key="option.key" class="setting"><span><b>{{ option.title }}</b><small>{{ option.copy }}</small></span><input v-model="draft[option.key]" type="checkbox" role="switch" /></label>
        </div>
        <label v-else-if="draft.mode === 'fixed'" class="setting"><span><b>Maximum load</b><small>New starts wait while one-minute load is above this.</small></span><input v-model.number="draft.maximum_load" type="number" min="1" max="200" step="0.5" inputmode="decimal" /></label>
        <p v-else class="mode-copy">Starts without waiting for host load. Maximum agents still applies.</p>
        <p class="mode-note">Only new starts wait. Running agents are never stopped by this setting.</p>
      </template>
      <dl v-else class="read-policy"><dt>Load setting</dt><dd>{{ summary }}</dd><dt>Maximum agents</dt><dd>{{ computer.host_capacity?.policy.maximum_agents || 'Unlimited' }}</dd><dt>Consider activity</dt><dd>{{ computer.host_capacity?.policy.consider_activity ? 'On' : 'Off' }}</dd></dl>
    </div>
  </section>
</template>
<style scoped>
.capacity-settings { padding-top: 12px; border-top: 1px solid var(--line); }.disclosure { display: grid; grid-template-columns: 16px auto minmax(0,1fr); align-items: center; gap: 10px; width: 100%; min-height: 52px; padding: 8px 0; border: 0; background: transparent; text-align: left; color: var(--ink); }.disclosure span { text-align: right; color: var(--ink-2); font-size: 12px; }.opened { transform: rotate(90deg); }.cap-body { padding-top: 12px; }.cap-row { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 10px; }.seg { display: inline-flex; gap: 2px; padding: 3px; background: var(--seg-bg); border-radius: 999px; }.seg button { height: 36px; padding: 0 12px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font-weight: 600; }.seg button[aria-checked=true] { background: var(--seg-on); color: var(--teal-ink); }.save-state { min-height: 2.8em; margin: 6px 0 4px; font-size: 12px; text-align: right; color: var(--ink-3); }.setting { display: flex; align-items: center; gap: 16px; min-height: 60px; padding: 8px 0; border-top: 1px solid var(--line); }.setting span { flex: 1; min-width: 0; }.setting b { display: block; font-size: 13px; }.setting small { display: block; margin-top: 2px; font-size: 12px; color: var(--ink-3); }.setting input[type=number] { width: 7ch; min-height: 36px; padding: 4px 8px; border: 1px solid var(--line-2); border-radius: var(--radius-s); background: var(--field-bg); color: var(--ink); }.setting input[type=checkbox] { width: 34px; height: 24px; flex: none; accent-color: var(--teal); }.mode-copy { padding: 12px 0; font-size: 13px; border-top: 1px solid var(--line); }.mode-note { margin-top: 10px; color: var(--ink-3); font-size: 12px; }.read-policy { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; font-size: 13px; }.read-policy dd { margin: 0; }@media(pointer:coarse) { .seg button, .btn, .setting input[type=number] { min-height: 44px; }.setting input[type=checkbox] { width: 44px; height: 44px; } }
</style>
