<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { confirmAction } from '../../lib/confirm'
import {
  KnowledgeError, acceptLearning, dismissLearning, listLearnings, undoKnowledge,
  type KnowledgeEntry, type KnowledgeItem, type MethodLearning,
} from '../../lib/knowledge'
import { toast } from '../../lib/toast'
import { relativeTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'

// Open process-learning candidates for this project. A person accepts one into
// an entry's changelog, or dismisses it. Agents and viewers can read the list.
const props = defineProps<{
  project: { id: string; routeKey: string; title: string }
  entries: KnowledgeItem[]
  canWrite: boolean
  person: boolean
  now: number
}>()
const emit = defineEmits<{ accepted: [entry: KnowledgeEntry]; reverted: [] }>()

const items = ref<MethodLearning[]>([])
const truncated = ref(false)
const error = ref('')
const busy = ref(false)
const accepting = ref<MethodLearning | null>(null)
const chosen = ref('')
const dialogError = ref('')
const dialog = ref<HTMLDialogElement>()
let opener: HTMLElement | null = null
let controller: AbortController | undefined

const canDecide = computed(() => props.person && props.canWrite)
const choices = computed(() => {
  const here = props.entries.filter(entry => !entry.project || entry.project.id === props.project.id)
  const live = here.filter(entry => entry.status !== 'archived')
  const pool = live.length ? live : here
  return [...pool].sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at) || a.title.localeCompare(b.title))
})
const chosenEntry = computed(() => choices.value.find(entry => entry.id === chosen.value) ?? null)

function preferred(entries: KnowledgeItem[]) {
  return entries.find(entry => /flywheel|changelog/i.test(`${entry.slug} ${entry.title}`))
    ?? entries.find(entry => entry.type === 'runbook')
    ?? entries[0]
}

function linkLabel(key: string) {
  return key.replaceAll('[', '(').replaceAll(']', ')').replaceAll('(', '⟨').replaceAll(')', '⟩')
}
const previewDate = computed(() => new Date().toISOString().slice(0, 10))

async function load() {
  controller?.abort()
  const current = controller = new AbortController()
  try {
    const page = await listLearnings(props.project.id, current.signal)
    if (current.signal.aborted) return
    items.value = page.items
    truncated.value = page.truncated
    error.value = ''
  } catch (e) {
    if (current.signal.aborted) return
    items.value = []
    error.value = e instanceof Error ? e.message : 'Method learnings could not be loaded.'
  }
}
watch(() => props.project.id, () => { void load() }, { immediate: true })
watch(choices, entries => {
  if (!accepting.value || entries.some(entry => entry.id === chosen.value)) return
  chosen.value = preferred(entries)?.id ?? ''
})
onBeforeUnmount(() => controller?.abort())

function openAccept(item: MethodLearning, event: MouseEvent) {
  opener = event.currentTarget instanceof HTMLElement ? event.currentTarget : null
  accepting.value = item
  chosen.value = preferred(choices.value)?.id ?? ''
  dialogError.value = ''
  void nextTick(() => dialog.value?.showModal())
}
function close() {
  dialog.value?.close()
  accepting.value = null
  dialogError.value = ''
  opener?.focus()
}
function backdrop(event: MouseEvent) { if (event.target === dialog.value) close() }

async function accept() {
  const item = accepting.value
  const entry = chosenEntry.value
  if (!item || !entry || busy.value) return
  busy.value = true
  dialogError.value = ''
  let since = entry.updated_at
  try {
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        const decision = await acceptLearning(item.id, entry.id, since)
        items.value = items.value.filter(row => row.id !== item.id)
        if (decision.entry) emit('accepted', decision.entry)
        close()
        toast('Added to the changelog.', { action: { label: 'Undo', run: () => void undo(decision.event_id) }, timeout: 8000 })
        return
      } catch (e) {
        if (attempt === 0 && e instanceof KnowledgeError && e.status === 412 && e.entry) {
          emit('accepted', e.entry)
          since = e.entry.updated_at
          continue
        }
        throw e
      }
    }
  } catch (e) {
    dialogError.value = e instanceof Error ? e.message : 'That could not be added.'
  } finally {
    busy.value = false
  }
}

async function dismiss(item: MethodLearning) {
  const ok = await confirmAction({
    title: 'Dismiss this learning?',
    body: item.text,
    confirmLabel: 'Dismiss learning',
    cancelLabel: 'Keep it',
  })
  if (!ok) return
  busy.value = true
  try {
    const decision = await dismissLearning(item.id)
    items.value = items.value.filter(row => row.id !== item.id)
    toast('Dismissed.', { action: { label: 'Undo', run: () => void undo(decision.event_id) }, timeout: 8000 })
  } catch (e) {
    toast(e instanceof Error ? e.message : 'Dismiss did not work.', { tone: 'error' })
  } finally {
    busy.value = false
  }
}

async function undo(eventId: number) {
  try {
    await undoKnowledge(eventId)
    await load()
    emit('reverted')
    toast('Undone.')
  } catch (e) {
    toast(e instanceof Error ? e.message : 'Undo did not work.', { tone: 'error' })
  }
}
</script>

