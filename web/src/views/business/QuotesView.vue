<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { MutationReceipt } from '../../lib/crm'
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { minorMoney } from '../../lib/crm'
import { usePreference } from '../../lib/preferences'
import { settingsLink } from '../../lib/settings'
import { DEFAULT_SORT, STATUSES, STATUS_META, amountActive, blankFilter, dateActive, firstDir, matchesQuote, narrowed, sortQuotes, statusOf, todayIso, type ColumnId, type QuoteRow, type Sort } from '../../lib/quotes/list'
import { acceptanceNotices, branchQuote, deleteQuote, duplicateQuote, finalizeQuote, getLink, getVersion, issueConfirm, issueError, lifecycleError, linkState, linkUrl, reviseConfirm, setArchived, undoQuote, type AcceptanceNotice, type QuoteProjection } from '../../lib/quotes/lifecycle'
import { getDraft } from '../../lib/quotes/api'
import { quoteActions, type LinkState, type QuoteActionId } from '../../lib/quotes/actions'
import { confirmAction } from '../../lib/confirm'
import { opensRowMenu, type RowMenuAnchor } from '../../lib/rowActions'
import { toast } from '../../lib/toast'
import { plural } from '../../lib/work'
import { useBusiness } from '../../stores/business'
import { useQuotes } from '../../stores/quotes'
import AppIcon from '../../components/AppIcon.vue'
import BizIcon from '../../components/business/BizIcon.vue'
import BusinessPage from '../../components/business/BusinessPage.vue'
import ChoiceFacet from '../../components/business/ChoiceFacet.vue'
import QuoteCreateDialog from '../../components/business/QuoteCreateDialog.vue'
import PanelSplitter from '../../components/PanelSplitter.vue'
import AmountFacet from '../../components/quotes/list/AmountFacet.vue'
import DateFacet from '../../components/quotes/list/DateFacet.vue'
import QuoteTable from '../../components/quotes/list/QuoteTable.vue'
import RowMenu from '../../components/business/RowMenu.vue'

const QuoteWorkspace = defineAsyncComponent(() => import('../../components/business/QuoteWorkspace.vue'))
type QuoteWorkspaceInstance = InstanceType<typeof import('../../components/business/QuoteWorkspace.vue')['default']>

// Business › Quotes: every quote in one list you can search, filter (status,
// customer, date, amount), sort and size, with the ticket list's keyboard (j/k,
// Enter, / and n). A quote opens docked beside the list, which keeps its place;
// from there it opens on its own page with the same session.
const business = useBusiness()
const store = useQuotes()
const route = useRoute()
const router = useRouter()
const pref = usePreference<{ widths?: Partial<Record<ColumnId, number>>; sort?: Sort }>('quotes')
const sort = computed<Sort>(() => pref.value.value?.sort ?? DEFAULT_SORT)
const widths = computed(() => pref.value.value?.widths ?? {})
const search = ref<HTMLInputElement>()
const table = ref<InstanceType<typeof QuoteTable>>()
const workspace = ref<QuoteWorkspaceInstance>()
const create = ref<InstanceType<typeof QuoteCreateDialog>>()
const filter = computed(() => store.filter)
const today = todayIso()
const creatorNotices = ref<AcceptanceNotice[]>([])
const noticesReady = ref(false)
async function loadCreatorNotices() {
  try { creatorNotices.value = await acceptanceNotices(true) }
  catch { creatorNotices.value = [] }
  finally { noticesReady.value = true }
}
const noticeName = (id: string) => all.value.find(q => q.quote_node_id === id)?.offer_no || 'Quote'
const noticeTime = (at: string) => new Intl.DateTimeFormat('en-GB', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(at))

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const openId = computed(() => typeof route.query.quote === 'string' && UUID.test(route.query.quote) ? route.query.quote : null)
const all = computed(() => store.items ?? [])
// The first load shows the list together with the accepted-offers notice, so the
// table does not jump down when the notice lands a moment after the quotes.
const settled = ref(!!store.items)
const items = computed(() => settled.value ? store.items : null)
const live = computed(() => all.value.filter(q => !q.archived))
const rows = computed(() => sortQuotes(all.value.filter(q => matchesQuote(q, filter.value, today)), sort.value))
const archivedCount = computed(() => all.value.length - live.value.length)
const isNarrowed = computed(() => narrowed(filter.value))
const statusOptions = computed(() => STATUSES.map(s => ({ value: s, label: STATUS_META[s].label, state: s, count: live.value.filter(q => statusOf(q) === s).length })).filter(o => o.count || filter.value.statuses.includes(o.value)))
const customerOptions = computed(() => {
  const names = new Map<string, { label: string; count: number }>()
  for (const q of live.value) { const known = names.get(q.customer_org_node_id); names.set(q.customer_org_node_id, { label: q.customer_name || 'A customer', count: (known?.count ?? 0) + 1 }) }
  return [...names].map(([value, o]) => ({ value, label: o.label, count: o.count })).sort((a, b) => a.label.localeCompare(b.label))
})
// Issued and still valid: what the customer can accept now, summed per currency.
const awaiting = computed(() => {
  const by = new Map<string, number>()
  for (const q of live.value) if (statusOf(q) === 'issued' && q.net_total_cents !== undefined) by.set(q.currency ?? '', (by.get(q.currency ?? '') ?? 0) + q.net_total_cents)
  return [...by].sort(([a], [b]) => a.localeCompare(b)).map(([currency, cents]) => minorMoney(cents, currency))
})
const issuedCount = computed(() => live.value.filter(q => statusOf(q) === 'issued').length)

