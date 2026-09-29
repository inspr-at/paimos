<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import QuoteText from './QuoteText.vue'
import { documentTotal } from '../../../lib/quotes/layout'
import { computed } from 'vue'
import { normalizeProfile, profileLabel, profileMoney } from '../../../lib/quotes/profile'
import type { QuoteEditor } from '../../../lib/quotes/editor'
import type { QuoteDocumentData } from '../../../lib/quotes/types'
const props = defineProps<{ document: QuoteDocumentData; editor: QuoteEditor; editable?: boolean; accepted?: { name: string; company?: string; at: string; digest: string } | null }>()
const profile = computed(() => normalizeProfile(props.document.profile))
</script>
<template>
  <section class="quote-acceptance" aria-label="Acceptance">
    <div v-if="profile?.definition.totals.discount === 'line'" class="quote-adjustment"><span>{{ profileLabel(document.profile, 'discount', 'Rabatt') }}</span><QuoteText :model-value="document.legal.discount_note ?? ''" label="Rabattinformation" :editable="editable" @update:model-value="editor.editField('legal', { key: 'discount_note', text: $event })" /></div>
    <div class="quote-total"><span>{{ profile?.definition.totals.net_label || profileLabel(document.profile, 'net', 'Nettosumme') }}</span><strong>{{ profileMoney(documentTotal(document.positions), document.currency, document.profile) }}</strong></div>
    <div v-if="profile?.definition.totals.vat === 'line'" class="quote-adjustment"><span>{{ profileLabel(document.profile, 'vat', 'Umsatzsteuer') }}</span><QuoteText :model-value="document.legal.vat_note ?? ''" label="Umsatzsteuerhinweis" :editable="editable" @update:model-value="editor.editField('legal', { key: 'vat_note', text: $event })" /></div>
    <div v-else-if="profile?.definition.totals.vat !== 'hidden'" class="quote-vat-note"><QuoteText :model-value="document.legal.vat_note ?? ''" label="Umsatzsteuerhinweis" :editable="editable" @update:model-value="editor.editField('legal', { key: 'vat_note', text: $event })" /></div>
    <div v-if="profile?.definition.payment_terms.position === 'after-totals'" class="quote-payment"><h3>{{ profile.definition.payment_terms.heading }}</h3><QuoteText tag="p" :model-value="document.legal.payment_terms ?? ''" label="Payment terms" :editable="editable" @update:model-value="editor.editField('legal', { key: 'payment_terms', text: $event })" /></div>
    <QuoteText tag="p" class="quote-accept-text" :model-value="document.legal.accept_text ?? ''" label="Annahmetext" :editable="editable" @update:model-value="editor.editField('legal', { key: 'accept_text', text: $event })" />
    <div class="quote-signatures"><div v-if="accepted" class="quote-stamp"><strong>Digital angenommen</strong><span>{{ accepted.name }} · {{ accepted.company }}</span><time>{{ accepted.at }}</time><small>{{ accepted.digest }}</small></div><div v-else>{{ profileLabel(document.profile, 'signature_customer', 'Ort, Datum, Unterschrift Auftraggeber') }}</div><div v-if="profile?.definition.acceptance.signature_columns !== 1">{{ profileLabel(document.profile, 'signature_sender', 'Ort, Datum, Unterschrift Auftragnehmer') }}</div></div>
  </section>
</template>
<style scoped>
.quote-acceptance { display: grid; gap: 6mm; break-inside: avoid; font-size: 9pt; }.quote-total { display: flex; justify-content: space-between; gap: 10mm; border-top: 1px solid var(--line-2); padding-top: 4mm; font-size: 13pt; }.quote-accept-text { line-height: 1.5; }.quote-signatures { display: grid; grid-template-columns: 1fr 1fr; gap: 12mm; margin-top: 8mm; }.quote-signatures > div { border-top: 1px solid var(--ink-2); padding-top: 3mm; }.quote-stamp { display: grid; gap: 1mm; border: 1px solid var(--line-2) !important; border-radius: 4px; padding: 4mm !important; overflow-wrap: anywhere; }.quote-stamp small { font-size: 7pt; }
:global(.quote-document.classic-v1 .quote-acceptance) { font-size: 9.6pt; gap: 0; }
:global(.quote-document.classic-v1 .quote-acceptance) { background: linear-gradient(var(--ink), var(--ink)) top / 100% 1px no-repeat; padding-top: 7px; margin-top: 4mm; }
:global(.quote-document.classic-v1 .quote-total) { width: 66mm; margin-left: auto; font-size: 11pt; font-weight: 700; padding: 3px 6px 3px 0; border: 0; }
:global(.quote-document.classic-v1 .quote-vat-note) { text-align: right; padding-right: 6px; color: var(--ink-2); font-size: 8.5pt; white-space: normal; }
:global(.quote-document.classic-v1 .quote-vat-note .quote-text) { white-space: normal; }
:global(.quote-document.classic-v1 .quote-accept-text) { margin: 9.8mm 0 0; font-size: 9.6pt; color: var(--ink); white-space: pre-line; }
:global(.quote-document.classic-v1 .quote-signatures) { gap: var(--quote-signature-gap); margin-top: calc(var(--quote-signature-lead) + 4mm); }
:global(.quote-document.classic-v1 .quote-signatures > div) { border-color: var(--ink); color: var(--ink-2); padding-top: 4px; font-size: 6.8pt; letter-spacing: .08em; text-transform: uppercase; font-weight: 600; }
.quote-payment h3 { font-size: 10pt; font-weight: 700; margin: 0 0 2mm; }.quote-payment p { white-space: pre-line; }
.quote-adjustment { display: flex; justify-content: space-between; gap: 10mm; font-size: 9pt; }
.quote-adjustment > span { font-weight: 600; }
</style>
