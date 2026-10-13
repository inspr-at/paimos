// SPDX-License-Identifier: AGPL-3.0-only
import { h, type FunctionalComponent } from 'vue'
import SessionExpert from '../../components/nativeExpert/SessionExpert.vue'
import type { NativeApproval } from '../nativeSessionContract'
import type { SessionViewProps } from './ports'

// A3: the panel owns its single menu. These views share the existing lifetime,
// consent and permission fences, and never render the standalone selector.
function sessionPane(name: string, pane: 'events' | 'terminal') {
  const View: FunctionalComponent<SessionViewProps, { review: (approval: NativeApproval) => void }> = (props, { attrs, slots, emit }) => h(SessionExpert, {
    ...attrs, ...props, pane, standalone: false,
    onReview: (approval: NativeApproval) => emit('review', approval),
  }, slots)
  View.displayName = name
  View.props = ['session', 'viewer', 'consent', 'transport', 'active', 'name', 'host', 'modelProfile']
  View.emits = ['review']
  return View
}

export const SessionEvents = sessionPane('SessionEvents', 'events')
export const SessionTerminal = sessionPane('SessionTerminal', 'terminal')
