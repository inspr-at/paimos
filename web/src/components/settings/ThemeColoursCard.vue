<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { ThemeRecord } from '../../lib/themes'
import { CARD_COLOURS, colourContrast, derivedDark, suggestColour, type ColourMode } from '../../lib/themeColours'
import SettingsCard from './SettingsCard.vue'
import ThemeColourPicker from './ThemeColourPicker.vue'
import AppIcon from '../AppIcon.vue'
const props = defineProps<{ draft: ThemeRecord; editable: boolean }>()
const emit = defineEmits<{ change: [draft: ThemeRecord] }>()
type AccentKey = 'primary' | 'secondary'
type Pick = { accent: AccentKey; mode: ColourMode } | { marker: true }
const picker = ref<Pick | null>(null)
let opener: HTMLElement | null = null
const accents = [{ key: 'primary' as const, label: 'Primary accent', hint: 'Buttons, links, selection, focus and progress' }, { key: 'secondary' as const, label: 'Secondary accent', hint: 'Stars, highlights, warnings and waiting work' }]
const modes: ColourMode[] = ['light', 'dark']
const markers = [{ value: 'primary' as const, label: 'Primary' }, { value: 'secondary' as const, label: 'Secondary' }, { value: 'neutral' as const, label: 'Neutral grey' }, { value: 'custom' as const, label: 'Custom' }]
const value = (key: AccentKey, mode: ColourMode) => props.draft.values[key][mode] ?? derivedDark(props.draft.values[key].light)
const ratio = (key: AccentKey, mode: ColourMode) => colourContrast(value(key, mode), CARD_COLOURS[mode])
const pickerValue = computed(() => !picker.value ? '#0e6f6c' : 'marker' in picker.value ? props.draft.values.recurring_marker.custom ?? '#8547b0' : value(picker.value.accent, picker.value.mode))
const pickerTitle = computed(() => !picker.value ? '' : 'marker' in picker.value ? 'Recurring marker · custom' : `${picker.value.accent === 'primary' ? 'Primary' : 'Secondary'} accent · ${picker.value.mode}`)
function edit(fn: (theme: ThemeRecord) => void) { if (!props.editable) return; const copy = JSON.parse(JSON.stringify(props.draft)) as ThemeRecord; fn(copy); emit('change', copy) }
function open(pick: Pick, event: MouseEvent) { if (!props.editable) return; opener = event.currentTarget as HTMLElement; picker.value = pick }
function close() { picker.value = null; void nextTick(() => opener?.focus()) }
function pick(hex: string) {
  const captured = picker.value
  if (!captured) return
  edit(theme => { if ('marker' in captured) theme.values.recurring_marker.custom = hex; else theme.values[captured.accent][captured.mode] = hex })
}
function marker(source: ThemeRecord['values']['recurring_marker']['source'], event: MouseEvent) {
  edit(theme => { theme.values.recurring_marker.source = source; if (source === 'custom' && !theme.values.recurring_marker.custom) theme.values.recurring_marker.custom = '#8547b0' })
  if (source === 'custom') open({ marker: true }, event)
}
function markerColour(mode: ColourMode) {
  const choice = props.draft.values.recurring_marker
  if (choice.source === 'primary' || choice.source === 'secondary') return value(choice.source, mode)
  if (choice.source === 'neutral') return mode === 'light' ? '#7c8c8d' : '#8aa3a2'
  return mode === 'light' ? choice.custom ?? '#8547b0' : derivedDark(choice.custom ?? '#8547b0')
}
watch([() => props.draft.id, () => props.editable], () => { picker.value = null })
</script>
<template>
  <SettingsCard title="Colours" icon="sun" anchor="colours">
    <template #lead>Primary and secondary accents, and the mark on recurring tickets. Only the preview changes until Save.</template>
    <div class="permission-note"><p><strong>{{ draft.name || 'Untitled theme' }}</strong> · {{ editable ? (draft.scope === 'workspace' ? 'Workspace theme · you manage it.' : 'Your theme · only you see it.') : draft.scope === 'workspace' ? 'Read-only workspace theme. Duplicate it to make your own.' : 'Read-only. Changing your theme needs permission to edit your profile.' }}</p></div>
    <div class="editor-grid">
      <div class="colour-controls">
        <div v-for="accent in accents" :key="accent.key" class="colour-field">
          <h3>{{ accent.label }}</h3><p class="hint">{{ accent.hint }}</p>
          <div class="swatches">
            <button v-for="mode in modes" :key="mode" type="button" class="swatch" :aria-label="`${accent.label}, ${mode}`" :disabled="!editable" @click="open({ accent: accent.key, mode }, $event)"><i :style="{ background: value(accent.key, mode) }" /><span>{{ mode === 'light' ? 'Light' : 'Dark' }}</span><code>{{ value(accent.key, mode) }}</code></button>
          </div>
          <div class="derived"><span>{{ draft.values[accent.key].dark === null ? 'Dark derived from light' : 'Dark set by hand' }}</span><button type="button" class="text-link" :disabled="!editable || draft.values[accent.key].dark === null" :aria-label="`Use derived ${accent.key} dark`" @click="edit(theme => theme.values[accent.key].dark = null)">Use derived</button></div>
          <div class="contrast-lines">
            <p v-for="mode in modes" :key="mode" :class="{ warning: ratio(accent.key, mode) < 4.5 }" :data-testid="`${accent.key}-${mode}-contrast`"><AppIcon :name="ratio(accent.key, mode) < 4.5 ? 'alert' : 'check'" :size="13" /><span>{{ mode === 'light' ? 'Light' : 'Dark' }} {{ (Math.floor(ratio(accent.key, mode) * 10) / 10).toFixed(1) }}:1 on cards<span v-if="ratio(accent.key, mode) < 4.5"> · below 4.5:1</span></span><button type="button" class="text-link suggest" :style="{ visibility: ratio(accent.key, mode) < 4.5 && editable ? 'visible' : 'hidden' }" :disabled="!editable || ratio(accent.key, mode) >= 4.5" :aria-label="`Suggest readable ${accent.key} ${mode}`" @click="edit(theme => theme.values[accent.key][mode] = suggestColour(value(accent.key, mode), mode))">Suggest</button></p>
          </div>
        </div>
        <div class="colour-field">
          <h3>Recurring marker</h3><p class="hint">The loop badge on tickets that recurring work creates</p>
          <div class="marker-choices" role="radiogroup" aria-label="Recurring marker colour"><button v-for="option in markers" :key="option.value" type="button" role="radio" :aria-checked="draft.values.recurring_marker.source === option.value" :disabled="!editable" @click="marker(option.value, $event)">{{ option.label }}</button></div>
        </div>
      </div>
      <aside class="previews" aria-label="Live colour preview">
        <p class="eyebrow">Preview · light and dark</p>
        <section v-for="mode in modes" :key="mode" class="colour-preview" :class="mode" :aria-label="`${mode} colour preview`" :style="{ '--preview-primary': value('primary', mode), '--preview-secondary': value('secondary', mode), '--preview-marker': markerColour(mode) }">
          <header><strong>{{ mode === 'light' ? 'Light' : 'Dark' }}</strong><span>Ticket list</span></header>
          <div class="preview-row"><span class="preview-key">AEON-24</span><span>Build the next small thing</span><AppIcon name="star" :size="14" class="preview-star" /></div>
          <div class="preview-row selected"><span class="preview-key">AEON-25</span><span>Weekly workspace check</span><AppIcon name="refresh" :size="14" class="preview-marker" /></div>
          <div class="preview-progress"><span>In progress</span><div><i /></div></div>
          <footer><span class="preview-link">Open ticket</span><span class="preview-secondary">Waiting for review</span></footer>
        </section>
      </aside>
    </div>
    <ThemeColourPicker v-if="picker && editable" :key="pickerTitle" :title="pickerTitle" :value="pickerValue" @change="pick" @close="close" />
  </SettingsCard>
