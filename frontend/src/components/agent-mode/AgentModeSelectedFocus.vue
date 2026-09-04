<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-806 — Detail level 1. This is semantic zoom over the exact Delivery
  object selected in levels 10/100. The ticket editor is deliberately a
  separate, explicit action; entering this component never opens it.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import type { Delivery, DeliveryStage, StageKey } from '@/services/agentMode'
import AgentModeDeliveryCard from './AgentModeDeliveryCard.vue'
import AgentModeWorkerContext from './AgentModeWorkerContext.vue'
import { estimateView } from './agentModePresentation'

const props = defineProps<{
  delivery: Delivery
  position: number
  total: number
  serverNowMs: number
  locale: string
  degraded?: boolean
  ticketOpen?: boolean
  ticketAvailable?: boolean
}>()

const emit = defineEmits<{
  prev: []
  next: []
  'zoom-out': []
  'open-ticket': []
  interact: []
}>()

const { t } = useI18n()
const STAGE_CHAIN: StageKey[] = ['specification', 'implementation', 'qa', 'deployment', 'verification']
const stages = computed<Array<{ key: StageKey; fact: DeliveryStage | null }>>(() => {
  const byKey = new Map(props.delivery.stages.map((stage) => [stage.key, stage]))
  return STAGE_CHAIN.map((key) => ({ key, fact: byKey.get(key) ?? null }))
})
const estimate = computed(() => estimateView(props.delivery, props.locale, props.serverNowMs, props.degraded === true))
const attemptLabel = computed(() => {
  const attempt = props.delivery.attempt
  if (attempt.number != null) return t('agentMode.detail.attemptNumber', { n: attempt.number })
  return attempt.id ? t('agentMode.detail.attempt') : t('agentMode.detail.attemptUnknown')
})
</script>

