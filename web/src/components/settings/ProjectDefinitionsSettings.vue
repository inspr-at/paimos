<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { Project } from '../../stores/projects'
import { useSession } from '../../stores/session'
import { can, myProjectRole, myWorkspaceRole, onAccessRevalidate } from '../../lib/authz'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { APIError } from '../../lib/api'
import { checkCount, cloneChecks, definitionDiff, readDefinitionContext, resetChanges, resetDefinition, saveDefinition, type DefinitionChecks, type DefinitionContext } from '../../lib/definitions'
import { useProjectSettingsOverlay } from '../../lib/projectSettings'
import { claimSettingsPopover } from '../../lib/settingsOverlays'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import SettingsCard from './SettingsCard.vue'
import ProjectSettingsExplainer from './ProjectSettingsExplainer.vue'
import DefinitionEditor from './DefinitionEditor.vue'

const props = defineProps<{ project: Project }>(), session = useSession(), scope = useIdentityScope(), reads = scope.lane()
const context = ref<DefinitionContext | null>(null), loading = ref(true), busy = ref(false), error = ref(''), notice = ref(''), stale = ref(false), editorEpoch = ref(0)
const own = computed(() => context.value?.definition?.source === 'project')
const checks = computed(() => context.value?.definition?.checks ?? {})
const workspace = computed(() => context.value?.workspace_default?.checks ?? {})
const admin = computed(() => session.identity?.principal.kind === 'person' && can('nodes.write', props.project.id) && [myWorkspaceRole()?.key, myProjectRole(props.project.id)?.key].some(role => role === 'owner' || role === 'admin'))
const mayManage = computed(() => !!context.value?.can_manage && admin.value && !stale.value)
const diff = computed(() => own.value ? definitionDiff(checks.value, workspace.value, context.value?.copied_from_checks) : [])
const differenceCount = computed(() => diff.value.filter(row => row.kind !== 'same').length)
const empty = computed(() => !checkCount(checks.value))
const originTitle = computed(() => own.value ? `${props.project.routeKey} has its own definition` : context.value?.definition ? 'Uses the workspace default' : 'No definition yet')
const originText = computed(() => {
  if (empty.value) return own.value ? `No checks: tickets in ${props.project.routeKey} move freely, for people and agents. Add one below, or reset to the workspace default.` : context.value?.definition ? `The workspace default has no checks. Tickets in ${props.project.routeKey} move freely, for people and agents.` : `Tickets in ${props.project.routeKey} move freely, for people and agents. Add a check below to create this project’s own definition.`
  if (!own.value) return `Changes in Settings › Policies apply to ${props.project.routeKey} at once.`
  return differenceCount.value ? `Differs in ${differenceCount.value} ${differenceCount.value === 1 ? 'place' : 'places'}, marked below.` : 'Same as the workspace default so far.'
})
const reset = ref<{ context: DefinitionContext; projectID: string; key: string; anchor: HTMLElement; lines: string[] } | null>(null), resetBody = ref<HTMLElement>()
const register = useProjectSettingsOverlay()
let unregister: (() => void) | undefined, release: (() => void) | undefined
function closeReset(focus = true) { const anchor = reset.value?.anchor; reset.value = null; unregister?.(); unregister = undefined; release?.(); release = undefined; if (focus && anchor?.isConnected) anchor.focus({ preventScroll: true }) }
function message(problem: unknown) { return problem instanceof APIError && problem.status === 409 ? 'The definition or workspace default changed. Reload before trying again; nothing from this action was saved.' : problem instanceof Error ? problem.message : 'The definition could not be saved.' }
function load() {
  if (busy.value) return
  const target = props.project.id
  loading.value = true; error.value = ''; notice.value = ''
  void reads.run(({ after, signal }) => after(readDefinitionContext(target, signal), value => {
    if (props.project.id !== target || value.project_id !== target) return
    context.value = value; stale.value = false; editorEpoch.value++
  }), { failed: problem => { error.value = message(problem); stale.value = true }, settled: () => { loading.value = false } })
}
function save(next: DefinitionChecks, applied: () => void = () => {}) {
  const captured = context.value, target = props.project.id
  if (!captured || !mayManage.value || busy.value) return
  const wasOwn = captured.definition?.source === 'project'
  busy.value = true; error.value = ''; notice.value = ''
  void scope.run(({ after, signal }) => after(saveDefinition(target, next, captured, signal), saved => {
    if (target !== props.project.id || context.value !== captured) return
    context.value = { ...captured, definition: saved, copied_from_checks: wasOwn ? captured.copied_from_checks : cloneChecks(captured.workspace_default?.checks) }
    applied(); notice.value = 'Definition saved.'
  }), { failed: problem => { error.value = message(problem); if (problem instanceof APIError && [403,409].includes(problem.status)) stale.value = true }, settled: () => { busy.value = false } })
}
function showReset(event: MouseEvent) {
  if (!context.value || !own.value || !mayManage.value || busy.value) return
  closeReset(false)
  reset.value = { context: context.value, projectID: props.project.id, key: props.project.routeKey, anchor: event.currentTarget as HTMLElement, lines: resetChanges(checks.value, workspace.value) }
  release = claimSettingsPopover(() => closeReset(false))
  unregister = register({ element: () => resetBody.value, close: closeReset, submit: confirmReset })
}
function confirmReset() {
  const action = reset.value
  if (!action || !mayManage.value || busy.value || action.projectID !== props.project.id || action.context !== context.value) return
  busy.value = true; error.value = ''; notice.value = ''
  void scope.run(({ after, signal }) => after(resetDefinition(action.projectID, action.context, signal), saved => {
    if (action.projectID !== props.project.id || reset.value !== action || context.value !== action.context) return
    context.value = { ...action.context, definition: saved, copied_from_checks: null }; editorEpoch.value++
    closeReset(); notice.value = 'The project follows the workspace default.'
  }), { failed: problem => { error.value = message(problem); if (problem instanceof APIError && [403,409].includes(problem.status)) stale.value = true }, settled: () => { busy.value = false } })
}
watch([scope.owner, () => props.project.id], () => { scope.reset(); closeReset(false); context.value = null; busy.value = false; stale.value = false; if (scope.owner.value) load() }, { immediate: true, flush: 'sync' })
const stopAccess = onAccessRevalidate({ reset: () => { closeReset(false); context.value = null; busy.value = false; stale.value = true }, refresh: () => { if (!busy.value) load() } })
watch(mayManage, allowed => { if (!allowed) closeReset(false) })
onBeforeUnmount(() => { stopAccess(); closeReset(false) })
</script>

