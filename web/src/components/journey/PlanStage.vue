<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { APIError, undoEvent } from '../../lib/api'
import { ACTION_LONG, gateApprovals, hours, offeredApproval, RELEASE_STATE_LABEL } from '../../lib/journey'
import { toast } from '../../lib/toast'
import { useJourneyContext } from '../../lib/journeyContext'
import { plural, statusMeta } from '../../lib/work'
import { useJourney } from '../../stores/journey'
import AppIcon from '../AppIcon.vue'
import GateApprovals from './GateApprovals.vue'
import BacklogCard from './BacklogCard.vue'
import GateCard from './GateCard.vue'
import LaterCard from './LaterCard.vue'
// Phones get the short prompt: the example would be cut at the field's edge.
const narrowQuery = window.matchMedia('(max-width: 600px)')
const narrow = ref(narrowQuery.matches)
const onNarrow = (event: MediaQueryListEvent) => { narrow.value = event.matches }
narrowQuery.addEventListener('change', onNarrow)
onBeforeUnmount(() => narrowQuery.removeEventListener('change', onNarrow))
import ReleaseList from './ReleaseList.vue'
import ReleaseTickets from './ReleaseTickets.vue'
import ExistingTicketPicker from './ExistingTicketPicker.vue'
import type { MembershipResult } from '../../lib/releaseMembership'

// Plan: the tickets of the release, grouped by feature. While the current
// release is planning, ticked tickets form it and unticked ones stay in the
// backlog; the walker shows them one by one with their screens. Other releases
// read the same way, without the boxes.
const ctx = useJourneyContext()
const store = useJourney()
const journey = computed(() => ctx.journey.value)
const walker = computed(() => ctx.data.walker.value.value)
const status = computed(() => ctx.data.walker.status.value)
const stats = computed(() => ctx.plan.stats.value)
const planning = computed(() => journey.value.next_action.key === 'start_build')
const opening = computed(() => journey.value.next_action.key === 'open_first_release')
const approvals = computed(() => gateApprovals(ctx.approvals.value, 'build', journey.value.current_release_id))
const approval = computed(() => offeredApproval(ctx.approvals.value, journey.value, 'build', ctx.now.value))
const releases = computed(() => ctx.data.releases.value)
const epicsWithout = computed(() => stats.value.emptyFeatures.map(g => g.feature!.key))
const reqRevision = computed(() => journey.value.requirements_revision)

