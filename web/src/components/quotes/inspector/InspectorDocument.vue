<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { PAGE_MM } from '../../../lib/quotes/layout'
import { MARK_DEFAULT_MM, MARK_RANGES, mmText } from '../../../lib/quotes/inspector'
import MmField from './MmField.vue'
import { settingsLink } from '../../../lib/settings'
import { normalizeProfile } from '../../../lib/quotes/profile'
import type { QuoteEditor } from '../../../lib/quotes/editor'
import type { DocumentSettings, QuoteDocumentData } from '../../../lib/quotes/types'
import DatePicker from '../DatePicker.vue'
import QuoteIcon from './QuoteIcon.vue'

// The Document scope: this quote's own settings (dates, reference, currency) and
// what its page is (size, margins, language), with templates one link away in
// Settings. Templates shape new quotes; this one keeps its text.
const props = defineProps<{ editor: QuoteEditor; document: QuoteDocumentData; editable: boolean; admin: boolean; markPage?: number | null }>()
const emit = defineEmits<{ run: [command: () => void] }>()
const set = (patch: DocumentSettings) => emit('run', () => props.editor.setDocumentSettings(patch))
const hasPositions = computed(() => props.document.positions.length > 0)
// The footer mark (F08.03): one size and one vertical offset for the mark on every page.
const hasMark = computed(() => !!(props.document.layout.logo_file_id || props.document.sender.logo_file_id))
const markWidth = computed(() => { const n = Number(props.document.layout.logo_width_mm); return Number.isFinite(n) && props.document.layout.logo_width_mm ? n : MARK_DEFAULT_MM })
const markOffset = computed(() => { const n = Number(props.document.layout.logo_offset_mm); return Number.isFinite(n) ? n : 0 })
const markMoved = computed(() => !!props.document.layout.logo_width_mm || !!props.document.layout.logo_offset_mm)
// The page as this quote prints it: its document profile's geometry and language,
// or the built-in standard page when it has none.
const page = computed(() => {
  const p = normalizeProfile(props.document.profile)?.definition.page
  const mm = (value: string | undefined, fallback: number) => { const n = Number(value); return value && Number.isFinite(n) ? n : fallback }
  return {
    width: mm(p?.width_mm, PAGE_MM.width), height: mm(p?.height_mm, PAGE_MM.height),
    top: mm(p?.top_mm, PAGE_MM.marginTop), right: mm(p?.right_mm, PAGE_MM.marginRight), bottom: mm(p?.bottom_mm, PAGE_MM.marginBottom), left: mm(p?.left_mm, PAGE_MM.marginLeft),
  }
})
const pageSize = computed(() => {
  const { width, height } = page.value
  if (width === 210 && height === 297) return 'A4 portrait'
  if (width === 297 && height === 210) return 'A4 landscape'
  return `${width} by ${height} mm`
})
const margins = computed(() => {
  const { top, right, bottom, left } = page.value
  return top === bottom && left === right ? `${top} mm top and bottom, ${left} mm left and right` : `${top} mm top, ${right} mm right, ${bottom} mm bottom, ${left} mm left`
})
const language = computed(() => normalizeProfile(props.document.profile)?.definition.locale === 'en' ? 'English' : 'German (Austria)')
const dateProblem = computed(() => props.document.valid_until && props.document.offer_date && props.document.valid_until < props.document.offer_date ? 'Valid until is before the quote date.' : '')
function days(from: string, to: string) {
  if (!from || !to) return ''
  const n = Math.round((Date.parse(`${to}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / 86_400_000)
  return Number.isFinite(n) && n >= 0 ? `${n} ${n === 1 ? 'day' : 'days'}` : ''
}
function currency(event: Event) {
  const input = event.target as HTMLInputElement
  const value = input.value.trim().toUpperCase()
  if (!/^[A-Z]{3}$/.test(value)) { input.value = props.document.currency; return }
  if (value !== props.document.currency) set({ currency: value })
}
</script>

<template>
  <div class="tab-body">
    <section id="quote-this-quote" class="group" aria-labelledby="doc-details">
      <h3 id="doc-details" class="group-title">This quote</h3>
      <div class="rows">
        <div class="row"><span class="row-label">Quote date</span><DatePicker label="Quote date" :model-value="document.offer_date" :disabled="!editable" @update:model-value="value => set({ offer_date: value })" /></div>
        <div class="row"><span class="row-label">Valid until</span><DatePicker label="Valid until" :model-value="document.valid_until" :disabled="!editable" :invalid="!!dateProblem" @update:model-value="value => set({ valid_until: value })" /></div>
        <p v-if="dateProblem" class="note bad" role="alert">{{ dateProblem }}</p>
        <p v-else-if="days(document.offer_date, document.valid_until)" class="note">Open for {{ days(document.offer_date, document.valid_until) }}.</p>
        <label class="row"><span class="row-label">Project reference</span><input class="field-sm" :value="document.project_ref" maxlength="200" :disabled="!editable" placeholder="Optional" @change="set({ project_ref: ($event.target as HTMLInputElement).value.trim() })" /></label>
        <label class="row"><span class="row-label">Currency</span><input class="field-sm mono" :value="document.currency" maxlength="3" :disabled="!editable || hasPositions" autocomplete="off" aria-describedby="doc-currency-note" @change="currency" /></label>
        <p id="doc-currency-note" class="note">{{ hasPositions ? 'Fixed once positions are priced. Remove them to change it.' : 'Positions are priced in it.' }}</p>
      </div>
    </section>

    <section class="group" aria-labelledby="doc-page">
      <h3 id="doc-page" class="group-title">Page</h3>
      <dl class="facts">
        <div><dt>Size</dt><dd>{{ pageSize }}</dd></div>
        <div><dt>Margins</dt><dd>{{ margins }}</dd></div>
        <div><dt>Language</dt><dd>{{ language }}</dd></div>
        <div><dt>Numbers</dt><dd>Sections 1, 2, 3; set per section on the Section tab</dd></div>
      </dl>
      <p class="note">{{ document.profile ? 'Page size, margins, language and type come from the document profile picked in the title bar; the PDF matches.' : 'This quote uses the built-in standard page. A document profile, picked in the title bar, sets its own size, margins, language and type.' }}</p>
    </section>

    <section v-if="hasMark" id="quote-footer-mark" class="group" aria-labelledby="doc-mark">
      <div class="group-head">
        <h3 id="doc-mark" class="group-title">Footer mark</h3>
        <button v-if="markMoved" type="button" class="reset" :disabled="!editable" data-tip="Back to the default size and position" @mousedown.prevent @click="set({ layout: { logo_width_mm: undefined, logo_offset_mm: undefined } })"><QuoteIcon name="reset" :size="13" />Reset</button>
      </div>
      <p class="note" aria-live="polite">{{ markPage != null ? `The mark on page ${markPage + 1} is selected. ` : '' }}Its size and position apply to the mark on every page.</p>
      <div class="fields">
        <MmField label="Mark width" :value="markWidth" :range="MARK_RANGES.width" :disabled="!editable" @commit="value => set({ layout: { logo_width_mm: mmText(value) ?? undefined } })" />
        <MmField label="Mark up or down" :value="markOffset" :range="MARK_RANGES.offset" :disabled="!editable" hint="Negative moves it up" @commit="value => set({ layout: { logo_offset_mm: mmText(value) ?? undefined } })" />
      </div>
    </section>

    <section class="group" aria-labelledby="doc-templates">
      <h3 id="doc-templates" class="group-title">Templates</h3>
      <p class="note">The sender, introduction and closing texts of new quotes come from the templates. This quote keeps its own text.</p>
      <RouterLink v-if="admin" class="link-row" :to="settingsLink('business', 'quotes')"><QuoteIcon name="document" :size="15" />Edit templates<QuoteIcon name="arrow" :size="13" class="go" /></RouterLink>
      <p v-else class="note">A workspace admin edits templates in Settings.</p>
    </section>
  </div>
</template>

<style scoped>
.tab-body { display: grid; grid-template-columns: minmax(0, 1fr); }
.group { min-width: 0; grid-template-columns: minmax(0, 1fr); }
.group { display: grid; gap: 10px; padding: 16px 0; border-top: 1px solid var(--line); }
.group:first-child { border-top: 0; padding-top: 4px; }
.group-title { font: 500 10.5px/1.4 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.rows { display: grid; gap: 6px; }
.row { display: grid; grid-template-columns: minmax(0, 1fr) 150px; align-items: center; gap: 12px; min-height: 34px; }
.row-label { font-size: 13px; color: var(--ink-2); }
.field-sm { width: 100%; height: 30px; padding: 0 8px; border: 1px solid var(--glass-edge); border-radius: 8px; background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); color: var(--ink); font-size: 13px; }
@media (max-width: 600px) { .field-sm { height: 44px; } }
.field-sm.mono { font-family: var(--mono); text-transform: uppercase; }
.field-sm:focus { outline: none; box-shadow: var(--focus-ring); }
.field-sm:disabled { color: var(--ink-3); background: var(--surface-2); }
.field-sm[aria-invalid="true"] { box-shadow: var(--field-inset), 0 0 0 1px var(--danger-line); }
.note { font-size: 12px; line-height: 1.5; color: var(--ink-2); }
.note.bad { color: var(--danger); }
.facts { display: grid; gap: 6px; margin: 0; }
.facts > div { display: grid; grid-template-columns: 84px minmax(0, 1fr); gap: 12px; font-size: 12.5px; }
.facts dt { color: var(--ink-3); }
.facts dd { margin: 0; color: var(--ink); }
.link-row { display: inline-flex; align-items: center; justify-self: start; gap: 8px; height: 32px; padding: 0 10px; margin-left: -10px; border-radius: 8px; color: var(--teal-ink); font-size: 13px; font-weight: 600; text-decoration: none; }
.link-row:hover { background: var(--row-hover); }
.link-row:focus-visible { box-shadow: var(--focus-ring); }
.go { margin-left: 2px; }
.group-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 24px; }
.fields { display: grid; gap: 6px; }
.reset { display: inline-flex; align-items: center; gap: 5px; height: 24px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12px; font-weight: 600; }
.reset:hover:not(:disabled) { background: var(--row-selected); }
.reset:focus-visible { box-shadow: var(--focus-ring); }
.reset:disabled { color: var(--ink-3); }
</style>
