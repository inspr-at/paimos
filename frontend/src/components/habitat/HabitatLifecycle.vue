<script setup lang="ts">
import { computed, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { ApiError } from '@/api/client'
import type { HarnessDispatchProfile } from '@/services/orchestrationTypes'
import { humanize } from './habitatModel'
import {
  loadHabitatRuntimes,
  submitHabitatIntent,
  loadHabitatIntent,
  cancelHabitatIntent,
  type HabitatRuntime,
  type HabitatIntent,
  type HabitatLifecycleRequest,
} from './habitatLifecycle'
const props = defineProps<{
  projectId: number | null
  agent: string
  profile: HarnessDispatchProfile | null
  authority: string
}>()
const emit = defineEmits<{ refresh: [] }>()
const auth = useAuthStore()
const runtimeState = ref<'loading' | 'ready' | 'unavailable' | 'unauthorized'>('loading')
const runtimes = shallowRef<HabitatRuntime[]>([])
const runtimeId = ref('')
const workspace = ref('')
const ticketId = ref('')
const workShape = ref<'ship' | 'scout'>('ship')
const role = ref<'worker' | 'coordinator'>('worker')
const operation = ref<'start' | 'repair'>('start')
const repairLayer = ref<'reporter' | 'listeners'>('reporter')
const pendingRequest = shallowRef<HabitatLifecycleRequest | null>(null)
const intent = shallowRef<HabitatIntent | null>(null)
const feedback = ref('')
const busy = ref(false)
const reviewing = ref(false)
const now = ref(Date.now())
let attempted = false
let preparedIdentity = ''
let generation = 0
let controller: AbortController | null = null
const eligible = computed(
  () => auth.isSuperAdmin && !auth.impersonation && props.projectId !== null,
)
const runtime = computed(
  () =>
    runtimes.value.find(
      (row) => row.id === runtimeId.value && Date.parse(row.expires_at) > now.value,
    ) ?? null,
)
const profileAvailable = computed(
  () =>
    !!props.profile &&
    !!runtime.value?.profiles.some(
      (profile) => profile.id === props.profile?.id && profile.version === props.profile?.version,
    ),
)
const ticketValid = computed(
  () =>
    !ticketId.value ||
    (/^[1-9]\d*$/.test(ticketId.value) && Number.isSafeInteger(Number(ticketId.value))),
)
const canPrepare = computed(
  () =>
    eligible.value &&
    runtime.value !== null &&
    (operation.value === 'repair' ||
      (profileAvailable.value &&
        /^[a-z][a-z0-9_-]{0,31}$/.test(props.agent) &&
        runtime.value.workspaces.some((w) => w.handle === workspace.value) &&
        ticketValid.value)),
)
const terminal = computed(
  () =>
    !!intent.value && ['completed', 'failed', 'expired', 'cancelled'].includes(intent.value.state),
)
const requestIdentity = computed(() =>
  JSON.stringify([
    props.agent,
    props.profile,
    runtimeId.value,
    workspace.value,
    ticketId.value,
    workShape.value,
    role.value,
    operation.value,
    repairLayer.value,
  ]),
)
function invalidate() {
  attempted = false
  generation++
  controller?.abort()
  controller = new AbortController()
  pendingRequest.value = null
  intent.value = null
  reviewing.value = false
  feedback.value = ''
  busy.value = false
  runtimeId.value = ''
  workspace.value = ''
  ticketId.value = ''
  runtimes.value = []
}
async function refreshRuntimes() {
  if (!eligible.value || !props.projectId) {
    runtimeState.value = 'unauthorized'
    return
  }
  const version = generation
  const signal = controller!.signal
  runtimeState.value = 'loading'
  try {
    const result = await loadHabitatRuntimes(props.projectId, signal)
    if (version !== generation || signal.aborted) return
    runtimes.value = result
    runtimeState.value = 'ready'
    now.value = Date.now()
  } catch (error) {
    if (version === generation && !signal.aborted) {
      runtimes.value = []
      runtimeState.value =
        error instanceof ApiError && [401, 403].includes(error.status)
          ? 'unauthorized'
          : 'unavailable'
    }
  }
}
watch(
  () => [props.authority, props.projectId],
  () => {
    invalidate()
    void refreshRuntimes()
  },
  { immediate: true, flush: 'sync' },
)
watch(requestIdentity, () => {
  reviewing.value = false
  if (!attempted) pendingRequest.value = null
})
function prepare() {
  if (!canPrepare.value || !runtime.value) return
  if (pendingRequest.value) {
    feedback.value = 'Check or cancel the existing request before starting another intent.'
    return
  }
  const common = {
    request_key: crypto.randomUUID(),
    runtime_id: runtime.value.id,
    runtime_generation: runtime.value.generation,
    account_label: runtime.value.account_label,
    ttl_seconds: 120,
  }
  pendingRequest.value =
    operation.value === 'repair'
      ? { ...common, operation: 'repair', repair_layer: repairLayer.value }
      : {
          ...common,
          operation: 'start',
          workspace_handle: workspace.value,
          agent_name: props.agent,
          dispatch_profile_id: props.profile!.id,
          dispatch_profile_version: props.profile!.version,
          ticket_id: ticketId.value ? Number(ticketId.value) : null,
          work_shape: ticketId.value ? workShape.value : 'unknown',
          role: role.value,
          parent_harness_session_id: null,
        }
  preparedIdentity = requestIdentity.value
  reviewing.value = true
}
async function submit() {
  if (
    !pendingRequest.value ||
    !props.projectId ||
    !eligible.value ||
    busy.value ||
    (!attempted && (!canPrepare.value || preparedIdentity !== requestIdentity.value))
  )
    return
  const version = generation
  const signal = controller!.signal
  busy.value = true
  attempted = true
  reviewing.value = false
  try {
    const result = await submitHabitatIntent(props.projectId, pendingRequest.value, signal)
    if (version !== generation || signal.aborted) return
    intent.value = result
    reviewing.value = false
    feedback.value = 'Intent recorded. Its durable state below is the execution evidence.'
    emit('refresh')
  } catch (error) {
    if (version !== generation || signal.aborted) return
    feedback.value =
      error instanceof ApiError && error.status === 409
        ? 'The selected runtime or binding changed. No new outcome is confirmed. Refresh evidence.'
        : 'No outcome confirmed. Retry this exact request to recover its idempotent result; do not create a second start.'
  } finally {
    if (version === generation && !signal.aborted) busy.value = false
  }
}
async function check(cancel = false) {
  if (!pendingRequest.value || !intent.value || !props.projectId || busy.value || !eligible.value)
    return
  const version = generation
  const signal = controller!.signal
  busy.value = true
  try {
    const result = cancel
      ? await cancelHabitatIntent(intent.value, pendingRequest.value, signal)
      : await loadHabitatIntent(props.projectId, intent.value.id, pendingRequest.value, signal)
    if (version !== generation || signal.aborted) return
    intent.value = result
    feedback.value = 'Durable lifecycle evidence refreshed.'
    emit('refresh')
  } catch {
    if (version === generation && !signal.aborted)
      feedback.value =
        'Evidence is unavailable or the revision changed. Execution outcome remains unconfirmed; refresh again.'
  } finally {
    if (version === generation && !signal.aborted) busy.value = false
  }
}
function discardReview() {
  if (!intent.value && !attempted && !busy.value) {
    pendingRequest.value = null
    reviewing.value = false
  }
}
const poll = setInterval(() => {
  now.value = Date.now()
  if (!document.hidden && intent.value && !terminal.value) void check()
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
      Browser lifecycle requests require a non-impersonated human super-admin session and an
      explicitly selected project. The CLI fallback remains below.
    </p>
    <template v-else>
      <p v-if="runtimeState === 'loading'" role="status">Reading advertised runtimes…</p>
      <div v-else-if="runtimeState !== 'ready'" class="habitat-error">
        <p>
          Browser lifecycle is unavailable for this account or deployment. No runtime is assumed
          online. Refresh or use the verified CLI fallback below.
        </p>
        <button type="button" @click="refreshRuntimes">Refresh runtime evidence</button>
      </div>
      <div v-else-if="!runtimes.length" class="habitat-card">
        <h3>No live advertised runtime</h3>
        <p>
          No local daemon has advertised an eligible project runtime. Process readiness is unknown.
          Configure a local runtime and refresh, or use the guided CLI fallback below.
        </p>
        <button type="button" @click="refreshRuntimes">Refresh runtimes</button>
      </div>
      <div v-else class="habitat-form">
        <label
          >Authenticated runtime<select v-model="runtimeId" :disabled="!!pendingRequest">
            <option value="">Choose one runtime</option>
            <option v-for="row in runtimes" :key="row.id" :value="row.id">
              {{ row.machine_id }} · {{ humanize(row.account_label) }} · {{ row.id.slice(0, 8) }}
            </option>
          </select></label
        >
        <p v-if="runtimeId && !runtime">
          This runtime advertisement expired. Refresh evidence before submitting.
        </p>
        <template v-if="runtime"
          ><p>
            Account: {{ humanize(runtime.account_label) }} · machine: {{ runtime.machine_id }}.
            Advertisement expires {{ runtime.expires_at }}. Advertisement is not process readiness.
          </p>
          <label
            >Operation<select v-model="operation" :disabled="!!pendingRequest">
              <option value="start">Start a fresh generation</option>
              <option value="repair">Repair a bounded runtime layer</option>
            </select></label
          >
          <template v-if="operation === 'start'"
            ><p v-if="!profileAvailable">
              Choose an immutable profile above that this runtime advertises. Unavailable
              combinations cannot be submitted.
            </p>
            <label
              >Workspace handle<select v-model="workspace" :disabled="!!pendingRequest">
                <option value="">Choose a verified workspace handle</option>
                <option v-for="row in runtime.workspaces" :key="row.handle" :value="row.handle">
                  {{ row.handle }} · identity {{ row.identity.slice(0, 12) }}
                </option>
              </select></label
            ><label
              >Role<select v-model="role" :disabled="!!pendingRequest">
                <option value="worker">Worker</option>
                <option value="coordinator">Project coordinator</option>
              </select></label
            ><label
              >Ticket ID (optional, server validates project ownership)<input
                v-model="ticketId"
                :disabled="!!pendingRequest"
                inputmode="numeric" /></label
            ><label v-if="ticketId"
              >Work shape<select v-model="workShape" :disabled="!!pendingRequest">
                <option value="ship">Ship</option>
                <option value="scout">Scout</option>
              </select></label
            >
            <p>
              No parent binding is added by this start. Assign an explicit same-project parent
              through the managed binding workflow.
            </p></template
          >
          <label v-else
            >Repair layer<select v-model="repairLayer" :disabled="!!pendingRequest">
              <option value="reporter">Reporter</option>
              <option value="listeners">Listeners</option>
            </select></label
          >
          <button v-if="!pendingRequest" type="button" :disabled="!canPrepare" @click="prepare">
            Review {{ operation }} request
          </button>
        </template>
      </div>
      <div
        v-if="pendingRequest && !intent"
        class="habitat-card"
        role="group"
        aria-label="Confirm lifecycle request"
      >
        <h3>Review {{ pendingRequest.operation }}</h3>
        <p>
          Project {{ projectId }} · runtime {{ pendingRequest.runtime_id }} · account
          {{ humanize(pendingRequest.account_label) }}.
        </p>
        <p v-if="pendingRequest.operation === 'start'">
          Agent {{ pendingRequest.agent_name }} · {{ pendingRequest.dispatch_profile_id }}@{{
            pendingRequest.dispatch_profile_version
          }}
          · {{ pendingRequest.role }} · ticket {{ pendingRequest.ticket_id ?? 'unbound' }} ·
          {{ pendingRequest.work_shape }}.
        </p>
        <p v-else>
          Repair only the {{ pendingRequest.repair_layer }} layer. Completion requires a real daemon
          outcome.
        </p>
        <div class="habitat-actions">
          <button
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
      </div>
      <div v-if="intent" class="habitat-card">
        <span class="habitat-eyebrow">Durable lifecycle intent</span>
        <h3>{{ humanize(intent.state) }}</h3>
        <p>
          {{ humanize(intent.reason) }} · revision {{ intent.revision }} · {{ intent.updatedAt }}
        </p>
        <p v-if="intent.state === 'completed'">
          Runtime completion recorded<template v-if="intent.resultSessionId">
            · public generation {{ intent.resultSessionId }}</template
          >. Refresh the worker projection for current activity.
        </p>
        <p v-else-if="intent.state === 'expired'">
          The execution outcome may be unknown. Inspect the runtime before starting another
          generation.
        </p>
        <p v-else-if="intent.state === 'failed'">
          The intent failed. Review the reported reason and runtime evidence before recovery.
        </p>
        <p v-else>No completed execution is claimed.</p>
        <small>Intent {{ intent.id }}</small>
        <div class="habitat-actions">
          <button type="button" :disabled="busy" @click="check()">Refresh intent evidence</button
          ><button
            v-if="['requested', 'claimed'].includes(intent.state)"
            type="button"
            :disabled="busy"
            @click="check(true)"
          >
            Cancel pending request
          </button>
        </div>
      </div>
      <p class="habitat-status" role="status">{{ feedback }}</p>
    </template>
  </div>
</template>
