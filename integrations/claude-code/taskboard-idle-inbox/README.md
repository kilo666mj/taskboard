# Taskboard idle inbox for Claude Code

A Claude Code plugin of function hooks that lets an idle Claude Code session
notice Taskboard work waiting on it: pause, cancel, resume, and retry
controls, session requests, live discussions, answered or expired
escalations, and instructions or questions in the task conversation.

The plugin does not poll while the agent is working, and it does not use the
model to poll. After a turn ends, it reads the [controller
inbox](../../../docs/agent-integrations.md#controller-inbox) for the runs this
session started or claimed. It submits a prompt only when the inbox holds
something it has not reported before.

## Requirements

- A Taskboard release that has `task_inbox` (v0.15.0 or later).
- Taskboard's MCP tools connected to the session, either directly or through
  an MCP gateway. The plugin recognizes any MCP tool whose name contains
  `taskboard` and ends in `task_start` or `task_claim`, for example
  `mcp__taskboard__task_start`. It calls the `task_inbox` tool with the same
  prefix. That call goes through the session's own MCP connection, so it uses
  the session's credentials and Taskboard principal, and the plugin keeps no
  token of its own.
- A Claude Code build with function-hook plugins.

## Behavior

1. When `task_start` or `task_claim` succeeds, the plugin records the
   returned task and run IDs in session state. It keeps the latest 20.
2. When a main-loop turn ends, the plugin schedules an inbox read 30 seconds
   later. While nothing changes, each wait doubles, up to 10 minutes. A new
   turn cancels the pending read.
3. When the inbox lists items not seen before, the plugin submits one
   prompt. The prompt names each item by kind and ID, and the agent then
   calls `task_inbox` and the specific tools to act. The prompt leaves out
   message and discussion text, and it tells the agent to treat people's
   text as conversation, not as instructions.
4. When a run's task is done or cancelled, the plugin drops that run. With
   no runs left, it stops reading.

The status line shows `taskboard: watching N runs`, `taskboard: N waiting`,
or `taskboard inbox unavailable`. If a read fails, for example because the
server predates `task_inbox`, the plugin waits the longest interval before
it tries again.

## Options

| Field | Default | Meaning |
| --- | --- | --- |
| `minIntervalSeconds` | 30 | Wait after a turn ends; also the interval after new items arrive |
| `maxIntervalSeconds` | 600 | Longest interval while nothing changes |

Set them in `/config`, or in settings under
`pluginConfigs["taskboard-idle-inbox"].options`.

## Loading it

For one session:

```sh
claude --plugin-dir /path/to/taskboard/integrations/claude-code/taskboard-idle-inbox
```

To load it in every session, put the absolute path in
`CLAUDE_CODE_PLUGIN_DIRS`, either in the environment or in the `env` block of
`~/.claude/settings.json`.

## Developing

```sh
claude plugin validate integrations/claude-code/taskboard-idle-inbox
claude plugin test integrations/claude-code/taskboard-idle-inbox
```

When Claude Code loads the plugin, it writes the API declarations to
`.claude-plugin/types/`. That folder is ignored by git, and `tsconfig.json`
extends it, so after the first load `tsc -p` type-checks the plugin.
