<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import ModelBoard from '../../src/components/settings/models/ModelBoard.vue'
import ModelBoardRoute from '../../src/components/settings/models/ModelBoardRoute.vue'
import TooltipHost from '../../src/components/TooltipHost.vue'
import { useSession } from '../../src/stores/session'
import { refreshPermissions } from '../../src/lib/authz'
import { boardPerson } from '../models-board-fixtures'
import { toasts } from '../../src/lib/toast'
const board = ref<InstanceType<typeof ModelBoard>>()
const session = useSession(), route = useRoute()
const german = computed(() => route.query.lang === 'de')
onMounted(async () => { session.identity = { tenant: { id: 'board-tenant', name: 'INSPR' }, principal: { id: boardPerson, name: 'Markus', kind: route.query.agent ? 'agent' : 'person' } }; await refreshPermissions() })
function changePerson() { session.identity = { tenant: { id: 'board-tenant', name: 'INSPR' }, principal: { id: '22222222-2222-4222-8222-222222222222', name: 'Other person', kind: 'person' } } }
</script>
<template>
  <div class="app-shell"><main><template v-if="route.path !== '/settings/models/board'"><header class="test-header"><h1>Models</h1><button data-template @click="board?.previewTemplate('best', $event)">Template</button><button data-change-person @click="changePerson">Change person</button></header><article><h2>{{ german ? 'Modelle und ihre Reihenfolge' : 'Models and their order' }}</h2><ModelBoard ref="board" :german="german" :projects="[{ id: 'project-a', name: 'AEON' }]" /></article><div class="tail" /></template></main></div>
  <ModelBoardRoute v-if="route.path === '/settings/models/board'" /><TooltipHost />
  <aside class="test-toasts"><div v-for="toast in toasts" :key="toast.id" role="status">{{ toast.message }}<button v-for="action in toast.actions" :key="action.label" @click="action.run">{{ action.label }}</button></div></aside>
</template>
<style scoped>.app-shell { height: 100dvh; }.app-shell > main { height: 100%; overflow: auto; padding: 24px; }.test-header { display: flex; justify-content: space-between; margin-bottom: 24px; }.test-header h1 { font-size: 24px; }.test-header button { background: transparent; color: var(--ink-3); border: 1px solid var(--line); border-radius: 8px; padding: 8px; }article { padding: 24px; border-radius: 20px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); }article h2 { font-size: 18px; }.tail { height: 600px; }.test-toasts { position: fixed; bottom: 12px; left: 12px; z-index: 100; display: grid; gap: 8px; }.test-toasts > div { padding: 12px; background: var(--surface-raised); border: 1px solid var(--line); border-radius: 8px; }.test-toasts button { margin-left: 12px; }@media (max-width: 860px) { .app-shell > main { padding: 12px; }article { padding: 12px; } }</style>
