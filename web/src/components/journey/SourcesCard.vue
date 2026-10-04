<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import type { Intake, IntakeSource } from '../../lib/journey'
import { absoluteTime, relativeTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'

// What Aithema recorded for the project: conversations (with their transcript,
// read-only here) and files, links and notes. The conversation itself happens in
// Aithema; this card only shows what it stored.
const props = defineProps<{ intake: Intake; status: string; error: string; now: number }>()
const emit = defineEmits<{ retry: [] }>()
const open = ref<Set<string>>(new Set())
const term = ref<Record<string, string>>({})
const turnsOf = (source: IntakeSource) => props.intake.turns.filter(t => t.source_id === source.id).sort((a, b) => a.ordinal - b.ordinal)
const conversations = computed(() => props.intake.sources.filter(s => s.kind === 'conversation'))
const others = computed(() => props.intake.sources.filter(s => s.kind !== 'conversation'))
function toggle(id: string) { const next = new Set(open.value); if (next.has(id)) next.delete(id); else next.add(id); open.value = next }
function shownTurns(source: IntakeSource) {
  const needle = (term.value[source.id] ?? '').trim().toLowerCase()
  const turns = turnsOf(source)
  return needle ? turns.filter(t => t.body.toLowerCase().includes(needle)) : turns
}
const ICON: Record<IntakeSource['kind'], 'send' | 'paperclip' | 'link' | 'edit'> = { conversation: 'send', file: 'paperclip', url: 'link', note: 'edit' }
const external = (source: IntakeSource) => source.kind === 'url' && /^https?:\/\//.test(source.locator ?? '')
// A citation opens its source (and turn) and flashes it.
async function reveal(sourceId: string, turnId?: string) {
  if (!open.value.has(sourceId) && props.intake.sources.find(s => s.id === sourceId)?.kind === 'conversation') toggle(sourceId)
  await nextTick()
  const el = document.getElementById(turnId ? `turn-${turnId}` : `source-${sourceId}`)
  if (!el) return
  el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  el.classList.remove('j-flash'); void el.offsetWidth; el.classList.add('j-flash')
}
defineExpose({ reveal })
</script>

<template>
  <section class="j-card sources" aria-labelledby="sources-title" data-sources>
    <header class="j-card-head">
      <p id="sources-title" class="eyebrow">Sources · stored with the project</p>
      <span v-if="intake.sources.length" class="j-count">{{ intake.sources.length }}</span>
    </header>
    <p v-if="status === 'loading' && !intake.sources.length" class="skeleton src-skel" aria-label="Loading sources" role="status" />
    <div v-else-if="status === 'error'" class="j-empty" role="alert">
      <strong>The sources could not be loaded</strong><span>{{ error }}</span>
      <button type="button" class="btn sm" @click="emit('retry')"><AppIcon name="refresh" :size="13" />Try again</button>
    </div>
    <div v-else-if="!intake.sources.length" class="j-empty">
      <span class="j-empty-icon"><AppIcon name="send" :size="16" /></span>
      <strong>No conversation or sources yet</strong>
      <span>When Aithema talks the project through with you, every turn is stored here, with the files, links and notes it used. The conversation itself opens from Aithema.</span>
    </div>
    <template v-else>
      <article v-for="source in conversations" :id="`source-${source.id}`" :key="source.id" class="conv">
        <header class="conv-head">
          <span class="src-icon" aria-hidden="true"><AppIcon :name="ICON[source.kind]" :size="14" /></span>
          <b class="conv-title">{{ source.label }}</b>
          <span class="meta"><time :datetime="source.created_at" :data-tip="absoluteTime(source.created_at)">{{ relativeTime(source.created_at, { now }) }}</time> · {{ turnsOf(source).length }} turns</span>
          <button type="button" class="btn sm ghost" :aria-expanded="open.has(source.id)" @click="toggle(source.id)">{{ open.has(source.id) ? 'Hide transcript' : 'Transcript' }}</button>
        </header>
        <div v-if="open.has(source.id)" class="transcript">
          <label class="search-field"><AppIcon name="search" :size="13" /><input v-model="term[source.id]" class="field" type="search" placeholder="Search the transcript" :aria-label="`Search the transcript of ${source.label}`" /></label>
          <p class="t-count">{{ shownTurns(source).length }} of {{ turnsOf(source).length }}</p>
          <ol class="turns">
            <li v-for="turn in shownTurns(source)" :id="`turn-${turn.id}`" :key="turn.id" :class="turn.speaker">
              <span class="mono t-no">{{ String(turn.ordinal).padStart(2, '0') }}</span>
              <span class="who">{{ turn.speaker === 'agent' ? 'Aithema' : 'Person' }}</span>
              <span class="body">{{ turn.body }}</span>
            </li>
          </ol>
          <p v-if="!turnsOf(source).length" class="j-note">No turns recorded yet.</p>
        </div>
      </article>
      <ul v-if="others.length" class="j-rows src-rows" aria-label="Files, links and notes">
        <li v-for="source in others" :id="`source-${source.id}`" :key="source.id">
          <AppIcon :name="ICON[source.kind]" :size="13" class="faint" />
          <a v-if="external(source)" v-clip-tip="source.label" class="grow clip-name" :href="source.locator" target="_blank" rel="noopener noreferrer">{{ source.label }}</a>
          <span v-else v-clip-tip="source.label" class="grow clip-name">{{ source.label }}</span>
          <span class="j-chip">{{ source.kind === 'url' ? 'link' : source.kind }}</span>
          <time class="mono" :datetime="source.created_at" :data-tip="absoluteTime(source.created_at)">{{ relativeTime(source.created_at, { now }) }}</time>
        </li>
      </ul>
    </template>
  </section>
</template>

<style scoped>
.src-skel { height: 64px; border-radius: 10px; }
.conv { display: grid; gap: 8px; padding: 6px 0 10px; border-bottom: 1px solid var(--line); border-radius: 8px; }
.conv-head { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; min-width: 0; }
.src-icon { display: grid; place-items: center; width: 28px; height: 28px; border-radius: 50%; background: var(--row-selected); color: var(--teal-ink); flex-shrink: 0; }
.conv-title { font-weight: 600; font-size: 14.5px; }
.meta { flex: 1; font-size: 12.5px; color: var(--ink-2); }
.transcript { display: grid; gap: 8px; }
.transcript .search-field { max-width: 320px; }
.transcript .search-field .field { height: 32px; padding-left: 30px; }
.transcript .search-field svg { position: absolute; left: 10px; color: var(--ink-3); }
.t-count { font-size: 12px; color: var(--ink-3); }
.turns { display: grid; gap: 2px; max-height: 360px; margin: 0; padding: 6px; overflow: auto; list-style: none; border-radius: 10px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.turns li { display: grid; grid-template-columns: 30px 64px minmax(0, 1fr); gap: 8px; padding: 6px; border-radius: 8px; font-size: 13.5px; }
.turns li.agent .body { color: var(--ink-2); }
.t-no { font-size: 11px; color: var(--ink-3); }
.who { font-size: 12px; font-weight: 600; color: var(--ink-2); }
.body { white-space: pre-wrap; overflow-wrap: anywhere; }
.faint { color: var(--ink-3); flex-shrink: 0; }
.src-rows a { color: var(--teal-ink); }
@media (max-width: 720px) { .turns li { grid-template-columns: 26px minmax(0, 1fr); } .who { grid-column: 2; } .body { grid-column: 1 / -1; } }
</style>
