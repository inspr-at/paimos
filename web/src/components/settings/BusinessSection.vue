<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { can, permissionsRevoked } from '../../lib/authz'
import { useBusiness } from '../../stores/business'
import SetupCard from '../business/SetupCard.vue'
import IntegrationCard from '../crm/IntegrationCard.vue'
import AppIcon from '../AppIcon.vue'
import QuoteSettingsCard from './QuoteSettingsCard.vue'
import QuoteProfilesCard from './QuoteProfilesCard.vue'
import SettingsCard from './SettingsCard.vue'

// Business for admins: the one place that enables the parts (the overview only
// shows them), the quote sender and numbering, and how customers are kept. People
// who may manage plugins but not the workspace settings get the parts card alone.
const business = useBusiness()
onMounted(() => { void business.loadPlugins() })
// A session that ends (401) keeps the cards on screen, inert, like the section itself.
const settingsAdmin = ref(can('settings.manage'))
watch(() => can('settings.manage'), now => { if (!permissionsRevoked()) settingsAdmin.value = now })
</script>

<template>
  <div v-if="business.plugins" class="section">
    <div id="parts" class="parts-anchor">
      <SetupCard />
    </div>

    <template v-if="settingsAdmin">
      <QuoteSettingsCard />
      <QuoteProfilesCard />

      <SettingsCard title="Customers" icon="building" anchor="customers">
        <template #lead>Customers with their contacts and addresses, linked to projects, quotes and hours.</template>
        <template v-if="business.open.crm" #aside><RouterLink class="btn sm ghost" to="/business/customers">Open Customers<AppIcon name="arrow" :size="13" /></RouterLink></template>
        <p v-if="!business.open.crm" class="set-note"><AppIcon name="info" :size="14" />Customers is not enabled yet. Switch it on under Business parts above; Quotes needs it too.</p>
        <IntegrationCard v-else :admin="business.admin" bare />
      </SettingsCard>
    </template>
  </div>
  <div v-else-if="business.pluginsError" class="section-error glass-card" role="alert"><p>{{ business.pluginsError }}</p><button type="button" class="btn" @click="business.loadPlugins(true)">Try again</button></div>
  <div v-else class="section-placeholder skeleton-body" role="status" aria-label="Loading business settings"><span class="skeleton" /><span class="skeleton" /><span class="skeleton" /></div>
</template>

<style scoped>
.section { display: grid; gap: 14px; }
.section-error { display: grid; justify-items: start; gap: 12px; padding: 20px; }
.section-placeholder { display: grid; align-content: start; gap: 14px; min-height: 100vh; }
.section-placeholder .skeleton { height: 90px; }
.parts-anchor { scroll-margin-top: 20px; }
/* The parts card keeps its own frame; here it takes the settings cards' measure. */
.parts-anchor :deep(.setup) { margin: 0; padding: 20px; }
.parts-anchor :deep(.setup-head) { gap: 12px; }
.parts-anchor :deep(.setup-icon) { width: 32px; height: 32px; border-radius: 10px; }
.parts-anchor :deep(.setup-head h2) { font: 600 15px/1.35 var(--font); letter-spacing: 0; }
.parts-anchor :deep(.setup-head p) { margin-top: 2px; font-size: 13px; }
@media (max-width: 600px) { .parts-anchor :deep(.setup) { padding: 16px; } }
</style>
