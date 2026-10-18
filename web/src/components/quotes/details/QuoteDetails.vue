<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import './details.css'
import { computed, ref, watch } from 'vue'
import { useAttachments } from '../../../lib/useAttachments'
import { acceptanceNotices, lifecycleError, listVersions, shortDigest, type AcceptanceNotice, type QuoteProjection, type QuoteVersion } from '../../../lib/quotes/lifecycle'
import { dayText, statusOf, STATUS_META } from '../../../lib/quotes/list'
import { toast } from '../../../lib/toast'
import AppIcon from '../../AppIcon.vue'
import BizIcon from '../../business/BizIcon.vue'
import AttachmentLightbox from '../../work/AttachmentLightbox.vue'
import AttachmentStrip from '../../work/AttachmentStrip.vue'
import QuoteStatusIcon from '../QuoteStatusIcon.vue'
import QuoteLinkCard from './QuoteLinkCard.vue'
import QuoteReceiptCard from './QuoteReceiptCard.vue'

// The Details pane of the side panel: where the quote stands and what comes
// next (issue, share, revise), the evidence each step left (the frozen
// version's fingerprint, the acceptance, the receipt), its versions and its
// files. Every step that changes the quote asks the workspace, which owns the
// edit session; this pane reads and shows.
const props = defineProps<{
  quoteId: string; offerNo: string; projection: QuoteProjection | null; frozen: QuoteVersion | null
  local: string; admin: boolean; staff: boolean; customerName: string; viewing: number | null; busy: string
}>()
const emit = defineEmits<{ issue: []; revise: []; view: [version: number | null]; changed: [] }>()
const versions = ref<QuoteVersion[] | null>(null)
const notices = ref<AcceptanceNotice[]>([])
const error = ref('')
let generation = 0
const status = computed(() => props.projection ? statusOf(props.projection) : 'draft')
const current = computed(() => props.projection?.current_version ?? 0)
const revising = computed(() => props.projection?.state === 'draft' && current.value > 0)
const acceptance = (version: number) => notices.value.find(n => n.quote_node_id === props.quoteId && n.version === version) ?? null
const currentAcceptance = computed(() => acceptance(current.value))
const when = (iso: string | undefined) => iso ? new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' }).format(new Date(iso)) : ''
const channel = (n: AcceptanceNotice | null) => n?.channel === 'public' ? 'through the customer link' : n ? 'by a signed-in contact' : ''
const validUntil = computed(() => props.frozen?.document?.valid_until ?? '')
// The cards below the status land together once the versions and the customer link
// are read: until then they keep their place out of sight under one loading state,
// so no card pushes another down as its read arrives.
const linkLoading = ref(false)
const settled = computed(() => versions.value !== null && !linkLoading.value)

async function load() {
  const quoteId = props.quoteId, current = ++generation
  const still = () => current === generation && props.quoteId === quoteId
  error.value = ''
  try {
    const [list, recent] = await Promise.all([listVersions(quoteId), acceptanceNotices().catch(() => [] as AcceptanceNotice[])])
    if (!still()) return
    versions.value = [...list].sort((a, b) => b.version - a.version); notices.value = recent
  } catch (e) { if (!still()) return; versions.value = []; error.value = lifecycleError(e, 'The versions could not be loaded.') }
}
watch(() => [props.quoteId, props.projection?.revision] as const, ([quoteId], previous) => {
  // The previous quote's versions stay out of the next quote while its own read is in flight.
  if (!previous || previous[0] !== quoteId) { versions.value = null; notices.value = []; error.value = '' }
  void load()
}, { immediate: true })

async function copy(value: string) {
  try { await navigator.clipboard.writeText(value); toast('Fingerprint copied.') } catch { toast('Copying did not work here.', { tone: 'error' }) }
}

// Files on the quote: the same strip, viewer and undo as on a ticket.
const attachments = useAttachments(computed(() => props.quoteId))
const lightbox = ref<InstanceType<typeof AttachmentLightbox>>()
const canAttach = computed(() => props.staff && !props.projection?.archived)
</script>

