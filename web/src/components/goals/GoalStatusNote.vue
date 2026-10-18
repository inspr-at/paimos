<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The workspace status note (AEON-1494 decision 1): one quiet paragraph an agent
// writes for the workspace, between the toolbar and Waiting on you. No box, no
// tint; its author and age sit right. At most three lines, then "… more", which
// expands in place; the chevron folds it to one line and the fold is remembered.
// Three lines are reserved (one when folded), so a new note never moves the list.
// Phones show one line and "more". Without a note nothing renders (the parent).
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import { agoText, type GoalStatusNote } from '../../lib/goals'

const props = defineProps<{ note: GoalStatusNote; now: number; folded: boolean }>()
const emit = defineEmits<{ fold: [folded: boolean] }>()

const LINE = 20, LINES = 3
const PHONE = '(max-width: 760px)'
const section = ref<HTMLElement>()
const body = ref<HTMLElement>()
const textEl = ref<HTMLElement>()
const full = ref(false)
// What the paragraph shows: the whole note, or its first words and "…" when "more" follows.
const shown = ref(props.note.text)
const more = ref(false)
const phone = ref(false)
const folded = computed(() => props.folded && !phone.value)
const by = computed(() => props.note.by_name.trim() || 'An agent')
const age = computed(() => agoText(props.note.set_at, props.now))

// Three lines at most. The box itself is exactly 60 px (20 px folded), so the
// list under it never moves. Linux draws the inline "more" button one pixel
// past the line box; that pixel is measured, then clipped, and is not a fourth line.
let turn = 0
function contentHeight() {
  const el = body.value
  if (!el) return 0
  const previous = el.style.height
  el.style.height = 'auto'
  const height = el.offsetHeight
  el.style.height = previous
  return height
}
async function fit() {
  const mine = ++turn, text = props.note.text
  shown.value = text
  if (folded.value) { more.value = false; return }
  if (full.value) { more.value = true; return }
  more.value = false
  await nextTick()
  if (mine !== turn) return
  if (phone.value) { const el = textEl.value; more.value = !!el && el.scrollWidth > el.clientWidth + 1; return }
  const limit = LINES * LINE + 1, overflows = () => contentHeight() > limit
  if (!overflows()) return
  more.value = true
  // Words, or characters for a note without spaces (a long link, say).
  const spaced = text.includes(' '), parts = spaced ? text.split(' ') : Array.from(text)
  const cut = (n: number) => `${parts.slice(0, n).join(spaced ? ' ' : '').replace(/[\s,;:.–—-]+$/, '')}…`
  let lo = 1, hi = parts.length - 1
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2)
    shown.value = cut(mid)
    await nextTick()
    if (mine !== turn) return
    if (overflows()) hi = mid - 1; else lo = mid
  }
  shown.value = cut(lo)
}
watch(() => [props.note.text, folded.value, full.value], () => { void fit() })

let observer: ResizeObserver | undefined, media: MediaQueryList | undefined, width = 0
const narrow = () => { phone.value = !!media?.matches; void fit() }
onMounted(() => {
  media = matchMedia(PHONE)
  phone.value = media.matches
  media.addEventListener('change', narrow)
  // Only a width change can re-wrap the paragraph; its own height changes are the fit's.
  observer = new ResizeObserver(entries => { const w = Math.round(entries[0]?.contentRect.width ?? 0); if (w !== width) { width = w; void fit() } })
  if (section.value) observer.observe(section.value)
  void fit()
  void document.fonts?.ready.then(() => fit())
})
onBeforeUnmount(() => { turn++; observer?.disconnect(); media?.removeEventListener('change', narrow) })

function toggleFold() { full.value = false; emit('fold', !props.folded) }
function toggleMore() { full.value = !full.value }
</script>

