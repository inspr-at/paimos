<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { releaseStats } from '../../lib/releaseStats'
import type { Release } from '../../lib/releases'
import ReleaseCadence from './ReleaseCadence.vue'
import ReleaseStatCard from './ReleaseStatCard.vue'

// The cadence at a glance under the release history's title (AEON-488): one
// card that cycles through the stats, and the cadence chart beside it. What
// runs here is the title's status line, and today is the chart's last bar, so
// nothing repeats. Phones stack the two cards, inside the gutter.
const props = defineProps<{ releases: Release[]; now: number; compact?: boolean }>()
const stats = computed(() => releaseStats(props.releases, props.now))
</script>

<template>
  <div class="overview" :class="{ compact }">
    <ReleaseStatCard v-if="stats.length" :stats="stats" :compact="compact" />
    <ReleaseCadence :releases="releases" :now="now" :compact="compact" />
  </div>
</template>

<style scoped>
.overview { display: grid; grid-template-columns: minmax(320px, 400px) minmax(0, 1fr); gap: 14px; align-items: stretch; --card-glow: rgba(14, 111, 108, .35); }
.overview > :only-child { grid-column: 1 / -1; }
:root[data-theme="dark"] .overview { --card-glow: rgba(0, 0, 0, .55); }
@media (prefers-color-scheme: dark) { :root:not([data-theme="light"]) .overview { --card-glow: rgba(0, 0, 0, .55); } }
@media (max-width: 900px) { .overview { grid-template-columns: minmax(0, 1fr); } }
.overview.compact { grid-template-columns: minmax(0, 1fr); gap: 12px; }
</style>
