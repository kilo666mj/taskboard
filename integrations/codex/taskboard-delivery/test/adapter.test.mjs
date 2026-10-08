import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm, stat } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { attach, WAIT_SECONDS } from '../adapter.mjs'
import { deliveryText, inboxItems, openRuns, runFromItem } from '../inbox.mjs'
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

function fakeCodex({ items = [], status = 'idle', inboxes, onInbox = () => {}, steer } = {}) {
  const calls = []
  const listeners = new Set()
  const thread = { status }
  const queue = [...inboxes]
  const rpc = {
    calls,
    thread,
    notify: note => { for (const listener of listeners) listener(note) },
    onNotification(listener) { listeners.add(listener); return () => listeners.delete(listener) },
    async call(method, params) {
      calls.push({ method, params })
      switch (method) {
        case 'thread/read': return { thread: { id: THREAD, ephemeral: false, status: { type: thread.status } } }
        case 'thread/items/list': return { data: items.map(item => ({ item, turnId: 'turn-0' })) }
        case 'mcpServer/tool/call': {
          const next = queue.length > 1 ? queue.shift() : queue[0]
          onInbox(calls.filter(call => call.method === 'mcpServer/tool/call').length, params)
          return { content: [{ type: 'text', text: JSON.stringify(next) }] }
        }
        case 'turn/steer': if (steer) return steer(params); return { turnId: params.expectedTurnId }
        case 'turn/start': return { turn: { id: 'turn-new' } }
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
          rpc.notify({ method: 'turn/started', params: { threadId: THREAD, turn: { id: 'turn-9' } } })
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
    assert.deepEqual(saved.seen, ['message:M1', 'control:C1:requested'])
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
        if (count === 1) rpc.notify({ method: 'turn/started', params: { threadId: THREAD, turn: { id: 'turn-old' } } })
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