// Adding a ticket while planning: it joins this release, under a feature or none.
const features = computed(() => ctx.plan.groups.value.filter(g => g.feature && !g.feature.derived).map(g => g.feature!))
const newTitle = ref('')
const newFeature = ref<string>('')
const adding = ref(false)
const picking = ref(false)
watch(ctx.data.planOwner, () => { newTitle.value = ''; newFeature.value = ''; adding.value = false; picking.value = false }, { flush: 'sync' })
const epics = computed(() => ctx.plan.groups.value.flatMap(group => group.feature ? [{ id: group.feature.id, key: group.feature.key, title: group.feature.title }] : []))
async function addTicket() {
  const title = newTitle.value.trim()
  const owner = ctx.data.capturePlanOwner()
  if (!title || adding.value || !owner || !ctx.editable.value) return
  const label = ctx.releaseLabel.value
  adding.value = true
  try {
    const before = new Set((walker.value?.tickets ?? []).map(t => t.ticket_node_id))
    const saved = await ctx.data.addTicket(owner, title, newFeature.value || null, true)
    if (!saved || !ctx.data.isPlanOwnerCurrent(owner)) return
    const added = saved.tickets.find(t => !before.has(t.ticket_node_id))
    newTitle.value = ''
    toast(`${added?.key ?? 'The ticket'} joins ${label.toLowerCase()}.`)
    void store.load(owner.projectId, true)
  } catch (e) {
    if (!ctx.data.isPlanOwnerCurrent(owner)) return
    const missing = e instanceof APIError && (e.status === 404 || e.status === 405)
    toast(missing ? 'This server cannot add tickets to a plan yet.' : `The ticket was not added: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' })
  } finally { if (ctx.data.isPlanOwnerCurrent(owner)) adding.value = false }
}
async function addedExisting(payload: { count: number | null; result: MembershipResult }) {
  if (!ctx.data.patchWalker(payload.result.walker)) return
  picking.value = false
  void ctx.data.loadWork(true)
  void store.load(ctx.project.value.id, true)
  const label = ctx.releaseLabel.value
  toast(`Added ${payload.count === null ? 'selected work' : plural(payload.count, 'leaf', 'leaves')} to ${label.toLowerCase()}.`, {
    timeout: 8000,
    action: payload.result.event_id ? { label: 'Undo', run: () => void undoAdded(payload.result.event_id) } : undefined,
  })
}
async function undoAdded(eventId: number) {
  try {
    await undoEvent(eventId)
    if (ctx.release.value) await ctx.data.loadWalker(ctx.release.value.id, true)
    toast('Undone: those leaves left the release.')
  } catch (e) {
    toast(e instanceof APIError && e.status === 409 ? 'The release changed since, so nothing was undone.' : `Undo did not work: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' })
  }
}
</script>

<template>
  <div class="j-grid">
    <div class="j-col">
      <!-- No release shown: the journey has none open yet. What it will be chosen from is the backlog. -->
      <BacklogCard v-if="!ctx.release.value" />
      <section v-else class="j-card" aria-labelledby="plan-tickets">
        <header class="j-card-head">
          <p id="plan-tickets" class="eyebrow">{{ ctx.editable.value ? 'Tickets · ticked ones form the release' : `Tickets · ${ctx.releaseLabel.value}` }}</p>
          <span v-if="walker" class="j-count">{{ ctx.editable.value ? `${stats.inRelease} in release · ${stats.backlog} in backlog` : plural(stats.inRelease, 'ticket') }}</span>
          <button type="button" class="btn sm" :disabled="!walker?.tickets.length" data-tip="Walk through the tickets with their screens · w" aria-keyshortcuts="w" @click="ctx.walk()"><AppIcon name="expand" :size="13" />Full screen</button>
        </header>
        <p v-if="status === 'loading'" class="skeleton list-skel" role="status" aria-label="Loading the release" />
        <div v-else-if="status === 'error'" class="j-empty" role="alert"><strong>This release could not be loaded</strong><span>{{ ctx.data.walker.error.value }}</span>
          <button type="button" class="btn sm" @click="ctx.data.loadWalker(ctx.release.value!.id, true)"><AppIcon name="refresh" :size="13" />Try again</button>
        </div>
        <div v-else-if="walker && !walker.tickets.length" class="j-empty"><strong>No tickets yet</strong><span>Agreeing the requirements generates the tickets{{ ctx.editable.value ? "; you can also add one below" : "" }}.</span></div>
        <p v-if="ctx.editable.value && walker" class="benefit-hint">Add English and German pills and benefits in ticket details before Done.</p>
        <form v-if="ctx.editable.value && walker" class="add-row" @submit.prevent="addTicket">
          <input v-model="newTitle" class="field" :placeholder="narrow ? 'Add a ticket' : 'Add a ticket, e.g. Show opening hours on the order form'" aria-label="New ticket for this release" maxlength="500" />
          <select v-if="features.length" v-model="newFeature" class="field feature" aria-label="Feature of the new ticket">
            <option value="">No feature</option>
            <option v-for="f in features" :key="f.id" :value="f.id">{{ f.title }}</option>
          </select>
          <button type="button" class="btn" @click="picking = true"><AppIcon name="search" :size="14" />Add existing</button>
          <button type="submit" class="btn primary" :disabled="!newTitle.trim() || adding">{{ adding ? 'Adding…' : 'Add' }}</button>
        </form>
        <ExistingTicketPicker v-if="picking && ctx.release.value" :project-id="ctx.project.value.id" :release-id="ctx.release.value.id" :release-title="ctx.releaseLabel.value" :epics="epics" @close="picking = false" @added="addedExisting" />
        <ReleaseTickets v-if="walker && walker.tickets.length && status !== 'loading' && status !== 'error'" :plan="ctx.plan" :editable="ctx.editable.value" :project-key="ctx.project.value.routeKey" :work-by-id="ctx.data.workById.value" @walk="t => ctx.walk(t.key)" @open="ctx.open" />
      </section>
      <section v-if="releases.length" class="j-card" aria-labelledby="plan-releases">
        <header class="j-card-head"><p id="plan-releases" class="eyebrow">Releases · {{ releases.length }}</p></header>
        <ReleaseList :releases="releases" :current-id="journey.current_release_id" :selected-id="ctx.release.value?.id ?? null" :now="ctx.now.value" :limit="6" @select="r => ctx.selectRelease(r.key)" />
      </section>
    </div>
    <div v-if="!journey.current_release_id || status === 'ready' || status === 'error'" class="j-col">
      <GateCard
        v-if="opening" eyebrow="Decision" title="Open release 1"
        :action="{ label: ctx.next.value.label, disabled: ctx.next.value.disabled, busy: ctx.next.value.busy, tip: ctx.next.value.tip }" @act="ctx.runNext()"
      >
        <p>{{ ACTION_LONG.open_first_release }}</p>
      </GateCard>
      <GateCard
        v-else-if="planning && ctx.current.value" eyebrow="Decision" :title="ctx.releaseLabel.value"
        :action="{ label: ctx.next.value.label, disabled: ctx.next.value.disabled, busy: ctx.next.value.busy, tip: ctx.next.value.tip }" @act="ctx.runNext()"
      >
        <div class="j-stats">
          <div class="j-stat"><b>{{ stats.inRelease }}</b><span>{{ stats.inRelease === 1 ? 'ticket' : 'tickets' }}</span></div>
          <div class="j-stat" :class="{ warn: stats.unestimated }"><b>{{ stats.hours ? hours(stats.hours) : '—' }}</b><span>{{ stats.unestimated ? `${stats.unestimated} not estimated` : 'estimate' }}</span></div>
          <div class="j-stat"><b>{{ stats.backlog }}</b><span>in the backlog</span></div>
        </div>
        <ul class="j-checks">
          <li v-if="epicsWithout.length"><AppIcon name="alert" :size="13" class="warn" /><span>{{ epicsWithout.join(', ') }} {{ epicsWithout.length === 1 ? 'has' : 'have' }} no ticket in this release</span></li>
          <li v-else-if="walker?.features.length"><AppIcon name="check" :size="13" class="ok" /><span>Every feature has a ticket in this release</span></li>
          <li v-if="stats.unestimated"><AppIcon name="alert" :size="13" class="warn" /><span>{{ plural(stats.unestimated, 'ticket') }} in the release {{ stats.unestimated === 1 ? 'has' : 'have' }} no estimate</span></li>
          <li><AppIcon name="info" :size="13" class="info" /><span>Starting the build agrees requirements revision {{ reqRevision }}</span></li>
          <li v-if="!journey.next_action.available && journey.next_action.reason && !/gate/i.test(journey.next_action.reason)"><AppIcon name="alert" :size="13" class="warn" /><span>{{ journey.next_action.reason }}</span></li>
        </ul>
        <GateApprovals gate="build" :approvals="approvals" :on="ctx.releaseLabel.value" :can-decide="ctx.canAct.value" :now="ctx.now.value" :me="ctx.me.value" />
        <p v-if="!approval" class="j-note">An agent asks for the build gate on {{ ctx.releaseLabel.value }}; it appears here for you to approve.</p>
      </GateCard>
      <GateCard v-else-if="planning" eyebrow="Decision" title="Start build" tone="blocked">
        <p>{{ journey.next_action.reason || 'No release is open.' }}</p>
        <p class="j-note">The build starts from the current release. Until one is open, earlier releases can be read but not changed.</p>
      </GateCard>
      <GateCard v-if="ctx.release.value && !(planning && ctx.current.value)" :eyebrow="ctx.current.value ? 'Planned' : 'Release'" :title="ctx.releaseLabel.value" tone="record">
        <dl class="j-kv">
          <dt>Version</dt><dd :class="{ faint: !ctx.release.value.version }">{{ ctx.release.value.version ?? 'Set when it goes live' }}</dd>
          <dt>Key</dt><dd class="mono">{{ ctx.release.value.key }}</dd>
          <dt>State</dt><dd>{{ walker && ctx.current.value ? RELEASE_STATE_LABEL[walker.state] : statusMeta(ctx.release.value.state).label }}</dd>
          <dt>Tickets</dt><dd>{{ walker ? plural(stats.inRelease, 'ticket') : '…' }}</dd>
        </dl>
        <p v-if="!ctx.current.value" class="j-note">An earlier release: its tickets are shown as they were planned.</p>
        <p v-if="journey.stage !== 'plan'"><button type="button" class="linkish" @click="ctx.view(journey.stage)">Where the journey is now <AppIcon name="arrow" :size="12" /></button></p>
      </GateCard>
      <LaterCard v-if="!planning && !opening && !ctx.release.value" stage="plan" />
      <p v-if="store.busy || ctx.plan.saving.value" class="saving" role="status">Saving the plan…</p>
    </div>
  </div>
</template>

<style scoped>
.benefit-hint { color: var(--ink-3); font-size: 12px; }
.list-skel { height: 560px; border-radius: 10px; }
.add-row { display: flex; flex-wrap: wrap; gap: 8px; }
.add-row .field, .add-row .btn { height: 36px; }
.add-row > .field:first-child { flex: 1 1 240px; }
/* A narrow card (a docked ticket beside it) wraps the row instead of overflowing. */
.add-row .feature { flex: 0 1 220px; min-width: 150px; }
@media (max-width: 720px) { .add-row > .field:first-child, .add-row .feature { flex-basis: 100%; } }
@media (max-width: 600px) {
  .add-row .field, .add-row .btn { height: 44px; }
  .add-row .btn { flex: 1 1 0; }
}
.saving { font-size: 12px; color: var(--ink-3); }
.linkish { display: inline-flex; align-items: center; gap: 4px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font-weight: 600; cursor: pointer; }
.linkish:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
</style>
