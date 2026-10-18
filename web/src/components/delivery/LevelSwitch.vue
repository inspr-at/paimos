<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Simple · Expert (AEON-994), a ViewSwitch (AEON-1453). Simple is the default; the choice
// is the person's own, and right-click or a long press makes one the level this page opens with.
import { computed } from 'vue'
import ViewSwitch from '../ViewSwitch.vue'
import type { Level } from '../../lib/deliveryNumbers'

const props = defineProps<{ modelValue: Level; label: string; names: Record<Level, string>; defaultValue?: Level | null }>()
const emit = defineEmits<{ 'update:modelValue': [level: Level]; setDefault: [level: Level | null] }>()
const LEVELS: Level[] = ['simple', 'expert']
const options = computed(() => LEVELS.map(level => ({ id: level, label: props.names[level], data: { 'data-level': level } })))
const level = (id: string) => LEVELS.find(value => value === id)
function select(id: string) { const value = level(id); if (value) emit('update:modelValue', value) }
function setDefault(id: string | null) { emit('setDefault', id === null ? null : level(id) ?? null) }
</script>

<template>
  <ViewSwitch class="level" look="seg" :options="options" :selected="modelValue" :label="label" :default-id="defaultValue ?? null" @select="select" @set-default="setDefault" />
</template>

<style scoped>
.level :deep(button) { min-width: 74px; }
@container delivery (max-width: 640px) {
  .level { flex: 1; }
  .level :deep(button) { flex: 1; height: 44px; }
}
</style>
