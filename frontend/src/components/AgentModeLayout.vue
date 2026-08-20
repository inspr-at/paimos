<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — reduced application shell for Agent Mode.

  Selected by App.vue from route meta (`shell: 'agent'`, see
  router/shell.ts). Compared with AppLayout it keeps ONLY:
    - a narrow logo rail (top-left anchor) with an explicit way back to
      the standard app, logout at the bottom-left,
    - settings at the top-right,
    - the auth / session / impersonation / security banners,
    - the header teleport targets the view fills (title, live chip,
      detail lever).
  No project / customer / issue / reporting navigation, no New Issue,
  no timer, no undo, no search. The left conversation column belongs to
  the Agent Mode view itself — it is a surface, not navigation.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import AppIcon from '@/components/AppIcon.vue'
import AppDevLoginBanner from '@/components/AppDevLoginBanner.vue'
import AppImpersonationBanner from '@/components/AppImpersonationBanner.vue'
import SessionExpiredModal from '@/components/SessionExpiredModal.vue'
import { instanceLabel, loadInstance } from '@/api/instance'
import { useBranding } from '@/composables/useBranding'
import { useSidebarColors } from '@/composables/useSidebarColors'
import { useTotpNag } from '@/composables/useTotpNag'
import { useAuthStore } from '@/stores/auth'
import { userInitials } from '@/utils/userDisplay'

const auth = useAuthStore()
const router = useRouter()
const { t } = useI18n()
const { branding } = useBranding()
const { bgColor } = useSidebarColors()
const { show2FAWarning } = useTotpNag()

// Instance banner (STAGING …) + feature flags — same shared module state
// AppLayout loads, so the environment label is never hidden by the shell.
loadInstance()

const displayName = computed(
  () => auth.user?.nickname || auth.user?.first_name || auth.user?.username || '',
)
const signedInAs = computed(() => t('agentMode.shell.signedInAs', { name: displayName.value }))
const railStyle = computed(() => ({ backgroundColor: bgColor.value }))

function goTo2FASetup() {
  router.push('/settings?tab=account#two-factor-authentication')
}

function logout() {
  void auth.logout()
}
</script>

<template>
  <div class="aml-shell" data-shell="agent">
    <AppDevLoginBanner />
    <AppImpersonationBanner />
    <SessionExpiredModal />

    <div class="aml-layout">
      <aside class="aml-rail" :style="railStyle" :aria-label="t('agentMode.shell.controls')">
        <div class="aml-rail-top">
          <div v-if="instanceLabel" class="aml-instance">{{ instanceLabel }}</div>
          <RouterLink to="/" class="aml-brand" :title="t('agentMode.shell.home')">
            <img :src="branding.logo" :alt="branding.company" class="aml-brand-logo" />
          </RouterLink>
          <RouterLink to="/" class="aml-rail-btn aml-exit" :title="t('agentMode.shell.backToApp')">
            <AppIcon name="arrow-left" :size="15" aria-hidden="true" />
            <span class="aml-rail-caption">{{ t('agentMode.shell.exit') }}</span>
            <span class="aml-sr-only">{{ t('agentMode.shell.backToApp') }}</span>
          </RouterLink>
        </div>

        <div class="aml-rail-bottom">
          <RouterLink to="/settings?tab=account" class="aml-avatar" :title="signedInAs" :aria-label="signedInAs">
            <img v-if="auth.user?.avatar_path" :src="auth.user.avatar_path" class="aml-avatar-img" alt="" />
            <span v-else aria-hidden="true">{{ userInitials(auth.user) }}</span>
          </RouterLink>
          <button type="button" class="aml-rail-btn aml-logout" :title="t('agentMode.shell.logout')" :aria-label="t('agentMode.shell.logout')" @click="logout">
            <AppIcon name="log-out" :size="15" aria-hidden="true" />
          </button>
        </div>
      </aside>

      <div class="aml-main">
        <header class="aml-top">
          <div id="app-header-left" class="aml-top-left"></div>
          <div class="aml-top-right">
            <div id="app-header-right" class="aml-top-slot"></div>
            <RouterLink to="/settings" class="aml-rail-btn aml-settings" :title="t('agentMode.shell.settings')" :aria-label="t('agentMode.shell.settings')">
              <AppIcon name="settings" :size="15" aria-hidden="true" />
            </RouterLink>
          </div>
        </header>

        <div v-if="show2FAWarning" class="aml-totp-warning" role="alert">
          <span class="aml-totp-pulse" aria-hidden="true"></span>
          <span>
            {{ t('agentMode.shell.totpWarning') }}
            <button class="aml-totp-link" type="button" @click="goTo2FASetup">{{ t('agentMode.shell.totpAction') }}</button>
            {{ t('agentMode.shell.totpSuffix') }}
          </span>
        </div>

        <div class="aml-content">
          <slot />
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.aml-sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

