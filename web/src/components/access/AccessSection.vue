<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { brand } from '../../lib/brand'
import { computed, nextTick, onMounted, ref, watch, type Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { can, permissionsRevoked } from '../../lib/authz'
import { safeReturnPath } from '../../lib/signInReturn'
import { useAccess } from '../../stores/access'
import AppIcon from '../AppIcon.vue'
import BizIcon, { type BizIconName } from '../business/BizIcon.vue'
import AgentsTab from './AgentsTab.vue'
import AuditTab from './AuditTab.vue'
import InviteSheet, { type InvitePrefill } from './InviteSheet.vue'
import InvitesTab from './InvitesTab.vue'
import PeopleTab from './PeopleTab.vue'
import PersonSheet from './PersonSheet.vue'
import ProjectsTab from './ProjectsTab.vue'
import RolesTab from './RolesTab.vue'

// Settings -> Access: who works here and what they may do. People, invites,
// roles, project access, agents and the access log, each a tab you see when you
// may (can()); /settings/access/<tab>/<id> opens a person, a role or a project.
type Tab = 'people' | 'invites' | 'roles' | 'projects' | 'agents' | 'audit'
const route = useRoute()
const router = useRouter()
const access = useAccess()
const TABS: { id: Tab; label: string; icon: BizIconName; permission: string; component: Component }[] = [
  { id: 'people', label: 'People', icon: 'users', permission: 'members.read', component: PeopleTab },
  { id: 'invites', label: 'Invites', icon: 'mail', permission: 'members.read', component: InvitesTab },
  { id: 'roles', label: 'Roles', icon: 'shield', permission: 'members.read', component: RolesTab },
  { id: 'projects', label: 'Projects', icon: 'layers', permission: 'members.read', component: ProjectsTab },
  { id: 'agents', label: 'Agents', icon: 'agent', permission: 'members.read', component: AgentsTab },
  { id: 'audit', label: 'Access log', icon: 'history', permission: 'audit.read', component: AuditTab },
]
const liveTabs = computed(() => TABS.filter(tab => can(tab.permission)))
// After a 401 the tabs stay as they were (inert); the note below says why.
const tabs = ref(liveTabs.value)
watch(liveTabs, now => { if (!permissionsRevoked()) tabs.value = now })
const ended = computed(() => permissionsRevoked())
function signIn() { window.open(`/signin?error=expired&return=${encodeURIComponent(safeReturnPath(route.fullPath))}`, '_blank', 'noopener') }
const tab = computed<Tab>(() => { const wanted = route.params.tab as Tab | undefined; return tabs.value.some(t => t.id === wanted) ? wanted! : tabs.value[0]?.id ?? 'people' })
const current = computed(() => TABS.find(t => t.id === tab.value)!)
const detail = computed(() => typeof route.params.id === 'string' ? route.params.id : '')
const count = (id: Tab) => id === 'people' ? access.people.length : id === 'invites' ? access.invites.filter(i => i.status === 'pending').length
  : id === 'roles' ? access.roles.length : id === 'agents' ? access.agents.filter(a => !a.service).length : null
const person = computed(() => tab.value === 'people' && detail.value ? access.person(detail.value) : undefined)
const inviting = ref(false)
const invitePrefill = ref<InvitePrefill | null>(null)
function go(id: Tab) { return router.push(`/settings/access/${id}`) }
// Arrow keys walk the tabs, as a tablist does.
const tabBar = ref<HTMLElement>()
function tabKeys(event: KeyboardEvent) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight' && event.key !== 'Home' && event.key !== 'End') return
  event.preventDefault()
  const ids = tabs.value.map(t => t.id)
  const at = ids.indexOf(tab.value)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? ids.length - 1 : (at + (event.key === 'ArrowRight' ? 1 : -1) + ids.length) % ids.length
  void go(ids[next]!).then(() => nextTick()).then(() => tabBar.value?.querySelector<HTMLElement>('[aria-selected="true"]')?.focus())
}
// The member data needs See members; with only the access log, none is asked for.
const needsMembers = computed(() => can('members.read'))
onMounted(() => { if (needsMembers.value) void access.load() })
watch(needsMembers, yes => { if (yes) void access.load() })
</script>

