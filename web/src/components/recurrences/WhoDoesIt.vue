<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { getNode, listNodes } from '../../lib/api'
import { getKnowledge, listKnowledge } from '../../lib/knowledge'
import { HARNESS_LABEL } from '../../lib/agentState'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { ROUTINE_EFFORTS, effortVerb, effortWord, type RoutineDraft, type RoutineExecutionPolicy, type RoutineGuardEntry } from '../../lib/recurrences'
import type { WorkKind } from '../../lib/workKinds'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import RoutineFolds from './RoutineFolds.vue'

// "Who does it" (AEON-1182): just the ticket, as today, or an agent works on it.
// Agent-only rows sit below the switch, so switching moves nothing above it.
const agent = defineModel<boolean>('agent', { required: true })
const draft = defineModel<RoutineDraft>('draft', { required: true })
const props = defineProps<{ projectId: string; project: string; unavailable: string; kinds: WorkKind[]; kindsState: 'loading' | 'ready' | 'failed'; route: { model: string; harness: string } | null; routeState: 'loading' | 'ready' | 'failed' | 'none'; policy: RoutineExecutionPolicy | null; rules: RoutineGuardEntry[] | null; rulesState: 'unsaved' | 'loading' | 'ready' | 'failed'; owner: string; mine: boolean; fresh: boolean; idPrefix: string }>()
const id = (name: string) => `${props.idPrefix}-${name}`
// Reads first: up to 32 tickets or knowledge entries of the output project, read before each run.
const labels = ref(new Map<string, string>()), picker = ref<HTMLElement | null>(null), term = ref(''), hits = ref<{ kind: 'node' | 'knowledge'; id: string; label: string }[]>([]), searching = ref(false), searchFailed = ref('')
const scope = useIdentityScope(), names = scope.lane(), search = scope.lane()
watch(() => draft.value.sources.map(source => source.id).join(','), () => {
  const missing = draft.value.sources.filter(source => !labels.value.has(source.id))
  if (!missing.length) return
  void names.run(({ after }) => after(Promise.all(missing.map(source => (source.kind === 'knowledge' ? getKnowledge(source.id).then(entry => `${entry.title}`) : getNode(source.id).then(node => `${node.key} ${node.title}`)).catch(() => 'Unavailable'))), found => {
    const next = new Map(labels.value); missing.forEach((source, index) => next.set(source.id, found[index]!)); labels.value = next
  }))
}, { immediate: true })
let timer: ReturnType<typeof setTimeout> | undefined
function find() {
  clearTimeout(timer)
  const q = term.value.trim(), projectId = props.projectId
  searching.value = true; searchFailed.value = ''
  timer = setTimeout(() => void search.run(({ after, signal }) => after(Promise.all([listNodes({ within: projectId, kind: ['work'], q, sort: 'state,-updated_at', limit: 8 }, { signal }), listKnowledge({ project_id: projectId, q, limit: 8 }, signal)]), ([nodes, knowledge]) => {
    hits.value = [...knowledge.items.map(entry => ({ kind: 'knowledge' as const, id: entry.id, label: entry.title })), ...nodes.items.map(node => ({ kind: 'node' as const, id: node.id, label: `${node.key} ${node.title}` }))]
  }), { failed: error => { searchFailed.value = error instanceof Error ? error.message : 'Search failed.' }, settled: () => { searching.value = false } }), 160)
}
function openPicker(event: MouseEvent) { picker.value = event.currentTarget as HTMLElement; term.value = ''; hits.value = []; find() }
function add(hit: { kind: 'node' | 'knowledge'; id: string; label: string }) {
  if (!draft.value.sources.some(source => source.id === hit.id) && draft.value.sources.length < 32) { labels.value = new Map(labels.value).set(hit.id, hit.label); draft.value.sources = [...draft.value.sources, { kind: hit.kind, id: hit.id }] }
  const back = picker.value; picker.value = null; back?.focus({ preventScroll: true })
}
function chooseAgent() { if (!props.unavailable || agent.value) agent.value = true }
function remove(sourceId: string) { draft.value.sources = draft.value.sources.filter(source => source.id !== sourceId) }
const kindOptions = computed(() => props.kinds.some(kind => kind.id === draft.value.kind) || !draft.value.kind ? props.kinds : [...props.kinds, { id: draft.value.kind, label: 'Saved kind (no longer listed)' } as WorkKind])
const efforts = computed(() => ROUTINE_EFFORTS.some(([role]) => role === draft.value.role) ? ROUTINE_EFFORTS : [...ROUTINE_EFFORTS, [draft.value.role, effortWord(draft.value.role)] as const])
</script>