function setSort(key: ColumnId) {
  const dir = sort.value.key === key ? (sort.value.dir === 'asc' ? 'desc' : 'asc') : firstDir(key)
  pref.save({ ...(pref.value.value ?? {}), sort: { key, dir } })
}
function setWidths(next: Partial<Record<ColumnId, number>>) { pref.save({ ...(pref.value.value ?? {}), widths: next }) }
function toggle(list: 'statuses' | 'customers', value: string) {
  const current = store.filter[list] as string[]
  store.filter = { ...store.filter, [list]: current.includes(value) ? current.filter(v => v !== value) : [...current, value] }
}
function clearFilters() { store.filter = { ...blankFilter(), archived: store.filter.archived } }

// ---------- Opening: docked beside the list, or on its own page ----------
const phoneQuery = window.matchMedia('(max-width: 720px)')
function open(row: Pick<QuoteRow, 'quote_node_id'>, focus = false) {
  store.cursor = row.quote_node_id
  if (phoneQuery.matches) { void router.push(`/business/quotes/${encodeURIComponent(row.quote_node_id)}`); return }
  const location = { path: '/business/quotes', query: { ...route.query, quote: row.quote_node_id } }
  if (openId.value) void router.replace(location); else void router.push(location)
  if (focus) void nextTick(() => setTimeout(() => workspace.value?.focus(), 60))
}
function openFull(id: string) { store.cursor = id; void router.push(`/business/quotes/${encodeURIComponent(id)}`) }
function closeDock() {
  if (!openId.value) return
  const { quote: _quote, ...rest } = route.query
  void router.replace({ path: '/business/quotes', query: rest })
  void nextTick(() => table.value?.focus())
}
function openCreate() { if (business.staff) create.value?.open(store.filter.customers.length === 1 ? { customerId: store.filter.customers[0], customerName: customerOptions.value.find(c => c.value === store.filter.customers[0])?.label } : {}) }
function created(quote: QuoteProjection) {
  toast(`Created ${quote.offer_no ?? 'a new quote'}. Write it on the page.`)
  void store.load(true)
  openFull(quote.quote_node_id)
}

