import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import { ApiError } from '@/api/client'
import {
  requestHabitatControl,
  loadHabitatControl,
  sendHabitatMessage,
  type HabitatControl,
} from '@/components/habitat/habitatControls'
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
  const control = ref<HabitatControl | null>(null)
  let controlKey: string | null = null
  let revision: number | null = null
  let generation = 0
  let controller: AbortController | null = null
  let messageKey: string | null = null
  let submittedBody: string | null = null
  let submittedLevel: 'simple' | 'steer' | null = null
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
    controlKey = null
    submittedBody = null
    submittedLevel = null
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
    if (kind !== action.value || revision !== options.worker.value!.revision) controlKey = null
    if (kind === 'interrupt' || kind === 'stop') controlKey ??= crypto.randomUUID()
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
      if (kind === 'simple' || kind === 'steer') {
        const latest = await loadOrchestration({
          projectId: worker.project.id,
          zoom: '100',
          signal,
        })
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
        const body = draft.value.trim()
        const peers = latest.fleet.workers.filter(
          (row) =>
            row.agent.id === worker.agent.id &&
            !['dead'].includes(row.liveness.state) &&
            row.phase !== 'stopped',
        )
        if (peers.length !== 1 || latest.fleet.sample_truncated)
          throw new ApiError(409, 'ambiguous recipient')
        if (submittedBody !== body || submittedLevel !== kind || !messageKey) {
          messageKey = `utt_${crypto.randomUUID().replace(/-/g, '')}`
          submittedBody = body
          submittedLevel = kind
        }
        await sendHabitatMessage(
          worker.project.id,
          worker.harness_session_id,
          revision!,
          messageKey,
          body,
          kind,
          signal,
        )
        if (!current()) return
        feedback.value =
          'Message recorded. Delivery and execution are separate; refresh recent communication for evidence.'
        draft.value = ''
        messageKey = null
        submittedBody = null
        submittedLevel = null
      } else {
        // Revision and ownership are checked atomically by the versioned endpoint.
        const result = await requestHabitatControl(
          worker.project.id,
          worker.harness_session_id,
          kind,
          revision!,
          controlKey!,
          signal,
        )
        if (!current()) return
        control.value = result
        controlKey = null
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
      const result = await loadHabitatControl(
        worker.project.id,
        worker.harness_session_id,
        requested,
        signal,
      )
      if (signal.aborted || version !== generation) return
      control.value = result
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