<template>
  <section v-if="items.length || error" class="learnings glass-card" aria-labelledby="method-learnings-title">
    <div class="head">
      <span class="mark" aria-hidden="true"><AppIcon name="sparkle" :size="15" /></span>
      <div class="head-copy">
        <h2 id="method-learnings-title">Method learnings</h2>
        <p v-if="items.length && !canDecide" class="wait">Waiting for a person to accept.</p>
        <p v-else-if="error" class="wait" role="status">{{ error }}</p>
      </div>
    </div>
    <ul v-if="items.length" class="rows">
      <li v-for="item in items" :key="item.id" class="row">
        <div class="copy">
          <p class="line" :title="item.text">{{ item.text }}</p>
          <p class="meta">
            <a :href="item.href">{{ item.key }}</a>
            <span>{{ item.source === 'comment' ? 'Comment' : 'Ticket' }}</span>
            <span v-if="item.author">{{ item.author.name }}</span>
            <time :datetime="item.at" :title="item.at">{{ relativeTime(item.at, { now }) }}</time>
          </p>
        </div>
        <div v-if="canDecide" class="actions">
          <button type="button" class="btn primary" :disabled="busy" @click="openAccept(item, $event)">Accept</button>
          <button type="button" class="btn" :disabled="busy" :aria-label="`Dismiss ${item.key}`" @click="dismiss(item)">Dismiss</button>
        </div>
      </li>
    </ul>
    <p v-if="truncated" class="more">Showing the 50 newest.</p>

    <dialog ref="dialog" class="learn-dialog" aria-labelledby="learn-accept-title" @cancel.prevent="close" @click="backdrop">
      <form v-if="accepting" class="learn-card" @submit.prevent="accept">
        <div class="head">
          <span class="mark" aria-hidden="true"><AppIcon name="book" :size="15" /></span>
          <div class="head-copy">
            <h2 id="learn-accept-title">Add to a changelog</h2>
            <p class="wait">{{ accepting.text }}</p>
          </div>
        </div>
        <label class="pick" for="learn-entry">Entry</label>
        <select id="learn-entry" v-model="chosen" class="entry" :disabled="!choices.length">
          <option v-if="!choices.length" value="">No entry in this project yet</option>
          <option v-for="entry in choices" :key="entry.id" :value="entry.id">{{ entry.title }}</option>
        </select>
        <p v-if="chosenEntry" class="where">Adds this line to {{ chosenEntry.title }}.</p>
        <p v-if="accepting" class="preview">
          <span>- {{ previewDate }}: {{ accepting.text }}.</span>
          <span>Source: [{{ linkLabel(accepting.key) }}]({{ accepting.href }}).</span>
        </p>
        <p v-if="dialogError" class="problem" role="alert">{{ dialogError }}</p>
        <div class="foot">
          <button type="button" class="btn" @click="close">Cancel</button>
          <button type="submit" class="btn primary" :disabled="busy || !chosen">{{ busy ? 'Adding…' : 'Add to changelog' }}</button>
        </div>
      </form>
    </dialog>
  </section>
</template>

<style scoped>
.learnings { container-type: inline-size; margin: 0 0 18px; padding: 14px 16px 16px; }
.head { display: flex; align-items: flex-start; gap: 10px; }
.head-copy { min-width: 0; }
h2 { margin: 0; font-size: 15px; font-weight: 600; letter-spacing: -0.01em; }
.wait { margin: 2px 0 0; color: var(--ink-2); font-size: 13px; overflow-wrap: anywhere; }
.mark { display: grid; place-items: center; flex-shrink: 0; width: 28px; height: 28px; border-radius: 8px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); }
.rows { list-style: none; margin: 12px 0 0; padding: 0; display: grid; gap: 8px; }
.row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 10px 16px; align-items: center; min-width: 0; padding: 10px 12px; border-radius: 12px; background: var(--surface-sunken); }
.copy { min-width: 0; }
.line { margin: 0; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; font-size: 14px; line-height: 1.4; }
.meta { display: flex; flex-wrap: wrap; gap: 4px 10px; margin: 4px 0 0; color: var(--ink-2); font-size: 12.5px; }
.meta a { color: var(--teal-ink); font-weight: 600; }
.actions { display: flex; gap: 8px; align-items: center; }
.actions .btn { display: inline-flex; align-items: center; justify-content: center; min-height: 32px; }
.more { margin: 10px 0 0; color: var(--ink-3); font-size: 12.5px; }
.learn-dialog { width: min(520px, calc(100vw - 24px)); max-height: calc(100dvh - 24px); padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.learn-dialog::backdrop { background: var(--scrim); }
.learn-card { display: grid; gap: 10px; max-height: calc(100dvh - 24px); overflow: auto; padding: 18px 18px 16px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); }
.pick { font-size: 12.5px; font-weight: 600; color: var(--ink-2); }
.entry { width: 100%; min-width: 0; min-height: 40px; padding: 0 10px; border-radius: 10px; border: 1px solid var(--line-2); background: var(--field-bg); color: var(--ink); }
.where { margin: 0; color: var(--ink-2); font-size: 13px; }
.preview { display: grid; gap: 4px; margin: 0; padding: 10px 12px; border-radius: 10px; background: var(--code-bg); color: var(--ink); font-size: 13px; line-height: 1.45; }
.preview span { overflow-wrap: anywhere; }
.problem { margin: 0; color: var(--danger); font-size: 13px; }
.foot { display: flex; justify-content: flex-end; gap: 8px; margin-top: 4px; }
.foot .btn { display: inline-flex; align-items: center; justify-content: center; min-height: 36px; }
@container (max-width: 560px) {
  .row { grid-template-columns: minmax(0, 1fr); }
  .actions .btn { flex: 1; min-height: 40px; }
}
@media (max-width: 480px) {
  .foot { flex-direction: column-reverse; }
  .foot .btn { min-height: 40px; }
  .learn-card { padding: 16px 14px 14px; }
}
@media (prefers-reduced-motion: reduce) {
  .learn-dialog, .row, .btn { transition: none; }
}
</style>
