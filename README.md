# agentboard

A small, self-hosted, Jira-style board for people who run AI agents: see every task's status and
**which agent is working on it**. One static Go binary, no dependencies, web UI written in Go and
compiled to WebAssembly.

> Work in progress: the first release (v0.1.0) is being assembled unit by unit. See `CHANGELOG.md`.

## Why

Agents (or scripts, or people) claim tasks with a lease and send heartbeats. If an agent goes quiet its
lease expires and the task returns to the queue. The board shows columns, agent presence and a timeline
of who did what.

## Status

Model and data format are in place; the board engine, server, UI and CLI follow.

## License

MIT, see [LICENSE](LICENSE).
