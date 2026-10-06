<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useSession } from '../../stores/session'
import { can, onAccessChange, permissionsAvailable, permissionsKnown } from '../../lib/authz'
import { permissionLabel } from '../../lib/access'
import { scopeOwner } from '../../lib/identityScope'
import { createPoliciesReader, deniedPolicy, ELSEWHERE_RULES, keyLimits, ownerLinks, POLICY_ROLES, POLICY_TABS, truncatedLadder, type PolicyRole, type PolicyTab } from '../../lib/policies'
import AppIcon from '../AppIcon.vue'
import PolicyLadderEditor from './PolicyLadderEditor.vue'
import PolicyPreferencesEditor from './PolicyPreferencesEditor.vue'

const session = useSession()
const modelView = ref<'ladder' | 'preferences'>('ladder')
const tab = ref<PolicyTab>('ladders'), role = ref<PolicyRole>('review-gate')
const reader = createPoliciesReader(() => scopeOwner(session.identity))
const { state, ladder, registry } = reader
const person = computed(() => session.identity?.principal.kind === 'person')
const permitted = computed(() => !!session.identity && (tab.value === 'elsewhere' ? person.value : can(tab.value === 'ladders' ? 'models.read' : 'roles.read') && (tab.value !== 'keys' || person.value)))
const links = computed(() => person.value ? ownerLinks(permission => can(permission)) : [])
const groups = computed(() => keyLimits(registry.value))
const roleDetail = computed(() => POLICY_ROLES.find(item => item.id === role.value)!.detail)
const sheet = ref<HTMLDialogElement | null>(null), detailsButton = ref<HTMLButtonElement | null>(null)
const ready = computed(() => tab.value === 'elsewhere' || permissionsKnown())
const statusText = computed(() => state.value === 'loading' ? 'Loading the rules…' : state.value === 'denied' ? tab.value === 'ladders' && modelView.value === 'preferences' ? 'To edit model preferences here, you also need workspace model visibility.' : deniedPolicy(tab.value) : state.value === 'failed' ? 'The rules could not be loaded. Try opening this tab again; the other tabs still work.' : ladder.value?.truncated ? truncatedLadder() : '')
const announcement = ref('')
// Populate the already-mounted region, including the first loading message.
watch(statusText, async () => { await nextTick(); announcement.value = statusText.value }, { immediate: true })
function closeDetails() { sheet.value?.close(); detailsButton.value?.focus({ preventScroll: true }) }
async function openDetails() { await nextTick(); sheet.value?.showModal() }
function load() {
  sheet.value?.close()
  if (!ready.value) { reader.reset(); return }
  if (tab.value !== 'elsewhere' && !permissionsAvailable()) { reader.reset(); state.value = 'failed'; return }
  if (tab.value === 'ladders' && modelView.value === 'preferences') { reader.reset(); state.value = permitted.value ? 'loaded' : 'denied'; return }
  void reader.load(tab.value, role.value, permitted.value)
}
// Compare each authority/source value, rather than a newly allocated tuple on
// every permission-cache revision (including unchanged focus answers).
watch([() => scopeOwner(session.identity), tab, modelView, role, ready, permitted, permissionsAvailable], load, { immediate: true, flush: 'sync' })
const stopAccess = onAccessChange(change => {
  // Same-person refresh belongs to the mounted editor. Resetting the parent
  // would unmount it and discard its captured draft, write and confirmed Undo.
  if (change === 'reset' || tab.value !== 'ladders' || state.value !== 'loaded') { reader.reset(); sheet.value?.close(); load() }
})
onBeforeUnmount(() => { stopAccess(); reader.dispose() })
function tabKey(event: KeyboardEvent, index: number) {
  if (event.altKey || event.ctrlKey || event.metaKey || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? 2 : (index + (event.key === 'ArrowRight' ? 1 : -1) + 3) % 3
  tab.value = POLICY_TABS[next]!.id
  const tabs = (event.currentTarget as HTMLElement).parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]')
  tabs?.[next]?.focus({ preventScroll: true })
}
</script>

