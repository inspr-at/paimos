<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { can, onAccessChange } from '../../lib/authz'
import { familyName, listReviews, requestReview, reviewLabel, reviewSkips, reviewUsage, type ReviewFamily, type ReviewRequest, type TicketReview } from '../../lib/reviews'

const props = defineProps<{ nodeId: string; projectId: string }>()
const reviews = ref<TicketReview[]>([])
const error = ref('')
const sending = ref(false)
const formOpen = ref(false)
const repository = ref('')
const base = ref('')
const head = ref('')
const authorFamily = ref<ReviewFamily | ''>('')
const authorRun = ref('')
const pullRequest = ref('')
const requestId = ref(crypto.randomUUID())
const write = computed(() => can('work_orders.write', props.projectId) && can('run.create', props.projectId))
const read = computed(() => can('work_orders.read', props.projectId))
const valid = computed(() => /^[\w.-]+\/[\w.-]+$/.test(repository.value) && /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/.test(base.value)
  && /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/.test(head.value) && base.value !== head.value && (authorFamily.value || authorRun.value)
  && (!pullRequest.value || /^[1-9][0-9]*$/.test(pullRequest.value)))
let generation = 0
let disposed = false
let abort: AbortController | undefined
let submitAbort: AbortController | undefined
let submittedBody: ReviewRequest | undefined

async function load() {
  if (!read.value || disposed || sending.value) return
  const id = props.nodeId
  const turn = ++generation
  abort?.abort()
  const currentAbort = new AbortController()
  abort = currentAbort
  try {
    const next = await listReviews(id, currentAbort.signal)
    if (turn === generation && id === props.nodeId && !disposed) { reviews.value = next; error.value = '' }
  } catch (cause) {
    if (turn === generation && !disposed && !currentAbort.signal.aborted) error.value = cause instanceof Error ? cause.message : 'Reviews could not be loaded.'
  }
}
function reset() {
  generation++; abort?.abort(); submitAbort?.abort(); reviews.value = []; error.value = ''; sending.value = false
  formOpen.value = false; repository.value = ''; base.value = ''; head.value = ''; authorFamily.value = ''; authorRun.value = ''; pullRequest.value = ''
  requestId.value = crypto.randomUUID(); submittedBody = undefined
}
watch(() => [props.nodeId, read.value], () => { reset(); void load() }, { immediate: true })
const poll = setInterval(() => { if (reviews.value.some(r => ['queued', 'starting', 'running', 'waiting'].includes(r.status))) void load() }, 15_000)
const stopAccess = onAccessChange(change => { if (change === 'reset') reset(); else void load() })
onBeforeUnmount(() => { disposed = true; generation++; abort?.abort(); submitAbort?.abort(); clearInterval(poll); stopAccess() })

async function submit() {
  if (!valid.value || !write.value || sending.value) return
  const id = props.nodeId
  const turn = ++generation
  abort?.abort()
  sending.value = true; error.value = ''
  submitAbort = new AbortController()
  const body: ReviewRequest = { request_id: requestId.value, repository: repository.value.trim(), base_sha: base.value, head_sha: head.value,
    ...(authorRun.value ? { author_run_id: authorRun.value.trim() } : { author_family: authorFamily.value as ReviewFamily }),
    ...(pullRequest.value ? { pull_request: Number(pullRequest.value) } : {}) }
  // After an uncertain write, retain its id and payload. Editing the form
  // creates an explicit new request; repeating the same form replays safely.
  if (submittedBody && JSON.stringify({ ...submittedBody, request_id: '' }) !== JSON.stringify({ ...body, request_id: '' })) body.request_id = requestId.value = crypto.randomUUID()
  submittedBody = body
  try {
    const result = await requestReview(id, body, submitAbort.signal)
    if (turn !== generation || disposed || id !== props.nodeId) return
    reviews.value = [result, ...reviews.value.filter(r => r.work_order_id !== result.work_order_id)]
    formOpen.value = false; requestId.value = crypto.randomUUID(); submittedBody = undefined
  } catch (cause) {
    if (turn === generation && !disposed && id === props.nodeId) error.value = cause instanceof Error ? cause.message : 'Review could not be requested.'
  } finally { if (turn === generation && !disposed) sending.value = false }
}
</script>

