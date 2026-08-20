<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-807 — bounded Detail-100 semantic zoom. Exactly one full selected card
  and at most twelve compact attention rows are mounted. Project/lane rows
  are aggregate controls; they never instantiate delivery-card watchers.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import type { Delivery } from '@/services/agentMode'
import {
  AGGREGATE_FLAG_KEYS,
  AGGREGATE_LANDING_KEYS,
  AGGREGATE_STAGE_KEYS,
  type AgentModeAggregates,
  type AgentModeAggregateUnavailableReason,
  type AgentModeCountSet,
  type AgentModeLaneAggregate,
  type AgentModeProjectAggregate,
  type AggregateFlagKey,
  type AggregateLandingKey,
  type AggregateStageKey,
} from '@/services/agentModeAggregateSchema'
import AgentModeAttentionStrip from './AgentModeAttentionStrip.vue'
import AgentModeDeliveryCard from './AgentModeDeliveryCard.vue'

const props = defineProps<{
  aggregates: AgentModeAggregates | null
  unavailableReason: AgentModeAggregateUnavailableReason | null
  deliveries: readonly Delivery[]
  selectedDelivery: Delivery | null
  selectedId: string | null
  serverNowMs: number
  locale: string
  degraded: boolean
}>()

const emit = defineEmits<{
  'drill-selection': [id: string]
  'drill-aggregate': [projectId: number, laneKey: string | null]
  'select-attention': [id: string]
  interact: []
}>()

const { t } = useI18n()

const calculatedLabel = computed(() => {
  if (!props.aggregates) return null
  const parsed = Date.parse(props.aggregates.calculatedAt)
  if (!Number.isFinite(parsed)) return null
  return new Intl.DateTimeFormat(props.locale, { dateStyle: 'medium', timeStyle: 'short' }).format(parsed)
})

function countLabel(counts: AgentModeCountSet): string {
  return t('agentMode.aggregate.activeCount', { n: counts.activeTotal }, counts.activeTotal)
}

function stageLabel(key: AggregateStageKey): string {
  return t(`agentMode.stage.${key}`)
}

function landingLabel(key: AggregateLandingKey): string {
  return t(`agentMode.aggregate.landing.${key}`)
}

function flagLabel(key: AggregateFlagKey): string {
  return t(`agentMode.aggregate.flag.${key}`)
}

function laneLabel(lane: AgentModeLaneAggregate): string {
  return lane.epicId == null
    ? t('agentMode.lanes.ungrouped')
    : [lane.epicKey, lane.epicTitle].filter(Boolean).join(' · ')
}

function projectAria(project: AgentModeProjectAggregate): string {
  return t('agentMode.aggregate.drillProject', {
    project: `${project.projectKey} · ${project.projectName}`,
    count: project.counts.activeTotal,
  })
}

function laneAria(project: AgentModeProjectAggregate, lane: AgentModeLaneAggregate): string {
  return t('agentMode.aggregate.drillLane', {
    project: project.projectKey,
    lane: laneLabel(lane),
    count: lane.counts.activeTotal,
  })
}

function unavailableLabel(reason: AgentModeAggregateUnavailableReason | null): string {
  return t(`agentMode.aggregate.unavailableReason.${reason ?? 'malformed'}`)
}
</script>

