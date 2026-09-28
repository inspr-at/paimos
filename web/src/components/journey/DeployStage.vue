<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { ACTION_LONG, gateApprovals, offeredApproval, PLUGIN_GATE } from '../../lib/journey'
import { useJourneyContext } from '../../lib/journeyContext'
import AppIcon from '../AppIcon.vue'
import GateApprovals from './GateApprovals.vue'
import GateCard from './GateCard.vue'
import HandoffList from './HandoffList.vue'
import LaterCard from './LaterCard.vue'

// Deploy: Pharos applies the release after your approval. Its deploy step has
// gates of its own; launch admission among them comes from the projection's
// launch readiness (can it admit, and the current blocker).
const ctx = useJourneyContext()
const journey = computed(() => ctx.journey.value)
const next = computed(() => journey.value.next_action)
const pharos = computed(() => ctx.plugins.value.find(p => p.id === 'pharos') ?? null)
const deployGates = computed(() => pharos.value?.workflow_steps?.find(s => s.key === 'deploy')?.gates ?? [])
const handoffs = computed(() => ctx.data.handoffs.value.value.filter(h => h.stage === 'deploy'))
const deployed = computed(() => handoffs.value.some(h => h.operation === 'deploy' && h.state === 'succeeded'))
const renewing = computed(() => !!next.value.renewal_action)
const decisionGate = computed(() => next.value.renewal_action === 'renew_candidate' ? 'candidate' : 'deploy')
const deciding = computed(() => next.value.key === 'approve_deploy' || next.value.key === 'retry_deploy' || renewing.value)
const awaitingEvidence = computed(() => next.value.key === 'approve_deploy' && next.value.label === 'Await deployment evidence')
// The decision in one title and one sentence; the gate request below says who asks.
const decisionTitle = computed(() => {
  if (next.value.key === 'retry_deploy') return 'The host did not apply it'
  if (next.value.renewal_action) return next.value.renewal_action === 'renew_candidate' ? 'Candidate approval is no longer live' : 'Deployment approval is no longer live'
  return next.value.label
})
const decisionDescription = computed(() => {
  if (next.value.renewal_action) return 'Renewing it restarts preparation and deployment with fresh evidence.'
  if (next.value.key === 'approve_deploy' && next.value.label === 'Apply deployment approval') return 'The gate is approved. Apply that decision to hand the release to Pharos for deployment.'
  if (awaitingEvidence.value) return 'The approved gate was applied. Pharos now reports the deployment and verification outcome here.'
  if (next.value.key === 'retry_deploy') return 'Retrying hands it to the host again, with fresh evidence.'
  return ACTION_LONG[next.value.key]
})
const handoffEmpty = computed(() => awaitingEvidence.value ? 'Pharos has not reported a deployment attempt yet.' : 'No deployment was handed to Pharos yet. After you apply the deployment approval, Pharos reports each attempt here.')
const approvals = computed(() => gateApprovals(ctx.approvals.value, decisionGate.value, journey.value.current_release_id))
const approval = computed(() => offeredApproval(ctx.approvals.value, journey.value, decisionGate.value, ctx.now.value))
// The action names a request this screen does not have at all (not merely an expired one).
const detailsMissing = computed(() => !!next.value.approval_request_id && !ctx.approvals.value.some(a => a.id === next.value.approval_request_id))
const state = computed(() => journey.value.stages.find(s => s.key === 'deploy')?.state ?? 'later')
// Launch readiness: a blocker once the release is at Deploy; before that, what it waits for.
const launch = computed(() => journey.value.launch_readiness ?? { can_admit: false, reason: 'This server does not report launch readiness.' })
const launchReady = computed(() => launch.value.can_admit)
const atDeploy = computed(() => journey.value.stage === 'deploy')
const launchNote = computed(() => launchReady.value ? 'ready' : atDeploy.value ? 'closed' : 'not yet')
// Each check reads by its shape as well as its colour: passed, waiting, yours, or
// not reported yet (Pharos checks it when it deploys).
type CheckTone = 'ok' | 'wait' | 'you' | 'open'
function checkTone(gate: string): CheckTone {
  if (gate === 'launch_admission') return launchReady.value ? 'ok' : 'wait'
  if (gate === 'person_decision') {
    if (!deciding.value || awaitingEvidence.value || approval.value?.decision === 'approved') return 'ok'
    return approval.value ? 'you' : 'wait'
  }
  return 'open'
}
const CHECK_TIP: Record<CheckTone, string> = { ok: 'Passed', wait: 'Waiting', you: 'Waiting for your decision', open: 'Pharos checks this when it deploys' }
const pluginName = computed(() => pharos.value ? 'Pharos' : '')
const targetNote = computed(() => !pharos.value ? 'Pharos is not installed on this server' : pharos.value.installation?.enabled ? '' : 'not enabled on this server')
</script>

