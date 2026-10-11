<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import BizIcon, { type BizIconName } from '../business/BizIcon.vue'

// One group of settings: an icon, a title, a line on what it does, then the
// controls or read-only facts. `anchor` makes it a deep-link target.
defineProps<{ title: string; icon: BizIconName; anchor?: string; variant?: 'project' }>()
</script>

<template>
  <section :id="anchor" class="settings-card glass-card" :class="{ 'project-card m-card': variant === 'project' }" :aria-labelledby="anchor ? `${anchor}-title` : undefined">
    <header class="card-head">
      <span v-if="variant !== 'project'" class="card-icon" aria-hidden="true"><BizIcon :name="icon" :size="16" /></span>
      <div class="card-titles">
        <h2 :id="anchor ? `${anchor}-title` : undefined">{{ title }}</h2>
        <p v-if="$slots.lead" class="lead"><slot name="lead" /></p>
      </div>
      <div v-if="$slots.aside" class="card-aside"><slot name="aside" /></div>
    </header>
    <div v-if="$slots.default" class="card-body"><slot /></div>
  </section>
</template>

<style scoped>
.settings-card { padding: 20px; scroll-margin-top: 20px; }
/* One rule for every card: the icon sits with the title, controls on the right are
   centred on the title and its line. */
.card-head { display: flex; align-items: center; gap: 12px; }
.card-icon { align-self: flex-start; }
.card-icon { display: grid; place-items: center; flex-shrink: 0; width: 32px; height: 32px; border-radius: 10px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.card-titles { flex: 1; min-width: 0; }
h2 { font: 600 15px/1.35 var(--font); letter-spacing: 0; color: var(--ink); }
.lead { margin-top: 2px; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.card-aside { flex-shrink: 0; display: flex; align-items: center; gap: 8px; align-self: center; }
.card-body { margin-top: 14px; }
.settings-card.project-card { padding: 24px 28px 22px; scroll-margin-top: 16px; }
.project-card .card-head { align-items: flex-start; gap: 16px; padding-bottom: 16px; }
.project-card h2 { font: 600 18px/1.3 var(--font); letter-spacing: -.01em; }
.project-card .lead { margin-top: 4px; font-size: 13.5px; line-height: 1.55; max-width: 66ch; }
.project-card .card-aside { margin-top: 4px; align-self: flex-start; }
.project-card .card-body { margin-top: 0; }
@container project-settings-body (max-width: 600px) {
  .settings-card.project-card { padding: 18px 14px 14px; }
  .project-card .card-head { flex-wrap: wrap; }
  .project-card .card-aside { flex-basis: auto; padding-left: 0; }
}
@media (max-width: 600px) {
  .settings-card { padding: 16px; }
  .card-head { flex-wrap: wrap; }
  .card-aside { flex-basis: 100%; padding-left: 44px; }
}
</style>
