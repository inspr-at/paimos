<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useProfile } from '../../stores/profile'
import { displayLanguage } from '../../lib/displayLanguage'
import type { Project } from '../../stores/projects'
import { useSession } from '../../stores/session'
import { can, myProjectRole, myWorkspaceRole, onAccessRevalidate } from '../../lib/authz'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { APIError } from '../../lib/api'
import { checkCount, cloneChecks, decideDefinitionChange, definitionDate, definitionAdminNames, definitionNotice, definitionDiff, readDefinitionContext, resetChanges, resetDefinition, saveDefinition, subscribeDefinitionChanges, type DefinitionChange, type DefinitionChecks, type DefinitionContext } from '../../lib/definitions'
import { useProjectSettingsOverlay } from '../../lib/projectSettings'
import { claimSettingsPopover } from '../../lib/settingsOverlays'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import SettingsCard from './SettingsCard.vue'
import ProjectSettingsExplainer from './ProjectSettingsExplainer.vue'
import DefinitionEditor from './DefinitionEditor.vue'

const route = useRoute(), profile = useProfile()
const language = computed(() => displayLanguage(profile.profile?.locale, 'definitions'))
const t = (en: string, de: string) => language.value === 'de' ? de : en
const props = defineProps<{ project: Project }>(), session = useSession(), scope = useIdentityScope(), reads = scope.lane()
const context = ref<DefinitionContext | null>(null), loading = ref(true), busy = ref(false), error = ref(''), stale = ref(false), editorEpoch = ref(0)
const editor = ref<InstanceType<typeof DefinitionEditor>>()
const notice = ref<HTMLElement>(), origin = ref<HTMLElement>(), noticeShown = ref(false)
const noticeHeight = ref(0), originHeight = ref(0)
const keyParts = (text: string) => text.split(props.project.routeKey)
// Retain space through decisions, but start from natural content on a new
// width or language so phone/German reservations can shrink again.
function rememberLayout(fresh = false) {
  if (!noticeShown.value) return
  for (const [element, height] of [[notice.value, noticeHeight], [origin.value, originHeight]] as const) {
    if (!element) continue
    element.style.minHeight = ''
    height.value = Math.max(fresh ? 0 : height.value, element.getBoundingClientRect().height)
    element.style.minHeight = `${height.value}px`
  }
}
let layoutObserver: ResizeObserver | undefined
onMounted(() => {
  let width = origin.value?.getBoundingClientRect().width
  layoutObserver = new ResizeObserver(([entry]) => {
    if (!entry || entry.contentRect.width === width) return
    width = entry.contentRect.width
    rememberLayout(true)
  })
  if (origin.value) layoutObserver.observe(origin.value)
})
watch(language, () => { void nextTick(() => rememberLayout(true)) })
const pending = computed(() => context.value?.pending_changes ?? [])
const changedDate = computed(() => definitionDate(context.value?.workspace_default?.revision ?? '', language.value))
const noticeText = computed(() => definitionNotice(props.project.routeKey,pending.value.length,changedDate.value,language.value))
const copyDate = computed(() => context.value?.definition?.created_at ? definitionDate(context.value.definition.created_at,language.value) : '')
const adminNames = computed(() => definitionAdminNames(context.value?.admins, context.value?.admins_more, language.value))
function landOnNotice() { if (route.query.definition_changes === '1' && pending.value.length) void nextTick(() => { notice.value?.scrollIntoView({block:'start'}); void editor.value?.focusWaiting() }) }
const own = computed(() => context.value?.definition?.source === 'project')
const checks = computed(() => context.value?.definition?.checks ?? {})
const workspace = computed(() => context.value?.workspace_default?.checks ?? {})
const admin = computed(() => session.identity?.principal.kind === 'person' && can('nodes.write', props.project.id) && [myWorkspaceRole()?.key, myProjectRole(props.project.id)?.key].some(role => role === 'owner' || role === 'admin'))
const mayManage = computed(() => !!context.value?.can_manage && admin.value && !stale.value)
const diff = computed(() => own.value ? definitionDiff(checks.value, workspace.value, context.value?.copied_from_checks) : [])
const differenceCount = computed(() => diff.value.filter(row => row.kind !== 'same').length)
const empty = computed(() => !checkCount(checks.value))
const originTitle = computed(() => !context.value ? t('Definitions','Definitionen') : own.value ? t(`${props.project.routeKey} has its own definition`,`${props.project.routeKey} hat eine eigene Definition`) : context.value.definition ? t('Uses the workspace default','Nutzt den Workspace-Standard') : t('No definition yet','Noch keine Definition'))
const originText = computed(() => {
  if (loading.value) return t('Reading the definition…','Definition wird gelesen…')
  if (!context.value) return t('The definition could not be read. Reload to try again.','Die Definition konnte nicht gelesen werden. Für einen erneuten Versuch neu laden.')
  if (empty.value) return own.value ? t(`No checks. Tickets in ${props.project.routeKey} move freely, for people and agents. Add one below.`,`Keine Prüfungen. Tickets in ${props.project.routeKey} bewegen sich frei, für Personen und Agenten. Unten eine Prüfung hinzufügen.`) : context.value.definition ? t(`The workspace default has no checks. Tickets in ${props.project.routeKey} move freely.`,`Der Workspace-Standard hat keine Prüfungen. Tickets in ${props.project.routeKey} bewegen sich frei.`) : t(`Tickets in ${props.project.routeKey} move freely, for people and agents. Add a check below.`,`Tickets in ${props.project.routeKey} bewegen sich frei, für Personen und Agenten. Unten eine Prüfung hinzufügen.`)
  if (own.value && !context.value?.workspace_default) return t(`Made for ${props.project.routeKey}; there is no workspace default.`,`Für ${props.project.routeKey} erstellt; es gibt keinen Workspace-Standard.`)
  if (!own.value) return t(`Changes to Policies in Settings apply to ${props.project.routeKey} at once.`,`Änderungen an den Richtlinien in den Einstellungen gelten sofort für ${props.project.routeKey}.`)
  const provenance = copyDate.value ? context.value.definition?.copied_from_revision ? t(`Copied from the workspace default on ${copyDate.value}. `,`Vom Workspace-Standard am ${copyDate.value} kopiert. `) : t(`Made for ${props.project.routeKey} on ${copyDate.value}. `,`Für ${props.project.routeKey} am ${copyDate.value} erstellt. `) : ''
 return provenance + (differenceCount.value ? t(`Differs in ${differenceCount.value}\u00a0${differenceCount.value === 1 ? 'place' : 'places'}, marked below.`,`Weicht an ${differenceCount.value}\u00a0${differenceCount.value === 1 ? 'Stelle' : 'Stellen'} ab, unten markiert.`) : t('Same as the workspace default so far.','Bisher gleich wie der Workspace-Standard.'))
})
const reset = ref<{ context: DefinitionContext; projectID: string; key: string; anchor: HTMLElement } | null>(null), resetBody = ref<HTMLElement>()
const register = useProjectSettingsOverlay()
let unregister: (() => void) | undefined, release: (() => void) | undefined
function closeReset(focus = true) { const anchor = reset.value?.anchor; reset.value = null; unregister?.(); unregister = undefined; release?.(); release = undefined; if (focus && anchor?.isConnected) anchor.focus({ preventScroll: true }) }
function message(problem: unknown) { return problem instanceof APIError && problem.status === 409 ? t('The definition or workspace default changed. Reload before trying again; nothing from this action was saved.','Die Definition oder der Workspace-Standard hat sich geändert. Vor einem erneuten Versuch neu laden; diese Aktion hat nichts gespeichert.') : problem instanceof Error ? problem.message : t('The definition could not be saved.','Die Definition konnte nicht gespeichert werden.') }
function failed(problem: unknown, target: string) {
  if (props.project.id !== target) return
  error.value = message(problem)
  if (editor.value?.formOpen || reset.value) return
  const owner = scope.owner.value
  toast(error.value, { tone: 'error', key: `definitions/${target}`, action: { label: t('Reload','Neu laden'), run: () => { if (props.project.id === target && scope.owner.value === owner) load() } } })
}
function load() {
  if (busy.value) return
  const target = props.project.id
  rememberLayout()
  loading.value = true; error.value = ''
  void reads.run(({ after, signal }) => after(readDefinitionContext(target, signal), value => {
    if (props.project.id !== target || value.project_id !== target) return
    const previous = context.value
    if (previous && (previous.definition?.revision !== value.definition?.revision || previous.workspace_default?.revision !== value.workspace_default?.revision)) editorEpoch.value++
    context.value = value; stale.value = false; noticeShown.value ||= !!value.pending_changes?.length; void nextTick(rememberLayout); landOnNotice()
  }), { failed: problem => { failed(problem, target); stale.value = true }, settled: () => { loading.value = false } })
}
function save(next: DefinitionChecks, applied: () => void = () => {}) {
  const captured = context.value, target = props.project.id
  if (!captured || !mayManage.value || busy.value) return
  const wasOwn = captured.definition?.source === 'project'
  rememberLayout()
  busy.value = true; error.value = ''
  void scope.run(({ after, signal }) => after(saveDefinition(target, next, captured, signal), saved => {
    if (target !== props.project.id || context.value !== captured) return
    context.value = { ...captured, definition: saved, copied_from_checks: wasOwn ? captured.copied_from_checks : cloneChecks(captured.workspace_default?.checks) }
    applied(); toast(t('Definition saved.','Definition gespeichert.'), { key: `definitions/${target}` })
  }), { failed: problem => { failed(problem, target); if (problem instanceof APIError && [403,409].includes(problem.status)) stale.value = true }, settled: () => { busy.value = false } })
}
function decide(change: DefinitionChange, decision: 'accept' | 'keep', applied: () => void) {
  const captured = context.value, target = props.project.id
  if (!captured || !mayManage.value || busy.value || !captured.pending_changes?.some(row => row.check_id === change.check_id)) return
  rememberLayout()
  busy.value = true; error.value = ''
  void scope.run(({after,signal}) => after(decideDefinitionChange(target,change,decision,captured,signal),saved => {
    if (target !== props.project.id || context.value !== captured || saved.project_id !== target) return
    context.value = saved; busy.value = false; applied()
    toast(t('Decision saved.','Entscheidung gespeichert.'),{key:`definitions/${target}`})
  }), {failed:problem => {failed(problem,target);if (problem instanceof APIError && [403,409].includes(problem.status)) stale.value=true},settled:() => {busy.value=false}})
}
function showReset(event: MouseEvent) {
  if (!context.value || !own.value || !mayManage.value || busy.value) return
  closeReset(false)
  reset.value = { context: context.value, projectID: props.project.id, key: props.project.routeKey, anchor: event.currentTarget as HTMLElement }
  release = claimSettingsPopover(() => closeReset(false))
  unregister = register({ element: () => resetBody.value, close: closeReset, submit: confirmReset })
}
function confirmReset() {
  const action = reset.value
  if (!action || !mayManage.value || busy.value || action.projectID !== props.project.id || action.context !== context.value) return
  rememberLayout()
  busy.value = true; error.value = ''
  void scope.run(({ after, signal }) => after(resetDefinition(action.projectID, action.context, signal), saved => {
    if (action.projectID !== props.project.id || reset.value !== action || context.value !== action.context) return
    context.value = { ...action.context, definition: saved, copied_from_checks: null, pending_changes: [] }; editorEpoch.value++
    closeReset(); toast(t('The project follows the workspace default.','Das Projekt folgt dem Workspace-Standard.'), { key: `definitions/${action.projectID}` })
  }), { failed: problem => { failed(problem, action.projectID); if (problem instanceof APIError && [403,409].includes(problem.status)) stale.value = true }, settled: () => { busy.value = false } })
}
let stopLive: (() => void) | undefined
watch([scope.owner, () => props.project.id], () => { stopLive?.(); scope.reset(); closeReset(false); context.value = null; busy.value = false; stale.value = false; noticeShown.value = false; noticeHeight.value = 0; originHeight.value = 0; if (scope.owner.value) { load(); stopLive=subscribeDefinitionChanges(() => { if (!own.value && !busy.value && !loading.value) load() }) } }, { immediate: true, flush: 'sync' })
const stopAccess = onAccessRevalidate({ reset: () => { closeReset(false); context.value = null; busy.value = false; stale.value = true; noticeShown.value = false; noticeHeight.value = 0; originHeight.value = 0 }, refresh: () => { if (!busy.value) load() } })
watch(mayManage, allowed => { if (!allowed && !error.value) closeReset(false) })
onBeforeUnmount(() => { layoutObserver?.disconnect(); stopLive?.(); stopAccess(); closeReset(false) })
</script>

