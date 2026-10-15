<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import type { EntryPanelRequest } from '../../lib/entryChecks'
import { rowStore } from '../../lib/rowStore'
import { useSession } from '../../stores/session'
import QueueReadyPanel from './QueueReadyPanel.vue'
const session = useSession(), route = useRoute()
const open = ref<EntryPanelRequest & { anchor: HTMLElement } | null>(null)
function requested(event: Event) {
  const detail = (event as CustomEvent<EntryPanelRequest>).detail
  if (!detail?.row || !detail.row.project) return
  const row = rowStore.latest(detail.row.id) ?? detail.row
  if (row.updated_at !== detail.row.updated_at) return
  const anchor = detail.anchor ?? (document.activeElement instanceof HTMLElement ? document.activeElement : document.body)
  if (!anchor.isConnected) return
  open.value = { ...detail, row, anchor }
}
watch(() => [session.identity?.principal.id, session.identity?.tenant.id, route.path], () => { open.value = null })
onMounted(() => window.addEventListener('aeon:entry-checks', requested))
onBeforeUnmount(() => window.removeEventListener('aeon:entry-checks', requested))
</script>
<template>
  <QueueReadyPanel v-if="open && open.row.project" :key="`${open.row.id}:${open.toStatus}`" :row="open.row" :project-id="open.row.project.id" :anchor="open.anchor" mode="status" :to-status="open.toStatus" :refusal="open.refusal" @close="restore => { const anchor = open?.anchor; open = null; if (restore) anchor?.focus() }" />
</template>
