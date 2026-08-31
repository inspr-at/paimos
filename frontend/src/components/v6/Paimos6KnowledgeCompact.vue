<script setup lang="ts">
import { AlertTriangle, BookOpen, MessageSquareText } from 'lucide-vue-next'

import type { StructuredKnowledgeSnapshot } from '@/v6/structuredKnowledge'

defineProps<{
  state: 'loading' | 'ready' | 'empty' | 'unavailable'
  snapshot: StructuredKnowledgeSnapshot | null
}>()
</script>

<template>
  <section class="p6-compact" aria-labelledby="p6-compact-title">
    <header>
      <div>
        <p><BookOpen :size="13" aria-hidden="true" /> Knowledge Compact</p>
        <h2 id="p6-compact-title">Small facts, explicit provenance</h2>
      </div>
      <span v-if="snapshot?.compact_product_session_id" class="p6-compact-session">
        Product session · {{ snapshot.compact_product_session_id.slice(0, 8) }}
      </span>
      <span v-else class="p6-compact-session is-unbound">No Compact session bound</span>
    </header>

    <div v-if="state === 'loading'" class="p6-compact-state" role="status">Loading authorized compact knowledge…</div>
    <div v-else-if="state === 'unavailable'" class="p6-compact-state is-unavailable" role="alert">
      Compact knowledge unavailable. No previous project data is retained.
    </div>
    <div v-else-if="state === 'empty'" class="p6-compact-state">
      No structured entries, legacy knowledge, or remember proposals in this project.
    </div>
    <div v-else-if="snapshot" class="p6-compact-grid">
      <article v-for="entry in snapshot.entries.slice(0, 6)" :key="`entry:${entry.knowledge_id}`" class="p6-fact">
        <div class="p6-fact-meta">
          <span>{{ entry.type.replace('_', ' ') }}</span>
          <span>{{ entry.validation.body_bytes }}/{{ snapshot.short_body_limit_bytes }} bytes max</span>
        </div>
        <h3>{{ entry.title }}</h3>
        <p class="p6-purpose">{{ entry.purpose }}</p>
        <p class="p6-body">{{ entry.short_body }}</p>
        <div v-if="entry.links.length" class="p6-links" aria-label="Canonical links">
          <span v-for="link in entry.links.slice(0, 4)" :key="link.link_id">
            {{ link.relation.replace('_', ' ') }} · {{ link.target_title }}
          </span>
        </div>
      </article>

      <article v-for="proposal in snapshot.proposals.filter((row) => row.state === 'proposed').slice(0, 3)" :key="`proposal:${proposal.proposal_id}`" class="p6-fact is-proposal">
        <div class="p6-fact-meta"><span><MessageSquareText :size="11" /> remember proposal</span><span>Review required</span></div>
        <h3>{{ proposal.title }}</h3>
        <p class="p6-purpose">{{ proposal.purpose }}</p>
        <div class="p6-flags">
          <span v-for="flag in proposal.validation.flags" :key="flag">{{ flag.replace(/_/g, ' ') }}</span>
          <span v-if="proposal.validation.flags.length === 0">compact candidate</span>
        </div>
      </article>

      <article v-if="snapshot.legacy.length" class="p6-fact is-legacy">
        <div class="p6-fact-meta"><span><AlertTriangle :size="11" /> legacy knowledge</span><span>{{ snapshot.legacy.length }} rows</span></div>
        <h3>Explicitly unstructured</h3>
        <p class="p6-purpose">Legacy bodies remain in the 5.x knowledge editor. They are not truncated, relabelled, or silently promoted into Compact.</p>
        <ul>
          <li v-for="entry in snapshot.legacy.slice(0, 4)" :key="entry.knowledge_id">
            {{ entry.title }} · {{ entry.body_bytes }} bytes
          </li>
        </ul>
      </article>
    </div>
  </section>
</template>

<style scoped>
.p6-compact { margin-top: 28px; padding: 20px; border: 1px solid #d6e0da; border-radius: 18px; background: rgba(248, 251, 248, 0.78); }
.p6-compact > header { display: flex; align-items: end; justify-content: space-between; gap: 18px; }
.p6-compact > header p { display: flex; align-items: center; gap: 6px; color: #5d7467; font-size: 10px; font-weight: 750; letter-spacing: .08em; text-transform: uppercase; }
.p6-compact > header h2 { margin-top: 4px; color: #31443a; font: 600 20px/1.2 "Bricolage Grotesque", sans-serif; letter-spacing: -.03em; }
.p6-compact-session { color: #4d6758; font: 600 9px/1.2 "JetBrains Mono", monospace; }
.p6-compact-session.is-unbound { color: #8a684f; }
.p6-compact-state { margin-top: 16px; padding: 22px; border: 1px dashed #cad6cf; border-radius: 12px; color: #59655e; font-size: 11px; text-align: center; }
.p6-compact-state.is-unavailable { border-color: #ddc3b8; color: #784d3b; background: #fff8f4; }
.p6-compact-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 11px; margin-top: 16px; }
.p6-fact { min-width: 0; padding: 14px; border: 1px solid #dce5df; border-radius: 13px; background: #fff; }
.p6-fact.is-proposal { border-style: dashed; background: #fffdf5; }
.p6-fact.is-legacy { background: #fff9f5; }
.p6-fact-meta { display: flex; align-items: center; justify-content: space-between; gap: 8px; color: #66766d; font-size: 8.5px; font-weight: 700; text-transform: uppercase; }
.p6-fact-meta span { display: inline-flex; align-items: center; gap: 4px; }
.p6-fact h3 { margin-top: 9px; color: #31443a; font-size: 13px; }
.p6-purpose { margin-top: 5px; color: #4e6157; font-size: 10px; font-weight: 650; line-height: 1.45; }
.p6-body { margin-top: 7px; color: #65736b; font-size: 10px; line-height: 1.55; }
.p6-links, .p6-flags { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 10px; }
.p6-links span, .p6-flags span { padding: 3px 5px; border-radius: 999px; color: #4f6658; background: #edf4ef; font-size: 8px; }
.p6-fact ul { margin: 8px 0 0 14px; color: #6a665f; font-size: 9px; line-height: 1.55; }
@media (max-width: 940px) { .p6-compact-grid { grid-template-columns: 1fr; } }
@media (max-width: 680px) {
  .p6-compact { padding: 15px; }
  .p6-compact > header { align-items: flex-start; flex-direction: column; }
}
</style>
