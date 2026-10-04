<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { vClipTip } from '../../directives/clipTip'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { listNodes, type ListItem, type WorkNode } from '../../lib/api'
import { keyPrefixOf, keyQuery, ticketResults, type TicketResult } from '../../lib/palette'
import { recents } from '../../lib/recents'
import { choiceById, RELATION_CHOICES, type RelationChoice } from '../../lib/relations'
import { searchWork, workKindMap } from '../../lib/ticketSearch'
import type { RelatedNode, RelatedTicket } from '../../lib/useTicket'
import { highlight } from '../../lib/work'
import { useProjects } from '../../stores/projects'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'
import ReadName from '../ReadName.vue'
import StatusIcon from './StatusIcon.vue'

// Link the open ticket to another: choose how they relate, then find the other
// by key or title with the command palette's search. Keyboard first: typing
// searches, arrows move, Enter links; Shift Tab reaches the relation, whose
// arrows change it. A refusal (a loop, an existing link) stays here in words.
const props = defineProps<{
  anchor: HTMLElement | null; subject: string; selfId: string; projectKey: string
  related: RelatedNode[]; link: (choice: RelationChoice, other: RelatedTicket) => Promise<string | null>
}>()
const emit = defineEmits<{ close: [restoreFocus: boolean] }>()
const projects = useProjects()

// The relation last used in this session comes first again.
const choice = ref<RelationChoice>(choiceById(lastChoice))
watch(choice, value => { lastChoice = value.id; refusal.value = '' })
const term = ref('')
const query = computed(() => term.value.trim())
const listed = ref<ListItem[]>([])
const hits = ref<WorkNode[]>([])
const workKinds = ref(new Map<string, string>())
const loading = ref(false)
const failed = ref('')
const refusal = ref('')
const busy = ref(false)
const active = ref(0)
let searched = ''
// Enter pressed before the answer for what was typed arrived: link its first hit then.
let enterPending = false
let timer: ReturnType<typeof setTimeout> | undefined
let controller: AbortController | null = null
const input = ref<HTMLInputElement>()
const types = ref<HTMLElement>()

const projectFor = (key: string) => projects.byRouteKey(keyPrefixOf(key))?.routeKey ?? null
// Recently opened tickets when nothing is typed; search results otherwise. The
// open ticket itself is never offered.
const results = computed<TicketResult[]>(() => {
  const found = query.value
    ? ticketResults(searched, listed.value, hits.value, workKinds.value, projectFor, null, 9)
    : recents.filter(recent => recent.type === 'ticket').map(recent => ({ type: 'ticket' as const, id: `recent-${recent.key}`, key: recent.key, title: recent.title, state: recent.state, kind: recent.kind, projectKey: recent.projectKey }))
  return found.filter(result => result.key !== props.subject).slice(0, 8)
})
// Tickets already linked this way are shown, marked, and not linked twice.
const linkedKeys = computed(() => new Set(props.related.filter(entry => entry.label === choice.value.label && entry.node).map(entry => entry.node!.key)))
watch(results, () => { if (active.value >= results.value.length) active.value = 0 })

async function search() {
  const q = query.value
  controller?.abort()
  if (!q) { listed.value = []; hits.value = []; loading.value = false; searched = ''; failed.value = ''; return }
  controller = new AbortController()
  const signal = controller.signal
  loading.value = true; failed.value = ''
  try {
    if (!workKinds.value.size) workKinds.value = await workKindMap()
    const found = await searchWork(q, { signal })
    if (signal.aborted) return
    listed.value = found.listed; hits.value = found.hits; searched = q; active.value = 0
    if (enterPending) { enterPending = false; await nextTick(); void choose(results.value[0]) }
  } catch (e) {
    if (!signal.aborted) { failed.value = e instanceof Error ? e.message : 'Search is unavailable'; enterPending = false }
  } finally {
    if (!signal.aborted) loading.value = false
  }
}
watch(term, () => {
  clearTimeout(timer); refusal.value = ''; enterPending = false
  if (!query.value) { void search(); return }
  loading.value = true
  timer = setTimeout(() => void search(), 120)
})
onBeforeUnmount(() => { clearTimeout(timer); controller?.abort() })

// A recent ticket carries no id: its key finds it.
async function resolve(result: TicketResult): Promise<RelatedTicket | null> {
  if (!result.id.startsWith('recent-')) return { id: result.id, key: result.key, title: result.title, state: result.state }
  const page = await listNodes({ q: result.key, kind: ['ticket', 'task', 'epic'], sort: 'key', limit: 5 })
  const item = page.items.find(candidate => candidate.key === result.key)
  return item ? { id: item.id, key: item.key, title: item.title, state: item.state } : null
}
async function choose(result: TicketResult | undefined) {
  if (!result || busy.value) return
  if (linkedKeys.value.has(result.key)) { refusal.value = `${props.subject} is already linked to ${result.key} as “${choice.value.label}”.`; return }
  busy.value = true; refusal.value = ''
  try {
    const other = await resolve(result)
    if (!other) { refusal.value = `${result.key} is no longer available.`; return }
    const problem = await props.link(choice.value, other)
    if (problem) { refusal.value = problem; await nextTick(); input.value?.focus(); return }
    emit('close', true)
  } catch (e) {
    refusal.value = e instanceof Error ? e.message : 'The link could not be made'
  } finally {
    busy.value = false
  }
}

