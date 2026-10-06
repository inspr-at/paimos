<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { confirmAction } from '../../lib/confirm'
import {
  KnowledgeError, acceptLearning, applySteps, defaultChosen, dismissLearning, draftLearning, listLearnings, recommendedSteps, undoKnowledge,
  type KnowledgeEntry, type KnowledgeItem, type MethodLearning, type MethodLearningRecommendation, type RecommendedStep, type SensitiveRange, type StepResult,
} from '../../lib/knowledge'
import { can } from '../../lib/authz'
import { listLayers, listSets, ROLE_LABEL, writeBlock, type Caller, type RuleLayer, type RuleSet, type RoleName } from '../../lib/rules'
import { useSession } from '../../stores/session'
import { toast } from '../../lib/toast'
import { relativeTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'

// Open process-learning candidates for this project. A person accepts one into
// an entry's changelog, or dismisses it. Agents and viewers can read the list.
// Agents may attach a recommendation to each (AEON-788); a person reviews them
// in one list, opts rows out, and applies the rest with one confirm.
const props = defineProps<{
  project: { id: string; routeKey: string; title: string }
  entries: KnowledgeItem[]
  canWrite: boolean
  person: boolean
  now: number
}>()
const emit = defineEmits<{ accepted: [entry: KnowledgeEntry]; reverted: []; emptied: [] }>()

const inboxPreview = 3
const items = ref<MethodLearning[]>([])
const truncated = ref(false)
const nextCursor = ref('')
const loadingOlder = ref(false)
const error = ref('')
const busy = ref(false)
const showAll = ref(false)
const accepting = ref<MethodLearning | null>(null)
const chosen = ref('')
const destination = ref<'changelog' | 'draft'>('changelog')
const layers = ref<RuleLayer[]>([])
const sets = ref<RuleSet[]>([])
const layerId = ref('')
const setId = ref('')
const rulesLoading = ref(false)
const dialogError = ref('')
// A 409 learning_sensitive: the suspected ranges, and the person's answer.
const suspect = ref<SensitiveRange[] | null>(null)
const notCredential = ref(false)
const confirming = computed(() => !!suspect.value && notCredential.value)
let rulesLoaded = false
const dialog = ref<HTMLDialogElement>()
const heading = ref<HTMLHeadingElement>()
const listEl = ref<HTMLElement>()
let opener: HTMLElement | null = null
let controller: AbortController | undefined

const shown = computed(() => (showAll.value || items.value.length <= inboxPreview) ? items.value : items.value.slice(0, inboxPreview))

const canDecide = computed(() => props.person && props.canWrite)
const choices = computed(() => {
  const here = props.entries.filter(entry => !entry.project || entry.project.id === props.project.id)
  const live = here.filter(entry => entry.status !== 'archived')
  const pool = live.length ? live : here
  return [...pool].sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at) || a.title.localeCompare(b.title))
})
const chosenEntry = computed(() => choices.value.find(entry => entry.id === chosen.value) ?? null)

function preferred(entries: KnowledgeItem[]) {
  return entries.find(entry => /flywheel|changelog/i.test(`${entry.slug} ${entry.title}`))
    ?? entries.find(entry => entry.type === 'runbook')
    ?? entries[0]
}

function linkLabel(key: string) {
  return key.replaceAll('[', '(').replaceAll(']', ')').replaceAll('(', '⟨').replaceAll(')', '⟩')
}
const previewDate = computed(() => new Date().toISOString().slice(0, 10))
// The learning split around its suspected credentials (code point offsets).
const previewParts = computed(() => {
  const chars = [...(accepting.value?.text ?? '')]
  const marks = (suspect.value ?? []).filter(r => r.field === 'text' && r.start < r.end && r.end <= chars.length).sort((a, b) => a.start - b.start)
  const parts: { text: string; mark: boolean }[] = []
  let at = 0
  for (const r of marks) {
    if (r.start < at) continue
    if (r.start > at) parts.push({ text: chars.slice(at, r.start).join(''), mark: false })
    parts.push({ text: chars.slice(r.start, r.end).join(''), mark: true })
    at = r.end
  }
  if (at < chars.length) parts.push({ text: chars.slice(at).join(''), mark: false })
  return parts
})
function flagged(e: unknown): boolean {
  if (!(e instanceof KnowledgeError) || e.code !== 'learning_sensitive') return false
  suspect.value = e.ranges
  notCredential.value = false
  dialogError.value = ''
  return true
}

