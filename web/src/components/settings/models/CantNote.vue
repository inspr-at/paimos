<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from '../../AppIcon.vue'
import HarnessMark from '../../agents/HarnessMark.vue'
import { vClipTip } from '../../../directives/clipTip'
import { lineName } from '../../../lib/modelsBoard'
const props = defineProps<{ items: { line: string; reason: string }[]; german: boolean }>()
const groups = computed(() => {
  const result = new Map<string, typeof props.items>()
  for (const item of props.items) {
    const label = /tools/i.test(item.reason) ? props.german ? 'Keine Werkzeuge in PAIMOS' : 'No tools in PAIMOS'
      : /review|qualified/i.test(item.reason) ? props.german ? 'Nicht für Prüfungen qualifiziert' : 'Not qualified to review' : item.reason
    result.set(label, [...(result.get(label) ?? []), item])
  }
  return [...result.entries()]
})
const harness = (line: string) => ({ openai: 'codex', anthropic: 'claude', xai: 'grok' })[line.split(':')[0] ?? ''] ?? ''
</script>
<template><div v-if="items.length" class="cant" :aria-label="german ? 'Kann diese Arbeit hier nicht' : 'Can’t do this here'"><template v-for="[label, cards] in groups" :key="label"><b class="cant-h"><AppIcon name="wrench" :size="12" /><span v-clip-tip="`${cards[0]?.reason}. ${german ? 'Eine Fähigkeit, keine Regel.' : 'A capability, not a rule.'}`">{{ label }}</span></b><p v-for="item in cards" :key="item.line" class="cant-r"><HarnessMark :harness="harness(item.line)" :size="14" /><span v-clip-tip="`${lineName({ line: item.line, version: '' })}: ${item.reason}`">{{ lineName({ line: item.line, version: '' }) }}</span></p></template></div></template>
<style scoped>.cant { margin-top: auto; display: grid; gap: 4px; padding: 8px 2px 0; border-top: 1px dashed var(--line-2); font-size: 11.5px; line-height: 16px; color: var(--ink-3); }.cant-h, .cant-r { display: flex; align-items: center; gap: 6px; min-width: 0; margin: 0; }.cant-h { color: var(--ink-2); }.cant-r span, .cant-h span { margin: 0; min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }.cant-r svg { flex: none; }</style>