<template>
  <section
    class="am-focus"
    :aria-label="t('agentMode.detail.focusAria')"
    :data-delivery-id="delivery.id"
    :data-lane-key="delivery.lane.key"
    :data-attempt-id="delivery.attempt.id ?? ''"
    :data-stage-key="delivery.stage.key"
    :data-plan-revision="delivery.attempt.planRevision ?? ''"
    :data-delivery-revision="delivery.deliveryRevision ?? ''"
    :data-trust-revision="delivery.trustRevision ?? ''"
  >
    <div class="am-focus-bar">
      <button type="button" class="am-focus-btn" @click="emit('zoom-out')">
        <AppIcon name="arrow-left" :size="12" aria-hidden="true" />
        {{ t('agentMode.detail.zoomOut') }}
        <kbd>Esc</kbd>
      </button>
      <span class="am-focus-pos">{{ t('agentMode.detail.position', { i: position, n: total }) }}</span>
      <div class="am-focus-nav">
        <button type="button" class="am-focus-btn" :disabled="position <= 1" @click="emit('prev')">
          <AppIcon name="chevron-left" :size="12" aria-hidden="true" />{{ t('agentMode.detail.prev') }}
        </button>
        <button type="button" class="am-focus-btn" :disabled="position >= total" @click="emit('next')">
          {{ t('agentMode.detail.next') }}<AppIcon name="chevron-right" :size="12" aria-hidden="true" />
        </button>
      </div>
    </div>

    <div class="am-focus-heading">
      <div>
        <h2 class="am-focus-title">{{ t('agentMode.detail.focusTitle') }}</h2>
        <div class="am-focus-lane">
          <span class="am-focus-lane-key">{{ delivery.lane.projectKey }}</span>
          {{ delivery.lane.projectName }}
          <span class="am-focus-lane-sep">/</span>
          {{ delivery.lane.epicKey ? `${delivery.lane.epicKey} · ${delivery.lane.epicTitle ?? ''}` : t('agentMode.lanes.ungrouped') }}
        </div>
        <div class="am-focus-lineage">
          <span>{{ attemptLabel }}</span>
          <span v-if="delivery.attempt.id" :title="delivery.attempt.id">{{ delivery.attempt.id }}</span>
          <span v-if="delivery.attempt.planRevision">{{ t('agentMode.detail.plan') }} {{ delivery.attempt.planRevision }}</span>
        </div>
      </div>
      <button
        type="button"
        class="am-focus-open-ticket"
        aria-controls="agent-mode-ticket-panel"
        :aria-expanded="ticketOpen ? 'true' : 'false'"
        :disabled="!ticketAvailable"
        :title="ticketAvailable ? undefined : t('agentMode.detail.ticketUnavailable')"
        @click="emit('open-ticket')"
      >
        <AppIcon name="panel-right-open" :size="14" aria-hidden="true" />
        {{ t('agentMode.detail.openTicket') }}
      </button>
    </div>

    <AgentModeDeliveryCard
      class="am-focus-card"
      :delivery="delivery"
      :selected="true"
      :tabbable="true"
      :activatable="false"
      :degraded="degraded"
      size="lg"
      :server-now-ms="serverNowMs"
      :locale="locale"
      @interact="emit('interact')"
    />

    <AgentModeWorkerContext :project-id="delivery.lane.projectId" :ticket-id="delivery.issueId" />

    <section class="am-focus-section" :aria-labelledby="`am-stage-title-${delivery.id}`">
      <div class="am-focus-section-head">
        <h3 :id="`am-stage-title-${delivery.id}`">{{ t('agentMode.detail.stageChain') }}</h3>
        <span>{{ t('agentMode.detail.requiredChain') }}</span>
      </div>
      <ol class="am-stage-chain">
        <li
          v-for="stage in stages"
          :key="stage.key"
          class="am-stage"
          :class="`am-stage--${stage.fact?.status ?? 'unknown'}`"
          :aria-current="delivery.stage.key === stage.key ? 'step' : undefined"
        >
          <span class="am-stage-marker" aria-hidden="true"></span>
          <div class="am-stage-copy">
            <strong>{{ stage.fact?.label ?? t(`agentMode.stage.${stage.key}`) }}</strong>
            <span class="am-stage-status">{{ t(`agentMode.stageStatus.${stage.fact?.status ?? 'unknown'}`) }}</span>
            <small v-if="stage.fact?.owner">{{ stage.fact.owner.label }} · {{ t(`agentMode.actorKind.${stage.fact.owner.kind}`) }}</small>
            <small v-if="stage.fact?.activity">{{ stage.fact.activity }}</small>
            <small v-if="stage.fact?.blockers.length" class="am-stage-blocker">{{ stage.fact.blockers[0].text }}</small>
            <small v-if="stage.fact?.evidence.length">{{ t('agentMode.detail.evidenceCount', { n: stage.fact.evidence.length }) }}</small>
            <small v-else>{{ t('agentMode.detail.noEvidence') }}</small>
          </div>
        </li>
      </ol>
    </section>

    <div class="am-focus-detail-grid">
      <section class="am-focus-section">
        <h3>{{ t('agentMode.detail.estimateTruth') }}</h3>
        <dl class="am-detail-list">
          <div><dt>{{ t('agentMode.detail.progress') }}</dt><dd>{{ estimate.presentation.showPercent ? `${estimate.presentation.percent} %` : t(`agentMode.estimate.withheld.${estimate.presentation.percentReason}`) }}</dd></div>
          <div>
            <dt>{{ t('agentMode.detail.landing') }}</dt>
            <dd>
              {{ estimate.presentation.rangeOnly && estimate.rangeLabel
                ? t('agentMode.estimate.range', { range: estimate.rangeLabel })
                : estimate.landingLabel ?? t(`agentMode.estimate.withheld.${estimate.presentation.etaReason}`) }}
            </dd>
          </div>
          <div v-if="estimate.rangeLabel && !estimate.presentation.rangeOnly"><dt>{{ t('agentMode.detail.range') }}</dt><dd>{{ estimate.rangeLabel }}</dd></div>
          <div v-if="estimate.basis"><dt>{{ t('agentMode.detail.basis') }}</dt><dd>{{ estimate.basis }}</dd></div>
          <div v-if="delivery.trustRevision"><dt>{{ t('agentMode.detail.trustRevision') }}</dt><dd>{{ delivery.trustRevision }}</dd></div>
          <div v-if="delivery.suppressionCodes.length"><dt>{{ t('agentMode.detail.suppressed') }}</dt><dd>{{ delivery.suppressionCodes.join(' · ') }}</dd></div>
          <div v-if="delivery.disagreementCodes.length"><dt>{{ t('agentMode.detail.disagreement') }}</dt><dd>{{ delivery.disagreementCodes.join(' · ') }}</dd></div>
        </dl>
      </section>

      <section class="am-focus-section">
        <h3>{{ t('agentMode.detail.blockers') }}</h3>
        <ul v-if="delivery.blockers.length" class="am-detail-items">
          <li v-for="blocker in delivery.blockers" :key="`${blocker.kind}:${blocker.text}`">
            <strong>{{ blocker.kind }}</strong><span>{{ blocker.text }}</span>
          </li>
        </ul>
        <p v-else class="am-detail-empty">{{ t('agentMode.detail.noBlockers') }}</p>
      </section>

      <section class="am-focus-section">
        <h3>{{ t('agentMode.detail.handoffs') }}</h3>
        <ul v-if="delivery.handoffs.length" class="am-detail-items">
          <li v-for="(handoff, index) in delivery.handoffs" :key="handoff.id ?? index">
            <strong>{{ handoff.from?.label ?? t('agentMode.card.noActor') }} → {{ handoff.to?.label ?? t('agentMode.card.noActor') }}</strong>
            <span>{{ handoff.summary ?? handoff.status ?? t('agentMode.detail.unknown') }}</span>
          </li>
        </ul>
        <p v-else class="am-detail-empty">{{ t('agentMode.detail.noHandoffs') }}</p>
      </section>

      <section class="am-focus-section">
        <h3>{{ t('agentMode.detail.evidence') }}</h3>
        <ul v-if="delivery.evidence.length" class="am-detail-items">
          <li v-for="(item, index) in delivery.evidence" :key="item.id ?? index">
            <strong>{{ item.label ?? item.kind }}</strong>
            <span>{{ item.summary ?? item.status ?? t('agentMode.detail.unknown') }}</span>
          </li>
        </ul>
        <p v-else class="am-detail-empty">{{ t('agentMode.detail.noEvidence') }}</p>
      </section>
    </div>
  </section>
