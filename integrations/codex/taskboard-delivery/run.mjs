#!/usr/bin/env node
import { attach } from './adapter.mjs'
import { AdapterError, connectCodex } from './rpc.mjs'
import { openState } from './state.mjs'

const controller = new AbortController()
for (const name of ['SIGINT', 'SIGTERM']) process.on(name, () => controller.abort())
const log = text => process.stderr.write(`taskboard-delivery: ${text}\n`)

let rpc
let state
try {
  const thread = process.env.TASKBOARD_CODEX_THREAD ?? ''
  if (!/^[0-9a-f-]{36}$/.test(thread)) throw new AdapterError('Set TASKBOARD_CODEX_THREAD to the UUID of the loaded Codex thread')
  state = await openState(process.env.TASKBOARD_CODEX_STATE_DIR, thread)
  rpc = await connectCodex(process.env.TASKBOARD_CODEX_URL ?? '', { signal: controller.signal })
  await attach({ rpc, thread, state, signal: controller.signal, log })
} catch (error) {
  if (!controller.signal.aborted) {
    log(error instanceof AdapterError ? error.message : `stopped: ${error.message}`)
    process.exitCode = 1
  }
} finally {
  rpc?.close()
  await state?.close()
}
