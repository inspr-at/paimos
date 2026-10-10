<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { LocalState } from '../../lib/quoteSession'
import type { PresenceSnapshot } from '../../lib/quotePresence'
import { STATUS_META, type QuoteStatus } from '../../lib/quotes/list'
import { stepZoom, type ZoomMode } from '../../lib/quotes/zoom'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import QuotePeople from './collaboration/QuotePeople.vue'
import QuoteIcon from './inspector/QuoteIcon.vue'
import QuoteStatusIcon from './QuoteStatusIcon.vue'
import ZoomControl from './ZoomControl.vue'

// The quote's title bar: the way back (or, docked beside the list, the way out),
// the number, status and save state on the left, zoom in the centre, then who
// else is here, Undo and Redo, PDF with the header chevron right beside it, the
// quote's other actions, Details and the one Format panel toggle. Docked, it
// also opens the quote on its own page; on its own page it can go back beside
// the list with the same session.
const props = defineProps<{
  offerNo: string; status: QuoteStatus; archived: boolean; revising: boolean; local: LocalState | 'loading'; canSave: boolean; saveHint?: string; canUndo: boolean; canRedo: boolean
  zoom: ZoomMode; percent: number; pane: 'format' | 'details' | null; canFormat: boolean; headerCollapsed: boolean; printing: boolean; compact: boolean
  layout: 'full' | 'dock'; presence: PresenceSnapshot | null; principalId: string; admin: boolean; staff: boolean
}>()
const emit = defineEmits<{
  save: []; undo: []; redo: []; zoom: [mode: ZoomMode]; print: []; toggleHeader: []; pane: [pane: 'format' | 'details']
  close: []; expand: []; collapse: []; duplicate: []; archive: []; copyNumber: []; issue: []
}>()
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
// Tooltips spell keys as words, as everywhere else ("Close · Esc").
const mod = mac ? 'Cmd ' : 'Ctrl '
const stateLabel = computed(() => props.revising ? 'Revising' : STATUS_META[props.status].label)
// The save state is a status, never a button: what is true of the draft now. The
// Save button beside it is the one action, enabled only when there is something to
// save that saving can land (the notice below the bar says why when it cannot).
const saveText = computed(() => ({
  loading: 'Opening…', clean: 'Saved', dirty: 'Unsaved changes', saving: 'Saving…', offline: 'Offline, kept here', failed: 'Not saved', conflict: 'Changed elsewhere', 'read-only': 'Read only',
} as Record<string, string>)[props.local] ?? '')
const stateIcon = computed(() => props.local === 'failed' || props.local === 'conflict' ? 'alert' : props.local === 'offline' ? 'offline' : 'check-circle')
const tone = computed(() => props.local === 'failed' ? 'bad' : props.local === 'dirty' || props.local === 'offline' || props.local === 'conflict' ? 'warn' : props.local === 'clean' ? 'ok' : '')
const editing = computed(() => props.status === 'draft' && props.local !== 'read-only')
const menu = ref<HTMLElement | null>(null)
const more = ref<HTMLButtonElement>()
function toggleMenu(event: MouseEvent) { menu.value = menu.value ? null : event.currentTarget as HTMLElement }
function closeMenu(restore: boolean) { menu.value = null; if (restore) more.value?.focus() }
function act(action: 'duplicate' | 'archive' | 'copyNumber' | 'issue' | 'expand' | 'collapse') {
  menu.value = null
  if (action === 'duplicate') emit('duplicate'); else if (action === 'archive') emit('archive'); else if (action === 'copyNumber') emit('copyNumber')
  else if (action === 'issue') emit('issue'); else if (action === 'expand') emit('expand'); else emit('collapse')
}

