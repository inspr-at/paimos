<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — delivery card (detail 10).

  Conveys without animation: identity, responsible agent/system, current
  activity, lifecycle stage, health, freshness, blockers, supplemental
  tags, and progress/ETA only when the trust policy authorizes it.
  Visual states are distinct and can stack:
    selected  → accent border + ring + "Selected" label + aria-current
                (the strongest persistent anchor)
    attention → amber rail + "Needs you" label — shown TOGETHER with
                "Selected" when both apply
    focus     → ink focus ring (offset) on the hit area
    hover     → raised surface
    health    → icon + word (color-independent)
    freshness → clock line; stale = dashed border + muted
    degraded  → last-known data while the feed is unreachable: dashed,
                qualified, exact estimates withheld
  One pointer click selects; clicking the selected card (or Enter/Space
  on it) activates (drill to the focused delivery) when `activatable`.

  Accessible name: the hit area is a <button> whose name is its visible
  text, prefixed once (sr-only) by the state flags that are rendered
  outside the button. Those visible flags and the progress bar are
  aria-hidden so nothing is announced twice.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import type { Delivery } from '@/services/agentMode'
import type { FilterExclusion } from '@/composables/agent-mode/agentModeFilters'
import AgentModeActivityGlyph from './AgentModeActivityGlyph.vue'
import {
  MAX_VISIBLE_TAGS,
  actorInitials,
  activityKey,
  estimateView,
  freshnessKey,
  healthIcon,
  healthKey,
  relativeReported,
  stageLabelKey,
  stagePosition,
} from './agentModePresentation'

const props = withDefaults(
  defineProps<{
    delivery: Delivery
    selected: boolean
    /** Whether this card should be in the tab order (roving tabindex). */
    tabbable?: boolean
    /** Server-aligned "now" for freshness + remaining time. */
    serverNowMs: number
    locale: string
    /** Pinned above results because a filter excludes it. */
    pinnedReason?: FilterExclusion | null
    /** Larger rendering for the focused-delivery level. */
    size?: 'md' | 'lg'
    /** Whether activating the selected card drills further (false in the
     * focused-delivery level, where the card already is the focus). */
    activatable?: boolean
    /** Last-known data shown while the feed is unreachable. */
    degraded?: boolean
  }>(),
  { tabbable: false, pinnedReason: null, size: 'md', activatable: true, degraded: false },
)

const emit = defineEmits<{
  select: [id: string]
  activate: [id: string]
  interact: []
}>()

const { t } = useI18n()

const d = computed(() => props.delivery)
const attention = computed(() => d.value.attention.level > 0)
const stale = computed(() => d.value.freshness.state === 'stale' || d.value.freshness.state === 'unknown')
const initials = computed(() => actorInitials(d.value))
const activityText = computed(() => d.value.activity.text ?? t('agentMode.activity.none'))
const activityKindLabel = computed(() => t(activityKey(d.value)))
const stage = computed(() => {
  const label = d.value.stage.label ?? t(stageLabelKey(d.value))
  const pos = stagePosition(d.value)
  return pos ? `${label} · ${pos}` : label
})
const reported = computed(() => relativeReported(d.value, props.locale, props.serverNowMs))
const estimate = computed(() => estimateView(d.value, props.locale, props.serverNowMs))
/** Exact percent / landing time are shown only when trusted AND the data
 * is current. Retained offline data must not keep false precision. */
const showPercent = computed(() => !props.degraded && estimate.value.presentation.showPercent)
const showEta = computed(() => !props.degraded && estimate.value.presentation.showEta)
const withheldReason = computed(() => {
  if (props.degraded) return 'offline'
  const p = estimate.value.presentation
  if (p.showEta || p.showPercent) return null
  // Prefer the ETA reason: it is what the user asks for first.
  const reason = p.etaReason !== 'none' ? p.etaReason : p.percentReason
  return reason === 'ok' ? null : reason
})
const blocker = computed(() => d.value.blockers[0]?.text ?? null)
const actorLabel = computed(() => {
  if (!d.value.actor) return t('agentMode.card.noActor')
  const kind = t(`agentMode.actorKind.${d.value.actor.kind}`)
  return `${d.value.actor.label} · ${kind}`
})
const visibleTags = computed(() => d.value.tags.slice(0, MAX_VISIBLE_TAGS))
const hiddenTagCount = computed(() => Math.max(0, d.value.tags.length - MAX_VISIBLE_TAGS))
const pinnedReasonLabel = computed(() =>
  props.pinnedReason ? t('agentMode.card.pinned', { filter: t(`agentMode.card.pinnedReason.${props.pinnedReason}`) }) : '',
)
// Screen readers get the visible card text as the button name; only the
// state flags (rendered outside the button, aria-hidden there) are
// repeated — exactly once, concisely, first.
const srPrefix = computed(() => {
  const parts = [
    props.selected ? t('agentMode.card.selected') : '',
    attention.value ? t('agentMode.card.attention') : '',
    pinnedReasonLabel.value,
  ].filter(Boolean)
  return parts.length ? `${parts.join('. ')}. ` : ''
})

