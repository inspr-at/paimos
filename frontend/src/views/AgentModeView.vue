<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — Agent Mode route (detail 10, plus the focused-delivery and
  portfolio-overview levels rendered from the same data).

  Renders inside AgentModeLayout (route meta `shell: 'agent'`): the left
  conversation column here is an Agent Mode surface, not navigation.

  Invariants implemented here (PAI-801):
    - exactly one LIVE, AUTHORIZED delivery is selected whenever any
      exists; restored deterministically; never stolen by attention
    - lanes = project → epic (+ explicit Ungrouped); tags supplemental
    - filters never erase selection: an excluded selection is pinned
    - no card target moves while the pointer/focus is on the canvas or
      within 500 ms of an interaction (interaction hold) — EXCEPT that
      security outranks layout stability: data the user may no longer
      see is never re-rendered (403/404 clear the snapshot; deliveries
      that left a successful snapshot become neutral tombstones, and
      lanes with no live delivery vanish at once)
    - percent / ETA only when the trust policy allows AND the data is
      current (retained offline data is qualified, exact estimates withheld)
    - honest loading / empty / offline / forbidden / not-found / error
    - delivery keyboard navigation is scoped to the canvas / cards and
      never hijacks interactive controls
-->
<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import AppIcon from '@/components/AppIcon.vue'
import AgentModeAttentionStrip from '@/components/agent-mode/AgentModeAttentionStrip.vue'
import AgentModeConversation, { type NarrationLine } from '@/components/agent-mode/AgentModeConversation.vue'
import AgentModeDeliveryCard from '@/components/agent-mode/AgentModeDeliveryCard.vue'
import AgentModeDetailLever, { type DetailLevel } from '@/components/agent-mode/AgentModeDetailLever.vue'
import AgentModeFilterBar from '@/components/agent-mode/AgentModeFilterBar.vue'
import AgentModeLanes from '@/components/agent-mode/AgentModeLanes.vue'
import AgentModeSelectionAnchor from '@/components/agent-mode/AgentModeSelectionAnchor.vue'
import AgentModeSelectedFocus from '@/components/agent-mode/AgentModeSelectedFocus.vue'
import AgentModeStateNotice from '@/components/agent-mode/AgentModeStateNotice.vue'
import { COMPACT_CONVERSATION_QUERY, estimateView } from '@/components/agent-mode/agentModePresentation'
import {
  applyFilters,
  exclusionReason,
  type AgentModeFilters,
  type HealthFilter,
} from '@/composables/agent-mode/agentModeFilters'
import {
  TOMBSTONE_TTL_MS,
  buildProjectGroups,
  flattenOrder,
  pruneDeadLanes,
  pruneIds,
  reconcileFrozenGroups,
  type AgentModeProjectGroup,
} from '@/composables/agent-mode/agentModeOrdering'
import { AGENT_MODE_LOADER_KEY, useAgentModeDeliveries } from '@/composables/agent-mode/useAgentModeDeliveries'
import { useAgentModeSelection } from '@/composables/agent-mode/useAgentModeSelection'
import { useInteractionHold } from '@/composables/agent-mode/useInteractionHold'
import { formatRelativeTimeWithLocale, formatTimeWithLocale, useDateFormat } from '@/composables/useDateFormat'
import { lsAgentModeSelectedKey } from '@/constants/storage'
import type { AgentModeSnapshotLoader, Delivery } from '@/services/agentMode'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{
  /** Override the snapshot loader (tests / DEV reference). Production
   * resolves to the real API when neither prop nor injection is set. */
  loader?: AgentModeSnapshotLoader
  /** Shown in the live chip when the data is known to be fixture-backed. */
  sourceLabel?: string
}>()

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const { t } = useI18n()
const { locale } = useDateFormat()

const injectedLoader = inject(AGENT_MODE_LOADER_KEY, null)
const data = useAgentModeDeliveries({ loader: props.loader ?? injectedLoader ?? undefined })

