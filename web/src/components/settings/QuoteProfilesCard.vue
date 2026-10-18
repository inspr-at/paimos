<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { getQuoteSettings } from '../../lib/settings'
import { listProfiles, type QuoteProfile } from '../../lib/quotes/profile'
import AppIcon from '../AppIcon.vue'
import SettingsCard from './SettingsCard.vue'
import ProfileThumb from './profiles/ProfileThumb.vue'

// Business › Document profiles: how quotes look. The profiles in use at a glance,
// the default for new quotes marked; each opens in the profile editor beside a
// live preview.
const profiles = ref<QuoteProfile[] | null>(null)
const defaultId = ref('')
const error = ref('')
const live = computed(() => (profiles.value ?? []).filter(p => !p.archived))
const archivedCount = computed(() => (profiles.value ?? []).length - live.value.length)
onMounted(async () => {
  try {
    const [list, settings] = await Promise.all([listProfiles(), getQuoteSettings().catch(() => null)])
    profiles.value = list; defaultId.value = settings?.default_profile_id ?? ''
  } catch (e) { error.value = e instanceof Error ? e.message : 'Profiles could not be loaded.'; profiles.value = [] }
})
</script>

<template>
  <SettingsCard title="Document profiles" icon="document" anchor="quote-profiles">
    <template #lead>How your quotes look: fonts, colours, margins, labels and the footer. Issued quotes keep the revision they were made with.</template>
    <template #aside><RouterLink class="btn sm" :to="live.length ? '/settings/business/profiles' : '/settings/business/profiles/new'"><AppIcon :name="live.length ? 'edit' : 'plus'" :size="13" />{{ live.length ? 'Edit profiles' : 'New profile' }}</RouterLink></template>
    <p v-if="error" class="set-note error" role="alert"><AppIcon name="alert" :size="14" /><span>{{ error }}</span></p>
    <div v-else-if="!profiles" class="set-skeleton" aria-hidden="true"><span class="skeleton" /><span class="skeleton" /></div>
    <p v-else-if="!live.length" class="set-note"><AppIcon name="info" :size="14" /><span>No profiles yet: quotes print in the standard look. A profile gives them your company’s type, colours and layout.</span></p>
    <ul v-else class="profile-list">
      <li v-for="p in live" :key="p.id">
        <RouterLink class="profile-row" :to="`/settings/business/profiles/${p.id}`">
          <ProfileThumb :definition="p.definition" :size="40" />
          <span class="row-text">
            <span v-clip-tip="p.name" class="row-name">{{ p.name }}</span>
            <span class="row-meta">{{ p.definition.layout_variant === 'classic-v1' ? 'Classic' : 'Standard' }} layout · {{ p.definition.locale === 'en' ? 'English' : 'Deutsch' }} · revision {{ p.revision }}</span>
          </span>
          <span v-if="p.id === defaultId" class="default-chip">Default</span>
          <AppIcon name="chevron-right" :size="14" class="row-chev" />
        </RouterLink>
      </li>
    </ul>
    <p v-if="archivedCount" class="archived-note">{{ archivedCount }} archived {{ archivedCount === 1 ? 'profile' : 'profiles' }} in the editor.</p>
  </SettingsCard>
</template>

<style scoped>
.profile-list { display: grid; margin: 0; padding: 0; list-style: none; }
.profile-list li + li { border-top: 1px solid var(--line); }
.profile-row { display: flex; align-items: center; gap: 12px; min-height: 60px; padding: 8px 8px; margin: 0 -8px; border-radius: 10px; color: var(--ink); text-decoration: none; }
@media (hover: hover) { .profile-row:hover { background: var(--row-hover); } }
.profile-row:focus-visible { box-shadow: var(--focus-ring); }
.row-text { display: grid; flex: 1; min-width: 0; }
.row-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13.5px; font-weight: 600; }
.row-meta { overflow-wrap: anywhere; font-size: 12.5px; color: var(--ink-2); }
.default-chip { flex-shrink: 0; height: 20px; padding: 0 7px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 10px/20px var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.row-chev { flex-shrink: 0; color: var(--ink-3); }
.archived-note { margin-top: 8px; font-size: 12.5px; color: var(--ink-2); }
@media (max-width: 720px) {
  .row-name { white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
  .profile-row { align-items: flex-start; }
}
</style>
