<!--
  PAI-805 — the single Detail-10 selection target. It is lifted above
  Attention so restored, voice and keyboard selections remain visible in the
  initial canvas. The natural lane renders only an honest "selected above"
  placeholder; it never renders a second selected/accessibility target.

  The lifted target deliberately composes the canonical delivery card. That
  keeps identity, actor, tags, activity, stage, health, freshness, blocker,
  progress/ETA, status and selected/attention semantics identical between the
  natural lanes and the persistent selection anchor.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import type { FilterExclusion } from '@/composables/agent-mode/agentModeFilters'
import type { Delivery } from '@/services/agentMode'
import AgentModeDeliveryCard from './AgentModeDeliveryCard.vue'

const props = defineProps<{
  delivery: Delivery
  serverNowMs: number
  locale: string
  excludedBy?: FilterExclusion | null
  degraded?: boolean
}>()

const emit = defineEmits<{
  activate: [id: string]
  interact: []
}>()

const { t } = useI18n()
const laneLabel = computed(() =>
  props.delivery.lane.epicId == null
    ? t('agentMode.lanes.ungrouped')
    : [props.delivery.lane.epicKey, props.delivery.lane.epicTitle].filter(Boolean).join(' · '),
)
const breadcrumb = computed(() =>
  `${props.delivery.lane.projectKey} · ${props.delivery.lane.projectName} / ${laneLabel.value}`,
)
</script>

<template>
  <section class="am-selection-anchor" :aria-label="breadcrumb">
    <div class="am-selection-anchor__context" aria-hidden="true">
      <span class="am-selection-anchor__label">
        <i></i>{{ t('agentMode.selection.label') }}
      </span>
      <span class="am-selection-anchor__breadcrumb">{{ breadcrumb }}</span>
    </div>

    <AgentModeDeliveryCard
      :delivery="delivery"
      :selected="true"
      :tabbable="true"
      :server-now-ms="serverNowMs"
      :locale="locale"
      :pinned-reason="excludedBy ?? null"
      :degraded="degraded ?? false"
      @activate="emit('activate', $event)"
      @interact="emit('interact')"
    />
  </section>
</template>

<style scoped>
.am-selection-anchor {
  min-width: 0;
  padding: 2px;
}

.am-selection-anchor__context {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
  margin: 0 10px 9px;
}

.am-selection-anchor__label {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 5px;
  color: var(--am-select);
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.05em;
  text-transform: uppercase;
}

.am-selection-anchor__label i {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}

.am-selection-anchor__breadcrumb {
  min-width: 0;
  overflow: hidden;
  color: var(--am-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 700px) {
  .am-selection-anchor__context {
    display: grid;
    gap: 3px;
    margin-inline: 8px;
  }
}
</style>
