<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { setPageTitle } from '../../lib/brand'
import { headerFolded } from '../../lib/chrome'
import { confirmAction } from '../../lib/confirm'
import { usePreference } from '../../lib/preferences'
import { selectionAnchor } from '../../lib/quotePresence'
import type { ConflictChoices } from '../../lib/quoteMerge'
import { acquireQuote, dropQuote, releaseQuote, type LiveQuote, type Scope } from '../../lib/quoteWorkspace'
import { getDraft } from '../../lib/quotes/api'
import { QuoteEditor } from '../../lib/quotes/editor'
import { branchQuote, duplicateQuote, finalizeQuote, getLink, getVersion, issueConfirm, issueError, lifecycleError, linkUrl, reviseConfirm, setArchived, undoQuote, type QuoteVersion } from '../../lib/quotes/lifecycle'
import { statusOf } from '../../lib/quotes/list'
import { getProfile, listProfiles, selectQuoteProfile, type QuoteProfile } from '../../lib/quotes/profile'
import { readZoom, zoomPercent, type ZoomMode } from '../../lib/quotes/zoom'
import { saveProblems, type SaveProblem } from '../../lib/quotes/saveProblems'
import { downloadRecovery } from '../../lib/quoteRecovery'
import type { QuoteDocumentData } from '../../lib/quotes/types'
import { toast } from '../../lib/toast'
import { useBusiness } from '../../stores/business'
import { useQuotes } from '../../stores/quotes'
import { useSession } from '../../stores/session'
import QuoteConflictDialog from '../quotes/collaboration/QuoteConflictDialog.vue'
import QuotePresenceOverlay from '../quotes/collaboration/QuotePresenceOverlay.vue'
import QuoteDetails from '../quotes/details/QuoteDetails.vue'
import QuoteDocument from '../quotes/editor/QuoteDocument.vue'
import QuoteIcon from '../quotes/inspector/QuoteIcon.vue'
import QuoteInspector from '../quotes/inspector/QuoteInspector.vue'
import QuoteTitleBar from '../quotes/QuoteTitleBar.vue'
import QuoteProfilePicker from '../quotes/QuoteProfilePicker.vue'
import AppIcon from '../AppIcon.vue'

// One quote at work: the title bar, the paper on a desk you can zoom, and the side
// panel with Format (while it is a draft) and Details (status, customer link,
// receipt, versions, files). The same component is the full page and the panel
// docked beside the Quotes list; both borrow the live quote (P6 session,
// presence and undo history) from lib/quoteWorkspace, so switching between them
// keeps unsaved work and never rejoins. Its layout follows its own width.
const props = defineProps<{ quoteId: string; layout: 'full' | 'dock'; autoPrint?: boolean }>()
const emit = defineEmits<{ close: []; expand: []; collapse: []; open: [quoteId: string]; printed: [] }>()
const identity = useSession()
const business = useBusiness()
const quotes = useQuotes()
const root = ref<HTMLElement | null>(null)
const host = ref<HTMLElement | null>(null)
const desk = ref<HTMLElement | null>(null)
const paper = ref<InstanceType<typeof QuoteDocument> | null>(null)

// ---------- The live quote ----------
const scope = computed<Scope | null>(() => identity.identity ? { tenantId: identity.identity.tenant.id, principalId: identity.identity.principal.id } : null)
const live = shallowRef<LiveQuote | null>(null)
const versionDocs = shallowRef(new Map<number, QuoteVersion>())
const busy = ref('')
const profileBusy = ref(false)
const printing = ref(false)
let generation = 0
const viewerKey = () => `${scope.value?.tenantId ?? ''}/${scope.value?.principalId ?? ''}`
function capture() {
  const id = props.quoteId, viewer = viewerKey(), request = generation, quote = live.value
  return { id, quote, current: () => request === generation && id === props.quoteId && viewer === viewerKey() && quote === live.value && !!scope.value && identity.authenticationCurrent?.() !== false }
}
let held: { scope: Scope; quote: LiveQuote } | null = null
function hold() {
  if (held) { if (held.quote.canSaveNow === numericValid) held.quote.canSaveNow = null; releaseQuote(held.scope, held.quote) }
  generation++; busy.value = ''; profileBusy.value = false; printing.value = false
  versionDocs.value = new Map()
  profiles.value = null; profileNames.value = new Map()
  held = null; live.value = null; viewing.value = null
  if (!scope.value) return
  const quote = acquireQuote(scope.value, props.quoteId)
  quote.canSaveNow = numericValid
  held = { scope: scope.value, quote }; live.value = quote
}
const viewing = ref<number | null>(null)
const profiles = ref<QuoteProfile[] | null>(null)
const profileNames = ref(new Map<string, string>())
watch(() => [props.quoteId, scope.value?.tenantId, scope.value?.principalId], hold, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { generation++; if (held) { if (held.quote.canSaveNow === numericValid) held.quote.canSaveNow = null; releaseQuote(held.scope, held.quote) }; held = null })

