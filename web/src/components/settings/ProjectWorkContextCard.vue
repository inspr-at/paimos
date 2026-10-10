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
import { failureText, MAX_PAGES, PAGE, PERMISSION, projectContextSummary, readColumn, readContexts, readProjectContext, saveProjectContext, tickable, type ProjectContext, type UseAccount, type WorkContext } from '../../lib/accountUse'
import SettingsCard from './SettingsCard.vue'

const props = defineProps<{ project: { id: string; title?: string } }>()
const session = useSession()
const allowed = computed(() => session.identity?.principal.kind === 'person' && can(PERMISSION))
const scope = useIdentityScope(() => allowed.value)
const reads = scope.lane()
// The context list and the project's own context are read separately: the mapped
// context is found wherever it sits in the matrix, and its accounts are counted
// across account pages, both bounded and reported when cut short.
interface Column { id: string; context: WorkContext | null; accounts: UseAccount[]; truncated: boolean }
const contexts = ref<WorkContext[]>([]), listTruncated = ref(false), revision = ref(0)
const mapping = ref<ProjectContext | null>(null), column = ref<Column | null>(null)
const state = ref<'loading' | 'ready' | 'error'>('loading'), busy = ref(false), said = ref('')

const options = computed(() => {
  const own = column.value?.context, all = own && !contexts.value.some(c => c.id === own.id) ? [...contexts.value, own] : contexts.value
  const holding = all.find(c => c.kind === 'holding')
  return [...tickable(all).map(c => ({ id: c.id, label: c.kind === 'default' ? `${c.name} (default)` : c.name })), ...(holding ? [{ id: holding.id, label: `${holding.name} · no account works here` }] : [])]
})
const summary = computed(() => {
  const c = column.value
  if (mapping.value && c?.id !== mapping.value.context_id) return 'Reading which accounts may work here…'
  return projectContextSummary(!!mapping.value, c?.context ?? null, (c?.accounts ?? []).map(a => a.label || HARNESS_LABEL[a.harness] || a.harness), !!c?.truncated)
})

async function readAll(projectId: string, signal: AbortSignal) {
  const [list, p] = await Promise.all([readContexts(signal), readProjectContext(projectId, signal)])
  const col = p ? await readColumn(p.context_id, signal) : null
  return { list, p, col }
}
function load() {
  if (!scope.owner.value) return
  const projectId = props.project.id
  void reads.run(({ after, signal }) => after(readAll(projectId, signal), ({ list, p, col }) => {
    if (projectId !== props.project.id) return
    contexts.value = list.contexts; listTruncated.value = list.truncated; mapping.value = p
    column.value = col && p ? { id: p.context_id, context: col.context, accounts: col.accounts, truncated: col.truncated } : null
    revision.value = Math.max(list.revision, p?.revision ?? 0, col?.revision ?? 0); state.value = 'ready'
  }), {
    failed: () => {
      if (state.value !== 'ready') { state.value = 'error'; return }
      said.value = 'The context could not be read again. The last state read stays visible.'; toast(said.value, { tone: 'error' })
    },
  })
}
watch([() => props.project.id, () => scope.owner.value], () => { contexts.value = []; listTruncated.value = false; revision.value = 0; mapping.value = null; column.value = null; said.value = ''; busy.value = false; state.value = 'loading'; load() }, { immediate: true })

function choose(event: Event) {
  const select = event.target as HTMLSelectElement, contextId = select.value
  // The action belongs to the project and revision on screen when the person chose.
  const projectId = props.project.id, expected = revision.value
  if (!expected || busy.value || contextId === mapping.value?.context_id) { select.value = mapping.value?.context_id ?? ''; return }
  busy.value = true; reads.cancel()
  const name = [...contexts.value, column.value?.context].find(c => c?.id === contextId)?.name ?? 'the context'
  void scope.run(({ after, signal }) => after(saveProjectContext(projectId, expected, contextId, signal), saved => {
    if (projectId !== props.project.id) return
    mapping.value = saved; revision.value = saved.revision
    said.value = `Moved to ${name}.`; toast(`${props.project.title ?? 'Project'} moved to ${name}.`)
  }), {
    failed: error => {
      select.value = mapping.value?.context_id ?? ''
      said.value = failureText(error, 'The context'); toast(said.value, { tone: 'error' })
      if (error instanceof APIError && error.status === 409) load()
    },
    settled: () => { busy.value = false; if (mapping.value && column.value?.id !== mapping.value.context_id) load() },
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
        <p v-if="listTruncated" class="line" role="note">Only the first {{ (MAX_PAGES * PAGE).toLocaleString('en') }} contexts are listed; this project's own context is always included.</p>
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
