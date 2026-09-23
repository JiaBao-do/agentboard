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
the previous file is kept as `board.json.before-import-<time>`. Input is validated first: a broken file, or a file that
does not have the shape of a board at all, is refused before anything is touched. Syntactically valid but unrelated
JSON (say `{"hello":"world"}`) is **not** silently treated as an empty board just because it happens to decode into a
zero-valued, internally-consistent `State` — it is refused with "does not look like an agentboard export".
[`TestExportImportRoundTripAcrossMachines`]

## 9. The WebAssembly UI and the Go version

`app.wasm` and `wasm_exec.js` are committed and must come from the same Go release (the loader and the runtime are
version-coupled). The committed pair was built with Go 1.27. Rebuild both together with `go generate ./internal/webui`; the
library itself compiles from Go 1.24. The wasm is 4.6 MB raw and about 1.3 MB gzipped on the wire. Commit a rebuilt wasm
only when UI code changed: every version stays in git history forever. [`TestEmbeddedFiles`]

## 10. User accounts, sessions and passwords (AGENTBOARD-8)

**agentboard has no TLS of its own, and it binds `127.0.0.1` by default.** A password submitted to a non-loopback
address over plain HTTP travels in cleartext on the wire. User accounts are built for the same deployments this whole
project targets: localhost, or a TLS-terminating reverse proxy in front (see pitfall 4). Do **not** point a browser at
a raw, non-loopback `agentboard serve` and type a real password into it. If you do put a reverse proxy in front, also
set `-cookie-secure` (below) once that proxy speaks HTTPS to the browser - otherwise the session cookie itself is
still marked as sendable over plain HTTP.

```sh
# WRONG: real credentials over plain HTTP to a non-loopback address.
agentboard serve -addr 0.0.0.0:7878 -token $TOKEN
# RIGHT: TLS terminates in front (nginx/caddy/etc.), agentboard stays on loopback behind it, and the cookie is marked Secure.
agentboard serve -addr 127.0.0.1:7878 -cookie-secure
```

- **Passwords are never recoverable, only verifiable.** Hashing is PBKDF2-HMAC-SHA256 (`crypto/hmac`, `crypto/sha256`,
  `crypto/rand`, `crypto/subtle` - standard library only, no `golang.org/x/crypto`), 600,000 iterations (OWASP's
  current minimum for PBKDF2-HMAC-SHA256), a fresh random 16+ byte salt per user, and a constant-time comparison on
  verify. Two users with the same password get different hashes. [`TestPBKDF2SHA256KnownVectors`,
  `TestHashPasswordUniqueSaltAndHash`, `TestVerifyPasswordNearMiss`]
- **Domains are never hardcoded.** Restrict who can register with `-allowed-email-domains`/`AGENTBOARD_ALLOWED_EMAIL_DOMAINS`
  (comma separated); empty (the default) allows any syntactically valid email. There is no built-in domain anywhere in
  this source, tests or docs - only `example.com`/`example.org`. [`TestRegisterDomainAllowlist`]
- **Sessions are a random server-side token, not a JWT.** `POST /api/auth/login` sets an HttpOnly, `SameSite=Strict`
  cookie naming a 256-bit random ID in an in-memory table; nothing about who you are is encoded in the cookie itself,
  so it cannot be decoded, only looked up. That also means **sessions do not survive a restart** - a deliberate
  trade-off for a tool at this scale, not an oversight. `-session-ttl` (default 24h) is a *sliding* window: it resets
  on every validated request, so an idle browser is logged out, not an actively used one. [`TestSessionStoreLifecycle`,
  `TestSessionStoreSlidingExpiry`]
- **Logout really ends the session.** `POST /api/auth/logout` deletes the server-side record, so a captured or
  replayed cookie value stops working immediately - it is not merely told to the browser to forget. This is the
  practical difference between a real session and a stateless signed token, which cannot be revoked before it expires.
  [`TestLogoutInvalidatesSessionServerSide`]
- **Login is throttled per account, not per IP.** `POST /api/auth/login` refuses further attempts for one (normalized)
  email after 10 failures in 15 minutes. This is a basic, in-memory, best-effort guard against a single naive scripted
  attacker guessing one account's password - it is **not** a persistent or distributed rate limiter, does not survive
  a restart, and does nothing against an attacker trying many different emails at low volume each (a WAF or a
  reverse-proxy-level limiter is the right tool for that; out of scope here, consistent with this tool's
  solo/trusted-team threat model). [`TestLoginRateLimiting`, `TestLoginLimiter`]
