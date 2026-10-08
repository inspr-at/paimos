<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import AppIcon from '../../AppIcon.vue'
import SettingsPopover from '../SettingsPopover.vue'
import { vClipTip } from '../../../directives/clipTip'
import { getSituationLimits, type SituationLimits } from '../../../lib/workKinds'
import { situationDefinition, textForKinds } from '../../../lib/workKindsCopy'
import type { BoardSituation } from '../../../lib/modelsBoard'
const props = defineProps<{ value: BoardSituation; german: boolean; contextKey: string }>()
const emit = defineEmits<{ choose: [value: BoardSituation] }>()
const router = useRouter()
const open = ref(false), anchor = ref<HTMLElement | null>(null), limits = ref<SituationLimits | null>(null), error = ref('')
const text = (en: string, de: string) => props.german ? de : en
const t = computed(() => textForKinds(props.german))
const name = (value: BoardSituation) => value === 'fix' && limits.value ? text(`Fix rounds 1–${limits.value.fix_rounds}`, `Korrekturrunden 1–${limits.value.fix_rounds}`) : t.value(value)
const definition = (value: BoardSituation) => limits.value ? situationDefinition(value, limits.value.fix_rounds, props.german) : error.value || t.value('loading')
const values: BoardSituation[] = ['first', 'fix', 'stuck']
const items = computed(() => [...values.map(value => ({ id: value, label: name(value), detail: definition(value), icon: props.value === value ? 'check' as const : 'tree' as const })), { id: 'definitions', label: text('Definitions', 'Definitionen'), detail: text('Review gate and Concept on request are their own columns.', 'Prüf-Gate und Konzept auf Anfrage sind eigene Spalten.'), separated: true, icon: 'book' as const }])
let generation = 0, abort: AbortController | undefined
watch(() => props.contextKey, async () => {
  const turn = ++generation
  abort?.abort(); abort = new AbortController(); limits.value = null; error.value = ''; open.value = false
  try { const result = await getSituationLimits(abort.signal); if (turn === generation) limits.value = result }
  catch { if (turn === generation) error.value = t.value('limitsError') }
}, { immediate: true })
function choose(value: string, key?: string) { if (key !== props.contextKey) return; if (value === 'definitions') void router.push('/settings/kinds#k-sits'); else if (values.includes(value as BoardSituation)) emit('choose', value as BoardSituation) }
onBeforeUnmount(() => { generation++; abort?.abort() })
</script>
<template>
  <span class="situation-picker"><span>{{ text('In this situation', 'In dieser Situation') }}</span><button type="button" class="situation-btn" data-board-situation aria-haspopup="menu" :aria-label="`${text('In this situation', 'In dieser Situation')}: ${name(value)}`" :data-tip="definition(value)" @click="anchor = $event.currentTarget as HTMLElement; open = true"><span class="select-value"><span v-clip-tip="definition(value)">{{ name(value) }}</span><span v-for="label in [...values.map(name), text('Fix rounds 1–6', 'Korrekturrunden 1–6')]" :key="label" class="reserve" aria-hidden="true">{{ label }}</span></span><AppIcon name="chevron" :size="14" /></button></span>
  <SettingsPopover v-model:open="open" :anchor="anchor" :label="text('In this situation', 'In dieser Situation')" :items="items" :context-key="contextKey" @select="choose" />
</template>
<style scoped>
.situation-picker { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 8px; min-width: 0; font-size: 12.5px; color: var(--ink-2); }.situation-btn { display: inline-flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 32px; max-width: 100%; padding: 0 10px; border: 1px solid var(--line); border-radius: 8px; background: var(--surface-raised); color: var(--ink); font-size: 13px; }.situation-btn svg { flex: none; }.select-value { display: grid; min-width: 0; text-align: left; }.select-value > span { grid-area: 1 / 1; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }.reserve { visibility: hidden; pointer-events: none; }
:global(.popover[aria-label="In this situation"] .mi), :global(.popover[aria-label="In dieser Situation"] .mi) { min-height: 7em; grid-template-rows: 20px 1fr; align-content: start; }
@media (max-width: 860px), (pointer: coarse) { .situation-btn { min-height: 44px; } }
</style>
