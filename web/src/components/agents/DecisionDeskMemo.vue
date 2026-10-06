<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { answerFor, draftFor, fieldTarget, kindLabels, macPlatform, outcomeLabels, outcomeUnavailable, roundCounts, submitModifier, type DeskDraft, type DeskItem, type DeskOutcome } from '../../lib/decisionDesk'
import { loadDeskContext, type DeskContext } from '../../lib/decisionDeskApi'
import { contentUrl, fileKind, hasThumbnail } from '../../lib/attachments'
import AttachmentLightbox from '../work/AttachmentLightbox.vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import { outcomeLine } from '../../lib/ticketOutcomes'
import { vClipTip } from '../../lib/clipTip'

const props = defineProps<{
  items: DeskItem[]; round: string[]; start: string; arrivalsCount: number; allowed: (item: DeskItem) => boolean
  decide: (item: DeskItem, draft: DeskDraft, requestId: string) => Promise<DeskItem>
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
const canEdit = computed(() => !!item.value && (!item.value.decided || item.value.kind === 'question' || item.value.kind === 'handover'))
const trimRestorable = computed(() => item.value?.keyTrim?.state === 'applied' && !!item.value.keyTrim.restore_until && Date.parse(item.value.keyTrim.restore_until) > Date.now())
const primary = computed(() => item.value?.kind === 'key_trim' ? (!item.value.decided ? 'Approve' : trimRestorable.value ? 'Restore' : 'Next') : item.value?.decided && !draft.value?.dirty ? 'Next' : item.value?.decided ? 'Replace & next' : 'Decide & next')
const announcement = ref('')
const trimRequests = new Map<string, string>()
const plainText = (value: unknown) => typeof value === 'string' ? value : 'Not supplied'
watch(item, async current => {
  const turn = ++contextGeneration
  if (current && !drafts.value[current.id]) drafts.value[current.id] = draftFor(current)
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
    if (live) { busy.value = false; navigate(1, `Decision recorded for ${current.title}. ${result.delivery || ''}`); await nextTick(); decideButton.value?.focus({ preventScroll: true }) }
  } catch (cause) {
    if (live && item.value?.id === capturedId) { error.value = cause instanceof Error ? cause.message : 'The decision failed. Your answer is kept.'; status.value = 'The decision was not confirmed.'; announcement.value = `Decision not confirmed. ${error.value}` }
  } finally { if (live) { busy.value = false; slam.value = false } }
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
  else if (key === 'enter' && (event.target === memo.value || event.target === decideButton.value)) { event.preventDefault(); void submit() }
  else if (key === 'escape') { event.preventDefault(); event.stopPropagation(); close() }
}
async function showAttachment(at: number) { const attachment = context.value.attachments[at]; if (!attachment) return; viewing.value = true; await nextTick(); lightbox.value?.open(attachment.id) }
watch(dialog, element => {
  if (!element) return
  opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
  element.showModal(); void nextTick(() => memo.value?.focus({ preventScroll: true }))
})
onBeforeUnmount(() => { live = false; contextGeneration++; clearTimeout(expiryTimer); sizeObserver?.disconnect(); dialog.value?.close(); if (opener?.isConnected) opener.focus({ preventScroll: true }) })
</script>

<template>
  <dialog ref="dialog" class="desk-dialog" aria-label="Decision Desk memo" @keydown="keys" @cancel.prevent="close">
    <div ref="frame" class="desk-frame" :style="scrollingHeight ? { height: `${scrollingHeight}px` } : undefined" data-testid="desk-frame">
      <div class="desk-toolbar" data-testid="desk-actions">
        <button ref="pagerButton" class="desk-pager" type="button" aria-haspopup="listbox" :aria-expanded="pager" :disabled="busy" data-testid="desk-pager" @click="openPager">
          <svg class="folder-outline" viewBox="0 0 270 46" preserveAspectRatio="none" aria-hidden="true"><path d="M.5 45.5V12 Q.5 .5 12 .5H208 Q220 .5 224 13L231 32Q235 45.5 249 45.5" /></svg>
          <span>{{ item ? kindLabels[item.kind] : 'Memo' }}</span><span class="pager-count">{{ index + 1 }} of {{ round.length }}<AppIcon name="chevron" :size="12" /></span>
        </button>
        <span class="toolbar-space"><span v-if="arrivalsCount" class="arrival-hint" role="status">{{ arrivalsCount }} new for the next round</span></span>
        <div class="action-buttons">
          <button type="button" :disabled="busy || (item?.kind === 'key_trim' && !item.decided && !trimDeclinable)" data-testid="desk-skip" @click="declineOrSkip">{{ item?.kind === 'key_trim' && !item.decided ? 'Decline' : 'Skip' }} <kbd v-if="item?.kind !== 'key_trim' || item.decided">S</kbd></button>
          <button ref="decideButton" class="desk-primary" type="button" :disabled="busy || ((!item?.decided || trimRestorable) && (blocked || (item?.kind !== 'key_trim' && !draft?.optionId)))" data-testid="desk-decide" @click="submit()">{{ primary }} <KeyCap v-if="editing" k="mod" /><KeyCap k="enter" /></button>
          <button class="close-desk" type="button" aria-label="Close memo" data-testid="desk-close" @click="close"><AppIcon name="close" :size="18" /></button>
        </div>
        <div v-if="pager" class="jump-popover">
          <p>{{ counts.open }} open · {{ counts.decided }} decided · {{ counts.skipped }} skipped</p>
          <ol role="listbox" tabindex="0" aria-label="Jump to a memo" :aria-activedescendant="`jump-${jumpIndex}`">
            <li v-for="(id, at) in round" :id="`jump-${at}`" :key="id" role="option" :aria-selected="jumpIndex === at" @click="pickJump(at)">
              <span>{{ at + 1 }}</span><span>{{ items.find(row => row.id === id)?.title ?? 'Unavailable item' }}</span><small>{{ items.find(row => row.id === id)?.decided ? 'Decided' : skipped.has(id) ? 'Skipped' : 'Open' }}</small>
            </li>
          </ol>
        </div>
      </div>
      <span class="sr-only" role="status" aria-live="polite" data-testid="desk-announcement">{{ announcement }}</span>
      <article ref="memo" class="desk-paper" tabindex="-1" data-testid="desk-paper">
        <template v-if="item && draft">
          <header class="memo-heading">
            <div><p class="eyebrow">{{ item.projectName }}</p><h2 v-clip-tip="{ text: item.title, onClip: measureTitle }">{{ item.title }}</h2></div>
            <div class="heading-side"><span class="waiting">{{ expired ? 'Expired' : item.decided ? 'Decided' : item.held ? 'Holding work' : 'Waiting' }}</span><strong class="large-stamp" :class="[{ slam }, `stamp-${draft.outcome}`]" @animationend="slam = false">{{ outcomeLabels[draft.outcome] }}</strong></div>
          </header>
          <div class="stamp-line" data-testid="desk-stamps" role="group" aria-label="Stamp it as">
            <span class="stamp-label">Stamp it as</span>
            <button v-for="(outcome, at) in outcomes" :key="outcome" class="stamp" :class="[`stamp-${outcome}`, { selected: draft.outcome === outcome, unavailable: !!outcomeUnavailable(item, outcome) }]" type="button" :aria-pressed="draft.outcome === outcome" :disabled="blocked || !canEdit || !!outcomeUnavailable(item, outcome)" :title="outcomeUnavailable(item, outcome) || ({ once: 'For this question only.', always: 'Answer the same question from the record.', requirement: 'Add an acceptance criterion.', doctrine: 'Propose a rule change.' }[outcome])" :data-testid="`stamp-${outcome}`" @click="stamp(outcome)">{{ outcomeLabels[outcome] }}<kbd v-if="!outcomeUnavailable(item, outcome)">{{ outcomeKeys[at] }}</kbd></button>
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
                  <button type="button" role="radio" :aria-checked="draft.optionId === choice.id" :disabled="blocked || !canEdit" :title="choice.description" :data-testid="`choice-${at}`" @click="choose(choice.id)"><kbd v-if="at < 9">{{ at + 1 }}</kbd><span><strong>{{ choice.title }}</strong><small>{{ choice.description }}</small></span><span v-if="item.recommended === choice.id" class="recommend">Suggested</span></button>
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
              <p v-if="item.decided && !canEdit" class="native-restriction">This protected decision keeps its native restrictions. <RouterLink v-if="item.kind === 'approval'" to="/agents">Open approvals to revoke an active grant</RouterLink></p>
            </section>
            <aside class="context-column" aria-label="Background and destination">
              <section v-if="titleClipped" data-testid="desk-full-title"><h3>Question in full</h3><p>{{ item.title }}</p></section>
              <section><h3>Meanwhile</h3><p>{{ item.meanwhile }}</p></section>
              <section v-if="item.prUrl"><h3>Rule proposal</h3><a :href="item.prUrl" target="_blank" rel="noopener noreferrer">Open the doctrine pull request<AppIcon name="external" :size="12" /></a></section>
              <section><h3>Background</h3><p>{{ item.context || 'No background supplied by the source.' }}</p></section>
              <section aria-label="Unavailable stamps"><h3>Stamp availability</h3><template v-for="outcome in outcomes" :key="outcome"><p v-if="outcomeUnavailable(item, outcome)">{{ outcomeLabels[outcome] }}: {{ outcomeUnavailable(item, outcome) }}</p></template></section>
              <section v-if="item.findings"><h3>Findings</h3><p>{{ item.findings }}</p></section>
              <section><h3>Where the answer goes</h3><p>{{ item.destination }}</p><p v-if="item.delivery">{{ item.delivery }}</p></section>
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
.desk-pager { position: relative; flex: none; display: flex; align-items: center; justify-content: space-between; width: 270px; padding: 0 40px 0 16px; border: 0; background: var(--surface); color: var(--ink); font-size: 12px; isolation: isolate; clip-path: path('M0 46V12Q0 0 12 0H208Q220 0 224 13L231 32Q235 46 249 46Z'); }
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
.action-buttons button { height: 42px; border: 0; padding: 0 14px; background: transparent; color: var(--ink-2); border-radius: 8px; font-size: 13px; white-space: nowrap; }
.action-buttons .desk-primary { width: 184px; background: var(--primary); color: var(--primary-on); font-weight: 600; }
.action-buttons .close-desk { width: 42px; padding: 0; display: grid; place-items: center; }
button { cursor: pointer; } button:disabled { cursor: default; } button:focus-visible, input:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; } kbd { font: 11px var(--mono); opacity: .65; margin-left: 7px; }
.desk-paper { flex: 1 1 auto; min-height: 0; position: relative; display: flex; flex-direction: column; padding: 24px 30px 28px; background: var(--surface); border-radius: 0 12px 12px 12px; border: 1px solid var(--line-2); border-top: 0; outline: 0; }
.desk-paper::before { content: ''; position: absolute; top: 0; left: 249px; right: 12px; height: 1px; background: var(--line-2); }
.memo-heading { flex: none; display: grid; grid-template-columns: minmax(0, 1fr) 150px; gap: 20px; height: 92px; }
.eyebrow { font-size: 11px; color: var(--ink-3); margin: 0 0 6px; }
h2 { margin: 0; font-size: 23px; line-height: 1.25; font-weight: 550; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere; }h2:focus-visible, .answer-slot:focus-visible { outline: 2px solid var(--teal); outline-offset: 2px; }
.heading-side { display: grid; align-content: start; justify-items: end; gap: 14px; }
.waiting { font-size: 11px; color: var(--ink-3); white-space: nowrap; }
.large-stamp { font-size: 19px; line-height: 1; font-weight: 750; text-transform: uppercase; letter-spacing: .06em; }
.stamp-line { flex: none; display: flex; align-items: center; gap: 18px; height: 46px; border-bottom: 1px solid var(--line); }
.stamp-label { font-size: 11px; color: var(--ink-3); }
.stamp { background: transparent; border: 0; padding: 6px 0; color: var(--ink-3); font-size: 12px; font-weight: 700; text-transform: uppercase; letter-spacing: .06em; }
.stamp.selected.stamp-once, .large-stamp.stamp-once { color: var(--primary-ink); }.stamp.selected.stamp-always, .large-stamp.stamp-always { color: var(--ok); }.stamp.selected.stamp-requirement, .large-stamp.stamp-requirement { color: var(--gold-ink); }.stamp.selected.stamp-doctrine, .large-stamp.stamp-doctrine { color: var(--danger); }
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
.jump-popover { position: absolute; z-index: 5; left: 0; top: 46px; width: min(480px, calc(100vw - 48px)); padding: 14px; background: var(--surface); border: 1px solid var(--line-2); border-radius: 10px; box-shadow: var(--shadow-pop); }.jump-popover p { margin: 0 0 10px; font-size: 12px; color: var(--ink-3); }.jump-popover ol { list-style: none; padding: 0; margin: 0; max-height: 280px; overflow: auto; }.jump-popover li { display: grid; grid-template-columns: 20px minmax(0, 1fr) 55px; align-items: center; gap: 8px; height: 40px; padding: 0 8px; font-size: 12px; cursor: pointer; }.jump-popover li[aria-selected=true] { background: var(--row-selected); }.jump-popover li>span:nth-child(2) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.jump-popover small { font-size: 10px; color: var(--ink-3); }
@media (prefers-reduced-motion: no-preference) { .slam { animation: stamp-slam .28s ease-out; } @keyframes stamp-slam { from { transform: scale(1.18); } to { transform: scale(1); } } }
@media (max-width: 720px) {
  .desk-dialog { top: 0; width: 100vw; height: 100dvh; max-height: none; padding: 0; border-radius: 0; border: 0; }.desk-frame { height: 100%; max-height: none; }.desk-toolbar { display: contents; }.desk-pager { order: 0; flex: none; height: 46px; margin-top: 12px; margin-left: 14px; }.toolbar-space { display: none; }.action-buttons { order: 2; display: grid; grid-template-columns: 64px minmax(0,1fr) 42px; flex: none; padding: 10px 14px max(10px, env(safe-area-inset-bottom)); border-top: 1px solid var(--line); background: var(--glass); }.action-buttons .desk-primary { width: 100%; }.action-buttons button[data-testid="desk-skip"] { width: 64px; font-size: 12px; }.action-buttons button { padding: 0 10px; }.desk-paper { order: 1; border: 0; border-radius: 0; padding: 22px 18px 0; overflow: hidden; }.desk-paper::before { display: none; }.memo-heading { height: 92px; grid-template-columns: minmax(0,1fr) 100px; gap: 12px; }h2 { font-size: 20px; }.large-stamp { font-size: 13px; }.stamp-line { gap: 14px; height: 44px; }.stamp-label { display: none; }.stamp { font-size: 10px; }.memo-status { font-size: 11px; height: 48px; }.memo-body { display: flex; flex-direction: column; gap: 24px; padding-bottom: 20px; }.context-column { border-left: 0; padding-left: 0; }.jump-popover { position: fixed; top: 58px; left: 14px; width: calc(100vw - 28px); }kbd { display: none; }.choice-row button { gap: 6px; }.answer-field { margin-left: 8px; width: calc(100% - 16px); }
}
</style>
