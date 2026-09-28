<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref } from 'vue'
import AppIcon from '../AppIcon.vue'
import { canFlip, groupState, rulesEqual, type AgentRule, type CheckState } from '../../lib/rules'

export interface ColumnSet {
  id: string
  name: string
  dirty: boolean
  readOnly: boolean
  collapsed: boolean
  versionId: string
  versionNote: string
  versions: { version: string; note?: string }[] | null
  rules: AgentRule[]
  baseline: AgentRule[]
  writable: boolean
  writeReason: string | null
}
const props = defineProps<{
  number: number
  title: string
  hint: string
  sets: ColumnSet[]
  held: string[]
  filter: string
  selectedId: string
  createLabel: string
  createReason: string | null
  hasLayer: boolean
  canAdd: boolean
}>()
const emit = defineEmits<{
  'toggle-rule': [setId: string, identity: string]
  'toggle-set': [setId: string]
  'toggle-layer': []
  select: [setId: string, identity: string]
  'add-rule': [setId: string]
  'add-set': [name: string]
  create: []
  rename: [setId: string, name: string]
  collapse: [setId: string]
  history: [setId: string]
  version: [setId: string, version: string]
}>()

const newName = ref('')
const heldSet = () => new Set(props.held)
const matches = (rule: AgentRule) => {
  const query = props.filter.trim().toLowerCase()
  return !query || rule.text.toLowerCase().includes(query) || rule.identity.includes(query)
}
const visible = (rules: AgentRule[]) => rules.filter(matches)
const stateOf = (rules: AgentRule[]): CheckState => groupState(rules, heldSet())
const layerRules = () => props.sets.flatMap(set => set.rules)
const layerMovable = () => layerRules().some(rule => canFlip(rule, heldSet()))
const draftOf = (set: ColumnSet, rule: AgentRule) => {
  if (set.readOnly) return false
  const old = set.baseline.find(item => item.identity === rule.identity)
  return !old || !rulesEqual([old], [rule])
}
const labelOf = (rule: AgentRule) => rule.text.trim() || 'Untitled rule'
const expiryOf = (rule: AgentRule) => rule.expires_at ? rule.expires_at.slice(0, 10) : ''

function setIndeterminate(el: unknown, state: CheckState) {
  if (el instanceof HTMLInputElement) el.indeterminate = state === 'mixed'
}
function onSetToggle(set: ColumnSet) {
  if (!set.writable || set.readOnly) return
  emit('toggle-set', set.id)
}
function addSet() {
  const name = newName.value.trim()
  if (!name) return
  emit('add-set', name)
  newName.value = ''
}
</script>

<template>
  <section class="column" :aria-label="title">
    <header class="column-head">
      <input
        type="checkbox" class="check-box" :checked="stateOf(layerRules()) === 'on'"
        :ref="el => setIndeterminate(el, stateOf(layerRules()))"
        :disabled="!layerMovable() || sets.every(set => !set.writable || set.readOnly)"
        :aria-label="`Switch ${title} rules`"
        @change="emit('toggle-layer')"
      >
      <span class="num">{{ number }}</span>
      <span class="titles"><span class="name">{{ title }}</span><span class="hint">{{ hint }}</span></span>
      <span class="count">{{ layerRules().filter(rule => rule.enabled || rule.strength === 'locked' || held.includes(rule.identity)).length }}/{{ layerRules().length }}</span>
    </header>

    <p v-if="!hasLayer" class="empty">{{ createReason ?? 'No rules in this layer yet.' }}</p>
    <button v-if="!hasLayer" type="button" class="btn sm" :disabled="!!createReason" @click="emit('create')">{{ createLabel }}</button>
    <p v-else-if="!sets.length" class="empty">No sets yet.</p>

    <article v-for="set in sets" :key="set.id" class="set" :class="{ collapsed: set.collapsed && !filter.trim() }">
      <header class="set-head">
        <input
          type="checkbox" class="check-box" :checked="stateOf(set.rules) === 'on'"
          :ref="el => setIndeterminate(el, stateOf(set.rules))"
          :disabled="!set.writable || set.readOnly || !set.rules.some(rule => canFlip(rule, heldSet()))"
          :aria-label="`Switch rules in ${set.name}`"
          @change="onSetToggle(set)"
        >
        <input v-if="set.writable && !set.readOnly" class="field name-field" :value="set.name" :aria-label="`Name of ${set.name}`" @change="emit('rename', set.id, ($event.target as HTMLInputElement).value)">
        <h3 v-else class="set-title">{{ set.name }}</h3>
        <span v-if="set.dirty" class="chip teal">Draft</span>
        <span class="count">{{ set.rules.filter(rule => rule.enabled).length }}/{{ set.rules.length }}</span>
        <button type="button" class="icon-btn sm flat" :aria-expanded="!set.collapsed" :aria-label="set.collapsed ? `Expand ${set.name}` : `Collapse ${set.name}`" @click="emit('collapse', set.id)"><AppIcon name="chevron" :size="14" /></button>
      </header>
      <p v-if="set.readOnly" class="ro">Published version {{ set.versionId }} is read-only.</p>
      <p v-else-if="set.writeReason" class="ro">{{ set.writeReason }}</p>
      <p v-if="set.readOnly && set.versionNote" class="version-note">Publish note: {{ set.versionNote }}</p>
      <label class="version">Version
        <select class="field" :value="set.versionId" :aria-label="`Versions of ${set.name}`" @focus="emit('history', set.id)" @change="emit('version', set.id, ($event.target as HTMLSelectElement).value)">
          <option value="">Current draft</option>
          <option v-for="version in set.versions ?? []" :key="version.version" :value="version.version">{{ version.version }}</option>
        </select>
      </label>
      <ul v-show="!set.collapsed || !!filter.trim()" class="rules">
        <li v-for="rule in visible(set.rules)" :key="rule.identity" class="rule" :class="{ on: rule.enabled && !held.includes(rule.identity), sel: selectedId === `${set.id}:${rule.identity}`, held: held.includes(rule.identity) }">
          <span v-if="held.includes(rule.identity)" class="lock" role="img" :aria-label="`${labelOf(rule)}, locked by a higher layer and stays on`"><AppIcon name="shield" :size="14" /></span>
          <input v-else-if="rule.strength === 'locked'" type="checkbox" class="check-box" checked disabled :aria-label="`${labelOf(rule)}, locked and stays on`">
          <input v-else type="checkbox" class="check-box" :checked="rule.enabled" :disabled="!set.writable || set.readOnly" :aria-label="labelOf(rule)" @change="emit('toggle-rule', set.id, rule.identity)">
          <button type="button" class="text" @click="emit('select', set.id, rule.identity)">{{ labelOf(rule) }}</button>
          <span v-if="draftOf(set, rule) || held.includes(rule.identity) || rule.strength === 'locked' || expiryOf(rule)" class="meta">
            <span v-if="draftOf(set, rule)" class="chip teal">Draft</span>
            <span v-if="held.includes(rule.identity)" class="chip lock">Stays on</span>
            <span v-else-if="rule.strength === 'locked'" class="chip lock">Locked</span>
            <span v-if="expiryOf(rule)" class="chip exp">{{ expiryOf(rule) }}</span>
          </span>
        </li>
      </ul>
      <button v-if="set.writable && !set.readOnly && !filter.trim()" type="button" class="add" @click="emit('add-rule', set.id)"><AppIcon name="plus" :size="14" />Add rule</button>
    </article>

    <form v-if="canAdd" class="add-set" @submit.prevent="addSet">
      <input v-model="newName" class="field" aria-label="New set name" placeholder="New set" maxlength="128">
      <button type="submit" class="btn sm" :disabled="!newName.trim()">Add set</button>
    </form>
  </section>