// ---------- Row actions: inline on hover and focus, all of them in the … menu ----------
const menu = ref<{ row: QuoteRow; anchor: RowMenuAnchor; link: LinkState; url: string; narrow: boolean } | null>(null)
const busy = ref<QuoteActionId | null>(null)
const menuItems = computed(() => menu.value ? quoteActions(menu.value.row, { admin: business.admin, staff: business.staff, link: menu.value.link, busy: busy.value, narrow: menu.value.narrow }) : [])
let opener: HTMLElement | null = null
async function openMenu(row: QuoteRow, anchor: RowMenuAnchor) {
  // The … button toggles its menu.
  if (menu.value && menu.value.anchor === anchor) { menu.value = null; return }
  opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
  store.cursor = row.quote_node_id
  menu.value = { row, anchor, link: 'loading', url: '', narrow: matchMedia('(max-width: 720px)').matches }
  // The customer link is read when the menu opens: copy it, or create one.
  if (!business.admin || row.state === 'draft' || row.current_version < 1) return
  try {
    const link = await getLink(row.quote_node_id, row.current_version)
    if (menu.value?.row.quote_node_id === row.quote_node_id) menu.value = { ...menu.value, link: linkState(link), url: link ? linkUrl(link) : '' }
  } catch { if (menu.value?.row.quote_node_id === row.quote_node_id) menu.value = { ...menu.value, link: 'error' } }
}
// Focus goes back where the menu was opened from: the … button, or the list.
function closeMenu(restore: boolean) {
  menu.value = null
  if (!restore) return
  void nextTick(() => { if (opener?.isConnected && opener.checkVisibility()) opener.focus(); else table.value?.focus() })
}
const nameOf = (row: QuoteRow) => row.offer_no || row.title || 'The quote'
const failed = (e: unknown, fallback: string) => toast(lifecycleError(e, fallback), { tone: 'error' })
const undoing = (receipt: MutationReceipt, after: () => void) => () => {
  void undoQuote(receipt).then(async () => { await store.load(true); after() }).catch(e => toast(e instanceof Error ? e.message : 'Undo did not work.', { tone: 'error' }))
}
async function copyText(text: string, done: string) {
  try { await navigator.clipboard.writeText(text); toast(done) } catch { toast('Copying did not work here.', { tone: 'error' }) }
}
async function act(row: QuoteRow, id: QuoteActionId) {
  const state = menu.value?.row.quote_node_id === row.quote_node_id ? menu.value : null
  if (id !== 'link') menu.value = null
  const docked = openId.value === row.quote_node_id ? workspace.value : null
  switch (id) {
    case 'open': open(row, true); return
    case 'openPage': openFull(row.quote_node_id); return
    case 'copyNumber': await copyText(row.offer_no ?? '', `Copied ${row.offer_no}.`); return
    case 'pdf': void router.push({ path: `/business/quotes/${encodeURIComponent(row.quote_node_id)}`, query: { print: '1' } }); return
    case 'link': {
      if (state?.link === 'copy' && state.url) { menu.value = null; await copyText(state.url, `Copied the customer link of ${nameOf(row)}.`); return }
      // Details keeps a one-time link visible when the server has no host key.
      // Creating it from this transient menu could lose the only copy.
      menu.value = null
      open(row, true)
      toast(`Create the customer link for ${nameOf(row)} in Details so you can save it.`)
      return
    }
    case 'duplicate': {
      try {
        const copy = await duplicateQuote(row.quote_node_id, row.revision)
        await store.load(true)
        store.cursor = copy.quote_node_id
        toast(`Duplicated ${nameOf(row)} as ${copy.offer_no ?? 'a new draft'}.`, {
          actions: [
            { label: 'Open', run: () => open({ quote_node_id: copy.quote_node_id }, true) },
            { label: 'Undo', run: undoing(copy, () => { if (openId.value === copy.quote_node_id) closeDock() }) },
          ],
          timeout: 8000,
        })
      } catch (e) { failed(e, 'The quote was not duplicated.') }
      return
    }
    case 'issue': {
      if (docked) { await docked.issue(); await store.load(true); return }
      if (!await confirmAction(issueConfirm(row.offer_no ?? '', row.current_version + 1))) return
      try {
        const draft = await getDraft(row.quote_node_id)
        await finalizeQuote(row.quote_node_id, { expected_quote_revision: row.revision, expected_draft_revision: draft.draft_revision, expected_document_sha256: draft.document_sha256 })
        await store.load(true)
        toast(`Issued ${nameOf(row)} as version ${row.current_version + 1}. See its customer link in Details.`, { action: { label: 'Open', run: () => open(row, true) } })
      } catch (e) { toast(issueError(e), { tone: 'error' }); void store.load(true) }
      return
    }
    case 'revise': {
      if (docked) { await docked.revise(); await store.load(true); return }
      if (!await confirmAction(reviseConfirm(row.current_version, row.state === 'accepted'))) return
      try {
        const version = await getVersion(row.quote_node_id, row.current_version)
        await branchQuote(row.quote_node_id, { expected_quote_revision: row.revision, expected_version: row.current_version, expected_content_sha256: version.content_sha256 })
        await store.load(true)
        toast(`You are revising ${nameOf(row)}. Issue it when it is ready.`)
        openFull(row.quote_node_id)
      } catch (e) { failed(e, 'The quote could not be revised. Nothing changed.'); void store.load(true) }
      return
    }
    case 'archive': case 'restore': {
      const archiving = id === 'archive'
      try {
        const receipt = await setArchived(row.quote_node_id, row.revision, archiving)
        await store.load(true)
        toast(archiving ? `Archived ${nameOf(row)}. It keeps its versions, evidence and files.` : `${nameOf(row)} is back in the list.`, {
          action: { label: 'Undo', run: undoing(receipt, () => {}) }, timeout: 8000,
        })
      } catch (e) { failed(e, 'That did not work.'); void store.load(true) }
      return
    }
    case 'delete': {
      try {
        const receipt = await deleteQuote(row.quote_node_id, row.revision)
        if (openId.value === row.quote_node_id) closeDock()
        if (store.items) store.items = store.items.filter(q => q.quote_node_id !== row.quote_node_id)
        toast(`Deleted draft ${nameOf(row)}.`, { action: { label: 'Undo', run: undoing(receipt, () => { store.cursor = row.quote_node_id }) }, timeout: 8000 })
      } catch (e) { failed(e, 'The draft was not deleted.'); void store.load(true) }
    }
  }
}

