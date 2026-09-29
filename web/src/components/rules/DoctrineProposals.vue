<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import { getDoctrineProposals, getDoctrineFindings, refreshDoctrineProposal, approveDoctrineProposal, doctrineMessage, proposalState, findingState, outcomeMetric, outcomeDelta, outcomePopulation, outcomeMetricName, type DoctrineProposal, type DoctrineFinding } from '../../lib/doctrine'
import { onAccessChange } from '../../lib/authz'
import BizIcon from '../business/BizIcon.vue'
const props = defineProps<{ latest?: DoctrineProposal }>()
const stored = ref<DoctrineProposal[]>([])
const findings = ref<DoctrineFinding[]>([])
const loaded = ref(false)
let generation = 0
const individual = computed(() => stored.value.filter(p => !findings.value.some(f => f.proposal_id === p.id)))
function upsert(p: DoctrineProposal) { stored.value = [p, ...stored.value.filter(item => item.id !== p.id)] }
watch(() => props.latest, p => { if (p) upsert(p) }, { immediate: true })
const busy = ref('')
const error = ref('')
const session = useSession()
const person = computed(() => session.identity?.principal.kind === 'person')
const canApprove = computed(() => session.identity?.principal.kind === 'person' && can('rules.publish'))
const canWrite = computed(() => can('rules.write'))
async function update(p: DoctrineProposal, approve = false) {
  if (busy.value) return
  error.value = ''
  busy.value = p.id
  try {
    const next = approve ? await approveDoctrineProposal(p.id, p.head_sha) : await refreshDoctrineProposal(p.id)
    upsert(next)
  } catch (cause) { error.value = doctrineMessage(cause) }
  finally { busy.value = '' }
}
async function load() {
  const current = ++generation
  if (!person.value) return
  try {
    const proposals = await getDoctrineProposals()
    const analysis = can('outcome.read') && can('knowledge.read') ? await getDoctrineFindings() : []
    if (current !== generation) return
    stored.value = [...stored.value, ...proposals.filter(p => !stored.value.some(existing => existing.id === p.id))]
    findings.value = analysis
    loaded.value = true
  } catch (cause) { if (current === generation) error.value = doctrineMessage(cause) }
}
const stopAccess = onAccessChange(() => { generation++; stored.value = []; findings.value = []; loaded.value = false; error.value = ''; void load() })
onBeforeUnmount(() => { generation++; stopAccess() })
onMounted(load)
</script>
<template>
  <div v-if="person && (loaded || stored.length || error)" class="proposals" aria-label="Proposals">
    <h4>Proposals</h4>
    <p v-if="error" role="alert" class="quiet">{{ error }}</p>
    <p v-if="loaded && !stored.length && !findings.length" class="quiet">No proposals yet.</p>
    <article v-for="f in findings" :key="f.id" class="proposal outcome-proposal" :aria-label="f.title">
      <div class="info">
        <div class="finding-heading"><strong :title="f.title">{{ f.title }}</strong><span class="finding-state">{{ findingState(f) }}</span></div>
        <span v-if="f.rule_label" class="rule-label quiet" :title="f.rule_label">{{ f.rule_label }}</span>
        <div class="measurement">
          <span><span class="quiet">Before</span> {{ outcomeMetric(f.before) }}</span>
          <template v-if="f.after"><span><span class="quiet">After</span> {{ outcomeMetric(f.after) }}</span><span class="delta">{{ outcomeDelta(f) }}</span></template>
        </div>
        <details>
          <summary><BizIcon name="chevron-right" :size="12" class="chev" />{{ f.count }} affected {{ f.count === 1 ? 'ticket' : 'tickets' }} · Evidence</summary>
          <div class="evidence">
            <RouterLink v-for="e in f.evidence" :key="e.id" :to="e.href" :title="`${e.kind.replaceAll('_', ' ')} · ${e.id}`">{{ e.ticket_key }}</RouterLink>
          </div>
          <p class="quiet">{{ f.harness }} · {{ f.ticket_kind }} · Rules {{ f.rules_version }}</p>
          <p class="quiet">{{ f.before.samples }} {{ outcomePopulation(f.before) }} before<span v-if="f.after">; {{ f.after.samples }} after, using rules {{ f.after.rules_version }}</span>.</p>
          <dl v-if="f.metrics?.length" class="context-metrics quiet" aria-label="Baseline context">
            <template v-for="metric in f.metrics.filter(m => m.name !== f.before.name)" :key="metric.name"><dt>{{ outcomeMetricName(metric) }}</dt><dd :title="`${metric.samples} ${outcomePopulation(metric)}`">{{ outcomeMetric(metric) }}</dd></template>
          </dl>
          <p v-if="f.reason" class="quiet">{{ f.reason }}</p>
          <p v-else-if="!f.after" class="quiet">The comparison will appear after the changed instructions are used.</p>
          <p v-else class="quiet">This comparison describes recorded outcomes; it does not establish cause.</p>
        </details>
      </div>
      <a v-if="f.pr_url" class="open-pr" :href="f.pr_url" target="_blank" rel="noopener noreferrer">Open on GitHub</a>
    </article>
    <article v-for="p in individual" :key="p.id" class="proposal">
      <div class="info">
        <a v-if="p.pr_url" :href="p.pr_url" target="_blank" rel="noopener noreferrer">{{ p.repository.split('/').at(-1) }} · PR #{{ p.pr_number }}</a>
        <span v-else>{{ p.repository.split('/').at(-1) }} · {{ p.orphaned ? p.branch : 'proposal started' }}</span>
        <span class="state">{{ proposalState(p) }}</span>
        <span v-if="p.orphaned" class="quiet">Branch {{ p.branch }} is on GitHub without a pull request. An admin should delete it.</span>
        <span v-if="p.gate_reason && p.state === 'in_review'" class="quiet">{{ p.gate_reason }}</span>
        <a v-if="p.release_url" :href="p.release_url" target="_blank" rel="noopener noreferrer" class="quiet">{{ p.release }}</a>
        <span v-else-if="p.state === 'merged'" class="quiet">{{ p.release_requested ? 'Release requested; waiting for the repository.' : p.approved_by ? 'Merged; release request still pending.' : 'Merged externally; request release in the repository.' }}</span>
      </div>
      <div class="actions">
        <button v-if="canWrite && p.pr_number" class="btn sm ghost" :disabled="!!busy" :aria-label="`Refresh ${p.repository} PR #${p.pr_number}`" @click="update(p)">{{ busy === p.id ? 'Checking…' : 'Refresh' }}</button>
        <button v-if="canApprove && (p.gate_ready || p.state === 'merged' && p.approved_by && !p.release_requested)" class="btn sm primary" :disabled="!!busy" :aria-label="`${p.state === 'merged' ? 'Request release for' : 'Approve & merge'} ${p.repository} PR #${p.pr_number}`" :title="`Approve commit ${p.head_sha}`" @click="update(p, true)">{{ p.state === 'merged' ? 'Request release' : 'Approve & merge' }}</button>
      </div>
    </article>
  </div>
