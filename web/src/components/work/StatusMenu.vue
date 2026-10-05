<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import { normaliseState, statusMeta, statusOptions } from '../../lib/work'
import { getStatusHelp } from '../../lib/api'
import { defaultStatusHelp, statusHint } from '../../lib/statusDefinitions'
import { openStatusHelp } from '../../lib/statusHelp'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'
import StatusIcon from './StatusIcon.vue'

const props = defineProps<{ anchor: HTMLElement | null; current: string; knownStates: string[]; ticketKey: string; projectId?: string; derived?: boolean; childrenCount?: number }>()
const emit = defineEmits<{ choose: [state: string]; close: [restoreFocus: boolean] }>()
const list = ref<HTMLElement>()
const options = computed(() => statusOptions(props.knownStates))
const current = computed(() => normaliseState(props.current))
const help = ref(defaultStatusHelp())
const id = useId()
const controller = new AbortController()
onMounted(() => { void getStatusHelp(props.projectId, controller.signal).then(value => { help.value = value }).catch(() => {}) })
onBeforeUnmount(() => controller.abort())
function hint(state: string) { return statusHint(help.value, statusMeta(state).key === 'progress' ? 'in_progress' : normaliseState(state)) }
function showHelp() { openStatusHelp(props.projectId, props.anchor); emit('close', false) }

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
  <FloatingPanel :anchor="anchor" :width="212" :tallest="480" :label="`Status of ${ticketKey}`" @close="restore => emit('close', restore)">
    <p class="menu-title eyebrow">Status</p>
    <p v-if="derived" class="menu-help" role="status">{{ childrenCount ? `Follows its ${childrenCount} children` : 'Follows its children' }}</p>
    <div v-else ref="list" class="menu" role="menu" :aria-label="`Status of ${ticketKey}`" @keydown="move">
      <template v-for="(option, index) in options" :key="option.value">
      <span v-if="option.meta.key === 'cancelled'" class="menu-sep" role="separator" aria-label="Exits" />
      <button type="button" role="menuitemradio" class="menu-item" :aria-checked="normaliseState(option.value) === current" :data-autofocus="normaliseState(option.value) === current ? '' : undefined" :data-tip="hint(option.value)" data-tip-side="end" :aria-describedby="`${id}-hint-${index}`" @click="emit('choose', option.value)">
        <StatusIcon :state="option.value" />
        <span class="label">{{ option.meta.label }}</span>
        <AppIcon v-if="normaliseState(option.value) === current" name="check" :size="14" class="tick" />
        <span v-else-if="index < 9" class="digit keycap" aria-hidden="true">{{ index + 1 }}</span>
      </button>
      <span :id="`${id}-hint-${index}`" class="sr-only">{{ hint(option.value) }}</span>
      </template>
      <span class="menu-sep" role="separator" />
      <button type="button" role="menuitem" class="menu-item help-item" @click="showHelp"><AppIcon name="help" :size="14" /><span class="label">What do these mean?</span></button>
    </div>
    <p v-if="!derived" class="menu-help"><AppIcon name="queue" :size="12" /><span><b>Queued</b> is Open with a place in the work queue. Pickup sets In progress; Blocked keeps the place and waits.</span></p>
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
.menu-sep { height: 1px; margin: 4px 8px; background: var(--line); }
.help-item { color: var(--ink-2); }
.help-item svg { color: var(--ink-3); }
.help-item:hover svg, .help-item:focus-visible svg { color: var(--teal-ink); }
.tick { color: var(--teal); }
.digit { opacity: 0; }
.menu-item:hover .digit, .menu-item:focus-visible .digit { opacity: 1; }
</style>
