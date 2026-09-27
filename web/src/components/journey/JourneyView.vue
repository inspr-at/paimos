<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { brand } from '../../lib/brand'
import { computed, onBeforeUnmount, onMounted, provide, ref, watch, type Component } from 'vue'
import '../../styles/journey.css'
import {
  ACTION_LONG, captureJourneyConfirmation, GATE_OF_ACTION, isImported, isStage, listPlugins, offeredApproval, releaseName, releaseStateLabel, STAGE_LABEL, STAGE_LATER, STAGE_OWNER, STAGES,
  type ActionKey, type JourneyConfirmation, type PluginInfo, type Stage,
} from '../../lib/journey'
import type { Approval } from '../../lib/agents'
import { canDecideApproval } from '../../lib/agentState'
import { can } from '../../lib/authz'
import { JOURNEY, type JourneyContext, type NextState } from '../../lib/journeyContext'
import { useJourneyData } from '../../lib/useJourneyData'
import { usePlan } from '../../lib/usePlan'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { usePoller } from '../../lib/usePolledData'
import { useAgents } from '../../stores/agents'
import { StaleJourney, useJourney } from '../../stores/journey'
import AppIcon from '../AppIcon.vue'
import JourneyRail from './JourneyRail.vue'
import ReleaseWalker from './ReleaseWalker.vue'
import InspireStage from './InspireStage.vue'
import ShapeStage from './ShapeStage.vue'
import RequirementsStage from './RequirementsStage.vue'
import PlanStage from './PlanStage.vue'
import BuildStage from './BuildStage.vue'
import DeployStage from './DeployStage.vue'
import AccessStage from './AccessStage.vue'
import LiveStage from './LiveStage.vue'

// The project's journey: the stage rail with the one next action, and the view of
// the stage being looked at (the current one unless another is chosen). Real
// data only: the server's projection, intake, requirements, releases and handoffs.
const props = defineProps<{
  project: { id: string; routeKey: string; title: string }
  stage: string | null; releaseKey: string | null; walkKey: string | null
  canWrite: boolean; person: boolean; me: string | null
}>()
const emit = defineEmits<{ stage: [stage: Stage]; release: [key: string | null]; walk: [key: string | null, mode: 'open' | 'move' | 'close']; open: [key: string] }>()

const store = useJourney()
const agents = useAgents()
const projectId = computed(() => props.project.id)
const journey = computed(() => store.journeys[projectId.value] ?? null)
const now = ref(Date.now())
const plugins = ref<PluginInfo[]>([])
const viewed = computed<Stage>(() => isStage(props.stage) ? props.stage : journey.value?.stage ?? 'inspire')
const data = useJourneyData(computed(() => projectId.value), journey)
// A stage shows once what it reads has arrived the first time (later refreshes
// keep what is shown), so nothing reads as empty or "not started" while loading.
const loadedOnce = { intake: ref(false), origin: ref(false), work: ref(false) }
watch(projectId, () => { for (const flag of Object.values(loadedOnce)) flag.value = false })
const settled = (status: string) => status === 'ready' || status === 'error'
watch(data.intake.status, status => { if (settled(status)) loadedOnce.intake.value = true })
watch(data.origin.status, status => { if (settled(status)) loadedOnce.origin.value = true })
watch(data.work.status, status => { if (settled(status)) loadedOnce.work.value = true })
const imported = computed(() => !!journey.value && isImported(journey.value))
const loadingStage = computed(() => {
  const j = journey.value
  if (!j) return !store.errors[projectId.value]
  const stage = viewed.value
  if (['inspire', 'shape', 'requirements'].includes(stage) && !loadedOnce.intake.value) return true
  if (['inspire', 'shape'].includes(stage) && imported.value && !loadedOnce.origin.value) return true
  if (['plan', 'build', 'deploy', 'access', 'live'].includes(stage) && !!j.current_release_id && !settled(data.releaseNodes.status.value)) return true
  // Tickets are counted and grouped by their state and epic: wait for them.
  if (['requirements', 'plan', 'build', 'live'].includes(stage) && (imported.value || stage !== 'requirements') && !loadedOnce.work.value) return true
  return false
})

