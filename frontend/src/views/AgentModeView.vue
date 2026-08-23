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
import {
  isNavigationFailure,
  onBeforeRouteUpdate,
  useRoute,
  useRouter,
  type LocationQueryRaw,
} from 'vue-router'

import {
  comparePermissionsEpoch,
  permissionsEpoch,
  permissionsEpochGeneration,
  sessionExpired,
} from '@/api/client'
import AppIcon from '@/components/AppIcon.vue'
import IssueSidePanel from '@/components/IssueSidePanel.vue'
import AgentModeAttentionStrip from '@/components/agent-mode/AgentModeAttentionStrip.vue'
import AgentModeConversation, { type NarrationLine } from '@/components/agent-mode/AgentModeConversation.vue'
import AgentModeControlsCard from '@/components/agent-mode/AgentModeControlsCard.vue'
import AgentModeVoiceConsole from '@/components/agent-mode/AgentModeVoiceConsole.vue'
import AgentModeDetailLever, { type DetailLevel } from '@/components/agent-mode/AgentModeDetailLever.vue'
import AgentModeFilterBar, { type AgentModeCardDensity } from '@/components/agent-mode/AgentModeFilterBar.vue'
import AgentModeLanes from '@/components/agent-mode/AgentModeLanes.vue'
import AgentModePortfolioOverview from '@/components/agent-mode/AgentModePortfolioOverview.vue'
import AgentModeSelectionAnchor from '@/components/agent-mode/AgentModeSelectionAnchor.vue'
import AgentModeSelectedFocus from '@/components/agent-mode/AgentModeSelectedFocus.vue'
import AgentModeStateNotice from '@/components/agent-mode/AgentModeStateNotice.vue'
import { COMPACT_CONVERSATION_QUERY, TIGHT_EDITOR_QUERY, estimateView } from '@/components/agent-mode/agentModePresentation'
import {
  EMPTY_FILTERS,
  filtersActive,
  type AgentModeFilters,
  type HealthFilter,
} from '@/composables/agent-mode/agentModeFilters'
import { buildAgentModeCommandHints } from '@/composables/agent-mode/agentModeCommandHints'
import {
  TOMBSTONE_TTL_MS,
  buildProjectGroups,
  flattenOrder,
  pruneDeadLanes,
  pruneIds,
  reconcileFrozenGroups,
  type AgentModeProjectGroup,
} from '@/composables/agent-mode/agentModeOrdering'
import {
  aggregateFocusOrder,
  nearestSurvivingAggregateFocusKey,
  reconcileAggregateOrder,
} from '@/composables/agent-mode/agentModeAggregateOrdering'
import { AGENT_MODE_LOADER_KEY, useAgentModeDeliveries } from '@/composables/agent-mode/useAgentModeDeliveries'
import { useAgentModeSelection } from '@/composables/agent-mode/useAgentModeSelection'
import {
  AGENT_MODE_CONTROLS_DEPENDENCIES_KEY,
  useAgentModeControls,
} from '@/composables/agent-mode/useAgentModeControls'
import {
  AGENT_MODE_VOICE_DEPENDENCIES_KEY,
  useAgentModeVoice,
} from '@/composables/agent-mode/useAgentModeVoice'
import { buildControlVoicePhrase } from '@/composables/agent-mode/agentModeVoiceIntent'
import { useInteractionHold } from '@/composables/agent-mode/useInteractionHold'
import { formatRelativeTimeWithLocale, formatTimeWithLocale, useDateFormat } from '@/composables/useDateFormat'
import { lsAgentModeDensityKey, lsAgentModeSelectedKey } from '@/constants/storage'
import type { AgentModeSnapshotLoader, Delivery } from '@/services/agentMode'
import type { AgentModeAggregates } from '@/services/agentModeAggregateSchema'
import type { AgentModeEventSourceFactory } from '@/services/agentModeEvents'
import {
  AGENT_MODE_DELIVERY_STATES,
  buildSnapshotPath,
  parseAgentModeDeliveryKey,
  parseAgentModeLaneFilter,
  parseAgentModeProjectFilter,
  trimAgentModeSpace,
  type AgentModeDeliveryState,
  type AgentModeSnapshotQuery,
} from '@/services/agentModeTransport'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{
  /** Override the snapshot loader (tests / DEV reference). Production
   * resolves to the real API when neither prop nor injection is set. */
  loader?: AgentModeSnapshotLoader
  /** Deterministic stream seam for component tests. Supplying one explicitly
   * opts a custom loader into the production dedicated-stream lifecycle. */
  eventSourceFactory?: AgentModeEventSourceFactory
  /** Shown in the live chip when the data is known to be fixture-backed. */
  sourceLabel?: string
  /** DEV reference owns `n`/`state` as fixture controls rather than snapshot
   * filters. Production routes never set this escape hatch. */
  devReference?: boolean
}>()

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const { t } = useI18n()
const { locale } = useDateFormat()

const injectedLoader = inject(AGENT_MODE_LOADER_KEY, null)
function initialSelectedQuery(): unknown {
  if (route.query.delivery !== undefined) return route.query.delivery ?? ''
  try {
    const key = lsAgentModeSelectedKey(auth.user?.id)
    const stored = localStorage.getItem(key)
    if (stored == null) return null
    const canonical = parseAgentModeDeliveryKey(stored)
    if (canonical) return canonical
    localStorage.removeItem(key)
    return null
  } catch {
    return null
  }
}
const initialSelectionInput = initialSelectedQuery()
const selectedQueryId = ref<unknown>(initialSelectionInput)
const initialPreferredId = parseAgentModeDeliveryKey(initialSelectionInput)
// A principal generation reset clears the URL asynchronously. Until that
// navigation commits, this synchronous override ensures the replacement
// request cannot capture any former principal's route vocabulary.
const authorityRouteResetPending = ref(false)
// One canonical URL boundary feeds both transport identity and presentation;
// downstream code never reparses or repairs project/lane values differently.
const canonicalRouteBoundary = computed(() => authorityRouteResetPending.value
  ? {
      filters: { ...EMPTY_FILTERS, states: [] },
      query: {
        // Do not issue any replacement until the old principal's known route
        // keys have actually been removed. The NaN sentinel is rejected by
        // the one transport validator without touching the network, and also
        // preserves the established fail-closed behavior for unknown/invalid
        // URL boundaries that the reset intentionally leaves untouched.
        projectId: Number.NaN,
        laneKey: null,
        states: [] as readonly AgentModeDeliveryState[],
        attention: 'all' as const,
        health: 'all' as const,
        q: '',
      },
    }
  : parseFilters())
