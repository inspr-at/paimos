<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import BizIcon from '../business/BizIcon.vue'
import MarkdownBody from '../MarkdownBody.vue'
import RuleItem from './RuleItem.vue'
import RulesDialog from './RulesDialog.vue'
import { HARNESS_LABEL, PUBLISH_NOTE_MAX, ROLE_LABEL, RULES_BUDGET, utf8Length, type AgentRule, type HarnessName, type RoleName, type RuleChange, type SetState } from '../../lib/rules'

// One meaningful approval: every set with unpublished changes, what changes in
// each, the size of the session file afterwards, an optional note, one button.
export interface PublishItem { id: string; name: string; where: string; state: SetState; rules: AgentRule[]; changes: RuleChange[] }
export interface Budget { bytes: number; role: RoleName; harness: HarnessName; project: string }
const props = defineProps<{ items: PublishItem[]; blocked: { id: string; name: string; where: string; reason: string }[]; budget: Budget | null; busy: boolean; error: string; title?: string }>()
const emit = defineEmits<{ close: []; confirm: [note: string] }>()
const note = ref('')
const noteTooLong = computed(() => utf8Length(note.value.trim()) > PUBLISH_NOTE_MAX)
const over = computed(() => !!props.budget && props.budget.bytes > RULES_BUDGET)
const meter = computed(() => props.budget ? Math.min(100, props.budget.bytes / RULES_BUDGET * 100) : 0)
const count = computed(() => props.items.length)
const label = computed(() => props.busy ? 'Publishing…' : `Publish ${count.value} ${count.value === 1 ? 'set' : 'sets'}`)
const fmt = (n: number) => n.toLocaleString('en-US')

function summary(item: PublishItem): string {
  if (item.state === 'new') return `New · ${item.rules.length} ${item.rules.length === 1 ? 'rule' : 'rules'}`
  const added = item.changes.filter(change => change.kind === 'added').length
  const changed = item.changes.filter(change => change.kind === 'changed').length
  const removed = item.changes.filter(change => change.kind === 'removed').length
  const parts = [added && `${added} added`, changed && `${changed} changed`, removed && `${removed} removed`].filter(Boolean)
  return parts.length ? parts.join(' · ') : 'Renamed'
}
const KIND = { added: 'Added', changed: 'Changed', removed: 'Removed' } as const
</script>

<template>
  <RulesDialog :title="title ?? 'Review and publish'" lede="These sets go live together. Agents receive them at their next session start." size="wide" :busy="busy" @close="emit('close')">
    <ul class="items" aria-label="Sets to publish">
      <li v-for="item in items" :key="item.id">
        <details class="item">
          <summary>
            <BizIcon name="chevron-right" :size="13" class="chev" />
            <span class="names"><span class="name">{{ item.name }}</span><span class="where">{{ item.where }}</span></span>
            <span class="what">{{ summary(item) }}</span>
          </summary>
          <ul v-if="item.state === 'new'" class="rules"><RuleItem v-for="rule in item.rules" :key="rule.identity" :rule="rule" /></ul>
          <ul v-else class="changes">
            <li v-for="change in item.changes" :key="change.kind + change.label" :class="change.kind">
              <span class="kind">{{ KIND[change.kind] }}</span>
              <MarkdownBody class="change-text" :body="change.label" />
            </li>
          </ul>
        </details>
      </li>
    </ul>

    <p v-if="blocked.length" class="blocked">
      <BizIcon name="lock" :size="13" />
      <span>{{ blocked.length }} more {{ blocked.length === 1 ? 'set waits' : 'sets wait' }} for someone who may publish {{ blocked.length === 1 ? 'it' : 'them' }}: {{ blocked.map(item => item.name).join(', ') }}.</span>
    </p>

    <p v-if="over" class="error" role="alert"><BizIcon name="alert" :size="14" /><span>The session file would be {{ fmt(budget!.bytes) }} bytes, {{ fmt(budget!.bytes - RULES_BUDGET) }} over the {{ fmt(RULES_BUDGET) }}-byte budget. Shorten rule text or move explanations into details.</span></p>

    <template #pinned>
      <label class="note"><span>Note <span class="opt">· optional, kept with every published version</span></span>
        <textarea v-model="note" class="field" rows="2" maxlength="500" placeholder="Why this goes live now"></textarea>
      </label>
      <p v-if="noteTooLong" class="error" role="alert">A note can be at most 500 bytes.</p>
      <p v-if="error" class="error" role="alert"><BizIcon name="alert" :size="14" /><span>{{ error }}</span></p>
    </template>

    <template #footer>
      <div v-if="budget" class="budget" :class="{ over }" :data-tip="`Largest session file after publishing: ${budget.project}, ${ROLE_LABEL[budget.role]}, ${HARNESS_LABEL[budget.harness]}`">
        <span class="budget-line">Session file <strong class="bytes">{{ fmt(budget.bytes) }}</strong> of {{ fmt(RULES_BUDGET) }} bytes</span>
        <span class="bar" aria-hidden="true"><i :style="{ width: `${meter}%` }"></i></span>
      </div>
      <button type="button" class="btn ghost" :disabled="busy" @click="emit('close')">Cancel</button>
      <button type="button" class="btn primary" :disabled="busy || !count || over || noteTooLong" @click="emit('confirm', note)">{{ label }}</button>
    </template>
  </RulesDialog>
