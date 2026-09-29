<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { readDeployTarget, type DeployApproval } from '../../lib/deployTarget'
import AppIcon from '../AppIcon.vue'

// The server a deploy approval names, always on the surface that asks for the
// decision. A missing target is informational; no authority is inferred here.
const props = defineProps<{ approval?: DeployApproval | null; required?: boolean; compact?: boolean }>()
const view = computed(() => {
  const read = readDeployTarget(props.approval)
  // A deploy surface with no request still has to say the server is unknown.
  if (!read.applicable && props.required) return readDeployTarget({ scope: 'journey.deploy' })
  return read
})
const shown = computed(() => view.value.applicable)
const facts = computed(() => {
  const target = view.value
  if (target.standing !== 'named') return []
  return [
    target.service ? ['Service', target.service] : null,
    target.image ? ['Image', target.image] : null,
    target.change ? ['Change', target.change] : null,
  ].filter((row): row is [string, string] => !!row)
})
</script>

<template>
  <section v-if="shown" class="target" :class="[view.standing, { compact }]" :aria-label="view.aria || 'Target not named'">
    <p class="head">
      <span class="mark" aria-hidden="true"><AppIcon name="server" :size="14" /></span>
      <span class="where">{{ view.where || 'Target not named' }}</span>
    </p>
    <dl v-if="facts.length" class="facts">
      <template v-for="[label, value] in facts" :key="label">
        <dt>{{ label }}</dt>
        <dd>{{ value }}</dd>
      </template>
    </dl>
  </section>
</template>

<style scoped>
.target {
  display: grid; gap: 8px; min-width: 0; padding: 10px 12px; border-radius: 10px;
  background: var(--surface); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink);
}
.target.compact { gap: 6px; padding: 8px 10px; }
.target.unknown { color: var(--ink-2); background: var(--surface); }
.head { display: flex; align-items: center; gap: 8px; min-width: 0; margin: 0; }
.mark {
  display: grid; place-items: center; flex: none; width: 28px; height: 28px; border-radius: 8px;
  background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2);
}
.where { min-width: 0; font-size: 13.5px; font-weight: 650; line-height: 1.35; overflow-wrap: anywhere; }
.facts { display: grid; grid-template-columns: 92px minmax(0, 1fr); gap: 4px 10px; margin: 0; }
.facts dt { margin: 0; font: 600 10px/1.4 var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-3); }
.facts dd { margin: 0; font-size: 13px; line-height: 1.4; overflow-wrap: anywhere; }
@media (max-width: 420px) {
  .facts { grid-template-columns: minmax(0, 1fr); gap: 2px 0; }
  .facts dd { margin-bottom: 6px; }
}
</style>
