<script setup lang="ts">
import { Command, LogOut, Mic, Moon, Sun } from 'lucide-vue-next'
import { computed, nextTick, onScopeDispose, provide, ref, shallowRef } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { permissionsEpoch, permissionsEpochGeneration } from '@/api/client'
import Paimos6CommandPalette, {
  type Paimos6PaletteActivation,
} from '@/components/v6/Paimos6CommandPalette.vue'
import { usePaimos6CommandPalette } from '@/composables/v6/usePaimos6CommandPalette'
import BrandLogo from '@/components/BrandLogo.vue'
import AppDevLoginBanner from '@/components/AppDevLoginBanner.vue'
import AppImpersonationBanner from '@/components/AppImpersonationBanner.vue'
import SessionExpiredModal from '@/components/SessionExpiredModal.vue'
import { useBranding } from '@/composables/useBranding'
import { useTotpNag } from '@/composables/useTotpNag'
import { instanceLabel, instanceHostname, loadInstance } from '@/api/instance'
import '@/components/habitat/habitat.css'
import { useAuthStore } from '@/stores/auth'
import { commandShortcutLabel } from '@/v6/commandPalette'
import { PAIMOS6_COMMAND_CONTEXT_KEY, type Paimos6CommandContext } from '@/v6/commandPaletteContext'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const commandButton = shallowRef<HTMLElement | null>(null)
const commandContext = shallowRef<Paimos6CommandContext | null>(null)
const principalId = computed(() => auth.user?.id ?? null)
const projectId = computed(() => {
  const raw = route.query.project
  if (typeof raw !== 'string' || !/^[1-9]\d*$/.test(raw)) return null
  const parsed = Number(raw)
  return Number.isSafeInteger(parsed) ? parsed : null
})
const authorityKey = computed(() =>
  JSON.stringify([
    globalThis.location?.origin ?? 'unknown-origin',
    permissionsEpochGeneration.value,
    permissionsEpoch.value,
    auth.user?.id ?? null,
    auth.user?.role ?? null,
    auth.user?.status ?? null,
    auth.allProjects,
    [...auth.accessibleProjects.entries()].sort(([left], [right]) => left - right),
  ]),
)
const routeKey = computed(() => route.fullPath)
const palette = usePaimos6CommandPalette({ principalId, authorityKey, projectId, routeKey })
const selectedSessionId = computed(() => commandContext.value?.selectedSessionId.value ?? null)
const shortcutLabel = computed(() =>
  palette.effectiveShortcut.value
    ? commandShortcutLabel(palette.effectiveShortcut.value)
    : palette.settingsState.value === 'loading'
      ? 'Loading…'
      : 'Unavailable',
)

provide(PAIMOS6_COMMAND_CONTEXT_KEY, (context) => {
  commandContext.value = context
})

async function activate(item: Paimos6PaletteActivation) {
  if (item.kind === 'node') {
    palette.announcement.value = 'Node detail is not available in the 6.0 web preview.'
    return
  }
  if (item.kind === 'session') {
    const currentProjectId = projectId.value
    if (currentProjectId === null) return
    palette.close()
    await router
      .replace({
        query: {
          ...route.query,
          view: 'sessions',
          project: String(currentProjectId),
          session: item.row.product_session_id,
        },
      })
      .catch(() => {})
    return
  }
  if (item.kind === 'knowledge') {
    const currentProjectId = projectId.value
    if (currentProjectId === null) return
    palette.close()
    await router
      .push({
        path: `/projects/${currentProjectId}`,
        query: { tab: 'knowledge', memory: item.row.type === 'memory' ? item.row.slug : undefined },
      })
      .catch(() => {})
    return
  }
  if (item.action === 'open_talk') {
    palette.close()
    await nextTick()
    commandContext.value?.openTalk()
  } else if (item.action === 'clear_session' && selectedSessionId.value) {
    commandContext.value?.clearSession()
    palette.close()
  } else if (item.action === 'open_settings') {
    palette.close()
    await router.push('/settings?tab=account').catch(() => {})
  } else if (item.action === 'return_5x') {
    palette.close()
    await router.push('/legacy').catch(() => {})
  }
}

