<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { APIError, api } from '../../lib/api'
import type { ProjectMessage } from '../../lib/agents'
import { attentionReasonText } from '../../lib/agentSignals'
import { useAgents, type SessionView } from '../../stores/agents'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import SessionMessages from './SessionMessages.vue'
import SessionRequests from './SessionRequests.vue'
import { belongsToSession, collapseMessages } from './sessionMessages'
import { loadReadMark, nearBottom, saveReadMark, unreadGroups, type InboxReceipt, type ReadMark } from './sessionChat'

// The Messages tab of the session panel (AEON-273): the thread with a read
// watermark per viewer, a pinned bottom with a jump button, and the composer.
const props = defineProps<{ view: SessionView; now: number; canWrite: boolean; active: boolean }>()
const emit = defineEmits<{ unread: [count: number] }>()
const agents = useAgents()
const identity = useSession()
const me = computed(() => identity.identity?.principal.id ?? '')
const s = computed(() => props.view.session)
const messages = computed(() => agents.thread(s.value))
const address = computed(() => agents.addressOf(s.value.agent_principal_id))
const current = computed(() => collapseMessages(messages.value.filter(m => belongsToSession(m, s.value.id))))
const ended = computed(() => s.value.phase === 'stopped' || !!s.value.stopped_at || !!s.value.archived_at)
// Managed sessions (AEON-260) take input only through the Session controls above:
// no composer and no Reply here.
const managed = computed(() => s.value.advertised_capabilities.includes('managed_control_v1'))
// Unmanaged sessions take rename/model requests next to the composer (AEON-225).
const unmanaged = computed(() => s.value.management_mode === 'unmanaged')

const draft = ref('')
const level = ref<'simple' | 'steer'>('simple')
const replyTo = ref<ProjectMessage | null>(null)
const sending = ref(false)
const sendError = ref('')

// ---------- Read state ----------
const mark = ref<ReadMark | null>(null)
const unread = computed(() => me.value ? unreadGroups(current.value, me.value, mark.value, ended.value) : [])
const newFrom = ref<string>()
const newCount = ref(0)
watch(() => unread.value.length, n => emit('unread', n), { immediate: true })
function markRead(event: number, id: string) {
  if (!me.value || !Number.isFinite(event) || (mark.value && mark.value.event >= event)) return
  mark.value = saveReadMark(me.value, s.value.id, event, id)
}

// Shared-inbox attention belongs to the agent principal, not this session: one quiet
// line above the thread.
const inboxNotes = computed(() => (s.value.attention_reasons ?? []).filter(r => r.scope === 'shared' || !r.blocking).map(r => {
  if (r.scope !== 'shared' || r.kind !== 'reply') return { code: attentionReasonText(r).code, text: attentionReasonText(r).detail }
  const actor = r.actor === 'agent' ? 'another agent' : r.actor === 'person' ? 'a person' : 'someone'
  return { code: `${r.scope}-${r.kind}-${r.actor}`, text: `Shared inbox: ${r.count} ${r.count === 1 ? 'reply' : 'replies'} outstanding, waiting for ${actor}.` }
}))

// ---------- Scrolling ----------
const scroller = ref<HTMLElement>()
const textarea = ref<HTMLTextAreaElement>()
const distance = ref(0)
// Unread posts are always below the read ones, so they are what the jump button counts.
const below = computed(() => props.active ? unread.value.length : 0)
const jump = computed(() => distance.value > 160 || (below.value > 0 && distance.value > 32))
let stick = true, lastTop = 0, entered = false, loaded = false
const reduced = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
function onScroll() {
  const el = scroller.value
  if (!el) return
  if (nearBottom(el)) stick = true
  else if (el.scrollTop < lastTop - 2) stick = false
  lastTop = el.scrollTop
  distance.value = el.scrollHeight - el.scrollTop - el.clientHeight
}
function toBottom(smooth = false) {
  const el = scroller.value
  if (!el) return
  stick = true
  el.scrollTo({ top: el.scrollHeight, behavior: smooth && !reduced() ? 'smooth' : 'auto' })
  if (!smooth || reduced()) onScroll()
}
// Opening the tab lands on the first unread message, or at the bottom. The "New"
// divider marks where unread starts, unless everything is new.
async function enter() {
  entered = true
  const first = unread.value[0]
  newFrom.value = first && current.value[0]?.id !== first.id ? first.id : undefined
  newCount.value = unread.value.length
  await nextTick()
  const el = scroller.value
  if (!el) return
  const target = el.querySelector<HTMLElement>('.new-divider') ?? (first ? el.querySelector<HTMLElement>(`.msg[data-id="${CSS.escape(first.id)}"]`) : null)
  if (target) {
    el.scrollTop = Math.max(0, target.getBoundingClientRect().top - el.getBoundingClientRect().top + el.scrollTop - 12)
    stick = nearBottom(el)
  } else toBottom()
  lastTop = el.scrollTop
  onScroll()
  observe()
}
watch(() => props.active, active => {
  if (active && loaded) void enter()
  if (!active) { entered = false; newFrom.value = undefined }
})
// New posts keep the view pinned when the reader is at the bottom; otherwise they
// only count toward the jump button.
watch(() => current.value.map(m => `${m.id}:${m.count}`).join(), async (_now, _before, onCleanup) => {
  let cancelled = false
  onCleanup(() => { cancelled = true })
  await nextTick()
  if (cancelled || !props.active || !entered) return
  if (stick) toBottom()
  else onScroll()
  observe()
})

