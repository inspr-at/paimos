<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { vClipTip } from '../../lib/clipTip'
import { api } from '../../lib/api'
import { releaseScope, type ReleaseScope } from '../../lib/releaseScope'
import { releaseName, type PlanningRelease } from '../../lib/deliveryPlanning'
import AppIcon from '../AppIcon.vue'
const props = defineProps<{ projectId: string; person: string; values: string[]; countText?: string; pendingChanges?: number; pendingIncomplete?: boolean }>()
const emit = defineEmits<{ applyChanges: []; choose: [values: string[]]; label: [value: string] }>()
const scope = computed(() => releaseScope(props.values))
const identity = computed(() => JSON.stringify([props.projectId, props.person]))
const dialog = ref<HTMLDialogElement>()
const scopeTarget = shallowRef<HTMLElement | null>(null)
let targetObserver: MutationObserver | null = null
onMounted(() => {
  // Header readiness is asynchronous. Never mount into a missing target.
  const locate = () => {
    const target = document.getElementById('release-scope-controls')
    if (target) { scopeTarget.value = target; targetObserver?.disconnect() }
  }
  targetObserver = new MutationObserver(locate)
  targetObserver.observe(document.body, { childList: true, subtree: true })
  locate()
})
const rows = ref<PlanningRelease[]>([]), cursor = ref(''), busy = ref(false), error = ref(''), mode = ref('')
const selectedName = ref(''), missing = ref('')
const label = computed(() => scope.value.kind === 'all' ? 'All work' : scope.value.kind === 'backlog' ? 'Backlog' : scope.value.kind === 'repair' ? 'Choose a release scope' : missing.value || selectedName.value || 'Loading release…')
watch(label, value => emit('label', value), { immediate: true })
let controller: AbortController | null = null, selectedController: AbortController | null = null, generation = 0, opener: HTMLElement | null = null
async function json(path: string, signal: AbortSignal) {
  const response = await api(path, { signal: AbortSignal.any([signal, AbortSignal.timeout(10000)]) })
  if (!response.ok) throw new Error(response.status === 404 || response.status === 403 ? 'This release scope is unavailable' : `Release scopes could not be loaded (${response.status})`)
  return response.json()
}
watch([identity, scope], async () => {
  selectedController?.abort(); selectedController = new AbortController()
  const signal = selectedController.signal, captured = JSON.stringify([identity.value, scope.value])
  selectedName.value = ''; missing.value = ''
  if (scope.value.kind !== 'release') return
  try {
    const row = await json(`/projects/${encodeURIComponent(props.projectId)}/releases/${scope.value.id}`, signal)
    if (signal.aborted || captured !== JSON.stringify([identity.value, scope.value])) return
    if (row.project_id !== props.projectId || row.release_id !== (scope.value as Extract<ReleaseScope, { kind: 'release' }>).id) throw new Error('This release scope is unavailable')
    selectedName.value = releaseName(row)
  } catch (e) { if (!signal.aborted && captured === JSON.stringify([identity.value, scope.value])) missing.value = e instanceof Error ? e.message : 'Scope unavailable' }
}, { immediate: true })
watch(identity, () => { generation++; controller?.abort(); rows.value = []; cursor.value = ''; mode.value = ''; error.value = ''; busy.value = false; dialog.value?.close() })
async function load(more = false) {
  if (busy.value) return
  const token = ++generation, captured = identity.value
  controller?.abort(); controller = new AbortController(); const signal = controller.signal
  busy.value = true; error.value = ''
  try {
    const root = `/projects/${encodeURIComponent(props.projectId)}`
    if (!more) {
      const status = await json(`${root}/delivery`, signal)
      if (signal.aborted || token !== generation || captured !== identity.value) return
      mode.value = status.mode
      if (mode.value !== 'releases') { rows.value = []; cursor.value = ''; return }
    }
    const page = await json(`${root}/releases?limit=50${more && cursor.value ? `&cursor=${encodeURIComponent(cursor.value)}` : ''}`, signal)
    if (signal.aborted || token !== generation || captured !== identity.value) return
    if (!Array.isArray(page.items) || page.items.length > 50 || page.items.some((r: PlanningRelease) => r.project_id !== props.projectId)) throw new Error('Invalid release scopes')
    if (more && page.next_cursor && page.next_cursor === cursor.value) throw new Error('The release cursor did not advance')
    const seen = new Set(more ? rows.value.map(r => r.release_id) : [])
    rows.value = [...(more ? rows.value : []), ...page.items.filter((r: PlanningRelease) => !seen.has(r.release_id))].slice(0, 500)
    cursor.value = page.next_cursor ?? ''
  } catch (e) { if (!signal.aborted && token === generation && captured === identity.value) error.value = e instanceof Error ? e.message : 'Scopes could not be loaded' }
  finally { if (token === generation && captured === identity.value) busy.value = false }
}
async function open() { opener = document.activeElement as HTMLElement; dialog.value?.showModal(); await nextTick(); [...dialog.value?.querySelectorAll<HTMLButtonElement>('.scope-done') ?? []].find(button => button.offsetHeight > 0)?.focus(); void load() }
function close() { dialog.value?.close(); opener?.focus({ preventScroll: true }) }
function choose(values: string[]) { emit('choose', values); close() }
function cancel(event: Event) { event.preventDefault(); const field = document.activeElement; if (field instanceof HTMLElement && ['INPUT', 'TEXTAREA', 'SELECT'].includes(field.tagName)) field.blur(); else close() }
onBeforeUnmount(() => { generation++; targetObserver?.disconnect(); controller?.abort(); selectedController?.abort() })
defineExpose({ open })
</script>
<template>
  <Teleport v-if="scopeTarget" :to="scopeTarget">
    <div class="scope-crumb" role="group" aria-label="Project scope">
      <span aria-hidden="true">/</span>
      <button type="button" class="scope-picker" aria-label="Choose release scope" :aria-description="label" aria-haspopup="dialog" :title="label" @click="open"><span v-clip-tip="label">{{ label }}</span><AppIcon name="chevron" :size="12" /></button>
      <button type="button" class="scope-clear" aria-label="Clear release scope" :disabled="scope.kind === 'all'" @click="choose([])"><AppIcon name="close" :size="13" /></button>
    </div>
  </Teleport>
  <div class="scope-line" role="group" aria-label="Current project scope">
    <button type="button" class="scope-picker" aria-label="Filters: release scope" aria-haspopup="dialog" @click="open"><AppIcon name="filter" :size="14" /><span v-clip-tip="label">{{ label }}</span><AppIcon name="chevron" :size="12" /></button>
    <span class="scope-count" :title="countText" role="status"><button v-if="pendingChanges" type="button" @click="emit('applyChanges')">{{ pendingIncomplete ? '≥ ' : '' }}{{ pendingChanges }} {{ pendingChanges === 1 ? 'change' : 'changes' }} · Apply <kbd class="keycap">a</kbd></button><span v-else v-clip-tip="countText || ''">{{ countText }}</span></span>
    <button type="button" class="scope-clear" aria-label="Clear scope" :disabled="scope.kind === 'all'" @click="choose([])"><AppIcon name="close" :size="14" /></button>
  </div>
  <Teleport to="body">
    <dialog ref="dialog" class="scope-dialog" aria-label="Filters: release scope" @cancel="cancel" @click="($event.target === dialog) && close()">
      <div class="scope-card">
        <header><h2>Release scope</h2><button type="button" class="btn scope-done" @click="close">Done</button></header>
        <div class="scope-actions"><button type="button" class="btn" :disabled="busy || !cursor || rows.length >= 500" @click="load(true)">Load more</button><button type="button" class="btn" :disabled="busy" @click="load()">Refresh</button></div>
        <p class="scope-feedback" role="status">{{ error || (busy ? 'Loading release scopes…' : rows.length >= 500 && cursor ? '500 release scopes loaded. More exist; use a saved release link to reach them.' : mode === 'journey' ? 'This project is still using its journey. All work is available.' : 'Tickets and Knowledge follow this scope. Other filters still apply.') }}</p>
        <div class="scope-options" role="group" aria-label="Release scopes">
          <button type="button" class="scope-option" :aria-pressed="scope.kind === 'all'" @click="choose([])">All work<AppIcon v-if="scope.kind === 'all'" name="check" :size="14" /></button>
          <button type="button" class="scope-option" :disabled="mode !== 'releases'" :aria-pressed="scope.kind === 'backlog'" @click="choose(['none'])">Backlog<AppIcon v-if="scope.kind === 'backlog'" name="check" :size="14" /></button>
          <button v-for="row in rows" :key="row.release_id" type="button" class="scope-option" :aria-pressed="scope.kind === 'release' && scope.id === row.release_id" :title="releaseName(row)" @click="choose([row.release_id])"><span v-clip-tip="releaseName(row)">{{ releaseName(row) }}</span><AppIcon v-if="scope.kind === 'release' && scope.id === row.release_id" name="check" :size="14" /></button>
        </div>
        <footer class="scope-footer"><button type="button" class="btn scope-done" @click="close">Done</button></footer>
      </div>
    </dialog>
  </Teleport>
