<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { ImportCopy, ImportDraft } from '../../lib/importDraft'
import AppIcon from '../AppIcon.vue'

// What the new project receives besides its tickets, read-only: the summary
// (three lines, then Show all), the first three facts, the transcript and the
// knowledge entry they become. Phones fold it into one row. The toggles keep
// their widest label, so a button never changes width when pressed.
const props = defineProps<{ draft: ImportDraft | null; loading: boolean; t: ImportCopy }>()
const FACTS = 3
const summaryOpen = ref(false), factsOpen = ref(false), folded = ref(true), clamped = ref(false)
const summary = ref<HTMLElement>()
const facts = computed(() => props.draft?.facts ?? [])
const extra = computed(() => Math.max(0, facts.value.length - FACTS))
const along = computed(() => props.draft ? props.t.alongParts(!!props.draft.summary, facts.value.length, props.draft.messages > 0) : '')
// Show all is offered only when the clamp actually hides text.
function measure() { const el = summary.value; clamped.value = !!el && (summaryOpen.value || el.scrollHeight > el.clientHeight + 1) }
let observer: ResizeObserver | undefined
onMounted(() => { if (typeof ResizeObserver !== 'undefined' && summary.value) { observer = new ResizeObserver(measure); observer.observe(summary.value) } })
onBeforeUnmount(() => observer?.disconnect())
watch(() => [props.draft?.summary, props.loading], () => { summaryOpen.value = false; factsOpen.value = false; void nextTick(measure) })
</script>

<template>
  <section class="wcl" :class="{ open: !folded }" aria-labelledby="np-wcl-h">
    <h3 id="np-wcl-h" class="np-sec">{{ t.along }}</h3>
    <button type="button" class="wcl-toggle" :aria-expanded="!folded" aria-controls="np-wcl-body" :disabled="loading" @click="folded = !folded">
      <span class="t"><b>{{ t.along }}</b><span v-if="loading" class="sk-row"><span class="skeleton" style="width: 62%" /></span><span v-else>{{ along }}</span></span><AppIcon name="chevron-right" :size="16" />
    </button>
    <div id="np-wcl-body" class="wcl-body">
      <div v-if="loading || draft?.summary" class="pv">
        <div class="pv-lab"><b>{{ t.summary }}</b><button type="button" class="link-btn" :class="{ wait: loading || !clamped }" :aria-expanded="summaryOpen" :tabindex="loading || !clamped ? -1 : undefined" @click="summaryOpen = !summaryOpen"><span class="btn-label"><span :aria-hidden="summaryOpen || undefined">{{ t.showAll }}</span><span :aria-hidden="!summaryOpen || undefined">{{ t.showLess }}</span></span></button></div>
        <p ref="summary" class="pv-sum" :class="{ clamp: !summaryOpen }"><template v-if="loading"><span class="sk-row"><span class="skeleton" style="width: 96%" /></span><span class="sk-row"><span class="skeleton" style="width: 92%" /></span><span class="sk-row"><span class="skeleton" style="width: 58%" /></span></template><template v-else>{{ draft?.summary }}</template></p>
      </div>
      <div v-if="loading || facts.length" class="pv">
        <div class="pv-lab"><b>{{ t.facts }}</b><button v-if="loading || extra" type="button" class="link-btn" :class="{ wait: loading }" :aria-expanded="factsOpen" :tabindex="loading ? -1 : undefined" @click="factsOpen = !factsOpen"><span class="btn-label"><span :aria-hidden="factsOpen || undefined">{{ t.more(extra) }}</span><span :aria-hidden="!factsOpen || undefined">{{ t.fewer }}</span></span></button></div>
        <ul class="pv-facts" :class="{ all: factsOpen }">
          <template v-if="loading"><li v-for="width in ['88%', '80%', '72%']" :key="width" class="sk-li"><span class="sk-row"><span class="skeleton" :style="{ width }" /></span></li></template>
          <template v-else><li v-for="(fact, index) in facts" :key="index" :class="{ more: index >= FACTS }">{{ fact }}</li></template>
        </ul>
      </div>
      <p v-if="loading || draft?.messages" class="pv-att"><AppIcon name="paperclip" :size="14" /><span v-if="loading"><span class="sk-row"><span class="skeleton" style="width: 210px; max-width: 100%" /></span></span><span v-else>{{ t.transcript(draft?.messages ?? 0) }}</span></p>
      <p class="sh-note"><AppIcon name="info" :size="13" /><span>{{ t.note }}</span></p>
    </div>
  </section>
