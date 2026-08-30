<script setup lang="ts">
import { CircleAlert, CirclePause, Link2, Mail, MessageCircle, Octagon, Radio, ShieldCheck } from 'lucide-vue-next'

import type { Paimos6SessionFixture } from '@/v6/sessionFixture'

defineProps<{
  session: Paimos6SessionFixture
  selected: boolean
}>()

defineEmits<{
  select: [id: string]
  clear: []
  action: [label: string, id: string]
}>()
</script>

<template>
  <article
    class="p6-session-card"
    :class="{ 'is-selected': selected, 'needs-attention': session.attention }"
    :aria-labelledby="`p6-session-${session.id}`"
  >
    <button
      type="button"
      class="p6-card-select"
      :aria-pressed="selected"
      :aria-label="`${selected ? 'Selected' : 'Select'} ${session.title}, target ${session.agent}`"
      @click="$emit('select', session.id)"
      @keydown.escape.prevent="$emit('clear')"
    >
      <span class="p6-card-topline">
        <span class="p6-session-mode">
          <ShieldCheck v-if="session.mode === 'managed'" :size="13" aria-hidden="true" />
          <Radio v-else :size="13" aria-hidden="true" />
          {{ session.mode === 'managed' ? 'Managed session' : 'Unmanaged CLI' }}
        </span>
        <span v-if="selected" class="p6-state-flag p6-state-selected">Selected</span>
        <span v-if="session.attention" class="p6-state-flag p6-state-attention">
          <CircleAlert :size="12" aria-hidden="true" /> Needs you
        </span>
      </span>

      <span class="p6-card-heading">
        <span :id="`p6-session-${session.id}`" class="p6-card-title">{{ session.title }}</span>
        <span class="p6-agent">{{ selected ? 'Selected target · Amy →' : 'Agent ·' }} {{ session.agent }}</span>
      </span>
      <span class="p6-summary">{{ session.summary }}</span>

      <span v-if="session.attentionReason" class="p6-attention-reason">
        <CircleAlert :size="14" aria-hidden="true" />
        {{ session.attentionReason }}
      </span>

      <span class="p6-card-facts">
        <span><span class="p6-status-dot" :class="`is-${session.status}`" aria-hidden="true"></span>{{ session.statusLabel }}</span>
        <span><Mail :size="13" aria-hidden="true" /> {{ session.unread }} unread · {{ session.inboxSummary }}</span>
        <span v-if="session.node"><Link2 :size="13" aria-hidden="true" /> {{ session.node.label }}</span>
        <span v-else class="p6-loose"><Link2 :size="13" aria-hidden="true" /> Loose session · no node attached</span>
      </span>
    </button>

    <div class="p6-card-actions" :aria-label="`Fixture controls for ${session.title}`">
      <button
        type="button"
        :disabled="!session.capabilities.directSteer"
        @click="$emit('action', 'Steer', session.id)"
      >
        <MessageCircle :size="13" aria-hidden="true" />
        {{ session.capabilities.directSteer ? 'Steer' : 'Direct steer unavailable' }}
      </button>
      <button
        type="button"
        :disabled="!session.capabilities.interrupt"
        @click="$emit('action', 'Interrupt', session.id)"
      >
        <CirclePause :size="13" aria-hidden="true" /> Interrupt
      </button>
      <button
        type="button"
        :disabled="!session.capabilities.stop"
        @click="$emit('action', 'Stop', session.id)"
      >
        <Octagon :size="13" aria-hidden="true" /> Stop
      </button>
      <button
        v-if="session.mode === 'unmanaged' && session.capabilities.paimosSteer"
        type="button"
        class="p6-paimos-nudge"
        @click="$emit('action', 'Paimos-steer nudge', session.id)"
      >
        Ask Paimos to steer · preview
      </button>
    </div>
    <p v-if="session.mode === 'unmanaged'" class="p6-unmanaged-note">
      Paimos does not own this process. Interrupt and stop are unavailable; use the Paimos-steer nudge.
    </p>
  </article>
</template>

