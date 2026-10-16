<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
import { shallowRef } from 'vue'
import { onAction } from '../../lib/shortcuts'
interface PanelShortcutOwner { enabled: () => boolean; when: (event: KeyboardEvent) => boolean; select: (index: number) => false | void; toggle: () => void }
const panelOwners: PanelShortcutOwner[] = [], panelOwnersRevision = shallowRef(0)
let panelBindings: (() => void)[] = []
function registerPanelShortcuts(owner: PanelShortcutOwner) {
  panelOwners.push(owner); panelOwnersRevision.value++
  const current = () => { void panelOwnersRevision.value; return panelOwners.at(-1) }
  if (panelOwners.length === 1) {
    const scope = { enabled: () => !!current()?.enabled(), when: (event: KeyboardEvent) => !!current()?.when(event) }
    panelBindings = [onAction('agents.panel.fullscreen', () => current()?.toggle(), scope),
      ...([1, 2, 3, 4, 5] as const).map(number => onAction(`agents.panel.tab.${number}`, () => current()?.select(number - 1), scope))]
  }
  return () => {
    const index = panelOwners.indexOf(owner)
    if (index >= 0) panelOwners.splice(index, 1)
    panelOwnersRevision.value++
    if (!panelOwners.length) { panelBindings.forEach(dispose => dispose()); panelBindings = [] }
  }
}
</script>
<script setup lang="ts">
import { capacityWaitText } from '../../lib/capacityWait'
import { brand } from '../../lib/brand'
import { api, getNode } from '../../lib/api'
import { readSessionNativeContext } from '../../lib/agentRows'
import { parseJson } from '../../lib/json'
import { can, ensurePermissions } from '../../lib/authz'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Approval, SessionChangeRequest, SessionControl } from '../../lib/agents'
import { RUN_OUTCOME, approvalRun, cost, elapsed, runDuration, runModel, scopeLabel, stopReasonLabel, tokens } from '../../lib/agentState'
import { absoluteTime, relativeTime, statusMeta } from '../../lib/work'
import { useAgents, type SessionView } from '../../stores/agents'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import TicketPeekLink from '../TicketPeekLink.vue'
import { useRoute, useRouter } from 'vue-router'
import SessionChat from './SessionChat.vue'
import SessionRequests from './SessionRequests.vue'
import SessionTabs from './SessionTabs.vue'
import { createPanelOpening, leadTab, openingTab, panelModelLine, panelQueryTab, PANEL_TAB_LABEL, panelTabs, type PanelLayout, type PanelTab } from '../../lib/leadPanel'
import { leadActionAvailable, leadActionReason } from '../../lib/lead'
import { openLeadPause } from '../../lib/leadOverlay'
import { usePreference } from '../../lib/preferences'
import { useAgentProjection } from '../../stores/agentProjection'
import { useProjectLeads } from '../../stores/projectLeads'
import { useAgentPause } from '../../stores/agentPause'
import { useProjects } from '../../stores/projects'
import { managedControlAllowed } from '../../lib/managedControl'
import { pausedSession } from '../../lib/agentPause'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import LeadContent from '../lead/LeadContent.vue'
import { useVisualViewport } from '../../lib/visualViewport'
import AgentGlyph from './AgentGlyph.vue'
import ProvenanceDetail from './ProvenanceDetail.vue'
import SessionStateEvidence from './SessionStateEvidence.vue'
import ListeningLabel from './ListeningLabel.vue'
import SessionRecovery from './SessionRecovery.vue'
import { useAgentRecovery } from '../../lib/agentRecovery'
import { capabilityWords, chatCapability } from './sessionChat'
import { displayLanguage } from '../../lib/displayLanguage'
import RemoveSessionDialog from './RemoveSessionDialog.vue'
import ManagedSessionControls from './ManagedSessionControls.vue'
import PauseEvidence from './PauseEvidence.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import { pausingSession } from '../../lib/agentPause'
import LiveWatch from './LiveWatch.vue'
import { activityOf, currentStep, currentActivity, activityDurations } from './activity'
import { cleanActivityNote } from '../../lib/activityPrivacy'
import { metadataChangeText, metadataChanges } from './metadataHistory'
import SessionEstimate from './SessionEstimate.vue'
import DeliveryRating from '../work/DeliveryRating.vue'
import { etaFromSession } from '../../lib/eta'
import { quickRemoval } from './sessionActions'
import { sessionEtaEligible } from './sessionRow'
import SessionHost from './SessionHost.vue'
import { listHostLabels } from '../../lib/agents'
import ServiceTierBlock from './ServiceTierBlock.vue'
import { tierHistoryText, tierRunCostLabel, tierCostAmount } from '../../lib/serviceTier'
import { useReaderAnchor } from '../../lib/readerAnchor'
import { shortcutHint, shortcutKeys, shortcutTyping } from '../../lib/shortcuts'
import { isHolderTerminalTarget, type TerminalActionView } from '../../lib/nativeTerminal/ports'
import { bindingKey, liveTerminalLink, type ExpertRequest, type ExpertViewer, type NativeExpertTransport } from '../../lib/nativeExpert/ports'
import { boundedEvent, boundedPage } from '../../lib/nativeExpert/buffer'
import { SessionEvents, SessionTerminal } from '../../lib/nativeExpert/views'
import NativeTerminal from '../nativeExpert/NativeTerminal.vue'
import { nativeSessionBounds, nativeSessionPayloadSchemas, type NativeBinding, type NativeSessionView, type NativeStreamEvent, type NativeEventPage } from '../../lib/nativeSessionContract'
import { useServiceTiers } from '../../stores/serviceTiers'
const serviceTiers = useServiceTiers()

// One panel menu, with Lead first for the bound project lead.
const actionsAnchor = ref<HTMLElement | null>(null)
const composing = ref(false)
function chatFocus(event: FocusEvent) {
  if ((event.target as HTMLElement).matches('#session-panel-messages textarea')) composing.value = true
}
function chatBlur(event: FocusEvent) {
  // Removing Reply/Edit feedback blurs its button to the body. Keep the
  // compact frame until focus explicitly leaves this panel.
  if (event.relatedTarget instanceof Node && !root.value?.contains(event.relatedTarget)) composing.value = false
}
const props = defineProps<{ view: SessionView | undefined; loading: boolean; now: number; canWrite: boolean; docked?: boolean; sheet?: boolean; layout?: PanelLayout; sidebarAvailable?: boolean; controlBlock: (view: SessionView, kind: SessionControl['kind']) => string; section?: 'next-up' | null }>()
const localLayout = ref<PanelLayout>('side')
const panelLayout = computed(() => props.layout ?? localLayout.value)
const localLargeLayout = usePreference<{ layout?: PanelLayout }>('agents:panel.layout.large'), localSmallLayout = usePreference<{ layout?: PanelLayout }>('agents:panel.layout.phone')
let localLayoutWrite = 0
async function requestLayout(next: PanelLayout, automatic = false) {
  if (props.layout !== undefined) { if (automatic) emit('autoLayout', next); else emit('layout', next); return }
  const write = ++localLayoutWrite, who = auth.identity?.principal.id, id = s.value?.id
  localLayout.value = next
  if (automatic) return
  const preference = window.innerWidth <= 720 || window.innerHeight <= 480 ? localSmallLayout : localLargeLayout
  if (await preference.ensureLoaded() && write === localLayoutWrite && who === auth.identity?.principal.id && id === s.value?.id) preference.save({ ...preference.value.value, layout: next }, 0)
}
const agentRecovery = useAgentRecovery(() => props.view?.session.id)
const emit = defineEmits<{ close: []; control: [view: SessionView, kind: SessionControl['kind']]; review: [approval: Approval]; layout: [layout: PanelLayout]; autoLayout: [layout: PanelLayout] }>()
watch(() => props.view?.session.id, () => { actionsAnchor.value = null; composing.value = false })
const s = computed(() => props.view?.session)
const capability = computed(() => s.value ? chatCapability(s.value) : 'between')
const capText = computed(() => capabilityWords[displayLanguage()][capability.value])
const agents = useAgents()
const auth = useSession()
// The person's own name for this computer (AEON-1211): compact rows have no Host cell, so the
// details name it and rename it. A late answer for another session or person is dropped.
const hostLabel = ref('')
const hostPerson = computed(() => auth.identity?.principal.kind === 'person')
let hostRead = 0
watch([() => props.view?.session.host, () => auth.identity?.principal.id], async ([host]) => {
  const read = ++hostRead
  hostLabel.value = ''
  if (!host || !hostPerson.value) return
  try {
    const labels = await listHostLabels()
    if (read === hostRead) hostLabel.value = labels.find(item => item.host === host)?.label ?? ''
  } catch { /* The registered name stays; the editor reads the labels again when opened. */ }
}, { immediate: true })
const root = ref<HTMLElement>()
const thread = ref<HTMLElement>()
// AEON-1279: anchor visible content, never the clipped absolute Now label.
useReaderAnchor({ scroller: () => thread.value, rows: () => thread.value?.querySelectorAll<HTMLElement>(':scope > :not(.sr-only):not([aria-hidden="true"]), :scope > section > :not(.sr-only):not([aria-hidden="true"])') })
const recovery = ref<{ open: (anchor?: HTMLElement) => void }>()
const removal = ref<{ remove: () => void }>()
// Same breakpoint as the full-height sheet. Only a managed session with the
// control capability collapses Recover, Remove and settings into one overflow.
const managedControls = computed(() => {
  const session = props.view?.session
  return !!session && !session.watch && session.management_mode === 'managed' && session.advertised_capabilities.includes('managed_control_v1')
})
// A watched session is read-only (AEON-258): no controls to collapse.
const showRecover = computed(() => {
  const session = props.view?.session
  return !!session && !session.watch && !session.archived_at && (can('harness.recover', session.project_id) || can('harness.force_stop', session.project_id))
})
const showRemove = computed(() => {
  const session = props.view?.session
  return !!session && !session.watch && auth.identity?.principal.kind === 'person' && !session.archived_at && can('harness.read', session.project_id)
})
// AEON-1212: one panel. Choice on opening only; live state never pulls someone
// off the tab they are reading. Both preferences belong to the signed-in person.
const route = useRoute(), router = useRouter()
const projection = useAgentProjection(), leads = useProjectLeads(), pause = useAgentPause(), projects = useProjects()
const leadPreference = usePreference<{ pinned?: unknown; last?: unknown }>('agents:lead-panel'), workerPreference = usePreference<{ last?: unknown }>('agents:session-panel')
const tab = ref<PanelTab>('overview'), unread = ref(0), why = ref(''), openedTab = ref<PanelTab>('overview')
const questionFocus = ref('')
const managed = ref<InstanceType<typeof ManagedSessionControls>>()
const chat = ref<InstanceType<typeof SessionChat>>()
const owner = computed(() => auth.identity ? `${auth.identity.tenant.id}:${auth.identity.principal.id}` : '')
const lead = computed(() => s.value?.project_id ? leads.views[s.value.project_id]?.lead ?? null : null)
const isLead = computed(() => !!s.value && lead.value?.session_id === s.value.id)
const project = computed(() => s.value ? projects.byId(s.value.project_id) : null)
const pinned = computed(() => leadTab(leadPreference.value.value?.pinned))
const questionReadable = computed(() => !!s.value && can('questions.read', s.value.project_id) && projection.questions.state !== 'forbidden')
const questions = computed(() => s.value && questionReadable.value ? projection.asksOf(s.value.id).filter(q => q.project_id === s.value!.project_id) : [])
const hasQuestion = computed(() => questions.value.length > 0 && auth.identity?.principal.kind === 'person' && can('questions.decide', s.value?.project_id))
const native = ref<NativeSessionView | null>(null), nativeReason = ref('Reading the session’s live capabilities…'), terminalAtOpen = ref(false), nativeKnown = ref(false), menuReady = ref(false), leadAtOpen = ref(false), viewerAtOpen = ref(false)
const availableTabs = computed(() => panelTabs(leadAtOpen.value, terminalAtOpen.value, !!s.value?.watch))
const viewer = computed<ExpertViewer>(() => ({ tenantId: auth.identity?.tenant.id ?? '', principalId: auth.identity?.principal.id ?? '',
  kind: auth.identity?.principal.kind === 'person' ? 'person' : 'agent', canRead: !!s.value && can('harness.read', s.value.project_id),
  // Consent is never inferred from a capability or from the current person's ownership.
  canWatch: false, canReadStructured: !!s.value && can('harness.read', s.value.project_id) }))
