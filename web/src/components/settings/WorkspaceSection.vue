<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { can, myWorkspaceRole, permissionsAvailable } from '../../lib/authz'
import { canFeature } from '../../lib/features'
import { useSession } from '../../stores/session'
import BrandCard from './BrandCard.vue'
import ModelProviderCard from './ModelProviderCard.vue'
import SettingsCard from './SettingsCard.vue'
import FeatureFlagsCard from './FeatureFlagsCard.vue'
import WorkspaceSummary from './WorkspaceSummary.vue'

// The workspace itself: its name and my role in it. Who is in it, their roles,
// invites and agent keys live under Access.
const session = useSession()
const role = computed(() => myWorkspaceRole()?.name ?? (permissionsAvailable() ? 'No workspace role' : '—'))
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
    <BrandCard v-if="can('settings.manage')" />
    <ModelProviderCard v-if="can('settings.manage')" />
  </div>
</template>
<style scoped>
.section { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; }
</style>
