<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref, watch } from 'vue'
import { readSessionProvenance } from '../../lib/agentRows'
import { absoluteTime, relativeTime } from '../../lib/work'

// Instruction versions recorded for one session: logical names, digests and
// version identifiers. The payload has no file contents or local paths.
const props = defineProps<{ projectId: string; sessionId: string; now?: number }>()

interface ProvenanceItem {
  kind: 'agents' | 'claude' | 'skill' | 'prompt_template' | 'rules_merged' | 'rules_set'
  logical_name: string
  hash_kind: 'content' | 'absent'
  content_sha256?: string | null
  version?: string | null
}
interface ProvenanceRevision {
  id: string
  revision: number
  recorded_at: string
  items: ProvenanceItem[]
}
interface ProvenancePage {
  revisions: ProvenanceRevision[]
  truncated: boolean
}

const state = ref<'loading' | 'ready' | 'error'>('loading')
const page = ref<ProvenancePage>({ revisions: [], truncated: false })
const labels: Record<ProvenanceItem['kind'], string> = {
  agents: 'Agents',
  claude: 'Claude',
  skill: 'Skill',
  prompt_template: 'Prompt template',
  rules_merged: 'Rules',
  rules_set: 'Rule set',
}

function itemName(item: ProvenanceItem) {
  if (item.kind === 'rules_merged') return 'Merged rules'
  if (item.kind === 'rules_set') return 'Published set'
  return item.logical_name
}

function itemTip(item: ProvenanceItem) {
  return item.kind === 'rules_set' ? item.logical_name : undefined
}

watch(() => [props.projectId, props.sessionId], async () => {
  const projectId = props.projectId
  const sessionId = props.sessionId
  state.value = 'loading'
  page.value = { revisions: [], truncated: false }
  try {
    const response = await readSessionProvenance(projectId, sessionId)
    if (!response.ok) throw new Error('unavailable')
    const body = await response.json() as ProvenancePage
    if (props.projectId !== projectId || props.sessionId !== sessionId) return
    page.value = { revisions: body.revisions ?? [], truncated: body.truncated === true }
    state.value = 'ready'
  } catch {
    if (props.projectId === projectId && props.sessionId === sessionId) state.value = 'error'
  }
}, { immediate: true })

function shortHash(value: string) {
  return value.length > 12 ? value.slice(0, 12) : value
}
</script>

<template>
  <!-- Nothing recorded means nothing shown: the section appears only with versions or an error. -->
  <section v-if="state === 'error' || page.revisions.length" class="provenance" aria-labelledby="provenance-title">
    <h3 id="provenance-title" class="head eyebrow">Instructions</h3>
    <p v-if="state === 'error'" class="line" role="alert">Instruction versions could not be loaded.</p>
    <ol v-else class="revisions">
      <li v-for="rev in page.revisions" :key="rev.id" class="revision">
        <p class="rev-meta"><span>Revision {{ rev.revision }}</span><time :datetime="rev.recorded_at" :data-tip="absoluteTime(rev.recorded_at)">{{ relativeTime(rev.recorded_at, { now }) }}</time></p>
        <ul class="items">
          <li v-for="item in rev.items" :key="`${rev.id}-${item.logical_name}`">
            <span class="kind">{{ labels[item.kind] || item.kind }}</span>
            <span class="name" :data-tip="itemTip(item)">{{ itemName(item) }}</span>
            <code v-if="item.hash_kind === 'content' && item.content_sha256" class="hash" :data-tip="item.content_sha256">{{ shortHash(item.content_sha256) }}</code>
            <span v-else class="absent">No content digest</span>
            <span v-if="item.version" class="version">{{ item.version }}</span>
          </li>
        </ul>
      </li>
    </ol>
    <p v-if="state === 'ready' && page.truncated" class="line">Older revisions are kept and are not all shown here.</p>
  </section>
</template>

<style scoped>
.provenance { margin-top: 24px; min-width: 0; max-width: 100%; }
.head { display: flex; align-items: center; gap: 8px; margin: 0 0 10px; }
.line { margin: 0; font-size: 13px; color: var(--ink-3); }
.revisions, .items { margin: 0; padding: 0; list-style: none; }
.revision { padding: 10px 0; border-top: 1px solid var(--line); }
.revision:first-child { border-top: 0; padding-top: 0; }
.rev-meta { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; margin: 0 0 8px; font-size: 12px; color: var(--ink-2); }
.rev-meta time { color: var(--ink-3); font-variant-numeric: tabular-nums; }
.items { display: grid; gap: 6px; min-width: 0; }
.items li { display: flex; align-items: baseline; flex-wrap: wrap; gap: 8px; min-width: 0; max-width: 100%; }
.items li > * { min-width: 0; max-width: 100%; }
.kind { flex-shrink: 0; font: 500 10px/1.4 var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-3); }
.name { min-width: 0; color: var(--ink); font-size: 13px; overflow-wrap: anywhere; }
.hash { font: 12px var(--mono); color: var(--ink-2); font-variant-ligatures: none; overflow-wrap: anywhere; }
.absent, .version { color: var(--ink-2); font-size: 12px; overflow-wrap: anywhere; }
@media (max-width: 720px) {
  .absent, .version { flex-basis: 100%; }
}
</style>
