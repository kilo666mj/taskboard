# Taskboard delivery for Codex

A small Node.js process that brings Taskboard work waiting on a Codex thread
into that thread: pause, cancel, resume, and retry controls, session requests,
live discussions, answered or expired escalations, and instructions or
questions in the task conversation. It is the Codex counterpart of the
[Claude Code plugin](../../claude-code/taskboard-idle-inbox/README.md) and
reports items the same way.

The adapter attaches to the Codex app-server that the interactive client is
already using. It never starts a server, starts or resumes a thread, or answers
an approval: those stay with the interactive client.

## How it works

1. On start, it checks that the thread is loaded and persistent, then reads the
   thread's history for completed `task_start` and `task_claim` calls on a
   Taskboard MCP server. It keeps the latest 20 runs. Because it does not resume
   the thread, its connection is not subscribed to the thread's events, so every
   30 seconds it lists the turns started since its last look
   (`thread/turns/list`) and reads only their items (`thread/items/list`). It
   stops when the thread is no longer loaded. Legacy-history threads, which
   Codex cannot page, are read whole through `thread/read` with their turns.
2. It reads the [controller
   inbox](../../../docs/agent-integrations.md#controller-inbox) by asking Codex to
   call `task_inbox` on the MCP server that started each run
   (`mcpServer/tool/call`). The call
   uses the thread's own MCP connection and Taskboard principal, so the adapter
   holds no Taskboard credential. A tool called `taskboard_task_start` on a
   gateway pairs with `taskboard_task_inbox`; `task_start` on a server named
   `taskboard` pairs with `task_inbox`.
3. If the inbox carries a `digest` and every run came from one server, each
   read waits up to 25 seconds for the next change, with a two-second pause
   between reads. Otherwise it reads every 30 seconds.
4. For items it has not delivered before, it adds input to the thread:
   - an idle thread gets a new turn (`turn/start`);
   - a busy thread has its running turn, which the adapter asks Codex for just
     before delivering, steered (`turn/steer`), so the model reads the items at
     its next step. If no running turn is reported, or the steer is refused
     because that turn has just ended, Codex queues the input as the next turn.

   The text names each item by kind and ID and leaves out people's message and
   discussion text; the agent reads the details with `task_inbox` and acts
   through the specific tools. Turns are started without model, approval,
   sandbox or directory overrides, so the thread keeps its own settings.
5. When a run's task is done or cancelled, the adapter drops the run once its
   last items are delivered. It keeps running for new runs until the thread is
   closed, archived, deleted or unloaded, or the app-server goes away.

Delivered item keys and watched runs are stored in a private state directory,
so a restart does not deliver an item again. A delivered item is remembered
while the inbox still lists it; at most 500 are outstanding at once, and the
rest follow as the agent handles earlier ones.

## Requirements

- Node.js 22 or newer. The adapter has no dependencies.
- A Codex build whose app-server offers `mcpServer/tool/call` and `turn/steer`.
- Taskboard's MCP tools configured in Codex, directly or through an MCP gateway
  such as Switchboard, with a Taskboard release that has `task_inbox` (v0.15.0
  or later). Waiting reads need a release whose inbox returns `digest`.

## Running it

Start one app-server on loopback and connect the interactive client to it:

```sh
codex app-server --listen ws://127.0.0.1:4500
codex --remote ws://127.0.0.1:4500
```

Open or resume the conversation, note its thread ID, and start the adapter:

```sh
TASKBOARD_CODEX_URL=ws://127.0.0.1:4500 \
TASKBOARD_CODEX_THREAD=<thread UUID> \
TASKBOARD_CODEX_STATE_DIR="$HOME/.local/state/taskboard-codex/<thread UUID>" \
  node /path/to/taskboard/integrations/codex/taskboard-delivery/run.mjs
```

| Variable | Meaning |
| --- | --- |
| `TASKBOARD_CODEX_URL` | Loopback WebSocket origin of the shared app-server |
| `TASKBOARD_CODEX_THREAD` | UUID of the loaded, persistent thread to deliver into |
| `TASKBOARD_CODEX_STATE_DIR` | Absolute directory for this thread's state; created with mode 0700 |

Run one adapter per thread, each with its own state directory. A lock
directory inside it refuses a second adapter; if one crashed, check that its
process has stopped before removing `lock`. SIGINT or SIGTERM stops delivery,
and starting again resumes without repeating delivered items. Progress and
errors go to standard error.

## Developing

```sh
cd integrations/codex/taskboard-delivery
npm test
```

The tests drive the adapter against a scripted app-server.
