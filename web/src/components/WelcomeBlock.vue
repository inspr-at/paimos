<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { pageGreeting, type Greeting } from '../lib/profile'
import { useProfile } from '../stores/profile'
import { useSession } from '../stores/session'
import AppIcon from './AppIcon.vue'
import Avatar from './Avatar.vue'

// A quiet welcome above the Projects title: your avatar (it opens your profile)
// and today's line, drawn once per page load in your time zone. Its height is
// kept from the start, so the page never moves when the words arrive; they fade in.
const store = useProfile()
const session = useSession()
const greeting = ref<Greeting | null>(null)
const ready = ref(false)
// Unknown until the profile is here: the space is kept; off means no block at all.
const enabled = computed(() => store.profile ? store.profile.greeting_enabled : !store.error)
const person = computed(() => store.profile ? (store.profile.preferred_name || store.profile.first_name || session.identity?.principal.name || '') : session.identity?.principal.name ?? '')
const fullName = computed(() => [store.profile?.first_name, store.profile?.last_name].filter(Boolean).join(' ') || session.identity?.principal.name || '')
onMounted(async () => {
  await store.load()
  if (!store.profile?.greeting_enabled) return
  greeting.value = await pageGreeting()
  ready.value = true
})
const hello = computed(() => greeting.value ? `${greeting.value.salutation}, ${greeting.value.name}` : `Hello, ${person.value.split(' ')[0]}`)
</script>

<template>
  <section v-if="enabled" class="welcome" :class="{ ready }" aria-label="Welcome">
    <RouterLink class="me" to="/settings/personal#profile" :aria-label="`Your profile: ${fullName}`" data-tip="Your profile">
      <Avatar :id="session.identity?.principal.id" :name="fullName" :size="40" />
      <span class="pencil" aria-hidden="true"><AppIcon name="edit" :size="12" /></span>
    </RouterLink>
    <div class="words">
      <p class="hello">{{ ready ? hello : '' }}</p>
      <p class="line">{{ ready && greeting ? greeting.message : '' }}</p>
    </div>
  </section>
</template>

<style scoped>
.welcome { display: flex; align-items: center; gap: 14px; min-height: 48px; margin-bottom: 18px; }
.me { position: relative; display: grid; place-items: center; flex-shrink: 0; width: 44px; height: 44px; border-radius: 50%; }
.me:focus-visible { box-shadow: var(--focus-ring); }
.me :deep(.avatar) { box-shadow: 0 0 0 1px var(--glass-rim), 0 4px 12px -6px color-mix(in srgb, var(--shadow-color) 35%, transparent); }
/* On hover or focus a small pencil says it opens your profile. */
.pencil {
  position: absolute; right: -2px; bottom: -2px; display: grid; place-items: center; width: 20px; height: 20px; border-radius: 50%;
  background: var(--surface-raised); color: var(--teal-ink); box-shadow: 0 0 0 1px var(--line-2), 0 2px 6px -2px color-mix(in srgb, var(--shadow-color) 30%, transparent);
  opacity: 0; transform: scale(.85); transition: opacity .15s ease, transform .15s ease;
}
.me:hover .pencil, .me:focus-visible .pencil { opacity: 1; transform: none; }
@media (hover: none) { .pencil { opacity: 1; transform: none; } }
.words { display: grid; gap: 1px; min-width: 0; opacity: 0; transform: translateY(3px); }
.ready .words { opacity: 1; transform: none; transition: opacity .6s ease, transform .6s cubic-bezier(.2, .75, .25, 1); }
.hello { min-height: 21px; font: 600 15px/1.4 var(--font); color: var(--ink); }
.line { min-height: 19px; font-size: 13.5px; line-height: 1.4; color: var(--ink-2); overflow-wrap: anywhere; }
@media (prefers-reduced-motion: reduce) { .words, .ready .words { transform: none; transition: none; } .pencil { transition: none; } }
@media (max-width: 600px) { .welcome { margin-bottom: 12px; } }
</style>
