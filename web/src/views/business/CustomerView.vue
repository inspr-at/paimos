<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import '../../styles/crm.css'
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import {
  addressLines, blankCustomer, deleteCustomer, draftOf, draftProblems, errorText, getCustomer, getRelated, listContacts, minorMoney, placeOf, sameDraft, setCustomerArchived, statusOf, telHref, undoEvents,
  updateCustomer, websiteHost, writeOf, type Contact, type Customer, type CustomerDraft, type Related,
} from '../../lib/crm'
import { setPageTitle } from '../../lib/brand'
import { confirmAction } from '../../lib/confirm'
import { useSession } from '../../stores/session'
import { captureToastOwner, toast } from '../../lib/toast'
import { useBusiness } from '../../stores/business'
import { useCustomers } from '../../stores/customers'
import AppIcon from '../../components/AppIcon.vue'
import KeyCap from '../../components/KeyCap.vue'
import Avatar from '../../components/Avatar.vue'
import BizIcon from '../../components/business/BizIcon.vue'
import BusinessPage from '../../components/business/BusinessPage.vue'
import FloatingPanel from '../../components/work/FloatingPanel.vue'
import ContactList from '../../components/crm/ContactList.vue'
import CustomerForm from '../../components/crm/CustomerForm.vue'
import IntegrationCard from '../../components/crm/IntegrationCard.vue'
import NotesSection from '../../components/crm/NotesSection.vue'
import RelatedSection from '../../components/crm/RelatedSection.vue'
import QuoteCreateDialog from '../../components/business/QuoteCreateDialog.vue'

// One customer: who they are and who to talk to, their projects, quotes and
// hours, the team's notes. Admins change it in an explicit edit mode (one form,
// one Save, Esc to cancel), and every change offers Undo from the event log.
const route = useRoute()
const router = useRouter()
const business = useBusiness()
const store = useCustomers()
const session = useSession()
const viewer = computed(() => session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : '')
let generation = 0
function capture() {
  const targetId = id.value, owner = viewer.value, request = generation
  return { id: targetId, current: () => request === generation && targetId === id.value && owner === viewer.value && !!owner && session.authenticationCurrent?.() !== false }
}
const id = computed(() => String(route.params.id ?? ''))
const customer = ref<Customer | null>(null)
const contacts = ref<Contact[] | null>(null)
const related = ref<Related | null>(null)
const state = ref<'loading' | 'ready' | 'missing' | 'error'>('loading')
const loadError = ref('')
const contactsError = ref('')
const relatedError = ref('')
const contactList = ref<InstanceType<typeof ContactList>>()
const admin = computed(() => business.admin)
// A quote for this customer, written on its own page once created.
const quoteDialog = ref<InstanceType<typeof QuoteCreateDialog>>()
const canQuote = computed(() => business.staff && business.open.quotes)
function newQuote() { if (customer.value) quoteDialog.value?.open({ customerId: customer.value.id, customerName: customer.value.name }) }
function quoteCreated(quote: { quote_node_id: string; offer_no?: string }) {
  toast(`Created ${quote.offer_no ?? 'a new quote'}. Write it on the page.`)
  void loadRelated()
  void router.push(`/business/quotes/${encodeURIComponent(quote.quote_node_id)}`)
}
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)

