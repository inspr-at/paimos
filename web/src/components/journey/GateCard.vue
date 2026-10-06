<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'

// The decision card beside a stage: an eyebrow, the decision's name and the one
// primary button. Records (decided, done) and blocked gates use the same card.
// On desktop, a button that cannot fit beside the title wraps below it. Phones
// put the action above the title so changing text cannot lift the control.
const props = defineProps<{
  eyebrow: string; title: string; tone?: 'gate' | 'record' | 'blocked'
  action?: { label: string; disabled?: boolean; busy?: boolean; tip?: string } | null
  actionLabels?: readonly string[]
}>()
// Keep the initial decision's text in the sizing grid through its completion.
const initialActionLabel = props.action?.label
const emit = defineEmits<{ act: [] }>()
</script>

<template>
  <section class="gate-card" :class="tone ?? 'gate'" :aria-label="`${eyebrow}: ${title}`">
    <header class="gh">
      <div class="gh-text">
        <p class="eyebrow gate-eyebrow"><AppIcon v-if="tone === 'blocked'" name="alert" :size="11" />{{ eyebrow }}</p>
        <h2>{{ title }}</h2>
      </div>
      <button
        v-if="action" type="button" class="btn primary gate-btn" :disabled="action.disabled || action.busy" :aria-disabled="action.disabled || undefined"
        :data-tip="action.tip" @click="emit('act')"
      >
        <span class="gate-label">
          <!-- Size from all states; only the current label is exposed. -->
          <span class="gate-label-size" aria-hidden="true" :data-label="action.label"></span>
          <span v-if="initialActionLabel" class="gate-label-size" aria-hidden="true" :data-label="initialActionLabel"></span>
          <span class="gate-label-size" aria-hidden="true" data-label="Working…"></span>
          <span v-for="label in actionLabels" :key="label" class="gate-label-size" aria-hidden="true" :data-label="label"></span>
          <span class="gate-label-text">{{ action.busy ? 'Working…' : action.label }}</span>
        </span>
        <AppIcon name="arrow" :size="14" />
      </button>
    </header>
    <div class="gate-body"><slot /></div>
  </section>
</template>

<style scoped>
.gate-card {
  position: relative; display: grid; grid-template-columns: minmax(0, 1fr); min-width: 0; gap: 12px; padding: 16px 18px 16px; border-radius: var(--radius); isolation: isolate;
  background: radial-gradient(120% 90% at 100% 0%, color-mix(in oklab, var(--aqua-2) 70%, transparent), transparent 55%), var(--glass);
  box-shadow: var(--shadow);
  -webkit-backdrop-filter: blur(18px); backdrop-filter: blur(18px);
}
/* A gold hairline just inside the edge. */
.gate-card::after { content: ''; position: absolute; inset: 6px; z-index: -1; border-radius: calc(var(--radius) - 5px); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--gold) 35%, transparent); pointer-events: none; }
.gate-card.record { background: var(--glass); box-shadow: var(--shadow); }
.gate-card.record::after { box-shadow: inset 0 0 0 1px var(--line); }
.gate-card.blocked { background: radial-gradient(120% 90% at 100% 0%, var(--gold-wash), transparent 60%), var(--glass); box-shadow: var(--shadow); }
.gate-card.blocked::after { box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--gold) 35%, transparent); }
.gh { display: flex; flex-wrap: wrap; align-items: flex-start; gap: 10px 12px; min-width: 0; }
.gh-text { flex: 1 1 220px; min-width: 0; }
.gate-eyebrow { display: inline-flex; align-items: center; gap: 6px; color: var(--teal-ink); }
.blocked .gate-eyebrow { color: var(--warn-ink); }
.record .gate-eyebrow { color: var(--ink-3); }
.gh h2 { margin-top: 4px; font-size: 22px; font-weight: 300; letter-spacing: -.01em; overflow-wrap: anywhere; }
.gate-btn { flex-shrink: 0; gap: 8px; }
.gate-label { display: grid; }
.gate-label-size, .gate-label-text { grid-area: 1 / 1; }
.gate-label-size { visibility: hidden; }
.gate-label-size::before { content: attr(data-label); }
.gate-btn[aria-disabled="true"] { cursor: not-allowed; }
.gate-body { display: grid; grid-template-columns: minmax(0, 1fr); min-width: 0; gap: 10px; font-size: 13.5px; color: var(--ink-2); overflow-wrap: anywhere; }
.gate-body:empty { display: none; }
@media (max-width: 720px) {
  .gate-btn { width: 100%; justify-content: center; order: -1; }
}
</style>
