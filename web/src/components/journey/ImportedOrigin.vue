<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { isWorkItem, isWorkLeaf, isWorkParent } from '../../lib/workVocabulary'
import { useJourneyContext } from '../../lib/journeyContext'
import { isDropped, isFinished } from '../../lib/journey'
import { entryPath, typeMeta } from '../../lib/knowledge'
import { plural } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import MarkdownBody from '../MarkdownBody.vue'

// What an imported project brought with it, shown where a new project would show
// its conversation and brief: the project's own description, its knowledge and
// the size of its work. Real data only; nothing is summarised or invented.
// "brief": only the description, where Shape shows the brief.
const props = defineProps<{ eyebrow?: string; mode?: 'full' | 'brief' }>()
const ctx = useJourneyContext()
const origin = computed(() => ctx.data.origin.value.value)
const status = computed(() => ctx.data.origin.status.value)
const description = computed(() => {
  const node = origin.value.node
  if (!node) return ''
  const classic = node.fields?.classic
  const fromClassic = classic && typeof classic === 'object' ? (classic as Record<string, unknown>).description : undefined
  return node.body?.trim() || (typeof fromClassic === 'string' ? fromClassic.trim() : '')
})
const long = computed(() => description.value.length > 600 || description.value.split('\n').length > 8)
const open = ref(false)
const knowledge = computed(() => origin.value.knowledge.filter(k => k.status !== 'archived'))
const shownKnowledge = computed(() => knowledge.value.slice(0, 6))
const work = computed(() => ctx.data.work.value.value)
const epics = computed(() => work.value.filter(item => isWorkItem(item) && isWorkParent(item)).length)
const tickets = computed(() => work.value.filter(item => isWorkLeaf(item) && !isDropped(item.state)))
const done = computed(() => tickets.value.filter(item => isFinished(item.state)).length)
const routeKey = computed(() => ctx.project.value.routeKey)
</script>

<template>
  <section class="j-card origin" aria-labelledby="origin-title">
    <header class="j-card-head"><p id="origin-title" class="eyebrow">{{ props.eyebrow ?? 'Brought over · from Paimos' }}</p></header>
    <p v-if="status === 'error'" class="j-note" role="alert">{{ ctx.data.origin.error.value }}</p>
    <template v-else>
      <ul v-if="mode !== 'brief'" class="facts" aria-label="What the project brought">
        <li><b>{{ epics }}</b><span>{{ epics === 1 ? 'epic' : 'epics' }}</span></li>
        <li><b>{{ tickets.length }}</b><span>{{ tickets.length === 1 ? 'ticket' : 'tickets' }} · {{ done }} done</span></li>
        <li><b>{{ knowledge.length }}</b><span>{{ knowledge.length === 1 ? 'knowledge entry' : 'knowledge entries' }}</span></li>
      </ul>
      <div v-if="description" class="description" :class="{ clamped: long && !open }">
        <MarkdownBody :body="description" />
      </div>
      <p v-else class="j-note">The project has no description yet; it can be added on the project.</p>
      <button v-if="long" type="button" class="btn sm ghost toggle" :aria-expanded="open" @click="open = !open">
        {{ open ? 'Show less' : 'Show the whole description' }}<AppIcon name="chevron" :size="12" class="chev" :class="{ up: open }" />
      </button>
      <template v-if="knowledge.length && mode !== 'brief'">
        <p class="eyebrow sub">Knowledge · {{ knowledge.length }}</p>
        <ul class="j-rows">
          <li v-for="entry in shownKnowledge" :key="entry.id">
            <span class="kind">{{ typeMeta(entry.type).label }}</span>
            <RouterLink v-clip-tip="entry.title" class="grow title clip-name" :to="entryPath(routeKey, entry.type, entry.slug)">{{ entry.title }}</RouterLink>
          </li>
        </ul>
        <RouterLink class="more-link" :to="`/p/${encodeURIComponent(routeKey)}/knowledge`">
          {{ knowledge.length > shownKnowledge.length ? `All ${plural(knowledge.length, 'entry', 'entries')} in Knowledge` : 'Open Knowledge' }}<AppIcon name="arrow" :size="12" />
        </RouterLink>
      </template>
    </template>
  </section>
</template>

<style scoped>
.facts { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; margin: 0; padding: 0; list-style: none; }
.facts li { display: grid; gap: 2px; padding: 10px 12px; border-radius: 10px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); }
.facts b { font: 500 20px/1.2 var(--mono); color: var(--ink); font-variant-numeric: tabular-nums; }
.facts span { font-size: 12px; color: var(--ink-2); }
.description { position: relative; font-size: 13.5px; }
/* A long description reads as its opening, fading out, until it is asked for. */
.description.clamped { max-height: 11.5em; overflow: hidden; -webkit-mask-image: linear-gradient(#000 70%, transparent); mask-image: linear-gradient(#000 70%, transparent); }
.toggle { justify-self: start; }
.chev { transition: transform .15s ease; }
.chev.up { transform: rotate(180deg); }
.sub { margin-top: 4px; }
.origin .j-rows .grow { min-width: 0; }
.kind { flex-shrink: 0; width: 92px; font: 500 10.5px/1.4 var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-3); }
.title { color: var(--ink); text-decoration: none; }
.title:hover { color: var(--teal-ink); text-decoration: underline; }
.title:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
.more-link { display: inline-flex; align-items: center; gap: 5px; justify-self: start; font-size: 12.5px; font-weight: 600; color: var(--teal-ink); text-decoration: none; }
.more-link:hover { text-decoration: underline; }
.more-link:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
@media (prefers-reduced-motion: reduce) { .chev { transition: none; } }
@media (max-width: 600px) {
  .facts { grid-template-columns: minmax(0, 1fr); }
  .facts li { grid-template-columns: auto minmax(0, 1fr); align-items: baseline; gap: 8px; }
  .kind { width: 76px; }
}
</style>
