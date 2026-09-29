<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { ProjectMessage } from '../../lib/agents'
import { useSession } from '../../stores/session'
import { absoluteTime, relativeTime } from '../../lib/work'
import { belongsToSession, collapseMessages, historicalSender } from './sessionMessages'
import { receiptTip, type InboxReceipt } from './sessionChat'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'

// newFrom places the "New" divider above that message; receipts carry the hand-off
// state of the viewer's own messages (AEON-165/267) when the server has one.
const props = defineProps<{ messages: ProjectMessage[]; sessionId: string; principalId: string; address: string; now: number; canReply: boolean; newFrom?: string; newCount?: number; receipts?: Record<string, InboxReceipt> }>()
const emit = defineEmits<{ reply: [message: ProjectMessage] }>()
const identity = useSession()
const me = computed(() => identity.identity?.principal.id ?? '')
const agent = computed(() => props.address.replace(/^[^:]+:/, '') || 'this agent')
const groups = computed(() => [
  { history: false, items: collapseMessages(props.messages.filter(m => belongsToSession(m, props.sessionId))) },
  { history: true, items: collapseMessages(props.messages.filter(m => !belongsToSession(m, props.sessionId))) },
])
const receiptOf = (m: ProjectMessage) => m.sender_principal_id === me.value ? props.receipts?.[m.id] : undefined
</script>

<template>
  <section class="session-messages" aria-label="Conversation">
    <template v-for="group in groups" :key="String(group.history)">
      <component :is="group.history ? 'details' : 'div'" v-if="!group.history || group.items.length" :class="{ history: group.history }">
        <summary v-if="group.history"><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Other sessions of {{ agent }}</summary>
        <p v-if="!group.items.length" class="empty-line">No messages in this session yet.</p>
        <ol v-else class="thread" :aria-label="group.history ? 'Other session messages' : 'Messages'">
          <template v-for="m in group.items" :key="m.id">
            <li v-if="!group.history && m.id === newFrom" class="new-divider" role="separator" :aria-label="`${newCount ?? 1} new`"><span>New</span></li>
            <li class="msg" :class="{ mine: m.sender_principal_id === me }" :data-event="group.history ? undefined : m.last_event" :data-id="m.id">
              <p class="msg-meta">
                <Avatar :name="historicalSender(m, me)" :kind="m.sender_principal_id === me ? 'person' : 'agent'" :size="18" />
                <span class="msg-author" :title="historicalSender(m, me)">{{ historicalSender(m, me) }}</span>
                <span v-if="m.count > 1" class="duplicate" :aria-label="`${m.count} identical posts`">×{{ m.count }}</span>
                <span v-if="m.delivery_level === 'steer'" class="msg-chip"><AppIcon name="bolt" :size="10" />Steer</span>
                <span v-if="m.is_action_request" class="msg-chip">Action request</span>
                <span v-if="m.reply_obligation === 'open'" class="msg-chip">Awaiting reply</span>
                <span v-if="m.human_resolution_outcome" class="msg-chip">{{ m.human_resolution_outcome === 'resolved' ? 'Resolved' : 'Dismissed' }}</span>
                <button v-if="!group.history && canReply && m.sender_principal_id === principalId" type="button" class="reply" @click="emit('reply', m)">Reply</button>
                <time v-if="m.created_at" class="msg-time" :datetime="m.created_at" :data-tip="absoluteTime(m.created_at)">{{ relativeTime(m.created_at, { now }) }}</time>
                <span v-if="receiptOf(m)" class="tick" :class="receiptOf(m)!.state" role="img" :aria-label="receiptTip(receiptOf(m)!, absoluteTime)" :data-tip="receiptTip(receiptOf(m)!, absoluteTime)">
                  <AppIcon v-if="receiptOf(m)!.state === 'failed'" name="alert" :size="12" />
                  <svg v-else width="15" height="12" viewBox="0 0 20 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
                    <path d="m1.8 8.6 3 3 6.2-6.8" />
                    <path v-if="receiptOf(m)!.state === 'handed_off'" d="m9.6 11.4.2.2 6.2-6.8" />
                  </svg>
                </span>
              </p>
              <p class="msg-body">{{ m.body }}</p>
            </li>
          </template>
        </ol>
      </component>
    </template>
  </section>
</template>

<style scoped>
.session-messages { min-width: 0; }
.empty-line, summary { font-size: 13px; color: var(--ink-2); }
.history { margin-top: 16px; }
summary { display: flex; align-items: center; gap: 6px; cursor: pointer; padding: 8px 0; overflow-wrap: anywhere; }
.history[open] .disclosure-chev { transform: rotate(90deg); }
summary:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 6px; }
.thread { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; margin: 0; padding: 0; list-style: none; }
.msg { min-width: 0; max-width: min(92%, 640px); padding: 10px 12px; border-radius: 12px; background: var(--comment-bg, var(--code-bg)); box-shadow: inset 0 0 0 1px var(--line); }
.msg.mine { justify-self: end; background: var(--chip-teal-bg); }
.msg-meta { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; min-width: 0; margin-bottom: 4px; font-size: 11.5px; }
.msg-author { min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 650; color: var(--ink); }
.msg-time { margin-left: auto; font-size: 11px; color: var(--ink-3); white-space: nowrap; }
.msg-chip { display: inline-flex; align-items: center; gap: 3px; padding: 2px 6px; border-radius: 999px; font-size: 10px; background: var(--chip-bg); color: var(--ink-2); white-space: nowrap; }
.duplicate { font: 500 11px var(--mono); color: var(--ink-2); }
.tick { display: inline-grid; place-items: center; width: 16px; height: 16px; margin-left: -2px; color: var(--ink-3); }
.tick.handed_off { color: var(--teal-ink); }
.tick.failed { color: var(--danger); }
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
