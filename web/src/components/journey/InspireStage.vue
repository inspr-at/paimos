<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { acceptDraft, ACTION_LONG, isImported, STAGE_LABEL, type IntakeDraft } from '../../lib/journey'
import { can } from '../../lib/authz'
import { useJourneyContext } from '../../lib/journeyContext'
import { toast } from '../../lib/toast'
import { absoluteTime, plural, relativeTime } from '../../lib/work'
import { useJourney } from '../../stores/journey'
import AppIcon from '../AppIcon.vue'
import MarkdownBody from '../MarkdownBody.vue'
import GateCard from './GateCard.vue'
import HistoryFold from './HistoryFold.vue'
import ImportedOrigin from './ImportedOrigin.vue'
import SourcesCard from './SourcesCard.vue'
import ExtensionData from './ExtensionData.vue'

// Inspire: what Aithema recorded (conversation and sources) and the drafts it
// proposes from them, each citing its sources. Accepting the brief draft turns
// the next action into "Confirm brief".
const ctx = useJourneyContext()
const store = useJourney()
const sources = ref<InstanceType<typeof SourcesCard>>()
const intake = computed(() => ctx.data.intake.value.value)
const drafts = computed(() => [...intake.value.drafts].sort((a, b) => a.kind === b.kind ? 0 : a.kind === 'brief' ? -1 : 1))
const here = computed(() => ctx.journey.value.stage === 'inspire')
const next = computed(() => ctx.journey.value.next_action)
const accepting = ref<string | null>(null)
const sourceLabel = (id: string) => intake.value.sources.find(s => s.id === id)?.label ?? 'source'
// Once the journey is past Inspire, this stage is history: one line, folded.
const recorded = computed(() => intake.value.sources.length > 0 || intake.value.drafts.length > 0)
const summary = computed(() => {
  const sources = intake.value.sources.length, accepted = intake.value.drafts.filter(d => d.status === 'accepted').length
  if (recorded.value) return `${plural(sources, 'source')} recorded · ${plural(accepted, 'draft')} accepted.`
  return 'No conversation or sources were recorded.'
})
// An imported project started in Paimos: what it brought stands where the conversation would.
const imported = computed(() => isImported(ctx.journey.value) && !here.value)
async function accept(draft: IntakeDraft) {
  accepting.value = draft.id
  try {
    await acceptDraft(ctx.project.value.id, draft)
    toast(draft.kind === 'brief' ? 'Brief accepted. Confirm it to move on to Shape.' : `Requirement accepted: ${draft.title}`)
    await Promise.all([ctx.data.loadIntake(true), store.load(ctx.project.value.id, true)])
  } catch (e) { toast(`The draft was not accepted: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' }) } finally { accepting.value = null }
}
</script>

<template>
  <div v-if="imported" class="j-grid">
    <div class="j-col">
      <ImportedOrigin />
      <HistoryFold v-if="recorded" title="Conversation and sources" :summary="summary" label="Show the sources">
        <SourcesCard ref="sources" :intake="intake" :status="ctx.data.intake.status.value" :error="ctx.data.intake.error.value" :now="ctx.now.value" @retry="ctx.data.loadIntake(true)" />
      </HistoryFold>
    </div>
    <div class="j-col">
      <GateCard eyebrow="Imported" title="Started in Paimos" tone="record">
        <p>This project came with its history, so there is no conversation here. Its description stands in for the brief, and its epics and tickets for the agreed work.</p>
        <button type="button" class="btn sm go" @click="ctx.view(ctx.journey.value.stage)">Go to {{ STAGE_LABEL[ctx.journey.value.stage] }}<AppIcon name="arrow" :size="13" /></button>
      </GateCard>
    </div>
  </div>
  <component :is="here ? 'div' : HistoryFold" v-else v-bind="here ? {} : { title: 'Conversation and sources', summary, label: 'Show the sources' }">
  <div class="j-grid">
    <div class="j-col">
      <SourcesCard ref="sources" :intake="intake" :status="ctx.data.intake.status.value" :error="ctx.data.intake.error.value" :now="ctx.now.value" @retry="ctx.data.loadIntake(true)" />
    </div>
    <div class="j-col">
      <GateCard
        v-if="here" :eyebrow="next.key === 'confirm_brief' ? 'Decision' : 'Next'" :title="next.key === 'confirm_brief' ? 'Confirm the brief' : 'The brief'"
        :action="next.key === 'confirm_brief' ? { label: ctx.next.value.label, disabled: ctx.next.value.disabled, busy: ctx.next.value.busy, tip: ctx.next.value.tip } : null" @act="ctx.runNext()"
      >
        <p>{{ ACTION_LONG[next.key] }}</p>
        <p v-if="next.key === 'continue_intake'" class="j-note">Aithema proposes the brief as a draft below; accepting it here turns the next step into “Confirm brief”.</p>
      </GateCard>
      <GateCard v-else eyebrow="Done" title="Brief confirmed" tone="record">
        <p>The brief is confirmed. <button type="button" class="linkish" @click="ctx.view('shape')">Shape <AppIcon name="arrow" :size="12" /></button></p>
      </GateCard>
      <section class="j-card" aria-labelledby="drafts-title">
        <header class="j-card-head"><p id="drafts-title" class="eyebrow">Drafts · from Aithema</p><span v-if="drafts.length" class="j-count">{{ drafts.length }}</span></header>
        <div v-if="!drafts.length" class="j-empty">
          <strong>No drafts yet</strong>
          <span>Aithema proposes the brief and the requirements from the conversation, each with the sources it drew on. They appear here for you to accept.</span>
        </div>
        <article v-for="draft in drafts" :key="draft.id" class="draft" :class="draft.status">
          <header class="draft-head">
            <span class="j-chip" :class="draft.kind === 'brief' ? 'teal' : ''">{{ draft.kind === 'brief' ? 'Brief' : draft.requirement_kind === 'nonfunctional' ? 'Non-functional' : 'Requirement' }}</span>
            <b class="draft-title">{{ draft.title }}</b>
            <span class="j-chip" :class="draft.status === 'accepted' ? 'ok' : draft.status === 'rejected' ? 'bad' : 'gold'">{{ draft.status }}</span>
          </header>
          <MarkdownBody v-if="draft.body" class="draft-body" :body="draft.body" />
          <ExtensionData :extensions="draft.extensions" />
          <p v-if="draft.citations.length" class="cites">
            <button v-for="(cite, i) in draft.citations" :key="i" type="button" class="j-cite" @click="sources?.reveal(cite.source_id, cite.turn_id)">{{ sourceLabel(cite.source_id) }}{{ cite.locator ? ` · ${cite.locator}` : '' }}</button>
          </p>
          <footer class="draft-foot">
            <time :datetime="draft.proposed_at" :data-tip="absoluteTime(draft.proposed_at)">Proposed {{ relativeTime(draft.proposed_at, { now: ctx.now.value }) }}</time>
            <button v-if="draft.status === 'proposed' && ctx.canAct.value && can('intake.decide', ctx.project.value.id)" type="button" class="btn sm primary" :disabled="accepting === draft.id" @click="accept(draft)"><AppIcon name="check" :size="13" />{{ accepting === draft.id ? 'Accepting…' : 'Accept draft' }}</button>
          </footer>
        </article>
      </section>
    </div>
  </div>
  </component>
</template>

<style scoped>
.draft { display: grid; gap: 6px; padding: 10px 0; border-top: 1px solid var(--line); }
.draft-head { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.draft-title { flex: 1; min-width: 0; font-weight: 600; font-size: 14px; }
.draft-body { font-size: 13.5px; max-height: 220px; overflow: auto; }
.draft.rejected { opacity: .7; }
.cites { display: flex; flex-wrap: wrap; gap: 6px; }
.draft-foot { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-size: 12px; color: var(--ink-3); }
.go { justify-self: start; }
.linkish { display: inline-flex; align-items: center; gap: 4px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font-weight: 600; cursor: pointer; }
.linkish:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
</style>
