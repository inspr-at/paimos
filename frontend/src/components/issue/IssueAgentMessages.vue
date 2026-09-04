<script setup lang="ts">
import { ref, watch } from 'vue'
import { fmtShortDateTime } from '@/utils/formatTime'
import {
  loadIssueAgentMessages,
  resolveHeldAgentMessage,
  type AgentMessage,
  type HumanResolutionOutcome,
} from '@/services/agentMessages'

const props = defineProps<{ issueId: number; projectId: number; canEdit: boolean }>()
const messages = ref<AgentMessage[]>([])
const pending = ref<Record<string, boolean>>({})
const resolutionErrors = ref<Record<string, string>>({})
const retryKeys = new Map<string, string>()
let loadSequence = 0
let fallbackKeySequence = 0

function newRetryKey(): string {
  if (typeof globalThis.crypto?.randomUUID === 'function') return globalThis.crypto.randomUUID()
  fallbackKeySequence += 1
  return `issue-resolution-${Date.now().toString(36)}-${fallbackKeySequence.toString(36)}`
}

function retryKey(messageId: string, outcome: HumanResolutionOutcome): string {
  const slot = `${messageId}:${outcome}`
  const existing = retryKeys.get(slot)
  if (existing) return existing
  const created = newRetryKey()
  retryKeys.set(slot, created)
  return created
}

async function loadMessages(issueId: number, sequence: number) {
  try {
    const loaded = await loadIssueAgentMessages(issueId)
    if (sequence === loadSequence && props.issueId === issueId) messages.value = loaded
  } catch {
    /* issue stays usable */
  }
}

watch(
  () => props.issueId,
  async (issueId) => {
    const sequence = ++loadSequence
    messages.value = []
    pending.value = {}
    resolutionErrors.value = {}
    retryKeys.clear()
    await loadMessages(issueId, sequence)
  },
  { immediate: true },
)

async function resolve(message: AgentMessage, outcome: HumanResolutionOutcome) {
  if (
    !props.canEdit ||
    props.projectId <= 0 ||
    pending.value[message.message_id] ||
    message.human_resolution_outcome
  )
    return
  const issueId = props.issueId
  const sequence = loadSequence
  pending.value = { ...pending.value, [message.message_id]: true }
  const nextErrors = { ...resolutionErrors.value }
  delete nextErrors[message.message_id]
  resolutionErrors.value = nextErrors
  try {
    const result = await resolveHeldAgentMessage(
      props.projectId,
      message.message_id,
      outcome,
      retryKey(message.message_id, outcome),
    )
    if (result.message_id !== message.message_id || result.outcome !== outcome) {
      throw new Error('resolution response did not match the requested decision')
    }
    if (sequence === loadSequence && props.issueId === issueId) {
      message.human_resolution_outcome = result.outcome
    }
  } catch {
    if (sequence === loadSequence && props.issueId === issueId) {
      resolutionErrors.value = {
        ...resolutionErrors.value,
        [message.message_id]: 'Decision was not recorded. Check your access and try again.',
      }
      // A competing browser tab may have committed the immutable decision.
      // Re-read authoritative state without exposing transport details.
      await loadMessages(issueId, sequence)
    }
  } finally {
    if (sequence === loadSequence && props.issueId === issueId) {
      const nextPending = { ...pending.value }
      delete nextPending[message.message_id]
      pending.value = nextPending
    }
  }
}
</script>

<template>
  <section v-if="messages.length" class="agent-messages" aria-label="Agent messages">
    <h3>
      Agent messages <span>{{ messages.length }}</span>
    </h3>
    <p class="agent-messages__notice">
      Agent-to-agent records are shown separately from human comments.
    </p>
    <article v-for="message in messages" :key="message.message_id" class="agent-message">
      <header>
        <code>{{ message.from }}</code
        ><span aria-hidden="true">→</span><code>{{ message.to }}</code>
        <time>{{ fmtShortDateTime(message.created_at) }}</time>
      </header>
      <p v-for="(part, index) in message.parts" :key="index">{{ part.text }}</p>
      <footer>
        thread {{ message.thread_id }} · hop {{ message.hop }}
        <strong v-if="message.human_resolution_outcome === 'resolved'"
          >Human decision recorded: resolved</strong
        >
        <strong v-else-if="message.human_resolution_outcome === 'dismissed'"
          >Human decision recorded: dismissed</strong
        >
        <strong v-else-if="message.is_action_request"
          >Action request held: human decision required</strong
        >
        <strong v-else-if="!message.delivered">Held: {{ message.held_reason }}</strong>
      </footer>
      <div
        v-if="message.is_action_request && !message.human_resolution_outcome"
        class="agent-message__decision"
      >
        <p>This records a decision only. It does not execute or deliver the request.</p>
        <div v-if="canEdit && projectId > 0" class="agent-message__decision-actions">
          <button
            type="button"
            :disabled="pending[message.message_id]"
            @click="resolve(message, 'resolved')"
          >
            Mark resolved
          </button>
          <button
            type="button"
            class="agent-message__dismiss"
            :disabled="pending[message.message_id]"
            @click="resolve(message, 'dismissed')"
          >
            Dismiss request
          </button>
        </div>
        <p v-else class="agent-message__read-only">
          Project edit access is required to record a decision.
        </p>
        <p v-if="resolutionErrors[message.message_id]" class="agent-message__error" role="alert">
          {{ resolutionErrors[message.message_id] }}
        </p>
      </div>
    </article>
  </section>
</template>

<style scoped>
.agent-messages {
  margin-top: 2rem;
}
.agent-messages h3 {
  display: flex;
  gap: 0.5rem;
  align-items: center;
  margin-bottom: 0.25rem;
}
.agent-messages h3 span {
  color: var(--text-muted);
  font-size: 0.8em;
}
.agent-messages__notice {
  color: var(--text-muted);
  font-size: 0.85rem;
  margin: 0.25rem 0 1rem;
}
.agent-message {
  border: 1px solid var(--border);
  border-left: 3px solid var(--brand-blue);
  border-radius: 6px;
  padding: 0.8rem 1rem;
  margin: 0.6rem 0;
  background: var(--bg-card);
}
.agent-message header {
  display: flex;
  align-items: center;
  gap: 0.45rem;
  font-size: 0.8rem;
  color: var(--text-muted);
}
.agent-message time {
  margin-left: auto;
}
.agent-message p {
  white-space: pre-wrap;
  margin: 0.65rem 0;
}
.agent-message footer {
  color: var(--text-muted);
  font-size: 0.75rem;
  overflow-wrap: anywhere;
}
.agent-message footer strong {
  color: #b45309;
  margin-left: 0.6rem;
}
.agent-message__decision {
  margin-top: 0.8rem;
  padding-top: 0.8rem;
  border-top: 1px solid var(--border);
}
.agent-message__decision p {
  color: var(--text-muted);
  font-size: 0.8rem;
  margin: 0 0 0.55rem;
}
.agent-message__decision-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
.agent-message__decision button {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 0.45rem 0.7rem;
  color: var(--text);
  background: var(--bg-card);
  cursor: pointer;
  font: inherit;
}
.agent-message__decision button:hover:not(:disabled) {
  border-color: var(--brand-blue);
}
.agent-message__decision button:disabled {
  cursor: wait;
  opacity: 0.55;
}
.agent-message__decision .agent-message__dismiss {
  color: #9f1239;
}
.agent-message__decision .agent-message__read-only {
  margin-bottom: 0;
}
.agent-message__decision .agent-message__error {
  color: #b91c1c;
  margin: 0.55rem 0 0;
}
</style>
