<script setup lang="ts">
import { computed, nextTick, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { ApiError, sessionExpired } from '@/api/client'
import { clearHabitatIntentRecovery } from '@/constants/storage'
import { loadOrchestration, type HarnessDispatchProfile } from '@/services/orchestration'
import { humanize, type HabitatWorker } from './habitatModel'
import {
  loadHabitatRuntimes,
  submitHabitatIntent,
  loadHabitatIntent,
  cancelHabitatIntent,
  parseHabitatRequest,
  habitatAccountChoices,
  habitatChoiceId,
  habitatRuntimeClasses,
  habitatRuntimeProfiles,
  type HabitatRuntime,
  type HabitatIntent,
  type HabitatLifecycleRequest,
} from './habitatLifecycle'
import {
  readIntentRecovery,
  writeIntentRecovery,
  clearIntentRecovery,
  type RecoveryScope,
} from './habitatRecovery'
import HabitatTicketPicker from './HabitatTicketPicker.vue'
const props = withDefaults(
  defineProps<{
    projectId: number | null
    agent: string
    profile: HarnessDispatchProfile | null
    authority: string
    deployment?: string | null
    workers?: HabitatWorker[]
    fresh?: boolean
    selectedSessionId?: string
  }>(),
  { deployment: null, workers: () => [], fresh: false, selectedSessionId: '' },
)
const emit = defineEmits<{ refresh: [] }>()
const auth = useAuthStore()
const runtimeState = ref<'loading' | 'ready' | 'unavailable' | 'unauthorized'>('loading')
const runtimes = shallowRef<HabitatRuntime[]>([])
const candidates = shallowRef<HabitatWorker[]>([])
const candidateState = ref<'loading' | 'ready' | 'unavailable'>('loading')
const candidateTruncated = ref(false)
const runtimeId = ref(''),
  workspace = ref(''),
  sessionId = ref(''),
  parentId = ref(''),
  accountChoiceId = ref('')
const ticketId = ref<number | null>(null)
const workShape = ref<'ship' | 'scout'>('ship'),
  role = ref<'worker' | 'coordinator'>('worker')
const operation = ref<HabitatLifecycleRequest['operation']>('start')
const repairLayer = ref<'reporter' | 'listeners'>('reporter')
const pendingRequest = shallowRef<HabitatLifecycleRequest | null>(null)
const intent = shallowRef<HabitatIntent | null>(null)
const intentFresh = ref(false),
  recovering = ref(false),
  feedback = ref(''),
  busy = ref(false),
  reviewing = ref(false),
  refreshingRuntimes = ref(false)
const confirmRef = ref<HTMLButtonElement | null>(null)
const prepareRef = ref<HTMLButtonElement | null>(null)
const runtimeRef = ref<HTMLSelectElement | null>(null)
const operationRef = ref<HTMLSelectElement | null>(null)
const now = ref(Date.now())
let attempted = false,
  savedAt = 0,
  recoveryId: string | null = null,
  preparedIdentity = '',
  generation = 0,
  lastRuntimeRefreshAt = 0
let controller: AbortController | null = null
const RUNTIME_REFRESH_COOLDOWN_MS = 30_000
const scope = computed<RecoveryScope | null>(() =>
  auth.user?.id &&
  !sessionExpired.value &&
  !auth.impersonation &&
  props.deployment &&
  props.projectId
    ? {
        origin: location.origin,
        instance: props.deployment,
        principalId: auth.user.id,
        projectId: props.projectId,
      }
    : null,
)
const eligible = computed(() => auth.isSuperAdmin && !!scope.value)
const runtime = computed(
  () =>
    runtimes.value.find(
      (row) => row.id === runtimeId.value && Date.parse(row.expires_at) > now.value,
    ) ?? null,
)
const existing = computed(() => ['attach', 'reassign', 'restart'].includes(operation.value))
const accountChoices = computed(() => {
  if (!runtime.value) return []
  return habitatAccountChoices(
    runtime.value,
    existing.value || operation.value === 'repair' ? null : props.profile,
  )
})
const selectedAccount = computed(
  () => accountChoices.value.find((choice) => habitatChoiceId(choice) === accountChoiceId.value) ?? null,
)
const implicitAccount = computed(() => {
  const choices = accountChoices.value
  return choices.length === 1 && !choices[0]!.account_key ? choices[0]! : null
})
const uniqueRepairClass = computed(() => {
  if (operation.value !== 'repair' || !runtime.value) return ''
  const classes = habitatRuntimeClasses(runtime.value)
  return classes.length === 1 ? classes[0]! : ''
})
const accountKey = computed(() => selectedAccount.value?.account_key ?? implicitAccount.value?.account_key ?? '')
const accountLabel = computed(
  () =>
    selectedAccount.value?.account_label ??
    implicitAccount.value?.account_label ??
    uniqueRepairClass.value,
)
const accountAvailable = computed(() => {
  if (!runtime.value) return false
  if (!accountChoices.value.length) return false
  return selectedAccount.value !== null || implicitAccount.value !== null
})
const ownedWorkers = computed(() => {
  const owner = runtime.value
  if (!owner || candidateState.value !== 'ready' || !props.fresh) return []
  return candidates.value.filter(
    (worker) =>
      worker.project.id === props.projectId &&
      worker.management_mode === 'managed' &&
      worker.runtime_provenance_trust === 'managed_reporter' &&
      worker.machine_id === owner.machine_id &&
      worker.account_label === accountLabel.value &&
      (accountKey.value ? (worker.account_key ?? '') === accountKey.value : !worker.account_key) &&
      owner.sessions.some((row) => row.session_id === worker.harness_session_id) &&
      (operation.value === 'restart'
        ? worker.phase === 'stopped' && worker.liveness.state === 'dead'
        : worker.liveness.state === 'idle' &&
          worker.liveness.source === 'agentd_reporter' &&
          worker.liveness.reporter_age_seconds !== null &&
          !['stopped', 'stopping'].includes(worker.phase)),
  )
})
const selectedWorker = computed(
  () => ownedWorkers.value.find((row) => row.harness_session_id === sessionId.value) ?? null,
)
const selectedWorkspaceHandle = computed(
  () =>
    runtime.value?.sessions.find(
      (row) => row.session_id === selectedWorker.value?.harness_session_id,
    )?.workspace_handle ?? null,
)
const availableWorkspaces = computed(() =>
  existing.value
    ? (runtime.value?.workspaces.filter((row) => row.handle === selectedWorkspaceHandle.value) ??
      [])
    : (runtime.value?.workspaces ?? []),
)
watch(selectedWorkspaceHandle, (handle) => {
  if (existing.value && !pendingRequest.value) workspace.value = handle ?? ''
})
const effectiveAgent = computed(() =>
  existing.value ? (selectedWorker.value?.agent.name ?? '') : props.agent,
)
const effectiveProfile = computed(() =>
  existing.value ? (selectedWorker.value?.dispatch_profile ?? null) : props.profile,
)
const effectiveRole = computed(() => (existing.value ? selectedWorker.value?.role : role.value))
const profileAvailable = computed(
  () =>
    !!effectiveProfile.value &&
    !!runtime.value &&
    habitatRuntimeProfiles(runtime.value).some(
      (profile) =>
        profile.id === effectiveProfile.value?.id &&
        profile.version === effectiveProfile.value?.version,
    ),
)
const parents = computed(() =>
  candidates.value.filter((row) => {
    if (
      row.project.id !== props.projectId ||
      ['stopped', 'stopping'].includes(row.phase) ||
      !['idle', 'busy'].includes(row.liveness.state) ||
      row.harness_session_id === sessionId.value
    )
      return false
    const seen = new Set<string>()
    let cursor: HabitatWorker | undefined = row
    while (cursor?.parent_harness_session_id) {
      if (
        cursor.parent_harness_session_id === sessionId.value ||
        seen.has(cursor.parent_harness_session_id)
      )
        return false
      seen.add(cursor.parent_harness_session_id)
      cursor = candidates.value.find(
        (parent) => parent.harness_session_id === cursor?.parent_harness_session_id,
      )
      if (!cursor) return false // ancestry outside this bounded page is not guessed
    }
    return true
  }),
)
const bindingChanged = computed(
  () =>
    !existing.value ||
    operation.value === 'restart' ||
    (selectedWorker.value &&
      ((selectedWorker.value.ticket?.id ?? null) !== ticketId.value ||
        selectedWorker.value.parent_harness_session_id !== (parentId.value || null) ||
        selectedWorker.value.work_shape !== (ticketId.value ? workShape.value : 'unknown'))),
)
const canPrepare = computed(
  () =>
    eligible.value &&
    runtime.value !== null &&
    runtimeState.value === 'ready' &&
    (operation.value === 'repair' ? accountLabel.value !== '' : accountAvailable.value) &&
    (operation.value === 'repair' ||
      (profileAvailable.value &&
        /^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/.test(effectiveAgent.value) &&
        ['worker', 'coordinator'].includes(effectiveRole.value ?? '') &&
        availableWorkspaces.value.some((row) => row.handle === workspace.value) &&
        (!existing.value || selectedWorker.value !== null) &&
        bindingChanged.value &&
        (!parentId.value ||
          parents.value.some((row) => row.harness_session_id === parentId.value)))),
)
const terminal = computed(
  () =>
    !!intent.value && ['completed', 'failed', 'expired', 'cancelled'].includes(intent.value.state),
)
const requestIdentity = computed(() =>
  JSON.stringify([
    effectiveAgent.value,
    effectiveProfile.value,
    runtimeId.value,
    runtime.value?.generation,
    workspace.value,
    ticketId.value,
    workShape.value,
    effectiveRole.value,
    operation.value,
    repairLayer.value,
    sessionId.value,
    selectedWorker.value?.revision,
    parentId.value,
    accountChoiceId.value,
    accountLabel.value,
    selectedAccount.value?.attachment_revision,
  ]),
)
function invalidate() {
  attempted = false
  savedAt = 0
  recoveryId = null
  generation++
  controller?.abort()
  controller = new AbortController()
  pendingRequest.value = null
  intent.value = null
  intentFresh.value = false
  recovering.value = false
  reviewing.value = false
  feedback.value = ''
  busy.value = false
  refreshingRuntimes.value = false
  runtimeId.value = ''
  workspace.value = ''
  ticketId.value = null
  parentId.value = ''
  sessionId.value = ''
  accountChoiceId.value = ''
  runtimes.value = []
  candidates.value = []
}
async function refreshRuntimes() {
  if (!eligible.value || !props.projectId) {
    runtimeState.value = 'unauthorized'
    refreshingRuntimes.value = false
    return
  }
  if (refreshingRuntimes.value) return
  const version = generation,
    signal = controller!.signal,
    project = props.projectId
  refreshingRuntimes.value = true
  lastRuntimeRefreshAt = Date.now()
  if (runtimeState.value !== 'ready') runtimeState.value = 'loading'
  if (candidateState.value !== 'ready') candidateState.value = 'loading'
  try {
    const [runtimeResult, workerResult] = await Promise.allSettled([
      loadHabitatRuntimes(project, signal),
      loadOrchestration({ projectId: project, zoom: '100', signal }),
    ])
    if (version !== generation || signal.aborted) return
    now.value = Date.now()
    if (runtimeResult.status === 'fulfilled') {
      runtimes.value = runtimeResult.value
      runtimeState.value = 'ready'
    } else {
      runtimes.value = []
      runtimeState.value = 'unavailable'
      const error = runtimeResult.reason
      if (error instanceof ApiError && [401, 403].includes(error.status)) {
        runtimeState.value = 'unauthorized'
        clearSavedEvidence()
      }
    }
    if (workerResult.status === 'fulfilled') {
      candidates.value = workerResult.value.fleet.workers
      candidateTruncated.value = workerResult.value.fleet.sample_truncated
      candidateState.value = 'ready'
    } else {
      candidates.value = []
      candidateTruncated.value = false
      candidateState.value = 'unavailable'
    }
    // Initial route selection is useful, but an advertisement refresh must not
    // silently replace a user's valid runtime choice or an immutable request.
    if (!pendingRequest.value && !runtimeId.value && props.selectedSessionId) {
      const worker = candidates.value.find(
        (row) => row.harness_session_id === props.selectedSessionId,
      )
      const owners = runtimes.value.filter((row) =>
        row.sessions.some((mapping) => mapping.session_id === props.selectedSessionId),
      )
      if (worker && owners.length === 1) {
        runtimeId.value = owners[0]!.id
        operation.value = worker.phase === 'stopped' ? 'restart' : 'reassign'
        sessionId.value = worker.harness_session_id
        accountChoiceId.value = habitatChoiceId({
          account_label: worker.account_label,
          account_key: worker.account_key ?? '',
          label: worker.account_label,
        })
      }
    }
  } finally {
    if (version === generation && !signal.aborted) refreshingRuntimes.value = false
  }
}
function clearSavedEvidence() {
  if (scope.value) clearIntentRecovery(scope.value)
  pendingRequest.value = null
  intent.value = null
  intentFresh.value = false
  recoveryId = null
  attempted = false
  reviewing.value = false
}
function persist(): boolean {
  return (
    !!scope.value &&
    !!pendingRequest.value &&
    writeIntentRecovery(scope.value, {
      intentId: recoveryId,
      request: pendingRequest.value,
      savedAt,
    })
  )
}
async function recover() {
  if (!eligible.value || !scope.value) return
  const stored = readIntentRecovery(scope.value)
  if (!stored) return
  attempted = true
  savedAt = stored.savedAt
  recoveryId = stored.intentId
  pendingRequest.value = stored.request
  if (recoveryId) {
    recovering.value = true
    await check()
    recovering.value = false
  } else
    feedback.value =
      'A saved request has no confirmed receipt. Nothing was repeated. Explicitly retry the exact request to recover its result.'
}
watch(
  () => [
    props.authority,
    props.projectId,
    props.deployment,
    props.selectedSessionId,
    eligible.value,
    sessionExpired.value,
  ],
  () => {
    if (!auth.user?.id || sessionExpired.value || auth.impersonation || !auth.isSuperAdmin)
      clearHabitatIntentRecovery()
    invalidate()
    void refreshRuntimes()
    void recover()
  },
  { immediate: true, flush: 'sync' },
)
watch(
  () => props.workers,
  () => {
    // Re-read authoritative candidates when the parent snapshot changes. The
    // request-identity watcher below decides whether a selected review is stale;
    // an unrelated heartbeat must not dismiss an otherwise exact review.
    void refreshRuntimes()
  },
  { deep: true },
)
watch(
  [
    () => selectedWorker.value?.harness_session_id,
    () => selectedWorker.value?.revision,
    () => selectedWorker.value?.ticket?.id ?? null,
    () => selectedWorker.value?.work_shape,
    () => selectedWorker.value?.parent_harness_session_id ?? '',
  ],
  () => {
    const worker = selectedWorker.value
    if (pendingRequest.value || !worker) return
    ticketId.value = worker.ticket?.id ?? null
    workShape.value = worker.work_shape === 'scout' ? 'scout' : 'ship'
    parentId.value = worker.parent_harness_session_id ?? ''
  },
  { immediate: true },
)
watch(requestIdentity, () => {
  if (!attempted) {
    reviewing.value = false
    pendingRequest.value = null
    preparedIdentity = ''
  }
})
watch(reviewing, (value) => {
  if (value) void nextTick(() => confirmRef.value?.focus())
})
watch(
  () => [runtime.value?.id, accountChoices.value] as const,
  () => {
    if (pendingRequest.value) return
    const choices = accountChoices.value
    if (!choices.length) {
      accountChoiceId.value = ''
      return
    }
    if (choices.some((choice) => habitatChoiceId(choice) === accountChoiceId.value)) return
    const uniqueClass = !!runtime.value && habitatRuntimeClasses(runtime.value).length === 1
    accountChoiceId.value = choices.length === 1 && uniqueClass ? habitatChoiceId(choices[0]!) : ''
  },
  { immediate: true },
)
function prepare() {
  if (!canPrepare.value || !runtime.value || pendingRequest.value || !accountLabel.value) return
  const common = {
    request_key: crypto.randomUUID(),
    runtime_id: runtime.value.id,
    runtime_generation: runtime.value.generation,
    account_label: accountLabel.value,
    ...(operation.value !== 'repair' && accountKey.value ? { account_key: accountKey.value } : {}),
    ...(operation.value !== 'repair' && selectedAccount.value?.attachment_revision
      ? { attachment_revision: selectedAccount.value.attachment_revision }
      : {}),
    ttl_seconds: 120,
  }
  const specification = {
    workspace_handle: workspace.value,
    agent_name: effectiveAgent.value,
    dispatch_profile_id: effectiveProfile.value?.id,
    dispatch_profile_version: effectiveProfile.value?.version,
    ticket_id: ticketId.value,
    work_shape: ticketId.value ? workShape.value : 'unknown',
    role: effectiveRole.value,
    parent_harness_session_id: parentId.value || null,
  }
  const worker = selectedWorker.value
  const mapping =
    worker && runtime.value.sessions.find((row) => row.session_id === worker.harness_session_id)
  pendingRequest.value = parseHabitatRequest(
    operation.value === 'repair'
      ? { ...common, operation: 'repair', repair_layer: repairLayer.value }
      : {
          ...common,
          ...specification,
          operation: operation.value,
          ...(existing.value && worker && mapping
            ? {
                session_id: worker.harness_session_id,
                session_generation: mapping.generation,
                expected_revision: worker.revision,
              }
            : {}),
        },
  )
  preparedIdentity = requestIdentity.value
  reviewing.value = true
}
async function submit() {
  if (
    !pendingRequest.value ||
    !props.projectId ||
    !eligible.value ||
    busy.value ||
    recoveryId ||
    (!attempted && (!canPrepare.value || preparedIdentity !== requestIdentity.value))
  )
    return
  savedAt ||= Date.now()
  if (!persist()) {
    feedback.value =
      'This browser cannot save recovery information. The request was not sent. Enable session storage and retry.'
    return
  }
  const version = generation,
    signal = controller!.signal
  busy.value = true
  attempted = true
  reviewing.value = false
  try {
    const result = await submitHabitatIntent(props.projectId, pendingRequest.value, signal)
    if (version !== generation || signal.aborted) return
    intent.value = result
    intentFresh.value = true
    recoveryId = result.id
    persist()
    feedback.value = 'Request recorded. Waiting for the runtime outcome.'
    emit('refresh')
  } catch (error) {
    if (version !== generation || signal.aborted) return
    if (error instanceof ApiError && [401, 403].includes(error.status)) {
      clearSavedEvidence()
      feedback.value =
        'Access is unavailable. Saved request evidence was cleared. No outcome is confirmed.'
    } else
      feedback.value =
        error instanceof ApiError && error.status === 409
          ? 'The selected runtime or binding changed. No new outcome is confirmed. Refresh evidence before reviewing another action.'
          : 'No outcome confirmed. Retry this exact request to recover its result; do not create a second start.'
  } finally {
    if (version === generation && !signal.aborted) busy.value = false
  }
}
async function check(cancel = false) {
  if (
    !pendingRequest.value ||
    !recoveryId ||
    !props.projectId ||
    busy.value ||
    !eligible.value ||
    (cancel &&
      (!intent.value ||
        !intentFresh.value ||
        !['requested', 'claimed'].includes(intent.value.state)))
  )
    return
  const version = generation,
    signal = controller!.signal
  busy.value = true
  try {
    const result = cancel
      ? await cancelHabitatIntent(intent.value!, pendingRequest.value, signal)
      : await loadHabitatIntent(props.projectId, recoveryId, pendingRequest.value, signal)
    if (version !== generation || signal.aborted) return
    if (
      intent.value &&
      (result.revision < intent.value.revision ||
        (terminal.value && JSON.stringify(result) !== JSON.stringify(intent.value)))
    )
      throw new Error('regressed intent')
    intent.value = result
    intentFresh.value = true
    persist()
    feedback.value = 'Request status refreshed.'
    emit('refresh')
  } catch (error) {
    if (version !== generation || signal.aborted) return
    intentFresh.value = false
    if (error instanceof ApiError && [401, 403, 404].includes(error.status)) {
      clearSavedEvidence()
      feedback.value =
        'This saved request is no longer accessible. Evidence was cleared. No execution outcome is confirmed.'
    } else
      feedback.value =
        'Connection or request evidence is unavailable. The outcome is unconfirmed. Reconnect and refresh; nothing will be repeated.'
  } finally {
    if (version === generation && !signal.aborted) busy.value = false
  }
}
function discardReview() {
  if (!intent.value && !attempted && !busy.value) {
    pendingRequest.value = null
    reviewing.value = false
    void nextTick(() => prepareRef.value?.focus())
  }
}
async function finishReview() {
  if (terminal.value && intentFresh.value && !busy.value) {
    clearSavedEvidence()
    await refreshRuntimes()
    await nextTick()
    const prepare = prepareRef.value
    if (prepare && !prepare.disabled) prepare.focus()
    else (operationRef.value ?? runtimeRef.value)?.focus()
  }
}
const poll = setInterval(() => {
  now.value = Date.now()
  const hasExpiredAdvertisement = runtimes.value.some(
    (row) => Date.parse(row.expires_at) <= now.value,
  )
  if (
    !document.hidden &&
    !refreshingRuntimes.value &&
    (hasExpiredAdvertisement || (runtimeId.value !== '' && !runtime.value)) &&
    now.value - lastRuntimeRefreshAt >= RUNTIME_REFRESH_COOLDOWN_MS
  )
    void refreshRuntimes()
  if (!document.hidden && recoveryId && (!terminal.value || !intentFresh.value)) void check()
}, 10_000)
onScopeDispose(() => {
  generation++
  controller?.abort()
  clearInterval(poll)
})
</script>
<template>
  <div class="habitat-stack" data-testid="habitat-lifecycle">
    <p v-if="!eligible">
      Browser lifecycle requires a signed-in super-admin and a verified project and instance. Use
      the guided CLI below when browser access is unavailable.
    </p>
    <template v-else>
      <p v-if="runtimeState === 'loading'" role="status">
        Finding available runtimes…
        <button type="button" :disabled="refreshingRuntimes" @click="refreshRuntimes">
          Refresh runtime evidence
        </button>
      </p>
      <div v-else-if="runtimeState !== 'ready'" class="habitat-error">
        <p>Runtime access is unavailable. Reconnect or check your access, then refresh.</p>
        <button type="button" @click="refreshRuntimes">Refresh runtime evidence</button>
      </div>
      <div v-else-if="!runtimes.length" class="habitat-card">
        <h3>No live advertised runtime</h3>
        <p>
          No eligible local runtime has connected for this project. Configure a runtime and refresh,
          or use the guided CLI below.
        </p>
        <button type="button" @click="refreshRuntimes">Refresh runtimes</button>
      </div>
      <div v-else class="habitat-form">
        <div class="habitat-actions">
          <button type="button" :disabled="refreshingRuntimes" @click="refreshRuntimes">
            {{ refreshingRuntimes ? 'Refreshing runtime evidence…' : 'Refresh runtime evidence' }}
          </button>
        </div>
        <label
          >Runtime<select
            ref="runtimeRef"
            v-model="runtimeId"
            aria-label="Runtime"
            :disabled="!!pendingRequest"
          >
            <option value="">Choose one runtime</option>
            <option v-for="row in runtimes" :key="row.id" :value="row.id">
              {{ row.machine_id }} · {{ habitatRuntimeClasses(row).map(humanize).join(' + ') }} · {{ row.id.slice(0, 8) }}
            </option>
          </select></label
        >
        <p v-if="runtimeId && !runtime">
          This runtime advertisement expired. Refresh before submitting.
        </p>
        <template v-if="runtime">
            <label v-if="accountChoices.length > 1 || accountChoices.some((choice) => choice.account_key)"
            >Account<select
              v-model="accountChoiceId"
              aria-label="Named account"
              :disabled="!!intent"
            >
              <option value="">Choose a configured account</option>
              <option
                v-for="choice in accountChoices"
                :key="habitatChoiceId(choice)"
                :value="habitatChoiceId(choice)"
              >
                {{ choice.label }} · {{ humanize(choice.account_label) }}
              </option>
            </select></label
          >
          <p v-if="(accountChoices.length > 1 || accountChoices.some((choice) => choice.account_key)) && !accountAvailable && (operation !== 'repair' || !accountLabel)">
            Choose one advertised account. Unsupported, forged or stale keys cannot be used.
          </p>
          <p v-if="runtime.account_scopes?.some((scope) => scope.account_availability === 'unavailable') && !accountChoices.length">
            No named account is attached in this runtime generation. Connect an enrolled account and restart the owned runtime to advertise it.
          </p>
          <p v-if="existing && accountAvailable && !ownedWorkers.length">
            No owned worker uses this account. Changing account cannot adopt an existing generation.
          </p>
          <p v-if="runtime && effectiveProfile && !profileAvailable">
            This profile is not advertised on the selected runtime.
          </p>
          <label
            >Operation<select
              ref="operationRef"
              v-model="operation"
              aria-label="Operation"
              :disabled="!!pendingRequest"
            >
              <option value="start">Start a new worker</option>
              <option value="attach">Attach an idle worker</option>
              <option value="reassign">Reassign an idle worker</option>
              <option value="restart">Restart a stopped worker</option>
              <option value="repair">Repair runtime connections</option>
            </select></label
          >
          <template v-if="operation !== 'repair'">
            <template v-if="existing">
              <label
                >Worker<select
                  v-model="sessionId"
                  aria-label="Lifecycle worker"
                  :disabled="!!pendingRequest"
                >
                  <option value="">
                    Choose an owned {{ operation === 'restart' ? 'stopped' : 'idle' }} worker
                  </option>
                  <option
                    v-for="worker in ownedWorkers"
                    :key="worker.harness_session_id"
                    :value="worker.harness_session_id"
                  >
                    {{ worker.agent.name }} · {{ worker.ticket?.key ?? 'No ticket' }} ·
                    {{ worker.harness_session_id.slice(0, 8) }}
                  </option>
                </select></label
              >
              <p v-if="!ownedWorkers.length">
                No eligible owned {{ operation === 'restart' ? 'terminal' : 'idle' }} generation is
                visible. Busy, unknown, unmapped or stale workers cannot be selected.
              </p>
              <p v-if="selectedWorker">
                {{ effectiveAgent }} · {{ effectiveProfile?.model ?? 'Profile unavailable' }} ·
                {{ humanize(effectiveRole ?? 'unknown') }}. This worker keeps its agent, profile,
                role and runtime.
              </p>
              <p v-if="candidateState !== 'ready' || candidateTruncated">
                Worker choices are
                {{
                  candidateState !== 'ready'
                    ? 'unavailable'
                    : 'limited to the first 100 visible workers'
                }}. Refresh to check current eligibility.
              </p>
            </template>
            <p v-if="!profileAvailable">
              {{
                existing
                  ? 'This worker’s exact profile is not advertised by the runtime.'
                  : 'Choose a profile above that this runtime supports.'
              }}
            </p>
            <label
              >Workspace<select
                v-model="workspace"
                aria-label="Workspace"
                :disabled="!!pendingRequest"
              >
                <option value="">Choose a verified workspace</option>
                <option
                  v-for="(row, index) in availableWorkspaces"
                  :key="row.handle"
                  :value="row.handle"
                >
                  {{ row.label || `Workspace ${index + 1}` }} · {{ row.identity.slice(0, 8) }}
                </option>
              </select></label
            >
            <small
              >Use the workspace label and fingerprint to identify where this worker belongs.
              {{
                existing
                  ? 'It must be the selected worker’s existing workspace; the server verifies the match.'
                  : 'The runtime verifies the workspace before starting.'
              }}</small
            >
            <label v-if="!existing"
              >Role<select v-model="role" :disabled="!!pendingRequest">
                <option value="worker">Worker</option>
                <option value="coordinator">Project coordinator</option>
              </select></label
            >
            <HabitatTicketPicker
              v-if="projectId"
              v-model="ticketId"
              :project-id="projectId"
              :authority="authority"
              :disabled="!!pendingRequest"
            />
            <label v-if="ticketId"
              >Work shape<select v-model="workShape" :disabled="!!pendingRequest">
                <option value="ship">Ship · product delivery</option>
                <option value="scout">Scout · investigation</option>
              </select></label
            >
            <label
              >Parent worker<select
                v-model="parentId"
                aria-label="Parent worker"
                :disabled="!!pendingRequest || candidateState !== 'ready'"
              >
                <option value="">No parent binding</option>
                <option
                  v-if="parentId && !parents.some((row) => row.harness_session_id === parentId)"
                  :value="parentId"
                  disabled
                >
                  Current parent · unavailable in these choices
                </option>
                <option
                  v-for="parent in parents"
                  :key="parent.harness_session_id"
                  :value="parent.harness_session_id"
                >
                  {{ parent.agent.name }} · {{ humanize(parent.role) }} ·
                  {{ parent.harness_session_id.slice(0, 8) }}
                </option>
              </select></label
            >
            <small v-if="!bindingChanged"
              >Choose a different ticket, work shape or parent to change this assignment.</small
            >
          </template>
          <label v-else
            >Repair layer<select v-model="repairLayer" :disabled="!!pendingRequest">
              <option value="reporter">Reporter</option>
              <option value="listeners">Listeners</option>
            </select></label
          >
          <details>
            <summary>Runtime details</summary>
            <p>
              {{ runtime.machine_id }} · {{ habitatRuntimeClasses(runtime).map(humanize).join(' + ')
              }}{{ selectedAccount ? ` · ${selectedAccount.label}` : '' }}. Advertisement expires
              {{ runtime.expires_at }}. A connected runtime does not prove a worker is ready.
            </p>
          </details>
          <button
            v-if="!pendingRequest"
            ref="prepareRef"
            type="button"
            :disabled="!canPrepare"
            @click="prepare"
          >
            Review {{ operation }} request
          </button>
        </template>
      </div>
      <div
        v-if="pendingRequest"
        class="habitat-card"
        role="group"
        aria-label="Lifecycle request review"
      >
        <h3>{{ attempted ? 'Saved' : 'Review' }} {{ pendingRequest.operation }} request</h3>
        <p v-if="pendingRequest.operation !== 'repair'">
          {{ pendingRequest.agent_name }} · {{ pendingRequest.dispatch_profile_id }}@{{
            pendingRequest.dispatch_profile_version
          }}
          · {{ humanize(pendingRequest.role) }} ·
          {{ pendingRequest.ticket_id ? `Ticket #${pendingRequest.ticket_id}` : 'No ticket' }} ·
          {{ humanize(pendingRequest.work_shape) }}.
        </p>
        <p v-else>
          Repair only the {{ pendingRequest.repair_layer }} layer. A runtime result is required to
          confirm completion.
        </p>
        <p v-if="['attach', 'reassign'].includes(pendingRequest.operation)">
          Apply this assignment to the selected idle generation. Its current work binding will be
          replaced.
        </p>
        <p v-if="pendingRequest.operation === 'restart'">
          Start a new generation for the selected owned, stopped worker. The terminal generation
          remains in history.
        </p>
        <p v-if="pendingRequest.operation === 'start'">
          Start a new worker on the selected runtime and workspace.
        </p>
        <details>
          <summary>Exact request details</summary>
          <dl class="habitat-facts">
            <dt>Project</dt>
            <dd>{{ projectId }}</dd>
            <dt>Runtime / generation</dt>
            <dd>{{ pendingRequest.runtime_id }} / {{ pendingRequest.runtime_generation }}</dd>
            <dt>Account</dt>
            <dd>
              {{
                pendingRequest.account_key
                  ? `${pendingRequest.account_key} · ${humanize(pendingRequest.account_label)}`
                  : humanize(pendingRequest.account_label)
              }}
            </dd>
            <template v-if="'attachment_revision' in pendingRequest && pendingRequest.attachment_revision">
              <dt>Attachment revision</dt>
              <dd>{{ pendingRequest.attachment_revision }}</dd>
            </template>
            <template v-if="pendingRequest.operation !== 'repair'"
              ><dt>Workspace handle</dt>
              <dd>{{ pendingRequest.workspace_handle }}</dd>
              <dt>Parent</dt>
              <dd>{{ pendingRequest.parent_harness_session_id ?? 'No parent' }}</dd></template
            ><template v-if="'session_id' in pendingRequest"
              ><dt>Worker / owned generation</dt>
              <dd>{{ pendingRequest.session_id }} / {{ pendingRequest.session_generation }}</dd>
              <dt>Expected revision</dt>
              <dd>{{ pendingRequest.expected_revision }}</dd></template
            >
            <dt>Request expires after</dt>
            <dd>{{ pendingRequest.ttl_seconds }} seconds</dd>
          </dl>
        </details>
        <div v-if="!intent && !recoveryId" class="habitat-actions">
          <button
            ref="confirmRef"
            type="button"
            :disabled="busy || (!attempted && !canPrepare)"
            class="habitat-primary"
            @click="submit"
          >
            {{ busy ? 'Submitting…' : 'Confirm exact request' }}</button
          ><button v-if="reviewing" type="button" :disabled="busy" @click="discardReview">
            Cancel review
          </button>
        </div>
        <p v-if="recovering" role="status">
          Checking the saved request with the server. Nothing is being repeated.
        </p>
        <button v-if="recoveryId && !intent" type="button" :disabled="busy" @click="check()">
          Reconnect and check request
        </button>
      </div>
      <div v-if="intent" class="habitat-card">
        <span class="habitat-eyebrow">{{
          intentFresh ? 'Request status' : 'Last confirmed status · connection unavailable'
        }}</span>
        <h3>{{ humanize(intent.state) }}</h3>
        <p>{{ humanize(intent.reason) }}</p>
        <p v-if="intentFresh && intent.state === 'completed'">
          Runtime completion recorded. Refresh workers to see current activity.
        </p>
        <p v-else-if="intent.state === 'expired'">
          The execution outcome may be unknown. Inspect the runtime before starting another
          generation.
        </p>
        <p v-else-if="intentFresh && intent.state === 'failed'">
          The intent failed. Review the reported reason and runtime before recovery.
        </p>
        <p v-else-if="intent.state === 'cancelled'">
          The pending request was cancelled before execution.
        </p>
        <p v-else>No completed execution is confirmed.</p>
        <details>
          <summary>Request record</summary>
          <p>Intent {{ intent.id }} · revision {{ intent.revision }} · {{ intent.updatedAt }}</p>
          <p v-if="intent.resultSessionId">Result worker: {{ intent.resultSessionId }}</p>
        </details>
        <div class="habitat-actions">
          <button type="button" :disabled="busy" @click="check()">Refresh intent evidence</button
          ><button
            v-if="intentFresh && ['requested', 'claimed'].includes(intent.state)"
            type="button"
            :disabled="busy"
            @click="check(true)"
          >
            Cancel pending request</button
          ><button
            v-if="terminal && intentFresh"
            type="button"
            :disabled="busy"
            @click="finishReview"
          >
            Finish reviewing this result
          </button>
        </div>
      </div>
      <p class="habitat-status" role="status">{{ feedback }}</p>
    </template>
  </div>
</template>
