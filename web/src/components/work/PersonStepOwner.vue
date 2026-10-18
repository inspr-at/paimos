<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { getNode } from '../../lib/api'
import { readStepOwners, reassignPersonStep, type PersonStep, type PersonSteps, type StepPerson } from '../../lib/personSteps'
import { rowStore } from '../../lib/rowStore'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'

const props = defineProps<{ step: PersonStep; disabled?: boolean; ask?: boolean; headless?: boolean }>()
const emit = defineEmits<{ changed: [result: PersonSteps]; close: []; busy: [value: boolean] }>()
const session = useSession(), anchor = ref<HTMLElement | null>(null), people = ref<StepPerson[]>([]), query = ref(''), after = ref<string | null>(null), busy = ref(false), error = ref(''), loading = ref(false)
const mine = computed(() => props.step.owner.id === session.identity?.principal.id)
let generation = 0, abort: AbortController | undefined, disposed = false
const identity = () => `${session.identity?.tenant.id}:${session.identity?.principal.id}`
function close(restore = false) { const target = anchor.value; anchor.value = null; generation++; abort?.abort(); if (restore) target?.focus({ preventScroll: true }); emit('close') }
async function load(more = false) {
  abort?.abort(); abort = new AbortController()
  const controller = abort, turn = ++generation, step = props.step, owner = identity(), term = query.value
  loading.value = true; error.value = ''
  try {
    const page = await readStepOwners(step, term, more ? after.value ?? undefined : undefined, controller.signal)
    if (!disposed && turn === generation && owner === identity() && props.step.id === step.id && props.step.revision === step.revision) { people.value = more ? [...people.value, ...page.items] : page.items; after.value = page.next_cursor }
  } catch (e) { if (turn === generation && !controller.signal.aborted) error.value = e instanceof Error ? e.message : 'People could not be read.' }
  finally { if (turn === generation) loading.value = false }
}
function openAt(target: HTMLElement) { anchor.value = target; query.value = ''; people.value = []; after.value = null; void load() }
function open(event: MouseEvent) { openAt(event.currentTarget as HTMLElement) }
defineExpose({ openAt })
async function choose(person: StepPerson) {
  if (busy.value || props.disabled || !props.step.can_tick) return
  const step = { ...props.step }, owner = identity(), sent = rowStore.mark()
  busy.value = true; emit('busy', true); error.value = ''
  try {
    const result = await reassignPersonStep(step, person.id)
    if (disposed || owner !== identity() || props.step.id !== step.id || props.step.revision !== step.revision) return
    // A successful write is reported even if the subsequent list refresh fails.
    emit('changed', result); close(true)
    try { const node = await getNode(step.node_id); if (!disposed && owner === identity() && node.id === step.node_id) rowStore.adoptNode(node, sent, { show: true }) } catch { /* The caller retains the confirmed server response. */ }
  } catch (e) { if (!disposed && owner === identity()) error.value = e instanceof Error ? e.message : 'The owner could not be changed.' }
  finally { if (!disposed && owner === identity()) { busy.value = false; emit('busy', false) } }
}
watch(query, () => { if (anchor.value) void load() })
watch(() => [props.step.id, props.step.revision, identity()], () => { close(); people.value = []; error.value = ''; busy.value = false })
onBeforeUnmount(() => { disposed = true; generation++; abort?.abort(); emit('busy', false) })
</script>
<template>
  <span v-if="!headless" class="step-who">
    <template v-if="!ask">Who: </template>
    <button v-if="step.can_tick" type="button" class="step-owner" :disabled="disabled || busy" :aria-label="ask ? 'Ask someone else…' : `Who checks this: ${step.owner.name}. Ask someone else`" aria-haspopup="dialog" :aria-expanded="!!anchor" :data-tip="step.owner.name" @click="open"><span>{{ ask ? 'Ask someone else…' : `${step.owner.name}${mine ? ' (you)' : ''}` }}</span><AppIcon name="chevron" :size="12" /></button>
    <b v-else>{{ step.owner.name }}</b>
  </span>
  <FloatingPanel v-if="anchor" :anchor="anchor" :width="360" :tallest="520" label="Who checks this" cycle field-escape :sheet="true" compact-sheet @close="close">
    <div class="owner-picker">
      <header><strong>Who checks this</strong><button type="button" class="btn sm" @click="close(true)">Cancel</button></header>
      <input v-model="query" type="search" class="field" aria-label="Find a person" placeholder="Find a person…" maxlength="100" :disabled="busy" />
      <div class="owner-options" role="group" aria-label="People with project access">
        <button v-for="person in people" :key="person.id" type="button" class="owner-option" :aria-pressed="person.id === step.owner.id" :disabled="busy || loading" :data-tip="person.name" @click="choose(person)"><span>{{ person.name }}{{ person.id === session.identity?.principal.id ? ' (you)' : '' }}</span><AppIcon v-if="person.id === step.owner.id" name="check" :size="14" /></button>
        <p v-if="!loading && !error && !people.length">No matching person with project access.</p>
      </div>
      <footer><button v-if="after" type="button" class="btn sm" :disabled="loading || busy" @click="load(true)">Show more</button><span role="status">{{ error || (loading ? 'Loading people…' : 'The step and its Desk item go to the chosen person.') }}</span><button v-if="error" type="button" class="btn sm" @click="load()">Retry</button></footer>
    </div>
  </FloatingPanel>
</template>
<style scoped>
.step-who { display: flex; align-items: center; min-width: 0; gap: .35em; font-size: 12px; color: var(--ink-2); }
.step-who b { font-weight: 500; overflow-wrap: anywhere; }
.step-owner { display: inline-flex; align-items: center; gap: 3px; min-width: 0; padding: 0; border: 0; background: transparent; color: inherit; font: inherit; text-align: left; }
.step-owner span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.step-owner svg { flex: none; }
.step-owner:hover { color: var(--ink); }
.owner-picker { display: flex; flex-direction: column; gap: 12px; padding: 8px; max-height: calc(var(--floating-max) - 24px); }
.owner-picker header { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex: none; }
.owner-picker input { flex: none; }
.owner-options { overflow: auto; min-height: 0; }
.owner-option { display: flex; align-items: center; justify-content: space-between; gap: 12px; width: 100%; height: 44px; border: 0; border-radius: 6px; padding: 0 8px; background: transparent; color: var(--ink); font: inherit; text-align: left; }
.owner-option span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.owner-option:hover { background: var(--row-hover); }
.owner-option[aria-pressed=true] { background: var(--row-selected); }
.owner-picker footer { flex: none; min-height: 44px; display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--ink-2); overflow-wrap: anywhere; }
@media (pointer: coarse) { .step-owner { min-height: 44px; } }
@media (max-width: 600px) { .owner-picker { height: 100%; max-height: 100%; padding-bottom: env(safe-area-inset-bottom); } .owner-options { flex: 1; } }
</style>
