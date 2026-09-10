<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { api } from '@/api/client'
import { date, type Offer } from './types'
const offers = ref<Offer[]>([])
onMounted(async () => {
  try {
    offers.value = await api.get<Offer[]>('/offers/acceptances')
  } catch {
    /* The CRM remains usable when the notice feed is temporarily unavailable. */
  }
})
</script>
<template>
  <aside v-if="offers.length" aria-label="Angenommene Angebote" class="acceptance-notices">
    <strong>Ihre angenommenen Angebote</strong
    ><RouterLink v-for="offer in offers" :key="offer.id" :to="`/crm/offers/${offer.id}`"
      >{{ offer.offer_no }} · {{ offer.document.customer.name }} · {{ offer.accepted_name }} ·
      {{ date(offer.accepted_at!) }}</RouterLink
    >
  </aside>
</template>
<style scoped>
.acceptance-notices {
  display: grid;
  gap: 8px;
  background: var(--h-surface, #fffefa);
  padding: 16px;
  border: 1px solid var(--border, #c9d4d2);
  border-radius: 10px;
  font-size: 13px;
}
.acceptance-notices a {
  color: inherit;
}
</style>
