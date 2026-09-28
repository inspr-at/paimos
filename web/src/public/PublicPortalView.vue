<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppIcon from '../components/AppIcon.vue'
import { setPageTitle } from '../lib/brand'
import { resilientFetch } from '../lib/api'

// A public catalog for one tenant. The page asks only for that portal, keeps
// the ballot cookie on a vote, and never renders a field the server did not name.
const props = defineProps<{ tenantSlug: string }>()

interface PortalFeature {
  key: string
  title: string
  summary: string
  status: 'idea' | 'reviewed' | 'planned' | 'in_progress' | 'live' | 'declined' | string
  live_since?: string
  legal_basis?: string
  decline_reason?: string
}
interface PortalWish {
  key: string
  title: string
  summary: string
  votes: number
}
interface PortalDocument {
  product: { key: string; title: string; summary: string } | null
  catalog: PortalFeature[]
  wishes: PortalWish[]
}

const statusLabel: Record<string, string> = {
  idea: 'Idea',
  reviewed: 'Reviewed',
  planned: 'Planned',
  in_progress: 'In progress',
  live: 'Live',
  declined: 'Declined',
}

const loading = ref(true)
const missing = ref(false)
const error = ref('')
const voteError = ref('')
const doc = ref<PortalDocument | null>(null)
const voted = ref<Record<string, boolean>>({})
const busy = ref('')

const base = computed(() => `/api/public/portal/${encodeURIComponent(props.tenantSlug)}`)

function votesLabel(count: number) {
  return `${count} ${count === 1 ? 'vote' : 'votes'}`
}

async function load() {
  loading.value = true
  error.value = ''
  missing.value = false
  voteError.value = ''
  voted.value = {}
  try {
    const response = await resilientFetch(base.value, { credentials: 'omit', cache: 'no-store', referrerPolicy: 'no-referrer' })
    if (response.status === 404) {
      missing.value = true
      doc.value = null
      setPageTitle('Portal unavailable')
      return
    }
    if (!response.ok) throw new Error('unavailable')
    doc.value = await response.json() as PortalDocument
    setPageTitle(doc.value.product?.title || 'Product portal')
  } catch {
    error.value = 'The portal could not be loaded.'
    doc.value = null
    setPageTitle('Product portal')
  } finally {
    loading.value = false
  }
}

async function vote(wish: PortalWish) {
  if (busy.value || voted.value[wish.key]) return
  busy.value = wish.key
  voteError.value = ''
  try {
    const response = await resilientFetch(`${base.value}/wishes/${encodeURIComponent(wish.key)}/votes`, {
      method: 'POST',
      credentials: 'same-origin',
      cache: 'no-store',
      referrerPolicy: 'no-referrer',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
    })
    if (response.status === 429) throw new Error('Too many votes from this network. Try again in a minute.')
    if (response.status === 404) throw new Error('This wish is not open for votes.')
    if (!response.ok) throw new Error('The vote was not saved.')
    const body = await response.json() as { votes?: number }
    if (typeof body.votes === 'number') wish.votes = body.votes
    voted.value = { ...voted.value, [wish.key]: true }
  } catch (cause) {
    voteError.value = cause instanceof Error ? cause.message : 'The vote was not saved.'
  } finally {
    busy.value = ''
  }
}

watch(() => props.tenantSlug, () => { void load() }, { immediate: true })
</script>

