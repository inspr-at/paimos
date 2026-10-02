<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { getNode } from '../../lib/api'
import { brand } from '../../lib/brand'
import { attachCopy } from '../../lib/attachCopy'
import { formatAttachCode, onAttachCode, takeAttachCode } from '../../lib/attachLink'
import { attachAction, attachOutcome, metadataOnlyAttach, type AttachReview } from '../../lib/attachWatch'
import { HARNESS_NAME } from '../../lib/capacity'
import { duration } from '../../lib/agentState'
import { can, ensurePermissions, onAccessChange } from '../../lib/authz'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { useAgents } from '../../stores/agents'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'

const identity = useSession()
const agents = useAgents()
const copy = computed(() => attachCopy(brand.value.short_name))
// A decision changes what /agents lists as waiting; the page refreshes it.
const props = defineProps<{ now: number }>()
const emit = defineEmits<{ changed: [result: AttachReview, declined: boolean] }>()
const allowed = computed(() => identity.identity?.principal.kind === 'person' && can('account.manage'))
// Nothing here awaits outside an identity scope (AEON-440). `link` belongs to the
// person who followed a link, before permissions are known, and to decisions that
// must finish after the dialog closes; `dialogScope` to the visible review while
// that person may manage accounts. Identity changes drop both scopes.
const link = useIdentityScope()
const dialogScope = useIdentityScope(() => allowed.value)
const lookups = dialogScope.lane()
const decisions = dialogScope.lane()
const labelReads = dialogScope.lane()
const dialog = ref<HTMLDialogElement>()
const codeInput = ref<HTMLInputElement>()
const code = ref('')
const fromLink = ref(false)
const enteredCode = ref('')
const choice = ref<'allow' | 'decline' | ''>('')
const declinedHere = ref(false)
const paper = ref<HTMLElement>()
const round = ref<AttachReview[]>([])
const roundIndex = ref(0)
const mac = /Mac|iPhone|iPad/.test(navigator.platform)
const review = ref<AttachReview | null>(null)
const outcome = computed(() => review.value ? attachOutcome(review.value, props.now) : null)
const pending = computed(() => outcome.value === 'waiting')
const harness = computed(() => review.value ? HARNESS_NAME[review.value.snapshot.harness] ?? review.value.snapshot.harness : '')
const expiry = computed(() => review.value ? duration(Date.parse(review.value.expires_at) - props.now) : '')
const soon = computed(() => !!review.value && Date.parse(review.value.expires_at) - props.now < 120_000)
const stamp = computed(() => outcome.value === 'approved' || review.value?.state === 'active' ? 'Allowed' : outcome.value === 'cancelled' ? declinedHere.value ? 'Declined' : 'Cancelled' : outcome.value === 'expired' ? 'Expired' : choice.value === 'allow' ? 'Allowed' : choice.value === 'decline' ? 'Declined' : 'Your call')
const canSubmit = computed(() => !busy.value && (!review.value ? /^\d{9}$/.test(code.value.replace(/[\s-]/g, '')) : pending.value && !!choice.value && (choice.value === 'decline' || labelsReady.value && !localUnavailable.value)))
const metadataOnly = computed(() => !!review.value && metadataOnlyAttach(review.value.snapshot))
const strict = computed(() => review.value?.consent_mode === 'local_auth')
const localUnavailable = computed(() => strict.value && review.value?.snapshot.platform !== 'darwin')
// Platform is the only signal on this review. A headless Mac is still darwin, so the line is for Linux and an unreported platform on a pairing that did not pin a key.
const otherComputerKeepsApproval = computed(() => !!review.value && !strict.value && review.value.snapshot.platform !== 'darwin')
const busy = ref(false)
const error = ref('')
const project = ref('')
const ticket = ref('')
// Approval waits until the person can read which project and ticket it covers.
const labelsReady = ref(false)
function close() {
  dialogScope.reset()
  dialog.value?.close(); code.value = ''; review.value = null; error.value = ''; busy.value = false; project.value = ''; ticket.value = ''; labelsReady.value = false; fromLink.value = false; enteredCode.value = ''; choice.value = ''; declinedHere.value = false; round.value = []; roundIndex.value = 0
}
function open() {
  close(); dialog.value?.showModal()
  void dialogScope.run(({ after }) => after(nextTick(), () => { codeInput.value?.focus() }))
}
// The link `aeon-agentd attach` prints only fills the code in; the person still
// reviews and approves. The router took it off the address bar and holds it in memory
// for the person it arrived for; waiting for permissions is part of the same scope, so
// a person who replaced them meanwhile never gets it.
function followLink() {
  if (!identity.identity) return Promise.resolve()
  const linked = takeAttachCode(link.owner.value)
  if (!linked) return Promise.resolve()
  return link.run(({ after }) => after(ensurePermissions(), permissions => {
    if (permissions !== 'known' || !allowed.value) return
    return after(nextTick(), () => { open(); code.value = formatAttachCode(linked); fromLink.value = true })
  }))
}
onMounted(followLink)
const stopLink = onAttachCode(followLink)
watch(() => !!identity.identity, followLink)
// Names are a convenience; approval binds the immutable IDs in the snapshot. A name
// that cannot be read is replaced by the ID itself. Loading never shares a lane
// with a decision, so deciding cannot cancel the names.
function loadLabels(result: AttachReview) {
  labelsReady.value = false; project.value = ''; ticket.value = ''
  const named = (names: PromiseSettledResult<{ key: string; title: string }>[]) => {
    project.value = names[0].status === 'fulfilled' ? names[0].value.title : result.snapshot.project_id
    ticket.value = names[1].status === 'fulfilled' ? `${names[1].value.key} · ${names[1].value.title}` : result.snapshot.ticket_id
    labelsReady.value = true
  }
  void labelReads.run(({ after }) => after(Promise.allSettled([getNode(result.snapshot.project_id), getNode(result.snapshot.ticket_id)]), named), {
    failed: () => named([{ status: 'rejected', reason: undefined }, { status: 'rejected', reason: undefined }]),
  })
}
function present(result: AttachReview) { review.value = result; code.value = ''; choice.value = ''; declinedHere.value = false; loadLabels(result) }
// Review a request the page already lists: same review, same digests, no code to type.
function focusPaper() { void dialogScope.run(({ after }) => after(nextTick(), () => paper.value?.focus({ preventScroll: true }))) }
function show(result: AttachReview, requests: AttachReview[] = []) {
  if (!allowed.value) return
  close(); dialog.value?.showModal()
  round.value = requests.filter(item => attachOutcome(item, props.now) === 'waiting')
  roundIndex.value = Math.max(0, round.value.findIndex(item => item.request_id === result.request_id))
  present(result); focusPaper()
}
function sync(requests: AttachReview[]) {
  if (!review.value || busy.value) return
  const current = requests.find(item => item.request_id === review.value?.request_id)
  // A poll issued before the decision can arrive after its accepted response.
  // Never turn that settled memo back into an actionable pending request.
  if (current && (review.value.state === 'pending' || current.state !== 'pending')) review.value = current
  round.value = round.value.map(item => {
    const next = requests.find(next => next.request_id === item.request_id)
    return next && (item.state === 'pending' || next.state !== 'pending') ? next : item
  })
}
function move(index: number) {
  const current = round.value[index]
  if (busy.value || !current) return
  dialogScope.reset(); error.value = ''; roundIndex.value = index; present(current)
  focusPaper()
}
function skip() {
  if (busy.value) return
  if (roundIndex.value + 1 < round.value.length) move(roundIndex.value + 1)
  else close()
}
function submit() {
  if (!canSubmit.value) return
  if (!review.value) void lookup()
  else void decide(choice.value === 'decline')
}
function field(target: EventTarget | null) {
  return target instanceof HTMLElement && target.matches('input:not([type=radio]):not([type=checkbox]), textarea, select, [contenteditable=true]')
}
function escape() {
  if (field(document.activeElement)) paper.value?.focus({ preventScroll: true })
  else close()
}
function keys(event: KeyboardEvent) {
  if (event.defaultPrevented || event.isComposing || event.repeat || event.altKey) return
  const inField = field(event.target)
  if (event.key === 'Escape' && !event.metaKey && !event.ctrlKey && !event.shiftKey) { event.preventDefault(); event.stopPropagation(); escape(); return }
  if (event.key === 'Enter') {
    const modifier = mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey
    if (event.shiftKey || (inField ? !modifier : event.metaKey || event.ctrlKey)) return
    // Native focused buttons and radios keep their own activation semantics.
    if (!inField && event.target instanceof HTMLElement && event.target.closest('button')) return
    event.preventDefault(); submit(); return
  }
  if (inField || event.metaKey || event.ctrlKey || event.shiftKey) return
  if (event.key.toLowerCase() === 's') { event.preventDefault(); skip() }
  else if (pending.value && (event.key === '1' || event.key === '2')) { event.preventDefault(); if (event.key === '2' || !localUnavailable.value) choice.value = event.key === '1' ? 'allow' : 'decline' }
  else if (event.key === 'ArrowLeft') { event.preventDefault(); move(roundIndex.value - 1) }
  else if (event.key === 'ArrowRight') { event.preventDefault(); move(roundIndex.value + 1) }
}
defineExpose({ show, sync })
function lookup() {
  const normalized = code.value.replace(/[\s-]/g, '')
  if (busy.value) return Promise.resolve()
  if (!allowed.value || !/^\d{9}$/.test(normalized)) { error.value = copy.value.invalidCode; return Promise.resolve() }
  return lookups.run(({ after, signal }) => {
    busy.value = true; error.value = ''
    return after(attachAction('/lookup', { user_code: normalized }, signal), result => { enteredCode.value = formatAttachCode(normalized); present(result); focusPaper() })
  }, {
    failed: e => { error.value = e instanceof Error ? e.message : 'Attach unavailable.' },
    settled: () => { busy.value = false },
  })
}
function decide(revoke = false) {
  if (!allowed.value || !review.value || busy.value || (!revoke && (!pending.value || localUnavailable.value || !labelsReady.value))) return Promise.resolve()
  const current = review.value
  return decisions.run(({ after: forDialog }) => {
    busy.value = true; error.value = ''
    return link.run(({ after, signal }) => after(attachAction(`/${encodeURIComponent(current.request_id)}/${revoke ? 'revoke' : 'approve'}`, revoke ? {} : { request_digest: current.request_digest, ...(current.consent_digest ? { consent_digest: current.consent_digest } : {}) }, signal), result => {
      // The POST and its body belong to the person, not the dialog: closing the
      // review cannot cancel an accepted write's canonical session/pending reads.
      // A changed identity still aborts it; only a live dialog uses its body.
      void agents.afterWrite()
      emit('changed', result, revoke && current.state === 'pending' && result.state === 'detached')
      return forDialog(result, answer => {
        if (round.value[roundIndex.value]) round.value[roundIndex.value] = answer
        review.value = answer; declinedHere.value = revoke && current.state === 'pending' && answer.state === 'detached'
        if (roundIndex.value + 1 < round.value.length) {
          roundIndex.value++; present(round.value[roundIndex.value]!)
        }
      })
    }))
  }, {
    failed: e => { error.value = e instanceof Error ? e.message : 'Could not update this watch.' },
    settled: () => { busy.value = false },
  })
}
// The same person refreshing their session keeps the review; a different person, workspace or right closes it.
watch(() => `${identity.identity?.tenant.id}/${identity.identity?.principal.id}/${allowed.value}`, close)
const stopAccess = onAccessChange(() => close())
onBeforeUnmount(() => { close(); stopAccess(); stopLink() })
</script>

