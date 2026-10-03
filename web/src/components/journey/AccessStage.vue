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

// Access: who may use the release. Janus prepares and applies a bounded permit;
// the step is skipped when no ticket in the release changes access.
const ctx = useJourneyContext()
const journey = computed(() => ctx.journey.value)
const next = computed(() => journey.value.next_action)
const state = computed(() => journey.value.stages.find(s => s.key === 'access')?.state ?? 'later')
const janus = computed(() => ctx.plugins.value.find(p => p.id === 'janus') ?? null)
const applyGates = computed(() => janus.value?.workflow_steps?.find(s => s.key === 'apply')?.gates ?? [])
const handoffs = computed(() => ctx.data.handoffs.value.value.filter(h => h.stage === 'access'))
const approvals = computed(() => gateApprovals(ctx.approvals.value, 'access', journey.value.current_release_id))
const approval = computed(() => offeredApproval(ctx.approvals.value, journey.value, 'access', ctx.now.value))
</script>

<template>
  <div v-if="state === 'skipped'" class="j-grid one">
    <section class="j-card"><h3>Not needed in {{ ctx.releaseLabel.value.toLowerCase() }}</h3><p class="j-note">No ticket in this release changes who may use it. The existing permit stays.</p></section>
  </div>
  <div v-else class="j-grid">
    <div class="j-col">
      <section class="j-card" aria-labelledby="access-permit">
        <header class="j-card-head"><p id="access-permit" class="eyebrow">Permit · Janus</p><span class="j-chip" :class="janus?.installation?.enabled ? 'ok' : 'gold'">{{ janus ? janus.installation?.enabled ? 'Enabled' : 'Not enabled' : 'Not installed' }}</span></header>
        <dl class="j-kv">
          <dt>Release</dt><dd>{{ ctx.release.value ? `${ctx.releaseLabel.value} · ${ctx.release.value.key}` : 'None is waiting for access' }}</dd>
          <dt>Plugin</dt><dd :class="{ faint: !janus }">{{ janus ? `Janus prepares and applies a bounded permit (${janus.owner})` : 'Janus is not installed on this server' }}</dd>
          <dt>Checks</dt>
          <dd>
            <ul class="j-checks">
              <li v-for="gate in applyGates" :key="gate"><AppIcon :name="gate === 'person_decision' ? 'user' : 'info'" :size="13" class="info" /><span>{{ PLUGIN_GATE[gate] ?? gate.replace(/_/g, ' ') }}</span></li>
              <li v-if="!applyGates.length"><AppIcon name="info" :size="13" class="info" /><span>Janus lists its checks once it is installed.</span></li>
            </ul>
          </dd>
        </dl>
      </section>
      <section v-if="handoffs.length || state !== 'later'" class="j-card" aria-labelledby="access-handoffs">
        <header class="j-card-head"><p id="access-handoffs" class="eyebrow">Handoffs · prepare and apply</p></header>
        <HandoffList :handoffs="handoffs" :now="ctx.now.value" empty="No access change was handed to Janus yet." />
      </section>
    </div>
    <div class="j-col">
      <GateCard
        v-if="next.key === 'approve_permit'" eyebrow="Decision" :title="next.access_renewal_action === 'renew_permit' ? 'Renew the Access permit' : 'Approve the permit'"
        :action="{ label: ctx.next.value.label, disabled: ctx.next.value.disabled, busy: ctx.next.value.busy, tip: ctx.next.value.tip }" @act="ctx.runNext()"
      >
        <p>{{ ACTION_LONG[next.access_renewal_action ?? 'approve_permit'] }}</p>
        <p v-if="!next.available && next.reason" class="j-note">{{ next.reason }}</p>
        <GateApprovals gate="access" :approvals="approvals" :on="ctx.releaseLabel.value" :can-decide="ctx.canAct.value" :now="ctx.now.value" :me="ctx.me.value" />
        <p v-if="!approval" class="j-note">An agent asks for the access gate; it appears here for you to approve.</p>
      </GateCard>
      <GateCard v-else-if="state === 'done'" eyebrow="Approved" title="Permit granted" tone="record"><p>The bounded permit is in place for this release.</p></GateCard>
      <LaterCard v-else stage="access" />
    </div>
  </div>
</template>
