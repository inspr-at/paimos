<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useAgents } from '../../stores/agents'
import { useServiceTiers } from '../../stores/serviceTiers'
import { TIER_NAME, offeredTier, sameOwnership, tierOptions, tierPrice, tierReport, tierSpeed, type ServiceTier, type TierState, tierEstimate, estimateCostText, estimateTimeText } from '../../lib/serviceTier'
import TierGlyph from './TierGlyph.vue'
import KeyCap from '../KeyCap.vue'
import { useVisualViewport } from '../../lib/visualViewport'
const tiers = useServiceTiers(), agents = useAgents()
const context = tiers.dialog!
const frame = ref<HTMLDialogElement>(), why = ref(''), choice = ref<ServiceTier | null>(null), snapshot = ref<TierState>(), loading = ref(true), error = ref('')
const phone = window.matchMedia('(max-width: 720px)').matches
const position = ref({ left: '0px', top: '0px' })
let placedAnchor: DOMRect | undefined
let scrollFrame = 0
const mac = /Mac|iPhone|iPad/.test(navigator.platform)
useVisualViewport(frame)
const session = computed(() => agents.sessionById(context.session.id) ?? context.session)
const report = computed(() => tierReport(session.value, snapshot.value?.reports))
const options = computed(() => tierOptions(report.value))
const selected = computed(() => options.value.find(t => t.tier === choice.value))
const waiting = computed(() => !!tiers.busy[context.session.id])
const valid = computed(() => !loading.value && offeredTier(selected.value) && choice.value !== snapshot.value?.active_tier && !waiting.value &&
  (context.ask ? tiers.canAsk(session.value) && !!why.value.trim() && [...why.value.trim()].length <= 500 : !tiers.unavailable(session.value)))
