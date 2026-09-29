<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { brand } from '../../lib/brand'
import { computed, nextTick, onMounted, ref } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRouter, type RouteLocationNormalized } from 'vue-router'
import { beyond, diff, groupPermissions, permissionLabel, type Permission, type Role } from '../../lib/access'
import { can, myPermissions, grantablePermissions, permissionsRevoked } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { useSession } from '../../stores/session'
import { toast } from '../../lib/toast'
import { useAccess } from '../../stores/access'
import AppIcon from '../AppIcon.vue'
import DeleteRoleSheet from './DeleteRoleSheet.vue'
import RiskBadge from './RiskBadge.vue'
import { fieldOf, problem } from './accessText'

// One role. Built-in roles read only, with Duplicate to customize. Custom roles
// are composed from the permission registry, grouped as the registry groups
// them, with each permission's risk, a search, and what differs from the role
// they are based on. Permissions I do not hold stay off, with the reason: no one
// can give more than they have.
const props = defineProps<{ role: Role | null; from: Role | null }>()
const access = useAccess()
const router = useRouter()
const manage = computed(() => can('roles.manage'))
// After a 401 the editor keeps what was typed, inert: fields disabled, no saving (the leave guard still protects it).
const readOnly = computed(() => !!props.role?.builtin || (!manage.value && !permissionsRevoked()))
const frozen = computed(() => readOnly.value || permissionsRevoked())
const base = computed<Role | null>(() => props.role ? (props.role.based_on ? access.roleById.get(props.role.based_on) ?? null : null) : props.from)
const initial = computed(() => props.role?.permissions ?? props.from?.permissions ?? [])
const name = ref(props.role?.name ?? (props.from ? `${props.from.name} copy` : ''))
const description = ref(props.role?.description ?? (props.from ? props.from.description : ''))
// A duplicate starts from what I may give: permissions of the source that I do
// not hold are left out (and said so), never sent.
const leftOut = computed(() => props.role ? [] : beyond(props.from?.permissions ?? [], grantablePermissions(myPermissions())))
const picked = ref(new Set<string>(props.role ? initial.value : initial.value.filter(k => !leftOut.value.includes(k))))
const term = ref('')
type View = 'all' | 'selected' | 'high' | 'changes'
const view = ref<View>('all')
const errors = ref<{ name?: string; permissions?: string; form?: string }>({})
const saving = ref(false)
const deleting = ref(false)
const mine = computed(() => grantablePermissions(myPermissions()))
const selected = computed(() => access.registry.filter(p => picked.value.has(p.key)).map(p => p.key))
const vsBase = computed(() => base.value ? diff(base.value.permissions, selected.value) : { added: [], removed: [] })
const unsaved = computed(() => {
  if (!props.role) return true
  const d = diff(initial.value, selected.value)
  return name.value.trim() !== props.role.name || description.value.trim() !== props.role.description || d.added.length + d.removed.length > 0
})
const changes = computed(() => { const d = diff(initial.value, selected.value); return d.added.length + d.removed.length })
const matches = (p: Permission) => { const needle = term.value.trim().toLowerCase(); return !needle || `${p.key} ${p.description} ${permissionLabel(p.key)} ${p.group}`.toLowerCase().includes(needle) }
const inView = (p: Permission) => view.value === 'all' || (view.value === 'selected' && picked.value.has(p.key)) || (view.value === 'high' && p.risk === 'high')
  || (view.value === 'changes' && (vsBase.value.added.includes(p.key) || vsBase.value.removed.includes(p.key)))
