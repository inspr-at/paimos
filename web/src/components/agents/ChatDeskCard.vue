<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
import type { Question } from '../../lib/decisionDeskApi'
// One read per person and question at a time; a failed read is forgotten so the
// next card retries. Bounded, so a long history cannot grow it without limit.
const reads = new Map<string, Promise<Question>>()
const MAX_READS = 100
// A long history opens many cards at once: at most four reads run together.
let active = 0
const waiting: (() => void)[] = []
async function limited<T>(task: () => Promise<T>): Promise<T> {
  if (active >= 4) await new Promise<void>(resolve => waiting.push(resolve))
  active++
  try { return await task() } finally { active--; waiting.shift()?.() }
}
</script>
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import { displayLanguage } from '../../lib/displayLanguage'
import { readQuestion } from '../../lib/decisionDeskApi'
import { absoluteTime } from '../../lib/work'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import { clockTime } from './chatThread'
import { chatWords } from './sessionChat'

// A Decision Desk delivery in the thread (AEON-1438): the question, its state and
// Open. The delivery's own text is the agent's instruction and stays in the data.
const props = defineProps<{ questionId: string; answer?: string; corrected?: boolean; at?: string; questions?: Question[]; sessionId?: string }>()
const identity = useSession()
const words = computed(() => chatWords[displayLanguage()])
const viewer = computed(() => identity.identity ? `${identity.identity.tenant.id}:${identity.identity.principal.id}` : '')
const fetched = ref<Question | null>(null)
const loading = ref(false)
let run = 0
const known = computed(() => props.questions?.find(question => question.id === props.questionId) ?? null)
// The newer record wins: the shared projection or this card's own read.
const question = computed(() => {
  const a = known.value, b = fetched.value
  if (!a || !b) return a ?? b
  return b.revision > a.revision ? b : a
})
// A new answer or correction reads the record again; the old text stays until
// the new one arrives, so the card does not blink. A person change drops it.
watch(() => [viewer.value, props.questionId, props.answer ?? '', !!props.corrected, !!known.value] as const, async ([who, id, answer, corrected, shared], before) => {
  const turn = ++run
  if (!before || before[0] !== who || before[1] !== id) fetched.value = null
  loading.value = false
  if (!who || shared) return
  const key = `${who}:${id}:${answer}:${corrected}`
  let read = reads.get(key)
  if (!read) {
    if (reads.size >= MAX_READS) reads.delete(reads.keys().next().value!)
    read = limited(() => readQuestion(id))
    reads.set(key, read)
  }
  loading.value = true
  try {
    const result = await read
    if (turn === run && result.id === id) fetched.value = result
  } catch {
    reads.delete(key)
  } finally {
    if (turn === run) loading.value = false
  }
}, { immediate: true })

const text = computed(() => {
  const q = question.value
  if (!q) return ''
  return q.askers.find(asker => asker.session_id && asker.session_id === props.sessionId)?.input.question || q.input.question
})
const title = computed(() => text.value || (loading.value ? '' : words.value.deskFallback))
const state = computed<{ kind: 'answered' | 'waiting' | 'unknown'; label?: string }>(() => {
  const q = question.value
  if (q?.state === 'answered' && q.answer) {
    const option = q.answer.option_id ? q.input.options.find(item => item.id === q.answer!.option_id) : undefined
    return { kind: 'answered', label: option?.title || q.answer.answer || props.answer }
  }
  if (props.answer) return { kind: 'answered', label: props.answer }
  if (q?.state === 'open') return { kind: 'waiting' }
  return { kind: 'unknown' }
})
const decides = computed(() => identity.identity?.principal.kind === 'person' && !!question.value && can('questions.decide', question.value.project_id))
const desk = computed(() => ({ path: '/decision-desk', query: { item: `q:${props.questionId}` } }))
</script>