function keydown(event: KeyboardEvent) {
  const count = results.value.length
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    if (count) active.value = (active.value + (event.key === 'ArrowDown' ? 1 : -1) + count) % count
    document.getElementById(`relation-option-${active.value}`)?.scrollIntoView({ block: 'nearest' })
  } else if (event.key === 'Enter') {
    event.preventDefault()
    if (query.value && searched !== query.value) enterPending = true
    else void choose(results.value[active.value])
  }
}
// The relation group is one Tab stop; arrows move and choose, as radios do.
function typeKeydown(event: KeyboardEvent) {
  const index = RELATION_CHOICES.findIndex(option => option.id === choice.value.id)
  // Left and right step through the grid; up and down move a row of three.
  const step = { ArrowRight: 1, ArrowDown: 3, ArrowLeft: -1, ArrowUp: -3 }[event.key]
  let next = index
  if (step) next = (index + step + RELATION_CHOICES.length) % RELATION_CHOICES.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = RELATION_CHOICES.length - 1
  else if (event.key === 'Enter') { event.preventDefault(); input.value?.focus(); return }
  else return
  event.preventDefault()
  choice.value = RELATION_CHOICES[next]
  void nextTick(() => types.value?.querySelector<HTMLElement>('[aria-checked="true"]')?.focus())
}
function pick(option: RelationChoice) { choice.value = option; input.value?.focus() }
</script>

<script lang="ts">
let lastChoice = 'relates'
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="420" :tallest="520" :label="`Link ${subject} to another ticket`" cycle @close="restore => emit('close', restore)">
    <div class="picker" :aria-busy="busy">
      <p class="head"><span class="eyebrow">Link {{ subject }}</span><span class="hint">{{ choice.hint }}</span></p>
      <div ref="types" class="types" role="radiogroup" :aria-label="`How ${subject} relates to the other ticket`" @keydown="typeKeydown">
        <button
          v-for="option in RELATION_CHOICES" :key="option.id" type="button" role="radio" class="type"
          :aria-checked="option.id === choice.id" :tabindex="option.id === choice.id ? 0 : -1" @click="pick(option)"
        >{{ option.label }}</button>
      </div>
      <label class="search">
        <AppIcon name="search" :size="14" class="lead" />
        <input
          ref="input" v-model="term" class="field" type="text" role="combobox" aria-controls="relation-results" aria-autocomplete="list" :aria-expanded="true"
          :aria-activedescendant="results.length ? `relation-option-${active}` : undefined" :aria-label="`Find the ticket ${subject} ${choice.label.toLowerCase()}`"
          placeholder="Find a ticket by key or title" autocomplete="off" spellcheck="false" data-autofocus @keydown="keydown"
        />
        <span v-if="loading || busy" class="spinner" aria-hidden="true" />
      </label>
      <p v-if="refusal" class="refusal" role="alert"><AppIcon name="alert" :size="14" /><span>{{ refusal }}</span></p>
      <div id="relation-options" class="options" :class="{ stale: loading && !!results.length }">
        <p v-if="!query && results.length" class="group-label eyebrow" aria-hidden="true">Recent</p>
        <div id="relation-results" class="option-list" role="listbox" :aria-label="query ? 'Tickets found' : 'Recent tickets'">
          <div v-for="(result, index) in results" :key="result.id" class="option-row" role="presentation">
            <div
              :id="`relation-option-${index}`" role="option" class="option"
              :aria-selected="index === active" :aria-disabled="linkedKeys.has(result.key) || undefined"
              :tabindex="index === active ? 0 : -1" @keydown.enter.prevent="choose(result)"
              @pointermove="active = index" @click="choose(result)"
            >
              <StatusIcon :state="result.state" :size="12" />
              <span class="key"><template v-for="(part, i) in highlight(result.key, keyQuery(query) ? query : '')" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
              <span v-clip-tip="result.title" class="title"><template v-for="(part, i) in highlight(result.title, query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
              <span v-if="linkedKeys.has(result.key)" class="tag">Linked</span>
              <span v-else-if="result.projectKey && result.projectKey !== projectKey" class="tag project">{{ result.projectKey }}</span>
              <AppIcon v-if="index === active && !linkedKeys.has(result.key)" name="enter" :size="13" class="enter" />
            </div>
          </div>
        </div>
        <div v-if="results.length" class="option-reads" role="group" aria-label="Read full ticket names">
          <div v-for="result in results" :key="result.id" class="read-row"><ReadName :text="`${result.key} · ${result.title}`" /></div>
        </div>
        <p v-if="failed" class="note error" role="alert">{{ failed }}</p>
        <p v-else-if="query && !loading && !results.length && searched === query" class="note">Nothing matches “{{ query }}”. Try a key like {{ projectKey }}-12 or words from a title.</p>
        <p v-else-if="!query && !results.length" class="note">Type a key like {{ projectKey }}-12, or words from a title.</p>
      </div>
      <p class="foot" aria-hidden="true">
        <span><kbd class="keycap"><AppIcon name="arrow-up" /></kbd><kbd class="keycap"><AppIcon name="arrow-down" /></kbd> move</span>
        <span><kbd class="keycap"><AppIcon name="enter" /></kbd> link</span>
        <span><kbd class="keycap"><AppIcon name="shift" /></kbd><kbd class="keycap">tab</kbd> relation</span>
      </p>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.picker { display: grid; gap: 8px; padding: 4px 4px 2px; }
