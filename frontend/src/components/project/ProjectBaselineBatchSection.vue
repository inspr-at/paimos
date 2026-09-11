<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from '@/components/AppIcon.vue'
import LoadingText from '@/components/LoadingText.vue'
import { errMsg } from '@/api/client'
import {
  controlBaselineBatch,
  getBaselineWorkflow,
  importBaselineHandover,
  patchBaselineDraft,
  reconcileBaselineBatch,
  requestBaselineReadiness,
  reviewBaselineDraft,
  setBaselineStreamEnabled,
  startBaselineBatch,
  isClassOnlyScope,
  isUnavailableScope,
  runtimeChoiceProblem,
  runtimeScopes,
  scopeAttachmentRevision,
  scopedAccounts,
  scopedProfiles,
  type Batch,
  type ControlOption,
  type DelegatedLaunchSelection,
  type Draft,
  type Forecast,
  type WorkerSelection,
  type Workflow,
} from '@/services/projectBaselineBatch'

const props = defineProps<{
  projectId: number
  canWrite: boolean
}>()

const loading = ref(true)
const error = ref('')
const workflow = ref<Workflow | null>(null)
const handoverText = ref('')
const confirmStart = ref(false)
const busy = ref(false)
const reconciled = ref<Set<string>>(new Set())
const mode = ref<'manual' | 'assisted' | 'automatic'>('manual')
const selected = ref<string[]>([])
const runtimeId = ref('')
const accountLabel = ref('')
const accountKey = ref('')
const profileKey = ref('')
const workspaceHandle = ref('')
const workerName = ref('')
const delegatedLaunchEnabled = ref(false)
const delegatedTargetRef = ref('')
const delegatedExpiresAt = ref('')

const enabled = computed(() => workflow.value?.inspr_stream_enabled === true)
const draft = computed(() => workflow.value?.draft ?? null)
const active = computed(() => workflow.value?.active_batch ?? null)
const history = computed(() => (workflow.value?.batches ?? []).filter((b) => b.id !== active.value?.id))
const runtimes = computed(() => workflow.value?.choices.runtimes ?? [])
const runtime = computed(() => runtimes.value.find((r) => r.runtime_id === runtimeId.value) ?? runtimes.value[0] ?? null)
const agentMode = computed(() => mode.value !== 'manual')
const readiness = computed(() => workflow.value?.readiness ?? null)
const scopeProblem = computed(() => {
  if (!agentMode.value) return ''
  const contractProblem = runtimeChoiceProblem(runtime.value)
  if (contractProblem) return contractProblem
  if (isUnavailableScope(runtime.value, accountLabel.value))
    return 'This account scope has no attached account available. Connect an enrolled account and restart the owned runtime before review.'
  return ''
})
const accountClasses = computed(() => runtime.value ? runtimeScopes(runtime.value).map((scope) => scope.account_label) : [])
const namedAccounts = computed(() => scopedAccounts(runtime.value, accountLabel.value))
const classProfiles = computed(() => scopedProfiles(runtime.value, accountLabel.value))
const classOnly = computed(() => isClassOnlyScope(runtime.value, accountLabel.value))
const delegatedTargets = computed(() => workflow.value?.choices.delegated_launch_targets ?? [])
const delegatedTarget = computed(() => delegatedTargets.value.find((target) => target.target_ref === delegatedTargetRef.value) ?? null)
const launchSelectionProblem = computed(() => {
  if (!delegatedLaunchEnabled.value) return ''
  if (mode.value !== 'automatic') return 'Delegated launch is available only in automatic mode.'
  if (!delegatedTarget.value) return 'Select one current server-listed project environment.'
  const expires = Date.parse(delegatedExpiresAt.value)
  if (!Number.isFinite(expires) || expires <= Date.now() || expires > Date.now() + 24 * 60 * 60 * 1000) {
    return 'Choose an expiry after now and no more than 24 hours away.'
  }
  return ''
})
// Agent execution needs a current owned observation. Manual delivery does not,
// and never invents one.
const startBlockedReason = computed(() => {
  if (launchSelectionProblem.value) return launchSelectionProblem.value
  if (!agentMode.value) return ''
  if (scopeProblem.value) return scopeProblem.value
  if (!readiness.value) return 'Select a runtime, account, profile and workspace, then check readiness.'
  if (readiness.value.status === 'ready') return ''
  return readiness.value.blocking_reason || readiness.value.status
})
function sortedRefs(refs: string[] | undefined) {
  // Unique membership, stable order. Refs may contain commas; the array is
  // the set, never a joined string.
  return [...new Set((refs ?? []).map((ref) => ref.trim()).filter(Boolean))].sort()
}