<template>
  <section ref="section" class="ws-note" :data-folded="folded ? 1 : 0" :data-full="full ? 1 : 0" :aria-label="`Workspace status by ${by}, ${age}`">
    <button
      type="button" class="n-toggle" :aria-expanded="!folded" aria-controls="ws-note-text"
      :aria-label="folded ? 'Show the status note' : 'Fold the status note to one line'" :data-tip="folded ? 'Show the note' : 'Fold to one line'" @click="toggleFold"
    >
      <AppIcon name="chevron-right" :size="14" />
    </button>
    <p id="ws-note-text" ref="body" class="n-body">
      <span ref="textEl" class="n-text">{{ shown }}</span>
      <button v-if="more" type="button" class="n-more" :aria-expanded="full" aria-controls="ws-note-text" @click="toggleMore">{{ full ? 'less' : 'more' }}</button>
    </p>
    <p class="n-meta"><span class="n-by">{{ by }}</span> · {{ age }}</p>
  </section>
</template>

<style scoped>
/* One quiet paragraph in the goal grid: no box, no tint, ink-2. Three lines are kept (one
   folded), so a new note never moves the list; only "more" pushes what follows down. */
.ws-note { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; align-items: start; padding: 13px 0 12px; }
.n-toggle { grid-column: 1; justify-self: center; position: relative; display: grid; place-items: center; width: 24px; height: 20px; padding: 0; border: 0; border-radius: 6px; background: transparent; color: var(--ink-3); }
@media (hover: hover) { .n-toggle:hover { background: var(--row-hover); color: var(--ink-2); } }
.n-toggle svg { transition: transform .15s ease; }
.ws-note[data-folded="0"] .n-toggle svg { transform: rotate(90deg); }
@media (prefers-reduced-motion: reduce) { .n-toggle svg { transition: none; } }
.n-body { grid-column: 2 / -4; min-width: 0; max-width: 92ch; height: 60px; margin: 0; overflow: hidden; font-size: 13.5px; line-height: 20px; color: var(--ink-2); overflow-wrap: anywhere; }
.ws-note[data-folded="1"] .n-body { height: 20px; white-space: nowrap; text-overflow: ellipsis; }
.ws-note[data-full="1"] .n-body { height: auto; overflow: visible; }
.n-more { position: relative; display: inline-block; vertical-align: top; height: 20px; margin: 0 -3px 0 4px; padding: 0 3px; border: 0; border-radius: 5px; appearance: none; background: transparent; color: var(--teal-ink); font-size: 13px; font-weight: 600; line-height: 20px; white-space: nowrap; }
@media (hover: hover) { .n-more:hover { background: var(--row-hover); } }
.n-meta { grid-column: -4 / -1; justify-self: end; margin: 0; font-size: 12.5px; line-height: 20px; color: var(--ink-3); white-space: nowrap; }
.n-by { font-weight: 600; color: var(--ink-2); }
.n-toggle:focus-visible, .n-more:focus-visible { outline: none; box-shadow: var(--focus-ring); }
/* Touch: the small controls get a 44 px hit area without changing their look. */
@media (max-width: 760px), (pointer: coarse) {
  .n-toggle::before, .n-more::before { content: ''; position: absolute; top: 50%; left: 50%; width: max(100%, 44px); height: max(100%, 44px); transform: translate(-50%, -50%); }
}
/* Phones: one line and "more"; expanded, the whole note with its byline below. */
@media (max-width: 760px) {
  .ws-note { display: flex; align-items: center; margin: 0 -8px; padding: 4px 14px 2px; min-height: 44px; }
  .n-toggle, .n-meta { display: none; }
  .n-body { flex: 1 1 auto; display: flex; align-items: baseline; gap: 8px; height: auto; min-height: 0; max-width: none; overflow: visible; font-size: 14px; }
  .n-text { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .n-more { flex: none; height: auto; margin: 0 -3px 0 0; font-size: 14px; }
  .ws-note[data-full="1"] { display: block; padding: 10px 14px; }
  .ws-note[data-full="1"] .n-body { display: block; line-height: 21px; }
  .ws-note[data-full="1"] .n-text { white-space: normal; }
  .ws-note[data-full="1"] .n-more { margin-left: 4px; }
  .ws-note[data-full="1"] .n-meta { display: block; margin-top: 4px; font-size: 13px; text-align: right; }
}
</style>
