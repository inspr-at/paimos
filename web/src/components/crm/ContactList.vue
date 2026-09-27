<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, reactive, ref } from 'vue'
import { blankContact, contactWrite, createContact, deleteContact, errorText, makePrimary, telHref, undoLatest, updateContact, validEmail, type Contact, type ContactWrite, type Customer } from '../../lib/crm'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'
import BizIcon from '../business/BizIcon.vue'

// The people at a customer. The primary contact leads (the first one added
// becomes it); admins add, edit, remove and choose the primary, each with Undo.
const props = defineProps<{ customer: Customer; contacts: Contact[] | null; admin: boolean; locked?: boolean; error?: string }>()
const emit = defineEmits<{ changed: [] }>()
const editing = ref<string | null>(null) // a contact id, or 'new'
const form = reactive<ContactWrite>(blankContact())
const touched = ref(false)
const busy = ref(false)
const formError = ref('')
const root = ref<HTMLElement>()
const sorted = computed(() => [...(props.contacts ?? [])].sort((a, b) => Number(b.primary) - Number(a.primary) || a.name.localeCompare(b.name)))
const problems = computed(() => ({
  name: !form.name.trim() ? 'A name is needed.' : '',
  email: validEmail(form.email.trim()) ? '' : 'Use an address like jana@hofer.at.',
}))

async function start(contact: Contact | null) {
  editing.value = contact?.id ?? 'new'
  Object.assign(form, contact ? contactWrite(contact) : blankContact())
  touched.value = false; formError.value = ''
  await nextTick()
  root.value?.querySelector<HTMLInputElement>('.contact-form input')?.focus()
}
function cancel() {
  const id = editing.value
  editing.value = null
  void nextTick(() => (id && id !== 'new' ? root.value?.querySelector<HTMLElement>(`[data-contact="${id}"] .edit-contact`) : root.value?.querySelector<HTMLElement>('.add-contact'))?.focus())
}
const undoToast = (message: string, steps: { node: string; types: string[] }[], done: string) => toast(message, {
  timeout: 8000,
  action: { label: 'Undo', run: () => { void undoLatest(steps).then(() => { emit('changed'); toast(done) }).catch(e => toast(errorText(e), { tone: 'error' })) } },
})
async function save() {
  touched.value = true
  if (problems.value.name || problems.value.email) { root.value?.querySelector<HTMLInputElement>(`.contact-form [aria-invalid="true"]`)?.focus(); return }
  if (busy.value) return
  busy.value = true; formError.value = ''
  const write: ContactWrite = { ...form, name: form.name.trim(), email: form.email.trim(), phone: form.phone.trim(), role: form.role.trim(), note: form.note.trim() }
  try {
    if (editing.value === 'new') {
      const created = await createContact(props.customer.id, write)
      editing.value = null
      emit('changed')
      undoToast(created.primary ? `Added ${created.name} as the primary contact.` : `Added ${created.name}.`,
        created.primary ? [{ node: props.customer.id, types: ['crm.primary_contact_changed'] }, { node: created.id, types: ['crm.contact_created'] }] : [{ node: created.id, types: ['crm.contact_created'] }],
        `${created.name} is removed again.`)
    } else {
      const current = props.contacts?.find(c => c.id === editing.value)
      if (!current) return
      const updated = await updateContact(current.id, write, current.revision)
      editing.value = null
      emit('changed')
      undoToast(`Saved ${updated.name}.`, [{ node: updated.id, types: ['crm.contact_updated'] }], `${current.name} is back as it was.`)
    }
  } catch (e) { formError.value = errorText(e, 'The contact was not saved.') }
  finally { busy.value = false }
}
async function primary(contact: Contact) {
  try {
    await makePrimary(props.customer.id, contact.id, props.customer.revision)
    emit('changed')
    undoToast(`${contact.name} is the primary contact now.`, [{ node: props.customer.id, types: ['crm.primary_contact_changed'] }], 'The earlier primary contact is back.')
  } catch (e) { toast(errorText(e), { tone: 'error' }); emit('changed') }
}
async function remove(contact: Contact) {
  const next = contact.primary ? sorted.value.filter(c => c.id !== contact.id).sort((a, b) => a.name.localeCompare(b.name))[0] : undefined
  const ok = await confirmAction({
    title: `Remove ${contact.name}?`,
    body: contact.primary ? `${contact.name} is the primary contact.${next ? ` ${next.name} becomes the primary contact.` : ''} You can undo this right after.` : 'You can undo this right after.',
    confirmLabel: 'Remove contact', danger: true,
  })
  if (!ok) return
  try {
    await deleteContact(contact.id)
    emit('changed')
    undoToast(`Removed ${contact.name}.`,
      contact.primary ? [{ node: contact.id, types: ['crm.contact_deleted'] }, { node: props.customer.id, types: ['crm.primary_contact_changed'] }] : [{ node: contact.id, types: ['crm.contact_deleted'] }],
      `${contact.name} is back.`)
  } catch (e) { toast(errorText(e), { tone: 'error' }) }
}
function formKeys(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); cancel() }
  else if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); void save() }
}
defineExpose({ add: () => start(null) })
</script>

