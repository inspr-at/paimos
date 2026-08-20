<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — detail-1 SEAM. Shows the persistent selected delivery with the
  same card semantics as detail 10 (identity, lane, stage, activity,
  health, freshness, estimate truth) plus previous/next and zoom-out.
  PAI-806 replaces the body with the full detail-1 canvas (stage chain,
  evidence, Open ticket → IssueSidePanel) while keeping this contract:
    props   → delivery, position/total, serverNowMs, locale
    emits   → prev, next, zoom-out, open-ticket (no-op here)
-->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import type { Delivery } from '@/services/agentMode'
import AgentModeDeliveryCard from './AgentModeDeliveryCard.vue'

defineProps<{
  delivery: Delivery
  position: number
  total: number
  serverNowMs: number
  locale: string
}>()

const emit = defineEmits<{
  prev: []
  next: []
  'zoom-out': []
  'open-ticket': [issueId: number]
  interact: []
}>()

const { t } = useI18n()
</script>

<template>
  <section class="am-focus" :aria-label="t('agentMode.detail.focusAria')">
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

    <div class="am-focus-lane">
      <span class="am-focus-lane-key">{{ delivery.lane.projectKey }}</span>
      {{ delivery.lane.projectName }}
      <span class="am-focus-lane-sep">/</span>
      {{ delivery.lane.epicKey ? `${delivery.lane.epicKey} · ${delivery.lane.epicTitle ?? ''}` : t('agentMode.lanes.ungrouped') }}
      <template v-if="delivery.tags.length">
        <span class="am-focus-lane-sep">·</span>
        <span class="am-focus-tags">{{ delivery.tags.join(', ') }}</span>
      </template>
    </div>

    <AgentModeDeliveryCard
      class="am-focus-card"
      :delivery="delivery"
      :selected="true"
      :tabbable="true"
      size="lg"
      :server-now-ms="serverNowMs"
      :locale="locale"
      @activate="emit('open-ticket', delivery.issueId)"
      @interact="emit('interact')"
    />

    <p class="am-focus-seam">{{ t('agentMode.detail.seamOne') }}</p>
  </section>
</template>

<style scoped>
.am-focus { display: grid; gap: 14px; max-width: 720px; margin: 0 auto; }
.am-focus-bar { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
.am-focus-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 5px 10px;
  border: 1px solid var(--am-line);
  border-radius: 8px;
  background: var(--am-surface);
  color: var(--am-ink);
  font-size: 12px;
}
.am-focus-btn:disabled { opacity: 0.5; cursor: default; }
.am-focus-btn:focus-visible { outline: 2px solid var(--am-focus); outline-offset: 2px; }
.am-focus-btn kbd {
  padding: 0 5px;
  border: 1px solid var(--am-line);
  border-radius: 4px;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 10px;
  color: var(--am-muted);
}
.am-focus-pos { font-family: 'JetBrains Mono', ui-monospace, monospace; font-size: 11px; color: var(--am-muted); }
.am-focus-nav { display: inline-flex; gap: 6px; }
.am-focus-lane { font-size: 12px; color: var(--am-muted); }
.am-focus-lane-key {
  margin-right: 6px;
  padding: 1px 6px;
  border-radius: 6px;
  background: color-mix(in srgb, var(--am-ink) 8%, var(--am-surface));
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 11px;
  font-weight: 600;
  color: var(--am-ink);
}
.am-focus-lane-sep { margin: 0 6px; }
.am-focus-tags { font-style: italic; }
.am-focus-card { margin-top: 6px; }
.am-focus-seam { margin: 4px 0 0; font-size: 11px; color: var(--am-muted); }
</style>