<template>
  <section :id="id('who')" class="editor-section" :aria-labelledby="id('who-h')">
    <p :id="id('who-h')" class="eyebrow">Who does it</p>
    <div class="field-row"><span class="field-label">Then</span><div class="seg who" role="radiogroup" aria-label="Who does it"><button type="button" role="radio" data-who="ticket" :aria-checked="!agent" @click="agent = false"><AppIcon name="ticket" :size="13" />Just create the ticket</button><button type="button" role="radio" data-who="agent" :aria-checked="agent" :aria-disabled="!!unavailable && !agent" :data-tip="unavailable && !agent ? unavailable : undefined" @click="chooseAgent"><AppIcon name="agent" :size="13" />An agent works on it</button></div></div>
    <div class="who-slot">
      <p v-if="!agent" class="hint pad">{{ unavailable || 'As today: people or the work queue pick it up.' }}</p>
      <template v-else>
        <div class="field-row"><label :for="id('kind')">Kind of work</label><div class="field-line">
          <select :id="id('kind')" v-model="draft.kind" class="field kind" :disabled="kindsState === 'loading'"><option v-if="!draft.kind" value="" disabled>{{ kindsState === 'loading' ? 'Loading kinds…' : kindsState === 'failed' ? 'Kinds couldn’t be read' : 'Pick a kind' }}</option><option v-for="kind in kindOptions" :key="kind.id" :value="kind.id">{{ kind.display_words?.label || kind.label }}</option></select>
          <span class="lbl-in">Effort</span>
          <div class="seg eff" role="radiogroup" aria-label="Effort"><button v-for="[role, label] in efforts" :key="role" type="button" role="radio" :data-role="role" :aria-checked="draft.role === role" @click="draft.role = role">{{ label }}</button></div>
        </div></div>
        <div class="field-row top"><span class="field-label">Reads first</span><div class="chips">
          <span v-for="source in draft.sources" :key="source.id" class="chip"><AppIcon :name="source.kind === 'knowledge' ? 'book' : 'ticket'" :size="12" /><span class="chip-t" :title="labels.get(source.id)">{{ labels.get(source.id) ?? 'Loading…' }}</span><button type="button" class="chip-x" :aria-label="`Remove ${labels.get(source.id) ?? 'source'}`" @click="remove(source.id)"><AppIcon name="close" :size="11" /></button></span>
          <button type="button" class="btn sm ghost" :disabled="draft.sources.length >= 32" aria-haspopup="dialog" :aria-expanded="!!picker" @click="openPicker"><AppIcon name="plus" :size="12" />Add a ticket or knowledge…</button>
          <span class="hint">Up to 32. Read before each run; nothing is fetched from outside.</span>
        </div></div>
        <p class="route"><AppIcon name="agent" :size="14" /><span class="rt-txt"><template v-if="routeState === 'ready' && route">Today <b>{{ route.model }} via {{ HARNESS_LABEL[route.harness] ?? route.harness }}</b> {{ effortVerb(draft.role) }}; a model of another family checks each action.</template><template v-else-if="routeState === 'loading'">Finding the model for {{ effortWord(draft.role) }}…</template><template v-else>No model is set for {{ effortWord(draft.role) }} yet{{ routeState === 'failed' ? ' (the route couldn’t be read)' : '' }}; Settings › Models decides.</template> It follows the ticket’s description and criteria as its instructions.</span><a href="/settings/models" target="_blank" rel="noopener" class="go">Settings › Models<AppIcon name="chevron-right" :size="12" /></a></p>
      </template>
    </div>
    <RoutineFolds v-if="agent" v-model="draft" :policy="policy" :rules="rules" :rules-state="rulesState" :owner="owner" :mine="mine" :project="project" :fresh="fresh" :id-prefix="idPrefix" />
    <FloatingPanel v-if="picker" :anchor="picker" :width="340" label="Add a ticket or knowledge" cycle @close="restore => { const back = picker; picker = null; if (restore) back?.focus() }">
      <div class="src-pop">
        <input v-model="term" class="field" type="search" aria-label="Search tickets and knowledge" placeholder="Search tickets and knowledge" data-autofocus @input="find" />
        <p v-if="searchFailed" class="hint" role="alert">{{ searchFailed }}</p>
        <p v-else-if="!hits.length" class="hint" role="status">{{ searching ? 'Searching…' : 'Nothing matches in this project.' }}</p>
        <div v-else role="listbox" aria-label="Results"><button v-for="hit in hits" :key="hit.id" type="button" role="option" class="src-hit" :aria-selected="draft.sources.some(source => source.id === hit.id)" @click="add(hit)"><AppIcon :name="hit.kind === 'knowledge' ? 'book' : 'ticket'" :size="13" /><span>{{ hit.label }}</span></button></div>
      </div>
    </FloatingPanel>
  </section>
