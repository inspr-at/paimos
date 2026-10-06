<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useRouter } from 'vue-router'
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, useId, watch, watchEffect } from 'vue'
import { APIError, getRecurrence, undoEvent, type ListItem } from '../../lib/api'
import { confirmAction } from '../../lib/confirm'
import { rowStore } from '../../lib/rowStore'
import { liveNodes } from '../../lib/liveNodes'
import { etaFromTicket } from '../../lib/eta'
import { toast } from '../../lib/toast'
import { queueable } from '../../lib/workQueue'
import SuggestedReleaseCell from './SuggestedReleaseCell.vue'
import EtaCell from './EtaCell.vue'
import { useActivity } from '../../lib/useActivity'
import { useTicket, type RelatedNode, type TicketChange } from '../../lib/useTicket'
import { absoluteTime, priorityLabel, relativeTime, statusMeta, statusOptions } from '../../lib/work'
import { workLabel, workNoun } from '../../lib/workVocabulary'
import { useWorkVocabulary } from '../../stores/workVocabulary'
import { useAttachments } from '../../lib/useAttachments'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import ActivityTimeline from './ActivityTimeline.vue'
import AttachmentLightbox from './AttachmentLightbox.vue'
import AttachmentStrip from './AttachmentStrip.vue'
import MarkdownEditor from './MarkdownEditor.vue'
import ChildList from './ChildList.vue'
import ConvertKindSheet from './ConvertKindSheet.vue'
import CommentComposer from './CommentComposer.vue'
import EpicPicker from './EpicPicker.vue'
import InlineTitle from './InlineTitle.vue'
import MarkdownSection from './MarkdownSection.vue'
import OptionMenu, { type MenuOption } from './OptionMenu.vue'
import PersonAvatar from './PersonAvatar.vue'
import PriorityIcon from './PriorityIcon.vue'
import StatusIcon from './StatusIcon.vue'
import StatusMenu from './StatusMenu.vue'
import HumanCheck from './HumanCheck.vue'
import RelationList from './RelationList.vue'
import RelationPicker from './RelationPicker.vue'
import TicketAgentWork from './TicketAgentWork.vue'
import TicketOutcomes from './TicketOutcomes.vue'
import TicketReviews from './TicketReviews.vue'
import TicketHeaderBar from './TicketHeaderBar.vue'
import TicketProperties from './TicketProperties.vue'
import TicketBenefits from './TicketBenefits.vue'
import TicketExtensions from './TicketExtensions.vue'
import { needsBenefitPrompt } from '../../lib/doneGate'
import { benefitDraft, benefitTextKeys, completedTicketState, firstBenefitGap } from '../../lib/ticketBenefits'
import { can } from '../../lib/authz'
import { AssignCancelled, assignToRelease, type ReleaseTarget } from '../../lib/releaseAssign'
import { openedMembershipMessage, releaseViewIsParent, type NativeReleaseView } from '../../lib/releaseMembership'
import ReleasePicker from './ReleasePicker.vue'
import { useJourney } from '../../stores/journey'
import QueueAction from './QueueAction.vue'
import WorkLifecycleSheet from './WorkLifecycleSheet.vue'
import QueueDetails from './QueueDetails.vue'
import QueueView from './QueueView.vue'
import AssigneeMenu from './AssigneeMenu.vue'
import { useWorkQueue } from '../../stores/workQueue'
import { usePoller } from '../../lib/usePolledData'
import { useSession } from '../../stores/session'
import RecurrenceEditor from '../recurrences/RecurrenceEditor.vue'
import RecurrenceProvenance from '../recurrences/RecurrenceProvenance.vue'
import RecurringPill from '../recurrences/RecurringPill.vue'
import { criteriaText, recurrenceName, type Recurrence } from '../../lib/recurrences'
import { useIdentityScope } from '../../lib/useIdentityScope'

// The ticket workspace: the same parts in the docked side panel and in the
// full page. Every write goes through useTicket (precondition, conflicts).
const props = defineProps<{
  item: ListItem | null; ticketKey: string; resolving: boolean; resolveError: string
  position: { index: number; count: number } | null; now: number; mode: 'panel' | 'full'
  project: { id: string; routeKey: string }; names: Map<string, string>
  me: { id: string; name: string } | null; canWrite: boolean; canDelete: boolean; canMove: boolean; canLink: boolean; canUnlink: boolean
  canComment: boolean; canDeleteComment: boolean; canAttach: boolean; people: { id: string; name: string }[]
  nativeReleases?: Map<string, NativeReleaseView>
  // Tickets followed to get here, oldest first (the panel's back trail).
  trail?: string[]
  // Set by a peek dock: a labeled jump to the project, and a return to the view underneath.
  openInProject?: boolean; backLabel?: string
}>()
const emit = defineEmits<{
  close: []; prev: []; next: []; expand: []; collapse: []; newTab: []; openKey: [key: string, newTab: boolean]; status: [anchor: HTMLElement]; trailBack: [steps: number]
  removed: [item: ListItem]; created: [item: ListItem]; moved: [item: ListItem, fromParent: string | null]; assigned: []; retry: []; openInProject: []
}>()

// The row store's display object for this ticket (the list's row when the
// list shows it): one object per node, so every read and write lands on what
// the panel shows (AEON-326).
const item = computed(() => {
  const given = props.item
  if (!given) return null
  return rowStore.row(given.id) ?? rowStore.adopt(given, 0, { full: false }) ?? given
})
// Set below from the editors: while one is open, changes by others wait.
const liveBusy = ref(false)
const ticket = useTicket(item, {
  names: props.names,
  onRemoved: removed => emit('removed', removed),
  onCreated: created => emit('created', created),
  onMoved: (moved, fromParent) => emit('moved', moved, fromParent),
  live: { busy: liveBusy, me: () => props.me?.id ?? null },
})
const activity = useActivity(computed(() => props.item?.id ?? null))
// Draft callbacks retain the record that supplied them, even if a caller holds
// a callback past a render or a confirmation. Mismatched saves are refused.
const record = computed(() => {
  const id = item.value?.id ?? null
  const setField = (name: string, value: string) => ticket.patch({ fields: { [name]: value || undefined } }, undefined, id)
  return {
    setTitle: (title: string) => ticket.patch({ title }, undefined, id),
    setBody: (body: string) => ticket.patch({ body }, undefined, id),
    setAcceptance: (value: string) => setField('acceptance_criteria', value),
    setNotes: (value: string) => setField('notes', value),
    addComment: (body: string) => activity.add(body, id ?? null),
  }
})
const eta = computed(() => etaFromTicket(item.value?.eta))
const etaConnectionStale = ref(liveNodes.state !== 'live')
onBeforeUnmount(liveNodes.onState(state => { etaConnectionStale.value = state !== 'live' }))
const convertOpen = ref(false)
const workActionsOpen = ref(false)
watch(() => [props.item?.id, props.me?.id], () => { workActionsOpen.value = false })
function workLifecycleCompleted() { void ticket.refresh(); activity.load() }
const header = ref<{ focusMore: () => void } | null>(null)
function closeWorkActions() {
  workActionsOpen.value = false
  void nextTick(() => header.value?.focusMore())
}
function finishConvert() {
  convertOpen.value = false
  if (item.value) toast(`${item.value.key} is now ${workNoun(workLabel(item.value, vocabulary.value))}`)
  activity.load()
}
function closeConvert() {
  convertOpen.value = false
  void nextTick(() => header.value?.focusMore())
}
const editable = computed(() => props.canWrite && !ticket.readOnly.value && !ticket.gone.value)
const session = useSession()
const vocabulary = useWorkVocabulary()
const repeatSource = ref<ListItem | null>(null)
const originRecurrence = ref<Recurrence | null>(null), recurrenceEdit = ref<Recurrence | null>(null)
const mayRepeat = computed(() => !!item.value && ['work', 'epic', 'ticket', 'task'].includes(item.value.kind_slug) && !ticket.gone.value && !ticket.readOnly.value && can('recurrences.manage', props.project.id))
const sourceProject = computed(() => item.value?.recurrence ? { id: item.value.recurrence.project_id, routeKey: item.value.recurrence.project_key } : null)
const mayEditRecurrence = computed(() => !!item.value?.recurrence && !item.value.recurrence.retired && can('recurrences.manage', item.value.recurrence.project_id))
const recurrenceScope = useIdentityScope()
watch([() => props.item?.id, () => props.project.id, () => item.value?.recurrence?.id, () => item.value?.recurrence?.project_id, () => item.value?.recurrence?.retired, () => recurrenceScope.owner.value], () => { recurrenceScope.reset(); repeatSource.value = null; recurrenceEdit.value = null; originRecurrence.value = null }, { flush: 'sync' })
// Source details remain valid while permissions load or change. Losing management
// access still cancels pending edits and closes the editor synchronously.
watch(mayEditRecurrence, value => { if (!value) { recurrenceScope.reset(); recurrenceEdit.value = null } }, { flush: 'sync' })
watch(mayRepeat, value => { if (!value) repeatSource.value = null })
function repeat() { if (mayRepeat.value && item.value) repeatSource.value = { ...item.value, fields: { ...item.value.fields } } }
function editRecurrence() {
  const recordId = item.value?.id, id = originRecurrence.value?.id
  const projectId = item.value?.recurrence?.project_id
  if (!mayEditRecurrence.value || !id || !projectId) return
  void recurrenceScope.run(({ after, signal }) => after(getRecurrence(id, signal), latest => {
    if (item.value && item.value.id === recordId && item.value.recurrence?.id === id && item.value.recurrence.project_id === projectId && latest.id === id && latest.project_id === projectId && mayEditRecurrence.value) recurrenceEdit.value = latest
  }), { failed: error => toast(error instanceof Error ? error.message : 'Recurring work unavailable.', { tone: 'error' }) })
}
function originLoaded(value: Recurrence) { if (item.value?.recurrence?.id === value.id && item.value.recurrence.project_id === value.project_id && !item.value.recurrence.retired) originRecurrence.value = value }
function recurrenceSaved(result: Recurrence) {
  const edited = !!recurrenceEdit.value
  repeatSource.value = null; recurrenceEdit.value = null
  const owner = recurrenceScope.owner.value, projectId = props.project.id, routeKey = edited ? sourceProject.value?.routeKey : props.project.routeKey
  if (edited) originRecurrence.value = result
  toast(`${edited ? 'Saved' : 'Created'} ${recurrenceName(result)}.`, { action: { label: 'Show', run: () => { if (routeKey && owner && owner === recurrenceScope.owner.value && props.project.id === projectId) void queueRouter.push({ path: `/p/${encodeURIComponent(routeKey)}/settings`, query: { recurrence: result.id } }) } } })
}
const humanCheckPerson = computed(() => session.identity?.principal.kind === 'person')
const humanCheckEditable = computed(() => humanCheckPerson.value || !item.value?.fields.human_check_completed)
const deletable = computed(() => props.canDelete && !ticket.readOnly.value && !ticket.gone.value)
const movable = computed(() => props.canMove && !ticket.readOnly.value && !ticket.gone.value)
const linkable = computed(() => props.canLink && !ticket.readOnly.value && !ticket.gone.value)
const unlinkable = computed(() => props.canUnlink && !ticket.readOnly.value && !ticket.gone.value)
const commentable = computed(() => props.canComment && !ticket.readOnly.value && !ticket.gone.value)
const commentDeletable = computed(() => props.canDeleteComment && !ticket.readOnly.value && !ticket.gone.value)
const attachable = computed(() => props.canAttach && !ticket.readOnly.value && !ticket.gone.value)
const attachments = useAttachments(computed(() => props.item?.id ?? null))
const lightbox = ref<InstanceType<typeof AttachmentLightbox>>()
const queueRouter = useRouter()
function routerToQueued() { return queueRouter.push({ path: `/p/${encodeURIComponent(props.project.routeKey)}/tickets`, query: { status: 'queued' } }) }
const queue = useWorkQueue()
const queueAction = ref<InstanceType<typeof QueueAction>>()
const queueAnchor = ref<HTMLElement | null>(null)
const queueEntry = computed(() => item.value && queueable(item.value) ? queue.entry(props.project.id, item.value.id) : null)
const queueGaps = computed(() => item.value ? queue.gaps(item.value) : [])
const queuePoller = usePoller(() => queue.load(props.project.id), 20_000)
watch(() => props.project.id, id => { void queue.load(id) }, { immediate: true })
onMounted(() => queuePoller.start())
onBeforeUnmount(() => queuePoller.stop())
const canQueue = computed(() => props.item?.is_leaf !== false && !ticket.readOnly.value && !ticket.gone.value && can('run.create', props.project.id))
const canRelease = computed(() => editable.value && !!props.item && can('releases.write', props.project.id))
const releaseView = computed(() => props.nativeReleases?.get(props.item?.id ?? ''))
const journeys = useJourney()