const groups = computed(() => groupPermissions(access.registry).map(g => ({ ...g, shown: g.items.filter(p => matches(p) && inView(p)), on: g.items.filter(p => picked.value.has(p.key)).length })).filter(g => g.shown.length))
const counts = computed(() => ({ all: access.registry.length, selected: picked.value.size, high: access.registry.filter(p => p.risk === 'high').length, changes: vsBase.value.added.length + vsBase.value.removed.length }))
// Adding what I do not hold is refused; taking it away is fine.
const blocked = (p: Permission) => !picked.value.has(p.key) && !mine.value.has(p.key)
const highOn = computed(() => access.registry.filter(p => p.risk === 'high' && picked.value.has(p.key)).length)
function toggle(p: Permission) {
  if (frozen.value || blocked(p)) return
  const next = new Set(picked.value)
  if (next.has(p.key)) next.delete(p.key); else next.add(p.key)
  picked.value = next
  errors.value.permissions = ''
}
function toggleGroup(items: Permission[]) {
  if (frozen.value) return
  const allowed = items.filter(p => !blocked(p))
  const on = allowed.every(p => picked.value.has(p.key))
  const next = new Set(picked.value)
  for (const p of allowed) { if (on) next.delete(p.key); else next.add(p.key) }
  picked.value = next
}
function revert() { name.value = props.role?.name ?? ''; description.value = props.role?.description ?? ''; picked.value = new Set(initial.value); errors.value = {} }
async function save() {
  if (saving.value) return
  errors.value = {}
  if (!name.value.trim()) { errors.value.name = 'A role needs a name.'; void nextTick(() => document.getElementById('role-name')?.focus()); return }
  // The server requires me to hold every permission in the role I save.
  const escalating = beyond(selected.value, mine.value)
  if (escalating.length) { errors.value.permissions = `You can only save a role made of permissions you hold. Untick ${escalating.map(permissionLabel).join(', ')}, or ask someone who holds ${escalating.length === 1 ? 'it' : 'them'}.`; return }
  saving.value = true
  try {
    if (props.role) {
      await access.updateRole(props.role.id, { name: name.value.trim(), description: description.value.trim(), permissions: selected.value })
      toast(`${name.value.trim()} is saved`)
    } else {
      const created = await access.createRole({ name: name.value.trim(), description: description.value.trim(), permissions: selected.value, based_on: props.from?.id ?? null })
      leaving = true
      await router.replace(`/settings/access/roles/${created.id}`)
      toast(`The role ${created.name} is ready. Give it to people under People or Projects.`)
    }
  } catch (e) {
    const field = fieldOf(e)
    const text = problem(e, 'The role was not saved').replace(/^The role was not saved: /, '')
    if (field === 'name') errors.value.name = text
    else if (field === 'permissions') errors.value.permissions = text
    else errors.value.form = text
  } finally { saving.value = false }
}
function afterDelete() { leaving = true; deleting.value = false; void router.push('/settings/access/roles') }
function duplicate() { if (props.role) void router.push(`/settings/access/roles/new?from=${props.role.id}`) }
let leaving = false
// Leaving with unsaved changes asks first (another tab, role or page).
async function guard(to: RouteLocationNormalized, from: RouteLocationNormalized) {
  if (to.path === '/signin' && useSession().requiresSignIn) return true
  if (to.path === from.path || leaving || readOnly.value || !unsaved.value || (!props.role && !name.value.trim() && !picked.value.size)) return true
  return confirmAction({ title: props.role ? `Leave ${props.role.name} without saving?` : 'Leave the new role without saving?', points: [changes.value ? `${changes.value} permission change${changes.value === 1 ? '' : 's'} to this role will be lost.` : 'What you entered for this role will be lost.'], confirmLabel: 'Leave without saving', cancelLabel: 'Keep editing', danger: true })
}
onBeforeRouteLeave(guard)
onBeforeRouteUpdate(guard)
onMounted(() => { if (!props.role) void nextTick(() => document.getElementById('role-name')?.focus()) })
</script>

