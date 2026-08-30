<script setup lang="ts">
import { computed, ref } from 'vue'
import { CircleDot, Inbox, Layers3, RadioTower, WifiOff } from 'lucide-vue-next'

import Paimos6SessionCard from '@/components/v6/Paimos6SessionCard.vue'
import Paimos6TalkDoor from '@/components/v6/Paimos6TalkDoor.vue'
import {
  PAIMOS6_PREVIEW_CONTRACT,
  PAIMOS6_SESSION_FIXTURES,
} from '@/v6/sessionFixture'

const selectedId = ref<string | null>(PAIMOS6_PREVIEW_CONTRACT.initialSelection)
const doorOpen = ref(false)
const statusMessage = ref('Local fixture ready. No session selected; preview utterances would target Paimos, but nothing can be sent.')

const selectedSession = computed(() =>
  PAIMOS6_SESSION_FIXTURES.find((session) => session.id === selectedId.value) ?? null,
)
const unreadTotal = computed(() => PAIMOS6_SESSION_FIXTURES.reduce((total, session) => total + session.unread, 0))
const attentionTotal = computed(() => PAIMOS6_SESSION_FIXTURES.filter((session) => session.attention).length)

function selectSession(id: string) {
  selectedId.value = id
  const session = PAIMOS6_SESSION_FIXTURES.find((candidate) => candidate.id === id)
  statusMessage.value = session
    ? `${session.title} selected. Preview target is ${session.agent}; nothing was sent.`
    : 'Selection unavailable.'
}

function clearSelection() {
  selectedId.value = null
  statusMessage.value = 'Selection cleared. Preview utterances would target Paimos; this fixture cannot record or send them.'
}

function previewAction(label: string, id: string) {
  const session = PAIMOS6_SESSION_FIXTURES.find((candidate) => candidate.id === id)
  statusMessage.value = `${label} is a local fixture no-op for ${session?.title ?? 'this session'}. No request was sent.`
}
</script>

<template>
  <div class="p6-preview-root">
  <main class="p6-home" aria-labelledby="p6-title" :inert="doorOpen || undefined">
    <section class="p6-intro">
      <div class="p6-source-framing" aria-label="Paimos sources">
        <span class="p6-source is-active"><CircleDot :size="12" aria-hidden="true" /> Paimos · active source</span>
        <span class="p6-source is-reserved">Pharos · reserved source</span>
        <span class="p6-source is-reserved">Janus · reserved source</span>
      </div>
      <div class="p6-title-row">
        <div>
          <p class="p6-kicker">Your agent loop, without the CRUD chrome</p>
          <h1 id="p6-title">Good morning. Here’s what needs you.</h1>
          <p class="p6-deck">
            Product-session-shaped fixtures for the six home. These are not relabelled 5.x issues, deliveries, runs, or harness sessions.
          </p>
        </div>
        <dl class="p6-glance" aria-label="Fixture session summary">
          <div><dt>Attention</dt><dd>{{ attentionTotal }} <span>session</span></dd></div>
          <div><dt>Inbox</dt><dd>{{ unreadTotal }} <span>unread</span></dd></div>
          <div><dt>Source</dt><dd><RadioTower :size="16" aria-hidden="true" /> fixture</dd></div>
        </dl>
      </div>
    </section>

    <section class="p6-sessions" aria-labelledby="p6-sessions-title">
      <div class="p6-section-head">
        <div>
          <p class="p6-section-kicker"><Layers3 :size="13" aria-hidden="true" /> Session home</p>
          <h2 id="p6-sessions-title">Near you now</h2>
        </div>
        <div class="p6-selection-copy">
          <template v-if="selectedSession">
            <span>Selected agent target · <strong>{{ selectedSession.agent }}</strong></span>
            <button type="button" @click="clearSelection">Clear selection <kbd>Esc on card</kbd></button>
          </template>
          <span v-else>No selection · preview target <strong>Paimos</strong></span>
        </div>
      </div>

      <div class="p6-session-grid">
        <Paimos6SessionCard
          v-for="session in PAIMOS6_SESSION_FIXTURES"
          :key="session.id"
          :session="session"
          :selected="selectedId === session.id"
          @select="selectSession"
          @clear="clearSelection"
          @action="previewAction"
        />
      </div>
    </section>

    <section class="p6-honesty" aria-labelledby="p6-honesty-title">
      <WifiOff :size="19" aria-hidden="true" />
      <div>
        <h2 id="p6-honesty-title">Live session seam unavailable</h2>
        <p>
          This deterministic preview has no session API, live inbox, native app, or push capability. At 390px it is the same responsive mobile-web stub—not a native client. Empty and unavailable live states land here without inventing data.
        </p>
      </div>
      <span>Fixture only</span>
    </section>

    <p class="p6-status" role="status" aria-live="polite" aria-atomic="true">
      <Inbox :size="13" aria-hidden="true" /> {{ statusMessage }}
    </p>

  </main>

  <Paimos6TalkDoor
      v-model:open="doorOpen"
      :target-agent="selectedSession?.agent ?? null"
      @status="statusMessage = $event"
    />
  </div>
</template>

