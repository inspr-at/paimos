<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import BizIcon from '../business/BizIcon.vue'
import RulesDialog from './RulesDialog.vue'
import HeadingIdentity from '../HeadingIdentity.vue'
import {
  HARNESS_LABEL, HARNESSES, LAYER_LABEL, ROLE_LABEL, ROLES, RulesError, budgetParts, byteSize, explainRules, mergeQuery, orderPreviewAgents, previewDenial, previewOptionLabel, rulesMessage,
  type ExplainedRule, type ExplainedRules, type ExplainedSet, type HarnessName, type NamedAgent, type RoleName, type RuleScope,
} from '../../lib/rules'

// What one agent receives now, as a translation table (AEON-314): every rule
// of the session file next to its explanation for people, grouped by set in
// precedence order, filtered by harness and search. The exact file stays one
// click away with its byte budget. Published rules only; the person is always
// the caller, because the server explains only your own session file.
const props = defineProps<{
  projects: { id: string; title: string }[]
  people: { id: string; name: string }[]
  agents: NamedAgent[]
  projectId: string
  personId: string
  waiting: number
  /** Where a set's scope sits, in words ("Company", "Aeon project", …). */
  where?: (scope: RuleScope) => string
}>()
const emit = defineEmits<{ close: [] }>()
const projectId = ref(props.projectId)
const personId = computed(() => props.personId)
const role = ref<RoleName>('builder')
const harness = ref<HarnessName>('claude-code')
const agentId = ref('')
const choosing = ref(false)
const data = ref<ExplainedRules | null>(null)
const loading = ref(false)
const error = ref('')
const query = ref('')
const showFile = ref(false)

const projectName = computed(() => props.projects.find(item => item.id === projectId.value)?.title ?? 'No project')
const personName = computed(() => props.people.find(item => item.id === personId.value)?.name ?? 'You')
const agentName = computed(() => props.agents.find(item => item.id === agentId.value)?.name ?? '')
const previewAgents = computed(() => orderPreviewAgents(props.agents))
const forLine = computed(() => [projectName.value, personName.value, agentName.value || ROLE_LABEL[role.value]].join(' · '))
const fmt = (n: number) => n.toLocaleString('en-US')

const parts = computed(() => data.value ? budgetParts(data.value.usage, data.value.byte_size, data.value.budget) : [])
const meter = computed(() => data.value ? Math.min(100, data.value.byte_size / data.value.budget.max_bytes * 100) : 0)
const over = computed(() => parts.value.some(part => part.over))
const problem = computed(() => {
  const p = data.value?.problem
  if (!p) return ''
  if (p.code === 'floor_missing') return 'Sessions do not start with this file yet: publish at least one locked company rule.'
  if (p.code === 'rules_budget_exceeded') {
    if (p.layer) return `${LAYER_LABEL[p.layer]} rules take ${fmt(p.actual_bytes ?? 0)} bytes, over their cap of ${fmt(p.max_bytes ?? 0)}. Sessions get only the cached file until this is fixed.`
    return `The file is ${fmt(p.actual_bytes ?? 0)} bytes, over the budget of ${fmt(p.max_bytes ?? 0)}. Sessions get only the cached file until this is fixed.`
  }
  return p.error
})

interface Group { set: ExplainedSet; rules: ExplainedRule[] }
const matches = (value: string | undefined, needle: string) => !!value && value.toLowerCase().includes(needle)
const groups = computed<Group[]>(() => {
  const out = data.value
  if (!out) return []
  const needle = query.value.trim().toLowerCase()
  return out.sets.map(set => {
    const all = out.rules.filter(rule => rule.set_id === set.set_id)
    if (!needle || matches(set.name, needle) || matches(set.tldr?.en, needle) || matches(set.tldr?.de, needle)) return { set, rules: all }
    return { set, rules: all.filter(rule => matches(rule.text, needle) || matches(rule.identity, needle) || matches(rule.tldr?.en, needle) || matches(rule.tldr?.de, needle)) }
  }).filter(group => group.rules.length)
})
const shown = computed(() => groups.value.reduce((n, group) => n + group.rules.length, 0))
const missing = computed(() => data.value?.rules.filter(rule => !rule.tldr?.en).length ?? 0)
const summary = computed(() => {
  const out = data.value
  if (!out) return ''
  if (query.value.trim()) return `${shown.value} of ${out.rules.length} ${out.rules.length === 1 ? 'rule matches' : 'rules match'}`
  const total = out.rules.length
  const parts = [`${total} ${total === 1 ? 'rule' : 'rules'} from ${out.sets.length} ${out.sets.length === 1 ? 'set' : 'sets'}`]
  if (missing.value) parts.push(`${missing.value} without an explanation`)
  return parts.join(' · ')
})
const whereOf = (scope: RuleScope) => props.where?.(scope) ?? LAYER_LABEL[scope.layer]