.head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; padding: 2px 4px 0; }
.head .hint { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--ink-2); }
.types { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 2px; padding: 3px; border-radius: 14px; background: var(--seg-bg); box-shadow: inset 0 1px 2px rgba(32, 60, 61, .08); }
.type { display: inline-flex; align-items: center; justify-content: center; min-width: 0; height: 28px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
@media (hover: hover) { .type:hover { color: var(--teal-ink); background: var(--row-hover); } }
.type[aria-checked="true"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: 0 1px 2px rgba(32, 60, 61, .12), inset 0 0 0 1px var(--glass-edge); }
.type:focus-visible { box-shadow: var(--focus-ring); }
.search { position: relative; display: flex; align-items: center; }
.search .lead { position: absolute; left: 11px; color: var(--ink-3); pointer-events: none; }
.search .field { height: 36px; padding-left: 32px; padding-right: 32px; font-size: 13.5px; }
.spinner { position: absolute; right: 11px; width: 14px; height: 14px; border-radius: 50%; border: 2px solid var(--line-2); border-top-color: var(--teal); }
@media (prefers-reduced-motion: no-preference) { .spinner { animation: spin .8s linear infinite; } @keyframes spin { to { transform: rotate(360deg); } } }
.refusal { display: flex; align-items: flex-start; gap: 8px; padding: 8px 10px; border-radius: 10px; background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); font-size: 12.5px; line-height: 1.45; }
.refusal svg { flex-shrink: 0; margin-top: 2px; }
.refusal span { color: var(--ink); }
.options { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 1px; transition: opacity .12s ease; }
.option-list, .option-reads { display: grid; gap: 1px; align-content: start; }
.option-list { grid-column: 1; }.option-reads { grid-column: 2; }
.option-row, .read-row { display: grid; min-height: 36px; }
.options > .group-label, .options > .note { grid-column: 1 / -1; }
.options.stale { opacity: .6; }
.group-label { padding: 4px 8px 2px; }
.option { display: flex; align-items: center; min-width: 0; gap: 9px; min-height: 36px; padding: 0 10px; border-radius: 8px; color: var(--ink); font-size: 13px; cursor: pointer; }
.option[aria-selected="true"] { background: var(--row-selected); }
.option[aria-disabled="true"] { cursor: default; }
.option[aria-disabled="true"] .title, .option[aria-disabled="true"] .key { color: var(--ink-3); }
.key { flex-shrink: 0; font: 500 11.5px/1 var(--mono); color: var(--ink-2); font-variant-ligatures: none; }
.option[aria-selected="true"]:not([aria-disabled="true"]) .key { color: var(--teal-ink); }
.option mark { color: var(--ink); }
.title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tag { flex-shrink: 0; height: 20px; padding: 0 7px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font: 500 10.5px/20px var(--mono); letter-spacing: .04em; font-variant-ligatures: none; }
.tag.project { color: var(--ink-3); }
.enter { flex-shrink: 0; color: var(--ink-3); }
.note { padding: 8px 10px; font-size: 12.5px; color: var(--ink-3); }
.note.error { color: var(--danger); }
.foot { display: flex; flex-wrap: wrap; gap: 14px; padding: 6px 6px 2px; border-top: 1px solid var(--line); font-size: 11.5px; color: var(--ink-3); }
.foot span { display: inline-flex; align-items: center; gap: 4px; }
@media (max-width: 600px), (pointer: coarse) {
  .type { height: 40px; padding: 0 6px; font-size: 13px; }
  .search .field { height: 44px; font-size: 16px; }
  .option, .option-row, .read-row { min-height: 44px; }
  .foot { display: none; }
}

@media (max-width: 720px) {
  .option, .option-row, .read-row { height: 52px; min-height: 52px; }
  .title { line-height: 18px; }
  .title { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; white-space: normal; overflow-wrap: anywhere; }
}
</style>
