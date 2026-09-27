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
const deciding = computed(() => next.value.key === 'approve_deploy' || next.value.key === 'retry_deploy')
const decisionDescription = computed(() => {
  if (next.value.key === 'approve_deploy' && next.value.label === 'Apply deployment approval') return 'The gate is approved. Apply that decision to hand the release to Pharos for deployment.'
  if (next.value.key === 'approve_deploy' && next.value.label === 'Await deployment evidence') return 'The approved gate was applied. Pharos now reports the deployment and verification outcome here.'
  return ACTION_LONG[next.value.key]
})
const awaitingEvidence = computed(() => next.value.key === 'approve_deploy' && next.value.label === 'Await deployment evidence')
const handoffEmpty = computed(() => awaitingEvidence.value ? 'Pharos has not reported a deployment attempt yet.' : 'No deployment was handed to Pharos yet. After you apply the deployment approval, Pharos reports each attempt here.')
const launchBlockedNote = computed(() => {
  if (next.value.key === 'retry_deploy') return 'Retrying records a new handoff after a fresh approval; the host applies the release once admission can succeed.'
  if (awaitingEvidence.value) return 'The approval is already applied. The host applies the release once admission can succeed.'
  if (next.value.label === 'Apply deployment approval') return 'The gate is approved. Applying it records the handoff; the host applies the release once admission can succeed.'
  return 'Approving the deployment still records your decision; the host applies the release once admission can succeed.'
})
const approvals = computed(() => gateApprovals(ctx.approvals.value, 'deploy', journey.value.current_release_id))
const approval = computed(() => offeredApproval(ctx.approvals.value, journey.value, 'deploy', ctx.now.value))
const state = computed(() => journey.value.stages.find(s => s.key === 'deploy')?.state ?? 'later')
// Launch readiness: a blocker once the release is at Deploy; before that, what it waits for.
const launch = computed(() => journey.value.launch_readiness ?? { can_admit: false, reason: 'This server does not report launch readiness.' })
const launchReady = computed(() => launch.value.can_admit)
const atDeploy = computed(() => journey.value.stage === 'deploy')
const launchNote = computed(() => launchReady.value ? 'ready' : atDeploy.value ? 'closed' : 'not yet')
</script>

<template>
  <div class="j-grid">
    <div class="j-col">
      <section class="j-card" aria-labelledby="deploy-target">
        <header class="j-card-head"><p id="deploy-target" class="eyebrow">Target · Pharos</p><span class="j-chip" :class="pharos?.installation?.enabled ? 'ok' : 'gold'">{{ pharos ? pharos.installation?.enabled ? 'Enabled' : 'Not enabled' : 'Not installed' }}</span></header>
        <dl class="j-kv">
          <dt>Release</dt><dd>{{ ctx.release.value ? `${ctx.releaseLabel.value} · ${ctx.release.value.key}` : 'None is ready to deploy' }}</dd>
          <dt>Plugin</dt><dd :class="{ faint: !pharos }">{{ pharos ? `Pharos deploys and verifies (${pharos.owner})` : 'Pharos is not installed on this server' }}</dd>
          <dt>Checks</dt>
          <dd>
            <ul class="j-checks">
              <li v-for="gate in deployGates" :key="gate">
                <AppIcon :name="gate === 'launch_admission' ? (launchReady ? 'check' : atDeploy ? 'close' : 'clock') : gate === 'person_decision' ? 'user' : 'info'" :size="13" :class="gate === 'launch_admission' ? (launchReady ? 'ok' : atDeploy ? 'bad' : 'info') : 'info'" />
                <span>{{ PLUGIN_GATE[gate] ?? gate.replace(/_/g, ' ') }}<template v-if="gate === 'launch_admission'"> · {{ launchNote }}</template><template v-else-if="gate === 'person_decision'"> · the deployment gate below</template></span>
              </li>
              <li v-if="!deployGates.length"><AppIcon name="info" :size="13" class="info" /><span>Pharos lists its checks once it is installed.</span></li>
            </ul>
          </dd>
        </dl>
      </section>
      <section v-if="handoffs.length || state !== 'later'" class="j-card" aria-labelledby="deploy-handoffs">
        <header class="j-card-head"><p id="deploy-handoffs" class="eyebrow">Handoffs · deploy and verify</p></header>
        <HandoffList :handoffs="handoffs" :now="ctx.now.value" :empty="handoffEmpty" />
      </section>
    </div>
    <div class="j-col">
      <GateCard v-if="!launchReady && atDeploy" eyebrow="Blocked" title="Launch admission is closed" tone="blocked">
        <p>{{ launch.reason || 'Pharos did not say why.' }}</p>
        <p class="j-note">{{ launchBlockedNote }}</p>
      </GateCard>
      <section v-else-if="launchReady" class="j-card ready" aria-label="Launch admission is ready">
        <p class="eyebrow ok-eyebrow"><AppIcon name="check" :size="11" />Launch admission</p>
        <p>Pharos can admit this release.</p>
      </section>
      <GateCard
        v-if="deciding" eyebrow="Decision" :title="next.key === 'retry_deploy' ? 'The host did not apply it' : next.label"
        :action="{ label: ctx.next.value.label, disabled: ctx.next.value.disabled, busy: ctx.next.value.busy, tip: ctx.next.value.tip }" @act="ctx.runNext()"
      >
        <p>{{ decisionDescription }}</p>
        <p v-if="!next.available && next.reason" class="j-note">{{ next.reason }}</p>
        <GateApprovals gate="deploy" :approvals="approvals" :on="ctx.releaseLabel.value" :can-decide="ctx.canAct.value" :now="ctx.now.value" :me="ctx.me.value" />
        <p v-if="!approval && !awaitingEvidence" class="j-note">{{ next.approval_request_id ? 'The action needs the current gate details. Refresh to check its availability.' : 'An agent asks for a fresh deployment gate; it appears here for you to approve.' }}</p>
      </GateCard>
      <GateCard v-else-if="state === 'done' || deployed" eyebrow="Deployed" :title="ctx.releaseLabel.value" tone="record"><p>Pharos applied and verified the release.</p></GateCard>
      <LaterCard v-else stage="deploy" :detail="ctx.release.value && !launchReady && launch.reason ? `Launch admission: ${launch.reason.charAt(0).toLowerCase()}${launch.reason.slice(1)}` : ''" />
    </div>
  </div>
</template>

<style scoped>
.ok-eyebrow { display: inline-flex; align-items: center; gap: 6px; color: var(--ok); }
.ready p:not(.eyebrow) { font-size: 13.5px; color: var(--ink-2); }
</style>
