<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import AppIcon from '../../AppIcon.vue'
import SettingsPopover from '../SettingsPopover.vue'
import { vClipTip } from '../../../directives/clipTip'
import type { BoardColumn, ThinkingWord } from '../../../lib/modelsBoard'
const props = defineProps<{ column: BoardColumn; editable: boolean; busy: boolean; german: boolean; contextKey: string }>()
const emit = defineEmits<{ choose: [value: ThinkingWord | null] }>()
const open = ref(false), anchor = ref<HTMLElement | null>(null)
const text = (en: string, de: string) => props.german ? de : en
const words = [['lean', 'Lean', 'Knapp'], ['standard', 'Standard', 'Standard'], ['deep', 'Deep', 'Gründlich'], ['max', 'Max', 'Maximal']] as const
const word = computed(() => { const item = words.find(item => item[0] === props.column.thinking.word)!; return text(item[1], item[2]) })
const review = computed(() => props.column.column.startsWith('review:'))
const auto = computed(() => !props.column.thinking.from_column)
const hint = computed(() => review.value ? text('xhigh, always', 'immer xhigh') : auto.value ? text('Thinking, auto from the knob', 'Denken, auto vom Regler') : text('Thinking, set for this column', 'Denken, für diese Spalte gesetzt'))
const items = computed(() => [{ id: 'auto', label: text('Auto, from the knob', 'Auto, vom Regler'), icon: !props.column.thinking.own ? 'check' as const : 'sparkle' as const }, ...words.map(([id, en, de]) => ({ id, label: text(en, de), detail: `Codex ${{ lean: 'medium', standard: 'high', deep: 'xhigh', max: 'xhigh' }[id]} · Claude ${{ lean: 'medium', standard: 'high', deep: 'xhigh', max: 'max' }[id]}`, icon: props.column.thinking.own === id ? 'check' as const : 'sparkle' as const }))])
function choose(value: string, key?: string) {
  if (key !== props.contextKey || !props.editable || props.busy || review.value) return
  if (value === 'auto') emit('choose', null)
  else if (words.some(item => item[0] === value)) emit('choose', value as ThinkingWord)
}
</script>
<template>
  <div class="bc-r4"><button v-if="editable && !review" type="button" class="column-thinking" :data-column-thinking="column.column" aria-haspopup="menu" :disabled="busy" :aria-label="`${text('Thinking for', 'Denken für')} ${column.label}: ${word}${auto ? ', auto' : ''}`" :data-tip="hint" @click="anchor = $event.currentTarget as HTMLElement; open = true"><AppIcon name="sparkle" :size="12" /><span class="thinking-value"><span v-clip-tip="hint">{{ word }}</span><span v-for="[, en, de] in words" :key="en" class="reserve" aria-hidden="true">{{ text(en, de) }}</span></span><small :style="{ visibility: auto ? 'visible' : 'hidden' }" :aria-hidden="!auto">auto</small><AppIcon name="chevron" :size="12" /></button><span v-else class="column-thinking readonly" :data-column-thinking-readonly="column.column" :data-tip="hint" tabindex="0"><AppIcon name="sparkle" :size="12" /><span v-clip-tip="hint">{{ review ? text('xhigh, always', 'immer xhigh') : word }}</span><small v-if="!review" :style="{ visibility: auto ? 'visible' : 'hidden' }" :aria-hidden="!auto">auto</small></span></div>
  <SettingsPopover v-model:open="open" :anchor="anchor" :label="`${text('Thinking', 'Denken')} · ${column.label}`" :items="items" :context-key="contextKey" @select="choose" />
</template>
<style scoped>
.bc-r4 { display: flex; align-items: center; min-width: 0; }.column-thinking { display: inline-flex; align-items: center; gap: 4px; min-height: 26px; max-width: 100%; margin: 0; padding: 0 6px; border: 1px solid var(--line); border-radius: 7px; background: var(--surface-raised); color: var(--ink); font-size: 11.5px; white-space: nowrap; }.column-thinking svg { flex: none; }.column-thinking > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }.column-thinking small { font: 10px var(--mono); color: var(--ink-3); }.readonly { border-color: transparent; background: transparent; color: var(--ink-2); padding-inline: 0; }@media (max-width: 860px), (pointer: coarse) { .column-thinking { min-height: 44px; } }
.thinking-value { display: grid; }.thinking-value > span { grid-area: 1 / 1; overflow: hidden; text-overflow: ellipsis; }.reserve { visibility: hidden; pointer-events: none; }
</style>
