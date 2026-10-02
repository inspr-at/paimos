<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '../../lib/api'
import { brand } from '../../lib/brand'
import { can, myWorkspaceRole, permissionsAvailable } from '../../lib/authz'
import { canFeature } from '../../lib/features'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import BrandCard from './BrandCard.vue'
import ModelProviderCard from './ModelProviderCard.vue'
import SettingsCard from './SettingsCard.vue'
import FeatureFlagsCard from './FeatureFlagsCard.vue'
import WorkspaceSummary from './WorkspaceSummary.vue'
import StatusAutopilot from './StatusAutopilot.vue'

// The workspace itself: its name and my role in it. Who is in it, their roles,
// invites and agent keys live under Access.
const session = useSession()
const role = computed(() => myWorkspaceRole()?.name ?? (permissionsAvailable() ? 'No workspace role' : '—'))
const minutes = ref(10)
const intervalReady = ref(false)
onMounted(async () => {
  if (!can('settings.manage')) return
  try {
    const response = await api('/settings/eta-interval')
    if (!response.ok) return
    const body = await response.json()
    if (typeof body.interval_minutes === 'number') {
      minutes.value = body.interval_minutes
      intervalReady.value = true
    }
  } catch { /* hide the control when the interval cannot be read */ }
})
// AEON-291: minutes of silence before the server marks a session outside the
// product as Lost contact. Hidden when it cannot be read.
const lostMinutes = ref(15)
const lostReady = ref(false)
onMounted(async () => {
  if (!can('settings.manage')) return
  try {
    const response = await api('/settings/heartbeat-lost')
    if (!response.ok) return
    const body = await response.json()
    if (typeof body.heartbeat_lost_minutes === 'number') { lostMinutes.value = body.heartbeat_lost_minutes; lostReady.value = true }
  } catch { /* hide the control */ }
})
async function saveLost() {
  const next = Math.round(Number(lostMinutes.value))
  if (!(next >= 5 && next <= 1440)) return
  lostMinutes.value = next
  await api('/settings/heartbeat-lost', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ heartbeat_lost_minutes: next }) })
}
async function saveInterval() {
  const next = Math.round(Number(minutes.value))
  if (next < 1 || next > 240) return
  minutes.value = next
  await api('/settings/eta-interval', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ interval_minutes: next }) })
}
</script>

<template>
  <div class="section">
    <SettingsCard title="Workspace" icon="folder" anchor="workspace">
      <template #lead>The workspace you are signed in to.</template>
      <dl class="set-facts">
        <div><dt>Name</dt><dd>{{ session.identity?.tenant.name }}</dd></div>
        <div><dt>Your role</dt><dd>{{ role }}</dd></div>
      </dl>
      <WorkspaceSummary v-if="can('nodes.read') && canFeature('workspace-summary')" />
    </SettingsCard>
    <FeatureFlagsCard v-if="can('settings.manage')" />
    <BrandCard v-if="can('settings.manage')" />
    <ModelProviderCard v-if="can('settings.manage')" />
    <SettingsCard v-if="intervalReady" title="Estimates" icon="clock" anchor="estimates">
      <template #lead>How often a working agent reports when a ticket will be ready, and when it will be live.</template>
      <label class="interval" for="eta-minutes">Minutes between estimates</label>
      <input id="eta-minutes" v-model.number="minutes" class="minutes" type="number" min="1" max="240" inputmode="numeric" @change="saveInterval" />
    </SettingsCard>
    <SettingsCard v-if="lostReady" title="Silent sessions" icon="agent" anchor="silent-sessions">
      <template #lead>A session running outside {{ brand.short_name }} that stops reporting is marked Lost contact. Its next heartbeat brings it back.</template>
      <label class="interval" for="lost-minutes">Minutes without a heartbeat</label>
      <input id="lost-minutes" v-model.number="lostMinutes" class="minutes" type="number" min="5" max="1440" inputmode="numeric" @change="saveLost" />
    </SettingsCard>
    <StatusAutopilot v-if="can('settings.manage')" />
    <SettingsCard v-if="can('members.read')" title="People and agents" icon="users" anchor="members">
      <template #lead>Members, invites, roles, project access and agent keys have their own place.</template>
      <template #aside><RouterLink class="btn sm" to="/settings/access">Open Access<AppIcon name="arrow" :size="13" /></RouterLink></template>
    </SettingsCard>
  </div>
</template>

<style scoped>
.section { display: grid; gap: 14px; }
.interval { display: block; margin-bottom: 8px; font-size: 13px; color: var(--ink-2); }
.minutes { width: 88px; height: 36px; padding: 0 10px; border: 1px solid var(--line-2); border-radius: 8px; background: var(--surface); color: var(--ink); font: 500 14px/1 var(--mono); }
</style>