const label = computed(() => !choice.value || choice.value === snapshot.value?.active_tier ? 'Pick another tier' : `${context.ask ? 'Ask for' : 'Switch to'} ${TIER_NAME[choice.value]} · ${tierPrice(selected.value)}`)
function close(restore = true) { tiers.close(restore) }
async function confirm() {
  if (!valid.value || !choice.value || !snapshot.value) return
  const id = context.session.id
  const ok = context.ask ? await tiers.ask(session.value, choice.value, why.value) : await tiers.change(session.value, context.name, choice.value, { snapshot: snapshot.value, withUndo: true })
  if (tiers.dialog !== context || session.value.id !== id) return
  if (ok) close()
  else error.value = tiers.errors[id] || 'The change was not confirmed. Close and check the session before trying again.'
}
function choose(key: ServiceTier) { if (!loading.value && !waiting.value && offeredTier(options.value.find(t => t.tier === key))) choice.value = key }
function keys(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopImmediatePropagation()
    const target = event.target
    if (target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))) { target.blur(); frame.value?.querySelector<HTMLButtonElement>('[data-cancel]')?.focus(); return }
    close(); return
  }
  const target = event.target as HTMLElement
  const field = target.matches('input,textarea,select') || target.isContentEditable
  if (event.key === 'Enter' && field && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) && !event.altKey && !event.shiftKey) { event.preventDefault(); event.stopPropagation(); void confirm(); return }
  if (event.key === 'Tab' && !event.metaKey && !event.ctrlKey && !event.altKey) {
    const stops = [...(frame.value?.querySelectorAll<HTMLElement>('button,input,[tabindex="0"]') ?? [])].filter(el => el.tabIndex >= 0 && !(el as HTMLButtonElement).disabled && el.getClientRects().length)
    const end = event.shiftKey ? stops[0] : stops[stops.length - 1]
    if (document.activeElement === end) { event.preventDefault(); (event.shiftKey ? stops[stops.length - 1] : stops[0])?.focus() }
    return
  }
  if (field || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return
  if (target.matches('[role=radio]') && ['ArrowDown', 'ArrowUp', 'ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) {
    event.preventDefault(); event.stopPropagation()
    const list = options.value.filter(offeredTier), index = list.findIndex(t => t.tier === choice.value)
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? list.length - 1 : (index + (['ArrowDown', 'ArrowRight'].includes(event.key) ? 1 : -1) + list.length) % list.length
    const key = list[next]?.tier
    if (key) { choose(key); frame.value?.querySelector<HTMLButtonElement>(`[data-option="${key}"]`)?.focus() }
  } else if (target.matches('[role=radio]') && event.key === 'Enter') { event.preventDefault(); event.stopPropagation(); void confirm() }

}
function outside(event: PointerEvent) {
  if (!frame.value?.contains(event.target as Node) && !context.anchor.contains(event.target as Node)) close(false)
}
function scroll(event: Event) {
  if (frame.value?.contains(event.target as Node) || phone) return
  const rect = context.anchor.getBoundingClientRect()
  if (placedAnchor && rect.top === placedAnchor.top && rect.left === placedAnchor.left && rect.bottom === placedAnchor.bottom && rect.right === placedAnchor.right) return
  close(false)
}
const initialWidth = innerWidth
const resize = () => { if (!phone || innerWidth !== initialWidth) close(false) }
// A draft belongs to one exact record/model/process. A late list read cannot
// redirect the operation to another generation while the picker is open.
watch(() => [session.value.id, session.value.project_id, session.value.model, session.value.stopped_at, session.value.archived_at, session.value.phase, session.value.process_ownership], () => {
  if (session.value.id !== context.session.id || session.value.project_id !== context.session.project_id || session.value.model !== context.session.model ||
    !sameOwnership(session.value.process_ownership, context.session.process_ownership) || session.value.stopped_at || session.value.archived_at || ['stopped', 'stopping'].includes(session.value.phase)) close(false)
})
onMounted(async () => {
  await nextTick()
  if (phone) frame.value?.showModal()
  else {
    frame.value?.show()
    const r = context.anchor.getBoundingClientRect(), width = Math.min(352, innerWidth - 16)
    const x = Math.max(8, Math.min(innerWidth - width - 8, r.left + r.width / 2 - width / 2))
    // Decide the side once, reserve the longest notes only for placement.
    const height = Math.min(520, innerHeight - 16)
    position.value = { left: `${Math.round(x)}px`, top: `${Math.round(r.bottom + 6 + height < innerHeight ? r.bottom + 6 : Math.max(8, r.top - height - 6))}px` }
  }
  document.addEventListener('pointerdown', outside, true)
  scrollFrame = requestAnimationFrame(() => { placedAnchor = context.anchor.getBoundingClientRect(); document.addEventListener('scroll', scroll, true) })
  window.addEventListener('resize', resize)
  window.addEventListener('keydown', escape, true)
  try {
    const answer = await tiers.load(session.value)
    if (tiers.dialog !== context) return
    if (!answer) throw new Error('The session changed. Reopen its tier picker.')
    snapshot.value = answer; choice.value = answer.active_tier
    if (answer.pending) { tiers.follow(session.value); close(); return }
  } catch (e) { error.value = e instanceof Error ? e.message : 'Tier information unavailable.' }
  finally { loading.value = false }
  await nextTick()
  frame.value?.querySelector<HTMLButtonElement>('[role=radio][aria-checked=true]')?.focus({ preventScroll: true })
})
function escape(event: KeyboardEvent) { if (event.key === 'Escape') keys(event) }
onBeforeUnmount(() => {
  cancelAnimationFrame(scrollFrame)
  document.removeEventListener('pointerdown', outside, true); document.removeEventListener('scroll', scroll, true)
  window.removeEventListener('resize', resize); window.removeEventListener('keydown', escape, true)
})
</script>
<template>
  <Teleport to="body">
    <dialog ref="frame" class="tier-pop pop floating" :class="{ sheet: phone }" :style="phone ? undefined : position" aria-label="Change tier" @keydown="keys" @cancel.prevent="close()">
      <header class="tp-head"><h2>{{ context.ask ? 'Ask for a tier' : 'Service tier' }}</h2><span>{{ context.name }}</span><small class="tp-est-header">Last run at it</small></header>
      <div class="tp-body">
        <div class="tp-list" role="radiogroup" aria-label="Service tier choices" :aria-busy="loading">
          <button v-for="(option, i) in options" :key="option.tier" type="button" role="radio" class="tp-option" :data-option="option.tier" :aria-checked="choice === option.tier" :aria-disabled="loading || !offeredTier(option) || waiting" :tabindex="choice === option.tier && offeredTier(option) ? 0 : -1" :aria-label="`${option.name}: ${!snapshot ? (loading ? 'vendor report pending' : 'vendor report unavailable') : offeredTier(option) ? `${tierSpeed(option)} · ${tierPrice(option)}; last run ${estimateCostText(tierEstimate(snapshot, option.tier))}, ${estimateTimeText(tierEstimate(snapshot, option.tier))}` : option.reason}`" @click="choose(option.tier)">
            <span class="tp-radio" aria-hidden="true" /><TierGlyph :active="option.tier" :report="report" :count="!offeredTier(option) ? i + 1 : undefined" :faint="!offeredTier(option)" />
            <span class="tp-text"><strong>{{ option.name }}<small v-if="snapshot?.active_tier === option.tier"> now</small></strong><small>{{ !snapshot ? (loading ? 'Checking vendor report…' : 'Vendor report unavailable') : offeredTier(option) ? `${tierSpeed(option)} · ${tierPrice(option)}` : option.reason }}</small></span>
            <span class="tp-estimate" :title="tierEstimate(snapshot, option.tier)?.basis"><template v-if="!snapshot">—</template><template v-else-if="offeredTier(option)">{{ estimateCostText(tierEstimate(snapshot, option.tier)) }}<small>{{ estimateTimeText(tierEstimate(snapshot, option.tier)) }}</small></template><template v-else>Not offered</template></span>
          </button>
        </div>
        <label v-if="context.ask" class="tp-field">Why (the person sees this)<input v-model="why" class="field" maxlength="500" :disabled="waiting" placeholder="For example: QA waits on this" /></label>
        <div class="tp-actions">
          <button type="button" class="btn sm ghost" data-cancel @click="close()">Cancel<KeyCap k="Esc" /></button>
          <button type="button" class="btn sm primary tp-go" :title="label" :disabled="!valid" @click="confirm"><span>{{ label }}</span><span class="tp-keys"><KeyCap v-if="context.ask" k="mod" /><KeyCap k="enter" /></span></button>
        </div>
        <div class="tp-notes" aria-live="polite">
          <p v-if="error" role="alert">{{ error }}</p>
          <template v-else>
            <p v-if="context.ask">A person decides. Nothing changes until the request is approved.</p>
            <p v-if="choice === snapshot?.active_tier">This session runs at <b>{{ choice ? TIER_NAME[choice] : 'an unreported tier' }}</b>. Pick another tier above.</p>
            <p v-else>Applies from the <b>{{ report?.applies === 'next_turn' ? 'next turn' : 'next run' }}</b>. The current run keeps its tier and cost.</p>
            <p v-if="offeredTier(selected)"><b>{{ selected.name }} · {{ tierPrice(selected) }} per token.</b> {{ selected.usage_multiplier ? `Capacity consumption: ${selected.usage_multiplier} times standard.` : 'Capacity multiplier not published.' }}</p>
            <p v-if="tierEstimate(snapshot, choice || 'default')" class="estimate-source" aria-label="Last-run estimate source">{{ tierEstimate(snapshot, choice || 'default')?.basis }}<template v-if="tierEstimate(snapshot, choice || 'default')?.run_id"> Run {{ tierEstimate(snapshot, choice || 'default')?.run_id }}.</template></p>
            <p>Same model and effort. Tools, tests and waits keep their time. Price scales cost; speed scales measured model time only.</p>
          </template>
        </div>
      </div>
    </dialog>
  </Teleport>
</template>
<style scoped>
.tier-pop{position:fixed;inset:auto;margin:0;width:352px;max-width:calc(100vw - 16px);max-height:calc(100dvh - 16px);padding:0;border:1px solid var(--glass-edge);color:var(--ink);overflow:auto;overscroll-behavior:contain;z-index:72}.tp-head{display:flex;align-items:baseline;gap:8px;padding:12px 14px 8px}.tp-head h2{font-size:14px;flex:none}.tp-head>span{font-size:12.5px;color:var(--ink-2);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.tp-list{display:grid;border-top:1px solid var(--line)}.tp-option{display:grid;grid-template-columns:14px 21px minmax(0,1fr) 86px;gap:8px;align-items:center;width:100%;height:52px;padding:0 14px;border:0;border-bottom:1px solid var(--line);border-radius:0;background:transparent;color:var(--ink);text-align:left}.tp-option[aria-disabled=true]{cursor:not-allowed;color:var(--ink-3)}.tp-option[aria-checked=true]{background:var(--row-selected)}.tp-option:not([aria-disabled=true]):hover{background:var(--row-hover)}.tp-option:focus-visible{box-shadow:inset 0 0 0 2px var(--aqua)}.tp-radio{width:14px;height:14px;border-radius:50%;background:var(--surface);box-shadow:inset 0 0 0 1.5px var(--line-2)}.tp-option[aria-checked=true] .tp-radio{box-shadow:inset 0 0 0 1.5px var(--teal),inset 0 0 0 4px var(--surface),inset 0 0 0 8px var(--teal)}.tp-text{display:grid;gap:2px;min-width:0}.tp-text strong{font-size:13px;font-weight:600}.tp-text small{font-size:11.5px;color:var(--ink-3);font-weight:450;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.tp-actions{display:flex;justify-content:flex-end;gap:6px;padding:10px 14px}.tp-go{width:222px;flex:none;height:32px;font-size:11.5px;white-space:nowrap;padding-inline:8px}.tp-actions>.btn{height:32px}.tp-go>span:first-child{min-width:0;overflow:hidden;text-overflow:ellipsis}.tp-keys{display:inline-flex;align-items:center;gap:2px;flex:none}.tp-notes{display:grid;gap:6px;padding:10px 14px 12px;border-top:1px solid var(--line);font-size:12px;line-height:1.45;color:var(--ink-2)}.tp-notes p{margin:0}.tp-field{display:grid;gap:4px;padding:10px 14px 0;font-size:12px;color:var(--ink-3)}.sheet{inset:var(--vv-top,0px) 0 auto 0;width:100%;max-width:none;height:var(--vv-h,100dvh);max-height:var(--vv-h,100dvh);border-radius:16px 16px 0 0;overflow:hidden}.sheet[open]{display:flex;flex-direction:column}.sheet::backdrop{background:var(--scrim)}.sheet .tp-head{padding:16px}.sheet .tp-body{display:flex;flex:1;flex-direction:column;min-height:0;overflow:hidden}.sheet .tp-notes{align-content:start;flex:1;min-height:0;overflow:auto;order:2}.sheet .tp-actions{order:3;flex:none;border-top:1px solid var(--line);padding:12px 16px calc(16px + env(safe-area-inset-bottom))}.sheet .tp-actions>.btn{min-height:44px}.sheet .tp-go{flex:1;width:auto}.sheet .tp-list,.sheet .tp-field{flex:none}
</style>

<style scoped>
.tp-est-header{flex:none;white-space:nowrap;font-size:9px;color:var(--ink-3);text-transform:uppercase}.tp-head>span{flex:1;min-width:0}.tp-estimate{display:grid;gap:2px;text-align:right;font-size:10.5px;font-variant-numeric:tabular-nums;white-space:nowrap}.tp-estimate small{font-size:9.5px;color:var(--ink-3);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;line-height:1.2}
</style>