const terminalLink = computed(() => native.value ? liveTerminalLink(native.value, viewer.value) : null)
const nativeBrowserReason = 'Only via aeon on the computer that runs it'
const nativeAction = computed<TerminalActionView | null>(() => {
  const current = native.value, binding = current?.current_binding
  if (!current || !binding || viewer.value.kind !== 'person') return null
  const kind = current.controller ? current.controller.actor_id === viewer.value.principalId ? 'return' : 'end' : 'take'
  return { kind, label: kind === 'return' ? 'Hand back' : kind === 'end' ? 'End control…' : 'Take over', disabled: true,
    reason: nativeBrowserReason, binding, revision: current.revision, controllerEpoch: current.controller?.epoch ?? null }
})

let nativeRead = 0, nativePanel = ''
let nativeReady: Promise<void> = Promise.resolve()
watch([() => s.value?.project_id, () => s.value?.id, owner, () => s.value?.heartbeat_at], (_keys, _old, cleanup) => {
  const read = ++nativeRead, selected = s.value
  // AEON-1439: the menu is fixed once per opening. A refresh that briefly drops
  // the session or the identity keeps it; the same pair back reads again.
  if (!selected || !owner.value) { if (!nativePanel) menuReady.value = true; return }
  const panel = `${owner.value}/${selected.project_id}/${selected.id}`
  const fresh = panel !== nativePanel; nativePanel = panel
  if (fresh) { native.value = null; nativeKnown.value = false; terminalAtOpen.value = false; menuReady.value = false; leadAtOpen.value = false; viewerAtOpen.value = false; nativeReason.value = 'Reading the session’s live capabilities…' }
  const abort = new AbortController(); cleanup(() => abort.abort())
  const signal = AbortSignal.any([abort.signal, AbortSignal.timeout(3000)])
  nativeReady = (async () => {
    try {
      await Promise.all([ensurePermissions(selected.project_id), leads.loadLead(selected.project_id)])
      const value = await readBoundedJSON(await readSessionNativeContext(selected.project_id, selected.id, signal))
      if (read !== nativeRead || abort.signal.aborted) return
      if (!isNativeContext(value) || (value.current_binding && (value.current_binding.tenant_id !== viewer.value.tenantId || value.current_binding.project_id !== selected.project_id || value.current_binding.harness_session_id !== selected.id || value.current_binding.owned_session_id !== value.owned_session_id))) throw new Error('Invalid native context')
      native.value = value; nativeKnown.value = true; nativeReason.value = ''
      if (!menuReady.value) viewerAtOpen.value = !!terminalLink.value
      if (!menuReady.value) terminalAtOpen.value = viewer.value.kind === 'person' && viewer.value.canRead && (value.capabilities.take_supported || value.mode === 'terminal' || !!terminalLink.value)
    } catch (error) {
      if (read !== nativeRead || abort.signal.aborted) return
      // Once identified as owned, failed context reads never fall back to legacy writes.
      native.value = null
      const reason = (error as { reason?: string }).reason
      nativeReason.value = reason === 'feature_disabled' || reason === 'permission_revoked' ? 'Live events are not available for this session yet.' : 'The session’s live capabilities could not be read. Reopen the panel to try again.'
    } finally { if (read === nativeRead) { if (!menuReady.value) leadAtOpen.value = isLead.value; menuReady.value = true } }
  })()
}, { immediate: true, flush: 'sync' })

let opening = 0
function selectTab(next: PanelTab) {
  if (!availableTabs.value.includes(next)) return
  opening++ // A person's choice cancels any automatic selection still awaiting reads.
  tab.value = next
  if (next === 'terminal' && window.innerHeight <= 480 && panelLayout.value !== 'full') void requestLayout('full', true)
  if (isLead.value) leadPreference.save({ ...leadPreference.value.value, last: next }, 0)
  else workerPreference.save({ last: next }, 0)
  if (route.query.tab !== undefined || route.hash === '#next-up') { const query = { ...route.query }; delete query.tab; void router.replace({ query, hash: '' }) }
}
function pinTab() { if (isLead.value) leadPreference.save({ ...leadPreference.value.value, pinned: pinned.value === 'lead' ? null : 'lead' }, 0) }
function closeActions(restore: boolean) {
  const anchor = actionsAnchor.value
  actionsAnchor.value = null
  if (restore && anchor?.isConnected) anchor.focus({ preventScroll: true })
}
function showQuestion(id: string) { selectTab('chat'); questionFocus.value = ''; void nextTick(() => { questionFocus.value = id }) }
// AEON-1439: one opening per panel open (another person or another session).
// Refreshes, polls and identity re-reads never choose again, never blank `why`
// and never move the tab a person picked.
const openings = createPanelOpening()
// The menu's latest read: a heartbeat or a brief drop can replace the read an opening started with.
async function menuSettled() { let ready: Promise<void>; do { ready = nativeReady; await ready } while (ready !== nativeReady) }
watch([() => s.value?.id, owner], async () => {
  const selected = s.value, who = owner.value, key = openings.begin(who, selected?.id)
  if (!key || !selected) return
  const mine = ++opening
  questionFocus.value = ''; why.value = ''
  const scope = projection.watchProjects({ project: selected.project_id })
  try {
    // nativeReady owns the opening lead read too. A second loadLead here can
    // discard that read before the fixed opening menu captures its Lead item.
    await Promise.all([leadPreference.ready, workerPreference.ready, menuSettled(), ensurePermissions(selected.project_id), projection.refresh()])
    if (mine !== opening) return
    // Missing while the reads returned (another person or session would have begun its own opening): open on the next sight.
    if (selected.id !== s.value?.id || who !== owner.value) { openings.abandon(key); return }
    const ids = projection.workersOf(selected.project_id, selected.id)
    const workers = agents.views.filter(v => ids?.has(v.session.id))
    const choice = openingTab({ lead: isLead.value, forced: panelQueryTab(route.query.tab, isLead.value), asksYou: hasQuestion.value,
      held: lead.value?.state === 'waiting_for_room', ended: selected.phase === 'stopped' || !!selected.stopped_at,
      workerLost: workers.some(v => v.status.state === 'unresponsive'), workerProblem: workers.some(v => v.status.state === 'problem'),
      pinned: pinned.value, last: (isLead.value ? leadPreference.value.value?.last : workerPreference.value.value?.last) as PanelTab | undefined })
    tab.value = availableTabs.value.includes(choice.tab) ? choice.tab : isLead.value ? 'lead' : 'overview'; openedTab.value = tab.value; why.value = route.hash === '#next-up' || props.section === 'next-up' ? 'Next up, from the queue count' : choice.why
    if ((route.hash === '#next-up' || props.section === 'next-up') && isLead.value) tab.value = openedTab.value = 'lead'
  } finally { scope() }
}, { immediate: true })
// Only a link or a Next up request that changed chooses again; any other route
// change leaves the person's tab alone (AEON-1439).
watch([() => String(route.query.tab ?? ''), () => route.hash, () => props.section], ([query, hash, section], [lastQuery, lastHash, lastSection]) => {
  if (section === 'next-up' && lastSection !== 'next-up' && isLead.value) { tab.value = openedTab.value = 'lead'; why.value = 'Next up, from the queue count' }
  const forced = query !== lastQuery || hash !== lastHash ? panelQueryTab(route.query.tab, isLead.value) : null
  if (forced && availableTabs.value.includes(forced)) { tab.value = forced; openedTab.value = forced; why.value = route.hash === '#next-up' ? 'Next up, from the queue count' : 'from this link' }
})
// A session still shown whose lead record names another session stopped being
// the lead; a refresh that briefly drops either keeps the tab (AEON-1439).
watch(isLead, value => { if (!value && s.value && lead.value && ['lead', 'session'].includes(tab.value)) tab.value = 'overview' })
onBeforeUnmount(() => { opening++ })
useVisualViewport(root)
watch([owner, () => s.value?.id], async () => {
  if (props.layout !== undefined) return
  const write = ++localLayoutWrite, who = owner.value
  localLayout.value = 'side'
  const preference = window.innerWidth <= 720 || window.innerHeight <= 480 ? localSmallLayout : localLargeLayout
  await preference.ready
  if (write !== localLayoutWrite || who !== owner.value) return
  const saved = preference.value.value?.layout
  if (saved === 'side' || saved === 'wide' || saved === 'full') localLayout.value = saved
}, { immediate: true })
const ticketState = ref('')

