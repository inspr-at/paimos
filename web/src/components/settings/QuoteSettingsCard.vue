<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import '../../styles/crm.css'
import { computed, onMounted, reactive, ref } from 'vue'
import { SENDER_FIELDS, getQuoteSettings, saveQuoteSettings, senderWrite, settingsLink, statusOf, type QuoteSettings } from '../../lib/settings'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'
import SettingsCard from './SettingsCard.vue'

// The sender every quote prints (your company, address, registration and bank),
// with the currency and the time zone quote numbers are dated in. A preview
// shows the letterhead as it will read. Texts and layout come with the editor.
const quotes = ref<QuoteSettings | null>(null)
const state = ref<'loading' | 'ready' | 'closed' | 'error'>('loading')
const form = reactive<Record<string, string>>({})
const numbering = reactive({ currency: '', zone: '' })
const saving = ref(false)
const error = ref('')
const touched = ref(false)
let base = ''
const detectedZone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'Europe/Vienna'
function fill(settings: QuoteSettings) {
  for (const field of SENDER_FIELDS) form[field.key] = settings.sender?.[field.key] ?? ''
  // First setup: the currency and this device's time zone are suggested, and saved with the sender.
  numbering.currency = settings.default_currency || 'EUR'
  numbering.zone = settings.numbering_time_zone || detectedZone
  base = snapshot()
}
const snapshot = () => JSON.stringify([form, numbering])
const dirty = computed(() => state.value === 'ready' && snapshot() !== base)
const firstSetup = computed(() => !!quotes.value && quotes.value.revision === 0)
const problems = computed(() => ({
  currency: /^[A-Z]{3}$/.test(numbering.currency.trim().toUpperCase()) ? '' : 'Three letters, such as EUR.',
  zone: /^[A-Za-z_]+(?:\/[A-Za-z0-9_+-]+)*$/.test(numbering.zone.trim()) ? '' : 'A time zone like Europe/Vienna.',
  company: form.company?.trim() ? '' : 'Quotes need the company name.',
}))
const invalid = computed(() => Object.values(problems.value).some(Boolean))
async function load() {
  state.value = 'loading'
  try { quotes.value = await getQuoteSettings(); fill(quotes.value); state.value = 'ready' }
  catch (e) { state.value = [403, 404, 409].includes(statusOf(e)) ? 'closed' : 'error' }
}
async function save() {
  touched.value = true
  const current = quotes.value
  if (!current || saving.value) return
  if (invalid.value) { document.querySelector<HTMLInputElement>('#quotes [aria-invalid="true"]')?.focus(); return }
  saving.value = true; error.value = ''
  try {
    quotes.value = await saveQuoteSettings(current, { sender: senderWrite(current.sender ?? {}, form), default_currency: numbering.currency.trim().toUpperCase(), numbering_time_zone: numbering.zone.trim() })
    fill(quotes.value)
    touched.value = false
    toast('Quote settings saved. New quotes use them.')
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'The quote settings were not saved.'
    // A newer version: take its revision, keep the edits on screen.
    if (statusOf(e) === 409) { try { quotes.value = await getQuoteSettings() } catch { /* the message already says to try again */ } }
  } finally { saving.value = false }
}
function reset() { if (quotes.value) fill(quotes.value); touched.value = false; error.value = '' }
function keys(event: KeyboardEvent) { if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); void save() } }
const groups = [
  { id: 'company', label: 'Sender' }, { id: 'address', label: 'Address' }, { id: 'legal', label: 'Registration' }, { id: 'bank', label: 'Bank' },
] as const
const fieldsOf = (group: string) => SENDER_FIELDS.filter(f => f.group === group)
// The letterhead as quotes print it: the parts that are filled in, line by line.
const preview = computed(() => {
  const v = (k: string) => (form[k] ?? '').trim()
  return {
    company: v('company'),
    address: [v('street'), [v('postal_code'), v('city')].filter(Boolean).join(' '), v('country')].filter(Boolean),
    reach: [v('contact_person'), v('email'), v('phone'), v('website')].filter(Boolean),
    legal: [v('uid') && `VAT ${v('uid')}`, v('register_no'), v('register_court')].filter(Boolean),
    bank: [v('bank_name'), v('iban') && `IBAN ${v('iban').toUpperCase()}`, v('bic') && `BIC ${v('bic')}`].filter(Boolean),
  }
})
onMounted(load)
</script>