</template>

<style scoped>
.am-focus { display: grid; gap: 16px; max-width: 980px; margin: 0 auto; }
.am-focus-bar { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
.am-focus-btn,
.am-focus-open-ticket {
  display: inline-flex; align-items: center; gap: 6px; min-height: 34px; padding: 6px 11px;
  border: 1px solid var(--am-line); border-radius: 8px; background: var(--am-surface); color: var(--am-ink); font-size: 12px;
}
.am-focus-btn:disabled { opacity: 0.5; cursor: default; }
.am-focus-open-ticket:disabled { opacity: 0.5; cursor: not-allowed; }
.am-focus-btn:focus-visible,
.am-focus-open-ticket:focus-visible { outline: 2px solid var(--am-focus); outline-offset: 2px; }
.am-focus-btn kbd { padding: 0 5px; border: 1px solid var(--am-line); border-radius: 4px; font: 10px 'JetBrains Mono', ui-monospace, monospace; color: var(--am-muted); }
.am-focus-pos,
.am-focus-lineage { font: 11px 'JetBrains Mono', ui-monospace, monospace; color: var(--am-muted); }
.am-focus-nav { display: inline-flex; gap: 6px; }
.am-focus-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.am-focus-title { margin: 0 0 5px; font-family: 'Bricolage Grotesque', 'DM Sans', sans-serif; font-size: 17px; font-weight: 600; }
.am-focus-lane { font-size: 12px; color: var(--am-muted); }
.am-focus-lane-key { margin-right: 6px; padding: 1px 6px; border-radius: 6px; background: color-mix(in srgb, var(--am-ink) 8%, var(--am-surface)); font: 600 11px 'JetBrains Mono', ui-monospace, monospace; color: var(--am-ink); }
.am-focus-lane-sep { margin: 0 6px; }
.am-focus-lineage { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 5px; }
.am-focus-open-ticket { flex: 0 0 auto; border-color: var(--am-select); color: var(--am-select); font-weight: 600; }
.am-focus-card { margin-top: 2px; }
.am-focus-section { min-width: 0; padding: 14px; border: 1px solid var(--am-line); border-radius: 12px; background: color-mix(in srgb, var(--am-surface) 94%, var(--am-shell)); }
.am-focus-section h3 { margin: 0 0 10px; font-size: 12px; font-weight: 700; letter-spacing: .04em; text-transform: uppercase; }
.am-focus-section-head { display: flex; justify-content: space-between; align-items: baseline; gap: 10px; }
.am-focus-section-head > span { color: var(--am-muted); font-size: 11px; }
.am-stage-chain { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 0; margin: 0; padding: 0; list-style: none; }
.am-stage { position: relative; min-width: 0; padding: 0 8px; }
.am-stage::before { content: ''; position: absolute; top: 7px; left: 0; right: 0; height: 2px; background: var(--am-line); }
.am-stage:first-child::before { left: 50%; }
.am-stage:last-child::before { right: 50%; }
.am-stage-marker { position: relative; z-index: 1; display: block; width: 14px; height: 14px; margin: 0 auto 8px; border: 2px solid var(--am-line-strong); border-radius: 50%; background: var(--am-surface); }
.am-stage--succeeded .am-stage-marker { border-color: var(--am-green); background: var(--am-green); }
.am-stage--active .am-stage-marker { border-color: var(--am-select); box-shadow: 0 0 0 4px color-mix(in srgb, var(--am-select) 16%, transparent); }
.am-stage--blocked .am-stage-marker,
.am-stage--failed .am-stage-marker { border-color: var(--am-red); background: var(--am-red); }
.am-stage--waiting .am-stage-marker { border-color: var(--am-amber); }
.am-stage-copy { display: grid; gap: 3px; text-align: center; overflow-wrap: anywhere; }
.am-stage-copy strong { font-size: 11px; }
.am-stage-copy small,
.am-stage-status { color: var(--am-muted); font-size: 10px; line-height: 1.3; }
.am-stage-blocker { color: var(--am-red) !important; }
.am-focus-detail-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.am-detail-list { display: grid; gap: 7px; margin: 0; }
.am-detail-list > div { display: grid; grid-template-columns: minmax(90px, .45fr) minmax(0, 1fr); gap: 8px; font-size: 11px; }
.am-detail-list dt { color: var(--am-muted); }
.am-detail-list dd { margin: 0; overflow-wrap: anywhere; }
.am-detail-items { display: grid; gap: 7px; margin: 0; padding: 0; list-style: none; }
.am-detail-items li { display: grid; gap: 2px; font-size: 11px; }
.am-detail-items span,
.am-detail-empty { color: var(--am-muted); }
.am-detail-empty { margin: 0; font-size: 11px; }
@media (max-width: 760px) {
  .am-stage-chain { grid-template-columns: 1fr; gap: 8px; }
  .am-stage { display: grid; grid-template-columns: 18px minmax(0, 1fr); padding: 0; }
  .am-stage::before { top: 0; bottom: -8px; left: 7px !important; right: auto !important; width: 2px; height: auto; }
  .am-stage:last-child::before { display: none; }
  .am-stage-marker { margin: 1px 0 0; }
  .am-stage-copy { text-align: left; padding-left: 7px; }
  .am-focus-detail-grid { grid-template-columns: 1fr; }
  .am-focus-heading { align-items: stretch; flex-direction: column; }
  .am-focus-open-ticket { justify-content: center; width: 100%; }
}
</style>