async function load() {
  controller?.abort()
  const current = controller = new AbortController()
  try {
    const page = await listLearnings(props.project.id, current.signal)
    if (current.signal.aborted) return
    items.value = page.items
    truncated.value = page.truncated
    nextCursor.value = page.next_cursor ?? ''
    error.value = ''
  } catch (e) {
    if (current.signal.aborted) return
    items.value = []
    error.value = e instanceof Error ? e.message : 'Method learnings could not be loaded.'
  }
}
// Older learnings continue after the last loaded one. The project and the
// cursor are captured, so a project switch or a reload drops a late answer.
async function loadOlder() {
  const cursor = nextCursor.value
  const project = props.project.id
  if (!cursor || loadingOlder.value) return
  loadingOlder.value = true
  const current = controller
  try {
    const page = await listLearnings(project, current?.signal, cursor)
    if (project !== props.project.id || cursor !== nextCursor.value || current !== controller) return
    const known = new Set(items.value.map(item => item.id))
    items.value = [...items.value, ...page.items.filter(item => !known.has(item.id))]
    truncated.value = page.truncated
    nextCursor.value = page.next_cursor ?? ''
    showAll.value = true
  } catch (e) {
    if (current?.signal.aborted) return
    toast(e instanceof Error ? e.message : 'Older learnings could not be loaded.', { tone: 'error' })
  } finally {
    loadingOlder.value = false
  }
}
watch(() => props.project.id, () => { showAll.value = false; void load() }, { immediate: true })
watch(choices, entries => {
  if (!accepting.value || entries.some(entry => entry.id === chosen.value)) return
  chosen.value = preferred(entries)?.id ?? ''
})
onBeforeUnmount(() => controller?.abort())

function layerOption(layer: RuleLayer): string {
  const scope = layer.scope
  if (scope.agent_id || scope.task_id) return ''
  if (scope.layer === 'project') {
    if (scope.project_id !== props.project.id) return ''
    return props.project.title
  }
  if (scope.layer === 'person') return 'Your rules'
  if (scope.layer === 'company') return 'Company'
  if (scope.layer === 'agent' && scope.role) return `Agent role · ${ROLE_LABEL[scope.role as RoleName] ?? scope.role}`
  return ''
}
// Only layers this person may write, by the same rule the rules page uses,
// so the dialog never offers a set that would answer 403.
const session = useSession()
const caller = computed<Caller | null>(() => {
  const identity = session.identity
  if (!identity) return null
  return { id: identity.principal.id, kind: identity.principal.kind === 'agent' ? 'agent' : 'person', allows: (permission, project) => can(permission, project) }
})
const draftLayers = computed(() => layers.value.flatMap(layer => {
  const label = layerOption(layer)
  return label && !writeBlock(caller.value, layer.scope) ? [{ id: layer.id, label }] : []
}))

async function loadSets() {
  const id = layerId.value
  if (!id) {
    sets.value = []
    setId.value = ''
    return
  }
  rulesLoading.value = true
  try {
    const page = await listSets(id)
    if (layerId.value !== id) return
    sets.value = page.sets
    setId.value = page.sets.some(set => set.id === setId.value) ? setId.value : (page.sets[0]?.id ?? '')
  } catch (e) {
    sets.value = []
    setId.value = ''
    dialogError.value = e instanceof Error ? e.message : 'Rule sets could not be loaded.'
  } finally {
    rulesLoading.value = false
  }
}
async function loadRules() {
  if (rulesLoaded) {
    if (!draftLayers.value.some(layer => layer.id === layerId.value)) {
      layerId.value = draftLayers.value[0]?.id ?? ''
      await loadSets()
    }
    return
  }
  rulesLoading.value = true
  dialogError.value = ''
  try {
    const page = await listLayers()
    layers.value = page.layers
    rulesLoaded = true
    layerId.value = draftLayers.value[0]?.id ?? ''
    await loadSets()
  } catch (e) {
    dialogError.value = e instanceof Error ? e.message : 'Rule sets could not be loaded.'
  } finally {
    rulesLoading.value = false
  }
}
watch(destination, value => { if (value === 'draft') void loadRules() })

function openAccept(item: MethodLearning, event: MouseEvent) {
  opener = event.currentTarget instanceof HTMLElement ? event.currentTarget : null
  accepting.value = item
  destination.value = 'changelog'
  chosen.value = preferred(choices.value)?.id ?? ''
  dialogError.value = ''
  suspect.value = null
  notCredential.value = false
  void nextTick(() => dialog.value?.showModal())
}
function excerpt(text: string) {
  const chars = [...text]
  if (chars.length <= 48) return text
  return `${chars.slice(0, 48).join('').trimEnd()}…`
}
function actionName(verb: string, item: MethodLearning) {
  return `${verb} ${item.key}: ${excerpt(item.text)}`
}
// A click handler passes the event, which must not count as "skip restore".
function close(restore: boolean | Event = true) {
  const restoreFocus = typeof restore === 'boolean' ? restore : true
  dialog.value?.close()
  accepting.value = null
  dialogError.value = ''
  suspect.value = null
  notCredential.value = false
  if (restoreFocus) opener?.focus()
}
async function settleFocus(removedId: string, before: MethodLearning[]) {
  const index = before.findIndex(row => row.id === removedId)
  const next = index >= 0 ? before[index + 1] : undefined
  await nextTick()
  const button = next ? listEl.value?.querySelector<HTMLButtonElement>(`[data-learning-id="${CSS.escape(next.id)}"] button`) : null
  if (button) {
    button.focus()
    return
  }
  // The inbox unmounts with its last row, so the heading is gone.
  if ((items.value.length || error.value) && heading.value) {
    heading.value.focus()
    return
  }
  emit('emptied')
}
function backdrop(event: MouseEvent) { if (event.target === dialog.value) close() }

