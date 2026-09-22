# W-B: rclone Traversal Completeness / RC

Source: rclone git clone `--depth=1` at `/tmp/d03/rclone`, commit `cfb90e3` ("s3: fix version-at listings with URL encoded keys").
Investigation date: 2026-09-23.
Scope: answer the 10 investigation questions with source file paths and line numbers. FACT and INFERENCE are explicitly separated. Nothing is written as fact unless backed by a cited source line.

## Summary (INFERENCES only, derived from FACTS below)

- rclone's `Fs.List()` doc promises a *complete single directory* (not a recursive/provider-complete snapshot), and uses the word "should" (not "must").
- Recursive traversal is an optional feature (`ListR`); 4 of the 10 requested backends lack it and must fall back to per-directory `List` walking.
- The walk layer has two distinct error semantics: `walk` stops on the first callback error; `listRwalk` continues on directory-list errors and returns the last one at the end. Neither exposes a per-directory skipped/failed tally.
- `StatsInfo` has no `skippedDirs`/`failedDirs`/`totalDirs` fields; `operations/list` RC returns only a `list` array with no completeness marker, total count, or pagination cursor.
- No checkpoint/resume or scan-state persistence exists for traversal/sync. `--max-duration` stops the run with a fatal error; the next run re-lists from scratch.
- `ChangeNotify` on onedrive/drive/dropbox/box is polling (ticker) driven by a delta/cursor/events API token, not a push webhook.

## Q1. `Fs.List()` doc promise

FACT-1.1: `fs/types.go:20-29` — the `List` method comment reads: "List the objects and directories in dir into entries. The entries can be returned in any order but should be for a complete directory." and "This should return ErrDirNotFound if the directory isn't found."

FACT-1.2: `fs/types.go:29` — signature: `List(ctx context.Context, dir string) (entries DirEntries, err error)`.

INFERENCE-1.1: The doc promises completeness for a *single directory* (one level), not a recursive/provider-complete snapshot. The wording "should" (not "must") is a soft contract; a backend returning a partial directory without error would violate the intent but not be prevented by the type system.

INFERENCE-1.2: There is no return field indicating whether the directory listing was complete/truncated; completeness is implicit in `err == nil`.

## Q2. `ListR` optional feature and backend coverage

FACT-2.1: `fs/features.go:144-160` — `ListR ListRFn` is a field of `Features`; comment: "ListR lists the objects and directories of the Fs starting from dir recursively into out." and "Don't implement this unless you have a more efficient way of listing recursively that doing a directory traversal."

FACT-2.2: `fs/features.go:342-344` — `Fill` populates `ft.ListR` only if the Fs implements `ListRer` (`if do, ok := f.(ListRer); ok { ft.ListR = do.ListR }`).

FACT-2.3: `fs/types.go:314-316` — `ListRFn func(ctx context.Context, dir string, callback ListRCallback) error`.

FACT-2.4: Backend `ListR` presence among the 10 requested (cited line is the `func (f *Fs) ListR(` declaration):
- azureblob: YES — `backend/azureblob/azureblob.go:1509`
- s3: YES — `backend/s3/s3.go:2800`
- onedrive: YES — `backend/onedrive/onedrive.go:1507`
- drive: YES — `backend/drive/drive.go:2244`
- dropbox: NO (no `func (f *Fs) ListR(` in `backend/dropbox/`)
- box: NO (no `func (f *Fs) ListR(` in `backend/box/`)
- local: NO
- webdav: NO
- sftp: NO
- ftp: NO
- http: NO

INFERENCE-2.1: `ListR` is an optional capability. For backends without it (dropbox, box, local, webdav, sftp, ftp, http), rclone must perform recursive traversal by repeated per-directory `List` calls (see Q4/Q5 walk layer).

INFERENCE-2.2: A backend advertising `ListR` does not by itself prove provider-complete traversal; it only means the backend has a recursive listing code path. Completeness still depends on the backend's API returning the full set and on error handling (Q4/Q5).

## Q3. `ListP` (paginated list) backend coverage

