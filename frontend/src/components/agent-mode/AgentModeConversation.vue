<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — conversation / narration surface (restrained).
  Lines are TEMPLATED from structured state by the view — no LLM, no
  invented reassurance. This is the Agent Mode conversation surface, not
  global navigation: it never carries app chrome.

  `compact` (constrained widths, or when the ticket editor opens):
  the column collapses into a small lower-left dock showing only the most
  recent two or three lines plus listening/live state. The parent reserves a grid row for it,
  so its rectangle never intersects the scrolling delivery canvas.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

export interface NarrationLine {
  id: string
  role: 'system' | 'user'
  text: string
}

const props = withDefaults(
  defineProps<{
    lines: readonly NarrationLine[]
    live: boolean
    liveLabel: string
    compact?: boolean
  }>(),
  { compact: false },
)

const { t } = useI18n()
const visibleLines = computed(() => (props.compact ? props.lines.slice(-3) : props.lines))
</script>

<template>
  <aside
    class="am-conv"
    :class="{ 'am-conv--compact': compact }"
    :aria-label="t('agentMode.a11y.conversation')"
    :data-compact="compact ? 'true' : 'false'"
  >
    <div v-if="!compact" class="am-conv-head">
      <span class="am-eyebrow">{{ t('agentMode.narration.eyebrow') }}</span>
      <span class="am-conv-live" :class="{ 'is-live': live }">
        <i aria-hidden="true"></i>{{ liveLabel }}
      </span>
    </div>
    <ol class="am-conv-lines" :aria-label="compact ? t('agentMode.narration.latest') : t('agentMode.narration.eyebrow')">
      <li v-for="line in visibleLines" :key="line.id" class="am-conv-line" :class="`am-conv-line--${line.role}`">
        <span class="am-conv-role">{{ line.role === 'user' ? t('agentMode.narration.you') : 'Paimos' }}</span>
        <span class="am-conv-text">{{ line.text }}</span>
      </li>
    </ol>
    <div class="am-conv-controls">
      <slot name="controls" />
    </div>
    <div class="am-conv-dock">
      <span class="am-conv-compact-live" :class="{ 'is-live': live }">
        <i aria-hidden="true"></i>
        <strong>{{ t('agentMode.narration.feed') }}</strong>
        <small>{{ liveLabel }}</small>
      </span>
      <span class="am-conv-key-help">
        <strong>{{ t('agentMode.narration.keysTitle') }}</strong>
        <small>{{ t('agentMode.narration.keysHint') }}</small>
      </span>
    </div>
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
  display: flex;
  justify-content: space-between;
  gap: 10px;
  padding: 11px 12px;
  border: 1px solid var(--am-line);
  border-radius: 12px;
  background: var(--am-surface);
}
.am-conv-controls:empty { display: none; }
.am-conv-controls { margin-bottom: 10px; }
.am-conv-dock strong { font-size: 11px; font-weight: 600; }
.am-conv-dock small { font-size: 10px; color: var(--am-muted); line-height: 1.4; }
.am-conv-key-help,
.am-conv-compact-live { display: grid; gap: 2px; }
.am-conv-compact-live { grid-template-columns: auto auto; align-items: center; }
.am-conv-compact-live small { grid-column: 2; }
.am-conv-compact-live > i { width: 6px; height: 6px; border-radius: 50%; background: currentColor; color: var(--am-muted); }
.am-conv-compact-live.is-live > i { color: var(--am-green); box-shadow: 0 0 0 4px color-mix(in srgb, var(--am-green) 14%, transparent); }

/* ── Compact dock: lower-left in a reserved parent-grid row ── */
.am-conv--compact {
  width: min(300px, calc(100% - 28px));
  margin: 0 0 14px 14px;
  padding: 0;
  border-right: 0;
  background: transparent;
}
.am-conv--compact .am-conv-lines { flex: 0 0 auto; padding: 0 0 8px; overflow: visible; }
.am-conv--compact .am-conv-controls { margin-bottom: 8px; }
.am-conv--compact .am-conv-line { max-width: 100%; box-shadow: 0 8px 28px color-mix(in srgb, var(--am-ink) 14%, transparent); }
.am-conv--compact .am-conv-dock { box-shadow: 0 8px 28px color-mix(in srgb, var(--am-ink) 12%, transparent); }
/* Touch-size screens keep the approved desktop composition but reflow the
   compact surface into one full-width, in-flow status row. One authoritative
   latest line is enough here: the canvas already presents the selected
   delivery and the app header carries feed state. This preserves a usable
   canvas instead of allowing narration/control chrome to consume most of a
   phone viewport. */
@media (max-width: 640px) {
  .am-conv--compact { width: calc(100% - 28px); }
  .am-conv--compact .am-conv-lines > :not(:last-child),
  .am-conv--compact .am-conv-dock { display: none; }
  .am-conv--compact .am-conv-lines { padding-bottom: 0; }
}
</style>
