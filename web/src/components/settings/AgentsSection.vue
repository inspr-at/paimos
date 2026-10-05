<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { can } from '../../lib/authz'
import AgentActivityCard from './AgentActivityCard.vue'
import ModelRefreshSettings from './ModelRefreshSettings.vue'
</script>

<template>
  <div class="section">
    <p class="who">Workspace admins see and change these settings. Applies to every project.</p>
    <AgentActivityCard v-if="can('settings.manage')" />
    <ModelRefreshSettings v-if="can('models.read')" />
  </div>
</template>

<style scoped>
.section { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; }
.who { color: var(--ink-2); font-size: 13px; }
</style>
