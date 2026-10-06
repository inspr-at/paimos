<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { recurrenceReleases, runRecurrence } from '../../lib/api'
import { can } from '../../lib/authz'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { recurrenceName, reasonWords, when, type Recurrence, type RecurrenceRelease, type RecurrenceResult } from '../../lib/recurrences'
import KeyCap from '../KeyCap.vue'
const props = defineProps<{ item: Recurrence }>()
const emit = defineEmits<{ close: []; ran: [result: RecurrenceResult] }>()
const dialog = ref<HTMLDialogElement>(), releases = ref<RecurrenceRelease[]>([]), choice = ref(''), loading = ref(props.item.trigger.kind === 'event'), busy = ref(false), failure = ref(''), truncated = ref(false)
const scope = useIdentityScope(() => can('recurrences.manage', props.item.project_id))
const target = props.item, key = crypto.randomUUID()
function run() {
  if (busy.value || loading.value || (target.trigger.kind === 'event' && !choice.value) || !can('recurrences.manage', target.project_id)) return
  busy.value = true; failure.value = ''
  const selected = choice.value
  void scope.run(({ after, signal }) => after(runRecurrence(target.id, { idempotency_key: key, expected_revision: target.revision, force_overlap: !!target.open_previous && target.overlap_policy === 'skip', ...(selected ? { release_key: selected } : {}) }, signal), result => { emit('ran', result) }), { failed: error => { failure.value = error instanceof Error ? error.message : 'The run failed.' }, settled: () => { busy.value = false } })
}
function keys(event: KeyboardEvent) {
  const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
  if (event.key === 'Enter' && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) && !event.shiftKey && !event.altKey) { event.preventDefault(); run() }
  if (event.key === 'Escape') { event.preventDefault(); if (!busy.value) emit('close') }
  if (['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(event.key) && !event.metaKey && !event.ctrlKey && !event.altKey) {
    const group = (event.target as HTMLElement).closest('[role="radiogroup"]')
    if (!group) return
    const buttons = [...group.querySelectorAll<HTMLButtonElement>('button:not(:disabled)')], at = buttons.indexOf(event.target as HTMLButtonElement)
    if (!buttons.length) return
    event.preventDefault(); const next = buttons[(at + (['ArrowUp', 'ArrowLeft'].includes(event.key) ? -1 : 1) + buttons.length) % buttons.length]; next?.click(); next?.focus()
  }
}
onMounted(() => {
  dialog.value?.showModal()
  if (target.trigger.kind === 'event') void scope.run(({ after, signal }) => after(recurrenceReleases(target.id, signal), page => { releases.value = page.items; truncated.value = page.truncated }), { failed: error => { failure.value = error instanceof Error ? error.message : 'Releases unavailable.' }, settled: () => { loading.value = false } })
})
onBeforeUnmount(() => dialog.value?.close())
</script>
<template>
  <Teleport to="body"><dialog ref="dialog" class="run-dialog" aria-label="Run recurring work now" @cancel.prevent="!busy && emit('close')" @keydown.stop="keys"><header><h2>Run now</h2><div class="actions"><button type="button" class="btn sm ghost" :disabled="busy" @click="emit('close')">Cancel</button><button type="button" class="btn sm primary" :disabled="busy || loading || (item.trigger.kind === 'event' && !choice)" @click="run">Create #{{ item.occurrence_count + 1 }}<span class="keys"><KeyCap k="mod" /><KeyCap k="enter" /></span></button></div></header><div class="run-body"><p>{{ recurrenceName(item) }}</p><p v-if="item.open_previous && item.overlap_policy === 'skip'" class="overlap">#{{ item.open_previous.number }} ({{ item.open_previous.key || 'previous ticket' }}) is still open. Create another anyway?</p><template v-if="item.trigger.kind === 'event'"><p>Run as if this release was just published:</p><p v-if="loading" role="status">Loading releases…</p><div class="release-choices" role="radiogroup" aria-label="Published release"><button v-for="release in releases" :key="release.key" type="button" role="radio" :aria-checked="choice === release.key" :disabled="!!release.receipt || busy" :title="release.version" :data-tip="release.version" @click="choice = release.key"><b>{{ release.name || 'Unnamed release' }}</b><small>{{ release.receipt ? release.receipt.key ? `already has ${release.receipt.key}` : `already attempted · ${reasonWords(release.receipt.reason)}` : when(release.published_at) }}</small></button></div><p v-if="!loading && !releases.length && !failure">No published releases yet.</p><p v-if="truncated">Showing the newest 100 releases.</p></template><p v-if="failure" class="error" role="alert">{{ failure }}</p></div></dialog></Teleport>
</template>
<style scoped>
.run-dialog { position: fixed; inset: auto; top: 64px; left: 50%; transform: translateX(-50%); margin: 0; padding: 0; width: min(var(--dialog-m), calc(100vw - 32px)); max-height: calc(100dvh - 80px); color: var(--ink); background: var(--surface-raised); border: 1px solid var(--glass-edge); border-radius: 16px; box-shadow: var(--shadow-pop); overflow: hidden; }.run-dialog[open] { display: flex; flex-direction: column; }.run-dialog::backdrop { background: var(--scrim); }header { display: flex; align-items: center; gap: 12px; height: 60px; padding: 10px 14px 10px 20px; border-bottom: 1px solid var(--line); flex: none; }h2 { flex: 1; font: 600 16px var(--font); }.actions { display: flex; gap: 8px; }.actions .primary { min-width: 142px; }.keys { display: flex; gap: 2px; }.run-body { padding: 16px 20px; overflow: auto; display: grid; gap: 12px; font-size: 13px; }.overlap { color: var(--warn-ink); }.release-choices { display: grid; gap: 1px; }.release-choices button { min-height: 50px; padding: 6px 10px; border: 0; border-radius: 8px; background: transparent; text-align: left; }.release-choices button[aria-checked="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }.release-choices button:hover:not(:disabled) { background: var(--row-hover); }.release-choices b, .release-choices small { display: block; }.release-choices small { color: var(--ink-3); font-size: 12px; }
@media (max-width: 600px) { .run-dialog { top: 8px; left: 0; transform: none; width: 100%; max-width: none; max-height: calc(100dvh - 8px); }header { padding: 8px 12px; }.keys { display: none; }.actions .primary { min-width: 110px; } }
</style>
