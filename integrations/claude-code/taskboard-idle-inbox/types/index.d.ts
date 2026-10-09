/** An MCP tool's name as the session lists it. */
export type McpToolName = `mcp__${string}__${string}`

/** A Taskboard run this session started or claimed, and the inbox tool of its server. */
export type WatchedRun = { taskId: string; runId: string; inboxTool: McpToolName; title?: string }

/** A queued, blocked or waiting task in the pane. */
export type QueueRow = { id: string; title: string; status: string; visibility: string }

declare module 'claude-code' {
  interface PluginState {
    'taskboard-idle-inbox': {
      /** Runs to check, oldest first, at most 20. */
      runs: WatchedRun[]
      /** Keys of inbox items the agent has already been woken for. */
      seen: string[]
      /** Bumped on each load, so a read in flight from an earlier load stops. */
      generation: number
      /** One line per item the latest inbox read listed, for the pane. */
      waiting: string[]
      /** The queued, blocked and waiting tasks the latest list read returned. */
      queue: QueueRow[]
      /** Why the latest list read failed, if it did. */
      queueError: string | null
      /** Whether the pane was opened this session, so a reload does not reopen one the person closed. */
      paneOpened: boolean
    }
  }
}
