<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch, type Component } from 'vue'
import { useRoute } from 'vue-router'
import '../styles/settings.css'
import { can, permissionsKnown, permissionsRevoked, refreshPermissions } from '../lib/authz'
import AppIcon from '../components/AppIcon.vue'
import BizIcon, { type BizIconName } from '../components/business/BizIcon.vue'
import BusinessSection from '../components/settings/BusinessSection.vue'
import PersonalSection from '../components/settings/PersonalSection.vue'
import DeveloperSection from '../components/settings/DeveloperSection.vue'
import ProjectsSection from '../components/settings/ProjectsSection.vue'
import PortalSection from '../components/settings/PortalSection.vue'
import WorkspaceSection from '../components/settings/WorkspaceSection.vue'
import AccessSection from '../components/access/AccessSection.vue'
import AgentRulesSection from '../components/rules/AgentRulesSection.vue'
import AccountsSection from '../components/settings/AccountsSection.vue'
import PoliciesSection from '../components/settings/PoliciesSection.vue'
import { SETTINGS_SECTIONS, anyOf, sectionOf, visibleSections, type SectionId } from '../lib/settings'
import { useSession } from '../stores/session'
import { doctrineInbox } from '../lib/doctrineInbox'

// Settings: Personal for everyone; Workspace, Business and Projects for admins;
// Access for whoever may see the members (can('members.read')).
// /settings/<section>#<card> deep-links to one card, which is ringed on arrival.
const route = useRoute()
const session = useSession()
const admin = computed(() => can('settings.manage'))
// A session that ends (401) revokes every grant; what is on screen stays as it
// was, inert, so typed input and a join link shown once are not lost.
const liveSections = computed(() => visibleSections(admin.value, permission => can(permission)))
const sections = ref(liveSections.value)
watch(liveSections, now => { if (!permissionsRevoked()) sections.value = now })
const shown = new Set<SectionId>()
const current = computed(() => sectionOf(route.params.section))
const meta = computed(() => SETTINGS_SECTIONS.find(section => section.id === current.value)!)
const granted = computed(() => meta.value.permission ? anyOf(meta.value.permission, permission => can(permission)) : !meta.value.admin || admin.value)
watch(granted, ok => { if (ok) shown.add(current.value) }, { immediate: true })
const allowed = computed(() => granted.value || (permissionsRevoked() && shown.has(current.value)))
// A permission-gated section waits for my permissions before it says no.
const deciding = computed(() => !!meta.value.permission && !permissionsKnown())
// Which sections show depends on my permissions: the layout waits for them, so
// the nav never re-flows under the pointer (usually a few milliseconds).
void refreshPermissions()
const VIEW: Record<SectionId, Component> = { personal: PersonalSection, developer: DeveloperSection, policies: PoliciesSection, 'agent-rules': AgentRulesSection, accounts: AccountsSection, workspace: WorkspaceSection, access: AccessSection, business: BusinessSection, projects: ProjectsSection, portal: PortalSection }
const ICON: Record<SectionId, BizIconName> = { personal: 'user', developer: 'gear', policies: 'shield', 'agent-rules': 'book', accounts: 'gauge', workspace: 'folder', access: 'users', business: 'briefcase', projects: 'layers', portal: 'globe' }

