<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { useBusiness, type AreaId } from '../../stores/business'
import { useSession } from '../../stores/session'
import AppIcon, { type BizIconName as IconName } from './BizIcon.vue'

// The frame every Business page shares: eyebrow, title, one summary line, the
// section tabs, and the closed state when the page's plugin is not enabled.
const props = defineProps<{ title: string; area?: AreaId; panelOpen?: boolean; wide?: boolean }>()
const business = useBusiness()
const session = useSession()
const route = useRoute()
// Overview · Customers · Quotes · Hours · Rates. Customers and Quotes always show:
// a closed part says so on its page, and Quotes says what arrives with the editor.
const tabs: { to: string; label: string; icon: IconName; area?: AreaId }[] = [
  { to: '/business', label: 'Overview', icon: 'briefcase' },
  { to: '/business/customers', label: 'Customers', icon: 'building' },
  { to: '/business/quotes', label: 'Quotes', icon: 'document' },
  { to: '/business/hours', label: 'Hours', icon: 'clock', area: 'hours' },
  { to: '/business/rates', label: 'Rates', icon: 'tag', area: 'costs' },
]
const shown = computed(() => tabs.filter(tab => !tab.area || business.open[tab.area]))
const current = (to: string) => to === '/business' ? route.path === '/business' : route.path.startsWith(to)
const loaded = computed(() => business.plugins !== null)
const closed = computed(() => !!props.area && loaded.value && !business.open[props.area])
onMounted(() => { void business.loadPlugins() })
</script>

<template>
  <section class="biz-page" :class="{ 'panel-open': panelOpen, wide }" :aria-labelledby="`${area ?? 'business'}-title`">
    <!-- The head measures its own width, so a page squeezed beside a docked panel
         stacks its actions and folds its tabs the way a phone does. -->
    <div class="head-zone">
    <header class="page-head">
      <div class="head-main">
        <p class="eyebrow"><slot name="eyebrow">{{ session.identity?.tenant.name ?? 'Workspace' }}</slot></p>
        <h1 :id="`${area ?? 'business'}-title`">{{ title }}</h1>
        <p class="summary"><slot name="summary" /></p>
      </div>
      <div v-if="$slots.actions && !closed" class="head-actions"><slot name="actions" /></div>
    </header>
    <nav v-if="loaded && shown.length > 1" class="biz-tabs" aria-label="Business">
      <RouterLink v-for="tab in shown" :key="tab.to" :to="tab.to" class="biz-tab" :aria-current="current(tab.to) ? 'page' : undefined">
        <AppIcon :name="tab.icon" :size="14" /><span>{{ tab.label }}</span>
      </RouterLink>
    </nav>
    </div>

    <div v-if="!loaded && !business.pluginsError" class="gate-skeleton skeleton-body" role="status" aria-label="Loading"><span class="skeleton" /><span class="skeleton short" /></div>
    <div v-else-if="!loaded" class="gate glass-card" role="alert">
      <span class="gate-icon danger"><AppIcon name="alert" :size="18" /></span>
      <h2>Business could not be loaded</h2>
      <p>{{ business.pluginsError }}</p>
      <button type="button" class="btn" @click="business.loadPlugins(true)"><AppIcon name="refresh" :size="14" />Try again</button>
    </div>
    <div v-else-if="closed" class="gate glass-card">
      <span class="gate-icon"><AppIcon name="lock" :size="18" /></span>
      <h2>{{ title }} is not enabled for this workspace</h2>
      <p v-if="business.admin">You can enable it on the Business overview. Nothing changes for your projects until you do.</p>
      <p v-else>A workspace admin can enable it. Until then this page stays closed.</p>
      <RouterLink class="btn" to="/business">{{ business.admin ? 'Open Business setup' : 'Business overview' }}</RouterLink>
    </div>
    <slot v-else />
  </section>
</template>

