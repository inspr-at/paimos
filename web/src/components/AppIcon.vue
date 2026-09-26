<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
// One inline SVG set: 16×16 grid, stroke 1.8, round caps and joins, currentColor.
export type IconName =
  | 'sun' | 'moon' | 'user' | 'chevron' | 'chevron-right' | 'chevron-left' | 'chevron-up' | 'arrow' | 'arrow-up' | 'arrow-down'
  | 'logout' | 'tree' | 'compass' | 'search' | 'list' | 'plus' | 'close' | 'edit' | 'copy' | 'external' | 'filter' | 'check'
  | 'rows-comfortable' | 'rows-compact' | 'keyboard' | 'epic' | 'ticket' | 'task' | 'alert' | 'refresh' | 'sliders' | 'enter'
  | 'inbox' | 'folder' | 'layers' | 'archive' | 'clock' | 'expand' | 'collapse' | 'more' | 'link' | 'trash' | 'agent' | 'outline' | 'expand-all' | 'collapse-all' | 'arrow-left' | 'eye' | 'eye-off' | 'monitor' | 'key'
  | 'pause' | 'play' | 'stop' | 'shield' | 'send' | 'bolt' | 'gauge' | 'interrupt' | 'halt'
  | 'minus' | 'compare' | 'download' | 'paperclip' | 'image' | 'upload' | 'info' | 'journey'
  | 'users' | 'history' | 'sparkle' | 'wrench' | 'bug' | 'gear' | 'commit' | 'tag' | 'box' | 'rollback'
  | 'command' | 'option' | 'backspace'
  | 'book' | 'runbook' | 'guideline' | 'memory' | 'server' | 'folders' | 'terminal' | 'hash' | 'graph'
  | 'bookmark' | 'star' | 'calendar' | 'coin' | 'not' | 'sort' | 'select' | 'shift'
  | 'cards' | 'columns' | 'grip' | 'sort-name' | 'progress'
</script>
<script setup lang="ts">
withDefaults(defineProps<{ name: IconName; size?: number }>(), { size: 16 })
</script>

