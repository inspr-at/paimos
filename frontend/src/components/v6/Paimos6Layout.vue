<!--
  PAI-854 — isolated Paimos 6 preview shell. This is intentionally not the
  production AppLayout or AgentModeLayout: no left rail and no CRUD chrome.
-->
<script setup lang="ts">
import { ArrowLeft, Command, FlaskConical } from 'lucide-vue-next'
</script>

<template>
  <div class="p6-shell" data-shell="v6">
    <header class="p6-shell-header">
      <a class="p6-back" href="/" aria-label="Return to the current 5.x dashboard">
        <ArrowLeft :size="16" aria-hidden="true" />
        <span>5.x dashboard</span>
      </a>
      <div class="p6-wordmark" aria-label="Paimos six preview">
        <span class="p6-mark" aria-hidden="true">P</span>
        <span>Paimos</span>
        <span class="p6-six">6 preview</span>
      </div>
      <div class="p6-shell-tools">
        <span class="p6-fixture-chip">
          <FlaskConical :size="14" aria-hidden="true" />
          Local fixture · non-live
        </span>
        <span
          class="p6-command-mount"
          aria-label="Command palette visual mock only; shortcut behavior is unchanged in this preview"
        >
          <span>Visual mock</span>
          <Command :size="13" aria-hidden="true" />
          <kbd>⌘ K</kbd>
        </span>
      </div>
    </header>
    <div class="p6-shell-content">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.p6-shell {
  --p6-ink: #1d2723;
  --p6-muted: #67726c;
  --p6-line: #dce4df;
  --p6-moss: #2f6b52;
  min-height: 100vh;
  color: var(--p6-ink);
  background:
    radial-gradient(circle at 16% -10%, rgba(205, 225, 213, 0.55), transparent 31rem),
    #f7f8f5;
  font-family: "DM Sans", system-ui, sans-serif;
}

.p6-shell-header {
  position: relative;
  z-index: 4;
  display: grid;
  grid-template-columns: 1fr auto 1fr;
  align-items: center;
  min-height: 66px;
  padding: 0 34px;
  border-bottom: 1px solid rgba(193, 205, 198, 0.72);
  background: rgba(247, 248, 245, 0.82);
  backdrop-filter: blur(18px);
}

.p6-back,
.p6-wordmark,
.p6-shell-tools,
.p6-fixture-chip,
.p6-command-mount {
  display: inline-flex;
  align-items: center;
}

.p6-back {
  justify-self: start;
  gap: 7px;
  min-width: 30px;
  min-height: 30px;
  color: var(--p6-muted);
  font-size: 12px;
  font-weight: 600;
  text-decoration: none;
}

.p6-back:hover { color: var(--p6-ink); }
.p6-back:focus-visible { outline: 3px solid rgba(47, 107, 82, 0.28); outline-offset: 5px; border-radius: 4px; }

.p6-wordmark {
  gap: 9px;
  font-family: "Bricolage Grotesque", "DM Sans", sans-serif;
  font-size: 17px;
  font-weight: 600;
  letter-spacing: -0.025em;
}

.p6-mark {
  display: grid;
  width: 28px;
  height: 28px;
  place-items: center;
  border-radius: 9px;
  color: #f8fbf8;
  background: #223f32;
  font-size: 14px;
}

.p6-six {
  padding-left: 8px;
  border-left: 1px solid var(--p6-line);
  color: var(--p6-muted);
  font-family: "DM Sans", sans-serif;
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.09em;
  text-transform: uppercase;
}

.p6-shell-tools {
  justify-self: end;
  gap: 8px;
}

.p6-fixture-chip,
.p6-command-mount {
  gap: 6px;
  min-height: 30px;
  border: 1px solid var(--p6-line);
  border-radius: 9px;
  color: var(--p6-muted);
  background: rgba(255, 255, 255, 0.64);
  font-size: 10px;
  font-weight: 650;
  letter-spacing: 0.025em;
}

.p6-fixture-chip { padding: 0 10px; }
.p6-command-mount { padding: 0 8px; }
.p6-command-mount kbd { font: 600 10px/1 "JetBrains Mono", monospace; }
.p6-shell-content { min-height: calc(100vh - 66px); }

@media (max-width: 680px) {
  .p6-shell-header {
    grid-template-columns: auto 1fr auto;
    min-height: 58px;
    padding: 0 14px;
  }
  .p6-back span,
  .p6-fixture-chip { display: none; }
  .p6-back { justify-content: center; }
  .p6-wordmark { justify-self: center; font-size: 15px; gap: 7px; }
  .p6-mark { width: 25px; height: 25px; border-radius: 8px; }
  .p6-six { font-size: 9px; }
  .p6-shell-content { min-height: calc(100vh - 58px); }
}

@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after { scroll-behavior: auto !important; transition-duration: 0.01ms !important; animation-duration: 0.01ms !important; }
}
</style>