async function loadCustomer(action = capture()) {
  try {
    const fresh = await getCustomer(action.id)
    if (!action.current()) return
    customer.value = fresh; store.upsert(fresh); state.value = 'ready'
    setPageTitle(fresh.name)
  } catch (e) {
    if (!action.current()) return
    if (statusOf(e) === 404) { state.value = 'missing'; store.remove(action.id); setPageTitle('Customer not found') }
    else if (!customer.value) { state.value = 'error'; loadError.value = errorText(e, 'This customer could not be loaded.') }
  }
}
async function loadContacts(action = capture()) {
  try { const found = await listContacts(action.id); if (!action.current()) return; contacts.value = found; contactsError.value = ''; for (const c of contacts.value) store.setContact(c.id, { name: c.name, email: c.email, role: c.role }) }
  catch (e) { if (action.current() && statusOf(e) !== 404) contactsError.value = errorText(e, 'Contacts could not be loaded.') }
}
async function loadRelated(action = capture()) {
  try { const found = await getRelated(action.id); if (!action.current()) return; related.value = found; relatedError.value = '' }
  catch (e) { if (action.current() && statusOf(e) !== 404) relatedError.value = errorText(e, 'Projects, quotes and hours could not be loaded.') }
}
async function load() {
  generation++
  const action = capture()
  loadError.value = ''; contactsError.value = ''; relatedError.value = ''
  const known = store.items?.find(c => c.id === id.value)
  customer.value = known ?? null; contacts.value = null; related.value = null
  state.value = known ? 'ready' : 'loading'
  if (known) setPageTitle(known.name)
  await Promise.all([loadCustomer(action), loadContacts(action), loadRelated(action)])
}
// A contact change moves the customer's revision (the primary contact), so both reload.
async function contactsChanged() { await Promise.all([loadCustomer(), loadContacts()]) }
function updated(next: Customer) { if (next.id !== id.value) return; customer.value = next; store.upsert(next); setPageTitle(next.name) }

const primary = computed(() => contacts.value?.find(c => c.primary) ?? null)
const rate = computed(() => customer.value ? minorMoney(customer.value.hourly_rate_minor, customer.value.currency) : '')
const lpRate = computed(() => customer.value ? minorMoney(customer.value.lp_rate_minor, customer.value.currency) : '')
const billing = computed(() => addressLines(customer.value?.billing_address ?? null))
const visiting = computed(() => addressLines(customer.value?.visiting_address ?? null))
const sameAddress = computed(() => billing.value.length > 0 && billing.value.join('\n') === visiting.value.join('\n'))
const blocked = computed(() => !!related.value && (related.value.projects.length > 0 || related.value.quotes.length > 0))

// ---------- Edit mode ----------
const editing = ref(false)
const saving = ref(false)
const touched = ref(false)
const conflict = ref(false)
const EMPTY: Customer = { ...blankCustomer(), id: '', key: '', revision: 0, customer_no: null, primary_contact_node_id: null }
const draft = reactive<CustomerDraft>(draftOf(EMPTY))
let base: CustomerDraft = draftOf(EMPTY)
const problems = computed(() => draftProblems(draft))
const dirty = computed(() => editing.value && !sameDraft(draft, base))
async function startEdit() {
  if (!admin.value || !customer.value || editing.value) return
  base = draftOf(customer.value)
  Object.assign(draft, draftOf(customer.value))
  touched.value = false; conflict.value = false; editing.value = true
  await nextTick()
  const name = document.getElementById('edit-name') as HTMLInputElement | null
  name?.focus(); name?.setSelectionRange(name.value.length, name.value.length)
}
async function save() {
  const action = capture(), current = customer.value
  if (!current || !action.current() || current.id !== action.id || saving.value) return
  touched.value = true
  if (Object.keys(problems.value).length) {
    await nextTick(); document.querySelector<HTMLInputElement>('.edit-card [aria-invalid="true"]')?.focus()
    return
  }
  if (!dirty.value && !conflict.value) { editing.value = false; return }
  saving.value = true
  try {
    const next = await updateCustomer(current.id, writeOf(draft, current), current.revision)
    if (!action.current()) return
    updated(next)
    editing.value = false; conflict.value = false
    toast(`Saved ${next.name}.`, {
      timeout: 8000,
      action: { label: 'Undo', run: () => { void undoEvents(next.event_ids).then(() => { if (!action.current()) return; void loadCustomer(); toast(`${next.name} is back as it was.`) }).catch(e => toast(errorText(e), { tone: 'error' })) } },
    })
    void nextTick(() => editButton.value?.focus({ preventScroll: true }))
  } catch (e) {
    if (!action.current()) return
    if (statusOf(e) === 409 && !/not enabled/.test(errorText(e))) {
      // The newer version is loaded; the draft stays, and saving again replaces it.
      conflict.value = true
      await loadCustomer()
    } else toast(errorText(e, 'The customer was not saved.'), { tone: 'error' })
  } finally { if (action.current()) saving.value = false }
}
async function cancel() {
  if (dirty.value && !(await confirmAction({ title: 'Discard your changes?', body: `Your edits to ${customer.value?.name ?? 'this customer'} have not been saved.`, confirmLabel: 'Discard', danger: true }))) return
  editing.value = false; conflict.value = false
  void nextTick(() => editButton.value?.focus({ preventScroll: true }))
}
async function confirmLeave(to: { path: string }) {
  if (to.path === '/signin' && useSession().requiresSignIn) return true
  if (!dirty.value) return true
  return confirmAction({ title: 'Leave without saving?', body: `Your edits to ${customer.value?.name ?? 'this customer'} have not been saved.`, confirmLabel: 'Leave', danger: true })
}
onBeforeRouteLeave(confirmLeave)
onBeforeRouteUpdate((to, from) => to.params.id === from.params.id || confirmLeave(to))
function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value) event.preventDefault() }

