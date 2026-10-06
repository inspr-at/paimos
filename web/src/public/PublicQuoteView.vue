<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import QuoteDocument from '../components/quotes/editor/QuoteDocument.vue'
import { brand, setPageTitle } from '../lib/brand'
import { documentTotal } from '../lib/quotes/layout'
import { COPY, documentLanguage, formatDay, formatMoment, formatMoney } from '../lib/quotes/publicCopy'
import type { QuoteDocumentData } from '../lib/quotes/types'
import { useVersion } from '../stores/version'
import { resilientFetch } from '../lib/api'
import { usePoller } from '../lib/usePolledData'

// The page a customer opens from a quote link: the sender's name, the frozen
// document exactly as issued, and, while it can be accepted, a short form to
// accept it. Calm like paper, readable at 390 px, in light or dark. It asks
// the server for this one quote only (no session, no cookies) and loads
// nothing from anywhere else, so it runs under the strict public policy.
const props = defineProps<{ publicTenant: string; token: string }>()
interface PublicQuote {
  document: QuoteDocumentData; offer_no: string; version: number; content_sha256: string
  state: string; expires_at: string; acceptable: boolean; receipt_ready: boolean; accepted_at?: string
}
const quote = ref<PublicQuote | null>(null)
const busy = ref(false)
const loading = ref(true)
const missing = ref(false)
const error = ref('')
const overflow = ref('')
const acceptedNow = ref<{ name: string; at: string } | null>(null)
const name = ref('')
const company = ref('')
const note = ref('')
const confirm = ref(false)
const tried = ref(false)
const nameInput = ref<HTMLInputElement>()
const decision = ref<HTMLElement>()
let mutationID = crypto.randomUUID()
const version = useVersion()
void version.load()
const apiPath = computed(() => `/api/public/quotes/${encodeURIComponent(props.publicTenant)}/${encodeURIComponent(props.token)}`)
const publicURL = computed(() => `${location.origin}/offers/${encodeURIComponent(props.publicTenant)}/${encodeURIComponent(props.token)}`)
const pdfPath = computed(() => `${apiPath.value}/pdf`)
const sender = computed(() => quote.value?.document.sender.company?.trim() || '')
const recipient = computed(() => quote.value?.document.recipient.name?.trim() || '')
// Every word, date and amount in the document's language; the signed-in app stays English.
const lang = computed(() => documentLanguage(quote.value?.document))
const t = computed(() => COPY[lang.value])
const total = computed(() => { const d = quote.value?.document; if (!d) return ''; try { return formatMoney(documentTotal(d.positions), d.currency, lang.value) } catch { return '' } })
const linkEnded = computed(() => !!quote.value && Date.parse(quote.value.expires_at) <= Date.now())
const accepted = computed(() => !!quote.value && (quote.value.state === 'accepted' || !!quote.value.accepted_at))
const when = (iso: string | undefined) => formatMoment(iso, lang.value)
const day = (iso: string | undefined) => formatDay(iso, lang.value)
// The evidence sentence around the fingerprint, which is shown in the mono face.
const evidence = computed(() => t.value.evidence('\u0000').split('\u0000') as [string, string])
const shortDigest = (sha: string) => `${sha.slice(0, 8)}…${sha.slice(-6)}`
const closedReason = computed(() => {
  const q = quote.value
  if (!q || q.acceptable || accepted.value) return ''
  if (linkEnded.value) return t.value.linkEnded(when(q.expires_at))
  if (q.state === 'issued' && q.document.valid_until) return t.value.validityEnded(day(q.document.valid_until))
  return t.value.replaced
})

