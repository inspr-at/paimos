<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { ProjectMessage } from '../../lib/agents'
import { useSession } from '../../stores/session'
import { absoluteTime, relativeTime } from '../../lib/work'
import { belongsToSession, collapseMessages, historicalSender } from './sessionMessages'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'

const props = defineProps<{ messages: ProjectMessage[]; sessionId: string; principalId: string; address: string; now: number; canReply: boolean }>()
const emit = defineEmits<{ reply: [message: ProjectMessage] }>()
const identity = useSession()
const me = computed(() => identity.identity?.principal.id ?? '')
const agent = computed(() => props.address.replace(/^[^:]+:/, '') || 'this agent')
const groups = computed(() => [
  { history: false, items: collapseMessages(props.messages.filter(m => belongsToSession(m, props.sessionId))) },
  { history: true, items: collapseMessages(props.messages.filter(m => !belongsToSession(m, props.sessionId))) },
])
</script>

<template>
  <section class="session-messages" aria-labelledby="messages-title">
    <h3 id="messages-title" class="eyebrow">Messages</h3>
    <template v-for="group in groups" :key="String(group.history)">
      <component :is="group.history ? 'details' : 'div'" v-if="!group.history || group.items.length" :class="{ history: group.history }">
        <summary v-if="group.history"><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Other sessions of {{ agent }}</summary>
        <p v-if="!group.items.length" class="empty-line">No messages in this session.</p>
        <ol v-else class="thread" :aria-label="group.history ? 'Other session messages' : 'Messages'">
          <li v-for="m in group.items" :key="m.id" class="msg" :class="{ mine: m.sender_principal_id === me }">
            <p class="msg-meta">
              <Avatar :name="historicalSender(m, me)" :kind="m.sender_principal_id === me ? 'person' : 'agent'" :size="18" />
              <span class="msg-author" :title="historicalSender(m, me)">{{ historicalSender(m, me) }}</span>
              <span v-if="m.count > 1" class="duplicate" :aria-label="`${m.count} identical posts`">×{{ m.count }}</span>
              <span v-if="m.delivery_level === 'steer'" class="msg-chip"><AppIcon name="bolt" :size="10" />Steer</span>
              <span v-if="m.is_action_request" class="msg-chip">Action request</span>
              <span v-if="m.reply_obligation === 'open'" class="msg-chip">Awaiting reply</span>
              <span v-if="m.human_resolution_outcome" class="msg-chip">{{ m.human_resolution_outcome === 'resolved' ? 'Resolved' : 'Dismissed' }}</span>
              <time v-if="m.created_at" class="msg-time" :datetime="m.created_at" :data-tip="absoluteTime(m.created_at)">{{ relativeTime(m.created_at, { now }) }}</time>
            </p>
            <p class="msg-body">{{ m.body }}</p>
            <button v-if="!group.history && canReply && m.sender_principal_id === principalId" type="button" class="reply" @click="emit('reply', m)">Reply</button>
          </li>
        </ol>
      </component>
    </template>
  </section>
</template>

<style scoped>
.session-messages { margin-top: 26px; min-width: 0; }
h3 { margin-bottom: 10px; }
.empty-line, summary { font-size: 13px; color: var(--ink-2); }
.history { margin-top: 16px; }
summary { display: flex; align-items: center; gap: 6px; cursor: pointer; padding: 8px 0; overflow-wrap: anywhere; }
.history[open] .disclosure-chev { transform: rotate(90deg); }
summary:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 6px; }
.thread { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; margin: 0; padding: 0; list-style: none; }
.msg { min-width: 0; max-width: 92%; padding: 10px 12px; border-radius: 12px; background: var(--comment-bg, var(--code-bg)); box-shadow: inset 0 0 0 1px var(--line); }
.msg.mine { justify-self: end; background: var(--chip-teal-bg); }
.msg-meta { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-bottom: 4px; font-size: 11.5px; }
.msg-author { min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 650; color: var(--ink); }
.msg-time { margin-left: auto; font-size: 11px; color: var(--ink-3); white-space: nowrap; }
.msg-chip { display: inline-flex; align-items: center; gap: 3px; padding: 2px 6px; border-radius: 999px; font-size: 10px; background: var(--chip-bg); color: var(--ink-2); }
.duplicate { font: 500 11px var(--mono); color: var(--ink-2); }
.msg-body { font-size: 13.5px; line-height: 1.5; color: var(--ink); white-space: pre-wrap; overflow-wrap: anywhere; }
.reply { margin-top: 6px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font-size: 12px; font-weight: 600; }
.reply:hover { text-decoration: underline; }
.reply:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
</style>
