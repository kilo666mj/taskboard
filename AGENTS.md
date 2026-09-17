# Taskboard agent instructions

Taskboard is the durable checklist and presence service for agent work. Keep
workflow state, transition validation, notification policy, and audit history
inside this application. Switchboard is only its MCP composition layer.

Never store prompts, chain-of-thought, credentials, or arbitrary tool output.
Status notes should be concise, user-visible summaries.

After Go changes, run:

```sh
gofmt -w .
go test ./...
go vet ./...
```

After web changes, run the JavaScript syntax check and exercise the affected
API/UI path. Preserve keyboard access, reduced-motion behavior, and responsive
layouts.
