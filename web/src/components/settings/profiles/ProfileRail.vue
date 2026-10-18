<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import type { QuoteProfile } from '../../../lib/quotes/profile'
import AppIcon from '../../AppIcon.vue'
import ProfileThumb from './ProfileThumb.vue'

// Every document profile of the workspace: the ones in use, each with a small
// sheet in its colours, the default for new quotes marked; archived ones fold
// away below and can be restored.
const props = defineProps<{ profiles: QuoteProfile[]; selectedId: string; defaultId: string; creating: boolean; loading?: boolean }>()
const emit = defineEmits<{ select: [id: string]; create: []; restore: [profile: QuoteProfile] }>()
const live = computed(() => props.profiles.filter(p => !p.archived))
const archived = computed(() => props.profiles.filter(p => p.archived))
const showArchived = ref(false)
const variant = (p: QuoteProfile) => p.definition.layout_variant === 'classic-v1' ? 'Classic' : 'Standard'
const locale = (p: QuoteProfile) => p.definition.locale === 'en' ? 'English' : 'Deutsch'
</script>

<template>
  <nav class="rail" aria-label="Document profiles">
    <div class="rail-head">
      <h2>Profiles</h2>
      <button type="button" class="btn sm" :aria-pressed="creating" @click="emit('create')"><AppIcon name="plus" :size="13" />New</button>
    </div>
    <div v-if="loading && !profiles.length" class="rail-skeleton" aria-hidden="true"><span v-for="i in 3" :key="i" class="skeleton" /></div>
    <ul v-else class="rail-list">
      <li v-if="creating">
        <span class="item current new" aria-current="page"><span class="new-sheet" aria-hidden="true"><AppIcon name="plus" :size="12" /></span><span class="item-text"><span class="item-name">New profile</span><span class="item-meta">Not saved yet</span></span></span>
      </li>
      <li v-for="p in live" :key="p.id">
        <RouterLink class="item" :to="`/settings/business/profiles/${p.id}`" :aria-current="!creating && p.id === selectedId ? 'page' : undefined" @click="emit('select', p.id)">
          <ProfileThumb :definition="p.definition" :size="34" />
          <span class="item-text">
            <span v-clip-tip="p.name" class="item-name">{{ p.name }}</span>
            <span class="item-meta"><span v-if="p.id === defaultId" class="default" data-tip="New quotes start with this profile">Default · </span>{{ variant(p) }} · {{ locale(p) }} · rev. {{ p.revision }}</span>
          </span>
        </RouterLink>
      </li>
      <li v-if="!live.length && !creating" class="rail-empty">No profiles yet. Quotes print in the standard look until you make one.</li>
    </ul>
    <div v-if="archived.length" class="rail-archived">
      <button type="button" class="btn sm ghost fold" :aria-expanded="showArchived" @click="showArchived = !showArchived">
        <AppIcon name="chevron-right" :size="12" class="fold-chev" />Archived <span class="count">{{ archived.length }}</span>
      </button>
      <ul v-if="showArchived" class="rail-list">
        <li v-for="p in archived" :key="p.id" class="archived-row">
          <RouterLink class="item" :to="`/settings/business/profiles/${p.id}`" :aria-current="p.id === selectedId ? 'page' : undefined" @click="emit('select', p.id)">
            <ProfileThumb :definition="p.definition" :size="34" />
            <span class="item-text"><span v-clip-tip="p.name" class="item-name">{{ p.name }}</span><span class="item-meta">Archived · rev. {{ p.revision }}</span></span>
          </RouterLink>
          <button type="button" class="icon-btn sm flat" :aria-label="`Restore ${p.name}`" data-tip="Restore" @click="emit('restore', p)"><AppIcon name="rollback" :size="14" /></button>
        </li>
      </ul>
    </div>
  </nav>
</template>

<style scoped>
.rail { display: flex; flex-direction: column; gap: 8px; min-height: 0; padding: 14px 10px; overflow: auto; border-right: 1px solid var(--line-2); background: var(--surface-raised-2); }
.rail-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 0 6px 4px; }
.rail-head h2 { font: 500 10.5px/1.4 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.rail-list { display: grid; gap: 2px; margin: 0; padding: 0; list-style: none; }
.item { display: flex; align-items: center; gap: 10px; min-height: 52px; padding: 8px; border-radius: 10px; color: var(--ink); text-decoration: none; }
@media (hover: hover) { a.item:hover { background: var(--row-hover); } }
.item[aria-current="page"] { background: var(--row-selected); }
.item:focus-visible { box-shadow: var(--focus-ring); }
.item-text { display: grid; flex: 1; min-width: 0; }
.item-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13.5px; font-weight: 600; }
.item-meta { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--ink-2); }
.new-sheet { display: grid; place-items: center; width: 24px; height: 34px; border-radius: 3px; outline: 1.5px dashed var(--line-2); outline-offset: -1.5px; color: var(--ink-3); flex-shrink: 0; }
.default { color: var(--teal-ink); font-weight: 600; }
.rail-empty { padding: 8px 6px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.rail-skeleton { display: grid; gap: 6px; padding: 4px; }
.rail-skeleton .skeleton { height: 44px; border-radius: 10px; }
.rail-archived { margin-top: 6px; padding-top: 8px; border-top: 1px solid var(--line); }
.fold { gap: 6px; color: var(--ink-2); }
.fold-chev { color: var(--ink-3); transition: transform .15s ease; }
.fold[aria-expanded="true"] .fold-chev { transform: rotate(90deg); }
.count { font: 600 11px/1 var(--mono); color: var(--ink-3); }
.archived-row { display: flex; align-items: center; gap: 2px; }
.archived-row .item { flex: 1; min-width: 0; }
.archived-row .item-name { color: var(--ink-2); }
@media (max-width: 720px) {
  .item { height: 76px; }
  .item-name { height: 2.8em; line-height: 1.4; white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
}
@media (prefers-reduced-motion: reduce) { .fold-chev { transition: none; } }
</style>
