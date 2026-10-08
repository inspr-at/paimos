<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { useSession } from '../../src/stores/session'
import { refreshPermissions } from '../../src/lib/authz'
import { boardPerson } from '../models-board-fixtures'
import TooltipHost from '../../src/components/TooltipHost.vue'
import ToastHost from '../../src/components/ToastHost.vue'
const session = useSession(), route = useRoute()
onMounted(async () => {
  session.identity = { tenant: { id: 'board-tenant', name: 'INSPR' }, principal: { id: boardPerson, name: 'Markus', kind: route.query.agent ? 'agent' : 'person' } }
  await refreshPermissions()
})
function changePerson() { session.identity = { tenant: { id: 'board-tenant', name: 'INSPR' }, principal: { id: '22222222-2222-4222-8222-222222222222', name: 'Other person', kind: 'person' } } }
</script>
<template><div class="app-shell"><main class="harness-scroll"><RouterView /></main><button v-if="route.query.identity" class="identity-control" data-change-person @click="changePerson">Change person</button></div><TooltipHost /><ToastHost /></template>
<style scoped>.app-shell { height: 100dvh; }.harness-scroll { height: 100%; overflow: auto; }.identity-control { position: fixed; bottom: 4px; left: 4px; z-index: 120; }</style>
