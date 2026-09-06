<script setup lang="ts">
import { RouterLink } from 'vue-router'
import { computed } from 'vue'
import type { Delivery } from '@/services/agentMode'
import { estimatePresentation } from '@/composables/agent-mode/agentModeTrust'
import { humanize } from './habitatModel'
const props = defineProps<{ delivery: Delivery; fresh: boolean; detail?: boolean }>()
const estimate = computed(() => estimatePresentation(props.delivery))
</script>
<template>
  <article class="habitat-delivery">
    <RouterLink :to="`/projects/${delivery.lane.projectId}/issues/${delivery.issueId}`"
      ><strong>{{ delivery.issueKey }}</strong> · {{ delivery.title }}</RouterLink
    >
    <p>{{ humanize(delivery.stage.key) }} · {{ humanize(delivery.health) }}</p>
    <template v-if="fresh && estimate.showPercent"
      ><progress
        class="habitat-progress"
        :value="estimate.percent ?? 0"
        max="100"
        :aria-label="`${delivery.issueKey} trusted progress`"
      ></progress
      ><span>{{ estimate.percent }}% · trusted evidence</span></template
    >
    <p v-else>Progress unknown · {{ humanize(estimate.percentReason) }}</p>
    <p v-if="fresh && estimate.showEta">
      Trusted ETA ·
      {{
        estimate.rangeOnly
          ? `${estimate.optimisticAt} – ${estimate.pessimisticAt}`
          : estimate.landingAt
      }}
    </p>
    <p v-else>ETA unknown · {{ humanize(estimate.etaReason) }}</p>
    <details v-if="detail">
      <summary>Stage &amp; freshness evidence</summary>
      <ul>
        <li v-for="stage in delivery.stages" :key="stage.key">
          {{ humanize(stage.key) }} · {{ humanize(stage.status) }}
        </li>
      </ul>
      <p>
        {{ humanize(delivery.freshness.state) }} ·
        {{ delivery.freshness.lastReportAt ?? 'Report time unknown' }}
      </p>
    </details>
  </article>
</template>
<style scoped>
.habitat-delivery {
  padding: 15px 0;
  border-top: 1px solid var(--h-line);
  font-size: 12px;
  overflow-wrap: anywhere;
}
.habitat-delivery p {
  margin-block: 6px;
}
</style>
