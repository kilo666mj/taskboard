import { expect, mock, test } from 'claude-code/testing'

const START = 'mcp__switchboard__taskboard_task_start'
const INBOX = 'mcp__switchboard__taskboard_task_inbox'

const emptyInbox = {
  count: 0,
  runs: [{ task_id: 'T1', run_id: 'R1', task_status: 'active', active: true }],
  controls: [],
  session_requests: [],
  discussions: [],
  escalations: [],
  messages: [],
}

test('polls only while idle, backs off, and wakes once per new item', async ($, on) => {
  const clock = mock.clock(on)
  let inbox: unknown = emptyInbox
  const polls: unknown[] = []
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
    polls.push(e)
    return { result: {}, text: JSON.stringify(inbox) }
  })

  await $.turn.start({ text: 'work', turnId: 't1' })
  await $.tool.call({ tool: START, title: 'Work', checklist: ['One'] })
  await clock.advance(120_000)
  expect(polls.length).toBe(0)

  await $.turn.complete({ answer: '', durationMs: 1, isAborted: false, turnId: 't1', reason: 'answer' })
  await clock.advance(30_000)
  expect(polls.length).toBe(1)
  await clock.advance(59_000)
  expect(polls.length).toBe(1)
  await clock.advance(1_000)
  expect(polls.length).toBe(2)
  expect(prompts.length).toBe(0)

  inbox = {
    ...emptyInbox,
    count: 1,
    controls: [{ id: 'C1', task_id: 'T1', target_run_id: 'R1', kind: 'pause', status: 'requested' }],
  }
  await clock.advance(120_000)
  expect(polls.length).toBe(3)
  expect(prompts.length).toBe(1)
  expect(prompts[0]).toContain('pause control C1')

  await clock.advance(600_000)
  expect(polls.length).toBe(3)

  await $.turn.start({ text: '', turnId: 't2' })
  await $.turn.complete({ answer: '', durationMs: 1, isAborted: false, turnId: 't2', reason: 'answer' })
  await clock.advance(30_000)
  expect(polls.length).toBe(4)
  expect(prompts.length).toBe(1)

  inbox = { ...emptyInbox, runs: [{ task_id: 'T1', run_id: 'R1', task_status: 'done', active: false }] }
  await clock.advance(60_000)
  expect(polls.length).toBe(5)
  await clock.advance(3_600_000)
  expect(polls.length).toBe(5)
})