// ---------- More: copy link, delete ----------
const moreAnchor = ref<HTMLElement | null>(null)
const moreButton = ref<HTMLButtonElement>()
const editButton = ref<HTMLButtonElement>()
function toggleMore(event: MouseEvent) { moreAnchor.value = moreAnchor.value ? null : event.currentTarget as HTMLElement }
function closeMore(restore: boolean) { moreAnchor.value = null; if (restore) moreButton.value?.focus() }
function menuKeys(event: KeyboardEvent) {
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('button:not(:disabled)')]
  const index = items.indexOf(document.activeElement as HTMLButtonElement)
  event.preventDefault(); event.stopPropagation()
  items[event.key === 'ArrowDown' ? Math.min(items.length - 1, index + 1) : Math.max(0, index - 1)]?.focus()
}
async function copyLink() {
  moreAnchor.value = null
  try { await navigator.clipboard.writeText(window.location.href); toast('Link copied.') } catch { toast('The link could not be copied.', { tone: 'error' }) }
}
// Archiving keeps everything that depends on the customer and hides it from
// lists and pickers; the toast undoes it.
async function archive(archived: boolean) {
  const action = capture()
  const c = customer.value
  moreAnchor.value = null
  if (!c) return
  try {
    const next = await setCustomerArchived(c.id, c.revision, archived)
    if (!action.current()) return
    customer.value = next
    store.upsert(next)
    toast(archived ? `Archived ${c.name}. Its quotes, projects and hours stay; new quotes no longer offer it.` : `${c.name} is back in the list.`, {
      timeout: 8000,
      action: { label: 'Undo', run: () => { void undoEvents(next.event_ids).then(() => { if (!action.current()) return; void loadCustomer(); void store.load(true) }).catch(e => toast(errorText(e), { tone: 'error' })) } },
    })
  } catch (e) { if (action.current()) toast(errorText(e, 'That did not work.'), { tone: 'error' }) }
}
async function remove() {
  const action = capture(), samePerson = captureToastOwner()
  const c = customer.value
  moreAnchor.value = null
  if (!c) return
  const people = contacts.value ?? []
  const ok = await confirmAction({
    title: `Delete ${c.name}?`,
    body: people.length ? `Its ${people.length === 1 ? 'contact goes' : `${people.length} contacts go`} with it. You can undo this right after.` : 'You can undo this right after.',
    confirmLabel: 'Delete customer', danger: true,
  })
  if (!ok || !action.current()) return
  try {
    const receipt = await deleteCustomer(c.id)
    if (!action.current()) return
    store.remove(c.id)
    void router.push('/business/customers')
    toast(`Deleted ${c.name}.`, {
      timeout: 8000,
      action: { label: 'Undo', run: () => { void undoEvents(receipt.event_ids).then(() => { if (!samePerson()) return; void store.load(true); toast(`${c.name} is back.`) }).catch(e => { if (samePerson()) toast(errorText(e), { tone: 'error' }) }) } },
    })
  } catch (e) {
    if (!action.current()) return
    toast(statusOf(e) === 409 ? `${c.name} has projects or quotes, so it stays.` : errorText(e, 'The customer was not deleted.'), { tone: 'error' })
  }
}

