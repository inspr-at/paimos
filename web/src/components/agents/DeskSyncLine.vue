<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import { deskSyncCopy, type DeskSyncLine } from '../../lib/deskSync'

// AEON-1700 (Tick UI spec v1): the Decision Desk's one sync line, [icon 12] state · next check. The words are
// not a live region (the Desk announces state changes on its own); the focusable text reads the full sentence,
// and its tip gives the exact clock on hover, focus and long press.
const props = defineProps<{ line: DeskSyncLine; pulse: number; busy: boolean }>()
const emit = defineEmits<{ retry: [] }>()
const words = deskSyncCopy()
const mark = ref<HTMLElement>()
let reduced: MediaQueryList | undefined
// ≤ 520 px the line takes its short forms (§3). Only the form on screen is in the DOM.
const phone = matchMedia('(max-width: 520px)'), short = ref(phone.matches)
const readPhone = () => { short.value = phone.matches }
onMounted(() => { reduced = matchMedia('(prefers-reduced-motion: reduce)'); phone.addEventListener('change', readPhone) })
onBeforeUnmount(() => phone.removeEventListener('change', readPhone))
// Each live event pulses the icon slot once. The slot outlives the swap from the Live dot to "Change arrived"'s
// refresh icon, which the same event causes, so the pulse is seen. Reduced motion keeps it still; the words carry it.
watch(() => props.pulse, () => {
  if (reduced?.matches || typeof mark.value?.animate !== 'function') return
  mark.value.animate([{ transform: 'scale(1)', opacity: 1 }, { transform: 'scale(1.6)', opacity: 0.35 }, { transform: 'scale(1)', opacity: 1 }], { duration: 600, easing: 'ease-out' })
})
</script>

<template>
  <p class="desk-sync" :class="{ warn: line.warn }" :data-state="line.state" data-testid="desk-sync">
    <span class="sync-text" tabindex="0" :data-tip="line.tip || undefined">
      <span v-if="line.icon" ref="mark" class="sync-mark"><span v-if="line.icon === 'dot'" class="sync-dot" role="img" :aria-label="words.live" data-testid="desk-sync-dot" /><AppIcon v-else class="sync-icon" :class="{ spin: line.spin }" :name="line.icon" :size="12" /></span>
      <span class="sync-words" aria-hidden="true"><template v-for="(part, index) in line.parts" :key="index"><span v-if="'n' in part" class="sync-n"><span>{{ part.n }}</span></span><template v-else>{{ short && part.short !== undefined ? part.short : part.text }}</template></template></span>
      <span class="sr-only">{{ line.sentence }}</span>
    </span>
    <template v-if="line.retry"><span class="sync-sep" aria-hidden="true">·</span><button type="button" class="sync-retry" :disabled="busy" data-testid="desk-sync-retry" @click="emit('retry')">{{ words.tryAgain }}</button></template>
  </p>
</template>

<style scoped>
/* One line, 20 px tall, muted ink-2 words with tabular numbers; warn ink only for a real problem. */
.desk-sync { display: flex; align-items: center; gap: 0 6px; min-width: 0; min-height: 20px; margin: 0; font-size: 12px; line-height: 20px; color: var(--ink-2); font-variant-numeric: tabular-nums; }
/* Inline flow: the icon belongs to the first line, so a line that must wrap (a narrow free end) keeps them together. */
.sync-text { min-width: 0; border-radius: 4px; outline: none; text-wrap: balance; }
.sync-text:focus-visible { box-shadow: var(--focus-ring); }
/* A number slot reserves "59" (an invisible layer under it), so 9 s → 10 s and 3 min → 59 min never shift the words. */
.sync-n { display: inline-grid; justify-items: end; }
.sync-n::before { content: '59'; visibility: hidden; }
.sync-n::before, .sync-n > span { grid-area: 1 / 1; }
.sync-icon { display: block; color: var(--ink-3); }
.warn .sync-icon { color: inherit; }
.warn { color: var(--warn-ink); }
/* One 12 px icon slot for the dot and the icons; the 6 px dot sits centred in it. */
.sync-mark { display: inline-grid; place-items: center; width: 12px; height: 12px; margin-right: 6px; vertical-align: -1px; }
.sync-mark > * { grid-area: 1 / 1; }
.sync-dot { display: grid; place-items: center; width: 12px; height: 12px; }
.sync-dot::before { content: ''; width: 6px; height: 6px; border-radius: 50%; background: var(--ink-3); }
@media (prefers-reduced-motion: no-preference) { .sync-icon.spin { animation: desk-sync-turn 1.2s linear infinite; } }
@keyframes desk-sync-turn { to { transform: rotate(1turn); } }
.sync-sep { opacity: .7; }
/* A text button in the sentence's own colour. Coarse pointers get a 44 px box that adds no line height (AEON-1477). */
.sync-retry { display: inline-flex; align-items: center; padding: 0; border: 0; border-radius: 4px; background: none; color: inherit; font: inherit; font-weight: 600; text-decoration: underline; text-underline-offset: 2px; cursor: pointer; white-space: nowrap; }
.sync-retry:disabled { opacity: .55; cursor: default; }
.sync-retry:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (pointer: coarse) { .sync-retry { justify-content: center; min-width: 44px; min-height: 44px; margin: -12px 0; border-radius: 0; } }
</style>
