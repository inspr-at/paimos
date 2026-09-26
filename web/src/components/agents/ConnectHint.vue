<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'
defineProps<{ canStart: boolean }>()
const emit = defineEmits<{ start: [] }>()
</script>

<template>
  <div class="connect">
    <span class="halo"><AppIcon name="agent" :size="22" /></span>
    <h3>No agent has connected yet</h3>
    <p class="lead">{{ canStart ? 'Start an agent to queue a run; its daemon connects when an account is ready.' : 'Ask a workspace admin to start an agent; its daemon connects when an account is ready.' }}</p>
    <button v-if="canStart" type="button" class="btn sm" @click="emit('start')"><AppIcon name="plus" :size="14" />Start agent</button>
  </div>
</template>

<style scoped>
.connect { display: grid; justify-items: center; gap: 10px; padding: 40px 28px 44px; border-top: 1px solid var(--line); text-align: center; }
.halo { display: grid; place-items: center; width: 52px; height: 52px; margin-bottom: 4px; border-radius: 16px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line), 0 8px 24px -12px var(--teal); color: var(--teal-ink); }
.lead { max-width: 56ch; font-size: 13.5px; color: var(--ink-2); }
</style>
