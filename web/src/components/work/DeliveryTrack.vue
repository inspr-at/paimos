<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'
import { deliverySteps, stateLabel, type DeliveryLanguage } from '../../lib/delivery'
withDefaults(defineProps<{ step: number; variant?: 'normal' | 'late' | 'failed' | 'held'; lang?: DeliveryLanguage }>(), { variant: 'normal', lang: 'en' })
</script>
<template>
  <ol class="track" :class="variant" :aria-label="lang === 'de' ? 'Lieferschritte' : 'Delivery steps'">
    <li v-for="(state, index) in deliverySteps" :key="state" :class="{ done: index < step, current: index === step }" :aria-current="index === step ? 'step' : undefined">
      <span class="dot"><AppIcon v-if="index < step" name="check" :size="9" /><AppIcon v-else-if="index === step && variant !== 'normal' && variant !== 'late'" :name="variant === 'held' ? 'pause' : 'close'" :size="9" /><i v-else-if="index === step" /></span>
      <span class="label">{{ stateLabel(state, lang) }}</span>
    </li>
  </ol>
</template>
<style scoped>
.track { display:grid; grid-template-columns:repeat(6,minmax(0,1fr)); margin:12px -4px 10px; padding:0; list-style:none; }
li { position:relative; display:grid; justify-items:center; align-content:start; gap:5px; min-width:0; padding:0 2px; text-align:center; font-size:11px; line-height:1.25; color:var(--ink-3); }
li+li::before { content:''; position:absolute; top:6px; left:calc(-50% + 10px); right:calc(50% + 10px); height:2px; border-radius:2px; background:var(--track); }
.done::before,.current::before { background:color-mix(in srgb,var(--primary-line) 55%,transparent); }
.dot { position:relative; z-index:1; display:grid; place-items:center; width:14px; height:14px; border-radius:50%; background:var(--surface-raised); box-shadow:inset 0 0 0 1.5px var(--line-2); }
.done .dot { background:var(--primary); box-shadow:none; color:var(--primary-on); }
.current .dot { background:var(--primary-tint-2); box-shadow:inset 0 0 0 2px var(--primary-line); color:var(--primary-ink); }
.dot i { width:4px; height:4px; border-radius:50%; background:currentColor; }
.late .current .dot { background:var(--queue-wait-bg); box-shadow:inset 0 0 0 2px var(--gold); color:var(--warn-ink); }
.failed .current .dot { background:var(--danger-bg); box-shadow:inset 0 0 0 2px var(--danger); color:var(--danger); }
.held .current .dot { background:var(--surface-raised); box-shadow:inset 0 0 0 2px var(--ink-3); color:var(--ink-2); }
.label { max-width:100%; hyphens:auto; overflow-wrap:break-word; }
.done .label { color:var(--ink-2); }.current .label { color:var(--ink); font-weight:600; }
@container delivery (max-width:479px) { .label { min-height:2.5em; font-size:10.5px; } li { padding:0 1px; } }
</style>