</template>
<style scoped>
.permission-note { color: var(--ink-2); font-size: 12px; height: 54px; margin-bottom: 16px; overflow-y: auto; }.permission-note strong { color: var(--ink); }
.editor-grid { display: grid; grid-template-columns: minmax(0, 1.25fr) minmax(0, 1fr); gap: 24px; }
.colour-controls { min-width: 0; }
.colour-field { padding: 16px 0; border-top: 1px solid var(--line); }
.colour-field:first-child { padding-top: 0; border-top: 0; }
h3 { font-size: 13px; font-weight: 600; }
.hint { font-size: 12px; color: var(--ink-2); margin: 3px 0 12px; }
.swatches { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
.swatch { display: grid; grid-template-columns: 20px minmax(0, 1fr); align-items: center; gap: 2px 8px; text-align: left; padding: 8px 10px; min-height: 52px; border: 1px solid var(--line-2); border-radius: 10px; background: var(--surface-raised); color: var(--ink); }
.swatch i { width: 20px; height: 20px; border-radius: 50%; grid-row: span 2; box-shadow: inset 0 0 0 1px var(--line-2); }
.swatch span { font-size: 12px; }.swatch code { font-size: 11px; }
.derived { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 10px; min-height: 44px; font-size: 12px; color: var(--ink-2); }
.text-link { padding: 0; min-height: 28px; border: 0; background: none; color: var(--teal-ink); text-decoration: underline; text-underline-offset: 3px; font-size: 12px; }
.contrast-lines p { display: flex; align-items: center; gap: 6px; min-height: 32px; font-size: 12px; color: var(--ink-2); }
.contrast-lines p > span { flex: 1; }.contrast-lines svg { flex-shrink: 0; }.contrast-lines .warning { color: var(--warn-ink); }
.marker-choices { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px; }
.marker-choices button { min-height: 44px; border: 1px solid var(--line); background: transparent; color: var(--ink); border-radius: 8px; font-size: 12px; }
.marker-choices [aria-checked="true"] { background: var(--row-selected); border-color: var(--line-2); font-weight: 600; }
.previews { min-width: 0; display: grid; gap: 12px; align-content: start; }.previews > p { font-size: 10px; }
.colour-preview { background: #fffefa; color: #203c3d; border: 1px solid var(--line-2); border-radius: 12px; overflow: hidden; font-size: 11px; }
.colour-preview.dark { background: #1c393d; color: #edf4f0; }
.colour-preview header, .colour-preview footer { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 8px; padding: 12px; }
.colour-preview header { border-bottom: 1px solid rgba(128,128,128,.2); }.colour-preview header span { opacity: .75; }
.preview-row { display: grid; grid-template-columns: auto minmax(0, 1fr) 14px; gap: 8px; padding: 12px; border-bottom: 1px solid rgba(128,128,128,.2); }
.preview-row.selected { background: color-mix(in srgb, var(--preview-primary) 12%, transparent); }
.preview-key { opacity: .75; font-size: 10px; }.preview-star,.preview-secondary { color: var(--preview-secondary); }.preview-marker { color: var(--preview-marker); }
.preview-progress { padding: 12px; color: var(--preview-primary); }.preview-progress > div { margin-top: 6px; height: 4px; background: rgba(128,128,128,.2); border-radius: 4px; }.preview-progress i { display: block; width: 65%; height: 100%; background: var(--preview-primary); border-radius: 4px; }.preview-link { color: var(--preview-primary); }
@container (max-width: 640px) { .editor-grid { grid-template-columns: minmax(0, 1fr); } .previews { grid-template-columns: repeat(2, minmax(0, 1fr)); } .previews > p { grid-column: 1 / -1; } }
@media (max-width: 600px) { .editor-grid, .previews { grid-template-columns: minmax(0, 1fr); }.text-link { min-height: 44px; }.contrast-lines p { min-height: 44px; } }
@media (pointer: coarse) { .text-link { min-height: 44px; }.contrast-lines p { min-height: 44px; } }
</style>
