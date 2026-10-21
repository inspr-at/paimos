<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
import type { IconName } from '../AppIcon.vue'
// Business glyphs in the same set as AppIcon: 16×16 grid, stroke 1.8, round caps
// and joins, currentColor. Any other name is drawn by AppIcon itself.
export type BizGlyph = 'briefcase' | 'document' | 'building' | 'tag' | 'grip' | 'print' | 'download' | 'calendar' | 'lock' | 'seal' | 'history' | 'mail' | 'globe' | 'phone' | 'star' | 'pin' | 'plug' | 'note' | 'bell'
export type BizIconName = IconName | BizGlyph
const GLYPHS = new Set<string>(['briefcase', 'document', 'building', 'tag', 'grip', 'print', 'download', 'calendar', 'lock', 'seal', 'history', 'mail', 'globe', 'phone', 'star', 'pin', 'plug', 'note', 'bell'])
</script>
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'
withDefaults(defineProps<{ name: BizIconName; size?: number }>(), { size: 16 })
</script>

<template>
  <svg v-if="GLYPHS.has(name)" :width="size" :height="size" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
    <template v-if="name === 'briefcase'"><rect x="1.8" y="4.8" width="12.4" height="8.8" rx="1.8" /><path d="M5.8 4.8V3.6c0-.6.4-1 1-1h2.4c.6 0 1 .4 1 1v1.2M1.8 8.8h12.4M7 8.8v1.2h2V8.8" /></template>
    <template v-else-if="name === 'document'"><path d="M4.4 1.8h4.8l3 3v8.4c0 .6-.4 1-1 1H4.4c-.6 0-1-.4-1-1V2.8c0-.6.4-1 1-1Z" /><path d="M9 1.8v3.2h3.2M5.8 8.4h4.4M5.8 11h2.8" /></template>
    <template v-else-if="name === 'building'"><rect x="3" y="1.8" width="10" height="12.4" rx="1.4" /><path d="M6 5h.01M10 5h.01M6 8h.01M10 8h.01M6.8 14.2v-2.6h2.4v2.6" /></template>
    <template v-else-if="name === 'tag'"><path d="M8.6 1.8h4.4c.7 0 1.2.5 1.2 1.2v4.4L7.6 14a1.2 1.2 0 0 1-1.7 0L2 10.1a1.2 1.2 0 0 1 0-1.7Z" /><circle cx="11" cy="5" r=".9" fill="currentColor" stroke="none" /></template>
    <template v-else-if="name === 'grip'"><circle cx="6" cy="3.8" r="1.1" fill="currentColor" stroke="none" /><circle cx="10" cy="3.8" r="1.1" fill="currentColor" stroke="none" /><circle cx="6" cy="8" r="1.1" fill="currentColor" stroke="none" /><circle cx="10" cy="8" r="1.1" fill="currentColor" stroke="none" /><circle cx="6" cy="12.2" r="1.1" fill="currentColor" stroke="none" /><circle cx="10" cy="12.2" r="1.1" fill="currentColor" stroke="none" /></template>
    <template v-else-if="name === 'print'"><path d="M4.4 5.6V2.2h7.2v3.4" /><rect x="1.8" y="5.6" width="12.4" height="6" rx="1.4" /><path d="M4.4 9.4h7.2v4.4H4.4Z" /></template>
    <path v-else-if="name === 'download'" d="M8 2.4v8M4.6 7.2 8 10.6l3.4-3.4M2.8 13.6h10.4" />
    <template v-else-if="name === 'calendar'"><rect x="1.8" y="3" width="12.4" height="11.2" rx="1.8" /><path d="M1.8 6.6h12.4M5.2 1.8v2.4M10.8 1.8v2.4" /></template>
    <template v-else-if="name === 'lock'"><rect x="3" y="7" width="10" height="7.2" rx="1.6" /><path d="M5.4 7V5a2.6 2.6 0 0 1 5.2 0v2" /></template>
    <template v-else-if="name === 'seal'"><circle cx="8" cy="6.6" r="4.6" /><path d="m6 6.6 1.4 1.4 2.8-2.8M5.2 10.6l-.8 3.6L8 12.8l3.6 1.4-.8-3.6" /></template>
    <template v-else-if="name === 'history'"><path d="M2.6 8a5.4 5.4 0 1 0 1.6-3.8M2.4 2.4v2.8h2.8" /><path d="M8 5.2V8l2 1.3" /></template>
    <template v-else-if="name === 'mail'"><rect x="1.8" y="3.4" width="12.4" height="9.2" rx="1.6" /><path d="m2.4 4.4 5.6 4.4 5.6-4.4" /></template>
    <template v-else-if="name === 'globe'"><circle cx="8" cy="8" r="6.2" /><path d="M1.8 8h12.4M8 1.8c1.7 1.8 2.5 3.8 2.5 6.2s-.8 4.4-2.5 6.2C6.3 12.4 5.5 10.4 5.5 8S6.3 3.6 8 1.8Z" /></template>
    <path v-else-if="name === 'star'" d="m8 1.9 1.8 3.8 4.1.5-3 2.9.8 4.1L8 11.2l-3.7 2 .8-4.1-3-2.9 4.1-.5Z" />
    <template v-else-if="name === 'pin'"><path d="M8 14.2s4.6-4.1 4.6-7.8a4.6 4.6 0 0 0-9.2 0c0 3.7 4.6 7.8 4.6 7.8Z" /><circle cx="8" cy="6.4" r="1.7" /></template>
    <template v-else-if="name === 'plug'"><path d="M5.6 1.8v3M10.4 1.8v3M3.6 4.8h8.8v2.6a4.4 4.4 0 0 1-8.8 0ZM8 11.8v2.4" /></template>
    <template v-else-if="name === 'note'"><path d="M3.4 1.8h9.2c.6 0 1 .4 1 1v6.6l-4.2 4.8H3.4c-.6 0-1-.4-1-1V2.8c0-.6.4-1 1-1Z" /><path d="M9.4 14.2V10.4c0-.6.4-1 1-1h3.2M5.2 5.4h5.6M5.2 8h3.2" /></template>
    <template v-else-if="name === 'bell'"><path d="M3.4 11.6h9.2l-1.2-1.6V6.6a3.4 3.4 0 0 0-6.8 0V10Z" /><path d="M6.4 13.8a1.7 1.7 0 0 0 3.2 0" /></template>
    <path v-else d="M3.4 2.2h2.4l1.2 3-1.6 1a7.4 7.4 0 0 0 4.4 4.4l1-1.6 3 1.2v2.4c0 .7-.6 1.2-1.3 1.2A11.4 11.4 0 0 1 2.2 3.5c0-.7.5-1.3 1.2-1.3Z" />
  </svg>
  <AppIcon v-else :name="name as IconName" :size="size" />
</template>
