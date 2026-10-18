<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import '../../styles/crm.css'
import { computed, nextTick, ref, watch } from 'vue'
import { getRelated, type RelatedProject } from '../../lib/crm'
import { createQuote, getSettings, lifecycleError, type QuoteProjection } from '../../lib/quotes/lifecycle'
import { listProfiles, type QuoteProfile } from '../../lib/quotes/profile'
import { highlight } from '../../lib/work'
import { useBusiness } from '../../stores/business'
import { useCustomers } from '../../stores/customers'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import BizIcon from './BizIcon.vue'

// A new quote: who it is for and what it is called. The document starts from the
// workspace's sender and texts (the Business settings), the customer's address and
// today's date; its number and the customer's number are assigned on creation.
// Opened from a customer's page, the customer is already chosen.
const emit = defineEmits<{ created: [quote: QuoteProjection, customer: string] }>()
const business = useBusiness()
const customers = useCustomers()
const dialog = ref<HTMLDialogElement>()
const titleInput = ref<HTMLInputElement>()
const customerInput = ref<HTMLInputElement>()
const customerId = ref('')
const fixedCustomer = ref(false)
const fixedName = ref('')
const search = ref('')
const listOpen = ref(false)
const active = ref(0)
const title = ref('')
const projectId = ref('')
const profileId = ref('')
const profiles = ref<QuoteProfile[]>([])
const projects = ref<RelatedProject[] | null>(null)
const busy = ref(false)
const error = ref('')
const touched = ref(false)
const settings = ref<'loading' | 'ready' | 'missing' | 'error'>('loading')
let opener: HTMLElement | null = null

// Archived customers are left out of the choice; one fixed by its page still shows.
const all = computed(() => (customers.items ?? []).filter(c => !c.archived))
const chosen = computed(() => customers.items?.find(c => c.id === customerId.value) ?? null)
const matches = computed(() => {
  const q = search.value.trim().toLowerCase()
  const list = q ? all.value.filter(c => [c.name, c.legal_name, c.customer_no ?? ''].some(v => v.toLowerCase().includes(q))) : all.value
  return [...list].sort((a, b) => a.name.localeCompare(b.name)).slice(0, 50)
})
const problems = computed(() => ({
  customer: !customerId.value ? 'Choose who the quote is for.' : '',
  title: !title.value.trim() ? 'A title is needed.' : title.value.trim().length > 512 ? 'At most 512 characters.' : '',
}))
const dirty = computed(() => !!title.value.trim() || (!fixedCustomer.value && !!customerId.value))
function senderReady(s: Awaited<ReturnType<typeof getSettings>>) {
  if (s.revision < 1 || !s.numbering_time_zone || !/^[A-Z]{3}$/.test(s.default_currency)) return false
  const required = ['company', 'street', 'postal_code', 'city', 'country']
  return required.every(key => typeof s.sender?.[key] === 'string' && (s.sender[key] as string).trim())
    && typeof s.sender?.email === 'string' && /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(s.sender.email)
}

async function open(options: { customerId?: string; customerName?: string } = {}) {
  opener = document.activeElement as HTMLElement
  customerId.value = options.customerId ?? ''; fixedCustomer.value = !!options.customerId; fixedName.value = options.customerName ?? ''
  search.value = ''; title.value = ''; projectId.value = ''; projects.value = null; profileId.value = ''; profiles.value = []
  error.value = ''; busy.value = false; touched.value = false; listOpen.value = false; active.value = 0
  settings.value = 'loading'
  dialog.value?.showModal()
  void customers.load()
  void getSettings().then(s => { settings.value = senderReady(s) ? 'ready' : 'missing'; profileId.value = s.default_profile_id || '' }).catch(() => { settings.value = 'error' })
  void listProfiles().then(items => { profiles.value = items.filter(p => !p.archived) }).catch(() => { profiles.value = [] })
  await nextTick()
  ;(customerId.value ? titleInput.value : customerInput.value)?.focus()
}
function close() { dialog.value?.close(); listOpen.value = false; opener?.focus({ preventScroll: true }) }
// The customer's projects, for an optional link; a quote needs none.
watch(customerId, async id => {
  projects.value = null; projectId.value = ''
  if (!id) return
  try { const related = await getRelated(id); if (customerId.value === id) projects.value = related.projects }
  catch { if (customerId.value === id) projects.value = [] }
})
function pick(id: string) {
  customerId.value = id; listOpen.value = false; search.value = ''
  void nextTick(() => titleInput.value?.focus())
}
function comboKeys(event: KeyboardEvent) {
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault(); listOpen.value = true
    const count = matches.value.length
    if (count) active.value = (active.value + (event.key === 'ArrowDown' ? 1 : -1) + count) % count
  } else if (event.key === 'Enter' && listOpen.value) {
    event.preventDefault()
    const hit = matches.value[active.value]
    if (hit) pick(hit.id)
  } else if (event.key === 'Escape' && listOpen.value) { event.preventDefault(); event.stopPropagation(); listOpen.value = false }
}
watch(search, value => { active.value = 0; if (value) listOpen.value = true })
async function submit() {
  touched.value = true
  if (settings.value !== 'ready') return
  if (problems.value.customer) { if (!fixedCustomer.value) customerInput.value?.focus(); return }
  if (problems.value.title) { titleInput.value?.focus(); return }
  if (busy.value) return
  busy.value = true; error.value = ''
  try {
    const quote = await createQuote({ title: title.value.trim(), customer_org_node_id: customerId.value, ...(projectId.value ? { project_node_id: projectId.value } : {}), ...(profileId.value ? { profile_id: profileId.value } : {}) })
    dialog.value?.close()
    emit('created', quote, customerId.value)
  } catch (e) { error.value = lifecycleError(e, 'The quote was not created. Nothing changed.') }
  finally { busy.value = false }
}
function backdrop(event: MouseEvent) { if (event.target === dialog.value && !dirty.value) close() }
function keys(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); void submit() }
}
defineExpose({ open })
</script>