<template>
  <div class="portal">
    <div class="sheet">
      <p v-if="loading" class="eyebrow" role="status">Loading the portal…</p>
      <template v-else-if="missing">
        <p class="eyebrow">Product portal</p>
        <h1>This portal is not available</h1>
        <p class="lead">The address may be wrong, or this workspace has not published a portal.</p>
      </template>
      <template v-else-if="error">
        <p class="eyebrow">Product portal</p>
        <h1>The portal could not be loaded</h1>
        <p class="lead" role="alert">{{ error }}</p>
        <button class="vote" type="button" @click="load"><AppIcon name="refresh" :size="14" />Try again</button>
      </template>
      <template v-else-if="doc && !doc.product">
        <p class="eyebrow">Product portal</p>
        <h1>Nothing published yet</h1>
        <p class="lead">This workspace has opened its portal and has not published a product.</p>
      </template>
      <template v-else-if="doc?.product">
        <p class="eyebrow">Product portal</p>
        <h1>{{ doc.product.title }}</h1>
        <p v-if="doc.product.summary" class="lead">{{ doc.product.summary }}</p>

        <section class="block" aria-labelledby="catalog-heading">
          <h2 id="catalog-heading">Catalog</h2>
          <p v-if="!doc.catalog.length" class="quiet">No public features yet.</p>
          <ul v-else class="catalog">
            <li v-for="item in doc.catalog" :key="item.key" class="card">
              <div class="card-top">
                <h3>{{ item.title }}</h3>
                <p :class="['status', item.status]">{{ statusLabel[item.status] || item.status }}</p>
              </div>
              <p v-if="item.summary" class="summary">{{ item.summary }}</p>
              <p v-if="item.live_since" class="meta"><span>Live since</span> <span class="mono">{{ item.live_since }}</span></p>
              <p v-if="item.legal_basis" class="meta"><span>Legal basis</span> {{ item.legal_basis }}</p>
              <p v-if="item.decline_reason" class="meta">{{ item.decline_reason }}</p>
            </li>
          </ul>
        </section>

        <section class="block" aria-labelledby="wishes-heading">
          <h2 id="wishes-heading">Wishes</h2>
          <p class="quiet">One vote from this browser. No account and no name.</p>
          <p v-if="voteError" class="alert" role="alert">{{ voteError }}</p>
          <p v-if="!doc.wishes.length" class="quiet">No published wishes yet.</p>
          <ul v-else class="wishes">
            <li v-for="wish in doc.wishes" :key="wish.key" class="wish">
              <div class="wish-copy">
                <h3>{{ wish.title }}</h3>
                <p v-if="wish.summary" class="summary">{{ wish.summary }}</p>
                <p class="count">{{ votesLabel(wish.votes) }}</p>
              </div>
              <button
                class="vote"
                type="button"
                :disabled="!!voted[wish.key] || busy === wish.key"
                :aria-label="voted[wish.key] ? `Voted for ${wish.title}` : `Vote for ${wish.title}`"
                @click="vote(wish)"
              >
                <AppIcon :name="voted[wish.key] ? 'check' : 'star'" :size="14" />
                {{ busy === wish.key ? 'Voting…' : voted[wish.key] ? 'Voted' : 'Vote' }}
              </button>
            </li>
          </ul>
        </section>
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
  margin: 0 0 10px;
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
.block { margin-top: 40px; }
h2 { margin: 0 0 8px; font: 650 22px/1.2 var(--serif); letter-spacing: -0.02em; }
.quiet { margin: 0 0 14px; color: var(--ink-2); line-height: 1.45; }
.alert { margin: 0 0 14px; color: var(--danger); }
.catalog, .wishes { list-style: none; margin: 0; padding: 0; }
.catalog { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 260px), 1fr)); gap: 12px; }
.card, .wish {
  min-width: 0;
  border-radius: 16px;
  background: var(--surface-raised);
  box-shadow: inset 0 0 0 1px var(--line), 0 1px 2px rgba(20, 40, 40, 0.04);
}
.card { padding: 18px; }
.card-top, .wish { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px 16px; }
.wish { padding: 16px 16px 16px 18px; margin-top: 12px; align-items: center; }
.wish-copy { min-width: 0; }
h3 { margin: 0; font-size: 17px; line-height: 1.3; font-weight: 650; overflow-wrap: anywhere; }
.summary { margin: 8px 0 0; color: var(--ink-2); font-size: 14.5px; line-height: 1.5; overflow-wrap: anywhere; }
.meta { margin: 10px 0 0; color: var(--ink-2); font-size: 13.5px; line-height: 1.4; overflow-wrap: anywhere; }
.meta span:first-child { color: var(--ink-3); }
.mono { font-family: var(--mono); font-variant-ligatures: none; }
.count { margin: 8px 0 0; font: 600 13px/1.3 var(--mono); font-variant-numeric: tabular-nums; color: var(--ink); }
.status {
  display: inline-flex;
  align-items: center;
  flex: 0 0 auto;
  min-height: 26px;
  padding: 0 10px;
  border-radius: 999px;
  background: var(--chip-bg);
  box-shadow: inset 0 0 0 1px var(--chip-line);
  color: var(--ink-2);
  font: 600 12px/1 var(--font);
}
.status.live { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.status.in_progress { background: var(--aqua-wash); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.status.declined { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }
.vote {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  min-height: 44px;
  padding: 0 16px;
  border: 0;
  border-radius: 999px;
  background: var(--btn-bg);
  box-shadow: var(--shadow-btn);
  color: var(--ink);
  font: 600 14px/1 var(--font);
  cursor: pointer;
}
.vote:disabled { cursor: default; color: var(--ink-2); background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.vote:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (hover: hover) { .vote:not(:disabled):hover { background: var(--btn-bg-hover); } }
@media (max-width: 560px) {
  .sheet { padding: 24px 16px 56px; }
  .card-top, .wish { flex-direction: column; align-items: stretch; }
  .vote { width: 100%; }
}
main:has(> .page-flow > .portal) { background: var(--surface); }
</style>