// ---------- Seen: in view while the tab and the page are visible ----------
let seen: IntersectionObserver | undefined
let resize: ResizeObserver | undefined
function observe() {
  const el = scroller.value
  if (!el || typeof IntersectionObserver === 'undefined') return
  seen?.disconnect()
  seen = new IntersectionObserver(entries => {
    if (!props.active || document.visibilityState !== 'visible') return
    const rootHeight = el.clientHeight
    let best: { event: number; id: string } | undefined
    for (const entry of entries) {
      if (!entry.isIntersecting || (entry.intersectionRatio < 0.6 && entry.intersectionRect.height < rootHeight * 0.5)) continue
      const target = entry.target as HTMLElement
      const event = Number(target.dataset.event)
      if (!best || event > best.event) best = { event, id: target.dataset.id ?? '' }
    }
    if (best) markRead(best.event, best.id)
  }, { root: el, threshold: [0, 0.6, 1] })
  el.querySelectorAll<HTMLElement>('.msg[data-event]').forEach(node => seen!.observe(node))
}
function visibility() { if (document.visibilityState === 'visible' && props.active && entered) observe() }
onMounted(() => {
  document.addEventListener('visibilitychange', visibility)
  if (typeof ResizeObserver !== 'undefined' && scroller.value) {
    // The keyboard or a taller composer shrinks the thread: stay on the latest post.
    resize = new ResizeObserver(() => { if (props.active && entered && stick) toBottom() })
    resize.observe(scroller.value)
  }
})
onBeforeUnmount(() => { seen?.disconnect(); resize?.disconnect(); document.removeEventListener('visibilitychange', visibility) })

// ---------- Receipts for the viewer's own posts (sender only; 404 means none) ----------
const receipts = ref<Record<string, InboxReceipt>>({})
const noReceipt = new Set<string>()
let receiptFlight: Promise<void> | undefined
function refreshReceipts() {
  if (receiptFlight || !props.active || !me.value) return
  const todo = current.value.filter(m => m.sender_principal_id === me.value).slice(-8)
    .filter(m => !noReceipt.has(m.id) && !['handed_off', 'failed'].includes(receipts.value[m.id]?.state ?? ''))
  if (!todo.length) return
  const session = s.value.id
  receiptFlight = Promise.all(todo.map(async m => {
    try {
      const response = await api(`/inbox/messages/${encodeURIComponent(m.id)}/receipt`)
      if (!response.ok) { if (response.status < 500) noReceipt.add(m.id); return }
      const receipt = await response.json() as InboxReceipt
      if (s.value.id === session) receipts.value = { ...receipts.value, [m.id]: receipt }
    } catch { /* The tick simply stays away. */ }
  })).then(() => undefined).finally(() => { receiptFlight = undefined })
}

// ---------- Load and live refresh ----------
let refreshedAt = 0
async function refresh() {
  refreshedAt = Date.now()
  await agents.refreshThread(s.value.project_id)
}
watch([() => me.value, () => s.value.id], async ([viewer, id]) => {
  mark.value = viewer ? loadReadMark(viewer, id) : null
  receipts.value = {}; noReceipt.clear()
  entered = false; loaded = false; newFrom.value = undefined; distance.value = 0; stick = true
  draft.value = ''; replyTo.value = null; sendError.value = ''
  await refresh()
  if (s.value.id !== id) return
  loaded = true
  if (props.active) await enter()
  refreshReceipts()
}, { immediate: true })
// The agents page re-reads sessions on every wake and poll; the open thread follows.
watch(() => agents.sessionsUpdatedAt, () => { if (loaded && Date.now() - refreshedAt > 4000) void refresh().then(refreshReceipts) })
watch(() => props.active, active => { if (active) refreshReceipts() })