<template>
  <SettingsCard title="Quote settings" icon="document" anchor="quotes">
    <template #lead>Your company as every quote shows it, and how quote numbers are dated.</template>
    <template v-if="state === 'ready' && dirty" #aside><span class="unsaved" aria-live="polite">Unsaved</span></template>
    <div v-if="state === 'loading'" class="set-skeleton" role="status" aria-label="Loading quote settings"><span class="skeleton" /><span class="skeleton" /><span class="skeleton" /></div>
    <div v-else-if="state === 'closed'" class="set-note"><AppIcon name="info" :size="14" /><span>Quote settings show here once Quotes is enabled for this workspace.</span><RouterLink class="btn sm" :to="settingsLink('business', 'parts')">Business parts</RouterLink></div>
    <p v-else-if="state === 'error'" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />The quote settings could not be loaded.<button type="button" class="btn sm" @click="load">Try again</button></p>
    <form v-else class="quote-form" aria-label="Quote sender" novalidate @submit.prevent="save" @keydown="keys">
      <div class="sender">
        <div class="groups">
          <fieldset v-for="group in groups" :id="group.id === 'company' ? 'sender' : undefined" :key="group.id" class="f-group">
            <legend>{{ group.label }}</legend>
            <div class="f-grid">
              <div v-for="field in fieldsOf(group.id)" :key="field.key" class="f-row" :class="{ wide: 'wide' in field && field.wide }">
                <label class="f-label" :for="`sender-${field.key}`">{{ field.label }}</label>
                <input
                  :id="`sender-${field.key}`" v-model="form[field.key]" class="field" :class="{ mono: 'mono' in field && field.mono }" maxlength="500"
                  :autocomplete="'auto' in field ? field.auto : 'off'" :type="field.key === 'email' ? 'email' : field.key === 'website' ? 'url' : field.key === 'phone' ? 'tel' : 'text'"
                  :aria-invalid="field.key === 'company' && touched && !!problems.company" :aria-describedby="field.key === 'company' ? 'sender-company-note' : undefined"
                />
                <p v-if="field.key === 'company' && touched && problems.company" id="sender-company-note" class="f-note bad" role="alert"><AppIcon name="alert" :size="12" />{{ problems.company }}</p>
              </div>
            </div>
          </fieldset>
          <fieldset class="f-group">
            <legend>Numbers and money</legend>
            <div class="f-grid">
              <div class="f-row">
                <label class="f-label" for="quote-currency">Currency</label>
                <input id="quote-currency" v-model="numbering.currency" class="field mono" maxlength="3" autocomplete="off" :aria-invalid="touched && !!problems.currency" aria-describedby="quote-currency-note" @input="numbering.currency = numbering.currency.toUpperCase()" />
                <p id="quote-currency-note" class="f-note" :class="{ bad: touched && !!problems.currency }">{{ touched && problems.currency ? problems.currency : 'New quotes start in it.' }}</p>
              </div>
              <div class="f-row">
                <label class="f-label" for="quote-zone">Numbering time zone</label>
                <input id="quote-zone" v-model="numbering.zone" class="field mono" maxlength="100" autocomplete="off" spellcheck="false" :aria-invalid="touched && !!problems.zone" aria-describedby="quote-zone-note" />
                <p id="quote-zone-note" class="f-note" :class="{ bad: touched && !!problems.zone }">{{ touched && problems.zone ? problems.zone : 'Quote numbers carry the date there.' }}</p>
              </div>
            </div>
            <p v-if="firstSetup" class="f-note"><AppIcon name="info" :size="12" />Suggested for a first setup; nothing is stored until you save.</p>
          </fieldset>
        </div>
        <aside class="letterhead" aria-label="How quotes show the sender">
          <p class="lh-label">On every quote</p>
          <div class="paper">
            <p class="lh-company" :class="{ unset: !preview.company }">{{ preview.company || 'Your company' }}</p>
            <p v-for="line in preview.address" :key="line" class="lh-line">{{ line }}</p>
            <p v-if="preview.reach.length" class="lh-line lh-reach dot-list"><span v-for="part in preview.reach" :key="part">{{ part }}</span></p>
            <hr v-if="preview.legal.length || preview.bank.length" />
            <p v-if="preview.legal.length" class="lh-small dot-list"><span v-for="part in preview.legal" :key="part">{{ part }}</span></p>
            <p v-if="preview.bank.length" class="lh-small dot-list"><span v-for="part in preview.bank" :key="part">{{ part }}</span></p>
          </div>
          <p class="lh-note">Email delivery is disabled. Accepted PDF receipts remain available in the quote. Type, colours and layout come from the document profiles below.</p>
        </aside>
      </div>
      <p v-if="error" class="f-error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
      <footer class="quote-foot">
        <p class="f-hint">Applies to quotes made from now on; issued quotes keep the sender they were sent with.</p>
        <button type="button" class="btn sm ghost" :disabled="!dirty || saving" @click="reset">Reset</button>
        <button type="submit" class="btn sm primary" :disabled="saving || (!dirty && !firstSetup)"><AppIcon name="check" :size="13" />{{ saving ? 'Saving…' : 'Save quote settings' }}</button>
      </footer>
    </form>
  </SettingsCard>
