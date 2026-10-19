<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { api } from '../../lib/api'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import SettingsCard from './SettingsCard.vue'

interface Policy { enabled: boolean; dry_run: boolean; months: number; revision: number }
const session = useSession()
const owner = () => `${session.identity?.tenant.id ?? ''}/${session.identity?.principal.id ?? ''}`
const enabled = ref(false), dryRun = ref(true), saved = ref<Policy | null>(null)
const busy = ref(false), ready = ref(false), message = ref(''), failed = ref(false)
let epoch = 0, disposed = false
const current = (turn: number, identity: string) => !disposed && turn === epoch && owner() === identity && can('settings.manage')
function policy(value: unknown): Policy {
  const p = value as Policy
  if (!p || typeof p.enabled !== 'boolean' || typeof p.dry_run !== 'boolean' || p.months !== 12 || !Number.isSafeInteger(p.revision) || p.revision < 0) throw new Error('Invalid policy')
  return p
}
async function load() {
  const turn = ++epoch, identity = owner()
  busy.value = true; ready.value = false; saved.value = null; failed.value = false; message.value = 'Loading retention settings…'
  enabled.value = false; dryRun.value = true
  try {
    const response = await api('/settings/import-retention')
    if (!response.ok) throw new Error('load')
    const value = policy(await response.json())
    if (current(turn, identity)) { saved.value = value; enabled.value = value.enabled; dryRun.value = value.dry_run; ready.value = true; message.value = '' }
  } catch { if (current(turn, identity)) { failed.value = true; message.value = 'Retention settings could not be loaded. Reload to try again.' } }
  finally { if (current(turn, identity)) busy.value = false }
}
async function save() {
  if (busy.value || !ready.value || !saved.value || !can('settings.manage')) return
  const turn = epoch, identity = owner(), expected = saved.value.revision
  const body = { enabled: enabled.value, dry_run: dryRun.value, expected_revision: expected }
  busy.value = true; failed.value = false; message.value = ''
  try {
    const response = await api('/settings/import-retention', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
    if (!current(turn, identity)) return
    if (response.status === 409) { ready.value = false; throw new Error('conflict') }
    if (!response.ok) throw new Error('save')
    const value = policy(await response.json())
    if (current(turn, identity)) { saved.value = value; enabled.value = value.enabled; dryRun.value = value.dry_run; message.value = 'Retention settings saved.' }
  } catch (error) {
    if (current(turn, identity)) { failed.value = true; message.value = error instanceof Error && error.message === 'conflict' ? 'Changed elsewhere. Reload before saving.' : 'Retention settings were not saved. Try again.' }
  } finally { if (current(turn, identity)) busy.value = false }
}
watch(owner, () => { void load() }, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { disposed = true; epoch++ })
</script>

<template>
  <SettingsCard title="START import retention" icon="folder" anchor="import-retention">
    <template #lead>Remove projects created from START after 12 months without a person's changes or engagement flag.</template>
    <div class="retention-controls" :aria-busy="busy">
      <label><input v-model="enabled" type="checkbox" :disabled="busy || !ready"> Enable retention</label>
      <label><input v-model="dryRun" type="checkbox" :disabled="busy || !ready"> Dry run</label>
      <button class="btn" type="button" :disabled="busy || !ready" @click="save">Save retention</button>
      <button class="btn" type="button" :disabled="busy" @click="load">Reload</button>
    </div>
    <p class="hint">Dry run keeps every project. With dry run off, eligible projects and their records are permanently deleted; audit history is retained. Other projects are excluded.</p>
    <p v-if="message" :class="{ problem: failed }" :role="failed ? 'alert' : 'status'">{{ message }}</p>
  </SettingsCard>
</template>

<style scoped>
.retention-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 12px 20px; }
label { display: flex; align-items: center; gap: 8px; min-height: 44px; font-size: 13px; color: var(--ink); cursor: pointer; }
input { accent-color: var(--teal-ink); }
.btn { min-height: 44px; }
.hint, [role] { margin-top: 12px; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.problem { color: var(--red-ink); }
</style>
