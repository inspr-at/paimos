<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppIcon from '../components/AppIcon.vue'
import ReleaseName from '../components/ReleaseName.vue'
import { setPageTitle } from '../lib/brand'
import { resilientFetch } from '../lib/api'

const props = defineProps<{ tenantSlug: string; productSlug?: string }>()
const pageBase = computed(() => `/portal/${encodeURIComponent(props.tenantSlug)}${props.productSlug ? `/products/${encodeURIComponent(props.productSlug)}` : ''}`)
const apiBase = computed(() => `/api/public/portal/${encodeURIComponent(props.tenantSlug)}${props.productSlug ? `/products/${encodeURIComponent(props.productSlug)}` : ''}`)

interface PublicNote {
  pill_en: string
  pill_de: string
  benefit_en: string
  benefit_de: string
}
interface PublicRelease {
  released_at: string
  version?: string
  codename?: string
  notes: PublicNote[]
}
interface ReleasesDocument {
  product: { key: string; title: string; summary: string } | null
  releases: PublicRelease[]
}

const langKey = 'aeon.portal.releases.lang'

const loading = ref(true)
const missing = ref(false)
const error = ref('')
const doc = ref<ReleasesDocument | null>(null)
const lang = ref<'en' | 'de'>(storedLang())

function storedLang(): 'en' | 'de' {
  try {
    return localStorage.getItem(langKey) === 'de' ? 'de' : 'en'
  } catch {
    return 'en'
  }
}

function setLang(next: 'en' | 'de') {
  lang.value = next
  try {
    localStorage.setItem(langKey, next)
  } catch {
    // Private mode keeps the choice for this view only.
  }
}

function releasedOn(iso: string) {
  const parsed = new Date(iso)
  if (Number.isNaN(parsed.getTime())) return iso
  return parsed.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })
}

function validTime(iso: string) {
  return !Number.isNaN(new Date(iso).getTime())
}

function line(note: PublicNote, field: 'pill' | 'benefit') {
  const en = (field === 'pill' ? note.pill_en : note.benefit_en)?.trim() ?? ''
  const de = (field === 'pill' ? note.pill_de : note.benefit_de)?.trim() ?? ''
  if (lang.value === 'de' && de) return { value: de, de: true }
  if (en) return { value: en, de: false }
  if (de) return { value: de, de: true }
  return { value: '', de: false }
}

function noteHasText(note: PublicNote) {
  return [note.pill_en, note.pill_de, note.benefit_en, note.benefit_de].some(value => (value ?? '').trim() !== '')
}

function noteHasGerman(note: PublicNote) {
  return (note.pill_de ?? '').trim() !== '' || (note.benefit_de ?? '').trim() !== ''
}

const visibleReleases = computed(() => (doc.value?.releases ?? []).filter(release => release.notes.some(noteHasText)))
const hasGerman = computed(() => visibleReleases.value.some(release => release.notes.some(noteHasGerman)))

let loadRevision = 0
async function load() {
  const revision = ++loadRevision
  const address = apiBase.value
  loading.value = true
  error.value = ''
  missing.value = false
  try {
    const response = await resilientFetch(`${address}/releases`, {
      credentials: 'omit',
      cache: 'no-store',
      referrerPolicy: 'no-referrer',
    })
    if (revision !== loadRevision || address !== apiBase.value) return
    if (response.status === 404) {
      missing.value = true
      doc.value = null
      setPageTitle('Portal unavailable')
      return
    }
    if (!response.ok) throw new Error('unavailable')
    const document = await response.json() as ReleasesDocument
    if (revision !== loadRevision || address !== apiBase.value) return
    doc.value = document
    setPageTitle(doc.value.product ? 'Releases' : 'Nothing published yet')
  } catch {
    if (revision !== loadRevision || address !== apiBase.value) return
    error.value = 'Releases could not be loaded.'
    doc.value = null
    setPageTitle('Releases')
  } finally {
    if (revision === loadRevision) loading.value = false
  }
}

watch(() => [props.tenantSlug, props.productSlug], () => { void load() }, { immediate: true })
</script>

