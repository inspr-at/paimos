<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import { getDoctrineProposals, refreshDoctrineProposal, approveDoctrineProposal, doctrineMessage, proposalState, type DoctrineProposal } from '../../lib/doctrine'
const props = defineProps<{ latest?: DoctrineProposal }>()
const stored = ref<DoctrineProposal[]>([])
function upsert(p: DoctrineProposal) { stored.value = [p, ...stored.value.filter(item => item.id !== p.id)] }
watch(() => props.latest, p => { if (p) upsert(p) }, { immediate: true })
const busy = ref('')
const error = ref('')
const session = useSession()
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
onMounted(async () => {
  try { const loaded = await getDoctrineProposals(); stored.value = [...stored.value, ...loaded.filter(p => !stored.value.some(existing => existing.id === p.id))] }
  catch (cause) { error.value = doctrineMessage(cause) }
})
</script>
<template>
  <div v-if="stored.length || error" class="proposals">
    <h4>Proposed changes</h4>
    <p v-if="error" role="alert" class="quiet">{{ error }}</p>
    <article v-for="p in stored" :key="p.id" class="proposal">
      <div class="info">
        <a v-if="p.pr_url" :href="p.pr_url" target="_blank" rel="noopener noreferrer">{{ p.repository.split('/').at(-1) }} · PR #{{ p.pr_number }}</a>
        <span v-else>{{ p.repository.split('/').at(-1) }} · proposal started</span>
        <span class="state">{{ proposalState(p) }}</span>
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
@media(max-width:600px) { .proposals { margin-left: 0; } .proposal { align-items: flex-start; flex-direction: column; } }
</style>
