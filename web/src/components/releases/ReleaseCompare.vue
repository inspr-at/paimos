<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { compare, emptyNotesLine, forReading, presentChanges, releasedAt, releaseTitle, span, type Release, type ReleaseReading } from '../../lib/releases'
import { absoluteTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import CalendarVersion from '../CalendarVersion.vue'
import ReleaseChanges from './ReleaseChanges.vue'
import TicketChips from './TicketChips.vue'

// Two releases side by side: everything that shipped after the older one, up to
// and including the newer one. The same Highlights / Details reading as one release.
const props = defineProps<{ releases: Release[]; from: string; to: string | null; repository: string; query: string; locale: string | null; reading: ReleaseReading }>()
const emit = defineEmits<{ swap: []; exit: [] }>()
const result = computed(() => props.to && props.to !== props.from ? compare(props.releases, props.from, props.to) : null)
const count = computed(() => result.value ? result.value.groups.features.length + result.value.groups.fixes.length + result.value.groups.other.length : 0)
// The range reads like one release: a block per linked ticket, and a chip only
// for tickets that head no block.
const full = computed(() => result.value ? presentChanges(result.value.changes, props.locale) : null)
const lines = computed(() => full.value ? forReading(full.value, props.reading) : null)
const shown = computed(() => lines.value ? lines.value.features.length + lines.value.fixes.length + lines.value.other.length : 0)
const rangeTitle = (r: Release) => releaseTitle(r, props.locale)
const chipTickets = computed(() => {
  if (!result.value || !full.value) return []
  const lined = new Set([...full.value.features, ...full.value.fixes].map(line => line.key))
  return result.value.tickets.filter(key => !lined.has(key))
})
const between = computed(() => {
  if (!result.value) return ''
  const find = (v: string) => props.releases.find(r => r.version === v)
  const a = find(result.value.older), b = find(result.value.newer)
  const ta = a ? Date.parse(releasedAt(a) ?? '') : NaN, tb = b ? Date.parse(releasedAt(b) ?? '') : NaN
  return Number.isNaN(ta) || Number.isNaN(tb) ? '' : span(Math.abs(tb - ta))
})
</script>

<template>
  <section class="compare" aria-labelledby="compare-title">
    <p class="eyebrow">Compare releases</p>
    <h2 id="compare-title" class="pair">
      <span class="end"><span class="tag">From</span><CalendarVersion :value="result?.older ?? from" /></span>
      <AppIcon name="arrow" :size="16" class="to-arrow" />
      <span class="end" :class="{ waiting: !result }"><span class="tag">To</span><CalendarVersion v-if="result" :value="result.newer" /><span v-else class="pick">Pick a second release</span></span>
    </h2>
    <div class="actions">
      <button v-if="result" type="button" class="btn sm" @click="emit('swap')"><AppIcon name="refresh" :size="12" />Swap</button>
      <button type="button" class="btn sm ghost" @click="emit('exit')">Done</button>
    </div>
    <template v-if="result">
      <p class="facts">
        <span><b>{{ result.releases.length }}</b> {{ result.releases.length === 1 ? 'release' : 'releases' }}</span>
        <span><b>{{ count }}</b> {{ count === 1 ? 'change' : 'changes' }}</span>
        <span><b>{{ result.tickets.length }}</b> {{ result.tickets.length === 1 ? 'ticket' : 'tickets' }}</span>
        <span v-if="between">over <b>{{ between }}</b></span>
      </p>
      <TicketChips v-if="chipTickets.length" :tickets="chipTickets" />
      <section class="included" aria-labelledby="compare-included">
        <h3 id="compare-included" class="included-h">Releases in this range</h3>
        <ul>
          <li v-for="r in result.releases" :key="r.version"><CalendarVersion :value="r.version" class="inc-version" /><span v-if="rangeTitle(r)" class="inc-headline">{{ rangeTitle(r) }}</span><span v-else-if="releasedAt(r)" class="inc-date">{{ absoluteTime(releasedAt(r)!) }}</span></li>
        </ul>
      </section>
      <ReleaseChanges v-if="shown && lines" :presented="lines" :repository="repository" :query="query" :reading="reading" class="changes" />
      <p v-else-if="reading === 'highlights' && count" class="none">{{ emptyNotesLine(locale) }}</p>
      <p v-else class="none">No changes are recorded between these releases.</p>
    </template>
    <p v-else class="hint">
      Move through the list with <kbd class="keycap">j</kbd> <kbd class="keycap">k</kbd> and press <kbd class="keycap">Enter</kbd>, or click a release. The changes from the older to the newer one are added up here.
    </p>
  </section>
</template>

<style scoped>
.compare { display: grid; align-content: start; gap: 12px; max-width: 820px; }
.eyebrow { margin: 0; }
.pair { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 14px; font: 500 20px/1.3 var(--mono); color: var(--ink); }
.end { display: inline-flex; align-items: center; gap: 10px; min-height: 44px; padding: 6px 14px 6px 8px; border-radius: 12px; background: var(--glass); border: 1px solid var(--glass-edge); box-shadow: 0 0 0 1px var(--line); }
.end.waiting { border-style: dashed; border-color: var(--line-2); box-shadow: none; background: transparent; }
.tag { display: inline-grid; place-items: center; height: 22px; padding: 0 7px; border-radius: 7px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 10px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; }
.pick { font: 500 14px/1.3 var(--font); color: var(--ink-3); }
.to-arrow { color: var(--ink-3); }
.actions { display: flex; flex-wrap: wrap; gap: 6px; }
.facts { display: flex; flex-wrap: wrap; gap: 6px 18px; font-size: 13px; color: var(--ink-2); }
.facts b { color: var(--ink); font-weight: 650; font-variant-numeric: tabular-nums; }
.changes { margin-top: 6px; }
.included { display: grid; gap: 6px; margin-top: 4px; }
.included-h { font: 500 10.5px/1.5 var(--mono); letter-spacing: .16em; text-transform: uppercase; color: var(--ink-3); }
.included ul { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.included li { display: grid; grid-template-columns: 170px minmax(0, 1fr); align-items: baseline; gap: 12px; padding: 5px 0; border-bottom: 1px solid var(--line); font-size: 13.5px; color: var(--ink); }
.included li:last-child { border-bottom: 0; }
.inc-version { font-size: 12px; }
.inc-headline { overflow-wrap: anywhere; }
.inc-date { color: var(--ink-3); }
.none, .hint { font-size: 13.5px; color: var(--ink-2); line-height: 1.7; }
@media (max-width: 760px) { .pair { font-size: 15px; } .included li { grid-template-columns: minmax(0, 1fr); gap: 2px; } }
</style>
