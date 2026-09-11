<script setup lang="ts">
import OfferQR from './OfferQR.vue'
import OfferText from './OfferText.vue'
import { money, net, type OfferDocument } from './types'
defineProps<{
  document: OfferDocument
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
        <div>Ort, Datum, Unterschrift Auftraggeber</div>
        <div>Ort, Datum, Unterschrift Auftragnehmer</div>
      </div>
    </div>
    <OfferQR v-if="publicUrl || qrPreview" :url="publicUrl" :preview="!publicUrl && qrPreview" />
  </div>
</template>
