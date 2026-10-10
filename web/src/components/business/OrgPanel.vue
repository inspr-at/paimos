<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<!-- Parked (AEON-70, 2026-09-24): quotes and organisations will be ported from Markus's current classic Paimos quote builder; this file is not routed or linked. -->
<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { APIError, deleteNode, getRelations, updateNode, type ListItem, type Relation } from '../../lib/api'
import { bindContact, createRelation, deleteRelation, listBindings, type Binding } from '../../lib/business'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { absoluteTime, initials, relativeTime } from '../../lib/work'
import type { SaveResult } from '../../lib/useTicket'
import { useBusiness } from '../../stores/business'
import { useProjects } from '../../stores/projects'
import AppIcon from './BizIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import InlineTitle from '../work/InlineTitle.vue'
import MoneyText from './MoneyText.vue'
import PickerMenu, { type PickOption } from './PickerMenu.vue'
import QuoteStatus from './QuoteStatus.vue'

// One organisation in the docked panel: its details, contacts (with the person
// each is bound to, which is what lets them accept an offer), the projects it is
// the customer of, and its quotes.
const props = defineProps<{ org: ListItem | null; orgKey: string; position: { index: number; count: number } | null; now: number; resolveError: string }>()
const emit = defineEmits<{ close: []; prev: []; next: []; removed: [id: string] }>()
const business = useBusiness()
const projects = useProjects()
const router = useRouter()
const root = ref<HTMLElement>()
const title = ref<InstanceType<typeof InlineTitle>>()
const relations = ref<Relation[]>([])
const relationsState = ref<'loading' | 'ready' | 'error'>('loading')
const bindings = reactive(new Map<string, Binding[]>())
const editingField = ref('')
const fieldDraft = ref('')
const editingContact = ref('')
const contactDraft = reactive<Record<string, string>>({})
const newContact = reactive({ name: '', email: '' })
const adding = ref(false)
const menu = ref<{ kind: 'bind' | 'project' | 'more' | 'contact'; anchor: HTMLElement; contactId?: string } | null>(null)
const newName = ref<HTMLInputElement>()