// ---------- Folding (U20): nothing overlaps, however narrow the bar ----------
// The bar measures itself; while any group spills out of its box, it folds one
// more step: 1 labels (PDF, Details) and the profile's name go, 2 the zoom moves
// into the … menu, 3 so do Undo and Redo, 4 the save state keeps only its icon.
// (Save's caption goes with the labels at 1.)
const bar = ref<HTMLElement>()
const fold = ref(0)
const FOLDS = 4
// Two rows (phones, the docked panel) fold Undo and Redo into the menu before the
// zoom: on a small screen fitting the page matters more than a second undo button.
const historyFolded = computed(() => fold.value >= (props.compact ? 2 : 3))
const zoomFolded = computed(() => fold.value >= (props.compact ? 3 : 2))
function spilling(): boolean {
  const el = bar.value
  if (!el) return false
  const box = el.getBoundingClientRect()
  if (el.scrollWidth > el.clientWidth + 1) return true
  for (const group of el.querySelectorAll<HTMLElement>(':scope > .left, :scope > .center, :scope > .right')) {
    const g = group.getBoundingClientRect()
    for (const child of group.querySelectorAll<HTMLElement>(':scope > *')) {
      const r = child.getBoundingClientRect()
      if (r.width === 0) continue
      if (r.left < Math.max(g.left, box.left) - 1 || r.right > Math.min(g.right, box.right) + 1) return true
    }
  }
  return false
}
let fitting = false, again = false
async function fit() {
  if (fitting) { again = true; return }
  fitting = true
  try {
    do {
      again = false
      fold.value = 0
      await nextTick()
      while (fold.value < FOLDS && spilling()) { fold.value++; await nextTick() }
    } while (again)
  } finally { fitting = false }
}
let observer: ResizeObserver | undefined
let seen = 0
onMounted(() => {
  observer = new ResizeObserver(([entry]) => { const width = Math.round(entry!.contentRect.width); if (width !== seen) { seen = width; void fit() } })
  if (bar.value) observer.observe(bar.value)
})
onBeforeUnmount(() => observer?.disconnect())
watch(() => [props.offerNo, props.local, props.status, props.revising, props.archived, props.pane, props.canFormat, props.compact, props.layout, props.canSave, props.presence?.sessions.length, props.percent], () => { void fit() })
// Folded, the zoom and history live in the … menu.
const zoomOut = computed(() => stepZoom(props.percent, -1))
const zoomIn = computed(() => stepZoom(props.percent, 1))
function menuZoom(mode: ZoomMode) { menu.value = null; emit('zoom', mode) }
function menuHistory(action: 'undo' | 'redo') { menu.value = null; if (action === 'undo') emit('undo'); else emit('redo') }
function menuKeys(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')]
  const index = items.indexOf(document.activeElement as HTMLButtonElement)
  event.preventDefault()
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : event.key === 'ArrowDown' ? Math.min(items.length - 1, index + 1) : Math.max(0, index - 1)
  items[next]?.focus()
}
</script>

