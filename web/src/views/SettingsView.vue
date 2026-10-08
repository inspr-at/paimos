<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch, type Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import '../styles/settings.css'
import { can, permissionsKnown, permissionsRevoked, refreshPermissions } from '../lib/authz'
import AppIcon from '../components/AppIcon.vue'
import BizIcon, { type BizIconName } from '../components/business/BizIcon.vue'
import { SETTINGS_GROUPS, SETTINGS_SECTIONS, anyOf, sectionOf, visibleSections, type SectionId, type SettingsSection } from '../lib/settings'
import { useSession } from '../stores/session'
import { useProfile } from '../stores/profile'
import { textForKinds } from '../lib/workKindsCopy'
import { doctrineInbox } from '../lib/doctrineInbox'
import { scopeOwner } from '../lib/identityScope'
import { settingsFooter, settingsNeeds } from '../lib/footerProviders'
import { useFooterSummary } from '../lib/footerSummary'
import { preferenceSaves, retryFailedPreferences } from '../lib/preferences'

// Load only the selected section, so its bundle cannot hold up navigation.
const BusinessSection = defineAsyncComponent(() => import('../components/settings/BusinessSection.vue'))
const PersonalSection = defineAsyncComponent(() => import('../components/settings/PersonalSection.vue'))
const ThemeSection = defineAsyncComponent(() => import('../components/settings/ThemeSection.vue'))
const DeveloperSection = defineAsyncComponent(() => import('../components/settings/DeveloperSection.vue'))
const VocabularySection = defineAsyncComponent(() => import('../components/settings/VocabularySection.vue'))
const KindsOfWorkSection = defineAsyncComponent(() => import('../components/settings/KindsOfWorkSection.vue'))
const AgentsSection = defineAsyncComponent(() => import('../components/settings/AgentsSection.vue'))
const AutopilotSection = defineAsyncComponent(() => import('../components/settings/AutopilotSection.vue'))
const PortalSection = defineAsyncComponent(() => import('../components/settings/PortalSection.vue'))
const WorkspaceSection = defineAsyncComponent(() => import('../components/settings/WorkspaceSection.vue'))
const AccessSection = defineAsyncComponent(() => import('../components/access/AccessSection.vue'))
const AgentRulesSection = defineAsyncComponent(() => import('../components/rules/AgentRulesSection.vue'))
const AccountsSection = defineAsyncComponent(() => import('../components/settings/AccountsSection.vue'))
const PoliciesSection = defineAsyncComponent(() => import('../components/settings/PoliciesSection.vue'))
const ModelsSection = defineAsyncComponent(() => import('../components/settings/ModelsSection.vue'))

// Settings groups share one frame; explicit grants gate Access, rules and accounts.
// /settings/<section>#<target> deep-links to a card or field, ringed on arrival.
const route = useRoute()
const router = useRouter()
const session = useSession()
const profile = useProfile()
const german = computed(() => route.query.lang === 'de' || document.documentElement.lang.startsWith('de'))
const modelText = (en: string, de: string) => german.value ? de : en
const kindsText = computed(() => textForKinds(profile.profile?.principal_id === session.identity?.principal.id && /^de\b/i.test(profile.profile?.locale ?? '')))
const sectionLabel = (section: SettingsSection) => section.id === 'kinds' ? kindsText.value('title') : section.id === 'models' ? modelText('Models', 'Modelle') : section.label
const sectionSummary = (section: SettingsSection) => section.id === 'kinds' ? kindsText.value('summary') : section.id === 'models' ? modelText('Which model does what, when', 'Welches Modell was wann tut') : section.summary
const admin = computed(() => can('settings.manage'))
// A session that ends (401) revokes every grant; what is on screen stays as it
// was, inert, so typed input and a join link shown once are not lost.
const liveSections = computed(() => visibleSections(admin.value, permission => can(permission)))
const sections = ref(liveSections.value)
watch(liveSections, now => { if (!permissionsRevoked()) sections.value = now })
const shown = reactive(new Set<SectionId>())
// This key owns only the mounted contents, never permission to act. A missing
// session freezes those contents; a different authenticated owner replaces them.
const mountedOwner = ref(scopeOwner(session.identity))
watch(() => scopeOwner(session.identity), now => {
  if (!now || now === mountedOwner.value) return
  mountedOwner.value = now
  shown.clear()
  sections.value = liveSections.value
  closePicker()
}, { flush: 'sync' })
const current = computed(() => sectionOf(route.params.section))

