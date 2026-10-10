<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { workLifecycleRequest, type WorkAction, type WorkPreview } from '../../lib/workLifecycle'
import KeyCap from '../KeyCap.vue'

const props = defineProps<{ nodeId: string; nodeKey: string; personId: string }>()
const emit = defineEmits<{ close: []; completed: [] }>()
const dialog = ref<HTMLDialogElement>()
const closeButton = ref<HTMLButtonElement>()
const preview = ref<WorkPreview | null>(null)
const action = ref<WorkAction | null>(null)
const choice = ref<'split' | 'cancel'>('split')
const titles = ref('')
const busy = ref(false)
const error = ref('')
const mac = /Mac|iPhone|iPad/.test(navigator.platform)
const childTitles = computed(() => titles.value.split('\n').map(value => value.trim()).filter(Boolean))
const waiting = computed(() => action.value?.state === 'waiting')
const canSubmit = computed(() => !!preview.value && !busy.value && !action.value && (choice.value === 'cancel'
  ? preview.value.open_leaves > 0 : preview.value.is_leaf && childTitles.value.length > 0 && childTitles.value.length <= 20 && childTitles.value.every(title => title.length <= 500)))
// One stable label for each option; busy feedback is in the reserved message slot.
let generation = 0
let timer: ReturnType<typeof setTimeout> | undefined
const identity = () => `${props.personId}:${props.nodeId}`

function reset() {
  generation++
  if (timer) clearTimeout(timer)
  preview.value = null; action.value = null; titles.value = ''; error.value = ''; busy.value = false
}
async function load() {
  const token = ++generation, target = props.nodeId, person = identity()
  try {
    const value = await workLifecycleRequest<WorkPreview>(target)
    if (token !== generation || person !== identity()) return
    preview.value = value; action.value = value.pending; choice.value = value.pending?.kind ?? (value.is_leaf ? 'split' : 'cancel')
    if (value.pending) schedule()
  } catch (cause) { if (token === generation && person === identity()) error.value = cause instanceof Error ? cause.message : 'Preview could not be loaded' }
}
function schedule() { if (timer) clearTimeout(timer); timer = setTimeout(() => { void check() }, 2000) }
async function check() {
  const saved = action.value
  if (!saved || saved.state !== 'waiting' || busy.value) return
  const token = generation, target = props.nodeId, person = identity()
  busy.value = true
  try {
    const result = await workLifecycleRequest<WorkAction>(target, `/${encodeURIComponent(saved.id)}/continue`, 'POST')
    if (token !== generation || person !== identity()) return
    action.value = result; error.value = ''
    if (result.state === 'completed') emit('completed')
    else schedule()
  } catch (cause) { if (token === generation && person === identity()) error.value = cause instanceof Error ? cause.message : 'Handover could not be checked' }
  finally { if (token === generation && person === identity()) busy.value = false }
}
async function submit() {
  if (!canSubmit.value || !preview.value) return
  const token = generation, target = props.nodeId, person = identity(), snapshot = preview.value
  busy.value = true; error.value = ''
  try {
    const result = await workLifecycleRequest<WorkAction>(target, '', 'POST', {
      request_id: crypto.randomUUID(), kind: choice.value, expected_updated_at: snapshot.updated_at,
      expected_open_leaves: snapshot.open_leaves, expected_scope_revision: snapshot.scope_revision,
      ...(choice.value === 'split' ? { children: childTitles.value.map(title => ({ title })) } : {}),
    })
    if (token !== generation || person !== identity()) return
    action.value = result
    if (result.state === 'completed') emit('completed')
    else schedule()
  } catch (cause) { if (token === generation && person === identity()) error.value = cause instanceof Error ? cause.message : 'Work action failed' }
  finally { if (token === generation && person === identity()) busy.value = false }
}
async function abandon() {
  const saved = action.value
  if (!saved || busy.value) return
  const token = generation, target = props.nodeId, person = identity()
  busy.value = true
  try {
    await workLifecycleRequest<WorkAction>(target, `/${encodeURIComponent(saved.id)}`, 'DELETE')
    if (token !== generation || person !== identity()) return
    reset(); await load()
  } catch (cause) { if (token === generation && person === identity()) error.value = cause instanceof Error ? cause.message : 'Request could not be abandoned' }
  finally { if (token === generation && person === identity()) busy.value = false }
}
function close() { dialog.value?.close(); emit('close') }
function keydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopPropagation()
    if ((event.target as HTMLElement).matches('input,textarea,[contenteditable="true"]')) (event.target as HTMLElement).blur()
    else close()
  } else if (event.key === 'Enter' && (mac ? event.metaKey : event.ctrlKey) && !event.altKey && !event.shiftKey) {
    event.preventDefault(); event.stopPropagation(); void submit()
  }
}
watch(() => [props.nodeId, props.personId], () => { reset(); void load() })
onMounted(async () => { dialog.value?.showModal(); await nextTick(); closeButton.value?.focus(); void load() })
onBeforeUnmount(reset)
</script>