async function refresh(force = true) {
  await Promise.all([store.load(projectId.value, force), agents.refreshApprovals()])
}
watch(projectId, () => { void refresh() }, { immediate: true })
// Plugins describe the deploy and access steps; sessions name the agents asking for gates.
onMounted(async () => { void agents.refreshSessions(); try { plugins.value = await listPlugins() } catch { plugins.value = [] } })

// ---------- What each stage reads ----------
watch([viewed, () => journey.value?.revision], ([stage]) => {
  if (!journey.value) return
  // The current release is named everywhere (rail, gates), so its list is always read.
  if (journey.value.current_release_id) void data.loadReleases()
  if (stage === 'inspire' || stage === 'shape' || stage === 'requirements') void data.loadIntake()
  // An imported project brought its description, knowledge, epics and tickets: those stand in for intake.
  if ((stage === 'inspire' || stage === 'shape') && isImported(journey.value)) { void data.loadOrigin(); void data.loadWork() }
  if (stage === 'requirements') { void data.loadRequirements(); void data.loadWork() }
  if (['plan', 'build', 'deploy', 'access', 'live'].includes(stage)) { void data.loadReleases(); void data.loadWork() }
  if (stage === 'deploy' || stage === 'access') void data.loadHandoffs()
}, { immediate: true })
// The release shown: the one chosen, else the current one. Live shows what is
// live: the current release once the journey reached Live, else the newest one
// released before it (an imported project's history), else none.
const release = computed(() => {
  const refs = data.releases.value
  if (props.releaseKey) return refs.find(r => r.key.toLowerCase() === props.releaseKey!.toLowerCase()) ?? null
  const current = journey.value?.current_release_id
  const liveReached = journey.value?.stages.find(s => s.key === 'live')?.state !== 'later'
  if (viewed.value === 'live' && !liveReached) return [...refs].reverse().find(r => r.id !== current && releaseStateLabel(r.state) === 'Released') ?? null
  if (current) return refs.find(r => r.id === current) ?? null
  if (viewed.value === 'live') return refs.length ? refs[refs.length - 1] : null
  return null
})
const releaseLabel = computed(() => release.value ? releaseName(release.value) : 'the release')
const currentRelease = computed(() => data.releases.value.find(r => r.id === journey.value?.current_release_id) ?? null)
const isCurrent = computed(() => !!release.value && release.value.id === journey.value?.current_release_id)
watch(() => [release.value?.id, ['plan', 'build', 'deploy', 'access', 'live'].includes(viewed.value)] as const, ([id, needed]) => {
  if (needed && id) void data.loadWalker(id)
}, { immediate: true })
// When the journey moves (an action, a plan write, a gate elsewhere), what is
// already shown is read again so the stage never shows the state before.
watch(() => journey.value?.revision, (revision, before) => {
  if (revision === undefined || before === undefined || revision === before) return
  if (data.walker.status.value === 'ready' && data.walkerRelease.value) void data.loadWalker(data.walkerRelease.value, true)
  if (data.requirements.status.value === 'ready') void data.loadRequirements(true)
  if (data.intake.status.value === 'ready') void data.loadIntake(true)
  if (data.handoffs.status.value === 'ready') void data.loadHandoffs(true)
  if (data.releaseNodes.status.value === 'ready') void data.loadReleases(true)
})
const canAct = computed(() => props.person && can('journey.act', projectId.value))
// The plan changes only while the journey is at Plan, on the current release in planning.
const editable = computed(() => canAct.value && can('releases.write', projectId.value) && journey.value?.stage === 'plan' && isCurrent.value && data.walker.value.value?.state === 'planning' && data.walker.value.value.release_node_id === journey.value?.current_release_id)
const plan = usePlan(data, editable, () => { void store.load(projectId.value, true) })

