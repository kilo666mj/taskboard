/** An MCP tool's name as the session lists it. */
export type McpToolName = `mcp__${string}__${string}`

/** A Taskboard run this session started or claimed, and the inbox tool of its server. */
export type WatchedRun = { taskId: string; runId: string; inboxTool: McpToolName }

declare module 'claude-code' {
  interface PluginState {
    'taskboard-idle-inbox': {
      /** Runs to check, oldest first, at most 20. */
      runs: WatchedRun[]
      /** Keys of inbox items the agent has already been woken for. */
      seen: string[]
    }
  }
}