async function saveDraft() {
  const item = accepting.value
  const layer = layerId.value
  const set = setId.value
  if (!item || !layer || !set || busy.value) return
  busy.value = true
  dialogError.value = ''
  try {
    const decision = await draftLearning(item.id, confirming.value ? { layer_id: layer, set_id: set, confirm_not_sensitive: true } : { layer_id: layer, set_id: set })
    const before = items.value.slice()
    items.value = items.value.filter(row => row.id !== item.id)
    close(false)
    busy.value = false
    toast('Saved as a rule draft.', { action: { label: 'Undo', run: () => void undo(decision.event_id) }, timeout: 8000 })
    await settleFocus(item.id, before)
  } catch (e) {
    if (!flagged(e)) dialogError.value = e instanceof Error ? e.message : 'That could not be saved.'
  } finally {
    busy.value = false
  }
}

async function accept() {
  if (destination.value === 'draft') {
    await saveDraft()
    return
  }
  const item = accepting.value
  const entry = chosenEntry.value
  if (!item || !entry || busy.value) return
  busy.value = true
  dialogError.value = ''
  let since = entry.updated_at
  try {
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        const decision = await acceptLearning(item.id, entry.id, since, confirming.value, undefined, item.text)
        const before = items.value.slice()
        items.value = items.value.filter(row => row.id !== item.id)
        if (decision.entry) emit('accepted', decision.entry)
        close(false)
        busy.value = false
        toast('Added to the changelog.', { action: { label: 'Undo', run: () => void undo(decision.event_id) }, timeout: 8000 })
        await settleFocus(item.id, before)
        return
      } catch (e) {
        if (attempt === 0 && e instanceof KnowledgeError && e.status === 412 && e.entry) {
          emit('accepted', e.entry)
          since = e.entry.updated_at
          continue
        }
        throw e
      }
    }
  } catch (e) {
    if (changed(e)) await reviewAgain(item, e)
    else if (!flagged(e)) dialogError.value = e instanceof Error ? e.message : 'That could not be added.'
  } finally {
    busy.value = false
  }
}
function changed(e: unknown): e is KnowledgeError { return e instanceof KnowledgeError && e.code === 'learning_changed' }
// The learning reads differently now than on screen: reload it and show the
// current text in the open dialog, so the person reviews it again.
async function reviewAgain(item: MethodLearning, e: KnowledgeError) {
  await load()
  if (accepting.value?.id !== item.id) return
  const fresh = items.value.find(row => row.id === item.id)
  if (!fresh) {
    close()
    toast('This learning is no longer open.', { tone: 'error' })
    return
  }
  accepting.value = fresh
  suspect.value = null
  notCredential.value = false
  dialogError.value = e.message
}

async function dismiss(item: MethodLearning) {
  const ok = await confirmAction({
    title: 'Dismiss this learning?',
    body: item.text,
    confirmLabel: 'Dismiss learning',
    cancelLabel: 'Keep it',
  })
  if (!ok) return
  busy.value = true
  try {
    const decision = await dismissLearning(item.id, undefined, item.text)
    const before = items.value.slice()
    items.value = items.value.filter(row => row.id !== item.id)
    busy.value = false
    toast('Dismissed.', { action: { label: 'Undo', run: () => void undo(decision.event_id) }, timeout: 8000 })
    await settleFocus(item.id, before)
  } catch (e) {
    toast(e instanceof Error ? e.message : 'Dismiss did not work.', { tone: 'error' })
    if (changed(e)) await load()
  } finally {
    busy.value = false
  }
}

// ---------- Recommendations (AEON-788) ----------
function recommendationLine(rec: MethodLearningRecommendation) {
  const who = rec.by?.name ?? 'An agent'
  if (rec.decision === 'dismiss') return `${who} recommends dismissing: ${rec.reason ?? ''}`
  const target = rec.target_missing ? 'an entry that is gone' : (rec.knowledge_title || 'an entry')
  return rec.lesson ? `${who} recommends adding to ${target}: “${rec.lesson}”` : `${who} recommends adding to ${target}`
}
function recommendationNote(rec: MethodLearningRecommendation) {
  if (rec.decision === 'accept' && rec.target_missing) return 'Its entry no longer exists here.'
  return rec.stale ? 'Written before the learning changed.' : ''
}
const recommended = computed(() => items.value.filter(item => item.recommendation).length)

