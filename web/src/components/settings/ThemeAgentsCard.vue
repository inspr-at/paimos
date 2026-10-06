<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, useId } from 'vue'
import type { ThemeRecord } from '../../lib/themes'
import type { AgentThemeAppearance } from '../../lib/agentTheme'
import { AGENT_PALETTES, type AgentPalette } from '../../lib/agentPalettes'
import { ICON_SIZE, indicatorRing, indicatorVariants, nativeIconSize, type AgentIndicatorStyle, type IndicatorRing } from '../../lib/indicatorVariants'
import type { AgentState } from '../../lib/agentSignals'
import LiveBot, { availableVariants } from '../projects/LiveBot.vue'
import SettingsCard from './SettingsCard.vue'

const props = defineProps<{ draft: ThemeRecord; editable: boolean; readOnly?: boolean }>()
const emit = defineEmits<{ change: [draft: ThemeRecord] }>()
const id = useId(), avatarGrid = ref<HTMLElement>()
const appearance = computed(() => props.draft.values.agents)
const available = new Set(availableVariants.map(variant => variant.id))
const families = [{ id: 'indicator', name: 'Indicator' }, { id: 'robot', name: 'Robots' }, { id: 'creative', name: 'Creative' }] as const
const rings: IndicatorRing[] = ['moving', 'still', 'off']
const modes = ['light', 'dark'] as const
const states: { state: AgentState; label: string }[] = [
  { state: 'working', label: 'Working' }, { state: 'waiting', label: 'Waiting' },
  { state: 'throttled', label: 'Throttled' }, { state: 'problem', label: 'Problem' }, { state: 'idle', label: 'Idle' },
]
const current = computed(() => indicatorVariants.find(variant => variant.id === appearance.value.avatar)!)
const ring = computed(() => indicatorRing(appearance.value.avatar, appearance.value.ring ?? undefined))
const size = computed(() => appearance.value.size ?? nativeIconSize(appearance.value.avatar))
const dim = computed(() => appearance.value.dim_inactive ?? true)
const opacity = computed(() => appearance.value.inactive_opacity ?? 55)
function change(patch: Partial<AgentThemeAppearance>) {
  if (!props.editable) return
  emit('change', { ...props.draft, values: { ...props.draft.values, agents: { ...appearance.value, ...patch } } })
}
function avatarKeys(event: KeyboardEvent, selected: AgentIndicatorStyle) {
  if (event.metaKey || event.ctrlKey || event.altKey) return
  const delta = { ArrowLeft: -1, ArrowUp: -1, ArrowRight: 1, ArrowDown: 1 }[event.key]
  const options = indicatorVariants.filter(variant => available.has(variant.id))
  let index = options.findIndex(variant => variant.id === selected)
  if (event.key === 'Home') index = 0
  else if (event.key === 'End') index = options.length - 1
  else if (delta !== undefined) index = (index + delta + options.length) % options.length
  else return
  event.preventDefault()
  const avatar = options[index]!.id
  change({ avatar })
  void nextTick(() => avatarGrid.value?.querySelector<HTMLButtonElement>(`[data-avatar="${avatar}"]`)?.focus())
}
function radioKeys(event: KeyboardEvent, options: readonly string[], current: string, choose: (value: string) => void) {
  if (event.metaKey || event.ctrlKey || event.altKey) return
  const delta = { ArrowLeft: -1, ArrowUp: -1, ArrowRight: 1, ArrowDown: 1 }[event.key]
  if (delta === undefined) return
  event.preventDefault()
  choose(options[(options.indexOf(current) + delta + options.length) % options.length]!)
  const group = event.currentTarget as HTMLElement
  void nextTick(() => group.querySelector<HTMLButtonElement>('[aria-checked="true"]')?.focus())
}
</script>

