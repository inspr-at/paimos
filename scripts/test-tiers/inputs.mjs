// SPDX-License-Identifier: AGPL-3.0-only
import { constants, openSync, closeSync, fstatSync, lstatSync, readSync, realpathSync } from 'node:fs'
import { relative, resolve, isAbsolute } from 'node:path'

export const inputBounds = Object.freeze({ files: 20_000, entries: 100_000, totalBytes: 64 * 1024 * 1024, fileBytes: 2 * 1024 * 1024 })

export function inputMetadata(checkout, file, limit = inputBounds.fileBytes) {
  const root = realpathSync(checkout), path = resolve(root, file)
  const inside = relative(root, realpathSync(path))
  if (inside === '..' || inside.startsWith('../') || isAbsolute(inside)) throw new Error('input escapes checkout')
  const stat = lstatSync(path)
  if (!stat.isFile() || stat.isSymbolicLink()) throw new Error('input is not a regular file')
  if (stat.size >= limit) throw new Error('per-file byte bound reached')
  return { path, size: stat.size, dev: stat.dev, ino: stat.ino }
}

// Never read to EOF without a bound. One extra byte detects a growing file;
// metadata checks bind the bytes to the file admitted during the preflight.
export function readInput(metadata, limit = inputBounds.fileBytes) {
  const fd = openSync(metadata.path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK)
  try {
    const stat = fstatSync(fd)
    if (!stat.isFile() || stat.size !== metadata.size || stat.dev !== metadata.dev || stat.ino !== metadata.ino || stat.size >= limit)
      throw new Error('input changed during scan')
    const bytes = Buffer.alloc(metadata.size + 1)
    let length = 0, n
    while (length < bytes.length && (n = readSync(fd, bytes, length, bytes.length - length, null))) length += n
    const after = fstatSync(fd)
    if (length !== metadata.size || after.size !== metadata.size || after.mtimeMs !== stat.mtimeMs || after.ctimeMs !== stat.ctimeMs)
      throw new Error('input changed during read')
    if (bytes.subarray(0, length).some(byte => byte < 32 && ![9, 10, 12, 13].includes(byte) || byte === 127)) throw new Error('binary input')
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes.subarray(0, length))
  } finally { closeSync(fd) }
}

export function boundedText(checkout, file, limit = inputBounds.fileBytes) {
  try { return readInput(inputMetadata(checkout, file, limit), limit) } catch { return undefined }
}
