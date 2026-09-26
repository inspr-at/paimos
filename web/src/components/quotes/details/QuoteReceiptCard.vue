<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import './details.css'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { getConfirmation, lifecycleError, readiness, receiptBusy, receiptUrl, retryConfirmation, shortDigest, RECEIPT_PILL, type ConfirmationJob, type Readiness } from '../../../lib/quotes/lifecycle'
import { toast } from '../../../lib/toast'
import { usePoller } from '../../../lib/usePolledData'
import AppIcon from '../../AppIcon.vue'
import BizIcon from '../../business/BizIcon.vue'

// The acceptance receipt: a PDF of the accepted version with its acceptance
// stamp, rendered once on the server and kept unchanged. While it is being made
// the card says so and checks again every few seconds; a failed render can be
// retried, and an uncertain delivery only after you confirm you checked it.
const props = defineProps<{ quoteId: string; version: number; admin: boolean }>()
const job = ref<ConfirmationJob | null>(null)
const ready = ref<Readiness | null>(null)
const loaded = ref(false)
const error = ref('')
const busy = ref(false)
const checked = ref(false)
const poll = usePoller(load, 5000, { enabled: () => receiptBusy(job.value?.state) })
const failures = ref(0)
let generation = 0
const hasReceipt = computed(() => !!job.value?.receipt_sha256)
const when = (iso: string | undefined) => iso ? new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' }).format(new Date(iso)) : ''

async function load() {
  const current = ++generation
  try {
    const [next, readyState] = await Promise.all([getConfirmation(props.quoteId, props.version), ready.value ? Promise.resolve(ready.value) : readiness().catch(() => null)])
    if (current !== generation) return
    job.value = next; ready.value = readyState; error.value = ''; failures.value = 0
  } catch (e) { if (current === generation && ++failures.value >= (job.value ? 3 : 1)) error.value = lifecycleError(e, 'The receipt could not be read.') }
  finally { if (current === generation) loaded.value = true }
}
watch(() => [props.quoteId, props.version], () => { job.value = null; loaded.value = false; checked.value = false; failures.value = 0; void load() }, { immediate: true })
onMounted(() => poll.start())
onBeforeUnmount(() => { generation++; poll.stop() })

async function retry() {
  if (busy.value) return
  busy.value = true; error.value = ''
  try { job.value = await retryConfirmation(props.quoteId, props.version, job.value?.state === 'uncertain'); toast('The receipt is being made again.'); void load() }
  catch (e) { error.value = lifecycleError(e, 'The receipt could not be retried.') }
  finally { busy.value = false }
}
async function copyDigest(value: string) {
  try { await navigator.clipboard.writeText(value); toast('Fingerprint copied.') } catch { toast('Copying did not work here.', { tone: 'error' }) }
}
</script>

<template>
  <section class="d-card" aria-labelledby="receipt-title">
    <header class="d-head">
      <span class="d-icon" aria-hidden="true"><BizIcon name="seal" :size="15" /></span>
      <h3 id="receipt-title">Acceptance receipt</h3>
      <span v-if="job" class="pill" :class="{ ok: hasReceipt }">{{ RECEIPT_PILL[job.state] }}</span>
    </header>
    <div v-if="!loaded" class="sk" aria-hidden="true"><span class="skeleton" /><span class="skeleton short" /></div>
    <template v-else>
      <p v-if="!job" class="d-text">No receipt belongs to this version. Receipts are made for acceptances through the customer link or by a signed-in contact.</p>
      <template v-else>
        <p v-if="hasReceipt" class="d-text">A PDF of the accepted version with its acceptance stamp, made {{ when(job.updated_at) }}. It is kept exactly as made.</p>
        <p v-else-if="receiptBusy(job.state)" class="d-text" role="status">The receipt is being made. This card updates by itself.<template v-if="failures"> Retrying.</template></p>
        <p v-else-if="job.state === 'failed'" class="d-text">Making the receipt failed after {{ job.attempts }} {{ job.attempts === 1 ? 'try' : 'tries' }}. The acceptance itself is recorded and safe.</p>
        <p v-else-if="job.state === 'uncertain'" class="d-text">It is not certain whether the receipt went out. It is not sent again by itself.</p>
        <dl v-if="hasReceipt" class="d-facts">
          <dt>Fingerprint</dt>
          <dd><span class="d-digest" :data-tip="`SHA-256 of the receipt file: ${job.receipt_sha256}`">{{ shortDigest(job.receipt_sha256!) }}<button type="button" class="d-copy" aria-label="Copy the receipt fingerprint" @click="copyDigest(job.receipt_sha256!)"><AppIcon name="copy" :size="12" /></button></span></dd>
          <template v-if="job.renderer_version"><dt>Made with</dt><dd>{{ job.renderer_version }}</dd></template>
        </dl>
        <a v-if="hasReceipt" class="btn sm" :href="receiptUrl(quoteId, version)" download><AppIcon name="download" :size="13" />Download receipt (PDF)</a>
        <template v-if="admin && (job.state === 'failed' || job.state === 'uncertain')">
          <label v-if="job.state === 'uncertain'" class="check"><input v-model="checked" type="checkbox" class="check-box" />I checked that the customer did not get it</label>
          <button type="button" class="btn sm" :disabled="busy || (job.state === 'uncertain' && !checked)" @click="retry"><AppIcon name="refresh" :size="13" />{{ busy ? 'Retrying…' : 'Make it again' }}</button>
        </template>
      </template>
      <p v-if="ready && !ready.renderer_available" class="hint">This server cannot make PDF receipts yet. Accepted quotes stay recorded; receipts follow once it can.</p>
      <p v-if="ready && !ready.smtp_enabled" class="hint">Email is off: receipts are kept here and on the customer’s page, not sent.</p>
      <p v-if="error" class="d-error" role="alert"><AppIcon name="alert" :size="13" />{{ error }}</p>
    </template>
  </section>
</template>

<style scoped>
.d-card > .btn { width: fit-content; }
.check { display: flex; align-items: flex-start; gap: 8px; font-size: 12.5px; color: var(--ink); }
.check .check-box { margin-top: 2px; }
</style>
