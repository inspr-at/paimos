<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref, useId } from 'vue'
import AppIcon from '../AppIcon.vue'

// A stage the project has left behind: one line of what happened, with the
// full view folded away until asked for.
defineProps<{ title: string; summary: string; label?: string }>()
const open = ref(false)
const id = useId()
</script>

<template>
  <section class="j-card history" :aria-labelledby="`${id}-title`">
    <header class="history-head">
      <span class="history-mark" aria-hidden="true"><AppIcon name="check" :size="13" /></span>
      <div class="history-text">
        <p class="eyebrow">History</p>
        <h3 :id="`${id}-title`">{{ title }}</h3>
        <p class="j-note">{{ summary }}</p>
      </div>
      <button type="button" class="btn sm ghost" :aria-expanded="open" :aria-controls="`${id}-body`" @click="open = !open">
        {{ open ? 'Fold away' : label ?? 'Show what happened' }}<AppIcon name="chevron" :size="12" class="chev" :class="{ up: open }" />
      </button>
    </header>
    <div v-if="open" :id="`${id}-body`" class="history-body"><slot /></div>
  </section>
</template>

<style scoped>
.history-head { display: flex; align-items: flex-start; gap: 12px; }
.history-mark { display: grid; place-items: center; flex-shrink: 0; width: 28px; height: 28px; border-radius: 8px; color: var(--secondary-ink); background: var(--secondary-tint-3); box-shadow: 0 0 0 1px color-mix(in srgb, var(--secondary-line) 50%, transparent); }
.history-text { display: grid; gap: 2px; flex: 1; min-width: 0; }
.history-text h3 { font-size: 16px; font-weight: 500; }
.chev { transition: transform .15s ease; }
.chev.up { transform: rotate(180deg); }
.history-body { display: grid; gap: 12px; padding-top: 6px; border-top: 1px solid var(--line); }
@media (max-width: 720px) { .history-head { flex-wrap: wrap; } }
</style>