FACT-3.1: `fs/features.go:162-175` — `ListP` field of `Features`; comment: "ListP lists the objects and directories of the Fs starting from dir non recursively to out." and "It should call callback for each tranche of entries read."

FACT-3.2: `fs/features.go:345-347` — `Fill` populates `ft.ListP` only if the Fs implements `ListPer`.

FACT-3.3: Backend `ListP` presence among the 10 requested:
- azureblob: YES — `backend/azureblob/azureblob.go:1466`
- s3: YES — `backend/s3/s3.go:2758`
- onedrive: YES — `backend/onedrive/onedrive.go:1465`
- drive: YES — `backend/drive/drive.go:2023`
- dropbox: YES — `backend/dropbox/dropbox.go:1082`
- box: YES — `backend/box/box.go:741`
- webdav: YES — `backend/webdav/webdav.go:939`
- local: NO
- sftp: NO
- ftp: NO
- http: NO

INFERENCE-3.1: `ListP` is more widely implemented than `ListR` among the requested backends. dropbox and box implement `ListP` but not `ListR`, so rclone can page their single-level listings but cannot use a single recursive call for them.

INFERENCE-3.2: `ListP` is non-recursive (per `fs/features.go:163`); it does not by itself provide recursive traversal — that is `ListR`'s role.

## Q4. `listRwalk` behavior on directory-list error

FACT-4.1: `fs/walk/walk.go:168-185` — `listRwalk` calls `Walk` with a callback. On error the callback does:
```
if err != nil {
    listErr = err
    err = fs.CountError(ctx, err)
    fs.Errorf(path, "error listing: %v", err)
    return nil
}
```
After `Walk` returns: `if listErr != nil { return listErr }; return walkErr`.

FACT-4.2: `fs/walk/walk.go:160` — `ListR` (the public entry) routes to `listRwalk` when `ListR` feature is unavailable or filters/bounded recursion force the fallback.

INFERENCE-4.1: `listRwalk` does **not** stop on a directory-list error: the callback returns `nil`, so `Walk` continues to other directories. The error is counted via `fs.CountError` and logged.

INFERENCE-4.2: `listErr = err` overwrites on each error (FACT-4.1), so the value returned at the end is the **last** directory-list error observed, not the first and not an aggregate. There is no list of failed directories returned.

INFERENCE-4.3: Partial-failure exposure is therefore: (a) a log line per failed directory, (b) a global error count via `CountError`, (c) a single terminal error return. There is no structured "failedDirs" output.

## Q5. `walk` (lowercase) error propagation

FACT-5.1: `fs/walk/walk.go:367-465` — `walk` runs `ci.Checkers` goroutines. On each job it calls `listDir`, then `fn(job.remote, entries, err)` (line 421). The error handling (lines 424-434):
```
if err != nil && err != ErrorSkipDir {
    traversing.Done()
    err = fs.CountError(ctx, err)
    fs.Errorf(job.remote, "error listing: %v", err)
    closeQuit()
    select {
    case errs <- err:
    default:
    }
    continue
}
```

FACT-5.2: `fs/walk/walk.go:382` — `errs := make(chan error, 1)` (buffer 1).

FACT-5.3: `fs/walk/walk.go:464` — `return <-errs` (returns the first error placed in the buffered channel, or nil).

FACT-5.4: `fs/walk/walk.go:24` — `ErrorSkipDir` is the documented way for the callback to skip a directory without stopping the walk.

INFERENCE-5.1: `walk` stops on the **first** callback error that is not `ErrorSkipDir`: it calls `closeQuit()` which signals all goroutines to exit (lines 384-393, 447-449).

INFERENCE-5.2: Because `errs` is buffered with capacity 1 and the send uses `select`/`default` (lines 430-433), only the **first** error is retained; subsequent errors are dropped.

INFERENCE-5.3: Contrast with Q4: `listRwalk` wraps the callback to swallow directory errors (return nil), so when `Walk` is invoked via `listRwalk`, a directory-list error does NOT trigger this stop path — the walk continues and `listRwalk` surfaces the last error itself. The stop-on-first-error behavior in `walk` is reached only when the *user callback* (not the directory list) returns an error.

