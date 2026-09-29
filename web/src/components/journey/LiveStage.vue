<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { ACTION_LONG, CALENDAR_VERSION, isImported, releaseStateLabel } from '../../lib/journey'
import { CALENDAR_DISPLAY_SCHEME } from '../../lib/version-copy'
import { useJourneyContext } from '../../lib/journeyContext'
import { plural } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import GateCard from './GateCard.vue'
import LaterCard from './LaterCard.vue'
import ReleaseList from './ReleaseList.vue'
import ReleaseTickets from './ReleaseTickets.vue'
import ReleaseVersion from './ReleaseVersion.vue'

// Live: what the release shipped, every release so far, and the next one.
const ctx = useJourneyContext()
const journey = computed(() => ctx.journey.value)
const next = computed(() => journey.value.next_action)
const walker = computed(() => ctx.data.walker.value.value)
const included = computed(() => (walker.value?.tickets ?? []).filter(t => t.included).length)
const releases = computed(() => ctx.data.releases.value)
const imported = computed(() => isImported(journey.value))
const stageState = computed(() => journey.value.stages.find(s => s.key === 'live')?.state ?? 'later')
const releasedCount = computed(() => releases.value.filter(r => releaseStateLabel(r.state) === 'Released').length)
// Nothing has gone live and there is no release to show: one card, and the way to where the journey is.
const bare = computed(() => !ctx.release.value && !releases.value.length && ctx.data.releaseNodes.status.value !== 'loading')
</script>

<template>
  <div v-if="bare" class="j-grid one narrow">
    <LaterCard v-if="stageState === 'later'" stage="live" detail="Nothing from this project has gone live here yet." />
    <section v-else class="j-card"><div class="j-empty"><strong>No release yet</strong><span>A release goes live once it is deployed and, where needed, its access is granted.</span></div></section>
  </div>
  <div v-else class="j-grid">
    <div class="j-col">
      <section class="j-card" aria-labelledby="live-tickets">
        <header class="j-card-head">
          <p id="live-tickets" class="eyebrow">{{ ctx.release.value ? `In ${ctx.releaseLabel.value.toLowerCase()} · ${plural(included, 'ticket')}` : 'In the release' }}</p>
          <ReleaseVersion v-if="ctx.release.value?.version && CALENDAR_VERSION.test(ctx.release.value.version)" class="ver" :version="ctx.release.value.version" :scheme="CALENDAR_DISPLAY_SCHEME" />
          <button type="button" class="btn sm" :disabled="!included" aria-keyshortcuts="w" @click="ctx.walk()"><AppIcon name="expand" :size="13" />Full screen</button>
        </header>
        <div v-if="!ctx.release.value" class="j-empty"><strong>No release yet</strong><span>A release goes live once it is deployed and, where needed, its access is granted.</span></div>
        <p v-else-if="ctx.data.walker.status.value === 'loading'" class="skeleton list-skel" role="status" aria-label="Loading the release" />
        <div v-else-if="ctx.data.walker.status.value === 'error'" class="j-empty" role="alert"><strong>This release could not be loaded</strong><span>{{ ctx.data.walker.error.value }}</span></div>
        <div v-else-if="!included" class="j-empty"><strong>No tickets in this release</strong><span v-if="imported">Paimos listed no tickets for it.</span></div>
        <ReleaseTickets v-else :plan="ctx.plan" :editable="false" only="included" compact :project-key="ctx.project.value.routeKey" :work-by-id="ctx.data.workById.value" @walk="t => ctx.walk(t.key)" @open="ctx.open" />
      </section>
      <section v-if="releases.length || ctx.data.releaseNodes.status.value === 'loading'" class="j-card" aria-labelledby="live-releases">
        <header class="j-card-head"><p id="live-releases" class="eyebrow">Releases · {{ releases.length }}</p></header>
        <p v-if="ctx.data.releaseNodes.status.value === 'loading' && !releases.length" class="skeleton list-skel" role="status" aria-label="Loading releases" />
        <ReleaseList v-else :releases="releases" :current-id="journey.current_release_id" :selected-id="ctx.release.value?.id ?? null" :now="ctx.now.value" :limit="8" @select="r => ctx.selectRelease(r.key)" />
      </section>
    </div>
    <div class="j-col">
      <GateCard
        v-if="next.key === 'plan_next_release'" eyebrow="Next" :title="next.label.replace(/^Plan /, '').replace(/^r/, 'R')"
        :action="{ label: ctx.next.value.label, disabled: ctx.next.value.disabled, busy: ctx.next.value.busy, tip: ctx.next.value.tip }" @act="ctx.runNext()"
      >
        <p>{{ ACTION_LONG.plan_next_release }}</p>
      </GateCard>
      <LaterCard
        v-else-if="ctx.release.value && !ctx.current.value" stage="live" eyebrow="Live now" :title="`${ctx.releaseLabel.value} is the latest released`"
        :detail="`${plural(releasedCount, 'release')} of ${releases.length} went live before the journey was recorded here. The next one goes live from here.`"
      />
      <LaterCard v-else stage="live" :detail="releases.length ? `${plural(releases.length, 'release')} so far.` : ''" />
    </div>
  </div>
</template>

<style scoped>
.list-skel { height: 140px; border-radius: 10px; }
.ver { min-height: 0; font-size: 11.5px; }
</style>
