<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import QuoteText from './QuoteText.vue'
import DatePicker from '../DatePicker.vue'
import type { QuoteEditor } from '../../../lib/quotes/editor'
import type { QuoteDocumentData } from '../../../lib/quotes/types'
import { computed } from 'vue'
import { normalizeProfile, profileAssetUrl, profileDate, profileLabel } from '../../../lib/quotes/profile'
const props = defineProps<{ document: QuoteDocumentData; editor: QuoteEditor; offerNo?: string; editable?: boolean }>()
const profile = computed(() => normalizeProfile(props.document.profile))
const classic = () => profile.value?.definition.layout_variant === 'classic-v1'
const set = (part: 'sender' | 'recipient' | 'legal', key: string, text: string) => props.editor.editField(part, { key, text })
const date = (value: string) => profileDate(props.document.profile, value)
const label = (key: string, fallback: string) => profileLabel(props.document.profile, key, fallback)
</script>
<template>
  <section class="quote-cover" aria-label="Quote cover">
    <p class="quote-overline">{{ label('quote', 'Angebot') }} <span>{{ offerNo }}</span><img v-if="classic() && profile?.definition.cover.brand_asset_id" class="quote-brand-dots" :src="profileAssetUrl(profile.definition.cover.brand_asset_id)" alt="" /></p>
    <QuoteText tag="h1" class="quote-title" :model-value="document.title" label="Angebotstitel" :editable="editable" @update:model-value="editor.editField('title', $event)" />
    <QuoteText tag="p" class="quote-subtitle" :model-value="document.subtitle" label="Untertitel" :editable="editable" @update:model-value="editor.editField('subtitle', $event)" />
    <div class="quote-cover-grid">
      <div>
        <p class="quote-label">{{ label('recipient', 'Auftraggeber') }}</p>
        <QuoteText tag="p" class="quote-recipient" :model-value="document.recipient.name ?? ''" label="Firma des Kunden" :editable="editable" @update:model-value="set('recipient', 'name', $event)" />
        <QuoteText tag="p" :model-value="document.recipient.address ?? ''" label="Kundenanschrift" :editable="editable" @update:model-value="set('recipient', 'address', $event)" />
        <p v-if="classic() && (document.recipient.contact || editable)">z. Hd. <QuoteText tag="span" :model-value="document.recipient.contact ?? ''" label="Kundenkontakt" :editable="editable" @update:model-value="set('recipient', 'contact', $event)" /></p>
        <QuoteText v-else-if="!classic()" tag="p" :model-value="document.recipient.contact ?? ''" label="Kundenkontakt" :editable="editable" @update:model-value="set('recipient', 'contact', $event)" />
        <QuoteText tag="p" :model-value="document.recipient.country ?? ''" label="Land des Kunden" :editable="editable" @update:model-value="set('recipient', 'country', $event)" />
      </div>
      <dl class="quote-meta">
        <dt>{{ label('number', 'Angebotsnummer') }}</dt><dd>{{ offerNo }}</dd>
        <dt>{{ label('date', 'Angebotsdatum') }}</dt><dd><DatePicker v-if="editable" variant="paper" :label="label('date', 'Angebotsdatum')" :model-value="document.offer_date" @update:model-value="editor.editField('offer_date', $event)" /><template v-else>{{ date(document.offer_date) }}</template></dd>
        <dt>{{ label('customer', 'Kundennummer') }}</dt><dd>{{ document.recipient.customer_no }}</dd>
        <dt>{{ label('valid', 'Gültig bis') }}</dt><dd><DatePicker v-if="editable" variant="paper" :label="label('valid', 'Gültig bis')" :model-value="document.valid_until" @update:model-value="editor.editField('valid_until', $event)" /><template v-else>{{ date(document.valid_until) }}</template></dd>
        <dt>{{ label('contact', 'Ansprechpartner') }}</dt><dd><QuoteText :model-value="document.sender.contact_person ?? ''" :label="label('contact', 'Ansprechpartner')" :editable="editable" @update:model-value="set('sender', 'contact_person', $event)" /></dd>
        <dt>{{ label('project', 'Projektreferenz') }}</dt><dd><QuoteText :model-value="document.project_ref" :label="label('project', 'Projektreferenz')" :editable="editable" @update:model-value="editor.editField('project_ref', $event)" /></dd>
      </dl>
    </div>
    <p class="quote-sender"><strong>{{ document.sender.company }}</strong><span>{{ document.sender.street }}, {{ document.sender.postal_code }} {{ document.sender.city }}{{ classic() ? ',' : '' }} {{ document.sender.country }}</span><span v-if="document.sender.register_no">{{ document.sender.register_no }} · {{ document.sender.register_court }}</span><span>{{ document.sender.email }}</span><span v-if="classic() && document.sender.uid">UID {{ document.sender.uid }}</span></p>
    <QuoteText tag="p" class="quote-intro" :model-value="document.legal.intro ?? ''" label="Einleitung" :editable="editable" @update:model-value="set('legal', 'intro', $event)" />
  </section>