</template>
<style scoped>
.scope-crumb, .scope-line { display: flex; align-items: center; gap: 6px; min-width: 0; }
.scope-crumb { width: clamp(12rem, 19vw, 19rem); height: 30px; }
.scope-picker { display: flex; align-items: center; gap: 6px; min-width: 0; flex: 1; height: 30px; padding: 0 4px; border: 0; background: transparent; color: var(--ink); font-weight: 600; }
.scope-picker > span { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; text-align: left; }
.scope-clear { display: grid; place-items: center; flex: 0 0 30px; height: 30px; border: 0; background: transparent; color: var(--ink-2); }
.scope-clear:disabled { opacity: .35; }
.scope-picker:focus-visible, .scope-clear:focus-visible { box-shadow: var(--focus-ring); }
.scope-count { width: 42%; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-size: 12px; color: var(--ink-2); text-align: right; }
.scope-count button { border: 0; background: transparent; color: var(--teal-ink); font: inherit; min-height: 44px; }
.scope-line { display: none; height: 44px; border-bottom: 1px solid var(--line); }
.scope-dialog { position: fixed; inset: max(72px, 8vh) auto auto 50%; transform: translateX(-50%); margin: 0; width: min(92vw, 36rem); max-height: 80dvh; padding: 0; border: 1px solid var(--line); background: var(--surface); color: var(--ink); box-shadow: var(--shadow-lg); }
.scope-dialog::backdrop { background: rgb(0 0 0 / .25); }
.scope-card { display: flex; flex-direction: column; max-height: 80dvh; padding: 18px; gap: 12px; }
.scope-card header { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.scope-card h2 { font-size: 18px; }
.scope-footer { display: none; }
.scope-actions { display: flex; gap: 8px; }
.scope-feedback { min-height: 3lh; margin: 0; color: var(--ink-2); font-size: 13px; }
.scope-options { overflow-y: auto; scrollbar-gutter: stable; min-height: 0; }
.scope-option { display: flex; justify-content: space-between; align-items: center; width: 100%; height: 44px; gap: 12px; padding: 0 8px; border: 0; border-bottom: 1px solid var(--line); background: transparent; color: var(--ink); text-align: left; }
.scope-option > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.scope-option[aria-pressed="true"] { background: var(--row-selected); font-weight: 600; }
@media(max-width:1024px) { .scope-line { display: flex; } }
@media(max-width:720px) { .scope-dialog { inset: 0; transform: none; width: 100%; max-width: none; height: 100dvh; max-height: 100dvh; } .scope-card { height: 100%; max-height: none; padding-bottom: max(18px, env(safe-area-inset-bottom)); } .scope-options { flex: 1; } .scope-card header .scope-done { display: none; } .scope-footer { display: flex; flex: 0 0 auto; justify-content: flex-end; padding-top: 8px; border-top: 1px solid var(--line); } .scope-footer .btn { min-height: 44px; } .scope-picker, .scope-clear { min-height: 44px; } .scope-clear { flex-basis: 44px; } }
</style>
