<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — project → epic lanes with an explicit Ungrouped lane.
  Receives an already-ordered layout (frozen while interaction is held)
  and never reorders on its own. Roving tabindex: only the selected card
  is tabbable; arrow travel and DOM focus are handled by the view.

  Ids that left the latest authorized snapshot are rendered as neutral
  TOMBSTONES only (no key, title, actor, activity, blocker, status or
  tags): a slot that keeps the layout still under the pointer without
  re-rendering data the user may no longer see.
-->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import type { Delivery } from '@/services/agentMode'
import type { AgentModeProjectGroup } from '@/composables/agent-mode/agentModeOrdering'
import AgentModeDeliveryCard from './AgentModeDeliveryCard.vue'

defineProps<{
  groups: readonly AgentModeProjectGroup[]
  deliveriesById: ReadonlyMap<string, Delivery>
  /** Ids kept in a frozen layout that are no longer in the snapshot. */
  tombstoneIds: ReadonlySet<string>
  selectedId: string | null
  /** Detail 10 renders this identity once in the focus anchor above the
   * lanes. Its natural slot remains an honest, non-interactive marker. */
  liftedSelectedId?: string | null
  serverNowMs: number
  locale: string
  /** Last-known data shown while the feed is unreachable. */
  degraded?: boolean
}>()

const emit = defineEmits<{
  select: [id: string]
  activate: [id: string]
  interact: []
}>()

const { t } = useI18n()

function laneLabel(lane: AgentModeProjectGroup['lanes'][number]): string {
  if (lane.ungrouped) return t('agentMode.lanes.ungrouped')
  return [lane.epicKey, lane.epicTitle].filter(Boolean).join(' · ') || t('agentMode.lanes.ungrouped')
}

function laneDomId(key: string): string {
  return `am-lane-${key.replace(/[^a-z0-9]/gi, '-')}`
}
</script>

<template>
  <div class="am-lanes">
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
        :aria-labelledby="laneDomId(lane.key)"
        :data-lane-key="lane.key"
      >
        <div class="am-lane-label">
          <h3 :id="laneDomId(lane.key)">{{ laneLabel(lane) }}</h3>
          <small>{{ t('agentMode.lanes.count', { n: lane.deliveryIds.length }, lane.deliveryIds.length) }}</small>
        </div>
        <div class="am-lane-cards">
          <template v-for="id in lane.deliveryIds" :key="id">
            <div
              v-if="id === liftedSelectedId"
              class="am-selected-above"
              role="note"
              data-selected-above="true"
              :data-layout-id="id"
            >
              <span aria-hidden="true">↟</span>
              <span>{{ t('agentMode.lanes.selectedAbove') }}</span>
            </div>
            <AgentModeDeliveryCard
              v-else-if="deliveriesById.has(id)"
              :delivery="deliveriesById.get(id)!"
              :selected="id === selectedId"
              :tabbable="id === selectedId"
              :server-now-ms="serverNowMs"
              :locale="locale"
              :degraded="degraded"
              @select="emit('select', $event)"
              @activate="emit('activate', $event)"
              @interact="emit('interact')"
            />
            <div v-else-if="tombstoneIds.has(id)" class="am-tombstone" role="note" data-tombstone="true">
              <span>{{ t('agentMode.card.gone') }}</span>
            </div>
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

.am-tombstone {
  display: grid;
  min-height: 148px;
  place-items: center;
  padding: 16px 14px;
  border: 1px dashed var(--am-line-strong);
  border-radius: 14px;
  color: var(--am-muted);
  font-size: 12px;
  text-align: center;
}

.am-selected-above {
  display: flex;
  min-height: 48px;
  align-items: center;
  justify-content: center;
  gap: 7px;
  padding: 10px 12px;
  border: 1px dashed color-mix(in srgb, var(--am-select) 45%, var(--am-line));
  border-radius: 12px;
  background: color-mix(in srgb, var(--am-select) 3%, transparent);
  color: var(--am-muted);
  font-size: 11px;
}
.am-selected-above > span:first-child { color: var(--am-select); font-size: 15px; }

@media (max-width: 760px) {
  .am-lane { grid-template-columns: 1fr; gap: 6px; }
  .am-lane-label { position: static; padding-top: 6px; }
}
</style>
