<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { COLUMN_BY_ID, amountOf, clampWidth, dayText, numberOf, statusOf, titleOf, fitColumns, visibleColumns, STATUS_META, type ColumnId, type QuoteRow, type Sort } from '../../../lib/quotes/list'
import { highlight } from '../../../lib/work'
import AppIcon from '../../AppIcon.vue'
import BizIcon from '../../business/BizIcon.vue'
import QuoteStatusIcon from '../QuoteStatusIcon.vue'
import type { RowMenuAnchor } from '../../../lib/rowActions'

// The quote list: the ticket list's table (sortable headers, edges to drag or
// fit, a keyboard cursor) with one row per quote. The quote's title takes the
// width the other columns leave; every other column keeps the width you give it.
// Phones get one card per quote instead of columns.
const props = defineProps<{
  rows: QuoteRow[]; loading: boolean; query: string; sort: Sort; cursorId: string | null; openId: string | null; menuId?: string | null; canDuplicate?: boolean
  widths: Partial<Record<ColumnId, number>>; today: string
}>()
const emit = defineEmits<{
  sort: [key: ColumnId]; cursor: [id: string]; open: [row: QuoteRow]; widths: [widths: Partial<Record<ColumnId, number>>]; gridFocus: []
  action: [row: QuoteRow, id: 'duplicate' | 'pdf']; menu: [row: QuoteRow, anchor: RowMenuAnchor]
}>()

const card = ref<HTMLElement>()
const grid = ref<HTMLTableElement>()
const width = ref(1200)
const phone = ref(false)
const phoneQuery = window.matchMedia('(max-width: 720px)')
const phoneChange = () => { phone.value = phoneQuery.matches }
let sizer: ResizeObserver | undefined
onMounted(() => {
  phoneChange(); phoneQuery.addEventListener('change', phoneChange)
  if (card.value) {
    // The first fit happens before paint, so the columns never jump into place.
    const box = getComputedStyle(card.value)
    width.value = card.value.clientWidth - parseFloat(box.paddingLeft) - parseFloat(box.paddingRight)
    sizer = new ResizeObserver(([entry]) => { width.value = entry.contentRect.width }); sizer.observe(card.value)
  }
})
onBeforeUnmount(() => { phoneQuery.removeEventListener('change', phoneChange); sizer?.disconnect() })

// Live widths while an edge is dragged; saved when the drag ends.
const live = ref<Partial<Record<ColumnId, number>>>({})
watch(() => props.widths, () => { if (!resizing) live.value = {} })
const sized = computed(() => ({ ...props.widths, ...live.value }))
const ids = computed(() => visibleColumns(width.value, sized.value))
const columns = computed(() => ids.value.map(id => COLUMN_BY_ID.get(id)!))
// Beside a docked quote the table is narrow: the other columns give way before the title does.
const fitted = computed(() => fitColumns(ids.value, width.value, sized.value))
const shown = (id: ColumnId) => fitted.value[id] ?? clampWidth(id, sized.value[id])
const titleWidth = computed(() => fitted.value.title!)
const colWidth = (id: ColumnId) => id === 'title' ? null : shown(id)
const currentWidth = (id: ColumnId) => id === 'title' ? titleWidth.value : shown(id)

let resizing: { id: ColumnId; startX: number; startWidth: number } | null = null
let lastPress = { id: '' as ColumnId | '', at: 0 }
function resizeStart(event: PointerEvent, id: ColumnId) {
  if (event.button !== 0 || id === 'title') return
  event.preventDefault(); event.stopPropagation()
  if (lastPress.id === id && event.timeStamp - lastPress.at < 350) { lastPress = { id: '', at: 0 }; autofit(id); return }
  lastPress = { id, at: event.timeStamp }
  resizing = { id, startX: event.clientX, startWidth: shown(id) }
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
}
function resizeMove(event: PointerEvent) {
  if (!resizing) return
  live.value = { ...live.value, [resizing.id]: clampWidth(resizing.id, resizing.startWidth + event.clientX - resizing.startX) }
}
function resizeEnd() {
  if (!resizing) return
  const id = resizing.id
  resizing = null
  if (live.value[id] !== undefined) emit('widths', { ...props.widths, [id]: live.value[id] })
}
function resizeKey(event: KeyboardEvent, id: ColumnId) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  const next = clampWidth(id, shown(id) + (event.key === 'ArrowRight' ? 16 : -16))
  live.value = { ...live.value, [id]: next }
  emit('widths', { ...props.widths, [id]: next })
}
// Fit: the widest content in the column, header included, within its bounds.
function autofit(id: ColumnId) {
  let widest = 0
  for (const cell of grid.value?.querySelectorAll<HTMLElement>(`td.c-${id} .cell, th.c-${id} .th-sort`) ?? []) {
    const children = [...cell.children] as HTMLElement[]
    const gap = parseFloat(getComputedStyle(cell).columnGap) || 0
    widest = Math.max(widest, children.reduce((sum, child) => sum + Math.max(child.scrollWidth, child.getBoundingClientRect().width), 0) + gap * Math.max(0, children.length - 1))
  }
  const next = clampWidth(id, Math.ceil(widest + 26))
  live.value = { ...live.value, [id]: next }
  emit('widths', { ...props.widths, [id]: next })
}

