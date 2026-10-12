<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Compact rows open line 1 with a 12 px harness mark (AEON-1211, design AEON-1196). The Execution
// column is hidden there, so the mark names the model, the harness and the account in its tooltip
// and accessible name, e.g. "Opus 5.5 · xhigh · Claude · Claude Max".
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import HarnessMark from './HarnessMark.vue'
import type { SessionExecution } from './sessionRow'
const props = defineProps<{ harness: string; harnessLabel: string; exec: SessionExecution }>()
const text = computed(() => (props.exec.kind === 'ai' ? [props.exec.modelLine, props.harnessLabel, props.exec.account] : [props.exec.accountLine, props.exec.model]).filter(Boolean).join(' · '))
</script>

<template>
  <span class="op-mark" role="img" :aria-label="text" :data-tip="text">
    <HarnessMark v-if="exec.kind === 'ai'" :harness="harness" :size="12" />
    <AppIcon v-else :name="exec.kind === 'media' ? 'image' : 'terminal'" :size="12" />
  </span>
</template>

<style scoped>
.op-mark { display: inline-grid; place-items: center; flex: none; width: 12px; height: 12px; color: var(--ink-2); }
.op-mark :deep(svg) { width: 12px; height: 12px; }
</style>