<template>
  <div class="role-editor">
    <RouterLink class="back" to="/settings/access/roles"><AppIcon name="arrow-left" :size="13" />All roles</RouterLink>
    <header class="head">
      <div class="head-main">
        <template v-if="readOnly">
          <h3 class="r-title">{{ role?.name }}</h3>
          <p class="r-desc">{{ role?.description }}</p>
        </template>
        <template v-else>
          <div class="field-row">
            <label class="label" for="role-name">Name</label>
            <input id="role-name" v-model="name" :disabled="frozen" class="field name-input" type="text" maxlength="60" autocomplete="off" :aria-invalid="!!errors.name" :aria-describedby="errors.name ? 'role-name-error' : undefined" @input="errors.name = ''" />
            <span v-if="errors.name" id="role-name-error" class="error"><AppIcon name="alert" :size="12" />{{ errors.name }}</span>
          </div>
          <div class="field-row">
            <label class="label" for="role-description">What it is for</label>
            <input id="role-description" v-model="description" :disabled="frozen" class="field" type="text" maxlength="200" placeholder="For example: a member who also issues quotes" />
          </div>
        </template>
        <p class="badges">
          <span v-if="role?.builtin" class="badge"><AppIcon name="shield" :size="12" />Built in</span>
          <span v-else class="badge custom"><AppIcon name="sliders" :size="12" />{{ role ? 'Custom' : 'New custom role' }}</span>
          <span v-if="base" class="badge plain">Based on {{ base.name }}</span>
          <span v-if="role" class="badge plain">{{ role.member_count ? `Used by ${role.member_count}` : 'Unused' }}</span>
          <span class="badge plain">{{ picked.size }} of {{ access.registry.length }} permissions<template v-if="highOn"> · {{ highOn }} high risk</template></span>
        </p>
      </div>
      <div class="head-acts">
        <button v-if="role?.builtin && manage" type="button" class="btn sm" @click="duplicate"><AppIcon name="copy" :size="13" />Duplicate to customize</button>
        <button v-if="role && !role.builtin && manage" type="button" class="btn sm" @click="duplicate"><AppIcon name="copy" :size="13" />Duplicate</button>
        <button v-if="role && !role.builtin && manage" type="button" class="btn sm danger" @click="deleting = true"><AppIcon name="trash" :size="13" />Delete role…</button>
      </div>
    </header>
    <p v-if="role?.builtin" class="set-note"><AppIcon name="info" :size="14" /><span>Built-in roles come with {{ brand.short_name }} and change only with it. Duplicate this one to make your own version.</span></p>

    <div class="composer">
      <div class="tools">
        <label class="search-field find">
          <AppIcon name="search" :size="14" />
          <input v-model="term" class="field" type="search" placeholder="Find a permission" aria-label="Find a permission" autocomplete="off" spellcheck="false" />
        </label>
        <div class="seg" role="radiogroup" aria-label="Permissions to show">
          <button type="button" role="radio" :aria-checked="view === 'all'" @click="view = 'all'">All<span class="n mono">{{ counts.all }}</span></button>
          <button type="button" role="radio" :aria-checked="view === 'selected'" @click="view = 'selected'">In this role<span class="n mono">{{ counts.selected }}</span></button>
          <button type="button" role="radio" :aria-checked="view === 'high'" @click="view = 'high'">High risk<span class="n mono">{{ counts.high }}</span></button>
          <button v-if="base" type="button" role="radio" :aria-checked="view === 'changes'" @click="view = 'changes'">Changes<span class="n mono">{{ counts.changes }}</span></button>
        </div>
      </div>
      <p v-if="base" class="vs" aria-live="polite">
        <span>Compared with {{ base.name }}:</span>
        <span v-if="!vsBase.added.length && !vsBase.removed.length" class="same">the same permissions</span>
        <template v-else>
          <span v-if="vsBase.added.length" class="plus"><AppIcon name="plus" :size="11" />{{ vsBase.added.length }} added</span>
          <span v-if="vsBase.removed.length" class="minus"><AppIcon name="minus" :size="11" />{{ vsBase.removed.length }} removed</span>
        </template>
      </p>
      <p v-if="leftOut.length" class="set-note"><AppIcon name="info" :size="14" />Left out of the copy, because you do not hold {{ leftOut.length === 1 ? 'it' : 'them' }}: {{ leftOut.map(permissionLabel).join(', ') }}.</p>
      <p v-if="errors.permissions" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ errors.permissions }}</p>
      <section v-for="g in groups" :key="g.group" class="group" :aria-labelledby="`g-${g.group}`">
        <div class="g-head">
          <h4 :id="`g-${g.group}`">{{ g.group }}</h4>
          <span class="g-count mono">{{ g.on }} of {{ g.items.length }}</span>
          <button v-if="!frozen && !term && view === 'all'" type="button" class="btn sm ghost" @click="toggleGroup(g.items)">{{ g.items.filter(p => !blocked(p)).every(p => picked.has(p.key)) ? 'None' : 'All' }}</button>
        </div>
        <ul class="perms">
          <li v-for="p in g.shown" :key="p.key" class="perm" :class="{ on: picked.has(p.key), blocked: blocked(p), added: vsBase.added.includes(p.key), removed: vsBase.removed.includes(p.key) }">
            <label class="perm-label">
              <input
                type="checkbox" class="check-box" :checked="picked.has(p.key)" :disabled="frozen || blocked(p)"
                :aria-describedby="`p-${p.key}-desc${blocked(p) ? ` p-${p.key}-why` : ''}`" @change="toggle(p)"
              />
              <span class="p-text">
                <span class="p-name">{{ permissionLabel(p.key) }}<code class="p-key">{{ p.key }}</code></span>
                <span :id="`p-${p.key}-desc`" class="p-desc">{{ p.description }}<template v-if="!p.grantable_at.includes('project')"> · workspace only</template></span>
                <span v-if="blocked(p)" :id="`p-${p.key}-why`" class="p-why"><AppIcon name="info" :size="12" />You do not hold this permission, so you cannot give it.</span>
              </span>
            </label>
            <span class="p-side">
              <span v-if="vsBase.added.includes(p.key)" class="mark plus" :data-tip="`Not in ${base?.name}`"><AppIcon name="plus" :size="11" /><span class="sr-only">added compared with {{ base?.name }}</span></span>
              <span v-else-if="vsBase.removed.includes(p.key)" class="mark minus" :data-tip="`In ${base?.name}, not here`"><AppIcon name="minus" :size="11" /><span class="sr-only">removed compared with {{ base?.name }}</span></span>
              <RiskBadge :risk="p.risk" />
            </span>
          </li>
        </ul>
      </section>
      <p v-if="!groups.length" class="empty">{{ term ? `No permission matches “${term}”.` : view === 'changes' ? `The same as ${base?.name}.` : 'Nothing to show.' }}</p>
    </div>

    <div v-if="!frozen" class="savebar">
      <span class="state" aria-live="polite">
        <template v-if="errors.form"><AppIcon name="alert" :size="13" class="err" />{{ errors.form }}</template>
        <template v-else-if="!role">Not created yet</template>
        <template v-else-if="unsaved">{{ changes ? `${changes} permission change${changes === 1 ? '' : 's'} not saved` : 'Not saved' }}</template>
        <template v-else><AppIcon name="check" :size="13" class="ok" />Saved</template>
      </span>
      <button v-if="role && unsaved" type="button" class="btn sm" @click="revert">Discard</button>
      <button type="button" class="btn sm primary" :disabled="saving || (!!role && !unsaved)" @click="save">{{ saving ? 'Saving…' : role ? 'Save role' : 'Create role' }}</button>
    </div>
    <DeleteRoleSheet v-if="deleting && role" :role="role" @close="deleting = false" @deleted="afterDelete" />
  </div>