const pending = computed(() => s.value?.run_id && s.value.phase !== 'stopped' && s.value.needs_attention !== false ? agents.pending.filter(a => a.agent_principal_id === s.value!.agent_principal_id && approvalRun(a) === s.value!.run_id) : [])
const recentRuns = computed(() => s.value ? agents.recentRuns(s.value.agent_principal_id).slice(0, 8) : [])
const run = computed(() => props.view?.run)
// The detail read lands in the ledger like every other copy of the session, so what is
// shown here is always the row the ledger holds: a detail that arrives late never
// replaces a newer list row, and the list never hides the detail's history (AEON-449).
const activity = computed(() => props.view ? activityOf(props.view) : null)
const reported = computed(() => s.value)
// A watched session has no Messages tab, so it always shows the overview with the live view.
const pane = computed(() => tab.value === 'session' ? 'overview' : tab.value)
const hasWork = computed(() => !!(reported.value?.brief || reported.value?.worktree || reported.value?.branch || reported.value?.commits?.length))
const timeline = computed(() => activity.value?.agent_activity_mode === 'off' ? [] : (activity.value?.activity_history ?? []).flatMap(item => {
  const note = cleanActivityNote(item.note)
  return note ? [{ ...item, note }] : []
}))
const currentTimeline = computed(() => activity.value?.agent_activity_mode === 'off' ? [] : activityDurations((activity.value?.current_activity_history ?? []).filter(item => activity.value?.agent_activity_mode !== 'tool_activity' || item.source === 'auto'), props.now, s.value?.stopped_at))
const tierState = computed(() => s.value ? serviceTiers.state(s.value) : undefined)
const runCost = computed(() => {
  const cost = tierState.value?.run_cost
  return cost && run.value && cost.run_id === run.value.id && cost.model === s.value?.model ? cost : undefined
})
const metadataHistory = computed(() => [
  ...metadataChanges(s.value?.metadata_history).map((item, i) => ({ id: `metadata-${i}`, at: item.at, text: metadataChangeText(item) })),
  ...(tierState.value?.history ?? []).map(item => ({ id: `tier-${item.id}`, at: item.at, text: tierHistoryText(item) })),
].sort((a, b) => Date.parse(b.at) - Date.parse(a.at)))
const step = computed(() => {
  if (!props.view) return ''
  return currentActivity(props.view, props.now) || currentStep(props.view)
})
const setupLine = computed(() => {
  const r = reported.value
  if (!props.view) return ''
  return [props.view.harness, r?.harness_version].filter(Boolean).join(' ')
})
const recentRunModel = (item: typeof recentRuns.value[number]) => panelModelLine({ harness: s.value?.harness ?? 'codex', model: runModel(item), reasoning_effort: '' }, agents.models)
const modelLine = computed(() => s.value ? panelModelLine(reported.value || s.value, agents.models, props.view?.model) : '')

const identityTitle = computed(() => [
  props.view?.ticket?.title || props.view?.name,
  !props.view?.ticket?.title && isLead.value ? props.view?.projectTitle : '',
  props.view?.status.label,
  native.value?.controller ? native.value.controller.actor_id === viewer.value.principalId ? 'You have control in a terminal' : 'Another person has control' : '',
  modelLine.value, s.value?.host,
].filter(Boolean).join(' · '))
const fullScreenTip = computed(() => panelLayout.value === 'full' ? 'Exit full screen · Esc' : ['Full screen', shortcutHint('agents.panel.fullscreen')].filter(Boolean).join(' · '))
const shortcutScope = { enabled: () => !!s.value && !props.loading && menuReady.value, when: (event: KeyboardEvent) => !isHolderTerminalTarget(event.target) && !isHolderTerminalTarget(document.activeElement) }
const stopPanelShortcuts = registerPanelShortcuts({ ...shortcutScope,
  toggle: () => { void requestLayout(panelLayout.value === 'full' ? 'side' : 'full') },
  select: index => {
    const next = availableTabs.value[index]
    if (!next) return false
    selectTab(next)
    void nextTick(() => root.value?.querySelector<HTMLElement>(`[role="tab"][aria-selected="true"]`)?.focus({ preventScroll: true }))
  },
})
onBeforeUnmount(() => { localLayoutWrite++; stopPanelShortcuts() })
function editSetting(kind: 'rename' | 'model' | 'effort') {
  const anchor = actionsAnchor.value
  closeActions(false)
  if (anchor && !nativeKnown.value) managed.value?.openSetting(kind, anchor)
}
function steer() { const anchor = actionsAnchor.value; closeActions(false); if (anchor && !nativeKnown.value) managed.value?.openSteer(anchor) }
function settingReason(kind: 'rename' | 'model' | 'effort' | 'steer') { return nativeKnown.value ? nativeBrowserReason : managed.value?.settingReason(kind) ?? 'This setting is not available for this session.' }
// A session that runs on its own takes rename and model requests (AEON-225). They
// open from ⋯ as a small form; Details lists them with their outcome (AEON-1438).
const requestAnchor = ref<HTMLElement | null>(null)
const requestKind = ref<'rename_request' | 'model_request'>('rename_request')
const requestList = ref<InstanceType<typeof SessionRequests>>()
const unmanagedRequests = computed(() => !!s.value && s.value.management_mode === 'unmanaged' && !s.value.watch && !nativeKnown.value && auth.identity?.principal.kind === 'person')
const requestsOffered = computed(() => unmanagedRequests.value && !!s.value && !s.value.stopped_at && !s.value.archived_at && s.value.phase !== 'stopped' && can('harness.control', s.value.project_id))
function openRequest(kind: 'rename_request' | 'model_request') {
  const anchor = actionsAnchor.value
  closeActions(false)
  requestKind.value = kind
  requestAnchor.value = anchor
}
function closeRequest(restore: boolean) {
  const anchor = requestAnchor.value
  requestAnchor.value = null
  if (restore && anchor?.isConnected) anchor.focus({ preventScroll: true })
}
function requestSent(control: SessionChangeRequest) {
  requestList.value?.add(control)
  toast(control.kind === 'rename_request' ? 'Rename requested' : 'Model change requested')
  closeRequest(true)
}
watch(() => s.value?.id, () => { requestAnchor.value = null })
const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)
const nonempty = (value: unknown): value is string => typeof value === 'string' && value.length > 0
const nonnegativeInteger = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0
const nullableText = (value: unknown): value is string | null => value === null || typeof value === 'string'
function isNativeBinding(value: unknown): value is NativeBinding {
  return record(value) && ['tenant_id', 'project_id', 'owned_session_id', 'harness_session_id', 'runtime_epoch', 'daemon_id', 'daemon_generation'].every(key => nonempty(value[key])) && nonnegativeInteger(value.binding_revision) && nullableText(value.run_id)
}
function isNativeContext(value: unknown): value is NativeSessionView {
  if (!record(value) || !nonempty(value.owned_session_id) || !nonempty(value.owner_person_id) || value.profile !== 'native' ||
    !nonnegativeInteger(value.generation) || !nonnegativeInteger(value.revision) ||
    !['coordinator', 'worker'].includes(String(value.role)) || !['claude', 'codex', 'cursor', 'grok', 'pi', 'gemini', 'opencode'].includes(String(value.harness)) ||
    !['accepted', 'waiting_for_exit', 'starting', 'idle', 'running', 'requires_action', 'quiescing', 'taken', 'returning', 'recovering', 'ownership_lost', 'stopping', 'stopped', 'failed'].includes(String(value.state)) ||
    !['structured', 'terminal', 'none'].includes(String(value.mode)) || typeof value.conversation_ref !== 'string' ||
    !nullableText(value.predecessor_harness_session_id) || !nullableText(value.reason) || !nullableText(value.terminal_viewer_url) ||
    (value.current_binding !== null && !isNativeBinding(value.current_binding))) return false
  const caps = value.capabilities, controller = value.controller
  return record(caps) && ['input', 'interrupt', 'stop', 'resume', 'take', 'expert', 'terminal_view', 'approvals', 'questions', 'correlated_receipts', 'take_supported', 'events_supported', 'interrupt_supported', 'chat_supported'].every(key => typeof caps[key] === 'boolean') &&
    ['native', 'queued', 'next_step', 'unsupported'].includes(String(caps.steer)) && ['stream', 'stream_and_qualified_idle', 'unsupported'].includes(String(caps.quota)) && nonempty(caps.qualification_digest) &&
    (controller === null || record(controller) && ['controller_id', 'actor_id', 'client_id', 'epoch', 'expires_at'].every(key => nonempty(controller[key])))
}
// Check the untrusted envelope before the existing event validator checks its
// kind-specific payload, privacy, binding, generation, sequence and byte bounds.
function isNativeEnvelope(value: unknown): value is NativeStreamEvent {
  return record(value) && isNativeBinding(value.binding) && nonnegativeInteger(value.generation) &&
    typeof value.seq === 'string' && typeof value.sequence === 'string' && typeof value.dropped_frames === 'string' &&
    typeof value.observed_at === 'string' && value.source === 'native' && typeof value.kind === 'string' &&
    Object.hasOwn(nativeSessionPayloadSchemas, value.kind) && record(value.payload)
}
function readNativeEvent(value: unknown, request: ExpertRequest): NativeStreamEvent {
  if (!isNativeEnvelope(value)) throw new Error('Invalid live event')
  const event = boundedEvent(value, request)
  if (!event) throw new Error('Invalid live event')
  return event
}
function readNativePage(value: unknown, request: ExpertRequest, before: string | null): NativeEventPage {
  if (!record(value) || !nonnegativeInteger(value.generation) || !Array.isArray(value.items) || value.items.length > nativeSessionBounds.event_page_items ||
    !nullableText(value.next_before) || typeof value.truncated !== 'boolean') throw new Error('Invalid event page')
  const page = { generation: value.generation, items: value.items.map(item => readNativeEvent(item, request)), next_before: value.next_before, truncated: value.truncated }
  if (!boundedPage(page, request, before)) throw new Error('Invalid event page')
  return page
}
async function boundedJSON(path: string, signal: AbortSignal, body?: unknown, headers?: Record<string, string>): Promise<unknown> {
  const response = await api(path, { signal, ...(body === undefined ? {} : { method: 'POST', body: JSON.stringify(body) }), headers: { 'Content-Type': 'application/json', ...headers } }, 3000)
  return readBoundedJSON(response)
}
async function readBoundedJSON(response: Response): Promise<unknown> {
  if (!response.ok) throw { status: response.status, reason: response.status === 503 ? 'feature_disabled' : [401, 403, 404].includes(response.status) ? 'permission_revoked' : response.status === 409 ? 'cursor_expired' : 'native_failure' }
  if (!response.body) throw new Error('Missing session response')
  const reader = response.body.getReader(), chunks: Uint8Array[] = []; let size = 0
  try { for (;;) { const part = await reader.read(); if (part.done) break; size += part.value.byteLength; if (size > nativeSessionBounds.event_page_bytes) throw new Error('Session response exceeds bounds'); chunks.push(part.value) } }
  finally { await reader.cancel().catch(() => {}); reader.releaseLock() }
  const bytes = new Uint8Array(size); let offset = 0
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength }
  return parseJson(new TextDecoder('utf-8', { fatal: true }).decode(bytes))
}
// Read-only structured transport. Expert/PTY exports stay closed until I2-core
// supplies an explicit server-approved owner consent. No control grant is requested.
const eventTransport: NativeExpertTransport = {
  async events(request, before, limit, signal) {
    if (request.view !== 'structured') throw { reason: 'qualification_missing' }
    const path = `/projects/${encodeURIComponent(request.binding.project_id)}/native-sessions/${encodeURIComponent(request.binding.owned_session_id)}/events`
    const query = new URLSearchParams({ generation: String(request.generation), limit: String(Math.min(limit, nativeSessionBounds.event_page_items)) })
    if (before) query.set('before', before)
    return readNativePage(await boundedJSON(`${path}?${query}`, AbortSignal.any([signal, AbortSignal.timeout(3000)])), request, before)
  },
  async stream(request, receive, signal) {
    if (request.view !== 'structured') throw { reason: 'qualification_missing' }
    const path = `/projects/${encodeURIComponent(request.binding.project_id)}/native-sessions/${encodeURIComponent(request.binding.owned_session_id)}`
    const query = new URLSearchParams({ view: 'structured', expected_runtime_epoch: request.binding.runtime_epoch,
      expected_binding_revision: String(request.binding.binding_revision), expected_harness_session_id: request.binding.harness_session_id })
    const attached = await boundedJSON(`${path}/attach`, AbortSignal.any([signal, AbortSignal.timeout(3000)]),
      { request_id: crypto.randomUUID(), expected_binding: request.binding, client_id: crypto.randomUUID(), mode: 'view', view: 'structured', consent_id: null })
    if (!record(attached) || attached.mode !== 'view' || attached.control_capability !== null || !nonempty(attached.viewer_id) || !isNativeBinding(attached.binding) ||
      bindingKey(attached.binding) !== bindingKey(request.binding) || !nonnegativeInteger(attached.expires_in_ms) || attached.expires_in_ms === 0) throw new Error('Invalid viewer grant')
    const lease = AbortSignal.any([signal, AbortSignal.timeout(Math.max(1, Math.min(attached.expires_in_ms, 30_000)))])
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined
    try {
      const connecting = new AbortController(), timeout = setTimeout(() => connecting.abort(), 3000)
      let response: Response
      try { response = await api(`${path}/stream?${query}`, { signal: AbortSignal.any([lease, connecting.signal]), headers: { Accept: 'text/event-stream', 'X-Aeon-Viewer-ID': attached.viewer_id, ...(request.lastEventId ? { 'Last-Event-ID': request.lastEventId } : {}) } }, 30_000) } finally { clearTimeout(timeout) }
      if (!response.ok || !response.body) throw { status: response.status, reason: response.status === 503 ? 'feature_disabled' : [401, 403, 404].includes(response.status) ? 'permission_revoked' : response.status === 409 ? 'cursor_expired' : 'native_failure' }
      reader = response.body.getReader()
      const decoder = new TextDecoder('utf-8', { fatal: true }); let pending = ''
      for (;;) {
        const part = await reader.read(); if (part.done || lease.aborted) break
        if (part.value.byteLength > nativeSessionBounds.browser_tail_bytes) throw new Error('Live frame exceeds bounds')
        pending = (pending + decoder.decode(part.value, { stream: true })).replace(/\r\n/g, '\n')
        let end: number
        while ((end = pending.indexOf('\n\n')) >= 0) {
          const frame = pending.slice(0, end); pending = pending.slice(end + 2)
          if (frame.length > nativeSessionBounds.exported_frame_bytes) throw new Error('Live frame exceeds bounds')
          const data = frame.split('\n').filter(line => line.startsWith('data:')).map(line => line.slice(5).trimStart()).join('\n')
          if (data && !signal.aborted) receive(readNativeEvent(parseJson(data), request))
        }
        if (pending.length > nativeSessionBounds.exported_frame_bytes) throw new Error('Live frame exceeds bounds')
      }
    } finally {
      await reader?.cancel().catch(() => {}); reader?.releaseLock()
      await boundedJSON(`${path}/detach`, AbortSignal.timeout(3000), { request_id: crypto.randomUUID(), expected_binding: attached.binding, viewer_id: attached.viewer_id }).catch(() => {})
    }
  },
}