<template>
  <section class="am-streams" :aria-label="t('agentMode.streams.title')" data-detail-level="100">
    <header class="am-streams-head">
      <div>
        <span class="am-streams-kicker">{{ t('agentMode.aggregate.semanticZoom') }}</span>
        <h2 class="am-streams-title">{{ t('agentMode.streams.title') }}</h2>
      </div>
      <span v-if="calculatedLabel" class="am-streams-calculated">
        {{ t('agentMode.aggregate.calculatedAt', { at: calculatedLabel }) }}
      </span>
    </header>

    <div v-if="selectedDelivery" class="am-streams-selection" :aria-label="t('agentMode.aggregate.pinnedSelection')">
      <span class="am-streams-section-label">
        <AppIcon name="pin" :size="11" aria-hidden="true" />
        {{ t('agentMode.aggregate.pinnedSelection') }}
      </span>
      <AgentModeDeliveryCard
        :delivery="selectedDelivery"
        :selected="true"
        :tabbable="true"
        :degraded="degraded"
        :server-now-ms="serverNowMs"
        :locale="locale"
        @activate="emit('drill-selection', $event)"
        @interact="emit('interact')"
      />
    </div>

    <div v-if="!aggregates" class="am-streams-unavailable" role="status">
      <AppIcon name="circle-alert" :size="18" aria-hidden="true" />
      <div>
        <h3>{{ t('agentMode.aggregate.unavailable') }}</h3>
        <p>{{ unavailableLabel(unavailableReason) }}</p>
        <small>{{ t('agentMode.aggregate.unavailableGuard') }}</small>
      </div>
    </div>

    <template v-else>
      <section class="am-aggregate-root" :aria-label="t('agentMode.aggregate.portfolioTotal')">
        <div class="am-aggregate-root-total">
          <span>{{ t('agentMode.aggregate.active') }}</span>
          <strong>{{ aggregates.root.activeTotal }}</strong>
          <small>{{ t('agentMode.aggregate.activeUnit') }}</small>
        </div>

        <div class="am-aggregate-partition">
          <h3>{{ t('agentMode.aggregate.currentStage') }}</h3>
          <ul class="am-aggregate-stage-list">
            <li v-for="key in AGGREGATE_STAGE_KEYS" :key="key">
              <span>{{ stageLabel(key) }}</span><strong>{{ aggregates.root.currentStage[key] }}</strong>
            </li>
          </ul>
        </div>

        <div class="am-aggregate-partition">
          <h3>{{ t('agentMode.aggregate.landingTitle') }}</h3>
          <ul class="am-aggregate-landing-list">
            <li v-for="key in AGGREGATE_LANDING_KEYS" :key="key">
              <span>{{ landingLabel(key) }}</span><strong>{{ aggregates.root.landing[key] }}</strong>
            </li>
          </ul>
        </div>

        <div class="am-aggregate-flags" :aria-label="t('agentMode.aggregate.overlappingFlags')">
          <span
            v-for="key in AGGREGATE_FLAG_KEYS"
            :key="key"
            class="am-aggregate-flag"
            :class="{ 'is-active': aggregates.root.flags[key] > 0, 'is-risk': key === 'blocked' || key === 'failed_needs_retry' }"
          >
            <i aria-hidden="true"></i>{{ flagLabel(key) }} <strong>{{ aggregates.root.flags[key] }}</strong>
          </span>
        </div>
      </section>

      <AgentModeAttentionStrip
        :deliveries="deliveries"
        :selected-id="selectedId"
        :server-now-ms="serverNowMs"
        :locale="locale"
        :authoritative="aggregates.attention"
        :max="12"
        @select="emit('select-attention', $event)"
      />

      <section class="am-aggregate-groups" :aria-label="t('agentMode.aggregate.deliveryStreams')">
        <div class="am-streams-section-label">
          <AppIcon name="layers-3" :size="12" aria-hidden="true" />
          {{ t('agentMode.aggregate.deliveryStreams') }}
        </div>

        <article
          v-for="project in aggregates.projects"
          :key="project.projectId"
          class="am-aggregate-project"
          :data-project-id="project.projectId"
        >
          <button
            type="button"
            class="am-aggregate-project-control"
            :aria-label="projectAria(project)"
            @click="emit('drill-aggregate', project.projectId, null)"
          >
            <span class="am-aggregate-project-identity">
              <span>{{ project.projectKey }}</span>
              <strong>{{ project.projectName }}</strong>
            </span>
            <span class="am-aggregate-total">{{ countLabel(project.counts) }}</span>
            <AppIcon name="zoom-in" :size="14" aria-hidden="true" />
          </button>

          <div class="am-aggregate-lanes">
            <button
              v-for="lane in project.lanes"
              :key="lane.laneKey"
              type="button"
              class="am-aggregate-lane-control"
              :aria-label="laneAria(project, lane)"
              :data-lane-key="lane.laneKey"
              @click="emit('drill-aggregate', project.projectId, lane.laneKey)"
            >
              <span class="am-aggregate-lane-main">
                <strong>{{ laneLabel(lane) }}</strong>
                <span>{{ countLabel(lane.counts) }}</span>
              </span>
              <span class="am-aggregate-lane-stages" :aria-label="t('agentMode.aggregate.currentStage')">
                <span v-for="key in AGGREGATE_STAGE_KEYS" :key="key">
                  <small>{{ stageLabel(key) }}</small><strong>{{ lane.counts.currentStage[key] }}</strong>
                </span>
              </span>
              <span class="am-aggregate-lane-health" :aria-label="t('agentMode.aggregate.overlappingFlags')">
                <span v-for="key in AGGREGATE_FLAG_KEYS.filter((flag) => lane.counts.flags[flag] > 0)" :key="key">
                  {{ flagLabel(key) }} {{ lane.counts.flags[key] }}
                </span>
                <span v-if="!AGGREGATE_FLAG_KEYS.some((flag) => lane.counts.flags[flag] > 0)">
                  {{ t('agentMode.aggregate.noFlags') }}
                </span>
              </span>
              <span class="am-aggregate-lane-landing" :aria-label="t('agentMode.aggregate.landingTitle')">
                <span v-for="key in AGGREGATE_LANDING_KEYS" :key="key">
                  {{ landingLabel(key) }} <strong>{{ lane.counts.landing[key] }}</strong>
                </span>
              </span>
            </button>
          </div>
        </article>
      </section>
    </template>
  </section>
