<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Display › Show (AEON-1614, Tick spec v1 + v1.1): three switches in the existing
// mini-switch rows, each saved with the person's Goals preferences. Done goals (H) is
// on when done goals are visible; Hidden goals ({n}) shows the goals the person hid,
// disabled in place while there are none; Waiting on you shows the Desk band. The
// Display panel uses them as switches, the phone ⋯ Goal display menu as menu items.
// Rows keep one height and the switch keeps its place, so nothing moves on a flip.
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import type { GoalsPage } from './goalsPage'
import { hideCopy } from '../../lib/goals'

const props = defineProps<{ page: GoalsPage; menu?: boolean }>()
const role = computed(() => props.menu ? 'menuitemcheckbox' : 'switch')
const count = computed(() => props.page.hiddenCount.value)
</script>

<template>
  <div class="show-rows" :role="menu ? 'group' : undefined" :aria-label="hideCopy.show">
    <p class="eyebrow show-label">{{ hideCopy.show }}</p>
    <button
      type="button" :role="role" class="show-row" data-show="done" :aria-checked="!page.hideDone.value" aria-keyshortcuts="h"
      @click="page.setHideDone(!page.hideDone.value)"
    ><AppIcon name="circle-check" :size="14" /><span class="show-name">{{ hideCopy.doneGoals }}</span><kbd class="keycap" aria-hidden="true">H</kbd><span class="mini-switch" aria-hidden="true" /></button>
    <button
      type="button" :role="role" class="show-row" data-show="hidden" :aria-checked="page.showHidden.value" :disabled="count === 0"
      :data-tip="hideCopy.hiddenTip" :aria-description="hideCopy.hiddenTip" @click="page.setShowHidden(!page.showHidden.value)"
    ><AppIcon name="eye-off" :size="14" /><span class="show-name">{{ hideCopy.hiddenGoals(count) }}</span><span class="mini-switch" aria-hidden="true" /></button>
    <button
      type="button" :role="role" class="show-row" data-show="waiting" :aria-checked="page.showWaiting.value"
      @click="page.setShowWaiting(!page.showWaiting.value)"
    ><AppIcon name="person-wait" :size="14" /><span class="show-name">{{ hideCopy.waiting }}</span><span class="mini-switch" aria-hidden="true" /></button>
  </div>
</template>

<style scoped>
.show-rows { display: grid; }
.show-label { margin: 0; padding: 4px 2px 4px; }
.show-row { display: flex; align-items: center; gap: 10px; width: 100%; height: 36px; padding: 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13px; text-align: left; }
.show-row > svg { flex: none; color: var(--ink-3); }
.show-row:hover:not(:disabled), .show-row:focus-visible { background: var(--row-hover); }
.show-row:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.show-row:disabled { color: var(--ink-3); cursor: default; }
.show-row:disabled .mini-switch { opacity: .5; }
.show-name { flex: 1 1 auto; min-width: 0; overflow-wrap: anywhere; }
.show-row .keycap { flex: none; }
.mini-switch { position: relative; flex: none; width: 34px; height: 20px; border-radius: 999px; background: var(--track); box-shadow: inset 0 1px 3px color-mix(in srgb, var(--shadow-color) 20%, transparent); }
.mini-switch::after { content: ''; position: absolute; top: 2px; left: 2px; width: 16px; height: 16px; border-radius: 50%; background: var(--switch-knob); box-shadow: 0 1px 3px color-mix(in srgb, var(--shadow-color) 30%, transparent); }
[aria-checked="true"] > .mini-switch { background: var(--primary); box-shadow: none; }
[aria-checked="true"] > .mini-switch::after { transform: translateX(14px); }
/* Phones and touch: 44 px rows, the phone menu's height. */
@media (max-width: 760px), (pointer: coarse) { .show-row { height: 44px; font-size: 14px; } }
@media (max-width: 760px), (hover: none) { .show-row .keycap { display: none; } }
</style>