## Q6. `StatsInfo` tracked statistics

FACT-6.1: `fs/accounting/stats.go:34-71` — `StatsInfo` struct fields include: `bytes`, `errors`, `lastError`, `fatalError`, `retryError`, `retryAfter`, `checks`, `checking`, `checkQueue`, `checkQueueSize`, `transfers`, `transferring`, `transferQueue`, `transferQueueSize`, `listed`, `renames`, `renameQueue`, `renameQueueSize`, `deletes`, `deletesSize`, `deletedDirs`, `updatedDirs`, `serverSideCopies`, `serverSideCopyBytes`, `serverSideMoves`, `serverSideMoveBytes`.

FACT-6.2: `fs/accounting/stats.go:110-159` — `RemoteStats` emits: `totalChecks`, `totalTransfers`, `totalBytes`, `transferTime`, `speed`, `bytes`, `errors`, `fatalError`, `retryError`, `checks`, `transfers`, `deletes`, `deletedDirs`, `updatedDirs`, `renames`, `listed`, `elapsedTime`, `serverSideCopies`, `serverSideCopyBytes`, `serverSideMoves`, `serverSideMoveBytes`, `eta`, `lastError`.

FACT-6.3: There is no field named `skippedDirs`, `failedDirs`, or `totalDirs` in the `StatsInfo` struct (FACT-6.1 enumerates all fields).

FACT-6.4: `fs/walk/walk.go:300` and `fs/walk/walk.go:476` — `accounting.Stats(ctx).Listed(int64(len(entries)))` is called inside the ListR callback, incrementing `listed` by the number of entries received per tranche.

INFERENCE-6.1: rclone tracks a single aggregate `listed` count (number of entries received from listing), not a directory-level completeness tally. There is no counter for directories that failed to list, directories skipped, or total directories expected.

INFERENCE-6.2: `errors` is a global error count (incremented via `CountError`), not a per-directory failure map. A caller cannot determine from `StatsInfo` which directories were not traversed.

## Q7. `operations/list` RC command return

FACT-7.1: `fs/operations/rc.go:27-56` — RC call registered at path `operations/list`, help text lists options: `fs`, `remote`, `opt` with sub-options `recurse`, `noModTime`, `showEncrypted`, `showOrigIDs`, `showHash`, `noMimeType`, `dirsOnly`, `filesOnly`, `metadata`, `hashTypes`. "Returns: - list - This is an array of objects as described in the lsjson command".

FACT-7.2: `fs/operations/rc.go:59-80` — `rcList` builds `list := []*ListJSONItem{}`, calls `ListJSON`, then sets `out["list"] = list` and returns. On `ListJSON` error it returns `nil, err` (lines 74-76).

FACT-7.3: `fs/operations/lsjson.go:244-249` — `ListJSON` calls `walk.ListR(ctx, fsrc, remote, false, ConfigMaxDepth(ctx, lj.opt.Recurse), walk.ListAll, ...)`.

FACT-7.4: The `operations/list` help text (FACT-7.1) and the `rcList` function (FACT-7.2) contain no input parameter for a page token/cursor and no output field named `total`, `complete`, `truncated`, `cursor`, or `nextToken`.

INFERENCE-7.1: `operations/list` returns a single `list` array and nothing else. There is no completeness marker, no total count, and no pagination cursor in either input or output.

INFERENCE-7.2: Because it routes through `walk.ListR` (FACT-7.3), the completeness semantics are those of Q1/Q4/Q5: a nil error means the walk completed without the callback stopping it; it does not carry an explicit provider-complete assertion.

INFERENCE-7.3: A caller wanting to detect partial failure must inspect the returned error only; there is no structured partial-result indicator in the successful-return path.

## Q8. Checkpoint / resume / scan-state persistence

