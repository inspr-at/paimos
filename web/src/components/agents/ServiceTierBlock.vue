<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, watch } from 'vue'
import type { HarnessSession } from '../../lib/agents'
import { useServiceTiers } from '../../stores/serviceTiers'
import { useSession } from '../../stores/session'
import { can } from '../../lib/authz'
import { TIER_NAME, tierOptions, tierPrice, tierSpeed, offeredTier, tierEstimate, estimateCostText, estimateTimeText } from '../../lib/serviceTier'
import TierGlyph from './TierGlyph.vue'
import AppIcon from '../AppIcon.vue'
const props = defineProps<{ session: HarnessSession; name: string }>()
const tiers = useServiceTiers(), auth = useSession()
const state = computed(() => tiers.state(props.session)), report = computed(() => tiers.report(props.session))
const rejection = computed(() => tiers.rejection(props.session))
const options = computed(() => tierOptions(report.value))
const active = computed(() => options.value.find(t => t.tier === state.value.active_tier))
const request = computed(() => state.value.requests[0])
const permitted = computed(() => auth.identity?.principal.kind === 'person' && can('harness.control', props.session.project_id))
const canOpen = computed(() => (!tiers.unavailable(props.session) || tiers.canAsk(props.session)) && !state.value.pending && !tiers.busy[props.session.id])
const explanation = computed(() => {
  const s = props.session
  if (s.stopped_at || s.archived_at || s.phase === 'stopped') return `This session ran at ${state.value.active_tier ? TIER_NAME[state.value.active_tier] : 'an unreported tier'}.`
  if (s.management_mode === 'unmanaged') return `Reported by ${report.value?.harness_version || s.harness} — ${report.value?.change_instructions || 'change it in its terminal'}.`
  if (state.value.pending) return `Switching to ${TIER_NAME[state.value.pending.value]} · waiting for the daemon. The active tier stays unchanged until confirmation.`
  return `A change applies from the ${report.value?.applies === 'next_turn' ? 'next turn' : 'next run'}. The current run keeps its tier and cost.`
})
const source = computed(() => { try { const url = new URL(report.value?.source || ''); return ['https:', 'http:'].includes(url.protocol) ? url.href : null } catch { return null } })
let epoch = 0
watch([() => props.session.id, () => props.session.service_tier_revision, () => props.session.service_tier_request], async () => {
  const own = ++epoch, s = props.session
  try { await tiers.load(s); if (own === epoch && props.session.id === s.id) tiers.follow(s) }
  catch (error) { if (own === epoch && props.session.id === s.id) tiers.errors[s.id] = error instanceof Error ? error.message : 'Tier information unavailable.' }
}, { immediate: true })
async function checkResult() {
  const s = props.session, own = ++epoch
  try {
    const answer = await tiers.load(s)
    if (answer && own === epoch && props.session.id === s.id) { tiers.errors[s.id] = ''; tiers.follow(s) }
  } catch { /* Keep the visible error until a read confirms the result. */ }
}
async function decide(decision: 'approve' | 'decline') {
  const s = props.session, q = request.value
  if (!q || q.state !== 'pending') return
  await tiers.change(s, props.name, q.tier, { request: q, decision, withUndo: decision === 'approve' })
}
</script>
<template>
  <section class="service-tier block" aria-label="Service tier">
    <div class="tier-title"><h3 class="eyebrow">Service tier</h3>
      <button v-if="!tiers.unavailable(session) || tiers.canAsk(session)" type="button" class="btn sm ghost tier-change" :disabled="!canOpen" aria-haspopup="dialog" @click="tiers.open(session, name, $event.currentTarget as HTMLElement)">{{ tiers.canAsk(session) ? 'Ask for a tier' : 'Change tier' }}</button>
    </div>
    <div v-if="request" class="tier-request" :class="{ settled: request.state !== 'pending' }" role="note">
      <!-- Actions retain their boxes when the outcome appears below them. -->
      <div class="request-actions">
        <button v-if="permitted" type="button" class="btn sm primary" :disabled="request.state !== 'pending' || !canOpen || !offeredTier(options.find(t => t.tier === request?.tier))" @click="decide('approve')">Approve {{ TIER_NAME[request.tier] }}</button>
        <button v-if="permitted" type="button" class="btn sm ghost" :disabled="request.state !== 'pending' || tiers.busy[session.id] || !session.process_ownership" @click="decide('decline')">Decline</button>
        <span v-if="!permitted">Waiting for a person</span>
      </div>
      <strong><AppIcon name="shield" :size="14" />{{ request.state === 'pending' ? `${tiers.canAsk(session) ? 'You asked' : name + ' asks'} for ${TIER_NAME[request.tier]}` : `${request.state === 'approved' ? 'Approved' : 'Declined'} · ${TIER_NAME[request.tier]}` }}</strong>
      <p>{{ request.reason }}</p>
      <p v-if="request.state === 'pending'">{{ tierPrice(options.find(t => t.tier === request?.tier)) }} · nothing changes until a person approves.</p>
    </div>
    <div class="tier-head"><TierGlyph :active="state.active_tier" :report="report" :pending="state.pending?.value" :scale="1.5" /><strong :class="{ paid: state.active_tier && state.active_tier !== 'default' }">{{ state.active_tier ? TIER_NAME[state.active_tier] : 'Not reported' }}</strong>
      <span>{{ tierSpeed(active) }} · {{ tierPrice(active) }}</span>
    </div>
    <p class="tier-applies">{{ explanation }}</p>
    <div class="compare" role="table" aria-label="Service tiers for this session">
      <div class="compare-head" role="row"><span role="columnheader">Tier</span><span role="columnheader">Speed · price per token</span><span role="columnheader">Last run at it</span></div>
      <div v-for="(option, i) in options" :key="option.tier" class="compare-row" :class="{ current: option.tier === state.active_tier, off: !offeredTier(option) }" role="row">
        <span role="cell" class="compare-name"><TierGlyph :active="option.tier" :report="report" :count="!offeredTier(option) ? i + 1 : undefined" :faint="!offeredTier(option)" />{{ option.name }}</span>
        <span role="cell">{{ offeredTier(option) ? `${tierSpeed(option)} · ${tierPrice(option)}` : option.reason }}<small v-if="offeredTier(option)">{{ option.mechanism || 'No launch flag' }}</small></span>
        <span role="cell" class="compare-estimate" :title="tierEstimate(state, option.tier).basis"><template v-if="offeredTier(option)">{{ estimateCostText(tierEstimate(state, option.tier)) }}<small>{{ estimateTimeText(tierEstimate(state, option.tier)) }}</small></template><template v-else>Not offered</template></span>
      </div>
    </div>
    <p class="basis">Same model and effort. Speed estimates apply only to model time; tools, tests and waits keep their time. The estimates apply price to frozen token costs and speed to measured model time only.</p>
    <p class="basis estimate-source" aria-label="Last-run estimate source">{{ tierEstimate(state, 'default').basis }}<template v-if="tierEstimate(state, 'default').run_id"> Run {{ tierEstimate(state, 'default').run_id }}.</template></p>
    <p v-if="report" class="source">{{ report.model }} · {{ report.harness }} {{ report.harness_version }} · adapter {{ report.adapter_version }} · checked <time :datetime="report.checked_at">{{ report.checked_at }}</time>.<br><a v-if="source" :href="source" target="_blank" rel="noopener noreferrer">Vendor source</a><span v-else>{{ report.source }}</span></p>
    <p v-if="tiers.errors[session.id] || rejection" class="tier-error" role="alert">{{ tiers.errors[session.id] || `The last tier change was rejected: ${rejection?.reason || 'reason unavailable'}.` }} <button type="button" class="btn sm ghost" @click="checkResult">Check result</button> <button v-if="tiers.canDismissRejection(session)" type="button" class="btn sm ghost" @click="tiers.dismissRejection(session)">Dismiss</button></p>
    <p v-if="tiers.unavailable(session) && session.management_mode === 'managed' && !session.stopped_at" class="rights">{{ tiers.unavailable(session) }}</p>
  </section>