// ---------- Keyboard: e edits; in edit mode Cmd/Ctrl+Enter saves and Esc cancels ----------
function typing(target: EventTarget | null) {
  return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
}
function keys(event: KeyboardEvent) {
  if (event.defaultPrevented || document.querySelector('dialog[open], .floating')) return
  if (editing.value) {
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); void save() }
    else if (event.key === 'Escape') { event.preventDefault(); void cancel() }
    return
  }
  if (event.metaKey || event.ctrlKey || event.altKey || typing(event.target)) return
  if (event.key === 'e' && admin.value && state.value === 'ready') { event.preventDefault(); void startEdit() }
}
onMounted(() => { window.addEventListener('keydown', keys); window.addEventListener('beforeunload', beforeUnload) })
onBeforeUnmount(() => { generation++; window.removeEventListener('keydown', keys); window.removeEventListener('beforeunload', beforeUnload) })
// Only this page's own route loads (leaving it clears the id before it unmounts).
watch([id, viewer], ([value]) => { generation++; editing.value = false; saving.value = false; conflict.value = false; moreAnchor.value = null; if (!value || !viewer.value || !route.path.startsWith('/business/customers/')) { customer.value = null; contacts.value = null; related.value = null; return }; void load() }, { immediate: true, flush: 'sync' })
</script>