<template>
  <div class="access-section">
    <section class="settings-card glass-card access-card" aria-labelledby="access-title">
      <header class="card-head">
        <span class="card-icon" aria-hidden="true"><BizIcon name="users" :size="16" /></span>
        <div class="card-titles">
          <h2 id="access-title">People and access</h2>
          <p class="lead">Who works here, with which role and on which projects. {{ brand.short_name }} decides access; an INSPR ID only proves who someone is.</p>
        </div>
      </header>
      <p v-if="ended" class="set-note error ended" role="alert"><AppIcon name="alert" :size="14" /><span>Your session has ended, so nothing here can change. Sign in again in a new tab; what you typed and any link on screen stay here.</span><button type="button" class="btn sm" data-session-keep @click="signIn">Sign in again</button></p>
      <div ref="tabBar" class="tabs" :class="{ drilled: !!detail && (tab === 'roles' || tab === 'projects') }" role="tablist" aria-label="Access" @keydown="tabKeys">
        <RouterLink
          v-for="t in tabs" :id="`access-tab-${t.id}`" :key="t.id" :to="`/settings/access/${t.id}`" class="tab" role="tab" :aria-selected="tab === t.id" :tabindex="tab === t.id ? 0 : -1"
          :aria-controls="`access-panel-${t.id}`"
        >
          <BizIcon :name="t.icon" :size="14" /><span>{{ t.label }}</span><span v-if="count(t.id) !== null && access.state === 'ready'" class="n mono">{{ count(t.id) }}</span>
        </RouterLink>
      </div>
      <div :id="`access-panel-${tab}`" class="panel" role="tabpanel" :aria-labelledby="`access-tab-${tab}`">
        <div v-if="tab !== 'audit' && (access.state === 'loading' || access.state === 'idle')" class="set-skeleton" role="status" aria-label="Loading access"><span class="skeleton" /><span class="skeleton" /><span class="skeleton" /><span class="skeleton" /></div>
        <p v-else-if="tab !== 'audit' && access.state === 'error'" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ access.error || 'Access could not be loaded.' }}<button type="button" class="btn sm" @click="access.load(true)">Try again</button></p>
        <component :is="current.component" v-else v-bind="tab === 'roles' || tab === 'projects' ? { detail } : {}" @invite="(prefill?: InvitePrefill) => { invitePrefill = prefill ?? null; inviting = true }" />
      </div>
    </section>
    <PersonSheet v-if="person" :key="person.principal_id" :person="person" :focus="route.hash === '#projects' ? 'projects' : undefined" @close="router.push('/settings/access/people')" />
    <InviteSheet v-if="inviting" :prefill="invitePrefill" @close="inviting = false" />
  </div>
</template>

<style scoped>
.access-section { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; min-width: 0; }
.access-card { padding: 20px 20px 16px; }
.card-head { display: flex; align-items: flex-start; gap: 12px; }
.card-icon { display: grid; place-items: center; flex-shrink: 0; width: 32px; height: 32px; border-radius: 10px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.card-titles { flex: 1; min-width: 0; }
h2 { font: 600 15px/1.35 var(--font); color: var(--ink); }
.lead { margin-top: 2px; max-width: 72ch; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.tabs { display: flex; gap: 2px; margin: 16px -4px 0; padding: 3px; border-radius: 12px; background: var(--seg-bg); overflow-x: auto; scrollbar-width: none; }
.tabs::-webkit-scrollbar { display: none; }
.tab { display: inline-flex; align-items: center; justify-content: center; gap: 7px; flex: 1 0 auto; height: 32px; padding: 0 12px; border-radius: 9px; color: var(--ink-2); font-size: 13px; font-weight: 600; text-decoration: none; white-space: nowrap; }
.tab svg { color: var(--ink-3); }
@media (hover: hover) { .tab:hover { color: var(--ink); } }
.tab[aria-selected="true"] { background: var(--seg-on); color: var(--ink); box-shadow: var(--shadow-btn); }
.tab[aria-selected="true"] svg { color: var(--teal-ink); }
.tab:focus-visible { box-shadow: var(--focus-ring); }
.n { font-size: 11px; font-weight: 600; color: var(--ink-3); }
.ended { margin-top: 14px; }
.panel { display: grid; grid-template-columns: minmax(0, 1fr); margin-top: 16px; min-width: 0; }
@media (max-width: 600px) {
  .access-card { padding: 16px 12px 12px; }
  /* Phones: all six tabs in view, three to a row. */
  .tabs { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); margin: 14px -2px 0; overflow: visible; }
  .tab { height: 44px; padding: 0 6px; gap: 5px; font-size: 12.5px; }
  .tab .n { display: none; }
  /* A role or project opened on a phone replaces its list; its back link returns to the tabs. */
  .tabs.drilled { display: none; }
}
</style>
