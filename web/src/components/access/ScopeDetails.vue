<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { permissionLabel, type Permission } from '../../lib/access'
import RiskBadge from './RiskBadge.vue'

defineProps<{ scope: Permission }>()
</script>

<template>
  <span class="scope-text">
    <span class="scope-name">{{ permissionLabel(scope.key) }}<RiskBadge :risk="scope.risk" /></span>
    <span class="mono scope-id">{{ scope.key }}</span>
    <span class="scope-description">{{ scope.description }}</span>
    <slot />
  </span>
</template>

<style scoped>
.scope-text { display: grid; min-width: 0; gap: 3px; font-size: 13px; line-height: 1.4; color: var(--ink); overflow-wrap: anywhere; }
.scope-name { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.scope-id { font-size: 11px; color: var(--ink-3); }
.scope-description { font-size: 12px; color: var(--ink-2); }
</style>
