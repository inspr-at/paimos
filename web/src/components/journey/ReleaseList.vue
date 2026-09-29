<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { CALENDAR_VERSION, releaseName, releaseStateLabel, type ReleaseRef } from '../../lib/journey'
import { CALENDAR_DISPLAY_SCHEME } from '../../lib/version-copy'
import { absoluteTime, relativeTime, statusMeta } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import StatusIcon from '../work/StatusIcon.vue'
import ReleaseVersion from './ReleaseVersion.vue'

// The project's releases as a compact timeline, newest first: number, version,
// state and when. Choosing one shows its tickets; the current one is marked.
const props = defineProps<{ releases: ReleaseRef[]; currentId: string | null; selectedId: string | null; now: number; limit?: number }>()
const emit = defineEmits<{ select: [release: ReleaseRef] }>()
const all = ref(false)
const newest = computed(() => [...props.releases].reverse())
const shown = computed(() => all.value || !props.limit ? newest.value : newest.value.slice(0, props.limit))
</script>

<template>
  <div class="release-list">
    <ol class="j-timeline" aria-label="Releases, newest first">
      <li v-for="release in shown" :key="release.id" :class="{ sel: release.id === selectedId }">
        <span class="dot" :class="release.id === currentId ? 'run' : statusMeta(release.state).closed ? 'ok' : ''" aria-hidden="true">
          <AppIcon v-if="release.id === currentId" name="bolt" :size="11" />
          <StatusIcon v-else :state="release.state" :size="11" />
        </span>
        <button type="button" class="what pick" :aria-current="release.id === selectedId ? 'true' : undefined" @click="emit('select', release)">
          <span class="line"><b>{{ releaseName(release) }}</b><span v-if="release.id === currentId" class="j-chip teal">Current</span></span>
          <small class="line">
            <ReleaseVersion v-if="release.version && CALENDAR_VERSION.test(release.version)" class="ver" :version="release.version" :scheme="CALENDAR_DISPLAY_SCHEME" :interactive="false" />
            <span v-else-if="release.version" class="mono ver-text">{{ release.version }}</span>
            <span class="mono key">{{ release.key }}</span>
            <span>· {{ releaseStateLabel(release.state) }}</span>
          </small>
        </button>
        <time :datetime="release.created_at" :data-tip="absoluteTime(release.created_at)">{{ relativeTime(release.created_at, { now }) }}</time>
      </li>
    </ol>
    <button v-if="limit && releases.length > limit" type="button" class="btn sm ghost more" @click="all = !all">{{ all ? 'Show fewer' : `Show all ${releases.length} releases` }}</button>
  </div>
</template>

<style scoped>
.release-list { display: grid; grid-template-columns: minmax(0, 1fr); min-width: 0; gap: 6px; }
.j-timeline, .what { grid-template-columns: minmax(0, 1fr); min-width: 0; }
.j-timeline > li { min-width: 0; }
.pick { padding: 2px 6px; margin: -2px -6px; border: 0; border-radius: 8px; background: transparent; text-align: left; cursor: pointer; }
.pick:hover { background: var(--row-hover); }
.pick:focus-visible { box-shadow: var(--focus-ring); }
li.sel .pick { background: var(--row-selected); }
.line { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 8px; min-width: 0; }
.line b { min-width: 0; overflow-wrap: anywhere; font-weight: 600; color: var(--ink); }
.ver { min-height: 0; font-size: 11.5px; }
.ver-text, .key { font-size: 11px; color: var(--ink-3); }
.more { justify-self: start; }
</style>