<style scoped>
.biz-page { width: 100%; max-width: 1600px; margin: 0 auto; padding: 22px 28px 28px; }
.biz-page.wide { max-width: none; }
.page-head { display: flex; align-items: flex-end; justify-content: space-between; gap: 24px; margin-bottom: 16px; }
.page-head h1 { margin-top: 6px; }
.head-main { min-width: 0; }
.summary { margin-top: 6px; min-height: 20px; font-size: 13.5px; color: var(--ink-2); }
@media (max-width: 600px) { .summary { min-height: 40px; } }
.summary :deep(b) { color: var(--ink); font-weight: 600; font-variant-numeric: tabular-nums; }
.head-actions { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
.biz-tabs { display: flex; gap: 4px; margin: 0 0 18px; padding: 3px; width: max-content; max-width: 100%; overflow-x: auto; border-radius: 999px; background: var(--seg-bg); box-shadow: inset 0 1px 2px rgba(32, 60, 61, .08); scrollbar-width: none; }
.biz-tabs::-webkit-scrollbar { display: none; }
.biz-tab { display: inline-flex; align-items: center; gap: 7px; flex-shrink: 0; height: 30px; padding: 0 14px 0 12px; border-radius: 999px; color: var(--ink-2); font-size: 13px; font-weight: 600; text-decoration: none; white-space: nowrap; }
.biz-tab svg { color: var(--ink-3); }
@media (hover: hover) { .biz-tab:hover { color: var(--teal-ink); background: var(--row-hover); } }
.biz-tab:focus-visible { box-shadow: var(--focus-ring); }
.biz-tab[aria-current="page"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: 0 1px 2px rgba(32, 60, 61, .12), inset 0 0 0 1px var(--glass-edge); }
.biz-tab[aria-current="page"] svg { color: var(--teal); }
.gate { display: grid; justify-items: center; gap: 8px; max-width: 560px; margin: 24px auto; padding: 44px 28px; text-align: center; }
.gate h2 { font-size: 17px; }
.gate p { font-size: 13.5px; max-width: 46ch; }
.gate .btn { margin-top: 8px; }
.gate-icon { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 4px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.gate-icon.danger { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.gate-skeleton { display: grid; gap: 12px; padding: 20px 0; }
.gate-skeleton .skeleton { width: 100%; height: 44px; border-radius: 12px; }
.gate-skeleton .short { width: 60%; height: 10px; }
/* Wide screens dock the panel: the page reflows beside it and takes all the
   width the panel leaves, so no gap opens between them. */
@media (min-width: 1100px) {
  .biz-page.panel-open { max-width: none; margin: 0; padding-right: calc(var(--panel-w) + 22px); }
}
.head-zone { container: biz-head / inline-size; }
/* A narrow page (a panel beside it, or a small window): the actions go under the
   title and summary, and the tabs share the row, icon over label. */
@container biz-head (max-width: 700px) {
  .page-head { flex-direction: column; align-items: stretch; gap: 12px; }
  .head-actions { flex-wrap: wrap; }
}
@container biz-head (max-width: 560px) {
  .biz-tabs { display: grid; grid-auto-flow: column; grid-auto-columns: minmax(0, 1fr); gap: 2px; width: auto; overflow: visible; border-radius: 16px; }
  .biz-tab { flex-direction: column; justify-content: center; gap: 3px; height: 50px; padding: 0 1px; border-radius: 13px; font-size: 10px; }
  .biz-tab span { max-width: 100%; overflow: hidden; text-overflow: ellipsis; }
}
@media (max-width: 720px) {
  .biz-page { padding: 14px 12px 20px; }
  .page-head { flex-direction: column; align-items: stretch; gap: 12px; margin-bottom: 12px; padding: 0 4px; }
  .head-actions { flex-wrap: wrap; }
  .biz-tabs { width: auto; margin-bottom: 14px; }
  .biz-tab { height: 36px; }
}
/* Phones: every tab in one row, icon over label; nothing scrolls sideways or is cut. */
@media (max-width: 600px) {
  .biz-tabs { display: grid; grid-auto-flow: column; grid-auto-columns: minmax(0, 1fr); gap: 2px; width: auto; overflow: visible; border-radius: 16px; }
  .biz-tab { flex-direction: column; justify-content: center; gap: 3px; height: 50px; padding: 0 1px; border-radius: 13px; font-size: 10px; }
  .biz-tab span { max-width: 100%; }
}
</style>
