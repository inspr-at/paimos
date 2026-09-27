<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { getJourney, listReleases, releaseName, releaseRefs } from '../../lib/journey'
import { canOpenRelease, nextReleaseTitle, planningRelease } from '../../lib/releaseMembership'
import type { ReleaseTarget } from '../../lib/releaseAssign'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'

// An open planning release, or a new one titled Release N+1. Choosing does not
// write; the caller adds the tickets and asks before a move.
const props = defineProps<{ anchor: HTMLElement | null; projectId: string; subject: string }>()
const emit = defineEmits<{ choose: [target: ReleaseTarget]; close: [restoreFocus: boolean] }>()

interface Row { id: string; title: string; detail: string; disabled: boolean; target: ReleaseTarget | null }
const rows = ref<Row[]>([])
const loading = ref(true)
const failed = ref('')
const active = ref(0)
let generation = 0

async function load() {
  const request = ++generation
  loading.value = true
  failed.value = ''
  try {
    const [journey, nodes] = await Promise.all([getJourney(props.projectId), listReleases(props.projectId)])
    if (request !== generation) return
    const releases = releaseRefs(nodes)
    const planning = planningRelease(journey, releases)
    const fresh = nextReleaseTitle(releases)
    const open = canOpenRelease(journey, planning)
    const next: Row[] = []
    if (planning) next.push({ id: planning.id, title: planning.version ?? releaseName(planning), detail: 'In planning', disabled: false, target: { kind: 'existing', id: planning.id, title: planning.version ?? releaseName(planning) } })
    next.push({ id: 'new', title: fresh, detail: open.ok ? 'New release' : open.reason, disabled: !open.ok, target: open.ok ? { kind: 'new', title: fresh } : null })
    rows.value = next
    active.value = Math.max(0, next.findIndex(row => !row.disabled))
  } catch (error) {
    if (request !== generation) return
    failed.value = error instanceof Error ? error.message : 'Releases could not be loaded.'
  } finally {
    if (request === generation) loading.value = false
  }
}
onMounted(load)
onBeforeUnmount(() => { generation++ })

function choose(row: Row) {
  if (row.disabled || !row.target) return
  emit('choose', row.target)
}
function keydown(event: KeyboardEvent) {
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    if (!rows.value.length) return
    const step = event.key === 'ArrowDown' ? 1 : -1
    for (let i = 1; i <= rows.value.length; i++) {
      const index = (active.value + step * i + rows.value.length) % rows.value.length
      if (!rows.value[index].disabled) { active.value = index; return }
    }
  } else if (event.key === 'Enter') {
    event.preventDefault()
    const row = rows.value[active.value]
    if (row) choose(row)
  }
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="360" :label="`Release for ${subject}`" cycle @close="restore => emit('close', restore)">
    <p class="menu-title eyebrow">Add to release</p>
    <div class="options" role="listbox" aria-label="Releases" tabindex="0" data-autofocus @keydown="keydown">
      <button
        v-for="(row, index) in rows" :id="`release-option-${index}`" :key="row.id" type="button" role="option" class="option"
        :aria-selected="active === index" :aria-disabled="row.disabled" :disabled="row.disabled" :data-tip="row.disabled ? row.detail : undefined"
        @click="choose(row)" @pointermove="active = index"
      >
        <AppIcon :name="row.id === 'new' ? 'plus' : 'layers'" :size="14" />
        <span class="text"><span class="title">{{ row.title }}</span><span class="detail">{{ row.detail }}</span></span>
      </button>
      <p v-if="loading && !rows.length" class="note" role="status">Looking for releases…</p>
      <p v-else-if="failed" class="note error" role="alert">{{ failed }}</p>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.menu-title { padding: 6px 10px 4px; }
.options { display: grid; gap: 1px; }
.option { display: flex; align-items: flex-start; gap: 9px; min-height: 44px; padding: 8px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); text-align: left; }
.option svg { margin-top: 2px; color: var(--ink-2); flex-shrink: 0; }
.option[aria-selected="true"] { background: var(--row-selected); }
.option:disabled { color: var(--ink-3); cursor: default; }
.text { display: grid; gap: 2px; min-width: 0; }
.title { font-size: 13.5px; font-weight: 600; }
.detail { font-size: 12px; color: var(--ink-3); line-height: 1.35; }
.option:disabled .detail { color: var(--ink-3); }
.note { padding: 8px 10px; font-size: 13px; color: var(--ink-3); }
.note.error { color: var(--danger); }
</style>
