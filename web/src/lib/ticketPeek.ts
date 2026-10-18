// SPDX-License-Identifier: AGPL-3.0-only
// One app-level ticket peek. A plain click opens the ticket in the side panel
// and leaves the current view; the anchor stays a real link for a new tab or
// window. The release history provides its own nearer peek beside the sheet.
import { computed, nextTick, onScopeDispose, provide, ref, watch, type InjectionKey, type Ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { normalKey } from './ticketLinks'

export interface TicketPeek {
  open: (key: string, from?: HTMLElement | null) => void
  openKey: Ref<string | null>
}
export const TICKET_PEEK: InjectionKey<TicketPeek> = Symbol('ticket-peek')

export const PEEK_QUERY = 'peek'

export interface TicketPeekPanel {
  requestClose: () => void | Promise<void>
  shortcut: (key: string) => boolean
  contains: (target: EventTarget | null) => boolean
  focus: () => void | Promise<void>
}

const typing = (target: EventTarget | null) =>
  target instanceof HTMLElement && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)

// Called once from the app shell. The open ticket is the `peek` query so the
// path of the current view stays put. Esc closes it before any page shortcut.
export function provideTicketPeek() {
  const route = useRoute()
  const router = useRouter()
  const openKey = ref<string | null>(null)
  const panel = ref<TicketPeekPanel | null>(null)
  let opener: HTMLElement | null = null
  let pushed = false

  // A routed ticket owns the right edge. A stale peek must neither cover it
  // with another record nor reserve a second panel's width in the shell.
  watch(() => route.params.ticketKey ? '' : (typeof route.query[PEEK_QUERY] === 'string' ? route.query[PEEK_QUERY] : ''), value => {
    openKey.value = value ? normalKey(value) : null
    if (!openKey.value) pushed = false
  }, { immediate: true })

  function open(key: string, from?: HTMLElement | null) {
    opener = from ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null)
    const next = normalKey(key)
    if (typeof route.query[PEEK_QUERY] === 'string' && normalKey(route.query[PEEK_QUERY]) === next) {
      void nextTick(() => panel.value?.focus())
      return
    }
    pushed = true
    void router.push({ path: route.path, query: { ...route.query, [PEEK_QUERY]: next }, hash: route.hash })
  }

  async function close() {
    const from = opener
    opener = null
    if (pushed && typeof window.history.state?.back === 'string') {
      pushed = false
      const done = new Promise<void>(resolve => {
        const stop = router.afterEach(() => { stop(); resolve() })
      })
      router.back()
      await done
    } else {
      pushed = false
      const { [PEEK_QUERY]: _peek, ...rest } = route.query
      await router.replace({ path: route.path, query: rest, hash: route.hash })
    }
    await nextTick()
    const session = document.querySelector<HTMLElement>('.session-panel')
    if (session) session.focus({ preventScroll: true })
    else if (from?.isConnected) from.focus({ preventScroll: true })
  }

  // A session dock and the ticket dock share the right edge. The peek replaces
  // the session, and this action brings the session back without leaving /agents.
  const backLabel = computed(() =>
    route.path.startsWith('/agents/') && typeof route.params.sessionId === 'string' && route.params.sessionId ? 'Back to session' : '')

  function onKey(event: KeyboardEvent) {
    const host = panel.value
    if (!openKey.value || !host) return
    if (event.key === 'Escape') {
      if (document.querySelector('.floating, dialog[open]')) return
      event.preventDefault()
      event.stopImmediatePropagation()
      void host.requestClose()
      return
    }
    if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey || typing(event.target)) return
    if (!host.contains(event.target)) return
    if (host.shortcut(event.key)) {
      event.preventDefault()
      event.stopImmediatePropagation()
    }
  }
  window.addEventListener('keydown', onKey, true)
  onScopeDispose(() => window.removeEventListener('keydown', onKey, true))

  function bind(instance: unknown) {
    panel.value = instance && typeof instance === 'object' && 'requestClose' in instance ? instance as TicketPeekPanel : null
  }

  provide(TICKET_PEEK, { open, openKey })
  return { openKey, close, backLabel, bind }
}
