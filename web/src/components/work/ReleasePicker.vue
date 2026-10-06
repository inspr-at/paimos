<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { vClipTip } from '../../directives/clipTip'
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
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
const dialog = ref<HTMLDialogElement>()
const phoneQuery = window.matchMedia('(max-width: 720px)')
const phone = ref(phoneQuery.matches)
let generation = 0
// Decide the width when opened or resized, independently of selection/text.
const panelWidth = ref(0)
function resize() { panelWidth.value = Math.min(window.innerWidth - 32, Math.max(props.anchor?.getBoundingClientRect().width ?? 0, window.innerWidth * 0.4)) }
resize()

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
    if (planning) { const title = planning.title.trim() || releaseName(planning); next.push({ id: planning.id, title, detail: 'In planning', disabled: false, target: { kind: 'existing', id: planning.id, title } }) }
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
function backdrop(event: MouseEvent) { if (event.target === dialog.value) emit('close', false) }
onMounted(async () => {
  window.addEventListener('resize', resize)
  void load()
  if (!phone.value) return
  await nextTick()
  dialog.value?.showModal()
  dialog.value?.querySelector<HTMLElement>('[data-autofocus]')?.focus({ preventScroll: true })
})
onBeforeUnmount(() => { generation++; window.removeEventListener('resize', resize) })

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
  <dialog v-if="phone" ref="dialog" class="release-sheet" :aria-label="`Release for ${subject}`" @cancel.prevent="emit('close', true)" @click="backdrop">
    <div class="sheet-card">
      <span class="grabber" aria-hidden="true" />
      <p class="menu-title eyebrow">Add to release</p>
      <div class="options" role="listbox" aria-label="Releases" tabindex="0" data-autofocus @keydown="keydown">
        <button
          v-for="(row, index) in rows" :id="`release-option-${index}`" :key="row.id" type="button" role="option" class="option"
          :aria-selected="active === index" :aria-disabled="row.disabled" :disabled="row.disabled" :data-tip="row.disabled ? row.detail : undefined"
          @click="choose(row)" @pointermove="active = index"
        >
          <AppIcon :name="row.id === 'new' ? 'plus' : 'layers'" :size="14" />
          <span class="text"><span v-clip-tip class="title">{{ row.title }}</span><span v-clip-tip class="detail">{{ row.id === 'new' && row.disabled ? 'Finish the current release first' : row.detail }}</span></span>
        </button>
        <p v-if="rows.find(row => row.id === 'new' && row.disabled)" class="note">{{ rows.find(row => row.id === 'new' && row.disabled)?.detail }}</p>
        <p v-if="loading && !rows.length" class="note" role="status">Looking for releases…</p>
        <p v-else-if="failed" class="note error" role="alert">{{ failed }}</p>
      </div>
      <footer class="sheet-actions"><button type="button" @click="emit('close', true)">Close <kbd>Esc</kbd></button></footer>
    </div>
  </dialog>
  <FloatingPanel v-else :anchor="anchor" :width="panelWidth" :label="`Release for ${subject}`" cycle @close="restore => emit('close', restore)">
    <p class="menu-title eyebrow">Add to release</p>
    <div class="options" role="listbox" aria-label="Releases" tabindex="0" data-autofocus @keydown="keydown">
      <button
        v-for="(row, index) in rows" :id="`release-option-${index}`" :key="row.id" type="button" role="option" class="option"
        :aria-selected="active === index" :aria-disabled="row.disabled" :disabled="row.disabled" :data-tip="row.disabled ? row.detail : undefined"
        @click="choose(row)" @pointermove="active = index"
      >
        <AppIcon :name="row.id === 'new' ? 'plus' : 'layers'" :size="14" />
        <span class="text"><span v-clip-tip class="title">{{ row.title }}</span><span v-clip-tip class="detail">{{ row.id === 'new' && row.disabled ? 'Finish the current release first' : row.detail }}</span></span>
      </button>
      <p v-if="rows.find(row => row.id === 'new' && row.disabled)" class="note">{{ rows.find(row => row.id === 'new' && row.disabled)?.detail }}</p>
      <p v-if="loading && !rows.length" class="note" role="status">Looking for releases…</p>
      <p v-else-if="failed" class="note error" role="alert">{{ failed }}</p>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.menu-title { padding: 6px 10px 4px; }
.options { display: grid; gap: 1px; }
.option { display: flex; align-items: center; gap: 9px; height: 76px; padding: 8px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); text-align: left; }
.option svg { margin-top: 2px; color: var(--ink-2); flex-shrink: 0; }
.option[aria-selected="true"] { background: var(--row-selected); }
.option:disabled { color: var(--ink-3); cursor: default; }
.text { display: grid; gap: 2px; min-width: 0; }
.title { font-size: 13.5px; font-weight: 600; line-height: 1.3; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; overflow-wrap: anywhere; }
.detail { font-size: 12px; color: var(--ink-3); line-height: 1.35; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.option:disabled .detail { color: var(--ink-3); }
.note { padding: 8px 10px; font-size: 13px; color: var(--ink-3); }
.note.error { color: var(--danger); }
.release-sheet {
  position: fixed; inset: 0; width: 100%; max-width: none; height: 100dvh; max-height: none;
  margin: 0; padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible;
}
.release-sheet::backdrop { background: var(--scrim); }
.sheet-card {
  display: flex; flex-direction: column; height: 100%; padding: env(safe-area-inset-top) 8px 0;
  background: var(--surface-raised);
  box-shadow: 0 -18px 40px -18px color-mix(in srgb, var(--shadow-black) 35%, transparent);
}
.grabber { align-self: center; width: 40px; height: 4px; margin-top: 8px; border-radius: 999px; background: var(--line-2); }
.sheet-card .menu-title { padding: 10px 12px 6px; }
.sheet-card .option { padding: 10px 12px; }
.sheet-card .options { flex: 1; min-height: 0; align-content: start; overflow: auto; overscroll-behavior: contain; }
.sheet-actions { flex: none; display: flex; justify-content: flex-end; padding: 10px 12px calc(10px + env(safe-area-inset-bottom)); border-top: 1px solid var(--line); }
.sheet-actions button { min-height: 44px; padding: 8px 14px; border: 0; background: transparent; color: var(--ink); }
.sheet-actions kbd { margin-left: 8px; font-size: 11px; color: var(--ink-3); }
@media (prefers-reduced-motion: no-preference) {
  .release-sheet[open] .sheet-card { animation: sheet-up .24s cubic-bezier(.2, .7, .2, 1); }
  @keyframes sheet-up { from { transform: translateY(40px); opacity: .6; } to { transform: none; opacity: 1; } }
}
</style>
