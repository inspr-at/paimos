<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { codenameOf } from '../../lib/codenames'
import ReleaseName from '../ReleaseName.vue'

// The codename in the product's display voice. The history hero keeps its name
// while its separate status dock reveals the version; detail headings retain
// their existing name-to-version interaction (AEON-430).
const props = defineProps<{ version: string; name?: string; quiet?: boolean; plain?: boolean }>()
defineSlots<{ default?: () => unknown }>()
const label = computed(() => props.name || codenameOf(props.version))
</script>

<template>
  <span class="codename" :class="{ quiet }">
    <span v-if="plain" class="codename-label"><slot>{{ label || version }}</slot></span>
    <ReleaseName v-else :version="version" :name="name"><slot>{{ label }}</slot></ReleaseName>
  </span>
</template>

<style scoped>
.codename { display: contents; }
.codename-label, .codename :deep(.rn-name) {
  white-space: normal; overflow: visible; text-overflow: clip; overflow-wrap: anywhere;
  text-transform: none; font-weight: 300; color: inherit;
}
.quiet { color: var(--ink-2); }
.codename :deep(.rn-stamp) { letter-spacing: 0; }
</style>
