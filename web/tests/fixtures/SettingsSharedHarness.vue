<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import NeedsYouList, { type NeedsYouItem } from '../../src/components/settings/NeedsYouList.vue'
import SettingsDockedPanel from '../../src/components/settings/SettingsDockedPanel.vue'
import SettingsPopover, { type SettingsMenuItem } from '../../src/components/settings/SettingsPopover.vue'
import AppIcon from '../../src/components/AppIcon.vue'
const dock = ref<InstanceType<typeof SettingsDockedPanel>>()
const open = ref(false), pop = ref(false), otherPop = ref(false), form = ref(false)
const opener = ref<HTMLElement | null>(null), anchor = ref<HTMLElement | null>(null), otherAnchor = ref<HTMLElement | null>(null)
const context = ref('accounts-admin-person-1'), record = ref('computer-1')
const empty = ref(false), long = ref(false), draft = ref('10'), error = ref(''), writes = ref(0), pending = ref(false)
const query = new URLSearchParams(location.search)
const german = query.has('german')
const formLabel = german ? 'Warnschwellen für Kontingente ändern' : 'Quota warnings'
const items = computed<NeedsYouItem[]>(() => [
  { id: 'verify', name: german ? 'Anmeldung auf dem Arbeitscomputer erneut überprüfen' : 'Sign-in needs verification', detail: german ? 'Die Anmeldung auf dem Arbeitscomputer konnte nicht bestätigt werden.' : 'A short check on this computer.', count: empty.value ? 0 : 1, icon: 'key', action: { label: german ? 'Erneut überprüfen' : 'Verify again', fixesProblem: true } },
  { id: 'cleanup', name: german ? 'Bereinigung des entfernten Computers abschließen' : 'Computer cleanup is waiting', detail: german ? 'Andere Computer und Konten bleiben unverändert.' : 'Other computers keep working.', count: empty.value ? 0 : 1, tone: 'waiting', icon: 'monitor', action: { label: german ? 'Bereinigung öffnen' : 'Open cleanup' } },
  { id: 'missed', name: 'No missed releases', detail: 'Nothing waiting for a release.', count: 0 },
])
const menu: SettingsMenuItem[] = [
  { id: 'read', label: 'Read quota now', detail: 'Ask for a fresh reading.', icon: 'refresh' },
  { id: 'disabled', label: 'Unavailable action', disabled: true },
  { id: 'remove', label: 'Remove computer…', icon: 'trash', confirmation: { title: 'Remove this computer?', effect: 'New work stops and its sign-ins are blocked.', keeps: 'Accounts on other computers keep working.', action: 'Remove computer' } },
]
function show(event: Event) { opener.value = event.currentTarget as HTMLElement; open.value = true }
function showMenu(event: Event) { anchor.value = event.currentTarget as HTMLElement; pop.value = !pop.value; form.value = false }
function showForm(event: Event) { anchor.value = event.currentTarget as HTMLElement; form.value = true; error.value = ''; pop.value = true }
function select(id: string, owner: string | undefined) { if (owner !== context.value) return; writes.value++; pop.value = false; void id }
function save(owner: string | undefined) {
  if (owner !== context.value) return
  if (draft.value === 'fail') { error.value = 'Could not save. Try again.'; return }
  if (draft.value === 'pending') { pending.value = true; return }
  writes.value++; pop.value = false
}
</script>
<template>
  <main>
    <div class="test-controls"><button class="btn" @click="empty = !empty">Toggle empty</button><button class="btn" @click="context = 'other-person'">Change identity</button><button class="btn" @click="long = !long">Toggle content</button><output aria-label="Writes">{{ writes }}</output></div>
    <SettingsDockedPanel ref="dock" v-model:open="open" :title="record === 'computer-1' ? 'Arbeitscomputer mit ausführlichem Namen' : 'Computer 2'" fact="macOS · Connected" icon="monitor" :opener="opener" :context-key="context" :record-key="record">
      <template #navigation><button class="btn nav-control" @click="context = 'autopilot'">Accounts and computers</button><p>Autopilot</p></template>
      <NeedsYouList :items="items" aside="Everything else is working." />
      <div class="rows"><button v-for="n in 3" :key="n" class="record-row" :data-row="n" @click="show">Arbeitscomputer {{ n }} — Accounts and computers</button></div>
      <div class="page-actions"><button class="btn" @click="showMenu">Page menu</button><button class="btn" @click="showForm">Change quota</button><button class="btn" @click="otherAnchor = $event.currentTarget as HTMLElement; otherPop = true">Other menu</button></div>
      <div class="page-tail">Content grows downward.</div>
      <template #overflow><button class="btn sm" aria-label="More actions" @click="showMenu"><AppIcon name="more" /></button></template>
      <template #panel><label>Computer name<input class="field" value="Arbeitscomputer" /></label><p v-for="n in long && record === 'computer-1' ? 60 : 2" :key="n">{{ n }}. Details about this computer, its sign-ins and cleanup. This text stays fully readable.</p></template>
      <template #footer><button class="btn" @click="record = record === 'computer-1' ? 'computer-2' : 'computer-1'">Next computer</button><button class="btn primary" @click="showForm">Apply capacity</button></template>
    </SettingsDockedPanel>
    <SettingsPopover v-model:open="pop" :anchor="anchor" :frame="dock?.frame" :label="form ? formLabel : 'Computer actions'" :mode="form ? 'form' : 'menu'" :items="menu" :context-key="context" hint="Whole percent from 1 to 50." :error="error" :busy="pending" @select="select" @submit="save">
      <template #default="{ hintId }"><label class="form-label">Early notice<input v-model="draft" class="field" :aria-describedby="hintId" /></label></template>
    </SettingsPopover>
    <SettingsPopover v-model:open="otherPop" :anchor="otherAnchor" label="Other actions" :items="menu" />
  </main>
</template>
<style scoped>
main { padding: 16px; }.test-controls { display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 16px; }.nav-control { max-width: 100%; }.rows { margin-top: 24px; }.record-row { display: block; width: fit-content; max-width: 100%; min-height: 60px; padding: 12px; border: 0; border-bottom: 1px solid var(--line); background: transparent; color: var(--ink); text-align: left; font-size: 14px; }.record-row:hover { background: var(--row-hover); }.page-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 24px; }.page-tail { margin-top: 24px; min-height: 1000px; }.pane-body p { margin: 12px 0; }label, .form-label { display: grid; gap: 6px; margin-top: 12px; }
</style>
