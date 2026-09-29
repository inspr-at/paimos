<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, useId } from 'vue'
import BizIcon from '../business/BizIcon.vue'
import { HARNESS_LABEL, HARNESSES, ROLE_LABEL, ROLES, resetAvailability, resetRule, ruleSwitchLabel, touchRule, type AgentRule, type HarnessName, type RoleName, type RulePatch } from '../../lib/rules'

// One rule while its set is being edited: the text and its reason in view, the
// on/off switch and the lock beside them, everything rarer under "More".
const props = defineProps<{ rule: AgentRule; heldBy?: string; canLock: boolean; lockReason?: string; original?: AgentRule | null; setName?: string }>()
const emit = defineEmits<{ change: [rule: AgentRule]; remove: []; duplicate: [] }>()
const reset = computed(() => resetAvailability(props.rule, props.original))
const resetReady = computed(() => reset.value.available && props.rule.source.edited_here)
const resetNote = computed(() => reset.value.available || !reset.value.show ? '' : reset.value.reason)
function applyReset() {
  if (!props.original) return
  const next = resetRule(props.rule, props.original)
  if (next) emit('change', next)
}
const id = useId()
const locked = computed(() => props.rule.strength === 'locked')
const label = computed(() => props.rule.text.trim() || 'New rule')
const switchLabel = computed(() => ruleSwitchLabel(props.rule.text, props.setName ?? '', props.rule.enabled || props.rule.strength === 'locked', 'New rule'))