<template>
  <SettingsCard :title="t('Definitions','Definitionen')" icon="check" anchor="definitions" variant="project">
    <template #lead>{{ t('The checks a ticket must pass before it enters In progress, QA or Done. Other statuses have none.','Die Prüfungen vor dem Wechsel eines Tickets zu In Arbeit, Qualitätsprüfung oder Erledigt. Für andere Status gibt es keine.') }}</template>
    <template #aside><ProjectSettingsExplainer id="definitions" :language="language" :project-key="project.routeKey" /></template>
      <p v-if="context && (!admin || !context.can_manage)" class="lock-line"><AppIcon name="lock" :size="14" />{{ t(`Project admins change this project’s definition${adminNames ? `: ${adminNames}` : ''}. These checks are read-only.`,`Projektadministratoren ändern diese Definition${adminNames ? `: ${adminNames}` : ''}. Die Prüfungen sind schreibgeschützt.`) }}</p>
      <div ref="origin" class="origin" :style="originHeight ? { minHeight: `${originHeight}px` } : undefined" :aria-busy="loading || busy"><AppIcon :name="own ? 'copy' : 'folders'" :size="16" class="or-ic" /><div class="or-l"><b><template v-for="(part, index) in keyParts(originTitle)" :key="index"><span v-if="index" class="project-key">{{ project.routeKey }}</span>{{ part }}</template></b><small role="status"><template v-for="(part, index) in keyParts(originText)" :key="index"><span v-if="index" class="project-key">{{ project.routeKey }}</span>{{ part }}</template></small><ProjectSettingsExplainer id="copy" :language="language" :label="t('Workspace default or this project','Workspace-Standard oder dieses Projekt')" :project-key="project.routeKey" /></div><div class="or-r">
        <div class="origin-action"><button v-if="mayManage && !own && context?.definition" type="button" class="btn sm" :disabled="busy" @click="save(cloneChecks(checks))"><AppIcon name="copy" :size="13" />{{ t('Change for this project','Für dieses Projekt ändern') }}</button>
        <button v-if="mayManage && own" type="button" class="link-btn" :disabled="busy" aria-haspopup="dialog" @click="showReset">{{ context?.workspace_default ? t('Reset to workspace default…','Auf Workspace-Standard zurücksetzen…') : t('Remove this definition…','Diese Definition entfernen…') }}</button></div>
        <RouterLink class="link-btn" to="/settings/policies#definitions"><template v-if="own">{{ t('Workspace default','Workspace-Standard') }}</template><template v-else>{{ t('Settings','Einstellungen') }} <AppIcon name="chevron-right" :size="12" /> {{ t('Policies','Richtlinien') }}</template><AppIcon name="chevron-right" :size="12" /></RouterLink>
      </div></div>
      <p v-if="noticeShown" id="definition-changes" ref="notice" class="default-notice" :style="noticeHeight ? { minHeight: `${noticeHeight}px` } : undefined" role="status"><AppIcon name="history" :size="15" /><span class="notice-copy"><span><template v-for="(part, index) in keyParts(noticeText)" :key="index"><span v-if="index" class="project-key">{{ project.routeKey }}</span>{{ part }}</template></span></span></p>
      <DefinitionEditor v-if="context" ref="editor" :key="editorEpoch" :checks="checks" :workspace="workspace" :base="context.copied_from_checks" :project-key="project.routeKey" :own="own" :editable="mayManage && (own || !context.definition)" :busy="busy" :error="error" :people="project.people" :pending="pending" :changed-date="changedDate" @change="save" @decide="decide" />
    <FloatingPanel v-if="reset" :anchor="reset.anchor" :label="t(`Reset ${reset.key}’s definition`,`Definition von ${reset.key} zurücksetzen`)" :width="440" :tallest="650" sheet compact-sheet cycle settings-keys class="definition-reset" @close="closeReset">
      <div ref="resetBody" class="reset-dialog" tabindex="-1"><header><h3><template v-for="(part, index) in keyParts(reset.context.workspace_default ? t(`Reset ${reset.key} to the workspace default?`,`${reset.key} auf den Workspace-Standard zurücksetzen?`) : t(`Remove ${reset.key}’s definition?`,`Definition von ${reset.key} entfernen?`))" :key="index"><span v-if="index" class="project-key">{{ reset.key }}</span>{{ part }}</template></h3></header><div class="reset-content"><p><template v-for="(part, index) in keyParts(reset.context.workspace_default ? t(`${reset.key} drops its own definition and follows the workspace default again.`,`${reset.key} verwirft seine eigene Definition und folgt wieder dem Workspace-Standard.`) : t(`There is no workspace default, so tickets in ${reset.key} move freely again.`,`Es gibt keinen Workspace-Standard; Tickets in ${reset.key} bewegen sich wieder frei.`))" :key="index"><span v-if="index" class="project-key">{{ reset.key }}</span>{{ part }}</template></p><ul><li v-for="line in resetChanges(reset.context.definition?.checks ?? {}, reset.context.workspace_default?.checks, language)" :key="line">{{ line }}</li></ul><p>{{ t('Tickets keep their status; the new checks apply to their next move.','Tickets behalten ihren Status; die neuen Prüfungen gelten beim nächsten Statuswechsel.') }}</p></div><footer><p class="reset-feedback" role="status">{{ error }}</p><button type="button" class="btn" @click="closeReset()">{{ t('Cancel','Abbrechen') }}</button><button type="button" class="btn primary" :disabled="busy || !mayManage" @click="confirmReset">{{ reset.context.workspace_default ? t('Reset','Zurücksetzen') : t('Remove','Entfernen') }}<KeyCap k="mod" /><KeyCap k="enter" /></button></footer></div>
    </FloatingPanel>
  </SettingsCard>
