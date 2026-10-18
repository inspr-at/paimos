<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The rich figure tip (AEON-1358): the app tooltip's look, opaque, in the top layer (like TooltipHost) so no overflow
// or transform clips it. Its owner renders it while lib/figureTip says the tip is theirs; the content is the owner's
// slot and keeps a fixed size per owner, so "Counting…" and the landed figures occupy the same box.
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { closeFigureTip, escapeFigureTip, FIGURE_TIP_ID, figureTip } from '../../lib/figureTip'
import '../../styles/work-figures.css'

// fig 300 px and wide 330 px (the design's popups); xwide fits the work bar's longest footer line (no-release projects).
// These widths are the floor: a font or language wider than the design's grows the box to its longest line instead of
// clipping it, and the box is placed again whenever its size changes.
const props = withDefaults(defineProps<{ width?: 'auto' | 'fig' | 'wide' | 'xwide' }>(), { width: 'auto' })
const tip = ref<HTMLElement>()
const x = ref(0)
const y = ref(0)

function place() {
  const el = tip.value, anchor = figureTip.anchor
  if (!el || !anchor?.isConnected) return
  if (!el.matches(':popover-open')) el.showPopover?.()
  const r = anchor.getBoundingClientRect(), w = el.offsetWidth, h = el.offsetHeight
  x.value = Math.round(Math.min(Math.max(8, r.left + r.width / 2 - w / 2), innerWidth - w - 8))
  let top = figureTip.place === 'above' ? r.top - h - 8 : r.bottom + 8
  if (figureTip.place === 'above' && top < 8) top = r.bottom + 8
  if (figureTip.place === 'below' && top + h > innerHeight - 8) top = Math.max(8, r.top - h - 8)
  y.value = Math.round(top)
}
function keydown(event: KeyboardEvent) {
  if (event.key !== 'Escape' || !figureTip.anchor) return
  event.preventDefault(); event.stopImmediatePropagation()
  escapeFigureTip()
}
function pointerDown(event: PointerEvent) {
  const target = event.target as Node | null
  if (!figureTip.pinned || tip.value?.contains(target) || figureTip.anchor?.contains(target)) return
  closeFigureTip()
}
function scroll(event: Event) { if (!(event.target instanceof Node && tip.value?.contains(event.target))) closeFigureTip() }
function resize() { closeFigureTip() }
// Moving along the header row hands the same tip to the next anchor.
watch(() => [figureTip.anchor, figureTip.owner, props.width], () => { void nextTick(place) })
let sized: ResizeObserver | null = null
onMounted(() => {
  void nextTick(place)
  if (typeof ResizeObserver !== 'undefined' && tip.value) { sized = new ResizeObserver(() => place()); sized.observe(tip.value) }
  window.addEventListener('keydown', keydown, true)
  document.addEventListener('pointerdown', pointerDown, true)
  document.addEventListener('scroll', scroll, true)
  window.addEventListener('resize', resize)
})
onBeforeUnmount(() => {
  sized?.disconnect()
  window.removeEventListener('keydown', keydown, true)
  document.removeEventListener('pointerdown', pointerDown, true)
  document.removeEventListener('scroll', scroll, true)
  window.removeEventListener('resize', resize)
})
defineExpose({ place })
</script>

<template>
  <Teleport to="body">
    <div :id="FIGURE_TIP_ID" ref="tip" popover="manual" role="tooltip" class="figure-tip" :class="[`w-${props.width}`, { pinned: figureTip.pinned }]" :style="{ transform: `translate(${x}px, ${y}px)` }">
      <slot />
    </div>
  </Teleport>
</template>

<style scoped>
.figure-tip {
  position: fixed; z-index: 80; inset: auto; top: 0; left: 0; margin: 0; border: 0; width: max-content; max-width: min(320px, calc(100vw - 16px)); padding: 5px 10px; border-radius: 8px;
  overflow: visible; pointer-events: none;
  background: var(--tip-bg); color: var(--tip-ink); font-size: 12.5px; line-height: 1.4;
  box-shadow: 0 0 0 1px var(--glass-rim), 0 10px 24px -10px color-mix(in srgb, var(--shadow-black) 50%, transparent);
}
.figure-tip.pinned { pointer-events: auto; }
.figure-tip.w-fig, .figure-tip.w-wide, .figure-tip.w-xwide { padding: 6px 12px 7px; }
.figure-tip.w-fig, .figure-tip.w-wide, .figure-tip.w-xwide { max-width: calc(100vw - 16px); }
.figure-tip.w-fig { min-width: min(300px, calc(100vw - 16px)); }
.figure-tip.w-wide { min-width: min(330px, calc(100vw - 16px)); }
.figure-tip.w-xwide { min-width: min(372px, calc(100vw - 16px)); }
</style>
