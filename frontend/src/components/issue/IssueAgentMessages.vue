<script setup lang="ts">
import { ref, watch } from 'vue'
import { fmtShortDateTime } from '@/utils/formatTime'
import { loadIssueAgentMessages, type AgentMessage } from '@/services/agentMessages'

const props = defineProps<{ issueId: number }>()
const messages = ref<AgentMessage[]>([])
let loadSequence = 0

watch(() => props.issueId, async (issueId) => {
  const sequence = ++loadSequence
  messages.value = []
  try {
    const loaded = await loadIssueAgentMessages(issueId)
    if (sequence === loadSequence && props.issueId === issueId) messages.value = loaded
  } catch { /* issue stays usable */ }
}, { immediate: true })
</script>

<template>
  <section v-if="messages.length" class="agent-messages" aria-label="Agent messages">
    <h3>Agent messages <span>{{ messages.length }}</span></h3>
    <p class="agent-messages__notice">Agent-to-agent records are shown separately from human comments.</p>
    <article v-for="message in messages" :key="message.message_id" class="agent-message">
      <header>
        <code>{{ message.from }}</code><span aria-hidden="true">→</span><code>{{ message.to }}</code>
        <time>{{ fmtShortDateTime(message.created_at) }}</time>
      </header>
      <p v-for="(part, index) in message.parts" :key="index">{{ part.text }}</p>
      <footer>
        thread {{ message.thread_id }} · hop {{ message.hop }}
        <strong v-if="!message.delivered">Held: {{ message.held_reason }}</strong>
      </footer>
    </article>
  </section>
</template>

<style scoped>
.agent-messages { margin-top: 2rem; }
.agent-messages h3 { display:flex; gap:.5rem; align-items:center; margin-bottom:.25rem; }
.agent-messages h3 span { color:var(--text-muted); font-size:.8em; }
.agent-messages__notice { color:var(--text-muted); font-size:.85rem; margin:.25rem 0 1rem; }
.agent-message { border:1px solid var(--border); border-left:3px solid var(--brand-blue); border-radius:6px; padding:.8rem 1rem; margin:.6rem 0; background:var(--bg-card); }
.agent-message header { display:flex; align-items:center; gap:.45rem; font-size:.8rem; color:var(--text-muted); }
.agent-message time { margin-left:auto; }
.agent-message p { white-space:pre-wrap; margin:.65rem 0; }
.agent-message footer { color:var(--text-muted); font-size:.75rem; overflow-wrap:anywhere; }
.agent-message footer strong { color:#b45309; margin-left:.6rem; }
</style>