// Receipts follow an acceptance within a minute or so; the page checks a few times.
let polls = 0
const poll = usePoller(() => { polls++; return load(true) }, 5000, { enabled: () => accepted.value && !quote.value?.receipt_ready && polls < 24 })
async function load(quiet = false) {
  if (!quiet) { loading.value = true; error.value = ''; missing.value = false }
  try {
    const response = await resilientFetch(apiPath.value, { credentials: 'omit', cache: 'no-store', referrerPolicy: 'no-referrer' })
    if (response.status === 404 || response.status === 410) { missing.value = true; quote.value = null; return }
    if (!response.ok) throw new Error(t.value.failedBody)
    quote.value = await response.json() as PublicQuote
    setPageTitle(t.value.pageTitle(quote.value.offer_no, sender.value))
  } catch (cause) {
    if (!quiet) error.value = cause instanceof Error && cause.message === t.value.failedBody ? cause.message : t.value.failedBody
  } finally { if (!quiet) loading.value = false }
}
watch(() => [props.publicTenant, props.token], () => { mutationID = crypto.randomUUID(); acceptedNow.value = null; polls = 0; void load() }, { immediate: true })

async function accept() {
  tried.value = true
  if (!quote.value || !quote.value.acceptable || busy.value) return
  if (!name.value.trim()) { nameInput.value?.focus(); return }
  if (!confirm.value) return
  busy.value = true
  error.value = ''
  try {
    const response = await fetch(`${apiPath.value}/accept`, {
      method: 'POST', credentials: 'omit', cache: 'no-store', referrerPolicy: 'no-referrer', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ version: quote.value.version, expected_content_sha256: quote.value.content_sha256,
        client_mutation_id: mutationID, name: name.value.trim(), company: company.value.trim(), note: note.value.trim(), confirm: true }),
    })
    if (response.status === 429) throw new Error(t.value.tooMany)
    if (!response.ok) throw new Error(response.status === 409 ? t.value.noLongerAcceptable : t.value.notSaved)
    const body = await response.json().catch(() => ({})) as { accepted_at?: string }
    acceptedNow.value = { name: name.value.trim(), at: body.accepted_at ?? new Date().toISOString() }
    await load(true)
    await nextTick()
    decision.value?.focus()
  } catch (cause) {
    error.value = cause instanceof Error && Object.values(t.value).includes(cause.message) ? cause.message : t.value.notSaved
  } finally { busy.value = false }
}
function toDecision() { decision.value?.scrollIntoView({ behavior: 'smooth', block: 'start' }); void nextTick(() => nameInput.value?.focus({ preventScroll: true })) }

// The paper keeps its A4 proportions and shrinks to the screen, never scrolls sideways.
const desk = ref<HTMLElement>()
const initialDeskWidth = Math.min(880, window.innerWidth - (window.innerWidth <= 600 ? 24 : 40))
const scale = ref(Math.min(1, Math.max(0.3, Math.floor(initialDeskWidth) / 794)))
let sizer: ResizeObserver | undefined
let frame = 0
const fit = (width: number) => Math.min(1, Math.max(0.3, Math.floor(width) / 794))
watch(desk, el => {
  sizer?.disconnect()
  if (!el) return
  // The first fit lands before the page is painted, so the paper never jumps into place.
  const style = getComputedStyle(el)
  scale.value = fit(el.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight))
  // Later fits wait for layout to settle: a changed scale may change the width it was measured at.
  sizer = new ResizeObserver(([entry]) => { cancelAnimationFrame(frame); const width = entry.contentRect.width; frame = requestAnimationFrame(() => { scale.value = fit(width) }) })
  sizer.observe(el)
}, { flush: 'post' })
// Private by construction: no referrer leaves this page and search engines are asked to stay away.
const metas: HTMLMetaElement[] = []
// Screen readers read the page in its language.
const pageLang = document.documentElement.lang
watch(lang, value => { document.documentElement.lang = value }, { immediate: true })
onMounted(() => {
  poll.start()
  for (const [key, content] of [['robots', 'noindex, nofollow, noarchive'], ['referrer', 'no-referrer']] as const) {
    const meta = document.createElement('meta'); meta.name = key; meta.content = content; document.head.append(meta); metas.push(meta)
  }
})
onBeforeUnmount(() => { sizer?.disconnect(); cancelAnimationFrame(frame); poll.stop(); for (const meta of metas) meta.remove(); document.documentElement.lang = pageLang })
</script>

