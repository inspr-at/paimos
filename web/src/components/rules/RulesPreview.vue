<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import BizIcon from '../business/BizIcon.vue'
import RulesDialog from './RulesDialog.vue'
import {
  HARNESS_LABEL, HARNESSES, ROLE_LABEL, ROLES, RULES_BUDGET, RulesError, mergeQuery, mergeRules, orderPreviewAgents, previewDenial, previewOptionLabel, rulesMessage,
  type HarnessName, type MergedRules, type NamedAgent, type RoleName,
} from '../../lib/rules'

// The session file one agent would receive now, from published rules only. The
// context defaults to this project, you and the builder role; "Preview for…"
// is the only place the specialist selectors appear. The person is always the
// caller: the server previews only your own merged file.
const props = defineProps<{
  projects: { id: string; title: string }[]
  people: { id: string; name: string }[]
  agents: NamedAgent[]
  projectId: string
  personId: string
  waiting: number
}>()
const emit = defineEmits<{ close: [] }>()
const projectId = ref(props.projectId)
const personId = computed(() => props.personId)
const role = ref<RoleName>('builder')
const harness = ref<HarnessName>('claude-code')
const agentId = ref('')
const choosing = ref(false)
const merged = ref<MergedRules | null>(null)
const loading = ref(false)
const error = ref('')

const projectName = computed(() => props.projects.find(item => item.id === projectId.value)?.title ?? 'No project')
const personName = computed(() => props.people.find(item => item.id === personId.value)?.name ?? 'You')
const agentName = computed(() => props.agents.find(item => item.id === agentId.value)?.name ?? '')
const previewAgents = computed(() => orderPreviewAgents(props.agents))
const forLine = computed(() => [projectName.value, personName.value, agentName.value || ROLE_LABEL[role.value], HARNESS_LABEL[harness.value]].join(' · '))
const meter = computed(() => merged.value ? Math.min(100, merged.value.byte_size / RULES_BUDGET * 100) : 0)
const fmt = (n: number) => n.toLocaleString('en-US')

function denial(cause: unknown): string {
  if (!(cause instanceof RulesError)) return ''
  const selected = props.agents.find(item => item.id === agentId.value)
  return previewDenial(cause.code, selected?.preview?.creator_name)
}
async function load() {
  const blocked = props.agents.find(item => item.id === agentId.value && item.preview?.allowed === false)
  if (blocked) {
    merged.value = null
    error.value = previewDenial(blocked.preview?.reason ?? '', blocked.preview?.creator_name) || 'You do not have permission for that.'
    return
  }
  const query = mergeQuery({ projectId: projectId.value, personId: personId.value, agentId: agentId.value, role: role.value, harness: harness.value, taskId: '' })
  if ('error' in query) { error.value = query.error; merged.value = null; return }
  loading.value = true
  error.value = ''
  try { merged.value = await mergeRules(query.query) } catch (cause) {
    merged.value = null
    error.value = denial(cause) || (cause instanceof RulesError && cause.code === 'floor_missing'
      ? 'Nothing is live yet. Sessions need at least one published, locked company rule.'
      : rulesMessage(cause))
  } finally { loading.value = false }
}
onMounted(load)
watch([projectId, role, harness, agentId], load)
</script>

<template>
  <RulesDialog title="Session file" lede="What an agent receives at session start. Only published rules are included." size="side" @close="emit('close')">
    <div class="for">
      <span class="for-label">For</span>
      <span class="for-value">{{ forLine }}</span>
      <button type="button" class="btn sm ghost" :aria-expanded="choosing" data-autofocus @click="choosing = !choosing">{{ choosing ? 'Done' : 'Preview for…' }}</button>
    </div>
    <div v-if="choosing" class="choose" role="group" aria-label="Preview for">
      <label>Project<select v-model="projectId" class="field"><option v-for="project in projects" :key="project.id" :value="project.id">{{ project.title }}</option></select></label>
      <label>Role<select v-model="role" class="field"><option v-for="item in ROLES" :key="item" :value="item">{{ ROLE_LABEL[item] }}</option></select></label>
      <label>Harness<select v-model="harness" class="field"><option v-for="item in HARNESSES" :key="item" :value="item">{{ HARNESS_LABEL[item] }}</option></select></label>
      <label v-if="agents.length" class="wide">Named agent<select v-model="agentId" class="field"><option value="">None</option><option v-for="agent in previewAgents" :key="agent.id" :value="agent.id" :disabled="agent.preview?.allowed === false">{{ previewOptionLabel(agent) }}</option></select></label>
    </div>
    <p v-if="waiting" class="hint">{{ waiting }} {{ waiting === 1 ? 'set waits' : 'sets wait' }} to be published and {{ waiting === 1 ? 'is' : 'are' }} not in this file yet.</p>
    <div v-if="merged" class="budget">
      <div class="budget-line"><span>Size</span><span class="bytes">{{ fmt(merged.byte_size) }} of {{ fmt(RULES_BUDGET) }} bytes</span></div>
      <span class="bar" aria-hidden="true"><i :style="{ width: `${meter}%` }"></i></span>
    </div>
    <p v-if="error" class="error" role="alert"><BizIcon name="info" :size="14" /><span>{{ error }}</span></p>
    <div v-if="loading && !merged" class="skeleton-block" aria-label="Loading the session file" role="status"><span class="skeleton"></span><span class="skeleton"></span><span class="skeleton"></span></div>
    <pre v-if="merged" class="file" tabindex="0" aria-label="Session file">{{ merged.body }}</pre>
  </RulesDialog>
</template>

<style scoped>
.for { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; padding: 10px 12px; border-radius: 12px; background: var(--surface-2); }
.for-label { color: var(--ink-3); font-size: 12.5px; font-weight: 600; }
.for-value { flex: 1; min-width: 0; overflow-wrap: anywhere; font-size: 13.5px; font-weight: 600; }
.choose { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.choose label { display: grid; gap: 4px; min-width: 0; color: var(--ink-2); font-size: 12px; font-weight: 650; }
.choose .wide { grid-column: 1 / -1; }
.choose option:disabled { color: var(--ink-3); }
.hint { margin: 0; color: var(--ink-3); font-size: 12.5px; }
.budget { display: grid; gap: 6px; }
.budget-line { display: flex; justify-content: space-between; font-size: 12.5px; color: var(--ink-2); }
.bytes { color: var(--ink); font-weight: 600; font-variant-numeric: tabular-nums; }
.error { display: flex; gap: 6px; align-items: flex-start; margin: 0; padding: 10px 12px; border-radius: 10px; background: var(--surface-2); color: var(--ink-2); font-size: 13px; }
.error svg { flex: none; margin-top: 2px; }
.skeleton-block { display: grid; gap: 8px; }
.file { margin: 0; padding: 14px; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); white-space: pre-wrap; overflow-wrap: anywhere; font: 12.5px/1.6 var(--mono); color: var(--ink); }
.file:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (max-width: 600px) { .choose { grid-template-columns: 1fr; } }
</style>
