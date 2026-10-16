<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import { defaultTip } from '../../lib/viewSwitch'
import { useViewMenuTrigger } from '../../lib/viewMenuTrigger'
import type { ViewOption } from './projectNavigation'

// The segmented look every view switch shares (AEON-1453): tabs, the project's sections,
// or the round .seg radio group. The person's default carries the small pin in reserved
// space, so marking one never moves a segment. With `menu`, right-click, a long press and
// Shift+F10 report the segment; ViewSwitch opens its menu.
const props = withDefaults(defineProps<{ items: readonly ViewOption[]; selected: string; label: string; sections?: boolean; seg?: boolean; tips?: boolean; iconSize?: number; defaultId?: string | null; menu?: boolean }>(), { defaultId: null, iconSize: undefined })
const emit = defineEmits<{ select: [id: string]; menu: [id: string, anchor: HTMLElement] }>()
const trigger = useViewMenuTrigger((id, anchor) => emit('menu', id, anchor))
const role = computed(() => props.seg ? 'radio' : 'tab')
const glyph = computed(() => props.iconSize ?? (props.seg ? 14 : 15))
const MOVES = ['ArrowLeft', 'ArrowRight', 'Home', 'End']
const RADIO_MOVES = [...MOVES, 'ArrowUp', 'ArrowDown']
const focusable = (id: string) => props.selected === id || (!props.items.some(tab => tab.id === props.selected) && id === props.items.find(tab => !tab.disabled)?.id)
const tip = (item: ViewOption) => item.id === props.defaultId ? defaultTip(item.label) : item.tip ?? (props.tips ? item.label : undefined)
function move(event: KeyboardEvent) {
  // Tabs keep every key to themselves, as they always did; the radio look passes on the keys it does not use.
  if (!props.seg) event.stopPropagation()
  const button = (event.target as HTMLElement).closest<HTMLButtonElement>('[data-view]')
  if (props.menu && button?.dataset.view && trigger.keydown(event, button.dataset.view, button)) return
  if (event.altKey || event.ctrlKey || event.metaKey) return
  if (!(props.seg ? RADIO_MOVES : MOVES).includes(event.key)) return
  event.preventDefault()
  event.stopPropagation()
  const buttons = Array.from((event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>(`[role="${role.value}"]:not(:disabled)`))
  const index = buttons.indexOf(event.target as HTMLButtonElement)
  const forward = event.key === 'ArrowRight' || event.key === 'ArrowDown'
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1
    : (index + (forward ? 1 : -1) + buttons.length) % buttons.length
  buttons[next]?.focus()
  buttons[next]?.click()
}
function click(event: MouseEvent, id: string) {
  if (trigger.heldClick(event)) return
  emit('select', id)
}
</script>

<template>
  <div :class="seg ? 'seg' : ['project-tabs', { sections }]" :role="seg ? 'radiogroup' : 'tablist'" :aria-label="label" @keydown="move">
    <button v-for="item in items" :key="item.id" type="button" :role="role" v-bind="item.data"
      :aria-selected="seg ? undefined : selected === item.id" :aria-checked="seg ? selected === item.id : undefined"
      :aria-label="item.ariaLabel ?? item.label" :aria-description="item.id === defaultId ? 'Opens here by default' : undefined" :data-tip="tip(item)" :data-view="item.id"
      :disabled="item.disabled" :tabindex="focusable(item.id) ? 0 : -1" @click="click($event, item.id)"
      @pointerdown="menu && trigger.pointerdown($event, item.id)" @pointermove="trigger.pointermove" @pointerup="trigger.pointerend" @pointercancel="trigger.pointerend"
      @contextmenu="menu && trigger.contextmenu($event, item.id)">
      <AppIcon v-if="item.icon" :name="item.icon" :size="glyph" /><span class="tab-label">{{ item.label }}</span>
      <span v-if="item.id === defaultId" class="view-pin" aria-hidden="true"><AppIcon name="pin" :size="10" /></span>
    </button>
    <slot />
  </div>
</template>

<style scoped>
.project-tabs { display: inline-flex; flex-shrink: 0; align-items: center; gap: 2px; padding: 3px; border-radius: 9px; background: var(--seg-bg); }
:where(.project-tabs) > button { display: inline-flex; align-items: center; justify-content: center; gap: 6px; min-height: 28px; padding: 0 10px; border: 0; border-radius: 6px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
:where(.project-tabs) > button svg { flex-shrink: 0; }
:where(.project-tabs) > button:hover { background: var(--row-hover); color: var(--ink); }
:where(.project-tabs) > button[aria-selected="true"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: 0 1px 2px color-mix(in srgb, var(--shadow-color) 12%, transparent), inset 0 0 0 1px var(--glass-edge); }
:where(.project-tabs) > button:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; }
/* The default's pin sits in the corner of its own segment: no segment grows or moves for it. */
button { position: relative; -webkit-touch-callout: none; }
.view-pin { position: absolute; right: 2px; bottom: 1px; display: inline-grid; color: var(--ink-3); pointer-events: none; }
.sections { margin-top: 18px; gap: 4px; padding: 0; background: transparent; }
.sections button { min-height: 36px; padding: 0 14px; font-size: 13.5px; }
.sections button[aria-selected="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--line); }
@media (max-width: 600px) {
  .project-tabs { max-width: 100%; }
  :where(.project-tabs) > button { flex: 1; min-height: 40px; padding: 0 9px; }
  .sections { display: flex; }
  .sections button { flex: 1; min-width: 0; min-height: 44px; padding: 0 8px; }
  /* Four sections no longer fit as words at 360 px (AEON-1007): only the selected one shows its word;
     the others keep their icon, with the name in aria-label and the tooltip. */
  .sections button:not([aria-selected="true"]) .tab-label { display: none; }
  .sections button[aria-selected="true"] { flex: 2.4; }
  .sections .tab-label { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
}
</style>