// ---------- Keyboard: j/k or arrows move, Enter opens, / searches, n adds ----------
function typing(target: EventTarget | null) {
  return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
}
function move(step: number) {
  const list = rows.value
  if (!list.length) return
  const index = list.findIndex(q => q.quote_node_id === store.cursor)
  const next = index === -1 ? (step > 0 ? 0 : list.length - 1) : Math.max(0, Math.min(list.length - 1, index + step))
  store.cursor = list[next]!.quote_node_id
}
function inDock(target: EventTarget | null) { return target instanceof Node && !!document.querySelector('.quote-dock')?.contains(target) }
function keys(event: KeyboardEvent) {
  if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || document.querySelector('dialog[open], .floating') || inDock(event.target)) return
  if (typing(event.target)) {
    if (event.target === search.value && (event.key === 'ArrowDown' || event.key === 'Enter') && rows.value.length) {
      event.preventDefault()
      if (event.key === 'Enter' && rows.value.length === 1) { open(rows.value[0]!, true); return }
      if (!rows.value.some(q => q.quote_node_id === store.cursor)) store.cursor = rows.value[0]!.quote_node_id
      table.value?.focus()
    } else if (event.target === search.value && event.key === 'Escape') {
      event.preventDefault()
      if (store.filter.q) store.filter = { ...store.filter, q: '' }
      else search.value?.blur()
    }
    return
  }
  const key = event.key
  if (key === 'j' || key === 'ArrowDown') { event.preventDefault(); move(1) }
  else if (key === 'k' || key === 'ArrowUp') { event.preventDefault(); move(-1) }
  else if (key === 'Home') { event.preventDefault(); move(-Infinity) }
  else if (key === 'End') { event.preventDefault(); move(Infinity) }
  else if ((key === 'Enter' || key === 'o') && store.cursor && !(event.target as HTMLElement).closest?.('a, button, [role="separator"]')) {
    const row = rows.value.find(q => q.quote_node_id === store.cursor)
    if (!row) return
    event.preventDefault()
    if (event.shiftKey) openFull(row.quote_node_id); else open(row, true)
  } else if (key === 'Escape' && openId.value) { event.preventDefault(); closeDock() }
  else if (opensRowMenu(event) && store.cursor) {
    // The context-menu key or Shift+F10 opens the cursor row's menu at its … button.
    const row = rows.value.find(q => q.quote_node_id === store.cursor)
    if (!row) return
    event.preventDefault()
    void openMenu(row, table.value?.moreButton(row.quote_node_id) ?? { x: innerWidth / 2, y: innerHeight / 3 })
  }
  else if (key === '/') { event.preventDefault(); search.value?.focus(); search.value?.select() }
  else if (key === 'n' && business.staff) { event.preventDefault(); openCreate() }
}