// ---------- The one next action ----------
const gate = computed(() => journey.value ? GATE_OF_ACTION[journey.value.next_action.key] ?? null : null)
const approval = computed(() => journey.value && gate.value ? offeredApproval(agents.approvals, journey.value, gate.value, now.value) : null)
const actionDeadline = computed(() => {
  const id = journey.value?.next_action.approval_request_id
  if (!id) return Infinity
  const offer = journey.value?.stages.find(stage => stage.gate_offer_id === id)
  const details = agents.approvals.find(item => item.id === id)
  return Math.min(...[offer?.gate_offer_expires_at, details?.expires_at].map(value => Date.parse(value ?? '')).filter(Number.isFinite))
})
// Disable at the effective deadline, even between the ordinary clock ticks.
let expiryClock: ReturnType<typeof setTimeout> | undefined
watch([actionDeadline, now], ([deadline]) => {
  clearTimeout(expiryClock)
  if (Number.isFinite(deadline) && deadline > now.value) expiryClock = setTimeout(() => { now.value = Date.now() }, Math.min(2_147_483_647, Math.max(0, deadline - Date.now())))
}, { immediate: true })
const next = computed<NextState>(() => {
  const j = journey.value
  if (!j) return { label: '', disabled: true, tip: '', busy: false }
  const action = j.next_action
  const waitingForGate = !action.available && /gate/i.test(action.reason ?? '') && approval.value?.decision === null
  let disabled = !canAct.value || action.key === 'wait_for_build' || (!action.available && !waitingForGate && action.key !== 'continue_intake' && action.key !== 'decide')
  let tip = !canAct.value ? (props.person ? 'You can read this journey; changing it needs write access.' : 'Only a person can move the journey.') : action.available || waitingForGate ? '' : action.reason ?? ''
  const offeredStage = j.stages.find(stage => stage.gate_offer_id && stage.gate_offer_id === action.approval_request_id)
  const actionDetails = agents.approvals.find(item => item.id === action.approval_request_id)
  if (offeredStage?.gate_offer_state === 'expired' || (offeredStage?.gate_offer_expires_at && Date.parse(offeredStage.gate_offer_expires_at) <= now.value) || (actionDetails && Date.parse(actionDetails.expires_at) <= now.value)) {
    disabled = true
    tip = 'Gate approval expired. The agent asks again for a fresh one.'
  }
  if (approval.value?.decision === null && !canDecideApproval(approval.value, can)) { disabled = true; tip = 'Deciding this gate requires approval and action permissions.' }
  if (gate.value && !approval.value && action.key !== 'decide') { disabled = true; tip = tip || (action.approval_request_id ? 'The action gate is unavailable or its details are missing. Refresh to check it.' : `Waiting for the ${gate.value} gate: an agent asks for it, you approve it here.`) }
  // A pending gate is approved by the same click; labels that already say "Approve" stay as they are.
  const label = gate.value && approval.value?.decision === null && action.key !== 'decide' && !/^Approve /.test(action.label) ? `Approve and ${action.label.charAt(0).toLowerCase()}${action.label.slice(1)}` : action.label
  return { label, disabled, tip, busy: store.busy }
})
async function act(action: ActionKey, options: { approval?: Approval | null; reason?: string; done?: string; confirmation?: JourneyConfirmation<ActionKey> } = {}) {
  try {
    if (!journey.value) return false
    const confirmation = options.confirmation ?? captureJourneyConfirmation(journey.value, action, options.approval ?? null)
    if (confirmation.projectId !== projectId.value) throw new StaleJourney('The project changed while you were confirming. Review the current decision and confirm again.')
    if (!canAct.value || (confirmation.approval?.decision === null && !canDecideApproval(confirmation.approval, can))) throw new Error('You do not have permission to take this step.')
    const before = journey.value?.stage
    // The person who decides the gate is the one who takes the step (the server checks it).
    await store.act(confirmation, { reason: options.reason })
    toast(options.done ?? `${journey.value ? STAGE_LABEL[journey.value.stage] : 'Journey'}: done.`)
    void agents.refreshApprovals()
    if (journey.value && journey.value.stage !== before) emit('stage', journey.value.stage)
    return true
  } catch (e) {
    toast(e instanceof StaleJourney ? e.message : `${e instanceof Error ? e.message : 'The step was not taken.'}`, { tone: 'error' })
    return false
  }
}
const DONE: Partial<Record<string, (n: string) => string>> = {
  confirm_brief: () => 'Brief confirmed. The lenses run; then you decide.',
  open_first_release: () => 'Release 1 is open. Tick the tickets that form it.',
  mark_candidate: n => `${n} is the candidate. Review it next.`,
  start_build: n => `${n} build started · ${plan.stats.value.inRelease} tickets.`,
  approve_candidate: () => 'Release candidate approved. Deployment is next.',
  approve_deploy: () => 'Deployment approved.',
  retry_deploy: () => 'Deployment retried with fresh evidence.',
  approve_permit: () => 'Permit approved.',
  plan_next_release: () => 'The next release is open for planning.',
}
async function runNext() {
  now.value = Date.now()
  const j = journey.value
  if (!j || next.value.disabled && j.next_action.key !== 'continue_intake') return
  const action = j.next_action
  if (viewed.value !== action.stage) { emit('stage', action.stage); return }
  if (action.key === 'continue_intake') { document.querySelector<HTMLElement>('[data-sources]')?.scrollIntoView({ block: 'start', behavior: 'smooth' }); return }
  if (action.key === 'decide') { document.querySelector<HTMLElement>('.decide .btn')?.focus(); return }
  if (action.key === 'wait_for_build') return
  if (action.key === 'approve_requirements') {
    if (!approval.value) return
    const confirmation = captureJourneyConfirmation(j, action.key, approval.value)
    const ok = await confirmAction({ title: 'Agree the requirements?', body: `${confirmation.approval!.decision === null ? 'This approves the requirements gate and agrees' : 'This agrees'} revision ${j.requirements_revision}: features and tickets are generated from it.`, confirmLabel: next.value.label })
    if (!ok) return
    try {
      if (confirmation.projectId !== projectId.value) throw new StaleJourney('The project changed while you were confirming. Review the current decision and confirm again.')
      if (!canAct.value || (confirmation.approval!.decision === null && !canDecideApproval(confirmation.approval!, can))) throw new Error('You do not have permission to take this step.')
      await store.agree(confirmation); toast('Requirements agreed. Plan the release next.'); void data.loadRequirements(true); void data.loadReleases(true); void data.loadWork(true); void agents.refreshApprovals()
    }
    catch (e) { toast(e instanceof Error ? e.message : 'The requirements were not agreed.', { tone: 'error' }) }
    return
  }
  const key = action.key as ActionKey
  const confirmation = captureJourneyConfirmation(j, key, approval.value)
  const withGate = confirmation.approval
  const body = `${withGate && withGate.decision === null ? `This approves the ${gate.value} gate that ${agents.askerName(withGate.agent_principal_id, withGate.agent_name).name} asked for. ` : ''}${ACTION_LONG[action.key]}`
  const ok = await confirmAction({ title: `${action.label}?`, body, confirmLabel: next.value.label })
  if (!ok) return
  await act(key, { confirmation, done: DONE[key]?.(releaseLabel.value) })
}

