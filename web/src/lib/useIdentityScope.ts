// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onScopeDispose, watch, type ComputedRef } from 'vue'
import { onAccessChange } from './authz'
import { createScope, scopeOwner, type Scope } from './identityScope'
import { useSession } from '../stores/session'

// The identity scope of a component: its owner is the signed-in tenant and person
// (and, when `requires` is given, only while that holds, e.g. the right to manage
// accounts). Everything in flight is aborted and dropped, synchronously, the moment
// the owner changes (another person or workspace, a sign-out, a lost permission),
// when permissions are reset, and when the component goes away. See identityScope.ts.
export function useIdentityScope(requires: () => boolean = () => true): Scope & { owner: ComputedRef<string> } {
  const session = useSession()
  const owner = computed(() => requires() ? scopeOwner(session.identity) : '')
  const scope = createScope(() => owner.value)
  const stopOwner = watch(owner, () => scope.reset(), { flush: 'sync' })
  const stopAccess = onAccessChange(change => { if (change === 'reset') scope.reset() })
  onScopeDispose(() => { stopOwner(); stopAccess(); scope.dispose() })
  return { ...scope, owner }
}