</template>

<style scoped>
.am-streams {
  --am-d100-unit: 8px;
  display: grid;
  gap: calc(var(--am-d100-unit) * 2);
  width: min(1120px, 100%);
}
.am-streams-head { display: flex; align-items: end; justify-content: space-between; gap: 16px; }
.am-streams-kicker,
.am-streams-section-label { color: var(--am-muted); font-size: 10.5px; font-weight: 650; letter-spacing: 0.075em; text-transform: uppercase; }
.am-streams-section-label { display: flex; align-items: center; gap: 6px; }
.am-streams-title { margin: 3px 0 0; font-family: 'Bricolage Grotesque', 'DM Sans', sans-serif; font-size: 19px; font-weight: 600; }
.am-streams-calculated { color: var(--am-muted); font-size: 10.5px; }
.am-streams-selection { display: grid; gap: 7px; }

.am-streams-unavailable {
  display: flex;
  gap: 12px;
  align-items: flex-start;
  padding: 18px;
  border: 1px solid color-mix(in srgb, var(--am-amber) 42%, var(--am-line));
  border-radius: 14px;
  background: color-mix(in srgb, var(--am-amber) 7%, var(--am-surface));
  color: var(--am-amber);
}
.am-streams-unavailable h3 { margin: 0; font-size: 14px; }
.am-streams-unavailable p { margin: 4px 0; color: var(--am-ink); font-size: 12px; }
.am-streams-unavailable small { color: var(--am-muted); font-size: 10.5px; }

