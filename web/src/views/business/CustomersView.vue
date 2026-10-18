<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import '../../styles/crm.css'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { NO_FILTER, countryOf, customerActions, errorText, facetOf, filtered, matchesCustomer, setCustomerArchived, sortCustomers, undoEvents, type ColumnId, type Customer, type CustomerActionId, type SortKey } from '../../lib/crm'
import { opensRowMenu, type RowMenuAnchor } from '../../lib/rowActions'
import type { QuoteProjection } from '../../lib/quotes/lifecycle'
import { usePreference } from '../../lib/preferences'
import { toast } from '../../lib/toast'
import { plural } from '../../lib/work'
import { useBusiness } from '../../stores/business'
import { useCustomers } from '../../stores/customers'
import AppIcon from '../../components/AppIcon.vue'
import BizIcon from '../../components/business/BizIcon.vue'
import BusinessPage from '../../components/business/BusinessPage.vue'
import ChoiceFacet from '../../components/business/ChoiceFacet.vue'
import CustomerCreateDialog from '../../components/crm/CustomerCreateDialog.vue'
import CustomerTable from '../../components/crm/CustomerTable.vue'
import QuoteCreateDialog from '../../components/business/QuoteCreateDialog.vue'
import RowMenu from '../../components/business/RowMenu.vue'
import IntegrationCard from '../../components/crm/IntegrationCard.vue'

// Business › Customers: every customer in one list you can search, sort and
// filter, with the keyboard of the ticket list (j/k, Enter, / and n). Admins add
// customers here; the number comes with the first quote.
const business = useBusiness()
const store = useCustomers()
const router = useRouter()
const pref = usePreference<{ widths?: Partial<Record<ColumnId, number>>; sort?: { key: SortKey; dir: 'asc' | 'desc' } }>('customers')
const sort = computed(() => pref.value.value?.sort ?? { key: 'name' as SortKey, dir: 'asc' as const })
const widths = computed(() => pref.value.value?.widths ?? {})
const search = ref<HTMLInputElement>()
const table = ref<InstanceType<typeof CustomerTable>>()
const create = ref<InstanceType<typeof CustomerCreateDialog>>()
const quoteDialog = ref<InstanceType<typeof QuoteCreateDialog>>()
const filter = computed(() => store.filter)
const canQuote = computed(() => business.staff && business.open.quotes)

// Archived customers stay loaded but out of the list until asked for.
const every = computed(() => store.items ?? [])
const live = computed(() => every.value.filter(c => !c.archived))
const all = computed(() => filter.value.archived ? every.value : live.value)
const archivedCount = computed(() => every.value.length - live.value.length)
const contactName = (c: Customer) => store.primaryOf(c)?.name ?? ''
const rows = computed(() => sortCustomers(all.value.filter(c => matchesCustomer(c, filter.value, store.primaryOf(c))), sort.value.key, sort.value.dir, contactName))
const countries = computed(() => facetOf(all.value, countryOf))
const industries = computed(() => facetOf(all.value, c => c.industry.trim()))
const numbered = computed(() => live.value.filter(c => c.customer_no).length)
const narrowed = computed(() => !!filter.value.q.trim() || filtered(filter.value))

function setSort(key: SortKey) {
  const dir = sort.value.key === key ? (sort.value.dir === 'asc' ? 'desc' : 'asc') : 'asc'
  pref.save({ ...(pref.value.value ?? {}), sort: { key, dir } })
}
function setWidths(next: Partial<Record<ColumnId, number>>) { pref.save({ ...(pref.value.value ?? {}), widths: next }) }
function toggle(list: 'countries' | 'industries', value: string) {
  const current = store.filter[list]
  store.filter = { ...store.filter, [list]: current.includes(value) ? current.filter(v => v !== value) : [...current, value] }
}
function clearFilters() { store.filter = { ...NO_FILTER, archived: store.filter.archived } }
function open(c: Customer) { store.cursor = c.id; void router.push(`/business/customers/${c.id}`) }
function openCreate() { create.value?.open(rows.value.length ? '' : filter.value.q.trim()) }
function created(c: Customer) {
  store.upsert(c)
  store.cursor = c.id
  toast(`Added ${c.name}.`, {
    action: {
      label: 'Undo', run: () => {
        void undoEvents(c.event_ids).then(() => {
          store.remove(c.id)
          if (router.currentRoute.value.path === `/business/customers/${c.id}`) void router.replace('/business/customers')
          toast(`${c.name} is removed again.`)
        }).catch(e => toast(e instanceof Error ? e.message : 'Undo did not work.', { tone: 'error' }))
      },
    },
    timeout: 8000,
  })
  void router.push(`/business/customers/${c.id}`)
}