// A named agent the caller cannot preview says why in a plain sentence
// (AEON-315), from the listing or from the server's denial code.
function denial(cause: unknown): string {
  if (!(cause instanceof RulesError)) return ''
  const selected = props.agents.find(item => item.id === agentId.value)
  return previewDenial(cause.code, selected?.preview?.creator_name)
}

let serial = 0
async function load() {
  const blocked = props.agents.find(item => item.id === agentId.value && item.preview?.allowed === false)
  if (blocked) {
    serial++
    data.value = null
    loading.value = false
    error.value = previewDenial(blocked.preview?.reason ?? '', blocked.preview?.creator_name) || 'You do not have permission for that.'
    return
  }
  const request = mergeQuery({ projectId: projectId.value, personId: personId.value, agentId: agentId.value, role: role.value, harness: harness.value, taskId: '' })
  if ('error' in request) { error.value = request.error; data.value = null; return }
  const mine = ++serial
  loading.value = true
  error.value = ''
  try {
    const out = await explainRules(request.query)
    if (mine === serial) data.value = out
  } catch (cause) {
    if (mine === serial) { data.value = null; error.value = denial(cause) || rulesMessage(cause) }
  } finally { if (mine === serial) loading.value = false }
}
onMounted(load)
watch([projectId, role, harness, agentId], load)
</script>

<template>
  <RulesDialog title="What agents receive" lede="Each rule next to its explanation. Only published rules count." size="sheet" @close="emit('close')">
    <div class="for">
      <span class="for-label">For</span>
      <HeadingIdentity :text="forLine" class="for-value" />
      <button type="button" class="btn sm ghost" :aria-expanded="choosing" data-autofocus @click="choosing = !choosing">
        <span :aria-hidden="choosing">Change</span>
        <span :aria-hidden="!choosing">Done</span>
      </button>
    </div>
    <div v-if="choosing" class="choose" role="group" aria-label="Preview for">
      <label>Project<select v-model="projectId" class="field"><option v-for="project in projects" :key="project.id" :value="project.id">{{ project.title }}</option></select></label>
      <label>Role<select v-model="role" class="field"><option v-for="item in ROLES" :key="item" :value="item">{{ ROLE_LABEL[item] }}</option></select></label>
      <label v-if="agents.length">Named agent<select v-model="agentId" class="field"><option value="">None</option><option v-for="agent in previewAgents" :key="agent.id" :value="agent.id" :disabled="agent.preview?.allowed === false">{{ previewOptionLabel(agent) }}</option></select></label>
    </div>

    <div class="tools">
      <div class="harness" role="radiogroup" aria-label="Harness">
        <button v-for="item in HARNESSES" :key="item" type="button" role="radio" :aria-checked="harness === item" class="seg" @click="harness = item">{{ HARNESS_LABEL[item] }}</button>
      </div>
      <label class="search">
        <AppIcon name="search" :size="14" />
        <input v-model="query" type="search" class="search-input" placeholder="Search rules and explanations" aria-label="Search rules and explanations">
      </label>
    </div>

    <p v-if="waiting" class="hint">{{ waiting }} {{ waiting === 1 ? 'set waits' : 'sets wait' }} to be published and {{ waiting === 1 ? 'is' : 'are' }} not in this file yet.</p>

    <div v-if="data" class="budget" :class="{ over }">
      <p class="budget-line">
        <template v-for="(part, i) in parts" :key="part.label"><span v-if="i" class="sep" aria-hidden="true"> · </span><span class="part" :class="{ 'part-over': part.over }">{{ part.label }}<span v-if="part.over" class="sr-only"> (over)</span></span></template>
      </p>
      <span class="bar" aria-hidden="true"><i :style="{ width: `${meter}%` }"></i></span>
    </div>
    <p v-if="problem" class="problem" role="status"><BizIcon name="info" :size="14" /><span>{{ problem }}</span></p>
    <p v-if="error" class="error" role="alert"><BizIcon name="info" :size="14" /><span>{{ error }}</span></p>
    <div v-if="loading && !data" class="skeleton-block" aria-label="Loading the session file" role="status"><span class="skeleton"></span><span class="skeleton"></span><span class="skeleton"></span></div>

    <template v-if="data">
      <p class="summary" role="status">{{ summary }}</p>
      <div class="table" role="table" aria-label="Explanation and exact rule" :aria-rowcount="shown">
        <div class="thead" role="row">
          <span role="columnheader">Explanation</span>
          <span role="columnheader">Exact rule</span>
        </div>
        <div v-for="group in groups" :key="group.set.set_id" class="group" role="rowgroup">
          <div class="group-head" role="row">
            <span role="cell" class="group-cell">
              <span class="group-title"><strong class="set-name">{{ group.set.name }}</strong><span class="where">{{ whereOf(group.set.scope) }}</span><span class="set-bytes">{{ byteSize(group.set.bytes) }}</span></span>
              <span v-if="group.set.tldr?.en" class="set-tldr">{{ group.set.tldr.en }}<span v-if="group.set.tldr.check" class="check" data-tip="The rules changed after this explanation was written.">Check</span></span>
            </span>
          </div>
          <div v-for="rule in group.rules" :key="rule.identity" class="row" role="row">
            <span role="cell" class="explain">
              <template v-if="rule.tldr?.en"><span :lang="'en'">{{ rule.tldr.en }}</span><span v-if="rule.tldr.check" class="check" data-tip="The rule text changed after this explanation was written.">Check</span></template>
              <span v-else class="none">No explanation yet</span>
            </span>
            <span role="cell" class="exact">
              <span class="exact-text">
                <span v-if="rule.strength === 'locked'" class="lock" role="img" aria-label="Locked" data-tip="Locked"><BizIcon name="lock" :size="12" /></span>
                <code>{{ rule.text }}</code>
              </span>
              <span class="ident">{{ rule.identity }}</span>
            </span>
          </div>
        </div>
        <p v-if="!groups.length" class="empty">{{ query.trim() ? `No rule matches “${query.trim()}”.` : 'No published rules reach this session yet.' }}</p>
      </div>

      <div class="file-block">
        <button type="button" class="btn sm ghost" :aria-expanded="showFile" aria-controls="rules-exact-file" @click="showFile = !showFile">
          <AppIcon :name="showFile ? 'eye-off' : 'eye'" :size="14" />{{ showFile ? 'Hide exact file' : 'Show exact file' }}
        </button>
        <span class="file-size">{{ fmt(data.byte_size) }} of {{ fmt(data.budget.max_bytes) }} bytes</span>
      </div>
      <pre v-if="showFile" id="rules-exact-file" class="file" tabindex="0" aria-label="Session file">{{ data.body }}</pre>
    </template>
  </RulesDialog>