<template>
  <dialog ref="dialog" class="create" aria-labelledby="new-quote-title" @cancel.prevent="close" @click="backdrop">
    <form class="create-card" novalidate @submit.prevent="submit" @keydown="keys">
      <header class="create-head">
        <div>
          <h2 id="new-quote-title">New quote</h2>
          <p class="lead">It starts from your sender and texts, the customer’s address and today’s date. You write the rest on the page.</p>
        </div>
        <button type="button" class="icon-btn sm flat" aria-label="Close" data-tip="Close · Esc" @click="close"><AppIcon name="close" :size="15" /></button>
      </header>

      <div v-if="settings === 'missing'" class="gate" role="alert">
        <span class="gate-icon"><BizIcon name="seal" :size="16" /></span>
        <div>
          <p class="gate-title">Set up the sender first</p>
          <p v-if="business.admin">A quote carries your company’s name, address and bank details. Add them once in the Business settings; every new quote starts from them.</p>
          <p v-else>A quote carries the company’s name, address and bank details. A workspace admin adds them in the Business settings.</p>
          <RouterLink v-if="business.admin" class="btn sm" to="/settings/business" @click="close">Open Business settings</RouterLink>
        </div>
      </div>
      <p v-else-if="settings === 'error'" class="f-error" role="alert"><AppIcon name="alert" :size="14" />The quote settings could not be read, so nothing can be created right now.</p>

      <div class="f-grid" :class="{ muted: settings === 'missing' }">
        <div class="f-row wide">
          <span id="new-quote-customer-label" class="f-label">Customer</span>
          <div v-if="fixedCustomer" class="fixed-customer"><BizIcon name="building" :size="14" /><span>{{ chosen?.name ?? fixedName }}</span><span v-if="chosen?.customer_no" class="number mono">{{ chosen.customer_no }}</span></div>
          <div v-else class="combo">
            <div v-if="chosen && !listOpen" class="chosen">
              <BizIcon name="building" :size="14" /><span class="chosen-name">{{ chosen.name }}</span><span v-if="chosen.customer_no" class="number mono">{{ chosen.customer_no }}</span>
              <button type="button" class="btn sm ghost" @click="customerId = ''; nextTick(() => customerInput?.focus())">Change</button>
            </div>
            <template v-else>
              <input
                id="new-quote-customer" ref="customerInput" v-model="search" class="field" role="combobox" autocomplete="off" placeholder="Find a customer by name or number"
                aria-labelledby="new-quote-customer-label" aria-autocomplete="list" :aria-expanded="listOpen" aria-controls="new-quote-customers"
                :aria-activedescendant="listOpen && matches[active] ? `new-quote-c-${matches[active]!.id}` : undefined" :aria-invalid="touched && !!problems.customer"
                :disabled="settings === 'missing'" @click="listOpen = true" @keydown="comboKeys" @blur="listOpen = false"
              />
              <ul v-if="listOpen" id="new-quote-customers" class="options" role="listbox" aria-label="Customers">
                <li
                  v-for="(c, i) in matches" :id="`new-quote-c-${c.id}`" :key="c.id" role="option" class="option" :class="{ active: i === active }" :aria-selected="i === active"
                  @mousedown.prevent="pick(c.id)" @mousemove="active = i"
                >
                  <span class="option-name"><template v-for="(part, j) in highlight(c.name, search)" :key="j"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                  <span v-if="c.customer_no" class="number mono">{{ c.customer_no }}</span>
                </li>
                <li v-if="customers.items && !matches.length" class="none" role="presentation">No customer matches. Add one on the Customers page.</li>
                <li v-else-if="!customers.items" class="none" role="presentation">{{ customers.error || 'Loading customers…' }}</li>
              </ul>
            </template>
          </div>
          <p v-if="touched && problems.customer" class="f-note bad" role="alert"><AppIcon name="alert" :size="12" />{{ problems.customer }}</p>
        </div>
        <div class="f-row wide">
          <label class="f-label" for="new-quote-name">Title</label>
          <input id="new-quote-name" ref="titleInput" v-model="title" class="field" maxlength="512" autocomplete="off" placeholder="Website relaunch" :disabled="settings === 'missing'" :aria-invalid="touched && !!problems.title" />
          <p v-if="touched && problems.title" class="f-note bad" role="alert"><AppIcon name="alert" :size="12" />{{ problems.title }}</p>
        </div>
        <div v-if="customerId && projects && projects.length" class="f-row wide">
          <label class="f-label" for="new-quote-project">Project <span class="opt">optional</span></label>
          <select id="new-quote-project" v-model="projectId" class="field" :disabled="settings === 'missing'">
            <option value="">No project</option>
            <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.title }}</option>
          </select>
        </div>
        <div v-if="profiles.length" class="f-row wide">
          <label class="f-label" for="new-quote-profile">Document profile</label>
          <select id="new-quote-profile" v-model="profileId" class="field" :disabled="settings === 'missing'">
            <option value="">Standard document</option>
            <option v-for="profile in profiles" :key="profile.id" :value="profile.id">{{ profile.name }} · r{{ profile.revision }}</option>
          </select>
        </div>
      </div>
      <p v-if="error" class="f-error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
      <footer class="create-foot">
        <p class="f-hint"><KeyCap k="mod" /><KeyCap k="enter" /> creates · <kbd class="keycap">esc</kbd> closes</p>
        <button type="button" class="btn" @click="close">Cancel</button>
        <button type="submit" class="btn primary" :disabled="busy || settings !== 'ready'"><AppIcon name="plus" :size="14" />{{ busy ? 'Creating…' : 'Create quote' }}</button>
      </footer>
    </form>
  </dialog>