// ── Clock: server-aligned "now" (PAI-803 clock-skew rule) ───────────────
const nowMs = ref(Date.now())
const serverNowMs = computed(() => nowMs.value + data.serverOffsetMs.value)
let clockTimer: ReturnType<typeof setInterval> | null = null
let retryTimer: ReturnType<typeof setInterval> | null = null
const retryInSeconds = ref<number | null>(null)
function tickRetry() {
  const at = data.retryAt.value
  retryInSeconds.value = at == null ? null : Math.max(0, Math.ceil((at - Date.now()) / 1000))
}
watch(data.retryAt, (at) => {
  if (retryTimer !== null) clearInterval(retryTimer)
  retryTimer = null
  tickRetry()
  if (at != null) retryTimer = setInterval(tickRetry, 1000)
})

// ── Constrained widths: conversation collapses to a compact dock ────────
const compactConversation = ref(false)
let compactMq: MediaQueryList | null = null
function syncCompact() {
  compactConversation.value = !!compactMq?.matches
}

onMounted(() => {
  clockTimer = setInterval(() => {
    nowMs.value = Date.now()
  }, 15_000)
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    compactMq = window.matchMedia(COMPACT_CONVERSATION_QUERY)
    syncCompact()
    compactMq.addEventListener?.('change', syncCompact)
  }
})
onBeforeUnmount(() => {
  if (clockTimer !== null) clearInterval(clockTimer)
  if (retryTimer !== null) clearInterval(retryTimer)
  if (tombstoneTimer !== null) clearTimeout(tombstoneTimer)
  compactMq?.removeEventListener?.('change', syncCompact)
  compactMq = null
})

// ── Detail level + filters live in the URL (shareable, restorable) ──────
function parseDetail(raw: unknown): DetailLevel {
  const n = Number(Array.isArray(raw) ? raw[0] : raw)
  return n === 1 || n === 100 ? n : 10
}
const detailLevel = computed<DetailLevel>(() => parseDetail(route.query.detail))

const HEALTH_FILTERS: HealthFilter[] = ['all', 'attention', 'blocked', 'stale']
function parseFilters(): AgentModeFilters {
  const projectRaw = Number(route.query.project)
  const healthRaw = String(route.query.health ?? 'all') as HealthFilter
  return {
    projectId: Number.isFinite(projectRaw) && projectRaw > 0 ? projectRaw : null,
    health: HEALTH_FILTERS.includes(healthRaw) ? healthRaw : 'all',
    query: typeof route.query.q === 'string' ? route.query.q : '',
  }
}
const filters = computed<AgentModeFilters>(parseFilters)

// Query patches are coalesced per tick and serialized: two patches issued
// in the same turn (e.g. selection + detail) become ONE navigation, and
// a later patch never clobbers an earlier in-flight one.
let queuedPatch: Record<string, string | undefined> | null = null
let navChain: Promise<unknown> = Promise.resolve()
function replaceQuery(patch: Record<string, string | undefined>): Promise<void> {
  queuedPatch = { ...(queuedPatch ?? {}), ...patch }
  navChain = navChain.then(async () => {
    const pending = queuedPatch
    queuedPatch = null
    if (!pending) return
    const next: Record<string, string> = {}
    for (const [k, v] of Object.entries(route.query)) {
      if (typeof v === 'string') next[k] = v
    }
    for (const [k, v] of Object.entries(pending)) {
      if (v === undefined || v === '') delete next[k]
      else next[k] = v
    }
    try {
      await router.replace({ query: next })
    } catch {
      /* navigation failures (e.g. during unmount) are not user-facing */
    }
  })
  return navChain as Promise<void>
}

function setDetail(level: DetailLevel) {
  void replaceQuery({ detail: level === 10 ? undefined : String(level) })
}

async function setFilters(next: AgentModeFilters) {
  await replaceQuery({
    project: next.projectId == null ? undefined : String(next.projectId),
    health: next.health === 'all' ? undefined : next.health,
    q: next.query.trim() === '' ? undefined : next.query,
  })
  // A filter change is the user's own action: apply the new layout now,
  // even inside the interaction hold.
  layoutGroups.value = canonicalGroups.value
}

// ── Layout: lanes from filtered deliveries, frozen while interacting ────
const hold = useInteractionHold()
const filtered = computed(() => applyFilters(data.deliveries.value, filters.value))
const canonicalGroups = computed(() => buildProjectGroups(filtered.value))
const layoutGroups = shallowRef<AgentModeProjectGroup[]>([])
const canonicalLayoutIds = computed(() => new Set(flattenOrder(canonicalGroups.value)))

