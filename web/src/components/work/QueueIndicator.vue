<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import type { QueuedTicket } from '../../lib/workQueue'
import AppIcon from '../AppIcon.vue'
import QueueDetails from './QueueDetails.vue'
const props = defineProps<{ entry: QueuedTicket; manual?: boolean }>()
const id = useId(), trigger = ref<HTMLElement>(), card = ref<HTMLElement>()
const showing = ref(false), x = ref(0), y = ref(0)
let timer: ReturnType<typeof setTimeout> | undefined
let parent: HTMLElement | null = null
const focus = () => void show()
onMounted(() => { parent = trigger.value?.closest('button') ?? null; parent?.addEventListener('focus', focus); parent?.addEventListener('blur', hide) })
async function show() {
  showing.value = true; await nextTick()
  if (!trigger.value || !card.value) return
  const rect = trigger.value.getBoundingClientRect(), width = card.value.offsetWidth, height = card.value.offsetHeight
  x.value = Math.min(Math.max(8, rect.left), innerWidth - width - 8)
  y.value = rect.bottom + height + 14 <= innerHeight ? rect.bottom + 6 : Math.max(8, rect.top - height - 6)
}
function hide() { clearTimeout(timer); showing.value = false }
function hover() { clearTimeout(timer); timer = setTimeout(() => void show(), 380) }
window.addEventListener('scroll', hide, true)
window.addEventListener('resize', hide)
onBeforeUnmount(() => { hide(); parent?.removeEventListener('focus', focus); parent?.removeEventListener('blur', hide); window.removeEventListener('scroll', hide, true); window.removeEventListener('resize', hide) })
void props
</script>
<template>
  <span ref="trigger" class="q-ind" role="img" :aria-label="`${entry.waiting_reason ? 'Waiting' : 'Queued'} #${entry.position}`" :class="{ wait: entry.waiting_reason }" :aria-describedby="id" @pointerenter="hover" @pointerleave="hide" @focusin="show" @focusout="hide" @pointerdown="hide">
    <AppIcon :name="entry.waiting_reason ? 'clock' : 'queue'" :size="entry.waiting_reason ? 12 : 13" /><b class="q-num">#{{ entry.position }}</b><span class="q-word">{{ entry.target_agent_id ? `for ${entry.expected_agent?.name ?? 'chosen agent'}` : entry.waiting_reason ? 'Waiting' : 'Queued' }}</span>
    <span :id="id" class="sr-only">Queued by {{ entry.by.name }} at {{ entry.at }}. {{ entry.waiting_reason }} Expected {{ entry.expected_agent?.name ?? 'next free agent' }}, {{ entry.expected_model }} {{ entry.expected_effort }}. {{ entry.expected_start ? `Starts ${entry.expected_start}` : 'No expected start yet' }}.</span>
  </span>
  <Teleport to="body"><div v-if="showing" ref="card" class="hcard pop" aria-hidden="true" :style="{ transform: `translate(${x}px, ${y}px)` }"><QueueDetails :entry="entry" :manual="manual" /></div></Teleport>
</template>
<style scoped>
.q-ind { display: inline-flex; align-items: center; gap: 6px; min-width: 0; color: var(--ink-2); font-size: 12.5px; }
.q-ind > svg { color: var(--teal-ink); }
.q-num { flex: none; font: 600 11.5px/1 var(--mono); color: var(--ink); }
.q-word { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.q-ind.wait { height: 20px; padding: 0 8px 0 6px; border-radius: 999px; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); }
.q-ind.wait > svg, .q-ind.wait .q-num { color: var(--queue-wait-ink); }
.hcard { position: fixed; z-index: 85; top: 0; left: 0; width: min(300px, calc(100vw - 16px)); padding: 11px 13px 12px; font-size: 12.5px; pointer-events: none; }
</style>
