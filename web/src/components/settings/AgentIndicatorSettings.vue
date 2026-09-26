<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { AGENT_INDICATOR_KEY, useAgentIndicator } from '../../lib/agentIndicator'
import { onPreferenceFailure } from '../../lib/preferences'
import LiveBot from '../projects/LiveBot.vue'
import AppIcon from '../AppIcon.vue'

const { choice, setStyle, setHovering } = useAgentIndicator()
const failed = ref(false)
onBeforeUnmount(onPreferenceFailure(key => { if (key === AGENT_INDICATOR_KEY) failed.value = true }))
function retry() { failed.value = false; setStyle(choice.value.style) }
</script>

<template>
  <div class="indicator-settings">
    <fieldset>
      <legend>Agent indicator</legend>
      <p class="hint">Choose how your agents look while they work.</p>
      <div class="indicator-choices">
        <label v-for="option in (['calm', 'playful'] as const)" :key="option" class="indicator-choice" :class="{ selected: choice.style === option }">
          <input type="radio" name="agent-indicator" :value="option" :checked="choice.style === option" @change="failed = false; setStyle(option)" />
          <span class="preview"><LiveBot :indicator-style="option" :size="36" /></span>
          <span class="choice-copy"><strong>{{ option === 'calm' ? 'Calm' : 'Playful' }}</strong><span>{{ option === 'calm' ? 'A quiet activity ring' : 'A friendly robot' }}</span></span>
          <AppIcon v-if="choice.style === option" class="selected-mark" name="check" :size="14" />
        </label>
      </div>
    </fieldset>
    <div class="hovering-setting">
      <div><p id="indicator-hovering-label" class="hovering-label">Hovering</p><p id="indicator-hovering-hint" class="hint">A gentle up-and-down motion, with either style. Respects reduced motion.</p></div>
      <label class="switch">
        <input type="checkbox" role="switch" :checked="choice.hovering" aria-labelledby="indicator-hovering-label" aria-describedby="indicator-hovering-hint" @change="failed = false; setHovering(($event.target as HTMLInputElement).checked)" />
        <span>{{ choice.hovering ? 'On' : 'Off' }}</span>
      </label>
    </div>
    <p v-if="failed" class="save-error" role="alert">Your agent indicator setting could not be saved.<button class="btn sm" type="button" @click="retry">Try again</button></p>
  </div>
</template>

<style scoped>
.indicator-settings { padding-top: 16px; border-top: 1px solid var(--line); }
fieldset { min-width: 0; padding: 0; border: 0; margin: 0; }
legend, .hovering-label { padding: 0; font-size: 13.5px; font-weight: 600; color: var(--ink); }
.hint { margin-top: 3px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.indicator-choices { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; margin-top: 12px; }
.indicator-choice { position: relative; display: flex; align-items: center; gap: 12px; min-width: 0; padding: 14px; border-radius: 12px; box-shadow: inset 0 0 0 1px var(--line); cursor: pointer; }
.indicator-choice.selected { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.indicator-choice:has(input:focus-visible) { outline: none; box-shadow: var(--focus-ring); }
.indicator-choice input { position: absolute; inset: 0; width: 100%; height: 100%; margin: 0; opacity: 0; cursor: pointer; }
.selected-mark { position: absolute; top: 10px; right: 10px; color: var(--teal-ink); pointer-events: none; }
.preview { display: grid; place-items: center; flex-shrink: 0; width: 44px; height: 44px; }
.choice-copy { display: grid; gap: 3px; font-size: 13px; color: var(--ink); }
.choice-copy strong { font-weight: 600; }
.choice-copy > span { font-size: 12px; color: var(--ink-2); }
.hovering-setting { display: flex; align-items: center; gap: 16px; justify-content: space-between; margin-top: 18px; }
.switch { flex-shrink: 0; }
.save-error { display: flex; align-items: center; gap: 8px; margin-top: 12px; font-size: 12.5px; color: var(--danger); }
@media (max-width: 600px) {
  .indicator-choice { flex-direction: column; align-items: flex-start; gap: 8px; padding: 12px; }
  .hovering-setting { align-items: flex-start; }
}
</style>
