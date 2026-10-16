<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { displayLanguage } from '../../lib/displayLanguage'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { MessageStatus, ProjectMessage } from '../../lib/agents'
import type { Question } from '../../lib/decisionDeskApi'
import { useSession } from '../../stores/session'
import { absoluteTime } from '../../lib/work'
import { collapseMessages, historicalSender, type MessageGroup } from './sessionMessages'
import { chatWords, statusTip } from './sessionChat'
import { clockTime, messageChip, receiptTarget, threadRows, type ThreadRow } from './chatThread'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import ChatDeskCard from './ChatDeskCard.vue'
import { toast } from '../../lib/toast'
import MarkdownBody from '../MarkdownBody.vue'

// The thread (AEON-1438): day separators, one quiet meta line per message, Desk
// deliveries as cards, Copy and Reply on demand, and the receipt only on the
// viewer's latest message. newFrom places the "New" divider above that message;
// statuses carry the delivery progress of the viewer's own messages (AEON-280).
const props = defineProps<{ messages: ProjectMessage[]; principalId: string; sessionId?: string; now: number; canReply: boolean; newFrom?: string; newCount?: number; statuses?: Record<string, MessageStatus>; questions?: Question[] }>()
const emit = defineEmits<{ reply: [message: ProjectMessage]; retry: [message: ProjectMessage] }>()
const identity = useSession()
const language = computed(() => displayLanguage())
const german = computed(() => language.value === 'de')
const words = computed(() => chatWords[language.value])
const label = (status: MessageStatus) => ({ sent: words.value.sending, delivered: words.value.delivered, read: words.value.read, not_delivered: words.value.failed })[status.status]
const me = computed(() => identity.identity?.principal.id ?? '')
const items = computed(() => collapseMessages(props.messages))
// Day boundaries follow the clock tick, so "Today" turns into "Yesterday" overnight.
const today = computed(() => new Date(props.now).toDateString())
const rows = computed(() => { void today.value; return threadRows(items.value, props.now, language.value) })
const receiptId = computed(() => receiptTarget(rows.value, me.value))
const chip = (message: MessageGroup) => messageChip(message, words.value)
const mine = (message: MessageGroup) => message.sender_principal_id === me.value
const fromAgent = (message: MessageGroup) => message.sender_principal_id === props.principalId
// The divider sits above the row that holds the first unread post, a Desk card included.
const divider = (row: ThreadRow) => !!props.newFrom && row.type !== 'day' && (row.type === 'desk' ? row.members.includes(props.newFrom) : row.message.members.includes(props.newFrom))
const statusOf = (m: ProjectMessage): MessageStatus | undefined => {
  if (m.sender_principal_id !== me.value) return undefined
  if (m.send_failed) return { message_id: m.id, status: 'not_delivered', delivered_at: null, read_at: null, deliver_by: null }
  return props.statuses?.[m.id] ?? { message_id: m.id, status: 'sent', delivered_at: null, read_at: null, deliver_by: null }
}
const tip = (m: ProjectMessage) => { const status = statusOf(m)!; return german.value ? `${label(status)} · ${absoluteTime(status.read_at || status.delivered_at || m.created_at || '')}` : statusTip(status, absoluteTime) }
const failed = (m: MessageGroup) => !m.answered && statusOf(m)?.status === 'not_delivered'

async function copy(message: ProjectMessage) {
  const viewer = me.value, id = message.id, body = message.body
  try { await navigator.clipboard.writeText(body); if (viewer === me.value && props.messages.some(item => item.id === id && item.body === body)) toast(words.value.copied) }
  catch { if (viewer === me.value) toast(words.value.copyFailed, { tone: 'error' }) }
}

