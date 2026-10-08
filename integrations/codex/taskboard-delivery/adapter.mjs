import { setTimeout as sleep } from 'node:timers/promises'

import { addRun, deliveryText, inboxItems, openRuns, runFromItem, SEEN_LIMIT, toolResult } from './inbox.mjs'
import { AdapterError } from './rpc.mjs'

// Server cap on task_inbox waits; Codex's default MCP tool timeout is 60 s.
export const WAIT_SECONDS = 25
const ENDED = ['thread/closed', 'thread/archived', 'thread/deleted', 'connection/closed']
// Codex answers a history method that the thread's store cannot page with
// "method not found"; other errors (such as an unloaded thread) are real.
const UNSUPPORTED = -32601
const TURN_PAGE = 20

// Delivers Taskboard inbox items into one loaded Codex thread. Runs are
// learned from the thread's own task_start and task_claim calls; the inbox is
// read through the thread's own MCP server, so the adapter holds no Taskboard
// credential. An idle thread gets a turn of its own; a busy one is steered.
//
// The adapter does not resume the thread, so this connection is not
// subscribed to its events and notifications may never arrive. Every pollMs
// it reads the turns started since its last look to learn new runs, and it
// asks for the thread's running turn when it delivers.
export async function attach({ rpc, thread, state, signal, log = () => {},
  pollMs = 30_000, gapMs = 2_000, retryMs = 5_000, maxRetryMs = 300_000, wait = sleep, now = Date.now }) {
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
    if (note.method === 'turn/completed') kick()
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
  // The newest finished turn whose items have been read; later scans stop there.
  let scannedThrough
  // Set once the thread's store refuses paged history; its turns then come
  // whole from thread/read.
  let legacy = false
  async function remember(run) {
    known.add(run.runId)
    current = { ...current, runs: addRun(current.runs, run) }
    await state.save(current)
    digest = undefined
    kick()
  }

  try {
    await readThread()
    for (const run of await newRuns()) {
      known.add(run.runId)
      current = { ...current, runs: addRun(current.runs, run) }
    }
    await state.save(current)
    log(`watching ${current.runs.length} Taskboard run${current.runs.length === 1 ? '' : 's'} in thread ${thread}`)

    while (!signal?.aborted && !stopped) {
      if (now() - scannedAt >= pollMs) {
        try {
          const status = (await rpc.call('thread/read', { threadId: thread }))?.thread?.status?.type
          if (status === 'notLoaded') { stopped = new AdapterError('Codex thread is no longer loaded; delivery stopped'); break }
          for (const run of await newRuns()) if (!known.has(run.runId)) await remember(run)
        } catch (error) {
          if (signal?.aborted || stopped) break
          log(`history unavailable: ${error.message}`)
        }
      }
      const runs = current.runs
      if (runs.length === 0) { await pause(pollMs); continue }
      // Each run is read through the server and tool that started it. A digest
      // covers one read, so the adapter waits only when every run shares one.
      const groups = groupRuns(runs)
      const waiting = groups.length === 1 && canWait === true && digest !== undefined
      let inbox
      try {
        const inboxes = []
        for (const group of groups) {
          const result = await rpc.call('mcpServer/tool/call', {
            server: group.server, threadId: thread, tool: group.inboxTool,
            arguments: { runs: group.runs.map(run => ({ task_id: run.taskId, run_id: run.runId })), ...(waiting ? { wait_seconds: WAIT_SECONDS, digest } : {}) },
          }, (WAIT_SECONDS + 20) * 1000)
          if (result?.isError) throw new AdapterError(`task_inbox failed: ${toolResult(result)?.error ?? 'error result'}`)
          const read = toolResult(result)
          if (!read || !Array.isArray(read.runs)) throw new AdapterError('task_inbox returned an unexpected result')
          inboxes.push(read)
        }
        inbox = mergeInboxes(inboxes)
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

      const seen = new Set(current.seen)
      const fresh = inboxItems(inbox).filter(item => !seen.has(item.key))
      // Drop finished runs only once their items landed, so a failed delivery
      // reads them again rather than losing them.
      const delivered = fresh.length === 0 || await deliver(fresh)
      if (delivered) {
        const open = openRuns(current.runs, inbox)
        if (open.length !== current.runs.length) current = { ...current, runs: open }
      }
      if (fresh.length > 0 && delivered) {
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

  // Runs named in the turns since the last scan, oldest first. Only the newest
  // turn can still be running; it is read again until it finishes.
  async function newRuns() {
    scannedAt = now()
    const turns = await unscannedTurns()
    const finished = turns.find(turn => turn.status !== 'inProgress')
    const runs = turns.toReversed().flatMap(turn => turn.items.map(runFromItem).filter(Boolean))
    if (finished) scannedThrough = finished.id
    return runs
  }

  // The turns after scannedThrough, newest first, with their items.
  async function unscannedTurns() {
    if (!legacy) {
      try { return await pagedTurns() } catch (error) {
        if (error.code !== UNSUPPORTED) throw error
        legacy = true
      }
    }
    const turns = []
    for (const turn of (await legacyTurns()).toReversed()) {
      if (turn?.id === scannedThrough) break
      turns.push({ id: turn?.id, status: turn?.status, items: Array.isArray(turn?.items) ? turn.items : [] })
    }
    return turns
  }

  async function pagedTurns() {
    const turns = []
    const cursors = new Set()
    let cursor
    pages: do {
      const page = await rpc.call('thread/turns/list', { threadId: thread, limit: TURN_PAGE, sortDirection: 'desc', itemsView: 'notLoaded', ...(cursor ? { cursor } : {}) })
      if (!Array.isArray(page?.data)) throw new AdapterError('Invalid Codex turn page')
      for (const turn of page.data) {
        if (typeof turn?.id !== 'string') throw new AdapterError('Invalid Codex turn')
        if (turn.id === scannedThrough) break pages
        turns.push({ id: turn.id, status: turn.status })
      }
      cursor = advance(cursors, page.nextCursor)
    } while (cursor)
    for (const turn of turns) turn.items = await turnItems(turn.id)
    return turns
  }

  async function turnItems(turnId) {
    const items = []
    const cursors = new Set()
    let cursor
    do {
      const page = await rpc.call('thread/items/list', { threadId: thread, turnId, limit: 100, sortDirection: 'asc', ...(cursor ? { cursor } : {}) })
      if (!Array.isArray(page?.data)) throw new AdapterError('Invalid Codex history page')
      items.push(...page.data.map(entry => entry.item))
      cursor = advance(cursors, page.nextCursor)
    } while (cursor)
    return items
  }

  function advance(cursors, cursor) {
    if (cursor && cursors.has(cursor)) throw new AdapterError('Codex history pagination did not advance')
    cursors.add(cursor)
    return cursor
  }

  // Legacy-history threads cannot be paged, so each look reads them whole.
  async function legacyTurns() {
    const turns = (await rpc.call('thread/read', { threadId: thread, includeTurns: true }))?.thread?.turns
    if (!Array.isArray(turns)) throw new AdapterError('Invalid Codex thread history')
    return turns
  }

  // The thread's running turn, which turn/steer must name.
  async function runningTurn() {
    let newest
    if (!legacy) {
      try {
        newest = (await rpc.call('thread/turns/list', { threadId: thread, limit: 1, sortDirection: 'desc', itemsView: 'notLoaded' }))?.data?.[0]
      } catch (error) {
        if (error.code !== UNSUPPORTED) throw error
        legacy = true
      }
    }
    if (legacy) newest = (await legacyTurns()).at(-1)
    return newest?.status === 'inProgress' ? newest.id : undefined
  }

  // Never passes model, approval, sandbox or cwd overrides: Taskboard items
  // cannot change the thread's authority. Reports whether the input landed.
  async function deliver(fresh) {
    try {
      const t = await readThread()
      const busy = t.status.type === 'active'
      const input = [{ type: 'text', text: deliveryText(fresh, busy), text_elements: [] }]
      const turn = busy ? await runningTurn() : undefined
      if (turn) {
        try {
          await rpc.call('turn/steer', { threadId: thread, expectedTurnId: turn, input })
          return true
        } catch (error) {
          log(`steer refused (${error.message}); queueing the items as the next turn`)
        }
      }
      // On a turn that is still running, Codex queues this as the next turn.
      await rpc.call('turn/start', { threadId: thread, input })
      return true
    } catch (error) {
      log(`delivery failed: ${error.message}`)
      return false
    }
  }
}

function groupRuns(runs) {
  const groups = new Map()
  for (const run of runs) {
    const key = JSON.stringify([run.server, run.inboxTool])
    if (!groups.has(key)) groups.set(key, { server: run.server, inboxTool: run.inboxTool, runs: [] })
    groups.get(key).runs.push(run)
  }
  return [...groups.values()]
}

// One server's inbox keeps its digest; several are merged without one.
function mergeInboxes(inboxes) {
  if (inboxes.length === 1) return inboxes[0]
  const merged = { count: 0 }
  for (const read of inboxes) {
    merged.count += read.count ?? 0
    for (const [key, value] of Object.entries(read)) if (Array.isArray(value)) merged[key] = [...(merged[key] ?? []), ...value]
  }
  return merged
}
