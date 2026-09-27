<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { LAST_OWNER_REASON, identityLine, isLastOwner, matchesPerson, projectSummary, type Person } from '../../lib/access'
import { can, myPermissions } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { absoluteTime, relativeTime } from '../../lib/work'
import { useAccess } from '../../stores/access'
import { useSession } from '../../stores/session'
import type { RowAction, RowMenuAnchor } from '../../lib/rowActions'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'
import RowMenu from '../business/RowMenu.vue'
import ChoicePicker from '../settings/ChoicePicker.vue'
import RolePicker from './RolePicker.vue'
import StatusChip from './StatusChip.vue'
import { problem, deactivatePoints } from './accessText'

// Everyone who works here, one row per person: imported classic identities that
// are linked show as "also known as" under their person. Unlinked ones wait in a
// folded group with a Link to person action. Status reads apart from the
// actions; actions I may not take are left out or say why they are off.
const emit = defineEmits<{ invite: [] }>()
const access = useAccess()
const session = useSession()
const router = useRouter()
const term = ref('')
const status = ref<'all' | 'active' | 'deactivated'>('all')
const importedOpen = ref(false)
const manage = computed(() => can('members.manage'))
const me = computed(() => session.identity?.principal.id ?? '')
const counts = computed(() => ({ all: access.people.length, active: access.people.filter(p => p.status === 'active').length, deactivated: access.people.filter(p => p.status === 'deactivated').length }))
const shown = computed(() => access.people
  .filter(p => status.value === 'all' || p.status === status.value)
  .filter(p => matchesPerson(p, term.value))
  .sort((a, b) => Number(a.status === 'deactivated') - Number(b.status === 'deactivated') || a.name.localeCompare(b.name)))
const lastOwner = (p: Person) => isLastOwner(p)

// ---------- Role ----------
const picker = ref<{ person: Person; anchor: HTMLElement } | null>(null)
const busy = ref(false)
// Why the server refused a role change, shown in the open picker.
const roleError = ref('')
watch(picker, () => { roleError.value = '' })
function openRole(person: Person, anchor: HTMLElement) { picker.value = picker.value?.person.principal_id === person.principal_id ? null : { person, anchor } }
async function chooseRole(roleId: string | null) {
  const target = picker.value
  if (!target) return
  const before = target.person.workspace_role?.id ?? null
  const role = access.roles.find(r => r.id === roleId)
  // Taking permissions away from myself asks first.
  if (target.person.principal_id === me.value && role && [...myPermissions()].some(key => !role.permissions.includes(key))) {
    const ok = await confirmAction({ title: `Change your own role to ${role.name}?`, points: ['You lose permissions you have now; some of these pages may close for you.', 'Only someone who still holds them can give them back.'], confirmLabel: `Become ${role.name}`, danger: true })
    if (!ok) return
  }
  busy.value = true
  roleError.value = ''
  try {
    await access.setWorkspaceRole(target.person.principal_id, roleId)
    picker.value = null
    const name = target.person.name
    toast(roleId ? `${name} is now ${role?.name ?? 'changed'}` : `${name} has no workspace role now`, { action: { label: 'Undo', run: () => void access.setWorkspaceRole(target.person.principal_id, before).then(() => toast(`${name} has their role back`), e => toast(problem(e, 'The role could not be put back'), { tone: 'error' })) } })
  } catch (e) { roleError.value = problem(e, `${target.person.name} keeps their role`) }
  finally { busy.value = false }
}

// ---------- Row menu ----------
const menu = ref<{ person: Person; anchor: RowMenuAnchor } | null>(null)
const actions = computed<RowAction[]>(() => {
  const p = menu.value?.person
  if (!p) return []
  const out: RowAction[] = [{ id: 'open', label: 'Open', icon: 'arrow', group: 1, keys: 'Enter' }]
  if (manage.value) {
    out.push({ id: 'projects', label: 'Project access…', icon: 'layers', group: 1 })
    if (p.status === 'active') out.push({ id: 'deactivate', label: 'Deactivate…', icon: 'stop', group: 2, danger: true, reason: lastOwner(p) ? LAST_OWNER_REASON : undefined })
    else out.push({ id: 'reactivate', label: 'Reactivate', icon: 'refresh', group: 2 })
  }
  return out
})
function openMenu(person: Person, anchor: RowMenuAnchor) { menu.value = menu.value?.person.principal_id === person.principal_id && anchor instanceof HTMLElement ? null : { person, anchor } }
function openPerson(person: Person, hash = '') { void router.push(`/settings/access/people/${person.principal_id}${hash}`) }
async function act(id: string) {
  const person = menu.value?.person
  menu.value = null
  if (!person) return
  if (id === 'open') openPerson(person)
  else if (id === 'projects') openPerson(person, '#projects')
  else if (id === 'deactivate') await deactivate(person)
  else if (id === 'reactivate') await reactivate(person)
}
async function deactivate(person: Person) {
  const ok = await confirmAction({ title: `Deactivate ${person.name}?`, points: deactivatePoints(person.name), confirmLabel: `Deactivate ${person.name.split(' ')[0]}`, danger: true })
  if (!ok) return
  try { await access.deactivate(person.principal_id); toast(`${person.name} is deactivated`, { action: { label: 'Reactivate', run: () => void reactivate(person) } }) }
  catch (e) { toast(problem(e, `${person.name} stays active`), { tone: 'error' }) }
}
async function reactivate(person: Person) {
  try { await access.reactivate(person.principal_id); toast(`${person.name} can sign in again`) }
  catch (e) { toast(problem(e, `${person.name} stays deactivated`), { tone: 'error' }) }
}
function rowClick(event: MouseEvent, person: Person) {
  if ((event.target as HTMLElement).closest('button, a')) return
  openPerson(person)
}
function contextMenu(event: MouseEvent, person: Person) { event.preventDefault(); openMenu(person, { x: event.clientX, y: event.clientY }) }