<template>
  <template v-if="allowed">
    <button class="btn attach-session" type="button" @click="open"><AppIcon name="eye" :size="15" />{{ copy.attachSession }}</button>
    <dialog ref="dialog" class="desk-dlg" aria-labelledby="attach-title" @keydown="keys" @cancel.prevent="escape" @click="event => { if (event.target === dialog) close() }">
      <div class="desk">
        <header class="desk-top">
          <nav v-if="round.length > 1" class="round" aria-label="Attach requests in this round">
            <button type="button" class="icon-btn" aria-label="Previous request" :disabled="busy || roundIndex === 0" @click="move(roundIndex - 1)"><AppIcon name="chevron-left" :size="16" /></button>
            <span class="mono">{{ roundIndex + 1 }} / {{ round.length }}</span>
            <button type="button" class="icon-btn" aria-label="Next request" :disabled="busy || roundIndex + 1 === round.length" @click="move(roundIndex + 1)"><AppIcon name="chevron-right" :size="16" /></button>
          </nav>
          <span v-else class="desk-title">Attach session</span>
          <span class="desk-status" aria-live="polite">{{ !pending && review ? stamp : '' }}</span>
          <div class="desk-acts at-top">
            <button type="button" class="desk-btn ghost" :disabled="busy" @click="skip">Skip <kbd class="keycap">S</kbd></button>
            <button type="button" class="desk-btn primary" :disabled="!canSubmit" @click="submit"><AppIcon :name="review ? 'check' : 'arrow'" :size="15" /><span>{{ busy ? 'Saving…' : !review ? 'Find request' : roundIndex + 1 < round.length ? 'Decide & next' : 'Decide' }}</span><KeyCap v-if="!review" k="mod" /><KeyCap k="enter" /></button>
          </div>
          <button class="desk-x" type="button" aria-label="Close attach review" @click="close"><AppIcon name="close" :size="18" /></button>
        </header>
        <article ref="paper" class="paper" tabindex="-1">
          <template v-if="!review">
            <header class="memo-head">
              <div class="memo-headtext"><p class="memo-kind"><AppIcon name="link" :size="13" />{{ copy.attachTitle }}</p><h2 id="attach-title">Enter the code from your terminal</h2>
                <p v-if="fromLink" class="memo-mean"><AppIcon name="link" :size="14" /><span><strong>Filled in from your terminal link.</strong> Check it matches your terminal. Nothing is approved until you allow it.</span></p>
                <p v-else class="memo-mean"><AppIcon name="terminal" :size="14" /><span>Run <code>aeon-agentd attach</code> on your paired computer. It prints the code and a link that fills it in.</span></p>
              </div>
            </header>
            <div class="memo-body">
              <section class="memo-answer"><label class="eyebrow" for="attach-code">{{ copy.codeLabel }}</label><input id="attach-code" ref="codeInput" v-model="code" class="code-field" inputmode="numeric" autocomplete="off" placeholder="123 456 789" maxlength="15" :disabled="busy" /><p class="fine">Nine digits. Spaces are fine.</p></section>
              <aside class="margin" aria-label="Background"><section><h3 class="eyebrow">What the code does</h3><p>It picks out one running session on a computer you paired. It is only a lookup, never an approval.</p></section><section><h3 class="eyebrow">Next</h3><p>Find request opens it here, on the same desk, to allow or decline.</p><p>{{ copy.pairingHelp }}</p></section></aside>
            </div>
          </template>
          <template v-else>
            <header class="memo-head">
              <div class="memo-headtext">
                <p class="memo-kind held"><AppIcon name="clock" :size="13" />Approval · attach a session</p>
                <h2 id="attach-title">{{ !review || metadataOnly ? copy.attachTitle : copy.watchTitle }}</h2>
                <p class="memo-q">Attach {{ harness }} on {{ review.snapshot.host }}{{ metadataOnly ? ', status only' : ' and share its conversation' }}</p>
                <dl class="memo-meta"><dt>Project</dt><dd :title="review.snapshot.project_id">{{ labelsReady ? project : 'Loading…' }}</dd><dt>Ticket</dt><dd :title="review.snapshot.ticket_id">{{ labelsReady ? ticket : 'Loading…' }}</dd><dt>Started</dt><dd>{{ review.snapshot.process.started }}, in your terminal</dd><dt>Expires</dt><dd><time :class="{ soon }" :datetime="review.expires_at">{{ pending ? `Expires in ${expiry}` : stamp }}</time></dd></dl>
                <p v-if="pending" class="memo-mean park"><AppIcon name="clock" :size="14" /><span><strong>Your terminal waits for this.</strong> {{ copy.linkHelp }}</span></p>
                <p class="memo-mean"><AppIcon name="shield" :size="14" /><span>Requested by a process on {{ review.snapshot.host }}. <strong>{{ metadataOnly ? 'Only allow if you started this attach yourself.' : 'Only allow if you started this watch yourself.' }}</strong> {{ brand.short_name }} cannot see who typed the command.</span></p>
              </div>
              <div class="stamp-slot" aria-hidden="true"><span class="stamp" :class="{ empty: stamp === 'Your call', neutral: stamp === 'Expired' || stamp === 'Cancelled', declined: stamp === 'Declined' }">{{ stamp }}</span></div>
            </header>
            <div class="memo-body">
              <section class="memo-answer">
                <template v-if="pending">
                  <h3 id="attach-choice" class="eyebrow">Your decision</h3>
                  <div class="choices" role="radiogroup" aria-labelledby="attach-choice">
                    <label class="choice"><input v-model="choice" type="radio" name="attach-choice" value="allow" :disabled="busy || localUnavailable" aria-label="Allow" /><kbd class="keycap">1</kbd><span><strong>Allow</strong><span>{{ strict ? `Then confirm with Touch ID on ${review.snapshot.host}. Nothing is shared before that.` : 'Allow this session to link to its ticket.' }}</span></span></label>
                    <label class="choice"><input v-model="choice" type="radio" name="attach-choice" value="decline" :disabled="busy" aria-label="Decline" /><kbd class="keycap">2</kbd><span><strong>Decline</strong><span>Nothing is shared. The terminal says the request ended.</span></span></label>
                  </div>
                  <p v-if="localUnavailable" class="limits" role="status">Local confirmation is unavailable on this computer; this setting requires an updated paired Mac daemon.</p>
                </template>
                <template v-else>
                  <h3 class="eyebrow">{{ stamp }}</h3>
                  <p v-if="strict && outcome === 'approved'" role="status">Waiting for confirmation on {{ review.snapshot.host }}. Nothing is shared until you confirm there.</p>
                  <p v-else-if="review.state === 'approved' || review.state === 'active'" role="status">{{ metadataOnly ? 'Approved. Keep the attach terminal open to report session status.' : 'Approved. Keep the attach terminal open to share new turns.' }}</p>
                  <p v-else-if="outcome === 'expired'" role="status">Request expired. Run aeon-agentd attach again.</p>
                  <p v-else role="status">{{ declinedHere ? 'You declined it. Nothing was shared.' : 'Stopped in the terminal or declined in another tab. Nothing was shared.' }}</p>
                  <button v-if="outcome === 'approved' || review.state === 'active'" class="btn" type="button" :disabled="busy" @click="decide(true)">{{ metadataOnly ? 'Detach session' : 'Revoke watch' }}</button>
                </template>
              </section>
              <aside class="margin" aria-label="Background">
                <section v-if="enteredCode"><h3 class="eyebrow">{{ fromLink ? 'From your terminal link' : 'The code you entered' }}</h3><p class="code-line">{{ enteredCode }}</p><p>The same code your terminal shows.</p></section>
                <section><h3 class="eyebrow">What it shares</h3><p class="mode">{{ metadataOnly ? 'Status only (no conversation text)' : 'Watch the conversation' }}</p><p>{{ metadataOnly ? 'Session status only; no conversation text is read or shared.' : 'New turns will be visible to people explicitly granted conversation access in this project, until you revoke.' }}</p><p v-if="!metadataOnly" class="limits">Only approve a single trust context; redaction is best effort.</p></section>
                <section><h3 class="eyebrow">Which process asked</h3><dl><dt>Host</dt><dd>{{ review.snapshot.host }} · {{ review.snapshot.harness }}</dd><dt>Process</dt><dd>PID {{ review.snapshot.process.pid }} · UID {{ review.snapshot.process.uid }}</dd><dt>Folder</dt><dd class="path">{{ review.snapshot.process.cwd }}</dd><dt>Executable</dt><dd class="path">{{ review.snapshot.process.executable }}</dd><dt>Started</dt><dd class="path">{{ review.snapshot.process.started }}</dd><template v-if="!metadataOnly"><dt>Transcript</dt><dd class="path">{{ review.snapshot.transcript }}</dd><dt>File identity</dt><dd class="path">{{ review.snapshot.file_id }}</dd></template></dl><p>{{ copy.computerPaired }} · {{ review.state === 'active' ? copy.sessionLinked : copy.sessionUnlinked }}</p></section>
                <section><h3 class="eyebrow">Afterwards</h3><p v-if="strict">Touch ID on {{ review.snapshot.host }} confirms it. Revoke it any time from the session row.</p><p v-else>Revoke it any time from the session row.</p><p v-if="otherComputerKeepsApproval">This computer keeps approval in {{ brand.short_name }}.</p></section>
                <details><summary><AppIcon name="chevron-right" :size="12" />Approval details</summary><dl><dt>Project ID</dt><dd class="path">{{ review.snapshot.project_id }}</dd><dt>Ticket ID</dt><dd class="path">{{ review.snapshot.ticket_id }}</dd><dt>Snapshot</dt><dd class="path">{{ review.request_digest }}</dd></dl><p class="limits">{{ metadataOnly ? 'Same-user processes are not isolated. Ancestry checks are defence in depth.' : 'The mirror is agent-written and unverified; same-user processes are not isolated. Ancestry checks are defence in depth.' }}</p></details>
              </aside>
            </div>
          </template>
          <p v-if="error" class="error" role="alert">{{ error }}</p>
        </article>
        <div class="desk-acts at-bottom">
          <button type="button" class="desk-btn ghost" :disabled="busy" @click="skip">Skip</button>
          <button type="button" class="desk-btn primary" :disabled="!canSubmit" @click="submit"><AppIcon :name="review ? 'check' : 'arrow'" :size="15" /><span>{{ busy ? 'Saving…' : !review ? 'Find request' : roundIndex + 1 < round.length ? 'Decide & next' : 'Decide' }}</span></button>
        </div>
        <p class="desk-hint"><span><kbd class="keycap">1</kbd> <kbd class="keycap">2</kbd> choose</span><span><KeyCap v-if="!review" k="mod" /><KeyCap k="enter" /> {{ review ? 'decide' : 'find request' }}</span><span><kbd class="keycap">S</kbd> skip</span><span><kbd class="keycap">esc</kbd> leave field, then close</span></p>
      </div>
    </dialog>
  </template>
