<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import '../../styles/crm.css'
import { diffCounts, lineDiff } from '../../lib/crm'
import AppIcon from '../AppIcon.vue'

// The notes as they are against a proposal, line by line: removed lines on a red
// tint, added ones on a green tint, each marked for screen readers too.
const props = defineProps<{ before: string; after: string; label: string }>()
const lines = computed(() => lineDiff(props.before, props.after))
const counts = computed(() => diffCounts(lines.value))
</script>

<template>
  <figure class="diff">
    <figcaption class="diff-head">
      <span>{{ label }}</span>
      <span v-if="!lines.some(line => line.kind === 'summary')" class="counts dot-list"><span>{{ counts.added }} added</span><span>{{ counts.removed }} removed</span></span>
    </figcaption>
    <ol class="lines" :aria-label="label">
      <li v-for="(line, i) in lines" :key="i" class="line" :class="line.kind">
        <span class="gutter" aria-hidden="true"><AppIcon v-if="line.kind === 'add'" name="plus" :size="11" /><AppIcon v-else-if="line.kind === 'remove'" name="minus" :size="11" /></span>
        <span v-if="line.kind === 'add' || line.kind === 'remove'" class="sr-only">{{ line.kind === 'add' ? 'Added:' : 'Removed:' }}</span>
        <span class="text">{{ line.text || ' ' }}</span>
      </li>
      <li v-if="!lines.length" class="line same"><span class="gutter" /><span class="text empty">Both are empty.</span></li>
    </ol>
  </figure>
</template>

<style scoped>
.diff { margin: 0; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); overflow: hidden; }
.diff-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 8px 12px; border-bottom: 1px solid var(--line); font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.counts { letter-spacing: .04em; text-transform: none; font-size: 11.5px; color: var(--ink-2); }
.lines { max-height: min(46vh, 420px); margin: 0; padding: 6px 0; overflow: auto; list-style: none; font: 13px/1.55 var(--mono); font-variant-ligatures: none; }
.line { display: grid; grid-template-columns: 26px minmax(0, 1fr); align-items: start; padding: 1px 12px 1px 0; color: var(--ink); }
.line .text { white-space: pre-wrap; overflow-wrap: anywhere; }
.line .empty { color: var(--ink-3); font-family: var(--font); }
.gutter { display: grid; place-items: center; height: 20px; color: var(--ink-3); }
.line.add { background: color-mix(in oklab, var(--ok) 13%, transparent); }
.line.add .gutter { color: var(--ok); }
.line.remove { background: var(--danger-bg); }
.line.remove .gutter { color: var(--danger); }
.line.remove .text { text-decoration: line-through; text-decoration-color: color-mix(in oklab, var(--danger) 55%, transparent); }
</style>
