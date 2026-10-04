<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { isWorkItem, isWorkParent } from '../../lib/workVocabulary'
import { isJourneyLeaf, parentLeafGroups, workParentId } from '../../lib/journeyWork'
import { ACTION_LONG, addRequirement, gateApprovals, isDropped, isFinished, isImported, offeredApproval, STAGE_LABEL, type Requirement } from '../../lib/journey'
import { useJourneyContext } from '../../lib/journeyContext'
import { can } from '../../lib/authz'
import { toast } from '../../lib/toast'
import { plural, statusMeta } from '../../lib/work'
import type { ListItem } from '../../lib/api'
import { StaleJourney, useJourney } from '../../stores/journey'
import AppIcon from '../AppIcon.vue'
import GateApprovals from './GateApprovals.vue'
import GateCard from './GateCard.vue'
import LaterCard from './LaterCard.vue'
import ExtensionData from './ExtensionData.vue'

// Requirements: functional ones become features (epics) with tickets, non-functional
// ones knowledge entries and acceptance criteria. Agreeing them needs the
// requirements gate for exactly this revision.
const ctx = useJourneyContext()
const store = useJourney()
const journey = computed(() => ctx.journey.value)
const all = computed(() => ctx.data.requirements.value.value)
const extensionsFor = (nodeId: string) => ctx.data.intake.value.value.drafts.find(d => d.status === 'accepted' && d.target_node_id === nodeId)?.extensions
const functional = computed(() => all.value.filter(r => r.kind === 'functional'))
const nonfunctional = computed(() => all.value.filter(r => r.kind === 'nonfunctional'))
const drafts = computed(() => all.value.filter(r => r.status === 'draft').length)
const here = computed(() => journey.value.next_action.key === 'approve_requirements')
const approvals = computed(() => gateApprovals(ctx.approvals.value, 'requirements', journey.value.project_node_id))
const approval = computed(() => offeredApproval(ctx.approvals.value, journey.value, 'requirements', ctx.now.value))
const epicKey = (r: Requirement) => r.feature_node_id ? ctx.data.workById.value.get(r.feature_node_id)?.key ?? null : null
const tickets = computed(() => all.value.reduce((sum, r) => sum + r.generated_ticket_ids.length, 0))
// An imported project brought its features as epics: they stand for what was agreed
// until requirements are agreed here. Each shows how far its tickets are.
const imported = computed(() => isImported(journey.value) && !all.value.length)
const work = computed(() => ctx.data.work.value.value)
const keyOrder = (a: ListItem, b: ListItem) => a.key.localeCompare(b.key, undefined, { numeric: true })
const epicOf = workParentId
const importedEpics = computed(() => {
  const groups = parentLeafGroups(work.value)
  return work.value.filter(i => isWorkItem(i) && isWorkParent(i)).sort(keyOrder).map(epic => {
    const mine = (groups.get(epic.id) ?? []).filter(i => !isDropped(i.state))
    const done = mine.filter(t => isFinished(t.state)).length
    return { epic, total: mine.length, done, segments: Array.from({ length: Math.min(12, Math.max(1, mine.length)) }, (_, i) => i < Math.round(done / Math.max(1, mine.length) * Math.min(12, Math.max(1, mine.length)))) }
  })
})
const loose = computed(() => work.value.filter(i => isJourneyLeaf(i) && !isDropped(i.state) && !epicOf(i)).length)
const epicHref = (key: string) => `/p/${encodeURIComponent(ctx.project.value.routeKey)}/${encodeURIComponent(key)}`

// Adding a requirement (a draft until the next agreement).
const kind = ref<Requirement['kind']>('functional')
const title = ref('')
const adding = ref(false)
async function add() {
  const text = title.value.trim()
  if (!text || adding.value) return
  adding.value = true
  try {
    await addRequirement(ctx.project.value.id, { kind: kind.value, title: text, body: '', expected_revision: journey.value.revision, idempotency_key: crypto.randomUUID() })
    title.value = ''
    toast(`Added: ${text}. It is agreed with the next revision.`)
    await Promise.all([ctx.data.loadRequirements(true), store.load(ctx.project.value.id, true)])
  } catch (e) {
    toast(e instanceof Error ? `The requirement was not added: ${e.message}` : 'The requirement was not added.', { tone: 'error' })
    if (!(e instanceof StaleJourney)) void store.load(ctx.project.value.id, true)
  } finally { adding.value = false }
}
</script>

