<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { getNode } from '../../lib/api'
import { formatAttachCode, onAttachCode, takeAttachCode } from '../../lib/attachLink'
import { attachAction, metadataOnlyAttach, type AttachReview } from '../../lib/attachWatch'
import { can, ensurePermissions, onAccessChange } from '../../lib/authz'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'

const identity = useSession()
// A decision changes what /agents lists as waiting; the page refreshes it.
const emit = defineEmits<{ changed: [] }>()
const allowed = computed(() => identity.identity?.principal.kind === 'person' && can('account.manage'))
const dialog = ref<HTMLDialogElement>()
const codeInput = ref<HTMLInputElement>()
const code = ref('')
const review = ref<AttachReview | null>(null)
const metadataOnly = computed(() => !!review.value && metadataOnlyAttach(review.value.snapshot))
const strict = computed(() => review.value?.consent_mode === 'local_auth')
const localUnavailable = computed(() => strict.value && review.value?.snapshot.platform !== 'darwin')
// Platform is the only signal on this review. A headless Mac is still darwin, so the line is for Linux and an unreported platform.
const otherComputerKeepsApproval = computed(() => !!review.value && !strict.value && review.value.snapshot.platform !== 'darwin')
const busy = ref(false)
const error = ref('')
const project = ref('')
const ticket = ref('')
// Approval waits until the person can read which project and ticket it covers.
const labelsReady = ref(false)
let operation: AbortController | undefined
let labelsLoad: AbortController | undefined
function close() {
  operation?.abort(); operation = undefined; labelsLoad?.abort(); labelsLoad = undefined
  dialog.value?.close(); code.value = ''; review.value = null; error.value = ''; busy.value = false; project.value = ''; ticket.value = ''; labelsReady.value = false
}
async function open() { close(); dialog.value?.showModal(); await nextTick(); codeInput.value?.focus() }
// The link `aeon-agentd attach` prints only fills the code in; the person still
// reviews and approves. The router took it off the address bar and holds it in memory.
async function openWithCode(value: string) { await open(); code.value = formatAttachCode(value) }
async function followLink() {
  if (!identity.identity) return
  const linked = takeAttachCode()
  if (!linked) return
  const settled = await ensurePermissions()
  if (settled === 'known' && allowed.value) { await nextTick(); await openWithCode(linked) }
}
onMounted(followLink)
const stopLink = onAttachCode(followLink)
watch(() => !!identity.identity, followLink)
// Names are a convenience; approval binds the immutable IDs in the snapshot. A name
// that cannot be read is replaced by the ID itself. Loading never shares a controller
// with a decision, so deciding cannot cancel the names.
async function loadLabels(result: AttachReview) {
  labelsLoad?.abort()
  const controller = new AbortController(); labelsLoad = controller
  labelsReady.value = false; project.value = ''; ticket.value = ''
  const names = await Promise.allSettled([getNode(result.snapshot.project_id), getNode(result.snapshot.ticket_id)])
  if (labelsLoad !== controller || controller.signal.aborted) return
  project.value = names[0].status === 'fulfilled' ? names[0].value.title : result.snapshot.project_id
  ticket.value = names[1].status === 'fulfilled' ? `${names[1].value.key} · ${names[1].value.title}` : result.snapshot.ticket_id
  labelsReady.value = true
}
function present(result: AttachReview) { review.value = result; code.value = ''; void loadLabels(result) }
// Review a request the page already lists: same review, same digests, no code to type.
function show(result: AttachReview) {
  if (!allowed.value) return
  close(); dialog.value?.showModal()
  present(result)
}
defineExpose({ show })
async function lookup() {
  const normalized = code.value.replace(/[\s-]/g, '')
  if (!allowed.value || !/^\d{9}$/.test(normalized)) { error.value = 'Enter the nine-digit code from your terminal.'; return }
  operation?.abort(); const controller = new AbortController(); operation = controller; busy.value = true; error.value = ''
  try {
    const result = await attachAction('/lookup', { user_code: normalized }, controller.signal)
    if (operation !== controller || controller.signal.aborted) return
    present(result)
  } catch (e) { if (!controller.signal.aborted) error.value = e instanceof Error ? e.message : 'Attach unavailable.' }
  finally { if (operation === controller) busy.value = false }
}
async function decide(revoke = false) {
  if (!allowed.value || !review.value || busy.value || (!revoke && !labelsReady.value)) return
  operation?.abort(); const controller = new AbortController(); operation = controller; busy.value = true; error.value = ''
  try {
    const result = await attachAction(`/${encodeURIComponent(review.value.request_id)}/${revoke ? 'revoke' : 'approve'}`, revoke ? {} : { request_digest: review.value.request_digest, ...(review.value.consent_digest ? { consent_digest: review.value.consent_digest } : {}) }, controller.signal)
    if (operation === controller && !controller.signal.aborted) { review.value = result; emit('changed') }
  } catch (e) { if (!controller.signal.aborted) error.value = e instanceof Error ? e.message : 'Could not update this watch.' }
  finally { if (operation === controller) busy.value = false }
}
watch(() => [identity.identity?.tenant.id, identity.identity?.principal.id, allowed.value], close)
const stopAccess = onAccessChange(() => close())
onBeforeUnmount(() => { close(); stopAccess(); stopLink() })
</script>

