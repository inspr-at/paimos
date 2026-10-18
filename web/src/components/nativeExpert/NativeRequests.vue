<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import { bindingKey } from '../../lib/nativeExpert/ports'
import type { NativeApproval, NativeBinding, NativeSessionView } from '../../lib/nativeSessionContract'

// I2 provides current authenticated identity and the existing desk opener.
// This preview never calls a vendor, approves a tool or changes permissions.
const props = withDefaults(defineProps<{
  session: NativeSessionView
  viewer: { tenantId: string; principalId: string; kind: 'person' | 'agent'; canRead: boolean; canDecide: boolean }
  requests: readonly NativeApproval[]
  active: boolean
  /** Monotonic time captured before fetching this server-derived TTL batch. */
  requestStartedAt: number
  reviewLabel?: string
  review: (request: Readonly<{ principalId: string; binding: NativeBinding; approvalId: string; requestId: string; deskItemId: string; revision: number; effectDigest: string }>, signal: AbortSignal) => Promise<{ binding: NativeBinding; approvalId: string; requestId: string; deskItemId: string }>
}>(), { reviewLabel: 'Review in Decision Desk' })

const emit = defineEmits<{ reviewed: [approval: NativeApproval] }>()
const index = ref(0), busy = ref(false), feedback = ref(''), tick = ref(performance.now())
let pending: AbortController | null = null, timer: ReturnType<typeof setInterval> | undefined
const identity = computed(() => JSON.stringify([props.viewer.tenantId, props.viewer.principalId, props.viewer.kind,
  props.session.owned_session_id, props.session.owner_person_id, bindingKey(props.session.current_binding), props.active,
  props.viewer.canRead, props.viewer.canDecide, props.session.revision]))
const readable = computed(() => props.active && props.viewer.canRead && props.viewer.tenantId === props.session.current_binding?.tenant_id &&
  props.session.current_binding?.owned_session_id === props.session.owned_session_id)
const bounded = computed(() => props.requests.length <= 32 && props.requests.every(request => typeof request.summary === 'string' &&
  new TextEncoder().encode(request.summary).length <= 2048 && request.allowed_decisions.length <= 3))
const requests = computed(() => readable.value && bounded.value ? props.requests.filter(request =>
  bindingKey(request.binding) === bindingKey(props.session.current_binding) && request.desk_item_id && request.approval_id && request.request_id &&
  Number.isFinite(request.expires_in_ms) && request.expires_in_ms > 0 && request.expires_in_ms <= 300000 &&
  request.allowed_decisions.every(decision => ['allow_once', 'deny', 'answer'].includes(decision))) : [])
const current = computed(() => requests.value[index.value] ?? null)
const seriesKey = computed(() => JSON.stringify(requests.value.map(request => [request.approval_id, request.request_id, request.effect_digest, request.turn_ref, request.desk_item_id])))
const requestKey = computed(() => current.value ? JSON.stringify([identity.value, current.value.approval_id, current.value.request_id,
  current.value.effect_digest, current.value.turn_ref, current.value.desk_item_id, props.requestStartedAt]) : identity.value)
const expired = computed(() => !current.value || !Number.isFinite(props.requestStartedAt) || props.requestStartedAt > tick.value ||
  tick.value >= props.requestStartedAt + current.value.expires_in_ms)
const canReview = computed(() => readable.value && props.viewer.kind === 'person' && props.viewer.canDecide && !!current.value && !expired.value)
const status = computed(() => !readable.value ? 'This session cannot be read.' : !bounded.value ? 'The request list exceeds its limits. Reload the current session.' :
  !current.value ? 'No pending requests for this runtime.' : props.viewer.kind !== 'person' ? 'A signed-in person must decide this request.' :
  !props.viewer.canDecide ? 'Decision rights are required.' : expired.value ? 'This request expired. Wait for a new request.' :
  current.value.step_up_required ? 'This request needs the existing human verification in Decision Desk.' : 'Only a decision for this exact request is accepted.')

