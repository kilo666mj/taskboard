import { expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'

import { taskUrl } from '../hooks/register'

const START = 'mcp__switchboard__taskboard_task_start'
const INBOX = 'mcp__switchboard__taskboard_task_inbox'

const inbox = {
  count: 1,
  runs: [{ task_id: 'T1', run_id: 'R1', task_status: 'active', active: true }],
  controls: [],
  session_requests: [],
  discussions: [],
  escalations: [],
  messages: [{ id: 'M1', task_id: 'T1', kind: 'instruction', requires_ack: false }],
}

function world(on: On) {
  const opens: string[] = []
  const lists: { server: string; tool: string; args: unknown }[] = []
  const clock = mock.clock(on)
  on('session.start', ($, e) => ({ cwd: e.cwd }))
  on('ui.status', () => ({ value: undefined }))
  on('turn.start', ($, e) => ({ turnId: e.turnId }))
  on('turn.complete', () => ({ text: '' }))
  on('prompt.submit', ($, e) => ({ text: e.text }))
  on('command.register', ($, e) => ({ value: { command: e.name } }) as never)
  on('ui.open', ($, e) => {
    opens.push(`${e.id}:${e.columns}`)
    return { value: { isPlaced: true } }
  })
  on('mcp.call', ($, e) => {
    lists.push({ server: e.server, tool: e.tool, args: e.args })
    const tasks = [{ id: 'Q1', title: 'Rerun flaky CI', status: 'queued', visibility: 'agent' }]
    return { value: { content: [{ type: 'text', text: JSON.stringify({ tasks }) }], isError: false } } as never
  })
  on('tool.call', { tool: START }, () => ({
    result: {},
    text: JSON.stringify({ run: { id: 'R1', task_id: 'T1' }, task: { id: 'T1', title: 'Port the pane' } }),
  }))
  on('tool.call', { tool: INBOX }, () => ({ result: {}, text: JSON.stringify(inbox) }))
  return { opens, lists, clock }
}

test('task links follow the board URL, or are left out without one', async () => {
  expect(taskUrl('https://taskboard.example.com/', 'T 1')).toBe('https://taskboard.example.com/?task=T%201')
  expect(taskUrl('  ', 'T1')).toBeUndefined()
})

test('the first start opens the pane with the run, its inbox and the queue', { options: { boardUrl: 'https://taskboard.example.com' } }, async ($, on) => {
  const { opens, lists, clock } = world(on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.tool.call({ tool: START, title: 'Port the pane', checklist: ['One'] })
  await clock.settle()

  expect(opens).toEqual(['taskboard:44'])
  expect(lists[0]?.server).toBe('switchboard')
  expect(lists[0]?.tool).toBe('taskboard_task_list')

  const ui = await $.ui.mount({
    plugin: 'taskboard-idle-inbox',
    surface: 'terminal',
    component: 'Pane',
    requestId: 'taskboard',
    props: { title: 'Taskboard', isFocused: false },
  } as never)
  const links = await ui.findAll({ type: 'Link' })
  expect(links.map(link => (link.props as { href: string }).href)).toEqual([
    'https://taskboard.example.com/?task=T1',
    'https://taskboard.example.com/?task=Q1',
  ])
  expect((await ui.find({ key: 'refresh' }))?.type).toBe('Button')

  // A second start does not reopen a pane the person may have closed.
  await $.tool.call({ tool: START, title: 'More', checklist: ['Two'] })
  expect(opens.length).toBe(1)
})
