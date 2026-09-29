<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, toRef, useId, watch } from 'vue'
import { APIError, undoEvent, type ListItem } from '../../lib/api'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { useActivity } from '../../lib/useActivity'
import { useTicket, type RelatedNode } from '../../lib/useTicket'
import { absoluteTime, kindLabel, priorityLabel, relativeTime, statusMeta, statusOptions } from '../../lib/work'
import { useAttachments } from '../../lib/useAttachments'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import ActivityTimeline from './ActivityTimeline.vue'
import AttachmentLightbox from './AttachmentLightbox.vue'
import AttachmentStrip from './AttachmentStrip.vue'
import MarkdownEditor from './MarkdownEditor.vue'
import ChildList from './ChildList.vue'
import CommentComposer from './CommentComposer.vue'
import EpicPicker from './EpicPicker.vue'
import InlineTitle from './InlineTitle.vue'
import MarkdownSection from './MarkdownSection.vue'
import OptionMenu, { type MenuOption } from './OptionMenu.vue'
import PersonAvatar from './PersonAvatar.vue'
import PriorityIcon from './PriorityIcon.vue'
import StatusIcon from './StatusIcon.vue'
import StatusMenu from './StatusMenu.vue'
import RelationList from './RelationList.vue'
import RelationPicker from './RelationPicker.vue'
import TicketAgentWork from './TicketAgentWork.vue'
import TicketOutcomes from './TicketOutcomes.vue'
import TicketHeaderBar from './TicketHeaderBar.vue'
import TicketProperties from './TicketProperties.vue'
import TicketBenefits from './TicketBenefits.vue'
import { needsBenefitPrompt } from '../../lib/doneGate'
import { benefitDraft, benefitTextKeys, completedTicketState, firstBenefitGap } from '../../lib/ticketBenefits'
import { can } from '../../lib/authz'
import { AssignCancelled, assignToRelease, type ReleaseTarget } from '../../lib/releaseAssign'
import { openedMembershipMessage, type NativeReleaseView } from '../../lib/releaseMembership'
import ReleasePicker from './ReleasePicker.vue'
import { useJourney } from '../../stores/journey'
import StartAgentDialog from '../agents/StartAgentDialog.vue'

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

const item = toRef(props, 'item')
const ticket = useTicket(item, {
  names: props.names,
  onRemoved: removed => emit('removed', removed),
  onCreated: created => emit('created', created),
  onMoved: (moved, fromParent) => emit('moved', moved, fromParent),
})
const activity = useActivity(computed(() => props.item?.id ?? null))
const editable = computed(() => props.canWrite && !ticket.readOnly.value && !ticket.gone.value)
const deletable = computed(() => props.canDelete && !ticket.readOnly.value && !ticket.gone.value)
const movable = computed(() => props.canMove && !ticket.readOnly.value && !ticket.gone.value)
const linkable = computed(() => props.canLink && !ticket.readOnly.value && !ticket.gone.value)
const unlinkable = computed(() => props.canUnlink && !ticket.readOnly.value && !ticket.gone.value)
const commentable = computed(() => props.canComment && !ticket.readOnly.value && !ticket.gone.value)
const commentDeletable = computed(() => props.canDeleteComment && !ticket.readOnly.value && !ticket.gone.value)
const attachable = computed(() => props.canAttach && !ticket.readOnly.value && !ticket.gone.value)
const attachments = useAttachments(computed(() => props.item?.id ?? null))
const lightbox = ref<InstanceType<typeof AttachmentLightbox>>()
const startDialog = ref<InstanceType<typeof StartAgentDialog>>()
const canStartAgent = computed(() => props.item?.kind_slug === 'ticket' && !ticket.gone.value && can('work_orders.write') && can('run.create'))
const canRelease = computed(() => editable.value && !!props.item && props.item.kind_slug !== 'epic' && can('releases.write', props.project.id))
const releaseView = computed(() => props.item?.kind_slug === 'epic' ? { status: 'none' as const } : props.nativeReleases?.get(props.item?.id ?? ''))
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
onBeforeUnmount(() => { releaseChoiceAlive = false; wideQuery.removeEventListener('change', onWide); sizer?.disconnect() })
const contextColumn = computed(() => props.mode === 'full' ? wideScreen.value : width.value >= 860)

