<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from '../../AppIcon.vue'
import type { BoardProfile } from '../../../lib/modelsBoard'
const props = defineProps<{ thinking: NonNullable<BoardProfile['thinking']>; usage: NonNullable<BoardProfile['usage']>; compact?: boolean; editable: boolean; busy: boolean; german: boolean }>()
const emit = defineEmits<{ thinking: [value: NonNullable<BoardProfile['thinking']>] }>()
const text = (en: string, de: string) => props.german ? de : en
const words = [['lean', 'Lean', 'Knapp'], ['standard', 'Standard', 'Standard'], ['deep', 'Deep', 'Gründlich'], ['max', 'Max', 'Maximal']] as const
const effortFor = { lean: 'medium', standard: 'high', deep: 'xhigh', max: 'xhigh' } as const
const effort = computed(() => effortFor[props.thinking])
const levelName = computed(() => { const word = words.find(item => item[0] === props.thinking)!; return text(word[1], word[2]) })
const claude = computed(() => props.thinking === 'max' ? 'max' : effort.value)
</script>
<template>
  <div class="knobs" :class="{ compact }">
    <div class="knob"><b>{{ text('Thinking', 'Denken') }}</b><div class="seg" role="group" :aria-label="text('Thinking', 'Denken')" data-thinking-group><template v-for="[id, en, de] in words" :key="id"><button v-if="editable" type="button" :data-thinking="id" :aria-pressed="thinking === id" :disabled="busy" @click="emit('thinking', id)">{{ text(en, de) }}</button><span v-else :class="{ selected: thinking === id }">{{ text(en, de) }}</span></template></div><p v-if="!compact" class="now"><span>{{ levelName }}</span><AppIcon name="arrow" :size="12" /><span class="sr-only">{{ text(' maps to ', ' entspricht ') }}</span><span>Codex {{ effort }} · Claude {{ claude }} · {{ text('Grok xhigh, its only setting', 'Grok xhigh, seine einzige Stufe') }}</span></p></div>
    <div class="knob"><b>{{ text('Usage', 'Nutzung') }}</b><div class="seg readonly" :aria-label="text('Usage · read only', 'Nutzung · nur lesen')"><span v-for="[id, en, de] in [['careful', 'Careful', 'Schonend'], ['balanced', 'Balanced', 'Ausgewogen'], ['maxout', 'Max out', 'Ausschöpfen']]" :key="id" :class="{ selected: usage === id }">{{ text(en!, de!) }}</span></div><p class="now">{{ text('Arrives with AEON-864 (release 126 or 127). Until then, Accounts and computers paces each account, as today.', 'Kommt mit AEON-864 (Release 126 oder 127). Bis dahin taktet Konten und Computer jedes Konto, wie heute.') }}</p></div>
  </div>
</template>
<style scoped>.knobs { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin-top: 16px; }.knob { display: grid; gap: 8px; align-content: start; min-width: 0; padding: 14px; border-radius: 14px; box-shadow: inset 0 0 0 1px var(--line); background: var(--surface-raised-2); }.knob > b { font-size: 14px; }.seg { display: grid; grid-auto-flow: column; grid-auto-columns: minmax(0, 1fr); border: 1px solid var(--line); border-radius: 8px; overflow: hidden; }.seg > * { display: grid; place-items: center; min-height: 36px; padding: 6px 8px; border: 0; background: transparent; color: var(--ink-2); font: inherit; font-size: 12px; }.seg [aria-pressed="true"], .seg .selected { background: var(--row-selected); color: var(--ink); font-weight: 650; }.seg button:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }.readonly { opacity: .65; }.now { display: flex; flex-wrap: wrap; align-items: center; gap: 4px; min-height: 4.5em; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }.now svg { flex: none; }.compact { margin-top: 0; gap: 12px; }.compact .knob { padding: 0; box-shadow: none; background: transparent; }.compact .knob > b { font-size: 12px; color: var(--ink-3); }.compact .now { min-height: 0; }@container body (max-width: 580px) { .knobs { grid-template-columns: minmax(0, 1fr); }.seg > * { min-height: 44px; } }</style>
