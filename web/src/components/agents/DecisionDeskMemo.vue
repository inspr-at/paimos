<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { answerFor, deskProject, draftFor, fieldTarget, kindLabels, projectColor, projectSwitch, type DeskProject, macPlatform, outcomeLabels, outcomeUnavailable, roundCounts, submitModifier, type DeskDraft, type DeskItem, type DeskOutcome } from '../../lib/decisionDesk'
import { deliveryTime, loadDeskContext, type DeskContext } from '../../lib/decisionDeskApi'
import { RISK_LABEL, riskFor, scopeLabel } from '../../lib/agentState'
import { contentUrl, fileKind, hasThumbnail } from '../../lib/attachments'
import AttachmentLightbox from '../work/AttachmentLightbox.vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import TargetSummary from '../deploy/TargetSummary.vue'
import { outcomeLine } from '../../lib/ticketOutcomes'
import { vClipTip } from '../../lib/clipTip'

const props = defineProps<{
  items: DeskItem[]; round: string[]; start: string; arrivalsCount: number; pendingRuleIds?: readonly string[]; allowed: (item: DeskItem) => boolean
  decide: (item: DeskItem, draft: DeskDraft, requestId: string) => Promise<DeskItem>
  canRevoke?: (item: DeskItem) => boolean; revoke?: (item: DeskItem) => Promise<DeskItem>
  editRule?: (item: DeskItem) => Promise<void>
}>()
const emit = defineEmits<{ close: []; recorded: [item: DeskItem] }>()
const dialog = ref<HTMLDialogElement>(), memo = ref<HTMLElement>(), decideButton = ref<HTMLButtonElement>(), pagerButton = ref<HTMLButtonElement>()
const frame = ref<HTMLElement>(), body = ref<HTMLElement>(), scrollingHeight = ref<number>()
const index = ref(Math.max(0, props.round.indexOf(props.start))), pager = ref(false), jumpIndex = ref(0), busy = ref(false), status = ref(''), error = ref(''), editing = ref(''), slam = ref(false)
const drafts = ref<Record<string, DeskDraft>>({}), skipped = ref(new Set<string>()), context = ref<DeskContext>({ attachments: [], related: [], outcomes: [], warnings: [] }), contextLoading = ref(false)
const lightbox = ref<InstanceType<typeof AttachmentLightbox>>(), viewing = ref(false), brokenThumbs = ref(new Set<string>())
const item = computed(() => props.items.find(item => item.id === props.round[index.value]))
const draft = computed(() => item.value ? drafts.value[item.value.id] : undefined)
const selectedChoice = computed(() => item.value?.choices.find(choice => choice.id === draft.value?.optionId))
const titleClipped = ref(false)
// AEON-1057: the project of the item shown before, kept for this desk session,
// so a change of project is named however the person moved (J/K, jump list, Decide & next).
const project = computed(() => item.value ? deskProject(item.value) : undefined)
const switchedFrom = ref<DeskProject>()
let shown: { id: string; project: DeskProject } | undefined
// The folder tab is sized when the memo opens or the window resizes, never per item.
const tabWidth = ref(360)
const tabPath = computed(() => { const w = tabWidth.value; return `M0 46V12Q0 0 12 0H${w - 62}Q${w - 50} 0 ${w - 46} 13L${w - 39} 32Q${w - 35} 46 ${w - 21} 46Z` })
const tabOutline = computed(() => { const w = tabWidth.value; return `M.5 45.5V12 Q.5 .5 12 .5H${w - 62} Q${w - 50} .5 ${w - 46} 13L${w - 39} 32Q${w - 35} 45.5 ${w - 21} 45.5` })
function sizeTab() { tabWidth.value = window.matchMedia('(max-width: 720px)').matches ? Math.min(360, window.innerWidth - 28) : 360 }
sizeTab(); window.addEventListener('resize', sizeTab)
const roundProjects = computed(() => new Set(props.round.map(id => props.items.find(row => row.id === id)?.projectId).filter(Boolean)).size)
function jumpProject(at: number) { const row = props.items.find(row => row.id === props.round[at]); return row ? deskProject(row) : undefined }
function projectChange(at: number) {
  const before = at ? jumpProject(at - 1) : undefined, here = jumpProject(at)
  return !!before && !!here && before.id !== here.id
}
function openProject() {
  const href = project.value?.href
  if (!href) return
  // A new tab without access back to the desk; the desk stays as it is.
  const opened = window.open(href, '_blank')
  if (opened) opened.opener = null
}
function measureTitle(clipped: boolean) { titleClipped.value = clipped }
const counts = computed(() => roundCounts(props.items, props.round, skipped.value))
const mac = macPlatform(navigator.platform), submitKey = mac ? 'Cmd+Enter' : 'Ctrl+Enter'
const outcomes: DeskOutcome[] = ['once', 'always', 'requirement', 'doctrine']
const outcomeKeys = ['O', 'A', 'R', 'D']
const expired = ref(false)
let contextGeneration = 0, live = true, expiryTimer: ReturnType<typeof setTimeout> | undefined, opener: HTMLElement | null = null
let sizeObserver: ResizeObserver | undefined
watch(frame, element => {
  sizeObserver?.disconnect()
  if (!element) return
  // Pattern A grows downward until the body needs scrolling. From that point
  // the series is pattern B and keeps its frame, even for a later short memo.
  sizeObserver = new ResizeObserver(() => {
    if (scrollingHeight.value || window.matchMedia('(max-width: 720px)').matches || !body.value) return
    if (body.value.scrollHeight > body.value.clientHeight + 1) scrollingHeight.value = element.getBoundingClientRect().height
  })
  sizeObserver.observe(element)
  if (body.value) sizeObserver.observe(body.value)
})
const blocked = computed(() => !item.value || !props.allowed(item.value) || !!item.value.unavailable || expired.value || busy.value)
const trimDeclinable = computed(() => item.value?.kind === 'key_trim' && !item.value.decided && props.allowed(item.value) && !expired.value && !busy.value && (!item.value.unavailable || item.value.unavailable === item.value.keyTrim?.blocked_reason))
const primaryDisabled = computed(() => busy.value || ((!item.value?.decided || trimRestorable.value) && (blocked.value || (item.value?.kind !== 'key_trim' && (!draft.value?.optionId || !!item.value?.choices.find(choice => choice.id === draft.value?.optionId)?.unavailable)))))
const canEdit = computed(() => !!item.value && (!item.value.decided || item.value.kind === 'question' || item.value.kind === 'handover'))
const trimRestorable = computed(() => item.value?.keyTrim?.state === 'applied' && !!item.value.keyTrim.restore_until && Date.parse(item.value.keyTrim.restore_until) > Date.now())
const primary = computed(() => item.value?.kind === 'key_trim' ? (!item.value.decided ? 'Approve' : trimRestorable.value ? 'Restore' : 'Next') : item.value?.decided && !draft.value?.dirty ? 'Next' : item.value?.decided ? 'Replace & next' : 'Decide & next')
const announcement = ref('')
const trimRequests = new Map<string, string>()
const plainText = (value: unknown) => typeof value === 'string' ? value : 'Not supplied'
function doctrineReviewPath(id: string, state?: string) {
  return state === 'pending' || (!state && props.pendingRuleIds?.includes(id))
    ? `/decision-desk?item=r:${encodeURIComponent(id)}` : '/settings/agent-rules'
}
let pendingDecisionFocus: { id: string; revision: number } | undefined
watch([() => item.value?.id, () => item.value?.revision, primaryDisabled], ([id, revision, disabled]) => {
  const pending = pendingDecisionFocus
  if (!pending) return
  if (id !== pending.id || revision !== pending.revision) { pendingDecisionFocus = undefined; return }
  if (!disabled) {
    pendingDecisionFocus = undefined
    // A permission read may finish after the next memo renders. Restore the
    // action only if the person has not already moved to another control.
    if (live && document.activeElement === memo.value) decideButton.value?.focus({ preventScroll: true })
  }
}, { flush: 'post' })
function focusDecision() {
  pendingDecisionFocus = undefined
  if (!decideButton.value || !item.value) return
  if (decideButton.value.disabled) {
    pendingDecisionFocus = { id: item.value.id, revision: item.value.revision }
    memo.value?.focus({ preventScroll: true })
  } else decideButton.value.focus({ preventScroll: true })
}
watch(item, async (current, previous) => {
  const turn = ++contextGeneration
  if (current && current.id !== shown?.id) {
    const here = deskProject(current)
    switchedFrom.value = projectSwitch(shown?.project, here); shown = { id: current.id, project: here }
    if (switchedFrom.value) announcement.value = `${announcement.value} Now in ${here.name}. The item before was in ${switchedFrom.value.name}.`.trim()
  }
  if (current && (!drafts.value[current.id] || current.decided && current.id === previous?.id && !previous.decided && current.kind !== 'question' && current.kind !== 'handover')) drafts.value[current.id] = draftFor(current)
  error.value = ''; status.value = ''; editing.value = ''; pager.value = false; brokenThumbs.value = new Set()
  context.value = { attachments: [], related: [], outcomes: [], warnings: [] }; contextLoading.value = false
  clearTimeout(expiryTimer)
  expired.value = !!current?.expiresAt && Date.parse(current.expiresAt) <= Date.now()
  if (current?.expiresAt && !expired.value) expiryTimer = setTimeout(() => { expired.value = true; status.value = 'This request expired. No permission was granted.'; announcement.value = status.value }, Math.min(2_147_483_647, Math.max(0, Date.parse(current.expiresAt) - Date.now())))
  if (current?.ticketId || current?.ticketKey) {
    contextLoading.value = true
    const result = await loadDeskContext(current.ticketId ?? '', current.ticketKey)
    if (live && turn === contextGeneration && item.value?.id === current.id) { context.value = result; contextLoading.value = false }
  }
}, { immediate: true })
function touch() { if (draft.value) draft.value.dirty = true; status.value = ''; error.value = '' }
async function focusField(name: string) {
  editing.value = name
  await nextTick()
  const field = dialog.value?.querySelector<HTMLInputElement>(`[data-field="${name}"]`)
  field?.focus(); field?.setSelectionRange(field.value.length, field.value.length)
}
function finishEditing() {
  const label = editing.value === 'answer' ? 'answer' : item.value?.kind === 'approval' || item.value?.kind === 'rule' ? 'reason' : 'note'
  editing.value = ''; status.value = `Your ${label} is set. Enter decides.`; announcement.value = status.value
  memo.value?.focus({ preventScroll: true })
}
function choose(id: string) {
  if (blocked.value || !canEdit.value || !draft.value) return
  if (item.value?.choices.find(choice => choice.id === id)?.unavailable) return
  draft.value.optionId = id; touch()
  if (item.value?.choices.find(choice => choice.id === id)?.field) void focusField('answer')
}
function stamp(outcome: DeskOutcome) {
  if (blocked.value || !canEdit.value || !item.value || !draft.value || outcomeUnavailable(item.value, outcome)) return
  draft.value.outcome = outcome; touch()
}
function navigate(delta: number, notice = '') {
  if (busy.value || props.round.length < 2) return
  index.value = (index.value + delta + props.round.length) % props.round.length
  const next = props.items.find(item => item.id === props.round[index.value])
  announcement.value = `${notice ? notice + ' ' : ''}Memo ${index.value + 1} of ${props.round.length}. ${next?.title ?? 'Unavailable item'}`
}
function declineOrSkip() { if (item.value?.kind === 'key_trim' && !item.value.decided) void submit('decline'); else skip() }
function skip() {
  if (!item.value || busy.value) return
  skipped.value = new Set([...skipped.value, item.value.id]); navigate(1, 'Skipped for this round. The request stays open.')
  status.value = 'Skipped for this round. The request stays open.'
}
async function submit(trimAction?: 'approve' | 'decline' | 'restore') {
  const current = item.value
  const trim = current?.kind === 'key_trim' && (!current.decided || trimRestorable.value)
  const sentDraft = trim && draft.value ? { ...draft.value, optionId: trimAction ?? (current?.decided ? 'restore' : 'approve'), dirty: true } : draft.value
  if (!current || !sentDraft || busy.value) return
  if (!trim && current.decided && !sentDraft.dirty) { navigate(1); return }
  if (blocked.value && !(trimAction === 'decline' && trimDeclinable.value)) { error.value = expired.value ? 'This request expired.' : current.unavailable || 'You do not have permission to decide this item.'; return }
  const choiceUnavailable = current.choices.find(choice => choice.id === sentDraft.optionId)?.unavailable
  if (choiceUnavailable) { error.value = choiceUnavailable; return }
  const unavailable = (current.kind === 'question' || current.kind === 'handover') && outcomeUnavailable(current, sentDraft.outcome)
  if (unavailable) { error.value = unavailable; return }
  if (!trim && !answerFor(current, sentDraft)) {
    const hasAnswerField = current.choices.find(choice => choice.id === sentDraft.optionId)?.field
    error.value = hasAnswerField ? 'Set an answer before deciding.' : 'Choose an answer above.'
    if (hasAnswerField) void focusField('answer')
    return
  }
  if ((current.kind === 'rule' && sentDraft.optionId === 'dismiss') && !sentDraft.reason.trim()) { error.value = 'Give the proposer a reason.'; void focusField('reason'); return }
  const capturedId = current.id, capturedRevision = current.revision, capturedIndex = index.value
  const payload = { ...sentDraft }, requestKey = JSON.stringify([capturedId, capturedRevision, payload.optionId])
  const requestId = trim ? trimRequests.get(requestKey) ?? crypto.randomUUID() : crypto.randomUUID()
  if (trim) trimRequests.set(requestKey, requestId)
  busy.value = true; slam.value = true; error.value = ''; status.value = 'Recording the decision…'
  try {
    const result = await props.decide({ ...current }, payload, requestId)
    if (!live) return
    if (item.value?.id !== capturedId || item.value.revision !== capturedRevision || index.value !== capturedIndex || (item.value.unavailable && !(trimAction === 'decline' && item.value.unavailable === current.keyTrim?.blocked_reason)) || !props.allowed(item.value)) {
      status.value = 'The source changed while recording. Reopen it to confirm the result.'
      slam.value = false
      announcement.value = status.value
      return
    }
    drafts.value[capturedId] = draftFor(result); skipped.value.delete(capturedId)
    emit('recorded', result); slam.value = false; announcement.value = `Decision recorded for ${current.title}. ${result.delivery || ''}`
    status.value = ''
    await nextTick()
    if (live) { busy.value = false; navigate(1, `Decision recorded for ${current.title}. ${result.delivery || ''}`); await nextTick(); focusDecision() }
  } catch (cause) {
    if (live && item.value?.id === capturedId) { error.value = cause instanceof Error ? cause.message : 'The decision failed. Your answer is kept.'; status.value = 'The decision was not confirmed.'; announcement.value = `Decision not confirmed. ${error.value}` }
  } finally { if (live) { busy.value = false; slam.value = false } }
}
async function editRuleProposal() {
  const current = item.value
  if (!current || current.kind !== 'rule' || current.decided || blocked.value || !props.editRule) return
  const id = current.id, revision = current.revision
  busy.value = true; error.value = ''
  try { await props.editRule({ ...current }) }
  catch (cause) { if (live && item.value?.id === id && item.value.revision === revision) error.value = cause instanceof Error ? cause.message : 'The native proposal editor could not be opened.' }
  finally { if (live) busy.value = false }
}
async function revokeGrant() {
  const current = item.value
  if (!current || busy.value || !props.revoke || !props.canRevoke?.(current)) return
  const id = current.id, revision = current.revision
  busy.value = true; error.value = ''; status.value = 'Revoking the grant…'
  try {
    const result = await props.revoke({ ...current })
    if (!live || item.value?.id !== id || item.value.revision !== revision) return
    emit('recorded', result)
    await nextTick()
    if (!live || item.value?.id !== id || item.value.revision !== revision) return
    status.value = result.delivery ?? 'Grant revoked.'; announcement.value = status.value
  } catch (cause) {
    if (live && item.value?.id === id) { error.value = cause instanceof Error ? cause.message : 'Revocation was not confirmed.'; announcement.value = error.value }
  } finally { if (live) busy.value = false }
}
function openPager() { if (busy.value) return; pager.value = !pager.value; jumpIndex.value = index.value; if (pager.value) void nextTick(() => dialog.value?.querySelector<HTMLElement>('[role="listbox"]')?.focus()) }
function pickJump(at: number) { if (busy.value) return; index.value = at; pager.value = false; announcement.value = `Memo ${at + 1} of ${props.round.length}. ${props.items.find(item => item.id === props.round[at])?.title ?? 'Unavailable item'}`; void nextTick(() => pagerButton.value?.focus({ preventScroll: true })) }
function close() { if (viewing.value) return; dialog.value?.close(); emit('close') }
function keys(event: KeyboardEvent) {
  // Reading full context must keep native scrolling, including Shift+Space.
  if (event.target instanceof Element && event.target.closest('.answer-slot') &&
    ['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', ' ', 'PageUp', 'PageDown', 'Home', 'End'].includes(event.key)) return
  if (viewing.value || event.isComposing || event.defaultPrevented || (event.repeat && event.key === 'Enter')) return
  const field = fieldTarget(event.target)
  if (event.key === 'Enter' && submitModifier(event, mac)) { event.preventDefault(); void submit(); return }
  // All other browser/OS combinations remain native, including Shift navigation.
  if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return
  if (field) {
    if (event.key === 'Enter' || event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); finishEditing() }
    return
  }
  if (pager.value) {
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); pager.value = false; pagerButton.value?.focus() }
    else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); jumpIndex.value = (jumpIndex.value + (event.key === 'ArrowDown' ? 1 : -1) + props.round.length) % props.round.length; void nextTick(() => dialog.value?.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' })) }
    else if (event.key === 'Enter') { event.preventDefault(); pickJump(jumpIndex.value) }
    return
  }
  const key = event.key.toLowerCase()
  if (/^[1-9]$/.test(key)) { event.preventDefault(); const choice = item.value?.choices[Number(key) - 1]; if (choice) choose(choice.id) }
  else if (outcomeKeys.includes(key.toUpperCase())) { const outcome = outcomes[outcomeKeys.indexOf(key.toUpperCase())]!; if (item.value && !outcomeUnavailable(item.value, outcome)) { event.preventDefault(); stamp(outcome) } }
  else if (key === 'j' || key === 'arrowright') { event.preventDefault(); navigate(1) }
  else if (key === 'k' || key === 'arrowleft') { event.preventDefault(); navigate(-1) }
  else if (key === 'arrowup' || key === 'arrowdown') {
    event.preventDefault(); const choices = item.value?.choices ?? [], at = choices.findIndex(choice => choice.id === draft.value?.optionId)
    const choice = choices[(at + (key === 'arrowdown' ? 1 : -1) + choices.length) % choices.length]; if (choice) choose(choice.id)
  } else if (key === 's') { event.preventDefault(); skip() }
  else if (key === 'p') { if (project.value?.href) { event.preventDefault(); openProject() } }
  else if (key === 'enter' && (event.target === memo.value || event.target === decideButton.value)) { event.preventDefault(); void submit() }
  else if (key === 'escape') { event.preventDefault(); event.stopPropagation(); close() }
}
async function showAttachment(at: number) { const attachment = context.value.attachments[at]; if (!attachment) return; viewing.value = true; await nextTick(); lightbox.value?.open(attachment.id) }
watch(dialog, element => {
  if (!element) return
  opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
  element.showModal(); void nextTick(() => memo.value?.focus({ preventScroll: true }))
})
onBeforeUnmount(() => { window.removeEventListener('resize', sizeTab); live = false; contextGeneration++; clearTimeout(expiryTimer); sizeObserver?.disconnect(); dialog.value?.close(); if (opener?.isConnected) opener.focus({ preventScroll: true }) })
</script>

