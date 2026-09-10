<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, RouterLink, useRoute, useRouter } from 'vue-router'
import { api, errMsg, ApiError } from '@/api/client'
import { crmEnabled, loadInstance } from '@/api/instance'
import { useAuthStore } from '@/stores/auth'
import OfferDocument from '@/components/offers/OfferDocument.vue'
import OfferSettingsDialog from '@/components/offers/OfferSettingsDialog.vue'
import type { Offer, OfferSettings } from '@/components/offers/types'
const route = useRoute(),
  router = useRouter(),
  auth = useAuthStore()
const offer = ref<Offer>()
const error = ref(''),
  overflow = ref(''),
  saving = ref(false),
  dirty = ref(false),
  conflict = ref(false),
  settingsOpen = ref(false),
  finalizeOpen = ref(false)
const loading = ref(true),
  finalizing = ref(false)
const renderer = ref<InstanceType<typeof OfferDocument>>()
const finalizeDialog = ref<HTMLDialogElement>()
watch(finalizeOpen, async (open) => { await nextTick(); if (open) finalizeDialog.value?.showModal(); else finalizeDialog.value?.close() })
const printMode = computed(() => route.path.endsWith('/print'))
const editable = computed(
  () =>
    !!offer.value &&
    offer.value.status === 'draft' &&
    auth.isAdmin &&
    !printMode.value &&
    !conflict.value &&
    !finalizing.value,
)
let timer: ReturnType<typeof setTimeout> | undefined
let savedDocument = ''
let pending: Promise<boolean> | undefined
const state = computed(() =>
  conflict.value
    ? 'Speicherkonflikt'
    : saving.value
      ? 'Speichert …'
      : dirty.value
        ? 'Ungespeichert'
        : offer.value?.status === 'sent'
          ? 'Finalisiert'
          : 'Gespeichert',
)
async function load() {
  loading.value = true
  error.value = ''
  try {
    await loadInstance()
    offer.value = await api.get<Offer>(`/offers/${route.params.id}`)
    savedDocument = JSON.stringify(offer.value.document)
    dirty.value = false
    conflict.value = false
    document.title = `${offer.value.offer_no} · Angebot`
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    loading.value = false
  }
}
watch(
  () => offer.value?.document,
  () => {
    if (!editable.value || !offer.value) return
    dirty.value = JSON.stringify(offer.value.document) !== savedDocument
    if (timer) clearTimeout(timer)
    if (dirty.value) timer = setTimeout(() => void save(), 700)
  },
  { deep: true },
)
async function save(): Promise<boolean> {
  if (document.querySelector('.offer-document .sheet input:invalid')) {
    error.value = 'Bitte ungültige Zahlen korrigieren.'
    return false
  }
  if (pending) return pending
  if (!offer.value || !dirty.value) return true
  if (conflict.value) return false
  if (timer) clearTimeout(timer)
  pending = (async () => {
    saving.value = true
    error.value = ''
    try {
      while (offer.value && JSON.stringify(offer.value.document) !== savedDocument) {
        const snapshot = JSON.stringify(offer.value.document)
        const result = await api.put<Offer>(`/offers/${offer.value.id}`, {
          revision: offer.value.revision,
          document: JSON.parse(snapshot),
        })
        offer.value.revision = result.revision
        offer.value.updated_at = result.updated_at
        savedDocument = snapshot
      }
      dirty.value = false
      return true
    } catch (e) {
      error.value = errMsg(e)
      if (e instanceof ApiError && e.status === 409) conflict.value = true
      return false
    } finally {
      saving.value = false
      pending = undefined
    }
  })()
  return pending
}
async function finalize() {
  if (!offer.value || !(await save())) return
  await renderer.value?.paginate()
  if (overflow.value) return
  saving.value = true
  finalizing.value = true
  error.value = ''
  try {
    const result = await api.put<Offer>(`/offers/${offer.value.id}`, {
      revision: offer.value.revision,
      document: offer.value.document,
      finalize: true,
    })
    savedDocument = JSON.stringify(result.document)
    offer.value = result
    dirty.value = false
    finalizeOpen.value = false
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    saving.value = false
    finalizing.value = false
  }
}
async function printOffer() {
  if (!(await save())) return
  await document.fonts.ready
  await renderer.value?.paginate()
  await nextTick()
  if (overflow.value || document.querySelector('.offer-document .sheet input:invalid')) {
    error.value = overflow.value || 'Bitte ungültige Zahlen korrigieren.'
    return
  }
  if (printMode.value) window.print()
  else await router.push(`/crm/offers/${offer.value!.id}/print`)
}
async function duplicate() {
  if (!offer.value || !(await save())) return
  saving.value = true
  error.value = ''
  try {
    const copy = await api.post<Offer>('/offers', {
      customer_id: offer.value.customer_id,
      duplicate_id: offer.value.id,
    })
    await router.push(`/crm/offers/${copy.id}`)
    await load()
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    saving.value = false
  }
}
function applySettings(s: OfferSettings) {
  if (editable.value && offer.value) offer.value.document.sender = structuredClone(s.sender)
}
function downloadDraft() {
 if (!offer.value) return
 const blob = new Blob([JSON.stringify(offer.value.document, null, 2)], { type: 'application/json' })
 const url = URL.createObjectURL(blob), link = document.createElement('a')
 link.href = url; link.download = `${offer.value.offer_no}-entwurf.json`; link.click(); URL.revokeObjectURL(url)
}
function addPosition() {
  offer.value?.document.positions.push({
    short_text: '',
    long_text: '',
    quantity: 1,
    unit: 'Std.',
    unit_price_cents: 0,
    total_cents: 0,
  })
}
function beforeUnload(e: BeforeUnloadEvent) {
  if (dirty.value || saving.value) {
    e.preventDefault()
    e.returnValue = ''
  }
}
onMounted(() => {
  void load()
  window.addEventListener('beforeunload', beforeUnload)
})
onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
  window.removeEventListener('beforeunload', beforeUnload)
})
onBeforeRouteLeave(async () => !dirty.value || (await save()))
</script>
<template>
  <main class="offer-view">
    <template v-if="crmEnabled">
      <header class="offer-tools">
        <RouterLink
          :to="
            printMode
              ? `/crm/offers/${route.params.id}`
              : offer
                ? `/crm/${offer.customer_id}`
                : '/crm'
          "
          >← {{ printMode ? 'Zum Angebot' : 'Zum Kunden' }}</RouterLink
        ><strong>{{ offer?.offer_no || 'Angebot' }}</strong
        ><span role="status">{{ state }}</span>
        <div class="offer-actions">
          <button v-if="editable" type="button" class="btn" @click="settingsOpen = true">
            Absender &amp; Textbausteine</button
          ><button v-if="editable" type="button" class="btn" @click="addPosition">+ Position</button
          ><button
            v-if="auth.isAdmin && offer && !printMode"
            type="button"
            class="btn"
            :disabled="saving"
            @click="duplicate"
          >
            Duplizieren</button
          ><button
            v-if="editable"
            type="button"
            class="btn"
            :disabled="saving || !!overflow"
            @click="finalizeOpen = true"
          >
            Finalisieren</button
          ><button
            v-if="offer"
            type="button"
            class="btn btn-primary"
            :disabled="saving || !!overflow || conflict"
            @click="printOffer"
          >
            {{ printMode ? 'Drucken / PDF' : 'Druckansicht / PDF' }}
          </button>
        </div>
      </header>
      <p v-if="loading" class="offer-notice">Angebot wird geladen …</p>
      <p v-if="error || overflow" role="alert" class="offer-notice offer-error">
        {{ error || overflow }}
        <button v-if="dirty && !conflict" class="btn" @click="save">Erneut speichern</button>
      </p>
      <p v-if="editable" class="offer-notice">
        Klicke in einen Text, um ihn zu bearbeiten. Änderungen werden automatisch gespeichert.
      </p>
      <p v-else-if="offer?.status === 'sent' && !printMode" class="offer-notice">
        Finalisiert am {{ offer.sent_at?.slice(0, 10) }}. Zum Ändern ein neues Angebot duplizieren.
      </p>
      <div v-if="conflict" class="offer-notice" role="alert">
        Deine Änderungen sind noch in diesem Fenster. Du kannst sie sichern oder den aktuellen Serverstand laden.
        <button type="button" class="btn" @click="downloadDraft">Lokalen Entwurf herunterladen</button>
        <button type="button" class="btn" @click="load">Serverstand laden und lokale Änderungen verwerfen</button>
      </div>
      <OfferDocument
        v-if="offer"
        ref="renderer"
        :key="`${offer.id}-${printMode}`"
        :offer="offer"
        :editable="editable"
        @overflow="overflow = $event"
      />
      <OfferSettingsDialog
        :open="settingsOpen"
        @close="settingsOpen = false"
        @saved="applySettings"
      />
      <dialog ref="finalizeDialog" class="finalize-dialog" aria-label="Angebot finalisieren" @cancel.prevent="finalizeOpen=false"><h2>Angebot finalisieren</h2><p>
          Absender, Kundenanschrift, Texte und Preise werden festgeschrieben. Danach kannst du das
          Angebot als PDF weitergeben. Eine E-Mail wird dabei nicht verschickt.
        </p>
        <p v-if="error" role="alert">{{ error }}</p>
        <button class="btn btn-primary" :disabled="saving" @click="finalize">
          Jetzt finalisieren
        </button><button type="button" class="btn" @click="finalizeOpen=false">Abbrechen</button></dialog>
    </template>
    <p v-else class="offer-notice">CRM ist auf dieser Instanz deaktiviert.</p>
  </main>
</template>
<style scoped>
.finalize-dialog { color:#203c3d;background:#fffefa;border:1px solid #dfe6e5;border-radius:12px;padding:24px;width:min(500px,calc(100vw - 32px)); }
.finalize-dialog::backdrop {background:#10232788}
.finalize-dialog h2 {font-size:18px;margin:0 0 12px}
.finalize-dialog p {line-height:1.5}

.offer-view {
  min-width: 0;
}
.offer-tools {
  display: flex;
  gap: 14px;
  align-items: center;
  flex-wrap: wrap;
  padding: 14px 20px;
  background: var(--h-surface, var(--bg-card));
  border-bottom: 1px solid var(--border);
}
.offer-tools a {
  color: inherit;
}
.offer-tools [role='status'] {
  font-size: 12px;
  color: var(--text-muted);
}
.offer-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-left: auto;
}
.offer-notice {
  padding: 8px 20px;
  margin: 0;
  font-size: 13px;
}
.offer-error {
  color: var(--danger, #b42318);
}
</style>
