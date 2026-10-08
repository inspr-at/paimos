<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import AppIcon from '../../AppIcon.vue'
import { vClipTip } from '../../../directives/clipTip'
import { boardText, type BoardColumn, type BoardLayer } from '../../../lib/modelsBoard'
const props = defineProps<{ column: BoardColumn; layer: BoardLayer; german: boolean; editable: boolean; full: boolean; inherited?: boolean; template?: 'best' | 'balanced' | 'save' | null }>()
defineEmits<{ menu: [event: MouseEvent]; reset: [] }>()
const text = (en: string, de: string) => boardText(props.german, en, de)
const state = computed(() => props.layer === 'rules' ? text(`${props.column.top.length + props.column.bottom.length} pinned, ${props.column.not.length} not allowed`, `${props.column.top.length + props.column.bottom.length} angepinnt, ${props.column.not.length} nicht erlaubt`) : ({ own: props.layer === 'default' ? text('Own order', 'Eigene Reihenfolge') : text('Your order', 'Ihre Reihenfolge'), default: text('Workspace default', 'Vorgabe'), template: props.inherited ? text('Workspace default', 'Vorgabe') : text(`Template · ${{ best: 'Best quality', balanced: 'Balanced', save: 'Save tokens' }[props.template ?? 'balanced']}`, `Vorlage · ${{ best: 'Beste Qualität', balanced: 'Ausgewogen', save: 'Tokens sparen' }[props.template ?? 'balanced']}`), follows: text('Follows Everything else', 'Folgt Alles andere') })[props.column.source])
</script>
<template>
  <header class="bc-head" :class="{ full }">
    <div class="bc-r1"><b v-clip-tip="column.label" class="bc-name" :data-tip="`${column.label}: ${column.sentence}`" tabindex="0">{{ column.short }}</b><button v-if="editable && (layer === 'rules' || !column.fixed)" class="icon-btn sm board-menu" type="button" :aria-label="`${column.label}: ${text('column menu', 'Spaltenmenü')}`" @click="$emit('menu', $event)"><AppIcon name="more" :size="14" /></button></div>
    <p v-clip-tip="column.sentence" class="bc-def">{{ column.sentence }}</p>
    <p class="bc-state"><AppIcon :name="layer === 'rules' ? 'pin' : column.source === 'own' ? 'user' : 'layers'" :size="12" /><span v-clip-tip="state">{{ state }}</span><button v-if="column.source === 'own' && layer !== 'rules' && editable" type="button" class="link-btn" @click="$emit('reset')">{{ text('Reset', 'Zurücksetzen') }}</button></p>
  </header>
</template>
<style scoped>
.board-menu { flex: none; background: transparent; box-shadow: none; border: 0; }.bc-head { display: grid; grid-template-rows: 24px 16px 16px; row-gap: 4px; height: 72px; padding: 0 2px 8px; }.bc-head.full { height: 120px; grid-template-rows: 24px 64px 16px; }.bc-r1 { display: flex; align-items: center; gap: 4px; min-width: 0; }.bc-name { flex: 1; min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; font-size: 13px; font-weight: 650; line-height: 24px; }.bc-def, .bc-state { min-width: 0; margin: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; font-size: 11.5px; line-height: 16px; color: var(--ink-3); }.bc-state { display: flex; align-items: center; gap: 6px; }.bc-state > span { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; }.bc-state svg { flex: none; }.bc-state .link-btn { flex: none; font-size: 11.5px; }.full .bc-def { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 4; white-space: normal; color: var(--ink-2); }
</style>
<style scoped src="../../../styles/settingsButtons.css"></style>