<template>
  <div class="details">
    <section class="d-card status" aria-labelledby="status-title">
      <header class="d-head">
        <QuoteStatusIcon :status="status" :label="false" :size="16" />
        <h3 id="status-title">{{ revising ? `Revising version ${current}` : STATUS_META[status].label }}</h3>
        <span v-if="projection?.archived" class="pill">Archived</span>
      </header>
      <template v-if="status === 'draft'">
        <p v-if="revising" class="d-text">Version {{ current }} stays exactly as it was issued. Issuing this draft makes it version {{ current + 1 }} of the same quote.</p>
        <p v-else class="d-text">Only people in this workspace can see it. Issuing freezes the saved document as version 1, with a fingerprint the customer’s acceptance is bound to.</p>
        <button v-if="admin" type="button" class="btn sm primary" :disabled="!!busy || projection?.archived" @click="emit('issue')"><BizIcon name="seal" :size="13" />{{ busy === 'issue' ? 'Issuing…' : 'Issue quote…' }}</button>
        <p v-else class="hint">A workspace admin issues quotes.</p>
        <p v-if="admin && (local === 'dirty' || local === 'failed' || local === 'offline')" class="hint">Your unsaved changes are saved first.</p>
      </template>
      <template v-else-if="status === 'issued' || status === 'expired'">
        <p class="d-text">Version {{ current }} was issued{{ frozen ? ` on ${when(frozen.created_at)}` : '' }} and is frozen: its text and prices cannot change.<template v-if="status === 'expired'"> Its validity ended on {{ dayText(validUntil) }}; the customer can still read it, not accept it.</template><template v-else-if="validUntil"> Valid until {{ dayText(validUntil) }}.</template></p>
        <p class="hint">Nothing was sent. Share it with the customer link below, or as a PDF.</p>
      </template>
      <template v-else-if="status === 'accepted'">
        <p class="d-text">The customer accepted version {{ current }}<template v-if="currentAcceptance"> on {{ when(currentAcceptance.accepted_at) }} {{ channel(currentAcceptance) }}</template>. The acceptance is bound to the fingerprint below and cannot change.</p>
      </template>
      <p v-else class="d-text">This quote is void. It stays readable with its history.</p>
      <dl v-if="frozen && projection?.state !== 'draft'" class="d-facts">
        <dt>Fingerprint</dt>
        <dd><span class="d-digest" :data-tip="`SHA-256 of version ${frozen.version}: ${frozen.content_sha256}`">{{ shortDigest(frozen.content_sha256) }}<button type="button" class="d-copy" :aria-label="`Copy the fingerprint of version ${frozen.version}`" @click="copy(frozen.content_sha256)"><AppIcon name="copy" :size="12" /></button></span></dd>
      </dl>
      <button v-if="admin && (status === 'issued' || status === 'expired' || status === 'accepted') && !projection?.archived" type="button" class="btn sm" :disabled="!!busy" @click="emit('revise')">
        <AppIcon name="edit" :size="13" />{{ busy === 'revise' ? 'Opening…' : `Revise as version ${current + 1}…` }}
      </button>
    </section>

    <div class="rest" :class="{ settling: !settled }" :aria-busy="!settled">
    <div v-if="!settled" class="rest-loading" role="status" aria-label="Loading the quote’s details"><span class="skeleton" /><span class="skeleton short" /><span class="skeleton" /></div>
    <QuoteLinkCard
      v-if="projection && current > 0 && (status === 'issued' || status === 'expired' || status === 'accepted')"
      :quote-id="quoteId" :version="current" :admin="admin" :acceptable="status === 'issued'" :accepted="status === 'accepted'" @changed="emit('changed')" @loading="value => linkLoading = value"
    />
    <QuoteReceiptCard v-if="status === 'accepted' && current > 0" :quote-id="quoteId" :version="current" :admin="admin" />

    <section class="d-card" aria-labelledby="versions-title">
      <header class="d-head">
        <span class="d-icon" aria-hidden="true"><BizIcon name="history" :size="15" /></span>
        <h3 id="versions-title">Versions</h3>
        <span v-if="versions?.length" class="count">{{ versions.length }}</span>
      </header>
      <div v-if="versions === null" class="sk" aria-hidden="true"><span class="skeleton" /><span class="skeleton short" /></div>
      <p v-else-if="!versions.length" class="d-text">None yet. Each time the quote is issued, the document is frozen here as a new version.</p>
      <ol v-else class="versions">
        <li v-for="v in versions" :key="v.version" class="version" :class="{ on: viewing === v.version }">
          <div class="v-main">
            <span class="v-name">Version {{ v.version }}</span>
            <span class="v-meta">{{ when(v.created_at) }}<template v-if="acceptance(v.version)"> · accepted {{ when(acceptance(v.version)!.accepted_at) }}</template></span>
            <span class="d-digest" :data-tip="`SHA-256: ${v.content_sha256}`">{{ shortDigest(v.content_sha256) }}<button type="button" class="d-copy" :aria-label="`Copy the fingerprint of version ${v.version}`" @click="copy(v.content_sha256)"><AppIcon name="copy" :size="12" /></button></span>
          </div>
          <button v-if="v.document && (projection?.state === 'draft' || v.version !== current)" type="button" class="btn sm ghost" :aria-pressed="viewing === v.version" @click="emit('view', viewing === v.version ? null : v.version)">
            {{ viewing === v.version ? 'Back to the draft' : 'View' }}
          </button>
        </li>
      </ol>
      <p v-if="error" class="d-error" role="alert"><AppIcon name="alert" :size="13" />{{ error }}</p>
    </section>

    <section class="d-card files" aria-labelledby="files-title">
      <header class="d-head">
        <span class="d-icon" aria-hidden="true"><AppIcon name="paperclip" :size="15" /></span>
        <h3 id="files-title">Files</h3>
        <span v-if="attachments.count.value" class="count">{{ attachments.count.value }}</span>
      </header>
      <p v-if="!attachments.count.value && !attachments.loading.value" class="d-text">Briefs, plans or signed copies that belong to this quote. They are not part of the document the customer sees.</p>
      <AttachmentStrip
        v-if="attachments.count.value || canAttach" layout="strip" :items="attachments.items.value" :uploads="attachments.uploads.value"
        :can-write="canAttach" :loading="attachments.loading.value" :error="attachments.error.value"
        @open="id => lightbox?.open(id)" @add="files => attachments.add(files)" @remove="attachments.remove" @reorder="attachments.reorder" @caption="attachments.setCaption"
        @retry="attachments.retry" @cancel="attachments.cancel" @reload="attachments.load"
      />
    </section>

    <section class="d-card" aria-labelledby="facts-title">
      <header class="d-head"><span class="d-icon" aria-hidden="true"><BizIcon name="document" :size="15" /></span><h3 id="facts-title">About</h3></header>
      <dl class="d-facts">
        <dt>Number</dt><dd class="mono">{{ offerNo || 'Not numbered' }}</dd>
        <dt>Customer</dt><dd><RouterLink v-if="projection" class="d-link" :to="`/business/customers/${projection.customer_org_node_id}`">{{ customerName || 'Open customer' }}</RouterLink></dd>
        <template v-if="projection?.project_ref"><dt>Project</dt><dd>{{ projection.project_ref }}</dd></template>
      </dl>
    </section>
    </div>
    <AttachmentLightbox ref="lightbox" :items="attachments.items.value" :ticket-key="offerNo || 'Quote'" :can-write="canAttach" :set-caption="attachments.setCaption" />
  </div>