<template>
  <dialog ref="dialog" class="desk-dialog" aria-label="Decision Desk memo" @keydown="keys" @cancel.prevent="close">
    <div ref="frame" class="desk-frame" :style="{ '--tab-w': `${tabWidth}px`, ...(scrollingHeight ? { height: `${scrollingHeight}px` } : {}) }" data-testid="desk-frame">
      <div class="desk-toolbar" data-testid="desk-actions">
        <button ref="pagerButton" class="desk-pager" type="button" aria-haspopup="listbox" :aria-expanded="pager" :disabled="busy" data-testid="desk-pager" :style="{ width: `${tabWidth}px`, clipPath: `path('${tabPath}')` }" @click="openPager">
          <svg class="folder-outline" :viewBox="`0 0 ${tabWidth} 46`" preserveAspectRatio="none" aria-hidden="true"><path :d="tabOutline" /></svg>
          <span class="pager-label"><span v-if="project" class="ft-project" :class="{ switched: !!switchedFrom }" :style="{ '--pc': projectColor(project) }" :title="switchedFrom ? `${project.name}: another project than the item before (${switchedFrom.name})` : `Project: ${project.name}`" data-testid="desk-tab-project"><i class="project-dot" aria-hidden="true" /><span>{{ project.name }}</span></span><span class="pager-kind">{{ item ? kindLabels[item.kind] : 'Memo' }}</span></span><span class="pager-count">{{ index + 1 }} of {{ round.length }}<AppIcon name="chevron" :size="12" /></span>
        </button>
        <span class="toolbar-space"><span v-if="arrivalsCount" class="arrival-hint" role="status">{{ arrivalsCount }} new for the next round</span></span>
        <div class="action-buttons">
          <span v-if="revoke || editRule" class="native-action">
            <button v-if="revoke" type="button" :style="{ visibility: item?.kind === 'approval' && item.decided && item.optionId === 'approved' ? 'visible' : 'hidden' }" :disabled="busy || !item || !canRevoke?.(item)" data-testid="desk-revoke" @click="revokeGrant">Revoke</button>
            <button v-if="editRule" type="button" :style="{ visibility: item?.kind === 'rule' && !item.decided ? 'visible' : 'hidden' }" :disabled="blocked || !canEdit" data-testid="desk-edit-rule" @click="editRuleProposal">Edit</button>
          </span>
          <button type="button" :disabled="busy || (item?.kind === 'key_trim' && !item.decided && !trimDeclinable)" data-testid="desk-skip" @click="declineOrSkip">{{ item?.kind === 'key_trim' && !item.decided ? 'Decline' : 'Skip' }} <kbd v-if="item?.kind !== 'key_trim' || item.decided">S</kbd></button>
          <button ref="decideButton" class="desk-primary" type="button" :disabled="primaryDisabled" data-testid="desk-decide" @click="submit()"><span class="primary-label" data-size-label="Replace & next"><span>{{ primary }}</span></span> <KeyCap :style="{ visibility: editing ? 'visible' : 'hidden' }" :aria-hidden="!editing" k="mod" /><KeyCap k="enter" /></button>
          <button class="close-desk" type="button" aria-label="Close memo" data-testid="desk-close" @click="close"><AppIcon name="close" :size="18" /></button>
        </div>
        <div v-if="pager" class="jump-popover">
          <p data-testid="desk-jump-head">{{ counts.open }} open · {{ counts.decided }} decided · {{ counts.skipped }} skipped<template v-if="roundProjects > 1"> · {{ roundProjects }} projects</template></p>
          <ol role="listbox" tabindex="0" aria-label="Jump to a memo" :aria-activedescendant="`jump-${jumpIndex}`">
            <li v-for="(id, at) in round" :id="`jump-${at}`" :key="id" role="option" :class="{ 'project-change': projectChange(at) }" :aria-selected="jumpIndex === at" :data-testid="`desk-jump-${at}`" @click="pickJump(at)">
              <span>{{ at + 1 }}</span><span class="jump-project" :style="jumpProject(at) ? { '--pc': projectColor(jumpProject(at)!) } : undefined" data-testid="desk-jump-project"><template v-if="jumpProject(at)"><i class="project-dot" aria-hidden="true" /><span>{{ jumpProject(at)!.name }}</span></template></span><span>{{ items.find(row => row.id === id)?.title ?? 'Unavailable item' }}</span><small>{{ items.find(row => row.id === id)?.decided ? 'Decided' : skipped.has(id) ? 'Skipped' : 'Open' }}</small>
            </li>
          </ol>
        </div>
      </div>
      <span class="sr-only" role="status" aria-live="polite" data-testid="desk-announcement">{{ announcement }}</span>
      <article ref="memo" class="desk-paper" tabindex="-1" data-testid="desk-paper">
        <template v-if="item && draft">
          <header class="memo-heading" :class="{ switched: !!switchedFrom }">
            <div><div class="memo-meta" data-testid="desk-project-line"><span class="meta-label">Project</span><a v-if="project?.href" class="project-link" :href="project.href" target="_blank" rel="noopener" :style="{ '--pc': projectColor(project) }" :title="`Open ${project.name} in a new tab`" data-testid="desk-project-link"><i class="project-dot" aria-hidden="true" /><span>{{ project.name }}</span><AppIcon name="external" :size="12" /><kbd>P</kbd></a><span v-else-if="project" class="project-link" :style="{ '--pc': projectColor(project) }" data-testid="desk-project-link"><i class="project-dot" aria-hidden="true" /><span>{{ project.name }}</span></span><p v-if="switchedFrom && project" class="project-switch" :style="{ '--pc': projectColor(project) }" data-testid="desk-project-switch"><AppIcon name="arrow" :size="12" /><span v-clip-tip><b>Now in {{ project.name }}.</b> The item before was in {{ switchedFrom.name }}.</span></p></div><h2 v-clip-tip="{ text: item.title, onClip: measureTitle }">{{ item.title }}</h2></div>
            <div class="heading-side"><span class="waiting">{{ expired ? 'Expired' : item.decided ? 'Decided' : item.held ? 'Holding work' : 'Waiting' }}</span><strong class="large-stamp" :class="[{ slam }, `stamp-${draft.outcome}`]" @animationend="slam = false">{{ outcomeLabels[draft.outcome] }}</strong></div>
          </header>
          <div class="stamp-line" data-testid="desk-stamps" role="group" aria-label="Stamp it as">
            <span class="stamp-label">Stamp it as</span>
            <button v-for="(outcome, at) in outcomes" :key="outcome" class="stamp" :class="[`stamp-${outcome}`, { selected: draft.outcome === outcome, unavailable: !!outcomeUnavailable(item, outcome) }]" type="button" :aria-pressed="draft.outcome === outcome" :disabled="blocked || !canEdit || !!outcomeUnavailable(item, outcome)" :title="outcomeUnavailable(item, outcome) || ({ once: 'For this question only.', always: 'Answer the same question from the record.', requirement: 'Add an acceptance criterion.', doctrine: 'Propose a rule change.' }[outcome])" :data-testid="`stamp-${outcome}`" @click="stamp(outcome)">{{ outcomeLabels[outcome] }}<kbd :style="{ visibility: outcomeUnavailable(item, outcome) ? 'hidden' : 'visible' }" :aria-hidden="!!outcomeUnavailable(item, outcome)">{{ outcomeKeys[at] }}</kbd></button>
          </div>
          <div class="memo-status" data-testid="desk-status">{{ error || status || item.unavailable || (expired ? 'This request expired.' : !allowed(item) ? 'You do not have permission to decide.' : outcomeUnavailable(item, draft.outcome) || item.delivery || item.suggestion) }}</div>
          <div ref="body" class="memo-body" data-testid="desk-body">
            <section v-if="item.keyTrim" class="answer-column key-trim-evidence" aria-label="Key trim evidence" data-testid="key-trim-evidence">
              <h3>{{ item.keyTrim.key_name }} · {{ item.keyTrim.owner_name }}</h3>
              <p>Evidence observed {{ new Date(item.keyTrim.evidence.observed_at).toLocaleString() }}; snapshot recorded {{ new Date(item.keyTrim.created_at).toLocaleString() }}.</p>
              <h3>Scopes kept ({{ item.keyTrim.candidate_scopes.length }})</h3>
              <p class="scope-list">{{ item.keyTrim.candidate_scopes.join(', ') || 'None' }}</p>
              <h3>Scopes dropped ({{ item.keyTrim.evidence.risks.length }})</h3>
              <table><thead><tr><th>Scope / evidence</th><th>Risk if dropped</th></tr></thead><tbody><tr v-for="risk in item.keyTrim.evidence.risks" :key="risk.scope"><td><code>{{ risk.scope }}</code><p>{{ risk.evidence }}</p><small>{{ item.keyTrim.usage.find(use => use.scope === risk.scope)?.last_used_at ? `Last recorded use: ${new Date(item.keyTrim.usage.find(use => use.scope === risk.scope)!.last_used_at!).toLocaleString()}` : 'No recorded use; earlier use is unknown.' }}</small></td><td>{{ risk.risk_if_dropped }}</td></tr></tbody></table>
              <p>Usage timestamps can lag by up to one minute. Approval rechecks the last 24 hours and the exact live scope set.</p>
              <p v-if="item.keyTrim.restore_until">Restore until {{ new Date(item.keyTrim.restore_until).toLocaleString() }}. An intervening scope edit or changed grant authority can prevent restore.</p>
              <p v-if="item.keyTrim.state === 'restored'">Previous scopes restored: <span class="scope-list">{{ item.keyTrim.previous_scopes.join(', ') }}</span></p>
            </section>
            <section v-else class="answer-column" aria-label="Your answer">
              <h3>Your answer</h3>
              <div class="choices" role="radiogroup" aria-label="Answer choices" data-testid="desk-choices">
                <div v-for="(choice, at) in item.choices" :key="choice.id" class="choice-row" :class="{ selected: draft.optionId === choice.id }" :data-testid="`choice-row-${at}`">
                  <button type="button" role="radio" :aria-checked="draft.optionId === choice.id" :disabled="blocked || !canEdit || !!choice.unavailable" :title="choice.unavailable || choice.description" :data-testid="`choice-${at}`" @click="choose(choice.id)"><kbd v-if="at < 9">{{ at + 1 }}</kbd><span><strong>{{ choice.title }}</strong><small>{{ choice.description }}</small></span><span v-if="item.recommended === choice.id" class="recommend">Suggested</span></button>
                  <input v-if="choice.field" v-model="draft.answer" data-field="answer" class="answer-field" :class="{ editing: editing === 'answer', set: editing !== 'answer' && !!draft.answer }" :aria-label="choice.title" maxlength="8000" :disabled="blocked || !canEdit || draft.optionId !== choice.id" placeholder="The answer in your own words" @input="touch" @focus="editing = 'answer'" />
                </div>
              </div>
              <div class="answer-slot" data-testid="desk-answer-summary" tabindex="0" aria-label="Selected answer details">
                <template v-if="selectedChoice"><strong>{{ selectedChoice.title }}</strong><p v-if="selectedChoice.description">{{ selectedChoice.description }}</p></template>
                <p>{{ answerFor(item, draft) || 'Choose an answer above.' }}</p>
                <p v-if="editing === 'answer'">Editing · Enter finishes · {{ submitKey }} decides</p>
              </div>
              <label class="ps-line"><span>{{ item.kind === 'approval' || item.kind === 'rule' ? 'Reason' : 'P.S.' }}</span><input v-model="draft.reason" data-field="reason" :aria-label="item.kind === 'approval' || item.kind === 'rule' ? 'Reason' : 'P.S. for the agent'" maxlength="2000" :disabled="blocked || !canEdit" :class="{ editing: editing === 'reason' }" placeholder="A note for the agent" @input="touch" @focus="editing = 'reason'" /></label>
              <p class="editing-slot">{{ editing === 'reason' ? `Enter finishes · ${submitKey} decides` : item.fromRecord || (item.decided ? 'Changing an answer records a replacement through its native workflow.' : 'A decision is recorded only when Decide is pressed.') }}</p>
              <p v-if="item.why" class="recommend-why"><strong>Why this recommendation</strong>{{ item.why }}</p>
              <p v-if="item.decided && !canEdit" class="native-restriction">This protected decision keeps its native restrictions. <span v-if="item.kind === 'approval'">Active grants can be revoked with Revoke above.</span></p>
            </section>
            <aside class="context-column" aria-label="Background and destination">
              <section v-if="titleClipped" data-testid="desk-full-title"><h3>Question in full</h3><p>{{ item.title }}</p></section>
              <section v-if="item.rule" aria-label="Rule proposal details"><h3>Rule proposal</h3><p>Proposed by {{ item.rule.proposer || 'an agent' }}</p><p v-if="item.rule.outdated">The rule changed since this was proposed. Edit against the current rule first.</p><h3>Current rule</h3><p>{{ item.rule.base }}</p><h3>Proposed rule</h3><pre v-if="item.rule.diff?.length" class="rule-diff"><template v-for="(part, at) in item.rule.diff" :key="at"><del v-if="part.op === 'del'">{{ part.text }}</del><ins v-else-if="part.op === 'ins'">{{ part.text }}</ins><template v-else>{{ part.text }}</template></template></pre><p v-else>{{ item.rule.proposed }}</p></section>
              <section v-if="item.approval" aria-label="Permission request">
                <h3>Permission request</h3>
                <p>Asked by {{ item.approval.agent_name?.trim() || `Agent ${item.approval.agent_principal_id.slice(0, 8)}` }}</p>
                <p>{{ scopeLabel(item.approval.scope) }} · {{ RISK_LABEL[riskFor(item.approval)] }}</p>
                <p>{{ item.approval.resource_kind === 'tenant' ? 'The whole workspace' : `${item.approval.resource_kind === 'node' ? 'Ticket' : 'Run'} ${item.approval.resource_id || 'not named'}` }}</p>
                <p>Expires <time :datetime="item.approval.expires_at">{{ deliveryTime(item.approval.expires_at) }}</time></p>
                <TargetSummary :approval="item.approval" />
              </section>
              <section><h3>Meanwhile</h3><p>{{ item.meanwhile }}</p></section>
              <section v-if="item.prUrl"><h3>Rule proposal</h3><a :href="item.prUrl" target="_blank" rel="noopener noreferrer">Open the doctrine pull request<AppIcon name="external" :size="12" /></a></section>
              <section><h3>Background</h3><p>{{ item.context || 'No background supplied by the source.' }}</p></section>
              <section aria-label="Unavailable stamps"><h3>Stamp availability</h3><template v-for="outcome in outcomes" :key="outcome"><p v-if="outcomeUnavailable(item, outcome)">{{ outcomeLabels[outcome] }}: {{ outcomeUnavailable(item, outcome) }}</p></template></section>
              <section v-if="item.findings"><h3>Findings</h3><p>{{ item.findings }}</p></section>
              <section><h3>Where the answer goes</h3><p>{{ item.destination }}</p><p v-if="item.delivery">{{ item.delivery }}</p></section>
              <section v-if="item.outcomeEffects?.length" aria-label="Outcome effects" data-testid="desk-outcome-effects"><h3>{{ outcomeLabels[item.outcome] }} outcome</h3>
                <div v-for="effect in item.outcomeEffects" :key="effect.id">
                  <p>{{ effect.state === 'failed' ? 'Outcome failed; the answer remains recorded.' : effect.state === 'pending' ? `Outcome scheduled for ${deliveryTime(effect.deliver_after)}.` : effect.state === 'replaced' ? 'Outcome replaced by a correction.' : 'Outcome applied.' }}</p>
                  <p v-if="effect.error_message || effect.error_code">{{ effect.error_message || 'The source did not supply a failure reason.' }}<small v-if="effect.error_code"> ({{ effect.error_code }})</small></p>
                  <p v-if="effect.state === 'failed'">{{ effect.effect_data?.retryable === false ? 'Automatic retry is blocked. Review the source and replace the answer to try again.' : effect.effect_data?.retryable === true ? 'Automatic retry is scheduled.' : 'Retry availability has not been supplied.' }}</p>
                  <p v-if="effect.effect_ref">Effect reference: {{ effect.effect_ref }}</p>
                  <p v-if="effect.effect_data?.ticket_id"><RouterLink :to="`/work/${encodeURIComponent(effect.effect_data.ticket_id)}`">Open affected ticket</RouterLink></p>
                  <p v-if="effect.effect_data?.knowledge_id"><a :href="`/api/nodes/${encodeURIComponent(effect.effect_data.knowledge_id)}`" target="_blank" rel="noopener noreferrer">Open decision knowledge</a></p>
                  <p v-if="effect.effect_data?.doctrine_id"><RouterLink :to="doctrineReviewPath(effect.effect_data.doctrine_id, effect.doctrine_state ?? 'pending')">Review doctrine draft {{ effect.effect_data.doctrine_id }}</RouterLink><span v-if="effect.doctrine_state"> · {{ effect.doctrine_state }}</span></p>
                  <p v-for="review in effect.effect_data?.review_required" :key="`${review.kind}:${review.ref}`"><strong>Person review required.</strong> {{ review.why }} <RouterLink :to="review.kind === 'criterion' ? `/work/${encodeURIComponent(review.ref)}` : doctrineReviewPath(review.ref, review.ref === effect.effect_data?.doctrine_id ? effect.doctrine_state : undefined)">Review {{ review.kind }} ({{ review.ref }})</RouterLink></p>
                </div>
              </section>
              <section v-if="item.source"><h3>Original source</h3><a v-if="item.source.startsWith('/api/')" :href="item.source" target="_blank" rel="noopener noreferrer">Open source record<AppIcon name="external" :size="12" /></a><RouterLink v-else :to="item.source">Open source record<AppIcon name="external" :size="12" /></RouterLink></section>
              <section v-if="item.ticketId || item.ticketKey"><h3>From the ticket</h3><p v-if="contextLoading" role="status">Reading ticket context…</p><template v-if="context.ticket"><RouterLink :to="`/work/${context.ticket.id}`">{{ context.ticket.key }} · {{ context.ticket.title }} <AppIcon name="external" :size="12" /></RouterLink><dl><div><dt>Status</dt><dd>{{ context.ticket.state }}</dd></div><div><dt>Priority</dt><dd>{{ plainText(context.ticket.fields.priority) }}</dd></div><div><dt>Acceptance</dt><dd>{{ plainText(context.ticket.fields.acceptance_criteria) }}</dd></div><div><dt>PR / checks</dt><dd>{{ plainText(context.ticket.fields.pr_url) }} · {{ context.outcomes.filter(outcome => outcome.kind === 'ci_result').slice(0, 3).map(outcome => outcomeLine(outcome).full).join(' · ') || 'No check evidence supplied' }}</dd></div></dl></template><p v-for="warning in context.warnings" :key="warning" class="context-warning">{{ warning }}</p></section>
              <section v-if="context.related.length"><h3>Related records</h3><RouterLink v-for="related in context.related" :key="related.id" class="related-link" :to="`/work/${related.id}`">{{ related.key }} · {{ related.title }}</RouterLink></section>
              <section v-if="context.attachments.length"><h3>Attachments</h3><div class="attachments"><button v-for="(attachment, at) in context.attachments" :key="attachment.id" type="button" class="attachment" :aria-label="`Open ${attachment.caption || attachment.name}`" @click="showAttachment(at)"><img v-if="hasThumbnail(attachment) && !brokenThumbs.has(attachment.id)" :src="contentUrl(attachment.id, 'thumb')" alt="" @error="brokenThumbs.add(attachment.id)" /><span v-else>{{ fileKind(attachment) }}</span><small>{{ attachment.caption || attachment.name }}</small></button></div></section>
            </aside>
          </div>
        </template>
        <p v-else>This item is no longer accessible. Close the memo and refresh the desk.</p>
      </article>
    </div>
    <AttachmentLightbox ref="lightbox" :items="context.attachments" :ticket-key="context.ticket?.key || ''" :can-write="false" :set-caption="async () => false" @close="viewing = false" />
  </dialog>