function cancel() { pending?.abort(); pending = null; busy.value = false; feedback.value = '' }
watch(identity, () => { cancel(); index.value = 0 }, { flush: 'sync' })
watch(seriesKey, () => { cancel(); index.value = Math.min(index.value, Math.max(0, requests.value.length - 1)) }, { flush: 'sync' })
watch(requestKey, cancel, { flush: 'sync' })
function move(delta: number) { index.value = Math.max(0, Math.min(requests.value.length - 1, index.value + delta)) }
async function review() {
  tick.value = performance.now()
  if (!canReview.value || busy.value) return
  const source = current.value!
  const approval = { ...source, binding: { ...source.binding }, allowed_decisions: [...source.allowed_decisions] }
  const key = requestKey.value, revision = props.session.revision, actor = props.viewer.principalId
  const abort = pending = new AbortController()
  const request = Object.freeze({ principalId: actor, binding: Object.freeze({ ...approval.binding }), approvalId: approval.approval_id,
    requestId: approval.request_id, deskItemId: approval.desk_item_id!, revision, effectDigest: approval.effect_digest })
  const live = () => pending === abort && !abort.signal.aborted && key === requestKey.value && canReview.value && props.session.revision === revision
  busy.value = true; feedback.value = ''
  try {
    const result = await props.review(request, abort.signal)
    tick.value = performance.now()
    if (!live()) return
    if (bindingKey(result.binding) !== bindingKey(request.binding) || result.approvalId !== request.approvalId ||
      result.requestId !== request.requestId || result.deskItemId !== request.deskItemId) {
      feedback.value = 'The request changed. Reopen the current request.'; return
    }
    feedback.value = 'Request opened in Decision Desk.'
    emit('reviewed', approval)
  } catch {
    // Adapter bodies may contain private native payloads; show no raw errors.
    if (live()) feedback.value = 'Decision Desk could not be opened. Try again.'
  } finally { if (pending === abort) { pending = null; busy.value = false } }
}
onMounted(() => { timer = setInterval(() => { tick.value = performance.now() }, 250) })
onBeforeUnmount(() => { cancel(); clearInterval(timer) })
</script>

<template>
  <section class="native-requests" aria-label="Session requests">
    <div class="request-controls" role="toolbar" aria-label="Request navigation">
      <div class="request-navigation" role="group" aria-label="Request series">
        <button class="btn" type="button" aria-label="Previous request" title="Previous request" :aria-disabled="index === 0" @click="move(-1)"><AppIcon name="chevron-left" :size="16" /></button>
        <span class="request-count">{{ requests.length ? index + 1 : 0 }} of {{ requests.length }}</span>
        <button class="btn" type="button" aria-label="Next request" title="Next request" :aria-disabled="index >= requests.length - 1" @click="move(1)"><AppIcon name="chevron-right" :size="16" /></button>
      </div>
      <button class="btn" type="button" :aria-disabled="!canReview || busy" @click="review"><AppIcon name="external" :size="15" /><span>{{ reviewLabel }}</span></button>
    </div>
    <p class="request-status" role="status">{{ feedback || status }}</p>
    <div v-if="current" :key="requestKey" class="request-content">
      <h3>{{ current.kind === 'question' ? 'Question' : 'Permission request' }}</h3>
      <p class="request-summary">{{ current.summary }}</p>
      <p class="request-boundary">{{ current.kind === 'question' ? 'An exact answer applies to this request only.' : 'Allow once or deny. Broader permissions need their own human control.' }}</p>
    </div>
  </section>
</template>

<style scoped>
.native-requests { min-width: 0; display: flex; flex-direction: column; background: var(--surface); color: var(--ink); container: native-requests / inline-size; }
.request-controls { display: flex; align-items: stretch; gap: 12px; flex: none; padding: 12px; border-bottom: 1px solid var(--line); }
.request-navigation { display: flex; align-items: center; gap: 6px; flex: none; }
.request-navigation .btn { width: 44px; height: 44px; padding: 0; display: inline-flex; justify-content: center; align-items: center; }
.request-count { min-width: 7ch; text-align: center; white-space: nowrap; font-size: 12px; font-variant-numeric: tabular-nums; color: var(--ink-2); }
.request-controls > .btn { display: inline-flex; align-items: center; justify-content: center; gap: 8px; min-height: 44px; flex: 1; white-space: normal; overflow-wrap: anywhere; }
.btn[aria-disabled="true"] { opacity: .5; cursor: default; }
.request-status { margin: 0; padding: 12px; color: var(--ink-2); font-size: 12px; line-height: 1.5; }
.request-content { min-height: 0; padding: 0 12px 16px; overflow: auto; overscroll-behavior: contain; overflow-wrap: anywhere; }
.request-content h3 { font-size: 14px; font-weight: 600; margin: 0 0 12px; }
.request-summary { margin: 0; white-space: pre-wrap; font-size: 13px; line-height: 1.6; }
.request-boundary { color: var(--ink-3); font-size: 12px; line-height: 1.5; margin-top: 16px; }
@container native-requests (max-width: 440px) { .request-controls { flex-direction: column; gap: 8px; } .request-navigation { justify-content: space-between; } }
@media (max-width: 600px) { .native-requests { flex: 1; min-height: 0; max-height: 100dvh; padding-bottom: env(safe-area-inset-bottom); } .request-content { flex: 1; } }
</style>