// The list keeps its scroll when you come back from a quote's own page.
const scroller = () => document.getElementById('main')
onMounted(async () => {
  window.addEventListener('keydown', keys)
  await business.loadPlugins()
  if (business.open.quotes) { await Promise.all([store.load(true), loadCreatorNotices()]); settled.value = true; await nextTick(); const el = scroller(); if (el && store.scroll) el.scrollTop = store.scroll }
})
onBeforeUnmount(() => { window.removeEventListener('keydown', keys); store.scroll = scroller()?.scrollTop ?? 0 })
watch(() => business.open.quotes, on => { if (on) void Promise.all([store.load(true), loadCreatorNotices()]).then(() => { settled.value = true }) })
watch(openId, (id, old) => { if (!id && old) void loadCreatorNotices() })
watch(openId, id => { if (id) store.cursor = id })
</script>

<template>
  <div class="quotes-view" :class="{ docked: !!openId }">
    <BusinessPage title="Quotes" area="quotes" :panel-open="!!openId" class="quotes-list-page">
      <template #summary>
        <span v-if="items && live.length" class="dot-list">
          <span>{{ plural(live.length, 'quote') }}</span>
          <span v-if="issuedCount">{{ issuedCount }} waiting for the customer</span>
          <span v-for="sum in awaiting" :key="sum"><b>{{ sum }}</b> to accept</span>
        </span>
        <span v-else-if="items">No quotes yet</span>
        <span v-else-if="store.error">Quotes could not be loaded</span>
        <span v-else class="summary-loading"><span class="skeleton summary-skeleton" /><span class="skeleton summary-skeleton line2" /></span>
      </template>
      <template v-if="business.staff" #actions>
        <RouterLink v-if="business.admin" class="btn ghost" :to="settingsLink('business', 'quotes')" data-tip="Sender, texts and numbering for new quotes"><AppIcon name="gear" :size="14" />Quote settings</RouterLink>
        <button type="button" class="btn primary" aria-keyshortcuts="n" data-tip="New quote · n" @click="openCreate"><AppIcon name="plus" :size="14" />New quote</button>
      </template>

      <template v-if="noticesReady && (store.items || store.error)">
      <div class="toolbar" role="search">
        <label class="search-field list-search">
          <AppIcon name="search" :size="14" />
          <input ref="search" v-model="store.filter.q" class="field" type="search" placeholder="Find a quote, number or customer" aria-label="Find a quote" aria-keyshortcuts="/" autocomplete="off" />
          <kbd class="keycap slash" aria-hidden="true">/</kbd>
        </label>
        <div class="facets">
          <ChoiceFacet label="Status" :options="statusOptions" :selected="filter.statuses" @toggle="v => toggle('statuses', v)" @clear="store.filter = { ...store.filter, statuses: [] }" />
          <ChoiceFacet v-if="customerOptions.length" label="Customer" :options="customerOptions" :selected="filter.customers" @toggle="v => toggle('customers', v)" @clear="store.filter = { ...store.filter, customers: [] }" />
          <DateFacet :value="filter.date" :today="today" @change="v => store.filter = { ...store.filter, date: v }" />
          <AmountFacet :value="filter.amount" @change="v => store.filter = { ...store.filter, amount: v }" />
          <button v-if="archivedCount || filter.archived" type="button" class="btn sm facet-toggle" :aria-pressed="filter.archived" @click="store.filter = { ...store.filter, archived: !filter.archived }">
            <AppIcon name="archive" :size="13" />Archived<span class="toggle-count">{{ archivedCount }}</span>
          </button>
          <button v-if="isNarrowed" type="button" class="btn sm ghost" @click="clearFilters">Clear</button>
        </div>
        <span class="spacer" />
        <p v-if="items && (isNarrowed || filter.archived)" class="count" role="status">{{ rows.length }} of {{ filter.archived ? all.length : live.length }}</p>
      </div>

      <details v-if="items && creatorNotices.length" class="acceptance-notices">
        <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Recently accepted offers you created <span>{{ creatorNotices.length }}</span></summary>
        <ol>
          <li v-for="notice in creatorNotices" :key="`${notice.quote_node_id}:${notice.version}`">
            <RouterLink :to="`/business/quotes/${notice.quote_node_id}`">{{ noticeName(notice.quote_node_id) }} · version {{ notice.version }}</RouterLink>
            <span>Accepted {{ noticeTime(notice.accepted_at) }}</span>
          </li>
        </ol>
      </details>
      <div v-if="store.error && !items" class="state glass-card" role="alert">
        <span class="state-icon danger"><AppIcon name="alert" :size="18" /></span>
        <h2>{{ store.status === 403 ? 'Quotes are not open to you' : 'Quotes could not be loaded' }}</h2>
        <p>{{ store.status === 403 ? 'Only admins and members of this workspace see its quotes. A workspace admin can change your role.' : 'The list did not load this time. Nothing is lost; try again in a moment.' }}</p>
        <p v-if="store.status !== 403 && store.error" class="state-detail">The server said: {{ store.error }}</p>
        <button v-if="store.status !== 403" type="button" class="btn" @click="store.load(true)"><AppIcon name="refresh" :size="14" />Try again</button>
      </div>
      <div v-else-if="items && !all.length" class="state glass-card">
        <span class="state-icon"><BizIcon name="document" :size="18" /></span>
        <h2>No quotes yet</h2>
        <p v-if="business.staff">Write a quote for a customer: it starts from your sender and texts, and gets its number when you create it. You issue it when it is ready and share it with a link.</p>
        <p v-else>Quotes appear here once someone writes one.</p>
        <div v-if="business.staff" class="state-actions">
          <button type="button" class="btn primary" @click="openCreate"><AppIcon name="plus" :size="14" />New quote</button>
          <RouterLink class="btn ghost" to="/business/customers">Customers</RouterLink>
        </div>
      </div>
      <QuoteTable
        v-else ref="table" :key="items ? 'rows' : 'loading'" :rows="items ? rows : []" :loading="!items" :query="filter.q" :sort="sort" :cursor-id="store.cursor" :open-id="openId" :menu-id="menu?.row.quote_node_id ?? null" :can-duplicate="business.staff" :widths="widths" :today="today"
        @sort="setSort" @cursor="id => store.cursor = id" @open="row => open(row)" @widths="setWidths" @grid-focus="() => { if (!store.cursor && rows.length) store.cursor = rows[0]!.quote_node_id }"
        @action="(row, id) => act(row, id)" @menu="(row, anchor) => openMenu(row, anchor)"
      >
        <div v-if="items && all.length && !rows.length" class="state inline">
          <span class="state-icon"><AppIcon name="search" :size="18" /></span>
          <h2>{{ filter.q.trim() ? `No quote matches “${filter.q.trim()}”` : 'No quote matches these filters' }}</h2>
          <p>Search looks at numbers, titles, customers, project references and status.<template v-if="amountActive(filter)"> Quotes without an amount are left out while an amount is set.</template><template v-if="dateActive(filter)"> The date is the one on the quote.</template></p>
          <div class="state-actions">
            <button type="button" class="btn" @click="clearFilters">Clear search and filters</button>
            <button v-if="!filter.archived && archivedCount" type="button" class="btn ghost" @click="store.filter = { ...store.filter, archived: true }">Include archived</button>
          </div>
        </div>
      </QuoteTable>
      <p v-if="items && rows.length" class="keys-hint dot-list" aria-hidden="true">
        <span><kbd class="keycap">j</kbd><kbd class="keycap">k</kbd> move</span><span><kbd class="keycap"><AppIcon name="enter" /></kbd> open</span><span><kbd class="keycap">shift</kbd><kbd class="keycap"><AppIcon name="enter" /></kbd> own page</span><span><kbd class="keycap">shift</kbd><kbd class="keycap">F10</kbd> actions</span><span><kbd class="keycap">/</kbd> search</span><span v-if="business.staff"><kbd class="keycap">n</kbd> new quote</span>
      </p>
      </template>
      <div v-else class="quote-list-placeholder" aria-hidden="true"><span class="skeleton" /><span class="skeleton" /><span class="skeleton" /></div>
    </BusinessPage>
    <PanelSplitter v-if="openId" class="quote-panel-splitter early" field="quotePanel" css-var="--quote-panel-user-w" target=".quote-dock" :reserve="480" />
    <div v-if="openId" class="quote-dock">
      <QuoteWorkspace ref="workspace" :quote-id="openId" layout="dock" @close="closeDock" @expand="openFull(openId)" @open="id => open({ quote_node_id: id })" />
    </div>
    <QuoteCreateDialog ref="create" @created="created" />
    <RowMenu v-if="menu" :anchor="menu.anchor" :items="menuItems" :label="`Actions for ${menu.row.offer_no || menu.row.title || 'the quote'}`" @select="id => act(menu!.row, id as QuoteActionId)" @close="closeMenu" />
  </div>