function canonicalScopeKey(refs: string[] | undefined) {
  return JSON.stringify(sortedRefs(refs))
}

function setupRequiredCopy(kind: string) {
  if (kind === 'built_artifact_identity') {
    return 'Setup required: built_artifact_identity. Report the typed built receipt with paimos baseline-batch report-built; this screen never starts deployment.'
  }
  return `Setup required: ${kind}. Use the existing operator external-stage CLI; this screen never carries handoff secrets.`
}

function workerInputs(modeValue: string, worker: WorkerSelection) {
  if (modeValue === 'manual') {
    return {
      worker_name: '',
      runtime_id: '',
      runtime_generation: '',
      account_label: '',
      account_key: '',
      attachment_revision: 0,
      profile_id: '',
      profile_version: '',
      workspace_handle: '',
    }
  }
  return {
    worker_name: (worker.worker_name ?? '').trim(),
    runtime_id: worker.runtime_id ?? '',
    runtime_generation: worker.runtime_generation ?? '',
    account_label: worker.account_label ?? '',
    account_key: worker.account_key ?? '',
    attachment_revision: worker.attachment_revision ?? 0,
    profile_id: worker.profile_id ?? '',
    profile_version: worker.profile_version ?? '',
    workspace_handle: worker.workspace_handle ?? '',
  }
}

function localReviewInputs() {
  return {
    mode: mode.value,
    selected: sortedRefs(selected.value),
    ...workerInputs(mode.value, {
      worker_name: workerName.value,
      runtime_id: runtime.value?.runtime_id || runtimeId.value,
      runtime_generation: runtime.value?.runtime_generation ?? '',
      account_label: accountLabel.value,
      account_key: accountKey.value,
      attachment_revision: scopeAttachmentRevision(runtime.value, accountLabel.value),
      profile_id: profileKey.value.split('@')[0],
      profile_version: profileKey.value.split('@').slice(1).join('@'),
      workspace_handle: workspaceHandle.value,
    }),
    delegated_launch: delegatedLaunchPayload(),
  }
}

function reviewedInputs(d: Draft) {
  return {
    mode: d.execution_mode,
    selected: sortedRefs(d.selected?.requirement_refs),
    ...workerInputs(d.execution_mode, d.worker ?? {}),
    delegated_launch: d.delegated_launch ?? null,
  }
}

function sameReviewInputs(local: ReturnType<typeof localReviewInputs>, reviewed: ReturnType<typeof reviewedInputs>) {
  return local.mode === reviewed.mode
    && canonicalScopeKey(local.selected) === canonicalScopeKey(reviewed.selected)
    && local.worker_name === reviewed.worker_name
    && local.runtime_id === reviewed.runtime_id
    && local.runtime_generation === reviewed.runtime_generation
    && local.account_label === reviewed.account_label
    && local.account_key === reviewed.account_key
    && local.attachment_revision === reviewed.attachment_revision
    && local.profile_id === reviewed.profile_id
    && local.profile_version === reviewed.profile_version
    && local.workspace_handle === reviewed.workspace_handle
    && JSON.stringify(local.delegated_launch) === JSON.stringify(reviewed.delegated_launch)
}

// Server review_valid is the last bound snapshot. Local edits are not patched
// until review/readiness/start, so confirmation and start must follow the
// current inputs, not a recheckable box beside a stale review_valid flag.
const reviewBindingCurrent = computed(() => {
  const d = draft.value
  if (!d?.review_valid || !d.review_id) return false
  return sameReviewInputs(localReviewInputs(), reviewedInputs(d))
})

const canStart = computed(
  () => reviewBindingCurrent.value && confirmStart.value && !startBlockedReason.value && !busy.value,
)

watch(runtime, (r) => {
  if (!r) return
  // Defaults must not run over a bound review: filling a runtime/account
  // during hydration would make a rightful binding look dirty.
  if (draft.value?.review_valid) return
  if (!runtimeId.value) runtimeId.value = r.runtime_id
  const classes = runtimeScopes(r).map((scope) => scope.account_label)
  // A unique advertised class is not a first-scope guess. Mixed v3 stays
  // empty until the human names the class. Named keys and profiles are never
  // inferred from the first advertised row.
  if (!accountLabel.value && classes.length === 1) accountLabel.value = classes[0] ?? ''
  if (accountLabel.value && !classes.includes(accountLabel.value)) {
    accountLabel.value = ''
    accountKey.value = ''
    profileKey.value = ''
  }
  if (!workspaceHandle.value && r.workspaces[0]) workspaceHandle.value = r.workspaces[0].handle
})