<template>
  <SettingsCard title="Definitions" icon="check" anchor="definitions" variant="project">
    <template #lead>The checks a ticket must pass before it enters In progress, QA or Done. Other statuses have none.</template>
    <template #aside><ProjectSettingsExplainer id="definitions" :project-key="project.routeKey" /></template>
    <div class="definition-feedback" role="status"><span :class="{ error }">{{ error || (loading ? 'Reading the definition…' : busy ? 'Saving the definition…' : notice) }}</span><button v-if="error || stale" type="button" class="link-btn" :disabled="busy" @click="load">Reload</button></div>
    <template v-if="context">
      <p v-if="!admin || !context.can_manage" class="lock-line"><AppIcon name="lock" :size="14" />Project admins change this project’s definition. These checks are read-only.</p>
      <div class="origin"><AppIcon :name="own ? 'copy' : 'folders'" :size="16" class="or-ic" /><div class="or-l"><b>{{ originTitle }}</b><small>{{ originText }}</small><ProjectSettingsExplainer id="copy" :project-key="project.routeKey" /></div><div class="or-r">
        <button v-if="mayManage && !own && context.definition" type="button" class="btn sm" :disabled="busy" @click="save(cloneChecks(checks))"><AppIcon name="copy" :size="13" />Change for this project</button>
        <button v-if="mayManage && own" type="button" class="link-btn" :disabled="busy" aria-haspopup="dialog" @click="showReset">{{ context.workspace_default ? 'Reset to workspace default…' : 'Remove this definition…' }}</button>
        <RouterLink class="link-btn" to="/settings/policies#definitions">{{ own ? 'Workspace default' : 'Settings › Policies' }}<AppIcon name="chevron-right" :size="12" /></RouterLink>
      </div></div>
      <DefinitionEditor :key="editorEpoch" :checks="checks" :workspace="workspace" :base="context.copied_from_checks" :project-key="project.routeKey" :own="own" :editable="mayManage && (own || !context.definition)" :busy="busy" :error="error" :people="project.people" @change="save" />
    </template>
    <FloatingPanel v-if="reset" :anchor="reset.anchor" :label="`Reset ${reset.key}’s definition`" :width="440" :tallest="650" sheet compact-sheet cycle settings-keys class="definition-reset" @close="closeReset">
      <div ref="resetBody" class="reset-dialog" tabindex="-1"><header><h3>{{ reset.context.workspace_default ? `Reset ${reset.key} to the workspace default?` : `Remove ${reset.key}’s definition?` }}</h3></header><div class="reset-content"><p>{{ reset.context.workspace_default ? `${reset.key} drops its own definition and follows the workspace default again.` : `There is no workspace default, so tickets in ${reset.key} move freely again.` }}</p><ul><li v-for="line in reset.lines" :key="line">{{ line }}</li></ul><p>Tickets keep their status; the new checks apply to their next move.</p></div><p class="reset-feedback" role="status">{{ error }}</p><footer><button type="button" class="btn" @click="closeReset()">Cancel</button><button type="button" class="btn primary" :disabled="busy || !mayManage" @click="confirmReset">{{ reset.context.workspace_default ? 'Reset' : 'Remove' }}<KeyCap k="mod" /><KeyCap k="enter" /></button></footer></div>
    </FloatingPanel>
  </SettingsCard>