</template>

<style scoped>
/* Quotes dock a little wider than tickets (a page of paper needs the room), with
   a width of its own: dragged, it is the person's, and the list always keeps
   480px of the window, so the two share it without a gap. */
.quotes-view { --panel-default: clamp(560px, calc(640px + (100vw - 1440px) * .4), 48vw); --panel-w: min(var(--quote-panel-user-w, var(--panel-default)), 72vw, calc(100vw - 480px)); }
.summary-skeleton { display: inline-block; width: 220px; }
.quote-list-placeholder { display: grid; align-content: start; gap: 14px; min-height: 400px; padding-top: 10px; }
.quote-list-placeholder .skeleton { width: min(100%, 620px); height: 36px; }
.quote-list-placeholder .skeleton:first-child { width: min(100%, 300px); height: 44px; }
.quote-list-placeholder .skeleton:last-child { height: 220px; }
/* Phones wrap the summary to two lines; the loading line holds both. */
.summary-loading { display: inline-block; }
.summary-skeleton.line2 { display: none; }
@media (max-width: 600px) {
  .summary-loading { display: grid; gap: 10px; padding: 5px 0; }
  .summary-skeleton, .summary-skeleton.line2 { display: block; }
  .summary-skeleton.line2 { width: 60%; }
}
.acceptance-notices { min-width: 0; margin: 0 0 12px; padding: 9px 12px; border-radius: 10px; background: var(--surface-raised-2); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink); }
.acceptance-notices summary { width: fit-content; cursor: pointer; font-size: 13px; font-weight: 600; }
.acceptance-notices summary:focus-visible { border-radius: 4px; box-shadow: var(--focus-ring); }
.acceptance-notices summary span { color: var(--ink-2); font-variant-numeric: tabular-nums; }
.acceptance-notices ol { display: grid; gap: 4px; max-height: 210px; overflow: auto; margin: 8px 0 0; padding: 0; list-style: none; }
.acceptance-notices li { display: flex; flex-wrap: wrap; gap: 3px 12px; min-width: 0; padding: 4px 0; font-size: 12.5px; }
.acceptance-notices a { color: var(--teal-ink); font-weight: 600; overflow-wrap: anywhere; }
.acceptance-notices li span { color: var(--ink-2); }
.toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 10px 12px; min-height: 48px; margin-bottom: 12px; }
.list-search { width: 300px; }
.list-search .field { height: 32px; padding-right: 34px; font-size: 13.5px; }
.slash { position: absolute; right: 8px; pointer-events: none; }
.list-search:focus-within .slash { display: none; }
.facets { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; min-width: 0; }
.facet-toggle { gap: 6px; padding: 0 10px; font-weight: 600; color: var(--ink-2); }
.facet-toggle svg { color: var(--ink-3); }
.toggle-count { font: 600 11px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.spacer { flex: 1; }
.count { font-size: 12.5px; color: var(--ink-2); font-variant-numeric: tabular-nums; }
.state { display: grid; justify-items: center; gap: 8px; max-width: 620px; margin: 8px auto 0; padding: 44px 28px; text-align: center; }
.state.inline { max-width: none; margin: 0; padding: 40px 24px 36px; border-top: 1px solid var(--line); }
.state h2 { font-size: 17px; text-wrap: balance; overflow-wrap: anywhere; }
.state p { max-width: 52ch; font-size: 13.5px; color: var(--ink-2); }
/* What the server said, for whoever helps: small and quiet under the plain words. */
.state .state-detail { font: 500 11.5px/1.5 var(--mono); color: var(--ink-3); font-variant-ligatures: none; }
.state-actions { display: flex; flex-wrap: wrap; justify-content: center; gap: 8px; margin-top: 8px; }
.state-icon { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 4px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.state-icon.danger { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.keys-hint { width: fit-content; max-width: 100%; align-items: center; margin: 10px 0 0 auto; font-size: 12px; color: var(--ink-3); }
.keys-hint > span { display: inline-flex; align-items: center; gap: 4px; }
/* The docked quote: a glass panel at the right, like a ticket's, with the whole workspace inside. */
.quote-dock {
  position: fixed; z-index: 15; top: calc(var(--header-h) + 10px); right: 10px; bottom: calc(var(--footer-h) + 10px); width: min(640px, calc(100vw - 20px));
  display: flex; flex-direction: column; border-radius: var(--radius); border: 1px solid var(--glass-edge); overflow: hidden;
  background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow);
}
.quote-dock > * { flex: 1; min-height: 0; }
@media (min-width: 1100px) { .quote-dock { width: var(--panel-w); } }
/* Between 900 and 1100px the list and the quote still split the window. */
@media (min-width: 900px) and (max-width: 1099px) {
  .quote-dock { width: var(--panel-w); }
  .quotes-list-page.panel-open { max-width: none; margin: 0; padding-right: calc(var(--panel-w) + 22px); }
}
@media (prefers-reduced-motion: no-preference) {
  .quote-dock { animation: dock-in .22s cubic-bezier(.2, .7, .2, 1); }
  @keyframes dock-in { from { opacity: 0; transform: translateX(24px); } to { opacity: 1; transform: none; } }
}
@media (max-width: 720px) {
  .toolbar { gap: 8px; }
  .list-search { flex: 1 1 100%; width: auto; }
  .list-search .field { height: 44px; font-size: 16px; }
  .slash { display: none; }
  .facets { flex: 1 1 100%; row-gap: 8px; }
  .facets :deep(.btn) { height: 44px; }
  .keys-hint { display: none; }
  .state { padding: 32px 18px; }
}
</style>
