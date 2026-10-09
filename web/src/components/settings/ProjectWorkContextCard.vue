<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1054: a project's work context (concept AEON-1044 §2.5). The context
// decides which accounts may work on the project. A project without a mapping,
// or in Unassigned, gets no account; it never falls back to the default.
import { computed, ref, watch } from 'vue'
import { APIError } from '../../lib/api'
import { can } from '../../lib/authz'
import { toast } from '../../lib/toast'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { HARNESS_LABEL } from '../../lib/agentState'
import { useSession } from '../../stores/session'
import { allowedSet, cellKey, failureText, PERMISSION, readMatrix, readProjectContext, saveProjectContext, tickable, type Matrix, type ProjectContext } from '../../lib/accountUse'
import SettingsCard from './SettingsCard.vue'

const props = defineProps<{ project: { id: string; title?: string } }>()
const session = useSession()
const allowed = computed(() => session.identity?.principal.kind === 'person' && can(PERMISSION))
const scope = useIdentityScope(() => allowed.value)
const reads = scope.lane()
const matrix = ref<Matrix | null>(null), mapping = ref<ProjectContext | null>(null), state = ref<'loading' | 'ready' | 'error'>('loading'), busy = ref(false), said = ref('')

const options = computed(() => {
  const contexts = matrix.value?.contexts ?? []
  const holding = contexts.find(c => c.kind === 'holding')
  return [...tickable(contexts).map(c => ({ id: c.id, label: c.kind === 'default' ? `${c.name} (default)` : c.name })), ...(holding ? [{ id: holding.id, label: `${holding.name} · no account works here` }] : [])]
})
const current = computed(() => matrix.value?.contexts.find(c => c.id === mapping.value?.context_id))
const accountsHere = computed(() => {
  const m = matrix.value, c = current.value
  if (!m || !c || c.kind === 'holding') return []
  const set = allowedSet(m.cells)
  return m.accounts.filter(a => set.has(cellKey(a.id, c.id))).map(a => a.label || HARNESS_LABEL[a.harness] || a.harness)
})
const summary = computed(() => {
  if (!mapping.value) return 'This project has no context yet, so no account may work on it. Choose one.'
  if (!current.value || current.value.kind === 'holding') return 'No account may work on this project until it gets a context.'
  const n = accountsHere.value.length
  return n ? `${n === 1 ? '1 account may' : `${n} accounts may`} work here: ${accountsHere.value.join(', ')}.` : 'No account is allowed in this context yet. Tick accounts in Settings › Accounts.'
})

function load() {
  if (!scope.owner.value) return
  const projectId = props.project.id
  void reads.run(({ after, signal }) => after(Promise.all([readMatrix(signal), readProjectContext(projectId, signal)]), ([m, p]) => {
    if (projectId !== props.project.id) return
    matrix.value = m; mapping.value = p; state.value = 'ready'
  }), { failed: () => { state.value = matrix.value ? 'ready' : 'error' } })
}
watch([() => props.project.id, () => scope.owner.value], () => { matrix.value = null; mapping.value = null; said.value = ''; busy.value = false; state.value = 'loading'; load() }, { immediate: true })

function choose(event: Event) {
  const select = event.target as HTMLSelectElement, contextId = select.value, m = matrix.value
  // The action belongs to the project and revision on screen when the person chose.
  // Two reads can answer at different revisions; send the newer one, the server re-checks.
  const projectId = props.project.id, revision = Math.max(mapping.value?.revision ?? 0, m?.rules.revision ?? 0)
  if (!m || !revision || busy.value || contextId === mapping.value?.context_id) { select.value = mapping.value?.context_id ?? ''; return }
  busy.value = true; reads.cancel()
  const name = m.contexts.find(c => c.id === contextId)?.name ?? 'the context'
  void scope.run(({ after, signal }) => after(saveProjectContext(projectId, revision, contextId, signal), saved => {
    if (projectId !== props.project.id) return
    mapping.value = saved; matrix.value = { ...m, rules: { ...m.rules, revision: saved.revision } }
    said.value = `Moved to ${name}.`; toast(`${props.project.title ?? 'Project'} moved to ${name}.`)
  }), {
    failed: error => {
      select.value = mapping.value?.context_id ?? ''
      said.value = failureText(error, 'The context'); toast(said.value, { tone: 'error' })
      if (error instanceof APIError && error.status === 409) load()
    },
    settled: () => { busy.value = false },
  })
}
</script>

<template>
  <div v-if="allowed" class="context-layout">
    <SettingsCard title="Where accounts may work" icon="shield" anchor="project-work-context">
      <template #lead>The context decides which accounts may work on this project. Owners and admins choose it.</template>
      <p v-if="state === 'loading'" class="line" role="status">Loading the context…</p>
      <p v-else-if="state === 'error'" class="line" role="alert">The context could not be read. <button type="button" class="btn sm" @click="load">Try again</button></p>
      <template v-else>
        <label class="pick"><span>Context</span>
          <select class="field" data-project-context :value="mapping?.context_id ?? ''" :disabled="busy" :aria-describedby="'project-context-summary'" @change="choose">
            <option v-if="!mapping" value="" disabled>Not chosen · no account works here</option>
            <option v-for="o in options" :key="o.id" :value="o.id">{{ o.label }}</option>
          </select>
        </label>
        <p id="project-context-summary" class="line">{{ summary }}</p>
        <p class="sr-only" role="status" aria-live="polite">{{ said }}</p>
      </template>
    </SettingsCard>
  </div>
</template>

<style scoped>
.context-layout { padding: 0 0 18px; }
.pick { display: grid; grid-template-columns: auto minmax(0, 22rem); align-items: center; gap: 12px; font-size: 13px; color: var(--ink-2); }
.pick .field { min-width: 0; min-height: 36px; }
.line { margin-top: 10px; font-size: 12.5px; color: var(--ink-3); }
@media (max-width: 600px) { .pick { grid-template-columns: minmax(0, 1fr); gap: 6px; } .pick .field { min-height: 44px; } }
</style>