const ariaSort = (id: ColumnId) => props.sort.key === id ? (props.sort.dir === 'asc' ? 'ascending' : 'descending') : undefined
const tip = (label: string) => `Sort by ${label.toLowerCase()}`
function rowClick(event: MouseEvent, row: QuoteRow) {
  if ((event.target as HTMLElement).closest('button')) return
  emit('cursor', row.quote_node_id)
  emit('open', row)
}
// The quote's link opens its full page in a new tab (or window) with a modifier;
// a plain click docks it beside the list like any other click on the row.
function linkClick(event: MouseEvent) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) { event.stopPropagation(); return }
  event.preventDefault()
}
watch(() => props.cursorId, async id => {
  if (!id) return
  await nextTick()
  document.getElementById(`quote-${id}`)?.scrollIntoView({ block: 'nearest' })
})
function fullOf(row: QuoteRow, id: ColumnId) {
  switch (id) {
    case 'title': return [titleOf(row), row.project_ref].filter(Boolean).join(' · ')
    case 'customer': return row.customer_name
    case 'number': return numberOf(row)
    default: return ''
  }
}
function tipIfCut(event: PointerEvent) {
  const cell = event.currentTarget as HTMLElement
  const bottom = cell.getBoundingClientRect().bottom
  const cut = ([...cell.children] as HTMLElement[]).some(el => el.getBoundingClientRect().top >= bottom - 1 || el.scrollWidth > el.clientWidth + 1)
  if (cut && cell.dataset.full) cell.dataset.tip = cell.dataset.full
  else delete cell.dataset.tip
}
const pageUrl = (row: QuoteRow) => `/business/quotes/${encodeURIComponent(row.quote_node_id)}`
// "Valid until" reads as a warning only while an issued quote is about to lapse.
function soon(row: QuoteRow) {
  if (statusOf(row) !== 'issued' || !row.valid_until) return false
  const days = (Date.parse(`${row.valid_until}T00:00:00Z`) - Date.parse(`${props.today}T00:00:00Z`)) / 86_400_000
  return days >= 0 && days <= 7
}
// A right-click opens the row's own menu where the pointer is.
function contextMenu(event: MouseEvent, row: QuoteRow) {
  event.preventDefault()
  emit('cursor', row.quote_node_id)
  emit('menu', row, { x: event.clientX, y: event.clientY })
}
// An inline action moves the cursor to its row, so focus has a place to come back to.
function inline(row: QuoteRow, id: 'duplicate' | 'pdf') {
  emit('cursor', row.quote_node_id)
  emit('action', row, id)
}
function more(event: MouseEvent, row: QuoteRow) {
  emit('cursor', row.quote_node_id)
  emit('menu', row, event.currentTarget as HTMLElement)
}
// The … button of a row, for a menu opened from the keyboard.
const moreButton = (id: string) => document.querySelector<HTMLElement>(`#quote-${CSS.escape(id)} .row-more`)
defineExpose({ focus: () => (phone.value ? card.value?.querySelector<HTMLElement>('.card-link') : grid.value)?.focus(), moreButton })
</script>

