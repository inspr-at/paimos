<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { APIError, api } from '../../lib/api'
import { messageStatuses, type MessageStatus, type ProjectMessage } from '../../lib/agents'
import { useAgents, type SessionView } from '../../stores/agents'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import SessionMessages from './SessionMessages.vue'
import SessionRequests from './SessionRequests.vue'
import { collapseMessages } from './sessionMessages'
import { awaitsInboxHook, hookDeliveryNotice, keepFailedReadMark, loadReadMark, markerFromServer, nearBottom, preferReadMark, queueReadMark, readMarkFlushDelay, saveReadMark, statusDone, unreadGroups, type ReadMark } from './sessionChat'

// The Messages tab of the session panel (AEON-273): the thread with a read
// watermark per viewer, a pinned bottom with a jump button, and the composer.
const props = defineProps<{ view: SessionView; now: number; canWrite: boolean; active: boolean }>()
const emit = defineEmits<{ unread: [count: number] }>()
const agents = useAgents()
const identity = useSession()
const me = computed(() => identity.identity?.principal.id ?? '')
const person = computed(() => identity.identity?.principal.kind === 'person')
const s = computed(() => props.view.session)
const messages = computed(() => agents.thread(s.value))
const address = computed(() => agents.addressOf(s.value.agent_principal_id))
// A registered address is sent when one exists. Otherwise the recipient is the session's principal.
const recipient = computed(() => address.value || s.value.agent_principal_id)
const current = computed(() => collapseMessages(messages.value))
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
const hookNotice = ref(false)

// ---------- Read state ----------
const mark = ref<ReadMark | null>(null)
const unread = computed(() => me.value ? unreadGroups(current.value, me.value, mark.value, ended.value) : [])
const newFrom = ref<string>()
const newCount = ref(0)
watch(() => unread.value.length, n => emit('unread', n), { immediate: true })
function markRead(event: number, id: string) {
  if (holdingMarker()) return
  if (!me.value || !id || !Number.isFinite(event)) return
  if (!mark.value || mark.value.event < event) mark.value = saveReadMark(me.value, s.value.id, event, id)
  if (!person.value || !mark.value) return
  boundProject = s.value.project_id
  boundSession = s.value.id
  const before = pending?.event ?? -1
  pending = queueReadMark(pending, mark.value)
  // A repeated look at the same post must not postpone a flush that is already waiting.
  if (flushTimer === undefined || (pending?.event ?? -1) > before) arm()
}
const readPath = (projectId: string, sessionId: string) =>
  `/projects/${encodeURIComponent(projectId)}/harness-sessions/${encodeURIComponent(sessionId)}/read-marker`
