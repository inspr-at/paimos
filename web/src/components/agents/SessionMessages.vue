<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { ProjectMessage } from '../../lib/agents'
import { useSession } from '../../stores/session'
import { useProfile } from '../../stores/profile'
import { absoluteTime, relativeTime } from '../../lib/work'
import { collapseMessages, historicalSender } from './sessionMessages'
import type { MessageStatus } from '../../lib/agents'
import { chatWords, statusLabel, statusTip } from './sessionChat'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'
import MarkdownBody from '../MarkdownBody.vue'

// newFrom places the "New" divider above that message; statuses carry the delivery
// progress of the viewer's own messages (AEON-280) when the server has one.
const props = defineProps<{ messages: ProjectMessage[]; principalId: string; now: number; canReply: boolean; newFrom?: string; newCount?: number; statuses?: Record<string, MessageStatus> }>()
const emit = defineEmits<{ reply: [message: ProjectMessage]; retry: [message: ProjectMessage] }>()
const identity = useSession()
const profile = useProfile()
const german = computed(() => profile.profile?.locale.startsWith('de'))
const words = computed(() => chatWords[german.value ? 'de' : 'en'])
const label = (status: MessageStatus) => german.value ? ({ sent: words.value.sending, delivered: words.value.delivered, read: words.value.read, not_delivered: words.value.failed })[status.status] : statusLabel(status)
const me = computed(() => identity.identity?.principal.id ?? '')
const items = computed(() => collapseMessages(props.messages))
const statusOf = (m: ProjectMessage): MessageStatus | undefined => {
  if (m.sender_principal_id !== me.value) return undefined
  if (m.send_failed) return { message_id: m.id, status: 'not_delivered', delivered_at: null, read_at: null, deliver_by: null }
  return props.statuses?.[m.id] ?? { message_id: m.id, status: 'sent', delivered_at: null, read_at: null, deliver_by: null }
}
</script>

<template>
  <section class="session-messages" aria-label="Conversation">
    <p v-if="!items.length" class="empty-line">No messages in this session yet.</p>
    <ol v-else class="thread" aria-label="Messages">
      <template v-for="m in items" :key="m.id">
        <li v-if="m.id === newFrom" class="new-divider" role="separator" :aria-label="`${newCount ?? 1} new`"><span>New</span></li>
        <li class="msg" :class="{ mine: m.sender_principal_id === me }" :data-event="m.optimistic ? undefined : m.last_event" :data-id="m.id">
          <p class="msg-meta">
            <Avatar :name="historicalSender(m, me)" :kind="m.sender_principal_id === me ? 'person' : 'agent'" :size="18" />
            <span class="msg-author" :title="historicalSender(m, me)">{{ historicalSender(m, me) }}</span>
            <span v-if="m.count > 1" class="duplicate" :aria-label="`${m.count} identical posts`">×{{ m.count }}</span>
            <span v-if="m.delivery_level === 'steer'" class="msg-chip"><AppIcon name="bolt" :size="10" />Steer</span>
            <span v-if="m.is_action_request" class="msg-chip">Action request</span>
            <span v-if="m.reply_obligation === 'open' && !m.answered" class="msg-chip">Awaiting reply</span>
            <span v-if="m.human_resolution_outcome" class="msg-chip">{{ m.human_resolution_outcome === 'resolved' ? 'Resolved' : 'Dismissed' }}</span>
            <button v-if="canReply && m.sender_principal_id === principalId" type="button" class="reply" @click="emit('reply', m)">{{ words.reply }}</button>
            <time v-if="m.created_at" class="msg-time" :datetime="m.created_at" :data-tip="absoluteTime(m.created_at)">{{ relativeTime(m.created_at, { now }) }}</time>
            <span v-if="m.answered" class="delivery answered" data-status="answered" data-tip="A reply to this message is in the conversation">{{ words.read }}</span>
            <span v-else-if="statusOf(m) && statusOf(m)!.status !== 'not_delivered'" class="delivery" :class="statusOf(m)!.status" :data-status="statusOf(m)!.status" :data-tip="statusTip(statusOf(m)!, absoluteTime)">
              <svg width="15" height="12" viewBox="0 0 20 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
                <path d="m1.8 8.6 3 3 6.2-6.8" />
                <path v-if="statusOf(m)!.status !== 'sent'" d="m9.6 11.4.2.2 6.2-6.8" />
              </svg>
              <span class="delivery-word">{{ label(statusOf(m)!) }}</span>
            </span>
          </p>
          <p v-if="m.sender_principal_id === me" class="msg-body">{{ m.body }}</p>
          <MarkdownBody v-else :body="m.body" chat />
          <p v-if="!m.answered && statusOf(m)?.status === 'not_delivered'" class="undelivered" data-status="not_delivered" :data-tip="statusTip(statusOf(m)!, absoluteTime)"><AppIcon name="alert" :size="12" /><span>{{ label(statusOf(m)!) }}</span><button v-if="canReply" type="button" class="retry" @click="emit('retry', m)">{{ words.retry }}</button></p>
        </li>
      </template>
    </ol>
  </section>
