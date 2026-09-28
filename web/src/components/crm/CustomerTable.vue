<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { COLUMN_BY_ID, clampWidth, layoutWidths, minorMoney, placeOf, visibleColumns, type ColumnId, type ContactCard, type Customer, type SortKey } from '../../lib/crm'
import { highlight } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'
import BizIcon from '../business/BizIcon.vue'
import type { RowMenuAnchor } from '../../lib/rowActions'

// The customer list: the ticket list's table (sortable headers, columns that
// follow the table's width, edges to drag or fit, a keyboard cursor) with one row
// per customer. Phones get one card per customer instead of columns. A row's
// actions show on hover and focus (a new quote, then all of them under …); the
// same menu opens on right-click, the context-menu key and Shift+F10.
const props = defineProps<{
  rows: Customer[]; loading: boolean; query: string; sort: { key: SortKey; dir: 'asc' | 'desc' }
  cursorId: string | null; widths: Partial<Record<ColumnId, number>>; contact: (c: Customer) => ContactCard | undefined
  canQuote?: boolean; menuId?: string | null
}>()
const emit = defineEmits<{
  sort: [key: SortKey]; cursor: [id: string]; open: [customer: Customer]; widths: [widths: Partial<Record<ColumnId, number>>]; gridFocus: []
  action: [customer: Customer, id: 'quote']; menu: [customer: Customer, anchor: RowMenuAnchor]
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
// Customer stops near its target; spare width widens the contact, place and industry.
const layout = computed(() => layoutWidths(ids.value, width.value, sized.value))
const shown = (id: ColumnId) => layout.value[id] ?? clampWidth(id, sized.value[id])
const columns = computed(() => ids.value.map(id => COLUMN_BY_ID.get(id)!))
const nameWidth = computed(() => Math.max(COLUMN_BY_ID.get('name')!.min, Math.round(width.value - ids.value.filter(id => id !== 'name').reduce((sum, id) => sum + shown(id), 0))))
const colWidth = (id: ColumnId) => id === 'name' ? null : shown(id)
const currentWidth = (id: ColumnId) => id === 'name' ? nameWidth.value : shown(id)

let resizing: { id: ColumnId; startX: number; startWidth: number } | null = null
let lastPress = { id: '' as ColumnId | '', at: 0 }
function resizeStart(event: PointerEvent, id: ColumnId) {
  if (event.button !== 0 || id === 'name') return
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

const ariaSort = (id: SortKey) => props.sort.key === id ? (props.sort.dir === 'asc' ? 'ascending' : 'descending') : undefined
const tip = (label: string) => `Sort by ${label.toLowerCase()}`
function rowClick(event: MouseEvent, customer: Customer) {
  // Links inside the row keep their own click (a new tab, mail, phone).
  if ((event.target as HTMLElement).closest('a, button')) return
  emit('cursor', customer.id)
  emit('open', customer)
}
// The cursor row stays in view as it moves.
watch(() => props.cursorId, async id => {
  if (!id) return
  await nextTick()
  document.getElementById(`customer-${id}`)?.scrollIntoView({ block: 'nearest' })
})
// The full value of a cell, shown as a tooltip when the cell cannot show all of it.
function fullOf(c: Customer, id: ColumnId) {
  const person = props.contact(c)
  switch (id) {
    case 'name': return [c.name, c.legal_name !== c.name ? c.legal_name : ''].filter(Boolean).join(' · ')
    case 'contact': return person ? [person.name, person.role].filter(Boolean).join(' · ') : ''
    case 'place': return placeOf(c)
    case 'industry': return c.industry
    case 'number': return c.customer_no ?? ''
    default: return money(c)
  }
}
// Runs before the tooltip host reads data-tip: a cell carries its full value
// only while something in it is cut off or has dropped to the hidden line.
function tipIfCut(event: PointerEvent) {
  const cell = event.currentTarget as HTMLElement
  const bottom = cell.getBoundingClientRect().bottom
  const cut = ([...cell.children] as HTMLElement[]).some(el => el.getBoundingClientRect().top >= bottom - 1 || el.scrollWidth > el.clientWidth + 1)
  if (cut && cell.dataset.full) cell.dataset.tip = cell.dataset.full
  else delete cell.dataset.tip
}
const skeletonRows = 8
const money = (c: Customer) => minorMoney(c.hourly_rate_minor, c.currency)
function contextMenu(event: MouseEvent, c: Customer) {
  event.preventDefault()
  emit('cursor', c.id)
  emit('menu', c, { x: event.clientX, y: event.clientY })
}
function more(event: MouseEvent, c: Customer) {
  emit('cursor', c.id)
  emit('menu', c, event.currentTarget as HTMLElement)
}
const moreButton = (id: string) => document.querySelector<HTMLElement>(`#customer-${CSS.escape(id)} .row-more`)
defineExpose({ focus: () => (phone.value ? card.value?.querySelector<HTMLElement>('.card-link') : grid.value)?.focus(), moreButton })
</script>

<template>
  <div ref="card" class="table-card">
    <!-- Phones: one card per customer, the name first, then who and where. -->
    <ul v-if="phone" class="cards" aria-label="Customers" :aria-busy="loading">
      <template v-if="loading && !rows.length">
        <li v-for="i in 5" :key="i" class="card-row ghost" aria-hidden="true"><span class="skeleton sk-name" /><span class="skeleton sk-line" /></li>
      </template>
      <li v-for="c in rows" :id="`customer-${c.id}`" :key="c.id" class="card-row" :class="{ cursor: cursorId === c.id, archived: c.archived }" @contextmenu="contextMenu($event, c)">
        <RouterLink class="card-link" :to="`/business/customers/${c.id}`" @click="emit('cursor', c.id)">
          <span class="card-top">
            <span class="card-name" :title="c.name"><template v-for="(part, i) in highlight(c.name, query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
            <span v-if="c.customer_no" class="number mono">{{ c.customer_no }}</span>
          </span>
          <span v-if="c.archived" class="archived-chip">Archived</span>
          <span v-if="c.legal_name && c.legal_name !== c.name" class="card-sub">{{ c.legal_name }}</span>
          <span class="card-meta">
            <span v-if="contact(c)" class="meta-item"><AppIcon name="user" :size="12" />{{ contact(c)!.name }}</span>
            <span v-if="placeOf(c)" class="meta-item"><BizIcon name="pin" :size="12" />{{ placeOf(c) }}</span>
            <span v-if="money(c)" class="meta-item mono">{{ money(c) }}/h</span>
          </span>
        </RouterLink>
        <button type="button" class="icon-btn sm flat row-more card-more" :aria-label="`Actions for ${c.name}`" aria-haspopup="menu" :aria-expanded="menuId === c.id" @click="more($event, c)"><AppIcon name="more" :size="16" /></button>
      </li>
    </ul>

    <table v-else ref="grid" class="customers" role="grid" aria-label="Customers" :aria-busy="loading" tabindex="0" :aria-activedescendant="cursorId && rows.some(r => r.id === cursorId) ? `customer-${cursorId}` : undefined" @focus="emit('gridFocus')" @contextmenu.self.prevent>
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
              v-if="column.id !== 'name'" class="col-resize" role="separator" aria-orientation="vertical" tabindex="0"
              :aria-label="`Resize ${column.label} column`" :aria-valuenow="currentWidth(column.id)" :aria-valuemin="column.min" :aria-valuemax="column.max"
              data-tip="Drag to resize · double-click to fit" @pointerdown="resizeStart($event, column.id)" @pointermove="resizeMove" @pointerup="resizeEnd" @pointercancel="resizeEnd"
              @keydown="resizeKey($event, column.id)" @click.stop
            />
          </th>
        </tr>
      </thead>
      <tbody v-if="loading && !rows.length" aria-hidden="true">
        <tr v-for="i in skeletonRows" :key="i" class="row ghost">
          <td v-for="column in columns" :key="column.id" :class="`c-${column.id}`"><div class="cell"><span class="skeleton" :style="{ width: column.id === 'name' ? `${36 + ((i * 29) % 40)}%` : '60%' }" /></div></td>
        </tr>
      </tbody>
      <tbody v-else :class="{ dim: loading }">
        <tr
          v-for="c in rows" :id="`customer-${c.id}`" :key="c.id" class="row" :class="{ cursor: cursorId === c.id, archived: c.archived }"
          :aria-selected="cursorId === c.id" @click="rowClick($event, c)" @contextmenu="contextMenu($event, c)"
        >
          <template v-for="column in columns" :key="column.id">
            <td v-if="column.id === 'name'" class="c-name">
              <div class="cell drop" :data-full="fullOf(c, 'name')" @pointerover="tipIfCut">
                <span class="org-mark" aria-hidden="true"><BizIcon name="building" :size="14" /></span>
                <RouterLink class="name-link" :to="`/business/customers/${c.id}`" :title="c.name" tabindex="-1">
                  <template v-for="(part, i) in highlight(c.name, query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template>
                </RouterLink>
                <span v-if="c.archived" class="archived-chip">Archived</span>
                <span v-if="c.legal_name && c.legal_name !== c.name" class="legal">{{ c.legal_name }}</span>
              </div>
              <span class="row-actions" :class="{ solo: !canQuote || c.archived }">
                <button v-if="canQuote && !c.archived" type="button" class="icon-btn sm flat" :aria-label="`New quote for ${c.name}`" data-tip="New quote for this customer" @click.stop="emit('cursor', c.id); emit('action', c, 'quote')"><BizIcon name="document" :size="14" /></button>
                <button type="button" class="icon-btn sm flat row-more" :aria-label="`Actions for ${c.name}`" aria-haspopup="menu" :aria-expanded="menuId === c.id" data-tip="All actions · Shift F10" @click.stop="more($event, c)"><AppIcon name="more" :size="15" /></button>
              </span>
            </td>
            <td v-else-if="column.id === 'number'" class="c-number">
              <div class="cell">
                <span v-if="c.customer_no" class="number mono"><template v-for="(part, i) in highlight(c.customer_no, query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                <span v-else class="empty" data-tip="A number is assigned with the first quote">Not yet</span>
              </div>
            </td>
            <td v-else-if="column.id === 'contact'" class="c-contact">
              <div class="cell" :class="{ drop: contact(c) }" :data-full="fullOf(c, 'contact')" @pointerover="tipIfCut">
                <template v-if="contact(c)"><Avatar :name="contact(c)!.name" :size="20" /><span class="person">{{ contact(c)!.name }}</span><span v-if="contact(c)!.role" class="role">{{ contact(c)!.role }}</span></template>
                <span v-else class="empty">—</span>
              </div>
            </td>
            <td v-else-if="column.id === 'place'" class="c-place"><div class="cell" :data-full="fullOf(c, 'place')" @pointerover="tipIfCut"><span v-if="placeOf(c)" class="text">{{ placeOf(c) }}</span><span v-else class="empty">—</span></div></td>
            <td v-else-if="column.id === 'industry'" class="c-industry"><div class="cell" :data-full="fullOf(c, 'industry')" @pointerover="tipIfCut"><span v-if="c.industry" class="text">{{ c.industry }}</span><span v-else class="empty">—</span></div></td>
            <td v-else class="c-rate end"><div class="cell"><span v-if="money(c)" class="mono">{{ money(c) }}</span><span v-else class="empty">—</span></div></td>
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
.customers { width: 100%; border-collapse: separate; border-spacing: 0; table-layout: fixed; font-size: 13.5px; }
.customers:focus-visible { box-shadow: none; }
.customers:focus-visible .row.cursor { outline: 2px solid var(--teal); outline-offset: -2px; }
thead th {
  position: sticky; top: 0; z-index: 2; height: 36px; padding: 0 12px; text-align: left; font-weight: 500;
  background: var(--surface-raised-2); border-bottom: 1px solid var(--line-2);
}
thead th:first-child { padding-left: 18px; }
thead th.end { text-align: right; }
thead th.end .th-sort { flex-direction: row-reverse; }
.th-sort { display: inline-flex; align-items: center; gap: 6px; height: 26px; margin: 0 -6px; padding: 0 6px; border: 0; border-radius: 6px; background: transparent; font: 500 10.5px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; white-space: nowrap; }
.th-sort:hover { color: var(--ink); background: var(--row-hover); }
.th-sort.on { color: var(--teal-ink); }
.th-sort:focus-visible { box-shadow: var(--focus-ring); }
.sort-mark { display: inline-flex; align-items: center; min-width: 11px; }
.col-resize { position: absolute; top: 6px; bottom: 6px; right: 0; z-index: 3; width: 9px; cursor: col-resize; touch-action: none; outline: none; }
.col-resize::after { content: ''; position: absolute; top: 0; bottom: 0; left: 8px; width: 1px; background: var(--line-2); opacity: 0; transition: opacity .12s ease; }
thead th:hover .col-resize::after { opacity: 1; }
.col-resize:hover::after, .col-resize:active::after, .col-resize:focus-visible::after { opacity: 1; width: 2px; left: 7px; background: var(--teal); }

.row { height: 44px; cursor: pointer; scroll-margin-top: 44px; }
.row td { height: 44px; padding: 0 12px; border-bottom: 1px solid var(--line); vertical-align: middle; }
.row td:first-child { padding-left: 18px; }
tbody .row:last-child td { border-bottom: 0; }
.cell { display: flex; align-items: center; gap: 8px; min-width: 0; line-height: 18px; white-space: nowrap; }
/* The secondary value (legal name, role) drops out whole before the main one is
   cut: what does not fit wraps to a second line the cell never shows. The primary
   text is capped so it stays on the first line with its icon and ellipsizes. */
.cell.drop { flex-wrap: wrap; align-content: flex-start; row-gap: 24px; height: 24px; overflow: hidden; }
/* One line is as tall as its tallest item (the 20px avatar here, the 24px mark by the name). */
.c-contact .cell.drop { height: 20px; row-gap: 20px; }
.end .cell { justify-content: flex-end; }
@media (hover: hover) { .row:hover td { background: var(--row-hover); } }
.row.cursor td { background: var(--row-selected); }
tbody.dim { opacity: .55; }
.org-mark { display: grid; place-items: center; flex-shrink: 0; width: 24px; height: 24px; border-radius: 7px; background: var(--code-bg); color: var(--ink-2); }
.row.cursor .org-mark { background: var(--chip-teal-bg); color: var(--teal-ink); }
/* Row actions float over the end of the name cell, shown on hover, on the cursor
   row and while one of them has focus; the name fades out beneath them. */
td.c-name { position: relative; }
.row-actions { position: absolute; top: 50%; right: 8px; display: inline-flex; gap: 2px; transform: translateY(-50%); visibility: hidden; }
.row-actions .icon-btn { width: 26px; height: 26px; color: var(--ink-3); }
@media (hover: hover) { .row-actions .icon-btn:hover { color: var(--teal-ink); } }
.row.cursor .row-actions, .row-actions:focus-within { visibility: visible; }
@media (hover: hover) { .row:hover .row-actions { visibility: visible; } }
.row.cursor td.c-name .cell, td.c-name:focus-within .cell { -webkit-mask-image: linear-gradient(to left, transparent 60px, #000 88px); mask-image: linear-gradient(to left, transparent 60px, #000 88px); }
@media (hover: hover) { .row:hover td.c-name .cell { -webkit-mask-image: linear-gradient(to left, transparent 60px, #000 88px); mask-image: linear-gradient(to left, transparent 60px, #000 88px); } }
.row.cursor td.c-name:has(.solo) .cell, td.c-name:has(.solo):focus-within .cell { -webkit-mask-image: linear-gradient(to left, transparent 32px, #000 60px); mask-image: linear-gradient(to left, transparent 32px, #000 60px); }
@media (hover: hover) { .row:hover td.c-name:has(.solo) .cell { -webkit-mask-image: linear-gradient(to left, transparent 32px, #000 60px); mask-image: linear-gradient(to left, transparent 32px, #000 60px); } }
.archived-chip { flex: 0 0 auto; height: 18px; padding: 0 7px; border-radius: 999px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-2); font: 600 10px/18px var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.row.archived .name-link { color: var(--ink-2); }
.card-row .archived-chip { justify-self: start; }
/* 32px keeps the 24px mark and the 8px gap on this line; the name ellipsizes beside them. */
.name-link { flex: 0 1 auto; min-width: 0; max-width: calc(100% - 32px); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-weight: 600; text-decoration: none; }
.name-link:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
.legal { flex: 0 0 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--ink-3); font-size: 12.5px; }
.number { padding: 2px 7px; border-radius: 6px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink); font: 500 12px/16px var(--mono); font-variant-ligatures: none; }
.empty { color: var(--ink-3); font-size: 12.5px; }
/* 28px keeps the 20px avatar and the 8px gap on this line. */
.person { flex: 0 1 auto; min-width: 0; max-width: calc(100% - 28px); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); }
.role { flex: 0 0 auto; color: var(--ink-3); font-size: 12.5px; }
.text { overflow: hidden; text-overflow: ellipsis; color: var(--ink-2); }
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
.card-link { display: grid; gap: 4px; padding: 12px 16px; color: inherit; text-decoration: none; }
.card-row.cursor .card-link { background: var(--row-selected); }
.card-link:focus-visible { box-shadow: inset var(--focus-ring); }
.card-top { display: flex; align-items: center; gap: 8px; min-width: 0; }
.card-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 15px; font-weight: 650; color: var(--ink); }
.card-sub { font-size: 12.5px; color: var(--ink-3); overflow-wrap: anywhere; }
.card-meta { display: flex; flex-wrap: wrap; gap: 4px 12px; margin-top: 2px; }
.meta-item { display: inline-flex; align-items: center; gap: 5px; min-width: 0; font-size: 12.5px; color: var(--ink-2); overflow-wrap: anywhere; }
.meta-item svg { flex-shrink: 0; color: var(--ink-3); }
</style>