// A managed run without an inbox cannot receive follow-up messages (AEON-282). The rows no longer
// say it; this tab does, in words, where it says whether the session listens (AEON-1211).
const noInbox = computed(() => !!props.view && props.view.session.management_mode === 'managed' && !!props.view.session.run_id && !props.view.session.advertised_capabilities.includes('inbox'))
// Fetch only the selected session, and refresh when a heartbeat changes metadata
// even if the activity note stays the same. The list does not carry history.
watch([
  () => s.value?.id,
  () => s.value?.project_id,
  () => s.value?.heartbeat_at,
  () => s.value?.display_label,
  () => s.value?.model,
  () => s.value?.reasoning_effort,
  () => props.view ? activityOf(props.view).activity_note : '',
], async (_current, _previous, onCleanup) => {
  const current = s.value
  if (!current) return
  const controller = new AbortController()
  onCleanup(() => controller.abort())
  try {
    await agents.loadSessionDetail(current.project_id, current.id, controller.signal)
  } catch { /* The list still shows the latest step if detail is unavailable. */ }
}, { immediate: true })
watch(() => props.view?.ticket?.id, async id => {
  ticketState.value = ''
  if (!id) return
  try { const node = await getNode(id); if (props.view?.ticket?.id === id) ticketState.value = statusMeta(node.state).label } catch { /* Ticket summary remains useful. */ }
}, { immediate: true })
watch(() => s.value?.id, async id => {
  if (!id || !s.value) return
  thread.value?.scrollTo({ top: 0 })
  await agents.refreshAgentRuns(s.value.agent_principal_id)
}, { immediate: true })
// Per-person state resets for another person only; an identity re-read that
// briefly has no identity keeps the same person's tab (AEON-1439).
let shownTo = ''
watch([() => s.value?.id, () => auth.identity?.tenant.id, () => auth.identity?.principal.id, () => props.sheet], async () => {
  actionsAnchor.value = null; composing.value = false
  const person = auth.identity ? `${auth.identity.tenant.id}/${auth.identity.principal.id}` : ''
  if (person && shownTo && person !== shownTo) { tab.value = 'overview'; unread.value = 0 }
  if (person) shownTo = person
  const id = s.value?.id, owner = `${auth.identity?.tenant.id}/${auth.identity?.principal.id}`
  await nextTick()
  if (id !== s.value?.id || owner !== `${auth.identity?.tenant.id}/${auth.identity?.principal.id}`) return
  for (const body of root.value?.querySelectorAll<HTMLElement>('.scroll') ?? []) body.scrollTop = 0
}, { flush: 'post', immediate: true })
onMounted(() => { root.value?.focus({ preventScroll: true }) })

// AEON-291: offer Interrupt and Stop only where they work right now; a session
// outside the product gets one quiet line instead of disabled buttons.
const quick = computed(() => !!props.view && quickRemoval(props.view))
function pickTier(anchor: HTMLElement | null) {
  const view = props.view
  if (view && anchor) serviceTiers.open(view.session, view.name, anchor)
}
function control(kind: SessionControl['kind']) { if (props.view && !props.controlBlock(props.view, kind)) emit('control', props.view, kind) }
const controlSession = computed(() => reported.value || s.value)
const resumeOffered = computed(() => isLead.value ? lead.value?.state === 'paused' : !!controlSession.value && pausedSession(controlSession.value))
const managedAccess = () => ({ now: props.now, grant: { person: auth.identity?.principal.kind === 'person', can }, runStatus: controlSession.value?.run_status ?? props.view?.run?.status })
const interruptReason = computed(() => {
  const row = controlSession.value
  if (nativeKnown.value) return nativeBrowserReason
  if (managed.value?.waiting) return 'A control is pending.'
  if (!row || !props.view) return 'No session is selected.'
  if (pausingSession(row)) return 'This session is handing over.'
  if (managedControls.value) return managedControlAllowed(row, 'interrupt', managedAccess()) ? '' : 'This session cannot be interrupted now.'
  const reason = props.controlBlock(props.view, 'interrupt')
  if (reason) return reason
  return tab.value === 'chat' && !chat.value?.working ? 'This session has no active turn to interrupt.' : ''
})
const pauseReason = computed(() => {
  if (nativeKnown.value) return "Pause and resume aren't here yet"
  if (isLead.value) return leadActionReason(lead.value, resumeOffered.value ? 'resume' : 'pause')
  const row = controlSession.value
  return row && pause.eligible(row, resumeOffered.value ? 'resume' : 'pause') ? '' : 'This session cannot pause or resume now.'
})
const stopReason = computed(() => {
  const row = controlSession.value
  if (nativeKnown.value) return nativeBrowserReason
  if (managed.value?.waiting) return 'A control is pending.'
  if (!row) return 'No session is selected.'
  if (managedControls.value) return managedControlAllowed(row, 'stop', managedAccess()) ? '' : 'This session cannot be stopped now.'
  return pause.eligible(row, 'stop') ? '' : 'This session cannot be stopped now.'
})
function interrupt() { if (!interruptReason.value) { chat.value?.interrupted(); if (managedControls.value) void managed.value?.interrupt(); else control('interrupt') } }
async function pauseOrResume(anchor: HTMLElement) {
  const row = controlSession.value
  if (!row || pauseReason.value) return
  if (!isLead.value) { pause.open(resumeOffered.value ? 'resume' : 'pause', [row], anchor); return }
  if (!resumeOffered.value) { openLeadPause(row.project_id, anchor); return }
  const id = row.id, who = owner.value, revision = lead.value?.revision
  if (!leadActionAvailable(lead.value, 'resume')) return
  try {
    const next = await leads.start(row.project_id, { revision: lead.value!.revision, generation: lead.value!.generation, session_id: row.id }, 'resume')
    if (next && id === s.value?.id && who === owner.value && revision !== undefined) toast('Lead restart requested.')
  } catch (e) { if (id === s.value?.id && who === owner.value) toast(e instanceof Error ? e.message : 'The lead did not restart.', { tone: 'error' }) }
}
async function stopSession(anchor: HTMLElement) {
  const row = controlSession.value
  if (!row || stopReason.value) return
  if (!managedControls.value) { pause.open('stop', [row], anchor); return }
  const id = row.id, who = owner.value, ownership = JSON.stringify(row.process_ownership)
  const confirmed = await confirmAction({ title: `Stop ${props.view?.name}?`, body: 'Ends this managed session. It cannot be resumed; the next session starts fresh.', danger: true, confirmLabel: 'Stop session' })
  if (confirmed && id === s.value?.id && who === owner.value && ownership === JSON.stringify(controlSession.value?.process_ownership) && !stopReason.value) void managed.value?.stop()
}
function panelKey(event: KeyboardEvent) {
  if (isHolderTerminalTarget(event.target) && !event.metaKey && !event.ctrlKey && (event.key === 'Escape' || /^[1-5f]$/i.test(event.key) || event.key.toLowerCase() === 'e' && event.shiftKey)) { event.stopImmediatePropagation(); return }
  if (props.layout === undefined && event.key === 'Escape' && panelLayout.value === 'full' && !shortcutTyping(event.target)) { event.preventDefault(); event.stopImmediatePropagation(); void requestLayout('side'); return }
  if (panelLayout.value !== 'full' || event.key !== 'Tab' || !root.value) return
  const controls = [...root.value.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], input, textarea, select, [tabindex="0"]')].filter(el => el.getClientRects().length && !el.closest('[inert]'))
  if (!controls.length) return
  const edge = event.shiftKey ? controls[0] : controls.at(-1)
  if (event.target === edge || !controls.includes(document.activeElement as HTMLElement)) { event.preventDefault(); (event.shiftKey ? controls.at(-1) : controls[0])?.focus({ preventScroll: true }) }
}
defineExpose({ focus: () => root.value?.focus({ preventScroll: true }) })
</script>

