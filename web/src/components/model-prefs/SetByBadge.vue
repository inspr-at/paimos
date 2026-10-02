<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, ref, useId } from 'vue'
import AppIcon from '../AppIcon.vue'
import { resetRowLabel, setByLabel, type PrefLevel, type PrefViewRow } from '../../lib/modelPrefs'
const props = defineProps<{ row: PrefViewRow; level: PrefLevel; kind: string; editable: boolean; busy: boolean; ownLocked: boolean }>()
const emit = defineEmits<{ lock: []; reset: [] }>()
const id = useId(), menu = ref<HTMLElement>(), button = ref<HTMLButtonElement>()
const opened = ref(false)
function hide() { menu.value?.hidePopover(); opened.value = false; button.value?.focus({ preventScroll: true }) }
async function open(event: MouseEvent) {
  if (props.busy) return
  if (event.altKey) { if (props.editable && props.level !== 'project' && (!props.row.locked_by || props.row.locked_by === props.level)) emit('lock'); return }
  if (opened.value) { hide(); return }
  const rect = button.value!.getBoundingClientRect()
  menu.value!.style.left = `${Math.max(8, Math.min(rect.left, innerWidth - 276))}px`
  menu.value!.style.top = `${Math.max(8, Math.min(rect.bottom + 4, innerHeight - 150))}px`
  menu.value?.showPopover(); opened.value = true; await nextTick(); menu.value?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus()
}
function choose(action: 'lock' | 'reset') { hide(); if (action === 'lock') emit('lock'); else emit('reset') }
function keys(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); hide(); return }
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const items = [...menu.value!.querySelectorAll<HTMLButtonElement>('button:not(:disabled)')]
  const i = items.indexOf(document.activeElement as HTMLButtonElement)
  items[event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (i + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus()
}
</script>
<template>
  <button ref="button" type="button" class="set-by" :aria-label="`${kind}: ${setByLabel(row)}`" aria-haspopup="menu" :aria-expanded="opened" :aria-controls="id" :aria-disabled="busy" @click="open"><span class="level-dot" :style="{ '--lv': `var(--level-${row.locked_by || row.set_by || 'default'})` }" /><AppIcon v-if="row.locked_by" name="lock" :size="12" /><span>{{ setByLabel(row) }}</span><AppIcon v-if="row.warnings.length" name="alert" :size="12" :data-tip="row.warnings.join('; ')" /></button>
  <div :id="id" ref="menu" popover="auto" class="set-menu" role="menu" :aria-label="`${kind} setting`" @keydown="keys" @toggle="opened = $event.newState === 'open'">
    <p>{{ setByLabel(row) }}. {{ level === 'project' ? 'Project settings cannot lock other levels.' : 'A row lock fixes both model choices below this level.' }}</p>
    <button v-if="level !== 'project'" type="button" role="menuitem" :disabled="!editable || !!row.locked_by && row.locked_by !== level" @click="choose('lock')"><AppIcon name="lock" :size="14" />{{ ownLocked ? 'Unlock' : level === 'default' ? 'Lock for people and projects' : 'Lock for projects' }}</button>
    <button type="button" role="menuitem" :disabled="!editable || !row.changed_here || !!row.locked_by && row.locked_by !== level" @click="choose('reset')"><AppIcon name="refresh" :size="14" />{{ resetRowLabel(level) }}</button>
  </div>
</template>
<style scoped>
.set-by { display: flex; align-items: center; gap: 5px; width: 100%; min-width: 0; height: 34px; padding: 0 4px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); text-align: left; font-size: 12px; font-weight: 600; }
.set-by:hover { background: var(--row-hover); } .set-by span:last-child { overflow-wrap: anywhere; }
.set-menu { position: fixed; inset: auto; margin: 0; width: 268px; max-width: calc(100vw - 16px); padding: 6px; background: var(--surface-raised); color: var(--ink); border: 1px solid var(--line); border-radius: 12px; box-shadow: var(--shadow-pop); }
.set-menu p { margin: 0; padding: 6px 8px; font-size: 12px; color: var(--ink-3); }
.set-menu button { display: flex; align-items: center; gap: 8px; height: 44px; width: 100%; padding: 0 8px; border: 0; background: transparent; border-radius: 8px; text-align: left; color: var(--ink); font-size: 12px; }
</style>
