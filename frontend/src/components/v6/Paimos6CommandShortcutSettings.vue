<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import { ApiError, permissionsEpoch, permissionsEpochGeneration } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import {
  commandShortcutLabel,
  loadCommandPaletteSettings,
  saveInstanceCommandShortcut,
  saveUserCommandShortcut,
  validateCommandShortcut,
  type CommandPaletteSettingsWire,
} from '@/v6/commandPalette'

const auth = useAuthStore()
const settings = ref<CommandPaletteSettingsWire | null>(null)
const state = ref<'loading' | 'ready' | 'unavailable'>('loading')
const userValue = ref('')
const instanceValue = ref('')
const message = ref('')
const saving = ref(false)
let version = 0
let controller: AbortController | null = null
const authorityKey = computed(() => JSON.stringify([
  permissionsEpochGeneration.value,
  permissionsEpoch.value,
  auth.user?.id ?? null,
  auth.user?.role ?? null,
  auth.user?.status ?? null,
]))
const effectiveLabel = computed(() => settings.value ? commandShortcutLabel(settings.value.effective_shortcut) : 'Unavailable')

function install(result: CommandPaletteSettingsWire) {
  settings.value = result
  userValue.value = result.user_shortcut ?? result.effective_shortcut
  instanceValue.value = result.instance_shortcut ?? result.default_shortcut
  state.value = 'ready'
}

async function load() {
  const key = authorityKey.value
  const requestVersion = ++version
  controller?.abort()
  controller = new AbortController()
  settings.value = null
  state.value = 'loading'
  message.value = ''
  try {
    const result = await loadCommandPaletteSettings(controller.signal)
    if (requestVersion !== version || key !== authorityKey.value || controller.signal.aborted) return
    install(result)
  } catch {
    if (requestVersion !== version || key !== authorityKey.value || controller.signal.aborted) return
    state.value = 'unavailable'
  }
}

function validationMessage(value: string): string | null {
  const validation = validateCommandShortcut(value)
  if (validation === 'shortcut_collision') return 'Shortcut collides with a reserved browser or shell command.'
  if (validation === 'invalid_shortcut') return 'Use canonical chord syntax such as Mod+KeyK.'
  return null
}

function saveError(cause: unknown): string {
  const code = cause instanceof ApiError ? cause.error ?? cause.code : null
  return code === 'shortcut_collision'
    ? 'Shortcut collides with a reserved browser or shell command.'
    : code === 'invalid_shortcut' ? 'The server rejected this shortcut.' : 'Shortcut settings unavailable.'
}

async function saveUser(reset = false) {
  const candidate = reset ? null : userValue.value.trim()
  const invalid = candidate === null ? null : validationMessage(candidate)
  if (invalid) { message.value = invalid; return }
  saving.value = true
  message.value = ''
  const key = authorityKey.value
  const requestVersion = version
  try {
    const result = await saveUserCommandShortcut(candidate)
    if (key !== authorityKey.value || requestVersion !== version) return
    install(result)
    message.value = reset ? 'User override reset to the inherited shortcut.' : 'User command shortcut saved.'
  } catch (cause) {
    if (key !== authorityKey.value || requestVersion !== version) return
    message.value = saveError(cause)
  } finally { saving.value = false }
}

async function saveInstance(reset = false) {
  const candidate = reset ? null : instanceValue.value.trim()
  const invalid = candidate === null ? null : validationMessage(candidate)
  if (invalid) { message.value = invalid; return }
  saving.value = true
  message.value = ''
  const key = authorityKey.value
  const requestVersion = version
  try {
    const result = await saveInstanceCommandShortcut(candidate)
    if (key !== authorityKey.value || requestVersion !== version) return
    install(result)
    message.value = reset ? 'Instance override reset to the safe default.' : 'Instance command shortcut saved.'
  } catch (cause) {
    if (key !== authorityKey.value || requestVersion !== version) return
    message.value = saveError(cause)
  } finally { saving.value = false }
}

watch(authorityKey, () => void load(), { immediate: true, flush: 'sync' })
</script>

<template>
  <div class="p6-command-settings">
    <div>
      <strong>Paimos 6 command palette</strong>
      <p>Effective shortcut: <kbd>{{ effectiveLabel }}</kbd><template v-if="settings"> · {{ settings.source }}</template>. Stored on the server; never in this browser or URL.</p>
    </div>
    <p v-if="state === 'loading'" role="status">Loading command shortcut settings…</p>
    <p v-else-if="state === 'unavailable'" role="alert">Command shortcut settings unavailable.</p>
    <template v-else>
      <label>User override <input v-model="userValue" type="text" autocomplete="off" spellcheck="false" placeholder="Mod+KeyK" /></label>
      <div class="p6-command-setting-actions">
        <button type="button" class="btn btn-sm" :disabled="saving" @click="saveUser(false)">Save override</button>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="saving || settings?.user_shortcut === null" @click="saveUser(true)">Reset user</button>
      </div>
      <template v-if="auth.isAdmin">
        <label>Instance override <input v-model="instanceValue" type="text" autocomplete="off" spellcheck="false" placeholder="Mod+KeyK" /></label>
        <div class="p6-command-setting-actions">
          <button type="button" class="btn btn-sm" :disabled="saving" @click="saveInstance(false)">Save instance override</button>
          <button type="button" class="btn btn-ghost btn-sm" :disabled="saving || settings?.instance_shortcut === null" @click="saveInstance(true)">Reset instance</button>
        </div>
      </template>
    </template>
    <p class="p6-command-setting-message" role="status" aria-live="polite">{{ message }}</p>
  </div>
</template>

<style scoped>
.p6-command-settings { display: grid; grid-template-columns: minmax(180px, 1fr) minmax(180px, 1fr) auto; align-items: end; gap: 10px 14px; width: 100%; padding: 14px; border: 1px solid var(--border); border-radius: var(--radius); background: var(--bg-card); }
.p6-command-settings strong { font-size: 13px; }
.p6-command-settings p { margin-top: 3px; color: var(--text-muted); font-size: 11px; }
.p6-command-settings label { display: grid; gap: 4px; color: var(--text-muted); font-size: 11px; font-weight: 600; }
.p6-command-settings input { min-height: 40px; }
.p6-command-setting-actions { display: flex; gap: 6px; }
.p6-command-setting-message { grid-column: 1 / -1; min-height: 18px; }
@media (max-width: 680px) {
  .p6-command-settings { grid-template-columns: 1fr; }
  .p6-command-setting-message { grid-column: 1; }
  .p6-command-setting-actions button { min-height: 44px; }
}
</style>