<template>
  <aside ref="root" class="session-panel" :class="{ composing, docked, sheet, full: panelLayout === 'full', wide: panelLayout === 'wide' }" :data-layout="panelLayout" aria-label="Session details" tabindex="-1" @keydown="panelKey" @focusin="chatFocus" @focusout="chatBlur">
    <!-- The identity stays in view while runs and messages scroll below it. -->
    <header class="panel-head">
      <div class="pane-bar">
        <div v-if="view && !loading" class="pane-identity">
          <AgentGlyph :view="view" :size="28" />
          <h2 class="pane-title" tabindex="0" :title="identityTitle" :data-tip="identityTitle">
            <span class="name">{{ view.ticket?.key || view.name }}</span><template v-if="view.ticket?.title || isLead"><span class="title-separator"> · </span><span class="title-detail">{{ view.ticket?.title || view.projectTitle }}</span></template>
            <span class="sr-only"> · {{ view.status.label }} · {{ modelLine }}</span>
          </h2>
        </div>
        <div v-else class="pane-identity" role="status">{{ loading ? 'Reading session…' : 'Session unavailable' }}</div>
        <SessionTabs v-if="view && !loading && menuReady" :selected="tab" :lead="leadAtOpen" :terminal="terminalAtOpen" :watch="!!reported?.watch" :pinned="pinned" :unread="tab === 'chat' ? 0 : Math.max(unread, hasQuestion ? questions.length : 0)" @select="selectTab" />
        <div class="pane-buttons" role="group" aria-label="Panel actions">
          <button v-if="sidebarAvailable" type="button" class="pane-icon pane-layout" aria-label="Sessions list" :aria-pressed="panelLayout === 'side'" :data-tip="panelLayout === 'side' ? 'Hide the sessions list' : 'Show the sessions list'" @click="requestLayout(panelLayout === 'side' ? 'wide' : 'side')"><AppIcon :name="panelLayout === 'side' ? 'layout-focus' : 'layout-list'" :size="18" /></button>
          <button type="button" class="pane-icon pane-layout" aria-label="Full screen" :aria-pressed="panelLayout === 'full'" :data-tip="fullScreenTip" :aria-keyshortcuts="shortcutKeys('agents.panel.fullscreen').join(' ') || undefined" @click="requestLayout(panelLayout === 'full' ? 'side' : 'full')"><AppIcon :name="panelLayout === 'full' ? 'collapse' : 'expand'" :size="18" /></button>
          <button v-if="view && !loading && menuReady" type="button" class="pane-icon pane-more" aria-label="More session actions" aria-haspopup="menu" :aria-expanded="!!actionsAnchor" @click="actionsAnchor = actionsAnchor ? null : $event.currentTarget as HTMLElement"><AppIcon name="more" :size="18" /></button>
          <button type="button" class="pane-icon pane-close" aria-label="Close session details" aria-keyshortcuts="Escape" data-tip="Close · Esc" @click="emit('close')"><AppIcon name="close" :size="18" /></button>
        </div>
      </div>
      <!-- The menu's lead state at opening, so a refresh that briefly drops the session never rewrites the line (AEON-1439). -->
      <p v-if="leadAtOpen && why" class="sr-only">Opened on {{ PANEL_TAB_LABEL[openedTab] }}: {{ why.replace('Session is', 'Details is') }}</p>
      <ManagedSessionControls v-if="view && !loading && !reported?.watch && !nativeKnown" ref="managed" overflow :session="reported || view.session" :now="now" :run-status="view.run?.status" />
      <p v-if="managed?.feedback || managed?.error" class="session-feedback" :role="managed?.error ? 'alert' : 'status'">{{ managed.error || managed.feedback }}</p>
      <FloatingPanel v-if="actionsAnchor && view && !loading" :anchor="actionsAnchor" align="end" label="More session actions" :width="nativeKnown ? 440 : 360" @close="closeActions">
        <div class="panel-menu" :class="{ 'phone-full': panelLayout === 'full', 'native-menu': nativeKnown }" role="menu" aria-label="More session actions">
          <button type="button" role="menuitem" class="panel-menu-item" @click="requestLayout(panelLayout === 'full' ? 'side' : 'full'); closeActions(true)"><AppIcon :name="panelLayout === 'full' ? 'collapse' : 'expand'" :size="16" /><span>{{ panelLayout === 'full' ? 'Exit full screen' : 'Full screen' }}</span><KeyCap k="shift" /><KeyCap k="E" /></button>
          <button v-if="sidebarAvailable" type="button" role="menuitem" class="panel-menu-item" @click="requestLayout(panelLayout === 'side' ? 'wide' : 'side'); closeActions(true)"><AppIcon :name="panelLayout === 'side' ? 'layout-focus' : 'layout-list'" :size="16" /><span>{{ panelLayout === 'side' ? 'Hide the sessions list' : 'Show the sessions list' }}</span></button>
          <div class="phone-panel-choices" role="group" aria-label="Session views">
            <button v-for="choice in availableTabs" :key="choice" type="button" role="menuitemradio" :aria-checked="choice === tab" class="panel-menu-item" @click="selectTab(choice); closeActions(true)"><span>{{ PANEL_TAB_LABEL[choice] }}</span><AppIcon v-if="choice === tab" name="check" :size="14" /></button>
          </div>
          <template v-if="!reported?.watch">
            <button type="button" role="menuitem" class="panel-menu-item" :disabled="!!interruptReason" @click="interrupt(); closeActions(true)"><AppIcon name="interrupt" :size="16" /><span>Interrupt<small>{{ interruptReason || 'Ends its current step' }}</small></span></button>
            <button type="button" role="menuitem" class="panel-menu-item" :disabled="!!pauseReason || !!(s && leads.busy[s.project_id])" @click="pauseOrResume(actionsAnchor!); closeActions(false)"><AppIcon :name="resumeOffered ? 'play' : 'pause'" :size="16" /><span>{{ resumeOffered ? 'Resume' : isLead ? 'Pause…' : 'Pause' }}<small>{{ pauseReason || (resumeOffered ? 'Through the current start checks' : 'Workers finish their step') }}</small></span></button>
            <button type="button" role="menuitem" class="panel-menu-item danger-ghost" :disabled="!!stopReason" @click="stopSession(actionsAnchor!); closeActions(false)"><AppIcon name="stop" :size="16" /><span>Stop…<small>{{ stopReason || 'Opens the confirmation' }}</small></span></button>
            <button v-if="managedControls" type="button" role="menuitem" class="panel-menu-item" :disabled="!!settingReason('steer')" @click="steer"><AppIcon name="send" :size="16" /><span>Steer…<small>{{ settingReason('steer') || 'Change the next step' }}</small></span></button>
          </template>
          <button v-if="terminalAtOpen" type="button" role="menuitem" class="panel-menu-item" disabled><AppIcon name="terminal" :size="16" /><span>{{ nativeAction?.label || 'Control unavailable' }}<small>{{ nativeAction?.reason || nativeReason }}</small></span></button>
          <a v-if="viewerAtOpen && viewer.kind === 'person'" role="menuitem" class="panel-menu-item" :href="terminalLink?.href" :aria-disabled="!terminalLink" :tabindex="terminalLink ? 0 : -1" target="_blank" rel="noopener noreferrer" @click="terminalLink ? closeActions(false) : $event.preventDefault()"><AppIcon name="terminal" :size="16" /><span>Open live terminal</span><AppIcon name="external" :size="14" /></a>
          <button v-if="showRecover" type="button" role="menuitem" class="panel-menu-item" :disabled="nativeKnown" :title="nativeKnown ? nativeBrowserReason : undefined" @click="recovery?.open(actionsAnchor ?? undefined); closeActions(false)"><AppIcon name="wrench" :size="16" /><span>Recover<small v-if="nativeKnown">{{ nativeBrowserReason }}</small></span></button>
          <button v-if="showRemove" type="button" role="menuitem" class="panel-menu-item" :disabled="nativeKnown" :aria-label="`Remove ${view.name}`" @click="removal?.remove(); closeActions(false)"><AppIcon name="trash" :size="16" /><span>{{ quick ? 'Remove' : 'Remove…' }}<small v-if="nativeKnown">{{ nativeBrowserReason }}</small></span></button>
          <button v-if="isLead" type="button" role="menuitemcheckbox" :aria-checked="pinned === 'lead'" class="panel-menu-item" @click="pinTab(); closeActions(true)"><AppIcon name="pin" :size="16" /><span>Open on Lead by default</span><AppIcon v-if="pinned === 'lead'" name="check" :size="14" /></button>
          <button v-if="!reported?.watch && !serviceTiers.unavailable(view.session)" type="button" role="menuitem" class="panel-menu-item" :disabled="nativeKnown" @click="pickTier(actionsAnchor); closeActions(false)"><AppIcon name="gauge" :size="16" /><span>Change tier…<small v-if="nativeKnown">{{ nativeBrowserReason }}</small></span></button>
          <template v-if="requestsOffered">
            <button type="button" role="menuitem" class="panel-menu-item" @click="openRequest('rename_request')"><AppIcon name="edit" :size="16" /><span>Rename session…<small>{{ view.name }} applies it at its next step</small></span></button>
            <button type="button" role="menuitem" class="panel-menu-item" @click="openRequest('model_request')"><AppIcon name="sliders" :size="16" /><span>Change model or effort…<small>Now {{ modelLine || 'unknown' }}</small></span></button>
          </template>
          <template v-if="managedControls">
            <button v-for="setting in (['rename', 'model', 'effort'] as const)" :key="setting" type="button" role="menuitem" class="panel-menu-item" :disabled="!!settingReason(setting)" @click="editSetting(setting)"><AppIcon name="edit" :size="16" /><span>{{ setting === 'rename' ? 'Name…' : setting === 'model' ? 'Model…' : 'Effort…' }}<small>{{ settingReason(setting) || (setting === 'rename' ? view.name : setting === 'model' ? modelLine : view.session.reasoning_effort) }}</small></span></button>
          </template>
          <button v-if="!reported?.watch && !nativeKnown && agentRecovery.action(view.session)" type="button" role="menuitem" class="panel-menu-item" :disabled="agentRecovery.busy[view.session.id]" @click="agentRecovery.request(view.session, view.name); closeActions(false)"><AppIcon name="refresh" :size="16" /><span>{{ agentRecovery.action(view.session) === 'restart' ? 'Restart' : 'Reconnect' }}</span></button>
        </div>
      </FloatingPanel>
      <FloatingPanel v-if="requestAnchor && view && !loading" :anchor="requestAnchor" align="end" :width="344" :label="requestKind === 'rename_request' ? 'Rename session' : 'Change model or effort'" cycle field-escape @close="closeRequest">
        <SessionRequests :key="`${view.session.id}:${requestKind}`" mode="form" :kind="requestKind" :session="view.session" :name="view.name" :now="now" @sent="requestSent" @cancel="closeRequest(true)" />
      </FloatingPanel>
    </header>
    <template v-if="view && !loading">
      <SessionRecovery ref="recovery" hide-trigger :session="view.session" />
      <RemoveSessionDialog ref="removal" hide-trigger :session="view.session" :label="view.name" :quick="quick" />
    </template>

    <!-- Until the first load completes the body stays a placeholder, so runs and
         messages arrive together instead of pushing each other down. -->
    <div v-if="loading" class="scroll" role="status" aria-label="Loading session">
      <div class="sk"><span class="skeleton w40" /><span class="skeleton w70" /><span class="skeleton w90" /><span class="skeleton w60" /></div>
    </div>
    <div v-else-if="!view" class="scroll">
      <div class="gone">
        <span class="gone-icon"><AppIcon name="agent" :size="20" /></span>
        <h2>This session is not here</h2>
        <p>It may belong to a project you cannot see, or the server no longer lists it.</p>
        <button type="button" class="btn" @click="emit('close')">Back to agents</button>
      </div>
    </div>

    <LeadContent v-if="view && !loading && isLead && project" v-show="tab === 'lead'" :key="`${owner}:${view.session.id}`" :project-id="view.session.project_id" :project-key="project.routeKey" :route-key="project.routeKey" :session-started="view.session.created_at" :section="tab === 'lead' && (route.hash === '#next-up' || section === 'next-up') ? 'next-up' : undefined" role="tabpanel" id="session-panel-lead" aria-labelledby="session-tab-lead" @chat="showQuestion" />
    <div v-else-if="!loading && !view" />
    <div v-if="view && !loading" v-show="pane === 'overview'" id="session-panel-overview" ref="thread" class="scroll" role="tabpanel" aria-labelledby="session-tab-overview">
      <div v-if="pending.length" class="callout" role="note">
        <AppIcon name="shield" :size="15" />
        <div class="callout-text">
          <strong>{{ pending.length === 1 ? 'Waiting for your permission' : `${pending.length} requests wait for you` }}</strong>
          <span>{{ scopeLabel(pending[0].scope) }}</span>
        </div>
        <button type="button" class="btn sm primary" @click="emit('review', pending[0])">Review</button>
      </div>

      <LiveWatch v-if="reported?.watch" :session="reported" />

      <section class="now-block" aria-labelledby="now-title">
        <h3 id="now-title" class="sr-only">Now</h3>
        <PauseEvidence :session="reported || view.session" :now="now" />
        <p v-if="view.session.archived_at" class="now-meta">Archived registration · process state unknown. No process was stopped by recovery.</p>
        <strong class="now-step">{{ step }}</strong>
        <p class="now-meta">
          <span :data-tip="`Since ${absoluteTime(view.session.created_at)}`">{{ view.session.stopped_at ? 'Ran' : 'Running' }} {{ elapsed(view.session, now) }}</span>
          <template v-if="view.session.stopped_at"> · {{ view.status.state === 'done' ? 'Done' : stopReasonLabel(view.session.stop_reason) || 'Ended' }} {{ relativeTime(view.session.stopped_at, { now }) }}</template>
          <template v-else-if="view.session.heartbeat_at"> · heartbeat <time :datetime="view.session.heartbeat_at" :data-tip="absoluteTime(view.session.heartbeat_at)">{{ relativeTime(view.session.heartbeat_at, { now }) }}</time></template>
          <template v-else> · no heartbeat yet</template>
        </p>
        <p v-if="view.session.phase !== 'stopped' && !view.session.stopped_at && !view.session.archived_at" class="now-meta now-listen">
          <span v-if="noInbox" class="no-inbox" data-tip="Launch a managed worker to receive follow-up messages.">No inbox · this run cannot receive follow-up messages</span>
          <ListeningLabel v-else :session="view.session" :now="now" />
        </p>
        <p v-if="sessionEtaEligible(view) && (etaFromSession(view.session) || view.session.phase === 'working')" class="now-meta now-eta"><SessionEstimate labelled :eta="etaFromSession(view.session)" :now="now" :missing="view.session.phase === 'working'" /></p>
        <SessionStateEvidence :view="view" :now="now" />
        <ol v-if="currentTimeline.length" class="activity-timeline" aria-label="Current activity history">
          <li v-for="(item, index) in currentTimeline.slice(0, 6)" :key="`${item.at}-${index}`"><time :datetime="item.at" :title="absoluteTime(item.at)">{{ item.duration }}</time><span>{{ item.text }}</span></li>
        </ol>
        <ol v-else-if="timeline.length" class="activity-timeline" aria-label="Recent activity">
          <li v-for="(item, index) in timeline.slice(0, 6)" :key="`${item.at}-${index}`"><time :datetime="item.at">{{ relativeTime(item.at, { now }) }}</time><span>{{ item.note }}</span></li>
        </ol>
        <DeliveryRating v-if="s && s.phase === 'stopped'" :session-id="s.id" />
      </section>

      <ServiceTierBlock :key="view.session.id" :session="view.session" :name="view.name" />
      <section v-if="!reported?.watch" class="recovery-evidence" aria-label="Session control evidence">
        <p class="now-meta" :title="capText[1]">{{ capText[0] }}</p>
        <p v-if="view.session.agent_recovery" class="outside-note">{{ view.session.agent_recovery.detail }}</p>
      </section>
      <SessionRequests v-if="unmanagedRequests" ref="requestList" :key="view.session.id" mode="list" :session="view.session" :now="now" />

      <section class="block first" aria-labelledby="setup-title">
        <h3 id="setup-title" class="eyebrow">Facts</h3>
        <dl class="facts">
          <div class="fact"><dt>Project</dt><dd>
            <RouterLink v-if="view.projectKey" class="project-link" :to="`/p/${encodeURIComponent(view.projectKey)}`">{{ view.projectTitle || view.projectKey }}</RouterLink>
            <span v-else class="muted">Workspace</span>
          </dd></div>
          <div class="fact"><dt>Runs on</dt><dd><SessionHost :key="`${auth.identity?.principal.id}:${view.session.host}`" class="host-fact" :host="view.session.host" :label="hostLabel" :editable="hostPerson" @renamed="hostLabel = $event" /><span class="muted">{{ isLead ? 'lead session' : view.session.role === 'coordinator' ? 'coordinator' : 'worker' }} · {{ view.session.management_mode === 'managed' ? `owned by ${brand.short_name}` : 'runs on its own' }}</span></dd></div>
          <div v-if="modelLine" class="fact"><dt>Model</dt><dd class="mono">{{ modelLine }}<span v-if="run && run.model_evidence === 'vendor_reported'" class="evidence" data-tip="Reported by the vendor, not only requested"><AppIcon name="check" :size="11" /></span></dd></div>
          <div v-if="reported?.account_label || view.account" class="fact"><dt>Account</dt><dd>{{ reported?.account_label || view.account }}</dd></div>
          <div class="fact"><dt>Harness</dt><dd>{{ setupLine }}</dd></div>
          <div v-if="view.ticket" class="fact wide"><dt>Ticket</dt><dd class="ticket-dd"><TicketPeekLink class="ticket-fact" :ticket-key="view.ticket.key" :href="view.ticket.href" :tip="view.ticket.title"><span class="key">{{ view.ticket.key }}</span> <span class="ticket-title">{{ view.ticket.title }}</span></TicketPeekLink><span v-if="ticketState" class="faint ticket-state">&nbsp;· {{ ticketState }}</span></dd></div>
        </dl>
        <ol v-if="metadataHistory.length" class="metadata-history" aria-label="Recent session changes">
          <li v-for="item in metadataHistory" :key="item.id"><time :datetime="item.at">{{ relativeTime(item.at, { now }) }}</time><span>{{ item.text }}</span></li>
        </ol>
        <p v-if="tierState?.history_truncated" class="muted">Showing the latest 50 tier events.</p>
      </section>

      <section v-if="hasWork" class="block" aria-labelledby="work-title">
        <h3 id="work-title" class="eyebrow">Work</h3>
        <dl class="facts">
          <div v-if="reported?.brief" class="fact wide"><dt>Brief</dt><dd>{{ reported.brief }}</dd></div>
          <div v-if="reported?.branch" class="fact"><dt>Branch</dt><dd class="mono">{{ reported.branch }}</dd></div>
          <div v-if="reported?.worktree" class="fact wide"><dt>Worktree</dt><dd class="mono">{{ reported.worktree }}</dd></div>
          <div v-if="reported?.commits?.length" class="fact wide"><dt>Commits</dt><dd><ol class="commits"><li v-for="commit in reported.commits" :key="commit.sha"><code>{{ commit.sha }}</code><span>{{ commit.subject }}</span></li></ol></dd></div>
        </dl>
      </section>

      <section v-if="run" class="block" aria-labelledby="telemetry-title">
        <h3 id="telemetry-title" class="eyebrow">Current run</h3>
        <p v-if="run.wait" class="outside-note">{{ capacityWaitText(run.wait, 'Agents', now) }}</p>
        <div class="telemetry">
          <div class="metric"><span class="metric-label">Status</span><span class="run-chip" :class="run.wait?.code === 'vendor' ? '' : RUN_OUTCOME[run.status].tone">{{ run.wait?.code === 'vendor' ? 'Throttled' : RUN_OUTCOME[run.status].label }}</span></div>
          <div class="metric"><span class="metric-label">Tokens in</span><b>{{ tokens(run.input_tokens) }}</b></div>
          <div class="metric"><span class="metric-label">Tokens out</span><b>{{ tokens(run.output_tokens) }}</b></div>
          <div class="metric"><span class="metric-label">Cost</span><b>{{ runCost ? tierCostAmount(runCost) : cost(run.cost_micros) }}</b><small v-if="runCost" class="tier-cost">{{ tierRunCostLabel(runCost) }}</small><small v-if="runCost">{{ runCost.provisional ? 'Provisional token estimate' : 'Token estimate' }} · not billed cost</small><small v-if="runCost && run.cost_micros > 0">Reported cost {{ cost(run.cost_micros) }}</small></div>
        </div>
      </section>

      <section v-if="view.session.management_mode === 'managed' && recentRuns.length" class="block" aria-labelledby="runs-title">
        <h3 id="runs-title" class="eyebrow">Recent runs</h3>
        <div class="runs" role="table" aria-label="Recent runs">
          <div class="run-row run-head" role="row">
            <span role="columnheader">Outcome</span><span role="columnheader">Model</span><span role="columnheader" class="run-tokens">Tokens</span>
            <span role="columnheader" class="run-duration">Took</span><span role="columnheader" class="run-when">Started</span>
          </div>
          <div v-for="item in recentRuns" :key="item.id" class="run-row" role="row">
            <span role="cell"><span class="run-chip" :class="RUN_OUTCOME[item.status].tone">{{ RUN_OUTCOME[item.status].label }}</span></span>
            <span role="cell" class="run-model mono" :data-tip="recentRunModel(item) || undefined">{{ recentRunModel(item) || 'Agent run' }}</span>
            <span role="cell" class="run-tokens mono" :data-tip="`${item.input_tokens.toLocaleString()} in · ${item.output_tokens.toLocaleString()} out`">{{ tokens(item.input_tokens + item.output_tokens) }}</span>
            <span role="cell" class="run-duration mono">{{ runDuration(item, now) || '—' }}</span>
            <time role="cell" class="run-when" :datetime="item.created_at" :data-tip="absoluteTime(item.started_at ?? item.created_at)">{{ relativeTime(item.started_at ?? item.created_at, { now }) }}</time>
          </div>
        </div>
      </section>

      <ProvenanceDetail v-if="s" :project-id="s.project_id" :session-id="s.id" :now="now" />
    </div>
    <SessionChat v-if="view && !loading && !reported?.watch && !nativeKnown" ref="chat" v-show="tab === 'chat'" id="session-panel-messages" :view="view" :now="now" :can-write="canWrite" :active="tab === 'chat'"
      :questions="questions" :question-readable="questionReadable" :question-focus="questionFocus" :interrupt-block="interruptReason" role="tabpanel" aria-labelledby="session-tab-messages" @unread="unread = $event" @interrupt="interrupt" />
    <div v-if="view && !loading" v-show="tab === 'events'" id="session-panel-events" class="live-pane" role="tabpanel" aria-labelledby="session-tab-events">
      <SessionEvents v-if="native" :key="`${owner}:${native.owned_session_id}`" :session="native" :viewer="viewer" :consent="null" :transport="eventTransport" :active="tab === 'events'" :name="view.name" :host="view.session.host || ''" />
      <p v-else class="live-unavailable" role="status">{{ nativeReason }}</p>
    </div>
    <div v-if="view && !loading && terminalAtOpen" v-show="tab === 'terminal'" id="session-panel-terminal" class="live-pane" role="tabpanel" aria-labelledby="session-tab-terminal">
      <SessionTerminal v-if="native && native.mode === 'structured' && viewerAtOpen" :session="native" :viewer="viewer" :consent="null" :transport="eventTransport" :active="tab === 'terminal'" :name="view.name" :host="view.session.host || ''" />
      <NativeTerminal v-else-if="native" :session="native" :viewer="viewer" :consent="null" :transport="{ stream: async () => { throw { reason: 'qualification_missing' } } }" :renderer-factory="null" :active="tab === 'terminal'" :model-name="modelLine" :host="view.session.host || ''" :layout="panelLayout" :action="nativeAction" />
      <p v-else class="live-unavailable" role="status">The live terminal is no longer available.</p>
    </div>
    <div v-if="nativeKnown && view && !loading" v-show="tab === 'chat'" id="session-panel-messages" class="live-pane" role="tabpanel" aria-labelledby="session-tab-messages"><p class="live-unavailable">Owned session input from this panel is not available yet. Use the qualified session client.</p></div>
  </aside>