FACT-8.1: A source-tree search for `checkpoint`, `resume`, `scan state`, `scanstate`, `persist progress`, `progress persist` across `fs/`, `cmd/`, `backend/` (non-test `.go` files) found no traversal/sync checkpoint mechanism. Matches were unrelated: `fmt.ScanState` (scanner interface), an NFS readdir cookie comment (`cmd/mount2/node.go:277`), per-backend upload-resume for single files (e.g. `backend/webdav/tus.go:71`, `backend/zoho/zoho.go` rate limiting), and a `// presume` comment.

FACT-8.2: `fs/sync/sync.go` contains no field or file path for persisting traversal progress between runs; the sync state (`syncCopyMove` struct, including `toBeChecked`, `toBeUploaded`, `toBeRenamed` pipes) is built fresh per invocation (see `newPipe` calls at `fs/sync/sync.go:184-195`).

INFERENCE-8.1: rclone has no checkpoint/resume mechanism for traversal or sync scan state. Each sync/list run begins with a fresh listing of the source and destination. A run interrupted before completion leaves no resumable scan artifact; the next run re-lists from the root.

INFERENCE-8.2: The `--backup-dir`/`--suffix` features exist but concern file-versioning on the destination, not traversal checkpointing, and are out of scope for this finding (not cited as checkpoint mechanisms).

## Q9. `ChangeNotify` backend coverage and polling vs webhook

FACT-9.1: `fs/features.go:93-96` — `ChangeNotify func(context.Context, func(string, EntryType), <-chan time.Duration)`; comment: "ChangeNotify calls the passed function with a path that has had changes. If the implementation uses polling, it should adhere to the given interval."

FACT-9.2: `fs/features.go:310-312` — `Fill` populates `ft.ChangeNotify` only if the Fs implements `ChangeNotifier`.

FACT-9.3: Backend `ChangeNotify` presence among the 10 requested:
- onedrive: YES — `backend/onedrive/onedrive.go:3078`
- drive: YES — `backend/drive/drive.go:3234`
- dropbox: YES — `backend/dropbox/dropbox.go:1614`
- box: YES — `backend/box/box.go:1285`
- s3, azureblob, local, webdav, sftp, ftp, http: NO

FACT-9.4: `backend/onedrive/onedrive.go:3078-3117` — implementation uses `time.NewTicker(pollInterval)` driven by `pollIntervalChan`; on each tick it calls `changeNotifyRunner` which advances a delta token (`changeNotifyStartPageToken` / `changeNotifyNextChange` using a Graph delta endpoint, `onedrive.go:3119-3137`).

FACT-9.5: `backend/drive/drive.go:3234-3276` — uses `time.NewTicker(pollInterval)`; on each tick calls `changeNotifyRunner` which uses the Drive API `Changes.List(pageToken)` (`drive.go:3294-3303`).

FACT-9.6: `backend/dropbox/dropbox.go:1614-1656` — uses `time.NewTicker(pollInterval)`; on each tick calls `changeNotifyRunner` with a cursor from `ListFolderGetLatestCursor` (`dropbox.go:1658-1668`).

FACT-9.7: `backend/box/box.go:1285-1339` — uses `time.NewTicker(pollInterval)`; on each tick calls `changeNotifyRunner` with a `stream_position` from the Box events endpoint (`box.go:1341-1344`, GET `/events`).

INFERENCE-9.1: All four requested backends with `ChangeNotify` (onedrive, drive, dropbox, box) implement it as **polling**: a `time.Ticker` advances a delta/cursor/stream-position token on each poll interval. None of these four implementations contain a webhook/push registration path in the cited source ranges.

INFERENCE-9.2: The polling interval is externally controlled via `pollIntervalChan <-chan time.Duration` (FACT-9.1), so the cadence is set by the caller, not the backend.

## Q10. `--max-duration` / interrupt recovery

FACT-10.1: `fs/config.go:361` and `fs/config.go:637` — config field `max_duration` mapped to `MaxDuration Duration`.

FACT-10.2: `fs/cutoffmode.go:18-21` — `CutoffModeHard`, `CutoffModeSoft`, `CutoffModeCautious`; `CutoffModeDefault = CutoffModeHard`.