<template>
  <section class="policies" aria-labelledby="policies-title">
    <header class="policy-head">
      <p class="eyebrow">Who may do what, when</p>
      <h2 id="policies-title">Policies</h2>
      <p class="intro">The rules already in force, and where they belong.</p>
    </header>
    <div class="policy-tabs" role="tablist" aria-label="Policy sections">
      <button v-for="(item, index) in POLICY_TABS" :id="`policy-tab-${item.id}`" :key="item.id" role="tab" data-testid="policies-tab" :aria-selected="tab === item.id" :aria-controls="`policy-panel-${item.id}`" :tabindex="tab === item.id ? 0 : -1" @click="tab = item.id" @keydown="tabKey($event, index)">
        <span>{{ item.label }}</span><small>{{ item.note }}</small>
      </button>
    </div>
    <p data-testid="policies-announcement" role="status" aria-live="polite" aria-atomic="true" class="sr-only">{{ announcement }}</p>
    <template v-for="panelTab in POLICY_TABS" :key="panelTab.id">
      <div v-if="tab !== panelTab.id" :id="`policy-panel-${panelTab.id}`" role="tabpanel" :aria-labelledby="`policy-tab-${panelTab.id}`" hidden />
    </template>
    <div :id="`policy-panel-${tab}`" role="tabpanel" :aria-labelledby="`policy-tab-${tab}`" class="policy-panel">
      <div v-if="permitted" class="policy-controls">
        <p class="source-note">{{ tab === 'ladders' ? 'Saved job order and model preferences · owned by their model sources.' : tab === 'keys' ? 'Permission registry · owned by Access. These permissions cannot be carried by agent keys.' : 'Existing owners · this page holds no policy values of its own.' }}</p>
        <button ref="detailsButton" class="detail-link" data-testid="policies-row-link" @click="openDetails">About these rules <AppIcon name="chevron-right" :size="14" /></button>
        <div v-if="tab === 'ladders'" class="model-views" role="group" aria-label="Model policy view"><button :aria-pressed="modelView === 'ladder'" @click="modelView = 'ladder'">Saved job order</button><button :aria-pressed="modelView === 'preferences'" @click="modelView = 'preferences'">Model preferences</button></div>
        <div v-if="tab === 'ladders' && modelView === 'ladder'" class="role-block">
          <div class="policy-roles" role="group" aria-label="Model role" data-testid="policies-role-group">
            <button v-for="item in POLICY_ROLES" :key="item.id" data-testid="policies-role" :aria-pressed="role === item.id" @click="role = item.id">{{ item.label }}</button>
          </div>
          <p class="role-detail">{{ roleDetail }}</p>
        </div>
        <nav v-if="tab === 'elsewhere' && links.length" class="owner-links" aria-label="Rule owners">
          <RouterLink v-for="link in links" :key="link.title" :to="link.to!" data-testid="policies-owner-link">{{ link.title }} <AppIcon name="chevron-right" :size="12" /></RouterLink>
        </nav>
      </div>
      <div class="policy-content" :aria-busy="state === 'loading'">
        <p v-if="state === 'loading' || state === 'denied' || state === 'failed'" class="state-line">{{ statusText }}</p>
        <template v-else-if="tab === 'ladders'">
          <PolicyLadderEditor v-if="modelView === 'ladder'" :key="`${scopeOwner(session.identity)}/${role}`" :owner="scopeOwner(session.identity)" :role="role" :person="person" />
          <PolicyPreferencesEditor v-else :key="scopeOwner(session.identity)" :owner="scopeOwner(session.identity)" :person="person" />
        </template>
        <template v-else-if="tab === 'keys'">
          <section v-for="group in groups" :key="group.risk" class="risk-group" :aria-label="`${group.risk} risk permissions`">
            <h3 class="risk-title">{{ group.risk }} risk</h3>
            <div v-for="permission in group.items" :key="permission.key" class="policy-row key-row">
              <div><h3>{{ permissionLabel(permission.key) }}</h3><p>{{ permission.description }}</p><code>{{ permission.key }}</code></div><span class="rule-status">Enforced</span>
            </div>
          </section>
          <p v-if="!groups.length" class="state-line">The registry lists no permissions excluded from agent keys.</p>
        </template>
        <div v-else class="elsewhere-rows">
          <article v-for="rule in ELSEWHERE_RULES" :key="rule.title" class="policy-row key-row">
            <div><h3>{{ rule.title }}</h3><p>{{ rule.text }}</p><small class="owner">Owner: {{ rule.owner }}</small><button v-if="rule.title === 'Model preferences' || rule.title === 'Review ladder editing'" class="detail-link" @click="modelView = rule.title === 'Model preferences' ? 'preferences' : 'ladder'; tab = 'ladders'">Open here <AppIcon name="chevron-right" :size="12" /></button></div><span class="rule-status">{{ rule.status }}</span>
          </article>
        </div>
      </div>
    </div>
    <dialog ref="sheet" class="policy-sheet" aria-labelledby="policy-sheet-title" @cancel.prevent="closeDetails">
      <header><p class="eyebrow">Policy source</p><h2 id="policy-sheet-title">About these rules</h2></header>
      <div class="sheet-body">
        <template v-if="tab === 'ladders' && ladder">
          <h3>Configured ladder · CLI</h3><p>The CLI walks the configured priority and harness health. It has no ticket context. A listed step is not a promise that dispatch can run it.</p>
          <h3>Qualified routing · Dispatch</h3><p>Dispatch checks platform capability and approved accounts for the work, then applies model preferences.</p>
          <template v-if="role === 'review-gate'">
            <p>Dispatch uses {{ ladder.dispatch_family_order.join(', ') }} as its built-in review family order.</p>
            <p v-for="floor in ladder.review_floors" :key="floor">{{ floor }}</p>
          </template>
          <h3>Availability</h3><p>A suspended step records its reason and expiry. Once the suspension expires, it stops excluding that profile. The server decides availability when routing runs.</p>
          <p v-if="ladder.truncated">{{ truncatedLadder() }}</p>
        </template>
        <template v-else-if="tab === 'keys'"><h3>Permission registry · Access</h3><p>These permissions are marked as not grantable to agent keys in the live registry. Holding a role does not bypass a key’s scope ceiling.</p><p>This registry endpoint is available to person sessions with See roles. Bearer keys are refused by the middleware even when they hold See roles.</p></template>
        <template v-else-if="tab === 'elsewhere'"><h3>The existing owner decides</h3><p>This page reads existing rules. It stores no policy, grants no exception and changes no enforcement.</p><p>Enforced describes existing checks in this workspace. Advisory describes guidance or rules owned elsewhere, including GitHub repository controls.</p><p>Editors are linked only where an existing screen and your permissions allow it. Model preferences and saved job order can be changed on the first tab with the owning source’s permissions. Lane coordination remains with its owner.</p></template>
        <p v-else>{{ statusText || 'Open this tab again to load its rules.' }}</p>
      </div>
      <footer><button class="btn" data-testid="policies-sheet-close" @click="closeDetails">Close <kbd>Esc</kbd></button></footer>
    </dialog>
  </section>