</template>

<style scoped>
.lock-line { display: flex; align-items: start; gap: 8px; font-size: 12.5px; line-height: 1.5; color: var(--ink-3); margin-bottom: 12px; }.lock-line svg { flex: none; margin-top: 2px; }.origin { display: grid; grid-template-columns: 28px minmax(0,1fr) auto; align-items: start; column-gap: 12px; padding: 14px 2px; border-top: 1px solid var(--line); }.or-ic { justify-self: center; margin-top: 3px; color: var(--ink-3); }.or-l { display: grid; gap: 3px; min-width: 0; }.or-l b { font-size: 13.5px; font-weight: 600; }.or-l small { min-height: calc(2 * 1.55em); font-size: 12.5px; line-height: 1.55; color: var(--ink-2); max-width: 66ch; }.or-r { display: grid; justify-items: end; align-items: start; gap: 10px; padding-top: 1px; }.link-btn { display: inline-flex; align-items: center; gap: 4px; padding: 0; border: 0; background: transparent; color: var(--ink-2); font-size: 12.5px; min-height: 30px; }.origin-action { display: flex; align-items: start; justify-content: end; }.origin .btn { white-space: normal; text-align: left; }
:global(.floating.definition-reset) { padding: 0; background: var(--surface-raised); border: 1px solid var(--glass-edge); border-radius: 12px; box-shadow: var(--shadow-pop); }.reset-dialog { display: flex; flex-direction: column; min-height: 0; max-height: inherit; }.reset-dialog:focus { outline: none; }.reset-dialog header { padding: 16px; flex: none; }.reset-dialog h3 { font: 600 15px/1.4 var(--font); }.reset-content { padding: 0 16px; overflow: auto; font-size: 13px; line-height: 1.6; }.reset-content ul { padding-left: 18px; list-style: disc; margin: 8px 0; }.reset-feedback { height: 2.8em; overflow: auto; overflow-wrap: anywhere; font-size: 12px; line-height: 1.4; color: var(--danger-ink); }.reset-dialog footer { display: grid; grid-template-columns: minmax(0,1fr) auto auto; align-items: center; gap: 8px; padding: 12px 16px; border-top: 1px solid var(--line); flex: none; }
@container project-settings-body (max-width: 600px) { .origin { grid-template-columns: 24px minmax(0,1fr); row-gap: 10px; }.or-l small { height: calc(3 * 1.55em); min-height: calc(3 * 1.55em); }.or-r { grid-column: 2; justify-items: start; grid-template-rows: 44px 44px; }.origin-action { height: 44px; justify-content: start; } }
@media (max-width: 600px) { :global(.floating.definition-reset.sheet) { height: 100dvh; max-height: 100dvh !important; border-radius: 0; }.reset-dialog { height: 100%; }.reset-dialog header { padding-top: max(18px,env(safe-area-inset-top)); }.reset-content { flex: 1; }.reset-dialog footer { padding-bottom: max(14px,env(safe-area-inset-bottom)); }.reset-dialog footer .btn { flex: 1; } }
@media (pointer: coarse), (max-width: 600px) { .link-btn, .origin .btn, .reset-dialog .btn { min-height: 44px; } }
.default-notice { display: flex; align-items: start; gap: 10px; padding: 10px 2px 12px; font-size: 12.5px; line-height: 1.6; color: var(--ink-2); scroll-margin-top: 20px; }.default-notice > svg { flex: none; margin-top: 3px; color: var(--ink-3); }.origin, .default-notice { box-sizing: border-box; }.project-key { white-space: nowrap; overflow-wrap: normal; }
</style>