FACT-10.3: `fs/sync/sync.go:196-197` — `if ci.MaxDuration > 0 { s.maxDurationEndTime = time.Now().Add(time.Duration(ci.MaxDuration)) }`.

FACT-10.4: `fs/sync/sync.go:203-218` — Hard mode applies a deadline to the main context (`s.ctx, s.cancel = context.WithDeadline(ctx, s.maxDurationEndTime)`); Soft/Cautious apply the deadline to the input context (`s.inCtx`) instead, so in-flight transfers continue but the list/check/transfer pipelines stop accepting new work.

FACT-10.5: `fs/sync/sync.go:29` and `fs/sync/sync.go:33` — `ErrorMaxDurationReached = errors.New("max transfer duration reached as set by --max-duration")` and `ErrorMaxDurationReachedFatal = fserrors.FatalError(ErrorMaxDurationReached)`.

FACT-10.6: `fs/sync/sync.go:1024-1028` — after the run, if the deadline was exceeded, `s.processError(ErrorMaxDurationReachedFatal)` is called. Because it is a `FatalError`, the retry loop will not retry (per rclone's fatal-error convention).

FACT-10.7: `cmd/cmd.go:504` — the top-level error handler has a case `errors.Is(err, fssync.ErrorMaxDurationReached)` (distinct handling from the fatal wrapper).

INFERENCE-10.1: `--max-duration` stops a sync in progress. Hard mode cuts transfers immediately via context deadline; Soft/Cautious let in-flight transfers finish but stop feeding new work (FACT-10.4).

INFERENCE-10.2: Reaching the max duration is terminal for that invocation (fatal, no retry within the same run — FACT-10.6). Combined with Q8 (no checkpoint), the next invocation starts over with a fresh full listing; there is no resume from where the previous run stopped.

INFERENCE-10.3: Therefore rclone's interrupt recovery model is idempotent re-execution (re-list + re-diff), not checkpoint-based resume. Correctness of a resumed run depends on the source/destination listing being repeatable, not on preserved traversal state.

## Cross-cutting observations

INFERENCE-X.1 (from Q1, Q4, Q5, Q6, Q7): rclone does not emit an explicit provider-complete assertion in any of its list/RC outputs. "Success" means `err == nil` after the walk completed; the completeness of the result is implicit in (a) the backend's `List`/`ListR` honoring its docstring and (b) no directory-list error having occurred. There is no machine-readable marker a caller can check to distinguish "complete" from "no error happened to surface".

INFERENCE-X.2 (from Q4, Q5): The two walk paths differ in partial-failure semantics. `walk` (used by `walkListDirSorted` and `walkNDirTree`) stops on the first callback error; `listRwalk` (the ListR-fallback path) continues past directory-list errors and returns the last one. A consumer cannot rely on uniform partial-failure behavior without knowing which path was taken.

INFERENCE-X.3 (from Q6, Q7): Neither `StatsInfo` nor `operations/list` exposes a per-directory failure/skip tally. To detect incomplete traversal a caller must rely on the single returned error and the global `errors` count, which do not identify which directories were missed.

INFERENCE-X.4 (from Q8, Q10): rclone has no traversal checkpoint/resume. `--max-duration` plus re-run is the only interrupt/recover pattern; it re-lists from the root each time.

## Limitations of this investigation

- Only the 10 requested backends were checked for ListR/ListP/ChangeNotify; other backends (e.g. b2, googlecloudstorage, swift, pcloud, etc.) were enumerated by grep but not individually cited.
- The grep for checkpoint/resume (Q8) covered `fs/`, `cmd/`, `backend/` non-test Go files; documentation prose outside those directories was not exhaustively searched for undocumented mechanisms.
- Whether a backend's `ListR`/`List` actually returns a provider-complete set depends on the remote API's behavior under pagination, rate limits, and eventual consistency; that is a per-backend runtime property not provable from the rclone source alone.
- `ChangeNotify` webhook capability: the four cited implementations are polling; this does not prove the remote APIs lack webhook support, only that rclone's client does not use a webhook path in these implementations.
