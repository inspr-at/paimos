<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The avatar menu is about me (AEON-312): who I am, my settings, my theme, signing
// out, and a quiet line with the running version. The app and the workspace live
// in the gear menu beside it. Phones and short screens have no footer bar (AEON-1459):
// the running release is "What's new" here, and the product's name is the menu's foot.
import { computed, nextTick, ref } from 'vue'
import { useRouter } from 'vue-router'
import { accountEmail, accountName } from '../../lib/api'
import { brand } from '../../lib/brand'
import { compactChrome, narrowestPhone } from '../../lib/chrome'
import { codenameOf, releaseAria } from '../../lib/codenames'
import { run } from '../../lib/commands'
import { dark, setTheme, themeChoice, toggleTheme, type ThemeChoice } from '../../lib/theme'
import { useReleases } from '../../stores/releases'
import { useSession } from '../../stores/session'
import { useVersion } from '../../stores/version'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'
import CalendarVersion from '../CalendarVersion.vue'
import VersionDisplay from '../VersionDisplay.vue'
import HeaderMenu from './HeaderMenu.vue'

const session = useSession()
const router = useRouter()
const menu = ref<InstanceType<typeof HeaderMenu>>()
const busy = ref(false)
// On the narrowest phones the header's moon steps aside for the breadcrumb; its
// quick toggle then leads this sheet instead (AEON-312). The Desk preview's
// chevron steps aside first; its entry then follows Personal settings (AEON-1214).
const moonHidden = ref(false), deskHidden = ref(false)
function opened() {
  const hidden = (selector: string) => { const el = document.querySelector<HTMLElement>(selector); return !!el && getComputedStyle(el).display === 'none' }
  moonHidden.value = hidden('.app-header .theme-btn')
  deskHidden.value = hidden('.app-header .desk-more')
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
// Compact: the release the page runs, as the footer named it; it opens at that release.
const compact = compactChrome(), narrowest = narrowestPhone()
const version = useVersion(), history = useReleases()
const running = computed(() => version.value?.version ?? '')
const codename = computed(() => version.value?.codename || codenameOf(running.value))
const whatsNewLabel = computed(() => `What’s new${running.value ? `, ${releaseAria(codename.value, running.value)}` : ''}`)
function whatsNew() { menu.value?.close(); run({ name: 'releases', version: history.current || 'current' }) }
// The narrowest phones have no gear in the header: Settings and help opens from here.
function appMenu() { menu.value?.close(); run({ name: 'app-menu' }) }
// Focus goes back to the avatar first, so Escape in the preview returns there.
function deskPreview() { menu.value?.close(true); run({ name: 'desk-preview' }) }
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
    <button v-if="narrowest" class="hm-item quick-app" type="button" role="menuitem" tabindex="-1" @click="appMenu"><AppIcon name="gear" /><span class="hm-text">Settings and help</span></button>
    <button v-if="deskHidden" class="hm-item quick-desk" type="button" role="menuitem" tabindex="-1" @click="deskPreview">
      <AppIcon name="check" /><span class="hm-text">Preview the Decision Desk</span>
    </button>
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
    <template v-if="compact">
      <button class="hm-item whats-new" type="button" role="menuitem" tabindex="-1" :aria-label="whatsNewLabel" @click="whatsNew">
        <AppIcon name="history" />
        <span class="wn-text"><span class="wn-title">What’s new</span><span v-if="running" class="wn-release" aria-hidden="true"><span v-if="codename" lang="en">{{ codename }}</span><span class="nw"><template v-if="codename">&nbsp;·&nbsp;</template><CalendarVersion :value="running" :scheme="version.value?.scheme" rest /></span></span></span>
      </button>
      <hr class="hm-sep" role="separator" />
      <p class="me-foot" role="none">{{ brand.wordmark }}</p>
    </template>
    <div v-else class="version-line" role="none">
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
/* What's new: its label over the release's name and calendar version; the dot never ends a line. */
.whats-new { min-height: 52px; padding-block: 6px; }
.wn-text { display: grid; gap: 1px; min-width: 0; }
.wn-title { color: var(--ink); }
.wn-release { color: var(--ink-2); font-size: 12px; overflow-wrap: anywhere; }
.nw { white-space: nowrap; }
.me-foot { margin: 0; padding: 6px 10px 4px; font: 600 10px/16px var(--mono); letter-spacing: .24em; color: var(--ink-3); font-variant-ligatures: none; }
</style>