<template>
  <dialog ref="dialog" class="work-lifecycle" aria-labelledby="work-lifecycle-title" @cancel.prevent="close" @keydown="keydown">
    <header><span class="eyebrow">{{ nodeKey }}</span><h2 id="work-lifecycle-title">Work actions</h2></header>
    <div class="choices" role="radiogroup" aria-label="Work action">
      <button type="button" role="radio" :aria-checked="choice === 'split'" :disabled="!!action || preview?.is_leaf === false" @click="choice = 'split'">Split into children</button>
      <button type="button" role="radio" :aria-checked="choice === 'cancel'" :disabled="!!action" @click="choice = 'cancel'">Cancel with its open children</button>
    </div>
    <div class="actions">
      <button type="button" class="btn primary" :disabled="!canSubmit" @click="submit"><span class="action-label"><span :class="{ inactive: choice !== 'split' }" :aria-hidden="choice !== 'split'">Split into children</span><span :class="{ inactive: choice !== 'cancel' }" :aria-hidden="choice !== 'cancel'">Cancel with its open children</span></span> <KeyCap k="mod" /><KeyCap k="enter" /></button>
      <button ref="closeButton" type="button" class="btn" @click="close">Close <KeyCap k="Esc" /></button>
    </div>
    <div class="follow-up">
      <button type="button" class="btn" :disabled="!waiting || busy" @click="check">Check handover</button>
      <button type="button" class="btn" :disabled="!waiting || busy" @click="abandon">Abandon request</button>
    </div>
    <div class="body">
      <div class="message" role="status" aria-live="polite">
        <p v-if="error" class="error">{{ error }}</p>
        <p v-else-if="!preview">Loading work…</p>
        <p v-else-if="action?.state === 'completed'">{{ action.kind === 'split' ? `${action.result.length} children created.` : 'The open leaves have been handled.' }}</p>
        <p v-else-if="waiting">Waiting for {{ action?.waiting_count }} {{ action?.waiting_count === 1 ? 'leaf' : 'leaves' }} to stop gracefully. {{ action?.kind === 'cancel' ? `${action.result.length} of ${action.target_count} leaves handled.` : 'Children will be created after handover.' }}</p>
        <p v-else-if="choice === 'split'">{{ preview.busy ? 'This leaf is busy. Its agent will receive a graceful handover request first.' : 'This leaf becomes a parent. Its history and past work stay here.' }}</p>
        <p v-else>{{ preview.open_leaves }} open {{ preview.open_leaves === 1 ? 'leaf' : 'leaves' }} will be cancelled. Running agents receive graceful stop requests; each leaf waits for its run to stop. Finished leaves stay finished.</p>
      </div>
      <div v-if="waiting" class="waiting-tools">
        <p>Closing this sheet keeps the saved request running. Abandoning it leaves completed work and handover requests in place.</p>
      </div>
      <label v-else-if="choice === 'split' && !action" class="child-draft">Child titles, one per line
        <textarea v-model="titles" rows="4" maxlength="10000" :placeholder="'First piece of work\nSecond piece of work'" />
        <span>1–20 children. Submit with <KeyCap k="mod" /> <KeyCap k="enter" />.</span>
      </label>
    </div>
  </dialog>
</template>

<style scoped>
.work-lifecycle { position: fixed; inset: 8vh auto auto 50%; transform: translateX(-50%); margin: 0; width: min(var(--dialog-m), calc(100vw - 2rem)); max-height: 84dvh; padding: 1.5rem; border: 1px solid var(--line); border-radius: var(--radius-lg); background: var(--surface); color: var(--ink); box-shadow: var(--shadow-lg); overflow: hidden; }
.work-lifecycle[open] { display: flex; flex-direction: column; }
.work-lifecycle::backdrop { background: color-mix(in srgb, var(--shadow-black) 40%, transparent); }
header h2 { margin: .25rem 0 1.2rem; font-size: 1.25rem; }
.eyebrow { color: var(--ink-3); font-size: .8rem; }
.choices { display: grid; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); }
.choices button { min-height: 44px; height: 44px; text-align: left; padding: 0 .75rem; font: inherit; font-size: .9rem; border: 0; border-radius: 0; background: transparent; color: inherit; }
.choices button[aria-checked="true"] { background: var(--surface-2); font-weight: 600; }
.choices button:disabled { opacity: .55; }
.actions { display: flex; flex-wrap: wrap; gap: .5rem; margin: 1rem 0; }
@media (pointer: coarse), (max-width: 720px) { .actions .btn, .follow-up .btn { min-height: 44px; } }
.body { min-width: 0; min-height: 0; overflow: auto; }
header,.choices,.actions,.follow-up { flex-shrink: 0; }
.action-label { display: grid; }
.action-label span { grid-area: 1 / 1; }
.action-label .inactive { visibility: hidden; }
.follow-up { display: flex; flex-wrap: wrap; gap: .5rem; margin-bottom: 1rem; }
.message { line-height: 1.55; }
.message p { margin: 0 0 .75rem; }
.error { color: var(--danger); }
.child-draft { display: grid; gap: .5rem; font-size: .9rem; }
textarea { width: 100%; box-sizing: border-box; resize: vertical; background: var(--surface); color: var(--ink); border: 1px solid var(--line); border-radius: var(--radius); padding: .75rem; font: inherit; }
.child-draft span,.waiting-tools p { color: var(--ink-3); font-size: .8rem; }
.waiting-tools { display: flex; flex-wrap: wrap; gap: .5rem; }
.waiting-tools p { flex-basis: 100%; }
@media (max-width: 720px) {
 .work-lifecycle { inset: 0; transform: none; width: 100%; max-width: none; height: 100dvh; max-height: none; box-sizing: border-box; border-radius: 0; padding: calc(1rem + env(safe-area-inset-top)) 1rem 0; display: flex; flex-direction: column; overflow: hidden; }
 header,.choices { flex-shrink: 0; }
 .body { flex: 1; overflow: auto; padding-top: 1rem; }
 .actions { order: 5; flex-shrink: 0; margin: 0 -1rem; padding: 1rem 1rem calc(1rem + env(safe-area-inset-bottom)); border-top: 1px solid var(--line); background: var(--surface); }
 .actions .btn { flex: 1 1 auto; }
}
</style>