<template>
  <BusinessPage :title="customer?.name ?? (state === 'missing' ? 'Customer not found' : 'Customer')" area="crm">
    <template #eyebrow><RouterLink class="back" to="/business/customers"><AppIcon name="arrow-left" :size="12" />Customers</RouterLink></template>
    <template #summary>
      <span v-if="customer" class="summary-line dot-list">
        <span v-if="customer.archived" class="archived-chip">Archived</span>
        <span v-if="customer.legal_name && customer.legal_name !== customer.name">{{ customer.legal_name }}</span>
        <span v-if="customer.industry">{{ customer.industry }}</span>
        <a v-if="customer.website" :href="customer.website" target="_blank" rel="noopener" class="site">{{ websiteHost(customer.website) }}<AppIcon name="external" :size="11" /></a>
      </span>
      <span v-else-if="state === 'loading'" class="skeleton summary-skeleton" />
    </template>
    <template v-if="customer && state === 'ready'" #actions>
      <template v-if="editing">
        <span v-if="dirty" class="unsaved" aria-live="polite">Unsaved</span>
        <button type="button" class="btn sm ghost" :disabled="saving" aria-keyshortcuts="Escape" data-tip="Cancel · Esc" @click="cancel">Cancel</button>
        <button type="button" class="btn sm primary" :disabled="saving" :aria-keyshortcuts="mac ? 'Meta+Enter' : 'Control+Enter'" :data-tip="`Save · ${mac ? 'Cmd' : 'Ctrl'} Enter`" @click="save"><AppIcon name="check" :size="13" />{{ saving ? 'Saving…' : conflict ? 'Save anyway' : 'Save' }}</button>
      </template>
      <template v-else>
        <button v-if="canQuote" type="button" class="btn sm" data-tip="A quote for this customer" @click="newQuote"><AppIcon name="plus" :size="13" />New quote</button>
        <button v-if="admin" ref="editButton" type="button" class="btn sm" aria-keyshortcuts="e" data-tip="Edit every field · e" @click="startEdit"><AppIcon name="edit" :size="13" />Edit</button>
        <button ref="moreButton" type="button" class="icon-btn sm" aria-label="More actions" aria-haspopup="menu" :aria-expanded="!!moreAnchor" data-tip="More" @click="toggleMore"><AppIcon name="more" :size="15" /></button>
      </template>
    </template>

    <FloatingPanel v-if="moreAnchor && customer" :anchor="moreAnchor" :width="230" align="end" :label="`Actions for ${customer.name}`" @close="closeMore">
      <div class="more-menu" role="menu" :aria-label="`Actions for ${customer.name}`" @keydown="menuKeys">
        <button type="button" role="menuitem" class="menu-item" data-autofocus @click="copyLink"><AppIcon name="link" :size="14" />Copy link</button>
        <template v-if="admin">
          <div class="menu-sep" role="separator" />
          <button v-if="customer.archived" type="button" role="menuitem" class="menu-item" @click="archive(false)"><AppIcon name="rollback" :size="14" />Restore from the archive</button>
          <button v-else type="button" role="menuitem" class="menu-item" @click="archive(true)"><AppIcon name="archive" :size="14" />Archive</button>
          <button type="button" role="menuitem" class="menu-item danger" :disabled="blocked" :data-tip="blocked ? 'Customers with projects or quotes stay' : undefined" @click="remove"><AppIcon name="trash" :size="14" />Delete customer…</button>
        </template>
      </div>
    </FloatingPanel>

    <div v-if="state === 'missing'" class="state glass-card">
      <span class="state-icon"><BizIcon name="building" :size="18" /></span>
      <h2>This customer no longer exists</h2>
      <p>It may have been deleted. The other customers are in the list.</p>
      <RouterLink class="btn" to="/business/customers"><AppIcon name="arrow-left" :size="14" />All customers</RouterLink>
    </div>
    <div v-else-if="state === 'error'" class="state glass-card" role="alert">
      <span class="state-icon danger"><AppIcon name="alert" :size="18" /></span>
      <h2>This customer could not be loaded</h2>
      <p>{{ loadError }}</p>
      <button type="button" class="btn" @click="load"><AppIcon name="refresh" :size="14" />Try again</button>
    </div>
    <div v-else-if="state === 'loading'" class="skeleton-page" role="status" aria-label="Loading the customer">
      <span class="skeleton hero-sk" /><div class="sk-cols"><span class="skeleton card-sk" /><span class="skeleton card-sk short" /></div>
    </div>

    <!-- Edit mode: the whole customer as one form, one Save -->
    <form v-else-if="editing && customer" class="edit-card glass-card" :aria-label="`Edit ${customer.name}`" novalidate @submit.prevent="save">
      <p v-if="conflict" class="f-error" role="alert"><AppIcon name="alert" :size="14" /><span><strong>Changed elsewhere while you were editing.</strong> Your changes are kept below; saving again replaces the newer version.</span></p>
      <CustomerForm :key="customer.id" :draft="draft" :problems="problems" :touched="touched" />
      <footer class="edit-foot">
        <p class="f-hint"><KeyCap k="mod" /><KeyCap k="enter" /> save · <kbd class="keycap">esc</kbd> cancel</p>
        <span v-if="dirty" class="unsaved">Unsaved</span>
        <button type="button" class="btn" :disabled="saving" @click="cancel">Cancel</button>
        <button type="submit" class="btn primary" :disabled="saving"><AppIcon name="check" :size="14" />{{ saving ? 'Saving…' : conflict ? 'Save anyway' : 'Save' }}</button>
      </footer>
    </form>

    <template v-else-if="customer">
      <section class="hero glass-card" aria-label="At a glance">
        <div class="hero-cell who">
          <p class="hero-label">Primary contact</p>
          <div v-if="primary" class="person">
            <Avatar :name="primary.name" :size="40" />
            <div class="person-text">
              <p class="person-name">{{ primary.name }}</p>
              <p v-if="primary.role" class="person-role">{{ primary.role }}</p>
              <p v-if="primary.email || primary.phone" class="person-reach">
                <a v-if="primary.email" :href="`mailto:${primary.email}`"><BizIcon name="mail" :size="13" />{{ primary.email }}</a>
                <a v-if="primary.phone" :href="telHref(primary.phone)"><BizIcon name="phone" :size="13" />{{ primary.phone }}</a>
              </p>
            </div>
          </div>
          <p v-else-if="contacts" class="hero-unset">No contact yet<button v-if="admin" type="button" class="btn sm ghost" @click="contactList?.add()"><AppIcon name="plus" :size="13" />Add one</button></p>
          <span v-else class="skeleton hero-line" />
        </div>
        <div class="hero-cell">
          <p class="hero-label">Customer number</p>
          <p v-if="customer.customer_no" class="hero-value mono">{{ customer.customer_no }}</p>
          <p v-else class="hero-unset">Assigned with the first quote</p>
        </div>
        <div class="hero-cell">
          <p class="hero-label">Hourly rate</p>
          <p v-if="rate" class="hero-value mono">{{ rate }}</p>
          <p v-else class="hero-unset">Not set</p>
        </div>
        <div class="hero-cell">
          <p class="hero-label">Location</p>
          <p v-if="placeOf(customer)" class="hero-value">{{ placeOf(customer) }}</p>
          <p v-else class="hero-unset">No address yet</p>
        </div>
      </section>

      <div class="layout">
        <div class="main-col">
          <ContactList :key="customer.id" ref="contactList" :customer="customer" :contacts="contacts" :admin="admin" :error="contactsError" @changed="contactsChanged" />
          <RelatedSection :key="customer.id" :related="related" :customer-id="id" :admin="admin" :error="relatedError" :can-quote="canQuote" @retry="loadRelated" @changed="loadRelated" @new-quote="newQuote" />
          <NotesSection :key="customer.id" :customer="customer" :admin="admin" @updated="updated" @reload="loadCustomer" />
        </div>
        <aside class="side-col" aria-label="Details">
          <section class="crm-card glass-card" aria-labelledby="about-title">
            <header class="card-head">
              <span class="card-icon" aria-hidden="true"><BizIcon name="building" :size="15" /></span>
              <div class="card-titles"><h2 id="about-title">About</h2></div>
            </header>
            <p v-if="customer.description" class="description">{{ customer.description }}</p>
            <dl class="facts">
              <div><dt>Phone</dt><dd v-if="customer.phone"><a :href="telHref(customer.phone)">{{ customer.phone }}</a></dd><dd v-else class="unset">Not set</dd></div>
              <div><dt>Website</dt><dd v-if="customer.website"><a :href="customer.website" target="_blank" rel="noopener">{{ websiteHost(customer.website) }}</a></dd><dd v-else class="unset">Not set</dd></div>
              <div v-if="customer.domain"><dt>Email domain</dt><dd>{{ customer.domain }}</dd></div>
              <div><dt>VAT ID</dt><dd v-if="customer.vat_id" class="mono">{{ customer.vat_id }}</dd><dd v-else class="unset">Not set</dd></div>
              <div v-if="customer.tax_id"><dt>Tax number</dt><dd class="mono">{{ customer.tax_id }}</dd></div>
              <div v-if="customer.register_no"><dt>Company register</dt><dd class="mono">{{ customer.register_no }}</dd></div>
              <div v-if="lpRate"><dt>Rate per point</dt><dd class="mono">{{ lpRate }}</dd></div>
              <div v-if="customer.employee_count != null"><dt>Employees</dt><dd class="mono">{{ customer.employee_count.toLocaleString('en-GB') }}</dd></div>
              <div v-if="customer.annual_revenue_minor != null"><dt>Annual revenue</dt><dd class="mono">{{ minorMoney(customer.annual_revenue_minor, customer.currency) }}</dd></div>
            </dl>
          </section>
          <section class="crm-card glass-card" aria-labelledby="addresses-title">
            <header class="card-head">
              <span class="card-icon" aria-hidden="true"><BizIcon name="pin" :size="15" /></span>
              <div class="card-titles"><h2 id="addresses-title">Addresses</h2></div>
            </header>
            <div class="addresses">
              <div class="address">
                <p class="address-label">Billing</p>
                <address v-if="billing.length"><span v-for="line in billing" :key="line">{{ line }}</span></address>
                <p v-else class="unset">Not set{{ admin ? '. Quotes use it once it is.' : '' }}</p>
              </div>
              <div class="address">
                <p class="address-label">Visiting</p>
                <p v-if="sameAddress" class="unset">Same as billing</p>
                <address v-else-if="visiting.length"><span v-for="line in visiting" :key="line">{{ line }}</span></address>
                <p v-else class="unset">Not set</p>
              </div>
            </div>
          </section>
          <IntegrationCard :key="customer.id" :admin="admin" :customer="customer" @synced="updated" />
        </aside>
      </div>
    </template>
    <QuoteCreateDialog ref="quoteDialog" @created="quoteCreated" />
  </BusinessPage>