// ---------- Imported classic identities ----------
const linking = ref<{ id: string; name: string; anchor: HTMLElement } | null>(null)
const linkChoices = computed(() => access.people.filter(p => p.status === 'active').map(p => ({ value: p.principal_id, label: p.name, detail: identityLine(p) })))
async function link(personId: string) {
  const from = linking.value
  linking.value = null
  if (!from) return
  const person = access.person(personId)
  try {
    await access.linkAlias(personId, from.id)
    toast(`${from.name} is now ${person?.name ?? 'linked'}`, { action: { label: 'Undo', run: () => void access.unlinkAlias(personId, from.id).then(() => toast(`${from.name} is unlinked again`), e => toast(problem(e, 'The link stays'), { tone: 'error' })) } })
  } catch (e) { toast(problem(e, `${from.name} was not linked`), { tone: 'error' }) }
}
</script>

<template>
  <div class="people-tab">
    <div class="list-tools">
      <label class="search-field find">
        <AppIcon name="search" :size="14" />
        <input v-model="term" class="field" type="search" placeholder="Find a person" aria-label="Find a person" autocomplete="off" spellcheck="false" />
      </label>
      <div class="seg" role="radiogroup" aria-label="Status">
        <button v-for="option in (['all', 'active', 'deactivated'] as const)" :key="option" type="button" role="radio" :aria-checked="status === option" @click="status = option">
          {{ option === 'all' ? 'All' : option === 'active' ? 'Active' : 'Deactivated' }}<span class="n mono">{{ counts[option] }}</span>
        </button>
      </div>
      <span class="spacer" />
      <button v-if="manage" type="button" class="btn primary sm" @click="emit('invite')"><AppIcon name="plus" :size="13" />Invite people</button>
    </div>

    <table class="people" aria-label="People">
      <thead>
        <tr><th scope="col">Person</th><th scope="col" class="c-id">Email or INSPR ID</th><th scope="col">Workspace role</th><th scope="col" class="c-proj">Projects</th><th scope="col" class="c-last">Last active</th><th scope="col">Status</th><th scope="col"><span class="sr-only">Actions</span></th></tr>
      </thead>
      <tbody>
        <tr v-for="p in shown" :key="p.principal_id" class="person" :class="{ off: p.status === 'deactivated' }" :data-person="p.principal_id" @click="rowClick($event, p)" @contextmenu="contextMenu($event, p)">
          <td class="c-person">
            <span class="who">
              <Avatar :id="p.principal_id" :name="p.name" :size="30" />
              <span class="names">
                <RouterLink class="name" :to="`/settings/access/people/${p.principal_id}`">{{ p.name }}<span v-if="p.principal_id === me" class="you">you</span></RouterLink>
                <span v-if="p.aliases.length" class="aka">also known as {{ p.aliases.map(a => a.name).join(', ') }}</span>
                <span class="id-inline">{{ identityLine(p) }}</span>
              </span>
            </span>
          </td>
          <td class="c-id"><span class="ident">{{ p.email ?? '' }}</span><span v-if="p.identity === 'inspr_id'" class="inspr" data-tip="Signs in with an INSPR ID"><AppIcon name="shield" :size="11" />INSPR ID</span></td>
          <td class="c-role">
            <button
              v-if="manage && p.status === 'active'" type="button" class="role-btn" :aria-haspopup="'dialog'" :aria-expanded="picker?.person.principal_id === p.principal_id"
              :aria-label="`Workspace role of ${p.name}: ${p.workspace_role?.name ?? 'none'}. Change`" @click="openRole(p, $event.currentTarget as HTMLElement)"
            >
              <span :class="{ none: !p.workspace_role }">{{ p.workspace_role?.name ?? 'Projects only' }}</span>
              <AppIcon v-if="lastOwner(p)" name="shield" :size="12" class="lock" />
              <AppIcon name="chevron" :size="12" class="chev" />
            </button>
            <span v-else class="role-text" :class="{ none: !p.workspace_role }">{{ p.workspace_role?.name ?? 'Projects only' }}</span>
          </td>
          <td class="c-proj"><span v-if="p.project_roles.length" class="proj" :data-tip="p.project_roles.map(r => `${r.project_title}: ${r.role.name}`).join('\n')">{{ projectSummary(p.project_roles) }}</span><span v-else class="muted">{{ p.workspace_role ? 'Through the workspace' : 'None' }}</span></td>
          <td class="c-last"><time v-if="p.last_active_at" :datetime="p.last_active_at" :data-tip="absoluteTime(p.last_active_at)">{{ relativeTime(p.last_active_at) }}</time><span v-else class="muted">Never</span></td>
          <td class="c-status"><StatusChip :tone="p.status === 'active' ? 'ok' : 'off'" :label="p.status === 'active' ? 'Active' : 'Deactivated'" /></td>
          <td class="c-act"><button type="button" class="icon-btn sm flat more" :aria-label="`Actions for ${p.name}`" aria-haspopup="menu" :aria-expanded="menu?.person.principal_id === p.principal_id" @click="openMenu(p, $event.currentTarget as HTMLElement)"><AppIcon name="more" :size="15" /></button></td>
        </tr>
      </tbody>
    </table>
    <p v-if="!shown.length" class="empty">{{ term ? `No one matches “${term}”.` : status === 'deactivated' ? 'No one is deactivated.' : 'No people yet.' }}</p>

    <section v-if="access.imported.length" class="imported" aria-labelledby="imported-title">
      <h3 id="imported-title">
        <button type="button" class="fold" :aria-expanded="importedOpen" aria-controls="imported-rows" @click="importedOpen = !importedOpen">
          <AppIcon name="chevron" :size="12" class="chev" :class="{ open: importedOpen }" />Imported from classic, no sign-in<span class="n mono">{{ access.imported.length }}</span>
        </button>
      </h3>
      <p v-if="importedOpen" class="imported-lead">Identities that came over from classic Paimos and never signed in here. Link one to the person it belongs to, and its history shows under that person.</p>
      <ul v-if="importedOpen" id="imported-rows" class="imported-rows">
        <li v-for="i in access.imported" :key="i.principal_id">
          <Avatar :id="i.principal_id" :name="i.name" :size="26" />
          <span class="i-name mono">{{ i.name }}</span>
          <span class="i-role">{{ i.classic_role ? `classic ${i.classic_role.replace('_', ' ')}` : '' }}</span>
          <button v-if="manage" type="button" class="btn sm" :aria-label="`Link ${i.name} to a person`" @click="linking = { id: i.principal_id, name: i.name, anchor: $event.currentTarget as HTMLElement }"><AppIcon name="link" :size="13" />Link to person</button>
        </li>
      </ul>
    </section>

    <RolePicker
      v-if="picker" :anchor="picker.anchor" :subject="picker.person.name" :roles="access.roles" :current="picker.person.workspace_role?.id ?? null" :registry="access.registry"
      :mine="myPermissions()" scope="workspace" allow-none none-label="Projects only" :locked="lastOwner(picker.person)" :busy="busy" :can-apply="can('members.manage')" :error="roleError" @choose="chooseRole" @close="picker = null"
    />
    <RowMenu v-if="menu" :anchor="menu.anchor" :items="actions" :label="`Actions for ${menu.person.name}`" @select="act" @close="menu = null" />
    <ChoicePicker v-if="linking" :anchor="linking.anchor" :label="`Link ${linking.name} to`" :choices="linkChoices" current="" placeholder="Find a person…" @choose="link" @close="linking = null" />
  </div>