// ---------- Navigation ----------
const walking = computed(() => props.walkKey !== null && !!data.walker.value.value)
function walk(key?: string) {
  const first = plan.order.value[0]
  const target = key ?? first?.key
  if (target) emit('walk', target, 'open')
}
const context: JourneyContext = {
  project: computed(() => props.project), journey: computed(() => journey.value!), data, plan, release, releaseLabel: computed(() => release.value ? releaseName(release.value) : 'Release'),
  current: isCurrent, editable, canAct, me: computed(() => props.me), now, approvals: computed(() => agents.approvals), plugins, next, runNext, act,
  view: stage => emit('stage', stage), walk, open: key => emit('open', key), selectRelease: key => emit('release', key),
}
provide(JOURNEY, context)
const STAGE_VIEW: Record<Stage, Component> = { inspire: InspireStage, shape: ShapeStage, requirements: RequirementsStage, plan: PlanStage, build: BuildStage, deploy: DeployStage, access: AccessStage, live: LiveStage }
const viewedState = computed(() => journey.value?.stages.find(s => s.key === viewed.value)?.state ?? 'later')
const CHIP: Record<string, string> = { current: 'Now', done: 'Done', skipped: 'Not needed', later: 'Later', blocked: 'Blocked' }
const title = computed(() => {
  const r = release.value ? releaseName(release.value) : ''
  switch (viewed.value) {
    case 'inspire': return imported.value && viewedState.value !== 'current' ? 'Where it came from' : 'Conversation and sources'
    case 'plan': return r ? `Plan ${r.toLowerCase()}` : 'Plan'
    case 'build': return r ? `Build ${r.toLowerCase()}` : 'Build'
    case 'deploy': return r ? `Deploy ${r.toLowerCase()}` : 'Deploy'
    case 'access': return r ? `Access for ${r.toLowerCase()}` : 'Access'
    case 'live': return r && (viewedState.value !== 'later' || releaseStateLabel(release.value!.state) === 'Released') ? `${r} is live` : 'Live'
    default: return STAGE_LABEL[viewed.value]
  }
})
const subtitle = computed(() => viewedState.value === 'later' ? STAGE_LATER[viewed.value]
  : viewed.value === 'inspire' ? (imported.value && viewedState.value !== 'current' ? 'Brought over from Paimos with its history.' : STAGE_LATER.inspire)
  : `Profile: ${journey.value ? journey.value.profile.charAt(0).toUpperCase() + journey.value.profile.slice(1) : ''}`)

