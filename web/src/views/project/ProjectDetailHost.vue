<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">

import { defineAsyncComponent } from 'vue'
import PanelSplitter from '../../components/PanelSplitter.vue'
import TicketWorkspace from '../../components/work/TicketWorkspace.vue'
import { DOCK_LIST_RESERVE } from '../../lib/knowledge'
import { toRefs } from 'vue'
import type { Project } from '../../stores/projects'
import { bindProjectRef, useProjectPageContext } from './context'
const props = defineProps<{ project: Project }>()
const { project } = toRefs(props)
const { openStatus, childCreated, childMoved, removed, list, move, knowledgeListQuery, knowledge, knowledgeEntry, knowledgeDocked, shownEntry, closeKnowledgeDock, closeKnowledgeEntry, panelError, panelLoading, panelItem, panelPosition, resolvePanel, nativeReleases, refreshMemberships, loadProjectPeople, people, closePanel, expand, collapse, trail, trailBack, openRelated, newTab, ticketKey, now, panel, fullView, me, workspaceOwner, workspacePerson, writable, nodeDeletable, nodeMovable, relationLinkable, relationUnlinkable, commentable, commentDeletable, attachable, knowledgeWritable, knowledgeDeletable } = useProjectPageContext()

const KnowledgeEntryPage = defineAsyncComponent(() => import('../../components/knowledge/KnowledgeEntryPage.vue'))
const bindKnowledgeEntry = bindProjectRef(knowledgeEntry)
const bindPanel = bindProjectRef(panel)
</script>

<template>
  <KnowledgeEntryPage
    v-if="shownEntry" :key="`${project.id}/${me?.id ?? ''}`" :ref="bindKnowledgeEntry" :project="{ id: project.id, routeKey: project.routeKey, title: project.title }" :mode="shownEntry.mode"
    :type="shownEntry.type" :slug="shownEntry.slug" :state="knowledge" :can-write="knowledgeWritable" :can-delete="knowledgeDeletable" :now="now" :list-query="knowledgeListQuery"
    @close="shownEntry.mode === 'dock' ? closeKnowledgeDock() : closeKnowledgeEntry()"
  />
  <PanelSplitter v-if="knowledgeDocked" field="knowledgePanel" css-var="--knowledge-panel-user-w" target=".entry-page.dock" :reserve="DOCK_LIST_RESERVE" />
  <PanelSplitter v-if="ticketKey && !fullView" class="early" :reserve="500" />
  <TicketWorkspace
    v-if="ticketKey" :key="`${workspaceOwner}/${project.id}/${ticketKey.toUpperCase()}`" :ref="bindPanel" :item="panelItem" :ticket-key="ticketKey.toUpperCase()" :resolving="panelLoading" :resolve-error="panelError"
    :position="panelPosition" :now="now" :mode="fullView ? 'full' : 'panel'" :project="{ id: project.id, routeKey: project.routeKey }"
    :names="list.names" :me="workspacePerson" :can-write="writable" :can-delete="nodeDeletable" :can-move="nodeMovable" :can-link="relationLinkable" :can-unlink="relationUnlinkable"
    :can-comment="commentable" :can-delete-comment="commentDeletable" :can-attach="attachable" :people="people" :ensure-people="loadProjectPeople" :native-releases="nativeReleases"
    @close="closePanel" @prev="move(-1)" @next="move(1)" @expand="expand" @collapse="collapse" @new-tab="newTab(panelItem?.key ?? ticketKey)"
    @status="anchor => panelItem && openStatus(panelItem, anchor, 'panel')" @open-key="openRelated" :trail="trail" @trail-back="trailBack" @removed="removed" @created="childCreated" @moved="childMoved" @assigned="() => { void list.load(); void refreshMemberships() }" @retry="resolvePanel"
  />
</template>
