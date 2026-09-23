# agentboard

[![ci](https://github.com/JiaBao-do/agentboard/actions/workflows/ci.yml/badge.svg)](https://github.com/JiaBao-do/agentboard/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/JiaBao-do/agentboard.svg)](https://pkg.go.dev/github.com/JiaBao-do/agentboard)

A small, self-hosted, Jira-style board for people who run AI agents: see every task's status and **which agent is
working on it, and whether it is still running**. One static Go binary, no dependencies, web UI written in Go and
compiled to WebAssembly.

```
 agents / scripts / hooks                     you
   agentboard task claim ...   REST + SSE     browser
   agentboard agent heartbeat ─────────────▶ ┌────────────────────────────┐
   Go: agentboard.Client                     │ agentboard (one binary)    │
                                             │  Board: rules, leases      │──▶ board.json (compressed, checksummed)
   ◀── change events (SSE / webhook)         │  Server: API + embedded UI │──▶ archive/*.jsonl.gz
                                             └────────────────────────────┘
```

Agents claim tasks with a **lease** and send heartbeats. If an agent goes quiet its lease expires and the task returns to the
queue. Every change is attributed to a real actor (never anonymous) and shown on the card, in the task drawer and on the timeline.

## Install and run

```sh
go install github.com/JiaBao-do/agentboard/cmd/agentboard@latest   # Go 1.24+; or download a release binary
agentboard serve            # http://127.0.0.1:7878, data in ./data
agentboard demo             # in another terminal: fill it with a realistic example
```

## Use it from an agent

```sh
export AGENTBOARD_AGENT=my-agent               # who you are; anonymous writes are refused
agentboard project add DEMO "Demo"             # idempotent
id=$(agentboard task add -p DEMO -ensure "Rotate the logs")
agentboard task claim "$id" -lease 5m
agentboard agent heartbeat -task "$id"         # keep the lease alive, show what you are doing
agentboard task update "$id" -status review
agentboard task done "$id"
```

Runnable programs are in [`examples/`](examples): `quickstart` (embed the server, under 30 lines), `client-agent` (a Go
agent), `shell-agent` (`.sh` and `.ps1`), `webhook-receiver` (verifies signatures), `auth-demo` (register, log in, log
out, and confirm the existing unauthenticated agent workflow still works). Their outputs are checked by tests.
Integrations for Claude Code hooks and automation loops are docs only: [`docs/examples`](docs/examples).

## Features

- User accounts (email + password) for the web UI, separate from the existing self-declared `Agent`: register, log
  in and log out at `/api/auth/*` or the UI's Account panel. This is authentication only, not a new access-control
  layer - see "Things to care about" below and [`examples/auth-demo`](examples/auth-demo).
- Epic → story → task hierarchy, statuses `todo / in_progress / review / done / blocked`, priorities, labels.
- Timeline (Gantt) view: give any task an optional start/end date (`task update ID -start ... -end ...`) and see a
  project's phases as a month-by-month chart with weekly gridlines and proportional bars.
- Live board over Server-Sent Events, agent presence, activity timeline, dark/light theme, Stop button.
- REST API, Go client, CLI (`serve`, `task`, `agent`, `status`, `export`, `import`, `dump`, `demo`, `stop`), generic signed webhook.
- Write-behind saving with coalescing, retry and a visible Saved / Saving / Save failed state.
- Compressed, checksummed data file (28x smaller than JSON), data-directory lock, activity archive.

## Things to care about

Full list with wrong/right snippets and the tests that back each claim: [docs/PITFALLS.md](docs/PITFALLS.md). In short:

- Heartbeat at least once per lease, or the task is given away. All expiry uses the server clock.
- One writer per data directory; a second server or an offline command refuses and names the PID.
- Saving is asynchronous: a graceful stop loses nothing, a hard crash can lose up to 2 s. `-save-mode sync` trades speed for it.
- Binds to `127.0.0.1`. Never expose the port (or the shutdown endpoint) without a token and TLS in front.
- Agent names are self-declared: attribution, not access control. The same is true of a logged-in User session - it
  changes which name the UI's own writes are attributed under, not what any endpoint will accept; see point below.
- **User accounts have no TLS of their own either.** Passwords are hashed (PBKDF2-HMAC-SHA256, 600k iterations,
  standard library only) and never recoverable, but a password *submitted* to a non-loopback address over plain HTTP
  still travels in cleartext on the wire - put TLS in front (same as the shutdown endpoint above) before exposing
  login off of localhost, and set `-cookie-secure` once you do. Sessions are a random server-side token in memory
  (never a JWT): they do not survive a restart, and `-session-ttl` (24h default) is a sliding window. Logout really
  invalidates the session server-side. Registration domains are never hardcoded: `-allowed-email-domains` is opt-in
  and empty by default. Failed logins are throttled per account (10/15min) as a basic guard, not a substitute for a
  real rate limiter. Full detail, including exactly what the login throttle does and does not defend against: [docs/PITFALLS.md](docs/PITFALLS.md#10-user-accounts-sessions-and-passwords-agentboard-8).
- Timeline dates (`StartDate`/`EndDate`) are calendar dates, not timestamps: no time-of-day, no time zone, so a
  phase's date range reads the same everywhere the board is viewed.
- Behind a proxy, turn response buffering off for `/api/events`.
- `board.json` is binary: use `export`/`import`, back up the whole data directory.
- `app.wasm` and `wasm_exec.js` must come from the same Go release (committed pair: Go 1.27); rebuild with `go generate ./internal/webui`.

## Portability guarantees

1. Single static binary (`CGO_ENABLED=0`), everything embedded: no CDN, no external fonts, no runtime downloads.
2. No network calls except the ones you configure (the optional webhook). No telemetry.
3. Zero third-party Go dependencies: `go.mod` has no `require`.
4. Vendor-neutral: agents are generic (`name`, free-text `kind`, `meta`); product integrations live in docs only.
5. One data directory, [documented](docs/DATA_FORMAT.md), versioned formats; a newer version is refused, never misread.
6. Flags and environment variables only; default bind `127.0.0.1`.
7. Builds for linux, macOS and Windows on amd64 and arm64 (checked in CI on the Go 1.24 floor).

## Requirements

Go 1.24 or newer to build from source; released binaries need nothing. The committed WebAssembly UI was built with Go 1.27
(4.7 MB raw, about 1.3 MB gzipped on the wire; it is committed only when UI code changes to keep the repository small).

## License

MIT, see [LICENSE](LICENSE).