<template>
  <main class="public-quote">
    <header class="pq-bar">
      <div class="pq-bar-inner">
        <p class="pq-sender">{{ sender || t.quote }}</p>
        <p v-if="quote" class="pq-ref"><span>{{ t.quoteNo(quote.offer_no) }}</span><span>{{ t.version(quote.version) }}</span></p>
      </div>
    </header>

    <div v-if="loading" class="pq-wrap" role="status" :aria-label="t.loading"><div class="pq-card pq-skeleton"><span class="skeleton" /><span class="skeleton short" /></div></div>
    <div v-else-if="missing" class="pq-wrap">
      <section class="pq-card pq-message" role="alert">
        <h1>{{ t.missingTitle }}</h1>
        <p>{{ t.missingBody }}</p>
      </section>
    </div>
    <div v-else-if="!quote" class="pq-wrap">
      <section class="pq-card pq-message" role="alert">
        <h1>{{ t.failedTitle }}</h1>
        <p>{{ error || t.failedBody }}</p>
        <button type="button" class="pq-btn" @click="load()">{{ t.retry }}</button>
      </section>
    </div>
    <template v-else>
      <div class="pq-wrap">
        <section class="pq-card pq-intro" aria-labelledby="pq-title">
          <p class="pq-eyebrow">{{ recipient ? t.forRecipient(recipient) : t.quote }}</p>
          <h1 id="pq-title">{{ quote.document.title }}</h1>
          <p v-if="quote.document.subtitle" class="pq-sub">{{ quote.document.subtitle }}</p>
          <dl class="pq-facts">
            <div><dt>{{ t.netTotal }}</dt><dd class="pq-mono">{{ total }}</dd></div>
            <div><dt>{{ t.dated }}</dt><dd class="pq-mono">{{ day(quote.document.offer_date) }}</dd></div>
            <div><dt>{{ t.validUntil }}</dt><dd class="pq-mono">{{ day(quote.document.valid_until) }}</dd></div>
          </dl>
          <p v-if="accepted" class="pq-status ok" role="status"><svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="8" cy="8" r="6.2" /><path d="m5.3 8.2 1.9 1.9 3.6-3.9" /></svg><span>{{ quote.accepted_at ? t.acceptedOn(when(quote.accepted_at)) : t.accepted }}</span></p>
          <p v-else-if="quote.acceptable" class="pq-status">{{ t.invite }}</p>
          <p v-else class="pq-status muted">{{ t.closed }} {{ closedReason }}</p>
          <div class="pq-actions">
            <button v-if="quote.acceptable" type="button" class="pq-btn primary" @click="toDecision">{{ t.reviewAndAccept }}</button>
            <a class="pq-btn" :href="pdfPath" target="_blank" rel="noopener noreferrer"><svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 2.6v7.6M4.8 7.2 8 10.4l3.2-3.2M3 13.2h10" /></svg>{{ accepted && quote.receipt_ready ? t.receiptPdf : t.pdf }}<span class="sr-only">{{ t.newTab }}</span></a>
          </div>
        </section>
      </div>

      <p v-if="overflow" role="alert" class="pq-wrap pq-note">{{ t.overflow(overflow) }}</p>
      <section ref="desk" class="pq-desk" :aria-label="t.documentRegion">
        <div class="pq-paper" :style="{ zoom: scale }">
          <QuoteDocument :document="quote.document" :offer-no="quote.offer_no" :editable="false" :public-link="publicURL" @overflow="overflow = $event ?? ''" />
        </div>
      </section>

      <div class="pq-wrap">
        <section v-if="acceptedNow" ref="decision" class="pq-card pq-done" tabindex="-1" aria-labelledby="pq-done-title">
          <h2 id="pq-done-title">{{ t.thanks(acceptedNow.name) }}</h2>
          <p role="status">{{ t.recorded(quote.version, when(acceptedNow.at)) }} {{ quote.receipt_ready ? t.receiptReady : t.receiptPending }}</p>
          <a v-if="quote.receipt_ready" class="pq-btn" :href="pdfPath" target="_blank" rel="noopener noreferrer">{{ t.openReceipt }}<span class="sr-only">{{ t.newTab }}</span></a>
        </section>
        <section v-else-if="quote.acceptable" ref="decision" class="pq-card pq-decision" tabindex="-1" aria-labelledby="decision-title">
          <h2 id="decision-title">{{ t.acceptTitle }}</h2>
          <p class="pq-lead">{{ t.acceptLead(quote.version, quote.offer_no, total) }}</p>
          <form novalidate @submit.prevent="accept">
            <div class="pq-field">
              <label for="public-name">{{ t.yourName }}</label>
              <input id="public-name" ref="nameInput" v-model="name" name="name" autocomplete="name" required maxlength="500" :aria-invalid="tried && !name.trim()" aria-describedby="public-name-note" />
              <p v-if="tried && !name.trim()" id="public-name-note" class="pq-bad" role="alert">{{ t.nameMissing }}</p>
            </div>
            <div class="pq-field">
              <label for="public-company">{{ t.company }} <span class="pq-opt">{{ t.optional }}</span></label>
              <input id="public-company" v-model="company" name="organization" autocomplete="organization" maxlength="500" />
            </div>
            <div class="pq-field">
              <label for="public-note">{{ t.noteTo(sender || t.theSender) }} <span class="pq-opt">{{ t.optional }}</span></label>
              <textarea id="public-note" v-model="note" name="note" maxlength="4000" rows="3" />
            </div>
            <label class="pq-check" for="public-confirm"><input id="public-confirm" v-model="confirm" type="checkbox" required />{{ t.confirm }}</label>
            <button class="pq-btn primary pq-submit" type="submit" :disabled="busy || !confirm">{{ busy ? t.submitting : t.submit }}</button>
          </form>
          <p class="pq-fine">{{ evidence[0] }}<span class="pq-mono">{{ shortDigest(quote.content_sha256) }}</span>{{ evidence[1] }}</p>
        </section>
        <p v-if="error && quote" role="alert" class="pq-card pq-error">{{ error }}</p>
      </div>
    </template>
    <footer v-if="!loading" class="pq-foot"><p>{{ t.footer(sender, brand.wordmark) }}</p></footer>
  </main>
