<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, ref } from 'vue'
import AppIcon from '../../AppIcon.vue'

// The kinds of work a row can be for: the existing kinds only, never invented here.
defineProps<{ kinds: { column: string; label: string }[]; note?: string; link?: boolean }>()
const emit = defineEmits<{ choose: [column: string]; navigate: [] }>()
const active = ref(0), list = ref<HTMLElement>()
function keys(event: KeyboardEvent, count: number, kinds: { column: string }[]) {
  if (!count) return
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); active.value = (active.value + (event.key === 'ArrowDown' ? 1 : -1) + count) % count; void nextTick(() => list.value?.querySelector('.opt.on')?.scrollIntoView({ block: 'nearest' })) }
  else if (event.key === 'Home' || event.key === 'End') { event.preventDefault(); active.value = event.key === 'Home' ? 0 : count - 1 }
  else if (event.key === 'Enter') { event.preventDefault(); const kind = kinds[active.value]; if (kind) emit('choose', kind.column) }
}
</script>
<template>
  <div ref="list" class="pm-l" role="listbox" aria-label="Kinds of work" tabindex="-1" data-autofocus :aria-activedescendant="kinds[active] ? `kind-${kinds[active]!.column}` : undefined" @keydown="keys($event, kinds.length, kinds)">
    <div v-for="(kind, index) in kinds" :id="`kind-${kind.column}`" :key="kind.column" class="opt plain" :class="{ on: index === active }" role="option" aria-selected="false" :data-kind="kind.column" @click="emit('choose', kind.column)" @mousemove="active = index"><span class="t">{{ kind.label }}</span><span /></div>
    <p v-if="!kinds.length" class="pm-none">Every kind of work already has its own row.</p>
  </div>
  <p v-if="note" class="pm-why"><AppIcon name="info" :size="13" /><span>{{ note }}</span></p>
  <p v-if="link" class="pm-foot">From <RouterLink class="link-btn quiet" to="/settings/kinds" @click="emit('navigate')">Settings › Kinds of work</RouterLink></p>
</template>