</template>

<style scoped>
.items { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; border-radius: 12px; box-shadow: 0 0 0 1px var(--line); }
.items > li + li { border-top: 1px solid var(--line); }
.item summary { display: flex; align-items: center; gap: 10px; min-height: 44px; padding: 6px 12px; cursor: pointer; list-style: none; }
.item summary::-webkit-details-marker { display: none; }
@media (hover: hover) { .item summary:hover { background: var(--row-hover); } }
.item summary:focus-visible { outline: none; box-shadow: inset var(--focus-ring); }
.chev { flex: none; color: var(--ink-3); }
.item[open] .chev { transform: rotate(90deg); }
.names { display: flex; align-items: baseline; gap: 8px; min-width: 0; flex: 1; }
.name { font-size: 14px; font-weight: 600; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.where { flex: none; color: var(--ink-3); font-size: 12.5px; }
.what { flex: none; color: var(--ink-2); font-size: 12.5px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.rules { list-style: none; margin: 0; padding: 0 10px 8px 26px; }
.changes { list-style: none; margin: 0; padding: 2px 12px 10px 35px; display: flex; flex-direction: column; gap: 6px; }
.changes li { display: flex; gap: 10px; align-items: baseline; }
.kind { flex: none; width: 64px; color: var(--ink-3); font-size: 12px; font-weight: 600; }
.changes .change-text { flex: 1; min-width: 0; font-size: 13.5px; line-height: 1.5; }
.changes .change-text :deep(p) { margin: 0; }
.changes .removed .change-text :deep(p) { color: var(--ink-3); text-decoration: line-through; text-decoration-color: var(--line-2); }
.blocked { display: flex; gap: 8px; align-items: flex-start; margin: 0; color: var(--ink-2); font-size: 13px; }
.blocked svg { flex: none; margin-top: 2px; color: var(--ink-3); }
.budget { display: grid; gap: 5px; margin-right: auto; min-width: 0; }
.budget-line { color: var(--ink-3); font-size: 12.5px; white-space: nowrap; }
.budget .bar { width: 180px; height: 4px; }
.bytes { color: var(--ink); font-weight: 600; font-variant-numeric: tabular-nums; }
.over .bytes { color: var(--danger); }
.over .bar > i { background: var(--danger); box-shadow: none; }
.note { display: grid; gap: 6px; color: var(--ink-2); font-size: 12.5px; font-weight: 650; }
.note .opt { color: var(--ink-3); font-weight: 450; }
.note textarea { height: auto; padding: 8px 12px; font-weight: 450; resize: vertical; }
.error { display: flex; gap: 6px; align-items: flex-start; margin: 0; padding: 10px 12px; border-radius: 10px; background: var(--danger-bg); color: var(--danger); font-size: 13px; }
.error svg { flex: none; margin-top: 2px; }
@media (max-width: 600px) {
  .item summary { flex-wrap: wrap; row-gap: 0; }
  .names { flex-direction: column; gap: 0; }
  .what { width: 100%; padding-left: 23px; }
  .changes { padding-left: 12px; }
  .changes li { flex-direction: column; gap: 0; }
  .budget { width: 100%; }
  .budget .bar { width: 100%; }
}
</style>