<template>
  <div ref="card" class="table-card">
    <ul v-if="phone" class="cards" aria-label="Quotes" :aria-busy="loading">
      <template v-if="loading && !rows.length">
        <li v-for="i in 5" :key="i" class="card-row ghost" aria-hidden="true"><span class="skeleton sk-name" /><span class="skeleton sk-line" /></li>
      </template>
      <li v-for="row in rows" :id="`quote-${row.quote_node_id}`" :key="row.quote_node_id" class="card-row" :class="{ cursor: cursorId === row.quote_node_id }" @contextmenu="contextMenu($event, row)">
        <RouterLink class="card-link" :to="pageUrl(row)" @click="emit('cursor', row.quote_node_id)">
          <span class="card-top">
            <span class="card-name"><template v-for="(part, i) in highlight(titleOf(row), query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
            <span v-if="amountOf(row)" class="card-amount mono">{{ amountOf(row) }}</span>
          </span>
          <span class="card-meta">
            <span class="number mono">{{ numberOf(row) }}</span>
            <QuoteStatusIcon :status="statusOf(row)" :size="13" />
            <span v-if="row.archived" class="archived-chip">Archived</span>
          </span>
          <span class="card-sub">
            <span v-if="row.customer_name" class="meta-item"><BizIcon name="building" :size="12" />{{ row.customer_name }}</span>
            <span v-if="row.offer_date" class="meta-item"><BizIcon name="calendar" :size="12" />{{ dayText(row.offer_date) }}</span>
          </span>
        </RouterLink>
        <button type="button" class="icon-btn sm flat row-more card-more" :aria-label="`Actions for ${numberOf(row)}`" aria-haspopup="menu" :aria-expanded="menuId === row.quote_node_id" @click="more($event, row)"><AppIcon name="more" :size="16" /></button>
      </li>
    </ul>

    <table
      v-else ref="grid" class="quotes" role="grid" aria-label="Quotes" :aria-busy="loading" tabindex="0"
      :aria-activedescendant="cursorId && rows.some(r => r.quote_node_id === cursorId) ? `quote-${cursorId}` : undefined" @focus="emit('gridFocus')" @contextmenu.self.prevent
    >
      <colgroup>
        <col v-for="column in columns" :key="column.id" :style="colWidth(column.id) ? { width: `${colWidth(column.id)}px` } : undefined" />
      </colgroup>
      <thead>
        <tr>
          <th v-for="column in columns" :key="column.id" scope="col" :class="[`c-${column.id}`, { end: column.end }]" :aria-sort="ariaSort(column.id)">
            <button type="button" class="th-sort" :class="{ on: sort.key === column.id }" :data-tip="tip(column.label)" @click="emit('sort', column.id)">
              <span>{{ column.label }}</span>
              <span class="sort-mark" aria-hidden="true"><AppIcon v-if="sort.key === column.id" :name="sort.dir === 'asc' ? 'arrow-up' : 'arrow-down'" :size="11" /></span>
            </button>
            <span
              v-if="column.id !== 'title'" class="col-resize" role="separator" aria-orientation="vertical" tabindex="0"
              :aria-label="`Resize ${column.label} column`" :aria-valuenow="currentWidth(column.id)" :aria-valuemin="column.min" :aria-valuemax="column.max"
              data-tip="Drag to resize · double-click to fit" @pointerdown="resizeStart($event, column.id)" @pointermove="resizeMove" @pointerup="resizeEnd" @pointercancel="resizeEnd"
              @keydown="resizeKey($event, column.id)" @click.stop
            />
          </th>
        </tr>
      </thead>
      <tbody v-if="loading && !rows.length" aria-hidden="true">
        <tr v-for="i in 8" :key="i" class="row ghost">
          <td v-for="column in columns" :key="column.id" :class="`c-${column.id}`"><div class="cell"><span class="skeleton" :style="{ width: column.id === 'title' ? `${36 + ((i * 29) % 40)}%` : '60%' }" /></div></td>
        </tr>
      </tbody>
      <tbody v-else :class="{ dim: loading }">
        <tr
          v-for="row in rows" :id="`quote-${row.quote_node_id}`" :key="row.quote_node_id" class="row"
          :class="{ cursor: cursorId === row.quote_node_id, open: openId === row.quote_node_id, archived: row.archived }"
          :aria-selected="cursorId === row.quote_node_id" @click="rowClick($event, row)" @contextmenu="contextMenu($event, row)"
        >
          <template v-for="column in columns" :key="column.id">
            <td v-if="column.id === 'number'" class="c-number">
              <div class="cell" :data-full="fullOf(row, 'number')" @pointerover="tipIfCut">
                <span class="number mono"><template v-for="(part, i) in highlight(numberOf(row), query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
              </div>
            </td>
            <td v-else-if="column.id === 'title'" class="c-title">
              <div class="cell drop" :data-full="fullOf(row, 'title')" @pointerover="tipIfCut">
                <a class="title-link" :href="pageUrl(row)" tabindex="-1" @click="linkClick($event)">
                  <template v-for="(part, i) in highlight(titleOf(row), query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template>
                </a>
                <span v-if="row.archived" class="archived-chip">Archived</span>
                <span v-if="row.project_ref" class="ref">{{ row.project_ref }}</span>
              </div>
              <span class="row-actions">
                <button v-if="canDuplicate" type="button" class="icon-btn sm flat" :aria-label="`Duplicate ${numberOf(row)}`" data-tip="Duplicate as a new draft" @click.stop="inline(row, 'duplicate')"><AppIcon name="copy" :size="14" /></button>
                <button type="button" class="icon-btn sm flat" :aria-label="`Print ${numberOf(row)} or save it as PDF`" data-tip="Print or save as PDF" @click.stop="inline(row, 'pdf')"><BizIcon name="print" :size="14" /></button>
                <button type="button" class="icon-btn sm flat row-more" :aria-label="`Actions for ${numberOf(row)}`" aria-haspopup="menu" :aria-expanded="menuId === row.quote_node_id" data-tip="All actions · Shift F10" @click.stop="more($event, row)"><AppIcon name="more" :size="15" /></button>
              </span>
            </td>
            <td v-else-if="column.id === 'customer'" class="c-customer">
              <div class="cell" :data-full="fullOf(row, 'customer')" @pointerover="tipIfCut">
                <span v-if="row.customer_name" class="text"><template v-for="(part, i) in highlight(row.customer_name, query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                <span v-else class="empty">—</span>
              </div>
            </td>
            <td v-else-if="column.id === 'status'" class="c-status"><div class="cell" :data-tip="STATUS_META[statusOf(row)].hint"><QuoteStatusIcon :status="statusOf(row)" /></div></td>
            <td v-else-if="column.id === 'date'" class="c-date"><div class="cell"><span v-if="row.offer_date" class="day">{{ dayText(row.offer_date) }}</span><span v-else class="empty">—</span></div></td>
            <td v-else-if="column.id === 'valid'" class="c-valid">
              <div class="cell"><span v-if="row.valid_until" class="day" :class="{ soon: soon(row), past: statusOf(row) === 'expired' }">{{ dayText(row.valid_until) }}</span><span v-else class="empty">—</span></div>
            </td>
            <td v-else class="c-amount end"><div class="cell"><span v-if="amountOf(row)" class="mono">{{ amountOf(row) }}</span><span v-else class="empty">—</span></div></td>
          </template>
        </tr>
      </tbody>
    </table>
    <slot />
  </div>
