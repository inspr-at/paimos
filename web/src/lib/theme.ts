// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import { readPreference, writePreference } from './preferences.ts'

// Light, Dark, or System (follow the OS). A signed-in person's choice is a server
// preference (key "theme"), so it survives reloads and follows them across devices;
// the colour-token first-paint cache is separate from this mode preference.
export type ThemeChoice = 'light' | 'dark' | 'system'
const preference = window.matchMedia('(prefers-color-scheme: dark)')
export const themeChoice = ref<ThemeChoice>('system')
export const dark = ref(preference.matches)
preference.addEventListener('change', (event) => {
  if (themeChoice.value === 'system') dark.value = event.matches
})
let modeGeneration = 0
const choices: readonly ThemeChoice[] = ['light', 'dark', 'system']
export function setTheme(choice: ThemeChoice, persist = true) {
  modeGeneration++
  themeChoice.value = choice
  if (persist) void writePreference('theme', { choice })
  if (choice === 'system') {
    delete document.documentElement.dataset.theme
    dark.value = preference.matches
  } else {
    document.documentElement.dataset.theme = choice
    dark.value = choice === 'dark'
  }
}
export function toggleTheme(persist = true) { setTheme(dark.value ? 'light' : 'dark', persist) }
// Applies the stored choice after sign-in; an unknown or missing value keeps System.
export async function restoreTheme(current: () => boolean = () => true) {
  const started = modeGeneration
  const stored = await readPreference('theme')
  if (!current() || started !== modeGeneration) return
  const choice = stored?.choice
  setTheme(typeof choice === 'string' && (choices as readonly string[]).includes(choice) ? choice as ThemeChoice : 'system', false)
}
export function resetTheme() {
  modeGeneration++
  themeChoice.value = 'system'
  dark.value = preference.matches
  delete document.documentElement.dataset.theme
}