watch(accountLabel, (label) => {
  const keys = scopedAccounts(runtime.value, label).map((account) => account.key)
  if (accountKey.value && !keys.includes(accountKey.value)) accountKey.value = ''
  const profiles = scopedProfiles(runtime.value, label)
  if (profileKey.value && !profiles.some((profile) => `${profile.id}@${profile.version}` === profileKey.value)) {
    profileKey.value = ''
  }
})

watch(mode, (value) => {
  if (value !== 'automatic') delegatedLaunchEnabled.value = false
})

watch([mode, selected, runtimeId, accountLabel, accountKey, profileKey, workspaceHandle, workerName,
  delegatedLaunchEnabled, delegatedTargetRef, delegatedExpiresAt], () => {
  confirmStart.value = false
})

function onConfirmStart(ev: Event) {
  const box = ev.target as HTMLInputElement
  confirmStart.value = box.checked && reviewBindingCurrent.value
  box.checked = confirmStart.value
}

function hydrateFromDraft(d: Draft) {
  selected.value = [...(d.selected.requirement_refs ?? [])]
  if (d.execution_mode === 'assisted' || d.execution_mode === 'automatic' || d.execution_mode === 'manual') {
    mode.value = d.execution_mode
  }
  runtimeId.value = d.worker.runtime_id ?? ''
  accountLabel.value = d.worker.account_label ?? ''
  accountKey.value = d.worker.account_key ?? ''
  profileKey.value = d.worker.profile_id ? `${d.worker.profile_id}@${d.worker.profile_version ?? ''}` : ''
  workspaceHandle.value = d.worker.workspace_handle ?? ''
  workerName.value = d.worker.worker_name ?? ''
  delegatedLaunchEnabled.value = !!d.delegated_launch
  delegatedTargetRef.value = d.delegated_launch?.target_ref ?? ''
  delegatedExpiresAt.value = d.delegated_launch?.expires_at ?? ''
}

const draftUnresolved = computed(() => draft.value?.unresolved ?? [])

function authorizedHandoffAction(batch: Batch) {
  return batch.progress.next_action === 'authorize_pharos_handoff'
    || batch.progress.next_action === 'authorize_verification_handoff'
    || batch.progress.next_action === 'rotate_revoked_handoff'
}

function reconcileDedupeKey(batch: Batch) {
  return [
    batch.id,
    batch.attempt_id ?? '',
    batch.progress.next_action,
    batch.progress.handoff?.stage_key ?? '',
    batch.progress.handoff?.handoff_id ?? '',
    batch.progress.setup_required ?? '',
  ].join(':')
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    workflow.value = await getBaselineWorkflow(props.projectId)
    if (workflow.value.draft) hydrateFromDraft(workflow.value.draft)
    await maybeReconcileAutomatic()
  } catch (e) {
    error.value = errMsg(e, 'Could not load delivery draft.')
  } finally {
    loading.value = false
  }
}

async function maybeReconcileAutomatic() {
  const batch = workflow.value?.active_batch
  if (!props.canWrite || !batch || batch.execution_mode !== 'automatic' || !authorizedHandoffAction(batch)) return
  if (reconciled.value.has(reconcileDedupeKey(batch))) return
  reconciled.value.add(reconcileDedupeKey(batch))
  try {
    const updated = await reconcileBaselineBatch(props.projectId, batch.id)
    applyBatch(updated)
  } catch (e) {
    error.value = errMsg(e, 'Could not apply the currently authorized Pharos handoff.')
  }
}

function applyBatch(updated: Batch) {
  const current = workflow.value
  if (!current) return
  workflow.value = {
    ...current,
    active_batch: current.active_batch?.id === updated.id ? updated : current.active_batch,
    batches: current.batches.map((row) => (row.id === updated.id ? updated : row)),
  }
}

async function reconcileAuthorized(batch: Batch) {
  if (!authorizedHandoffAction(batch)) return
  busy.value = true
  error.value = ''
  try {
    applyBatch(await reconcileBaselineBatch(props.projectId, batch.id))
  } catch (e) {
    error.value = errMsg(e, 'Could not apply the currently authorized Pharos handoff.')
  } finally {
    busy.value = false
  }
}

onMounted(() => { void load() })
watch(() => props.projectId, () => { void load() })

async function enableStream() {
  busy.value = true
  error.value = ''
  try {
    workflow.value = await setBaselineStreamEnabled(props.projectId, true)
  } catch (e) {
    error.value = errMsg(e, 'Could not enable the INSPR delivery stream.')
  } finally {
    busy.value = false
  }
}

