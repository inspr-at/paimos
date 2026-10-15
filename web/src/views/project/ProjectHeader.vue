<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">

import { isClipped, vClipTip } from '../../directives/clipTip'
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import ProjectStatusCounts from '../../components/work/ProjectStatusCounts.vue'
import { PROJECT_COLUMN_BY_ID } from '../../lib/projectColumns'
import { absoluteTime, relativeTime } from '../../lib/work'
import { queueHours } from '../../lib/workQueue'
import AppIcon from '../../components/AppIcon.vue'
import StatusIcon from '../../components/work/StatusIcon.vue'
import WorkBar from '../../components/work/WorkBar.vue'
import HeaderGlimpse from '../../components/work/HeaderGlimpse.vue'
import { toRefs } from 'vue'
import type { Project } from '../../stores/projects'
import { useProjectPageContext } from './context'
const { project } = toRefs(defineProps<{ project: Project }>())
const { counts, selectCountGroup, selectCountStatus, projectId, queueAnchor, queueSnapshot, queueError, headerDensity, ticketsHeader, filters, headerGraph, glimpseShown, glimpseActive, now, graphActive, showViewBar, figuresBusy, figuresFailed, figuresNarrowed, retryFigures, workHead, workBarSlots, workHideLabel } = useProjectPageContext()
const descriptionExpanded = ref(false)
const descriptionClipped = ref(false)
const descriptionBlock = ref<HTMLElement>()
const descriptionMore = ref<HTMLButtonElement>()
const descriptionText = ref<HTMLElement>()
const descriptionProbe = ref<HTMLElement>()
const descriptionNeedsPanel = ref(false)
const descriptionFull = ref<HTMLElement>()
const descriptionRoom = ref(0)
// Description disclosure owns its measurement; full text may exceed the
// viewport, so it belongs in a focusable scroll region rather than a tooltip.
watch([descriptionText, descriptionProbe, () => project.value?.description], ([element, probe], _, onCleanup) => {
  if (!element) return
  const measure = () => {
    descriptionClipped.value = isClipped(element)
    const rect = element.getBoundingClientRect()
    const room = Math.max(rect.top - 16, innerHeight - rect.bottom - 16)
    descriptionNeedsPanel.value = innerWidth <= 720 || matchMedia('(pointer: coarse)').matches || (probe?.offsetHeight ?? 0) > room
    if (!descriptionClipped.value) descriptionExpanded.value = false
  }
  const observer = new ResizeObserver(measure)
  observer.observe(element)
  if (probe) observer.observe(probe)
  window.addEventListener('resize', measure)
  measure()
  onCleanup(() => { observer.disconnect(); window.removeEventListener('resize', measure) })
}, { flush: 'post' })
function closeDescription() {
  descriptionExpanded.value = false
  descriptionMore.value?.focus()
}
async function toggleDescription() {
  descriptionRoom.value = Math.max(44, innerHeight - (descriptionBlock.value?.getBoundingClientRect().bottom ?? 0) - 12)
  const openedFor = projectId.value
  descriptionExpanded.value = !descriptionExpanded.value
  if (descriptionExpanded.value) {
    await nextTick()
    if (descriptionExpanded.value && projectId.value === openedFor) descriptionFull.value?.focus({ preventScroll: true })
  }
}
function dismissDescription(event: PointerEvent) {
  if (event.target instanceof Node && !descriptionBlock.value?.contains(event.target)) descriptionExpanded.value = false
}
watch([projectId, () => project.value?.description], () => {
  descriptionExpanded.value = false
  descriptionClipped.value = false
})

onMounted(() => document.addEventListener('pointerdown', dismissDescription))
onBeforeUnmount(() => document.removeEventListener('pointerdown', dismissDescription))
</script>

