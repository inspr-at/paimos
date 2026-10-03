<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { vClipTip } from '../lib/clipTip'

const props = withDefaults(defineProps<{ text: string; lines?: number; expanded?: boolean }>(), { lines: 2, expanded: false })
const id = useId(), clipped = ref(false), revealed = ref(false)
const full = computed(() => props.expanded || revealed.value)
function measured(value: boolean) { if (!full.value) clipped.value = value }
watch(() => props.text, () => { revealed.value = false; clipped.value = false })
</script>

<template>
  <div class="expandable-text">
    <!-- The reveal control stays above the text that grows away from it. -->
    <button v-if="!expanded && (clipped || revealed)" type="button" class="reveal-text" :aria-expanded="revealed" :aria-controls="id" @click.stop="revealed = !revealed">
      <!-- Both labels contribute to intrinsic sizing; only the active one is read. -->
      <span class="reveal-label" :class="{ active: !revealed }" :aria-hidden="revealed">Show all</span>
      <span class="reveal-label" :class="{ active: revealed }" :aria-hidden="!revealed">Show less</span>
    </button>
    <p :id="id" v-clip-tip="{ text, onClip: measured }" class="text" :class="{ full }" :style="{ '--preview-lines': lines }">{{ text }}</p>
  </div>
</template>

<style scoped>
.text { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: var(--preview-lines); line-clamp: var(--preview-lines); overflow: hidden; overflow-wrap: anywhere; white-space: pre-wrap; margin: 0; }
.text.full { display: block; overflow: visible; -webkit-line-clamp: unset; line-clamp: unset; }
.reveal-text { display: grid; align-items: center; width: max-content; min-width: 44px; max-width: 100%; min-height: 28px; margin: 0 0 4px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font: inherit; font-size: 12px; text-align: left; cursor: pointer; }
.reveal-label { grid-area: 1 / 1; min-width: 0; overflow-wrap: anywhere; visibility: hidden; }
.reveal-label.active { visibility: visible; }
/* Reserve the whole target in flow, clear of the preceding action's hit area. */
@media (pointer: coarse) { .reveal-text { min-height: 44px; margin-top: 4px; } }
.reveal-text:focus-visible, .text:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; }
</style>