async function importHandover() {
  busy.value = true
  error.value = ''
  try {
    const parsed = JSON.parse(handoverText.value) as unknown
    await importBaselineHandover(props.projectId, parsed)
    handoverText.value = ''
    await load()
  } catch (e) {
    error.value = errMsg(e, 'Import failed. Imported JSON is a claim, not approval.')
  } finally {
    busy.value = false
  }
}

function workerPayload() {
  if (!agentMode.value) return {}
  const [profileId, profileVersion] = profileKey.value.split('@')
  const payload: WorkerSelection = {
    worker_name: workerName.value.trim(),
    runtime_id: runtime.value?.runtime_id,
    runtime_generation: runtime.value?.runtime_generation,
    account_label: accountLabel.value,
    attachment_revision: scopeAttachmentRevision(runtime.value, accountLabel.value),
    profile_id: profileId,
    profile_version: profileVersion,
    workspace_handle: workspaceHandle.value,
  }
  if (accountKey.value) payload.account_key = accountKey.value
  return payload
}

function delegatedLaunchPayload(): DelegatedLaunchSelection | null {
  const target = delegatedTarget.value
  if (!delegatedLaunchEnabled.value || mode.value !== 'automatic' || !target) return null
  return {
    target_ref: target.target_ref,
    workflow: 'deploy-production',
    environment: target.environment,
    expires_at: delegatedExpiresAt.value.trim(),
    max_launches: 1,
  }
}

// The selection is stored on the draft first so the owned probe and the review
// bind exactly the same target.
async function saveSelection() {
  if (!draft.value) return
  await patchBaselineDraft(props.projectId, draft.value.id, {
    execution_mode: mode.value,
    selected_requirement_refs: sortedRefs(selected.value),
    worker: workerPayload(),
    delegated_launch: delegatedLaunchPayload(),
  })
}

async function checkReadiness() {
  if (!draft.value) return
  busy.value = true
  error.value = ''
  try {
    await saveSelection()
    await requestBaselineReadiness(props.projectId, draft.value.id)
    await load()
  } catch (e) {
    error.value = errMsg(e, 'The owned readiness probe could not be requested.')
  } finally {
    busy.value = false
  }
}

async function review() {
  if (!draft.value || launchSelectionProblem.value) return
  busy.value = true
  error.value = ''
  try {
    const reviewed = await reviewBaselineDraft(props.projectId, draft.value.id, {
      execution_mode: mode.value,
      selected_requirement_refs: sortedRefs(selected.value),
      worker: workerPayload(),
      delegated_launch: delegatedLaunchPayload(),
    })
    confirmStart.value = false
    if (workflow.value) workflow.value = { ...workflow.value, draft: reviewed }
    hydrateFromDraft(reviewed)
    await load()
  } catch (e) {
    error.value = errMsg(e, 'Review could not be bound.')
    confirmStart.value = false
  } finally {
    busy.value = false
  }
}

async function start() {
  if (!draft.value?.review_id || !confirmStart.value || !reviewBindingCurrent.value) return
  busy.value = true
  error.value = ''
  try {
    const idempotencyKey = crypto.randomUUID()
    await startBaselineBatch(props.projectId, draft.value.id, {
      idempotency_key: idempotencyKey,
      review_id: draft.value.review_id,
      draft_revision: draft.value.revision,
      confirm: true,
      content_digest: draft.value.baseline.content_digest,
      revision_seal: draft.value.baseline.revision_seal,
      execution_mode: mode.value,
      selected_requirement_refs: sortedRefs(selected.value),
      worker: workerPayload(),
      delegated_launch: delegatedLaunchPayload(),
    }, idempotencyKey)
    confirmStart.value = false
    await load()
  } catch (e) {
    error.value = errMsg(e, 'Start blocked. Guessed readiness cannot open the gate.')
  } finally {
    busy.value = false
  }
}

async function control(batch: Batch, action: string) {
  busy.value = true
  error.value = ''
  try {
    await controlBaselineBatch(props.projectId, batch.id, action, crypto.randomUUID())
    await load()
  } catch (e) {
    error.value = errMsg(e, 'Control failed.')
  } finally {
    busy.value = false
  }
}

function toggleReq(ref: string) {
  if (selected.value.includes(ref)) selected.value = selected.value.filter((r) => r !== ref)
  else selected.value = [...selected.value, ref]
}

function etaMinutes(seconds: number) {
  return Math.round(seconds / 60)
}