</template>

<style scoped>
.set-skeleton { min-height: 720px; align-content: start; }
.unsaved { font-size: 12px; font-weight: 600; color: var(--warn-ink); }
.quote-form { display: grid; gap: 16px; }
.sender { display: grid; grid-template-columns: minmax(0, 1fr) 300px; gap: 24px; align-items: start; }
.groups { display: grid; gap: 18px; min-width: 0; }
.letterhead { position: sticky; top: 16px; display: grid; gap: 8px; }
.lh-label { font: 500 10.5px/1.4 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
/* Paper in either theme: quotes are documents, and the preview reads like one
   (the quote document's own paper colours). */
.paper {
  --paper: #fffefa; --paper-ink: #1f2f30; --paper-ink-2: #4a5c5e; --paper-ink-3: #5b6d6f; --paper-line: rgba(31, 47, 48, .14);
  display: grid; gap: 2px; padding: 18px 18px 16px; border-radius: 10px; background: var(--paper); color: var(--paper-ink);
  box-shadow: 0 1px 2px rgba(20, 40, 42, .12), 0 10px 24px -14px rgba(20, 40, 42, .35), inset 0 0 0 1px var(--paper-line); font-size: 12.5px; line-height: 1.5; overflow-wrap: anywhere;
}
.paper p { color: var(--paper-ink); }
.lh-company { margin-bottom: 4px; font-size: 14.5px; font-weight: 700; }
.paper .lh-company.unset { color: var(--paper-ink-3); font-weight: 600; }
.lh-line { color: var(--paper-ink); }
.lh-reach { margin-top: 4px; }
.paper .dot-list > *::before { color: var(--paper-ink-3); }
.paper hr { margin: 10px 0 6px; border: 0; border-top: 1px solid var(--paper-line); }
.paper .lh-small { font-size: 11px; color: var(--paper-ink-2); }
.lh-note { font-size: 12px; line-height: 1.5; color: var(--ink-2); }
.quote-foot { display: flex; align-items: center; justify-content: flex-end; gap: 8px; padding-top: 14px; border-top: 1px solid var(--line); }
.quote-foot .f-hint { flex: 1; }
@media (max-width: 1100px) { .sender { grid-template-columns: minmax(0, 1fr); } .letterhead { position: static; max-width: 420px; } }
@media (max-width: 600px) {
  .quote-foot { flex-wrap: wrap; }
  .quote-foot .f-hint { flex-basis: 100%; }
  .quote-foot .btn { flex: 1; height: 44px; }
}
</style>