</template>

<style scoped>
.archived-chip { height: 18px; padding: 0 7px; border-radius: 999px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-2); font: 600 10px/18px var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.back { display: inline-flex; align-items: center; gap: 6px; min-height: 24px; margin: -4px -6px; padding: 0 6px; border-radius: 6px; color: var(--ink-2); text-decoration: none; }
.back:hover { color: var(--teal-ink); background: var(--row-hover); }
.back:focus-visible { box-shadow: var(--focus-ring); }
.site { display: inline-flex; align-items: center; gap: 4px; color: var(--teal-ink); text-decoration: none; }
@media (max-width: 600px) { .back, .site { min-height: 44px; } }
.site:hover { text-decoration: underline; }
.summary-skeleton { display: inline-block; width: 240px; }
.unsaved { font-size: 12px; font-weight: 600; color: var(--gold-ink); }
.state { display: grid; justify-items: center; gap: 8px; max-width: 580px; margin: 12px auto 0; padding: 44px 28px; text-align: center; }
.state h2 { font-size: 17px; }
.state p { max-width: 46ch; font-size: 13.5px; color: var(--ink-2); }
.state .btn { margin-top: 8px; }
.state-icon { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 4px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.state-icon.danger { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.skeleton-page { display: grid; gap: 16px; }
.hero-sk { height: 96px; border-radius: var(--radius); }
.sk-cols { display: grid; grid-template-columns: minmax(0, 1fr) 360px; gap: 16px; }
.card-sk { height: 280px; border-radius: var(--radius); }
.card-sk.short { height: 200px; }

.hero { display: grid; grid-template-columns: minmax(0, 1.7fr) repeat(3, minmax(0, 1fr)); min-height: 125px; margin-bottom: 16px; padding: 0; overflow: hidden; }
.hero-cell { display: grid; align-content: start; gap: 6px; min-width: 0; padding: 16px 20px; border-left: 1px solid var(--line); }
.hero-cell:first-child { border-left: 0; }
.hero-label { font: 500 10px/1.4 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.hero-value { font-size: 15px; font-weight: 600; line-height: 1.4; color: var(--ink); overflow-wrap: anywhere; }
.hero-value.mono { font-family: var(--mono); font-variant-numeric: tabular-nums; font-variant-ligatures: none; }
.hero-unset { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; font-size: 13.5px; color: var(--ink-3); }
.hero-line { width: 70%; height: 12px; margin-top: 6px; }
.person { display: flex; align-items: flex-start; gap: 12px; min-width: 0; }
.person-text { display: grid; gap: 1px; min-width: 0; }
.person-name { font-size: 15px; font-weight: 650; color: var(--ink); overflow-wrap: anywhere; }
.person-role { font-size: 12.5px; color: var(--ink-2); }
.person-reach { display: flex; flex-wrap: wrap; gap: 2px 14px; margin-top: 3px; }
.person-reach a { display: inline-flex; align-items: center; gap: 6px; min-height: 24px; color: var(--teal-ink); font-size: 13px; text-decoration: none; overflow-wrap: anywhere; }
.person-reach a svg { flex-shrink: 0; color: var(--ink-3); }
.person-reach a:hover { text-decoration: underline; }

.layout { display: grid; grid-template-columns: minmax(0, 1fr) 360px; gap: 16px; align-items: start; }
.main-col, .side-col { display: grid; gap: 16px; min-width: 0; }
.description { margin-bottom: 14px; font-size: 13.5px; line-height: 1.55; color: var(--ink); white-space: pre-line; }
.facts { grid-template-columns: repeat(2, minmax(0, 1fr)); }
.addresses { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
.address-label { margin-bottom: 4px; font: 500 10px/1.4 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
address { display: grid; font-style: normal; font-size: 13.5px; line-height: 1.5; color: var(--ink); overflow-wrap: anywhere; }
.unset { font-size: 13px; color: var(--ink-3); }

.edit-card { display: grid; gap: 18px; max-width: 1100px; padding: 22px 24px 0; }
.edit-foot {
  position: sticky; bottom: 0; z-index: 2; display: flex; align-items: center; justify-content: flex-end; gap: 10px; margin: 0 -24px; padding: 12px 24px;
  border-top: 1px solid var(--line); border-radius: 0 0 var(--radius) var(--radius); background: var(--surface-raised-2); -webkit-backdrop-filter: blur(14px); backdrop-filter: blur(14px);
}
.edit-foot .f-hint { flex: 1; }

.more-menu { display: grid; gap: 1px; }
.menu-item { display: flex; align-items: center; gap: 10px; width: 100%; height: 34px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.menu-item svg { color: var(--ink-2); }
@media (hover: hover) { .menu-item:hover:not(:disabled) { background: var(--row-hover); } }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.menu-item:disabled { color: var(--ink-3); }
.menu-item.danger:not(:disabled), .menu-item.danger:not(:disabled) svg { color: var(--danger); }
.menu-item.danger:hover:not(:disabled) { background: var(--danger-bg); }
.menu-sep { height: 1px; margin: 4px 6px; background: var(--line); }

@media (max-width: 1280px) {
  .layout, .sk-cols { grid-template-columns: minmax(0, 1fr) 320px; }
}
@media (max-width: 1100px) {
  .layout, .sk-cols { grid-template-columns: minmax(0, 1fr); }
  .side-col { grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); }
  .hero { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .hero-cell:nth-child(3) { border-left: 0; }
  .hero-cell:nth-child(n + 3) { border-top: 1px solid var(--line); }
}
@media (max-width: 600px) {
  /* Phones: the contact across, number and rate side by side, the place across. */
  .hero { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .hero-cell { border-left: 0; padding: 14px 16px; }
  .hero-cell.who, .hero-cell:nth-child(4) { grid-column: 1 / -1; }
  .hero-cell:nth-child(3) { border-left: 1px solid var(--line); }
  .hero-cell:nth-child(n + 2) { border-top: 1px solid var(--line); }
  .side-col { grid-template-columns: minmax(0, 1fr); }
  .edit-card { padding: 16px 16px 0; }
  .edit-foot { margin: 0 -16px; padding: 10px 16px; flex-wrap: wrap; }
  .edit-foot .f-hint { display: none; }
  .edit-foot .btn { flex: 1; height: 44px; }
  .facts { grid-template-columns: minmax(0, 1fr); }
}
</style>