const canonicalRouteFilters = computed<AgentModeFilters>(() => canonicalRouteBoundary.value.filters)
const snapshotQuery = computed<AgentModeSnapshotQuery>(() => {
  return {
    ...canonicalRouteBoundary.value.query,
    selectedDelivery: selectedQueryId.value as string | null,
  }
})
async function clearRejectedSelectedDelivery(deliveryId: string) {
  if (selectedQueryId.value === deliveryId) selectedQueryId.value = null
  try {
    const key = lsAgentModeSelectedKey(auth.user?.id)
    if (localStorage.getItem(key) === deliveryId) localStorage.removeItem(key)
  } catch {
    /* storage may be unavailable; the in-memory/query identity is still cleared */
  }
  await replaceQuery({ delivery: undefined })
}
const data = useAgentModeDeliveries({
  loader: props.loader ?? injectedLoader ?? undefined,
  query: snapshotQuery,
  reloadOnQueryChange: false,
  onSelectedDeliveryRejected: clearRejectedSelectedDelivery,
  eventSourceFactory: props.eventSourceFactory,
})

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
const viewportCompact = ref(false)
const editorTight = ref(false)
const ticketOpen = ref(false)
let compactMq: MediaQueryList | null = null
let editorMq: MediaQueryList | null = null
const compactConversation = computed(() => viewportCompact.value || (ticketOpen.value && editorTight.value))
function syncCompact() {
  viewportCompact.value = !!compactMq?.matches
  editorTight.value = !!editorMq?.matches
}

onMounted(() => {
  clockTimer = setInterval(() => {
    nowMs.value = Date.now()
  }, 15_000)
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    compactMq = window.matchMedia(COMPACT_CONVERSATION_QUERY)
    editorMq = window.matchMedia(TIGHT_EDITOR_QUERY)
    syncCompact()
    compactMq.addEventListener?.('change', syncCompact)
    editorMq.addEventListener?.('change', syncCompact)
  }
})
onBeforeUnmount(() => {
  if (clockTimer !== null) clearInterval(clockTimer)
  if (retryTimer !== null) clearInterval(retryTimer)
  if (tombstoneTimer !== null) clearTimeout(tombstoneTimer)
  compactMq?.removeEventListener?.('change', syncCompact)
  editorMq?.removeEventListener?.('change', syncCompact)
  compactMq = null
  editorMq = null
})

// ── Detail level + filters live in the URL (shareable, restorable) ──────
function parseDetail(raw: unknown): DetailLevel {
  const n = Number(Array.isArray(raw) ? raw[0] : raw)
  return n === 1 || n === 100 ? n : 10
}
const detailLevel = computed<DetailLevel>(() => parseDetail(route.query.detail))

const HEALTH_FILTERS: HealthFilter[] = ['all', 'attention', 'blocked', 'stale']
const DELIVERY_STATES = new Set<string>(AGENT_MODE_DELIVERY_STATES)
const AGENT_MODE_ROUTE_QUERY_KEYS = new Set([
  'project', 'lane', 'state', 'attention', 'health', 'q', 'delivery', 'detail',
])
const PRINCIPAL_BOUND_AGENT_MODE_QUERY_KEYS = [
  'delivery', 'project', 'lane', 'q', 'state', 'health', 'attention',
] as const
function routeContractViolationToken(): string | null {
  const unknown = Object.keys(route.query).filter((key) => (
    !AGENT_MODE_ROUTE_QUERY_KEYS.has(key) && !(props.devReference && key === 'n')
  )).sort()
  if (unknown.length > 0) return JSON.stringify(unknown.map((key) => [key, route.query[key]]))
  // Vue Router represents the bare `?state` form as an explicit null. It is
  // not equivalent to an absent state parameter at the backend boundary.
  if (!props.devReference && Object.prototype.hasOwnProperty.call(route.query, 'state') && route.query.state === null) {
    return JSON.stringify(['state', null])
  }
  const detail = route.query.detail
  return detail === undefined || (typeof detail === 'string' && ['1', '10', '100'].includes(detail))
    ? null
    : JSON.stringify(['detail', detail])
}
function parseFilters(): { filters: AgentModeFilters; query: AgentModeSnapshotQuery } {
  const contractViolation = routeContractViolationToken() !== null
  const projectRaw = route.query.project
  const projectId = projectRaw == null || projectRaw === '' ? null : parseAgentModeProjectFilter(projectRaw)
  const laneRaw = route.query.lane
  const laneKey = laneRaw == null || laneRaw === '' ? null : parseAgentModeLaneFilter(laneRaw)
  const healthRaw = route.query.health
  const health = healthRaw == null || healthRaw === ''
    ? 'all'
    : typeof healthRaw === 'string' && HEALTH_FILTERS.includes(healthRaw as HealthFilter)
      ? healthRaw as HealthFilter
      : 'all'
  const qRaw = route.query.q
  const queryText = typeof qRaw === 'string' ? qRaw : ''
  const stateRaw = props.devReference ? undefined : route.query.state
  const stateValues = stateRaw === undefined ? [] : Array.isArray(stateRaw) ? stateRaw : [stateRaw]
  const validStates = stateValues.every((state): state is AgentModeDeliveryState => (
    typeof state === 'string' && DELIVERY_STATES.has(state)
  ))
  const states = validStates ? [...new Set(stateValues)].sort() : []
  const attentionRaw = route.query.attention
  const attention = attentionRaw == null || attentionRaw === ''
    ? 'all'
    : attentionRaw === 'required' || attentionRaw === 'all'
      ? attentionRaw
      : 'all'
  return {
    filters: { projectId, laneKey, health, query: queryText, states, attention },
    query: {
      // NaN is a deliberate internal invalid sentinel. It reaches the one
      // transport validator and cannot serialize into a broader request.
      projectId: contractViolation
        ? Number.NaN
        : (projectRaw == null || projectRaw === '' ? null : projectId ?? projectRaw) as number | null,
      laneKey: (laneRaw == null || laneRaw === '' ? null : laneKey ?? laneRaw) as string | null,
      states: (validStates ? states : stateValues) as readonly AgentModeDeliveryState[],
      attention: (attentionRaw == null || attentionRaw === '' ? 'all' : attentionRaw) as 'all' | 'required',
      health: (healthRaw == null || healthRaw === '' ? 'all' : healthRaw) as HealthFilter,
      q: (qRaw == null ? '' : qRaw) as string,
    },
  }
}
const filters = canonicalRouteFilters

