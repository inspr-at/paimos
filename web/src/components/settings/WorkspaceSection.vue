<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive } from 'vue'
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
import ModelRefreshSettings from './ModelRefreshSettings.vue'
import StatusAutopilot from './StatusAutopilot.vue'
import AgentActivityCard from './AgentActivityCard.vue'
import QuotaWarningCard from './QuotaWarningCard.vue'

// The workspace itself: its name and my role in it. Who is in it, their roles,
// invites and agent keys live under Access.
const session = useSession()
const role = computed(() => myWorkspaceRole()?.name ?? (permissionsAvailable() ? 'No workspace role' : '—'))
let disposed = false
onBeforeUnmount(() => { disposed = true })
function timing(path: string, field: string, min: number, max: number, initial: number) {
  const state = reactive({ minutes: initial, confirmed: initial, ready: false, saving: false, error: '', retry: null as number | null })
  const valid = (value: unknown): value is number => typeof value === 'number' && Number.isInteger(value) && value >= min && value <= max
  onMounted(async () => {
    if (!can('settings.manage')) return
    try {
      const response = await api(path)
      if (!response.ok) return
      const body = await response.json()
      if (!disposed && valid(body[field])) { state.minutes = state.confirmed = body[field]; state.ready = true }
    } catch { /* hide the control when its authoritative value cannot be read */ }
  })
  async function save(value = state.minutes) {
    if (disposed || state.saving || !state.ready) return
    const next = Math.round(Number(value))
    if (!valid(next)) { state.minutes = state.confirmed; state.retry = null; state.error = `Use ${min}–${max} minutes. Last confirmed: ${state.confirmed} minutes.`; return }
    state.saving = true
    try {
      const response = await api(path, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ [field]: next }) })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      const body = await response.json()
      if (!valid(body[field])) throw new Error('Invalid timing response')
      if (!disposed) { state.minutes = state.confirmed = body[field]; state.error = ''; state.retry = null }
    } catch {
      if (!disposed) {
        state.minutes = state.confirmed; state.retry = next
        state.error = `The save could not be confirmed. Last confirmed: ${state.confirmed} minutes.`
      }
    } finally { if (!disposed) state.saving = false }
  }
  return { state, save }
}
const estimate = timing('/settings/eta-interval', 'interval_minutes', 1, 240, 10)
const lost = timing('/settings/heartbeat-lost', 'heartbeat_lost_minutes', 5, 1440, 15)
const saveInterval = () => estimate.save()
const saveLost = () => lost.save()
</script>

<template>
  <div class="section">
    <SettingsCard title="Workspace" icon="folder" anchor="workspace">
      <template #lead>The workspace you are signed in to.</template>
      <dl class="set-facts">
        <div><dt>Name</dt><dd>{{ session.identity?.tenant.name }}</dd></div>
        <div><dt>Your role</dt><dd>{{ role }}</dd></div>
      </dl>
    </SettingsCard>
    <FeatureFlagsCard v-if="can('settings.manage')" />
    <SettingsCard v-if="can('nodes.read') && canFeature('workspace-summary')" title="Workspace summary" icon="folder" anchor="workspace-summary">
      <WorkspaceSummary />
    </SettingsCard>
    <ModelRefreshSettings v-if="can('models.read')" />
    <BrandCard v-if="can('settings.manage')" />
    <ModelProviderCard v-if="can('settings.manage')" />
    <SettingsCard v-if="estimate.state.ready" title="Estimates" icon="clock" anchor="estimates">
      <template #lead>How often a working agent reports when a ticket will be ready, and when it will be live.</template>
      <label class="interval" for="eta-minutes">Minutes between estimates</label>
      <div class="timing-controls">
        <input id="eta-minutes" v-model.number="estimate.state.minutes" class="minutes" type="number" min="1" max="240" inputmode="numeric" :disabled="estimate.state.saving" :aria-describedby="estimate.state.error ? 'eta-error' : undefined" @change="saveInterval" />
        <button v-if="estimate.state.retry !== null" type="button" class="btn sm" :disabled="estimate.state.saving" @click="estimate.save(estimate.state.retry!)">Retry {{ estimate.state.retry }} minutes</button>
      </div>
    </SettingsCard>
    <SettingsCard v-if="lost.state.ready" title="Silent sessions" icon="agent" anchor="silent-sessions">
      <template #lead>A session running outside {{ brand.short_name }} that stops reporting is marked Lost contact. Its next heartbeat brings it back.</template>
      <label class="interval" for="lost-minutes">Minutes without a heartbeat</label>
      <div class="timing-controls">
        <input id="lost-minutes" v-model.number="lost.state.minutes" class="minutes" type="number" min="5" max="1440" inputmode="numeric" :disabled="lost.state.saving" :aria-describedby="lost.state.error ? 'lost-error' : undefined" @change="saveLost" />
        <button v-if="lost.state.retry !== null" type="button" class="btn sm" :disabled="lost.state.saving" @click="lost.save(lost.state.retry!)">Retry {{ lost.state.retry }} minutes</button>
      </div>
    </SettingsCard>
    <!-- Both timing controls stay above feedback that grows downward. -->
    <p v-if="estimate.state.error" id="eta-error" class="timing-error" role="alert">Estimates: {{ estimate.state.error }}</p>
    <p v-if="lost.state.error" id="lost-error" class="timing-error" role="alert">Silent sessions: {{ lost.state.error }}</p>
    <QuotaWarningCard v-if="can('settings.manage')" />
    <AgentActivityCard v-if="can('settings.manage')" />
    <StatusAutopilot v-if="can('settings.manage')" />
    <SettingsCard v-if="can('members.read')" title="People and agents" icon="users" anchor="members">
      <template #lead>Members, invites, roles, project access and agent keys have their own place.</template>
      <template #aside><RouterLink class="btn sm" to="/settings/access">Open Access<AppIcon name="arrow" :size="13" /></RouterLink></template>
    </SettingsCard>
  </div>
</template>

<style scoped>
/* Long project options must not define the width of every settings card. */
.section { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; }
.interval { display: block; margin-bottom: 8px; font-size: 13px; color: var(--ink-2); }
.minutes { width: 88px; height: 36px; padding: 0 10px; border: 1px solid var(--line-2); border-radius: 8px; background: var(--surface); color: var(--ink); font: 500 14px/1 var(--mono); }
.timing-controls { display: flex; align-items: center; gap: 10px; }
.timing-error { margin-top: 8px; font-size: 13px; color: var(--danger); }
</style>
