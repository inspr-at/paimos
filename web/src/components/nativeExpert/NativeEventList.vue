<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onBeforeUpdate, onUpdated, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import { correlations, eventTypes, type EventRow } from '../../lib/nativeExpert/rows'
import type { NativeApproval } from '../../lib/nativeSessionContract'

const props = defineProps<{
  rows: EventRow[]; retained: number; evicted: number; notice: string; status: string
  before: string | null; loading: boolean; watching: boolean; canStart: boolean; canInspect: () => boolean; follow: boolean
}>()
const emit = defineEmits<{ earlier: []; start: []; stop: []; follow: [value: boolean]; review: [approval: NativeApproval] }>()
const scroll = ref<HTMLElement>(), expanded = ref(new Set<string>()), copyResult = ref('')
let anchor: { id: string; y: number } | null = null, copying = 0, disposed = false
const time = (at: string) => { const date = new Date(at); return Number.isNaN(date.valueOf()) ? '—' : date.toLocaleTimeString('en-GB', { hour12: false }) }
const icon = (row: EventRow) => eventTypes.find(type => type.key === row.type)!.icon
const raw = (row: EventRow) => JSON.stringify(row.events.length === 1 ? row.events[0] : row.events, null, 2)
function toggle(row: EventRow) {
  if (!props.canInspect()) return
  emit('follow', false)
  const next = new Set(expanded.value)
  if (next.has(row.id)) next.delete(row.id); else next.add(row.id)
  expanded.value = next; copyResult.value = ''
}
async function copy(row: EventRow) {
  if (!props.canInspect()) return
  const turn = ++copying, text = raw(row)
  copyResult.value = ''
  try {
    await navigator.clipboard.writeText(text)
    if (!disposed && turn === copying && props.canInspect()) copyResult.value = 'Copied'
  } catch { if (!disposed && turn === copying && props.canInspect()) copyResult.value = 'Could not copy. Select the JSON below.' }
}
function review(row: EventRow) {
  const event = row.events.at(-1)!
  if (props.canInspect() && event.kind === 'approval') emit('review', event.payload)
}
function scrolled() {
  const el = scroll.value
  if (el) emit('follow', el.scrollHeight - el.scrollTop - el.clientHeight <= 2)
}
async function jump() { emit('follow', true); await nextTick(); if (scroll.value) scroll.value.scrollTop = scroll.value.scrollHeight }
watch(() => props.follow, follow => { if (follow) void jump() })
onBeforeUpdate(() => {
  anchor = null
  if (!scroll.value || props.follow) return
  const top = scroll.value.getBoundingClientRect().top
  const row = Array.from(scroll.value.querySelectorAll<HTMLElement>('[data-event-row]')).find(el => el.getBoundingClientRect().bottom > top)
  if (row) anchor = { id: row.dataset.eventRow!, y: row.getBoundingClientRect().top }
})
onUpdated(() => {
  const el = scroll.value
  if (!el) return
  if (props.follow) el.scrollTop = el.scrollHeight
  else if (anchor) {
    const row = Array.from(el.querySelectorAll<HTMLElement>('[data-event-row]')).find(row => row.dataset.eventRow === anchor!.id)
    if (row) el.scrollTop += row.getBoundingClientRect().top - anchor.y
  }
  anchor = null
})
onBeforeUnmount(() => { disposed = true; copying++ })
</script>

<template>
  <div class="event-main">
    <div class="replay-controls">
      <span class="retained">Showing the last <span class="count">{{ retained }}</span> frames</span>
      <button class="btn sm ghost" type="button" :disabled="loading || before === null || !watching" @click="emit('earlier')">Load earlier</button>
      <button class="btn sm ghost" type="button" :disabled="!watching && !canStart" @click="watching ? emit('stop') : emit('start')">
        <AppIcon :name="watching ? 'eye-off' : 'eye'" :size="13" />
        <span class="btn-label"><span :aria-hidden="watching">Watch live</span><span :aria-hidden="!watching">Stop viewing</span></span>
      </button>
    </div>
    <div ref="scroll" class="event-scroll" tabindex="0" aria-label="Events, oldest first" @scroll="scrolled">
      <p v-if="evicted" class="notice" role="status">{{ evicted }} frames left the local buffer. This is a bounded live view, not complete history.</p>
      <p v-if="notice" class="notice" role="status">{{ notice }}</p>
      <ol class="event-list">
        <li v-for="row in rows" :key="row.id" class="event" :class="{ ask: row.type === 'asks', gap: row.name === 'gap' }">
          <div class="event-line">
            <button class="event-row" type="button" :data-event-row="row.id" :aria-expanded="expanded.has(row.id)" :title="`${row.name} · ${row.text}`" @click="toggle(row)">
              <time class="event-time" :datetime="row.events[0]!.observed_at">{{ time(row.events[0]!.observed_at) }}</time>
              <AppIcon :name="icon(row)" :size="16" />
              <span class="event-summary"><strong>{{ row.name }}</strong><span>{{ row.text }}</span></span>
              <span class="event-measure">{{ row.measure }}</span>
              <AppIcon class="event-chevron" name="chevron-right" :size="12" />
            </button>
            <button v-if="row.events.at(-1)!.kind === 'approval' && (row.events.at(-1)!.payload as NativeApproval).desk_item_id" class="btn sm event-review" type="button" @click="review(row)">Review</button>
          </div>
          <div v-if="expanded.has(row.id)" class="event-detail">
            <div class="raw-head">
              <span>Raw event · seq {{ row.events.at(-1)!.seq }}</span>
              <button class="btn sm ghost" type="button" @click="copy(row)"><AppIcon name="copy" :size="13" />Copy JSON</button>
            </div>
            <p class="copy-result" role="status">{{ copyResult }}</p>
            <p v-if="row.text" class="event-text">{{ row.text }}</p>
            <p v-if="row.measure" class="faint">{{ row.measure }}</p>
            <dl class="correlations"><div v-for="[name, value] in correlations(row)" :key="name"><dt>{{ name }}</dt><dd>{{ value }}</dd></div></dl>
            <template v-for="event in row.events" :key="event.seq">
              <template v-if="event.kind === 'expert'">
                <p v-if="event.payload.omitted">Native body omitted · {{ event.payload.original_bytes }} original bytes.</p>
                <div v-if="event.payload.redacted_paths.length" class="redactions"><span>Redacted paths</span><ul><li v-for="path in event.payload.redacted_paths" :key="path">{{ path }}</li></ul></div>
                <pre v-if="!event.payload.omitted" class="event-json" aria-label="Native envelope">{{ event.payload.frame_json }}</pre>
              </template>
            </template>
            <pre class="event-json" tabindex="0" aria-label="C0 event envelope">{{ raw(row) }}</pre>
          </div>
        </li>
      </ol>
      <p class="event-tail" role="status"><AppIcon :name="watching ? 'pulse' : 'info'" :size="13" />{{ status }}</p>
    </div>
    <button v-if="watching && !follow" class="btn jump" type="button" @click="jump"><AppIcon name="arrow-down" :size="14" />Jump to live</button>
  </div>