// One trailing timer. Hide and unmount flush immediately. A failed send stays
// in `pending` and leaves on the next flush, with no toast.
let pending: ReadMark | null = null
let flushTimer: ReturnType<typeof setTimeout> | undefined
let markSending = false
let flushAgain = false
let boundProject = ''
let boundSession = ''
let readGeneration = 0
// The first server read is in flight and this browser has no watermark yet.
// Screen observations must not mark the provisional bottom view as read.
let markerHold = 0
let awaitingServerPlacement = false
let userMoved = false
let placed = false
let adjusting = false
let anchorTop = 0
let placeChain: Promise<void> = Promise.resolve()
const holdingMarker = () => markerHold !== 0 && markerHold === readGeneration
function arm() {
  if (!pending || !person.value) return
  if (flushTimer !== undefined) clearTimeout(flushTimer)
  flushTimer = setTimeout(() => { flushTimer = undefined; void flush() }, readMarkFlushDelay)
}
function syncDivider() {
  if (!entered || holdingMarker()) {
    newFrom.value = undefined
    newCount.value = 0
    return
  }
  const first = unread.value[0]
  newFrom.value = first && current.value[0]?.id !== first.id ? first.id : undefined
  newCount.value = unread.value.length
}
async function sendMark(projectId: string, sessionId: string, viewer: string, generation: number, next: ReadMark) {
  const response = await api(readPath(projectId, sessionId), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ last_read_message_id: next.id, last_read_event_id: next.event }),
    keepalive: true,
  })
  if (!response.ok || generation !== readGeneration || s.value.id !== sessionId) return response.ok && generation === readGeneration
  const remote = markerFromServer(await response.json())
  if (remote && s.value.id === sessionId && (!mark.value || remote.event > mark.value.event)) {
    mark.value = saveReadMark(viewer, sessionId, remote.event, remote.id, undefined, remote.at)
  }
  return true
}
async function flush() {
  if (flushTimer !== undefined) { clearTimeout(flushTimer); flushTimer = undefined }
  if (!person.value || !pending) return
  if (markSending) { flushAgain = true; return }
  const next = pending
  const projectId = boundProject
  const sessionId = boundSession
  const viewer = me.value
  const generation = readGeneration
  pending = null
  markSending = true
  let ok = false
  try { ok = await sendMark(projectId, sessionId, viewer, generation, next) }
  catch { ok = false }
  finally { markSending = false }
  if (!ok && generation === readGeneration && s.value.id === sessionId) pending = keepFailedReadMark(pending, next)
  if (!flushAgain) return
  flushAgain = false
  if (pending) await flush()
}
function releaseMarkerHold(generation: number) {
  if (markerHold !== generation) return
  markerHold = 0
  const waiting = awaitingServerPlacement
  awaitingServerPlacement = false
  if (!entered || generation !== readGeneration) return
  syncDivider()
  if (waiting && !readerMoved()) void schedulePlace(generation)
  else observe()
}
async function pullReadMark(projectId: string, sessionId: string, viewer: string, generation: number) {
  try {
    const response = await api(readPath(projectId, sessionId))
    if (generation !== readGeneration || s.value.id !== sessionId) return
    if (!response.ok) {
      releaseMarkerHold(generation)
      return
    }
    const remote = markerFromServer(await response.json())
    const advanced = !!(remote && preferReadMark(mark.value, remote) === remote)
    if (advanced && remote) mark.value = saveReadMark(viewer, sessionId, remote.event, remote.id, undefined, remote.at)
    const waiting = awaitingServerPlacement && generation === readGeneration
    const held = markerHold === generation
    if (held) markerHold = 0
    if (waiting && entered && !readerMoved() && advanced) {
      awaitingServerPlacement = false
      await schedulePlace(generation)
    } else if (waiting || advanced) {
      if (waiting) awaitingServerPlacement = false
      syncDivider()
      if (waiting && entered) observe()
    }
    // The reader left the provisional bottom before the marker arrived.
    if (held && entered && placed && userMoved) observe()
    const current = mark.value
    if (!person.value || !current?.id || (remote && current.event <= remote.event)) return
    boundProject = projectId
    boundSession = sessionId
    pending = queueReadMark(pending, current)
    arm()
  } catch {
    // Offline: the local watermark stands, and a provisional landing may observe.
    releaseMarkerHold(generation)
  }
}
function repull() {
  if (!person.value || !me.value) return
  void pullReadMark(s.value.project_id, s.value.id, me.value, readGeneration)
}