</template>

<style scoped>
.role-editor { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; }
.back { display: inline-flex; align-items: center; gap: 6px; justify-self: start; height: 28px; margin-left: -8px; padding: 0 8px; border-radius: 8px; color: var(--ink-2); font-size: 13px; text-decoration: none; }
.back:hover { color: var(--teal-ink); background: var(--row-hover); }
.back:focus-visible { box-shadow: var(--focus-ring); }
.head { display: flex; align-items: flex-start; gap: 16px; }
.head-main { flex: 1; display: grid; gap: 10px; min-width: 0; }
.head-acts { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
.r-title { font: 600 18px/1.3 var(--font); color: var(--ink); }
.r-desc { font-size: 13.5px; color: var(--ink-2); }
.field-row { display: grid; gap: 6px; max-width: 520px; }
.label { font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.name-input { font-size: 15px; font-weight: 600; }
.error { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--danger); }
.badges { display: flex; flex-wrap: wrap; gap: 6px; }
.badge { display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 9px; border-radius: 999px; background: var(--surface-2); color: var(--ink-2); font-size: 12px; font-weight: 600; }
.badge.custom { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.badge.plain { background: transparent; box-shadow: inset 0 0 0 1px var(--line-2); font-weight: 500; }
.btn.danger { color: var(--danger); }
.composer { display: grid; grid-template-columns: minmax(0, 1fr); gap: 12px; }
.tools { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; }
.find { width: 260px; }
.n { margin-left: 6px; font-size: 11px; color: var(--ink-3); }
.vs { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 10px; font-size: 13px; color: var(--ink-2); }
.vs .plus, .vs .minus { display: inline-flex; align-items: center; gap: 4px; font-weight: 600; }
.plus { color: var(--ok); }
.minus { color: var(--danger); }
.same { color: var(--ink-3); }
.group { display: grid; gap: 4px; }
.g-head { display: flex; align-items: center; gap: 8px; min-height: 30px; }
.g-head h4 { margin: 0; font: 600 10.5px/1.5 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.g-count { font-size: 11px; color: var(--ink-3); }
.g-head .btn { margin-left: auto; height: 26px; padding: 0 9px; }
.perms { display: grid; margin: 0; padding: 0; list-style: none; border-radius: 12px; box-shadow: inset 0 0 0 1px var(--line); }
.perm { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 12px; padding: 0 12px 0 0; }
.perm + .perm { box-shadow: 0 -1px 0 var(--line); }
.perm.on { background: color-mix(in oklab, var(--row-selected), transparent 45%); }
.perm-label { display: grid; grid-template-columns: 16px minmax(0, 1fr); gap: 12px; align-items: start; padding: 10px 0 10px 12px; cursor: pointer; }
.perm.blocked .perm-label { cursor: default; }
.check-box { margin-top: 2px; }
.check-box:disabled { cursor: default; opacity: .55; }
.p-text { display: grid; gap: 2px; min-width: 0; }
.p-name { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 8px; font-size: 13.5px; font-weight: 600; color: var(--ink); }
.perm.blocked .p-name { color: var(--ink-2); }
.p-key { padding: 0 5px; border-radius: 5px; background: var(--code-bg); font-size: 11px; font-weight: 500; color: var(--ink-2); }
.p-desc { font-size: 12.5px; line-height: 1.45; color: var(--ink-2); }
.p-why { display: flex; align-items: center; gap: 5px; font-size: 12px; color: var(--ink-2); }
.p-why svg { color: var(--ink-3); }
.p-side { display: flex; align-items: center; gap: 6px; }
.mark { display: grid; place-items: center; width: 20px; height: 20px; border-radius: 50%; }
.mark.plus { background: rgba(47, 122, 90, .14); }
.mark.minus { background: var(--danger-bg); }
.empty { padding: 12px 0; font-size: 13px; color: var(--ink-3); }
.savebar { position: sticky; bottom: calc(var(--footer-h, 40px) + 8px); z-index: 4; display: flex; align-items: center; justify-content: flex-end; gap: 8px; margin: 4px -8px 0; padding: 10px 10px 10px 16px; border-radius: 999px; border: 1px solid var(--glass-edge); background: var(--glass); box-shadow: var(--shadow-pop); -webkit-backdrop-filter: blur(18px) saturate(1.2); backdrop-filter: blur(18px) saturate(1.2); }
.state { display: inline-flex; align-items: center; gap: 6px; margin-right: auto; font-size: 13px; color: var(--ink-2); }
.state .ok { color: var(--ok); }
.state .err { color: var(--danger); }
@media (max-width: 760px) {
  .head { flex-direction: column; }
  .head-acts { justify-content: flex-start; }
  .find { width: 100%; }
  /* Phones: the filters wrap onto a second row instead of hiding off the edge. */
  .seg { width: 100%; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); border-radius: 16px; }
  .seg button { white-space: nowrap; }
}
@media (max-width: 600px) {
  .back { height: 44px; }
  .head-acts .btn, .seg button { height: 44px; }
  .perm-label { padding: 12px 0 12px 12px; }
  .savebar { border-radius: 16px; flex-wrap: wrap; }
  .savebar .btn { height: 44px; }
  .check-box { width: 20px; height: 20px; }
  .perm-label { grid-template-columns: 20px minmax(0, 1fr); }
}
</style>
