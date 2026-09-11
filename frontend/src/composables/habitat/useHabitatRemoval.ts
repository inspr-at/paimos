import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import { ApiError } from '@/api/client'
import {
  requestHabitatControl,
  loadHabitatControl,
  type HabitatControl,
} from '@/components/habitat/habitatControls'
import {
  canFinishThenRemove,
  canOpenRemovalDialog,
  canStopNowRemove,
  loadHabitatRetirement,
  requestHabitatRetirement,
  type HabitatRetirement,
  type RemovalMode,
} from '@/components/habitat/habitatRemoval'
import {
  clearIntentRecovery,
  findLifecycleDraftForSession,
} from '@/components/habitat/habitatRecovery'
import type { HabitatWorker } from '@/components/habitat/habitatModel'

export function useHabitatRemoval(options: {
  worker: Readonly<Ref<HabitatWorker | null>>
  authority: Readonly<Ref<string>>
  fresh: Readonly<Ref<boolean>>
  editable: Readonly<Ref<boolean>>
  principalId: Readonly<Ref<number | null>>
}) {
  const open = ref(false)
  const mode = ref<RemovalMode | null>(null)
  const feedback = ref('')
  const busy = ref(false)
  const control = ref<HabitatControl | HabitatRetirement | null>(null)
  const draftScan = ref(0)
  let controlKey: string | null = null
  let revision: number | null = null
  let generation = 0
  let controller: AbortController | null = null

  const lifecycleDraft = computed(() => {
    const _draftScan = draftScan.value
    const worker = options.worker.value
    const principal = options.principalId.value
    if (!worker || principal === null) return null
    return findLifecycleDraftForSession(principal, worker.project.id, worker.harness_session_id)
  })
  function refreshLifecycleDraft() {
    draftScan.value++
  }
  const draftOnlyAvailable = computed(() => lifecycleDraft.value !== null)
  const allowed = computed(() => {
    const worker = options.worker.value
    if (!worker) return false
    return canOpenRemovalDialog(
      worker,
      options.editable.value,
      options.fresh.value,
      draftOnlyAvailable.value,
    )
  })
  const stopNowAvailable = computed(() => {
    const worker = options.worker.value
    return worker ? canStopNowRemove(worker, options.editable.value, options.fresh.value) : false
  })
  const finishThenRemoveAvailable = computed(() => {
    const worker = options.worker.value
    return worker ? canFinishThenRemove(worker, options.editable.value, options.fresh.value) : false
  })
  const handoffAvailable = computed(() => false)
  const signature = computed(
    () => `${options.authority.value}/${options.worker.value?.harness_session_id ?? ''}`,
  )

  function clear() {
    generation++
    controller?.abort()
    open.value = false
    mode.value = null
    feedback.value = ''
    busy.value = false
    control.value = null
    controlKey = null
    revision = null
  }

  watch(signature, clear, { flush: 'sync' })
  watch(
    () => options.worker.value?.revision,
    () => {
      if (open.value && revision !== options.worker.value?.revision && !busy.value) {
        mode.value = null
        feedback.value =
          'Worker evidence changed. Review its current revision before requesting removal.'
      }
    },
  )

  function begin() {
    if (!allowed.value) return
    refreshLifecycleDraft()
    open.value = true
    mode.value = null
    revision = options.worker.value!.revision
    feedback.value = ''
    control.value = null
    controlKey = null
  }

  function choose(next: RemovalMode) {
    const worker = options.worker.value
    if (!worker) return
    if (next === 'stop_now' && !stopNowAvailable.value) return
    if (next === 'draft_only' && !draftOnlyAvailable.value) return
    if (next === 'finish_then_remove' && !finishThenRemoveAvailable.value) return
    if (next === 'handoff') return
    if (mode.value !== next) controlKey = null
    mode.value = next
    revision = options.worker.value!.revision
    feedback.value = ''
    if (next === 'stop_now' || next === 'finish_then_remove') controlKey ??= crypto.randomUUID()
  }

  function cancel() {
    if (busy.value) return
    clear()
  }

  async function requestStop(
    worker: HabitatWorker,
    signal: AbortSignal,
    context: string,
    version: number,
  ) {
    const result = await requestHabitatControl(
      worker.project.id,
      worker.harness_session_id,
      'stop',
      revision!,
      controlKey!,
      signal,
    )
    if (signal.aborted || version !== generation || context !== signature.value) return
    control.value = result
    controlKey = null
    feedback.value =
      'Stop requested. Waiting for owned runtime control evidence; public stopped state alone does not prove local reaping.'
    open.value = false
    mode.value = null
  }

  async function requestRetirement(
    worker: HabitatWorker,
    signal: AbortSignal,
    context: string,
    version: number,
  ) {
    const result = await requestHabitatRetirement(
      worker.project.id,
      worker.harness_session_id,
      revision!,
      controlKey!,
      signal,
    )
    if (signal.aborted || version !== generation || context !== signature.value) return
    control.value = result
    controlKey = null
    feedback.value =
      'Retirement requested. New work is fenced while the owned runtime finishes its current turn and settles accepted work. Completion still requires an owned stop receipt and exact stopped-generation proof.'
    open.value = false
    mode.value = null
  }

  async function submit() {
    const worker = options.worker.value
    const selected = mode.value
    if (!worker || !selected || busy.value) return
    if (selected === 'stop_now' && !stopNowAvailable.value) return
    if (selected === 'finish_then_remove' && !finishThenRemoveAvailable.value) return
    if (selected === 'draft_only' && !draftOnlyAvailable.value) return

    const version = ++generation
    controller?.abort()
    controller = new AbortController()
    const signal = controller.signal
    const context = signature.value
    const current = () => !signal.aborted && version === generation && context === signature.value
    busy.value = true
    feedback.value = ''
    try {
      if (selected === 'draft_only') {
        const draft = lifecycleDraft.value
        if (!draft) return
        clearIntentRecovery(draft.scope)
        refreshLifecycleDraft()
        if (!current()) return
        feedback.value =
          'Local lifecycle review draft cleared. No API was called and the owned process is unchanged.'
        open.value = false
        mode.value = null
        return
      }

      if (selected === 'stop_now') {
        await requestStop(worker, signal, context, version)
      } else if (selected === 'finish_then_remove') {
        await requestRetirement(worker, signal, context, version)
      }
    } catch (error) {
      if (!current()) return
      feedback.value =
        error instanceof ApiError && error.status === 409
          ? 'Worker revision changed. Refresh and review before trying again.'
          : error instanceof ApiError && [401, 403, 404].includes(error.status)
            ? 'This removal is no longer authorized or available. Refresh access and ownership evidence.'
            : 'No successful removal outcome is confirmed. Refresh the evidence before retrying.'
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
      const result =
        requested.kind === 'retire_after_work'
          ? await loadHabitatRetirement(
              worker.project.id,
              worker.harness_session_id,
              requested,
              signal,
            )
          : await loadHabitatControl(
              worker.project.id,
              worker.harness_session_id,
              requested,
              signal,
            )
      if (signal.aborted || version !== generation) return
      control.value = result
      feedback.value =
        result.kind === 'retire_after_work' && result.state === 'completed'
          ? 'Retirement completed with owned stop and stopped-generation evidence.'
          : result.kind === 'retire_after_work' &&
              (result.state === 'failed' || result.state === 'outcome_unknown')
            ? 'Retirement did not establish a safe completed removal. Review the durable outcome before retrying.'
            : 'Removal control evidence refreshed.'
    } catch {
      if (!signal.aborted && version === generation)
        feedback.value = 'Control outcome is unavailable. The last confirmed record remains below.'
    } finally {
      if (!signal.aborted && version === generation) busy.value = false
    }
  }

  onScopeDispose(clear)
  return {
    open,
    mode,
    feedback,
    busy,
    control,
    allowed,
    draftOnlyAvailable,
    stopNowAvailable,
    finishThenRemoveAvailable,
    handoffAvailable,
    lifecycleDraft,
    begin,
    choose,
    cancel,
    submit,
    checkControl,
  }
}