<template>
  <SettingsCard :title="`Agents · ${draft.name || 'Untitled theme'}`" icon="agent" anchor="agents">
    <template #lead>How every agent looks: avatar, motion, size and state colours. Agents move only while they work.</template>
    <p class="permission-note">{{ readOnly ? 'This theme is read-only. Duplicate it above to make your own.' : 'Changes stay in these previews until Save.' }}</p>
    <div class="agents-editor">
      <div class="controls">
        <fieldset class="avatar-field">
          <legend>Avatar</legend>
          <p class="hint">In lists, the live line and the dial.</p>
          <div ref="avatarGrid" class="avatars" role="radiogroup" aria-label="Agent avatar">
            <div v-for="family in families" :key="family.id" class="family">
              <p class="family-name">{{ family.name }}</p>
              <div class="tiles">
                <button v-for="variant in indicatorVariants.filter(option => option.family === family.id)" :key="variant.id"
                  type="button" role="radio" :aria-label="variant.name" :aria-checked="appearance.avatar === variant.id"
                  :tabindex="appearance.avatar === variant.id ? 0 : -1" :disabled="!editable || !available.has(variant.id)"
                  :data-avatar="variant.id" class="avatar-tile" :aria-describedby="`${id}-avatar-detail`"
                  @click="change({ avatar: variant.id })" @keydown="avatarKeys($event, variant.id)">
                  <LiveBot :indicator-style="variant.id" :theme-agents="appearance" :id="variant.id" :size="32" />
                  <span>{{ variant.name }}</span>
                </button>
              </div>
            </div>
          </div>
          <p :id="`${id}-avatar-detail`" class="detail"><strong>{{ current.name }}</strong> · {{ current.description }}</p>
        </fieldset>
        <fieldset class="motion-field">
          <legend>Motion and size</legend>
          <p class="hint">Artwork moves while working; reduced motion stills it.</p>
          <div class="control-row"><span :id="`${id}-ring`">Ring</span>
            <div class="seg" role="radiogroup" :aria-labelledby="`${id}-ring`" @keydown="radioKeys($event, rings, ring, value => change({ ring: value as IndicatorRing }))">
              <button v-for="option in rings" :key="option" type="button" role="radio" :aria-checked="ring === option" :tabindex="ring === option ? 0 : -1" :disabled="!editable" @click="change({ ring: option })">{{ option[0]!.toUpperCase() + option.slice(1) }}</button>
            </div>
          </div>
          <div class="control-row"><label :for="`${id}-hover`">Hover</label><label class="switch"><input :id="`${id}-hover`" type="checkbox" role="switch" aria-label="Hover" :checked="appearance.hover" :disabled="!editable" @change="change({ hover: ($event.target as HTMLInputElement).checked })" /><span>{{ appearance.hover ? 'On' : 'Off' }}</span></label></div>
          <div class="control-row"><label :for="`${id}-size`">Size</label><div class="range-control"><input :id="`${id}-size`" type="range" :min="ICON_SIZE.min" :max="ICON_SIZE.max" :step="ICON_SIZE.step" :value="size" :disabled="!editable" :aria-valuetext="`${size}%, ${appearance.size === null ? 'as drawn' : 'chosen size'}`" @input="change({ size: Number(($event.target as HTMLInputElement).value) })" /><output :for="`${id}-size`">{{ size }}%</output></div></div>
          <div class="drawn-row"><button type="button" class="text-link" :disabled="!editable || (appearance.ring === null && appearance.size === null)" @click="change({ ring: null, size: null })">Use drawn ring and sizes</button></div>
          <div class="control-row"><label :for="`${id}-dim`">Dim inactive</label><label class="switch"><input :id="`${id}-dim`" type="checkbox" role="switch" aria-label="Dim inactive" :checked="dim" :disabled="!editable" @change="change({ dim_inactive: ($event.target as HTMLInputElement).checked })" /><span>{{ dim ? 'On' : 'Off' }}</span></label></div>
          <div class="control-row"><label :for="`${id}-opacity`">Opacity</label><div class="range-control"><input :id="`${id}-opacity`" type="range" min="40" max="80" :value="opacity" :disabled="!editable || !dim" @input="change({ inactive_opacity: Number(($event.target as HTMLInputElement).value) })" /><output :for="`${id}-opacity`">{{ opacity }}%</output></div></div>
        </fieldset>
        <fieldset class="palette-field">
          <legend>State colours</legend>
          <div class="palettes" role="radiogroup" aria-label="State colours" @keydown="radioKeys($event, AGENT_PALETTES.map(p => p.id), appearance.palette, value => change({ palette: value as AgentPalette }))">
            <button v-for="palette in AGENT_PALETTES" :key="palette.id" type="button" role="radio" :aria-checked="appearance.palette === palette.id" :tabindex="appearance.palette === palette.id ? 0 : -1" :disabled="!editable" :aria-describedby="`${id}-palette-detail`" @click="change({ palette: palette.id })">
              <span class="palette-dots" aria-hidden="true"><i v-for="state in ['working', 'waiting', 'throttled', 'problem']" :key="state" :style="{ background: `var(--agent-${palette.id}-${state})` }" /></span>{{ palette.id === 'standard' || palette.id === 'monochrome' ? palette.name : palette.id[0]!.toUpperCase() + palette.id.slice(1) }}
            </button>
          </div>
          <p :id="`${id}-palette-detail`" class="detail palette-detail">{{ AGENT_PALETTES.find(p => p.id === appearance.palette)?.description }}</p>
          <p class="hint">Appearance moved here from Personal settings, Agents. Heartbeat warnings and estimates stay there.</p>
        </fieldset>
      </div>
      <aside class="previews" aria-label="Agents preview">
        <p class="eyebrow">Every state · light and dark</p>
        <section v-for="mode in modes" :key="mode" class="agent-theme-preview" :class="mode" :aria-label="`${mode} agents preview`">
          <header><strong>{{ mode === 'light' ? 'Light' : 'Dark' }}</strong><span>Live agent</span></header>
          <div class="live-row"><LiveBot :theme-agents="appearance" :size="32" id="theme-live" /><div><strong>aeon-643-agentcard</strong><span>Working on AEON-643 · 12 min</span></div></div>
          <div class="states"><div v-for="item in states" :key="item.state" class="state-preview" :data-preview-state="item.state"><LiveBot :theme-agents="appearance" :state="item.state" :size="32" :id="`theme-${item.state}`" /><span>{{ item.label }}</span></div></div>
        </section>
      </aside>
    </div>
  </SettingsCard>
