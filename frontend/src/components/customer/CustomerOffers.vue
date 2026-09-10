<script setup lang="ts">
import { ref, watch } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { api, errMsg } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { crmEnabled } from '@/api/instance'
import OfferSettingsDialog from '@/components/offers/OfferSettingsDialog.vue'
import { money, date, type Offer, type OfferSettings } from '@/components/offers/types'
const props = defineProps<{ customerId: number }>()
const router = useRouter(),
  auth = useAuthStore()
const offers = ref<Offer[]>([]),
  error = ref(''),
  busy = ref(false),
  settingsOpen = ref(false),
  createAfterSettings = ref(false)
watch(
  () => props.customerId,
  async () => {
    try {
      offers.value = await api.get<Offer[]>(`/customers/${props.customerId}/offers`)
    } catch (e) {
      error.value = errMsg(e)
    }
  },
  { immediate: true },
)
async function create() {
  busy.value = true
  error.value = ''
  try {
    const s = await api.get<OfferSettings>('/integrations/crm/offers')
    if (!s.sender.company) {
      createAfterSettings.value = true
      settingsOpen.value = true
      return
    }
    const o = await api.post<Offer>('/offers', { customer_id: props.customerId })
    await router.push(`/crm/offers/${o.id}`)
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    busy.value = false
  }
}
function saved() {
  if (createAfterSettings.value) {
    createAfterSettings.value = false
    void create()
  }
}
</script>
<template>
  <section v-if="crmEnabled" class="customer-offers">
    <header>
      <h2>Angebote</h2>
      <button v-if="auth.isAdmin" class="btn btn-primary btn-sm" :disabled="busy" @click="create">
        + Angebot erstellen
      </button>
    </header>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="!offers.length">Noch keine Angebote.</p>
    <RouterLink v-for="o in offers" :key="o.id" class="offer-row" :to="`/crm/offers/${o.id}`"
      ><span
        ><strong>{{ o.offer_no }}</strong> · {{ o.document.title
        }}<small>{{ date(o.document.offer_date) }}</small></span
      ><span
        >{{ money(o.document.net_total_cents)
        }}<small>{{ o.status === 'draft' ? 'Entwurf' : 'Finalisiert' }}</small></span
      ></RouterLink
    ><OfferSettingsDialog :open="settingsOpen" @saved="saved" @close="settingsOpen = false" />
  </section>
</template>
<style scoped>
.customer-offers {
  padding: 20px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--bg-card);
}
header {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: center;
}
h2 {
  font-size: 15px;
  margin: 0;
}
.offer-row {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 0;
  color: inherit;
  text-decoration: none;
  border-bottom: 1px solid var(--border);
}
small {
  display: block;
  color: var(--text-muted);
  margin-top: 4px;
}
.offer-row > span:last-child {
  text-align: right;
}
p {
  font-size: 13px;
  color: var(--text-muted);
}
</style>