</template>

<style scoped>
.people-tab { display: grid; grid-template-columns: minmax(0, 1fr); gap: 12px; min-width: 0; }
.list-tools { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; }
.find { width: 260px; }
.spacer { flex: 1; }
.n { margin-left: 6px; font-size: 11px; color: var(--ink-3); }
.people { width: 100%; border-collapse: collapse; font-size: 13px; }
.people thead th { height: 32px; padding: 0 10px; text-align: left; border-bottom: 1px solid var(--line); font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; white-space: nowrap; }
.people td { padding: 8px 10px; border-bottom: 1px solid var(--line); vertical-align: middle; }
.person { cursor: pointer; }
@media (hover: hover) { .person:hover td { background: var(--row-hover); } }
/* Deactivated: quieter words and a grey face, never a lower-contrast fade. */
.person.off .name, .person.off .role-text, .person.off .proj { color: var(--ink-2); font-weight: 500; }
.person.off :deep(.avatar) { filter: grayscale(1); }
.who { display: flex; align-items: center; gap: 10px; min-width: 0; }
.names { display: grid; min-width: 0; }
.name { display: inline-flex; align-items: center; gap: 6px; color: var(--ink); font-weight: 600; font-size: 13.5px; text-decoration: none; }
.name:hover { color: var(--teal-ink); }
.name:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
.you { height: 16px; padding: 0 6px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 10px/16px var(--mono); letter-spacing: .06em; text-transform: uppercase; }
.aka { font-size: 12px; color: var(--ink-2); }
.id-inline { display: none; }
.ident { display: block; color: var(--ink-2); overflow-wrap: anywhere; }
.inspr { display: inline-flex; align-items: center; gap: 4px; margin-top: 2px; font-size: 11.5px; color: var(--ink-3); }
.role-btn { display: inline-flex; align-items: center; gap: 6px; height: 28px; margin-left: -8px; padding: 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13px; font-weight: 600; white-space: nowrap; }
@media (hover: hover) { .role-btn:hover { background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); } }
.role-btn[aria-expanded="true"] { background: var(--row-selected); }
.role-btn:focus-visible { box-shadow: var(--focus-ring); }
.role-text { font-weight: 600; white-space: nowrap; }
.none { color: var(--ink-2); font-weight: 500; }
.lock { color: var(--teal-ink); }
.chev { color: var(--ink-3); }
.proj { color: var(--ink); }
.muted { color: var(--ink-3); }
.c-last { white-space: nowrap; color: var(--ink-2); }
.c-act { width: 40px; text-align: right; }
.more { color: var(--ink-3); }
.empty { padding: 18px 10px; font-size: 13px; color: var(--ink-3); }
.imported { margin-top: 6px; }
.imported h3 { margin: 0; font: inherit; }
.fold { display: inline-flex; align-items: center; gap: 8px; height: 34px; margin-left: -8px; padding: 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); font-size: 13px; font-weight: 600; }
@media (hover: hover) { .fold:hover { background: var(--row-hover); color: var(--ink); } }
.fold:focus-visible { box-shadow: var(--focus-ring); }
.fold .chev { transform: rotate(-90deg); }
.fold .chev.open { transform: none; }
@media (prefers-reduced-motion: no-preference) { .fold .chev { transition: transform .15s ease; } }
.imported-lead { max-width: 70ch; margin: 2px 0 8px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.imported-rows { display: grid; margin: 0; padding: 0; list-style: none; }
.imported-rows li { display: grid; grid-template-columns: 26px minmax(0, 1fr) auto auto; align-items: center; gap: 10px; min-height: 46px; border-top: 1px solid var(--line); }
.i-name { font-size: 12.5px; color: var(--ink); }
.i-role { font-size: 12px; color: var(--ink-3); }
@media (max-width: 1100px) { .c-last, .people thead th.c-last { display: none; } }
@media (max-width: 900px) { .c-id, .people thead th.c-id, .c-proj, .people thead th.c-proj { display: none; } .id-inline { display: block; font-size: 12px; color: var(--ink-2); overflow-wrap: anywhere; } }
@media (max-width: 600px) {
  .find { width: 100%; }
  .find .field { height: 44px; font-size: 16px; }
  .seg { width: 100%; display: grid; grid-template-columns: repeat(3, 1fr); }
  .seg button { height: 40px; }
  .list-tools .btn.primary { width: 100%; height: 44px; }
  /* Phones: each person is a small card: who, then role and status on a line. */
  .people thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
  .people, .people tbody { display: block; }
  .person { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; grid-template-areas: "who who act" "role status status"; align-items: center; gap: 0 8px; padding: 8px 0 6px; border-bottom: 1px solid var(--line); }
  .people td { display: block; padding: 0; border: 0; }
  .people td.c-id, .people td.c-proj, .people td.c-last { display: none; }
  .c-person { grid-area: who; }
  .c-role { grid-area: role; padding-left: 40px !important; }
  .c-status { grid-area: status; justify-self: end; }
  .c-act { grid-area: act; width: auto; }
  .more { width: 44px; height: 44px; }
  .role-btn { height: 44px; }
  .imported-rows li { grid-template-columns: 26px minmax(0, 1fr) auto; }
  .i-role { display: none; }
  .imported-rows .btn { height: 44px; }
}
</style>
