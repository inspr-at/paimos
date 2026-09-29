<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import AppIcon from '../AppIcon.vue'
import BizIcon from '../business/BizIcon.vue'
import MarkdownBody from '../MarkdownBody.vue'
import RuleTldrEditor from './RuleTldrEditor.vue'
import { HARNESS_LABEL, HARNESSES, ROLE_LABEL, ROLES, ruleSwitchLabel, type AgentRule } from '../../lib/rules'

// One rule as agents read it: the text rendered as Markdown, a lock when it is
// locked, and only the exceptions as quiet tags. Its explanation for people
// (TL;DR) sits quietly underneath. Everything technical (reason, details,
// source, identity) waits behind the row's own disclosure, where the
// explanation can be written or edited in place.
const props = defineProps<{
  rule: AgentRule; heldBy?: string; pending?: boolean; switchable?: boolean; switchDisabled?: boolean; setName?: string
  /** Saves this rule's explanation into the draft; absent when the caller may not write. */
  explain?: (value: { en: string; de?: string } | null) => Promise<string | null>
}>()
const emit = defineEmits<{ toggle: [enabled: boolean] }>()
const open = ref(false)
const editing = ref(false)
const id = useId()
const checkTip = 'The rule text changed after this explanation was written. Open the details to check it.'

const date = (value: string) => new Date(value).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' })
const only = (picked: string[] | undefined, all: readonly string[], label: (value: never) => string) =>
  picked?.length && picked.length < all.length ? picked.map(value => label(value as never)).join(', ') : ''
const roles = computed(() => only(props.rule.roles, ROLES, value => ROLE_LABEL[value]))
const harnesses = computed(() => only(props.rule.harnesses, HARNESSES, value => HARNESS_LABEL[value]))
const tags = computed(() => {
  const out: string[] = []
  if (props.pending) out.push('Not live yet')
  if (!props.rule.enabled && props.rule.strength !== 'locked') out.push('Off')
  if (props.rule.source.edited_here) out.push('Edited here')
  if (roles.value) out.push(`${roles.value} only`)
  if (harnesses.value) out.push(`${harnesses.value} only`)
  if (props.rule.expires_at) out.push(`Until ${date(props.rule.expires_at)}`)
  return out
})
const lockTip = computed(() => props.heldBy
  ? `Locked in ${props.heldBy} rules, which win over this one.`
  : props.rule.strength === 'locked' ? 'Locked: always on, and lower layers cannot switch it off.' : '')
const switchLabel = computed(() => ruleSwitchLabel(props.rule.text, props.setName ?? '', props.rule.enabled))

function onSwitch(event: Event) {
  const input = event.target as HTMLInputElement
  const next = input.checked
  input.checked = props.rule.enabled
  if (!props.switchDisabled) emit('toggle', next)
}
</script>

<template>
  <li class="rule" :class="{ off: !rule.enabled && rule.strength !== 'locked', held: !!heldBy }">
    <span class="lead">
      <label v-if="switchable" class="switch">
        <input type="checkbox" :checked="rule.enabled" :disabled="switchDisabled" :aria-label="switchLabel" @change="onSwitch">
      </label>
      <span v-else-if="lockTip" class="lock" role="img" :aria-label="heldBy ? `Locked in ${heldBy} rules` : 'Locked'" :data-tip="lockTip"><BizIcon name="lock" :size="13" /></span>
      <span v-else class="dot" aria-hidden="true"></span>
    </span>
    <div class="main">
      <div class="line">
        <MarkdownBody class="rule-text" :body="rule.text || 'Untitled rule'" />
        <span v-for="tag in tags" :key="tag" class="tag">{{ tag }}</span>
      </div>
      <p v-if="rule.tldr?.en" class="tldr" :title="rule.tldr.de ? `${rule.tldr.en} · ${rule.tldr.de}` : rule.tldr.en">
        <span class="tldr-text">{{ rule.tldr.en }}</span>
        <span v-if="rule.tldr.check" class="check" :data-tip="checkTip">Check</span>
      </p>
      <div v-if="open" :id="`${id}-more`" class="more">
        <RuleTldrEditor v-if="editing && explain" :value="rule.tldr" :save="explain" @done="editing = false" />
        <dl>
          <template v-if="!editing && (rule.tldr?.en || explain)">
            <dt>Explanation</dt>
            <dd class="explanation">
              <span v-if="rule.tldr?.en" class="words">{{ rule.tldr.en }}<span v-if="rule.tldr.de" class="faint de" lang="de">{{ rule.tldr.de }}</span></span>
              <span v-else class="faint">None yet</span>
              <button v-if="explain" type="button" class="link" @click="editing = true">{{ rule.tldr?.en ? (rule.tldr.check ? 'Check' : 'Edit') : 'Add' }}</button>
            </dd>
          </template>
          <dt>Why</dt><dd><MarkdownBody class="small-md" :body="rule.why" /></dd>
          <template v-if="rule.details"><dt>Details</dt><dd><MarkdownBody class="small-md" :body="rule.details" /></dd></template>
          <dt>Source</dt><dd>{{ rule.source.reference }}<span v-if="rule.source.edited_here" class="faint"> · edited here</span></dd>
          <dt>ID</dt><dd class="mono">{{ rule.identity }}<span v-if="rule.source.identity && rule.source.identity !== rule.identity" class="faint"> · from {{ rule.source.identity }}</span><span v-if="rule.source.revision" class="faint"> · {{ rule.source.revision }}</span></dd>
        </dl>
      </div>
    </div>
    <button type="button" class="icon-btn sm flat more-btn" :aria-expanded="open" :aria-controls="open ? `${id}-more` : undefined" :aria-label="`${open ? 'Hide' : 'Show'} details for ${rule.text || rule.identity}`" :data-tip="open ? 'Hide details' : 'Why, source and ID'" @click="open = !open">
      <AppIcon :name="open ? 'chevron-up' : 'info'" :size="14" />
    </button>
  </li>