</template>

<style scoped>
.for { display: flex; align-items: flex-start; gap: 6px 10px; padding: 8px 8px 8px 12px; border-radius: 12px; background: var(--surface-2); }
/* Both labels size the same grid cell; only the active label is seen or read. */
.for .btn { flex: none; display: inline-grid; justify-items: center; }
.for .btn > span { grid-area: 1 / 1; }
.for .btn > span[aria-hidden="true"] { visibility: hidden; }
.for-label { color: var(--ink-3); font-size: 12.5px; font-weight: 600; }
/* Changing the preview identity must not displace its selectors or search. */
.for-value { flex: 1; min-width: 0; height: 2lh; line-height: 1.45; font-size: 13.5px; font-weight: 600; white-space: normal; overflow-wrap: anywhere; overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
@media (pointer: coarse) { .for-value { line-height: max(1.45em, 22px); } }
.choose { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; }
.choose label { display: grid; gap: 4px; min-width: 0; color: var(--ink-2); font-size: 12px; font-weight: 650; }
.choose option:disabled { color: var(--ink-3); }
.tools { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
.harness { display: inline-flex; align-items: center; gap: 2px; padding: 3px; border-radius: 10px; background: var(--surface-2); }
.seg { display: inline-flex; align-items: center; justify-content: center; height: 28px; min-height: 0; margin: 0; padding: 0 12px; border: 0; border-radius: 7px; background: none; color: var(--ink-2); font: inherit; font-size: 13px; font-weight: 600; line-height: 1; cursor: pointer; }
.seg[aria-checked="true"] { background: var(--surface); color: var(--ink); box-shadow: 0 0 0 1px var(--line), 0 1px 2px color-mix(in srgb, var(--shadow-black) 6%, transparent); }
.seg:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (hover: hover) { .seg[aria-checked="false"]:hover { color: var(--ink); } }
.search { flex: 1 1 220px; display: flex; align-items: center; gap: 6px; min-width: 0; height: 34px; padding: 0 10px; border-radius: 10px; background: var(--surface); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-3); }
.search:focus-within { box-shadow: var(--focus-ring); }
.search-input { flex: 1; min-width: 0; height: 100%; padding: 0; border: 0; background: none; color: var(--ink); font: inherit; font-size: 13.5px; outline: none; box-shadow: none; }
.search-input:focus, .search-input:focus-visible { outline: none; box-shadow: none; }
.hint { margin: 0; color: var(--ink-3); font-size: 12.5px; }
.budget { display: grid; gap: 6px; }
.budget-line { margin: 0; color: var(--ink-2); font-size: 12.5px; font-variant-numeric: tabular-nums; }
.part { white-space: nowrap; }
.sep { color: var(--ink-3); }
.part-over { color: var(--danger); font-weight: 650; }
.over .bar > i { background: var(--danger); box-shadow: none; }
.problem, .error { display: flex; gap: 6px; align-items: flex-start; margin: 0; padding: 10px 12px; border-radius: 10px; background: var(--surface-2); color: var(--ink-2); font-size: 13px; line-height: 1.45; }
.problem svg, .error svg { flex: none; margin-top: 2px; }
.skeleton-block { display: grid; gap: 8px; }
.summary { margin: 0; color: var(--ink-3); font-size: 12.5px; }
.table { display: flex; flex-direction: column; border-radius: 12px; box-shadow: 0 0 0 1px var(--line); overflow: hidden; }
.thead, .row { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.15fr); gap: 16px; padding: 0 14px; }
.thead { padding-top: 8px; padding-bottom: 8px; background: var(--surface-2); color: var(--ink-3); font-size: 11.5px; font-weight: 650; letter-spacing: .02em; text-transform: uppercase; }
.group + .group { border-top: 1px solid var(--line); }
.group-head { padding: 12px 14px 6px; }
.group-cell { display: grid; gap: 2px; min-width: 0; }
.group-title { display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px 10px; min-width: 0; }
.set-name { font-size: 14px; font-weight: 650; }
.where { min-width: 0; overflow-wrap: anywhere; color: var(--ink-3); font-size: 12.5px; }
.set-bytes { margin-left: auto; color: var(--ink-3); font-size: 12px; font-variant-numeric: tabular-nums; }
.set-tldr { display: flex; align-items: baseline; gap: 8px; color: var(--ink-2); font-size: 13px; line-height: 1.45; }
.row { padding-top: 8px; padding-bottom: 8px; align-items: start; }
.row + .row { border-top: 1px solid var(--line); }
@media (hover: hover) { .row:hover { background: var(--row-hover); } }
.explain { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 8px; min-width: 0; font-size: 13.5px; line-height: 1.5; color: var(--ink); }
.none { color: var(--ink-3); font-style: italic; }
.check { flex: none; padding: 0 7px; border-radius: 999px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-2); font-size: 11px; font-weight: 600; line-height: 17px; font-style: normal; }
.exact { display: grid; gap: 2px; min-width: 0; }
.exact-text { display: flex; align-items: baseline; gap: 6px; min-width: 0; }
.exact code { min-width: 0; font: 12.5px/1.55 var(--mono); color: var(--ink); overflow-wrap: anywhere; }
.lock { flex: none; display: inline-grid; place-items: center; color: var(--teal-ink); transform: translateY(1px); }
.ident { color: var(--ink-3); font: 11.5px/1.4 var(--mono); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.empty { margin: 0; padding: 18px 14px; color: var(--ink-3); font-size: 13px; }
.file-block { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
.file-size { color: var(--ink-3); font-size: 12.5px; font-variant-numeric: tabular-nums; }
.file { margin: 0; padding: 14px; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); white-space: pre-wrap; overflow-wrap: anywhere; font: 12.5px/1.6 var(--mono); color: var(--ink); }
.file:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
@media (max-width: 600px) {
  .choose { grid-template-columns: 1fr; }
  .harness { width: 100%; box-sizing: border-box; flex-wrap: wrap; }
  .seg { flex: 1 1 0; padding: 0 6px; }
  .thead { display: none; }
  .row { grid-template-columns: minmax(0, 1fr); gap: 4px; }
  .exact { padding: 6px 8px; border-radius: 8px; background: var(--surface-2); }
}
</style>