</template>
<style scoped>
.quote-cover { display: grid; gap: 8mm; }
.quote-overline { font-size: 10pt; font-weight: 700; letter-spacing: .18em; text-transform: uppercase; display: flex; justify-content: space-between; }
.quote-title { font-size: 27pt; line-height: 1.13; font-weight: 700; margin: 7mm 0 0; }
.quote-subtitle { font-size: 12pt; color: var(--ink-2); margin: 0; }
.quote-cover-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12mm; margin-top: 12mm; }
.quote-label { font-size: 8pt; font-weight: 700; text-transform: uppercase; letter-spacing: .1em; color: var(--ink-2); }
.quote-recipient { font-size: 12pt; font-weight: 700; }
.quote-meta { display: grid; grid-template-columns: 32mm 1fr; gap: 3mm 5mm; margin: 0; font-size: 9pt; }
.quote-meta dt { color: var(--ink-2); }.quote-meta dd { margin: 0; overflow-wrap: anywhere; }
.quote-sender { display: flex; flex-wrap: wrap; gap: 1mm 5mm; border-top: 1px solid var(--line-2); padding-top: 4mm; font-size: 8pt; }
.quote-intro { font-size: 10pt; line-height: 1.5; margin-top: 8mm; }
:global(.quote-document.classic-v1 .quote-cover) { gap: 0; padding-bottom: 5.8mm; }
:global(.quote-document.classic-v1 .quote-overline) { font-family: var(--quote-display-font, var(--quote-body-font, var(--font))); font-size: 34pt; font-weight: 400; letter-spacing: .14em; color: var(--teal); margin: var(--quote-cover-top) 0 0; line-height: 1; }
:global(.quote-document.classic-v1 .quote-overline span) { display: none; }
:global(.quote-document.classic-v1 .quote-overline .quote-brand-dots) { display: block; width: 11.25mm; height: 2.5mm; align-self: center; }
:global(.quote-document.classic-v1 .quote-title) { font-family: var(--quote-display-font, var(--quote-body-font, var(--font))); font-size: var(--quote-title-size); font-weight: 400; margin: var(--quote-title-gap) 0 1.5mm; line-height: 1.25; letter-spacing: .01em; }
:global(.quote-document.classic-v1 .quote-subtitle) { font-size: 10.5pt; color: var(--ink-2); }
:global(.quote-document.classic-v1 .quote-cover-grid) { gap: 0; margin-top: var(--quote-columns-gap); border-top: 1px solid var(--line); padding-top: var(--quote-columns-padding); }
:global(.quote-document.classic-v1 .quote-cover-grid p) { margin: 0; white-space: pre-line; }
:global(.quote-document.classic-v1 .quote-cover-grid > div:first-child p:not(.quote-label)) { color: var(--ink); }
:global(.quote-document.classic-v1 .quote-cover-grid .quote-label) { margin-bottom: 0; color: var(--teal); font-size: 7.5pt; font-weight: 600; letter-spacing: .14em; }
:global(.quote-document.classic-v1 .quote-cover-grid .quote-recipient) { font-size: 10pt; font-weight: 600; }
:global(.quote-document.classic-v1 .quote-cover-grid > div:first-child) { padding-right: 10mm; border-right: 1px solid var(--line); }
:global(.quote-document.classic-v1 .quote-meta) { padding-left: 10mm; }
:global(.quote-document.classic-v1 .quote-meta) { grid-template-columns: auto 1fr; gap: 3px 12px; align-items: baseline; font-size: 10pt; }
:global(.quote-document.classic-v1 .quote-meta dt) { color: var(--ink-3); font-size: 7.5pt; font-weight: 600; letter-spacing: .12em; text-transform: uppercase; }
:global(.quote-document.classic-v1 .quote-meta dd) { font-weight: 600; }
:global(.quote-document.classic-v1 .quote-sender) { margin-top: 5mm; padding-top: 4mm; }
:global(.quote-document.classic-v1 .quote-sender) { font-size: 8.5pt; color: var(--ink-2); gap: 4px 14px; }
:global(.quote-document.classic-v1 .quote-sender strong) { color: var(--ink); font-weight: 600; }
:global(.quote-document.classic-v1 .quote-intro) { margin-top: 7mm; font-size: 10.5pt; line-height: 1.55; color: var(--ink); white-space: pre-line; }
</style>
