<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { useSession } from '../../src/stores/session'
import { refreshPermissions } from '../../src/lib/authz'
import ModelRegistryCard from '../../src/components/settings/models/ModelRegistryCard.vue'
import TooltipHost from '../../src/components/TooltipHost.vue'
import ToastHost from '../../src/components/ToastHost.vue'
const session = useSession(), route = useRoute()
// The card alone, in the 720 px column of Settings › Models, with a person signed in.
onMounted(async () => {
  session.identity = { tenant: { id: 'registry-tenant', name: 'INSPR' }, principal: { id: '11111111-1111-4111-8111-111111111111', name: 'Markus', kind: route.query.agent ? 'agent' : 'person' } }
  await refreshPermissions()
})
</script>
<template><div class="app-shell"><main class="harness-scroll"><div class="column"><ModelRegistryCard /></div></main></div><TooltipHost /><ToastHost /></template>
<style scoped>.app-shell { height: 100dvh; }.harness-scroll { height: 100%; overflow: auto; padding: 20px; }.column { max-width: 720px; margin: 0 auto; }</style>