function isLive(id: string): boolean {
  return data.deliveriesById.value.has(id)
}

function isLiveInLayout(id: string): boolean {
  return canonicalLayoutIds.value.has(id)
}

watch(
  [canonicalGroups, hold.held],
  ([next, held]) => {
    // Under hold: keep every still-live card in its slot, append newcomers,
    // and drop at once any lane / project whose deliveries ALL left the
    // authorized snapshot (their headers are grouping metadata). Ids that
    // left but share a lane with live cards stay as neutral tombstones.
    layoutGroups.value = held ? pruneDeadLanes(reconcileFrozenGroups(layoutGroups.value, next), isLiveInLayout) : next
  },
  { immediate: true },
)

/** Ids kept in the frozen layout that are no longer in the snapshot. */
const tombstoneIds = computed(() => {
  const s = new Set<string>()
  for (const id of flattenOrder(layoutGroups.value)) if (!isLiveInLayout(id)) s.add(id)
  return s
})

// Tombstones are short-lived: after TOMBSTONE_TTL_MS they collapse even
// while the hold is still active.
const tombstoneSince = new Map<string, number>()
let tombstoneTimer: ReturnType<typeof setTimeout> | null = null
function scheduleTombstonePrune() {
  if (tombstoneTimer !== null) clearTimeout(tombstoneTimer)
  tombstoneTimer = null
  if (tombstoneSince.size === 0) return
  const oldest = Math.min(...tombstoneSince.values())
  const wait = Math.max(0, TOMBSTONE_TTL_MS - (Date.now() - oldest))
  tombstoneTimer = setTimeout(pruneExpiredTombstones, wait)
}
function pruneExpiredTombstones() {
  tombstoneTimer = null
  const now = Date.now()
  const expired = new Set<string>()
  for (const [id, at] of tombstoneSince) if (now - at >= TOMBSTONE_TTL_MS) expired.add(id)
  if (expired.size > 0) {
    layoutGroups.value = pruneIds(layoutGroups.value, expired)
    for (const id of expired) tombstoneSince.delete(id)
  }
  scheduleTombstonePrune()
}
watch(tombstoneIds, (ids) => {
  for (const id of [...tombstoneSince.keys()]) if (!ids.has(id)) tombstoneSince.delete(id)
  const now = Date.now()
  for (const id of ids) if (!tombstoneSince.has(id)) tombstoneSince.set(id, now)
  scheduleTombstonePrune()
})

// ── Selection ───────────────────────────────────────────────────────────
const storageKey = computed(() => lsAgentModeSelectedKey(auth.user?.id))
const preferredId = ref<string | null>(typeof route.query.delivery === 'string' ? route.query.delivery : null)
/** The pinned (filtered-out) delivery the user travelled from. It stays at
 * the head of the travel order while the filter still excludes it, so arrow
 * travel is reversible: into the results and back to the pinned card. */
const pinnedAnchorId = ref<string | null>(null)
/** Visual travel order restricted to LIVE ids; a pinned (filtered-out)
 * selection — or the pinned origin — travels first. */
const travelOrder = computed<string[]>(() => {
  const ids = flattenOrder(layoutGroups.value).filter(isLive)
  const sel = selection.selectedId.value
  if (sel && !ids.includes(sel) && isLive(sel)) return [sel, ...ids]
  const anchor = pinnedAnchorId.value
  if (anchor && anchor !== sel && !ids.includes(anchor) && isLive(anchor)) return [anchor, ...ids]
  return ids
})
const selection = useAgentModeSelection({
  deliveries: data.deliveries,
  order: travelOrder,
  storageKey,
  preferredId,
})
const selectedDelivery = selection.selectedDelivery
const selectedExcludedBy = computed(() => {
  const d = selectedDelivery.value
  return d ? exclusionReason(d, filters.value) : null
})
const canvasRef = ref<HTMLElement | null>(null)

