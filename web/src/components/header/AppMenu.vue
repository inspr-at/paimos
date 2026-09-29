<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The gear menu is about the app and the workspace (AEON-312): workspace settings
// for admins, connecting agents, the release history and shortcuts, help and
// feedback, and whether the system is well. The avatar beside it is about me.
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '../../lib/api'
import { can, myWorkspaceRole } from '../../lib/authz'
import { run } from '../../lib/commands'
import { feedbackRecipient, sendFeedback, type FeedbackRecipient } from '../../lib/feedback'
import { FEEDBACK_MAX, feedbackBody, summarizeStatus, type SystemProbe } from '../../lib/headerMenus'
import { displayHeadline, hasUsableNotes, localizedNote } from '../../lib/releases'
import { absoluteTime, relativeTime } from '../../lib/work'
import { useReleases } from '../../stores/releases'
import { useSession } from '../../stores/session'
import { useVersion } from '../../stores/version'
import AppIcon from '../AppIcon.vue'
import CalendarVersion from '../CalendarVersion.vue'
import KeyCap from '../KeyCap.vue'
import HeaderMenu from './HeaderMenu.vue'

const session = useSession()
const releases = useReleases()
const version = useVersion()
const route = useRoute()
const router = useRouter()
const menu = ref<InstanceType<typeof HeaderMenu>>()
const pane = ref<'menu' | 'help'>('menu')

// Who sees what: Workspace settings for admins, keys for whoever manages them,
// connecting a computer for people of the workspace (not guests).
const admin = computed(() => can('settings.manage'))
const keys = computed(() => can('keys.manage'))
const connect = computed(() => session.identity?.principal.kind !== 'agent' && !!myWorkspaceRole())
const canFeedback = computed(() => can('inbox.send'))
const newCount = computed(() => releases.newCount)

// ---------- System status: read each time the menu opens ----------
const probe = ref<SystemProbe | null>(null)
let probing = 0
async function check() {
  const turn = ++probing
  probe.value = null
  const health = api('/health').then(r => r.ok ? r.json() as Promise<SystemProbe['health']> : { status: 'error', db: 'down' }).catch(() => null)
  const ready = api('/ready').then(r => r.ok).catch(() => null)
  const answer = { health: await health, ready: await ready }
  if (turn === probing) probe.value = answer
}
const status = computed(() => summarizeStatus(probe.value))
const running = computed(() => version.value?.version ?? '')
const liveSince = computed(() => releases.history?.live_since ?? '')
const deployed = computed(() => liveSince.value ? `deployed ${relativeTime(liveSince.value, { long: true })}` : '')
// The row itself says it short; its name and tip say it in full.
const deployedShort = computed(() => liveSince.value ? `deployed ${relativeTime(liveSince.value)}` : '')
const statusName = computed(() => ['System status', status.value.label, status.value.detail, running.value && `version ${running.value}`, deployed.value].filter(Boolean).join(', '))

function opened() {
  pane.value = 'menu'
  void version.load()
  void releases.load()
  void check()
}

// ---------- Actions ----------
function go(path: string) { menu.value?.close(); void router.push(path) }
function showReleases(at?: string) { menu.value?.close(); run(at ? { name: 'releases', version: at } : { name: 'releases' }) }
function showShortcuts() { menu.value?.close(); run({ name: 'shortcuts' }) }

// ---------- Help & feedback ----------
const latest = computed(() => {
  const list = releases.history?.releases ?? []
  return list.find(release => release.version === releases.current) ?? list[0] ?? null
})
const benefits = computed(() => {
  const release = latest.value
  if (!release || !hasUsableNotes(release)) return []
  return release.notes.items.map(item => localizedNote(item)).filter(note => note.pill || note.benefit).slice(0, 3)
})
const headline = computed(() => latest.value && !benefits.value.length ? displayHeadline(latest.value) : '')
const recipient = ref<FeedbackRecipient | null | undefined>(undefined)
const recipientError = ref(false)
const draft = ref('')
const sending = ref(false)
const sent = ref('')
const sendError = ref('')
let sendKey = ''

async function openHelp() {
  pane.value = 'help'
  sent.value = ''
  sendError.value = ''
  void menu.value?.focusFirst('.pane-back')
  if (canFeedback.value && recipient.value === undefined) {
    try { recipient.value = await feedbackRecipient(); recipientError.value = false }
    catch { recipientError.value = true }
  }
}
function backToMenu() {
  pane.value = 'menu'
  void menu.value?.focusFirst('[data-help-item]')
}
async function send() {
  const text = draft.value.trim()
  if (!text || !recipient.value || sending.value) return
  sending.value = true
  sendError.value = ''
  sendKey ||= crypto.randomUUID()
  try {
    await sendFeedback(recipient.value.principal_id, feedbackBody(text, { path: route.fullPath, version: running.value }), sendKey)
    sent.value = `Sent to ${recipient.value.name}. Thank you.`
    draft.value = ''
    sendKey = ''
  } catch { sendError.value = 'Your feedback was not sent. Please try again.' }
  finally { sending.value = false }
}
function draftKeys(event: KeyboardEvent) {
  if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) { event.preventDefault(); void send() }
}
// A changed message is a new message.
function edited() { sendKey = ''; sent.value = '' }
</script>

