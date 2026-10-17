<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The Goals view's popover (AEON-1457): the ETA history and the chart's point line.
// It is placed once when it opens, right-aligned to its anchor or centred on it,
// inside the card the anchor belongs to; it opens on the preferred side and flips
// when there is no room. Lines never change while it is shown.
import { nextTick, onMounted, ref } from 'vue'
import AppIcon from '../AppIcon.vue'
export interface TipLine { text: string; muted?: string; attention?: boolean }
const props = withDefaults(defineProps<{ anchor: HTMLElement; lines: TipLine[]; prefer?: 'above' | 'below'; align?: 'end' | 'center'; x?: number | null }>(), { prefer: 'below', align: 'end', x: null })
const el = ref<HTMLElement>()
const pos = ref({ left: 0, top: 0, side: props.prefer, max: 380, ready: false })
function place() {
  const tip = el.value
  if (!tip) return
  const card = props.anchor.closest('.projects-card')?.getBoundingClientRect() ?? { left: 0, right: innerWidth }
  const lo = Math.max(8, card.left + 8), hi = Math.min(innerWidth - 8, card.right - 8)
  const max = Math.min(380, hi - lo)
  tip.style.maxWidth = `${max}px`
  const r = props.anchor.getBoundingClientRect(), w = tip.offsetWidth, h = tip.offsetHeight
  const centre = props.x ?? r.left + r.width / 2
  let left = props.align === 'end' ? r.right - 4 - w : centre - w / 2
  left = Math.max(lo, Math.min(hi - w, left))
  // The room is the scrolling page between the header and footer bars, not the window.
  const main = document.getElementById('main')?.getBoundingClientRect() ?? { top: 0, bottom: innerHeight }
  const gap = props.align === 'center' ? 6 : 8
  const fitsBelow = r.bottom + gap + h <= main.bottom - 8, fitsAbove = r.top - gap - h >= main.top + 8
  const below = props.prefer === 'below' ? fitsBelow || !fitsAbove : !fitsAbove
  pos.value = { left: Math.round(left), top: Math.round(below ? r.bottom + gap : r.top - h - gap), side: below ? 'below' : 'above', max, ready: true }
}
onMounted(() => { void nextTick(place) })
defineExpose({ place })
</script>

<template>
  <Teleport to="body">
    <div ref="el" class="goal-tip" role="tooltip" :data-side="pos.side" :style="{ left: `${pos.left}px`, top: `${pos.top}px`, maxWidth: `${pos.max}px`, visibility: pos.ready ? 'visible' : 'hidden' }">
      <div v-for="(line, i) in lines" :key="i" class="tip-line" :class="{ attn: line.attention }">
        <AppIcon v-if="line.attention" name="clock" :size="12" />
        <span>{{ line.text }}<span v-if="line.muted" class="muted">{{ line.muted }}</span></span>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.goal-tip { position: fixed; z-index: 95; width: max-content; padding: 7px 11px; border-radius: 9px; background: var(--tip-solid); color: var(--tip-ink); font-size: 12px; line-height: 1.4; pointer-events: none; box-shadow: 0 12px 26px -12px color-mix(in srgb, var(--shadow-black) 50%, transparent), 0 0 0 1px var(--tip-line); font-variant-numeric: tabular-nums; }
.tip-line + .tip-line { margin-top: 2px; }
.muted { color: var(--tip-muted); }
.attn { display: flex; align-items: center; gap: 6px; color: var(--tip-attn); }
.attn svg { flex: none; }
</style>