function readCardDensity(userId: number | undefined): AgentModeCardDensity {
  try {
    return localStorage.getItem(lsAgentModeDensityKey(userId)) === 'full' ? 'full' : 'calm'
  } catch {
    return 'calm'
  }
}
const cardDensity = ref<AgentModeCardDensity>(readCardDensity(auth.user?.id))
watch(() => auth.user?.id, (userId) => { cardDensity.value = readCardDensity(userId) })
watch(cardDensity, (density) => {
  try { localStorage.setItem(lsAgentModeDensityKey(auth.user?.id), density) } catch { /* preference remains in memory */ }
})

// Filter identity is a privacy boundary and is therefore loaded explicitly.
// Selection has a different lifecycle below: user/deep-link changes refetch,
// while a server-owned default already describes the snapshot just received
// and must not create a duplicate initial request.
const routeFilterIdentity = computed(() => JSON.stringify([
  route.query.project,
  route.query.lane,
  props.devReference ? undefined : route.query.state,
  route.query.attention,
  route.query.health,
  route.query.q,
  routeContractViolationToken(),
]))
watch(routeFilterIdentity, () => {
  void data.load({ background: data.hasData.value })
}, { flush: 'sync' })

// Query patches are coalesced per tick and serialized: two patches issued
// in the same turn (e.g. selection + detail) become ONE navigation, and
// a later patch never clobbers an earlier in-flight one.
type QueryPatch = Record<string, string | string[] | undefined>
let queuedPatch: QueryPatch | null = null
let queuedPatchAuthorityToken: number | null = null
let navChain: Promise<unknown> = Promise.resolve()
let internalRouteWriteDepth = 0
let routeAuthorityToken = 0
function invalidateInternalRouteWrites() {
  routeAuthorityToken += 1
  queuedPatch = null
  queuedPatchAuthorityToken = null
}
async function applyQueryPatch(pending: QueryPatch, stillCurrent?: () => boolean): Promise<boolean> {
  if (stillCurrent && !stillCurrent()) return false
  // Preserve every untouched raw query value, including explicit nulls,
  // mixed arrays, and unknown keys. An unrelated detail/selection write must
  // never silently repair an invalid boundary into a broader request.
  const next: LocationQueryRaw = { ...route.query }
  for (const [k, v] of Object.entries(pending)) {
    if (v === undefined || v === '') delete next[k]
    else next[k] = v
  }
  if (stillCurrent && !stillCurrent()) return false
  const intendedPath = router.resolve({ query: next }).fullPath
  // Re-check guarded writes at Vue Router's final pre-commit boundary. A
  // dirty-editor/global guard may await while ACL/session authority changes;
  // checking only before router.replace would let that stale navigation land.
  // Scope the temporary guard to this exact target so a newer navigation is
  // never cancelled by an older action's predicate.
  const removeCommitGuard = stillCurrent
    ? router.beforeResolve((to) => to.fullPath !== intendedPath || stillCurrent())
    : null
  try {
    internalRouteWriteDepth += 1
    const failure = await router.replace({ query: next })
    if (isNavigationFailure(failure)) return false
    return !stillCurrent || stillCurrent()
  } catch {
    /* navigation failures (e.g. during unmount) are not user-facing */
    return false
  } finally {
    internalRouteWriteDepth -= 1
    removeCommitGuard?.()
  }
}

function replaceQuery(patch: QueryPatch, stillCurrent?: () => boolean): Promise<boolean> {
  const authorityToken = routeAuthorityToken
  const current = () => authorityToken === routeAuthorityToken && (!stillCurrent || stillCurrent())
  // A guarded voice mutation keeps its own predicate until the exact point
  // the serialized router write runs. Coalescing it with an unrelated UI
  // patch could otherwise let a stale guard suppress or authorize both.
  if (stillCurrent) {
    navChain = navChain.then(() => applyQueryPatch(patch, current))
    return navChain as Promise<boolean>
  }
  queuedPatch = { ...(queuedPatch ?? {}), ...patch }
  queuedPatchAuthorityToken = authorityToken
  navChain = navChain.then(async () => {
    const pending = queuedPatch
    const pendingAuthorityToken = queuedPatchAuthorityToken
    queuedPatch = null
    queuedPatchAuthorityToken = null
    if (!pending || pendingAuthorityToken !== routeAuthorityToken) return false
    return applyQueryPatch(pending, () => pendingAuthorityToken === routeAuthorityToken)
  })
  return navChain as Promise<boolean>
}

watch(permissionsEpoch, (epoch, previous) => {
  if (
    epoch != null
    && previous != null
    && comparePermissionsEpoch(epoch, previous) > 0
  ) invalidateInternalRouteWrites()
}, { flush: 'sync' })
watch(sessionExpired, (expired) => {
  if (expired) invalidateInternalRouteWrites()
}, { flush: 'sync' })

async function setDetail(level: DetailLevel, stillCurrent?: () => boolean): Promise<boolean> {
  if (level !== 1 && ticketOpen.value) {
    const allowed = await ticketPanelRef.value?.requestLeave()
    if (allowed === false) return false
    if (stillCurrent && !stillCurrent()) return false
    ticketOpen.value = false
  }
  if (stillCurrent && !stillCurrent()) return false
  return replaceQuery({ detail: level === 10 ? undefined : String(level) }, stillCurrent)
}

async function setFilters(next: Partial<AgentModeFilters>, stillCurrent?: () => boolean): Promise<boolean> {
  const patch: QueryPatch = {}
  if (Object.prototype.hasOwnProperty.call(next, 'projectId')) {
    patch.project = next.projectId == null ? undefined : String(next.projectId)
  }
  if (Object.prototype.hasOwnProperty.call(next, 'laneKey')) patch.lane = next.laneKey ?? undefined
  if (Object.prototype.hasOwnProperty.call(next, 'health')) {
    patch.health = next.health === 'all' ? undefined : next.health
  }
  if (Object.prototype.hasOwnProperty.call(next, 'query')) {
    patch.q = trimAgentModeSpace(next.query ?? '') === '' ? undefined : next.query
  }
  if (Object.prototype.hasOwnProperty.call(next, 'states')) {
    patch.state = next.states?.length ? [...next.states] : undefined
  }
  if (Object.prototype.hasOwnProperty.call(next, 'attention')) {
    patch.attention = next.attention === 'required' ? 'required' : undefined
  }
  const applied = await replaceQuery(patch, stillCurrent)
  // A filter change is the user's own action: apply the new layout now,
  // even inside the interaction hold.
  if (applied) layoutGroups.value = canonicalGroups.value
  return applied
}