// ---------- Touch: a long press opens the message menu ----------
// Coarse pointers have no hover, so Copy and Reply move into a menu that a long
// press, the context-menu key or Enter on the focused message opens. The pressed
// message lifts in place; nothing around it moves.
const coarse = ref(false), narrow = ref(false)
let coarseQuery: MediaQueryList | undefined, narrowQuery: MediaQueryList | undefined
const syncMedia = () => { coarse.value = !!coarseQuery?.matches; narrow.value = !!narrowQuery?.matches }
onMounted(() => {
  if (typeof matchMedia !== 'function') return
  coarseQuery = matchMedia('(hover: none), (pointer: coarse)'); narrowQuery = matchMedia('(max-width: 359px)')
  syncMedia()
  coarseQuery.addEventListener('change', syncMedia); narrowQuery.addEventListener('change', syncMedia)
})
const avatarSize = computed(() => narrow.value ? 18 : 20)
const menuable = (m: MessageGroup) => fromAgent(m) || mine(m)
const menu = ref<{ id: string; anchor: HTMLElement; row: HTMLElement; message: MessageGroup } | null>(null)
let pressTimer: ReturnType<typeof setTimeout> | undefined
let pressOrigin: { x: number; y: number } | null = null
let pressFired = false
function cancelPress() { if (pressTimer !== undefined) clearTimeout(pressTimer); pressTimer = undefined; pressOrigin = null }
function openMenu(row: HTMLElement, message: MessageGroup) {
  cancelPress()
  // A message taller than the view anchors the menu at its first line.
  const scroller = row.closest<HTMLElement>('.thread-scroll')
  const bottom = scroller ? scroller.getBoundingClientRect().bottom : innerHeight
  const anchor = row.getBoundingClientRect().bottom > bottom ? (row.firstElementChild as HTMLElement | null) ?? row : row
  menu.value = { id: message.id, anchor, row, message }
}
function closeMenu(restore: boolean) {
  const row = menu.value?.row
  menu.value = null
  if (restore && row?.isConnected && row.tabIndex >= 0) row.focus({ preventScroll: true })
}
function press(event: PointerEvent, message: MessageGroup) {
  if (event.pointerType === 'mouse' || !menuable(message)) return
  if ((event.target as HTMLElement).closest('a, button, summary, .code-fold, input, textarea')) return
  const row = event.currentTarget as HTMLElement
  cancelPress()
  pressFired = false
  pressOrigin = { x: event.clientX, y: event.clientY }
  pressTimer = setTimeout(() => { pressTimer = undefined; pressFired = true; openMenu(row, message) }, 450)
}
function pressMove(event: PointerEvent) {
  if (pressOrigin && Math.hypot(event.clientX - pressOrigin.x, event.clientY - pressOrigin.y) > 8) cancelPress()
}
// The click that ends a long press must not reach links or the thread.
function swallow(event: MouseEvent) { if (pressFired) { pressFired = false; event.preventDefault(); event.stopPropagation() } }
function contextMenu(event: MouseEvent, message: MessageGroup) {
  if (!coarse.value || !menuable(message)) return
  event.preventDefault()
  if (menu.value?.id !== message.id) openMenu(event.currentTarget as HTMLElement, message)
}
function menuKey(event: KeyboardEvent, message: MessageGroup) {
  if (!coarse.value || !menuable(message) || event.target !== event.currentTarget) return
  if (event.key === 'Enter' || event.key === ' ' || event.key === 'ContextMenu' || (event.key === 'F10' && event.shiftKey)) {
    event.preventDefault()
    openMenu(event.currentTarget as HTMLElement, message)
  }
}
function menuReply() { const message = menu.value?.message; closeMenu(false); if (message && props.canReply) emit('reply', message) }
function menuCopy() { const message = menu.value?.message; closeMenu(true); if (message) void copy(message) }
// A message that leaves the thread, or a person change, closes its menu.
watch(me, () => { if (menu.value) closeMenu(false) })
watch(items, list => { if (menu.value && !list.some(item => item.id === menu.value!.id)) closeMenu(false) })
onBeforeUnmount(() => {
  cancelPress()
  coarseQuery?.removeEventListener('change', syncMedia); narrowQuery?.removeEventListener('change', syncMedia)
})
</script>

