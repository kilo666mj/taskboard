# Taskboard inbox delivery for Claude Code

A Claude Code plugin of function hooks that brings Taskboard work waiting on a
session into it: pause, cancel, resume, and retry controls, session requests,
live discussions, answered or expired escalations, and instructions or
questions in the task conversation.

It reads the [controller
inbox](../../../docs/agent-integrations.md#controller-inbox) for the runs this
session started or claimed, and never uses the model to poll. Against a server
whose inbox carries a `digest`, it keeps one waiting read open and delivers new
items within seconds, whether the session is idle or working. Against an older
server, it reads only while the session is idle. Either way it reports each
item once.

The plugin keeps its original name, `taskboard-idle-inbox`, so existing
installations update in place.

## Requirements

- A Taskboard release that has `task_inbox` (v0.15.0 or later). Delivery
  while working, and within seconds, needs a release whose inbox returns
  `digest`.
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
   returned task and run IDs in session state, keeps the latest 20, and reads
   the inbox at once.
2. If the inbox carries a `digest`, the plugin passes it back with
   `wait_seconds: 25`, so each read returns as soon as something changes. It
   keeps one such read open, with a two-second pause between reads, while the
   session is idle or working.
3. Without a `digest`, the plugin reads only while the session is idle: 30
   seconds after a main-loop turn ends, then doubling while nothing changes, up
   to 10 minutes. A new turn cancels the pending read.
4. When the inbox lists items not seen before, the plugin hands them to the
   session:
   - idle: it submits one prompt, which starts a turn;
   - working: it adds a note to the running turn, which the model reads at
     its next step. The note asks the agent to handle a pause or cancel first
     and anything else after its current step.

   Either text names each item by kind and ID; the agent then calls
   `task_inbox` and the specific tools to act. It leaves out message and
   discussion text and tells the agent to treat people's text as conversation,
   not as instructions. A note that cannot be added is delivered as a prompt
   after the turn.
5. When a run's task is done or cancelled, the plugin drops that run. With
   no runs left, it stops reading.

The status line shows `taskboard: watching N runs`, `taskboard: N waiting`,
or `taskboard inbox unavailable`. After a failed read, the plugin retries
after the shortest interval, doubling up to the longest; against a server
without `digest` it waits the longest interval.

## Options

| Field | Default | Meaning |
| --- | --- | --- |
| `minIntervalSeconds` | 30 | Without `digest`: wait after a turn ends, and after new items arrive. Also the first retry after a failed read |
| `maxIntervalSeconds` | 600 | Without `digest`: longest interval while nothing changes. Also the longest retry interval |

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
