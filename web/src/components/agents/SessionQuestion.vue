<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { APIError } from '../../lib/api'
import { can, onAccessRevalidate } from '../../lib/authz'
import { questionSessions } from '../../lib/agentProjection'
import { commitDesk, decisionPermission, emptySources, questionItem, readQuestion, type Question } from '../../lib/decisionDeskApi'
import { useSession } from '../../stores/session'
import { useAgentProjection } from '../../stores/agentProjection'
import AppIcon from '../AppIcon.vue'

// A real Desk question in the chat. Messages never enter this decision path.
const props = defineProps<{ question: Question; sessionId: string; open: boolean; label: string }>()
const auth = useSession(), projection = useAgentProjection()
const current = ref(props.question)
const state = ref<'ready' | 'pending' | 'recorded' | 'failed' | 'elsewhere'>('ready')
const selected = ref(''), error = ref('')
let epoch = 0
let request: { id: string; revision: number; option: string; question: Question } | null = null
const owner = computed(() => auth.identity ? `${auth.identity.tenant.id}:${auth.identity.principal.id}` : '')
const sources = () => { const value = emptySources(); value.questions.set(`q:${current.value.id}`, current.value); return value }
const item = computed(() => questionItem(current.value, ''))
const permitted = computed(() => auth.identity?.principal.kind === 'person' && decisionPermission(item.value, sources(), can))
const choices = computed(() => current.value.input.options.length > 4 ? current.value.input.options.slice(0, 3) : current.value.input.options)
const desk = computed(() => ({ path: '/decision-desk', query: { item: `q:${current.value.id}` } }))
const answer = computed(() => {
  const value = current.value.answer
  if (!value) return ''
  const label = current.value.input.options.find(option => option.id === value.option_id)?.title ?? value.answer
  const at = Date.parse(value.created_at)
  const time = Number.isFinite(at) ? new Date(at).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : 'time unavailable'
  return state.value === 'elsewhere' ? `Answered on the Desk: ${label} · ${time}`
    : `Answered: ${label} · ${time}${value.decided_by === auth.identity?.principal.id ? ' · by you' : ''}`
})
function reset() { epoch++; request = null; selected.value = ''; error.value = ''; current.value = props.question; state.value = props.question.state === 'answered' ? 'elsewhere' : 'ready' }
watch(() => [owner.value, props.sessionId, props.question.id], reset, { flush: 'sync' })
watch(() => props.question, question => {
  if (question.revision < current.value.revision) return
  current.value = question
  if (question.state === 'answered' && state.value !== 'recorded') { epoch++; request = null; state.value = 'elsewhere' }
}, { deep: true })
const stopAccess = onAccessRevalidate({ reset, refresh: () => { if (!props.open) void refreshAnswer() } })
onBeforeUnmount(() => { epoch++; stopAccess() })
function matches(question: Question, id: string) { return question.id === id && question.project_id === props.question.project_id && questionSessions(question).includes(props.sessionId) }
async function refreshAnswer() {
  const turn = epoch, who = owner.value, id = current.value.id
  try {
    const question = await readQuestion(id)
    if (turn !== epoch || who !== owner.value || !matches(question, id)) return
    if (question.revision < current.value.revision) return
    current.value = question
    if (question.state === 'answered') { request = null; state.value = 'elsewhere' }
  } catch (e) { if (turn === epoch && who === owner.value) { state.value = 'failed'; error.value = e instanceof Error ? e.message : 'The question could not be read.' } }
}
watch(() => props.open, open => { if (!open && current.value.state === 'open') void refreshAnswer() })
async function decide(option: string, retry = false) {
  if (!permitted.value || state.value === 'pending' || current.value.state !== 'open') return
  const choice = current.value.input.options.find(value => value.id === option)
  if (!choice || choice.field || choice.unavailable) return
  if (!retry || !request) request = { id: crypto.randomUUID(), revision: current.value.revision, option, question: current.value }
  const action = request, turn = epoch, who = owner.value
  // Retry keeps exactly the original request and revision if the reply was lost.
  if (action.option !== option || action.revision !== current.value.revision) { request = null; await refreshAnswer(); return }
  selected.value = option; state.value = 'pending'; error.value = ''
  const source = emptySources(), original = questionItem(action.question, '')
  source.questions.set(original.id, action.question)
  try {
    await commitDesk(original, { optionId: option, answer: '', reason: '', outcome: 'once', dirty: true }, source, action.id, false)
    if (turn !== epoch || who !== owner.value) return
    const saved = source.questions.get(original.id)
    if (!saved || !matches(saved, action.question.id) || saved.revision <= action.revision || saved.state !== 'answered' || !saved.answer || saved.answer.option_id !== option) throw new Error('The server did not confirm this answer.')
    current.value = saved; state.value = 'recorded'; request = null
    projection.wake('question.answered')
  } catch (e) {
    if (turn !== epoch || who !== owner.value) return
    state.value = 'failed'; error.value = e instanceof Error ? e.message : 'The answer was not recorded.'
    if (e instanceof APIError && e.status === 409) { request = null; await refreshAnswer() }
  }
}
function retry() { if (selected.value) void decide(selected.value, true); else void refreshAnswer() }
defineExpose({ focus: () => document.getElementById(`question-${props.question.id}`)?.focus({ preventScroll: true }) })
</script>