<template>
  <div class="j-grid">
    <div class="j-col">
      <section v-if="imported" class="j-card" aria-labelledby="req-imported">
        <header class="j-card-head"><p id="req-imported" class="eyebrow">Features · {{ importedEpics.length }} · imported epics</p><span class="j-count">{{ plural(work.filter(i => isJourneyLeaf(i) && !isDropped(i.state)).length, 'ticket') }}</span></header>
        <p v-if="!importedEpics.length" class="j-note">The project came without epics{{ loose ? `; its ${plural(loose, 'ticket')} stand on their own` : '' }}. Requirements agreed here become its first features.</p>
        <ol v-else class="reqs">
          <li v-for="(row, i) in importedEpics" :key="row.epic.id" class="req">
            <span class="rid mono">F{{ i + 1 }}</span>
            <div class="req-body">
              <p class="req-title">{{ row.epic.title }}</p>
              <p class="req-meta">
                <span class="j-segs mini" role="img" :aria-label="`${row.done} of ${row.total} tickets done`"><i v-for="(on, n) in row.segments" :key="n" :class="{ on }" /></span>
                {{ row.total ? `${row.done} of ${plural(row.total, 'ticket')} done` : 'No tickets yet' }}
                <RouterLink class="ekey mono" :to="epicHref(row.epic.key)">{{ row.epic.key }}<AppIcon name="arrow" :size="10" /></RouterLink>
              </p>
            </div>
            <span class="j-chip" :class="row.total && row.done === row.total ? 'ok' : ''">{{ statusMeta(row.epic.state).label }}</span>
          </li>
        </ol>
        <p v-if="importedEpics.length && loose" class="j-note">{{ plural(loose, 'ticket') }} {{ loose === 1 ? 'is' : 'are' }} not tied to an epic.</p>
      </section>
      <section v-for="group in [{ id: 'functional', title: 'Functional', note: 'become features with tickets', items: functional }, { id: 'nonfunctional', title: 'Non-functional', note: 'become knowledge and acceptance criteria', items: nonfunctional }].filter(g => !imported || g.items.length)" :key="group.id" class="j-card" :aria-labelledby="`req-${group.id}`">
        <header class="j-card-head"><p :id="`req-${group.id}`" class="eyebrow">{{ group.title }} · {{ group.items.length }} · {{ group.note }}</p></header>
        <p v-if="ctx.data.requirements.status.value === 'loading' && !all.length" class="skeleton req-skel" role="status" aria-label="Loading requirements" />
        <p v-else-if="!group.items.length" class="j-note">{{ group.id === 'functional' ? 'No functional requirements yet. Aithema drafts them from the conversation; you can add one below.' : 'None yet.' }}</p>
        <ol v-else class="reqs">
          <li v-for="(req, i) in group.items" :key="req.node_id" class="req">
            <span class="rid mono">{{ group.id === 'functional' ? 'F' : 'N' }}{{ i + 1 }}</span>
            <div class="req-body">
              <p class="req-title">{{ req.title }}</p>
              <ExtensionData :extensions="extensionsFor(req.node_id)" />
              <p class="req-meta">
                <template v-if="req.kind === 'functional'"><AppIcon name="arrow" :size="11" />{{ req.generated_ticket_ids.length ? plural(req.generated_ticket_ids.length, 'ticket') : 'No tickets yet · Aithema breaks this feature down when you ask' }}</template>
                <template v-else><AppIcon name="arrow" :size="11" />knowledge and acceptance criteria</template>
                <a v-if="epicKey(req)" class="ekey mono" :href="`/p/${encodeURIComponent(ctx.project.value.routeKey)}/${encodeURIComponent(epicKey(req)!)}`" target="_blank" rel="noopener">{{ epicKey(req) }}<AppIcon name="external" :size="10" /></a>
              </p>
            </div>
            <span class="j-chip" :class="req.status === 'agreed' ? 'ok' : req.status === 'draft' ? 'gold' : ''">{{ req.status === 'agreed' ? `rev ${req.revision}` : req.status }}</span>
          </li>
        </ol>
      </section>
      <form v-if="ctx.canAct.value && can('requirements.write', ctx.project.value.id)" class="j-card add" @submit.prevent="add">
        <p class="eyebrow">Add a requirement</p>
        <div class="add-row">
          <select v-model="kind" class="field kind" aria-label="Kind of requirement"><option value="functional">Functional</option><option value="nonfunctional">Non-functional</option></select>
          <input v-model="title" class="field" placeholder="What must it do, or how well?" aria-label="Requirement" maxlength="500" />
          <button type="submit" class="btn" :disabled="!title.trim() || adding">{{ adding ? 'Adding…' : 'Add' }}</button>
        </div>
      </form>
    </div>
    <div class="j-col">
      <GateCard
        v-if="here" eyebrow="Decision" title="Approve requirements"
        :action="{ label: ctx.next.value.label, disabled: ctx.next.value.disabled, busy: ctx.next.value.busy, tip: ctx.next.value.tip }" @act="ctx.runNext()"
      >
        <ul class="j-checks">
          <li><AppIcon name="check" :size="13" class="ok" /><span><b>{{ plural(functional.length, 'feature') }}</b> (epics) from the functional requirements</span></li>
          <li><AppIcon name="check" :size="13" class="ok" /><span><b>{{ plural(nonfunctional.length, 'knowledge entry', 'knowledge entries') }}</b> · acceptance criteria on the tickets</span></li>
          <li v-if="drafts"><AppIcon name="info" :size="13" class="info" /><span>{{ plural(drafts, 'draft') }} agreed with this revision</span></li>
          <li v-if="journey.next_action.reason && !journey.next_action.available"><AppIcon name="alert" :size="13" class="warn" /><span>{{ journey.next_action.reason }}</span></li>
        </ul>
        <GateApprovals gate="requirements" :approvals="approvals" on="the project" :can-decide="ctx.canAct.value" :now="ctx.now.value" :me="ctx.me.value" />
        <p v-if="!approval" class="j-note">An agent asks for the requirements gate for this revision; it appears here for you to approve.</p>
        <p class="j-note">{{ ACTION_LONG.approve_requirements }} Next: plan the release.</p>
      </GateCard>
      <GateCard v-else-if="all.some(r => r.status === 'agreed')" eyebrow="Agreed" :title="`Requirements · revision ${journey.requirements_revision}`" tone="record">
        <p>{{ plural(tickets, 'ticket') }} generated · {{ plural(nonfunctional.length, 'knowledge entry', 'knowledge entries') }}.</p>
        <p><button type="button" class="linkish" @click="ctx.view('plan')">Plan <AppIcon name="arrow" :size="12" /></button></p>
      </GateCard>
      <GateCard v-else-if="imported" eyebrow="Imported" title="Features came with the project" tone="record">
        <p>{{ importedEpics.length ? `The ${plural(importedEpics.length, 'epic')} above` : 'The tickets' }} came from Paimos and stand for what was agreed. A requirement added here is agreed with the next revision and becomes a feature beside them.</p>
        <button type="button" class="btn sm go" @click="ctx.view(journey.stage)">Go to {{ STAGE_LABEL[journey.stage] }}<AppIcon name="arrow" :size="13" /></button>
      </GateCard>
      <LaterCard v-else stage="requirements" />
    </div>
  </div>