</template>

<style scoped>
.event-main { flex: 1; min-height: 0; display: flex; flex-direction: column; position: relative; }
.replay-controls { display: flex; align-items: center; flex: none; gap: 4px; min-height: 44px; padding: 4px 12px; font-size: 12px; color: var(--ink-3); }
.retained { flex: 1; min-width: 0; }
.count { font-variant-numeric: tabular-nums; }
.replay-controls button { flex: none; }
.event-scroll { flex: 1; min-height: 0; padding: 4px 8px 12px; overflow: auto; overscroll-behavior: contain; overflow-anchor: none; }
.notice { margin: 0 12px 8px; color: var(--warn-ink); font-size: 12px; overflow-wrap: anywhere; }
.event-list { display: grid; list-style: none; margin: 0; padding: 0; }
.event + .event { border-top: 1px solid var(--line); }
.event-line { display: flex; align-items: center; min-width: 0; border-radius: var(--radius-row); }
.event-row { display: grid; grid-template-columns: 66px 20px minmax(0, 1fr) auto 14px; align-items: center; column-gap: 8px; flex: 1; min-width: 0; height: 36px; padding: 4px 8px; border: 0; border-radius: var(--radius-row); background: transparent; color: var(--ink); font-size: 12.5px; text-align: left; }
.event-row:hover { background: var(--row-hover); }
.event-time { font: 500 11.5px var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; white-space: nowrap; }
.event-summary { display: flex; align-items: baseline; gap: 6px; min-width: 0; overflow: hidden; white-space: nowrap; }
.event-summary strong { flex: none; max-width: 100%; font-weight: 650; overflow: hidden; text-overflow: ellipsis; }
.event-summary > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--ink-2); }
.event-measure { max-width: 24ch; overflow: hidden; text-overflow: ellipsis; font: 11.5px var(--mono); color: var(--ink-3); white-space: nowrap; }
.event-chevron { color: var(--ink-3); }
[aria-expanded="true"] .event-chevron { transform: rotate(90deg); }
.ask .event-line { background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }
.gap .event-row { color: var(--warn-ink); }
.event-review { margin-right: 8px; flex: none; }
.event-detail { display: grid; gap: 8px; margin: 2px 8px 12px 102px; padding: 10px 0; min-width: 0; font-size: 12.5px; }
.raw-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-width: 0; color: var(--ink-3); }
.raw-head > span { min-width: 0; overflow-wrap: anywhere; }
.raw-head button { flex: none; }
.copy-result { min-height: 1.5em; color: var(--ink-3); }
.event-text { white-space: pre-wrap; overflow-wrap: anywhere; }
.correlations { display: grid; gap: 4px; margin: 0; }
.correlations > div { display: grid; grid-template-columns: minmax(0, 12ch) minmax(0, 1fr); gap: 10px; }
.correlations dt { color: var(--ink-3); overflow-wrap: anywhere; }
.correlations dd { margin: 0; font-family: var(--mono); overflow-wrap: anywhere; }
.redactions { overflow-wrap: anywhere; }
.redactions ul { margin: 4px 0; padding-left: 18px; }
.event-json { margin: 0; max-height: 240px; overflow: auto; padding: 8px 10px; border-radius: var(--radius-row); background: var(--code-bg); font: 11.5px/1.55 var(--mono); color: var(--ink); white-space: pre-wrap; overflow-wrap: anywhere; }
.event-tail { display: flex; align-items: flex-start; gap: 6px; min-height: 36px; margin: 6px 12px 0; font-size: 12px; color: var(--ink-3); }
.event-tail svg { margin-top: 3px; }
.jump { position: absolute; z-index: 1; left: 50%; bottom: max(14px, env(safe-area-inset-bottom)); transform: translateX(-50%); background: var(--surface-raised); box-shadow: var(--shadow-pop); white-space: nowrap; }
.jump:active { transform: translateX(-50%); }
@container native-expert (max-width: 560px) {
  .event-row { grid-template-columns: 58px 20px minmax(0, 1fr) 14px; column-gap: 6px; padding: 4px 6px; }
  .event-measure { display: none; }
  .event-detail { margin-left: 8px; }
  .replay-controls { flex-wrap: wrap; justify-content: space-between; }
  .retained { flex-basis: 100%; }
}
@media (pointer: coarse), (max-width: 600px) {
  .event-row, .replay-controls button, .raw-head button, .event-review, .jump { min-height: 44px; }
}
</style>
