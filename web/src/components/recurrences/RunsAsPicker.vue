<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { listExecutionPrincipals } from '../../lib/api'
import { useIdentityScope } from '../../lib/useIdentityScope'
import type { ExecutionPrincipal } from '../../lib/recurrences'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'

// Runs as (AEON-1184): the restricted agents the owner may pick for a project or
// workspace routine. Pages are read until next_cursor is null, even past an empty
// page; a page for an older revision is dropped and the list is read again.
export type RunsAsState = 'loading' | 'ready' | 'none' | 'unavailable'
const props = defineProps<{ recurrenceId: string; revision: number; scopeLabel: string; modelValue: string | null }>()
const emit = defineEmits<{ 'update:modelValue': [id: string | null]; state: [state: RunsAsState] }>()
const MAX_PAGES = 20
const items = ref<ExecutionPrincipal[]>([]), state = ref<RunsAsState>('loading'), truncated = ref(false), anchor = ref<HTMLElement | null>(null), list = ref<HTMLElement>()
const scope = useIdentityScope(), reads = scope.lane()
const chosen = computed(() => items.value.find(item => item.id === props.modelValue))
function load() {
  const id = props.recurrenceId, revision = props.revision, found: ExecutionPrincipal[] = []
  state.value = 'loading'; items.value = []; truncated.value = false; anchor.value = null; emit('state', 'loading')
  void reads.run(({ after, signal }) => {
    const page = (cursor: string | undefined, count: number): Promise<void> => after(listExecutionPrincipals(id, cursor, signal), result => {
      if (id !== props.recurrenceId || revision !== props.revision) return
      if (!Array.isArray(result.items) || result.items.length > 100) throw new Error('The list of agents could not be read.')
      found.push(...result.items.filter(item => !found.some(known => known.id === item.id)))
      if (result.next_cursor && count < MAX_PAGES) return page(result.next_cursor, count + 1)
      truncated.value = !!result.next_cursor
      items.value = found
      state.value = found.length ? 'ready' : 'none'
      if (!found.some(item => item.id === props.modelValue)) emit('update:modelValue', found.length === 1 ? found[0]!.id : null)
      emit('state', state.value)
    })
    return page(undefined, 1)
  }, { failed: () => { state.value = 'unavailable'; emit('update:modelValue', null); emit('state', 'unavailable') } })
}
watch([() => props.recurrenceId, () => props.revision, () => scope.owner.value], load, { immediate: true })
function open(event: MouseEvent) { anchor.value = anchor.value ? null : event.currentTarget as HTMLElement }
function pick(item: ExecutionPrincipal) { emit('update:modelValue', item.id); const back = anchor.value; anchor.value = null; back?.focus({ preventScroll: true }) }
function close(restore: boolean) { const back = anchor.value; anchor.value = null; if (restore) back?.focus({ preventScroll: true }) }
function keys(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const options = [...(list.value?.querySelectorAll<HTMLButtonElement>('[role="option"]') ?? [])], at = options.indexOf(document.activeElement as HTMLButtonElement)
  event.preventDefault()
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? options.length - 1 : (at + (event.key === 'ArrowDown' ? 1 : -1) + options.length) % options.length
  options[next]?.focus()
}
</script>

<template>
  <span class="runs-as">
    <button v-if="state === 'ready'" id="runs-as" type="button" class="field pick inl" aria-haspopup="listbox" :aria-expanded="!!anchor" :aria-label="`Runs as: ${chosen?.label ?? 'not picked yet'}`" @click="open"><span>{{ chosen?.label ?? 'Pick an agent' }}</span><AppIcon name="chevron" :size="11" /></button>
    <b v-else-if="state === 'loading'" class="ra-wait" role="status">checking the agents…</b>
  </span>
  <FloatingPanel v-if="anchor" :anchor="anchor" :width="300" align="start" label="Runs as" @close="close">
    <div class="ra-pop">
      <p class="ra-h">Runs as</p>
      <div ref="list" role="listbox" aria-label="Runs as" @keydown="keys">
        <button v-for="item in items" :key="item.id" type="button" class="ra-option" role="option" :aria-selected="item.id === modelValue" :data-autofocus="item.id === modelValue || undefined" @click="pick(item)">
          <AppIcon :name="item.id === modelValue ? 'check' : 'agent'" :size="14" /><span class="ra-t">{{ item.label }}</span><span class="ra-d">Agent · restricted to {{ scopeLabel }}</span>
        </button>
      </div>
      <p class="ra-foot">{{ truncated ? `The first ${items.length} are listed. ` : '' }}Only agents that pass the same check as consent are listed. An admin adds one in Settings › Access.</p>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.runs-as { display: inline; }
.pick.inl { display: inline-flex; align-items: center; gap: 6px; width: auto; max-width: 100%; height: 28px; padding: 0 10px; margin: 0 2px; font-weight: 600; vertical-align: 1px; }
.pick.inl span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ra-wait { font-weight: 500; color: var(--ink-3); }
.ra-pop { display: grid; gap: 4px; padding: 6px; }
.ra-h { padding: 4px 8px 2px; font: 500 10.5px/1.5 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
[role="listbox"] { display: grid; gap: 1px; max-height: 280px; overflow: auto; }
.ra-option { display: grid; grid-template-columns: 16px minmax(0, 1fr); column-gap: 10px; align-items: center; width: 100%; min-height: 44px; padding: 6px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); text-align: left; }
.ra-option svg { grid-row: span 2; color: var(--ink-3); }
.ra-option[aria-selected="true"] svg { color: var(--teal-ink); }
.ra-option:hover, .ra-option:focus-visible { background: var(--row-hover); }
.ra-option[aria-selected="true"] { background: var(--row-selected); }
.ra-t { font-size: 13px; font-weight: 600; overflow-wrap: anywhere; }
.ra-d { font-size: 12px; color: var(--ink-3); }
.ra-foot { padding: 6px 8px 4px; font-size: 12px; line-height: 1.45; color: var(--ink-3); }
@media (pointer: coarse) { .pick.inl { min-height: 44px; } }
</style>
