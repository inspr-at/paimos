import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountComponent } from '@/components/ai/testMount'
import { api } from '@/api/client'
import HabitatTicketPicker from './HabitatTicketPicker.vue'

afterEach(() => {
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('Habitat ticket picker', () => {
  it('accepts the canonical issue_key returned by the issue-list envelope', async () => {
    vi.spyOn(api, 'getWithMeta').mockResolvedValue({
      data: {
        issues: [
          {
            id: 917,
            project_id: 1,
            issue_key: 'PAI-917',
            title: 'Fast iteration and runtime lifecycle',
          },
        ],
        total: 1,
        offset: 0,
        limit: 50,
        has_more: false,
      },
      status: 200,
      permissionsEpoch: '0',
      permissionsEpochGeneration: 0,
      etag: null,
      lastModified: null,
    })
    const mounted = await mountComponent(HabitatTicketPicker, {
      projectId: 1,
      authority: 'human:1',
      modelValue: null,
      disabled: false,
    })

    await vi.waitFor(() => {
      const ticket = mounted.el.querySelector<HTMLSelectElement>('[aria-label="Ticket"]')
      expect(ticket?.disabled).toBe(false)
      expect(ticket?.options[1]?.value).toBe('917')
      expect(ticket?.options[1]?.textContent).toContain('PAI-917')
    })
    expect(mounted.el.textContent).not.toContain('Tickets unavailable')
    await mounted.unmount()
  })
})