</template>

<style scoped>
.wcl { display: grid; gap: 14px; margin-top: 4px; padding-top: 20px; border-top: 1px solid var(--line); }
.np-sec { font: 600 15px/1.35 var(--font); letter-spacing: -.005em; color: var(--ink); }
.wcl-toggle { display: none; }
.wcl-body { display: grid; gap: 16px; }
.pv { display: grid; gap: 4px; }
.pv-lab { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 24px; }
.pv-lab > b { font-size: 13px; font-weight: 600; }
.link-btn { position: relative; display: inline-flex; align-items: center; height: 24px; margin-right: -8px; padding: 0 8px; border: 0; border-radius: 7px; background: transparent; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
.link-btn:hover { background: var(--row-hover); }
.link-btn.wait { visibility: hidden; }
.link-btn .btn-label { justify-items: end; }
.pv-sum { font-size: 13px; line-height: 1.55; color: var(--ink-2); overflow-wrap: anywhere; white-space: pre-line; }
.pv-sum.clamp { display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
.pv-facts { list-style: none; margin: 0; padding: 0; display: grid; }
.pv-facts li { position: relative; padding-left: 14px; font-size: 13px; line-height: 1.6; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.pv-facts li::before { content: ''; position: absolute; left: 2px; top: calc(.8em - 2px); width: 4px; height: 4px; border-radius: 50%; background: var(--ink-3); }
.pv-facts li.sk-li::before { display: none; }
.pv-facts li.sk-li { padding-left: 0; }
.pv-facts li.more { display: none; }
.pv-facts.all li.more { display: block; }
.pv-facts.all li { white-space: normal; }
.pv-att { display: flex; align-items: center; gap: 8px; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.pv-att > svg { color: var(--ink-3); }
.pv-att > span { min-width: 0; flex: 1; }
.sh-note { display: flex; gap: 8px; padding: 10px 12px; border-radius: 10px; background: var(--surface-sunken); font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.sh-note svg { flex: none; margin-top: 2px; }
/* Skeletons take the line box of the text they stand for. */
.sk-row { display: flex; align-items: center; height: 1lh; }
@media (max-width: 600px) {
  .wcl { gap: 0; padding-top: 0; }
  .wcl > .np-sec { display: none; }
  .wcl-toggle { display: flex; align-items: center; gap: 12px; width: calc(100% + 16px); min-height: 64px; margin: 0 -8px; padding: 10px 8px; border: 0; border-radius: 10px; background: transparent; color: var(--ink); text-align: left; }
  .wcl-toggle:active { background: var(--row-hover); }
  .wcl-toggle .t { display: grid; gap: 2px; flex: 1; min-width: 0; }
  .wcl-toggle .t b { font: 600 15px/1.35 var(--font); }
  .wcl-toggle .t > span { font-size: 12.5px; line-height: 1.5; color: var(--ink-3); }
  .wcl-toggle > svg { color: var(--ink-3); transition: transform .15s ease; }
  .wcl.open .wcl-toggle > svg { transform: rotate(90deg); }
  .wcl-body { display: none; }
  .wcl.open .wcl-body { display: grid; padding: 4px 0 8px; }
  .link-btn::before { content: ''; position: absolute; top: 50%; left: 50%; width: max(100%, 44px); height: max(100%, 44px); transform: translate(-50%, -50%); }
  .pv-facts li { white-space: normal; }
}
@media (prefers-reduced-motion: reduce) { .wcl-toggle > svg { transition: none; } }
</style>
