<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">

import { can } from '../../lib/authz'
import BulkBar from '../../components/work/BulkBar.vue'
import { toRefs } from 'vue'
import type { Project } from '../../stores/projects'
import { bindProjectRef, useProjectPageContext } from './context'
const { project } = toRefs(defineProps<{ project: Project }>())
const { sequence, total, selectable, selected, clearSelection, selectAllMatching, selectedDeleted, liveSelection, listFrame, bulkBusy, openBulk, bulkArchive, openRelease, bulkQueue, queue, mayQueue, writable, bulkBar } = useProjectPageContext()
const bindBulkBar = bindProjectRef(bulkBar)
</script>

<template>
  <BulkBar :ref="bindBulkBar"
    v-if="selectable && selected.size" :count="selected.size" :deleted="selectedDeleted.length" :loaded="sequence.length" :total="total" :busy="bulkBusy || queue.busy" :can-queue="mayQueue" :can-write="writable" :can-release="can('releases.write', project.id)" :frame="listFrame"
    @status="anchor => openBulk('status', anchor)" @assignee="anchor => openBulk('assignee', anchor)" @priority="anchor => openBulk('priority', anchor)"
    @labels="anchor => openBulk('labels', anchor)" @move="anchor => openBulk('move', anchor)" @release="anchor => openRelease(anchor, liveSelection())" @queue="bulkQueue" @archive="bulkArchive" @clear="clearSelection" @select-all="selectAllMatching"
  />
</template>