const view = computed(() => live.value?.view.value ?? null)
const projection = computed(() => live.value?.projection.value ?? null)
const frozen = computed(() => live.value?.frozen.value ?? null)
const presence = computed(() => live.value?.presence.value ?? null)
const recovery = computed(() => live.value?.recovery.value ?? null)
const error = computed(() => live.value?.error.value ?? '')
const offerNo = computed(() => projection.value?.offer_no ?? '')
const status = computed(() => projection.value ? statusOf(projection.value) : 'draft')
const isDraft = computed(() => !projection.value || projection.value.state === 'draft')
const revising = computed(() => isDraft.value && (projection.value?.current_version ?? 0) > 0)
const admin = computed(() => business.admin)
const staff = computed(() => business.staff)
const publicUrl = ref('')
let linkRead = 0
async function loadPublicLink() {
  const action = capture()
  const current = ++linkRead
  publicUrl.value = ''
  const version = projection.value?.current_version
  if (!admin.value || isDraft.value || !version) return
  try {
    const link = await getLink(action.id, version)
    if (current === linkRead && action.current()) publicUrl.value = link?.path ? linkUrl(link) : ''
  } catch { /* The document remains printable without a customer link. */ }
}
watch(() => [props.quoteId, projection.value?.current_version, projection.value?.state, admin.value], () => { void loadPublicLink() }, { immediate: true })
// Work that is not on the server yet (leaving asks first; printing and issuing save it).
const canSave = computed(() => view.value?.local === 'dirty' || view.value?.local === 'failed' || view.value?.local === 'offline')
// Why the last save did not land, and what in the quote to fix (FX3's field reasons).
const failure = computed(() => view.value?.local === 'failed' ? view.value.failure ?? null : null)
const problems = computed<SaveProblem[]>(() => failure.value?.kind === 'invalid' ? saveProblems(failure.value.body, view.value?.working ?? null) : [])
// The title bar's Save: ready only when saving can land. A refused document needs a
// fix first, a newer draft a review; a passing failure or lost connection can retry.
const saveReady = computed(() => {
  const v = view.value
  if (!v || viewing.value !== null || v.remote === 'newer' || v.review) return false
  if (v.local === 'dirty' || v.local === 'offline') return true
  return v.local === 'failed' && failure.value?.kind === 'server'
})
const saveHint = computed(() => {
  const v = view.value
  if (!v) return ''
  if (v.local === 'failed' && failure.value?.kind === 'invalid') return 'Fix what the notice below names; the draft then saves by itself'
  if (v.local === 'failed' && failure.value?.kind === 'forbidden') return 'This draft cannot be saved from here any more'
  if (v.remote === 'newer' || v.review || v.local === 'conflict') return 'Review the newer draft first'
  if (v.local === 'saving') return 'Saving…'
  return 'Nothing to save'
})
const editable = computed(() => isDraft.value && viewing.value === null && !!view.value && view.value.local !== 'read-only' && view.value.local !== 'loading' && staff.value)
// An older version picked in Details, the frozen current version once issued, else the draft.
const document = computed<QuoteDocumentData | null>(() => {
  if (viewing.value !== null) return versionDocs.value.get(viewing.value)?.document ?? null
  if (!isDraft.value && (projection.value?.current_version ?? 0) > 0) return frozen.value?.document ?? null
  return view.value?.working ?? null
})
// The editor (and its undo history) belongs to the live quote while it is a draft.
const editor = computed(() => {
  const quote = live.value, working = view.value?.working
  if (!quote || !working || !isDraft.value || viewing.value !== null) return null
  quote.editor ??= new QuoteEditor(working)
  return quote.editor
})
// ---------- The document profile (U19): picked on a draft, frozen on a version ----------
const currentProfile = computed(() => document.value?.profile ?? null)
// Names as they were at the revision the quote holds (a profile may be renamed later).
const profileName = computed(() => {
  const p = currentProfile.value
  if (!p) return ''
  return profileNames.value.get(`${p.id}:${p.revision}`) ?? profiles.value?.find(item => item.id === p.id)?.name ?? ''
})
watch(currentProfile, async p => {
  if (!p || profileNames.value.has(`${p.id}:${p.revision}`)) return
  const action = capture()
  try { const at = await getProfile(p.id, p.revision); if (!action.current()) return; profileNames.value = new Map(profileNames.value).set(`${p.id}:${p.revision}`, at.name) } catch { /* the list's name stands in */ }
}, { immediate: true })
async function loadProfiles() {
  if (!staff.value) return
  const action = capture()
  try { const loaded = await listProfiles(); if (action.current()) profiles.value = loaded } catch { if (action.current()) profiles.value = [] }
}
watch(() => [props.quoteId, isDraft.value], () => { if (isDraft.value) void loadProfiles() }, { immediate: true })
// Choosing saves pending edits first, then takes the profile's current revision.
async function chooseProfile(profileId: string, undoing = false, expectedRevision?: number) {
  const action = capture(), quote = action.quote
  if (!quote || !editable.value || profileBusy.value) return
  const before = currentProfile.value
  if (expectedRevision !== undefined && quote.view.value?.baseRevision !== expectedRevision) { toast('The profile changed since. Review it before undoing.', { tone: 'error' }); return }
  if ((before?.id ?? '') === profileId && !undoing && (profiles.value?.find(p => p.id === profileId)?.revision ?? 0) <= (before?.revision ?? 0)) return
  profileBusy.value = true
  try {
    if (canSave.value) {
      await quote.session.save()
      if (!action.current()) return
      if (quote.view.value?.local !== 'clean') { toast('Save the draft before choosing another profile.', { tone: 'error' }); return }
    }
    if (!action.current()) return
    await selectQuoteProfile(action.id, quote.view.value?.baseRevision ?? 0, profileId)
    if (!action.current()) return
    await quote.session.reload(true)
    if (!action.current()) return
    const undoRevision = quote.view.value?.baseRevision
    const name = profileId ? profiles.value?.find(p => p.id === profileId)?.name ?? 'the profile' : 'the standard document'
    if (!undoing) toast(`This draft now uses ${name}.`, { action: { label: 'Undo', run: () => { if (action.current()) void chooseProfile(before?.id ?? '', true, undoRevision); else toast('Open the original quote to undo its profile change.') } }, timeout: 8000 })
    else toast(`Back to ${before ? profileName.value || 'the earlier profile' : 'the standard document'}.`)
  } catch (e) {
    if (!action.current()) return
    toast(lifecycleError(e, 'The profile was not changed.'), { tone: 'error' })
  } finally { if (action.current()) profileBusy.value = false }
}

