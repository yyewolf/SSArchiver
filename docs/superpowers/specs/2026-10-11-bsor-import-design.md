# SSArchiver — Local .bsor import (gap-fill) — Design

Date: 2026-10-11
Status: Approved in chat (approach 1); spec for review.
Builds on: 2026-10-08-ssarchiver-design.md and 2026-10-10-beatleader-design.md.

## 1. Purpose

Players keep local BeatLeader replays (`.bsor`, written by the game mod) that the
archive may lack: attempts BeatLeader never served (`replay: null` in its API),
replays it later dropped, or runs whose history was private when the player was
added. This design adds an admin upload that archives those files into the rows
that are already tracked — filling gaps only, reporting every file it could not
place.

Success criteria:

- An admin drops one or many `.bsor` files on **Manage**; each file is routed to
  its owning player automatically (the file's `playerId` is the BeatLeader
  account ID) and stored in the replay row that matches it.
- Only rows without an archived replay receive a file. Rows the worker is still
  downloading (`pending`) and rows already archived are never touched.
- Every file gets a visible outcome: imported (with a link to its page), already
  archived, or skipped with a specific reason. Nothing is silently dropped.
- No schema changes; no changes to feeds, polling, backfill or the download
  worker; the serving surface (pages, raw files, embeds) treats an imported
  replay like any other archived replay.

## 2. External facts (verified 2026-10-11, against 11 real local files and the live API)

### 2.1 Local replay format = BSOR v1 (BeatLeader's native format)

- Files start with int32 LE magic `0x442D3D69` (`"i=-D"`) and one version byte
  (`1`). Then sections follow as `[1 byte type][payload]`:
  `0=info, 1=frames, 2=notes, 3=walls, 4=heights, 5=pauses, 6=saberOffsets,
  7=customData`. Layout matches ArcViewer's `BsorDecoderV1.cs`
  (github.com/AllPoland/ArcViewer, `Assets/__Scripts/Loading/Replays/BeatLeader/Legacy`).
- `info` (verified field by field on all 11 files), little-endian, strings are
  `[int32 length][utf8 bytes]`:
  `version, gameVersion, timestamp, playerID, playerName, platform,
  trackingSystem, hmd, controller, hash, songName, mapper, difficulty` (13
  strings), then `int32 score`, then `mode, environment, modifiers` (3
  strings), then `float32 jumpDistance`, `bool leftHanded`, `float32 height,
  startTime, failTime, speed` (4 floats).
- The CDN replays SSArchiver already downloads are the **same format and often
  the same bytes**: the CDN file of a run and the local file of the same run
  were byte-identical (same SHA-256). No conversion is needed anywhere; the
  BeatLeader hosted viewer plays local-format files the same as CDN files.
  (The bundled ArcViewer WebGL build does not parse this format; BL rows
  already default to the BeatLeader hosted viewer, so nothing regresses.)
- `hash` is uppercase; database `leaderboards.song_hash` is lowercase.
- The mod's filenames are
  `{playerId}[-fail|-quit|-restart|-practice]-{songName}-{difficulty}-{mode}-{hash}-{timeset}.bsor`
  but song names contain dashes; only the end-type token right after the
  player ID is worth parsing from the filename. A renamed file must still
  import (everything needed is in the header), so filename data is a hint,
  never a requirement.

### 2.2 Matching facts (live API cross-check)

- File `timestamp` (string, unix seconds) is the **start** of the run (map
  load). BeatLeader's score `timeset` / attempt `timepost` is the run's **end**
  (+2–6 s posting delay). In the sampled session every PB was posted ~3–4 min
  after its local file's timestamp. Timestamp equality must not be used for
  matching.
- `info.score` equals the row's score exactly: the PB score for clear files
  (`modified == unmodified` without modifiers) and the attempt's score at the
  moment of the end for fail files.
- For fail files, `info.failTime` (seconds into the song) equals the attempt's
  `time` (stored as `end_time`) to the microsecond, and `failTime > 0` ⇔ the
  file is a fail. Non-fail files have `failTime == 0`; quit/restart/practice
   cannot be read from the header (filename token only). Score must match
   exactly: `header.Score = unmodified_score` (BeatLeader rows always carry
   it; a degenerate 0 simply fails to match and the file is skipped).
