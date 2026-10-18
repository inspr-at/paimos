<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import './details.css'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { quoteQr } from '../../../lib/quotes/qr'
import { confirmAction } from '../../../lib/confirm'
import { createLink, getLink, lifecycleError, linkUrl, revokeLink, type PublicLink } from '../../../lib/quotes/lifecycle'
import { toast } from '../../../lib/toast'
import AppIcon from '../../AppIcon.vue'
import BizIcon from '../../business/BizIcon.vue'

// The customer link for one issued version: finalization creates it, while an
// admin can create one for older issued history, copy it again or revoke it.
// The link opens this frozen version and nothing else, and it can accept it
// until it ends, the quote is revised or someone revokes it.
const props = defineProps<{ quoteId: string; version: number; admin: boolean; acceptable: boolean; accepted: boolean }>()
const emit = defineEmits<{ changed: []; loading: [value: boolean] }>()
const link = ref<PublicLink | null>(null)
const loaded = ref(false)
const error = ref('')
const busy = ref(false)
const fresh = ref('')
const days = ref(30)
const copied = ref(false)
const DAYS = [7, 14, 30, 60, 90]
const now = ref(Date.now())
const state = computed<'none' | 'active' | 'expired' | 'revoked'>(() => {
  const l = link.value
  if (!l) return 'none'
  if (l.revoked_at) return 'revoked'
  return Date.parse(l.expires_at) <= now.value ? 'expired' : 'active'
})
const when = (iso: string | undefined) => iso ? new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' }).format(new Date(iso)) : ''
const endsOn = computed(() => new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric' }).format(new Date(Date.now() + days.value * 86_400_000)))
let generation = 0
// A read or a click belongs to the quote on screen at that moment. A later
// switch drops the result instead of painting or revoking the quote now shown.
function still(current: number, quoteId: string, version: number) {
  return current === generation && props.quoteId === quoteId && props.version === version
}

async function load() {
  const quoteId = props.quoteId, version = props.version, current = ++generation
  error.value = ''
  if (!props.admin) { if (still(current, quoteId, version)) loaded.value = true; return }
  try {
    const next = await getLink(quoteId, version)
    if (!still(current, quoteId, version)) return
    link.value = next; fresh.value = next?.path ? linkUrl(next) : ''; now.value = Date.now()
  } catch (e) { if (still(current, quoteId, version)) error.value = lifecycleError(e, 'The link could not be read.') }
  finally { if (still(current, quoteId, version)) loaded.value = true }
}
watch(() => [props.quoteId, props.version], () => { fresh.value = ''; link.value = null; loaded.value = false; error.value = ''; busy.value = false; void load() }, { immediate: true })
// The Details pane shows its cards together once this one has read its link.
watch(loaded, value => emit('loading', !value), { immediate: true })
onBeforeUnmount(() => emit('loading', false))

async function create() {
  if (busy.value) return
  const quoteId = props.quoteId, version = props.version
  const expiresAt = new Date(Date.now() + days.value * 86_400_000).toISOString()
  // A read already in flight must not replace the link this click just showed.
  const current = ++generation
  busy.value = true; error.value = ''
  try {
    const made = await createLink(quoteId, version, expiresAt)
    if (!still(current, quoteId, version)) return
    fresh.value = linkUrl(made)
    link.value = { ...made, token: undefined, path: undefined }
    now.value = Date.now()
    loaded.value = true
    emit('changed')
  } catch (e) { if (still(current, quoteId, version)) error.value = lifecycleError(e, 'The link was not created. Nothing changed.') }
  finally { if (still(current, quoteId, version)) busy.value = false }
}
async function revoke() {
  const quoteId = props.quoteId, version = props.version
  const ok = await confirmAction({
    title: 'Revoke the customer link?', confirmLabel: 'Revoke link', danger: true,
    body: 'Whoever has it can no longer open or accept the quote. The quote itself stays issued, and you can create a new link afterwards.',
  })
  if (!ok || props.quoteId !== quoteId || props.version !== version) return
  const current = ++generation
  busy.value = true; error.value = ''
  try {
    const revoked = await revokeLink(quoteId, version)
    if (!still(current, quoteId, version)) return
    link.value = revoked; fresh.value = ''; now.value = Date.now(); toast('The link is revoked.'); emit('changed')
  } catch (e) { if (still(current, quoteId, version)) error.value = lifecycleError(e, 'The link was not revoked.') }
  finally { if (still(current, quoteId, version)) busy.value = false }
}
async function copy() {
  try { await navigator.clipboard.writeText(fresh.value); copied.value = true; setTimeout(() => { copied.value = false }, 1800) }
  catch { toast('Copying did not work here. Select the link and copy it.', { tone: 'error' }) }
}
function selectAll(event: FocusEvent) { (event.target as HTMLInputElement).select() }
// The same link as a QR code, for a printed page or a phone across the table.
const showQr = ref(false)
const qr = computed(() => fresh.value ? quoteQr(fresh.value) : null)
</script>

<template>
  <section class="d-card" aria-labelledby="link-title">
    <header class="d-head">
      <span class="d-icon" aria-hidden="true"><AppIcon name="link" :size="15" /></span>
      <h3 id="link-title">Customer link</h3>
      <span v-if="state === 'active'" class="pill ok">Active</span>
      <span v-else-if="state === 'revoked'" class="pill">Revoked</span>
      <span v-else-if="state === 'expired'" class="pill">Ended</span>
    </header>

    <p v-if="!admin" class="d-text">An admin shares quotes with customers through a link.</p>
    <div v-else-if="!loaded" class="sk" aria-hidden="true"><span class="skeleton" /><span class="skeleton short" /></div>
    <template v-else>
      <div v-if="fresh" class="fresh">
        <label class="d-label" for="quote-link-url">Customer link</label>
        <div class="url-row">
          <input id="quote-link-url" class="field url" :value="fresh" readonly @focus="selectAll" />
          <button type="button" class="btn sm primary" @click="copy"><AppIcon :name="copied ? 'check' : 'copy'" :size="13" />{{ copied ? 'Copied' : 'Copy' }}</button>
        </div>
        <div class="fresh-actions">
          <a class="open" :href="fresh" target="_blank" rel="noopener noreferrer">Open as the customer sees it<AppIcon name="external" :size="12" /></a>
          <button type="button" class="open" :aria-expanded="showQr" aria-controls="quote-link-qr" @click="showQr = !showQr">{{ showQr ? 'Hide QR code' : 'QR code' }}</button>
        </div>
        <svg v-if="showQr && qr" id="quote-link-qr" class="qr" :viewBox="`-2 -2 ${qr.size + 4} ${qr.size + 4}`" role="img" aria-label="QR code of the customer link" shape-rendering="crispEdges">
          <rect x="-2" y="-2" :width="qr.size + 4" :height="qr.size + 4" fill="#fff" /><path :d="qr.path" fill="#000" />
        </svg>
      </div>
	  <p v-if="link?.copy_unavailable_reason === 'key_not_configured' && fresh" class="hint" role="status">This server has no customer-link key. Copy or save this link now. It cannot be copied or shown as a QR code after you leave this page.</p>

      <p v-if="state === 'active'" class="d-text">Opens this version until <strong>{{ when(link!.expires_at) }}</strong>{{ acceptable ? ', and can accept it.' : '. Acceptance is closed.' }}</p>
      <p v-else-if="state === 'revoked'" class="d-text">Revoked on {{ when(link!.revoked_at) }}. It no longer opens the quote.</p>
      <p v-else-if="state === 'expired'" class="d-text">Ended on {{ when(link!.expires_at) }}. It no longer opens the quote.</p>
      <p v-else-if="accepted" class="d-text">This version was accepted without a link.</p>
      <p v-else class="d-text">Share this version with a link. Whoever has it can read the quote and accept it, without an account.</p>

      <div v-if="(state === 'none' || state === 'revoked') && acceptable" class="create-row">
        <label class="d-label" for="quote-link-days">Link ends after</label>
        <div class="create-controls">
          <select id="quote-link-days" v-model.number="days" class="field days">
            <option v-for="d in DAYS" :key="d" :value="d">{{ d }} days</option>
          </select>
          <button type="button" class="btn sm primary" :disabled="busy" @click="create"><AppIcon name="link" :size="13" />{{ busy ? 'Creating…' : state === 'none' ? 'Create link' : 'Create a new link' }}</button>
        </div>
        <p class="hint">Ends on {{ endsOn }}.</p>
      </div>
      <div v-if="state === 'active' || state === 'expired'" class="actions">
        <button type="button" class="btn sm ghost danger-text" :disabled="busy" @click="revoke">Revoke link</button>
      </div>
	  <p v-if="state === 'active' && !fresh && link?.copy_unavailable_reason === 'key_not_configured'" class="hint" role="status">This server has no customer-link key. This link cannot be copied or shown as a QR code again. Revoke it to create a new one.</p>
	  <p v-else-if="state === 'active' && !fresh && acceptable" class="hint">This older link cannot be copied again. Revoke it to create a new one.</p>
      <p v-if="error" class="d-error" role="alert"><AppIcon name="alert" :size="13" />{{ error }}</p>

      <p class="privacy"><BizIcon name="lock" :size="13" /><span>The page shows only this frozen version: no other quotes, customers or people. It is never cached or indexed. The verifier is stored separately from any encrypted admin-only copy.</span></p>
    </template>
  </section>
</template>

<style scoped>
.fresh { display: grid; gap: 6px; padding: 10px; border-radius: 10px; background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.url-row { display: flex; gap: 6px; }
.url { flex: 1; min-width: 0; height: 32px; padding: 0 9px; font: 500 12px/1 var(--mono); font-variant-ligatures: none; }
.open { display: inline-flex; align-items: center; gap: 5px; width: fit-content; padding: 0; border: 0; background: none; font-size: 12.5px; font-weight: 600; color: var(--teal-ink); text-decoration: none; cursor: pointer; }
.fresh-actions { display: flex; flex-wrap: wrap; gap: 4px 16px; }
.qr { width: 148px; height: 148px; justify-self: start; border-radius: 6px; box-shadow: 0 0 0 1px var(--line-2); }
.open:hover { text-decoration: underline; }
.open:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
.create-row { display: grid; gap: 6px; }
.create-controls { display: flex; flex-wrap: wrap; gap: 6px; }
.days { width: auto; height: 32px; padding: 0 28px 0 10px; font-size: 13px; }
.actions { display: flex; gap: 6px; }
.danger-text { color: var(--danger); }
.privacy { display: flex; align-items: flex-start; gap: 7px; padding-top: 10px; border-top: 1px solid var(--line); font-size: 12px; line-height: 1.45; color: var(--ink-3); }
.privacy svg { flex-shrink: 0; margin-top: 2px; }
</style>