// ---------- Keys: [ and ] move between stages, w opens the walker ----------
function typing(target: EventTarget | null) { return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName)) }
function keydown(event: KeyboardEvent) {
  if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || typing(event.target)) return
  if (document.querySelector('dialog[open], .floating')) return
  const i = STAGES.indexOf(viewed.value)
  if (event.key === '[' && i > 0) { event.preventDefault(); emit('stage', STAGES[i - 1]) }
  else if (event.key === ']' && i < STAGES.length - 1) { event.preventDefault(); emit('stage', STAGES[i + 1]) }
  else if (event.key === 'w' && data.walker.value.value?.tickets.length && ['plan', 'build', 'live'].includes(viewed.value)) { event.preventDefault(); walk() }
}
let clock: ReturnType<typeof setInterval> | undefined
const poll = usePoller(refresh, 30_000, { enabled: () => !store.busy && !document.querySelector('dialog[open]') })
onMounted(() => {
  window.addEventListener('keydown', keydown)
  clock = setInterval(() => { now.value = Date.now() }, 15_000)
  // The journey moves with agents and gates: refresh it while the page is visible.
  poll.start()
})
onBeforeUnmount(() => { window.removeEventListener('keydown', keydown); clearInterval(clock); clearTimeout(expiryClock); poll.stop() })
</script>

