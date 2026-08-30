import { afterEach, describe, expect, it } from 'vitest'
import { h } from 'vue'

import { mountComponent } from '@/components/ai/testMount'
import Paimos6Layout from './Paimos6Layout.vue'

describe('Paimos6Layout (PAI-854 isolated preview shell)', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('mounts the fixture and command affordances without ordinary CRUD chrome or a rail', async () => {
    const mounted = await mountComponent(Paimos6Layout, {}, {
      default: () => h('main', { class: 'fixture-home' }, 'session home'),
    })
    const shell = mounted.el.querySelector<HTMLElement>('[data-shell="v6"]')!
    const text = shell.textContent ?? ''

    expect(shell).not.toBeNull()
    expect(text).toContain('Paimos')
    expect(text).toContain('6 preview')
    expect(text).toContain('Local fixture · non-live')
    expect(text).toContain('Visual mock')
    expect(shell.querySelector('kbd')?.textContent).toBe('⌘ K')
    expect(shell.querySelector('.p6-command-mount')?.getAttribute('aria-label')).toContain('visual mock only')
    expect(shell.querySelector('.fixture-home')?.textContent).toBe('session home')
    expect(shell.querySelector('aside')).toBeNull()
    expect(shell.querySelector('nav')).toBeNull()
    for (const label of ['Projects', 'Customers', 'Issues', 'Reporting', 'New Issue', 'Timer', 'Undo']) {
      expect(text).not.toContain(label)
    }
    expect(shell.querySelector<HTMLAnchorElement>('.p6-back')?.getAttribute('href')).toBe('/')
    await mounted.unmount()
  })
})
