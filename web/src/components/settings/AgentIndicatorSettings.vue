<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { AGENT_INDICATOR_KEY, useAgentIndicator, type AgentIndicatorStyle, type IndicatorRing } from '../../lib/agentIndicator'
import { ICON_SIZE, indicatorRing, indicatorVariants, nativeIconSize, resolveIndicatorStyle } from '../../lib/indicatorVariants'
import { onPreferenceFailure } from '../../lib/preferences'
import LiveBot, { availableVariants } from '../projects/LiveBot.vue'
import AgentStateLabel from '../agents/AgentStateLabel.vue'
import { AGENT_STATE_KEY, useAgentAppearance } from '../../lib/agentAppearance'
import type { AgentPalette, AgentState } from '../../lib/agentSignals'
import { AGENT_PALETTES } from '../../lib/agentPalettes'

const { choice, setStyle, setHovering, setRing, setSize } = useAgentIndicator()
const { choice: states, save: saveStates } = useAgentAppearance()
const previewStates: AgentState[] = ['working', 'awaiting', 'waiting', 'throttled', 'problem', 'unresponsive', 'idle', 'stopped']
const stateFailed = ref(false)
onBeforeUnmount(onPreferenceFailure(key => { if (key === AGENT_STATE_KEY) stateFailed.value = true }))
function saveState(patch: Parameters<typeof saveStates>[0]) { stateFailed.value = false; saveStates(patch) }
const id = useId()
const available = new Set(availableVariants.map(variant => variant.id))
const selected = computed(() => resolveIndicatorStyle(choice.value.style, availableVariants))
const current = computed(() => indicatorVariants.find(variant => variant.id === selected.value)!)
const focused = ref<AgentIndicatorStyle>(selected.value)
watch(selected, value => { focused.value = value })
const grid = ref<HTMLElement>()
const failed = ref(false)
onBeforeUnmount(onPreferenceFailure(key => { if (key === AGENT_INDICATOR_KEY) failed.value = true }))
function retry() { failed.value = false; setStyle(choice.value.style) }
function select(style: AgentIndicatorStyle) {
  if (!available.has(style)) return
  failed.value = false
  setStyle(style)
}
// The icons wrap, so arrows move along the row order; native Enter/Space selects.
function move(event: KeyboardEvent, index: number) {
  const count = indicatorVariants.length
  const delta = { ArrowLeft: -1, ArrowUp: -1, ArrowRight: 1, ArrowDown: 1 }[event.key]
  const from = event.key === 'Home' ? -1 : event.key === 'End' ? count : index
  const step = delta ?? (event.key === 'Home' ? 1 : event.key === 'End' ? -1 : undefined)
  if (step === undefined) return
  event.preventDefault()
  for (let offset = 1; offset <= count; offset++) {
    const option = indicatorVariants[(from + step * offset + count * 2) % count]!
    if (!available.has(option.id)) continue
    focused.value = option.id
    grid.value?.querySelector<HTMLButtonElement>(`[data-variant="${option.id}"]`)?.focus()
    return
  }
}

const RINGS: { ring: IndicatorRing; label: string }[] = [
  { ring: 'moving', label: 'Moving' },
  { ring: 'still', label: 'Still' },
  { ring: 'off', label: 'Off' },
]
const ring = computed(() => indicatorRing(selected.value, choice.value.ring))
const ringGroup = ref<HTMLElement>()
function chooseRing(next: IndicatorRing) { failed.value = false; setRing(next) }
function ringKeys(event: KeyboardEvent) {
  const delta = { ArrowLeft: -1, ArrowUp: -1, ArrowRight: 1, ArrowDown: 1 }[event.key]
  if (delta === undefined) return
  event.preventDefault()
  const index = RINGS.findIndex(option => option.ring === ring.value)
  chooseRing(RINGS[(index + delta + RINGS.length) % RINGS.length]!.ring)
  void nextTick(() => ringGroup.value?.querySelector<HTMLElement>('[aria-checked="true"]')?.focus())
}

// Unset, the slider shows where the selected style is drawn; moving it sets one size for every style.
const size = computed(() => choice.value.size ?? nativeIconSize(selected.value))
const sizeText = computed(() => choice.value.size === undefined ? `${size.value}%, as drawn` : `${size.value}%`)
function chooseSize(value: number | undefined) { failed.value = false; setSize(value) }
const fill = (value: number, min: number, max: number) => ({ '--fill': `${(value - min) / (max - min) * 100}%` })
</script>