<template>
  <header class="project-head" :class="{ 'glimpse-room': !ticketsHeader && glimpseActive && !showViewBar }">
    <div class="head-flex" :class="{ 'with-glimpse': !ticketsHeader && glimpseActive }">
    <div class="head-main">
      <div class="title-line">
    <span class="key-badge big">{{ project.routeKey }}</span>
    <h1 id="project-title" v-clip-tip="project.title" :title="project.title" :aria-label="project.title">{{ project.title }}</h1>
    <span v-if="project.frozen" class="chip state-chip">Frozen</span>
    <span v-else-if="project.archived" class="chip state-chip">Archived</span>
    <RouterLink v-if="can('nodes.write', project.id)" class="icon-btn sm head-edit" :to="`/p/${encodeURIComponent(project.routeKey)}/settings#f-name`" aria-label="Edit name and description in Settings" data-tip="Edit in Settings › General"><AppIcon name="edit" :size="14" /></RouterLink>
      </div>
      <div v-if="project.description" ref="descriptionBlock" class="description-block" @keydown.esc.stop.prevent="closeDescription">
    <button v-if="descriptionClipped && descriptionNeedsPanel" ref="descriptionMore" type="button" class="description-more" :aria-expanded="descriptionExpanded" aria-controls="project-description-full" @click="toggleDescription">{{ descriptionExpanded ? 'Less' : 'More' }}</button>
    <p id="project-description" ref="descriptionText" v-clip-tip="descriptionNeedsPanel ? '' : project.description" class="description">{{ project.description }}</p>
    <div class="description-measure" aria-hidden="true"><p ref="descriptionProbe" class="description-probe">{{ project.description }}</p></div>
    <div v-if="descriptionExpanded" id="project-description-full" ref="descriptionFull" class="description-full" :style="{ maxHeight: `min(40dvh, ${descriptionRoom}px)` }" role="region" aria-label="Full project description" tabindex="0" @keydown.stop @keydown.esc.prevent="closeDescription">{{ project.description }}</div>
      </div>
    </div>
    <div v-if="ticketsHeader" class="activity header-activity"><span>Active <time :datetime="project.last_activity" :data-tip="absoluteTime(project.last_activity)">{{ relativeTime(project.last_activity, { now, long: true }) }}</time></span>
      <button v-if="queueSnapshot?.items.length || queueError" type="button" class="stat q-stat" aria-haspopup="dialog" :aria-expanded="!!queueAnchor" :aria-label="queueError ? 'Queue read failed. Open the work queue to retry' : `${queueSnapshot?.items.length} queued. Open the work queue`" :data-tip="queueSnapshot ? queueHours(queueSnapshot) : 'Open the work queue to retry'" @click="queueAnchor = queueAnchor ? null : $event.currentTarget as HTMLElement"><AppIcon :name="queueError ? 'alert' : 'queue'" :size="12" /><template v-if="queueError">Queue unavailable</template><template v-else><b>{{ queueSnapshot?.items.length }}</b> queued</template></button>
    </div>
    <HeaderGlimpse v-if="headerGraph && !graphActive" :project-id="project.id" :project-key="project.routeKey" :ticket-count="counts?.total ?? 0" :enabled="headerGraph" @active="glimpseShown = $event" />
    <div v-if="counts" class="head-stats" :aria-label="`${counts.open} open, ${counts.progress} in progress, ${counts.done} done of ${counts.total}`">
      <ProjectStatusCounts v-if="ticketsHeader" :summary="project" :filters="filters" :density="headerDensity" @group="selectCountGroup" @status="selectCountStatus" />
      <div v-if="!ticketsHeader" class="stat-line">
    <span class="stat" :data-tip="PROJECT_COLUMN_BY_ID.get('open')!.tip"><StatusIcon state="open" :size="11" /><b>{{ counts.open.toLocaleString('en-GB') }}</b> open</span>
    <span class="stat" :data-tip="PROJECT_COLUMN_BY_ID.get('doing')!.tip"><StatusIcon state="in_progress" :size="11" /><b>{{ counts.progress.toLocaleString('en-GB') }}</b> doing</span>
    <span class="stat" :data-tip="PROJECT_COLUMN_BY_ID.get('done')!.tip"><StatusIcon state="done" :size="11" /><b>{{ counts.done.toLocaleString('en-GB') }}</b> done</span>
    <button v-if="queueSnapshot?.items.length || queueError" type="button" class="stat q-stat" aria-haspopup="dialog" :aria-expanded="!!queueAnchor" :aria-label="queueError ? 'Queue read failed. Open the work queue to retry' : `${queueSnapshot?.items.length} queued. Open the work queue`" @click="queueAnchor = queueAnchor ? null : $event.currentTarget as HTMLElement"><AppIcon :name="queueError ? 'alert' : 'queue'" :size="12" /><template v-if="queueError">Queue unavailable</template><template v-else><b>{{ queueSnapshot?.items.length }}</b> queued</template></button>
      </div>
      <p v-if="!ticketsHeader && queueSnapshot?.items.length" class="q-warn" :class="{ on: queueSnapshot.capacity.warning || (queueSnapshot.capacity.hours ?? 0) >= 4 }"><AppIcon name="clock" :size="12" />{{ queueHours(queueSnapshot) }}</p>
      <WorkBar
    class="head-work" :header="workHead" :busy="figuresBusy" :failure="figuresFailed" :narrowed="figuresNarrowed" :slots="workBarSlots" :fallback-leaves="counts.total"
    :density="ticketsHeader && headerDensity === 'comfortable' ? 'comfortable' : 'compact'" :hide-label="workHideLabel" @retry="retryFigures"
      />
      <p v-if="!ticketsHeader" class="activity">Active <time :datetime="project.last_activity" :data-tip="absoluteTime(project.last_activity)">{{ relativeTime(project.last_activity, { now, long: true }) }}</time></p>
    </div>
    <div v-else class="head-stats head-stats-skeleton" aria-hidden="true">
      <span class="skeleton stat-placeholder" /><span class="skeleton progress-placeholder" /><span class="skeleton activity-placeholder" />
    </div>
    </div>
  </header>
</template>