function activateOrSelect() {
  emit('interact')
  if (!props.selected) emit('select', d.value.id)
  else if (props.activatable) emit('activate', d.value.id)
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    activateOrSelect()
  }
}
</script>

<template>
  <article
    class="am-card"
    :class="[
      `am-card--${d.health}`,
      `am-card--${size}`,
      {
        'is-selected': selected,
        'is-attention': attention,
        'is-stale': stale,
        'is-degraded': degraded,
        'is-pinned': !!pinnedReason,
      },
    ]"
    :data-delivery-id="d.id"
    :data-selected="selected ? 'true' : 'false'"
  >
    <span v-if="selected || attention" class="am-card-flags" aria-hidden="true">
      <span v-if="selected" class="am-card-flag am-card-flag--selected">
        <i></i>{{ t('agentMode.card.selected') }}
      </span>
      <span v-if="attention" class="am-card-flag am-card-flag--attention">
        {{ t('agentMode.card.attention') }}
      </span>
    </span>

    <button
      type="button"
      class="am-card-hit"
      :tabindex="tabbable ? 0 : -1"
      :aria-current="selected ? 'true' : undefined"
      :data-card-hit="d.id"
      @click="activateOrSelect"
      @keydown="onKeydown"
      @pointerdown="emit('interact')"
    >
      <span v-if="srPrefix" class="am-sr-only">{{ srPrefix }}</span>

      <div class="am-card-head">
        <span class="am-card-actor" aria-hidden="true">{{ initials }}</span>
        <div class="am-card-id">
          <span class="am-card-key">{{ d.issueKey }}</span>
          <strong class="am-card-title">{{ d.title }}</strong>
        </div>
        <span class="am-card-health" :class="`am-card-health--${d.health}`">
          <AppIcon :name="healthIcon(d)" :size="12" aria-hidden="true" />
          <span>{{ t(healthKey(d)) }}</span>
        </span>
      </div>

      <div class="am-card-now">
        <AgentModeActivityGlyph :kind="d.activity.kind" :size="size === 'lg' ? 'md' : 'sm'" />
        <span class="am-card-now-kind">{{ activityKindLabel }}</span>
        <span class="am-card-now-text" :class="{ 'is-empty': !d.activity.text }">{{ activityText }}</span>
      </div>

      <dl class="am-card-facts">
        <div>
          <dt>{{ t('agentMode.card.by') }}</dt>
          <dd>{{ actorLabel }}</dd>
        </div>
        <div>
          <dt>{{ t('agentMode.card.stage') }}</dt>
          <dd>{{ stage }}</dd>
        </div>
        <div class="am-card-fresh" :class="`am-card-fresh--${d.freshness.state}`">
          <dt>{{ t('agentMode.card.reported') }}</dt>
          <dd>
            <span>{{ reported ?? t('agentMode.freshness.noReport') }}</span>
            <span v-if="stale" class="am-card-fresh-state">· {{ t(freshnessKey(d)) }}</span>
          </dd>
        </div>
      </dl>

      <p v-if="degraded" class="am-card-retained">
        <AppIcon name="wifi-off" :size="11" aria-hidden="true" />
        <span>{{ t('agentMode.card.retained') }}</span>
      </p>

      <p v-if="d.tags.length" class="am-card-tags">
        <span class="am-sr-only">{{ t('agentMode.card.tagsSummary', { list: d.tags.join(', ') }) }}</span>
        <span aria-hidden="true" class="am-card-tags-visible">
          <span v-for="tag in visibleTags" :key="tag" class="am-card-tag">#{{ tag }}</span>
          <span v-if="hiddenTagCount" class="am-card-tag am-card-tag--more">{{ t('agentMode.card.tagsMore', { n: hiddenTagCount }) }}</span>
        </span>
      </p>

      <p v-if="blocker" class="am-card-blocker">
        <AppIcon name="octagon-alert" :size="12" aria-hidden="true" />
        <span><b>{{ t('agentMode.card.blocked') }}:</b> {{ blocker }}</span>
      </p>
      <p v-else-if="attention && d.attention.reason" class="am-card-reason">{{ d.attention.reason }}</p>

      <div class="am-card-estimate">
        <template v-if="showPercent || showEta">
          <div
            v-if="showPercent"
            class="am-card-progress"
            aria-hidden="true"
          >
            <span :style="{ width: `${estimate.presentation.percent}%` }"></span>
          </div>
          <div class="am-card-estimate-row">
            <span v-if="showPercent" class="am-card-percent">{{ t('agentMode.estimate.percent', { n: estimate.presentation.percent }) }}</span>
            <span v-if="showEta && estimate.presentation.rangeOnly && estimate.rangeLabel" class="am-card-eta am-card-eta--range">
              {{ t('agentMode.estimate.range', { range: estimate.rangeLabel }) }}
            </span>
            <span v-else-if="showEta && estimate.landingLabel" class="am-card-eta">
              {{ t('agentMode.estimate.lands', { time: estimate.landingLabel }) }}
              <small v-if="estimate.remainingLabel">· {{ estimate.remainingLabel }}</small>
            </span>
            <span v-else class="am-card-eta am-card-eta--withheld">{{ t(`agentMode.estimate.withheld.${estimate.presentation.etaReason}`) }}</span>
          </div>
        </template>
        <span v-else class="am-card-eta am-card-eta--withheld">
          {{ t(`agentMode.estimate.withheld.${withheldReason ?? 'none'}`) }}
        </span>
      </div>

      <p v-if="d.statusText" class="am-card-status">{{ d.statusText }}</p>
    </button>

    <div v-if="pinnedReason" class="am-card-pinned-note" aria-hidden="true">
      <AppIcon name="pin" :size="11" aria-hidden="true" />
      {{ pinnedReasonLabel }}
    </div>

    <button
      v-if="selected && activatable"
      type="button"
      class="am-card-drill"
      :tabindex="-1"
      :aria-label="t('agentMode.card.detailsAria', { key: d.issueKey })"
      @click.stop="emit('activate', d.id)"
    >
      {{ t('agentMode.card.details') }}
      <AppIcon name="arrow-up-right" :size="12" aria-hidden="true" />
    </button>
  </article>