// ---------- Edit mode: title, text and properties together, one Save ----------
const editing = ref(false)
const saving = ref(false)
const benefitNotice = ref('')
const benefitInvalidKey = ref('')
const titleField = ref<HTMLTextAreaElement>()
const draft = reactive({ ...benefitDraft({}), title: '', body: '', acceptance: '', notes: '', state: '', priority: '', assignee: '' })
let base = { ...draft }
function snapshot() {
  const it = props.item!
  return {
    ...benefitDraft(it.fields), title: it.title, body: it.body ?? '', acceptance: typeof it.fields.acceptance_criteria === 'string' ? it.fields.acceptance_criteria : '',
    notes: typeof it.fields.notes === 'string' ? it.fields.notes : '', state: it.state, priority: it.priority && it.priority !== 'none' ? it.priority : '', assignee: it.assignee?.id ?? '',
  }
}
const editDirty = computed(() => editing.value && (Object.keys(base) as (keyof typeof base)[]).some(key => draft[key] !== base[key]))
const editStatusOptions = computed(() => {
  const options = statusOptions([props.item?.state ?? ''])
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
  const target = props.item
  if (!benefitNotice.value || !target) return
  if (!needsBenefitPrompt({ kind_slug: target.kind_slug, state: base.state, fields: draft }, draft.state)) {
    benefitNotice.value = ''
    benefitInvalidKey.value = ''
    return
  }
  const gap = firstBenefitGap(draft)
  benefitNotice.value = gap?.line ?? ''
  benefitInvalidKey.value = gap?.key ?? ''
}
async function startEdit(focus: 'title' | 'body' | 'benefit' = 'title') {
  if (!editable.value || !props.item || editing.value) return
  base = snapshot(); Object.assign(draft, base)
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
  const target = props.item
  if (!target || saving.value) return
  if (!draft.title.trim()) { toast('A title is needed.', { tone: 'error' }); titleField.value?.focus(); return }
  if (!editDirty.value) { editing.value = false; return }
  const patch: Record<string, unknown> = {}
  if (draft.title.trim() !== base.title) patch.title = draft.title.trim()
  if (draft.body !== base.body) patch.body = draft.body
  if (draft.state !== base.state) patch.state = draft.state
  const fields = { ...target.fields }
  let fieldsChanged = false
  const setField = (name: string, value: string, before: string) => { if (value === before) return; fieldsChanged = true; if (value.trim()) fields[name] = value; else delete fields[name] }
  setField('acceptance_criteria', draft.acceptance, base.acceptance)
  setField('notes', draft.notes, base.notes)
  setField('priority', draft.priority, base.priority)
  setField('assignee', draft.assignee, base.assignee)
  if (target.kind_slug === 'ticket') {
    for (const key of benefitTextKeys) setField(key, draft[key], base[key])
    if (draft.hide_from_release_notes !== base.hide_from_release_notes) { fieldsChanged = true; fields.hide_from_release_notes = draft.hide_from_release_notes }
  }
  if (fieldsChanged) patch.fields = fields
  // The benefit editor is already on this form. Point at the first incomplete field.
  if (needsBenefitPrompt({ kind_slug: target.kind_slug, state: base.state, fields }, draft.state)) {
    const gap = firstBenefitGap(fields)
    benefitNotice.value = gap?.line ?? 'A 2–4 word pill and a benefit, in both languages, are required.'
    benefitInvalidKey.value = gap?.key ?? 'pill_en'
    await nextTick()
    focusBenefit(benefitInvalidKey.value)
    return
  }
  benefitNotice.value = ''
  benefitInvalidKey.value = ''
  saving.value = true
  const result = await ticket.patch(patch)
  saving.value = false
  if (result === 'ok') {
    if (draft.assignee !== base.assignee) {
      const option = assigneeOptions.value.find(o => o.value === draft.assignee)
      target.assignee = draft.assignee && option ? { id: draft.assignee, name: option.label } : null
      if (target.assignee) props.names.set(target.assignee.id, target.assignee.name)
    }
    editing.value = false
    toast(`Saved ${target.key}`)
    void nextTick(() => root.value?.focus({ preventScroll: true }))
  } else if (result === 'conflict') {
    // The newer version is loaded; the draft stays, compared against it from now on.
    base = snapshot()
  }
}
async function cancelEdit() {
  if (editDirty.value && !(await confirmAction({ title: 'Discard your changes?', body: `Your edits to ${props.item?.key ?? 'this ticket'} have not been saved.`, confirmLabel: 'Discard', danger: true }))) return
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
watch(() => props.item?.id, () => { editing.value = false })
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
const composer = ref<InstanceType<typeof CommentComposer>>()
const timeline = ref<InstanceType<typeof ActivityTimeline>>()
const menu = ref<{ kind: 'priority' | 'assignee' | 'epic' | 'release'; anchor: HTMLElement } | null>(null)
const showAcceptance = ref(false)
const showNotes = ref(false)

const acceptance = computed(() => typeof props.item?.fields.acceptance_criteria === 'string' ? props.item.fields.acceptance_criteria : '')
const notes = computed(() => typeof props.item?.fields.notes === 'string' ? props.item.fields.notes : '')
const hasChildren = computed(() => !!props.item && (props.item.kind_slug === 'epic' || props.item.children_count > 0 || ticket.children.value.length > 0))
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
  showAcceptance.value = false; showNotes.value = false; menu.value = null; linkAnchor.value = null
  scroller.value?.scrollTo({ top: 0 })
})

