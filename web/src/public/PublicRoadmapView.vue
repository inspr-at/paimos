<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppIcon from '../components/AppIcon.vue'
import { setPageTitle } from '../lib/brand'
import { resilientFetch } from '../lib/api'

const props = defineProps<{ tenantSlug: string }>()

interface RoadmapItem {
  pill_en: string
  pill_de: string
  benefit_en: string
  benefit_de: string
  target: string
  status: string
}
interface RoadmapDocument {
  schema: string
  product: { key: string; title: string; summary: string } | null
  items: RoadmapItem[]
}

const langKey = 'aeon.portal.roadmap.lang'

const loading = ref(true)
const missing = ref(false)
const error = ref('')
const doc = ref<RoadmapDocument | null>(null)
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

function line(item: RoadmapItem, field: 'pill' | 'benefit') {
  const en = (field === 'pill' ? item.pill_en : item.benefit_en)?.trim() ?? ''
  const de = (field === 'pill' ? item.pill_de : item.benefit_de)?.trim() ?? ''
  if (lang.value === 'de' && de) return { value: de, de: true }
  if (en) return { value: en, de: false }
  if (de) return { value: de, de: true }
  return { value: '', de: false }
}

function itemHasText(item: RoadmapItem) {
  return [item.pill_en, item.pill_de, item.benefit_en, item.benefit_de].some(value => (value ?? '').trim() !== '')
}

function itemHasGerman(item: RoadmapItem) {
  return (item.pill_de ?? '').trim() !== '' || (item.benefit_de ?? '').trim() !== ''
}

const visibleItems = computed(() => (doc.value?.items ?? []).filter(itemHasText))
const hasGerman = computed(() => visibleItems.value.some(itemHasGerman))

const groups = computed(() => {
  const out: { target: string; items: RoadmapItem[] }[] = []
  for (const item of visibleItems.value) {
    const target = (item.target ?? '').trim()
    const last = out[out.length - 1]
    if (!last || last.target !== target) out.push({ target, items: [item] })
    else last.items.push(item)
  }
  return out
})

function targetLabel(target: string) {
  if (target === 'later') return lang.value === 'de' ? 'Später' : 'Later'
  const match = /^r(\d+)$/.exec(target)
  if (match) return `Release ${match[1]}`
  return target
}

function statusLabel(status: string) {
  const german = lang.value === 'de'
  switch (status) {
    case 'in_progress':
      return german ? 'In Arbeit' : 'In progress'
    case 'shipped':
      return german ? 'Geliefert' : 'Shipped'
    default:
      return german ? 'Geplant' : 'Planned'
  }
}

async function load() {
  loading.value = true
  error.value = ''
  missing.value = false
  try {
    const response = await resilientFetch(`/api/public/portal/${encodeURIComponent(props.tenantSlug)}/roadmap`, {
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
    doc.value = await response.json() as RoadmapDocument
    setPageTitle(doc.value.product ? "What's coming" : 'Nothing published yet')
  } catch {
    error.value = 'The roadmap could not be loaded.'
    doc.value = null
    setPageTitle("What's coming")
  } finally {
    loading.value = false
  }
}

watch(() => props.tenantSlug, () => { void load() }, { immediate: true })
</script>

<template>
  <div class="portal">
    <div class="sheet">
      <p v-if="loading" class="eyebrow" role="status">Loading the roadmap…</p>
      <template v-else-if="missing">
        <p class="eyebrow">Product portal</p>
        <h1>This portal is not available</h1>
        <p class="lead">The address may be wrong, or this workspace has not published a portal.</p>
      </template>
      <template v-else-if="error">
        <p class="eyebrow">Product portal</p>
        <h1>The roadmap could not be loaded</h1>
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
        </nav>
        <p class="eyebrow">Product portal</p>
        <h1>What's coming</h1>
        <div v-if="hasGerman" class="lang" role="group" aria-label="Language">
          <button type="button" :aria-pressed="lang === 'en'" @click="setLang('en')">English</button>
          <button type="button" lang="de" :aria-pressed="lang === 'de'" @click="setLang('de')">Deutsch</button>
        </div>
        <p v-if="!groups.length" class="quiet">Nothing public on the roadmap yet.</p>
        <div v-else class="groups">
          <section v-for="group in groups" :key="group.target" class="group">
            <h2>{{ targetLabel(group.target) }}</h2>
            <article v-for="(item, index) in group.items" :key="index" class="item">
              <p v-if="line(item, 'pill').value" class="pill" :lang="line(item, 'pill').de ? 'de' : undefined">{{ line(item, 'pill').value }}</p>
              <p class="status">{{ statusLabel(item.status) }}</p>
              <p v-if="line(item, 'benefit').value" class="benefit" :lang="line(item, 'benefit').de ? 'de' : undefined">{{ line(item, 'benefit').value }}</p>
            </article>
          </section>
        </div>
        <footer class="colophon">
          <a class="colophon-link" :href="`/portal/${tenantSlug}/llms.txt`">llms.txt</a>
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
.groups { margin-top: 22px; display: grid; gap: 22px; }
.group { min-width: 0; }
.group h2 {
  margin: 0 0 8px;
  font: 650 22px/1.25 var(--serif);
  letter-spacing: -0.02em;
}
.item { margin-top: 14px; min-width: 0; }
.item:first-of-type { margin-top: 0; }
.pill { margin: 0; font-weight: 650; line-height: 1.35; overflow-wrap: anywhere; }
.status { margin: 4px 0 0; color: var(--ink-3); font-size: 13px; line-height: 1.4; }
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
