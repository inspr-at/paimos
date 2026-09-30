// SPDX-License-Identifier: AGPL-3.0-only
export function validPiModel(provider: string, model: string): boolean {
  if (!model || model.length > 116 || provider.length + model.length + 1 > 128 || /^sk-/i.test(model)) return false
  return (provider === 'openrouter'
    ? /^[a-zA-Z0-9][a-zA-Z0-9._-]*\/[a-zA-Z0-9][a-zA-Z0-9._-]*(?::[a-zA-Z0-9][a-zA-Z0-9._-]*)?$/
    : /^[a-zA-Z0-9][a-zA-Z0-9._:/-]*$/).test(model)
}
export const piDataNote = (model: string) => model.startsWith('stealth/') || model.endsWith(':free') || model === 'openrouter/free'
