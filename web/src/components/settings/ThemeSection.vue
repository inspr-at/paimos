<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { useThemeEditor } from '../../stores/themeEditor'
import type { ThemeRecord } from '../../lib/themes'
import { derivedDark } from '../../lib/themeColours'
import { toast } from '../../lib/toast'
import SettingsCard from './SettingsCard.vue'
import ThemeColoursCard from './ThemeColoursCard.vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
const editor = useThemeEditor()
const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const { items, active, draft, cursor, busy, error, message, conflict, dirty, valid, selfWrite } = editor
const renaming = ref<string | null>(null), confirming = ref<ThemeRecord | null>(null)
function scope(theme: ThemeRecord) { return theme.id === active.value?.default_theme_id ? 'Workspace default' : theme.scope === 'workspace' ? 'Workspace' : 'Yours' }
function rename(theme: ThemeRecord) {
  if (busy.value || !editor.editable(theme) || draft.value?.id !== theme.id) return
  renaming.value = theme.id
  void nextTick(() => { const input = document.querySelector<HTMLInputElement>('.theme-name-input'); input?.focus(); input?.select() })
}
function keys(event: KeyboardEvent) {
  if (event.key === 'Enter' && (isMac ? event.metaKey : event.ctrlKey)) { event.preventDefault(); void editor.save() }
  if (event.key === 'Escape') {
    if (event.target instanceof HTMLInputElement) { event.preventDefault(); event.target.blur() }
    else { confirming.value = null; renaming.value = null }
  }
}
async function remove() {
  const captured = confirming.value
  if (!captured) return
  await editor.remove(captured)
  if (confirming.value?.id === captured.id && !error.value) confirming.value = null
}
watch(() => active.value?.theme.id, () => { renaming.value = null; confirming.value = null })
</script>
<template>
  <div class="theme-section" @keydown="keys">
    <SettingsCard title="Themes" icon="layers" anchor="themes">
      <template #lead>Choose the theme you work in. Duplicate any theme to make your own; only you see your themes.</template>
      <template #aside><button type="button" class="btn sm" :disabled="busy || dirty || !selfWrite" @click="editor.newTheme"><AppIcon name="plus" :size="13" />New theme</button></template>
      <div class="theme-status" :class="{ error: !!error }"><p :role="error ? 'alert' : 'status'">{{ error || message || (busy ? 'Loading…' : dirty ? 'Save or discard your edits before choosing another theme.' : 'Everyone starts with the workspace default.') }}</p><button type="button" class="text-link" :style="{ visibility: error && !dirty ? 'visible' : 'hidden' }" :disabled="busy || !error || dirty" @click="editor.load">Reload themes</button></div>
      <div class="theme-list" aria-label="Themes list">
        <div v-for="theme in items" :key="theme.id" class="theme-row" :class="{ selected: theme.id === active?.theme.id }" :data-theme-id="theme.id">
          <button type="button" class="theme-choice" :inert="confirming?.id === theme.id" :aria-label="`Use ${theme.name}`" :aria-pressed="theme.id === active?.theme.id" :disabled="busy || dirty || !selfWrite" @click="editor.choose(theme)">
            <span class="theme-dots" aria-hidden="true"><i :style="{ background: theme.values.primary.light }" /><i :style="{ background: theme.values.primary.dark ?? derivedDark(theme.values.primary.light) }" /><i :style="{ background: theme.values.secondary.light }" /></span><span class="choice-indicator" aria-hidden="true"><AppIcon v-if="theme.id === active?.theme.id" name="check" :size="14" /></span>
          </button>
          <div class="theme-details" :inert="confirming?.id === theme.id">
            <input v-if="renaming === theme.id && draft?.id === theme.id" v-model="draft.name" class="theme-name-input" maxlength="80" aria-label="Theme name" :disabled="busy" @blur="renaming = null" />
            <button v-else type="button" class="theme-name" :data-tip="draft?.id === theme.id ? draft.name : theme.name" @click="toast(draft?.id === theme.id ? draft.name : theme.name)">{{ draft?.id === theme.id ? draft.name || 'Untitled theme' : theme.name }}</button>
            <span class="scope">{{ scope(theme) }}{{ theme.scope === 'workspace' ? editor.editable(theme) ? ' · you manage it' : ' · read-only' : '' }}</span>
          </div>
          <div class="row-actions" :inert="confirming?.id === theme.id">
            <button type="button" class="text-link" :aria-label="`Duplicate ${theme.name}`" :disabled="busy || dirty || !selfWrite" @click="editor.duplicate(theme)">Duplicate</button>
            <button v-if="editor.editable(theme)" type="button" class="text-link" :aria-label="`Rename ${theme.name}`" :disabled="busy || draft?.id !== theme.id" @click="rename(theme)">Rename</button>
            <button v-if="editor.editable(theme) && theme.id !== active?.default_theme_id" type="button" class="text-link" :aria-label="`Delete ${theme.name}`" :disabled="busy || dirty" @click="confirming = JSON.parse(JSON.stringify(theme))">Delete</button>
          </div>
          <div v-if="confirming?.id === theme.id" class="delete-confirm" role="group" aria-label="Delete theme confirmation">
            <div class="confirm-actions"><button type="button" class="btn danger" :disabled="busy || dirty" @click="remove">Delete theme</button><button type="button" class="btn" :disabled="busy" @click="confirming = null">Keep theme</button></div>
            <p>Delete <strong>{{ confirming.name }}</strong>? People using it return to the workspace default.</p>
          </div>
        </div>
      </div>
      <button v-if="cursor" type="button" class="btn sm load-more" :disabled="busy" @click="editor.more">Load more themes</button>
    </SettingsCard>
    <ThemeColoursCard v-if="draft" :draft="draft" :editable="editor.editable(draft) && !busy && !conflict" @change="draft = $event" />
    <Teleport to="body">
      <div v-if="dirty && draft" class="theme-savebar" role="region" aria-label="Unsaved theme changes" @keydown="keys">
        <p>Unsaved changes to <strong>{{ draft.name || 'Untitled theme' }}</strong><span v-if="!valid"> · Enter a name of up to 80 characters.</span></p>
        <div><button type="button" class="btn" :disabled="busy" @click="editor.discard">Discard</button><button type="button" class="btn primary" :aria-keyshortcuts="isMac ? 'Meta+Enter' : 'Control+Enter'" :disabled="busy || !valid || !editor.editable(draft) || conflict" @click="editor.save">Save<KeyCap k="mod" /><KeyCap k="enter" /></button></div>
      </div>
    </Teleport>
  </div>
