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

## Legacy files

Version 1 was plain indented JSON. On first load such a file is kept once, byte for byte, as `board.json.bak`
(an existing backup is never overwritten) and `board.json` is rewritten in version 2.

## State schema (`schema version 1`)

The payload is the `State` document: `version`, `projects`, `tasks`, `agents`, `activity`, `next_activity_id`.
`agentboard export` writes exactly this document as indented JSON. Field names are stable; renaming one needs a schema
version bump and a migration step in `DecodeState`.
