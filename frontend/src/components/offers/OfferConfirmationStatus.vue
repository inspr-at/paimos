<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, errMsg } from '@/api/client'
import { apiURL } from '@/publicPath'
import { receiptTime, type OfferConfirmation } from './types'
const props = defineProps<{
  confirmation?: OfferConfirmation
  offerId?: number
  publicToken?: string
  admin?: boolean
}>()
const emit = defineEmits<{ refresh: [] }>()
const busy = ref(false),
  error = ref(''),
  acknowledge = ref(false)
const message = computed(
  () =>
    ({
      pending: 'Bestätigung wird vorbereitet',
      rendering: 'Angenommene PDF wird erstellt',
      sending: 'Bestätigung wird versendet',
      sent: `Bestätigung versendet · ${receiptTime(props.confirmation?.sent_at)}`,
      failed: 'Bestätigung konnte nicht versendet werden. Die Annahme ist gespeichert.',
      uncertain:
        'Versand unklar. Die E-Mail könnte bereits zugestellt sein. Die Annahme ist gespeichert.',
      legacy: 'Älteres Angebot: kein automatischer Bestätigungsversand hinterlegt.',
      unavailable: 'Versandstatus derzeit nicht verfügbar.',
    })[props.confirmation?.state || 'unavailable'],
)
const pdfURL = computed(() =>
  apiURL(
    props.publicToken
      ? `/public/offers/${encodeURIComponent(props.publicToken)}/pdf`
      : `/offers/${props.offerId}/pdf`,
  ),
)
async function retry() {
  if (!props.offerId || busy.value) return
  busy.value = true
  error.value = ''
  try {
    await api.post(`/offers/${props.offerId}/confirmation/retry`, {
      acknowledge_uncertain: acknowledge.value,
    })
    emit('refresh')
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    busy.value = false
  }
}
</script>
<template>
  <div class="confirmation-status" role="status">
    <span>{{ message }}</span>
    <a v-if="confirmation?.pdf_ready" :href="pdfURL" class="btn btn-sm" download>Angenommene PDF</a>
    <template v-if="admin && ['failed', 'uncertain'].includes(confirmation?.state || '')">
      <label v-if="confirmation?.state === 'uncertain'"
        ><input v-model="acknowledge" type="checkbox" /> Mögliche doppelte Zustellung beim erneuten
        Versand bestätigen</label
      >
      <button
        class="btn btn-sm"
        :disabled="busy || (confirmation?.state === 'uncertain' && !acknowledge)"
        @click="retry"
      >
        Erneut senden
      </button>
    </template>
    <span v-if="error" role="alert">{{ error }}</span>
  </div>
</template>
<style scoped>
.confirmation-status {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
  font-size: 13px;
  padding: 12px 0;
}
label {
  display: flex;
  align-items: center;
  gap: 6px;
}
@media print {
  .confirmation-status {
    display: none;
  }
}
</style>