</template>

<style scoped>
.am-sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

.am-card {
  position: relative;
  min-width: 0;
  border: 1px solid var(--am-line);
  border-radius: 14px;
  background: var(--am-surface);
  color: var(--am-ink);
  transition: border-color 0.18s cubic-bezier(0.2, 0.7, 0.1, 1), box-shadow 0.18s cubic-bezier(0.2, 0.7, 0.1, 1);
}
.am-card:hover { border-color: var(--am-line-strong); box-shadow: 0 6px 18px color-mix(in srgb, var(--am-ink) 8%, transparent); }

/* attention: amber rail on the left edge — an offer, not a selection */
.am-card.is-attention::before {
  content: '';
  position: absolute;
  top: 14px;
  bottom: 14px;
  left: -1px;
  width: 3px;
  border-radius: 0 3px 3px 0;
  background: var(--am-amber);
}

/* selection: accent border + ring + label — the strongest persistent
   anchor, independent from hover & focus; attention stays visible on top */
.am-card.is-selected {
  border-color: var(--am-select);
  box-shadow: 0 0 0 1px var(--am-select), 0 0 0 5px color-mix(in srgb, var(--am-select) 14%, transparent);
  background: color-mix(in srgb, var(--am-select) 4%, var(--am-surface));
}
.am-card.is-selected:hover { border-color: var(--am-select); }
.am-card.is-selected.is-attention::before { left: 0; border-radius: 3px; }

.am-card.is-stale,
.am-card.is-degraded { border-style: dashed; }
.am-card.is-stale .am-card-title,
.am-card.is-stale .am-card-now-text,
.am-card.is-degraded .am-card-title,
.am-card.is-degraded .am-card-now-text { color: var(--am-muted); }

.am-card-flags {
  position: absolute;
  top: -9px;
  left: 12px;
  z-index: 1;
  display: inline-flex;
  gap: 6px;
}
.am-card-flag {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 2px 8px;
  border-radius: 999px;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  background: var(--am-surface);
  box-shadow: 0 0 0 3px var(--am-shell);
}
.am-card-flag--selected { color: var(--am-select); border: 1px solid var(--am-select); }
.am-card-flag--selected > i { width: 6px; height: 6px; border-radius: 50%; background: var(--am-select); }
.am-card-flag--attention { color: var(--am-amber); border: 1px solid color-mix(in srgb, var(--am-amber) 55%, transparent); }