type ForecastContext = {
  scopeRefs?: string[]
  requirementCount?: number
  evidenceObserved?: boolean
  evidenceFresh?: boolean
  batchStatus?: string
}

function durablePlanningSeconds(ctx?: ForecastContext) {
  const scopeCount = ctx?.scopeRefs?.length ?? 0
  const impactCount = ctx?.requirementCount ?? 0
  const reqs = scopeCount > 0 ? scopeCount : (impactCount > 0 ? impactCount : 1)
  return {
    seconds: reqs * 1800,
    basis: `${reqs} selected requirement${reqs === 1 ? '' : 's'} at batch confirmation`,
  }
}

function scaleRemainingSeconds(total: number, percent: number) {
  if (percent <= 0) return total
  if (percent >= 100) return 0
  return Math.round(total * (100 - percent) / 100)
}

function clientEducatedFallback(f: Forecast, ctx?: ForecastContext) {
  const planning = durablePlanningSeconds(ctx)
  let remaining = scaleRemainingSeconds(planning.seconds, f.percent)
  if (remaining === 0 && f.percent < 100 && ctx?.batchStatus !== 'completed') {
    remaining = planning.seconds
  }
  let basis = planning.basis
  if (ctx?.evidenceObserved && !ctx?.evidenceFresh) {
    basis += '; progress evidence stale — educated ETA assumes batch planning unchanged'
  } else if (!ctx?.evidenceObserved) {
    basis += '; no progress observed yet — educated ETA from batch planning only'
  }
  return {
    text: `ETA ${etaMinutes(remaining)} min (estimated)`,
    basis: f.educated_eta_basis || basis,
  }
}

function forecastETA(f: Forecast, ctx?: ForecastContext) {
  if (f.eta_seconds != null) {
    return { text: `ETA ${etaMinutes(f.eta_seconds)} min`, basis: '' }
  }
  if (f.educated_eta_seconds != null) {
    const label = f.educated_eta_label ?? 'guessed'
    return {
      text: `ETA ${etaMinutes(f.educated_eta_seconds)} min (${label} fallback)`,
      basis: f.educated_eta_basis ?? '',
    }
  }
  return clientEducatedFallback(f, ctx)
}

function batchForecastCtx(batch: Batch): ForecastContext {
  return {
    scopeRefs: batch.scope?.requirement_refs,
    evidenceObserved: batch.progress?.evidence_observed,
    evidenceFresh: batch.progress?.evidence_fresh,
    batchStatus: batch.status,
  }
}

function draftForecastCtx(draft: Draft): ForecastContext {
  return {
    scopeRefs: draft.selected?.requirement_refs,
    requirementCount: draft.impact?.requirement_count,
  }
}

function forecastLine(f: Forecast, ctx?: ForecastContext) {
  const eta = forecastETA(f, ctx)
  const line = `${f.subject}: ${Math.round(f.percent)}% · ${eta.text} · ${f.label}`
  return eta.basis ? `${line} · ${eta.basis}` : line
}

function freshnessLine(batch: Batch) {
  if (!batch.progress.evidence_observed) return 'No progress observed yet'
  return batch.progress.evidence_fresh ? 'Progress evidence is fresh' : 'Progress evidence is stale'
}

function availableControls(batch: Batch): ControlOption[] {
  return batch.controls.filter((c) => c.available)
}

function unavailableControls(batch: Batch): ControlOption[] {
  return batch.controls.filter((c) => !c.available && !!c.reason)
}

function stateLabel(d: Draft | null, b: Batch | null) {
  if (b) return b.workflow_state
  if (reviewBindingCurrent.value) return 'review'
  if (d?.review_valid) return 'needs-review'
  if (d) return 'draft'
  return 'empty'
}
</script>

