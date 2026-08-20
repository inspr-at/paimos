<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — project → epic lanes with an explicit Ungrouped lane.
  Receives an already-ordered layout (frozen while interaction is held)
  and never reorders on its own. Roving tabindex: only the selected card
  is tabbable; arrow travel is handled by the view.
-->
<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import type { Delivery } from '@/services/agentMode'
import type { AgentModeProjectGroup } from '@/composables/agent-mode/agentModeOrdering'
import AgentModeDeliveryCard from './AgentModeDeliveryCard.vue'

const props = defineProps<{
  groups: readonly AgentModeProjectGroup[]
  deliveriesById: ReadonlyMap<string, Delivery>
  /** Last-known state for ids retained in a frozen layout. */
  retainedById: ReadonlyMap<string, Delivery>
  selectedId: string | null
  serverNowMs: number
  locale: string
  /** Increment to move DOM focus onto the selected card. */
  focusToken: number
}>()

const emit = defineEmits<{
  select: [id: string]
  activate: [id: string]
  interact: []
}>()

const { t } = useI18n()
const root = ref<HTMLElement | null>(null)

function cssEscape(value: string): string {
  if (typeof CSS !== 'undefined' && typeof CSS.escape === 'function') return CSS.escape(value)
  return value.replace(/["\\]/g, '\\$&')
}

function deliveryFor(id: string): Delivery | null {
  return props.deliveriesById.get(id) ?? props.retainedById.get(id) ?? null
}

function isGone(id: string): boolean {
  return !props.deliveriesById.has(id) && props.retainedById.has(id)
}

function laneLabel(lane: AgentModeProjectGroup['lanes'][number]): string {
  if (lane.ungrouped) return t('agentMode.lanes.ungrouped')
  return [lane.epicKey, lane.epicTitle].filter(Boolean).join(' · ') || t('agentMode.lanes.ungrouped')
}

watch(
  () => props.focusToken,
  async () => {
    await nextTick()
    if (!props.selectedId || !root.value) return
    const el = root.value.querySelector<HTMLElement>(`[data-card-hit="${cssEscape(props.selectedId)}"]`)
    el?.focus({ preventScroll: false })
  },
)

defineExpose({
  focusSelected() {
    if (!props.selectedId || !root.value) return
    root.value.querySelector<HTMLElement>(`[data-card-hit="${cssEscape(props.selectedId)}"]`)?.focus()
  },
})
</script>

<template>
  <div ref="root" class="am-lanes">
    <section
      v-for="group in groups"
      :key="group.projectId"
      class="am-project"
      :aria-labelledby="`am-project-${group.projectId}`"
    >
      <h2 :id="`am-project-${group.projectId}`" class="am-project-head">
        <span class="am-project-key">{{ group.projectKey }}</span>
        <span class="am-project-name">{{ group.projectName }}</span>
        <span class="am-project-count">{{ t('agentMode.lanes.count', { n: group.count }, group.count) }}</span>
      </h2>
      <div
        v-for="lane in group.lanes"
        :key="lane.key"
        class="am-lane"
        :class="{ 'am-lane--ungrouped': lane.ungrouped }"
        role="group"
        :aria-labelledby="`am-lane-${lane.key.replace(/[^a-z0-9]/gi, '-')}`"
        :data-lane-key="lane.key"
      >
        <div class="am-lane-label">
          <h3 :id="`am-lane-${lane.key.replace(/[^a-z0-9]/gi, '-')}`">{{ laneLabel(lane) }}</h3>
          <small>{{ t('agentMode.lanes.count', { n: lane.deliveryIds.length }, lane.deliveryIds.length) }}</small>
        </div>
        <div class="am-lane-cards">
          <template v-for="id in lane.deliveryIds" :key="id">
            <AgentModeDeliveryCard
              v-if="deliveryFor(id)"
              :delivery="deliveryFor(id)!"
              :selected="id === selectedId"
              :tabbable="id === selectedId"
              :server-now-ms="serverNowMs"
              :locale="locale"
              :gone="isGone(id)"
              @select="emit('select', $event)"
              @activate="emit('activate', $event)"
              @interact="emit('interact')"
            />
          </template>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.am-lanes { display: grid; gap: 22px; }
.am-project { display: grid; gap: 12px; }
.am-project-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin: 0;
  font-family: 'Bricolage Grotesque', 'DM Sans', sans-serif;
  font-size: 15px;
  font-weight: 600;
  letter-spacing: -0.01em;
}
.am-project-key {
  padding: 1px 7px;
  border-radius: 6px;
  background: color-mix(in srgb, var(--am-ink) 8%, var(--am-surface));
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 11px;
  font-weight: 600;
}
.am-project-count { font-size: 12px; font-weight: 400; color: var(--am-muted); }

.am-lane {
  display: grid;
  grid-template-columns: 118px minmax(0, 1fr);
  gap: 12px;
  align-items: start;
}
.am-lane-label {
  position: sticky;
  top: 0;
  padding: 10px 8px 8px 0;
  border-top: 2px solid var(--am-blue);
  color: var(--am-blue);
}
.am-lane--ungrouped .am-lane-label { border-top-style: dashed; color: var(--am-muted); }
.am-lane-label h3 { margin: 0; font-size: 12px; font-weight: 600; line-height: 1.3; }
.am-lane-label small { display: block; margin-top: 3px; font-size: 11px; color: var(--am-muted); }
.am-lane-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 300px), 1fr));
  gap: 14px 12px;
  padding-top: 10px;
}

@media (max-width: 760px) {
  .am-lane { grid-template-columns: 1fr; gap: 6px; }
  .am-lane-label { position: static; padding-top: 6px; }
}
</style>
