# Contributing

Issues and pull requests are welcome. For security reports, follow
[`SECURITY.md`](SECURITY.md) instead of opening a public issue.

## Development

Install the Go version declared in [`go.mod`](go.mod) and a Rust toolchain that
supports the version in [`desktop/src-tauri/Cargo.toml`](desktop/src-tauri/Cargo.toml).
Before opening a pull request, run:

```sh
gofmt -w .
go test -race ./...
go vet ./...
node --check web/app.js
node --check web/sw.js
node --check desktop/src/app.js
(cd desktop/src-tauri && cargo fmt --check && cargo clippy --locked -- -D warnings && cargo test --locked)
docker build -t taskboard:test .
```

Keep commits focused and include tests for behavior changes. Public examples
must use reserved domains (`example.com`, `example.net`, or `example.org`) and
documentation address ranges. Never commit real inventories, host variables,
tokens, push endpoints, private keys, internal names, or operational records.

Pull requests should explain the problem, chosen behavior, security or
operational implications, and how the change was verified.
