import { expect, mock, test } from 'claude-code/testing'

const START = 'mcp__switchboard__taskboard_task_start'
const INBOX = 'mcp__switchboard__taskboard_task_inbox'

const emptyInbox = {
  digest: 'd1',
  count: 0,
  runs: [{ task_id: 'T1', run_id: 'R1', task_status: 'active', active: true }],
  controls: [],
  session_requests: [],
  discussions: [],
  escalations: [],
  messages: [],
}

test('waits on a digest server and delivers while busy and while idle', async ($, on) => {
  const clock = mock.clock(on)
  const session = mock.session(on)
  let inbox: unknown = emptyInbox
  const polls: { wait_seconds?: number; digest?: string }[] = []
  const prompts: string[] = []

  on('session.start', () => ({ cwd: '/' }))
  on('ui.status', () => ({ value: undefined }))
  on('turn.start', ($, e) => ({ turnId: e.turnId }))
  on('turn.complete', () => ({ text: '' }))
  on('prompt.submit', ($, e) => {
    prompts.push(e.text)
    return { text: e.text }
  })
  on('tool.call', { tool: START }, () => ({ result: {}, text: JSON.stringify({ run: { id: 'R1', task_id: 'T1' } }) }))
  on('tool.call', { tool: INBOX }, ($, e) => {
    polls.push(e as { wait_seconds?: number; digest?: string })
    return { result: {}, text: JSON.stringify(inbox) }
  })

  await $.turn.start({ text: 'work', turnId: 't1' })
  await $.tool.call({ tool: START, title: 'Work', checklist: ['One'] })
  await clock.advance(0)
  expect(polls.length).toBe(1)
  expect(polls[0].wait_seconds).toBeUndefined()

  // Busy, but the server can wait: the reader keeps a waiting read open.
  await clock.advance(2_000)
  expect(polls.length).toBe(2)
  expect(polls[1].wait_seconds).toBe(25)
  expect(polls[1].digest).toBe('d1')

  inbox = {
    ...emptyInbox,
    digest: 'd2',
    count: 1,
    messages: [{ id: 'M1', task_id: 'T1', kind: 'instruction', requires_ack: true }],
  }
  await clock.advance(2_000)
  expect(polls.length).toBe(3)
  expect(prompts.length).toBe(0)
  const notes = session.appended()
  expect(notes.length).toBe(1)
  expect(JSON.stringify(notes[0].message)).toContain('instruction message M1 on task T1 needs acknowledgement')

  // The note was delivered, so the end of the turn does not repeat it.
  await $.turn.complete({ answer: '', durationMs: 1, isAborted: false, turnId: 't1', reason: 'answer' })
  await clock.advance(0)
  expect(polls.length).toBe(4)
  expect(polls[3].wait_seconds).toBeUndefined()
  expect(prompts.length).toBe(0)

  // Idle: a new item becomes a prompt of its own.
  inbox = {
    ...emptyInbox,
    digest: 'd3',
    count: 2,
    messages: [{ id: 'M1', task_id: 'T1', kind: 'instruction', requires_ack: true }],
    controls: [{ id: 'C1', task_id: 'T1', target_run_id: 'R1', kind: 'pause', status: 'requested' }],
  }
  await clock.advance(2_000)
  expect(polls.length).toBe(5)
  expect(prompts.length).toBe(1)
  expect(prompts[0]).toContain('pause control C1')
  expect(prompts[0]).not.toContain('message M1')
  expect(session.appended().length).toBe(1)

  // A finished task ends the watch.
  inbox = { ...emptyInbox, digest: 'd4', runs: [{ task_id: 'T1', run_id: 'R1', task_status: 'done', active: false }] }
  await clock.advance(2_000)
  expect(polls.length).toBe(6)
  await clock.advance(3_600_000)
  expect(polls.length).toBe(6)
})