watch(selection.selectedId, (id) => {
  void replaceQuery({ delivery: id ?? undefined })
})
watch([selection.selectedId, selectedExcludedBy], ([id, excluded]) => {
  if (id && excluded) pinnedAnchorId.value = id
})
// Reset the anchor only when the filter VALUES change (the selection also
// writes to the URL, which must not disturb the travel head).
const filtersKey = computed(() => `${filters.value.projectId}|${filters.value.health}|${filters.value.query}`)
watch(filtersKey, () => {
  pinnedAnchorId.value = selectedExcludedBy.value ? selection.selectedId.value : null
})

function cssEscape(value: string): string {
  if (typeof CSS !== 'undefined' && typeof CSS.escape === 'function') return CSS.escape(value)
  return value.replace(/["\\]/g, '\\$&')
}

/** Moves DOM focus onto the selected card wherever it renders (lanes,
 * pinned section, focused level) so keyboard travel and focus agree. */
async function focusSelectedCard() {
  await nextTick()
  const id = selection.selectedId.value
  if (!id || !canvasRef.value) return
  canvasRef.value.querySelector<HTMLElement>(`[data-card-hit="${cssEscape(id)}"]`)?.focus()
}

function selectDelivery(id: string) {
  hold.markInteraction()
  selection.select(id)
}

function drill(id: string) {
  hold.markInteraction()
  if (selection.selectedId.value !== id) selection.select(id)
  setDetail(1)
}

function zoomOut() {
  if (detailLevel.value === 1) setDetail(10)
  else if (detailLevel.value === 10) setDetail(100)
}

function moveSelection(how: 'next' | 'prev' | 'first' | 'last') {
  hold.markInteraction()
  if (how === 'next') selection.step(1)
  else if (how === 'prev') selection.step(-1)
  else selection.selectEdge(how)
  void focusSelectedCard()
}

/** Interactive descendants keep their own keyboard behaviour. */
const INTERACTIVE_SELECTOR = [
  'button',
  'a[href]',
  'input',
  'select',
  'textarea',
  '[contenteditable=""]',
  '[contenteditable="true"]',
  '[role="radiogroup"]',
  '[role="radio"]',
  '[role="checkbox"]',
  '[role="switch"]',
  '[role="tab"]',
  '[role="menuitem"]',
  '[role="option"]',
  '[role="listbox"]',
  '[role="combobox"]',
  '[role="slider"]',
].join(', ')

function onCanvasKeydown(event: KeyboardEvent) {
  const target = event.target as HTMLElement | null
  const onCard = !!target?.closest?.('.am-card-hit')
  if (!onCard && target?.closest?.(INTERACTIVE_SELECTOR)) return
  switch (event.key) {
    case 'ArrowRight':
    case 'ArrowDown':
      event.preventDefault()
      moveSelection('next')
      break
    case 'ArrowLeft':
    case 'ArrowUp':
      event.preventDefault()
      moveSelection('prev')
      break
    case 'Home':
      event.preventDefault()
      moveSelection('first')
      break
    case 'End':
      event.preventDefault()
      moveSelection('last')
      break
    case 'Escape':
      event.preventDefault()
      zoomOut()
      break
    default:
      break
  }
}

// ── Derived copy: headline, live chip, narration, announcements ─────────
const counts = computed(() => {
  const list = data.deliveries.value
  return {
    total: list.length,
    healthy: list.filter((d) => d.health === 'healthy').length,
    attention: list.filter((d) => d.attention.level > 0).length,
    blocked: list.filter((d) => d.health === 'blocked').length,
    stale: list.filter((d) => d.freshness.state === 'stale' || d.freshness.state === 'unknown').length,
    unknown: list.filter((d) => d.health === 'unknown').length,
  }
})

const headline = computed(() => {
  const n = counts.value.total
  if (n === 0) return t('agentMode.headline.none')
  if (n === 1) return t('agentMode.headline.one')
  return t('agentMode.headline.many', { n })
})
const breakdown = computed(() => {
  const c = counts.value
  const parts: string[] = []
  if (c.healthy) parts.push(t('agentMode.narration.partHealthy', { n: c.healthy }))
  if (c.attention) parts.push(t('agentMode.narration.partAttention', { n: c.attention }, c.attention))
  if (c.blocked) parts.push(t('agentMode.narration.partBlocked', { n: c.blocked }))
  if (c.stale) parts.push(t('agentMode.narration.partStale', { n: c.stale }))
  if (c.unknown) parts.push(t('agentMode.narration.partUnknown', { n: c.unknown }))
  return parts.join(' · ')
})
const dateLine = computed(() => {
  const weekday = new Intl.DateTimeFormat(locale.value, { weekday: 'long' }).format(new Date(serverNowMs.value))
  return `${weekday} · ${formatTimeWithLocale(serverNowMs.value, locale.value)}`
})

const feedLive = computed(() => (data.status.value === 'ready' || data.status.value === 'empty') && !data.refreshing.value)
const liveLabel = computed(() => {
  if (props.sourceLabel) return props.sourceLabel
  if (data.refreshing.value) return t('agentMode.live.refreshing')
  switch (data.status.value) {
    case 'ready':
    case 'empty': {
      const at = data.lastLoadedAt.value
      const when = at == null ? '' : formatRelativeTimeWithLocale(at, locale.value, nowMs.value)
      return when ? `${t('agentMode.live.live')} · ${t('agentMode.live.updated', { when })}` : t('agentMode.live.live')
    }
    case 'offline':
      return retryInSeconds.value == null
        ? t('agentMode.live.offline')
        : `${t('agentMode.live.offline')} · ${t('agentMode.live.retryIn', { s: retryInSeconds.value })}`
    case 'loading':
    case 'idle':
      return t('agentMode.state.loading')
    default:
      return t('agentMode.live.stalled')
  }
})

function selectionSentence(d: Delivery): string {
  const actor = d.actor?.label ?? t('agentMode.card.noActor')
  const activity = d.activity.text ?? t(`agentMode.activity.${d.activity.kind}`)
  const est = estimateView(d, locale.value, serverNowMs.value)
  let text = t('agentMode.narration.selection', { key: d.issueKey, actor, activity })
  if (est.landingLabel && !data.degraded.value) {
    text += ' ' + t('agentMode.narration.selectionEta', { time: est.landingLabel, remaining: est.remainingLabel ?? '—' })
  } else {
    const reason = data.degraded.value
      ? 'offline'
      : est.presentation.etaReason === 'ok'
        ? 'none'
        : est.presentation.etaReason
    text += ' ' + t('agentMode.narration.selectionNoEta', { reason: t(`agentMode.estimate.withheld.${reason}`) })
  }
  return text
}

const narrationLines = computed<NarrationLine[]>(() => {
  const lines: NarrationLine[] = []
  const status = data.status.value
  if (!data.hasData.value && status !== 'ready' && status !== 'empty') {
    // No authorized data: narrate the honest state, never "nothing in motion".
    const text = status === 'loading' || status === 'idle' ? t('agentMode.state.loading') : t(`agentMode.state.${status}.title`)
    lines.push({ id: `state:${status}`, role: 'system', text })
    return lines
  }
  const n = counts.value.total
  const summary = t('agentMode.narration.summary', { n }, n)
  lines.push({ id: 'summary', role: 'system', text: breakdown.value ? `${summary} ${breakdown.value}.` : summary })
  if (counts.value.attention > 0) {
    lines.push({
      id: 'attention',
      role: 'system',
      text: t('agentMode.narration.attentionOffer', { n: counts.value.attention }, counts.value.attention),
    })
  }
  const change = selection.lastChange.value
  const d = selectedDelivery.value
  if (d) {
    if (change?.origin === 'restored') {
      lines.push({ id: 'restored', role: 'system', text: t('agentMode.narration.restored', { key: d.issueKey }) })
    } else if (change?.origin === 'default') {
      if (change.previous && !isLive(change.previous)) {
        // Never repeat the identity of a delivery that left the authorized set.
        lines.push({ id: 'moved', role: 'system', text: t('agentMode.narration.moved', { key: d.issueKey }) })
      } else {
        const why = d.attention.level > 0
          ? t('agentMode.narration.whyAttention')
          : d.eta?.trusted && d.eta.landingAt
            ? t('agentMode.narration.whyLanding')
            : t('agentMode.narration.whyOrder')
        lines.push({ id: 'defaulted', role: 'system', text: t('agentMode.narration.defaulted', { key: d.issueKey, why }) })
      }
    }
    lines.push({ id: `selection:${d.id}`, role: 'system', text: selectionSentence(d) })
  }
  return lines
})

const announcement = ref('')
watch(selection.lastChange, (change) => {
  if (!change?.id) return
  const d = data.deliveriesById.value.get(change.id)
  if (!d) return
  announcement.value = t('agentMode.a11y.selectionChanged', { key: d.issueKey, title: d.title })
})

// ── Portfolio overview: per-lane counts (same vocabulary as the cards) ──
const streamRows = computed(() =>
  buildProjectGroups(data.deliveries.value).flatMap((g) =>
    g.lanes.map((lane) => {
      const items = lane.deliveryIds.map((id) => data.deliveriesById.value.get(id)).filter((d): d is Delivery => !!d)
      return {
        key: lane.key,
        label: `${g.projectKey} / ${lane.ungrouped ? t('agentMode.lanes.ungrouped') : [lane.epicKey, lane.epicTitle].filter(Boolean).join(' · ')}`,
        active: items.length,
        attention: items.filter((d) => d.attention.level > 0).length,
        blocked: items.filter((d) => d.health === 'blocked').length,
        stale: items.filter((d) => d.freshness.state === 'stale' || d.freshness.state === 'unknown').length,
      }
    }),
  ),
)

const showNotice = computed(() => {
  const s = data.status.value
  if (s === 'empty') return true
  if (s === 'ready') return false
  return !data.hasData.value
})
const showOfflineBanner = computed(() => data.degraded.value)

const selectedPosition = computed(() => {
  const idx = selection.selectedIndex.value
  return idx >= 0 ? idx + 1 : 0
})
</script>

<template>
  <div class="am-root" :class="{ 'am-root--compact': compactConversation }">
    <Teleport defer to="#app-header-left">
      <span class="ah-title">{{ t('agentMode.title') }}</span>
      <span v-if="data.hasData.value" class="ah-subtitle">{{ t('agentMode.subtitle', { n: counts.total }, counts.total) }}</span>
    </Teleport>
    <Teleport defer to="#app-header-right">
      <div class="am-header-tools">
        <span class="am-live-chip" :class="{ 'is-live': feedLive, 'is-off': data.status.value === 'offline' }" role="status" aria-live="polite">
          <AppIcon v-if="data.status.value === 'offline'" name="wifi-off" :size="12" aria-hidden="true" />
          <i v-else aria-hidden="true"></i>
          {{ liveLabel }}
        </span>
        <AgentModeDetailLever :level="detailLevel" @update:level="setDetail" />
      </div>
    </Teleport>

    <span class="am-sr-only" role="status" aria-live="polite">{{ announcement }}</span>

    <AgentModeConversation :lines="narrationLines" :live="feedLive" :live-label="liveLabel" :compact="compactConversation" />

    <main
      ref="canvasRef"
      class="am-canvas"
      :aria-label="t('agentMode.a11y.canvas')"
      :data-held="hold.held.value ? 'true' : 'false'"
      @pointerenter="hold.onPointerEnter"
      @pointerleave="hold.onPointerLeave"
      @focusin="hold.onFocusIn"
      @focusout="hold.onFocusOut"
      @keydown="onCanvasKeydown"
    >
      <AgentModeStateNotice
        v-if="showNotice"
        :status="data.status.value"
        :message="data.error.value?.message ?? null"
        :retry-in-seconds="retryInSeconds"
        :attempt="data.attempt.value"
        @retry="data.retryNow"
      />

      <template v-else>
        <header class="am-canvas-head">
          <div>
            <span class="am-eyebrow">{{ dateLine }}</span>
            <h1 class="am-headline">{{ headline }}</h1>
            <p v-if="breakdown" class="am-subline">{{ breakdown }}</p>
          </div>
        </header>

        <div v-if="showOfflineBanner" class="am-banner" role="status">
          <AppIcon :name="data.status.value === 'offline' ? 'wifi-off' : 'alert-circle'" :size="13" aria-hidden="true" />
          <span>
            {{ t(`agentMode.state.${data.status.value}.title`) }}
            · {{ t('agentMode.card.retained') }}
            <template v-if="data.status.value === 'offline' && retryInSeconds != null"> · {{ t('agentMode.state.offline.retryIn', { s: retryInSeconds }) }}</template>
          </span>
          <button type="button" class="am-banner-retry" @click="data.retryNow">{{ t('agentMode.state.retry') }}</button>
        </div>

        <!-- Focused delivery -->
        <AgentModeSelectedFocus
          v-if="detailLevel === 1 && selectedDelivery"
          :delivery="selectedDelivery"
          :position="selectedPosition"
          :total="travelOrder.length"
          :server-now-ms="serverNowMs"
          :locale="locale"
          :degraded="data.degraded.value"
          @prev="moveSelection('prev')"
          @next="moveSelection('next')"
          @zoom-out="setDetail(10)"
          @interact="hold.markInteraction"
        />

        <!-- Portfolio overview: pinned selection + lane counts -->
        <section v-else-if="detailLevel === 100" class="am-streams" :aria-label="t('agentMode.streams.title')">
          <h2 class="am-streams-title">{{ t('agentMode.streams.title') }}</h2>
          <AgentModeDeliveryCard
            v-if="selectedDelivery"
            :delivery="selectedDelivery"
            :selected="true"
            :tabbable="true"
            :degraded="data.degraded.value"
            :server-now-ms="serverNowMs"
            :locale="locale"
            @activate="drill"
            @interact="hold.markInteraction"
          />
          <h3 class="am-streams-subtitle">{{ t('agentMode.streams.lanesTitle') }}</h3>
          <table class="am-streams-table">
            <thead>
              <tr>
                <th scope="col">{{ t('agentMode.streams.lane') }}</th>
                <th scope="col">{{ t('agentMode.streams.active') }}</th>
                <th scope="col">{{ t('agentMode.streams.attention') }}</th>
                <th scope="col">{{ t('agentMode.streams.blocked') }}</th>
                <th scope="col">{{ t('agentMode.streams.stale') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in streamRows" :key="row.key">
                <th scope="row">{{ row.label }}</th>
                <td>{{ row.active }}</td>
                <td :class="{ 'is-warn': row.attention }">{{ row.attention }}</td>
                <td :class="{ 'is-risk': row.blocked }">{{ row.blocked }}</td>
                <td :class="{ 'is-warn': row.stale }">{{ row.stale }}</td>
              </tr>
            </tbody>
          </table>
        </section>

        <!-- Detail 10 -->
        <template v-else>
          <AgentModeSelectionAnchor
            v-if="selectedDelivery"
            :delivery="selectedDelivery"
            :server-now-ms="serverNowMs"
            :locale="locale"
            :excluded-by="selectedExcludedBy"
            :degraded="data.degraded.value"
            @activate="drill"
            @interact="hold.markInteraction"
          />

          <AgentModeAttentionStrip
            :deliveries="data.deliveries.value"
            :selected-id="selection.selectedId.value"
            :server-now-ms="serverNowMs"
            :locale="locale"
            @select="selectDelivery"
          />

          <AgentModeFilterBar :filters="filters" :deliveries="data.deliveries.value" @update:filters="setFilters" />

          <p v-if="layoutGroups.length === 0" class="am-nomatch">{{ t('agentMode.filters.noMatch') }}</p>

          <AgentModeLanes
            :groups="layoutGroups"
            :deliveries-by-id="data.deliveriesById.value"
            :tombstone-ids="tombstoneIds"
            :selected-id="selection.selectedId.value"
            :lifted-selected-id="selection.selectedId.value"
            :server-now-ms="serverNowMs"
            :locale="locale"
            :degraded="data.degraded.value"
            @select="selectDelivery"
            @activate="drill"
            @interact="hold.markInteraction"
          />
        </template>
      </template>
    </main>
  </div>
</template>

<style scoped>
.am-sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

/* ── Agent Mode tokens ──────────────────────────────────────────────────
   Base surfaces / ink / lines derive from the HOST theme tokens so Agent
   Mode follows whatever theme PAIMOS exposes. The semantic accents are
   fixed values tuned for the host's light surfaces (#ffffff / #f2f5f8):
   every one reads ≥ 4.5:1 as text and ≥ 3:1 as a non-text indicator
   there (see agentModeTheme.test.ts). There is deliberately NO
   prefers-color-scheme override here: the OS theme must not recolor a
   light-themed app into ~2:1 pastels. A future host dark theme overrides
   these tokens at the host level, not per component. */
.am-root {
  --am-ink: var(--text);
  --am-muted: var(--text-muted);
  --am-line: var(--border);
  --am-line-strong: color-mix(in srgb, var(--text) 28%, var(--border));
  --am-surface: var(--bg-card);
  --am-shell: var(--bg);
  --am-green: #1f7a4d;
  --am-amber: #955a0e;
  --am-red: #b4342c;
  --am-blue: #3f6a95;
  --am-purple: #6b4fa0;
  --am-select: #2f63d6;
  --am-focus: var(--text);

  position: relative;
  flex: 1;
  min-height: 0;
  min-width: 0;
  display: grid;
  grid-template-columns: 248px minmax(0, 1fr);
  grid-template-rows: minmax(0, 1fr);
  background: var(--am-shell);
  color: var(--am-ink);
}
.am-root--compact {
  grid-template-columns: minmax(0, 1fr);
  grid-template-rows: minmax(0, 1fr) auto;
}
.am-root--compact :deep(.am-conv) { grid-column: 1; grid-row: 2; }
.am-root--compact .am-canvas { grid-column: 1; grid-row: 1; }

.am-header-tools { display: inline-flex; align-items: center; gap: 12px; }
.am-live-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--text-muted);
  white-space: nowrap;
}
.am-live-chip > i { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
/* The chip is teleported into the shell header (outside .am-root), so it
   carries its own light-surface accents rather than the --am-* tokens. */
.am-live-chip.is-live { color: #1f7a4d; }
.am-live-chip.is-live > i { box-shadow: 0 0 0 4px rgba(31, 122, 77, 0.14); }
.am-live-chip.is-off { color: #b4342c; }

.am-canvas {
  min-width: 0;
  min-height: 0;
  overflow: auto;
  padding: 26px 28px 40px;
  background:
    radial-gradient(circle at 78% 0%, color-mix(in srgb, var(--am-green) 7%, transparent), transparent 34%),
    var(--am-shell);
}
.am-root--compact .am-canvas { padding: 22px 18px 32px; }
.am-canvas > * + * { margin-top: 18px; }

.am-eyebrow {
  color: var(--am-muted);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
.am-headline {
  margin: 6px 0 4px;
  font-family: 'Bricolage Grotesque', 'DM Sans', sans-serif;
  font-size: clamp(22px, 2.6vw, 30px);
  font-weight: 500;
  letter-spacing: -0.03em;
}
.am-subline { margin: 0; color: var(--am-muted); font-size: 13px; }

.am-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border: 1px solid color-mix(in srgb, var(--am-red) 40%, var(--am-line));
  border-radius: 10px;
  background: color-mix(in srgb, var(--am-red) 7%, var(--am-surface));
  color: var(--am-red);
  font-size: 12px;
}
.am-banner-retry {
  margin-left: auto;
  padding: 3px 9px;
  border: 1px solid currentColor;
  border-radius: 7px;
  background: transparent;
  color: inherit;
  font-size: 11px;
  font-weight: 600;
}

.am-nomatch { margin: 0; color: var(--am-muted); font-size: 13px; }

.am-streams { display: grid; gap: 14px; max-width: 860px; }
.am-streams-title { margin: 0; font-family: 'Bricolage Grotesque', 'DM Sans', sans-serif; font-size: 17px; font-weight: 600; }
.am-streams-subtitle { margin: 6px 0 0; font-size: 13px; font-weight: 600; color: var(--am-muted); }
.am-streams-table { width: 100%; border-collapse: collapse; font-size: 12px; }
.am-streams-table th,
.am-streams-table td { padding: 7px 10px; border-bottom: 1px solid var(--am-line); text-align: left; }
.am-streams-table thead th { font-size: 10.5px; letter-spacing: 0.06em; text-transform: uppercase; color: var(--am-muted); }
.am-streams-table tbody th { font-weight: 500; }
.am-streams-table td { font-family: 'JetBrains Mono', ui-monospace, monospace; font-variant-numeric: tabular-nums; }
.am-streams-table td.is-warn { color: var(--am-amber); font-weight: 600; }
.am-streams-table td.is-risk { color: var(--am-red); font-weight: 600; }
</style>
