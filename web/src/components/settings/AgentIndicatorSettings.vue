<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { AGENT_INDICATOR_KEY, useAgentIndicator, type AgentIndicatorStyle } from '../../lib/agentIndicator'
import { indicatorVariants, resolveIndicatorStyle } from '../../lib/indicatorVariants'
import { onPreferenceFailure } from '../../lib/preferences'
import LiveBot, { availableVariants } from '../projects/LiveBot.vue'
import AppIcon from '../AppIcon.vue'
import AgentStateLabel from '../agents/AgentStateLabel.vue'
import { AGENT_STATE_KEY, useAgentAppearance } from '../../lib/agentAppearance'
import type { AgentPalette, AgentState } from '../../lib/agentSignals'

const { choice, setStyle, setHovering } = useAgentIndicator()
const { choice: states, save: saveStates } = useAgentAppearance()
const previewStates: AgentState[] = ['working', 'waiting', 'throttled', 'problem', 'idle', 'stopped']
const stateFailed = ref(false)
onBeforeUnmount(onPreferenceFailure(key => { if (key === AGENT_STATE_KEY) stateFailed.value = true }))
function saveState(patch: Parameters<typeof saveStates>[0]) { stateFailed.value = false; saveStates(patch) }
const id = useId()
const available = new Set(availableVariants.map(variant => variant.id))
const selected = computed(() => resolveIndicatorStyle(choice.value.style, availableVariants))
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
// Arrows browse the three-column grid; native Enter/Space activation selects.
// Missing tiles are skipped, with wraparound along the same row/column order.
function move(event: KeyboardEvent, index: number) {
  const delta = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -3, ArrowDown: 3 }[event.key]
  if (delta === undefined) return
  event.preventDefault()
  for (let step = 1; step <= indicatorVariants.length; step++) {
    const next = (index + delta * step + indicatorVariants.length * 3) % indicatorVariants.length
    const option = indicatorVariants[next]!
    if (!available.has(option.id)) continue
    focused.value = option.id
    grid.value?.querySelector<HTMLButtonElement>(`[data-variant="${option.id}"]`)?.focus()
    return
  }
}
</script>

<template>
  <div class="indicator-settings">
    <fieldset>
      <legend :id="`${id}-label`">Agent indicator</legend>
      <div class="choice-intro">
        <p :id="`${id}-hint`" class="hint">Choose how your agents look while they work.</p>
        <span class="scale-hint">Calm <AppIcon name="arrow" :size="12" /> playful</span>
      </div>
      <div ref="grid" class="indicator-choices" role="radiogroup" :aria-labelledby="`${id}-label`" :aria-describedby="`${id}-hint`">
        <button v-for="(option, index) in indicatorVariants" :key="option.id" type="button" role="radio"
          class="indicator-choice" :class="{ selected: selected === option.id, unavailable: !available.has(option.id) }"
          :data-variant="option.id" :aria-checked="selected === option.id" :aria-disabled="!available.has(option.id)"
          :disabled="!available.has(option.id)" :tabindex="focused === option.id && available.has(option.id) ? 0 : -1"
          :aria-labelledby="`${id}-${option.id}-name`" :aria-describedby="`${id}-${option.id}-description`"
          @focus="focused = option.id" @click="select(option.id)" @keydown="move($event, index)">
          <span class="preview">
            <LiveBot v-if="available.has(option.id)" :indicator-style="option.id" :id="option.id" :size="44" />
            <svg v-else class="placeholder" viewBox="0 0 44 44" aria-hidden="true" focusable="false"><circle cx="22" cy="22" r="13" /><path d="M18 22h8" /></svg>
          </span>
          <span class="choice-copy"><strong :id="`${id}-${option.id}-name`">{{ option.name }}</strong><span :id="`${id}-${option.id}-description`">{{ available.has(option.id) ? option.description : 'Coming soon' }}</span></span>
          <AppIcon v-if="selected === option.id" class="selected-mark" name="check" :size="14" />
        </button>
      </div>
    </fieldset>
    <div class="hovering-setting">
      <div><p :id="`${id}-hovering-label`" class="hovering-label">Hovering</p><p :id="`${id}-hovering-hint`" class="hint">A gentle up-and-down motion while working. Respects reduced motion.</p></div>
      <label class="switch">
        <input type="checkbox" role="switch" :checked="choice.hovering" :aria-labelledby="`${id}-hovering-label`" :aria-describedby="`${id}-hovering-hint`" @change="failed = false; setHovering(($event.target as HTMLInputElement).checked)" />
        <span>{{ choice.hovering ? 'On' : 'Off' }}</span>
      </label>
    </div>
    <p v-if="failed" class="save-error" role="alert">Your agent indicator setting could not be saved.<button class="btn sm" type="button" @click="retry">Try again</button></p>
    <fieldset class="state-settings">
      <legend>Agent states</legend>
      <p class="hint">The same colours, marks and words everywhere. Saved for your account.</p>
      <label class="state-field">Palette
        <select class="field" aria-label="Palette" :value="states.palette" @change="saveState({ palette: ($event.target as HTMLSelectElement).value as AgentPalette })">
          <option value="standard">Standard</option><option value="colour-blind">Colour-blind friendly</option><option value="monochrome">Monochrome</option>
        </select>
      </label>
      <div class="state-preview" aria-label="Agent state preview">
        <span v-for="state in previewStates" :key="state" class="state-example">
          <LiveBot :state="state" :size="32" /><AgentStateLabel :state="state" />
        </span>
      </div>
      <div class="hovering-setting">
        <div><p :id="`${id}-dim`" class="hovering-label">Dim inactive</p><p class="hint">Idle and normally stopped agents stay grey.</p></div>
        <label class="switch"><input type="checkbox" role="switch" :checked="states.dimInactive" :aria-labelledby="`${id}-dim`" @change="saveState({ dimInactive: ($event.target as HTMLInputElement).checked })" /><span>{{ states.dimInactive ? 'On' : 'Off' }}</span></label>
      </div>
      <label class="state-field">Inactive opacity · {{ states.inactiveOpacity }}%
        <input type="range" min="40" max="80" step="1" :disabled="!states.dimInactive" :value="states.inactiveOpacity" aria-label="Inactive opacity" @input="saveState({ inactiveOpacity: Number(($event.target as HTMLInputElement).value) })" />
      </label>
      <p class="hint">Heartbeat warnings for sessions that were working. A normal stop never becomes a problem.</p>
      <div class="thresholds">
        <label class="state-field">Yellow after (minutes)<input class="field" type="number" min="1" max="1439" step="1" :value="states.yellowMinutes" @change="saveState({ yellowMinutes: Number(($event.target as HTMLInputElement).value) })" /></label>
        <label class="state-field">Red after (minutes)<input class="field" type="number" :min="states.yellowMinutes + 1" max="1440" step="1" :value="states.redMinutes" @change="saveState({ redMinutes: Number(($event.target as HTMLInputElement).value) })" /></label>
      </div>
      <p class="hint">Red must follow yellow. Changing yellow moves red forward when needed.</p>
      <p v-if="stateFailed" class="save-error" role="alert">Your agent state settings could not be saved.<button class="btn sm" type="button" @click="saveState({})">Try again</button></p>
    </fieldset>
  </div>
