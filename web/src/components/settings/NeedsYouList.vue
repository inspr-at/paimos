<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
import type { IconName } from '../AppIcon.vue'
export interface NeedsYouItem {
  id: string
  name: string
  detail: string
  /** Zero keeps this kind as a calm row while another kind needs attention. */
  count: number
  icon?: IconName
  tone?: 'warning' | 'waiting'
  href?: string
  action?: { label: string; fixesProblem?: boolean; disabled?: boolean }
}
</script>
<script setup lang="ts">
import { computed, useId } from 'vue'
import AppIcon from '../AppIcon.vue'
const props = defineProps<{ items: readonly NeedsYouItem[]; heading?: string; aside?: string; showCalm?: boolean }>()
const emit = defineEmits<{ action: [id: string] }>()
const titleId = useId()
const count = computed(() => props.items.reduce((total, item) => total + Math.max(0, item.count), 0))
const title = computed(() => props.heading ?? (count.value === 0 ? 'Nothing needs you' : `${count.value.toLocaleString()} ${count.value === 1 ? 'thing needs' : 'things need'} you`))
</script>
<template>
  <section class="needs-block" :aria-labelledby="titleId">
    <header class="needs-head"><h2 :id="titleId">{{ title }}</h2><div class="needs-aside"><slot name="aside"><p v-if="aside">{{ aside }}</p></slot></div></header>
    <ul v-if="count || showCalm" class="attention">
      <li v-for="item in items" :key="item.id" class="att-row" :class="{ calm: item.count === 0 }">
        <span class="att-icon" :class="item.count > 0 && item.tone !== 'waiting' ? 'warn' : 'mute'"><AppIcon :name="item.count === 0 ? 'check' : item.icon ?? 'alert'" /></span>
        <a v-if="item.count > 0 && item.href" :href="item.href" class="att-name">{{ item.name }}</a><span v-else class="att-name">{{ item.name }}</span>
        <p>{{ item.detail }}</p>
        <button v-if="item.count > 0 && item.action" type="button" class="btn sm" :class="{ primary: item.action.fixesProblem }" :disabled="item.action.disabled" @click="emit('action', item.id)">{{ item.action.label }}</button>
      </li>
    </ul>
  </section>
</template>
<style scoped>
.needs-block { min-width: 0; container: needs-you / inline-size; }
.needs-head { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 4px 16px; margin-bottom: 12px; }
.needs-head h2 { font: 400 22px/1.25 var(--serif); letter-spacing: -.015em; }
.needs-aside { display: inline-flex; flex-wrap: wrap; gap: 4px 12px; font-size: 12px; color: var(--ink-3); }
.attention { list-style: none; padding: 0; margin: 0; border: 1px solid var(--line); border-radius: var(--radius); background: var(--surface-raised-2); }
.att-row { display: grid; grid-template-columns: 32px minmax(0, 1fr) auto; align-items: center; gap: 2px 14px; padding: 14px 18px; }
.att-row + .att-row { border-top: 1px solid var(--line); }
.att-icon { display: grid; place-items: center; width: 32px; height: 32px; border-radius: 50%; grid-row: span 2; align-self: start; }
.warn { background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); }
.mute { background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); }
.att-name { color: var(--ink); font-size: 14px; font-weight: 650; overflow-wrap: anywhere; }
.att-row p { grid-column: 2; font-size: 13px; color: var(--ink-2); overflow-wrap: anywhere; }
.att-row > .btn { grid-column: 3; grid-row: 1 / span 2; justify-self: end; }
.calm .att-name { color: var(--ink-2); font-weight: 600; }.calm p { color: var(--ink-3); }
@container needs-you (max-width: 520px) { .att-row { grid-template-columns: 32px minmax(0, 1fr); padding: 14px; }.att-row > .btn { grid-column: 2; grid-row: auto; justify-self: start; margin-top: 10px; min-height: 44px; } }
@media (pointer: coarse) { .btn, a.att-name { min-height: 44px; } a.att-name { display: flex; align-items: center; } }
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