<template>
  <section v-if="read && (reviews.length || write || error)" class="reviews" aria-label="Cross-family review">
    <h3 class="eyebrow">Cross-family review</h3>
    <p v-if="error" class="note" role="alert">{{ error }} <button type="button" class="text-action" @click="load">Try again</button></p>
    <p v-if="!reviews.length && !error" class="note">No commit range has been reviewed.</p>
    <ol v-if="reviews.length" class="review-list">
      <li v-for="review in reviews.slice(0, 5)" :key="review.work_order_id" class="review" :class="{ passed: review.gate_open }">
        <div class="top"><strong>{{ reviewLabel(review) }}</strong><span v-if="review.reviewer_family" class="model" :title="[review.reviewer_model, review.reviewer_effort].filter(Boolean).join(' · ')">{{ familyName(review.reviewer_family) }}<span v-if="review.reviewer_model"> · {{ review.reviewer_model }}</span></span></div>
        <p class="range" :title="`${review.repository} · ${review.base_sha}...${review.head_sha}`"><span>{{ review.repository }}</span><code>{{ review.base_sha.slice(0, 8) }}…{{ review.head_sha.slice(0, 8) }}</code></p>
        <p v-if="!review.gate_open && !(review.status === 'completed' && review.result.verdict === 'changes')" class="note reason">{{ review.gate_reason }}</p>
        <ul v-if="review.result.findings.length" class="findings" aria-label="Review findings">
          <li v-for="(finding, index) in review.result.findings" :key="index"><div class="location"><span class="severity">{{ finding.severity }}</span><code>{{ finding.file }}:{{ finding.line }}</code></div><p>{{ finding.message }}</p></li>
        </ul>
        <p v-if="reviewUsage(review)" class="usage">{{ reviewUsage(review) }}</p>
        <p v-if="review.github_status === 'error'" class="note">GitHub status could not be posted; reporting will retry.</p>
        <p v-else-if="review.github_status === 'stale'" class="note">The pull request moved to another commit. Request a review of its new head.</p>
        <details v-if="reviewSkips(review).length" class="routing"><summary><AppIcon name="chevron-right" :size="12" class="chev" />Review route</summary><ul><li v-for="reason in reviewSkips(review)" :key="reason">{{ reason }}</li></ul></details>
      </li>
    </ol>
    <details v-if="write" :open="formOpen" class="request" @toggle="formOpen = ($event.target as HTMLDetailsElement).open">
      <summary><AppIcon name="chevron-right" :size="12" class="chev" />Review a commit range</summary>
      <form @submit.prevent="submit">
        <fieldset :disabled="sending">
          <label>Repository<input v-model="repository" autocomplete="off" placeholder="owner/repository" required maxlength="200" /></label>
          <label>Base commit<input v-model="base" autocomplete="off" placeholder="Full commit hash" required maxlength="64" /></label>
          <label>Head commit<input v-model="head" autocomplete="off" placeholder="Full commit hash" required maxlength="64" /></label>
          <label>Author run (optional)<input v-model="authorRun" autocomplete="off" placeholder="Managed run ID" /></label>
          <label v-if="!authorRun">Author family<select v-model="authorFamily" required><option value="" disabled>Choose the author</option><option value="openai">Codex</option><option value="anthropic">Claude</option><option value="xai">Grok</option><option value="cursor">Cursor</option><option value="google">Google</option><option value="local">Local</option></select></label>
          <label>Pull request (optional)<input v-model="pullRequest" inputmode="numeric" autocomplete="off" placeholder="Number" /></label>
        </fieldset>
        <button type="submit" class="submit" :disabled="!valid || sending">{{ sending ? 'Requesting…' : 'Request review' }}</button>
      </form>
    </details>
  </section>
</template>

<style scoped>
.reviews { min-width: 0; }
.eyebrow { margin: 0 0 8px; }
.note, .usage { margin: 6px 0; color: var(--ink-3); font-size: 12.5px; line-height: 1.5; overflow-wrap: anywhere; }
.review-list, .findings, .routing ul { margin: 0; padding: 0; list-style: none; }
.review-list { display: grid; gap: 8px; }
.review { padding: 12px; border-radius: 10px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--line); min-width: 0; }
.passed { background: color-mix(in srgb, var(--teal-ink) 5%, var(--chip-bg)); }
.top { display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px 10px; color: var(--ink); font-size: 13px; }
.model { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); font-size: 12px; }
.range { display: flex; flex-wrap: wrap; gap: 4px 8px; margin: 5px 0; font-size: 12px; color: var(--ink-3); }
.range span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
code { font: 11.5px var(--mono); overflow-wrap: anywhere; }
.reason { color: var(--ink-2); }
.findings { display: grid; gap: 10px; margin-top: 10px; }
.location { display: flex; flex-wrap: wrap; gap: 4px 8px; align-items: baseline; }
.severity { font-size: 11px; font-weight: 600; color: var(--ink-2); text-transform: capitalize; }
.findings p { margin: 3px 0 0; font-size: 12.5px; color: var(--ink); line-height: 1.5; overflow-wrap: anywhere; }
.routing, .request { margin-top: 10px; font-size: 12.5px; color: var(--ink-2); }
summary { display: flex; align-items: center; gap: 6px; cursor: pointer; width: fit-content; }
.chev { flex-shrink: 0; }
details[open] > summary .chev { transform: rotate(90deg); }
.routing li { margin: 5px 0 0; color: var(--ink-3); overflow-wrap: anywhere; }
fieldset { border: 0; padding: 0; margin: 12px 0; display: grid; gap: 10px; }
label { display: grid; gap: 5px; font-size: 12px; }
input, select { min-width: 0; width: 100%; box-sizing: border-box; font: inherit; color: var(--ink); background: var(--chip-bg); border: 1px solid var(--line); border-radius: 6px; padding: 8px 10px; }
.submit { padding: 8px 12px; border-radius: 6px; border: 1px solid var(--line); background: var(--ink); color: var(--canvas); font: inherit; cursor: pointer; }
.submit:disabled { opacity: .5; cursor: default; }
.text-action { border: 0; padding: 0; background: transparent; color: var(--teal-ink); font: inherit; cursor: pointer; }
input:focus-visible, select:focus-visible, button:focus-visible, summary:focus-visible { outline: none; box-shadow: var(--focus-ring); }
</style>
