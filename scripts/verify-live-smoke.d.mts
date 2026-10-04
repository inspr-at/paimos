// SPDX-License-Identifier: AGPL-3.0-only
export function allowLiveRead(method: string, rawURL: string, base?: string): boolean;
export function smoke(env?: Readonly<Record<string, string | undefined>>, base?: string): Promise<void>;