<template>
  <div class="portal">
    <div class="sheet">
      <p v-if="loading" class="eyebrow" role="status">Loading releases…</p>
      <template v-else-if="missing">
        <p class="eyebrow">Product portal</p>
        <h1>This portal is not available</h1>
        <p class="lead">The address may be wrong, or this workspace has not published a portal.</p>
      </template>
      <template v-else-if="error">
        <p class="eyebrow">Product portal</p>
        <h1>Releases could not be loaded</h1>
        <p class="lead" role="alert">{{ error }}</p>
        <button class="again" type="button" @click="load"><AppIcon name="refresh" :size="14" />Try again</button>
      </template>
      <template v-else-if="!doc?.product">
        <p class="eyebrow">Product portal</p>
        <h1>Nothing published yet</h1>
        <p class="lead">This workspace has opened its portal and has not published a product.</p>
      </template>
      <template v-else>
        <nav class="jumps" aria-label="Portal">
          <router-link class="jump" :to="pageBase"><AppIcon name="arrow-left" :size="16" />Catalog</router-link>
        </nav>
        <p class="eyebrow">Product portal</p>
        <h1>Releases</h1>
        <div v-if="hasGerman" class="lang" role="group" aria-label="Language">
          <button type="button" :aria-pressed="lang === 'en'" @click="setLang('en')">English</button>
          <button type="button" lang="de" :aria-pressed="lang === 'de'" @click="setLang('de')">Deutsch</button>
        </div>
        <p v-if="!visibleReleases.length" class="quiet">No published releases yet.</p>
        <ol v-else class="releases">
          <li v-for="(release, index) in visibleReleases" :key="`${release.released_at}-${index}`" class="release">
            <!-- The marketing name leads; its calendar version shows on hover (AEON-430). -->
            <template v-if="release.codename && release.version">
              <h2><ReleaseName :version="release.version" :name="release.codename" /></h2>
              <p class="version">
                <time v-if="validTime(release.released_at)" :datetime="release.released_at">{{ releasedOn(release.released_at) }}</time>
                <template v-else>{{ releasedOn(release.released_at) }}</template>
              </p>
            </template>
            <template v-else>
              <h2>
                <time v-if="validTime(release.released_at)" :datetime="release.released_at">{{ releasedOn(release.released_at) }}</time>
                <template v-else>{{ releasedOn(release.released_at) }}</template>
              </h2>
              <p v-if="release.version" class="version" :title="release.version">{{ release.version }}</p>
            </template>
            <article v-for="(note, noteIndex) in release.notes" :key="noteIndex" class="note">
              <p v-if="line(note, 'pill').value" class="pill" :lang="line(note, 'pill').de ? 'de' : undefined">{{ line(note, 'pill').value }}</p>
              <p v-if="line(note, 'benefit').value" class="benefit" :lang="line(note, 'benefit').de ? 'de' : undefined">{{ line(note, 'benefit').value }}</p>
            </article>
          </li>
        </ol>
        <footer class="colophon">
          <a class="colophon-link" :href="`${pageBase}/llms.txt`">llms.txt</a>
        </footer>
      </template>
    </div>
  </div>
</template>

<style scoped>
.portal {
  min-height: 100%;
  background:
    radial-gradient(900px 420px at 0% -10%, var(--wash-1), transparent 70%),
    var(--surface);
  color: var(--ink);
  font-family: var(--font);
}
.sheet { width: min(920px, 100%); margin: 0 auto; padding: 36px 20px 72px; }
.eyebrow {
  margin: 18px 0 10px;
  font: 600 11px/1.4 var(--mono);
  letter-spacing: 0.14em;
  text-transform: uppercase;
  color: var(--ink-3);
}
h1 {
  margin: 0;
  font: 650 clamp(34px, 5vw, 56px)/1.02 var(--serif);
  letter-spacing: -0.03em;
  text-wrap: balance;
  overflow-wrap: anywhere;
}
.lead { max-width: 38rem; margin: 14px 0 0; font-size: 18px; line-height: 1.45; color: var(--ink-2); overflow-wrap: anywhere; }
.quiet { margin: 18px 0 0; color: var(--ink-2); line-height: 1.45; }
.jumps { display: flex; flex-wrap: wrap; gap: 4px 18px; }
.jump {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-height: 44px;
  color: var(--ink-2);
  font: 600 14px/1 var(--font);
  text-decoration: none;
}
.jump:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 8px; }
.lang { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 16px; }
.lang button {
  min-height: 44px;
  padding: 0 14px;
  border: 0;
  border-radius: 999px;
  background: transparent;
  color: var(--ink-3);
  font: 600 14px/1 var(--font);
  cursor: pointer;
}
.lang button[aria-pressed="true"] {
  background: var(--chip-bg);
  box-shadow: inset 0 0 0 1px var(--line);
  color: var(--ink);
}
.lang button:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.releases { list-style: none; margin: 22px 0 0; padding: 0; display: grid; gap: 12px; }
.release {
  min-width: 0;
  padding: 18px;
  border-radius: 16px;
  background: var(--surface-raised);
  box-shadow: inset 0 0 0 1px var(--line), 0 1px 2px color-mix(in srgb, var(--shadow-color) 4%, transparent);
}
.release h2 {
  margin: 0;
  font: 650 22px/1.25 var(--serif);
  letter-spacing: -0.02em;
}
.release h2 time { font: inherit; color: inherit; }
.release h2 :deep(.rn-stamp) { font-size: 13px; }
.version {
  margin: 4px 0 0;
  overflow: hidden;
  color: var(--ink-3);
  font-size: 12.5px;
  line-height: 1.35;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.note { margin-top: 14px; }
.note:first-of-type { margin-top: 12px; }
.pill { margin: 0; font-weight: 650; line-height: 1.35; overflow-wrap: anywhere; }
.benefit { margin: 6px 0 0; color: var(--ink-2); font-size: 15px; line-height: 1.5; overflow-wrap: anywhere; }
.colophon { margin-top: 48px; }
.colophon-link {
  display: inline-flex;
  align-items: center;
  min-height: 44px;
  color: var(--ink-3);
  font: 500 12.5px/1 var(--font);
  text-decoration: none;
}
.colophon-link:hover { color: var(--ink-2); }
.colophon-link:focus-visible { outline: none; box-shadow: var(--focus-ring); border-radius: 8px; }
.again {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  min-height: 44px;
  margin-top: 16px;
  padding: 0 16px;
  border: 0;
  border-radius: 999px;
  background: var(--btn-bg);
  box-shadow: var(--shadow-btn);
  color: var(--ink);
  font: 600 14px/1 var(--font);
  cursor: pointer;
}
.again:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (max-width: 560px) {
  .sheet { padding: 24px 16px 56px; }
}
:global(main:has(> .page-flow > .portal)) {
  scrollbar-gutter: auto;
  background:
    radial-gradient(900px 420px at 0% -10%, var(--wash-1), transparent 70%),
    var(--surface);
}
</style>
