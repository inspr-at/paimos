<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { IntakeExtensions } from '../../lib/journey'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ extensions?: IntakeExtensions }>()
// No local namespace registry exists yet. Every version retains the same
// neutral disclosure; data is plain text, including HTML-looking evidence.
const entries = computed(() => Object.entries(props.extensions ?? {}).map(([key, value]) => ({
  key, namespace: key.split('@')[0], version: value.version, data: JSON.stringify(value.data, null, 2),
})))
</script>

<template>
  <section v-if="entries.length" class="extensions" aria-label="Extension data">
    <p class="eyebrow">Extension data</p>
    <details v-for="entry in entries" :key="entry.key" class="extension">
      <summary :aria-label="`${entry.namespace} · version ${entry.version}`">
        <AppIcon name="chevron-right" :size="12" class="chevron" />
        <span class="namespace" :title="entry.key">{{ entry.namespace }}</span>
        <span class="version">v{{ entry.version }}</span>
      </summary>
      <pre tabindex="0" :aria-label="`${entry.namespace} version ${entry.version} data`">{{ entry.data }}</pre>
    </details>
  </section>
</template>

<style scoped>
.extensions { display: grid; gap: 6px; min-width: 0; }
.eyebrow { margin: 0; color: var(--ink-3); font-size: 11px; }
.extension { min-width: 0; border: 1px solid var(--line); border-radius: 8px; background: var(--surface); }
summary { display: flex; align-items: center; gap: 8px; min-height: 36px; padding: 8px 10px; cursor: pointer; list-style: none; }
summary::-webkit-details-marker { display: none; }
summary:focus-visible, pre:focus-visible { outline: 2px solid var(--ink-2); outline-offset: 2px; border-radius: 6px; }
.chevron { flex: none; color: var(--ink-3); }
details[open] .chevron { transform: rotate(90deg); }
.namespace { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 12px var(--mono); color: var(--ink-2); }
.version { flex: none; font: 11px var(--mono); color: var(--ink-3); }
pre { margin: 0; padding: 0 10px 10px; max-height: 300px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; font: 12px/1.6 var(--mono); color: var(--ink); }
</style>
