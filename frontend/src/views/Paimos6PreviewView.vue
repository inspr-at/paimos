<script setup lang="ts">
import { computed, defineAsyncComponent } from 'vue'
import { useRoute } from 'vue-router'
import HabitatControlRoom from '@/components/habitat/HabitatControlRoom.vue'
const route = useRoute()
const Sessions = defineAsyncComponent(() => import('./Paimos6SessionsView.vue'))
const sessions = computed(
  () => route.query.view === 'sessions' || typeof route.query.session === 'string',
)
</script>
<template>
  <Sessions v-if="sessions" />
  <HabitatControlRoom v-else />
</template>
