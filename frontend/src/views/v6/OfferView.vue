<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, RouterLink, useRoute, useRouter } from 'vue-router'
import {
  ArrowLeft,
  Check,
  ChevronDown,
  ChevronUp,
  Copy,
  FileCheck2,
  Link,
  ListPlus,
  LoaderCircle,
  Minus,
  Plus,
  Printer,
  Save,
  Settings2,
} from 'lucide-vue-next'
import { ensureUTC } from '@/composables/useDateFormat'
import { OFFER_CHROME_KEY } from '@/composables/useOfferChrome'
import { publicURL } from '@/publicPath'
import { offerStatus, receiptTime } from '@/components/offers/types'
import { api, errMsg, ApiError } from '@/api/client'
import { crmEnabled, instanceHostname, loadInstance } from '@/api/instance'
import { useAuthStore } from '@/stores/auth'
import OfferDocument from '@/components/offers/OfferDocument.vue'
import OfferSettingsDialog from '@/components/offers/OfferSettingsDialog.vue'
import type { Offer, OfferSettings } from '@/components/offers/types'
const route = useRoute(),
  router = useRouter(),
  auth = useAuthStore()
const offer = ref<Offer>()
const collapsed = inject(OFFER_CHROME_KEY, ref(true))
const toolbar = ref<HTMLElement>()
const viewport = ref<HTMLElement>()
const zoomMode = ref('width')
const zoom = ref(1)
const zoomSteps = [50, 75, 100, 125, 150, 175, 200]
function fitZoom() {
  const width = (viewport.value?.clientWidth ?? window.innerWidth) - 32
  const height = window.innerHeight - (toolbar.value?.getBoundingClientRect().bottom ?? 0) - 32
  zoom.value =
    zoomMode.value === 'width'
      ? Math.max(0.1, width / ((210 * 96) / 25.4))
      : zoomMode.value === 'page'
        ? Math.max(0.1, Math.min(width / ((210 * 96) / 25.4), height / ((297 * 96) / 25.4)))
        : Number(zoomMode.value) / 100
}
function stepZoom(direction: number) {
  const current = Math.round(zoom.value * 100)
  zoomMode.value = String(
    direction > 0
      ? (zoomSteps.find((n) => n > current) ?? 200)
      : ([...zoomSteps].reverse().find((n) => n < current) ?? 50),
  )
}
watch([zoomMode, collapsed], async () => {
  await nextTick()
  fitZoom()
})
let resizeObserver: ResizeObserver | undefined
const saveFeedback = ref(false)
const saveFailed = ref(false)
let feedbackTimer: ReturnType<typeof setTimeout> | undefined
const busy = computed(() => saving.value || saveFeedback.value)
const savedAt = computed(() => {
  const raw = offer.value?.updated_at
  if (!raw) return ''
  const date = new Date(ensureUTC(raw))
  if (!Number.isFinite(date.getTime())) return ''
  const today = date.toLocaleDateString('de-AT') === new Date().toLocaleDateString('de-AT')
  return `${today ? 'Heute' : date.toLocaleDateString('de-AT')}, ${date.toLocaleTimeString('de-AT')}`
})
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
watch(loading, async () => {
  await nextTick()
  if (toolbar.value) resizeObserver?.observe(toolbar.value)
  fitZoom()
})
watch(finalizeOpen, async (open) => {
  await nextTick()
  if (open) finalizeDialog.value?.showModal()
  else finalizeDialog.value?.close()
})
const publicUrl = computed(() =>
  offer.value?.public_token
    ? new URL(publicURL(`/offers/${offer.value.public_token}`), window.location.origin).href
    : '',
)
const copied = ref(false)
async function copyLink() {
  if (!offer.value) return
  try {
    if (!publicUrl.value) offer.value = await api.post<Offer>(`/offers/${offer.value.id}/link`, {})
    await navigator.clipboard.writeText(publicUrl.value)
    copied.value = true
  } catch (e) {
    error.value = errMsg(e)
  }
}
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
  loading.value
    ? 'Lädt …'
    : !offer.value
      ? 'Nicht geladen'
      : conflict.value
        ? 'Speicherkonflikt'
        : busy.value
          ? 'Speichert …'
          : saveFailed.value
            ? 'Nicht gespeichert'
            : dirty.value
              ? 'Ungespeichert'
              : offer.value?.status !== 'draft' && offer.value
                ? offerStatus(offer.value.status)
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
    saveFailed.value = false
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
async function save(force = false): Promise<boolean> {
  if (document.querySelector('.offer-document .sheet input:invalid')) {
    saveFailed.value = true
    error.value = 'Bitte ungültige Zahlen korrigieren.'
    return false
  }
  if (pending) {
    const ok = await pending
    return force && ok ? save(true) : ok
  }
  if (!offer.value || (!dirty.value && !force)) return true
  if (force && !editable.value) return false
  if (conflict.value) return false
  if (timer) clearTimeout(timer)
  pending = (async () => {
    saving.value = true
    saveFailed.value = false
    saveFeedback.value = true
    if (feedbackTimer) clearTimeout(feedbackTimer)
    const started = Date.now()
    error.value = ''
    try {
      let forceWrite = force
      while (
        offer.value &&
        (forceWrite || JSON.stringify(offer.value.document) !== savedDocument)
      ) {
        forceWrite = false
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
      saveFailed.value = true
      error.value = errMsg(e)
      if (e instanceof ApiError && e.status === 409) conflict.value = true
      return false
    } finally {
      saving.value = false
      pending = undefined
      feedbackTimer = setTimeout(
        () => {
          saveFeedback.value = false
        },
        Math.max(0, 1000 - (Date.now() - started)),
      )
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
  const blob = new Blob([JSON.stringify(offer.value.document, null, 2)], {
    type: 'application/json',
  })
  const url = URL.createObjectURL(blob),
    link = document.createElement('a')
  link.href = url
  link.download = `${offer.value.offer_no}-entwurf.json`
  link.click()
  URL.revokeObjectURL(url)
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
  window.addEventListener('resize', fitZoom)
  resizeObserver = new ResizeObserver(fitZoom)
  if (viewport.value) resizeObserver.observe(viewport.value)
  if (toolbar.value) resizeObserver.observe(toolbar.value)
  fitZoom()
})
onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
  window.removeEventListener('beforeunload', beforeUnload)
  window.removeEventListener('resize', fitZoom)
  resizeObserver?.disconnect()
  if (feedbackTimer) clearTimeout(feedbackTimer)
})
onBeforeRouteLeave(async () => !dirty.value || (await save()))
</script>
<template>
  <main ref="viewport" class="offer-view" :class="{ 'is-collapsed': collapsed }">
    <template v-if="crmEnabled">
      <header ref="toolbar" class="offer-tools">
        <RouterLink
          class="tool-button icon-only"
          :to="
            printMode
              ? `/crm/offers/${route.params.id}`
              : offer
                ? `/crm/${offer.customer_id}`
                : '/crm'
          "
          :title="printMode ? 'Zum Angebot' : 'Zum Kunden'"
          :aria-label="printMode ? 'Zum Angebot' : 'Zum Kunden'"
          ><ArrowLeft :size="16"
        /></RouterLink>
        <strong class="offer-number" :title="instanceHostname">{{
          offer?.offer_no || 'Angebot'
        }}</strong>
        <div class="save-state" role="status" aria-live="polite">
          <button
            v-if="editable"
            class="save-button tool-button"
            type="button"
            :disabled="busy || loading"
            title="Jetzt speichern"
            aria-label="Jetzt speichern"
            @click="save(true)"
          >
            <LoaderCircle v-if="busy" :size="15" class="save-spinner" />
            <template v-else
              ><Check v-if="!dirty && !saveFailed" :size="15" class="save-check" /><Save
                :size="15"
                :class="{ 'save-hover': !dirty && !saveFailed }"
            /></template>
          </button>
          <LoaderCircle v-else-if="busy" :size="15" class="save-spinner" />
          <span>{{ state }}</span
          ><time
            v-if="savedAt"
            :datetime="offer?.updated_at"
            :title="`Zuletzt gespeichert: ${savedAt}`"
            >{{ savedAt }}</time
          >
        </div>
        <div class="offer-actions">
          <div class="zoom-controls" role="group" aria-label="Dokumentzoom">
            <button
              type="button"
              class="tool-button icon-only"
              title="Verkleinern"
              aria-label="Verkleinern"
              :disabled="zoom <= 0.5"
              @click="stepZoom(-1)"
            >
              <Minus :size="14" />
            </button>
            <select v-model="zoomMode" aria-label="Zoom">
              <option value="width">Seitenbreite</option>
              <option value="page">Ganze Seite</option>
              <option v-for="level in zoomSteps" :key="level" :value="String(level)">
                {{ level }} %
              </option>
            </select>
            <button
              type="button"
              class="tool-button icon-only"
              title="Vergrößern"
              aria-label="Vergrößern"
              :disabled="zoom >= 2"
              @click="stepZoom(1)"
            >
              <Plus :size="14" />
            </button>
          </div>
          <button
            v-if="offer && offer.status !== 'draft' && !printMode && (publicUrl || auth.isAdmin)"
            class="tool-button"
            type="button"
            :title="copied ? 'Link kopiert' : 'Kundenlink kopieren'"
            aria-label="Kundenlink kopieren"
            @click="copyLink"
          >
            <Link :size="16" /><span class="action-label">{{ copied ? 'Kopiert' : 'Link' }}</span>
          </button>
          <button
            v-if="editable"
            class="tool-button"
            type="button"
            title="Absender & Textbausteine"
            aria-label="Absender & Textbausteine"
            @click="settingsOpen = true"
          >
            <Settings2 :size="16" /><span class="action-label">Texte</span>
          </button>
          <button
            v-if="editable"
            class="tool-button"
            type="button"
            title="Position hinzufügen"
            aria-label="Position hinzufügen"
            @click="addPosition"
          >
            <ListPlus :size="16" /><span class="action-label">Position</span>
          </button>
          <button
            v-if="auth.isAdmin && offer && !printMode"
            class="tool-button"
            type="button"
            title="Duplizieren"
            aria-label="Duplizieren"
            :disabled="saving"
            @click="duplicate"
          >
            <Copy :size="16" /><span class="action-label">Duplizieren</span>
          </button>
          <button
            v-if="editable"
            class="tool-button"
            type="button"
            title="Finalisieren: Kundenlink und QR-Code erstellen"
            aria-label="Finalisieren"
            :disabled="saving || !!overflow"
            @click="finalizeOpen = true"
          >
            <FileCheck2 :size="16" /><span class="action-label">Finalisieren</span>
          </button>
          <button
            v-if="offer"
            class="tool-button print-button"
            type="button"
            title="Druckansicht / PDF"
            aria-label="Druckansicht / PDF"
            :disabled="saving || !!overflow || conflict"
            @click="printOffer"
          >
            <Printer :size="16" /><span>PDF</span>
          </button>
          <button
            v-if="!printMode"
            class="tool-button icon-only"
            type="button"
            :aria-expanded="!collapsed"
            :title="collapsed ? 'Kopfzeilen ausklappen' : 'Kopfzeilen einklappen'"
            :aria-label="collapsed ? 'Kopfzeilen ausklappen' : 'Kopfzeilen einklappen'"
            @click="collapsed = !collapsed"
          >
            <ChevronDown v-if="collapsed" :size="16" /><ChevronUp v-else :size="16" />
          </button>
        </div>
      </header>
      <p v-if="loading" class="offer-notice">Angebot wird geladen …</p>
      <p v-if="error || overflow" role="alert" class="offer-notice offer-error">
        {{ error || overflow }}
        <button v-if="dirty && !conflict" class="btn" @click="save()">Erneut speichern</button>
      </p>
      <p v-if="editable && !collapsed" class="offer-notice">
        Klicke in einen Text, um ihn zu bearbeiten. Änderungen werden automatisch gespeichert.
        Kundenlink und QR-Code werden beim Finalisieren erstellt.
      </p>
      <p v-else-if="offer?.status === 'sent' && !printMode" class="offer-notice">
        Finalisiert am {{ offer.sent_at?.slice(0, 10) }}. Zum Ändern ein neues Angebot duplizieren.
      </p>
      <p v-if="offer?.status === 'accepted'" class="offer-notice" role="status">
        Angenommen von {{ offer.accepted_name }} · {{ offer.accepted_company }} ·
        {{ receiptTime(offer.accepted_at) }}<br v-if="offer.accepted_note" />{{
          offer.accepted_note
        }}
      </p>
      <p v-if="offer?.status === 'expired'" class="offer-notice">
        Die Bindefrist ist abgelaufen. Der Kundenlink zeigt das Angebot ohne Annahmeformular.
      </p>
      <div v-if="conflict" class="offer-notice" role="alert">
        Deine Änderungen sind noch in diesem Fenster. Du kannst sie sichern oder den aktuellen
        Serverstand laden.
        <button type="button" class="btn" @click="downloadDraft">
          Lokalen Entwurf herunterladen
        </button>
        <button type="button" class="btn" @click="load">
          Serverstand laden und lokale Änderungen verwerfen
        </button>
      </div>
      <OfferDocument
        v-if="offer"
        ref="renderer"
        :key="`${offer.id}-${printMode}`"
        :offer="offer"
        :zoom="zoom"
        :public-url="publicUrl"
        :editable="editable"
        @overflow="overflow = $event"
      />
      <OfferSettingsDialog
        :open="settingsOpen"
        @close="settingsOpen = false"
        @saved="applySettings"
      />
      <dialog
        ref="finalizeDialog"
        class="finalize-dialog"
        aria-label="Angebot finalisieren"
        @cancel.prevent="finalizeOpen = false"
      >
        <h2>Angebot finalisieren</h2>
        <p>
          Absender, Kundenanschrift, Texte und Preise werden festgeschrieben. Danach kannst du das
          Angebot per Kundenlink, QR-Code oder PDF weitergeben. Der QR-Code erscheint dann auch im
          Dokument und in der PDF. Wer den Kundenlink besitzt, kann das Angebot ansehen und bis zum
          Ablaufdatum annehmen. Eine E-Mail wird dabei nicht verschickt.
        </p>
        <p v-if="error" role="alert">{{ error }}</p>
        <button class="btn btn-primary" :disabled="saving" @click="finalize">
          Jetzt finalisieren</button
        ><button type="button" class="btn" @click="finalizeOpen = false">Abbrechen</button>
      </dialog>
    </template>
    <p v-else class="offer-notice">CRM ist auf dieser Instanz deaktiviert.</p>
  </main>
</template>
<style scoped>
.finalize-dialog {
  color: #203c3d;
  background: #fffefa;
  border: 1px solid #dfe6e5;
  border-radius: 12px;
  padding: 24px;
  width: min(500px, calc(100vw - 32px));
}
.finalize-dialog::backdrop {
  background: #10232788;
}
.finalize-dialog h2 {
  font-size: 18px;
  margin: 0 0 12px;
}
.finalize-dialog p {
  line-height: 1.5;
}

.offer-view {
  min-width: 0;
}
.offer-tools {
  display: flex;
  gap: 10px;
  align-items: center;
  flex-wrap: wrap;
  padding: 6px 12px;
  background: var(--h-surface, var(--bg-card));
  border-bottom: 1px solid var(--border);
  position: sticky;
  top: 0;
  z-index: 20;
}
.offer-number {
  font-size: 12px;
  white-space: nowrap;
}
.tool-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 30px;
  min-height: 30px;
  padding: 0 8px;
  border: 1px solid transparent;
  border-radius: 6px;
  background: transparent;
  color: inherit;
  font: inherit;
  font-size: 12px;
  text-decoration: none;
  cursor: pointer;
  white-space: nowrap;
}
.tool-button:hover:not(:disabled),
.tool-button:focus-visible {
  background: var(--h-hover, #e7f2ef);
  border-color: var(--border);
}
.tool-button:focus-visible,
select:focus-visible {
  outline: 2px solid #0e6f6c;
  outline-offset: 2px;
}
.tool-button:disabled {
  opacity: 0.5;
  cursor: default;
}
.icon-only {
  width: 28px;
  padding: 0;
}
.save-state {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
}
.save-state time {
  margin-left: 3px;
  font-variant-numeric: tabular-nums;
}
.save-button {
  width: 27px;
  padding: 0;
}
.save-check {
  color: #248358;
}
.save-hover {
  display: none;
}
.save-button:hover .save-check,
.save-button:focus-visible .save-check {
  display: none;
}
.save-button:hover .save-hover,
.save-button:focus-visible .save-hover {
  display: block;
}
.save-spinner {
  animation: save-spin 1s linear infinite;
}
@keyframes save-spin {
  to {
    transform: rotate(360deg);
  }
}
@media (prefers-reduced-motion: reduce) {
  .save-spinner {
    animation: none;
  }
}
.offer-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  margin-left: auto;
}
.is-collapsed .action-label {
  display: none;
}
.zoom-controls {
  display: flex;
  align-items: center;
  margin-right: 8px;
}
.zoom-controls select {
  max-width: 120px;
  height: 28px;
  min-height: 28px;
  padding: 0 4px;
  line-height: normal;
  box-sizing: border-box;
  font: inherit;
  font-size: 11px;
  color: inherit;
  border: 0;
  border-radius: 4px;
  background: var(--h-surface, var(--bg-card));
  cursor: pointer;
}
.print-button {
  color: #0e6f6c;
  background: #e0f3ef;
}
@media (max-width: 760px) {
  .offer-tools {
    gap: 4px;
  }
  .action-label {
    display: none;
  }
  .offer-actions {
    max-width: 100%;
    overflow-x: auto;
    margin-left: 0;
  }
  .offer-actions > * {
    flex-shrink: 0;
  }
  .save-state {
    font-size: 10px;
  }
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
