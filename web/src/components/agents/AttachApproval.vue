<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { getNode } from '../../lib/api'
import { attachAction, type AttachReview } from '../../lib/attachWatch'
import { can, onAccessChange } from '../../lib/authz'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'

const identity = useSession()
const allowed = computed(() => identity.identity?.principal.kind === 'person' && can('account.manage'))
const dialog = ref<HTMLDialogElement>()
const codeInput = ref<HTMLInputElement>()
const code = ref('')
const review = ref<AttachReview | null>(null)
const strict = computed(() => review.value?.consent_mode === 'local_auth')
const localUnavailable = computed(() => strict.value && review.value?.snapshot.platform !== 'darwin')
const busy = ref(false)
const error = ref('')
const project = ref('')
const ticket = ref('')
let operation: AbortController | undefined
function close() { operation?.abort(); operation = undefined; dialog.value?.close(); code.value = ''; review.value = null; error.value = ''; busy.value = false; project.value = ''; ticket.value = '' }
async function open() { close(); dialog.value?.showModal(); await nextTick(); codeInput.value?.focus() }
async function lookup() {
  const normalized = code.value.replace(/[\s-]/g, '')
  if (!allowed.value || !/^\d{9}$/.test(normalized)) { error.value = 'Enter the nine-digit code from your terminal.'; return }
  operation?.abort(); const controller = new AbortController(); operation = controller; busy.value = true; error.value = ''
  try {
    const result = await attachAction('/lookup', { user_code: normalized }, controller.signal)
    if (operation !== controller || controller.signal.aborted) return
    review.value = result; code.value = ''
    // Names are a convenience; approval binds the immutable IDs in the snapshot.
    const names = await Promise.allSettled([getNode(result.snapshot.project_id), getNode(result.snapshot.ticket_id)])
    if (operation !== controller || controller.signal.aborted) return
    project.value = names[0].status === 'fulfilled' ? names[0].value.title : 'Selected project'
    ticket.value = names[1].status === 'fulfilled' ? `${names[1].value.key} · ${names[1].value.title}` : 'Selected ticket'
  } catch (e) { if (!controller.signal.aborted) error.value = e instanceof Error ? e.message : 'Attach unavailable.' }
  finally { if (operation === controller) busy.value = false }
}
async function decide(revoke = false) {
  if (!allowed.value || !review.value || busy.value) return
  operation?.abort(); const controller = new AbortController(); operation = controller; busy.value = true; error.value = ''
  try {
    const result = await attachAction(`/${encodeURIComponent(review.value.request_id)}/${revoke ? 'revoke' : 'approve'}`, revoke ? {} : { request_digest: review.value.request_digest, ...(review.value.consent_digest ? { consent_digest: review.value.consent_digest } : {}) }, controller.signal)
    if (operation === controller && !controller.signal.aborted) review.value = result
  } catch (e) { if (!controller.signal.aborted) error.value = e instanceof Error ? e.message : 'Could not update this watch.' }
  finally { if (operation === controller) busy.value = false }
}
watch(() => [identity.identity?.tenant.id, identity.identity?.principal.id, allowed.value], close)
const stopAccess = onAccessChange(() => close())
onBeforeUnmount(() => { close(); stopAccess() })
</script>

<template>
  <template v-if="allowed">
    <button class="btn" type="button" @click="open"><AppIcon name="eye" :size="15" />Attach session</button>
    <dialog ref="dialog" aria-labelledby="attach-title" @cancel.prevent="close" @click="event => { if (event.target === dialog) close() }">
      <header><h2 id="attach-title">Watch a running session</h2><button class="close" type="button" aria-label="Close attach review" @click="close"><AppIcon name="close" :size="18" /></button></header>
      <form v-if="!review" @submit.prevent="lookup">
        <p>Enter the code from <code>aeon-agentd attach</code> on your paired computer.</p>
        <label for="attach-code">Attach code</label>
        <input id="attach-code" ref="codeInput" v-model="code" inputmode="numeric" autocomplete="off" placeholder="123 456 789" maxlength="15" :disabled="busy" />
        <footer><button type="submit" class="btn primary" :disabled="busy">{{ busy ? 'Checking…' : 'Review session' }}</button></footer>
      </form>
      <template v-else>
        <p class="request-warning">Requested by a process on {{ review.snapshot.host }}. <strong>Only allow if you started this watch yourself.</strong></p>
        <p class="host">{{ review.snapshot.host }} <span>· {{ review.snapshot.harness }}</span></p>
        <dl>
          <dt>Project</dt><dd :title="review.snapshot.project_id">{{ project }}</dd>
          <dt>Ticket</dt><dd :title="review.snapshot.ticket_id">{{ ticket }}</dd>
          <dt>Folder</dt><dd class="path">{{ review.snapshot.process.cwd }}</dd>
          <dt>Process</dt><dd>PID {{ review.snapshot.process.pid }} · UID {{ review.snapshot.process.uid }}</dd>
          <dt>Executable</dt><dd class="path">{{ review.snapshot.process.executable }}</dd>
          <dt>Started</dt><dd class="path">{{ review.snapshot.process.started }}</dd>
          <dt>Transcript</dt><dd class="path" :title="review.snapshot.transcript"><template v-for="(segment, index) in review.snapshot.transcript.split('/')" :key="index">{{ index ? '/' : '' }}<wbr v-if="index" />{{ segment }}</template></dd>
          <dt>File identity</dt><dd class="path">{{ review.snapshot.file_id }}</dd>
        </dl>
        <details><summary><AppIcon name="chevron-right" class="disclosure-chev" :size="12" />Approval details</summary><dl>
          <dt>Project ID</dt><dd class="path">{{ review.snapshot.project_id }}</dd>
          <dt>Ticket ID</dt><dd class="path">{{ review.snapshot.ticket_id }}</dd>
          <dt>Snapshot</dt><dd class="path">{{ review.request_digest }}</dd>
        </dl><p class="limits">The mirror is agent-written and unverified; same-user processes are not isolated.</p></details>
        <p class="consent">New turns will be visible to people explicitly granted conversation access in this project, until you revoke.</p>
        <p class="limits">Only approve a single trust context; redaction is best effort.</p>
        <p v-if="localUnavailable" class="consent" role="status">Local confirmation is unavailable on this computer; this setting requires an updated paired Mac daemon.</p>
        <p v-else-if="strict && review.state === 'pending'" class="local-step">Next, confirm on {{ review.snapshot.host }} with Touch ID or your Mac password.</p>
        <p v-if="strict && review.state === 'approved'" role="status">Waiting for confirmation on {{ review.snapshot.host }}. Nothing is shared until you confirm there.</p>
        <p v-else-if="review.state === 'approved' || review.state === 'active'" role="status">Approved. Keep the attach terminal open to share new turns.</p>
        <p v-else-if="review.state !== 'pending'" role="status">This watch ended. Start a new attach in your terminal to resume.</p>
        <footer v-if="review.state === 'pending'"><button class="btn" type="button" :disabled="busy" @click="decide(true)">Decline</button><button class="btn primary" type="button" :disabled="busy || localUnavailable" @click="decide()">{{ busy ? 'Saving…' : strict ? 'Allow and confirm on Mac' : 'Allow live watch' }}</button></footer>
        <footer v-else-if="review.state === 'approved' || review.state === 'active'"><button class="btn" type="button" :disabled="busy" @click="decide(true)">Revoke watch</button></footer>
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
