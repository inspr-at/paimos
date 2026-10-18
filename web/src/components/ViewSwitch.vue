<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import ProjectTabs from './work/ProjectTabs.vue'
import ViewDefaultMenu from './ViewDefaultMenu.vue'
import type { ViewOption } from './work/projectNavigation'

// One view switch for every place with views (AEON-1453). It wraps the existing
// segmented look (ProjectTabs: tabs, sections or the round .seg) and adds only the
// person's default: right-click, a long press or Shift+F10 on a segment opens a small
// menu with "Open here by default" or "Stop opening here by default"; the default
// carries the small pin. Remembering is useViewSwitch's (lib/viewSwitch.ts).
const props = withDefaults(defineProps<{
  options: readonly ViewOption[]
  selected: string
  label: string
  look?: 'tabs' | 'sections' | 'seg'
  tips?: boolean
  iconSize?: number
  defaultId?: string | null
  // Without it, the switch is the plain segmented look (no menu, no pin).
  menu?: boolean
}>(), { look: 'tabs', tips: false, iconSize: undefined, defaultId: null, menu: true })
const emit = defineEmits<{ select: [id: string]; setDefault: [id: string | null] }>()
const open = ref<{ id: string; anchor: HTMLElement } | null>(null)
const option = computed(() => open.value ? props.options.find(item => item.id === open.value!.id) ?? null : null)
function show(id: string, anchor: HTMLElement) { open.value = { id, anchor } }
function close(restore: boolean) {
  const anchor = open.value?.anchor
  open.value = null
  if (restore && anchor?.isConnected) anchor.focus({ preventScroll: true })
}
function set(makeDefault: boolean) {
  const at = open.value
  if (!at) return
  emit('setDefault', makeDefault ? at.id : null)
  close(true)
}
</script>

<template>
  <ProjectTabs :items="options" :selected="selected" :label="label" :sections="look === 'sections'" :seg="look === 'seg'" :tips="tips" :icon-size="iconSize"
    :default-id="menu ? defaultId : null" :menu="menu" @select="id => emit('select', id)" @menu="show">
    <ViewDefaultMenu v-if="open && option" :anchor="open.anchor" :label="option.label" :is-default="option.id === defaultId" @set="set" @close="close" />
  </ProjectTabs>
</template>