<template>
  <div class="indicator-settings">
    <fieldset class="picker">
      <legend :id="`${id}-label`">Agent indicator</legend>
      <p :id="`${id}-hint`" class="hint">Choose how your agents look while they work.</p>
      <div class="picker-body">
        <div class="picker-main">
          <div ref="grid" class="indicator-choices" role="radiogroup" :aria-labelledby="`${id}-label`" :aria-describedby="`${id}-hint`">
            <button v-for="(option, index) in indicatorVariants" :key="option.id" type="button" role="radio"
              class="indicator-choice" :class="{ selected: selected === option.id, unavailable: !available.has(option.id) }"
              :data-variant="option.id" :aria-checked="selected === option.id" :aria-disabled="!available.has(option.id)"
              :disabled="!available.has(option.id)" :tabindex="focused === option.id && available.has(option.id) ? 0 : -1"
              :title="option.name" :aria-labelledby="`${id}-${option.id}-name`" :aria-describedby="`${id}-${option.id}-description`"
              @focus="focused = option.id" @click="select(option.id)" @keydown="move($event, index)">
              <LiveBot v-if="available.has(option.id)" :indicator-style="option.id" :id="option.id" :size="30" />
              <svg v-else class="placeholder" viewBox="0 0 30 30" aria-hidden="true" focusable="false"><circle cx="15" cy="15" r="9" /><path d="M12 15h6" /></svg>
              <span :id="`${id}-${option.id}-name`" class="sr-only">{{ option.name }}</span>
              <span :id="`${id}-${option.id}-description`" class="sr-only">{{ available.has(option.id) ? option.description : 'Coming soon' }}</span>
            </button>
          </div>
          <p class="caption" data-testid="indicator-caption"><strong>{{ current.name }}</strong> · {{ current.description }}</p>
        </div>
        <div class="demo" aria-hidden="true">
          <LiveBot :indicator-style="selected" id="indicator-demo" :size="64" />
        </div>
      </div>
    </fieldset>

    <div class="setting-row">
      <div class="setting-copy">
        <p :id="`${id}-ring-label`" class="setting-label">Activity ring</p>
        <p :id="`${id}-ring-hint`" class="hint">The outline around each agent: Moving turns while it works, Still stays put, Off hides it. Separate from Hovering.</p>
      </div>
      <div ref="ringGroup" class="seg" role="radiogroup" :aria-labelledby="`${id}-ring-label`" :aria-describedby="`${id}-ring-hint`" @keydown="ringKeys">
        <button v-for="option in RINGS" :key="option.ring" type="button" role="radio" :aria-checked="ring === option.ring" :tabindex="ring === option.ring ? 0 : -1" @click="chooseRing(option.ring)">{{ option.label }}</button>
      </div>
    </div>

    <div class="setting-row slider-row">
      <div class="setting-copy">
        <label :for="`${id}-size`" class="setting-label">Icon size</label>
        <p :id="`${id}-size-hint`" class="hint">How much of the space inside the ring the icon fills. At 100% it reaches the ring.</p>
      </div>
      <div class="slider">
        <input :id="`${id}-size`" class="range" type="range" :min="ICON_SIZE.min" :max="ICON_SIZE.max" :step="ICON_SIZE.step" :value="size"
          :style="fill(size, ICON_SIZE.min, ICON_SIZE.max)" :aria-valuetext="sizeText" :aria-describedby="`${id}-size-hint`"
          @input="chooseSize(Number(($event.target as HTMLInputElement).value))" />
        <span class="slider-value">{{ size }}%</span>
        <button v-if="choice.size !== undefined" class="link-btn" type="button" @click="chooseSize(undefined)">Use drawn sizes</button>
        <span v-else class="slider-note">Each style as drawn</span>
      </div>
    </div>

    <div class="setting-row switch-row">
      <div class="setting-copy"><p :id="`${id}-hovering-label`" class="setting-label">Hovering</p><p :id="`${id}-hovering-hint`" class="hint">A gentle up-and-down motion while working. Respects reduced motion.</p></div>
      <label class="switch">
        <input type="checkbox" role="switch" :checked="choice.hovering" :aria-labelledby="`${id}-hovering-label`" :aria-describedby="`${id}-hovering-hint`" @change="failed = false; setHovering(($event.target as HTMLInputElement).checked)" />
        <span>{{ choice.hovering ? 'On' : 'Off' }}</span>
      </label>
    </div>
    <p v-if="failed" class="save-error" role="alert">Your agent indicator setting could not be saved.<button class="btn sm" type="button" @click="retry">Try again</button></p>

    <fieldset class="state-settings">
      <legend>State colours</legend>
      <div class="setting-row flush">
        <div class="setting-copy">
          <p class="hint">The same colours, marks and words everywhere. Saved for your account.</p>
          <p :id="`${id}-palette-hint`" class="hint">{{ AGENT_PALETTES.find(option => option.id === states.palette)?.description }}</p>
        </div>
        <select class="field palette" aria-label="Palette" :aria-describedby="`${id}-palette-hint`" :value="states.palette" @change="saveState({ palette: ($event.target as HTMLSelectElement).value as AgentPalette })">
          <option v-for="option in AGENT_PALETTES" :key="option.id" :value="option.id">{{ option.name }}</option>
        </select>
      </div>
      <div class="state-preview" aria-label="Agent state preview">
        <span v-for="state in previewStates" :key="state" class="state-example">
          <LiveBot :state="state" :size="32" /><AgentStateLabel :state="state" />
        </span>
      </div>
      <div class="setting-row switch-row">
        <div class="setting-copy"><p :id="`${id}-dim`" class="setting-label">Dim inactive</p><p class="hint">Idle and normally stopped agents stay grey.</p></div>
        <label class="switch"><input type="checkbox" role="switch" :checked="states.dimInactive" :aria-labelledby="`${id}-dim`" @change="saveState({ dimInactive: ($event.target as HTMLInputElement).checked })" /><span>{{ states.dimInactive ? 'On' : 'Off' }}</span></label>
      </div>
      <div class="setting-row slider-row" :class="{ disabled: !states.dimInactive }">
        <div class="setting-copy"><label :for="`${id}-opacity`" class="setting-label">Inactive opacity</label></div>
        <div class="slider">
          <input :id="`${id}-opacity`" class="range" type="range" min="40" max="80" step="1" :disabled="!states.dimInactive" :value="states.inactiveOpacity"
            :style="fill(states.inactiveOpacity, 40, 80)" aria-label="Inactive opacity" :aria-valuetext="`${states.inactiveOpacity}%`"
            @input="saveState({ inactiveOpacity: Number(($event.target as HTMLInputElement).value) })" />
          <span class="slider-value">{{ states.inactiveOpacity }}%</span>
        </div>
      </div>
      <div class="heartbeat">
        <p class="setting-label">Heartbeat timing</p>
        <p class="hint">Warnings for sessions that were working. No heartbeat means activity is unconfirmed; Problem means a reported failure.</p>
        <div class="thresholds">
          <label class="state-field">Yellow after (minutes)<input class="field" type="number" min="1" max="1439" step="1" :value="states.yellowMinutes" @change="saveState({ yellowMinutes: Number(($event.target as HTMLInputElement).value) })" /></label>
          <label class="state-field">Red after (minutes)<input class="field" type="number" :min="states.yellowMinutes + 1" max="1440" step="1" :value="states.redMinutes" @change="saveState({ redMinutes: Number(($event.target as HTMLInputElement).value) })" /></label>
        </div>
        <p class="hint">Red must follow yellow. Changing yellow moves red forward when needed.</p>
      </div>
      <p v-if="stateFailed" class="save-error" role="alert">Your agent state settings could not be saved.<button class="btn sm" type="button" @click="saveState({})">Try again</button></p>
    </fieldset>
  </div>
