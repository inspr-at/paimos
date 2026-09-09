<!--
 PAIMOS — Your Professional & Personal AI Project OS
 Copyright (C) 2026 Markus Barta <markus@barta.com>

 This program is free software: you can redistribute it and/or modify
 it under the terms of the GNU Affero General Public License as
 published by the Free Software Foundation, version 3.

 This program is distributed in the hope that it will be useful,
 but WITHOUT ANY WARRANTY; without even the implied warranty of
 MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 GNU Affero General Public License for more details.

 PAI-980. The CRM door in the Paimos 6 shell.

 Renders the existing customer list (`/crm`) or detail (`/crm/:id`)
 inside the v6 layout. The 5.x views teleport their title into
 `#app-header-left`, which only AppHeader provides, so this wrapper
 hosts that target itself. When the instance switch is off the door
 shows a short notice instead of the views; the data and the
 `/customers` routes stay reachable, nothing is hidden from the API.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { crmEnabled, loadInstance } from '@/api/instance'
import { useAuthStore } from '@/stores/auth'
import CustomersView from '@/views/CustomersView.vue'
import CustomerDetailView from '@/views/CustomerDetailView.vue'

const route = useRoute()
const auth = useAuthStore()
void loadInstance()
const isDetail = computed(() => typeof route.params.id === 'string' && route.params.id.length > 0)
</script>

<template>
  <section class="crm-door" data-testid="crm-door">
    <template v-if="crmEnabled">
      <header class="crm-door-header">
        <div id="app-header-left" class="crm-door-title" />
        <div id="app-header-right" class="crm-door-tools" />
      </header>
      <div class="crm-door-body">
        <CustomerDetailView v-if="isDetail" :key="String(route.params.id)" />
        <CustomersView v-else />
      </div>
    </template>
    <div v-else class="crm-door-off" role="status">
      <strong>CRM is disabled on this instance.</strong>
      <p>
        Customer data is kept; only the entry points are hidden.
        <RouterLink v-if="auth.isAdmin" to="/integrations?tab=crm">Enable it under Integrations → CRM</RouterLink>
        <span v-else>Ask an administrator to enable it under Integrations → CRM.</span>
      </p>
    </div>
  </section>
</template>

<style scoped>
.crm-door {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px 20px 32px;
  min-width: 0;
}
.crm-door-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 28px;
}
.crm-door-title :deep(.ah-title) {
  font-size: 1.15rem;
  font-weight: 650;
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.crm-door-title :deep(.ah-crumb) {
  color: inherit;
  opacity: 0.7;
  text-decoration: none;
}
.crm-door-title :deep(.ah-sep) {
  opacity: 0.5;
}
.crm-door-body {
  min-width: 0;
}
.crm-door-off {
  max-width: 560px;
  padding: 20px 22px;
  border: 1px solid var(--h-line, rgba(0, 0, 0, 0.12));
  border-radius: 12px;
  display: grid;
  gap: 6px;
}
.crm-door-off p {
  margin: 0;
}
</style>
