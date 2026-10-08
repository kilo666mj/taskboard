import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm, stat } from 'node:fs/promises'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { setTimeout as sleep } from 'node:timers/promises'

import { attach, WAIT_SECONDS } from '../adapter.mjs'
import { deliveryText, inboxItems, openRuns, pendingItems, runFromItem, SEEN_LIMIT } from '../inbox.mjs'
import { connectCodex } from '../rpc.mjs'
import { openState } from '../state.mjs'

const THREAD = '0199a000-0000-7000-8000-000000000001'
const startItem = (runId, taskId, extra = {}) => ({
  type: 'mcpToolCall', id: `call-${runId}`, server: 'switchboard', tool: 'taskboard_task_start', status: 'completed',
  arguments: {}, result: { content: [{ type: 'text', text: JSON.stringify({ run: { id: runId, task_id: taskId } }) }] }, ...extra,
})
const inbox = (fields = {}) => ({
  digest: 'd1', count: 0, runs: [{ task_id: 'T1', run_id: 'R1', task_status: 'active', active: true }],
  controls: [], session_requests: [], discussions: [], escalations: [], messages: [], ...fields,
})

function fakeCodex({ items = [], turns = [{ id: 'turn-0', status: 'completed', items }], status = 'idle', inboxes, onInbox = () => {}, steer, start, legacy = false } = {}) {
  const calls = []
  const listeners = new Set()
  const thread = { status }
  const queue = [...inboxes]
  const unsupported = method => Object.assign(new Error(`${method} is not supported yet`), { code: -32601 })
  const rpc = {
    calls,
    thread,
    // Oldest first, as Codex stores them; tests add turns as the thread goes on.
    turns,
    notify: note => { for (const listener of listeners) listener(note) },
    onNotification(listener) { listeners.add(listener); return () => listeners.delete(listener) },
    async call(method, params) {
      calls.push({ method, params })
      switch (method) {
        case 'thread/read': return { thread: { id: THREAD, ephemeral: false, status: { type: thread.status }, turns: params.includeTurns ? rpc.turns : [] } }
        case 'thread/turns/list': {
          if (legacy) throw unsupported(method)
          const newest = rpc.turns.toReversed()
          const from = Number(params.cursor ?? 0)
          const page = newest.slice(from, from + params.limit).map(turn => ({ id: turn.id, status: turn.status, items: [] }))
          return { data: page, nextCursor: from + params.limit < newest.length ? String(from + params.limit) : null }
        }
        case 'thread/items/list': {
          if (legacy) throw unsupported(method)
          const turn = rpc.turns.find(candidate => candidate.id === params.turnId)
          if (!turn) throw Object.assign(new Error('turn not found'), { code: -32600 })
          return { data: turn.items.map(item => ({ item, turnId: turn.id })) }
        }
        case 'mcpServer/tool/call': {
          const next = queue.length > 1 ? queue.shift() : queue[0]
          onInbox(calls.filter(call => call.method === 'mcpServer/tool/call').length, params)
          return { content: [{ type: 'text', text: JSON.stringify(next) }] }
        }
        case 'turn/steer': if (steer) return steer(params); return { turnId: params.expectedTurnId }
        case 'turn/start': if (start) return start(params); return { turn: { id: 'turn-new' } }
        default: throw new Error(`unexpected ${method}`)
      }
    },
  }
  return rpc
}

async function withState(body) {
  const directory = await mkdtemp(join(tmpdir(), 'taskboard-delivery-'))
  const state = await openState(join(directory, 'state'), THREAD)
  try { await body(state, join(directory, 'state')) } finally { await state.close(); await rm(directory, { recursive: true }) }
}

