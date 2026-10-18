<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { settingsLink } from '../../lib/settings'
import { useBusiness } from '../../stores/business'
import { availability, statusLabel } from './catalog'
import { businessParts, businessSections, partLabel, SETUP_PLACE } from './areas'
import AppIcon from './BizIcon.vue'

// What the overview says about the business parts: which are on, and what is
// missing. It only reads; Settings › Business is the one place that switches them.
// The intro greets a workspace with nothing on and carries the way there as its
// primary action; the status sits under the page head, which carries that link.
defineProps<{ variant: 'intro' | 'status' }>()
const business = useBusiness()
const and = (labels: string[]) => labels.length > 1 ? `${labels.slice(0, -1).join(', ')} and ${labels.at(-1)}` : labels[0] ?? ''
const rows = computed(() => businessParts.map(part => {
  const section = businessSections.find(s => s.id === part.id)!
  const state = business.plugins ? availability(section, business.plugins) : null
  const on = business.open[part.id]
  const gate = state?.gate.state
  return {
    ...part, on, summary: section.summary,
    status: on ? 'Enabled' : state ? statusLabel(state) : '',
    attention: !on && (gate === 'digest_mismatch' || gate === 'under_granted'),
    needs: on ? [] : part.needs.filter(need => !business.open[need]).map(need => partLabel(need)),
  }
}))
</script>

<template>
  <section class="parts glass-card" :class="variant" aria-labelledby="parts-title">
    <header class="parts-head">
      <span class="parts-icon"><AppIcon name="briefcase" :size="18" /></span>
      <div>
        <h2 id="parts-title">{{ variant === 'intro' ? 'Set up Business' : 'Business parts' }}</h2>
        <p>{{ variant === 'intro'
          ? `Customers, quotes priced from your rates, and hours on tickets with an admin’s approval per period. Each part is a first-party plugin you enable in ${SETUP_PLACE}. Your projects and tickets stay as they are.`
          : `Which parts are on for everyone in this workspace, and what is missing. Parts are enabled and disabled in ${SETUP_PLACE}.` }}</p>
      </div>
    </header>
    <ul class="areas" aria-label="Business parts">
      <li v-for="row in rows" :key="row.id" class="area" :class="{ on: row.on }">
        <span class="area-icon"><AppIcon :name="row.icon" :size="15" /></span>
        <span class="area-text">
          <span class="area-name">{{ row.label }}</span>
          <span v-if="variant === 'intro'" class="area-summary">{{ row.summary }}</span>
          <span v-if="row.needs.length" class="area-needs">Needs {{ and(row.needs) }}.</span>
        </span>
        <span class="area-state" :class="{ on: row.on, attention: row.attention }"><AppIcon :name="row.on ? 'circle-check' : row.attention ? 'alert' : 'minus'" :size="14" />{{ row.status }}</span>
      </li>
    </ul>
    <footer v-if="variant === 'intro' && business.admin" class="parts-foot">
      <RouterLink class="btn primary" :to="settingsLink('business', 'parts')">Set up in {{ SETUP_PLACE }}<AppIcon name="arrow" :size="14" /></RouterLink>
    </footer>
  </section>
</template>

<style scoped>
.parts { padding: 20px 22px 18px; }
.parts.status { padding: 16px 18px 14px; }
.parts-head { display: flex; gap: 14px; align-items: flex-start; }
.parts-head h2 { font-size: 17px; font-weight: 650; }
.parts-head p { margin-top: 4px; font-size: 13.5px; max-width: 70ch; }
.status .parts-head p { font-size: 12.5px; }
.parts-icon { display: grid; place-items: center; flex-shrink: 0; width: 40px; height: 40px; border-radius: 12px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.areas { display: grid; gap: 2px; margin: 16px 0 0; padding: 0; list-style: none; }
.area { display: grid; grid-template-columns: 30px minmax(0, 1fr) auto; grid-template-areas: "icon text state"; align-items: center; column-gap: 14px; min-height: 50px; padding: 10px 6px 10px 10px; border-top: 1px solid var(--line); }
.area-icon { grid-area: icon; display: grid; place-items: center; width: 30px; height: 30px; border-radius: 9px; background: var(--code-bg); color: var(--ink-2); }
.area.on .area-icon { background: var(--chip-teal-bg); color: var(--teal-ink); }
.area-text { grid-area: text; display: grid; gap: 2px; min-width: 0; }
.area-name { font-size: 14px; font-weight: 650; color: var(--ink); }
.area-summary { font-size: 12.5px; color: var(--ink-2); }
.area-needs { font-size: 12px; color: var(--warn-ink); }
/* The state is a word with its own shape, so it reads without colour. */
.area-state { grid-area: state; display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; font-weight: 600; color: var(--ink-3); white-space: nowrap; }
.area-state.on { color: var(--ok); }
.area-state.attention { color: var(--warn-ink); }
.parts-foot { display: flex; justify-content: flex-end; margin-top: 12px; padding-top: 14px; border-top: 1px solid var(--line); }
@media (max-width: 720px) {
  .parts, .parts.status { padding: 16px 14px; }
  .parts-foot .btn { flex: 1; justify-content: center; height: 44px; }
}
/* Phones: the state goes under the name, so the name and its needs keep the width. */
@media (max-width: 600px) {
  .area { grid-template-columns: 30px minmax(0, 1fr); grid-template-areas: "icon text" ". state"; row-gap: 4px; align-items: start; }
  .area-icon { align-self: center; }
}
</style>