</template>

<style scoped>
.definition-feedback { display: flex; justify-content: space-between; align-items: start; gap: 8px; min-height: 3.5em; font-size: 12.5px; line-height: 1.5; color: var(--ink-3); }.error { color: var(--danger-ink); }.lock-line { display: flex; align-items: start; gap: 8px; font-size: 12.5px; line-height: 1.5; color: var(--ink-3); margin-bottom: 12px; }.lock-line svg { flex: none; margin-top: 2px; }.origin { display: grid; grid-template-columns: 28px minmax(0,1fr) auto; align-items: start; column-gap: 12px; padding: 14px 2px; border-top: 1px solid var(--line); }.or-ic { justify-self: center; margin-top: 3px; color: var(--ink-3); }.or-l { display: grid; gap: 3px; min-width: 0; }.or-l b { font-size: 13.5px; font-weight: 600; }.or-l small { min-height: calc(2 * 1.55em); font-size: 12.5px; line-height: 1.55; color: var(--ink-2); max-width: 66ch; }.or-r { display: grid; justify-items: end; align-items: start; gap: 10px; padding-top: 1px; }.link-btn { display: inline-flex; align-items: center; gap: 4px; padding: 0; border: 0; background: transparent; color: var(--ink-2); font-size: 12.5px; min-height: 30px; }.origin .btn { white-space: normal; text-align: left; }
:global(.floating.definition-reset) { padding: 0; background: var(--surface-raised); border: 1px solid var(--glass-edge); border-radius: 12px; box-shadow: var(--shadow-pop); }.reset-dialog { display: flex; flex-direction: column; min-height: 0; max-height: inherit; }.reset-dialog:focus { outline: none; }.reset-dialog header { padding: 16px; flex: none; }.reset-dialog h3 { font: 600 15px/1.4 var(--font); }.reset-content { padding: 0 16px; overflow: auto; font-size: 13px; line-height: 1.6; }.reset-content ul { padding-left: 18px; list-style: disc; margin: 8px 0; }.reset-feedback { min-height: 3em; padding: 6px 16px; font-size: 12px; color: var(--danger-ink); }.reset-dialog footer { display: flex; flex-wrap: wrap; justify-content: end; gap: 8px; padding: 12px 16px; border-top: 1px solid var(--line); flex: none; }
@container project-settings-body (max-width: 600px) { .origin { grid-template-columns: 24px minmax(0,1fr); row-gap: 10px; }.or-r { grid-column: 2; justify-items: start; } }
@media (max-width: 600px) { :global(.floating.definition-reset.sheet) { height: 100dvh; max-height: 100dvh !important; border-radius: 0; }.reset-dialog { height: 100%; }.reset-dialog header { padding-top: max(18px,env(safe-area-inset-top)); }.reset-content { flex: 1; }.reset-dialog footer { padding-bottom: max(14px,env(safe-area-inset-bottom)); }.reset-dialog footer .btn { flex: 1; } }
@media (pointer: coarse), (max-width: 600px) { .link-btn, .origin .btn, .reset-dialog .btn { min-height: 44px; } }
</style>
