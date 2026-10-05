<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { can, myWorkspaceRole, permissionsAvailable } from '../../lib/authz'
import { canFeature } from '../../lib/features'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import WorkVocabularyCard from './WorkVocabularyCard.vue'
import BrandCard from './BrandCard.vue'
import ModelProviderCard from './ModelProviderCard.vue'
import SettingsCard from './SettingsCard.vue'
import FeatureFlagsCard from './FeatureFlagsCard.vue'
import WorkspaceSummary from './WorkspaceSummary.vue'
import StatusAutopilot from './StatusAutopilot.vue'
import QuotaWarningCard from './QuotaWarningCard.vue'

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
    <WorkVocabularyCard v-if="can('settings.manage')" />
    <BrandCard v-if="can('settings.manage')" />
    <ModelProviderCard v-if="can('settings.manage')" />
    <QuotaWarningCard v-if="can('settings.manage')" />
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
</style>