</template>

<style scoped>
.policies { min-width: 0; color: var(--ink); }
.policy-head { margin-bottom: 22px; }
.policy-head h2 { font-size: 28px; letter-spacing: -.035em; margin: 5px 0 8px; }
.intro, .source-note, .role-detail, .policy-row p, .routing-notes, .state-line, .sheet-body p { color: var(--ink-2); font-size: 13px; line-height: 1.6; }
.policy-tabs { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); border-block: 1px solid var(--line-2); }
.policy-tabs button { display: flex; flex-direction: column; justify-content: center; gap: 5px; height: 78px; text-align: left; padding: 10px 14px; background: transparent; color: var(--ink-2); border: 0; font-size: 13px; }
.policy-tabs button[aria-selected="true"] { background: var(--row-selected); color: var(--ink); }
.policy-tabs button span { font-weight: 650; }
.policy-tabs small { font-size: 12px; line-height: 1.4; font-weight: 400; }
.policy-tabs button:focus-visible, .policy-roles button:focus-visible, .detail-link:focus-visible, .owner-links a:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
.policy-controls { padding-top: 18px; }
.source-note { min-height: 42px; margin: 0; }
.detail-link { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0; color: var(--teal-ink); background: none; border: 0; font-size: 12px; font-weight: 600; }
.model-views { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:6px; margin-top:12px; }
.model-views button { min-height:44px; padding:8px; background:transparent; color:var(--ink-2); border:1px solid var(--line-2); font-size:12px; }
.model-views button[aria-pressed="true"] { background:var(--row-selected); color:var(--ink); font-weight:650; }
.role-block { margin-top: 12px; }
.policy-roles { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 4px; }
.policy-roles button { height: 42px; padding: 6px; font-size: 12px; border: 1px solid var(--line-2); border-radius: 3px; background: transparent; color: var(--ink-2); }
.policy-roles button[aria-pressed="true"] { background: var(--row-selected); color: var(--ink); font-weight: 650; }
.role-detail { height: 42px; margin: 10px 0 0; }
.owner-links { display: flex; flex-wrap: wrap; gap: 8px 20px; padding: 12px 0 18px; }
.owner-links a { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; color: var(--teal-ink); min-height: 28px; }
.policy-content { overflow-wrap: anywhere; }
.state-line { margin: 0; padding: 20px 0; }
.ladder { list-style: none; padding: 0; margin: 0; }
.policy-row { display: grid; grid-template-columns: 34px minmax(0, 1fr); gap: 12px; padding: 18px 0; border-top: 1px solid var(--line); align-items: start; }
.policy-row h3 { font-size: 14px; font-weight: 650; margin: 0 0 4px; }
.policy-row p { margin: 0; }
.step-number { color: var(--ink-3); font-variant-numeric: tabular-nums; font-size: 12px; padding-top: 2px; }
.rule-status { font-size: 11px; color: var(--ink-2); padding-top: 3px; }
.list-status { font-size: 12px; color: var(--ink-2); margin: 0; padding: 8px 0; }
.step-state { margin-top: 5px !important; }
.key-row { grid-template-columns: minmax(0, 1fr) auto; }
.policy-row code, .owner { display: block; margin-top: 6px; font-size: 11px; color: var(--ink-3); }
.risk-title { text-transform: capitalize; font-size: 12px; margin: 20px 0 12px; letter-spacing: .04em; color: var(--ink-2); }
.truncation { font-size: 12px; color: var(--warn-ink); padding: 14px 0; }
.routing-notes { padding: 16px 0 0; border-top: 1px solid var(--line); }
.routing-notes p { margin: 0 0 8px; }
.routing-notes strong { color: var(--ink); margin-right: 4px; }
.policy-sheet { position: fixed; inset: 8vh 0 auto; margin: 0 auto; padding: 0; width: min(600px, calc(100vw - 40px)); max-height: 84dvh; border: 1px solid var(--line-2); border-radius: 6px; background: var(--surface); color: var(--ink); }
.policy-sheet[open] { display: grid; grid-template-rows: auto minmax(0, 1fr) auto; }
.policy-sheet::backdrop { background: var(--scrim); }
.policy-sheet header { padding: 24px 26px 18px; }
.policy-sheet h2 { margin-top: 8px; font-size: 22px; }
.sheet-body { padding: 0 26px 20px; overflow-y: auto; overflow-wrap: anywhere; }
.sheet-body h3 { font-size: 14px; margin: 16px 0 8px; }
.sheet-body p { margin: 0 0 12px; }
.policy-sheet footer { padding: 14px 26px max(14px, env(safe-area-inset-bottom)); border-top: 1px solid var(--line); display: flex; justify-content: flex-end; }
.policy-sheet kbd { font-size: 10px; color: var(--ink-2); }
@media (max-width: 600px) {
  .policy-tabs button { padding: 8px; height: 100px; font-size: 12px; }
  .source-note { min-height: 63px; }
  .policy-roles { grid-template-columns: repeat(6, minmax(0, 1fr)); }
  .policy-roles button { grid-column: span 2; }
  .policy-roles button:nth-last-child(-n+2) { grid-column: span 3; }
  .role-detail { height: 42px; }
  .policy-row { gap: 8px; grid-template-columns: 22px minmax(0, 1fr); }
  .key-row { grid-template-columns: minmax(0, 1fr) auto; }
  .policy-sheet { inset: 0; margin: 0; width: 100%; max-width: none; height: 100dvh; max-height: 100dvh; border: 0; border-radius: 0; }
  .policy-sheet header { padding: 22px 20px 16px; }
  .sheet-body { padding-inline: 20px; }
  .policy-sheet footer { padding-inline: 20px; }
}
</style>
