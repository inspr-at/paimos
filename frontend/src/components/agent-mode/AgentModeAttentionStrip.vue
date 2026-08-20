<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 / PAI-807 — attention strip. With schema-v1 aggregates it keeps
  the server's selector-independent order and reason flags; otherwise the
  Detail-10 compatibility path offers up to three normalized deliveries.
  It NEVER changes selection by itself.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import type { Delivery } from '@/services/agentMode'
import type {
  AgentModeAttentionAggregate,
  AgentModeAttentionAggregateItem,
  AttentionReasonKey,
} from '@/services/agentModeAggregateSchema'
import { compareDeliveries } from '@/composables/agent-mode/agentModeOrdering'
import { formatRelativeTimeWithLocale } from '@/composables/useDateFormat'
import AgentModeActivityGlyph from './AgentModeActivityGlyph.vue'

const props = defineProps<{
  deliveries: readonly Delivery[]
  selectedId: string | null
  serverNowMs: number
  locale: string
  max?: number
  authoritative?: AgentModeAttentionAggregate | null
}>()

const emit = defineEmits<{ select: [id: string] }>()
const { t } = useI18n()

interface DisplayItem {
  delivery: Delivery
  aggregate: AgentModeAttentionAggregateItem | null
}

const items = computed<DisplayItem[]>(() => {
  if (props.authoritative) {
    const deliveries = new Map(props.deliveries.map((delivery) => [delivery.id, delivery]))
    return props.authoritative.items
      .filter((item) => item.deliveryId !== props.selectedId)
      .flatMap((aggregate): DisplayItem[] => {
        const delivery = deliveries.get(aggregate.deliveryId)
        return delivery ? [{ delivery, aggregate }] : []
      })
      .slice(0, props.max ?? 3)
  }
  return props.deliveries
    .filter((delivery) => delivery.attention.level > 0 && delivery.id !== props.selectedId)
    .sort(compareDeliveries)
    .slice(0, props.max ?? 3)
    .map((delivery) => ({ delivery, aggregate: null }))
})
const hiddenCount = computed(() => {
  if (props.authoritative) {
    const selectedWasCandidate = props.authoritative.items.some((item) => item.deliveryId === props.selectedId)
    return Math.max(0, props.authoritative.total - (selectedWasCandidate ? 1 : 0) - items.value.length)
  }
  const total = props.deliveries.filter((d) => d.attention.level > 0 && d.id !== props.selectedId).length
  return Math.max(0, total - items.value.length)
})

function since(item: DisplayItem): string | null {
  const value = item.aggregate?.since ?? item.delivery.attention.since
  return value ? formatRelativeTimeWithLocale(value, props.locale, props.serverNowMs) : null
}

function level(item: DisplayItem): number {
  return item.aggregate?.level ?? item.delivery.attention.level
}

function primary(item: DisplayItem): string {
  if (item.aggregate) return t(`agentMode.aggregate.reason.${item.aggregate.primaryReason}`)
  return item.delivery.attention.reason ?? item.delivery.activity.text ?? t(`agentMode.health.${item.delivery.health}`)
}

function reasonLabel(reason: AttentionReasonKey): string {
  return t(`agentMode.aggregate.reason.${reason}`)
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
      <li
        v-for="item in items"
        :key="item.delivery.id"
        class="am-attention-item"
        :class="`am-attention-item--${level(item)}`"
        :data-attention-id="item.delivery.id"
      >
        <span class="am-attention-marker" aria-hidden="true"></span>
        <div class="am-attention-copy">
          <strong><span class="am-attention-key">{{ item.delivery.issueKey }}</span> {{ item.delivery.title }}</strong>
          <small>
            {{ primary(item) }}
            <template v-if="since(item)"> · {{ since(item) }}</template>
          </small>
          <span class="am-attention-context">
            <AgentModeActivityGlyph :kind="item.delivery.activity.kind" />
            {{ item.delivery.lane.projectKey }}<template v-if="item.delivery.lane.epicKey"> / {{ item.delivery.lane.epicKey }}</template>
            · {{ item.delivery.actor?.label ?? t('agentMode.card.noActor') }}
            · {{ t(`agentMode.health.${item.delivery.health}`) }}
            · {{ item.delivery.activity.text ?? t(`agentMode.activity.${item.delivery.activity.kind}`) }}
          </span>
          <span v-if="item.aggregate" class="am-attention-flags" :aria-label="t('agentMode.aggregate.allReasons')">
            <span v-for="flag in item.aggregate.flags" :key="flag">{{ reasonLabel(flag) }}</span>
          </span>
          <span v-if="item.delivery.tags.length" class="am-attention-tags">
            <span v-for="tag in item.delivery.tags" :key="tag">#{{ tag }}</span>
          </span>
        </div>
        <button
          type="button"
          class="am-attention-select"
          :aria-label="t('agentMode.attention.selectLabel', { key: item.delivery.issueKey })"
          @click="emit('select', item.delivery.id)"
        >
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
.am-attention-context { display: flex; align-items: center; gap: 3px; margin-top: 3px; color: var(--am-muted); font-size: 10.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.am-attention-flags,
.am-attention-tags { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 5px; }
.am-attention-flags > span,
.am-attention-tags > span { padding: 1px 5px; border-radius: 999px; background: color-mix(in srgb, var(--am-amber) 10%, var(--am-surface)); color: var(--am-amber); font-size: 9.5px; }
.am-attention-tags > span { background: color-mix(in srgb, var(--am-blue) 9%, var(--am-surface)); color: var(--am-blue); }
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

@media (max-width: 620px) {
  .am-attention-item { grid-template-columns: auto minmax(0, 1fr); }
  .am-attention-select { grid-column: 2; justify-self: start; }
}
</style>