</template>

<style scoped>
.editor-section { min-width: 0; display: grid; gap: 10px; padding: 16px 0; border-top: 1px solid var(--line); }
.field-row { display: grid; grid-template-columns: 84px minmax(0, 1fr); gap: 12px; align-items: center; min-height: 34px; }
.field-row > label, .field-label { font-size: 12.5px; color: var(--ink-3); }
.field-row.top { align-items: start; }
.top > .field-label { padding-top: 8px; }
.field-row > .seg { justify-self: start; }
.field-line { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 10px; min-width: 0; }
.field { font-size: 13px; min-width: 0; }
.kind { width: 180px; }
.lbl-in { margin-left: 8px; font-size: 12.5px; color: var(--ink-3); }
.who-slot { display: grid; gap: 10px; min-height: 22px; }
.hint { font-size: 12px; line-height: 1.45; color: var(--ink-3); }
.hint.pad { padding-left: 96px; }
.seg.who button[aria-disabled="true"] { opacity: .55; cursor: not-allowed; }
.chips { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 8px; min-width: 0; }
.chips .hint { flex-basis: 100%; }
.chip { display: inline-flex; align-items: center; gap: 6px; max-width: 100%; height: 28px; padding: 0 4px 0 10px; border-radius: 999px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); font-size: 12.5px; color: var(--ink); }
.chip > svg { flex: none; color: var(--ink-3); }
.chip-t { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chip-x { display: grid; place-items: center; flex: none; width: 22px; height: 22px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: var(--ink-3); }
.chip-x:hover { background: var(--row-hover); color: var(--ink); }
.route { display: flex; align-items: flex-start; flex-wrap: wrap; gap: 4px 8px; min-height: 58px; margin-left: 96px; padding: 8px 12px; border-radius: 10px; background: var(--surface-sunken); font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.route > svg { flex: none; margin-top: 2px; color: var(--ink-3); }
.route b { color: var(--ink); font-weight: 600; }
.route > .rt-txt { flex: 1 1 260px; min-width: 0; }
.go { display: inline-flex; align-items: center; gap: 2px; white-space: nowrap; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; text-decoration: none; }
.go:hover { text-decoration: underline; text-underline-offset: 2px; }
.src-pop { display: grid; gap: 6px; padding: 8px; }
[role="listbox"] { display: grid; gap: 1px; max-height: 260px; overflow: auto; }
.src-hit { display: grid; grid-template-columns: 14px minmax(0, 1fr); gap: 8px; align-items: center; min-height: 36px; padding: 4px 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); text-align: left; font-size: 13px; }
.src-hit span { overflow-wrap: anywhere; }
.src-hit:hover, .src-hit:focus-visible { background: var(--row-hover); }
.src-hit[aria-selected="true"] { background: var(--row-selected); }
@media (max-width: 600px) {
  .field-row { grid-template-columns: minmax(0, 1fr); gap: 6px; }
  .top > .field-label { padding: 0; }
  .hint.pad, .route { padding-left: 12px; margin-left: 0; }
  .hint.pad { padding-left: 0; }
  .route { min-height: 92px; }
  .seg.who { display: grid; grid-template-columns: minmax(0, 1fr); border-radius: 14px; justify-self: stretch; }
  .seg.who button { justify-content: flex-start; }
  .seg.eff { flex-wrap: wrap; border-radius: 14px; }
  .lbl-in { flex-basis: 100%; margin: 0; }
}
@media (pointer: coarse) {
  .seg button { min-width: 44px; min-height: 44px; }
  .chip { min-height: 44px; }
  .chip-x { width: 44px; height: 44px; margin: -8px -4px -8px 0; }
  .go { min-height: 44px; }
  .src-hit { min-height: 44px; }
}
</style>
