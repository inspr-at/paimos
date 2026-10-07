<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { canPause, LEAD_WORDS } from '../../lib/lead'
import { closeLeadSheet } from '../../lib/leadOverlay'
import { toast } from '../../lib/toast'
import { useProjectLeads } from '../../stores/projectLeads'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'

// The PauseDialog pattern for a lead. The lead lifecycle offers one level: a
// checkpoint with handover (limit 10 min). New dispatch stops immediately; the
// process keeps its dial slot until it confirms it stopped.
const props = defineProps<{ projectId: string }>()
const w = LEAD_WORDS
const leads = useProjectLeads(), projects = useProjects(), session = useSession()
const dialog = ref<HTMLDialogElement>(), pauseButton = ref<HTMLButtonElement>()
const busy = ref(false), error = ref('')
const key = computed(() => projects.byId(props.projectId)?.routeKey ?? 'this project')
const lead = computed(() => leads.views[props.projectId]?.lead ?? null)
// The revision on screen when the sheet opened; a changed lead must be looked at again.
const opened = lead.value ? { revision: lead.value.revision, generation: lead.value.generation } : null
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
let generation = 0

async function pause() {
  if (busy.value) return
  const turn = generation, who = session.identity?.principal.id, current = lead.value
  if (!current || !canPause(current)) { error.value = `The ${w.l} can’t be paused in its current state.`; return }
  if (!opened || current.revision !== opened.revision || current.generation !== opened.generation) { error.value = `The ${w.l} changed meanwhile. Check its state, then pause again.`; return }
  busy.value = true; error.value = ''
  try {
    const next = await leads.pause(props.projectId)
    if (turn !== generation || who !== session.identity?.principal.id || !next) return
    toast(`${key.value} ${w.l} pausing`, { timeout: 5200 })
    close(true)
  } catch (e) { if (turn === generation) error.value = e instanceof Error ? e.message : `The ${w.l} was not paused.` }
  finally { if (turn === generation) busy.value = false }
}
function close(restore = true) { generation++; dialog.value?.close(); closeLeadSheet(restore) }
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close() }
  else if (event.key === 'Enter' && (mac ? event.metaKey : event.ctrlKey) && !event.altKey && !event.shiftKey) { event.preventDefault(); event.stopPropagation(); void pause() }
}
watch(() => session.identity?.principal.id, () => close(false))
onMounted(async () => { dialog.value?.showModal(); await nextTick(); pauseButton.value?.focus() })
onBeforeUnmount(() => { generation++ })
</script>

<template>
  <dialog ref="dialog" class="lead-sheet" aria-labelledby="pause-lead-title" @cancel.prevent="close()" @keydown="keydown">
    <header class="sheet-head"><h2 id="pause-lead-title">Pause the {{ key }} {{ w.l }}</h2><button type="button" class="icon-btn" aria-label="Close" @click="close()"><AppIcon name="close" /></button></header>
    <div class="sheet-acts">
      <button ref="pauseButton" type="button" class="btn primary" data-act="pause" :aria-disabled="busy" @click="pause"><AppIcon name="pause" :size="15" />Pause<KeyCap k="mod" class="hint" /><KeyCap k="enter" class="hint" /></button>
      <button type="button" class="btn ghost" data-act="cancel" @click="close()">Cancel<KeyCap k="Esc" class="hint" /></button>
    </div>
    <div class="sheet-body">
      <p v-if="error" class="sheet-error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
      <p class="level"><b>Pause</b><span>limit 10 min · handover</span></p>
      <p class="level-slot">Finishes or rolls back the current step, commits work in progress and hands over. Nothing new starts from now on.</p>
      <p class="small">Its workers finish the tickets they are on. Queued work stays queued. Resume restarts the {{ w.l }} through the usual start checks once it has stopped.</p>
    </div>
  </dialog>
</template>

<style scoped>
.lead-sheet { position: fixed; inset: 12vh auto auto 50%; transform: translateX(-50%); margin: 0; width: min(560px, calc(100vw - 32px)); max-height: calc(100dvh - 12vh - 24px); padding: 0; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: var(--surface-raised); color: var(--ink); box-shadow: var(--shadow-pop); overflow: hidden; }
.lead-sheet[open] { display: flex; flex-direction: column; }
.lead-sheet::backdrop { background: var(--scrim); }
.sheet-head { display: flex; align-items: center; gap: 12px; padding: 16px 12px 0 20px; }
.sheet-head h2 { flex: 1; min-width: 0; margin: 0; font: 400 21px/1.25 var(--serif); letter-spacing: -.015em; }
.sheet-acts { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 14px 20px 16px; border-bottom: 1px solid var(--line); flex: none; }
.sheet-acts .btn[aria-disabled="true"] { opacity: .55; cursor: default; }
.sheet-body { overflow: auto; padding: 16px 20px 22px; }
.sheet-error { display: flex; align-items: center; gap: 8px; margin: 0 0 12px; color: var(--danger); font-size: 13px; }
.level { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 10px; min-height: 46px; margin: 0; padding: 0 10px; border-radius: var(--radius-row, 8px); background: var(--row-selected); }
.level b { color: var(--ink); font-size: 13.5px; font-weight: 600; }
.level span { color: var(--ink-3); font-size: 12.5px; }
.level-slot { margin: 0; padding: 10px 10px 0; font-size: 13px; color: var(--ink-2); }
.small { margin: 12px 0 0; padding: 0 10px; font-size: 12.5px; color: var(--ink-3); }
@media (max-width: 720px) {
  .lead-sheet { inset: 0; transform: none; width: 100%; max-width: none; height: 100dvh; max-height: none; border-radius: 0; border: 0; }
  .sheet-head { padding: max(12px, env(safe-area-inset-top)) 8px 10px 16px; border-bottom: 1px solid var(--line); }
  .sheet-body { flex: 1; padding: 16px; }
  .sheet-acts { order: 3; border-bottom: 0; border-top: 1px solid var(--line); padding: 10px 16px calc(10px + env(safe-area-inset-bottom)); flex-direction: row-reverse; }
  .sheet-acts .btn { flex: 1; min-height: 44px; }
}
@media (pointer: coarse) { .hint { display: none; } }
</style>
