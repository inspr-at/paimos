// SPDX-License-Identifier: AGPL-3.0-only
import { onBeforeUnmount, onMounted, ref, shallowRef, watch, type Ref } from 'vue'
import type { DeliveryActions } from './deliveryChanges'
import type { PlanningItem, PlanningRelease } from './deliveryPlanning'
import { moveAdvice, PICKUP_MS, refusal, submitMove, visibleGap, type MoveSlot, type MoveSubject, type MoveTarget } from './deliveryMoves'

export function usePlanningMoves(options: { root: Ref<HTMLElement | undefined>; identity: () => string; releases: () => PlanningRelease[]; items: () => PlanningItem[]; scroll: () => HTMLElement | null; agent: () => boolean; allowed: () => boolean; stale: () => boolean; actions?: DeliveryActions }) {
  const draft = shallowRef<MoveSubject | null>(null), initial = ref<'append'|'top'|'after'>('append')
  const drag = shallowRef<MoveSubject | null>(null), target = shallowRef<MoveTarget | null>(null)
  const feedback = ref(''), busy = ref(false), point = ref({ x: 0, y: 0 }), line = ref<{ x: number; y: number; width: number } | null>(null)
  const highlight = ref(''), refused = ref(false), cursor = ref('')
  let pending: { subject: MoveSubject; pointer: number; x: number; y: number; touch: boolean; identity: string } | null = null
  let disposed = false
  let timer: ReturnType<typeof setTimeout> | undefined, frame = 0, swallow = false, draftIdentity = ''
  const rows = options.items
  function subject(element: Element | null): MoveSubject | null {
    const itemId = element?.closest<HTMLElement>('[data-planning-item]')?.dataset.planningItem
    if (itemId) { const record = rows().find(r => r.item_id === itemId); return record ? { kind: 'item', record: { ...record } } : null }
    const releaseId = element?.closest<HTMLElement>('[data-planning-release]')?.dataset.planningRelease
    const record = options.releases().find(r => r.release_id === releaseId)
    return record ? { kind: 'release', record: { ...record } } : null
  }
  function open(value: MoveSubject, position: 'append'|'top'|'after' = 'append') {
    if (busy.value || options.stale()) return
    draftIdentity = options.identity(); draft.value = value; initial.value = position; feedback.value = ''
  }
  function openItem(id: string) { const record = rows().find(r => r.item_id === id); if (record) open({ kind: 'item', record: { ...record } }) }
  function current(value: MoveSubject) {
    const record = value.kind === 'item' ? rows().find(r => r.item_id === value.record.item_id) : options.releases().find(r => r.release_id === value.record.release_id)
    return !options.stale() && record?.revision === value.record.revision && record.project_id === value.record.project_id
  }
  function advice(value: MoveSubject, to: MoveTarget) {
    const reason = moveAdvice(value, to, options.releases(), options.agent(), options.allowed())
    if (reason) return reason
    if (options.agent() && value.kind === 'item' && (to.release?.release_id ?? '') === (value.record.release_id ?? '')) {
      const anchor = rows().find(r => r.item_id === (to.slot.after_id ?? to.slot.before_id))
      if (anchor?.rank && value.record.rank && (to.slot.after_id ? anchor.rank < value.record.rank : anchor.rank <= value.record.rank)) return 'Agents can only move work later. Ask a person.'
    }
    return ''
  }
  async function save(value: MoveSubject, to: MoveTarget, identity: string) {
    if (busy.value) return
    if (identity !== options.identity() || !current(value)) { feedback.value = 'This work changed. Reopen the move.'; return }
    const latest = to.release && options.releases().find(r => r.release_id === to.release!.release_id)
    if (latest && latest.revision !== to.release!.revision) { feedback.value = 'The destination changed. Reopen the move.'; return }
    const reason = advice(value, to)
    if (reason) { feedback.value = reason; return }
    if (!options.actions) { feedback.value = 'Move actions are unavailable.'; return }
    busy.value = true
    try {
      // Close the stationary sheet before the explicit committed row transition.
      draft.value = null
      const actions = options.actions
      const guarded: DeliveryActions = { ...actions, commit: (captured, change) => !disposed && identity === options.identity() && actions.commit(captured, change) }
      const result = await submitMove(value, to, guarded)
      if (identity !== options.identity()) return
      feedback.value = result.undo_event_id ? 'Move saved. Undo is available in the toolbar.' : 'Move saved.'
      const id = value.kind === 'item' ? value.record.item_id : value.record.release_id
      requestAnimationFrame(() => { if (identity === options.identity()) {
        const element = [...options.root.value?.querySelectorAll<HTMLElement>('[data-planning-item], [data-planning-release]') ?? []].find(el => el.dataset.planningItem === id || el.dataset.planningRelease === id)
        ;(element?.querySelector<HTMLElement>('[data-move-handle], .release-more') ?? options.root.value?.querySelector<HTMLElement>('.expansion-controls button'))?.focus({ preventScroll: true })
      } })
    } catch (e) { if (identity === options.identity()) feedback.value = refusal(e) }
    finally { if (identity === options.identity()) busy.value = false }
  }
  function confirm(to: MoveTarget) { if (draft.value) void save(draft.value, to, draftIdentity) }
  function cancel() { clearTimeout(timer); pending = null; drag.value = null; target.value = null; line.value = null; highlight.value = ''; refused.value = false; if(frame) cancelAnimationFrame(frame); frame = 0 }
  function resolve(x: number, y: number) {
    line.value = null; highlight.value = ''; target.value = null; refused.value = false
    const value = drag.value, el = document.elementFromPoint(x,y)
    if (!value || !el || !options.root.value?.contains(el)) return
    const itemRow = el.closest<HTMLElement>('[data-planning-item]'), releaseRow = el.closest<HTMLElement>('[data-planning-release]')
    let dest: PlanningRelease | null = null, slot: MoveSlot = {}, box: DOMRect | undefined, gapY = 0
    if (value.kind === 'release') {
      const block = el.closest<HTMLElement>('[data-planning-block]'), id = block?.dataset.planningBlock
      dest = options.releases().find(r => r.release_id === id) ?? null
      if (!dest || dest.release_id === value.record.release_id) return
      box = (block!.querySelector('.release-row') ?? block!).getBoundingClientRect()
      const after = y > box.top + box.height/2
      slot = visibleGap(options.releases().filter(r => !['released','abandoned'].includes(r.state)), dest.release_id, after, value.record.release_id)
      gapY = after ? box.bottom : box.top
    } else if (itemRow) {
      const anchor = rows().find(r => r.item_id === itemRow.dataset.planningItem)
      if (!anchor || anchor.item_id === value.record.item_id) return
      dest = options.releases().find(r => r.release_id === anchor.release_id) ?? null
      box = itemRow.getBoundingClientRect()
      if (anchor.rank) {
        const after = y > box.top + box.height/2
        slot = visibleGap(rows().filter(r => r.release_id === anchor.release_id), anchor.item_id, after, value.record.item_id)
        gapY = after ? box.bottom : box.top
      } // Tail-zone explicitly appends; tail rows are never anchors.
    } else {
      const id = releaseRow?.dataset.planningRelease ?? el.closest<HTMLElement>('[data-planning-block]')?.dataset.planningBlock
      if (!id) return
      if (id !== 'backlog') { dest = options.releases().find(r => r.release_id === id) ?? null; if (!dest) return }
      box = (releaseRow ?? el.closest<HTMLElement>('.backlog-row') ?? el.closest<HTMLElement>('[data-planning-block]'))?.getBoundingClientRect()
    }
    const to = { release: dest ? { ...dest } : null, slot }; target.value = to
    const reason = advice(value,to); refused.value = !!reason
    const anchorId = slot.after_id ?? slot.before_id
    const namedAnchor = rows().find(row => row.item_id === anchorId)?.key ?? options.releases().find(row => row.release_id === anchorId)?.display_name ?? options.releases().find(row => row.release_id === anchorId)?.title
    feedback.value = reason || (namedAnchor ? `Place ${slot.after_id ? 'after' : 'before'} ${namedAnchor}.` : 'Append to ranked work, before new Backlog work.')
    if (box && gapY && !reason) line.value = { x: box.x, y: gapY, width: box.width }
    else highlight.value = dest?.release_id ?? 'backlog'
  }
  function scroll() {
    if (!drag.value) return
    const root = options.scroll(), box = root?.getBoundingClientRect()
    if (root && box) {
      const amount = point.value.y < box.top+48 ? -8 : point.value.y > box.bottom-48 ? 8 : 0
      if (amount) { root.scrollTop += amount; resolve(point.value.x,point.value.y) }
    }
    frame = requestAnimationFrame(scroll)
  }
  function pickup() {
    if (!pending || pending.identity !== options.identity() || !current(pending.subject)) { cancel(); return }
    drag.value = pending.subject; point.value = { x: pending.x, y: pending.y }; swallow = true
    options.root.value?.setPointerCapture?.(pending.pointer)
    resolve(pending.x,pending.y); frame = requestAnimationFrame(scroll)
  }
  function down(event: PointerEvent) {
    if (event.button !== 0 || pending || drag.value || busy.value || options.stale()) return
    const el = event.target instanceof Element ? event.target : null, value = subject(el)
    if (!value) return
    cursor.value = value.kind === 'item' ? value.record.item_id : value.record.release_id
    const handle = !!el?.closest('[data-move-handle]'), touch = event.pointerType === 'touch'
    if (!handle && (!touch || el?.closest('button,input,select,textarea'))) return
    pending = { subject: value, pointer: event.pointerId, x:event.clientX,y:event.clientY,touch,identity:options.identity() }
    if (touch) timer = setTimeout(pickup,PICKUP_MS)
    else event.preventDefault()
  }
  function move(event: PointerEvent) {
    if (!pending || event.pointerId !== pending.pointer) return
    if (!drag.value) {
      if (Math.hypot(event.clientX-pending.x,event.clientY-pending.y) > 8) { if (pending.touch) cancel(); else pickup() }
      if (!drag.value) return
    }
    event.preventDefault(); point.value = {x:event.clientX,y:event.clientY}; resolve(event.clientX,event.clientY)
  }
  function up(event: PointerEvent) {
    if (!pending) { setTimeout(() => { swallow = false }, 0); return }
    if (event.pointerId !== pending.pointer) return
    const value=drag.value, to=target.value, identity=pending.identity
    const hadDrag=!!value
    cancel()
    if (hadDrag) {
      event.preventDefault()
      // Suppress the release click, but never consume the next deliberate click.
      setTimeout(() => { swallow=false },0)
      if (value && to) void save(value,to,identity)
    }
  }
  function click(event: MouseEvent) { if (swallow) { event.preventDefault(); event.stopImmediatePropagation(); swallow=false } }
  function keys(event: KeyboardEvent) {
    if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return
    if (event.key === 'Escape' && (pending || drag.value)) { event.preventDefault(); cancel(); feedback.value='Move cancelled.'; return }
    if (event.target instanceof Element && event.target.closest('input,textarea,select,[contenteditable="true"]') || document.querySelector('dialog[open],.floating')) return
    if (!['g','t','m'].includes(event.key)) return
    const active = subject(document.activeElement) ?? (cursor.value ? subject(options.root.value?.querySelector(`[data-planning-item="${cursor.value}"], [data-planning-release="${cursor.value}"]`) ?? null) : null)
    if (active) { event.preventDefault(); open(active,event.key === 't' ? 'top' : event.key === 'm' ? 'after' : 'append') }
  }
  function touch(event: TouchEvent) { if (drag.value) event.preventDefault() }
  function scrollIntent() { if (pending && !drag.value) cancel() }
  function context(event: MouseEvent) { if (pending || drag.value) event.preventDefault() }
  function pointerCancel() { cancel(); swallow = false }
  watch(options.scroll, (next, previous) => { previous?.removeEventListener('scroll',scrollIntent); next?.addEventListener('scroll',scrollIntent) }, { immediate: true })
  watch(options.identity, () => { cancel(); draft.value=null; cursor.value=''; feedback.value=''; busy.value=false })
  onMounted(() => { window.addEventListener('pointermove',move,{passive:false}); window.addEventListener('pointerup',up); window.addEventListener('pointercancel',pointerCancel); window.addEventListener('keydown',keys); window.addEventListener('touchmove',touch,{passive:false}) })
  onBeforeUnmount(() => { disposed = true; cancel(); window.removeEventListener('pointermove',move); window.removeEventListener('pointerup',up); window.removeEventListener('pointercancel',pointerCancel); window.removeEventListener('keydown',keys); window.removeEventListener('touchmove',touch); options.scroll()?.removeEventListener('scroll',scrollIntent) })
  return { draft,initial,drag,target,feedback,busy,point,line,highlight,refused,open,openItem,confirm,advice,cancel,down,click,context }
}