<template>
  <template v-if="allowed">
    <button class="btn attach-session" type="button" @click="open"><AppIcon name="eye" :size="15" />Attach session</button>
    <dialog ref="dialog" aria-labelledby="attach-title" @cancel.prevent="close" @click="event => { if (event.target === dialog) close() }">
      <header><h2 id="attach-title">{{ !review || metadataOnly ? 'Attach a running session' : 'Watch a running session' }}</h2><button class="close" type="button" aria-label="Close attach review" @click="close"><AppIcon name="close" :size="18" /></button></header>
      <form v-if="!review" @submit.prevent="lookup">
        <p>Enter the code from <code>aeon-agentd attach</code> on your paired computer.</p>
        <label for="attach-code">Attach code</label>
        <input id="attach-code" ref="codeInput" v-model="code" inputmode="numeric" autocomplete="off" placeholder="123 456 789" maxlength="15" :disabled="busy" />
        <footer><button type="submit" class="btn primary" :disabled="busy">{{ busy ? 'Checking…' : 'Review session' }}</button></footer>
      </form>
      <template v-else>
        <p class="request-warning">Requested by a process on {{ review.snapshot.host }}. <strong>{{ metadataOnly ? 'Only allow if you started this attach yourself.' : 'Only allow if you started this watch yourself.' }}</strong></p>
        <p class="host">{{ review.snapshot.host }} <span>· {{ review.snapshot.harness }}</span></p>
        <dl>
          <dt>Mode</dt><dd class="mode">{{ metadataOnly ? 'Status only (no conversation text)' : 'Watch the conversation' }}</dd>
          <dt>Project</dt><dd :title="review.snapshot.project_id" :class="{ quiet: !labelsReady }">{{ labelsReady ? project : 'Loading…' }}</dd>
          <dt>Ticket</dt><dd :title="review.snapshot.ticket_id" :class="{ quiet: !labelsReady }">{{ labelsReady ? ticket : 'Loading…' }}</dd>
          <dt>Folder</dt><dd class="path">{{ review.snapshot.process.cwd }}</dd>
          <dt>Process</dt><dd>PID {{ review.snapshot.process.pid }} · UID {{ review.snapshot.process.uid }}</dd>
          <dt>Executable</dt><dd class="path">{{ review.snapshot.process.executable }}</dd>
          <dt>Started</dt><dd class="path">{{ review.snapshot.process.started }}</dd>
          <template v-if="!metadataOnly"><dt>Transcript</dt><dd class="path" :title="review.snapshot.transcript"><template v-for="(segment, index) in review.snapshot.transcript.split('/')" :key="index">{{ index ? '/' : '' }}<wbr v-if="index" />{{ segment }}</template></dd>
          <dt>File identity</dt><dd class="path">{{ review.snapshot.file_id }}</dd></template>
        </dl>
        <details><summary><AppIcon name="chevron-right" class="disclosure-chev" :size="12" />Approval details</summary><dl>
          <dt>Project ID</dt><dd class="path">{{ review.snapshot.project_id }}</dd>
          <dt>Ticket ID</dt><dd class="path">{{ review.snapshot.ticket_id }}</dd>
          <dt>Snapshot</dt><dd class="path">{{ review.request_digest }}</dd>
        </dl><p class="limits">{{ metadataOnly ? 'Same-user processes are not isolated. Ancestry checks are defence in depth.' : 'The mirror is agent-written and unverified; same-user processes are not isolated. Ancestry checks are defence in depth.' }}</p></details>
        <p v-if="metadataOnly" class="consent">Session status only; no conversation text is read or shared.</p>
        <p v-else class="consent">New turns will be visible to people explicitly granted conversation access in this project, until you revoke.</p>
        <p v-if="!metadataOnly" class="limits">Only approve a single trust context; redaction is best effort.</p>
        <p v-if="otherComputerKeepsApproval" class="limits">Linux and a headless Mac keep this approval, and Touch ID is the default only when that Mac can use it.</p>
        <p v-if="localUnavailable" class="consent" role="status">Local confirmation is unavailable on this computer; this setting requires an updated paired Mac daemon.</p>
        <p v-else-if="strict && review.state === 'pending'" class="local-step">Next, confirm on {{ review.snapshot.host }} with Touch ID.</p>
        <p v-if="strict && review.state === 'approved'" role="status">Waiting for confirmation on {{ review.snapshot.host }}. Nothing is shared until you confirm there.</p>
        <p v-else-if="review.state === 'approved' || review.state === 'active'" role="status">{{ metadataOnly ? 'Approved. Keep the attach terminal open to report session status.' : 'Approved. Keep the attach terminal open to share new turns.' }}</p>
        <p v-else-if="review.state !== 'pending'" role="status">This watch ended. Start a new attach in your terminal to resume.</p>
        <footer v-if="review.state === 'pending'"><button class="btn" type="button" :disabled="busy" @click="decide(true)">Decline</button><button class="btn primary" type="button" :disabled="busy || localUnavailable || !labelsReady" @click="decide()">{{ busy ? 'Saving…' : strict ? 'Allow and confirm on Mac' : metadataOnly ? 'Allow attach' : 'Allow live watch' }}</button></footer>
        <footer v-else-if="review.state === 'approved' || review.state === 'active'"><button class="btn" type="button" :disabled="busy" @click="decide(true)">{{ metadataOnly ? 'Detach session' : 'Revoke watch' }}</button></footer>
      </template>
      <p v-if="error" role="alert">{{ error }}</p>
    </dialog>
  </template>
