<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { listFreshness } from '../../lib/listFreshness'

const props = defineProps<{ updatedAt: number | null; untrusted: boolean; inline?: boolean }>()
const now = ref(Date.now())
const view = computed(() => listFreshness(props.updatedAt, props.untrusted, now.value))
let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => { timer = setInterval(() => { now.value = Date.now() }, 5_000) })
onBeforeUnmount(() => clearInterval(timer))
</script>

<template>
  <p class="list-freshness" :class="[view.state, { inline }]" :data-tip="view.tip" :aria-label="view.text" role="status">
    <span class="live-mark" aria-hidden="true" /><span v-clip-tip class="freshness-text">{{ view.text }}</span>
  </p>
</template>

<style scoped>
.list-freshness { display: inline-flex; align-items: center; gap: 7px; min-height: 28px; margin: 0; color: var(--ink-3); font-size: 12px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.list-freshness.stale { color: var(--gold-ink); }
.live-mark { width: 7px; height: 7px; border-radius: 50%; background: var(--st-backlog); flex: none; }
.live .live-mark { background: var(--ok); box-shadow: 0 0 0 3px color-mix(in srgb, var(--ok) 16%, transparent); }
.stale .live-mark { background: var(--gold); }
.inline { display: contents; }
.inline .live-mark { order: 0; }
.inline .freshness-text { order: 2; width: 14ch; overflow: hidden; text-overflow: ellipsis; }
@media (max-width: 1100px) { .inline .freshness-text { display: none; } }
</style>