</template>

<style scoped>
.create { width: min(var(--dialog-m), calc(100vw - 24px)); max-height: calc(100dvh - 24px); padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.create::backdrop { background: var(--scrim); backdrop-filter: blur(2px); }
.create-card { display: grid; gap: 16px; max-height: calc(100dvh - 24px); overflow: auto; padding: 20px 22px 18px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); }
.create-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
h2 { font-size: 18px; }
.lead { margin-top: 4px; max-width: 46ch; font-size: 13px; color: var(--ink-2); }
.gate { display: flex; gap: 12px; padding: 12px 14px; border-radius: 12px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line-2); font-size: 13px; color: var(--ink-2); }
.gate > div { display: grid; gap: 6px; justify-items: start; }
.gate-title { font-weight: 650; color: var(--ink); }
.gate-icon { display: grid; place-items: center; flex-shrink: 0; width: 32px; height: 32px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.muted { opacity: .55; }
.fixed-customer, .chosen { display: flex; align-items: center; gap: 8px; min-height: 38px; padding: 0 6px 0 12px; border-radius: 10px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); font-size: 13.5px; font-weight: 600; }
.fixed-customer svg, .chosen svg { flex-shrink: 0; color: var(--ink-3); }
.chosen-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fixed-customer { padding-right: 12px; }
/* In the flow of the form: the dialog grows with it, so no choice is ever cut off. */
.options { max-height: 220px; overflow: auto; margin: 4px 0 0; padding: 4px; list-style: none; border-radius: 12px; border: 1px solid var(--line-2); background: var(--surface-raised); box-shadow: var(--shadow); }
.option { display: flex; align-items: center; gap: 10px; min-height: 34px; padding: 0 10px; border-radius: 8px; font-size: 13.5px; cursor: pointer; }
.option.active { background: var(--row-selected); }
.option-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.none { padding: 8px 10px; font-size: 13px; color: var(--ink-3); }
.number { flex-shrink: 0; padding: 1px 6px; border-radius: 6px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font: 500 11.5px/16px var(--mono); color: var(--ink-2); font-variant-ligatures: none; }
.create-foot { display: flex; align-items: center; justify-content: flex-end; gap: 8px; }
.create-foot .f-hint { flex: 1; }
@media (max-width: 600px) {
  .create-card { padding: 16px; }
  .create-foot { flex-wrap: wrap; }
  .create-foot .f-hint { display: none; }
  .create-foot .btn { flex: 1; height: 44px; }
}
</style>