<template>
  <svg :width="size" :height="size" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
    <template v-if="name === 'sun'"><circle cx="8" cy="8" r="2.8" /><path d="M8 1.6v1.3M8 13.1v1.3M1.6 8h1.3M13.1 8h1.3M3.5 3.5l.9.9M11.6 11.6l.9.9M3.5 12.5l.9-.9M11.6 4.4l.9-.9" /></template>
    <path v-else-if="name === 'moon'" d="M13.6 9.6A5.9 5.9 0 0 1 6.4 2.4a6 6 0 1 0 7.2 7.2Z" />
    <template v-else-if="name === 'users'"><circle cx="6.2" cy="5.6" r="2.4" /><path d="M1.8 13.6c0-2.6 2-4.2 4.4-4.2s4.4 1.6 4.4 4.2M10.4 3.4a2.1 2.1 0 1 1 .9 4M12 9.6c1.4.4 2.4 1.6 2.4 3.6" /></template>
    <template v-else-if="name === 'user'"><circle cx="8" cy="5.4" r="2.6" /><path d="M2.8 14c0-2.9 2.3-4.6 5.2-4.6s5.2 1.7 5.2 4.6" /></template>
    <path v-else-if="name === 'chevron'" d="m4.5 6.3 3.5 3.5 3.5-3.5" />
    <path v-else-if="name === 'chevron-right'" d="m6.3 4.5 3.5 3.5-3.5 3.5" />
    <path v-else-if="name === 'chevron-left'" d="m9.7 4.5-3.5 3.5 3.5 3.5" />
    <path v-else-if="name === 'chevron-up'" d="m4.5 9.7 3.5-3.5 3.5 3.5" />
    <path v-else-if="name === 'arrow'" d="M2.8 8h10.4M9.2 4l4 4-4 4" />
    <template v-else-if="name === 'eye'"><path d="M1.6 8s2.4-4.6 6.4-4.6S14.4 8 14.4 8s-2.4 4.6-6.4 4.6S1.6 8 1.6 8Z" /><circle cx="8" cy="8" r="1.9" /></template>
    <template v-else-if="name === 'monitor'"><rect x="1.8" y="2.6" width="12.4" height="8.6" rx="1.8" /><path d="M5.6 13.6h4.8M8 11.2v2.4" /></template>
    <template v-else-if="name === 'key'"><circle cx="5.4" cy="10.6" r="3" /><path d="M7.6 8.4 13.6 2.4M11.4 4.6l1.8 1.8M9.8 6.2l1.4 1.4" /></template>
    <template v-else-if="name === 'eye-off'"><path d="M6.2 3.7A6.5 6.5 0 0 1 8 3.4c4 0 6.4 4.6 6.4 4.6a11 11 0 0 1-1.7 2.2M4.3 4.7A10.6 10.6 0 0 0 1.6 8s2.4 4.6 6.4 4.6c1 0 1.9-.3 2.7-.7M6.7 6.6a1.9 1.9 0 0 0 2.7 2.7M2.4 2.4l11.2 11.2" /></template>
    <path v-else-if="name === 'arrow-left'" d="M13.2 8H2.8M6.8 4l-4 4 4 4" />
    <path v-else-if="name === 'arrow-up'" d="M8 13.2V2.8M4 6.8l4-4 4 4" />
    <path v-else-if="name === 'arrow-down'" d="M8 2.8v10.4M4 9.2l4 4 4-4" />
    <path v-else-if="name === 'logout'" d="M6 2.5H3.3a.8.8 0 0 0-.8.8v9.4c0 .4.4.8.8.8H6M10.2 5l3 3-3 3M5.8 8h7.4" />
    <template v-else-if="name === 'tree'"><rect x="5.8" y="1.6" width="4.4" height="3.8" rx="1.1" /><rect x="1.6" y="10.8" width="4.2" height="3.6" rx="1.1" /><rect x="10.2" y="10.8" width="4.2" height="3.6" rx="1.1" /><path d="M8 5.4v2.4M3.7 10.8V8.6h8.6v2.2" /></template>
    <template v-else-if="name === 'search'"><circle cx="7" cy="7" r="4.4" /><path d="m10.3 10.3 3.5 3.5" /></template>
    <path v-else-if="name === 'list'" d="M5.6 4h8M5.6 8h8M5.6 12h8M2.4 4h.01M2.4 8h.01M2.4 12h.01" />
    <path v-else-if="name === 'plus'" d="M8 3v10M3 8h10" />
    <path v-else-if="name === 'close'" d="m4.2 4.2 7.6 7.6M11.8 4.2l-7.6 7.6" />
    <path v-else-if="name === 'edit'" d="m10.4 2.6 3 3L6 13l-3.6.6L3 10Z" />
    <template v-else-if="name === 'copy'"><rect x="5.6" y="5.6" width="7.8" height="7.8" rx="1.8" /><path d="M10.4 5.6V4a1.4 1.4 0 0 0-1.4-1.4H4A1.4 1.4 0 0 0 2.6 4v5c0 .8.6 1.4 1.4 1.4h1.6" /></template>
    <path v-else-if="name === 'external'" d="M9.4 2.6h4v4M13.4 2.6 7.8 8.2M12 9.6v2.6c0 .7-.6 1.2-1.2 1.2H3.8c-.7 0-1.2-.5-1.2-1.2V5.2c0-.7.5-1.2 1.2-1.2h2.6" />
    <path v-else-if="name === 'filter'" d="M2.4 3.4h11.2L9.3 8.6v4.2l-2.6 1V8.6Z" />
    <path v-else-if="name === 'check'" d="m3.4 8.4 3 3 6.2-6.8" />
    <template v-else-if="name === 'rows-comfortable'"><rect x="2" y="2.6" width="12" height="4.4" rx="1.3" /><rect x="2" y="9" width="12" height="4.4" rx="1.3" /></template>
    <path v-else-if="name === 'rows-compact'" d="M2.4 2.8h11.2M2.4 6.2h11.2M2.4 9.8h11.2M2.4 13.2h11.2" />
    <template v-else-if="name === 'keyboard'"><rect x="1.4" y="3.8" width="13.2" height="8.6" rx="1.8" /><path d="M4.4 6.8h.01M6.8 6.8h.01M9.2 6.8h.01M11.6 6.8h.01M5 9.6h6" /></template>
    <path v-else-if="name === 'epic'" d="M9 1.8 3.6 9.2h4.1l-.9 5 5.6-7.4H8.2Z" />
    <template v-else-if="name === 'ticket'"><rect x="2.6" y="2.4" width="10.8" height="11.2" rx="2" /><path d="M5.4 6h5.2M5.4 8.6h5.2M5.4 11.2h2.8" /></template>
    <template v-else-if="name === 'task'"><rect x="1.8" y="1.8" width="6" height="4.6" rx="1.3" /><path d="M4.8 6.4v4.3c0 .5.4.9.9.9h1.9" /><rect x="7.6" y="9.4" width="6.6" height="4.6" rx="1.3" /></template>
    <template v-else-if="name === 'alert'"><circle cx="8" cy="8" r="6.2" /><path d="M8 4.8v3.6M8 11.1h.01" /></template>
    <path v-else-if="name === 'refresh'" d="M13.2 8a5.2 5.2 0 1 1-1.6-3.8M13.4 2.4v3.2h-3.2" />
    <template v-else-if="name === 'sliders'"><path d="M2.4 4.6h6.6M12.6 4.6h1M2.4 11.4h1M7 11.4h6.6" /><circle cx="10.8" cy="4.6" r="1.7" /><circle cx="5.2" cy="11.4" r="1.7" /></template>
    <path v-else-if="name === 'enter'" d="M13 3.4v4.4c0 .9-.7 1.6-1.6 1.6H3.2M6 6.6 3.2 9.4 6 12.2" />
    <template v-else-if="name === 'inbox'"><path d="M2 9.4 4.2 3.6h7.6L14 9.4v3a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1Z" /><path d="M2 9.4h3.3l1 1.5h3.4l1-1.5H14" /></template>
    <path v-else-if="name === 'folder'" d="M2 4.6c0-.6.4-1 1-1h3.2l1.5 1.5H13c.6 0 1 .4 1 1V12c0 .6-.4 1-1 1H3c-.6 0-1-.4-1-1Z" />
    <path v-else-if="name === 'layers'" d="M8 2 14 5 8 8 2 5ZM2 8.2l6 3 6-3M2 11.2l6 3 6-3" />
    <template v-else-if="name === 'cards'"><rect x="2" y="2" width="5.2" height="5.2" rx="1.4" /><rect x="8.8" y="2" width="5.2" height="5.2" rx="1.4" /><rect x="2" y="8.8" width="5.2" height="5.2" rx="1.4" /><rect x="8.8" y="8.8" width="5.2" height="5.2" rx="1.4" /></template>
    <template v-else-if="name === 'columns'"><rect x="1.8" y="2.6" width="12.4" height="10.8" rx="1.8" /><path d="M6 2.6v10.8M10 2.6v10.8" /></template>
    <template v-else-if="name === 'grip'"><circle cx="6" cy="3.8" r="1.1" fill="currentColor" stroke="none" /><circle cx="10" cy="3.8" r="1.1" fill="currentColor" stroke="none" /><circle cx="6" cy="8" r="1.1" fill="currentColor" stroke="none" /><circle cx="10" cy="8" r="1.1" fill="currentColor" stroke="none" /><circle cx="6" cy="12.2" r="1.1" fill="currentColor" stroke="none" /><circle cx="10" cy="12.2" r="1.1" fill="currentColor" stroke="none" /></template>
    <template v-else-if="name === 'archive'"><rect x="1.8" y="2.8" width="12.4" height="3.4" rx="1" /><path d="M3 6.2v6.2c0 .6.4 1 1 1h8c.6 0 1-.4 1-1V6.2M6.4 9h3.2" /></template>
    <path v-else-if="name === 'expand'" d="M9.4 2.6h4v4M13.4 2.6 9.2 6.8M6.6 13.4h-4v-4M2.6 13.4l4.2-4.2" />
    <path v-else-if="name === 'collapse'" d="M13.4 2.6 9.4 6.6M9.4 3v3.6H13M2.6 13.4l4-4M6.6 13V9.4H3" />
    <template v-else-if="name === 'more'"><circle cx="3.4" cy="8" r="1.25" fill="currentColor" stroke="none" /><circle cx="8" cy="8" r="1.25" fill="currentColor" stroke="none" /><circle cx="12.6" cy="8" r="1.25" fill="currentColor" stroke="none" /></template>
    <path v-else-if="name === 'link'" d="M6.6 9.4 9.4 6.6M7.2 4.4l1.2-1.2a2.6 2.6 0 0 1 3.7 3.7l-1.2 1.2M8.8 11.6l-1.2 1.2a2.6 2.6 0 0 1-3.7-3.7l1.2-1.2" />
    <path v-else-if="name === 'outline'" d="M2.4 3.2h7M4.2 3.2v9.4M4.2 7.9h9.4M4.2 12.6h9.4" />
    <path v-else-if="name === 'expand-all'" d="m4.4 3.6 3.6 3.2 3.6-3.2M4.4 9.2 8 12.4l3.6-3.2" />
    <path v-else-if="name === 'collapse-all'" d="m4.4 6.8 3.6-3.2 3.6 3.2M4.4 12.4 8 9.2l3.6 3.2" />
    <template v-else-if="name === 'agent'"><rect x="2.6" y="5" width="10.8" height="8.2" rx="2.4" /><path d="M8 5V2.6M6 9h.01M10 9h.01M1 8.4v2M15 8.4v2" /><circle cx="8" cy="2.2" r=".6" fill="currentColor" stroke="none" /></template>
    <path v-else-if="name === 'trash'" d="M2.8 4.2h10.4M6.2 4.2V2.8h3.6v1.4M4.2 4.2l.7 8.6c.1.7.6 1.2 1.3 1.2h3.6c.7 0 1.2-.5 1.3-1.2l.7-8.6M6.8 7v4.2M9.2 7v4.2" />
    <path v-else-if="name === 'pause'" d="M6 3.8v8.4M10 3.8v8.4" />
    <path v-else-if="name === 'play'" d="M5.4 3.6v8.8l7-4.4z" stroke-linejoin="round" />
    <rect v-else-if="name === 'stop'" x="3.8" y="3.8" width="8.4" height="8.4" rx="1.6" />
    <template v-else-if="name === 'shield'"><path d="M8 1.9 13 3.8v3.9c0 3-2.1 5.4-5 6.4-2.9-1-5-3.4-5-6.4V3.8Z" /><path d="m5.9 8 1.5 1.5 2.8-2.9" /></template>
    <template v-else-if="name === 'interrupt'"><circle cx="8" cy="8" r="6.2" /><path d="M6.5 5.7v4.6M9.5 5.7v4.6" /></template>
    <template v-else-if="name === 'halt'"><path d="M5.5 1.9h5l3.6 3.6v5l-3.6 3.6h-5l-3.6-3.6v-5Z" /><rect x="6" y="6" width="4" height="4" rx=".6" fill="currentColor" stroke="none" /></template>
    <path v-else-if="name === 'send'" d="M14 2 7.2 8.8M14 2 9.6 14 7.2 8.8 2 6.4Z" />
    <path v-else-if="name === 'bolt'" d="M9 1.8 3.6 9h4l-.8 5.2L12.4 7h-4Z" />
    <template v-else-if="name === 'gauge'"><path d="M2.4 11.6a5.6 5.6 0 1 1 11.2 0" /><path d="m8 11.6 2.6-3.4" /></template>
    <path v-else-if="name === 'minus'" d="M3.6 8h8.8" />
    <template v-else-if="name === 'compare'"><rect x="1.9" y="2.8" width="12.2" height="10.4" rx="1.8" /><path d="M8 1.6v12.8" /><path d="m5 6.4-1.4 1.6L5 9.6M11 6.4l1.4 1.6L11 9.6" /></template>
    <path v-else-if="name === 'download'" d="M8 2.2v8.2M4.8 7.4 8 10.6l3.2-3.2M2.8 13.4h10.4" />
    <path v-else-if="name === 'upload'" d="M8 10.6V2.4M4.8 5.6 8 2.4l3.2 3.2M2.8 13.4h10.4" />
    <path v-else-if="name === 'paperclip'" d="m13 7.4-5 5a3.2 3.2 0 0 1-4.6-4.6l5.3-5.2a2.1 2.1 0 0 1 3 3L6.4 10.8a1 1 0 0 1-1.5-1.4l4.6-4.6" />
    <template v-else-if="name === 'image'"><rect x="2" y="2.6" width="12" height="10.8" rx="1.8" /><circle cx="5.8" cy="6.2" r="1.2" /><path d="m2.4 12 3.6-3.4 2.6 2.4 2.2-2 3 2.8" /></template>
    <template v-else-if="name === 'journey'"><circle cx="3.2" cy="11.6" r="1.7" /><circle cx="8" cy="4.6" r="1.7" /><circle cx="12.8" cy="10.2" r="1.7" /><path d="m4.3 10.1 2.6-4M9.2 6l2.4 2.7" /></template>
    <template v-else-if="name === 'clock'"><circle cx="8" cy="8" r="6.2" /><path d="M8 4.6V8l2.2 1.4" /></template>
    <template v-else-if="name === 'history'"><path d="M2.7 8a5.3 5.3 0 1 0 1.6-3.8" /><path d="M2.5 2.6v2.6h2.6" /><path d="M8 5.1v3.1l2 1.3" /></template>
    <path v-else-if="name === 'sparkle'" d="M8 1.9c.6 3.3 2.8 5.5 6.1 6.1-3.3.6-5.5 2.8-6.1 6.1-.6-3.3-2.8-5.5-6.1-6.1 3.3-.6 5.5-2.8 6.1-6.1Z" />
    <path v-else-if="name === 'wrench'" d="M13.75 5.01A3.4 3.4 0 1 1 10.99 2.25l.61 2.15ZM8 8 2.6 13.4" />
    <template v-else-if="name === 'gear'"><path d="M12.4 6.2 14.2 6.8 14.2 9.2 12.4 9.8 13.2 11.5 11.5 13.2 9.8 12.4 9.2 14.2 6.8 14.2 6.2 12.4 4.5 13.2 2.8 11.5 3.6 9.8 1.8 9.2 1.8 6.8 3.6 6.2 2.8 4.5 4.5 2.8 6.2 3.6 6.8 1.8 9.2 1.8 9.8 3.6 11.5 2.8 13.2 4.5Z" /><circle cx="8" cy="8" r="1.9" /></template>
    <template v-else-if="name === 'bug'"><path d="M4.8 8.4a3.2 3.2 0 0 1 6.4 0v2.2a3.2 3.2 0 0 1-6.4 0Z" /><path d="M6.7 5.4 5.9 3.2M9.3 5.4l.8-2.2M4.8 9.6H2.3M11.2 9.6h2.5M5.2 12.4l-1.9 1.2M10.8 12.4l1.9 1.2" /></template>
    <template v-else-if="name === 'commit'"><circle cx="8" cy="8" r="2.5" /><path d="M1.6 8h3.9M10.5 8h3.9" /></template>
    <template v-else-if="name === 'tag'"><path d="M2.2 2.9v4.3c0 .3.1.6.4.8l5.6 5.6c.4.4 1 .4 1.4 0l3.9-3.9c.4-.4.4-1 0-1.4L7.9 2.7a1.1 1.1 0 0 0-.8-.3H2.7a.5.5 0 0 0-.5.5Z" /><circle cx="5.2" cy="5.4" r=".6" fill="currentColor" stroke="none" /></template>
    <template v-else-if="name === 'box'"><path d="M8 1.8 13.6 4.9v6.2L8 14.2l-5.6-3.1V4.9Z" /><path d="M2.4 4.9 8 8l5.6-3.1M8 8v6.2" /></template>
    <path v-else-if="name === 'rollback'" d="M5.6 3.2 2.8 6l2.8 2.8M2.8 6h6.6a3.8 3.8 0 0 1 0 7.6H6.8" />
    <!-- Keys drawn on keycaps: Command, Option and Backspace (never their text symbols). -->
    <path v-else-if="name === 'command'" d="M6 6V4.2A1.8 1.8 0 1 0 4.2 6h7.6A1.8 1.8 0 1 0 10 4.2v7.6a1.8 1.8 0 1 0 1.8-1.8H4.2A1.8 1.8 0 1 0 6 11.8Z" />
    <path v-else-if="name === 'option'" d="M2.2 4h3.6l4.4 8h3.6M9.6 4h4.2" />
    <template v-else-if="name === 'backspace'"><path d="M5.8 3.4h6.8c.8 0 1.4.6 1.4 1.4v6.4c0 .8-.6 1.4-1.4 1.4H5.8L1.8 8Z" /><path d="m7.6 6.2 3.6 3.6M11.2 6.2l-3.6 3.6" /></template>
    <!-- Knowledge: the tab, its five kinds, the agent command line and heading anchors. -->
    <template v-else-if="name === 'book'"><path d="M8 4.2C6.4 3 4.2 2.6 2.2 3v9.2c2-.4 4.2 0 5.8 1.2 1.6-1.2 3.8-1.6 5.8-1.2V3c-2-.4-4.2 0-5.8 1.2Z" /><path d="M8 4.2v9.2" /></template>
    <template v-else-if="name === 'runbook'"><path d="M5.6 2.9H4.4c-.9 0-1.6.7-1.6 1.6v8.1c0 .9.7 1.6 1.6 1.6h7.2c.9 0 1.6-.7 1.6-1.6V4.5c0-.9-.7-1.6-1.6-1.6h-1.2" /><rect x="5.6" y="1.8" width="4.8" height="2.4" rx=".9" /><path d="m5.4 8.1 1.3 1.3 2.3-2.4M5.4 11.7h5.2" /></template>
    <template v-else-if="name === 'guideline'"><path d="M8 1.8v12.4M5.4 14.2h5.2" /><path d="M8 3.2h4.1l1.7 1.7-1.7 1.7H8M8 7.9H3.9L2.2 9.6l1.7 1.7H8" /></template>
    <template v-else-if="name === 'memory'"><path d="M8 1.9a4.3 4.3 0 0 0-2.7 7.6c.5.4.8 1 .8 1.6v.4h3.8v-.4c0-.6.3-1.2.8-1.6A4.3 4.3 0 0 0 8 1.9Z" /><path d="M6.3 14.1h3.4" /></template>
    <template v-else-if="name === 'server'"><rect x="2.2" y="2.4" width="11.6" height="4.6" rx="1.4" /><rect x="2.2" y="9" width="11.6" height="4.6" rx="1.4" /><path d="M4.8 4.7h.01M4.8 11.3h.01M8 4.7h3.2M8 11.3h3.2" /></template>
    <template v-else-if="name === 'folders'"><path d="M4.6 5.2V4c0-.6.4-1 1-1h2.3l1.2 1.2h4.1c.6 0 1 .4 1 1v5.4c0 .6-.4 1-1 1h-.9" /><path d="M1.8 7.2c0-.6.4-1 1-1h2.3l1.2 1.2h4c.6 0 1 .4 1 1v4c0 .6-.4 1-1 1H2.8c-.6 0-1-.4-1-1Z" /></template>
    <template v-else-if="name === 'terminal'"><rect x="1.8" y="2.6" width="12.4" height="10.8" rx="1.8" /><path d="m4.7 6.3 2 1.7-2 1.7M8.3 10.1h3" /></template>
    <template v-else-if="name === 'graph'"><circle cx="4" cy="4.6" r="1.9" /><circle cx="12" cy="3.8" r="1.6" /><circle cx="11.2" cy="12" r="2.1" /><circle cx="3.6" cy="11.8" r="1.5" /><path d="M5.8 4.4 10.4 4M5.5 5.8l4.3 4.7M11.7 5.4l-.3 4.5M5.1 11.8h4" /></template>
    <path v-else-if="name === 'hash'" d="M6.3 2.4 5.1 13.6M10.9 2.4 9.7 13.6M2.8 5.9h10.6M2.6 10.1h10.6" />
    <path v-else-if="name === 'bookmark'" d="M4.4 2.2h7.2c.3 0 .6.3.6.6v11L8 11.1l-4.2 2.7v-11c0-.3.3-.6.6-.6Z" />
    <path v-else-if="name === 'star'" d="m8 2 1.8 3.7 4 .6-2.9 2.8.7 4L8 11.2l-3.6 1.9.7-4-2.9-2.8 4-.6Z" />
    <template v-else-if="name === 'calendar'"><rect x="2.2" y="3.2" width="11.6" height="10.6" rx="2.2" /><path d="M2.2 6.8h11.6M5.4 1.8v2.8M10.6 1.8v2.8" /></template>
    <template v-else-if="name === 'coin'"><circle cx="8" cy="8" r="6" /><path d="M9.9 5.9c-.4-.6-1.1-1-1.9-1-1.1 0-1.9.6-1.9 1.4 0 2 3.8 1 3.8 3.1 0 .8-.8 1.4-1.9 1.4-.8 0-1.6-.4-2-1M8 3.8v1.1M8 11v1.2" /></template>
    <template v-else-if="name === 'not'"><circle cx="8" cy="8" r="5.8" /><path d="m3.9 12.1 8.2-8.2" /></template>
    <!-- Project sorts: A to Z beside a downward arrow; a ring three quarters round. -->
    <template v-else-if="name === 'sort-name'"><path d="M1.9 7.2 4.1 2l2.2 5.2M2.6 5.6h3" /><path d="M2.2 9.3h3.9l-3.9 4.8h3.9" /><path d="M11.4 2.4v11.2M9 11.2l2.4 2.4 2.4-2.4" /></template>
    <template v-else-if="name === 'progress'"><circle cx="8" cy="8" r="5.8" stroke-opacity=".32" /><path d="M8 2.2a5.8 5.8 0 1 1-5.8 5.8" /></template>
    <path v-else-if="name === 'sort'" d="M4.8 2.6v10.8M2.4 11l2.4 2.4 2.4-2.4M11.2 13.4V2.6M8.8 5l2.4-2.4L13.6 5" />
    <template v-else-if="name === 'select'"><rect x="2.2" y="2.2" width="11.6" height="11.6" rx="3" /><path d="m5.2 8.2 2 2 3.6-4.2" /></template>
    <path v-else-if="name === 'shift'" d="M8 2.2 2.6 8h2.9v5.2h5V8h2.9Z" />
    <template v-else-if="name === 'info'"><circle cx="8" cy="8" r="6.2" /><path d="M8 7.4v3.8M8 4.9v.05" /></template>
    <template v-else><circle cx="8" cy="8" r="6.2" /><path d="M10.6 5.4 9.2 9.2l-3.8 1.4 1.4-3.8Z" /></template>
  </svg>
</template>