// ── Layout: lanes from filtered deliveries, frozen while interacting ────
const hold = useInteractionHold()
const canonicalGroups = computed(() => buildProjectGroups(data.deliveries.value))
const layoutGroups = shallowRef<AgentModeProjectGroup[]>([])
const canonicalLayoutIds = computed(() => new Set(flattenOrder(canonicalGroups.value)))

const canonicalAggregates = computed(() => data.snapshot.value?.aggregates ?? null)
const projectActiveTotals = computed<ReadonlyMap<number, number> | null>(() => {
  const aggregate = canonicalAggregates.value
  return aggregate
    ? new Map(aggregate.projects.map((project) => [project.projectId, project.counts.activeTotal]))
    : null
})
const laneActiveTotals = computed<ReadonlyMap<string, number> | null>(() => {
  const aggregate = canonicalAggregates.value
  return aggregate
    ? new Map(aggregate.projects.flatMap((project) => (
      project.lanes.map((lane) => [lane.laneKey, lane.counts.activeTotal] as const)
    )))
    : null
})
const aggregateLayout = shallowRef<AgentModeAggregates | null>(null)
const canvasRef = ref<HTMLElement | null>(null)
let aggregateFocusRecoverySeq = 0

function focusedAggregateKey(): string | null {
  const active = document.activeElement as HTMLElement | null
  if (!active?.isConnected || !canvasRef.value?.contains(active)) return null
  return active.closest<HTMLElement>('[data-aggregate-focus-key]')?.dataset.aggregateFocusKey ?? null
}

async function recoverOmittedAggregateFocus(recoveryKey: string | null, sequence: number) {
  await nextTick()
  if (sequence !== aggregateFocusRecoverySeq || !canvasRef.value) return
  const active = document.activeElement as HTMLElement | null
  if (active?.isConnected && active !== document.body && active !== document.documentElement) return
  const pinned = canvasRef.value.querySelector<HTMLElement>('.am-streams-selection [data-card-hit]')
  if (pinned) {
    pinned.focus()
    return
  }
  if (!recoveryKey) return
  canvasRef.value
    .querySelector<HTMLElement>(`[data-aggregate-focus-key="${cssEscape(recoveryKey)}"]`)
    ?.focus()
}

watch(
  [canonicalAggregates, hold.held],
  ([fresh, held]) => {
    const previous = aggregateLayout.value
    const focusedKey = focusedAggregateKey()
    // Aggregate omission/revocation is immediate even under hold. Surviving
    // targets retain position only; all labels/counts come from `fresh`.
    const next = fresh
      ? held
        ? reconcileAggregateOrder(previous, fresh)
        : fresh
      : null
    aggregateLayout.value = next
    const targetSurvives = focusedKey != null && aggregateFocusOrder(next).includes(focusedKey)
    aggregateFocusRecoverySeq += 1
    if (focusedKey && !targetSurvives) {
      const recoveryKey = nearestSurvivingAggregateFocusKey(previous, next, focusedKey)
      void recoverOmittedAggregateFocus(recoveryKey, aggregateFocusRecoverySeq)
    }
  },
  { immediate: true },
)

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
const preferredId = ref<string | null>(initialPreferredId)
const serverFallbackId = computed(() => data.snapshot.value?.selectedDeliveryId ?? null)
/** The pinned (filtered-out) delivery the user travelled from. It stays at
 * the head of the travel order while the filter still excludes it, so arrow
 * travel is reversible: into the results and back to the pinned card. */
const pinnedAnchorId = ref<string | null>(null)
let skipAuthorityClearedSelection = false
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
  deliveries: data.selectableDeliveries,
  order: travelOrder,
  storageKey,
  preferredId,
  fallbackId: serverFallbackId,
  retainOnEmpty: computed(() => !data.hasData.value
    && ['loading', 'offline', 'error'].includes(data.status.value)),
})
const pendingRouteSelection = ref<string | null>(null)
watch(
  () => route.query.delivery,
  (raw) => {
    // Selection/default/rejection URL synchronization echoes an identity this
    // component already owns. In particular, a selected-404 transaction may
    // serialize "remove stale" and "publish fallback" writes; neither is a
    // fresh external selection request.
    if (internalRouteWriteDepth > 0) return
    const input = raw === undefined ? null : raw ?? ''
    const changed = JSON.stringify(input) !== JSON.stringify(selectedQueryId.value)
    selectedQueryId.value = input
    const canonical = parseAgentModeDeliveryKey(input)
    preferredId.value = canonical
    pendingRouteSelection.value = canonical && canonical !== selection.selectedId.value ? canonical : null
    // Browser back/forward and a pasted deep link are real selected-only
    // request changes. A route write initiated by the selection watcher has
    // already updated selectedQueryId and already started the request.
    if (changed) void data.load({ background: data.hasData.value })
  },
  { flush: 'sync' },
)
watch(data.snapshot, () => {
  const requested = pendingRouteSelection.value
  if (!requested || !data.selectableDeliveries.value.some((delivery) => delivery.id === requested)) return
  selection.select(requested)
  pendingRouteSelection.value = null
})
const selectedDelivery = selection.selectedDelivery
const selectedOutsideReason = computed(() => {
  const snapshot = data.snapshot.value
  return snapshot?.selectedOutsideResults?.id === selection.selectedId.value
    ? snapshot.selectedOutsideReason
    : null
})
const canOpenTicket = computed(() => {
  const delivery = selectedDelivery.value
  return !!delivery
    && delivery.capabilities.viewIssue === true
    && auth.canView(delivery.lane.projectId)
})
const selectedExcludedBy = computed(() => {
  return selectedOutsideReason.value === 'filter_excluded' ? 'server' as const : null
})
watch(selection.selectedId, (id, previous) => {
  // A server-driven removal/revocation outranks local dirty state: close the
  // old ticket surface immediately so omitted data is never retained under a
  // newly selected delivery. User-driven changes are guarded before commit.
  if (ticketOpen.value && previous && previous !== id && selection.lastChange.value?.source === 'system') {
    ticketOpen.value = false
  }
  selectedQueryId.value = id
  if (skipAuthorityClearedSelection) {
    skipAuthorityClearedSelection = false
    if (id == null) return
  }
  // Server reconciliation/defaulting describes the authoritative response
  // already in hand. Only a user choice not represented by that response
  // starts the selected-only superseding request.
  if (
    selection.lastChange.value?.source === 'user'
    && !data.degraded.value
    && data.snapshot.value?.selectedDeliveryId !== id
  ) {
    void data.load({ background: data.hasData.value })
  }
  void replaceQuery({ delivery: id ?? undefined })
})
watch([selection.selectedId, selectedExcludedBy], ([id, excluded]) => {
  if (id && excluded) pinnedAnchorId.value = id
})
// Reset the anchor only when the filter VALUES change (the selection also
// writes to the URL, which must not disturb the travel head).
const filtersKey = computed(() => `${filters.value.projectId}|${filters.value.laneKey}|${filters.value.health}|${filters.value.query}|${filters.value.states?.join(',')}|${filters.value.attention}`)
watch(filtersKey, () => {
  pinnedAnchorId.value = selectedExcludedBy.value ? selection.selectedId.value : null
})