// ---------- Composer ----------
const composeBlock = computed(() => {
  if (ended.value || sendError.value === 'This session has ended.') return 'This session has ended.'
  if (agents.messagingState === 'error') return 'Messages could not be loaded right now. Close and reopen the session to try again.'
  if (agents.messagingState === 'forbidden') return 'Messages are open to workspace admins.'
  if (!address.value) return `${props.view.name} has no message address yet. It gets one when it registers a message target.`
  return ''
})
async function send() {
  if (!draft.value.trim() || sending.value || composeBlock.value) return
  sending.value = true; sendError.value = ''
  try {
    await agents.send(s.value, address.value, draft.value.trim(), level.value, replyTo.value?.id)
    refreshedAt = Date.now()
    draft.value = ''; replyTo.value = null
    await nextTick(); toBottom(true); refreshReceipts()
  } catch (e) { sendError.value = e instanceof APIError && e.status === 409 && e.body.code === 'session_ended' ? 'This session has ended.' : e instanceof Error ? e.message : 'The message was not sent. Please try again.' }
  finally { sending.value = false }
}
function composerKeys(event: KeyboardEvent) {
  if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) { event.preventDefault(); void send() }
}
function reply(message: ProjectMessage) {
  replyTo.value = message
  void nextTick(() => textarea.value?.focus())
}
defineExpose({ focusComposer: () => textarea.value?.focus() })
</script>

<template>
  <div class="session-chat">
    <div class="thread-wrap">
      <div ref="scroller" class="thread-scroll" @scroll.passive="onScroll">
        <section v-if="inboxNotes.length" class="inbox-note" aria-label="Inbox attention">
          <p v-for="note in inboxNotes" :key="note.code"><AppIcon name="inbox" :size="13" />{{ note.text }}</p>
        </section>
        <SessionMessages :messages="messages" :session-id="s.id" :principal-id="s.agent_principal_id" :address="address" :now="now"
          :can-reply="canWrite && !composeBlock && !managed" :new-from="newFrom" :new-count="newCount" :receipts="receipts" @reply="reply" />
      </div>
      <Transition name="jump">
        <button v-if="jump" type="button" class="jump" :class="{ labelled: below > 0 }"
          :aria-label="below > 0 ? `${below} new ${below === 1 ? 'message' : 'messages'}, go to the latest` : 'Go to the latest message'"
          :data-tip="below > 0 ? undefined : 'Latest message'" @click="toBottom(true)">
          <span v-if="below > 0" class="jump-count">{{ below }} new</span>
          <AppIcon name="chevron" :size="16" />
        </button>
      </Transition>
    </div>

    <p v-if="managed && !ended" class="managed-hint"><AppIcon name="send" :size="13" />Steer this managed session with the controls above.</p>
    <footer v-else-if="address || composeBlock || unmanaged" class="composer">
      <SessionRequests v-if="unmanaged" :key="s.id" :session="s" :now="now" />
      <p v-if="composeBlock" class="compose-block"><AppIcon name="inbox" :size="13" />{{ composeBlock }}</p>
      <form v-else class="compose" @submit.prevent="send">
        <p v-if="replyTo" class="replying"><span>Replying to “{{ replyTo.body.slice(0, 80) }}{{ replyTo.body.length > 80 ? '…' : '' }}”</span><button type="button" class="icon-btn sm flat" aria-label="Cancel the reply" @click="replyTo = null"><AppIcon name="close" :size="12" /></button></p>
        <label class="sr-only" :for="`compose-${s.id}`">Message to {{ view.name }}</label>
        <textarea :id="`compose-${s.id}`" ref="textarea" v-model="draft" class="field" rows="2" :placeholder="`Message ${view.name}…`" :disabled="!canWrite || sending" @keydown="composerKeys" @focus="toBottom(true)" />
        <p v-if="sendError" class="send-error" role="alert"><AppIcon name="alert" :size="12" />{{ sendError }}</p>
        <div class="compose-row">
          <div class="seg level" role="radiogroup" aria-label="Delivery">
            <button type="button" role="radio" :aria-checked="level === 'simple'" data-tip="Waits until the agent reads its inbox" @click="level = 'simple'">Simple</button>
            <button type="button" role="radio" :aria-checked="level === 'steer'" data-tip="Reaches the agent during its current turn" @click="level = 'steer'"><AppIcon name="bolt" :size="11" />Steer</button>
          </div>
          <span class="compose-hint" aria-hidden="true"><KeyCap k="mod" /><KeyCap k="enter" /></span>
          <button type="submit" class="btn sm primary send" :disabled="!draft.trim() || sending || !canWrite"><AppIcon name="send" :size="13" />{{ sending ? 'Sending…' : 'Send' }}</button>
        </div>
      </form>
    </footer>
  </div>
