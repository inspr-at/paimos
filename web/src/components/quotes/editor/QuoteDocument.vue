<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
import type { QuoteEditor as Editor } from '../../../lib/quotes/editor'
// A lent editor is wrapped again by each view that mounts it; every view wraps
// the editor's own select, never the previous view's wrapper.
const ownSelect = new WeakMap<Editor, Editor['select']>()
// The document a lent editor last agreed with (as JSON), so a view that borrows it
// later replaces its document only for a real change, never for the editor's own
// normalisation, and so keeps its undo history.
const synced = new WeakMap<Editor, string>()
function unwrappedSelect(editor: Editor): Editor['select'] {
  let base = ownSelect.get(editor)
  if (!base) { base = editor.select.bind(editor); ownSelect.set(editor, base) }
  return base
}
</script>
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, toRaw, watch } from 'vue'
import QuoteAcceptance from './QuoteAcceptance.vue'
import QuoteCover from './QuoteCover.vue'
import QuotePositions from './QuotePositions.vue'
import QuoteProse from './QuoteProse.vue'
import QuoteText from './QuoteText.vue'
import SectionMenu from '../inspector/SectionMenu.vue'
import QuoteIcon from '../inspector/QuoteIcon.vue'
import { QuoteEditor } from '../../../lib/quotes/editor'
import { collectTextNodes, pointInTextNodes } from '../../../lib/quotes/caret'
import { sectionLabel } from '../../../lib/quotes/inspector'
import { sectionActions } from '../../../lib/quotes/sectionActions'
import { fitClassicBlocks, fitWholeBlocks, type PaginationResult, type PagePlan } from '../../../lib/quotes/layout'
import { MARK_DEFAULT_MM } from '../../../lib/quotes/inspector'
import { contentUrl } from '../../../lib/attachments'
import { quoteQr } from '../../../lib/quotes/qr'
import { loadProfileFonts, normalizeProfile, pageNumber, profileAssetUrl, profileDate, profileStyle } from '../../../lib/quotes/profile'
import type { QuoteDocumentData, DocumentSettings, ListMode, MarkName, NumberingOptions, OffsetPatch, QuoteMarker, QuoteSection, SectionNumberingStyle, SectionSettingsPatch, TextSelection } from '../../../lib/quotes/types'
const props = withDefaults(defineProps<{ document: QuoteDocumentData; offerNo?: string; editable?: boolean; accepted?: { name: string; company?: string; at: string; digest: string } | null; editor?: QuoteEditor | null; publicLink?: string; draftPreview?: boolean }>(), { editable: false, offerNo: '', editor: null, publicLink: '', draftPreview: false })
const emit = defineEmits<{ 'update:document': [document: QuoteDocumentData]; change: [document: QuoteDocumentData]; 'render-state': [state: PaginationResult]; overflow: [message: string | null]; mark: [page: number] }>()
// P7: a workspace may lend its editor (and so its undo history) to this view, so
// moving the quote between the docked panel and the full page keeps both.
const editor = props.editor ?? new QuoteEditor(props.document)
if (props.editor) {
  // The view that lent it before must not hear this one's changes.
  editor.onChange = undefined
  const incoming = JSON.stringify(props.document), known = synced.get(editor)
  if (known === undefined) synced.set(editor, incoming)
  else if (known !== incoming && incoming !== JSON.stringify(editor.document)) { editor.replaceDocument(props.document); synced.set(editor, incoming) }
}
const state = shallowRef(editor.document)
// P4: the inspector and title bar follow every change and selection through this counter.
const version = ref(0)
const touch = () => { version.value++ }
let lastEmitted: QuoteDocumentData | null = null
editor.onChange = document => { state.value = document; lastEmitted = document; if (props.editor) synced.set(editor, JSON.stringify(document)); touch(); emit('update:document', document); emit('change', document); schedule() }
const baseSelect = unwrappedSelect(editor)
editor.select = (selection, preserveTyping) => { baseSelect(selection, preserveTyping); touch() }
// The session hands every edit back as a copy; only a different document (a reload,
// a restored draft, a merge) replaces the editor's, or each edit would echo forever.
watch(() => props.document, document => {
  if (toRaw(document) === lastEmitted || JSON.stringify(document) === JSON.stringify(editor.document)) return
  editor.replaceDocument(document); lastEmitted = editor.document; if (props.editor) synced.set(editor, JSON.stringify(document)); schedule()
})
const pages = ref<PagePlan[]>([{ kind: 'cover', sectionIds: [], positionIds: [], acceptance: false }, { kind: 'positions', sectionIds: [], positionIds: [], acceptance: true }])
const renderState = ref<PaginationResult>({ ready: false, overflow: null, pages: pages.value })
const measureRoot = ref<HTMLElement>()
const heightProbe = ref<HTMLElement>()
let resize: ResizeObserver | null = null
let scheduled = false
let generation = 0
const sections = computed(() => state.value.sections)
const profile = computed(() => normalizeProfile(state.value.profile))
const classic = computed(() => profile.value?.definition.layout_variant === 'classic-v1')
const paperStyle = computed(() => profileStyle(profile.value))
const footerNumber = (page: number) => pageNumber(profile.value, page, pages.value.length)
const groupLabel = (kind: 'terms' | 'positions') => kind === 'terms'
  ? (profile.value?.definition.labels.terms || 'Bedingungen')
  : (profile.value?.definition.labels.positions || 'Leistungen')