<template>
  <article :id="`question-${question.id}`" class="session-question" :class="{ answered: current.state === 'answered' }" tabindex="-1" :aria-label="current.input.question">
    <div class="question-head"><strong>{{ label }}</strong><time :datetime="current.created_at">{{ new Date(current.created_at).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) }}</time><span class="qtag">{{ permitted ? 'Asks you' : 'Question' }}</span></div>
    <p class="question-text">{{ current.input.question }}</p>
    <div v-if="permitted || current.state === 'answered'" class="answer-slot">
      <div class="question-options" :class="{ hidden: ['recorded', 'failed', 'elsewhere'].includes(state) }" :inert="['recorded', 'failed', 'elsewhere'].includes(state) || undefined" :aria-hidden="['recorded', 'failed', 'elsewhere'].includes(state) || undefined">
        <template v-for="option in choices" :key="option.id">
          <RouterLink v-if="option.field" class="btn sm" :to="desk">{{ option.title }}<AppIcon name="chevron-right" :size="13" /></RouterLink>
          <button v-else type="button" class="btn sm" :class="{ primary: current.input.recommend === option.id }" :disabled="state === 'pending' || !!option.unavailable" :data-tip="option.unavailable || option.description" @click="decide(option.id)"><span class="option-label"><span :class="{ hidden: state === 'pending' && selected === option.id }">{{ option.title }}</span><span class="recording" :class="{ hidden: state !== 'pending' || selected !== option.id }">Recording…</span></span></button>
        </template>
        <RouterLink v-if="current.input.options.length > 4" class="btn sm ghost" :to="desk">All options on the Desk</RouterLink>
      </div>
      <div v-if="['recorded', 'elsewhere'].includes(state)" class="answer-result" role="status"><AppIcon name="check" :size="14" /><span>{{ answer }}</span></div>
      <div v-else-if="state === 'failed'" class="answer-result" role="alert"><span :data-tip="error">Not recorded. Try again</span><button type="button" class="btn sm ghost" @click="retry">Retry</button></div>
    </div>
    <p v-else class="readonly">Waits for a project admin</p>
    <RouterLink class="desk-link" :to="desk">{{ permitted ? 'Same item on the Decision Desk' : 'Open on the Desk' }}<AppIcon name="chevron-right" :size="13" /></RouterLink>
  </article>
</template>

<style scoped>
.session-question { max-width: 88%; padding: 9px 12px; border-radius: 14px; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); min-width: 0; overflow-wrap: anywhere; }
.session-question.answered { background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.question-head { display: flex; align-items: baseline; gap: 8px; margin-bottom: 3px; font-size: 12px; }
.question-head time { font: 500 11px var(--mono); color: var(--ink-3); }
.qtag { margin-left: auto; padding: 0 7px; border-radius: 999px; background: var(--surface-raised); color: var(--queue-wait-ink); font-size: 11px; font-weight: 650; }
.question-text { font-size: 13px; color: var(--ink); margin: 0; }
.answer-slot { position: relative; margin-top: 8px; min-height: 44px; }
.question-options { display: flex; flex-wrap: wrap; gap: 8px; }
.question-options .btn { min-height: 44px; white-space: normal; max-width: 100%; }
.option-label { display: grid; }
.option-label > span { grid-area: 1 / 1; }
.recording { align-self: center; }
.hidden { visibility: hidden; }
.answer-result { position: absolute; inset: 0; display: flex; align-items: center; gap: 8px; overflow: auto; font-size: 12.5px; color: var(--ink-2); }
.answer-result svg { flex: none; }
.answer-result .btn { flex: none; min-height: 44px; }
.desk-link { display: inline-flex; align-items: center; gap: 3px; min-height: 44px; color: var(--ink-3); font-size: 12px; text-decoration: none; }
.readonly { margin: 8px 0 0; font-size: 12.5px; color: var(--ink-2); }
</style>