</template>

<style scoped>
.column { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
.column-head, .set-head { display: flex; align-items: center; gap: 8px; min-width: 0; }
.column-head { padding: 10px 12px; border-radius: 12px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); }
.num { display: grid; place-items: center; width: 24px; height: 24px; border-radius: 50%; background: var(--aqua-3); color: var(--teal-ink); font-size: 12px; font-weight: 650; flex: none; }
.titles { display: grid; min-width: 0; }
.name, .set-title { font-weight: 650; }
.set-title { margin: 0; font-size: 14px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hint, .count, .ro, .empty { color: var(--ink-3); font-size: 12px; }
.hint { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.count { margin-left: auto; white-space: nowrap; }
.set { display: flex; flex-direction: column; gap: 6px; min-width: 0; padding: 8px; border-radius: 12px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); }
.set.collapsed .rules, .set.collapsed .add, .set.collapsed .version, .set.collapsed .ro, .set.collapsed .version-note { display: none; }
.name-field { flex: 1 1 8em; width: auto; min-width: 0; height: 30px; font-weight: 650; }
.set-head { flex-wrap: wrap; }
.version { display: grid; gap: 4px; color: var(--ink-2); font-size: 12px; font-weight: 600; }
.version-note { margin: 0; color: var(--ink-2); font-size: 12px; white-space: pre-wrap; overflow-wrap: anywhere; }
.rules { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; }
.rule { display: grid; grid-template-columns: 18px minmax(0, 1fr); gap: 4px 8px; align-items: start; padding: 6px; border-radius: 8px; }
.rule.on { background: color-mix(in srgb, var(--aqua) 28%, transparent); }
.rule.sel { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.rule.held { background: var(--surface-2); }
.text { grid-column: 2; min-width: 0; margin: 0; padding: 0; border: 0; background: none; color: inherit; font: inherit; text-align: left; overflow-wrap: anywhere; cursor: pointer; }
.rule:not(.on):not(.held) .text { color: var(--ink-3); }
.meta { grid-column: 2; display: flex; flex-wrap: wrap; gap: 4px; }
.lock { display: grid; place-items: center; width: 18px; height: 18px; color: var(--ink); }
.chip.lock, .chip.exp { color: var(--ink); background: var(--surface); letter-spacing: 0; text-transform: none; }
.chip.lock { box-shadow: inset 0 0 0 1px var(--danger-line); }
.chip.exp { box-shadow: inset 0 0 0 1px var(--line-2); }
.add, .add-set { display: flex; align-items: center; gap: 6px; }
.add { border: 0; background: none; color: var(--ink-3); font-size: 13px; padding: 4px 6px; border-radius: 8px; }
.add:hover { background: var(--row-hover); color: var(--teal-ink); }
.add-set { min-width: 0; }
.add-set .field { min-width: 0; }
.check-box:indeterminate { background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--teal); }
.check-box:indeterminate::after { content: ''; width: 8px; height: 2px; background: var(--teal-ink); border: 0; transform: none; }
.check-box:disabled { cursor: not-allowed; }
</style>
