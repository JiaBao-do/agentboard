# agentboard

A Jira-style task board for people who run AI agents: see every task's status and which agent is working
on it. Go library and single static binary, module `github.com/JiaBao-do/agentboard`. The web UI is Go
compiled to WebAssembly, embedded in the binary. Fills the gap of a small, self-hosted, dependency-free
task tracker with agent presence and leases.

## Commands
- Enable the push gate once per clone: `git config core.hooksPath .githooks` (never `--no-verify`).
- Test: `go test -race -shuffle=on ./...`
- Vet + WASM: `go vet ./... && GOOS=js GOARCH=wasm go vet ./cmd/agentboard-ui/`
- Lint: `golangci-lint run` (v2 config in `.golangci.yml`)
- Vuln: `govulncheck ./...`
- Rebuild the UI after touching `cmd/agentboard-ui`, `internal/view` or `model`: `go generate ./internal/webui`
  (commits `app.wasm` + `wasm_exec.js`; both must come from the same Go release).
- Run: `go run ./cmd/agentboard serve` then open the printed URL.

## Architecture
- `model/`: plain data types shared by server, client and the WASM UI (stdlib only, keep it tiny).
- Root package `agentboard`: `Board` (all state + rules, one mutex), `Store` (persistence), `Server`
  (REST + SSE + static UI), `Client` (used by the CLI and by agents), `Webhook` (generic outgoing events).
- `internal/webui/dist`: embedded UI (`index.html`, `boot.js`, `style.css`, `app.wasm`, `wasm_exec.js`).
- `cmd/agentboard-ui`: the UI, Go with `syscall/js`; only builds for `GOOS=js GOARCH=wasm`.
- `internal/view`: pure UI logic (grouping, time formatting), unit tested natively.
- `internal/cli` + `cmd/agentboard`: the command line.

## Conventions
- Requires **Go 1.24+** (`go.mod` says `go 1.24`, no toolchain line; CI runs 1.24 and `stable`). Develop on the latest Go but use **no API newer than 1.24** (no `WaitGroup.Go`, no `errors.AsType`); `go vet` (stdversion) enforces it, and the 1.24 CI job runs the full tests. **Zero third-party dependencies**, `go.mod` has no `require`.
- Every exported identifier is documented. Tests are table-driven; new behavior needs a new test.
- Errors are wrapped with `%w` and use the sentinels in `board.go`; the server maps them to HTTP codes.
- Conventional Commits, one per completed unit; each pushed commit passes the pre-push hook on a clean checkout.
- UI never uses `innerHTML`: agent-supplied text is untrusted. All text goes through `textContent`.

## Invariants (do not break)
Portability guarantees:
1. Single static binary: `CGO_ENABLED=0`, all assets embedded, no CDN, no fonts or scripts fetched at runtime.
2. No network calls except ones the user configures (the optional webhook). No telemetry.
3. Zero third-party Go dependencies.
4. Vendor-neutral: no hardcoded GitHub/Claude/Jira/etc. in core code. Agents are generic (`name`, free-text `kind`,
   `meta` map). Product-specific integrations live in `docs/` and `examples/` only.
5. Data lives in one directory as documented, versioned JSON (`docs/DATA_FORMAT.md`). A newer schema version is
   refused, never misread. `export`/`import` move a whole board between machines.
6. Config is flags and env vars, plus an optional plain-JSON config file; no hidden state outside the data dir.
   Default bind is 127.0.0.1.
Runtime guarantees:
7. One writer per data dir (lock file with PID); writes are atomic (temp file, fsync, rename).
8. `Board` serialises all calls; persistence is write-behind but `Flush`/`Close` always leave the store current.
9. `POST /api/admin/shutdown` requires POST + `X-Agentboard-Action` header + same-origin + allowed Host/token.

## Roadmap
- Drag and drop between columns, saved filters.
- More export formats (CSV), import from other trackers.
- Per-project WIP limits and lease policies.