</template>

<style scoped>
.indicator-settings { padding-top: 16px; border-top: 1px solid var(--line); }
fieldset { min-width: 0; padding: 0; border: 0; margin: 0; }
legend, .hovering-label { padding: 0; font-size: 13.5px; font-weight: 600; color: var(--ink); }
.hint { margin-top: 3px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.choice-intro { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 4px 12px; }
.scale-hint { display: inline-flex; align-items: center; gap: 6px; font-size: 11px; color: var(--ink-3); white-space: nowrap; }
.indicator-choices { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; margin-top: 14px; }
.indicator-choice { appearance: none; position: relative; display: flex; flex-direction: column; align-items: center; justify-content: flex-start; gap: 12px; min-width: 0; padding: 18px 10px 14px; border: 1px solid var(--line); border-radius: 12px; background: var(--surface-raised); color: var(--ink); font: inherit; text-align: center; cursor: pointer; }
.indicator-choice:hover:not(:disabled) { background: var(--surface-sunken); }
.indicator-choice.selected, .indicator-choice.selected:hover { background: var(--chip-teal-bg); border-color: var(--chip-teal-line); }
.indicator-choice:focus-visible { outline: 2px solid var(--teal); outline-offset: 3px; }
.indicator-choice.unavailable { background: transparent; border-color: var(--line); cursor: default; }
.selected-mark { position: absolute; top: 9px; right: 9px; color: var(--teal-ink); pointer-events: none; }
.preview { display: grid; place-items: center; flex-shrink: 0; width: 48px; height: 48px; }
.placeholder { width: 44px; height: 44px; fill: none; stroke: var(--ink-3); stroke-width: 1; opacity: .45; }
.choice-copy { display: grid; gap: 4px; font-size: 13px; line-height: 1.4; }
.choice-copy strong { font-weight: 600; }
.choice-copy > span { font-size: 11.5px; color: var(--ink-2); text-wrap: balance; }
.unavailable .choice-copy, .unavailable .choice-copy > span { color: var(--ink-3); }
.hovering-setting { display: flex; align-items: center; gap: 16px; justify-content: space-between; margin-top: 18px; }
.switch { flex-shrink: 0; }
.save-error { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 12px; font-size: 12.5px; color: var(--danger); }
.state-settings { margin-top: 26px; }
.state-field { display: grid; gap: 6px; margin-block: 14px; font-size: 12.5px; font-weight: 550; color: var(--ink); }
.state-field .field { width: 100%; min-height: 36px; padding: 6px 10px; }
.state-preview { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px 10px; padding: 14px 10px; border-radius: 10px; background: var(--surface-sunken); }
.state-example { display: grid; justify-items: center; gap: 8px; text-align: center; }
.thresholds { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
@media (max-width: 600px) {
  .indicator-choices { gap: 7px; }
  .indicator-choice { padding: 14px 6px 10px; gap: 8px; border-radius: 10px; }
  .choice-copy { font-size: 12px; }
  .choice-copy > span { font-size: 10.5px; }
  .hovering-setting { align-items: flex-start; }
}
</style>