// ---------- Following links: a modified click opens a new tab ----------
let modifiedClick = false
function rememberClick(event: MouseEvent) { modifiedClick = event.metaKey || event.ctrlKey || event.shiftKey }
function openLinked(key: string) { const tab = modifiedClick; modifiedClick = false; emit('openKey', key, tab) }

// ---------- Layout: a context column (activity, attachments, relations) when there is room ----------
const width = ref(0)
const wideScreen = ref(window.matchMedia('(min-width: 1800px)').matches)
const wideQuery = window.matchMedia('(min-width: 1800px)')
const onWide = (event: MediaQueryListEvent) => { wideScreen.value = event.matches }
let sizer: ResizeObserver | undefined
onMounted(() => {
  wideQuery.addEventListener('change', onWide)
  if (root.value) { sizer = new ResizeObserver(([entry]) => { width.value = entry.contentRect.width }); sizer.observe(root.value) }
})
let releaseChoiceAlive = true
onBeforeUnmount(() => {
  releaseChoiceAlive = false; wideQuery.removeEventListener('change', onWide); sizer?.disconnect()
})
const contextColumn = computed(() => props.mode === 'full' ? wideScreen.value : width.value >= 860)

// ---------- Edit mode: title, text and properties together, one Save ----------
const editing = ref(false)
const saving = ref(false)
const benefitNotice = ref('')
const benefitInvalidKey = ref('')
const titleField = ref<HTMLTextAreaElement>()
const draft = reactive({ ...benefitDraft({}), title: '', body: '', acceptance: '', notes: '', state: '', priority: '', assignee: '', humanCheck: '' })
let base = { ...draft }
// The draft's values from one server copy: the editor's base (the copy its
// save is checked against), never whatever object shows at the moment.
function snapshot(it: ListItem = ticket.base() ?? item.value!) {
  return {
    ...benefitDraft(it.fields), title: it.title, body: it.body ?? '', acceptance: criteriaText(it.fields.acceptance_criteria),
    notes: typeof it.fields.notes === 'string' ? it.fields.notes : '', state: it.state, priority: it.priority && it.priority !== 'none' ? it.priority : '', assignee: it.assignee?.id ?? '',
    humanCheck: it.human_check ?? '',
  }
}
const editDirty = computed(() => editing.value && (Object.keys(base) as (keyof typeof base)[]).some(key => draft[key] !== base[key]))
const editStatusOptions = computed(() => {
  const options = statusOptions([item.value?.state ?? ''])
  return options.some(o => o.value === draft.state) || !draft.state ? options : [{ value: draft.state, meta: statusMeta(draft.state) }, ...options]
})
const benefitNames: Record<string, string> = {
  pill_en: 'Pill · English', pill_de: 'Pill · Deutsch', benefit_en: 'Benefit · English', benefit_de: 'Benefit · Deutsch',
}
function focusBenefit(key: string) {
  const name = benefitNames[key]
  const scope = root.value?.querySelector('.edit-benefits')
  if (!scope || !name) return
  const label = [...scope.querySelectorAll('label')].find(item => item.textContent?.trim() === name)
  const id = label?.getAttribute('for')
  if (!id) return
  scope.querySelector<HTMLElement>(`#${CSS.escape(id)}`)?.focus()
}
function refreshBenefitNotice() {
  const target = item.value
  if (!benefitNotice.value || !target) return
  if (!needsBenefitPrompt({ kind_id: target.kind_id, kind_slug: target.kind_slug, estimate: target.estimate, state: base.state, fields: draft }, draft.state)) {
    benefitNotice.value = ''
    benefitInvalidKey.value = ''
    return
  }
  const gap = firstBenefitGap(draft)
  benefitNotice.value = gap?.line ?? ''
  benefitInvalidKey.value = gap?.key ?? ''
}
async function startEdit(focus: 'title' | 'body' | 'benefit' = 'title') {
  if (!editable.value || !item.value || editing.value) return
  // Editing starts now: the row is pinned and the draft builds on the copy
  // the editor keeps as its base.
  const copy = ticket.hold()
  base = snapshot(copy ?? item.value); Object.assign(draft, base)
  benefitNotice.value = ''
  benefitInvalidKey.value = ''
  editing.value = true
  await nextTick()
  // The caret goes to the end of the title: typing adds to it rather than replacing it.
  if (focus === 'title') { const el = titleField.value; el?.focus(); el?.setSelectionRange(el.value.length, el.value.length); growTitle() }
  else if (focus === 'benefit') root.value?.querySelector<HTMLInputElement>('.edit-benefits input')?.focus()
  else root.value?.querySelector<HTMLTextAreaElement>('.edit-form .md-area')?.focus()
}
function changeBenefit(key: string, value: string | boolean) {
  if (key === 'hide_from_release_notes' && typeof value === 'boolean') draft.hide_from_release_notes = value
  else if (typeof value === 'string' && benefitTextKeys.includes(key as typeof benefitTextKeys[number])) draft[key as typeof benefitTextKeys[number]] = value
  refreshBenefitNotice()
}
function growTitle() { const el = titleField.value; if (el) { el.style.height = 'auto'; el.style.height = `${el.scrollHeight}px` } }
async function saveEdit() {
  const target = item.value
  if (!target || saving.value) return
  if (!draft.title.trim()) { toast('A title is needed.', { tone: 'error' }); titleField.value?.focus(); return }
  if (!editDirty.value) { editing.value = false; return }
  // Only what the viewer changed goes out; useTicket puts changed fields on
  // the editor's base copy and sends that copy's revision.
  const patch: TicketChange = {}
  if (draft.title.trim() !== base.title) patch.title = draft.title.trim()
  if (draft.body !== base.body) patch.body = draft.body
  if (draft.state !== base.state) patch.state = draft.state
  if (draft.humanCheck.trim() !== base.humanCheck) {
    if (!humanCheckPerson.value && (!draft.humanCheck.trim() || !humanCheckEditable.value)) {
      toast(!draft.humanCheck.trim() ? 'Only a person can mark a human check checked.' : 'Only a person can undo a human check.', { tone: 'error' })
      root.value?.querySelector<HTMLInputElement>('.edit-human-check')?.focus()
      return
    }
    patch.human_check = draft.humanCheck.trim() || null
  }
  const changed: Record<string, unknown> = {}
  const setField = (name: string, value: string, before: string) => { if (value === before) return; changed[name] = value.trim() ? value : undefined }
  setField('acceptance_criteria', draft.acceptance, base.acceptance)
  setField('notes', draft.notes, base.notes)
  setField('priority', draft.priority, base.priority)
  setField('assignee', draft.assignee, base.assignee)
  if (['ticket', 'work'].includes(target.kind_slug)) {
    for (const key of benefitTextKeys) setField(key, draft[key], base[key])
    if (draft.hide_from_release_notes !== base.hide_from_release_notes) changed.hide_from_release_notes = draft.hide_from_release_notes
  }
  if (Object.keys(changed).length) patch.fields = changed
  // The fields as the save will leave them, for the benefit check.
  const fields: Record<string, unknown> = { ...(ticket.base()?.fields ?? target.fields) }
  for (const [key, value] of Object.entries(changed)) { if (value === undefined) delete fields[key]; else fields[key] = value }
  // The benefit editor is already on this form. Point at the first incomplete field.
  if (needsBenefitPrompt({ kind_id: target.kind_id, kind_slug: target.kind_slug, estimate: target.estimate, state: base.state, fields }, draft.state)) {
    const gap = firstBenefitGap(fields)
    benefitNotice.value = gap?.line ?? 'A 2–4 word pill and a benefit, in both languages, are required.'
    benefitInvalidKey.value = gap?.key ?? 'pill_en'
    await nextTick()
    focusBenefit(benefitInvalidKey.value)
    return
  }
  benefitNotice.value = ''
  benefitInvalidKey.value = ''
  // The answer names the assignee by id: the store names them from here.
  const person = draft.assignee !== base.assignee ? assigneeOptions.value.find(o => o.value === draft.assignee && o.value) : undefined
  if (person) { props.names.set(person.value, person.label); rowStore.learnName(person.value, person.label) }
  saving.value = true
  const result = await ticket.patch(patch, undefined, target.id)
  if (item.value?.id !== target.id) return
  saving.value = false
  if (result === 'ok') {
    editing.value = false
    toast(`Saved ${target.key}`)
    void nextTick(() => root.value?.focus({ preventScroll: true }))
  } else if (result === 'conflict') {
    // The editor rebased onto the newer version; the draft stays, compared
    // against it from now on. What the viewer did not touch takes the newer
    // value, so saving again does not undo someone else's change.
    const fresh = snapshot()
    for (const key of Object.keys(base) as (keyof typeof base)[]) if (draft[key] === base[key]) (draft as Record<string, unknown>)[key] = fresh[key]
    base = fresh
  }
}
async function cancelEdit() {
  if (editDirty.value && !(await confirmAction({ title: 'Discard your changes?', body: `Your edits to ${item.value?.key ?? 'this ticket'} have not been saved.`, confirmLabel: 'Discard', danger: true }))) return
  editing.value = false
  void nextTick(() => root.value?.focus({ preventScroll: true }))
}
// Status, priority and assignee in the form: the app's own menus (icons, arrows,
// digits, a filter for people), styled as form fields; a choice only edits the draft.
const uid = useId()
const editMenu = ref<{ kind: 'status' | 'priority' | 'assignee'; anchor: HTMLElement } | null>(null)
function openEditMenu(kind: 'status' | 'priority' | 'assignee', event: Event) {
  const anchor = event.currentTarget as HTMLElement
  editMenu.value = editMenu.value?.kind === kind ? null : { kind, anchor }
}
function editMenuKeys(kind: 'status' | 'priority' | 'assignee', event: KeyboardEvent) {
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); if (editMenu.value?.kind !== kind) openEditMenu(kind, event) }
}
function closeEditMenu(restore: boolean) { const anchor = editMenu.value?.anchor; editMenu.value = null; if (restore) anchor?.focus() }
function chooseEdit(kind: 'status' | 'priority' | 'assignee', value: string) {
  if (kind === 'status') draft.state = value
  else if (kind === 'priority') draft.priority = value
  else draft.assignee = value
  closeEditMenu(true)
  refreshBenefitNotice()
}
const draftAssignee = computed(() => assigneeOptions.value.find(option => option.value === draft.assignee && option.value))
function editKeys(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); event.stopPropagation(); void saveEdit() }
  else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); void cancelEdit() }
}
watch(() => props.item?.id, () => { editing.value = false; saving.value = false }, { flush: 'sync' })
watch(editing, value => { if (!value) { editMenu.value = null; benefitNotice.value = ''; benefitInvalidKey.value = '' } })