// ---------- Scrolling ----------
const scroller = ref<HTMLElement>()
const textarea = ref<HTMLTextAreaElement>()
const distance = ref(0)
// Unread posts are always below the read ones, so they are what the jump button counts.
const below = computed(() => props.active ? unread.value.length : 0)
const jump = computed(() => distance.value > 160 || (below.value > 0 && distance.value > 32))
let stick = true, lastTop = 0, entered = false, loaded = false
// Finger origin for a touch. A move downward (clientY grows) scrolls the thread up.
let touchY: number | undefined
const reduced = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
const markerPending = () => holdingMarker() || awaitingServerPlacement
const upwardKey = (key: string) => key === 'ArrowUp' || key === 'PageUp' || key === 'Home'
// A thread that already fits has scrollTop 0 and produces no scroll event, so
// nothing would pin it again. An upward gesture unpins only when it can scroll.
const canScrollUp = () => (scroller.value?.scrollTop ?? 0) > 0
// Input runs before the browser queues scroll. Remember the reader so a marker
// resolved in that gap cannot reclaim the position. Clear the pinned bottom
// only for an upward gesture that can scroll, or for a real scroll while that
// marker is still pending. A tap or click never unpins.
function claimReader() {
  if (!props.active || !entered) return false
  userMoved = true
  awaitingServerPlacement = false
  return true
}
function onWheel(event: WheelEvent) {
  const pending = markerPending()
  if (!claimReader()) return
  if ((event.deltaY < 0 && canScrollUp()) || pending) stick = false
}
function onPointerDown() {
  claimReader()
}
function onTouchStart(event: TouchEvent) {
  const point = event.touches?.[0] ?? event.changedTouches?.[0]
  touchY = point?.clientY
  claimReader()
}
function onTouchMove(event: TouchEvent) {
  const point = event.touches?.[0] ?? event.changedTouches?.[0]
  if (!point || touchY === undefined || point.clientY <= touchY + 8) return
  const pending = markerPending()
  if (!claimReader()) return
  if (canScrollUp() || pending) stick = false
}
function onTouchEnd() {
  touchY = undefined
}
function onScrollKey(event: KeyboardEvent) {
  if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return
  if (!['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '].includes(event.key)) return
  const target = event.target
  if (target instanceof HTMLElement && target.closest('input, textarea, select, button, a, [contenteditable="true"]')) return
  const pending = markerPending()
  if (!claimReader()) return
  if ((upwardKey(event.key) && canScrollUp()) || pending) stick = false
}
function readerMoved() {
  // Also cover scrollbar/accessibility/programmatic scrolling whose scroll
  // event has not yet run. Our own placements update anchorTop synchronously.
  const el = scroller.value
  if (placed && el && !adjusting && Math.abs(el.scrollTop - anchorTop) > 2) {
    const pending = markerPending()
    const upward = el.scrollTop < anchorTop - 2
    if (claimReader() && (upward || pending)) stick = false
  }
  return userMoved
}
function onScroll() {
  const el = scroller.value
  if (!el) return
  const top = el.scrollTop
  if (nearBottom(el)) stick = true
  else if (top < lastTop - 2) stick = false
  lastTop = top
  distance.value = el.scrollHeight - top - el.clientHeight
  if (!entered || !placed) return
  if (Math.abs(top - anchorTop) <= 2 || adjusting) {
    if (adjusting) anchorTop = top
    return
  }
  userMoved = true
  awaitingServerPlacement = false
}
// The reader asked for the latest post: a late server mark must not pull them away.
function toBottom(smooth = false) {
  const el = scroller.value
  if (!el) return
  userMoved = true
  awaitingServerPlacement = false
  stick = true
  el.scrollTo({ top: el.scrollHeight, behavior: smooth && !reduced() ? 'smooth' : 'auto' })
  anchorTop = el.scrollTop
  if (!smooth || reduced()) onScroll()
}
// Keep a pinned bottom without treating the move as the reader's own scroll.
function keepBottom() {
  const el = scroller.value
  if (!el) return
  adjusting = true
  stick = true
  el.scrollTop = el.scrollHeight
  anchorTop = el.scrollTop
  lastTop = el.scrollTop
  distance.value = el.scrollHeight - el.scrollTop - el.clientHeight
  adjusting = false
}
function schedulePlace(generation: number) {
  const run = placeChain.then(() => placeThread(generation))
  placeChain = run.then(() => undefined, () => undefined)
  return run
}
// Reopening resumes at the first unread message. A first visit, with no mark on
// this browser or the server, shows the latest exchange. A server mark that
// arrives after that provisional landing moves the reader unless they scrolled.
async function placeThread(generation: number) {
  if (generation !== readGeneration || !entered || !props.active) return
  // Read displacement before inserting the divider: browser scroll anchoring
  // from that DOM change is not user intent. Input handlers remain synchronous
  // across the nextTick boundaries below.
  readerMoved()
  // The server mark can arrive while the thread paints. Sync again so the
  // divider and the scroll use that mark, not the provisional bottom.
  syncDivider()
  let synced = mark.value?.event ?? null
  await nextTick()
  if (generation !== readGeneration || !entered || !props.active) return
  if (userMoved) {
    if (!holdingMarker()) observe()
    return
  }
  if ((mark.value?.event ?? null) !== synced) {
    syncDivider()
    await nextTick()
    if (generation !== readGeneration || !entered || !props.active || userMoved) return
  }
  const el = scroller.value
  if (!el) return
  const first = unread.value[0]
  const target = mark.value ? el.querySelector<HTMLElement>('.new-divider') ?? (first ? el.querySelector<HTMLElement>(`.msg[data-id="${CSS.escape(first.id)}"]`) : null) : null
  adjusting = true
  if (target) {
    el.scrollTop = Math.max(0, target.getBoundingClientRect().top - el.getBoundingClientRect().top + el.scrollTop - 12)
    stick = nearBottom(el)
  } else {
    el.scrollTop = el.scrollHeight
    stick = true
  }
  anchorTop = el.scrollTop
  lastTop = el.scrollTop
  distance.value = el.scrollHeight - el.scrollTop - el.clientHeight
  adjusting = false
  placed = true
  if (holdingMarker()) awaitingServerPlacement = true
  else observe()
}
async function enter() {
  entered = true
  userMoved = false
  placed = false
  touchY = undefined
  await schedulePlace(readGeneration)
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
  if (stick) keepBottom()
  else onScroll()
  observe()
})