</template>

<style scoped>
.rule { display: grid; grid-template-columns: 34px minmax(0, 1fr) 28px; gap: 2px 10px; align-items: start; padding: 7px 8px 7px 10px; border-radius: 10px; }
@media (hover: hover) { .rule:hover { background: var(--row-hover); } }
.lead { display: grid; place-items: center; width: 34px; height: 22px; }
.lead .switch { width: 34px; height: 20px; min-width: 0; min-height: 0; line-height: 0; }
.lock { display: grid; place-items: center; width: 18px; height: 18px; color: var(--teal-ink); cursor: default; }
.held .lock { color: var(--ink-3); }
.dot { width: 5px; height: 5px; border-radius: 50%; background: var(--line-2); }
.main { min-width: 0; }
.line { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 8px; }
.rule .rule-text { flex: 1 1 24ch; min-width: 0; font-size: 14px; line-height: 1.6; }
.rule .rule-text :deep(p) { margin: 0; }
.rule.off .rule-text, .rule.held .rule-text { color: var(--ink-3); }
.rule.off .rule-text :deep(p), .rule.held .rule-text :deep(p) { color: var(--ink-3); }
.tag { flex: none; padding: 1px 8px; border-radius: 999px; background: var(--surface-2); color: var(--ink-2); font-size: 11.5px; line-height: 18px; white-space: nowrap; }
.more { margin-top: 6px; padding: 10px 12px; border-radius: 10px; background: var(--surface-2); }
dl { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 6px 14px; margin: 0; font-size: 12.5px; }
dt { color: var(--ink-3); font-weight: 600; }
dd { margin: 0; min-width: 0; color: var(--ink-2); overflow-wrap: anywhere; }
.mono { font-family: var(--mono); font-size: 12px; }
.rule .small-md { font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.rule .small-md :deep(p) { margin: 0 0 .4em; color: var(--ink-2); }
.rule .small-md :deep(p:last-child) { margin-bottom: 0; }
.more-btn { color: var(--ink-3); }
.tldr { display: flex; align-items: baseline; gap: 8px; margin: 1px 0 0; min-width: 0; color: var(--ink-3); font-size: 12.5px; line-height: 1.45; }
.tldr-text { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.check { flex: none; padding: 0 7px; border-radius: 999px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-2); font-size: 11px; font-weight: 600; line-height: 17px; }
.more .tldr-edit { margin-bottom: 10px; }
.explanation { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 10px; }
.explanation .words { min-width: 0; }
.explanation .de { display: block; margin-top: 2px; }
.faint { color: var(--ink-3); }
.link { padding: 0; border: 0; background: none; color: var(--teal-ink); font-size: 12.5px; font-weight: 650; cursor: pointer; }
.link:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 4px; }
@media (hover: hover) { .rule:not(:hover):not(:focus-within) .more-btn[aria-expanded="false"] { opacity: .55; } }
@media (max-width: 600px) {
  .tldr-text { white-space: normal; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
  dl { grid-template-columns: minmax(0, 1fr); gap: 2px; }
  dd + dt { margin-top: 6px; }
  .lead .switch { position: relative; }
  .lead .switch::before { content: ''; position: absolute; top: 50%; left: 50%; width: 44px; height: 44px; transform: translate(-50%, -50%); }
}
</style>