// ---------- Row actions: a new quote inline, all of them in the … menu ----------
const menu = ref<{ customer: Customer; anchor: RowMenuAnchor } | null>(null)
const menuItems = computed(() => menu.value ? customerActions(menu.value.customer, { admin: business.admin, canQuote: canQuote.value }) : [])
let opener: HTMLElement | null = null
function openMenu(c: Customer, anchor: RowMenuAnchor) {
  // The … button toggles its menu.
  if (menu.value && menu.value.anchor === anchor) { menu.value = null; return }
  opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
  store.cursor = c.id
  menu.value = { customer: c, anchor }
}
// Focus goes back where the menu was opened from: the … button, or the list.
function closeMenu(restore: boolean) {
  menu.value = null
  if (!restore) return
  void nextTick(() => { if (opener?.isConnected && opener.checkVisibility()) opener.focus(); else table.value?.focus() })
}
function newQuote(c: Customer) { quoteDialog.value?.open({ customerId: c.id, customerName: c.name }) }
function quoteCreated(quote: QuoteProjection) {
  toast(`Created ${quote.offer_no ?? 'a new quote'}. Write it on the page.`)
  void router.push(`/business/quotes/${encodeURIComponent(quote.quote_node_id)}`)
}
async function act(c: Customer, id: CustomerActionId) {
  menu.value = null
  switch (id) {
    case 'open': open(c); return
    case 'quote': newQuote(c); return
    case 'copyNumber':
      try { await navigator.clipboard.writeText(c.customer_no ?? ''); toast(`Copied ${c.customer_no}.`) } catch { toast('Copying did not work here.', { tone: 'error' }) }
      return
    case 'archive': case 'restore': {
      const archiving = id === 'archive'
      try {
        const saved = await setCustomerArchived(c.id, c.revision, archiving)
        store.upsert(saved)
        toast(archiving ? `Archived ${c.name}. Its quotes, projects and hours stay; new quotes no longer offer it.` : `${c.name} is back in the list.`, {
          action: {
            label: 'Undo', run: () => {
              void undoEvents(saved.event_ids).then(() => store.load(true))
                .catch(e => toast(errorText(e, 'Undo did not work.'), { tone: 'error' }))
            },
          },
          timeout: 8000,
        })
      } catch (e) { toast(errorText(e), { tone: 'error' }); void store.load(true) }
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
  const index = list.findIndex(c => c.id === store.cursor)
  const next = index === -1 ? (step > 0 ? 0 : list.length - 1) : Math.max(0, Math.min(list.length - 1, index + step))
  store.cursor = list[next].id
}
function keys(event: KeyboardEvent) {
  if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || document.querySelector('dialog[open], .floating')) return
  if (typing(event.target)) {
    if (event.target === search.value && (event.key === 'ArrowDown' || event.key === 'Enter') && rows.value.length) {
      event.preventDefault()
      if (event.key === 'Enter' && rows.value.length === 1) { open(rows.value[0]); return }
      if (!rows.value.some(c => c.id === store.cursor)) store.cursor = rows.value[0].id
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
  else if ((key === 'Enter' || key === 'o') && store.cursor) {
    const c = rows.value.find(r => r.id === store.cursor)
    if (c && !(event.target as HTMLElement).closest?.('a, button, [role="separator"]')) { event.preventDefault(); open(c) }
  } else if (opensRowMenu(event) && store.cursor) {
    const c = rows.value.find(r => r.id === store.cursor)
    if (!c) return
    event.preventDefault()
    openMenu(c, table.value?.moreButton(c.id) ?? { x: innerWidth / 2, y: innerHeight / 3 })
  } else if (key === '/') { event.preventDefault(); search.value?.focus(); search.value?.select() }
  else if (key === 'n' && business.admin) { event.preventDefault(); openCreate() }
}
onMounted(async () => {
  window.addEventListener('keydown', keys)
  await business.loadPlugins()
  if (business.open.crm) void store.load(true)
})
onBeforeUnmount(() => window.removeEventListener('keydown', keys))
watch(() => business.open.crm, on => { if (on) void store.load(true) })
</script>

<template>
  <BusinessPage title="Customers" area="crm">
    <template #summary>
      <span v-if="store.items && every.length" class="dot-list"><span>{{ plural(live.length, 'customer') }}</span><span>{{ numbered }} with a number</span><span v-if="archivedCount">{{ archivedCount }} archived</span></span>
      <span v-else-if="store.items">No customers yet</span>
      <span v-else-if="store.error">Customers could not be loaded</span>
      <span v-else class="skeleton summary-skeleton" />
    </template>
    <template v-if="business.admin" #actions>
      <button type="button" class="btn primary" aria-keyshortcuts="n" data-tip="New customer · n" @click="openCreate"><AppIcon name="plus" :size="14" />New customer</button>
    </template>

    <div class="toolbar" role="search">
      <label class="search-field list-search">
        <AppIcon name="search" :size="14" />
        <input ref="search" v-model="store.filter.q" class="field" type="search" placeholder="Find a customer, number or contact" aria-label="Find a customer" aria-keyshortcuts="/" autocomplete="off" />
        <kbd class="keycap slash" aria-hidden="true">/</kbd>
      </label>
      <div class="seg" role="group" aria-label="Customer number">
        <button type="button" :aria-pressed="filter.number === 'all'" @click="store.filter = { ...store.filter, number: 'all' }">All</button>
        <button type="button" :aria-pressed="filter.number === 'with'" @click="store.filter = { ...store.filter, number: 'with' }">With number</button>
        <button type="button" :aria-pressed="filter.number === 'without'" @click="store.filter = { ...store.filter, number: 'without' }">Without</button>
      </div>
      <ChoiceFacet v-if="countries.length" label="Country" :options="countries" :selected="filter.countries" @toggle="v => toggle('countries', v)" @clear="store.filter = { ...store.filter, countries: [] }" />
      <ChoiceFacet v-if="industries.length" label="Industry" :options="industries" :selected="filter.industries" @toggle="v => toggle('industries', v)" @clear="store.filter = { ...store.filter, industries: [] }" />
      <button v-if="archivedCount || filter.archived" type="button" class="btn sm facet-toggle" :aria-pressed="filter.archived" @click="store.filter = { ...store.filter, archived: !filter.archived }">
        <AppIcon name="archive" :size="13" />Archived<span class="toggle-count">{{ archivedCount }}</span>
      </button>
      <button v-if="narrowed" type="button" class="btn sm ghost" @click="clearFilters">Clear</button>
      <span class="spacer" />
      <p v-if="store.items && (narrowed || filter.archived)" class="count" role="status">{{ rows.length }} of {{ all.length }}</p>
    </div>

    <IntegrationCard v-if="business.admin" :admin="true" bare @imported="created" />

    <div v-if="store.error && !store.items" class="state glass-card" role="alert">
      <span class="state-icon danger"><AppIcon name="alert" :size="18" /></span>
      <h2>Customers could not be loaded</h2>
      <p>The list did not load this time. Nothing is lost; try again in a moment.</p>
      <p v-if="store.error" class="state-detail">The server said: {{ store.error }}</p>
      <button type="button" class="btn" @click="store.load(true)"><AppIcon name="refresh" :size="14" />Try again</button>
    </div>
    <div v-else-if="store.items && !every.length" class="state glass-card">
      <span class="state-icon"><BizIcon name="building" :size="18" /></span>
      <h2>No customers yet</h2>
      <p v-if="business.admin">Add the organisations you work for. Their contacts, projects, quotes and hours come together on each customer’s page.</p>
      <p v-else>A workspace admin adds customers. Their contacts, projects, quotes and hours then come together here.</p>
      <button v-if="business.admin" type="button" class="btn primary" @click="openCreate"><AppIcon name="plus" :size="14" />New customer</button>
    </div>
    <CustomerTable
      v-else ref="table" :rows="rows" :loading="store.loading && !store.items" :query="filter.q" :sort="sort" :cursor-id="store.cursor" :widths="widths" :contact="store.primaryOf" :can-quote="canQuote" :menu-id="menu?.customer.id ?? null"
      @sort="setSort" @cursor="id => store.cursor = id" @open="open" @widths="setWidths" @grid-focus="() => { if (!store.cursor && rows.length) store.cursor = rows[0].id }"
      @action="c => newQuote(c)" @menu="(c, anchor) => openMenu(c, anchor)"
    >
      <div v-if="store.items && every.length && !live.length && !narrowed && !filter.archived" class="state inline">
        <span class="state-icon"><AppIcon name="archive" :size="18" /></span>
        <h2>Every customer is archived</h2>
        <p>Archived customers keep their quotes, projects and hours. Show them to restore one.</p>
        <div class="state-actions"><button type="button" class="btn" @click="store.filter = { ...store.filter, archived: true }">Show archived</button></div>
      </div>
      <div v-else-if="store.items && every.length && !rows.length" class="state inline">
        <span class="state-icon"><AppIcon name="search" :size="18" /></span>
        <h2>{{ filter.q.trim() ? `No customer matches “${filter.q.trim()}”` : 'No customer matches these filters' }}</h2>
        <p>Search looks at names, legal names, numbers, industries, places and primary contacts.</p>
        <div class="state-actions">
          <button v-if="narrowed" type="button" class="btn" @click="clearFilters">Clear search and filters</button>
          <button v-if="!filter.archived && archivedCount" type="button" class="btn ghost" @click="store.filter = { ...store.filter, archived: true }">Include archived</button>
          <button v-if="business.admin && filter.q.trim()" type="button" class="btn ghost" @click="openCreate"><AppIcon name="plus" :size="14" />Add “{{ filter.q.trim() }}”</button>
        </div>
        <p class="aside-note"><BizIcon name="plug" :size="13" />Importing from another CRM needs a connected provider. None is connected{{ business.admin ? '; an operator sets one up on the server' : '' }}.</p>
      </div>
    </CustomerTable>
    <p v-if="store.items && rows.length" class="keys-hint dot-list" aria-hidden="true">
      <span><kbd class="keycap">j</kbd><kbd class="keycap">k</kbd> move</span><span><kbd class="keycap"><AppIcon name="enter" /></kbd> open</span><span><kbd class="keycap">shift</kbd><kbd class="keycap">F10</kbd> actions</span><span><kbd class="keycap">/</kbd> search</span><span v-if="business.admin"><kbd class="keycap">n</kbd> new customer</span>
    </p>
    <CustomerCreateDialog ref="create" @created="created" />
    <QuoteCreateDialog ref="quoteDialog" @created="quoteCreated" />
    <RowMenu v-if="menu" :anchor="menu.anchor" :items="menuItems" :label="`Actions for ${menu.customer.name}`" @select="id => act(menu!.customer, id as CustomerActionId)" @close="closeMenu" />
  </BusinessPage>
</template>

<style scoped>
.summary-skeleton { display: inline-block; width: 220px; }
.toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 10px 12px; min-height: 48px; margin-bottom: 12px; }
.list-search { width: 320px; }
.list-search .field { height: 32px; padding-right: 34px; font-size: 13.5px; }
.slash { position: absolute; right: 8px; pointer-events: none; }
.list-search:focus-within .slash { display: none; }
.facet-toggle { gap: 6px; padding: 0 10px; font-weight: 600; color: var(--ink-2); }
.facet-toggle svg { color: var(--ink-3); }
.toggle-count { font: 600 11px/1 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.spacer { flex: 1; }
.count { font-size: 12.5px; color: var(--ink-2); font-variant-numeric: tabular-nums; }
.state { display: grid; justify-items: center; gap: 8px; max-width: 620px; margin: 8px auto 0; padding: 44px 28px; text-align: center; }
.state.inline { max-width: none; margin: 0; padding: 40px 24px 36px; border-top: 1px solid var(--line); }
.state h2 { font-size: 17px; text-wrap: balance; overflow-wrap: anywhere; }
.state p { max-width: 50ch; font-size: 13.5px; color: var(--ink-2); }
/* What the server said, for whoever helps: small and quiet under the plain words. */
.state .state-detail { font: 500 11.5px/1.5 var(--mono); color: var(--ink-3); font-variant-ligatures: none; }
.state > .btn { margin-top: 8px; }
.state-actions { display: flex; flex-wrap: wrap; justify-content: center; gap: 8px; margin-top: 8px; }
.state-icon { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 4px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.state-icon.danger { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.aside-note { display: flex; align-items: flex-start; gap: 7px; margin-top: 14px; padding: 9px 12px; border-radius: 10px; background: var(--surface-2); font-size: 12.5px !important; text-align: left; }
.aside-note svg { margin-top: 2px; flex-shrink: 0; color: var(--ink-3); }
/* Right-aligned as a whole; inside, lines start on the left so no dot leads a line. */
.keys-hint { width: fit-content; max-width: 100%; align-items: center; margin: 10px 0 0 auto; font-size: 12px; color: var(--ink-3); }
.keys-hint > span { display: inline-flex; align-items: center; gap: 4px; }
@media (max-width: 720px) {
  .toolbar { gap: 20px 8px; }
  .list-search { flex: 1 1 100%; width: auto; }
  .list-search .field { height: 44px; font-size: 16px; }
  .slash { display: none; }
  .seg { flex: 1 1 100%; }
  .seg button { flex: 1; height: 38px; white-space: nowrap; }
  .toolbar :deep(.btn) { height: 44px; }
  .keys-hint { display: none; }
  .state { padding: 32px 18px; }
}
</style>