const id = computed(() => props.org?.id ?? '')
const orgFields = computed(() => ['legal_name', 'website'].filter(field => business.fieldsOf('organisation').includes(field)))
const contactFields = computed(() => ['email', 'phone', 'role', 'note'].filter(field => business.fieldsOf('contact').includes(field)))
const LABEL: Record<string, string> = { legal_name: 'Legal name', website: 'Website', email: 'Email', phone: 'Phone', role: 'Role', note: 'Note' }
const contactIds = computed(() => relations.value.filter(r => r.type === 'contact_for' && r.target_node_id === id.value).map(r => r.source_node_id))
const orgContacts = computed(() => contactIds.value.map(c => business.contact(c)).filter((c): c is ListItem => !!c))
const projectLinks = computed(() => relations.value.filter(r => r.type === 'customer_of' && r.source_node_id === id.value && !!projects.byId(r.target_node_id)))
const orgQuotes = computed(() => business.quotes.filter(q => q.customer_org_node_id === id.value))
const text = (value: unknown) => typeof value === 'string' ? value : ''
const site = (value: string) => value.replace(/^https?:\/\//, '').replace(/\/$/, '')
const href = (value: string) => /^https?:\/\//.test(value) ? value : `https://${value}`

async function load() {
  const orgId = id.value
  if (!orgId) return
  relationsState.value = 'loading'; editingField.value = ''; editingContact.value = ''; menu.value = null
  void business.loadPrincipals(); void business.loadKinds(); void projects.load()
  try {
    const { items } = await getRelations(orgId)
    if (orgId !== id.value) return
    relations.value = items
    relationsState.value = 'ready'
    syncLinks()
    await Promise.all(contactIds.value.map(loadBindings))
  } catch { if (orgId === id.value) relationsState.value = 'error' }
}
function syncLinks() {
  business.setLinks(id.value, {
    contacts: contactIds.value,
    projects: projectLinks.value.map(r => r.target_node_id),
    quotes: relations.value.filter(r => r.type === 'customer_of' && r.source_node_id === id.value && !projects.byId(r.target_node_id)).map(r => r.target_node_id),
  })
}
async function loadBindings(contactId: string) {
  try { bindings.set(contactId, await listBindings(contactId)) } catch { bindings.set(contactId, []) }
}
watch(id, () => { relations.value = []; bindings.clear(); void load() }, { immediate: true })

// ---------- Organisation ----------
async function patchNode(node: ListItem, patch: { title?: string; fields?: Record<string, unknown> }, list: 'organisations' | 'contacts'): Promise<SaveResult> {
  try {
    const saved = await updateNode(node.id, patch, { ifUnmodifiedSince: node.updated_at })
    business.upsertNode(list, { ...node, ...saved })
    return 'ok'
  } catch (e) {
    if (e instanceof APIError && e.status === 412) {
      await business.loadCRM(true)
      toast(`${node.title} was changed elsewhere. The newer version is shown; your change is kept.`, { tone: 'error' })
      return 'conflict'
    }
    toast(`Not saved: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' })
    return 'error'
  }
}
const saveTitle = (value: string) => props.org ? patchNode(props.org, { title: value }, 'organisations') : Promise.resolve('error' as const)
async function startField(field: string) {
  editingField.value = field; fieldDraft.value = text(props.org?.fields[field])
  await nextTick(); root.value?.querySelector<HTMLInputElement>(`[data-field="${field}"]`)?.focus()
}
async function saveField() {
  const org = props.org, field = editingField.value
  if (!org || !field) return
  const value = fieldDraft.value.trim()
  if (value === text(org.fields[field])) { editingField.value = ''; return }
  const fields = { ...org.fields }
  if (value) fields[field] = value; else delete fields[field]
  if (await patchNode(org, { fields }, 'organisations') === 'ok') editingField.value = ''
}
function fieldKeys(event: KeyboardEvent) {
  if (event.key === 'Enter') { event.preventDefault(); void saveField() }
  else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); editingField.value = '' }
}
async function removeOrg() {
  const org = props.org
  if (!org) return
  menu.value = null
  const ok = await confirmAction({
    title: `Delete ${org.title}?`,
    body: orgQuotes.value.length ? `${org.title} is the customer of ${orgQuotes.value.length} ${orgQuotes.value.length === 1 ? 'quote' : 'quotes'}; they keep their frozen versions. The organisation leaves this list; its history stays in the audit log.` : 'The organisation leaves this list; its history stays in the audit log.',
    confirmLabel: 'Delete organisation', danger: true,
  })
  if (!ok) return
  try { await deleteNode(org.id); business.removeNode('organisations', org.id); toast(`${org.title} deleted.`); emit('removed', org.id) }
  catch (e) { toast(`Not deleted: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' }) }
}

// ---------- Contacts ----------
async function addContact() {
  const org = props.org
  const name = newContact.name.trim()
  if (!org || !name || adding.value) return
  adding.value = true
  try {
    const fields: Record<string, unknown> = {}
    if (newContact.email.trim() && contactFields.value.includes('email')) fields.email = newContact.email.trim()
    const node = await business.createCRMNode('contact', name, fields)
    const rel = await createRelation(node.id, org.id, 'contact_for')
    business.upsertNode('contacts', node)
    business.setContactOrg(node.id, org.id)
    relations.value = [...relations.value, { ...rel, type: 'contact_for' } as Relation]
    bindings.set(node.id, [])
    syncLinks()
    newContact.name = ''; newContact.email = ''
    await nextTick(); newName.value?.focus()
  } catch (e) { toast(`The contact was not added: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' }) }
  finally { adding.value = false }
}
function addKeys(event: KeyboardEvent) { if (event.key === 'Enter') { event.preventDefault(); void addContact() } }
async function editContact(contact: ListItem) {
  editingContact.value = contact.id
  for (const key of Object.keys(contactDraft)) delete contactDraft[key]
  contactDraft.title = contact.title
  for (const field of contactFields.value) contactDraft[field] = text(contact.fields[field])
  menu.value = null
  await nextTick(); root.value?.querySelector<HTMLInputElement>(`[data-contact-edit="${contact.id}"]`)?.focus()
}
async function saveContact(contact: ListItem) {
  const fields = { ...contact.fields }
  for (const field of contactFields.value) { const value = (contactDraft[field] ?? '').trim(); if (value) fields[field] = value; else delete fields[field] }
  const title = (contactDraft.title ?? '').trim() || contact.title
  if (await patchNode(contact, { title, fields }, 'contacts') === 'ok') editingContact.value = ''
}
async function unlinkContact(contactId: string) {
  const contact = business.contact(contactId)
  menu.value = null
  const rel = relations.value.find(r => r.type === 'contact_for' && r.source_node_id === contactId && r.target_node_id === id.value)
  if (!rel || !contact) return
  const ok = await confirmAction({ title: `Remove ${contact.title} from ${props.org?.title}?`, body: 'The contact stays in the workspace; only the link to this organisation goes. Quotes already issued to them keep their frozen recipient.', confirmLabel: 'Remove contact', danger: true })
  if (!ok) return
  try { await deleteRelation(rel.id); relations.value = relations.value.filter(r => r.id !== rel.id); syncLinks() }
  catch (e) { toast(`Not removed: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' }) }
}
const peopleOptions = computed<PickOption[]>(() => business.people.map(p => ({ value: p.id, label: p.name, hint: 'Person', icon: 'user' })))
async function bind(option: PickOption) {
  const contactId = menu.value?.contactId
  const anchor = menu.value?.anchor
  menu.value = null
  if (!contactId) return
  const contact = business.contact(contactId)
  const ok = await confirmAction({
    title: `Bind ${option.label} to ${contact?.title ?? 'this contact'}?`,
    body: `${option.label} can then accept offers issued to ${contact?.title ?? 'this contact'} for ${props.org?.title}, when signed in. Nothing else changes for them.`,
    confirmLabel: 'Bind person',
  })
  anchor?.focus()
  if (!ok) return
  try { await bindContact(contactId, option.value); await loadBindings(contactId); toast(`${option.label} is bound to ${contact?.title}.`) }
  catch (e) { toast(`Not bound: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' }) }
}

// ---------- Projects ----------
const projectOptions = computed<PickOption[]>(() => projects.projects.filter(p => !p.archived && !projectLinks.value.some(r => r.target_node_id === p.id)).map(p => ({ value: p.id, label: p.title, badge: p.routeKey })))
async function linkProject(option: PickOption) {
  menu.value = null
  if (!props.org) return
  try { const rel = await createRelation(props.org.id, option.value, 'customer_of'); relations.value = [...relations.value, { ...rel, type: 'customer_of' } as Relation]; syncLinks(); toast(`${props.org.title} is now the customer of ${option.label}.`) }
  catch (e) { toast(`Not linked: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' }) }
}
async function unlinkProject(rel: Relation) {
  const project = projects.byId(rel.target_node_id)
  const ok = await confirmAction({ title: `Unlink ${project?.title ?? 'this project'}?`, body: `${props.org?.title} stops being its customer. Existing quotes keep their project.`, confirmLabel: 'Unlink project', danger: true })
  if (!ok) return
  try { await deleteRelation(rel.id); relations.value = relations.value.filter(r => r.id !== rel.id); syncLinks() }
  catch (e) { toast(`Not unlinked: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' }) }
}

function openMenu(kind: 'bind' | 'project' | 'more' | 'contact', event: MouseEvent, contactId?: string) {
  const anchor = event.currentTarget as HTMLElement
  menu.value = menu.value?.kind === kind && menu.value.contactId === contactId ? null : { kind, anchor, contactId }
}
function closeMenu(restore: boolean) { const anchor = menu.value?.anchor; menu.value = null; if (restore) anchor?.focus() }
function copy(value: string, label: string) { navigator.clipboard.writeText(value).then(() => toast(`Copied ${label}`), () => toast(`${label} could not be copied`, { tone: 'error' })); menu.value = null }
function copyLink() { if (props.org) copy(`${location.origin}/business/organisations/${encodeURIComponent(props.org.key)}`, 'link') }
function newQuote() { if (props.org) void router.push({ path: '/business/quotes', query: { customer: props.org.id, new: '1' } }) }
defineExpose({ el: root, editTitle: () => title.value?.start(), focusAddContact: () => newName.value?.focus(), isDirty: () => !!editingField.value || !!editingContact.value || !!newContact.name.trim() })
</script>

<template>
  <aside ref="root" class="org-panel" aria-label="Organisation details" tabindex="-1">
    <header class="panel-bar">
      <button type="button" class="key-chip" :aria-label="`Copy ${org?.key ?? orgKey}`" :data-tip="`Copy ${org?.key ?? orgKey}`" @click="copy(org?.key ?? orgKey, org?.key ?? orgKey)"><AppIcon name="building" :size="12" /><span>{{ org?.key ?? orgKey }}</span><AppIcon name="copy" :size="11" class="copy-glyph" /></button>
      <span v-if="position" class="position mono">{{ position.index + 1 }} / {{ position.count }}</span>
      <div class="nav">
        <button type="button" class="icon-btn sm flat" aria-label="Previous organisation" aria-keyshortcuts="k" data-tip="Previous · k" :disabled="!position || position.index === 0" @click="emit('prev')"><AppIcon name="chevron-up" :size="15" /></button>
        <button type="button" class="icon-btn sm flat" aria-label="Next organisation" aria-keyshortcuts="j" data-tip="Next · j" :disabled="!position || position.index >= position.count - 1" @click="emit('next')"><AppIcon name="chevron" :size="15" /></button>
      </div>
      <span class="spacer" />
      <button type="button" class="icon-btn sm flat" aria-label="More actions" aria-haspopup="menu" :aria-expanded="menu?.kind === 'more'" data-tip="More" @click="openMenu('more', $event)"><AppIcon name="more" :size="15" /></button>
      <button type="button" class="icon-btn sm flat" aria-label="Close organisation details" aria-keyshortcuts="Escape" data-tip="Close · Esc" @click="emit('close')"><AppIcon name="close" :size="15" /></button>
    </header>

    <div class="scroll">
      <div v-if="!org && resolveError" class="ws-state" role="alert">
        <span class="state-icon"><AppIcon name="building" :size="18" /></span>
        <h2>This organisation is not here</h2>
        <p>{{ resolveError }}</p>
        <button type="button" class="btn" @click="emit('close')">Back to organisations</button>
      </div>
      <div v-else-if="!org" class="sk" role="status" aria-label="Loading organisation"><span class="skeleton t" /><span class="skeleton" /><span class="skeleton s" /></div>
      <template v-else>
        <InlineTitle ref="title" :value="org.title" :editable="business.staff" :save="saveTitle" />
        <dl class="fields">
          <div v-for="field in orgFields" :key="field" class="field-row">
            <dt>{{ LABEL[field] }}</dt>
            <dd>
              <input v-if="editingField === field" v-model="fieldDraft" class="field inline-input" :data-field="field" :aria-label="LABEL[field]" :placeholder="field === 'website' ? 'example.com' : 'Registered name'" maxlength="500" @keydown="fieldKeys" @blur="saveField" />
              <template v-else>
                <a v-if="field === 'website' && text(org.fields[field])" class="value link" :href="href(text(org.fields[field]))" target="_blank" rel="noopener noreferrer"><AppIcon name="globe" :size="13" />{{ site(text(org.fields[field])) }}</a>
                <span v-else-if="text(org.fields[field])" class="value">{{ text(org.fields[field]) }}</span>
                <span v-else class="value unset">Not set</span>
                <button v-if="business.staff" type="button" class="icon-btn sm flat edit" :aria-label="`Edit ${LABEL[field].toLowerCase()}`" :data-tip="`Edit ${LABEL[field].toLowerCase()}`" @click="startField(field)"><AppIcon name="edit" :size="12" /></button>
              </template>
            </dd>
          </div>
        </dl>
        <p class="meta">Updated <time :datetime="org.updated_at" :data-tip="absoluteTime(org.updated_at)">{{ relativeTime(org.updated_at, { now, long: true }) }}</time></p>
        <div class="divider" />

        <section class="block" aria-labelledby="contacts-title">
          <div class="block-head"><h3 id="contacts-title" class="eyebrow">Contacts</h3><span class="count mono">{{ orgContacts.length }}</span></div>
          <div v-if="relationsState === 'loading'" class="sk rows"><span class="skeleton" /><span class="skeleton s" /></div>
          <p v-else-if="relationsState === 'error'" class="inline-error" role="alert"><AppIcon name="alert" :size="14" />Contacts could not be loaded. <button type="button" class="btn sm" @click="load">Try again</button></p>
          <ul v-else class="contacts" aria-label="Contacts">
            <li v-for="contact in orgContacts" :key="contact.id" class="contact" :class="{ editing: editingContact === contact.id }">
              <template v-if="editingContact !== contact.id">
                <span class="avatar" aria-hidden="true">{{ initials(contact.title) }}</span>
                <span class="c-text">
                  <span class="c-name">{{ contact.title }}<span v-if="text(contact.fields.role)" class="c-role">{{ text(contact.fields.role) }}</span></span>
                  <span class="c-meta">
                    <a v-if="text(contact.fields.email)" :href="`mailto:${text(contact.fields.email)}`"><AppIcon name="mail" :size="11" />{{ text(contact.fields.email) }}</a>
                    <span v-if="text(contact.fields.phone)"><AppIcon name="phone" :size="11" />{{ text(contact.fields.phone) }}</span>
                  </span>
                  <span class="c-bind">
                    <template v-if="(bindings.get(contact.id) ?? []).length">
                      <span v-for="b in bindings.get(contact.id)" :key="b.principal_id" class="bound" :data-tip="`Bound ${relativeTime(b.bound_at, { now, long: true })} by ${business.nameOf(b.bound_by_principal_id)}`"><AppIcon name="shield" :size="11" />{{ b.principal_name }} can accept offers</span>
                    </template>
                    <span v-else class="unbound">Not bound to a person</span>
                    <button v-if="business.admin" type="button" class="link-btn" aria-haspopup="dialog" @click="openMenu('bind', $event, contact.id)">Bind person</button>
                  </span>
                </span>
                <button v-if="business.staff" type="button" class="icon-btn sm flat c-more" :aria-label="`Actions for ${contact.title}`" aria-haspopup="menu" @click="openMenu('contact', $event, contact.id)"><AppIcon name="more" :size="14" /></button>
              </template>
              <form v-else class="contact-edit" @submit.prevent="saveContact(contact)" @keydown.esc.stop.prevent="editingContact = ''">
                <label><span>Name</span><input v-model="contactDraft.title" class="field" :data-contact-edit="contact.id" maxlength="512" /></label>
                <label v-for="field in contactFields" :key="field"><span>{{ LABEL[field] }}</span><input v-model="contactDraft[field]" class="field" :type="field === 'email' ? 'email' : 'text'" maxlength="320" /></label>
                <div class="edit-actions"><button type="button" class="btn sm" @click="editingContact = ''">Cancel</button><button type="submit" class="btn sm on">Save contact</button></div>
              </form>
            </li>
            <li v-if="!orgContacts.length" class="empty-line">No contacts yet. Add the people you send offers to.</li>
          </ul>
          <div v-if="business.staff && relationsState === 'ready'" class="add-contact" @keydown="addKeys">
            <AppIcon name="plus" :size="13" class="add-icon" />
            <input ref="newName" v-model="newContact.name" class="field" placeholder="Add a contact: name" aria-label="New contact name" maxlength="512" autocomplete="off" />
            <input v-if="contactFields.includes('email')" v-model="newContact.email" class="field" type="email" placeholder="Email" aria-label="New contact email" autocomplete="off" />
            <button type="button" class="btn sm" :disabled="!newContact.name.trim() || adding" @click="addContact">{{ adding ? 'Adding…' : 'Add' }}</button>
          </div>
        </section>

        <section class="block" aria-labelledby="org-projects-title">
          <div class="block-head">
            <h3 id="org-projects-title" class="eyebrow">Customer of</h3><span class="count mono">{{ projectLinks.length }}</span>
            <span class="spacer" />
            <button v-if="business.staff" type="button" class="link-btn" aria-haspopup="dialog" @click="openMenu('project', $event)"><AppIcon name="link" :size="12" />Link project</button>
          </div>
          <ul v-if="projectLinks.length" class="project-chips" aria-label="Projects">
            <li v-for="rel in projectLinks" :key="rel.id" class="project-chip">
              <RouterLink :to="`/p/${encodeURIComponent(projects.byId(rel.target_node_id)!.routeKey)}`"><span class="key-badge">{{ projects.byId(rel.target_node_id)!.routeKey }}</span>{{ projects.byId(rel.target_node_id)!.title }}</RouterLink>
              <button v-if="business.staff" type="button" class="chip-x" :aria-label="`Unlink ${projects.byId(rel.target_node_id)!.title}`" @click="unlinkProject(rel)"><AppIcon name="close" :size="11" /></button>
            </li>
          </ul>
          <p v-else-if="relationsState === 'ready'" class="empty-line">Not the customer of any project yet.</p>
        </section>

        <section class="block" aria-labelledby="org-quotes-title">
          <div class="block-head">
            <h3 id="org-quotes-title" class="eyebrow">Quotes</h3><span class="count mono">{{ orgQuotes.length }}</span>
            <span class="spacer" />
            <button v-if="business.staff && business.open.quotes" type="button" class="link-btn" @click="newQuote"><AppIcon name="plus" :size="12" />New quote</button>
          </div>
          <ul v-if="orgQuotes.length" class="quote-list" aria-label="Quotes">
            <li v-for="q in orgQuotes" :key="q.quote_node_id">
              <RouterLink class="quote-line" :to="`/business/quotes/${encodeURIComponent(q.key)}`">
                <span class="key-badge">{{ q.key }}</span><span class="q-title">{{ q.title }}</span><QuoteStatus :state="q.state" />
                <MoneyText v-if="q.current" :amount="q.current.total" :currency="q.current.currency" /><span v-else class="unset">No version</span>
              </RouterLink>
            </li>
          </ul>
          <p v-else class="empty-line">No quotes for {{ org.title }} yet.</p>
        </section>
      </template>
    </div>

    <PickerMenu v-if="menu?.kind === 'bind'" :anchor="menu.anchor" title="Bind a person" :options="peopleOptions" placeholder="Find a person…" empty="No person in this workspace matches." @choose="bind" @close="closeMenu" />
    <PickerMenu v-if="menu?.kind === 'project'" :anchor="menu.anchor" title="Customer of" :options="projectOptions" placeholder="Find a project…" @choose="linkProject" @close="closeMenu" />
    <FloatingPanel v-if="menu?.kind === 'contact' && menu.contactId" :anchor="menu.anchor" :width="230" align="end" label="Contact actions" @close="closeMenu">
      <div class="more-menu" role="menu" aria-label="Contact actions">
        <button type="button" role="menuitem" class="menu-item" data-autofocus @click="editContact(business.contact(menu.contactId!)!)"><AppIcon name="edit" :size="14" />Edit contact</button>
        <div class="menu-sep" role="separator" />
        <button type="button" role="menuitem" class="menu-item danger" @click="unlinkContact(menu.contactId!)"><AppIcon name="close" :size="14" />Remove from organisation…</button>
      </div>
    </FloatingPanel>
    <FloatingPanel v-if="menu?.kind === 'more' && org" :anchor="menu.anchor" :width="230" align="end" :label="`Actions for ${org.title}`" @close="closeMenu">
      <div class="more-menu" role="menu" :aria-label="`Actions for ${org.title}`">
        <button type="button" role="menuitem" class="menu-item" data-autofocus @click="copyLink"><AppIcon name="link" :size="14" />Copy link</button>
        <button type="button" role="menuitem" class="menu-item" @click="copy(org.key, org.key)"><AppIcon name="copy" :size="14" />Copy key</button>
        <div class="menu-sep" role="separator" />
        <button type="button" role="menuitem" class="menu-item danger" :disabled="!business.staff" @click="removeOrg"><AppIcon name="trash" :size="14" />Delete organisation…</button>
      </div>
    </FloatingPanel>
  </aside>
</template>

<style scoped>
.org-panel {
  position: fixed; z-index: 15; top: calc(var(--header-h) + 10px); right: 10px; bottom: calc(var(--footer-h) + 10px); width: min(560px, calc(100vw - 20px));
  display: flex; flex-direction: column; min-height: 0; outline: none; border-radius: var(--radius); border: 1px solid var(--glass-edge);
  background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow);
  -webkit-backdrop-filter: blur(20px) saturate(1.15); backdrop-filter: blur(20px) saturate(1.15);
}
.org-panel:focus-visible { box-shadow: var(--shadow-pop), var(--focus-ring); }
@media (min-width: 1100px) { .org-panel { width: var(--panel-w); } }
@media (prefers-reduced-motion: no-preference) {
  .org-panel { animation: panel-in .22s cubic-bezier(.2, .7, .2, 1); }
  @keyframes panel-in { from { opacity: 0; transform: translateX(24px); } to { opacity: 1; transform: none; } }
}
.panel-bar { display: flex; align-items: center; gap: 6px; height: 52px; padding: 0 10px 0 14px; border-bottom: 1px solid var(--line); flex-shrink: 0; }
.key-chip { display: inline-flex; flex-shrink: 0; align-items: center; gap: 6px; height: 26px; padding: 0 9px 0 10px; border: 0; border-radius: 7px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 12px/1 var(--mono); letter-spacing: .03em; font-variant-ligatures: none; }
.key-chip:hover { box-shadow: inset 0 0 0 1px var(--teal); }
.key-chip:focus-visible { box-shadow: var(--focus-ring); }
.copy-glyph { opacity: .45; }
.position { margin-left: 6px; font-size: 11.5px; color: var(--ink-3); }
.nav { display: inline-flex; gap: 2px; margin-left: 2px; }
.nav .icon-btn:disabled { opacity: .35; }
.spacer { flex: 1; }
.scroll { flex: 1; min-height: 0; overflow: auto; overscroll-behavior: contain; padding: 18px 22px 28px; }
.fields { display: grid; gap: 2px; margin: 12px 0 0; }
.field-row { display: grid; grid-template-columns: 100px minmax(0, 1fr); align-items: center; min-height: 34px; }
.field-row dt { font: 500 10.5px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.field-row dd { display: flex; align-items: center; gap: 4px; margin: 0; min-width: 0; }
.value { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13.5px; color: var(--ink); }
.value.link { display: inline-flex; align-items: center; gap: 6px; color: var(--teal-ink); text-decoration: none; }
.value.link:hover { text-decoration: underline; }
.unset { color: var(--ink-3); font-size: 13px; }
.edit { width: 26px; height: 26px; color: var(--ink-3); opacity: 0; }
.field-row:hover .edit, .edit:focus-visible { opacity: 1; }
@media (hover: none) { .edit { opacity: 1; } }
.inline-input { height: 30px; font-size: 13.5px; }
.meta { margin-top: 10px; font-size: 12.5px; color: var(--ink-3); }
.meta time { color: var(--ink-2); }
.divider { height: 1px; margin: 16px 0 18px; background: linear-gradient(90deg, var(--line-2), transparent); }
.block { margin-top: 24px; }
.block:first-of-type { margin-top: 0; }
.block-head { display: flex; align-items: center; gap: 8px; min-height: 28px; margin-bottom: 8px; }
.count { display: inline-grid; place-items: center; min-width: 20px; height: 18px; padding: 0 6px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font-size: 10.5px; color: var(--ink-2); }
.contacts { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.contact { display: flex; align-items: flex-start; gap: 12px; padding: 10px 8px; border-radius: 10px; }
@media (hover: hover) { .contact:not(.editing):hover { background: var(--row-hover); } }
.avatar { display: grid; place-items: center; flex-shrink: 0; width: 32px; height: 32px; border-radius: 50%; background: var(--avatar-bg); box-shadow: 0 0 0 1px var(--glass-rim); color: var(--teal-ink); font: 700 11px/1 var(--mono); font-variant-ligatures: none; }
.c-text { display: grid; gap: 3px; flex: 1; min-width: 0; }
.c-name { display: flex; align-items: baseline; gap: 8px; font-size: 14px; font-weight: 650; color: var(--ink); }
.c-role { font-size: 12px; font-weight: 400; color: var(--ink-2); }
.c-meta { display: flex; flex-wrap: wrap; gap: 4px 14px; font-size: 12.5px; color: var(--ink-2); }
.c-meta > * { display: inline-flex; align-items: center; gap: 5px; min-width: 0; }
.c-meta a { color: var(--ink-2); text-decoration: none; }
.c-meta a:hover { color: var(--teal-ink); text-decoration: underline; }
.c-meta svg { color: var(--ink-3); }
.c-bind { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 10px; margin-top: 2px; font-size: 12px; }
.bound { display: inline-flex; align-items: center; gap: 5px; height: 20px; padding: 0 8px; border-radius: 999px; background: var(--ok-bg); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--ok) 28%, transparent); color: var(--ok); font-weight: 600; }
.unbound { color: var(--ink-3); }
.link-btn { display: inline-flex; align-items: center; gap: 5px; height: 24px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12px; font-weight: 600; }
.link-btn:hover { background: var(--row-hover); }
.link-btn:focus-visible { box-shadow: var(--focus-ring); }
.c-more { flex-shrink: 0; color: var(--ink-3); }
.contact-edit { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px 10px; width: 100%; }
.contact-edit label { display: grid; gap: 4px; }
.contact-edit label span { font: 500 10px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.contact-edit .field { height: 32px; font-size: 13px; }
.edit-actions { grid-column: 1 / -1; display: flex; justify-content: flex-end; gap: 8px; }
.add-contact { display: flex; align-items: center; gap: 8px; margin-top: 8px; padding: 6px 6px 6px 12px; border-radius: 12px; box-shadow: inset 0 0 0 1px var(--line); }
.add-contact .field { height: 30px; font-size: 13px; }
.add-contact .field:first-of-type { flex: 1.2; }
.add-contact .field + .field { flex: 1; }
.add-icon { color: var(--ink-3); }
.project-chips { display: flex; flex-wrap: wrap; gap: 6px; margin: 0; padding: 0; list-style: none; }
.project-chip { display: inline-flex; align-items: center; height: 30px; padding: 0 4px 0 6px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font-size: 12.5px; }
.project-chip a { display: inline-flex; align-items: center; gap: 7px; color: var(--ink); text-decoration: none; }
.project-chip .key-badge { height: 20px; padding: 0 6px; font-size: 10.5px; }
.chip-x { display: grid; place-items: center; width: 24px; height: 24px; margin-left: 2px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: var(--ink-3); }
.chip-x:hover { color: var(--danger); background: var(--danger-bg); }
.quote-list { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.quote-line { display: grid; grid-template-columns: max-content minmax(0, 1fr) auto auto; align-items: center; gap: 10px; min-height: 40px; padding: 4px 8px; border-radius: 10px; color: var(--ink); text-decoration: none; font-size: 13px; }
@media (hover: hover) { .quote-line:hover { background: var(--row-hover); } }
.quote-line:focus-visible { box-shadow: var(--focus-ring); }
.q-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.empty-line { padding: 4px 2px; font-size: 13px; color: var(--ink-3); list-style: none; }
.inline-error { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; padding: 8px 12px; border-radius: 10px; background: var(--danger-bg); color: var(--danger); font-size: 13px; }
.sk { display: grid; gap: 14px; }
.sk .t { width: 60%; height: 18px; } .sk .s { width: 40%; }
.sk.rows { padding: 8px 0; }
.ws-state { display: grid; justify-items: center; gap: 8px; padding: 56px 16px; text-align: center; }
.ws-state h2 { font-size: 17px; }
.ws-state .btn { margin-top: 8px; }
.state-icon { display: grid; place-items: center; width: 44px; height: 44px; margin-bottom: 4px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.more-menu { display: grid; gap: 1px; }
.menu-item { display: flex; align-items: center; gap: 10px; width: 100%; height: 34px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.menu-item svg { color: var(--ink-2); }
@media (hover: hover) { .menu-item:hover:not(:disabled) { background: var(--row-hover); } }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.menu-item.danger, .menu-item.danger svg { color: var(--danger); }
.menu-item.danger:hover:not(:disabled) { background: var(--danger-bg); }
.menu-sep { height: 1px; margin: 4px 6px; background: var(--line); }
@media (max-width: 720px) {
  .org-panel { z-index: 40; inset: 0; width: auto; height: 100dvh; border-radius: 0; border: 0; background: var(--canvas); }
  .panel-bar { height: 56px; padding: 0 6px 0 12px; }
  .panel-bar .icon-btn { width: 44px; height: 44px; }
  .position { display: none; }
  .scroll { padding: 16px 16px 24px; }
  .add-contact { flex-wrap: wrap; }
  .add-contact .field { flex: 1 1 100% !important; height: 44px; font-size: 16px; }
  .add-contact .btn { height: 40px; margin-left: auto; }
  .contact-edit { grid-template-columns: minmax(0, 1fr); }
  .quote-line { grid-template-columns: max-content minmax(0, 1fr) auto; }
  .quote-line .money { grid-column: 2 / -1; justify-self: end; }
  @media (prefers-reduced-motion: no-preference) { .org-panel { animation-name: sheet-in; } @keyframes sheet-in { from { transform: translateY(24px); opacity: 0; } to { transform: none; opacity: 1; } } }
}
</style>
