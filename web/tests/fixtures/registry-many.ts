// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1163: the registry's spread, as the live one holds it (2026-10-11) plus the approved mock's "many models" state:
// 36 harness/model ids. Cursor names Grok's level in its ids (eight for Grok 4.7 and its fast variant);
// Opus 5.5 runs through Claude Code, Cursor and pi; aliases follow their line. Test data only.
export interface ManyRoute {
  harness: string; model: string; family: string; efforts: string[]; display_name: string; short_name?: string; model_version?: string
  origin?: 'shipped' | 'provider' | 'harness' | 'manual'; source?: 'auto' | 'manual'; enabled?: boolean; note?: string; identity?: string
  lifecycle?: { state: string | null; date: string | null; successor: string | null; reason: string; source: string }
  pause?: { until: string | null; reason: string; by: string | null; at: string }
}
export const LEVEL: Record<string, number | null> = { off: 0, default: null, minimal: 1, low: 1, medium: 2, high: 3, xhigh: 4, max: 5, '0': 0, '1024': 1, '8192': 2, '24576': 3, '32768': 4 }
const L4 = ['low', 'medium', 'high', 'xhigh'], L5 = [...L4, 'max']
const RULE = { reason: 'Same model line has a newer sourced version', source: 'aeon_model_line/aeon_model_version_newer' }
const route = (harness: string, model: string, family: string, efforts: string[], display_name: string, extra: Partial<ManyRoute> = {}): ManyRoute => ({ harness, model, family, efforts, display_name, ...extra })
/** One row per id; a test makes one profile per level from it. */
export function manyRoutes(): ManyRoute[] {
  return [
    route('codex', 'gpt-6.1-sol', 'openai', L4, 'GPT Sol', { short_name: 'Sol', model_version: '6.1', origin: 'harness', note: 'strongest for building', identity: 'openai:gpt-6.1-sol', lifecycle: { state: 'active', date: null, successor: null, reason: '', source: 'agent-report:codex' } }),
    route('codex', 'gpt-6-sol', 'openai', ['medium', 'high', 'xhigh'], 'GPT Sol', { short_name: 'Sol', model_version: '6', origin: 'shipped', identity: 'openai:gpt-6-sol', lifecycle: { state: 'superseded', date: null, successor: 'openai:gpt-6.1-sol', reason: 'Source reports a successor', source: 'agent-report:codex' } }),
    route('codex', 'gpt-6.1-astra', 'openai', L4, 'GPT Astra', { short_name: 'Astra', model_version: '6.1', origin: 'harness', note: 'deepest reasoning, slower', identity: 'openai:gpt-6.1-astra' }),
    route('codex', 'gpt-6-astra', 'openai', L4, 'GPT Astra', { short_name: 'Astra', model_version: '6', origin: 'harness', identity: 'openai:gpt-6-astra', lifecycle: { state: 'superseded', date: null, successor: 'openai:gpt-6.1-astra', ...RULE } }),
    route('codex', 'gpt-6-luna', 'openai', L4, 'GPT Luna', { short_name: 'Luna', model_version: '6', origin: 'harness', note: 'fast and light', identity: 'openai:gpt-6-luna', lifecycle: { state: 'retiring', date: '2026-10-31T12:00:00Z', successor: null, reason: 'OpenAI shuts it down on 31 Oct', source: 'https://platform.openai.com/docs/deprecations' } }),
    route('codex', 'gpt-6-terra', 'openai', ['medium', 'high', 'xhigh'], 'GPT Terra', { short_name: 'Terra', model_version: '6', origin: 'harness', identity: 'openai:gpt-6-terra' }),
    route('claude', 'claude-opus-5-5', 'anthropic', L5, 'Claude Opus', { short_name: 'Opus', model_version: '5.5', origin: 'provider', note: 'best for design and concepts', identity: 'anthropic:claude-opus-5-5', lifecycle: { state: 'active', date: null, successor: null, reason: '', source: 'https://api.anthropic.com/v1/models' } }),
    route('cursor', 'claude-opus-5.5', 'anthropic', ['high', 'max'], 'claude-opus-5.5', { model_version: '5.5', origin: 'harness', identity: 'anthropic:claude-opus-5.5' }),
    route('pi', 'anthropic/claude-opus-5-5', 'anthropic', ['high', 'xhigh'], 'Claude Opus', { short_name: 'Opus', model_version: '5.5', origin: 'harness', identity: 'anthropic:claude-opus-5-5' }),
    route('pi', 'anthropic/claude-opus-5', 'anthropic', ['high', 'xhigh'], 'Claude Opus', { short_name: 'Opus', model_version: '5', origin: 'shipped', identity: 'anthropic:claude-opus-5', lifecycle: { state: 'deprecated', date: '2027-01-15T12:00:00Z', successor: 'anthropic:claude-opus-5-5', reason: 'Anthropic marks it deprecated', source: 'https://api.anthropic.com/v1/models' } }),
    route('claude', 'opus', 'anthropic', ['high', 'xhigh', 'max'], 'Claude Opus', { short_name: 'Opus', origin: 'shipped', identity: 'anthropic:opus' }),
    route('claude', 'sonnet', 'anthropic', ['high', 'xhigh', 'max'], 'Claude Sonnet', { short_name: 'Sonnet', origin: 'shipped', note: 'fast everyday work', identity: 'anthropic:sonnet' }),
    route('pi', 'anthropic/claude-sonnet-5', 'anthropic', ['high'], 'Claude Sonnet', { short_name: 'Sonnet', model_version: '5', origin: 'shipped', identity: 'anthropic:claude-sonnet-5' }),
    route('claude', 'fable', 'anthropic', ['high', 'xhigh', 'max'], 'Claude Fable', { short_name: 'Fable', origin: 'shipped', identity: 'anthropic:fable' }),
    route('claude', 'claude-fable-5-1', 'anthropic', ['high', 'xhigh', 'max'], 'Claude Fable', { short_name: 'Fable', model_version: '5.1', origin: 'provider', enabled: false, identity: 'anthropic:claude-fable-5-1' }),
    route('claude', 'haiku', 'anthropic', ['low', 'medium', 'high'], 'Claude Haiku', { short_name: 'Haiku', origin: 'shipped', note: 'quick, cheap reads', identity: 'anthropic:haiku' }),
    route('grok', 'grok-4.7', 'xai', L4, 'Grok', { short_name: 'Grok', model_version: '4.7', origin: 'provider', note: 'reviews and concepts', identity: 'xai:grok-4.7' }),
    ...L4.map(level => route('cursor', `grok-4.7-${level}`, 'xai', [level], 'Grok', { short_name: 'Grok', model_version: '4.7', origin: 'harness', identity: 'xai:grok-4.7' })),
    ...L4.map(level => route('cursor', `grok-4.7-${level}-fast`, 'xai', [level], 'Grok fast', { short_name: 'Grok fast', model_version: '4.7', origin: 'harness', identity: 'xai:grok-4.7-fast' })),
    route('grok', 'grok-4.7-build-fast', 'xai', ['high'], 'Grok build fast', { origin: 'shipped', identity: 'xai:grok-4.7-build-fast' }),
    route('grok', 'grok-4.6', 'xai', ['high', 'xhigh'], 'Grok', { short_name: 'Grok', model_version: '4.6', origin: 'shipped', identity: 'xai:grok-4.6', lifecycle: { state: 'superseded', date: null, successor: 'xai:grok-4.7', ...RULE } }),
    route('grok', 'grok-4.5', 'xai', ['high'], 'Grok', { short_name: 'Grok', model_version: '4.5', origin: 'shipped', identity: 'xai:grok-4.5', lifecycle: { state: 'superseded', date: null, successor: 'xai:grok-4.7', ...RULE } }),
    route('gemini', 'gemini-2.5-pro', 'google', ['1024', '8192', '24576', '32768'], 'Gemini Pro', { short_name: 'Gemini Pro', model_version: '2.5', origin: 'shipped', note: 'long context', identity: 'google:gemini-2.5-pro' }),
    route('opencode', 'google/gemini-2.5-pro', 'google', ['default'], 'Gemini Pro', { short_name: 'Gemini Pro', model_version: '2.5', origin: 'shipped', identity: 'google:gemini-2.5-pro' }),
    route('gemini', 'gemini-2.5-flash', 'google', ['0', '1024', '8192', '24576'], 'Gemini Flash', { short_name: 'Gemini Flash', model_version: '2.5', origin: 'shipped', identity: 'google:gemini-2.5-flash', pause: { until: '2026-10-14T12:00:00Z', reason: 'Paused in Settings › Models', by: null, at: '2026-10-10T12:00:00Z' } }),
    route('cursor', 'composer-2.5', 'cursor', ['default'], 'Composer', { short_name: 'Composer', model_version: '2.5', origin: 'harness', note: 'quick edits in the editor', identity: 'cursor:composer-2.5' }),
    route('cursor', 'composer-2.5-fast', 'cursor', ['default'], 'Composer fast', { short_name: 'Composer fast', model_version: '2.5', origin: 'harness', identity: 'cursor:composer-2.5-fast' }),
    route('opencode', 'ollama/qwen3-coder', 'local', ['default'], 'Qwen3 Coder', { origin: 'shipped', identity: 'local:qwen3-coder' }),
    route('pi', 'openrouter/qwen/qwen3-coder', 'unknown', ['off'], 'Qwen3 Coder', { origin: 'manual', source: 'manual', note: 'trial for small scripts (AEON-1003)', identity: 'unknown:qwen/qwen3-coder' }),
    route('pi', 'openrouter/stealth/space-bunny-alpha', 'unknown', ['off'], 'Space Bunny Alpha', { origin: 'shipped', identity: 'unknown:stealth/space-bunny-alpha' }),
  ]
}