<template>
  <section v-if="!loading && !enabled && canWrite" class="bb bb-optin" data-testid="baseline-batch-optin">
    <p class="bb-note">
      INSPR delivery stream is off for this project. Nothing here changes existing tickets or delivery.
    </p>
    <button type="button" class="btn btn-sm" :disabled="busy" @click="enableStream">Enable INSPR delivery stream</button>
    <p v-if="error" class="bb-error">{{ error }}</p>
  </section>

  <section v-else-if="enabled" class="bb" data-testid="baseline-batch">
    <header class="bb-head">
      <AppIcon name="layers" :size="14" />
      <h3>Baseline delivery</h3>
      <span class="bb-state" data-testid="baseline-state">{{ stateLabel(draft, active) }}</span>
    </header>
    <p class="bb-note">
      Imported Aithema snapshots are claims. They do not authenticate an upstream human.
      A current project member must review and start. Adding to a draft never launches
      or mutates a running batch. Numeric forecasts are labelled guessed vs observed;
      guesses never unlock gates. Manual work does not invent an AI account.
    </p>
    <LoadingText v-if="loading" label="Loading delivery draft…" />
    <p v-else-if="error" class="bb-error">{{ error }}</p>

    <div v-if="active" class="bb-card" :data-batch-status="active.status">
      <div class="bb-row">
        <strong data-testid="batch-state">{{ active.status }}</strong>
        <RouterLink :to="`/projects/${projectId}/issues/${active.issue_id}`">Open issue</RouterLink>
      </div>
      <p v-if="active.progress.blocking_reason" class="bb-unresolved" data-testid="batch-blocking-reason">
        Blocked: {{ active.progress.blocking_reason }}
      </p>
      <p v-if="active.progress.setup_required" class="bb-unresolved" data-testid="batch-setup-required">
        {{ setupRequiredCopy(active.progress.setup_required) }}
      </p>
      <p v-if="active.progress.next_action" class="bb-meta" data-testid="batch-next-action">
        Next: {{ active.progress.next_action }}
      </p>
      <p v-if="active.progress.handoff" class="bb-meta" data-testid="batch-handoff">
        Handoff {{ active.progress.handoff.handoff_id }} · {{ active.progress.handoff.state }}
        <span v-if="active.progress.handoff.mint_required"> · mint required on the owner-only secret-file path</span>
      </p>
      <ul class="bb-forecasts" data-testid="batch-forecasts">
        <li v-for="f in active.forecasts" :key="f.kind + f.subject">
          {{ forecastLine(f, batchForecastCtx(active)) }} <span class="bb-meta">({{ f.basis }})</span>
        </li>
      </ul>
      <p class="bb-meta" data-testid="batch-freshness">{{ freshnessLine(active) }}</p>
      <ul class="bb-stages">
        <li v-for="stage in active.progress.stages" :key="stage.stage_key">
          {{ stage.stage_key }}: {{ stage.state }}<span v-if="stage.stale"> · stale</span>
          <span v-if="stage.never_signaled"> · no signal yet</span>
        </li>
      </ul>
      <p class="bb-meta">{{ active.baseline.content_digest }}</p>
      <p v-if="active.launch_grant" class="bb-launch-summary" data-testid="launch-grant">
        One-shot launch grant: <strong>{{ active.launch_grant.state }}</strong> ·
        {{ active.launch_grant.environment }} · used {{ active.launch_grant.used_launches }}/{{ active.launch_grant.max_launches }} ·
        expires {{ active.launch_grant.expires_at }}
      </p>
      <div v-if="canWrite" class="bb-actions">
        <button
          v-for="option in availableControls(active)"
          :key="option.action"
          type="button"
          class="btn btn-sm"
          :data-control="option.action"
          :disabled="busy"
          @click="control(active, option.action)"
        >
          {{ option.action }}
        </button>
        <button
          v-if="active.execution_mode === 'assisted' && authorizedHandoffAction(active)"
          type="button"
          class="btn btn-sm"
          data-testid="reconcile-handoff"
          :disabled="busy"
          @click="reconcileAuthorized(active)"
        >
          Authorize next Pharos handoff
        </button>
      </div>
      <p v-for="option in unavailableControls(active)" :key="`why-${option.action}`" class="bb-meta">
        {{ option.action }} unavailable: {{ option.reason }}
      </p>
    </div>

    <div v-if="draft" class="bb-card">
      <p class="bb-claim">
        Claimed approved_by <code>{{ draft.baseline.imported_claimed_approved_by || '(none)' }}</code>
        is <strong>untrusted</strong> ({{ draft.baseline.authenticity }}).
      </p>
      <p class="bb-meta">{{ draft.baseline.baseline_ref }} · r{{ draft.baseline.revision }}</p>
      <p class="bb-impact" data-testid="draft-impact">
        Estimated impact: {{ draft.impact.requirement_count }} requirements ·
        {{ draft.impact.acceptance_criteria_count }} acceptance criteria ·
        {{ draft.impact.unresolved_count }} unresolved · {{ forecastLine(draft.impact.forecast, draftForecastCtx(draft)) }}
      </p>
      <ul class="bb-reqs">
        <li v-for="req in draft.requirements" :key="req.requirement_ref">
          <label>
            <input type="checkbox" :checked="selected.includes(req.requirement_ref)" :disabled="!canWrite" @change="toggleReq(req.requirement_ref)" />
            <span>{{ req.requirement_ref }} — {{ req.statement }}</span>
          </label>
        </li>
      </ul>
      <p v-if="draftUnresolved.length" class="bb-unresolved">
        Unresolved: {{ draftUnresolved.map((u) => u.summary || u.ref).join(', ') }}
      </p>
      <fieldset v-if="canWrite" class="bb-mode">
        <legend>Execution</legend>
        <label><input v-model="mode" type="radio" value="manual" /> Manual</label>
        <label><input v-model="mode" type="radio" value="assisted" /> Assisted</label>
        <label><input v-model="mode" type="radio" value="automatic" /> Automatic</label>
      </fieldset>
      <div v-if="canWrite && agentMode" class="bb-worker">
        <input v-model="workerName" class="bb-input" data-testid="baseline-worker" placeholder="Named worker (project agent)" />
        <select v-model="runtimeId" class="bb-input" data-testid="baseline-runtime">
          <option v-for="r in runtimes" :key="r.runtime_id" :value="r.runtime_id">Runtime {{ r.runtime_id.slice(0, 8) }}</option>
        </select>
        <select v-model="accountLabel" class="bb-input" data-testid="baseline-account-class">
          <option disabled value="">Account class</option>
          <option v-for="cls in accountClasses" :key="cls" :value="cls">{{ cls }}</option>
        </select>
        <select v-if="namedAccounts.length" v-model="accountKey" class="bb-input" data-testid="baseline-account">
          <option disabled value="">Named account</option>
          <option v-for="a in namedAccounts" :key="a.key" :value="a.key">{{ a.label }}</option>
        </select>
        <p v-else-if="classOnly" class="bb-note" data-testid="baseline-class-only">
          Class-only {{ accountLabel }}: no named account key.
        </p>
        <select v-model="profileKey" class="bb-input" data-testid="baseline-profile">
          <option disabled value="">Profile</option>
          <option v-for="p in classProfiles" :key="p.id + p.version" :value="`${p.id}@${p.version}`">{{ p.id }} {{ p.version }}</option>
        </select>
        <select v-model="workspaceHandle" class="bb-input" data-testid="baseline-workspace">
          <option v-for="w in runtime?.workspaces ?? []" :key="w.handle" :value="w.handle">{{ w.label || w.handle.slice(0, 8) }}</option>
        </select>
        <p v-if="scopeProblem" class="bb-unresolved" data-testid="baseline-scope-error">{{ scopeProblem }}</p>
        <p v-else-if="!runtimes.length" class="bb-note">No owned runtime advertised. Assisted and automatic stay blocked until an owned probe proves this development target. Manual remains usable.</p>
      </div>
      <fieldset v-if="canWrite && mode === 'automatic'" class="bb-launch" data-testid="delegated-launch-review">
        <legend>One-shot deployment launch</legend>
        <label class="bb-confirm">
          <input v-model="delegatedLaunchEnabled" type="checkbox" data-testid="delegated-launch-enabled" />
          Allow the reviewed automatic batch to request exactly one deploy-production launch
        </label>
        <template v-if="delegatedLaunchEnabled">
          <label>
            Exact server-listed target
            <select v-model="delegatedTargetRef" class="bb-input" data-testid="delegated-launch-target">
              <option disabled value="">Select project environment</option>
              <option v-for="target in delegatedTargets" :key="target.target_ref" :value="target.target_ref">
                {{ target.label }}
              </option>
            </select>
          </label>
          <label>
            Expires at (RFC3339, within 24 hours)
            <input v-model="delegatedExpiresAt" class="bb-input" data-testid="delegated-launch-expiry" placeholder="2026-09-09T18:00:00Z" />
          </label>
          <p class="bb-note">
            This review binds the exact target digest, production workflow, environment, expiry, and maximum of one launch.
            Automatic mode or possession of a handoff secret alone is never host consent. Existing impact, privacy, data-loss,
            scope, review, artifact, and authority gates remain mandatory.
          </p>
          <p v-if="launchSelectionProblem" class="bb-unresolved" data-testid="delegated-launch-problem">{{ launchSelectionProblem }}</p>
        </template>
      </fieldset>
      <div v-if="agentMode" class="bb-readiness" data-testid="readiness">
        <p class="bb-meta">
          Readiness: <strong>{{ readiness?.status ?? 'unknown' }}</strong>
          <span v-if="readiness?.observed_at"> · observed {{ readiness.observed_at }}</span>
          <span v-if="readiness?.fresh_until"> · fresh until {{ readiness.fresh_until }}</span>
        </p>
        <p v-if="startBlockedReason" class="bb-unresolved" data-testid="readiness-blocking-reason">
          {{ startBlockedReason }}<span v-if="readiness?.next_action"> — next: {{ readiness.next_action }}</span>
        </p>
        <p v-if="readiness?.probe_state" class="bb-meta">Owned probe: {{ readiness.probe_state }}</p>
        <ul v-if="readiness?.checks?.length" class="bb-stages">
          <li v-for="check in readiness.checks" :key="check.id">{{ check.id }}: {{ check.status }} ({{ check.reason }})</li>
        </ul>
        <button v-if="canWrite" type="button" class="btn btn-sm" data-testid="check-readiness" :disabled="busy" @click="checkReadiness">
          Check readiness on the owned daemon
        </button>
      </div>
      <div v-if="canWrite" class="bb-actions">
        <button type="button" class="btn btn-sm" data-testid="bind-review" :disabled="busy || !!launchSelectionProblem" @click="review">Bind review</button>
        <label class="bb-confirm">
          <input
            type="checkbox"
            data-testid="confirm-start"
            :checked="confirmStart"
            :disabled="!reviewBindingCurrent"
            @change="onConfirmStart"
          />
          I confirm this exact baseline, scope, mode, and any displayed one-shot launch grant
        </label>
        <button type="button" class="btn btn-primary btn-sm" data-testid="start-batch" :disabled="!canStart" @click="start">
          Start batch
        </button>
      </div>
    </div>

    <div v-if="history.length" class="bb-card" data-testid="batch-history">
      <strong class="bb-meta">Batch history</strong>
      <ul class="bb-stages">
        <li v-for="batch in history" :key="batch.id">
          {{ batch.started_at }} · {{ batch.execution_mode }} · {{ batch.status }}
          <span v-for="f in batch.forecasts" :key="f.kind">· {{ forecastLine(f, batchForecastCtx(batch)) }}</span>
        </li>
      </ul>
    </div>

    <div v-if="canWrite" class="bb-card">
      <label class="bb-import">
        Paste aithema.handover/0.1 JSON
        <textarea v-model="handoverText" rows="6" class="bb-input" />
      </label>
      <button type="button" class="btn btn-sm" :disabled="busy || !handoverText.trim()" @click="importHandover">Add to draft</button>
    </div>
  </section>
