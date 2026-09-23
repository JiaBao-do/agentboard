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
- Run: `go run ./cmd/agentboard serve` then open the printed URL; `agentboard demo` fills it with an example.
- Local-only notes (`RUNNING.md`, `STATUS.md`) are git-ignored and must never contain paths in tracked files (leakcheck hook).

## Architecture
- `model/`: plain data types shared by server, client and the WASM UI (stdlib only, keep it tiny).
- Root package `agentboard`: `Board` (all state + rules, one mutex), `Store` (persistence), `Server`
  (REST + SSE + static UI), `Client` (used by the CLI and by agents), `Webhook` (generic outgoing events).
- Auth (AGENTBOARD-8): `pbkdf2.go` (stdlib-only PBKDF2-HMAC-SHA256), `auth.go` (`Board.Register`, email/password
  validation, domain allowlist), `login.go` (`Board.Authenticate`, `Board.User`, transparent hash upgrade),
  `sessions.go` (in-memory `sessionStore` + `loginLimiter`, owned by `Server`, not persisted), `auth_server.go`
  (the `/api/auth/*` HTTP handlers and the session cookie). `User` lives in `model.State` (schema v2); sessions do
  not - they are server-side, in-memory and ephemeral by design, so a restart logs everyone out.
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
5. Data lives in one directory: `board.json` is a versioned, CRC-32C checksummed, deflated container (`docs/DATA_FORMAT.md`);
   damage is detected, a newer version is refused (never misread), a corrupt file is never overwritten. Legacy JSON migrates
   once (kept as `.bak`). `export`/`import` (plain or gzip JSON) move a board between machines.
6. Config is flags and environment variables only (a JSON config file is on the roadmap); no hidden state outside the data dir.
   Default bind is 127.0.0.1.
Runtime guarantees:
7. One writer per data dir (`agentboard.lock` with PID/host/addr; stale owners are taken over, foreign ones never); writes are
   atomic (temp file, fsync, rename); old activity is archived, never dropped.
8. `Board` serialises all calls; persistence is write-behind but `Flush`/`Close` always leave the store current.
9. Every write names a real actor (never anonymous; `system` is reserved).
10. `POST /api/admin/shutdown` requires POST + `X-Agentboard-Action` header + same-origin + allowed Host/token.
11. User accounts authenticate identity for the web UI; they are **not** a new authorization boundary. Every
    existing endpoint keeps working exactly as before whether or not anyone is logged in - the same `Token`/
    `AllowedHosts`/loopback-bind protections (invariant 10 and `ServerOptions.Token`) are still the only access
    control, same as an `Agent`'s self-declared name always was (see invariant 9 and README "Things to care
    about"). Do not add a session requirement to any existing endpoint without a deliberate, separately-discussed
    decision: the explicit design goal was that CLI/agent workflows never need to log in.
12. Passwords: PBKDF2-HMAC-SHA256 only, stdlib-only (`crypto/hmac`, `crypto/sha256`, `crypto/rand`,
    `crypto/subtle`), never a reversible scheme, never `golang.org/x/crypto` (a dependency). Never hardcode an
    email domain anywhere in source, tests or docs (`AllowedEmailDomains`/`-allowed-email-domains` is the only
    way in); use `example.com`/`example.org` in every test and doc.
13. Sessions are a random ID in an in-memory table (`sessionStore`), never a JWT or anything self-describing:
    revocation (logout) must stay real, not "delete the client's copy and hope".

## Roadmap
- Drag and drop between columns, saved filters.
- More export formats (CSV), import from other trackers.
- Per-project WIP limits and lease policies.
- Optional plain-JSON config file; import through the API while the server runs; a byte-size archive trigger.
- Auth follow-ups (deliberately out of scope for AGENTBOARD-8): password change/reset, account deletion (and
  the session invalidation that should go with it), roles/permissions beyond plain identity, persistent/
  distributed rate limiting, TLS (documented as a reverse-proxy's job, not agentboard's).
