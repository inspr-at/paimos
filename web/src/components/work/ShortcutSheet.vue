<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, ref } from 'vue'
import AppIcon, { type IconName } from '../AppIcon.vue'

type Key = string | { icon: IconName; label: string }
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
// Symbol keys are drawn, never typed: Command and Option on a Mac, words elsewhere.
const MOD: Key = mac ? { icon: 'command', label: 'Command' } : 'Ctrl'
const ALT: Key = mac ? { icon: 'option', label: 'Option' } : 'Alt'
const BACKSPACE: Key = { icon: 'backspace', label: 'Backspace' }
const sections: { title: string; rows: { keys: Key[][]; label: string; joiner?: string }[] }[] = [
  { title: 'Anywhere', rows: [
    { keys: [[MOD, 'K']], label: 'Search tickets, projects and actions' },
    { keys: [['/']], label: 'Search, on pages without a list' },
    { keys: [['g'], ['p']], joiner: 'then', label: 'Go to Projects' },
    { keys: [['g'], ['a']], joiner: 'then', label: 'Go to Agents' },
    { keys: [['g'], ['b']], joiner: 'then', label: 'Go to Business' },
    { keys: [['?']], label: 'This sheet' },
  ] },
  { title: 'Projects', rows: [
    { keys: [['j'], ['k']], label: 'Next and previous project' },
    { keys: [[{ icon: 'arrow-left', label: 'Left arrow' }], [{ icon: 'arrow', label: 'Right arrow' }], [{ icon: 'arrow-up', label: 'Up arrow' }], [{ icon: 'arrow-down', label: 'Down arrow' }]], label: 'Among cards: the card beside, above or below' },
    { keys: [['x']], label: 'Select the project; Shift-click selects a range' },
    { keys: [['m']], label: 'Move to group, with type-ahead; a new name makes a group' },
    { keys: [[ALT, { icon: 'arrow-up', label: 'Up arrow' }], [ALT, { icon: 'arrow-down', label: 'Down arrow' }]], label: 'On a group header: move the group up or down' },
    { keys: [['Esc']], label: 'Clear the selection' },
  ] },
  { title: 'Search', rows: [
    { keys: [[{ icon: 'arrow-down', label: 'Down arrow' }], [{ icon: 'arrow-up', label: 'Up arrow' }]], label: 'Move through results' },
    { keys: [['Tab']], label: 'Next group of results' },
    { keys: [[MOD, { icon: 'enter', label: 'Enter' }]], label: 'Open in a new tab' },
    { keys: [[BACKSPACE]], label: 'Search all projects, not only this one' },
  ] },
  { title: 'Agents', rows: [
    { keys: [['j'], ['k']], label: 'Next and previous request or session' },
    { keys: [['a'], ['d']], label: 'Approve or deny a permission; resolve or dismiss a held request' },
    { keys: [[{ icon: 'enter', label: 'Enter' }]], label: 'Open the selected session' },
  ] },
  { title: 'Hours', rows: [
    { keys: [['l']], label: 'Log time' },
    { keys: [[{ icon: 'arrow-left', label: 'Left arrow' }], [{ icon: 'arrow', label: 'Right arrow' }]], label: 'Previous and next week' },
    { keys: [['t']], label: 'This week' },
    { keys: [[{ icon: 'enter', label: 'Enter' }]], label: 'Log the entry' },
    { keys: [['Esc']], label: 'Close the review' },
  ] },
  { title: 'Journey', rows: [
    { keys: [['['], [']']], label: 'Previous and next stage' },
    { keys: [['w']], label: 'Walk through the release' },
    { keys: [[{ icon: 'arrow-left', label: 'Left arrow' }], [{ icon: 'arrow', label: 'Right arrow' }]], label: 'In the walker: previous and next ticket; with Shift, feature' },
    { keys: [['Space']], label: 'In the walker: include in the release or defer' },
    { keys: [['c'], ['z'], ['i']], joiner: '·', label: 'In the walker: compare, 100 %, details' },
  ] },
  { title: 'Release history', rows: [
    { keys: [['j'], ['k']], label: 'Next and previous release' },
    { keys: [[{ icon: 'enter', label: 'Enter' }]], label: 'Open the release' },
    { keys: [['c']], label: 'Compare two releases' },
    { keys: [['e']], label: 'Show or hide the evidence' },
    { keys: [['/']], label: 'Search headlines, changes and ticket keys' },
  ] },
  { title: 'Ticket list', rows: [
    { keys: [['j'], [{ icon: 'arrow-down', label: 'Down arrow' }]], label: 'Next ticket' },
    { keys: [['k'], [{ icon: 'arrow-up', label: 'Up arrow' }]], label: 'Previous ticket' },
    { keys: [[{ icon: 'enter', label: 'Enter' }], ['o']], label: 'Open the ticket in the side panel' },
    { keys: [['Esc']], label: 'Close the side panel' },
    { keys: [['/']], label: 'Search this list' },
    { keys: [[{ icon: 'shift', label: 'Shift' }, 'F']], label: 'Filter by labels, epic, cost unit, release or date' },
    { keys: [['-']], label: 'In a filter menu: exclude the value' },
  ] },
  { title: 'Select and change many', rows: [
    { keys: [['x']], label: 'Select or clear the ticket' },
    { keys: [[{ icon: 'shift', label: 'Shift' }, 'J'], [{ icon: 'shift', label: 'Shift' }, 'K']], label: 'Grow the selection down or up' },
    { keys: [[MOD, 'A']], label: 'Select every loaded ticket' },
    { keys: [['s'], ['a'], ['p'], ['l'], ['m']], joiner: '·', label: 'With a selection: status, assignee, priority, labels, move' },
    { keys: [['Esc']], label: 'Clear the selection' },
  ] },
  { title: 'Knowledge', rows: [
    { keys: [['j'], ['k']], label: 'Next and previous entry, in the list and on an entry' },
    { keys: [[{ icon: 'enter', label: 'Enter' }], ['o']], label: 'Open the entry' },
    { keys: [['/']], label: 'Search titles, slugs and text' },
    { keys: [['n']], label: 'New entry' },
    { keys: [['e']], label: 'Edit the entry' },
    { keys: [[MOD, { icon: 'enter', label: 'Enter' }]], label: 'Save the entry' },
    { keys: [['Esc']], label: 'Back to the list, or cancel editing' },
  ] },
  { title: 'Outline', rows: [
    { keys: [[{ icon: 'arrow', label: 'Right arrow' }]], label: 'Expand, or step into the first child' },
    { keys: [[{ icon: 'arrow-left', label: 'Left arrow' }]], label: 'Collapse, or step out to the parent' },
    { keys: [['Space']], label: 'Expand or collapse' },
    { keys: [['n']], label: 'On an epic: new ticket inside it' },
  ] },
  { title: 'Open ticket', rows: [
    { keys: [['j'], ['k']], label: 'Next and previous ticket, the list follows' },
    { keys: [['e']], label: 'Edit the whole ticket' },
    { keys: [[MOD, { icon: 'enter', label: 'Enter' }]], label: 'Save the edit' },
    { keys: [[ALT, { icon: 'arrow-left', label: 'Left arrow' }]], label: 'Back along followed links' },
    { keys: [[MOD, 'V']], label: 'Paste a screenshot as an attachment' },
    { keys: [['s']], label: 'Status' },
    { keys: [['p']], label: 'Priority' },
    { keys: [['a']], label: 'Assignee' },
    { keys: [['r']], label: 'Link to another ticket' },
    { keys: [['c']], label: 'Write a comment' },
    { keys: [['f']], label: 'Full page and back' },
    { keys: [[MOD, { icon: 'enter', label: 'Enter' }]], label: 'Save a description, notes or comment' },
  ] },
  { title: 'Create', rows: [
    { keys: [['n']], label: 'New ticket at the top of the list' },
  ] },
  { title: 'Menus', rows: [
    { keys: [['1'], ['8']], joiner: 'to', label: 'Choose a status in the status menu' },
  ] },
]
const dialog = ref<HTMLDialogElement>()
const closeButton = ref<HTMLButtonElement>()
let opener: HTMLElement | null = null
async function open() {
  if (dialog.value?.open) return
  opener = document.activeElement as HTMLElement
  dialog.value?.showModal()
  await nextTick()
  closeButton.value?.focus()
}
function close() { dialog.value?.close(); opener?.focus({ preventScroll: true }) }
function backdrop(event: MouseEvent) { if (event.target === dialog.value) close() }
defineExpose({ open, close })
</script>