<template>
  <HeaderMenu
    v-if="session.identity" id="app-menu" ref="menu" :role="pane === 'help' ? 'dialog' : 'menu'" :label="pane === 'help' ? 'Help and feedback' : 'App and workspace'"
    trigger-label="App and workspace" tip="Settings and help" class="app-menu" @open="opened"
  >
    <template #trigger><AppIcon name="gear" /></template>

    <template v-if="pane === 'menu'">
      <template v-if="admin || connect || keys">
        <button v-if="admin" class="hm-item" type="button" role="menuitem" tabindex="-1" @click="go('/settings/workspace')"><AppIcon name="gear" /><span class="hm-text">Workspace settings</span></button>
        <button v-if="connect" class="hm-item" type="button" role="menuitem" tabindex="-1" @click="go('/agents/register-agent')"><AppIcon name="monitor" /><span class="hm-text">Connect a computer</span></button>
        <button v-if="keys" class="hm-item" type="button" role="menuitem" tabindex="-1" @click="go('/settings/access/agents')"><AppIcon name="key" /><span class="hm-text">Agent keys</span></button>
        <hr class="hm-sep" role="separator" />
      </template>
      <button class="hm-item" type="button" role="menuitem" tabindex="-1" @click="showReleases()">
        <AppIcon name="history" /><span class="hm-text">Release history</span>
        <span v-if="newCount" class="hm-end new-count">{{ newCount }} new</span>
      </button>
      <button class="hm-item" type="button" role="menuitem" tabindex="-1" aria-keyshortcuts="?" @click="showShortcuts">
        <AppIcon name="keyboard" /><span class="hm-text">Keyboard shortcuts</span><span class="hm-end" aria-hidden="true"><KeyCap k="?" /></span>
      </button>
      <button class="hm-item" type="button" role="menuitem" tabindex="-1" aria-haspopup="dialog" data-help-item @click="openHelp">
        <AppIcon name="help" /><span class="hm-text">Help &amp; feedback</span><span class="hm-end" aria-hidden="true"><AppIcon name="chevron-right" :size="14" /></span>
      </button>
      <hr class="hm-sep" role="separator" />
      <button class="hm-item status-item" type="button" role="menuitem" tabindex="-1" :aria-label="statusName" data-tip="Check again" @click="check">
        <AppIcon name="pulse" />
        <span class="status-text">
          <span class="status-top"><span class="hm-text">System status</span><span class="status-state" :class="status.state"><i class="dot" aria-hidden="true" />{{ status.label }}</span></span>
          <span v-if="status.detail" class="status-sub">{{ status.detail }}</span>
          <span v-else-if="running || deployed" class="status-sub">
            <CalendarVersion v-if="running" :value="running" class="status-version" />
            <span v-if="running && deployed" aria-hidden="true">·</span>
            <time v-if="deployed" class="deployed" :datetime="liveSince" :title="`Deployed ${absoluteTime(liveSince)}`">{{ deployedShort }}</time>
          </span>
        </span>
      </button>
    </template>

    <div v-else class="help-pane">
      <div class="pane-head">
        <button class="icon-btn flat pane-back" type="button" aria-label="Back to the menu" @click="backToMenu"><AppIcon name="chevron-left" /></button>
        <h2 class="pane-title">Help &amp; feedback</h2>
      </div>
      <ul class="tips" aria-label="Quick help">
        <li><span>Search and run anything</span><span class="keys"><KeyCap k="mod" /><KeyCap k="K" /></span></li>
        <li><span>Go to a place</span><span class="keys"><KeyCap k="g" /><span class="then">then</span><KeyCap k="p" /></span></li>
        <li><span>Every shortcut on this page</span><span class="keys"><KeyCap k="?" /></span></li>
      </ul>

      <section v-if="latest" class="news" aria-labelledby="news-title">
        <div class="news-head">
          <h3 id="news-title" class="eyebrow">What’s new</h3>
          <CalendarVersion :value="latest.version" class="news-version" />
        </div>
        <ul v-if="benefits.length" class="benefits">
          <li v-for="note in benefits" :key="note.pill + note.benefit"><strong v-if="note.pill">{{ note.pill }}</strong><span v-if="note.benefit">{{ note.benefit }}</span></li>
        </ul>
        <p v-else-if="headline" class="news-headline">{{ headline }}</p>
        <button class="btn sm ghost news-open" type="button" @click="showReleases(latest.version)">See this release<AppIcon name="arrow" :size="14" /></button>
      </section>

      <section v-if="canFeedback" class="feedback" aria-labelledby="feedback-title">
        <h3 id="feedback-title" class="eyebrow">Send feedback</h3>
        <p v-if="recipient === null" class="quiet">You own this workspace, so your team’s feedback comes to you.</p>
        <p v-else-if="recipientError" class="quiet">Feedback can’t be sent right now.</p>
        <form v-else @submit.prevent="send">
          <label class="sr-only" for="feedback-text">Feedback for {{ recipient?.name ?? 'the workspace owner' }}</label>
          <textarea
            id="feedback-text" v-model="draft" class="field feedback-text" rows="3" :maxlength="FEEDBACK_MAX" :disabled="!recipient"
            :placeholder="recipient ? `What should ${recipient.name.split(' ')[0]} know?` : 'Loading…'" @input="edited" @keydown="draftKeys"
          />
          <div class="feedback-foot">
            <span class="quiet to">{{ recipient ? `Goes to ${recipient.name}’s inbox` : '' }}</span>
            <button class="btn primary sm" type="submit" :disabled="!draft.trim() || !recipient || sending"><AppIcon name="send" :size="14" />{{ sending ? 'Sending…' : 'Send' }}</button>
          </div>
          <p v-if="sent" class="sent" role="status"><AppIcon name="check" :size="14" />{{ sent }}</p>
          <p v-if="sendError" class="error" role="alert">{{ sendError }}</p>
        </form>
      </section>
    </div>
  </HeaderMenu>