</template>

<style scoped>
.req-skel { height: 48px; border-radius: 10px; }
.go { justify-self: start; }
.j-segs.mini { width: 72px; height: 5px; gap: 2px; }
.reqs { display: grid; margin: 0; padding: 0; list-style: none; }
.req { display: grid; grid-template-columns: 30px minmax(0, 1fr) auto; gap: 10px; align-items: start; padding: 9px 2px; border-top: 1px solid var(--line); }
.req:first-child { border-top: 0; }
.rid { display: inline-grid; place-items: center; height: 20px; border-radius: 6px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-size: 10.5px; }
.req-title { font-size: 14px; color: var(--ink); overflow-wrap: anywhere; }
.req-meta { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 8px; margin-top: 3px; font-size: 12.5px; color: var(--ink-2); }
.ekey { display: inline-flex; align-items: center; gap: 3px; font-size: 10.5px; color: var(--ink-3); text-decoration: none; }
.ekey:hover { color: var(--teal-ink); }
.add { gap: 8px; }
.add-row { display: flex; gap: 8px; }
.add-row .kind { width: 150px; flex-shrink: 0; }
.add-row .field { height: 36px; }
.linkish { display: inline-flex; align-items: center; gap: 4px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font-weight: 600; cursor: pointer; }
.linkish:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
@media (max-width: 720px) { .add-row { flex-wrap: wrap; } .add-row .kind { width: 100%; } }
</style>