// The footer stays empty while everything is saved and nothing needs you (AEON-785).
// Access says its own line.
useFooterSummary(() => {
  if (!session.identity || current.value === 'access') return null
  return settingsFooter({
    saving: preferenceSaves.saving.size > 0, failed: preferenceSaves.failed.size > 0, needs: settingsNeeds.value,
    act: {
      retry: retryFailedPreferences,
      needs: () => {
        if (current.value !== 'accounts') void router.push('/settings/accounts')
        void nextTick(() => document.querySelector('.needs-block')?.scrollIntoView({ block: 'start', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' }))
      },
    },
  })
})
const meta = computed(() => SETTINGS_SECTIONS.find(section => section.id === current.value)!)
const who = computed(() => current.value === 'models' ? modelText(meta.value.who, 'Die eigene Reihenfolge wird hier festgelegt. Admins setzen die Vorgabe und Regeln des Arbeitsbereichs; Projektverantwortliche setzen Projektregeln.') : meta.value.who)
const granted = computed(() => meta.value.permission ? anyOf(meta.value.permission, permission => can(permission)) : !meta.value.admin || admin.value)
watch([current, granted, mountedOwner], ([section, ok]) => { if (ok) shown.add(section) }, { immediate: true })
const allowed = computed(() => granted.value || (permissionsRevoked() && shown.has(current.value)))
// A permission-gated section waits for my permissions before it says no.
const deciding = computed(() => !!meta.value.permission && !permissionsKnown())
// Which sections show depends on my permissions: the layout waits for them, so
// the nav never re-flows under the pointer (usually a few milliseconds).
void refreshPermissions()
const VIEW: Record<SectionId, Component> = { personal: PersonalSection, theme: ThemeSection, developer: DeveloperSection, policies: PoliciesSection, models: ModelsSection, workspace: WorkspaceSection, vocabulary: VocabularySection, kinds: KindsOfWorkSection, access: AccessSection, agents: AgentsSection, 'agent-rules': AgentRulesSection, accounts: AccountsSection, autopilot: AutopilotSection, business: BusinessSection, portal: PortalSection }
const ICON: Record<SectionId, BizIconName> = { personal: 'user', theme: 'sun', developer: 'gear', policies: 'shield', models: 'columns', workspace: 'building', vocabulary: 'tag', kinds: 'list', access: 'users', agents: 'agent', 'agent-rules': 'book', accounts: 'monitor', autopilot: 'sparkle', business: 'briefcase', portal: 'globe' }
const groups = computed(() => SETTINGS_GROUPS.map(label => ({ label, sections: sections.value.filter(section => section.group === label) })).filter(group => group.sections.length))
const pickerOpen = ref(false)
const picker = ref<HTMLButtonElement>()
const navColumn = ref<HTMLElement>()
function closePicker(focus = false) { pickerOpen.value = false; if (focus) picker.value?.focus({ preventScroll: true }) }
function outside(event: PointerEvent) { if (pickerOpen.value && event.target instanceof Node && !navColumn.value?.contains(event.target)) closePicker() }
function resize() { closePicker() }
function navKeys(event: KeyboardEvent) {
  if (event.key === 'Escape' && pickerOpen.value) { event.preventDefault(); event.stopPropagation(); closePicker(true) }
}
let returnPickerFocus = false
function sectionClick(event: MouseEvent) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return
  returnPickerFocus = pickerOpen.value
  closePicker()
  // RouterLink starts its navigation after this handler. Wait for the route
  // watcher for other sections; selecting the current one restores immediately.
  if ((event.currentTarget as HTMLElement).getAttribute('aria-current') === 'page') {
    returnPickerFocus = false; void nextTick(() => closePicker(true))
  }
}
watch(current, async () => {
  closePicker()
  if (!returnPickerFocus) return
  returnPickerFocus = false
  await nextTick()
  if (!disposed) closePicker(true)
}, { flush: 'post' })
watch(sections, () => closePicker())
onMounted(() => { document.addEventListener('pointerdown', outside); window.addEventListener('resize', resize) })
onBeforeUnmount(() => { document.removeEventListener('pointerdown', outside); window.removeEventListener('resize', resize); clearTimeout(arrival); disposed = true })
let disposed = false

// A deep link scrolls to its target once the section has rendered it.
let arrival: ReturnType<typeof setTimeout> | undefined
watch(() => [current.value, route.hash] as const, async ([section, hash]) => {
  if (!hash) return
  for (let tries = 0; tries < 20; tries++) {
    await nextTick()
    if (disposed || current.value !== section || route.hash !== hash) return
    const target = document.getElementById(decodeURIComponent(hash.slice(1)))
    if (target) {
      target.scrollIntoView({ block: 'start', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
      // Legacy bookmarks can now point to a field inside a consolidated card.
      // Keep wrapper links ringing their card, and ring field links themselves.
      const highlight = target.classList.contains('settings-card') ? target : target.querySelector<HTMLElement>('.settings-card, .setup') ?? target
      highlight.classList.add('arrived')
      clearTimeout(arrival)
      arrival = setTimeout(() => highlight.classList.remove('arrived'), 1800)
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
      <h1 id="settings-title">{{ current === 'models' ? modelText('Settings', 'Einstellungen') : 'Settings' }}</h1>
      <p class="summary">{{ current === 'models' ? modelText('Preferences, policies and the workspace settings available to you.', 'Einstellungen, Richtlinien und die verfügbaren Arbeitsbereich-Einstellungen.') : 'Preferences, policies and the workspace settings available to you.' }}</p>
    </header>
    <!-- One grid for everyone: with only Personal to show, the nav still holds its column. -->
    <div v-if="!permissionsKnown()" class="layout waiting" role="status" aria-label="Loading settings"><span class="skeleton nav-skeleton" /><span class="skeleton body-skeleton" /></div>
    <div v-else class="layout" :class="{ 'nav-open': pickerOpen }">
      <div ref="navColumn" class="nav-col" @keydown="navKeys">
        <button ref="picker" type="button" class="nav-picker" :aria-expanded="pickerOpen" aria-controls="settings-section-nav" :aria-label="`Section: ${sectionLabel(meta)}. Choose another section`" @click="pickerOpen = !pickerOpen">
          <span class="link-icon" aria-hidden="true"><BizIcon :name="ICON[current]" :size="15" /></span>
          <span class="link-text"><span class="link-label">{{ sectionLabel(meta) }}</span><span class="link-summary">{{ meta.group }} · {{ sections.length }} sections</span></span>
          <AppIcon name="chevron" :size="14" />
        </button>
        <nav id="settings-section-nav" class="section-nav" aria-label="Settings sections">
          <template v-for="group in groups" :key="group.label">
            <p class="nav-group">{{ group.label }}</p>
            <RouterLink v-for="section in group.sections" :key="section.id" :to="`/settings/${section.id}`" class="section-link" :aria-current="section.id === current ? 'page' : undefined" @click="sectionClick">
              <span class="link-icon" aria-hidden="true"><BizIcon :name="ICON[section.id]" :size="15" /></span>
              <span class="link-text"><span class="link-label">{{ sectionLabel(section) }}</span><span v-clip-tip="sectionSummary(section)" class="link-summary">{{ sectionSummary(section) }}</span></span>
              <span v-if="section.admin && !section.permission" class="admin-mark" role="img" aria-label="Admins only" data-tip="Only workspace admins see this"><AppIcon name="shield" :size="12" /></span>
              <span v-else-if="section.fresh" class="new-mark">{{ modelText('New', 'Neu') }}</span>
              <span v-else-if="section.id === 'agent-rules' && doctrineInbox.pending" class="waiting-dot" role="img" :aria-label="`${doctrineInbox.pending} doctrine ${doctrineInbox.pending === 1 ? 'proposal waits' : 'proposals wait'}`" :data-tip="`${doctrineInbox.pending} doctrine proposals wait for review`" />
            </RouterLink>
          </template>
        </nav>
      </div>
      <div class="body" :class="{ wide: current === 'access' || current === 'agent-rules' || current === 'models' }">
        <p v-if="allowed && current !== 'kinds'" class="who"><AppIcon :name="meta.admin && !meta.permission ? 'shield' : 'eye'" :size="14" /><span>{{ who }}</span></p>
        <nav v-if="allowed && current === 'theme'" class="theme-links" aria-label="Theme cards"><RouterLink to="/settings/theme#themes">Themes</RouterLink><RouterLink to="/settings/theme#colours">Colours</RouterLink><RouterLink to="/settings/theme#agents">Agents</RouterLink></nav>
        <component :is="VIEW[current]" v-if="allowed" :key="`${mountedOwner}/${current}`" />
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
.settings-page { width: 100%; max-width: 1440px; margin: 0 auto; padding: 22px 28px 40px; container: settings-page / inline-size; }
.body:not(.wide) { max-width: 960px; }
.waiting .skeleton { border-radius: 14px; }
.nav-skeleton { height: 220px; }
.body-skeleton { height: 320px; }
.page-head { margin-bottom: 20px; }
.page-head h1 { margin-top: 6px; }
.summary { margin-top: 6px; font-size: 13.5px; color: var(--ink-2); }
.layout { position: relative; display: grid; grid-template-columns: 248px minmax(0, 1fr); gap: 28px; align-items: start; }
/* The list is taller than the shell once Kinds and Models are both shown.
   Cap it to the space under the page head so a lower section scrolls inside
   the nav. main would otherwise scroll, and a sticky list taller than main
   cannot stay put. 130px is the settings padding, the page head and its margin. */
.section-nav { position: sticky; top: 16px; display: grid; gap: 1px; max-height: calc(100dvh - var(--header-h) - var(--footer-h) - 130px); overflow-y: auto; overscroll-behavior: contain; }
.theme-links { display: flex; flex-wrap: wrap; gap: 8px 16px; padding: 8px 10px; font-size: 12px; }.theme-links a { color: var(--teal-ink); min-height: 28px; display: inline-flex; align-items: center; }
@media (pointer: coarse) { .theme-links a { min-height: 44px; } }
.section-link { display: grid; grid-template-columns: 30px minmax(0, 1fr) auto; align-items: center; gap: 10px; height: 46px; padding: 6px 10px; border-radius: 12px; color: var(--ink); text-decoration: none; }
@media (hover: hover) { .section-link:hover { background: var(--row-hover); } }
.section-link:focus-visible { box-shadow: var(--focus-ring); }
/* The current section: a raised card, like the active place. */
.section-link[aria-current="page"] { background: var(--surface-raised); box-shadow: 0 0 0 1px var(--line), 0 6px 18px -12px color-mix(in srgb, var(--shadow-color) 40%, transparent); }
.link-icon { display: grid; place-items: center; width: 30px; height: 30px; border-radius: 9px; background: var(--surface-2); color: var(--ink-2); }
.section-link[aria-current="page"] .link-icon { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.link-text { display: grid; min-width: 0; }
.link-label { font-weight: 600; font-size: 13.5px; }
.link-summary { font-size: 12px; color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.new-mark { font-size: 10px; font-weight: 600; color: var(--teal-ink); }
.admin-mark { display: grid; place-items: center; width: 22px; height: 22px; border-radius: 50%; color: var(--ink-3); }
/* Doctrine proposals wait (AEON-444): a small neutral dot. */
.waiting-dot { justify-self: center; width: 7px; height: 7px; margin: 0 7px; border-radius: 50%; background: var(--ink-2); }
.nav-col { align-self: stretch; min-width: 0; position: relative; }
.nav-group { margin: 8px 10px 2px; font: 500 10px/1.4 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.nav-group:first-child { margin-top: 0; }
.nav-picker { display: none; }
.body { min-width: 0; display: grid; gap: 14px; container: body / inline-size; }
.who { display: flex; align-items: flex-start; gap: 8px; padding: 2px 4px 0; font-size: 13px; color: var(--ink-2); }
.who svg { flex: none; margin-top: 2px; color: var(--ink-3); }
.gate { display: grid; justify-items: center; gap: 8px; max-width: 560px; margin: 0 auto; padding: 40px 28px; text-align: center; }
.gate h2 { font-size: 17px; }
.gate p { font-size: 13.5px; max-width: 46ch; }
.gate .btn { margin-top: 8px; }
.gate-icon { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 4px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
@container settings-page (max-width: 720px) {
  .layout { grid-template-columns: minmax(0, 1fr); gap: 14px; }
  .nav-picker { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 50px; padding: 8px 12px; border: 1px solid var(--glass-edge); border-radius: 12px; background: var(--glass); box-shadow: var(--shadow-btn); text-align: left; color: var(--ink); }
  .nav-picker .link-text { flex: 1; }
  .section-nav { display: none; position: absolute; z-index: 30; top: calc(100% + 8px); left: 0; right: 0; padding: 8px; max-height: min(70dvh, 640px); overflow-y: auto; overscroll-behavior: contain; border-radius: 14px; background: var(--surface-raised); box-shadow: var(--shadow-pop); }
  .nav-open .section-nav { display: grid; }
}
@media (max-width: 600px) {
  .settings-page { padding: 14px 16px 28px; }
  .page-head { margin-bottom: 14px; }
}
</style>
