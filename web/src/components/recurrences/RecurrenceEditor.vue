<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, useId, watch } from 'vue'
import { createRecurrence, getNode, getRecurrence, getRoutineExecutionSettings, getRoutineGuardrails, listRoutineWorkKinds, pauseRecurrence, previewRecurrenceDraft, putRoutineConsent, recurrenceHistory, resolveRoutineRoute, updateRecurrence, type ListItem } from '../../lib/api'
import { getMembers, getProjectMembers } from '../../lib/access'
import { can, myProjectRole, myWorkspaceRole } from '../../lib/authz'
import { displayLanguage } from '../../lib/displayLanguage'
import { toast } from '../../lib/toast'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { activeKinds, type WorkKind } from '../../lib/workKinds'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import { useWorkVocabulary } from '../../stores/workVocabulary'
import { workNoun } from '../../lib/workVocabulary'
import { HARNESS_LABEL } from '../../lib/agentState'
import { acceptsRecord, assignmentFrom, budgetWords, consentRequest, copyRecurrenceInput, effortVerb, effortWord, isStale, mayWords, parseCriteria, recurrenceEstimate, recurrenceName, recurrenceSourceIsParent, recurrenceZone, renderTitle, routineDraft, routineProblems, ruleParts, staleFields, templateFrom, templateProblems, triggerWords, waitReasonWords, weekdays, when, zones, type Recurrence, type RecurrenceInput, type RecurrenceTrigger, type RoutineDefinition, type RoutineExecutionPolicy, type RoutineExecutionSettings, type RoutineGuardEntry, type RoutineScopeKind, type StaleField } from '../../lib/recurrences'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import EpicPicker from '../work/EpicPicker.vue'
import TemplateTitle from './TemplateTitle.vue'
import RoutineConsent from './RoutineConsent.vue'
import WhoDoesIt from './WhoDoesIt.vue'
import type { RunsAsState } from './RunsAsPicker.vue'

// One editor for every entry point (AEON-1182): a ticket's Repeat…, a project's
// Routines box, Settings › Personal and Settings › Routines. Ticket-only stays the
// recurring-work editor plus "Who does it" and a read-only sentence. An agent
// routine saves paused and unconsented; the same sheet then asks "Let agents run it".
// saved: the sheet is done (parents close it); changed: a write landed and it stays open.
const props = defineProps<{ project?: { id: string; routeKey: string; title?: string } | null; source?: ListItem | null; recurrence?: Recurrence; scope?: RoutineScopeKind; consent?: boolean }>()
const emit = defineEmits<{ close: []; saved: [item: Recurrence, outcome: 'saved' | 'consented' | 'kept']; changed: [item: Recurrence]; revoked: [saved: boolean] }>()
const vocabulary = useWorkVocabulary(), session = useSession(), projects = useProjects()
const uid = useId(), dialog = ref<HTMLDialogElement>(), body = ref<HTMLElement>(), nameField = ref<HTMLInputElement>()
const initial = props.recurrence, source = props.source
// The saved record shown: the prop, then each write's answer. Revision, parent and output are bound to it.
const base = ref<Recurrence | undefined>(initial)
const scopeKind: RoutineScopeKind = initial?.definition?.scope.kind ?? props.scope ?? 'project'
const outputProject = ref(initial?.project_id ?? props.project?.id ?? '')
const outputInfo = computed(() => props.project && props.project.id === outputProject.value ? props.project : projects.byId(outputProject.value))
const projectKey = computed(() => outputInfo.value?.routeKey ?? (outputProject.value ? 'its project' : 'a project'))
const template = reactive(initial ? { ...copyRecurrenceInput(initial).template, type: 'work' as const } : templateFrom(source))
if (initial && !template.name) template.name = recurrenceName(initial).slice(0, 80)
const parent = ref(initial?.parent_id ?? (recurrenceSourceIsParent(source) && source ? source.id : source?.parent_id || outputProject.value))
const topLabel = 'No parent (top level of the project)'
const parentLabel = ref(recurrenceSourceIsParent(source) && source ? `${source.key} · ${source.title}` : !parent.value ? 'Pick the project first' : parent.value === outputProject.value ? topLabel : 'Loading parent…')
function scheduleFrom(trigger?: RecurrenceTrigger) {
  const parts = ruleParts(trigger?.rrule), anchorDate = trigger?.start_date ? new Date(`${trigger.start_date}T00:00:00Z`) : null
  return { kind: trigger?.kind || 'time', frequency: parts.FREQ || 'WEEKLY', days: parts.BYDAY?.split(',') || [anchorDate ? ['SU', 'MO', 'TU', 'WE', 'TH', 'FR', 'SA'][anchorDate.getUTCDay()]! : 'MO'], interval: parts.INTERVAL || '1', day: parts.BYMONTHDAY || String(anchorDate?.getUTCDate() || 1), time: trigger?.time_of_day || '09:00', zone: trigger?.timezone || trigger?.event_timezone || (trigger?.kind === 'event' ? 'UTC' : Intl.DateTimeFormat().resolvedOptions().timeZone) || 'UTC', start: trigger?.event_start || 'now' }
}
const schedule = reactive(scheduleFrom(initial?.trigger))
// Name/template edits must keep overdue work and pending publications. Preserve
// legacy RRULE spelling and implicit event defaults until the trigger is edited.
let initialSchedule = JSON.stringify(schedule)
const estimate = ref(template.estimate_hours ? `${template.estimate_hours} h` : ''), criteria = ref(template.acceptance_criteria.join('\n'))
const queue = ref(initial?.queue_each ?? false), skip = ref(initial?.overlap_policy !== 'create')
const step = ref<'form' | 'consent'>('form'), formOpen = ref(false)
const picker = ref<HTMLElement | null>(null), busy = ref(false), failure = ref(''), previewError = ref(''), previewBusy = ref(false), times = ref<string[]>([]), policy = ref<RoutineExecutionPolicy | null>(initial?.execution_policy ?? null)
// Who does it: the saved assignment, or ticket-only. The ticket description is the agent's instructions.
const agent = ref(!!initial?.definition?.assignment), draft = ref(routineDraft(initial?.definition?.assignment))
const me = computed(() => session.identity?.principal.id ?? ''), person = computed(() => !!session.identity && session.identity.principal.kind !== 'agent')
const owner = computed(() => base.value?.definition?.owner_principal_id ?? me.value)
const mine = computed(() => person.value && owner.value === me.value)
const adminRole = (role: { key: string } | null) => role?.key === 'owner' || role?.key === 'admin'
const scopeAdmin = computed(() => adminRole(myWorkspaceRole()) || (scopeKind === 'project' && !!outputProject.value && adminRole(myProjectRole(outputProject.value))))
// Who may keep this sheet open at all; losing it closes the sheet (revocation).
const scopeAllowed = computed(() => scopeKind === 'project' ? can('recurrences.manage', outputProject.value) : scopeKind === 'personal' ? person.value : can('recurrences.manage') && (!!initial || scopeAdmin.value))
const outputAllowed = computed(() => !outputProject.value || can('recurrences.manage', outputProject.value))
const scope = useIdentityScope(() => scopeAllowed.value), previews = scope.lane(), kindsLane = scope.lane(), routeLane = scope.lane(), rulesLane = scope.lane(), settingsLane = scope.lane()
const openedAs = { id: me.value, name: session.identity?.principal.name ?? '' }
const localZone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const zoneOptions = computed(() => [...new Set([schedule.zone, ...zones])])
// A project or workspace routine is owned by an admin of its scope; a new one is owned by its creator.
const agentUnavailable = computed(() => base.value?.definition || scopeKind === 'personal' || scopeAdmin.value ? '' : scopeKind === 'workspace' ? 'Only a workspace admin can set up an agent routine; they own it and let agents run it.' : `Only a ${projectKey.value} admin can set up an agent routine here; they own it and let agents run it.`)
const definition = computed<RoutineDefinition | undefined>(() => {
  const saved = base.value?.definition
  if (scopeKind === 'project' && !saved && !agent.value) return undefined
  const assignment = agent.value ? assignmentFrom(draft.value, template.description) : null
  return { scope: saved ? { ...saved.scope } : scopeKind === 'project' ? { kind: 'project', project_id: outputProject.value } : { kind: scopeKind }, owner_principal_id: saved?.owner_principal_id ?? me.value, ...(assignment ? { assignment } : {}) }
})
const input = computed<RecurrenceInput>(() => ({ project_id: outputProject.value, parent_id: parent.value, template: { ...template, name: template.name?.trim(), estimate_hours: recurrenceEstimate(estimate.value) || 0, acceptance_criteria: parseCriteria(criteria.value) }, trigger: base.value && JSON.stringify(schedule) === initialSchedule ? { ...base.value.trigger } : schedule.kind === 'event' ? { ...(base.value?.trigger.kind === 'event' ? copyRecurrenceInput(base.value).trigger : { kind: 'event' as const, event: 'release.published' as const }), event_start: schedule.start, event_timezone: schedule.zone } : { kind: 'time', rrule: `FREQ=${schedule.frequency}${schedule.frequency === 'WEEKLY' ? `;BYDAY=${weekdays.filter(([day]) => schedule.days.includes(day)).map(([day]) => day).join(',')}` : schedule.frequency === 'MONTHLY' ? `;INTERVAL=${schedule.interval};BYMONTHDAY=${schedule.day}` : ''}`, time_of_day: schedule.time, timezone: schedule.zone, ...(base.value?.trigger.start_date ? { start_date: base.value.trigger.start_date } : {}) }, queue_each: agent.value || queue.value, overlap_policy: skip.value ? 'skip' : 'create', catch_up_policy: 'one', ...(definition.value ? { definition: definition.value } : {}) }))
const problems = computed(() => [
  ...(!outputProject.value ? ['Pick the project its tickets go to.'] : !outputAllowed.value ? [`You can’t add routines to ${projectKey.value}.`] : []),
  ...templateProblems(input.value, estimate.value), ...(agent.value ? routineProblems(draft.value, template.description) : []),
])
let timer: ReturnType<typeof setTimeout> | undefined
watch(() => JSON.stringify(input.value), () => {
  clearTimeout(timer); previews.cancel(); times.value = []; previewError.value = ''; previewBusy.value = !problems.value.length
  if (problems.value.length) return
  const snapshot = copyRecurrenceInput(input.value)
  timer = setTimeout(() => void previews.run(({ after, signal }) => after(previewRecurrenceDraft(snapshot, signal), result => { times.value = result.times; policy.value = result.execution_policy ?? null }), { failed: error => { previewError.value = error instanceof Error ? error.message : 'Preview could not be loaded.' }, settled: () => { previewBusy.value = false } }), 160)
}, { immediate: true })
// Revocation closes the sheet and the box says why; another person signing in clears the draft.
watch(() => scope.owner.value, (now, previous) => {
  if (!previous || now === previous) return
  if (session.identity && session.identity.principal.id !== openedAs.id) { toast(`Signed in as ${session.identity.principal.name} · ${openedAs.name}’s unsaved routine draft was cleared.`, { timeout: 6000 }); emit('close'); return }
  if (!now) { emit('revoked', step.value === 'consent'); emit('close') }
}, { flush: 'sync' })
watch([() => props.project?.id, () => props.source?.id, () => props.recurrence?.id], () => { scope.reset(); emit('close') }, { flush: 'sync' })
const zoneNote = computed(() => schedule.kind === 'time' && schedule.zone !== 'Europe/Vienna' && times.value[0] ? `= ${new Intl.DateTimeFormat('en-GB', { timeZone: 'Europe/Vienna', hour: '2-digit', minute: '2-digit' }).format(new Date(times.value[0]))} in Vienna` : '')
const daylightNote = computed(() => {
  if (!times.value.length) return ''
  const fmt = new Intl.DateTimeFormat('en-GB', { timeZone: schedule.zone, timeZoneName: 'shortOffset' })
  const offsets = times.value.map(at => fmt.formatToParts(new Date(at)).find(p => p.type === 'timeZoneName')?.value)
  return new Set(offsets).size > 1 ? `The clocks change within these dates; runs stay at ${schedule.time} local time.` : ''
})
function previewTitle(at: string, index: number) { return renderTitle(template.title, (base.value?.occurrence_count || 0) + index + 1, at, recurrenceZone(input.value.trigger)) }

