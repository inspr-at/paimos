import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import { api, ApiError } from '@/api/client'
import { loadOrchestration } from '@/services/orchestration'
import { canControlWorker, type HabitatWorker } from '@/components/habitat/habitatModel'

export function useHabitatActions(options: {
  worker: Readonly<Ref<HabitatWorker | null>>
  authority: Readonly<Ref<string>>
  fresh: Readonly<Ref<boolean>>
  editable: Readonly<Ref<boolean>>
}) {
  const action = ref<'simple' | 'steer' | 'interrupt' | 'stop' | null>(null)
  const draft = ref('')
  const feedback = ref('')
  const busy = ref(false)
  const control = ref<{
    id: string
    kind: string
    state: string
    outcome: string | null
    reason: string | null
  } | null>(null)
  let revision: number | null = null
  let generation = 0
  let controller: AbortController | null = null
  let messageKey: string | null = null
  let submittedBody: string | null = null
  const allowed = (capability: keyof HabitatWorker['capabilities']) =>
    !!options.worker.value &&
    canControlWorker(options.worker.value, capability, options.editable.value, options.fresh.value)
  const signature = computed(
    () => `${options.authority.value}/${options.worker.value?.harness_session_id ?? ''}`,
  )
  function clear() {
    generation++
    controller?.abort()
    action.value = null
    draft.value = ''
    feedback.value = ''
    busy.value = false
    control.value = null
    messageKey = null
    submittedBody = null
  }
  watch(signature, clear, { flush: 'sync' })
  watch(
    () => options.worker.value?.revision,
    () => {
      if (action.value && revision !== options.worker.value?.revision && !busy.value) {
        action.value = null
        feedback.value =
          'Worker evidence changed. Review its current revision before requesting a control.'
      }
    },
  )
  function prepare(kind: NonNullable<typeof action.value>) {
    if (!allowed(kind === 'simple' ? 'inbox' : kind)) return
    action.value = kind
    revision = options.worker.value!.revision
    feedback.value = ''
  }
  async function submit() {
    const worker = options.worker.value
    const kind = action.value
    if (!worker || !kind || busy.value || !allowed(kind === 'simple' ? 'inbox' : kind)) return
    if ((kind === 'simple' || kind === 'steer') && !draft.value.trim()) return
    const version = ++generation
    controller?.abort()
    controller = new AbortController()
    const signal = controller.signal
    const context = signature.value
    const current = () => !signal.aborted && version === generation && context === signature.value
    busy.value = true
    feedback.value = ''
    try {
      // Existing controls name the immutable generation. Refresh the projection
      // before issuing a request; the server independently enforces ownership.
      const projectionRequest = { projectId: worker.project.id, zoom: '100', signal }
      const latest = await loadOrchestration(projectionRequest)
      if (!current()) return
      const next = latest.fleet.workers.find(
        (row) => row.harness_session_id === worker.harness_session_id,
      )
      if (
        !next ||
        next.revision !== revision ||
        !canControlWorker(
          next,
          kind === 'simple' ? 'inbox' : kind,
          options.editable.value,
          options.fresh.value,
        )
      )
        throw new ApiError(409, 'stale worker')
      if (kind === 'simple' || kind === 'steer') {
        const body = draft.value.trim()
        const peers = latest.fleet.workers.filter(
          (row) =>
            row.agent.id === worker.agent.id &&
            !['dead'].includes(row.liveness.state) &&
            row.phase !== 'stopped',
        )
        if (peers.length !== 1 || latest.fleet.sample_truncated)
          throw new ApiError(409, 'ambiguous recipient')
        if (submittedBody !== body || !messageKey) {
          messageKey = crypto.randomUUID()
          submittedBody = body
        }
        const raw = await api.post<unknown>(
          `/v2/projects/${worker.project.id}/messages`,
          {
            to: `${worker.harness}:${worker.agent.name}`,
            body,
            delivery_level: kind,
            issue_id: worker.ticket?.id ?? null,
          },
          { signal, headers: { 'Idempotency-Key': messageKey } },
        )
        if (!current()) return
        const result = raw as { message_id?: unknown; delivered?: unknown }
        if (
          !result ||
          typeof result.message_id !== 'string' ||
          typeof result.delivered !== 'boolean'
        )
          throw new Error('invalid acknowledgement')
        feedback.value =
          'Message recorded. Delivery and execution are separate; refresh recent communication for evidence.'
        draft.value = ''
        messageKey = null
        submittedBody = null
      } else {
        const raw = await api.post<unknown>(
          `/projects/${worker.project.id}/harness-sessions/${encodeURIComponent(worker.harness_session_id)}/controls/${kind}`,
          {},
          { signal },
        )
        if (!current()) return
        const result = raw as {
          id?: unknown
          harness_session_id?: unknown
          kind?: unknown
          state?: unknown
        }
        if (
          !result ||
          typeof result.id !== 'string' ||
          !/^[0-9a-f-]{36}$/.test(result.id) ||
          result.harness_session_id !== worker.harness_session_id ||
          result.kind !== kind ||
          !['pending', 'claimed', 'applied', 'rejected'].includes(String(result.state))
        )
          throw new Error('invalid control acknowledgement')
        control.value = {
          id: result.id,
          kind,
          state: String(result.state),
          outcome: null,
          reason: null,
        }
        feedback.value =
          'Control requested. Waiting for owned runtime evidence; completion is not yet confirmed.'
      }
      action.value = null
    } catch (error) {
      if (!current()) return
      feedback.value =
        error instanceof ApiError && error.status === 409
          ? 'Worker revision or recipient changed. Refresh and review before trying again.'
          : error instanceof ApiError && [401, 403, 404].includes(error.status)
            ? 'This action is no longer authorized or available. Refresh access and ownership evidence.'
            : 'No successful outcome is confirmed. Refresh the evidence before retrying.'
    } finally {
      if (current()) busy.value = false
    }
  }
  async function checkControl() {
    const worker = options.worker.value
    const requested = control.value
    if (!worker || !requested || busy.value) return
    const version = ++generation
    controller?.abort()
    controller = new AbortController()
    const signal = controller.signal
    busy.value = true
    try {
      const raw = await api.get<unknown>(
        `/projects/${worker.project.id}/harness-sessions/${encodeURIComponent(worker.harness_session_id)}/controls/${requested.id}`,
        { signal },
      )
      if (signal.aborted || version !== generation) return
      const result = raw as Record<string, unknown>
      if (
        !result ||
        result.id !== requested.id ||
        result.project_id !== worker.project.id ||
        result.harness_session_id !== worker.harness_session_id ||
        result.kind !== requested.kind ||
        !['pending', 'claimed', 'applied', 'rejected'].includes(String(result.state))
      )
        throw new Error('invalid outcome')
      control.value = {
        ...requested,
        state: String(result.state),
        outcome: typeof result.outcome === 'string' ? result.outcome : null,
        reason: typeof result.reason === 'string' ? result.reason : null,
      }
      feedback.value = 'Control evidence refreshed.'
    } catch {
      if (!signal.aborted && version === generation)
        feedback.value = 'Control outcome is unavailable. The last confirmed record remains below.'
    } finally {
      if (!signal.aborted && version === generation) busy.value = false
    }
  }
  onScopeDispose(clear)
  return { action, draft, feedback, busy, control, allowed, prepare, submit, checkControl }
}
