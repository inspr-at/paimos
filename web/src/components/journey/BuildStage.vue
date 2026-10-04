<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { isJourneyLeaf } from '../../lib/journeyWork'
import { ACTION_LONG, gateApprovals, offeredApproval } from '../../lib/journey'
import { useJourneyContext } from '../../lib/journeyContext'
import { plural, statusMeta } from '../../lib/work'
import { useJourney } from '../../stores/journey'
import AppIcon from '../AppIcon.vue'
import StatusIcon from '../work/StatusIcon.vue'
import GateApprovals from './GateApprovals.vue'
import GateCard from './GateCard.vue'
import LaterCard from './LaterCard.vue'
import ReleaseList from './ReleaseList.vue'
import ReleaseTickets from './ReleaseTickets.vue'

// Build: the current release first: how far its tickets are, what still stands
// between it and the candidate, then the tickets and the releases before it.
// The candidate gate follows once the build is ready; sending it back needs a reason.
const ctx = useJourneyContext()
const store = useJourney()
const journey = computed(() => ctx.journey.value)
const walker = computed(() => ctx.data.walker.value.value)
const tickets = computed(() => (walker.value?.tickets ?? []).filter(t => t.included))
const stateOf = (id: string) => ctx.data.workById.value.get(id)?.state ?? ''
const counts = computed(() => {
  const out = { done: 0, qa: 0, progress: 0, open: 0, cancelled: 0 }
  for (const t of tickets.value) {
    const key = statusMeta(stateOf(t.ticket_node_id)).key
    if (key === 'cancelled' || key === 'archived') out.cancelled++
    else if (statusMeta(stateOf(t.ticket_node_id)).closed) out.done++
    else if (key === 'qa') out.qa++
    else if (key === 'progress') out.progress++
    else out.open++
  }
  return out
})
const scope = computed(() => tickets.value.length - counts.value.cancelled)
const segments = computed(() => { const n = Math.min(24, Math.max(scope.value, 1)); return Array.from({ length: n }, (_, i) => i < Math.round(counts.value.done / Math.max(1, scope.value) * n)) })
const percent = computed(() => scope.value ? Math.round(counts.value.done / scope.value * 100) : 0)
const next = computed(() => journey.value.next_action)
const building = computed(() => next.value.key === 'wait_for_build')
const candidate = computed(() => next.value.key === 'approve_candidate' && next.value.stage === 'build')
const marking = computed(() => next.value.key === 'mark_candidate')
const buildApprovals = computed(() => gateApprovals(ctx.approvals.value, 'build', journey.value.current_release_id))
const buildApproval = computed(() => offeredApproval(ctx.approvals.value, journey.value, 'build', ctx.now.value))
const here = computed(() => journey.value.stage === 'build')
const approvals = computed(() => gateApprovals(ctx.approvals.value, 'candidate', journey.value.current_release_id))
const approval = computed(() => offeredApproval(ctx.approvals.value, journey.value, 'candidate', ctx.now.value))
const releases = computed(() => ctx.data.releases.value)
const stageState = computed(() => journey.value.stages.find(s => s.key === 'build')?.state ?? 'later')
// Not reached and nothing to show yet: the stage says so once, with the way to where the journey is.
const bare = computed(() => stageState.value === 'later' && !ctx.release.value && !releases.value.length)
const openTickets = computed(() => ctx.data.work.value.value.filter(i => isJourneyLeaf(i) && !statusMeta(i.state).closed).length)
const backlogLine = computed(() => openTickets.value ? `${plural(openTickets.value, 'open ticket')} wait in the backlog; release 1 is chosen from them.` : '')
const left = computed(() => counts.value.open + counts.value.progress + counts.value.qa)
const rejecting = ref(false)
const reason = ref('')
const reasonField = ref<HTMLTextAreaElement>()
async function startReject() { rejecting.value = true; reason.value = ''; await nextTick(); reasonField.value?.focus() }
async function reject() {
  if (!reason.value.trim()) return
  if (await ctx.act('reject_candidate', { approval: approval.value, reason: reason.value.trim(), done: 'The candidate goes back to the crew with your reason.' })) rejecting.value = false
}
</script>

