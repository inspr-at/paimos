/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h } from 'vue'

import type { AttachmentJob } from '@/composables/useAttachmentUploads'
import AttachmentSidebar from './AttachmentSidebar.vue'

function job(
  id: string,
  status: AttachmentJob['status'],
  progress: number,
  error?: string,
): AttachmentJob {
  const file = new File(['contents'], `${id}.png`, { type: 'image/png' })
  return {
    id,
    origin: 'current-session',
    file,
    filename: file.name,
    size: file.size,
    isImage: true,
    progress,
    status,
    attachmentId: status === 'done' ? 44 : null,
    error,
    previewUrl: null,
  }
}

function mount(
  jobs: AttachmentJob[],
  readonly = false,
  manageJob?: (job: AttachmentJob) => boolean,
) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const retry = vi.fn()
  const remove = vi.fn()
  const addFiles = vi.fn()
  const app = createApp({
    render: () => h(AttachmentSidebar, {
      jobs,
      readonly,
      manageJob,
      onRetry: retry,
      onRemove: remove,
      onAddFiles: addFiles,
    }),
  })
  app.mount(root)
  return { root, retry, remove, addFiles, unmount: () => { app.unmount(); root.remove() } }
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('AttachmentSidebar accessibility', () => {
  it('exposes the picker as a labelled, focusable keyboard action', () => {
    const harness = mount([])
    const picker = harness.root.querySelector<HTMLElement>('.att-drop')!
    const input = harness.root.querySelector<HTMLInputElement>('input[type="file"]')!
    expect(picker.getAttribute('role')).toBe('button')
    expect(picker.tabIndex).toBe(0)
    expect(picker.getAttribute('aria-label')).toContain('Add attachments')
    expect(picker.getAttribute('for')).toBe(input.id)

    const click = vi.spyOn(input, 'click')
    picker.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    picker.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true }))
    expect(click).toHaveBeenCalledTimes(2)
    harness.unmount()
  })

  it('conveys upload progress and failures to assistive technology', () => {
    const harness = mount([
      job('pending', 'pending', 42),
      job('failed', 'failed', 0, 'network unavailable'),
    ])
    const progress = harness.root.querySelector<HTMLElement>('[role="progressbar"]')!
    expect(progress.getAttribute('aria-label')).toBe('Uploading pending.png')
    expect(progress.getAttribute('aria-valuemin')).toBe('0')
    expect(progress.getAttribute('aria-valuemax')).toBe('100')
    expect(progress.getAttribute('aria-valuenow')).toBe('42')
    expect(harness.root.querySelector('[role="status"]')?.textContent).toContain('42%')
    expect(harness.root.querySelector('[role="alert"]')?.textContent).toContain('network unavailable')

    const buttons = harness.root.querySelectorAll<HTMLButtonElement>('.att-btn')
    buttons[0].click()
    buttons[1].click()
    expect(harness.retry).toHaveBeenCalledTimes(1)
    expect(harness.remove).toHaveBeenCalledTimes(1)
    harness.unmount()
  })

  it('removes all upload activation affordances in read-only mode', () => {
    const harness = mount([], true)
    expect(harness.root.querySelector('.att-drop')).toBeNull()
    expect(harness.root.querySelector('input[type="file"]')).toBeNull()
    harness.unmount()
  })

  it('can manage only explicitly-authorized session jobs in a read-only host', () => {
    const seeded = { ...job('seeded', 'done', 100), origin: 'seeded' as const }
    const failed = job('failed-session', 'failed', 0, 'failed')
    const done = { ...job('done-session', 'done', 100), attachmentId: 45 }
    const harness = mount(
      [seeded, failed, done],
      true,
      (candidate) => candidate.origin === 'current-session',
    )

    expect(harness.root.querySelector('[title="Remove seeded.png"]')).toBeNull()
    expect(harness.root.querySelector('[title="Retry upload of failed-session.png"]')).not.toBeNull()
    expect(harness.root.querySelector('[title="Remove failed-session.png"]')).not.toBeNull()
    expect(harness.root.querySelector('[title="Remove done-session.png"]')).not.toBeNull()
    expect(harness.root.querySelector('.att-drop')).toBeNull()
    harness.unmount()
  })
})
