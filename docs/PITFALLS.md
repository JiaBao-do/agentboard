# Things to care about

Each claim below is backed by a test (named in brackets) or by a command you can run.

## 1. Leases and heartbeats

An agent claims a task with a lease. Heartbeats extend every lease the agent holds. When heartbeats stop, the lease
expires and the task goes back to `todo`, attributed to `system`. [`TestLeaseExpiryReleasesTask`, `TestHeartbeatExtendsLease`]

```sh
# WRONG: claim once and go quiet. After 10 minutes (the default) your task is given away.
agentboard task claim LOOP-3
# RIGHT: heartbeat at least once per lease, and send the task you are working on.
agentboard agent heartbeat -task LOOP-3      # every minute or so
```

- **Presence is not the lease.** An agent shows *online* for 2 minutes after its last heartbeat (`-agent-ttl`); its task
  lease is separate (`-lease`, default 10 minutes, `task claim -lease 30m` per task).
- **Clock skew does not matter for agents.** All expiry arithmetic uses the *server's* clock; clients never send
  timestamps. The UI shows "lease 4m left" from the server's `now` in the snapshot, not from the browser clock.
- Moving a task out of `in_progress` (review, blocked, done) drops the lease but keeps the assignee, so the board still
  shows who did the work. Moving it back to `todo` clears the assignee. [`TestUpdate`]
- Only the holder can release or finish a leased task; anyone else gets 409. Once the lease has expired, anyone may claim it.
  [`TestClaim`, `TestReleaseAndComplete`]

## 2. One writer per data directory

`agentboard.lock` holds the owner's PID, host and address. A second `serve`, and the offline commands
`export -data`, `dump -data` and `import`, refuse while the owner is alive. [`TestSecondServeOnTheSameDirIsRefused`,
`TestOfflineCommandsRefuseWhileServerRuns`]

- A crash leaves a stale lock; the next start sees its PID is dead and takes over. [`TestStaleLockDoesNotBlockStart`]
- A lock written by another host (a data directory synced from another machine) is **never** taken over automatically:
  the PID means nothing here. Stop the other machine, then `serve -force-unlock` (works only when the owner is provably
  dead on this host; otherwise delete the lock file yourself once you are sure). [`TestLockRefusalsThatMustNotAutoRecover`]
- Do not put the data directory on a network share that several machines use at once.

## 3. Saving is asynchronous by default

Changes are written by a background writer after a short debounce (200 ms, never later than 2 s after the first change).
Many rapid updates become one write. [`TestAsyncSavesAreCoalesced`, `TestMaxLatencyCapsContinuousLoad`]

- A **graceful stop** (Ctrl-C, SIGTERM, `agentboard stop`, the UI Stop button) flushes everything: nothing is lost.
  [`TestCloseFlushesEverything`, `TestShutdownHappyPathKeepsDataAndReloads`]
- A **hard crash** (`kill -9`, power loss) can lose up to the max latency (2 s) of changes. The file itself is never left
  half written: writes are temp file, fsync, rename. [`TestCrashLosesAtMostTheWindow`,
  `TestFileStoreCrashBeforeRenameKeepsOldFile`]
- Need every change durable before the API answers? `serve -save-mode sync` (slower, holds the board lock during the write).
- If saving fails (full disk, permissions) the change stays pending, the writer retries with backoff, and the UI chip
  turns to "Save failed". Watch `agentboard status`. [`TestSaveFailureIsRetriedAndSurfaced`]

## 4. Do not expose it, and never expose the shutdown endpoint

The server binds `127.0.0.1` by default. A web page you visit can make your browser call `localhost`, so state-changing
requests need `Content-Type: application/json` (forces a CORS preflight the server never answers) and requests with an
unexpected `Host` are refused (DNS rebinding). [`TestRequestValidation`, `TestHostAllowList`]

```sh
# WRONG: a public bind without a token. agentboard refuses to start.
agentboard serve -addr 0.0.0.0:7878
# RIGHT: token + TLS in front (agentboard has no TLS of its own) + the host name you proxy on.
AGENTBOARD_TOKEN=$(head -c 24 /dev/urandom | base64) agentboard serve -addr 0.0.0.0:7878 -allow-host board.example.com
```

`POST /api/admin/shutdown` needs POST, an `X-Agentboard-Action: shutdown` header, a same-origin `Origin`, and an allowed
Host or a token. It refuses on a non-loopback listener without a token. Turn it off with `-disable-shutdown`.
[`TestShutdownEndpointRejections`, `TestServeRefusesShutdownOnPublicListenerWithoutToken`]

- The token travels in `Authorization: Bearer`; the browser event stream must use `?token=`, which can show up in proxy
  logs. Prefer not to put a logging proxy in front, or strip query strings.

## 5. Agent identity is self-declared

Any process that can reach the API can write as any agent name. Attribution ("who did it") is for humans reading the board,
not access control. Anonymous writes are refused (400 without an actor; the name `system` is reserved). [`TestWritesAreAttributedToTheActingAgent`]

```sh
# WRONG: no name, so the write is refused.        agentboard task add -p LOOP "x"
# RIGHT:                                          AGENTBOARD_AGENT=batchx-builder agentboard task add -p LOOP "x"
```

If you need to stop one agent impersonating another, run separate boards or put an authenticating proxy in front.

## 6. Live updates (SSE) and proxies

The UI refreshes over Server-Sent Events. Reverse proxies that buffer responses (`nginx` default) hold events back:
set `proxy_buffering off` for `/api/events` (the server already sends `X-Accel-Buffering: no`). The stream sends a tick every
15 s and the UI re-fetches every 20 s as a fallback, and reconnects on its own. After the Stop button the UI stops reconnecting.

## 7. Data files, versions and backups

- `board.json` is a **compressed, checksummed binary file**, not JSON. Do not hand-edit it; use `agentboard export`
  and `import`. [`TestDecodeFileDetectsDamage`, `FuzzDecodeFile`]
- A corrupt file makes the server refuse to start and leaves the file untouched so you can restore. A file written by a
  newer version is refused, never misread. [`TestCorruptDataFileIsNeverOverwritten`, `TestNewerSchemaInsideValidFileIsRefused`]
- Old plain-JSON files migrate on first load; the original is kept once as `board.json.bak`. [`TestLegacyJSONIsMigratedOnce`]
- **Back up the whole data directory** (`board.json` plus `archive/`), with the server stopped or via `agentboard export`.
  A file copy of a running server's `board.json` is safe (writes are atomic) but may miss the last 2 s.
- Old activity moves to `archive/activity-YYYYMM.jsonl.gz` past 5,000 entries; a task's timeline in the UI shows only
  live entries. `export -data DIR -with-archive` puts the full history back together. [`TestActivityIsArchivedAndNothingIsLost`]

## 8. Export and import semantics

`export` writes the whole board as readable, versioned JSON (`-gzip` optional), from the running server or, with `-data`,
straight from a stopped data directory. `import` **replaces** the board in a data directory (server must be stopped);
the previous file is kept as `board.json.before-import-<time>`. Input is validated first: a broken file is refused before
anything is touched. [`TestExportImportRoundTripAcrossMachines`]

## 9. The WebAssembly UI and the Go version

`app.wasm` and `wasm_exec.js` are committed and must come from the same Go release (the loader and the runtime are
version-coupled). The committed pair was built with Go 1.27. Rebuild both together with `go generate ./internal/webui`; the
library itself compiles from Go 1.24. The wasm is 4.6 MB raw and about 1.3 MB gzipped on the wire. Commit a rebuilt wasm
only when UI code changed: every version stays in git history forever. [`TestEmbeddedFiles`]
