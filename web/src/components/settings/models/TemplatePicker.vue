<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../../AppIcon.vue'
import HoverText from './HoverText.vue'
import type { BoardProfile } from '../../../lib/modelsBoard'
const props = defineProps<{ value: BoardProfile['template']; own: string[]; inherited: boolean; compact?: boolean; editable: boolean; busy: boolean; german: boolean }>()
const emit = defineEmits<{ choose: [template: NonNullable<BoardProfile['template']>, event: MouseEvent] }>()
const text = (en: string, de: string) => props.german ? de : en
const templates = [
  { id: 'best' as const, icon: 'star' as const, label: ['Best quality', 'Beste Qualität'], description: ['The strongest models first, Astra and Opus included. Costs more.', 'Die stärksten Modelle zuerst, auch Astra und Opus. Kostet mehr.'] },
  { id: 'balanced' as const, icon: 'gauge' as const, label: ['Balanced', 'Ausgewogen'], description: ['Strong everyday models first; Astra and Opus further down, except in design and concepts.', 'Starke Alltagsmodelle zuerst; Astra und Opus weiter unten, außer bei Design und Konzepten.'] },
  { id: 'save' as const, icon: 'leaf' as const, label: ['Save tokens', 'Tokens sparen'], description: ['Cheaper models first; the strongest at the bottom.', 'Günstigere Modelle zuerst; die stärksten ganz unten.'] },
]
</script>
<template>
  <div class="templates" :class="{ compact }">
    <div class="template-row" role="group" :aria-label="text('Template', 'Vorlage')" data-template-group>
      <template v-for="item in templates" :key="item.id">
        <button v-if="editable" type="button" class="template" :aria-pressed="value === item.id" :disabled="busy" :data-template="item.id" @click="emit('choose', item.id, $event)"><AppIcon :name="item.icon" :size="compact ? 14 : 18" /><span><b>{{ text(...item.label as [string, string]) }}</b><small v-if="!compact">{{ text(...item.description as [string, string]) }}</small></span></button>
        <div v-else class="template" :class="{ selected: value === item.id }"><AppIcon :name="item.icon" :size="compact ? 14 : 18" /><span><b>{{ text(...item.label as [string, string]) }}</b><small v-if="!compact">{{ text(...item.description as [string, string]) }}</small></span></div>
      </template>
    </div>
    <HoverText class="template-note" :text="`${inherited ? text('You follow the workspace default. ', 'Sie folgen der Vorgabe des Arbeitsbereichs. ') : ''}${own.length ? text(`${own.length} columns keep your own order: ${own.join(', ')}.`, `${own.length} Spalten behalten Ihre eigene Reihenfolge: ${own.join(', ')}.`) : text('Every column follows the template.', 'Jede Spalte folgt der Vorlage.')}`" />
  </div>
</template>
<style scoped>
.templates { display: grid; gap: 8px; min-width: 0; }.template-row { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; }.template { display: grid; grid-template-columns: 22px minmax(0, 1fr); gap: 12px; align-content: start; min-height: 104px; padding: 16px 18px; border: 0; border-radius: 14px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink); text-align: left; font: inherit; }.template svg { margin-top: 2px; color: var(--ink-3); }.template b { display: block; font-size: 16px; font-weight: 650; }.template small { display: block; margin-top: 3px; font-size: 12.5px; line-height: 1.45; color: var(--ink-2); }.template[aria-pressed="true"], .template.selected { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }.template-note { height: 20px; font-size: 12px; color: var(--ink-3); }.template:focus-visible { outline: none; box-shadow: var(--focus-ring); }.compact .template { min-height: 44px; padding: 6px 10px; gap: 8px; grid-template-columns: 16px minmax(0, 1fr); align-items: center; }.compact .template b { font-size: 13px; }
@media (hover: hover) { button.template:hover:not(:disabled) { background: var(--row-hover); } }
@container body (max-width: 580px) { .template-row { grid-template-columns: minmax(0, 1fr); }.template { min-height: 104px; }.compact .template { min-height: 44px; } }
</style>