- **Login and registration cannot be used to enumerate accounts.** An unknown email and a correct-email-wrong-password
  login both fail with the same error and, deliberately, about the same latency (an unknown email still pays a
  PBKDF2-sized cost against a fixed dummy hash) - a timing difference between the two would otherwise leak which
  emails are registered. [`TestAuthenticateUnknownEmailTakesSimilarTimeToWrongPassword`]
- **A session identifies who you are; it does not gate anything.** Every existing endpoint (tasks, agents, export,
  `admin/shutdown`, ...) works exactly the same whether or not anyone is logged in - the `Token`/`AllowedHosts`/
  loopback-bind protections in pitfall 4 are still the only access control. This is intentional (see CLAUDE.md
  invariant 11): existing CLI and agent workflows must never be forced to log in. Logging in only changes which name
  the *web UI's own writes* are attributed under (see pitfall 5: this was already attribution, not access control,
  for agents - a logged-in human account does not change that model, it just replaces a free-typed name with a
  verified one). [`TestAPIFlowThroughClient` still passes unauthenticated end to end]
- **No account-deletion or password-change endpoint yet** (see CLAUDE.md roadmap), so there is nothing yet that needs
  to invalidate *every* session for an account at once beyond a single logout. If you add one, invalidate every
  session for that email at the same time, not just the one that triggered it - a stolen session should not survive
  its own victim changing their password.

```sh
# curl equivalents (see examples/auth-demo for the same flow through the Go client):
curl -sS -c cookies.txt -X POST http://127.0.0.1:7878/api/auth/register \
  -H 'Content-Type: application/json' -d '{"email":"dev@example.com","password":"correct horse battery staple"}'
curl -sS -b cookies.txt http://127.0.0.1:7878/api/auth/me
curl -sS -b cookies.txt -X POST http://127.0.0.1:7878/api/auth/logout -H 'Content-Type: application/json' -d '{}'
```

## 11. Timeline dates are calendar dates, not timestamps (AGENTBOARD-6)

`Task.StartDate`/`EndDate` (`model.Date`) deliberately carry no time-of-day and no time zone: they marshal as plain
`"YYYY-MM-DD"`. This is a considered choice, not a missing feature.

- **A plan phase means the same days everywhere.** "QA - SIT: Mar 9 - Mar 20" is the same two weeks whether you read
  the board from Tokyo or from New York. If these were timestamps, "Mar 9 00:00" would silently mean different
  instants depending on which zone encoded it and which zone decoded it - exactly the kind of off-by-a-day bug a
  Gantt chart must never have. [`TestDateJSONRoundTrip`, `TestDateOf`]
- **Comparisons are calendar order, not duration.** `Date.Before`/`After`/`AddDays` operate on year/month/day; there
  is no way to ask "how many hours" between two Dates, only whole days (`view.DaysBetween`). Do not convert a `Date`
  to a `time.Time` and do duration arithmetic on it expecting DST-aware results - there is no DST in a calendar date.
- **A bar's end date is inclusive on screen, exclusive in the math.** If `EndDate` is March 9, the task occupies all
  of March 9 in the Timeline view; internally `internal/view.TaskRange` treats the displayed range as
  `[StartDate, EndDate+1day)` so bar-width arithmetic is plain day counting, never an off-by-one sliver.
  [`TestTaskRange`, `TestBarSpan`]
- **Either, both or neither may be set**, on any task type (epic, story or task): the Timeline view only cares
  whether a task has both dates, never what kind it is. A task with just one of the two is not shown as a bar (there
  is nothing to draw a range from) but is otherwise a completely normal task. [`TestUpdateDates`, `TestTimelineRowsOmitsUndatedAndOutOfWindow`]
- **`EndDate` is never before `StartDate`.** `Board.Update` rejects a patch that would produce that ordering (checked
  against the stored value for whichever end the patch does not touch, so changing only `StartDate` past an existing
  `EndDate` is rejected too) and `ValidateState` rejects it again on import, so a hand-edited file cannot smuggle in
  an inverted range. [`TestUpdateDates`, `TestValidateStateRejectsEndBeforeStart`]
