<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The avatar menu is about me (AEON-312): who I am, my settings, my theme, signing
// out, and a quiet line with the running version. The app and the workspace live
// in the gear menu beside it.
import { computed, nextTick, ref } from 'vue'
import { useRouter } from 'vue-router'
import { accountEmail, accountName } from '../../lib/api'
import { run } from '../../lib/commands'
import { dark, setTheme, themeChoice, toggleTheme, type ThemeChoice } from '../../lib/theme'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'
import VersionDisplay from '../VersionDisplay.vue'
import HeaderMenu from './HeaderMenu.vue'

const session = useSession()
const router = useRouter()
const menu = ref<InstanceType<typeof HeaderMenu>>()
const busy = ref(false)
// On the narrowest phones the header's moon steps aside for the breadcrumb; its
// quick toggle then leads this sheet instead (AEON-312).
const moonHidden = ref(false)
function opened() {
  const moon = document.querySelector<HTMLElement>('.app-header .theme-btn')
  moonHidden.value = !!moon && getComputedStyle(moon).display === 'none'
}
const error = ref('')
const name = computed(() => session.identity ? accountName(session.identity) : '')
const email = computed(() => session.identity ? accountEmail(session.identity) : '')
const themes: { value: ThemeChoice; label: string; icon: 'sun' | 'moon' | 'monitor' }[] = [
  { value: 'light', label: 'Light', icon: 'sun' }, { value: 'dark', label: 'Dark', icon: 'moon' }, { value: 'system', label: 'System', icon: 'monitor' },
]

// Left and right choose a theme; the group is one stop for up and down.
function themeKeys(event: KeyboardEvent) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  const index = themes.findIndex(theme => theme.value === themeChoice.value)
  setTheme(themes[(index + (event.key === 'ArrowRight' ? 1 : -1) + themes.length) % themes.length].value)
  void nextTick(() => document.querySelector<HTMLElement>('#account-menu [role="menuitemradio"][aria-checked="true"]')?.focus())
}
async function personal() { menu.value?.close(); await router.push('/settings/personal') }
function releases() { menu.value?.close(); run({ name: 'releases' }) }
async function signOut() {
  busy.value = true
  error.value = ''
  try {
    await session.signOut()
    menu.value?.close()
    await router.replace('/signin')
  } catch { error.value = 'Sign out didn’t complete. Please try again.' }
  finally { busy.value = false }
}
</script>

<template>
  <HeaderMenu v-if="session.identity" id="account-menu" ref="menu" label="Account" :trigger-label="`Account for ${name}`" class="account" @open="opened" @close="error = ''">
    <template #trigger="{ phone }">
      <Avatar :id="session.identity.principal.id" :name="name" :size="phone ? 36 : 28" />
    </template>
    <div class="who" role="none">
      <Avatar :id="session.identity.principal.id" :name="name" :size="40" class="who-avatar" />
      <div class="who-text">
        <p class="account-name">{{ name }}</p>
        <p v-if="email" class="account-email" :title="email">{{ email }}</p>
        <p class="account-tenant" title="Workspace"><AppIcon name="folder" :size="12" /><span class="sr-only">Workspace: </span>{{ session.identity.tenant.name }}</p>
      </div>
    </div>
    <button v-if="moonHidden" class="hm-item quick-theme" type="button" role="menuitem" tabindex="-1" @click="toggleTheme()">
      <AppIcon :name="dark ? 'sun' : 'moon'" /><span class="hm-text">{{ dark ? 'Switch to light theme' : 'Switch to dark theme' }}</span>
    </button>
    <button class="hm-item" type="button" role="menuitem" tabindex="-1" @click="personal"><AppIcon name="user" /><span class="hm-text">Personal settings</span></button>
    <div class="theme-row" role="group" aria-labelledby="account-theme-label">
      <span id="account-theme-label" class="eyebrow">Theme</span>
      <div class="seg theme-seg" @keydown="themeKeys">
        <button
          v-for="option in themes" :key="option.value" type="button" role="menuitemradio" tabindex="-1" :aria-checked="themeChoice === option.value"
          @click="setTheme(option.value)"
        ><AppIcon :name="option.icon" :size="14" />{{ option.label }}</button>
      </div>
    </div>
    <hr class="hm-sep" role="separator" />
    <button class="hm-item" type="button" role="menuitem" tabindex="-1" :aria-disabled="busy || undefined" @click="!busy && signOut()"><AppIcon name="logout" /><span class="hm-text">{{ busy ? 'Signing out…' : 'Sign out' }}</span></button>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div class="version-line" role="none">
      <VersionDisplay menuitem class="version-copy" />
      <button class="version-link" type="button" role="menuitem" tabindex="-1" @click="releases">Release history</button>
    </div>
  </HeaderMenu>
</template>

<style scoped>
.who { display: flex; align-items: center; gap: 12px; padding: 10px 10px 12px; margin-bottom: 4px; border-bottom: 1px solid var(--line); }
.who-avatar { flex-shrink: 0; }
.who-text { min-width: 0; }
.account-name { color: var(--ink); font-weight: 650; font-size: 14px; overflow-wrap: anywhere; }
.account-email { font-size: 12.5px; color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.account-tenant { display: inline-flex; align-items: center; gap: 5px; margin-top: 4px; padding: 2px 8px 2px 6px; border-radius: 999px; background: var(--code-bg); font-size: 11.5px; color: var(--ink-2); }
/* Theme: its own small block, the three choices side by side. */
.theme-row { display: grid; gap: 6px; padding: 8px 10px 10px; }
.theme-row .eyebrow { margin: 0; }
.theme-seg { display: grid; grid-template-columns: repeat(3, 1fr); }
.theme-seg button { height: 30px; gap: 6px; padding: 0 6px; }
.theme-seg button:focus-visible { box-shadow: var(--focus-ring); }
.error { margin: 6px 10px; font-size: 12.5px; color: var(--danger); }
/* The version: quiet, copyable, with the history one step away. */
.version-line { display: flex; align-items: center; gap: 8px; margin-top: 4px; padding: 0 4px 0 6px; border-top: 1px solid var(--line); }
.version-copy { font-size: 12px; color: var(--ink-2); }
.version-copy :deep([role="menuitem"]) { border-radius: 6px; padding: 0 4px; cursor: copy; }
.version-copy :deep([role="menuitem"]:focus-visible) { outline: none; box-shadow: var(--focus-ring); }
.version-link {
  margin-left: auto; min-height: 44px; padding: 0 8px; border: 0; border-radius: 8px; background: transparent;
  color: var(--teal-ink); font-size: 12.5px; font-weight: 600; white-space: nowrap;
}
@media (hover: hover) { .version-link:hover { background: var(--row-hover); } }
.version-link:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (max-width: 600px), (pointer: coarse) {
  .theme-seg button { height: 44px; font-size: 13.5px; }
}
</style>