<template>
  <div v-if="bare" class="j-grid one narrow"><LaterCard stage="build" :detail="backlogLine" /></div>
  <div v-else class="j-grid">
    <div class="j-col">
      <section class="j-card" aria-labelledby="build-release">
        <header class="j-card-head">
          <p id="build-release" class="eyebrow">{{ ctx.release.value ? `${ctx.releaseLabel.value} · ${here ? (tickets.length && !left ? 'every ticket done' : 'in progress') : 'tickets'}` : 'The release' }}</p>
          <span v-if="tickets.length" class="j-count">{{ counts.done }} of {{ plural(scope, 'ticket') }} done</span>
          <button type="button" class="btn sm" :disabled="!tickets.length" aria-keyshortcuts="w" @click="ctx.walk()"><AppIcon name="expand" :size="13" />Full screen</button>
        </header>
        <div v-if="!ctx.release.value" class="j-empty"><strong>No release is being built</strong><span>The build starts from the planned release.</span></div>
        <p v-else-if="ctx.data.walker.status.value === 'loading'" class="skeleton list-skel" role="status" aria-label="Loading the release" />
        <div v-else-if="!tickets.length" class="j-empty"><strong>No tickets in this release</strong></div>
        <template v-else>
          <div class="progress">
            <div class="j-segs" role="img" :aria-label="`${counts.done} of ${scope} tickets done`"><i v-for="(on, i) in segments" :key="i" :class="{ on }" /></div>
            <b class="pct mono">{{ percent }}%</b>
          </div>
          <ul class="tally" aria-label="Tickets by status">
            <li><StatusIcon state="done" :size="12" /><b>{{ counts.done }}</b> done</li>
            <li><StatusIcon state="qa" :size="12" /><b>{{ counts.qa }}</b> in QA</li>
            <li><StatusIcon state="in_progress" :size="12" /><b>{{ counts.progress }}</b> in progress</li>
            <li><StatusIcon state="new" :size="12" /><b>{{ counts.open }}</b> not started</li>
            <li v-if="counts.cancelled"><StatusIcon state="cancelled" :size="12" /><b>{{ counts.cancelled }}</b> cancelled</li>
          </ul>
          <ReleaseTickets :plan="ctx.plan" :editable="false" only="included" :project-key="ctx.project.value.routeKey" :work-by-id="ctx.data.workById.value" @walk="t => ctx.walk(t.key)" @open="ctx.open" />
        </template>
      </section>
      <section v-if="releases.length" class="j-card" aria-labelledby="build-releases">
        <header class="j-card-head"><p id="build-releases" class="eyebrow">Releases · {{ releases.length }}</p></header>
        <ReleaseList :releases="releases" :current-id="journey.current_release_id" :selected-id="ctx.release.value?.id ?? null" :now="ctx.now.value" :limit="6" @select="r => ctx.selectRelease(r.key)" />
      </section>
    </div>
    <div class="j-col">
      <section v-if="here && ctx.release.value" class="j-card" aria-labelledby="build-left">
        <header class="j-card-head"><p id="build-left" class="eyebrow">What stands before the candidate</p></header>
        <ul class="j-checks">
          <li v-if="left"><AppIcon name="clock" :size="13" class="warn" /><span><b>{{ plural(left, 'ticket') }}</b> still open{{ counts.qa ? ` · ${counts.qa} in QA` : '' }}{{ counts.progress ? ` · ${counts.progress} in progress` : '' }}</span></li>
          <li v-else-if="tickets.length"><AppIcon name="check" :size="13" class="ok" /><span>Every ticket of {{ ctx.releaseLabel.value }} is done</span></li>
          <li v-if="!next.available && next.reason && !marking && !building"><AppIcon name="info" :size="13" class="info" /><span>{{ next.reason }}</span></li>
          <li v-if="marking"><AppIcon :name="buildApproval ? 'shield' : 'info'" :size="13" :class="buildApproval ? 'warn' : 'info'" /><span>{{ buildApproval ? 'The build gate is requested: approve it to mark the candidate' : 'Marking the candidate needs the build gate, which an agent asks for' }}</span></li>
          <li v-else><AppIcon :name="approvals.some(a => a.decision === null) ? 'shield' : 'info'" :size="13" :class="approvals.some(a => a.decision === null) ? 'warn' : 'info'" /><span>{{ approvals.some(a => a.decision === null) ? 'The candidate gate is requested' : 'The candidate gate is asked for when the build is ready' }}</span></li>
        </ul>
        <p v-if="!marking" class="j-note">{{ ACTION_LONG.wait_for_build }}</p>
      </section>
      <GateCard
        v-if="marking" eyebrow="Decision" :title="`Mark ${ctx.releaseLabel.value.toLowerCase()} as the candidate`"
        :action="{ label: ctx.next.value.label, disabled: ctx.next.value.disabled, busy: ctx.next.value.busy, tip: ctx.next.value.tip }" @act="ctx.runNext()"
      >
        <p>{{ ACTION_LONG.mark_candidate }}</p>
        <p v-if="!next.available && next.reason && buildApproval" class="j-note">{{ next.reason }}</p>
        <GateApprovals gate="build" :approvals="buildApprovals" :on="ctx.releaseLabel.value" :can-decide="ctx.canAct.value" :now="ctx.now.value" :me="ctx.me.value" />
        <p v-if="!buildApproval" class="j-note">An agent asks for the build gate on {{ ctx.releaseLabel.value }}; it appears here for you to approve.</p>
      </GateCard>
      <GateCard
        v-else-if="candidate" eyebrow="Decision" title="Approve the release candidate"
        :action="{ label: ctx.next.value.label, disabled: ctx.next.value.disabled, busy: ctx.next.value.busy, tip: ctx.next.value.tip }" @act="ctx.runNext()"
      >
        <p>All {{ plural(tickets.length, 'ticket') }} of {{ ctx.releaseLabel.value }} are built. {{ ACTION_LONG.approve_candidate }}</p>
        <GateApprovals gate="candidate" :approvals="approvals" :on="ctx.releaseLabel.value" :can-decide="ctx.canAct.value" :now="ctx.now.value" :me="ctx.me.value" />
        <p v-if="!approval && !journey.stages.find(s => s.key === 'build')?.gate_offer_id" class="j-note">An agent asks for the candidate gate; it appears here for you to approve.</p>
        <div v-if="ctx.canAct.value" class="reject">
          <button v-if="!rejecting" type="button" class="btn sm ghost" :disabled="!approval || store.busy" @click="startReject">Send it back</button>
          <form v-else class="reason" @submit.prevent="reject">
            <label for="reject-reason">What should change?</label>
            <textarea id="reject-reason" ref="reasonField" v-model="reason" class="field" rows="2" maxlength="2048" @keydown.esc.stop="rejecting = false" />
            <div class="reason-actions"><button type="button" class="btn sm ghost" @click="rejecting = false">Cancel</button><button type="submit" class="btn sm deny" :disabled="!reason.trim() || store.busy">Send back</button></div>
          </form>
        </div>
      </GateCard>
      <GateCard v-else-if="!building && journey.stages.find(s => s.key === 'build')?.state === 'done'" eyebrow="Done" :title="`${ctx.releaseLabel.value} candidate`" tone="record">
        <p>{{ next.renewal_action === 'renew_candidate' ? 'The candidate approval is no longer live. Renew it in Deploy.' : 'The candidate is approved.' }} <button type="button" class="linkish" @click="ctx.view('deploy')">Deploy <AppIcon name="arrow" :size="12" /></button></p>
      </GateCard>
      <LaterCard v-else-if="!here" stage="build" />
    </div>
  </div>
</template>

<style scoped>
.list-skel { height: 160px; border-radius: 10px; }
.progress { display: flex; align-items: center; gap: 12px; }
.progress .j-segs { flex: 1; height: 8px; }
.pct { font-size: 13px; color: var(--ink); }
.tally { display: flex; flex-wrap: wrap; gap: 6px 16px; margin: 0; padding: 0 0 4px; list-style: none; font-size: 12.5px; color: var(--ink-2); }
.tally li { display: inline-flex; align-items: center; gap: 6px; }
.tally b { font: 600 13px/1 var(--mono); color: var(--ink); }
.reject { display: grid; }
.reject > .btn { justify-self: start; }
.reason { display: grid; gap: 6px; }
.reason label { font-size: 12.5px; color: var(--ink-2); }
.reason textarea { height: auto; padding: 8px 10px; }
.reason-actions { display: flex; justify-content: flex-end; gap: 6px; }
.btn.deny { background: var(--danger-bg); color: var(--danger); box-shadow: inset 0 0 0 1px var(--danger-line); }
.linkish { display: inline-flex; align-items: center; gap: 4px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font-weight: 600; cursor: pointer; }
.linkish:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
</style>