const review = ref<HTMLDialogElement>()
const reviewOpen = ref(false)
const reviewProject = ref('')
const steps = ref<RecommendedStep[]>([])
const chosenSteps = ref<Set<string>>(new Set())
const results = ref<Map<string, StepResult>>(new Map())
const applying = ref(false)
const finished = ref(false)
let reviewOpener: HTMLElement | null = null
// A run belongs to this component, this project and this person. Unmounting,
// switching project or signing in as someone else ends it: no further step is
// sent and late results are dropped.
let applyRun = 0
// A step was refused because its learning changed: the inbox reloads when the
// review closes, so the person reviews the current text.
let reloadOnClose = false
// Applied rows leave the inbox when the review closes, so the dialog (inside
// the inbox) keeps showing every result until then.
let appliedIds = new Set<string>()
const acceptSteps = computed(() => steps.value.filter(step => step.decision === 'accept'))
const dismissSteps = computed(() => steps.value.filter(step => step.decision === 'dismiss'))
const chosenCount = computed(() => steps.value.filter(step => chosenSteps.value.has(step.id) && step.blocked !== 'target').length)
const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`
const reviewSummary = computed(() => {
  const accept = acceptSteps.value.filter(step => chosenSteps.value.has(step.id) && step.blocked !== 'target').length
  const dismiss = dismissSteps.value.filter(step => chosenSteps.value.has(step.id)).length
  return `${accept} to accept · ${dismiss} to dismiss · ${steps.value.length - accept - dismiss} left out`
})
const failures = computed(() => steps.value.flatMap(step => {
  const result = results.value.get(step.id)
  return result && !result.ok ? [{ id: step.id, key: step.key, message: result.message }] : []
}))
const reviewStatus = computed(() => {
  const all = [...results.value.values()]
  const failed = all.filter(result => !result.ok).length
  if (applying.value) return `Applying ${Math.min(all.length + 1, chosenCount.value)} of ${chosenCount.value}…`
  if (!finished.value) return chosenCount.value ? 'Nothing is applied until you confirm.' : 'Choose at least one row.'
  return failed ? `${all.length - failed} applied, ${failed} failed. The failed learnings stay open.` : `${all.length} applied.`
})

function openReview(event: MouseEvent) {
  reviewOpener = event.currentTarget instanceof HTMLElement ? event.currentTarget : null
  reviewProject.value = props.project.id
  steps.value = recommendedSteps(items.value)
  chosenSteps.value = defaultChosen(steps.value)
  results.value = new Map()
  finished.value = false
  reloadOnClose = false
  reviewOpen.value = true
  void nextTick(() => review.value?.showModal())
}
function toggleStep(id: string, on: boolean) {
  const next = new Set(chosenSteps.value)
  if (on) next.add(id)
  else next.delete(id)
  chosenSteps.value = next
}
// Closing during a run lets the current request finish and stops the rest.
function closeReview(restore: boolean | Event = true) {
  const restoreFocus = typeof restore === 'boolean' ? restore : true
  if (!reviewOpen.value) return
  review.value?.close()
  reviewOpen.value = false
  if (appliedIds.size) {
    const applied = appliedIds
    appliedIds = new Set()
    items.value = items.value.filter(item => !applied.has(item.id))
    if (!items.value.length && !error.value) {
      emit('emptied')
      return
    }
  }
  if (reloadOnClose) {
    reloadOnClose = false
    void load()
  }
  if (restoreFocus) void nextTick(() => (reviewOpener?.isConnected ? reviewOpener : heading.value)?.focus())
}
watch(() => props.project.id, () => { applyRun++; closeReview(false) })
watch(() => session.identity?.principal.id, (id, before) => {
  if (!before || id === before) return
  applyRun++
  closeReview(false)
  close(false)
  void load()
})
onBeforeUnmount(() => { applyRun++ })
function reviewBackdrop(event: MouseEvent) { if (event.target === review.value && !applying.value) closeReview() }
async function applyChosen() {
  if (applying.value || finished.value || !chosenCount.value || !canDecide.value) return
  const project = reviewProject.value
  const run = ++applyRun
  const current = () => run === applyRun && project === props.project.id
  const frozen = steps.value.slice()
  const chosen = new Set(chosenSteps.value)
  applying.value = true
  results.value = new Map()
  const done = await applySteps(frozen, chosen, {
    accept: (id, knowledgeId, lesson, text) => acceptLearning(id, knowledgeId, undefined, false, lesson || undefined, text),
    dismiss: (id, reason, text) => dismissLearning(id, reason || undefined, text),
  }, result => {
    if (!current()) return
    results.value = new Map(results.value).set(result.id, result)
    if (result.changed) reloadOnClose = true
    if (result.ok && result.decision?.entry) emit('accepted', result.decision.entry)
  }, () => !reviewOpen.value || !current())
  applying.value = false
  if (!current()) return
  finished.value = true
  const applied = done.filter(result => result.ok).map(result => result.id)
  applied.forEach(id => appliedIds.add(id))
  const failed = done.length - applied.length
  if (failed) toast(`${plural(applied.length, 'recommendation', 'recommendations')} applied, ${failed} failed.`, { tone: 'error' })
  else toast(`${plural(applied.length, 'recommendation', 'recommendations')} applied.`)
  // Closed during the run: the remaining steps were not sent.
  if (!reviewOpen.value) {
    reviewOpen.value = true
    closeReview(false)
  }
}
function stepResult(id: string) { return results.value.get(id) }

async function undo(eventId: number) {
  try {
    await undoKnowledge(eventId)
    await load()
    emit('reverted')
    toast('Undone.')
  } catch (e) {
    toast(e instanceof Error ? e.message : 'Undo did not work.', { tone: 'error' })
  }
}
</script>

<template>
  <section v-if="items.length || error" class="learnings glass-card" aria-labelledby="method-learnings-title">
    <div class="head">
      <span class="mark" aria-hidden="true"><AppIcon name="sparkle" :size="15" /></span>
      <div class="head-copy">
        <h2 id="method-learnings-title" ref="heading" tabindex="-1">Method learnings</h2>
        <p v-if="items.length && !canDecide" class="wait">Waiting for a person to accept.</p>
        <p v-else-if="error" class="wait" role="status">{{ error }}</p>
      </div>
      <div v-if="truncated || (canDecide && recommended)" class="head-tools">
        <button v-if="truncated" type="button" class="btn ghost" :disabled="loadingOlder" @click="loadOlder">
          <span class="btn-label"><span :aria-hidden="loadingOlder ? 'true' : undefined">Load older</span><span :aria-hidden="loadingOlder ? undefined : 'true'">Loading…</span></span>
        </button>
        <button v-if="canDecide && recommended" type="button" class="btn" :disabled="busy || applying" @click="openReview">Apply recommendations</button>
      </div>
    </div>
    <ul v-if="items.length" ref="listEl" class="rows">
      <li v-for="item in shown" :key="item.id" class="row" :data-learning-id="item.id">
        <div class="copy">
          <p class="line" :title="item.text">{{ item.text }}</p>
          <p class="meta">
            <a :href="item.href">{{ item.key }}</a>
            <span>{{ item.source === 'comment' ? 'Comment' : 'Ticket' }}</span>
            <span v-if="item.author">{{ item.author.name }}</span>
            <time :datetime="item.at" :title="item.at">{{ relativeTime(item.at, { now }) }}</time>
          </p>
          <p v-if="item.recommendation" class="rec" :class="{ off: item.recommendation.stale || item.recommendation.target_missing }">
            <AppIcon name="agent" :size="13" class="rec-icon" />
            <span>{{ recommendationLine(item.recommendation) }}<template v-if="recommendationNote(item.recommendation)"> · <em>{{ recommendationNote(item.recommendation) }}</em></template></span>
          </p>
        </div>
        <div v-if="canDecide" class="actions">
          <button type="button" class="btn" :disabled="busy" :aria-label="actionName('Accept', item)" @click="openAccept(item, $event)">Accept</button>
          <button type="button" class="btn ghost" :disabled="busy" :aria-label="actionName('Dismiss', item)" @click="dismiss(item)">Dismiss</button>
        </div>
      </li>
    </ul>
    <button v-if="items.length > inboxPreview" type="button" class="btn ghost disclose" @click="showAll = !showAll">{{ showAll ? 'Show fewer' : `Show all ${items.length}` }}</button>
    <p v-if="truncated" class="more">Showing the {{ items.length }} newest. Load older from the top.</p>

    <dialog ref="review" class="review-dialog" aria-labelledby="learn-review-title" @cancel.prevent="closeReview" @click="reviewBackdrop">
      <form v-if="reviewOpen" class="review-card" @submit.prevent="applyChosen">
        <div class="review-head">
          <span class="mark" aria-hidden="true"><AppIcon name="agent" :size="15" /></span>
          <div class="head-copy">
            <h2 id="learn-review-title">Apply recommendations</h2>
            <p class="wait">{{ reviewSummary }}</p>
          </div>
        </div>
        <!-- Controls sit above the list (below it in the phone sheet); results
             only fill each row's fixed mark and the slot after the lists. -->
        <div class="review-foot">
          <p class="review-status" role="status">{{ reviewStatus }}</p>
          <div class="review-actions">
            <button type="button" class="btn" @click="closeReview">Close</button>
            <button type="submit" class="btn primary" :disabled="applying || finished || !chosenCount">
              <span class="btn-label"><span :aria-hidden="applying ? 'true' : undefined">Apply chosen</span><span :aria-hidden="applying ? undefined : 'true'">Applying…</span></span>
            </button>
          </div>
        </div>
        <div class="review-body" tabindex="-1">
          <section v-for="group in [{ name: 'Accept into a changelog', id: 'accept', list: acceptSteps }, { name: 'Dismiss', id: 'dismiss', list: dismissSteps }]" v-show="group.list.length" :key="group.id" class="group" :aria-labelledby="`learn-review-${group.id}`">
            <h3 :id="`learn-review-${group.id}`">{{ group.name }} <span class="count">{{ group.list.length }}</span></h3>
            <ul class="steps">
              <li v-for="step in group.list" :key="step.id" class="step" :data-step-id="step.id" :class="{ out: !chosenSteps.has(step.id) }">
                <input :id="`step-${step.id}`" type="checkbox" class="tick" :checked="chosenSteps.has(step.id) && step.blocked !== 'target'"
                  :disabled="applying || finished || step.blocked === 'target'" @change="toggleStep(step.id, ($event.target as HTMLInputElement).checked)" />
                <label :for="`step-${step.id}`" class="step-copy">
                  <span class="step-line">{{ step.decision === 'accept' ? step.lesson : step.text }}</span>
                  <span class="step-meta">
                    <span v-if="step.decision === 'accept'">Into {{ step.knowledgeTitle || 'an entry that is gone' }}</span>
                    <span v-else>Reason: {{ step.reason }}</span>
                    <span>{{ step.key }}</span>
                    <span v-if="step.by">by {{ step.by }}</span>
                  </span>
                  <span v-if="step.blocked === 'stale'" class="step-note">The learning changed after this was recommended. Left out unless you choose it.</span>
                  <span v-else-if="step.blocked === 'target'" class="step-note">Its entry no longer exists here; accept it on its own.</span>
                  <span v-if="stepResult(step.id)" class="sr-only">{{ stepResult(step.id)?.message }}</span>
                </label>
                <span class="step-mark" :class="{ bad: stepResult(step.id) && !stepResult(step.id)?.ok }" :data-result="stepResult(step.id) ? (stepResult(step.id)?.ok ? 'applied' : 'failed') : undefined" aria-hidden="true">
                  <AppIcon v-if="stepResult(step.id)" :name="stepResult(step.id)?.ok ? 'check' : 'alert'" :size="14" />
                </span>
              </li>
            </ul>
          </section>
          <section v-if="failures.length" class="group failures" aria-labelledby="learn-review-failed">
            <h3 id="learn-review-failed">Not applied <span class="count">{{ failures.length }}</span></h3>
            <ul class="steps">
              <li v-for="failure in failures" :key="failure.id" class="failure"><span class="failure-key">{{ failure.key }}</span>{{ failure.message }}</li>
            </ul>
          </section>
        </div>
      </form>
    </dialog>

    <dialog ref="dialog" class="learn-dialog" aria-labelledby="learn-accept-title" @cancel.prevent="close" @click="backdrop">
      <form v-if="accepting" class="learn-card" @submit.prevent="accept">
        <div class="head">
          <span class="mark" aria-hidden="true"><AppIcon name="book" :size="15" /></span>
          <div class="head-copy">
            <h2 id="learn-accept-title">{{ destination === 'draft' ? 'Save a rule draft' : 'Add to a changelog' }}</h2>
          </div>
        </div>
        <fieldset class="dest">
          <legend>Save it as</legend>
          <div class="choices">
            <label><input v-model="destination" type="radio" name="learn-dest" value="changelog" /> Changelog</label>
            <label><input v-model="destination" type="radio" name="learn-dest" value="draft" /> Rule draft</label>
          </div>
        </fieldset>
        <template v-if="destination === 'changelog'">
          <label class="pick" for="learn-entry">Entry</label>
          <select id="learn-entry" v-model="chosen" class="entry" :disabled="!choices.length">
            <option v-if="!choices.length" value="">No entry in this project yet</option>
            <option v-for="entry in choices" :key="entry.id" :value="entry.id">{{ entry.title }}</option>
          </select>
          <p v-if="accepting" class="preview">
            <span>{{ previewDate }}: <template v-for="(part, i) in previewParts" :key="i"><mark v-if="part.mark" class="suspect">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template>.</span>
            <span>Source: <a :href="accepting.href">{{ linkLabel(accepting.key) }}</a>.</span>
          </p>
        </template>
        <template v-else>
          <p v-if="rulesLoading" class="wait">Loading rule sets…</p>
          <p v-else-if="!draftLayers.length" class="wait">No rule set you can draft into.</p>
          <template v-else>
            <label class="pick" for="learn-layer">Layer</label>
            <select id="learn-layer" v-model="layerId" class="entry" @change="loadSets()">
              <option v-for="layer in draftLayers" :key="layer.id" :value="layer.id">{{ layer.label }}</option>
            </select>
            <p v-if="!rulesLoading && !sets.length" class="wait">No rule set you can draft into.</p>
            <template v-else>
              <label class="pick" for="learn-set">Rule set</label>
              <select id="learn-set" v-model="setId" class="entry" :disabled="rulesLoading || !sets.length">
                <option v-for="set in sets" :key="set.id" :value="set.id">{{ set.name }}</option>
              </select>
            </template>
          </template>
          <p v-if="accepting" class="preview">
            <span><template v-for="(part, i) in previewParts" :key="i"><mark v-if="part.mark" class="suspect">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
            <span>Publishing stays a separate approval.</span>
          </p>
        </template>
        <div v-if="suspect && accepting" class="suspect-note" role="alert">
          <p>This looks like a credential — <a :href="accepting.href">remove it</a>, or confirm it is not one.</p>
          <label class="confirm"><input v-model="notCredential" type="checkbox" /> It is not a credential</label>
        </div>
        <p v-if="dialogError" class="problem" role="alert">{{ dialogError }}</p>
        <div class="foot">
          <button type="button" class="btn" @click="close">Cancel</button>
          <button type="submit" class="btn primary" :disabled="busy || (!!suspect && !notCredential) || (destination === 'changelog' ? !chosen : rulesLoading || !setId)">{{ destination === 'draft' ? (busy ? 'Saving…' : 'Save rule draft') : (busy ? 'Adding…' : 'Add to changelog') }}</button>
        </div>
      </form>
    </dialog>
  </section>
</template>

<style scoped>
.learnings { container-type: inline-size; margin: 0 0 18px; padding: 14px 16px 16px; }
.head { display: flex; flex-wrap: wrap; align-items: flex-start; gap: 10px; }
.head-tools { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; margin-left: auto; }
.head > .head-copy { flex: 1 1 12rem; }
.head-copy { min-width: 0; }
h2 { margin: 0; font-size: 15px; font-weight: 600; letter-spacing: -0.01em; }
.wait { margin: 2px 0 0; color: var(--ink-2); font-size: 13px; overflow-wrap: anywhere; }
.mark { display: grid; place-items: center; flex-shrink: 0; width: 28px; height: 28px; border-radius: 8px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); }
.rows { list-style: none; margin: 12px 0 0; padding: 0; display: grid; gap: 8px; }
.row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 10px 16px; align-items: start; min-width: 0; padding: 10px 12px; border-radius: 12px; background: var(--surface-sunken); }
.copy { min-width: 0; }
.line { margin: 0; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere; min-width: 0; font-size: 14px; line-height: 1.4; }
.meta { display: flex; flex-wrap: wrap; gap: 4px 10px; margin: 4px 0 0; color: var(--ink-2); font-size: 12.5px; }
.meta a { color: var(--teal-ink); font-weight: 600; }
.actions { display: flex; gap: 8px; align-items: center; }
.rec { display: flex; gap: 6px; align-items: baseline; margin: 6px 0 0; color: var(--ink); font-size: 13px; line-height: 1.45; overflow-wrap: anywhere; }
.rec.off { color: var(--ink-2); }
.rec em { font-style: normal; color: var(--ink-2); }
.rec-icon { flex: none; align-self: center; color: var(--teal-ink); }
/* Anchored at a fixed top: the list grows downward, away from the controls above it. */
.review-dialog { --review-top: min(10dvh, 80px); width: min(var(--dialog-l), calc(100vw - 24px)); max-height: calc(100dvh - var(--review-top) - 12px); margin: var(--review-top) auto auto; padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.review-dialog::backdrop { background: var(--scrim); backdrop-filter: blur(2px); }
/* A long list of text: the glass gradient sits on an opaque base so the inbox behind never shows through. */
.review-card { display: grid; grid-template-rows: auto auto minmax(0, 1fr); grid-template-areas: "head" "foot" "body"; max-height: calc(100dvh - var(--review-top) - 12px); border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)), var(--surface-raised); box-shadow: var(--shadow-pop), var(--shadow); }
.review-head { grid-area: head; display: flex; align-items: flex-start; gap: 10px; padding: 18px 18px 6px; }
.review-body { grid-area: body; overflow: auto; overscroll-behavior: contain; padding: 4px 18px 14px; outline: none; }
.group h3 { margin: 14px 0 4px; font-size: 12.5px; font-weight: 600; color: var(--ink-2); }
.group .count { font-weight: 500; color: var(--ink-3); }
.steps { list-style: none; margin: 0; padding: 0; }
.step { display: grid; grid-template-columns: auto minmax(0, 1fr) 16px; gap: 10px; align-items: start; padding: 10px 0; border-bottom: 1px solid var(--line); }
.step:last-child { border-bottom: 0; }
.tick { width: 18px; height: 18px; margin: 2px 0 0; accent-color: var(--teal); }
.step-copy { display: grid; gap: 3px; min-width: 0; cursor: pointer; }
.step.out .step-line { color: var(--ink-2); }
.step-line { font-size: 14px; line-height: 1.45; overflow-wrap: anywhere; }
.step-meta { display: flex; flex-wrap: wrap; gap: 2px 10px; color: var(--ink-2); font-size: 12.5px; overflow-wrap: anywhere; }
.step-note { color: var(--ink-2); font-size: 12.5px; }
.step-mark { display: grid; place-items: center; width: 16px; height: 20px; color: var(--teal-ink); }
.step-mark.bad { color: var(--danger); }
.failure { padding: 8px 0; border-bottom: 1px solid var(--line); color: var(--ink); font-size: 13px; line-height: 1.45; overflow-wrap: anywhere; }
.failure:last-child { border-bottom: 0; }
.failure-key { margin-right: 8px; color: var(--ink-2); font-weight: 600; }
.review-foot { grid-area: foot; display: flex; flex-wrap: wrap; align-items: flex-start; gap: 8px 16px; padding: 8px 18px 12px; border-bottom: 1px solid var(--line); }
.review-status { flex: 1 1 14rem; margin: 0; padding-top: 8px; color: var(--ink-2); font-size: 13px; line-height: 1.45; }
.review-actions { display: flex; gap: 8px; margin-left: auto; }
.review-actions .btn { min-height: 36px; }
.actions .btn { display: inline-flex; align-items: center; justify-content: center; min-height: 32px; }
.more { margin: 10px 0 0; color: var(--ink-3); font-size: 12.5px; }
.learn-dialog { width: min(var(--dialog-m), calc(100vw - 24px)); max-height: calc(100dvh - 24px); padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.learn-dialog::backdrop { background: var(--scrim); }
.learn-card { display: grid; gap: 10px; max-height: calc(100dvh - 24px); overflow: auto; padding: 18px 18px 16px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); }
.pick { font-size: 12.5px; font-weight: 600; color: var(--ink-2); }
.dest { margin: 0; padding: 0; border: 0; min-width: 0; }
.dest legend { padding: 0; font-size: 12.5px; font-weight: 600; color: var(--ink-2); }
.choices { display: flex; flex-wrap: wrap; gap: 8px 16px; margin-top: 6px; }
.choices label { display: inline-flex; align-items: center; gap: 6px; min-height: 32px; font-size: 14px; }
.entry { width: 100%; min-width: 0; max-width: 100%; min-height: 40px; padding: 0 10px; border-radius: 10px; border: 1px solid var(--line-2); background: var(--field-bg); color: var(--ink); }
.disclose { margin-top: 10px; }
.preview { display: grid; gap: 4px; margin: 0; padding: 10px 12px; border-radius: 10px; background: var(--code-bg); color: var(--ink); font-size: 13px; line-height: 1.45; }
.preview span { overflow-wrap: anywhere; }
.preview a { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 2px; }
.problem { margin: 0; color: var(--danger); font-size: 13px; }
.suspect { padding: 0 2px; border-radius: 3px; background: color-mix(in srgb, var(--warn) 24%, transparent); color: inherit; }
.suspect-note { display: grid; gap: 6px; margin: 0; padding: 10px 12px; border-radius: 10px; color: var(--ink); background: color-mix(in srgb, var(--warn) 10%, transparent); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--warn) 32%, transparent); font-size: 13px; line-height: 1.45; }
.suspect-note p { margin: 0; overflow-wrap: anywhere; }
.suspect-note a { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 2px; }
.confirm { display: inline-flex; align-items: center; gap: 8px; min-height: 32px; font-size: 14px; }
.foot { display: flex; justify-content: flex-end; gap: 8px; margin-top: 4px; }
.foot .btn { display: inline-flex; align-items: center; justify-content: center; min-height: 36px; }
@container (max-width: 560px) {
  .row { grid-template-columns: minmax(0, 1fr); align-items: start; }
  .actions { flex-wrap: wrap; }
  .actions .btn { flex: none; width: auto; min-height: 40px; }
}
@media (max-width: 600px) {
  /* Phone: a full-height sheet with the actions pinned at the bottom. */
  .review-dialog { width: 100vw; max-width: 100vw; height: 100dvh; max-height: 100dvh; margin: 0; }
  .review-card { grid-template-rows: auto minmax(0, 1fr) auto; grid-template-areas: "head" "body" "foot"; height: 100dvh; max-height: 100dvh; border-radius: 0; border: 0; padding-bottom: env(safe-area-inset-bottom); }
  .review-head { padding: max(16px, env(safe-area-inset-top)) 18px 12px; border-bottom: 1px solid var(--line); }
  .review-foot { align-items: center; padding: 10px 18px 16px; border-top: 1px solid var(--line); border-bottom: 0; }
  .review-status { padding-top: 0; }
  .review-actions { flex: 1 1 100%; }
  .review-actions .btn { flex: 1 1 0; min-height: 44px; }
}
@media (pointer: coarse) {
  .step { padding: 12px 0; }
  .tick { width: 22px; height: 22px; }
}
@media (max-width: 480px) {
  .foot { flex-direction: column-reverse; }
  .foot .btn { min-height: 40px; }
  .learn-card { padding: 16px 14px 14px; }
}
@media (prefers-reduced-motion: reduce) {
  .learn-dialog, .row, .btn { transition: none; }
}
</style>
