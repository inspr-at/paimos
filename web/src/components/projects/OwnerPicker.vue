<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import type { OwnerOption } from '../../lib/importDraft'
import AppIcon from '../AppIcon.vue'
import PersonAvatar from '../work/PersonAvatar.vue'

// The owner of a new project: people only, the person themselves first. The
// list hangs from the field inside the sheet (a modal sheet keeps everything
// outside it inert); Escape closes the list before the sheet, through close().
const props = defineProps<{ modelValue: string; people: OwnerOption[]; me: string; label: string; you: string; find: string; nobody: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [id: string] }>()
const open = ref(false), query = ref(''), trigger = ref<HTMLButtonElement>(), search = ref<HTMLInputElement>(), root = ref<HTMLElement>()
const chosen = computed(() => props.people.find(person => person.id === props.modelValue))
const shown = computed(() => {
  const q = query.value.trim().toLocaleLowerCase()
  return q ? props.people.filter(person => person.name.toLocaleLowerCase().includes(q)) : props.people
})
function outside(event: PointerEvent) { if (!root.value?.contains(event.target as Node)) close(false) }
function show() {
  if (props.disabled) return
  open.value = true; query.value = ''
  document.addEventListener('pointerdown', outside, true)
  void nextTick(() => search.value?.focus({ preventScroll: true }))
}
/** Closes the list; true when it was open, so the sheet keeps Escape for it. */
function close(restore = true): boolean {
  if (!open.value) return false
  open.value = false
  document.removeEventListener('pointerdown', outside, true)
  if (restore) trigger.value?.focus({ preventScroll: true })
  return true
}
function choose(id: string) { emit('update:modelValue', id); close() }
function move(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp'].includes(event.key)) return
  event.preventDefault()
  const items = [...(root.value?.querySelectorAll<HTMLButtonElement>('.menu-item') ?? [])]
  const at = items.indexOf(document.activeElement as HTMLButtonElement)
  items[Math.max(0, Math.min(items.length - 1, at + (event.key === 'ArrowDown' ? 1 : -1)))]?.focus()
}
watch(() => props.disabled, disabled => { if (disabled) close(false) })
onBeforeUnmount(() => document.removeEventListener('pointerdown', outside, true))
defineExpose({ close })
</script>

<template>
  <div ref="root" class="np-f owner">
    <b id="np-owner-l">{{ label }}</b>
    <button id="np-owner" ref="trigger" type="button" class="pick" aria-haspopup="dialog" :aria-expanded="open" aria-labelledby="np-owner-l np-owner" :disabled="disabled" @click="open ? close() : show()">
      <PersonAvatar v-if="chosen" :id="chosen.id" :name="chosen.name" :size="22" />
      <span class="who">{{ chosen?.name ?? '' }}<span v-if="chosen?.id === me" class="faint"> · {{ you }}</span></span>
      <AppIcon name="chevron" :size="14" />
    </button>
    <div v-if="open" class="pop people-pop" role="dialog" :aria-label="label" @keydown="move">
      <input ref="search" v-model="query" class="field pp-search" type="search" :placeholder="find" :aria-label="find" autocomplete="off" @keydown.enter.prevent="shown.length === 1 && choose(shown[0]!.id)">
      <div class="menu" role="menu" :aria-label="label">
        <button v-for="person in shown" :key="person.id" type="button" role="menuitemradio" class="menu-item" :aria-checked="person.id === modelValue" @click="choose(person.id)">
          <PersonAvatar :id="person.id" :name="person.name" :size="18" /><span class="label">{{ person.name }}<span v-if="person.id === me" class="faint"> · {{ you }}</span></span><AppIcon class="ck" name="check" :size="14" />
        </button>
      </div>
      <p v-if="!shown.length" class="pp-none">{{ nobody }}</p>
    </div>
  </div>
</template>

<style scoped>
.np-f { display: grid; gap: 6px; min-width: 0; align-content: start; }
.np-f > b { font-size: 13px; font-weight: 600; }
.pick { display: flex; align-items: center; gap: 8px; width: 100%; min-width: 0; height: 38px; padding: 0 10px 0 8px; border: 1px solid var(--glass-edge); border-radius: var(--radius-s); background: var(--field-bg); color: var(--ink); box-shadow: var(--field-inset), 0 0 0 1px var(--line); font-size: 13.5px; text-align: left; }
.pick:hover:not(:disabled), .pick[aria-expanded="true"] { box-shadow: var(--field-inset), 0 0 0 1px var(--glass-rim); }
.pick:disabled { opacity: 1; color: var(--ink-2); background: var(--surface-2); box-shadow: 0 0 0 1px var(--line); }
.pick:disabled :deep(.avatar) { opacity: .7; }
.pick .who { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pick > svg { color: var(--ink-3); }
.faint { color: var(--ink-3); font-weight: 400; }
/* The list hangs from the owner and date row, so on a phone it spans both columns. */
.people-pop { position: absolute; top: calc(100% + 6px); left: 0; z-index: 4; width: min(264px, 100%); padding: 6px; }
.pp-search { width: 100%; height: 30px; margin-bottom: 4px; font-size: 13px; }
.menu { display: grid; gap: 1px; max-height: 248px; overflow: auto; }
.menu-item { display: flex; align-items: center; gap: 10px; min-width: 0; min-height: 34px; padding: 0 10px 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.menu-item:hover { background: var(--row-hover); }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.menu-item[aria-checked="true"] { background: var(--row-selected); }
.menu-item .label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.menu-item .ck { color: var(--teal-ink); visibility: hidden; }
.menu-item[aria-checked="true"] .ck { visibility: visible; }
.pp-none { padding: 8px 10px; font-size: 12.5px; color: var(--ink-3); }
@media (pointer: coarse), (max-width: 600px) {
  .pick { height: 44px; }
  .pick .who .faint { display: none; }
  .people-pop { width: 100%; }
  .pp-search { height: 44px; font-size: 16px; }
  .menu-item { min-height: 44px; }
}
</style>
