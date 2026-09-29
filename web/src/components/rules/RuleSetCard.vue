<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import BizIcon from '../business/BizIcon.vue'
import RowMenu from '../business/RowMenu.vue'
import RuleEditRow from './RuleEditRow.vue'
import RuleItem from './RuleItem.vue'
import type { RowAction } from '../../lib/rowActions'
import type { AgentRule, RuleSet, SetState } from '../../lib/rules'

// One rule set as a quiet collapsible card: its name, how many rules and locks,
// a chip only when it is not live yet, and one menu for the rarer actions.
// Editing happens in place; the card's own footer saves the draft.
export interface SetDraft { name: string; rules: AgentRule[] }
const props = defineProps<{
  set: RuleSet
  state: SetState
  subtitle?: string
  open: boolean
  held: Map<string, string>
  /** Rules whose saved text differs from the live version (only for a changed set). */
  pending?: Set<string>
  draft: SetDraft | null
  editReason: string | null
  publishReason: string | null
  lockReason: string | null
  saving: boolean
  error: string
}>()
const emit = defineEmits<{
  toggle: []
  edit: []
  cancel: []
  save: []
  publish: []
  history: []
  draft: [draft: SetDraft]
  'add-rule': []
}>()
const id = useId()
const menuAnchor = ref<HTMLElement | null>(null)
const rules = computed(() => props.draft?.rules ?? props.set.rules)
const locked = computed(() => rules.value.filter(rule => rule.strength === 'locked').length)
const summary = computed(() => {
  const count = rules.value.length
  const parts = [`${count} ${count === 1 ? 'rule' : 'rules'}`]
  if (locked.value) parts.push(`${locked.value} locked`)
  return parts.join(' · ')
})
const chip = computed(() => props.state === 'new' ? 'Draft' : props.state === 'changed' ? 'Changed' : '')
const chipTip = computed(() => props.state === 'new' ? 'Never published. Agents do not receive it yet.' : props.state === 'changed' ? 'Edited since it was published. Agents still receive the published version.' : '')
const items = computed<RowAction[]>(() => [
  { id: 'edit', label: 'Edit rules', icon: 'edit', group: 0, reason: props.editReason ?? undefined },
  { id: 'publish', label: 'Publish this set', icon: 'upload', group: 0, reason: props.state === 'live' ? 'Already live.' : props.publishReason ?? undefined },
  { id: 'history', label: 'Version history', icon: 'history', group: 1, reason: props.set.published_version ? undefined : 'Not published yet.' },
])
function select(action: string) {
  menuAnchor.value = null
  if (action === 'edit') emit('edit')
  else if (action === 'publish') emit('publish')
  else if (action === 'history') emit('history')
}
function changeRule(index: number, rule: AgentRule) {
  if (!props.draft) return
  const next = [...props.draft.rules]
  next[index] = rule
  emit('draft', { name: props.draft.name, rules: next })
}
function removeRule(index: number) {
  if (!props.draft) return
  emit('draft', { name: props.draft.name, rules: props.draft.rules.filter((_, i) => i !== index) })
}
</script>

<template>
  <article class="set" :class="{ open: open || !!draft, editing: !!draft }" :aria-labelledby="`${id}-name`">
    <header class="head">
      <button v-if="!draft" type="button" class="toggle" :aria-expanded="open" :aria-controls="`${id}-rules`" @click="emit('toggle')">
        <BizIcon name="chevron-right" :size="14" class="chev" />
        <span class="names">
          <h4 :id="`${id}-name`" class="name" :title="set.name">{{ set.name }}</h4>
          <span v-if="subtitle" class="subtitle">{{ subtitle }}</span>
          <span class="summary">{{ summary }}</span>
        </span>
      </button>
      <label v-else class="name-edit">
        <span class="sr-only">Set name</span>
        <input :id="`${id}-name`" class="field" :value="draft.name" maxlength="128" aria-label="Set name" @input="emit('draft', { name: ($event.target as HTMLInputElement).value, rules: draft.rules })">
      </label>
      <span v-if="chip" class="chip state" :data-tip="chipTip">{{ chip }}</span>
      <button v-if="!draft" type="button" class="icon-btn sm flat" :aria-label="`Actions for ${set.name}`" aria-haspopup="menu" data-tip="Edit, publish, history" @click="menuAnchor = $event.currentTarget as HTMLElement"><BizIcon name="more" :size="16" /></button>
    </header>

    <ul v-if="!draft && open" :id="`${id}-rules`" class="rules">
      <RuleItem v-for="rule in set.rules" :key="rule.identity" :rule="rule" :held-by="held.get(rule.identity)" :pending="pending?.has(rule.identity)" />
      <li v-if="!set.rules.length" class="empty">No rules in this set yet.</li>
    </ul>

    <template v-if="draft">
      <ul class="edit-rules">
        <RuleEditRow
          v-for="(rule, index) in draft.rules" :key="index" :rule="rule" :held-by="held.get(rule.identity)"
          :can-lock="!lockReason" :lock-reason="lockReason ?? undefined" @change="changeRule(index, $event)" @remove="removeRule(index)"
        />
      </ul>
      <button type="button" class="add" @click="emit('add-rule')"><BizIcon name="plus" :size="14" />Add rule</button>
      <footer class="foot">
        <p v-if="error" class="error" role="alert"><BizIcon name="alert" :size="14" /><span>{{ error }}</span></p>
        <p v-else class="hint">Saved as a draft. Agents keep the published version until you publish.</p>
        <div class="buttons">
          <button type="button" class="btn sm ghost" :disabled="saving" @click="emit('cancel')">Cancel</button>
          <button type="button" class="btn sm primary" :disabled="saving" @click="emit('save')">{{ saving ? 'Saving…' : 'Save draft' }}</button>
        </div>
      </footer>
    </template>

    <RowMenu v-if="menuAnchor" :anchor="menuAnchor" :items="items" :label="`Actions for ${set.name}`" @select="select" @close="menuAnchor = null" />
  </article>