</template>

<style scoped>
.table-card {
  position: relative; border-radius: var(--radius); border: 1px solid var(--glass-edge); overflow: clip;
  background: linear-gradient(165deg, var(--surface-raised-2), var(--glass) 60%); box-shadow: var(--shadow);
}
.quotes { width: 100%; border-collapse: separate; border-spacing: 0; table-layout: fixed; font-size: 13.5px; }
.quotes:focus-visible { box-shadow: none; }
.quotes:focus-visible .row.cursor { outline: 2px solid var(--teal); outline-offset: -2px; }
thead th {
  position: sticky; top: 0; z-index: 2; height: 36px; padding: 0 12px; text-align: left; font-weight: 500;
  background: var(--surface-raised-2); border-bottom: 1px solid var(--line-2);
}
thead th:first-child { padding-left: 18px; }
thead th.end { text-align: right; }
thead th.end .th-sort { flex-direction: row-reverse; }
.th-sort { display: inline-flex; align-items: center; gap: 6px; max-width: 100%; height: 26px; margin: 0 -6px; padding: 0 6px; border: 0; border-radius: 6px; background: transparent; font: 500 10.5px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; white-space: nowrap; }
.th-sort span:first-child { overflow: hidden; text-overflow: ellipsis; }
.th-sort:hover { color: var(--ink); background: var(--row-hover); }
.th-sort.on { color: var(--teal-ink); }
.th-sort:focus-visible { box-shadow: var(--focus-ring); }
.sort-mark { display: inline-flex; align-items: center; min-width: 11px; }
.col-resize { position: absolute; top: 6px; bottom: 6px; right: 0; z-index: 3; width: 9px; cursor: col-resize; touch-action: none; outline: none; }
.col-resize::after { content: ''; position: absolute; top: 0; bottom: 0; left: 8px; width: 1px; background: var(--line-2); opacity: 0; transition: opacity .12s ease; }
thead th:hover .col-resize::after { opacity: 1; }
.col-resize:hover::after, .col-resize:active::after, .col-resize:focus-visible::after { opacity: 1; width: 2px; left: 7px; background: var(--teal); }

