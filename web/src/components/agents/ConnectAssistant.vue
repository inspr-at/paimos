<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useSession } from '../../stores/session'
import { useAssistants } from '../../stores/assistants'
import { assistantRequest, type Assistant, type AssistantPolicy } from '../../lib/assistants'
import { assistantCopy } from '../../lib/assistantsCopy'
import { shortcutTyping } from '../../lib/shortcuts'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
const session = useSession(), store = useAssistants(), router = useRouter(), words = assistantCopy()
const name = ref(''), busy = ref(false), error = ref('')
let alive = true
onBeforeUnmount(() => { alive = false })
const owner = computed(() => session.identity ? `${session.identity.tenant.id}/${session.identity.principal.id}` : '')
const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
watch(owner, () => { name.value = ''; busy.value = false; error.value = '' })
async function connect() {
  const identity = session.identity, who = owner.value, displayName = name.value.trim()
  if (!store.mayConnect || !identity || busy.value || !displayName) return
  busy.value = true; error.value = ''
  const policy: AssistantPolicy = { allowed_projects: [], allowed_scopes: ['inbox.read', 'inbox.receipt', 'inbox.send'], weekly_budget: { amount: '0', unit: 'USD' }, outward_action_rule: 'approval_required', never_ask_person_for_secrets_or_manual_steps: true }
  try {
    const item = await assistantRequest<Assistant>('/assistants', 'POST', { display_name: displayName, vendor: 'grok', owner_person_id: identity.principal.id, policy })
    if (!alive || who !== owner.value) return
    store.invalidate(); store.adopt(item)
    await router.push({ path: '/agents', query: { assistant: item.principal_id } })
  } catch (failure) { if (alive && who === owner.value) error.value = failure instanceof Error ? failure.message : words.loadFailed }
  finally { if (alive && who === owner.value) busy.value = false }
}
function key(event: KeyboardEvent) { if (shortcutTyping(event.target) && event.key === 'Enter' && !event.altKey && !event.shiftKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)) { event.preventDefault(); void connect() } }
</script>
<template>
  <article class="connect-assistant" @keydown="key">
    <RouterLink class="back-link" to="/agents"><AppIcon name="arrow-left" />Agents</RouterLink>
    <h1>{{ words.connect }}</h1><p class="connect-context">{{ words.empty.split('. ').slice(1).join('. ') }}</p>
    <form v-if="store.mayConnect" @submit.prevent="connect">
      <div class="connect-fields"><label>{{ words.name }}<input v-model="name" class="field" maxlength="200" required :disabled="busy" autocomplete="off" /></label><label>{{ words.vendor }}<input class="field" value="Grok" readonly /></label></div>
      <div class="connect-actions"><RouterLink class="btn" to="/agents">{{ words.cancel }}</RouterLink><button class="btn primary" type="submit" :disabled="busy || !name.trim()" :aria-keyshortcuts="mac ? 'Meta+Enter' : 'Control+Enter'">{{ words.connect }}<KeyCap k="mod" /><KeyCap k="enter" /></button></div>
      <p class="connect-result" role="status">{{ error }}</p><p class="connect-explanation">{{ words.connectHonest }}</p>
    </form><p v-else>{{ words.readOnly }}</p>
  </article>
</template>
<style scoped>
.connect-assistant { max-width: 760px; margin: 0 auto; padding: 28px var(--gutter) 48px; color: var(--ink); }
.back-link { display: inline-flex; align-items: center; gap: 7px; min-height: 44px; color: var(--ink-2); font-size: 13px; }
h1 { margin: 18px 0 12px; font-size: 28px; }
.connect-context, .connect-explanation { color: var(--ink-2); font-size: 13px; line-height: 1.6; }
.connect-fields { display: grid; gap: 16px; margin: 24px 0 16px; }
label { display: grid; gap: 6px; font-size: 13px; }
input { min-width: 0; width: 100%; min-height: 44px; }
.connect-actions { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; }
.connect-actions .btn { min-height: 44px; }
.connect-result { color: var(--danger); min-height: 20px; font-size: 13px; }
@media (max-width: 520px) { .connect-assistant { padding-inline: 16px; } }
</style>