// ---------- Attachments: drop anywhere on the ticket, paste a screenshot ----------
const dropping = ref(false)
let dragDepth = 0
const hasFiles = (event: DragEvent) => !!event.dataTransfer?.types.includes('Files')
function dragEnter(event: DragEvent) { if (!hasFiles(event) || !attachable.value) return; event.preventDefault(); dragDepth++; dropping.value = true }
function dragOverRoot(event: DragEvent) { if (!hasFiles(event) || !attachable.value) return; event.preventDefault(); if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy' }
function dragLeaveRoot(event: DragEvent) { if (!hasFiles(event)) return; dragDepth = Math.max(0, dragDepth - 1); if (!dragDepth) dropping.value = false }
function dropFiles(event: DragEvent) {
  if (!hasFiles(event) || !attachable.value) return
  event.preventDefault(); dragDepth = 0; dropping.value = false
  const files = [...(event.dataTransfer?.files ?? [])]
  if (files.length) void attachments.add(files)
}
function pasteFiles(event: ClipboardEvent) {
  const target = event.target as HTMLElement
  if (!attachable.value || target.closest('input, textarea, [contenteditable="true"]')) return
  const files = [...(event.clipboardData?.files ?? [])]
  if (!files.length) return
  event.preventDefault()
  // Pasted screenshots arrive as "image.png"; name them by local date and time.
  const stamp = new Date(), two = (n: number) => String(n).padStart(2, '0')
  const when = `${stamp.getFullYear()}-${two(stamp.getMonth() + 1)}-${two(stamp.getDate())} ${two(stamp.getHours())}.${two(stamp.getMinutes())}`
  void attachments.add(files.map((file, i) => file.name && file.name !== 'image.png' ? file : new File([file], `Screenshot ${when}${files.length > 1 ? ` (${i + 1})` : ''}.png`, { type: file.type })))
}
// Pasting into an editor uploads and inserts the image reference at the caret.
async function attachmentId(file: File) { return (await attachments.upload(file))?.id ?? null }
function openAttachment(id: string) { lightbox.value?.open(id) }

const root = ref<HTMLElement>()
const scroller = ref<HTMLElement>()
const title = ref<InstanceType<typeof InlineTitle>>()
const descSection = ref<InstanceType<typeof MarkdownSection>>()
const acSection = ref<InstanceType<typeof MarkdownSection>>()
const notesSection = ref<InstanceType<typeof MarkdownSection>>()
const sections = computed(() => [descSection.value, acSection.value, notesSection.value].filter(section => !!section))
const commentDraft = ref('')
const composer = ref<InstanceType<typeof CommentComposer>>()
const timeline = ref<InstanceType<typeof ActivityTimeline>>()
watchEffect(() => { liveBusy.value = convertOpen.value || editing.value || saving.value || !!title.value?.editing || sections.value.some(section => section.editing) })
// Parts someone else just changed get a brief tint (AEON-326).
const PROPERTY_FIELDS = ['state', 'parent_id', 'fields.priority', 'fields.assignee', 'fields.estimate', 'fields.eta', 'fields.release']
const liveTint = computed(() => {
  const fields = ticket.liveFields.value
  return {
    title: fields.includes('title'), props: fields.some(field => PROPERTY_FIELDS.includes(field)), body: fields.includes('body'),
    acceptance: fields.includes('fields.acceptance_criteria'), notes: fields.includes('fields.notes'),
  }
})
const menu = ref<{ kind: 'priority' | 'assignee' | 'epic' | 'release'; anchor: HTMLElement } | null>(null)
const showAcceptance = ref(false)
const showNotes = ref(false)

const acceptance = computed(() => criteriaText(item.value?.fields.acceptance_criteria))
const notes = computed(() => typeof item.value?.fields.notes === 'string' ? item.value.fields.notes : '')
async function saveHumanCheck(text: string | null) {
  const result = await ticket.patch({ human_check: text })
  if (result === 'ok') { activity.load(); toast(text ? 'Human check restored' : 'Human check marked checked') }
  return result
}
const hasChildren = computed(() => !!item.value && (item.value.kind_slug === 'epic' || item.value.children_count > 0 || ticket.children.value.length > 0))
const priorityOptions: MenuOption[] = [{ value: 'high', label: 'High' }, { value: 'medium', label: 'Medium' }, { value: 'low', label: 'Low' }, { value: '', label: 'No priority' }]
const assigneeOptions = computed<MenuOption[]>(() => {
  const people = new Map(props.people.map(person => [person.id, person.name]))
  if (props.me) people.set(props.me.id, props.me.name)
  const others = [...people.entries()].filter(([id]) => id !== props.me?.id).sort((a, b) => a[1].localeCompare(b[1]))
  return [
    ...(props.me ? [{ value: props.me.id, label: props.me.name, hint: 'you' }] : []),
    ...others.map(([id, name]) => ({ value: id, label: name })),
    { value: '', label: 'Unassigned' },
  ]
})
watch(() => props.item?.id, () => {
  showAcceptance.value = false; showNotes.value = false; menu.value = null; linkAnchor.value = null; queueAnchor.value = null
  scroller.value?.scrollTo({ top: 0 })
})

function link() { return `${location.origin}/p/${encodeURIComponent(props.project.routeKey)}/${encodeURIComponent(props.item?.key ?? props.ticketKey)}` }
function copy(text: string, label: string) {
  navigator.clipboard.writeText(text).then(() => toast(`${label} copied`), () => toast(`${label} could not be copied`, { tone: 'error' }))
}
// The visible control for a key: narrow and wide layouts both render some of them.
function anchorFor(shortcut: string) {
  return [...(root.value?.querySelectorAll<HTMLElement>(`[aria-keyshortcuts="${shortcut}"]`) ?? [])].find(el => el.getClientRects().length) ?? null
}
function openMenu(kind: 'priority' | 'assignee' | 'epic' | 'release', anchor: HTMLElement | null) {
  if (!anchor) return
  if (kind === 'release' ? canRelease.value : kind === 'epic' ? movable.value : kind === 'assignee' ? editable.value || canQueue.value : editable.value) menu.value = { kind, anchor }
}
async function chooseRelease(target: ReleaseTarget) {
  const it = item.value
  const projectId = props.project.id
  if (!it || !canRelease.value) return
  const ticket = { id: it.id, key: it.key, title: it.title, state: it.state, kind: it.kind_slug, isParent: releaseViewIsParent(releaseView.value) }
  menu.value = null
  try {
    const outcome = await assignToRelease(projectId, [ticket], target)
    if (outcome.journey) journeys.set(projectId, outcome.journey)
    if (!releaseChoiceAlive || props.project.id !== projectId || props.item?.id !== ticket.id) return
    const changed = outcome.opened ? openedMembershipMessage(outcome.opened) : null
    if (changed) {
      toast(changed)
      emit('assigned')
      return
    }
    emit('assigned')
    const title = outcome.opened?.status === 'added' ? outcome.opened.releaseTitle : outcome.releaseTitle
    const skipped = outcome.skipped.length ? ` ${outcome.skipped[0].reason}` : ''
    const eventId = outcome.result?.event_id
    const count = outcome.opened?.status === 'added' ? outcome.opened.count : outcome.result?.leaf_node_ids?.length
    const message = ticket.isParent
      ? count === undefined ? `Release placement for ${ticket.key} could not be confirmed. Refresh to check its leaves.` : `Added ${count} ${count === 1 ? 'leaf' : 'leaves'} under ${ticket.key} to ${title}.${skipped}`
      : `Added ${ticket.key} to ${title}.${skipped}`
    toast(message, {
      timeout: 8000,
      action: eventId ? { label: 'Undo', run: () => void undoRelease(eventId, ticket.key) } : undefined,
    })
  } catch (error) {
    if (!releaseChoiceAlive || props.project.id !== projectId || props.item?.id !== ticket.id) return
    if (error instanceof AssignCancelled) return
    toast(error instanceof Error ? error.message : 'The ticket was not added to a release.', { tone: 'error' })
  }
}
async function undoRelease(eventId: number, key: string) {
  try {
    await undoEvent(eventId)
    emit('assigned')
    toast(`Undone: ${key} left the release.`)
  } catch (error) {
    toast(error instanceof APIError && error.status === 409 ? 'The release changed since, so nothing was undone.' : `Undo did not work: ${error instanceof Error ? error.message : 'unknown error'}`, { tone: 'error' })
  }
}
function closeMenu(restore: boolean) { const anchor = menu.value?.anchor; menu.value = null; if (restore) anchor?.focus() }
async function choosePriority(value: string) { const anchor = menu.value?.anchor; menu.value = null; anchor?.focus(); await ticket.setPriority(value || null) }
async function chooseAssignee(value: string) {
  const anchor = menu.value?.anchor; menu.value = null; anchor?.focus()
  const option = assigneeOptions.value.find(o => o.value === value)
  await ticket.setAssignee(value && option ? { id: value, name: option.label } : null)
}
async function chooseEpic(epic: { id: string; key: string; title: string } | null) {
  menu.value = null
  if (epic) await ticket.moveTo(epic)
}
// ---------- Links to other tickets ----------
const linkAnchor = ref<HTMLElement | null>(null)
// On a phone the picker opens under Link with the screen's height to itself:
// Link scrolls to the top first, so the keyboard does not cover the results.
async function openLink(anchor: HTMLElement | null) {
  if (!anchor || !linkable.value || !item.value) return
  menu.value = null
  if (matchMedia('(max-width: 600px)').matches) {
    anchor.scrollIntoView({ block: 'start', behavior: 'instant' })
    await new Promise(requestAnimationFrame)
  }
  linkAnchor.value = anchor
}
function closeLink(restore: boolean) { const anchor = linkAnchor.value; linkAnchor.value = null; if (restore) anchor?.focus() }
// Removing a link asks first, then offers Undo, like the other destructive actions.
async function unlinkEntry(entry: RelatedNode): Promise<boolean> {
  const target = item.value
  if (!target || !unlinkable.value) return false
  const other = entry.node?.key ?? 'an unavailable ticket'
  const ok = await confirmAction({
    title: `Remove the link to ${other}?`,
    body: `“${entry.label} ${other}” goes from ${target.key} and from ${other}. You can undo it right after.`,
    confirmLabel: 'Remove link', danger: true,
  })
  return ok ? ticket.unlink(entry) : false
}
async function remove() {
  const target = item.value
  if (!target || !deletable.value) return
  const ok = await confirmAction({
    title: `Delete ${target.key}?`,
    body: `“${target.title}” leaves the project list. ${target.children_count ? 'Its children must be moved or deleted first.' : 'The history stays in the audit log.'}`,
    confirmLabel: `Delete ${workNoun(workLabel(target, vocabulary.value))}`, danger: true,
  })
  if (ok) await ticket.remove(target)
}
async function addSection(kind: 'acceptance' | 'notes') {
  if (kind === 'acceptance') showAcceptance.value = true; else showNotes.value = true
  await nextTick()
  ;(kind === 'acceptance' ? acSection.value : notesSection.value)?.start()
}

function isDirty() {
  return editDirty.value || !!title.value?.isDirty() || sections.value.some(section => section.isDirty()) || !!composer.value?.isDirty() || !!timeline.value?.isDirty()
}
function discard() {
  editing.value = false; saving.value = false; commentDraft.value = ''
  title.value?.discard(); sections.value.forEach(section => section.discard())
  composer.value?.discard(); timeline.value?.discard()
  ticket.release()
}
function focus() { root.value?.focus({ preventScroll: true }) }
function queueKey(event: KeyboardEvent) {
  const repeatTarget = event.target as HTMLElement | null
  if (event.key.toUpperCase() === 'R' && event.shiftKey && !event.defaultPrevented && !event.metaKey && !event.ctrlKey && !event.altKey && !editing.value && !repeatTarget?.isContentEditable && !repeatTarget?.closest('input, textarea, select') && !document.querySelector('dialog[open], .floating') && mayRepeat.value) { event.preventDefault(); event.stopPropagation(); repeat(); return }
  if (event.key !== 'q' || event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || editing.value || !queueAction.value || document.querySelector('.floating')) return
  const target = event.target as HTMLElement | null
  if (target?.isContentEditable || target?.closest('input, textarea, select')) return
  event.preventDefault(); event.stopPropagation(); void queueAction.value.toggle()
}
defineExpose({
  el: root, focus, isDirty, discard,
  // An editor or a save is open: a live list waits with its structural updates.
  busy: () => liveBusy.value,
  editTitle: () => title.value?.start(),
  startEdit, editing,
  openStatus: () => { const anchor = anchorFor('s'); if (anchor && editable.value) emit('status', anchor) },
  openPriority: () => openMenu('priority', anchorFor('p')),
  toggleQueue: () => queueAction.value?.toggle(),
  openAssignee: () => openMenu('assignee', anchorFor('a')),
  openRelease: () => openMenu('release', anchorFor('g')),
  openLink: () => openLink(anchorFor('r')),
  focusComposer: () => composer.value?.focus(),
})
</script>

<template>
  <component
    :is="mode === 'panel' ? 'aside' : 'article'" ref="root" class="ticket-ws" :class="[mode, { 'has-context': contextColumn && !editing, editing }]" aria-label="Ticket details" tabindex="-1"
    @click.capture="rememberClick" @dragenter="dragEnter" @dragover="dragOverRoot" @dragleave="dragLeaveRoot" @drop="dropFiles" @paste="pasteFiles" @keydown="queueKey"
  >
    <TicketHeaderBar
      ref="header"
      :ticket-key="item?.key ?? ticketKey" :kind="item?.kind_slug ?? null" :level-name="item ? workLabel(item, vocabulary.value) : undefined" :level-icon="item?.level_icon" :position="position" :mode="mode" :can-write="editable"
      :can-delete="deletable" :can-move="movable && ['work','ticket'].includes(item?.kind_slug ?? '')" :can-repeat="mayRepeat" :can-edit-recurrence="mayEditRecurrence" :recurrence-label="originRecurrence && item?.recurrence && !item.recurrence.retired ? recurrenceName(originRecurrence) : undefined" :trail="trail" :editing="editing" :saving="saving" :dirty="editDirty"
      :can-work-actions="item?.kind_slug === 'work' && editable && humanCheckPerson"
       :open-in-project="openInProject" :back-label="backLabel"
      @copy-key="copy(item?.key ?? ticketKey, item?.key ?? ticketKey)" @copy-link="copy(link(), 'link')" @prev="emit('prev')" @next="emit('next')"
      @expand="emit('expand')" @collapse="emit('collapse')" @new-tab="emit('newTab')" @close="emit('close')" @open-in-project="emit('openInProject')"
      @move="anchor => openMenu('epic', anchor)" @delete="remove" @convert="convertOpen = true" @back="steps => emit('trailBack', steps)"
      @edit="startEdit()" @save="saveEdit" @cancel="cancelEdit"
      @repeat="repeat"
      @edit-recurrence="editRecurrence"
      @work-actions="workActionsOpen = true"
    >
      <template #queue><QueueAction v-if="item && canQueue" ref="queueAction" :row="item" :project-id="project.id" label /></template>
      <template v-if="item?.recurrence" #marker><RecurringPill :recurrence="item.recurrence" /></template>
    </TicketHeaderBar>
    <p class="sr-only" role="status" aria-live="polite">{{ ticket.liveMessage.value }}</p>
    <RecurrenceEditor v-if="repeatSource" :project="project" :source="repeatSource" @close="repeatSource = null" @saved="recurrenceSaved" />
    <RecurrenceEditor v-if="recurrenceEdit && sourceProject && mayEditRecurrence" :project="sourceProject" :recurrence="recurrenceEdit" @close="recurrenceEdit = null" @saved="recurrenceSaved" />
    <WorkLifecycleSheet v-if="workActionsOpen && item && me" :node-id="item.id" :node-key="item.key" :person-id="me.id" @close="closeWorkActions" @completed="workLifecycleCompleted" />
    <ConvertKindSheet v-if="convertOpen && item" :item="item" :children="ticket.children.value" :children-loading="ticket.childrenLoading.value" :convert="ticket.convert" @close="closeConvert" @converted="finishConvert" />

    <div ref="scroller" class="ws-scroll">
      <div v-if="ticket.gone.value" class="ws-state" role="alert">
        <span class="state-icon"><AppIcon name="archive" :size="18" /></span>
        <h2>{{ item?.key ?? ticketKey }} is no longer here</h2>
        <p>It was deleted or moved out of this project.</p>
        <button type="button" class="btn" @click="emit('close')">Back to the list</button>
      </div>
      <div v-else-if="resolveError && !item" class="ws-state" role="alert">
        <span class="state-icon danger"><AppIcon name="alert" :size="18" /></span>
        <h2>This ticket could not be opened</h2>
        <p>{{ resolveError }}</p>
        <button type="button" class="btn" @click="emit('retry')"><AppIcon name="refresh" :size="14" />Try again</button>
      </div>
      <div v-else-if="!item" class="ws-skeleton" role="status" aria-label="Loading ticket">
        <span class="skeleton w30" /><span class="skeleton title-skel" /><span class="skeleton title-skel short" />
        <span class="chips"><span class="skeleton chip" /><span class="skeleton chip" /><span class="skeleton chip" /></span>
        <span class="skeleton w90" /><span class="skeleton w80" /><span class="skeleton w60" /><span class="skeleton w85" />
      </div>

      <!-- Edit mode: the whole ticket as one form, one Save -->
      <form v-else-if="editing" class="edit-form" :aria-label="`Edit ${item.key}`" @submit.prevent="saveEdit" @keydown="editKeys">
        <p v-if="ticket.liveHeld.value" class="edit-live" role="status">
          <AppIcon :name="ticket.liveHeld.value === 'deleted' ? 'archive' : 'refresh'" :size="13" />{{ ticket.liveHeld.value === 'deleted' ? 'Deleted elsewhere meanwhile.' : 'Changed elsewhere meanwhile: saving shows that version first and keeps your draft.' }}
        </p>
        <label class="sr-only" for="edit-title">Title</label>
        <textarea id="edit-title" ref="titleField" v-model="draft.title" class="edit-title" :class="{ large: mode === 'full' }" rows="1" maxlength="500" placeholder="Title" @input="growTitle" @keydown.enter.exact.prevent />
        <div class="edit-props">
          <div class="edit-prop"><span :id="`${uid}-status`" class="prop-label">Status</span>
            <button
              type="button" class="field field-pick" aria-haspopup="menu" :aria-expanded="editMenu?.kind === 'status'" :aria-labelledby="`${uid}-status ${uid}-status-value`"
              @click="openEditMenu('status', $event)" @keydown="editMenuKeys('status', $event)"
            ><StatusIcon :state="draft.state" /><span :id="`${uid}-status-value`" class="pick-value">{{ item.status_derived ? (item.work_children_count ? `Follows its ${item.work_children_count} children` : 'Follows its children') : statusMeta(draft.state).label }}</span><AppIcon name="chevron" :size="12" class="pick-chev" /></button>
          </div>
          <div class="edit-prop"><span :id="`${uid}-priority`" class="prop-label">Priority</span>
            <button
              type="button" class="field field-pick" aria-haspopup="menu" :aria-expanded="editMenu?.kind === 'priority'" :aria-labelledby="`${uid}-priority ${uid}-priority-value`"
              @click="openEditMenu('priority', $event)" @keydown="editMenuKeys('priority', $event)"
            ><PriorityIcon v-if="draft.priority" :priority="draft.priority" /><span v-else class="pick-none" aria-hidden="true">—</span><span :id="`${uid}-priority-value`" class="pick-value" :class="{ unset: !draft.priority }">{{ draft.priority ? priorityLabel(draft.priority) : 'No priority' }}</span><AppIcon name="chevron" :size="12" class="pick-chev" /></button>
          </div>
          <div class="edit-prop"><span :id="`${uid}-assignee`" class="prop-label">Assignee</span>
            <button
              type="button" class="field field-pick" aria-haspopup="menu" :aria-expanded="editMenu?.kind === 'assignee'" :aria-labelledby="`${uid}-assignee ${uid}-assignee-value`"
              @click="openEditMenu('assignee', $event)" @keydown="editMenuKeys('assignee', $event)"
            ><PersonAvatar v-if="draftAssignee" :id="draftAssignee.value" :name="draftAssignee.label" :size="18" /><AppIcon v-else name="user" :size="13" class="pick-none" /><span :id="`${uid}-assignee-value`" class="pick-value" :class="{ unset: !draftAssignee }">{{ draftAssignee?.label ?? 'Unassigned' }}</span><AppIcon name="chevron" :size="12" class="pick-chev" /></button>
          </div>
        </div>
        <section class="edit-section" aria-labelledby="edit-desc"><h3 id="edit-desc" class="eyebrow">Description</h3>
          <MarkdownEditor v-model="draft.body" label="Description" bare :split="mode === 'full'" :min-rows="mode === 'full' ? 12 : 7" :attachment-id="attachmentId" placeholder="What is this about? Paste a screenshot to add it inline." @save="saveEdit" @cancel="cancelEdit" />
        </section>
        <section class="edit-section" aria-labelledby="edit-ac"><h3 id="edit-ac" class="eyebrow">Acceptance criteria</h3>
          <MarkdownEditor v-model="draft.acceptance" label="Acceptance criteria" bare :split="mode === 'full'" :min-rows="4" :attachment-id="attachmentId" placeholder="- [ ] What must be true when this is done" @save="saveEdit" @cancel="cancelEdit" />
        </section>
        <section v-if="['work','ticket', 'task'].includes(item.kind_slug)" class="edit-section"><label :for="`${uid}-human-check`" class="eyebrow">Needs a human check</label><input :id="`${uid}-human-check`" v-model="draft.humanCheck" class="field edit-human-check" :disabled="!humanCheckEditable" maxlength="500" placeholder="What only a person can confirm" /><p class="hc-edit-hint">Automatic moves to Delivered and Accepted skip this ticket until it is checked.<template v-if="!humanCheckPerson"> {{ humanCheckEditable ? 'Only a person can mark it checked.' : 'Only a person can undo the completed check.' }}</template></p></section>
        <section class="edit-section" aria-labelledby="edit-notes"><h3 id="edit-notes" class="eyebrow">Notes</h3>
          <MarkdownEditor v-model="draft.notes" label="Notes" bare :split="mode === 'full'" :min-rows="3" :attachment-id="attachmentId" @save="saveEdit" @cancel="cancelEdit" />
        </section>
        <TicketBenefits v-if="['ticket', 'work'].includes(item.kind_slug)" class="edit-benefits" :fields="draft" :parent="item.estimate?.is_parent" editing :disabled="saving" :done="completedTicketState(item.state, rowStore.kindSchema(item.kind_id))" :notice="benefitNotice" :invalid-key="benefitInvalidKey" @change="changeBenefit" />
        <p class="edit-hint"><KeyCap k="mod" /><KeyCap k="enter" /> save · <kbd class="keycap">esc</kbd> cancel · paste or drop images to attach them</p>
      </form>

      <div v-else class="ws-grid">
        <div class="ws-main">
          <p v-if="!editable" class="read-only" role="note"><AppIcon name="alert" :size="13" />You can read this {{ workNoun(workLabel(item, vocabulary.value)) }} but not change it.</p>
          <InlineTitle :record-id="item.id" ref="title" :class="{ 'live-tint': liveTint.title }" :value="item.title" :editable="editable" :large="mode === 'full'" :save="record.setTitle" />
          <TicketProperties
            class="ws-props" :class="{ 'only-narrow': mode === 'full', 'live-tint': liveTint.props }" :item="item" :editable="editable" layout="row" :now="now"
            :release-view="releaseView" :release-editable="canRelease" :save-estimate="ticket.setEstimate" :save-placement="fields => ticket.patch({ fields })" :queue-editable="canQueue" :queue-entry="queueEntry"
            @status="anchor => emit('status', anchor)" @priority="anchor => openMenu('priority', anchor)" @assignee="anchor => openMenu('assignee', anchor)"
            @epic="anchor => openMenu('epic', anchor)" @release="anchor => openMenu('release', anchor)" @open-parent="openLinked"
          />
          <section v-if="queueEntry" class="q-card" :class="{ wait: queueEntry.waiting_reason }" aria-label="Work queue">
            <QueueDetails :entry="queueEntry" :manual="queue.snapshots[project.id]?.manual_order" />
            <div class="q-card-acts">
              <button v-if="!queueEntry.target_agent_id" type="button" class="btn sm" :disabled="!canQueue || queue.busy || queue.firstShared(project.id)?.ticket_id === item.id" @click="queue.move(project.id, item.id, 'top').catch(e => toast(e.message, { tone: 'error' }))"><AppIcon name="to-top" :size="13" />Move to top</button>
              <button v-if="item.is_leaf !== false" type="button" class="btn sm" :disabled="!canQueue" @click="openMenu('assignee', $event.currentTarget as HTMLElement)"><AppIcon name="play" :size="12" />Start now on…</button>
              <button type="button" class="btn sm ghost" :disabled="!canQueue || queue.busy" @click="queue.remove(project.id, item.id).catch(e => toast(e.message, { tone: 'error' }))"><AppIcon name="close" :size="13" />Remove</button>
              <button type="button" class="btn sm ghost" @click="queueAnchor = $event.currentTarget as HTMLElement"><AppIcon name="queue" :size="13" />Open the queue</button>
            </div>
          </section>
          <section v-else-if="canQueue && queueable(item) && queueGaps.length" class="q-card wait" aria-label="Not ready to queue">
            <p class="eyebrow"><AppIcon name="alert" :size="14" />Not ready to queue</p>
            <p>Missing {{ queueGaps.map(gap => gap === 'estimate' ? 'estimate' : gap === 'criteria' ? 'acceptance criteria' : 'a named blocker').join(', ') }}.</p>
            <button type="button" class="btn sm" @click="queueAction?.toggle()"><AppIcon name="sparkle" :size="13" />Fix what is missing</button>
          </section>
          <p v-if="item.kind_slug !== 'epic'" class="meta">Suggested release <SuggestedReleaseCell :row="item" :project-id="project.id" :now="now" :description-id="`drawer-suggested-${item.id}`" /></p>
          <RecurrenceProvenance v-if="item.recurrence" :key="item.id" :node-id="item.id" :recurrence="item.recurrence" @loaded="originLoaded" />
          <HumanCheck :item="item" :editable="editable" :save="saveHumanCheck" :names="names" />
          <p v-if="eta" class="meta ws-eta"><span>Progress and ETA</span><EtaCell :eta="eta" :now="now" align="start" labelled :connection-stale="etaConnectionStale" /></p>
          <p class="meta" :class="{ 'only-narrow': mode === 'full' }">
            Updated <time :datetime="item.updated_at" :data-tip="absoluteTime(item.updated_at)">{{ relativeTime(item.updated_at, { now, long: true }) }}</time>
            · Created <time :datetime="item.created_at" :data-tip="absoluteTime(item.created_at)">{{ relativeTime(item.created_at, { now, long: true }) }}</time>
          </p>
          <AttachmentStrip
            v-if="!contextColumn && (attachments.count.value || attachable)" class="ws-attachments" layout="strip" :items="attachments.items.value" :uploads="attachments.uploads.value"
            :can-write="attachable" :loading="attachments.loading.value" :error="attachments.error.value"
            @open="openAttachment" @add="files => attachments.add(files)" @remove="attachments.remove" @reorder="attachments.reorder" @caption="attachments.setCaption"
            @retry="attachments.retry" @cancel="attachments.cancel" @reload="attachments.load"
          />
          <div class="divider" />

          <div class="sections">
            <MarkdownSection :record-id="item.id" ref="descSection" :class="{ 'live-tint': liveTint.body }" title="Description" :value="item.body" :editable="editable" :save="record.setBody" :attachment-id="attachable ? attachmentId : undefined" empty-text="Add a description" @open-attachment="openAttachment" />
            <TicketExtensions :project-id="project.id" :node-id="item.id" />
            <MarkdownSection :record-id="item.id" v-if="acceptance.trim() || showAcceptance" ref="acSection" :class="{ 'live-tint': liveTint.acceptance }" title="Acceptance criteria" :value="acceptance" :editable="editable" :save="record.setAcceptance" :attachment-id="attachable ? attachmentId : undefined" @open-attachment="openAttachment" />
            <MarkdownSection :record-id="item.id" v-if="notes.trim() || showNotes" ref="notesSection" :class="{ 'live-tint': liveTint.notes }" title="Notes" :value="notes" :editable="editable" :save="record.setNotes" :attachment-id="attachable ? attachmentId : undefined" @open-attachment="openAttachment" />
            <div v-if="editable && (!(acceptance.trim() || showAcceptance) || !(notes.trim() || showNotes))" class="add-sections">
              <button v-if="!(acceptance.trim() || showAcceptance)" type="button" class="add-section" @click="addSection('acceptance')"><AppIcon name="plus" :size="12" />Acceptance criteria</button>
              <button v-if="!(notes.trim() || showNotes)" type="button" class="add-section" @click="addSection('notes')"><AppIcon name="plus" :size="12" />Notes</button>
            </div>
            <TicketBenefits v-if="['ticket', 'work'].includes(item.kind_slug)" class="ws-benefits" :fields="item.fields" :parent="item.estimate?.is_parent" :node-id="item.id" @generated="ticket.refresh()" :done="completedTicketState(item.state, rowStore.kindSchema(item.kind_id))" :editable="editable" @edit="startEdit('benefit')" />
          </div>

          <TicketAgentWork v-if="['work','ticket','epic','task'].includes(item.kind_slug)" class="ws-block" :node-id="item.id" :kind="item.kind_slug" :level-name="workLabel(item, vocabulary.value)" />
          <TicketOutcomes v-if="['work', 'ticket'].includes(item.kind_slug)" class="ws-block" :node-id="item.id" />
          <TicketReviews v-if="['work', 'ticket', 'task'].includes(item.kind_slug)" :key="item.id" class="ws-block" :node-id="item.id" :project-id="project.id" />
          <ChildList
            v-if="hasChildren" class="ws-block" :children="ticket.children.value" :loading="ticket.childrenLoading.value" :editable="editable"
            :child-label="item.kind_slug === 'work' ? workNoun(vocabulary.leaf.name) : item.kind_slug === 'epic' ? 'ticket' : 'task'" :parent-label="workNoun(workLabel(item, vocabulary.value))" :progress="ticket.childProgress()" :progress-error="ticket.progressError.value" :add="title => ticket.addChild(title, project.routeKey)"
            @open="openLinked"
          />
          <!-- Relations, then activity: both wait for the relations, so neither jumps. -->
          <template v-if="!contextColumn && ticket.relationsReady.value">
            <RelationList class="ws-block" :class="{ 'only-narrow': mode === 'full' }" :related="ticket.related.value" :editable="linkable" :removable="unlinkable" :unlink="unlinkEntry" @open="openLinked" @link="openLink" />
            <ActivityTimeline :record-id="item.id"
              ref="timeline" class="ws-block" :entries="activity.timeline.value" :loading="activity.loading.value" :loading-older="activity.loadingOlder.value"
              :has-older="!!activity.cursor.value" :error="activity.error.value" :me="me?.id" :now="now" :can-write="commentable" :can-delete="commentDeletable"
              :edit="activity.edit" :remove="activity.remove" @older="activity.loadOlder" @retry="activity.load"
            />
            <CommentComposer v-model="commentDraft" :record-id="item?.id" v-if="mode === 'full'" ref="composer" class="ws-block inline-composer" :me="me?.name ?? '?'" :me-id="me?.id ?? null" :post="record.addComment" :disabled="!commentable" />
          </template>
        </div>

        <!-- Wide: a context column beside the reading column -->
        <aside v-if="contextColumn" class="ws-context" aria-label="Attachments, relations and activity">
          <AttachmentStrip
            v-if="attachments.count.value || attachable" layout="gallery" :items="attachments.items.value" :uploads="attachments.uploads.value"
            :can-write="attachable" :loading="attachments.loading.value" :error="attachments.error.value"
            @open="openAttachment" @add="files => attachments.add(files)" @remove="attachments.remove" @reorder="attachments.reorder" @caption="attachments.setCaption"
            @retry="attachments.retry" @cancel="attachments.cancel" @reload="attachments.load"
          />
          <template v-if="ticket.relationsReady.value">
          <RelationList v-if="mode === 'panel' || ticket.related.value.length || linkable" class="ctx-block" :related="ticket.related.value" :editable="linkable" :removable="unlinkable" :unlink="unlinkEntry" @open="openLinked" @link="openLink" />
          <ActivityTimeline :record-id="item.id"
            ref="timeline" class="ctx-block" :entries="activity.timeline.value" :loading="activity.loading.value" :loading-older="activity.loadingOlder.value"
            :has-older="!!activity.cursor.value" :error="activity.error.value" :me="me?.id" :now="now" :can-write="commentable" :can-delete="commentDeletable"
            :edit="activity.edit" :remove="activity.remove" @older="activity.loadOlder" @retry="activity.load"
          />
          <CommentComposer v-model="commentDraft" :record-id="item?.id" v-if="mode === 'full'" ref="composer" class="ctx-block inline-composer" :me="me?.name ?? '?'" :me-id="me?.id ?? null" :post="record.addComment" :disabled="!commentable" />
          </template>
        </aside>

        <aside v-if="mode === 'full'" class="ws-side" aria-label="Properties">
          <div class="side-card">
            <TicketProperties
              :class="{ 'live-tint': liveTint.props }" :item="item" :editable="editable" layout="column" :now="now"
              :release-view="releaseView" :release-editable="canRelease" :save-estimate="ticket.setEstimate" :save-placement="fields => ticket.patch({ fields })" :queue-editable="canQueue" :queue-entry="queueEntry"
              @status="anchor => emit('status', anchor)" @priority="anchor => openMenu('priority', anchor)" @assignee="anchor => openMenu('assignee', anchor)"
              @epic="anchor => openMenu('epic', anchor)" @release="anchor => openMenu('release', anchor)" @open-parent="openLinked"
            />
          </div>
          <div v-if="(ticket.related.value.length || linkable) && !contextColumn && ticket.relationsReady.value" class="side-card"><RelationList :related="ticket.related.value" :editable="linkable" :removable="unlinkable" :unlink="unlinkEntry" @open="openLinked" @link="openLink" /></div>
        </aside>
      </div>
    </div>

    <footer v-if="mode === 'panel' && item && !ticket.gone.value && !editing" class="ws-composer">
      <CommentComposer v-model="commentDraft" :record-id="item?.id" ref="composer" :me="me?.name ?? '?'" :me-id="me?.id ?? null" :post="record.addComment" :disabled="!commentable" />
    </footer>

    <div v-if="dropping" class="drop-overlay" aria-hidden="true">
      <div class="drop-card"><AppIcon name="upload" :size="22" /><strong>Drop to attach to {{ item?.key ?? ticketKey }}</strong><span>Images show as thumbnails; other files as cards.</span></div>
    </div>
    <AttachmentLightbox ref="lightbox" :items="attachments.items.value" :ticket-key="item?.key ?? ticketKey" :can-write="attachable" :set-caption="attachments.setCaption" :names="names" />

    <OptionMenu v-if="menu?.kind === 'priority' && item" :anchor="menu.anchor" title="Priority" :subject="item.key" kind="priority" :options="priorityOptions" :current="item.priority ?? ''" @choose="choosePriority" @close="closeMenu" />
    <AssigneeMenu v-if="menu?.kind === 'assignee' && item" :row="item" :project-id="project.id" :anchor="menu.anchor" :people="assigneeOptions" :can-assign="editable" @choose="chooseAssignee" @changed="emit('assigned')" @close="closeMenu" />
    <QueueView v-if="queueAnchor" :project-id="project.id" :anchor="queueAnchor" @close="restore => { const anchor = queueAnchor; queueAnchor = null; if (restore) anchor?.focus() }" @open="key => { queueAnchor = null; openLinked(key) }" @filter="() => { queueAnchor = null; void routerToQueued() }" />
    <StatusMenu :project-id="project.id" v-if="editMenu?.kind === 'status' && item" :anchor="editMenu.anchor" :derived="item.status_derived" :children-count="item.work_children_count" :current="draft.state" :known-states="editStatusOptions.map(option => option.value)" :ticket-key="item.key" @choose="value => chooseEdit('status', value)" @close="closeEditMenu" />
    <OptionMenu v-if="editMenu?.kind === 'priority' && item" :anchor="editMenu.anchor" title="Priority" :subject="item.key" kind="priority" :options="priorityOptions" :current="draft.priority" @choose="value => chooseEdit('priority', value)" @close="closeEditMenu" />
    <OptionMenu v-if="editMenu?.kind === 'assignee' && item" :anchor="editMenu.anchor" title="Assignee" :subject="item.key" kind="assignee" :options="assigneeOptions" :current="draft.assignee" searchable @choose="value => chooseEdit('assignee', value)" @close="closeEditMenu" />
    <RelationPicker
      v-if="linkAnchor && item" :anchor="linkAnchor" :subject="item.key" :self-id="item.id" :project-key="project.routeKey"
      :related="ticket.related.value" :link="ticket.link" @close="closeLink"
    />
    <EpicPicker v-if="menu?.kind === 'epic' && item" :anchor="menu.anchor" :project-id="project.id" :current="['work','epic'].includes(item.parent?.kind_slug ?? '') ? item.parent!.id : null" :subject="item.key" @choose="chooseEpic" @close="closeMenu" />
    <ReleasePicker v-if="menu?.kind === 'release' && item" :anchor="menu.anchor" :project-id="project.id" :subject="item.key" @choose="chooseRelease" @close="closeMenu" />
  </component>
</template>

<style scoped>
.q-card { margin-top: 14px; padding: 12px 14px; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.q-card.wait { background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); }
.q-card-acts { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 12px; }
.ticket-ws { display: flex; flex-direction: column; min-height: 0; outline: none; }
.ticket-ws.panel {
  position: fixed; z-index: 15; top: calc(var(--header-h) + 10px); right: 10px; bottom: calc(var(--footer-h) + 10px); width: min(560px, calc(100vw - 20px));
  border-radius: var(--radius); border: 1px solid var(--glass-edge);
  background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow);
  -webkit-backdrop-filter: blur(20px) saturate(1.15); backdrop-filter: blur(20px) saturate(1.15);
}
.ticket-ws.panel:focus-visible { box-shadow: var(--shadow-pop), var(--focus-ring); }
.ws-scroll { flex: 1; min-height: 0; overflow: auto; overscroll-behavior: contain; }
.panel .ws-scroll { padding: 18px 24px 24px; }
.ws-props { margin-top: 14px; }
.meta { margin-top: 12px; font-size: 12.5px; color: var(--ink-3); }
.meta time { color: var(--ink-2); }
.ws-eta { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.divider { height: 1px; margin: 18px 0 20px; background: linear-gradient(90deg, var(--line-2), transparent); }
.read-only { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; padding: 8px 12px; border-radius: 10px; background: var(--code-bg); font-size: 12.5px; color: var(--ink-2); }
.ws-benefits { margin-top: 26px; }
.add-sections { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 14px; }
.add-section { display: inline-flex; align-items: center; gap: 5px; height: 26px; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-3); font-size: 12px; }
@media (hover: hover) { .add-section:hover { color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--glass-rim); background: var(--row-hover); } }
.add-section:focus-visible { box-shadow: var(--focus-ring); }
.ws-block { margin-top: 28px; }
.ws-composer { flex-shrink: 0; padding: 10px 16px 12px; border-top: 1px solid var(--line); background: var(--surface-raised-2); border-radius: 0 0 var(--radius) var(--radius); }
.ws-state { display: grid; justify-items: center; gap: 8px; padding: 56px 16px; text-align: center; }
.ws-state h2 { font-size: 17px; }
.ws-state p { font-size: 13.5px; }
.ws-state .btn { margin-top: 8px; }
.state-icon { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 4px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.state-icon.danger { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.ws-skeleton { display: grid; gap: 14px; padding-top: 4px; }
.panel .ws-skeleton { padding: 0; }
.ws-skeleton .w30 { width: 30%; } .ws-skeleton .w90 { width: 90%; } .ws-skeleton .w80 { width: 80%; } .ws-skeleton .w60 { width: 60%; } .ws-skeleton .w85 { width: 85%; }
.ws-skeleton .title-skel { width: 88%; height: 18px; border-radius: 8px; }
.ws-skeleton .title-skel.short { width: 52%; }
.ws-skeleton .chips { display: flex; gap: 8px; margin: 4px 0 12px; }
.ws-skeleton .chip { width: 86px; height: 26px; border-radius: 999px; }

.ws-attachments { margin-top: 16px; }
/* Context column: activity, attachments and relations beside a clean reading column. */
.panel.has-context .ws-grid { display: grid; grid-template-columns: minmax(0, 1fr) clamp(300px, 36%, 400px); gap: 28px; align-items: start; }
.panel.has-context .ws-main { min-width: 0; max-width: 72ch; }
.ws-context { display: grid; grid-template-columns: minmax(0, 1fr); gap: 22px; min-width: 0; align-content: start; }
.panel .ws-context { padding-left: 24px; border-left: 1px solid var(--line); }
.ctx-block { min-width: 0; }
/* Edit mode: one form, one Save. */
.edit-form { display: grid; gap: 16px; }
.panel .edit-form { padding-bottom: 12px; }
.edit-title {
  width: 100%; min-height: 40px; padding: 6px 10px; border: 1px solid var(--glass-edge); border-radius: 10px; resize: none; overflow: hidden;
  background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); color: var(--ink); font: 650 20px/1.3 var(--font); letter-spacing: -.01em;
}
.edit-title.large { font-size: 28px; }
.edit-title:focus { box-shadow: var(--focus-ring); }
.hc-edit-hint { font-size: 12px; color: var(--ink-2); margin-top: 6px; }
.edit-props { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 10px; }
.edit-prop { display: grid; gap: 4px; }
.prop-label { font: 500 10px/1.5 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
/* Status, priority and assignee as form fields that open the app's own menus. */
.field-pick { display: flex; align-items: center; gap: 8px; height: 36px; padding: 0 10px 0 11px; color: var(--ink); font-size: 13.5px; text-align: left; cursor: pointer; }
.field-pick:hover { border-color: var(--chip-teal-line); }
.field-pick[aria-expanded="true"] { box-shadow: var(--focus-ring); }
.pick-value { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pick-value.unset, .pick-none { color: var(--ink-3); }
.pick-chev { flex-shrink: 0; color: var(--ink-3); }
.edit-section { display: grid; gap: 8px; }
.edit-section .eyebrow { margin: 0; }
.edit-hint { display: flex; flex-wrap: wrap; align-items: center; gap: 4px; font-size: 12px; color: var(--ink-3); }
.edit-live { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-radius: 10px; background: var(--code-bg); font-size: 12.5px; color: var(--ink-2); }
.edit-live svg { flex-shrink: 0; color: var(--teal-ink); }
/* Someone else's change: a brief full tint, never an edge accent (AEON-326). */
.live-tint { border-radius: 10px; animation: live-tint 2s ease-out; }
@keyframes live-tint { from { background-color: var(--row-selected); box-shadow: 0 0 0 6px var(--row-selected); } to { background-color: transparent; box-shadow: 0 0 0 6px transparent; } }
@media (prefers-reduced-motion: reduce) { .live-tint { animation: none; background-color: var(--row-hover); box-shadow: 0 0 0 6px var(--row-hover); } }
@media (max-width: 720px), (hover: none) { .edit-hint { display: none; } }
/* Full page editing is a focused writing layout: editor and preview side by side. */
.ticket-ws.full.editing { max-width: 1480px; }
.full .edit-form { padding: 26px 0 40px; }
/* Dropping files anywhere on the ticket. */
.drop-overlay { position: absolute; inset: 0; z-index: 30; display: grid; place-items: center; padding: 24px; border-radius: inherit; background: rgba(14, 111, 108, .12); box-shadow: inset 0 0 0 2px var(--teal); -webkit-backdrop-filter: blur(3px); backdrop-filter: blur(3px); pointer-events: none; }
.full .drop-overlay { position: fixed; inset: calc(var(--header-h) + 8px) 8px calc(var(--footer-h) + 8px); border-radius: var(--radius); }
.drop-card { display: grid; justify-items: center; gap: 6px; padding: 22px 28px; border-radius: 16px; background: var(--surface-raised); box-shadow: var(--shadow-pop); color: var(--ink); text-align: center; }
.drop-card svg { color: var(--teal); }
.drop-card span { font-size: 12.5px; color: var(--ink-2); }
.ticket-ws { position: relative; }
.ticket-ws.panel { position: fixed; }

/* Full page: content left at a readable measure, properties and relations right. */
.ticket-ws.full { width: 100%; max-width: 1160px; min-height: 100%; margin: 0 auto; }
.full .ws-scroll { overflow: visible; }
.full .ws-grid { display: grid; grid-template-columns: minmax(0, 1fr) 320px; gap: 40px; align-items: start; padding: 26px 0 40px; }
/* Three columns: a ~72ch reading column, the context (attachments, relations,
   activity) and the properties; the set stays together in the middle. */
.ticket-ws.full.has-context { max-width: 1480px; }
.full.has-context .ws-grid { grid-template-columns: minmax(0, 1fr) clamp(340px, 24vw, 440px) 300px; gap: 44px; }
.full.has-context .ws-main { max-width: none; }
.full.has-context .ws-context { position: sticky; top: 16px; max-height: calc(100dvh - var(--header-h) - var(--footer-h) - 32px); overflow: auto; padding-right: 4px; }
.full .ws-main { min-width: 0; max-width: 820px; }
.full .sections :deep(.markdown-body), .full .inline-composer, .full .activity, .full .children { max-width: 72ch; }
.full .ws-side { position: sticky; top: 16px; display: grid; gap: 14px; }
.side-card { padding: 14px 18px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised-2), var(--glass) 60%); box-shadow: var(--shadow); }
.side-card :deep(.relation-label) { width: 84px; }
.full .panel-bar { border-bottom: 0; padding: 0; height: 44px; }
@media (min-width: 1100px) { .ticket-ws.panel { width: var(--panel-w); } }
/* On the page canvas a tint reads muddy; comments get a light raised fill instead. */
.full :deep(.comment-card) { background: var(--comment-page-bg); box-shadow: var(--comment-page-edge); }
.full .only-narrow { display: none; }
@media (max-width: 980px) {
  .full .ws-grid { grid-template-columns: minmax(0, 1fr); padding-top: 14px; }
  .full .ws-side { display: none; }
  .full .only-narrow { display: revert; }
  .full .ws-props.only-narrow { display: flex; }
}
@media (prefers-reduced-motion: no-preference) {
  .ticket-ws.panel { animation: panel-in .22s cubic-bezier(.2, .7, .2, 1); }
  @keyframes panel-in { from { opacity: 0; transform: translateX(24px); } to { opacity: 1; transform: none; } }
}
@media (max-width: 720px) {
  .ticket-ws.panel { z-index: 40; inset: 0; width: auto; height: 100dvh; border-radius: 0; border: 0; background: var(--canvas); }
  .panel .ws-scroll { padding: 16px 18px 24px; }
  .ws-composer { border-radius: 0; padding: 8px 12px calc(8px + env(safe-area-inset-bottom)); background: var(--surface-raised); }
  @media (prefers-reduced-motion: no-preference) { .ticket-ws.panel { animation-name: sheet-in; } @keyframes sheet-in { from { transform: translateY(24px); opacity: 0; } to { transform: none; opacity: 1; } } }
}
</style>
