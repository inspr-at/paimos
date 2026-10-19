<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The capacity line (AEON-1459, design AEON-1279 r2 R2.1): on phones and short screens the dial
// strip and the accounts chip fold into one line, "24 agents · 16 running · 4 accounts · 2 computers ›".
// It opens their detail as a sheet: the page's own dial control (its units and steppers, moved in by
// the page, so there is one dial and one writer), the accounts and computers, then the full dial.
import { computed, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'

export interface CapacityFacts { total: number; running: number; known: boolean }
export interface AccountFacts { accounts: number; computers: number; online: number; flag: string; known: boolean }
const props = defineProps<{ dial: CapacityFacts | null; machines: AccountFacts | null; showAccounts: boolean; open: boolean }>()
const emit = defineEmits<{ open: [anchor: HTMLElement]; close: [restoreFocus: boolean]; ready: []; fullDial: [] }>()
const trigger = ref<HTMLButtonElement>()
const plural = (n: number, one: string) => `${n.toLocaleString('en-GB')} ${n === 1 ? one : `${one}s`}`
// Each part is a number and its word, glued; the dots sit between parts, never at a line's edge.
const parts = computed(() => {
  const out: { n: number; word: string }[] = []
  if (props.dial?.known) out.push({ n: props.dial.total, word: props.dial.total === 1 ? 'agent' : 'agents' }, { n: props.dial.running, word: 'running' })
  if (props.machines?.known) {
    if (props.showAccounts) out.push({ n: props.machines.accounts, word: props.machines.accounts === 1 ? 'account' : 'accounts' })
    out.push({ n: props.machines.computers, word: props.machines.computers === 1 ? 'computer' : 'computers' })
  }
  return out
})
const reading = computed(() => !parts.value.length)
const label = computed(() => {
  const dial = props.dial?.known ? `${plural(props.dial.total, 'agent')} at once, ${props.dial.running} running` : ''
  const rest = props.machines?.known ? [props.showAccounts ? plural(props.machines.accounts, 'account') : '', plural(props.machines.computers, 'computer'), props.machines.flag].filter(Boolean).join(', ') : ''
  return `Capacity: ${[dial, rest].filter(Boolean).join(', ') || 'reading'}. Open the details`
})
function toggle() { if (props.open) emit('close', false); else if (trigger.value) emit('open', trigger.value) }
const body = ref<HTMLElement>()
// The page moves its dial and accounts in once the sheet's body is in place.
watch(body, element => { if (element) emit('ready') }, { flush: 'post' })
defineExpose({ focus: () => trigger.value?.focus({ preventScroll: true }) })
</script>

<template>
  <div class="cap-line-slot">
  <button
    ref="trigger" type="button" class="cap-line" aria-haspopup="dialog" :aria-expanded="open" :aria-label="label"
    data-tip="Agents at once, accounts and computers" @click="toggle"
  >
    <span class="cl-t" aria-hidden="true">
      <span v-if="reading" class="cl-reading">Reading capacity…</span>
      <template v-for="(part, index) in parts" :key="part.word"><template v-if="index">&nbsp;<span class="cl-sep">·</span>&nbsp;</template><b>{{ part.n.toLocaleString('en-GB') }}</b>&nbsp;{{ part.word }}</template>
    </span>
    <AppIcon name="chevron-right" :size="14" class="cl-go" />
  </button>
  <FloatingPanel v-if="open" :anchor="trigger ?? null" align="start" :width="360" :tallest="600" label="Capacity" phone-sheet :sheet-below="719" cycle fields-own-escape @close="emit('close', $event)">
      <div class="cap-sheet">
        <div class="cs-head"><h3 class="cs-title" tabindex="-1" data-autofocus>Capacity</h3><button type="button" class="btn sm primary" @click="emit('close', true)">Done</button></div>
        <div id="capacity-sheet-dial" ref="body" class="cs-dial" />
        <hr class="cs-sep" role="separator" />
        <button type="button" class="cs-item" @click="emit('fullDial')">
          <AppIcon name="gauge" :size="16" /><span class="cs-t"><span>The full dial</span><small>Daily limits, pace and boosts per harness</small></span><AppIcon name="chevron-right" :size="14" class="cs-go" />
        </button>
      </div>
  </FloatingPanel>
  </div>
</template>

<style scoped>
.cap-line-slot { display: none; min-width: 0; }
.cap-line { display: none; }
@media (max-width: 719px), (max-height: 500px) {
  .cap-line-slot { display: flex; }
  .cap-line {
    display: inline-flex; flex: 1 1 auto; max-width: 100%; align-items: center; gap: 8px; min-width: 0; height: 44px; padding: 0 10px 0 14px; border: 1px solid var(--glass-edge); border-radius: 999px;
    background: var(--btn-bg); box-shadow: var(--shadow-btn); color: var(--ink-2); font: 500 12.5px/1 var(--font); white-space: nowrap; cursor: pointer; font-variant-numeric: tabular-nums;
  }
  .cap-line:hover, .cap-line[aria-expanded="true"] { background: var(--btn-bg-hover); color: var(--ink); }
  .cap-line:focus-visible { outline: none; box-shadow: var(--focus-ring); }
}
.cl-t { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.cl-t b { color: var(--ink); font-weight: 650; }
.cl-sep { margin: 0 2px; color: var(--ink-3); }
.cl-reading { color: var(--ink-3); }
.cl-go { flex: none; margin-left: auto; color: var(--ink-3); }
.cap-sheet { display: grid; gap: 4px; }
.cs-head { display: flex; align-items: center; gap: 8px; padding: 4px 4px 4px 10px; }
.cs-title { flex: 1; margin: 0; font-size: 15px; outline: none; }
.cs-head .btn { min-height: 44px; }
/* The page's capacity row, moved in: its dial and accounts chip wrap here as on a phone. */
.cs-dial { padding: 4px 0; }
.cs-dial:empty { min-height: 44px; }
.cs-sep { height: 1px; margin: 4px; border: 0; background: var(--line); }
.cs-item { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 52px; padding: 4px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font: inherit; font-size: 14px; text-align: left; cursor: pointer; }
.cs-item > svg { flex: none; color: var(--ink-2); }
@media (hover: hover) { .cs-item:hover { background: var(--row-hover); } }
.cs-item:focus-visible { outline: none; background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.cs-t { display: grid; gap: 2px; flex: 1; min-width: 0; }
.cs-t small { color: var(--ink-3); font-size: 12px; }
.cs-go { margin-left: auto; color: var(--ink-3) !important; }
</style>