.row { height: 46px; cursor: pointer; scroll-margin-top: 44px; }
.row td { height: 46px; padding: 0 12px; border-bottom: 1px solid var(--line); vertical-align: middle; }
.row td:first-child { padding-left: 18px; }
tbody .row:last-child td { border-bottom: 0; }
.cell { display: flex; align-items: center; gap: 8px; min-width: 0; line-height: 18px; white-space: nowrap; }
/* The reference and the archived note drop out whole before the title is cut. */
.cell.drop { flex-wrap: wrap; align-content: flex-start; row-gap: 18px; height: 18px; overflow: hidden; }
.end .cell { justify-content: flex-end; }
@media (hover: hover) { .row:hover td { background: var(--row-hover); } }
.row.cursor td, .row.open td { background: var(--row-selected); }
tbody.dim { opacity: .55; }
.row.archived .title-link { color: var(--ink-2); }
.number { padding: 2px 7px; border-radius: 6px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink); font: 500 12px/16px var(--mono); font-variant-ligatures: none; overflow: hidden; text-overflow: ellipsis; }
/* Row actions float over the end of the title cell (as on the ticket list), shown
   on hover, on the cursor row and while one of them has focus; the title fades
   out beneath them. */
td.c-title { position: relative; }
.row-actions { position: absolute; top: 50%; right: 8px; display: inline-flex; gap: 2px; transform: translateY(-50%); visibility: hidden; }
.row-actions .icon-btn { width: 26px; height: 26px; color: var(--ink-3); }
@media (hover: hover) { .row-actions .icon-btn:hover { color: var(--teal-ink); } }
.row.cursor .row-actions, .row-actions:focus-within { visibility: visible; }
@media (hover: hover) { .row:hover .row-actions { visibility: visible; } }
.row.cursor td.c-title .cell, td.c-title:focus-within .cell { -webkit-mask-image: linear-gradient(to left, transparent 88px, #000 116px); mask-image: linear-gradient(to left, transparent 88px, #000 116px); }
@media (hover: hover) { .row:hover td.c-title .cell { -webkit-mask-image: linear-gradient(to left, transparent 88px, #000 116px); mask-image: linear-gradient(to left, transparent 88px, #000 116px); } }
.row.cursor td.c-title .cell, td.c-title:focus-within .cell { padding-right: 88px; }
@media (hover: hover) { .row:hover td.c-title .cell { padding-right: 88px; } }
.title-link { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--ink); font-weight: 600; text-decoration: none; }
.ref { flex: 0 0 auto; color: var(--ink-3); font-size: 12.5px; }
.archived-chip { flex: 0 0 auto; height: 18px; padding: 0 7px; border-radius: 999px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-2); font: 600 10px/18px var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.text { overflow: hidden; text-overflow: ellipsis; color: var(--ink-2); }
.day { color: var(--ink-2); font-variant-numeric: tabular-nums; }
.day.soon { color: var(--warn-ink); font-weight: 600; }
.day.past { color: var(--ink-3); text-decoration: line-through; text-decoration-color: var(--line-2); }
.empty { color: var(--ink-3); font-size: 12.5px; }
.mono { font-family: var(--mono); font-size: 12.5px; font-variant-numeric: tabular-nums; font-variant-ligatures: none; color: var(--ink); }
.ghost .skeleton { height: 10px; }

/* Phones */
.cards { margin: 0; padding: 0; list-style: none; }
.card-row { position: relative; border-bottom: 1px solid var(--line); }
.card-more { position: absolute; top: 8px; right: 8px; z-index: 1; width: 36px; height: 36px; color: var(--ink-2); }
.card-row .card-link { padding-right: 52px; }
.card-row:last-child { border-bottom: 0; }
.card-row.ghost { display: grid; gap: 10px; padding: 16px; }
.sk-name { width: 55%; height: 12px; } .sk-line { width: 80%; }
.card-link { display: grid; gap: 6px; padding: 12px 16px; color: inherit; text-decoration: none; }
.card-row.cursor .card-link { background: var(--row-selected); }
.card-link:focus-visible { box-shadow: inset var(--focus-ring); }
.card-top { display: flex; align-items: baseline; gap: 10px; min-width: 0; }
.card-name { flex: 1; min-width: 0; font-size: 15px; font-weight: 650; color: var(--ink); overflow-wrap: anywhere; }
.card-amount { flex-shrink: 0; font-size: 13px; }
.card-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; }
.card-meta .quote-status { font-size: 12.5px; }
.card-sub { display: flex; flex-wrap: wrap; gap: 4px 12px; }
.meta-item { display: inline-flex; align-items: center; gap: 5px; min-width: 0; font-size: 12.5px; color: var(--ink-2); overflow-wrap: anywhere; }
.meta-item svg { flex-shrink: 0; color: var(--ink-3); }
</style>