<template>
  <section class="session-messages" aria-label="Conversation">
    <ol v-if="rows.length" class="thread" aria-label="Messages">
      <template v-for="row in rows" :key="row.key">
        <li v-if="row.type === 'day'" class="day" role="separator">{{ row.label }}</li>
        <template v-else>
          <li v-if="divider(row)" class="new-divider" role="separator" :aria-label="`${newCount ?? 1} new`"><span>{{ words.newDivider }}</span></li>
          <li v-if="row.type === 'desk'" class="msg desk" :data-event="row.message.optimistic ? undefined : row.lastEvent" :data-id="row.message.id">
            <ChatDeskCard :question-id="row.questionId" :answer="row.answer" :corrected="row.corrected" :at="row.message.created_at" :questions="questions" :session-id="sessionId" />
          </li>
          <li v-else-if="mine(row.message)" class="msg mine" :class="{ pressed: menu?.id === row.message.id }"
            :tabindex="coarse ? 0 : undefined" :aria-haspopup="coarse ? 'menu' : undefined"
            :data-event="row.message.optimistic ? undefined : row.message.last_event" :data-id="row.message.id" :data-receipt="row.message.answered ? 'answered' : statusOf(row.message)?.status"
            @pointerdown="press($event, row.message)" @pointermove="pressMove" @pointerup="cancelPress" @pointercancel="cancelPress"
            @click.capture="swallow" @contextmenu="contextMenu($event, row.message)" @keydown="menuKey($event, row.message)">
            <p class="msg-body">{{ row.message.body }}</p>
            <p class="msg-meta">
              <span v-if="row.message.count > 1" class="duplicate" :aria-label="`${row.message.count} identical posts`">×{{ row.message.count }}</span>
              <span v-if="chip(row.message)" class="msg-chip">{{ chip(row.message) }}</span>
              <time v-if="row.message.created_at" class="msg-time" :datetime="row.message.created_at" :data-tip="absoluteTime(row.message.created_at)">{{ clockTime(row.message.created_at) }}</time>
              <template v-if="row.message.id === receiptId && !failed(row.message)">
                <span class="sep" aria-hidden="true">·</span>
                <span v-if="row.message.answered" class="delivery answered" data-status="answered" data-tip="A reply to this message is in the conversation">{{ words.read }}</span>
                <span v-else class="delivery" :class="statusOf(row.message)!.status" :data-status="statusOf(row.message)!.status" :data-tip="tip(row.message)">
                  <svg width="15" height="12" viewBox="0 0 20 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
                    <path d="m1.8 8.6 3 3 6.2-6.8" />
                    <path v-if="statusOf(row.message)!.status !== 'sent'" d="m9.6 11.4.2.2 6.2-6.8" />
                  </svg>
                  <span class="delivery-word">{{ label(statusOf(row.message)!) }}</span>
                </span>
              </template>
            </p>
            <p v-if="failed(row.message)" class="undelivered" data-status="not_delivered" :data-tip="tip(row.message)"><AppIcon name="alert" :size="12" /><span>{{ label(statusOf(row.message)!) }}</span><button v-if="canReply" type="button" class="retry" @click="emit('retry', row.message)">{{ words.retry }}</button></p>
          </li>
          <li v-else class="msg" :class="{ agent: fromAgent(row.message), pressed: menu?.id === row.message.id }"
            :tabindex="coarse && fromAgent(row.message) ? 0 : undefined" :aria-haspopup="coarse && fromAgent(row.message) ? 'menu' : undefined"
            :data-event="row.message.optimistic ? undefined : row.message.last_event" :data-id="row.message.id"
            @pointerdown="press($event, row.message)" @pointermove="pressMove" @pointerup="cancelPress" @pointercancel="cancelPress"
            @click.capture="swallow" @contextmenu="contextMenu($event, row.message)" @keydown="menuKey($event, row.message)">
            <p class="msg-meta">
              <Avatar :id="fromAgent(row.message) ? null : row.message.sender_principal_id" :name="historicalSender(row.message, me)" :kind="fromAgent(row.message) ? 'agent' : 'person'" :size="avatarSize" />
              <span class="msg-author" :title="historicalSender(row.message, me)">{{ historicalSender(row.message, me) }}</span>
              <template v-if="row.message.created_at"><span class="sep" aria-hidden="true">·</span><time class="msg-time" :datetime="row.message.created_at" :data-tip="absoluteTime(row.message.created_at)">{{ clockTime(row.message.created_at) }}</time></template>
              <span v-if="row.message.count > 1" class="duplicate" :aria-label="`${row.message.count} identical posts`">×{{ row.message.count }}</span>
              <span v-if="chip(row.message)" class="msg-chip">{{ chip(row.message) }}</span>
            </p>
            <MarkdownBody :body="row.message.body" chat />
            <div v-if="fromAgent(row.message)" class="message-actions" role="group" :aria-label="words.actions">
              <button type="button" class="icon-btn sm flat" :aria-label="words.copy" :data-tip="words.copy" @click="copy(row.message)"><AppIcon name="copy" :size="15" /></button>
              <button v-if="canReply" type="button" class="icon-btn sm flat" :aria-label="words.reply" :data-tip="words.reply" @click="emit('reply', row.message)"><AppIcon name="reply" :size="15" /></button>
            </div>
          </li>
        </template>
      </template>
    </ol>
    <div v-if="menu" class="msg-scrim" aria-hidden="true" />
    <FloatingPanel v-if="menu" :anchor="menu.anchor" :align="mine(menu.message) ? 'end' : 'start'" :width="216" :label="words.actions" @close="closeMenu">
      <div class="msg-menu" role="menu" :aria-label="words.actions">
        <button v-if="fromAgent(menu.message) && canReply" type="button" role="menuitem" class="msg-menu-item" @click="menuReply"><AppIcon name="reply" :size="18" /><span>{{ words.reply }}</span></button>
        <button type="button" role="menuitem" class="msg-menu-item" @click="menuCopy"><AppIcon name="copy" :size="18" /><span>{{ words.copy }}</span></button>
      </div>
    </FloatingPanel>
  </section>
