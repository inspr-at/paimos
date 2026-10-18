<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The workspace status note (AEON-1494 decision 1, Tick's 10-18 amendment): one quiet
// paragraph an agent writes for the workspace, between the toolbar and Waiting on you.
// No box, no tint; its author and age sit right. At every width it shows one line,
// cut with "…" and followed by "more" when the note is longer. Exactly one line is
// reserved, so a new or changed note never moves the list. "more" expands in place
// (the person's click may push the list down, never the toolbar) and "less" folds it
// back. A new note starts on one line again. Without a note nothing renders (the parent).
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { agoText, type GoalStatusNote } from '../../lib/goals'

const props = defineProps<{ note: GoalStatusNote; now: number }>()

const body = ref<HTMLElement>()
const textEl = ref<HTMLElement>()
const full = ref(false)
const more = ref(false)
const by = computed(() => props.note.by_name.trim() || 'An agent')
const age = computed(() => agoText(props.note.set_at, props.now))

// "more" shows when the whole note does not fit the line on its own. The text's
// scroll width is its unclipped width; within a pixel of the line counts as too long,
// so a rounding error can only add "more", never hide the rest of a note.
function fit() {
  if (full.value) { more.value = true; return }
  const text = textEl.value, line = body.value
  if (text && line) more.value = text.scrollWidth + 1 > line.clientWidth
}
// "more" belongs to the note on screen; another note starts on one line.
watch(() => [props.note.text, props.note.set_at], () => { if (full.value) full.value = false; else fit() }, { flush: 'post' })
watch(full, fit, { flush: 'post' })

let observer: ResizeObserver | undefined, width = 0
onMounted(() => {
  fit()
  // Only a width change can re-cut the line; the expanded height is the person's.
  observer = new ResizeObserver(entries => { const w = Math.round(entries[0]?.contentRect.width ?? 0); if (w !== width) { width = w; fit() } })
  if (body.value) observer.observe(body.value)
  void document.fonts?.ready.then(fit)
})
onBeforeUnmount(() => observer?.disconnect())
</script>

<template>
  <section class="ws-note" :data-full="full ? 1 : 0" :aria-label="`Workspace status by ${by}, ${age}`">
    <p id="ws-note-text" ref="body" class="n-body">
      <span ref="textEl" class="n-text">{{ note.text }}</span>
      <button v-if="more" type="button" class="n-more" :aria-expanded="full" aria-controls="ws-note-text" @click="full = !full">{{ full ? 'less' : 'more' }}</button>
    </p>
    <p class="n-meta"><span class="n-by">{{ by }}</span> · {{ age }}</p>
  </section>
</template>

<style scoped>
/* One quiet paragraph in the goal grid: no box, no tint, ink-2. One line is kept, so a
   new note never moves the list; only "more" pushes what follows down. */
.ws-note { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; align-items: start; padding: 13px 0 12px; }
.n-body { grid-column: 2 / -4; display: flex; align-items: baseline; gap: 4px; min-width: 0; max-width: 92ch; height: 20px; margin: 0; font-size: 13.5px; line-height: 20px; color: var(--ink-2); }
.n-text { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.n-more { flex: none; position: relative; height: 20px; margin: 0 -3px 0 0; padding: 0 3px; border: 0; border-radius: 5px; appearance: none; background: transparent; color: var(--teal-ink); font-size: 13px; font-weight: 600; line-height: 20px; white-space: nowrap; }
@media (hover: hover) { .n-more:hover { background: var(--row-hover); } }
.ws-note[data-full="1"] .n-body { display: block; height: auto; overflow-wrap: anywhere; }
.ws-note[data-full="1"] .n-text { white-space: normal; }
.ws-note[data-full="1"] .n-more { display: inline-block; vertical-align: top; margin-left: 4px; }
.n-meta { grid-column: -4 / -1; justify-self: end; margin: 0; font-size: 12.5px; line-height: 20px; color: var(--ink-3); white-space: nowrap; }
.n-by { font-weight: 600; color: var(--ink-2); }
.n-more:focus-visible { outline: none; box-shadow: var(--focus-ring); }
/* Touch: "more" gets a 44 px hit area without changing its look. */
@media (max-width: 760px), (pointer: coarse) {
  .n-more::before { content: ''; position: absolute; top: 50%; left: 50%; width: max(100%, 44px); height: max(100%, 44px); transform: translate(-50%, -50%); }
}
/* Phones: the line and "more" across the width; expanded, the whole note with its byline below. */
@media (max-width: 760px) {
  .ws-note { display: flex; align-items: center; margin: 0 -8px; padding: 4px 14px 2px; min-height: 44px; }
  .n-meta { display: none; }
  .n-body { flex: 1 1 auto; gap: 8px; max-width: none; font-size: 14px; }
  .n-more { font-size: 14px; }
  .ws-note[data-full="1"] { display: block; padding: 10px 14px; }
  .ws-note[data-full="1"] .n-body { line-height: 21px; }
  .ws-note[data-full="1"] .n-more { height: auto; line-height: 21px; }
  .ws-note[data-full="1"] .n-meta { display: block; margin-top: 4px; font-size: 13px; text-align: right; }
}
</style>