</template>

<style scoped>
.set { min-width: 0; }
.set:first-child { border-top-left-radius: 14px; border-top-right-radius: 14px; }
.set:last-child { border-bottom-left-radius: 14px; border-bottom-right-radius: 14px; }
.set.editing { background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.head { display: flex; align-items: center; gap: 8px; padding: 6px 8px 6px 6px; min-width: 0; }
.toggle { flex: 1; display: flex; align-items: center; gap: 10px; min-width: 0; min-height: 40px; padding: 0 8px; border: 0; border-radius: 10px; background: none; color: inherit; font: inherit; text-align: left; cursor: pointer; }
@media (hover: hover) { .toggle:hover { background: var(--row-hover); } }
.toggle:focus-visible { box-shadow: var(--focus-ring); outline: none; }
.chev { flex: none; color: var(--ink-3); }
.open .chev { transform: rotate(90deg); }
@media (prefers-reduced-motion: no-preference) { .chev { transition: transform .15s ease; } }
.names { display: flex; align-items: baseline; gap: 8px; min-width: 0; flex: 1 1 auto; }
.names .summary { margin-left: auto; }
.name { margin: 0; font-size: 14.5px; font-weight: 650; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.subtitle { color: var(--ink-3); font-size: 12.5px; white-space: nowrap; }
.summary { flex: none; color: var(--ink-3); font-size: 12.5px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.chip.state { flex: none; height: 20px; padding: 0 8px; font-family: var(--font); font-size: 11.5px; letter-spacing: 0; box-shadow: none; background: var(--surface-2); color: var(--ink-2); }
.name-edit { flex: 1; min-width: 0; padding: 4px 2px 4px 4px; }
.name-edit .field { height: 34px; font-weight: 650; }
.rules { list-style: none; margin: 0; padding: 0 8px 8px 30px; display: flex; flex-direction: column; }
.empty { padding: 6px 10px; color: var(--ink-3); font-size: 13px; }
.edit-rules { list-style: none; margin: 0; padding: 0 14px; display: flex; flex-direction: column; }
.add { display: inline-flex; align-items: center; gap: 6px; margin: 8px 12px 0; padding: 6px 10px; border: 0; border-radius: 8px; background: none; color: var(--teal-ink); font-size: 13px; font-weight: 600; }
@media (hover: hover) { .add:hover { background: var(--row-hover); } }
.foot { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; margin-top: 8px; padding: 10px 12px 12px; border-top: 1px solid var(--line); }
.foot .hint, .foot .error { flex: 1 1 240px; margin: 0; font-size: 12.5px; }
.hint { color: var(--ink-3); }
.error { display: flex; gap: 6px; align-items: flex-start; color: var(--danger); }
.error svg { flex: none; margin-top: 2px; }
.buttons { display: flex; gap: 8px; margin-left: auto; }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
@media (max-width: 600px) {
  .rules { padding-left: 8px; }
  .toggle { min-height: 52px; }
  .names { flex-direction: column; align-items: flex-start; gap: 1px; }
  .name { max-width: 100%; }
  .names .summary { margin-left: 0; }
  .edit-rules { padding: 4px 8px 0; }
}
</style>