watch(permissionsEpochGeneration, () => {
  // Identity generations are a stronger boundary than an ACL refresh. Clear
  // the active route/selection vocabulary synchronously so the former
  // principal's opaque ids and filters cannot enter the replacement query.
  invalidateInternalRouteWrites()
  let validBoundary = true
  try {
    buildSnapshotPath(snapshotQuery.value)
  } catch {
    validBoundary = false
  }
  authorityRouteResetPending.value = true
  selectedQueryId.value = null
  preferredId.value = null
  pendingRouteSelection.value = null
  pinnedAnchorId.value = null
  ticketOpen.value = false
  skipAuthorityClearedSelection = true
  selection.clearForAuthorityReset()

  const resetToken = routeAuthorityToken
  if (!validBoundary) {
    // Never repair an invalid Agent Mode boundary into a broad request for a
    // different principal. Replace the entire stale surface with a safe app
    // route while the NaN latch guarantees zero Agent Mode transport.
    internalRouteWriteDepth += 1
    void router.replace('/')
      .catch(() => {})
      .finally(() => { internalRouteWriteDepth -= 1 })
    return
  }
  const next: LocationQueryRaw = { ...route.query }
  const hadPrincipalQuery = PRINCIPAL_BOUND_AGENT_MODE_QUERY_KEYS.some((key) => (
    Object.prototype.hasOwnProperty.call(next, key)
  ))
  for (const key of PRINCIPAL_BOUND_AGENT_MODE_QUERY_KEYS) delete next[key]
  if (!hadPrincipalQuery) {
    authorityRouteResetPending.value = false
    // The deliveries owner already queued this generation's single current
    // replacement. With no route scrub to await, let that owner proceed.
    return
  }
  internalRouteWriteDepth += 1
  void router.replace({ query: next })
    .then((failure) => {
      if (resetToken !== routeAuthorityToken || isNavigationFailure(failure)) return
      authorityRouteResetPending.value = false
      void data.load({ background: false, force: true })
    })
    .catch(() => {
      // Keep the empty query override active. A failed navigation must not
      // re-authorize the old principal's route values.
    })
    .finally(() => {
      internalRouteWriteDepth -= 1
    })
}, { flush: 'sync' })

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

async function selectAttention(id: string) {
  selectDelivery(id)
  await focusSelectedCard()
}

async function drill(id: string): Promise<boolean> {
  hold.markInteraction()
  if (selection.selectedId.value !== id) {
    if (!await mayChangeSelection()) return false
    if (!selection.select(id)) return false
  }
  return setDetail(1)
}

async function drillAggregate(projectId: number, laneKey: string | null) {
  hold.markInteraction()
  await replaceQuery({
    detail: undefined,
    project: String(projectId),
    lane: laneKey ?? undefined,
  })
  // The retained selection is the deterministic focus-restoration target. If
  // the server returns it outside the drill filter it remains pinned above.
  await focusSelectedCard()
}

function zoomOut() {
  if (detailLevel.value === 1) void setDetail(10)
  else if (detailLevel.value === 10) void setDetail(100)
}

interface TicketPanelHandle {
  requestLeave: () => Promise<boolean>
}
const ticketPanelRef = ref<TicketPanelHandle | null>(null)

async function openTicket() {
  if (!canOpenTicket.value || detailLevel.value !== 1) return
  ticketOpen.value = true
}

async function closeTicket() {
  ticketOpen.value = false
  await nextTick()
  const button = canvasRef.value?.querySelector<HTMLElement>('.am-focus-open-ticket')
  button?.focus()
}

async function mayChangeSelection(): Promise<boolean> {
  if (!ticketOpen.value) return true
  return (await ticketPanelRef.value?.requestLeave()) !== false
}

async function moveSelection(how: 'next' | 'prev' | 'first' | 'last'): Promise<boolean> {
  if (!await mayChangeSelection()) return false
  hold.markInteraction()
  const before = selection.selectedId.value
  const next = how === 'next'
    ? selection.step(1)
    : how === 'prev'
      ? selection.step(-1)
      : selection.selectEdge(how)
  if (!next || next === before) return false
  await focusSelectedCard()
  return true
}

const voiceEnabled = computed(() => !props.devReference)
const voiceSurfaceVisible = computed(() => voiceEnabled.value && (
  data.hasData.value
  || (data.authorityVersion.value > 0 && data.status.value === 'loading' && !sessionExpired.value)
))
const voiceOnline = computed(() => !data.degraded.value)
const controlsDependencies = inject(AGENT_MODE_CONTROLS_DEPENDENCIES_KEY, undefined)
const controls = useAgentModeControls({
  delivery: selectedDelivery,
  userId: computed(() => auth.user?.id ?? null),
  online: voiceOnline,
  degraded: data.degraded,
  authorityAvailable: data.hasData,
  authorityVersion: data.authorityVersion,
  enabled: voiceEnabled,
  dependencies: controlsDependencies,
})
const controlVoicePhrase = computed(() => {
  const binding = controls.boundCommand.value
  if (!binding || binding.command.status !== 'pending_confirmation') return ''
  return buildControlVoicePhrase(binding.command, locale.value)
})
const voiceControlChallenge = computed(() => {
  const binding = controls.boundCommand.value
  const phrase = controlVoicePhrase.value
  if (!binding || binding.command.status !== 'pending_confirmation' || phrase === '') return null
  return { command: binding.command, issueKey: binding.command.display.issueKey, phrase }
})