</template>

<style scoped>
.desk-dialog { inset: 0; top: 7dvh; bottom: auto; width: min(1060px, calc(100vw - 48px)); max-width: none; max-height: 86dvh; margin: 0 auto; padding: 12px; border: 1px solid var(--glass-rim); border-radius: 20px; background: var(--glass); color: var(--ink); box-shadow: var(--shadow); backdrop-filter: blur(24px); overflow: hidden; }
.desk-dialog::backdrop { background: var(--scrim); backdrop-filter: blur(5px); }
.desk-frame { display: flex; flex-direction: column; max-height: calc(86dvh - 26px); min-height: 0; }
.desk-toolbar { position: relative; display: flex; flex: none; height: 46px; align-items: stretch; gap: 12px; }
.desk-pager { position: relative; flex: none; display: flex; align-items: center; justify-content: space-between; gap: 10px; width: 360px; padding: 0 40px 0 16px; border: 0; background: var(--surface); color: var(--ink); font-size: 12px; isolation: isolate; }
/* AEON-1057: the project on the folder tab, filled when it changed from the item before. */
.pager-label { display: flex; align-items: center; gap: 8px; min-width: 0; }.pager-kind { white-space: nowrap; }
.ft-project { display: inline-flex; align-items: center; gap: 6px; flex: none; height: 24px; max-width: 124px; padding: 0 8px; border-radius: 6px; box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--pc) 35%, transparent); color: var(--pc); font-size: 12px; font-weight: 650; }
.ft-project > span, .jump-project > span, .project-link > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ft-project.switched { background: color-mix(in srgb, var(--pc) 14%, transparent); box-shadow: inset 0 0 0 1.5px var(--pc); }
.project-dot { flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--pc); }
.memo-meta { display: flex; align-items: center; gap: 10px; height: 26px; margin-bottom: 6px; min-width: 0; font-size: 12px; }
.meta-label { flex: none; font-size: 11px; color: var(--ink-3); }
.project-link { display: inline-flex; align-items: center; gap: 6px; flex: 0 1 auto; min-width: 0; color: var(--pc); font-weight: 650; text-decoration: none; }
a.project-link:hover { text-decoration: underline; text-underline-offset: 3px; }
.project-link svg { flex: none; opacity: .75; }.project-link kbd { margin-left: 2px; }
.project-switch { display: flex; align-items: center; gap: 7px; flex: 0 1 auto; min-width: 0; height: 24px; margin: 0; padding: 0 9px; border-radius: 6px; background: color-mix(in srgb, var(--pc) 10%, transparent); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--pc) 35%, transparent); color: var(--ink-2); white-space: nowrap; overflow: hidden; }
.project-switch > span { overflow: hidden; text-overflow: ellipsis; }.project-switch svg { flex: none; color: var(--pc); }.project-switch b { color: var(--pc); font-weight: 700; }
.folder-outline { position: absolute; inset: 0; width: 100%; height: 100%; pointer-events: none; fill: none; stroke: var(--line-2); stroke-width: 1; }
.pager-count { display: flex; align-items: center; gap: 6px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.toolbar-space { flex: 1; display: flex; align-items: center; min-width: 0; }
.arrival-hint { color: var(--ink-3); font-size: 11px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.action-buttons { display: flex; align-items: center; gap: 8px; }
.action-buttons button[data-testid="desk-skip"] { width: 92px; }
.key-trim-evidence table { width: 100%; table-layout: fixed; border-collapse: collapse; font-size: 12px; }
.key-trim-evidence th, .key-trim-evidence td { text-align: left; vertical-align: top; padding: 10px 8px 10px 0; border-bottom: 1px solid var(--line); overflow-wrap: anywhere; }
.key-trim-evidence p { font-size: 12px; color: var(--ink-2); line-height: 1.5; }
.scope-list { overflow-wrap: anywhere; font-family: var(--font-mono, monospace); }
.native-action { display: grid; }.native-action button { grid-area: 1 / 1; }
.rule-diff { white-space: pre-wrap; overflow-wrap: anywhere; font: 12px/1.65 var(--mono); }.rule-diff ins { background: var(--row-selected); }
.action-buttons button { height: 42px; border: 0; padding: 0 14px; background: transparent; color: var(--ink-2); border-radius: 8px; font-size: 13px; white-space: nowrap; }
.action-buttons .desk-primary { display: flex; align-items: center; justify-content: center; background: var(--primary); color: var(--primary-on); font-weight: 600; }
/* Labels and field shortcuts reserve their intrinsic width for the whole round. */
.primary-label { display: grid; }
.primary-label::before { content: attr(data-size-label); visibility: hidden; grid-area: 1 / 1; }
.primary-label > span { grid-area: 1 / 1; }
.action-buttons .close-desk { width: 42px; padding: 0; display: grid; place-items: center; }
button { cursor: pointer; } button:disabled { cursor: default; } button:focus-visible, input:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; } kbd { font: 11px var(--mono); opacity: .65; margin-left: 7px; }
.desk-paper { flex: 1 1 auto; min-height: 0; position: relative; display: flex; flex-direction: column; padding: 24px 30px 28px; background: var(--surface); border-radius: 0 12px 12px 12px; border: 1px solid var(--line-2); border-top: 0; outline: 0; }
.desk-paper::before { content: ''; position: absolute; top: 0; left: calc(var(--tab-w, 360px) - 21px); right: 12px; height: 1px; background: var(--line-2); }
.memo-heading { flex: none; display: grid; grid-template-columns: minmax(0, 1fr) 150px; gap: 20px; height: 92px; }
h2 { margin: 0; font-size: 23px; line-height: 1.25; font-weight: 550; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere; }h2:focus-visible, .answer-slot:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; }
.heading-side { display: grid; align-content: start; justify-items: end; gap: 14px; }
.waiting { font-size: 11px; color: var(--ink-3); white-space: nowrap; }
.large-stamp { font-size: 19px; line-height: 1; font-weight: 750; text-transform: uppercase; letter-spacing: .06em; }
.stamp-line { flex: none; display: flex; align-items: center; gap: 18px; height: 46px; border-bottom: 1px solid var(--line); }
.stamp-label { font-size: 11px; color: var(--ink-3); }
.stamp { background: transparent; border: 0; padding: 6px 0; color: var(--ink-3); font-size: 12px; font-weight: 700; text-transform: uppercase; letter-spacing: .06em; }
.stamp.selected.stamp-once, .large-stamp.stamp-once { color: var(--primary-ink); }.stamp.selected.stamp-always, .large-stamp.stamp-always { color: var(--ok); }.stamp.selected.stamp-requirement, .large-stamp.stamp-requirement { color: var(--secondary-ink); }.stamp.selected.stamp-doctrine, .large-stamp.stamp-doctrine { color: var(--danger); }
.stamp.unavailable { opacity: .1; }.memo-status { flex: none; height: 42px; display: flex; align-items: center; font-size: 12px; color: var(--ink-2); overflow: auto; overflow-wrap: anywhere; }
.memo-body { overflow: auto; min-height: 0; display: grid; grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr); gap: 32px; scrollbar-gutter: stable; }
h3 { font-size: 11px; font-weight: 650; text-transform: uppercase; letter-spacing: .08em; color: var(--ink-3); margin: 0 0 10px; }
.answer-column, .context-column { min-width: 0; }.context-column { border-left: 1px solid var(--line); padding-left: 26px; }
.choice-row { height: 74px; border-bottom: 1px solid var(--line); padding: 4px 0; box-sizing: border-box; }.choice-row.selected { background: var(--row-hover); }
.choice-row button { width: 100%; height: 42px; display: flex; align-items: center; gap: 12px; text-align: left; border: 0; background: transparent; color: var(--ink); padding: 0 8px; min-width: 0; }.choice-row button>span:first-of-type { min-width: 0; flex: 1; }.choice-row kbd { margin: 0; flex: none; width: 14px; }.choice-row strong, .choice-row small { display: block; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }.choice-row strong { font-size: 13px; font-weight: 550; }.choice-row small { font-size: 11px; color: var(--ink-3); margin-top: 3px; }.recommend { font-size: 10px; color: var(--primary-ink); flex: none; }
.answer-field { display: block; height: 23px; width: calc(100% - 48px); margin-left: 36px; padding: 0 4px; font-size: 12px; color: var(--ink); border: 0; border-bottom: 1px solid var(--line-2); border-radius: 0; background: transparent; }.answer-field.set { font-weight: 650; }.answer-field.editing, .ps-line input.editing { background: var(--row-selected); border-bottom-color: var(--teal); }.answer-field:disabled { opacity: .45; }
.answer-slot { height: 54px; margin: 10px 0; overflow: auto; font-size: 12px; line-height: 1.5; overflow-wrap: anywhere; }.ps-line { display: flex; align-items: center; gap: 12px; height: 34px; border-bottom: 1px solid var(--line); font-size: 12px; }.ps-line input { min-width: 0; width: 100%; height: 28px; border: 0; background: transparent; color: var(--ink); font: inherit; }.editing-slot { height: 42px; overflow: auto; font-size: 11px; color: var(--ink-3); margin: 8px 0; }.recommend-why { font-size: 12px; line-height: 1.5; }.recommend-why strong { display: block; margin-bottom: 6px; }.native-restriction { font-size: 12px; }
.context-column section { margin-bottom: 22px; }.context-column p { font-size: 12px; line-height: 1.65; white-space: pre-wrap; overflow-wrap: anywhere; margin: 0; }.context-column a { font-size: 12px; color: var(--teal-ink); text-decoration: none; overflow-wrap: anywhere; }.context-column dl { font-size: 12px; margin-bottom: 0; }.context-column dl>div { display: grid; grid-template-columns: 75px minmax(0,1fr); gap: 8px; margin: 6px 0; }.context-column dt { color: var(--ink-3); }.context-column dd { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }.related-link { display: block; margin: 5px 0; }.context-warning { color: var(--warn-ink); }
.answer-slot p { margin: 4px 0; white-space: pre-wrap; }
.attachments { display: flex; flex-wrap: wrap; gap: 10px; }.attachment { width: 88px; border: 0; background: transparent; color: var(--ink); padding: 0; }.attachment img, .attachment>span { width: 88px; height: 58px; object-fit: cover; background: var(--surface-2); display: grid; place-items: center; border-radius: 4px; }.attachment small { display: block; font-size: 10px; margin-top: 5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.jump-popover { position: absolute; z-index: 5; left: 0; top: 46px; width: min(600px, calc(100vw - 48px)); padding: 14px; background: var(--surface); border: 1px solid var(--line-2); border-radius: 10px; box-shadow: var(--shadow-pop); }.jump-popover p { margin: 0 0 10px; font-size: 12px; color: var(--ink-3); }.jump-popover ol { list-style: none; padding: 0; margin: 0; max-height: 280px; overflow: auto; }.jump-popover li { display: grid; grid-template-columns: 20px 112px minmax(0, 1fr) 55px; align-items: center; gap: 8px; height: 40px; padding: 0 8px; font-size: 12px; cursor: pointer; }.jump-popover li[aria-selected=true] { background: var(--row-selected); }.jump-popover li>span:nth-child(3) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.jump-popover small { font-size: 10px; color: var(--ink-3); }
.jump-project { display: inline-flex; align-items: center; gap: 6px; min-width: 0; color: var(--pc, var(--ink-3)); font-weight: 650; }
/* Where the project changes in the round, a dashed hairline between the rows. */
.jump-popover li.project-change { border-top: 1px dashed var(--line-2); }
@media (prefers-reduced-motion: no-preference) { .slam { animation: stamp-slam .28s ease-out; } @keyframes stamp-slam { from { transform: scale(1.18); } to { transform: scale(1); } } }
@media (max-width: 720px) {
  .action-buttons .native-action { grid-column: 1 / -1; grid-row: 2; justify-self: start; }
  .action-buttons button[data-testid="desk-skip"] { grid-column: 1; grid-row: 1; }
  .action-buttons .desk-primary { grid-column: 2; grid-row: 1; }
  .action-buttons .close-desk { grid-column: 3; grid-row: 1; }
  .desk-dialog { top: 0; width: 100vw; height: 100dvh; max-height: none; padding: 0; border-radius: 0; border: 0; }.desk-frame { height: 100%; max-height: none; }.desk-toolbar { display: contents; }.desk-pager { order: 0; flex: none; height: 46px; margin-top: 12px; margin-left: 14px; }.toolbar-space { display: none; }.action-buttons { order: 2; display: grid; grid-template-columns: max-content minmax(0,1fr) 42px; flex: none; padding: 10px 14px max(10px, env(safe-area-inset-bottom)); border-top: 1px solid var(--line); background: var(--glass); }.action-buttons .desk-primary { width: 100%; }.action-buttons button { padding: 0 8px; min-height: 44px; }.desk-paper { order: 1; border: 0; border-radius: 0; padding: 22px 18px 0; overflow: hidden; }.desk-paper::before { display: none; }.memo-heading { height: 92px; grid-template-columns: minmax(0,1fr) 100px; gap: 12px; }h2 { font-size: 20px; }.large-stamp { font-size: 13px; }.meta-label { display: none; }.ft-project { max-width: 96px; }.memo-meta { flex-wrap: wrap; height: auto; row-gap: 4px; margin-bottom: 4px; }.project-link { height: 20px; }.memo-heading.switched h2 { -webkit-line-clamp: 1; }
  /* The notice reads in full on two lines; the question gives up its second line meanwhile (it stays in full below). */
  .project-switch { height: auto; padding: 2px 8px; white-space: normal; line-height: 1.3; }.project-switch > span { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }.jump-popover li { grid-template-columns: 20px 84px minmax(0, 1fr) 55px; }.project-switch { font-size: 11px; }.stamp-line { gap: 14px; height: 44px; }.stamp-label { display: none; }.stamp { font-size: 10px; }.memo-status { font-size: 11px; height: 48px; }.memo-body { display: flex; flex-direction: column; gap: 24px; padding-bottom: 20px; }.context-column { border-left: 0; padding-left: 0; }.jump-popover { position: fixed; top: 58px; left: 14px; width: calc(100vw - 28px); }kbd { display: none; }.choice-row button { gap: 6px; }.answer-field { margin-left: 8px; width: calc(100% - 16px); }
}
</style>