// On its own page the tab and the breadcrumb name the quote.
watch(offerNo, number => { if (props.layout === 'full' && number) setPageTitle(number) }, { immediate: true })
// The customer's current name from the list (loaded quietly if this page opened first),
// else the name printed on the document.
if (!quotes.items && !quotes.loading) void quotes.load()
const customerName = computed(() => quotes.items?.find(q => q.quote_node_id === props.quoteId)?.customer_name || document.value?.recipient.name || '')
const remoteName = computed(() => presence.value?.sessions.find(s => s.principal_id === view.value?.remoteActorId)?.name ?? '')

function edit(value: QuoteDocumentData) {
  const quote = live.value
  if (!quote || !editable.value) return
  quote.session.edit(value)
  const client = quote.presenceClient
  if (client && paper.value) {
    const revision = view.value?.baseRevision ?? 0
    void selectionAnchor(paper.value.editor.selection, value, revision).then(anchor => { if (client === quote.presenceClient) client.setSelection(anchor, 'editing', revision) })
  }
}
function numericValid() {
  const invalid = root.value?.querySelector<HTMLInputElement>('input:invalid')
  if (!invalid) return true
  invalid.reportValidity()
  toast('Correct the highlighted number before saving.', { tone: 'error' })
  return false
}
function save() { if (numericValid()) void live.value?.session.save() }
function retry() { if (numericValid()) void live.value?.session.retry() }
// "Show" on a save problem: the section or position on the paper, or the field on the Document tab.
function showProblem(problem: SaveProblem) {
  const target = problem.target
  if (!target) return
  if ('section' in target) { jump(target.section); return }
  if ('position' in target) {
    const row = root.value?.querySelector<HTMLElement>(`[data-position-id="${CSS.escape(target.position)}"]`)
    row?.scrollIntoView({ block: 'center', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
    row?.querySelector<HTMLElement>('input, [contenteditable="true"]')?.focus({ preventScroll: true })
    return
  }
  openPane('format')
  reveal.value = { tab: 'document', target: 'quote-this-quote', n: (reveal.value?.n ?? 0) + 1 }
}
const failureLead = computed(() => {
  const f = failure.value
  if (!f) return ''
  if (f.kind === 'invalid') return problems.value.length ? (problems.value.length === 1 ? 'One part of this draft needs a fix before it can be saved.' : `${problems.value.length} parts of this draft need a fix before it can be saved.`) : `The server did not accept this draft${f.message ? `: ${f.message.replace(/\.$/, '')}` : ''}.`
  if (f.kind === 'forbidden') return f.status === 401 ? 'Your sign-in has ended, so this draft could not be saved.' : 'You can no longer edit this draft.'
  return 'The server could not save it just now.'
})

// ---------- Unsaved work from an earlier visit ----------
// On a draft it can be restored. On an issued version it never goes onto the version
// (that stays as issued): it can start the next version, or be downloaded or discarded.
const canRevise = computed(() => admin.value && (status.value === 'issued' || status.value === 'expired' || status.value === 'accepted') && !projection.value?.archived)
const nextVersion = computed(() => (projection.value?.current_version ?? 0) + 1)
function useRecovery() { const quote = live.value; if (quote?.recovery.value && isDraft.value) { quote.session.restore(quote.recovery.value); quote.recovery.value = null } }
async function useServer() { const quote = live.value; if (!quote) return; quote.recovery.value = null; await quote.session.reload(true) }
async function discardRecovery() {
  const quote = live.value
  if (!quote?.recovery.value) return
  quote.recovery.value = null
  await quote.session.discardRecovery()
  toast('Your unsaved work from before is discarded. The issued version is unchanged.')
}
function downloadWork() { const stored = live.value?.recovery.value; if (stored) downloadRecovery(stored) }
async function reviseWithWork() {
  const stored = live.value?.recovery.value
  if (!stored || !canRevise.value) return
  const revised = await revise(true)
  if (!revised) return
  // The new draft opens in a fresh session; the work lands once it has, and replaces
  // any copy that session found (it came from before the revision).
  const quote = revised.quote
  if (!quote || !revised.current()) return
  await quote.ready
  if (!revised.current()) return
  quote.recovery.value = null
  const outcome = await quote.session.adopt(stored)
  if (!revised.current()) return
  if (outcome === 'applied') toast(`Revising version ${nextVersion.value - 1} with your unsaved work. It saves as you go.`)
  else if (outcome === 'review') { reviewOpen.value = true; toast('Your work and the issued version changed the same places. Choose what to keep.') }
  else toast('The revision is open, but your earlier work could not be applied. Download it to keep a copy.', { tone: 'error' })
}

// ---------- Undo and redo, from the paper's editor ----------
const version = computed(() => paper.value?.version ?? 0)
const canUndo = computed(() => { void version.value; return editable.value && !!paper.value?.editor.history.canUndo })
const canRedo = computed(() => { void version.value; return editable.value && !!paper.value?.editor.history.canRedo })
function undo() { if (!editable.value) return; paper.value?.editor.undo(); paper.value?.touch() }
function redo() { if (!editable.value) return; paper.value?.editor.redo(); paper.value?.touch() }

// ---------- Layout: side panel docked, over the paper, or a sheet ----------
const width = ref(1200)
const phoneQuery = window.matchMedia('(max-width: 600px)')
const phone = ref(phoneQuery.matches)
const phoneChange = () => { phone.value = phoneQuery.matches }
let sizer: ResizeObserver | undefined
const mode = computed<'dock' | 'overlay' | 'sheet'>(() => phone.value ? 'sheet' : width.value >= 1080 ? 'dock' : 'overlay')
const compact = computed(() => phone.value || width.value < 780)
// The side panel docks only on a full page wide enough for it; docked beside the list it floats.
const sideMode = computed<'dock' | 'overlay' | 'sheet'>(() => mode.value === 'sheet' ? 'sheet' : mode.value === 'dock' && props.layout === 'full' ? 'dock' : 'overlay')
type Pane = 'format' | 'details'
const pref = usePreference<{ zoom?: ZoomMode; phoneZoom?: ZoomMode; inspector?: boolean; pane?: Pane | 'none'; headerFolded?: boolean }>('quote-editor')
const floating = ref<Pane | null>(null)
const savedPane = computed<Pane | null>(() => {
  const stored = pref.value.value
  if (stored?.pane === 'none' || stored?.inspector === false) return null
  return stored?.pane === 'details' ? 'details' : 'format'
})
const canFormat = computed(() => isDraft.value && viewing.value === null)
const pane = computed<Pane | null>(() => {
  const chosen = sideMode.value === 'dock' ? savedPane.value : floating.value
  if (chosen === 'format' && !canFormat.value) return sideMode.value === 'dock' ? 'details' : null
  return chosen
})
function setPane(next: Pane) {
  if (sideMode.value === 'dock') {
    const open = pane.value === next ? 'none' : next
    pref.save({ ...(pref.value.value ?? {}), pane: open, inspector: open !== 'none' })
  } else floating.value = floating.value === next ? null : next
}
function openPane(next: Pane) { if (pane.value !== next) setPane(next) }
function toggleHeader() {
  headerFolded.value = !headerFolded.value
  pref.save({ ...(pref.value.value ?? {}), headerFolded: headerFolded.value })
}
if (props.layout === 'full') void pref.ready.then(() => { headerFolded.value = !!pref.value.value?.headerFolded })

// ---------- Zoom ----------
const deskSize = ref({ width: 1200, height: 800 })
// Docked beside the list the paper fits the panel's width unless you zoom it there;
// the full page keeps your zoom.
const dockZoom = ref<ZoomMode | null>(null)
// A phone keeps its own zoom: a desk's 100 % would put most of the page off screen (AEON-140).
const effectiveZoom = computed<ZoomMode>(() => props.layout === 'dock' ? dockZoom.value ?? 'width' : phone.value ? readZoom(pref.value.value?.phoneZoom, 'width') : readZoom(pref.value.value?.zoom, 100))
const percent = computed(() => zoomPercent(effectiveZoom.value, deskSize.value))
function setZoom(next: ZoomMode) { if (props.layout === 'dock') dockZoom.value = next; else pref.save({ ...(pref.value.value ?? {}), [phone.value ? 'phoneZoom' : 'zoom']: next }) }
// Both are measured once before the first paint (so the bar never lays out for a
// width it does not have and then jumps), then followed as they change.
function contentBox(el: HTMLElement) {
  const box = getComputedStyle(el)
  return { width: el.clientWidth - parseFloat(box.paddingLeft) - parseFloat(box.paddingRight), height: el.clientHeight - parseFloat(box.paddingTop) - parseFloat(box.paddingBottom) }
}
watch(desk, el => { sizer?.disconnect(); if (el) { deskSize.value = contentBox(el); sizer = new ResizeObserver(([entry]) => { deskSize.value = { width: entry.contentRect.width, height: entry.contentRect.height } }); sizer.observe(el) } }, { flush: 'post' })
let rootSizer: ResizeObserver | undefined
watch(root, el => { rootSizer?.disconnect(); if (el) { width.value = contentBox(el).width; rootSizer = new ResizeObserver(([entry]) => { width.value = entry.contentRect.width }); rootSizer.observe(el) } }, { flush: 'post' })

// ---------- PDF: save first, then the browser's print (or Save as PDF) ----------
// Opened from the list's PDF action: print once the paper is laid out.
watch(() => props.autoPrint && !!document.value && !!paper.value, ready => {
  if (!ready) return
  emit('printed')
  void nextTick(() => print())
}, { immediate: true })
async function print() {
  const action = capture()
  if (printing.value) return
  if (!numericValid()) return
  printing.value = true
  try {
    if (canSave.value) await action.quote?.session.save()
    if (!action.current()) return
    if (isDraft.value && view.value?.local !== 'clean') { toast('Save the quote before printing it.', { tone: 'error' }); return }
    if (!isDraft.value && admin.value && !publicUrl.value) await loadPublicLink()
    if (!action.current()) return
    await paper.value?.whenReady()
    await nextTick()
    if (action.current()) window.print()
  } finally { if (action.current()) printing.value = false }
}

// ---------- Lifecycle: issue, revise, duplicate, archive ----------
async function issue() {
  const action = capture(), quote = action.quote, current = projection.value
  if (!quote || !current || busy.value) return
  if (!numericValid()) return
  const next = current.current_version + 1
  const ok = await confirmAction(issueConfirm(offerNo.value, next))
  if (!ok || !action.current()) return
  busy.value = 'issue'
  try {
    if (canSave.value) await quote.session.save()
    if (!action.current()) return
    if (quote.view.value?.local !== 'clean') { toast('Save the quote before issuing it.', { tone: 'error' }); return }
    const draft = await getDraft(action.id)
    if (!action.current()) return
    if (draft.draft_revision !== quote.view.value.baseRevision) { toast('Someone saved a newer draft. Review it before issuing.', { tone: 'error' }); await quote.session.check(); return }
    await finalizeQuote(action.id, { expected_quote_revision: current.revision, expected_draft_revision: draft.draft_revision, expected_document_sha256: draft.document_sha256 })
    if (!action.current()) return
    await quote.refresh()
    if (!action.current()) return
    quotes.patch(action.id, { state: 'issued', classic_status: 'sent', current_version: next })
    openPane('details')
    toast(`Issued as version ${next}. Share it with a customer link.`)
  } catch (e) {
    if (!action.current()) return
    toast(issueError(e), { tone: 'error' })
    void quote.refresh().catch(() => {})
  } finally { if (action.current()) busy.value = '' }
}
async function revise(withWork = false): Promise<ReturnType<typeof capture> | null> {
  const action = capture(), quote = action.quote, current = projection.value, version = frozen.value
  if (!quote || !current || !version || busy.value || !scope.value) return null
  const ok = await confirmAction(reviseConfirm(current.current_version, status.value === 'accepted'))
  if (!ok || !action.current()) return null
  busy.value = 'revise'
  try {
    await branchQuote(action.id, { expected_quote_revision: current.revision, expected_version: current.current_version, expected_content_sha256: version.content_sha256 })
    if (!action.current()) return null
    quotes.patch(action.id, { state: 'draft', classic_status: 'draft' })
    // A new draft needs a new edit session: the issued one was read-only for good.
    const done = held
    held = null
    if (done) dropQuote(done.scope, done.quote)
    hold()
    openPane('format')
    if (!withWork) toast(`You are revising version ${current.current_version}. Issue it when it is ready.`)
    return capture()
  } catch (e) { if (!action.current()) return null; toast(lifecycleError(e, 'The quote could not be revised. Nothing changed.'), { tone: 'error' }); return null }
  finally { if (action.current()) busy.value = '' }
}
async function duplicate() {
  const action = capture(), current = projection.value
  if (!current || busy.value) return
  busy.value = 'duplicate'
  try {
    const copy = await duplicateQuote(action.id, current.revision)
    if (!action.current()) return
    void quotes.load(true)
    toast(`Duplicated as ${copy.offer_no ?? 'a new draft'}.`, {
      action: { label: 'Undo', run: () => { void undoQuote(copy).then(() => { void quotes.load(true); if (props.quoteId === copy.quote_node_id) emit('open', current.quote_node_id) }).catch(e => toast(e instanceof Error ? e.message : 'Undo did not work.', { tone: 'error' })) } },
      timeout: 8000,
    })
    emit('open', copy.quote_node_id)
  } catch (e) { if (action.current()) toast(lifecycleError(e, 'The quote was not duplicated.'), { tone: 'error' }) }
  finally { if (action.current()) busy.value = '' }
}
async function archive() {
  const action = capture(), quote = action.quote, current = projection.value
  if (!quote || !current || busy.value) return
  const archiving = !current.archived
  const id = props.quoteId, name = offerNo.value || 'The quote'
  busy.value = 'archive'
  try {
    // No question first: it keeps everything, and the toast undoes it.
    const receipt = await setArchived(id, current.revision, archiving)
    if (!action.current()) return
    await quote.refresh()
    if (!action.current()) return
    quotes.patch(id, { archived: archiving })
    toast(archiving ? `${name} is archived. It keeps its versions, evidence and files.` : `${name} is back in the list.`, {
      action: { label: 'Undo', run: () => { void undoQuote(receipt).then(async () => { await quote.refresh(); quotes.patch(id, { archived: !archiving }) }).catch(e => toast(e instanceof Error ? e.message : 'Undo did not work.', { tone: 'error' })) } },
      timeout: 8000,
    })
  } catch (e) { if (action.current()) toast(lifecycleError(e, 'That did not work.'), { tone: 'error' }) }
  finally { if (action.current()) busy.value = '' }
}
async function copyNumber() {
  try { await navigator.clipboard.writeText(offerNo.value); toast(`Copied ${offerNo.value}.`) } catch { toast('Copying did not work here.', { tone: 'error' }) }
}
let versionRead = 0
async function showVersion(version: number | null) {
  const action = capture(), request = ++versionRead
  if (version === null) { viewing.value = null; return }
  if (!versionDocs.value.has(version)) {
    try {
      const loaded = await getVersion(action.id, version)
      if (!action.current() || request !== versionRead) return
      versionDocs.value = new Map(versionDocs.value).set(version, loaded)
    } catch (e) { if (action.current() && request === versionRead) toast(lifecycleError(e, 'That version could not be opened.'), { tone: 'error' }); return }
  }
  if (action.current() && request === versionRead) viewing.value = version
}

// ---------- Reviewing a newer draft ----------
const reviewOpen = ref(false)
watch(() => view.value?.review ?? null, review => { if (!review) reviewOpen.value = false })
async function review() {
  const action = capture(), quote = action.quote
  if (!quote) return
  if (quote.view.value?.local === 'clean' && !quote.view.value.review) { await quote.session.reload(); return }
  if (!quote.view.value?.review) await quote.session.reviewChanges()
  if (action.current()) reviewOpen.value = true
}
async function resolve(choices: ConflictChoices) {
  const action = capture()
  const saved = await action.quote?.session.acceptReview(choices)
  if (!action.current()) return
  reviewOpen.value = false
  if (saved) toast('Merged and saved.')
}
async function discard() {
  const action = capture()
  if (!await confirmAction({ title: 'Discard your changes?', confirmLabel: 'Discard and reload', danger: true, body: 'Your edits since the last save are replaced by the newer draft. Download your copy first if you might need it.' })) return
  if (!action.current()) return
  reviewOpen.value = false
  await action.quote?.session.reload(true)
}

// ---------- Keys ----------
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
function keys(event: KeyboardEvent) {
  if (event.defaultPrevented || !live.value) return
  const target = event.target as HTMLElement
  // Docked, only keys meant for this panel act on it.
  if (props.layout === 'dock' && !root.value?.contains(target) && target !== globalThis.document.body) return
  const field = !!target.closest?.('input, textarea, select')
  const mod = mac ? event.metaKey : event.ctrlKey
  const key = event.key.toLowerCase()
  if (mod && !event.altKey && key === 's') { event.preventDefault(); save(); return }
  if (field || globalThis.document.querySelector('dialog[open]')) return
  if (mod && !event.altKey && (key === 'z' || key === 'y')) { event.preventDefault(); if (key === 'y' || event.shiftKey) redo(); else undo(); return }
  if (mod && !event.altKey && (key === 'b' || key === 'i') && editable.value && paper.value) { event.preventDefault(); paper.value.setMarks(key === 'b' ? 'bold' : 'italic'); paper.value.touch(); return }
  if (event.key === 'Escape' && !globalThis.document.querySelector('.floating')) {
    if (sideMode.value !== 'dock' && floating.value) { floating.value = null; return }
    if (props.layout === 'dock' && !target.isContentEditable) { event.preventDefault(); emit('close') }
  }
}
function hasLocalWork() {
  return !!root.value?.querySelector('input:invalid') || (!!view.value && ['dirty', 'saving', 'failed', 'offline', 'conflict'].includes(view.value.local))
}
function beforeUnload(event: BeforeUnloadEvent) {
  if (!hasLocalWork()) return
  event.preventDefault()
  event.returnValue = ''
}
onBeforeRouteLeave(async to => {
  if (to.path === '/signin' && identity.requiresSignIn) return true
  const sameQuote = to.path === `/business/quotes/${props.quoteId}` || (to.path === '/business/quotes' && to.query.quote === props.quoteId)
  if (sameQuote || !hasLocalWork()) return true
  const quote = live.value
  if (quote && view.value?.local === 'dirty' && view.value.remote === 'current' && numericValid()) await quote.session.save()
  if (!hasLocalWork()) return true
  return confirmAction({
    title: 'Leave with unsaved changes?', confirmLabel: 'Leave and keep a recovery copy',
    body: 'Your edits could not be saved. A recovery copy remains in this browser tab so you can restore them when you reopen the quote.',
  })
})
onMounted(() => { window.addEventListener('keydown', keys); window.addEventListener('beforeunload', beforeUnload); phoneQuery.addEventListener('change', phoneChange) })
onBeforeUnmount(() => { window.removeEventListener('keydown', keys); window.removeEventListener('beforeunload', beforeUnload); phoneQuery.removeEventListener('change', phoneChange); sizer?.disconnect(); rootSizer?.disconnect() })
onBeforeUnmount(() => { linkRead++ })
function detailsChanged() { void live.value?.refresh(); void loadPublicLink() }
// Clicking the footer mark on a page opens its settings on the Document tab.
const reveal = ref<{ tab: 'document'; target: string; n: number } | null>(null)
function revealMark() {
  if (!editable.value) return
  openPane('format')
  reveal.value = { tab: 'document', target: 'quote-footer-mark', n: (reveal.value?.n ?? 0) + 1 }
}
function jump(id: string) { paper.value?.jump(id); if (sideMode.value === 'sheet') floating.value = null }
defineExpose({ focus: () => root.value?.focus({ preventScroll: true }), issue, revise, dirty: () => canSave.value })
</script>

<template>
  <section ref="root" class="quote-ws" :data-recovery-ready="live?.recoveryReady.value ?? false" :class="[`layout-${layout}`, `side-${sideMode}`, { 'side-open': !!pane && !!document, compact }]" :aria-label="layout === 'dock' ? `Quote ${offerNo}`.trim() : 'Quote editor'" tabindex="-1">
    <QuoteTitleBar
      class="quote-titlebar" :offer-no="offerNo" :status="status" :archived="!!projection?.archived" :revising="revising" :local="viewing !== null ? 'read-only' : view?.local ?? 'loading'"
      :can-save="saveReady" :save-hint="saveHint" :can-undo="canUndo" :can-redo="canRedo" :zoom="effectiveZoom" :percent="percent" :pane="pane" :can-format="canFormat"
      :header-collapsed="headerFolded" :printing="printing" :compact="compact" :layout="layout" :presence="presence" :principal-id="identity.identity?.principal.id ?? ''"
      :admin="admin" :staff="staff"
      @save="save" @undo="undo" @redo="redo" @zoom="setZoom" @print="print" @toggle-header="toggleHeader" @pane="setPane"
      @close="emit('close')" @expand="emit('expand')" @collapse="emit('collapse')" @duplicate="duplicate" @archive="archive" @copy-number="copyNumber" @issue="issue"
    >
      <template v-if="document && (isDraft ? staff : true)" #profile="{ folded }">
        <QuoteProfilePicker
          :current="currentProfile" :name="profileName" :profiles="profiles" :editable="editable" :busy="profileBusy" :compact="compact || folded" :admin="admin"
          @open="loadProfiles" @choose="id => chooseProfile(id)"
        />
      </template>
    </QuoteTitleBar>
    <div v-if="error || recovery || viewing !== null || view?.remote === 'newer' || view?.review || view?.local === 'failed' || view?.local === 'offline'" class="quote-notices">
      <p v-if="error" class="notice bad" role="alert"><QuoteIcon name="alert" :size="15" /><span>{{ error }}</span><RouterLink v-if="layout === 'full'" class="btn sm" to="/business/quotes">Back to Quotes</RouterLink></p>
      <p v-if="viewing !== null" class="notice" role="status"><QuoteIcon name="history" :size="15" /><span>You are reading version {{ viewing }} as it was issued. It cannot change.</span><button type="button" class="btn sm" @click="viewing = null">{{ isDraft ? 'Back to the draft' : 'Back to the current version' }}</button></p>
      <p v-if="recovery && isDraft" class="notice" role="alert"><QuoteIcon name="history" :size="15" /><span>Unsaved work from this tab is available.</span><button type="button" class="btn sm" @click="useRecovery">Restore my work</button><button type="button" class="btn sm ghost" @click="useServer">Use saved draft</button></p>
      <p v-else-if="recovery && projection" class="notice" role="alert">
        <QuoteIcon name="history" :size="15" /><span>You have unsaved work from before version {{ nextVersion - 1 }} was issued. The issued version stays as it is.</span>
        <button v-if="canRevise" type="button" class="btn sm" :disabled="!!busy" @click="reviseWithWork">Revise as version {{ nextVersion }} with my work</button>
        <button v-else type="button" class="btn sm" @click="downloadWork">Download my work</button>
        <button type="button" class="btn sm ghost" @click="discardRecovery">Discard</button>
      </p>
      <p v-if="view?.review" class="notice warn" role="alert">
        <QuoteIcon name="alert" :size="15" />
        <span>{{ view.review.conflicts.length ? `${remoteName || 'Someone'} changed ${view.review.conflicts.length === 1 ? 'a place' : `${view.review.conflicts.length} places`} you changed too.` : `${remoteName || 'Someone'} saved changes that merge with yours.` }}</span>
        <button type="button" class="btn sm" @click="reviewOpen = true">{{ view.review.conflicts.length ? 'Review changes' : 'Review and merge' }}</button>
      </p>
      <p v-else-if="view?.remote === 'newer'" class="notice" role="status">
        <QuoteIcon name="refresh" :size="15" /><span>{{ remoteName || 'Someone' }} saved a newer draft.</span>
        <button type="button" class="btn sm" @click="review">{{ view.local === 'clean' ? 'Load it' : 'Review changes' }}</button>
      </p>
      <div v-if="view?.local === 'failed'" class="notice bad save-failure" role="alert">
        <QuoteIcon name="alert" :size="15" />
        <div class="notice-text">
          <p><strong>Not saved.</strong> {{ failureLead }}</p>
          <ul v-if="problems.length" class="problems" aria-label="What to fix">
            <li v-for="(problem, index) in problems" :key="index">
              <span class="where">{{ problem.where }}</span>
              <span class="what">{{ problem.message }}<button v-if="problem.target" type="button" class="show-btn" :aria-label="`Show ${problem.where}`" @click="showProblem(problem)">Show</button></span>
            </li>
          </ul>
          <p class="fine">{{ failure?.kind === 'invalid' ? 'Your changes are kept in this tab. Once it is fixed, the draft saves by itself.' : 'Your changes are kept in this tab.' }}</p>
        </div>
        <button v-if="!failure || failure.kind === 'server'" type="button" class="btn sm" @click="retry">Try again</button>
        <button v-else type="button" class="btn sm ghost" @click="live?.session.exportRecovery()">Download my work</button>
      </div>
      <p v-else-if="view?.local === 'offline'" class="notice warn" role="status">
        <QuoteIcon name="offline" :size="15" /><span>You are offline. Your changes are kept in this tab and save when the connection is back.</span>
        <button type="button" class="btn sm" @click="retry">Try now</button>
      </p>
    </div>
    <div class="workspace">
      <div ref="desk" class="quote-desk" :tabindex="editable ? undefined : 0" :role="editable ? undefined : 'region'" :aria-label="editable ? undefined : 'Quote document'">
        <div v-if="!document && !error" class="loading" role="status" aria-label="Loading quote"><span class="skeleton page-skeleton" /></div>
        <div v-if="document" class="desk-inner">
          <div ref="host" class="paper-stack" :style="{ zoom: percent / 100 }">
            <QuoteDocument
              :key="`${quoteId}:${viewing ?? (isDraft ? 'draft' : 'frozen')}`" ref="paper" :document="document" :offer-no="offerNo" :editable="editable" :editor="editor" :public-link="viewing === null && !isDraft ? publicUrl : ''" :draft-preview="viewing === null && isDraft"
              @update:document="edit" @mark="revealMark"
            />
          </div>
        </div>
      </div>
      <div v-if="document && pane" id="quote-side" class="quote-inspector-slot">
        <QuoteInspector
          v-if="pane === 'format' && paper && editor" :editor="paper.editor" :version="version" :offer-no="offerNo" :editable="editable" :admin="admin" :actions="paper.actions" :mode="sideMode"
          :mark-page="paper.markPage" :reveal="reveal" @close="floating = null" @jump="jump"
        />
        <aside v-else-if="pane === 'details'" class="details-pane" aria-label="Details">
          <header class="details-head">
            <h2 class="details-title">Details</h2>
            <button v-if="sideMode !== 'dock'" type="button" class="icon-btn sm flat" aria-label="Close details" data-tip="Close" @click="floating = null"><AppIcon name="close" :size="15" /></button>
          </header>
          <div class="details-body">
            <QuoteDetails
              :quote-id="quoteId" :offer-no="offerNo" :projection="projection" :frozen="frozen" :local="view?.local ?? 'loading'" :admin="admin" :staff="staff"
              :customer-name="customerName" :viewing="viewing" :busy="busy" @issue="issue" @revise="revise" @view="showVersion" @changed="detailsChanged"
            />
          </div>
        </aside>
      </div>
      <button v-if="document && pane && sideMode === 'sheet'" type="button" class="sheet-scrim" aria-label="Close the panel" tabindex="-1" @click="floating = null" />
    </div>
    <QuotePresenceOverlay v-if="document && identity.identity && isDraft && viewing === null" :root="host" :document="document" :revision="view?.baseRevision ?? 0" :principal-id="identity.identity.principal.id" :presence="presence" />
    <QuoteConflictDialog
      :open="reviewOpen && !!view?.review" :preview="view?.review ?? null" :durable-recovery="view?.durableRecovery ?? true" :document="view?.working ?? null" :actor-name="remoteName"
      @resolve="resolve" @export="live?.session.exportRecovery()" @discard="discard" @close="reviewOpen = false"
    />
  </section>
</template>

<style scoped>
.quote-ws { position: relative; display: flex; flex-direction: column; height: 100%; min-height: 0; outline: none; }
.quote-notices { display: grid; gap: 6px; padding: 8px 16px; }
.notice { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 10px; max-width: 900px; margin: 0 auto; width: 100%; padding: 8px 12px; border-radius: 10px; background: var(--surface-raised-2); box-shadow: inset 0 0 0 1px var(--line-2); font-size: 13px; color: var(--ink); }
.notice svg { flex-shrink: 0; color: var(--ink-3); }
.notice span { flex: 1; min-width: 12em; text-wrap: pretty; }
.notice.warn { background: var(--gold-wash); }
.notice.warn svg { color: var(--gold-ink); }
.notice.bad { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }
.notice.bad svg { color: var(--danger); }
/* A refused save: what to fix, each with the way to it. */
.save-failure { align-items: flex-start; }
.save-failure > svg { margin-top: 3px; }
.save-failure > .btn { align-self: center; }
.notice-text { display: grid; gap: 6px; flex: 1; min-width: 12em; }
.notice-text p { margin: 0; line-height: 1.5; }
.notice-text .fine { font-size: 12px; color: var(--ink-2); }
.problems { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
/* Each problem: where it is, then what is wrong with the way to it. */
.problems li { display: grid; line-height: 1.45; }
.problems .where { font-weight: 600; overflow-wrap: anywhere; }
.problems .what { color: var(--ink); }
.show-btn { min-height: 24px; margin-left: 6px; padding: 0 6px; border: 0; border-radius: 6px; background: transparent; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; cursor: pointer; }
@media (hover: hover) { .show-btn:hover { background: var(--row-hover); } }
.show-btn:focus-visible { box-shadow: var(--focus-ring); }
.workspace { position: relative; flex: 1; min-height: 0; display: grid; grid-template-columns: minmax(0, 1fr); }
.side-dock.side-open .workspace { grid-template-columns: minmax(0, 1fr) 320px; }
/* The desk: a quiet surface the paper sits on, scrolling in both directions when zoomed. */
.quote-desk { position: relative; min-width: 0; min-height: 0; overflow: auto; background: var(--surface-sunken); overscroll-behavior: contain; outline: none; }
.quote-desk:focus-visible { box-shadow: inset var(--focus-ring); }
.desk-inner { width: max-content; min-width: 100%; padding: 24px; }
.paper-stack { width: 210mm; margin: 0 auto; }
.loading { display: grid; place-items: center; padding: 40px; }
.page-skeleton { width: min(560px, 80%); height: 70vh; border-radius: 6px; }
.quote-inspector-slot { min-height: 0; border-left: 1px solid var(--line-2); background: var(--surface-raised-2); }
/* Narrower, or docked beside the list: the panel floats over the paper's right side. */
.side-overlay .quote-inspector-slot { position: absolute; top: 8px; right: 8px; bottom: 8px; z-index: 20; width: min(340px, calc(100% - 16px)); border: 0; border-radius: 14px; box-shadow: var(--shadow-pop), var(--shadow); overflow: hidden; }
/* Phones: a sheet from the bottom over the lower part of the page. */
.side-sheet .quote-inspector-slot { position: absolute; left: 0; right: 0; top: auto; bottom: 0; z-index: 20; width: auto; height: min(62%, 540px); border: 0; border-radius: 16px 16px 0 0; box-shadow: var(--shadow-pop), var(--shadow); overflow: hidden; }
.sheet-scrim { position: absolute; inset: 0; z-index: 19; border: 0; background: var(--scrim); }
.side-sheet .desk-inner, .compact .desk-inner { padding: 12px; }
.details-pane { display: flex; flex-direction: column; height: 100%; min-height: 0; background: var(--surface-raised-2); color: var(--ink); }
.details-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 44px; padding: 10px 16px 4px; }
.details-title { font: 600 14px/1.3 var(--font); color: var(--ink); }
.details-head .icon-btn { margin-right: -6px; }
.details-body { flex: 1; min-height: 0; overflow: auto; padding: 8px 16px 20px; overscroll-behavior: contain; }
</style>

<style>
/* PDF: only the paper prints, at full size, whatever the zoom and theme. */
@media print {
  .app-shell { display: block !important; height: auto !important; }
  .app-header, .app-footer, .skip-link, .toast-host, .quote-titlebar, .quote-notices, .quote-inspector-slot, .sheet-scrim, .quotes-list-page, .quote-panel-splitter { display: none !important; }
  main, .quote-desk { overflow: visible !important; background: none !important; }
  .desk-inner { padding: 0 !important; }
  .paper-stack { zoom: 1 !important; }
  .quote-ws, .workspace, .quote-dock { display: block !important; position: static !important; height: auto !important; width: auto !important; box-shadow: none !important; border: 0 !important; }
}
</style>