// The compact controls row is authorized asynchronously after the selected
// card was positioned. Keep that target inside the now-smaller scrollport
// without moving focus away from either the canvas or a control the operator
// is already using.
watch([controls.state, controls.targets], async () => {
  if (!compactConversation.value) return
  await nextTick()
  const id = selection.selectedId.value
  if (!id || !canvasRef.value) return
  canvasRef.value.querySelector<HTMLElement>(`[data-card-hit="${cssEscape(id)}"]`)
    ?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
}, { flush: 'post' })
const voiceDependencies = inject(AGENT_MODE_VOICE_DEPENDENCIES_KEY, undefined)
const voiceOneShotWarning = computed(() => selectedDelivery.value?.capabilities.oneShotRunActive === true
  && selectedDelivery.value.capabilities.liveNote !== true)

interface VoiceActionContext {
  selectedId: string | null
  travelOrder: string
  authorityVersion: number
  permissionsEpoch: string | null
  permissionsEpochGeneration: number
}

function captureVoiceActionContext(): VoiceActionContext {
  return {
    selectedId: selection.selectedId.value,
    travelOrder: travelOrder.value.join('\u0000'),
    authorityVersion: data.authorityVersion.value,
    permissionsEpoch: permissionsEpoch.value,
    permissionsEpochGeneration: permissionsEpochGeneration.value,
  }
}

function voiceActionContextIsCurrent(context: VoiceActionContext): boolean {
  return voiceEnabled.value
    && voice.operational.value
    && data.hasData.value
    && !sessionExpired.value
    && selection.selectedId.value === context.selectedId
    && travelOrder.value.join('\u0000') === context.travelOrder
    && data.authorityVersion.value === context.authorityVersion
    && permissionsEpoch.value === context.permissionsEpoch
    && permissionsEpochGeneration.value === context.permissionsEpochGeneration
}

async function voiceSelectDelivery(id: string): Promise<boolean> {
  if (selection.selectedId.value === id) return true
  const actionContext = captureVoiceActionContext()
  if (!await mayChangeSelection()) return false
  if (!voiceActionContextIsCurrent(actionContext)) return false
  hold.markInteraction()
  if (!selection.select(id)) return false
  await focusSelectedCard()
  return true
}

function voiceSetDetail(level: DetailLevel): Promise<boolean> {
  const actionContext = captureVoiceActionContext()
  return setDetail(level, () => voiceActionContextIsCurrent(actionContext))
}

async function voiceSetFilters(next: Partial<AgentModeFilters>): Promise<boolean> {
  if (data.degraded.value) return false
  const actionContext = captureVoiceActionContext()
  return setFilters(next, () => voiceActionContextIsCurrent(actionContext))
}

async function voiceShowDetails(id: string): Promise<boolean> {
  let actionContext = captureVoiceActionContext()
  if (selection.selectedId.value !== id) {
    if (!await mayChangeSelection()) return false
    if (!voiceActionContextIsCurrent(actionContext)) return false
    if (!selection.select(id)) return false
    actionContext = captureVoiceActionContext()
  }
  return setDetail(1, () => voiceActionContextIsCurrent(actionContext))
}

const voice = useAgentModeVoice({
  deliveries: data.selectableDeliveries,
  travelOrder,
  selectedId: selection.selectedId,
  online: voiceOnline,
  degraded: data.degraded,
  locale,
  authorityAvailable: data.hasData,
  authorityVersion: data.authorityVersion,
  authorityEpoch: data.authorityEpoch,
  controlTargets: controls.targets,
  controlChallenge: voiceControlChallenge,
  enabled: voiceEnabled,
  dependencies: voiceDependencies,
  actions: {
    selectDelivery: voiceSelectDelivery,
    setFilters: voiceSetFilters,
    clearFilters: () => voiceSetFilters({ ...EMPTY_FILTERS }),
    setDetail: voiceSetDetail,
    showDetails: voiceShowDetails,
    requestControl: controls.activate,
    confirmControl: controls.confirmExact,
    notePosted: () => data.retryNow(),
    authorityChanged: () => data.load({ background: false, force: true }),
  },
})

const projectCatalog = computed(() => voice.projectCatalog.value)
const projectPickerTotals = computed<ReadonlyMap<number, number> | null>(() => {
  const totals = projectActiveTotals.value
  if (!totals || filtersActive(filters.value)) return totals
  return new Map(projectCatalog.value.map((project) => [
    project.projectId,
    totals.get(project.projectId) ?? 0,
  ]))
})
const commandHints = computed(() => {
  const flags = canonicalAggregates.value?.root.flags
  return buildAgentModeCommandHints({
    locale: locale.value,
    detailLevel: detailLevel.value,
    selected: selectedDelivery.value != null,
    travelCount: travelOrder.value.length,
    filtersActive: filtersActive(filters.value),
    health: filters.value.health,
    blockedCount: flags?.blocked ?? 0,
    attentionCount: flags?.attention ?? 0,
    staleCount: flags?.stale_no_signal ?? 0,
    candidateCount: voice.candidateMatchCount.value,
    notePending: voice.note.value != null,
  })
})

/** Deterministic entry seam for card activation now and voice intent later.
 * It changes semantic zoom only; opening the mouse editor remains explicit. */
defineExpose({ showDetails: drill })

// Browser back/forward and other route-driven zoom changes use the same
// editor handshake as the in-canvas lever. This keeps a dirty ticket bound
// to its delivery instead of silently hiding the editor underneath Detail 10.
onBeforeRouteUpdate(async (to, from) => {
  if (!ticketOpen.value || parseDetail(from.query.detail) !== 1 || parseDetail(to.query.detail) === 1) return true
  if (!await mayChangeSelection()) return false
  ticketOpen.value = false
  return true
})

