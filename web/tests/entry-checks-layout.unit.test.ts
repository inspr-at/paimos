// SPDX-License-Identifier: AGPL-3.0-only
import { createPinia } from 'pinia'
import { reactive } from 'vue'
import { expect, it, vi } from 'vitest'
import { useEntryDefinitionLayout } from '../src/lib/useEntryChecks'

const session = reactive({ identity: { principal: { id: 'ada' }, tenant: { id: 'workspace' } } })
vi.mock('../src/stores/session', () => ({ useSession: () => session }))
vi.mock('../src/lib/entryChecks', () => ({ readEntryChecks: vi.fn() }))

// Risk: a keyed workspace loses its empty-project hint, or another identity
// inherits it and hides the new owner's usual loading row.
it('keeps bounded empty-project layout hints only for the current person and tenant', () => {
  const pinia = createPinia(), layout = useEntryDefinitionLayout(pinia)
  try {
    expect(layout.isEmpty('project')).toBe(false)
    layout.remember('project', false)
    expect(useEntryDefinitionLayout(pinia).isEmpty('project')).toBe(true)
    expect(layout.isEmpty('other-project')).toBe(false)
    layout.remember('project', true)
    expect(layout.isEmpty('project')).toBe(false)
    layout.remember('project', false)
    session.identity.principal.id = 'grace'
    expect(layout.isEmpty('project')).toBe(false)
    layout.remember('project', false)
    session.identity.tenant.id = 'another-workspace'
    expect(layout.isEmpty('project')).toBe(false)
    for (let index = 0; index < 65; index++) layout.remember(`project-${index}`, false)
    expect(layout.isEmpty('project-0')).toBe(false)
    expect(layout.isEmpty('project-1')).toBe(true)
    expect(layout.isEmpty('project-64')).toBe(true)
  } finally { layout.$dispose() }
})