</template>
<style scoped>
.proposals { display: flex; flex-direction: column; gap: 8px; margin-left: 36px; min-width: 0; }
h4 { margin: 0; color: var(--ink-2); font-size: 13px; font-weight: 650; }
.proposal { display: flex; align-items: center; gap: 12px; padding: 12px 14px; border-radius: 12px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); min-width: 0; }
.info { display: flex; flex-direction: column; gap: 3px; flex: 1; min-width: 0; font-size: 13px; overflow-wrap: anywhere; }
a { color: var(--teal-ink); text-decoration: none; }
a:hover { text-decoration: underline; }
.state { font-weight: 600; }
.quiet { margin: 0; color: var(--ink-3); font-size: 12px; line-height: 1.5; }
.actions { display: flex; gap: 6px; flex-wrap: wrap; }
.outcome-proposal { align-items: flex-start; }
.outcome-proposal .info { width: 100%; }
.finding-heading { display: flex; align-items: baseline; gap: 12px; min-width: 0; }
.finding-heading strong { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-weight: 600; }
.finding-state { flex-shrink: 0; color: var(--ink-3); font-size: 11px; }
.rule-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.measurement { display: flex; flex-wrap: wrap; gap: 4px 16px; margin: 7px 0; font-variant-numeric: tabular-nums; color: var(--ink); }
.measurement .quiet { margin-right: 3px; }
.delta { color: var(--ink-2); font-size: 12px; align-self: center; }
details { min-width: 0; }
summary { display: flex; align-items: center; gap: 5px; width: fit-content; cursor: pointer; color: var(--ink-2); font-size: 12px; padding: 6px 0; }
.chev { flex-shrink: 0; }
details[open] .chev { transform: rotate(90deg); }
summary:focus-visible, .open-pr:focus-visible { outline: 2px solid var(--teal-ink); outline-offset: 3px; border-radius: 3px; }
.evidence { display: flex; flex-wrap: wrap; gap: 6px 12px; margin: 7px 0; }
.evidence a { font-size: 12px; }
.context-metrics { display: grid; grid-template-columns: auto 1fr; gap: 2px 12px; margin: 8px 0; }
.context-metrics dt, .context-metrics dd { margin: 0; }
.context-metrics dd { color: var(--ink-2); font-variant-numeric: tabular-nums; }
.open-pr { font-size: 12px; white-space: nowrap; padding: 5px 0; }
@media(max-width:600px) { .proposals { margin-left: 0; } .proposal { align-items: flex-start; flex-direction: column; } }
</style>
