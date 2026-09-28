<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'
import { HARNESS_LABEL, HARNESSES, ROLE_LABEL, ROLES, type AgentRule, type HarnessName, type RoleName, type RulePatch } from '../../lib/rules'

const props = defineProps<{
  layer: string
  setName: string
  rule: AgentRule
  readOnly: boolean
  lockNote: string
  reset: { show: boolean; available: boolean; reason: string }
  deleteReason: string | null
}>()
const emit = defineEmits<{
  close: []
  change: [patch: RulePatch]
  duplicate: []
  remove: []
  reset: []
}>()

const localStamp = (value: string | null | undefined) => {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const part = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${part(date.getMonth() + 1)}-${part(date.getDate())}T${part(date.getHours())}:${part(date.getMinutes())}`
}
function expiry(value: string) {
  if (!value) { emit('change', { expires_at: null }); return }
  const date = new Date(value)
  if (!Number.isNaN(date.getTime())) emit('change', { expires_at: date.toISOString() })
}
function toggleRole(role: RoleName) {
  const roles = new Set(props.rule.roles ?? [])
  if (roles.has(role)) roles.delete(role)
  else roles.add(role)
  emit('change', { roles: ROLES.filter(item => roles.has(item)) })
}
function toggleHarness(harness: HarnessName) {
  const harnesses = new Set(props.rule.harnesses ?? [])
  if (harnesses.has(harness)) harnesses.delete(harness)
  else harnesses.add(harness)
  emit('change', { harnesses: HARNESSES.filter(item => harnesses.has(item)) })
}
</script>

<template>
  <aside class="detail" aria-labelledby="rule-detail-title">
    <header class="head">
      <div>
        <p class="crumb">{{ layer }} · {{ setName }}</p>
        <h2 id="rule-detail-title">Rule</h2>
      </div>
      <button type="button" class="icon-btn sm flat" aria-label="Close rule" @click="emit('close')"><AppIcon name="close" :size="16" /></button>
    </header>
    <div class="body">
      <label class="fld">Rule
        <textarea class="field text" rows="3" :value="rule.text" :readonly="readOnly" data-autofocus @change="emit('change', { text: ($event.target as HTMLTextAreaElement).value })"></textarea>
        <span class="hint">One line. This is what agents read.</span>
      </label>
      <fieldset class="fld">
        <legend>Strength</legend>
        <div class="seg">
          <button type="button" :aria-pressed="rule.strength === 'normal'" :disabled="readOnly" @click="emit('change', { strength: 'normal' })">Normal</button>
          <button type="button" :aria-pressed="rule.strength === 'locked'" :disabled="readOnly" @click="emit('change', { strength: 'locked' })"><AppIcon name="shield" :size="13" />Locked</button>
        </div>
        <span class="hint">A locked rule stays on. Lower layers cannot switch it off.</span>
      </fieldset>
      <label class="fld">Why
        <textarea class="field" rows="2" :value="rule.why" :readonly="readOnly" @change="emit('change', { why: ($event.target as HTMLTextAreaElement).value })"></textarea>
      </label>
      <label class="fld">Details
        <textarea class="field" rows="4" :value="rule.details ?? ''" :readonly="readOnly" @change="emit('change', { details: ($event.target as HTMLTextAreaElement).value })"></textarea>
        <span class="hint">Loaded on demand. The merged file does not include details.</span>
      </label>
      <div class="pair">
        <label class="fld">Expires
          <input class="field" type="datetime-local" :value="localStamp(rule.expires_at)" :disabled="readOnly || rule.strength === 'locked'" @change="expiry(($event.target as HTMLInputElement).value)">
        </label>
        <label class="fld">Identity
          <input class="field" :value="rule.identity" :readonly="readOnly" spellcheck="false" @change="emit('change', { identity: ($event.target as HTMLInputElement).value.trim() })">
        </label>
      </div>
      <p v-if="lockNote" class="hint">{{ lockNote }}</p>
      <label class="fld">Source
        <input class="field" :value="rule.source.reference" :readonly="readOnly" @change="emit('change', { source: { reference: ($event.target as HTMLInputElement).value } })">
      </label>
      <div class="pair">
        <label class="fld">Source revision
          <input class="field" :value="rule.source.revision ?? ''" :readonly="readOnly" @change="emit('change', { source: { revision: ($event.target as HTMLInputElement).value } })">
        </label>
        <label class="fld">Upstream identity
          <input class="field" :value="rule.source.identity ?? ''" :readonly="readOnly" @change="emit('change', { source: { identity: ($event.target as HTMLInputElement).value } })">
        </label>
      </div>
      <p v-if="rule.source.edited_here" class="hint">Edited here.</p>
      <fieldset class="fld">
        <legend>Roles</legend>
        <div class="tags">
          <button v-for="role in ROLES" :key="role" type="button" class="tag" :aria-pressed="(rule.roles ?? []).includes(role)" :disabled="readOnly" @click="toggleRole(role)">{{ ROLE_LABEL[role] }}</button>
        </div>
      </fieldset>
      <fieldset class="fld">
        <legend>Harnesses</legend>
        <div class="tags">
          <button v-for="harness in HARNESSES" :key="harness" type="button" class="tag" :aria-pressed="(rule.harnesses ?? []).includes(harness)" :disabled="readOnly" @click="toggleHarness(harness)">{{ HARNESS_LABEL[harness] }}</button>
        </div>
      </fieldset>
      <div v-if="reset.show" class="origin">
        <p>{{ reset.reason }}</p>
        <button type="button" class="btn sm" :disabled="!reset.available || readOnly" @click="emit('reset')">Reset to template</button>
      </div>
    </div>
    <footer class="foot">
      <button type="button" class="btn sm danger" :disabled="!!deleteReason || readOnly" @click="emit('remove')">Delete</button>
      <button type="button" class="btn sm ghost" :disabled="readOnly" @click="emit('duplicate')">Duplicate</button>
      <p v-if="deleteReason" class="hint">{{ deleteReason }}</p>
    </footer>
  </aside>
</template>

<style scoped>
.detail { display: flex; flex-direction: column; min-width: 0; min-height: 0; background: var(--surface); box-shadow: -1px 0 0 var(--line); }
.head, .foot { display: flex; align-items: center; gap: 8px; padding: 12px 14px; }
.head { border-bottom: 1px solid var(--line); }
.foot { border-top: 1px solid var(--line); flex-wrap: wrap; }
.head .icon-btn { margin-left: auto; }
.crumb { margin: 0; color: var(--ink-3); font-size: 12px; }
.head h2 { margin: 2px 0 0; font-size: 16px; }
.body { display: flex; flex-direction: column; gap: 12px; padding: 14px; overflow: auto; }
.fld { display: grid; gap: 5px; margin: 0; padding: 0; border: 0; color: var(--ink-2); font-size: 12px; font-weight: 650; min-width: 0; }
.fld .field { font-weight: 450; color: var(--ink); }
textarea.field { height: auto; padding: 8px 12px; resize: vertical; line-height: 1.45; }
.text { font-size: 15px; font-weight: 600; }
.hint, .origin p { margin: 0; color: var(--ink-3); font-size: 12px; font-weight: 450; }
.pair { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.tags { display: flex; flex-wrap: wrap; gap: 6px; }
.tag { height: 28px; padding: 0 10px; border-radius: 999px; border: 1px solid var(--line-2); background: var(--surface); color: var(--ink-2); font-size: 12px; font-weight: 600; }
.tag[aria-pressed="true"] { background: var(--aqua-3); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.origin { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 8px 10px; border-radius: 10px; background: var(--surface-2); }
.foot .hint { flex-basis: 100%; }
@media (max-width: 800px) {
  .detail { width: min(440px, 100%); height: 100%; box-shadow: var(--shadow-pop); }
  .pair { grid-template-columns: 1fr; }
}
</style>