</template>

<style scoped>
.session-messages { min-width: 0; }
.thread { display: grid; grid-template-columns: minmax(0, 1fr); gap: 12px; margin: 0; padding: 0; list-style: none; }
.day { justify-self: center; padding: 4px 0 2px; font-size: 12px; font-weight: 600; color: var(--ink-3); }
.msg { position: relative; min-width: 0; width: 100%; padding: 0; }
.msg :deep(.chat-markdown > :last-child) { margin-bottom: 0; }
/* One quiet meta line: name · time, then at most one chip. */
.msg-meta { display: flex; align-items: center; gap: 6px; min-width: 0; min-height: 20px; margin: 0 0 5px; font-size: 12.5px; color: var(--ink-3); white-space: nowrap; }
.msg-author { min-width: 0; overflow: hidden; text-overflow: ellipsis; font-weight: 650; color: var(--ink); }
.msg-time { flex: none; font-size: 12px; font-variant-numeric: tabular-nums; }
.msg-chip { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; height: 20px; margin-left: 4px; padding: 0 7px; border-radius: 6px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font-size: 11.5px; font-weight: 500; line-height: 20px; }
.duplicate { flex: none; font: 500 11.5px var(--mono); color: var(--ink-2); }
/* Copy and Reply: a reserved row at the message's end, shown on hover or focus. */
.message-actions { display: flex; gap: 2px; height: 28px; margin: 4px 0 0 -6px; opacity: 0; }
.message-actions .icon-btn { border-radius: 8px; color: var(--ink-3); }
@media (hover: hover) { .message-actions .icon-btn:hover { color: var(--teal-ink); background: var(--row-hover); } }
.msg:hover .message-actions, .msg:focus-within .message-actions { opacity: 1; }
@media (prefers-reduced-motion: no-preference) { .message-actions { transition: opacity .15s ease; } }
/* Your own messages: the bubble on the column's right edge, the time under it. */
.msg.mine { justify-self: end; width: auto; max-width: min(82%, 640px); display: flex; flex-direction: column; align-items: flex-end; }
.mine .msg-body { margin: 0; padding: 9px 14px; border-radius: 18px; background: color-mix(in srgb, var(--teal) 10%, var(--surface-raised)); color: var(--ink); font-size: 14.5px; line-height: 1.5; white-space: pre-wrap; overflow-wrap: anywhere; word-break: break-word; }
.mine .msg-meta { justify-content: flex-end; min-height: 18px; margin: 4px 2px 0; font-size: 12px; }
.mine .msg-chip { margin-left: 0; }
.delivery { display: inline-flex; align-items: center; gap: 3px; color: var(--ink-3); }
.delivery svg { flex: none; }
.delivery.read, .delivery.answered { color: var(--teal-ink); }
.undelivered { display: flex; align-items: center; justify-content: flex-end; gap: 5px; margin: 6px 0 0; font-size: 12px; font-weight: 600; color: var(--danger); overflow-wrap: anywhere; }
.undelivered svg { flex: none; }
.retry { border: 0; background: transparent; color: inherit; font: inherit; font-weight: 650; text-decoration: underline; min-height: 44px; }
/* A hairline with a quiet label; no coloured edge (AGENTS.md rule 11). */
.new-divider { display: flex; align-items: center; gap: 10px; margin: 4px 0; color: var(--teal-ink); font-size: 11px; font-weight: 650; letter-spacing: .04em; }
.new-divider::before, .new-divider::after { content: ''; flex: 1; height: 1px; background: var(--chip-teal-line); }
/* Touch: no reserved action row; a long press lifts the message in place under a scrim. */
.msg-scrim { position: fixed; inset: 0; z-index: 30; background: var(--scrim); }
.msg.pressed { z-index: 31; }
.msg.pressed::before { content: ''; position: absolute; inset: -8px -10px; z-index: -1; border-radius: 14px; background: var(--surface-raised); box-shadow: var(--shadow-pop); }
.msg.mine.pressed::before { inset: -6px -8px; }
.msg-menu { display: grid; }
.msg-menu-item { display: flex; align-items: center; gap: 12px; width: 100%; min-height: 44px; padding: 6px 12px; border: 0; border-radius: 10px; background: transparent; color: var(--ink); font: inherit; font-size: 15px; line-height: 1.3; text-align: left; }
.msg-menu-item > svg { flex: none; color: var(--ink-2); }
.msg-menu-item + .msg-menu-item { box-shadow: 0 -1px 0 var(--line); }
@media (hover: hover) { .msg-menu-item:hover { background: var(--row-hover); } }
.msg-menu-item:focus-visible { outline: 2px solid var(--primary-line); outline-offset: -2px; }
@media (hover: none), (pointer: coarse) {
  .thread { gap: 20px; }
  .message-actions { display: none; }
  .msg.agent, .msg.mine { -webkit-user-select: none; user-select: none; -webkit-touch-callout: none; }
  .msg[tabindex]:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 10px; }
}
@media (max-width: 720px) {
  .msg-meta { gap: 8px; margin-bottom: 4px; font-size: 12px; line-height: 16px; }
  .msg-author { flex: none; max-width: 60%; font-size: 13px; }
  .msg-meta .sep { display: none; }
  .msg-chip { margin-left: 0; height: auto; padding: 1px 7px; background: var(--surface-sunken); box-shadow: none; line-height: 16px; }
  .msg :deep(.chat-markdown) { font-size: 15px; line-height: 1.5; }
  .msg.mine { max-width: 85%; }
  .mine .msg-body { border-radius: 18px 18px 6px 18px; background: var(--chip-teal-bg); font-size: 15px; line-height: 1.45; }
  .mine .msg-meta { gap: 6px; }
}
/* The smallest phones: a tighter meta line, so name and chip still fit on one line. */
@media (max-width: 359px) {
  .msg-meta { gap: 5px; }
  .msg-chip { padding: 1px 5px; font-size: 11px; }
}
</style>