<template>
  <article class="desk-delivery" :aria-label="`${words.deskTitle}: ${title || words.deskFallback}`">
    <p class="desk-meta">
      <span class="desk-glyph" aria-hidden="true"><AppIcon name="check" :size="12" /></span>
      <span class="desk-who">{{ words.deskTitle }}</span>
      <template v-if="at"><span class="sep" aria-hidden="true">·</span><time :datetime="at" :data-tip="absoluteTime(at)">{{ clockTime(at) }}</time></template>
    </p>
    <p class="desk-q"><span v-if="!title" class="skeleton desk-skeleton" aria-hidden="true" />{{ title }}</p>
    <div class="desk-foot">
      <p v-if="state.kind === 'answered'" class="desk-state"><AppIcon name="circle-check" :size="15" class="ok" /><span>{{ corrected ? words.deskCorrected : words.deskAnswered }} <b>{{ state.label }}</b></span></p>
      <p v-else-if="state.kind === 'waiting'" class="desk-state waiting"><AppIcon name="clock" :size="15" /><span>{{ decides ? words.deskWaiting : words.deskWaitingOther }}</span></p>
      <span v-else class="desk-state" />
      <RouterLink class="desk-open" :to="desk" :aria-label="words.deskOpenLabel">{{ words.deskOpen }}<AppIcon name="chevron-right" :size="14" /></RouterLink>
    </div>
  </article>
</template>

<style scoped>
/* Paper on the thread: the meta line sits above it on desktop, inside it on phones. */
.desk-delivery { --meta-h: 25px; position: relative; isolation: isolate; max-width: 540px; min-width: 0; padding-bottom: 8px; }
.desk-delivery::before { content: ''; position: absolute; z-index: -1; inset: var(--meta-h) 0 0; border-radius: 12px; background: var(--paper); box-shadow: 0 0 0 1px var(--paper-line), 0 10px 24px -20px var(--shadow-color); }
.desk-meta { display: flex; align-items: center; gap: 6px; min-width: 0; min-height: 20px; margin: 0 0 5px; font-size: 12.5px; color: var(--ink-3); white-space: nowrap; }
.desk-glyph { display: grid; place-items: center; flex: none; width: 20px; height: 20px; border-radius: 50%; background: var(--secondary-tint-3); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--secondary-line) 40%, transparent); color: var(--secondary-ink); }
.desk-who { overflow: hidden; text-overflow: ellipsis; color: var(--ink); font-weight: 650; }
.desk-meta time { font-size: 12px; font-variant-numeric: tabular-nums; }
.desk-q { margin: 0; padding: 12px 14px 0; font-size: 14.5px; font-weight: 600; line-height: 1.45; color: var(--ink); overflow-wrap: anywhere; }
.desk-skeleton { display: inline-block; width: min(70%, 320px); height: 12px; vertical-align: middle; }
.desk-foot { display: flex; align-items: center; gap: 8px; min-height: 30px; margin-top: 6px; padding: 0 8px 0 14px; font-size: 13px; color: var(--ink-2); }
.desk-state { display: inline-flex; align-items: center; gap: 6px; flex: 1; min-width: 0; margin: 0; overflow-wrap: anywhere; }
.desk-state > svg { flex: none; color: var(--ink-3); }
.desk-state > svg.ok { color: var(--ok); }
.desk-state b { font-weight: 600; color: var(--ink); }
.desk-state.waiting { color: var(--ink); font-weight: 600; }
.desk-open { display: inline-flex; align-items: center; gap: 4px; flex: none; height: 30px; padding: 0 6px 0 10px; border-radius: 8px; color: var(--teal-ink); font-size: 13px; font-weight: 600; text-decoration: none; }
@media (hover: hover) { .desk-open:hover { background: var(--row-hover); } }
.desk-open:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (pointer: coarse) { .desk-open { height: 44px; } }
@media (max-width: 720px) {
  .desk-delivery { --meta-h: 0px; max-width: none; padding: 10px 12px 0; }
  .desk-delivery::before { border-radius: 14px; box-shadow: inset 0 0 0 1px var(--paper-edge), 0 1px 3px color-mix(in srgb, var(--shadow-color) 7%, transparent); }
  .desk-meta { gap: 8px; margin-bottom: 4px; font-size: 12px; }
  .desk-meta .sep { display: none; }
  .desk-who { font-size: 13px; }
  .desk-q { margin-top: 2px; padding: 0; font-size: 15px; }
  .desk-foot { min-height: 44px; margin-top: 0; padding: 0; font-size: 13.5px; }
  .desk-state > svg { display: none; }
  .desk-open { height: 44px; margin-right: -6px; padding: 0 6px 0 12px; border-radius: 999px; font-size: 14px; }
}
@media (max-width: 359px) { .desk-meta { gap: 5px; } .desk-glyph { width: 18px; height: 18px; } }
</style>