</template>

<style scoped>
.bb {
  display: flex;
  flex-direction: column;
  gap: .7rem;
  min-width: 0;
  max-width: 100%;
}
.bb-optin { gap: .4rem; align-items: flex-start; }
.bb-head {
  display: flex;
  align-items: center;
  gap: .4rem;
  flex-wrap: wrap;
  min-width: 0;
}
.bb-head h3 {
  margin: 0;
  font-size: 13px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: .04em;
}
.bb-state, .bb-note, .bb-meta, .bb-impact, .bb-claim, .bb-unresolved, .bb-error {
  font-size: 12px;
  color: var(--text-muted);
  overflow-wrap: anywhere;
}
.bb-error { color: var(--danger, #94513c); }
.bb-card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: .8rem;
  display: flex;
  flex-direction: column;
  gap: .55rem;
  min-width: 0;
}
.bb-row, .bb-actions, .bb-mode, .bb-worker {
  display: flex;
  flex-wrap: wrap;
  gap: .45rem;
  align-items: center;
  min-width: 0;
}
.bb-launch {
  display: flex;
  flex-direction: column;
  gap: .45rem;
  border: 1px solid var(--border);
  border-radius: 8px;
  min-width: 0;
}
.bb-launch-summary { font-size: 12px; color: var(--text-muted); overflow-wrap: anywhere; }
.bb-readiness { display: flex; flex-direction: column; gap: .35rem; min-width: 0; }
.bb-reqs, .bb-forecasts, .bb-stages {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: .3rem;
  min-width: 0;
}
.bb-forecasts, .bb-stages { font-size: 12px; color: var(--text-muted); overflow-wrap: anywhere; }
.bb-reqs label, .bb-confirm, .bb-import {
  display: flex;
  gap: .4rem;
  align-items: flex-start;
  font-size: 13px;
}
.bb-input, .bb-import textarea {
  width: 100%;
  min-width: 0;
  max-width: 100%;
  box-sizing: border-box;
}
.bb-import { flex-direction: column; }
@media (max-width: 390px) {
  .bb-actions, .bb-worker, .bb-mode { flex-direction: column; align-items: stretch; }
  .bb-head { flex-direction: column; align-items: flex-start; }
}
</style>
