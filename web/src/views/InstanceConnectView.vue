<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSession } from '../stores/session'
import { useDeskConnections } from '../stores/deskConnections'
import { accountEmail, accountName } from '../lib/api'
import { readInstances, type DeskSibling } from '../lib/deskElsewhere'
import { confirmInstance, takeInstanceFragment } from '../lib/instanceConnect'
import { brand } from '../lib/brand'
import { displayLanguage } from '../lib/displayLanguage'
import Avatar from '../components/Avatar.vue'

const props = defineProps<{ callback?: boolean; openHome?: boolean }>()
const route = useRoute(), router = useRouter(), session = useSession(), store = useDeskConnections()
const text = (en: string, de: string) => displayLanguage() === 'de' ? de : en
const sibling = ref<DeskSibling | null>(null), busy = ref(false), result = ref(''), error = ref(''), returnTo = ref('/decision-desk')
const owner = computed(() => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
let generation = 0, abort = new AbortController()
const callbackFragment = props.callback || props.openHome ? takeInstanceFragment() : ''
const account = computed(() => session.identity ? accountName(session.identity) : '')
const email = computed(() => session.identity ? accountEmail(session.identity) : '')
watch(() => [owner.value, route.fullPath], async () => {
  const turn = ++generation; abort.abort(); abort = new AbortController()
  sibling.value = null; busy.value = false; error.value = ''; result.value = ''
  if (!owner.value || !session.authenticationCurrent()) return
  try {
    if (props.openHome) {
      const who = owner.value, params = new URLSearchParams(callbackFragment.replace(/^#/, ''))
      if (params.get('owner') !== who) throw new Error('This decision belongs to another signed-in person.')
      await store.refresh()
      if (turn !== generation || owner.value !== who || !session.authenticationCurrent()) return
      const home = store.reads.find(row => row.tag === params.get('instance'))
      const target = new URL(params.get('url') ?? '')
      if (!home || target.origin !== home.origin || target.username || target.password || /[\\\r\n\t]/.test(params.get('url') ?? '')) throw new Error('This instance is not configured for this connection.')
      location.replace(target.href)
    } else if (props.callback) {
      const back = await store.acceptFragment(callbackFragment)
      if (turn !== generation) return
      if (!back) throw new Error('Connection could not be completed. Return and connect again.')
      returnTo.value = back; result.value = text('Connected. You can close this tab.', 'Verbunden. Dieser Tab kann geschlossen werden.')
      if (window.opener) window.close()
    } else {
      const origin = typeof route.query.origin === 'string' ? route.query.origin : '', state = typeof route.query.state === 'string' ? route.query.state : ''
      if (!/^[A-Za-z0-9_-]{43}$/.test(state)) throw new Error('Invalid connection request.')
      const instances = await readInstances(abort.signal)
      if (turn !== generation) return
      sibling.value = instances.siblings.find(row => row.origin === origin) ?? null
      if (!sibling.value) throw new Error('This instance is not configured for this connection.')
    }
  } catch (cause) { if (turn === generation) error.value = cause instanceof Error ? cause.message : 'Connection could not be completed.' }
}, { immediate: true, flush: 'sync' })
async function connect() {
  const target = sibling.value, who = owner.value, turn = generation, state = typeof route.query.state === 'string' ? route.query.state : ''
  if (!target || busy.value || !who || !session.authenticationCurrent()) return
  busy.value = true; error.value = ''
  try {
    const destination = await confirmInstance(target.origin, state, abort.signal)
    if (turn === generation && who === owner.value && session.authenticationCurrent()) location.replace(destination)
  } catch (cause) { if (turn === generation) error.value = cause instanceof Error ? cause.message : 'Connection could not be completed.' }
  finally { if (turn === generation) busy.value = false }
}
function cancel() { generation++; abort.abort(); sibling.value = null; result.value = text('Nothing was shared.', 'Es wurde nichts geteilt.') }
async function switchAccount() {
  const back = route.fullPath, turn = generation
  try { await session.signOut(); await router.replace({ path: '/signin', query: { return: back } }) }
  catch { if (turn === generation) error.value = text('Sign out failed. Please try again.', 'Abmelden fehlgeschlagen. Bitte erneut versuchen.') }
}
onBeforeUnmount(() => { generation++; abort.abort() })
</script>

<template>
  <main class="instance-connect">
    <p class="connect-brand">{{ brand.short_name }}<span v-if="session.identity"> · {{ session.identity.tenant.name }}</span></p>
    <article class="connect-card">
      <h1>{{ sibling ? text(`Show your waiting decisions on ${sibling.tag}?`, `Wartende Entscheidungen auf ${sibling.tag} anzeigen?`) : text('Connect another instance', 'Andere Instanz verbinden') }}</h1>
      <template v-if="sibling && !result">
        <p>{{ text(`${sibling.tag} (${new URL(sibling.origin).host}) will read what waits for you here: titles, projects and how long each has waited. It can't change anything. The access lives only in that browser, and you can disconnect it any time in Settings › Personal.`, `${sibling.tag} (${new URL(sibling.origin).host}) liest, was hier wartet: Titel, Projekte und wie lange es schon wartet. Ändern kann es nichts. Der Zugriff gilt nur in diesem Browser und lässt sich jederzeit unter Einstellungen › Persönlich trennen.`) }}</p>
        <div class="connect-account"><Avatar :id="session.identity?.principal.id" :name="account" :size="36" /><span><b>{{ account }}</b><small>{{ email }}</small></span></div>
        <button type="button" class="linkish" :disabled="busy" @click="switchAccount">{{ text('Not you? Switch account', 'Nicht das richtige Konto? Konto wechseln') }}</button>
        <p>{{ text('Ends after 90 days, or when you disconnect.', 'Endet nach 90 Tagen oder beim Trennen.') }}</p>
        <div class="connect-actions"><button type="button" class="btn primary" :disabled="busy" @click="connect">{{ text('Connect', 'Verbinden') }}</button><button type="button" class="btn" :disabled="busy" @click="cancel">{{ text('Cancel', 'Abbrechen') }}</button></div>
      </template>
      <p v-if="result" role="status">{{ result }}</p>
      <RouterLink v-if="callback && result" class="btn" :to="returnTo">{{ text('Return to the Desk', 'Zum Desk zurück') }}</RouterLink>
      <p v-if="error" class="connect-error" role="alert">{{ error }}</p>
    </article>
  </main>
</template>

<style scoped>
.instance-connect { width: min(100% - 32px, 440px); margin: 7vh auto 0; color: var(--ink); }
.connect-brand { color: var(--ink-3); font-weight: 600; }
.connect-card { border: 1px solid var(--line); border-radius: 12px; padding: 24px; background: var(--surface); }
h1 { margin: 0 0 20px; font-size: 22px; line-height: 1.3; overflow-wrap: anywhere; }
p { font-size: 14px; line-height: 1.6; }
.connect-account { display: flex; gap: 12px; align-items: center; margin: 18px 0 10px; }
.connect-account span { display: grid; min-width: 0; overflow-wrap: anywhere; }
.connect-account small { color: var(--ink-3); }
.connect-actions { display: flex; flex-wrap: wrap; gap: 10px; }
button, .btn { min-height: 44px; }
.connect-error { color: var(--danger); }
@media (max-width: 520px) { .instance-connect { margin-top: 24px; } .connect-card { padding: 20px; } .connect-actions { flex-direction: column; } }
</style>