<template>
  <section ref="root" class="crm-card glass-card contacts" aria-labelledby="contacts-title">
    <header class="card-head">
      <span class="card-icon" aria-hidden="true"><AppIcon name="users" :size="15" /></span>
      <div class="card-titles">
        <h2 id="contacts-title">Contacts <span v-if="contacts?.length" class="card-count">{{ contacts.length }}</span></h2>
        <p class="card-lead">The primary contact is who quotes are addressed to.</p>
      </div>
      <button v-if="admin && !locked && editing !== 'new'" type="button" class="btn sm add-contact" @click="start(null)"><AppIcon name="plus" :size="13" />Add contact</button>
    </header>

    <p v-if="error" class="f-error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
    <div v-else-if="!contacts" class="sk" aria-hidden="true"><span v-for="i in 2" :key="i" class="sk-row"><span class="skeleton sk-avatar" /><span class="skeleton" /></span></div>
    <p v-else-if="!contacts.length && editing !== 'new'" class="empty-line">No contacts yet.{{ admin ? ' The first one you add becomes the primary contact.' : '' }}</p>

    <ul v-if="sorted.length || editing === 'new'" class="list">
      <template v-for="c in sorted" :key="c.id">
        <li v-if="editing !== c.id" class="contact" :class="{ primary: c.primary }" :data-contact="c.id">
          <Avatar :name="c.name" :size="36" />
          <div class="who">
            <p class="name-line">
              <span class="name">{{ c.name }}</span>
              <span v-if="c.primary" class="primary-chip"><BizIcon name="star" :size="11" />Primary</span>
            </p>
            <p v-if="c.role" class="role">{{ c.role }}</p>
            <p v-if="c.email || c.phone" class="reach">
              <a v-if="c.email" :href="`mailto:${c.email}`" class="reach-link"><BizIcon name="mail" :size="13" />{{ c.email }}</a>
              <a v-if="c.phone" :href="telHref(c.phone)" class="reach-link"><BizIcon name="phone" :size="13" />{{ c.phone }}</a>
            </p>
            <p v-if="c.note" class="note">{{ c.note }}</p>
          </div>
          <div v-if="admin && !locked" class="row-actions">
            <button v-if="!c.primary" type="button" class="icon-btn sm flat" :aria-label="`Make ${c.name} the primary contact`" :data-tip="'Make primary'" @click="primary(c)"><BizIcon name="star" :size="14" /></button>
            <button type="button" class="icon-btn sm flat edit-contact" :aria-label="`Edit ${c.name}`" data-tip="Edit" @click="start(c)"><AppIcon name="edit" :size="14" /></button>
            <button type="button" class="icon-btn sm flat danger" :aria-label="`Remove ${c.name}`" data-tip="Remove" @click="remove(c)"><AppIcon name="trash" :size="14" /></button>
          </div>
        </li>
        <li v-else class="contact-form-row">
          <form class="contact-form" :aria-label="`Edit ${c.name}`" novalidate @submit.prevent="save" @keydown="formKeys">
            <div class="f-grid">
              <div class="f-row"><label class="f-label" :for="`contact-name-${c.id}`">Name</label><input :id="`contact-name-${c.id}`" v-model="form.name" class="field" maxlength="200" autocomplete="off" :aria-invalid="touched && !!problems.name" /><p v-if="touched && problems.name" class="f-note bad" role="alert">{{ problems.name }}</p></div>
              <div class="f-row"><label class="f-label" :for="`contact-role-${c.id}`">Role <span class="opt">optional</span></label><input :id="`contact-role-${c.id}`" v-model="form.role" class="field" maxlength="200" autocomplete="off" placeholder="Managing director" /></div>
              <div class="f-row"><label class="f-label" :for="`contact-email-${c.id}`">Email <span class="opt">optional</span></label><input :id="`contact-email-${c.id}`" v-model="form.email" class="field" type="email" inputmode="email" maxlength="320" autocomplete="off" :aria-invalid="touched && !!problems.email" /><p v-if="touched && problems.email" class="f-note bad" role="alert">{{ problems.email }}</p></div>
              <div class="f-row"><label class="f-label" :for="`contact-phone-${c.id}`">Phone <span class="opt">optional</span></label><input :id="`contact-phone-${c.id}`" v-model="form.phone" class="field" type="tel" maxlength="100" autocomplete="off" /></div>
              <div class="f-row wide"><label class="f-label" :for="`contact-note-${c.id}`">Note <span class="opt">optional</span></label><textarea :id="`contact-note-${c.id}`" v-model="form.note" class="field" rows="2" maxlength="20000" /></div>
            </div>
            <p v-if="formError" class="f-error" role="alert"><AppIcon name="alert" :size="14" />{{ formError }}</p>
            <div class="form-actions"><button type="button" class="btn sm" @click="cancel">Cancel</button><button type="submit" class="btn sm primary" :disabled="busy">{{ busy ? 'Saving…' : 'Save contact' }}</button></div>
          </form>
        </li>
      </template>
      <li v-if="editing === 'new'" class="contact-form-row">
        <form class="contact-form" aria-label="New contact" novalidate @submit.prevent="save" @keydown="formKeys">
          <div class="f-grid">
            <div class="f-row"><label class="f-label" for="contact-name-new">Name</label><input id="contact-name-new" v-model="form.name" class="field" maxlength="200" autocomplete="off" placeholder="Jana Hofer" :aria-invalid="touched && !!problems.name" /><p v-if="touched && problems.name" class="f-note bad" role="alert">{{ problems.name }}</p></div>
            <div class="f-row"><label class="f-label" for="contact-role-new">Role <span class="opt">optional</span></label><input id="contact-role-new" v-model="form.role" class="field" maxlength="200" autocomplete="off" placeholder="Managing director" /></div>
            <div class="f-row"><label class="f-label" for="contact-email-new">Email <span class="opt">optional</span></label><input id="contact-email-new" v-model="form.email" class="field" type="email" inputmode="email" maxlength="320" autocomplete="off" :aria-invalid="touched && !!problems.email" /><p v-if="touched && problems.email" class="f-note bad" role="alert">{{ problems.email }}</p></div>
            <div class="f-row"><label class="f-label" for="contact-phone-new">Phone <span class="opt">optional</span></label><input id="contact-phone-new" v-model="form.phone" class="field" type="tel" maxlength="100" autocomplete="off" /></div>
            <div class="f-row wide"><label class="f-label" for="contact-note-new">Note <span class="opt">optional</span></label><textarea id="contact-note-new" v-model="form.note" class="field" rows="2" maxlength="20000" /></div>
          </div>
          <p v-if="formError" class="f-error" role="alert"><AppIcon name="alert" :size="14" />{{ formError }}</p>
          <div class="form-actions"><button type="button" class="btn sm" @click="cancel">Cancel</button><button type="submit" class="btn sm primary" :disabled="busy">{{ busy ? 'Adding…' : 'Add contact' }}</button></div>
        </form>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.empty-line { font-size: 13.5px; color: var(--ink-3); }
