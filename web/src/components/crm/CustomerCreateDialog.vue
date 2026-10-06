<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import '../../styles/crm.css'
import { computed, nextTick, reactive, ref } from 'vue'
import { blankCustomer, createCustomer, errorText, normalizeWebsite, tidyAddress, validWebsite, type Customer } from '../../lib/crm'
import AppIcon from '../AppIcon.vue'

// A new customer: the name, and what helps you recognise it later. Everything
// else is filled in on the customer page; the number comes with the first quote.
const emit = defineEmits<{ created: [customer: Customer] }>()
const dialog = ref<HTMLDialogElement>()
const nameInput = ref<HTMLInputElement>()
const form = reactive({ name: '', legal: '', industry: '', website: '', phone: '', city: '', country: '' })
const busy = ref(false)
const error = ref('')
const touched = ref(false)
let opener: HTMLElement | null = null
const website = computed(() => normalizeWebsite(form.website))
const problems = computed(() => ({
  name: !form.name.trim() ? 'A name is needed.' : form.name.trim().length > 200 ? 'At most 200 characters.' : '',
  website: validWebsite(website.value) ? '' : 'Use a web address like hofer.at.',
}))
const dirty = computed(() => Object.values(form).some(v => v.trim()))
async function open(name = '') {
  opener = document.activeElement as HTMLElement
  Object.assign(form, { name, legal: '', industry: '', website: '', phone: '', city: '', country: '' })
  error.value = ''; busy.value = false; touched.value = false
  dialog.value?.showModal()
  await nextTick(); nameInput.value?.focus()
}
function close() { dialog.value?.close(); opener?.focus({ preventScroll: true }) }
async function submit() {
  touched.value = true
  if (problems.value.name || problems.value.website) { (problems.value.name ? nameInput.value : dialog.value?.querySelector<HTMLInputElement>('#new-customer-website'))?.focus(); return }
  if (busy.value) return
  busy.value = true; error.value = ''
  try {
    const write = blankCustomer(form.name.trim())
    Object.assign(write, { legal_name: form.legal.trim(), industry: form.industry.trim(), website: website.value, phone: form.phone.trim() })
    write.billing_address = tidyAddress({ street: '', postal_code: '', city: form.city, country: form.country, freeform: '' })
    const customer = await createCustomer(write)
    dialog.value?.close()
    emit('created', customer)
  } catch (e) { error.value = errorText(e, 'The customer was not added.') }
  finally { busy.value = false }
}
function backdrop(event: MouseEvent) { if (event.target === dialog.value && !dirty.value) close() }
function keys(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); void submit() }
}
defineExpose({ open })
</script>

<template>
  <dialog ref="dialog" class="create" aria-labelledby="new-customer-title" @cancel.prevent="close" @click="backdrop">
    <form class="create-card" novalidate @submit.prevent="submit" @keydown="keys">
      <header class="create-head">
        <div>
          <h2 id="new-customer-title">New customer</h2>
          <p class="lead">Contacts, addresses and rates follow on the customer’s page.</p>
        </div>
        <button type="button" class="icon-btn sm flat" aria-label="Close" data-tip="Close · Esc" @click="close"><AppIcon name="close" :size="15" /></button>
      </header>
      <div class="f-grid">
        <div class="f-row wide">
          <label class="f-label" for="new-customer-name">Name</label>
          <input id="new-customer-name" ref="nameInput" v-model="form.name" class="field" maxlength="200" autocomplete="organization" placeholder="Bäckerei Hofer" :aria-invalid="touched && !!problems.name" aria-describedby="new-customer-name-note" />
          <p v-if="touched && problems.name" id="new-customer-name-note" class="f-note bad" role="alert"><AppIcon name="alert" :size="12" />{{ problems.name }}</p>
        </div>
        <div class="f-row wide">
          <label class="f-label" for="new-customer-legal">Legal name <span class="opt">optional</span></label>
          <input id="new-customer-legal" v-model="form.legal" class="field" maxlength="200" autocomplete="off" placeholder="As on invoices" />
        </div>
        <div class="f-row">
          <label class="f-label" for="new-customer-industry">Industry <span class="opt">optional</span></label>
          <input id="new-customer-industry" v-model="form.industry" class="field" maxlength="200" autocomplete="off" />
        </div>
        <div class="f-row">
          <label class="f-label" for="new-customer-website">Website <span class="opt">optional</span></label>
          <input id="new-customer-website" v-model="form.website" class="field" type="url" inputmode="url" maxlength="500" autocomplete="url" placeholder="hofer.at" :aria-invalid="touched && !!problems.website" aria-describedby="new-customer-website-note" />
          <p v-if="touched && problems.website" id="new-customer-website-note" class="f-note bad" role="alert"><AppIcon name="alert" :size="12" />{{ problems.website }}</p>
        </div>
        <div class="f-row">
          <label class="f-label" for="new-customer-city">City <span class="opt">optional</span></label>
          <input id="new-customer-city" v-model="form.city" class="field" maxlength="200" autocomplete="address-level2" />
        </div>
        <div class="f-row">
          <label class="f-label" for="new-customer-country">Country <span class="opt">optional</span></label>
          <input id="new-customer-country" v-model="form.country" class="field" maxlength="100" autocomplete="country-name" />
        </div>
      </div>
      <p v-if="error" class="f-error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
      <footer class="create-foot">
        <p class="f-hint"><kbd class="keycap"><AppIcon name="enter" /></kbd> adds · <kbd class="keycap">esc</kbd> closes</p>
        <button type="button" class="btn" @click="close">Cancel</button>
        <button type="submit" class="btn primary" :disabled="busy"><AppIcon name="plus" :size="14" />{{ busy ? 'Adding…' : 'Add customer' }}</button>
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
.lead { margin-top: 4px; font-size: 13px; color: var(--ink-2); }
.create-foot { display: flex; align-items: center; justify-content: flex-end; gap: 8px; }
.create-foot .f-hint { flex: 1; }
@media (max-width: 600px) {
  .create-card { padding: 16px; }
  .create-foot { flex-wrap: wrap; }
  .create-foot .f-hint { display: none; }
  .create-foot .btn { flex: 1; height: 44px; }
}
</style>