<style scoped>
.p6-session-card {
  position: relative;
  overflow: hidden;
  border: 1px solid var(--p6-line, #dce4df);
  border-radius: 18px;
  background: rgba(255, 255, 255, 0.76);
  box-shadow: 0 12px 32px rgba(35, 54, 44, 0.035);
  transition: border-color 160ms ease, box-shadow 160ms ease, transform 160ms ease;
}

.p6-session-card::before {
  position: absolute;
  inset: 0 auto 0 0;
  width: 3px;
  background: transparent;
  content: "";
}

.p6-session-card.needs-attention::before { background: #b86a38; }
.p6-session-card.is-selected { border-color: #5b8b74; box-shadow: 0 0 0 3px rgba(47, 107, 82, 0.11), 0 16px 40px rgba(35, 54, 44, 0.07); }

.p6-card-select {
  display: flex;
  width: 100%;
  flex-direction: column;
  gap: 12px;
  padding: 20px 20px 15px;
  border: 0;
  color: inherit;
  background: transparent;
  text-align: left;
}
.p6-card-select:hover { background: rgba(246, 249, 246, 0.78); }
.p6-card-select:focus-visible,
.p6-card-actions button:focus-visible {
  outline: 3px solid rgba(47, 107, 82, 0.3);
  outline-offset: -3px;
}

.p6-card-topline,
.p6-card-heading,
.p6-session-mode,
.p6-state-flag,
.p6-card-facts > span,
.p6-card-actions button {
  display: flex;
  align-items: center;
}
.p6-card-topline { width: 100%; gap: 7px; min-height: 22px; }
.p6-session-mode { gap: 5px; margin-right: auto; color: #59655e; font-size: 10px; font-weight: 700; letter-spacing: 0.08em; text-transform: uppercase; }
.p6-state-flag { gap: 4px; padding: 3px 7px; border: 1px solid; border-radius: 999px; font-size: 10px; font-weight: 700; }
.p6-state-selected { border-color: #b6d2c2; color: #285b45; background: #eef7f1; }
.p6-state-attention { border-color: #e8c4aa; color: #89471f; background: #fff5ed; }
.p6-card-heading { width: 100%; justify-content: space-between; gap: 12px; }
.p6-card-title { font-family: "Bricolage Grotesque", "DM Sans", sans-serif; font-size: 18px; font-weight: 600; letter-spacing: -0.025em; }
.p6-agent { flex: 0 0 auto; color: #536159; font: 500 10px/1.2 "JetBrains Mono", monospace; }
.p6-summary { min-height: 42px; color: #606e66; font-size: 12.5px; line-height: 1.62; }
.p6-attention-reason { display: flex; align-items: flex-start; gap: 8px; padding: 10px 11px; border-radius: 10px; color: #7b4828; background: #fff7f1; font-size: 11.5px; line-height: 1.45; }
.p6-attention-reason svg { flex: 0 0 auto; margin-top: 1px; }
.p6-card-facts { display: grid; gap: 7px; color: #59655e; font-size: 10.5px; }
.p6-card-facts > span { gap: 6px; }
.p6-status-dot { width: 7px; height: 7px; border-radius: 50%; background: #75827b; }
.p6-status-dot.is-working { background: #3f8263; }
.p6-status-dot.is-waiting { border: 2px solid #b86a38; background: transparent; }
.p6-loose { font-weight: 650; color: #4f6258; }
.p6-card-actions { display: flex; flex-wrap: wrap; gap: 6px; padding: 11px 15px 14px; border-top: 1px solid #edf1ee; }
.p6-card-actions button { gap: 5px; min-height: 30px; padding: 0 9px; border: 1px solid #dce4df; border-radius: 8px; color: #58665e; background: #fbfcfa; font: 600 10.5px/1 "DM Sans", sans-serif; }
.p6-card-actions button:hover:not(:disabled) { border-color: #a9bdb1; color: #285b45; background: #f2f7f3; }
.p6-card-actions button:disabled { border-style: dashed; color: #59655e; background: #f5f7f5; cursor: not-allowed; }
.p6-card-actions .p6-paimos-nudge { border-color: #b9cdbf; color: #285b45; background: #f0f6f2; }
.p6-unmanaged-note { padding: 0 15px 14px; color: #67756d; font-size: 10.5px; line-height: 1.45; }

@media (max-width: 680px) {
  .p6-card-select { padding: 17px 16px 14px; }
  .p6-card-heading { align-items: flex-start; flex-direction: column; gap: 5px; }
  .p6-summary { min-height: 0; }
  .p6-card-actions { padding-inline: 12px; }
}

@media (prefers-reduced-motion: reduce) {
  .p6-session-card { transition: none; }
}
</style>
