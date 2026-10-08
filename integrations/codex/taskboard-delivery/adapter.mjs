import { setTimeout as sleep } from 'node:timers/promises'

import { addRun, deliveryText, inboxItems, openRuns, runFromItem, SEEN_LIMIT, toolResult } from './inbox.mjs'
import { AdapterError } from './rpc.mjs'

// Server cap on task_inbox waits; Codex's default MCP tool timeout is 60 s.
export const WAIT_SECONDS = 25
const ENDED = ['thread/closed', 'thread/archived', 'thread/deleted', 'connection/closed']

// Delivers Taskboard inbox items into one loaded Codex thread. Runs are
// learned from the thread's own task_start and task_claim calls; the inbox is
// read through the thread's own MCP server, so the adapter holds no Taskboard
// credential. An idle thread gets a turn of its own; a busy one is steered.
//
// The adapter does not resume the thread, so this connection is not
// subscribed to its events and item notifications may never arrive. The
// history is read again every pollMs to learn runs started since.
export async function attach({ rpc, thread, state, signal, log = () => {},
  pollMs = 30_000, gapMs = 2_000, retryMs = 5_000, maxRetryMs = 300_000, wait = sleep, now = Date.now }) {
  let activeTurn
  let wake = () => {}
  let stopped
  const kick = () => wake()
  const unsubscribe = rpc.onNotification(note => {
    const params = note.params ?? {}
    if (ENDED.includes(note.method) && (note.method === 'connection/closed' || params.threadId === thread)) {
      stopped = new AdapterError(note.method === 'connection/closed' ? 'Codex app-server connection closed' : `Codex thread ${note.method.split('/')[1]}; delivery stopped`)
      kick()
      return
    }
    if (params.threadId !== thread) return
    if (note.method === 'turn/started') activeTurn = params.turn?.id
    if (note.method === 'turn/completed') { if (activeTurn === params.turn?.id) activeTurn = undefined; kick() }
    if (note.method === 'item/completed') {
      const run = runFromItem(params.item)
      if (run && !known.has(run.runId)) void remember(run)
    }
  })
  const pause = ms => new Promise(resolve => {
    wake = resolve
    wait(ms, undefined, { signal }).then(resolve, resolve)
  })

  let current = state.get()
  let digest
  let canWait
  let retry = retryMs
  // Every run learned in this process, so a finished run is not learned again.
  const known = new Set()
  let scannedAt
  async function remember(run) {
    known.add(run.runId)
    current = { ...current, runs: addRun(current.runs, run) }
    await state.save(current)
    digest = undefined
    kick()
  }

  try {
    await readThread()
    for (const run of await historyRuns()) {
      known.add(run.runId)
      current = { ...current, runs: addRun(current.runs, run) }
    }
    await state.save(current)
    log(`watching ${current.runs.length} Taskboard run${current.runs.length === 1 ? '' : 's'} in thread ${thread}`)

    while (!signal?.aborted && !stopped) {
      if (now() - scannedAt >= pollMs) {
        try {
          for (const run of await historyRuns()) if (!known.has(run.runId)) await remember(run)
        } catch (error) {
          if (signal?.aborted || stopped) break
          log(`history unavailable: ${error.message}`)
        }
      }
      const runs = current.runs
      if (runs.length === 0) { await pause(pollMs); continue }
      const latest = runs.at(-1)
      const waiting = canWait === true && digest !== undefined
      let inbox
      try {
        const result = await rpc.call('mcpServer/tool/call', {
          server: latest.server, threadId: thread, tool: latest.inboxTool,
          arguments: { runs: runs.map(run => ({ task_id: run.taskId, run_id: run.runId })), ...(waiting ? { wait_seconds: WAIT_SECONDS, digest } : {}) },
        }, (WAIT_SECONDS + 20) * 1000)
        if (result?.isError) throw new AdapterError(`task_inbox failed: ${toolResult(result)?.error ?? 'error result'}`)
        inbox = toolResult(result)
        if (!inbox || !Array.isArray(inbox.runs)) throw new AdapterError('task_inbox returned an unexpected result')
      } catch (error) {
        if (signal?.aborted || stopped) break
        log(`inbox unavailable: ${error.message}`)
        digest = undefined
        await pause(retry)
        retry = Math.min(retry * 2, maxRetryMs)
        continue
      }
      retry = retryMs
      canWait = typeof inbox.digest === 'string' && inbox.digest !== ''
      digest = canWait ? inbox.digest : undefined

      const open = openRuns(current.runs, inbox)
      const seen = new Set(current.seen)
      const fresh = inboxItems(inbox).filter(item => !seen.has(item.key))
      if (open.length !== current.runs.length) current = { ...current, runs: open }
      if (fresh.length > 0 && await deliver(fresh)) {
        current = { ...current, seen: [...current.seen, ...fresh.map(item => item.key)].slice(-SEEN_LIMIT) }
        log(`delivered ${fresh.length} Taskboard item${fresh.length === 1 ? '' : 's'}`)
      }
      await state.save(current)
      await pause(canWait ? gapMs : pollMs)
    }
  } finally { unsubscribe() }
  if (stopped && !signal?.aborted) throw stopped

  async function readThread() {
    const result = await rpc.call('thread/read', { threadId: thread })
    const t = result?.thread
    if (t?.id !== thread || t.ephemeral || !['idle', 'active'].includes(t.status?.type)) {
      throw new AdapterError('The Codex thread must already be loaded and persistent in the interactive client')
    }
    return t
  }

  async function historyRuns() {
    scannedAt = now()
    return (await history()).map(runFromItem).filter(Boolean)
  }

  // thread/items/list needs a store that pages items; legacy-history threads
  // reject it, so their items come from the turns thread/read returns.
  async function history() {
    const items = []
    const cursors = new Set()
    let cursor
    do {
      let page
      try {
        page = await rpc.call('thread/items/list', { threadId: thread, limit: 100, sortDirection: 'asc', ...(cursor ? { cursor } : {}) })
      } catch (error) {
        if (cursor || error.code === undefined) throw error
        return legacyHistory()
      }
      if (!Array.isArray(page?.data)) throw new AdapterError('Invalid Codex history page')
      items.push(...page.data.map(entry => entry.item))
      cursor = page.nextCursor
      if (cursor && cursors.has(cursor)) throw new AdapterError('Codex history pagination did not advance')
      cursors.add(cursor)
    } while (cursor)
    return items
  }

  async function legacyHistory() {
    const turns = (await rpc.call('thread/read', { threadId: thread, includeTurns: true }))?.thread?.turns
    if (!Array.isArray(turns)) throw new AdapterError('Invalid Codex thread history')
    return turns.flatMap(turn => Array.isArray(turn?.items) ? turn.items : [])
  }

  // Never passes model, approval, sandbox or cwd overrides: Taskboard items
  // cannot change the thread's authority. Reports whether the input landed.
  async function deliver(fresh) {
    try {
      const t = await readThread()
      const busy = t.status.type === 'active'
      const input = [{ type: 'text', text: deliveryText(fresh, busy), text_elements: [] }]
      if (busy && activeTurn) {
        try {
          await rpc.call('turn/steer', { threadId: thread, expectedTurnId: activeTurn, input })
          return true
        } catch (error) {
          log(`steer refused (${error.message}); queueing the items as the next turn`)
        }
      }
      // On an active turn whose ID this adapter has not seen, Codex queues it.
      await rpc.call('turn/start', { threadId: thread, input })
      return true
    } catch (error) {
      log(`delivery failed: ${error.message}`)
      return false
    }
  }
}
