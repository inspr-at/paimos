<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import mark from '../assets/brand/aeon-mark.svg'

// One calm page for everything that is not work: not found, errors, lost connection.
defineProps<{ eyebrow: string; title: string; tone?: 'calm' | 'problem' }>()
</script>

<template>
  <section class="status-page" :aria-labelledby="'status-title'">
    <div class="status-card" :class="tone ?? 'calm'">
      <span class="mark-halo" aria-hidden="true"><img :src="mark" width="44" height="44" alt="" /></span>
      <p class="eyebrow">{{ eyebrow }}</p>
      <h1 id="status-title">{{ title }}</h1>
      <div class="status-body"><slot /></div>
      <div v-if="$slots.actions" class="status-actions"><slot name="actions" /></div>
      <div v-if="$slots.details" class="status-details"><slot name="details" /></div>
    </div>
  </section>
</template>

<style scoped>
.status-page { display: grid; place-items: center; min-height: 100%; padding: 40px 24px; }
.status-card {
  position: relative; width: min(480px, 100%); padding: 34px 36px 28px; text-align: center; border-radius: 22px; border: 1px solid var(--glass-edge);
  background: linear-gradient(165deg, var(--surface-raised-2), var(--glass) 60%); box-shadow: var(--shadow);
  -webkit-backdrop-filter: blur(20px) saturate(1.15); backdrop-filter: blur(20px) saturate(1.15);
}
.status-card::after { content: ''; position: absolute; left: 12%; right: 12%; top: 0; height: 1px; background: linear-gradient(90deg, transparent, var(--glass-edge), var(--aqua), var(--glass-edge), transparent); opacity: .8; }
.mark-halo { display: inline-grid; place-items: center; width: 72px; height: 72px; margin-bottom: 18px; border-radius: 20px; background: var(--brand-plate); box-shadow: 0 0 0 1px var(--glass-rim), 0 14px 32px -16px color-mix(in srgb, var(--primary-line) 55%, transparent), 0 0 40px -10px color-mix(in srgb, var(--primary-tint) 90%, transparent); }
.problem .mark-halo { box-shadow: 0 0 0 1px var(--danger-line), 0 14px 32px -16px color-mix(in srgb, var(--danger-fill) 45%, transparent), 0 0 40px -12px color-mix(in srgb, var(--gold) 80%, transparent); }
.eyebrow { margin: 0; }
h1 { margin: 10px 0 12px; font-size: clamp(26px, 5vw, 32px); }
.status-body { display: grid; gap: 8px; font-size: 14px; color: var(--ink-2); }
.status-body :deep(p) { color: var(--ink-2); }
.status-actions { display: flex; flex-wrap: wrap; justify-content: center; gap: 10px; margin-top: 24px; }
.status-actions :deep(.btn) { height: 38px; padding: 0 16px; font-size: 13.5px; }
.status-details { margin-top: 20px; padding-top: 14px; border-top: 1px solid var(--line); text-align: left; }
@media (prefers-reduced-motion: no-preference) {
  .status-card { animation: rise .45s cubic-bezier(.2, .7, .2, 1); }
  @keyframes rise { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: none; } }
}
@media (max-width: 480px) { .status-page { padding: 24px 14px; } .status-card { padding: 28px 22px 22px; } .status-actions :deep(.btn) { height: 44px; } }
</style>