function link() { return `${location.origin}/p/${encodeURIComponent(props.project.routeKey)}/${encodeURIComponent(props.item?.key ?? props.ticketKey)}` }
function copy(text: string, label: string) {
  navigator.clipboard.writeText(text).then(() => toast(`Copied ${label}`), () => toast(`${label} could not be copied`, { tone: 'error' }))
}
// The visible control for a key: narrow and wide layouts both render some of them.
function anchorFor(shortcut: string) {
  return [...(root.value?.querySelectorAll<HTMLElement>(`[aria-keyshortcuts="${shortcut}"]`) ?? [])].find(el => el.getClientRects().length) ?? null
}
function openMenu(kind: 'priority' | 'assignee' | 'epic' | 'release', anchor: HTMLElement | null) {
  if (!anchor) return
  if (kind === 'release' ? canRelease.value : kind === 'epic' ? movable.value : editable.value) menu.value = { kind, anchor }
}
async function chooseRelease(target: ReleaseTarget) {
  const it = props.item
  const projectId = props.project.id
  if (!it || !canRelease.value) return
  const ticket = { id: it.id, key: it.key, title: it.title, state: it.state, kind: it.kind_slug }
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
    toast(`Added ${ticket.key} to ${title}.${skipped}`, {
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
  if (!anchor || !linkable.value || !props.item) return
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
  const target = props.item
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
  const target = props.item
  if (!target || !deletable.value) return
  const ok = await confirmAction({
    title: `Delete ${target.key}?`,
    body: `“${target.title}” leaves the project list. ${target.children_count ? 'Its children must be moved or deleted first.' : 'The history stays in the audit log.'}`,
    confirmLabel: `Delete ${kindLabel(target.kind_slug).toLowerCase()}`, danger: true,
  })
  if (ok) await ticket.remove()
}
async function addSection(kind: 'acceptance' | 'notes') {
  if (kind === 'acceptance') showAcceptance.value = true; else showNotes.value = true
  await nextTick()
  ;(kind === 'acceptance' ? acSection.value : notesSection.value)?.start()
}

function isDirty() {
  return editDirty.value || !!title.value?.isDirty() || sections.value.some(section => section.isDirty()) || !!composer.value?.isDirty() || !!timeline.value?.isDirty()
}
function focus() { root.value?.focus({ preventScroll: true }) }
defineExpose({
  el: root, focus, isDirty,
  editTitle: () => title.value?.start(),
  startEdit, editing,
  openStatus: () => { const anchor = anchorFor('s'); if (anchor && editable.value) emit('status', anchor) },
  openPriority: () => openMenu('priority', anchorFor('p')),
  openAssignee: () => openMenu('assignee', anchorFor('a')),
  openRelease: () => openMenu('release', anchorFor('g')),
  openLink: () => openLink(anchorFor('r')),
  focusComposer: () => composer.value?.focus(),
})
</script>

<template>
  <component
    :is="mode === 'panel' ? 'aside' : 'article'" ref="root" class="ticket-ws" :class="[mode, { 'has-context': contextColumn && !editing, editing }]" aria-label="Ticket details" tabindex="-1"
    @click.capture="rememberClick" @dragenter="dragEnter" @dragover="dragOverRoot" @dragleave="dragLeaveRoot" @drop="dropFiles" @paste="pasteFiles"
  >
    <TicketHeaderBar
      :ticket-key="item?.key ?? ticketKey" :kind="item?.kind_slug ?? null" :position="position" :mode="mode" :can-write="editable"
      :can-delete="deletable" :can-move="movable && item?.kind_slug === 'ticket'" :trail="trail" :editing="editing" :saving="saving" :dirty="editDirty"
      :can-start-agent="canStartAgent" :open-in-project="openInProject" :back-label="backLabel"
      @copy-key="copy(item?.key ?? ticketKey, item?.key ?? ticketKey)" @copy-link="copy(link(), 'link')" @prev="emit('prev')" @next="emit('next')"
      @expand="emit('expand')" @collapse="emit('collapse')" @new-tab="emit('newTab')" @close="emit('close')" @open-in-project="emit('openInProject')"
      @move="anchor => openMenu('epic', anchor)" @delete="remove" @back="steps => emit('trailBack', steps)"
      @edit="startEdit()" @save="saveEdit" @cancel="cancelEdit"
      @start-agent="item && startDialog?.open(item)"
    />
    <StartAgentDialog ref="startDialog" />

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
        <label class="sr-only" for="edit-title">Title</label>
        <textarea id="edit-title" ref="titleField" v-model="draft.title" class="edit-title" :class="{ large: mode === 'full' }" rows="1" maxlength="500" placeholder="Title" @input="growTitle" @keydown.enter.exact.prevent />
        <div class="edit-props">
          <div class="edit-prop"><span :id="`${uid}-status`" class="prop-label">Status</span>
            <button
              type="button" class="field field-pick" aria-haspopup="menu" :aria-expanded="editMenu?.kind === 'status'" :aria-labelledby="`${uid}-status ${uid}-status-value`"
              @click="openEditMenu('status', $event)" @keydown="editMenuKeys('status', $event)"
            ><StatusIcon :state="draft.state" /><span :id="`${uid}-status-value`" class="pick-value">{{ statusMeta(draft.state).label }}</span><AppIcon name="chevron" :size="12" class="pick-chev" /></button>
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
        <section class="edit-section" aria-labelledby="edit-notes"><h3 id="edit-notes" class="eyebrow">Notes</h3>
          <MarkdownEditor v-model="draft.notes" label="Notes" bare :split="mode === 'full'" :min-rows="3" :attachment-id="attachmentId" @save="saveEdit" @cancel="cancelEdit" />
        </section>
        <TicketBenefits v-if="item.kind_slug === 'ticket'" class="edit-benefits" :fields="draft" editing :disabled="saving" :done="completedTicketState(item.state)" :notice="benefitNotice" :invalid-key="benefitInvalidKey" @change="changeBenefit" />
        <p class="edit-hint"><KeyCap k="mod" /><KeyCap k="enter" /> save · <kbd class="keycap">esc</kbd> cancel · paste or drop images to attach them</p>
      </form>

      <div v-else class="ws-grid">
        <div class="ws-main">
          <p v-if="!editable" class="read-only" role="note"><AppIcon name="alert" :size="13" />You can read this {{ kindLabel(item.kind_slug).toLowerCase() }} but not change it.</p>
          <InlineTitle ref="title" :value="item.title" :editable="editable" :large="mode === 'full'" :save="ticket.setTitle" />
          <TicketProperties
            class="ws-props" :class="{ 'only-narrow': mode === 'full' }" :item="item" :editable="editable" layout="row" :now="now"
            :release-view="releaseView" :release-editable="canRelease"
            @status="anchor => emit('status', anchor)" @priority="anchor => openMenu('priority', anchor)" @assignee="anchor => openMenu('assignee', anchor)"
            @epic="anchor => openMenu('epic', anchor)" @release="anchor => openMenu('release', anchor)" @open-parent="openLinked"
          />
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
            <MarkdownSection ref="descSection" title="Description" :value="item.body" :editable="editable" :save="ticket.setBody" :attachment-id="attachable ? attachmentId : undefined" empty-text="Add a description" @open-attachment="openAttachment" />
            <MarkdownSection v-if="acceptance.trim() || showAcceptance" ref="acSection" title="Acceptance criteria" :value="acceptance" :editable="editable" :save="value => ticket.setField('acceptance_criteria', value)" :attachment-id="attachable ? attachmentId : undefined" @open-attachment="openAttachment" />
            <MarkdownSection v-if="notes.trim() || showNotes" ref="notesSection" title="Notes" :value="notes" :editable="editable" :save="value => ticket.setField('notes', value)" :attachment-id="attachable ? attachmentId : undefined" @open-attachment="openAttachment" />
            <div v-if="editable && (!(acceptance.trim() || showAcceptance) || !(notes.trim() || showNotes))" class="add-sections">
              <button v-if="!(acceptance.trim() || showAcceptance)" type="button" class="add-section" @click="addSection('acceptance')"><AppIcon name="plus" :size="12" />Acceptance criteria</button>
              <button v-if="!(notes.trim() || showNotes)" type="button" class="add-section" @click="addSection('notes')"><AppIcon name="plus" :size="12" />Notes</button>
            </div>
            <TicketBenefits v-if="item.kind_slug === 'ticket'" class="ws-benefits" :fields="item.fields" :done="completedTicketState(item.state)" :editable="editable" @edit="startEdit('benefit')" />
          </div>

          <TicketAgentWork v-if="item.kind_slug === 'ticket' || item.kind_slug === 'epic' || item.kind_slug === 'task'" class="ws-block" :node-id="item.id" :kind="item.kind_slug" />
          <TicketOutcomes v-if="item.kind_slug === 'ticket'" class="ws-block" :node-id="item.id" />
          <ChildList
            v-if="hasChildren" class="ws-block" :children="ticket.children.value" :loading="ticket.childrenLoading.value" :editable="editable"
            :child-label="item.kind_slug === 'epic' ? 'ticket' : 'task'" :progress="ticket.childProgress()" :add="title => ticket.addChild(title, project.routeKey)"
            @open="openLinked"
          />
          <!-- Relations, then activity: both wait for the relations, so neither jumps. -->
          <template v-if="!contextColumn && ticket.relationsReady.value">
            <RelationList class="ws-block" :class="{ 'only-narrow': mode === 'full' }" :related="ticket.related.value" :editable="linkable" :removable="unlinkable" :unlink="unlinkEntry" @open="openLinked" @link="openLink" />
            <ActivityTimeline
              ref="timeline" class="ws-block" :entries="activity.timeline.value" :loading="activity.loading.value" :loading-older="activity.loadingOlder.value"
              :has-older="!!activity.cursor.value" :error="activity.error.value" :me="me?.id" :now="now" :can-write="commentable" :can-delete="commentDeletable"
              :edit="activity.edit" :remove="activity.remove" @older="activity.loadOlder" @retry="activity.load"
            />
            <CommentComposer v-if="mode === 'full'" ref="composer" class="ws-block inline-composer" :me="me?.name ?? '?'" :me-id="me?.id ?? null" :post="activity.add" :disabled="!commentable" />
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
          <ActivityTimeline
            ref="timeline" class="ctx-block" :entries="activity.timeline.value" :loading="activity.loading.value" :loading-older="activity.loadingOlder.value"
            :has-older="!!activity.cursor.value" :error="activity.error.value" :me="me?.id" :now="now" :can-write="commentable" :can-delete="commentDeletable"
            :edit="activity.edit" :remove="activity.remove" @older="activity.loadOlder" @retry="activity.load"
          />
          <CommentComposer v-if="mode === 'full'" ref="composer" class="ctx-block inline-composer" :me="me?.name ?? '?'" :me-id="me?.id ?? null" :post="activity.add" :disabled="!commentable" />
          </template>
        </aside>

        <aside v-if="mode === 'full'" class="ws-side" aria-label="Properties">
          <div class="side-card">
            <TicketProperties
              :item="item" :editable="editable" layout="column" :now="now"
              :release-view="releaseView" :release-editable="canRelease"
              @status="anchor => emit('status', anchor)" @priority="anchor => openMenu('priority', anchor)" @assignee="anchor => openMenu('assignee', anchor)"
              @epic="anchor => openMenu('epic', anchor)" @release="anchor => openMenu('release', anchor)" @open-parent="openLinked"
            />
          </div>
          <div v-if="(ticket.related.value.length || linkable) && !contextColumn && ticket.relationsReady.value" class="side-card"><RelationList :related="ticket.related.value" :editable="linkable" :removable="unlinkable" :unlink="unlinkEntry" @open="openLinked" @link="openLink" /></div>
        </aside>
      </div>
    </div>

    <footer v-if="mode === 'panel' && item && !ticket.gone.value && !editing" class="ws-composer">
      <CommentComposer ref="composer" :me="me?.name ?? '?'" :me-id="me?.id ?? null" :post="activity.add" :disabled="!commentable" />
    </footer>

    <div v-if="dropping" class="drop-overlay" aria-hidden="true">
      <div class="drop-card"><AppIcon name="upload" :size="22" /><strong>Drop to attach to {{ item?.key ?? ticketKey }}</strong><span>Images show as thumbnails; other files as cards.</span></div>
    </div>
    <AttachmentLightbox ref="lightbox" :items="attachments.items.value" :ticket-key="item?.key ?? ticketKey" :can-write="attachable" :set-caption="attachments.setCaption" :names="names" />

    <OptionMenu v-if="menu?.kind === 'priority' && item" :anchor="menu.anchor" title="Priority" :subject="item.key" kind="priority" :options="priorityOptions" :current="item.priority ?? ''" @choose="choosePriority" @close="closeMenu" />
    <OptionMenu v-if="menu?.kind === 'assignee' && item" :anchor="menu.anchor" title="Assignee" :subject="item.key" kind="assignee" :options="assigneeOptions" :current="item.assignee?.id ?? ''" searchable @choose="chooseAssignee" @close="closeMenu" />
    <StatusMenu v-if="editMenu?.kind === 'status' && item" :anchor="editMenu.anchor" :current="draft.state" :known-states="editStatusOptions.map(option => option.value)" :ticket-key="item.key" @choose="value => chooseEdit('status', value)" @close="closeEditMenu" />
    <OptionMenu v-if="editMenu?.kind === 'priority' && item" :anchor="editMenu.anchor" title="Priority" :subject="item.key" kind="priority" :options="priorityOptions" :current="draft.priority" @choose="value => chooseEdit('priority', value)" @close="closeEditMenu" />
    <OptionMenu v-if="editMenu?.kind === 'assignee' && item" :anchor="editMenu.anchor" title="Assignee" :subject="item.key" kind="assignee" :options="assigneeOptions" :current="draft.assignee" searchable @choose="value => chooseEdit('assignee', value)" @close="closeEditMenu" />
    <RelationPicker
      v-if="linkAnchor && item" :anchor="linkAnchor" :subject="item.key" :self-id="item.id" :project-key="project.routeKey"
      :related="ticket.related.value" :link="ticket.link" @close="closeLink"
    />
    <EpicPicker v-if="menu?.kind === 'epic' && item" :anchor="menu.anchor" :project-id="project.id" :current="item.parent?.kind_slug === 'epic' ? item.parent.id : null" :subject="item.key" @choose="chooseEpic" @close="closeMenu" />
    <ReleasePicker v-if="menu?.kind === 'release' && item" :anchor="menu.anchor" :project-id="project.id" :subject="item.key" @choose="chooseRelease" @close="closeMenu" />
  </component>
</template>

<style scoped>
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
