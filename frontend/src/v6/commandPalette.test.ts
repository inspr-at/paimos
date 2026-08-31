import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/api/client'
import {
  commandShortcutLabel,
  commandShortcutMatches,
  loadCommandPaletteSearch,
  loadCommandPaletteSettings,
  parseCommandPaletteSearch,
  parseCommandPaletteSettings,
  parseCommandShortcut,
  saveInstanceCommandShortcut,
  saveUserCommandShortcut,
  validateCommandShortcut,
  CommandPaletteContractError,
} from './commandPalette'

const settings = {
  schema_version: 1,
  default_shortcut: 'Mod+KeyK',
  instance_shortcut: 'Alt+KeyJ',
  user_shortcut: 'Mod+Alt+KeyK',
  effective_shortcut: 'Mod+Alt+KeyK',
  source: 'user',
}
const search = {
  schema_version: 1,
  query: 'plan',
  sessions: [{ product_session_id: '17e5d8f7-0b11-4bee-a8a4-a11406de865a', title: 'Plan launch', summary: '', updated_at: '2026-08-31T12:00:00Z' }],
  nodes: [{ node_id: 7, node_key: 'PAI-866', title: 'Commands', type: 'task', type_label: 'Task', status: 'open', updated_at: '2026-08-31T12:00:00Z' }],
  knowledge: [{ knowledge_id: 9, type: 'memory', type_label: 'Memory', slug: 'command-contract', title: 'Command contract', updated_at: '2026-08-31T12:00:00Z' }],
}

describe('PAI-866 command shortcut contract', () => {
  it('parses only canonical ordered code chords and renders platform Mod labels', () => {
    expect(parseCommandShortcut('Mod+Alt+Shift+KeyK')).toEqual({ modifiers: ['Mod', 'Alt', 'Shift'], code: 'KeyK' })
    for (const invalid of ['K', 'Shift+KeyK', 'Meta+Mod+KeyK', 'Mod+Ctrl+KeyK', 'Alt+Mod+KeyK', 'Mod+k', 'Mod+Escape']) {
      expect(parseCommandShortcut(invalid), invalid).toBeNull()
    }
    expect(commandShortcutLabel('Mod+KeyK', true)).toBe('Cmd+K')
    expect(commandShortcutLabel('Mod+KeyK', false)).toBe('Ctrl+K')
  })

  it('returns deterministic collisions and matches KeyboardEvent.code exactly', () => {
    expect(validateCommandShortcut('Mod+KeyR')).toBe('shortcut_collision')
    expect(validateCommandShortcut('Ctrl+Shift+Digit4')).toBe('shortcut_collision')
    expect(validateCommandShortcut('Ctrl+Alt+KeyR')).toBe('ok')
    expect(validateCommandShortcut('Mod+KeyK')).toBe('ok')
    expect(commandShortcutMatches(new KeyboardEvent('keydown', { code: 'KeyK', key: 'κ', ctrlKey: true }), 'Mod+KeyK', false)).toBe(true)
    expect(commandShortcutMatches(new KeyboardEvent('keydown', { code: 'KeyJ', key: 'k', ctrlKey: true }), 'Mod+KeyK', false)).toBe(false)
  })
})

describe('PAI-866 strict command wire boundaries', () => {
  afterEach(() => vi.restoreAllMocks())

  it('accepts coherent settings precedence and rejects extra or contradictory fields', () => {
    expect(parseCommandPaletteSettings(settings)).toEqual(settings)
    expect(() => parseCommandPaletteSettings({ ...settings, project_id: 1 })).toThrow(CommandPaletteContractError)
    expect(() => parseCommandPaletteSettings({ ...settings, effective_shortcut: 'Alt+KeyJ' })).toThrow(CommandPaletteContractError)
    expect(() => parseCommandPaletteSettings({ ...settings, user_shortcut: 'Mod+KeyR', effective_shortcut: 'Mod+KeyR' })).toThrow(CommandPaletteContractError)
  })

  it('accepts exact grouped rows and rejects hidden, malformed, duplicate, or over-limit facts', () => {
    expect(parseCommandPaletteSearch(search, 'plan', 8)).toEqual(search)
    expect(() => parseCommandPaletteSearch({ ...search, sessions: [{ ...search.sessions[0], secret: 'no' }] }, 'plan', 8)).toThrow(CommandPaletteContractError)
    expect(() => parseCommandPaletteSearch({ ...search, knowledge: [{ ...search.knowledge[0], type: 'Memory' }] }, 'plan', 8)).toThrow(CommandPaletteContractError)
    expect(() => parseCommandPaletteSearch({ ...search, nodes: [search.nodes[0], search.nodes[0]] }, 'plan', 8)).toThrow(CommandPaletteContractError)
    expect(() => parseCommandPaletteSearch(search, 'other', 8)).toThrow(CommandPaletteContractError)
  })

  it('calls only the frozen settings/search endpoints and strict bodies', async () => {
    const get = vi.spyOn(api, 'get').mockImplementation((path: string) => Promise.resolve(path.includes('/projects/') ? search : settings) as never)
    const put = vi.spyOn(api, 'put').mockResolvedValue(settings as never)
    const controller = new AbortController()
    await loadCommandPaletteSettings(controller.signal)
    await loadCommandPaletteSearch(42, 'plan', 8, controller.signal)
    await saveUserCommandShortcut('Mod+Alt+KeyK')
    await saveInstanceCommandShortcut(null)
    expect(get).toHaveBeenNthCalledWith(1, '/command-palette/v1/settings', { signal: controller.signal })
    expect(get).toHaveBeenNthCalledWith(2, '/projects/42/command-palette/v1?q=plan&limit=8', { signal: controller.signal })
    expect(put).toHaveBeenNthCalledWith(1, '/command-palette/v1/settings', { shortcut: 'Mod+Alt+KeyK' })
    expect(put).toHaveBeenNthCalledWith(2, '/command-palette/v1/instance-settings', { shortcut: null })
  })
})
