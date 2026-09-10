<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { apiURL, publicURL } from '@/publicPath'
import OfferDocument from '@/components/offers/OfferDocument.vue'
import { date, type PublicOffer } from '@/components/offers/types'
const route = useRoute()
const offer = ref<PublicOffer>()
const error = ref(''),
  overflow = ref(''),
  loading = ref(true),
  busy = ref(false)
const name = ref(''),
  company = ref(''),
  note = ref(''),
  confirmed = ref(false)
const renderer = ref<InstanceType<typeof OfferDocument>>()
const publicUrl = computed(
  () => new URL(publicURL(`/offers/${String(route.params.token)}`), window.location.origin).href,
)
async function request(method = 'GET', body?: unknown): Promise<PublicOffer> {
  const response = await fetch(
    apiURL(
      `/public/offers/${encodeURIComponent(String(route.params.token))}${method === 'POST' ? '/accept' : ''}`,
    ),
    {
      method,
      credentials: 'omit',
      cache: 'no-store',
      referrerPolicy: 'no-referrer',
      headers:
        method === 'POST' ? { 'Content-Type': 'application/json', 'X-Offer-Acceptance': '1' } : {},
      body: body ? JSON.stringify(body) : undefined,
    },
  )
  if (!response.ok) {
    if (response.status === 404)
      throw new Error('Dieses Angebot ist nicht verfügbar. Bitte wenden Sie sich an den Absender.')
    if (response.status === 409)
      throw new Error(
        'Dieses Angebot kann nicht mehr angenommen werden. Bitte laden Sie den aktuellen Stand.',
      )
    if (response.status === 429)
      throw new Error('Zu viele Anfragen. Bitte in einer Minute erneut versuchen.')
    throw new Error(
      'Das Angebot konnte nicht geladen oder gespeichert werden. Bitte erneut versuchen.',
    )
  }
  return response.json()
}
async function load() {
  loading.value = true
  error.value = ''
  offer.value = undefined
  confirmed.value = false
  try {
    offer.value = await request()
    name.value = offer.value.document.customer.contact
    company.value = offer.value.document.customer.name
    document.title = `${offer.value.offer_no} · Angebot`
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Angebot konnte nicht geladen werden.'
  } finally {
    loading.value = false
  }
}
async function accept() {
  if (!offer.value || busy.value || !confirmed.value || !name.value.trim() || !company.value.trim())
    return
  busy.value = true
  error.value = ''
  try {
    offer.value = await request('POST', {
      name: name.value,
      company: company.value,
      note: note.value,
      confirmed: confirmed.value,
      revision: offer.value.revision,
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Annahme konnte nicht gespeichert werden.'
  } finally {
    busy.value = false
  }
}
async function print() {
  await document.fonts.ready
  await renderer.value?.paginate()
  await nextTick()
  if (!overflow.value) window.print()
}
watch(
  () => route.params.token,
  () => {
    note.value = ''
    void load()
  },
  { immediate: true },
)
</script>
<template>
  <main class="public-offer">
    <section class="offer-controls">
      <p v-if="loading" role="status">Angebot wird geladen …</p>
      <div v-if="error" role="alert">
        <p>{{ error }}</p>
        <button type="button" class="btn" :disabled="busy" @click="load">
          Aktuellen Stand laden
        </button>
      </div>
      <template v-if="offer">
        <header>
          <div>
            <small>{{ offer.document.sender.company }} · {{ offer.offer_no }}</small>
            <h1>{{ offer.document.title }}</h1>
            <p>
              Für {{ offer.document.customer.name }} · gültig bis
              {{ date(offer.document.valid_until) }}
            </p>
          </div>
          <button type="button" class="btn" :disabled="!!overflow" @click="print">
            Als PDF drucken
          </button>
        </header>
        <p v-if="overflow" role="alert">{{ overflow }}</p>
        <div v-if="offer.status === 'accepted'" class="receipt" role="status">
          <h2>Vielen Dank. Das Angebot wurde angenommen.</h2>
          <p>
            {{ offer.accepted_name }} · {{ offer.accepted_company }}<br />{{
              offer.accepted_at?.replace('T', ' ').replace('Z', ' UTC')
            }}
          </p>
          <p v-if="offer.accepted_note">{{ offer.accepted_note }}</p>
        </div>
        <p v-else-if="offer.status === 'expired'" class="receipt" role="status">
          Die Bindefrist ist abgelaufen. Eine Online-Annahme ist nicht mehr möglich. Bitte wenden
          Sie sich an {{ offer.document.sender.email }}.
        </p>
        <details v-else-if="offer.status === 'sent'" class="accept-details">
          <summary>Angebot annehmen</summary>
          <form @submit.prevent="accept">
            <h2>Angebot rechtsverbindlich annehmen</h2>
            <p>
              Bitte prüfen Sie das vollständige Angebot einschließlich der Bedingungen vor Ihrer
              Annahme.
            </p>
            <label
              >Ihr vollständiger Name<input
                v-model="name"
                name="name"
                autocomplete="name"
                required
                maxlength="200"
                :disabled="busy" /></label
            ><label
              >Firma<input
                v-model="company"
                name="company"
                autocomplete="organization"
                required
                maxlength="300"
                :disabled="busy" /></label
            ><label
              >Anmerkung (optional)<textarea
                v-model="note"
                maxlength="2000"
                :disabled="busy"
              /></label
            ><label class="confirm"
              ><input v-model="confirmed" type="checkbox" required :disabled="busy" /><span
                >Ich bin zur Vertretung der genannten Firma berechtigt und nehme das Angebot
                {{ offer.offer_no }} einschließlich seiner Bedingungen rechtsverbindlich an.</span
              ></label
            >
            <p class="audit-note">
              Zur Dokumentation werden Name, Firma, Zeitpunkt, Anmerkung sowie IP-Adresse und
              Browserkennung gespeichert.
            </p>
            <button
              type="submit"
              class="btn btn-primary"
              :disabled="busy || !confirmed || !name.trim() || !company.trim()"
            >
              {{ busy ? 'Annahme wird gespeichert …' : 'Jetzt rechtsverbindlich annehmen' }}
            </button>
          </form>
        </details>
      </template>
    </section>
    <div class="document-scroll">
      <OfferDocument
        v-if="offer"
        ref="renderer"
        :offer="offer"
        :public-url="publicUrl"
        @overflow="overflow = $event"
      />
    </div>
  </main>
</template>
<style scoped>
.public-offer {
  min-height: 100vh;
  background: #f7f6f2;
  color: #203c3d;
}
.offer-controls {
  max-width: 794px;
  margin: auto;
  padding: 24px;
  box-sizing: border-box;
}
header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 20px;
  flex-wrap: wrap;
}
h1 {
  font-size: 24px;
  margin: 6px 0;
}
h2 {
  font-size: 18px;
}
p {
  line-height: 1.5;
}
.document-scroll {
  overflow-x: auto;
  max-width: 100vw;
}
.accept-details,
.receipt {
  background: #fffefa;
  border: 1px solid #c9d4d2;
  border-radius: 12px;
  padding: 20px;
  margin-top: 20px;
}
summary {
  cursor: pointer;
  font-weight: 600;
  color: #0e6f6c;
}
form {
  display: grid;
  gap: 14px;
}
form p {
  margin: 0;
}
label {
  display: grid;
  gap: 6px;
}
input:not([type='checkbox']),
textarea {
  box-sizing: border-box;
  width: 100%;
  font: inherit;
  padding: 10px;
  border: 1px solid #a3aeac;
  background: white;
  color: #203c3d;
  border-radius: 6px;
}
.confirm {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  line-height: 1.5;
}
.confirm input {
  margin-top: 5px;
  accent-color: #0e6f6c;
}
.audit-note {
  font-size: 12px;
  color: #596e70;
}
button {
  white-space: normal;
}
@media print {
  .offer-controls {
    display: none;
  }
  .document-scroll {
    overflow: visible;
    max-width: none;
  }
  .public-offer {
    background: white;
  }
}
</style>