<template>
  <header ref="bar" class="titlebar" :class="[{ compact, dock: layout === 'dock' }, `fold-${fold}`]">
    <div class="left">
      <RouterLink v-if="layout === 'full'" class="icon-btn sm flat back" to="/business/quotes" aria-label="Back to Quotes" data-tip="Quotes"><QuoteIcon name="arrow-left" :size="15" /></RouterLink>
      <div class="identity">
        <p class="number">{{ offerNo || 'Draft quote' }}</p>
        <span class="state-chip" :class="status"><QuoteStatusIcon :status="status" :label="false" :size="12" />{{ stateLabel }}</span>
        <span v-if="archived" class="state-chip">Archived</span>
      </div>
      <div class="save" :class="tone">
        <template v-if="editing || local === 'loading'">
          <span class="save-state" role="status" aria-live="polite">
            <svg v-if="local === 'saving' || local === 'loading'" class="spinner" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="8" cy="8" r="5.8" class="spin-track" /><path d="M8 2.2a5.8 5.8 0 0 1 5.8 5.8" class="spin-arc" /></svg>
            <span v-else-if="local === 'dirty'" class="dirty-dot" aria-hidden="true" />
            <QuoteIcon v-else :name="stateIcon" :size="14" />
            <span class="save-text">{{ saveText }}</span>
          </span>
          <button
            type="button" class="save-btn" :class="{ ready: canSave }" :disabled="!canSave" aria-label="Save draft" :aria-keyshortcuts="mac ? 'Meta+S' : 'Control+S'"
            :data-tip="canSave ? `Save · ${mod}S` : saveHint || 'Nothing to save'" @click="emit('save')"
          ><QuoteIcon name="save" :size="15" /><span class="save-caption">Save</span></button>
        </template>
        <span v-else class="frozen" role="status" :data-tip="status === 'draft' ? 'You can read this draft; an admin or member edits it' : 'Issued versions never change; revise to make a new one'"><AppIcon name="eye" :size="14" /><span class="save-text">Read only</span></span>
      </div>
      <div v-if="compact" class="win">
        <!-- Narrow bars keep the zoom its room: the profile rides on the first row. -->
        <slot name="profile" :folded="fold >= 1" />
        <QuotePeople :presence="presence" :principal-id="principalId" compact />
        <template v-if="layout === 'dock'">
          <button type="button" class="icon-btn sm flat expand-btn" aria-label="Open on its own page" data-tip="Open on its own page" @click="emit('expand')"><AppIcon name="expand" :size="15" /></button>
          <button type="button" class="icon-btn sm flat" aria-label="Close the quote" data-tip="Close · Esc" @click="emit('close')"><AppIcon name="close" :size="15" /></button>
        </template>
      </div>
    </div>
    <div class="center"><ZoomControl v-if="!zoomFolded" :mode="zoom" :percent="percent" :compact="compact" @zoom="value => emit('zoom', value)" /></div>
    <div class="right">
      <QuotePeople v-if="!compact" :presence="presence" :principal-id="principalId" />
      <div v-if="editing && !historyFolded" class="history" role="group" aria-label="History">
        <button type="button" class="icon-btn sm flat" :disabled="!canUndo" aria-label="Undo" :aria-keyshortcuts="mac ? 'Meta+Z' : 'Control+Z'" :data-tip="`Undo · ${mod}Z`" @mousedown.prevent @click="emit('undo')"><QuoteIcon name="undo" :size="15" /></button>
        <button type="button" class="icon-btn sm flat" :disabled="!canRedo" aria-label="Redo" :aria-keyshortcuts="mac ? 'Meta+Shift+Z' : 'Control+Shift+Z'" :data-tip="`Redo · ${mac ? 'Shift Cmd ' : 'Ctrl Shift '}Z`" @mousedown.prevent @click="emit('redo')"><QuoteIcon name="redo" :size="15" /></button>
      </div>
      <slot v-if="!compact" name="profile" :folded="fold >= 1" />
      <div class="print-pair">
        <button type="button" class="btn sm pdf" :disabled="printing" data-tip="Print or save as PDF" aria-label="PDF" @click="emit('print')"><QuoteIcon name="print" :size="15" /><span class="pdf-text">PDF</span></button>
        <button
          v-if="layout === 'full'" type="button" class="icon-btn sm flat chevron" :aria-expanded="!headerCollapsed" :aria-label="headerCollapsed ? 'Show the app header' : 'Hide the app header'"
          :data-tip="headerCollapsed ? 'Show the app header' : 'Hide the app header'" @click="emit('toggleHeader')"
        ><QuoteIcon :name="headerCollapsed ? 'chevron' : 'chevron-up'" :size="15" /></button>
      </div>
      <button ref="more" type="button" class="icon-btn sm flat" aria-label="More actions" aria-haspopup="menu" :aria-expanded="!!menu" data-tip="More" @click="toggleMenu"><AppIcon name="more" :size="15" /></button>
      <span class="divider" aria-hidden="true" />
      <button type="button" class="btn sm pane-btn" :aria-pressed="pane === 'details'" aria-label="Details" :aria-controls="pane ? 'quote-side' : undefined" data-tip="Status, customer link, versions and files" @click="emit('pane', 'details')">
        <QuoteIcon name="seal" :size="14" /><span class="pane-text">Details</span>
      </button>
      <button v-if="canFormat" type="button" class="icon-btn sm sidebar" :aria-pressed="pane === 'format'" aria-label="Format panel" :aria-controls="pane ? 'quote-side' : undefined" :data-tip="pane === 'format' ? 'Hide the format panel' : 'Show the format panel'" @click="emit('pane', 'format')"><QuoteIcon name="sidebar" :size="16" /></button>
      <template v-if="layout === 'dock' && !compact">
        <span class="divider" aria-hidden="true" />
        <button type="button" class="icon-btn sm flat" aria-label="Open on its own page" data-tip="Open on its own page" @click="emit('expand')"><AppIcon name="expand" :size="15" /></button>
        <button type="button" class="icon-btn sm flat" aria-label="Close the quote" data-tip="Close · Esc" @click="emit('close')"><AppIcon name="close" :size="15" /></button>
      </template>
    </div>
    <FloatingPanel v-if="menu" :anchor="menu" :width="248" align="end" label="Quote actions" @close="closeMenu">
      <div role="menu" aria-label="Quote actions" @keydown="menuKeys">
        <template v-if="historyFolded && editing">
          <button type="button" role="menuitem" class="menu-item" :disabled="!canUndo" @click="menuHistory('undo')"><QuoteIcon name="undo" :size="14" />Undo</button>
          <button type="button" role="menuitem" class="menu-item" :disabled="!canRedo" @click="menuHistory('redo')"><QuoteIcon name="redo" :size="14" />Redo</button>
          <div class="menu-sep" role="separator" />
        </template>
        <div v-if="zoomFolded" role="group" :aria-label="`Zoom, ${percent} %`">
          <p class="menu-label" aria-hidden="true">Zoom · {{ percent }} %</p>
          <button type="button" role="menuitem" class="menu-item" :disabled="zoomOut === null" @click="zoomOut !== null && menuZoom(zoomOut)"><QuoteIcon name="minus" :size="14" />Zoom out<span v-if="zoomOut" class="menu-hint">{{ zoomOut }} %</span></button>
          <button type="button" role="menuitem" class="menu-item" :disabled="zoomIn === null" @click="zoomIn !== null && menuZoom(zoomIn)"><QuoteIcon name="plus" :size="14" />Zoom in<span v-if="zoomIn" class="menu-hint">{{ zoomIn }} %</span></button>
          <button type="button" role="menuitem" class="menu-item" :aria-current="zoom === 'width' ? 'true' : undefined" @click="menuZoom('width')"><QuoteIcon name="fit-width" :size="14" />Fit width</button>
          <button type="button" role="menuitem" class="menu-item" :aria-current="zoom === 'page' ? 'true' : undefined" @click="menuZoom('page')"><QuoteIcon name="fit-page" :size="14" />Fit page</button>
        </div>
        <div v-if="zoomFolded" class="menu-sep" role="separator" />
        <button v-if="admin && status === 'draft' && !archived" type="button" role="menuitem" class="menu-item" data-autofocus @click="act('issue')"><QuoteIcon name="seal" :size="14" />Issue quote…</button>
        <button v-if="staff" type="button" role="menuitem" class="menu-item" @click="act('duplicate')"><AppIcon name="copy" :size="14" />Duplicate as a new quote</button>
        <button type="button" role="menuitem" class="menu-item" :disabled="!offerNo" @click="act('copyNumber')"><AppIcon name="tag" :size="14" />Copy the quote number</button>
        <button v-if="layout === 'full'" type="button" role="menuitem" class="menu-item" @click="act('collapse')"><AppIcon name="collapse" :size="14" />Show beside the list</button>
        <button v-else type="button" role="menuitem" class="menu-item" @click="act('expand')"><AppIcon name="expand" :size="14" />Open on its own page</button>
        <template v-if="admin">
          <div class="menu-sep" role="separator" />
          <button type="button" role="menuitem" class="menu-item" @click="act('archive')"><AppIcon name="archive" :size="14" />{{ archived ? 'Restore from the archive' : 'Archive' }}</button>
        </template>
      </div>
    </FloatingPanel>
  </header>