// Capability or project-access revocation immediately removes the issue
// surface. The selected delivery may remain visible only if the refreshed
// snapshot still authorizes it; the ticket payload is never retained.
watch(canOpenTicket, (allowed) => {
  if (!allowed) ticketOpen.value = false
})

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
      void moveSelection('next')
      break
    case 'ArrowLeft':
    case 'ArrowUp':
      event.preventDefault()
      void moveSelection('prev')
      break
    case 'Home':
      event.preventDefault()
      void moveSelection('first')
      break
    case 'End':
      event.preventDefault()
      void moveSelection('last')
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
  const root = aggregateLayout.value?.root
  return root
    ? {
        total: root.activeTotal,
        healthy: 0,
        attention: root.flags.attention,
        blocked: root.flags.blocked,
        stale: root.flags.stale_no_signal,
        unknown: root.flags.unknown_reporter,
      }
    : null
})

const headline = computed(() => {
  const c = counts.value
  if (!c) return t('agentMode.aggregate.unavailable')
  const n = c.total
  if (n === 0) return t('agentMode.headline.none')
  if (n === 1) return t('agentMode.headline.one')
  return t('agentMode.headline.many', { n })
})
const breakdown = computed(() => {
  const c = counts.value
  if (!c) return ''
  const parts: string[] = []
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
  const est = estimateView(d, locale.value, serverNowMs.value, data.degraded.value)
  let text = t('agentMode.narration.selection', { key: d.issueKey, actor, activity })
  if (est.presentation.rangeOnly && est.rangeLabel) {
    text += ' ' + t('agentMode.narration.selectionRange', { range: est.rangeLabel })
  } else if (est.landingLabel) {
    text += ' ' + t('agentMode.narration.selectionEta', { time: est.landingLabel, remaining: est.remainingLabel ?? '—' })
  } else {
    const reason = est.presentation.etaReason === 'ok' ? 'none' : est.presentation.etaReason
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
  if (detailLevel.value === 100 && !aggregateLayout.value) {
    lines.push({ id: 'aggregate-unavailable', role: 'system', text: t('agentMode.aggregate.unavailableNarration') })
    const selected = selectedDelivery.value
    if (selected) lines.push({ id: `selection:${selected.id}`, role: 'system', text: selectionSentence(selected) })
    return lines
  }
  const c = counts.value
  if (!c) return lines
  const n = c.total
  const summary = t('agentMode.narration.summary', { n }, n)
  lines.push({ id: 'summary', role: 'system', text: breakdown.value ? `${summary} ${breakdown.value}.` : summary })
  if (c.attention > 0) {
    lines.push({
      id: 'attention',
      role: 'system',
      text: t('agentMode.narration.attentionOffer', { n: c.attention }, c.attention),
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
  // Selection identity is authorized snapshot data too. A full ACL
  // revocation/404 reconciles to a null selection; clear the live-region
  // payload instead of retaining the previously announced key/title.
  announcement.value = ''
  if (!change?.id) return
  const d = data.deliveriesById.value.get(change.id)
  if (!d) return
  announcement.value = t('agentMode.a11y.selectionChanged', { key: d.issueKey, title: d.title })
})
watch(data.snapshot, (current) => {
  const id = selection.selectedId.value
  if (!current || !id || !data.deliveriesById.value.has(id)) announcement.value = ''
}, { flush: 'sync' })

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
  <div class="am-root" :class="{ 'am-root--compact': compactConversation, 'am-root--ticket': ticketOpen }">
    <Teleport defer to="#app-header-left">
      <span class="ah-title">{{ t('agentMode.title') }}</span>
      <span v-if="data.hasData.value && counts" class="ah-subtitle">{{ t('agentMode.subtitle', { n: counts.total }, counts.total) }}</span>
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

    <AgentModeConversation :lines="narrationLines" :live="feedLive" :live-label="liveLabel" :compact="compactConversation">
      <template #controls>
        <AgentModeControlsCard
          v-if="voiceSurfaceVisible && selectedDelivery"
          :state="controls.state.value"
          :targets="controls.targets.value"
          :command="controls.boundCommand.value"
          :busy="controls.busy.value"
          :available="controls.controlAvailable.value"
          :transition-available="controls.transitionAvailable.value"
          :selected-issue-key="selectedDelivery.issueKey"
          :voice-phrase="controlVoicePhrase"
          :focus-token="controls.focusToken.value"
          :focus-return-token="controls.focusReturnToken.value"
          @activate="controls.activate"
          @confirm="controls.confirm"
          @withdraw="controls.withdraw"
          @retry="controls.initialize"
          @dismiss="controls.dismissTerminal"
        />
        <AgentModeVoiceConsole
          v-if="voiceSurfaceVisible"
          :mic-state="voice.micState.value"
          :mic-level="voice.micLevel.value"
          :mic-supported="voice.micSupported()"
          :permission="voice.permission.value"
          :wants-listening="voice.wantsListening.value"
          :mic-start-pending="voice.micStartPending.value"
          :speech-active="voice.speechActive.value"
          :authorized="voice.operational.value"
          :audio-available="voice.audioAvailable.value"
          :voice-replies-enabled="voice.voiceRepliesEnabled.value"
          :reply-state="voice.replyState.value"
          :draft="voice.draft.value"
          :candidates="voice.candidates.value"
          :candidate-match-count="voice.candidateMatchCount.value"
          :candidate-truncated="voice.candidateTruncated.value"
          :note="voice.note.value"
          :note-target="voice.noteTarget.value"
          :notice="voice.notice.value"
          :unsupported-control="voice.unsupportedControl.value"
          :error="voice.error.value"
          :busy="voice.commandBusy.value"
          :note-focus-token="voice.noteFocusToken.value"
          :input-reset-token="voice.inputResetToken.value"
          :one-shot-warning="voiceOneShotWarning"
          :compact="compactConversation"
          :hints="commandHints"
          @toggle-mic="voice.toggleListening"
          @set-replies="voice.setVoiceReplies"
          @submit="voice.submitTyped"
          @choose="voice.chooseCandidate"
          @confirm-note="voice.confirmNote"
          @cancel-note="voice.cancelNote"
        />
      </template>
    </AgentModeConversation>

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
      <!-- Keep one stable filter instance mounted across loading/invalid/error
           transitions so the user can finish typing or repair the URL without
           losing focus or draft text. -->
      <AgentModeFilterBar
        v-if="detailLevel === 10"
        :filters="filters"
        :aggregates="canonicalAggregates"
        :project-catalog="projectCatalog"
        :project-active-totals="projectPickerTotals"
        :density="cardDensity"
        @update:filters="setFilters"
        @update:density="cardDensity = $event"
      />

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

        <div
          v-if="selectedOutsideReason"
          class="am-selection-outside-status"
          :data-selected-outside-reason="selectedOutsideReason"
          role="status"
        >
          <AppIcon name="pin" :size="12" aria-hidden="true" />
          {{ t(`agentMode.selection.outsideReason.${selectedOutsideReason}`) }}
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
          :ticket-open="ticketOpen"
          :ticket-available="canOpenTicket"
          @prev="moveSelection('prev')"
          @next="moveSelection('next')"
          @zoom-out="setDetail(10)"
          @open-ticket="openTicket"
          @interact="hold.markInteraction"
        />

        <!-- Portfolio overview: one pinned card + authoritative aggregates -->
        <AgentModePortfolioOverview
          v-else-if="detailLevel === 100"
          :aggregates="aggregateLayout"
          :unavailable-reason="data.snapshot.value?.aggregateUnavailableReason ?? null"
          :deliveries="data.deliveries.value"
          :selected-delivery="selectedDelivery"
          :selected-id="selection.selectedId.value"
          :server-now-ms="serverNowMs"
          :locale="locale"
          :degraded="data.degraded.value"
          @drill-selection="drill"
          @drill-aggregate="drillAggregate"
          @select-attention="selectAttention"
          @interact="hold.markInteraction"
        />

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
            :authoritative="canonicalAggregates?.attention"
            @select="selectDelivery"
          />

          <p v-if="layoutGroups.length === 0" class="am-nomatch">{{ t('agentMode.filters.noMatch') }}</p>

          <AgentModeLanes
            :groups="layoutGroups"
            :deliveries-by-id="data.deliveriesById.value"
            :project-active-totals="projectActiveTotals"
            :lane-active-totals="laneActiveTotals"
            :tombstone-ids="tombstoneIds"
            :selected-id="selection.selectedId.value"
            :lifted-selected-id="selection.selectedId.value"
            :server-now-ms="serverNowMs"
            :locale="locale"
            :degraded="data.degraded.value"
            :density="cardDensity"
            @select="selectDelivery"
            @activate="drill"
            @interact="hold.markInteraction"
          />
        </template>
      </template>
    </main>

    <IssueSidePanel
      v-if="ticketOpen && selectedDelivery && canOpenTicket"
      id="agent-mode-ticket-panel"
      ref="ticketPanelRef"
      class="am-ticket-panel"
      :issue-id="selectedDelivery.issueId"
      :readonly="!auth.canEdit(selectedDelivery.lane.projectId) || selectedDelivery.capabilities.editIssue !== true"
      :allow-attachments="auth.canEdit(selectedDelivery.lane.projectId) && selectedDelivery.capabilities.attach === true"
      :allow-comments="auth.canEdit(selectedDelivery.lane.projectId) && selectedDelivery.capabilities.comment === true"
      :internal-comments-only="true"
      :note-affects-next-run="selectedDelivery.capabilities.oneShotRunActive === true && selectedDelivery.capabilities.liveNote !== true"
      embedded
      @close="closeTicket"
      @updated="data.retryNow"
    />
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
.am-root--compact :deep(.am-conv-controls) {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  align-items: start;
  gap: 8px;
}
.am-root:not(.am-root--compact) :deep(.am-controls) { margin-bottom: 8px; }
.am-root--compact :deep(.am-controls-live:empty) { display: none; }
.am-root--ticket:not(.am-root--compact) {
  grid-template-columns: 232px minmax(420px, 1fr) minmax(360px, 420px);
}
.am-root--ticket:not(.am-root--compact) :deep(.am-conv) { grid-column: 1; grid-row: 1; }
.am-root--ticket:not(.am-root--compact) .am-canvas { grid-column: 2; grid-row: 1; }
.am-root--ticket:not(.am-root--compact) :deep(.am-ticket-panel) { grid-column: 3; grid-row: 1; }
.am-root--ticket.am-root--compact {
  grid-template-columns: minmax(0, 1fr) minmax(340px, 42vw);
  grid-template-rows: minmax(0, 1fr) auto;
}
.am-root--ticket.am-root--compact .am-canvas { grid-column: 1; grid-row: 1; }
.am-root--ticket.am-root--compact :deep(.am-conv) { grid-column: 1; grid-row: 2; }
.am-root--ticket.am-root--compact :deep(.am-ticket-panel) { grid-column: 2; grid-row: 1 / span 2; }

@media (max-width: 736px) {
  .am-root--ticket.am-root--compact {
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: minmax(320px, 1fr) minmax(340px, 52vh) auto;
    overflow: auto;
  }
  .am-root--ticket.am-root--compact .am-canvas { grid-column: 1; grid-row: 1; min-height: 320px; }
  .am-root--ticket.am-root--compact :deep(.am-ticket-panel) { grid-column: 1; grid-row: 2; min-height: 340px; }
  .am-root--ticket.am-root--compact :deep(.am-conv) { grid-column: 1; grid-row: 3; }
}

@media (max-width: 640px) {
  .am-root--compact :deep(.am-conv-controls) {
    grid-template-columns: minmax(120px, 0.8fr) minmax(0, 1.4fr);
  }
}

@media (prefers-reduced-motion: reduce) {
  /* Detail 1 embeds the existing issue editor inside Agent Mode. Its generic
     hover/fade transitions must obey the shell's reduced-motion contract too. */
  .am-root :deep(.side-panel--embedded *),
  .am-root :deep(.side-panel--embedded *::before),
  .am-root :deep(.side-panel--embedded *::after) {
    scroll-behavior: auto !important;
    animation: none !important;
    transition: none !important;
  }
}

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
.am-selection-outside-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  width: fit-content;
  padding: 5px 9px;
  border: 1px solid color-mix(in srgb, var(--am-select) 32%, var(--am-line));
  border-radius: 999px;
  background: color-mix(in srgb, var(--am-select) 6%, var(--am-surface));
  color: var(--am-select);
  font-size: 10.5px;
  font-weight: 600;
}

.am-nomatch { margin: 0; color: var(--am-muted); font-size: 13px; }

</style>
