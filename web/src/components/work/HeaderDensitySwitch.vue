<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useProjectHeader } from '../../lib/useProjectHeader'
import type { HeaderDensity } from '../../lib/projectHeader'
const { headerDensity, setHeaderDensity } = useProjectHeader()
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const choices: { value: HeaderDensity; label: string; path: string }[] = [
  { value: 'comfortable', label: 'Comfortable', path: 'M2.2 2.2h11.6v6.6H2.2zM2.8 11.6h10.4M2.8 14h7' },
  { value: 'compact', label: 'Compact', path: 'M2.2 2.2h11.6V6H2.2zM2.8 8.6h10.4M2.8 11.2h10.4M2.8 13.8h7' },
  { value: 'collapsed', label: 'Collapsed', path: 'M2.2 2.8h11.6M2.8 6.2h10.4M2.8 8.8h10.4M2.8 11.4h10.4M2.8 14h7' },
]
function move(event: KeyboardEvent) {
  if (event.altKey || event.metaKey || event.ctrlKey || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  const buttons = Array.from((event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('button'))
  const index = buttons.indexOf(event.target as HTMLButtonElement)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? 2 : (index + (event.key === 'ArrowRight' ? 1 : 2)) % 3
  event.preventDefault(); buttons[next]?.focus(); buttons[next]?.click()
}
</script>
<template>
  <div class="header-density" role="radiogroup" aria-label="Project header" @keydown="move">
    <span class="header-label" aria-hidden="true">Header</span>
    <span class="density-segment">
      <button v-for="choice in choices" :key="choice.value" type="button" role="radio" :aria-label="`${choice.label} project header`"
        :aria-checked="headerDensity === choice.value" :tabindex="headerDensity === choice.value ? 0 : -1"
        :aria-keyshortcuts="choice.value === 'collapsed' ? mac ? 'Meta+Shift+Period' : 'Control+Shift+Period' : undefined"
        aria-controls="project-header-fold" :data-tip="choice.label + (choice.value === 'collapsed' ? mac ? ' · Command+Shift+.' : ' · Ctrl+Shift+.' : '')"
        @click="setHeaderDensity(choice.value)">
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path :d="choice.path" /></svg>
      </button>
    </span>
  </div>
</template>
<style scoped>
.header-density { display: inline-flex; align-items: center; gap: 8px; flex: none; }
.header-label { font: 500 10.5px/1 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.density-segment { display: inline-flex; gap: 2px; padding: 3px; border-radius: 999px; background: var(--seg-bg); }
button { display: grid; place-items: center; width: 32px; height: 28px; padding: 0; border: 0; border-radius: 999px; background: transparent; color: var(--ink-3); }
button:hover { color: var(--ink); background: var(--row-hover); }
button[aria-checked="true"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: var(--shadow-btn); }
button:focus-visible { box-shadow: var(--focus-ring); }
@media (max-width: 1100px) { .header-label { display: none; } }
@media (pointer: coarse) { button { width: 44px; height: 44px; } }
</style>