// The running header's date prints like the cover's, in the profile's locale.
const headerDate = computed(() => profileDate(profile.value, state.value.offer_date))
let fontsReady: Promise<void> = Promise.resolve()
watch(profile, value => { fontsReady = loadProfileFonts(value); schedule() }, { immediate: true })
const publicQr = computed(() => props.publicLink ? quoteQr(props.publicLink) : null)
const previewQr = computed(() => props.draftPreview && !props.publicLink ? quoteQr('DRAFT PREVIEW - NO CUSTOMER LINK') : null)
const finalQr = computed(() => publicQr.value ?? previewQr.value)
const byId = (id: string) => sections.value.find(s => s.id === id)!
// Pages are planned after measuring; until then a deleted section may still be listed.
const exists = (id: string) => sections.value.some(s => s.id === id)
function sectionIndex(id: string) { return sections.value.findIndex(s => s.id === id) }
async function measure() {
  const current = ++generation
  await nextTick()
  const root = measureRoot.value, probe = heightProbe.value
  if (!root || !probe) return
  try { await fontsReady; await document.fonts.ready } catch { renderState.value = { ready: false, overflow: 'Document profile font could not be loaded', pages: pages.value }; emit('overflow', renderState.value.overflow); return }
  if (current !== generation) return
  const rect = (selector: string) => root.querySelector<HTMLElement>(selector)?.getBoundingClientRect().height ?? 0
  const available = probe.getBoundingClientRect().height - (classic.value ? 12 : 0)
  if (available <= 0) return
  const cover = rect('[data-measure-cover]')
  const sectionHeights = state.value.sections.map(section => ({ id: section.id, px: rect(`[data-measure-section="${section.id}"]`) }))
  const positionHeights = state.value.positions.map(position => ({ id: position.id, px: rect(`[data-measure-position="${position.id}"] tbody`) }))
  const breaks = new Set(state.value.sections.filter(section => section.page_break_before).map(section => section.id))
  const next = classic.value
    ? fitClassicBlocks(cover, sectionHeights, positionHeights, rect('[data-measure-acceptance]'), available, {
        terms: rect('[data-measure-terms-heading]'), positions: rect('[data-measure-positions-heading]'),
        table: rect('.quote-positions thead') + 12, continuation: rect('[data-measure-continuation]'),
      })
    : fitWholeBlocks(cover, sectionHeights, positionHeights, rect('[data-measure-acceptance]'), available, undefined, breaks)
  pages.value = next.pages
  renderState.value = next
  emit('render-state', next)
  emit('overflow', next.overflow)
}
function schedule() {
  renderState.value = { ...renderState.value, ready: false, overflow: null }
  if (scheduled) return
  scheduled = true
  void nextTick().then(() => { scheduled = false; void measure() })
}
watch(state, schedule)
onMounted(() => { resize = new ResizeObserver(schedule); if (measureRoot.value) resize.observe(measureRoot.value); window.addEventListener('resize', schedule); schedule() })
onBeforeUnmount(() => { resize?.disconnect(); window.removeEventListener('resize', schedule) })
// ---------- The footer mark (P4, F08.03): the company's mark on every page ----------
// Sized 18..96 mm and moved -6..10 mm, in tenths, for the whole document; clicking
// the mark on any page selects that page's mark and asks for its settings.
const markFile = computed(() => profile.value?.definition.footer.asset_id || state.value.layout.logo_file_id || state.value.sender.logo_file_id || '')
const markSrc = computed(() => profile.value?.definition.footer.asset_id ? profileAssetUrl(markFile.value) : contentUrl(markFile.value, 'original'))
const markWidth = computed(() => { const n = Number(state.value.layout.logo_width_mm); return Number.isFinite(n) && n >= 18 && n <= 96 ? n : MARK_DEFAULT_MM })
const markOffset = computed(() => { const n = Number(state.value.layout.logo_offset_mm); return Number.isFinite(n) && n >= -6 && n <= 10 ? n : 0 })
const markPage = ref<number | null>(null)
const markMissing = ref(false)
watch(markFile, () => { markMissing.value = false })
function selectMark(pageIndex: number) { markPage.value = pageIndex; editor.select({}); touch(); emit('mark', pageIndex) }

