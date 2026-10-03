<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, useId, watch } from 'vue'
import { beyond, diff, effectLine, LAST_OWNER_REASON, lostPermission, neededToGive, OWNER_TRANSFER_REASON, ownerChangeNeedsTransfer, type Permission, permissionLabel, projectRolesOf, type Role, workspaceRolesOf } from '../../lib/access'
import AppIcon from '../AppIcon.vue'
import { grantablePermissions } from '../../lib/authz'
import FloatingPanel from '../work/FloatingPanel.vue'
import RiskBadge from './RiskBadge.vue'

// Choosing a role for someone, in the workspace or on a project. Picking a role
// shows what it would change before anything is saved: its effect in words and
// what the person gains and loses. Roles I may not give are there, disabled,
// with the reason; so is everything else while the last owner keeps Owner.
const props = withDefaults(defineProps<{
  // place: the project, for a project role ("Give Mira Guest on Pharos").
  anchor: HTMLElement | null; subject: string; place?: string; roles: Role[]; current: string | null; registry: Permission[]; mine: Set<string>
  scope: 'workspace' | 'project'; allowNone?: boolean; noneLabel?: string; locked?: boolean; busy?: boolean
  // Why the server refused the last choice, in words; shown at the choice until another role is picked.
  error?: string
  // Whether I may still change it (members.manage here); false keeps the choice and blocks Apply.
  canApply?: boolean
  roleDetails?: ReadonlyMap<string, string>
}>(), { canApply: true, allowNone: false, noneLabel: 'No workspace role', locked: false, busy: false, error: '', roleDetails: () => new Map() })
const emit = defineEmits<{ choose: [roleId: string | null]; close: [restoreFocus: boolean] }>()
const id = useId()
const byKey = computed(() => new Map(props.registry.map(p => [p.key, p])))
// Only roles the server accepts at this scope: never Guest in the workspace, never Owner or Customer on a project.
const offered = computed(() => props.scope === 'workspace' ? workspaceRolesOf(props.roles) : projectRolesOf(props.roles, props.registry))
const NONE = '__none__'
const options = computed(() => [...offered.value.map(role => ({ id: role.id, role })), ...(props.allowNone ? [{ id: NONE, role: null }] : [])])
const picked = ref<string>(props.current ?? (props.allowNone ? NONE : ''))
const currentRole = computed(() => props.roles.find(role => role.id === props.current) ?? null)
// Why nothing but the current role can be chosen: the last owner, or an owner's role without Transfer ownership.
const lockReason = computed(() => props.locked ? LAST_OWNER_REASON : props.scope === 'workspace' && ownerChangeNeedsTransfer(currentRole.value, props.mine) ? OWNER_TRANSFER_REASON : '')
const pickedRole = computed(() => props.roles.find(role => role.id === picked.value) ?? null)
function reasonFor(role: Role | null): string {
  if (lockReason.value && (role?.id ?? null) !== props.current) return lockReason.value
  if (!role) return ''
  const missing = beyond(neededToGive(role, props.scope, props.registry), grantablePermissions(props.mine))
  if (missing.length) return `Includes ${missing.length === 1 ? 'a permission' : 'permissions'} you do not hold: ${missing.slice(0, 3).map(permissionLabel).join(', ')}${missing.length > 3 ? ` and ${missing.length - 3} more` : ''}.`
  return ''
}
const RISK = { high: 0, medium: 1, low: 2 } as const
const byRisk = (keys: string[]) => [...keys].sort((a, b) => RISK[byKey.value.get(a)?.risk ?? 'low'] - RISK[byKey.value.get(b)?.risk ?? 'low'])
// What is gained or lost, the riskiest first.
const change = computed(() => { const d = diff(currentRole.value?.permissions ?? [], pickedRole.value?.permissions ?? []); return { added: byRisk(d.added), removed: byRisk(d.removed) } })
const changed = computed(() => (picked.value === NONE ? null : picked.value) !== props.current)
const pickedReason = computed(() => reasonFor(picked.value === NONE ? null : pickedRole.value))
const labelFor = (role: Role | null) => role ? `Give ${props.subject} ${role.name}${props.place ? ` on ${props.place}` : ''}` : `Remove ${props.subject}’s workspace role`
const applyLabel = computed(() => labelFor(pickedRole.value))
// Reserve the labels this picker can show, at the actual font and available
// width. The descriptive action stays readable without changing button geometry.
const actionLabels = computed(() => [...new Set(['Choose a role', ...options.value.map(option => labelFor(option.role))])])
const whom = computed(() => props.place ? `${props.subject} on ${props.place}` : props.subject)
const roleDetail = (role: Role) => props.roleDetails.get(role.id) || role.description
const risky = (keys: string[]) => keys.filter(key => byKey.value.get(key)?.risk === 'high')
function keys(event: KeyboardEvent) {
  const ids = options.value.map(option => option.id)
  const at = ids.indexOf(picked.value)
  // Arrows choose a role in the list; elsewhere (the preview) they scroll.
  if ((event.key === 'ArrowDown' || event.key === 'ArrowUp') && list.value?.contains(event.target as Node)) {
    event.preventDefault()
    picked.value = ids[(at + (event.key === 'ArrowDown' ? 1 : -1) + ids.length) % ids.length]!
    document.getElementById(`${id}-${picked.value}`)?.focus({ preventScroll: true })
    void nextTick(reveal)
  } else if (event.key === 'Enter' && changed.value && !pickedReason.value) { event.preventDefault(); apply() }
}
function apply() { if (!changed.value || pickedReason.value || props.busy || !props.canApply) return; emit('choose', picked.value === NONE ? null : picked.value) }
onMounted(() => { document.getElementById(`${id}-${picked.value}`)?.focus({ preventScroll: true }); void nextTick(reveal) })
const list = ref<HTMLElement>()
const body = ref<HTMLElement>()
const refusal = ref(props.error)
watch(() => props.error, value => { refusal.value = value })
watch(picked, () => { refusal.value = '' })
// Opening and keyboard navigation reveal a role inside the body alone. A pointer
// selection never scrolls or moves the row that was just clicked.
function reveal() {
  const box = body.value, item = document.getElementById(`${id}-${picked.value}`)
  if (!box || !item) return
  const top = item.getBoundingClientRect().top - box.getBoundingClientRect().top + box.scrollTop, bottom = top + item.offsetHeight
  if (top < box.scrollTop) box.scrollTop = top
  else if (bottom > box.scrollTop + box.clientHeight) box.scrollTop = bottom - box.clientHeight
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :tallest="960" :label="`Role of ${whom}`" sheet cycle @close="restore => emit('close', restore)">
    <div class="picker" @keydown="keys">
      <p class="eyebrow title">{{ scope === 'workspace' ? 'Workspace role' : 'Project role' }} · {{ whom }}</p>
      <div ref="body" class="picker-body">
        <p v-if="lockReason" :id="`${id}-locked`" class="locked"><AppIcon name="shield" :size="14" /><span>{{ lockReason }}</span></p>
        <div ref="list" class="options" role="radiogroup" :aria-label="`Role for ${whom}`">
          <button
            v-for="option in options" :id="`${id}-${option.id}`" :key="option.id" type="button" role="radio" class="option"
            :aria-checked="picked === option.id" :tabindex="picked === option.id ? 0 : -1" :class="{ off: !!reasonFor(option.role) }"
            :data-autofocus="picked === option.id ? '' : undefined"
            :aria-describedby="reasonFor(option.role) ? (lockReason ? `${id}-locked` : `${id}-${option.id}-why`) : undefined" @click="picked = option.id"
          >
            <span class="dot" aria-hidden="true"><span /></span>
            <span class="text">
              <span class="name"><span class="role-name" :data-tip="option.role?.name ?? noneLabel">{{ option.role?.name ?? noneLabel }}</span><span v-if="option.id === (current ?? (allowNone ? NONE : ''))" class="now">now</span><span v-if="option.role && !option.role.builtin" class="custom">Custom</span></span>
              <span v-if="!reasonFor(option.role) || lockReason" class="desc" :data-tip="option.role ? roleDetail(option.role) : undefined">{{ option.role ? roleDetail(option.role) : scope === 'workspace' ? 'Only the projects they are given.' : 'No role on this project.' }}</span>
              <!-- While the last owner is locked the reason is said once, above; otherwise per role. -->
              <span v-if="reasonFor(option.role) && !lockReason" :id="`${id}-${option.id}-why`" class="why" :data-tip="reasonFor(option.role)"><AppIcon name="info" :size="12" /><span>{{ reasonFor(option.role) }}</span></span>
            </span>
          </button>
        </div>
        <!-- Always in place, so the menu opens where the full preview will fit. -->
        <div v-if="!lockReason" class="preview" :class="{ idle: !changed }" role="region" aria-label="What changes" tabindex="0" aria-live="polite">
          <p class="eyebrow">What changes</p>
          <p v-if="!canApply" class="refusal" role="alert"><AppIcon name="shield" :size="13" /><span>{{ lostPermission('members.manage') }}</span></p>
          <p v-if="refusal" :id="`${id}-error`" class="refusal" role="alert"><AppIcon name="alert" :size="13" /><span>{{ refusal }}</span></p>
          <p v-if="!changed" class="effect idle-text">Pick another role to see what it adds or takes away.</p>
          <p v-else class="effect">{{ pickedRole ? effectLine(pickedRole.permissions, registry) : scope === 'workspace' ? 'Only the projects they are given, nothing in the workspace.' : 'No access on this project beyond their workspace role.' }}</p>
          <div v-if="changed && change.added.length" class="delta">
            <span class="delta-h gain"><AppIcon name="plus" :size="11" />Gains {{ change.added.length }}</span>
            <span v-for="key in change.added.slice(0, 6)" :key="key" class="perm" :class="{ high: risky([key]).length }">{{ permissionLabel(key) }}<RiskBadge v-if="risky([key]).length" risk="high" compact /></span>
            <span v-if="change.added.length > 6" class="more">and {{ change.added.length - 6 }} more</span>
          </div>
          <div v-if="changed && change.removed.length" class="delta">
            <span class="delta-h lose"><AppIcon name="minus" :size="11" />Loses {{ change.removed.length }}</span>
            <span v-for="key in change.removed.slice(0, 6)" :key="key" class="perm">{{ permissionLabel(key) }}</span>
            <span v-if="change.removed.length > 6" class="more">and {{ change.removed.length - 6 }} more</span>
          </div>
        </div>
        <p v-if="lockReason && !canApply" class="refusal" role="alert"><AppIcon name="shield" :size="13" /><span>{{ lostPermission('members.manage') }}</span></p>
        <p v-if="lockReason && refusal" :id="`${id}-error`" class="refusal" role="alert"><AppIcon name="alert" :size="13" /><span>{{ refusal }}</span></p>
      </div>
      <div class="actions">
        <template v-if="lockReason"><button type="button" class="btn sm" @click="emit('close', true)">Close</button></template>
        <template v-else>
          <button type="button" class="btn sm" @click="emit('close', true)">Cancel</button>
          <button type="button" class="btn sm primary" :disabled="!changed || !!pickedReason || busy || !canApply" :data-tip="canApply ? pickedReason || undefined : lostPermission('members.manage')" @click="apply">
            <span class="action-label"><span v-for="label in actionLabels" :key="label" class="reserve" aria-hidden="true">{{ label }}</span><span>{{ changed ? applyLabel : 'Choose a role' }}</span></span>
            <kbd class="keycap" aria-hidden="true"><AppIcon name="enter" :size="12" /></kbd>
          </button>
        </template>
      </div>
    </div>
  </FloatingPanel>
</template>

<style scoped>
/* Choices and the reserved preview share one body; actions remain outside it. */
.picker { display: flex; flex-direction: column; gap: 8px; max-height: calc(var(--floating-max, 960px) - 12px); padding: 4px 4px 2px; }
.picker > * { flex-shrink: 0; }
.picker > .picker-body { flex: 1 1 auto; min-height: 0; overflow: auto; overscroll-behavior: contain; scrollbar-gutter: stable; }
.title { padding: 2px 6px 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.locked { display: grid; grid-template-columns: 14px 1fr; gap: 8px; margin: 0 2px; padding: 9px 10px; border-radius: 10px; background: var(--surface-2); font-size: 12.5px; line-height: 1.45; color: var(--ink-2); }
.locked svg { margin-top: 2px; color: var(--teal-ink); }
.options { display: grid; align-content: start; gap: 1px; }
.option { display: grid; grid-template-columns: 18px minmax(0, 1fr); gap: 10px; align-items: center; width: 100%; height: 60px; padding: 7px 10px; border: 0; border-radius: 10px; background: transparent; color: var(--ink); text-align: left; }
@media (hover: hover) { .option:hover { background: var(--row-hover); } }
.option[aria-checked="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.option:focus-visible { box-shadow: var(--focus-ring); }
.dot { display: grid; place-items: center; width: 16px; height: 16px; margin-top: 1px; border-radius: 50%; box-shadow: inset 0 0 0 1.5px var(--line-2); }
.option[aria-checked="true"] .dot { box-shadow: inset 0 0 0 1.5px var(--teal); }
.option[aria-checked="true"] .dot > span { width: 8px; height: 8px; border-radius: 50%; background: var(--teal); }
.text { display: grid; gap: 2px; min-width: 0; }
.name { display: flex; align-items: center; gap: 6px; min-width: 0; font-size: 13.5px; font-weight: 600; }
.role-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.now, .custom { flex-shrink: 0; }
.now, .custom { height: 17px; padding: 0 6px; border-radius: 999px; background: var(--surface-2); color: var(--ink-2); font: 600 10px/17px var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.custom { background: var(--chip-teal-bg); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
/* One line each, so the list and what changes both fit; the full text is the tooltip. */
.desc { font-size: 12.5px; line-height: 1.4; color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.why { display: flex; gap: 5px; align-items: flex-start; min-width: 0; font-size: 12px; line-height: 1.4; color: var(--ink-2); }
.why > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.why svg { flex-shrink: 0; }
.why svg { margin-top: 2px; color: var(--ink-3); }
.option.off .name { color: var(--ink-2); }
.preview { display: flex; flex-direction: column; gap: 6px; height: 160px; overflow: auto; overscroll-behavior: contain; scrollbar-gutter: stable; margin: 8px 2px 0; padding: 10px 12px; border-top: 1px solid var(--line); }
.preview > * { flex-shrink: 0; }
.effect { font-size: 13px; line-height: 1.45; color: var(--ink); }
.idle-text { color: var(--ink-3); }
.refusal { display: grid; grid-template-columns: 13px minmax(0, 1fr); gap: 7px; margin: 0 4px; font-size: 12.5px; line-height: 1.45; color: var(--danger); }
.refusal svg { margin-top: 2px; }
.preview:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.delta { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 6px; }
.delta-h { display: inline-flex; align-items: center; gap: 4px; margin-right: 2px; font: 600 11px/1 var(--mono); letter-spacing: .04em; font-variant-ligatures: none; }
.delta-h.gain { color: var(--ok); }
.delta-h.lose { color: var(--danger); }
.perm { display: inline-flex; align-items: center; gap: 4px; height: 22px; padding: 0 8px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font-size: 12px; color: var(--ink); }
.perm.high { padding-right: 2px; }
.more { font-size: 12px; color: var(--ink-3); }
.actions { display: grid; grid-template-columns: auto minmax(0, max-content); justify-content: end; align-items: center; gap: 8px; padding: 8px 2px 2px; border-top: 1px solid var(--line); }
.actions .primary { display: inline-grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; min-width: 0; height: auto; min-height: 28px; padding-block: 5px; line-height: 1.35; }
.action-label { display: grid; min-width: 0; }
.action-label > span { grid-area: 1 / 1; white-space: normal; overflow-wrap: anywhere; }
.action-label .reserve { visibility: hidden; }
@media (max-width: 600px) {
  .picker { height: calc(var(--floating-max) - 12px); }
  .option { padding: 10px; }
  .actions { padding-bottom: calc(2px + env(safe-area-inset-bottom)); }
  .actions .btn { height: auto; min-height: 44px; }
}
</style>
