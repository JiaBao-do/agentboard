# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [Semantic Versioning](https://semver.org/). It stays on `0.x` until the API is stable.

## [Unreleased]

### Fixed
- The "Server stopped" screen no longer shows a fabricated restart command. It previously guessed
  `agentboard serve -addr <host>`, which is wrong (and unusable if copy-pasted) for anything not started as that
  exact raw binary invocation — `go run .`, a pm2 process, a systemd service, or a wrapper script all had the same
  wrong command shown. It now shows a generic, honest message pointing at the README's "Install and run" section or
  the reader's own process manager config instead of guessing.

## [0.1.1] - 2026-09-22

### Fixed
- `agentboard import` (and `DecodeFile`/`DecodeState` underneath it) no longer accepts arbitrary-but-valid JSON that
  isn't actually a board export or legacy board file. Previously, any JSON object missing the board's top-level fields
  (e.g. `{"hello":"world"}`) decoded into a zero-valued, internally-consistent `State` and silently replaced a live
  board with an empty one (exit 0, no warning). `DecodeState` now requires at least one of the documented top-level
  fields (`version`, `projects`, `tasks`, `agents`, `activity`, `next_activity_id`, `archived_through`) to be present
  before treating the input as a board at all; genuine exports and legacy pre-versioning files (which always name at
  least `projects`/`tasks`) are unaffected. The before-import backup (`board.json.before-import-<time>`) is unchanged.
  Found by an independent verification pass, not by a user report.

## [0.1.0] - 2026-09-21

Initial release: a self-hosted, single-binary task board for seeing which agent is working on what.

### Added
- Data model: projects, epic/story/task hierarchy, tasks with status and priority, agents with free-form
  `meta`, append-only activity, and a versioned state schema.
- REST API, Server-Sent Events for live updates, and a web UI (Go compiled to WebAssembly, embedded in the
  binary — no separate frontend to deploy).
- CLI: `serve`, `task` (add/list/claim/update/done), `agent heartbeat`, `status`, `demo`, `export`/`import`/`dump`.
- Task claim with a lease and heartbeat expiry, so a stale agent's task returns to the board automatically.
- A "Stop server" button in the UI with a confirmation step and CSRF-style protections (same-origin, custom
  header, POST-only).
- A compact, checksummed, compressed on-disk format with automatic migration from the original plain-JSON format.
- A single-writer lock on the data directory with stale-owner takeover, and an activity archive so the main
  data file stays small.
- Cross-platform release binaries (linux/darwin/windows × amd64/arm64) with `SHA256SUMS`.
