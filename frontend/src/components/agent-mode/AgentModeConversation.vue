<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — conversation / narration surface (restrained).
  Lines are TEMPLATED from structured state by the view — no LLM, no
  invented reassurance. PAI-808 owns voice: it plugs in through the
  `listening` prop and the `#dock` slot without changing this contract.
-->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'

export interface NarrationLine {
  id: string
  role: 'system' | 'user'
  text: string
}

defineProps<{
  lines: readonly NarrationLine[]
  /** Voice seam (PAI-808): when true a listening dock is shown. */
  listening?: boolean
  live: boolean
  liveLabel: string
}>()

const { t } = useI18n()
</script>

<template>
  <aside class="am-conv" :aria-label="t('agentMode.a11y.conversation')">
    <div class="am-conv-head">
      <span class="am-eyebrow">{{ t('agentMode.narration.eyebrow') }}</span>
      <span class="am-conv-live" :class="{ 'is-live': live }">
        <i aria-hidden="true"></i>{{ liveLabel }}
      </span>
    </div>
    <ol class="am-conv-lines" :aria-label="t('agentMode.narration.eyebrow')">
      <li v-for="line in lines" :key="line.id" class="am-conv-line" :class="`am-conv-line--${line.role}`">
        <span class="am-conv-role">{{ line.role === 'user' ? t('agentMode.narration.you') : 'Paimos' }}</span>
        <span class="am-conv-text">{{ line.text }}</span>
      </li>
    </ol>
    <slot name="dock">
      <div class="am-conv-dock" :class="{ 'is-listening': listening }">
        <strong>{{ listening ? t('agentMode.narration.listening') : t('agentMode.narration.keysTitle') }}</strong>
        <small>{{ listening ? t('agentMode.narration.listeningHint') : t('agentMode.narration.keysHint') }}</small>
        <small v-if="!listening" class="am-conv-seam">{{ t('agentMode.narration.voiceSeam') }}</small>
      </div>
    </slot>
  </aside>
</template>

<style scoped>
.am-conv {
  display: flex;
  min-width: 0;
  min-height: 0;
  flex-direction: column;
  padding: 18px 16px 16px;
  border-right: 1px solid var(--am-line);
  background: color-mix(in srgb, var(--am-ink) 3%, var(--am-shell));
}
.am-conv-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.am-eyebrow {
  color: var(--am-muted);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
.am-conv-live { display: inline-flex; align-items: center; gap: 6px; font-size: 11px; color: var(--am-muted); }
.am-conv-live > i { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.am-conv-live.is-live { color: var(--am-green); }
.am-conv-live.is-live > i { box-shadow: 0 0 0 4px color-mix(in srgb, var(--am-green) 14%, transparent); }

.am-conv-lines {
  display: flex;
  flex: 1;
  flex-direction: column;
  justify-content: flex-end;
  gap: 10px;
  margin: 0;
  padding: 22px 0 16px;
  list-style: none;
  overflow: auto;
}
.am-conv-line {
  max-width: 94%;
  padding: 9px 11px;
  border-radius: 12px 12px 12px 4px;
  background: var(--am-surface);
  box-shadow: 0 1px 2px color-mix(in srgb, var(--am-ink) 8%, transparent);
  font-size: 12px;
  line-height: 1.45;
}
.am-conv-line--user { align-self: flex-end; border-radius: 12px 12px 4px 12px; background: color-mix(in srgb, var(--am-ink) 88%, var(--am-surface)); color: var(--am-surface); }
.am-conv-role { display: block; margin-bottom: 3px; font-size: 10px; color: var(--am-muted); }
.am-conv-line--user .am-conv-role { color: inherit; opacity: 0.7; }
.am-conv-text { display: block; }

.am-conv-dock {
  display: grid;
  gap: 2px;
  padding: 11px 12px;
  border: 1px solid var(--am-line);
  border-radius: 12px;
  background: var(--am-surface);
}
.am-conv-dock strong { font-size: 12px; font-weight: 600; }
.am-conv-dock small { font-size: 11px; color: var(--am-muted); line-height: 1.4; }
.am-conv-seam { margin-top: 4px; }
.am-conv-dock.is-listening { border-color: var(--am-green); }
</style>
