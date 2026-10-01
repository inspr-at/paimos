<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { normaliseState, statusOptions } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'
import StatusIcon from './StatusIcon.vue'

const props = defineProps<{ anchor: HTMLElement | null; current: string; knownStates: string[]; ticketKey: string }>()
const emit = defineEmits<{ choose: [state: string]; close: [restoreFocus: boolean] }>()
const list = ref<HTMLElement>()
const options = computed(() => statusOptions(props.knownStates))
const current = computed(() => normaliseState(props.current))

function move(event: KeyboardEvent) {
  const items = [...(list.value?.querySelectorAll<HTMLButtonElement>('button') ?? [])]
  const index = items.indexOf(document.activeElement as HTMLButtonElement)
  let next = -1
  if (event.key === 'ArrowDown' || event.key === 'j') next = Math.min(items.length - 1, index + 1)
  else if (event.key === 'ArrowUp' || event.key === 'k') next = Math.max(0, index - 1)
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = items.length - 1
  else if (/^[1-9]$/.test(event.key) && Number(event.key) <= items.length) { event.preventDefault(); items[Number(event.key) - 1].click(); return }
  if (next >= 0) { event.preventDefault(); event.stopPropagation(); items[next]?.focus() }
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="212" :label="`Status of ${ticketKey}`" @close="restore => emit('close', restore)">
    <p class="menu-title eyebrow">Status</p>
    <div ref="list" class="menu" role="menu" :aria-label="`Status of ${ticketKey}`" @keydown="move">
      <button v-for="(option, index) in options" :key="option.value" type="button" role="menuitemradio" class="menu-item" :aria-checked="normaliseState(option.value) === current" :data-autofocus="normaliseState(option.value) === current ? '' : undefined" @click="emit('choose', option.value)">
        <StatusIcon :state="option.value" />
        <span class="label">{{ option.meta.label }}</span>
        <AppIcon v-if="normaliseState(option.value) === current" name="check" :size="14" class="tick" />
        <span v-else class="digit keycap" aria-hidden="true">{{ index + 1 }}</span>
      </button>
    </div>
    <p class="menu-help"><AppIcon name="queue" :size="12" /><span><b>Queued</b> is Open with a place in the work queue. Pickup sets In progress; Blocked keeps the place and waits.</span></p>
  </FloatingPanel>
</template>

<style scoped>
.menu-help { display: flex; align-items: flex-start; gap: 7px; margin: 6px 4px 0; padding: 8px 6px 2px; border-top: 1px solid var(--line); font-size: 11.5px; color: var(--ink-3); }
.menu-help svg { flex: none; margin-top: 2px; color: var(--teal-ink); }
.menu-title { padding: 6px 10px 4px; }
.menu { display: grid; gap: 1px; }
.menu-item { display: flex; align-items: center; gap: 10px; height: 32px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.menu-item:hover { background: var(--row-hover); }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.menu-item:active { background: var(--row-selected); }
.menu-item .label { flex: 1; }
.tick { color: var(--teal); }
.digit { opacity: 0; }
.menu-item:hover .digit, .menu-item:focus-visible .digit { opacity: 1; }
</style>