.aml-shell {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  background: var(--bg);
  color: var(--text);
}
.aml-layout {
  --aml-rail-width: 52px;
  display: grid;
  flex: 1;
  min-height: 0;
  grid-template-columns: var(--aml-rail-width) minmax(0, 1fr);
  height: 100vh;
}

/* ── Rail: logo anchor top-left, logout bottom-left ─────────────────── */
.aml-rail {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: space-between;
  padding: 14px 6px 12px;
  color: var(--sidebar-text, #e4e4e7);
  overflow: hidden;
}
.aml-rail-top,
.aml-rail-bottom {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
}
.aml-instance {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  z-index: 1;
  padding: 0.1rem 0.2rem;
  background: #dc2626;
  color: #fff;
  font-size: 8px;
  font-weight: 700;
  letter-spacing: 0.05em;
  text-align: center;
  text-transform: uppercase;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  opacity: 0.85;
}
.aml-brand {
  display: grid;
  width: 34px;
  height: 34px;
  place-items: center;
  border: 1px solid rgba(255, 255, 255, 0.18);
  border-radius: 10px;
  text-decoration: none;
}
.aml-brand-logo { width: 22px; height: 22px; object-fit: contain; }
.aml-rail-btn {
  display: inline-flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  min-width: 34px;
  padding: 6px 4px;
  border: 0;
  border-radius: 9px;
  background: transparent;
  color: inherit;
  font: inherit;
  text-decoration: none;
  cursor: pointer;
  opacity: 0.82;
  transition: background 0.15s, opacity 0.15s;
}
.aml-rail-btn:hover { background: rgba(255, 255, 255, 0.08); opacity: 1; }
.aml-rail-caption { font-size: 9.5px; font-weight: 600; letter-spacing: 0.04em; text-transform: uppercase; }
.aml-avatar {
  display: grid;
  width: 30px;
  height: 30px;
  place-items: center;
  overflow: hidden;
  border-radius: 50%;
  background: var(--brand-blue);
  color: #fff;
  font-size: 11px;
  font-weight: 700;
  text-decoration: none;
  box-shadow: 0 0 0 1px rgba(0, 0, 0, 0.35);
}
.aml-avatar-img { width: 100%; height: 100%; object-fit: cover; display: block; }
.aml-rail :is(.aml-brand, .aml-rail-btn, .aml-avatar):focus-visible {
  outline: 2px solid #fff;
  outline-offset: 2px;
}

/* ── Main column: slim top bar (view title · live chip · lever · settings) ── */
.aml-main {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  height: 100vh;
  overflow: hidden;
}
.aml-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  height: 48px;
  padding: 0 0.75rem 0 1.25rem;
  border-bottom: 1px solid var(--border);
  background: var(--bg-card);
  flex-shrink: 0;
}
.aml-top-left {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
}
.aml-top-right {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  min-width: 0;
  flex-shrink: 0;
}
.aml-top-slot { display: flex; align-items: center; gap: 0.75rem; min-width: 0; }
.aml-settings {
  flex-direction: row;
  color: var(--text-muted);
  opacity: 1;
}
.aml-settings:hover { background: color-mix(in srgb, var(--text) 8%, transparent); color: var(--text); }
.aml-settings:focus-visible { outline: 2px solid var(--text); outline-offset: 2px; }

.aml-content {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

/* ── 2FA security nag — same copy and look as AppLayout ────────────── */
.aml-totp-warning {
  display: flex;
  align-items: center;
  gap: 0.65rem;
  margin: 0;
  padding: 0.6rem 1.25rem;
  background: #fde8e8;
  border-bottom: 1px solid #f5c6cb;
  font-size: 13px;
  color: #7b1a22;
}
.aml-totp-pulse {
  flex-shrink: 0;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: #c0392b;
}
.aml-totp-link {
  background: none;
  border: none;
  padding: 0;
  margin: 0;
  font: inherit;
  color: #7b1a22;
  font-weight: 600;
  text-decoration: underline;
  cursor: pointer;
}
.aml-totp-link:hover { color: #c0392b; }
</style>
