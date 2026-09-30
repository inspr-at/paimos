<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { HarnessSession } from '../../lib/agents'
import { appendWatchText, attachAction, metadataOnlyAttach, watchText } from '../../lib/attachWatch'
import { can, onAccessChange } from '../../lib/authz'
import { useAgents } from '../../stores/agents'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ session: HarnessSession }>()
const identity = useSession()
const agents = useAgents()
const person = computed(() => identity.identity?.principal.kind === 'person')
const metadataOnly = computed(() => !!props.session.watch && metadataOnlyAttach(props.session.watch))
const allowed = computed(() => !metadataOnly.value && person.value && can('harness.watch', props.session.project_id))
const owner = computed(() => person.value && props.session.watch?.owner_id === identity.identity?.principal.id)
const text = ref('')
const state = ref<'idle' | 'connecting' | 'live' | 'ended'>('idle')
const revoked = ref(false)
const error = ref('')
const revoking = ref(false)
let stream: EventSource | undefined
let lastFrame = 0
let watchdog: ReturnType<typeof setInterval> | undefined
const available = computed(() => !revoked.value && props.session.watch?.state === 'active')
const watching = computed(() => state.value === 'connecting' || state.value === 'live')
function stop(ended = false) {
  stream?.close(); stream = undefined
  clearInterval(watchdog); watchdog = undefined
  text.value = ''; state.value = ended ? 'ended' : 'idle'
}
function start() {
  stop()
  if (!allowed.value || !available.value) return
  error.value = ''; state.value = 'connecting'; lastFrame = Date.now()
  const current = new EventSource(`/api/projects/${encodeURIComponent(props.session.project_id)}/harness-sessions/${encodeURIComponent(props.session.id)}/watch`)
  stream = current
  const fresh = () => stream === current && allowed.value && available.value
  current.addEventListener('keepalive', () => { if (!fresh()) return; lastFrame = Date.now(); state.value = 'live' })
  current.addEventListener('text', event => {
    if (!fresh()) return
    const chunk = watchText((event as MessageEvent).data)
    if (chunk === null) { stop(true); return }
    lastFrame = Date.now(); state.value = 'live'; text.value = appendWatchText(text.value, chunk)
  })
  current.addEventListener('end', () => { if (stream === current) stop(true) })
  current.onerror = () => { if (stream === current) stop(true) } // No automatic replay/rejoin.
  watchdog = setInterval(() => { if (Date.now() - lastFrame > 5000) stop(true) }, 1000)
}
async function revoke() {
  if (!owner.value || !props.session.watch) return
  const requestID = props.session.watch.request_id
  revoking.value = true; error.value = ''
  try { await attachAction(`/${encodeURIComponent(requestID)}/revoke`); void agents.afterWrite(); if (props.session.watch?.request_id === requestID) { revoked.value = true; stop(true) } }
  catch { if (props.session.watch?.request_id === requestID) error.value = 'Could not revoke the watch. Try again.' }
  finally { revoking.value = false }
}
watch(() => [props.session.id, identity.identity?.tenant.id, identity.identity?.principal.id], () => { stop(); revoked.value = false; error.value = '' })
watch([allowed, available], () => { if (!allowed.value || !available.value) stop(true) })
const stopAccess = onAccessChange(() => stop(true))
function hidden() { if (document.hidden) stop() }
document.addEventListener('visibilitychange', hidden)
onBeforeUnmount(() => { stop(); stopAccess(); document.removeEventListener('visibilitychange', hidden) })
</script>

<template>
  <section v-if="session.watch && (allowed || owner)" class="live-watch" :aria-label="metadataOnly ? 'Attached session' : 'Live conversation'">
    <div class="watch-head">
      <h3>{{ metadataOnly ? 'Attached session' : 'Live conversation' }}</h3>
      <button v-if="watching" class="btn small" type="button" @click="stop()">Stop viewing</button>
      <button v-else-if="allowed && available" class="btn small" type="button" @click="start"><AppIcon name="eye" :size="14" />Watch live</button>
    </div>
    <p v-if="metadataOnly" class="terms">Session status only; no conversation text is read or shared.</p>
    <p v-else class="terms">read-only · ends when the owner revokes</p>
    <template v-if="watching">
      <p class="provenance">Written by the agent, not verified.</p>
      <pre v-if="text" tabindex="0" aria-label="Agent-written live text">{{ text }}</pre>
      <p v-else class="waiting" role="status">{{ state === 'connecting' ? 'Connecting…' : 'Waiting for new turns…' }}</p>
    </template>
    <p v-else-if="session.watch.process_state === 'confirmed_exited'" class="waiting" role="status">Process exit confirmed.</p>
    <p v-else-if="metadataOnly && !available" class="waiting" role="status">{{ session.watch.state === 'unreachable' ? 'Session unreachable. Process exit is unconfirmed.' : 'Session detached. Process exit is unconfirmed.' }}</p>
    <p v-else-if="revoked || session.watch.state === 'detached'" class="waiting" role="status">Watch detached. Process exit is unconfirmed.</p>
    <p v-else-if="session.watch.state === 'unreachable'" class="waiting" role="status">Watch unreachable. Process exit is unconfirmed.</p>
    <p v-else-if="state === 'ended' || !available" class="waiting" role="status">Watch ended or unreachable. Process exit is unconfirmed.</p>
    <details v-if="!metadataOnly" class="limits"><summary><AppIcon name="chevron-right" class="disclosure-chev" :size="12" />Privacy and trust</summary><p>Only new turns are shared. Redaction is best effort; processes running as the same user are not isolated.</p></details>
    <button v-if="owner && available" class="revoke" type="button" :disabled="revoking" @click="revoke">{{ revoking ? 'Revoking…' : metadataOnly ? 'Detach session' : 'Revoke watch for everyone' }}</button>
    <p v-if="error" role="alert">{{ error }}</p>
  </section>
</template>

<style scoped>
.live-watch { margin: 20px 0; padding: 16px; background: var(--surface-sunken); border: 1px solid var(--line); border-radius: 12px; min-width: 0; }
.watch-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
h3 { margin: 0; font-size: 14px; font-weight: 600; }
p { margin: 8px 0 0; }
.terms, .provenance, .waiting, .limits { font-size: 12px; color: var(--ink-2); line-height: 1.6; }
.provenance { margin-top: 16px; }
pre { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 360px; overflow-y: auto; font: 12px/1.65 var(--font-mono, ui-monospace, monospace); padding: 12px; background: var(--surface); border: 1px solid var(--line); border-radius: 8px; }
.limits { margin-top: 12px; }
summary::-webkit-details-marker { display: none; }
summary::marker { content: ""; }
.disclosure-chev { flex-shrink: 0; }
details[open] > summary .disclosure-chev { transform: rotate(90deg); }
summary { display: flex; align-items: center; gap: 6px; list-style: none; cursor: pointer; }
.revoke { margin-top: 12px; padding: 0; border: 0; color: var(--ink-2); background: transparent; text-decoration: underline; text-underline-offset: 3px; cursor: pointer; font: inherit; font-size: 12px; }
.btn { display: inline-flex; align-items: center; justify-content: center; gap: 6px; }
</style>