.am-aggregate-root {
  display: grid;
  grid-template-columns: minmax(100px, 0.55fr) minmax(230px, 1.5fr) minmax(230px, 1.5fr);
  gap: 12px;
  padding: 14px;
  border: 1px solid var(--am-line);
  border-radius: 16px;
  background: var(--am-surface);
}
.am-aggregate-root-total { display: flex; flex-direction: column; justify-content: center; padding: 10px 14px; border-right: 1px solid var(--am-line); }
.am-aggregate-root-total > span { color: var(--am-muted); font-size: 10px; letter-spacing: 0.06em; text-transform: uppercase; }
.am-aggregate-root-total > strong { font-family: 'Bricolage Grotesque', 'DM Sans', sans-serif; font-size: 36px; line-height: 1.05; font-variant-numeric: tabular-nums; }
.am-aggregate-root-total > small { color: var(--am-muted); font-size: 10.5px; }
.am-aggregate-partition h3 { margin: 0 0 7px; color: var(--am-muted); font-size: 10px; letter-spacing: 0.055em; text-transform: uppercase; }
.am-aggregate-stage-list,
.am-aggregate-landing-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 5px 12px; margin: 0; padding: 0; list-style: none; }
.am-aggregate-stage-list li,
.am-aggregate-landing-list li { display: flex; justify-content: space-between; gap: 8px; color: var(--am-muted); font-size: 10.5px; }
.am-aggregate-stage-list strong,
.am-aggregate-landing-list strong { color: var(--am-ink); font-family: 'JetBrains Mono', ui-monospace, monospace; font-variant-numeric: tabular-nums; }
.am-aggregate-flags { grid-column: 1 / -1; display: flex; flex-wrap: wrap; gap: 6px; padding-top: 11px; border-top: 1px solid var(--am-line); }
.am-aggregate-flag { display: inline-flex; align-items: center; gap: 5px; padding: 3px 7px; border: 1px solid var(--am-line); border-radius: 999px; color: var(--am-muted); font-size: 10px; }
.am-aggregate-flag i { width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.am-aggregate-flag strong { font-family: 'JetBrains Mono', ui-monospace, monospace; font-variant-numeric: tabular-nums; }
.am-aggregate-flag.is-active { border-color: color-mix(in srgb, var(--am-amber) 42%, var(--am-line)); color: var(--am-amber); }
.am-aggregate-flag.is-risk.is-active { border-color: color-mix(in srgb, var(--am-red) 45%, var(--am-line)); color: var(--am-red); }

.am-aggregate-groups { display: grid; gap: 10px; }
.am-aggregate-project { overflow: hidden; border: 1px solid var(--am-line); border-radius: 14px; background: var(--am-surface); }
.am-aggregate-project-control,
.am-aggregate-lane-control { width: 100%; border: 0; background: transparent; color: var(--am-ink); text-align: left; }
.am-aggregate-project-control { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; align-items: center; gap: 12px; min-height: calc(var(--am-d100-unit) * 7); padding: 10px 14px; border-bottom: 1px solid var(--am-line); }
.am-aggregate-project-control:hover,
.am-aggregate-lane-control:hover { background: color-mix(in srgb, var(--am-blue) 5%, var(--am-surface)); }
.am-aggregate-project-control:focus-visible,
.am-aggregate-lane-control:focus-visible { position: relative; z-index: 1; outline: 2px solid var(--am-focus); outline-offset: -3px; }
.am-aggregate-project-identity { min-width: 0; display: flex; align-items: baseline; gap: 9px; }
.am-aggregate-project-identity > span { color: var(--am-blue); font-family: 'JetBrains Mono', ui-monospace, monospace; font-size: 11px; }
.am-aggregate-project-identity > strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
.am-aggregate-total { color: var(--am-muted); font-size: 11px; }
.am-aggregate-lanes { display: grid; }
.am-aggregate-lane-control { display: grid; grid-template-columns: minmax(150px, 1.2fr) minmax(250px, 2fr) minmax(170px, 1.3fr); grid-template-areas: 'main stages health' 'main landing landing'; gap: 7px 16px; min-height: calc(var(--am-d100-unit) * 9); padding: 10px 14px; border-top: 1px solid var(--am-line); }
.am-aggregate-lane-control:first-child { border-top: 0; }
.am-aggregate-lane-main { grid-area: main; align-self: center; min-width: 0; display: grid; gap: 3px; }
.am-aggregate-lane-main strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.am-aggregate-lane-main > span { color: var(--am-muted); font-size: 10.5px; }
.am-aggregate-lane-stages { grid-area: stages; display: grid; grid-template-columns: repeat(6, minmax(30px, 1fr)); gap: 4px; }
.am-aggregate-lane-stages > span { display: grid; gap: 2px; padding: 3px 4px; border-radius: 5px; background: color-mix(in srgb, var(--am-blue) 6%, var(--am-surface)); text-align: center; }
.am-aggregate-lane-stages small { overflow: hidden; text-overflow: ellipsis; color: var(--am-muted); font-size: 8.5px; white-space: nowrap; }
.am-aggregate-lane-stages strong { font-family: 'JetBrains Mono', ui-monospace, monospace; font-size: 10px; }
.am-aggregate-lane-health { grid-area: health; display: flex; align-items: center; flex-wrap: wrap; gap: 4px; }
.am-aggregate-lane-health > span { color: var(--am-muted); font-size: 9px; }
.am-aggregate-lane-health > span:not(:last-child)::after { content: ' ·'; }
.am-aggregate-lane-landing { grid-area: landing; display: flex; flex-wrap: wrap; gap: 4px 10px; color: var(--am-muted); font-size: 8.75px; }
.am-aggregate-lane-landing strong { color: var(--am-ink); font-family: 'JetBrains Mono', ui-monospace, monospace; }

@media (max-width: 860px) {
  .am-aggregate-root { grid-template-columns: 90px minmax(0, 1fr); }
  .am-aggregate-root .am-aggregate-partition:last-of-type { grid-column: 1 / -1; padding-top: 10px; border-top: 1px solid var(--am-line); }
  .am-aggregate-lane-control { grid-template-columns: minmax(130px, 0.8fr) minmax(220px, 1.6fr); grid-template-areas: 'main stages' 'health health' 'landing landing'; }
}

@media (max-width: 620px) {
  .am-streams-head { align-items: start; flex-direction: column; gap: 5px; }
  .am-aggregate-root { grid-template-columns: 1fr; }
  .am-aggregate-root-total { border-right: 0; border-bottom: 1px solid var(--am-line); }
  .am-aggregate-root .am-aggregate-partition:last-of-type,
  .am-aggregate-flags { grid-column: 1; }
  .am-aggregate-project-control { grid-template-columns: minmax(0, 1fr) auto; }
  .am-aggregate-project-control > svg { display: none; }
  .am-aggregate-lane-control { grid-template-columns: minmax(0, 1fr); grid-template-areas: 'main' 'stages' 'health' 'landing'; }
  .am-aggregate-lane-stages { overflow-x: auto; }
}

@media (prefers-reduced-motion: reduce) {
  .am-aggregate-project-control,
  .am-aggregate-lane-control { scroll-behavior: auto; transition: none; }
}
</style>