</template>

<style scoped>
.desk-dlg { width: min(1060px, calc(100vw - 32px)); max-width: none; max-height: none; margin: 7vh auto auto; padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.desk-dlg::backdrop { background: var(--scrim); backdrop-filter: blur(3px); }
.desk { --paper: var(--surface-raised); display: flex; flex-direction: column; max-height: calc(100dvh - 7vh - 16px); padding: 0 16px 14px; border: 1px solid var(--glass-edge); border-radius: 20px; background: linear-gradient(165deg, color-mix(in srgb, var(--aqua-3) 55%, var(--surface-raised-2)), var(--glass) 60%); backdrop-filter: blur(24px) saturate(1.2); box-shadow: var(--shadow-pop), var(--shadow); }
.desk-top { display: flex; align-items: center; gap: 12px; flex: none; height: 70px; padding: 8px 2px 0 4px; }
.desk-title { width: 150px; flex: none; padding-left: 10px; font: 600 11px/1 var(--mono); letter-spacing: .2em; text-transform: uppercase; color: var(--ink-3); }
.round { display: flex; justify-content: space-between; align-items: center; width: 150px; flex: none; }
.desk-status { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; text-align: right; font-size: 12.5px; color: var(--ink-2); }
.desk-acts { display: flex; align-items: center; gap: 10px; flex: none; }
.desk-acts.at-bottom { display: none; }
.desk-btn { display: inline-flex; align-items: center; justify-content: center; gap: 8px; height: 42px; padding: 0 18px; border: 0; border-radius: 999px; font-size: 13.5px; font-weight: 650; white-space: nowrap; }
.desk-btn.ghost { width: 128px; border: 1px solid var(--glass-edge); background: var(--btn-bg); box-shadow: var(--shadow-btn); }
.desk-btn.primary { width: 212px; background: linear-gradient(180deg, #f1d18f, #d69b31); color: #2a1c04; box-shadow: inset 0 1px 0 rgba(255, 255, 255, .55), 0 8px 18px -10px var(--scrim); }
.desk-btn.primary .keycap { color: #2a1c04; background: rgba(255, 255, 255, .35); }
.desk-btn:disabled { opacity: .4; }
.desk-x { display: grid; place-items: center; flex: none; width: 40px; height: 40px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); }
.desk-btn:focus-visible, .desk-x:focus-visible { box-shadow: var(--focus-ring); }
.paper { flex: 0 1 auto; min-height: 0; overflow: hidden auto; overscroll-behavior: contain; padding: 26px 32px 30px; border-radius: 12px; background: var(--paper); box-shadow: 0 0 0 1px var(--line-2); }
.paper:focus { outline: none; box-shadow: 0 0 0 1px var(--line-2); }
.memo-head { display: grid; grid-template-columns: minmax(0, 1fr) 210px; gap: 20px; padding-bottom: 18px; border-bottom: 1px solid var(--line); }
.memo-head:not(:has(.stamp-slot)) { grid-template-columns: minmax(0, 1fr); }
.memo-headtext { display: grid; gap: 10px; min-width: 0; }
.memo-kind { display: flex; align-items: center; gap: 7px; font: 600 10.5px/1.4 var(--mono); letter-spacing: .16em; text-transform: uppercase; color: var(--teal-ink); }
.memo-kind.held, .park, .soon { color: var(--gold-ink); }
h2, .memo-q { margin: 0; font: 500 24px/1.25 var(--serif); letter-spacing: -.016em; overflow-wrap: anywhere; }
.memo-q { font-size: 18px; }
p { margin: 0; font-size: 13px; line-height: 1.55; color: var(--ink-2); }
.memo-mean { display: flex; align-items: start; gap: 8px; }
.memo-mean svg { flex: none; margin-top: 3px; }
.memo-mean strong { color: var(--ink); }
.memo-meta { margin: 0; }
dl { display: grid; grid-template-columns: 84px minmax(0, 1fr); gap: 8px 12px; font-size: 13px; line-height: 1.5; }
dt { color: var(--ink-3); }
dd { margin: 0; overflow-wrap: anywhere; }
.memo-body { display: grid; grid-template-columns: minmax(0, 1.55fr) minmax(0, 1fr); gap: 36px; padding-top: 20px; }
.memo-answer { display: grid; gap: 20px; align-content: start; min-width: 0; }
h3 { margin: 0; }
.margin { display: grid; gap: 18px; align-content: start; min-width: 0; padding-left: 26px; border-left: 1px solid var(--line); }
.margin section { display: grid; gap: 6px; }
.path, .code-line { font: 12px/1.6 var(--mono); overflow-wrap: anywhere; }
.code-line { font-size: 15px; font-weight: 600; }
.code-field { width: 100%; height: 52px; padding: 0 2px; border: 0; border-bottom: 1.5px solid var(--line-2); border-radius: 0; background: transparent; color: var(--ink); font: 500 30px/1 var(--mono); letter-spacing: .12em; }
.code-field:focus { border-bottom-color: var(--teal); outline: none; box-shadow: none; }
.fine, .limits { font-size: 12px; color: var(--ink-3); }
.choices { margin: 0; padding: 0; list-style: none; border-top: 1px solid var(--line); }
.choices > .choice { border-bottom: 1px solid var(--line); }
.choice { display: grid; grid-template-columns: 18px 20px minmax(0, 1fr); gap: 12px; align-items: start; min-height: 96px; padding: 12px 10px; cursor: pointer; }
.choice:has(input:checked) { background: color-mix(in srgb, var(--teal) 9%, transparent); }
.choice input { margin: 3px 0 0; accent-color: var(--teal); }
.choice > span { display: grid; gap: 3px; font-size: 13px; line-height: 1.45; }
.choice strong { font-size: 14px; }
.stamp-slot { display: grid; place-items: center; min-height: 92px; }
.stamp { display: inline-flex; padding: 11px 16px; border: 2.5px solid var(--ok); border-radius: 6px; color: var(--ok); background: color-mix(in srgb, var(--ok) 7%, var(--paper)); box-shadow: inset 0 0 0 3px var(--paper), inset 0 0 0 4.5px var(--ok); font: 800 17px/1 var(--mono); letter-spacing: .18em; text-transform: uppercase; transform: rotate(-8deg); }
.stamp.empty { border-style: dashed; color: var(--ink-3); border-color: var(--ink-3); box-shadow: none; background: transparent; font-size: 12px; }
.stamp.declined { border-color: var(--danger); color: var(--danger); box-shadow: inset 0 0 0 3px var(--paper), inset 0 0 0 4.5px var(--danger); }
.stamp.neutral { border-color: var(--ink-3); color: var(--ink-3); box-shadow: none; }
summary { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--ink-2); cursor: pointer; list-style: none; }
summary::-webkit-details-marker { display: none; }
.desk-hint { display: flex; align-items: center; flex-wrap: wrap; gap: 16px; flex: none; margin: 12px 4px 0; font-size: 11.5px; color: var(--ink-3); }
.desk-hint > span { display: inline-flex; align-items: center; gap: 3px; }
.error { margin-top: 16px; color: var(--danger); }
@media (max-width: 720px) {
  .desk-acts.at-top { display: none; }
  .desk-status { display: none; }
  .desk-title, .round { flex: 1; }
  .desk-top { height: 56px; }
  .desk-acts.at-bottom { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.4fr); gap: 8px; padding-top: 10px; }
  .desk-acts.at-bottom .desk-btn { width: 100%; height: 48px; padding: 0 10px; font-size: 14.5px; }
  .desk-hint { display: none; }
  .paper { padding: 20px 18px 24px; flex: 1 1 auto; }
  .memo-head { grid-template-columns: minmax(0, 1fr) 104px; gap: 10px; }
  h2 { font-size: 20px; }
  .memo-body { grid-template-columns: minmax(0, 1fr); gap: 18px; }
  .margin { padding: 16px 0 0; border-left: 0; border-top: 1px solid var(--line); }
  .stamp-slot { min-height: 60px; place-items: start end; padding-top: 2px; }
  .stamp { padding: 8px 10px; border-width: 2px; font-size: 11.5px; letter-spacing: .12em; }
  .code-field { font-size: 24px; }
}
@media (max-width: 720px) {
  .desk-dlg { width: 100vw; height: 100dvh; margin: 0; }
  .desk { height: 100%; max-height: 100dvh; padding: env(safe-area-inset-top) 10px calc(10px + env(safe-area-inset-bottom)); border: 0; border-radius: 0; }
}
</style>