</template>
<style scoped>
.service-tier{margin-top:24px;min-width:0}.tier-title{display:flex;align-items:center;justify-content:space-between;gap:10px;min-height:32px;margin-bottom:10px}.tier-title h3{margin:0}.tier-head{margin-top:10px;display:flex;flex-wrap:wrap;align-items:center;gap:6px 10px;min-height:32px}.tier-head strong{font-size:13px}.tier-head>span{font-size:12px;color:var(--ink-2)}.paid{color:var(--warn-ink)}.tier-change{margin-left:auto}.tier-applies{margin-top:6px;font-size:12.5px;line-height:1.45}.compare{margin-top:14px;border-top:1px solid var(--line)}.compare-head,.compare-row{display:grid;grid-template-columns:84px minmax(0,1fr) 100px;gap:12px;align-items:center;padding:8px 0;border-bottom:1px solid var(--line);font-size:12px}.compare-head{height:28px;padding:0;font:500 9.5px/1 var(--mono);letter-spacing:.08em;text-transform:uppercase;color:var(--ink-3)}.compare-row{min-height:52px}.compare-row.current{background:var(--row-selected);border-radius:8px;margin-inline:-8px;padding-inline:8px}.compare-row.off{color:var(--ink-3)}.compare-name{display:flex;align-items:center;gap:8px;font-weight:600}.compare-row small{display:block;margin-top:3px;font:11px/1.4 var(--mono);overflow-wrap:anywhere;color:var(--ink-3)}.basis,.source,.rights{margin-top:10px;color:var(--ink-3);font-size:11.5px;line-height:1.45;overflow-wrap:anywhere}.source{padding-top:10px;border-top:1px solid var(--line)}.tier-request{display:grid;gap:6px;margin-top:14px;padding:12px;background:var(--gold-wash);border-radius:10px;box-shadow:inset 0 0 0 1px var(--line-2);font-size:12.5px;overflow-wrap:anywhere}.request-actions{display:flex;gap:6px;min-height:32px}.request-actions>.btn{width:126px;height:32px}.tier-request strong{display:flex;align-items:center;gap:6px}.tier-request p{margin:0}.settled{background:var(--surface-2)}.tier-error{margin-top:10px;font-size:12px;color:var(--danger)}
</style>

<style scoped>
.compare-estimate{text-align:right;font-size:11.5px;font-variant-numeric:tabular-nums}.compare-estimate small{font-family:var(--font)}
@container panel (max-width:460px){.compare-head{display:none}.compare-row{grid-template-columns:minmax(0,1fr) 112px;gap:6px 12px}.compare-name{grid-column:1}.compare-estimate{grid-column:2;grid-row:1}.compare-row>span:nth-child(2){grid-column:1/-1;grid-row:2}.compare-row{position:relative}}
</style>
