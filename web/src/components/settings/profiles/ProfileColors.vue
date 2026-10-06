<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { reactive, useId } from 'vue'
import { COLOR_TOKENS, contrastNote, normalizeHex, ratioText, type ColorKey } from '../../../lib/quotes/profileForm'
import type { QuoteProfileDefinition } from '../../../lib/quotes/types'
import AppIcon from '../../AppIcon.vue'

// The six colour tokens of the document. Each shows on the profile's own paper,
// with its contrast against it: text tokens need 4.5:1 (AA) because quotes print
// small; a warning says so but never blocks saving.
const props = defineProps<{ definition: QuoteProfileDefinition; disabled?: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const id = useId()
const typed = reactive<Record<string, string>>({})
function commit(key: ColorKey, raw: string) {
  const value = normalizeHex(raw)
  if (!value) return
  props.definition.colors[key] = value
  delete typed[key]
  emit('changed')
}
function picked(key: ColorKey, event: Event) { commit(key, (event.target as HTMLInputElement).value) }
const bad = (key: string) => typed[key] !== undefined && typed[key].trim() !== '' && !normalizeHex(typed[key])
</script>

<template>
  <ul class="colors">
    <li v-for="token in COLOR_TOKENS" :key="token.key" class="color">
      <label class="swatch" :style="{ '--swatch': definition.colors[token.key], '--paper': definition.colors.paper }" :data-tip="`Pick the ${token.label.toLowerCase()} colour`">
        <input type="color" class="picker" :value="definition.colors[token.key]" :disabled="disabled" :aria-label="`${token.label} colour`" @change="picked(token.key, $event)" />
        <span v-if="token.text" class="swatch-text" aria-hidden="true">Aa</span>
      </label>
      <div class="color-text">
        <label :for="`${id}-${token.key}`" class="color-label">{{ token.label }}</label>
        <p :id="`${id}-${token.key}-use`" class="color-use">{{ token.use }}</p>
      </div>
      <div class="color-value">
        <input
          :id="`${id}-${token.key}`" class="field hex" :class="{ bad: bad(token.key) }" spellcheck="false" autocomplete="off" maxlength="7" :disabled="disabled"
          :value="typed[token.key] ?? definition.colors[token.key]" :aria-invalid="bad(token.key) || undefined" :aria-describedby="`${id}-${token.key}-use ${id}-${token.key}-note`"
          @input="typed[token.key] = ($event.target as HTMLInputElement).value" @change="commit(token.key, ($event.target as HTMLInputElement).value)" @keydown.enter.prevent="commit(token.key, ($event.target as HTMLInputElement).value)"
        />
        <template v-for="note in [contrastNote(token.key, definition.colors)]" :key="token.key">
          <span v-if="note" :id="`${id}-${token.key}-note`" class="ratio" :class="note.level" :data-tip="note.message || undefined">
            <AppIcon v-if="note.level !== 'ok'" name="alert" :size="12" /><AppIcon v-else-if="token.text" name="check" :size="12" />{{ ratioText(note.ratio) }}
            <span class="sr-only">{{ note.message }}</span>
          </span>
          <span v-else :id="`${id}-${token.key}-note`" class="ratio none">Paper</span>
        </template>
      </div>
      <p v-if="bad(token.key)" class="warn bad-text">Use a colour like #2a7f78.</p>
      <p v-else-if="contrastNote(token.key, definition.colors)?.level === 'low' || contrastNote(token.key, definition.colors)?.level === 'large'" class="warn">{{ contrastNote(token.key, definition.colors)!.message }}</p>
    </li>
  </ul>
</template>

<style scoped>
.colors { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; }
.color { display: grid; grid-template-columns: 40px minmax(0, 1fr) auto; align-items: center; column-gap: 12px; padding: 6px 0; border-top: 1px solid var(--line); }
.color:first-child { border-top: 0; }
.swatch { position: relative; display: grid; place-items: center; width: 40px; height: 40px; border-radius: 10px; background: var(--paper); box-shadow: inset 0 0 0 1px var(--line-2); cursor: pointer; overflow: hidden; }
.swatch::before { content: ''; position: absolute; inset: 6px; border-radius: 6px; background: var(--swatch); }
.swatch:has(.swatch-text)::before { inset: auto 6px 6px auto; width: 10px; height: 10px; border-radius: 50%; box-shadow: 0 0 0 1.5px var(--paper); }
.swatch-text { position: relative; color: var(--swatch); font: 700 16px/1 var(--font); }
.swatch:focus-within { box-shadow: inset 0 0 0 1px var(--line-2), var(--focus-ring); }
.picker { position: absolute; inset: 0; width: 100%; height: 100%; opacity: 0; cursor: pointer; }
.color-text { min-width: 0; }
.color-label { display: block; font-size: 13px; font-weight: 600; color: var(--ink); }
.color-use { margin-top: 1px; font-size: 12px; line-height: 1.4; color: var(--ink-2); }
.color-value { display: flex; align-items: center; gap: 6px; }
.hex { width: 92px; height: 30px; font: 500 12.5px/1 var(--mono); font-variant-ligatures: none; text-transform: lowercase; }
.hex.bad { box-shadow: var(--field-inset), 0 0 0 1px var(--danger-line); }
.ratio { display: inline-flex; align-items: center; justify-content: center; gap: 4px; min-width: 62px; height: 24px; padding: 0 7px; border-radius: 999px; font: 600 11px/1 var(--mono); font-variant-numeric: tabular-nums; font-variant-ligatures: none; background: var(--surface-2); color: var(--ink-2); box-shadow: inset 0 0 0 1px var(--line); }
.ratio.ok svg { color: var(--teal-ink); }
.ratio.large { background: var(--gold-wash); color: var(--warn-ink); box-shadow: inset 0 0 0 1px var(--gold-line, var(--line-2)); }
.ratio.low { background: var(--danger-bg); color: var(--danger); box-shadow: inset 0 0 0 1px var(--danger-line); }
.ratio.none { color: var(--ink-3); }
.warn { grid-column: 2 / -1; margin-top: 4px; font-size: 12px; line-height: 1.4; color: var(--warn-ink); }
.warn.bad-text { color: var(--danger); }
@media (max-width: 520px) {
  .color { grid-template-columns: 40px minmax(0, 1fr); row-gap: 6px; }
  .color-value { grid-column: 2; }
  .warn { grid-column: 1 / -1; }
}
</style>