// ---------- Seen: in view while the tab and the page are visible ----------
let seen: IntersectionObserver | undefined
let resize: ResizeObserver | undefined
let observeGeneration = 0
function observe() {
  const el = scroller.value
  seen?.disconnect()
  if (!el || holdingMarker() || typeof IntersectionObserver === 'undefined') return
  const generation = ++observeGeneration
  seen = new IntersectionObserver(entries => {
    if (generation !== observeGeneration || holdingMarker()) return
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
function onVisibility() {
  if (document.visibilityState === 'hidden') { void flush(); return }
  repull()
  if (props.active && entered) observe()
}
function onFocus() { repull() }
onMounted(() => {
  document.addEventListener('visibilitychange', onVisibility)
  window.addEventListener('focus', onFocus)
  if (typeof ResizeObserver !== 'undefined' && scroller.value) {
    // The keyboard or a taller composer shrinks the thread: stay on the latest post.
    resize = new ResizeObserver(() => { if (props.active && entered && stick) keepBottom() })
    resize.observe(scroller.value)
  }
})
onBeforeUnmount(() => {
  seen?.disconnect(); resize?.disconnect()
  document.removeEventListener('visibilitychange', onVisibility)
  window.removeEventListener('focus', onFocus)
  void flush()
})

// ---------- Delivery status of the viewer's own posts (sender only, AEON-280) ----------
// One batched read; delivery events re-read it live, finished posts are not asked again.
const statuses = ref<Record<string, MessageStatus>>({})
let statusFlight: Promise<void> | undefined
let statusAgain = false
function refreshReceipts() {
  if (!props.active || !me.value) return
  if (statusFlight) { statusAgain = true; return }
  const todo = current.value.filter(m => m.sender_principal_id === me.value).slice(-30)
    .filter(m => !statusDone(statuses.value[m.id])).map(m => m.id)
  if (!todo.length) return
  const session = s.value.id
  statusFlight = messageStatuses(todo).then(page => {
    if (s.value.id !== session) return
    const next = { ...statuses.value }
    for (const item of page.items) next[item.message_id] = item
    statuses.value = next
  }).catch(() => { /* The status simply stays as it was. */ }).finally(() => {
    statusFlight = undefined
    if (statusAgain) { statusAgain = false; refreshReceipts() }
  })
}
watch(() => agents.deliveryPulse, () => refreshReceipts())

// ---------- Load and live refresh ----------
let refreshedAt = 0
async function refresh() {
  refreshedAt = Date.now()
  await agents.refreshThread(s.value.project_id, s.value.id)
}
watch([() => me.value, () => s.value.id], async ([viewer, id]) => {
  const generation = ++readGeneration
  if (flushTimer !== undefined) { clearTimeout(flushTimer); flushTimer = undefined }
  const leftover = pending
  const leftoverProject = boundProject
  const leftoverSession = boundSession
  pending = null
  if (leftover && person.value && leftoverSession && leftoverSession !== id) void sendMark(leftoverProject, leftoverSession, viewer, generation - 1, leftover).catch(() => undefined)
  const local = viewer ? loadReadMark(viewer, id) : null
  mark.value = local
  statuses.value = {}
  entered = false; loaded = false; newFrom.value = undefined; distance.value = 0; stick = true
  userMoved = false
  placed = false
  awaitingServerPlacement = false
  touchY = undefined
  markerHold = viewer && person.value && !local ? generation : 0
  seen?.disconnect()
  draft.value = ''; replyTo.value = null; sendError.value = ''; hookNotice.value = false
  const projectId = s.value.project_id
  boundProject = projectId
  boundSession = id
  // The thread renders from the messages read. The marker catches up after.
  if (viewer && person.value) void pullReadMark(projectId, id, viewer, generation)
  await refresh()
  if (generation !== readGeneration || s.value.id !== id) return
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
  // Only a message with no session still needs a registered target.
  if (!s.value.id && !address.value) return `${props.view.name} has no message address yet. It gets one when it registers a message target.`
  return ''
})
const showHookNotice = computed(() => hookNotice.value && awaitsInboxHook(s.value) && !current.value.some(m => m.sender_principal_id === me.value && (statuses.value[m.id]?.status === 'delivered' || statuses.value[m.id]?.status === 'read')))
async function send() {
  if (!draft.value.trim() || sending.value || composeBlock.value) return
  sending.value = true; sendError.value = ''
  try {
    await agents.send(s.value, recipient.value, draft.value.trim(), level.value, replyTo.value?.id)
    refreshedAt = Date.now()
    draft.value = ''; replyTo.value = null
    hookNotice.value = awaitsInboxHook(s.value)
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
      <div ref="scroller" class="thread-scroll" @scroll.passive="onScroll"
        @wheel.capture.passive="onWheel" @touchstart.capture.passive="onTouchStart"
        @touchmove.capture.passive="onTouchMove" @touchend.capture.passive="onTouchEnd"
        @touchcancel.capture.passive="onTouchEnd" @pointerdown.capture.passive="onPointerDown"
        @keydown.capture="onScrollKey">
        <SessionMessages :messages="messages" :principal-id="s.agent_principal_id" :now="now"
          :can-reply="canWrite && !composeBlock && !managed" :new-from="newFrom" :new-count="newCount" :statuses="statuses" @reply="reply" />
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
    <footer v-else class="composer">
      <SessionRequests v-if="unmanaged" :key="s.id" :session="s" :now="now" />
      <p v-if="composeBlock" class="compose-block"><AppIcon name="inbox" :size="13" />{{ composeBlock }}</p>
      <form v-else class="compose" @submit.prevent="send">
        <p v-if="showHookNotice" class="compose-block" role="status">{{ hookDeliveryNotice }}</p>
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