const { branding, brandName } = useBranding()
const { show2FAWarning } = useTotpNag()
void loadInstance()
const displayName = computed(
  () => auth.user?.nickname || auth.user?.first_name || auth.user?.username || 'Account',
)
const view = computed(() =>
  typeof route.query.session === 'string' ? 'sessions' : route.query.view || 'home',
)
const theme = ref<'day' | 'night'>(initialTheme())
function initialTheme(): 'day' | 'night' {
  try {
    const saved = localStorage.getItem('paimos:habitat-theme')
    if (saved === 'day' || saved === 'night') return saved
  } catch {
    /* Storage may be disabled. */
  }
  return globalThis.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'night' : 'day'
}
function toggleTheme() {
  theme.value = theme.value === 'day' ? 'night' : 'day'
  try {
    localStorage.setItem('paimos:habitat-theme', theme.value)
  } catch {
    /* Memory-only theme remains usable. */
  }
}
function openTalk() {
  if (commandContext.value) commandContext.value.openTalk()
  else void router.replace({ query: { ...route.query, view: 'sessions' } })
}
onScopeDispose(() => {
  commandContext.value = null
})
</script>

<template>
  <div class="p6-shell habitat-shell" data-shell="v6" :data-theme="theme">
    <AppDevLoginBanner />
    <AppImpersonationBanner />
    <SessionExpiredModal />
    <a class="habitat-skip" href="#habitat-main">Skip to content</a>
    <header class="habitat-header">
      <a class="habitat-brand" href="/" :aria-label="brandName + ' home'">
        <BrandLogo :src="branding.logo" :alt="brandName" />
        <span
          ><strong>{{ brandName }}</strong
          ><small>Agent Intercom</small></span
        >
      </a>
      <nav class="habitat-nav" aria-label="Control room">
        <RouterLink
          v-for="item in [
            ['home', 'Home'],
            ['workers', 'Workers'],
            ['projects', 'Projects'],
            ['assign', 'Assign / start'],
          ]"
          :key="item[0]"
          :to="{ path: '/', query: { ...route.query, view: item[0], session: undefined } }"
          :aria-current="view === item[0] ? 'page' : undefined"
          >{{ item[1] }}</RouterLink
        >
      </nav>
      <div class="habitat-header-tools">
        <button type="button" class="habitat-voice" @click="openTalk">
          <Mic :size="16" aria-hidden="true" /><span>Voice</span>
        </button>
        <button
          ref="commandButton"
          type="button"
          class="p6-command-mount"
          :aria-label="`Open command palette (${shortcutLabel})`"
          @click="palette.show"
        >
          <Command :size="15" aria-hidden="true" /><kbd>{{ shortcutLabel }}</kbd>
        </button>
        <button
          type="button"
          :aria-label="theme === 'day' ? 'Switch to dark mode' : 'Switch to bright mode'"
          @click="toggleTheme"
        >
          <Moon v-if="theme === 'day'" :size="16" aria-hidden="true" /><Sun
            v-else
            :size="16"
            aria-hidden="true"
          />
        </button>
        <RouterLink
          class="habitat-account"
          to="/settings?tab=account"
          :aria-label="`Signed in as ${displayName}. Open settings`"
          >{{ displayName }}</RouterLink
        >
        <button type="button" aria-label="Log out" @click="auth.logout()">
          <LogOut :size="16" aria-hidden="true" />
        </button>
      </div>
    </header>
    <div class="habitat-source">
      <span>{{ instanceHostname || 'Instance identity unavailable' }}</span
      ><span>Authenticated browser session</span>
    </div>
    <div v-if="instanceLabel" class="habitat-instance" role="status">{{ instanceLabel }}</div>
    <div v-if="show2FAWarning" class="habitat-security" role="alert">
      Protect your account with two-factor authentication.
      <RouterLink to="/settings?tab=account#two-factor-authentication"
        >Set up two-factor authentication</RouterLink
      >
    </div>
    <div class="p6-shell-content"><slot /></div>
    <footer class="habitat-footer">
      <span>Agent Intercom · {{ brandName }}</span>
      <RouterLink :to="{ path: '/', query: { ...route.query, view: 'sessions' } }"
        >Product sessions</RouterLink
      >
      <RouterLink to="/legacy">Classic workspace</RouterLink>
      <RouterLink to="/settings">Settings</RouterLink>
    </footer>
    <Paimos6CommandPalette
      :open="palette.open.value"
      :query="palette.query.value"
      :search="palette.search.value"
      :search-state="palette.searchState.value"
      :settings-state="palette.settingsState.value"
      :shortcut-label="shortcutLabel"
      :shortcut-source="palette.settings.value?.source ?? null"
      :selected-session-id="selectedSessionId"
      :announcement="palette.announcement.value"
      :return-focus="commandButton"
      @update:query="palette.query.value = $event"
      @close="palette.close"
      @activate="activate"
    />
  </div>
</template>
