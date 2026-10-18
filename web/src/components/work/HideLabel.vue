<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { HIDE_STATES, hiddenStates, hideLabel } from '../../lib/hideStates'
import { statusMeta } from '../../lib/work'
import StatusIcon from './StatusIcon.vue'
const props = defineProps<{ states?: readonly string[] }>()
const label = computed(() => hideLabel(props.states))
const selected = computed(() => hiddenStates(props.states))
</script>
<template>
  <span class="hide-label-slot" aria-hidden="true">
    <span class="measure">Hide closed</span><span class="measure">Hide finished</span>
    <span class="measure label">Hide <span class="icons"><StatusIcon v-for="state in HIDE_STATES.slice(0, 4)" :key="state" :state="state" :size="13" /></span></span>
    <span class="label">{{ label }}<span v-if="label === 'Hide'" class="icons"><span v-for="state in selected" :key="state" :data-tip="statusMeta(state).label"><StatusIcon :state="state" :size="13" /></span></span></span>
  </span>
</template>
<style scoped>
.hide-label-slot { display: inline-grid; text-align: start; white-space: nowrap; }
.hide-label-slot > span { grid-area: 1 / 1; }
.measure { visibility: hidden; pointer-events: none; }
.label, .icons { display: inline-flex; align-items: center; gap: 5px; }
.icons { gap: 3px; }
.icons > span { display: inline-grid; place-items: center; }
</style>
