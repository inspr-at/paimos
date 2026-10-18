<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">

import { defineAsyncComponent } from 'vue'
import type { TicketGraphState } from '../../lib/ticketGraphRenderer'
import { toRefs } from 'vue'
import type { Project } from '../../stores/projects'
import { bindProjectRef, useProjectPageContext } from './context'
const props = defineProps<{ project: Project }>()
const { project } = toRefs(props)
const { knowledge, knowledgeTab, knowledgeWide, shownEntry, learningAccepted, learningReverted, updateKnowledge, openKey, session, ticketKey, filters, now, activeTicketView, knowledgeActive, knowledgeEntryOpen, knowledgeFilters, settingsActive, deliveryActive, graphState, ticketGraphView, me, knowledgeWritable } = useProjectPageContext()
const ProjectSettings = defineAsyncComponent(() => import('../../components/settings/ProjectSettings.vue'))
// Knowledge loads with its tab, not with every project page.
const KnowledgeTab = defineAsyncComponent(() => import('../../components/knowledge/KnowledgeTab.vue'))

// Delivery (AEON-994) loads with its tab.
const DeliveryView = defineAsyncComponent(() => import('../../components/delivery/DeliveryView.vue'))
import ProjectTicketList from './ProjectTicketList.vue'
const bindKnowledgeTab = bindProjectRef(knowledgeTab)
const bindTicketGraphView = bindProjectRef(ticketGraphView)
</script>

<template>
  <template v-if="settingsActive">
    <ProjectSettings :key="project.id" :project="project" />
  </template>
  <DeliveryView v-else-if="deliveryActive" :key="`${project.id}/${me?.id ?? ''}`" :project="{ id: project.id, routeKey: project.routeKey, title: project.title }" />
  <KnowledgeTab
    v-else-if="knowledgeActive" :ref="bindKnowledgeTab" :project="{ id: project.id, routeKey: project.routeKey, title: project.title }" :state="knowledge"
    :filters="knowledgeFilters" :can-write="knowledgeWritable" :person="session.identity?.principal.kind === 'person'" :now="now" :paused="knowledgeEntryOpen || !!ticketKey" :dock="knowledgeWide" :open-entry="shownEntry?.mode === 'dock' ? shownEntry : null" @update="updateKnowledge" @accepted="learningAccepted" @reverted="learningReverted"
  />
  <component :is="activeTicketView.component" v-else-if="activeTicketView.component"
    :project="project" :filters="filters" :ref="bindTicketGraphView" @open="openKey" @state="(value: TicketGraphState) => graphState = value" />
  <ProjectTicketList v-else :project="project" />
</template>