</template>
<style scoped>
.theme-section { display: grid; gap: 16px; container-type: inline-size; padding-bottom: 100px; }
.theme-status { height: 64px; display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 12px; align-items: center; font-size: 12px; color: var(--ink-2); }.theme-status p { max-height: 56px; overflow-y: auto; }.theme-status.error { color: var(--danger); }
.theme-list { max-height: 288px; overflow-y: auto; scrollbar-gutter: stable; border: 1px solid var(--line); border-radius: 12px; background: var(--surface); }
.theme-row { position: relative; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; gap: 12px; align-items: center; height: 96px; padding: 12px; border-bottom: 1px solid var(--line); }.theme-row:last-child { border-bottom: 0; }.theme-row.selected { background: var(--row-selected); }
.theme-choice { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 6px; height: 60px; padding: 0 4px; border: 0; background: transparent; color: var(--teal-ink); }
.theme-dots { display: flex; gap: 3px; }.theme-dots i { width: 12px; height: 12px; border-radius: 50%; box-shadow: inset 0 0 0 1px var(--line-2); }.choice-indicator { height: 14px; }
.theme-details { display: grid; gap: 4px; min-width: 0; }.theme-name { display: block; font-size: 13px; font-weight: 600; line-height: 44px; height: 44px; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; border: 0; background: none; padding: 0; color: var(--ink); text-align: left; }.theme-name-input { width: 100%; min-width: 0; height: 44px; padding: 0 4px; font-size: 13px; }.scope { color: var(--ink-2); font-size: 11px; }
.row-actions { display: flex; gap: 12px; }.text-link { border: 0; background: transparent; color: var(--teal-ink); font-size: 12px; min-height: 44px; padding: 0; text-decoration: underline; text-underline-offset: 3px; }
.delete-confirm { position: absolute; inset: 0; background: var(--surface-raised); padding: 8px 12px; display: flex; flex-direction: column; gap: 4px; }.confirm-actions { display: flex; flex-wrap: wrap; flex-shrink: 0; gap: 8px; }.confirm-actions button { min-height: 44px; }.delete-confirm p { font-size: 12px; overflow-wrap: anywhere; overflow-y: auto; min-height: 0; }.load-more { margin-top: 12px; }
.theme-savebar { position: fixed; z-index: 80; bottom: calc(var(--footer-h) + 12px + env(safe-area-inset-bottom, 0px)); left: 50%; transform: translateX(-50%); width: min(48rem, calc(100vw - 32px)); display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: end; gap: 12px; padding: 12px 16px; border-radius: 14px; background: var(--surface-raised); box-shadow: var(--shadow-pop); }.theme-savebar p { font-size: 13px; color: var(--ink-2); overflow-wrap: anywhere; }.theme-savebar strong { color: var(--ink); }.theme-savebar > div { display: flex; gap: 8px; }.theme-savebar button { min-height: 44px; }
@container (max-width: 640px) { .theme-row { grid-template-columns: auto minmax(0, 1fr); grid-template-rows: 64px 44px; height: 136px; gap: 4px 12px; }.theme-choice { height: 60px; }.row-actions { grid-column: 2; gap: 16px; }.theme-list { max-height: 408px; } }
@media (max-width: 600px) { .theme-savebar { left: 0; bottom: var(--footer-h); transform: none; width: 100%; border-radius: 0; padding-bottom: max(12px, env(safe-area-inset-bottom)); grid-template-columns: minmax(0, 1fr); }.theme-savebar > div { justify-content: flex-end; }.theme-savebar p { max-height: 3em; overflow-y: auto; }.theme-section { padding-bottom: 150px; } }
</style>