<style scoped>
.p6-preview-root { min-height: 100%; }
.p6-home {
  width: min(1180px, calc(100% - 64px));
  margin: 0 auto;
  padding: 70px 0 44px;
}
.p6-intro { max-width: 1080px; margin: 0 auto; }
.p6-source-framing { display: flex; flex-wrap: wrap; gap: 7px; }
.p6-source { display: inline-flex; align-items: center; gap: 5px; padding: 5px 9px; border: 1px solid #d8e2dc; border-radius: 999px; font-size: 9.5px; font-weight: 700; letter-spacing: 0.055em; text-transform: uppercase; }
.p6-source.is-active { color: #2d6048; background: #edf6f0; }
.p6-source.is-reserved { border-style: dashed; color: #59655e; background: rgba(255, 255, 255, 0.5); }
.p6-title-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: end; gap: 70px; margin-top: 30px; }
.p6-kicker,
.p6-section-kicker { color: #5d7467; font-size: 10px; font-weight: 750; letter-spacing: 0.1em; text-transform: uppercase; }
.p6-title-row h1 { max-width: 720px; margin-top: 9px; font-family: "Bricolage Grotesque", "DM Sans", sans-serif; font-size: clamp(35px, 5vw, 59px); font-weight: 500; line-height: 1.04; letter-spacing: -0.055em; }
.p6-deck { max-width: 680px; margin-top: 18px; color: #66736c; font-size: 13px; line-height: 1.7; }
.p6-glance { display: grid; min-width: 250px; grid-template-columns: repeat(3, 1fr); gap: 0; padding-bottom: 5px; }
.p6-glance div { padding: 0 16px; border-left: 1px solid #dbe3de; }
.p6-glance dt { color: #59655e; font-size: 9px; font-weight: 700; letter-spacing: 0.07em; text-transform: uppercase; }
.p6-glance dd { display: flex; align-items: center; gap: 5px; margin-top: 5px; color: #31443a; font: 500 18px/1 "Bricolage Grotesque", sans-serif; }
.p6-glance dd span { color: #59655e; font: 500 9px/1 "DM Sans", sans-serif; }
.p6-sessions { margin-top: 78px; }
.p6-section-head { display: flex; align-items: end; justify-content: space-between; gap: 24px; margin-bottom: 17px; }
.p6-section-kicker { display: flex; align-items: center; gap: 6px; }
.p6-section-head h2 { margin-top: 4px; font-family: "Bricolage Grotesque", "DM Sans", sans-serif; font-size: 23px; font-weight: 600; letter-spacing: -0.035em; }
.p6-selection-copy { display: flex; align-items: center; gap: 12px; color: #59655e; font-size: 10.5px; }
.p6-selection-copy strong { color: #315b47; font-family: "JetBrains Mono", monospace; font-size: 10px; }
.p6-selection-copy button { padding: 5px 8px; border: 1px solid #d7e0da; border-radius: 7px; color: #53645b; background: #fbfcfa; font-size: 10px; }
.p6-selection-copy button:hover { border-color: #aabdb1; }
.p6-selection-copy button:focus-visible { outline: 3px solid rgba(47, 107, 82, 0.3); outline-offset: 3px; }
.p6-selection-copy kbd { margin-left: 4px; color: #59655e; font: 500 9px/1 "JetBrains Mono", monospace; }
.p6-session-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px; }
.p6-honesty { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 14px; margin-top: 17px; padding: 16px 18px; border: 1px dashed #cad6cf; border-radius: 14px; color: #68756e; background: rgba(252, 253, 250, 0.58); }
.p6-honesty h2 { color: #4d5b53; font-size: 11px; font-weight: 700; }
.p6-honesty p { margin-top: 3px; font-size: 10.5px; line-height: 1.5; }
.p6-honesty > span { padding: 4px 7px; border: 1px solid #d5ded9; border-radius: 999px; font-size: 9px; font-weight: 700; text-transform: uppercase; }
.p6-status { display: flex; align-items: center; gap: 6px; min-height: 22px; margin-top: 14px; color: #59655e; font-size: 10px; }

@media (max-width: 940px) {
  .p6-home { width: min(100% - 36px, 760px); padding-top: 48px; }
  .p6-title-row { grid-template-columns: 1fr; gap: 28px; }
  .p6-glance { width: min(100%, 360px); }
  .p6-session-grid { grid-template-columns: 1fr; }
}

@media (max-width: 680px) {
  .p6-home { width: calc(100% - 28px); padding: 34px 0 86px; }
  .p6-source-framing { gap: 5px; }
  .p6-source { padding: 4px 7px; font-size: 8.5px; }
  .p6-title-row { margin-top: 22px; }
  .p6-title-row h1 { font-size: clamp(34px, 11vw, 44px); }
  .p6-deck { font-size: 12px; }
  .p6-glance { min-width: 0; }
  .p6-glance div { padding: 0 10px; }
  .p6-sessions { margin-top: 52px; }
  .p6-section-head { align-items: flex-start; flex-direction: column; }
  .p6-selection-copy { width: 100%; align-items: flex-start; justify-content: space-between; }
  .p6-selection-copy span { max-width: 220px; }
  .p6-honesty { grid-template-columns: auto 1fr; align-items: start; }
  .p6-honesty > span { grid-column: 2; justify-self: start; }
}
</style>