</template>

<style scoped>
.indicator-settings { padding-top: 16px; border-top: 1px solid var(--line); }
fieldset { min-width: 0; padding: 0; border: 0; margin: 0; }
legend, .setting-label { display: block; padding: 0; font-size: 13.5px; font-weight: 600; color: var(--ink); }
.hint { margin-top: 3px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.picker-body { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 12px 20px; margin-top: 12px; }
.picker-main { min-width: 0; }
.indicator-choices { display: flex; flex-wrap: wrap; gap: 6px; }
.indicator-choice { appearance: none; position: relative; display: grid; place-items: center; width: 48px; height: 48px; padding: 0; border: 1px solid var(--line); border-radius: 12px; background: var(--surface-raised); color: var(--ink); cursor: pointer; }
.indicator-choice:hover:not(:disabled) { background: var(--surface-sunken); }
.indicator-choice.selected, .indicator-choice.selected:hover { background: var(--chip-teal-bg); border-color: var(--chip-teal-line); box-shadow: 0 0 0 1px var(--chip-teal-line); }
.indicator-choice:focus-visible { outline: 2px solid var(--teal); outline-offset: 3px; }
.indicator-choice.unavailable { background: transparent; cursor: default; }
.placeholder { width: 30px; height: 30px; fill: none; stroke: var(--ink-3); stroke-width: 1; opacity: .45; }
.caption { margin-top: 10px; min-height: 20px; font-size: 13px; line-height: 20px; color: var(--ink-2); }
.caption strong { font-weight: 600; color: var(--ink); }
.demo { display: grid; place-items: center; width: 96px; height: 96px; border-radius: 14px; background: var(--surface-sunken); }
.setting-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-top: 18px; }
.setting-row.flush { margin-top: 4px; }
.setting-copy { min-width: 0; }
.seg { flex-shrink: 0; }
.slider { display: grid; grid-template-columns: minmax(140px, 220px) 42px; align-items: center; gap: 4px 10px; flex-shrink: 0; }
.slider-value { font-size: 12.5px; font-weight: 600; font-variant-numeric: tabular-nums; text-align: right; color: var(--ink); }
.slider-note, .link-btn { grid-column: 1; justify-self: start; font-size: 12px; color: var(--ink-2); }
.link-btn { padding: 2px 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-size: 12px; font-weight: 600; text-decoration: underline; text-underline-offset: 2px; cursor: pointer; }
.link-btn:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; border-radius: 4px; }
.slider-row.disabled .setting-label, .slider-row.disabled .slider-value { color: var(--ink-3); }
.range { --fill: 50%; --track: color-mix(in srgb, var(--ink-3) 38%, transparent); appearance: none; width: 100%; height: 24px; margin: 0; background: transparent; cursor: pointer; }
.range::-webkit-slider-runnable-track { height: 4px; border-radius: 999px; background: linear-gradient(to right, var(--teal) var(--fill), var(--track) var(--fill)); }
.range::-moz-range-track { height: 4px; border-radius: 999px; background: var(--track); }
.range::-moz-range-progress { height: 4px; border-radius: 999px; background: var(--teal); }
.range::-webkit-slider-thumb { appearance: none; width: 18px; height: 18px; margin-top: -7px; border: 2px solid var(--teal); border-radius: 50%; background: var(--surface-raised); box-shadow: 0 1px 3px rgba(32, 60, 61, .2); }
.range::-moz-range-thumb { width: 14px; height: 14px; border: 2px solid var(--teal); border-radius: 50%; background: var(--surface-raised); box-shadow: 0 1px 3px rgba(32, 60, 61, .2); }
.range:focus-visible { outline: none; }
.range:focus-visible::-webkit-slider-thumb { box-shadow: var(--focus-ring); }
.range:focus-visible::-moz-range-thumb { box-shadow: var(--focus-ring); }
.range:disabled { cursor: default; }
.range:disabled::-webkit-slider-runnable-track { background: var(--track); }
.range:disabled::-moz-range-progress { background: var(--track); }
.range:disabled::-webkit-slider-thumb { border-color: var(--ink-3); box-shadow: none; }
.range:disabled::-moz-range-thumb { border-color: var(--ink-3); box-shadow: none; }
.save-error { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 12px; font-size: 12.5px; color: var(--danger); }
.state-settings { margin-top: 28px; }
.palette { flex-shrink: 0; width: 220px; min-height: 36px; padding: 6px 10px; }
.state-preview { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 14px 10px; margin-top: 14px; padding: 14px 10px; border-radius: 10px; background: var(--surface-sunken); }
.state-example { display: grid; justify-items: center; gap: 8px; text-align: center; }
.heartbeat { margin-top: 22px; }
.state-field { display: grid; gap: 6px; margin-block: 14px; font-size: 12.5px; font-weight: 550; color: var(--ink); }
.state-field .field { width: 100%; min-height: 36px; padding: 6px 10px; }
.thresholds { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
@media (max-width: 600px) {
  .picker-body { grid-template-columns: minmax(0, 1fr); }
  .demo { grid-row: 1; width: 100%; height: 84px; }
  .indicator-choice { width: 44px; height: 44px; }
  .setting-row:not(.switch-row) { align-items: stretch; flex-direction: column; gap: 10px; }
  .switch-row { align-items: flex-start; }
  .seg { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .seg button { height: 40px; }
  .slider { grid-template-columns: minmax(0, 1fr) 42px; }
  .palette { width: 100%; }
  .state-preview { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