test('learns runs from history, waits on the digest, starts a turn when idle and steers when busy', async () => {
  await withState(async (state, directory) => {
    const controller = new AbortController()
    const rpc = fakeCodex({
      items: [startItem('R1', 'T1'), { type: 'userMessage', id: 'u1', content: [] }],
      inboxes: [
        inbox(),
        inbox({ digest: 'd2', count: 1, messages: [{ id: 'M1', task_id: 'T1', kind: 'instruction', requires_ack: true }] }),
        inbox({ digest: 'd3', count: 2, messages: [{ id: 'M1', task_id: 'T1', kind: 'instruction', requires_ack: true }],
          controls: [{ id: 'C1', task_id: 'T1', target_run_id: 'R1', kind: 'pause', status: 'requested' }] }),
        inbox({ digest: 'd4', runs: [{ task_id: 'T1', run_id: 'R1', task_status: 'done', active: false }] }),
      ],
      onInbox(count) {
        if (count === 3) {
          rpc.thread.status = 'active'
          rpc.turns.push({ id: 'turn-9', status: 'inProgress', items: [] })
        }
        if (count === 4) controller.abort()
      },
    })
    await attach({ rpc, thread: THREAD, state, signal: controller.signal, wait: async () => {} })

    const reads = rpc.calls.filter(call => call.method === 'mcpServer/tool/call')
    assert.equal(reads.length, 4)
    assert.equal(reads[0].params.server, 'switchboard')
    assert.equal(reads[0].params.tool, 'taskboard_task_inbox')
    assert.equal(reads[0].params.threadId, THREAD)
    assert.deepEqual(reads[0].params.arguments, { runs: [{ task_id: 'T1', run_id: 'R1' }] })
    assert.equal(reads[1].params.arguments.wait_seconds, WAIT_SECONDS)
    assert.equal(reads[1].params.arguments.digest, 'd1')

    const starts = rpc.calls.filter(call => call.method === 'turn/start')
    const steers = rpc.calls.filter(call => call.method === 'turn/steer')
    assert.equal(starts.length, 1)
    assert.match(starts[0].params.input[0].text, /instruction message M1 on task T1 needs acknowledgement/)
    assert.deepEqual(Object.keys(starts[0].params).sort(), ['input', 'threadId'])
    assert.equal(steers.length, 1)
    assert.equal(steers[0].params.expectedTurnId, 'turn-9')
    assert.match(steers[0].params.input[0].text, /pause control C1/)
    assert.doesNotMatch(steers[0].params.input[0].text, /message M1/)

    const saved = JSON.parse(await readFile(join(directory, 'state.json'), 'utf8'))
    assert.deepEqual(saved.runs, [])
    // The last inbox lists nothing, so the delivered keys are forgotten.
    assert.deepEqual(saved.seen, [])
    assert.equal((await stat(join(directory, 'state.json'))).mode & 0o777, 0o600)
  })
})

test('a refused steer is queued as the next turn, and a delivered item is not repeated', async () => {
  await withState(async state => {
    const controller = new AbortController()
    const rpc = fakeCodex({
      status: 'active',
      items: [startItem('R1', 'T1')],
      inboxes: [inbox({ digest: 'd2', messages: [{ id: 'M1', task_id: 'T1', kind: 'note', requires_ack: false }] })],
      steer: () => { throw new Error('turn mismatch') },
      onInbox(count) {
        if (count === 1) rpc.turns.push({ id: 'turn-old', status: 'inProgress', items: [] })
        if (count === 3) controller.abort()
      },
    })
    await attach({ rpc, thread: THREAD, state, signal: controller.signal, wait: async () => {} })
    assert.equal(rpc.calls.filter(call => call.method === 'turn/steer').length, 1)
    assert.equal(rpc.calls.filter(call => call.method === 'turn/start').length, 1)
    assert.deepEqual(state.get().seen, ['message:M1'])
  })
})

test('runs learned during a read survive it, and older servers are polled without waiting', async () => {
  await withState(async state => {
    const controller = new AbortController()
    const pauses = []
    const legacy = { ...inbox(), digest: undefined }
    const rpc = fakeCodex({
      items: [startItem('R1', 'T1')],
      inboxes: [legacy],
      onInbox(count) {
        if (count === 1) rpc.notify({ method: 'item/completed', params: { threadId: THREAD, turnId: 't', item: startItem('R2', 'T2') } })
        if (count === 3) controller.abort()
      },
    })
    await attach({ rpc, thread: THREAD, state, signal: controller.signal, pollMs: 30_000, gapMs: 2_000, wait: async ms => { pauses.push(ms) } })
    const reads = rpc.calls.filter(call => call.method === 'mcpServer/tool/call')
    assert.ok(reads.every(read => read.params.arguments.wait_seconds === undefined))
    assert.deepEqual(reads[1].params.arguments.runs, [{ task_id: 'T1', run_id: 'R1' }, { task_id: 'T2', run_id: 'R2' }])
    assert.ok(pauses.includes(30_000))
    assert.ok(!pauses.includes(2_000))
  })
})