.am-card-hit {
  display: block;
  width: 100%;
  padding: 16px 14px 12px;
  border: 0;
  border-radius: inherit;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.am-card--lg .am-card-hit { padding: 22px 20px 16px; }
/* keyboard focus: ink ring, offset outside the selection ring so both read */
.am-card-hit:focus { outline: none; }
.am-card-hit:focus-visible {
  outline: 2px solid var(--am-focus);
  outline-offset: 4px;
  border-radius: 12px;
}
.am-card.is-selected .am-card-hit:focus-visible { outline-offset: 7px; }

.am-card-head { display: flex; align-items: flex-start; gap: 10px; min-width: 0; }
.am-card-actor {
  display: grid;
  flex: 0 0 auto;
  width: 30px;
  height: 30px;
  place-items: center;
  border-radius: 9px;
  background: color-mix(in srgb, var(--am-ink) 7%, var(--am-surface));
  color: var(--am-ink);
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 11px;
  font-weight: 600;
}
.am-card--lg .am-card-actor { width: 38px; height: 38px; font-size: 13px; }
.am-card-id { min-width: 0; flex: 1; }
.am-card-key {
  display: block;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 11px;
  color: var(--am-muted);
}
.am-card-title {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  font-weight: 600;
  letter-spacing: -0.01em;
}
.am-card--lg .am-card-title {
  white-space: normal;
  font-family: 'Bricolage Grotesque', 'DM Sans', sans-serif;
  font-size: 22px;
  font-weight: 500;
  letter-spacing: -0.02em;
}
.am-card-health {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  font-weight: 600;
  white-space: nowrap;
}
.am-card-health--healthy { color: var(--am-green); }
.am-card-health--attention { color: var(--am-amber); }
.am-card-health--at_risk { color: var(--am-amber); }
.am-card-health--blocked { color: var(--am-red); }
.am-card-health--unknown { color: var(--am-muted); }

.am-card-now {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--am-line);
  font-size: 12px;
}
.am-card-now-kind { flex: 0 0 auto; color: var(--am-muted); }
.am-card-now-text { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.am-card--lg .am-card-now-text { white-space: normal; font-size: 14px; }
.am-card-now-text.is-empty { color: var(--am-muted); font-style: italic; }

.am-card-facts {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
  margin: 10px 0 0;
}
.am-card-facts > div { min-width: 0; }
.am-card-facts dt {
  font-size: 10px;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--am-muted);
}
.am-card-facts dd {
  margin: 1px 0 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11.5px;
}
.am-card-fresh--stale dd,
.am-card-fresh--unknown dd { color: var(--am-amber); }
.am-card-fresh-state { font-weight: 600; }

.am-card-retained {
  display: flex;
  align-items: center;
  gap: 5px;
  margin: 8px 0 0;
  font-size: 11px;
  color: var(--am-amber);
}

/* supplemental tags: muted mono text, capped, never a lane */
.am-card-tags { margin: 8px 0 0; font-size: 11px; line-height: 1.4; }
.am-card-tags-visible { display: inline-flex; flex-wrap: wrap; gap: 2px 8px; }
.am-card-tag { font-family: 'JetBrains Mono', ui-monospace, monospace; color: var(--am-muted); }
.am-card-tag--more { font-weight: 600; }

.am-card-blocker,
.am-card-reason,
.am-card-status {
  margin: 10px 0 0;
  font-size: 11.5px;
  line-height: 1.4;
}
.am-card-blocker { display: flex; gap: 6px; align-items: flex-start; color: var(--am-red); }
.am-card-blocker b { font-weight: 600; }
.am-card-reason { color: var(--am-amber); }
.am-card-status { color: var(--am-muted); }

.am-card-estimate { margin-top: 12px; }
.am-card-progress {
  height: 3px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--am-line);
}
.am-card-progress > span { display: block; height: 100%; border-radius: inherit; background: var(--am-green); }
.am-card-estimate-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  margin-top: 6px;
  font-size: 11.5px;
}
.am-card-percent { font-family: 'JetBrains Mono', ui-monospace, monospace; font-weight: 600; font-variant-numeric: tabular-nums; }
.am-card-eta { font-family: 'JetBrains Mono', ui-monospace, monospace; font-variant-numeric: tabular-nums; }
.am-card-eta--range { color: var(--am-ink); }
.am-card-eta small { color: var(--am-muted); font-size: 11px; }
.am-card-eta--withheld { font-family: 'DM Sans', system-ui, sans-serif; color: var(--am-muted); font-style: italic; }

.am-card-pinned-note {
  display: flex;
  align-items: center;
  gap: 5px;
  margin: 0 14px 12px;
  padding: 6px 9px;
  border-radius: 8px;
  font-size: 11px;
  background: color-mix(in srgb, var(--am-select) 9%, var(--am-surface));
  color: var(--am-select);
}

.am-card-drill {
  position: absolute;
  right: 12px;
  bottom: -11px;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 9px;
  border: 1px solid var(--am-select);
  border-radius: 999px;
  background: var(--am-surface);
  color: var(--am-select);
  font-size: 11px;
  font-weight: 600;
  box-shadow: 0 0 0 3px var(--am-shell);
  cursor: pointer;
}
.am-card-drill:focus-visible { outline: 2px solid var(--am-focus); outline-offset: 2px; }

@media (prefers-reduced-motion: reduce) {
  .am-card { transition: none; }
}
</style>