// ---------- Section chrome (P4): handle, context menu, drag to reorder, Alt+Up/Down ----------
const actions = sectionActions(editor, touch)
const currentSection = computed(() => { void version.value; return editor.selection.text?.sectionId ?? editor.selection.sectionId ?? null })
const labelOf = (section: QuoteSection) => classic.value ? String(sectionIndex(section.id) + 1) : sectionLabel(sectionIndex(section.id) + 1, section.numbering_style ?? profile.value?.definition.sections.numbering as SectionNumberingStyle | undefined)
const spacing = (section: QuoteSection) => ({ paddingTop: section.spacing_before_mm ? `${section.spacing_before_mm}mm` : undefined, paddingBottom: section.spacing_after_mm ? `${section.spacing_after_mm}mm` : undefined })
const menu = ref<{ id: string; anchor: HTMLElement } | null>(null)
const dragId = ref<string | null>(null)
const dropTarget = ref<{ id: string; before: boolean } | null>(null)
function openMenu(id: string, anchor: HTMLElement) { if (props.editable) menu.value = { id, anchor } }
// A right click on a heading opens the actions where the pointer is.
function headingMenu(event: MouseEvent, id: string) {
  if (!props.editable) return
  event.preventDefault()
  const at = new DOMRect(event.clientX, event.clientY, 0, 0)
  menu.value = { id, anchor: { getBoundingClientRect: () => at, contains: () => false, focus: () => {} } as unknown as HTMLElement }
}
function closeMenu(restore: boolean) {
  const m = menu.value
  menu.value = null
  if (restore && m && m.anchor instanceof HTMLElement) m.anchor.focus()
}
function moveSection(id: string, direction: number) {
  const active = document.activeElement as HTMLElement | null
  const inProse = !!active?.closest('.quote-prose')
  if (actions.move(id, direction > 0 ? 1 : -1)) void nextTick(() => refocus(id, inProse))
}
// A moved section is rendered anew; focus and the text selection come back to it.
function refocus(id: string, prose: boolean) {
  const root = document.querySelector<HTMLElement>(`.quote-page [data-section-id="${id}"]`)
  if (!root) return
  const target = root.querySelector<HTMLElement>(prose ? '.quote-prose' : '.quote-section-heading [contenteditable]') ?? root.querySelector<HTMLElement>('.quote-section-handle')
  target?.focus({ preventScroll: true })
  target?.scrollIntoView({ block: 'nearest' })
  if (prose && editor.selection.text?.sectionId === id) placeSelection(editor.selection.text)
}
function placeSelection(selection: TextSelection) {
  const root = document.querySelector<HTMLElement>(`.quote-page [data-section-id="${selection.sectionId}"] .quote-prose`)
  const point = (nodeId: string, offset: number) => {
    const el = root?.querySelector<HTMLElement>(`[data-text-id="${nodeId}"]`)
    return el ? pointInTextNodes(collectTextNodes(el), offset) ?? { node: el, offset: 0 } : null
  }
  const a = point(selection.anchor.nodeId, selection.anchor.offset), f = point(selection.focus.nodeId, selection.focus.offset)
  if (a && f) window.getSelection()?.setBaseAndExtent(a.node, a.offset, f.node, f.offset)
}
function sectionKeys(event: KeyboardEvent, id: string) {
  // Prose moves its section itself; headings and the handle do it here.
  if (event.defaultPrevented || !props.editable || !event.altKey || (event.key !== 'ArrowUp' && event.key !== 'ArrowDown')) return
  event.preventDefault()
  moveSection(id, event.key === 'ArrowUp' ? -1 : 1)
}
function dragStart(event: DragEvent, id: string) {
  dragId.value = id; menu.value = null
  event.dataTransfer?.setData('text/plain', id)
  if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move'
}
function dragOver(event: DragEvent, id: string) {
  if (!dragId.value) return
  event.preventDefault()
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
  dropTarget.value = { id, before: event.clientY < rect.top + rect.height / 2 }
}
function dropSection() {
  const from = dragId.value, target = dropTarget.value
  dragId.value = null; dropTarget.value = null
  if (!from || !target || target.id === from) return
  const origin = sectionIndex(from)
  let to = sectionIndex(target.id) + (target.before ? 0 : 1)
  if (origin < to) to--
  actions.moveTo(from, to)
}
function jump(id: string) {
  editor.select({ sectionId: id })
  void nextTick(() => {
    const root = document.querySelector<HTMLElement>(`.quote-page [data-section-id="${id}"]`)
    root?.scrollIntoView({ block: 'start', behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
    root?.querySelector<HTMLElement>('.quote-section-heading [contenteditable]')?.focus({ preventScroll: true })
  })
}
function whenReady(): Promise<PaginationResult> {
  return new Promise(resolve => {
    const poll = () => { if (renderState.value.ready || renderState.value.overflow) resolve(renderState.value); else requestAnimationFrame(poll) }
    poll()
  })
}
// Typed P4/P5 integration surface; this component owns all document mutation and history.
defineExpose({
  editor, renderState, whenReady, version, touch, actions, jump, markPage, select: editor.select.bind(editor), undo: editor.undo.bind(editor), redo: editor.redo.bind(editor),
  setMarks: (mark: MarkName | 'normal', active?: boolean) => editor.setMarks(mark, active),
  setListMode: (mode: ListMode, bullet?: Exclude<QuoteMarker, 'decimal'>) => editor.setListMode(mode, bullet),
  indent: () => editor.indent(), outdent: () => editor.outdent(),
  setNumbering: (options: NumberingOptions) => editor.setNumbering(options),
  setOffsets: (patch: OffsetPatch) => editor.setOffsets(patch),
  insertSection: (afterId?: string) => editor.insertSection(afterId),
  moveSection: (id: string, targetIndex: number) => editor.moveSection(id, targetIndex),
  deleteSection: (id: string) => editor.deleteSection(id),
  setSectionSettings: (id: string, patch: SectionSettingsPatch) => editor.setSectionSettings(id, patch),
  setDocumentSettings: (patch: DocumentSettings) => editor.setDocumentSettings(patch),
})
</script>
<template>
  <div class="quote-document" :class="[profile?.definition.layout_variant, { 'has-brand-dots': !!profile?.definition.footer.dots_asset_id }]" :style="paperStyle" :data-quote-ready="renderState.ready" :data-quote-overflow="renderState.overflow ?? undefined" :data-page-count="pages.length">
    <div v-if="renderState.overflow" class="quote-overflow" role="alert">{{ renderState.overflow }}</div>
    <article v-for="(page, pageIndex) in pages" :key="pageIndex" class="quote-page" :data-page="pageIndex + 1">
      <header v-if="classic" class="quote-page-header"><span>{{ profile?.definition.labels.quote || 'ANGEBOT' }} {{ offerNo }}</span><span>{{ headerDate }}</span></header>
      <div class="quote-page-content">
        <div v-if="classic && pageIndex > 0 && !page.heading" class="quote-continuation"></div>
        <QuoteCover v-if="page.kind === 'cover'" :document="state" :editor="editor" :offer-no="offerNo" :editable="editable" />
        <h2 v-if="classic && page.heading" class="quote-group-heading"><span>{{ page.heading === 'terms' ? 'I.' : sections.length ? 'II.' : 'I.' }} {{ groupLabel(page.heading) }}</span><img v-if="profile?.definition.cover.brand_asset_id" class="quote-heading-dots" :src="profileAssetUrl(profile.definition.cover.brand_asset_id)" alt="" /></h2>
        <div
          v-for="id in page.sectionIds.filter(exists)" :key="id" class="quote-section" :data-section-id="id" :style="spacing(byId(id))"
          :class="{ current: editable && currentSection === id, lifted: dragId === id, 'drop-before': dropTarget?.id === id && dropTarget.before, 'drop-after': dropTarget?.id === id && !dropTarget.before }"
          @dragover="dragOver($event, id)" @drop.prevent="dropSection" @keydown="sectionKeys($event, id)"
        >
          <button
            v-if="editable" type="button" class="quote-section-handle" draggable="true" :aria-label="`Section ${sectionIndex(id) + 1} actions`" aria-haspopup="menu" :aria-expanded="menu?.id === id"
            aria-keyshortcuts="Alt+ArrowUp Alt+ArrowDown" data-tip="Drag to move · click for actions" @dragstart="dragStart($event, id)" @dragend="dragId = null; dropTarget = null" @click="openMenu(id, $event.currentTarget as HTMLElement)"
          ><QuoteIcon name="grip" :size="14" /></button>
          <div class="quote-section-heading" @contextmenu="headingMenu($event, id)"><span v-if="labelOf(byId(id))" class="quote-section-number">{{ labelOf(byId(id)) }}</span><QuoteText tag="h2" :model-value="byId(id).heading" :label="`Heading section ${sectionIndex(id) + 1}`" :editable="editable" @focus="editor.select({ sectionId: id })" @update:model-value="editor.editSection(id, { heading: $event })" /></div>
          <QuoteProse :editor="editor" :section-id="id" :body="byId(id).body" :nodes="byId(id).nodes" :section-number="sectionIndex(id) + 1" :editable="editable" @move-section="moveSection(id, $event)" />
        </div>
        <QuotePositions v-if="page.kind === 'positions' && (page.positionIds.length || pageIndex === pages.findIndex(p => p.kind === 'positions'))" :positions="state.positions" :indices="page.positionIds" :editor="editor" :editable="editable" :profile="profile" :continuation="pageIndex > pages.findIndex(p => p.kind === 'positions')" />
        <QuoteAcceptance v-if="page.acceptance" :document="state" :editor="editor" :editable="editable" :accepted="accepted" />
      </div>
      <footer class="quote-page-footer">
        <span class="quote-footer-start">
          <span v-if="profile?.definition.layout_variant === 'classic-v1'">{{ offerNo }}</span>
          <img v-if="profile?.definition.footer.dots_asset_id" class="quote-footer-dots" :src="profileAssetUrl(profile.definition.footer.dots_asset_id)" alt="" />
          <button
            v-if="markFile && editable" type="button" class="quote-mark" :class="{ selected: markPage === pageIndex, missing: markMissing }" :style="{ width: profile ? `${profile.definition.footer.width_mm}mm` : `${markWidth}mm`, transform: profile ? `translateY(${profile.definition.footer.offset_mm}mm)` : `translateY(${markOffset}mm)` }"
            :aria-label="`Company mark on page ${pageIndex + 1}`" :aria-pressed="markPage === pageIndex" data-tip="Size and position of the mark" @click="selectMark(pageIndex)"
          ><img v-if="!markMissing" :src="markSrc" alt="" draggable="false" @error="markMissing = true" /><span v-else class="quote-mark-empty">Mark</span></button>
          <span v-else-if="markFile && !markMissing" class="quote-mark" :style="{ width: profile ? `${profile.definition.footer.width_mm}mm` : `${markWidth}mm`, transform: profile ? `translateY(${profile.definition.footer.offset_mm}mm)` : `translateY(${markOffset}mm)` }"><img :src="markSrc" alt="" @error="markMissing = true" /></span>
          <span v-if="profile?.definition.layout_variant !== 'classic-v1'">{{ state.sender.company }}</span>
        </span>
        <span class="quote-footer-end">
          <span v-if="finalQr && pageIndex === pages.length - 1" class="quote-qr-wrap">
            <svg class="quote-final-qr" :viewBox="`-2 -2 ${finalQr.size + 4} ${finalQr.size + 4}`" role="img" :aria-label="publicQr ? 'QR code of the customer link' : 'Draft preview QR code; no customer link'" shape-rendering="crispEdges"><rect x="-2" y="-2" :width="finalQr.size + 4" :height="finalQr.size + 4" fill="#fff" /><path :d="finalQr.path" fill="#000" /></svg>
            <span v-if="previewQr" class="quote-qr-note">Draft preview</span>
          </span>
          <span>{{ profile ? footerNumber(pageIndex + 1) : `${offerNo} · ${pageIndex + 1} / ${pages.length}` }}</span>
        </span>
      </footer>
    </article>
    <div ref="measureRoot" class="quote-measure" aria-hidden="true" inert>
      <div ref="heightProbe" class="quote-height-probe"></div>
      <div data-measure-cover><QuoteCover :document="state" :editor="editor" :offer-no="offerNo" /></div>
      <div data-measure-continuation class="quote-continuation"></div>
      <h2 data-measure-terms-heading class="quote-group-heading">I. {{ groupLabel('terms') }}</h2>
      <h2 data-measure-positions-heading class="quote-group-heading">II. {{ groupLabel('positions') }}</h2>
      <div v-for="(section, index) in sections" :key="section.id" :data-measure-section="section.id" class="quote-section" :style="spacing(section)"><div class="quote-section-heading"><span v-if="labelOf(section)" class="quote-section-number">{{ labelOf(section) }}</span><h2>{{ section.heading }}</h2></div><QuoteProse :editor="editor" :section-id="section.id" :body="section.body" :nodes="section.nodes" :section-number="index + 1" /></div>
      <div v-for="position in state.positions" :key="position.id" :data-measure-position="position.id"><QuotePositions :positions="state.positions" :indices="[position.id]" :editor="editor" :profile="profile" /></div>
      <div data-measure-acceptance><QuoteAcceptance :document="state" :editor="editor" :accepted="accepted" /></div>
    </div>
    <SectionMenu
      v-if="menu" :anchor="menu.anchor" :index="sectionIndex(menu.id)" :count="sections.length" :label="`Actions for section ${sectionIndex(menu.id) + 1}`"
      @close="closeMenu" @add="actions.addBelow(menu!.id)" @up="moveSection(menu!.id, -1)" @down="moveSection(menu!.id, 1)" @remove="actions.remove(menu!.id)"
    />
  </div>
</template>
<style>
/* Paper is a light print surface in either app theme: the document scope pins the
   light tokens its parts use, so dark mode never turns the page dark. */
.quote-document {
  --ink: #203c3d; --ink-2: #4c6163; --ink-3: #5f7274; --teal: #0e6f6c; --teal-ink: #0b5c59;
  --line: rgba(32, 60, 61, .1); --line-2: rgba(32, 60, 61, .18); --surface: #fffefa; --surface-2: rgba(32, 60, 61, .045); --surface-raised: #fffefa;
  --field-bg: rgba(255, 255, 255, .66); --row-hover: rgba(14, 111, 108, .055); --row-selected: rgba(164, 229, 223, .3);
  --danger: #b24a44; --danger-bg: rgba(178, 74, 68, .08); --danger-line: rgba(178, 74, 68, .3); --aqua: #a4e5df; --focus-ring: 0 0 0 2px #a4e5df, 0 0 18px rgba(164, 229, 223, .8);
  --quote-paper: #fffefa; --quote-ink: var(--ink); color-scheme: light; color: var(--quote-ink); font-family: var(--font); font-size: 9pt;
}
.quote-document[class] { font-family: var(--quote-body-font, var(--font)); }
.quote-document.classic-v1 { font-size: var(--quote-body-size); line-height: 1.5; font-variant-numeric: tabular-nums; }
.quote-document.classic-v1 .quote-page { padding: var(--quote-top) var(--quote-right) var(--quote-bottom) var(--quote-left); color: var(--ink); }
.quote-document.classic-v1 .quote-page-content { height: var(--quote-content-height); }
.quote-document.classic-v1 .quote-continuation { height: 6mm; }
.quote-document.classic-v1 .quote-group-heading { display: flex; align-items: center; justify-content: space-between; margin: 0; padding: 6mm 0 4.2mm; font-family: var(--quote-display-font, var(--quote-body-font, var(--font))); font-size: var(--quote-section-size); font-weight: 400; letter-spacing: .14em; line-height: 1.2; text-transform: uppercase; color: var(--teal); }
.quote-document.classic-v1 .quote-heading-dots { display: block; width: 11.25mm; height: 2.5mm; }
.quote-document.classic-v1 .quote-page-header { display: flex; justify-content: space-between; gap: 8mm; border-bottom: 1px solid var(--line); padding-bottom: 5px; color: var(--ink-3); font-size: 7.5pt; font-weight: 600; letter-spacing: .14em; text-transform: uppercase; }
.quote-document.classic-v1 .quote-page-footer { left: var(--quote-left); right: var(--quote-right); bottom: var(--quote-bottom); display: grid; grid-template-columns: 1fr auto 1fr; padding-top: 7px; font-size: var(--quote-footer-size); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); border-color: var(--line); }
.quote-document.classic-v1 .quote-footer-start { gap: 0; }
.quote-document.classic-v1 .quote-footer-start > .quote-mark { position: absolute; left: 50%; transform: translateX(-50%) translateY(var(--quote-footer-offset)) !important; }
.quote-document.classic-v1 .quote-footer-dots { position: absolute; left: calc(50% - 16.5mm); width: 7.2mm; height: 1.6mm; }
.quote-document.classic-v1.has-brand-dots .quote-footer-start > .quote-mark { left: calc(50% + 5.4mm); width: 25mm !important; }
.quote-document.classic-v1 .quote-footer-end { grid-column: 3; justify-content: flex-end; }
.quote-document.classic-v1 .quote-section { display: grid; grid-template-columns: 9mm minmax(0, 1fr); column-gap: 2mm; align-items: baseline; margin-top: 0; }
.quote-document.classic-v1 .quote-section + .quote-section { margin-top: 4.2mm; }
.quote-document.classic-v1 .quote-section-heading { display: contents; }
.quote-document.classic-v1 .quote-section-number { grid-column: 1; color: var(--teal); font-size: 10pt; font-weight: 700; }
.quote-document.classic-v1 .quote-section-heading h2 { grid-column: 2; margin: 0; color: var(--teal); font-size: 10.5pt; font-weight: 700; }
.quote-document.classic-v1 .quote-section .quote-prose { grid-column: 2; margin-top: 1px; color: #333c3c; font-size: 9.6pt; line-height: 1.5; white-space: pre-line; }
.quote-document.classic-v1 .quote-section .quote-prose-row { line-height: 1.5; min-height: 0; }
.quote-document.classic-v1 .quote-section .quote-marker { color: var(--teal); font-weight: 700; }
.quote-document.classic-v1 .quote-measure { padding: var(--quote-top) var(--quote-right) var(--quote-bottom) var(--quote-left); }
.quote-document.classic-v1 .quote-height-probe { height: var(--quote-content-height); }
.quote-page { position: relative; box-sizing: border-box; width: 210mm; height: 297mm; padding: 20mm 21mm 20mm; margin: 0 auto 12mm; background: var(--quote-paper); box-shadow: var(--shadow); overflow: hidden; }
.quote-page-content { height: 245mm; }
.quote-page-footer { position: absolute; bottom: 12mm; left: 21mm; right: 21mm; border-top: 1px solid var(--line-2); padding-top: 3mm; display: flex; justify-content: space-between; align-items: center; gap: 10mm; font-size: 7pt; color: var(--ink-2); }
.quote-footer-end { display: inline-flex; align-items: center; gap: 4mm; flex-shrink: 0; }
.quote-qr-wrap { display: grid; justify-items: center; gap: 1mm; }
.quote-qr-note { font: 600 6pt/1 var(--font); text-transform: uppercase; white-space: nowrap; }
.quote-final-qr { display: block; width: 18mm; height: 18mm; flex-shrink: 0; }
.quote-footer-start { display: flex; align-items: center; gap: 4mm; min-width: 0; }
.quote-mark { display: block; flex-shrink: 0; padding: 0; border: 0; border-radius: 2px; background: transparent; line-height: 0; }
.quote-mark img { display: block; width: 100%; height: auto; max-height: 14mm; object-fit: contain; object-position: left center; }
button.quote-mark { cursor: pointer; }
button.quote-mark:hover { box-shadow: 0 0 0 1px var(--line-2); }
button.quote-mark:focus-visible { box-shadow: var(--focus-ring); }
button.quote-mark.selected { box-shadow: 0 0 0 1.5px var(--teal); }
.quote-mark-empty { display: grid; place-items: center; height: 7mm; border-radius: 2px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-3); font-size: 6.5pt; line-height: 1; }
.quote-section { position: relative; margin-top: 6mm; break-inside: avoid; }.quote-section-heading { display: flex; gap: 2mm; align-items: baseline; margin-bottom: 3mm; font-size: 13pt; font-weight: 700; }.quote-section-heading h2 { font: inherit; margin: 0; flex: 1; }.quote-section-number { color: var(--ink-2); }
.quote-section-handle { position: absolute; left: -9mm; top: 0; display: grid; place-items: center; width: 6.5mm; height: 7mm; padding: 0; border: 0; border-radius: 6px; background: transparent; color: var(--ink-3); cursor: grab; opacity: 0; transition: opacity .12s ease; }
.quote-section:hover > .quote-section-handle, .quote-section.current > .quote-section-handle, .quote-section-handle:focus-visible, .quote-section-handle[aria-expanded="true"] { opacity: 1; }
@media (hover: none) { .quote-section-handle { opacity: 1; } }
.quote-section-handle:hover { color: var(--ink); background: var(--row-hover); }
.quote-section-handle:focus-visible { box-shadow: var(--focus-ring); }
.quote-section.lifted { opacity: .4; }
/* Where a dragged section lands: a line between sections, its own element, not a border. */
.quote-section.drop-before::before, .quote-section.drop-after::after { content: ''; position: absolute; left: 0; right: 0; height: 2px; border-radius: 2px; background: var(--teal); pointer-events: none; }
.quote-section.drop-before::before { top: -3mm; }
.quote-section.drop-after::after { bottom: -3mm; }
/* The render warning is app chrome, not document text: the app's font and the danger ink,
   so it stays readable on the dark theme's danger tint (AEON-140). */
.quote-overflow { max-width: 210mm; margin: 0 auto 4mm; background: var(--danger-bg); border: 1px solid var(--danger-line); border-radius: 8px; padding: 3mm; color: var(--danger); font: 500 13px/1.4 var(--font); }
.quote-measure { position: absolute; left: -10000px; top: 0; width: 210mm; padding: 20mm 21mm; box-sizing: border-box; visibility: hidden; background: var(--quote-paper); }.quote-height-probe { height: 245mm; position: absolute; pointer-events: none; }
@media print { .quote-page { margin: 0; box-shadow: none; break-after: page; background: var(--quote-paper); color: var(--ink); } .quote-document { color: var(--ink); } .quote-section-handle,.quote-overflow,.quote-measure,.quote-mark.missing { display: none !important; } button.quote-mark { box-shadow: none !important; } @page { size: A4; margin: 0; } }
</style>