</template>

<style scoped>
.permission-note { font-size: 12px; color: var(--ink-2); margin-bottom: 16px; }
.agents-editor { display: grid; grid-template-columns: minmax(0, 1.25fr) minmax(0, 1fr); gap: 24px; align-items: start; }
.controls, fieldset { min-width: 0; }
fieldset { border: 0; padding: 16px 0; margin: 0; border-top: 1px solid var(--line); }
fieldset:first-child { padding-top: 0; border-top: 0; }
legend { float: left; width: 100%; padding: 0; font-size: 13px; font-weight: 600; }
.hint { clear: both; font-size: 12px; line-height: 1.5; color: var(--ink-2); padding-top: 4px; }
.avatars { clear: both; padding-top: 10px; }
.family + .family { margin-top: 10px; }
.family-name { font: 500 10px/1.5 var(--mono); text-transform: uppercase; letter-spacing: .08em; color: var(--ink-2); margin-bottom: 4px; }
.tiles { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 6px; }
.avatar-tile { display: grid; justify-items: center; align-content: center; gap: 4px; height: 68px; padding: 4px; border: 1px solid var(--line); border-radius: 8px; background: transparent; color: var(--ink); font-size: 11px; cursor: pointer; }
.avatar-tile[aria-checked="true"], .palettes [aria-checked="true"] { background: var(--row-selected); border-color: var(--line-2); box-shadow: 0 0 0 1px var(--line-2); }
button:focus-visible, input:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; }
button:disabled { cursor: default; }
.detail { height: 3em; margin-top: 8px; font-size: 12px; line-height: 1.5; color: var(--ink-2); }
.detail strong { color: var(--ink); }
.control-row { clear: both; display: grid; grid-template-columns: minmax(5.5em, 1fr) minmax(0, 2.3fr); gap: 10px; align-items: center; min-height: 48px; font-size: 12px; }
.control-row:first-of-type { margin-top: 8px; }
.seg { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); }
.seg button { min-height: 44px; padding: 0 4px; font-size: 12px; }
/* 44px is the label's hit target. The track stays the shared switch. */
.switch { justify-self: start; min-width: 44px; min-height: 44px; }.switch span { min-width: 3ch; }
.range-control { display: grid; grid-template-columns: minmax(0, 1fr) 5ch; gap: 8px; align-items: center; }
.range-control input { width: 100%; min-width: 0; height: 44px; accent-color: var(--teal); }
output { text-align: right; font-variant-numeric: tabular-nums; }
.drawn-row { min-height: 44px; display: flex; align-items: center; }
.text-link { font-size: 12px; min-height: 44px; padding: 0; background: transparent; border: 0; color: var(--teal-ink); text-decoration: underline; text-underline-offset: 3px; }
.text-link:disabled { color: var(--ink-2); }
.palettes { clear: both; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px; padding-top: 12px; }
.palettes button { min-height: 48px; display: flex; flex-wrap: wrap; gap: 6px; align-items: center; padding: 6px 8px; font-size: 12px; background: transparent; border: 1px solid var(--line); border-radius: 8px; color: var(--ink); }
.palette-dots { display: flex; gap: 3px; }.palette-dots i { width: 7px; height: 7px; border-radius: 50%; }
.palette-detail { height: 4.5em; }
.previews { min-width: 0; }.eyebrow { font-size: 10px; text-transform: uppercase; letter-spacing: .08em; color: var(--ink-2); margin-bottom: 10px; }
.agent-theme-preview { background: var(--surface); color: var(--ink); border: 1px solid var(--line); border-radius: 10px; padding: 14px; }
.agent-theme-preview + .agent-theme-preview { margin-top: 12px; }
.agent-theme-preview header { display: flex; justify-content: space-between; font-size: 12px; color: var(--ink-2); margin-bottom: 14px; }
.live-row { display: flex; gap: 10px; align-items: center; padding-bottom: 14px; border-bottom: 1px solid var(--line); }
.live-row > div { min-width: 0; display: grid; gap: 3px; }.live-row strong { font-size: 12px; overflow-wrap: anywhere; }.live-row span { color: var(--ink-2); font-size: 11px; }
.states { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 16px 6px; padding-top: 16px; }
.state-preview { display: grid; justify-items: center; gap: 6px; font-size: 10px; }
@container (max-width: 720px) { .agents-editor { grid-template-columns: minmax(0, 1fr); }.previews { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }.eyebrow { grid-column: 1 / -1; margin-bottom: 0; }.agent-theme-preview + .agent-theme-preview { margin-top: 0; } }
@container (max-width: 440px) { .previews { grid-template-columns: minmax(0, 1fr); }.palette-dots { width: 100%; }.detail { height: 4.5em; }.palette-detail { height: 4.5em; } }
</style>