<template>
  <dialog ref="dialog" class="sheet" aria-labelledby="shortcuts-title" @cancel.prevent="close" @click="backdrop">
    <div class="sheet-card">
      <header>
        <h2 id="shortcuts-title">Keyboard shortcuts</h2>
        <button ref="closeButton" type="button" class="icon-btn sm" aria-label="Close shortcuts" @click="close"><AppIcon name="close" :size="14" /></button>
      </header>
      <section v-for="section in sections" :key="section.title">
        <p class="eyebrow">{{ section.title }}</p>
        <dl>
          <div v-for="row in section.rows" :key="row.label" class="row">
            <dt>
              <template v-for="(combo, c) in row.keys" :key="c">
                <span v-if="c > 0" class="or">{{ row.joiner ?? 'or' }}</span>
                <span class="combo">
                  <kbd v-for="(key, k) in combo" :key="k" class="keycap">
                    <template v-if="typeof key === 'string'">{{ key }}</template>
                    <template v-else><AppIcon :name="key.icon" /><span class="sr-only">{{ key.label }}</span></template>
                  </kbd>
                </span>
              </template>
            </dt>
            <dd>{{ row.label }}</dd>
          </div>
        </dl>
      </section>
    </div>
  </dialog>
</template>

<style scoped>
.sheet { width: min(520px, calc(100vw - 24px)); max-width: none; max-height: calc(100dvh - 48px); padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.sheet::backdrop { background: var(--scrim); backdrop-filter: blur(3px); }
.sheet-card { max-height: calc(100dvh - 48px); overflow: auto; padding: 22px 26px 18px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); }
header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
h2 { font-size: 19px; }
section { margin-top: 14px; }
section .eyebrow { margin-bottom: 4px; }
dl { margin: 0; }
.row { display: grid; grid-template-columns: 150px 1fr; align-items: center; gap: 12px; min-height: 34px; border-bottom: 1px solid var(--line); }
.row:last-child { border-bottom: 0; }
dt { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
dd { margin: 0; font-size: 13.5px; color: var(--ink); }
.combo { display: inline-flex; gap: 3px; }
.keycap { min-width: 20px; height: 20px; font-size: 11px; }
.or { font-size: 11px; color: var(--ink-3); }
@media (max-width: 600px) { .sheet-card { padding: 18px; } .row { grid-template-columns: 116px 1fr; } }
</style>