.sk { display: grid; align-content: start; gap: 12px; min-height: 204px; }
.sk-row { display: flex; align-items: center; gap: 12px; }
.sk-row .skeleton { flex: 1; }
.sk-row .sk-avatar { flex: 0 0 36px; height: 36px; border-radius: 50%; }
.list { display: grid; margin: 0; padding: 0; list-style: none; }
.contact { display: flex; align-items: flex-start; gap: 12px; padding: 12px 0; border-top: 1px solid var(--line); }
.list > :first-child { border-top: 0; padding-top: 2px; }
.who { display: grid; gap: 2px; flex: 1; min-width: 0; }
.name-line { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; min-height: 22px; }
.name { font-size: 14px; font-weight: 650; color: var(--ink); overflow-wrap: anywhere; }
.primary-chip { display: inline-flex; align-items: center; gap: 4px; height: 20px; padding: 0 8px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-size: 11.5px; font-weight: 600; }
.role { font-size: 12.5px; color: var(--ink-2); }
.reach { display: flex; flex-wrap: wrap; gap: 2px 14px; margin-top: 2px; }
.reach-link { display: inline-flex; align-items: center; gap: 6px; min-height: 24px; color: var(--teal-ink); font-size: 13px; text-decoration: none; overflow-wrap: anywhere; }
@media (max-width: 600px) { .reach-link { min-height: 44px; } .row-actions .icon-btn { width: 44px; height: 44px; } }
.reach-link svg { flex-shrink: 0; color: var(--ink-3); }
.reach-link:hover { text-decoration: underline; }
.reach-link:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
.note { margin-top: 2px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); white-space: pre-line; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
.row-actions { display: flex; gap: 2px; flex-shrink: 0; }
.row-actions .icon-btn { color: var(--ink-3); }
.row-actions .icon-btn:hover { color: var(--teal-ink); }
.row-actions .icon-btn.danger:hover { color: var(--danger); }
.contact-form-row { padding: 12px 0; border-top: 1px solid var(--line); }
.contact-form { display: grid; gap: 12px; padding: 14px; border-radius: 12px; background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.form-actions { display: flex; justify-content: flex-end; gap: 8px; }
@media (max-width: 600px) {
  .contact { flex-wrap: wrap; }
  .row-actions { width: 100%; justify-content: flex-end; margin-top: -4px; }
  .add-contact { height: 44px; }
  .row-actions .icon-btn { width: 44px; height: 44px; }
  .form-actions .btn { flex: 1; height: 44px; }
}
</style>