function patch(change: RulePatch) { emit('change', touchRule(props.rule, change)) }
const oneLine = (value: string) => value.replace(/[\r\n\u2028\u2029]+/g, ' ')
function localStamp(value: string | null | undefined) {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const part = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${part(date.getMonth() + 1)}-${part(date.getDate())}T${part(date.getHours())}:${part(date.getMinutes())}`
}
function expiry(value: string) {
  if (!value) { patch({ expires_at: null }); return }
  const date = new Date(value)
  if (!Number.isNaN(date.getTime())) patch({ expires_at: date.toISOString() })
}
function toggleRole(role: RoleName) {
  // An empty list means every role; a click takes one out or puts it back, never all.
  const roles = new Set(props.rule.roles?.length ? props.rule.roles : ROLES)
  if (roles.has(role)) roles.delete(role)
  else roles.add(role)
  if (!roles.size) return
  patch({ roles: ROLES.filter(item => roles.has(item)) })
}
function toggleHarness(harness: HarnessName) {
  const harnesses = new Set(props.rule.harnesses?.length ? props.rule.harnesses : HARNESSES)
  if (harnesses.has(harness)) harnesses.delete(harness)
  else harnesses.add(harness)
  if (!harnesses.size) return
  patch({ harnesses: HARNESSES.filter(item => harnesses.has(item)) })
}
</script>

<template>
  <li class="edit-rule" :aria-label="label">
    <div class="top">
      <label class="switch" :data-tip="locked ? 'Locked rules are always on.' : heldBy ? `Locked in ${heldBy} rules.` : undefined">
        <input type="checkbox" :checked="rule.enabled || locked" :disabled="locked || !!heldBy" :aria-label="switchLabel" @change="patch({ enabled: ($event.target as HTMLInputElement).checked })">
      </label>
      <div class="texts">
        <textarea class="field text" rows="1" maxlength="512" :value="rule.text" placeholder="What agents must do, in one line" :aria-label="`Rule text`" data-autofocus @input="patch({ text: oneLine(($event.target as HTMLTextAreaElement).value) })"></textarea>
        <textarea class="field why" rows="1" :value="rule.why" maxlength="1024" placeholder="Why, in one sentence" aria-label="Why" @input="patch({ why: oneLine(($event.target as HTMLTextAreaElement).value) })"></textarea>
        <button v-if="resetReady" type="button" class="reset" @click="applyReset">Reset to template</button>
        <p v-else-if="resetNote" class="reset-note">{{ resetNote }}</p>
      </div>
      <div class="side">
        <button
          type="button" class="icon-btn sm flat lock-btn" :aria-pressed="locked" :aria-label="locked ? `Unlock ${label}` : `Lock ${label}`"
          :aria-disabled="!canLock || undefined" :data-tip="!canLock ? lockReason : locked ? 'Locked: always on, lower layers cannot switch it off. Click to unlock.' : 'Lock: always on, and lower layers cannot switch it off.'"
          @click="canLock && patch({ strength: locked ? 'normal' : 'locked' })"
        ><BizIcon name="lock" :size="14" /></button>
        <button type="button" class="icon-btn sm flat" :aria-label="`Remove ${label}`" :aria-disabled="locked || undefined" :data-tip="locked ? 'Unlock the rule to remove it.' : 'Remove from this draft'" @click="!locked && emit('remove')"><BizIcon name="trash" :size="14" /></button>
      </div>
    </div>
    <details class="more">
      <summary><BizIcon name="chevron-right" :size="12" class="chev" />More</summary>
      <div class="grid">
        <label class="fld wide">Details <span class="opt">loaded on demand, not in the session file</span>
          <textarea class="field" rows="3" :value="rule.details ?? ''" @input="patch({ details: ($event.target as HTMLTextAreaElement).value })"></textarea>
        </label>
        <label class="fld">Source <span class="opt">ticket or incident</span>
          <input class="field" :value="rule.source.reference" maxlength="512" @input="patch({ source: { reference: oneLine(($event.target as HTMLInputElement).value) } })">
        </label>
        <label class="fld">Expires <span class="opt">optional</span>
          <input class="field" type="datetime-local" :value="localStamp(rule.expires_at)" :disabled="locked" @change="expiry(($event.target as HTMLInputElement).value)">
        </label>
        <fieldset class="fld">
          <legend>Roles</legend>
          <div class="tags">
            <button v-for="role in ROLES" :key="role" type="button" class="tag" :aria-pressed="!(rule.roles ?? []).length || (rule.roles ?? []).includes(role)" @click="toggleRole(role)">{{ ROLE_LABEL[role] }}</button>
          </div>
        </fieldset>
        <fieldset class="fld">
          <legend>Harnesses</legend>
          <div class="tags">
            <button v-for="harness in HARNESSES" :key="harness" type="button" class="tag" :aria-pressed="!(rule.harnesses ?? []).length || (rule.harnesses ?? []).includes(harness)" @click="toggleHarness(harness)">{{ HARNESS_LABEL[harness] }}</button>
          </div>
        </fieldset>
        <label class="fld">ID
          <input :id="`${id}-identity`" class="field mono" :value="rule.identity" spellcheck="false" maxlength="96" @change="patch({ identity: ($event.target as HTMLInputElement).value.trim() })">
        </label>
        <div class="fld wide">
          <button type="button" class="btn sm ghost" @click="emit('duplicate')">Duplicate rule</button>
        </div>
      </div>
    </details>
  </li>
</template>

<style scoped>
.edit-rule { display: flex; flex-direction: column; gap: 4px; padding: 10px 2px 8px; }
.edit-rule + .edit-rule { border-top: 1px solid var(--line); }
.top { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; grid-template-areas: "switch texts side"; gap: 10px; align-items: start; }
.switch { grid-area: switch; }
.texts { grid-area: texts; }
.side { grid-area: side; }
.switch { padding-top: 7px; }
.texts { display: grid; gap: 6px; min-width: 0; }
.reset { justify-self: start; padding: 0; border: 0; background: none; color: var(--teal-ink); font-size: 12.5px; font-weight: 650; }
.reset-note { margin: 0; color: var(--ink-3); font-size: 12.5px; line-height: 1.4; }
textarea.field { height: auto; padding: 7px 11px; resize: vertical; line-height: 1.45; }
textarea.text { field-sizing: content; min-height: 36px; resize: none; }
.text { font-size: 14px; }
textarea.why { field-sizing: content; min-height: 30px; padding: 5px 11px; resize: none; font-size: 13px; color: var(--ink-2); }
.side { display: flex; gap: 2px; }
.lock-btn[aria-pressed="true"] { color: var(--teal-ink); background: var(--row-selected); }
[aria-disabled="true"] { opacity: .45; cursor: not-allowed; }
.more { margin-left: 44px; }
.more summary { display: inline-flex; align-items: center; gap: 4px; color: var(--ink-3); font-size: 12.5px; font-weight: 600; cursor: pointer; list-style: none; border-radius: 6px; }
.more summary::-webkit-details-marker { display: none; }
.more[open] .chev { transform: rotate(90deg); }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-top: 8px; }
.fld { display: grid; gap: 4px; min-width: 0; margin: 0; padding: 0; border: 0; color: var(--ink-2); font-size: 12px; font-weight: 650; }
.fld .field { font-weight: 450; color: var(--ink); }
.fld.wide { grid-column: 1 / -1; }
.opt { color: var(--ink-3); font-weight: 450; }
.mono { font-family: var(--mono); font-size: 12.5px; }
.tags { display: flex; flex-wrap: wrap; gap: 6px; }
.tag { height: 26px; padding: 0 10px; border-radius: 999px; border: 1px solid var(--line-2); background: var(--surface); color: var(--ink-3); font-size: 12px; font-weight: 600; }
.tag[aria-pressed="true"] { background: var(--chip-teal-bg); color: var(--teal-ink); border-color: var(--chip-teal-line); }
@media (max-width: 600px) {
  .top { grid-template-columns: auto minmax(0, 1fr) auto; grid-template-areas: "switch . side" "texts texts texts"; row-gap: 4px; }
  .switch { padding-top: 4px; }
  .more { margin-left: 0; }
  .grid { grid-template-columns: 1fr; }
}
</style>