// ---------- Agent rows: kinds of work, today's route, the rules that apply ----------
const kinds = ref<WorkKind[]>([]), kindsState = ref<'loading' | 'ready' | 'failed'>('loading'), route = ref<{ model: string; harness: string } | null>(null), routeState = ref<'loading' | 'ready' | 'failed' | 'none'>('loading')
const rules = ref<RoutineGuardEntry[] | null>(null), rulesState = ref<'unsaved' | 'loading' | 'ready' | 'failed'>('unsaved')
watch([agent, outputProject], ([on, projectId]) => {
  if (!on || !projectId) return
  kindsState.value = 'loading'
  void kindsLane.run(({ after, signal }) => after(listRoutineWorkKinds(projectId, displayLanguage(), signal), page => { kinds.value = activeKinds(page.items); kindsState.value = 'ready'; if (!draft.value.kind) draft.value.kind = kinds.value[0]?.id ?? '' }), { failed: () => { kindsState.value = 'failed' } })
}, { immediate: true })
watch([agent, () => draft.value.role, outputProject], ([on, role, projectId]) => {
  if (!on || !projectId) return
  routeState.value = 'loading'
  void routeLane.run(({ after, signal }) => after(resolveRoutineRoute(role, projectId, signal), result => { route.value = result.profile ? { model: result.profile.model, harness: result.profile.harness } : null; routeState.value = result.profile ? 'ready' : 'none' }), { failed: () => { route.value = null; routeState.value = 'failed' } })
}, { immediate: true })
watch([agent, () => base.value?.id, () => !!base.value?.definition?.assignment], ([on, id, saved]) => {
  if (!on || !id || !saved) { rulesState.value = 'unsaved'; return }
  rulesState.value = 'loading'
  void rulesLane.run(({ after, signal }) => after(getRoutineGuardrails(id, signal), result => { if (id === base.value?.id) { rules.value = result.rules; rulesState.value = 'ready' } }), { failed: () => { rulesState.value = 'failed' } })
}, { immediate: true })
// Who owns it, in words. Names come from the member lists when they may be read.
const ownerName = ref('')
watch(owner, id => {
  ownerName.value = ''
  if (!id || id === me.value) return
  const projectId = base.value?.definition?.scope.project_id
  if (projectId && can('members.read', projectId)) void scope.run(({ after }) => after(getProjectMembers(projectId), list => { if (owner.value === id) ownerName.value = list.find(member => member.principal_id === id)?.name ?? '' }), { failed: () => {} })
  else if (!projectId && can('members.read')) void scope.run(({ after }) => after(getMembers(), list => { if (owner.value === id) ownerName.value = list.people.find(member => member.principal_id === id)?.name ?? '' }), { failed: () => {} })
}, { immediate: true })
const ownerWords = computed(() => mine.value ? 'you' : ownerName.value || (scopeKind === 'workspace' ? 'a workspace admin' : `a ${projectKey.value} admin`))
const ownerTitle = computed(() => ownerWords.value[0]!.toUpperCase() + ownerWords.value.slice(1))