</template>

<style scoped>
.session-panel {
  position: fixed; z-index: 15; top: calc(var(--header-h) + 10px); right: 10px; bottom: calc(var(--footer-h) + 10px); width: min(560px, calc(100vw - 20px));
  display: flex; flex-direction: column; min-height: 0; outline: none;
  border-radius: var(--radius); border: 1px solid var(--glass-edge);
  background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow);
  -webkit-backdrop-filter: blur(20px) saturate(1.15); backdrop-filter: blur(20px) saturate(1.15);
}
.session-panel:focus-visible { box-shadow: var(--shadow-pop), var(--focus-ring); }
@media (min-width: 1100px) { .session-panel { width: var(--panel-w); } }
.session-panel.docked { position: sticky; inset: auto; top: 0; flex: 1 1 0; min-width: 0; width: auto; height: var(--pane-h); overflow: hidden; animation: none; box-shadow: var(--shadow); }
.session-panel.docked .pane-bar { padding-right: 48px; }
.session-panel.sheet { z-index: 40; inset: calc(var(--header-h) + 8px) 16px calc(var(--footer-h) + 8px); width: auto; }
@media (prefers-reduced-motion: no-preference) {
  .session-panel { animation: panel-in .22s cubic-bezier(.2, .7, .2, 1); }
  @keyframes panel-in { from { opacity: 0; transform: translateX(24px); } to { opacity: 1; transform: none; } }
}
.panel-head { flex-shrink: 0; padding: 8px 10px 10px 18px; border-bottom: 1px solid var(--line); }
.head-top { display: flex; align-items: flex-start; gap: 8px; min-height: 36px; }
/* Actions that no longer fit wrap to a right-aligned second line; none shrinks under its label (AEON-1062). */
.head-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 4px; min-width: 0; margin-top: 6px; }
.head-actions .btn { display: inline-flex; align-items: center; justify-content: center; gap: 5px; white-space: nowrap; }
.outside-note { margin: 2px 0 0; font-size: 12px; line-height: 1.4; color: var(--ink-3); }
.host-meta { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); font-size: 12px; }
.name { min-width: 0; white-space: normal; overflow-wrap: anywhere; font-size: 18px; font-weight: 650; letter-spacing: -.01em; }
.state-text { flex-shrink: 0; font-size: 12.5px; font-weight: 600; color: var(--ink-2); }
.state-text.needs { color: var(--warn-ink); }
.spacer { flex: 1; }
.bar-sep { width: 1px; height: 18px; margin: 0 4px; background: var(--line-2); }
.panel-head [aria-disabled="true"] { opacity: .35; cursor: not-allowed; }
.stop:not([aria-disabled="true"]):hover { color: var(--danger); }
.scroll { flex: 1; min-height: 0; overflow: hidden auto; overscroll-behavior: contain; padding: 18px 24px 24px; }
.now-block { display: grid; gap: 6px; padding: 0 0 18px; border-bottom: 1px solid var(--line); }
.now-step { font-size: 19px; line-height: 1.3; color: var(--ink); overflow-wrap: anywhere; }
.now-meta { font-size: 12px; color: var(--ink-2); }
.now-listen { display: flex; min-width: 0; }
.no-inbox { min-width: 0; color: var(--ink-2); }
.activity-timeline { display: grid; gap: 0; margin: 10px 0 0; padding: 0; list-style: none; }
.activity-timeline li { display: grid; grid-template-columns: 65px minmax(0, 1fr); gap: 10px; align-items: baseline; padding: 8px 0; border-top: 1px solid var(--line); font-size: 12.5px; color: var(--ink); }
.activity-timeline time { color: var(--ink-3); font-size: 11px; white-space: nowrap; }
.activity-timeline span { overflow-wrap: anywhere; }
.metadata-history { display: grid; gap: 0; margin: 12px 0 0; padding: 0; list-style: none; }
.metadata-history li { display: grid; grid-template-columns: 72px minmax(0, 1fr); gap: 10px; align-items: baseline; padding: 8px 0; border-top: 1px solid var(--line); font-size: 12.5px; color: var(--ink); }
.metadata-history time { color: var(--ink-3); font-size: 11px; white-space: nowrap; }
.metadata-history span { overflow-wrap: anywhere; }
.callout { display: flex; align-items: center; gap: 12px; margin-bottom: 18px; padding: 12px 12px 12px 14px; border-radius: 12px; background: var(--gold-wash); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--gold) 35%, transparent); color: var(--warn-ink); }
.callout-text { display: grid; flex: 1; min-width: 0; font-size: 12.5px; color: var(--ink-2); }
.callout-text strong { color: var(--ink); font-size: 13px; }
.facts { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); align-items: start; gap: 12px 20px; margin: 0; }
.fact { display: grid; grid-template-columns: minmax(0, 1fr); gap: 3px; min-width: 0; }
.fact.wide { grid-column: 1 / -1; }
.fact dt { font-size: 12px; line-height: 1.4; color: var(--ink-3); }
.fact dd { margin: 0; font-size: 13px; color: var(--ink); overflow-wrap: anywhere; display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.fact dd.mono, .fact dd .mono { font-size: 12px; font-family: var(--mono); font-variant-ligatures: none; }
.commits { display: grid; gap: 5px; margin: 0; padding: 0; list-style: none; }
.commits li { display: flex; gap: 8px; flex-wrap: wrap; }
.commits code { font: 12px var(--mono); color: var(--ink-2); }
.chat-identity { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0; margin-top: 8px; }
.chat-setup { min-width: 0; font-size: 12px; color: var(--ink-3); overflow-wrap: anywhere; }
.capability { display: inline-flex; align-items: center; gap: 5px; min-width: 0; max-width: 100%; min-height: 28px; padding: 3px 9px; border: 0; border-radius: 999px; color: var(--teal-ink); background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); font: 600 12px/1.4 var(--font); cursor: help; text-align: left; }
.capability svg { flex: none; }
.capability.queue, .capability.between { color: var(--ink-2); background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--line); }
.capability.abort { color: var(--warn-ink); background: var(--gold-wash); box-shadow: inset 0 0 0 1px var(--warn-line); }
.capability:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (max-width: 720px) { .capability { min-height: 44px; } }
.muted { color: var(--ink-3); }
.evidence { display: inline-grid; place-items: center; width: 16px; height: 16px; border-radius: 50%; background: var(--chip-teal-bg); color: var(--teal-ink); }
.project-link { display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%; color: var(--ink); text-decoration: none; }
.project-link:hover { color: var(--teal-ink); }
/* The Ticket fact reads as one sentence, "PHAROS-11 Title · In progress" (AEON-1240): the dd flows as text, the
   link is inline, and its state is glued to the title's last word by a no-break space, so it never starts a line. */
