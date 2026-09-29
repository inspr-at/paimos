<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppIcon from '../components/AppIcon.vue'
import { setPageTitle } from '../lib/brand'
import { resilientFetch } from '../lib/api'

const props = defineProps<{ tenantSlug: string }>()

interface PublicNote {
  pill_en: string
  pill_de: string
  benefit_en: string
  benefit_de: string
}
interface PublicRelease {
  released_at: string
  version?: string
  notes: PublicNote[]
}
interface ReleasesDocument {
  product: { key: string; title: string; summary: string } | null
  releases: PublicRelease[]
}

const loading = ref(true)
const missing = ref(false)
const error = ref('')
const doc = ref<ReleasesDocument | null>(null)

const title = computed(() => doc.value?.product?.title || 'Nothing published yet')

function releasedOn(iso: string) {
  const parsed = new Date(iso)
  if (Number.isNaN(parsed.getTime())) return iso
  return parsed.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })
}

function heading(release: PublicRelease) {
  return release.version || releasedOn(release.released_at)
}

function showsGerman(note: PublicNote) {
  const pill = note.pill_de.trim()
  const benefit = note.benefit_de.trim()
  if (!pill && !benefit) return false
  return pill !== note.pill_en.trim() || benefit !== note.benefit_en.trim()
}

async function load() {
  loading.value = true
  error.value = ''
  missing.value = false
  try {
    const response = await resilientFetch(`/api/public/portal/${encodeURIComponent(props.tenantSlug)}/releases`, {
      credentials: 'omit',
      cache: 'no-store',
      referrerPolicy: 'no-referrer',
    })
    if (response.status === 404) {
      missing.value = true
      doc.value = null
      setPageTitle('Portal unavailable')
      return
    }
    if (!response.ok) throw new Error('unavailable')
    doc.value = await response.json() as ReleasesDocument
    setPageTitle(doc.value.product?.title || 'Nothing published yet')
  } catch {
    error.value = 'Releases could not be loaded.'
    doc.value = null
    setPageTitle('Releases')
  } finally {
    loading.value = false
  }
}

watch(() => props.tenantSlug, () => { void load() }, { immediate: true })
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
          <router-link class="jump" :to="`/portal/${tenantSlug}`"><AppIcon name="arrow-left" :size="16" />Catalog</router-link>
          <a class="jump" :href="`/portal/${tenantSlug}/llms.txt`">llms.txt</a>
        </nav>
        <p class="eyebrow">Product portal</p>
        <h1>{{ title }}</h1>
        <p class="lead">What shipped, in the words saved when it shipped.</p>
        <p v-if="!doc.releases.length" class="quiet">No published releases yet.</p>
        <ol v-else class="releases">
          <li v-for="(release, index) in doc.releases" :key="`${release.released_at}-${index}`" class="release">
            <h2 class="version" :title="heading(release)">{{ heading(release) }}</h2>
            <p v-if="release.version" class="when">{{ releasedOn(release.released_at) }}</p>
            <article v-for="(note, noteIndex) in release.notes" :key="noteIndex" class="note">
              <p v-if="note.pill_en" class="pill">{{ note.pill_en }}</p>
              <p v-if="note.benefit_en" class="benefit">{{ note.benefit_en }}</p>
              <details v-if="showsGerman(note)">
                <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Deutsch</summary>
                <p v-if="note.pill_de" class="pill">{{ note.pill_de }}</p>
                <p v-if="note.benefit_de" class="benefit">{{ note.benefit_de }}</p>
              </details>
            </article>
          </li>
        </ol>
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
.releases { list-style: none; margin: 28px 0 0; padding: 0; display: grid; gap: 12px; }
.release {
  min-width: 0;
  padding: 18px;
  border-radius: 16px;
  background: var(--surface-raised);
  box-shadow: inset 0 0 0 1px var(--line), 0 1px 2px rgba(20, 40, 40, 0.04);
}
.version {
  margin: 0;
  overflow: hidden;
  font: 600 15px/1.35 var(--mono);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.when { margin: 6px 0 0; color: var(--ink-3); font-size: 13.5px; }
.note { margin-top: 14px; }
.note:first-of-type { margin-top: 12px; }
.pill { margin: 0; font-weight: 650; line-height: 1.35; overflow-wrap: anywhere; }
.benefit { margin: 6px 0 0; color: var(--ink-2); font-size: 15px; line-height: 1.5; overflow-wrap: anywhere; }
details { margin-top: 4px; }
summary {
  display: inline-flex;
  align-items: center;
  width: fit-content;
  min-height: 44px;
  color: var(--ink-2);
  cursor: pointer;
}
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
main:has(> .page-flow > .portal) { background: var(--surface); }
</style>