test('stops when the thread is closed', async () => {
  await withState(async state => {
    const rpc = fakeCodex({
      items: [startItem('R1', 'T1')],
      inboxes: [inbox()],
      onInbox(count) { if (count === 2) rpc.notify({ method: 'thread/closed', params: { threadId: THREAD } }) },
    })
    await assert.rejects(attach({ rpc, thread: THREAD, state, wait: async () => {} }), /thread closed/)
  })
})

test('only completed Taskboard start or claim calls name runs', () => {
  assert.deepEqual(runFromItem(startItem('R1', 'T1')), { taskId: 'T1', runId: 'R1', server: 'switchboard', inboxTool: 'taskboard_task_inbox' })
  assert.deepEqual(runFromItem({ ...startItem('R1', 'T1'), server: 'taskboard', tool: 'task_claim', result: { content: [], structuredContent: { run: { id: 'R1', task_id: 'T1' } } } }),
    { taskId: 'T1', runId: 'R1', server: 'taskboard', inboxTool: 'task_inbox' })
  assert.equal(runFromItem({ ...startItem('R1', 'T1'), status: 'failed' }), undefined)
  assert.equal(runFromItem({ ...startItem('R1', 'T1'), server: 'other', tool: 'task_start' }), undefined)
  assert.equal(runFromItem({ ...startItem('R1', 'T1'), tool: 'taskboard_task_list' }), undefined)
  assert.equal(runFromItem({ type: 'userMessage', id: 'u', content: [] }), undefined)
})

test('item keys and text match the Claude Code plugin and leave out people\'s text', () => {
  const items = inboxItems(inbox({
    discussions: [{ id: 'D1', task_id: 'T1', status: 'active', messages: [{ id: 'X1', body: 'secret plan' }] }],
    escalations: [{ id: 'E1', task_id: 'T1', status: 'answered', blocking: true }],
  }))
  assert.deepEqual(items.map(item => item.key), ['discussion:D1:X1', 'escalation:E1:answered'])
  const text = deliveryText(items, false)
  assert.match(text, /claim the queued task for a new run/)
  assert.doesNotMatch(text, /secret plan/)
  assert.match(deliveryText(items, true), /arrived while you were working/)
  assert.deepEqual(openRuns([{ runId: 'R1' }, { runId: 'R9' }], inbox({ runs: [{ run_id: 'R1', task_status: 'done' }] })), [{ runId: 'R9' }])
})

test('state refuses a second adapter and another thread\'s directory', async () => {
  await withState(async (state, directory) => {
    await assert.rejects(openState(directory, THREAD), /Another adapter holds/)
    await state.save({ runs: [], seen: ['message:M1'] })
    await state.close()
    await assert.rejects(openState(directory, '0199a000-0000-7000-8000-000000000002'), /another Codex thread/)
    const again = await openState(directory, THREAD)
    assert.deepEqual(again.get().seen, ['message:M1'])
    // Hand the lock back so withState can release it.
    Object.assign(state, again)
  })
})

test('concurrent saves land in call order and finish before the lock is released', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'taskboard-delivery-'))
  try {
    const state = await openState(join(directory, 'state'), THREAD)
    // Earlier snapshots are larger, so unserialized writes would finish out of order.
    for (let i = 0; i < 20; i++) void state.save({ runs: [], seen: Array.from({ length: 200 - i * 10 }, (_, j) => `message:${i}-${j}`) })
    await state.close()
    const saved = JSON.parse(await readFile(join(directory, 'state', 'state.json'), 'utf8'))
    assert.equal(saved.seen[0], 'message:19-0')
    assert.equal(saved.seen.length, 10)
  } finally { await rm(directory, { recursive: true }) }
})