</template>

<style scoped>
.new-count { color: var(--gold-ink); font-weight: 600; }
.hm-end :deep(.keycap) { margin: 0; }
/* System status: a row with a second, quieter line. */
.status-item { align-items: flex-start; padding-top: 9px; padding-bottom: 9px; }
.status-item > svg { margin-top: 1px; }
.status-text { flex: 1; min-width: 0; display: grid; gap: 3px; }
.status-top { display: flex; align-items: center; gap: 8px; }
.status-state { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; font-size: 12px; font-weight: 600; color: var(--ink-2); white-space: nowrap; }
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--ink-3); }
.status-state.healthy { color: var(--ok); }
.status-state.healthy .dot { background: var(--ok); }
.status-state.degraded { color: var(--warn-ink); }
.status-state.degraded .dot { background: var(--gold); }
.status-state.down { color: var(--danger); }
.status-state.down .dot { background: var(--danger); }
.status-sub { display: flex; align-items: center; gap: 6px; min-width: 0; font-size: 12px; color: var(--ink-3); white-space: nowrap; overflow: hidden; }
.status-version { flex-shrink: 0; font-size: 11.5px; color: var(--ink-2); }
.deployed { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
/* Help & feedback: a pane in the same surface, one step back to the menu. */
.help-pane { display: grid; gap: 4px; }
.pane-head { display: flex; align-items: center; gap: 6px; padding: 2px 2px 4px; }
.pane-back { width: 36px; height: 36px; }
.pane-title { margin: 0; font-size: 14px; font-weight: 650; color: var(--ink); }
.tips { display: grid; gap: 2px; margin: 0 0 4px; padding: 0 10px 8px; list-style: none; border-bottom: 1px solid var(--line); }
.tips li { display: flex; align-items: center; justify-content: space-between; gap: 10px; min-height: 30px; font-size: 13px; color: var(--ink-2); }
.keys { display: inline-flex; align-items: center; gap: 3px; flex-shrink: 0; }
.then { font-size: 11px; color: var(--ink-3); padding: 0 2px; }
.news, .feedback { display: grid; gap: 6px; padding: 8px 10px; }
.news { border-bottom: 1px solid var(--line); padding-bottom: 10px; }
.news-head { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; }
.news-head .eyebrow, .feedback .eyebrow { margin: 0; }
.news-version { font-size: 11.5px; color: var(--ink-3); }
.benefits { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.benefits li { display: grid; gap: 1px; font-size: 13px; line-height: 1.4; color: var(--ink-2); }
.benefits strong { font-weight: 650; color: var(--ink); }
.news-headline { font-size: 13px; line-height: 1.4; color: var(--ink); }
.news-open { justify-self: start; gap: 6px; margin-left: -8px; color: var(--teal-ink); }
.feedback form { display: grid; gap: 8px; }
.feedback-text { width: 100%; min-height: 76px; resize: vertical; font: inherit; font-size: 13.5px; line-height: 1.45; padding: 8px 10px; }
.feedback-foot { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.feedback-foot .btn { gap: 6px; flex-shrink: 0; }
.quiet { font-size: 12.5px; color: var(--ink-3); }
.to { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sent { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--ok); }
.error { font-size: 12.5px; color: var(--danger); }
@media (max-width: 600px), (pointer: coarse) {
  .pane-back { width: 44px; height: 44px; }
  .news-open, .feedback-foot .btn { min-height: 44px; }
  .feedback-text { font-size: 16px; }
}
</style>
