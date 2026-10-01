// SPDX-License-Identifier: AGPL-3.0-only
// JSON.parse returns any. Keep deserialization unknown so callers must validate
// or narrow it before use; parsing cannot silently manufacture an admitted row.
export const parseJson = (text: string): unknown => JSON.parse(text)
