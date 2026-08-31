<!--
  PAI-854 / PAI-867 — isolated Paimos 6 production shell. This is intentionally not the
  production AppLayout or AgentModeLayout: no left rail and no CRUD chrome.
-->
<script setup lang="ts">
import { ArrowLeft, Command, RadioTower } from 'lucide-vue-next'
import { computed, nextTick, provide, shallowRef } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { permissionsEpoch, permissionsEpochGeneration } from '@/api/client'
import Paimos6CommandPalette, { type Paimos6PaletteActivation } from '@/components/v6/Paimos6CommandPalette.vue'
import { usePaimos6CommandPalette } from '@/composables/v6/usePaimos6CommandPalette'
import { useAuthStore } from '@/stores/auth'
import { commandShortcutLabel } from '@/v6/commandPalette'
import {
  PAIMOS6_COMMAND_CONTEXT_KEY,
  type Paimos6CommandContext,
} from '@/v6/commandPaletteContext'

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
const authorityKey = computed(() => JSON.stringify([
  globalThis.location?.origin ?? 'unknown-origin',
  permissionsEpochGeneration.value,
  permissionsEpoch.value,
  auth.user?.id ?? null,
  auth.user?.role ?? null,
  auth.user?.status ?? null,
  auth.allProjects,
  [...auth.accessibleProjects.entries()].sort(([left], [right]) => left - right),
]))
const routeKey = computed(() => route.fullPath)
const palette = usePaimos6CommandPalette({ principalId, authorityKey, projectId, routeKey })
const selectedSessionId = computed(() => commandContext.value?.selectedSessionId.value ?? null)
const shortcutLabel = computed(() => palette.effectiveShortcut.value
  ? commandShortcutLabel(palette.effectiveShortcut.value)
  : palette.settingsState.value === 'loading' ? 'Loading…' : 'Unavailable')

provide(PAIMOS6_COMMAND_CONTEXT_KEY, (context) => { commandContext.value = context })

async function activate(item: Paimos6PaletteActivation) {
  if (item.kind === 'node') {
    palette.announcement.value = 'Node detail is not available in the 6.0 web preview.'
    return
  }
  if (item.kind === 'session') {
    const currentProjectId = projectId.value
    if (currentProjectId === null) return
    palette.close()
    await router.replace({ query: { project: String(currentProjectId), session: item.row.product_session_id } }).catch(() => {})
    return
  }
  if (item.kind === 'knowledge') {
    const currentProjectId = projectId.value
    if (currentProjectId === null) return
    palette.close()
    await router.push({
      path: `/projects/${currentProjectId}`,
      query: { tab: 'knowledge', memory: item.row.type === 'memory' ? item.row.slug : undefined },
    }).catch(() => {})
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
</script>

<template>
  <div class="p6-shell" data-shell="v6">
    <header class="p6-shell-header">
      <a class="p6-back" href="/legacy" aria-label="Open the 5.x dashboard">
        <ArrowLeft :size="16" aria-hidden="true" />
        <span>5.x dashboard</span>
      </a>
      <div class="p6-wordmark" aria-label="Paimos 6.0">
        <span class="p6-mark" aria-hidden="true">P</span>
        <span>Paimos</span>
        <span class="p6-six">6.0</span>
      </div>
      <div class="p6-shell-tools">
        <span class="p6-fixture-chip">
          <RadioTower :size="14" aria-hidden="true" />
          Live · web
        </span>
        <button
          ref="commandButton"
          type="button"
          class="p6-command-mount"
          :aria-label="`Open command palette (${shortcutLabel})`"
          @click="palette.show"
        >
          <span>Commands</span>
          <Command :size="13" aria-hidden="true" />
          <kbd>{{ shortcutLabel }}</kbd>
        </button>
      </div>
    </header>
    <div class="p6-shell-content">
      <slot />
    </div>
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

<style scoped>
.p6-shell {
  --p6-ink: #1d2723;
  --p6-muted: #67726c;
  --p6-line: #dce4df;
  --p6-moss: #2f6b52;
  min-height: 100vh;
  color: var(--p6-ink);
  background:
    radial-gradient(circle at 16% -10%, rgba(205, 225, 213, 0.55), transparent 31rem),
    #f7f8f5;
  font-family: "DM Sans", system-ui, sans-serif;
}

.p6-shell-header {
  position: relative;
  z-index: 4;
  display: grid;
  grid-template-columns: 1fr auto 1fr;
  align-items: center;
  min-height: 66px;
  padding: 0 34px;
  border-bottom: 1px solid rgba(193, 205, 198, 0.72);
  background: rgba(247, 248, 245, 0.82);
  backdrop-filter: blur(18px);
}

.p6-back,
.p6-wordmark,
.p6-shell-tools,
.p6-fixture-chip,
.p6-command-mount {
  display: inline-flex;
  align-items: center;
}

.p6-back {
  justify-self: start;
  gap: 7px;
  min-width: 30px;
  min-height: 30px;
  color: var(--p6-muted);
  font-size: 12px;
  font-weight: 600;
  text-decoration: none;
}

.p6-back:hover { color: var(--p6-ink); }
.p6-back:focus-visible { outline: 3px solid rgba(47, 107, 82, 0.28); outline-offset: 5px; border-radius: 4px; }

.p6-wordmark {
  gap: 9px;
  font-family: "Bricolage Grotesque", "DM Sans", sans-serif;
  font-size: 17px;
  font-weight: 600;
  letter-spacing: -0.025em;
}

.p6-mark {
  display: grid;
  width: 28px;
  height: 28px;
  place-items: center;
  border-radius: 9px;
  color: #f8fbf8;
  background: #223f32;
  font-size: 14px;
}

.p6-six {
  padding-left: 8px;
  border-left: 1px solid var(--p6-line);
  color: var(--p6-muted);
  font-family: "DM Sans", sans-serif;
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.09em;
  text-transform: uppercase;
}

.p6-shell-tools {
  justify-self: end;
  gap: 8px;
}

.p6-fixture-chip,
.p6-command-mount {
  gap: 6px;
  min-height: 30px;
  border: 1px solid var(--p6-line);
  border-radius: 9px;
  color: var(--p6-muted);
  background: rgba(255, 255, 255, 0.64);
  font-size: 10px;
  font-weight: 650;
  letter-spacing: 0.025em;
}

.p6-fixture-chip { padding: 0 10px; }
.p6-command-mount { padding: 0 8px; }
.p6-command-mount { cursor: pointer; }
.p6-command-mount kbd { font: 600 10px/1 "JetBrains Mono", monospace; }
.p6-shell-content { min-height: calc(100vh - 66px); }

@media (max-width: 680px) {
  .p6-shell-header {
    grid-template-columns: auto 1fr auto;
    min-height: 58px;
    padding: 0 14px;
  }
  .p6-back span,
  .p6-fixture-chip { display: none; }
  .p6-back { justify-content: center; }
  .p6-wordmark { justify-self: center; font-size: 15px; gap: 7px; }
  .p6-mark { width: 25px; height: 25px; border-radius: 8px; }
  .p6-six { font-size: 9px; }
  .p6-command-mount { min-width: 44px; min-height: 44px; }
  .p6-shell-content { min-height: calc(100vh - 58px); }
}

@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after { scroll-behavior: auto !important; transition-duration: 0.01ms !important; animation-duration: 0.01ms !important; }
}
</style>