- BeatLeader itself stores pseudo-hashes for some OST maps (`Be There For You
  [OST II]` is listed by the API with hash `BeThereForYou` — verified live), so
  a local file's matching "placeholder" hash still matches the archive's
  leaderboard row. A hash the archive has no leaderboard for (genuinely
  unknown map, or a map difficulty the player's feeds never listed) cannot be
  matched and must be skipped with a reason.
- Attempts of the sampled player are public (the instance already archives
  them); `replay: null` attempt rows (`has_replay = false`,
  `replay_state = 'none'`) are exactly the rows this import fills.

## 3. Overview

One new admin flow, three small pieces:

1. `internal/bsor` — BSOR v1 header parser + filename end-type hint.
2. `internal/service` — `ImportReplay`: parse → route to the tracked player →
   match a replay-less row → store file → guarded mark.
3. `internal/web` — `POST /admin/replays/import` handler (streamed multipart)
   and an "Import replays" card on **Manage** with a per-file results panel.

## 4. Parser package `internal/bsor`

```go
type Header struct {
    PlayerID, PlayerName          string
    Timestamp                     int64  // unix seconds, run start
    Hash, SongName, Mapper        string
    Difficulty, Mode, Modifiers   string
    Score                         int64
    FailTime, Speed, StartTime    float64
}

func ParseHeader(r io.Reader) (Header, error)
func FilenameEndType(name string) (endType string, ok bool) // End* consts, "" if none
```

- `ParseHeader` reads magic + version, then walks sections; when it meets
  `info` (type 0) it decodes the fields listed in §2.1 and returns — it never
  reads frames/notes/walls. A wrong magic/version, truncated info, or a
  non-UTF8 string is an error; a reasonable size guard (e.g. strings ≤ 1 KiB,
  matching the header's realistic bounds) protects against garbage.
- `FilenameEndType` parses the leading `{digits}[-{fail|quit|restart|practice}-]`
  prefix; anything else is ignored ("" = no hint).
- Tests: synthetic fixtures built in Go (magic + version + info + a frames
  section stub), plus an env-gated test that parses real files from
  `SSA_BSOR_SAMPLES` when set (not committed). Fixture assertions use the
  values observed in §2.

## 5. Service: `ImportReplay` (internal/service/import.go)

```go
type ImportResult struct {
    File, Status, Reason       string // Status: "imported" | "already" | "skipped"
    PlayerID                   string
    ScoreID                    int64
    URL                        string // public page path, filled when imported
}

func (s *Service) ImportReplay(ctx context.Context, name string, r io.Reader) (ImportResult, error)
```

The `error` return is reserved for fatal conditions (cancelled context).
Per file, in order, every other failure — parse, match, storage or database —
becomes a `skipped` result with a specific reason:

1. **Parse** the header. Parse error → skip *"not a BSOR replay"*.
2. **Resolve the account**: `player_platforms` where `platform = 'beatleader'`
   and `external_id = header.PlayerID`. Missing → skip *"no tracked player for
   {id}"*. (Files self-route; the UI has no player selector.)
3. **Find the map**: `leaderboards` where `platform = 'beatleader'` and
   `lower(song_hash) = lower(header.Hash)` and `game_mode = header.Mode` and
   `difficulty_raw = header.Difficulty`. None → skip *"map not in the archive"*
   (covers the placeholder-hash case). Two leaderboards of the same
   hash+mode+raw name (BL contexts) are accepted; their rows are unioned.
4. **Candidate rows**: the player's `scores` on those leaderboards with
   `replay_state IN ('none', 'gone', 'failed')`.
   - If the map has rows but none in these states: if any row is `archived` →
     skip *"already archived"*; if any is `pending` → skip *"download in
     progress"*.
5. **Narrow by play**: end type from `FilenameEndType`, else `fail` when
   `FailTime > 0`, else unknown. Candidates must satisfy: rows of
   `kind = 'score'` only for clear/unknown files; `kind = 'attempt'` rows only
   with the file's end type (`fail` matches `end_type = 'fail'`; unknown files
   accept any attempt end type). Fails additionally require
   `|end_time − FailTime| ≤ 1.0` s. Score must match exactly:
   `header.Score = unmodified_score` (fallback `modified_score` when
   unmodified is 0 — ScoreSaber rows only, kept for symmetry; BL rows always
   carry both).
   - Exactly one candidate → step 6.
   - None → skip *"no matching play"* (message includes the parsed map name,
     difficulty, end type and score).
   - More than one (after the end-type and score narrowing) → skip
     *"ambiguous (N candidates)"*. Ties are not broken by timestamp (§2.2).
6. **Store and mark** (same order as the worker): `PutReplay(row, r)` (hashes
   and sizes the file), then a guarded update
   `UPDATE scores SET replay_state = 'archived', replay_size, replay_sha256,
   archived_at, next_attempt_at = NULL, last_error = '' WHERE id = ? AND
   replay_state IN ('none','gone','failed')`. `RowsAffected = 0` means the
   worker archived the row meanwhile: `Remove` the just-stored file and report
   *"already archived"*. Success → result `imported` with the row's page URL
   (`/s/bl/{externalID}` or `/s/bl/attempt/{externalID}` via the registry).
- Files are processed one at a time by the caller; a failure in one file never
  affects another. No `sync_events` are written; the results panel is the
  record and the refreshed row shows the new counts.
- `has_replay` is left as the platform reports it (a `replay: null` attempt
  stays `false`); `refreshPlay` on later syncs never resets
  `replay_state = 'archived'` (verified: it only touches the state when the
  platform newly offers a replay, and `ReplayURL` stays `nil` so no URL-changed
  noise). The serving surface gates on `replay_state`, so imported replays are
  downloadable and embeddable like any other archived replay.
- The storage-then-guard pattern mirrors the download worker: a crash between
  `PutReplay` and the update leaves an orphan file, which the existing
  storage/reconcile cleanup handles.

## 6. Web: handler and UI

- Route `POST /admin/replays/import` behind `requireAdmin` (internal/web).
- The handler reads `r.MultipartReader()` directly — never
  `ParseMultipartForm`, whose >10 MiB parts would spool to `/tmp` (absent or
  read-only in the scratch image). Parts are streamed into `ImportReplay`.
- Caps (enforced while streaming): 256 MiB per file, 4 GiB per request;
  a part over its cap is drained and skipped *"file too large"*.
- UI (Manage page, new card between "Add a player" and the table):
  title "Import replays", description explaining auto-routing and gap-fill,
  `<input type="file" multiple accept=".bsor">`, submit button
  (`hx-post`, `hx-encoding="multipart/form-data"`, target `#admin-players`).
  Zero files → error toast.
- Response: the refreshed `AdminPlayersTable` (new archived counts) plus an
  out-of-band **results panel** (`#import-results`, rendered inside the import
  card so the table swap cannot wipe it): one line per file —
  ✓ name → map [difficulty] imported (linking the score/attempt page),
  = already archived, ✗ skipped with the reason. Empty by default
  (`empty:hidden`).
- No CSRF/session work beyond the existing admin POST patterns.

## 7. Non-goals (this iteration)

- No conversion to ArcViewer's protobuf format (BL rows use the BeatLeader
  hosted viewer; CDN files are the same BSOR v1 format anyway).
- No scripted/API import endpoint (the service method is reusable later).
- No ScoreSaber `.dat` upload, no replacing an archived replay with an uploaded
  one, no import creating new rows, leaderboards or players, no re-uploads of
  `pending` rows, no sync-event log for imports.

## 8. Testing

- `internal/bsor`: synthetic fixtures (all info fields, wrong magic, truncated
  file, oversize string), `FilenameEndType` table, env-gated real-sample test
  (`SSA_BSOR_SAMPLES`, skipped when unset).
- `internal/service` (table-driven, fake platform/registry): PB clear import;
  fail import via `end_time`; filename end types (quit/practice); unknown
  end type with a single candidate; ambiguous skip; already archived (and the
  guard race where the state flips between match and mark, asserting the file
  is removed); pending skip; untracked player; unknown map; placeholder hash;
  superseded score rows pick the exact-score row.
- `internal/web` (existing env-test style): multipart POST imports a fixture,
  refreshes the row, renders the results panel lines; oversized part skipped;
  unauthenticated POST rejected.
- Manual pass on the dev instance with the 12 sampled files: imported files
  appear on the player page and play in the BeatLeader hosted viewer;
  "Be There For You" reports *"map not in the archive"*.
