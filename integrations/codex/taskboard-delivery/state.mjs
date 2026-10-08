import { randomUUID } from 'node:crypto'
import { lstat, mkdir, open, readFile, rename, rmdir, unlink } from 'node:fs/promises'
import { isAbsolute, join } from 'node:path'

import { AdapterError } from './rpc.mjs'

const MAX_BYTES = 64 * 1024

// Private state for one thread's adapter: the runs it watches and the inbox
// items it has delivered, so a restart does not repeat them. A lock directory
// keeps a second adapter off the same state; after a crash, check that the
// old process is gone before removing it.
export async function openState(directory, thread) {
  if (!directory || !isAbsolute(directory)) throw new AdapterError('Set TASKBOARD_CODEX_STATE_DIR to an absolute private directory')
  await mkdir(directory, { mode: 0o700, recursive: true })
  const info = await lstat(directory)
  if (!info.isDirectory() || info.isSymbolicLink() || (info.mode & 0o077) || info.uid !== process.getuid()) {
    throw new AdapterError('TASKBOARD_CODEX_STATE_DIR must be a private directory owned by this user')
  }
  const lock = join(directory, 'lock')
  try { await mkdir(lock, { mode: 0o700 }) } catch {
    throw new AdapterError(`Another adapter holds ${lock}; remove it only after checking that process has stopped`)
  }
  const path = join(directory, 'state.json')
  let value = { thread, runs: [], seen: [] }
  try {
    const file = await lstat(path)
    if (!file.isFile() || file.isSymbolicLink() || (file.mode & 0o077) || file.size > MAX_BYTES) throw new AdapterError('Invalid adapter state file')
    const stored = JSON.parse(await readFile(path, 'utf8'))
    if (stored?.thread !== thread) throw new AdapterError('The state directory belongs to another Codex thread; use one directory per thread')
    value = { thread, runs: Array.isArray(stored.runs) ? stored.runs : [], seen: Array.isArray(stored.seen) ? stored.seen : [] }
  } catch (error) {
    if (error.code !== 'ENOENT') { await rmdir(lock); throw error }
  }
  // Saves run one at a time in call order, so an older snapshot never lands
  // after a newer one; close waits for them before releasing the lock.
  let writing = Promise.resolve()
  async function write(data) {
    const temporary = join(directory, `.state-${randomUUID()}.tmp`)
    try {
      const file = await open(temporary, 'wx', 0o600)
      try { await file.writeFile(data); await file.sync() } finally { await file.close() }
      await rename(temporary, path)
    } finally {
      await unlink(temporary).catch(error => { if (error.code !== 'ENOENT') throw error })
    }
  }
  return {
    get: () => value,
    async save(next) {
      value = { thread, runs: next.runs, seen: next.seen }
      const data = JSON.stringify(value)
      if (Buffer.byteLength(data) > MAX_BYTES) throw new AdapterError('Adapter state too large')
      writing = writing.catch(() => {}).then(() => write(data))
      return writing
    },
    async close() {
      await writing.catch(() => {})
      await rmdir(lock)
    },
  }
}