</template>

<style scoped>
/* Each side keeps at least what it holds (the zoom moves off centre before anything
   overlaps); when even that does not fit, the bar folds (see fit()). */
.titlebar { display: grid; grid-template-columns: minmax(min-content, 1fr) auto minmax(min-content, 1fr); align-items: center; gap: 12px; min-height: 52px; padding: 8px 16px; border-bottom: 1px solid var(--line-2); background: var(--surface-raised-2); -webkit-backdrop-filter: blur(14px) saturate(1.15); backdrop-filter: blur(14px) saturate(1.15); }
.dock { border-radius: var(--radius) var(--radius) 0 0; }
.left, .right { display: flex; align-items: center; gap: 8px; min-width: 0; }
.right { justify-content: flex-end; gap: 6px; }
/* Controls keep their size; the bar folds instead of squeezing them. */
.right > *, .win > * { flex-shrink: 0; }
.center { display: flex; align-items: center; justify-content: center; }
.back { flex-shrink: 0; }
.identity { display: flex; align-items: center; gap: 8px; min-width: 0; }
.number { font: 600 14px/1.2 var(--mono); color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-variant-ligatures: none; }
.state-chip { display: inline-flex; align-items: center; gap: 5px; flex-shrink: 0; height: 22px; padding: 0 8px 0 6px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font: 600 10px/22px var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.state-chip:not(:has(svg)) { padding-left: 8px; }
.save { display: flex; align-items: center; gap: 6px; min-width: 0; margin-left: 4px; }
/* The status: an icon and a word, not a control. Its slot keeps one width while you
   type, so the Save button beside it never shifts. */
.save-state { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 4px; color: var(--ink-2); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
.titlebar:not(.compact) .save-state { min-width: 124px; }
.save-state svg { flex-shrink: 0; color: var(--ink-3); }
.save.ok .save-state svg { color: color-mix(in oklab, var(--ok), var(--ink) 20%); }
.save.warn .save-state { color: var(--warn-ink); }
.save.warn .save-state svg { color: var(--warn-ink); }
.save.bad .save-state, .save.bad .save-state svg { color: var(--danger); }
.dirty-dot { flex-shrink: 0; width: 8px; height: 8px; margin: 0 3px; border-radius: 50%; background: var(--gold); }
/* The action: always in the same place; ready (teal) only when there is something to save. */
.save-btn { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 10px 0 8px; border: 0; border-radius: 8px; background: transparent; box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-3); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
.save-btn:disabled { cursor: default; }
.save-btn.ready { background: var(--seg-on); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
@media (hover: hover) { .save-btn.ready:hover { filter: brightness(1.03); } }
.save-btn:focus-visible { box-shadow: var(--focus-ring); }
.frozen { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 4px; color: var(--ink-2); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
.frozen svg { color: var(--ink-3); }
.spinner { animation: spin .8s linear infinite; }
.spin-track { stroke: var(--line-2); }
.spin-arc { stroke: var(--teal); }
@keyframes spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .spinner { animation-duration: 2.4s; } }
.history { display: inline-flex; gap: 2px; padding-right: 6px; margin-right: 2px; border-right: 1px solid var(--line); }
.print-pair { display: inline-flex; align-items: center; gap: 2px; }
.pdf { gap: 6px; }
.chevron { width: 26px; }
.divider { width: 1px; height: 20px; margin: 0 2px; background: var(--line); }
.pane-btn { gap: 6px; padding: 0 10px; font-weight: 600; color: var(--ink-2); }
.pane-btn[aria-pressed="true"], .sidebar[aria-pressed="true"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.win { display: flex; align-items: center; gap: 2px; margin-left: auto; flex-shrink: 0; }
.menu-item { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 34px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.menu-item svg { flex-shrink: 0; color: var(--ink-3); }
@media (hover: hover) { .menu-item:hover:not(:disabled) { background: var(--row-hover); } }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: none; outline: none; }
.menu-item:disabled { color: var(--ink-3); cursor: default; }
.menu-sep { height: 1px; margin: 4px 6px; background: var(--line); }
/* Narrow: labels step aside; phones and the docked panel stack the bar in two rows. */
.titlebar.compact { grid-template-columns: minmax(min-content, 1fr) max-content; grid-template-areas: "left left" "center right"; row-gap: 6px; padding: 6px 12px 8px; }
.titlebar.compact .left { grid-area: left; }
.titlebar.compact .center { grid-area: center; justify-content: flex-start; }
.titlebar.compact .right { grid-area: right; gap: 4px; }
.titlebar.compact .pdf-text, .titlebar.compact .pane-text { display: none; }
/* Folded (measured, not guessed): labels first, then the save state's words. */
.fold-1 .pdf-text, .fold-1 .pane-text, .fold-2 .pdf-text, .fold-2 .pane-text, .fold-3 .pdf-text, .fold-3 .pane-text, .fold-4 .pdf-text, .fold-4 .pane-text { display: none; }
.fold-1 .pdf, .fold-2 .pdf, .fold-3 .pdf, .fold-4 .pdf, .fold-1 .pane-btn, .fold-2 .pane-btn, .fold-3 .pane-btn, .fold-4 .pane-btn { width: 32px; padding: 0; justify-content: center; }
/* Save's caption folds with the other labels; at the narrowest the status keeps its
   icon (its words stay for screen readers). */
.fold-1 .save-caption, .fold-2 .save-caption, .fold-3 .save-caption, .fold-4 .save-caption { display: none; }
.fold-1 .save-btn, .fold-2 .save-btn, .fold-3 .save-btn, .fold-4 .save-btn { width: 28px; padding: 0; justify-content: center; }
.fold-4 .save-text { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
.fold-4 .save-state { min-width: 0 !important; }
.menu-label { padding: 6px 10px 2px; font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.menu-hint { margin-left: auto; font: 500 11px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.menu-item[aria-current="true"] { color: var(--teal-ink); font-weight: 600; }
.titlebar.compact .pdf, .titlebar.compact .pane-btn { width: 32px; padding: 0; justify-content: center; }
.titlebar.compact .history { padding-right: 4px; }
.titlebar.compact .divider { display: none; }
/* Phones: every control in the bar is a 40 px button, 6 px apart in both directions,
   like the app header's round buttons above it; each reaches 44 px for a finger
   (base.css) without reaching into its neighbour's. */
@media (max-width: 600px) {
  .titlebar.compact { row-gap: 6px; padding: 4px 10px; }
  .titlebar.compact .left, .titlebar.compact .right, .titlebar.compact .win, .titlebar.compact .history, .titlebar.compact .print-pair { gap: 6px; }
  .titlebar.compact .history { padding-right: 0; border-right: 0; margin-right: 0; }
  .titlebar.compact .icon-btn.sm, .titlebar.compact .btn.sm, .titlebar.compact .save-btn, .titlebar.compact :deep(.btn.sm) { height: 40px; min-width: 40px; border-radius: 12px; }
  .titlebar.compact .pdf, .titlebar.compact .pane-btn, .titlebar.compact .save-btn, .titlebar.compact :deep(.compact.picker), .titlebar.compact :deep(.compact.frozen-profile) { width: 40px; height: 40px; }
  .titlebar.compact :deep(.compact.frozen-profile) { display: inline-flex; align-items: center; }
  .titlebar.compact .save-btn { position: relative; }
  .titlebar.compact .save-btn::before { content: ''; position: absolute; top: 50%; left: 50%; width: 44px; height: 44px; transform: translate(-50%, -50%); }
  /* The first row holds the way back, the number, the state and the draft's
     controls: the state speaks by its icon (its words stay for screen readers and
     the notice below says what went wrong), Save by its icon, so the zoom keeps
     its place on the second row. */
  .titlebar.compact .save-state { height: 40px; min-width: 0; padding: 0 2px; }
  .titlebar.compact .save-text { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
  .titlebar.compact .save-caption { display: none; }
  /* Docked on a phone the quote fills the screen: its own page is one step away in the … menu. */
  .titlebar.compact .win .expand-btn { display: none; }
}
</style>
