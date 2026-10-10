<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { displayLanguage } from '../../lib/displayLanguage'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'
import KeyCap from '../KeyCap.vue'
import MarkdownBody from '../MarkdownBody.vue'
import { elapsed, liveWords, type LiveTurn } from './chatLive'

// The agent's reply while it streams (AEON-1071, design AEON-947 §3.6). It
// lives at the end of the thread inside the scroll area, so it only grows
// downward; the persisted final message replaces it in the same place.
const props = defineProps<{ turn: LiveTurn; name: string; now: number; canStop: boolean }>()
const words = computed(() => liveWords[displayLanguage()])
const open = ref(false)
watch(() => props.turn.started, () => { open.value = false })
const running = computed(() => !props.turn.ended)
const seconds = (ms: number) => (ms / 1000).toFixed(1).replace('.', displayLanguage() === 'de' ? ',' : '.') + ' s'
const toolTime = computed(() => seconds(props.turn.tools.reduce((sum, tool) => sum + Math.max(0, (tool.ended ?? props.now) - tool.started), 0)))
const foldLabel = computed(() => `${words.value.tools(props.turn.tools.length)} · ${toolTime.value}`)
</script>

<template>
  <section class="live-turn" :aria-label="words.live" :aria-busy="running" :data-state="turn.ended ? 'ended' : turn.state">
    <p class="live-meta">
      <Avatar :name="name" kind="agent" :size="18" />
      <span class="live-author" :title="name">{{ name }}</span>
      <span class="live-tag" tabindex="0" :data-tip="words.liveOnly" :aria-label="words.liveOnly">Live</span>
    </p>
    <template v-if="turn.tools.length">
      <!-- The toggle sits above the list it opens, so it never moves (AEON-541). -->
      <button v-if="!running" type="button" class="fold" :aria-expanded="open" :data-tip="words.liveOnly" @click="open = !open">
        <AppIcon name="chevron-right" :size="13" class="chev" />{{ foldLabel }}
      </button>
      <ul v-if="running || open" class="tools" :aria-label="foldLabel">
        <li v-for="tool in turn.tools" :key="tool.id" class="tool" :class="{ active: tool.status === 'in_progress' && running, failed: tool.status === 'failed' }">
          <AppIcon :name="tool.status === 'failed' ? 'alert' : tool.status === 'completed' ? 'check' : 'wrench'" :size="14" />
          <span class="tool-title" :title="tool.title">{{ tool.title }}</span>
          <span v-if="tool.status === 'failed'" class="tool-failed">{{ words.toolFailed }}</span>
          <span v-if="tool.ended !== undefined" class="tool-time">{{ seconds(tool.ended - tool.started) }}</span>
        </li>
      </ul>
    </template>
    <MarkdownBody v-if="turn.text" :body="turn.text" chat class="live-text" />
    <p v-if="turn.truncated" class="live-note">{{ words.truncated }}</p>
    <p v-else-if="turn.gap" class="live-note">{{ words.gap }}</p>
    <p v-if="running && turn.stop === 'requested'" class="working" role="status"><AppIcon name="stop" :size="14" />{{ words.stopping }}</p>
    <p v-else-if="running && turn.state === 'requires_action'" class="working" role="status"><AppIcon name="alert" :size="14" />{{ words.waiting }}</p>
    <p v-else-if="running" class="working">
      <AppIcon name="sparkle" :size="16" class="spark" />
      <span>{{ words.working }}</span><span class="num">{{ elapsed(now - turn.started) }}</span>
      <span v-if="canStop" class="esc-hint"><span aria-hidden="true">·</span><KeyCap k="Esc" /><span>{{ words.escStop }}</span></span>
    </p>
    <p v-else-if="!running && turn.stop === 'applied'" class="live-note stopped" role="status">{{ words.stopped }}</p>
  </section>
</template>

<style scoped>
.live-turn { min-width: 0; padding: 6px 0; margin-top: 10px; }
.live-meta { display: flex; align-items: center; gap: 6px; min-width: 0; margin-bottom: 4px; font-size: 11.5px; }
.live-author { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 650; color: var(--ink); }
.live-tag { padding: 1px 6px; border-radius: 999px; font-size: 10px; background: var(--chip-bg); color: var(--ink-2); white-space: nowrap; }
.tools { display: grid; gap: 1px; margin: 0 0 8px; padding: 0; list-style: none; }
.tool { display: flex; align-items: center; gap: 8px; min-height: 26px; min-width: 0; font-size: 13px; color: var(--ink-2); }
.tool > svg { flex: none; color: var(--ink-3); }
.tool.failed > svg, .tool-failed { color: var(--danger); }
.tool-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 12px var(--mono); color: var(--ink); }
.tool-failed { font-size: 11.5px; white-space: nowrap; }
.tool-time { margin-left: auto; padding-left: 8px; font-size: 11.5px; color: var(--ink-3); font-variant-numeric: tabular-nums; white-space: nowrap; }
.tool.active .tool-title { background: linear-gradient(90deg, var(--ink-3) 0%, var(--ink) 45%, var(--ink-3) 90%); background-size: 220% 100%; -webkit-background-clip: text; background-clip: text; color: transparent; animation: live-shimmer 1.8s linear infinite; }
.fold { display: inline-flex; align-items: center; gap: 6px; min-height: 28px; margin: 0 0 8px; padding: 0 10px 0 8px; border: 0; border-radius: 999px; background: var(--chip-bg); color: var(--ink-2); font: inherit; font-size: 12.5px; white-space: nowrap; }
.fold .chev { transition: transform .15s; }
.fold[aria-expanded="true"] .chev { transform: rotate(90deg); }
@media (hover: hover) { .fold:hover { color: var(--teal-ink); background: var(--row-hover); } }
.live-note { margin-top: 6px; font-size: 12px; color: var(--ink-3); }
.live-note.stopped { color: var(--ink-2); }
.working { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; min-height: 26px; margin-top: 4px; font-size: 12.5px; color: var(--ink-3); }
.working > svg { flex: none; }
.working .spark { color: var(--teal); animation: live-spark 3.2s ease-in-out infinite; }
.working .num { font-variant-numeric: tabular-nums; }
.esc-hint { display: inline-flex; align-items: center; gap: 6px; }
@keyframes live-shimmer { from { background-position: 100% 0; } to { background-position: -120% 0; } }
@keyframes live-spark { 0%, 100% { opacity: .55; transform: scale(.92); } 50% { opacity: 1; transform: scale(1); } }
@media (prefers-reduced-motion: reduce) { .working .spark, .tool.active .tool-title { animation: none; } .tool.active .tool-title { color: var(--ink); } }
@media (max-width: 720px) { .esc-hint { display: none; } }
</style>