</template>

<style scoped>
.details { display: grid; gap: 10px; }
.rest { position: relative; display: grid; gap: 10px; }
.settling > :not(.rest-loading) { visibility: hidden; }
.rest-loading { position: absolute; inset: 0 0 auto; display: grid; gap: 10px; padding: 16px; border-radius: var(--radius); background: var(--surface-raised-2); box-shadow: inset 0 0 0 1px var(--line-2); }
.rest-loading .short { width: 55%; }
.status .btn { width: fit-content; }
.count { font: 600 11.5px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.versions { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; }
.version { display: flex; align-items: center; gap: 8px; padding: 8px 8px 8px 10px; margin: 0 -6px; border-radius: 10px; }
.version.on { background: var(--row-selected); }
.v-main { display: grid; gap: 3px; flex: 1; min-width: 0; justify-items: start; }
.v-name { font-size: 13px; font-weight: 650; color: var(--ink); }
.v-meta { font-size: 12px; color: var(--ink-2); }
.mono { font-family: var(--mono); font-variant-ligatures: none; }
.d-link { color: var(--teal-ink); font-weight: 600; text-decoration: none; }
.d-link:hover { text-decoration: underline; }
.d-link:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
/* The card names the files; the strip's own heading stays for screen readers only. */
.files :deep(.attachments .head) { justify-content: flex-end; min-height: 0; }
.files :deep(#attachments-title) { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
</style>
