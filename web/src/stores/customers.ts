// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { ref, shallowRef } from 'vue'
import { NO_FILTER, contactDirectory, listCustomers, type ContactCard, type Customer, type CustomerFilter, type NoteProposal } from '../lib/crm'

// Customers for the list and the customer page: loaded once, kept while you move
// between them, so going back finds the list as you left it (search, filters,
// the row you were on). Note proposals made this session stay with their customer
// until applied or set aside; the server keeps every proposal either way.
export const useCustomers = defineStore('customers', () => {
  const items = ref<Customer[] | null>(null)
  const error = ref('')
  const loading = ref(false)
  const directory = shallowRef(new Map<string, ContactCard>())
  const filter = ref<CustomerFilter>({ ...NO_FILTER })
  const cursor = ref<string | null>(null)
  const proposals = ref(new Map<string, NoteProposal>())
  let request: Promise<void> | null = null
  // reset() retires this generation so a read already in flight cannot refill the list.
  let generation = 0

  function load(force = false): Promise<void> {
    if (request) return request
    if (items.value && !error.value && !force) return Promise.resolve()
    loading.value = true
    const run = generation
    request = (async () => {
      try {
        const [list, contacts] = await Promise.all([listCustomers(), contactDirectory().catch(() => null)])
        if (run !== generation) return
        items.value = list.items
        if (contacts) directory.value = contacts.items
        error.value = list.partial || !contacts || contacts.partial ? 'Some customers or contacts could not be loaded.' : ''
      } catch (e) {
        if (run !== generation) return
        error.value = e instanceof Error ? e.message : 'Customers could not be loaded.'
      } finally { if (run === generation) { loading.value = false; request = null } }
    })()
    return request
  }
  function upsert(customer: Customer) {
    if (!items.value) return
    const index = items.value.findIndex(c => c.id === customer.id)
    if (index === -1) items.value = [...items.value, customer]
    else items.value = items.value.map(c => c.id === customer.id ? customer : c)
  }
  function remove(id: string) { if (items.value) items.value = items.value.filter(c => c.id !== id) }
  function setContact(id: string, card: ContactCard | null) {
    const next = new Map(directory.value)
    if (card) next.set(id, card); else next.delete(id)
    directory.value = next
  }
  const primaryOf = (c: Customer) => c.primary_contact_node_id ? directory.value.get(c.primary_contact_node_id) : undefined
  function setProposal(orgId: string, draft: NoteProposal | null) {
    const next = new Map(proposals.value)
    if (draft) next.set(orgId, draft); else next.delete(orgId)
    proposals.value = next
  }
  function reset() {
    generation++; request = null; loading.value = false
    items.value = null; error.value = ''; directory.value = new Map(); filter.value = { ...NO_FILTER }; cursor.value = null; proposals.value = new Map()
  }
  return { items, error, loading, directory, filter, cursor, proposals, load, upsert, remove, setContact, primaryOf, setProposal, reset }
})
