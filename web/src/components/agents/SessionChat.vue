<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { displayLanguage } from '../../lib/displayLanguage'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { APIError } from '../../lib/api'
import { cancelSessionMessage, messageStatuses, type MessageStatus, type ProjectMessage } from '../../lib/agents'
import { readSessionMarker, writeSessionMarker } from '../../lib/agentRows'
import { useAgents, type SessionView } from '../../stores/agents'
import { useSession } from '../../stores/session'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import SessionMessages from './SessionMessages.vue'
import SessionRequests from './SessionRequests.vue'
import { collapseMessages } from './sessionMessages'
import { advanceReceipt, chatCapability, isQueuedMessage, chatSendLevel, chatWords, awaitsInboxHook, hookNoticeVisible, hookReceipts, keepFailedReadMark, loadReadMark, markerFromServer, nearBottom, preferReadMark, queueReadMark, readMarkFlushDelay, receiptQueryBatches, saveReadMark, sessionBoundSends, unreadGroups, type ReadMark } from './sessionChat'

// The Messages tab of the session panel (AEON-273): the thread with a read
// watermark per viewer, a pinned bottom with a jump button, and the composer.
const props = defineProps<{ view: SessionView; now: number; canWrite: boolean; active: boolean; interruptBlock: string }>()
const emit = defineEmits<{ unread: [count: number]; interrupt: [] }>()
const agents = useAgents()
const identity = useSession()
const words = computed(() => chatWords[displayLanguage()])
const me = computed(() => identity.identity?.principal.id ?? '')
const person = computed(() => identity.identity?.principal.kind === 'person')
const s = computed(() => props.view.session)
const localSends = ref<ProjectMessage[]>([])
// In-flight optimistic sends belong to the session they were addressed to.
// A late failure for A must not leave B looking busy (S8-013).
const sending = computed(() => localSends.value.some(message => message.optimistic && !message.send_failed && message.recipient_session_id === s.value.id))
const statuses = ref<Record<string, MessageStatus>>({})
const messages = computed(() => {
  if (!me.value) return []
  const server = agents.thread(s.value)
  const known = new Set(server.map(message => message.id))
  return [...server, ...localSends.value.filter(message => !known.has(message.id))]
})
const address = computed(() => agents.addressOf(s.value.agent_principal_id))
// A registered address is sent when one exists. Otherwise the recipient is the session's principal.
const recipient = computed(() => address.value || s.value.agent_principal_id)
const working = computed(() => s.value.activity === 'busy')
const capability = computed(() => chatCapability(s.value))
const atPrompt = computed(() => capability.value === 'between' && awaitsInboxHook(s.value))
const uncertain = ref<Set<string>>(new Set())
const queuedIds = ref<Set<string>>(new Set())
const queue = computed(() => messages.value.filter(message => message.sender_principal_id === me.value && !message.cancelled && !statuses.value[message.id]?.cancelled && isQueuedMessage({ ...message, queue_pending: message.queue_pending || queuedIds.value.has(message.id) }, statuses.value[message.id], working.value, atPrompt.value)))
const threadMessages = computed(() => {
  const queued = new Set(queue.value.map(message => message.id))
  return messages.value.filter(message => !message.cancelled && !queued.has(message.id) && !statuses.value[message.id]?.cancelled).sort((a, b) => {
    // Insert a live queue item when delivery is evidenced. Historical receipt
    // reads must not reorder already loaded posts or move a reader's anchor.
    const at = Date.parse((queuedIds.value.has(a.id) && statuses.value[a.id]?.delivered_at) || a.created_at || '')
    const bt = Date.parse((queuedIds.value.has(b.id) && statuses.value[b.id]?.delivered_at) || b.created_at || '')
    return Number.isFinite(at) && Number.isFinite(bt) && at !== bt ? at - bt : a.sent_event_id - b.sent_event_id
  })
})
const current = computed(() => collapseMessages(threadMessages.value))
const ended = computed(() => s.value.phase === 'stopped' || !!s.value.stopped_at || !!s.value.archived_at)
const canSteer = computed(() => working.value && ['native', 'next'].includes(capability.value) && s.value.advertised_capabilities.includes('steer'))
const alternative = computed(() => capability.value === 'next' ? words.value.next : words.value.now)
const primaryLabel = computed(() => editing.value ? words.value.save : working.value && capability.value !== 'between' ? words.value.after : words.value.send)
// Unmanaged sessions take rename/model requests next to the composer (AEON-225).
const unmanaged = computed(() => s.value.management_mode === 'unmanaged')