.fact dd.ticket-dd { display: block; line-height: 1.5; }
.ticket-fact { color: var(--ink); text-decoration: none; }
.ticket-fact .key { font: 600 11.5px var(--mono); color: var(--teal-ink); }
.ticket-fact:hover .ticket-title { text-decoration: underline; }
.ticket-state { white-space: nowrap; }
/* Touch: the inline link reaches 44 px through its block padding, which moves no text. */
@media (max-width: 720px), (pointer: coarse) { .ticket-fact { padding-block: calc((44px - 1em) / 2); } }
/* Keep the computer fact's 24 px row and surface, with separate 44 px touch
   targets extending into its surrounding whitespace. */
@media (max-width: 720px), (pointer: coarse) {
  .host-fact { height: 24px; }
  .host-fact.editable { padding-right: 48px; }
  .host-fact :deep(.host-badge) { position: relative; isolation: isolate; min-width: 44px; height: 44px; margin-block: -10px; background: transparent; }
  .host-fact :deep(.host-badge)::before { content: ''; position: absolute; z-index: -1; inset: 10px 0; height: 24px; border-radius: inherit; background: color-mix(in srgb, var(--ink-3) 8%, transparent); }
  .host-fact :deep(.host-pencil) { top: -10px; width: 44px; height: 44px; opacity: 1; }
}
.block { margin-top: 24px; }
.block.first { margin-top: 18px; }
.block h3 { margin-bottom: 10px; }
.telemetry { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; }
.metric { display: grid; gap: 4px; padding: 10px 12px; border-radius: 10px; background: var(--code-bg); }
.metric-label { font-size: 11.5px; color: var(--ink-2); }
.metric b { font: 600 15px/1.2 var(--mono); color: var(--ink); font-variant-numeric: tabular-nums; }
.run-chip { justify-self: start; display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; font: 600 10.5px/1 var(--mono); letter-spacing: .04em; font-variant-ligatures: none; background: var(--chip-bg); color: var(--ink-2); box-shadow: inset 0 0 0 1px var(--chip-line); }
.run-chip.ok { background: var(--ok-bg); color: color-mix(in oklab, var(--ok), var(--ink) 28%); box-shadow: inset 0 0 0 1px var(--ok-line); }
.run-chip.busy { background: var(--chip-teal-bg); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.run-chip.bad { background: var(--danger-bg); color: color-mix(in oklab, var(--danger), var(--ink) 28%); box-shadow: inset 0 0 0 1px var(--danger-line); }
.empty-line { font-size: 13px; color: var(--ink-3); }
.runs { display: grid; grid-template-columns: minmax(0, 1fr); }
.run-row { display: grid; grid-template-columns: 92px minmax(0, 1fr) 48px 56px 68px; align-items: center; gap: 10px; min-height: 36px; border-bottom: 1px solid var(--line); font-size: 12.5px; }
.run-head { min-height: 24px; font: 500 9.5px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.run-row:last-child { border-bottom: 0; }
.run-model { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--ink); }
.run-tokens, .run-duration { font-size: 12px; color: var(--ink-2); text-align: right; font-variant-numeric: tabular-nums; }
.run-when { font-size: 12px; color: var(--ink-3); text-align: right; white-space: nowrap; }
.run-head .run-tokens, .run-head .run-duration, .run-head .run-when { font: inherit; color: inherit; }
.gone { display: grid; justify-items: center; gap: 8px; padding: 56px 16px; text-align: center; }
.gone h2 { font-size: 17px; }
.gone p { font-size: 13.5px; color: var(--ink-2); }
.gone .btn { margin-top: 8px; }
.gone-icon { display: grid; place-items: center; width: 44px; height: 44px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.sk { display: grid; gap: 14px; }
.sk .w40 { width: 40%; height: 18px; } .sk .w70 { width: 70%; } .sk .w90 { width: 90%; } .sk .w60 { width: 60%; }
@media (max-width: 720px) {
  /* The sheet follows the visual viewport, so the keyboard never covers the composer. */
  .session-panel { z-index: 40; inset: var(--vv-top, 0px) 0 auto 0; width: auto; height: var(--vv-h, 100dvh); border-radius: 0; border: 0; background: var(--canvas); }
  .session-panel.sheet { inset: var(--vv-top, 0px) 0 auto 0; width: auto; height: var(--vv-h, 100dvh); padding-bottom: env(safe-area-inset-bottom); }
  .panel-head { padding: calc(6px + env(safe-area-inset-top)) 8px 10px 16px; }
  .head-actions { flex-wrap: wrap; }
  .head-actions .spacer { flex-basis: 100%; height: 0; }
  /* Nested action components keep their labels' room and wrap onto the next line.
     Shrinking their roots hid Remove beyond the sheet's edge. */
  .head-actions :deep(.btn) { flex: 0 0 auto; min-width: min-content; min-height: 44px; padding-inline: 8px; }
  .head-actions :deep(.pause-controls) { flex-wrap: wrap; max-width: 100%; }
  .head-top .icon-btn { width: 44px; height: 44px; }
  /* Compact only after entering the composer. Keep that frame while focus
     moves to thread actions, so focusing Retry/Copy cannot move the click. */
  @media (prefers-reduced-motion: no-preference) { .session-panel { animation-name: sheet-in; } @keyframes sheet-in { from { transform: translateY(24px); opacity: 0; } to { transform: none; opacity: 1; } } }
}
</style>

<style scoped>
.session-panel { container: pane / inline-size; background: var(--surface); -webkit-backdrop-filter: none; backdrop-filter: none; }
.session-panel.docked .pane-bar { padding-right: 12px; }
.session-panel.full { position: fixed; z-index: 60; inset: var(--vv-top, 0px) 0 auto; width: auto; height: var(--vv-h, 100dvh); border: 0; border-radius: 0; box-shadow: none; padding-bottom: env(safe-area-inset-bottom); }
.session-panel.wide.docked { width: 100%; }
.panel-head { position: relative; padding: 0; border-bottom: 1px solid var(--line); }
.pane-bar { display: flex; align-items: center; gap: 16px; height: 44px; padding: 0 12px; }
.pane-identity { display: flex; align-items: center; gap: 8px; flex: 1 1 0; min-width: 0; }
.pane-title { display: flex; align-items: baseline; min-width: 0; overflow: hidden; font-size: 14.5px; font-weight: 650; margin: 0; white-space: nowrap; }
.pane-title .name { flex: none; font: inherit; white-space: nowrap; }
.title-separator { flex: none; white-space: pre; font-weight: 500; }
.title-detail { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; }
.pane-title:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
.pane-buttons { display: flex; align-items: center; flex: none; gap: 4px; margin-left: auto; }
.pane-icon { display: inline-grid; place-items: center; flex: none; width: 36px; height: 36px; padding: 0; border: 0; border-radius: var(--radius-row); background: transparent; color: var(--ink-2); cursor: pointer; }
.pane-icon:hover { background: var(--row-hover); color: var(--ink); }
.pane-icon:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
.session-feedback { padding: 8px 12px; margin: 0; background: var(--surface-raised); font-size: 12px; overflow-wrap: anywhere; }
.panel-menu { display: grid; gap: 2px; min-width: 0; }
.panel-menu-item { display: flex; align-items: center; gap: 10px; min-height: 44px; padding: 8px 10px; border: 0; border-radius: var(--radius-row); background: transparent; color: var(--ink); text-align: left; text-decoration: none; font: inherit; font-size: 13px; cursor: pointer; }
.panel-menu-item > svg { flex: none; color: var(--ink-2); }
.panel-menu-item > span { flex: 1; min-width: 0; }
.panel-menu-item small { display: block; margin-top: 2px; color: var(--ink-3); font-size: 11.5px; line-height: 1.4; }
.panel-menu-item:disabled { opacity: .6; cursor: not-allowed; }
.panel-menu-item:hover:not(:disabled) { background: var(--row-hover); }
.panel-menu-item:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
.phone-panel-choices { display: none; }
.live-pane { display: flex; flex-direction: column; flex: 1; min-height: 0; min-width: 0; overflow: hidden; }
.live-unavailable { padding: 24px; font-size: 13px; color: var(--ink-2); overflow-wrap: anywhere; }
.live-pane :deep(.replay-controls:has(.btn-label > span:first-child[aria-hidden="false"]) .replay-history > button), .live-pane :deep(.replay-controls:has(.btn-label > span:first-child[aria-hidden="false"]) .replay-history > span[aria-hidden="true"]) { display: none; }
.live-pane :deep(.expert-bar) { height: 44px; padding-block: 4px; border-bottom: 0; }
@container pane (max-width: 450px) { .pane-layout { display: none; } .pane-bar { gap: 16px; } }
@media (max-width: 720px) {
  .pane-bar { height: 92px; display: grid; grid-template-columns: minmax(0, 1fr) auto; grid-template-rows: 48px 44px; gap: 0 8px; padding: env(safe-area-inset-top) 12px 0; box-sizing: content-box; }
  .pane-identity { min-width: 0; }
  .pane-title { font-size: 13px; }
  .pane-bar > :deep(.session-tabs) { grid-column: 1 / -1; grid-row: 2; justify-self: start; }
  .pane-buttons { grid-column: 2; grid-row: 1; }
  .pane-layout { display: none; }
  .pane-icon { width: 44px; height: 44px; }
  .full .pane-bar { height: 48px; grid-template-rows: 48px; }
  .full .pane-bar > :deep(.session-tabs) { display: none; }
  .panel-menu.phone-full .phone-panel-choices { display: grid; }
  .live-pane :deep(.retained-size) { display: none; }
  .live-pane :deep(.retained-value) { white-space: nowrap; }
  .live-pane :deep(.retained-value:has(.count)) { font-size: 0; }
  .live-pane :deep(.retained-value .count), .live-pane :deep(.retained-value:has(.count)::before), .live-pane :deep(.retained-value:has(.count)::after) { font-size: 12px; }
  .live-pane :deep(.retained-value:has(.count)::before) { content: "Last "; }
  .live-pane :deep(.retained-value:has(.count)::after) { content: " events"; }
  .scroll :deep(.btn), .project-link { min-width: 44px; min-height: 44px; }
}
@media (pointer: coarse) { .pane-icon { width: 44px; height: 44px; } .pane-bar { min-height: 48px; } }
</style>