// ---------- Save, then consent (spec "Save, then consent") ----------
const settings = ref<RoutineExecutionSettings | null>(null), settingsFailed = ref(false), principal = ref<string | null>(null), runsAs = ref<RunsAsState>('loading')
const consentFailure = ref(''), consentCode = ref(''), pausing = ref(false)
const stale = ref<{ actor: string; at: string } | null>(null), changed = ref(new Set<StaleField>())
let savedSnapshot = initial ? JSON.stringify(input.value) : ''
const dirty = computed(() => JSON.stringify(input.value) !== savedSnapshot)
const wait = computed(() => base.value?.execution_policy?.wait_reason ?? '')
const personal = computed(() => base.value?.definition?.scope.kind === 'personal')
const consentReady = computed(() => mine.value && wait.value === 'execution_consent_required' && (settings.value ? settings.value.automatic_launch_enabled : true) && (personal.value || (runsAs.value === 'ready' && !!principal.value)))
const primary = computed<'create' | 'save' | 'consent' | null>(() => step.value === 'consent' ? dirty.value ? 'save' : mine.value ? 'consent' : null : base.value ? 'save' : 'create')
const primaryLabel = computed(() => busy.value ? primary.value === 'consent' ? 'Letting agents run it…' : 'Saving…' : primary.value === 'consent' ? 'Let agents run it' : primary.value === 'save' ? 'Save' : 'Create')
const primaryDisabled = computed(() => primary.value === 'consent' ? !consentReady.value || busy.value : !scopeAllowed.value || busy.value || !!problems.value.length || previewBusy.value || !!previewError.value)
const heading = computed(() => step.value === 'consent' ? 'Let agents run it' : base.value ? 'Edit routine' : source ? 'Repeat' : 'New routine')
const subtitle = computed(() => {
  if (step.value === 'consent' && base.value) return `${recurrenceName(base.value)} · saved, ${base.value.paused ? 'paused' : 'waiting'} until ${mine.value ? 'you let' : `${ownerWords.value} lets`} agents run it`
  if (base.value) return `Edit ${recurrenceName(base.value)}`
  if (source) return recurrenceSourceIsParent(source) ? `Creates a child ${workNoun(vocabulary.leaf.name)} of ${source.key} each time` : `Repeats ${source.key} as a template`
  return scopeKind === 'personal' ? 'Just for you · acts with your rights' : scopeKind === 'workspace' ? 'For the whole workspace' : `New routine in ${props.project?.title || props.project?.routeKey || projectKey.value}`
})
const cancelLabel = computed(() => step.value === 'consent' ? mine.value && base.value?.paused ? 'Keep paused' : 'Close' : 'Cancel')
function resetBaseline(saved: Recurrence) {
  base.value = saved; initialSchedule = JSON.stringify(schedule); savedSnapshot = JSON.stringify(input.value); policy.value = saved.execution_policy ?? policy.value
}
function close() {
  if (busy.value) return
  if (step.value === 'consent' && base.value) emit('saved', base.value, 'kept')
  else emit('close')
}
function enterConsent() {
  step.value = 'consent'; formOpen.value = false; consentFailure.value = ''; consentCode.value = ''
  const projectId = outputProject.value
  settings.value = null; settingsFailed.value = false
  void settingsLane.run(({ after, signal }) => after(getRoutineExecutionSettings(projectId, signal), result => { settings.value = result }), { failed: () => { settingsFailed.value = true } })
  void nextTick(() => body.value?.scrollTo({ top: 0 }))
}
function save() {
  if (primary.value === 'consent') { letAgentsRun(); return }
  if (primaryDisabled.value) return
  const saved = base.value
  // Save on an unchanged agent routine goes straight to the consent step: no empty revision.
  if (saved && !dirty.value && agent.value && saved.definition?.assignment) { if (saved.execution_policy?.eligible) emit('saved', saved, 'saved'); else enterConsent(); return }
  const snapshot = copyRecurrenceInput(input.value), id = saved?.id, revision = saved?.revision
  busy.value = true; failure.value = ''
  void scope.run(({ after, signal }) => after(id ? updateRecurrence(id, snapshot, revision!, signal) : createRecurrence(snapshot, signal), result => {
    if (id && !acceptsRecord(base.value, result)) return
    stale.value = null; changed.value = new Set()
    if (!result.definition?.assignment) { emit('saved', result, 'saved'); return }
    resetBaseline(result); emit('changed', result); enterConsent()
  }), { failed: error => { if (isStale(error) && id) reloadStale(id); else failure.value = error instanceof Error ? error.message : 'The routine was not saved.' }, settled: () => { busy.value = false } })
}
// Docs order: resume first, then consent to the revision the resume returned. A failed
// consent is said in words and never retried; a resumed routine offers Pause again.
function letAgentsRun() {
  const saved = base.value, chosen = principal.value
  if (!saved || !consentReady.value || busy.value) return
  busy.value = true; consentFailure.value = ''; consentCode.value = ''
  void scope.run(({ after, signal }) => {
    const consent = (current: Recurrence) => {
      const request = consentRequest(current, chosen)
      if (!request) throw Object.assign(new Error(waitReasonWords('execution_identity_required')), { code: 'execution_identity_required' })
      return after(putRoutineConsent(current.id, request, signal), done => { if (!acceptsRecord(base.value, done)) return; resetBaseline(done); emit('saved', done, 'consented') })
    }
    return saved.paused ? after(pauseRecurrence(saved.id, false, saved.revision, signal), resumed => { if (!acceptsRecord(base.value, resumed)) return; resetBaseline(resumed); emit('changed', resumed); return consent(resumed) }) : consent(saved)
  }, { failed: error => {
    const code = (error as { code?: string }).code ?? (error instanceof Error && /^[a-z_]+$/.test(error.message) ? error.message : '')
    if (isStale(error) && base.value) { reloadStale(base.value.id); return }
    consentCode.value = code; consentFailure.value = code ? waitReasonWords(code, { owner: ownerTitle.value, project: projectKey.value }) : error instanceof Error ? error.message : 'Agents may not run it.'
  }, settled: () => { busy.value = false } })
}
function pauseAgain() {
  const saved = base.value
  if (!saved || pausing.value) return
  pausing.value = true
  void scope.run(({ after, signal }) => after(pauseRecurrence(saved.id, true, saved.revision, signal), paused => { if (!acceptsRecord(base.value, paused)) return; resetBaseline(paused); emit('changed', paused); consentFailure.value = ''; consentCode.value = '' }), { failed: error => { consentFailure.value = error instanceof Error ? error.message : 'It could not be paused.' }, settled: () => { pausing.value = false } })
}
// A stale write (409): the record as it is now. Fields the server changed take its value
// and a soft tint; every other unsaved field is kept, and Save is enabled again.
function reloadStale(id: string) {
  void scope.run(({ after, signal }) => after(getRecurrence(id, signal), server => {
    const shown = base.value
    if (!shown || !acceptsRecord(shown, server)) return
    const fields = staleFields(copyRecurrenceInput(shown), server)
    for (const field of fields) {
      if (field === 'name') template.name = server.template.name ?? ''
      else if (field === 'title') template.title = server.template.title
      else if (field === 'description') template.description = server.template.description
      else if (field === 'priority') template.priority = server.template.priority
      else if (field === 'criteria') criteria.value = server.template.acceptance_criteria.join('\n')
      else if (field === 'estimate') estimate.value = server.template.estimate_hours ? `${server.template.estimate_hours} h` : ''
      else if (field === 'queue') queue.value = server.queue_each
      else if (field === 'overlap') skip.value = server.overlap_policy !== 'create'
      else if (field === 'assignment') { agent.value = !!server.definition?.assignment; draft.value = routineDraft(server.definition?.assignment) }
    }
    const keepSchedule = !fields.includes('trigger')
    if (!keepSchedule) Object.assign(schedule, scheduleFrom(server.trigger))
    base.value = server; policy.value = server.execution_policy ?? policy.value
    if (!keepSchedule || JSON.stringify(schedule) === initialSchedule) initialSchedule = JSON.stringify(schedule)
    savedSnapshot = ''
    changed.value = new Set(fields); stale.value = { actor: '', at: '' }
    return after(recurrenceHistory(id, 'changes', undefined, signal), page => { const last = page.items[0]; if (last && base.value?.id === id) stale.value = { actor: last.actor_name, at: last.at } })
  }), { failed: error => { failure.value = error instanceof Error ? error.message : 'The routine changed elsewhere and could not be read again.' } })
}
function keys(event: KeyboardEvent) {
  if (event.key === 'Enter' && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) && !event.altKey && !event.shiftKey) { event.preventDefault(); save(); return }
  if (event.key === 'Escape') {
    if (picker.value) return
    event.preventDefault(); const target = event.target as HTMLElement
    if (target.matches('input, textarea, select') || target.isContentEditable) body.value?.focus({ preventScroll: true })
    else close()
  }
  if (['ArrowLeft', 'ArrowRight'].includes(event.key) && !event.metaKey && !event.ctrlKey && !event.altKey) {
    const group = (event.target as HTMLElement).closest('[role="radiogroup"]')
    if (!group) return
    const buttons = [...group.querySelectorAll<HTMLButtonElement>('button:not([aria-disabled="true"])')], index = buttons.indexOf(event.target as HTMLButtonElement)
    event.preventDefault(); const button = buttons[(index + (event.key === 'ArrowRight' ? 1 : -1) + buttons.length) % buttons.length]; button?.click(); button?.focus()
  }
}
function chooseParent(value: { id: string; key: string; title: string } | null) { parent.value = value?.id || outputProject.value; parentLabel.value = value ? `${value.key} · ${value.title}` : topLabel; const anchor = picker.value; picker.value = null; anchor?.focus() }
function chooseProject(id: string) { outputProject.value = id; parent.value = id; parentLabel.value = id ? topLabel : 'Pick the project first' }
const projectChoices = computed(() => projects.projects.filter(project => !project.archived && (project.id === outputProject.value || can('recurrences.manage', project.id))))
// The sentence: a read-only summary in a fixed slot, each part a jump to its field.
function jump(target: string) { const el = document.getElementById(`${uid}-${target}`); el?.scrollIntoView({ block: 'center', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' }); (el?.matches('button') ? el : el?.closest('section')?.querySelector<HTMLElement>('input, button, select, textarea'))?.focus({ preventScroll: true }) }
const sentenceTitle = computed(() => {
  const at = times.value[0] || new Date().toISOString(), title = template.title.trim() ? renderTitle(template.title, (base.value?.occurrence_count || 0) + 1, at, recurrenceZone(input.value.trigger), { name: '(next release)', version: '(version)' }) : ''
  return title.length > 48 ? `${title.slice(0, 47)}…` : title
})
const parentKey = computed(() => parent.value && parent.value !== outputProject.value ? parentLabel.value.split(' · ')[0] : '')
const may = computed(() => draft.value.actions.includes('pr.open') ? 'may open a pull request, never merge' : draft.value.actions.length ? `may write ${mayWords(draft.value.actions)}, never merge` : 'stays in its own copy')
const budgetLine = computed(() => budgetWords(assignmentFrom(draft.value, 'x')?.budget).toLowerCase())
const sentenceText = computed(() => `${triggerWords(input.value.trigger).replace(/ · .*$/, '')}, create ${sentenceTitle.value ? `“${sentenceTitle.value}”` : `a ${workNoun(vocabulary.leaf.name)}`} in ${projectKey.value}${scopeKind === 'personal' ? ', just for you' : scopeKind === 'workspace' ? ', for the workspace' : ''}${agent.value ? `, and an agent ${effortVerb(draft.value.role)}: it ${may.value}, ${budgetLine.value}, on a computer ${mine.value ? 'of yours' : `${ownerWords.value} allowed`}.` : `${parentKey.value ? ` under ${parentKey.value}` : ''}. People or the work queue pick it up.`}`)
// Before it runs: what the next run would do, from the same policy the server checks.
const nextRun = computed(() => schedule.kind === 'event' ? 'After the next release' : times.value[0] ? when(times.value[0], schedule.zone) : previewBusy.value ? 'Loading preview…' : 'No preview yet')
const runState = computed(() => {
  const reason = policy.value?.wait_reason ?? ''
  if (step.value === 'consent') return { icon: 'person-wait' as const, text: mine.value ? 'Nothing runs until you let agents run it.' : `Nothing runs until ${ownerWords.value} lets agents run it.` }
  if (reason && reason !== 'execution_consent_required') return { icon: 'pause' as const, text: `Once let run, it creates the ticket, then waits: ${waitReasonWords(reason, { owner: ownerWords.value, project: projectKey.value }).replace(/^\w/, c => c.toLowerCase())}` }
  return { icon: 'info' as const, text: 'Saving starts nothing. After saving you decide whether agents may run it.' }
})
onMounted(() => {
  dialog.value?.showModal(); void nextTick(() => nameField.value?.focus())
  if (scopeKind !== 'project') void projects.load()
  if (parent.value && parent.value !== outputProject.value && !recurrenceSourceIsParent(source)) void scope.run(({ after }) => after(getNode(parent.value), node => { parentLabel.value = `${node.key} · ${node.title}` }), { failed: () => { parentLabel.value = 'Parent unavailable' } })
  if (props.consent && initial?.definition?.assignment) enterConsent()
})
onBeforeUnmount(() => { clearTimeout(timer); dialog.value?.close() })
</script>

<template>
  <Teleport to="body">
    <dialog ref="dialog" class="recurrence-editor" :class="{ 'has-foot': !!primary }" :aria-labelledby="`${uid}-heading`" @cancel.prevent="close" @keydown.stop="keys" @click="event => { if (event.target === dialog) close() }">
      <header class="editor-head">
        <div class="editor-titles"><h2 :id="`${uid}-heading`">{{ heading }}</h2><p>{{ subtitle }}</p></div>
        <div class="editor-actions"><button type="button" class="btn sm ghost" :disabled="busy" @click="close">{{ cancelLabel }}<KeyCap k="esc" /></button><button v-if="primary" type="button" class="btn sm primary save" :disabled="primaryDisabled" :aria-keyshortcuts="mac ? 'Meta+Enter' : 'Control+Enter'" @click="save"><AppIcon v-if="primary === 'consent'" name="play" :size="12" /><span>{{ primaryLabel }}</span><span class="keys"><KeyCap k="mod" /><KeyCap k="enter" /></span></button></div>
      </header>
      <div ref="body" class="editor-body" tabindex="-1">
        <p v-if="stale" class="calm-line" role="status"><AppIcon name="refresh" :size="13" /><span><b>Changed elsewhere; shown as it is now.</b> {{ stale.actor ? `${stale.actor} changed it ${when(stale.at, localZone)}${base ? ` (revision ${base.revision})` : ''}.` : '' }} Your unsaved changes to other fields are kept; save again to apply them.</span></p>
        <RoutineConsent v-if="step === 'consent' && base" v-model:principal="principal" v-model:runs-as="runsAs" :record="base" :mine="mine" :owner-name="ownerTitle" :project="projectKey" :settings="settings" :settings-failed="settingsFailed" :failure="consentFailure" :failure-code="consentCode" :pausing="pausing" @pause-again="pauseAgain" />
        <p class="ed-sentence" :title="sentenceText"><AppIcon name="sentence" :size="14" /><span class="ed-sentence-t"><a :href="`#${uid}-when`" @click.prevent="jump('when')">{{ triggerWords(input.trigger).replace(/ · .*$/, '') }}</a>, create <a :href="`#${uid}-what`" @click.prevent="jump('what')">{{ sentenceTitle ? `“${sentenceTitle}”` : `a ${workNoun(vocabulary.leaf.name)}` }}</a> in <b>{{ projectKey }}</b>{{ scopeKind === 'personal' ? ', just for you' : scopeKind === 'workspace' ? ', for the workspace' : '' }}<template v-if="agent">, and <a :href="`#${uid}-who`" @click.prevent="jump('who')">an agent {{ effortVerb(draft.role) }}</a>: it <a :href="`#${uid}-f-rules`" @click.prevent="jump('f-rules')">{{ may }}</a>, <a :href="`#${uid}-f-budget`" @click.prevent="jump('f-budget')">{{ budgetLine }}</a>, <a :href="`#${uid}-f-needs`" @click.prevent="jump('f-needs')">on a computer {{ mine ? 'of yours' : `${ownerWords} allowed` }}</a>.</template><template v-else>{{ parentKey ? ` under ${parentKey}` : '' }}. <a :href="`#${uid}-who`" @click.prevent="jump('who')">People or the work queue pick it up</a>.</template></span></p>
        <template v-for="part in step === 'consent' ? ['preview', 'form'] : ['form', 'preview']" :key="part">
          <section v-if="part === 'preview'" :id="`${uid}-next-section`" class="editor-section preview" :aria-labelledby="`${uid}-next`" aria-live="polite"><div class="preview-head"><p :id="`${uid}-next`" class="eyebrow">{{ agent ? 'Before it runs' : 'Next' }}</p><span class="small">{{ triggerWords(input.trigger) }}</span></div>
            <div v-if="agent" class="pv-card">
              <dl class="pv-grid"><div><dt>Next run</dt><dd><b>{{ nextRun }}</b></dd></div><div><dt>Creates</dt><dd>{{ sentenceTitle ? `“${sentenceTitle}”` : `a ${workNoun(vocabulary.leaf.name)}` }} in {{ projectKey }}</dd></div><div><dt>Who</dt><dd>{{ routeState === 'ready' && route ? `${route.model} via ${HARNESS_LABEL[route.harness] ?? route.harness}` : `The ${effortWord(draft.role)} model` }} {{ effortVerb(draft.role) }}</dd></div><div><dt>May</dt><dd>{{ draft.actions.includes('pr.open') ? 'open a pull request; may not merge' : `${mayWords(draft.actions)}; never merges` }}</dd></div><div><dt>Stops at</dt><dd :class="{ bad: policy?.wait_reason === 'budget_mode_unqualified' }">{{ draft.mode === 'off' ? 'no token or money limit; daily caps and time limits' : budgetLine.replace(/^stops at /, '').replace(/ a run$/, '') }}{{ policy?.wait_reason === 'budget_mode_unqualified' ? ' · can’t be enforced yet, so it won’t start' : '' }}</dd></div><div><dt>Runs on</dt><dd :class="{ bad: policy?.wait_reason === 'capability_unqualified' }">{{ policy?.wait_reason === 'capability_unqualified' ? `a computer ${draft.browser ? 'with a browser' : 'for coding'} · none qualified yet` : `a qualified computer ${mine ? 'you' : ownerWords} allowed` }}</dd></div></dl>
              <p class="pv-state"><AppIcon :name="runState.icon" :size="13" /><span>{{ runState.text }}</span></p>
            </div>
            <ol class="preview-rows"><li v-for="index in 4" :key="index"><template v-if="schedule.kind === 'time'"><span class="when">{{ times[index - 1] ? when(times[index - 1]!, schedule.zone) : index === 1 ? previewBusy ? 'Loading preview…' : 'No preview yet' : '—' }}</span><span class="what">{{ times[index - 1] ? base?.open_previous && skip && index === 1 ? `Skipped if #${base.open_previous.number} is still open` : previewTitle(times[index - 1]!, index - 1) : '' }}</span></template><template v-else><span class="when">{{ index === 1 ? 'Next release' : 'Following release' }}</span><span class="what">{{ renderTitle(template.title, (base?.occurrence_count || 0) + index, new Date().toISOString(), schedule.zone, { name: '(next release)', version: '(version)' }) }}</span></template></li></ol>
            <ul class="notes"><li v-for="problem in problems" :key="problem" class="warning"><AppIcon name="alert" :size="13" /><span>{{ problem }}</span></li><li v-if="failure || previewError" class="warning" role="alert"><AppIcon name="alert" :size="13" /><span>{{ failure || previewError }}</span></li><li v-if="daylightNote"><AppIcon name="clock" :size="13" /><span>{{ daylightNote }}</span></li><li v-if="schedule.kind === 'time'"><AppIcon name="clock" :size="13" /><span>Nonexistent times when clocks move forward are skipped. Repeated times run once, at the first instant.</span></li><li v-if="schedule.kind === 'time' && schedule.frequency === 'MONTHLY'"><AppIcon name="calendar" :size="13" /><span>{{ schedule.day === '-1' ? 'Last day follows each month, including February.' : 'Months without the selected day are skipped.' }}</span></li><li v-if="schedule.kind === 'event'"><AppIcon name="info" :size="13" /><span>Publication dates cannot be predicted. Past releases are not created automatically.</span></li></ul>
            <p class="editor-hint"><KeyCap k="mod" /><KeyCap k="enter" /> {{ primary === 'consent' ? 'let agents run it' : base ? 'save' : 'create' }} · <KeyCap k="esc" /> leaves a field, then closes</p>
          </section>
          <div v-else class="form-wrap" :class="{ folded: step === 'consent' }">
            <button v-if="step === 'consent'" :id="`${uid}-f-form`" type="button" class="fold-row" :aria-expanded="formOpen" :aria-controls="`${uid}-fb-form`" @click="formOpen = !formOpen"><AppIcon name="chevron-right" :size="13" /><span class="fr-ic"><AppIcon name="edit" :size="15" /></span><span class="fr-t">The routine itself</span><span class="fr-s">What, where, when and who · editing asks again</span></button>
            <div :id="`${uid}-fb-form`" class="fold" :class="{ open: step !== 'consent' || formOpen }" :inert="step === 'consent' && !formOpen"><div class="fold-in"><div class="form">
              <section :id="`${uid}-what-section`" class="editor-section" :aria-labelledby="`${uid}-what`"><p :id="`${uid}-what`" class="eyebrow">What</p>
                <div class="field-row"><label :for="`${uid}-name`">Name</label><input :id="`${uid}-name`" ref="nameField" v-model="template.name" class="field" :class="{ changed: changed.has('name') }" maxlength="80" placeholder="e.g. Weekly tool sweep" /></div>
                <div class="field-row top"><span :id="`${uid}-title`" class="field-label">Title</span><TemplateTitle v-model="template.title" :class="{ changed: changed.has('title') }" :event="schedule.kind === 'event' && input.trigger.event === 'release.published'" :label-id="`${uid}-title`" /></div>
                <div class="field-row"><span class="field-label">{{ vocabulary.leaf.name }}</span><div class="field-line"><select v-model="template.priority" class="field priority" :class="{ changed: changed.has('priority') }" aria-label="Priority"><option v-for="value in ['critical', 'high', 'medium', 'low']" :key="value" :value="value">{{ value[0]!.toUpperCase() + value.slice(1) }}</option></select><input v-model="estimate" class="field estimate" :class="{ changed: changed.has('estimate') }" aria-label="Estimate" placeholder="Estimate" /></div></div>
                <div class="field-row top"><label :for="`${uid}-description`">Description</label><textarea :id="`${uid}-description`" v-model="template.description" class="field" :class="{ changed: changed.has('description') }" rows="3" maxlength="65536" :placeholder="agent ? 'What the agent does each time, in plain words' : 'What each run is about'" /></div>
                <div class="field-row top"><label :for="`${uid}-criteria`">Criteria</label><textarea :id="`${uid}-criteria`" v-model="criteria" class="field mono" :class="{ changed: changed.has('criteria') }" rows="3" placeholder="- [ ] What must be true when it is done" /></div>
              </section>
              <section class="editor-section" :aria-labelledby="`${uid}-where`"><p :id="`${uid}-where`" class="eyebrow">Where</p>
                <div v-if="scopeKind !== 'project'" class="field-row"><label :for="`${uid}-project`">Project</label><select :id="`${uid}-project`" class="field" :value="outputProject" :disabled="!!base" :data-tip="base ? 'The project is fixed for an existing routine' : undefined" @change="chooseProject(($event.target as HTMLSelectElement).value)"><option value="" disabled>Pick the project its tickets go to</option><option v-for="choice in projectChoices" :key="choice.id" :value="choice.id">{{ choice.routeKey }} · {{ choice.title }}</option></select></div>
                <div class="field-row"><span class="field-label">Parent</span><button type="button" class="field parent" aria-label="Parent" :disabled="!!base || !outputProject" :data-tip="base ? 'The parent is fixed for an existing routine' : undefined" aria-haspopup="dialog" :aria-expanded="!!picker" @click="picker = $event.currentTarget as HTMLElement"><AppIcon :name="parent === outputProject ? 'folder' : 'epic'" :size="13" /><span>{{ parentLabel }}</span><AppIcon name="chevron" :size="12" /></button></div>
                <p v-if="scopeKind !== 'project'" class="hint pad">{{ scopeKind === 'personal' ? 'Only you see this routine. Its tickets go to the project above, where people who can see the project see them.' : 'Everyone in the workspace sees this routine. Its tickets go to the project above.' }}</p>
                <div v-if="scopeKind !== 'personal' && agent && !mine" class="field-row"><span class="field-label">Owner</span><p class="owner-line"><b>{{ ownerTitle }}</b><span class="hint">{{ scopeKind === 'workspace' ? 'Workspace routines belong to a workspace admin.' : `Project routines belong to a ${projectKey} admin.` }} They let agents run it.</span></p></div>
              </section>
              <section :id="`${uid}-when-section`" class="editor-section" :aria-labelledby="`${uid}-when`"><p :id="`${uid}-when`" class="eyebrow">When</p>
                <div class="field-row"><span class="field-label">Trigger</span><div class="seg" :class="{ changed: changed.has('trigger') }" role="radiogroup" aria-label="Trigger"><button type="button" role="radio" :aria-checked="schedule.kind === 'time'" @click="schedule.kind = 'time'"><AppIcon name="calendar" :size="13" />Schedule</button><button type="button" role="radio" :aria-checked="schedule.kind === 'event'" @click="schedule.kind = 'event'"><AppIcon name="journey" :size="13" />Event</button></div></div>
                <div class="trigger-slot">
                  <div class="trigger-pane" :aria-hidden="schedule.kind !== 'time'" :inert="schedule.kind !== 'time'">
                    <div class="field-row"><span class="field-label">Repeat</span><div class="seg" role="radiogroup" aria-label="Repeat"><button v-for="frequency in ['DAILY', 'WEEKLY', 'MONTHLY']" :key="frequency" type="button" role="radio" :aria-checked="schedule.frequency === frequency" @click="schedule.frequency = frequency">{{ frequency[0] + frequency.slice(1).toLowerCase() }}</button></div></div>
                    <div class="field-row"><span class="field-label">On</span><div class="day-slot">
                      <p :aria-hidden="schedule.frequency !== 'DAILY'" :inert="schedule.frequency !== 'DAILY'">Every day, weekends included</p>
                      <div class="seg weekdays" role="group" aria-label="Weekdays" :aria-hidden="schedule.frequency !== 'WEEKLY'" :inert="schedule.frequency !== 'WEEKLY'"><button v-for="[day, label] in weekdays" :key="day" type="button" :aria-label="label" :aria-pressed="schedule.days.includes(day)" @click="schedule.days = schedule.days.includes(day) ? schedule.days.filter(d => d !== day) : [...schedule.days, day]">{{ label.slice(0, 2) }}</button></div>
                      <div class="field-line" :aria-hidden="schedule.frequency !== 'MONTHLY'" :inert="schedule.frequency !== 'MONTHLY'"><select v-model="schedule.interval" class="field interval" aria-label="Every"><option v-for="n in [1, 2, 3, 6]" :key="n" :value="String(n)">{{ n === 1 ? 'Every month' : `Every ${n} months` }}</option></select><span class="small">on the</span><select v-model="schedule.day" class="field month-day" aria-label="Day of the month"><option v-for="day in 31" :key="day" :value="String(day)">{{ day }}</option><option value="-1">Last day</option><option v-if="!Array.from({ length: 31 }, (_, i) => String(i + 1)).includes(schedule.day) && schedule.day !== '-1'" :value="schedule.day">Days {{ schedule.day }} (existing rule)</option></select></div>
                    </div></div>
                    <div class="field-row"><label :for="`${uid}-time`">At</label><div class="field-line zone-line"><input :id="`${uid}-time`" v-model="schedule.time" class="field time" :class="{ changed: changed.has('trigger') }" type="time" /><select v-model="schedule.zone" class="field zone" aria-label="Time zone"><option v-for="zone in zoneOptions" :key="zone" :value="zone">{{ zone }}</option></select><span class="zone-note">{{ zoneNote }}</span></div></div>
                  </div>
                  <div class="trigger-pane" :aria-hidden="schedule.kind !== 'event'" :inert="schedule.kind !== 'event'">
                    <div class="field-row"><label :for="`${uid}-event`">After</label><select :id="`${uid}-event`" class="field event"><option>{{ triggerWords({ kind: 'event', event: base?.trigger.event || 'release.published' }).replace(/^After /, '') }}</option></select></div>
                    <div class="field-row"><span class="field-label">Start</span><div class="seg" role="radiogroup" aria-label="Start"><button v-for="[value, label] in ([['now', 'Right away'], ['hour', '1 hour later'], ['morning', 'Next morning']] as const)" :key="value" type="button" role="radio" :aria-checked="schedule.start === value" @click="schedule.start = value">{{ label }}</button></div></div>
                    <div class="field-row"><span class="field-label">{{ input.trigger.event === 'release.published' ? 'Release' : 'Event' }}</span><div class="release-note"><span v-if="input.trigger.event === 'release.published'">The Release token becomes its marketing name, e.g. Sunlit Sonde.</span><span v-else>Source identifiers are included in each created ticket.</span><span v-if="schedule.start === 'morning'">Next morning means 06:00 in {{ schedule.zone }}.</span></div></div>
                  </div>
                </div>
              </section>
              <WhoDoesIt v-model:agent="agent" v-model:draft="draft" :project-id="outputProject" :project="projectKey" :unavailable="agentUnavailable" :kinds="kinds" :kinds-state="kindsState" :route="route" :route-state="routeState" :policy="policy" :rules="rules" :rules-state="rulesState" :owner="ownerTitle" :mine="mine" :fresh="!base?.definition?.assignment" :id-prefix="uid" />
              <section class="editor-section" :aria-labelledby="`${uid}-options`"><p :id="`${uid}-options`" class="eyebrow">Options</p><label class="option" :class="{ locked: agent }"><input :checked="agent || queue" type="checkbox" :disabled="agent" @change="queue = ($event.target as HTMLInputElement).checked" /><span><b>Put each one into the work queue</b><small>{{ agent ? 'Always on when an agent works on it: the agent picks it up from the queue. It needs an estimate and criteria.' : 'Agents pick it up in queue order; it needs an estimate and criteria.' }}</small></span></label><label class="option"><input v-model="skip" type="checkbox" /><span><b>Skip while the previous one is still open</b><small>Otherwise the next one is created anyway.</small></span></label><p class="fixed-rule"><AppIcon name="info" :size="13" />After downtime, at most one missed run is created, never all at once.</p></section>
            </div></div></div>
          </div>
        </template>
      </div>
      <footer v-if="primary" class="editor-foot"><button type="button" class="btn primary" :disabled="primaryDisabled" @click="save"><AppIcon v-if="primary === 'consent'" name="play" :size="13" />{{ primaryLabel }}</button></footer>
      <EpicPicker v-if="picker" :anchor="picker" :project-id="outputProject" :current="parent === outputProject ? null : parent" subject="routine" allow-none @choose="chooseParent" @close="restore => { const anchor = picker; picker = null; if (restore) anchor?.focus() }" />
    </dialog>
  </Teleport>
</template>

<style scoped>
.recurrence-editor { position: fixed; inset: auto; top: 64px; left: 50%; transform: translateX(-50%); margin: 0; padding: 0; width: min(var(--dialog-l), calc(100vw - 32px)); max-height: calc(100dvh - 80px); color: var(--ink); border: 1px solid var(--glass-edge); border-radius: 16px; background: var(--surface-raised); box-shadow: var(--shadow-pop); overflow: hidden; }
.recurrence-editor[open] { display: flex; flex-direction: column; }
.recurrence-editor::backdrop { background: var(--scrim); }
.editor-head { display: flex; align-items: center; gap: 10px; flex: none; height: 60px; padding: 10px 14px 10px 20px; border-bottom: 1px solid var(--line); }
.editor-titles { flex: 1; min-width: 0; }.editor-titles h2 { font: 600 16px/1.3 var(--font); }.editor-titles p { font-size: 12.5px; color: var(--ink-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.editor-actions { display: flex; gap: 8px; flex: none; }.save { min-width: 112px; }.keys { display: inline-flex; gap: 2px; }
.editor-body { overflow: auto; overscroll-behavior: contain; overflow-anchor: auto; padding: 4px 20px 18px; }
.editor-foot { display: none; }
.editor-section { min-width: 0; display: grid; gap: 10px; padding: 16px 0; border-top: 1px solid var(--line); }.form > .editor-section:first-child { border: 0; }
.field-row { display: grid; grid-template-columns: 84px minmax(0, 1fr); gap: 12px; align-items: center; min-height: 34px; }.field-row > label, .field-label { font-size: 12.5px; color: var(--ink-3); }.field-row.top { align-items: start; }.top > label, .top > .field-label { padding-top: 8px; }
textarea.field { height: auto; min-height: 70px; line-height: 1.5; resize: vertical; }.field { font-size: 13px; min-width: 0; }.field-row > .seg { justify-self: start; }.field-line { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 10px; min-width: 0; }.priority { width: 112px; }.estimate { width: 100px; }.time { width: 120px; }.zone { flex: 1; width: 200px; }.interval { width: 140px; }.month-day { width: 105px; }.event { width: 260px; }
.parent { display: flex; align-items: center; gap: 8px; text-align: left; }.parent span { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.trigger-slot, .day-slot { display: grid; }.trigger-pane { grid-area: 1/1; display: grid; gap: 10px; align-content: start; }.day-slot > * { grid-area: 1/1; align-self: center; }.day-slot > .seg { justify-self: start; }[aria-hidden="true"] { visibility: hidden; }
.weekdays button { min-width: 34px; padding: 0 7px; }.seg button[aria-pressed="true"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--glass-edge); }.zone-note { flex-basis: 100%; height: 18px; color: var(--ink-3); font-size: 12px; }.small { font-size: 12px; color: var(--ink-3); }.release-note { display: grid; gap: 3px; height: 60px; align-content: start; font-size: 12px; color: var(--ink-3); }
.option { display: grid; grid-template-columns: 18px minmax(0, 1fr); gap: 10px; cursor: pointer; }.option input { margin: 3px 0 0; accent-color: var(--teal); }.option b { display: block; font-size: 13px; font-weight: 600; }.option small { display: block; font-size: 12px; color: var(--ink-3); }.option.locked { cursor: default; }.fixed-rule { display: flex; align-items: center; gap: 8px; padding-left: 28px; font-size: 12px; color: var(--ink-3); }
.preview-head { min-width: 0; display: flex; gap: 12px; align-items: baseline; justify-content: space-between; }.preview-head .small { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.preview-rows { padding: 0; list-style: none; }.preview-rows li { display: grid; grid-template-columns: 216px minmax(0, 1fr); align-items: center; gap: 12px; height: 32px; border-top: 1px solid var(--line); font-size: 13px; }.preview-rows li:first-child { border: 0; }.when { font-variant-numeric: tabular-nums; white-space: nowrap; }.what { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); }
.notes { list-style: none; padding: 0; display: grid; gap: 6px; }.notes li { display: grid; grid-template-columns: 14px minmax(0, 1fr); gap: 8px; font-size: 12.5px; color: var(--ink-2); }.notes svg { margin-top: 2px; }.notes .warning { color: var(--warn-ink); }.editor-hint { display: flex; align-items: center; gap: 4px; font-size: 12px; color: var(--ink-3); }
/* AEON-1182: the sentence keeps one fixed slot, so typing or switching an option never moves a control below it. */
.ed-sentence { display: flex; align-items: center; gap: 10px; height: 92px; margin: 12px 0 0; padding: 12px 14px; border-radius: 12px; background: var(--surface-sunken); font-size: 14px; line-height: 1.6; color: var(--ink); }
.ed-sentence > svg { flex: none; color: var(--ink-3); }
.ed-sentence-t { min-width: 0; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 3; line-clamp: 3; overflow: hidden; overflow-wrap: anywhere; }
.ed-sentence a { color: var(--ink); text-decoration: underline; text-decoration-color: var(--line-2); text-underline-offset: 3px; }
.ed-sentence a:hover { color: var(--teal-ink); text-decoration-color: currentColor; }
.ed-sentence b { font-weight: 600; }
.field.changed, .seg.changed, .changed :deep(.field) { background: var(--queue-wait-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--queue-wait-line); }
.calm-line { display: flex; align-items: flex-start; gap: 8px; margin-top: 12px; padding: 10px 12px; border-radius: 10px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); background: var(--surface-sunken); }
.calm-line > svg { flex: none; margin-top: 2px; }
.calm-line b { color: var(--ink); font-weight: 600; }
.hint { font-size: 12px; line-height: 1.45; color: var(--ink-3); }
.hint.pad { padding-left: 96px; }
.owner-line { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 10px; font-size: 13px; }
.owner-line b { font-weight: 600; }
/* Before it runs: what the next run would do, above the same four dates. */
.pv-card { display: grid; gap: 8px; padding: 14px 16px; border-radius: 12px; background: var(--surface-raised-2); box-shadow: inset 0 0 0 1px var(--line); }
.pv-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0 24px; margin: 0; }
.pv-grid > div { display: grid; grid-template-columns: 76px minmax(0, 1fr); gap: 10px; align-items: baseline; padding: 6px 0; font-size: 13px; }
.pv-grid > div:nth-child(n + 3) { border-top: 1px solid var(--line); }
.pv-grid dt { font: 500 10px/1.5 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.pv-grid dd { margin: 0; min-width: 0; color: var(--ink); line-height: 1.45; overflow-wrap: anywhere; }
.pv-grid dd b { font-weight: 600; }
.pv-grid dd.bad { color: var(--danger); font-weight: 600; }
.pv-state { display: flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--ink-2); }
.pv-state svg { flex: none; }
/* Consent: the form folds away behind "The routine itself", because any edit asks again. */
.form-wrap { display: grid; }
.form-wrap:not(.folded) > .fold > .fold-in { overflow: visible; }
.form-wrap.folded { margin-top: 12px; border: 1px solid var(--line); border-radius: 14px; background: var(--surface-raised-2); overflow: hidden; }
.form-wrap.folded .form { padding: 0 16px 8px; border-top: 1px solid var(--line); }
.fold-row { display: grid; grid-template-columns: 14px 28px auto minmax(0, 1fr); align-items: center; gap: 10px; width: 100%; min-height: 52px; padding: 8px 14px; border: 0; background: transparent; color: var(--ink); text-align: left; }
@media (hover: hover) { .fold-row:hover { background: var(--row-hover); } }
.fold-row[aria-expanded="true"] { background: var(--row-selected); }
.fold-row > svg:first-child { color: var(--ink-3); transition: transform .2s ease; }
.fold-row[aria-expanded="true"] > svg:first-child { transform: rotate(90deg); }
.fr-ic { display: grid; place-items: center; width: 28px; height: 28px; border-radius: 8px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); }
.fr-t { font-size: 13.5px; font-weight: 650; white-space: nowrap; }
.fr-s { min-width: 0; text-align: right; font-size: 12.5px; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fold { display: grid; grid-template-rows: 0fr; transition: grid-template-rows .24s ease; }
.fold.open { grid-template-rows: 1fr; }
.fold-in { min-height: 0; overflow: hidden; }
.fold:not(.open) .fold-in { visibility: hidden; }
@media (prefers-reduced-motion: reduce) { .fold, .fold-row > svg:first-child { transition: none; } }
@media (max-width: 600px) {
  .recurrence-editor { top: 8px; left: 0; transform: none; width: 100%; max-width: none; height: calc(100dvh - 8px); max-height: none; border-radius: 16px 16px 0 0; border-bottom: 0; }.editor-head { padding: 8px 10px 8px 16px; }.editor-actions :deep(.keycap) { display: none; }.save { min-width: 84px; }.editor-body { flex: 1 1 auto; min-height: 0; padding: 0 16px 24px; }.field-row { grid-template-columns: minmax(0, 1fr); gap: 6px; }.top > label, .top > .field-label { padding: 0; }.zone { min-width: 150px; }.event { width: 100%; }.weekdays button { min-width: 34px; }.release-note { height: 68px; }.preview-rows li { grid-template-columns: minmax(0, 1fr); height: 50px; gap: 0; align-content: center; }.editor-hint { display: none; }
  /* The phone sheet: the primary lives in a footer pinned above the safe area. */
  .has-foot .editor-actions .save { display: none; }
  .editor-foot { display: flex; flex: none; padding: 10px 16px calc(10px + env(safe-area-inset-bottom, 0px)); border-top: 1px solid var(--line); background: var(--surface-raised); }
  .editor-foot .btn { flex: 1; min-height: 44px; }
  .ed-sentence { height: 150px; font-size: 13.5px; }
  .ed-sentence-t { -webkit-line-clamp: 5; line-clamp: 5; }
  .hint.pad { padding-left: 0; }
  .pv-grid { grid-template-columns: minmax(0, 1fr); }
  .pv-grid > div:nth-child(n + 2) { border-top: 1px solid var(--line); }
  .fold-row { grid-template-columns: 14px 28px minmax(0, 1fr); }
  .fr-s { grid-column: 3; text-align: left; white-space: normal; }
}
</style>
