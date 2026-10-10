<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref } from 'vue'
import TooltipHost from '../../src/components/TooltipHost.vue'
import ChildList from '../../src/components/work/ChildList.vue'
import FacetOptions from '../../src/components/work/FacetOptions.vue'
import EpicPicker from '../../src/components/work/EpicPicker.vue'
import LabelMenu from '../../src/components/work/LabelMenu.vue'
import OptionMenu from '../../src/components/work/OptionMenu.vue'
import RelationPicker from '../../src/components/work/RelationPicker.vue'
import TicketProperties from '../../src/components/work/TicketProperties.vue'
import ConnectedComputers from '../../src/components/agents/ConnectedComputers.vue'
import { vClipTip } from '../../src/directives/clipTip'
import { longName } from '../clip-tip-fixtures'
import type { ListItem } from '../../src/lib/api'
import { toggleIn, toggleOut } from '../../src/lib/ticketList'

const anchor = ref<HTMLElement>()
const opened = ref('')
const singleEpic = new URLSearchParams(location.search).has('single-epic')
const selected = ref<string[]>([])
const chosen = ref('')
const standalone = ref(longName)
const item = { id: 'n-1', key: 'PHAROS-11', title: longName, state: 'backlog', kind_slug: 'ticket', kind_label: 'Ticket', fields: {}, priority: null, assignee: null, parent: null, project: null, children_count: 0, created_at: new Date().toISOString(), updated_at: new Date().toISOString() } as ListItem
const permissions = { canLookup: false, canApprove: false, canDeny: false, canDisconnect: false, canListComputers: true, canApproveAccounts: false, canForceStop: false as const }
const options = [{ value: 'long', label: longName, count: 2 }, { value: 'short', label: 'Kurz', count: 1 }]
function toggle(value: string) { selected.value = toggleIn(selected.value, value) }
function exclude(value: string) { selected.value = toggleOut(selected.value, value) }
</script>

<template>
  <main class="harness">
    <h1>Ganze Namen</h1>
    <nav class="controls" aria-label="Test pickers">
      <button v-for="picker in ['epic', 'label', 'option', 'relation']" :key="picker" class="btn" type="button" @click="opened = picker">{{ picker }}</button>
    </nav>
    <button ref="anchor" class="btn anchor" type="button" @click="opened = ''">Close picker</button>
    <!-- Own the Tab stop so lifecycle checks keep focus even after unclipping. -->
    <section><h2>Standalone name</h2><p v-clip-tip class="standalone" tabindex="0">{{ standalone }}</p><button class="btn" @click="standalone = standalone === longName ? 'Kurz' : longName">Change name</button></section>
    <section><h2>Children</h2><ChildList :children="[item, { ...item, id: 'n-short', key: 'PHAROS-12', title: 'Kurz' }]" :loading="false" :editable="false" child-label="ticket" :progress="{ done: 0, total: 2, percent: 0 }" :add="async () => null" @open="chosen = $event" /></section>
    <section><h2>Filter</h2><FacetOptions dimension="tag" :options="options" :selected="selected" @toggle="toggle" @exclude="exclude" /></section>
    <section><h2>Ticket properties</h2><TicketProperties :item="item" :editable="false" layout="column" :now="Date.now()" /></section>
    <section><h2>Computers</h2><ConnectedComputers :permissions="permissions" embedded /></section>
    <p role="status">{{ chosen }}</p>
    <EpicPicker v-if="opened === 'epic'" :anchor="anchor ?? null" project-id="p-pharos" current="n-epic" subject="PHAROS-11" :allow-none="!singleEpic" @choose="chosen = $event?.key ?? ''" @close="opened = ''" />
    <LabelMenu v-if="opened === 'label'" :anchor="anchor ?? null" :labels="[{ name: longName, color: 'blue', on: 0 }, { name: 'Kurz', color: '', on: 0 }]" :count="2" @close="opened = ''" @apply="chosen = 'applied'" />
    <OptionMenu v-if="opened === 'option'" :anchor="anchor ?? null" title="Assignee" subject="PHAROS-11" kind="assignee" :options="options" current="short" @choose="chosen = $event" @close="opened = ''" />
    <RelationPicker v-if="opened === 'relation'" :anchor="anchor ?? null" subject="PHAROS-11" self-id="n-1" project-key="PHAROS" :related="[]" :link="async () => { chosen = 'linked'; return null }" @close="opened = ''" />
    <TooltipHost />
  </main>
</template>

<style scoped>
.harness { width: min(100%, 900px); margin: 0 auto; padding: 20px; }
h1 { margin-bottom: 12px; font-size: 24px; } h2 { margin-bottom: 8px; font-size: 15px; }
.controls { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 10px; }
.anchor { margin-bottom: 10px; }
section { border-top: 1px solid var(--line); padding: 16px 0; min-width: 0; }
.standalone { width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