test('legacy-history threads are read through their turns', async () => {
  await withState(async state => {
    const controller = new AbortController()
    const rpc = fakeCodex({
      legacy: true,
      items: [startItem('R1', 'T1')],
      inboxes: [inbox()],
      onInbox() { controller.abort() },
    })
    await attach({ rpc, thread: THREAD, state, signal: controller.signal, wait: async () => {} })
    assert.ok(rpc.calls.some(call => call.method === 'thread/read' && call.params.includeTurns === true))
    assert.deepEqual(rpc.calls.find(call => call.method === 'mcpServer/tool/call').params.arguments.runs, [{ task_id: 'T1', run_id: 'R1' }])
  })
})

test('runs started after attaching are learned from new turns without notifications', async () => {
  await withState(async state => {
    const controller = new AbortController()
    let clock = 0
    let pauses = 0
    const rpc = fakeCodex({ turns: [{ id: 'turn-0', status: 'completed', items: [] }], inboxes: [inbox({ runs: [{ task_id: 'T1', run_id: 'R1', task_status: 'done', active: false }] })] })
    await attach({
      rpc, thread: THREAD, state, signal: controller.signal, pollMs: 30_000, now: () => clock,
      wait: async ms => {
        clock += ms
        pauses++
        if (pauses === 1) rpc.turns.push({ id: 'turn-1', status: 'inProgress', items: [startItem('R1', 'T1')] })
        if (pauses === 2) rpc.turns.at(-1).status = 'completed'
        if (pauses === 5) controller.abort()
      },
    })
    const reads = rpc.calls.filter(call => call.method === 'mcpServer/tool/call')
    // R1 is read once; once the inbox reports it done, later scans do not bring it back.
    assert.equal(reads.length, 1)
    assert.deepEqual(reads[0].params.arguments.runs, [{ task_id: 'T1', run_id: 'R1' }])
    // Each finished turn's items are read once; later scans only list the newest turns.
    const itemReads = turn => rpc.calls.filter(call => call.method === 'thread/items/list' && call.params.turnId === turn).length
    assert.equal(itemReads('turn-0'), 1)
    assert.equal(itemReads('turn-1'), 2)
    assert.ok(rpc.calls.filter(call => call.method === 'thread/turns/list').length >= 4)
    assert.deepEqual(state.get().runs, [])
  })
})

test('a busy thread is steered on the turn Codex reports running', async () => {
  await withState(async state => {
    const controller = new AbortController()
    const rpc = fakeCodex({
      status: 'active',
      turns: [{ id: 'turn-0', status: 'completed', items: [startItem('R1', 'T1')] }, { id: 'turn-7', status: 'inProgress', items: [] }],
      inboxes: [inbox({ messages: [{ id: 'M1', task_id: 'T1', kind: 'note', requires_ack: false }] })],
      onInbox(count) { if (count === 2) controller.abort() },
    })
    await attach({ rpc, thread: THREAD, state, signal: controller.signal, wait: async () => {} })
    const steers = rpc.calls.filter(call => call.method === 'turn/steer')
    assert.deepEqual(steers.map(call => call.params.expectedTurnId), ['turn-7'])
    assert.equal(rpc.calls.filter(call => call.method === 'turn/start').length, 0)
  })
})

test('history errors other than an unsupported method are not hidden by a whole-thread read', async () => {
  await withState(async state => {
    const rpc = fakeCodex({ items: [startItem('R1', 'T1')], inboxes: [inbox()] })
    const call = rpc.call
    rpc.call = async (method, params) => {
      if (method === 'thread/turns/list') throw Object.assign(new Error('thread not loaded'), { code: -32600 })
      return call(method, params)
    }
    await assert.rejects(attach({ rpc, thread: THREAD, state, wait: async () => {} }), /thread not loaded/)
    assert.ok(!rpc.calls.some(entry => entry.method === 'thread/read' && entry.params.includeTurns))
  })
})

test('stops once the thread is no longer loaded', async () => {
  await withState(async state => {
    let clock = 0
    const rpc = fakeCodex({ items: [startItem('R1', 'T1')], inboxes: [inbox()] })
    await assert.rejects(attach({
      rpc, thread: THREAD, state, pollMs: 30_000, now: () => clock,
      wait: async () => { clock += 30_000; rpc.thread.status = 'notLoaded' },
    }), /no longer loaded/)
  })
})