</template>

<style scoped>
.session-messages { min-width: 0; }
.empty-line { font-size: 13px; color: var(--ink-2); }
.thread { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; margin: 0; padding: 0; list-style: none; }
.msg { min-width: 0; max-width: min(92%, 640px); padding: 10px 12px; border-radius: 12px; background: var(--comment-bg, var(--code-bg)); box-shadow: inset 0 0 0 1px var(--line); }
.msg.mine { justify-self: end; background: var(--chip-teal-bg); }
.msg-meta { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; min-width: 0; margin-bottom: 4px; font-size: 11.5px; }
.msg-author { min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 650; color: var(--ink); }
.msg-time { margin-left: auto; font-size: 11px; color: var(--ink-3); white-space: nowrap; }
.msg-chip { display: inline-flex; align-items: center; gap: 3px; padding: 2px 6px; border-radius: 999px; font-size: 10px; background: var(--chip-bg); color: var(--ink-2); white-space: nowrap; }
.duplicate { font: 500 11px var(--mono); color: var(--ink-2); }
/* Sent → Delivered → Read: one quiet check becomes two, Read turns teal. Words, not colour alone. */
.delivery { display: inline-flex; align-items: center; gap: 3px; font-size: 11px; color: var(--ink-3); white-space: nowrap; }
.retry { border: 0; background: transparent; color: inherit; font-weight: 650; text-decoration: underline; min-height: 44px; }
.delivery svg { flex: none; }
.delivery.read, .delivery.answered { color: var(--teal-ink); }
.undelivered { display: flex; align-items: center; gap: 5px; margin-top: 6px; font-size: 12px; font-weight: 600; color: var(--danger); overflow-wrap: anywhere; }
.undelivered svg { flex: none; }
.msg-body { font-size: 13.5px; line-height: 1.5; color: var(--ink); white-space: pre-wrap; overflow-wrap: anywhere; word-break: break-word; }
.reply { margin-left: auto; padding: 0 4px 0 0; border: 0; background: transparent; color: var(--teal-ink); font-size: 11.5px; font-weight: 650; }
.reply:hover { text-decoration: underline; }
.reply ~ .msg-time { margin-left: 0; }
/* Reply sits in the meta line: always on touch, on hover or focus with a pointer. */
@media (hover: hover) { .msg:not(:hover, :focus-within) .reply { opacity: 0; } }
.reply:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
/* A hairline with a quiet label; no coloured edge (AGENTS.md rule 11). */
.new-divider { display: flex; align-items: center; gap: 10px; margin: 4px 0; color: var(--teal-ink); font-size: 11px; font-weight: 650; letter-spacing: .04em; }
.new-divider::before, .new-divider::after { content: ''; flex: 1; height: 1px; background: var(--chip-teal-line); }
</style>