<template>
  <div class="j-grid deploy">
    <div class="j-col">
      <section class="j-card" aria-labelledby="deploy-target">
        <header class="j-card-head"><p id="deploy-target" class="eyebrow">Target</p></header>
        <dl class="j-kv">
          <dt>Host</dt>
          <dd><template v-if="pharos"><b class="host">{{ pluginName }}</b> <span class="faint">deploys and verifies</span></template><span v-if="targetNote" class="target-note" :class="{ faint: !pharos }"><template v-if="pharos"> · </template>{{ targetNote }}</span></dd>
          <dt>Release</dt><dd :class="{ faint: !ctx.release.value }">{{ ctx.release.value ? `${ctx.releaseLabel.value} · ${ctx.release.value.key}` : 'None is ready to deploy' }}</dd>
          <dt>Checks</dt>
          <dd>
            <ul class="j-checks deploy-checks">
              <li v-for="gate in deployGates" :key="gate" :class="checkTone(gate)">
                <span class="check-mark" :class="checkTone(gate)" :data-tip="CHECK_TIP[checkTone(gate)]">
                  <AppIcon v-if="checkTone(gate) === 'ok'" name="check" :size="12" />
                  <AppIcon v-else-if="checkTone(gate) === 'wait'" name="clock" :size="12" />
                  <AppIcon v-else-if="checkTone(gate) === 'you'" name="user" :size="12" />
                  <svg v-else width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-dasharray="2.6 2.4" aria-hidden="true"><circle cx="8" cy="8" r="5.6" /></svg>
                  <span class="sr-only">{{ CHECK_TIP[checkTone(gate)] }}:</span>
                </span>
                <span class="check-text">
                  <span>{{ PLUGIN_GATE[gate] ?? gate.replace(/_/g, ' ') }}<template v-if="gate === 'launch_admission'"> · {{ launchNote }}</template><template v-else-if="gate === 'person_decision' && checkTone(gate) !== 'ok'"> · {{ checkTone(gate) === 'you' ? 'waiting for you' : 'waiting for a gate request' }}</template></span>
                  <small v-if="gate === 'launch_admission' && !launchReady && launch.reason">{{ launch.reason }}</small>
                </span>
              </li>
              <li v-if="!deployGates.length" class="open"><span class="check-mark open"><AppIcon name="info" :size="12" /></span><span class="check-text">Pharos lists its checks once it is installed.</span></li>
            </ul>
          </dd>
        </dl>
      </section>
      <section v-if="handoffs.length || state !== 'later'" class="j-card" aria-labelledby="deploy-handoffs">
        <header class="j-card-head"><p id="deploy-handoffs" class="eyebrow">Handoffs · deploy and verify</p></header>
        <HandoffList :handoffs="handoffs" :now="ctx.now.value" :empty="handoffEmpty" />
      </section>
    </div>
    <div class="j-col decide-col">
      <GateCard
        v-if="deciding" eyebrow="Decision" :title="decisionTitle"
        :action="{ label: ctx.next.value.label, disabled: ctx.next.value.disabled, busy: ctx.next.value.busy, tip: ctx.next.value.tip }" @act="ctx.runNext()"
      >
        <p>{{ decisionDescription }}</p>
        <p v-if="ctx.release.value" class="goes-to"><AppIcon name="server" :size="13" /><span>{{ ctx.releaseLabel.value }} goes to <b>{{ pharos ? 'Pharos' : 'the host' }}</b></span></p>
        <GateApprovals :gate="decisionGate" :approvals="approvals" :on="ctx.releaseLabel.value" :can-decide="ctx.canAct.value" :now="ctx.now.value" :me="ctx.me.value" />
        <p v-if="!approval && !awaitingEvidence && (detailsMissing || !approvals.length)" class="j-note">{{ detailsMissing ? 'The gate details are missing. Refresh to check them.' : `An agent asks for a fresh ${decisionGate === 'candidate' ? 'candidate' : 'deployment'} gate; it appears here for you to approve.` }}</p>
      </GateCard>
      <section v-else-if="launchReady" class="j-card ready" aria-label="Launch admission is ready">
        <p class="eyebrow ok-eyebrow"><AppIcon name="check" :size="11" />Launch admission</p>
        <p>Pharos can admit this release.</p>
      </section>
      <GateCard v-if="!deciding && (state === 'done' || deployed)" eyebrow="Deployed" :title="ctx.releaseLabel.value" tone="record"><p>Pharos applied and verified the release.</p></GateCard>
      <LaterCard v-else-if="!deciding" stage="deploy" :detail="ctx.release.value && !launchReady && launch.reason ? `Launch admission: ${launch.reason.charAt(0).toLowerCase()}${launch.reason.slice(1)}` : ''" />
    </div>
  </div>
</template>

<style scoped>
.ok-eyebrow { display: inline-flex; align-items: center; gap: 6px; color: var(--ok); }
.ready p:not(.eyebrow) { font-size: 13.5px; color: var(--ink-2); }
.host { font-weight: 600; }
.faint { color: var(--ink-3); }
.target-note { color: var(--gold-ink); }
.target-note.faint { color: var(--ink-3); }
.deploy-checks li { grid-template-columns: 20px minmax(0, 1fr); }
.check-mark { display: inline-grid; place-items: center; width: 18px; height: 18px; margin-top: 1px; border-radius: 50%; color: var(--ink-3); }
.check-mark svg { margin: 0; }
.check-mark.ok { color: var(--ok); background: color-mix(in oklab, var(--ok) 12%, transparent); }
.check-mark.wait { color: var(--gold-ink); background: var(--gold-wash); }
.check-mark.you { color: var(--teal-ink); background: var(--chip-teal-bg); }
.check-text { display: grid; gap: 1px; min-width: 0; }
.check-text small { font-size: 12px; color: var(--ink-2); }
li.open .check-text { color: var(--ink-2); }
.goes-to { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--ink-2); }
.goes-to svg { flex-shrink: 0; color: var(--ink-3); }
.goes-to b { color: var(--ink); font-weight: 600; }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
/* One column: the decision comes first, the evidence after it. */
@media (max-width: 1080px) { .decide-col { order: -1; } }
@container journey (max-width: 800px) { .decide-col { order: -1; } }
</style>