</template>

<style scoped>
.public-quote { min-height: 100vh; min-height: 100dvh; background: var(--surface); color: var(--ink); font-family: var(--font); }
.pq-bar { border-bottom: 1px solid var(--line-2); background: var(--surface-raised-2); }
.pq-bar-inner { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 4px 16px; max-width: 880px; margin: 0 auto; padding: 16px 20px; }
.pq-sender { font: 600 17px/1.3 var(--serif); color: var(--ink); overflow-wrap: anywhere; }
.pq-ref { display: flex; flex-wrap: wrap; gap: 4px 12px; font: 500 12.5px/1.4 var(--mono); color: var(--ink-2); font-variant-ligatures: none; }
.pq-wrap { max-width: 880px; margin: 0 auto; padding: 0 20px; }
.pq-card { margin: 24px 0; padding: 24px; border-radius: 16px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2), 0 1px 2px rgba(20, 40, 40, .04); }
.pq-card h1 { font: 650 clamp(22px, 4.2vw, 30px)/1.2 var(--serif); letter-spacing: -.01em; text-wrap: balance; overflow-wrap: anywhere; }
.pq-card h2 { font-size: 19px; font-weight: 650; }
.pq-card p { font-size: 14.5px; line-height: 1.55; color: var(--ink-2); }
.pq-eyebrow { font: 500 11px/1.4 var(--mono) !important; letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3) !important; font-variant-ligatures: none; }
.pq-intro { display: grid; gap: 10px; }
.pq-sub { font-size: 16px !important; color: var(--ink-2); }
.pq-facts { display: flex; flex-wrap: wrap; gap: 12px 32px; margin: 6px 0 0; }
.pq-facts div { display: grid; gap: 2px; }
.pq-facts dt { font-size: 12px; color: var(--ink-3); }
.pq-facts dd { margin: 0; font-size: 15px; font-weight: 600; color: var(--ink); }
.pq-mono { font-family: var(--mono); font-variant-numeric: tabular-nums; font-variant-ligatures: none; }
.pq-status { display: flex; align-items: flex-start; gap: 8px; margin-top: 4px; }
.pq-status.ok { color: var(--ink) !important; font-weight: 600; }
.pq-status.ok svg { flex-shrink: 0; margin-top: 3px; color: var(--ok); }
.pq-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 6px; }
.pq-btn { display: inline-flex; align-items: center; justify-content: center; gap: 7px; min-height: 44px; padding: 0 18px; border: 1px solid var(--line-2); border-radius: 10px; background: var(--surface-raised); color: var(--ink); font: 600 14px/1 var(--font); text-decoration: none; white-space: nowrap; cursor: pointer; }
@media (hover: hover) { .pq-btn:hover { background: var(--row-hover); } }
.pq-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.pq-btn.primary { border-color: var(--primary-line); background: var(--primary); color: var(--primary-on); }
@media (hover: hover) { .pq-btn.primary:hover { filter: brightness(1.06); background: var(--primary); } }
.pq-btn:disabled { opacity: .5; cursor: default; }
.pq-note { margin-top: -8px; font-size: 13px; color: var(--danger); }
/* The desk: the frozen document on a quiet surface, as light paper in any theme. */
.pq-desk { padding: 8px 20px 12px; }
.pq-paper { width: 210mm; margin: 0 auto; }
.pq-decision form { display: grid; gap: 14px; margin-top: 18px; }
.pq-lead { margin-top: 6px; }
.pq-field { display: grid; gap: 6px; }
.pq-field label { font-size: 13.5px; font-weight: 600; color: var(--ink); }
.pq-opt { font-weight: 500; color: var(--ink-3); }
.pq-field input, .pq-field textarea { width: 100%; min-height: 44px; border: 1px solid var(--line-2); border-radius: 10px; background: var(--surface); color: var(--ink); padding: 10px 12px; font: 15px/1.4 var(--font); }
.pq-field textarea { resize: vertical; }
.pq-field input:focus-visible, .pq-field textarea:focus-visible { outline: none; box-shadow: var(--focus-ring); border-color: var(--teal); }
.pq-field input[aria-invalid="true"] { border-color: var(--danger); }
.pq-bad { font-size: 13px !important; color: var(--danger) !important; }
.pq-check { display: flex; align-items: flex-start; gap: 10px; font-size: 14.5px; line-height: 1.45; color: var(--ink); cursor: pointer; }
.pq-check input { flex-shrink: 0; width: 18px; height: 18px; margin-top: 1px; accent-color: var(--teal); }
.pq-submit { justify-self: start; min-width: 180px; }
.pq-fine { margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--line); font-size: 12.5px !important; color: var(--ink-3) !important; }
.pq-done { display: grid; gap: 10px; outline: none; }
.pq-done:focus-visible, .pq-decision:focus-visible { box-shadow: inset 0 0 0 1px var(--line-2), var(--focus-ring); }
.pq-decision { outline: none; }
.pq-error { color: var(--danger) !important; background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); font-size: 14px; }
.pq-message { display: grid; gap: 10px; justify-items: start; margin-top: 48px; }
.pq-skeleton { display: grid; gap: 12px; }
.pq-skeleton .skeleton { height: 14px; }
.pq-skeleton .short { width: 50%; }
.pq-foot { max-width: 880px; margin: 0 auto; padding: 8px 20px 40px; }
.pq-foot p { font-size: 12.5px; color: var(--ink-3); text-align: center; }
@media (max-width: 600px) {
  .pq-wrap { padding: 0 12px; }
  .pq-card { margin: 16px 0; padding: 18px 16px; border-radius: 14px; }
  .pq-bar-inner { padding: 12px 16px; }
  .pq-desk { padding: 4px 12px 8px; }
  .pq-actions .pq-btn, .pq-submit { flex: 1 1 100%; width: 100%; }
}
@media print {
  .pq-bar, .pq-wrap, .pq-foot, .pq-note { display: none !important; }
  .pq-desk { padding: 0; }
  .pq-paper { zoom: 1 !important; }
}
</style>

<style>
/* The customer's page surface reaches under the shell's reserved scrollbar gutter,
   so no strip of the app background shows at the right edge. */
main:has(> .page-flow > .public-quote) { background: var(--surface); }
</style>