<template>
  <section class="journey-view" aria-labelledby="journey-title">
    <div v-if="loadingStage" class="journey-skeleton" role="status" aria-label="Loading the journey">
      <span class="skeleton sk-rail" /><span class="skeleton sk-head" /><span class="skeleton sk-body" />
    </div>
    <div v-else-if="!journey" class="journey-error" role="alert">
      <AppIcon name="alert" :size="20" />
      <h2 id="journey-title">The journey could not be loaded</h2>
      <p>{{ store.errors[project.id] || 'Please try again.' }}</p>
      <button type="button" class="btn" @click="refresh()"><AppIcon name="refresh" :size="14" />Try again</button>
    </div>
    <template v-else>
      <JourneyRail :journey="journey" :viewed="viewed" :project-title="project.title" :release-label="currentRelease ? releaseName(currentRelease) : ''" :action="next" @view="s => emit('stage', s)" />
      <header class="stage-head">
        <div class="stage-t">
          <p class="eyebrow">{{ STAGE_LABEL[viewed] }} · <span class="state-chip" :class="viewedState">{{ CHIP[viewedState] }}</span></p>
          <h2 id="journey-title">{{ title }}</h2>
        </div>
        <p class="subtitle">{{ subtitle }}</p>
        <span class="spacer" />
        <span class="owner mono" :data-tip="`${STAGE_LABEL[viewed]} is carried by ${STAGE_OWNER[viewed] ?? brand.short_name}`">{{ STAGE_OWNER[viewed] ?? brand.short_name }}</span>
      </header>
      <component :is="STAGE_VIEW[viewed]" :key="viewed" />
      <p class="journey-hint">
        <kbd class="keycap">[</kbd><kbd class="keycap">]</kbd> stages<template v-if="data.walker.value.value?.tickets.length && ['plan', 'build', 'live'].includes(viewed)"> · <kbd class="keycap">w</kbd> walk the release</template> · <kbd class="keycap">?</kbd> all shortcuts
      </p>
    </template>
    <ReleaseWalker
      v-if="walking" :plan="plan" :editable="editable" :release-label="release ? releaseName(release) : 'Release'" :project-key="project.routeKey"
      :work-by-id="data.workById.value" :start-key="walkKey" @close="emit('walk', null, 'close')" @moved="key => emit('walk', key, 'move')" @open="key => { emit('walk', null, 'close'); emit('open', key) }"
    />
  </section>
</template>

<style scoped>
/* Wide screens: the journey keeps a readable width, aligned with the header. */
.journey-view { display: grid; grid-template-columns: minmax(0, 1fr); min-width: 0; gap: 16px; max-width: 1760px; padding: 4px 0 8px; container: journey / inline-size; }
/* A docked ticket narrows the journey without narrowing the viewport. Keep
   both cards readable by responding to the space the stage actually has. */
@container journey (max-width: 800px) {
  .journey-view :deep(.j-grid) { grid-template-columns: minmax(0, 1fr); }
}
.stage-head { display: flex; align-items: flex-end; gap: 18px; min-width: 0; padding: 4px 2px 0; }
.stage-t { display: grid; gap: 4px; min-width: 0; }
.stage-t h2 { font-size: 26px; font-weight: 300; letter-spacing: -.02em; }
.state-chip { display: inline-flex; align-items: center; height: 18px; margin-left: 4px; padding: 0 8px; border-radius: 999px; font: 600 10px/1 var(--mono); letter-spacing: .08em; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); vertical-align: 1px; }
.state-chip.current { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.state-chip.done { color: var(--ok); }
.state-chip.blocked { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.subtitle { padding-bottom: 4px; font-size: 13.5px; color: var(--ink-2); }
.spacer { flex: 1; }
.owner { flex-shrink: 0; padding: 3px 9px; border-radius: 999px; font-size: 10.5px; letter-spacing: .08em; text-transform: uppercase; color: var(--ink-3); background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); }
.journey-hint { display: flex; align-items: center; justify-content: center; gap: 5px; padding: 8px 0 0; font-size: 12px; color: var(--ink-3); }
.journey-hint .keycap + .keycap { margin-left: 2px; }
.journey-skeleton { display: grid; gap: 14px; }
.sk-rail { height: 138px; border-radius: var(--radius); }
.sk-head { width: min(320px, 100%); height: 62px; border-radius: 8px; }
.sk-body { height: 320px; border-radius: var(--radius); }
.journey-error { display: grid; justify-items: center; gap: 10px; padding: 72px 24px; text-align: center; }
.journey-error > svg { color: var(--danger); }
@media (max-width: 720px) {
  .stage-head { flex-wrap: wrap; align-items: flex-start; gap: 6px; }
  .subtitle { flex-basis: 100%; padding-bottom: 0; }
  .stage-t h2 { font-size: 22px; }
  .spacer, .owner { display: none; }
  .journey-hint { display: none; }
}
</style>
