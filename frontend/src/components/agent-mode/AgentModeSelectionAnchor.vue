<!--
  PAI-805 — the single Detail-10 selection target. It is lifted above
  Attention so restored, voice and keyboard selections remain visible in the
  initial canvas. The natural lane renders only an honest "selected above"
  placeholder; it never renders a second selected/accessibility target.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import type { FilterExclusion } from '@/composables/agent-mode/agentModeFilters'
import type { Delivery } from '@/services/agentMode'
import {
  activityKey,
  estimateView,
  healthIcon,
  healthKey,
  relativeReported,
  stageLabelKey,
  stagePosition,
} from './agentModePresentation'

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
const d = computed(() => props.delivery)
const laneLabel = computed(() =>
  d.value.lane.epicId == null
    ? t('agentMode.lanes.ungrouped')
    : [d.value.lane.epicKey, d.value.lane.epicTitle].filter(Boolean).join(' · '),
)
const breadcrumb = computed(() => `${d.value.lane.projectKey} · ${d.value.lane.projectName} / ${laneLabel.value}`)
const activity = computed(() => d.value.activity.text ?? t('agentMode.activity.none'))
const stage = computed(() => {
  const label = d.value.stage.label ?? t(stageLabelKey(d.value))
  const pos = stagePosition(d.value)
  return pos ? `${label} · ${pos}` : label
})
const reported = computed(() => relativeReported(d.value, props.locale, props.serverNowMs) ?? t('agentMode.freshness.noReport'))
const estimate = computed(() => estimateView(d.value, props.locale, props.serverNowMs))
const eta = computed(() => {
  if (props.degraded) return t('agentMode.estimate.withheld.offline')
  if (estimate.value.presentation.showEta && estimate.value.landingLabel) {
    return t('agentMode.estimate.lands', { time: estimate.value.landingLabel })
  }
  const reason = estimate.value.presentation.etaReason
  return t(`agentMode.estimate.withheld.${reason === 'ok' ? 'none' : reason}`)
})
const exclusion = computed(() =>
  props.excludedBy
    ? t('agentMode.selection.outsideResults', { filter: t(`agentMode.card.pinnedReason.${props.excludedBy}`) })
    : '',
)

function activate() {
  emit('interact')
  emit('activate', d.value.id)
}

function onKeydown(event: KeyboardEvent) {
  if (event.key !== 'Enter' && event.key !== ' ') return
  event.preventDefault()
  activate()
}
</script>

<template>
  <article
    class="am-selection-anchor"
    :class="{ 'is-attention': d.attention.level > 0, 'is-degraded': degraded }"
    :data-delivery-id="d.id"
    data-selected="true"
  >
    <button
      type="button"
      class="am-selection-anchor__hit am-card-hit"
      :data-card-hit="d.id"
      aria-current="true"
      @click="activate"
      @keydown="onKeydown"
      @pointerdown="emit('interact')"
    >
      <span class="am-selection-anchor__label"><i aria-hidden="true"></i>{{ t('agentMode.selection.label') }}</span>
      <span class="am-selection-anchor__breadcrumb">{{ breadcrumb }}</span>
      <span class="am-selection-anchor__identity">
        <span class="am-selection-anchor__key">{{ d.issueKey }}</span>
        <strong>{{ d.title }}</strong>
      </span>
      <span class="am-selection-anchor__now">
        <AppIcon :name="healthIcon(d)" :size="12" aria-hidden="true" />
        <b>{{ t(healthKey(d)) }}</b>
        <span>{{ t(activityKey(d)) }} · {{ activity }}</span>
      </span>
      <span class="am-selection-anchor__facts">
        <span>{{ stage }}</span>
        <span>{{ t('agentMode.card.reported') }} {{ reported }}</span>
        <span>{{ eta }}</span>
      </span>
      <span v-if="exclusion" class="am-selection-anchor__excluded">
        <AppIcon name="filter" :size="11" aria-hidden="true" />{{ exclusion }}
      </span>
    </button>
  </article>
</template>

<style scoped>
.am-selection-anchor {
  position: relative;
  min-width: 0;
  overflow: hidden;
  border: 1px solid var(--am-select);
  border-radius: 13px;
  background: color-mix(in srgb, var(--am-select) 4%, var(--am-surface));
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--am-select) 12%, transparent);
}
.am-selection-anchor.is-attention { border-left: 4px solid var(--am-amber); }
.am-selection-anchor.is-degraded { border-style: dashed; }
.am-selection-anchor__hit {
  display: grid;
  width: 100%;
  min-width: 0;
  grid-template-columns: auto minmax(0, 1fr) auto;
  grid-template-areas:
    'label breadcrumb breadcrumb'
    'identity identity now'
    'facts facts facts'
    'excluded excluded excluded';
  gap: 5px 12px;
  padding: 11px 14px 10px;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.am-selection-anchor__hit:focus-visible { outline: 2px solid var(--am-focus); outline-offset: -4px; }
.am-selection-anchor__label {
  grid-area: label;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--am-select);
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.05em;
  text-transform: uppercase;
}
.am-selection-anchor__label i { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.am-selection-anchor__breadcrumb {
  grid-area: breadcrumb;
  min-width: 0;
  overflow: hidden;
  color: var(--am-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.am-selection-anchor__identity { grid-area: identity; display: flex; min-width: 0; align-items: baseline; gap: 9px; }
.am-selection-anchor__identity strong { overflow: hidden; font-size: 14px; text-overflow: ellipsis; white-space: nowrap; }
.am-selection-anchor__key { flex: none; color: var(--am-muted); font-family: 'JetBrains Mono', ui-monospace, monospace; font-size: 10.5px; }
.am-selection-anchor__now { grid-area: now; display: flex; min-width: 0; align-items: center; gap: 5px; font-size: 11.5px; white-space: nowrap; }
.am-selection-anchor__now > span { max-width: 270px; overflow: hidden; color: var(--am-muted); text-overflow: ellipsis; }
.am-selection-anchor__facts { grid-area: facts; display: flex; min-width: 0; gap: 16px; color: var(--am-muted); font-size: 10.5px; }
.am-selection-anchor__facts > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.am-selection-anchor__excluded { grid-area: excluded; display: inline-flex; align-items: center; gap: 5px; color: var(--am-amber); font-size: 10.5px; }

@media (max-width: 700px) {
  .am-selection-anchor__hit {
    grid-template-columns: minmax(0, 1fr);
    grid-template-areas: 'label' 'breadcrumb' 'identity' 'now' 'facts' 'excluded';
    gap: 4px;
    padding: 10px 12px;
  }
  .am-selection-anchor__now > span { max-width: none; }
  .am-selection-anchor__facts { gap: 10px; }
  .am-selection-anchor__facts > span:nth-child(2) { display: none; }
}
</style>