</template>

<style scoped>
dialog { width: min(520px, calc(100vw - 32px)); max-height: calc(100dvh - 48px); box-sizing: border-box; overflow-y: auto; padding: 24px; border: 1px solid var(--line-2); border-radius: 18px; color: var(--ink); background: var(--surface); box-shadow: 0 20px 64px var(--scrim); }
dialog::backdrop { background: var(--scrim); }
header { display: flex; justify-content: space-between; align-items: start; gap: 12px; }
h2 { font-size: 20px; line-height: 1.3; margin: 0; }
.close { display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0; width: 28px; height: 28px; padding: 0; border: 0; background: transparent; color: var(--ink-2); cursor: pointer; }
p { font-size: 13px; line-height: 1.6; margin: 16px 0; }
label { display: block; font-size: 12px; font-weight: 600; margin-top: 24px; margin-bottom: 6px; }
input { box-sizing: border-box; width: 100%; padding: 12px; font: 22px/1.3 ui-monospace, monospace; letter-spacing: .08em; border: 1px solid var(--line-2); border-radius: 8px; background: var(--field-bg); color: var(--ink); }
.request-warning { padding: 12px 14px; border-radius: 10px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); }
.request-warning strong { display: block; font-weight: 600; }
.local-step { font-weight: 500; }
.host { font-size: 16px; font-weight: 600; }
.host span, dt, .limits { color: var(--ink-2); font-weight: 400; }
dl { display: grid; grid-template-columns: 84px minmax(0, 1fr); gap: 9px 12px; font-size: 13px; line-height: 1.5; }
dd { margin: 0; overflow-wrap: anywhere; }
.mode { font-weight: 600; }
dd.quiet { color: var(--ink-3); }
.path { font: 12px/1.6 ui-monospace, monospace; }
summary::-webkit-details-marker { display: none; }
summary::marker { content: ""; }
.disclosure-chev { flex-shrink: 0; }
details[open] > summary .disclosure-chev { transform: rotate(90deg); }
summary { display: flex; align-items: center; gap: 6px; list-style: none; font-size: 12px; color: var(--ink-2); cursor: pointer; }
.consent { padding: 12px; border-radius: 8px; background: var(--surface-sunken); }
.limits { font-size: 12px; }
footer { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 10px; margin-top: 24px; }
.btn { display: inline-flex; align-items: center; justify-content: center; gap: 6px; }
@media (max-width: 440px) { dialog { padding: 20px; } dl { grid-template-columns: 70px minmax(0, 1fr); gap: 8px; } }
</style>
