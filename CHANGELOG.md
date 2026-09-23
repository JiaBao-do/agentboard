# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [Semantic Versioning](https://semver.org/). It stays on `0.x` until the API is stable.

## [Unreleased]

### Security
- **Fixed:** `GET /api/export` (and `agentboard export`/`dump` run without `-data`) returned every registered
  user's `password_hash` and `salt` unfiltered, with no authentication required beyond whatever already protects
  the endpoint - on the default loopback bind, any local process could read every account's PBKDF2 hash, which
  is exactly the input an offline dictionary/brute-force attack needs and completely bypasses the per-account
  login throttle (AGENTBOARD-11, found by an independent verification pass). `Board.Export` (the code this
  endpoint calls) now always drops the `users` map from a network export; the offline `export -data DIR` path
  (`Board.ExportWithCredentials`) is unaffected and keeps full account fidelity for real backups, since reaching
  it already needs filesystem access to the data directory. A network export/import cycle no longer carries
  accounts: re-register on the new board. See docs/PITFALLS.md #12.

### Added
- Timeline (Gantt) view foundation: `Task` gained optional `StartDate`/`EndDate` calendar dates (schema version 3;
  see docs/DATA_FORMAT.md), settable and clearable with `agentboard task update ID -start YYYY-MM-DD -end
  YYYY-MM-DD` (`-start clear`/`-end clear` removes one without touching the other) and over `PATCH /api/tasks/{id}`.
  `EndDate` is never before `StartDate` when both are set. A version 2 board migrates with no data change (tasks
  simply have no dates yet). `internal/view` gained the pure date math (month/week bucketing, bar position and
  width) the Timeline UI is built on, unit tested independently of the browser.
- User accounts (email + password) for the web UI, distinct from the existing, credential-less `Agent`: `Board.Register`,
  `Board.Authenticate`, and `POST /api/auth/{register,login,logout}` + `GET /api/auth/me`. Passwords are hashed with
  PBKDF2-HMAC-SHA256 (600,000 iterations, OWASP's current minimum; standard-library only - no new dependency), a
  random per-user salt, and a constant-time comparison; the server never stores or can recover a plaintext password.
  Logging in starts a server-side session (a random 256-bit ID in an in-memory table, never a JWT) carried by an
  HttpOnly, `SameSite=Strict` cookie, sliding-expiry (24h by default, `-session-ttl`); logging out invalidates it
  server-side, so a captured cookie stops working immediately, not just once it expires. Registration domains can be
  restricted with `-allowed-email-domains`/`AGENTBOARD_ALLOWED_EMAIL_DOMAINS` (comma separated, empty: any domain -
  this is a runtime setting only, never a hardcoded domain). Failed logins are throttled per account (10 per 15
  minutes) as a basic brute-force guard; see docs/PITFALLS.md for exactly what that does and does not defend against.
  **This is authentication only, not a new authorization layer**: every existing endpoint (tasks, agents, export,
  shutdown, ...) still works exactly as before, with or without anyone logged in - a logged-in session only changes
  which name the web UI's own writes are attributed under, so existing CLI/agent workflows need no changes at all.
  The UI gained matching register/log-in forms and a "signed in as ..." / "Log out" header state.

### Changed
- **Behavior change:** the default data directory (`serve -data`/`AGENTBOARD_DATA`, when neither is given) is now
  `./data` instead of the hidden `./.agentboard`. This only changes the *default*; an explicit `-data` flag or
  `AGENTBOARD_DATA` still wins exactly as before. Existing deployments that relied on the old default and pass no
  explicit `-data` will look in the wrong place after upgrading: either move `.agentboard/` to `data/`, or keep
  passing `-data .agentboard` (or set `AGENTBOARD_DATA=.agentboard`) to preserve the old location. File layout inside
  the directory is unchanged (one compressed/checksummed `board.json`, `agentboard.lock`, `archive/`).

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