</template>

<style scoped>
.session-chat { flex: 1; display: flex; flex-direction: column; min-height: 0; min-width: 0; }
.thread-wrap { position: relative; flex: 1; min-height: 0; display: flex; flex-direction: column; }
.thread-scroll { flex: 1; min-height: 0; overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain; padding: 18px 24px 20px; }
.inbox-note { display: grid; gap: 4px; margin-bottom: 12px; }
.inbox-note p { display: flex; align-items: center; gap: 6px; min-width: 0; font-size: 12.5px; color: var(--ink-2); overflow-wrap: anywhere; }
.inbox-note svg { flex: none; color: var(--ink-3); }
.jump { position: absolute; right: 16px; bottom: 12px; display: inline-flex; align-items: center; justify-content: center; gap: 6px; min-width: 40px; height: 40px; padding: 0; border: 0; border-radius: 999px; background: var(--surface-raised); color: var(--ink-2); box-shadow: var(--shadow-pop); }
.jump svg { flex: none; }
.jump.labelled { padding: 0 12px 0 14px; color: var(--teal-ink); }
.jump-count { font-size: 12.5px; font-weight: 650; white-space: nowrap; }
@media (hover: hover) { .jump:hover { color: var(--ink); background: var(--btn-bg-hover); } }
.jump:focus-visible { outline: none; box-shadow: var(--shadow-pop), var(--focus-ring); }
.jump-enter-active, .jump-leave-active { transition: opacity .16s ease, transform .16s ease; }
.jump-enter-from, .jump-leave-to { opacity: 0; transform: translateY(6px); }
@media (prefers-reduced-motion: reduce) { .jump-enter-active, .jump-leave-active { transition: none; } }
.composer { flex-shrink: 0; padding: 10px 14px 12px; border-top: 1px solid var(--line); background: var(--surface-raised-2); border-radius: 0 0 var(--radius) var(--radius); }
.compose { display: grid; grid-template-columns: minmax(0, 1fr); gap: 8px; min-width: 0; }
.compose textarea { width: 100%; min-width: 0; min-height: 56px; max-height: 180px; resize: vertical; padding: 9px 11px; font: inherit; font-size: 13.5px; line-height: 1.45; }
.compose-row { display: flex; align-items: center; gap: 10px; min-width: 0; }
.level { flex-shrink: 0; }
.level button { display: inline-flex; align-items: center; gap: 4px; }
.compose-hint { margin-left: auto; display: inline-flex; gap: 2px; }
.compose-block { display: flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--ink-2); padding: 6px 2px; overflow-wrap: anywhere; }
.compose-block svg { flex: none; }
.managed-hint { flex-shrink: 0; display: flex; align-items: center; gap: 8px; margin: 0; padding: 10px 18px calc(10px + env(safe-area-inset-bottom)); border-top: 1px solid var(--line); font-size: 12.5px; color: var(--ink-2); overflow-wrap: anywhere; }
.managed-hint svg { flex: none; color: var(--ink-3); }
.replying { display: flex; align-items: center; gap: 6px; min-width: 0; padding: 4px 4px 4px 10px; border-radius: 8px; background: var(--code-bg); font-size: 12px; color: var(--ink-2); }
.replying span { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.send-error { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--danger); overflow-wrap: anywhere; }
@media (max-width: 720px) {
  .thread-scroll { padding: 14px 16px 16px; }
  .jump { right: 12px; }
  .composer { border-radius: 0; padding: 8px 12px calc(8px + env(safe-area-inset-bottom)); background: var(--surface-raised); }
  /* 16px keeps iOS Safari from zooming the page when the field takes focus. */
  .compose textarea { font-size: 16px; min-height: 48px; resize: none; }
  .compose-hint { display: none; }
  .compose-row .send { margin-left: auto; height: 40px; }
}
</style>
