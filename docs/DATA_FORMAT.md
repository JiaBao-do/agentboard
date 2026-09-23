# Data format

The data directory holds `board.json`. Despite the name it is a small binary container, not JSON.
You cannot hand-edit it; use `agentboard export` / `import` (readable, versioned JSON) to inspect or move a board.

## File layout, format version 2

| offset | size | field |
|---|---|---|
| 0 | 4 | magic `ABD2` |
| 4 | 1 | format version (`2`) |
| 5 | 8 | payload length n, big endian |
| 13 | n | payload: compact JSON of the state, deflate compressed (RFC 1951, default level) |
| 13+n | 4 | CRC-32C (Castagnoli) of the payload, big endian |

- Damage (flipped byte, truncation, appended data, bad deflate) is detected and reported as `ErrCorrupt`
  naming the failed check. A corrupt file is never overwritten: the server refuses to start so you can restore.
- A file with a newer format version, or an unknown magic, is refused (never guessed at).
- Decompression is capped at 1 GiB.
- Writes are atomic: temporary file in the same directory, fsync, rename.

## Why compact JSON + deflate

Measured on a seeded board (`go test -run TestReportFormatSizes -v`):

| board | indented JSON | compact JSON | gob | JSON+deflate (chosen) |
|---|---|---|---|---|
| 1,000 tasks, 5,000 activity | 1,482,150 B | 1,051,798 B | 588,114 B | 53,144 B (28x smaller, ~6 ms) |
| 10,000 tasks, 50,000 activity | 14,941,161 B | 10,640,809 B | 5,969,356 B | 526,671 B (28x smaller, ~55 ms) |

gob+deflate was no smaller than JSON+deflate, and JSON stays debuggable and stable across versions. BestSpeed
is 15% larger for a 3x faster encode; Best is 9% smaller for a 7x slower one. Encoding runs in the save queue's
writer goroutine, never on the request path.

## Activity archive

Once the live activity log exceeds `-max-activity` (default 5,000 entries) the oldest entries move to
`archive/activity-YYYYMM.jsonl.gz` (month of the entry, UTC). Each file is a series of gzip members, so appending is
a plain append; each member starts with `{"agentboard_archive":1}` and then one activity JSON object per line.
Archiving is at-least-once (a crash can duplicate entries); readers dedupe by `id` (`ReadArchive` does). The state
records `archived_through`. `agentboard export -data DIR -with-archive` merges archives back into one timeline.

## Locking

`agentboard.lock` in the data directory holds `{pid, host, started, addr, token}` (created with O_EXCL). One writer
per directory. A second server, and the offline commands `export -data`, `dump -data` and `import`, refuse while a live
process holds it and name its PID and address. A lock whose PID is dead on this host is taken over automatically; a lock from
another host or an unreadable one is never taken over automatically. `serve -force-unlock` removes only a lock whose
process is provably dead and never touches data files.

## Legacy files

Version 1 was plain indented JSON. On first load such a file is kept once, byte for byte, as `board.json.bak`
(an existing backup is never overwritten) and `board.json` is rewritten in version 2.

## State schema (`schema version 3`)

The payload is the `State` document: `version`, `projects`, `tasks`, `agents`, `users`, `activity`, `next_activity_id`.
`agentboard export` writes exactly this document as indented JSON (see "Exporting user accounts" below for what the
network-reachable `GET /api/export` leaves out). Field names are stable; renaming one needs a schema version bump and
a migration step in `DecodeState`.

Schema version 2 (AGENTBOARD-8) added `users`: human accounts with a hashed password, distinct from the
self-declared, credential-less `agents`. A user record is `{email, password_hash, salt, iterations, created_at,
updated_at}`; `password_hash` and `salt` are never anything the plaintext password could be recovered from (see
docs/PITFALLS.md #10). A version 1 file (no `users` field) migrates to an empty, non-nil `users` map on load; no
existing field changed shape, so a version 1 export still imports cleanly into version 2.

Schema version 3 (AGENTBOARD-6) added optional `start_date`/`end_date` to each task, for the Timeline (Gantt) view: a
task can have neither, either or both. Both are calendar dates, `"YYYY-MM-DD"`, with no time-of-day and no time zone
(Go type `model.Date`) - deliberately not a timestamp, since a plan phase like "QA - SIT: Mar 9 - Mar 20" means the
same calendar days everywhere it is read, not a specific instant that would shift with the reader's zone. When both
are set, `end_date` is never before `start_date` (enforced by `Board.Update` and again by `ValidateState`, so a
hand-edited import cannot violate it either). A version 2 file (no task has these fields) migrates by simply leaving
them unset (a nil `*Date` is already the correct "no date" value), so a version 2 export still imports cleanly into
version 3.

`DecodeState` (used by both `DecodeFile`'s legacy path and `import`) refuses any input whose top level names none of
these fields, rather than accepting it as a valid but empty board: a file that is syntactically valid JSON but not
actually a board export (say `{"hello":"world"}`) decodes to a zero-valued `State` with nothing internally
inconsistent about it, so without this check it would be silently imported as an empty board. A genuine legacy file
that predates the `version` field still names `projects` and `tasks`, so it is unaffected.

## Exporting user accounts

`agentboard export`/`dump` run **offline**, reading `board.json` straight off disk (the server must not be running):
getting this far already needs filesystem access to the data directory, which is at least as strong a bar as reading
`board.json` directly, so this path (`Board.ExportWithCredentials` underneath it) keeps full fidelity,
`password_hash`/`salt`/`iterations` included, so that moving a whole board - accounts and all - between machines
works as a real restore.

`GET /api/export` (and anything reachable over the network, including a same-host process that just hits the bound
port, and therefore `agentboard export`/`dump` run *without* `-data`) is a different trust boundary: reaching it
needs no filesystem access at all, and on the default loopback bind that is a low bar in practice - any local process
can hit `127.0.0.1:7878`. So this path (`Board.Export`) **never** includes `password_hash`, `salt` or `iterations`
for any `User`, regardless of `-token`/auth state: it drops the `users` map entirely rather than emit half-shaped
User records (email/timestamps but no credential) that `ValidateState` would then refuse to import at all - see
`docs/PITFALLS.md` #12. An export made this way still restores projects, tasks, agents and activity; accounts must be
re-registered on the new board. (An earlier version of this document reasoned that exposing `password_hash`/`salt`
was safe because the hash is not reversible; that reasoning was wrong - a leaked salted PBKDF2 hash is exactly the
input an offline dictionary/brute-force attack needs, and handing it out for free defeats the login rate limiter
entirely. AGENTBOARD-11.)