// A deep link scrolls to its card once the section has rendered it.
let arrival: ReturnType<typeof setTimeout> | undefined
watch(() => [current.value, route.hash] as const, async ([, hash]) => {
  if (!hash) return
  for (let tries = 0; tries < 20; tries++) {
    await nextTick()
    const target = document.getElementById(decodeURIComponent(hash.slice(1)))
    if (target) {
      target.scrollIntoView({ block: 'start', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
      const card = target.classList.contains('settings-card') ? target : target.querySelector<HTMLElement>('.settings-card, .setup')
      card?.classList.add('arrived')
      clearTimeout(arrival)
      arrival = setTimeout(() => card?.classList.remove('arrived'), 1800)
      return
    }
    await new Promise(resolve => setTimeout(resolve, 50))
  }
}, { immediate: true })
</script>

<template>
  <section class="settings-page" aria-labelledby="settings-title">
    <header class="page-head">
      <p class="eyebrow">{{ session.identity?.tenant.name ?? 'Workspace' }}</p>
      <h1 id="settings-title">Settings</h1>
      <p class="summary">Preferences, policies and the workspace settings available to you.</p>
    </header>
    <!-- One grid for everyone: with only Personal to show, the nav still holds its column. -->
    <div v-if="!permissionsKnown()" class="layout waiting" role="status" aria-label="Loading settings"><span class="skeleton nav-skeleton" /><span class="skeleton body-skeleton" /></div>
    <div v-else class="layout" :class="{ single: sections.length < 2 }">
      <nav class="section-nav" aria-label="Settings sections">
        <RouterLink v-for="section in sections" :key="section.id" :to="`/settings/${section.id}`" class="section-link" :aria-current="section.id === current ? 'page' : undefined">
          <span class="link-icon" aria-hidden="true"><BizIcon :name="ICON[section.id]" :size="15" /></span>
          <span class="link-text"><span class="link-label">{{ section.label }}</span><span class="link-summary">{{ section.summary }}</span></span>
          <span v-if="section.admin && !section.permission" class="admin-mark" role="img" aria-label="Admins only" data-tip="Only workspace admins see this"><AppIcon name="shield" :size="12" /></span>
          <span v-else-if="section.id === 'agent-rules' && doctrineInbox.pending" class="waiting-dot" role="img" :aria-label="`${doctrineInbox.pending} doctrine ${doctrineInbox.pending === 1 ? 'proposal waits' : 'proposals wait'}`" :data-tip="`${doctrineInbox.pending} doctrine ${doctrineInbox.pending === 1 ? 'proposal waits' : 'proposals wait'} for review`" />
        </RouterLink>
      </nav>
      <div class="body" :class="{ wide: current === 'access' || current === 'agent-rules' }">
        <component :is="VIEW[current]" v-if="allowed" :key="current" />
        <div v-else-if="deciding" class="set-skeleton" role="status" aria-label="Loading"><span class="skeleton" /><span class="skeleton" /></div>
        <div v-else class="gate glass-card">
          <span class="gate-icon"><AppIcon name="shield" :size="18" /></span>
          <h2>{{ meta.deniedTitle ?? (meta.permission ? `${meta.label} is for people who manage the workspace` : `${meta.label} settings are for workspace admins`) }}</h2>
          <p>{{ meta.denied ?? (meta.permission ? 'Seeing who is in the workspace needs the See members permission. An admin can give it to you.' : 'A workspace admin can change these. Your own settings are under Personal.') }}</p>
          <RouterLink class="btn" to="/settings/personal">Personal settings</RouterLink>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
/* One page width for every section, so the section nav never moves when you
   switch sections. Access holds wide tables and uses the whole content
   column; the other sections keep a readable width, aligned to the same edge. */
.settings-page { width: 100%; max-width: 1440px; margin: 0 auto; padding: 22px 28px 40px; }
.body:not(.wide) { max-width: 960px; }
.waiting .skeleton { border-radius: 14px; }
.nav-skeleton { height: 220px; }
.body-skeleton { height: 320px; }
.page-head { margin-bottom: 20px; }
.page-head h1 { margin-top: 6px; }
.summary { margin-top: 6px; font-size: 13.5px; color: var(--ink-2); }
.layout { display: grid; grid-template-columns: 240px minmax(0, 1fr); gap: 28px; align-items: start; }
.section-nav { position: sticky; top: 16px; display: grid; gap: 4px; }
.section-link { display: grid; grid-template-columns: 30px minmax(0, 1fr) auto; align-items: center; gap: 10px; min-height: 52px; padding: 8px 10px; border-radius: 12px; color: var(--ink); text-decoration: none; }
@media (hover: hover) { .section-link:hover { background: var(--row-hover); } }
.section-link:focus-visible { box-shadow: var(--focus-ring); }
/* The current section: a raised card, like the active place. */
.section-link[aria-current="page"] { background: var(--surface-raised); box-shadow: 0 0 0 1px var(--line), 0 6px 18px -12px rgba(32, 60, 61, .4); }
.link-icon { display: grid; place-items: center; width: 30px; height: 30px; border-radius: 9px; background: var(--surface-2); color: var(--ink-2); }
.section-link[aria-current="page"] .link-icon { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.link-text { display: grid; min-width: 0; }
.link-label { font-weight: 600; font-size: 13.5px; }
.link-summary { font-size: 12px; color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.admin-mark { display: grid; place-items: center; width: 22px; height: 22px; border-radius: 50%; color: var(--ink-3); }
/* Doctrine proposals wait (AEON-444): a small neutral dot. */
.waiting-dot { justify-self: center; width: 7px; height: 7px; margin: 0 7px; border-radius: 50%; background: var(--ink-2); }
.body { min-width: 0; }
.gate { display: grid; justify-items: center; gap: 8px; max-width: 560px; margin: 0 auto; padding: 40px 28px; text-align: center; }
.gate h2 { font-size: 17px; }
.gate p { font-size: 13.5px; max-width: 46ch; }
.gate .btn { margin-top: 8px; }
.gate-icon { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 4px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
@media (max-width: 900px) {
  .layout { grid-template-columns: minmax(0, 1fr); gap: 16px; }
  /* Narrow: the sections become a two-by-two grid above the page. */
  .section-nav { position: static; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px; }
  .section-link { grid-template-columns: 28px minmax(0, 1fr) auto; min-height: 48px; padding: 6px 10px; background: var(--glass); box-shadow: 0 0 0 1px var(--line); }
  .link-summary { display: none; }
  /* Narrow, a single section needs no nav above it. */
  .single .section-nav { display: none; }
}
@media (max-width: 600px) {
  .settings-page { padding: 14px 16px 28px; }
  .page-head { margin-bottom: 14px; }
}
</style>
