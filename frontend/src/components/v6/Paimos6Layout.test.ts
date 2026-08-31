import { afterEach, describe, expect, it, vi } from 'vitest'
import { h, nextTick } from 'vue'

import { api } from '@/api/client'
import { mountComponent } from '@/components/ai/testMount'
import { commandShortcutLabel } from '@/v6/commandPalette'

const { route, router } = vi.hoisted(() => ({
  route: { query: { project: '42' } as Record<string, string>, fullPath: '/?project=42' },
  router: { replace: vi.fn().mockResolvedValue(undefined), push: vi.fn().mockResolvedValue(undefined) },
}))

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRoute: () => route, useRouter: () => router }
})

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: { id: 1, role: 'member', status: 'active' },
    allProjects: true,
    accessibleProjects: new Map(),
  }),
}))
import Paimos6Layout from './Paimos6Layout.vue'

describe('Paimos6Layout (PAI-854 / PAI-867 isolated production shell)', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.restoreAllMocks()
    router.replace.mockReset().mockResolvedValue(undefined)
    router.push.mockReset().mockResolvedValue(undefined)
  })

  it('mounts the live Paimos 6 home and command affordances without ordinary CRUD chrome or a rail', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      schema_version: 1, default_shortcut: 'Mod+KeyK', instance_shortcut: null,
      user_shortcut: null, effective_shortcut: 'Mod+KeyK', source: 'default',
    } as never)
    const mounted = await mountComponent(Paimos6Layout, {}, {
      default: () => h('main', { class: 'fixture-home' }, 'session home'),
    })
    const shell = mounted.el.querySelector<HTMLElement>('[data-shell="v6"]')!
    const text = shell.textContent ?? ''

    expect(shell).not.toBeNull()
    expect(text).toContain('Paimos')
    expect(text).toContain('6.0')
    expect(text).toContain('Live · web')
    expect(text).not.toContain('Visual mock')
    expect(shell.querySelector('kbd')?.textContent).toBe(commandShortcutLabel('Mod+KeyK'))
    expect(shell.querySelector('.p6-command-mount')?.getAttribute('aria-label')).toContain('Open command palette')
    expect(shell.querySelector('.fixture-home')?.textContent).toBe('session home')
    expect(shell.querySelector('aside')).toBeNull()
    expect(shell.querySelector('nav')).toBeNull()
    for (const label of ['Projects', 'Customers', 'Issues', 'Reporting', 'New Issue', 'Timer', 'Undo']) {
      expect(text).not.toContain(label)
    }
    expect(shell.querySelector<HTMLAnchorElement>('.p6-back')?.getAttribute('href')).toBe('/legacy')

    shell.querySelector<HTMLButtonElement>('.p6-command-mount')!.click()
    await nextTick()
    expect(shell.querySelector('[role="dialog"][aria-modal="true"]')).not.toBeNull()
    expect(shell.querySelector('[role="combobox"]')).not.toBeNull()
    expect(shell.textContent).toContain('Responsive web · no push')
    expect(shell.textContent).toContain('Open talk-first door')
    expect(shell.textContent).toContain('Command shortcut settings')
    expect(shell.textContent).toContain('Open 5.x dashboard')
    expect(shell.textContent).not.toContain('Clear selected session')
    const legacy = [...shell.querySelectorAll<HTMLButtonElement>('[role="option"]')]
      .find((button) => button.textContent?.includes('Open 5.x dashboard'))!
    legacy.click()
    await vi.waitFor(() => expect(router.push).toHaveBeenCalledWith('/legacy'))
    await mounted.unmount()
  })

  it('renders authorized grouped results and keeps unavailable node activation honest', async () => {
    vi.spyOn(api, 'get').mockImplementation((path: string) => Promise.resolve(path.includes('/projects/') ? {
      schema_version: 1,
      query: 'command',
      sessions: [{ product_session_id: '17e5d8f7-0b11-4bee-a8a4-a11406de865a', title: 'Command session', summary: '', updated_at: '2026-08-31T12:00:00Z' }],
      nodes: [{ node_id: 7, node_key: 'PAI-866', title: 'Command palette', type: 'task', type_label: 'Task', status: 'open', updated_at: '2026-08-31T12:00:00Z' }],
      knowledge: [{ knowledge_id: 9, type: 'memory', type_label: 'Memory', slug: 'command-contract', title: 'Command contract', updated_at: '2026-08-31T12:00:00Z' }],
    } : {
      schema_version: 1, default_shortcut: 'Mod+KeyK', instance_shortcut: null,
      user_shortcut: null, effective_shortcut: 'Mod+KeyK', source: 'default',
    }) as never)
    const mounted = await mountComponent(Paimos6Layout, {}, { default: () => h('main', 'home') })
    mounted.el.querySelector<HTMLButtonElement>('.p6-command-mount')!.click()
    await nextTick()
    const input = mounted.el.querySelector<HTMLInputElement>('[role="combobox"]')!
    input.value = 'command'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('PAI-866 · Command palette'))
    const node = [...mounted.el.querySelectorAll<HTMLButtonElement>('[role="option"]')]
      .find((button) => button.textContent?.includes('PAI-866'))!
    expect(node.getAttribute('aria-disabled')).toBe('true')
    node.click()
    await nextTick()
    expect(mounted.el.textContent).toContain('Node detail is not available in the 6.0 web preview.')
    expect(router.push).not.toHaveBeenCalled()
    expect(router.replace).not.toHaveBeenCalled()

    const session = [...mounted.el.querySelectorAll<HTMLButtonElement>('[role="option"]')]
      .find((button) => button.textContent?.includes('Command session'))!
    session.click()
    await vi.waitFor(() => expect(router.replace).toHaveBeenCalledWith({
      query: { project: '42', session: '17e5d8f7-0b11-4bee-a8a4-a11406de865a' },
    }))

    mounted.el.querySelector<HTMLButtonElement>('.p6-command-mount')!.click()
    await nextTick()
    const reopened = mounted.el.querySelector<HTMLInputElement>('[role="combobox"]')!
    reopened.value = 'command'
    reopened.dispatchEvent(new Event('input', { bubbles: true }))
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('Command contract'))
    const knowledge = [...mounted.el.querySelectorAll<HTMLButtonElement>('[role="option"]')]
      .find((button) => button.textContent?.includes('Command contract'))!
    knowledge.click()
    await vi.waitFor(() => expect(router.push).toHaveBeenCalledWith({
      path: '/projects/42', query: { tab: 'knowledge', memory: 'command-contract' },
    }))
    await mounted.unmount()
  })
})