test('a finished run whose items failed to deliver is read again', async () => {
  await withState(async state => {
    const controller = new AbortController()
    // Ends the loop if the expected reads never come, so a regression fails instead of hanging.
    let rounds = 0
    let starts = 0
    const rpc = fakeCodex({
      items: [startItem('R1', 'T1')],
      inboxes: [inbox({ digest: 'd2', runs: [{ task_id: 'T1', run_id: 'R1', task_status: 'done', active: false }],
        messages: [{ id: 'M1', task_id: 'T1', kind: 'note', requires_ack: false }] })],
      start: () => { starts++; if (starts === 1) throw new Error('busy'); return { turn: { id: 'turn-new' } } },
      onInbox(count) { if (count === 2) controller.abort() },
    })
    await attach({ rpc, thread: THREAD, state, signal: controller.signal, wait: async () => { if (++rounds > 20) controller.abort() } })
    const reads = rpc.calls.filter(call => call.method === 'mcpServer/tool/call')
    assert.equal(reads.length, 2)
    assert.deepEqual(reads[1].params.arguments.runs, [{ task_id: 'T1', run_id: 'R1' }])
    assert.equal(starts, 2)
    assert.deepEqual(state.get().seen, ['message:M1'])
    assert.deepEqual(state.get().runs, [])
  })
})

test('runs from different Taskboard servers are read through their own server, without waiting', async () => {
  await withState(async state => {
    const controller = new AbortController()
    // Ends the loop if the expected reads never come, so a regression fails instead of hanging.
    let rounds = 0
    const rpc = fakeCodex({
      items: [startItem('R1', 'T1'), startItem('R2', 'T2', { server: 'taskboard-direct' })],
      inboxes: [inbox()],
      onInbox(count) { if (count === 4) controller.abort() },
    })
    await attach({ rpc, thread: THREAD, state, signal: controller.signal, wait: async () => { if (++rounds > 20) controller.abort() } })
    const reads = rpc.calls.filter(call => call.method === 'mcpServer/tool/call')
    assert.deepEqual(reads.slice(0, 2).map(read => [read.params.server, read.params.arguments.runs.map(run => run.run_id)]), [['switchboard', ['R1']], ['taskboard-direct', ['R2']]])
    assert.ok(reads.every(read => read.params.arguments.wait_seconds === undefined))
  })
})

test('delivered keys stay remembered while listed, and at most SEEN_LIMIT are outstanding', () => {
  const items = count => Array.from({ length: count }, (_, i) => ({ key: `message:M${i}`, line: `m${i}` }))
  // 600 new items: only SEEN_LIMIT go out now.
  let split = pendingItems([], items(600))
  assert.equal(split.fresh.length, SEEN_LIMIT)
  assert.equal(split.pending.length, 600)
  let seen = [...split.kept, ...split.fresh.map(item => item.key)]
  // Nothing already delivered is offered again, however long the inbox.
  split = pendingItems(seen, items(600))
  assert.equal(split.fresh.length, 0)
  assert.equal(split.pending.length, 100)
  // Once the agent clears 150, the rest go out and the cleared keys are forgotten.
  split = pendingItems(seen, items(600).slice(150))
  assert.equal(split.kept.length, 350)
  assert.deepEqual(split.fresh.map(item => item.key), items(600).slice(500).map(item => item.key))
  seen = [...split.kept, ...split.fresh.map(item => item.key)]
  assert.equal(seen.length, 450)
})

test('a connection that never opens is closed when it times out', async () => {
  // Accepts the TCP connection and never answers the WebSocket handshake.
  const server = createServer()
  const sockets = []
  const closed = new Promise(resolve => server.on('connection', socket => { sockets.push(socket); socket.on('close', () => resolve(true)) }))
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  try {
    await assert.rejects(connectCodex(`ws://127.0.0.1:${server.address().port}`, { timeout: 100 }), /connection closed/)
    assert.equal(await Promise.race([closed, sleep(2_000).then(() => false)]), true, 'the adapter left the connection open')
  } finally {
    for (const socket of sockets) socket.destroy()
    server.close()
  }
})
