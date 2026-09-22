# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [Semantic Versioning](https://semver.org/). It stays on `0.x` until the API is stable.

## [Unreleased]

### Added
- Data model: projects, epic/story/task hierarchy, tasks with status and priority, agents with free-form
  `meta`, append-only activity, and a versioned state schema.

### Fixed
- `agentboard import` (and `DecodeFile`/`DecodeState` underneath it) no longer accepts arbitrary-but-valid JSON that
  isn't actually a board export or legacy board file. Previously, any JSON object missing the board's top-level fields
  (e.g. `{"hello":"world"}`) decoded into a zero-valued, internally-consistent `State` and silently replaced a live
  board with an empty one (exit 0, no warning). `DecodeState` now requires at least one of the documented top-level
  fields (`version`, `projects`, `tasks`, `agents`, `activity`, `next_activity_id`, `archived_through`) to be present
  before treating the input as a board at all; genuine exports and legacy pre-versioning files (which always name at
  least `projects`/`tasks`) are unaffected. The before-import backup (`board.json.before-import-<time>`) is unchanged.
