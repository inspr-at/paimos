<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — attention strip. Offers up to three deliveries that need the
  user; it NEVER changes selection by itself. Selecting requires an
  explicit click on "Select".
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import type { Delivery } from '@/services/agentMode'
import { compareDeliveries } from '@/composables/agent-mode/agentModeOrdering'
import { formatRelativeTimeWithLocale } from '@/composables/useDateFormat'

const props = defineProps<{
  deliveries: readonly Delivery[]
  selectedId: string | null
  serverNowMs: number
  locale: string
  max?: number
}>()

const emit = defineEmits<{ select: [id: string] }>()
const { t } = useI18n()

const items = computed(() =>
  props.deliveries
    .filter((d) => d.attention.level > 0 && d.id !== props.selectedId)
    .sort(compareDeliveries)
    .slice(0, props.max ?? 3),
)
const hiddenCount = computed(() => {
  const total = props.deliveries.filter((d) => d.attention.level > 0 && d.id !== props.selectedId).length
  return Math.max(0, total - items.value.length)
})

function since(d: Delivery): string | null {
  if (!d.attention.since) return null
  return formatRelativeTimeWithLocale(d.attention.since, props.locale, props.serverNowMs)
}
</script>

<template>
  <section v-if="items.length" class="am-attention" :aria-label="t('agentMode.attention.title')">
    <div class="am-attention-head">
      <AppIcon name="hourglass" :size="13" aria-hidden="true" />
      <strong>{{ t('agentMode.attention.title') }}</strong>
      <span class="am-attention-hint">{{ t('agentMode.attention.offerHint') }}</span>
    </div>
    <ul class="am-attention-list">
      <li v-for="d in items" :key="d.id" class="am-attention-item" :class="`am-attention-item--${d.attention.level}`">
        <span class="am-attention-marker" aria-hidden="true"></span>
        <div class="am-attention-copy">
          <strong><span class="am-attention-key">{{ d.issueKey }}</span> {{ d.title }}</strong>
          <small>
            {{ d.attention.reason ?? d.activity.text ?? t(`agentMode.health.${d.health}`) }}
            <template v-if="since(d)"> · {{ since(d) }}</template>
          </small>
        </div>
        <button type="button" class="am-attention-select" @click="emit('select', d.id)">
          {{ t('agentMode.attention.select') }}
        </button>
      </li>
    </ul>
    <p v-if="hiddenCount" class="am-attention-more">{{ t('agentMode.attention.more', { n: hiddenCount }) }}</p>
  </section>
</template>

<style scoped>
.am-attention {
  padding: 12px 14px;
  border: 1px solid color-mix(in srgb, var(--am-amber) 38%, var(--am-line));
  border-radius: 14px;
  background: color-mix(in srgb, var(--am-amber) 7%, var(--am-surface));
}
.am-attention-head { display: flex; align-items: center; gap: 8px; color: var(--am-amber); font-size: 12px; }
.am-attention-head strong { font-weight: 600; }
.am-attention-hint { margin-left: auto; color: var(--am-muted); font-size: 11px; }
.am-attention-list { display: grid; gap: 6px; margin: 10px 0 0; padding: 0; list-style: none; }
.am-attention-item {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid var(--am-line);
  border-radius: 10px;
  background: var(--am-surface);
}
.am-attention-marker {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--am-amber);
  box-shadow: 0 0 0 4px color-mix(in srgb, var(--am-amber) 16%, transparent);
}
.am-attention-item--3 .am-attention-marker { background: var(--am-red); box-shadow: 0 0 0 4px color-mix(in srgb, var(--am-red) 16%, transparent); }
.am-attention-copy { min-width: 0; }
.am-attention-copy strong { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; font-weight: 600; }
.am-attention-key { font-family: 'JetBrains Mono', ui-monospace, monospace; font-weight: 500; color: var(--am-muted); margin-right: 4px; }
.am-attention-copy small { display: block; margin-top: 2px; font-size: 11px; color: var(--am-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.am-attention-select {
  padding: 4px 10px;
  border: 1px solid var(--am-line-strong);
  border-radius: 8px;
  background: var(--am-surface);
  color: var(--am-ink);
  font-size: 11px;
  font-weight: 600;
}
.am-attention-select:hover { border-color: var(--am-select); color: var(--am-select); }
.am-attention-select:focus-visible { outline: 2px solid var(--am-focus); outline-offset: 2px; }
.am-attention-more { margin: 8px 0 0; font-size: 11px; color: var(--am-muted); }
</style>
