<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import AppIcon from '../AppIcon.vue'
import BizIcon from '../business/BizIcon.vue'
import MarkdownBody from '../MarkdownBody.vue'
import { HARNESS_LABEL, HARNESSES, ROLE_LABEL, ROLES, type AgentRule } from '../../lib/rules'

// One rule as agents read it: the text rendered as Markdown, a lock when it is
// locked, and only the exceptions as quiet tags. Everything technical (reason,
// details, source, identity) waits behind the row's own disclosure.
const props = defineProps<{ rule: AgentRule; heldBy?: string; pending?: boolean; switchable?: boolean; switchDisabled?: boolean }>()
const emit = defineEmits<{ toggle: [enabled: boolean] }>()
const open = ref(false)
const id = useId()

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
        <input type="checkbox" :checked="rule.enabled" :disabled="switchDisabled" :aria-label="`${rule.text.trim() || 'Untitled rule'} is on`" @change="onSwitch">
      </label>
      <span v-else-if="lockTip" class="lock" role="img" :aria-label="heldBy ? `Locked in ${heldBy} rules` : 'Locked'" :data-tip="lockTip"><BizIcon name="lock" :size="13" /></span>
      <span v-else class="dot" aria-hidden="true"></span>
    </span>
    <div class="main">
      <div class="line">
        <MarkdownBody class="rule-text" :body="rule.text || 'Untitled rule'" />
        <span v-for="tag in tags" :key="tag" class="tag">{{ tag }}</span>
      </div>
      <div v-if="open" :id="`${id}-more`" class="more">
        <dl>
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
@media (hover: hover) { .rule:not(:hover):not(:focus-within) .more-btn[aria-expanded="false"] { opacity: .55; } }
@media (max-width: 600px) {
  dl { grid-template-columns: minmax(0, 1fr); gap: 2px; }
  dd + dt { margin-top: 6px; }
  .lead .switch { position: relative; }
  .lead .switch::before { content: ''; position: absolute; top: 50%; left: 50%; width: 44px; height: 44px; transform: translate(-50%, -50%); }
}
</style>
