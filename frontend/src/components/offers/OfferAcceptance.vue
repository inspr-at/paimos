<script setup lang="ts">
import OfferQR from './OfferQR.vue'
import OfferText from './OfferText.vue'
import { money, net, receiptTime, type Offer, type OfferDocument } from './types'
defineProps<{
  document: OfferDocument
  receipt?: Partial<Offer>
  editable?: boolean
  publicUrl?: string
  qrPreview?: boolean
}>()
</script>
<template>
  <div class="offer-acceptance">
    <div class="totals">
      <div class="grand">
        <span>Nettosumme</span><span>{{ money(net(document)) }}</span>
      </div>
      <div class="vatnote">
        <OfferText v-model="document.vat_note" :editable="editable" label="Umsatzsteuerhinweis" />
      </div>
    </div>
    <div class="accept">
      <OfferText v-model="document.accept_text" tag="p" :editable="editable" label="Annahmetext" />
      <div class="sig">
        <div v-if="receipt?.status === 'accepted'" class="acceptance-stamp">
          <strong
            ><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m5 12 4 4L19 6" /></svg>DIGITAL
            ANGENOMMEN</strong
          >
          <span>{{ receipt.accepted_name }}</span>
          <span>{{ receipt.accepted_company }}</span>
          <span>{{ receiptTime(receipt.accepted_at) }} · Europe/Vienna</span>
          <small v-if="receipt.document_sha256" :title="receipt.document_sha256"
            >Annahmenachweis (SHA-256):<br /><code
              >{{ receipt.document_sha256.slice(0, 24) }}…</code
            ></small
          >
        </div>
        <div v-else>Ort, Datum, Unterschrift Auftraggeber</div>
        <div>Ort, Datum, Unterschrift Auftragnehmer</div>
      </div>
    </div>
    <OfferQR
      v-if="publicUrl || qrPreview"
      :url="publicUrl"
      :accepted="receipt?.status === 'accepted'"
      :preview="!publicUrl && qrPreview"
    />
  </div>
</template>

<style scoped>
.sig .acceptance-stamp {
  border: 1px solid #55d0c0;
  border-radius: 8px;
  padding: 14px;
  background: #effafa;
  color: #203c3d;
  display: grid;
  gap: 4px;
  font-size: 11px;
  line-height: 1.4;
  overflow-wrap: anywhere;
  white-space: normal;
  text-transform: none;
  letter-spacing: normal;
  min-width: 0;
}
.acceptance-stamp strong {
  display: flex;
  align-items: center;
  gap: 6px;
  color: #0e6f6c;
  letter-spacing: 0.08em;
  font-size: 12px;
}
.acceptance-stamp svg {
  width: 16px;
  height: 16px;
  fill: none;
  stroke: currentColor;
  stroke-width: 2;
  flex: none;
}
.acceptance-stamp small {
  margin-top: 6px;
  font-size: 9px;
}
.acceptance-stamp code {
  font-size: 10px;
}
</style>