// A copy counts toward the pending cap only until the thread has its server id.
// The id swap can land after the read that already contains it, so both the
// thread watch and deliver drop it.
function forgetSettled() {
  const known = new Set(agents.thread(s.value).map(message => message.id))
  const next = localSends.value.filter(message => !known.has(message.id))
  if (next.length !== localSends.value.length) localSends.value = next
}
watch(() => agents.thread(s.value).map(message => message.id).join(), forgetSettled)
const draft = ref('')
const editing = ref<ProjectMessage | null>(null)
const queueBusy = ref(false)
// A missing navigator.onLine is not a reported outage. Only an explicit false,
// or a stale/offline session state, closes the composer (streamHealth).
const browserOnline = () => typeof navigator === 'undefined' || navigator.onLine !== false
const online = ref(browserOnline())
const offline = computed(() => !online.value || ['stale', 'offline'].includes(props.view.status?.state ?? ''))
const noPermission = computed(() => agents.threadState(s.value) === 'forbidden')
const replyTo = ref<ProjectMessage | null>(null)
const sendError = ref('')
const historyLoading = ref(false)
const historyError = ref('')
const canStop = computed(() => !ended.value && s.value.activity === 'busy' && !props.interruptBlock)
function stop() { if (props.active && canStop.value) emit('interrupt') }
function chatKeys(event: KeyboardEvent) {
  if (!props.active || event.key !== 'Escape' || event.repeat || event.defaultPrevented || event.isComposing || event.altKey || event.ctrlKey || event.metaKey) return
  if (document.querySelector('dialog[open], .floating, .ticket-peek-host')) return
  if (event.target instanceof HTMLElement && event.target !== document.body && !root.value?.contains(event.target)) return
  if (editing.value) { event.preventDefault(); event.stopPropagation(); editing.value = null; return }
  if (replyTo.value) { event.preventDefault(); event.stopPropagation(); replyTo.value = null; return }
  // The repo keyboard convention takes precedence over the mock: the first
  // Escape leaves a text field; the next press can interrupt the turn.
  const target = event.target
  if (target instanceof HTMLElement && target.closest('input, textarea, select, [contenteditable="true"]')) {
    event.preventDefault(); event.stopPropagation(); target.blur(); return
  }
  if (canStop.value) { event.preventDefault(); event.stopPropagation(); stop() }
}

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
  const response = await writeSessionMarker(projectId, sessionId, { last_read_message_id: next.id, last_read_event_id: next.event })
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
    const response = await readSessionMarker(projectId, sessionId)
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
const root = ref<HTMLElement>()
const textarea = ref<HTMLTextAreaElement>()
const distance = ref(0)
// Unread posts are always below the read ones, so they are what the jump button counts.
const below = computed(() => props.active ? unread.value.length : 0)
const jump = computed(() => distance.value > 160 || (below.value > 0 && distance.value > 32))
let stick = true, lastTop = 0, entered = false, loaded = false
let paging = false
async function earlier() {
  if (historyLoading.value) return
  const session = s.value, viewer = me.value, generation = readGeneration
  const el = scroller.value
  if (!el) return
  historyLoading.value = true; historyError.value = ''; paging = true
  stick = false
  try {
    await agents.earlierThread(session.project_id, session.id)
    if (generation !== readGeneration || viewer !== me.value || session.id !== s.value.id) return
    await nextTick()
    onScroll(); observe()
  } catch {
    if (generation === readGeneration) historyError.value = words.value.historyError
  } finally {
    if (generation === readGeneration) { historyLoading.value = false; paging = false }
  }
}
// Finger origin for a touch. A move downward (clientY grows) scrolls the thread up.
let touchY: number | undefined
const reduced = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
const markerPending = () => holdingMarker() || awaitingServerPlacement
// Shift+Space pages up, the same as PageUp. Space alone pages down.
const upwardKey = (event: KeyboardEvent) => event.key === 'ArrowUp' || event.key === 'PageUp' || event.key === 'Home' || (event.key === ' ' && event.shiftKey)
// A thread that already fits has scrollTop 0 and produces no scroll event, so
// nothing would pin it again. An upward gesture unpins only when it can scroll.
const canScrollUp = () => (scroller.value?.scrollTop ?? 0) > 0
// Input runs before the browser queues scroll. Remember the reader so a marker
// resolved in that gap cannot reclaim the position. Clear the pinned bottom
// only for an upward gesture that can scroll, or for a real scroll while that
// marker is still pending. A tap or click after placement never unpins.
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
function onPointerDown(event: PointerEvent) {
  if (markerPending() || (event.target as HTMLElement).closest('.code-fold')) stick = false
  claimReader()
}
function onTouchStart(event: TouchEvent) {
  const point = event.touches?.[0] ?? event.changedTouches?.[0]
  touchY = point?.clientY
  if (markerPending()) stick = false
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
  if (['Enter', ' '].includes(event.key) && (event.target as HTMLElement).closest('.code-fold')) stick = false
  if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return
  if (!['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '].includes(event.key)) return
  const target = event.target
  if (target instanceof HTMLElement && target.closest('input, textarea, select, button, a, [contenteditable="true"]')) return
  const pending = markerPending()
  if (!claimReader()) return
  if ((upwardKey(event) && canScrollUp()) || pending) stick = false
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
watch(() => current.value.map(m => `${m.id}:${m.count}:${m.body}`).join(), async (_now, _before, onCleanup) => {
  const el = scroller.value
  const anchor = el ? [...el.querySelectorAll<HTMLElement>('.msg')].find(node => node.getBoundingClientRect().bottom > el.getBoundingClientRect().top) : undefined
  const offset = anchor?.getBoundingClientRect().top
  let cancelled = false
  onCleanup(() => { cancelled = true })
  await nextTick()
  if (cancelled || !props.active || !entered) return
  if (stick && !paging) keepBottom()
  else {
    if (el && anchor?.isConnected && offset !== undefined) {
      adjusting = true
      el.scrollTop += anchor.getBoundingClientRect().top - offset
      anchorTop = el.scrollTop; lastTop = el.scrollTop
      adjusting = false
    }
    onScroll()
  }
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
  window.addEventListener('keydown', chatKeys, true)
  if (typeof ResizeObserver !== 'undefined' && scroller.value) {
    // The keyboard or a taller composer shrinks the thread: stay on the latest post.
    resize = new ResizeObserver(() => { if (props.active && entered && stick) keepBottom() })
    resize.observe(scroller.value)
    const content = scroller.value.querySelector('.session-messages')
    if (content) resize.observe(content)
  }
})
onBeforeUnmount(() => {
  seen?.disconnect(); resize?.disconnect()
  document.removeEventListener('visibilitychange', onVisibility)
  window.removeEventListener('focus', onFocus)
  window.removeEventListener('keydown', chatKeys, true)
  void flush()
})

// ---------- Delivery status of the viewer's own posts (sender only, AEON-280) ----------
// Outstanding sends are asked in batches the status route will accept. A
// delivered receipt on screen is asked again so it can become read, after
// every send that still has no terminal receipt.
// Session-bound sends from the loaded thread. Receipts, not memory, decide
// which of them still wait on the inbox hook, so a reload matches the server.
const boundHookIds = computed(() => sessionBoundSends(messages.value, s.value.id, me.value))
let statusFlight: Promise<void> | undefined
let statusAgain = false
function refreshReceipts() {
  if (!props.active || !me.value) return
  if (statusFlight) { statusAgain = true; return }
  const visible = current.value.filter(m => m.sender_principal_id === me.value).slice(-30).map(m => m.id)
  const batches = receiptQueryBatches(boundHookIds.value, visible, statuses.value)
  if (!batches.length) return
  const session = s.value.id, viewer = me.value, generation = readGeneration
  statusFlight = (async () => {
    for (const ids of batches) {
      const page = await messageStatuses(ids)
      if (s.value.id !== session || me.value !== viewer || readGeneration !== generation) return
      const next = { ...statuses.value }
      for (const item of page.items) next[item.message_id] = advanceReceipt(next[item.message_id], item)
      statuses.value = next
      const held = new Set(queuedIds.value)
      for (const message of messages.value) {
        if (message.sender_principal_id === me.value && isQueuedMessage(message, next[message.id], working.value, atPrompt.value)) held.add(message.id)
      }
      queuedIds.value = held
    }
  })().catch(() => { /* The status simply stays as it was. */ }).finally(() => {
    statusFlight = undefined
    if (statusAgain) { statusAgain = false; refreshReceipts() }
  })
}
watch(() => agents.deliveryPulse, () => refreshReceipts())

// ---------- Load and live refresh ----------
async function refresh() {
  await agents.refreshThread(s.value.project_id, s.value.id)
}
watch([() => me.value, () => s.value.id], async ([viewer, id], before) => {
  const generation = ++readGeneration
  if (flushTimer !== undefined) { clearTimeout(flushTimer); flushTimer = undefined }
  const leftover = pending
  const leftoverProject = boundProject
  const leftoverSession = boundSession
  pending = null
  if (leftover && person.value && before?.[0] === viewer && leftoverSession && leftoverSession !== id) void sendMark(leftoverProject, leftoverSession, viewer, generation - 1, leftover).catch(() => undefined)
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
  draft.value = ''; replyTo.value = null; editing.value = null; queueBusy.value = false; uncertain.value = new Set(); queuedIds.value = new Set(); sendError.value = ''; localSends.value = []
  historyLoading.value = false; historyError.value = ''; paging = false
  const projectId = s.value.project_id
  boundProject = projectId
  boundSession = id
  // The thread renders from the messages read. The marker catches up after.
  if (viewer && person.value) void pullReadMark(projectId, id, viewer, generation)
  if (viewer) await refresh()
  if (generation !== readGeneration || s.value.id !== id) return
  loaded = true
  if (props.active) await enter()
  refreshReceipts()
}, { immediate: true })
// Message hints refresh the open thread immediately; session reads retain the
// periodic recovery path. The store coalesces bursts with a trailing read.
watch([() => agents.threadPulse, () => agents.sessionsUpdatedAt], () => { if (loaded && props.active) void refresh().then(refreshReceipts) })
watch(() => props.active, active => { if (active && loaded) void refresh().then(refreshReceipts) })

// ---------- Composer ----------
const composeBlock = computed(() => {
  if (ended.value || sendError.value === words.value.ended) return words.value.ended
  if (agents.threadState(s.value) === 'error') return words.value.loadError
  if (agents.threadState(s.value) === 'forbidden') return words.value.noPermission
  // Only a message with no session still needs a registered target.
  if (!s.value.id && !address.value) return `${props.view.name} has no message address yet. It gets one when it registers a message target.`
  return ''
})
const hookReceipt = computed(() => hookReceipts(boundHookIds.value, statuses.value))
const showHookNotice = computed(() => unmanaged.value && hookNoticeVisible(awaitsInboxHook(s.value), hookReceipt.value))
async function deliver(local: ProjectMessage) {
  const session = s.value, viewer = me.value, generation = readGeneration
  const stillHere = () => generation === readGeneration && session.id === s.value.id && viewer === me.value
  local.send_failed = false
  try {
    const accepted = await agents.send(session, recipient.value, local.body, local.delivery_level, local.reply_to ?? undefined, local.client_id, local.resend_of)
    if (!stillHere()) return
    if (local.queue_pending) queuedIds.value = new Set([...queuedIds.value, accepted.id])
    localSends.value = localSends.value.map(message => message.client_id === local.client_id ? { ...accepted, client_id: local.client_id, queue_pending: local.queue_pending } : message)
    forgetSettled()
    refreshReceipts()
  } catch (error) {
    if (!stillHere()) return
    localSends.value = localSends.value.map(message => message.client_id === local.client_id ? { ...message, send_failed: true } : message)
    if (error instanceof APIError && error.status === 409 && error.body.code === 'session_ended') sendError.value = words.value.ended
  }
}
async function send(level: 'simple' | 'steer' = 'simple', source?: ProjectMessage): Promise<boolean> {
  const body = source?.body ?? draft.value.trim()
  if (!body || !props.canWrite || composeBlock.value || offline.value || queueBusy.value) return false
  if (editing.value && !source) return saveEdit()
  if (localSends.value.length >= 100) { toast(words.value.pendingLimit); return false }
  const clientId = crypto.randomUUID()
  const local: ProjectMessage = {
    id: clientId, client_id: clientId, optimistic: true, queue_pending: source?.queue_pending ?? (working.value || atPrompt.value), resend_of: source?.id, body, sender_principal_id: me.value,
    recipient_principal_id: s.value.agent_principal_id, recipient_session_id: s.value.id, to: recipient.value,
    sent_event_id: Number.MAX_SAFE_INTEGER, is_action_request: false, expects_reply: false,
    delivery_level: level, reply_to: source ? source.reply_to : replyTo.value?.id, status: 'accepted', reply_obligation: 'none', created_at: new Date().toISOString(),
  }
  if (local.queue_pending) queuedIds.value = new Set([...queuedIds.value, local.id])
  localSends.value = [...localSends.value, local]
  if (!source) { draft.value = ''; replyTo.value = null }
  void deliver(local)
  await nextTick(); if (stick) keepBottom()
  return true
}
function retry(message: ProjectMessage) {
  if (!props.canWrite || composeBlock.value || offline.value) return
  if (message.send_failed && message.client_id) {
    // An ambiguous POST reuses its idempotency key. A confirmed terminal
    // delivery failure is a new explicit send, never automatic reinjection.
    localSends.value = localSends.value.map(item => item.client_id === message.client_id ? { ...item, send_failed: false } : item)
    void deliver(message)
  } else if (statuses.value[message.id]?.status === 'not_delivered') {
    void send(message.delivery_level, message)
  }
}
function queueWhen(message: ProjectMessage) {
  if (uncertain.value.has(message.id)) return words.value.unclear
  if (message.send_failed || statuses.value[message.id]?.status === 'not_delivered') return words.value.failed
  if (capability.value === 'between') return words.value.queueBetween
  return message.delivery_level === 'steer' ? capability.value === 'next' ? words.value.queueNext : words.value.queueNow : words.value.after
}
function editQueued(message: ProjectMessage) {
  if (queueBusy.value || !props.canWrite || message.optimistic) return
  editing.value = { ...message }; replyTo.value = null; draft.value = message.body
  void nextTick(() => textarea.value?.focus())
}
async function cancelQueued(message: ProjectMessage, forEdit = false) {
  if (queueBusy.value || !props.canWrite || message.optimistic) return false
  const session = s.value, viewer = me.value, generation = readGeneration
  queueBusy.value = true
  try {
    const result = await cancelSessionMessage(session.project_id, message.id)
    if (generation !== readGeneration || session.id !== s.value.id || viewer !== me.value) return false
    if (result.result === 'cancelled') {
      statuses.value = { ...statuses.value, [message.id]: { message_id: message.id, status: 'not_delivered', cancelled: true, delivered_at: null, read_at: null, deliver_by: statuses.value[message.id]?.deliver_by ?? null } }
      if (!forEdit) { toast(words.value.cancelled); if (editing.value?.id === message.id) editing.value = null }
      return true
    }
    if (result.result === 'uncertain') uncertain.value = new Set([...uncertain.value, message.id])
    toast(result.result === 'too_late' ? words.value.tooLate : words.value.unclear)
    refreshReceipts()
    return false
  } catch {
    if (generation === readGeneration) toast(words.value.cancelFailed, { tone: 'error' })
    return false
  } finally { if (generation === readGeneration) queueBusy.value = false }
}
async function saveEdit(): Promise<boolean> {
  const message = editing.value, body = draft.value.trim(), generation = readGeneration
  if (!message || !body) return false
  const cancelled = await cancelQueued(message, true)
  if (generation !== readGeneration || editing.value?.id !== message.id) return false
  // too_late and uncertainty keep the text and leave edit mode, matching the accepted queue flow.
  if (!cancelled) { editing.value = null; return false }
  const enqueued = await send(message.delivery_level, { ...message, body, queue_pending: true })
  if (generation !== readGeneration || editing.value?.id !== message.id) return false
  // A refused replacement must stay editable. The cancel already happened, so the next Save retries it.
  if (!enqueued) return false
  editing.value = null
  if (draft.value.trim() === body) draft.value = ''
  return true
}
async function stopAndSend() {
  if (!canStop.value || !draft.value.trim() || editing.value || queueBusy.value || offline.value) return
  const view = props.view, generation = readGeneration, body = draft.value.trim(), parent = replyTo.value?.id
  queueBusy.value = true
  try {
    await agents.control(view, 'interrupt')
    if (generation !== readGeneration || view.session.id !== s.value.id) return
    queueBusy.value = false
    // After-turn delivery waits for the explicit interrupt to end the turn.
    const enqueued = await send('simple', { body, reply_to: parent, queue_pending: true } as ProjectMessage)
    if (generation !== readGeneration || view.session.id !== s.value.id) return
    if (enqueued && draft.value.trim() === body) { draft.value = ''; replyTo.value = null }
  } catch {
    if (generation === readGeneration) toast(words.value.cancelFailed, { tone: 'error' })
  } finally { if (generation === readGeneration) queueBusy.value = false }
}
function connectionChanged() { online.value = browserOnline(); if (online.value) void refresh().then(refreshReceipts) }
onMounted(() => { window.addEventListener('online', connectionChanged); window.addEventListener('offline', connectionChanged) })
onBeforeUnmount(() => { window.removeEventListener('online', connectionChanged); window.removeEventListener('offline', connectionChanged) })
function suggest(text: string) { if (props.canWrite && !composeBlock.value) { draft.value = text; void nextTick(() => textarea.value?.focus()) } }
function composerKeys(event: KeyboardEvent) {
  const level = chatSendLevel(event, canSteer.value)
  if (level) { event.preventDefault(); void send(level) }
}
function reply(message: ProjectMessage) {
  editing.value = null
  replyTo.value = message
  void nextTick(() => textarea.value?.focus())
}
defineExpose({ focusComposer: () => textarea.value?.focus() })
</script>

<template>
  <div ref="root" class="session-chat">
    <div class="thread-wrap">
      <div ref="scroller" class="thread-scroll" @scroll.passive="onScroll"
        @wheel.capture.passive="onWheel" @touchstart.capture.passive="onTouchStart"
        @touchmove.capture.passive="onTouchMove" @touchend.capture.passive="onTouchEnd"
        @touchcancel.capture.passive="onTouchEnd" @pointerdown.capture.passive="onPointerDown"
        @keydown.capture="onScrollKey">
        <div class="history-head">
          <button v-if="agents.threadMore[s.id]" type="button" class="earlier" :disabled="historyLoading" @click="earlier">{{ words.older }}</button>
          <span v-else-if="agents.threadState(s) === 'ready' && messages.length">{{ words.start }}</span>
          <span v-if="historyError" class="history-error" role="alert">{{ historyError }}</span>
        </div>
        <div v-if="noPermission" class="empty-state"><AppIcon name="lock" :size="32" /><h3>{{ words.noPermissionTitle }}</h3><p>{{ words.noPermission }}</p></div>
        <div v-else-if="agents.threadState(s) === 'ready' && !messages.length" class="empty-state"><AppIcon name="agent" :size="32" /><h3>{{ words.emptyTitle }} {{ view.name }}</h3><p>{{ words.emptyBody }}</p><div class="suggestions"><button v-for="text in [words.suggest1, words.suggest2, words.suggest3]" :key="text" type="button" :disabled="!canWrite || ended" @click="suggest(text)">{{ text }}</button></div></div>
        <SessionMessages v-else :messages="threadMessages" :principal-id="s.agent_principal_id" :session-id="s.id" :now="now"
          :can-reply="canWrite && !composeBlock" :new-from="newFrom" :new-count="newCount" :statuses="statuses" :queued-ids="queuedIds" @reply="reply" @retry="retry" />
      </div>
        <p v-if="editing" class="replying" role="status"><AppIcon name="edit" :size="13" /><span>{{ words.editing }}</span><button type="button" class="icon-btn sm flat" :aria-label="words.cancel" @click="editing = null"><AppIcon name="close" :size="12" /></button></p>
        <p v-else-if="replyTo" class="replying"><span :title="replyTo.body">{{ words.replying }} “{{ replyTo.body }}”</span><button type="button" class="icon-btn sm flat" :aria-label="words.cancelReply" @click="replyTo = null"><AppIcon name="close" :size="12" /></button></p>
      <p v-if="offline" class="offline-banner" role="status"><AppIcon name="refresh" :size="14" />{{ words.offline }}</p>
      <Transition name="jump">
        <button v-if="jump" type="button" class="jump" :class="{ labelled: below > 0 }"
          :aria-label="below > 0 ? `${words.newCount(below)}, ${words.latest}` : words.latest"
          :data-tip="below > 0 ? undefined : 'Latest message'" @click="toBottom(true)">
          <AppIcon name="arrow-down" :size="16" />
          <span v-if="below > 0" class="jump-count">{{ words.newCount(below) }}</span>
        </button>
      </Transition>
    </div>

    <footer v-if="!noPermission" class="composer">
      <SessionRequests v-if="unmanaged" :key="s.id" :session="s" :now="now" />
      <ul v-if="queue.length" class="chat-queue" :aria-label="words.queued">
        <li v-for="message in queue" :key="message.id" class="queue-item" :class="{ editing: editing?.id === message.id }" :data-id="message.id" :data-status="message.send_failed ? 'not_delivered' : statuses[message.id]?.status ?? 'sent'">
          <AppIcon :name="message.delivery_level === 'steer' ? 'bolt' : 'queue'" :size="14" />
          <span class="queue-text" tabindex="0" :data-tip="message.body" :title="message.body">{{ message.body }}</span><span class="queue-when">{{ queueWhen(message) }}</span>
          <button v-if="message.send_failed || statuses[message.id]?.status === 'not_delivered'" type="button" class="icon-btn flat" :aria-label="words.retry" :disabled="queueBusy || !canWrite || offline" @click="retry(message)"><AppIcon name="refresh" :size="14" /></button>
          <button type="button" class="icon-btn flat" :aria-label="words.edit" :disabled="queueBusy || !canWrite || message.optimistic || offline" @click="editQueued(message)"><AppIcon name="edit" :size="14" /></button>
          <button type="button" class="icon-btn flat" :aria-label="words.cancel" :disabled="queueBusy || !canWrite || message.optimistic || offline" @click="cancelQueued(message)"><AppIcon name="close" :size="14" /></button>
        </li>
      </ul>
      <p v-if="composeBlock" class="compose-block"><AppIcon name="inbox" :size="13" />{{ composeBlock }}</p>
      <form v-else class="compose" @submit.prevent="send()">
        <p v-if="showHookNotice" class="compose-block" role="status">{{ view.name }} {{ words.unmanagedNote }}</p>

        <label class="sr-only" :for="`compose-${s.id}`">{{ words.message }} {{ view.name }}</label>
        <textarea :id="`compose-${s.id}`" ref="textarea" v-model="draft" class="field" rows="2" :placeholder="`${words.placeholder} ${view.name}`" :disabled="!canWrite" @keydown="composerKeys" @focus="toBottom(true)" />
        <p v-if="capability === 'abort'" class="gemini-note">{{ words.geminiNote }}</p>
        <p v-if="sendError" class="send-error" role="alert"><AppIcon name="alert" :size="12" />{{ sendError }}</p>
        <div class="compose-row">
          <span class="compose-hint"><KeyCap k="shift" /><KeyCap k="enter" />{{ words.newline }}</span>
          <button v-if="canSteer" type="button" class="btn sm steer-send" :aria-label="alternative" :aria-busy="sending" :disabled="!draft.trim() || !canWrite || queueBusy || !!editing || offline" @click="send('steer')"><AppIcon name="bolt" :size="13" />{{ alternative }}<KeyCap k="mod" /><KeyCap k="enter" /></button>
          <button v-if="capability === 'abort' && working" type="button" class="btn sm stop-send" :aria-label="words.stopSend" :aria-busy="sending" :disabled="!draft.trim() || !canWrite || queueBusy || !!editing || !canStop || offline" @click="stopAndSend"><AppIcon name="alert" :size="13" />{{ words.stopSend }}</button>
          <button type="submit" class="btn sm primary send" :aria-label="primaryLabel" :aria-busy="sending" :disabled="!draft.trim() || !canWrite || queueBusy || offline"><AppIcon name="send" :size="13" /><span class="send-label"><span>{{ primaryLabel }}</span><span aria-hidden="true" class="label-size">{{ words.after }}</span></span><KeyCap k="enter" /></button>
          <button v-if="s.management_mode === 'managed' && s.advertised_capabilities.includes('interrupt')" type="button" class="btn sm chat-stop" :aria-label="words.stop" :data-tip="canStop ? words.stopTip : interruptBlock || words.stopTip" :disabled="!canStop" @click="stop"><AppIcon name="stop" :size="13" /><span>{{ words.stop }}</span><KeyCap k="Esc" /></button>
        </div>
      </form>
    </footer>
  </div>
</template>

<style scoped>
.offline-banner { position: absolute; z-index: 2; top: 8px; left: 50%; transform: translateX(-50%); display: flex; align-items: center; gap: 6px; padding: 6px 10px; border-radius: 999px; background: var(--surface-raised); box-shadow: var(--shadow-pop); font-size: 12px; white-space: nowrap; }
.empty-state { display: grid; justify-items: center; gap: 10px; max-width: 340px; margin: 24px auto; text-align: center; color: var(--ink-2); }
.empty-state h3 { font-size: 17px; color: var(--ink); }
.empty-state p { font-size: 13.5px; }
.suggestions { display: grid; justify-items: center; }
.suggestions button { min-height: 44px; border: 0; background: none; color: var(--teal-ink); font: inherit; font-size: 13px; }
.chat-queue { display: grid; gap: 6px; max-height: 180px; overflow-y: auto; padding: 0; margin: 0 0 8px; list-style: none; }
.queue-item { display: flex; align-items: center; gap: 6px; min-height: 44px; min-width: 0; padding: 2px 3px 2px 10px; border-radius: 12px; background: var(--code-bg); box-shadow: inset 0 0 0 1px var(--line); }
.queue-item > svg { flex: none; color: var(--ink-3); }
.queue-item.editing { background: var(--chip-teal-bg); }
.queue-text { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
.queue-when { flex: 0 1 auto; font-size: 11.5px; color: var(--ink-3); }
.queue-item .icon-btn { flex: none; width: 44px; height: 44px; }
.send-label { display: grid; }
.send-label > span { grid-area: 1 / 1; }
.label-size { visibility: hidden; }
.gemini-note { font-size: 12px; color: var(--ink-2); }
.stop-send { color: var(--warn-ink); background: var(--gold-wash); }
.session-chat { flex: 1; display: flex; flex-direction: column; min-height: 0; min-width: 0; }
.thread-wrap { position: relative; flex: 1; min-height: 0; display: flex; flex-direction: column; }
.thread-scroll { flex: 1; min-height: 0; overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain; padding: 18px 24px 20px; }
.jump { z-index: 1; position: absolute; left: 50%; transform: translateX(-50%); bottom: 12px; display: inline-flex; align-items: center; justify-content: center; gap: 6px; min-width: 44px; height: 44px; padding: 0; border: 0; border-radius: 999px; background: var(--surface-raised); color: var(--ink-2); box-shadow: var(--shadow-pop); }
.history-head { position: relative; display: flex; justify-content: center; align-items: center; min-height: 44px; margin-bottom: 12px; font-size: 12px; color: var(--ink-3); }
.earlier { border: 0; background: transparent; color: var(--teal-ink); min-height: 44px; padding: 0 10px; font: inherit; font-weight: 600; }
.history-error { position: absolute; top: 100%; left: 0; right: 0; z-index: 1; background: var(--surface-raised); color: var(--danger); box-shadow: var(--shadow-pop); padding: 8px; }
.chat-stop { background: var(--ink); color: var(--surface-raised); min-height: 44px; }
.jump svg { flex: none; }
.jump.labelled { padding: 0 12px 0 14px; color: var(--teal-ink); }
.jump-count { font-size: 12.5px; font-weight: 650; white-space: nowrap; }
@media (hover: hover) { .jump:hover { color: var(--ink); background: var(--btn-bg-hover); } }
.jump:focus-visible { outline: none; box-shadow: var(--shadow-pop), var(--focus-ring); }
.jump-enter-active, .jump-leave-active { transition: opacity .16s ease, transform .16s ease; }
.jump-enter-from, .jump-leave-to { opacity: 0; transform: translate(-50%, 6px); }
@media (prefers-reduced-motion: reduce) { .jump-enter-active, .jump-leave-active { transition: none; } }
.composer { flex-shrink: 0; padding: 10px 14px 12px; border-top: 1px solid var(--line); background: var(--surface-raised-2); border-radius: 0 0 var(--radius) var(--radius); }
.compose { position: relative; display: grid; grid-template-columns: minmax(0, 1fr); gap: 8px; min-width: 0; }
.compose textarea { width: 100%; min-width: 0; min-height: 56px; height: 64px; resize: none; padding: 9px 11px; font: inherit; font-size: 13.5px; line-height: 1.45; }
.compose-row { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; min-width: 0; }
.compose-hint { margin-left: auto; display: inline-flex; gap: 2px; }
.compose-block { display: flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--ink-2); padding: 6px 2px; overflow-wrap: anywhere; }
.compose-block svg { flex: none; }
.replying { position: absolute; bottom: 12px; left: 14px; right: 14px; display: flex; align-items: center; gap: 6px; min-width: 0; padding: 4px 4px 4px 10px; border-radius: 8px; background: var(--code-bg); font-size: 12px; color: var(--ink-2); }
.replying span { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.compose > .compose-block { pointer-events: none; position: absolute; bottom: calc(100% + 8px); left: 0; right: 0; margin: 0; padding: 8px 12px; background: var(--surface-raised); box-shadow: var(--shadow-pop); }
.send-error { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--danger); overflow-wrap: anywhere; position: absolute; bottom: calc(100% + 8px); left: 0; right: 0; background: var(--surface-raised); }
@media (max-width: 720px) {
  .thread-scroll { padding: 14px 16px 16px; }
  .composer { border-radius: 0; padding: 8px 12px calc(8px + env(safe-area-inset-bottom)); background: var(--surface-raised); }
  /* 16px keeps iOS Safari from zooming the page when the field takes focus. */
  .compose textarea { font-size: 16px; min-height: 48px; resize: none; }
  .compose-hint { display: none; }
  .compose-row .send { margin-left: auto; min-height: 44px; }
  .compose-row .steer-send, .stop-send { min-height: 44px; }
  .compose-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 6px; }
  .compose-row .send { margin-left: 0; }
  .compose-row .steer-send, .compose-row .stop-send { grid-column: 1 / -1; }
}
</style>
