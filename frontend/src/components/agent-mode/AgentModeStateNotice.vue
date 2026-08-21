<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — honest loading / empty / offline / forbidden / not-found /
  error surface. Nothing here ever shows fabricated deliveries.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import LoadingText from '@/components/LoadingText.vue'
import type { AgentModeStatus } from '@/composables/agent-mode/useAgentModeDeliveries'

const props = defineProps<{
  status: AgentModeStatus
  message?: string | null
  /** Seconds until the next automatic retry (offline). */
  retryInSeconds?: number | null
  attempt?: number
}>()

const emit = defineEmits<{ retry: [] }>()
const { t } = useI18n()

const icon = computed(() => {
  switch (props.status) {
    case 'offline':
      return 'wifi-off'
    case 'forbidden':
      return 'shield'
    case 'not-found':
      return 'search'
    case 'error':
      return 'alert-circle'
    case 'empty':
      return 'inbox'
    default:
      return 'circle'
  }
})
const retryable = computed(() => props.status === 'offline' || props.status === 'error' || props.status === 'empty')
</script>

<template>
  <div class="am-state" :class="`am-state--${status}`" role="status" aria-live="polite">
    <LoadingText v-if="status === 'loading' || status === 'idle'" :label="t('agentMode.state.loading')" align="center" />
    <template v-else>
      <AppIcon :name="icon" :size="22" class="am-state-icon" aria-hidden="true" />
      <h2 class="am-state-title">{{ t(`agentMode.state.${status}.title`) }}</h2>
      <p class="am-state-body">
        {{ t(`agentMode.state.${status}.body`) }}
        <template v-if="status === 'offline' && retryInSeconds != null">
          {{ t('agentMode.state.offline.retryIn', { s: retryInSeconds }) }}
        </template>
      </p>
      <p v-if="message && status !== 'empty'" class="am-state-detail">{{ message }}</p>
      <button v-if="retryable" type="button" class="am-state-retry" @click="emit('retry')">
        <AppIcon name="refresh-cw" :size="12" aria-hidden="true" />
        {{ status === 'empty' ? t('agentMode.state.refresh') : t('agentMode.state.retry') }}
      </button>
    </template>
  </div>
</template>

<style scoped>
.am-state {
  display: grid;
  justify-items: center;
  gap: 6px;
  max-width: 460px;
  margin: 48px auto;
  padding: 26px 22px;
  border: 1px dashed var(--am-line-strong);
  border-radius: 16px;
  text-align: center;
  color: var(--am-muted);
}
.am-state--offline,
.am-state--error { border-color: color-mix(in srgb, var(--am-red) 45%, var(--am-line)); }
.am-state--forbidden,
.am-state--not-found { border-color: color-mix(in srgb, var(--am-amber) 45%, var(--am-line)); }
.am-state-icon { color: var(--am-ink); }
.am-state-title {
  margin: 4px 0 0;
  font-family: 'Bricolage Grotesque', 'DM Sans', sans-serif;
  font-size: 17px;
  font-weight: 500;
  color: var(--am-ink);
}
.am-state-body { margin: 0; font-size: 13px; line-height: 1.5; }
.am-state-detail { margin: 0; font-family: 'JetBrains Mono', ui-monospace, monospace; font-size: 11px; }
.am-state-retry {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-top: 8px;
  padding: 6px 12px;
  border: 1px solid var(--am-line-strong);
  border-radius: 9px;
  background: var(--am-surface);
  color: var(--am-ink);
  font-size: 12px;
  font-weight: 600;
}
.am-state-retry:focus-visible { outline: 2px solid var(--am-focus); outline-offset: 2px; }
</style>
